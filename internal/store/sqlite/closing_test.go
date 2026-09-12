package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/closing"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
)

// mustDate 解析日期，失败即终止测试。
func mustDate(s string) calendar.Date {
	d, err := calendar.Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

// addLine 往凭证里加一条分录。
func addLine(t *testing.T, v *voucher.Voucher, code, summary string,
	debit, credit money.Money) {
	t.Helper()
	if err := v.AddEntry(ledger.Entry{
		AccountCode: code, Summary: summary, Debit: debit, Credit: credit,
	}); err != nil {
		t.Fatalf("加分录 %s 失败: %v", code, err)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// newTestDBThrough 建一个期间生成到指定月份的账套。
func newTestDBThrough(t *testing.T, throughMonth int) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if _, err := db.CreateBook(ctx, CreateBookInput{
		CompanyName: "结账测试账套",
		TaxType:     TaxTypeGeneral,
		StartYear:   2025, StartMonth: 1,
		ThroughYear: 2025,
		CurrentYear: 2025, CurrentMonth: throughMonth,
	}); err != nil {
		t.Fatalf("建账失败: %v", err)
	}
	return db
}

func closeP(t *testing.T, db *DB, y, m int, by string) *CloseResult {
	t.Helper()
	res, err := db.Closing().ClosePeriod(context.Background(), CloseInput{
		Period: period.NewKey(y, m), PostingBy: by, At: time.Now(),
	})
	if err != nil {
		t.Fatalf("结账 %d-%02d 失败: %v", y, m, err)
	}
	return res
}

func periodStatus(t *testing.T, db *DB, y, m int) string {
	t.Helper()
	var st string
	err := db.SQL().QueryRow(
		`SELECT status FROM period WHERE year = ? AND month = ?`, y, m).Scan(&st)
	if err != nil {
		t.Fatalf("查询期间状态失败: %v", err)
	}
	return st
}

// rawBalance 返回某科目前缀截至某日的原始净余额（借−贷）。
func rawBalance(t *testing.T, db *DB, code, asOf string) money.Money {
	t.Helper()
	var net int64
	err := db.SQL().QueryRow(`
		SELECT COALESCE(SUM(le.debit), 0) - COALESCE(SUM(le.credit), 0)
		  FROM ledger_entry le JOIN account a ON a.id = le.account_id
		 WHERE a.code LIKE ? AND le.biz_date <= ?`, code+"%", asOf).Scan(&net)
	if err != nil {
		t.Fatalf("查询余额失败: %v", err)
	}
	return money.Money(net)
}

// ---------------------------------------------------------------------------
// 顺序约束
// ---------------------------------------------------------------------------

// ★ 不能跳过前期直接结账 —— 否则前期凭证会在结转之后才录进来，
// 而结转凭证已经把它算漏了，账面上永远差这一块。
func TestClosePeriodRequiresSequential(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	_, err := db.Closing().ClosePeriod(ctx, CloseInput{
		Period: period.NewKey(2025, 9), PostingBy: "王主管", At: time.Now()})
	if !errors.Is(err, ErrPriorOpen) {
		t.Fatalf("跳过前期结账应报 ErrPriorOpen，实际 %v", err)
	}
	if got := periodStatus(t, db, 2025, 9); got != "open" {
		t.Errorf("失败的结账不应改动期间状态，实际 %s", got)
	}
}

func TestClosePeriodRejectsUnknownPeriod(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	_, err := db.Closing().ClosePeriod(ctx, CloseInput{
		Period: period.NewKey(2030, 5), PostingBy: "王主管"})
	if !errors.Is(err, period.ErrPeriodNotFound) {
		t.Fatalf("不存在的期间应报 ErrPeriodNotFound，实际 %v", err)
	}
}

func TestClosePeriodRequiresPostingBy(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	_, err := db.Closing().ClosePeriod(ctx, CloseInput{Period: period.NewKey(2025, 1)})
	if !errors.Is(err, voucher.ErrMissingMaker) {
		t.Fatalf("缺少结账人应报 ErrMissingMaker，实际 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 结账主流程
// ---------------------------------------------------------------------------

func TestClosePeriodTransfersPnL(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	f := seedBook(t, db)
	_ = f

	// 1—8 月无业务，先按顺序结掉
	for m := 1; m <= 8; m++ {
		res := closeP(t, db, 2025, m, "王主管")
		if res.Planned() {
			t.Errorf("%d 月无损益，不应生成结转凭证（得到 %s）", m, res.VoucherNo)
		}
		if res.Plan.Closing != nil {
			t.Errorf("%d 月不应有结转结果", m)
		}
		if got := periodStatus(t, db, 2025, m); got != "closed" {
			t.Errorf("%d 月状态 = %s，期望 closed", m, got)
		}
	}

	// 9 月：收入 80,000，费用 15,000，利润 65,000
	res := closeP(t, db, 2025, 9, "王主管")
	if !res.Planned() {
		t.Fatal("9 月应生成结转凭证")
	}
	if res.Plan.Closing == nil {
		t.Fatal("9 月应有结转结果")
	}
	p := res.Plan.Closing
	if p.TotalIncome != money100(80000) {
		t.Errorf("收入合计 = %s，期望 80000.00", p.TotalIncome)
	}
	if p.TotalExpense != money100(15000) {
		t.Errorf("费用合计 = %s，期望 15000.00", p.TotalExpense)
	}
	if p.Profit != money100(65000) {
		t.Errorf("利润 = %s，期望 65000.00", p.Profit)
	}

	// 结转凭证：字为「转」、来源为 closing、借贷平衡
	vs, err := db.Closing().Journal(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("结转凭证数 = %d，期望 1", len(vs))
	}
	cv := vs[0]
	if cv.Word != voucher.WordZhuan {
		t.Errorf("凭证字 = %s，期望 转", cv.Word)
	}
	if cv.Source != voucher.SourceClosing {
		t.Errorf("来源 = %s，期望 closing", cv.Source)
	}
	if cv.Status != voucher.StatusPosted {
		t.Errorf("状态 = %s，期望 posted", cv.Status)
	}
	if !cv.IsBalanced() {
		t.Errorf("结转凭证借贷不平：借 %s 贷 %s", cv.TotalDebit(), cv.TotalCredit())
	}
	if cv.TotalDebit() != money100(80000) {
		t.Errorf("结转凭证金额 = %s，期望 80000.00", cv.TotalDebit())
	}
	// 记账日期应为期间最后一天
	if got := cv.BizDate.String(); got != "2025-09-30" {
		t.Errorf("记账日期 = %s，期望 2025-09-30", got)
	}

	// ★ 结转后损益类科目余额必须归零
	for _, code := range []string{"5001", "560206", "560210"} {
		if got := rawBalance(t, db, code, "2025-09-30"); !got.IsZero() {
			t.Errorf("结转后 %s 余额 = %s，期望 0", code, got)
		}
	}
	// 本年利润 贷方 65,000（raw = −65,000）
	if got := rawBalance(t, db, "3103", "2025-09-30"); got != money100(-65000) {
		t.Errorf("本年利润余额 = %s，期望 -65000.00", got)
	}
	if got := periodStatus(t, db, 2025, 9); got != "closed" {
		t.Errorf("9 月状态 = %s，期望 closed", got)
	}
}

// ★ 已结账期间不能再过账
func TestClosedPeriodRejectsPosting(t *testing.T) {
	ctx := context.Background()
	db := newTestDBThrough(t, 2)
	closeP(t, db, 2025, 1, "王主管")

	accIDs, err := db.Accounts().IDsByCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v, err := voucher.New(voucher.WordJi, mustDate("2025-01-20"), "李会计")
	if err != nil {
		t.Fatal(err)
	}
	addLine(t, v, "1002", "测试", money100(100), 0)
	addLine(t, v, "3001", "测试", 0, money100(100))

	_, err = db.Vouchers().Post(ctx, PostInput{
		Voucher: v, Accounts: accIDs, PostingBy: "王主管", At: time.Now()})
	if err == nil {
		t.Fatal("已结账期间不应允许过账")
	}
	if !errors.Is(err, period.ErrNotOpen) {
		t.Fatalf("期望 ErrNotOpen，实际 %v", err)
	}
}

// 重复结账必须被拒绝
func TestClosePeriodTwice(t *testing.T) {
	ctx := context.Background()
	db := newTestDBThrough(t, 1)
	closeP(t, db, 2025, 1, "王主管")

	_, err := db.Closing().ClosePeriod(ctx, CloseInput{
		Period: period.NewKey(2025, 1), PostingBy: "王主管"})
	if !errors.Is(err, ErrAlreadyClosed) {
		t.Fatalf("重复结账应报 ErrAlreadyClosed，实际 %v", err)
	}
}

// ★ 结账要先把本期的草稿**过账**，再算结转计划。
//
// 顺序不能颠倒：结转计划是按总账算出来的。草稿还没过账时总账里
// 根本没有本期这笔费用，算出来的损益必然少一块 —— 而且
// 「没数据的正确结果」看起来跟「真的没有费用」一模一样，没人看得出来。
func TestClosePeriodPostsDraftsBeforePlanning(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db) // 里面已经有 9 月的一批已过账凭证
	for m := 1; m <= 8; m++ {
		closeP(t, db, 2025, m, "王主管")
	}

	// 一张**草稿**：9 月又付了 2,000 房租（只在草稿里，总账里还没有）
	deptAdmin := int64(1)
	dv, err := voucher.New(voucher.WordJi, mustDate("2025-09-28"), "李会计")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []ledger.Entry{
		{AccountCode: "560210", Summary: "补记 9 月房租",
			Debit: money100(2000), Aux: ledger.Aux{DeptID: &deptAdmin}},
		{AccountCode: "1002", Summary: "补记 9 月房租", Credit: money100(2000)},
	} {
		if err := dv.AddEntry(e); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := db.Vouchers().SaveDraft(ctx, DraftInput{Voucher: dv, CreatedBy: "李会计"})
	if err != nil {
		t.Fatalf("存草稿失败: %v", err)
	}

	// 结账前：体检报告里说清楚「本期草稿会在结账时过账」
	h, err := db.CheckPeriodHealth(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range h.Items {
		if it.Key == "draft_vouchers" {
			found = true
			if it.Count != 1 {
				t.Errorf("草稿数 = %d，期望 1", it.Count)
			}
			if it.Level != HealthOK {
				t.Errorf("草稿是正常状态，不该报警：%s", it.Level)
			}
		}
	}
	if !found {
		t.Error("体检报告应当报出本期草稿张数")
	}
	if !h.CanClose() {
		t.Error("草稿不该阻断结账 —— 结账本来就会把它们过账")
	}

	res, err := db.Closing().ClosePeriod(ctx, CloseInput{
		Period: period.NewKey(2025, 9), PostingBy: "王主管", At: time.Now()})
	if err != nil {
		t.Fatalf("结账失败: %v", err)
	}

	// 1. 草稿被过账了：有了凭证号、状态是 posted
	posted, err := db.Vouchers().Get(ctx, saved.VoucherID)
	if err != nil {
		t.Fatal(err)
	}
	if posted.Status != voucher.StatusPosted {
		t.Errorf("★ 结账应当把本期草稿过账，实际状态 %s", posted.Status)
	}
	if posted.No == "" {
		t.Error("★ 过账后应当分配到凭证号")
	}
	if res.PostedDrafts == nil || res.PostedDrafts.Posted != 1 {
		t.Errorf("结账结果应报出过账张数 1，实际 %+v", res.PostedDrafts)
	}

	// 2. 结转计划里**包含**这笔草稿的费用 —— 证明先过账、后算计划
	if res.Plan == nil || res.Plan.Closing == nil {
		t.Fatal("结账计划为空")
	}
	// seedBook 的 9 月费用：办公用品 3,000 + 房租 12,000 + 这张草稿 2,000
	if got := res.Plan.Closing.TotalExpense; got != money100(17000) {
		t.Errorf("★ 结转计划的费用 = %s，期望 17,000.00。\n"+
			"    少的那部分就是草稿里的 2,000 —— 说明结转计划是在草稿过账**之前**算的。",
			got)
	}

	// 3. 总账里真的有这笔
	var n int
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ledger_entry WHERE voucher_id = ?`, saved.VoucherID).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("总账里应有 2 条该凭证的分录，实际 %d", n)
	}
}

// ★ 一张过不了账的草稿必须**指名道姓**地中止结账。
//
// 一次结账卡住却不说卡在哪张凭证，用户只能一张张试；
// 而凭证动辄几十张，试到天亮也找不到。
func TestClosePeriodAbortsAndNamesBadDraft(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)
	for m := 1; m <= 8; m++ {
		closeP(t, db, 2025, m, "王主管")
	}

	// 手工插一张**空**草稿（没有任何分录）—— 它一定过不了账
	newDraftVoucher(t, db, "2025-09-30")

	_, err := db.Closing().ClosePeriod(ctx, CloseInput{
		Period: period.NewKey(2025, 9), PostingBy: "王主管", At: time.Now()})
	if err == nil {
		t.Fatal("有草稿过不了账时，结账必须中止")
	}
	msg := err.Error()
	if !strings.Contains(msg, "草稿 #") && !strings.Contains(msg, "第 1 张") {
		t.Errorf("要指出是哪一张凭证：%v", err)
	}
	if !strings.Contains(msg, "回滚") {
		t.Errorf("要说清楚整批都没过：%v", err)
	}

	// 期间必须还开着 —— 中止就是中止，不能留下半截状态
	if st := periodStatus(t, db, 2025, 9); st != string(period.StatusOpen) {
		t.Errorf("结账中止后期间应仍为 open，实际 %s", st)
	}
}

// 体检必须拦下真正的错误：制造一笔不平衡的总账
func TestClosePeriodBlockedByUnbalancedLedger(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)
	for m := 1; m <= 8; m++ {
		closeP(t, db, 2025, m, "王主管")
	}
	// 给某条借方总账分录凭空加 1 元，制造借贷不平
	if err := db.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE ledger_entry SET debit = debit + 100
			 WHERE id = (SELECT MIN(id) FROM ledger_entry WHERE debit > 0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	_, err := db.Closing().ClosePeriod(ctx, CloseInput{
		Period: period.NewKey(2025, 9), PostingBy: "王主管"})
	if !errors.Is(err, ErrHealthFailed) {
		t.Fatalf("试算不平衡应阻断结账，实际 %v", err)
	}
	if got := periodStatus(t, db, 2025, 9); got != "open" {
		t.Errorf("阻断后 9 月状态 = %s，期望 open", got)
	}
	// 阻断时不能留下任何结转凭证
	vs, err := db.Closing().Journal(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Errorf("被阻断的结账不应留下凭证，实际 %d 张", len(vs))
	}
}

// ---------------------------------------------------------------------------
// 反结账
// ---------------------------------------------------------------------------

func TestReopenPeriodReversesClosing(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)
	for m := 1; m <= 9; m++ {
		closeP(t, db, 2025, m, "王主管")
	}

	res, err := db.Closing().ReopenPeriod(ctx, ReopenInput{
		Period: period.NewKey(2025, 9), PostingBy: "王主管", At: time.Now()})
	if err != nil {
		t.Fatalf("反结账失败: %v", err)
	}
	if len(res.Reversed) != 1 {
		t.Fatalf("应冲销 1 张结转凭证，实际 %v", res.Reversed)
	}

	// 期间已放开
	if got := periodStatus(t, db, 2025, 9); got != "open" {
		t.Errorf("反结账后状态 = %s，期望 open", got)
	}
	// ★ 损益类科目余额必须恢复 —— 冲销是红字凭证而不是删凭证
	if got := rawBalance(t, db, "5001", "2025-09-30"); got != money100(-80000) {
		t.Errorf("反结账后主营业务收入余额 = %s，期望 -80000.00", got)
	}
	if got := rawBalance(t, db, "560206", "2025-09-30"); got != money100(3000) {
		t.Errorf("反结账后办公费余额 = %s，期望 3000.00", got)
	}
	if got := rawBalance(t, db, "3103", "2025-09-30"); !got.IsZero() {
		t.Errorf("反结账后本年利润余额 = %s，期望 0", got)
	}

	// 冲销凭证真实存在且已入账
	rev, err := db.Vouchers().Get(ctx, res.VoucherIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if rev.ReversesID == nil {
		t.Error("冲销凭证应记录被冲销凭证的 id")
	}
	if !rev.IsBalanced() {
		t.Error("冲销凭证借贷不平")
	}
	// 原结转凭证标记为已作废（审计标记）
	js, err := db.Closing().Journal(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	if len(js) != 0 {
		t.Errorf("已冲销的结转凭证不应再出现在结转台账里，实际 %d 张", len(js))
	}

	// 反结账后可以重新结账，且会生成新的结转凭证
	res2 := closeP(t, db, 2025, 9, "王主管")
	if !res2.Planned() {
		t.Fatal("重新结账应生成新的结转凭证")
	}
	if res2.VoucherNo == res.Reversed[0] {
		t.Error("重新结账不应复用已冲销的凭证号")
	}
	if got := rawBalance(t, db, "5001", "2025-09-30"); !got.IsZero() {
		t.Errorf("重新结账后主营业务收入余额 = %s，期望 0", got)
	}
	if got := rawBalance(t, db, "3103", "2025-09-30"); got != money100(-65000) {
		t.Errorf("重新结账后本年利润 = %s，期望 -65000.00", got)
	}
}

func TestReopenRequiresClosedPeriod(t *testing.T) {
	ctx := context.Background()
	db := newTestDBThrough(t, 3)

	_, err := db.Closing().ReopenPeriod(ctx, ReopenInput{
		Period: period.NewKey(2025, 3), PostingBy: "王主管"})
	if !errors.Is(err, ErrNotClosed) {
		t.Fatalf("未结账期间反结账应报 ErrNotClosed，实际 %v", err)
	}
}

// ★ 后面还关着的时候不能放开前面 —— 否则后续期间的结转基础就失效了
func TestReopenRequiresLatestPeriod(t *testing.T) {
	ctx := context.Background()
	db := newTestDBThrough(t, 3)
	for m := 1; m <= 3; m++ {
		closeP(t, db, 2025, m, "王主管")
	}

	_, err := db.Closing().ReopenPeriod(ctx, ReopenInput{
		Period: period.NewKey(2025, 2), PostingBy: "王主管"})
	if !errors.Is(err, ErrLaterClosed) {
		t.Fatalf("应报 ErrLaterClosed，实际 %v", err)
	}
	if got := periodStatus(t, db, 2025, 2); got != "closed" {
		t.Errorf("失败的反结账不应改动状态，实际 %s", got)
	}
	// 从最后往前逐期反结账则允许
	if _, err := db.Closing().ReopenPeriod(ctx, ReopenInput{
		Period: period.NewKey(2025, 3), PostingBy: "王主管"}); err != nil {
		t.Fatalf("反结账 3 月失败: %v", err)
	}
	if _, err := db.Closing().ReopenPeriod(ctx, ReopenInput{
		Period: period.NewKey(2025, 2), PostingBy: "王主管"}); err != nil {
		t.Fatalf("反结账 2 月失败: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 年末结转
// ---------------------------------------------------------------------------

// 12 月结账要把「本年利润」转入「利润分配—未分配利润」
func TestYearEndClosingTransfersProfit(t *testing.T) {
	ctx := context.Background()
	db := newTestDBThrough(t, 12)
	accIDs, err := db.Accounts().IDsByCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cust := mustContacts(t, db, "customer", "客户甲")
	deptAdmin := int64(1) // 管理部门

	// 12 月发生：收入 100,000（含税 113,000），费用 20,000
	postV := func(lines ...entry) {
		t.Helper()
		v, err := voucher.New(voucher.WordJi, mustDate("2025-12-20"), "李会计")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range lines {
			en := ledger.Entry{
				AccountCode: e.code, Summary: e.summary,
				Debit: e.debit, Credit: e.credit,
			}
			en.Aux.ContactID = e.contact
			en.Aux.DeptID = e.dept
			if err := v.AddEntry(en); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Vouchers().Post(ctx, PostInput{
			Voucher: v, Accounts: accIDs, PostingBy: "王主管", At: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	postV(
		entry{code: "1122", summary: "销售", debit: money100(113000), contact: &cust},
		entry{code: "5001", summary: "销售", credit: money100(100000)},
		entry{code: "22210102", summary: "销项税", credit: money100(13000)},
	)
	postV(
		entry{code: "560210", summary: "房租", debit: money100(20000), dept: &deptAdmin},
		entry{code: "1002", summary: "房租", credit: money100(20000)},
	)

	for m := 1; m <= 11; m++ {
		closeP(t, db, 2025, m, "王主管")
	}
	res := closeP(t, db, 2025, 12, "王主管")
	if res.Plan == nil || !res.Plan.YearEnd {
		t.Fatal("12 月应被识别为年度末期间")
	}

	// 本年利润在 12 月结转后应立即转入未分配利润，余额归零
	if got := rawBalance(t, db, "3103", "2025-12-31"); !got.IsZero() {
		t.Errorf("年末结转后本年利润 = %s，期望 0", got)
	}
	// 未分配利润贷方 80,000 → raw = −80,000
	if got := rawBalance(t, db, "310401", "2025-12-31"); got != money100(-80000) {
		t.Errorf("未分配利润 = %s，期望 -80000.00", got)
	}
	if got := rawBalance(t, db, "5001", "2025-12-31"); !got.IsZero() {
		t.Errorf("年末结转后主营业务收入 = %s，期望 0", got)
	}
	// 年末结转凭证应是平衡的
	vs, err := db.Closing().Journal(ctx, period.NewKey(2025, 12))
	if err != nil {
		t.Fatal(err)
	}
	var debit money.Money
	for _, v := range vs {
		if !v.IsBalanced() {
			t.Errorf("期末凭证 %s 借贷不平", v.No)
		}
		debit = debit.Add(v.TotalDebit())
	}
	if debit.IsZero() {
		t.Error("12 月应有结转凭证")
	}
}

// 非年末期间不应触碰本年利润与未分配利润
func TestNonYearEndDoesNotTouchRetainedEarnings(t *testing.T) {
	db := newTestDB(t)
	seedBook(t, db)
	for m := 1; m <= 9; m++ {
		closeP(t, db, 2025, m, "王主管")
	}
	if got := rawBalance(t, db, "310401", "2025-09-30"); !got.IsZero() {
		t.Errorf("非年末不应结转未分配利润，实际 %s", got)
	}
	if got := rawBalance(t, db, "3103", "2025-09-30"); got != money100(-65000) {
		t.Errorf("本年利润 = %s，期望 -65000.00", got)
	}
}

// ---------------------------------------------------------------------------
// 结账预览
// ---------------------------------------------------------------------------

// Plan 必须只读 —— 预览一次不能改变任何数据
func TestPlanIsReadOnly(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	plan, err := db.Closing().Plan(ctx, period.NewKey(2025, 9), closing.Accounts{})
	if err != nil {
		t.Fatalf("生成结账计划失败: %v", err)
	}
	if plan.Closing == nil {
		t.Fatal("9 月应有结转结果")
	}
	if plan.Closing.Profit != money100(65000) {
		t.Errorf("预览利润 = %s，期望 65000.00", plan.Closing.Profit)
	}

	// 预览之后：没有凭证、期间仍开放、损益未结转
	vs, err := db.Closing().Journal(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Errorf("预览不应生成凭证，实际 %d 张", len(vs))
	}
	if got := periodStatus(t, db, 2025, 9); got != "open" {
		t.Errorf("预览不应改动期间状态，实际 %s", got)
	}
	if got := rawBalance(t, db, "5001", "2025-09-30"); got != money100(-80000) {
		t.Errorf("预览不应结转损益，主营业务收入 = %s", got)
	}
	// 计划本身必须平衡
	if err := plan.Validate(); err != nil {
		t.Errorf("结账计划借贷不平: %v", err)
	}
}

// 结转金额与科目方向：收入类余额在借方、费用类在贷方
func TestClosingEntryDirections(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	plan, err := db.Closing().Plan(ctx, period.NewKey(2025, 9), closing.Accounts{})
	if err != nil {
		t.Fatal(err)
	}
	byCode := map[string]closing.Entry{}
	for _, e := range plan.ClosingEntries {
		byCode[e.AccountCode] = e
	}
	if e := byCode["5001"]; e.Debit != money100(80000) || !e.Credit.IsZero() {
		t.Errorf("主营业务收入应借记 80,000，实际 %+v", e)
	}
	if e := byCode["560206"]; e.Credit != money100(3000) || !e.Debit.IsZero() {
		t.Errorf("办公费应贷记 3,000，实际 %+v", e)
	}
	if e := byCode["560210"]; e.Credit != money100(12000) || !e.Debit.IsZero() {
		t.Errorf("租赁费应贷记 12,000，实际 %+v", e)
	}
	// 本年利润只出现一次，金额为净额
	if e := byCode["3103"]; e.Credit != money100(65000) {
		t.Errorf("本年利润应贷记净额 65,000，实际 %+v", e)
	}
	if len(plan.ClosingEntries) != 4 {
		t.Errorf("分录数 = %d，期望 4", len(plan.ClosingEntries))
	}
}

// 只统计叶子科目：汇总科目一并结转会重复计算
func TestPnLBalancesUseLeafOnly(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	plan, err := db.Closing().Plan(ctx, period.NewKey(2025, 9), closing.Accounts{})
	if err != nil {
		t.Fatal(err)
	}
	grp := map[string]bool{}
	for _, e := range plan.ClosingEntries {
		grp[e.AccountCode] = true
	}
	// 5602 是汇总科目，不应单独出现在结转分录里
	if grp["5602"] {
		t.Error("汇总科目 5602 不应单独结转")
	}
	if grp["560206"] != true {
		t.Error("明细科目 560206 应出现在结转分录里")
	}
}

// 亏损期间的结转方向必须反转
func TestClosingLossDirection(t *testing.T) {
	ctx := context.Background()
	db := newTestDBThrough(t, 3)
	accIDs, err := db.Accounts().IDsByCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deptAdmin := int64(1)
	v, err := voucher.New(voucher.WordJi, mustDate("2025-03-15"), "李会计")
	if err != nil {
		t.Fatal(err)
	}
	if err := v.AddEntry(ledger.Entry{
		AccountCode: "560210", Summary: "房租",
		Debit: money100(30000), Aux: ledger.Aux{DeptID: &deptAdmin},
	}); err != nil {
		t.Fatal(err)
	}
	addLine(t, v, "1002", "房租", 0, money100(30000))
	if _, err := db.Vouchers().Post(ctx, PostInput{
		Voucher: v, Accounts: accIDs, PostingBy: "王主管", At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	closeP(t, db, 2025, 1, "王主管")
	closeP(t, db, 2025, 2, "王主管")
	res := closeP(t, db, 2025, 3, "王主管")

	if res.Plan.Closing.Profit != money100(-30000) {
		t.Errorf("亏损 = %s，期望 -30000.00", res.Plan.Closing.Profit)
	}
	// 亏损时本年利润在借方
	vs, err := db.Closing().Journal(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range vs[0].Entries {
		if e.AccountCode == "3103" {
			found = true
			if e.Debit != money100(30000) {
				t.Errorf("亏损时本年利润应借记 30,000，实际借 %s 贷 %s", e.Debit, e.Credit)
			}
		}
	}
	if !found {
		t.Error("结转凭证应包含本年利润分录")
	}
	if !vs[0].IsBalanced() {
		t.Errorf("亏损结转凭证借贷不平：借 %s 贷 %s", vs[0].TotalDebit(), vs[0].TotalCredit())
	}
	// 本年利润借方 30,000 → raw = +30,000
	if got := rawBalance(t, db, "3103", "2025-03-31"); got != money100(30000) {
		t.Errorf("本年利润 = %s，期望 30000.00", got)
	}
}

// 空期间结账不生成凭证，但期间照样关闭
func TestCloseEmptyPeriod(t *testing.T) {
	ctx := context.Background()
	db := newTestDBThrough(t, 2)

	res := closeP(t, db, 2025, 1, "王主管")
	if res.Planned() {
		t.Error("空期间不应生成结转凭证")
	}
	if res.Plan == nil {
		t.Fatal("空期间也应有计划（用于展示步骤）")
	}
	if len(res.Plan.Steps) == 0 {
		t.Error("计划应包含步骤说明")
	}
	if got := periodStatus(t, db, 2025, 1); got != "closed" {
		t.Errorf("空期间也应关闭，实际 %s", got)
	}
	// 结转台账为空
	vs, err := db.Closing().Journal(ctx, period.NewKey(2025, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Errorf("台账应为空，实际 %d 张", len(vs))
	}
}

// 结账后科目余额表仍应满足资产 = 负债 + 所有者权益
func TestBalanceSheetBalancesAfterClosing(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)
	for m := 1; m <= 9; m++ {
		closeP(t, db, 2025, m, "王主管")
	}

	asOf := mustDate("2025-09-30")
	_, _, issues, err := db.Reports().BuildBalanceSheet(ctx, asOf)
	if err != nil {
		t.Fatal(err)
	}
	for _, is := range issues {
		if is.Fatal {
			t.Errorf("结账后资产负债表勾稽不成立: %v", is)
		}
	}
	// 权益里的「本年利润」应体现已结转的利润
	if got := rawBalance(t, db, "3103", "2025-09-30"); got != money100(-65000) {
		t.Errorf("本年利润 = %s，期望 -65000.00", got)
	}
}

// postDrafts 过账某期间的全部草稿 —— 测试里等价于「结账的第一步」。
//
// ★ 本工程只在账期结算时过账，所以任何「模块生成凭证 → 读总账」
// 的测试都要显式走这一步。少了它，测试读到的是**空总账**：
// 断言会以「余额 = 0」的形式失败，而那正是「凭证还没过账」的样子。
func postDrafts(t *testing.T, db *DB, y, m int) *PostPeriodDraftsResult {
	t.Helper()
	res, err := db.Vouchers().PostPeriodDrafts(context.Background(),
		period.NewKey(y, m), "王主管", time.Now())
	if err != nil {
		t.Fatalf("过账 %d-%02d 的草稿失败: %v", y, m, err)
	}
	return res
}
