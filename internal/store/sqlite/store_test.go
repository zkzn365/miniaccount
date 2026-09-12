package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
)

// ---------------------------------------------------------------------------
// 测试环境
// ---------------------------------------------------------------------------

// memDBCounter 给每个测试一份独立的内存库。
var memDBCounter atomic.Int64

// uniqueMemoryDSN 返回一个只属于本次调用的内存库连接串。
func uniqueMemoryDSN() string {
	return fmt.Sprintf("file:testdb_%d?mode=memory&cache=shared",
		memDBCounter.Add(1))
}

// newTestDB 建一个内存账套：走完整的建账流程
// （迁移 + 账套信息 + 预置科目 + 会计期间）。
//
// 刻意复用 CreateBook 而不是自己拼装，这样测试验证的就是真实建账路径 ——
// 否则建账流程里的 bug 会被测试夹具悄悄绕过。
func newTestDB(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	// 用独立命名的内存库，而不是 ":memory:"。
	//
	// ":memory:" 走的是 `file::memory:?cache=shared` —— **进程级共享**的
	// 一份库。同一个包里第二个 newTestDB 会撞「账套已存在」，
	// 两个测试实际上在操作同一份数据（一个 Close 掉，另一个就废了）。
	// 大多数测试只有一个账套所以没暴露，但这是个随时会咬人的陷阱。
	db, err := Open(ctx, Options{Path: uniqueMemoryDSN()})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if _, err := db.CreateBook(ctx, CreateBookInput{
		CompanyName: "测试账套（小企业会计准则）",
		TaxType:     TaxTypeGeneral,
		StartYear:   2025, StartMonth: 1,
		ThroughYear: 2025,
		CurrentYear: 2025, CurrentMonth: 9,
	}); err != nil {
		t.Fatalf("建账失败: %v", err)
	}
	return db
}

func postingCtx(t *testing.T, db *DB) *ledger.Context {
	t.Helper()
	ctx := context.Background()
	tree, err := db.Accounts().Tree(ctx)
	if err != nil {
		t.Fatalf("加载科目树失败: %v", err)
	}
	cal, err := db.Periods().Load(ctx)
	if err != nil {
		t.Fatalf("加载期间表失败: %v", err)
	}
	kinds, err := db.Contacts().Kinds(ctx)
	if err != nil {
		t.Fatalf("加载往来档案失败: %v", err)
	}
	return &ledger.Context{Accounts: tree, Periods: cal, ContactKinds: kinds}
}

func mustContacts(t *testing.T, db *DB, kind, name string) int64 {
	t.Helper()
	var id int64
	err := db.WithTx(context.Background(), func(tx *Tx) error {
		var err error
		id, err = db.Contacts().Insert(context.Background(), tx, kind, name)
		return err
	})
	if err != nil {
		t.Fatalf("新增往来单位失败: %v", err)
	}
	return id
}

func money100(y int64) money.Money { return money.Money(y) * money.Yuan }

// ---------------------------------------------------------------------------
// 迁移与种子数据
// ---------------------------------------------------------------------------

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	v1, err := db.AppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 迁移版本应连续且从 1 开始
	if len(v1) == 0 || v1[0] != 1 {
		t.Fatalf("已应用迁移 = %v，期望从 1 开始", v1)
	}
	for i, v := range v1 {
		if v != i+1 {
			t.Fatalf("迁移版本应连续：%v", v1)
		}
	}
	// 重复迁移不应报错、不应重复应用
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等，得到 %v", err)
	}
	v2, _ := db.AppliedMigrations(ctx)
	if len(v2) != len(v1) {
		t.Errorf("重复迁移后版本数变了: %v → %v", v1, v2)
	}
}

func TestSeedChartOfAccounts(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	accts, err := db.Accounts().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accts) != 191 {
		t.Fatalf("科目数 = %d，期望 191", len(accts))
	}

	tree, err := db.Accounts().Tree(ctx)
	if err != nil {
		t.Fatalf("预置科目表应能构建成合法科目树: %v", err)
	}
	if len(tree.Roots()) != 66 {
		t.Errorf("一级科目数 = %d，期望 66", len(tree.Roots()))
	}

	// 抽查关键科目
	checks := []struct {
		code   string
		name   string
		root   account.RootType
		dir    account.BalanceDir
		isLeaf bool
		hasAux bool
	}{
		{"1002", "银行存款", account.RootAsset, account.DirDebit, true, false},
		{"1122", "应收账款", account.RootAsset, account.DirDebit, true, true},   // 客户
		{"1602", "累计折旧", account.RootAsset, account.DirCredit, true, false}, // 备抵
		{"224101", "其他应付款—股东", account.RootLiability, account.DirCredit, true, true},
		{"22210102", "应交增值税—销项税额", account.RootLiability, account.DirCredit, true, false},
		{"4001", "生产成本", account.RootCost, account.DirDebit, false, false}, // 有下级 → 汇总
		{"560207", "管理费用—差旅费", account.RootExpense, account.DirDebit, true, true},
		{"5801", "所得税费用", account.RootExpense, account.DirDebit, true, false},
	}
	for _, c := range checks {
		a, ok := tree.Get(c.code)
		if !ok {
			t.Errorf("科目 %s 不存在", c.code)
			continue
		}
		if a.Name != c.name {
			t.Errorf("%s 名称 = %q，期望 %q", c.code, a.Name, c.name)
		}
		if a.RootType != c.root {
			t.Errorf("%s root_type = %s，期望 %s", c.code, a.RootType, c.root)
		}
		if a.BalanceDir != c.dir {
			t.Errorf("%s 余额方向 = %s，期望 %s", c.code, a.BalanceDir, c.dir)
		}
		if a.IsLeaf != c.isLeaf {
			t.Errorf("%s is_leaf = %v，期望 %v", c.code, a.IsLeaf, c.isLeaf)
		}
		if a.RequiresAux() != c.hasAux {
			t.Errorf("%s 要求辅助核算 = %v，期望 %v", c.code, a.RequiresAux(), c.hasAux)
		}
	}

	// 成本类必须独立存在 —— 这正是 Frappe Books 缺的那一类
	costCount := 0
	for _, a := range accts {
		if a.RootType == account.RootCost {
			costCount++
		}
	}
	if costCount != 10 {
		t.Errorf("成本类科目数 = %d，期望 10", costCount)
	}

	// 幂等
	if err := db.SeedChartOfAccounts(ctx); err != nil {
		t.Fatalf("重复写入种子应幂等，得到 %v", err)
	}
	again, _ := db.Accounts().List(ctx)
	if len(again) != 191 {
		t.Errorf("重复写入后科目数 = %d，期望仍为 191", len(again))
	}
}

// ---------------------------------------------------------------------------
// 过账全链路
// ---------------------------------------------------------------------------

// ★ 这是本阶段最重要的测试：从建账到落总账的完整闭环。
func TestPostVoucherEndToEnd(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	pctx := postingCtx(t, db)
	shareholder := mustContacts(t, db, "shareholder", "张三")

	// 记一笔股东借款：借 银行存款 5 万，贷 其他应付款—股东 5 万
	v, err := voucher.New(voucher.WordJi, calendar.MustParse("2025-09-11"), "李会计")
	if err != nil {
		t.Fatal(err)
	}
	if err := v.AddEntries(
		ledger.Entry{AccountCode: "1002", Summary: "收到张三借款", Debit: money100(50000)},
		ledger.Entry{AccountCode: "224101", Summary: "收到张三借款", Credit: money100(50000),
			Aux: ledger.Aux{ContactID: &shareholder}},
	); err != nil {
		t.Fatal(err)
	}

	// 领域校验
	if _, err := v.Validate(pctx); err != nil {
		t.Fatalf("凭证应通过领域校验: %v", err)
	}

	// 过账
	accIDs, err := db.Accounts().IDsByCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Vouchers().Post(ctx, PostInput{
		Voucher:   v,
		Accounts:  accIDs,
		PostingBy: "王主管",
		At:        time.Date(2025, 9, 11, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("过账失败: %v", err)
	}
	if res.No != "记-2025-09-0001" {
		t.Errorf("凭证号 = %q，期望 记-2025-09-0001", res.No)
	}
	if len(res.LedgerIDs) != 2 {
		t.Errorf("总账分录数 = %d，期望 2", len(res.LedgerIDs))
	}

	// 重新读出，验证持久化
	got, err := db.Vouchers().Get(ctx, res.VoucherID)
	if err != nil {
		t.Fatalf("读回凭证失败: %v", err)
	}
	if got.Status != voucher.StatusPosted {
		t.Errorf("状态 = %s，期望 posted", got.Status)
	}
	if got.Seq != 1 || got.No != res.No {
		t.Errorf("字号 = %d/%q", got.Seq, got.No)
	}
	if got.PostedBy != "王主管" || got.PostedAt == nil {
		t.Error("记账签章未落库")
	}
	if got.CreatedBy != "李会计" {
		t.Errorf("制单人 = %q", got.CreatedBy)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("分录数 = %d，期望 2", len(got.Entries))
	}
	if got.Entries[1].Aux.ContactID == nil || *got.Entries[1].Aux.ContactID != shareholder {
		t.Error("辅助核算未落库 —— 往来账会因此丢数据")
	}

	// 试算平衡
	d, c, err := db.Vouchers().TrialBalance(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	if d != c || d != money100(50000) {
		t.Errorf("试算平衡 = %s / %s，期望各 50,000.00", d, c)
	}

	// 余额
	bal, err := db.Vouchers().Balance(ctx,
		calendar.MustParse("2025-09-01"), calendar.MustParse("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	if bal["1002"] != money100(50000) {
		t.Errorf("银行存款余额 = %s，期望 50,000.00", bal["1002"])
	}
	// 其他应付款是贷方科目，原始口径下应为 -50000
	if bal["224101"] != -money100(50000) {
		t.Errorf("其他应付款—股东余额(原始口径) = %s，期望 -50,000.00", bal["224101"])
	}

	// 明细账（其他应付款—股东明细账，需求点名的报表）
	rows, err := db.Vouchers().Detail(ctx, "224101",
		calendar.MustParse("2025-09-01"), calendar.MustParse("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("明细账行数 = %d，期望 1", len(rows))
	}
	if rows[0].VoucherNo != res.No || rows[0].Credit != money100(50000) {
		t.Errorf("明细账行 = %+v", rows[0])
	}
	if rows[0].ContactID == nil || *rows[0].ContactID != shareholder {
		t.Error("明细账应带出往来单位，否则无法按股东筛选")
	}
}

// 凭证号按期间连续分配
func TestVoucherNumberingSequence(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	accIDs, _ := db.Accounts().IDsByCode(ctx)

	post := func(month int, day int) string {
		v, err := voucher.New(voucher.WordJi,
			calendar.MustNew(2025, month, day), "李会计")
		if err != nil {
			t.Fatal(err)
		}
		_ = v.AddEntries(
			ledger.Entry{AccountCode: "1001", Summary: "提现", Debit: money100(100)},
			ledger.Entry{AccountCode: "1002", Summary: "提现", Credit: money100(100)},
		)
		res, err := db.Vouchers().Post(ctx, PostInput{
			Voucher: v, Accounts: accIDs, PostingBy: "王主管", At: time.Now()})
		if err != nil {
			t.Fatalf("过账失败: %v", err)
		}
		return res.No
	}

	// 同一期间内递增
	if got := post(9, 1); got != "记-2025-09-0001" {
		t.Errorf("第 1 张 = %q", got)
	}
	if got := post(9, 5); got != "记-2025-09-0002" {
		t.Errorf("第 2 张 = %q", got)
	}
	if got := post(9, 9); got != "记-2025-09-0003" {
		t.Errorf("第 3 张 = %q", got)
	}
	// 换期间后从 1 重新开始 —— 这是中国实务的要求，也是 Frappe 的全局计数器做不到的
	if got := post(8, 20); got != "记-2025-08-0001" {
		t.Errorf("8 月第 1 张 = %q，期望 记-2025-08-0001", got)
	}
	// 回到 9 月应继续递增而不是重号
	if got := post(9, 25); got != "记-2025-09-0004" {
		t.Errorf("9 月第 4 张 = %q", got)
	}
}

// 唯一索引兜底：即使绕过 nextSeq 也会被数据库拦住
func TestVoucherSeqUniqueConstraint(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	err := db.WithTx(ctx, func(tx *Tx) error {
		for i := 0; i < 2; i++ {
			_, err := tx.Exec(ctx, `
				INSERT INTO voucher (year, month, word, seq, no, biz_date, status,
					source, created_by, created_at, updated_at)
				VALUES (2025, 9, '记', 1, ?, '2025-09-01', 'posted', 'manual', 'x', 't', 't')`,
				"记-2025-09-0001")
			if err != nil {
				return err
			}
		}
		return nil
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("重号应被唯一索引拦住，得到 %v", err)
	}
}

func TestPostRejectsClosedPeriod(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	accIDs, _ := db.Accounts().IDsByCode(ctx)

	// 结掉 9 月
	if err := db.WithTx(ctx, func(tx *Tx) error {
		return db.Periods().SetStatus(ctx, tx, period.NewKey(2025, 9),
			period.StatusClosed, "王主管", ptrTime(time.Now()))
	}); err != nil {
		t.Fatal(err)
	}

	v, _ := voucher.New(voucher.WordJi, calendar.MustParse("2025-09-11"), "李会计")
	_ = v.AddEntries(
		ledger.Entry{AccountCode: "1001", Summary: "x", Debit: money100(1)},
		ledger.Entry{AccountCode: "1002", Summary: "x", Credit: money100(1)},
	)
	_, err := db.Vouchers().Post(ctx, PostInput{
		Voucher: v, Accounts: accIDs, PostingBy: "王主管", At: time.Now()})
	if !errors.Is(err, period.ErrNotOpen) {
		t.Fatalf("已结账期间应拒绝过账，得到 %v", err)
	}
}

// 期间状态以数据库为准，而不是内存中的 Calendar
func TestPeriodStatusComesFromDB(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	accIDs, _ := db.Accounts().IDsByCode(ctx)

	// 内存里的 Calendar 说 9 月是 open；把它改成 closed 后重新加载
	cal, _ := db.Periods().Load(ctx)
	if p, ok := cal.Get(2025, 9); !ok || !p.IsOpen() {
		t.Fatal("前置条件：9 月应为 open")
	}

	if err := db.WithTx(ctx, func(tx *Tx) error {
		return db.Periods().SetStatus(ctx, tx, period.NewKey(2025, 9),
			period.StatusClosed, "王主管", ptrTime(time.Now()))
	}); err != nil {
		t.Fatal(err)
	}

	v, _ := voucher.New(voucher.WordJi, calendar.MustParse("2025-09-15"), "李会计")
	_ = v.AddEntries(
		ledger.Entry{AccountCode: "1001", Summary: "x", Debit: money100(1)},
		ledger.Entry{AccountCode: "1002", Summary: "x", Credit: money100(1)},
	)
	// 传入的 accounts 映射故意用当前库，过账应因 DB 中期间已结账而失败
	_, err := db.Vouchers().Post(ctx, PostInput{
		Voucher: v, Accounts: accIDs, PostingBy: "王主管", At: time.Now()})
	if !errors.Is(err, period.ErrNotOpen) {
		t.Fatalf("应以数据库中的期间状态为准，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 事务性：过账失败必须不留痕迹
// ---------------------------------------------------------------------------

// ★ 这条直接对应 Frappe Books 的缺陷：它逐条 insert 总账分录且没有事务，
// 中途失败会留下「半张凭证的总账」。本工程必须做到要么全成、要么全滚。
func TestPostIsAtomic(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	accIDs, _ := db.Accounts().IDsByCode(ctx)

	// 构造一张第二行科目不存在的凭证 —— 第一行会写成功，第二行失败
	v, _ := voucher.New(voucher.WordJi, calendar.MustParse("2025-09-11"), "李会计")
	_ = v.AddEntries(
		ledger.Entry{AccountCode: "1002", Summary: "x", Debit: money100(100)},
		ledger.Entry{AccountCode: "9999", Summary: "x", Credit: money100(100)}, // 不存在
	)

	_, err := db.Vouchers().Post(ctx, PostInput{
		Voucher: v, Accounts: accIDs, PostingBy: "王主管", At: time.Now()})
	if err == nil {
		t.Fatal("不存在的科目应导致过账失败")
	}

	// 凭证表必须为空
	var nv int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM voucher`).Scan(&nv); err != nil {
		t.Fatal(err)
	}
	if nv != 0 {
		t.Errorf("过账失败后凭证表应清空，实际 %d 条（事务未回滚）", nv)
	}
	// 总账必须为空 —— 这正是 Frappe 会留下的「半张凭证」
	var nl int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM ledger_entry`).Scan(&nl); err != nil {
		t.Fatal(err)
	}
	if nl != 0 {
		t.Errorf("过账失败后总账应清空，实际 %d 条", nl)
	}
}

// ---------------------------------------------------------------------------
// 红字冲销（持久化）
// ---------------------------------------------------------------------------

func TestVoidByReversalPersisted(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	pctx := postingCtx(t, db)
	accIDs, _ := db.Accounts().IDsByCode(ctx)
	sh := mustContacts(t, db, "shareholder", "张三")

	// 原凭证
	v, _ := voucher.New(voucher.WordJi, calendar.MustParse("2025-09-11"), "李会计")
	_ = v.AddEntries(
		ledger.Entry{AccountCode: "1002", Summary: "收借款", Debit: money100(50000)},
		ledger.Entry{AccountCode: "224101", Summary: "收借款", Credit: money100(50000),
			Aux: ledger.Aux{ContactID: &sh}},
	)
	res, err := db.Vouchers().Post(ctx, PostInput{
		Voucher: v, Accounts: accIDs, PostingBy: "王主管", At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}

	// 构造并过账红字凭证
	orig, err := db.Vouchers().Get(ctx, res.VoucherID)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := orig.BuildReversal("王主管", orig.BizDate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rev.Validate(pctx); err != nil {
		t.Fatalf("冲销凭证应通过校验: %v", err)
	}
	rres, err := db.Vouchers().Post(ctx, PostInput{
		Voucher: rev, Accounts: accIDs, PostingBy: "王主管", At: time.Now()})
	if err != nil {
		t.Fatalf("过账红字凭证失败: %v", err)
	}
	if rres.No != "记-2025-09-0002" {
		t.Errorf("红字凭证号 = %q", rres.No)
	}

	// 原凭证标记为已作废，并与红字凭证互指
	if err := db.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE voucher SET status = 'voided', voided_by = ? WHERE id = ?`,
			rres.VoucherID, res.VoucherID); err != nil {
			return err
		}
		// 原凭证的分录标记为「所属凭证已作废」—— 仅作审计标记，
		// 它们仍参与汇总，由红字凭证抵消
		_, err := tx.Exec(ctx,
			`UPDATE ledger_entry SET reverted = 1 WHERE voucher_id = ?`, res.VoucherID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// 冲销后：原凭证 + 红字凭证互相抵消，余额回到 0
	bal, err := db.Vouchers().Balance(ctx,
		calendar.MustParse("2025-09-01"), calendar.MustParse("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	if bal["1002"] != 0 {
		t.Errorf("冲销后银行存款余额 = %s，期望 0", bal["1002"])
	}

	// 红字凭证本身仍在账上（审计轨迹）
	all, err := db.Vouchers().ListByPeriod(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("期间内凭证数 = %d，期望 2（原凭证 + 红字凭证都在）", len(all))
	}

	// 两张凭证合计为零 —— 冲销的定义
	origRead, _ := db.Vouchers().Get(ctx, res.VoucherID)
	revRead, _ := db.Vouchers().Get(ctx, rres.VoucherID)
	if !origRead.IsBalanced() || !revRead.IsBalanced() {
		t.Error("两张凭证都应各自平衡")
	}
}

// ---------------------------------------------------------------------------
// 期间管理（持久化）
// ---------------------------------------------------------------------------

func TestPeriodRepoRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	cal, err := db.Periods().Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cal.Len() != 12 {
		t.Fatalf("期间数 = %d，期望 12", cal.Len())
	}
	if cal.BookStart() != period.NewKey(2025, 1) {
		t.Errorf("启用期间 = %v", cal.BookStart())
	}
	if p, ok := cal.Get(2025, 9); !ok || !p.IsOpen() {
		t.Error("9 月应为 open")
	}
	if p, ok := cal.Get(2025, 10); !ok || !p.IsFrozen() {
		t.Error("10 月应为 future")
	}

	// 结账后重新加载应保持
	if err := db.WithTx(ctx, func(tx *Tx) error {
		return db.Periods().SetStatus(ctx, tx, period.NewKey(2025, 1),
			period.StatusClosed, "王主管", ptrTime(time.Now()))
	}); err != nil {
		t.Fatal(err)
	}
	cal2, _ := db.Periods().Load(ctx)
	p, _ := cal2.Get(2025, 1)
	if !p.IsClosed() || p.ClosedBy != "王主管" || p.ClosedAt == nil {
		t.Errorf("重新加载后 1 月状态 = %+v", p)
	}
}

// 期间表必须连续，否则顺序结账规则不成立
func TestFromRowsRejectsGap(t *testing.T) {
	_, err := period.FromRows(
		[]period.Key{period.NewKey(2025, 1), period.NewKey(2025, 3)},
		nil, nil)
	if !errors.Is(err, period.ErrBadPeriodRange) {
		t.Fatalf("缺期间应报错，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 科目维护
// ---------------------------------------------------------------------------

func TestAccountInsertAndDisable(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	// 在 1002 下新增一个开户行明细
	err := db.WithTx(ctx, func(tx *Tx) error {
		_, err := db.Accounts().Insert(ctx, tx, &account.Account{
			Code: "100201", Name: "银行存款—工商银行", ParentCode: "1002",
			Level: 2, IsLeaf: true, RootType: account.RootAsset,
			BalanceDir: account.DirDebit, IsEnabled: true,
		})
		return err
	})
	if err != nil {
		t.Fatalf("新增明细科目失败: %v", err)
	}

	tree, err := db.Accounts().Tree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 1002 有了下级，必须变成汇总科目（不可记账）
	if tree.MustGet("1002").IsLeaf {
		t.Error("1002 新增下级后应变为汇总科目")
	}
	if err := tree.CheckPostable("1002"); err == nil {
		t.Error("1002 成为汇总科目后不应允许记账")
	}
	if err := tree.CheckPostable("100201"); err != nil {
		t.Errorf("新明细科目应可记账，得到 %v", err)
	}

	// 停用
	if err := db.WithTx(ctx, func(tx *Tx) error {
		return db.Accounts().SetEnabled(ctx, tx, "100201", false)
	}); err != nil {
		t.Fatal(err)
	}
	tree2, _ := db.Accounts().Tree(ctx)
	if err := tree2.CheckPostable("100201"); err == nil {
		t.Error("停用科目不应允许记账")
	}

	// 不存在的科目
	if err := db.WithTx(ctx, func(tx *Tx) error {
		return db.Accounts().SetEnabled(ctx, tx, "9999", false)
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("停用不存在的科目应报 ErrNotFound，得到 %v", err)
	}
}

// 重复编码必须被唯一约束拦住
func TestAccountCodeUnique(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	err := db.WithTx(ctx, func(tx *Tx) error {
		_, err := db.Accounts().Insert(ctx, tx, &account.Account{
			Code: "1001", Name: "重复的库存现金", Level: 1, IsLeaf: true,
			RootType: account.RootAsset, BalanceDir: account.DirDebit, IsEnabled: true,
		})
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("重复编码应报 ErrConflict，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 文件库
// ---------------------------------------------------------------------------

func TestFileDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "账套", "测试账套.db")

	db, err := Open(ctx, Options{Path: path, CreateDirs: true})
	if err != nil {
		t.Fatalf("打开文件库失败（中文路径）: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.SeedChartOfAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM account`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 191 {
		t.Errorf("科目数 = %d，期望 191", n)
	}

	// WAL 模式已启用
	var mode string
	if err := db.SQL().QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q，期望 wal", mode)
	}

	// 外键已启用
	var fk int
	if err := db.SQL().QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Error("外键未启用")
	}
}

func TestForeignKeyEnforced(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	// 引用不存在的科目应被外键拦住
	err := db.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO ledger_entry (biz_date, year, month, account_id, debit, credit,
				voucher_id, line_no, summary, reverted, created_at)
			VALUES ('2025-09-01', 2025, 9, 999999, 100, 0, 1, 1, 'x', 0, 't')`)
		return err
	})
	if err == nil {
		t.Fatal("引用不存在的科目应被外键拦住")
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func ptrTime(t time.Time) *time.Time { return &t }

// newEmptyDB 打开一个空库并跑完迁移，但**不建账**。
//
// newTestDB 会顺带建账，于是测不了 CreateBook 本身
// （第二次调用必然撞「账套已存在」）。
func newEmptyDB(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: uniqueMemoryDSN()})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// newDBAtVersion 把库迁移到**指定版本为止**，用于测试后续迁移。
//
// 测迁移只能在「迁移前的库」上测：ALTER TABLE ADD COLUMN 不可重复执行，
// 在已经跑过 0010 的库上重跑只会撞 duplicate column，
// 那样什么也证明不了。
func newDBAtVersion(t *testing.T, maxVersion int) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: uniqueMemoryDSN()})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, migrationTableDDL); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.Version > maxVersion {
			break
		}
		if _, err := db.sql.ExecContext(ctx, m.SQL); err != nil {
			t.Fatalf("执行迁移 %04d_%s 失败: %v", m.Version, m.Name, err)
		}
		if _, err := db.sql.ExecContext(ctx,
			`INSERT INTO schema_migration (version, name, applied_at) VALUES (?,?,?)`,
			m.Version, m.Name, nowString()); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
