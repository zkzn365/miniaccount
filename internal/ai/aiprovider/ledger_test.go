package aiprovider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
)

// ---------------------------------------------------------------------------
// 账务查询工具：报表 / 体检 / 工资 / 明细账
// ---------------------------------------------------------------------------

type fakeReports struct {
	data *ReportData
	kind string
	k    PeriodKey
	err  error
}

func (f *fakeReports) Report(_ context.Context, kind string, k PeriodKey) (*ReportData, error) {
	f.kind, f.k = kind, k
	return f.data, f.err
}

type fakeCheck struct {
	data *PeriodCheck
	k    PeriodKey
}

func (f *fakeCheck) CheckPeriod(_ context.Context, k PeriodKey) (*PeriodCheck, error) {
	f.k = k
	return f.data, nil
}

type fakePayroll struct {
	data *PayrollPreview
	k    PeriodKey
}

func (f *fakePayroll) PreviewPayroll(_ context.Context, k PeriodKey) (*PayrollPreview, error) {
	f.k = k
	return f.data, nil
}

type fakeLedgerTool struct {
	code string
	y, m int
	data *ReportData
}

func (f *fakeLedgerTool) Ledger(_ context.Context, code string, y, m int) (*ReportData, error) {
	f.code, f.y, f.m = code, y, m
	return f.data, nil
}

// 期间解析：格式错了要报错，而不是默默用成 0-0
func TestParsePeriod(t *testing.T) {
	ok := []struct {
		in   string
		want PeriodKey
	}{
		{"2026-09", PeriodKey{2026, 9}},
		{"2026-9", PeriodKey{2026, 9}},
		{" 2026-12 ", PeriodKey{2026, 12}},
	}
	for _, c := range ok {
		got, err := parsePeriod(c.in)
		if err != nil {
			t.Errorf("%q 应当能解析：%v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q → %+v，期望 %+v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "2026", "2026-13", "2026-00", "abc-09", "2026-ab"} {
		if _, err := parsePeriod(bad); err == nil {
			t.Errorf("%q 应当被拒绝", bad)
		}
	}
}

func TestGetReportRendersBalanced(t *testing.T) {
	p := &fakeReports{data: &ReportData{
		Title: "科目余额表", Period: "2026-09",
		Lines: []ReportLine{
			{Code: "1001", Name: "库存现金", Debit: money.MustParse("100.00")},
			{Code: "5001", Name: "主营业务收入", Credit: money.MustParse("100.00")},
		},
		TotalDebit: money.MustParse("100.00"), TotalCredit: money.MustParse("100.00"),
	}}
	out := runTool(t, GetReportTool(p), `{"kind":"trial","period":"2026-09"}`)
	if p.kind != "trial" || p.k.String() != "2026-09" {
		t.Errorf("参数没透传：kind=%q k=%v", p.kind, p.k)
	}
	if !strings.Contains(out, "科目余额表") || !strings.Contains(out, "1001 库存现金") {
		t.Errorf("渲染结果不对：%q", out)
	}
	if !strings.Contains(out, "试算平衡") {
		t.Errorf("借贷相等时要明说：%q", out)
	}
	// ★ 金额要写成「元」而不是「分」：模型会把它抄进回答里给用户看
	if strings.Contains(out, "10000") {
		t.Errorf("金额不该以分出现（模型会抄错小数点）：%q", out)
	}
}

// 不平衡必须显眼 —— 这是「本月检查核算」最要紧的一句话
func TestGetReportShowsImbalance(t *testing.T) {
	p := &fakeReports{data: &ReportData{
		Title: "科目余额表", Period: "2026-09",
		TotalDebit: money.MustParse("100.00"), TotalCredit: money.MustParse("99.00"),
	}}
	out := runTool(t, GetReportTool(p), `{"kind":"trial","period":"2026-09"}`)
	if !strings.Contains(out, "不平衡") || !strings.Contains(out, "1.00") {
		t.Errorf("不平衡要写清楚差多少：%q", out)
	}
}

func TestGetReportRejectsUnknownKind(t *testing.T) {
	_, err := GetReportTool(&fakeReports{}).Run(context.Background(),
		json.RawMessage(`{"kind":"spaceship","period":"2026-09"}`))
	if err == nil {
		t.Fatal("不认识的报表种类应当报错")
	}
	if !strings.Contains(err.Error(), "spreadsheet") && !strings.Contains(err.Error(), "spaceship") {
		t.Errorf("错误要点出是哪个种类：%v", err)
	}
}

func TestCheckPeriodRendersConclusion(t *testing.T) {
	p := &fakeCheck{data: &PeriodCheck{
		Period: "2026-09", CanClose: false, Drafts: 2,
		Items: []CheckItem{
			{Title: "试算平衡", Level: "ok", Detail: "借方合计 100.00 = 贷方合计 100.00"},
			{Title: "资产负债表勾稽", Level: "error", Detail: "资产 ≠ 负债 + 权益，差 1.00"},
		},
		Income: money.MustParse("1000.00"), Expense: money.MustParse("600.00"),
		Profit:         money.MustParse("400.00"),
		ClosingEntries: []string{"借 5001 结转损益 1000.00"},
	}}
	out := runTool(t, CheckPeriodTool(p), `{"period":"2026-09"}`)
	for _, want := range []string{
		"结账前体检", "试算平衡", "资产负债表勾稽", "差 1.00",
		"2 张草稿凭证", "利润 400.00", "不能结账",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("体检结果里缺少 %q：\n%s", want, out)
		}
	}
}

func TestCheckPeriodSaysCanClose(t *testing.T) {
	p := &fakeCheck{data: &PeriodCheck{Period: "2026-09", CanClose: true,
		Items: []CheckItem{{Title: "试算平衡", Level: "ok"}}}}
	out := runTool(t, CheckPeriodTool(p), `{"period":"2026-09"}`)
	if !strings.Contains(out, "可以结账") {
		t.Errorf("通过时要给一句明确结论：%q", out)
	}
}

// ★ 五险一金：没配方案时必须说清楚，并且**不许编费率**
func TestPreviewPayrollWarnsWhenNoScheme(t *testing.T) {
	p := &fakePayroll{data: &PayrollPreview{
		Period: "2026-09", SchemeCount: 0,
		// 与真实数据源同一句措辞（store 那边就是这么写的）
		Note: "账套里还没有配社保方案 —— 没有方案，社保算不出来。" +
			"需要用户提供**当地当年**的缴费比例与基数上下限，不要自己编。",
	}}
	tool := PreviewPayrollTool(p)
	out := runTool(t, tool, `{"period":"2026-09"}`)
	if !strings.Contains(out, "还没有配社保方案") {
		t.Errorf("要说清楚没方案这件事：%q", out)
	}
	if !strings.Contains(out, "不要自己编") {
		t.Errorf("★ 要把「不要编费率」这句话带给模型：%q", out)
	}
	// 工具描述里也必须有这条红线 —— 模型选工具、看输出，两处都该看到
	if !strings.Contains(tool.Description, "不要自己编费率") {
		t.Errorf("★ 工具描述里缺少禁止编费率：%q", tool.Description)
	}
}

func TestPreviewPayrollRendersFiveInsurances(t *testing.T) {
	p := &fakePayroll{data: &PayrollPreview{
		Period: "2026-09", SchemeCount: 1, Schemes: []string{"杭州2026"},
		People: []PayrollPerson{{
			Name: "张三", Gross: money.MustParse("12000.00"),
			InsuranceBase: money.MustParse("12000.00"),
			PensionSelf:   money.MustParse("960.00"), MedicalSelf: money.MustParse("240.00"),
			UnemploymentSelf: money.MustParse("60.00"), HousingFundSelf: money.MustParse("1440.00"),
			PensionCo: money.MustParse("1920.00"), MedicalCo: money.MustParse("1140.00"),
			UnemploymentCo: money.MustParse("60.00"), InjuryCo: money.MustParse("24.00"),
			MaternityCo: money.MustParse("96.00"), HousingFundCo: money.MustParse("1440.00"),
			IIT: money.MustParse("200.00"), Net: money.MustParse("9100.00"),
			SchemeName: "杭州2026",
		}},
		TotalGross: money.MustParse("12000.00"),
	}}
	out := runTool(t, PreviewPayrollTool(p), `{"period":"2026-09"}`)
	for _, want := range []string{
		"张三", "社保基数 12,000.00", "方案 杭州2026",
		"个人承担合计 2,700.00", "养老 960.00", "公积金 1,440.00",
		"单位承担合计 4,680.00", "工伤 24.00", "生育 96.00",
		"个税 200.00", "实发 9,100.00",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("五险一金要逐项列出，缺少 %q：\n%s", want, out)
		}
	}
}

func TestGetLedgerPassesArgs(t *testing.T) {
	p := &fakeLedgerTool{data: &ReportData{
		Title: "明细账 560210", Period: "2026-09",
		Lines: []ReportLine{{Code: "2026-09-05", Name: "付房租（记-2026-09-0001）",
			Debit: money.MustParse("12000.00")}},
	}}
	out := runTool(t, GetLedgerTool(p), `{"account_code":"560210","period":"2026-09"}`)
	if p.code != "560210" || p.y != 2026 || p.m != 9 {
		t.Errorf("参数没透传：%s %d %d", p.code, p.y, p.m)
	}
	if !strings.Contains(out, "付房租") || !strings.Contains(out, "12,000.00") {
		t.Errorf("明细账渲染不对：%q", out)
	}

	// 空科目编码要挡住 —— 否则会去查整本账
	if _, err := GetLedgerTool(&fakeLedgerTool{}).Run(context.Background(),
		json.RawMessage(`{"account_code":"  ","period":"2026-09"}`)); err == nil {
		t.Error("空科目编码应当报错")
	}
}

// ---------------------------------------------------------------------------
// 人事异动提议
// ---------------------------------------------------------------------------

func TestParseHRProposal(t *testing.T) {
	p, err := ParseHRProposal(`{"kind":"resign","employee_id":3,
		"leave_date":"2026-09-30","reason":"个人原因"}`)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != HRResign || p.EmployeeID != 3 || p.LeaveDate != "2026-09-30" {
		t.Errorf("解析结果不对：%+v", p)
	}
	if p.Label() != "办理离职" {
		t.Errorf("中文名 = %q", p.Label())
	}

	dept := `{"kind":"transfer","employee_id":3,"dept_id":2,"reason":"调岗"}`
	tp, err := ParseHRProposal(dept)
	if err != nil {
		t.Fatal(err)
	}
	if tp.DeptID == nil || *tp.DeptID != 2 || tp.Label() != "转部门" {
		t.Errorf("转部门解析不对：%+v", tp)
	}

	sal, err := ParseHRProposal(`{"kind":"salary","employee_id":3,
		"base_salary":"9500.00","reason":"晋升"}`)
	if err != nil {
		t.Fatal(err)
	}
	if sal.BaseSalary != "9500.00" || sal.Label() != "调薪" {
		t.Errorf("调薪解析不对：%+v", sal)
	}
}

// 不完整的提议必须被拒 —— 否则界面上会出现一张点了没反应的卡片
func TestParseHRProposalRejectsIncomplete(t *testing.T) {
	bad := []string{
		`{"kind":"resign","reason":"x"}`,                              // 没有 employee_id
		`{"kind":"resign","employee_id":3,"reason":"x"}`,              // 没有离职日期
		`{"kind":"transfer","employee_id":3,"reason":"x"}`,            // 没有部门
		`{"kind":"salary","employee_id":3,"reason":"x"}`,              // 没有金额
		`{"kind":"spaceship","employee_id":3,"reason":"x"}`,           // 种类乱填
		`{"kind":"resign","employee_id":0,"leave_date":"2026-09-30"}`, // id 为 0
	}
	for _, s := range bad {
		if _, err := ParseHRProposal(s); err == nil {
			t.Errorf("应当被拒绝：%s", s)
		}
	}
}

func TestProposeHRActionIsTerminal(t *testing.T) {
	tool := ProposeHRActionTool()
	if !tool.Terminal {
		t.Fatal("★ 人事异动必须由用户确认 —— 模型不能自己动手")
	}
	if tool.RenderAsk == nil {
		t.Fatal("需要 RenderAsk：历史里要留下提议过什么")
	}
	got := tool.RenderAsk(json.RawMessage(
		`{"kind":"resign","employee_id":3,"leave_date":"2026-09-30","reason":"个人原因"}`))
	if !strings.Contains(got, "离职") || !strings.Contains(got, "个人原因") {
		t.Errorf("渲染结果要能认出提议了什么：%q", got)
	}
	if got := tool.RenderAsk(json.RawMessage(`坏 JSON`)); got != "" {
		t.Errorf("坏参数应当返回空串，实际 %q", got)
	}
}

// ---------------------------------------------------------------------------
// 提示词：这些工具该怎么用，红线在哪
// ---------------------------------------------------------------------------

func TestPromptExplainsFinanceTools(t *testing.T) {
	sys := AccountantSystemPrompt(Input{Task: TaskFreeform})
	for _, want := range []string{
		"数字必须来自工具",
		"get_report", "get_ledger", "check_period", "preview_payroll",
		// 五险一金那条红线
		"五险一金", "不要自己编",
		// 人事异动那条边界
		"人事异动", "propose_hr_action",
		"是谁什么时候办的",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("提示词里缺少 %q", want)
		}
	}
}

// 工具描述的措辞：要写「什么时候用」而不是「这是什么」
func TestToolDescriptionsSayWhenToUse(t *testing.T) {
	tools := []Tool{
		GetReportTool(&fakeReports{}),
		CheckPeriodTool(&fakeCheck{}),
		PreviewPayrollTool(&fakePayroll{}),
		GetLedgerTool(&fakeLedgerTool{}),
		ProposeHRActionTool(),
		SearchDepartmentsTool(&fakeDepts{}),
		SearchEmployeesTool(&fakeEmps{}),
	}
	for _, tool := range tools {
		d := tool.Description
		if len([]rune(d)) < 20 {
			t.Errorf("%s 的描述太短，模型选不准：%q", tool.Name, d)
		}
		if !strings.Contains(d, "时用它") && !strings.Contains(d, "用它") {
			t.Errorf("%s 的描述没有说「什么时候用」：%q", tool.Name, d)
		}
		// 参数 schema 必须是合法 JSON
		var v any
		if err := json.Unmarshal(tool.Parameters, &v); err != nil {
			t.Errorf("%s 的参数不是合法 JSON：%v", tool.Name, err)
		}
	}
}
