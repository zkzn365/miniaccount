// Package ledger 实现复式记账的过账内核。
//
// # 设计要点
//
// Posting 是一个「过账构造器」：调用方声明要借/贷哪些科目、多少钱，
// Validate 一次性检查全部记账不变式，通过后才允许落库。
//
// 与 Frappe Books 的 LedgerPosting 相比，本实现做了三处关键加强：
//
//  1. **校验从 1 条扩到 7 条**。Frappe 只检查「借贷相等」，
//     因此可以记到汇总科目、可以记到已停用科目、可以漏填辅助核算。
//  2. **不合并同科目分录**。Frappe 把同科目同方向的多行合并成一行，
//     摘要与辅助核算随之丢失；中国实务的明细账需要逐行摘要，
//     因此本实现保留每一行。
//  3. **红字冲销用「借贷互换」而非负数金额**。这样所有金额始终非负，
//     与数据库的 CHECK (debit >= 0 AND credit >= 0) 约束一致，
//     也避免了 Frappe 里「金额可能为负」导致各报表对负数处理不一致的问题。
package ledger

import (
	"errors"
	"fmt"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

// 过账错误。
var (
	ErrNoEntries      = errors.New("ledger: 凭证没有分录")
	ErrTooFewEntries  = errors.New("ledger: 凭证至少需要两条分录")
	ErrNotBalanced    = errors.New("ledger: 借贷不平衡")
	ErrBothSides      = errors.New("ledger: 单条分录不能同时有借方和贷方金额")
	ErrNoAmount       = errors.New("ledger: 单条分录必须有一个方向的金额")
	ErrNegativeAmount = errors.New("ledger: 金额不能为负，红字冲销请使用 Reverse")
	ErrZeroAmount     = errors.New("ledger: 金额不能为零")
	ErrMissingSummary = errors.New("ledger: 分录摘要不能为空")
	ErrMissingAux     = errors.New("ledger: 缺少必需的辅助核算")
	ErrAccountMissing = errors.New("ledger: 科目不存在")
	ErrWrongAuxKind   = errors.New("ledger: 辅助核算类型与科目要求不符")
)

// Aux 是一条分录上的辅助核算维度。
//
// 客户/供应商/股东/其他单位都落在 ContactID 上（contact 表用 kind 区分），
// 这样往来账只需要按 contact_id 聚合，无需区分四种表。
type Aux struct {
	ContactID  *int64
	EmployeeID *int64
	DeptID     *int64
	ProjectID  *int64
}

// IsZero 报告是否未填写任何维度。
func (a Aux) IsZero() bool {
	return a.ContactID == nil && a.EmployeeID == nil && a.DeptID == nil && a.ProjectID == nil
}

// Entry 是一条分录。
type Entry struct {
	// AccountCode 是科目编码；AccountID 由 service 层在落库时回填。
	AccountCode string
	AccountID   int64

	// Summary 是本行摘要。中国实务要求逐行摘要，不允许为空。
	Summary string

	// Debit / Credit 恰好有一个大于零，另一个为零。两者都非负。
	Debit  money.Money
	Credit money.Money

	Aux Aux
}

// Amount 返回带符号金额：借方为正，贷方为负。
// 用它累加即可直接判断平衡（和为 0 即平衡）。
func (e Entry) Amount() money.Money {
	return e.Debit.Sub(e.Credit)
}

// Direction 返回分录方向；金额为零时返回空串。
func (e Entry) Direction() string {
	switch {
	case e.Debit.IsPositive():
		return "debit"
	case e.Credit.IsPositive():
		return "credit"
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Posting
// ---------------------------------------------------------------------------

// Posting 是一张凭证的过账计划。
//
// 零值不可用，必须用 NewPosting 构造。
type Posting struct {
	date    calendar.Date
	entries []Entry
}

// NewPosting 以给定记账日期创建过账计划。
func NewPosting(date calendar.Date) *Posting {
	return &Posting{date: date}
}

// Date 返回记账日期。
func (p *Posting) Date() calendar.Date { return p.date }

// Entries 返回全部分录（按加入顺序）。
func (p *Posting) Entries() []Entry { return p.entries }

// Len 返回分录条数。
func (p *Posting) Len() int { return len(p.entries) }

// Add 追加一条完整分录。
func (p *Posting) Add(e Entry) *Posting {
	p.entries = append(p.entries, e)
	return p
}

// Debit 追加一条借方分录。
func (p *Posting) Debit(accountCode string, amt money.Money) *Posting {
	return p.Add(Entry{AccountCode: accountCode, Debit: amt})
}

// Credit 追加一条贷方分录。
func (p *Posting) Credit(accountCode string, amt money.Money) *Posting {
	return p.Add(Entry{AccountCode: accountCode, Credit: amt})
}

// DebitWith 追加一条带摘要与辅助核算的借方分录。
func (p *Posting) DebitWith(accountCode, summary string, amt money.Money, aux Aux) *Posting {
	return p.Add(Entry{AccountCode: accountCode, Summary: summary, Debit: amt, Aux: aux})
}

// CreditWith 追加一条带摘要与辅助核算的贷方分录。
func (p *Posting) CreditWith(accountCode, summary string, amt money.Money, aux Aux) *Posting {
	return p.Add(Entry{AccountCode: accountCode, Summary: summary, Credit: amt, Aux: aux})
}

// SetSummary 把全部空摘要的行统一设为 s，便于「一借一贷同摘要」的常见场景。
func (p *Posting) SetSummary(s string) *Posting {
	for i := range p.entries {
		if p.entries[i].Summary == "" {
			p.entries[i].Summary = s
		}
	}
	return p
}

// TotalDebit 返回借方合计。
func (p *Posting) TotalDebit() money.Money {
	var t money.Money
	for _, e := range p.entries {
		t = t.Add(e.Debit)
	}
	return t
}

// TotalCredit 返回贷方合计。
func (p *Posting) TotalCredit() money.Money {
	var t money.Money
	for _, e := range p.entries {
		t = t.Add(e.Credit)
	}
	return t
}

// Difference 返回 借 − 贷。为零即平衡。
func (p *Posting) Difference() money.Money {
	return p.TotalDebit().Sub(p.TotalCredit())
}

// IsBalanced 报告借贷是否相等（精确到分）。
func (p *Posting) IsBalanced() bool { return p.Difference().IsZero() }

// Reverse 返回一张借贷互换的红字冲销计划。
//
// 用于「取消凭证」与「更正凭证」。原凭证与冲销凭证都保留，
// 形成完整审计轨迹 —— 与直接删除总账分录相比，
// 这既是《会计法》的要求，也让「这笔钱去哪了」永远可追溯。
func (p *Posting) Reverse() *Posting {
	r := NewPosting(p.date)
	for _, e := range p.entries {
		ne := e
		ne.Debit, ne.Credit = e.Credit, e.Debit
		r.entries = append(r.entries, ne)
	}
	return r
}

// ---------------------------------------------------------------------------
// 校验上下文
// ---------------------------------------------------------------------------

// Context 提供过账校验所需的外部事实。
//
// 刻意只依赖两个领域对象而非数据库句柄：这样 Posting 的校验
// 可以完全用内存中的科目树与期间表做单元测试，不需要起数据库。
type Context struct {
	Accounts *account.Tree
	Periods  *period.Calendar

	// ContactKinds 把 contact_id 映射到其 kind（customer/supplier/employee/shareholder/other）。
	// 为 nil 时跳过「辅助核算类型匹配」这一项校验。
	ContactKinds map[int64]string
}

// ---------------------------------------------------------------------------
// 校验：7 条不变式
// ---------------------------------------------------------------------------

// Validate 检查全部记账不变式，通过则返回该凭证所属的会计期间。
//
// 7 条不变式：
//
//  1. 分录数 ≥ 2
//  2. 每条分录的借/贷恰有一个 > 0，另一个 = 0，且都非负
//  3. 每条分录金额 > 0
//  4. 每条分录摘要非空
//  5. Σ借 == Σ贷（精确到分）
//  6. 科目存在、是明细科目、已启用；日期所在期间为 open
//  7. 科目声明的辅助核算维度必须全部填写，且类型匹配
func (p *Posting) Validate(ctx *Context) (*period.Period, error) {
	return p.validate(ctx, true)
}

// ValidateDraft 校验一张**草稿**是否已经成形，但不要求期间已启用。
//
// 与 Validate 的唯一区别是第 6 条（期间必须 open）：
//
//	草稿是「还没想好的输入」，用户完全可能先按记忆记下上个月的一笔业务，
//	过几天才去补结账。此时拦住他保存草稿毫无意义 —— 真正需要拦截的
//	是**过账**那一刻，而那一刻本来就会再校验一次。
//
// 其余六条（分录数、金额方向、摘要、借贷平衡、科目可记账、辅助核算齐全）
// 照常检查：这些是「这张凭证写对了没有」，与期间无关，越早发现越好。
func (p *Posting) ValidateDraft(ctx *Context) error {
	_, err := p.validate(ctx, false)
	return err
}

func (p *Posting) validate(ctx *Context, requireOpenPeriod bool) (*period.Period, error) {
	if ctx == nil || ctx.Accounts == nil || ctx.Periods == nil {
		return nil, errors.New("ledger: Validate 需要科目树与期间表")
	}

	// 1. 分录数
	if len(p.entries) == 0 {
		return nil, ErrNoEntries
	}
	if len(p.entries) < 2 {
		return nil, fmt.Errorf("%w（当前 %d 条）", ErrTooFewEntries, len(p.entries))
	}

	// 2、3、4、6、7 逐条检查
	for i, e := range p.entries {
		where := fmt.Sprintf("第 %d 行", i+1)
		if e.AccountCode != "" {
			where = fmt.Sprintf("第 %d 行(%s)", i+1, e.AccountCode)
		}

		if e.Debit.IsNegative() || e.Credit.IsNegative() {
			return nil, fmt.Errorf("%w：%s", ErrNegativeAmount, where)
		}
		switch {
		case e.Debit.IsPositive() && e.Credit.IsPositive():
			return nil, fmt.Errorf("%w：%s", ErrBothSides, where)
		case e.Debit.IsZero() && e.Credit.IsZero():
			return nil, fmt.Errorf("%w：%s", ErrNoAmount, where)
		}

		if e.Summary == "" {
			return nil, fmt.Errorf("%w：%s", ErrMissingSummary, where)
		}

		acc, ok := ctx.Accounts.Get(e.AccountCode)
		if !ok {
			return nil, fmt.Errorf("%w: %q（%s）", ErrAccountMissing, e.AccountCode, where)
		}
		if err := ctx.Accounts.CheckPostable(e.AccountCode); err != nil {
			return nil, fmt.Errorf("%s：%w", where, err)
		}
		if err := checkAux(acc, e, ctx); err != nil {
			return nil, fmt.Errorf("%s：%w", where, err)
		}
	}

	// 5. 借贷平衡
	if !p.IsBalanced() {
		return nil, fmt.Errorf("%w：借方合计 %s，贷方合计 %s，差额 %s",
			ErrNotBalanced, p.TotalDebit(), p.TotalCredit(), p.Difference())
	}

	// 6. 期间
	if !requireOpenPeriod {
		// 草稿：只要日期落在一个存在的期间里即可（用于回填期间字段）
		per, ok := ctx.Periods.PeriodOf(p.date)
		if !ok {
			return nil, fmt.Errorf("%w：%s", period.ErrPeriodNotFound, p.date)
		}
		return per, nil
	}
	per, err := ctx.Periods.CheckPostable(p.date)
	if err != nil {
		return nil, err
	}
	return per, nil
}

// checkAux 校验科目要求的辅助核算维度是否齐全、类型是否匹配。
//
// 这是 Frappe Books 完全没有的校验。缺了它，「其他应付款—股东」
// 这类科目就可能在没有股东的情况下被记账，往来账立刻失去意义。
func checkAux(acc *account.Account, e Entry, ctx *Context) error {
	for _, t := range acc.AuxTypes {
		switch t {
		case account.AuxCustomer, account.AuxSupplier, account.AuxShareholder, account.AuxOther:
			if e.Aux.ContactID == nil {
				return fmt.Errorf("%w：科目 %s(%s) 要求「%s」，但未填写往来单位",
					ErrMissingAux, acc.Name, acc.Code, t.Label())
			}
			if ctx.ContactKinds != nil {
				kind, ok := ctx.ContactKinds[*e.Aux.ContactID]
				if ok && !auxMatchesContact(t, kind) {
					return fmt.Errorf("%w：科目 %s 要求「%s」，但所选往来单位类型为 %q",
						ErrWrongAuxKind, acc.Code, t.Label(), kind)
				}
			}
		case account.AuxEmployee:
			if e.Aux.EmployeeID == nil && e.Aux.ContactID == nil {
				return fmt.Errorf("%w：科目 %s(%s) 要求「员工」",
					ErrMissingAux, acc.Name, acc.Code)
			}
		case account.AuxDept:
			if e.Aux.DeptID == nil {
				return fmt.Errorf("%w：科目 %s(%s) 要求「部门」",
					ErrMissingAux, acc.Name, acc.Code)
			}
		case account.AuxProject:
			if e.Aux.ProjectID == nil {
				return fmt.Errorf("%w：科目 %s(%s) 要求「项目」",
					ErrMissingAux, acc.Name, acc.Code)
			}
		}
	}
	return nil
}

// auxMatchesContact 判断辅助核算维度与往来单位类型是否相容。
func auxMatchesContact(t account.AuxType, kind string) bool {
	switch t {
	case account.AuxCustomer:
		return kind == "customer" || kind == "both"
	case account.AuxSupplier:
		return kind == "supplier" || kind == "both"
	case account.AuxShareholder:
		return kind == "shareholder"
	case account.AuxOther:
		return true // 「其他单位」接受任何类型
	default:
		return true
	}
}

// ---------------------------------------------------------------------------
// 尾差
// ---------------------------------------------------------------------------

// EnsureBalance 在借贷不平衡且差额不超过 limit 时，向指定科目补一条分录，
// 使凭证平衡；返回补入的金额（零表示本来就平衡）。
//
// 会计场景：价税合计因分别四舍五入产生 1~2 分的尾差。
// 用「财务费用—四舍五入」科目吸收，是实务通行做法。
//
// 注意：**超过 limit 时不做任何修改并返回错误**。
// 大额不平衡一定是分录本身有问题，必须暴露给用户，
// 绝不能用一个尾差科目把错误掩盖掉 —— 这正是 Frappe Books
// 的 makeRoundOffEntry 无上限自动调平所带来的风险。
func (p *Posting) EnsureBalance(accountCode string, limit money.Money) (money.Money, error) {
	diff := p.Difference()
	if diff.IsZero() {
		return 0, nil
	}
	if diff.Abs() > limit.Abs() {
		return 0, fmt.Errorf("%w：差额 %s 超过允许的尾差 %s，请检查分录",
			ErrNotBalanced, diff, limit.Abs())
	}
	summary := "尾差调整"
	if diff.IsPositive() {
		// 借方多 → 补贷方
		p.entries = append(p.entries, Entry{
			AccountCode: accountCode, Summary: summary, Credit: diff.Abs(),
		})
	} else {
		p.entries = append(p.entries, Entry{
			AccountCode: accountCode, Summary: summary, Debit: diff.Abs(),
		})
	}
	return diff.Abs(), nil
}
