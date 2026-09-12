package vat

import (
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
)

func tax(n int64) money.Money { return money.Money(n) * money.Yuan }

// base 是一个「一切都合规」的输入，各测试只改需要的那一项。
func base() DeductionInput {
	return DeductionInput{
		Status: VATGeneral, IdentityKnown: true,
		Method: MethodGeneral, Voucher: VoucherSpecialInvoice,
		TaxAmount: tax(1300),
	}
}

// ★ 一般纳税人取得专票、用于一般计税项目 → 可以抵扣。
func TestDeductibleBaseline(t *testing.T) {
	r := JudgeDeduction(base())
	if !r.Deductible {
		t.Fatalf("应可抵扣，实际 %v（%s）", r.Reason.Label(), r.Note)
	}
	if r.DeductibleAmount != tax(1300) {
		t.Errorf("可抵扣金额 = %s，期望 1300.00", r.DeductibleAmount)
	}
	if r.TransferOut != 0 || r.IncludedInCost != 0 {
		t.Error("可抵扣时不该有转出或计入成本的金额")
	}
}

// ★★ 小规模纳税人取得专用发票也**不能**抵扣，应计入成本或资产价值。
//
// 这是用户明确点名的一条，也是最容易做错的一条：
// 科目表里有「进项税额」专栏，小规模账套同样能挂上去。
func TestSmallScaleCannotDeductEvenWithSpecialInvoice(t *testing.T) {
	in := base()
	in.Status = VATSmallScale
	r := JudgeDeduction(in)

	if r.Deductible {
		t.Fatal("★ 小规模纳税人取得专票也不能抵扣")
	}
	if r.Reason != ReasonNotEligibleTaxpayer {
		t.Errorf("原因 = %v，期望「纳税人身份不允许」", r.Reason.Label())
	}
	if r.IncludedInCost != tax(1300) {
		t.Errorf("应计入成本的金额 = %s，期望 1300.00", r.IncludedInCost)
	}
	if r.TransferOut != 0 {
		t.Error("从未抵扣过，不存在转出")
	}
	if !strings.Contains(r.Note, "计入成本") {
		t.Errorf("提示应说明税额计入成本，实际 %q", r.Note)
	}
}

// ★ 身份未知时不能默认成一般纳税人 —— 那是放行本不该抵的进项。
func TestUnknownIdentityBlocksDeduction(t *testing.T) {
	in := base()
	in.IdentityKnown = false
	r := JudgeDeduction(in)
	if r.Deductible {
		t.Fatal("★ 身份未知时不能抵扣（默认成一般纳税人等于少缴税）")
	}
	if !strings.Contains(r.Note, "补填纳税人身份") {
		t.Errorf("提示应告诉用户怎么解决，实际 %q", r.Note)
	}
}

// ★ 计税方法决定能否抵扣：简易计税与免税都不行，零税率可以。
func TestMethodDecidesDeduction(t *testing.T) {
	cases := map[TaxationMethod]bool{
		MethodGeneral:    true,
		MethodZeroRated:  true,
		MethodSimplified: false,
		MethodExempt:     false,
	}
	for m, want := range cases {
		in := base()
		in.Method = m
		if got := JudgeDeduction(in).Deductible; got != want {
			t.Errorf("%s 可抵扣 = %v，期望 %v", m.Label(), got, want)
		}
	}
}

// ★ 凭证类型决定能否抵扣：普票不行，而通行费/旅客运输这类
// 「其他具有进项抵扣功能的合法凭证」可以。
func TestVoucherKindDecidesDeduction(t *testing.T) {
	cases := map[VoucherKind]bool{
		VoucherSpecialInvoice:  true,
		VoucherCustomsPayment:  true,
		VoucherOverseasTaxPaid: true,
		VoucherFarmPurchase:    true,
		VoucherFarmSales:       true,
		VoucherOtherDeductible: true,
		VoucherOrdinary:        false,
		VoucherNone:            false,
	}
	for k, want := range cases {
		in := base()
		in.Voucher = k
		res := JudgeDeduction(in)
		if res.Deductible != want {
			t.Errorf("%s 可抵扣 = %v，期望 %v", k.Label(), res.Deductible, want)
		}
		if !want {
			if res.Reason != ReasonInvalidVoucher {
				t.Errorf("%s 的原因 = %v，期望「凭证类型不具抵扣功能」",
					k.Label(), res.Reason.Label())
			}
			// 凭证本身不合格时不存在转出（从没抵过）
			if res.TransferOut != 0 {
				t.Errorf("%s 不该有转出", k.Label())
			}
			if res.IncludedInCost != tax(1300) {
				t.Errorf("%s 应把税额计入成本", k.Label())
			}
		}
	}
}

// ★ 用户列出的每一条「不得抵扣」都要能识别。
func TestAllNonDeductibleUsages(t *testing.T) {
	cases := []struct {
		name string
		set  func(*DeductionInput)
		want NonDeductibleReason
	}{
		{"用于简易计税或免税项目", func(in *DeductionInput) { in.ForSimplifiedOrExempt = true }, ReasonSimplifiedOrExempt},
		{"非正常损失", func(in *DeductionInput) { in.AbnormalLoss = true }, ReasonAbnormalLoss},
		{"集体福利、个人消费、交际应酬", func(in *DeductionInput) { in.CollectiveWelfare = true }, ReasonCollectiveWelfare},
		{"餐饮、居民日常、娱乐服务", func(in *DeductionInput) { in.CateringRecreation = true }, ReasonCateringRecreation},
		{"贷款利息及相关费用", func(in *DeductionInput) { in.LoanInterest = true }, ReasonLoanInterest},
		{"特定非应税交易", func(in *DeductionInput) { in.NonTaxableTransaction = true }, ReasonNonTaxableTransaction},
		{"股权转让、股息红利等", func(in *DeductionInput) { in.EquityTransfer = true }, ReasonEquityTransfer},
	}
	for _, c := range cases {
		in := base()
		c.set(&in)
		r := JudgeDeduction(in)
		if r.Deductible {
			t.Errorf("%s：不该抵扣", c.name)
			continue
		}
		if r.Reason != c.want {
			t.Errorf("%s：原因 = %v，期望 %v", c.name, r.Reason.Label(), c.want.Label())
		}
		if r.IncludedInCost != tax(1300) {
			t.Errorf("%s：税额应计入成本", c.name)
		}
	}
}

// ★ 「不予抵扣」与「进项税额转出」是两件事。
//
// 已经抵过、事后发现不得抵扣的，要做**转出**；
// 从没抵过的，只需把税额计入成本。两者的账务处理完全不同。
func TestTransferOutOnlyWhenAlreadyCredited(t *testing.T) {
	// 没抵过 → 计入成本，不转出
	in := base()
	in.CateringRecreation = true
	notYet := JudgeDeduction(in)
	if notYet.TransferOut != 0 || notYet.IncludedInCost != tax(1300) {
		t.Errorf("未抵扣过：转出 %s、计入成本 %s，期望 0 / 1300",
			notYet.TransferOut, notYet.IncludedInCost)
	}

	// 抵过了 → 转出
	in.AlreadyCredited = true
	already := JudgeDeduction(in)
	if already.TransferOut != tax(1300) {
		t.Errorf("已抵扣过：应转出 1300.00，实际 %s", already.TransferOut)
	}
	if already.IncludedInCost != 0 {
		t.Errorf("已抵扣过：不应再计入成本，实际 %s", already.IncludedInCost)
	}
	if !strings.Contains(already.Note, "进项税额转出") {
		t.Errorf("提示应说明要做转出，实际 %q", already.Note)
	}

	// 反例：凭证本身不合格时，即使标记「已抵扣」也不该产生转出
	// （它根本不构成进项，没有可转出的东西）
	bad := base()
	bad.Voucher = VoucherOrdinary
	bad.AlreadyCredited = true
	if got := JudgeDeduction(bad); got.TransferOut != 0 {
		t.Errorf("凭证不具抵扣功能时不该有转出，实际 %s", got.TransferOut)
	}
}

// ★ 混合用途长期资产：单项原值 ≤500 万全额抵扣，>500 万先抵后调。
func TestLongTermAssetThreshold(t *testing.T) {
	// 恰好 500 万 → 全额抵扣
	in := base()
	in.IsLongTermAsset, in.MixedUse = true, true
	in.AssetOriginalValue = LongTermAssetThreshold
	r := JudgeDeduction(in)
	if !r.Deductible || r.DeductibleAmount != tax(1300) {
		t.Errorf("原值 500 万元应全额抵扣，实际 %v / %s", r.Deductible, r.DeductibleAmount)
	}
	if strings.Contains(r.Note, "年度调整") {
		t.Errorf("未超过分界点不该提年度调整，实际 %q", r.Note)
	}

	// 超过 500 万 → 先抵扣，但提示需按年度调整
	in.AssetOriginalValue = LongTermAssetThreshold + 1
	r2 := JudgeDeduction(in)
	if !r2.Deductible {
		t.Error("超过 500 万元仍是先抵扣")
	}
	if !strings.Contains(r2.Note, "年度调整") {
		t.Errorf("超过 500 万元应提示按年度调整，实际 %q", r2.Note)
	}

	// 非混合用途的长期资产不受这条影响
	in.MixedUse = false
	in.AssetOriginalValue = LongTermAssetThreshold * 2
	if r3 := JudgeDeduction(in); strings.Contains(r3.Note, "年度调整") {
		t.Error("非混合用途不该适用年度调整规则")
	}
}

// 判定顺序：小规模 + 普票，应当报「身份不允许」而不是「凭证不合格」——
// 两者的账务处理与提示话术都不同。
func TestDeductionOrderPrefersIdentityReason(t *testing.T) {
	in := base()
	in.Status = VATSmallScale
	in.Voucher = VoucherOrdinary
	r := JudgeDeduction(in)
	if r.Reason != ReasonNotEligibleTaxpayer {
		t.Errorf("原因 = %v，期望先报「纳税人身份不允许」", r.Reason.Label())
	}
}

// 计税方法与免税/零税率的区分要落到抵扣结论上。
func TestExemptAndZeroRatedDifferInDeduction(t *testing.T) {
	exempt := base()
	exempt.Method = MethodExempt
	zero := base()
	zero.Method = MethodZeroRated

	if JudgeDeduction(exempt).Deductible {
		t.Error("免税项目的进项不得抵扣")
	}
	if !JudgeDeduction(zero).Deductible {
		t.Error("零税率项目的进项可以抵扣（并可退税）")
	}
}

// 全部凭证类型都要有中文名，界面上不能显示英文常量。
func TestAllVoucherKindsHaveLabels(t *testing.T) {
	for _, k := range AllVoucherKinds() {
		if k.Label() == string(k) {
			t.Errorf("凭证类型 %q 缺少中文名", k)
		}
	}
	seen := map[string]bool{}
	for _, k := range AllVoucherKinds() {
		if seen[string(k)] {
			t.Errorf("凭证类型重复：%s", k)
		}
		seen[string(k)] = true
	}
}

// 全部不得抵扣原因都要有中文名与转出判定。
func TestAllReasonsHaveLabels(t *testing.T) {
	reasons := []NonDeductibleReason{
		ReasonNone, ReasonNotEligibleTaxpayer, ReasonInvalidVoucher,
		ReasonSimplifiedOrExempt, ReasonAbnormalLoss, ReasonCollectiveWelfare,
		ReasonCateringRecreation, ReasonLoanInterest, ReasonNonTaxableTransaction,
		ReasonEquityTransfer, ReasonPendingAdjustment,
	}
	for _, r := range reasons {
		if r == ReasonNone {
			continue
		}
		if r.Label() == string(r) {
			t.Errorf("原因 %q 缺少中文名", r)
		}
	}
	// 身份/凭证类原因不该要求转出（从没抵过）
	if ReasonNotEligibleTaxpayer.RequiresTransferOut() {
		t.Error("身份不允许抵扣时不存在转出")
	}
	if ReasonInvalidVoucher.RequiresTransferOut() {
		t.Error("凭证不具抵扣功能时不存在转出")
	}
	// 用途类原因要求转出
	for _, r := range []NonDeductibleReason{
		ReasonSimplifiedOrExempt, ReasonAbnormalLoss, ReasonCollectiveWelfare,
		ReasonCateringRecreation, ReasonLoanInterest, ReasonNonTaxableTransaction,
		ReasonEquityTransfer,
	} {
		if !r.RequiresTransferOut() {
			t.Errorf("%s 应要求进项税额转出", r.Label())
		}
	}
}
