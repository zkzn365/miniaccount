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
	"miniaccount/internal/domain/voucher"
)

// columnarBook 造一个含管理费用、增值税两类业务的账套。
type columnarBook struct {
	db     *DB
	accIDs map[string]int64
}

func setupColumnar(t *testing.T) *columnarBook {
	t.Helper()
	ctx := context.Background()
	db := newTestDB(t)
	accIDs, err := db.Accounts().IDsByCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return &columnarBook{db: db, accIDs: accIDs}
}

// post 过账一张凭证。
func (b *columnarBook) post(t *testing.T, date, remark string, entries ...ledger.Entry) int64 {
	t.Helper()
	ctx := context.Background()
	v, err := voucher.New(voucher.WordJi, calendar.MustParse(date), "李会计")
	if err != nil {
		t.Fatalf("建凭证失败: %v", err)
	}
	v.Remark = remark
	for _, e := range entries {
		if err := v.AddEntry(e); err != nil {
			t.Fatalf("加分录失败（%s）：%v", e.AccountCode, err)
		}
	}
	saved, err := b.db.Vouchers().SaveDraft(ctx, DraftInput{Voucher: v, CreatedBy: "李会计"})
	if err != nil {
		t.Fatalf("保存凭证失败: %v", err)
	}
	loaded, err := b.db.Vouchers().Get(ctx, saved.VoucherID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.db.Vouchers().Post(ctx, PostInput{
		Voucher: loaded, Accounts: b.accIDs, PostingBy: "李会计", At: time.Now(),
	})
	if err != nil {
		t.Fatalf("过账失败: %v", err)
	}
	return res.VoucherID
}

// deptAux 返回部门辅助核算（管理费用各明细都要求部门）。
func deptAux(dept int64) ledger.Aux { return ledger.Aux{DeptID: &dept} }

var (
	colFrom = calendar.Date{Year: 2025, Month: 3, Day: 1}
	colTo   = calendar.Date{Year: 2025, Month: 3, Day: 31}
)

// seedMgmt 记三笔管理费用。
func (b *columnarBook) seedMgmt(t *testing.T) {
	t.Helper()
	dept := int64(1)
	// 3-05 办公用品 300
	b.post(t, "2025-03-05", "办公用品",
		ledger.Entry{AccountCode: "560206", Summary: "办公用品",
			Debit: money.Money(30_000), Aux: deptAux(dept)},
		ledger.Entry{AccountCode: "1002", Summary: "办公用品", Credit: money.Money(30_000)})
	// 3-12 出差机票 2400
	b.post(t, "2025-03-12", "出差机票",
		ledger.Entry{AccountCode: "560207", Summary: "出差机票",
			Debit: money.Money(240_000), Aux: deptAux(dept)},
		ledger.Entry{AccountCode: "1002", Summary: "出差机票", Credit: money.Money(240_000)})
	// 3-20 又办公用品 120
	b.post(t, "2025-03-20", "订书机",
		ledger.Entry{AccountCode: "560206", Summary: "订书机",
			Debit: money.Money(12_000), Aux: deptAux(dept)},
		ledger.Entry{AccountCode: "1002", Summary: "订书机", Credit: money.Money(12_000)})
}

// ★ 这是这张表最要紧的一条验证：多栏式把金额搬到了横向上，
// **横向加总必须与总账一字不差**。
//
// 一旦某笔发生额没归上栏目或被算了两次，表面上每一栏都合情合理，
// 只有和科目余额表对一次才能发现。
func TestColumnarMatchesTrialBalance(t *testing.T) {
	ctx := context.Background()
	b := setupColumnar(t)
	b.seedMgmt(t)

	rep, err := b.db.Columnar().BuildColumnar(ctx, "5602", colFrom, colTo)
	if err != nil {
		t.Fatalf("生成多栏式明细账失败: %v", err)
	}
	if errs := rep.Check(); len(errs) != 0 {
		t.Fatalf("不变式应成立: %v", errs)
	}

	// 科目余额表的 5602 行（汇总科目行带其全部下级的合计）
	tb, err := b.db.Reports().TrialBalanceReport(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatal(err)
	}
	var wantDebit, wantCredit money.Money
	var leafDebit money.Money
	var found bool
	for _, row := range tb.Rows {
		if row.AccountCode == "5602" {
			wantDebit, wantCredit, found = row.PeriodDebit, row.PeriodCredit, true
		}
		// 顺带把明细科目行也加一遍，两条路必须得到同一个数
		if strings.HasPrefix(row.AccountCode, "5602") && row.IsLeaf {
			leafDebit = leafDebit.Add(row.PeriodDebit)
		}
	}
	if !found {
		t.Fatal("科目余额表里没找到 5602")
	}
	if wantDebit == 0 {
		t.Fatal("汇总科目行的本期发生额不该是 0 —— 分录只落在明细科目上，" +
			"汇总行必须带下级合计，否则会计打开余额表第一眼看到的就是「管理费用 0 元」")
	}
	if wantDebit != leafDebit {
		t.Errorf("汇总行 %s 与明细行之和 %s 不一致", wantDebit, leafDebit)
	}
	if rep.DebitTotal != wantDebit {
		t.Errorf("借方发生额 = %s，科目余额表 = %s", rep.DebitTotal, wantDebit)
	}
	if rep.CreditTotal != wantCredit {
		t.Errorf("贷方发生额 = %s，科目余额表 = %s", rep.CreditTotal, wantCredit)
	}
	// 期末余额也要对上（借方正数）
	if rep.Closing != wantDebit.Sub(wantCredit) {
		t.Errorf("期末余额 = %s，科目余额表的期末借方 − 期末贷方 = %s",
			rep.Closing, wantDebit.Sub(wantCredit))
	}
}

// 栏目必须来自下级科目，且金额按科目正确分流。
func TestColumnarSplitsByChildAccount(t *testing.T) {
	ctx := context.Background()
	b := setupColumnar(t)
	b.seedMgmt(t)

	rep, err := b.db.Columnar().BuildColumnar(ctx, "5602", colFrom, colTo)
	if err != nil {
		t.Fatal(err)
	}
	if got := colOf(t, rep, "560206"); got != money.Money(42_000) {
		t.Errorf("办公费栏 = %s，期望 420.00", got)
	}
	if got := colOf(t, rep, "560207"); got != money.Money(240_000) {
		t.Errorf("差旅费栏 = %s，期望 2400.00", got)
	}
	// 17 个下级 → 17 栏
	if len(rep.Columns) != 17 {
		t.Errorf("栏目数 = %d，期望 17（5602 的全部下级）", len(rep.Columns))
	}
	// 栏目标题必须是短名，否则 17 栏排不下
	for _, c := range rep.Columns {
		if strings.Contains(c.Label, "—") {
			t.Errorf("栏目标题 %q 仍带上级名，应为短名（如「办公费」）", c.Label)
		}
	}
	if got := shortName("管理费用—办公费"); got != "办公费" {
		t.Errorf("shortName = %q", got)
	}
	if got := shortName("银行存款"); got != "银行存款" {
		t.Errorf("无分隔符时 shortName 应原样返回，实际 %q", got)
	}
}

// ★ 增值税：栏目自带方向，生成「借方 6 栏 + 贷方 4 栏」的双侧多栏式。
//
// 栏目直接来自科目表里 222101 的 10 个子科目 —— 财会〔2016〕22号
// 规定的专栏就是这 10 个，不需要任何硬编码的专栏表。
func TestColumnarVATColumnsFromChartOfAccounts(t *testing.T) {
	ctx := context.Background()
	b := setupColumnar(t)
	// 采购：借 进项税额 13,000
	b.post(t, "2025-03-05", "采购",
		ledger.Entry{AccountCode: "22210101", Summary: "进项税额", Debit: money.Money(1_300_000)},
		ledger.Entry{AccountCode: "1002", Summary: "采购", Credit: money.Money(1_300_000)})
	// 销售：贷 销项税额 26,000
	b.post(t, "2025-03-10", "销售",
		ledger.Entry{AccountCode: "1002", Summary: "销售", Debit: money.Money(2_600_000)},
		ledger.Entry{AccountCode: "22210102", Summary: "销项税额", Credit: money.Money(2_600_000)})
	// 进项税额转出：贷 进项税额转出 2,000
	b.post(t, "2025-03-18", "非正常损失",
		ledger.Entry{AccountCode: "560217", Summary: "非正常损失",
			Debit: money.Money(200_000), Aux: deptAux(1)},
		ledger.Entry{AccountCode: "22210108", Summary: "进项税额转出", Credit: money.Money(200_000)})
	// 缴纳本期税款：借 已交税金 5,000
	b.post(t, "2025-03-25", "缴纳增值税",
		ledger.Entry{AccountCode: "22210103", Summary: "缴纳增值税", Debit: money.Money(500_000)},
		ledger.Entry{AccountCode: "1002", Summary: "缴纳增值税", Credit: money.Money(500_000)})

	rep, err := b.db.Columnar().BuildColumnar(ctx, "222101", colFrom, colTo)
	if err != nil {
		t.Fatalf("生成应交增值税多栏式明细账失败: %v", err)
	}

	if len(rep.Columns) != 10 {
		t.Fatalf("栏目数 = %d，期望 10（财会〔2016〕22号 规定的专栏）", len(rep.Columns))
	}
	// 借方栏目在前、贷方栏目在后
	var seenCredit bool
	for _, c := range rep.Columns {
		if c.Side == "credit" {
			seenCredit = true
		} else if seenCredit {
			t.Errorf("栏目「%s」是借方栏，却排在贷方栏之后", c.Label)
		}
	}
	// 借方 6 栏：进项税额、已交税金、转出未交增值税、减免税款、
	//           出口抵减内销产品应纳税额、销项税额抵减
	// 贷方 4 栏：销项税额、转出多交增值税、出口退税、进项税额转出
	if got := rep.SideTotal("debit"); got != money.Money(1_800_000) {
		t.Errorf("借方栏目合计 = %s，期望 18,000.00（13,000 进项 + 5,000 已交）", got)
	}
	if got := rep.SideTotal("credit"); got != money.Money(2_800_000) {
		t.Errorf("贷方栏目合计 = %s，期望 28,000.00（26,000 销项 + 2,000 转出）", got)
	}
	// 应交未交 10,000（贷方余额）
	if rep.Closing != money.Money(-1_000_000) || rep.ClosingDir != "贷" {
		t.Errorf("期末 = %s（%s），期望 -10,000.00 贷", rep.Closing, rep.ClosingDir)
	}
	if errs := rep.Check(); len(errs) != 0 {
		t.Errorf("不变式应成立: %v", errs)
	}
}

// ★ 红字冲销：冲销凭证是一张有编号的真实凭证，
// 它必须落在**被冲科目那一栏**里成为负数，而不是被吞掉或串栏。
func TestColumnarReversalShowsAsNegativeInOwnColumn(t *testing.T) {
	ctx := context.Background()
	b := setupColumnar(t)
	b.seedMgmt(t)

	// 冲销 3-20 那笔订书机 120 元
	// 直接录一张借贷互换的凭证（红字凭证模型）
	before, err := b.db.Columnar().BuildColumnar(ctx, "5602", colFrom, colTo)
	if err != nil {
		t.Fatal(err)
	}
	office := colOf(t, before, "560206")

	b.post(t, "2025-03-25", "冲销误记的订书机",
		ledger.Entry{AccountCode: "560206", Summary: "冲销误记的订书机",
			Credit: money.Money(12_000), Aux: deptAux(1)},
		ledger.Entry{AccountCode: "1002", Summary: "冲销误记的订书机", Debit: money.Money(12_000)})

	after, err := b.db.Columnar().BuildColumnar(ctx, "5602", colFrom, colTo)
	if err != nil {
		t.Fatal(err)
	}
	if got := colOf(t, after, "560206"); got != office.Sub(money.Money(12_000)) {
		t.Errorf("冲销后办公费栏 = %s，期望 %s（原 %s − 120.00）",
			got, office.Sub(money.Money(12_000)), office)
	}
	// 冲销行要在，金额为负，栏目仍是办公费
	var ok bool
	for _, row := range after.Rows {
		if row.Summary != "冲销误记的订书机" {
			continue
		}
		ok = true
		if after.Columns[row.ColumnIndex].Key != "560206" {
			t.Errorf("冲销行落到了「%s」栏，期望「办公费」栏",
				after.Columns[row.ColumnIndex].Label)
		}
		if row.Amount != money.Money(-12_000) {
			t.Errorf("冲销行金额 = %s，期望 -120.00（红字）", row.Amount)
		}
	}
	if !ok {
		t.Error("冲销行不该从多栏式明细账里消失")
	}
	// 借贷发生额是总额：贷方增加了 120
	if after.CreditTotal != before.CreditTotal.Add(money.Money(12_000)) {
		t.Errorf("贷方发生额 = %s，期望 %s",
			after.CreditTotal, before.CreditTotal.Add(money.Money(12_000)))
	}
	if errs := after.Check(); len(errs) != 0 {
		t.Errorf("不变式应成立: %v", errs)
	}
}

// 期初余额来自本期之前，且逐行累计。
func TestColumnarOpeningFromPriorPeriod(t *testing.T) {
	ctx := context.Background()
	b := setupColumnar(t)
	// 2 月记一笔办公费 500
	b.post(t, "2025-02-10", "2 月办公用品",
		ledger.Entry{AccountCode: "560206", Summary: "2 月办公用品",
			Debit: money.Money(50_000), Aux: deptAux(1)},
		ledger.Entry{AccountCode: "1002", Summary: "2 月办公用品", Credit: money.Money(50_000)})
	b.seedMgmt(t)

	rep, err := b.db.Columnar().BuildColumnar(ctx, "5602", colFrom, colTo)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Opening != money.Money(50_000) {
		t.Errorf("期初余额 = %s，期望 500.00（2 月那笔）", rep.Opening)
	}
	if rep.OpeningDir != "借" {
		t.Errorf("期初方向 = %q", rep.OpeningDir)
	}
	// 3 月发生 2820，期末 3320
	if rep.Closing != money.Money(332_000) {
		t.Errorf("期末余额 = %s，期望 3320.00", rep.Closing)
	}
	// 期初那笔不能出现在 3 月的行里
	for _, row := range rep.Rows {
		if row.Summary == "2 月办公用品" {
			t.Error("2 月的分录不该出现在 3 月的多栏式明细账里")
		}
	}
}

// 截止到某一天：期间之后的业务不进表。
func TestColumnarRespectsDateRange(t *testing.T) {
	ctx := context.Background()
	b := setupColumnar(t)
	b.seedMgmt(t)

	// 只看到 3-12：办公费 300、差旅费 2400；3-20 那笔 120 不进
	rep, err := b.db.Columnar().BuildColumnar(ctx, "5602",
		colFrom, calendar.Date{Year: 2025, Month: 3, Day: 12})
	if err != nil {
		t.Fatal(err)
	}
	if got := colOf(t, rep, "560206"); got != money.Money(30_000) {
		t.Errorf("截至 3-12 的办公费 = %s，期望 300.00", got)
	}
	if len(rep.Rows) != 2 {
		t.Errorf("行数 = %d，期望 2", len(rep.Rows))
	}
}

// 没有下级科目的科目做不出多栏式，必须给出指向正确工具的报错。
func TestColumnarRejectsLeafAccount(t *testing.T) {
	ctx := context.Background()
	b := setupColumnar(t)

	_, err := b.db.Columnar().BuildColumnar(ctx, "1002", colFrom, colTo)
	if err == nil {
		t.Fatal("没有下级科目应报错")
	}
	// 报错要让用户知道该用什么
	if !strings.Contains(err.Error(), "明细账") {
		t.Errorf("报错应提示改用明细账，实际 %q", err.Error())
	}
}

// 科目不存在时报错，而不是给一张空表。
func TestColumnarRejectsUnknownAccount(t *testing.T) {
	ctx := context.Background()
	b := setupColumnar(t)
	if _, err := b.db.Columnar().BuildColumnar(ctx, "999999", colFrom, colTo); err == nil {
		t.Error("不存在的科目应报错")
	}
}

// 期间非法要挡住。
func TestColumnarRejectsBadRange(t *testing.T) {
	ctx := context.Background()
	b := setupColumnar(t)
	if _, err := b.db.Columnar().BuildColumnar(ctx, "5602",
		colTo, colFrom); err == nil {
		t.Error("起始日晚于截止日应报错")
	}
	if _, err := b.db.Columnar().BuildColumnar(ctx, "5602",
		calendar.Date{}, colTo); err == nil {
		t.Error("无效起始日应报错")
	}
}

// 兜底栏：分录直接记在上级科目上时不能丢，要进「其他」栏并提示。
func TestColumnarCatchesMovementOnParent(t *testing.T) {
	ctx := context.Background()
	b := setupColumnar(t)
	b.seedMgmt(t)

	// 正常业务走明细科目；这里手工造一条直接记在 5602 上的分录，
	// 模拟「历史上 5602 是明细科目、后来才加了下级」这类账套
	if err := b.db.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO ledger_entry (biz_date, year, month, account_id, debit, credit,
			                          voucher_id, line_no, summary, reverted, created_at)
			VALUES ('2025-03-28', 2025, 3, ?, 5000, 0, 1, 99, '直接记在管理费用上', 0, ?)`,
			b.accIDs["5602"], nowString())
		return err
	}); err != nil {
		t.Fatalf("造分录失败: %v", err)
	}

	rep, err := b.db.Columnar().BuildColumnar(ctx, "5602", colFrom, colTo)
	if err != nil {
		t.Fatal(err)
	}
	var other *int
	for i, c := range rep.Columns {
		if c.Other {
			idx := i
			other = &idx
		}
	}
	if other == nil {
		t.Fatal("应自动补出「其他」兜底栏")
	}
	if rep.ColumnTotals[*other] != money.Money(5_000) {
		t.Errorf("兜底栏 = %s，期望 50.00", rep.ColumnTotals[*other])
	}
	// 总额必须含它，否则这张表就和总账对不上了
	if rep.DebitTotal != money.Money(30_000+240_000+12_000+5_000) {
		t.Errorf("借方发生额 = %s，期望 2870.00（含兜底栏那 50.00）", rep.DebitTotal)
	}
	var noted bool
	for _, n := range rep.Notes {
		if strings.Contains(n, "其他") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("应提示有发生额未归入栏目，实际 %v", rep.Notes)
	}
	if errs := rep.Check(); len(errs) != 0 {
		t.Errorf("不变式应成立: %v", errs)
	}
}

// 候选科目列表只含有下级的科目。
func TestColumnCandidates(t *testing.T) {
	ctx := context.Background()
	b := setupColumnar(t)

	list, err := b.db.Columnar().ColumnCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("应至少返回一个候选科目")
	}
	seen := map[string]bool{}
	for _, a := range list {
		seen[a.Code] = true
	}
	for _, want := range []string{"5602", "222101", "2221"} {
		if !seen[want] {
			t.Errorf("候选里应包含 %s", want)
		}
	}
	// 没有下级的科目不该出现 —— 选了也做不出来
	for _, no := range []string{"1002", "1122", "2202"} {
		if seen[no] {
			t.Errorf("候选里不该出现没有下级的 %s", no)
		}
	}
}

func colOf(t *testing.T, r interface {
	ColumnTotal(string) (money.Money, bool)
}, key string) money.Money {
	t.Helper()
	v, ok := r.ColumnTotal(key)
	if !ok {
		t.Fatalf("栏目 %s 不存在", key)
	}
	return v
}
