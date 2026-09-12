package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/payroll"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 员工档案
// ---------------------------------------------------------------------------

func TestEmployeeCRUD(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	dept := mustDepartment(t, svc, "管理部门")

	id, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Code: "E001", Name: "张三", IDCard: "330100199001011234",
		BaseSalary: money100(12000), SpecialAdditional: money100(2000),
		SIProfile: "杭州标准", HireDate: "2024-03-01", Enabled: true,
		DeptID: &dept,
	})
	if err != nil {
		t.Fatalf("保存员工失败: %v", err)
	}
	if id == 0 {
		t.Fatal("应返回 id")
	}

	list, err := svc.Employees(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("员工数 = %d", len(list))
	}
	e := list[0]
	if e.Name != "张三" || e.BaseSalary != money100(12000) {
		t.Errorf("读回不一致: %+v", e)
	}
	// ★ 没单独给社保基数时按基本工资 —— 最常见的情形
	if e.SIBase != money100(12000) {
		t.Errorf("社保基数应默认等于基本工资，实际 %s", e.SIBase)
	}
	if e.HFBBase != money100(12000) {
		t.Errorf("公积金基数应默认等于社保基数，实际 %s", e.HFBBase)
	}
	if e.StatusLabel != "在职" {
		t.Errorf("状态 = %s", e.StatusLabel)
	}
	if e.HireDate != "2024-03-01" {
		t.Errorf("入职日期 = %s", e.HireDate)
	}

	// ★ 按最低基数缴纳的小微企业很多 —— 显式给的值不能被默认逻辑覆盖
	id2, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Code: "E002", Name: "李四",
		BaseSalary: money100(8000), SIBase: money100(4000),
		Enabled: true, DeptID: &dept,
	})
	if err != nil {
		t.Fatal(err)
	}
	list, _ = svc.Employees(ctx, false)
	for _, x := range list {
		if x.ID == id2 {
			if x.SIBase != money100(4000) {
				t.Errorf("显式指定的社保基数被覆盖了：%s", x.SIBase)
			}
			// 公积金没给，应跟随社保基数
			if x.HFBBase != money100(4000) {
				t.Errorf("公积金基数应跟随社保基数，实际 %s", x.HFBBase)
			}
		}
	}
}

func TestEmployeeValidation(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	if _, err := svc.SaveEmployee(ctx, service.EmployeeInput{Name: "  "}); err == nil {
		t.Error("空姓名应被拒绝")
	}
	if _, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Name: "王五", HireDate: "2024/03/01",
	}); err == nil {
		t.Error("非法日期格式应被拒绝")
	}
}

func TestEmployeesOnlyEnabled(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	dept := mustDepartment(t, svc, "综合部")
	if _, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Name: "在职的", Code: "A1", Enabled: true, BaseSalary: money100(5000),
		DeptID: &dept,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Name: "离职的", Code: "A2", Enabled: false, BaseSalary: money100(5000),
		LeaveDate: "2025-02-28", DeptID: &dept,
	}); err != nil {
		t.Fatal(err)
	}

	all, _ := svc.Employees(ctx, false)
	if len(all) != 2 {
		t.Errorf("全部员工 = %d，期望 2", len(all))
	}
	enabled, _ := svc.Employees(ctx, true)
	if len(enabled) != 1 || enabled[0].Name != "在职的" {
		t.Errorf("在职员工筛选有误: %+v", enabled)
	}
	for _, e := range all {
		if e.Name == "离职的" && e.StatusLabel != "离职" {
			t.Errorf("离职员工状态 = %s", e.StatusLabel)
		}
	}
}

// ---------------------------------------------------------------------------
// 工资单
// ---------------------------------------------------------------------------

// seedEmployees 建两个员工。
func seedEmployees(t *testing.T, svc *service.Service) {
	t.Helper()
	ctx := context.Background()
	// 费用科目要求部门辅助核算，员工必须有部门才能计提工资
	dept := mustDepartment(t, svc, "管理部门")
	for _, e := range []service.EmployeeInput{
		{Code: "E001", Name: "张三", BaseSalary: money100(12000),
			SpecialAdditional: money100(2000), Enabled: true, DeptID: &dept},
		{Code: "E002", Name: "李四", BaseSalary: money100(8000),
			Enabled: true, DeptID: &dept},
	} {
		if _, err := svc.SaveEmployee(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPayrollPreviewDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedEmployees(t, svc)

	d, err := svc.PayrollPreview(ctx, service.BuildPayrollInput{
		Year: 2025, Month: 1, CreatedBy: "李会计",
	})
	if err != nil {
		t.Fatalf("预演失败: %v", err)
	}
	if d.Headcount != 2 {
		t.Errorf("人数 = %d，期望 2", d.Headcount)
	}
	if d.TotalGross != money100(20000) {
		t.Errorf("应发合计 = %s，期望 20000.00", d.TotalGross)
	}
	if d.StatusLabel == "" || !strings.Contains(d.StatusLabel, "未保存") {
		t.Errorf("预演结果应标明未保存，实际 %q", d.StatusLabel)
	}

	// ★ 预演不得写库
	runs, _ := svc.PayrollRuns(ctx)
	if len(runs) != 0 {
		t.Errorf("预演不该写入工资单，实际 %d 张", len(runs))
	}
}

func TestBuildAndPostPayroll(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedEmployees(t, svc)

	d, err := svc.BuildPayroll(ctx, service.BuildPayrollInput{
		Year: 2025, Month: 1, CreatedBy: "李会计",
	})
	if err != nil {
		t.Fatalf("生成工资单失败: %v", err)
	}
	if d.ID == 0 {
		t.Fatal("应返回工资单 id")
	}
	if d.Status != "draft" {
		t.Errorf("新生成的应是草稿，实际 %s", d.Status)
	}
	if len(d.Items) != 2 {
		t.Fatalf("工资项 = %d", len(d.Items))
	}

	// 每人的实发 = 应发 − 个人社保 − 个税
	for _, it := range d.Items {
		want := it.Gross.Sub(it.AttendanceDeduct).Sub(it.OtherDeduct).
			Sub(it.InsuranceSelf).Sub(it.IIT)
		if it.Net != want {
			t.Errorf("%s 实发 = %s，按公式应为 %s", it.EmployeeName, it.Net, want)
		}
		if it.EmployeeName == "" {
			t.Error("工资项应带员工姓名")
		}
	}

	// 个税应大于 0（月薪 12000 与 8000 都超过起征点）
	if d.TotalIIT <= 0 {
		t.Errorf("应代扣个税，实际 %s", d.TotalIIT)
	}
	// ★ 累计预扣预缴法：第一个月按累计口径算，税额应为正且合理
	if d.TotalIIT >= d.TotalGross {
		t.Errorf("个税不可能超过应发，实际 %s vs %s", d.TotalIIT, d.TotalGross)
	}

	// 记账要签章
	if _, err := svc.PostPayroll(ctx, d.ID, ""); err == nil {
		t.Fatal("缺少记账人应被拒绝")
	}

	posted, err := svc.PostPayroll(ctx, d.ID, "王主管")
	if err != nil {
		t.Fatalf("工资单记账失败: %v", err)
	}
	if posted.Status != "posted" {
		t.Errorf("状态 = %s，期望 posted", posted.Status)
	}
	// 应生成计提与发放两张**草稿**凭证（过账在账期结算，所以没有号）
	if posted.AccrualVoucherLabel == "" || posted.PaymentVoucherLabel == "" {
		t.Errorf("应生成计提与发放两张凭证: %+v", posted)
	}
	if posted.AccrualVoucherNo != "" || posted.PaymentVoucherNo != "" {
		t.Errorf("★ 还没结算就不该有凭证号（草稿不占号）: %q %q",
			posted.AccrualVoucherNo, posted.PaymentVoucherNo)
	}

	// 试算必须仍然平衡 —— 工资凭证涉及多个科目，最容易在这里出问题
	rep, err := svc.DB().Reports().TrialBalanceReport(ctx, period.NewKey(2025, 1))
	if err != nil {
		t.Fatal(err)
	}
	_, _, d2, c2, _, _ := rep.Totals()
	if d2 != c2 {
		t.Errorf("工资记账后试算不平衡：借 %s ≠ 贷 %s", d2, c2)
	}
}

func TestBuildPayrollWithoutEmployees(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	_, err := svc.BuildPayroll(ctx, service.BuildPayrollInput{Year: 2025, Month: 1})
	if !errors.Is(err, service.ErrNoEmployees) {
		t.Fatalf("没有员工应报 ErrNoEmployees，实际 %v", err)
	}
}

func TestBuildPayrollBadPeriod(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedEmployees(t, svc)
	for _, in := range []service.BuildPayrollInput{
		{Year: 2025, Month: 0}, {Year: 2025, Month: 13}, {Year: 0, Month: 1},
	} {
		if _, err := svc.BuildPayroll(ctx, in); err == nil {
			t.Errorf("非法期间 %+v 应被拒绝", in)
		}
	}
}

func TestTaxTable(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	tt, err := svc.TaxTable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tt.Brackets) == 0 {
		t.Fatal("税率表不该为空")
	}
	// 综合所得预扣率表一是 7 级
	if len(tt.Brackets) != 7 {
		t.Errorf("级数 = %d，期望 7", len(tt.Brackets))
	}
	// 第一级 3%，最高一级 45%
	if tt.Brackets[0].RateLabel != "3%" {
		t.Errorf("第一级税率 = %s，期望 3%%", tt.Brackets[0].RateLabel)
	}
	if tt.Brackets[len(tt.Brackets)-1].RateLabel != "45%" {
		t.Errorf("最高级税率 = %s，期望 45%%",
			tt.Brackets[len(tt.Brackets)-1].RateLabel)
	}
	// 级距必须严格递增，且最后一级无上限
	for i := 1; i < len(tt.Brackets); i++ {
		if tt.Brackets[i].Upper <= tt.Brackets[i-1].Upper && tt.Brackets[i].Upper != 0 {
			t.Errorf("第 %d 级上限 %s 未递增", i+1, tt.Brackets[i].Upper)
		}
	}
	if tt.Brackets[len(tt.Brackets)-1].Upper != 0 {
		t.Error("最高一级应无上限（0 表示无上限）")
	}
	if !strings.Contains(tt.Note, "参数表") {
		t.Errorf("应说明税率来自可配置的参数表，实际 %q", tt.Note)
	}
}

// ---------------------------------------------------------------------------
// 导出
// ---------------------------------------------------------------------------

func TestExportExcel(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	dir := t.TempDir()

	cases := []struct {
		kind service.ExportKind
		name string
	}{
		{service.ExportBalanceSheet, "资产负债表"},
		{service.ExportIncomeStatement, "利润表"},
		{service.ExportTrialBalance, "科目余额表"},
		{service.ExportCashFlow, "现金流量表"},
		{service.ExportContactBalances, "往来单位余额表"},
	}
	for _, c := range cases {
		dest := filepath.Join(dir, string(c.kind)+".xlsx")
		res, err := svc.ExportExcel(ctx, service.ExportOptions{
			Kind: c.kind, Dest: dest, Year: 2025, Month: 1,
		})
		if err != nil {
			t.Errorf("导出 %s 失败: %v", c.name, err)
			continue
		}
		if res.Title != c.name {
			t.Errorf("标题 = %q，期望 %q", res.Title, c.name)
		}
		// 往来单位余额表在空账套上本来就是 0 行（没有往来单位），
		// 文件依然要能写出来 —— 这正是「空表也要导」的意义
		if res.Rows == 0 && c.kind != service.ExportContactBalances {
			t.Errorf("%s 导出 0 行", c.name)
		}
		fi, err := os.Stat(res.Path)
		if err != nil {
			t.Errorf("%s 没写出文件: %v", c.name, err)
			continue
		}
		// xlsx 是 zip，最小也有几 KB
		if fi.Size() < 3000 {
			t.Errorf("%s 文件只有 %d 字节，可能是空的", c.name, fi.Size())
		}
	}

	// 没给扩展名时自动补上
	res, err := svc.ExportExcel(ctx, service.ExportOptions{
		Kind: service.ExportBalanceSheet,
		Dest: filepath.Join(dir, "noext"), Year: 2025, Month: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.Path, ".xlsx") {
		t.Errorf("应自动补上 .xlsx，实际 %q", res.Path)
	}

	// 空路径与未知类型要报错
	if _, err := svc.ExportExcel(ctx, service.ExportOptions{
		Kind: service.ExportBalanceSheet, Year: 2025, Month: 1,
	}); err == nil {
		t.Error("空路径应被拒绝")
	}
	if _, err := svc.ExportExcel(ctx, service.ExportOptions{
		Kind: "不存在", Dest: filepath.Join(dir, "x.xlsx"), Year: 2025, Month: 1,
	}); err == nil {
		t.Error("未知类型应被拒绝")
	}
}

func TestSuggestedExportName(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	name, err := svc.SuggestedExportName(ctx, service.ExportBalanceSheet, 2025, 3)
	if err != nil {
		t.Fatal(err)
	}
	// 文件名里要同时有单位名、报表名与期间 ——
	// 攒了一堆导出文件之后还能分清哪个是哪个
	for _, want := range []string{"服务层测试公司", "资产负债表", "2025-03", ".xlsx"} {
		if !strings.Contains(name, want) {
			t.Errorf("文件名 %q 缺少 %q", name, want)
		}
	}

	// 单位名里的非法字符要被清掉，否则写不出文件
	if _, err := svc.DB().Books().Get(ctx); err != nil {
		t.Fatal(err)
	}
	// 按年导出时期间只有年份
	name, _ = svc.SuggestedExportName(ctx, service.ExportTrialBalance, 2025, 0)
	if !strings.Contains(name, "2025") || strings.Contains(name, "2025-00") {
		t.Errorf("按年导出的文件名 = %q", name)
	}
}

func TestExportPayroll(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedEmployees(t, svc)

	d, err := svc.BuildPayroll(ctx, service.BuildPayrollInput{
		Year: 2025, Month: 1, CreatedBy: "李会计",
	})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "工资表.xlsx")
	res, err := svc.ExportPayrollExcel(ctx, d.ID, dest)
	if err != nil {
		t.Fatalf("导出工资表失败: %v", err)
	}
	if res.Rows != 2 {
		t.Errorf("工资表行数 = %d，期望 2", res.Rows)
	}
	if fi, err := os.Stat(res.Path); err != nil || fi.Size() < 3000 {
		t.Errorf("工资表没写好: %v", err)
	}
}

// mustDepartment 取一个部门 id（迁移里已种了「管理部门」）。
func mustDepartment(t *testing.T, svc *service.Service, name string) int64 {
	t.Helper()
	ctx := context.Background()
	list, err := svc.Departments(ctx)
	if err != nil {
		t.Fatalf("读取部门失败: %v", err)
	}
	for _, d := range list {
		if d.Name == name || d.FullName == name {
			return d.ID
		}
	}
	id, err := svc.SaveDepartment(ctx, service.Department{Name: name, Enabled: true})
	if err != nil {
		t.Fatalf("新增部门失败: %v", err)
	}
	return id
}

// ---------------------------------------------------------------------------
// 部门档案
// ---------------------------------------------------------------------------

// ★ 迁移里种了「管理部门」：没有它，用户第一次记费用就会撞上
// 「缺少必需的辅助核算」，而账套里连一个可选部门都没有。
func TestDepartmentsSeeded(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	list, err := svc.Departments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("建账后应至少有一个默认部门")
	}
	var found bool
	for _, d := range list {
		if d.Name == "管理部门" {
			found = true
			if d.FullName == "" {
				t.Error("应拼出全名")
			}
		}
	}
	if !found {
		t.Errorf("应有默认的「管理部门」，实际 %+v", list)
	}
}

func TestDepartmentTree(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	parent, err := svc.SaveDepartment(ctx, service.Department{
		Name: "生产中心", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := svc.SaveDepartment(ctx, service.Department{
		Name: "一车间", ParentID: &parent, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	list, _ := svc.Departments(ctx)
	for _, d := range list {
		if d.ID == child {
			if d.FullName != "生产中心—一车间" {
				t.Errorf("全名 = %q，期望「生产中心—一车间」", d.FullName)
			}
		}
		if d.ID == parent && d.ParentID != nil {
			t.Error("顶层部门不该有上级")
		}
	}

	if _, err := svc.SaveDepartment(ctx, service.Department{Name: "  "}); err == nil {
		t.Error("空名称应被拒绝")
	}
}

// ★ 社保方案必须能建、能存、能校验。
//
// 修之前：仓储里的 SaveInsuranceSchemes 只被测试调用，
// desktop 无绑定、CLI 无命令、界面无入口 ——
// 员工档案填方案名报「方案不存在」，留空则静默按 0 缴社保。
// 整个工资模块因此不可用。
func TestInsuranceSchemeCRUD(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	// 一开始没有任何方案
	list, err := svc.InsuranceSchemes(ctx)
	if err != nil {
		t.Fatalf("取方案失败: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("新账套不该有社保方案，实际 %d 个", len(list))
	}

	// 模板：全零费率，且一眼能看出「还没配」
	tpl := svc.SchemeTemplate("杭州市（示例）")
	if tpl.Rates.PensionSelf != 0 {
		t.Error("模板不该预置任何费率 —— 看着像真的默认值会被用户直接当真使用")
	}
	if tpl.IsConfigured() {
		t.Error("全零费率的方案应报告为「未配置」")
	}

	// 保存一个填好的方案
	sc := svc.SchemeTemplate("杭州标准")
	sc.City = "杭州"
	sc.BaseMin, sc.BaseMax = money100(4000), money100(30000)
	sc.Rates.PensionSelf = money.Rate(80_000) // 8%
	sc.Rates.MedicalSelf = money.Rate(20_000) // 2%
	sc.Rates.PensionCo = money.Rate(160_000)  // 16%
	if err := svc.SaveInsuranceSchemes(ctx, []*payroll.InsuranceScheme{sc}); err != nil {
		t.Fatalf("保存方案失败: %v", err)
	}

	list, err = svc.InsuranceSchemes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Scheme.Name != "杭州标准" {
		t.Fatalf("方案 = %+v", list)
	}
	if !list[0].Configured {
		t.Error("填过费率的方案应报告为已配置")
	}
	if list[0].UsedBy != 0 {
		t.Errorf("还没有员工引用，UsedBy = %d", list[0].UsedBy)
	}
	// 上下限要能往返
	if list[0].Scheme.BaseMin != money100(4000) || list[0].Scheme.BaseMax != money100(30000) {
		t.Errorf("基数上下限没存住：%s ~ %s",
			list[0].Scheme.BaseMin, list[0].Scheme.BaseMax)
	}
}

// 方案要被员工引用后 UsedBy 才对得上 —— 界面靠它防止误删。
func TestInsuranceSchemeUsedBy(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	sc := svc.SchemeTemplate("当地标准")
	sc.Rates.PensionSelf = money.Rate(80_000)
	if err := svc.SaveInsuranceSchemes(ctx, []*payroll.InsuranceScheme{sc}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Name: "张三", Kind: "employee", BaseSalary: money100(10000),
		SIProfile: "当地标准",
	}); err != nil {
		t.Fatalf("建员工失败: %v", err)
	}

	list, err := svc.InsuranceSchemes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].UsedBy != 1 {
		t.Fatalf("UsedBy = %d，期望 1", list[0].UsedBy)
	}
}

// 非法方案要挡住，而不是存进去等算工资时才炸。
func TestInsuranceSchemeValidation(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	// 名称为空
	if err := svc.SaveInsuranceSchemes(ctx,
		[]*payroll.InsuranceScheme{svc.SchemeTemplate("")}); err == nil {
		t.Error("空名称应被拒绝")
	}

	// 上下限写反
	bad := svc.SchemeTemplate("写反了")
	bad.BaseMin, bad.BaseMax = money100(30000), money100(4000)
	if err := svc.SaveInsuranceSchemes(ctx,
		[]*payroll.InsuranceScheme{bad}); err == nil {
		t.Error("下限高于上限应被拒绝")
	}

	// 把「元」当成了「%」：费率 12000 元 = 1200%
	bad = svc.SchemeTemplate("费率填成金额")
	bad.Rates.PensionSelf = money.Rate(12_000 * money.RateScale)
	if err := svc.SaveInsuranceSchemes(ctx,
		[]*payroll.InsuranceScheme{bad}); err == nil {
		t.Error("费率超过 100% 应被拒绝")
	}

	// 一个都不留
	if err := svc.SaveInsuranceSchemes(ctx, nil); err == nil {
		t.Error("清空全部方案应被拒绝")
	}

	// 重名
	a, b := svc.SchemeTemplate("同名"), svc.SchemeTemplate("同名")
	a.Rates.PensionSelf, b.Rates.PensionSelf = money.Rate(80_000), money.Rate(90_000)
	if err := svc.SaveInsuranceSchemes(ctx,
		[]*payroll.InsuranceScheme{a, b}); err == nil {
		t.Error("方案重名应被拒绝（员工按名称引用，重名会让引用不确定）")
	}
}

// ★ 端到端：员工档案填了缴费基数 → 工资单按基数算社保。
//
// 这条盖住的是一个会让整月工资全错的 bug：员工按最低基数缴纳时，
// 社保必须按基数算，不能按实发工资算。
func TestPayrollUsesDeclaredInsuranceBaseEndToEnd(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	dept := mustDepartment(t, svc, "管理部门")

	// 方案：养老 8% + 医疗 2% = 个人 10%
	sc := svc.SchemeTemplate("当地标准")
	sc.BaseMin, sc.BaseMax = money100(4000), money100(30000)
	sc.Rates.PensionSelf = money.Rate(80_000)
	sc.Rates.MedicalSelf = money.Rate(20_000)
	if err := svc.SaveInsuranceSchemes(ctx, []*payroll.InsuranceScheme{sc}); err != nil {
		t.Fatal(err)
	}

	// 员工：月薪 2 万，但**按 4000 最低基数**缴纳
	emp, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Code: "E001", Name: "张三", Kind: "employee", Enabled: true,
		HireDate:   "2024-03-01",
		BaseSalary: money100(20000),
		SIBase:     money100(4000),
		SIProfile:  "当地标准",
		DeptID:     &dept,
	})
	if err != nil {
		t.Fatalf("建员工失败: %v", err)
	}
	_ = emp

	run, err := svc.BuildPayroll(ctx, service.BuildPayrollInput{
		Year: 2025, Month: 3,
	})
	if err != nil {
		t.Fatalf("生成工资单失败: %v", err)
	}
	if len(run.Items) == 0 {
		t.Fatal("工资单里没有员工")
	}
	it := run.Items[0]

	// 应发 = 基本工资 20000
	if it.Gross != money100(20000) {
		t.Fatalf("应发 = %s，期望 20000.00", it.Gross)
	}
	// ★ 社保基数必须是申报的 4000，不是应发的 20000
	if it.InsuranceBase != money100(4000) {
		t.Errorf("★ 社保基数 = %s，期望按申报的 4000.00（而不是应发工资 20000）",
			it.InsuranceBase)
	}
	// 个人社保 = 4000 × 10% = 400
	if want := money100(400); it.InsuranceSelf != want {
		t.Errorf("★ 个人社保 = %s，期望 %s（基数 4000 乘 10%%）",
			it.InsuranceSelf, want)
	}
	// 若按应发工资算会是 2000 —— 正是要防的那个 bug
	if it.InsuranceSelf == money100(2000) {
		t.Error("个人社保按应发工资算了")
	}
}
