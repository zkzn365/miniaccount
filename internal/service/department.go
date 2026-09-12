package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"miniaccount/internal/domain/audit"
)

// Department 是一个部门。
type Department struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
	// ParentID 指向上级部门；nil 表示顶层。
	ParentID *int64 `json:"parentId"`
	Enabled  bool   `json:"enabled"`
	Remark   string `json:"remark"`
	// FullName 含上级名称，如「生产中心—一车间」。
	FullName string `json:"fullName"`
}

// Departments 返回全部部门。
//
// 部门是「辅助核算四维」里唯一需要档案表的一维（见迁移 0008 的说明）。
// 没有它，用户只能凭空记住「1 是管理部门」，填错了也不报错。
func (s *Service) Departments(ctx context.Context) ([]Department, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, code, name, parent_id, is_enabled, remark
		  FROM department ORDER BY sort_order, code`)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var list []Department
	byID := map[int64]*Department{}
	for rows.Next() {
		var d Department
		var enabled int
		var parent sql.NullInt64
		if err := rows.Scan(&d.ID, &d.Code, &d.Name, &parent, &enabled, &d.Remark); err != nil {
			return nil, err
		}
		d.Enabled = enabled != 0
		if parent.Valid {
			v := parent.Int64
			d.ParentID = &v
		}
		list = append(list, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range list {
		byID[list[i].ID] = &list[i]
	}
	// 拼全名：报表与凭证上显示「生产中心—一车间」比「一车间」清楚
	for i := range list {
		name := list[i].Name
		seen := map[int64]bool{list[i].ID: true}
		cur := list[i].ParentID
		for cur != nil && !seen[*cur] {
			p, ok := byID[*cur]
			if !ok {
				break
			}
			seen[*cur] = true
			name = p.Name + "—" + name
			cur = p.ParentID
		}
		list[i].FullName = name
	}
	return list, nil
}

// SaveDepartment 新增或修改部门。
func (s *Service) SaveDepartment(ctx context.Context, d Department) (int64, error) {
	name := strings.TrimSpace(d.Name)
	if name == "" {
		return 0, fmt.Errorf("部门名称不能为空")
	}
	code := strings.TrimSpace(d.Code)
	if code == "" {
		// 没给编码时用名称兜底 —— 部门编码对小微企业没有实际意义，
		// 强制要求填只会增加录入负担
		code = name
	}

	var id int64
	err := s.db.WithTx(ctx, func(tx *sqliteTx) error {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if d.ID == 0 {
			res, err := tx.Exec(ctx, `
				INSERT INTO department (code, name, parent_id, is_enabled, remark,
					sort_order, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?)`,
				code, name, nullI64(d.ParentID), boolI(d.Enabled),
				d.Remark, 100, now, now)
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		}
		res, err := tx.Exec(ctx, `
			UPDATE department SET code = ?, name = ?, parent_id = ?, is_enabled = ?,
				remark = ?, updated_at = ? WHERE id = ?`,
			code, name, nullI64(d.ParentID), boolI(d.Enabled),
			d.Remark, now, d.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("部门 id=%d 不存在", d.ID)
		}
		id = d.ID
		return nil
	})
	return id, err
}

// ---------------------------------------------------------------------------
// 删除部门
// ---------------------------------------------------------------------------

// DepartmentUsage 报告一个部门被哪些地方引用。
//
// ★ 部门在库里是**自由整数**（ledger_entry.dept_id、employee.dept_id…），
// 没有外键兜着。所以删之前必须自己数一遍 —— 数据库不会拦你，
// 删完之后的后果是：去年的部门费用表里出现「部门#3」，
// 而没有人知道那是谁。
type DepartmentUsage struct {
	// Employees 是挂在它下面的员工（在职与离职都算）。
	Employees int `json:"employees"`
	// Children 是下级部门。
	Children int `json:"children"`
	// Entries 是已记账的凭证明细。
	Entries int `json:"entries"`
	// BankFlows 是银行流水上指定的建议部门。
	BankFlows int `json:"bankFlows"`
	// BankRules 是按部门自动归集的银行规则。
	BankRules int `json:"bankRules"`
	// Claims 是报销单上填的部门。
	//
	// 发票不在这里：invoice 表本身没有部门列（部门记在报销单上，
	// 发票只挂金额与税额）。硬加一条会查到「no such column」。
	Claims int `json:"claims"`
}

// Total 返回引用总数。
func (u DepartmentUsage) Total() int {
	return u.Employees + u.Children + u.Entries + u.BankFlows +
		u.BankRules + u.Claims
}

// Describe 生成一句人话，说明为什么删不掉、以及该怎么办。
func (u DepartmentUsage) Describe() string {
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d 处", what, n))
		}
	}
	add(u.Employees, "员工")
	add(u.Children, "下级部门")
	add(u.Entries, "已记账的凭证明细")
	add(u.BankFlows, "银行流水")
	add(u.BankRules, "银行规则")
	add(u.Claims, "报销单")
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "、")
}

// DepartmentUsageOf 统计一个部门被引用的次数。
//
// 导出是为了让界面在**点删除之前**就能把话说清楚 ——
// 「这个部门下还有 3 个员工」远比点完再报错有用。
func (s *Service) DepartmentUsageOf(ctx context.Context, id int64) (DepartmentUsage, error) {
	var u DepartmentUsage
	qs := []struct {
		sql string
		out *int
	}{
		{`SELECT COUNT(*) FROM employee WHERE dept_id = ?`, &u.Employees},
		{`SELECT COUNT(*) FROM department WHERE parent_id = ?`, &u.Children},
		{`SELECT COUNT(*) FROM ledger_entry WHERE dept_id = ?`, &u.Entries},
		{`SELECT COUNT(*) FROM bank_flow WHERE dept_id = ?`, &u.BankFlows},
		{`SELECT COUNT(*) FROM bank_rule WHERE dept_id = ?`, &u.BankRules},
		{`SELECT COUNT(*) FROM expense_claim WHERE dept_id = ?`, &u.Claims},
	}
	for _, q := range qs {
		if err := s.db.SQL().QueryRowContext(ctx, q.sql, id).Scan(q.out); err != nil {
			return u, translate(err)
		}
	}
	return u, nil
}

// DeleteDepartment 删除一个部门。
//
// # 为什么被引用时**拒绝删除**，而不是级联清掉或改成停用
//
//   - 级联清掉：会把已经记进账的凭证上的部门抹掉。账是会计档案，
//     删部门不该动到去年的部门费用表。
//   - 悄悄改成停用：用户点的是「删除」，得到的却是「停用」，
//     列表里那一行还在 —— 他会以为没删掉，然后再点一次。
//
// 所以这里明确拒绝，并说清楚被什么挡住了、以及**想停用该怎么做**。
// 停用本身是有意义的操作（部门撤了但历史要留），界面上有单独的开关。
func (s *Service) DeleteDepartment(ctx context.Context, id int64) error {
	list, err := s.Departments(ctx)
	if err != nil {
		return err
	}
	var target *Department
	for i := range list {
		if list[i].ID == id {
			target = &list[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("部门 id=%d 不存在（可能已经被删掉了）", id)
	}

	u, err := s.DepartmentUsageOf(ctx, id)
	if err != nil {
		return err
	}
	if u.Total() > 0 {
		return fmt.Errorf(
			"「%s」还在被使用，不能删除：%s。\n"+
				"已经记进账的凭证不会因为删部门而改变，所以不能连它一起清掉。\n"+
				"如果这个部门只是撤了、历史还要留，请改用「停用」——"+
				"停用之后新建凭证时选不到它，历史报表照旧。",
			target.FullName, u.Describe())
	}

	res, err := s.db.SQL().ExecContext(ctx,
		`DELETE FROM department WHERE id = ?`, id)
	if err != nil {
		return translate(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("部门 id=%d 不存在", id)
	}

	s.recordAudit(ctx, AuditEvent{
		Action:   audit.ActionDepartmentDelete,
		Summary:  fmt.Sprintf("删除部门「%s」", target.FullName),
		Entity:   "department",
		EntityID: fmt.Sprintf("%d", id),
		Detail:   map[string]any{"code": target.Code, "name": target.Name},
	})
	return nil
}
