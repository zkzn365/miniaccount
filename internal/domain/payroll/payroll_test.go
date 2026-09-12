package payroll

import (
	"strings"
	"testing"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

func y(n int64) money.Money { return money.Money(n) * money.Yuan }

// ---------------------------------------------------------------------------
// 税率表
// ---------------------------------------------------------------------------

func TestDefaultTaxTableValid(t *testing.T) {
	tb := DefaultWageTaxTable()
	if err := tb.Validate(); err != nil {
		t.Fatalf("默认税率表应合法: %v", err)
	}
	if len(tb.Brackets) != 7 {
		t.Errorf("级距数 = %d，期望 7", len(tb.Brackets))
	}
	if tb.BasicDeduction != y(5000) {
		t.Errorf("减除费用 = %s，期望 5000.00", tb.BasicDeduction)
	}
	// 官方速算扣除数逐一核对
	want := []struct {
		upper money.Money
		rate  money.Rate
		quick money.Money
	}{
		{y(36000), money.RatePercent(3), 0},
		{y(144000), money.RatePercent(10), y(2520)},
		{y(300000), money.RatePercent(20), y(16920)},
		{y(420000), money.RatePercent(25), y(31920)},
		{y(660000), money.RatePercent(30), y(52920)},
		{y(960000), money.RatePercent(35), y(85920)},
		{0, money.RatePercent(45), y(181920)},
	}
	for i, w := range want {
		b := tb.Brackets[i]
		if b.UpperLimit != w.upper || b.Rate != w.rate || b.QuickDeduction != w.quick {
			t.Errorf("第 %d 级 = {上限 %s, 率 %s, 速算 %s}，期望 {上限 %s, 率 %s, 速算 %s}",
				i+1, b.UpperLimit, b.Rate, b.QuickDeduction, w.upper, w.rate, w.quick)
		}
	}
}

func TestTaxTableValidateRejectsBad(t *testing.T) {
	cases := map[string]TaxTable{
		"空表": {Name: "空"},
		"最后一级有上限": {Name: "X", Brackets: []TaxBracket{
			{UpperLimit: y(1000), Rate: money.RatePercent(3)},
		}},
		"级距不递增": {Name: "X", Brackets: []TaxBracket{
			{UpperLimit: y(1000), Rate: money.RatePercent(3)},
			{UpperLimit: y(500), Rate: money.RatePercent(10)},
			{UpperLimit: 0, Rate: money.RatePercent(20)},
		}},
		"无上限后又出现级距": {Name: "X", Brackets: []TaxBracket{
			{UpperLimit: 0, Rate: money.RatePercent(3)},
			{UpperLimit: y(1000), Rate: money.RatePercent(10)},
		}},
	}
	for name, tbl := range cases {
		if err := tbl.Validate(); err == nil {
			t.Errorf("%s 应判定为非法", name)
		}
	}
}

// 逐级核对：每一级的税额必须等于「所得额 × 税率 − 速算扣除数」
func TestTaxTableLookupAndTax(t *testing.T) {
	tb := DefaultWageTaxTable()
	cases := []struct {
		taxable money.Money
		want    money.Money
	}{
		{y(0), 0},
		{y(1000), y(30)},                       // 1000 × 3%
		{y(36000), y(1080)},                    // 36000 × 3%
		{y(36001), money.MustParse("1080.10")}, // 落入第 2 级：36001×10% − 2520
		{y(144000), y(11880)},                  // 144000×10% − 2520
		{y(300000), y(43080)},                  // 300000×20% − 16920
		{y(420000), y(73080)},                  // 420000×25% − 31920
		{y(660000), y(145080)},                 // 660000×30% − 52920
		{y(960000), y(250080)},                 // 960000×35% − 85920
		{y(1000000), y(268080)},                // 1000000×45% − 181920
	}
	for _, c := range cases {
		got := tb.Tax(c.taxable)
		if got != c.want {
			t.Errorf("应纳税所得额 %s 的税额 = %s，期望 %s", c.taxable, got, c.want)
		}
	}
	// 负数与零
	if tb.Tax(y(-100)) != 0 {
		t.Error("负所得额税额应为 0")
	}
}

// 级距的连续性：相邻级在边界处税额必须衔接（这正是速算扣除数的作用）
func TestTaxBracketContinuity(t *testing.T) {
	tb := DefaultWageTaxTable()
	for i := 0; i < len(tb.Brackets)-1; i++ {
		boundary := tb.Brackets[i].UpperLimit
		atBoundary := tb.Tax(boundary)
		justOver := tb.Tax(boundary.Add(money.Yuan)) // 多 1 元
		// 跨级时税额只应增加「1 元 × 新税率」左右，不应跳变
		delta := justOver.Sub(atBoundary)
		nextRate := tb.Brackets[i+1].Rate
		expected := nextRate.Apply(money.Yuan)
		diff := delta.Sub(expected)
		if diff.Abs() > money.Yuan {
			t.Errorf("第 %d 级边界 %s 处税额跳变：%s → %s（差 %s，期望约 %s）",
				i+1, boundary, atBoundary, justOver, delta, expected)
		}
	}
}

// ---------------------------------------------------------------------------
// ★ 累计预扣预缴法
// ---------------------------------------------------------------------------

// 场景一：月薪 10,000，无社保无专项附加，全年个税应逐月递增（跨级距）
func TestCumulativeWithholdingProgression(t *testing.T) {
	tb := DefaultWageTaxTable()
	salary := y(10000)

	var prior YTD
	var totalTax money.Money
	monthly := make([]money.Money, 0, 12)

	for m := 1; m <= 12; m++ {
		cur := YTD{
			Months:      prior.Months + 1,
			Income:      prior.Income.Add(salary),
			TaxWithheld: prior.TaxWithheld,
		}
		tax := CumulativeTax(cur, tb)
		monthly = append(monthly, tax)
		totalTax = totalTax.Add(tax)
		prior = cur
		prior.TaxWithheld = prior.TaxWithheld.Add(tax)
	}

	// 1~7 月：累计所得额 = (10000−5000)×m = 5000m，1~7 月累计 ≤ 36000 → 3%
	for m := 1; m <= 7; m++ {
		if monthly[m-1] != y(150) {
			t.Errorf("第 %d 个月个税 = %s，期望 150.00（累计未超 36000，3%%）",
				m, monthly[m-1])
		}
	}
	// 8 月：累计所得额 40000 落入第 2 级
	// 累计应纳税 = 40000×10% − 2520 = 1480；已预扣 7×150 = 1050 → 本月 430
	if monthly[7] != y(430) {
		t.Errorf("第 8 个月个税 = %s，期望 430.00", monthly[7])
	}
	// 12 月：累计所得额 60000
	// 累计应纳税 = 60000×10% − 2520 = 3480；全年合计应为 3480
	if totalTax != y(3480) {
		t.Errorf("全年个税合计 = %s，期望 3480.00", totalTax)
	}
	// 逐月递增（同一级内持平，跨级后跳升）
	for i := 1; i < len(monthly); i++ {
		if monthly[i] < monthly[i-1] {
			t.Errorf("个税不应逐月下降：第 %d 月 %s < 第 %d 月 %s",
				i+1, monthly[i], i, monthly[i-1])
		}
	}
	_ = salary
}

// ★ 按月算 vs 累计算的差别：这正是「不能用按月算」的原因
func TestCumulativeDiffersFromMonthlyNaive(t *testing.T) {
	tb := DefaultWageTaxTable()
	salary := y(10000)

	// 错误做法：每月都当成独立月份，按 5000 减除费用算
	naiveMonthly := tb.Tax(salary.Sub(tb.BasicDeduction)) // 5000 → 3% → 150
	naiveYear := naiveMonthly.MulInt(12)                  // 1800

	// 正确做法：累计预扣预缴
	var prior YTD
	var correctYear money.Money
	for m := 1; m <= 12; m++ {
		cur := YTD{Months: m, Income: prior.Income.Add(salary), TaxWithheld: prior.TaxWithheld}
		tax := CumulativeTax(cur, tb)
		correctYear = correctYear.Add(tax)
		prior = cur
		prior.TaxWithheld = prior.TaxWithheld.Add(tax)
	}

	if correctYear != y(3480) {
		t.Fatalf("累计法全年 = %s，期望 3480.00", correctYear)
	}
	if naiveYear == correctYear {
		t.Fatal("按月算与累计算应当不同")
	}
	// 按月算会少扣 1680 元 —— 年末汇算清缴时员工要一次补缴
	if correctYear.Sub(naiveYear) != y(1680) {
		t.Errorf("差额 = %s，期望 1680.00", correctYear.Sub(naiveYear))
	}
	t.Logf("按月算少扣 %s，年末需补缴", correctYear.Sub(naiveYear))
}

// 有社保与专项附加扣除时，累计所得额相应减少
func TestCumulativeWithDeductions(t *testing.T) {
	tb := DefaultWageTaxTable()
	// 月薪 20,000；个人社保公积金 3,000；专项附加扣除 2,000
	income, si, sa := y(20000), y(3000), y(2000)

	var prior YTD
	var tax1 money.Money
	for m := 1; m <= 1; m++ {
		cur := YTD{
			Months:            m,
			Income:            prior.Income.Add(income),
			SpecialDeduction:  prior.SpecialDeduction.Add(si),
			SpecialAdditional: prior.SpecialAdditional.Add(sa),
			TaxWithheld:       prior.TaxWithheld,
		}
		// 累计所得额 = 20000 − 5000 − 3000 − 2000 = 10000 → 3% → 300
		tax1 = CumulativeTax(cur, tb)
	}
	if tax1 != y(300) {
		t.Errorf("首月个税 = %s，期望 300.00", tax1)
	}
}

// ★ 累计法下本月税额可以为「负」（收入骤降），但预扣环节必须按 0 处理，
// 多缴部分留到次年汇算清缴退。
func TestCumulativeNeverNegative(t *testing.T) {
	tb := DefaultWageTaxTable()

	// 前 6 个月月薪 30,000，已预扣若干
	var prior YTD
	for m := 1; m <= 6; m++ {
		cur := YTD{Months: m, Income: prior.Income.Add(y(30000)),
			TaxWithheld: prior.TaxWithheld}
		tax := CumulativeTax(cur, tb)
		prior = cur
		prior.TaxWithheld = prior.TaxWithheld.Add(tax)
	}
	withheldBefore := prior.TaxWithheld
	if !withheldBefore.IsPositive() {
		t.Fatal("前置条件：前 6 个月应有预扣")
	}

	// 第 7 个月收入骤降为 0（如长期病假）
	cur := YTD{
		Months:      prior.Months + 1,
		Income:      prior.Income.Add(0),
		TaxWithheld: withheldBefore,
	}
	tax := CumulativeTax(cur, tb)
	if tax.IsNegative() {
		t.Fatalf("本月应预扣不应为负，得到 %s（预扣环节不退税）", tax)
	}
	if !tax.IsZero() {
		t.Errorf("收入为零时本月应预扣为 0，得到 %s", tax)
	}
}

// 中途入职：减除费用按实际任职月数计算，不能重复享受
func TestMidYearHireDeduction(t *testing.T) {
	tb := DefaultWageTaxTable()
	// 7 月入职，月薪 10,000
	// 第 1 个月（7 月）：Months=1，累计所得额 = 10000 − 5000×1 = 5000 → 3% → 150
	cur := YTD{Months: 1, Income: y(10000)}
	if got := CumulativeTax(cur, tb); got != y(150) {
		t.Errorf("中途入职首月个税 = %s，期望 150.00", got)
	}
	// 若错误地按 7 个月减除费用算，会得到 0 税
	wrong := YTD{Months: 7, Income: y(10000)}
	if got := CumulativeTax(wrong, tb); got != 0 {
		t.Errorf("按 7 个月减除费用应得 0，得到 %s", got)
	}
}

// 低收入者全年不缴税
func TestLowIncomeNoTax(t *testing.T) {
	tb := DefaultWageTaxTable()
	var prior YTD
	for m := 1; m <= 12; m++ {
		cur := YTD{Months: m, Income: prior.Income.Add(y(5000)), TaxWithheld: prior.TaxWithheld}
		tax := CumulativeTax(cur, tb)
		if !tax.IsZero() {
			t.Errorf("月薪 5000 第 %d 月不应缴税，得到 %s", m, tax)
		}
		prior = cur
	}
}

// ---------------------------------------------------------------------------
// 劳务报酬
// ---------------------------------------------------------------------------

func TestLaborRemunerationTaxable(t *testing.T) {
	cases := map[money.Money]money.Money{
		y(0):     0,
		y(800):   0,                          // 800 − 800
		y(1000):  y(200),                     // 1000 − 800
		y(4000):  y(3200),                    // 4000 − 800（临界点上仍按定额扣）
		y(4001):  money.MustParse("3200.80"), // 4001 × 80%
		y(5000):  y(4000),                    // 5000 × 80%
		y(10000): y(8000),
	}
	for in, want := range cases {
		if got := LaborRemunerationTaxable(in); got != want {
			t.Errorf("LaborRemunerationTaxable(%s) = %s，期望 %s", in, got, want)
		}
	}
}

func TestLaborRemunerationTax(t *testing.T) {
	cases := []struct {
		income money.Money
		want   money.Money
	}{
		{y(3000), 0},        // 应纳税所得额 2200 ≤ 20000 → 2200×20% = 440
		{y(10000), y(1600)}, // 8000 × 20%
		{y(20000), y(3200)}, // 16000 × 20%
		{y(30000), y(5200)}, // 24000 → 第 2 级：24000×30% − 2000 = 5200
		{y(50000), y(8800)}, // 40000×30% − 2000 = 10000？见下
	}
	// 逐一按公式手算核对
	for _, c := range cases {
		got := LaborRemunerationTax(c.income)
		// 手算参照
		taxable := LaborRemunerationTaxable(c.income)
		var want money.Money
		switch {
		case taxable <= 0:
			want = 0
		case taxable <= y(20000):
			want = money.RatePercent(20).Apply(taxable)
		case taxable <= y(50000):
			want = money.RatePercent(30).Apply(taxable).Sub(y(2000))
		default:
			want = money.RatePercent(40).Apply(taxable).Sub(y(7000))
		}
		if got != want {
			t.Errorf("劳务报酬 %s 的预扣 = %s，手算 %s", c.income, got, want)
		}
	}
	// 3000 元：应纳税所得额 2200，20% → 440
	if got := LaborRemunerationTax(y(3000)); got != y(440) {
		t.Errorf("3000 元劳务报酬预扣 = %s，期望 440.00", got)
	}
	// 10000 元：应纳税所得额 8000，20% → 1600
	if got := LaborRemunerationTax(y(10000)); got != y(1600) {
		t.Errorf("10000 元劳务报酬预扣 = %s，期望 1600.00", got)
	}
}

// 劳务报酬按次预扣，不累计 —— 同样的金额重复多次，每次税额相同
func TestLaborIsPerOccurrenceNotCumulative(t *testing.T) {
	first := LaborRemunerationTax(y(10000))
	second := LaborRemunerationTax(y(10000))
	if first != second {
		t.Errorf("劳务报酬按次计算，两次应相同：%s vs %s", first, second)
	}
}

// ---------------------------------------------------------------------------
// 社保公积金
// ---------------------------------------------------------------------------

func testScheme() *InsuranceScheme {
	return &InsuranceScheme{
		Name: "测试城市", City: "测试",
		BaseMin: y(4000), BaseMax: y(30000),
		HousingFundBaseMin: y(2000), HousingFundBaseMax: y(40000),
		Rates: InsuranceRates{
			PensionSelf: money.RatePercent(8), MedicalSelf: money.RatePercent(2),
			UnemploymentSelf: money.RatePermille(5), HousingFundSelf: money.RatePercent(12),
			PensionCo: money.RatePercent(14), MedicalCo: money.RatePercent(9),
			UnemploymentCo: money.RatePermille(5), InjuryCo: money.RatePermille(2),
			MaternityCo: money.RatePercent(1), HousingFundCo: money.RatePercent(12),
		},
	}
}

func TestCalcSocialInsurance(t *testing.T) {
	sc := testScheme()
	// 月薪 20,000，在基数区间内
	si := sc.Calc(y(20000))

	if si.Base != y(20000) {
		t.Errorf("缴费基数 = %s，期望 20000.00", si.Base)
	}
	if si.PensionSelf != y(1600) { // 20000 × 8%
		t.Errorf("养老（个人）= %s，期望 1600.00", si.PensionSelf)
	}
	if si.MedicalSelf != y(400) { // 20000 × 2%
		t.Errorf("医疗（个人）= %s，期望 400.00", si.MedicalSelf)
	}
	if si.UnemploymentSelf != y(100) { // 20000 × 0.5%
		t.Errorf("失业（个人）= %s，期望 100.00", si.UnemploymentSelf)
	}
	if si.HousingFundSelf != y(2400) { // 20000 × 12%
		t.Errorf("公积金（个人）= %s，期望 2400.00", si.HousingFundSelf)
	}
	if si.SelfTotal() != y(4500) {
		t.Errorf("个人合计 = %s，期望 4500.00", si.SelfTotal())
	}
	// 单位: 养老14% + 医疗9% + 失业0.5% + 工伤0.2% + 生育1% = 24.7%；公积金 12%
	if si.PensionCo != y(2800) {
		t.Errorf("养老（单位）= %s，期望 2800.00", si.PensionCo)
	}
	wantCo := y(20000*24.7/100 + 2400)
	if si.CompanyTotal() != wantCo {
		t.Errorf("单位合计 = %s，期望 %s", si.CompanyTotal(), wantCo)
	}
}

// ★ 缴费基数上下限：这是最容易算错的地方
func TestSocialInsuranceBaseLimits(t *testing.T) {
	sc := testScheme()

	// 工资低于下限 → 按下限缴
	low := sc.Calc(y(2000))
	if low.Base != y(4000) {
		t.Errorf("低于下限时基数 = %s，期望 4000.00", low.Base)
	}
	if low.PensionSelf != y(320) { // 4000 × 8%
		t.Errorf("按下限缴养老 = %s，期望 320.00", low.PensionSelf)
	}

	// 工资高于上限 → 按上限缴
	high := sc.Calc(y(60000))
	if high.Base != y(30000) {
		t.Errorf("高于上限时基数 = %s，期望 30000.00", high.Base)
	}
	if high.PensionSelf != y(2400) { // 30000 × 8%
		t.Errorf("按上限缴养老 = %s，期望 2400.00", high.PensionSelf)
	}

	// 公积金基数与社保基数上下限不同
	hf := sc.Calc(y(35000)) // 超过社保上限 30000，但未超公积金上限 40000
	if hf.Base != y(30000) {
		t.Errorf("社保基数 = %s，期望 30000.00", hf.Base)
	}
	if hf.HousingFundBase != y(35000) {
		t.Errorf("公积金基数 = %s，期望 35000.00（上限不同）", hf.HousingFundBase)
	}
}

func TestClampBase(t *testing.T) {
	// 不设限
	if got := ClampBase(y(100), 0, 0); got != y(100) {
		t.Errorf("不设限时应原样返回，得到 %s", got)
	}
	// 负数归零
	if got := ClampBase(y(-100), 0, 0); !got.IsZero() {
		t.Errorf("负数应归零，得到 %s", got)
	}
	if got := ClampBase(y(100), y(50), y(200)); got != y(100) {
		t.Errorf("区间内应原样，得到 %s", got)
	}
	if got := ClampBase(y(10), y(50), 0); got != y(50) {
		t.Errorf("低于下限应取下限，得到 %s", got)
	}
	if got := ClampBase(y(1000), 0, y(200)); got != y(200) {
		t.Errorf("高于上限应取上限，得到 %s", got)
	}
}

// ---------------------------------------------------------------------------
// 单员工计算
// ---------------------------------------------------------------------------

func emp(kind EmployeeKind, expenseAcc string) *Employee {
	return &Employee{
		ID: 1, Code: "E001", Name: "张三", Kind: kind,
		HireDate: calendar.MustParse("2020-01-01"), IsEnabled: true,
		ExpenseAccountCode: expenseAcc, SchemeName: "测试城市",
	}
}

func TestComputeItemEmployee(t *testing.T) {
	sc := testScheme()
	tb := DefaultWageTaxTable()
	e := emp(KindEmployee, "560201")

	it := &Item{
		EmployeeID: 1, Employee: e,
		BaseSalary: y(20000),
	}
	res := ComputeItem(it, YTD{}, tb, sc)

	if res.GrossPay != y(20000) {
		t.Errorf("应发合计 = %s", res.GrossPay)
	}
	// 社保个人合计 4500
	if res.Insurance.SelfTotal() != y(4500) {
		t.Errorf("社保个人 = %s，期望 4500.00", res.Insurance.SelfTotal())
	}
	// 累计所得额 = 20000 − 5000 − 4500 = 10500 → 3% → 315
	if res.IIT != y(315) {
		t.Errorf("个税 = %s，期望 315.00", res.IIT)
	}
	// 实发 = 20000 − 4500 − 315 = 15185
	if res.NetPay != y(15185) {
		t.Errorf("实发 = %s，期望 15185.00", res.NetPay)
	}
	// 校验算术自洽
	if err := it.Validate(); err != nil {
		t.Errorf("算术应自洽: %v", err)
	}
	if res.Note == "" {
		t.Error("应给出计算说明，便于用户核对")
	}
}

func TestComputeItemLabor(t *testing.T) {
	tb := DefaultWageTaxTable()
	e := emp(KindLabor, "560201")

	it := &Item{EmployeeID: 1, Employee: e, BaseSalary: y(10000)}
	// 外聘劳务不缴社保
	res := ComputeItem(it, YTD{}, tb, nil)

	if !res.Insurance.SelfTotal().IsZero() {
		t.Errorf("外聘劳务不应缴社保，得到 %s", res.Insurance.SelfTotal())
	}
	// 劳务报酬：应纳税所得额 8000，20% → 1600
	if res.IIT != y(1600) {
		t.Errorf("劳务报酬个税 = %s，期望 1600.00", res.IIT)
	}
	// 实发 = 10000 − 1600 = 8400
	if res.NetPay != y(8400) {
		t.Errorf("实发 = %s，期望 8400.00", res.NetPay)
	}
}

// 考勤扣款应在税前扣除，从而降低个税
func TestAttendanceDeductionReducesTax(t *testing.T) {
	tb := DefaultWageTaxTable()
	e := emp(KindEmployee, "560201")

	base := &Item{EmployeeID: 1, Employee: e, BaseSalary: y(10000)}
	withDeduct := &Item{EmployeeID: 1, Employee: e, BaseSalary: y(10000),
		AttendanceDeduction: y(2000)}

	r1 := ComputeItem(base, YTD{}, tb, nil)
	r2 := ComputeItem(withDeduct, YTD{}, tb, nil)

	if r2.GrossPay != y(10000) {
		t.Errorf("应发合计不受考勤扣款影响，得到 %s", r2.GrossPay)
	}
	if !(r2.IIT < r1.IIT) {
		t.Errorf("考勤扣款应降低个税：%s vs %s", r2.IIT, r1.IIT)
	}
	// 扣款 2000 后计税依据 8000，累计所得额 = 8000 − 5000 = 3000 → 90
	if r2.IIT != y(90) {
		t.Errorf("扣款后个税 = %s，期望 90.00", r2.IIT)
	}
	// 实发 = 10000 − 2000 − 90 = 7910
	if r2.NetPay != y(7910) {
		t.Errorf("实发 = %s，期望 7910.00", r2.NetPay)
	}
}

// 专项附加扣除可降低个税
func TestSpecialAdditionalReducesTax(t *testing.T) {
	tb := DefaultWageTaxTable()
	e := emp(KindEmployee, "560201")

	noSA := &Item{EmployeeID: 1, Employee: e, BaseSalary: y(10000)}
	withSA := &Item{EmployeeID: 1, Employee: e, BaseSalary: y(10000),
		SpecialAdditional: y(2000)}

	r1 := ComputeItem(noSA, YTD{}, tb, nil)
	r2 := ComputeItem(withSA, YTD{}, tb, nil)
	if r2.IIT >= r1.IIT {
		t.Errorf("专项附加扣除应降低个税：%s vs %s", r2.IIT, r1.IIT)
	}
	// 无扣：累计所得 5000 → 150；有 2000 扣：3000 → 90
	if r1.IIT != y(150) || r2.IIT != y(90) {
		t.Errorf("个税 = %s / %s，期望 150.00 / 90.00", r1.IIT, r2.IIT)
	}
}

// 手工覆盖社保后不应被方案重算
func TestInsuranceOverride(t *testing.T) {
	sc := testScheme()
	tb := DefaultWageTaxTable()
	e := emp(KindEmployee, "560201")

	manual := SocialInsurance{PensionSelf: y(100)}
	it := &Item{EmployeeID: 1, Employee: e, BaseSalary: y(20000),
		Insurance: manual, InsuranceOverridden: true}
	res := ComputeItem(it, YTD{}, tb, sc)
	if res.Insurance.PensionSelf != y(100) {
		t.Errorf("手工覆盖后不应重算，得到 %s", res.Insurance.PensionSelf)
	}
}

// ---------------------------------------------------------------------------
// 工资单
// ---------------------------------------------------------------------------

func testRun() (*Run, *VoucherAccounts) {
	dept := int64(1)
	e1 := &Employee{ID: 1, Code: "E001", Name: "张三", Kind: KindEmployee,
		DeptID: &dept, ExpenseAccountCode: "560201", CompanyAccountCode: "560202"}
	e2 := &Employee{ID: 2, Code: "E002", Name: "李四", Kind: KindEmployee,
		DeptID: &dept, ExpenseAccountCode: "560201"}
	e3 := &Employee{ID: 3, Code: "E003", Name: "王五", Kind: KindLabor,
		ExpenseAccountCode: "560213"}

	sc := testScheme()
	tb := DefaultWageTaxTable()

	items := []*Item{
		{EmployeeID: 1, Employee: e1, BaseSalary: y(20000),
			SpecialAdditional: y(2000), InsuranceOverridden: true,
			Insurance: SocialInsurance{PensionSelf: y(1600), MedicalSelf: y(400),
				UnemploymentSelf: y(100), HousingFundSelf: y(2400),
				PensionCo: y(2800), MedicalCo: y(1800), UnemploymentCo: y(100),
				InjuryCo: y(40), MaternityCo: y(200), HousingFundCo: y(2400)}},
		{EmployeeID: 2, Employee: e2, BaseSalary: y(10000),
			InsuranceOverridden: true,
			Insurance:           SocialInsurance{}},
		{EmployeeID: 3, Employee: e3, BaseSalary: y(10000)},
	}
	for _, it := range items {
		var scheme *InsuranceScheme
		if it.Employee.Kind == KindEmployee {
			scheme = sc
		}
		ComputeItem(it, YTD{}, tb, scheme)
	}
	run := &Run{Period: PeriodKey{Year: 2025, Month: 9}, TaxTable: tb, Items: items,
		Status: RunDraft}
	vc := DefaultVoucherAccounts()
	return run, &vc
}

func TestRunTotals(t *testing.T) {
	run, _ := testRun()
	if err := run.Validate(); err != nil {
		t.Fatalf("工资单应自洽: %v", err)
	}
	tot := run.Totals()
	if tot.Headcount != 3 {
		t.Errorf("人数 = %d，期望 3", tot.Headcount)
	}
	// 应发合计 = 20000 + 10000 + 10000
	if tot.Gross != y(40000) {
		t.Errorf("应发合计 = %s，期望 40000.00", tot.Gross)
	}
	// 合计必须自洽：应发 = 个人社保 + 个税 + 实发（无考勤扣款时）
	if tot.Gross != tot.InsuranceSelf.Add(tot.IIT).Add(tot.Net) {
		t.Errorf("应发 %s ≠ 社保 %s + 个税 %s + 实发 %s",
			tot.Gross, tot.InsuranceSelf, tot.IIT, tot.Net)
	}
}

// ★ 工资单必须自洽：实发 = 应发 − 扣款 − 个人社保 − 个税
func TestRunValidateDetectsInconsistency(t *testing.T) {
	run, _ := testRun()
	// 人为破坏一行的实发
	run.Items[0].NetPay = run.Items[0].NetPay.Add(y(100))
	if err := run.Validate(); err == nil {
		t.Fatal("实发与公式不符时应报错")
	}
}

func TestRunRejectsNegative(t *testing.T) {
	run, _ := testRun()
	run.Items[0].BaseSalary = y(-100)
	if err := run.Validate(); err == nil {
		t.Fatal("负工资应报错")
	}
}

func TestRunEmpty(t *testing.T) {
	run := &Run{TaxTable: DefaultWageTaxTable()}
	if err := run.Validate(); err == nil {
		t.Fatal("空工资单应报错")
	}
}

// ---------------------------------------------------------------------------
// 凭证生成
// ---------------------------------------------------------------------------

func TestBuildAccrualEntries(t *testing.T) {
	run, vc := testRun()
	entries, err := run.BuildAccrualEntries(*vc)
	if err != nil {
		t.Fatalf("生成计提分录失败: %v", err)
	}
	// 借贷必须平衡
	var d, c money.Money
	for _, e := range entries {
		d = d.Add(e.Debit)
		c = c.Add(e.Credit)
	}
	if d != c {
		t.Fatalf("计提凭证借贷不平：借 %s 贷 %s", d, c)
	}
	// 贷方应有「应付职工薪酬—工资」= 应发合计
	var found bool
	for _, e := range entries {
		if e.AccountCode == vc.PayableSalary && e.Credit == y(40000) {
			found = true
		}
	}
	if !found {
		t.Errorf("缺少应付职工薪酬—工资 贷方 %s：%+v", y(40000), entries)
	}
	// 借方应按员工列示，便于按部门归集成本
	var debitLines int
	for _, e := range entries {
		if e.Debit.IsPositive() {
			debitLines++
		}
	}
	if debitLines < 3 {
		t.Errorf("借方应至少按 3 名员工分别列示，实际 %d 行", debitLines)
	}
}

func TestBuildPaymentEntries(t *testing.T) {
	run, vc := testRun()
	entries, err := run.BuildPaymentEntries(*vc)
	if err != nil {
		t.Fatalf("生成发放分录失败: %v", err)
	}
	var d, c money.Money
	for _, e := range entries {
		d = d.Add(e.Debit)
		c = c.Add(e.Credit)
	}
	if d != c {
		t.Fatalf("发放凭证借贷不平：借 %s 贷 %s", d, c)
	}
	// 借方应为应付职工薪酬—工资 = 应发合计
	if entries[0].AccountCode != vc.PayableSalary || entries[0].Debit != y(40000) {
		t.Errorf("第 1 条 = %+v", entries[0])
	}
	// 应包含代扣个税
	tot := run.Totals()
	var iitLine *Entry
	for i := range entries {
		if entries[i].AccountCode == vc.PayableIIT {
			iitLine = &entries[i]
		}
	}
	if iitLine == nil {
		t.Fatal("缺少代扣个税分录")
	}
	if iitLine.Credit != tot.IIT {
		t.Errorf("代扣个税 = %s，期望 %s", iitLine.Credit, tot.IIT)
	}
}

// 两张凭证的应付职工薪酬—工资应当一致（计提多少、发放多少）
func TestAccrualAndPaymentMatch(t *testing.T) {
	run, vc := testRun()
	acc, err := run.BuildAccrualEntries(*vc)
	if err != nil {
		t.Fatal(err)
	}
	pay, err := run.BuildPaymentEntries(*vc)
	if err != nil {
		t.Fatal(err)
	}
	var accrualPayable, paymentPayable money.Money
	for _, e := range acc {
		if e.AccountCode == vc.PayableSalary {
			accrualPayable = accrualPayable.Add(e.Credit)
		}
	}
	for _, e := range pay {
		if e.AccountCode == vc.PayableSalary {
			paymentPayable = paymentPayable.Add(e.Debit)
		}
	}
	if accrualPayable != paymentPayable {
		t.Errorf("计提应付 %s ≠ 发放应付 %s", accrualPayable, paymentPayable)
	}
}

// 无社保无个税时不应产生对应分录（避免出现金额为零的行）
func TestBuildEntriesSkipsZeroLines(t *testing.T) {
	e := &Employee{ID: 1, Code: "E001", Name: "低收入", Kind: KindEmployee,
		ExpenseAccountCode: "560201"}
	it := &Item{EmployeeID: 1, Employee: e, BaseSalary: y(3000)}
	ComputeItem(it, YTD{}, DefaultWageTaxTable(), nil)
	run := &Run{Period: PeriodKey{2025, 9}, TaxTable: DefaultWageTaxTable(),
		Items: []*Item{it}}
	vc := DefaultVoucherAccounts()

	pay, err := run.BuildPaymentEntries(vc)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range pay {
		if en.AccountCode == vc.PayableIIT {
			t.Error("无个税时不应出现应交个人所得税分录")
		}
		if en.AccountCode == vc.InsurancePersonal {
			t.Error("无社保时不应出现社保分录")
		}
	}
	var d, c money.Money
	for _, en := range pay {
		d, c = d.Add(en.Debit), c.Add(en.Credit)
	}
	if d != c {
		t.Errorf("借贷不平：%s vs %s", d, c)
	}
}

// ---------------------------------------------------------------------------
// 在职判断
// ---------------------------------------------------------------------------

func TestIsActiveIn(t *testing.T) {
	e := &Employee{IsEnabled: true,
		HireDate:  calendar.MustParse("2025-03-15"),
		LeaveDate: calendar.MustParse("2025-09-20")}

	// 在职期间
	if !e.IsActiveIn(PeriodKey{2025, 5}) {
		t.Error("5 月应在职")
	}
	// 入职当月（月中入职，当月仍算在职）
	if !e.IsActiveIn(PeriodKey{2025, 3}) {
		t.Error("入职当月应在职")
	}
	// 离职当月
	if !e.IsActiveIn(PeriodKey{2025, 9}) {
		t.Error("离职当月应在职")
	}
	// 入职前
	if e.IsActiveIn(PeriodKey{2025, 2}) {
		t.Error("入职前不应在职")
	}
	// 离职后
	if e.IsActiveIn(PeriodKey{2025, 10}) {
		t.Error("离职后不应在职")
	}
	// 停用
	e.IsEnabled = false
	if e.IsActiveIn(PeriodKey{2025, 5}) {
		t.Error("停用的员工不应在职")
	}
}

func TestEmployeeValidate(t *testing.T) {
	ok := &Employee{Name: "张三", Kind: KindEmployee, ExpenseAccountCode: "560201"}
	if err := ok.Validate(); err != nil {
		t.Errorf("合法员工不应报错: %v", err)
	}
	bad := []*Employee{
		{Name: "", Kind: KindEmployee, ExpenseAccountCode: "560201"},
		{Name: "张三", Kind: "bogus", ExpenseAccountCode: "560201"},
		{Name: "张三", Kind: KindEmployee, ExpenseAccountCode: ""},
	}
	for i, e := range bad {
		if err := e.Validate(); err == nil {
			t.Errorf("第 %d 个应报错", i+1)
		}
	}
}

// ★ 社保必须按**员工档案里申报的缴费基数**算，不能按当月工资额硬算。
//
// 小微企业普遍按最低基数缴纳，而最低基数与实发工资无关。
// 修之前 Calc 无视 SIBase：月薪 2 万、按 4000 基数缴的员工，
// 个人社保被算成工资额的比例（4500 而不是 900），
// 一个月多扣 3600 元；个税的专项扣除跟着虚增，实发、个税、社保三处全错。
func TestInsuranceUsesDeclaredBaseNotGrossPay(t *testing.T) {
	sc := &InsuranceScheme{
		Name: "当地标准",
		Rates: InsuranceRates{
			PensionSelf:      100_000, // 10%
			MedicalSelf:      20_000,  // 2%
			UnemploymentSelf: 5_000,   // 0.5%
			HousingFundSelf:  120_000, // 12%
		},
	}
	gross := money.Money(20_000_00) // 月薪 2 万

	// 未申报基数 → 按当月工资总额（法定口径）
	got := sc.CalcWithDeclaredBase(0, 0, gross)
	if got.Base != gross {
		t.Errorf("未申报时基数 = %s，期望按工资总额 %s", got.Base, gross)
	}
	// 养老 10% = 2000
	if got.PensionSelf != money.Money(2_000_00) {
		t.Errorf("未申报时养老 = %s，期望 2000.00", got.PensionSelf)
	}

	// 申报基数 4000 → 一律按 4000 算
	declared := money.Money(4_000_00)
	got = sc.CalcWithDeclaredBase(declared, 0, gross)
	if got.Base != declared {
		t.Errorf("申报基数 = %s，实际用了 %s", declared, got.Base)
	}
	if got.PensionSelf != money.Money(400_00) {
		t.Errorf("申报基数下养老 = %s，期望 400.00（基数 4000 乘 10%%）", got.PensionSelf)
	}
	// 公积金未单独申报 → 跟随社保基数
	if got.HousingFundBase != declared {
		t.Errorf("公积金基数 = %s，期望跟随社保基数 %s", got.HousingFundBase, declared)
	}
	if got.HousingFundSelf != money.Money(480_00) {
		t.Errorf("公积金个人 = %s，期望 480.00（基数 4000 乘 12%%）", got.HousingFundSelf)
	}

	// 公积金单独申报 6000 → 与社保基数分开
	got = sc.CalcWithDeclaredBase(declared, money.Money(6_000_00), gross)
	if got.Base != declared || got.HousingFundBase != money.Money(6_000_00) {
		t.Errorf("社保/公积金基数 = %s / %s，期望 4000 / 6000",
			got.Base, got.HousingFundBase)
	}
}

// 上下限仍然生效：申报基数也要被夹。
func TestInsuranceDeclaredBaseStillClamped(t *testing.T) {
	sc := &InsuranceScheme{
		BaseMin: money.Money(4_000_00),
		BaseMax: money.Money(30_000_00),
		Rates:   InsuranceRates{PensionSelf: 100_000},
	}
	gross := money.Money(20_000_00)

	// 申报 1000 低于下限 → 夹到 4000
	got := sc.CalcWithDeclaredBase(money.Money(1_000_00), 0, gross)
	if got.Base != money.Money(4_000_00) {
		t.Errorf("低于下限的申报基数 = %s，期望夹到 4000.00", got.Base)
	}
	// 申报 50000 高于上限 → 夹到 30000
	got = sc.CalcWithDeclaredBase(money.Money(50_000_00), 0, gross)
	if got.Base != money.Money(30_000_00) {
		t.Errorf("高于上限的申报基数 = %s，期望夹到 30000.00", got.Base)
	}
}

// Calc(gross) 保持旧语义（等价于未申报基数）—— 已有调用方与测试不受影响。
func TestCalcStillMeansGrossPay(t *testing.T) {
	sc := &InsuranceScheme{Rates: InsuranceRates{PensionSelf: 100_000}}
	gross := money.Money(8_000_00)
	if a, b := sc.Calc(gross), sc.CalcWithDeclaredBase(0, 0, gross); a != b {
		t.Errorf("Calc 与 CalcWithDeclaredBase(0,0,·) 应等价：%+v vs %+v", a, b)
	}
}

// ★ 减除费用的乘数是「任职月份数」，不是「已有工资单张数」。
//
// 少算的每个月 = 少 5000 元扣除 = 多扣个税，而且不会报任何错。
func TestEmployedMonths(t *testing.T) {
	d := func(s string) calendar.Date { return calendar.MustParse(s) }
	cases := []struct {
		name  string
		hire  string
		year  int
		month int
		want  int
	}{
		{"去年入职，全年都算", "2024-03-01", 2025, 1, 1},
		{"去年入职，到 12 月", "2024-03-01", 2025, 12, 12},
		{"去年入职，到 7 月", "2024-03-01", 2025, 7, 7},
		{"本年 3 月入职，3 月", "2025-03-15", 2025, 3, 1},
		{"本年 3 月入职，到 12 月", "2025-03-15", 2025, 12, 10},
		{"本年 1 月入职，到 3 月", "2025-01-05", 2025, 3, 3},
		{"还没入职（入职年份晚于计税年度）", "2026-01-01", 2025, 6, 0},
		{"12 月入职，当年 12 月", "2025-12-01", 2025, 12, 1},
	}
	for _, c := range cases {
		if got := EmployedMonths(d(c.hire), c.year, c.month); got != c.want {
			t.Errorf("%s：EmployedMonths(%s, %d, %d) = %d，期望 %d",
				c.name, c.hire, c.year, c.month, got, c.want)
		}
	}

	// 入职日期缺失 → 0，调用方会退回「工资单张数」口径
	if got := EmployedMonths(calendar.Date{}, 2025, 6); got != 0 {
		t.Errorf("入职日期缺失应返回 0，实际 %d", got)
	}
}

// ★ 端到端：年中启用软件不该多扣个税。
//
// 场景：员工 2024 年就在职，2025 年 8 月才开始用本软件建单。
// 到 12 月时任职 12 个月，累计减除费用应是 12×5000=60000。
// 按「工资单张数」（5 张）算只有 25000 —— 全年多扣 3500 元。
func TestMidYearAdoptionDoesNotOverWithhold(t *testing.T) {
	table := DefaultWageTaxTable()
	// 12 月：应发 20000，累计收入 20000（只有 12 月一张单）
	// 正确：累计减除 12×5000=60000 → 应纳税所得额 0 → 本月个税 0
	prior := YTD{}
	cur := YTD{
		Months: EmployedMonths(calendar.MustParse("2024-03-01"), 2025, 12),
		Income: money.Money(20_000_00),
	}
	if cur.Months != 12 {
		t.Fatalf("任职月数 = %d，期望 12", cur.Months)
	}
	if got := CumulativeTax(cur, table); got != 0 {
		t.Errorf("12 月个税 = %s，期望 0（累计减除 60000 覆盖了 20000 收入）", got)
	}
	_ = prior

	// 对照：按「工资单张数」口径（5 张）会算出 5000×5=25000 的减除，
	// 收入 20000 仍低于 25000，个税同样是 0；收入 100000 时才见分晓
	wrong := YTD{Months: 5, Income: money.Money(100_000_00)}
	right := YTD{Months: 12, Income: money.Money(100_000_00)}
	wrongTax := CumulativeTax(wrong, table)
	rightTax := CumulativeTax(right, table)
	if wrongTax <= rightTax {
		t.Errorf("口径错误时应多扣税：错 %s vs 对 %s", wrongTax, rightTax)
	}
	// 确切数字：错的口径 100000−25000=75000 → 75000×10%−2520=4980
	if want := money.Money(4_980_00); wrongTax != want {
		t.Errorf("错口径累计税 = %s，期望 %s", wrongTax, want)
	}
	// 正确口径 100000−60000=40000 → 40000×10%−2520=1480
	if want := money.Money(1_480_00); rightTax != want {
		t.Errorf("正确口径累计税 = %s，期望 %s", rightTax, want)
	}
}

// ★ 任职月份数多于工资单张数时必须出声。
//
// 减除费用按任职月份算（法定口径），但累计收入只有实际建过单的月份。
// 两者不匹配时个税会**少扣**，而且不会报任何错 ——
// 年度汇算时员工要补税、单位要更正申报。
func TestWarnsWhenHistoryMonthsAreMissing(t *testing.T) {
	table := DefaultWageTaxTable()
	it := &Item{BaseSalary: money.Money(10_000_00)}

	// 任职 9 个月，却只有 1 张工资单 → 应当告警
	// （告警挂在 Item 上，因为 Item 才是被持久化与展示的那一份）
	ComputeItem(it, YTD{Months: 8, Slips: 0}, table, nil)
	if it.TaxWarning == "" {
		t.Fatal("任职 9 个月却只有 1 张工资单，应当给出告警")
	}
	if !strings.Contains(it.TaxWarning, "9") || !strings.Contains(it.TaxWarning, "少扣") {
		t.Errorf("告警内容应说明月数与「少扣」，实际 %q", it.TaxWarning)
	}

	// 任职月数与工资单张数一致 → 不该告警
	ok := &Item{BaseSalary: money.Money(10_000_00)}
	ComputeItem(ok, YTD{Months: 8, Slips: 8}, table, nil)
	if ok.TaxWarning != "" {
		t.Errorf("月数与张数一致时不该告警，实际 %q", ok.TaxWarning)
	}
}
