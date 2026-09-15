// Package taxfiling 是**税务申报台账**：记下「这一期这个税种报没报、
// 什么时候报的、报了多少、谁办的」。
//
// # 为什么这一件事需要落库，而计算表不需要
//
// 税务计算表是账套的派生结果：账一变，它跟着变，所以不存副本。
// 但「申报」是**发生过的事实** —— 账后来改了，已申报的那个数不会跟着改。
// 这两件事必须分开记，否则会出现最麻烦的一种局面：
//
//	账上算出来该交 7,000，而当时按 6,500 申报的，谁也说不清哪个是对的。
//
// 所以台账里存的是**申报当时的快照**（应补税额、申报日期、回执号），
// 并且提供一个勾稽函数：拿台账里的数与现在算出来的表比，
// 不一致就报出来 —— 差异本身就是要处理的事（更正申报 / 下期调整）。
//
// # 作废而不是删除
//
// 更正申报时旧记录置为 `void` 并留下作废人与原因，不物理删除：
// 「这一期一共报过几次、每次报了多少」是税务检查时要答的问题。
package taxfiling

import (
	"fmt"
	"strings"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/taxreturn"
)

// Kind 是税种（与计算表共用一套取值）。
type Kind = taxreturn.Kind

// 三种税。
const (
	KindVAT = taxreturn.KindVAT
	KindCIT = taxreturn.KindCIT
	KindIIT = taxreturn.KindIIT
)

// Status 是申报状态。
type Status string

// 三种状态。
const (
	// StatusFiled 已申报（未缴或无需缴纳）。
	StatusFiled Status = "filed"
	// StatusPaid 已申报并已缴款。
	StatusPaid Status = "paid"
	// StatusVoid 已作废（更正申报时留下痕迹）。
	StatusVoid Status = "void"
)

// AllStatuses 列出全部状态。
var AllStatuses = []Status{StatusFiled, StatusPaid, StatusVoid}

// Valid 报告状态是否已知。
func (s Status) Valid() bool {
	for _, x := range AllStatuses {
		if x == s {
			return true
		}
	}
	return false
}

// Label 返回中文名。
func (s Status) Label() string {
	switch s {
	case StatusFiled:
		return "已申报"
	case StatusPaid:
		return "已申报并缴纳"
	case StatusVoid:
		return "已作废"
	default:
		return string(s)
	}
}

// Filing 是一条申报记录。
type Filing struct {
	ID    int64
	Year  int
	Month int
	Kind  Kind
	// PeriodLabel 是申报属期的文字（如「2025 年第 1 季度」）。
	PeriodLabel string
	Status      Status
	// FiledDate / PaidDate 是申报日期与缴款日期（YYYY-MM-DD）。
	FiledDate string
	PaidDate  string
	// ---- 申报当时的快照 ----
	// Payable 是本期应补(退)税额（可为负：多缴）。
	Payable money.Money
	// TaxAmount / Surcharge / Paid 是三个分量：
	//
	//	Payable = TaxAmount + Surcharge − Paid
	//
	// ★ 必须有 Paid（本期已交/已预缴）：没有它，「应纳 9,000 + 附加 1,080」
	// 与「应补 4,080」就对不上 —— 而差的 6,000 正是这个月已经交掉的。
	TaxAmount money.Money
	Surcharge money.Money
	Paid      money.Money
	// ---- 回执 ----
	Channel   string
	ReceiptNo string
	Operator  string
	Note      string
	// ---- 作废痕迹 ----
	VoidedBy   string
	VoidedAt   string
	VoidReason string
	CreatedAt  string
	UpdatedAt  string
}

// Period 返回所属期间。
func (f Filing) Period() period.Key { return period.NewKey(f.Year, f.Month) }

// Validate 检查一条申报记录是否成立。
//
// ★ 只挡**硬错误**：不可能的日期、状态与字段互相矛盾、金额自己对不上。
// 「该不该现在申报」这类判断不在这里 —— 那是办税人的事。
func (f Filing) Validate() error {
	if !f.Kind.Valid() {
		return fmt.Errorf("%w: %q", ErrBadKind, f.Kind)
	}
	k := f.Period()
	if !k.Valid() {
		return fmt.Errorf("%w: %04d-%02d", ErrBadPeriod, f.Year, f.Month)
	}
	if !f.Status.Valid() {
		return fmt.Errorf("%w: %q", ErrBadStatus, f.Status)
	}
	if f.Status == StatusVoid {
		// 作废要留痕：谁、什么时候、为什么
		if strings.TrimSpace(f.VoidedBy) == "" {
			return fmt.Errorf("taxfiling: 作废要记清是谁办的")
		}
		if _, err := calendar.Parse(f.VoidedAt); err != nil {
			return fmt.Errorf("taxfiling: 作废日期读不出来（%q）", f.VoidedAt)
		}
		return nil
	}
	// 申报日期：必填、合法，而且**不能早于所属期的月末** ——
	// 属期还没过完就申报了，这个税种一定不是按月申报的
	filed, err := calendar.Parse(f.FiledDate)
	if err != nil {
		return fmt.Errorf("taxfiling: 申报日期读不出来（%q），应为 YYYY-MM-DD", f.FiledDate)
	}
	end, err := calendar.New(f.Year, f.Month, calendar.DaysInMonth(f.Year, f.Month))
	if err != nil {
		return fmt.Errorf("taxfiling: 属期 %04d-%02d 的月末算不出来", f.Year, f.Month)
	}
	if filed.Before(end) {
		return fmt.Errorf("taxfiling: 申报日期 %s 早于属期月末 %s —— "+
			"属期还没结束不可能完成申报，请核对日期", filed, end)
	}
	if f.PaidDate != "" {
		paid, perr := calendar.Parse(f.PaidDate)
		if perr != nil {
			return fmt.Errorf("taxfiling: 缴款日期读不出来（%q）", f.PaidDate)
		}
		if paid.Before(filed) {
			return fmt.Errorf("taxfiling: 缴款日期 %s 早于申报日期 %s", paid, filed)
		}
		if f.Status != StatusPaid {
			return fmt.Errorf("taxfiling: 填了缴款日期，状态应当是「%s」而不是「%s」",
				StatusPaid.Label(), f.Status.Label())
		}
	}
	if f.Status == StatusPaid && strings.TrimSpace(f.PaidDate) == "" {
		return fmt.Errorf("taxfiling: 状态是「%s」，就要填缴款日期", StatusPaid.Label())
	}
	if f.Paid.IsNegative() {
		return fmt.Errorf("taxfiling: 已缴税额不能是负数")
	}
	// 金额勾稽：税额 + 附加 − 已缴 = 应补(退)税额
	if f.TaxAmount.Add(f.Surcharge).Sub(f.Paid) != f.Payable {
		return fmt.Errorf("taxfiling: 税额 %s + 附加税费 %s − 已缴 %s ≠ 应补(退)税额 %s —— "+
			"四个数要能对上，否则台账自己就是错的",
			f.TaxAmount, f.Surcharge, f.Paid, f.Payable)
	}
	return nil
}

// Reconcile 把台账里记的应补(退)税额与**现在算出来的**数比一比。
//
// 差异不是错误，而是一件要处理的事：账后来改了（新凭证、补录、调整），
// 于是现在的表与当时申报的数不一致。要不要更正申报，由办税人判断，
// 但软件必须先把差异摆出来 —— 不摆出来的话，谁也不会注意到。
func (f Filing) Reconcile(computed money.Money) (diff money.Money, msg string) {
	if f.Status == StatusVoid {
		return 0, "这条记录已作废，不参与勾稽。"
	}
	diff = computed.Sub(f.Payable)
	switch {
	case diff.IsZero():
		return 0, fmt.Sprintf("与当前计算表一致（%s）。", computed)
	case diff.IsPositive():
		return diff, fmt.Sprintf(
			"★ 按现在的账算，应补(退)税额是 %s，比当时申报的 %s **多 %s** —— "+
				"差额多半来自申报之后补录或调整的凭证。要不要更正申报由办税人判断，"+
				"也可以在下一期一并调整。", computed, f.Payable, diff)
	default:
		return diff, fmt.Sprintf(
			"★ 按现在的账算，应补(退)税额是 %s，比当时申报的 %s **少 %s** —— "+
				"请确认是账改了还是当时报多了；多缴部分一般留抵或申请退税。",
			computed, f.Payable, diff.Abs())
	}
}

// Summary 返回一句话（列表用）。
func (f Filing) Summary() string {
	base := fmt.Sprintf("%s %s %s", f.Kind.Label(), f.Period().String(), f.Status.Label())
	if !f.Payable.IsZero() {
		base += fmt.Sprintf("，应补(退) %s", f.Payable)
	}
	if f.FiledDate != "" {
		base += "，申报于 " + f.FiledDate
	}
	return base
}

// KindOption 是税种选项。
type KindOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// KindOptions 返回三种税的选项。
func KindOptions() []KindOption {
	out := make([]KindOption, 0, len(taxreturn.AllKinds))
	for _, k := range taxreturn.AllKinds {
		out = append(out, KindOption{Value: string(k), Label: k.Label()})
	}
	return out
}

// StatusOption 是状态选项。
type StatusOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	// Hint 说明这种状态什么时候用。
	Hint string `json:"hint"`
}

// StatusOptions 返回可选状态（不含「已作废」：作废走作废操作）。
func StatusOptions() []StatusOption {
	return []StatusOption{
		{Value: string(StatusFiled), Label: StatusFiled.Label(),
			Hint: "已经在电子税务局完成申报（应补税额为 0 或尚未缴纳）"},
		{Value: string(StatusPaid), Label: StatusPaid.Label(),
			Hint: "已申报并且已完成缴款（要填缴款日期）"},
	}
}

// ---------------------------------------------------------------------------
// 错误
// ---------------------------------------------------------------------------

// 台账的错误。
var (
	ErrBadKind   = fmt.Errorf("taxfiling: 不认识的税种")
	ErrBadPeriod = fmt.Errorf("taxfiling: 属期非法")
	ErrBadStatus = fmt.Errorf("taxfiling: 不认识的状态")
)
