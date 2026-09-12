package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/summary"
	"miniaccount/internal/domain/voucher"
)

// sd 是 store 测试里的日期简写。
func sd(s string) calendar.Date { return calendar.MustParse(s) }

// saveDraft 存一张草稿凭证（草稿不占号、不进总账）。
//
// 借 1002 / 贷 224101：两边的辅助核算要求都最少
// （3001 实收资本要求「股东」，草稿也得填，用它反而多一层干扰）。
func saveDraft(t *testing.T, db *DB, date, remark string, amount money.Money) int64 {
	t.Helper()
	ctx := context.Background()
	// mustContacts 是幂等的：账套里已有「张三」就返回它，不重复建。
	shareholder := mustContacts(t, db, "shareholder", "张三")

	v, err := voucher.New(voucher.WordJi, calendar.MustParse(date), "李会计")
	if err != nil {
		t.Fatalf("建凭证失败: %v", err)
	}
	v.Remark = remark
	if err := v.AddEntry(ledger.Entry{
		AccountCode: "1002", Summary: remark, Debit: amount}); err != nil {
		t.Fatal(err)
	}
	if err := v.AddEntry(ledger.Entry{
		AccountCode: "224101", Summary: remark, Credit: amount,
		Aux: ledger.Aux{ContactID: &shareholder}}); err != nil {
		t.Fatal(err)
	}
	saved, err := db.Vouchers().SaveDraft(ctx, DraftInput{Voucher: v, CreatedBy: "李会计"})
	if err != nil {
		t.Fatalf("存草稿失败: %v", err)
	}
	return saved.VoucherID
}

// ★ 凭证汇总表必须与总账一字不差。
//
// 这是这张表存在的意义：会计拿它去核对总账。三个角度（凭证字、
// 日期、科目）的合计与总账的分录之和必须完全相等，
// 否则这张表就没有资格被拿去对账。
func TestSummaryMatchesLedger(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db) // 5 张凭证，覆盖资产/负债/权益/收入/费用

	rep, err := db.Summary().BuildSummary(ctx, sd("2025-09-01"), sd("2025-09-30"))
	if err != nil {
		t.Fatalf("生成凭证汇总表失败: %v", err)
	}
	if errs := rep.Check(); len(errs) != 0 {
		t.Fatalf("不变式应成立: %v", errs)
	}

	// 总账口径：直接查 ledger_entry
	var (
		wantDebit, wantCredit int64
		wantCount             int
	)
	if err := db.sql.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(debit),0), COALESCE(SUM(credit),0),
		       COUNT(DISTINCT voucher_id)
		  FROM ledger_entry WHERE biz_date >= '2025-09-01' AND biz_date <= '2025-09-30'`).
		Scan(&wantDebit, &wantCredit, &wantCount); err != nil {
		t.Fatal(err)
	}
	if rep.VoucherCount != wantCount {
		t.Errorf("凭证张数 = %d，总账 = %d", rep.VoucherCount, wantCount)
	}
	if int64(rep.DebitTotal) != wantDebit {
		t.Errorf("借方合计 = %s，总账 = %d 分", rep.DebitTotal, wantDebit)
	}
	if int64(rep.CreditTotal) != wantCredit {
		t.Errorf("贷方合计 = %s，总账 = %d 分", rep.CreditTotal, wantCredit)
	}
	if !rep.Balanced() {
		t.Error("复式记账下借贷必然相等")
	}

	// 与科目余额表交叉验证：两者都来自总账，本期发生额必须一致
	tb, err := db.Reports().TrialBalanceReport(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	_, _, pd, pc, _, _ := tb.Totals()
	if pd != rep.DebitTotal || pc != rep.CreditTotal {
		t.Errorf("凭证汇总表 借%s/贷%s ≠ 科目余额表 借%s/贷%s",
			rep.DebitTotal, rep.CreditTotal, pd, pc)
	}

	// 逐科目比对：科目汇总表的每一行都要与科目余额表对上
	tbByCode := map[string][2]int64{}
	for _, row := range tb.Rows {
		if row.IsLeaf {
			tbByCode[row.AccountCode] = [2]int64{
				int64(row.PeriodDebit), int64(row.PeriodCredit)}
		}
	}
	for _, a := range rep.AccountRows {
		want, ok := tbByCode[a.AccountCode]
		if !ok {
			t.Errorf("科目 %s 不在科目余额表的明细科目里", a.AccountCode)
			continue
		}
		if int64(a.Debit) != want[0] || int64(a.Credit) != want[1] {
			t.Errorf("科目 %s：汇总表 借%d/贷%d，余额表 借%d/贷%d",
				a.AccountCode, a.Debit, a.Credit, want[0], want[1])
		}
	}
	// 反过来：余额表里有发生额的明细科目，汇总表也必须都有
	for code, v := range tbByCode {
		if v[0] == 0 && v[1] == 0 {
			continue
		}
		var found bool
		for _, a := range rep.AccountRows {
			if a.AccountCode == code {
				found = true
			}
		}
		if !found {
			t.Errorf("科目 %s 余额表有发生额，汇总表里却没有", code)
		}
	}
}

// 三个角度的合计必须完全相等。
func TestSummaryThreeGroupingsTie(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	rep, err := db.Summary().BuildSummary(ctx, sd("2025-09-01"), sd("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}

	var wd, dd, ad int64
	var wc, dc, ac int64
	var wCount, dCount int
	for _, w := range rep.WordRows {
		wd += int64(w.Debit)
		wc += int64(w.Credit)
		wCount += w.Count
	}
	for _, row := range rep.DayRows {
		dd += int64(row.Debit)
		dc += int64(row.Credit)
		dCount += row.Count
	}
	for _, a := range rep.AccountRows {
		ad += int64(a.Debit)
		ac += int64(a.Credit)
	}
	if wd != int64(rep.DebitTotal) || wc != int64(rep.CreditTotal) {
		t.Errorf("按凭证字 借%d/贷%d ≠ %s/%s", wd, wc, rep.DebitTotal, rep.CreditTotal)
	}
	if dd != int64(rep.DebitTotal) || dc != int64(rep.CreditTotal) {
		t.Errorf("按日期 借%d/贷%d ≠ %s/%s", dd, dc, rep.DebitTotal, rep.CreditTotal)
	}
	if ad != int64(rep.DebitTotal) || ac != int64(rep.CreditTotal) {
		t.Errorf("按科目 借%d/贷%d ≠ %s/%s", ad, ac, rep.DebitTotal, rep.CreditTotal)
	}
	if wCount != rep.VoucherCount || dCount != rep.VoucherCount {
		t.Errorf("张数：凭证字 %d、日期 %d、总数 %d",
			wCount, dCount, rep.VoucherCount)
	}
}

// ★ 红字冲销后：原凭证与红字凭证都计入，净额为零。
func TestSummaryIncludesBothSidesOfReversal(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	f := seedBook(t, db)
	_ = f

	before, err := db.Summary().BuildSummary(ctx, sd("2025-09-01"), sd("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}

	// 找一张已记账的凭证冲销掉
	vouchers, err := db.Vouchers().List(ctx, ListFilter{
		Year: 2025, Month: 9, Status: string(voucher.StatusPosted)})
	if err != nil {
		t.Fatal(err)
	}
	if len(vouchers) == 0 {
		t.Fatal("夹具里没有已记账的凭证")
	}
	if _, err := db.Vouchers().Reverse(ctx, ReverseInput{
		VoucherID: vouchers[0].ID, By: "王主管", At: time.Now(),
	}); err != nil {
		t.Fatalf("冲销失败: %v", err)
	}

	after, err := db.Summary().BuildSummary(ctx, sd("2025-09-01"), sd("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}

	// 多了一张红字凭证
	if after.VoucherCount != before.VoucherCount+1 {
		t.Errorf("凭证张数 = %d，期望 %d（多一张红字凭证）",
			after.VoucherCount, before.VoucherCount+1)
	}
	if after.ReversalCount != 1 {
		t.Errorf("红字凭证数 = %d，期望 1", after.ReversalCount)
	}
	if after.VoidedCount != 1 {
		t.Errorf("已冲销凭证数 = %d，期望 1", after.VoidedCount)
	}
	// ★ 被冲销的那张**仍要计入**，否则汇总表与总账对不上
	if after.VoidedCount > 0 {
		var noted bool
		for _, n := range after.Notes {
			if strings.Contains(n, "仍计入本表") {
				noted = true
			}
		}
		if !noted {
			t.Errorf("应说明被冲销的凭证仍计入，实际 %v", after.Notes)
		}
	}
	// 与总账仍然一致
	var wantDebit, wantCount int64
	if err := db.sql.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(debit),0), COUNT(DISTINCT voucher_id)
		  FROM ledger_entry WHERE biz_date BETWEEN '2025-09-01' AND '2025-09-30'`).
		Scan(&wantDebit, &wantCount); err != nil {
		t.Fatal(err)
	}
	if int64(after.DebitTotal) != wantDebit {
		t.Errorf("冲销后借方合计 = %s，总账 = %d 分", after.DebitTotal, wantDebit)
	}
	if int64(after.VoucherCount) != wantCount {
		t.Errorf("冲销后张数 = %d，总账 = %d", after.VoucherCount, wantCount)
	}
	if errs := after.Check(); len(errs) != 0 {
		t.Errorf("不变式应成立: %v", errs)
	}
}

// ★ 草稿不计入合计，但张数要提示出来。
//
// 月底汇总 5 张、系统里还躺着 2 张草稿，不提示的话会计会以为账记全了。
func TestSummaryExcludesDraftsButCountsThem(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	before, err := db.Summary().BuildSummary(ctx, sd("2025-09-01"), sd("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	if before.DraftCount != 0 {
		t.Fatalf("夹具里不该有草稿，实际 %d 张", before.DraftCount)
	}

	// 存两张草稿：草稿不占号、不进总账
	for i := 0; i < 2; i++ {
		saveDraft(t, db, "2025-09-20", "草稿测试", money100(500))
	}

	after, err := db.Summary().BuildSummary(ctx, sd("2025-09-01"), sd("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	if after.VoucherCount != before.VoucherCount {
		t.Errorf("草稿不该计入张数：%d → %d", before.VoucherCount, after.VoucherCount)
	}
	if after.DebitTotal != before.DebitTotal {
		t.Errorf("草稿不该计入发生额：%s → %s", before.DebitTotal, after.DebitTotal)
	}
	if after.DraftCount != 2 {
		t.Errorf("草稿数 = %d，期望 2", after.DraftCount)
	}
	var noted bool
	for _, n := range after.Notes {
		if strings.Contains(n, "草稿") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("应提示有草稿未记账，实际 %v", after.Notes)
	}
}

// 期间边界：区间外的凭证不进表。
func TestSummaryRespectsDateRange(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	full, err := db.Summary().BuildSummary(ctx, sd("2025-09-01"), sd("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	// 只到 09-05：只剩 ①②两张
	part, err := db.Summary().BuildSummary(ctx, sd("2025-09-01"), sd("2025-09-05"))
	if err != nil {
		t.Fatal(err)
	}
	if part.VoucherCount >= full.VoucherCount {
		t.Errorf("缩短期间后张数没有减少：%d → %d", full.VoucherCount, part.VoucherCount)
	}
	if int64(part.DebitTotal) >= int64(full.DebitTotal) {
		t.Error("缩短期间后发生额没有减少")
	}
	// 与总账一致
	var wantDebit, wantCount int64
	if err := db.sql.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(debit),0), COUNT(DISTINCT voucher_id)
		  FROM ledger_entry WHERE biz_date BETWEEN '2025-09-01' AND '2025-09-05'`).
		Scan(&wantDebit, &wantCount); err != nil {
		t.Fatal(err)
	}
	if int64(part.DebitTotal) != wantDebit || int64(part.VoucherCount) != wantCount {
		t.Errorf("区间内 借%d/%d张，总账 借%d/%d张",
			part.DebitTotal, part.VoucherCount, wantDebit, wantCount)
	}
}

// 空档日：09-02~09-04、09-06~09-09… 夹具里只有 01/05/10/15/25 五天有业务。
func TestSummaryDayGaps(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	rep, err := db.Summary().BuildSummary(ctx, sd("2025-09-01"), sd("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.DayRows) != 5 {
		t.Fatalf("有业务的日期 = %d 天，期望 5", len(rep.DayRows))
	}
	gaps := rep.DayGapDays()
	if len(gaps) != 25 {
		t.Errorf("空档 = %d 天，期望 25（30 天 − 5 天有业务）", len(gaps))
	}
}

// 非法期间要挡住。
func TestSummaryRejectsBadRange(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if _, err := db.Summary().BuildSummary(ctx,
		sd("2025-09-30"), sd("2025-09-01")); err == nil {
		t.Error("起始日晚于截止日应报错")
	}
	if _, err := db.Summary().BuildSummary(ctx,
		calendar.Date{}, sd("2025-09-30")); err == nil {
		t.Error("无效起始日应报错")
	}
}

// 科目名要带出来，界面不必再查一次科目表。
func TestSummaryCarriesAccountNames(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	rep, err := db.Summary().BuildSummary(ctx, sd("2025-09-01"), sd("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	byCode := map[string]summary.AccountRow{}
	for _, a := range rep.AccountRows {
		byCode[a.AccountCode] = a
	}
	if got := byCode["1002"].AccountName; got != "银行存款" {
		t.Errorf("1002 的名称 = %q，期望 银行存款", got)
	}
	// 明细科目带上级全名
	if got := byCode["560206"].FullName; !strings.Contains(got, "管理费用") {
		t.Errorf("560206 的全名 = %q，应含「管理费用」", got)
	}
	if got := byCode["1002"].Count; got == 0 {
		t.Error("1002 的分录条数不该是 0")
	}
}
