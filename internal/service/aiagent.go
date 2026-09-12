package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"miniaccount/internal/ai/aiprovider"
	"miniaccount/internal/domain/ai"
	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/money"
)

// ---------------------------------------------------------------------------
// AI Agent 批量记账
// ---------------------------------------------------------------------------
//
// # 与「单笔建议」的区别
//
// 单笔建议是：你描述一笔业务 → 它给一张凭证。适合偶尔问一句。
//
// 批量记账是：**把待处理的单据交给它跑一遍** —— 一批还没记账的银行流水、
// 几张待生成凭证的发票。这是会计每天真正要干的活：
// 月初打开软件，几十条流水等着记。
//
// # 边界（与单笔一致，一处都不放松）
//
//   - Agent 的工具全是**只读**的（查科目、查往来、查历史凭证），
//     它没有「顺手把凭证存了」的能力；
//   - 每条提议都过同一套护栏（科目必须存在、借贷必须相等、辅助核算齐全…）；
//   - 每条提议都写进 ai_suggestion 审计表（模型原话、指纹、token 消耗）；
//   - **过账仍然要人按**。跑完一批得到的是一堆草稿，不是一本账。
//
// # 为什么在后台跑
//
// 一次调用几秒到几十秒，十条就是几分钟。同步等会把界面卡死
// （Wails 的绑定是请求-响应），所以起一个运行记录、界面轮询进度。
// 这也让「跑到一半发现不对，停掉」成为可能。

// AIAgentSource 是待记账单据的来源。
type AIAgentSource string

// 支持的来源。
const (
	// AgentSourceBankFlows 是还没生成凭证的银行流水（最高频）。
	AgentSourceBankFlows AIAgentSource = "bank_flows"
	// AgentSourceInvoices 是还没生成凭证的发票。
	AgentSourceInvoices AIAgentSource = "invoices"
	// AgentSourceClaims 是已审批但还没生成凭证的报销单。
	AgentSourceClaims AIAgentSource = "claims"
)

// Label 返回中文名。
func (s AIAgentSource) Label() string {
	switch s {
	case AgentSourceBankFlows:
		return "银行流水"
	case AgentSourceInvoices:
		return "发票"
	case AgentSourceClaims:
		return "报销单"
	}
	return string(s)
}

// Valid 判断来源是否合法。
func (s AIAgentSource) Valid() bool {
	switch s {
	case AgentSourceBankFlows, AgentSourceInvoices, AgentSourceClaims:
		return true
	}
	return false
}

// AllAgentSources 返回可选来源。
func AllAgentSources() []AIAgentSource {
	return []AIAgentSource{AgentSourceBankFlows, AgentSourceInvoices, AgentSourceClaims}
}

// 一轮最多处理多少条。
//
// ★ 必须有上限：每条一次模型调用，用户点「开始」时未必意识到
// 这是在按条计费。50 条已经够一个月的流水了，再多应当是分几次做 ——
// 而且中途发现记法不对，损失也小。
const (
	agentDefaultLimit = 10
	agentMaxLimit     = 50
)

// AIAgentRequest 是一次批量记账的入参。
type AIAgentRequest struct {
	Source AIAgentSource
	// Limit 为 0 时取 agentDefaultLimit，上限 agentMaxLimit。
	Limit int
	// Date 是业务日期（YYYY-MM-DD）；留空按每条单据自己的日期。
	Date string
}

// AIAgentItem 是批量结果里的一条。
type AIAgentItem struct {
	TargetType string `json:"targetType"`
	TargetID   int64  `json:"targetId"`
	// Label 是这个单据的人话标识，如「03-05 收 杭州云帆科技 106,000.00」。
	Label string `json:"label"`
	// OK 为真表示通过了全部护栏，可以采纳。
	OK bool `json:"ok"`
	// SuggestionID 是审计表里的记录 id（采纳时用它）。
	SuggestionID int64   `json:"suggestionId"`
	Summary      string  `json:"summary"`
	Error        string  `json:"error,omitempty"`
	Confidence   float64 `json:"confidence"`
	// Failures 是护栏不通过的原因（摊开给用户看）。
	Failures []string `json:"failures,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	// Voucher 是通过护栏时的建议凭证。
	Voucher *VoucherDraft `json:"voucher,omitempty"`
	// Accepted / Rejected 记录用户对这条的处置（跑完之后才有）。
	Decision string `json:"decision,omitempty"`
}

// AIAgentRun 是一次批量记账的运行记录。
type AIAgentRun struct {
	ID     string        `json:"id"`
	Source AIAgentSource `json:"source"`
	// State 是 running / done / cancelled / failed。
	State string `json:"state"`
	Total int    `json:"total"`
	Done  int    `json:"done"`
	// OKCount 是通过护栏、可以直接采纳的条数。
	OKCount    int           `json:"okCount"`
	Items      []AIAgentItem `json:"items"`
	Model      string        `json:"model"`
	Error      string        `json:"error,omitempty"`
	StartedAt  string        `json:"startedAt"`
	FinishedAt string        `json:"finishedAt,omitempty"`
	// Cost 是累计 token 消耗，让用户对「这一轮花了多少」有概念。
	TokensIn  int `json:"tokensIn"`
	TokensOut int `json:"tokensOut"`
}

var (
	agentRunsMu sync.Mutex
	agentRuns   = map[string]*AIAgentRun{}
	agentCancel = map[string]context.CancelFunc{}
	agentSeq    int64
)

// agentRunTTL 是运行记录的保留条数（界面最多也就看最近几次）。
const agentRunTTL = 20

// StartAIAgentRun 起一轮批量记账，立刻返回运行记录，后台继续跑。
func (s *Service) StartAIAgentRun(ctx context.Context, req AIAgentRequest) (*AIAgentRun, error) {
	if !req.Source.Valid() {
		return nil, fmt.Errorf("未知的待记账来源 %q", req.Source)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = agentDefaultLimit
	}
	if limit > agentMaxLimit {
		return nil, fmt.Errorf("一次最多处理 %d 条（当前 %d）—— "+
			"每条都要调一次模型，分几次做更稳妥", agentMaxLimit, limit)
	}

	// 没配模型时直接报清楚，而不是跑出一条条「生成失败」
	prov, err := s.providerFor(ctx)
	if err != nil {
		return nil, err
	}
	if _, disabled := prov.(aiprovider.Disabled); disabled {
		return nil, fmt.Errorf("还没有可用的模型服务 —— 请先在「设置」里配置，" +
			"或在 AI 配置里填好接口地址与密钥")
	}

	items, err := s.pendingForAgent(ctx, req)
	if err != nil {
		return nil, err
	}

	agentRunsMu.Lock()
	agentSeq++
	run := &AIAgentRun{
		ID:        fmt.Sprintf("run-%d-%d", time.Now().Unix(), agentSeq),
		Source:    req.Source,
		State:     "running",
		Total:     len(items),
		Items:     items,
		Model:     prov.Model(),
		StartedAt: audit.NowStamp(time.Now()),
	}
	agentRuns[run.ID] = run
	pruneAgentRunsLocked()
	runCtx, cancel := context.WithCancel(context.Background())
	agentCancel[run.ID] = cancel
	agentRunsMu.Unlock()

	go s.runAgentBatch(runCtx, run, req)

	agentRunsMu.Lock()
	out := cloneRunLocked(run)
	agentRunsMu.Unlock()
	return out, nil
}

// AIAgentRunStatus 返回运行进度。
func (s *Service) AIAgentRunStatus(id string) (*AIAgentRun, error) {
	agentRunsMu.Lock()
	defer agentRunsMu.Unlock()
	run, ok := agentRuns[id]
	if !ok {
		return nil, fmt.Errorf("找不到这次运行（可能已经过期）")
	}
	return cloneRunLocked(run), nil
}

// LatestAIAgentRun 返回最近一次运行（界面打开时直接显示上一次的结果）。
func (s *Service) LatestAIAgentRun() *AIAgentRun {
	agentRunsMu.Lock()
	defer agentRunsMu.Unlock()
	var latest *AIAgentRun
	for _, r := range agentRuns {
		if latest == nil || r.StartedAt > latest.StartedAt {
			latest = r
		}
	}
	if latest == nil {
		return nil
	}
	return cloneRunLocked(latest)
}

// CancelAIAgentRun 停止一轮运行（当前那条跑完就停）。
func (s *Service) CancelAIAgentRun(id string) error {
	agentRunsMu.Lock()
	cancel, ok := agentCancel[id]
	agentRunsMu.Unlock()
	if !ok {
		return nil // 已经结束了，不算错
	}
	cancel()
	return nil
}

// runAgentBatch 是后台的实际执行：逐条跑，逐条记。
func (s *Service) runAgentBatch(ctx context.Context, run *AIAgentRun, req AIAgentRequest) {
	prov, err := s.providerFor(ctx)
	if err != nil {
		finishRun(run, "failed", err.Error())
		return
	}
	prompt, err := s.db.AI().PromptConfig(ctx)
	if err != nil {
		finishRun(run, "failed", err.Error())
		return
	}
	sg := aiSuggester(s, prov)

	for i := range run.Items {
		select {
		case <-ctx.Done():
			// 用户点了停止：把已经跑完的结果留着，别丢掉
			finishRun(run, "cancelled", "")
			return
		default:
		}
		item := run.Items[i]
		input, ok := s.agentInputFor(ctx, run.Source, item.TargetID, req.Date, prompt)
		if !ok {
			setItemResult(run, i, AIAgentItem{
				TargetType: item.TargetType, TargetID: item.TargetID,
				Label: item.Label, Error: "读不到这条单据（可能已被处理）",
			})
			continue
		}
		id := item.TargetID
		res := sg.Suggest(ctx, input, item.TargetType, &id)
		setItemResult(run, i, agentItemFromResult(item, res))
		// 进度与 token 一起更新：用户能看到「跑到第几条、花了多少」
		agentRunsMu.Lock()
		run.Done = i + 1
		if res.Response != nil {
			run.TokensIn += res.Response.TokensIn
			run.TokensOut += res.Response.TokensOut
		}
		agentRunsMu.Unlock()
	}
	finishRun(run, "done", "")
}

// agentItemFromResult 把编排器的结果转成界面要的一条。
func agentItemFromResult(item AIAgentItem, res *aiprovider.Result) AIAgentItem {
	out := item
	out.Confidence = 0
	out.SuggestionID = res.SuggestionID
	out.Summary = res.Summary()
	out.OK = res.OK()
	if res.Err != nil {
		out.Error = res.Err.Error()
	}
	if res.Report != nil {
		out.Failures = checkTitles(res.Report.Failures())
		out.Warnings = checkTitles(res.Report.Warnings())
	}
	if res.Proposal != nil {
		out.Confidence = res.Proposal.Confidence
		if v, err := res.Proposal.Materialize("AI 提议", nil); err == nil {
			d := &VoucherDraft{
				Word: string(v.Word), BizDate: v.BizDate.String(),
				Remark: v.Remark, Total: v.TotalDebit(),
			}
			for _, e := range v.Entries {
				d.Entries = append(d.Entries, ClosingEntry{
					AccountCode: e.AccountCode, Summary: e.Summary,
					Debit: e.Debit, Credit: e.Credit, AuxDesc: auxDesc(e.Aux),
				})
			}
			out.Voucher = d
		}
	}
	return out
}

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

func setItemResult(run *AIAgentRun, i int, item AIAgentItem) {
	agentRunsMu.Lock()
	if i >= 0 && i < len(run.Items) {
		// 保留原始的单据信息（Label/TargetID），只覆盖结果字段
		orig := run.Items[i]
		item.TargetType, item.TargetID, item.Label = orig.TargetType, orig.TargetID, orig.Label
		if item.OK {
			run.OKCount++
		}
		run.Items[i] = item
	}
	agentRunsMu.Unlock()
}

func finishRun(run *AIAgentRun, state, errMsg string) {
	agentRunsMu.Lock()
	run.State = state
	run.Error = errMsg
	run.FinishedAt = audit.NowStamp(time.Now())
	delete(agentCancel, run.ID)
	agentRunsMu.Unlock()
}

// cloneRunLocked 复制一份给界面：运行还在后台改，直接给指针会有数据竞争。
//
// ★ 调用方**必须已经持有 agentRunsMu**。
//
// 这里踩过一次：原来叫 cloneRun，自己加锁；而 AIAgentRunStatus 已经持锁
// 再调它 —— Go 的互斥锁不可重入，于是**自己把自己锁死**，界面查进度直接挂住。
// 改名成 ...Locked 是让「调用方持锁」这件事写在名字上。
func cloneRunLocked(run *AIAgentRun) *AIAgentRun {
	cp := *run
	cp.Items = append([]AIAgentItem(nil), run.Items...)
	return &cp
}

func pruneAgentRunsLocked() {
	if len(agentRuns) <= agentRunTTL {
		return
	}
	ids := make([]string, 0, len(agentRuns))
	for id := range agentRuns {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids[:len(ids)-agentRunTTL] {
		if _, running := agentCancel[id]; running {
			continue // 还在跑的不能丢
		}
		delete(agentRuns, id)
	}
}

// ---------------------------------------------------------------------------
// 待记账单据
// ---------------------------------------------------------------------------

// pendingForAgent 取出这一轮要处理哪些单据。
//
// 只取**还没生成凭证**的：已经记过的再记一遍就是重复记账，
// 而 AI 看不出「这条已经记过」—— 那是程序该保证的事。
func (s *Service) pendingForAgent(ctx context.Context, req AIAgentRequest) ([]AIAgentItem, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = agentDefaultLimit
	}
	switch req.Source {
	case AgentSourceBankFlows:
		rows, err := s.BankFlows(ctx, BankFlowQuery{Status: "imported", Limit: limit})
		if err != nil {
			return nil, err
		}
		out := make([]AIAgentItem, 0, len(rows))
		for _, r := range rows {
			out = append(out, AIAgentItem{
				TargetType: "bank_flow", TargetID: r.ID,
				Label: fmt.Sprintf("%s %s %s %s",
					r.Date, r.DirectionLabel, orDashText(r.CounterpartyName),
					money.Money(absMoney(r.Amount)).String()),
			})
		}
		return out, nil

	case AgentSourceInvoices:
		rows, err := s.Invoices(ctx, InvoiceQuery{})
		if err != nil {
			return nil, err
		}
		out := make([]AIAgentItem, 0, len(rows))
		for _, r := range rows {
			// 只取「还没生成凭证」的：已经记过的再记一遍就是重复记账
			if r.Posted {
				continue
			}
			out = append(out, AIAgentItem{
				TargetType: "invoice", TargetID: r.ID,
				Label: fmt.Sprintf("%s发票 %s %s %s", r.DirectionLabel,
					orDashText(r.Number), invoicePartyName(r), money.Money(r.TotalAmount).String()),
			})
			if len(out) >= limit {
				break
			}
		}
		return out, nil

	case AgentSourceClaims:
		rows, err := s.Claims(ctx, ClaimQuery{Status: "approved"})
		if err != nil {
			return nil, err
		}
		out := make([]AIAgentItem, 0, len(rows))
		for _, r := range rows {
			if r.VoucherNo != "" {
				continue // 已经生成过凭证
			}

			out = append(out, AIAgentItem{
				TargetType: "expense_claim", TargetID: r.ID,
				Label: fmt.Sprintf("报销单 %s %s %s", r.Code,
					orDashText(r.ClaimantName), money.Money(r.TotalAmount).String()),
			})
			if len(out) >= limit {
				break
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("未知的待记账来源 %q", req.Source)
}

// agentInputFor 把一条单据变成模型输入。
func (s *Service) agentInputFor(ctx context.Context, source AIAgentSource,
	id int64, dateOverride string, prompt ai.PromptConfig) (aiprovider.Input, bool) {

	switch source {
	case AgentSourceBankFlows:
		rows, err := s.BankFlows(ctx, BankFlowQuery{Status: "", Limit: 0})
		if err != nil {
			return aiprovider.Input{}, false
		}
		for _, r := range rows {
			if r.ID != id {
				continue
			}
			date := r.Date
			if strings.TrimSpace(dateOverride) != "" {
				date = dateOverride
			}
			dir := "支出"
			if r.Direction == "in" {
				dir = "收入"
			}
			return aiprovider.Input{
				Task: aiprovider.TaskBankFlow, Text: r.Summary,
				Amount: r.Amount, Date: date,
				Counterparty: r.CounterpartyName, Direction: dir,
				Prompt: prompt,
				Extra:  map[string]string{"流水号": r.SerialNo},
			}, true
		}
	case AgentSourceInvoices:
		rows, err := s.Invoices(ctx, InvoiceQuery{})
		if err != nil {
			return aiprovider.Input{}, false
		}
		for _, r := range rows {
			if r.ID != id {
				continue
			}
			return aiprovider.Input{
				Task: aiprovider.TaskInvoice, Text: invoiceText(r),
				Amount: r.TotalAmount, Date: orToday(r.InvoiceDate, dateOverride),
				Counterparty: invoicePartyName(r), Prompt: prompt,
				Extra: map[string]string{"发票号": r.Number},
			}, true
		}
	case AgentSourceClaims:
		rows, err := s.Claims(ctx, ClaimQuery{Status: "approved"})
		if err != nil {
			return aiprovider.Input{}, false
		}
		for _, r := range rows {
			if r.ID != id {
				continue
			}
			return aiprovider.Input{
				Task: aiprovider.TaskExpense, Text: claimText(r),
				Amount: r.TotalAmount, Date: orToday(r.ApplyDate, dateOverride),
				Counterparty: r.ClaimantName, Prompt: prompt,
				Extra: map[string]string{"单号": r.Code},
			}, true
		}
	}
	return aiprovider.Input{}, false
}

// AcceptAIAgentItem 采纳批量结果里的一条（落成草稿凭证）。
//
// 与单笔采纳走同一条路：提议从审计表读、重新过护栏、只落草稿。
func (s *Service) AcceptAIAgentItem(ctx context.Context, runID string,
	index int, createdBy string) (*AIAcceptResult, error) {

	agentRunsMu.Lock()
	run, ok := agentRuns[runID]
	if !ok {
		agentRunsMu.Unlock()
		return nil, fmt.Errorf("找不到这次运行（可能已经过期）")
	}
	if index < 0 || index >= len(run.Items) {
		agentRunsMu.Unlock()
		return nil, fmt.Errorf("序号 %d 超出范围", index)
	}
	item := run.Items[index]
	agentRunsMu.Unlock()

	if item.SuggestionID == 0 {
		return nil, fmt.Errorf("这一条还没有可采纳的建议")
	}
	res, err := s.AcceptAISuggestion(ctx, item.SuggestionID, createdBy)
	if err != nil {
		return nil, err
	}
	agentRunsMu.Lock()
	if index < len(run.Items) {
		run.Items[index].Decision = "accepted"
	}
	agentRunsMu.Unlock()
	return res, nil
}

// RejectAIAgentItem 拒绝批量结果里的一条。
func (s *Service) RejectAIAgentItem(ctx context.Context, runID string,
	index int, reason string) error {

	agentRunsMu.Lock()
	run, ok := agentRuns[runID]
	if !ok {
		agentRunsMu.Unlock()
		return fmt.Errorf("找不到这次运行（可能已经过期）")
	}
	if index < 0 || index >= len(run.Items) {
		agentRunsMu.Unlock()
		return fmt.Errorf("序号 %d 超出范围", index)
	}
	// ★ 在锁里把要用的值取出来。先解锁再读 run.Items 是数据竞争 ——
	// 后台那条 goroutine 正在原地改同一个切片。
	id := run.Items[index].SuggestionID
	agentRunsMu.Unlock()

	if id == 0 {
		return nil
	}
	if err := s.RejectAISuggestion(ctx, id, reason); err != nil {
		return err
	}
	agentRunsMu.Lock()
	if index < len(run.Items) {
		run.Items[index].Decision = "rejected"
	}
	agentRunsMu.Unlock()
	return nil
}

// invoiceText 把一张发票压成一句人话给模型看。
func invoiceText(r InvoiceView) string {
	parts := []string{r.DirectionLabel + r.KindLabel}
	if r.Category != "" {
		parts = append(parts, r.Category)
	}
	if r.Remark != "" {
		parts = append(parts, r.Remark)
	}
	return strings.Join(parts, " ")
}

// invoicePartyName 取「这张发票的对方是谁」——
// 进项票的对方是销方，销项票的对方是购方。
func invoicePartyName(r InvoiceView) string {
	if r.Direction == "in" {
		return r.SellerName
	}
	return r.BuyerName
}

// claimText 把一张报销单压成一句人话。
func claimText(r ClaimView) string {
	parts := []string{"报销"}
	if r.Destination != "" {
		parts = append(parts, r.Destination)
	}
	if r.Reason != "" {
		parts = append(parts, r.Reason)
	}
	names := make([]string, 0, len(r.Items))
	for _, it := range r.Items {
		if it.Summary != "" {
			names = append(names, it.Summary)
		}
	}
	if len(names) > 0 {
		parts = append(parts, strings.Join(names, "、"))
	}
	return strings.Join(parts, " ")
}

func orDashText(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func absMoney(m money.Money) money.Money {
	if m < 0 {
		return -m
	}
	return m
}

func orToday(date, override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	return date
}
