package vat

// # 为什么免税不能用「税率 0」表示
//
// 「零税率」与「免税」在**税额上都是零**，但两者的税收处理与进项处理
// **完全相反**：
//
//	零税率  销项 0，进项**可以退**（出口退税）—— 国家鼓励出口
//	免税    销项 0，进项**不得抵扣也不得退**，要计入成本
//
// 只存一个 `rate = 0` 是分不出这两种情况的：程序会把一笔出口业务
// 当成免税（少退了税），或者把一笔免税业务当成零税率（多退了税）。
// 两者都是**真金白银的错**，而且不会报任何错。
//
// 所以税率字段是**可空的**：`nil` 表示「这项业务不适用税率」
// （免税、不征税），而不是「税率是 0」。同时税收处理与进项处理
// 各自独立成字段，不靠税率去反推。

// TaxTreatment 是销项侧的税收处理方式。
type TaxTreatment string

// 税收处理方式。
const (
	// TreatmentTaxable 征税：按税率或征收率计算销项税额。
	TreatmentTaxable TaxTreatment = "TAXABLE"
	// TreatmentExempt 免税：不产生销项，进项不得抵扣。
	TreatmentExempt TaxTreatment = "EXEMPT"
	// TreatmentExemptNoRefund 免税、不退税：不产生销项，
	// 进项既不得抵扣也不得退，应计入成本。
	//
	// 小规模纳税人出口货物、符合范围的跨境服务与无形资产适用这一档
	// （财政部、税务总局 2026 年第 11 号公告）。
	TreatmentExemptNoRefund TaxTreatment = "EXEMPT_NO_REFUND"
	// TreatmentZeroRated 零税率：销项 0，进项**可以退**（出口退税）。
	//
	// ★ 只适用于采用一般计税的一般纳税人，不放宽给小规模纳税人。
	TreatmentZeroRated TaxTreatment = "ZERO_RATED"
	// TreatmentNotTaxable 不征税：不属于增值税征税范围，
	// 不产生销项，也不涉及进项转出。
	TreatmentNotTaxable TaxTreatment = "NOT_TAXABLE"
)

// AllTreatments 列出全部税收处理方式。
func AllTreatments() []TaxTreatment {
	return []TaxTreatment{TreatmentTaxable, TreatmentExempt,
		TreatmentExemptNoRefund, TreatmentZeroRated, TreatmentNotTaxable}
}

// Valid 报告税收处理方式是否合法。
func (t TaxTreatment) Valid() bool {
	for _, x := range AllTreatments() {
		if t == x {
			return true
		}
	}
	return false
}

// Label 返回中文名。
func (t TaxTreatment) Label() string {
	switch t {
	case TreatmentTaxable:
		return "征税"
	case TreatmentExempt:
		return "免税"
	case TreatmentExemptNoRefund:
		return "免税（不退税）"
	case TreatmentZeroRated:
		return "零税率（退免税）"
	case TreatmentNotTaxable:
		return "不征税"
	default:
		return string(t)
	}
}

// HasOutputTax 报告是否产生销项税额。
func (t TaxTreatment) HasOutputTax() bool { return t == TreatmentTaxable }

// RequiresRate 报告该处理方式是否需要一个税率/征收率。
//
// 免税、零税率、不征税都不需要 —— 它们的税率是**不适用**而不是 0。
func (t TaxTreatment) RequiresRate() bool { return t == TreatmentTaxable }

// InputTaxTreatment 是进项税额的处理方式。
type InputTaxTreatment string

// 进项税额处理方式。
const (
	// InputDeductible 可抵扣（一般计税项目对应的进项）。
	InputDeductible InputTaxTreatment = "DEDUCTIBLE"
	// InputDeductibleRefundable 可抵扣并可退（零税率对应的进项）。
	InputDeductibleRefundable InputTaxTreatment = "DEDUCTIBLE_REFUNDABLE"
	// InputNonDeductibleNonRefundable 不得抵扣、不得退税，应计入成本。
	//
	// 小规模纳税人（不论取得什么凭证）、免税项目、简易计税项目
	// 对应的进项都在这一档。
	InputNonDeductibleNonRefundable InputTaxTreatment = "NON_DEDUCTIBLE_NON_REFUNDABLE"
	// InputNotApplicable 不涉及（不征税交易）。
	InputNotApplicable InputTaxTreatment = "NOT_APPLICABLE"
)

// AllInputTreatments 列出全部进项处理方式。
func AllInputTreatments() []InputTaxTreatment {
	return []InputTaxTreatment{InputDeductible, InputDeductibleRefundable,
		InputNonDeductibleNonRefundable, InputNotApplicable}
}

// Valid 报告进项处理方式是否合法。
func (t InputTaxTreatment) Valid() bool {
	for _, x := range AllInputTreatments() {
		if t == x {
			return true
		}
	}
	return false
}

// Label 返回中文名。
func (t InputTaxTreatment) Label() string {
	switch t {
	case InputDeductible:
		return "可抵扣"
	case InputDeductibleRefundable:
		return "可抵扣并可退"
	case InputNonDeductibleNonRefundable:
		return "不得抵扣、不得退税（计入成本）"
	case InputNotApplicable:
		return "不涉及"
	default:
		return string(t)
	}
}

// CanDeduct 报告该处理方式下进项能否抵扣。
func (t InputTaxTreatment) CanDeduct() bool {
	return t == InputDeductible || t == InputDeductibleRefundable
}

// CanRefund 报告该处理方式下进项能否退税。
func (t InputTaxTreatment) CanRefund() bool { return t == InputDeductibleRefundable }
