package vat

import (
	"fmt"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// VoucherKind 是进项税额的**扣税凭证类型**。
//
// 能抵扣的前提之一是取得合法扣税凭证。凭证类型本身就是一道门槛：
// 增值税普通发票不能抵扣，而通行费电子普票、旅客运输票据、
// 农产品收购发票虽然也是「普票」，却按规定可以计算抵扣。
// 所以不能简单按「专票 / 普票」二分。
type VoucherKind string

// 扣税凭证类型。
const (
	// VoucherSpecialInvoice 增值税专用发票（含税控机动车销售统一发票）。
	VoucherSpecialInvoice VoucherKind = "special_invoice"
	// VoucherCustomsPayment 海关进口增值税专用缴款书。
	VoucherCustomsPayment VoucherKind = "customs_payment"
	// VoucherOverseasTaxPaid 境外服务、无形资产等对应的完税凭证。
	VoucherOverseasTaxPaid VoucherKind = "overseas_tax_paid"
	// VoucherFarmPurchase 农产品收购发票。
	VoucherFarmPurchase VoucherKind = "farm_purchase"
	// VoucherFarmSales 农产品销售发票。
	VoucherFarmSales VoucherKind = "farm_sales"
	// VoucherOtherDeductible 其他具有进项抵扣功能的合法凭证
	// （如通行费电子普通发票、注明旅客身份信息的航空/铁路/公路客票）。
	VoucherOtherDeductible VoucherKind = "other_deductible"
	// VoucherOrdinary 增值税普通发票（不可抵扣）。
	VoucherOrdinary VoucherKind = "ordinary"
	// VoucherNone 无凭证 / 其他不可抵扣凭证。
	VoucherNone VoucherKind = "none"
)

// AllVoucherKinds 列出全部凭证类型。
func AllVoucherKinds() []VoucherKind {
	return []VoucherKind{
		VoucherSpecialInvoice, VoucherCustomsPayment, VoucherOverseasTaxPaid,
		VoucherFarmPurchase, VoucherFarmSales, VoucherOtherDeductible,
		VoucherOrdinary, VoucherNone,
	}
}

// Label 返回中文名。
func (k VoucherKind) Label() string {
	switch k {
	case VoucherSpecialInvoice:
		return "增值税专用发票"
	case VoucherCustomsPayment:
		return "海关进口增值税专用缴款书"
	case VoucherOverseasTaxPaid:
		return "境外服务、无形资产等完税凭证"
	case VoucherFarmPurchase:
		return "农产品收购发票"
	case VoucherFarmSales:
		return "农产品销售发票"
	case VoucherOtherDeductible:
		return "其他具有进项抵扣功能的合法凭证"
	case VoucherOrdinary:
		return "增值税普通发票"
	case VoucherNone:
		return "无凭证或其他不可抵扣凭证"
	default:
		return string(k)
	}
}

// CanDeduct 报告该凭证类型**本身**是否具有抵扣功能。
//
// 这是必要条件而非充分条件：还要看用途（是否用于一般计税项目）
// 与是否属于不得抵扣范围。见 JudgeDeduction。
func (k VoucherKind) CanDeduct() bool {
	switch k {
	case VoucherSpecialInvoice, VoucherCustomsPayment, VoucherOverseasTaxPaid,
		VoucherFarmPurchase, VoucherFarmSales, VoucherOtherDeductible:
		return true
	default:
		return false
	}
}

// Valid 报告凭证类型是否合法。
func (k VoucherKind) Valid() bool {
	for _, x := range AllVoucherKinds() {
		if k == x {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 不得抵扣的原因
// ---------------------------------------------------------------------------

// NonDeductibleReason 是不得抵扣进项税额的原因。
//
// ★ 必须**分类保存**而不是一个「不可抵扣」布尔值：
//
//   - 用于简易计税/免税项目、集体福利、非正常损失对应的进项，
//     如果已经抵扣过，要**做进项税额转出**；
//   - 而取得普票本身就不构成进项，不存在转出的问题。
//
// 两者的账务处理完全不同，报表上也要能分别统计。
type NonDeductibleReason string

// 不得抵扣原因。
const (
	// ReasonNone 不存在不得抵扣情形。
	ReasonNone NonDeductibleReason = ""
	// ReasonNotEligibleTaxpayer 纳税人身份或计税方法不允许抵扣
	// （小规模纳税人取得专票也不能抵扣，应计入成本或资产价值）。
	ReasonNotEligibleTaxpayer NonDeductibleReason = "not_eligible_taxpayer"
	// ReasonInvalidVoucher 凭证类型本身不具有抵扣功能。
	ReasonInvalidVoucher NonDeductibleReason = "invalid_voucher"
	// ReasonSimplifiedOrExempt 用于简易计税项目或免税项目。
	ReasonSimplifiedOrExempt NonDeductibleReason = "simplified_or_exempt"
	// ReasonAbnormalLoss 非正常损失。
	ReasonAbnormalLoss NonDeductibleReason = "abnormal_loss"
	// ReasonCollectiveWelfare 集体福利、个人消费和交际应酬。
	ReasonCollectiveWelfare NonDeductibleReason = "collective_welfare"
	// ReasonCateringRecreation 直接消费的餐饮、居民日常和娱乐服务。
	ReasonCateringRecreation NonDeductibleReason = "catering_recreation"
	// ReasonLoanInterest 贷款利息及与贷款直接相关的顾问费、手续费、咨询费。
	ReasonLoanInterest NonDeductibleReason = "loan_interest"
	// ReasonNonTaxableTransaction 对应特定非应税交易。
	ReasonNonTaxableTransaction NonDeductibleReason = "non_taxable_transaction"
	// ReasonEquityTransfer 股权转让、股息红利及部分境外交易等。
	//
	// 依据 2026 年第 25 号公告，2026-09-01 起这一类不得抵扣的范围
	// 又得到进一步明确。
	ReasonEquityTransfer NonDeductibleReason = "equity_transfer"
	// ReasonPendingAdjustment 混合用途长期资产原值超过 500 万元，
	// 先抵扣、后按年度调整（本年度暂按可抵处理，需在年末调整）。
	ReasonPendingAdjustment NonDeductibleReason = "pending_annual_adjustment"
)

// Label 返回中文名。
func (r NonDeductibleReason) Label() string {
	switch r {
	case ReasonNone:
		return "可抵扣"
	case ReasonNotEligibleTaxpayer:
		return "纳税人身份或计税方法不允许抵扣"
	case ReasonInvalidVoucher:
		return "凭证类型不具有抵扣功能"
	case ReasonSimplifiedOrExempt:
		return "用于简易计税或免税项目"
	case ReasonAbnormalLoss:
		return "非正常损失"
	case ReasonCollectiveWelfare:
		return "集体福利、个人消费或交际应酬"
	case ReasonCateringRecreation:
		return "直接消费的餐饮、居民日常和娱乐服务"
	case ReasonLoanInterest:
		return "贷款利息及与贷款直接相关的顾问费、手续费、咨询费"
	case ReasonNonTaxableTransaction:
		return "对应特定非应税交易"
	case ReasonEquityTransfer:
		return "股权转让、股息红利或部分境外交易"
	case ReasonPendingAdjustment:
		return "混合用途长期资产（超 500 万元），先抵扣后按年度调整"
	default:
		return string(r)
	}
}

// RequiresTransferOut 报告该原因下**已抵扣的进项是否需要转出**。
//
// 只有「本该不能抵、却已经抵了」的情形才需要转出；
// 凭证本身不具抵扣功能时根本不存在可转出的进项。
func (r NonDeductibleReason) RequiresTransferOut() bool {
	switch r {
	case ReasonSimplifiedOrExempt, ReasonAbnormalLoss,
		ReasonCollectiveWelfare, ReasonCateringRecreation,
		ReasonLoanInterest, ReasonNonTaxableTransaction,
		ReasonEquityTransfer, ReasonPendingAdjustment:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// 长期资产
// ---------------------------------------------------------------------------

// 混合用途长期资产的单项原值分界点（500 万元）。
//
// 单项原值不超过 500 万元的，符合条件时可以**全额抵扣**；
// 超过 500 万元的，先抵扣，之后按年度调整。
const LongTermAssetThreshold = money.Money(5_000_000_00)

// DeductionInput 是一次进项税额抵扣判定的输入。
type DeductionInput struct {
	// Status 是业务发生日的增值税纳税人身份。
	Status VATStatus
	// IdentityKnown 为假表示账套没有该日期的身份记录 —— 必须拒绝抵扣，
	// 不能默认成一般纳税人（那等于放行本不该抵的进项）。
	IdentityKnown bool
	// Method 是该笔业务对应的计税方法。
	Method TaxationMethod
	// InputTax 是该笔业务适用的进项税额处理方式，取自税率政策。
	//
	// ★ 它是**权威判据**：非空时直接采用，不再按身份与方法推断。
	//
	// 理由：进项处理是政策明文规定的，不该由程序从别的字段反推。
	// 例如小规模纳税人出口适用「免税、不退税」，
	// 进项是「不得抵扣、不得退税」—— 这一条写在出口政策里，
	// 而不是从「小规模」+「简易计税」两个条件推出来的
	// （推不出「不得退税」这一半）。
	InputTax InputTaxTreatment
	// Voucher 是扣税凭证类型。
	Voucher VoucherKind
	// TaxAmount 是凭证上注明的进项税额。
	TaxAmount money.Money

	// ---- 用途相关的不得抵扣情形（由用户按实际情况勾选）----
	//
	// ForSimplifiedOrExempt 是「用于简易计税项目或免税项目」。
	ForSimplifiedOrExempt bool
	// AbnormalLoss 是「非正常损失」。
	AbnormalLoss bool
	// CollectiveWelfare 是「集体福利、个人消费或交际应酬」。
	CollectiveWelfare bool
	// CateringRecreation 是「直接消费的餐饮、居民日常和娱乐服务」。
	CateringRecreation bool
	// LoanInterest 是「贷款利息及与贷款直接相关的顾问费、手续费、咨询费」。
	LoanInterest bool
	// NonTaxableTransaction 是「对应特定非应税交易」。
	NonTaxableTransaction bool
	// EquityTransfer 是「股权转让、股息红利或部分境外交易」。
	EquityTransfer bool

	// ---- 长期资产 ----
	// IsLongTermAsset 为真表示该支出形成长期资产（固定资产、无形资产、不动产）。
	IsLongTermAsset bool
	// MixedUse 为真表示该长期资产**混合用于**一般计税与不得抵扣项目。
	MixedUse bool
	// AssetOriginalValue 是该长期资产的单项原值。
	AssetOriginalValue money.Money
	// AlreadyCredited 为真表示该笔进项**已经抵扣过** ——
	// 决定「不得抵扣」是「不予抵扣」还是「需要进项税额转出」。
	AlreadyCredited bool
}

// DeductionResult 是抵扣判定的结论。
type DeductionResult struct {
	// Deductible 为真表示本笔进项税额可以抵扣。
	Deductible bool
	// Reason 是不可抵扣的原因（Deductible 为真时为零值）。
	Reason NonDeductibleReason
	// DeductibleAmount 是可抵扣的进项税额。
	DeductibleAmount money.Money
	// TransferOut 是需要转出的进项税额（不得抵扣且已抵扣时非零）。
	TransferOut money.Money
	// IncludedInCost 是应计入成本或资产价值的金额。
	//
	// 小规模纳税人取得专票、或不得抵扣的进项，税额都要计入成本 ——
	// 这正是「不能抵扣」在账上的具体表现。
	IncludedInCost money.Money
	// Note 是给用户看的说明。
	Note string
}

// JudgeDeduction 判定一笔进项税额能否抵扣。
//
// 判定顺序（前面的直接决定结论，不再往后看）：
//
//  1. 纳税人身份必须已知且允许抵扣 ——
//     小规模纳税人取得专用发票也**不能**抵扣，应计入成本或资产价值
//  2. 计税方法必须允许抵扣（简易计税、免税不得抵扣；零税率可以）
//  3. 凭证类型必须具有抵扣功能
//  4. 用途不得落入不得抵扣范围
//  5. 长期资产的 500 万元分界
//
// ★ 第 1 步在第 3 步之前是有意的：小规模纳税人就算取得专票，
// 也是「不能抵扣」，而不是「凭证不合格」——两者的账务处理不同，
// 提示用户的话术也不同。
func JudgeDeduction(in DeductionInput) DeductionResult {
	// 1. 纳税人身份
	if !in.IdentityKnown {
		return nonDeductible(in, ReasonNotEligibleTaxpayer,
			"账套没有该业务日期的增值税纳税人身份记录。请先在账套设置里"+
				"补填纳税人身份及生效日期 —— 在身份明确之前不能按一般纳税人抵扣")
	}
	if !in.Status.CanDeductInput() {
		return nonDeductible(in, ReasonNotEligibleTaxpayer,
			fmt.Sprintf("%s不得抵扣进项税额，取得的专用发票也应将税额计入"+
				"成本或资产价值", in.Status.Label()))
	}

	// 2. 计税方法
	if !in.Method.Valid() {
		return nonDeductible(in, ReasonNotEligibleTaxpayer,
			fmt.Sprintf("计税方法 %q 未知，无法判定能否抵扣", in.Method))
	}
	if !in.Method.AllowsInputCredit() {
		return nonDeductible(in, ReasonSimplifiedOrExempt,
			fmt.Sprintf("该笔业务按%s计税，对应的进项税额不得抵扣",
				in.Method.Label()))
	}

	// 3. 凭证类型
	if !in.Voucher.Valid() {
		return nonDeductible(in, ReasonInvalidVoucher,
			fmt.Sprintf("扣税凭证类型 %q 未知，无法判定能否抵扣", in.Voucher))
	}
	if !in.Voucher.CanDeduct() {
		return nonDeductible(in, ReasonInvalidVoucher,
			fmt.Sprintf("%s不具有进项抵扣功能", in.Voucher.Label()))
	}

	// 3b. 政策明文的进项处理方式优先。
	//
	// 它比「按身份与方法推断」更准：政策会写明「免税、不退税」，
	// 而「不退税」这一半是推不出来的。
	if in.InputTax != "" {
		if !in.InputTax.Valid() {
			return nonDeductible(in, ReasonNotEligibleTaxpayer,
				fmt.Sprintf("进项处理方式 %q 未知", in.InputTax))
		}
		if !in.InputTax.CanDeduct() {
			return nonDeductible(in, ReasonNotEligibleTaxpayer,
				fmt.Sprintf("按适用的税收政策，本笔业务的进项税额%s",
					in.InputTax.Label()))
		}
		// 可抵扣：继续走后面的用途与长期资产判断
	}

	// 4. 用途
	if reason, note, blocked := usageBlock(in); blocked {
		return nonDeductible(in, reason, note)
	}

	// 5. 长期资产
	if in.IsLongTermAsset && in.MixedUse &&
		in.AssetOriginalValue > LongTermAssetThreshold {
		// 超过 500 万元：先抵扣，之后按年度调整
		return DeductionResult{
			Deductible:       true,
			DeductibleAmount: in.TaxAmount,
			Note: fmt.Sprintf(
				"混合用途长期资产单项原值 %s 超过 500 万元，本年度先按全额抵扣，"+
					"之后需按规定按年度调整不得抵扣部分",
				in.AssetOriginalValue),
		}
	}

	return DeductionResult{
		Deductible:       true,
		DeductibleAmount: in.TaxAmount,
		Note: fmt.Sprintf("%s，用于一般计税项目，进项税额 %s 可以抵扣",
			in.Voucher.Label(), in.TaxAmount),
	}
}

// usageBlock 检查用途相关的不得抵扣情形。
func usageBlock(in DeductionInput) (NonDeductibleReason, string, bool) {
	switch {
	case in.ForSimplifiedOrExempt:
		return ReasonSimplifiedOrExempt,
			"该笔支出用于简易计税项目或免税项目，进项税额不得抵扣", true
	case in.AbnormalLoss:
		return ReasonAbnormalLoss, "非正常损失对应的进项税额不得抵扣", true
	case in.CollectiveWelfare:
		return ReasonCollectiveWelfare,
			"用于集体福利、个人消费或交际应酬的进项税额不得抵扣", true
	case in.CateringRecreation:
		return ReasonCateringRecreation,
			"直接消费的餐饮、居民日常和娱乐服务对应的进项税额不得抵扣", true
	case in.LoanInterest:
		return ReasonLoanInterest,
			"贷款利息及与贷款直接相关的顾问费、手续费、咨询费" +
				"对应的进项税额不得抵扣", true
	case in.NonTaxableTransaction:
		return ReasonNonTaxableTransaction,
			"对应特定非应税交易的进项税额不得抵扣", true
	case in.EquityTransfer:
		return ReasonEquityTransfer,
			"股权转让、股息红利或部分境外交易对应的进项税额不得抵扣" +
				"（依据财政部、税务总局 2026 年第 25 号公告）", true
	}
	return ReasonNone, "", false
}

// nonDeductible 组装「不得抵扣」的结论。
//
// 已抵扣过的要做进项税额转出；没抵过的只需把税额计入成本。
// 这两件事在账上完全不同，不能混为一谈。
func nonDeductible(in DeductionInput, reason NonDeductibleReason,
	note string) DeductionResult {

	res := DeductionResult{Reason: reason, Note: note}
	if in.AlreadyCredited && reason.RequiresTransferOut() {
		res.TransferOut = in.TaxAmount
		res.Note += fmt.Sprintf("；已抵扣的 %s 需做进项税额转出", in.TaxAmount)
		return res
	}
	res.IncludedInCost = in.TaxAmount
	res.Note += fmt.Sprintf("；税额 %s 应计入成本或资产价值", in.TaxAmount)
	return res
}

// ResolveRateForInvoice 是「签一张发票该怎么算税」的便捷入口。
//
// 把身份、主体、业务、方法、日期、政策表一次给全，返回适用的政策。
// 界面上录入发票时调它，避免调用方自己拼判定顺序。
func ResolveRateForInvoice(policies []RatePolicy, status VATStatus,
	subject Subject, category BizCategory, method TaxationMethod,
	on calendar.Date) (Resolution, error) {

	if !status.Valid() {
		return Resolution{}, fmt.Errorf("vat: 纳税人身份 %q 未知", status)
	}
	if !subject.Valid() {
		return Resolution{}, fmt.Errorf("vat: 经营主体 %q 未知", subject)
	}
	return ResolveRate(policies, status, subject, category, method, on)
}
