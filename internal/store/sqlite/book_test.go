package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
)

// ---------------------------------------------------------------------------
// 建账
// ---------------------------------------------------------------------------

func TestCreateBook(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "账套", "测试公司.db")

	db, err := Open(ctx, Options{Path: path, CreateDirs: true})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// 建账前应报告未初始化
	if _, err := db.Books().Get(ctx); !errors.Is(err, ErrBookNotSetup) {
		t.Fatalf("未建账时应报 ErrBookNotSetup，得到 %v", err)
	}

	book, err := db.CreateBook(ctx, CreateBookInput{
		CompanyName: "杭州某某科技有限公司",
		CreditCode:  "91330100MA2XXXXXXX",
		LegalPerson: "张三",
		Address:     "杭州市西湖区某某路 1 号",
		Phone:       "0571-88888888",
		Email:       "finance@example.com",
		BankName:    "中国工商银行杭州分行",
		BankAccount: "1202 0000 0000 0000",
		TaxType:     TaxTypeGeneral,
		StartYear:   2025, StartMonth: 1,
		ThroughYear: 2026,
		CurrentYear: 2025, CurrentMonth: 9,
	})
	if err != nil {
		t.Fatalf("建账失败: %v", err)
	}
	if book.CompanyName != "杭州某某科技有限公司" {
		t.Errorf("公司名 = %q", book.CompanyName)
	}
	if book.Standard != "小企业会计准则" {
		t.Errorf("准则 = %q，期望「小企业会计准则」", book.Standard)
	}
	if book.BaseCurrency != "CNY" {
		t.Errorf("本位币 = %q，期望 CNY", book.BaseCurrency)
	}
	if book.StartPeriod() != period.NewKey(2025, 1) {
		t.Errorf("启用期间 = %v", book.StartPeriod())
	}

	// 科目表已预置
	accts, err := db.Accounts().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accts) != 191 {
		t.Errorf("科目数 = %d，期望 191", len(accts))
	}

	// 期间表已生成：2025-01 .. 2026-12 = 24 个
	cal, err := db.Periods().Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cal.Len() != 24 {
		t.Errorf("期间数 = %d，期望 24", cal.Len())
	}
	// 到 2025-09 为止是 open
	if got := len(cal.OpenPeriods()); got != 9 {
		t.Errorf("已启用期间 = %d，期望 9", got)
	}

	// 重复建账应被拒绝
	if _, err := db.CreateBook(ctx, CreateBookInput{
		CompanyName: "另一家公司", StartYear: 2025, StartMonth: 1,
	}); !errors.Is(err, ErrBookExists) {
		t.Errorf("重复建账应报 ErrBookExists，得到 %v", err)
	}
}

func TestCreateBookValidation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		in   CreateBookInput
	}{
		{"空公司名", CreateBookInput{StartYear: 2025, StartMonth: 1}},
		{"月份非法", CreateBookInput{CompanyName: "X", StartYear: 2025, StartMonth: 13}},
		{"年份非法", CreateBookInput{CompanyName: "X", StartYear: 1800, StartMonth: 1}},
		{"纳税人身份非法", CreateBookInput{CompanyName: "X", StartYear: 2025, StartMonth: 1, TaxType: "bogus"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := db.CreateBook(ctx, c.in); err == nil {
				t.Error("应当报错")
			}
		})
	}
}

// ★ 建账必须是原子的：任何一步失败都不能留下半个账套
func TestCreateBookIsAtomic(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// 先塞一个 book 行，让 CreateBook 里的 INSERT 撞主键失败
	if err := db.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO book (id, company_name, start_year, start_month, created_at, updated_at)
			VALUES (1, 'X', 2025, 1, 't', 't')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	_, err = db.CreateBook(ctx, CreateBookInput{
		CompanyName: "新公司", StartYear: 2025, StartMonth: 1, ThroughYear: 2025,
	})
	if err == nil {
		t.Fatal("应因主键冲突失败")
	}

	// 科目与期间都不应被写入（事务已回滚）
	var n int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM account`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("建账失败后科目数应为 0，实际 %d（事务未回滚）", n)
	}
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM period`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("建账失败后期间数应为 0，实际 %d", n)
	}
}

func TestBookUpdate(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t) // 夹具已通过 CreateBook 建好账套

	b, err := db.Books().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b.CompanyName = "改名后的公司"
	b.LegalPerson = "李四"
	b.SetupComplete = true
	if err := db.Books().Update(ctx, b); err != nil {
		t.Fatalf("更新账套失败: %v", err)
	}

	got, _ := db.Books().Get(ctx)
	if got.CompanyName != "改名后的公司" || got.LegalPerson != "李四" {
		t.Errorf("更新后 = %+v", got)
	}
	if !got.SetupComplete {
		t.Error("setup_complete 应已置位")
	}
}

// ---------------------------------------------------------------------------
// 结账前体检
// ---------------------------------------------------------------------------

func TestPeriodHealthAllGood(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	h, err := db.CheckPeriodHealth(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatalf("体检失败: %v", err)
	}
	if !h.CanClose() {
		for _, it := range h.Errors() {
			t.Errorf("不应有阻断性问题: %s — %s", it.Title, it.Detail)
		}
		t.Fatal("体检应通过")
	}
	// 应覆盖全部 6 项检查
	if len(h.Items) != 6 {
		t.Errorf("体检项数 = %d，期望 6", len(h.Items))
	}
	keys := map[string]bool{}
	for _, it := range h.Items {
		keys[it.Key] = true
	}
	for _, want := range []string{"trial_balance", "draft_vouchers", "voucher_sequence",
		"balance_sheet", "negative_cash", "contact_direction"} {
		if !keys[want] {
			t.Errorf("缺少体检项 %q", want)
		}
	}
	// 试算平衡项应给出具体数字
	for _, it := range h.Items {
		if it.Key == "trial_balance" && it.Level != HealthOK {
			t.Errorf("试算平衡应通过，得到 %s", it.Detail)
		}
	}
}

func TestPeriodHealthDetectsDrafts(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	// 手工插一张草稿凭证
	if id := newDraftVoucher(t, db, "2025-09-28"); id == 0 {
		t.Fatal("未能创建草稿凭证")
	}

	h, err := db.CheckPeriodHealth(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range h.Items {
		if it.Key == "draft_vouchers" {
			found = true
			if it.Level != HealthWarn {
				t.Errorf("草稿应为警告级，得到 %s", it.Level)
			}
			if it.Count != 1 {
				t.Errorf("草稿数 = %d，期望 1", it.Count)
			}
		}
	}
	if !found {
		t.Error("应检出草稿凭证项")
	}
	// 草稿只是警告，不阻断结账
	if !h.CanClose() {
		t.Error("草稿不应阻断结账")
	}
}

func TestPeriodHealthDetectsNegativeCash(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	f := seedBook(t, db)

	// 记一笔「付了不存在的钱」：贷 银行存款 200,000（账面只有 138,000）
	postSimple(t, db, f, "2025-09-29",
		entry{code: "560210", summary: "大额支出", debit: money100(200000), dept: ptrI64(1)},
		entry{code: "1002", summary: "大额支出", credit: money100(200000)},
	)

	h, err := db.CheckPeriodHealth(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range h.Items {
		if it.Key == "negative_cash" {
			found = true
			if it.Level != HealthWarn {
				t.Errorf("现金负余额应为警告级，得到 %s", it.Level)
			}
			t.Logf("检出：%s", it.Detail)
		}
	}
	if !found {
		t.Error("应检出银行存款贷方余额")
	}
}

func TestPeriodHealthDetectsImbalance(t *testing.T) {
	ctx := context.Background()
	// 这个用例需要「未建账」的空库，因此不能走 newTestDB
	db, err := Open(ctx, Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateBook(ctx, CreateBookInput{
		CompanyName: "测试", StartYear: 2025, StartMonth: 1, ThroughYear: 2025,
		CurrentYear: 2025, CurrentMonth: 9,
	}); err != nil {
		t.Fatal(err)
	}

	// 直接往总账里塞一条只有借方的分录，制造不平
	var accID int64
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT id FROM account WHERE code = '1001'`).Scan(&accID); err != nil {
		t.Fatal(err)
	}
	v := insertRawVoucher(t, db, 2025, 9, "记", 1, "2025-09-15")
	if err := db.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO ledger_entry (biz_date, year, month, account_id, debit, credit,
				voucher_id, line_no, summary, reverted, created_at)
			VALUES ('2025-09-15', 2025, 9, ?, 100000, 0, ?, 1, 'x', 0, 't')`, accID, v)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	h, err := db.CheckPeriodHealth(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	if h.CanClose() {
		t.Error("试算不平衡时必须阻断结账")
	}
	var found bool
	for _, it := range h.Items {
		if it.Key == "trial_balance" && it.Level == HealthError {
			found = true
		}
	}
	if !found {
		t.Error("应报出试算不平衡")
	}
}

// ---------------------------------------------------------------------------
// 辅助：直接构造凭证（绕过过账校验，用于制造异常场景）
// ---------------------------------------------------------------------------

type entry struct {
	code, summary string
	debit, credit money.Money
	dept          *int64
	contact       *int64
}

func ptrI64(v int64) *int64 { return &v }

func postSimple(t *testing.T, db *DB, f *bookFixture, date string, es ...entry) {
	t.Helper()
	ctx := context.Background()
	v, err := voucher.New(voucher.WordJi, calendar.MustParse(date), "李会计")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		en := ledger.Entry{
			AccountCode: e.code, Summary: e.summary,
			Debit: e.debit, Credit: e.credit,
		}
		if e.dept != nil {
			en.Aux.DeptID = e.dept
		}
		if e.contact != nil {
			en.Aux.ContactID = e.contact
		}
		if err := v.AddEntry(en); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Vouchers().Post(ctx, PostInput{
		Voucher: v, Accounts: f.AccIDs, PostingBy: "王主管", At: time.Now()}); err != nil {
		t.Fatalf("过账失败: %v", err)
	}
}

func newDraftVoucher(t *testing.T, db *DB, date string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := db.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.Exec(ctx, `
			INSERT INTO voucher (year, month, word, seq, no, biz_date, status, source,
				created_by, created_at, updated_at)
			VALUES (2025, 9, '记', 0, '', ?, 'draft', 'manual', '李会计', 't', 't')`, date)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertRawVoucher(t *testing.T, db *DB, year, month int, word string, seq int, date string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := db.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.Exec(ctx, `
			INSERT INTO voucher (year, month, word, seq, no, biz_date, status, source,
				created_by, posted_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?, 'posted', 'manual', 'x', 'y', 't', 't')`,
			year, month, word, seq, "记-2025-09-0001", date)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// ---------------------------------------------------------------------------
// 结账 + 体检联动
// ---------------------------------------------------------------------------

func TestClosePeriodAfterHealthCheck(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	h, err := db.CheckPeriodHealth(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	if !h.CanClose() {
		t.Fatalf("体检应通过：%v", h.Errors())
	}

	// 结账需要按顺序，先把 1..9 月结掉
	for m := 1; m <= 9; m++ {
		if err := db.WithTx(ctx, func(tx *Tx) error {
			return db.Periods().SetStatus(ctx, tx, period.NewKey(2025, m),
				period.StatusClosed, "王主管", ptrTime(time.Now()))
		}); err != nil {
			t.Fatalf("结账 %d 月失败: %v", m, err)
		}
	}

	// 重新加载期间表，9 月应为 closed
	cal, err := db.Periods().Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := cal.Get(2025, 9)
	if !p.IsClosed() {
		t.Error("9 月应为已结账")
	}
	// 已结账期间不允许再记账
	if _, err := cal.CheckPostable(calendar.MustParse("2025-09-15")); err == nil {
		t.Error("已结账期间不应允许过账")
	}
}

// 结账后资产负债表应仍然平衡
func TestBalanceSheetStillBalancesAfterClose(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	asOf := calendar.MustParse("2025-09-30")
	before, _, _, err := db.Reports().BuildBalanceSheet(ctx, asOf)
	if err != nil {
		t.Fatal(err)
	}
	for m := 1; m <= 9; m++ {
		if err := db.WithTx(ctx, func(tx *Tx) error {
			return db.Periods().SetStatus(ctx, tx, period.NewKey(2025, m),
				period.StatusClosed, "王主管", ptrTime(time.Now()))
		}); err != nil {
			t.Fatal(err)
		}
	}
	after, _, issues, err := db.Reports().BuildBalanceSheet(ctx, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("结账后勾稽关系应仍成立: %v", issues)
	}
	l30b, _ := before.Line(30)
	l30a, _ := after.Line(30)
	if l30b.Value != l30a.Value {
		t.Errorf("结账本身不应改变资产总计：%s → %s", l30b.Value, l30a.Value)
	}
}
