package aiprovider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 人事异动：离职 / 转部门 / 调薪
// ---------------------------------------------------------------------------
//
// 与 propose_new_aux 同一条边界：模型只能**提议**，用户点确认才执行。
//
// 为什么不给它直接执行的工具：这三件事都会改历史。
// 工资争议里最常见的一问是「3 月把他从销售部调到生产部、工资从 8000
// 调到 9500，是谁什么时候办的」—— 一个能自己动手的模型，
// 这个问题的答案就变成了「AI 办的」，而 AI 不该是责任人。

// HRProposal 是「建议对某位员工做一次人事异动」。
type HRProposal struct {
	// Kind 是 resign | transfer | salary。
	Kind string `json:"kind"`
	// EmployeeID 是目标员工。
	EmployeeID int64 `json:"employee_id"`
	// EmployeeName 由服务层回填（模型只给 id）。
	EmployeeName string `json:"-"`
	// DeptID 是转入部门（transfer 用）。
	DeptID *int64 `json:"dept_id"`
	// DeptName 由服务层回填。
	DeptName string `json:"-"`
	// LeaveDate 是离职日期（resign 用）。
	LeaveDate string `json:"leave_date"`
	// BaseSalary / SIBase / HFBBase 是调薪后的金额（salary 用，单位元）。
	BaseSalary string `json:"base_salary"`
	SIBase     string `json:"si_base"`
	HFBBase    string `json:"hfb_base"`
	// Reason 是原因，会写进操作日志。
	Reason string `json:"reason"`
}

// 人事异动种类。
const (
	HRResign   = "resign"
	HRTransfer = "transfer"
	HRSalary   = "salary"
)

// Valid 报告提议是否完整。
func (p HRProposal) Valid() bool {
	if p.EmployeeID <= 0 {
		return false
	}
	switch p.Kind {
	case HRResign:
		return strings.TrimSpace(p.LeaveDate) != ""
	case HRTransfer:
		return p.DeptID != nil && *p.DeptID > 0
	case HRSalary:
		return strings.TrimSpace(p.BaseSalary) != ""
	default:
		return false
	}
}

// Label 返回中文名。
func (p HRProposal) Label() string {
	switch p.Kind {
	case HRResign:
		return "办理离职"
	case HRTransfer:
		return "转部门"
	case HRSalary:
		return "调薪"
	default:
		return p.Kind
	}
}

// ProposeHRActionTool 构造「提议人事异动」工具（终止型）。
func ProposeHRActionTool() Tool {
	return Tool{
		Name: "propose_hr_action",
		Description: "员工离职、转部门、调薪时用它提议，然后停下来等用户确认。" +
			"你**不能**直接改员工档案 —— 这三件事都会改历史，" +
			"操作人必须是人。employee_id 必须来自 search_employees。" +
			"resign 要 leave_date；transfer 要 dept_id（来自 search_departments）；" +
			"salary 要 base_salary（元，字符串）。reason 会写进操作日志。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "kind": {"type": "string", "enum": ["resign", "transfer", "salary"],
             "description": "离职 / 转部门 / 调薪"},
    "employee_id": {"type": "integer", "description": "员工 id，必须来自 search_employees"},
    "leave_date": {"type": "string", "description": "离职日期 YYYY-MM-DD（kind=resign 必填）"},
    "dept_id": {"type": "integer", "description": "调入部门 id（kind=transfer 必填）"},
    "base_salary": {"type": "string", "description": "调整后的月工资，单位元，如 9500.00（kind=salary 必填）"},
    "si_base": {"type": "string", "description": "社保基数，留空表示跟随工资"},
    "hfb_base": {"type": "string", "description": "公积金基数，留空表示跟随社保基数"},
    "reason": {"type": "string", "description": "原因，会写进操作日志。写具体，如「调往生产部任主管」"}
  },
  "required": ["kind", "employee_id", "reason"]
}`),
		Terminal: true,
		RenderAsk: func(args json.RawMessage) string {
			p, err := ParseHRProposal(string(args))
			if err != nil {
				return ""
			}
			return fmt.Sprintf("（提议%s：员工 #%d）%s", p.Label(), p.EmployeeID, p.Reason)
		},
	}
}

// ParseHRProposal 解析 propose_hr_action 的参数。
func ParseHRProposal(raw string) (*HRProposal, error) {
	var p HRProposal
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	p.Kind = strings.TrimSpace(p.Kind)
	p.Reason = strings.TrimSpace(p.Reason)
	if !p.Valid() {
		return nil, fmt.Errorf("人事异动提议不完整（kind=%q employee_id=%d）",
			p.Kind, p.EmployeeID)
	}
	return &p, nil
}
