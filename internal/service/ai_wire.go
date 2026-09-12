package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"miniaccount/internal/ai/aiprovider"
	"miniaccount/internal/domain/ai"
	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// aiProviderFactory 允许替换「怎么拿到模型服务」。
//
// 存在的理由：批量记账要能在**没有网络、没有密钥**的情况下测 ——
// 进度、取消、逐条记录、失败不中断，这些逻辑与模型无关，
// 却是最容易错的部分。测试注入一个假服务，就能把它们全跑一遍。
//
// 生产路径永远是「从账套配置里取默认服务」。这个钩子同时给将来的
// 「离线演练模式」留了口子。
var aiProviderFactory func(ctx context.Context, s *Service) (aiprovider.Provider, error)

// SetAIProviderFactory 替换模型服务的来源（传 nil 恢复默认）。
func SetAIProviderFactory(f func(ctx context.Context, s *Service) (aiprovider.Provider, error)) {
	aiProviderFactory = f
}

// providerFor 取本次调用要用的模型服务。
func (s *Service) providerFor(ctx context.Context) (aiprovider.Provider, error) {
	if aiProviderFactory != nil {
		return aiProviderFactory(ctx, s)
	}
	return s.db.AI().DefaultProvider(ctx)
}

// aiSuggester 构造一个把存储层当作三个依赖的编排器。
//
// 之所以在 service 里现构造而不是长期持有：编排器本身是无状态的，
// 而 Provider 可能被用户随时切换（改配置、换模型），
// 每次请求重建可以保证「这次用的就是界面上显示的那个模型」。
func aiSuggester(s *Service, prov aiprovider.Provider) *aiprovider.Suggester {
	repo := s.db.AI()
	return &aiprovider.Suggester{
		Provider:  prov,
		Context:   repo,
		Retriever: repo,
		Auditor:   repo,
		// 隐私策略由部署形态决定：本地不脱敏，云端遮账号。
		Privacy: aiprovider.DefaultPrivacy(prov.Kind()),
		// ★ 给模型三个**只读**工具。
		//
		// 没有写操作、没有「顺手把凭证存了」的能力 ——
		// 它能做的最坏事情是返回一段通不过护栏的 JSON，然后被记进审计表。
		Tools: aiprovider.NewToolSet(
			aiprovider.SearchAccountsTool(repo),
			aiprovider.SearchContactsTool(repo),
			aiprovider.FindSimilarVouchersTool(repo),
		),
		MaxToolRounds: 3,
	}
}

// aiTask 把界面传来的任务名转成枚举。
//
// 认不出来的一律当「银行流水」处理：它是最高频的场景，
// 而且任务名只影响提示词里那句「请为下面这笔 X 业务编制记账凭证」，
// 不改变任何校验规则 —— 猜错的代价很小。
func aiTask(s string) aiprovider.Task {
	switch aiprovider.Task(s) {
	case aiprovider.TaskInvoice:
		return aiprovider.TaskInvoice
	case aiprovider.TaskExpense:
		return aiprovider.TaskExpense
	case aiprovider.TaskFreeform:
		return aiprovider.TaskFreeform
	default:
		return aiprovider.TaskBankFlow
	}
}

// suggestInput 把 service 的入参转成编排器的入参。
func suggestInput(in AISuggestInput, prompt ai.PromptConfig) aiprovider.Input {
	return aiprovider.Input{
		Task:         aiTask(in.Task),
		Text:         in.Text,
		Amount:       in.Amount,
		Date:         in.Date,
		Counterparty: in.Counterparty,
		Direction:    in.Direction,
		Extra:        in.Extra,
		Prompt:       prompt,
	}
}

// 保证金额与 AI 契约里的单位一致。
//
// 这里是一个**编译期**断言式的说明：契约里的金额是「分」（int64），
// 而界面上的输入框给的是「元」。转换必须在 service 边界完成，
// 绝不能把「元」传进去 —— 差了 100 倍且看起来完全正常。
var _ = money.Money(0)

// LayerLabel 返回 AI 建议来源层的中文名，供界面直接展示。
func LayerLabel(l string) string { return ai.Layer(l).Label() }

// ---------------------------------------------------------------------------
// 提示词定制
// ---------------------------------------------------------------------------

// AIPromptView 是提示词设置界面需要的全部内容。
type AIPromptView struct {
	// Instructions 是当前生效的记账要求（用户自定义；未自定义时是出厂默认）。
	//
	// ★ 界面直接把它放进编辑框：用户看到的就是生效的那份，
	// 不用去猜「默认长什么样」。
	Instructions string `json:"instructions"`
	// Custom 为真表示这份内容来自用户（而不是出厂默认）。
	Custom bool `json:"custom"`
	// Default 是出厂默认文本，供「恢复默认」预览。
	Default string `json:"default"`
	// TaskNotes 是按任务类型的附加要求。
	TaskNotes map[string]string `json:"taskNotes"`
	// Tasks 是可选的任务名与中文名，供界面渲染标签页。
	Tasks []AIPromptTask `json:"tasks"`
	// UpdatedAt 是最后修改时间。
	UpdatedAt string `json:"updatedAt"`
	// MaxInstructions / MaxTaskNote 是长度上限，界面据此提示。
	MaxInstructions int `json:"maxInstructions"`
	MaxTaskNote     int `json:"maxTaskNote"`
	// Locked 说明哪些部分由程序固定、不可修改 —— 界面必须显示，
	// 否则用户会以为「我改了没生效」。
	Locked []string `json:"locked"`
}

// AIPromptTask 是一个任务类型。
type AIPromptTask struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Note  string `json:"note"`
}

// AIPromptInput 是保存提示词设置的入参。
type AIPromptInput struct {
	Instructions string            `json:"instructions"`
	TaskNotes    map[string]string `json:"taskNotes"`
}

// AIPromptConfig 返回提示词设置。
func (s *Service) AIPromptConfig(ctx context.Context) (*AIPromptView, error) {
	c, err := s.db.AI().PromptConfig(ctx)
	if err != nil {
		return nil, err
	}
	v := &AIPromptView{
		Instructions:    c.Instructions,
		Custom:          !c.IsDefault(),
		Default:         aiprovider.DefaultInstructions(),
		TaskNotes:       map[string]string{},
		UpdatedAt:       c.UpdatedAt,
		MaxInstructions: ai.MaxInstructions,
		MaxTaskNote:     ai.MaxTaskNote,
		Locked: []string{
			"工作边界（只能用列出的科目编码、借贷必须相等、金额用「分」、摘要逐行填写）",
			"输出格式（程序按固定的 JSON 结构解析，改了会直接读不出来）",
			"账套信息、科目清单、往来单位清单（由程序按账套实时生成）",
		},
	}
	if v.Instructions == "" {
		v.Instructions = v.Default
	}
	for _, t := range ai.KnownTasks() {
		v.Tasks = append(v.Tasks, AIPromptTask{
			Value: t, Label: aiprovider.Task(t).Label(), Note: c.TaskNote(t),
		})
		v.TaskNotes[t] = c.TaskNote(t)
	}
	return v, nil
}

// SaveAIPromptConfig 保存提示词设置。
func (s *Service) SaveAIPromptConfig(ctx context.Context, in AIPromptInput) error {
	c := ai.PromptConfig{
		Instructions: in.Instructions,
		TaskNotes:    in.TaskNotes,
		UpdatedAt:    time.Now().Format(time.RFC3339),
	}
	// ★ 与出厂默认一致就存空 —— 存下来等于把默认值冻结在用户账套里，
	// 程序以后改进默认提示词就再也到不了他那儿。
	if c.Normalize().Instructions == strings.TrimSpace(aiprovider.DefaultInstructions()) {
		c.Instructions = ""
	}
	for k, v := range c.TaskNotes {
		if !ai.ValidTask(k) {
			return fmt.Errorf("未知的任务类型 %q", k)
		}
		_ = v
	}
	if err := s.db.AI().SavePromptConfig(ctx, c); err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAIPromptSave,
		Summary: "修改 AI 记账提示词",
		Detail: map[string]any{
			"记账要求长度": len([]rune(c.Instructions)),
			"任务附加要求": c.TaskNotes,
		},
	})
	return nil
}

// ResetAIPromptConfig 把提示词恢复成出厂默认。
func (s *Service) ResetAIPromptConfig(ctx context.Context) error {
	if err := s.db.AI().ResetPromptConfig(ctx); err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{
		Action: audit.ActionAIPromptReset, Summary: "把 AI 提示词恢复成出厂默认",
	})
	return nil
}

// AIPromptPreview 是「现在实际发给模型的是什么」。
type AIPromptPreview struct {
	// System / User 是两份提示词的全文。
	System string `json:"system"`
	User   string `json:"user"`
	// Digest 是提示词指纹（sha256），审计表里记的就是它。
	Digest string `json:"digest"`
	// Chars 是系统提示词的字符数，让用户对「一次调用有多大」有概念。
	Chars int `json:"chars"`
	// Task 是这次预览用的任务类型。
	Task string `json:"task"`
}

// PreviewAIPrompt 用当前设置渲染一份真实提示词。
//
// ★ 预览必须是**真的**：用同一套 SystemPrompt/UserPrompt 生成，
// 只是把「这笔业务」换成一个示例。另写一份预览逻辑必然与真实
// 提示词漂移，而用户是照着预览判断「我的设置生效了没有」的。
func (s *Service) PreviewAIPrompt(ctx context.Context, task string) (*AIPromptPreview, error) {
	t := aiTask(task)
	c, err := s.db.AI().PromptConfig(ctx)
	if err != nil {
		return nil, err
	}
	in := aiprovider.Input{
		Task:   t,
		Text:   "（示例）收到杭州某某科技有限公司货款",
		Date:   calendar.Today().String(),
		Prompt: c,
	}
	// 账套上下文尽量取真的；取不到（还没建账）也要能预览，
	// 否则用户在设置页里根本看不到自己的提示词长什么样。
	if svc := s.db.AI(); svc != nil {
		if book, err := s.db.AI().BookContext(ctx); err == nil {
			in.Book = book
		}
		if accts, err := s.db.AI().Accounts(ctx); err == nil {
			in.Accounts = accts
		}
		if cs, err := s.db.AI().Contacts(ctx); err == nil {
			in.Contacts = cs
		}
	}
	sys := aiprovider.SystemPrompt(in)
	usr := aiprovider.UserPrompt(in)
	return &AIPromptPreview{
		System: sys, User: usr,
		Digest: ai.Digest(sys + "\n" + usr),
		Chars:  len([]rune(sys)),
		Task:   string(t),
	}, nil
}

// ---------------------------------------------------------------------------
// 采纳 / 拒绝一条 AI 提议
// ---------------------------------------------------------------------------

// AIAcceptResult 是采纳一条提议的结果。
type AIAcceptResult struct {
	// VoucherID / VoucherNo 是落成的那张**草稿**凭证。
	//
	// ★ 只落草稿，不直接过账。AI 提议是「填好的一页纸」，
	// 过账意味着它进了总账 —— 那一步必须有人按下去，
	// 而且要经过与手工录入完全相同的校验。
	VoucherID int64  `json:"voucherId"`
	VoucherNo string `json:"voucherNo"`
	Summary   string `json:"summary"`
}

// AcceptAISuggestion 把一条通过护栏的 AI 提议落成凭证草稿。
//
// 提议从**审计表**里取，不从界面传回来：
// 界面传回来的内容可以被改，而事后追溯要回答的是
// 「模型当时说了什么、人改了什么」—— 两者必须分得开。
func (s *Service) AcceptAISuggestion(ctx context.Context, id int64,
	createdBy string) (*AIAcceptResult, error) {

	rec, err := s.db.AI().Suggestion(ctx, id)
	if err != nil {
		return nil, err
	}
	if rec.Proposed == nil {
		return nil, fmt.Errorf("这条建议的内容已经读不出来了（提议 JSON 解析失败），" +
			"不能采纳 —— 请在界面上重新生成一次")
	}
	if rec.Decision == string(aiprovider.DecisionAccepted) {
		return nil, fmt.Errorf("这条建议已经采纳过了（凭证 #%v）", derefID(rec.FinalVoucher))
	}
	if rec.Status != "valid" {
		return nil, fmt.Errorf("这条建议没有通过护栏校验，不能采纳：%s", rec.RejectReason)
	}

	// ★ 重新校验一次，而不是信当时那张护栏报告。
	//
	// 账套是会变的：期间可能已经结账、科目可能已经停用、往来单位可能被删。
	// 隔了一天再点「采纳」，当时通过的提议现在可能通不过了。
	lctx, err := s.db.AI().LedgerContext(ctx)
	if err != nil {
		return nil, err
	}
	report, verr := rec.Proposed.Validate(lctx, ai.Expect{})
	if verr != nil {
		return nil, verr
	}
	if !report.Passed() {
		return nil, fmt.Errorf("这张凭证按当前账套通不过校验，不能采纳：%s",
			aiReportSummary(report))
	}

	v, err := aiprovider.Draft(rec.Proposed, createdBy, &ai.AIProvenance{
		Provider: rec.ProviderName, Model: rec.Model, Layer: rec.Layer,
		Confidence: rec.Confidence, SuggestionID: rec.ID,
	})
	if err != nil {
		return nil, err
	}

	in := VoucherInput{
		Word: string(v.Word), Date: v.BizDate.String(), Remark: v.Remark,
		CreatedBy: createdBy,
	}
	for _, e := range v.Entries {
		in.Lines = append(in.Lines, VoucherLineInput{
			AccountCode: e.AccountCode, Summary: e.Summary,
			Debit: e.Debit, Credit: e.Credit,
			ContactID: e.Aux.ContactID, EmployeeID: e.Aux.EmployeeID,
			DeptID: e.Aux.DeptID, ProjectID: e.Aux.ProjectID,
		})
	}
	saved, err := s.SaveVoucher(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := s.db.AI().Decide(ctx, rec.ID,
		aiprovider.DecisionAccepted, &saved.ID, ""); err != nil {
		return nil, fmt.Errorf("凭证已经生成（%s），但采纳记录写失败：%w",
			saved.No, err)
	}
	// ★ 草稿**还没有凭证号**（编号在过账时才分配，草稿不占号）。
	// 这里直接写 saved.No 的话，提示语会变成「已生成草稿凭证 ，请核对后过账」——
	// 中间空一格，用户以为出了错。
	label := saved.No
	if strings.TrimSpace(label) == "" {
		label = fmt.Sprintf("（草稿 #%d）", saved.ID)
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAIAccept,
		Summary: fmt.Sprintf("采纳 AI 建议 #%d，生成草稿凭证 %s", rec.ID, label),
		Entity:  "ai_suggestion", EntityID: strconv.FormatInt(rec.ID, 10),
		Operator: createdBy, Source: audit.SourceAI,
		Detail: map[string]any{
			"建议编号": rec.ID, "模型": rec.Model, "提供方": rec.ProviderName,
			"置信度":   rec.Confidence,
			"生成的凭证": saved.ID,
		},
	})
	return &AIAcceptResult{
		VoucherID: saved.ID, VoucherNo: saved.No,
		Summary: fmt.Sprintf("已按 AI 建议生成草稿凭证 %s，请核对后过账", label),
	}, nil
}

// RejectAISuggestion 记录「这条建议没用」。
func (s *Service) RejectAISuggestion(ctx context.Context, id int64, reason string) error {
	rec, err := s.db.AI().Suggestion(ctx, id)
	if err != nil {
		return err
	}
	if rec.Decision == string(aiprovider.DecisionRejected) {
		return nil // 幂等：重复拒绝不算错
	}
	if err := s.db.AI().Decide(ctx, id, aiprovider.DecisionRejected, nil,
		strings.TrimSpace(reason)); err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAIReject,
		Summary: fmt.Sprintf("拒绝 AI 建议 #%d", id),
		Entity:  "ai_suggestion", EntityID: strconv.FormatInt(id, 10),
		Source: audit.SourceAI,
		Detail: map[string]any{"建议编号": id, "原因": strings.TrimSpace(reason)},
	})
	return nil
}

func derefID(p *int64) any {
	if p == nil {
		return "—"
	}
	return *p
}

// aiReportSummary 把护栏报告压成一句话。
func aiReportSummary(r *ai.Report) string {
	var parts []string
	for _, f := range r.Failures() {
		parts = append(parts, f.Title)
	}
	if len(parts) == 0 {
		return "未通过校验"
	}
	return strings.Join(parts, "；")
}
