package sqlite

import (
	"context"
	"testing"
	"time"

	"miniaccount/internal/domain/cashflow"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
)

// cfBook 构造一个现金流场景：
//
//	① 股东投入 100,000          借 银行存款 / 贷 实收资本        → 筹资流入
//	② 销售 90,400（含税）       借 应收账款 / 贷 收入 + 销项税
//	③ 收回货款 90,400           借 银行存款 / 贷 应收账款          → 经营流入
//	④ 支付货款 3,000            借 应付账款 / 贷 银行存款          → 经营流出
//	⑤ 发放工资 15,000           借 应付职工薪酬 / 贷 银行存款       → 经营流出
//	⑥ 购置设备 50,000           借 固定资产 / 贷 银行存款          → 投资流出
//	⑦ 提现 5,000                借 库存现金 / 贷 银行存款          → 不是现金流
//
//	经营净额 = 90,400 − 3,000 − 15,000 = 72,400
//	投资净额 = −50,000
//	筹资净额 = +100,000
//	净增加   = 122,400；期初 0 → 期末 122,400
func cfBook(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	accIDs, err := db.Accounts().IDsByCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cust := mustContacts(t, db, "customer", "客户甲")
	sup := mustContacts(t, db, "supplier", "供应商乙")
	shareholder := mustContacts(t, db, "shareholder", "张三")
	dept := int64(1)

	post := func(date string, lines ...ledger.Entry) {
		t.Helper()
		v, err := voucher.New(voucher.WordJi, mustDate(date), "李会计")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range lines {
			if err := v.AddEntry(e); err != nil {
				t.Fatalf("%s 加分录失败: %v", date, err)
			}
		}
		if _, err := db.Vouchers().Post(ctx, PostInput{
			Voucher: v, Accounts: accIDs, PostingBy: "王主管", At: time.Now()}); err != nil {
			t.Fatalf("过账 %s 失败: %v", date, err)
		}
	}
	aux := func(id *int64) ledger.Aux { return ledger.Aux{ContactID: id} }

	post("2025-09-01",
		ledger.Entry{AccountCode: "1002", Summary: "收到投资款", Debit: money100(100000)},
		ledger.Entry{AccountCode: "3001", Summary: "收到投资款",
			Credit: money100(100000), Aux: aux(&shareholder)},
	)
	post("2025-09-05",
		ledger.Entry{AccountCode: "1122", Summary: "销售商品",
			Debit: money100(90400), Aux: aux(&cust)},
		ledger.Entry{AccountCode: "5001", Summary: "销售商品", Credit: money100(80000)},
		ledger.Entry{AccountCode: "22210102", Summary: "销项税额", Credit: money100(10400)},
	)
	post("2025-09-06",
		ledger.Entry{AccountCode: "1002", Summary: "收回货款", Debit: money100(90400)},
		ledger.Entry{AccountCode: "1122", Summary: "收回货款",
			Credit: money100(90400), Aux: aux(&cust)},
	)
	post("2025-09-10",
		ledger.Entry{AccountCode: "2202", Summary: "支付货款",
			Debit: money100(3000), Aux: aux(&sup)},
		ledger.Entry{AccountCode: "1002", Summary: "支付货款", Credit: money100(3000)},
	)
	post("2025-09-15",
		ledger.Entry{AccountCode: "221101", Summary: "发放工资", Debit: money100(15000)},
		ledger.Entry{AccountCode: "1002", Summary: "发放工资", Credit: money100(15000)},
	)
	post("2025-09-20",
		ledger.Entry{AccountCode: "1601", Summary: "购置设备",
			Debit: money100(50000), Aux: ledger.Aux{DeptID: &dept}},
		ledger.Entry{AccountCode: "1002", Summary: "购置设备", Credit: money100(50000)},
	)
	post("2025-09-28",
		ledger.Entry{AccountCode: "1001", Summary: "提取现金", Debit: money100(5000)},
		ledger.Entry{AccountCode: "1002", Summary: "提取现金", Credit: money100(5000)},
	)
}

func TestCashFlowStatement(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	cfBook(t, db)

	st, err := db.CashFlow().StatementForPeriod(ctx, period.NewKey(2025, 9), nil)
	if err != nil {
		t.Fatalf("生成现金流量表失败: %v", err)
	}

	// 销售收款含税 90,400
	if got := st.AmountOf(1); got != money100(90400) {
		t.Errorf("销售收到的现金 = %s，期望 90400.00", got)
	}
	if got := st.AmountOf(5); got != money100(-3000) {
		t.Errorf("购买商品支付的现金 = %s，期望 -3000.00", got)
	}
	if got := st.AmountOf(6); got != money100(-15000) {
		t.Errorf("支付职工薪酬 = %s，期望 -15000.00", got)
	}
	if got := st.AmountOf(7); !got.IsZero() {
		t.Errorf("本月未向税局缴税，税费行应为 0，实际 %s", got)
	}

	// 三大活动净额
	if got := st.AmountOf(10); got != money100(72400) {
		t.Errorf("经营活动净额 = %s，期望 72400.00", got)
	}
	if got := st.AmountOf(19); got != money100(-50000) {
		t.Errorf("投资活动净额 = %s，期望 -50000.00", got)
	}
	if got := st.AmountOf(33); got != money100(100000) {
		t.Errorf("筹资活动净额 = %s，期望 100000.00", got)
	}
	if got := st.AmountOf(34); got != money100(122400) {
		t.Errorf("净增加额 = %s，期望 122400.00", got)
	}
	if st.ClosingCash != money100(122400) {
		t.Errorf("期末现金 = %s，期望 122400.00", st.ClosingCash)
	}

	// 勾稽关系全部成立
	for _, e := range st.Check() {
		t.Errorf("勾稽关系不成立: %v", e)
	}
	if len(st.Unclassified) != 0 {
		t.Errorf("不应有未归类项，实际 %v", st.Unclassified)
	}
}

// ★ 现金科目之间互转不能出现在现金流量表里
func TestCashFlowSkipsInternalTransfers(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	cfBook(t, db)

	entries, err := db.CashFlow().Entries(ctx, mustDate("2025-09-01"), mustDate("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	// 7 张凭证中：② 销售未收款（不涉及现金科目），⑦ 提现是现金互转。
	// 因此真正的现金分录只有 ①③④⑤⑥ 共 5 笔。
	if len(entries) != 6-1 {
		t.Errorf("现金分录数 = %d，期望 5（未收款的销售不计，提现不算现金流）", len(entries))
	}
	for _, e := range entries {
		if e.Summary == "提取现金" {
			t.Error("现金互转不应作为现金流量")
		}
		if e.Amount.IsZero() {
			t.Errorf("凭证 %s 的现金流为零，不应出现", e.VoucherNo)
		}
	}
}

// ★ 推算的期末现金必须等于账面期末现金，否则说明漏了科目
func TestCashFlowCrossChecksClosingCash(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	cfBook(t, db)

	st, err := db.CashFlow().StatementForPeriod(ctx, period.NewKey(2025, 9), nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.ActualClosingCash == nil {
		t.Fatal("应带回账面期末现金")
	}
	if *st.ActualClosingCash != money100(122400) {
		t.Errorf("账面期末现金 = %s，期望 122400.00", *st.ActualClosingCash)
	}
	if d := st.CashMismatch(); !d.IsZero() {
		t.Errorf("推算与账面期末现金差 %s，期望 0", d)
	}
}

// 期初余额：从 1 月开始累计
func TestCashFlowOpeningBalance(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	cfBook(t, db)

	// 9 月单月的期初应当是 0（全部业务都在 9 月发生）
	st, err := db.CashFlow().StatementForPeriod(ctx, period.NewKey(2025, 9), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !st.OpeningCash.IsZero() {
		t.Errorf("期初现金 = %s，期望 0", st.OpeningCash)
	}

	// 10 月无业务，期初应等于 9 月期末
	st10, err := db.CashFlow().StatementForPeriod(ctx, period.NewKey(2025, 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	if st10.OpeningCash != money100(122400) {
		t.Errorf("10 月期初现金 = %s，期望 122400.00", st10.OpeningCash)
	}
	if st10.ClosingCash != money100(122400) {
		t.Errorf("10 月期末现金 = %s，期望 122400.00", st10.ClosingCash)
	}
}

// 结账之后现金流量表仍应成立（结转凭证不动现金科目）
func TestCashFlowAfterClosing(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	cfBook(t, db)
	for m := 1; m <= 9; m++ {
		closeP(t, db, 2025, m, "王主管")
	}

	st, err := db.CashFlow().StatementForPeriod(ctx, period.NewKey(2025, 9), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.AmountOf(10); got != money100(72400) {
		t.Errorf("结账后经营活动净额 = %s，期望 72400.00", got)
	}
	if st.ClosingCash != money100(122400) {
		t.Errorf("结账后期末现金 = %s，期望 122400.00", st.ClosingCash)
	}
	for _, e := range st.Check() {
		t.Errorf("结账后勾稽关系不成立: %v", e)
	}
	if len(st.Unclassified) != 0 {
		t.Errorf("结转凭证不应产生未归类项，实际 %v", st.Unclassified)
	}
}

// 期初现金来自上年结转时也要能取到
func TestCashFlowOpeningFromPriorYear(t *testing.T) {
	ctx := context.Background()
	db := newTestDBThrough(t, 12)
	accIDs, err := db.Accounts().IDsByCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shareholder := mustContacts(t, db, "shareholder", "张三")
	for _, spec := range []struct{ date string }{
		{"2025-11-10"}, {"2025-12-10"},
	} {
		v, err := voucher.New(voucher.WordJi, mustDate(spec.date), "李会计")
		if err != nil {
			t.Fatal(err)
		}
		addLine(t, v, "1002", "投入", money100(10000), 0)
		if err := v.AddEntry(ledger.Entry{
			AccountCode: "3001", Summary: "投入",
			Credit: money100(10000), Aux: ledger.Aux{ContactID: &shareholder},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Vouchers().Post(ctx, PostInput{
			Voucher: v, Accounts: accIDs, PostingBy: "王主管", At: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	st, err := db.CashFlow().StatementForPeriod(ctx, period.NewKey(2025, 12), nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.OpeningCash != money100(10000) {
		t.Errorf("12 月期初现金 = %s，期望 10000.00", st.OpeningCash)
	}
	if got := st.AmountOf(33); got != money100(10000) {
		t.Errorf("筹资净额 = %s，期望 10000.00", got)
	}
	if st.ClosingCash != money100(20000) {
		t.Errorf("12 月期末现金 = %s，期望 20000.00", st.ClosingCash)
	}
}

// 现金科目清单与领域层同源：新增现金等价物科目不会漏
func TestCashPrefixClauseMatchesDomainRoots(t *testing.T) {
	like, args := cashPrefixClause("a.code")
	if len(args) != len(cashflow.CashAccountRoots) {
		t.Fatalf("参数数 = %d，期望 %d", len(args), len(cashflow.CashAccountRoots))
	}
	if like == "" {
		t.Fatal("应生成非空条件")
	}
	for _, root := range cashflow.CashAccountRoots {
		found := false
		for _, a := range args {
			if a == root+"%" {
				found = true
			}
		}
		if !found {
			t.Errorf("缺少现金科目前缀 %s", root)
		}
	}
	// 每个现金科目都应被判定为现金科目
	for _, code := range []string{"1001", "1002", "1012", "100201"} {
		if !cashflow.IsCashAccount(code) {
			t.Errorf("%s 应被识别为现金科目", code)
		}
	}
	for _, code := range []string{"1122", "1003", "2202"} {
		if cashflow.IsCashAccount(code) {
			t.Errorf("%s 不应被识别为现金科目", code)
		}
	}
}

// Display 把流出行显示为正数，避免报表出现「支付的… −3,000」
func TestLineDisplay(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	cfBook(t, db)
	st, err := db.CashFlow().StatementForPeriod(ctx, period.NewKey(2025, 9), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range st.Lines {
		if l.Inflow || l.IsSubtotal {
			if l.Display() != l.Amount {
				t.Errorf("行 %d（%s）展示值应等于原始金额", l.No, l.Name)
			}
			continue
		}
		if l.Display().IsNegative() {
			t.Errorf("流出行 %d（%s）展示值不应为负：%s", l.No, l.Name, l.Display())
		}
	}
}
