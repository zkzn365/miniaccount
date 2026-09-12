package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/payroll"
)

// ---------------------------------------------------------------------------
// 人事异动：离职、转部门、调薪
// ---------------------------------------------------------------------------
//
// # 为什么单独做成动作，而不是「让用户去编辑框里改」
//
//   - 三件事各有各的规则（离职日期不能早于入职、调入的部门必须存在且启用、
//     调薪不能填 0），散在一个几十个字段的大表单里，没人替你校验。
//   - 三件事都要**留痕**。「3 月把他从销售部调到生产部、工资从 8000
//     调到 9500」是工资争议里最常见的一问，而通用的「维护员工档案」
//     日志只剩一句「改了张三的档案」，答不出来。
//   - 三件事都该是**一个动作一步完成**。让用户填离职日期、再想起来
//     要把「启用」的勾去掉、还要把工资费用科目改成别的 —— 漏一步就是
//     一个已经离职的人继续出现在下个月的工资单里。
//
// # ★ 三件事都不改历史
//
// 工资单在生成时就把基本工资、社保基数、累计税额整套固化进了
// salary_item（见迁移 0003 的说明）。所以调薪只影响**之后**生成的工资单；
// 转部门不会让上个月的管理费用换部门；离职也不会动已计提的工资。
// 这一点必须在界面上说清楚，否则「改了工资，上个月会不会跟着变」
// 会成为反复出现的疑问。

// ResignInput 是员工离职的请求。
type ResignInput struct {
	ID int64 `json:"id"`
	// LeaveDate 是离职日期 YYYY-MM-DD。必填。
	LeaveDate string `json:"leaveDate"`
	// Reason 是离职原因（选填，进日志）。
	Reason string `json:"reason"`
	// Operator 是经办人（签章人）。留空记为「未署名」。
	Operator string `json:"operator"`
}

// ResignEmployee 办理离职。
//
// 做三件事：写离职日期、停用档案、留一条日志。
//
// ★ 停用是必须的，不是可选的：工资单生成时按 `IsActiveIn` 自动带人，
// 而它同时看离职日期与启用开关。只写日期不停用的话，离职当天之后的
// 期间仍然会把人带进来（日期判断用的是「整月」粒度）。
func (s *Service) ResignEmployee(ctx context.Context, in ResignInput) (*EmployeeView, error) {
	e, err := s.employeeByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if e.LeaveDate.Valid() {
		return nil, fmt.Errorf("%s 已经在 %s 离职了，不用再办一次",
			e.Name, e.LeaveDate)
	}

	raw := strings.TrimSpace(in.LeaveDate)
	if raw == "" {
		return nil, errors.New("请填写离职日期 —— 它决定这个人从哪个月起不再进工资单")
	}
	leave, perr := calendar.Parse(raw)
	if perr != nil {
		return nil, fmt.Errorf("离职日期 %q 无法解析，请用 2025-03-31 这种格式", raw)
	}
	if e.HireDate.Valid() && leave.Before(e.HireDate) {
		return nil, fmt.Errorf("离职日期 %s 早于入职日期 %s —— "+
			"这两个日期会同时用于判断某个月他算不算在职，颠倒过来会让工资单漏人或多算",
			leave, e.HireDate)
	}

	e.LeaveDate = leave
	e.IsEnabled = false
	if _, err := s.db.Payroll().UpsertEmployee(ctx, e); err != nil {
		return nil, err
	}

	s.recordAudit(ctx, AuditEvent{
		Action:   audit.ActionEmployeeResign,
		Summary:  fmt.Sprintf("%s 于 %s 离职", e.Name, leave),
		Entity:   "employee",
		EntityID: fmt.Sprintf("%d", e.ID),
		Detail: map[string]any{
			"name": e.Name, "leaveDate": leave.String(), "reason": in.Reason,
		},
		Operator: in.Operator,
	})

	out := toEmployeeView(e)
	return &out, nil
}

// TransferInput 是员工转部门的请求。
type TransferInput struct {
	ID     int64 `json:"id"`
	DeptID int64 `json:"deptId"`
	// Reason 是异动原因（选填，进日志）。
	Reason   string `json:"reason"`
	Operator string `json:"operator"`
}

// TransferEmployee 把员工调到一个新部门。
//
// ★ 部门不是可选项：绝大多数费用科目声明了按部门辅助核算，
// 员工没有部门，他的工资计提分录就会被「缺少必需的辅助核算」拒绝。
// 所以这里要求目标部门存在且启用，而不是允许「暂时空着」。
func (s *Service) TransferEmployee(ctx context.Context, in TransferInput) (*EmployeeView, error) {
	e, err := s.employeeByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	dept, err := s.enabledDepartment(ctx, in.DeptID)
	if err != nil {
		return nil, err
	}
	if e.DeptID != nil && *e.DeptID == in.DeptID {
		return nil, fmt.Errorf("%s 已经在「%s」了", e.Name, dept.FullName)
	}

	from := "（未指定）"
	if e.DeptID != nil {
		if d, derr := s.departmentName(ctx, *e.DeptID); derr == nil {
			from = d
		}
	}
	old := e.DeptID
	id := in.DeptID
	e.DeptID = &id
	if _, err := s.db.Payroll().UpsertEmployee(ctx, e); err != nil {
		return nil, err
	}

	s.recordAudit(ctx, AuditEvent{
		Action:   audit.ActionEmployeeTransfer,
		Summary:  fmt.Sprintf("%s 由「%s」调入「%s」", e.Name, from, dept.FullName),
		Entity:   "employee",
		EntityID: fmt.Sprintf("%d", e.ID),
		Detail: map[string]any{
			"name": e.Name, "fromDeptId": old, "toDeptId": in.DeptID,
			"toDept": dept.FullName, "reason": in.Reason,
		},
		Operator: in.Operator,
	})

	out := toEmployeeView(e)
	return &out, nil
}

// AdjustSalaryInput 是调薪的请求。
type AdjustSalaryInput struct {
	ID int64 `json:"id"`
	// BaseSalary 是新的月基本工资（分）。必须大于 0。
	BaseSalary money.Money `json:"baseSalary"`
	// SIBase / HFBBase 是新的社保与公积金基数（分）；为 0 表示跟随基本工资。
	//
	// 单独给是因为「涨了工资，但社保仍按最低基数缴」在小微企业非常常见，
	// 强制两者同步会把用户逼去填一个错的数。
	SIBase  money.Money `json:"siBase"`
	HFBBase money.Money `json:"hfbBase"`
	// Reason 是调薪原因（选填，进日志）。
	Reason   string `json:"reason"`
	Operator string `json:"operator"`
}

// AdjustSalary 调整员工的工资标准。
//
// 只改**档案上的标准值**。已经生成的工资单一律不动 ——
// 它们各自固化了当时的基本工资（见 salary_item 的列），
// 所以 3 月那张单子不会因为 4 月涨薪而变化。
func (s *Service) AdjustSalary(ctx context.Context, in AdjustSalaryInput) (*EmployeeView, error) {
	e, err := s.employeeByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if in.BaseSalary <= 0 {
		return nil, errors.New("新的基本工资必须大于 0 —— " +
			"要停发工资请用「离职」，直接填 0 会让工资单里出现一条 0 元的记录")
	}

	oldBase, oldSI := e.BaseSalary, e.SIBase
	e.BaseSalary = in.BaseSalary
	// 0 表示跟随基本工资（与 SaveEmployee 的口径一致）
	if in.SIBase > 0 {
		e.SIBase = in.SIBase
	} else {
		e.SIBase = in.BaseSalary
	}
	if in.HFBBase > 0 {
		e.HFBBase = in.HFBBase
	} else {
		e.HFBBase = e.SIBase
	}

	if _, err := s.db.Payroll().UpsertEmployee(ctx, e); err != nil {
		return nil, err
	}

	s.recordAudit(ctx, AuditEvent{
		Action:   audit.ActionEmployeeSalary,
		Summary:  fmt.Sprintf("%s 的基本工资由 %s 调整为 %s", e.Name, oldBase, e.BaseSalary),
		Entity:   "employee",
		EntityID: fmt.Sprintf("%d", e.ID),
		Detail: map[string]any{
			"name": e.Name,
			"from": int64(oldBase), "to": int64(e.BaseSalary),
			"siFrom": int64(oldSI), "siTo": int64(e.SIBase),
			"reason": in.Reason,
		},
		Operator: in.Operator,
	})

	out := toEmployeeView(e)
	return &out, nil
}

// ---------------------------------------------------------------------------
// 内部：按 id 取员工 / 部门
// ---------------------------------------------------------------------------

// employeeByID 取员工；不存在时报一句人话。
//
// 员工列表本身是按「是否只取在职」过滤的，所以不能复用它来找人 ——
// 给一个已离职的人办转部门是很正常的场景。
func (s *Service) employeeByID(ctx context.Context, id int64) (*payroll.Employee, error) {
	if id <= 0 {
		return nil, errors.New("没有指定员工")
	}
	list, err := s.db.Payroll().ListEmployees(ctx, false)
	if err != nil {
		return nil, err
	}
	for _, e := range list {
		if e.ID == id {
			return e, nil
		}
	}
	return nil, fmt.Errorf("员工 id=%d 不存在（可能已经被删掉了）", id)
}

// enabledDepartment 取一个可用的部门。
func (s *Service) enabledDepartment(ctx context.Context, id int64) (*Department, error) {
	if id <= 0 {
		return nil, errors.New("请选择部门 —— " +
			"绝大多数费用科目按部门辅助核算，员工没有部门就记不了账")
	}
	list, err := s.Departments(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			if !list[i].Enabled {
				return nil, fmt.Errorf("部门「%s」已停用，不能把人调进去 —— "+
					"停用的部门在新建凭证时是选不到的，工资计提会卡在辅助核算上",
					list[i].FullName)
			}
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("部门 id=%d 不存在", id)
}

// departmentName 取部门全名；取不到时返回空串（调用方自己给兜底文案）。
func (s *Service) departmentName(ctx context.Context, id int64) (string, error) {
	list, err := s.Departments(ctx)
	if err != nil {
		return "", err
	}
	for i := range list {
		if list[i].ID == id {
			return list[i].FullName, nil
		}
	}
	return "", fmt.Errorf("部门 id=%d 不存在", id)
}
