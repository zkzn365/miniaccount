package taxreturn_test

import (
	"strings"
	"testing"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/payroll"
	"miniaccount/internal/domain/taxreturn"
)

// 元 → 分
func y(n int64) money.Money { return money.Money(n) * money.Yuan }

// ---------------------------------------------------------------------------
// 增值税
// ---------------------------------------------------------------------------

func vatBase() taxreturn.VATInput {
	return taxreturn.VATInput{
		Period: "2025-03",
		// 销项 113,000（含税 113 万 × 13% 的简化数）、进项 60,000
		OutputTax: y(113_000), InputTax: y(60_000),
		Surcharges: taxreturn.DefaultSurchargeRates(),
	}
}

func TestVATBasicComputation(t *testing.T) {
	r, err := taxreturn.VAT(vatBase())
	if err != nil {
		t.Fatalf("算增值税失败: %v", err)
	}
	// 应纳税额 = 销项 113,000 − 实际抵扣 60,000 = 53,000
	if got := keyOf(t, r, "本期应纳税额（增值税）"); got != y(53_000) {
		t.Errorf("应纳税额 = %s，期望 53,000.00", got)
	}
	// 附加税费 = 53,000 × 12% = 6,360
	if got := keyOf(t, r, "附加税费（合计 12%）"); got != y(6_360) {
		t.Errorf("附加税费 = %s，期望 6,360.00（53,000 × 12%%）", got)
	}
	// 应补 = 53,000 + 6,360 = 59,360
	if got := keyOf(t, r, "本期应补(退)税额"); got != y(59_360) {
		t.Errorf("应补税额 = %s，期望 59,360.00", got)
	}
	if r.Submittable {
		t.Error("★ 计算表永远不能被标记为可直接申报")
	}
	if !strings.Contains(r.PolicyNote, "不替代申报表") {
		t.Errorf("口径说明里要写清它不替代申报表：%q", r.PolicyNote)
	}
	// ★ 每一行都要说清「这个数从哪来」—— 这是这张表的价值所在
	for _, row := range r.Rows {
		if strings.TrimSpace(row.Label) == "" {
			t.Error("有一行没有项目名称")
		}
		if row.Line != "合计" && strings.TrimSpace(row.Source) == "" {
			t.Errorf("第 %s 行「%s」没有写数据来源 —— 会计没法核对", row.Line, row.Label)
		}
	}
}

// 上期留抵：可抵进项大于销项时，剩下的结转到下期，本期不交税。
func TestVATCreditCarryForward(t *testing.T) {
	in := vatBase()
	in.InputTax = y(200_000)
	in.PriorCredit = y(50_000)
	r, err := taxreturn.VAT(in)
	if err != nil {
		t.Fatalf("算增值税失败: %v", err)
	}
	if got := keyOf(t, r, "本期应纳税额（增值税）"); !got.IsZero() {
		t.Errorf("可抵 250,000 > 销项 113,000，应纳税额应当是 0，实际 %s", got)
	}
	// 留抵 = 250,000 − 113,000 = 137,000
	if got := keyOf(t, r, "期末留抵税额（结转下期）"); got != y(137_000) {
		t.Errorf("期末留抵 = %s，期望 137,000.00", got)
	}
	if !warnContains(r, "留抵") {
		t.Error("有留抵时必须报出来 —— 否则用户看不懂为什么应纳是 0")
	}
}

// 进项税额转出减少可抵进项。
func TestVATTransferOutReducesInput(t *testing.T) {
	in := vatBase()
	in.InputTaxTransferredOut = y(10_000)
	r, _ := taxreturn.VAT(in)
	// 实际抵扣 = 60,000 − 10,000 = 50,000 → 应纳税额 = 113,000 − 50,000 = 63,000
	if got := keyOf(t, r, "本期应纳税额（增值税）"); got != y(63_000) {
		t.Errorf("应纳税额 = %s，期望 63,000.00（转出 10,000 后）", got)
	}
}

// 销项税额抵减（差额征税）减少销项。
func TestVATOutputDeduction(t *testing.T) {
	in := vatBase()
	in.OutputTaxDeducted = y(13_000)
	r, _ := taxreturn.VAT(in)
	// 销项合计 = 113,000 − 13,000 = 100,000 → 应纳 = 100,000 − 60,000 = 40,000
	if got := keyOf(t, r, "本期应纳税额（增值税）"); got != y(40_000) {
		t.Errorf("应纳税额 = %s，期望 40,000.00", got)
	}
}

// 减免税款与出口抵减内销都减少应纳税额，但不产生退税。
func TestVATReliefDoesNotProduceRefund(t *testing.T) {
	in := vatBase()
	in.TaxRelief = y(100_000) // 比应纳税额 53,000 还大
	r, err := taxreturn.VAT(in)
	if err != nil {
		t.Fatalf("算增值税失败: %v", err)
	}
	if got := keyOf(t, r, "本期应纳税额（增值税）"); !got.IsZero() {
		t.Errorf("★ 减免大于应纳税额时应当是 0（不产生负数应交），实际 %s", got)
	}
	if !warnContains(r, "留抵") && !warnContains(r, "减免") {
		t.Logf("warnings: %v", r.Warnings)
	}
}

func TestVATSmallScaleWarnsAboutInputTax(t *testing.T) {
	in := vatBase()
	in.SmallScale = true
	in.Surcharges.Halved = true
	r, _ := taxreturn.VAT(in)
	if !warnContains(r, "小规模") {
		t.Error("★ 小规模纳税人却有进项税额，必须报出来（进项应计入成本，不能抵扣）")
	}
	// 减半：12% → 6% → 53,000 × 6% = 3,180
	if got := keyOf(t, r, "附加税费（合计 6%）"); got != y(3_180) {
		t.Errorf("减半后的附加税费 = %s，期望 3,180.00", got)
	}
}

func TestVATPaidMoreThanPayable(t *testing.T) {
	in := vatBase()
	in.Paid = y(60_000) // 已交 > 应纳+附加 59,360
	r, _ := taxreturn.VAT(in)
	if got := keyOf(t, r, "本期应补(退)税额"); !got.IsNegative() {
		t.Errorf("多缴时应当是负数（留抵下期），实际 %s", got)
	}
	if !warnContains(r, "多缴") {
		t.Error("多缴必须报出来")
	}
}

func TestVATRejectsAbsurdSurchargeRates(t *testing.T) {
	in := vatBase()
	in.Surcharges = taxreturn.SurchargeRates{UrbanConstructionPPM: 900_000}
	if _, err := taxreturn.VAT(in); err == nil {
		t.Error("附加税费比例不可能到 90%，应当被拦下")
	}
}

// ---------------------------------------------------------------------------
// 企业所得税
// ---------------------------------------------------------------------------

func citBase() taxreturn.CITInput {
	return taxreturn.CITInput{
		Period: "2025 年第 1 季度",
		Revenue: y(1_000_000), Cost: y(600_000), Profit: y(200_000),
	}
}

func TestCITBasicComputation(t *testing.T) {
	r, err := taxreturn.CIT(citBase())
	if err != nil {
		t.Fatalf("算企业所得税失败: %v", err)
	}
	if got := keyOf(t, r, "应纳税所得额"); got != y(200_000) {
		t.Errorf("应纳税所得额 = %s，期望 200,000.00", got)
	}
	// 一般企业 25% → 50,000
	if got := keyOf(t, r, "应纳所得税额"); got != y(50_000) {
		t.Errorf("应纳所得税额 = %s，期望 50,000.00", got)
	}
	if !warnContains(r, "纳税调整") {
		t.Error("★ 纳税调整额为 0 时要提醒 —— 业务招待费、罚款滞纳金等限额项不调整会少缴税")
	}
}

func TestCITSmallLowProfit(t *testing.T) {
	in := citBase()
	in.Profit = y(1_000_000) // 应纳税所得额 100 万，未超 300 万
	in.SmallLowProfit = true
	r, _ := taxreturn.CIT(in)
	// 100 万 × 5% = 5 万
	if got := keyOf(t, r, "应纳所得税额"); got != y(50_000) {
		t.Errorf("小型微利企业应纳所得税额 = %s，期望 50,000.00（5%% 实际税负）", got)
	}
}

// ★ 300 万元是小型微利企业的**资格门槛**，不是分段线。
//
// 现行政策：年应纳税所得额不超过 300 万元才**算**小型微利企业；
// 一旦超过，当年即不符合条件，全额按法定税率 25% 计算。
//
// 发布前审计抓到过这里：原来按「分段」（300 万内 5% + 超出部分 25%）
// 算出 650,000，而正确的全额是 **1,250,000** —— 少算 60 万，
// 是会让企业被追缴的大错。
func TestCITSmallLowProfitCrossingCap(t *testing.T) {
	in := citBase()
	in.Profit = y(5_000_000) // 500 万
	in.SmallLowProfit = true
	r, _ := taxreturn.CIT(in)
	if got := keyOf(t, r, "应纳所得税额"); got != y(1_250_000) {
		t.Errorf("超过小微上限时应纳所得税额 = %s，期望 1,250,000.00（全额 25%%）", got)
	}
	if !warnContains(r, "不符合") {
		t.Errorf("★ 跨越上限必须说清「超过即不符合小微条件」，实际警告：%v", r.Warnings)
	}
	// 上限之内仍然是 5%
	in.Profit = y(2_000_000)
	r, _ = taxreturn.CIT(in)
	if got := keyOf(t, r, "应纳所得税额"); got != y(100_000) {
		t.Errorf("上限之内应纳所得税额 = %s，期望 100,000.00（5%%）", got)
	}
}

// ★ 亏损季度**不产生**「负的应纳所得税额」。
//
// 发布前审计实测：只有费用、没有收入时，原实现把 −100,000 直接乘税率，
// 表上出现「应纳所得税额 −25,000.00」「本期多缴 25,000.00」——
// 把亏损说成了多缴，用户可能据此去申请退税。
func TestCITLossProducesNoNegativeTax(t *testing.T) {
	in := citBase()
	in.Profit = y(-100_000)
	r, _ := taxreturn.CIT(in)
	if got := keyOf(t, r, "应纳所得税额"); !got.IsZero() {
		t.Errorf("亏损时应纳所得税额应为 0，实际 %s", got)
	}
	if got := keyOf(t, r, "本期应补(退)所得税额"); got.IsNegative() {
		t.Errorf("★ 亏损时不应出现「多缴」的负数，实际 %s", got)
	}
	if !warnContains(r, "亏损") {
		t.Error("亏损要说明可结转以后年度弥补")
	}
}

func TestCITLossAndPrepaid(t *testing.T) {
	in := citBase()
	in.Profit = y(-100_000)
	r, _ := taxreturn.CIT(in)
	if got := keyOf(t, r, "应纳所得税额"); !got.IsZero() && !got.IsNegative() {
		t.Errorf("亏损时应纳所得税额应当是 0 或负（不产生应交），实际 %s", got)
	}
	if !warnContains(r, "亏损") {
		t.Error("亏损要说明可结转以后年度弥补")
	}

	in = citBase()
	in.Prepaid = y(80_000) // 已预缴超过应纳 50,000
	r, _ = taxreturn.CIT(in)
	if got := keyOf(t, r, "本期应补(退)所得税额"); got != y(-30_000) {
		t.Errorf("多缴时应当是负数，实际 %s", got)
	}
	if !warnContains(r, "多缴") {
		t.Error("多缴必须报出来")
	}
}

// ---------------------------------------------------------------------------
// 个人所得税
// ---------------------------------------------------------------------------

func iitEmp(name string, months int, income, special, additional, withheld money.Money) taxreturn.IITEmployee {
	return taxreturn.IITEmployee{
		Name: name, Months: months, Income: income,
		SpecialDeduction: special, SpecialAdditional: additional,
		TaxWithheld: withheld,
	}
}

func TestIITBasicComputation(t *testing.T) {
	// 张三：任职 2 个月，累计收入 20,000，专项扣除（社保）2,000，
	// 专项附加扣除 2,000，累计已预扣 0
	in := taxreturn.IITInput{
		Period: "2025-02", Table: payroll.DefaultWageTaxTable(),
		Employees: []taxreturn.IITEmployee{
			iitEmp("张三", 2, y(20_000), y(2_000), y(2_000), 0),
		},
	}
	r, err := taxreturn.IIT(in)
	if err != nil {
		t.Fatalf("算个税失败: %v", err)
	}
	// 应纳税所得额 = 20,000 − 5,000×2 − 2,000 − 2,000 = 6,000
	// 税率 3% → 180
	if got := keyOf(t, r, "本期应预扣预缴税额合计"); got != y(180) {
		t.Errorf("本期应预扣 = %s，期望 180.00", got)
	}
	if !strings.Contains(r.Rows[0].Source, "任职 2 个月") {
		t.Errorf("行里要写清是哪个累计口径：%q", r.Rows[0].Source)
	}
}

// ★ 累计已预扣超过累计应纳税额：本期不再预扣，而且**不是负数**。
//
// 预扣预缴环节不退税；显示成负数会让用户以为这个月要退钱。
func TestIITOverWithheldIsZeroNotNegative(t *testing.T) {
	in := taxreturn.IITInput{
		Period: "2025-06", Table: payroll.DefaultWageTaxTable(),
		Employees: []taxreturn.IITEmployee{
			iitEmp("李四", 6, y(30_000), 0, 0, y(5_000)),
		},
	}
	r, _ := taxreturn.IIT(in)
	// 累计应纳税所得额 = 30,000 − 30,000 = 0 → 累计应纳税额 0；已预扣 5,000
	if got := keyOf(t, r, "本期应预扣预缴税额合计"); !got.IsZero() {
		t.Errorf("★ 多扣时本期应预扣必须是 0（预扣环节不退税），实际 %s", got)
	}
	if !warnContains(r, "超过累计应纳税额") {
		t.Error("多扣必须报出来，并说清多扣的部分在次年汇算清缴时退")
	}
	if !strings.Contains(r.Rows[0].Note, "不再预扣") {
		t.Errorf("行说明要说清本期不再预扣：%q", r.Rows[0].Note)
	}
}

func TestIITWarnsWhenMonthsMissing(t *testing.T) {
	in := taxreturn.IITInput{
		Period: "2025-03", Table: payroll.DefaultWageTaxTable(),
		Employees: []taxreturn.IITEmployee{iitEmp("王五", 0, y(10_000), 0, 0, 0)},
	}
	r, _ := taxreturn.IIT(in)
	if !warnContains(r, "任职月数为 0") {
		t.Error("★ 任职月数为 0 时减除费用算不出来，必须报出来（多扣个税不会报错）")
	}
}

func TestIITEmptyEmployees(t *testing.T) {
	r, err := taxreturn.IIT(taxreturn.IITInput{
		Period: "2025-03", Table: payroll.DefaultWageTaxTable(),
	})
	if err != nil {
		t.Fatalf("没有员工时不该报错，应当给出说明：%v", err)
	}
	if !strings.Contains(r.Concludes, "没有可计算的员工") {
		t.Errorf("结论 = %q", r.Concludes)
	}
	if !warnContains(r, "工资模块") {
		t.Error("要告诉用户去哪儿生成工资单")
	}
}

// ---------------------------------------------------------------------------
// 表的通用约束
// ---------------------------------------------------------------------------

func TestReturnValidate(t *testing.T) {
	ok := taxreturn.Return{
		Kind: taxreturn.KindVAT, Draft: true,
		Period: "2025-03", Title: "增值税及附加税费计算表",
		Rows: []taxreturn.Row{
			{Line: "1", Label: "销项税额", Amount: y(100)},
			{Line: "2", Label: "进项税额", Amount: y(60)},
		},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("基准用例应当通过：%v", err)
	}

	cases := []struct {
		name string
		mod  func(r *taxreturn.Return)
		want string
	}{
		{"税种不认识", func(r *taxreturn.Return) { r.Kind = "stamp" }, "税种"},
		{"没有表名", func(r *taxreturn.Return) { r.Title = "  " }, "表名"},
		{"没有期间", func(r *taxreturn.Return) { r.Period = "" }, "期间"},
		{"行次重复", func(r *taxreturn.Return) { r.Rows[1].Line = "1" }, "行次"},
		{"有一行没名称", func(r *taxreturn.Return) { r.Rows[0].Label = "" }, "项目名称"},
		{"声称可直接申报", func(r *taxreturn.Return) { r.Submittable = true }, "不能"},
		{"不是草稿", func(r *taxreturn.Return) { r.Draft = false }, "只能是草稿"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ok
			r.Rows = append([]taxreturn.Row(nil), ok.Rows...)
			c.mod(&r)
			err := r.Validate()
			if err == nil {
				t.Fatal("应当被拦下")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("报错应当提到 %q，实际 %v", c.want, err)
			}
		})
	}
}

func TestKindLabels(t *testing.T) {
	for _, k := range taxreturn.AllKinds {
		if k.Label() == "" || k.Label() == string(k) {
			t.Errorf("税种 %s 没有中文名", k)
		}
	}
	if taxreturn.Kind("x").Valid() {
		t.Error("不认识的税种不该 Valid")
	}
}

func TestPeriodHelpers(t *testing.T) {
	if taxreturn.QuarterOf(1) != 1 || taxreturn.QuarterOf(3) != 1 ||
		taxreturn.QuarterOf(4) != 2 || taxreturn.QuarterOf(12) != 4 {
		t.Error("季度换算不对")
	}
	s, e, err := taxreturn.QuarterRange(2)
	if err != nil || s != 4 || e != 6 {
		t.Errorf("第 2 季度应当是 4-6 月，得到 %d-%d（err=%v）", s, e, err)
	}
	if _, _, err := taxreturn.QuarterRange(5); err == nil {
		t.Error("第 5 季度应当报错")
	}
	if got := taxreturn.CITPeriodLabel(2025, 5); got != "2025 年第 2 季度" {
		t.Errorf("季度文字 = %q", got)
	}
	if got := taxreturn.PeriodLabel(2025, 3); got != "2025 年 3 月" {
		t.Errorf("期间文字 = %q", got)
	}
	// 累计口径的起点是年初：3 月的累计应当包含 1、2 月，不含 4 月
	if !taxreturn.DateInRange(calendar.MustParse("2025-01-15"), 2025, 3) ||
		!taxreturn.DateInRange(calendar.MustParse("2025-03-31"), 2025, 3) {
		t.Error("年初至本月末都应当在累计范围内")
	}
	if taxreturn.DateInRange(calendar.MustParse("2025-04-01"), 2025, 3) ||
		taxreturn.DateInRange(calendar.MustParse("2024-12-31"), 2025, 3) {
		t.Error("★ 累计口径的起点是**年初**，不能跨年、不能超过本月")
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func keyOf(t *testing.T, r *taxreturn.Return, label string) money.Money {
	t.Helper()
	for _, k := range r.Keys {
		if k.Label == label {
			return k.Amount
		}
	}
	t.Fatalf("表里没有 %q，实际有 %v", label, keyLabels(r))
	return 0
}

func keyLabels(r *taxreturn.Return) []string {
	out := make([]string, 0, len(r.Keys))
	for _, k := range r.Keys {
		out = append(out, k.Label)
	}
	return out
}

func warnContains(r *taxreturn.Return, want string) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w, want) {
			return true
		}
	}
	return false
}
