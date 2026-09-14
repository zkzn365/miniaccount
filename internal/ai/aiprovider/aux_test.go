package aiprovider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 辅助核算档案工具
// ---------------------------------------------------------------------------
//
// 这一组是拿一次真实事故换来的：用户说「误餐费 325，员工垫付现金报销」，
// 模型给出的分录会计上完全正确，但 560204 要求**部门**辅助核算而没填，
// 被护栏打回。根子不在模型 —— 它根本没有查部门的工具，
// 于是既查不到有哪些部门，也没法带着真实选项问用户。

type fakeDepts struct {
	list []DepartmentBrief
	err  error
}

func (f *fakeDepts) Departments(context.Context) ([]DepartmentBrief, error) {
	return f.list, f.err
}

type fakeEmps struct {
	list []EmployeeBrief
	err  error
}

func (f *fakeEmps) Employees(context.Context) ([]EmployeeBrief, error) {
	return f.list, f.err
}

func TestSearchDepartmentsListsIDs(t *testing.T) {
	p := &fakeDepts{list: []DepartmentBrief{
		{ID: 1, Code: "001", Name: "管理部门", FullName: "管理部门"},
		{ID: 3, Code: "003", Name: "华东区", FullName: "销售部/华东区"},
	}}
	tool := SearchDepartmentsTool(p)

	all := runTool(t, tool, `{"keyword":""}`)
	if !strings.Contains(all, "1 管理部门") || !strings.Contains(all, "3 销售部/华东区") {
		t.Errorf("应当把 id 与名称都列出来，实际：%q", all)
	}

	// 关键词过滤：带上级全名也要能搜到
	hit := runTool(t, tool, `{"keyword":"销售"}`)
	if !strings.Contains(hit, "3 ") {
		t.Errorf("按上级名应当能搜到下级部门，实际：%q", hit)
	}
	if strings.Contains(hit, "管理部门") {
		t.Errorf("不该返回不匹配的部门：%q", hit)
	}
}

// ★ 一条都没有时，必须明确告诉模型「下一步做什么」。
//
// 只说「没有匹配」，模型最常见的反应是换一个不需要该辅助核算的科目硬凑 ——
// 费用挂错地方，而报表照样平，看不出错。
func TestSearchDepartmentsEmptyTellsModelWhatToDo(t *testing.T) {
	tool := SearchDepartmentsTool(&fakeDepts{})
	out := runTool(t, tool, `{"keyword":""}`)
	if !strings.Contains(out, "还没有任何部门") {
		t.Errorf("要说清楚是「一个都没有」而不是「没匹配上」：%q", out)
	}
	if !strings.Contains(out, "propose_new_aux") {
		t.Errorf("★ 要给出下一步动作（提议新建），否则模型只能瞎猜：%q", out)
	}
	if !strings.Contains(out, "不要因此换一个") {
		t.Errorf("★ 要明确禁止「换个科目绕过去」：%q", out)
	}
}

// 有部门、但关键词没匹配上：要说「列全部再问用户」，而不是让模型自己编
func TestSearchDepartmentsNoMatchPointsToListing(t *testing.T) {
	p := &fakeDepts{list: []DepartmentBrief{{ID: 1, Name: "管理部门"}}}
	out := runTool(t, SearchDepartmentsTool(p), `{"keyword":"生产"}`)
	if !strings.Contains(out, "没有匹配的部门") {
		t.Errorf("要说清楚是没匹配上：%q", out)
	}
	if !strings.Contains(out, "列出全部") {
		t.Errorf("要指向「不带关键词列全部」，模型才知道怎么拿到真实选项：%q", out)
	}
}

func TestSearchEmployeesFiltersLeft(t *testing.T) {
	dept := int64(1)
	p := &fakeEmps{list: []EmployeeBrief{
		{ID: 1, Code: "E001", Name: "张三", DeptName: "管理部门"},
		{ID: 2, Code: "E002", Name: "李四", DeptName: "销售部", Left: true},
	}}
	tool := SearchEmployeesTool(p)

	active := runTool(t, tool, `{"keyword":""}`)
	if !strings.Contains(active, "张三") {
		t.Errorf("在职的要出来：%q", active)
	}
	if strings.Contains(active, "李四") {
		t.Errorf("默认不该列离职的：%q", active)
	}

	// ★ 补记上个月给某人的报销，而那个人这个月刚离职 —— 这种情况必须查得到
	withLeft := runTool(t, tool, `{"keyword":"","include_left":true}`)
	if !strings.Contains(withLeft, "李四") {
		t.Errorf("★ 要能连离职的一起查（补记历史业务时要用）：%q", withLeft)
	}
	if !strings.Contains(withLeft, "已离职") {
		t.Errorf("离职状态要标出来，否则模型分不清该选谁：%q", withLeft)
	}
	_ = dept

	// 按部门名搜
	byDept := runTool(t, tool, `{"keyword":"销售"}`)
	if !strings.Contains(byDept, "张三") && !strings.Contains(byDept, "（没有匹配）") {
		// 张三在管理部门，不该被「销售」匹配到
		if strings.Contains(byDept, "张三") {
			t.Errorf("不该返回不匹配的员工：%q", byDept)
		}
	}
}

func TestSearchEmployeesEmptyHint(t *testing.T) {
	out := runTool(t, SearchEmployeesTool(&fakeEmps{}), `{"keyword":""}`)
	if !strings.Contains(out, "还没有任何员工") || !strings.Contains(out, "propose_new_aux") {
		t.Errorf("空账套要给明确指引，实际：%q", out)
	}
}

// ---------------------------------------------------------------------------
// 提议新建档案
// ---------------------------------------------------------------------------

func TestParseAuxProposal(t *testing.T) {
	p, err := ParseAuxProposal(`{"kind":"department","name":"生产部",
		"reason":"560204 要求部门辅助核算"}`)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != AuxKindDepartment || p.Name != "生产部" {
		t.Errorf("解析结果不对：%+v", p)
	}
	if p.Label() != "部门" {
		t.Errorf("中文名 = %q", p.Label())
	}
	if !p.Valid() {
		t.Error("应当有效")
	}

	// 员工带部门
	e, err := ParseAuxProposal(`{"kind":"employee","name":"张三","dept_id":2,"reason":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if e.DeptID == nil || *e.DeptID != 2 {
		t.Errorf("员工所属部门没解析出来：%+v", e)
	}
	if e.Label() != "员工" {
		t.Errorf("中文名 = %q", e.Label())
	}

	// 缺名字 / 类型乱填：都要拒绝，否则界面会渲染出一个建不出来的卡片
	for _, bad := range []string{
		`{"kind":"department","reason":"x"}`,
		`{"kind":"spaceship","name":"x","reason":"y"}`,
		`{"kind":"department","name":"   ","reason":"y"}`,
	} {
		if _, err := ParseAuxProposal(bad); err == nil {
			t.Errorf("应当被拒绝：%s", bad)
		}
	}
}

// 提议是终止型的：调用它这一轮就结束，等用户确认
func TestProposeNewAuxIsTerminal(t *testing.T) {
	tool := ProposeNewAuxTool()
	if !tool.Terminal {
		t.Fatal("★ 提议必须是终止型 —— 否则模型会自己往下编，" +
			"而档案还没建出来")
	}
	if tool.RenderAsk == nil {
		t.Fatal("需要 RenderAsk：历史里要留下「提议了什么」")
	}
	got := tool.RenderAsk(json.RawMessage(
		`{"kind":"department","name":"生产部","reason":"560204 要求部门辅助核算"}`))
	if !strings.Contains(got, "生产部") || !strings.Contains(got, "部门") {
		t.Errorf("渲染结果要能认出提议了什么：%q", got)
	}
	// 参数坏掉时返回空串（由 Agent 退回原始参数），不能 panic
	if got := tool.RenderAsk(json.RawMessage(`不是 JSON`)); got != "" {
		t.Errorf("坏参数应当返回空串，实际 %q", got)
	}
}

// ---------------------------------------------------------------------------
// ★ 提示词必须把这条顺序写清楚
// ---------------------------------------------------------------------------

func TestPromptExplainsAuxOrder(t *testing.T) {
	sys := AccountantSystemPrompt(Input{Task: TaskFreeform})
	for _, want := range []string{
		"辅助核算不能空着",
		"search_departments",
		"search_employees",
		"propose_new_aux",
		// 最要紧的两句
		"不要因为缺一个部门，就换一个不需要部门辅助核算的科目",
		"缺部门就先问部门",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("★ 提示词里缺少 %q —— 模型不知道该按什么顺序处理缺辅助核算", want)
		}
	}
	// 单次任务那版不该被塞进对话规则（但辅助核算的规则两边都该有）
	plain := SystemPrompt(Input{Task: TaskBankFlow})
	if !strings.Contains(plain, "propose_new_aux") {
		t.Error("单次任务也会撞上缺辅助核算，那版提示词也该讲这件事")
	}
}
