package aiprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"miniaccount/internal/domain/ai"
)

// ---------------------------------------------------------------------------
// 对话式会计
// ---------------------------------------------------------------------------
//
// 与 Suggester 的区别：Suggester 是**单次任务** —— 给它一段业务信息，
// 要么给出凭证，要么在 warnings 里说「信息不够」。它没有嘴，问不了人。
//
// 而真实的记账不是这样。会计拿到「昨天买了台打印机」这句话，
// 第一反应是问「多少钱、开了票没有、付了现金还是挂账」——
// 问完才动笔。这一节就是把「能问」这件事补上。
//
// ## 为什么追问做成工具，而不是让模型输出一个 "action":"ask"
//
// 因为**工具调用是模型本来就会用的东西**。让它多学一套动作协议，
// 等于多一个会走样的地方；而给它一个 ask_user 工具，
// 和给它 search_accounts 是同一件事，模型不需要额外学。
//
// 这个做法来自 deepseek-harness 的 ask_user_question：
// 追问是一个**阻塞式工具**，被调用时这一轮就结束，等用户回答。
// 选项的组织方式也照它来 —— 推荐项放第一个并在 label 末尾加「（推荐）」，
// 是一个纯文案约定，不需要额外的字段。

// AskUserTool 返回「问用户」这个工具。
//
// 它的 Run 不会被调用（Terminal 为真时循环直接结束）——
// 留一个返回错的实现是为了「万一有人误用」时能被立刻发现，
// 而不是静默地什么也不做。
func AskUserTool() Tool {
	return Tool{
		Name: "ask_user",
		// 描述是模型选工具的唯一依据，所以写成「什么时候用」而不是「这是什么」。
		// 措辞风格与其余工具一致：先一句祈使句说明用途，再一句讲清楚回什么。
		Description: "当你缺少**影响科目或金额**的关键信息时，用它向用户提一个问题，" +
			"然后停下来等回答。一次只问一个最关键的问题 —— " +
			"用户答完之后你还会再有机会问。选项里你推荐的那个放第一个，" +
			"并在 label 末尾加「（推荐）」。能从账套里查到的（科目、往来单位、" +
			"历史记法）不要问用户，用查询工具自己查。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "这个问题的稳定标识，回答里会原样带回来（例如 payment、vat、dept）。同一段对话里不要重复用同一个 id。"
    },
    "header": {
      "type": "string",
      "description": "问题的短标题，两到六个字，例如「付款方式」「是否含税」。界面把它显示在问题上方。"
    },
    "question": {
      "type": "string",
      "description": "要问用户的那句话。写具体，不要写「请补充信息」这种等于没说的话。"
    },
    "options": {
      "type": "array",
      "description": "可选项。能穷举就给选项；穷举不了（比如具体金额）就留空，界面会给一个自由输入框。",
      "items": {
        "type": "object",
        "properties": {
          "label": {"type": "string", "description": "用户看到的一句话。推荐项放第一个，末尾加「（推荐）」。"},
          "description": {"type": "string", "description": "一句话说清这个选择的后果，例如「挂应付账款，下月付款时再冲」。"}
        },
        "required": ["label"]
      }
    },
    "multi_select": {
      "type": "boolean",
      "description": "用户是否可以选多个。默认 false。只有确实可能同时成立时才设 true（例如「这笔费用涉及哪几个部门」），能单选就别开。"
    }
  },
  "required": ["id", "question"]
}`),
		Terminal: true,
		RenderAsk: func(args json.RawMessage) string {
			q, err := ParseQuestion(string(args))
			if err != nil {
				return ""
			}
			var b strings.Builder
			fmt.Fprintf(&b, "（向用户提问 #%s）", q.ID)
			if q.Header != "" {
				fmt.Fprintf(&b, "%s：", q.Header)
			}
			b.WriteString(q.Question)
			if len(q.Options) > 0 {
				labels := make([]string, 0, len(q.Options))
				for _, o := range q.Options {
					labels = append(labels, o.Label)
				}
				fmt.Fprintf(&b, "\n选项：%s", strings.Join(labels, " ｜ "))
			}
			if q.MultiSelect {
				b.WriteString("\n（可多选）")
			}
			return b.String()
		},
	}
}

// AccountantQuestion 是会计向用户提的一个问题。
//
// 字段与 deepseek-harness 的 ask_user_question 对齐：
// id / header / question / options / multi_select。
type AccountantQuestion struct {
	// ID 是这个问题的稳定标识，回答里原样带回。
	//
	// ★ 它不是为了好看：模型一次可能问好几个问题（虽然本工程的提示词
	// 要求一次只问一个），而回答是**按 id 配对的**。
	// 靠问题正文去配对，用户把问题复述错一个字就配不上了。
	ID string `json:"id"`
	// Header 是短标题，如「付款方式」。
	Header string `json:"header"`
	// Question 是问题本身。
	Question string `json:"question"`
	// Options 是可选项；为空表示让用户自由输入。
	Options []AccountantOption `json:"options"`
	// MultiSelect 为真表示可以选多个。
	//
	// ★ 标签是 snake_case，与**模型协议**一致（工具参数、凭证 JSON 都是
	// 这个风格）；发给前端的 service.AccountantQuestion 才用 camelCase。
	// 两边各按各的约定，不混。
	MultiSelect bool `json:"multi_select"`
}

// AccountantOption 是一个选项。
type AccountantOption struct {
	Label string `json:"label"`
	// Description 说明这个选择的后果。
	Description string `json:"description"`
	// Recommended 为真表示这是第一个选项（约定：推荐项放第一 +
	// label 末尾带「（推荐）」）。这里再解析一次，是为了界面能加高亮 ——
	// 光靠字符串匹配「（推荐）」在用户改了文案之后就会失效。
	Recommended bool `json:"recommended"`
}

// AccountantReply 是会计的一次应答。
type AccountantReply struct {
	// Say 是会计说的话（模型给最终答案时的 reasoning；
	// 提问时为空 —— 那时要说的话就是问题本身）。
	Say string
	// Question 非空表示会计在等用户回答。
	Question *AccountantQuestion
	// Aux 非空表示会计提议新建一条档案（部门 / 员工），等用户确认。
	//
	// 与 Question 一样是**终止型**：它调用 propose_new_aux 就结束这一轮。
	Aux *AuxProposal
	// Proposal 非空表示会计给出了凭证提议（**尚未过护栏**）。
	Proposal *ai.Proposal
	// Raw 是模型返回的原始内容，进审计表。
	Raw string
	// ToolCalls 是本次用到的工具，进审计表。
	ToolCalls []ToolCallRecord
	// Model / TokensIn / TokensOut 是成本信息。
	Model     string
	TokensIn  int
	TokensOut int
	// Turns 是本次 Agent 循环实际用掉的轮数。
	Rounds int
}

// Accountant 是对话式会计。
type Accountant struct {
	Provider Provider
	Tools    *ToolSet
	Options  AgentOptions
	// maxAsks 是本次会话已经问过几轮（由调用方维护并传进来）。
	//
	// ★ 必须有上限。会问问题的模型偶尔会**一直问下去** ——
	// 用户的耐心比 token 便宜得多，问到第四轮还没出凭证，
	// 他就回去手工录了，这个功能也就白做了。
	Asked int
	// MaxAsks 是最多问几轮，默认 3。
	MaxAsks int
}

// DefaultMaxAsks 是默认的最多追问轮数。
const DefaultMaxAsks = 3

// Reply 在给定对话历史上跑一轮会计应答。
//
// history 由调用方持有（`[]Message`），返回值是追加后的完整历史 ——
// 会计要记得住用户上一句说了什么，而那句话在历史里。
func (a *Accountant) Reply(ctx context.Context, history []Message) (*AccountantReply, []Message, error) {
	if a.Provider == nil {
		return nil, history, ErrNoProvider
	}
	opts := a.Options
	if opts.MaxRounds <= 0 {
		opts = DefaultAgentOptions()
	}
	maxAsks := a.MaxAsks
	if maxAsks <= 0 {
		maxAsks = DefaultMaxAsks
	}

	// 问够了就不再给提问工具 —— 从**工具层面**断掉，
	// 而不是在提示词里求它别再问了（求不动的）。
	tools := a.Tools
	if a.Asked >= maxAsks && tools != nil {
		tools = withoutAsk(tools)
	}
	// 兜底：整条路必须有提问工具，否则「信息不全就猜」的问题原样还在。
	if tools == nil || tools.Len() == 0 {
		tools = NewToolSet(AskUserTool())
	}

	agent := &Agent{Provider: a.Provider, Tools: tools, Options: opts}
	res, out, err := agent.RunConversation(ctx, history)
	reply := &AccountantReply{Rounds: res.Rounds}
	reply.Raw = res.Content
	reply.ToolCalls = res.ToolCalls
	reply.TokensIn, reply.TokensOut = res.TokensIn, res.TokensOut

	if res.Asked != nil {
		if res.Asked.Name == "propose_new_aux" {
			p, perr := ParseAuxProposal(res.Asked.Args)
			if perr != nil {
				return reply, out, fmt.Errorf("ai: 解析档案提议失败（%w）：%s",
					perr, res.Asked.Args)
			}
			reply.Aux = p
			return reply, out, nil
		}
		q, qerr := ParseQuestion(res.Asked.Args)
		if qerr != nil {
			// 问题本身没解析出来：当成一次失败，但要带上原始参数，
			// 否则用户看到的是「AI 没反应」，谁也查不出为什么。
			return reply, out, fmt.Errorf("ai: 解析追问失败（%w）：%s", qerr, res.Asked.Args)
		}
		reply.Question = q
		return reply, out, nil
	}
	if err != nil {
		return reply, out, err
	}
	prop, perr := ai.Parse(res.Content)
	if perr != nil {
		return reply, out, perr
	}
	reply.Proposal = prop
	reply.Say = prop.Reasoning
	return reply, out, nil
}

// ParseQuestion 解析 ask_user 工具的参数。
func ParseQuestion(raw string) (*AccountantQuestion, error) {
	var q AccountantQuestion
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		return nil, err
	}
	if strings.TrimSpace(q.Question) == "" {
		return nil, fmt.Errorf("问题为空")
	}
	// id 缺失时补一个，而不是报错。
	//
	// 报错会让用户看到「AI 失败了」，而其实问题本身是好的、能回答的 ——
	// 缺的只是一个配对用的标识。补一个默认值，回答照样配得上。
	if strings.TrimSpace(q.ID) == "" {
		q.ID = "answer"
	}
	// 约定的推荐项：第一个选项 + label 末尾带「（推荐）」。
	// 两者都认 —— 模型偶尔只做对一半，界面不该因此就不高亮。
	for i := range q.Options {
		if i == 0 && strings.Contains(q.Options[i].Label, "推荐") {
			q.Options[i].Recommended = true
		}
	}
	return &q, nil
}

// withoutAsk 去掉提问工具（问够轮数之后用）。
func withoutAsk(ts *ToolSet) *ToolSet {
	var keep []Tool
	for _, name := range ts.Names() {
		if name == "ask_user" {
			continue
		}
		if t, ok := ts.Get(name); ok {
			keep = append(keep, t)
		}
	}
	return NewToolSet(keep...)
}

// AccountantAnswer 是一次用户回答。
//
// 形状照 deepseek-harness 的 `{id, selected[], custom?}`：
// 「点了哪个选项」与「自己打了什么字」是两件事，必须分开。
type AccountantAnswer struct {
	// QuestionID 是被回答的那个问题的 id。
	QuestionID string
	// Selected 是用户点选的选项 label（可能多个，见 multi_select）。
	Selected []string
	// Custom 是用户自己打的字。
	Custom string
}

// AnswerMessage 把一次回答渲染成回灌给模型的文本。
//
// ★ 必须带上「这是在回答你上一个问题」，而且要点明是**点的选项**
// 还是**自己打的字**。
//
// 只回「银行」两个字，模型下一轮可能把它当成一句新的业务描述；
// 而「银行转账，取得专用发票」是点来的还是打来的，含义也不同 ——
// 点来的是它在选项里写明的、有明确后果的那一条，
// 打来的是用户临时想到的，可能需要再确认一次。
func AnswerMessage(q *AccountantQuestion, ans AccountantAnswer) string {
	var b strings.Builder
	if q != nil && strings.TrimSpace(q.Question) != "" {
		id := strings.TrimSpace(q.ID)
		if id == "" {
			id = "answer"
		}
		fmt.Fprintf(&b, "（回答你刚才的问题 #%s：%s）\n", id, q.Question)
	}
	if len(ans.Selected) > 0 {
		fmt.Fprintf(&b, "选中的选项：%s\n", strings.Join(ans.Selected, "、"))
	}
	if c := strings.TrimSpace(ans.Custom); c != "" {
		if len(ans.Selected) > 0 {
			fmt.Fprintf(&b, "用户补充：%s\n", c)
		} else {
			fmt.Fprintf(&b, "用户回答：%s\n", c)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "（用户没有回答）"
	}
	return out
}

// QuestionMessage 是 AnswerMessage 的自由输入版（用户直接打字）。
//
// 保留它是因为调用方常常只有一段文字；两者渲染出来的东西必须一致，
// 所以这里只是包一层。
func QuestionMessage(q *AccountantQuestion, answer string) string {
	return AnswerMessage(q, AccountantAnswer{Custom: answer})
}
