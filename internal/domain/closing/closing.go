// Package closing 实现期末结转损益与年末利润结转。
//
// # 为什么必须有这一步
//
// 《小企业会计准则》要求损益类科目「年度终了，应将本科目的余额转入
// 『本年利润』科目，结转后本科目应无余额」。
//
// 不做结转会有三个后果：
//
//  1. 损益类科目余额逐月累积，明细账看不出「本月发生了什么」
//  2. 年末资产负债表与利润表无法对齐
//  3. 下一年的损益类科目带着上年的余额开始，数字全错
//
// # 结转金额取「余额」而不是「发生额」
//
// 结转时取该科目在结转日的余额（借−贷）。因为结账是顺序的，
// 上期已结账时该余额恰好等于本月发生额；若上期未结，则会一次性
// 把累积的余额转过去 —— 这正是「补结转」应有的行为。
package closing

import (
	"errors"
	"fmt"
	"sort"
	"strconv"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

// 结转相关错误。
var (
	ErrNoPnLAccount = errors.New("closing: 没有需要结转的损益类科目")
	ErrNotBalanced  = errors.New("closing: 结转分录借贷不平")
	ErrSameAccount  = errors.New("closing: 本年利润与利润分配科目不能相同")
	ErrBadPeriod    = errors.New("closing: 会计期间非法")
)

// PnLAccount 是一个损益类科目在结转日的余额。
type PnLAccount struct {
	Code string
	Name string
	// RootType 必须是 income 或 expense。
	RootType account.RootType
	// Raw 是原始净额（借 − 贷）。
	//
	//	收入类正常为负（贷方余额），费用类正常为正（借方余额）。
	//	用原始口径而不是「自然方向为正」，是因为结转方向要由借贷决定，
	//	而不是由科目属性决定。
	Raw money.Money
	// Aux 是该科目在结转日**唯一**的辅助核算组合（部门/项目/往来…）。
	//
	// 结转分录必须继承辅助核算，否则：
	//   - 要求「部门」的科目（如管理费用）会因缺辅助核算而无法过账
	//   - 部门费用表在结转当月会凭空多出一笔没有部门的费用
	//
	// 同一科目若存在多种辅助核算组合，调用方应按组合拆成多条 PnLAccount，
	// 而不是把它们合并成一条 —— 合并即丢失维度。
	Aux ledger.Aux
}

// AuxKey 返回辅助核算的可比较键，用于分组与排序。
func (a PnLAccount) AuxKey() string {
	return auxKey(a.Aux)
}

func auxKey(x ledger.Aux) string {
	return fmt.Sprintf("%s|%s|%s|%s",
		idStr(x.ContactID), idStr(x.EmployeeID), idStr(x.DeptID), idStr(x.ProjectID))
}

func idStr(p *int64) string {
	if p == nil {
		return "-"
	}
	return strconv.FormatInt(*p, 10)
}

// AbsAmount 返回该科目的结转金额（恒为正）。
func (a PnLAccount) AbsAmount() money.Money { return a.Raw.Abs() }

// Direction 返回结转分录的方向。
//
//	收入类（贷方余额）→ 借记该科目，把余额冲平
//	费用类（借方余额）→ 贷记该科目，把余额冲平
//	余额方向反转时（如红字冲销后）→ 方向也随之反转
func (a PnLAccount) Direction() string {
	if a.Raw.IsNegative() {
		return "debit"
	}
	if a.Raw.IsPositive() {
		return "credit"
	}
	return ""
}

// Entry 是结转凭证的一条分录。
type Entry struct {
	AccountCode string
	Summary     string
	Debit       money.Money
	Credit      money.Money
	// Aux 继承自被结转科目的辅助核算组合。
	Aux ledger.Aux
}

// Accounts 是结转所需的科目配置。
type Accounts struct {
	// Profit 是「本年利润」（3103）。
	Profit string
	// RetainedEarnings 是「利润分配—未分配利润」（310401）。
	RetainedEarnings string
}

// DefaultAccounts 返回按本项目预置科目表的默认配置。
func DefaultAccounts() Accounts {
	return Accounts{Profit: "3103", RetainedEarnings: "310401"}
}

// Result 是一次结转的计算结果。
type Result struct {
	// Entries 是结转凭证的分录。
	Entries []Entry
	// TotalIncome 是收入类结转金额。
	TotalIncome money.Money
	// TotalExpense 是费用类结转金额。
	TotalExpense money.Money
	// Profit 是本期利润（正为盈利，负为亏损）。
	Profit money.Money
	// AccountCount 是参与结转的科目数。
	AccountCount int
}

// BuildEntries 生成**结转损益**凭证的分录。
//
//	借：各收入类科目          余额
//	  贷：本年利润            收入合计 − 费用合计
//	借：本年利润              收入合计 − 费用合计
//	  贷：各费用类科目          余额
//
// 合并成一张凭证（而不是收支两张）：这样「本年利润」科目在凭证上
// 只有一条净额分录，一眼就能看出本期是盈是亏。
//
// 结转后所有损益类科目余额归零 —— 这是准则的硬要求。
func BuildEntries(p period.Key, accounts []PnLAccount, ac Accounts,
	summary string) (*Result, error) {

	if !p.Valid() {
		return nil, fmt.Errorf("%w: %v", ErrBadPeriod, p)
	}
	if ac.Profit == "" {
		return nil, errors.New("closing: 未指定本年利润科目")
	}

	// 只处理余额非零的科目：余额为零的科目转了也白转，
	// 在凭证上留一行 0.00 只会干扰阅读。
	active := make([]PnLAccount, 0, len(accounts))
	for _, a := range accounts {
		if a.Raw.IsZero() {
			continue
		}
		if a.RootType != account.RootIncome && a.RootType != account.RootExpense {
			return nil, fmt.Errorf("closing: 科目 %s(%s) 不是损益类",
				a.Name, a.Code)
		}
		active = append(active, a)
	}
	if len(active) == 0 {
		return nil, ErrNoPnLAccount
	}
	// 排序键带上辅助核算：同一科目的多条组合要按稳定的顺序排列，
	// 否则同样输入会生成行序不同的凭证，测试与对账都没法比。
	sort.Slice(active, func(i, j int) bool {
		if active[i].Code != active[j].Code {
			return active[i].Code < active[j].Code
		}
		return active[i].AuxKey() < active[j].AuxKey()
	})

	if summary == "" {
		summary = fmt.Sprintf("结转损益 %d年%02d月", p.Year, p.Month)
	}

	// 收入/费用合计用**带符号**的求和，不能用绝对值。
	//
	// 原因：红字冲销可能让某个收入科目出现借方余额（raw > 0），
	// 此时它对收入的贡献是**负的**（相当于冲减收入）。
	// 若用绝对值，合计会虚增，利润算错，而且生成的凭证借贷不平。
	//
	//	收入类贡献 = −raw（正常为贷方余额 raw<0，贡献为正）
	//	费用类贡献 = +raw（正常为借方余额 raw>0，贡献为正）
	res := &Result{AccountCount: len(active)}
	for _, a := range active {
		if a.RootType == account.RootIncome {
			res.TotalIncome = res.TotalIncome.Sub(a.Raw)
		} else {
			res.TotalExpense = res.TotalExpense.Add(a.Raw)
		}
	}
	res.Profit = res.TotalIncome.Sub(res.TotalExpense)

	// 各损益科目的冲平分录（逐条继承辅助核算）
	for _, a := range active {
		e := Entry{AccountCode: a.Code, Summary: summary, Aux: a.Aux}
		switch a.Direction() {
		case "debit":
			e.Debit = a.AbsAmount()
		case "credit":
			e.Credit = a.AbsAmount()
		default:
			continue
		}
		res.Entries = append(res.Entries, e)
	}

	// 本年利润的净额分录
	if !res.Profit.IsZero() {
		e := Entry{AccountCode: ac.Profit, Summary: summary}
		if res.Profit.IsPositive() {
			// 盈利：贷本年利润
			e.Credit = res.Profit
		} else {
			// 亏损：借本年利润
			e.Debit = res.Profit.Abs()
		}
		res.Entries = append(res.Entries, e)
	}

	// 自检：借贷必须相等
	var d, c money.Money
	for _, e := range res.Entries {
		d, c = d.Add(e.Debit), c.Add(e.Credit)
	}
	if d != c {
		return nil, fmt.Errorf("%w: 借 %s ≠ 贷 %s（收入 %s，费用 %s）",
			ErrNotBalanced, d, c, res.TotalIncome, res.TotalExpense)
	}
	return res, nil
}

// BuildYearEndEntries 生成**年末**把「本年利润」转入「利润分配—未分配利润」的分录。
//
//	盈利：借 本年利润 / 贷 利润分配—未分配利润
//	亏损：借 利润分配—未分配利润 / 贷 本年利润
//
// 这一步只在年度最后一个期间结账时做。
func BuildYearEndEntries(p period.Key, profitBalance money.Money,
	ac Accounts, summary string) ([]Entry, error) {

	if !p.Valid() {
		return nil, fmt.Errorf("%w: %v", ErrBadPeriod, p)
	}
	if ac.Profit == "" || ac.RetainedEarnings == "" {
		return nil, errors.New("closing: 未指定本年利润或利润分配科目")
	}
	if ac.Profit == ac.RetainedEarnings {
		return nil, ErrSameAccount
	}
	if profitBalance.IsZero() {
		return nil, nil // 无利润可转
	}
	if summary == "" {
		summary = fmt.Sprintf("结转本年利润 %d年", p.Year)
	}

	// profitBalance 是「本年利润」科目的原始净额（借−贷）。
	// 盈利时该科目为贷方余额（负），因此这里取绝对值后按方向生成。
	var entries []Entry
	if profitBalance.IsNegative() {
		// 贷方余额 = 盈利 → 借本年利润 / 贷未分配利润
		amount := profitBalance.Abs()
		entries = []Entry{
			{AccountCode: ac.Profit, Summary: summary, Debit: amount},
			{AccountCode: ac.RetainedEarnings, Summary: summary, Credit: amount},
		}
	} else {
		// 借方余额 = 亏损 → 借未分配利润 / 贷本年利润
		amount := profitBalance
		entries = []Entry{
			{AccountCode: ac.RetainedEarnings, Summary: summary, Debit: amount},
			{AccountCode: ac.Profit, Summary: summary, Credit: amount},
		}
	}
	return entries, nil
}

// IsYearEnd 报告某期间是否为年度最后一个期间（12 月）。
//
// 中国《会计法》规定会计年度为公历 1 月 1 日至 12 月 31 日，
// 因此年度结转固定在 12 月。
func IsYearEnd(p period.Key) bool { return p.Month == 12 }

// ---------------------------------------------------------------------------
// 结账流程
// ---------------------------------------------------------------------------

// Step 是结账流程中的一步。
type Step struct {
	Key    string
	Title  string
	Detail string
	Done   bool
	// Skipped 为真表示该步不适用（如非年末不需要结转本年利润）。
	Skipped bool
}

// Plan 是一次结账的完整计划。
type Plan struct {
	Period period.Key

	// Closing 是结转损益的结果；无损益可转时为 nil。
	Closing *Result
	// ClosingEntries 是最终要写入凭证的分录
	//（结转损益 + 年末利润结转）。
	ClosingEntries []Entry

	// YearEnd 为真表示这是年度最后一个期间，需要结转本年利润。
	YearEnd bool

	// Steps 是流程说明，供界面展示「结账做了什么」。
	Steps []Step
}

// BuildPlan 构造一个期间的结账计划。
//
// pnl 是该期间末各损益类科目的余额；
// profitBalance 是「本年利润」科目在**本次结转之前**的余额。
//
// ★ 为什么是「结转之前」：年末要把本年利润转入未分配利润，
// 而本年利润的最终金额只有在本期损益结转之后才成立。
// 让调用方自己加这一次，等于把最容易算错的一步交给每个调用点；
// 放在这里一次算对，调用方只需如实传账上的当前余额。
//
// 本函数只做计算，不碰数据库 —— 落库与状态迁移由 service/store 层负责，
// 事务边界由它掌握。
func BuildPlan(p period.Key, pnl []PnLAccount, profitBalance money.Money,
	ac Accounts) (*Plan, error) {

	plan := &Plan{Period: p, YearEnd: IsYearEnd(p)}

	// 第一步：结转损益
	res, err := BuildEntries(p, pnl, ac, "")
	switch {
	case errors.Is(err, ErrNoPnLAccount):
		// 本期没有任何损益发生 —— 这不一定是错（如停业期间），
		// 记录为「跳过」而不是失败。
		plan.Steps = append(plan.Steps, Step{
			Key: "close_pnl", Title: "结转损益", Skipped: true,
			Detail: "本期无损益发生，无需结转",
		})
	default:
		if err != nil {
			return nil, err
		}
		plan.Closing = res
		plan.ClosingEntries = append(plan.ClosingEntries, res.Entries...)
		plan.Steps = append(plan.Steps, Step{
			Key: "close_pnl", Title: "结转损益", Done: true,
			Detail: fmt.Sprintf("收入 %s，费用 %s，%s %s（%d 个科目）",
				res.TotalIncome, res.TotalExpense,
				profitWord(res.Profit), res.Profit.Abs(), res.AccountCount),
		})
	}

	// 第二步：年末结转本年利润
	if plan.YearEnd {
		// 本期损益结转后，本年利润的余额 = 结转前余额 − 本期利润。
		//
		// 符号：本年利润 raw = 借 − 贷。盈利是贷方发生，raw 因此**变小**。
		//   结转前 0，本期利润 65,000 → 结转后 raw = −65,000（贷方 65,000）
		//   若为亏损 −30,000，则 raw = +30,000（借方 30,000）
		afterClosing := profitBalance
		if plan.Closing != nil {
			afterClosing = afterClosing.Sub(plan.Closing.Profit)
		}
		ye, err := BuildYearEndEntries(p, afterClosing, ac, "")
		if err != nil {
			return nil, err
		}
		if len(ye) == 0 {
			plan.Steps = append(plan.Steps, Step{
				Key: "close_year", Title: "结转本年利润", Skipped: true,
				Detail: "本年利润余额为零，无需结转",
			})
		} else {
			plan.ClosingEntries = append(plan.ClosingEntries, ye...)
			plan.Steps = append(plan.Steps, Step{
				Key: "close_year", Title: "结转本年利润", Done: true,
				Detail: fmt.Sprintf("将本年利润 %s 转入未分配利润", afterClosing.Abs()),
			})
		}
	} else {
		plan.Steps = append(plan.Steps, Step{
			Key: "close_year", Title: "结转本年利润", Skipped: true,
			Detail: "非年度末期间，年末结账时处理",
		})
	}

	return plan, nil
}

// Validate 校验结账计划的所有分录借贷平衡。
func (p *Plan) Validate() error {
	var d, c money.Money
	for _, e := range p.ClosingEntries {
		d, c = d.Add(e.Debit), c.Add(e.Credit)
	}
	if d != c {
		return fmt.Errorf("%w: 借 %s ≠ 贷 %s", ErrNotBalanced, d, c)
	}
	// 每条分录只能有一个方向
	for i, e := range p.ClosingEntries {
		if e.Debit.IsPositive() && e.Credit.IsPositive() {
			return fmt.Errorf("closing: 第 %d 条分录同时有借有贷", i+1)
		}
		if e.Debit.IsZero() && e.Credit.IsZero() {
			return fmt.Errorf("closing: 第 %d 条分录金额为零", i+1)
		}
		if e.Debit.IsNegative() || e.Credit.IsNegative() {
			return fmt.Errorf("closing: 第 %d 条分录金额为负", i+1)
		}
	}
	return nil
}

// HasEntries 报告是否有需要写入的分录。
func (p *Plan) HasEntries() bool { return len(p.ClosingEntries) > 0 }

func profitWord(v money.Money) string {
	if v.IsNegative() {
		return "亏损"
	}
	if v.IsPositive() {
		return "利润"
	}
	return "持平"
}

// Summary 返回一句话总结，便于界面展示。
func (p *Plan) Summary() string {
	if p.Closing == nil {
		return fmt.Sprintf("%d年%02d月无损益发生", p.Period.Year, p.Period.Month)
	}
	return fmt.Sprintf("%d年%02d月：收入 %s，费用 %s，%s %s",
		p.Period.Year, p.Period.Month,
		p.Closing.TotalIncome, p.Closing.TotalExpense,
		profitWord(p.Closing.Profit), p.Closing.Profit.Abs())
}

// ReversePlan 生成冲销结账所需的信息。
//
// 反结账时必须先冲销该期生成的结转凭证 ——
// 否则损益类科目会残留已结转的状态，利润表与资产负债表都对不上。
func ReversePlan(closingVoucherNo string, p period.Key) Step {
	return Step{
		Key: "reverse_closing", Title: "冲销结转凭证", Done: true,
		Detail: fmt.Sprintf("红字冲销 %s（%d年%02d月的结转凭证）",
			closingVoucherNo, p.Year, p.Month),
	}
}

// Date 返回该期间的最后一个日历日，作为结转凭证的记账日期。
func Date(p period.Key) calendar.Date {
	d, _ := calendar.New(p.Year, p.Month, calendar.DaysInMonth(p.Year, p.Month))
	return d
}
