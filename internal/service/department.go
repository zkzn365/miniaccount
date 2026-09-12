package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
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
