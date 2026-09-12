// Package invoice 实现发票档案。
//
// 小微企业的发票管理需求很具体：
//
//   - 进项发票（供应商开来）：登记、认证状态跟踪、与采购/费用凭证关联
//   - 销项发票（开给客户）：登记、与收入凭证关联
//   - 按期间汇总，便于与增值税申报表核对
//
// 刻意**不做**的事（原始需求明确排除）：
//
//   - 不做图像 OCR：扫描件识别准确率不足以直接入账，错误率高于手工录入
//   - 不做增值税申报对接：税局接口地域差异大、变更频繁，维护成本远超收益
//
// 发票金额的三分法（不含税金额 / 税额 / 价税合计）必须自洽，
// 这是本包校验的重点 —— 一个数字对不上，整张发票就不能用。
package invoice

import (
	"errors"
	"fmt"
	"strings"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// 发票相关错误。
var (
	ErrBadDirection = errors.New("invoice: 发票方向非法")
	ErrBadKind      = errors.New("invoice: 发票种类非法")
	ErrBadAmount    = errors.New("invoice: 金额不自洽")
	ErrNoNumber     = errors.New("invoice: 缺少发票号码")
	ErrNoDate       = errors.New("invoice: 缺少开票日期")
	ErrDuplicate    = errors.New("invoice: 发票重复")
	ErrBadStatus    = errors.New("invoice: 发票状态非法")
)

// Direction 是发票方向。
type Direction string

// 发票方向。中文语境里「进项」「销项」比「采购」「销售」更准确 ——
// 它们描述的是增值税链条上的位置，而不是业务动作。
const (
	DirInput  Direction = "input"  // 进项（供应商开来，可抵扣）
	DirOutput Direction = "output" // 销项（开给客户，应计提）
)

// Label 返回中文名。
func (d Direction) Label() string {
	switch d {
	case DirInput:
		return "进项"
	case DirOutput:
		return "销项"
	default:
		return string(d)
	}
}

// Valid 报告方向是否合法。
func (d Direction) Valid() bool { return d == DirInput || d == DirOutput }

// Kind 是发票种类。
type Kind string

// 发票种类。
const (
	KindSpecial  Kind = "special"   // 增值税专用发票（可抵扣进项）
	KindGeneral  Kind = "general"   // 增值税普通发票（不可抵扣）
	KindESpecial Kind = "e_special" // 电子专用发票
	KindEGeneral Kind = "e_general" // 电子普通发票
	KindOther    Kind = "other"     // 其他（定额发票、机动车发票等）
)

// Label 返回中文名。
func (k Kind) Label() string {
	switch k {
	case KindSpecial:
		return "增值税专用发票"
	case KindGeneral:
		return "增值税普通发票"
	case KindESpecial:
		return "电子专用发票"
	case KindEGeneral:
		return "电子普通发票"
	case KindOther:
		return "其他发票"
	default:
		return string(k)
	}
}

// Valid 报告种类是否合法。
func (k Kind) Valid() bool {
	switch k {
	case KindSpecial, KindGeneral, KindESpecial, KindEGeneral, KindOther:
		return true
	default:
		return false
	}
}

// Deductible 报告该种发票的进项税是否可抵扣。
//
// 只有专用发票（含电子专票）的进项税额可以抵扣；
// 普通发票的税额要计入成本费用。这是「专票 vs 普票」的实质区别。
func (k Kind) Deductible() bool {
	return k == KindSpecial || k == KindESpecial
}

// Status 是发票的入账状态。
type Status string

// 发票状态。
const (
	StatusPending  Status = "pending"  // 未认证
	StatusVerified Status = "verified" // 已认证（已在税局勾选确认）
	StatusBooked   Status = "booked"   // 已入账（已生成/关联凭证）
	StatusVoided   Status = "voided"   // 已作废
)

// Label 返回中文名。
func (s Status) Label() string {
	switch s {
	case StatusPending:
		return "未认证"
	case StatusVerified:
		return "已认证"
	case StatusBooked:
		return "已入账"
	case StatusVoided:
		return "已作废"
	default:
		return string(s)
	}
}

// Valid 报告状态是否合法。
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusVerified, StatusBooked, StatusVoided:
		return true
	default:
		return false
	}
}

// Invoice 是一张发票。
type Invoice struct {
	ID        int64
	Direction Direction
	Kind      Kind

	// Code 是发票代码（10 或 12 位）；Number 是发票号码（8 位）。
	// 电子发票往往只有 20 位号码而没有代码，因此 Code 允许为空。
	Code   string
	Number string

	InvoiceDate calendar.Date

	SellerName  string
	SellerTaxNo string
	BuyerName   string
	BuyerTaxNo  string

	// 金额三分法：不含税金额 + 税额 = 价税合计
	AmountExTax money.Money
	TaxRate     money.Rate
	TaxAmount   money.Money
	TotalAmount money.Money

	// Category 是货物或服务分类（如「办公用品」「咨询服务」），
	// 用于按类别统计费用构成。
	Category string

	Status    Status
	ContactID *int64
	VoucherID *int64

	Remark string
	// AttachmentHashes 是随票附件的 sha256 列表（发票 PDF / 照片）。
	AttachmentHashes []string
}

// Validate 校验发票。
//
// 重点检查金额自洽：不含税金额 + 税额 必须等于价税合计（精确到分）。
// 差一分钱这张发票就不能用于抵扣或入账。
func (inv *Invoice) Validate() error {
	if !inv.Direction.Valid() {
		return fmt.Errorf("%w: %q", ErrBadDirection, inv.Direction)
	}
	if !inv.Kind.Valid() {
		return fmt.Errorf("%w: %q", ErrBadKind, inv.Kind)
	}
	if strings.TrimSpace(inv.Number) == "" {
		return ErrNoNumber
	}
	if !inv.InvoiceDate.Valid() {
		return fmt.Errorf("%w: %v", ErrNoDate, inv.InvoiceDate)
	}
	if !inv.Status.Valid() {
		return fmt.Errorf("%w: %q", ErrBadStatus, inv.Status)
	}
	if inv.Status == StatusVoided {
		return nil // 已作废的发票不再校验金额
	}

	neg := []struct {
		name string
		v    money.Money
	}{
		{"不含税金额", inv.AmountExTax}, {"税额", inv.TaxAmount},
		{"价税合计", inv.TotalAmount},
	}
	for _, n := range neg {
		if n.v.IsNegative() {
			return fmt.Errorf("%w: %s 为 %s", ErrBadAmount, n.name, n.v)
		}
	}

	// 三分法必须自洽
	want := inv.AmountExTax.Add(inv.TaxAmount)
	if inv.TotalAmount != want {
		return fmt.Errorf("%w: 不含税 %s + 税额 %s = %s，但价税合计为 %s（差 %s）",
			ErrBadAmount, inv.AmountExTax, inv.TaxAmount, want,
			inv.TotalAmount, inv.TotalAmount.Sub(want))
	}
	return nil
}

// DeductibleTax 返回可抵扣的进项税额。
//
// 普通发票的税额不可抵扣，要计入成本费用，因此返回 0。
func (inv *Invoice) DeductibleTax() money.Money {
	if inv.Direction != DirInput {
		return 0 // 销项税不是「抵扣」，是应交
	}
	if !inv.Kind.Deductible() {
		return 0
	}
	return inv.TaxAmount
}

// CostAmount 返回应计入成本费用的金额。
//
//	专票：不含税金额（税额单独挂进项税）
//	普票：价税合计（税额不能抵扣，一并计入成本）
func (inv *Invoice) CostAmount() money.Money {
	if inv.Direction == DirInput && inv.Kind.Deductible() {
		return inv.AmountExTax
	}
	return inv.TotalAmount
}

// Identity 返回发票的唯一标识，用于查重。
//
// 同一张发票可能被重复登记（换个单据再录一次），
// 因此需要按「方向 + 代码 + 号码」判重。
func (inv *Invoice) Identity() string {
	return strings.Join([]string{string(inv.Direction),
		strings.TrimSpace(inv.Code), strings.TrimSpace(inv.Number)}, "|")
}

// ComputeTax 由不含税金额与税率算出税额与价税合计。
//
// 税额按四舍五入到分。调用方应在录入时用它回填，
// 而不是让用户手工算三个数 —— 手工算必然出现差一两分的情况。
func (inv *Invoice) ComputeTax() {
	inv.TaxAmount = inv.TaxRate.Apply(inv.AmountExTax)
	inv.TotalAmount = inv.AmountExTax.Add(inv.TaxAmount)
}

// Reverse 由价税合计与税率反推不含税金额。
//
// 实务中拿到发票时看到的往往是价税合计，需要拆分。
//
//	不含税 = 价税合计 / (1 + 税率)
func (inv *Invoice) ReverseFromTotal() {
	// 注意：ApplyInverse 内部已经会加上 RateScale（即除以 1+税率），
	// 这里**不能**再把 RateScale 加进去，否则分母会变成 1+税率×2。
	inv.AmountExTax = inv.TaxRate.ApplyInverse(inv.TotalAmount)
	inv.TaxAmount = inv.TotalAmount.Sub(inv.AmountExTax)
}

// ---------------------------------------------------------------------------
// 汇总
// ---------------------------------------------------------------------------

// Summary 是按期间/方向的发票汇总。
type Summary struct {
	Count       int
	AmountExTax money.Money
	TaxAmount   money.Money
	TotalAmount money.Money
	// DeductibleTax 是可抵扣税额（仅进项专票）。
	DeductibleTax money.Money
}

// Summarize 汇总一组发票。
func Summarize(invs []*Invoice) Summary {
	var s Summary
	for _, inv := range invs {
		if inv.Status == StatusVoided {
			continue
		}
		s.Count++
		s.AmountExTax = s.AmountExTax.Add(inv.AmountExTax)
		s.TaxAmount = s.TaxAmount.Add(inv.TaxAmount)
		s.TotalAmount = s.TotalAmount.Add(inv.TotalAmount)
		s.DeductibleTax = s.DeductibleTax.Add(inv.DeductibleTax())
	}
	return s
}

// ---------------------------------------------------------------------------
// 常见税率
// ---------------------------------------------------------------------------

// 中国增值税税率（一般纳税人）。
//
// ⚠️ 税率会随政策调整（如 2019 年从 16%/10% 降为 13%/9%），
// 因此这里只提供**常量便于调用**，实际取值应由用户配置，
// 不应在业务逻辑里硬编码。
var (
	Rate13 = money.RatePercent(13) // 销售货物、提供加工修理修配劳务
	Rate9  = money.RatePercent(9)  // 交通运输、建筑、不动产租赁等
	Rate6  = money.RatePercent(6)  // 现代服务、生活服务
	Rate3  = money.RatePercent(3)  // 小规模纳税人征收率
	Rate1  = money.RatePercent(1)  // 小规模纳税人疫情期间优惠征收率
	Rate0  = money.Rate(0)         // 出口货物等
)

// CommonRates 返回常见税率，供界面下拉选择。
func CommonRates() []money.Rate {
	return []money.Rate{Rate13, Rate9, Rate6, Rate3, Rate1, Rate0}
}
