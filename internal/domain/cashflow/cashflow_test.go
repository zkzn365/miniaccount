package cashflow

import (
	"testing"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

func y(n int64) money.Money { return money.Money(n) * money.Yuan }

var (
	d0901 = calendar.MustParse("2025-09-01")
	d0930 = calendar.MustParse("2025-09-30")
)

func ca(code string, amount money.Money) CounterAccount {
	return CounterAccount{Code: code, Amount: amount}
}

func period_() (calendar.Date, calendar.Date) { return d0901, d0930 }

// ---------------------------------------------------------------------------
// 归类规则
// ---------------------------------------------------------------------------

func TestClassify(t *testing.T) {
	cl := DefaultClassifier()
	cases := map[string]int{
		"5001": lineSalesCash,       // 主营业务收入
		"5051": lineSalesCash,       // 其他业务收入
		"1122": lineSalesCash,       // 应收账款
		"2203": lineSalesCash,       // 预收账款
		"5401": lineBuyCash,         // 主营业务成本
		"1403": lineBuyCash,         // 原材料
		"2202": lineBuyCash,         // 应付账款
		"2211": lineStaffCash,       // 应付职工薪酬
		"1601": lineInvestOutTotal,  // 固定资产
		"1701": lineInvestOutTotal,  // 无形资产
		"2001": lineFinanceInTotal,  // 短期借款
		"2501": lineFinanceInTotal,  // 长期借款
		"3001": lineFinanceInTotal,  // 实收资本
		"2232": lineFinanceOutTotal, // 应付利润
	}
	for code, want := range cases {
		if got := cl.Classify(code); got != want {
			t.Errorf("Classify(%s) = %d，期望 %d", code, got, want)
		}
	}
	if got := cl.Classify("9999"); got != 0 {
		t.Errorf("未知科目应返回 0，得到 %d", got)
	}

	// ★ 应交税费必须按现金方向分流：
	//   收到含税货款时，销项税是销售收款的组成部分（准则要求按含税口径列报）；
	//   只有真正向税局缴税才是「支付的各项税费」。
	if got := cl.ClassifyFlow("2221", y(10400)); got != lineSalesCash {
		t.Errorf("收到含税货款时 2221 应归入销售收款，得到 %d", got)
	}
	if got := cl.ClassifyFlow("2221", y(-10400)); got != lineTaxPaid {
		t.Errorf("缴税时 2221 应归入支付的各项税费，得到 %d", got)
	}
	// 只按科目前缀（不限方向）时，方向限定的规则不应命中
	if got := cl.Classify("2221"); got != 0 {
		t.Errorf("不带方向时 2221 不应命中方向限定规则，得到 %d", got)
	}
}

// 方向限定规则与非方向规则可共存，且更具体的方向规则优先
func TestClassifyFlowFallback(t *testing.T) {
	cl := DefaultClassifier()
	// 5403 只限定为流出；流入时无规则命中
	if got := cl.ClassifyFlow("5403", y(-100)); got != lineTaxPaid {
		t.Errorf("5403 流出应归入税费，得到 %d", got)
	}
	if got := cl.ClassifyFlow("5403", y(100)); got != 0 {
		t.Errorf("5403 流入不应命中流出规则，得到 %d", got)
	}
	// 无方向限定的规则对两个方向都生效（回退）
	if got := cl.ClassifyFlow("5001", y(100)); got != lineSalesCash {
		t.Errorf("5001 流入 = %d", got)
	}
	if got := cl.ClassifyFlow("5001", y(-100)); got != lineSalesCash {
		t.Errorf("5001 流出应回退到同一条规则 = %d", got)
	}
}

// ★ 最长前缀优先：更具体的规则应当胜出
func TestClassifyLongestPrefixWins(t *testing.T) {
	cl := DefaultClassifier()
	// 224102 其他应付款—员工 的规则比 2241 更具体
	if got := cl.Classify("224102"); got != lineOtherOperatingOut {
		t.Errorf("Classify(224102) = %d，期望 %d", got, lineOtherOperatingOut)
	}
	// 自定义更长的规则应覆盖默认
	cl.WithRule("500101", lineOtherOperatingOut)
	if got := cl.Classify("500101"); got != lineOtherOperatingOut {
		t.Errorf("自定义规则未生效，得到 %d", got)
	}
	// 下级科目继承父级规则
	if got := cl.Classify("560207"); got != lineOtherOperatingOut {
		t.Errorf("下级科目应继承父级规则，得到 %d", got)
	}
}

func TestAccountsFor(t *testing.T) {
	cl := DefaultClassifier()
	got := cl.AccountsFor(lineSalesCash)
	if len(got) == 0 {
		t.Fatal("销售收款行应有对应科目")
	}
	// 应包含收入与应收
	var hasIncome, hasAR bool
	for _, c := range got {
		if c == "5001" {
			hasIncome = true
		}
		if c == "1122" {
			hasAR = true
		}
	}
	if !hasIncome || !hasAR {
		t.Errorf("应含收入与应收科目，实际 %v", got)
	}
}

// ---------------------------------------------------------------------------
// 现金科目判定
// ---------------------------------------------------------------------------

func TestIsCashAccount(t *testing.T) {
	for _, code := range []string{"1001", "1002", "1012", "100201", "100101"} {
		if !IsCashAccount(code) {
			t.Errorf("%s 应视为现金科目", code)
		}
	}
	for _, code := range []string{"1122", "2202", "5001", "1003"} {
		if IsCashAccount(code) {
			t.Errorf("%s 不应视为现金科目", code)
		}
	}
}

// ---------------------------------------------------------------------------
// 生成报表
// ---------------------------------------------------------------------------

// 一个典型的月份：
//
//	收货款 90,400（其中收入 80,000 + 销项税 10,400）
//	付货款 3,000（应付账款）
//	发工资 15,000（应付职工薪酬）
//	买设备 50,000（固定资产）
//	股东投入 100,000（实收资本）
func sampleEntries() []CashEntry {
	return []CashEntry{
		{Date: calendar.MustParse("2025-09-05"), VoucherNo: "记-0001",
			CashAccount: "1002", Amount: y(90400), Summary: "收货款",
			CounterAccounts: []CounterAccount{ca("5001", y(-80000)), ca("2221", y(-10400))}},
		{Date: calendar.MustParse("2025-09-10"), VoucherNo: "记-0002",
			CashAccount: "1002", Amount: y(-3000), Summary: "付货款",
			CounterAccounts: []CounterAccount{ca("2202", y(3000))}},
		{Date: calendar.MustParse("2025-09-15"), VoucherNo: "记-0003",
			CashAccount: "1002", Amount: y(-15000), Summary: "发工资",
			CounterAccounts: []CounterAccount{ca("2211", y(15000))}},
		{Date: calendar.MustParse("2025-09-20"), VoucherNo: "记-0004",
			CashAccount: "1002", Amount: y(-50000), Summary: "买设备",
			CounterAccounts: []CounterAccount{ca("1601", y(50000))}},
		{Date: calendar.MustParse("2025-09-01"), VoucherNo: "记-0005",
			CashAccount: "1002", Amount: y(100000), Summary: "股东投入",
			CounterAccounts: []CounterAccount{ca("3001", y(-100000))}},
	}
}

func TestBuildBasic(t *testing.T) {
	from, to := period_()
	st, err := Build(Input{
		From: from, To: to, CashEntries: sampleEntries(), OpeningCash: y(10000),
	}, nil)
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}

	// 销售收款 90,400 —— 含税口径：销项税是随货款一起收到的现金，
	// 属于「销售商品、提供劳务收到的现金」，不能算作支付的税费
	if got := st.AmountOf(lineSalesCash); got != y(90400) {
		t.Errorf("销售收到的现金 = %s，期望 90400.00（含税口径）", got)
	}
	// 本月没有真正向税局缴税
	if got := st.AmountOf(lineTaxPaid); got != 0 {
		t.Errorf("税费行 = %s，期望 0（销项税不在此列报）", got)
	}
	// 付货款、发工资、买设备
	if got := st.AmountOf(lineBuyCash); got != y(-3000) {
		t.Errorf("购买商品支付的现金 = %s", got)
	}
	if got := st.AmountOf(lineStaffCash); got != y(-15000) {
		t.Errorf("支付职工薪酬 = %s", got)
	}
	if got := st.AmountOf(lineInvestOutTotal); got != y(-50000) {
		t.Errorf("投资活动流出 = %s", got)
	}
	if got := st.AmountOf(lineFinanceInTotal); got != y(100000) {
		t.Errorf("筹资活动流入 = %s", got)
	}

	// 经营活动净额 = 90400 − 3000 − 15000 = 72400
	wantOp := y(90400).Sub(y(3000)).Sub(y(15000))
	if got := st.AmountOf(lineOperatingNet); got != wantOp {
		t.Errorf("经营活动净额 = %s，期望 %s", got, wantOp)
	}
	// 净增加额 = 72400 − 50000 + 100000 = 122400
	wantNet := wantOp.Sub(y(50000)).Add(y(100000))
	if got := st.AmountOf(lineNetIncrease); got != wantNet {
		t.Errorf("净增加额 = %s，期望 %s", got, wantNet)
	}
	// 期末现金 = 10000 + 122400
	if st.ClosingCash != y(10000).Add(wantNet) {
		t.Errorf("期末现金 = %s，期望 %s", st.ClosingCash, y(10000).Add(wantNet))
	}

	// 勾稽关系全部成立
	for _, err := range st.Check() {
		t.Errorf("勾稽关系不成立: %v", err)
	}
	if len(st.Unclassified) != 0 {
		t.Errorf("不应有未归类项，实际 %v", st.Unclassified)
	}
}

// 一笔现金对应多个不同行次的对方科目时，必须按比例分摊
func TestAllocationProportional(t *testing.T) {
	from, to := period_()
	// 付 21,000：其中 18,000 冲应付账款（购货）、3,000 付员工报销
	st, err := Build(Input{
		From: from, To: to, OpeningCash: 0,
		CashEntries: []CashEntry{{
			Date: d0901, CashAccount: "1002", Amount: y(-21000), Summary: "合并付款",
			CounterAccounts: []CounterAccount{
				ca("2202", y(18000)),  // → 购买商品支付的现金
				ca("224102", y(3000)), // → 支付其他与经营活动有关的现金
			},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.AmountOf(lineBuyCash); got != y(-18000) {
		t.Errorf("购货部分 = %s，期望 -18000.00", got)
	}
	if got := st.AmountOf(lineOtherOperatingOut); got != y(-3000) {
		t.Errorf("其他经营部分 = %s，期望 -3000.00", got)
	}
	// 分摊不能丢分
	if sum := st.AmountOf(lineBuyCash).Add(st.AmountOf(lineOtherOperatingOut)); sum != y(-21000) {
		t.Errorf("分摊合计 = %s，期望 -21000.00（分毫不丢）", sum)
	}
}

// ★ 含税销售整笔流入都算销售收款，分摊到同一行次也不会丢分
func TestAllocationSameLine(t *testing.T) {
	from, to := period_()
	st, err := Build(Input{
		From: from, To: to, OpeningCash: 0,
		CashEntries: []CashEntry{{
			Date: d0901, CashAccount: "1002", Amount: y(113000), Summary: "含税销售",
			CounterAccounts: []CounterAccount{
				ca("5001", y(-100000)), // 收入
				ca("2221", y(-13000)),  // 销项税（流入 → 同为销售收款）
			},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.AmountOf(lineSalesCash); got != y(113000) {
		t.Errorf("含税销售收款 = %s，期望 113000.00", got)
	}
	if got := st.AmountOf(lineTaxPaid); got != 0 {
		t.Errorf("销项税不应进入税费行，得到 %s", got)
	}
}

// 分摊除不尽时也不能丢分
func TestAllocationNoRoundingLoss(t *testing.T) {
	from, to := period_()
	// 100 元按 1:1:1 分摊
	st, err := Build(Input{
		From: from, To: to,
		CashEntries: []CashEntry{{
			Date: d0901, CashAccount: "1002", Amount: y(100),
			CounterAccounts: []CounterAccount{
				ca("5001", y(-1)), ca("5051", y(-1)), ca("5301", y(-1)),
			},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := st.AmountOf(lineSalesCash).Add(st.AmountOf(lineOtherOperatingIn))
	if sum != y(100) {
		t.Errorf("分摊合计 = %s，期望 100.00", sum)
	}
}

// ★ 无法归类的分录必须暴露出来，而不是静默丢弃
func TestUnclassifiedExposed(t *testing.T) {
	from, to := period_()
	st, err := Build(Input{
		From: from, To: to, OpeningCash: 0,
		CashEntries: []CashEntry{
			{Date: d0901, VoucherNo: "记-0001", CashAccount: "1002",
				Amount: y(-5000), Summary: "神秘支出",
				CounterAccounts: []CounterAccount{ca("9999", y(5000))}},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Unclassified) != 1 {
		t.Fatalf("应暴露 1 条未归类，实际 %d", len(st.Unclassified))
	}
	u := st.Unclassified[0]
	if u.VoucherNo != "记-0001" || u.Summary != "神秘支出" {
		t.Errorf("未归类项应带凭证号与摘要，得到 %+v", u)
	}
	if len(u.CounterAccounts) != 1 || u.CounterAccounts[0] != "9999" {
		t.Errorf("应列出未被识别的对方科目，得到 %v", u.CounterAccounts)
	}
	// 未归类的不应计入任何行
	if got := st.AmountOf(lineOtherOperatingOut); !got.IsZero() {
		t.Errorf("未归类项不应计入行项目，得到 %s", got)
	}
}

// 一笔分录里部分对方科目可归类、部分不可时，整笔都不计入
// （半截的数字比没有更糟）
func TestPartialClassificationExcluded(t *testing.T) {
	from, to := period_()
	st, err := Build(Input{
		From: from, To: to,
		CashEntries: []CashEntry{{
			Date: d0901, CashAccount: "1002", Amount: y(-1000),
			CounterAccounts: []CounterAccount{
				ca("2202", y(600)), // 可归类
				ca("9999", y(400)), // 不可归类
			},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.AmountOf(lineBuyCash); !got.IsZero() {
		t.Errorf("部分未归类时整笔都不应计入，得到 %s", got)
	}
	if len(st.Unclassified) != 1 {
		t.Errorf("应暴露为未归类，得到 %d 条", len(st.Unclassified))
	}
}

// 没有任何对方科目的分录也应记为未归类
func TestEntryWithoutCounterAccounts(t *testing.T) {
	from, to := period_()
	st, err := Build(Input{
		From: from, To: to,
		CashEntries: []CashEntry{{
			Date: d0901, CashAccount: "1002", Amount: y(-100), Summary: "单边分录",
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Unclassified) != 1 {
		t.Errorf("应暴露为未归类，得到 %d", len(st.Unclassified))
	}
}

// ---------------------------------------------------------------------------
// 勾稽关系
// ---------------------------------------------------------------------------

func TestCheckDetectsImbalance(t *testing.T) {
	st := &Statement{
		OpeningCash: y(1000),
		ClosingCash: y(9999), // 与推算不符
		Lines: []Line{
			{No: lineOperatingNet, Amount: y(500)},
			{No: lineInvestNet, Amount: 0},
			{No: lineFinanceNet, Amount: 0},
			{No: lineNetIncrease, Amount: y(500)},
		},
	}
	errs := st.Check()
	if len(errs) == 0 {
		t.Fatal("期末现金不符应被检出")
	}
}

func TestVerifyClosingCash(t *testing.T) {
	st := &Statement{ClosingCash: y(1000)}
	if err := VerifyClosingCash(st, y(1000)); err != nil {
		t.Errorf("一致时不应报错: %v", err)
	}
	if err := VerifyClosingCash(st, y(1200)); err == nil {
		t.Error("不一致应报错")
	}
}

func TestBuildInvalidPeriod(t *testing.T) {
	if _, err := Build(Input{From: d0930, To: d0901}, nil); err == nil {
		t.Error("起止颠倒应报错")
	}
	if _, err := Build(Input{}, nil); err == nil {
		t.Error("空期间应报错")
	}
}

// ---------------------------------------------------------------------------
// 行定义
// ---------------------------------------------------------------------------

func TestLineDefsComplete(t *testing.T) {
	st, err := Build(Input{From: d0901, To: d0930}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 关键行次都应存在
	for _, no := range []int{
		lineSalesCash, lineOperatingInTotal, lineOperatingOutTotal, lineOperatingNet,
		lineInvestNet, lineFinanceNet, lineNetIncrease, lineOpeningCash, lineClosingCash,
	} {
		if _, ok := st.Line(no); !ok {
			t.Errorf("缺少行次 %d", no)
		}
	}
	// 期初余额应被填进去
	if got := st.AmountOf(lineOpeningCash); !got.IsZero() {
		t.Errorf("期初应为 0（未传入），得到 %s", got)
	}
}

func TestEmptyPeriodProducesZeroStatement(t *testing.T) {
	st, err := Build(Input{From: d0901, To: d0930, OpeningCash: y(5000)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.AmountOf(lineNetIncrease); !got.IsZero() {
		t.Errorf("无业务时净增加额应为 0，得到 %s", got)
	}
	if st.ClosingCash != y(5000) {
		t.Errorf("无业务时期末应等于期初，得到 %s", st.ClosingCash)
	}
	for _, err := range st.Check() {
		t.Errorf("勾稽关系应成立: %v", err)
	}
}

func TestSummary(t *testing.T) {
	from, to := period_()
	st, _ := Build(Input{From: from, To: to, CashEntries: sampleEntries()}, nil)
	s := st.Summary()
	if s == "" {
		t.Error("应有一句话概览")
	}
	if !contains(s, "2025-09-01") {
		t.Errorf("概览应含期间，得到 %q", s)
	}
}

func TestActivityLabel(t *testing.T) {
	cases := map[Activity]string{
		Operating: "经营活动", Investing: "投资活动", Financing: "筹资活动",
	}
	for a, want := range cases {
		if got := a.Label(); got != want {
			t.Errorf("%s.Label() = %q，期望 %q", a, got, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
