package service_test

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 部门：新建 / 修改 / 停用 / 删除
// ---------------------------------------------------------------------------
//
// 部门是「辅助核算四维」里唯一需要档案表的一维：绝大多数费用科目
// 声明了按部门核算，没有部门，费用凭证过不了账、员工也计提不了工资。
//
// 在此之前，仓储里有 SaveDepartment，但**没有任何绑定暴露它** ——
// 界面上只有员工表单里那个只读下拉，用户根本没法新建部门。
// 所以这一组测试的第一条就是「能建出来」。

func TestDepartmentCRUD(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()

	// 建账时种了一个「管理部门」，这是设计如此（否则第一次记费用就没部门可选）
	base, err := svc.Departments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(base) == 0 {
		t.Fatal("建账应当种一个默认部门，否则第一次记费用就撞上「缺少必需的辅助核算」")
	}

	id, err := svc.SaveDepartment(ctx, service.Department{
		Name: "销售部", Code: "002", Enabled: true, Remark: "华东区",
	})
	if err != nil {
		t.Fatalf("新建部门失败: %v", err)
	}
	if id == 0 {
		t.Fatal("新建部门没有返回 id")
	}

	list, err := svc.Departments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got *service.Department
	for i := range list {
		if list[i].ID == id {
			got = &list[i]
		}
	}
	if got == nil {
		t.Fatal("新建的部门没出现在列表里")
	}
	if got.Name != "销售部" || got.Code != "002" || !got.Enabled {
		t.Errorf("部门字段没存对：%+v", got)
	}

	// 改名
	got.Name = "销售中心"
	if _, err := svc.SaveDepartment(ctx, *got); err != nil {
		t.Fatalf("修改部门失败: %v", err)
	}
	list, _ = svc.Departments(ctx)
	for _, d := range list {
		if d.ID == id && d.Name != "销售中心" {
			t.Errorf("改名没生效，还是 %q", d.Name)
		}
	}
}

// ★ 编码留空时用名称兜底。
//
// 部门编码对小微企业没有实际意义，强制填只会增加录入负担；
// 而 code 在库里有 UNIQUE 约束，留空直接插会撞约束报一个看不懂的错。
func TestDepartmentCodeFallsBackToName(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()

	id, err := svc.SaveDepartment(ctx, service.Department{Name: "生产车间", Enabled: true})
	if err != nil {
		t.Fatalf("不填编码应当能存下来: %v", err)
	}
	list, _ := svc.Departments(ctx)
	for _, d := range list {
		if d.ID == id && d.Code != "生产车间" {
			t.Errorf("编码应退回名称，实际 %q", d.Code)
		}
	}
}

// 全名带上上级，报表与凭证上显示「生产中心—一车间」比「一车间」清楚。
func TestDepartmentFullNameIncludesParent(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()

	parent, err := svc.SaveDepartment(ctx, service.Department{Name: "生产中心", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveDepartment(ctx, service.Department{
		Name: "一车间", Code: "002", ParentID: &parent, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	list, _ := svc.Departments(ctx)
	var found string
	for _, d := range list {
		if d.Name == "一车间" {
			found = d.FullName
		}
	}
	if found != "生产中心—一车间" {
		t.Errorf("全名 = %q，期望「生产中心—一车间」", found)
	}
}

// ★ 删除：没有被任何东西引用时真删。
func TestDeleteUnusedDepartment(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()

	id, err := svc.SaveDepartment(ctx, service.Department{Name: "临时部门", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteDepartment(ctx, id); err != nil {
		t.Fatalf("没人用的部门应当能删掉: %v", err)
	}
	list, _ := svc.Departments(ctx)
	for _, d := range list {
		if d.ID == id {
			t.Fatal("删完之后还在列表里")
		}
	}
}

// ★★ 删除：有员工、有下级、或已经被凭证用过时**必须拒绝**。
//
// 部门在库里是**自由整数**（ledger_entry.dept_id、employee.dept_id…），
// 没有外键兜着 —— 这层检查是唯一的防线。放过去的话，后果是
// 去年的部门费用表里出现「部门#3」，而没有人知道那是谁。
func TestDeleteDepartmentRefusedWhenInUse(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()

	deptID, err := svc.SaveDepartment(ctx, service.Department{Name: "销售部", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Name: "张三", Kind: "employee", DeptID: &deptID,
		BaseSalary: money.Money(800000), SIProfile: "default",
		ExpenseAccountCode: "560201", Enabled: true, HireDate: "2025-01-01",
	}); err != nil {
		t.Fatalf("建员工失败: %v", err)
	}

	err = svc.DeleteDepartment(ctx, deptID)
	if err == nil {
		t.Fatal("部门下还有员工，不该删得掉 —— 员工会指向一个不存在的部门")
	}
	// 错误里要说清楚被什么挡住了，以及替代方案
	if !strings.Contains(err.Error(), "员工") {
		t.Errorf("要说清楚被什么挡住了，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "停用") {
		t.Errorf("要给出替代方案（改用停用），实际 %v", err)
	}

	// 有下级部门同样拒绝
	parent, _ := svc.SaveDepartment(ctx, service.Department{Name: "生产中心", Enabled: true})
	child, _ := svc.SaveDepartment(ctx, service.Department{
		Name: "一车间", Code: "003", ParentID: &parent, Enabled: true,
	})
	_ = child
	if err := svc.DeleteDepartment(ctx, parent); err == nil {
		t.Error("还有下级部门时不该删得掉")
	}
}

// DepartmentUsageOf 让界面在**点删除之前**就能把话说明白。
func TestDepartmentUsageCounts(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()

	deptID, _ := svc.SaveDepartment(ctx, service.Department{Name: "销售部", Enabled: true})
	if _, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Name: "张三", Kind: "employee", DeptID: &deptID,
		BaseSalary: money.Money(800000), SIProfile: "default",
		ExpenseAccountCode: "560201", Enabled: true, HireDate: "2025-01-01",
	}); err != nil {
		t.Fatal(err)
	}

	u, err := svc.DepartmentUsageOf(ctx, deptID)
	if err != nil {
		t.Fatal(err)
	}
	if u.Employees != 1 {
		t.Errorf("员工数 = %d，期望 1", u.Employees)
	}
	if u.Total() != 1 {
		t.Errorf("引用总数 = %d，期望 1", u.Total())
	}
	if u.Describe() == "" {
		t.Error("Describe 应当生成一句人话")
	}
}

// ---------------------------------------------------------------------------
// 人事异动：离职 / 转部门 / 调薪
// ---------------------------------------------------------------------------

// 建一个员工，返回 id。
func hireOne(t *testing.T, svc *service.Service, deptID *int64) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Name: "张三", Kind: "employee", DeptID: deptID,
		BaseSalary: money.Money(800000), SIBase: money.Money(800000),
		SIProfile: "default", ExpenseAccountCode: "560201",
		Enabled: true, HireDate: "2025-01-01",
	})
	if err != nil {
		t.Fatalf("建员工失败: %v", err)
	}
	return id
}

func employeeByID(t *testing.T, svc *service.Service, id int64) *service.EmployeeView {
	t.Helper()
	list, err := svc.Employees(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	t.Fatalf("员工 id=%d 不见了", id)
	return nil
}

// ★ 离职：一个动作同时写日期与停用。
//
// 只写日期不停用的话，离职之后他仍然会被自动带进新的工资单 ——
// 工资单按 IsActiveIn 判断，而它同时看离职日期与启用开关。
func TestResignEmployeeWritesDateAndDisabled(t *testing.T) {
	svc := newSvc(t, 6)
	ctx := context.Background()
	deptID, _ := svc.SaveDepartment(ctx, service.Department{Name: "销售部", Enabled: true})
	id := hireOne(t, svc, &deptID)

	got, err := svc.ResignEmployee(ctx, service.ResignInput{
		ID: id, LeaveDate: "2025-03-31", Reason: "个人原因", Operator: "李会计",
	})
	if err != nil {
		t.Fatalf("离职失败: %v", err)
	}
	if got.LeaveDate != "2025-03-31" {
		t.Errorf("离职日期 = %q", got.LeaveDate)
	}
	if got.Enabled {
		t.Error("离职之后档案必须停用 —— 否则下个月的工资单还会把他带进来")
	}
	if got.StatusLabel != "离职" {
		t.Errorf("状态标签 = %q，期望「离职」", got.StatusLabel)
	}
}

// 离职日期不能早于入职日期：这两个日期会同时用于判断某个月算不算在职，
// 颠倒过来会让工资单漏人或多算。
func TestResignBeforeHireIsRejected(t *testing.T) {
	svc := newSvc(t, 6)
	ctx := context.Background()
	deptID, _ := svc.SaveDepartment(ctx, service.Department{Name: "销售部", Enabled: true})
	id := hireOne(t, svc, &deptID) // 入职 2025-01-01

	if _, err := svc.ResignEmployee(ctx, service.ResignInput{
		ID: id, LeaveDate: "2024-12-31",
	}); err == nil {
		t.Fatal("离职日期早于入职日期应当被拒绝")
	}
	// 空日期也要挡住 —— 留空的话这个人永远算在职
	if _, err := svc.ResignEmployee(ctx, service.ResignInput{ID: id}); err == nil {
		t.Fatal("离职日期为空应当被拒绝")
	}
	// 重复办离职
	if _, err := svc.ResignEmployee(ctx, service.ResignInput{
		ID: id, LeaveDate: "2025-03-31",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResignEmployee(ctx, service.ResignInput{
		ID: id, LeaveDate: "2025-04-30",
	}); err == nil {
		t.Error("已经离职的人不该能再办一次")
	}
}

// ★ 转部门：目标部门必须存在且**启用**。
//
// 停用的部门在新建凭证时是选不到的，把人调进去，他的工资计提
// 会卡在「缺少必需的辅助核算」上 —— 而且要到生成工资单时才发现。
func TestTransferEmployee(t *testing.T) {
	svc := newSvc(t, 6)
	ctx := context.Background()
	a, _ := svc.SaveDepartment(ctx, service.Department{Name: "销售部", Enabled: true})
	b, _ := svc.SaveDepartment(ctx, service.Department{Name: "生产部", Code: "003", Enabled: true})
	id := hireOne(t, svc, &a)

	got, err := svc.TransferEmployee(ctx, service.TransferInput{
		ID: id, DeptID: b, Reason: "产线缺人", Operator: "王主管",
	})
	if err != nil {
		t.Fatalf("转部门失败: %v", err)
	}
	if got.DeptID == nil || *got.DeptID != b {
		t.Errorf("部门没换过去：%+v", got.DeptID)
	}

	// 转到同一个部门没意义，直接说清楚
	if _, err := svc.TransferEmployee(ctx, service.TransferInput{
		ID: id, DeptID: b,
	}); err == nil {
		t.Error("转到当前部门应当被拒绝，并说明原因")
	}

	// 停用的部门不能调进去
	disabled, _ := svc.SaveDepartment(ctx, service.Department{
		Name: "已撤销部", Code: "004", Enabled: false,
	})
	if _, err := svc.TransferEmployee(ctx, service.TransferInput{
		ID: id, DeptID: disabled,
	}); err == nil {
		t.Error("调进停用部门应当被拒绝")
	}

	// 不存在的部门
	if _, err := svc.TransferEmployee(ctx, service.TransferInput{
		ID: id, DeptID: 999999,
	}); err == nil {
		t.Error("不存在的部门应当被拒绝")
	}
	// 不填部门
	if _, err := svc.TransferEmployee(ctx, service.TransferInput{
		ID: id,
	}); err == nil {
		t.Error("不填部门应当被拒绝 —— 没有部门就记不了账")
	}
}

// ★ 调薪：改档案上的标准值，历史工资单不动。
func TestAdjustSalary(t *testing.T) {
	svc := newSvc(t, 6)
	ctx := context.Background()
	deptID, _ := svc.SaveDepartment(ctx, service.Department{Name: "销售部", Enabled: true})
	id := hireOne(t, svc, &deptID)

	got, err := svc.AdjustSalary(ctx, service.AdjustSalaryInput{
		ID: id, BaseSalary: money.Money(950000), Reason: "年度调薪", Operator: "王主管",
	})
	if err != nil {
		t.Fatalf("调薪失败: %v", err)
	}
	if got.BaseSalary != money.Money(950000) {
		t.Errorf("基本工资 = %s，期望 9500.00", got.BaseSalary)
	}
	// 没给社保基数时跟随工资（与 SaveEmployee 同一口径）
	if got.SIBase != money.Money(950000) {
		t.Errorf("社保基数应当跟随工资，实际 %s", got.SIBase)
	}

	// 社保基数单独给：小微企业按最低基数缴很常见，强制同步会逼人填错的数
	got, err = svc.AdjustSalary(ctx, service.AdjustSalaryInput{
		ID: id, BaseSalary: money.Money(1200000), SIBase: money.Money(400000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SIBase != money.Money(400000) {
		t.Errorf("显式给的社保基数被覆盖了：%s", got.SIBase)
	}

	// 填 0 要挡住 —— 要停发工资应该走离职
	if _, err := svc.AdjustSalary(ctx, service.AdjustSalaryInput{
		ID: id, BaseSalary: 0,
	}); err == nil {
		t.Error("工资填 0 应当被拒绝，并指向「离职」")
	}
}

// 三个动作都要留下**能回答具体问题**的日志。
//
// 「3 月把他从销售部调到生产部、工资从 8000 调到 9500」是工资争议里
// 最常见的一问，而通用的「维护员工档案」日志只剩一句「改了张三的档案」。
func TestHRActionsAreAudited(t *testing.T) {
	svc := newSvc(t, 6)
	ctx := context.Background()

	// 日志默认是**关的**（见 service.SetAuditDir 的说明：不指定目录时
	// 宁可让调用方知道「日志没开」，也不要悄悄写到当前目录）。
	// 这里显式打开，测的就是「用户那边真的能查到这条记录」。
	service.SetAuditDir(t.TempDir())

	a, _ := svc.SaveDepartment(ctx, service.Department{Name: "销售部", Enabled: true})
	b, _ := svc.SaveDepartment(ctx, service.Department{Name: "生产部", Code: "003", Enabled: true})
	id := hireOne(t, svc, &a)

	if _, err := svc.AdjustSalary(ctx, service.AdjustSalaryInput{
		ID: id, BaseSalary: money.Money(950000), Reason: "年度调薪", Operator: "王主管",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TransferEmployee(ctx, service.TransferInput{
		ID: id, DeptID: b, Reason: "产线缺人", Operator: "王主管",
	}); err != nil {
		t.Fatal(err)
	}

	page, err := svc.QueryAudit(ctx, service.AuditQuery{Limit: 50})
	if err != nil {
		t.Fatalf("查日志失败: %v", err)
	}

	var sawSalary, sawTransfer bool
	for _, e := range page.Entries {
		// 调薪日志必须能看出「调到了多少」。金额由 money.Money 格式化，
		// 带千分位（9,500.00）—— 断言字面而不是「包含 9500」，
		// 否则这条测试会因为格式而假过。
		if strings.Contains(e.Summary, "基本工资") && strings.Contains(e.Summary, "9,500.00") {
			sawSalary = true
		}
		// 转部门必须能看出「调到了哪个部门」
		if strings.Contains(e.Summary, "调入") && strings.Contains(e.Summary, "生产部") {
			sawTransfer = true
		}
	}
	if !sawSalary {
		t.Error("调薪日志里要能看出「调到了多少」—— " +
			"「3 月把他的工资从 8000 调到 9500」是工资争议里最常见的一问")
	}
	if !sawTransfer {
		t.Error("转部门日志里要能看出「调到了哪个部门」")
	}

	// 操作人要记下来：日志不能回答「谁做的」等于没有
	for _, e := range page.Entries {
		if strings.Contains(e.Summary, "调入") && e.Operator != "王主管" {
			t.Errorf("操作人 = %q，期望「王主管」", e.Operator)
		}
	}
}

// ★★ 界面「编辑」一次不该改掉任何东西。
//
// 这一条是拿一个真实的数据丢失事故换来的：`toEmployeeView` 原来漏了
// deptId / position / expenseAccountCode 三个字段，而界面的编辑是
// 把这一行整个展开进表单再提交的（`editEmployee` 里的 `{...row}`）——
// 字段没被带出来，表单里就是空的，**保存一次就把这个人的部门、岗位、
// 工资费用科目全抹掉了**，而且没有任何提示。
//
// 后果不是「少显示一列」：抹掉部门之后他的工资计提会被
// 「缺少必需的辅助核算」拒绝；抹掉工资科目之后会退回默认的
// 「管理费用—工资」，生产人员的工资就从「生产成本—直接人工」
// 挪进了期间费用。
//
// 所以这里测的是一条不变式：**把 View 原样喂回 Input，账套不该有任何变化**。
// 界面提交的就是 View 的字段，这条不变式一破，编辑就等于清空。
func TestEmployeeViewRoundTripsWithoutLosingFields(t *testing.T) {
	svc := newSvc(t, 6)
	ctx := context.Background()

	deptID, _ := svc.SaveDepartment(ctx, service.Department{Name: "销售部", Enabled: true})
	id, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Name: "张三", Code: "E001", Kind: "employee", IDCard: "330100199001011234",
		Phone: "13800000000", BankName: "工商银行", BankAccount: "6222020000000000",
		BaseSalary: money.Money(800000), SIBase: money.Money(600000),
		HFBBase: money.Money(600000), SIProfile: "default",
		SpecialAdditional: money.Money(200000),
		DeptID:            &deptID, Position: "销售经理",
		ExpenseAccountCode: "560201", CompanyAccountCode: "560202",
		HireDate: "2025-01-01", Enabled: true, Remark: "华东区",
	})
	if err != nil {
		t.Fatalf("建员工失败: %v", err)
	}

	before := employeeByID(t, svc, id)
	if before.DeptID == nil {
		t.Fatal("EmployeeView 没带出 deptId —— 界面编辑一次就会把部门抹掉")
	}
	if before.Position == "" || before.ExpenseAccountCode == "" {
		t.Fatalf("EmployeeView 没带出岗位/工资科目：%+v", before)
	}

	// 按界面的做法：把 View 整个展开回 Input 再提交一次
	if _, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		ID: before.ID, Code: before.Code, Name: before.Name, Kind: before.Kind,
		IDCard: before.IDCard, Phone: before.Phone,
		BankName: before.BankName, BankAccount: before.BankAccount,
		BaseSalary: before.BaseSalary, SIBase: before.SIBase, HFBBase: before.HFBBase,
		SIProfile: before.SIProfile, SpecialAdditional: before.SpecialAdditional,
		DeptID: before.DeptID, Position: before.Position,
		ExpenseAccountCode: before.ExpenseAccountCode,
		CompanyAccountCode: "560202",
		HireDate:           before.HireDate, LeaveDate: before.LeaveDate,
		Enabled: before.Enabled, Remark: before.Remark,
	}); err != nil {
		t.Fatalf("回存失败: %v", err)
	}

	after := employeeByID(t, svc, id)
	if after.DeptID == nil || *after.DeptID != deptID {
		t.Errorf("编辑一次之后部门没了：%+v —— 他的工资计提会被「缺少必需的辅助核算」拒绝",
			after.DeptID)
	}
	if after.Position != "销售经理" {
		t.Errorf("岗位 = %q，被抹掉了", after.Position)
	}
	if after.ExpenseAccountCode != "560201" {
		t.Errorf("工资科目 = %q，被抹掉了（会退回默认的管理费用—工资，"+
			"把生产人员的工资挪进期间费用）", after.ExpenseAccountCode)
	}
}
