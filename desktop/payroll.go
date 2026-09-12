package main

import (
	"strings"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/payroll"
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 工资模块的绑定
// ---------------------------------------------------------------------------

// EmployeeRequest 是界面提交的员工信息。
//
// ★ 金额用「元」的字符串从界面传过来，由这里转成「分」。
// 与凭证录入同一套约定：前端只传用户敲的原文，
// 解析与换算全部走 Go 侧的严格十进制解析。
type EmployeeRequest struct {
	ID          int64  `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	IDCard      string `json:"idCard"`
	Phone       string `json:"phone"`
	BankName    string `json:"bankName"`
	BankAccount string `json:"bankAccount"`
	BaseSalary  string `json:"baseSalary"`
	SIBase      string `json:"siBase"`
	HFBBase     string `json:"hfbBase"`
	SIProfile   string `json:"siProfile"`
	SpecialAdd  string `json:"specialAdditional"`
	// DeptID 是所属部门。绝大多数费用科目要求部门辅助核算，
	// 员工没有部门就记不了账。
	DeptID   *int64 `json:"deptId"`
	Position string `json:"position"`
	// ExpenseAccountCode 是工资费用归集科目，留空默认「管理费用—工资」。
	ExpenseAccountCode string `json:"expenseAccountCode"`
	HireDate           string `json:"hireDate"`
	LeaveDate          string `json:"leaveDate"`
	Enabled            bool   `json:"enabled"`
	Remark             string `json:"remark"`
}

func (r EmployeeRequest) toService() (service.EmployeeInput, error) {
	out := service.EmployeeInput{
		ID: r.ID, Code: r.Code, Name: r.Name, IDCard: r.IDCard,
		Phone: r.Phone, BankName: r.BankName, BankAccount: r.BankAccount,
		SIProfile: r.SIProfile, HireDate: r.HireDate, LeaveDate: r.LeaveDate,
		Enabled: r.Enabled, Remark: r.Remark,
		DeptID: r.DeptID, Position: r.Position,
		ExpenseAccountCode: r.ExpenseAccountCode,
	}
	for _, f := range []struct {
		name string
		in   string
		out  *int64
	}{
		{"基本工资", r.BaseSalary, (*int64)(&out.BaseSalary)},
		{"社保基数", r.SIBase, (*int64)(&out.SIBase)},
		{"公积金基数", r.HFBBase, (*int64)(&out.HFBBase)},
		{"专项附加扣除", r.SpecialAdd, (*int64)(&out.SpecialAdditional)},
	} {
		m, err := ParseYuan(f.in)
		if err != nil {
			return out, errField(f.name, err)
		}
		*f.out = int64(m)
	}
	return out, nil
}

// Employees 返回员工列表。
func (a *App) Employees(onlyEnabled bool) (out []service.EmployeeView, err error) {
	defer recoverTo(&err, "Employees")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Employees(a.context(), onlyEnabled))
}

// SaveEmployee 新增或修改员工。
func (a *App) SaveEmployee(req EmployeeRequest) (out int64, err error) {
	defer recoverTo(&err, "SaveEmployee")()
	svc, f := a.book()
	if f != nil {
		return 0, f
	}
	in, cerr := req.toService()
	if cerr != nil {
		return 0, cerr
	}
	return wrap(svc.SaveEmployee(a.context(), in))
}

// PayrollRunRequest 是生成工资单的参数。
type PayrollRunRequest struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	// CreatedBy 是制单人。
	CreatedBy string `json:"createdBy"`
	// OnlyPostedHistory 为真时累计数只统计已确认/已过账的历史工资单。
	OnlyPostedHistory bool `json:"onlyPostedHistory"`
	// Save 为假时只预演不落库。
	Save bool `json:"save"`
}

// PayrollRuns 返回工资单列表。
func (a *App) PayrollRuns() (out []service.PayrollRunView, err error) {
	defer recoverTo(&err, "PayrollRuns")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.PayrollRuns(a.context()))
}

// PayrollRunDetail 返回一张工资单的完整内容。
func (a *App) PayrollRunDetail(id int64) (out *service.PayrollRunDetail, err error) {
	defer recoverTo(&err, "PayrollRunDetail")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.PayrollRun(a.context(), id))
}

// BuildPayroll 生成一张工资单；Save 为假时只预演。
func (a *App) BuildPayroll(req PayrollRunRequest) (out *service.PayrollRunDetail, err error) {
	defer recoverTo(&err, "BuildPayroll")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	in := service.BuildPayrollInput{
		Year: req.Year, Month: req.Month, CreatedBy: req.CreatedBy,
		OnlyPostedHistory: req.OnlyPostedHistory,
	}
	if !req.Save {
		return wrap(svc.PayrollPreview(a.context(), in))
	}
	return wrap(svc.BuildPayroll(a.context(), in))
}

// PostPayroll 把工资单记账。
func (a *App) PostPayroll(id int64, postingBy string) (out *service.PayrollRunDetail, err error) {
	defer recoverTo(&err, "PostPayroll")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	if strings.TrimSpace(postingBy) == "" {
		return nil, &Fault{Kind: FaultInvalid,
			Message: "请填写记账人 —— 工资凭证同样需要记账签章"}
	}
	return wrap(svc.PostPayroll(a.context(), id, postingBy))
}

// Departments 返回部门列表。
func (a *App) Departments() (out []service.Department, err error) {
	defer recoverTo(&err, "Departments")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Departments(a.context()))
}

// InsuranceSchemes 返回全部社保方案及其使用情况。
//
// ★ 这个入口以前不存在：仓储里的 SaveInsuranceSchemes 只被测试调用，
// 于是产品里**没有任何办法创建社保方案** —— 员工档案填了方案名会报
// 「方案不存在」，留空则静默按 0 缴社保。工资模块因此不可用。
func (a *App) InsuranceSchemes() (out []*service.InsuranceSchemeInfo, err error) {
	defer recoverTo(&err, "InsuranceSchemes")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.InsuranceSchemes(a.context()))
}

// SchemeTemplate 返回一张费率为零的空方案，供界面「新增」用。
//
// 刻意不预置任何比例：预置一组看着像真的数字，用户会直接保存就开始用，
// 而那几乎必然与当地当年标准不符，且算错了不会报任何错。
func (a *App) SchemeTemplate(name string) (out *payroll.InsuranceScheme, err error) {
	defer recoverTo(&err, "SchemeTemplate")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	// svc.SchemeTemplate 不返回 error（它只是造一个结构体），
	// 但绑定必须是 (T, error) —— 见 wrap 的说明：返回裸指针会让
	// Wails 把 nil 当成非 nil 的 error，成功的调用被报成失败。
	return wrap(svc.SchemeTemplate(name), nil)
}

// SaveInsuranceSchemes 保存全部社保方案（整体替换）。
func (a *App) SaveInsuranceSchemes(schemes []*payroll.InsuranceScheme) (err error) {
	defer recoverTo(&err, "SaveInsuranceSchemes")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	if serr := svc.SaveInsuranceSchemes(a.context(), schemes); serr != nil {
		return classify(serr)
	}
	return nil
}

// TaxTableInfo 返回当前生效的个税税率表。
func (a *App) TaxTableInfo() (out *service.TaxTableInfo, err error) {
	defer recoverTo(&err, "TaxTableInfo")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.TaxTable(a.context()))
}

// errField 把金额解析错误包装成带字段名的输入错误。
func errField(field string, err error) error {
	return &Fault{Kind: FaultInvalid,
		Message: field + "：" + err.Error()}
}

// ---------------------------------------------------------------------------
// 部门管理
// ---------------------------------------------------------------------------
//
// ★ 原来只有 Departments()（只读）。仓库里有 SaveDepartment，
// 但**没有任何绑定暴露它** —— 于是产品里根本没有办法新建或修改部门，
// 用户在设置里看到的部门清单永远只有建账时种下的那一个「管理部门」。
// 而绝大多数费用科目要求按部门辅助核算，没有部门就记不了费用。

// SaveDepartment 新增或修改部门。
func (a *App) SaveDepartment(req DepartmentRequest) (out int64, err error) {
	defer recoverTo(&err, "SaveDepartment")()
	svc, f := a.book()
	if f != nil {
		return 0, f
	}
	return wrap(svc.SaveDepartment(a.context(), service.Department{
		ID: req.ID, Code: req.Code, Name: req.Name,
		ParentID: req.ParentID, Enabled: req.Enabled, Remark: req.Remark,
	}))
}

// DeleteDepartment 删除部门。
//
// 被员工、下级部门或已记账凭证引用时会被拒绝，并说明被什么挡住了 ——
// 部门在库里是自由整数，没有外键兜着，所以这层检查是唯一的防线。
func (a *App) DeleteDepartment(id int64) (err error) {
	defer recoverTo(&err, "DeleteDepartment")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	_, f2 := wrap(struct{}{}, svc.DeleteDepartment(a.context(), id))
	return f2
}

// DepartmentUsageOf 统计一个部门被引用的次数。
//
// 界面在**点删除之前**就用它把话说明白（「这个部门下还有 3 个员工」），
// 而不是让用户点完再看报错。
func (a *App) DepartmentUsageOf(id int64) (out service.DepartmentUsage, err error) {
	defer recoverTo(&err, "DepartmentUsageOf")()
	svc, f := a.book()
	if f != nil {
		return out, f
	}
	return wrap(svc.DepartmentUsageOf(a.context(), id))
}

// DepartmentRequest 是新增/修改部门的请求。
type DepartmentRequest struct {
	ID       int64  `json:"id"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	ParentID *int64 `json:"parentId"`
	Enabled  bool   `json:"enabled"`
	Remark   string `json:"remark"`
}

// ---------------------------------------------------------------------------
// 人事异动
// ---------------------------------------------------------------------------

// ResignEmployeeRequest 是办理离职的请求。
type ResignEmployeeRequest struct {
	ID        int64  `json:"id"`
	LeaveDate string `json:"leaveDate"`
	Reason    string `json:"reason"`
	Operator  string `json:"operator"`
}

// ResignEmployee 办理离职：写离职日期 + 停用档案 + 留一条日志。
func (a *App) ResignEmployee(req ResignEmployeeRequest) (out *service.EmployeeView, err error) {
	defer recoverTo(&err, "ResignEmployee")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.ResignEmployee(a.context(), service.ResignInput{
		ID: req.ID, LeaveDate: req.LeaveDate, Reason: req.Reason,
		Operator: req.Operator,
	}))
}

// TransferEmployeeRequest 是把员工调入另一个部门的请求。
type TransferEmployeeRequest struct {
	ID       int64  `json:"id"`
	DeptID   int64  `json:"deptId"`
	Reason   string `json:"reason"`
	Operator string `json:"operator"`
}

// TransferEmployee 把员工调到一个新部门。
func (a *App) TransferEmployee(req TransferEmployeeRequest) (out *service.EmployeeView, err error) {
	defer recoverTo(&err, "TransferEmployee")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.TransferEmployee(a.context(), service.TransferInput{
		ID: req.ID, DeptID: req.DeptID, Reason: req.Reason, Operator: req.Operator,
	}))
}

// AdjustSalaryRequest 是调薪的请求。金额是**元**的字符串，与界面一致。
type AdjustSalaryRequest struct {
	ID         int64  `json:"id"`
	BaseSalary string `json:"baseSalary"`
	SIBase     string `json:"siBase"`
	HFBBase    string `json:"hfbBase"`
	Reason     string `json:"reason"`
	Operator   string `json:"operator"`
}

// AdjustSalary 调整员工的工资标准。
//
// ★ 只改档案上的标准值。已经生成的工资单各自固化了当时的基本工资
// （见 salary_item 的列），所以历史单据不会跟着变。
func (a *App) AdjustSalary(req AdjustSalaryRequest) (out *service.EmployeeView, err error) {
	defer recoverTo(&err, "AdjustSalary")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	in := service.AdjustSalaryInput{
		ID: req.ID, Reason: req.Reason, Operator: req.Operator,
	}
	for _, f2 := range []struct {
		name string
		in   string
		out  *money.Money
	}{
		{"基本工资", req.BaseSalary, &in.BaseSalary},
		{"社保基数", req.SIBase, &in.SIBase},
		{"公积金基数", req.HFBBase, &in.HFBBase},
	} {
		if strings.TrimSpace(f2.in) == "" {
			continue
		}
		m, perr := ParseYuan(f2.in)
		if perr != nil {
			return nil, errField(f2.name, perr)
		}
		*f2.out = m
	}
	return wrap(svc.AdjustSalary(a.context(), in))
}
