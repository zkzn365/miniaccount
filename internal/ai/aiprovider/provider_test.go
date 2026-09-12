package aiprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/ai"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
)

// ---------------------------------------------------------------------------
// 假模型
// ---------------------------------------------------------------------------

// fakeProvider 是一个不联网的模型，按预设返回内容。
//
// 用假模型测编排层有两个好处：
//   - 不用真的跑 7B 模型，测试秒级完成
//   - 可以精确构造「模型幻觉了科目」「模型返回了小数」这类
//     真实模型偶发但难以复现的输出
type fakeProvider struct {
	content  string
	err      error
	model    string
	kind     Kind
	lastReq  Request
	calls    int
	inTok    int
	outTok   int
	onCalled func(Request)
}

func (f *fakeProvider) Name() string { return "假模型" }
func (f *fakeProvider) Model() string {
	if f.model == "" {
		return "fake-1"
	}
	return f.model
}
func (f *fakeProvider) Kind() Kind {
	if f.kind == "" {
		return KindLocal
	}
	return f.kind
}
func (f *fakeProvider) Complete(_ context.Context, req Request) (*Response, error) {
	f.calls++
	f.lastReq = req
	if f.onCalled != nil {
		f.onCalled(req)
	}
	if f.err != nil {
		return nil, f.err
	}
	return &Response{
		Content: f.content, Model: f.Model(),
		TokensIn: f.inTok, TokensOut: f.outTok, Latency: time.Millisecond,
	}, nil
}

// ---------------------------------------------------------------------------
// 上下文来源
// ---------------------------------------------------------------------------

type fakeContext struct {
	lctx     *ledger.Context
	book     BookContext
	accounts []AccountBrief
	contacts []ContactBrief
	err      error
}

func (f *fakeContext) LedgerContext(context.Context) (*ledger.Context, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.lctx, nil
}
func (f *fakeContext) BookContext(context.Context) (BookContext, error) {
	return f.book, f.err
}
func (f *fakeContext) Accounts(context.Context) ([]AccountBrief, error) {
	return f.accounts, f.err
}
func (f *fakeContext) Contacts(context.Context) ([]ContactBrief, error) {
	return f.contacts, f.err
}

// fakeRetriever 返回固定历史。
type fakeRetriever struct {
	examples []Example
	err      error
	gotText  string
	gotCP    string
	gotAmt   int64
	gotLimit int
}

func (f *fakeRetriever) Similar(_ context.Context, text, cp string,
	amt int64, limit int) ([]Example, error) {
	f.gotText, f.gotCP, f.gotAmt, f.gotLimit = text, cp, amt, limit
	return f.examples, f.err
}

// fakeAuditor 记录审计调用。
type fakeAuditor struct {
	records  []SuggestionRecord
	decided  []Decision
	decideID []int64
	nextID   int64
	err      error
}

func (f *fakeAuditor) Record(_ context.Context, r SuggestionRecord) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.records = append(f.records, r)
	f.nextID++
	return f.nextID, nil
}
func (f *fakeAuditor) Decide(_ context.Context, id int64, d Decision,
	_ *int64, _ string) error {
	f.decided = append(f.decided, d)
	f.decideID = append(f.decideID, id)
	return f.err
}

// ---------------------------------------------------------------------------
// 脚手架
// ---------------------------------------------------------------------------

func testLedgerCtx(t *testing.T) *ledger.Context {
	t.Helper()
	mk := func(code, name string, rt account.RootType, dir account.BalanceDir,
		aux ...account.AuxType) *account.Account {
		// 编码 4-2-2-2：超过 4 位就是二级
		lv := 1
		if len(code) > 4 {
			lv = 2
		}
		return &account.Account{
			Code: code, Name: name, RootType: rt, BalanceDir: dir,
			Level: lv, IsLeaf: true, IsEnabled: true, AuxTypes: aux,
		}
	}
	tree, err := account.NewTree([]*account.Account{
		mk("1002", "银行存款", account.RootAsset, account.DirDebit),
		mk("1122", "应收账款", account.RootAsset, account.DirDebit, account.AuxCustomer),
		mk("2202", "应付账款", account.RootLiability, account.DirCredit, account.AuxSupplier),
		mk("3103", "本年利润", account.RootEquity, account.DirCredit),
		mk("5602", "管理费用", account.RootExpense, account.DirDebit, account.AuxDept),
	})
	if err != nil {
		t.Fatal(err)
	}
	cal, err := period.NewCalendar(2025, 1, 2025, period.NewKey(2025, 12))
	if err != nil {
		t.Fatal(err)
	}
	return &ledger.Context{
		Accounts: tree, Periods: cal,
		ContactKinds: map[int64]string{1: "customer", 2: "supplier"},
	}
}

func testInput() Input {
	return Input{
		Task: TaskBankFlow, Text: "收到杭州某某科技有限公司货款",
		Amount: money.Money(9040000), Date: "2025-03-11", Direction: "收入",
		Counterparty: "杭州某某科技有限公司",
	}
}

const okProposal = `{"voucher":{"word":"记","biz_date":"2025-03-11",
"remark":"收回应收账款","entries":[
{"summary":"收回货款","account_code":"1002","debit":9040000,"credit":0},
{"summary":"收回货款","account_code":"1122","debit":0,"credit":9040000,"contact_id":1}]},
"confidence":0.9,"reasoning":"历史同类","evidence":[],"warnings":[]}`

func newSuggester(t *testing.T, content string) (*Suggester, *fakeProvider,
	*fakeRetriever, *fakeAuditor) {

	t.Helper()
	fp := &fakeProvider{content: content}
	fr := &fakeRetriever{}
	fa := &fakeAuditor{}
	s := &Suggester{
		Provider: fp,
		Context: &fakeContext{
			lctx: testLedgerCtx(t),
			book: BookContext{CompanyName: "测试公司", Standard: "小企业会计准则",
				TaxType: "一般纳税人", Currency: "人民币", PeriodDesc: "2025年03月"},
			accounts: []AccountBrief{
				{Code: "1002", Name: "银行存款", Direction: "借"},
				{Code: "1122", Name: "应收账款", Direction: "借", AuxTypes: []string{"客户"}},
			},
			contacts: []ContactBrief{{ID: 1, Name: "杭州某某科技有限公司", Kind: "客户"}},
		},
		Retriever: fr, Auditor: fa,
		Now: func() time.Time {
			return time.Date(2025, 3, 11, 10, 0, 0, 0, time.UTC)
		},
	}
	return s, fp, fr, fa
}

// ---------------------------------------------------------------------------
// 提示词
// ---------------------------------------------------------------------------

// ★ 科目清单必须全量进系统提示词 —— 这是消灭「科目幻觉」的关键
func TestSystemPromptContainsClosedAccountSet(t *testing.T) {
	in := testInput()
	in.Accounts = []AccountBrief{
		{Code: "1002", Name: "银行存款", Direction: "借"},
		{Code: "560206", Name: "办公费", FullName: "管理费用—办公费",
			Direction: "借", AuxTypes: []string{"部门"}},
	}
	in.Contacts = []ContactBrief{{ID: 7, Name: "张三", Kind: "股东"}}
	sys := SystemPrompt(in)

	for _, want := range []string{"1002", "560206", "必填辅助核算：部门", "7 张三", "股东"} {
		if !strings.Contains(sys, want) {
			t.Errorf("系统提示词缺少 %q", want)
		}
	}
	// 边界必须写明
	for _, want := range []string{"只能使用下面列出的科目编码", "绝不允许", "整数「分」"} {
		if !strings.Contains(sys, want) {
			t.Errorf("系统提示词缺少边界说明 %q", want)
		}
	}
}

func TestSystemPromptEmptyBook(t *testing.T) {
	sys := SystemPrompt(Input{Book: BookContext{CompanyName: "X"}})
	if !strings.Contains(sys, "科目表为空") {
		t.Error("科目表为空时必须明确告知模型不要生成分录")
	}
	if !strings.Contains(sys, "还没有往来单位档案") {
		t.Error("应说明没有往来单位")
	}
}

func TestUserPromptContainsBusinessFacts(t *testing.T) {
	in := testInput()
	in.Extra = map[string]string{"发票号码": "12345678"}
	in.Examples = []Example{{
		VoucherNo: "记-2025-01-0003", Date: "2025-01-20", Remark: "收回货款",
		Text: "收到杭州某某科技有限公司货款", Score: 0.83,
		Lines: []ExampleLine{
			{Summary: "收回货款", AccountCode: "1002", AccountName: "银行存款",
				Debit: money.Money(9040000)},
			{Summary: "收回货款", AccountCode: "1122", AccountName: "应收账款",
				Credit: money.Money(9040000), ContactName: "杭州某某科技有限公司"},
		},
	}}
	u := UserPrompt(in)

	for _, want := range []string{
		"银行流水", "2025-03-11", "9040000", "杭州某某科技有限公司",
		"收到杭州某某科技有限公司货款", "发票号码", "记-2025-01-0003", "0.83",
		"借 1002 银行存款", "贷 1122 应收账款",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("用户提示词缺少 %q\n---\n%s", want, u)
		}
	}
}

func TestUserPromptAmountIsSignedAndPositiveDisplayed(t *testing.T) {
	in := testInput()
	in.Amount = money.Money(-300000)
	u := UserPrompt(in)
	if !strings.Contains(u, "3,000.00") {
		t.Errorf("金额应展示为可读的元：\n%s", u)
	}
	if !strings.Contains(u, "300000 分") {
		t.Errorf("同时要给出「分」的整数，避免模型自己换算：\n%s", u)
	}
}

// ---------------------------------------------------------------------------
// 脱敏
// ---------------------------------------------------------------------------

func TestDefaultPrivacy(t *testing.T) {
	if p := DefaultPrivacy(KindLocal); p.MaskAccounts || p.MaskNames {
		t.Error("本地模型数据不出机器，默认不脱敏")
	}
	if p := DefaultPrivacy(KindCloud); !p.MaskAccounts {
		t.Error("云端默认应遮住账号")
	}
}

func TestMaskLongDigits(t *testing.T) {
	cases := map[string]string{
		// 纯数字账号：8 位以上整段打码
		"账号6222021234567890123": "账号*******************",
		// 统一社会信用代码：含字母的混合串也要整体打码
		"代码91330100MA2KXYZ1234": "代码*******************",
		// 日期（8 位数字）与短金额不该被遮
		"日期2025-03-11": "日期2025-03-11",
		"金额50000":      "金额50000",
		"无数字":          "无数字",
		// 短混合串不遮，避免误伤普通英文缩写
		"科目ABC123": "科目ABC123",
	}
	for in, want := range cases {
		if got := maskLongDigits(in, 8); got != want {
			t.Errorf("maskLongDigits(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestMaskChineseNames(t *testing.T) {
	got := maskChineseNames("付款人：张三丰 收款人：李四")
	if !strings.Contains(got, "张**") {
		t.Errorf("姓名应保留姓氏并打码，实际 %q", got)
	}
	// 不在关键词之后的片段不动
	if got := maskChineseNames("杭州某某科技有限公司"); got != "杭州某某科技有限公司" {
		t.Errorf("不该误伤公司名，实际 %q", got)
	}
	// 单字不构成姓名
	if got := maskChineseNames("户名：张"); got != "户名：张" {
		t.Errorf("单字不该被当成姓名，实际 %q", got)
	}
}

func TestPrivacyApply(t *testing.T) {
	p := Privacy{MaskAccounts: true, MaskNames: true}
	got := p.Apply("付款人：张三丰 账号6222021234567890123")
	if strings.Contains(got, "三丰") {
		t.Errorf("姓名未打码：%q", got)
	}
	if strings.Contains(got, "6222021234567890123") {
		t.Errorf("账号未打码：%q", got)
	}
	if got := (Privacy{}).Apply("原样"); got != "原样" {
		t.Errorf("空策略应原样返回，实际 %q", got)
	}
}

// ---------------------------------------------------------------------------
// 编排：成功路径
// ---------------------------------------------------------------------------

func TestSuggestSuccessPath(t *testing.T) {
	s, fp, fr, fa := newSuggester(t, okProposal)
	fr.examples = []Example{{VoucherNo: "记-2025-01-0003", Score: 0.8}}

	res := s.Suggest(context.Background(), testInput(), "bank_flow", nil)

	if !res.OK() {
		t.Fatalf("应当成功：%s（err=%v）", res.Summary(), res.Err)
	}
	if res.Proposal.Voucher.Remark != "收回应收账款" {
		t.Errorf("提议备注 = %s", res.Proposal.Voucher.Remark)
	}
	if res.SuggestionID != 1 {
		t.Errorf("审计 id = %d", res.SuggestionID)
	}
	// 要求了 JSON 模式、温度 0
	if !fp.lastReq.JSONMode {
		t.Error("应要求结构化输出")
	}
	if fp.lastReq.Temperature != 0 {
		t.Errorf("温度应为 0，实际 %v", fp.lastReq.Temperature)
	}
	// 历史被检索并塞进提示词
	if !strings.Contains(fp.lastReq.User, "记-2025-01-0003") {
		t.Error("历史范例应出现在用户提示词里")
	}
	if fr.gotLimit != 4 {
		t.Errorf("默认检索 4 条，实际 %d", fr.gotLimit)
	}
	if fr.gotAmt != 9040000 {
		t.Errorf("检索应带上金额，实际 %d", fr.gotAmt)
	}
	// 审计记录
	if len(fa.records) != 1 {
		t.Fatalf("应写 1 条审计，实际 %d", len(fa.records))
	}
	r := fa.records[0]
	if r.Status != StatusValid {
		t.Errorf("状态 = %s，期望 valid", r.Status)
	}
	if r.Proposed == nil || r.Checksum == "" {
		t.Error("审计应保存结构化提议与指纹")
	}
	if r.PromptDigest == "" || len(r.PromptDigest) != 64 {
		t.Errorf("提示词指纹应是 sha256，实际 %q", r.PromptDigest)
	}
	// ★ 提示词指纹不含原文
	if strings.Contains(r.PromptDigest, "杭州") {
		t.Error("审计表里不该出现提示词原文")
	}
}

// ★ 护栏不通过不是 Err —— 它是一个需要摊开给用户看的结果
func TestSuggestGuardrailFailureIsNotAnError(t *testing.T) {
	bad := strings.Replace(okProposal, `"account_code":"1002"`, `"account_code":"9999"`, 1)
	s, _, _, fa := newSuggester(t, bad)

	res := s.Suggest(context.Background(), testInput(), "bank_flow", nil)

	if res.Err != nil {
		t.Fatalf("护栏不通过不应返回 Err（界面要展示报告而不是报错）：%v", res.Err)
	}
	if res.OK() {
		t.Fatal("幻觉科目必须让 OK() 为假")
	}
	if res.Report == nil || res.Report.Passed() {
		t.Fatal("应带回未通过的报告")
	}
	if len(res.Report.Failures()) == 0 {
		t.Fatal("应至少有一项阻断")
	}
	// 界面要能直接展示失败原因
	if !strings.Contains(res.Summary(), "科目") {
		t.Errorf("结论应点明问题：%s", res.Summary())
	}
	// 审计要记为 invalid 并带原因
	if len(fa.records) != 1 || fa.records[0].Status != StatusInvalid {
		t.Fatalf("审计应记为 invalid，实际 %+v", fa.records)
	}
	if !strings.Contains(fa.records[0].RejectReason, "9999") {
		t.Errorf("失败原因应点明科目，实际 %q", fa.records[0].RejectReason)
	}
}

func TestSuggestMalformedJSON(t *testing.T) {
	s, _, _, fa := newSuggester(t, "我觉得应该借银行存款，贷应收账款。")

	res := s.Suggest(context.Background(), testInput(), "bank_flow", nil)
	if res.Err == nil {
		t.Fatal("自由文本应被拒绝")
	}
	if !errors.Is(res.Err, ai.ErrMalformedJSON) {
		t.Errorf("错误应可识别为格式错误，实际 %v", res.Err)
	}
	if len(fa.records) != 1 || fa.records[0].Status != StatusInvalid {
		t.Fatal("格式错误也要留痕")
	}
	if fa.records[0].RawResponse == "" {
		t.Error("应保存模型原话，便于排错")
	}
}

// ★ 模型连不上也要留痕：否则「这个服务经常挂」永远发现不了
func TestSuggestProviderErrorIsRecorded(t *testing.T) {
	s, fp, _, fa := newSuggester(t, "")
	fp.err = fmt.Errorf("%w: 连接被拒绝", ErrUpstreamFail)

	res := s.Suggest(context.Background(), testInput(), "bank_flow", nil)
	if res.Err == nil {
		t.Fatal("应返回错误")
	}
	if !errors.Is(res.Err, ErrUpstreamFail) {
		t.Errorf("错误应可识别，实际 %v", res.Err)
	}
	if len(fa.records) != 1 {
		t.Fatalf("调用失败也要写审计，实际 %d 条", len(fa.records))
	}
	if fa.records[0].Status != StatusInvalid {
		t.Error("应记为 invalid")
	}
}

// 检索失败不应阻断：历史只是加分项
func TestSuggestSurvivesRetrieverFailure(t *testing.T) {
	s, fp, fr, _ := newSuggester(t, okProposal)
	fr.err = errors.New("检索炸了")

	res := s.Suggest(context.Background(), testInput(), "bank_flow", nil)
	if !res.OK() {
		t.Fatalf("检索失败不该阻断建议：%v", res.Err)
	}
	if strings.Contains(fp.lastReq.User, "历史同类凭证") {
		t.Error("检索失败时不该出现历史段落")
	}
}

func TestSuggestRequiresProvider(t *testing.T) {
	s := &Suggester{}
	res := s.Suggest(context.Background(), testInput(), "bank_flow", nil)
	if !errors.Is(res.Err, ErrNoProvider) {
		t.Fatalf("缺少 Provider 应报 ErrNoProvider，实际 %v", res.Err)
	}
}

func TestSuggestRequiresContext(t *testing.T) {
	s := &Suggester{Provider: &fakeProvider{content: okProposal}}
	res := s.Suggest(context.Background(), testInput(), "bank_flow", nil)
	if res.Err == nil {
		t.Fatal("缺少上下文来源应报错")
	}
}

// 审计写失败不该让用户记不了账
func TestSuggestSurvivesAuditFailure(t *testing.T) {
	s, _, _, fa := newSuggester(t, okProposal)
	fa.err = errors.New("审计表写不进去")

	res := s.Suggest(context.Background(), testInput(), "bank_flow", nil)
	if !res.OK() {
		t.Fatalf("审计失败不该阻断主流程：%v", res.Err)
	}
	if res.SuggestionID != 0 {
		t.Errorf("审计失败时 id 应为 0，实际 %d", res.SuggestionID)
	}
}

// ---------------------------------------------------------------------------
// 编排：采纳与拒绝
// ---------------------------------------------------------------------------

func TestAcceptCreatesDraft(t *testing.T) {
	s, _, _, fa := newSuggester(t, okProposal)
	res := s.Suggest(context.Background(), testInput(), "bank_flow", nil)

	v, err := s.Accept(context.Background(), res, "李会计", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != voucher.StatusDraft {
		t.Errorf("AI 产物一律先是草稿，实际 %s", v.Status)
	}
	if v.Source != voucher.SourceAI || !v.CreatedByAI {
		t.Error("应标记来源与 AI 标记")
	}
	// ★ 制单人签的是自然人，不是模型
	if v.CreatedBy != "李会计" {
		t.Errorf("制单人 = %s，期望自然人「李会计」", v.CreatedBy)
	}
	if v.TotalDebit() != money.Money(9040000) {
		t.Errorf("金额 = %s", v.TotalDebit())
	}
	if len(fa.decided) != 1 || fa.decided[0] != DecisionAccepted {
		t.Errorf("应记录采纳，实际 %v", fa.decided)
	}
	if fa.decideID[0] != res.SuggestionID {
		t.Error("处置应记在正确的建议 id 上")
	}
}

// ★ 护栏没过的提议绝不能落成凭证
func TestAcceptRefusesInvalidProposal(t *testing.T) {
	bad := strings.Replace(okProposal, `"account_code":"1002"`, `"account_code":"9999"`, 1)
	s, _, _, _ := newSuggester(t, bad)
	res := s.Suggest(context.Background(), testInput(), "bank_flow", nil)

	if _, err := s.Accept(context.Background(), res, "李会计", nil); err == nil {
		t.Fatal("未通过护栏的提议不得落地")
	}
}

func TestAcceptRequiresMaker(t *testing.T) {
	s, _, _, _ := newSuggester(t, okProposal)
	res := s.Suggest(context.Background(), testInput(), "bank_flow", nil)
	if _, err := s.Accept(context.Background(), res, "", nil); err == nil {
		t.Fatal("缺少确认人应报错")
	}
}

func TestAcceptNilResult(t *testing.T) {
	s, _, _, _ := newSuggester(t, okProposal)
	if _, err := s.Accept(context.Background(), nil, "李会计", nil); err == nil {
		t.Fatal("空结果应报错")
	}
}

func TestRejectRecordsDecision(t *testing.T) {
	s, _, _, fa := newSuggester(t, okProposal)
	res := s.Suggest(context.Background(), testInput(), "bank_flow", nil)

	if err := s.Reject(context.Background(), res, "摘要看不清"); err != nil {
		t.Fatal(err)
	}
	if len(fa.decided) != 1 || fa.decided[0] != DecisionRejected {
		t.Errorf("应记录拒绝，实际 %v", fa.decided)
	}
}

// ---------------------------------------------------------------------------
// HTTP 客户端
// ---------------------------------------------------------------------------

func TestOpenAICompatHappyPath(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"model":"qwen2.5:7b","choices":[
			{"message":{"role":"assistant","content":"{\"ok\":1}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":120,"completion_tokens":45}}`))
	}))
	defer srv.Close()

	c, err := NewOpenAICompat(Options{
		Name: "本地", Model: "qwen2.5:7b", BaseURL: srv.URL + "/v1", APIKey: "sk-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Complete(context.Background(), Request{
		System: "sys", User: "usr", JSONMode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != `{"ok":1}` {
		t.Errorf("内容 = %q", resp.Content)
	}
	if resp.TokensIn != 120 || resp.TokensOut != 45 {
		t.Errorf("token 统计 = %d/%d", resp.TokensIn, resp.TokensOut)
	}
	if resp.Model != "qwen2.5:7b" {
		t.Errorf("模型 = %s（应取服务端返回的）", resp.Model)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("路径 = %s", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("鉴权头 = %s", gotAuth)
	}
	// 两种 JSON 模式写法同时给出
	if gotBody["format"] != "json" {
		t.Error("应带 Ollama 风格的 format")
	}
	if rf, ok := gotBody["response_format"].(map[string]any); !ok ||
		rf["type"] != "json_object" {
		t.Error("应带 OpenAI 风格的 response_format")
	}
	if gotBody["stream"] != false {
		t.Error("应为非流式")
	}
}

func TestOpenAICompatUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key","type":"auth"}}`))
	}))
	defer srv.Close()

	c, _ := NewOpenAICompat(Options{Model: "m", BaseURL: srv.URL + "/v1"})
	_, err := c.Complete(context.Background(), Request{User: "x"})
	if !errors.Is(err, ErrUpstreamFail) {
		t.Fatalf("应报 ErrUpstreamFail，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("应透出服务端的错误信息，实际 %v", err)
	}
}

func TestOpenAICompatNonJSONError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	defer srv.Close()

	c, _ := NewOpenAICompat(Options{Model: "m", BaseURL: srv.URL + "/v1"})
	_, err := c.Complete(context.Background(), Request{User: "x"})
	// 非 JSON 的错误页走 ErrBadResponse 分支，但仍要带上状态码
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("错误信息应含状态码，实际 %v", err)
	}
}

func TestOpenAICompatContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := NewOpenAICompat(Options{Model: "m", BaseURL: srv.URL + "/v1"})
	_, err := c.Complete(ctx, Request{User: "x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应尊重取消，实际 %v", err)
	}
}

func TestNewOpenAICompatValidation(t *testing.T) {
	if _, err := NewOpenAICompat(Options{Model: "m"}); err == nil {
		t.Error("缺 BaseURL 应报错")
	}
	if _, err := NewOpenAICompat(Options{BaseURL: "http://x"}); err == nil {
		t.Error("缺 Model 应报错")
	}
}

// ★ 指向本机的一律视为本地 —— 猜错的代价不对称
func TestInferKind(t *testing.T) {
	local := []string{
		"http://127.0.0.1:11434/v1", "http://localhost:8080/v1",
		"http://0.0.0.0:1234/v1", "http://[::1]:11434/v1",
	}
	for _, u := range local {
		if k := inferKind(u); !k.IsLocal() {
			t.Errorf("%s 应判为本地，实际 %s", u, k)
		}
	}
	for _, u := range []string{
		"https://api.deepseek.com/v1", "https://dashscope.aliyuncs.com/v1",
	} {
		if k := inferKind(u); k.IsLocal() {
			t.Errorf("%s 应判为云端，实际 %s", u, k)
		}
	}
}

func TestDisabledProvider(t *testing.T) {
	d := Disabled{Reason: "用户关闭了 AI"}
	_, err := d.Complete(context.Background(), Request{})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("应报 ErrDisabled，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "用户关闭了 AI") {
		t.Errorf("应带上原因，实际 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 草稿
// ---------------------------------------------------------------------------

func TestDraftRequiresPassedReport(t *testing.T) {
	// Draft 本身不做护栏校验，但 Accept 会先查报告 —— 这里测 Accept 之外的直调
	p, err := ai.Parse(okProposal)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Draft(p, "李会计", &ai.AIProvenance{Provider: "假模型", Layer: ai.LayerAI})
	if err != nil {
		t.Fatal(err)
	}
	if v.Remark != "收回应收账款" {
		t.Errorf("备注 = %s", v.Remark)
	}
	if !v.CreatedByAI {
		t.Error("应标记 AI 来源")
	}
}
