package aiprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 辅助核算档案：部门 / 员工
// ---------------------------------------------------------------------------
//
// # 为什么补这两个工具
//
// 这是一次真实事故换来的。用户说「误餐费 325，员工垫付现金报销」，
// 模型给的分录是「借 560204 管理费用—职工福利费 / 贷 1001 库存现金」——
// 会计上完全正确，但 560204 要求**部门**辅助核算，而模型没填，
// 被护栏打回：缺少必需的辅助核算。
//
// 翻回去看，模型不是不听话，是**它根本没有办法知道有哪些部门**：
// 工具有搜科目、搜往来单位、搜历史凭证，唯独没有搜部门、搜员工。
// 于是它既查不到，也没法带着真实选项问用户，只能空着交差。
//
// 提示词里写着「能从账套里查到的不要问用户」，而它查不到 —— 这条规则
// 反而把它堵死了。补上工具，这条路才通。

// DepartmentBrief 是一个部门（给模型看的精简形状）。
type DepartmentBrief struct {
	ID   int64
	Code string
	// Name 是部门名。
	Name string
	// FullName 含上级，如「销售部/华东区」。
	FullName string
}

// DepartmentBriefProvider 提供部门清单。
type DepartmentBriefProvider interface {
	Departments(ctx context.Context) ([]DepartmentBrief, error)
}

// EmployeeBrief 是一名员工。
type EmployeeBrief struct {
	ID   int64
	Code string
	Name string
	// DeptName 是所属部门名（可能为空）。
	DeptName string
	// Left 为真表示已离职。
	Left bool
}

// EmployeeBriefProvider 提供员工清单。
type EmployeeBriefProvider interface {
	Employees(ctx context.Context) ([]EmployeeBrief, error)
}

// SearchDepartmentsTool 构造「按关键词搜部门」工具。
func SearchDepartmentsTool(p DepartmentBriefProvider) Tool {
	return Tool{
		Name: "search_departments",
		Description: "列出或搜索本账套的部门，返回 department_id 与名称。" +
			"当科目要求「部门」辅助核算时，**必须**先用它查到 id —— " +
			"查不到就问用户，账套里一个都没有才提议新建，" +
			"不要因为缺部门就换一个不需要部门的科目去记账。" +
			"**只能使用它返回的 id**。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"keyword": {"type": "string", "description": "部门名称或编码的片段，留空返回全部"}
			},
			"required": ["keyword"]
		}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct{ Keyword string }
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("参数格式错误: %w", err)
			}
			all, err := p.Departments(ctx)
			if err != nil {
				return "", err
			}
			kw := strings.ToLower(strings.TrimSpace(args.Keyword))
			var b strings.Builder
			n := 0
			for _, d := range all {
				name := d.FullName
				if name == "" {
					name = d.Name
				}
				if kw != "" && !strings.Contains(strings.ToLower(d.Code+" "+name), kw) {
					continue
				}
				fmt.Fprintf(&b, "%d %s\n", d.ID, name)
				n++
			}
			if n == 0 {
				return emptyAuxHint("部门", len(all)), nil
			}
			return b.String(), nil
		},
	}
}

// SearchEmployeesTool 构造「按关键词搜员工」工具。
func SearchEmployeesTool(p EmployeeBriefProvider) Tool {
	return Tool{
		Name: "search_employees",
		Description: "列出或搜索本账套的员工，返回 employee_id、姓名、所属部门与在职状态。" +
			"当科目要求「员工」辅助核算（如应付职工薪酬、其他应收款—员工）时，" +
			"**必须**先用它查到 id。默认只有在职的；要连离职的一起看就传 include_left=true。" +
			"**只能使用它返回的 id**。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"keyword": {"type": "string", "description": "姓名、工号或部门的片段，留空返回全部"},
				"include_left": {"type": "boolean", "description": "是否包含已离职员工，默认 false"}
			},
			"required": ["keyword"]
		}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Keyword     string `json:"keyword"`
				IncludeLeft bool   `json:"include_left"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("参数格式错误: %w", err)
			}
			all, err := p.Employees(ctx)
			if err != nil {
				return "", err
			}
			kw := strings.ToLower(strings.TrimSpace(args.Keyword))
			var b strings.Builder
			n := 0
			for _, e := range all {
				if e.Left && !args.IncludeLeft {
					continue
				}
				if kw != "" && !strings.Contains(strings.ToLower(e.Code+" "+e.Name+" "+e.DeptName), kw) {
					continue
				}
				fmt.Fprintf(&b, "%d %s", e.ID, e.Name)
				if e.Code != "" {
					fmt.Fprintf(&b, "（工号 %s）", e.Code)
				}
				if e.DeptName != "" {
					fmt.Fprintf(&b, " 部门：%s", e.DeptName)
				}
				if e.Left {
					b.WriteString(" 已离职")
				}
				b.WriteByte('\n')
				n++
			}
			if n == 0 {
				return emptyAuxHint("员工", len(all)), nil
			}
			return b.String(), nil
		},
	}
}

// emptyAuxHint 是「一条都没有」时给模型的话。
//
// ★ 这句话必须明确告诉它**下一步做什么**。
// 只说「没有匹配」的话，模型最常见的反应是换一个近似的东西硬凑 ——
// 比如缺部门就改用不需要部门的科目，费用挂错地方，而报表照样平。
func emptyAuxHint(what string, total int) string {
	if total == 0 {
		return fmt.Sprintf("账套里还没有任何%s档案。\n"+
			"不要因此换一个不需要该辅助核算的科目 —— 那会把费用挂错地方，"+
			"而报表照样是平的，看不出错。\n"+
			"用 propose_new_aux 提议新建一个，由用户确认。", what)
	}
	return fmt.Sprintf("没有匹配的%s。\n"+
		"请用不带关键词的调用列出全部%s，然后问用户是哪一个（把查到的都列成选项）；"+
		"确实一个都不合适时，用 propose_new_aux 提议新建。", what, what)
}

// ---------------------------------------------------------------------------
// 提议新建档案（需要用户确认）
// ---------------------------------------------------------------------------

// AuxProposal 是「建议新建一条辅助核算档案」。
//
// ★ 模型只能**提议**，不能自己建。
//
// 这条边界与「AI 产物一律先是草稿」是同一条：写账套的动作必须由人按下去。
// 让模型直接建部门，最坏的后果不是「建错一个部门」—— 而是它开始
// 「顺手把缺的东西都补齐」，而补齐的规则是你没审过的。
type AuxProposal struct {
	// Kind 是 department | employee。
	Kind string `json:"kind"`
	// Name 是档案名称。
	Name string `json:"name"`
	// Code 是编码，可空。
	Code string `json:"code"`
	// DeptID 是员工所属部门（仅 employee 用），可空。
	//
	// ★ 标签是 snake_case：这个结构体解析的是**模型给的工具参数**，
	// 与凭证 JSON（account_code、biz_date）同一套约定。
	// 发给前端的 service.AuxProposalView 才用 camelCase。
	DeptID *int64 `json:"dept_id"`
	// Reason 是为什么要建 —— 会显示给用户，也进审计。
	Reason string `json:"reason"`
}

// 可新建的档案类型。
const (
	AuxKindDepartment = "department"
	AuxKindEmployee   = "employee"
)

// Valid 报告类型是否支持。
func (p AuxProposal) Valid() bool {
	if strings.TrimSpace(p.Name) == "" {
		return false
	}
	return p.Kind == AuxKindDepartment || p.Kind == AuxKindEmployee
}

// Label 返回中文名。
func (p AuxProposal) Label() string {
	if p.Kind == AuxKindEmployee {
		return "员工"
	}
	return "部门"
}

// ProposeNewAuxTool 构造「提议新建辅助核算档案」工具（终止型）。
//
// ★ 终止型：调用它就结束这一轮，把提议交给用户确认。
// 用户点确认之后，由**界面**去调建档的绑定（那里有完整的审计与校验），
// 模型全程没有写库的能力。
func ProposeNewAuxTool() Tool {
	return Tool{
		Name: "propose_new_aux",
		Description: "账套里缺少某个辅助核算档案（部门 / 员工）时，用它提议新建一个，" +
			"然后停下来等用户确认。只用于**确实缺失**的情况 —— " +
			"已经存在的先用 search_departments / search_employees 查到并用它的 id。" +
			"reason 要写清楚为什么必须建（比如「560204 要求部门辅助核算」）。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "kind": {"type": "string", "enum": ["department", "employee"],
             "description": "要新建的是部门还是员工"},
    "name": {"type": "string", "description": "名称，如「生产部」「张三」"},
    "code": {"type": "string", "description": "编码，可留空（留空由系统按名称生成）"},
    "dept_id": {"type": "integer", "description": "员工所属部门的 id（仅员工需要，可留空）"},
    "reason": {"type": "string", "description": "为什么必须建这个档案，一句话。会显示给用户并记进审计"}
  },
  "required": ["kind", "name", "reason"]
}`),
		Terminal: true,
		RenderAsk: func(args json.RawMessage) string {
			p, err := ParseAuxProposal(string(args))
			if err != nil {
				return ""
			}
			return fmt.Sprintf("（提议新建%s「%s」）%s", p.Label(), p.Name, p.Reason)
		},
	}
}

// ParseAuxProposal 解析 propose_new_aux 的参数。
func ParseAuxProposal(raw string) (*AuxProposal, error) {
	var p AuxProposal
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	p.Kind = strings.TrimSpace(p.Kind)
	p.Name = strings.TrimSpace(p.Name)
	p.Code = strings.TrimSpace(p.Code)
	p.Reason = strings.TrimSpace(p.Reason)
	if !p.Valid() {
		return nil, fmt.Errorf("档案提议不完整（kind=%q name=%q）", p.Kind, p.Name)
	}
	return &p, nil
}
