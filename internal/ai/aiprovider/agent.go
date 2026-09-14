package aiprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"miniaccount/internal/domain/ai"
	"miniaccount/internal/domain/money"
)

// ---------------------------------------------------------------------------
// Agent 工具
// ---------------------------------------------------------------------------

// Tool 是一个**只读**工具。
//
// ★ 有副作用的工具一概不提供。
//
// 模型能做的唯一事情是「多问一句、多看一眼」。它拿不到写数据库的能力，
// 能造成的最坏后果是返回一段通不过护栏的 JSON —— 然后被记进审计表。
//
// 这条边界不是保守，是必须：一个能「顺手把凭证存了」的 Agent，
// 在账务场景里的风险远大于它带来的便利。
type Tool struct {
	// Name 是工具名，模型用它来调用。
	Name string
	// Description 要写清楚「什么时候该用它」。
	//
	// 模型的工具选择完全依赖这段描述 —— 写成「查询科目」它不会知道
	// 该在把银行流水映射到科目时用；写成「把用户说的『银行存款』
	// 映射到账套里真实的科目编码」它才会用对时机。
	Description string
	// Parameters 是 JSON Schema 形式的参数说明。
	Parameters json.RawMessage
	// Run 执行工具，返回给模型看的结果（通常是一段文本或 JSON）。
	Run func(ctx context.Context, args json.RawMessage) (string, error)
	// Terminal 为真表示这个工具**不是查一下再继续**，而是「停下来问用户」。
	//
	// 模型调用它时，循环立即结束，把这次调用原样交给上层去问人 ——
	// 结果不是给模型看的，是给用户看的。因此它不需要 Run。
	//
	// ★ 用一个标记位而不是「返回一个特殊错误」：错误会被回灌给模型
	// 让它重试，而这里要的是**正常结束**，不是失败。
	Terminal bool
}

// ToolSet 是工具集合。
type ToolSet struct {
	tools map[string]Tool
	order []string
}

// NewToolSet 构造工具集。
func NewToolSet(tools ...Tool) *ToolSet {
	ts := &ToolSet{tools: map[string]Tool{}}
	for _, t := range tools {
		if t.Name == "" {
			continue
		}
		ts.tools[t.Name] = t
		ts.order = append(ts.order, t.Name)
	}
	sort.Strings(ts.order)
	return ts
}

// Get 按名取工具。
func (ts *ToolSet) Get(name string) (Tool, bool) {
	if ts == nil {
		return Tool{}, false
	}
	t, ok := ts.tools[name]
	return t, ok
}

// Len 返回工具数。
func (ts *ToolSet) Len() int {
	if ts == nil {
		return 0
	}
	return len(ts.tools)
}

// Names 返回全部工具名（已排序，便于测试与展示）。
func (ts *ToolSet) Names() []string {
	if ts == nil {
		return nil
	}
	return append([]string(nil), ts.order...)
}

// ---------------------------------------------------------------------------
// Agent 循环
// ---------------------------------------------------------------------------

// AgentOptions 是 Agent 循环的参数。
type AgentOptions struct {
	// MaxRounds 是工具调用的最大轮数。
	//
	// ★ 必须有上限。模型可能陷入「反复查同一个科目」的循环，
	// 而每一次调用都是真金白银（云端）或真占 CPU（本地）。3 轮足够：
	// 小微企业的记账场景里，需要查三次以上才能定的业务极其罕见。
	MaxRounds int
	// MaxTokens 是单次调用的 token 上限。
	MaxTokens int
	// Temperature 建议 0。
	Temperature float64
}

// DefaultAgentOptions 返回默认参数。
func DefaultAgentOptions() AgentOptions {
	return AgentOptions{MaxRounds: 3, MaxTokens: 2048, Temperature: 0}
}

// AgentResult 是一次 Agent 会话的结果。
type AgentResult struct {
	// Content 是模型最终返回的那段 JSON（未解析）。
	Content string
	// Rounds 是实际用掉的轮数。
	Rounds int
	// ToolCalls 是本次会话调用过的工具，按顺序。
	//
	// 留它是为了两件事：审计（「这条建议是怎么得出的」）
	// 与调优（「模型老是先查科目再查历史，是不是提示词该改」）。
	ToolCalls []ToolCallRecord
	// Usage 是累计 token 消耗。
	TokensIn  int
	TokensOut int
	// Truncated 为真表示轮数用尽仍未得到最终答案。
	Truncated bool
	// Asked 非空表示本轮以「向用户提问」结束（模型调用了 Terminal 工具）。
	Asked *ToolCallRecord
}

// ToolCallRecord 记录一次工具调用。
type ToolCallRecord struct {
	Round  int
	Name   string
	Args   string
	Result string
	Err    string
}

// Agent 是带工具调用的编排循环。
type Agent struct {
	Provider Provider
	Tools    *ToolSet
	Options  AgentOptions
}

// ErrNoFinalAnswer 表示轮数用尽仍未拿到最终答案。
var ErrNoFinalAnswer = errors.New("ai: Agent 在限定轮数内没有给出最终结果")

// Run 执行 Agent 循环，返回模型最终的 JSON。
//
// 循环非常简单：发给模型 → 它要么给最终答案、要么要求调用工具 →
// 执行工具、把结果追加进对话 → 再来一轮。
//
// 之所以不做得更复杂（不做规划、不做反思），是因为这个场景本身简单：
// 模型要判断的是「这笔业务该记哪些科目」，需要的额外信息无非
// 「账套里有没有这个科目」「上次类似的怎么记的」。三轮之内一定能问完。
func (a *Agent) Run(ctx context.Context, system, user string) (*AgentResult, error) {
	res, _, err := a.RunConversation(ctx, []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	})
	return res, err
}

// RunConversation 在**给定的对话历史**上跑一轮 Agent 循环。
//
// 与 Run 的区别只有一个：Run 是「一问一答」（system + 一句用户输入 →
// 最终 JSON，历史留在函数里），RunConversation 把历史交回给调用方。
//
// ★ 需要它是因为「会计」这个场景是多轮的：他可能先问一句
// 「这笔是含税价还是不含税价？」再编凭证。要让追问成立，
// 模型必须看得见之前说过的话 —— 而那句话不在本次调用里，
// 在上一次调用里。历史因此必须由上层保存。
//
// 返回的 history 是**追加了本轮全部消息之后**的完整历史，
// 调用方应当原样存下来，下次接着传进来。
func (a *Agent) RunConversation(ctx context.Context, history []Message) (*AgentResult, []Message, error) {
	if a.Provider == nil {
		return nil, history, ErrNoProvider
	}
	opts := a.Options
	if opts.MaxRounds <= 0 {
		opts = DefaultAgentOptions()
	}

	res := &AgentResult{}
	// ★ 工具结果要按原样回灌给模型 ——
	// 模型是靠看到工具返回的内容来决定下一步的。
	history = append([]Message(nil), history...)

	for round := 1; round <= opts.MaxRounds; round++ {
		res.Rounds = round
		req := Request{
			Messages: history, JSONMode: true,
			Temperature: opts.Temperature, MaxTokens: opts.MaxTokens,
			Tools: a.toolSpecs(),
		}

		resp, err := a.Provider.Complete(ctx, req)
		if err != nil {
			return res, history, err
		}
		res.TokensIn += resp.TokensIn
		res.TokensOut += resp.TokensOut

		// 没有工具调用就是最终答案
		if len(resp.ToolCalls) == 0 {
			res.Content = resp.Content
			history = append(history, Message{Role: "assistant", Content: resp.Content})
			return res, history, nil
		}

		// 把模型的工具调用意图记进对话
		history = append(history, Message{
			Role: "assistant", Content: resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		for _, tc := range resp.ToolCalls {
			rec := ToolCallRecord{
				Round: round, Name: tc.Name, Args: tc.Arguments,
			}
			tool, ok := a.Tools.Get(tc.Name)
			// ★ 终止型工具（问用户）：不执行、不续轮，直接结束本次循环。
			//
			// 历史里要留下这次调用，下一轮用户答完之后接着往下说 ——
			// 所以 assistant 的那条消息已经在上面追加过了。
			if ok && tool.Terminal {
				res.Asked = &rec
				res.ToolCalls = append(res.ToolCalls, rec)
				return res, history, nil
			}
			if !ok {
				// ★ 未知工具不报错，而是把「没有这个工具」告诉模型。
				//
				// 直接失败会让一次小失误（模型拼错工具名）毁掉整次建议；
				// 而告诉它有哪些工具，它下一轮通常就能自己改正。
				rec.Err = "没有名为 " + tc.Name + " 的工具"
				if a.Tools != nil {
					rec.Err += "；可用工具：" + strings.Join(a.Tools.Names(), "、")
				}
				history = append(history, toolResultMessage(tc.ID, toolErrorText(rec.Err)))
				res.ToolCalls = append(res.ToolCalls, rec)
				continue
			}
			out, terr := tool.Run(ctx, rawOrEmpty(tc.Arguments))
			if terr != nil {
				rec.Err = terr.Error()
				history = append(history, toolResultMessage(tc.ID, toolErrorText(terr.Error())))
			} else {
				rec.Result = out
				history = append(history, toolResultMessage(tc.ID, out))
			}
			res.ToolCalls = append(res.ToolCalls, rec)
		}
	}

	res.Truncated = true
	return res, history, fmt.Errorf("%w（已用 %d 轮）", ErrNoFinalAnswer, opts.MaxRounds)
}

// ParseAgentResult 把 Agent 的最终输出解析成提议。
//
// 与直接调用 Provider 的结果走**同一套解析与护栏** ——
// 多了一轮工具调用，不该让校验标准有任何放松。
func ParseAgentResult(res *AgentResult) (*ai.Proposal, error) {
	if res == nil || res.Content == "" {
		return nil, fmt.Errorf("%w: Agent 没有返回内容", ai.ErrMalformedJSON)
	}
	return ai.Parse(res.Content)
}

func (a *Agent) toolSpecs() []ToolSpec {
	if a.Tools == nil || a.Tools.Len() == 0 {
		return nil
	}
	out := make([]ToolSpec, 0, a.Tools.Len())
	for _, name := range a.Tools.Names() {
		t, _ := a.Tools.Get(name)
		out = append(out, ToolSpec{
			Name: t.Name, Description: t.Description, Parameters: t.Parameters,
		})
	}
	return out
}

func rawOrEmpty(s string) json.RawMessage {
	if strings.TrimSpace(s) == "" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(s)
}

// toolErrorText 把工具失败渲染成回灌给模型的那段文字。
//
// ★ 统一加 `Error: ` 前缀，是照 deepseek-harness 的做法
// （它那边是 `Error: <message>` + `isError: true`）。
//
// 为什么非要有这个前缀：工具结果是**和正常结果走同一条通道**回灌的，
// 模型看到的是两段长得差不多的文本。不加前缀时，一段「没有匹配的科目」
// 与一段「数据库打不开」在它眼里没有区别 —— 而前者该换个词重试，
// 后者该停下来把问题写进 warnings。前缀是模型唯一能区分二者的线索。
//
// isError 那个字段这里没有：本工程走 OpenAI 兼容的 chat completions，
// 协议里 tool 消息只有 role / tool_call_id / content，
// 没有地方放一个布尔位。加了也是死的，不如不加。
func toolErrorText(msg string) string {
	if strings.HasPrefix(msg, "Error: ") {
		return msg
	}
	return "Error: " + msg
}

func toolResultMessage(id, content string) Message {
	return Message{Role: "tool", Content: content, ToolCallID: id}
}

// ---------------------------------------------------------------------------
// 内置工具
// ---------------------------------------------------------------------------

// AccountBriefProvider 提供科目查询能力（由存储层实现）。
type AccountBriefProvider interface {
	Accounts(ctx context.Context) ([]AccountBrief, error)
}

// ContactBriefProvider 提供往来单位查询能力。
type ContactBriefProvider interface {
	Contacts(ctx context.Context) ([]ContactBrief, error)
}

// SearchAccountsTool 构造「按关键词搜科目」工具。
//
// ★ 这是最常用的一个：模型最容易犯的错是把用户说的
// 「银行存款」直接当成科目名，而账套里可能叫「银行存款—工行」。
// 让它可以查，比在提示词里塞一份完整科目表更省 token，也更准。
func SearchAccountsTool(p AccountBriefProvider) Tool {
	return Tool{
		Name: "search_accounts",
		Description: "按关键词搜索本账套里可记账的科目，返回编码、名称、" +
			"余额方向与所需的辅助核算维度。" +
			"当你需要把用户说的科目名（如「银行存款」「办公费」）" +
			"映射到账套里真实的科目编码时使用它。" +
			"**只能使用它返回的编码**，不要自己编造。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"keyword": {"type": "string", "description": "科目名称或编码的片段，留空返回全部"},
				"limit": {"type": "integer", "description": "最多返回多少条，默认 20"}
			},
			"required": ["keyword"]
		}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Keyword string `json:"keyword"`
				Limit   int    `json:"limit"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("参数格式错误: %w", err)
			}
			all, err := p.Accounts(ctx)
			if err != nil {
				return "", err
			}
			limit := args.Limit
			if limit <= 0 {
				limit = 20
			}
			kw := strings.ToLower(strings.TrimSpace(args.Keyword))
			var hits []AccountBrief
			for _, a := range all {
				if kw != "" && !strings.Contains(strings.ToLower(a.Code+" "+a.FullName+" "+a.Name), kw) {
					continue
				}
				hits = append(hits, a)
				if len(hits) >= limit {
					break
				}
			}
			if len(hits) == 0 {
				return "没有匹配的科目。请换一个关键词，或把问题写进 warnings。", nil
			}
			var b strings.Builder
			for _, a := range hits {
				fmt.Fprintf(&b, "%s %s", a.Code, a.FullName)
				if a.Direction != "" {
					fmt.Fprintf(&b, "（%s）", a.Direction)
				}
				if len(a.AuxTypes) > 0 {
					fmt.Fprintf(&b, " [必填辅助核算：%s]", strings.Join(a.AuxTypes, "、"))
				}
				b.WriteByte('\n')
			}
			return b.String(), nil
		},
	}
}

// SearchContactsTool 构造「按关键词搜往来单位」工具。
func SearchContactsTool(p ContactBriefProvider) Tool {
	return Tool{
		Name: "search_contacts",
		Description: "按名称或简称搜索本账套的往来单位，返回 id、名称与类型。" +
			"当科目要求客户/供应商等辅助核算、而你需要确定 contact_id 时使用。" +
			"**只能使用它返回的 id**。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"keyword": {"type": "string", "description": "名称或简称的片段，留空返回全部"},
				"kind": {"type": "string", "description": "限定类型：customer/supplier/shareholder/employee/other"},
				"limit": {"type": "integer", "description": "最多返回多少条，默认 20"}
			},
			"required": ["keyword"]
		}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Keyword string `json:"keyword"`
				Kind    string `json:"kind"`
				Limit   int    `json:"limit"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("参数格式错误: %w", err)
			}
			all, err := p.Contacts(ctx)
			if err != nil {
				return "", err
			}
			limit := args.Limit
			if limit <= 0 {
				limit = 20
			}
			kw := strings.ToLower(strings.TrimSpace(args.Keyword))
			var b strings.Builder
			n := 0
			for _, c := range all {
				if kw != "" && !strings.Contains(strings.ToLower(c.Name), kw) {
					short := false
					for _, al := range c.Aliases {
						if strings.Contains(strings.ToLower(al), kw) {
							short = true
						}
					}
					if !short {
						continue
					}
				}
				fmt.Fprintf(&b, "%d %s（%s）\n", c.ID, c.Name, c.Kind)
				n++
				if n >= limit {
					break
				}
			}
			if n == 0 {
				return "没有匹配的往来单位。若这个单位还没建档，请把问题写进 warnings " +
					"并留空 contact_id —— 不要编造 id。", nil
			}
			return b.String(), nil
		},
	}
}

// HistoryProvider 提供历史凭证检索能力。
type HistoryProvider interface {
	Similar(ctx context.Context, text, counterparty string,
		amountMoney int64, limit int) ([]Example, error)
}

// FindSimilarVouchersTool 构造「找历史同类凭证」工具。
//
// ★ 这是整套工具里最有价值的一个。
//
// 小微企业 80% 的银行流水是重复的：同一个客户、同一个供应商、
// 同一个金额量级。用户上次怎么记的，这次就该怎么记。
// 让模型能主动去查，比在提示词里塞几条固定范例更准 ——
// 因为它可以带着具体的对手方与金额去查。
func FindSimilarVouchersTool(p HistoryProvider) Tool {
	return Tool{
		Name: "find_similar_vouchers",
		Description: "检索本账套里与给定业务描述相似的历史凭证及其分录。" +
			"**这是最有用的线索**：同一个对手方、同一类摘要，上次怎么记的这次就该怎么记。" +
			"当你不确定某笔业务该记哪些科目时，先查它。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"text": {"type": "string", "description": "业务描述或银行流水摘要"},
				"counterparty": {"type": "string", "description": "对方户名，填了会明显更准"},
				"amount": {"type": "integer", "description": "金额（分），用于匹配同一量级的业务"},
				"limit": {"type": "integer", "description": "最多返回几条，默认 3"}
			},
			"required": ["text"]
		}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Text         string `json:"text"`
				Counterparty string `json:"counterparty"`
				Amount       int64  `json:"amount"`
				Limit        int    `json:"limit"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("参数格式错误: %w", err)
			}
			limit := args.Limit
			if limit <= 0 {
				limit = 3
			}
			ex, err := p.Similar(ctx, args.Text, args.Counterparty, args.Amount, limit)
			if err != nil {
				return "", err
			}
			if len(ex) == 0 {
				return "账套里没有找到相似的历史凭证 —— 这说明这可能是一笔新业务，" +
					"请按会计原则自行判断。", nil
			}
			return FormatExamples(ex), nil
		},
	}
}

// FormatExamples 把历史范例渲染成给模型看的文本。
//
// 与提示词里的「历史同类凭证」段落用**同一个格式** ——
// 模型在提示词里见过的样子，与工具返回的样子一致时，它更容易用好。
func FormatExamples(ex []Example) string {
	var b strings.Builder
	for i, e := range ex {
		fmt.Fprintf(&b, "## 例 %d（相似度 %.2f）%s  %s\n", i+1, e.Score, e.VoucherNo, e.Date)
		if e.Text != "" {
			fmt.Fprintf(&b, "原始描述：%s\n", e.Text)
		}
		if e.Remark != "" {
			fmt.Fprintf(&b, "凭证备注：%s\n", e.Remark)
		}
		for _, l := range e.Lines {
			switch {
			case l.Debit.IsPositive():
				fmt.Fprintf(&b, "  借 %s %s  %s", l.AccountCode, l.AccountName, l.Debit)
			case l.Credit.IsPositive():
				fmt.Fprintf(&b, "  贷 %s %s  %s", l.AccountCode, l.AccountName, l.Credit)
			default:
				continue
			}
			if l.ContactName != "" {
				fmt.Fprintf(&b, "（%s）", l.ContactName)
			}
			if l.Summary != "" {
				fmt.Fprintf(&b, "  摘要：%s", l.Summary)
			}
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// 保证 money 包被引用（工具参数里用「分」表示金额）。
var _ = money.Money(0)
