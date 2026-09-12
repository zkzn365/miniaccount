package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/money"
)

// AccountRepo 是科目的持久化访问。
type AccountRepo struct{ db *DB }

// Accounts 返回科目仓储。
func (db *DB) Accounts() *AccountRepo { return &AccountRepo{db: db} }

// accountColumns 集中列出列名，避免各处 select * 后在 Scan 时错位。
const accountColumns = `id, code, name, parent_id, level, is_leaf, root_type,
	category, balance_dir, aux_types, is_enabled, is_preset, remark, sort_order`

// List 返回全部科目，按编码升序。
func (r *AccountRepo) List(ctx context.Context) ([]*account.Account, error) {
	return r.query(ctx, r.db.sql, `SELECT `+accountColumns+` FROM account ORDER BY code`)
}

// ListEnabled 返回启用中的科目。
func (r *AccountRepo) ListEnabled(ctx context.Context) ([]*account.Account, error) {
	return r.query(ctx, r.db.sql,
		`SELECT `+accountColumns+` FROM account WHERE is_enabled = 1 ORDER BY code`)
}

// ListLeaves 返回可记账的明细科目（启用中）。
func (r *AccountRepo) ListLeaves(ctx context.Context) ([]*account.Account, error) {
	return r.query(ctx, r.db.sql,
		`SELECT `+accountColumns+` FROM account
		  WHERE is_enabled = 1 AND is_leaf = 1 ORDER BY code`)
}

// GetByCode 按编码取科目。
func (r *AccountRepo) GetByCode(ctx context.Context, code string) (*account.Account, error) {
	rows, err := r.query(ctx, r.db.sql,
		`SELECT `+accountColumns+` FROM account WHERE code = ?`, code)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: 科目 %q", ErrNotFound, code)
	}
	return rows[0], nil
}

// Tree 加载全部科目并构建经过校验的科目树。
//
// 构建失败通常意味着数据被外部改坏（例如手工 UPDATE 造成父子编码不一致）。
// 此时**必须报错而不是静默忽略** —— 科目树错了，后面所有报表都是错的。
func (r *AccountRepo) Tree(ctx context.Context) (*account.Tree, error) {
	accts, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(accts) == 0 {
		return nil, fmt.Errorf("%w: 账套尚未初始化科目表", ErrNotFound)
	}
	return account.NewTree(accts)
}

// TreeFrom 在给定查询器（通常是事务）上加载科目树。
//
// 过账时必须在**同一事务内**加载，才能保证校验看到的是最新状态 ——
// 例如用户刚新增了一个子科目使某科目变成汇总科目。
func (r *AccountRepo) TreeFrom(ctx context.Context, tx *Tx) (*account.Tree, error) {
	accts, err := r.queryTx(ctx, tx, `SELECT `+accountColumns+` FROM account ORDER BY code`)
	if err != nil {
		return nil, err
	}
	if len(accts) == 0 {
		return nil, fmt.Errorf("%w: 账套尚未初始化科目表", ErrNotFound)
	}
	return account.NewTree(accts)
}

// IDsByCode 返回 编码 → id 的映射，过账时用来把编码换成外键。
func (r *AccountRepo) IDsByCode(ctx context.Context) (map[string]int64, error) {
	return r.idsByCodeQ(ctx, r.db.sql)
}

// IDsByCodeTx 在事务内查询 编码 → id 映射。
//
// 银行流水批量过账必须用事务内的版本：一个批次里的科目映射
// 必须与其他写入看到同一个快照。
func (r *AccountRepo) IDsByCodeTx(ctx context.Context, tx *Tx) (map[string]int64, error) {
	rows, err := tx.Query(ctx, `SELECT code, id FROM account`)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

func (r *AccountRepo) idsByCodeQ(ctx context.Context, q querier) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT code, id FROM account`)
	if err != nil {
		return nil, translateErr(err)
	}
	return scanIDs(rows)
}

func scanIDs(rows *sql.Rows) (map[string]int64, error) {
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var code string
		var id int64
		if err := rows.Scan(&code, &id); err != nil {
			return nil, err
		}
		out[code] = id
	}
	return out, rows.Err()
}

// Insert 新增一个科目（账套初始化之外的日常维护）。
func (r *AccountRepo) Insert(ctx context.Context, tx *Tx, a *account.Account) (int64, error) {
	var parentID any
	if a.ParentCode != "" {
		var pid int64
		err := tx.QueryRow(ctx, `SELECT id FROM account WHERE code = ?`, a.ParentCode).Scan(&pid)
		if err != nil {
			if err == sql.ErrNoRows {
				return 0, fmt.Errorf("%w: 父科目 %q", ErrNotFound, a.ParentCode)
			}
			return 0, translateErr(err)
		}
		parentID = pid
	}
	res, err := tx.Exec(ctx, `
		INSERT INTO account (code, name, parent_id, level, is_leaf, root_type,
			category, balance_dir, aux_types, is_enabled, is_preset, remark, sort_order)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.Code, a.Name, parentID, a.Level, boolInt(a.IsLeaf), string(a.RootType),
		string(a.Category()), string(a.BalanceDir), joinAux(a.AuxTypes),
		boolInt(a.IsEnabled), boolInt(a.IsPreset), a.Remark, a.SortOrder)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetEnabled 启用/停用科目。
func (r *AccountRepo) SetEnabled(ctx context.Context, tx *Tx, code string, enabled bool) error {
	res, err := tx.Exec(ctx, `UPDATE account SET is_enabled = ? WHERE code = ?`,
		boolInt(enabled), code)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: 科目 %q", ErrNotFound, code)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

// queryTx 在事务上执行科目查询，供过账校验使用。
func (r *AccountRepo) queryTx(ctx context.Context, tx *Tx, sqlText string, args ...any) ([]*account.Account, error) {
	rows, err := tx.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	return r.scanAccounts(rows)
}

func (r *AccountRepo) query(ctx context.Context, q querier, sqlText string, args ...any) ([]*account.Account, error) {
	rows, err := q.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	return r.scanAccounts(rows)
}

func (r *AccountRepo) scanAccounts(rows *sql.Rows) ([]*account.Account, error) {
	defer rows.Close()

	var out []*account.Account
	for rows.Next() {
		a := &account.Account{}
		var (
			parentID sql.NullInt64
			aux      string
			root     string
			cat      string
			dir      string
			isLeaf   int
			isEnab   int
			isPreset int
		)
		if err := rows.Scan(&a.ID, &a.Code, &a.Name, &parentID, &a.Level,
			&isLeaf, &root, &cat, &dir, &aux, &isEnab, &isPreset,
			&a.Remark, &a.SortOrder); err != nil {
			return nil, err
		}
		a.IsLeaf = isLeaf != 0
		a.IsEnabled = isEnab != 0
		a.IsPreset = isPreset != 0
		a.RootType = account.RootType(root)
		a.BalanceDir = account.BalanceDir(dir)
		a.AuxTypes = splitAux(aux)
		if parentID.Valid {
			a.ParentID = &parentID.Int64
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 回填 ParentCode 并校验树结构
	byID := make(map[int64]string, len(out))
	for _, a := range out {
		byID[a.ID] = a.Code
	}
	for _, a := range out {
		if a.ParentID != nil {
			code, ok := byID[*a.ParentID]
			if !ok {
				return nil, fmt.Errorf("%w: 科目 %s 的父科目 id=%d",
					ErrNotFound, a.Code, *a.ParentID)
			}
			a.ParentCode = code
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 辅助核算的序列化
// ---------------------------------------------------------------------------

// joinAux 把辅助核算维度序列化为逗号分隔的字符串。
func joinAux(ts []account.AuxType) string {
	if len(ts) == 0 {
		return ""
	}
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = string(t)
	}
	return strings.Join(parts, ",")
}

// splitAux 解析辅助核算维度。同时容忍旧数据里的分号分隔。
func splitAux(s string) []account.AuxType {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' })
	out := make([]account.AuxType, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		out = append(out, account.AuxType(f))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ContactRepo 是往来档案的持久化访问。
type ContactRepo struct{ db *DB }

// Contacts 返回往来档案仓储。
func (db *DB) Contacts() *ContactRepo { return &ContactRepo{db: db} }

// Kinds 返回 往来单位 id → kind 的映射。
//
// 过账校验需要它来判断「科目要求客户，但这笔挂的是供应商」这类错误。
func (r *ContactRepo) Kinds(ctx context.Context) (map[int64]string, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT id, kind FROM contact`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var kind string
		if err := rows.Scan(&id, &kind); err != nil {
			return nil, err
		}
		out[id] = kind
	}
	return out, rows.Err()
}

// KindsFrom 在给定查询器上加载 往来单位 id → kind 映射。
func (r *ContactRepo) KindsFrom(ctx context.Context, tx *Tx) (map[int64]string, error) {
	rows, err := tx.Query(ctx, `SELECT id, kind FROM contact`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var kind string
		if err := rows.Scan(&id, &kind); err != nil {
			return nil, err
		}
		out[id] = kind
	}
	return out, rows.Err()
}

// AddContact 在一个事务内新增往来单位。
//
// 与 Insert 的区别：Insert 要求调用方自己提供事务（供需要多步原子性的
// 场景复用），AddContact 自己开事务，供「就加一个客户」这类单步操作使用。
// 少了它，每个调用点都要写一遍 WithTx 样板，而写漏一次就会得到一个
// 没有事务保护的写操作。
func (r *ContactRepo) AddContact(ctx context.Context, kind, name string) (int64, error) {
	if !validContactKind(kind) {
		return 0, fmt.Errorf("%w: 往来单位类型 %q", ErrBadContactKind, kind)
	}
	if strings.TrimSpace(name) == "" {
		return 0, fmt.Errorf("%w: 往来单位名称不能为空", ErrBadContactKind)
	}
	var id int64
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		var e error
		id, e = r.Insert(ctx, tx, kind, name)
		return e
	})
	return id, err
}

// validContactKind 校验往来单位类型。
func validContactKind(kind string) bool {
	switch kind {
	case "customer", "supplier", "both", "employee", "shareholder", "other":
		return true
	default:
		return false
	}
}

// Insert 新增往来单位。测试与初始化用。
func (r *ContactRepo) Insert(ctx context.Context, tx *Tx, kind, name string) (int64, error) {
	res, err := tx.Exec(ctx, `
		INSERT INTO contact (code, kind, name, created_at, updated_at)
		VALUES (?,?,?,?,?)`, nil, kind, name, nowString(), nowString())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// 编译期断言：确保 querier 约束被满足，避免接口漂移。
var (
	_ querier = (*sql.DB)(nil)
	_ querier = (*sql.Tx)(nil)
	_         = money.Zero
)
