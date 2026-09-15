package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"miniaccount/internal/ai/aiprovider"
	"miniaccount/internal/domain/ai"
	"miniaccount/internal/domain/evidence"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/workpaper"
	"miniaccount/internal/store/sqlite"
)

// ---------------------------------------------------------------------------
// 会计（对话式记账）
// ---------------------------------------------------------------------------
//
// # 为什么要做成对话，而不是一个「生成建议」按钮
//
// 用户脑子里的一笔业务是「昨天买了台打印机」，不是一个填好的表单。
// 原来的界面要求他先选业务类型、再填金额、日期、对方户名、资金方向 ——
// 那是**会计要问的问题**，不是用户想说的话。结果是：
// 填不全就生成，生成了不对再改，改完还是不对。
//
// 真实的会计不是这样。他先听你说，缺什么问什么，问够了才动笔。
// 这一节就是把「会问」这件事补上：
//
//	用户：昨天买了台打印机
//	会计：这台打印机多少钱？（追问，带选项）
//	用户：3000，开了专票
//	会计：（给出凭证草稿）
//
// # 会话存在哪
//
// 存在**内存**里（进程内的一张表，超过 20 段就丢掉最旧的）。
//
// 账务数据是那张凭证草稿，它一落库就有完整轨迹（建议记录 + 操作日志），
// 对话本身不是账务数据。
//
// ★ 这一条与 deepseek-harness 的做法**不一样**，是有意的偏离：
// 它那边把对话做成 append-only 事件日志，请求由日志派生，可重放、可复算。
// 记账场景确实吃这一套，但代价是给一次聊天式交互加上一张事件表
// 和一套派生逻辑 —— 而这里真正需要留痕的东西（模型原话、结构化提议、
// 护栏明细、采纳与否）已经全部落在 ai_suggestion 表里，
// 缺的只是「用户当时那句话」。
//
// 所以取舍是：**先不持久化对话**。要补的话，正确的做法不是把
// []Message 存成一段 JSON，而是补一张 append-only 的会话事件表。
//
// 代价是重启丢会话。可以接受：重开一个，重新描述一句就是了。

// AccountantOption 是一个可选回答。
type AccountantOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	// Recommended 为真表示这是会计推荐的那个（选项里的第一个）。
	Recommended bool `json:"recommended"`
}

// AccountantQuestion 是会计的追问。
type AccountantQuestion struct {
	// ID 是问题的稳定标识，回答时原样带回（见 AccountantSendInput.AnswerID）。
	ID string `json:"id"`
	// Header 是短标题，如「付款方式」。
	Header string `json:"header"`
	// Question 是问题本身。
	Question string `json:"question"`
	// Options 可空：空表示让用户自由输入。
	Options []AccountantOption `json:"options"`
	// MultiSelect 为真表示可以选多个。
	MultiSelect bool `json:"multiSelect"`
}

// AuxProposalView 是「建议新建一条辅助核算档案」。
type AuxProposalView struct {
	// Kind 是 department | employee。
	Kind string `json:"kind"`
	// KindLabel 是中文名。
	KindLabel string `json:"kindLabel"`
	// Name / Code 是拟建的名称与编码。
	Name string `json:"name"`
	Code string `json:"code"`
	// DeptID 是员工所属部门（仅 employee）。
	DeptID *int64 `json:"deptId"`
	// Reason 是为什么要建 —— 显示给用户，也进审计。
	Reason string `json:"reason"`
}

// HRProposalView 是「建议对某位员工做一次人事异动」。
type HRProposalView struct {
	// Kind 是 resign | transfer | salary。
	Kind string `json:"kind"`
	// KindLabel 是中文名。
	KindLabel string `json:"kindLabel"`
	// EmployeeID / EmployeeName 是目标员工。
	EmployeeID   int64  `json:"employeeId"`
	EmployeeName string `json:"employeeName"`
	// DeptID / DeptName 是调入部门（transfer）。
	DeptID   *int64 `json:"deptId"`
	DeptName string `json:"deptName"`
	// LeaveDate 是离职日期（resign）。
	LeaveDate string `json:"leaveDate"`
	// BaseSalary / SIBase / HFBBase 是调整后的金额（salary，单位元）。
	BaseSalary string `json:"baseSalary"`
	SIBase     string `json:"siBase"`
	HFBBase    string `json:"hfbBase"`
	// Reason 是原因，会写进操作日志。
	Reason string `json:"reason"`
	// Detail 是一句给人看的说明（由服务层拼好）。
	Detail string `json:"detail"`
}

// AdjustmentProposalView 是「建议登记一笔审计调整」。
//
// ★ 底下这些都只是**提议**：模型没有写底稿的能力。
// 界面把这张卡摊开，用户按下确认之后由界面调 SaveAdjustment ——
// 那一步才真的落库，而且走的仍是完整的凭证护栏。
type AdjustmentProposalView struct {
	// Period / Year / Month 是调整所属期间。
	Period string `json:"period"`
	Year   int    `json:"year"`
	Month  int    `json:"month"`
	// Kind 是 adjust | reclass。
	Kind      string `json:"kind"`
	KindLabel string `json:"kindLabel"`
	Summary   string `json:"summary"`
	Reason    string `json:"reason"`
	Evidence  string `json:"evidence"`
	// Amount 是借方合计（分）。
	Amount money.Money `json:"amount"`
	// Lines 是分录。**带辅助核算 id**：界面要原样把它们交给保存接口，
	// 少了 id 这笔调整就会因为「缺少必需的辅助核算」被拦下，
	// 而用户在卡片上看到的分录明明是齐的。
	Lines []AdjustProposalLineView `json:"lines"`
	// Detail 是一句给人看的说明。
	Detail string `json:"detail"`
	// Problems 是这张提议本身的问题（服务层复核后填）。
	Problems []string `json:"problems"`
}

// AdjustProposalLineView 是提议里的一行分录。
type AdjustProposalLineView struct {
	LineNo      int         `json:"lineNo"`
	AccountCode string      `json:"accountCode"`
	AccountName string      `json:"accountName"`
	Summary     string      `json:"summary"`
	Debit       money.Money `json:"debit"`
	Credit      money.Money `json:"credit"`
	ContactID   *int64      `json:"contactId"`
	EmployeeID  *int64      `json:"employeeId"`
	DeptID      *int64      `json:"deptId"`
	ProjectID   *int64      `json:"projectId"`
	// AuxDesc 是辅助核算的文字描述。
	AuxDesc string `json:"auxDesc"`
}

// EvidenceProposalView 是「建议把某份资料挂到某个结论下」。
//
// ★ 与登记调整一样：模型只提议，落库由界面调 AddEvidence 完成。
type EvidenceProposalView struct {
	OwnerType      string `json:"ownerType"`
	OwnerTypeLabel string `json:"ownerTypeLabel"`
	OwnerID        int64  `json:"ownerId"`
	// OwnerTitle 是那条结论的一句话（由程序查出来填上）。
	OwnerTitle   string `json:"ownerTitle"`
	RefKind      string `json:"refKind"`
	RefKindLabel string `json:"refKindLabel"`
	RefID        int64  `json:"refId"`
	RefLabel     string `json:"refLabel"`
	Note         string `json:"note"`
	Reason       string `json:"reason"`
	// Problems 是这条提议现在还不能落库的原因。
	Problems []string `json:"problems"`
	Detail   string   `json:"detail"`
}

// CPAAnswerView 是一份**专业答复**。
//
// 字段与 CPA 规格里的「输出要求」十项一一对应，外加三条由**程序**
// 判定、不由模型说了算的（见下面每个字段的说明）。
type CPAAnswerView struct {
	// ---- 模型给的十项 ----
	Conclusion      string   `json:"conclusion"`
	Basis           []string `json:"basis"`
	Obtained        []string `json:"obtained"`
	Missing         []string `json:"missing"`
	Process         string   `json:"process"`
	Findings        []string `json:"findings"`
	Risk            string   `json:"risk"`
	Recommendations []string `json:"recommendations"`
	HumanReview     []string `json:"humanReview"`
	// Submittable 是**程序**判定，不是模型说的。
	//
	// ★ 永远为 false。AI 产出的东西不能直接用于正式申报或对外提交 ——
	// 这不是措辞问题，是这份工作的边界。模型声称 true 时，
	// 原文留在 SubmittableClaimed 里供审计，生效值仍是 false。
	Submittable bool `json:"submittable"`
	// SubmittableClaimed 是模型自己声称的值（审计用）。
	SubmittableClaimed bool `json:"submittableClaimed"`
	// PolicyNote 是政策的发布机关 / 适用地区 / 适用期间 / 查询日期。
	PolicyNote string `json:"policyNote"`
	// Text 是给用户看的正文。
	Text string `json:"text"`

	// ---- 程序判定的三条 ----
	// ProblemList 是这份答复本身的问题（结论为空、风险没标、声称可提交…）。
	//
	// ★ 标签必须与界面读的字段名一致（界面写的是 answer.problemList）。
	// 这里原来写成 "problems"，而单元测试断言的是**Go 字段**
	// v.ProblemList —— 两边都「过」了，只有界面悄悄拿不到数据，
	// 问题列表一个字都不显示。是端到端跑一遍才露出来的。
	ProblemList []string `json:"problemList"`
	// Escalation 是**必须转人工**的命中项；空表示没有命中。
	Escalation []string `json:"escalation"`
	// RiskUnstated 为真表示模型没有标注风险等级（已按中等处理）。
	RiskUnstated bool `json:"riskUnstated"`
}

// AccountantTurn 是对话里的一条消息。
type AccountantTurn struct {
	// Role 是 user（用户说的）或 accountant（会计说的）。
	Role string `json:"role"`
	// Text 是正文。
	Text string `json:"text"`
	// Question 非空表示这一条是**在问用户**。
	Question *AccountantQuestion `json:"question,omitempty"`
	// Aux 非空表示这一条是**提议新建档案**（部门 / 员工），等用户确认。
	//
	// ★ 由界面去调建档的绑定，模型全程没有写库能力 ——
	// 与「AI 产物一律先是草稿」是同一条边界。
	Aux *AuxProposalView `json:"aux,omitempty"`
	// HR 非空表示这一条是**提议人事异动**（离职 / 转部门 / 调薪）。
	HR *HRProposalView `json:"hr,omitempty"`
	// Adjustment 非空表示这一条是**提议登记审计调整**，等用户确认。
	Adjustment *AdjustmentProposalView `json:"adjustment,omitempty"`
	// Evidence 非空表示这一条是**提议挂一份审计依据**，等用户确认。
	Evidence *EvidenceProposalView `json:"evidence,omitempty"`
	// Answer 非空表示这一条是**专业答复**（不是凭证）。
	Answer *CPAAnswerView `json:"answer,omitempty"`
	// Voucher 非空表示这一条给出了凭证草稿。
	Voucher *VoucherDraft `json:"voucher,omitempty"`
	// Failures / Warnings 是护栏结论，界面要摊开展示。
	Failures []string `json:"failures,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	// SuggestionID 是这条草稿对应的建议记录 id（采纳时要用）。
	SuggestionID int64 `json:"suggestionId,omitempty"`
	// Passed 为真表示这张草稿通过了全部护栏，可以采纳。
	Passed bool `json:"passed,omitempty"`
	// Model / Tokens 是这个回合的成本。
	Model     string `json:"model,omitempty"`
	TokensIn  int    `json:"tokensIn,omitempty"`
	TokensOut int    `json:"tokensOut,omitempty"`
	// At 是这条消息的时间。
	At string `json:"at"`
}

// AccountantSession 是一次对话。
type AccountantSession struct {
	ID      string           `json:"id"`
	Turns   []AccountantTurn `json:"turns"`
	Asks    int              `json:"asks"`
	Busy    bool             `json:"busy"`
	Updated string           `json:"updated"`
}

// accountantStore 是进程内的会话表。
type accountantStore struct {
	mu       sync.Mutex
	sessions map[string]*accountantSessionState
	order    []string
}

type accountantSessionState struct {
	view    AccountantSession
	history []aiprovider.Message
	// pending 是「上一个问题」，用户下一句话就是回答它。
	pending *aiprovider.AccountantQuestion
	// input 是第一句业务描述（用于护栏里的金额比对与历史检索）。
	input aiprovider.Input
	// lctxDigest 等是审计要用的。
	digest string
}

var accountantSessions = &accountantStore{sessions: map[string]*accountantSessionState{}}

// maxAccountantSessions 是最多同时留几段对话。
//
// 与批量记账的 run 一样要有个上限：每次对话都带着完整的模型上下文
// （科目表、往来单位清单加起来几十 KB），留着不放会一直吃内存。
const maxAccountantSessions = 20

func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------------------
// 对外接口
// ---------------------------------------------------------------------------

// AccountantSendInput 是「跟会计说一句话」。
type AccountantSendInput struct {
	// SessionID 留空表示开一段新对话。
	SessionID string
	// Text 是用户说的话（第一句通常是业务描述）。
	Text string
	// Selected 是用户**点选**的选项 label（点选项时有值，自己打字时为空）。
	//
	// ★ 分开记是有用的：「点的」是会计写在选项里、标明了后果的那一条；
	// 「打的」是用户临时想到的，会计可能需要再确认一次。
	// 合成一句文本回灌，这个区别就丢了。
	Selected []string
}

// accountantTools 是会计 agent 能用的**全部**工具。
//
// # 两类工具，边界很清楚
//
//	只读（直连）      搜索、报表、体检、工资试算、明细账
//	终止型（要确认）  问用户、提议新建档案、提议人事异动
//
// ★ 抽成独立函数，是为了让这条边界能写成测试。
//
// 「不许有写账套的工具」这句话写在注释里没有用 —— 半年后有人
// 顺手加一个 close_period 就没人拦得住了。见 accountant_tools_test.go。
func accountantTools(repo *sqlite.AIRepo, tax aiprovider.TaxReturnProvider,
	doc aiprovider.AuditDocProvider, ev aiprovider.EvidenceProvider) *aiprovider.ToolSet {
	return aiprovider.NewToolSet(
		aiprovider.AskUserTool(),
		aiprovider.SearchAccountsTool(repo),
		aiprovider.SearchContactsTool(repo),
		// ★ 部门与员工这两个是补上来的。原来只有科目 / 往来单位 / 历史，
		// 于是遇到「560204 要求部门辅助核算」时，模型既查不到有哪些部门，
		// 也没法带着真实选项问用户，只能空着交差 ——
		// 用户看到的就是「缺少必需的辅助核算」。
		aiprovider.SearchDepartmentsTool(repo),
		aiprovider.SearchEmployeesTool(repo),
		aiprovider.FindSimilarVouchersTool(repo),
		// 账务查询：报表、本月体检、五险一金试算、明细账。
		// 全是只读 —— 读错了最多是它说错一句话，用户看得见、下一句就能纠正
		aiprovider.GetReportTool(repo),
		aiprovider.CheckPeriodTool(repo),
		aiprovider.PreviewPayrollTool(repo),
		aiprovider.GetLedgerTool(repo),
		// 审计底稿：读现状（只读）。
		// 模型要回答「这几笔错报加起来算不算重大」，就得先看到
		// 重要性水平与未更正错报合计 —— 否则它只能猜一个门槛。
		aiprovider.GetWorkpaperTool(repo),
		// 证据链：底稿上的结论各有哪些依据（只读）。
		// 它只说「底稿上记着哪些」，原件在不在由底稿页核对。
		// ★ 证据链走服务层（不是 repo）：界面与 AI 必须看到同一份
		// 「原件还在不在」的结论，否则模型会对已丢失的依据说「有依据」。
		aiprovider.GetEvidenceTool(ev),
		// 税务计算表：**与界面同一条计算路径**（传的是 service 自己）。
		// 让模型自己按记忆里的税率算，它会用上过期的优惠政策。
		aiprovider.GetTaxReturnTool(tax),
		// 审计与鉴证文书草稿（只读）。
		// 模型只能读，而且渲染文本里反复说清「草稿、待签字盖章」。
		aiprovider.GetAuditDocTool(doc),
		// 写账套的动作一律「提议 + 用户确认」，模型自己没有写库能力
		aiprovider.ProposeNewAuxTool(),
		aiprovider.ProposeHRActionTool(),
		aiprovider.ProposeAdjustmentTool(),
		aiprovider.ProposeEvidenceTool(),
	)
}

// AccountantSend 把用户的一句话交给会计，返回会计的应答。
//
// 会计要么**问一句**（信息不够），要么**给出凭证草稿**。
// 两条路都走同一套护栏 —— 问了几轮不影响校验的松紧。
func (s *Service) AccountantSend(ctx context.Context, in AccountantSendInput) (*AccountantSession, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, errors.New("请先描述这笔业务")
	}
	prov, err := s.db.AI().DefaultProvider(ctx)
	if err != nil {
		return nil, err
	}
	if prov == nil {
		return nil, errors.New("没有可用的模型服务。请先到「设置 → AI 记账助手」配置并启用一个")
	}

	id := strings.TrimSpace(in.SessionID)
	accountantSessions.mu.Lock()
	st := accountantSessions.sessions[id]
	if st == nil {
		id = newSessionID()
		st = &accountantSessionState{view: AccountantSession{ID: id, Turns: []AccountantTurn{}}}
		accountantSessions.sessions[id] = st
		accountantSessions.order = append(accountantSessions.order, id)
		accountantSessions.pruneLocked()
	}
	st.view.Busy = true
	accountantSessions.mu.Unlock()

	defer func() {
		accountantSessions.mu.Lock()
		st.view.Busy = false
		accountantSessions.mu.Unlock()
	}()

	// 1. 第一次说话：把账套上下文装进来（科目表、往来单位、历史同类）
	if len(st.history) == 0 {
		if err := s.accountantPrime(ctx, st, text); err != nil {
			return nil, err
		}
	}

	// 2. 用户这一句进历史。若上一轮是追问，就渲染成**结构化回答**：
	//    「在回答哪个问题」「点的是哪一项」「自己补了什么」三件事分开说。
	//    只回「银行」两个字，模型下一轮可能当成一句新的业务描述。
	userText := text
	if st.pending != nil {
		userText = aiprovider.AnswerMessage(st.pending, aiprovider.AccountantAnswer{
			QuestionID: st.pending.ID,
			Selected:   nonEmptyStrings(in.Selected),
			Custom:     text,
		})
		st.pending = nil
	} else {
		userText = "业务描述：" + text
	}
	st.history = append(st.history, aiprovider.Message{Role: "user", Content: userText})

	// 3. 跑一轮会计
	acct := &aiprovider.Accountant{
		Provider: prov,
		Tools:    accountantTools(s.db.AI(), s, s, s),
		Options: aiprovider.AgentOptions{
			MaxRounds: 4, MaxTokens: 2048, Temperature: 0,
		},
		Asked: st.view.Asks,
	}
	reply, history, rerr := acct.Reply(ctx, st.history)
	st.history = history
	if rerr != nil {
		// 出错也要把「用户说了什么」留在会话里，否则用户会以为消息丢了
		// 又重发一遍，而模型那边其实已经收到了。
		st.view.Turns = append(st.view.Turns, AccountantTurn{
			Role: "user", Text: text, At: time.Now().Format(time.RFC3339),
		})
		return &st.view, rerr
	}

	st.view.Turns = append(st.view.Turns, AccountantTurn{
		Role: "user", Text: text, At: time.Now().Format(time.RFC3339),
	})

	if reply.Question != nil {
		st.pending = reply.Question
		st.view.Asks++
		st.view.Turns = append(st.view.Turns, AccountantTurn{
			Role: "accountant",
			Text: reply.Question.Question,
			Question: &AccountantQuestion{
				ID:          reply.Question.ID,
				Header:      reply.Question.Header,
				Question:    reply.Question.Question,
				Options:     toServiceOptions(reply.Question.Options),
				MultiSelect: reply.Question.MultiSelect,
			},
			Model: reply.Model, TokensIn: reply.TokensIn, TokensOut: reply.TokensOut,
			At: time.Now().Format(time.RFC3339),
		})
		accountantSessions.touch(st)
		return &st.view, nil
	}

	// 3b. 提议新建档案：同样停下来等用户确认
	if reply.Aux != nil {
		st.pending = nil
		st.view.Turns = append(st.view.Turns, AccountantTurn{
			Role: "accountant",
			Text: fmt.Sprintf("账套里还没有「%s」，需要先建一个：%s",
				reply.Aux.Name, reply.Aux.Reason),
			Aux: &AuxProposalView{
				Kind:      reply.Aux.Kind,
				KindLabel: reply.Aux.Label(),
				Name:      reply.Aux.Name,
				Code:      reply.Aux.Code,
				DeptID:    reply.Aux.DeptID,
				Reason:    reply.Aux.Reason,
			},
			Model: reply.Model, TokensIn: reply.TokensIn, TokensOut: reply.TokensOut,
			At: time.Now().Format(time.RFC3339),
		})
		accountantSessions.touch(st)
		return &st.view, nil
	}

	// 3c. 人事异动提议：同样停下来等用户确认
	if reply.HR != nil {
		st.pending = nil
		v := s.hrProposalView(ctx, reply.HR)
		st.view.Turns = append(st.view.Turns, AccountantTurn{
			Role:  "accountant",
			Text:  v.Detail,
			HR:    v,
			Model: reply.Model, TokensIn: reply.TokensIn, TokensOut: reply.TokensOut,
			At: time.Now().Format(time.RFC3339),
		})
		accountantSessions.touch(st)
		return &st.view, nil
	}

	// 3c'. 审计调整提议：同样停下来等用户确认。
	//
	// ★ 底稿不是账簿，但它决定账要怎么改 —— 更不能让模型自己动手。
	if reply.Adjustment != nil {
		st.pending = nil
		v := s.adjustmentProposalView(ctx, reply.Adjustment)
		st.view.Turns = append(st.view.Turns, AccountantTurn{
			Role:       "accountant",
			Text:       v.Detail,
			Adjustment: v,
			Model:      reply.Model, TokensIn: reply.TokensIn, TokensOut: reply.TokensOut,
			At: time.Now().Format(time.RFC3339),
		})
		accountantSessions.touch(st)
		return &st.view, nil
	}

	// 3c''. 证据提议：同样停下来等用户确认
	if reply.Evidence != nil {
		st.pending = nil
		v := s.evidenceProposalView(ctx, reply.Evidence)
		st.view.Turns = append(st.view.Turns, AccountantTurn{
			Role:     "accountant",
			Text:     v.Detail,
			Evidence: v,
			Model:    reply.Model, TokensIn: reply.TokensIn, TokensOut: reply.TokensOut,
			At: time.Now().Format(time.RFC3339),
		})
		accountantSessions.touch(st)
		return &st.view, nil
	}

	// 3d. 专业答复：不编凭证，就是一段带结构的回答
	if reply.Answer != nil {
		st.pending = nil
		v := answerView(reply.Answer)
		st.view.Turns = append(st.view.Turns, AccountantTurn{
			Role:   "accountant",
			Text:   v.Text,
			Answer: v,
			Model:  reply.Model, TokensIn: reply.TokensIn, TokensOut: reply.TokensOut,
			At: time.Now().Format(time.RFC3339),
		})
		accountantSessions.touch(st)
		return &st.view, nil
	}

	// 4. 给了凭证：走**与单次建议完全相同**的护栏与审计
	turn := AccountantTurn{
		Role: "accountant", Text: reply.Say,
		Model: reply.Model, TokensIn: reply.TokensIn, TokensOut: reply.TokensOut,
		At: time.Now().Format(time.RFC3339),
	}
	if reply.Proposal != nil {
		res := s.accountantValidate(ctx, prov, reply, st.input)
		if res != nil {
			turn.SuggestionID = res.SuggestionID
			if res.Proposal != nil {
				turn.Voucher = s.voucherDraftOf(ctx, res.Proposal)
			}
			if res.Report != nil {
				turn.Passed = res.Report.Passed()
				turn.Failures = checkTitles(res.Report.Failures())
				turn.Warnings = checkTitles(res.Report.Warnings())
			}
			if res.Err != nil {
				turn.Text = strings.TrimSpace(turn.Text + "\n" + res.Err.Error())
			}
		}
	}
	st.view.Turns = append(st.view.Turns, turn)
	accountantSessions.touch(st)
	return &st.view, nil
}

// AccountantSession 读回一段对话。
func (s *Service) AccountantSession(id string) (*AccountantSession, error) {
	accountantSessions.mu.Lock()
	defer accountantSessions.mu.Unlock()
	st := accountantSessions.sessions[id]
	if st == nil {
		return nil, fmt.Errorf("对话不存在或已过期（重启会清空）")
	}
	out := st.view
	out.Turns = append([]AccountantTurn(nil), st.view.Turns...)
	return &out, nil
}

// AccountantReset 清空一段对话（换一笔业务重新说）。
func (s *Service) AccountantReset(id string) (*AccountantSession, error) {
	accountantSessions.mu.Lock()
	defer accountantSessions.mu.Unlock()
	st := accountantSessions.sessions[id]
	if st == nil {
		return &AccountantSession{ID: "", Turns: []AccountantTurn{}}, nil
	}
	st.view.Turns = []AccountantTurn{}
	st.view.Asks = 0
	st.history = nil
	st.pending = nil
	st.input = aiprovider.Input{}
	return &AccountantSession{ID: st.view.ID, Turns: []AccountantTurn{}}, nil
}

// accountantPrime 装上下文并写下第一轮历史。
func (s *Service) accountantPrime(ctx context.Context, st *accountantSessionState, text string) error {
	prov, err := s.db.AI().DefaultProvider(ctx)
	if err != nil {
		return err
	}
	repo := s.db.AI()
	// 账套上下文（期间日历、科目树）由 AI 仓储在检索与护栏里各自加载，
	// 这里只需要给提示词用的那几段。
	book, err := repo.BookContext(ctx)
	if err != nil {
		return fmt.Errorf("加载账套信息失败: %w", err)
	}
	accts, err := repo.Accounts(ctx)
	if err != nil {
		return fmt.Errorf("加载科目表失败: %w", err)
	}
	contacts, err := repo.Contacts(ctx)
	if err != nil {
		return fmt.Errorf("加载往来单位失败: %w", err)
	}
	prompt, err := repo.PromptConfig(ctx)
	if err != nil {
		return err
	}

	in := aiprovider.Input{
		Task: aiprovider.TaskFreeform, Text: text,
		Book: book, Accounts: accts, Contacts: contacts,
		Today: time.Now().Format("2006-01-02"), Prompt: prompt,
	}
	// 历史同类凭证：检索失败不阻断 —— 它只是加分项
	if ex, rerr := repo.Similar(ctx, text, "", 0, 4); rerr == nil {
		in.Examples = ex
	}
	system := aiprovider.AccountantSystemPrompt(in)
	st.digest = ai.Digest(system)
	st.input = in
	privacy := aiprovider.DefaultPrivacy(prov.Kind())
	st.history = []aiprovider.Message{
		{Role: "system", Content: privacy.Apply(system)},
	}
	return nil
}

// accountantValidate 走与单次建议相同的护栏。
func (s *Service) accountantValidate(ctx context.Context, prov aiprovider.Provider,
	reply *aiprovider.AccountantReply, in aiprovider.Input) *aiprovider.Result {
	lctx, err := s.db.AI().LedgerContext(ctx)
	if err != nil {
		return nil
	}
	sg := aiSuggester(s, prov)
	// 目标类型写 accountant：审计表里要能分开「单次建议」与「对话里给出的」，
	// 两者的采纳率不是一回事。
	return sg.ValidateProposal(ctx, &aiprovider.Response{
		Content: reply.Raw, Model: reply.Model,
		TokensIn: reply.TokensIn, TokensOut: reply.TokensOut,
	}, lctx, s.accountantDigest(in), in, "accountant", nil)
}

// accountantDigest 复算提示词指纹（与首轮登录时算的是同一段文本）。
func (s *Service) accountantDigest(in aiprovider.Input) string {
	return ai.Digest(aiprovider.AccountantSystemPrompt(in))
}

// checkTitles 把护栏结论转成界面直接显示的一行行文字。
//
// 带 detail 的写成「标题：细节」——「科目不存在」这种标题看不出是哪个科目，
// 而用户要改的恰恰是那个科目。
func checkTitles(cs []ai.Check) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if c.Detail != "" {
			out = append(out, c.Title+"："+c.Detail)
			continue
		}
		out = append(out, c.Title)
	}
	return out
}

// nonEmptyStrings 去掉空白项；全空时返回 nil（而不是一个空切片）。
func nonEmptyStrings(in []string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// answerView 把专业答复转成界面形状，并执行两条**程序判定**。
//
// # 1. submittable 一律压回 false
//
// 模型可以声称某份东西「可以直接用于正式申报或对外提交」，而这句话
// 必须被程序否掉：注册会计师业务依法由会计师事务所统一受理，
// 依法出具的报告才有效力。AI 草拟的文本没有这个效力。
//
// 它声称了什么留在 submittableClaimed 里 —— 不是为了相信它，
// 是为了出问题时能查出「当时模型是怎么说的」。
//
// # 2. 强制转人工
//
// 这是**兜底**：主要判定在提示词里（模型自己把事项写进 humanReview）。
// 兜底之所以要做，是因为漏报的代价不对称：
//
//	多报一次 → 用户多找一个会计看一眼
//	漏报一次 → 他拿着没核过的东西去申报
//
// 所以宁可误报。命中项全部列给用户看，由他自己判断要不要找人。
func answerView(a *aiprovider.Answer) *CPAAnswerView {
	// ★ 先归一，再取字段。
	//
	// 不能假定调用方已经归一过：风险等级那条不变式是安全相关的
	// （未标注必须按中等处理），靠调用顺序维持迟早会漏。
	a.Normalize()
	v := &CPAAnswerView{
		Conclusion: a.Conclusion, Basis: a.Basis, Obtained: a.Obtained,
		Missing: a.Missing, Process: a.Process, Findings: a.Findings,
		Risk: a.Risk, Recommendations: a.Recommendations,
		HumanReview: a.HumanReview, PolicyNote: a.PolicyNote, Text: a.Text,
		SubmittableClaimed: a.Submittable,
		Submittable:        false, // ★ 硬边界：说了不算
		RiskUnstated:       a.RiskUnstated,
	}
	v.ProblemList = a.Validate()
	if v.SubmittableClaimed {
		v.ProblemList = append(v.ProblemList,
			"模型声称这份材料可以直接用于正式申报或对外提交 —— 已按「不可以」处理。"+
				"AI 草拟的文本不具有证明效力，必须经人工注册会计师复核后才能对外")
	}
	v.Escalation = a.EscalationReasons()
	return v
}

// evidenceProposalView 把证据提议补成人看得懂的一张卡。
//
// ★ 与登记调整同一套做法：模型只给 id，名字由这里查出来填上 ——
// 让模型写名字，它会写一个「听起来对」的名字，而用户在一张
// 写着别的凭证号的卡片上按确认。
func (s *Service) evidenceProposalView(ctx context.Context,
	p *aiprovider.EvidenceProposal) *EvidenceProposalView {

	owner := evidence.OwnerType(p.OwnerType)
	v := &EvidenceProposalView{
		OwnerType: p.OwnerType, OwnerTypeLabel: owner.Label(), OwnerID: p.OwnerID,
		RefKind: p.RefKind, RefID: p.RefID, RefLabel: p.RefLabel,
		Note: p.Note, Reason: p.Reason,
	}
	if k, ok := evidence.RefKind(p.RefKind), true; ok {
		v.RefKindLabel = k.Label()
	}

	// 结论的一句话
	switch owner {
	case evidence.OwnerAdjustment:
		if a, err := s.db.Workpapers().Adjustment(ctx, p.OwnerID); err == nil {
			v.OwnerTitle = fmt.Sprintf("%s %s %s", a.Code, a.Summary, a.Amount())
		} else {
			v.Problems = append(v.Problems, "找不到这条审计调整："+err.Error())
		}
	case evidence.OwnerMateriality, evidence.OwnerConclusion:
		y, m := int(p.OwnerID/100), int(p.OwnerID%100)
		k := period.NewKey(y, m)
		if !k.Valid() {
			v.Problems = append(v.Problems,
				fmt.Sprintf("结论 id %d 里读不出会计期间（应当形如 202503）", p.OwnerID))
		} else {
			v.OwnerTitle = owner.Label() + "（" + k.String() + "）"
		}
	}
	// 资料的名字（单据类由程序查）
	if v.RefLabel == "" && p.RefID > 0 {
		label, err := s.describeRef(ctx, evidence.RefKind(p.RefKind), p.RefID)
		if err != nil {
			v.Problems = append(v.Problems, err.Error())
		} else {
			v.RefLabel = label
		}
	}

	v.Detail = fmt.Sprintf("建议为%s「%s」挂一份依据：%s。%s",
		v.OwnerTypeLabel, v.OwnerTitle, v.RefLabel, v.Reason)
	if len(v.Problems) > 0 {
		v.Detail += "\n注意：这条提议现在还不能落库 —— " + strings.Join(v.Problems, "；")
	}
	return v
}

// adjustmentProposalView 把审计调整提议补成人看得懂的一张卡。
//
// ★ 模型只给科目编码与辅助核算 id，名字由这里查出来填上 ——
// 与人事异动同一套理由：让模型写名字，它会写一个「听起来对」的名字，
// 而用户在一张写着别的名字的卡片上按确认。
//
// 另外这里还要做一件模型做不到的事：**复核**。
// 提议本身成形（借贷平、有依据）不代表能登记 ——
// 那些要拿账套真实的科目树来判。所以这里直接用
// SaveAdjustment 那一套校验跑一遍，把问题写在卡片上：
// 用户按确认之前就该看到「这个科目不存在」，而不是按下去才报错。
func (s *Service) adjustmentProposalView(ctx context.Context,
	p *aiprovider.AdjustmentProposal) *AdjustmentProposalView {

	v := &AdjustmentProposalView{
		Period: p.Period, Kind: p.Kind, KindLabel: p.KindLabel(),
		Summary: p.Summary, Reason: p.Reason, Evidence: p.Evidence,
		Amount: p.TotalDebit(), Lines: []AdjustProposalLineView{},
	}
	if k, err := timeParsePeriod(p.Period); err == nil {
		v.Year, v.Month = k.Year, k.Month
	}
	names, _ := s.accountNames(ctx)
	contactNames, _ := s.contactNames(ctx)
	deptNames, _ := s.departmentNames(ctx)
	for i, l := range p.Lines {
		v.Lines = append(v.Lines, AdjustProposalLineView{
			LineNo: i + 1, AccountCode: l.AccountCode, AccountName: names[l.AccountCode],
			Summary: l.Summary, Debit: l.Debit, Credit: l.Credit,
			ContactID: l.ContactID, EmployeeID: l.EmployeeID,
			DeptID: l.DeptID, ProjectID: l.ProjectID,
			AuxDesc: describeAux(ledger.Aux{
				ContactID: l.ContactID, EmployeeID: l.EmployeeID,
				DeptID: l.DeptID, ProjectID: l.ProjectID,
			}, contactNames, deptNames),
		})
	}

	// 拿账套的真实科目树复核一遍
	adj := workpaper.Adjustment{
		Period: period.NewKey(v.Year, v.Month), Code: "（提议）",
		Kind: workpaper.AdjustKind(p.Kind), Summary: p.Summary,
		Reason: p.Reason, Evidence: p.Evidence,
	}
	for _, l := range p.Lines {
		adj.Lines = append(adj.Lines, workpaper.AdjustLine{
			AccountCode: l.AccountCode, Summary: l.Summary,
			Debit: l.Debit, Credit: l.Credit,
			ContactID: l.ContactID, EmployeeID: l.EmployeeID,
			DeptID: l.DeptID, ProjectID: l.ProjectID,
		})
	}
	if err := s.validateAdjustment(ctx, adj); err != nil {
		v.Problems = append(v.Problems, err.Error())
	}

	v.Detail = fmt.Sprintf("建议登记%s %s：%s，金额 %s。依据：%s",
		v.KindLabel, v.Period, p.Summary, v.Amount, p.Reason)
	if len(v.Problems) > 0 {
		v.Detail += "\n注意：这笔调整现在还不能登记 —— " + strings.Join(v.Problems, "；")
	}
	return v
}

// timeParsePeriod 解析 "2026-09"（服务层用的是 period.Key）。
func timeParsePeriod(s string) (period.Key, error) {
	var y, m int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d-%d", &y, &m); err != nil {
		return period.Key{}, fmt.Errorf("会计期间 %q 格式不对，应为 2026-09", s)
	}
	k := period.NewKey(y, m)
	if !k.Valid() {
		return k, fmt.Errorf("会计期间 %q 非法", s)
	}
	return k, nil
}

// hrProposalView 把提议补成人看得懂的一张卡。
//
// ★ 模型只给 id，名字由这里查出来填上。
// 让模型填名字的话，它会写一个「听起来对」的名字 —— 而档案里可能
// 根本没有这个人，用户在一张写着别人名字的卡片上点「确认」，
// 改的却是另一个人。
func (s *Service) hrProposalView(ctx context.Context,
	p *aiprovider.HRProposal) *HRProposalView {

	v := &HRProposalView{
		Kind: p.Kind, KindLabel: p.Label(), EmployeeID: p.EmployeeID,
		DeptID: p.DeptID, LeaveDate: p.LeaveDate,
		BaseSalary: p.BaseSalary, SIBase: p.SIBase, HFBBase: p.HFBBase,
		Reason: p.Reason,
	}
	// 员工名
	if list, err := s.db.AI().Employees(ctx); err == nil {
		for _, e := range list {
			if e.ID == p.EmployeeID {
				v.EmployeeName = e.Name
				break
			}
		}
	}
	// 部门名
	if p.DeptID != nil {
		if names, err := s.departmentNames(ctx); err == nil {
			v.DeptName = names[*p.DeptID]
		}
	}
	who := v.EmployeeName
	if who == "" {
		who = fmt.Sprintf("员工 #%d", p.EmployeeID)
	}
	switch p.Kind {
	case aiprovider.HRResign:
		v.Detail = fmt.Sprintf("建议为「%s」办理离职，离职日期 %s。%s",
			who, p.LeaveDate, p.Reason)
	case aiprovider.HRTransfer:
		v.Detail = fmt.Sprintf("建议把「%s」调到「%s」。%s", who, v.DeptName, p.Reason)
	case aiprovider.HRSalary:
		v.Detail = fmt.Sprintf("建议把「%s」的月工资调整为 %s 元。%s",
			who, p.BaseSalary, p.Reason)
	}
	return v
}

func toServiceOptions(opts []aiprovider.AccountantOption) []AccountantOption {
	out := make([]AccountantOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, AccountantOption{
			Label: o.Label, Description: o.Description, Recommended: o.Recommended,
		})
	}
	return out
}

// voucherDraftOf 把提议落成界面要的凭证草稿形状。
//
// 与单次建议（AISuggest）用的是同一个函数：同一个提议，
// 从这里看和从那里看必须是同一张凭证。
//
// ★ 辅助核算渲染成**名称**（「管理部门」）而不是「部门#1」——
// 会计写给你的草稿上写着「部门#1」，你没法核对它对不对。
func (s *Service) voucherDraftOf(ctx context.Context,
	prop *ai.Proposal) *VoucherDraft {
	v, err := prop.Materialize("AI 提议", nil)
	if err != nil {
		return nil
	}
	names, _ := s.contactNames(ctx)
	deptNames, _ := s.departmentNames(ctx)
	out := &VoucherDraft{
		Word: string(v.Word), BizDate: v.BizDate.String(),
		Remark: v.Remark, Total: v.TotalDebit(),
	}
	for _, e := range v.Entries {
		out.Entries = append(out.Entries, ClosingEntry{
			AccountCode: e.AccountCode, Summary: e.Summary,
			Debit: e.Debit, Credit: e.Credit,
			AuxDesc: describeAux(e.Aux, names, deptNames),
		})
	}
	return out
}

func (st *accountantStore) touch(state *accountantSessionState) {
	accountantSessions.mu.Lock()
	defer accountantSessions.mu.Unlock()
	state.view.Updated = time.Now().Format(time.RFC3339)
}

// pruneLocked 丢掉最老的会话。调用方必须已持锁。
func (st *accountantStore) pruneLocked() {
	for len(st.order) > maxAccountantSessions {
		old := st.order[0]
		st.order = st.order[1:]
		delete(st.sessions, old)
	}
}
