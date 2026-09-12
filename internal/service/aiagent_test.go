package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/ai/aiprovider"
	"miniaccount/internal/service"
)

// fakeAI 是一个不联网的假模型：按提示词里出现的科目编码回一段合法 JSON。
//
// ★ 批量记账的进度、取消、逐条记录、失败不中断这些逻辑与模型无关，
// 却最容易错。用假模型就能把它们全跑一遍。
type fakeAI struct {
	// reply 是要返回的内容；为空时按 prompt 里的科目自动编一条。
	reply string
	// failOn 命中关键词时返回错误（用来测「一条失败不中断整批」）。
	failOn string
	calls  int
}

func (f *fakeAI) Name() string          { return "假模型" }
func (f *fakeAI) Model() string         { return "fake-1" }
func (f *fakeAI) Kind() aiprovider.Kind { return aiprovider.KindLocal }

func (f *fakeAI) Complete(_ context.Context, req aiprovider.Request) (*aiprovider.Response, error) {
	f.calls++
	text := req.User + req.System
	for _, m := range req.Messages {
		text += m.Content
	}
	if f.failOn != "" && strings.Contains(text, f.failOn) {
		return nil, context.DeadlineExceeded
	}
	body := f.reply
	if body == "" {
		body = `{"voucher":{"word":"记","biz_date":"2025-01-15","remark":"测试记账",
		  "entries":[
		    {"summary":"测试记账","account_code":"1002","debit":100000,"credit":0},
		    {"summary":"测试记账","account_code":"5001","debit":0,"credit":100000}
		  ]},"confidence":0.9,"reasoning":"测试","evidence":[],"warnings":[]}`
	}
	return &aiprovider.Response{
		Content: body, Model: "fake-1", TokensIn: 100, TokensOut: 50,
	}, nil
}

// withFakeAI 注入假模型，返回那个假模型以便断言调用次数。
func withFakeAI(t *testing.T, f *fakeAI) {
	t.Helper()
	service.SetAIProviderFactory(func(context.Context, *service.Service) (aiprovider.Provider, error) {
		return f, nil
	})
	t.Cleanup(func() { service.SetAIProviderFactory(nil) })
}

// 造几条待记账的银行流水。
func seedBankFlows(t *testing.T, svc *service.Service, n int) {
	t.Helper()
	ctx := context.Background()
	// 银行账户要先存在
	id, err := svc.SaveContact(ctx, service.ContactInput{
		Kind: "customer", Name: "杭州云帆科技有限公司", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = id
	csv := "交易日期,摘要,对方户名,收入金额,支出金额,流水号\n"
	for i := 0; i < n; i++ {
		csv += "2025-01-15,收到货款,杭州云帆科技有限公司,1000.00,,SN000" +
			string(rune('1'+i)) + "\n"
	}
	if _, err := svc.ImportBankStatement(ctx, service.BankImportInput{
		AccountCode: "1002", FileName: "test.csv",
		Data: []byte(csv), ImportedBy: "李会计",
	}); err != nil {
		t.Fatalf("导入流水失败: %v", err)
	}
}

// ★ 批量记账：把待处理的一批单据跑一遍，逐条给结果。
func TestAIAgentRunBatch(t *testing.T) {
	svc := withAudit(t)
	fake := &fakeAI{}
	withFakeAI(t, fake)
	seedBankFlows(t, svc, 3)

	ctx := context.Background()
	run, err := svc.StartAIAgentRun(ctx, service.AIAgentRequest{
		Source: service.AgentSourceBankFlows, Limit: 5,
	})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if run.Total != 3 {
		t.Fatalf("待处理条数 = %d，期望 3", run.Total)
	}
	if run.State != "running" {
		t.Errorf("刚启动应当是 running，实际 %s", run.State)
	}

	final := waitRun(t, svc, run.ID)
	if final.State != "done" {
		t.Fatalf("跑完应当是 done，实际 %s（%s）", final.State, final.Error)
	}
	if final.Done != 3 || final.OKCount != 3 {
		t.Errorf("完成 %d 条、通过 %d 条，期望 3/3", final.Done, final.OKCount)
	}
	if fake.calls != 3 {
		t.Errorf("模型被调用了 %d 次，期望 3 次（每条一次）", fake.calls)
	}
	if final.TokensIn != 300 {
		t.Errorf("token 统计 = %d，期望 300", final.TokensIn)
	}
	for i, it := range final.Items {
		if !it.OK || it.SuggestionID == 0 {
			t.Errorf("第 %d 条没有拿到可采纳的建议：%+v", i, it)
		}
		if it.Label == "" {
			t.Errorf("第 %d 条没有人话标识 —— 界面上一堆「#12」是没法核对的", i)
		}
		if it.Voucher == nil || len(it.Voucher.Entries) != 2 {
			t.Errorf("第 %d 条没有建议凭证：%+v", i, it.Voucher)
		}
	}
}

// ★ 单条失败不能让整批停掉 —— 十条里有一条读不出来就放弃后九条，
// 等于让用户白等。
func TestAIAgentRunContinuesOnItemFailure(t *testing.T) {
	svc := withAudit(t)
	fake := &fakeAI{failOn: "收到货款"} // 每条都失败
	withFakeAI(t, fake)
	seedBankFlows(t, svc, 3)

	ctx := context.Background()
	run, err := svc.StartAIAgentRun(ctx, service.AIAgentRequest{
		Source: service.AgentSourceBankFlows, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitRun(t, svc, run.ID)
	if final.State != "done" {
		t.Errorf("个条失败不该把整批标成失败，实际 %s", final.State)
	}
	if final.Done != 3 {
		t.Errorf("应当把 3 条都跑完（各自失败），实际 %d", final.Done)
	}
	if final.OKCount != 0 {
		t.Errorf("都失败了不该有通过的，实际 %d", final.OKCount)
	}
	for _, it := range final.Items {
		if it.Error == "" {
			t.Errorf("失败的那条要说明原因：%+v", it)
		}
	}
}

// ★ 采纳批量结果里的一条：落草稿凭证、写审计。
func TestAIAgentAcceptItem(t *testing.T) {
	svc := withAudit(t)
	withFakeAI(t, &fakeAI{})
	seedBankFlows(t, svc, 2)

	ctx := context.Background()
	run, err := svc.StartAIAgentRun(ctx, service.AIAgentRequest{
		Source: service.AgentSourceBankFlows, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitRun(t, svc, run.ID)

	res, err := svc.AcceptAIAgentItem(ctx, run.ID, 0, "王主管")
	if err != nil {
		t.Fatalf("采纳失败: %v", err)
	}
	d, err := svc.Voucher(ctx, res.VoucherID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "draft" {
		t.Errorf("★ 采纳后应当是草稿，实际 %s", d.Status)
	}

	// 处置要能在进度里看到（界面刷新后不会又显示成「待处理」）
	after, err := svc.AIAgentRunStatus(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Items[0].Decision != "accepted" {
		t.Errorf("第 0 条的处置 = %q，期望 accepted", after.Items[0].Decision)
	}
	if after.Items[1].Decision != "" {
		t.Errorf("第 1 条没被处理，不该有处置：%q", after.Items[1].Decision)
	}
	_ = final
}

// 拒绝一条。
func TestAIAgentRejectItem(t *testing.T) {
	svc := withAudit(t)
	withFakeAI(t, &fakeAI{})
	seedBankFlows(t, svc, 1)

	ctx := context.Background()
	run, err := svc.StartAIAgentRun(ctx, service.AIAgentRequest{
		Source: service.AgentSourceBankFlows, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, svc, run.ID)
	if err := svc.RejectAIAgentItem(ctx, run.ID, 0, "科目选错了"); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	after, _ := svc.AIAgentRunStatus(run.ID)
	if after.Items[0].Decision != "rejected" {
		t.Errorf("处置 = %q，期望 rejected", after.Items[0].Decision)
	}
}

// 参数校验：来源必须合法、条数有上限。
func TestAIAgentRunValidates(t *testing.T) {
	svc := withAudit(t)
	withFakeAI(t, &fakeAI{})
	ctx := context.Background()

	if _, err := svc.StartAIAgentRun(ctx, service.AIAgentRequest{
		Source: "不存在的来源",
	}); err == nil {
		t.Error("未知来源应当被拒绝")
	}
	if _, err := svc.StartAIAgentRun(ctx, service.AIAgentRequest{
		Source: service.AgentSourceBankFlows, Limit: 999,
	}); err == nil {
		t.Error("★ 超过上限应当被拒绝 —— 每条都是一次计费调用")
	} else if !strings.Contains(err.Error(), "最多") {
		t.Errorf("拒绝的理由要讲清楚：%v", err)
	}
}

// ★ 没配模型时要**直接说清楚**，而不是跑出一条条「生成失败」。
func TestAIAgentRunWithoutProvider(t *testing.T) {
	svc := withAudit(t)
	// 不注入假模型：库里也没有配任何服务
	service.SetAIProviderFactory(nil)
	ctx := context.Background()

	_, err := svc.StartAIAgentRun(ctx, service.AIAgentRequest{
		Source: service.AgentSourceBankFlows, Limit: 1,
	})
	if err == nil {
		t.Fatal("没配模型时应当直接报错")
	}
	if !strings.Contains(err.Error(), "模型服务") {
		t.Errorf("错误信息要指向「去配置模型服务」：%v", err)
	}
}

// 没有待处理单据时：跑一轮空批次，不报错。
func TestAIAgentRunEmptyQueue(t *testing.T) {
	svc := withAudit(t)
	withFakeAI(t, &fakeAI{})
	ctx := context.Background()
	run, err := svc.StartAIAgentRun(ctx, service.AIAgentRequest{
		Source: service.AgentSourceBankFlows, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitRun(t, svc, run.ID)
	if final.Total != 0 || final.State != "done" {
		t.Errorf("空队列应当是 0 条、done，实际 %d 条 / %s", final.Total, final.State)
	}
}

// waitRun 等一轮跑完（最多 5 秒）。
func waitRun(t *testing.T, svc *service.Service, id string) *service.AIAgentRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := svc.AIAgentRunStatus(id)
		if err != nil {
			t.Fatalf("查进度失败: %v", err)
		}
		if run.State != "running" {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("批量记账跑超时了")
	return nil
}
