package aiprovider

import (
	"context"
	"strings"
	"testing"
)

// scriptProvider 按剧本依次返回预设的响应。
//
// 对话式会计的关键行为**跨轮次**（第一轮问、第二轮答），
// 而只返回固定内容的假模型测不出来 —— 那样只能测到「一轮之内」的东西。
type scriptProvider struct {
	steps []*Response
	calls int
	// seen 记下每次请求的 messages，用来断言「历史真的传下去了」
	seen [][]Message
}

func (p *scriptProvider) Name() string  { return "剧本模型" }
func (p *scriptProvider) Model() string { return "script-1" }
func (p *scriptProvider) Kind() Kind    { return KindLocal }

func (p *scriptProvider) Complete(_ context.Context, req Request) (*Response, error) {
	p.seen = append(p.seen, req.Messages)
	if p.calls >= len(p.steps) {
		// 剧本用完了就返回最后一步，避免越界 panic 掩盖真正的断言失败
		return p.steps[len(p.steps)-1], nil
	}
	r := p.steps[p.calls]
	p.calls++
	return r, nil
}

func askStep(args string) *Response {
	return &Response{
		Model: "script-1",
		ToolCalls: []ToolCall{
			{ID: "c1", Name: "ask_user", Arguments: args},
		},
	}
}

func proposeStep(content string) *Response {
	return &Response{Content: content, Model: "script-1", TokensIn: 10, TokensOut: 20}
}

// ---------------------------------------------------------------------------
// ★ 追问：模型调用 ask_user 时，这一轮就结束，等用户回答
// ---------------------------------------------------------------------------

func TestAccountantStopsWhenAsking(t *testing.T) {
	prov := &scriptProvider{steps: []*Response{askStep(`{
		"header": "付款方式",
		"question": "这台打印机是怎么付款的？",
		"options": [
			{"label": "银行转账，取得专用发票（推荐）", "description": "可抵扣进项税"},
			{"label": "现金支付，只有收据", "description": "全额计入费用"}
		]
	}`)}}
	a := &Accountant{Provider: prov, Tools: NewToolSet(AskUserTool())}

	reply, history, err := a.Reply(context.Background(), []Message{
		{Role: "system", Content: "你是会计"},
		{Role: "user", Content: "业务描述：昨天买了台打印机"},
	})
	if err != nil {
		t.Fatalf("提问不该算错误: %v", err)
	}
	if reply.Question == nil {
		t.Fatal("★ 模型调了 ask_user，会计就该在等回答")
	}
	if reply.Question.Question != "这台打印机是怎么付款的？" {
		t.Errorf("问题 = %q", reply.Question.Question)
	}
	if reply.Question.Header != "付款方式" {
		t.Errorf("标题 = %q", reply.Question.Header)
	}
	if len(reply.Question.Options) != 2 {
		t.Fatalf("选项数 = %d", len(reply.Question.Options))
	}
	// ★ 推荐项约定：放第一个 + label 带「（推荐）」
	if !reply.Question.Options[0].Recommended {
		t.Error("★ 第一个选项带「（推荐）」，就应当被标成推荐项 —— 界面靠它加高亮")
	}
	if reply.Question.Options[1].Recommended {
		t.Error("第二个选项不该被标成推荐")
	}
	// 提问之后**不该**有提议
	if reply.Proposal != nil {
		t.Error("提问的那一轮不该同时给凭证")
	}
	// 历史要带上这次调用，用户答完之后模型才接得上
	if len(history) < 3 {
		t.Errorf("历史条数 = %d，应当包含 system / user / assistant(工具调用)", len(history))
	}
}

// 只带了 label、没写「推荐」的，不标推荐 —— 不能瞎猜
func TestParseQuestionRecommendedNeedsMarker(t *testing.T) {
	q, err := ParseQuestion(`{"question":"选一个","options":[{"label":"甲"},{"label":"乙"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range q.Options {
		if o.Recommended {
			t.Errorf("第 %d 个选项没有「推荐」字样，不该被标成推荐项", i+1)
		}
	}
}

func TestParseQuestionRejectsEmpty(t *testing.T) {
	if _, err := ParseQuestion(`{"header":"x"}`); err == nil {
		t.Fatal("没有问题正文应当报错 —— 否则界面上会出现一个空白气泡")
	}
	if _, err := ParseQuestion(`不是 JSON`); err == nil {
		t.Fatal("参数不是 JSON 应当报错")
	}
}

// ---------------------------------------------------------------------------
// ★ 追问有上限：问够了就拿掉提问工具
// ---------------------------------------------------------------------------

func TestAccountantStopsAskingAfterLimit(t *testing.T) {
	prov := &scriptProvider{steps: []*Response{
		askStep(`{"question":"还问？"}`),
		proposeStep(`{"voucher":{"word":"记","biz_date":"2025-03-11","remark":"x","entries":[]}}`),
	}}
	tools := NewToolSet(AskUserTool(), SearchAccountsTool(nil))
	a := &Accountant{
		Provider: prov, Tools: tools, MaxAsks: 2, Asked: 2, // 已经问满两轮
	}
	if _, _, err := a.Reply(context.Background(), []Message{
		{Role: "system", Content: "s"}, {Role: "user", Content: "u"},
	}); err != nil {
		t.Fatal(err)
	}
	// 问满之后，模型即便还想问也问不出来了 —— 它拿到的是**
	// 没有 ask_user 的工具表**，只能给凭证或凭空编。
	// 这里断言的是「拿掉工具」这一步本身（工具表不经过 Messages，
	// 从剧本里看不到它，所以直接测 withoutAsk）。
	full := NewToolSet(AskUserTool(), SearchAccountsTool(nil))
	if _, ok := full.Get("ask_user"); !ok {
		t.Fatal("前提不成立：完整工具表里本来就没有 ask_user")
	}
	trimmed := withoutAsk(full)
	if _, ok := trimmed.Get("ask_user"); ok {
		t.Fatal("★ 问满轮数之后还留着提问工具 —— 模型会一直问下去")
	}
	if trimmed.Len() != 1 {
		t.Fatalf("剩下的工具数 = %d，期望 1", trimmed.Len())
	}
}

func TestWithoutAskKeepsOtherTools(t *testing.T) {
	ts := NewToolSet(AskUserTool(), SearchAccountsTool(nil), SearchContactsTool(nil))
	if ts.Len() != 3 {
		t.Fatalf("工具数 = %d", ts.Len())
	}
	got := withoutAsk(ts)
	if _, ok := got.Get("ask_user"); ok {
		t.Error("ask_user 应当被去掉")
	}
	if _, ok := got.Get("search_accounts"); !ok {
		t.Error("查询工具不该被去掉 —— 去掉它会计就只能瞎猜了")
	}
	if got.Len() != 2 {
		t.Errorf("剩下的工具数 = %d，期望 2", got.Len())
	}
}

// ---------------------------------------------------------------------------
// ★ 信息够了就给凭证（且不再多问一句）
// ---------------------------------------------------------------------------

func TestAccountantProposesVoucher(t *testing.T) {
	content := `{
	  "voucher": {
	    "word": "记", "biz_date": "2025-03-11", "remark": "购打印机",
	    "entries": [
	      {"summary":"购打印机","account_code":"560206","debit":300000,"credit":0},
	      {"summary":"购打印机","account_code":"1002","debit":0,"credit":300000}
	    ]
	  },
	  "confidence": 0.9, "reasoning": "办公费，银行付款", "evidence": [], "warnings": []
	}`
	prov := &scriptProvider{steps: []*Response{proposeStep(content)}}
	a := &Accountant{Provider: prov, Tools: NewToolSet(AskUserTool())}

	reply, _, err := a.Reply(context.Background(), []Message{
		{Role: "system", Content: "s"}, {Role: "user", Content: "u"},
	})
	if err != nil {
		t.Fatalf("给凭证不该报错: %v", err)
	}
	if reply.Question != nil {
		t.Error("信息够了就不该再问")
	}
	if reply.Proposal == nil {
		t.Fatal("应当解析出提议")
	}
	if len(reply.Proposal.Voucher.Entries) != 2 {
		t.Fatalf("提议里的分录不对：%+v", reply.Proposal)
	}
	if reply.Say != "办公费，银行付款" {
		t.Errorf("会计要说的话应当来自 reasoning，实际 %q", reply.Say)
	}
}

// 提议 JSON 坏掉时，错误要能看出来是「解析失败」，而不是「AI 没反应」
func TestAccountantBadProposalJSON(t *testing.T) {
	prov := &scriptProvider{steps: []*Response{proposeStep(`{ 这不是 JSON }`)}}
	a := &Accountant{Provider: prov, Tools: NewToolSet(AskUserTool())}
	_, _, err := a.Reply(context.Background(), []Message{
		{Role: "system", Content: "s"}, {Role: "user", Content: "u"},
	})
	if err == nil {
		t.Fatal("坏 JSON 应当报错")
	}
	if !strings.Contains(err.Error(), "JSON") {
		t.Errorf("错误里要提到 JSON，实际：%v", err)
	}
}

// ---------------------------------------------------------------------------
// ★ 多轮：历史必须真的传下去
// ---------------------------------------------------------------------------

func TestAccountantKeepsHistoryAcrossTurns(t *testing.T) {
	prov := &scriptProvider{steps: []*Response{
		askStep(`{"question":"金额是多少？"}`),
		proposeStep(`{"voucher":{"word":"记","biz_date":"2025-03-11","remark":"x","entries":[]},"confidence":0.8,"reasoning":"够了","evidence":[],"warnings":[]}`),
	}}
	a := &Accountant{Provider: prov, Tools: NewToolSet(AskUserTool())}
	ctx := context.Background()

	history := []Message{
		{Role: "system", Content: "你是会计"},
		{Role: "user", Content: "业务描述：买了台打印机"},
	}
	reply, history, err := a.Reply(ctx, history)
	if err != nil || reply.Question == nil {
		t.Fatalf("第一轮应当提问：%v %+v", err, reply)
	}

	// 用户回答
	history = append(history, Message{
		Role: "user", Content: QuestionMessage(reply.Question, "3000"),
	})
	reply2, _, err := a.Reply(ctx, history)
	if err != nil {
		t.Fatal(err)
	}
	if reply2.Proposal == nil {
		t.Fatal("第二轮应当给出凭证")
	}

	// 第二次请求里必须看得见第一轮的问题与回答
	second := prov.seen[1]
	var joined strings.Builder
	for _, m := range second {
		joined.WriteString(m.Content)
		joined.WriteString("\n")
	}
	all := joined.String()
	if !strings.Contains(all, "买了台打印机") {
		t.Error("★ 第二轮看不见第一轮的业务描述 —— 对话就断了")
	}
	if !strings.Contains(all, "金额是多少") {
		t.Error("★ 第二轮看不见第一轮问过什么")
	}
	if !strings.Contains(all, "3000") {
		t.Error("★ 第二轮看不见用户的回答")
	}
}

func TestQuestionMessageCarriesContext(t *testing.T) {
	q := &AccountantQuestion{Question: "付款方式是什么？"}
	got := QuestionMessage(q, "银行转账")
	if !strings.Contains(got, "付款方式是什么") {
		t.Errorf("回灌的文本要带上原问题，否则模型会把回答当成新的业务描述：%q", got)
	}
	if !strings.Contains(got, "银行转账") {
		t.Errorf("回答本身丢了：%q", got)
	}
	// 没有问题上下文时也要把「这是一句回答」标出来 ——
	// 原样回一个「现金」，模型下一轮会把它当成一句新的业务描述
	if got := QuestionMessage(nil, "现金"); !strings.Contains(got, "现金") {
		t.Errorf("回答本身丢了：%q", got)
	}
}

// ★ 结构化回答：「点的选项」与「自己打的字」必须分开。
//
// 点来的是模型自己写在选项里、有明确后果的那一条；
// 打来的是用户临时想到的，含义不同，模型该不该再确认一次也不同。
func TestAnswerMessageSeparatesSelectedFromCustom(t *testing.T) {
	q := &AccountantQuestion{
		ID: "payment", Question: "这台打印机怎么付款的？",
	}

	// 点选项
	got := AnswerMessage(q, AccountantAnswer{
		QuestionID: "payment",
		Selected:   []string{"银行转账，取得专用发票（推荐）"},
	})
	if !strings.Contains(got, "#payment") {
		t.Errorf("★ 回答要带上问题的 id，否则多问题时就配不上了：%q", got)
	}
	if !strings.Contains(got, "选中的选项：") {
		t.Errorf("点选项要说清是点来的：%q", got)
	}
	if strings.Contains(got, "用户补充") {
		t.Errorf("没打字就不该出现「用户补充」：%q", got)
	}

	// 打字
	typed := AnswerMessage(q, AccountantAnswer{QuestionID: "payment", Custom: "3000 元"})
	if !strings.Contains(typed, "用户回答：3000 元") {
		t.Errorf("打字要说清是打来的：%q", typed)
	}
	if strings.Contains(typed, "选中的选项") {
		t.Errorf("没点选项就不该出现「选中的选项」：%q", typed)
	}

	// 又点又打
	both := AnswerMessage(q, AccountantAnswer{
		Selected: []string{"银行转账"}, Custom: "3000 元，开了专票",
	})
	if !strings.Contains(both, "选中的选项：银行转账") ||
		!strings.Contains(both, "用户补充：3000 元，开了专票") {
		t.Errorf("两者都要在：%q", both)
	}

	// 什么都没说也不能回一段空文本
	if got := AnswerMessage(q, AccountantAnswer{}); got == "" {
		t.Error("空回答也要有兜底文本，否则回灌给模型的是空消息")
	}
}

// id 由模型给；缺失时补一个默认值而不是报错 ——
// 问题本身是好的、能回答的，缺的只是一个配对用的标识
func TestParseQuestionFillsMissingID(t *testing.T) {
	q, err := ParseQuestion(`{"question":"多少钱？"}`)
	if err != nil {
		t.Fatalf("缺 id 不该报错: %v", err)
	}
	if q.ID == "" {
		t.Error("缺 id 时应当补一个默认值")
	}
	// multi_select 要能解出来
	q2, err := ParseQuestion(`{"id":"dept","question":"哪些部门？","multi_select":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !q2.MultiSelect {
		t.Error("multi_select 没解析出来")
	}
}

// ---------------------------------------------------------------------------
// ★ 系统提示词：会计版必须保留全部硬边界
// ---------------------------------------------------------------------------

func TestAccountantSystemPromptKeepsHardRules(t *testing.T) {
	in := Input{Task: TaskFreeform, Text: "买打印机"}
	plain := SystemPrompt(in)
	acct := AccountantSystemPrompt(in)

	// 加了对话规则
	for _, want := range []string{"# 与用户对话", "ask_user", "（推荐）", "不许问"} {
		if !strings.Contains(acct, want) {
			t.Errorf("会计版提示词里缺少 %q", want)
		}
	}
	// 硬边界一条都不能少 —— 两套提示词各写一份的话，改一处忘一处
	for _, want := range []string{
		"只能使用下面列出的科目编码",
		"借贷必须精确相等",
		"金额一律用整数「分」",
		"分录合计必须精确等于",
		"# 输出格式",
	} {
		if !strings.Contains(acct, want) {
			t.Errorf("★ 会计版丢了硬边界 %q", want)
		}
	}
	// 单次任务那版不该被污染
	if strings.Contains(plain, "# 与用户对话") {
		t.Error("单次任务的提示词里不该出现对话规则 —— 那边没有人可以问")
	}
}
