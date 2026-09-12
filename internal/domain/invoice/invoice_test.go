package invoice

import (
	"testing"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

func y(n int64) money.Money { return money.Money(n) * money.Yuan }

func base() *Invoice {
	return &Invoice{
		Direction:   DirInput,
		Kind:        KindSpecial,
		Code:        "033002100111",
		Number:      "12345678",
		InvoiceDate: calendar.MustParse("2025-09-10"),
		SellerName:  "杭州某某办公用品有限公司",
		SellerTaxNo: "91330100MA2XXXXXXX",
		BuyerName:   "测试公司",
		BuyerTaxNo:  "91330100MA2YYYYYYY",
		AmountExTax: y(1000),
		TaxRate:     Rate13,
		Status:      StatusPending,
	}
}

// ---------------------------------------------------------------------------
// 金额三分法
// ---------------------------------------------------------------------------

func TestComputeTax(t *testing.T) {
	inv := base()
	inv.ComputeTax()
	// 1000 × 13% = 130
	if inv.TaxAmount != y(130) {
		t.Errorf("税额 = %s，期望 130.00", inv.TaxAmount)
	}
	if inv.TotalAmount != y(1130) {
		t.Errorf("价税合计 = %s，期望 1130.00", inv.TotalAmount)
	}
	if err := inv.Validate(); err != nil {
		t.Errorf("自洽的发票应通过校验: %v", err)
	}
}

func TestReverseFromTotal(t *testing.T) {
	inv := base()
	inv.AmountExTax = 0
	inv.TotalAmount = y(1130) // 拿到发票时看到的是价税合计
	inv.ReverseFromTotal()

	// 1130 / 1.13 = 1000
	if inv.AmountExTax != y(1000) {
		t.Errorf("不含税金额 = %s，期望 1000.00", inv.AmountExTax)
	}
	if inv.TaxAmount != y(130) {
		t.Errorf("税额 = %s，期望 130.00", inv.TaxAmount)
	}
	if err := inv.Validate(); err != nil {
		t.Errorf("反算后应自洽: %v", err)
	}
}

// 反算与正算必须互相可逆（除不尽的金额也要自洽）
func TestReverseThenComputeIsConsistent(t *testing.T) {
	rates := []money.Rate{Rate13, Rate9, Rate6, Rate3, Rate1}
	for _, r := range rates {
		for _, total := range []money.Money{y(100), y(1130), y(999), money.MustParse("12345.67")} {
			inv := &Invoice{TaxRate: r, TotalAmount: total}
			inv.ReverseFromTotal()
			// 反算出的不含税 + 税额 必须精确等于价税合计
			if got := inv.AmountExTax.Add(inv.TaxAmount); got != total {
				t.Errorf("税率 %s、价税合计 %s：反算后 %s + %s = %s ≠ %s",
					r, total, inv.AmountExTax, inv.TaxAmount, got, total)
			}
		}
	}
}

// ★ 金额不自洽必须被拒绝 —— 差一分这张发票就不能用于抵扣
func TestValidateRejectsInconsistentAmount(t *testing.T) {
	inv := base()
	inv.TaxAmount = y(130)
	inv.TotalAmount = y(1129) // 应为 1130
	err := inv.Validate()
	if err == nil {
		t.Fatal("金额不自洽应报错")
	}
	if !contains(err.Error(), "1,130.00") {
		t.Errorf("错误信息应给出正确值，得到 %v", err)
	}
}

func TestValidateRejectsNegative(t *testing.T) {
	inv := base()
	inv.AmountExTax = y(-1)
	inv.TaxAmount = y(0)
	inv.TotalAmount = y(-1)
	if err := inv.Validate(); err == nil {
		t.Error("负数金额应报错")
	}
}

// ---------------------------------------------------------------------------
// 专票 vs 普票
// ---------------------------------------------------------------------------

// ★ 只有专用发票的进项税可以抵扣 —— 这是「专票 vs 普票」的实质区别
func TestDeductibility(t *testing.T) {
	special := base()
	special.ComputeTax()
	if !special.Kind.Deductible() {
		t.Error("专用发票应可抵扣")
	}
	if special.DeductibleTax() != y(130) {
		t.Errorf("可抵扣税额 = %s，期望 130.00", special.DeductibleTax())
	}
	// 专票：费用取不含税金额（税额单独挂进项税）
	if special.CostAmount() != y(1000) {
		t.Errorf("专票成本金额 = %s，期望 1000.00", special.CostAmount())
	}

	general := base()
	general.Kind = KindGeneral
	general.ComputeTax()
	if general.Kind.Deductible() {
		t.Error("普通发票不应可抵扣")
	}
	if general.DeductibleTax() != 0 {
		t.Errorf("普票可抵扣税额应为 0，得到 %s", general.DeductibleTax())
	}
	// 普票：税额不能抵扣，一并计入成本
	if general.CostAmount() != y(1130) {
		t.Errorf("普票成本金额 = %s，期望 1130.00（价税合计全额计入）", general.CostAmount())
	}

	// 电子专票同样可抵扣
	es := base()
	es.Kind = KindESpecial
	if !es.Kind.Deductible() {
		t.Error("电子专用发票应可抵扣")
	}
}

// 销项税不是「抵扣」，是应交
func TestOutputInvoiceHasNoDeductibleTax(t *testing.T) {
	out := base()
	out.Direction = DirOutput
	out.ComputeTax()
	if out.DeductibleTax() != 0 {
		t.Errorf("销项发票不应有可抵扣税额，得到 %s", out.DeductibleTax())
	}
}

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

func TestValidateRequiredFields(t *testing.T) {
	cases := map[string]func(*Invoice){
		"方向非法":  func(i *Invoice) { i.Direction = "bogus" },
		"种类非法":  func(i *Invoice) { i.Kind = "bogus" },
		"缺发票号码": func(i *Invoice) { i.Number = "" },
		"缺开票日期": func(i *Invoice) { i.InvoiceDate = calendar.Date{} },
		"状态非法":  func(i *Invoice) { i.Status = "bogus" },
	}
	for name, mutate := range cases {
		inv := base()
		inv.ComputeTax()
		mutate(inv)
		if err := inv.Validate(); err == nil {
			t.Errorf("%s 应报错", name)
		}
	}
}

// 已作废的发票不再校验金额（历史数据可能本来就不规范）
func TestVoidedSkipsAmountCheck(t *testing.T) {
	inv := base()
	inv.Status = StatusVoided
	inv.TotalAmount = y(999) // 故意与不含税+税额不符
	if err := inv.Validate(); err != nil {
		t.Errorf("已作废发票应跳过金额校验，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 判重
// ---------------------------------------------------------------------------

func TestIdentity(t *testing.T) {
	a := base()
	b := base()
	if a.Identity() != b.Identity() {
		t.Error("相同的方向+代码+号码应得到相同标识")
	}
	b.Number = "99999999"
	if a.Identity() == b.Identity() {
		t.Error("号码不同不应视为同一张")
	}
	// 方向不同也不应视为同一张（进项与销项可能碰巧号码相同）
	c := base()
	c.Direction = DirOutput
	if a.Identity() == c.Identity() {
		t.Error("方向不同不应视为同一张")
	}
}

// ---------------------------------------------------------------------------
// 汇总
// ---------------------------------------------------------------------------

func TestSummarize(t *testing.T) {
	e1 := base()
	e1.ComputeTax()

	e2 := base()
	e2.Number = "87654321"
	e2.Kind = KindGeneral
	e2.AmountExTax = y(500)
	e2.ComputeTax()

	e3 := base()
	e3.Number = "VOIDED01"
	e3.Status = StatusVoided
	e3.AmountExTax = y(99999)
	e3.ComputeTax()

	s := Summarize([]*Invoice{e1, e2, e3})
	if s.Count != 2 {
		t.Errorf("张数 = %d，期望 2（已作废不计）", s.Count)
	}
	if s.AmountExTax != y(1500) {
		t.Errorf("不含税合计 = %s，期望 1500.00", s.AmountExTax)
	}
	if s.TotalAmount != y(1695) { // 1130 + 565
		t.Errorf("价税合计 = %s，期望 1695.00", s.TotalAmount)
	}
	// 只有专票的 130 可抵扣，普票的 65 不可
	if s.DeductibleTax != y(130) {
		t.Errorf("可抵扣税额 = %s，期望 130.00（仅专票）", s.DeductibleTax)
	}
}

func TestCommonRates(t *testing.T) {
	rates := CommonRates()
	if len(rates) == 0 {
		t.Fatal("应返回常见税率")
	}
	// 13%、9%、6%、3%、1%、0
	want := []money.Rate{Rate13, Rate9, Rate6, Rate3, Rate1, Rate0}
	if len(rates) != len(want) {
		t.Fatalf("税率数 = %d，期望 %d", len(rates), len(want))
	}
	for i := range want {
		if rates[i] != want[i] {
			t.Errorf("第 %d 个税率 = %s，期望 %s", i, rates[i], want[i])
		}
	}
}

func TestKindLabels(t *testing.T) {
	cases := map[Kind]string{
		KindSpecial:  "增值税专用发票",
		KindGeneral:  "增值税普通发票",
		KindESpecial: "电子专用发票",
		KindEGeneral: "电子普通发票",
	}
	for k, want := range cases {
		if got := k.Label(); got != want {
			t.Errorf("%s.Label() = %q，期望 %q", k, got, want)
		}
	}
}

func TestDirectionLabels(t *testing.T) {
	if DirInput.Label() != "进项" || DirOutput.Label() != "销项" {
		t.Error("方向中文名错误")
	}
}

func TestStatusValidation(t *testing.T) {
	for _, s := range []Status{StatusPending, StatusVerified, StatusBooked, StatusVoided} {
		if !s.Valid() {
			t.Errorf("%s 应合法", s)
		}
	}
	if Status("bogus").Valid() {
		t.Error("未知状态不应合法")
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
