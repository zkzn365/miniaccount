package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/taxfiling"
)

// TaxFilingRepo 是税务申报台账的持久化访问。
type TaxFilingRepo struct{ db *DB }

// TaxFilings 返回申报台账仓储。
func (db *DB) TaxFilings() *TaxFilingRepo { return &TaxFilingRepo{db: db} }

// taxFilingColumns 是统一的列清单，读写两头照着它来。
const taxFilingColumns = `id, year, month, kind, period_label, status,
	filed_date, paid_date, payable, tax_amount, surcharge, paid,
	channel, receipt_no, operator, note,
	voided_by, COALESCE(voided_at, ''), void_reason, created_at, updated_at`

// SaveFiling 新增或修改一条申报记录。
//
// ★ 同一属期同一税种只能有一条**有效**记录（唯一索引只排除已作废的）。
// 已经登记过的再存一次会撞索引，这时返回 ErrDuplicate，
// 由服务层告诉用户「这一期已经报过了」—— 更正要先作废。
func (r *TaxFilingRepo) SaveFiling(ctx context.Context, f taxfiling.Filing) (int64, error) {
	if err := f.Validate(); err != nil {
		return 0, err
	}
	var id int64
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		now := nowString()
		if f.ID == 0 {
			res, err := tx.Exec(ctx, `
				INSERT INTO tax_filing
					(year, month, kind, period_label, status, filed_date, paid_date,
					 payable, tax_amount, surcharge, paid, channel, receipt_no, operator, note,
					 voided_by, voided_at, void_reason, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				f.Year, f.Month, string(f.Kind), f.PeriodLabel, string(f.Status),
				f.FiledDate, f.PaidDate, int64(f.Payable), int64(f.TaxAmount),
				int64(f.Surcharge), int64(f.Paid), f.Channel, f.ReceiptNo, f.Operator, f.Note,
				f.VoidedBy, nullText(f.VoidedAt), f.VoidReason, now, now)
			if err != nil {
				return translateErr(err)
			}
			id, err = res.LastInsertId()
			return translateErr(err)
		}
		// 修改：属期与税种不允许改（改了就是另一条记录了）
		res, err := tx.Exec(ctx, `
			UPDATE tax_filing SET status = ?, filed_date = ?, paid_date = ?,
				payable = ?, tax_amount = ?, surcharge = ?, paid = ?,
				channel = ?, receipt_no = ?, operator = ?, note = ?,
				voided_by = ?, voided_at = ?, void_reason = ?, updated_at = ?
			 WHERE id = ?`,
			string(f.Status), f.FiledDate, f.PaidDate,
			int64(f.Payable), int64(f.TaxAmount), int64(f.Surcharge), int64(f.Paid),
			f.Channel, f.ReceiptNo, f.Operator, f.Note,
			f.VoidedBy, nullText(f.VoidedAt), f.VoidReason, now, f.ID)
		if err != nil {
			return translateErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: 申报记录 id=%d", ErrNotFound, f.ID)
		}
		id = f.ID
		return nil
	})
	return id, err
}

// Filing 取某属期某税种**有效**的那条记录；没有则返回 (nil, nil)。
func (r *TaxFilingRepo) Filing(ctx context.Context, kind taxfiling.Kind,
	k period.Key) (*taxfiling.Filing, error) {

	list, err := r.query(ctx,
		`WHERE year = ? AND month = ? AND kind = ? AND status <> 'void' LIMIT 1`,
		k.Year, k.Month, string(kind))
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

// FilingByID 按 id 取一条（含已作废的）。
func (r *TaxFilingRepo) FilingByID(ctx context.Context, id int64) (*taxfiling.Filing, error) {
	list, err := r.query(ctx, `WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%w: 申报记录 id=%d", ErrNotFound, id)
	}
	return &list[0], nil
}

// FilingsOfYear 返回某年的全部申报记录（含已作废的，按属期倒序）。
//
// 含作废记录是有意的：列表上要能看出「这一期报过、后来作废重报过」。
func (r *TaxFilingRepo) FilingsOfYear(ctx context.Context, year int) ([]taxfiling.Filing, error) {
	return r.query(ctx, `WHERE year = ? ORDER BY month DESC, kind, id DESC`, year)
}

// VoidFiling 作废一条记录（不物理删除）。
func (r *TaxFilingRepo) VoidFiling(ctx context.Context, id int64,
	by, at, reason string) error {

	return r.db.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE tax_filing SET status = 'void', voided_by = ?, voided_at = ?,
				void_reason = ?, updated_at = ?
			 WHERE id = ? AND status <> 'void'`,
			by, at, reason, nowString(), id)
		if err != nil {
			return translateErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// 记录不存在，或者已经是作废状态
			var status string
			err := tx.QueryRow(ctx, `SELECT status FROM tax_filing WHERE id = ?`,
				id).Scan(&status)
			if err == sql.ErrNoRows {
				return fmt.Errorf("%w: 申报记录 id=%d", ErrNotFound, id)
			}
			if err != nil {
				return translateErr(err)
			}
			return fmt.Errorf("这条申报记录已经是「%s」了", taxfiling.Status(status).Label())
		}
		return nil
	})
}

// query 按条件取记录。
func (r *TaxFilingRepo) query(ctx context.Context, where string, args ...any) ([]taxfiling.Filing, error) {
	rows, err := r.db.sql.QueryContext(ctx,
		`SELECT `+taxFilingColumns+` FROM tax_filing `+where, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	out := []taxfiling.Filing{}
	for rows.Next() {
		var (
			f                                   taxfiling.Filing
			kind, status                        string
			payable, taxAmount, surcharge, paid int64
			voidedAt                            string
		)
		if err := rows.Scan(&f.ID, &f.Year, &f.Month, &kind, &f.PeriodLabel, &status,
			&f.FiledDate, &f.PaidDate, &payable, &taxAmount, &surcharge, &paid,
			&f.Channel, &f.ReceiptNo, &f.Operator, &f.Note,
			&f.VoidedBy, &voidedAt, &f.VoidReason, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, translateErr(err)
		}
		f.Kind = taxfiling.Kind(kind)
		f.Status = taxfiling.Status(status)
		f.Payable = money.Money(payable)
		f.TaxAmount = money.Money(taxAmount)
		f.Surcharge = money.Money(surcharge)
		f.Paid = money.Money(paid)
		f.VoidedAt = voidedAt
		out = append(out, f)
	}
	return out, translateErr(rows.Err())
}

// nullText 把空串写成 NULL。
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
