// Package ai 定义 AI 辅助生成凭证的**契约与护栏**。
//
// # 这个包不调用任何模型
//
// 它只做三件事：
//
//  1. 定义模型必须遵守的输出契约（严格 JSON）
//  2. 解析模型返回，拒绝一切越界、含糊、自相矛盾的内容
//  3. 把通过的提议转成可校验的凭证草稿
//
// 模型调用放在别的包（如 internal/ai），这样可以：
//   - 用假模型把护栏测透，不需要真的联网
//   - 换模型供应商时不必碰护栏逻辑
//
// # 第一原则
//
// **AI 只提议，人只确认，账由 Go 写。**
//
// 模型永远拿不到写数据库的能力。它能做的最出格的事是返回一段
// 通不过校验的 JSON —— 然后被记进审计表，让人看见。
//
// 具体地：
//
//	模型返回的 account_code 不在账套里 → 拒绝（幻觉科目是最常见的失效）
//	借贷不平                              → 拒绝，**绝不自动调平**
//	金额是小数或负数                      → 拒绝
//	日期落在已结账期间                     → 拒绝
package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
)

// 契约相关错误。
var (
	ErrMalformedJSON  = errors.New("ai: 模型返回的不是合法 JSON")
	ErrUnknownField   = errors.New("ai: 模型返回了契约之外的字段")
	ErrBadAmount      = errors.New("ai: 金额不是正整数「分」")
	ErrNotBalanced    = errors.New("ai: 借贷不平衡")
	ErrUnknownAccount = errors.New("ai: 科目不在当前账套中")
	ErrUnknownContact = errors.New("ai: 往来单位不存在")
	ErrAuxMismatch    = errors.New("ai: 辅助核算维度不符合科目要求")
	ErrNoEntries      = errors.New("ai: 提议没有任何分录")
)

// ---------------------------------------------------------------------------
// 契约
// ---------------------------------------------------------------------------

// Layer 标识提议是哪一层产出的。
//
// 三层依次兜底，越靠前越便宜、越可靠：
//
//	rule    —— 用户自己定的规则（银行流水匹配规则）
//	history —— 历史上同一对手方/同一摘要的记法（检索增强）
//	ai      —— 模型推理（最贵、最不可靠，但对新场景有用）
type Layer string

// 三层来源。
const (
	LayerRule    Layer = "rule"
	LayerHistory Layer = "history"
	LayerAI      Layer = "ai"
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
	default:
		return string(l)
	}
}

// Proposal 是模型必须返回的结构。
//
// 字段名用 snake_case，与设计文档中的 JSON Schema 一致，
// 这样发给任何 OpenAI 兼容接口时都不需要再转换。
type Proposal struct {
	Voucher    ProposedVoucher `json:"voucher"`
	Confidence float64         `json:"confidence"`
	Reasoning  string          `json:"reasoning"`
	Evidence   []string        `json:"evidence"`
	Warnings   []string        `json:"warnings"`
}

// ProposedVoucher 是模型提议的凭证主体。
type ProposedVoucher struct {
	Word    string          `json:"word"`
	BizDate string          `json:"biz_date"`
	Remark  string          `json:"remark"`
	Entries []ProposedEntry `json:"entries"`
}

// ProposedEntry 是模型提议的一条分录。
//
// Debit / Credit 用 Cents 而不是 int64：
// 只有保留字面量原文，才能区分「模型返回了 5000」与
// 「模型返回了 5000.00 / "5000" / 5e3」——
// 前者是合法金额，后者必须拒绝。用 int64 反序列化会把它们悄悄变成同一个值。
type ProposedEntry struct {
	Summary     string `json:"summary"`
	AccountCode string `json:"account_code"`
	Debit       Cents  `json:"debit"`
	Credit      Cents  `json:"credit"`
	ContactID   *int64 `json:"contact_id"`
	EmployeeID  *int64 `json:"employee_id"`
	DeptID      *int64 `json:"dept_id"`
	ProjectID   *int64 `json:"project_id"`
}

// Cents 是契约中的金额字面量，保留原文直到校验阶段。
//
// ★ 为什么需要自定义 UnmarshalJSON：
// json.Number 的底层类型是 string，encoding/json 遇到 JSON 字符串
// `"5000"` 时会**直接把它赋给 json.Number** —— 于是
// 「金额必须是数字字面量」这条约束就被静默绕过了。
// 这里显式检查首字节，字符串形式的金额一律拒绝。
type Cents struct{ raw json.Number }

// UnmarshalJSON 只接受 JSON 数字字面量。
func (c *Cents) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return fmt.Errorf("%w: 金额不能是字符串 %s", ErrBadAmount, string(b))
	}
	if string(b) == "null" {
		// null 视为未填写（0），由护栏判断「借贷都是 0」的情况
		c.raw = "0"
		return nil
	}
	c.raw = json.Number(string(b))
	return nil
}

// MarshalJSON 原样写回，保证提议可以往返序列化进审计表。
func (c Cents) MarshalJSON() ([]byte, error) {
	if c.raw == "" {
		return []byte("0"), nil
	}
	return []byte(c.raw), nil
}

// String 返回字面量原文。
func (c Cents) String() string { return string(c.raw) }

// Money 把字面量解析成「分」，非法字面量返回 ErrBadAmount。
func (c Cents) Money() (money.Money, error) { return ParseCents(c.raw) }

// IsSet 报告该字段是否在 JSON 里出现过。
func (c Cents) IsSet() bool { return c.raw != "" }

// Aux 返回该分录填写的辅助核算。
func (e ProposedEntry) Aux() ledger.Aux {
	return ledger.Aux{
		ContactID: e.ContactID, EmployeeID: e.EmployeeID,
		DeptID: e.DeptID, ProjectID: e.ProjectID,
	}
}

// ---------------------------------------------------------------------------
// 解析
// ---------------------------------------------------------------------------

// Parse 严格解析模型返回的 JSON。
//
// 三条严格规则：
//
//  1. **拒绝未知字段**。模型多返回一个 "tax_rate" 说明它没按契约走，
//     此时静默忽略等于放行一个我们没校验过的值。
//  2. **拒绝重复键**。`{"debit":100,"debit":200}` 在不同解析器下结果不同，
//     在钱的问题上不能有这种歧义。
//  3. **金额必须是纯整数字面量**。`5000.00`、`"5000"`、`5e3`、`null`
//     全部拒绝 —— 它们要么说明模型没用结构化输出，要么说明它想绕过整数约束。
func Parse(raw string) (*Proposal, error) {
	raw = stripCodeFence(strings.TrimSpace(raw))
	if raw == "" {
		return nil, fmt.Errorf("%w: 内容为空", ErrMalformedJSON)
	}
	if err := checkDuplicateKeys(raw); err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	dec.UseNumber()

	var p Proposal
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedJSON, err)
	}
	// 尾部还有内容说明返回了多个 JSON 对象（模型常见毛病）
	if dec.More() {
		return nil, fmt.Errorf("%w: JSON 之后还有多余内容", ErrMalformedJSON)
	}
	return &p, nil
}

// stripCodeFence 去掉模型爱加的 ```json 围栏。
//
// 即使用了结构化输出，仍有些本地模型会把整段包进 Markdown 代码块。
// 这是**唯一**一处容忍的格式偏差 —— 它不改变任何语义。
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// checkDuplicateKeys 检出重复的对象键。
//
// encoding/json 默认「后者胜出」，于是 {"account_code":"1002","account_code":"9999"}
// 会被解析成 9999 —— 而 1002 恰好是被校验过的那一个。
// 这类输入只可能来自模型或攻击者，一律拒绝。
func checkDuplicateKeys(raw string) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := scanValue(dec); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedJSON, err)
	}
	return nil
}

func scanValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch d := tok.(type) {
	case json.Delim:
		switch d {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return err
				}
				key, _ := kt.(string)
				if seen[key] {
					return fmt.Errorf("重复的键 %q", key)
				}
				seen[key] = true
				if err := scanValue(dec); err != nil {
					return err
				}
			}
			_, err := dec.Token() // 消耗 '}'
			return err
		case '[':
			for dec.More() {
				if err := scanValue(dec); err != nil {
					return err
				}
			}
			_, err := dec.Token() // 消耗 ']'
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 金额解析
// ---------------------------------------------------------------------------

// ParseCents 把契约里的金额字面量解析成「分」。
//
// 只接受纯十进制整数字面量：`0`、`5000`、`-0` 之外一律拒绝负数。
// 前导 `+`、小数点、指数、引号包裹、空白全部拒绝。
func ParseCents(n json.Number) (money.Money, error) {
	s := string(n)
	if s == "" {
		return 0, fmt.Errorf("%w: 缺失", ErrBadAmount)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("%w: %q 不是非负整数字面量", ErrBadAmount, s)
		}
	}
	v, err := n.Int64()
	if err != nil {
		return 0, fmt.Errorf("%w: %q 超出可表示范围", ErrBadAmount, s)
	}
	return money.Money(v), nil
}

// ---------------------------------------------------------------------------
// 校验：护栏
// ---------------------------------------------------------------------------

// CheckLevel 是单项检查的结论。
type CheckLevel string

// 检查结论。
const (
	CheckOK   CheckLevel = "ok"
	CheckFail CheckLevel = "fail"
	// CheckWarn 不阻断，但需要提醒用户多看一眼。
	CheckWarn CheckLevel = "warn"
)

// Check 是一项护栏检查的结果。
type Check struct {
	Key     string
	Title   string
	Level   CheckLevel
	Detail  string
	EntryNo int // 相关分录序号（1 起），0 表示整张凭证
}

// Report 是一次完整护栏校验的结果。
type Report struct {
	Checks []Check
	// Checksum 是提议内容的指纹，用于审计与去重。
	Checksum string
}

// Passed 报告是否通过全部阻断项。
func (r *Report) Passed() bool {
	for _, c := range r.Checks {
		if c.Level == CheckFail {
			return false
		}
	}
	return true
}

// Failures 返回全部阻断项。
func (r *Report) Failures() []Check {
	var out []Check
	for _, c := range r.Checks {
		if c.Level == CheckFail {
			out = append(out, c)
		}
	}
	return out
}

// Warnings 返回全部提醒项。
func (r *Report) Warnings() []Check {
	var out []Check
	for _, c := range r.Checks {
		if c.Level == CheckWarn {
			out = append(out, c)
		}
	}
	return out
}

// Summary 返回一句话结论。
func (r *Report) Summary() string {
	if r.Passed() {
		if n := len(r.Warnings()); n > 0 {
			return fmt.Sprintf("护栏校验通过（%d 项提醒）", n)
		}
		return "护栏校验通过"
	}
	f := r.Failures()
	parts := make([]string, 0, len(f))
	for _, c := range f {
		if c.EntryNo > 0 {
			parts = append(parts, fmt.Sprintf("第%d行 %s", c.EntryNo, c.Title))
		} else {
			parts = append(parts, c.Title)
		}
	}
	return fmt.Sprintf("护栏校验未通过（%d 项）：%s", len(f), strings.Join(parts, "；"))
}

// Detail 返回逐项展开的完整说明，供**审计记录**使用。
//
// 与 Summary 的分工：
//
//	Summary  一句话，给界面横幅用，越短越好
//	Detail   逐项列全，给审计表用 —— 「模型错在哪个科目上」
//	         这类问题只能靠原因里的细节回答，标题里没有科目编码
func (r *Report) Detail() string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range r.Checks {
		if c.Level == CheckOK {
			continue
		}
		mark := "阻断"
		if c.Level == CheckWarn {
			mark = "提醒"
		}
		if c.EntryNo > 0 {
			fmt.Fprintf(&b, "[%s] 第%d行 %s：%s\n", mark, c.EntryNo, c.Title, c.Detail)
		} else {
			fmt.Fprintf(&b, "[%s] %s：%s\n", mark, c.Title, c.Detail)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Expect 是调用方**已经确定知道**的事实，护栏拿它来核对模型的输出。
//
// # 为什么要有它
//
// 护栏能检查「提议自己跟自己是否自洽」（借贷相等、科目存在……），
// 但检查不了「提议跟这笔业务是否相符」—— 后者需要业务侧的输入，
// 而那是调用方才知道的。少了这一半，一份**内部自洽但金额写错**的
// 凭证会一路绿灯通过。
//
// 零值表示「没有可核对的事实」，此时不做对应检查。
//
// # 为什么不塞进 ledger.Context
//
// Context 描述的是**账套本身**（科目表、期间表、往来档案）——
// 同一个账套里这些是固定的；而这里的金额是**这一笔业务**的输入，
// 每笔都不同。混在一起，账套级的校验也会被迫带上业务级的字段。
type Expect struct {
	// Amount 是用户填的金额（分，正负表示收付方向）；0 表示没填，
	// 此时不做金额核对。
	Amount money.Money
}

// Validate 对提议执行全部护栏检查。
//
// ctx 提供科目树、期间表与往来档案；调用方从存储层加载。
// expect 是本笔业务里调用方已经确定的事实（见 Expect）。
// 这是**唯一**的准入闸门：任何提议在变成凭证之前都必须过这里。
func (p *Proposal) Validate(ctx *ledger.Context, expect Expect) (*Report, error) {
	if ctx == nil || ctx.Accounts == nil || ctx.Periods == nil {
		return nil, errors.New("ai: Validate 需要科目树与期间表")
	}
	r := &Report{Checks: make([]Check, 0, 16)}
	add := func(c Check) { r.Checks = append(r.Checks, c) }

	// ---- 1. 凭证头 ----
	w := voucher.Word(strings.TrimSpace(p.Voucher.Word))
	if w == "" {
		w = voucher.WordJi
		add(Check{Key: "word", Title: "凭证字", Level: CheckWarn,
			Detail: "模型未给凭证字，按「记」处理"})
	} else if !w.Valid() {
		add(Check{Key: "word", Title: "凭证字非法", Level: CheckFail,
			Detail: fmt.Sprintf("%q 不是有效的凭证字（记/收/付/转）", p.Voucher.Word)})
	} else {
		add(Check{Key: "word", Title: "凭证字", Level: CheckOK, Detail: string(w)})
	}

	date, dateErr := calendar.Parse(strings.TrimSpace(p.Voucher.BizDate))
	if dateErr != nil {
		add(Check{Key: "biz_date", Title: "记账日期非法", Level: CheckFail,
			Detail: fmt.Sprintf("%q 无法解析为 YYYY-MM-DD", p.Voucher.BizDate)})
	} else if per, ok := ctx.Periods.PeriodOf(date); !ok {
		add(Check{Key: "biz_date", Title: "日期不在任何会计期间内", Level: CheckFail,
			Detail: date.String()})
	} else {
		k := per.Key
		// ★ 日期必须落在「已启用且未结账」的期间
		switch per.Status {
		case period.StatusOpen:
			add(Check{Key: "biz_date", Title: "记账日期", Level: CheckOK,
				Detail: fmt.Sprintf("%s（%s）", date, k)})
		case period.StatusFuture:
			add(Check{Key: "biz_date", Title: "记账日期属于未启用期间", Level: CheckFail,
				Detail: fmt.Sprintf("%s 落在 %s，该期间尚未启用", date, k)})
		default:
			add(Check{Key: "biz_date", Title: "记账日期属于已结账期间", Level: CheckFail,
				Detail: fmt.Sprintf("%s 落在 %s，该期间已结账", date, k)})
		}
	}

	// ---- 2. 分录数 ----
	if len(p.Voucher.Entries) == 0 {
		add(Check{Key: "entries", Title: "没有分录", Level: CheckFail, Detail: "提议为空"})
		return r, nil
	}
	if len(p.Voucher.Entries) < 2 {
		add(Check{Key: "entries", Title: "分录数不足", Level: CheckFail,
			Detail: fmt.Sprintf("只有 %d 条，复式记账至少需要 2 条", len(p.Voucher.Entries))})
	}

	// ---- 3. 逐条分录 ----
	var totalDebit, totalCredit money.Money
	for i, e := range p.Voucher.Entries {
		no := i + 1

		// 3.1 摘要
		if strings.TrimSpace(e.Summary) == "" {
			add(Check{Key: "summary", Title: "摘要为空", Level: CheckFail,
				EntryNo: no, Detail: "《会计基础工作规范》要求逐行摘要"})
		}

		// 3.2 金额字面量
		d, derr := e.Debit.Money()
		if derr != nil {
			add(Check{Key: "amount", Title: "借方金额非法", Level: CheckFail,
				EntryNo: no, Detail: derr.Error()})
		}
		c, cerr := e.Credit.Money()
		if cerr != nil {
			add(Check{Key: "amount", Title: "贷方金额非法", Level: CheckFail,
				EntryNo: no, Detail: cerr.Error()})
		}
		if derr != nil || cerr != nil {
			continue
		}

		// 3.3 恰有一个为正
		switch {
		case d.IsPositive() && c.IsPositive():
			add(Check{Key: "amount", Title: "借贷同时有金额", Level: CheckFail,
				EntryNo: no, Detail: fmt.Sprintf("借 %s 贷 %s", d, c)})
			continue
		case d.IsZero() && c.IsZero():
			add(Check{Key: "amount", Title: "金额为零", Level: CheckFail,
				EntryNo: no, Detail: "借贷都是 0"})
			continue
		}
		totalDebit, totalCredit = totalDebit.Add(d), totalCredit.Add(c)

		// 3.4 科目存在且可记账
		code := strings.TrimSpace(e.AccountCode)
		acc, ok := ctx.Accounts.Get(code)
		if !ok {
			// ★ AI 最常见的失效：幻觉出一个不存在的科目编码
			add(Check{Key: "account", Title: "科目不存在", Level: CheckFail,
				EntryNo: no, Detail: fmt.Sprintf("%q 不在当前账套的科目表中", code)})
			continue
		}
		if err := ctx.Accounts.CheckPostable(code); err != nil {
			add(Check{Key: "account", Title: "科目不能记账", Level: CheckFail,
				EntryNo: no, Detail: err.Error()})
			continue
		}

		// 3.5 辅助核算
		if c := checkAux(acc, e, no, ctx); c != nil {
			add(*c)
		}
	}

	// ---- 4. 借贷平衡 ----
	if totalDebit != totalCredit {
		// ★ 绝不自动调平。
		//
		// 差额意味着模型理解错了业务，而不是「差一点」。
		// 用尾差科目抹平等于把错误藏进账里，之后再也找不出来。
		add(Check{Key: "balanced", Title: "借贷不平衡", Level: CheckFail,
			Detail: fmt.Sprintf("借方合计 %s，贷方合计 %s，差额 %s",
				totalDebit, totalCredit, totalDebit.Sub(totalCredit))})
	} else if totalDebit.IsZero() {
		add(Check{Key: "balanced", Title: "合计为零", Level: CheckFail, Detail: "借贷合计都是 0"})
	} else {
		add(Check{Key: "balanced", Title: "借贷平衡", Level: CheckOK,
			Detail: fmt.Sprintf("借 %s = 贷 %s", totalDebit, totalCredit)})
	}

	// ---- 5. 与「已知事实」对照 ----
	//
	// ★ 上面 3.x 与 4 检查的全是提议**自己跟自己**是否自洽：
	// 借贷相等、金额为正、科目存在、辅助核算齐不齐。
	// 而一份金额写错的凭证可以完全自洽 —— 实测遇到过：用户填 325 元，
	// 模型写成 326 元，借贷照样平衡，界面上显示的甚至是「借贷平衡」，
	// 二十多条护栏一条都没拦住。
	//
	// 这不是模型算错，是它**读错了输入**，而这类错误最危险：数字
	// 看起来很正常，会计一眼扫过去不会停。所以必须拿用户当初填的
	// 那个数对一次 —— 那是这一笔业务里唯一确定的事实。
	if expect.Amount != 0 {
		want, got := expect.Amount.Abs(), totalDebit.Abs()
		if got != want {
			add(Check{Key: "amount_match", Title: "金额与输入不符", Level: CheckFail,
				Detail: fmt.Sprintf("你填的是 %s，提议合计 %s，差 %s —— "+
					"差额通常意味着模型读错了金额，不要直接采纳",
					want, got, got.Sub(want).Abs())})
		} else {
			add(Check{Key: "amount_match", Title: "金额与输入一致", Level: CheckOK,
				Detail: want.String()})
		}
	}

	// ---- 6. 置信度 ----
	switch {
	case p.Confidence < 0 || p.Confidence > 1:
		add(Check{Key: "confidence", Title: "置信度越界", Level: CheckFail,
			Detail: fmt.Sprintf("%v 不在 [0,1] 区间", p.Confidence)})
	case p.Confidence < ConfidenceWarnThreshold:
		add(Check{Key: "confidence", Title: "置信度偏低", Level: CheckWarn,
			Detail: fmt.Sprintf("模型自评 %.2f，建议人工逐行核对", p.Confidence)})
	default:
		add(Check{Key: "confidence", Title: "置信度", Level: CheckOK,
			Detail: fmt.Sprintf("%.2f", p.Confidence)})
	}

	r.Checksum = p.Checksum()
	return r, nil
}

// ConfidenceWarnThreshold 是置信度提醒阈值。
const ConfidenceWarnThreshold = 0.75

// dim 是**存储维度**，与辅助核算类型不是一一对应。
//
// 之所以要区分：客户、供应商、股东、其他单位这四类往来单位
// 共用 contact 表、共用 `contact_id` 一列。如果按 AuxType 逐个判断
// 「填了没有」，一个 contact_id 会同时被算作四种维度都填了 ——
// 于是「科目只声明了客户，模型却填了供应商」这种真问题会被
// 误报成一堆「多填了辅助核算」的提醒，而真正该阻断的类型冲突反而漏掉。
//
// 所以先归一到存储维度：往来单位是一个维度，类型对不对由另一项检查负责。
type dim string

const (
	dimContact  dim = "contact"
	dimEmployee dim = "employee"
	dimDept     dim = "dept"
	dimProject  dim = "project"
)

// dimOf 把辅助核算类型归一到存储维度。
func dimOf(t account.AuxType) (dim, bool) {
	switch t {
	case account.AuxCustomer, account.AuxSupplier,
		account.AuxShareholder, account.AuxOther:
		return dimContact, true
	case account.AuxEmployee:
		return dimEmployee, true
	case account.AuxDept:
		return dimDept, true
	case account.AuxProject:
		return dimProject, true
	default:
		return "", false
	}
}

// dimLabel 返回存储维度的中文名。
//
// 往来单位按科目声明的**具体类型**称呼：对「应收账款」说
// 「要求客户」比说「要求往来单位」有用得多 —— 后者等于没说清要填什么。
func dimLabel(acc *account.Account, d dim) string {
	switch d {
	case dimContact:
		if k := contactKindFor(acc); k != "" {
			if t := account.AuxType(k); t.Valid() {
				return t.Label()
			}
		}
		return "往来单位"
	case dimEmployee:
		return "员工"
	case dimDept:
		return "部门"
	case dimProject:
		return "项目"
	default:
		return string(d)
	}
}

// providedDims 返回分录实际填写的存储维度集合。
func providedDims(a ledger.Aux) map[dim]bool {
	out := map[dim]bool{}
	if a.ContactID != nil {
		out[dimContact] = true
	}
	if a.EmployeeID != nil {
		out[dimEmployee] = true
	}
	if a.DeptID != nil {
		out[dimDept] = true
	}
	if a.ProjectID != nil {
		out[dimProject] = true
	}
	return out
}

// requiredDims 返回科目声明的存储维度集合。
func requiredDims(acc *account.Account) map[dim]bool {
	out := map[dim]bool{}
	for _, t := range acc.AuxTypes {
		if d, ok := dimOf(t); ok {
			out[d] = true
		}
	}
	return out
}

func checkAux(acc *account.Account, e ProposedEntry, no int,
	ctx *ledger.Context) *Check {

	have, want := providedDims(e.Aux()), requiredDims(acc)

	// 1. 科目要求的维度必须齐（阻断）
	for _, d := range []dim{dimContact, dimEmployee, dimDept, dimProject} {
		if want[d] && !have[d] {
			return &Check{Key: "aux", Title: "缺少必需的辅助核算", Level: CheckFail,
				EntryNo: no,
				Detail: fmt.Sprintf("科目 %s(%s) 要求「%s」，模型未给出",
					acc.Name, acc.Code, dimLabel(acc, d))}
		}
	}

	// 2. 科目没声明的维度不该乱填（提醒，不阻断）
	for _, d := range []dim{dimContact, dimEmployee, dimDept, dimProject} {
		if have[d] && !want[d] {
			return &Check{Key: "aux", Title: "多填了辅助核算", Level: CheckWarn,
				EntryNo: no,
				Detail: fmt.Sprintf("科目 %s(%s) 未声明「%s」，模型却填了值",
					acc.Name, acc.Code, dimLabel(acc, d))}
		}
	}

	// 3. 往来单位的**类型**必须与科目声明一致（阻断）
	//
	// 这一步才是「客户科目挂了供应商」的真正拦截点。
	if have[dimContact] && ctx.ContactKinds != nil {
		kind, ok := ctx.ContactKinds[*e.ContactID]
		if !ok {
			return &Check{Key: "contact", Title: "往来单位不存在", Level: CheckFail,
				EntryNo: no,
				Detail:  fmt.Sprintf("contact_id=%d 不在往来档案中", *e.ContactID)}
		}
		if wantKind := contactKindFor(acc); wantKind != "" && kind != wantKind {
			return &Check{Key: "contact", Title: "往来单位类型不匹配", Level: CheckFail,
				EntryNo: no,
				Detail: fmt.Sprintf("科目 %s(%s) 要求「%s」，但 contact_id=%d 的类型是「%s」",
					acc.Code, acc.Name, wantKind, *e.ContactID, kind)}
		}
	}
	return nil
}

// contactKindFor 返回科目辅助核算要求的往来单位类型。
//
// 只处理能一对一映射到 contact.kind 的维度；
// dept/project/employee 各有独立档案，不经 contact_id 表达。
func contactKindFor(acc *account.Account) string {
	for _, t := range []account.AuxType{
		account.AuxCustomer, account.AuxSupplier,
		account.AuxShareholder, account.AuxOther,
	} {
		if acc.SupportsAux(t) {
			return string(t)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 指纹
// ---------------------------------------------------------------------------

// Checksum 返回提议内容的稳定指纹（16 位十六进制）。
//
// 用途：
//   - 审计表去重（同一输入重复提议只记一条）
//   - 用户改了提议后重新校验时判断「是否真的变了」
//
// 只覆盖**影响账务**的字段：摘要、科目、金额、辅助核算、日期。
// reasoning / confidence 不进指纹 —— 它们变了但账没变。
func (p *Proposal) Checksum() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s\n", p.Voucher.Word, p.Voucher.BizDate, p.Voucher.Remark)
	for _, e := range p.Voucher.Entries {
		fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%s|%s|%s\n",
			e.Summary, e.AccountCode, e.Debit.String(), e.Credit.String(),
			ptrStr(e.ContactID), ptrStr(e.EmployeeID), ptrStr(e.DeptID), ptrStr(e.ProjectID))
	}
	return shortHash(b.String())
}

func ptrStr(p *int64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *p)
}

// ---------------------------------------------------------------------------
// 转成凭证草稿
// ---------------------------------------------------------------------------

// Materialize 把**已通过护栏**的提议转成凭证草稿。
//
// 返回值是草稿而不是已过账凭证：AI 的产物一律先落地为草稿，
// 由人确认后再过账。这样「谁记的账」始终有明确答案。
//
// createdBy 会写进凭证的制单人字段，界面上标记为 AI 提议。
func (p *Proposal) Materialize(createdBy string, at *AIProvenance) (*voucher.Voucher, error) {
	if createdBy == "" {
		return nil, voucher.ErrMissingMaker
	}
	date, err := calendar.Parse(strings.TrimSpace(p.Voucher.BizDate))
	if err != nil {
		return nil, fmt.Errorf("ai: 记账日期 %q 无法解析: %w", p.Voucher.BizDate, err)
	}
	word := voucher.Word(strings.TrimSpace(p.Voucher.Word))
	if word == "" {
		word = voucher.WordJi
	}

	v, err := voucher.New(word, date, createdBy)
	if err != nil {
		return nil, err
	}
	v.Remark = strings.TrimSpace(p.Voucher.Remark)
	v.Source = voucher.SourceAI
	if at != nil {
		v.CreatedByAI = true
	}

	for i, e := range p.Voucher.Entries {
		d, derr := e.Debit.Money()
		if derr != nil {
			return nil, fmt.Errorf("ai: 第 %d 行借方: %w", i+1, derr)
		}
		c, cerr := e.Credit.Money()
		if cerr != nil {
			return nil, fmt.Errorf("ai: 第 %d 行贷方: %w", i+1, cerr)
		}
		if err := v.AddEntry(ledger.Entry{
			AccountCode: strings.TrimSpace(e.AccountCode),
			Summary:     strings.TrimSpace(e.Summary),
			Debit:       d, Credit: c, Aux: e.Aux(),
		}); err != nil {
			return nil, fmt.Errorf("ai: 第 %d 行: %w", i+1, err)
		}
	}
	return v, nil
}

// AIProvenance 记录一条提议的来源，写进审计表并挂在凭证上。
type AIProvenance struct {
	Provider string
	Model    string
	Layer    Layer
	// PromptDigest 是输入摘要的指纹，**不含原始敏感数据**。
	PromptDigest string
	// Confidence 是模型自评置信度。
	Confidence float64
	// SuggestionID 指向审计表记录。
	SuggestionID int64
}

// ---------------------------------------------------------------------------
// 汇总与排序
// ---------------------------------------------------------------------------

// Rank 按「层优先、置信度次之」给提议排序。
//
// 规则 > 历史 > AI：便宜的层先出结果，AI 只在前面都没命中时才用。
// 同层内置信度高的优先。
func Rank(ps []Scored) []Scored {
	out := append([]Scored(nil), ps...)
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := layerRank(out[i].Layer), layerRank(out[j].Layer)
		if li != lj {
			return li < lj
		}
		return out[i].EffectiveConfidence() > out[j].EffectiveConfidence()
	})
	return out
}

func layerRank(l Layer) int {
	switch l {
	case LayerRule:
		return 0
	case LayerHistory:
		return 1
	case LayerAI:
		return 2
	default:
		return 3
	}
}

// Scored 是一条带来源与置信度的提议。
type Scored struct {
	Proposal *Proposal
	Layer    Layer
	// Confidence 是这条提议的置信度。
	//
	// 规则与历史两层没有模型自评，但可以有各自的「把握程度」
	// （规则完全匹配 = 1.0，历史相似度 = 相似度分值）。
	// 留空时回退到 Proposal.Confidence。
	Confidence float64
	// Source 说明这条提议从哪来（规则名 / 历史凭证号 / 模型名）。
	Source string
}

// EffectiveConfidence 返回用于排序的置信度。
//
// 两个字段并存很容易被写错（一个设了另一个没设），
// 所以把「用哪个」收敛到一个方法里，而不是让每个调用点各自判断。
func (s Scored) EffectiveConfidence() float64 {
	if s.Confidence != 0 {
		return s.Confidence
	}
	if s.Proposal != nil {
		return s.Proposal.Confidence
	}
	return 0
}
