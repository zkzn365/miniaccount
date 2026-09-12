package aiprovider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"miniaccount/internal/domain/ai"
)

// ---------------------------------------------------------------------------
// 工具集
// ---------------------------------------------------------------------------

func TestToolSetBasics(t *testing.T) {
	ts := NewToolSet(
		Tool{Name: "b", Description: "B"},
		Tool{Name: "a", Description: "A"},
		Tool{Name: ""}, // 无名工具应被跳过
	)
	if ts.Len() != 2 {
		t.Errorf("工具数 = %d，期望 2", ts.Len())
	}
	// 名字排序稳定，便于测试与展示
	if got := ts.Names(); got[0] != "a" || got[1] != "b" {
		t.Errorf("工具名 = %v，期望 [a b]", got)
	}
	if _, ok := ts.Get("a"); !ok {
		t.Error("应能取到工具 a")
	}
	if _, ok := ts.Get("不存在"); ok {
		t.Error("不存在的工具不该取到")
	}
	// nil 工具集不该 panic
	var nilTS *ToolSet
	if nilTS.Len() != 0 || nilTS.Names() != nil {
		t.Error("nil 工具集应安全返回空值")
	}
}

// fakeAccountProvider 提供固定的科目与往来单位。
type fakeAccountProvider struct {
	accounts []AccountBrief
	contacts []ContactBrief
}

func (f *fakeAccountProvider) Accounts(context.Context) ([]AccountBrief, error) {
	return f.accounts, nil
}
func (f *fakeAccountProvider) Contacts(context.Context) ([]ContactBrief, error) {
	return f.contacts, nil
}

func testProvider() *fakeAccountProvider {
	return &fakeAccountProvider{
		accounts: []AccountBrief{
			{Code: "1002", Name: "银行存款", FullName: "银行存款", Direction: "借"},
			{Code: "1122", Name: "应收账款", FullName: "应收账款", Direction: "借",
				AuxTypes: []string{"客户"}},
			{Code: "560210", Name: "管理费用—租赁费", FullName: "管理费用—租赁费",
				Direction: "借", AuxTypes: []string{"部门"}},
		},
		contacts: []ContactBrief{
			{ID: 1, Name: "杭州云帆科技有限公司", Kind: "customer"},
			{ID: 2, Name: "宁波恒信办公用品", Kind: "supplier"},
		},
	}
}

func runTool(t *testing.T, tool Tool, args string) string {
	t.Helper()
	out, err := tool.Run(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("工具执行失败: %v", err)
	}
	return out
}

func TestSearchAccountsTool(t *testing.T) {
	tool := SearchAccountsTool(testProvider())

	// 按关键词命中
	out := runTool(t, tool, `{"keyword":"租赁"}`)
	if !strings.Contains(out, "560210") {
		t.Errorf("应命中 560210，实际 %q", out)
	}
	if !strings.Contains(out, "部门") {
		t.Error("结果应带上必需的辅助核算维度 —— 模型据此才知道要填部门")
	}
	// 命中多个
	out = runTool(t, tool, `{"keyword":"银行"}`)
	if !strings.Contains(out, "1002") {
		t.Errorf("应命中 1002，实际 %q", out)
	}
	// 没命中时给出**可操作的**提示，而不是空串
	out = runTool(t, tool, `{"keyword":"不存在的科目"}`)
	if !strings.Contains(out, "没有匹配") || !strings.Contains(out, "warnings") {
		t.Errorf("没命中时应引导模型写 warnings，实际 %q", out)
	}
	// limit 生效
	out = runTool(t, tool, `{"keyword":"","limit":1}`)
	if n := strings.Count(out, "\n"); n != 1 {
		t.Errorf("limit=1 应只返回一条，实际 %d 条", n)
	}
	// 参数格式错误要报错
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"keyword":123}`)); err == nil {
		t.Error("参数类型错误应报错")
	}
}

func TestSearchContactsTool(t *testing.T) {
	tool := SearchContactsTool(testProvider())

	out := runTool(t, tool, `{"keyword":"云帆"}`)
	if !strings.Contains(out, "1") || !strings.Contains(out, "杭州云帆科技有限公司") {
		t.Errorf("应命中 id=1 的客户，实际 %q", out)
	}
	out = runTool(t, tool, `{"keyword":"恒信"}`)
	if !strings.Contains(out, "宁波恒信") {
		t.Errorf("应命中供应商，实际 %q", out)
	}
	// 没命中时要**明确禁止编造 id**
	out = runTool(t, tool, `{"keyword":"不存在的公司"}`)
	if !strings.Contains(out, "不要编造") {
		t.Errorf("没命中时应明确禁止编造 id，实际 %q", out)
	}
}

// fakeHistory 提供固定的历史范例。
type fakeHistory struct{ examples []Example }

func (f *fakeHistory) Similar(context.Context, string, string, int64, int) ([]Example, error) {
	return f.examples, nil
}

func TestFindSimilarVouchersTool(t *testing.T) {
	hist := &fakeHistory{examples: []Example{{
		VoucherNo: "记-2025-01-0003", Date: "2025-01-20", Remark: "支付房租",
		Text: "支付房租", Score: 0.83,
		Lines: []ExampleLine{
			{Summary: "支付房租", AccountCode: "560210", AccountName: "管理费用—租赁费",
				Debit: 300000},
			{Summary: "支付房租", AccountCode: "1002", AccountName: "银行存款",
				Credit: 300000},
		},
	}}}
	tool := FindSimilarVouchersTool(hist)

	out := runTool(t, tool, `{"text":"支付房租","limit":3}`)
	if !strings.Contains(out, "记-2025-01-0003") {
		t.Errorf("应返回历史凭证号，实际 %q", out)
	}
	if !strings.Contains(out, "借 560210") || !strings.Contains(out, "贷 1002") {
		t.Errorf("应返回完整分录（模型要照着抄），实际 %q", out)
	}

	// 没有历史时要有**明确的兜底**：告诉模型这是一笔新业务
	empty := FindSimilarVouchersTool(&fakeHistory{})
	out = runTool(t, empty, `{"text":"新业务"}`)
	if !strings.Contains(out, "新业务") {
		t.Errorf("没有历史时应说明这是新业务，实际 %q", out)
	}
}

// ---------------------------------------------------------------------------
// Agent 循环
// ---------------------------------------------------------------------------

// scriptedProvider 按脚本依次返回响应，用于驱动 Agent 循环。
type scriptedProvider struct {
	responses []*Response
	calls     []Request
	idx       int
	err       error
}

func (s *scriptedProvider) Name() string  { return "脚本模型" }
func (s *scriptedProvider) Model() string { return "scripted-1" }
func (s *scriptedProvider) Kind() Kind    { return KindLocal }
func (s *scriptedProvider) Complete(_ context.Context, req Request) (*Response, error) {
	s.calls = append(s.calls, req)
	if s.err != nil {
		return nil, s.err
	}
	if s.idx >= len(s.responses) {
		return &Response{Content: "{}"}, nil
	}
	r := s.responses[s.idx]
	s.idx++
	return r, nil
}

func TestAgentNoToolCalls(t *testing.T) {
	p := &scriptedProvider{responses: []*Response{
		{Content: `{"ok":1}`, TokensIn: 10, TokensOut: 5},
	}}
	a := &Agent{Provider: p, Tools: NewToolSet(), Options: DefaultAgentOptions()}

	res, err := a.Run(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if res.Content != `{"ok":1}` {
		t.Errorf("内容 = %q", res.Content)
	}
	if res.Rounds != 1 {
		t.Errorf("轮数 = %d，期望 1", res.Rounds)
	}
	if len(res.ToolCalls) != 0 {
		t.Error("不该有工具调用")
	}
	if res.TokensIn != 10 || res.TokensOut != 5 {
		t.Errorf("token 累计 = %d/%d", res.TokensIn, res.TokensOut)
	}
	// 没有工具时不该把 tools 字段发出去
	if len(p.calls[0].Tools) != 0 {
		t.Error("没有工具时不该声明 tools")
	}
}

// ★ 完整的一轮工具调用：模型先查科目，再给最终答案
func TestAgentToolLoop(t *testing.T) {
	p := &scriptedProvider{responses: []*Response{
		{
			Content: "我需要先查一下科目",
			ToolCalls: []ToolCall{{
				ID: "call_1", Name: "search_accounts",
				Arguments: `{"keyword":"租赁"}`,
			}},
			TokensIn: 100, TokensOut: 20,
		},
		{Content: `{"voucher":{}}`, TokensIn: 150, TokensOut: 60},
	}}
	ts := NewToolSet(SearchAccountsTool(testProvider()))
	a := &Agent{Provider: p, Tools: ts, Options: DefaultAgentOptions()}

	res, err := a.Run(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if res.Rounds != 2 {
		t.Errorf("轮数 = %d，期望 2", res.Rounds)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("工具调用数 = %d，期望 1", len(res.ToolCalls))
	}
	tc := res.ToolCalls[0]
	if tc.Name != "search_accounts" || tc.Round != 1 {
		t.Errorf("调用记录有误: %+v", tc)
	}
	if !strings.Contains(tc.Result, "560210") {
		t.Errorf("工具结果应被记录，实际 %q", tc.Result)
	}
	// token 要累加两轮
	if res.TokensIn != 250 || res.TokensOut != 80 {
		t.Errorf("token 累计 = %d/%d，期望 250/80", res.TokensIn, res.TokensOut)
	}

	// ★ 第二轮请求里必须带上工具声明与完整历史 ——
	// 模型是靠看到第一轮的工具结果才知道下一步的
	second := p.calls[1]
	if len(second.Tools) == 0 {
		t.Error("第二轮仍应带上工具声明")
	}
	if len(second.Messages) < 3 {
		t.Fatalf("第二轮应带上历史（system/user/assistant/tool），实际 %d 条",
			len(second.Messages))
	}
	var hasToolMsg bool
	for _, m := range second.Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "560210") &&
			m.ToolCallID == "call_1" {
			hasToolMsg = true
		}
	}
	if !hasToolMsg {
		t.Error("第二轮应包含带 tool_call_id 的工具结果消息")
	}
}

// 未知工具不该让整次建议失败，而应告诉模型有哪些工具可用
func TestAgentUnknownTool(t *testing.T) {
	p := &scriptedProvider{responses: []*Response{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "drop_database", Arguments: "{}"}}},
		{Content: `{"ok":1}`},
	}}
	a := &Agent{Provider: p, Tools: NewToolSet(SearchAccountsTool(testProvider())),
		Options: DefaultAgentOptions()}

	res, err := a.Run(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatalf("未知工具不该让整次失败: %v", err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Err == "" {
		t.Fatalf("应记录未知工具的错误: %+v", res.ToolCalls)
	}
	if !strings.Contains(res.ToolCalls[0].Err, "可用工具") {
		t.Errorf("应告诉模型有哪些工具可用，实际 %q", res.ToolCalls[0].Err)
	}
	// 错误信息也要回灌给模型
	var found bool
	for _, m := range p.calls[1].Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "没有名为") {
			found = true
		}
	}
	if !found {
		t.Error("未知工具的错误应回灌给模型，让它下一轮自行改正")
	}
}

// ★ 轮数必须有上限：模型可能陷入反复查同一个科目的循环
func TestAgentMaxRounds(t *testing.T) {
	// 每一轮都要求调用工具，永远不给最终答案
	resp := &Response{ToolCalls: []ToolCall{{
		ID: "c", Name: "search_accounts", Arguments: `{"keyword":""}`,
	}}}
	p := &scriptedProvider{}
	for i := 0; i < 20; i++ {
		p.responses = append(p.responses, resp)
	}
	a := &Agent{Provider: p, Tools: NewToolSet(SearchAccountsTool(testProvider())),
		Options: AgentOptions{MaxRounds: 3}}

	res, err := a.Run(context.Background(), "sys", "usr")
	if !errors.Is(err, ErrNoFinalAnswer) {
		t.Fatalf("轮数用尽应报 ErrNoFinalAnswer，实际 %v", err)
	}
	if !res.Truncated {
		t.Error("应标记为已截断")
	}
	if res.Rounds != 3 {
		t.Errorf("轮数 = %d，期望刚好用完 3 轮", res.Rounds)
	}
	if len(p.calls) != 3 {
		t.Errorf("模型被调用 %d 次，期望 3 次（不能无限循环）", len(p.calls))
	}
}

// 模型报错时应带着已消耗的 token 一起返回，便于排查与计费
func TestAgentProviderError(t *testing.T) {
	p := &scriptedProvider{err: errors.New("连接被拒绝")}
	a := &Agent{Provider: p, Tools: NewToolSet(), Options: DefaultAgentOptions()}
	if _, err := a.Run(context.Background(), "s", "u"); err == nil {
		t.Fatal("模型报错应向上传递")
	}
}

func TestAgentRequiresProvider(t *testing.T) {
	a := &Agent{}
	if _, err := a.Run(context.Background(), "s", "u"); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("缺少 Provider 应报 ErrNoProvider，实际 %v", err)
	}
}

// Agent 的最终输出走**同一套解析与护栏**
func TestParseAgentResult(t *testing.T) {
	res := &AgentResult{Content: `{"voucher":{"word":"记","biz_date":"2025-01-01",
	  "remark":"x","entries":[]},"confidence":0.9,"reasoning":"r",
	  "evidence":[],"warnings":[]}`}
	p, err := ParseAgentResult(res)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if p.Voucher.Remark != "x" {
		t.Errorf("解析结果有误: %+v", p.Voucher)
	}

	// 自由文本照样被拒绝 —— 多了一轮工具调用不该放松格式要求
	bad := &AgentResult{Content: "我觉得应该借银行存款"}
	if _, err := ParseAgentResult(bad); !errors.Is(err, ai.ErrMalformedJSON) {
		t.Errorf("自由文本应被拒绝，实际 %v", err)
	}
	// 空内容
	if _, err := ParseAgentResult(&AgentResult{}); err == nil {
		t.Error("空内容应报错")
	}
	if _, err := ParseAgentResult(nil); err == nil {
		t.Error("nil 结果应报错")
	}
}
