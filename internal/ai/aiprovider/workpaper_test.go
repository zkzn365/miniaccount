package aiprovider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
)

// ---------------------------------------------------------------------------
// 审计底稿工具
// ---------------------------------------------------------------------------
//
// 两条边界要焊死：
//   1. 读底稿是**只读**的（不许 Terminal）；
//   2. 提议登记调整是**终止型**的（模型只能提议，用户按确认才落库）。

type fakeWorkpaper struct{ brief *WorkpaperBrief }

func (f fakeWorkpaper) Workpaper(_ context.Context, _ PeriodKey) (*WorkpaperBrief, error) {
	return f.brief, nil
}

func TestGetWorkpaperToolIsReadOnly(t *testing.T) {
	ts := NewToolSet(GetWorkpaperTool(fakeWorkpaper{}))
	tool, ok := ts.Get("get_workpaper")
	if !ok {
		t.Fatal("工具集里应当有 get_workpaper")
	}
	if tool.Terminal {
		t.Error("★ 读底稿是只读的，不能是终止型 —— 终止型是留给「要用户确认」的写操作")
	}
	if tool.RenderAsk != nil {
		t.Error("只读工具不该有 RenderAsk（它不提问、不提议）")
	}
}

func TestProposeAdjustmentToolIsTerminal(t *testing.T) {
	ts := NewToolSet(ProposeAdjustmentTool())
	tool, ok := ts.Get("propose_adjustment")
	if !ok {
		t.Fatal("工具集里应当有 propose_adjustment")
	}
	if !tool.Terminal {
		t.Fatal("★ 提议登记审计调整必须由用户确认，因此必须是终止型")
	}
	if tool.RenderAsk == nil {
		t.Error("终止型工具要有 RenderAsk：它结束这一轮，得留一句普通文本给历史")
	}
	// 参数里必须点名「分」而不是「元」：让模型做小数运算，
	// 迟早会出现 3000 与 30.00 混着来的分录
	if !strings.Contains(string(tool.Parameters), "金额（分）") {
		t.Error("金额参数必须写明单位是分（与凭证契约一致）")
	}
}

func TestRenderWorkpaperWithAndWithoutMateriality(t *testing.T) {
	full := &WorkpaperBrief{
		Period: "2026-09",
		Materiality: &MaterialityBrief{
			Benchmark: "资产总额", BenchmarkAmount: 100_000_000,
			Overall: 500_000, Performance: 300_000, Trivial: 25_000,
			Note: "小型企业，以资产总额为基准",
		},
		Adjustments: []AdjustmentBrief{{
			Code: "ADJ-202609-001", Kind: "调整", Summary: "补提折旧",
			Reason: "折旧计算表少提", Evidence: "F-3", Amount: 300_000,
			Booked: true,
		}},
		MisstatementTotal: 300_000, MisstatementCount: 1,
		Concludes: "未更正错报合计 3,000.00，低于实际执行重要性 3,000.00。",
	}
	text := renderWorkpaper(full)
	for _, want := range []string{
		"2026-09 审计底稿", "整体重要性", "实际执行重要性", "明显微小错报临界值",
		"资产总额", "未更正错报", "补提折旧", "F-3",
		// ★ 状态必须说清「生成过凭证」与「已入账」的区别：
		// 模型若把「已生成凭证」当成「已入账」，它就会告诉用户
		// 「这笔已经改到账上了」，而实际上还躺在草稿里
		"已生成凭证（草稿，待账期结算过账）",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("底稿文本里缺少 %q，实际：\n%s", want, text)
		}
	}

	// 没有重要性水平时，必须让模型**去提醒用户**，
	// 而不是自己编一个门槛继续算
	empty := &WorkpaperBrief{Period: "2026-09"}
	text = renderWorkpaper(empty)
	if !strings.Contains(text, "尚未确定") {
		t.Errorf("没有门槛时应当明说，实际：\n%s", text)
	}
	if !strings.Contains(text, "不要在对话里替他编一个数") {
		t.Errorf("应当明确禁止模型自己编门槛，实际：\n%s", text)
	}
}

func TestWorkpaperToolParsesPeriod(t *testing.T) {
	tool := GetWorkpaperTool(fakeWorkpaper{&WorkpaperBrief{Period: "2026-09"}})
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"period":"九月"}`)); err == nil {
		t.Error("读不出来的期间应当报错，而不是去猜")
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"period":""}`)); err == nil {
		t.Error("没给期间应当报错")
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"period":"2026-09"}`)); err != nil {
		t.Errorf("正常期间不该报错：%v", err)
	}
}

// ---------------------------------------------------------------------------
// 调整提议
// ---------------------------------------------------------------------------

func adjProposalJSON(t *testing.T, body string) string {
	t.Helper()
	return `{
		"period": "2026-09",
		"kind": "adjust",
		"summary": "补提 2026 年折旧",
		"reason": "折旧计算表显示少提 3,000.00",
		"evidence": "折旧计算表（F-3）",
		` + body + `
	}`
}

func TestParseAdjustmentProposal(t *testing.T) {
	raw := adjProposalJSON(t, `"lines": [
		{"account_code": "560205", "summary": "补提折旧", "debit": 300000, "dept_id": 7},
		{"account_code": "1602", "summary": "补提折旧", "credit": 300000}
	]`)
	p, err := ParseAdjustmentProposal(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if p.TotalDebit() != money.Money(300000) || !p.Balanced() {
		t.Errorf("金额或平衡判断不对：借 %s 贷 %s", p.TotalDebit(), p.TotalCredit())
	}
	if p.KindLabel() != "调整" {
		t.Errorf("种类文字 = %q", p.KindLabel())
	}
	// ★ 辅助核算 id 的标签必须是 snake_case：这里曾经写成 camelCase
	// （deptId），于是模型按契约传的 dept_id 一个都没解析出来，
	// 分录到了护栏那里就是「缺少必需的辅助核算」——
	// 而模型明明填了。字段名跨语言走私，只有测试能焊住。
	if p.Lines[0].DeptID == nil || *p.Lines[0].DeptID != 7 {
		t.Fatalf("dept_id 没解析出来：%+v", p.Lines[0])
	}
}

func TestAdjustmentProposalValidate(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"借贷不平", `"lines": [
			{"account_code": "560205", "debit": 300000},
			{"account_code": "1602", "credit": 299900}]`, "借贷不平衡"},
		{"只有一行", `"lines": [{"account_code": "560205", "debit": 300000}]`, "至少需要两行"},
		{"没有科目", `"lines": [
			{"account_code": "", "debit": 300000},
			{"account_code": "1602", "credit": 300000}]`, "没有科目"},
		{"负数金额", `"lines": [
			{"account_code": "560205", "debit": -300000},
			{"account_code": "1602", "credit": -300000}]`, "负数"},
		{"同一行借贷都有", `"lines": [
			{"account_code": "560205", "debit": 300000, "credit": 1},
			{"account_code": "1602", "credit": 300000}]`, "借贷都有金额"},
		{"零金额", `"lines": [
			{"account_code": "560205", "debit": 0},
			{"account_code": "1602", "credit": 0}]`, "没有金额"},
		{"没有摘要", `"summary": "", "lines": [
			{"account_code": "560205", "debit": 300000},
			{"account_code": "1602", "credit": 300000}]`, "摘要"},
		{"没有依据", `"reason": "", "lines": [
			{"account_code": "560205", "debit": 300000},
			{"account_code": "1602", "credit": 300000}]`, "依据"},
		{"期间读不出来", `"period": "九月", "lines": [
			{"account_code": "560205", "debit": 300000},
			{"account_code": "1602", "credit": 300000}]`, "期间"},
		{"种类不认识", `"kind": "closing", "lines": [
			{"account_code": "560205", "debit": 300000},
			{"account_code": "1602", "credit": 300000}]`, "种类"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseAdjustmentProposal(adjProposalJSON(t, c.body))
			if err == nil {
				t.Fatal("应当被拦下")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("报错应当提到 %q，实际：%v", c.want, err)
			}
		})
	}

	// 基准用例必须能过 —— 否则上面每一条都可能是因为别的原因红的
	if _, err := ParseAdjustmentProposal(adjProposalJSON(t, `"lines": [
		{"account_code": "560205", "debit": 300000},
		{"account_code": "1602", "credit": 300000}]`)); err != nil {
		t.Fatalf("基准用例应当能解析：%v", err)
	}
	// 重分类也合法
	if _, err := ParseAdjustmentProposal(adjProposalJSON(t, `"kind": "reclass", "lines": [
		{"account_code": "1122", "debit": 300000},
		{"account_code": "122103", "credit": 300000}]`)); err != nil {
		t.Errorf("重分类应当能解析：%v", err)
	}
}

// 模型给的 kind 缺省时按「调整」处理，而不是报错：
// 缺一个字段就把整笔调整丢掉，用户会以为 AI 没反应。
func TestAdjustmentProposalDefaultsKind(t *testing.T) {
	raw := `{
		"period": "2026-09", "summary": "补提折旧", "reason": "少提",
		"lines": [{"account_code": "560205", "debit": 300000},
		          {"account_code": "1602", "credit": 300000}]
	}`
	p, err := ParseAdjustmentProposal(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if p.Kind != AdjustKindAdjust {
		t.Errorf("kind 缺省应当是 %s，实际 %q", AdjustKindAdjust, p.Kind)
	}
}
