// Package bank 实现银行流水导入与匹配。
//
// # 三层降级匹配
//
// 小微企业 70~90% 的银行流水是重复模式（同一供应商货款、同一股东往来、
// 每月工资、每月房租）。这些用规则和历史就能 100% 准确命中，
// **不该花 AI token，也不该引入不确定性**。AI 只处理剩下的长尾。
//
//	第 1 层  规则引擎    用户配置的 bank_rule，零成本、确定性
//	第 2 层  历史相似度  从用户自己的历史凭证里检索相似分录
//	第 3 层  AI Agent    前两层都没命中时才调用（后续阶段实现）
//
// 预期覆盖率：规则 + 历史 ≥ 80%，AI 处理 < 20%。
//
// # 记账方向
//
// 规则只需要指定**对方科目**，银行科目由流水本身的账户决定：
//
//	收入（钱进来）  借 银行存款  贷 对方科目
//	支出（钱出去）  借 对方科目  贷 银行存款
//
// 这样规则写起来直观（「看到张三就记其他应付款—股东」），
// 也不会因为写错方向而把账做反。
package bank

import (
	"errors"
	"fmt"
	"strings"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// Direction 是资金流向。
type Direction string

// 资金流向。
const (
	DirIn  Direction = "in"  // 收入（钱进来）
	DirOut Direction = "out" // 支出（钱出去）
)

// Label 返回中文名。
func (d Direction) Label() string {
	switch d {
	case DirIn:
		return "收入"
	case DirOut:
		return "支出"
	default:
		return string(d)
	}
}

// Valid 报告方向是否合法。
func (d Direction) Valid() bool { return d == DirIn || d == DirOut }

// Status 是流水的处理状态。
type Status string

// 流水状态。
const (
	StatusImported Status = "imported" // 刚导入，尚未匹配
	StatusMatched  Status = "matched"  // 有匹配提议，待人确认
	StatusPosted   Status = "posted"   // 已生成凭证
	StatusIgnored  Status = "ignored"  // 人工标记为不记账
)

// Label 返回中文名。
func (s Status) Label() string {
	switch s {
	case StatusImported:
		return "待匹配"
	case StatusMatched:
		return "待确认"
	case StatusPosted:
		return "已记账"
	case StatusIgnored:
		return "已忽略"
	default:
		return string(s)
	}
}

// Layer 标识匹配结果的来源。记录它是为了统计「规则/历史/AI 各覆盖多少」，
// 从而知道该扩充规则还是该调模型。
type Layer string

// 匹配层。
const (
	LayerRule    Layer = "rule"    // 规则命中
	LayerHistory Layer = "history" // 历史相似
	LayerAI      Layer = "ai"      // 模型提议
	LayerManual  Layer = "manual"  // 人工指定
)

// Label 返回中文名。
func (l Layer) Label() string {
	switch l {
	case LayerRule:
		return "规则"
	case LayerHistory:
		return "历史"
	case LayerAI:
		return "AI"
	case LayerManual:
		return "人工"
	default:
		return string(l)
	}
}

// 流水相关错误。
var (
	ErrBadDirection   = errors.New("bank: 资金流向非法")
	ErrBadAmount      = errors.New("bank: 金额必须为正")
	ErrNoCounterparty = errors.New("bank: 缺少匹配依据")
	ErrAlreadyPosted  = errors.New("bank: 流水已生成凭证")
	ErrNotMatched     = errors.New("bank: 流水尚未匹配，无法生成凭证")
	ErrNoMatched      = ErrNotMatched // 别名：保留语义化的名字供 BuildEntries 使用
	ErrDuplicate      = errors.New("bank: 流水重复")
)

// ---------------------------------------------------------------------------
// Flow
// ---------------------------------------------------------------------------

// Flow 是一条银行流水。
type Flow struct {
	ID       int64
	ImportID int64

	// AccountCode 是该流水所属的银行科目（如 100201 银行存款—工商银行）。
	AccountCode string
	AccountID   int64

	TxnDate   calendar.Date
	Direction Direction
	// Amount 恒为正，方向由 Direction 表达。
	//
	// 之所以不用带符号金额：负数的方向语义在不同银行的对账单里不一致
	// （有的用正负、有的用单独的收支列），统一成正数 + 显式方向更不容易出错。
	Amount money.Money

	// Balance 是这笔交易后的银行账户余额。用于和账面余额对账。
	Balance money.Money

	CounterpartyName    string
	CounterpartyAccount string
	Summary             string
	SerialNo            string

	Status     Status
	MatchLayer Layer
	Confidence float64

	// 提议的记账方案（人工可改后过账）
	CounterAccount string // 对方科目
	// 辅助核算维度。四维都保留：对方科目可能是「管理费用—办公费」（要部门），
	// 也可能是「其他应付款—股东」（要股东），只留往来单位会让其中一类用不了。
	ContactID  *int64
	EmployeeID *int64
	DeptID     *int64
	ProjectID  *int64
	Memo       string

	VoucherID *int64
	RuleID    *int64
}

// Label 返回便于列表展示的一行文本。
func (f *Flow) Label() string {
	return fmt.Sprintf("%s %s %s %s %s", f.TxnDate, f.Direction.Label(),
		f.Amount, f.CounterpartyName, f.Summary)
}

// DedupKey 计算去重键。
//
// 优先用银行流水号 —— 它是对账单里唯一的、跨次导入稳定的标识。
// 没有流水号时退化为「日期+方向+金额+余额+对方户名+摘要」的组合，
// 这对同一账户同一时刻的同一笔交易足够唯一。
//
// 之所以必须在数据库层再加唯一索引兜底：同一份对账单被重复导入是
// 最常见的用户误操作，静默产生重复流水会让账目凭空多出一倍。
func (f *Flow) DedupKey() string {
	if s := strings.TrimSpace(f.SerialNo); s != "" {
		return "sn:" + s
	}
	return "fp:" + strings.Join([]string{
		f.TxnDate.String(),
		string(f.Direction),
		f.Amount.PlainString(),
		f.Balance.PlainString(),
		strings.TrimSpace(f.CounterpartyName),
		strings.TrimSpace(f.Summary),
	}, "|")
}

// Validate 检查流水本身是否合法。
func (f *Flow) Validate() error {
	if !f.Direction.Valid() {
		return fmt.Errorf("%w: %q", ErrBadDirection, f.Direction)
	}
	if !f.Amount.IsPositive() {
		return fmt.Errorf("%w: %s", ErrBadAmount, f.Amount)
	}
	if !f.TxnDate.Valid() {
		return fmt.Errorf("bank: 交易日期非法: %v", f.TxnDate)
	}
	if f.AccountCode == "" {
		return errors.New("bank: 缺少银行科目")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Rule
// ---------------------------------------------------------------------------

// MatchField 是规则匹配的字段。
type MatchField string

// 匹配字段。
const (
	MatchCounterparty MatchField = "counterparty" // 对方户名
	MatchSummary      MatchField = "summary"      // 摘要
	MatchBoth         MatchField = "both"         // 两者任一命中
)

// Valid 报告匹配字段是否合法。
func (m MatchField) Valid() bool {
	return m == MatchCounterparty || m == MatchSummary || m == MatchBoth
}

// Rule 是一条匹配规则。
//
// 规则只需要给出**对方科目**；银行科目由流水所属账户决定，
// 借贷方向由流水方向决定。这样规则可以跨银行复用。
type Rule struct {
	ID       int64
	Name     string
	Priority int // 数值小的先匹配
	Enabled  bool

	// AccountCode 限定适用的银行科目；为空表示不限。
	AccountCode string
	AccountID   int64

	// Direction 限定资金流向；为空表示不限。
	Direction Direction

	MatchField MatchField
	Pattern    string // 子串匹配（大小写不敏感；对中文即精确子串）
	AmountMin  *money.Money
	AmountMax  *money.Money

	// 命中后的记账方案。
	//
	// ★ 四维辅助核算都要有，不能只有往来单位。
	// 一条「房租 → 管理费用—租赁费」的规则若不能带「部门」，
	// 匹配会上，生成凭证时却会被「缺少必需的辅助核算」拦下 ——
	// 用户看到「规则命中 2 条」却一条也记不上，而且不知道为什么。
	CounterAccountCode string
	CounterAccountID   int64
	ContactID          *int64
	EmployeeID         *int64
	DeptID             *int64
	ProjectID          *int64
	MemoTemplate       string

	HitCount int
}

// Matches 报告规则是否命中给定流水。
func (r *Rule) Matches(f *Flow) bool {
	if !r.Enabled {
		return false
	}
	// 银行科目限定
	if r.AccountCode != "" && r.AccountCode != f.AccountCode {
		return false
	}
	// 方向限定
	if r.Direction != "" && r.Direction != f.Direction {
		return false
	}
	// 金额区间
	if r.AmountMin != nil && f.Amount < *r.AmountMin {
		return false
	}
	if r.AmountMax != nil && f.Amount > *r.AmountMax {
		return false
	}
	// 文本匹配
	pattern := strings.TrimSpace(r.Pattern)
	if pattern == "" {
		// 无文本条件的规则等价于「金额区间 + 方向」规则
		return true
	}
	pattern = strings.ToLower(pattern)
	counterparty := strings.ToLower(f.CounterpartyName)
	summary := strings.ToLower(f.Summary)

	switch r.MatchField {
	case MatchCounterparty:
		return strings.Contains(counterparty, pattern)
	case MatchSummary:
		return strings.Contains(summary, pattern)
	case MatchBoth:
		return strings.Contains(counterparty, pattern) || strings.Contains(summary, pattern)
	default:
		return false
	}
}

// RenderMemo 用流水信息渲染摘要模板。
//
// 支持的占位符：
//
//	{counterparty}  对方户名
//	{summary}       银行摘要
//	{date}          交易日期
//	{amount}        金额
func (r *Rule) RenderMemo(f *Flow) string {
	tpl := r.MemoTemplate
	if tpl == "" {
		// 默认摘要：对方户名优先，退回银行摘要
		if f.CounterpartyName != "" {
			return f.CounterpartyName
		}
		return f.Summary
	}
	repl := strings.NewReplacer(
		"{counterparty}", f.CounterpartyName,
		"{summary}", f.Summary,
		"{date}", f.TxnDate.String(),
		"{amount}", f.Amount.PlainString(),
	)
	return strings.TrimSpace(repl.Replace(tpl))
}

// ---------------------------------------------------------------------------
// Suggestion
// ---------------------------------------------------------------------------

// Suggestion 是对一条流水的记账提议。
type Suggestion struct {
	Layer      Layer
	Confidence float64 // 0~1

	// CounterAccountCode 是对方科目；银行科目由流水决定。
	CounterAccountCode string
	CounterAccountID   int64
	// 四维辅助核算与科目编码一起构成「自动记账方案」。
	//
	// ★ 四维都要能从三层匹配里带出来。少了任何一维，
	// 指向要求该维度的科目的规则就会「匹配成功、生成凭证时被拦下」——
	// 用户看到「命中 2 条」却一条也记不上，而且看不出为什么。
	ContactID  *int64
	EmployeeID *int64
	DeptID     *int64
	ProjectID  *int64
	Memo       string

	// Reason 是给用户看的一句话理由，例如「对方户名与 3 笔历史分录一致」。
	Reason string
	// Evidence 是支撑证据，例如历史凭证号。
	Evidence []string

	// RuleID 在规则命中时填写，便于统计规则的命中次数。
	RuleID *int64
}

// IsActionable 报告该提议是否达到可自动勾选的置信度。
//
// 阈值 0.75 是刻意的保守值：宁可让用户多点几下，也不要静默记错账。
// 低于阈值的提议仍然展示，但默认不勾选。
func (s *Suggestion) IsActionable() bool { return s.Confidence >= 0.75 }

// ---------------------------------------------------------------------------
// 规则引擎（第 1 层）
// ---------------------------------------------------------------------------

// RuleEngine 按优先级依次尝试规则，返回第一个命中的提议。
type RuleEngine struct {
	// Rules 应按 Priority 升序排列（小者优先）。
	Rules []*Rule
}

// NewRuleEngine 构造规则引擎，并按优先级排序。
func NewRuleEngine(rules []*Rule) *RuleEngine {
	sorted := make([]*Rule, 0, len(rules))
	for _, r := range rules {
		if r.Enabled {
			sorted = append(sorted, r)
		}
	}
	// 稳定排序：优先级相同时保持原有顺序（用户配置顺序）
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Priority < sorted[j-1].Priority; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return &RuleEngine{Rules: sorted}
}

// Match 返回第一条命中的规则所给出的提议；无命中时返回 nil。
func (e *RuleEngine) Match(f *Flow) *Suggestion {
	for _, r := range e.Rules {
		if !r.Matches(f) {
			continue
		}
		s := &Suggestion{
			Layer:              LayerRule,
			Confidence:         ruleConfidence(r),
			CounterAccountCode: r.CounterAccountCode,
			CounterAccountID:   r.CounterAccountID,
			ContactID:          r.ContactID,
			EmployeeID:         r.EmployeeID,
			DeptID:             r.DeptID,
			ProjectID:          r.ProjectID,
			Memo:               r.RenderMemo(f),
			Reason:             fmt.Sprintf("命中规则「%s」", r.displayName()),
		}
		id := r.ID
		s.RuleID = &id
		return s
	}
	return nil
}

// ruleConfidence 给规则命中赋置信度。
//
// 带具体文本条件的规则比「只按金额区间」的规则更可信 ——
// 后者有可能误伤，因此不自动勾选。
func ruleConfidence(r *Rule) float64 {
	if strings.TrimSpace(r.Pattern) == "" {
		return 0.60 // 仅按金额/方向的规则：需人工确认
	}
	if r.MatchField == MatchSummary {
		// 银行摘要往往包含对方信息，比户名更具体
		return 0.95
	}
	return 0.90
}

func (r *Rule) displayName() string {
	if r.Name != "" {
		return r.Name
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	return fmt.Sprintf("#%d", r.ID)
}

// ---------------------------------------------------------------------------
// 历史相似度（第 2 层）
// ---------------------------------------------------------------------------

// HistoryEntry 是一条可参考的历史分录。
//
// 它由存储层从已过账凭证里抽取：同一银行科目、同一方向的对方科目分录。
type HistoryEntry struct {
	VoucherNo string
	VoucherID int64

	// CounterpartyName / Summary 来自该凭证对应流水的对手方与摘要。
	CounterpartyName string
	Summary          string

	Direction Direction
	Amount    money.Money

	// 该历史凭证里与银行科目配对的那个科目，
	// 连同它当时填的四维辅助核算一起带出来。
	//
	// 历史匹配的全部价值就是「上次怎么记的，这次照着记」——
	// 只带回科目而丢掉辅助核算，遇到要求部门的费用科目照样记不上。
	CounterAccountCode string
	CounterAccountID   int64
	ContactID          *int64
	EmployeeID         *int64
	DeptID             *int64
	ProjectID          *int64
	Memo               string
}

// HistoryMatcher 从历史分录里找出最相似的一笔。
type HistoryMatcher struct {
	Entries []HistoryEntry
}

// NewHistoryMatcher 构造历史匹配器。
func NewHistoryMatcher(entries []HistoryEntry) *HistoryMatcher {
	return &HistoryMatcher{Entries: entries}
}

// Match 返回最相似的历史提议；无可参考项时返回 nil。
//
// 打分规则（不用向量，纯字符串 —— 中文户名与摘要的精确子串匹配
// 在这个场景下已经足够准，且完全可解释、零成本、离线可用）：
//
//	对方户名完全相同        +0.60
//	一方包含另一方          +0.40
//	历史摘要含当前摘要片段  +0.20
//	金额完全相同            +0.10
//	方向不同                 直接排除
//	银行科目不同             直接排除
//
// 置信度上限 0.95 —— 历史相似毕竟不是规则命中，留一点余地。
func (h *HistoryMatcher) Match(f *Flow) *Suggestion {
	var best *HistoryEntry
	bestScore := 0.0

	for i := range h.Entries {
		e := &h.Entries[i]
		if e.Direction != f.Direction {
			continue
		}
		score := similarity(f, e)
		if score > bestScore {
			bestScore, best = score, e
		}
	}
	if best == nil || bestScore < 0.5 {
		return nil
	}
	if bestScore > 0.95 {
		bestScore = 0.95
	}
	return &Suggestion{
		Layer:              LayerHistory,
		Confidence:         bestScore,
		CounterAccountCode: best.CounterAccountCode,
		CounterAccountID:   best.CounterAccountID,
		ContactID:          best.ContactID,
		EmployeeID:         best.EmployeeID,
		DeptID:             best.DeptID,
		ProjectID:          best.ProjectID,
		Memo:               best.Memo,
		Reason: fmt.Sprintf("与历史凭证 %s 的记法一致（对方：%s）",
			best.VoucherNo, best.CounterpartyName),
		Evidence: []string{"voucher:" + best.VoucherNo},
	}
}

func similarity(f *Flow, e *HistoryEntry) float64 {
	var score float64

	a := norm(f.CounterpartyName)
	b := norm(e.CounterpartyName)
	switch {
	case a != "" && a == b:
		score += 0.60
	case a != "" && b != "" && (strings.Contains(a, b) || strings.Contains(b, a)):
		score += 0.40
	}

	sa, sb := norm(f.Summary), norm(e.Summary)
	if sa != "" && sb != "" {
		if sa == sb {
			score += 0.20
		} else if commonPrefix(sa, sb) >= 2 {
			score += 0.10
		}
	}

	if f.Amount == e.Amount {
		score += 0.10
	}
	return score
}

func norm(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	// 银行摘要常夹带空格，统一去掉便于比较
	return strings.ReplaceAll(s, " ", "")
}

// commonPrefix 返回两个字符串的公共前缀长度（按 rune 计）。
func commonPrefix(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n := len(ra)
	if len(rb) < n {
		n = len(rb)
	}
	i := 0
	for i < n && ra[i] == rb[i] {
		i++
	}
	return i
}

// ---------------------------------------------------------------------------
// 三层编排
// ---------------------------------------------------------------------------

// Matcher 把三层匹配串起来。
//
// AI 层通过 AISuggester 接口注入：默认不接入（nil），
// 前两层已能覆盖大部分场景。这样即便用户完全不用 AI，
// 银行流水导入功能也完整可用。
type Matcher struct {
	Rules   *RuleEngine
	History *HistoryMatcher
	AI      AISuggester
}

// AISuggester 是第 3 层的接口。实现见后续阶段的 AI 模块。
//
// 刻意把它定义成接口而不是直接依赖模型客户端：
// AI 是**增强**不是**依赖** —— 断网、模型崩了、没配 API Key，
// 记账功能必须完全可用。
type AISuggester interface {
	Suggest(f *Flow) (*Suggestion, error)
}

// Match 依次尝试三层，返回第一个可用的提议。
func (m *Matcher) Match(f *Flow) *Suggestion {
	if m.Rules != nil {
		if s := m.Rules.Match(f); s != nil {
			return s
		}
	}
	if m.History != nil {
		if s := m.History.Match(f); s != nil {
			return s
		}
	}
	if m.AI != nil {
		if s, err := m.AI.Suggest(f); err == nil && s != nil {
			s.Layer = LayerAI
			return s
		}
		// AI 失败不影响流程：降级为「未匹配，请人工填写」
	}
	return nil
}

// ---------------------------------------------------------------------------
// 生成凭证
// ---------------------------------------------------------------------------

// BuildEntries 把一条流水的记账方案转成一借一贷两条分录。
//
//	收入  借 银行科目  贷 对方科目
//	支出  借 对方科目  贷 银行科目
func (f *Flow) BuildEntries() ([]Entry, error) {
	if f.CounterAccount == "" {
		return nil, ErrNoMatched
	}
	memo := f.Memo
	if memo == "" {
		memo = f.Summary
	}
	if memo == "" {
		memo = f.CounterpartyName
	}
	if memo == "" {
		memo = f.TxnDate.String() + " 银行流水"
	}

	bank := Entry{AccountCode: f.AccountCode, Summary: memo}
	counter := Entry{AccountCode: f.CounterAccount, Summary: memo}

	// 辅助核算挂在**对方科目**那一侧：
	// 「其他应付款—股东」要求股东、「管理费用—办公费」要求部门，
	// 这些维度挂在银行存款上是没有意义的。
	counter.ContactID = f.ContactID
	counter.EmployeeID = f.EmployeeID
	counter.DeptID = f.DeptID
	counter.ProjectID = f.ProjectID

	// 借方在前、贷方在后 —— 与中国凭证的书写与阅读顺序一致
	switch f.Direction {
	case DirIn:
		bank.Debit = f.Amount
		counter.Credit = f.Amount
		return []Entry{bank, counter}, nil
	case DirOut:
		counter.Debit = f.Amount
		bank.Credit = f.Amount
		return []Entry{counter, bank}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrBadDirection, f.Direction)
	}
}

// Entry 是生成凭证用的一条分录（避免 bank 包直接依赖 ledger 包的构造细节）。
type Entry struct {
	AccountCode string
	Summary     string
	Debit       money.Money
	Credit      money.Money
	ContactID   *int64
	EmployeeID  *int64
	DeptID      *int64
	ProjectID   *int64
}

// ValidateAccount 校验对方科目是否可记账。
//
// 流水的银行科目同样要过这一关 —— 用户可能把流水挂到了汇总科目上。
func ValidateAccount(tree *account.Tree, code string) error {
	return tree.CheckPostable(code)
}
