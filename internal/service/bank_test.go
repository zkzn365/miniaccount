package service_test

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/domain/period"
	"miniaccount/internal/service"
)

// importTwoFlows 导入两条流水，返回它们的 id。
func importTwoFlows(t *testing.T, svc *service.Service) []int64 {
	t.Helper()
	csv := "\ufeff交易日期,摘要,对方户名,收入金额,支出金额,余额\n" +
		"2025-01-10,收到货款,杭州某某科技有限公司,106000.00,,106000.00\n" +
		"2025-01-15,支付水电费,杭州供电公司,,860.00,105140.00\n"
	if _, err := svc.ImportBankStatement(context.Background(), service.BankImportInput{
		AccountCode: "1002", FileName: "s.csv",
		Data: []byte(csv), ImportedBy: "李会计",
	}); err != nil {
		t.Fatal(err)
	}
	flows, err := svc.BankFlows(context.Background(), service.BankFlowQuery{})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, len(flows))
	for _, f := range flows {
		ids = append(ids, f.ID)
	}
	return ids
}

// ★ 三层匹配再全也总会有匹配不上的流水。没有人工指定这个入口，
// 它们就只能在界面上「忽略」—— 而忽略意味着这笔钱永远进不了账。
func TestSetBankSuggestionManual(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	dept := mustDepartment(t, svc, "管理部门")
	ids := importTwoFlows(t, svc)
	if len(ids) != 2 {
		t.Fatalf("导入条数 = %d", len(ids))
	}

	// 匹配一轮：没有规则，应该一条都匹配不上
	mres, err := svc.MatchBankFlows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mres.Matched != 0 {
		t.Fatalf("没有规则时不该匹配上，实际 %d 条", mres.Matched)
	}

	// 人工指定：水电费 → 管理费用—水电费（要求部门）
	v, err := svc.SetBankSuggestion(ctx, ids[1], service.BankSuggestionInput{
		CounterAccountCode: "560209", DeptID: &dept,
	})
	if err != nil {
		t.Fatalf("人工指定失败: %v", err)
	}
	if v.Status != "matched" {
		t.Errorf("状态 = %s，期望 matched", v.Status)
	}
	if v.CounterAccount != "560209" {
		t.Errorf("对方科目 = %s", v.CounterAccount)
	}
	// ★ 标成「人工」，复盘时能区分哪些是软件猜的、哪些是人定的
	if v.MatchLayerLabel != "人工" {
		t.Errorf("匹配来源 = %q，期望「人工」", v.MatchLayerLabel)
	}
	if v.Confidence != 1 {
		t.Errorf("人工指定的置信度应为 1，实际 %v", v.Confidence)
	}
	// 摘要默认取流水摘要
	if !strings.Contains(v.Memo, "水电费") {
		t.Errorf("摘要 = %q，应含流水摘要", v.Memo)
	}

	// 人工指定后应该能生成凭证
	res, err := svc.PostBankFlows(ctx, []int64{ids[1]}, "王主管")
	if err != nil {
		t.Fatalf("生成凭证失败: %v", err)
	}
	if res.Created != 1 {
		t.Fatalf("生成凭证 %d 张，期望 1；失败：%v", res.Created, res.Failures)
	}

	// 试算要平衡
	rep, _ := svc.DB().Reports().TrialBalanceReport(ctx, period.NewKey(2025, 1))
	// ★ 用 rep.Totals()，不要自己遍历行求和：
	// 汇总科目行带的是其下级的合计，全加一遍会把同一笔钱算两遍。
	_, _, d, c, _, _ := rep.Totals()
	if d != c {
		t.Errorf("人工指定生成的凭证导致试算不平衡：借 %s ≠ 贷 %s", d, c)
	}
}

func TestSetBankSuggestionValidation(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	dept := mustDepartment(t, svc, "管理部门")
	ids := importTwoFlows(t, svc)

	// 空科目
	if _, err := svc.SetBankSuggestion(ctx, ids[0], service.BankSuggestionInput{}); err == nil {
		t.Error("空科目应被拒绝")
	}
	// 不存在的科目
	if _, err := svc.SetBankSuggestion(ctx, ids[0], service.BankSuggestionInput{
		CounterAccountCode: "9999",
	}); err == nil {
		t.Error("不存在的科目应被拒绝")
	}
	// 汇总科目不能记账
	if _, err := svc.SetBankSuggestion(ctx, ids[0], service.BankSuggestionInput{
		CounterAccountCode: "2211",
	}); err == nil {
		t.Fatal("汇总科目应被拒绝")
	}
	// ★ 错误要在**指定时**就出现，而不是等生成凭证时才发现 ——
	// 那时用户面对的是一批操作，不知道是哪一条出了问题
	if _, err := svc.SetBankSuggestion(ctx, ids[0], service.BankSuggestionInput{
		CounterAccountCode: "560209", // 要求部门
	}); err == nil {
		t.Error("科目要求部门时应立即报错，而不是等生成凭证时")
	}
	// 补上部门就好了
	if _, err := svc.SetBankSuggestion(ctx, ids[0], service.BankSuggestionInput{
		CounterAccountCode: "560209", DeptID: &dept,
	}); err != nil {
		t.Fatalf("补上部门后应成功: %v", err)
	}
	// 不存在的流水
	if _, err := svc.SetBankSuggestion(ctx, 999999, service.BankSuggestionInput{
		CounterAccountCode: "1002",
	}); err == nil {
		t.Error("不存在的流水应报错")
	}
}

// 已生成凭证的流水不能再改
func TestSetBankSuggestionAfterPost(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	cust := mustContact(t, svc, "customer", "杭州某某科技有限公司")
	ids := importTwoFlows(t, svc)

	// 收款 → 预收账款（要求往来单位）
	if _, err := svc.SetBankSuggestion(ctx, ids[0], service.BankSuggestionInput{
		CounterAccountCode: "2203", ContactID: &cust,
	}); err != nil {
		t.Fatal(err)
	}
	// ★ 批量生成凭证把逐条的失败放进 Failures 而不是返回 error ——
	// 这是对的（一批里部分成功是常态），但调用方**必须**检查 Created，
	// 否则「全军覆没」会被当成成功。
	pres, err := svc.PostBankFlows(ctx, []int64{ids[0]}, "王主管")
	if err != nil {
		t.Fatal(err)
	}
	if pres.Created != 1 {
		t.Fatalf("生成凭证 %d 张，期望 1；失败原因：%v", pres.Created, pres.Failures)
	}
	if _, err := svc.SetBankSuggestion(ctx, ids[0], service.BankSuggestionInput{
		CounterAccountCode: "2203", ContactID: &cust,
	}); err == nil {
		t.Error("已生成凭证的流水不该还能改")
	}
}
