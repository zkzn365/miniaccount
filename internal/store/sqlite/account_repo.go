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

// txQuerier 把 *Tx 适配成 rowQuerier。
type txQuerier struct{ tx *Tx }

func (t txQuerier) QueryRowContext(ctx context.Context, query string,
	args ...any) *sql.Row {
	// Tx.QueryRow 返回的是同一类 *sql.Row；这里用底层 q
	return t.tx.q.QueryRowContext(ctx, query, args...)
}

// queryTx 在事务上执行科目查询，供过账校验使用。
func (r *AccountRepo) queryTx(ctx context.Context, tx *Tx, sqlText string, args ...any) ([]*account.Account, error) {
	rows, err := tx.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	// Tx 上没有 QueryRowContext：包一层
	return r.scanAccounts(ctx, txQuerier{tx}, rows)
}

func (r *AccountRepo) query(ctx context.Context, q querier, sqlText string, args ...any) ([]*account.Account, error) {
	rows, err := q.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	return r.scanAccounts(ctx, q, rows)
}

// rowQuerier 是「能查一行」的最小接口。
//
// 不直接用 querier：*Tx 的方法名是 Query/QueryRow，与 sql.DB 的
// QueryContext/QueryRowContext 不同名 —— 为了一个补查去给 Tx 加一套
// 别名方法不值当。接口要按**用到的能力**定义，不是按实现者有什么。
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (r *AccountRepo) scanAccounts(ctx context.Context, q rowQuerier,
	rows *sql.Rows) ([]*account.Account, error) {

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
	// ★ 先关游标再补查询。
	//
	// 连接池是 MaxOpenConns(1)：游标还开着的时候再发一条查询会**永久等锁**，
	// 表现为整个程序卡死（不是报错）。这个坑本项目踩过一次了。
	if err := rows.Close(); err != nil {
		return nil, err
	}

	// 回填 ParentCode
	byID := make(map[int64]string, len(out))
	for _, a := range out {
		byID[a.ID] = a.Code
	}
	for _, a := range out {
		if a.ParentID == nil {
			continue
		}
		if code, ok := byID[*a.ParentID]; ok {
			a.ParentCode = code
			continue
		}
		// ★ 单行查询（GetByCode / ByPrefix）时父科目不在结果集里 ——
		// 这不是「科目不存在」，只是「这次没查它」。
		//
		// 原来这里直接报 ErrNotFound，于是**任何一个有上级的科目都查不出来**：
		// 按编码取科目一律失败，调用方还以为是「没这个科目」，
		// 转头去新建，最后撞在唯一约束上。
		var code string
		err := q.QueryRowContext(ctx,
			`SELECT code FROM account WHERE id = ?`, *a.ParentID).Scan(&code)
		if err != nil {
			return nil, fmt.Errorf("%w: 科目 %s 的父科目 id=%d 不存在（科目树被改坏了）",
				ErrNotFound, a.Code, *a.ParentID)
		}
		a.ParentCode = code
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

// ---------------------------------------------------------------------------
// 科目使用情况
// ---------------------------------------------------------------------------

// AccountUsage 是一个科目被用过多少、还剩多少。
//
// ★ 「能不能删」完全取决于它：被分录引用过的科目删掉，
// 历史凭证就会指向一个不存在的科目 —— 报表凭空少一块，且极难排查。
type AccountUsage struct {
	// Entries 是引用过这个科目的分录行数。
	//
	// ★ 统计的是 voucher_entry（**所有凭证**，含草稿），不是 ledger_entry。
	//
	// 踩过一次：原来只数 ledger_entry，而草稿凭证不写总账 ——
	// 于是「这个科目还没被用过，可以删」，实际 voucher_entry 里有引用，
	// DELETE 直接撞外键约束，用户看到的是一句数据库错误。
	// 判断「能不能删」要看**所有引用**，不只是已过账的那部分。
	Entries int `json:"entries"`
	// Balance 是当前余额（分，借贷相抵后的净值）。
	Balance money.Money `json:"balance"`
	// Children 是下级科目数。
	Children int `json:"children"`
	// AuditLines 是审计调整分录里引用这个科目的行数。
	AuditLines int `json:"auditLines"`
}

// UsageOf 查单个科目的使用情况。
//
// ★ AuditLines 是**审计调整的分录行数**，按科目编码统计（调整分录存的是
// code 而不是 id）。漏掉这一项会让「删科目」在底稿上留下一个洞：
// 调整分录还在、科目的余额表行没了，审定表静默少一行、借贷不平 ——
// 而底稿上看不出任何异常（发布前审计实测）。
func (r *AccountRepo) UsageOf(ctx context.Context, id int64) (AccountUsage, error) {
	var u AccountUsage
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM voucher_entry WHERE account_id = ?),
		  (SELECT COALESCE(SUM(debit - credit), 0) FROM ledger_entry WHERE account_id = ?),
		  (SELECT COUNT(*) FROM account WHERE parent_id = ?),
		  (SELECT COUNT(*) FROM audit_adjustment_line
		    WHERE account_code = (SELECT code FROM account WHERE id = ?))`,
		id, id, id, id).Scan(&u.Entries, &u.Balance, &u.Children, &u.AuditLines)
	if err != nil {
		return u, translateErr(err)
	}
	return u, nil
}

// Usage 一次查出全部科目的使用情况（科目管理页要整表显示）。
//
// 用三条聚合查询再拼，而不是每行一次子查询：190 个科目 × 3 次查询
// 在界面打开时会明显卡一下，而这是每次进页面都要跑的。
func (r *AccountRepo) Usage(ctx context.Context) (map[int64]AccountUsage, error) {
	out := map[int64]AccountUsage{}

	// 引用数取自 voucher_entry（含草稿）；余额取自 ledger_entry（只算已过账）
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT account_id, COUNT(*) FROM voucher_entry GROUP BY account_id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		u := out[id]
		u.Entries = n
		out[id] = u
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	// 余额：只算已过账（草稿不进总账）
	rowsBal, err := r.db.sql.QueryContext(ctx, `
		SELECT account_id, COALESCE(SUM(debit - credit), 0)
		  FROM ledger_entry GROUP BY account_id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rowsBal.Close()
	for rowsBal.Next() {
		var id int64
		var bal money.Money
		if err := rowsBal.Scan(&id, &bal); err != nil {
			return nil, err
		}
		u := out[id]
		u.Balance = bal
		out[id] = u
	}
	if err := rowsBal.Err(); err != nil {
		return nil, err
	}

	// 下级数：父科目计数
	rows2, err := r.db.sql.QueryContext(ctx, `
		SELECT parent_id, COUNT(*) FROM account
		 WHERE parent_id IS NOT NULL GROUP BY parent_id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var id int64
		var n int
		if err := rows2.Scan(&id, &n); err != nil {
			return nil, err
		}
		u := out[id]
		u.Children = n
		out[id] = u
	}
	if err := rows2.Err(); err != nil {
		return nil, err
	}

	// 审计调整分录的引用（按科目编码）：科目管理页也要能看出
	// 哪些科目被底稿用着，否则删除按钮是亮的，点下去才发现删不掉。
	rows3, err := r.db.sql.QueryContext(ctx,
		`SELECT account_code, COUNT(*) FROM audit_adjustment_line GROUP BY account_code`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows3.Close()
	byCode := map[string]int{}
	for rows3.Next() {
		var code string
		var n int
		if err := rows3.Scan(&code, &n); err != nil {
			return nil, err
		}
		byCode[code] = n
	}
	if err := rows3.Err(); err != nil {
		return nil, err
	}
	if len(byCode) > 0 {
		rows4, err := r.db.sql.QueryContext(ctx, `SELECT id, code FROM account`)
		if err != nil {
			return nil, translateErr(err)
		}
		defer rows4.Close()
		for rows4.Next() {
			var id int64
			var code string
			if err := rows4.Scan(&id, &code); err != nil {
				return nil, err
			}
			if n := byCode[code]; n > 0 {
				u := out[id]
				u.AuditLines = n
				out[id] = u
			}
		}
		if err := rows4.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SetLeaf 改「明细/汇总」属性（在明细科目下加子科目时，父科目要变成汇总）。
func (r *AccountRepo) SetLeaf(ctx context.Context, tx *Tx, code string, leaf bool) error {
	res, err := tx.Exec(ctx, `UPDATE account SET is_leaf = ? WHERE code = ?`,
		boolInt(leaf), code)
	if err != nil {
		return translateErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("科目 %s 不存在", code)
	}
	return nil
}

// UpdateMeta 改科目的可变部分（名称、备注、辅助核算、明细/汇总）。
//
// ★ 只能改这些。编码、父科目、余额方向、大类一律不动 ——
// 那些是账已经记过的依据，改了历史就对不上。
func (r *AccountRepo) UpdateMeta(ctx context.Context, code, name, remark string,
	aux []account.AuxType, leaf bool) error {

	return r.db.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE account SET name = ?, remark = ?, aux_types = ?, is_leaf = ?
			 WHERE code = ?`,
			name, remark, encodeAuxTypes(aux), boolInt(leaf), code)
		if err != nil {
			return translateErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("科目 %s 不存在", code)
		}
		return nil
	})
}

// Delete 删除科目（调用方必须已确认它从未被用过）。
func (r *AccountRepo) Delete(ctx context.Context, id int64) error {
	return r.db.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.Exec(ctx, `DELETE FROM account WHERE id = ?`, id)
		if err != nil {
			return translateErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("科目不存在")
		}
		return nil
	})
}

// encodeAuxTypes 把辅助核算维度编码成库里存的形式。
func encodeAuxTypes(aux []account.AuxType) string {
	parts := make([]string, 0, len(aux))
	for _, a := range aux {
		parts = append(parts, string(a))
	}
	return strings.Join(parts, ",")
}
