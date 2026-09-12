package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"miniaccount/internal/domain/asset"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

// AssetRepo 是固定资产与费用摊销的持久化访问。
type AssetRepo struct{ db *DB }

// Assets 返回固定资产 / 费用摊销仓储。
func (db *DB) Assets() *AssetRepo { return &AssetRepo{db: db} }

const assetColumns = `id, code, name, category, dept_id, orig_value, salvage_ppm,
	useful_months, start_date, expense_account, accum_account, status,
	disposed_date, remark`

// ---------------------------------------------------------------------------
// 固定资产
// ---------------------------------------------------------------------------

// SaveAsset 新增或修改一张固定资产卡片。
func (r *AssetRepo) SaveAsset(ctx context.Context, a asset.FixedAsset) (int64, error) {
	if err := a.Validate(); err != nil {
		return 0, err
	}
	var id int64
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		now := nowString()
		if a.ID == 0 {
			res, err := tx.Exec(ctx, `
				INSERT INTO fixed_asset (code, name, category, dept_id, orig_value,
					salvage_ppm, useful_months, start_date, expense_account,
					accum_account, status, disposed_date, remark, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				a.Code, a.Name, string(a.Category), nullInt64(a.DeptID),
				int64(a.OrigValue), a.SalvagePPM, a.UsefulMonths,
				a.StartDate.String(), a.ExpenseAccount, a.AccumAccount,
				string(a.Status), dateOrEmpty(a.DisposedDate), a.Remark, now, now)
			if err != nil {
				return translateErr(err)
			}
			id, err = res.LastInsertId()
			return err
		}
		res, err := tx.Exec(ctx, `
			UPDATE fixed_asset SET code = ?, name = ?, category = ?, dept_id = ?,
				orig_value = ?, salvage_ppm = ?, useful_months = ?, start_date = ?,
				expense_account = ?, accum_account = ?, status = ?, disposed_date = ?,
				remark = ?, updated_at = ?
			 WHERE id = ?`,
			a.Code, a.Name, string(a.Category), nullInt64(a.DeptID),
			int64(a.OrigValue), a.SalvagePPM, a.UsefulMonths,
			a.StartDate.String(), a.ExpenseAccount, a.AccumAccount,
			string(a.Status), dateOrEmpty(a.DisposedDate), a.Remark, now, a.ID)
		if err != nil {
			return translateErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: 固定资产 id=%d", ErrNotFound, a.ID)
		}
		id = a.ID
		return nil
	})
	return id, err
}

// Assets 返回全部固定资产卡片。
func (r *AssetRepo) Assets(ctx context.Context) ([]asset.FixedAsset, error) {
	rows, err := r.db.sql.QueryContext(ctx,
		`SELECT `+assetColumns+` FROM fixed_asset ORDER BY status, start_date, id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	out := []asset.FixedAsset{}
	for rows.Next() {
		a, err := scanAsset(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, translateErr(rows.Err())
}

// Asset 按 id 读一张卡片。
func (r *AssetRepo) Asset(ctx context.Context, id int64) (*asset.FixedAsset, error) {
	row := r.db.sql.QueryRowContext(ctx,
		`SELECT `+assetColumns+` FROM fixed_asset WHERE id = ?`, id)
	a, err := scanAsset(row.Scan)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: 固定资产 id=%d", ErrNotFound, id)
	}
	if err != nil {
		return nil, translateErr(err)
	}
	return a, nil
}

// DeleteAsset 删除一张卡片。
//
// 已经计提过折旧的**不能删** —— 那些折旧凭证已经生成（过账后更是账的一部分），
// 删了卡片，凭证上「这台设备每月提 333.33」的依据就没了。
// 停用的资产请改用「处置」。
func (r *AssetRepo) DeleteAsset(ctx context.Context, id int64) error {
	var n int
	if err := r.db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM asset_depreciation WHERE asset_id = ?`, id).Scan(&n); err != nil {
		return translateErr(err)
	}
	if n > 0 {
		return fmt.Errorf("这台固定资产已经计提过 %d 期折旧，不能删除。\n"+
			"折旧凭证已经生成，删掉卡片它们就没有依据了。\n"+
			"不再使用的资产请改用「处置」——处置当月照提，次月起停。", n)
	}
	res, err := r.db.sql.ExecContext(ctx, `DELETE FROM fixed_asset WHERE id = ?`, id)
	if err != nil {
		return translateErr(err)
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return fmt.Errorf("%w: 固定资产 id=%d", ErrNotFound, id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 折旧记录
// ---------------------------------------------------------------------------

// DepreciationRow 是某个期间的一条折旧记录。
type DepreciationRow struct {
	AssetID   int64
	AssetName string
	Year      int
	Month     int
	Amount    money.Money
	VoucherID *int64
}

// DepreciatedBefore 返回某资产在**指定期间之前**已经计提的累计额。
//
// ★ 这是逐月计提的关键输入：每期的金额都依赖「到上期为止提了多少」
// （最后一期要兜尾差）。少算一期，尾差就补不上。
func (r *AssetRepo) DepreciatedBefore(ctx context.Context, assetID int64,
	k period.Key) (money.Money, error) {
	var total sql.NullInt64
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT SUM(amount) FROM asset_depreciation
		 WHERE asset_id = ? AND (year * 100 + month) < ?`,
		assetID, k.Year*100+k.Month).Scan(&total)
	if err != nil {
		return 0, translateErr(err)
	}
	return money.Money(total.Int64), nil
}

// DepreciatedTotal 返回某资产累计已提折旧（全部期间）。
func (r *AssetRepo) DepreciatedTotal(ctx context.Context, assetID int64) (money.Money, error) {
	var total sql.NullInt64
	err := r.db.sql.QueryRowContext(ctx,
		`SELECT SUM(amount) FROM asset_depreciation WHERE asset_id = ?`, assetID).
		Scan(&total)
	if err != nil {
		return 0, translateErr(err)
	}
	return money.Money(total.Int64), nil
}

// HasDepreciationIn 报告某资产在某期间是否已经提过。
func (r *AssetRepo) HasDepreciationIn(ctx context.Context, assetID int64, k period.Key) (bool, error) {
	var n int
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM asset_depreciation
		 WHERE asset_id = ? AND year = ? AND month = ?`, assetID, k.Year, k.Month).Scan(&n)
	return n > 0, translateErr(err)
}

// SaveDepreciationInTx 写入一条折旧记录。
//
// 重复计提由 `UNIQUE (asset_id, year, month)` 挡住 —— 这是最后一道防线：
// 漏提一个月（少一笔费用）和重复提一个月（多一笔费用）都不会报错，
// 只能靠约束。
//
// ★ label 由**调用方**传进来，不在这里查库。
//
// 这个函数跑在别人开好的事务里，而本库的连接池只有一条连接：
// 在这里再发一条查询会**直接死锁**（等一条永远不还回来的连接）。
// 报错信息要名字，那就把名字传进来 —— 调用方本来就有。
func (r *AssetRepo) SaveDepreciationInTx(ctx context.Context, tx *Tx,
	assetID int64, label string, k period.Key, amount money.Money,
	voucherID *int64) error {
	if !k.Valid() {
		return fmt.Errorf("%w: %v", period.ErrPeriodNotFound, k)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO asset_depreciation (asset_id, year, month, amount, voucher_id, created_at)
		VALUES (?,?,?,?,?,?)`,
		assetID, k.Year, k.Month, int64(amount), nullInt64(voucherID), nowString())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("「%s」的 %s 已经计提过折旧了 —— "+
				"同一期不能提两次（重复提会让费用凭空多一块）", label, k)
		}
		return translateErr(err)
	}
	return nil
}

// Depreciations 返回某期间的折旧记录。
func (r *AssetRepo) Depreciations(ctx context.Context, k period.Key) ([]DepreciationRow, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT d.asset_id, f.name, d.year, d.month, d.amount, d.voucher_id
		  FROM asset_depreciation d
		  JOIN fixed_asset f ON f.id = d.asset_id
		 WHERE d.year = ? AND d.month = ?
		 ORDER BY f.start_date, f.id`, k.Year, k.Month)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	out := []DepreciationRow{}
	for rows.Next() {
		var d DepreciationRow
		var amount int64
		var vid sql.NullInt64
		if err := rows.Scan(&d.AssetID, &d.AssetName, &d.Year, &d.Month,
			&amount, &vid); err != nil {
			return nil, translateErr(err)
		}
		d.Amount = money.Money(amount)
		if vid.Valid {
			id := vid.Int64
			d.VoucherID = &id
		}
		out = append(out, d)
	}
	return out, translateErr(rows.Err())
}

// ---------------------------------------------------------------------------
// 费用摊销
// ---------------------------------------------------------------------------

const amortColumns = `id, code, name, dept_id, total_amount, months, start_date,
	expense_account, asset_account, status, remark`

// SaveAmortization 新增或修改一个待摊项目。
func (r *AssetRepo) SaveAmortization(ctx context.Context, m asset.Amortization) (int64, error) {
	if err := m.Validate(); err != nil {
		return 0, err
	}
	var id int64
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		now := nowString()
		if m.ID == 0 {
			res, err := tx.Exec(ctx, `
				INSERT INTO amortization (code, name, dept_id, total_amount, months,
					start_date, expense_account, asset_account, status, remark,
					created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
				m.Code, m.Name, nullInt64(m.DeptID), int64(m.Total), m.Months,
				m.StartDate.String(), m.ExpenseAccount, m.AssetAccount,
				string(m.Status), m.Remark, now, now)
			if err != nil {
				return translateErr(err)
			}
			id, err = res.LastInsertId()
			return err
		}
		res, err := tx.Exec(ctx, `
			UPDATE amortization SET code = ?, name = ?, dept_id = ?, total_amount = ?,
				months = ?, start_date = ?, expense_account = ?, asset_account = ?,
				status = ?, remark = ?, updated_at = ?
			 WHERE id = ?`,
			m.Code, m.Name, nullInt64(m.DeptID), int64(m.Total), m.Months,
			m.StartDate.String(), m.ExpenseAccount, m.AssetAccount,
			string(m.Status), m.Remark, now, m.ID)
		if err != nil {
			return translateErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: 待摊项目 id=%d", ErrNotFound, m.ID)
		}
		id = m.ID
		return nil
	})
	return id, err
}

// Amortizations 返回全部待摊项目。
func (r *AssetRepo) Amortizations(ctx context.Context) ([]asset.Amortization, error) {
	rows, err := r.db.sql.QueryContext(ctx,
		`SELECT `+amortColumns+` FROM amortization ORDER BY status, start_date, id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	out := []asset.Amortization{}
	for rows.Next() {
		m, err := scanAmort(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, translateErr(rows.Err())
}

// Amortization 按 id 读一个项目。
func (r *AssetRepo) Amortization(ctx context.Context, id int64) (*asset.Amortization, error) {
	row := r.db.sql.QueryRowContext(ctx,
		`SELECT `+amortColumns+` FROM amortization WHERE id = ?`, id)
	m, err := scanAmort(row.Scan)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: 待摊项目 id=%d", ErrNotFound, id)
	}
	if err != nil {
		return nil, translateErr(err)
	}
	return m, nil
}

// DeleteAmortization 删除一个待摊项目（摊过的不给删，理由同固定资产）。
func (r *AssetRepo) DeleteAmortization(ctx context.Context, id int64) error {
	var n int
	if err := r.db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM amortization_entry WHERE amortization_id = ?`, id).
		Scan(&n); err != nil {
		return translateErr(err)
	}
	if n > 0 {
		return fmt.Errorf("这个项目已经摊过 %d 期，不能删除。\n"+
			"摊销凭证已经生成，删掉项目它们就没有依据了。\n"+
			"不再摊销请改用「作废」——作废之后新期间不再摊，历史记录保留。", n)
	}
	res, err := r.db.sql.ExecContext(ctx, `DELETE FROM amortization WHERE id = ?`, id)
	if err != nil {
		return translateErr(err)
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return fmt.Errorf("%w: 待摊项目 id=%d", ErrNotFound, id)
	}
	return nil
}

// AmortizedBefore 返回某项目在指定期间之前已摊的累计额。
func (r *AssetRepo) AmortizedBefore(ctx context.Context, id int64,
	k period.Key) (money.Money, error) {
	var total sql.NullInt64
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT SUM(amount) FROM amortization_entry
		 WHERE amortization_id = ? AND (year * 100 + month) < ?`,
		id, k.Year*100+k.Month).Scan(&total)
	if err != nil {
		return 0, translateErr(err)
	}
	return money.Money(total.Int64), nil
}

// HasAmortizationIn 报告某项目在某期间是否已经摊过。
func (r *AssetRepo) HasAmortizationIn(ctx context.Context, id int64, k period.Key) (bool, error) {
	var n int
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM amortization_entry
		 WHERE amortization_id = ? AND year = ? AND month = ?`, id, k.Year, k.Month).Scan(&n)
	return n > 0, translateErr(err)
}

// AmortizedTotal 返回某项目累计已摊额。
func (r *AssetRepo) AmortizedTotal(ctx context.Context, id int64) (money.Money, error) {
	var total sql.NullInt64
	err := r.db.sql.QueryRowContext(ctx,
		`SELECT SUM(amount) FROM amortization_entry WHERE amortization_id = ?`, id).
		Scan(&total)
	if err != nil {
		return 0, translateErr(err)
	}
	return money.Money(total.Int64), nil
}

// SaveAmortizationEntryInTx 写入一条摊销记录。
//
// label 由调用方传进来，理由同 SaveDepreciationInTx：
// 事务里不能再查库，会死锁。
func (r *AssetRepo) SaveAmortizationEntryInTx(ctx context.Context, tx *Tx,
	id int64, label string, k period.Key, amount money.Money,
	voucherID *int64) error {
	if !k.Valid() {
		return fmt.Errorf("%w: %v", period.ErrPeriodNotFound, k)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO amortization_entry (amortization_id, year, month, amount, voucher_id, created_at)
		VALUES (?,?,?,?,?,?)`,
		id, k.Year, k.Month, int64(amount), nullInt64(voucherID), nowString())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("「%s」的 %s 已经摊销过了 —— 同一期不能摊两次", label, k)
		}
		return translateErr(err)
	}
	return nil
}

// AmortizationEntries 返回某期间的摊销记录。
func (r *AssetRepo) AmortizationEntries(ctx context.Context, k period.Key) ([]DepreciationRow, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT e.amortization_id, a.name, e.year, e.month, e.amount, e.voucher_id
		  FROM amortization_entry e
		  JOIN amortization a ON a.id = e.amortization_id
		 WHERE e.year = ? AND e.month = ?
		 ORDER BY a.start_date, a.id`, k.Year, k.Month)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	out := []DepreciationRow{}
	for rows.Next() {
		var d DepreciationRow
		var amount int64
		var vid sql.NullInt64
		if err := rows.Scan(&d.AssetID, &d.AssetName, &d.Year, &d.Month,
			&amount, &vid); err != nil {
			return nil, translateErr(err)
		}
		d.Amount = money.Money(amount)
		if vid.Valid {
			id := vid.Int64
			d.VoucherID = &id
		}
		out = append(out, d)
	}
	return out, translateErr(rows.Err())
}

// ---------------------------------------------------------------------------
// 扫描与工具
// ---------------------------------------------------------------------------

type scanFn func(dest ...any) error

func scanAsset(scan scanFn) (*asset.FixedAsset, error) {
	var a asset.FixedAsset
	var category, status, startDate, disposed string
	var deptID sql.NullInt64
	var orig int64
	if err := scan(&a.ID, &a.Code, &a.Name, &category, &deptID, &orig,
		&a.SalvagePPM, &a.UsefulMonths, &startDate, &a.ExpenseAccount,
		&a.AccumAccount, &status, &disposed, &a.Remark); err != nil {
		return nil, translateErr(err)
	}
	a.Category = asset.Category(category)
	a.Status = asset.Status(status)
	a.OrigValue = money.Money(orig)
	if deptID.Valid {
		id := deptID.Int64
		a.DeptID = &id
	}
	if startDate != "" {
		d, err := calendar.Parse(startDate)
		if err != nil {
			return nil, fmt.Errorf("固定资产 #%d 的投入使用日期 %q 无法解析: %w",
				a.ID, startDate, err)
		}
		a.StartDate = d
	}
	if disposed != "" {
		d, err := calendar.Parse(disposed)
		if err != nil {
			return nil, fmt.Errorf("固定资产 #%d 的处置日期 %q 无法解析: %w",
				a.ID, disposed, err)
		}
		a.DisposedDate = d
	}
	return &a, nil
}

func scanAmort(scan scanFn) (*asset.Amortization, error) {
	var m asset.Amortization
	var status, startDate string
	var deptID sql.NullInt64
	var total int64
	if err := scan(&m.ID, &m.Code, &m.Name, &deptID, &total, &m.Months,
		&startDate, &m.ExpenseAccount, &m.AssetAccount, &status, &m.Remark); err != nil {
		return nil, translateErr(err)
	}
	m.Total = money.Money(total)
	m.Status = asset.AmortStatus(status)
	if deptID.Valid {
		id := deptID.Int64
		m.DeptID = &id
	}
	if startDate != "" {
		d, err := calendar.Parse(startDate)
		if err != nil {
			return nil, fmt.Errorf("待摊项目 #%d 的开始日期 %q 无法解析: %w",
				m.ID, startDate, err)
		}
		m.StartDate = d
	}
	return &m, nil
}

// dateOrEmpty 把零值日期写成空串（列上是 NOT NULL DEFAULT ”）。
func dateOrEmpty(d calendar.Date) string {
	if !d.Valid() {
		return ""
	}
	return d.String()
}
