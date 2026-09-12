package aiprovider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"miniaccount/internal/domain/ai"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/voucher"
)

// ---------------------------------------------------------------------------
// 编排层依赖
// ---------------------------------------------------------------------------

// ContextSource 提供构造提示词所需的账套上下文。
//
// 用接口而不是直接依赖存储层：编排层只关心「拿到什么」，
// 不关心「怎么取的」；测试时换成固定数据即可，不需要真的建库。
type ContextSource interface {
	// LedgerContext 返回校验用的科目树、期间表与往来档案。
	LedgerContext(ctx context.Context) (*ledger.Context, error)
	// BookContext 返回账套背景。
	BookContext(ctx context.Context) (BookContext, error)
	// Accounts 返回全部可记账科目（闭集，供模型选择）。
	Accounts(ctx context.Context) ([]AccountBrief, error)
	// Contacts 返回全部往来单位。
	Contacts(ctx context.Context) ([]ContactBrief, error)
}

// Retriever 检索历史凭证。
//
// ★ 这是整个 AI 环节里**性价比最高**的一层。
// 小微企业「上次同样的对手方怎么记的」比任何推理都准，
// 而且即使用户把 AI 关掉，光靠历史检索也能覆盖大部分流水。
type Retriever interface {
	// Similar 返回与 text / counterparty 最相似的历史凭证。
	//
	// 实现应优先匹配：① 对方户名完全相同 ② 摘要关键词重合
	// ③ 金额量级相近 ④ 时间更近的优先。
	Similar(ctx context.Context, text string, counterparty string,
		amountMoney int64, limit int) ([]Example, error)
}

// Auditor 记录 AI 提议与人工决定。
//
// ★ 必须留痕。AI 提议的凭证一旦入账，事后必须能回答：
// 哪个模型、什么输入、模型说了什么、人改了什么、谁点的确认。
// 没有这张表，「AI 记错了账」就无从追溯，也无法改进。
type Auditor interface {
	// Record 写入一条提议，返回其 id。
	Record(ctx context.Context, rec SuggestionRecord) (int64, error)
	// Decide 记录人工对这条提议的处置。
	Decide(ctx context.Context, id int64, status Decision, finalVoucherID *int64,
		rejectReason string) error
}

// SuggestionRecord 是一条待落库的提议记录。
type SuggestionRecord struct {
	TargetType string
	TargetID   *int64
	Provider   string
	Model      string
	Layer      ai.Layer
	// PromptDigest 是提示词的 sha256，**不含原文**。
	PromptDigest string
	// RawResponse 是模型原始返回，便于排错。可能为空（隐私设置）。
	RawResponse string
	// Proposed 是解析后的结构化提议。
	Proposed *ai.Proposal
	// Checksum 是提议内容的指纹。
	Checksum   string
	Confidence float64
	// Status 是护栏校验的结论：valid 表示通过。
	Status string
	// RejectReason 是护栏给出的失败原因。
	RejectReason string
	TokensIn     int
	TokensOut    int
}

// Decision 是人工对一条提议的处置。
type Decision string

// 处置结果。
const (
	// DecisionAccepted 原样采纳。
	DecisionAccepted Decision = "accepted"
	// DecisionModified 改过之后采纳 —— ★ 最有价值的学习信号。
	DecisionModified Decision = "modified"
	// DecisionRejected 拒绝。
	DecisionRejected Decision = "rejected"
)

// SuggestionStatus 是护栏校验后的状态。
const (
	StatusValid   = "valid"
	StatusInvalid = "invalid"
)

// ---------------------------------------------------------------------------
// 编排
// ---------------------------------------------------------------------------

// Suggester 把「检索 → 生成 → 校验 → 留痕」串起来。
type Suggester struct {
	Provider  Provider
	Context   ContextSource
	Retriever Retriever
	Auditor   Auditor
	Privacy   Privacy
	// Examples 是检索多少条历史范例。
	// 3~5 条足够：再多会挤占上下文，而且更远的历史相关性下降。
	Examples int
	// Temperature 建议保持 0。
	Temperature float64
	// MaxTokens 为 0 时按 2048 处理（一张凭证的 JSON 不会更长）。
	MaxTokens int
	// Tools 是给模型用的只读工具集。
	//
	// 为空时退化为「单轮调用」：把账套上下文全部塞进提示词。
	// 给了工具则走 Agent 循环 —— 模型可以自己决定还要看什么。
	//
	// 两种模式共用同一套护栏与解析，因此**多了一轮工具调用
	// 不会让校验标准有任何放松**。
	Tools *ToolSet
	// MaxToolRounds 是 Agent 循环的最大轮数。
	MaxToolRounds int
	// Now 允许注入时间，便于测试。
	Now func() time.Time
}

// Result 是一次建议的完整结果。
type Result struct {
	// Layer 标识这条建议来自哪一层。
	Layer ai.Layer
	// Proposal 是通过护栏的提议；未通过时为 nil。
	Proposal *ai.Proposal
	// Report 是护栏校验报告。
	Report *ai.Report
	// SuggestionID 是审计表里的记录 id，便于界面回溯。
	SuggestionID int64
	// Response 是模型调用的原始响应元信息（token 数、耗时）。
	Response *Response
	// Err 是过程性错误（网络失败、解析失败）。
	//
	// 与「护栏不通过」区分开：网络失败应当重试或提示用户，
	// 护栏不通过则应当把报告展示给用户看 —— 两者的界面动作完全不同。
	Err error
}

// OK 报告这次建议是否可以直接供用户确认。
func (r *Result) OK() bool {
	return r.Err == nil && r.Proposal != nil && r.Report != nil && r.Report.Passed()
}

// Summary 返回一句话结论。
func (r *Result) Summary() string {
	switch {
	case r.Err != nil:
		return "生成失败：" + r.Err.Error()
	case r.Report == nil:
		return "生成失败：没有拿到校验报告"
	case !r.Report.Passed():
		return r.Report.Summary()
	default:
		return fmt.Sprintf("%s 建议（置信度 %.2f）：%s",
			r.Layer.Label(), r.Proposal.Confidence, r.Proposal.Voucher.Remark)
	}
}

// Suggest 为一项业务生成记账建议。
//
// 流程：
//
//  1. 加载账套上下文（科目闭集、往来单位、期间）
//  2. 检索历史同类凭证 —— 提示词里最有用的部分
//  3. 构造系统/用户提示词，按隐私策略脱敏
//  4. 调用模型
//  5. 严格解析 + 护栏校验
//  6. 无论成败都写审计表
//
// ★ 无论第几步失败，审计记录都要落库。失败本身也是数据：
// 「这个模型在这个账套上 30% 的提议通不过护栏」是换模型或调提示词的依据。
func (s *Suggester) Suggest(ctx context.Context, in Input, targetType string,
	targetID *int64) *Result {

	if s.Provider == nil {
		return &Result{Err: ErrNoProvider}
	}
	if s.Now == nil {
		s.Now = time.Now
	}

	// 1. 上下文
	if s.Context == nil {
		return &Result{Err: errors.New("ai: 缺少账套上下文来源")}
	}
	lctx, err := s.Context.LedgerContext(ctx)
	if err != nil {
		return &Result{Err: fmt.Errorf("ai: 加载账套上下文失败: %w", err)}
	}
	book, err := s.Context.BookContext(ctx)
	if err != nil {
		return &Result{Err: fmt.Errorf("ai: 加载账套信息失败: %w", err)}
	}
	accts, err := s.Context.Accounts(ctx)
	if err != nil {
		return &Result{Err: fmt.Errorf("ai: 加载科目表失败: %w", err)}
	}
	contacts, err := s.Context.Contacts(ctx)
	if err != nil {
		return &Result{Err: fmt.Errorf("ai: 加载往来单位失败: %w", err)}
	}
	in.Book = book
	in.Accounts = accts
	in.Contacts = contacts
	in.Today = s.Now().Format("2006-01-02")

	// 2. 检索历史
	if s.Retriever != nil {
		n := s.Examples
		if n <= 0 {
			n = 4
		}
		// 检索失败不阻断：历史只是加分项，没有它模型照样能提议
		ex, rerr := s.Retriever.Similar(ctx, in.Text, in.Counterparty,
			int64(in.Amount), n)
		if rerr == nil {
			in.Examples = ex
		}
	}

	// 3. 脱敏与提示词
	system := SystemPrompt(in)
	user := UserPrompt(in)
	digest := ai.Digest(system + "\n" + user)
	system = s.Privacy.Apply(system)
	user = s.Privacy.Apply(user)

	// 4. 调用模型（有工具时走 Agent 循环）
	var (
		resp     *Response
		agentRes *AgentResult
		cerr     error
	)
	if s.Tools != nil && s.Tools.Len() > 0 {
		agent := &Agent{
			Provider: s.Provider, Tools: s.Tools,
			Options: AgentOptions{
				MaxRounds: s.MaxToolRounds, MaxTokens: s.maxTokens(),
				Temperature: s.Temperature,
			},
		}
		agentRes, cerr = agent.Run(ctx, system, user)
		if cerr == nil {
			// 把 Agent 的结果包成 Response，让后面的解析与留痕走同一条路
			resp = &Response{
				Content: agentRes.Content, Model: s.Provider.Model(),
				TokensIn: agentRes.TokensIn, TokensOut: agentRes.TokensOut,
			}
		}
	} else {
		resp, cerr = s.Provider.Complete(ctx, Request{
			System: system, User: user, JSONMode: true,
			Temperature: s.Temperature, MaxTokens: s.maxTokens(),
		})
	}
	if cerr != nil {
		// 调用失败也要留痕，否则「模型经常连不上」这种问题永远看不见
		s.record(ctx, SuggestionRecord{
			TargetType: targetType, TargetID: targetID,
			Provider: s.Provider.Name(), Model: s.Provider.Model(),
			Layer: ai.LayerAI, PromptDigest: digest,
			Status: StatusInvalid, RejectReason: cerr.Error(),
		})
		return &Result{Layer: ai.LayerAI, Response: resp, Err: cerr}
	}

	// 5. 解析 + 护栏
	prop, perr := ai.Parse(resp.Content)
	if perr != nil {
		id := s.record(ctx, SuggestionRecord{
			TargetType: targetType, TargetID: targetID,
			Provider: s.Provider.Name(), Model: resp.Model,
			Layer: ai.LayerAI, PromptDigest: digest,
			RawResponse: resp.Content,
			Status:      StatusInvalid, RejectReason: perr.Error(),
			TokensIn: resp.TokensIn, TokensOut: resp.TokensOut,
		})
		return &Result{Layer: ai.LayerAI, SuggestionID: id, Response: resp,
			Err: fmt.Errorf("%w: %v", ai.ErrMalformedJSON, perr)}
	}

	report, verr := prop.Validate(lctx)
	if verr != nil {
		s.record(ctx, SuggestionRecord{
			TargetType: targetType, TargetID: targetID,
			Provider: s.Provider.Name(), Model: resp.Model,
			Layer: ai.LayerAI, PromptDigest: digest,
			RawResponse: resp.Content, Proposed: prop, Checksum: prop.Checksum(),
			Confidence: prop.Confidence,
			Status:     StatusInvalid, RejectReason: verr.Error(),
			TokensIn: resp.TokensIn, TokensOut: resp.TokensOut,
		})
		return &Result{Layer: ai.LayerAI, Proposal: prop, Response: resp, Err: verr}
	}

	status, reason := StatusValid, ""
	if !report.Passed() {
		// ★ 审计里存**完整细节**而不是一句话总结：
		// 「模型把货款的科目幻觉成了 9999」这种结论只能从
		// 明细里读出来，标题级的总结查不出规律。
		status, reason = StatusInvalid, report.Detail()
	}
	id := s.record(ctx, SuggestionRecord{
		TargetType: targetType, TargetID: targetID,
		Provider: s.Provider.Name(), Model: resp.Model,
		Layer: ai.LayerAI, PromptDigest: digest,
		RawResponse: resp.Content, Proposed: prop, Checksum: report.Checksum,
		Confidence: prop.Confidence,
		Status:     status, RejectReason: reason,
		TokensIn: resp.TokensIn, TokensOut: resp.TokensOut,
	})

	out := &Result{
		Layer: ai.LayerAI, Proposal: prop, Report: report,
		SuggestionID: id, Response: resp,
	}
	if !report.Passed() {
		// 护栏不通过不是「错误」，而是一个需要用户看的结果：
		// 界面要把失败原因摊开，而不是弹一句「AI 失败了」。
		out.Err = nil
	}
	return out
}

func (s *Suggester) maxTokens() int {
	if s.MaxTokens > 0 {
		return s.MaxTokens
	}
	return 2048
}

// record 写审计记录；失败不影响主流程（审计失败不该让用户记不了账）。
func (s *Suggester) record(ctx context.Context, rec SuggestionRecord) int64 {
	if s.Auditor == nil {
		return 0
	}
	id, err := s.Auditor.Record(ctx, rec)
	if err != nil {
		return 0
	}
	return id
}

// Accept 记录「用户采纳了这条建议」并落成凭证草稿。
//
// createdBy 是**确认人**。记账责任始终落在自然人身上，
// 模型名只进审计表，不进凭证签章。
func (s *Suggester) Accept(ctx context.Context, r *Result, createdBy string,
	finalVoucherID *int64) (*voucher.Voucher, error) {

	if r == nil || r.Proposal == nil {
		return nil, errors.New("ai: 没有可采纳的提议")
	}
	if r.Report == nil || !r.Report.Passed() {
		return nil, fmt.Errorf("ai: 提议未通过护栏，不能采纳：%s", reportSummary(r.Report))
	}
	prov := &ai.AIProvenance{
		Provider: provName(s.Provider), Model: provModel(s.Provider),
		Layer: r.Layer, Confidence: r.Proposal.Confidence,
		SuggestionID: r.SuggestionID,
	}
	if r.Response != nil {
		prov.Model = r.Response.Model
	}
	v, err := Draft(r.Proposal, createdBy, prov)
	if err != nil {
		return nil, err
	}
	if s.Auditor != nil && r.SuggestionID != 0 {
		// ★ 决策必须落库，且失败要报出来。
		//
		// 原来这里是 `_ = s.Auditor.Decide(...)`：采纳记录写失败时
		// 用户照样拿到凭证草稿，而「这条建议被采纳了」这件事没记下来 ——
		// 审计轨迹与采纳率统计静默失真。
		// 对照 Reject 是返回 error 的，两者不该不对称。
		if err := s.Auditor.Decide(ctx, r.SuggestionID, DecisionAccepted,
			finalVoucherID, ""); err != nil {
			return nil, fmt.Errorf("ai: 记录采纳结果失败：%w", err)
		}
	}
	return v, nil
}

// Reject 记录「用户拒绝了这条建议」。
func (s *Suggester) Reject(ctx context.Context, r *Result, reason string) error {
	if r == nil || s.Auditor == nil || r.SuggestionID == 0 {
		return nil
	}
	return s.Auditor.Decide(ctx, r.SuggestionID, DecisionRejected, nil, reason)
}

func reportSummary(r *ai.Report) string {
	if r == nil {
		return "无报告"
	}
	return r.Summary()
}

func provName(p Provider) string {
	if p == nil {
		return ""
	}
	return p.Name()
}

func provModel(p Provider) string {
	if p == nil {
		return ""
	}
	return p.Model()
}

// MaskForLog 在写日志前对文本脱敏，避免敏感数据落进日志文件。
func MaskForLog(kind Kind, s string) string {
	return DefaultPrivacy(kind).Apply(strings.TrimSpace(s))
}
