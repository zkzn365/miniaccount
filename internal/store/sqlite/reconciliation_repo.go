package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/reconciliation"
)

// ReconciliationRepo 生成银行存款余额调节表。
type ReconciliationRepo struct{ db *DB }

// Reconciliation 返回余额调节仓储。
func (db *DB) Reconciliation() *ReconciliationRepo {
	return &ReconciliationRepo{db: db}
}

// BuildReconciliation 生成某银行科目的余额调节表。
//
// # 未达账项是怎么找出来的
//
// 银行流水与凭证是**双向关联**的（`bank_flow.voucher_id` ↔
// `voucher.source_id`）。有了这条关联，四类未达账项就都能用 SQL 判出来：
//
//	银行已收/已付、企业未收/未付
//	    流水存在、但 voucher_id 为空 —— 银行那边动了，企业还没记账
//
//	企业已收/已付、银行未收/未付
//	    银行科目上有分录，但那张凭证不来自任何流水 —— 企业记了，银行流水里没有
//
// 后者需要注意：企业自己做的非流水凭证（如内部转账、手工调账）
// 也会落进这一类。这是**正确的** —— 在没导入对账单之前，
// 企业确实无法知道银行有没有处理。用户按提示处理完流水后，
// 这一类会自然收敛到真正的未达账项。
func (r *ReconciliationRepo) BuildReconciliation(ctx context.Context,
	accountCode string, asOf calendar.Date,
	bankBalance *money.Money) (*reconciliation.Report, error) {
	return r.BuildReconciliationFrom(ctx, accountCode, calendar.Date{}, asOf, bankBalance)
}

// BuildReconciliationFrom 在指定对账窗口内生成调节表。
//
// from 留空时自动取「截止日之前最早一笔流水之日」——
// 也就是对账单覆盖到的第一天。见 reconciliation.Input.From 的说明：
// 窗口之前的余额两边都有，必须归入期初而不是未达账项。
func (r *ReconciliationRepo) BuildReconciliationFrom(ctx context.Context,
	accountCode string, from, asOf calendar.Date,
	bankBalance *money.Money) (*reconciliation.Report, error) {

	if accountCode == "" {
		accountCode = "1002"
	}

	// 科目名
	var accountName string
	var accountID int64
	err := r.db.sql.QueryRowContext(ctx,
		`SELECT id, name FROM account WHERE code = ?`, accountCode).
		Scan(&accountID, &accountName)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: 银行科目 %s", ErrNotFound, accountCode)
	}
	if err != nil {
		return nil, translateErr(err)
	}

	// 对账窗口起点：默认取最早一笔流水之日
	if !from.Valid() {
		d, ok, err := r.firstFlowDate(ctx, accountID, asOf)
		if err != nil {
			return nil, err
		}
		if ok {
			from = d
		} else {
			// 没有流水（没导入对账单）时窗口退化为一天，
			// 于是所有余额归入期初、未达账项为空。
			from = asOf
		}
	}

	// 账面余额与账面期初
	bookBalance, err := r.accountNet(ctx, accountID, calendar.Date{}, asOf)
	if err != nil {
		return nil, err
	}
	bookOpening, err := r.accountNet(ctx, accountID, calendar.Date{}, from.AddDays(-1))
	if err != nil {
		return nil, err
	}

	// 银行期初：最早一笔流水的余额 − 该笔金额
	bankOpening, err := r.bankOpening(ctx, accountID, from, asOf)
	if err != nil {
		return nil, err
	}

	bankIn, err := r.flowsNotBooked(ctx, accountID, from, asOf, "in")
	if err != nil {
		return nil, err
	}
	bankOut, err := r.flowsNotBooked(ctx, accountID, from, asOf, "out")
	if err != nil {
		return nil, err
	}
	bookIn, err := r.entriesNotBanked(ctx, accountID, from, asOf, true)
	if err != nil {
		return nil, err
	}
	bookOut, err := r.entriesNotBanked(ctx, accountID, from, asOf, false)
	if err != nil {
		return nil, err
	}

	// 从对账单里取期末余额（最后一条有余额的流水）
	bb := bankBalance
	if bb == nil {
		b, ok, err := r.lastStatementBalance(ctx, accountID, asOf)
		if err != nil {
			return nil, err
		}
		if ok {
			bb = &b
		}
	}

	// 未处理流水条数（提示用）
	var unreconciled int
	if err := r.db.sql.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM bank_flow
		 WHERE account_id = ? AND voucher_id IS NULL
		   AND status <> 'ignored' AND txn_date >= ? AND txn_date <= ?`,
		accountID, from.String(), asOf.String()).Scan(&unreconciled); err != nil {
		return nil, translateErr(err)
	}

	return reconciliation.Build(reconciliation.Input{
		AsOf: asOf, From: from,
		AccountCode: accountCode, AccountName: accountName,
		BookOpening:           bookOpening,
		BankOpening:           bankOpening,
		BookBalance:           bookBalance,
		BankBalance:           bb,
		BankReceivedNotBooked: bankIn,
		BankPaidNotBooked:     bankOut,
		BookReceivedNotBanked: bookIn,
		BookPaidNotBanked:     bookOut,
		UnreconciledFlows:     unreconciled,
	})
}

// accountNet 返回 [from, to] 区间内该科目的净额（借−贷）。
// from 为零值时表示不限起点。
func (r *ReconciliationRepo) accountNet(ctx context.Context,
	accountID int64, from, to calendar.Date) (money.Money, error) {
	var net int64
	if from.Valid() {
		err := r.db.sql.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(debit), 0) - COALESCE(SUM(credit), 0)
			  FROM ledger_entry
			 WHERE account_id = ? AND biz_date >= ? AND biz_date <= ?`,
			accountID, from.String(), to.String()).Scan(&net)
		if err != nil {
			return 0, translateErr(err)
		}
		return money.Money(net), nil
	}
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(debit), 0) - COALESCE(SUM(credit), 0)
		  FROM ledger_entry WHERE account_id = ? AND biz_date <= ?`,
		accountID, to.String()).Scan(&net)
	if err != nil {
		return 0, translateErr(err)
	}
	return money.Money(net), nil
}

// firstFlowDate 返回窗口内最早一笔流水的日期。
func (r *ReconciliationRepo) firstFlowDate(ctx context.Context,
	accountID int64, asOf calendar.Date) (calendar.Date, bool, error) {
	// MIN() 在没有匹配行时返回 NULL 而不是「无行」，
	// 所以必须用 NullString 接 —— 否则「还没导入对账单」这种
	// 最常见的初始状态会直接报 Scan 错误。
	var d sql.NullString
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT MIN(txn_date) FROM bank_flow
		 WHERE account_id = ? AND txn_date <= ? AND status <> 'ignored'`,
		accountID, asOf.String()).Scan(&d)
	if err != nil {
		return calendar.Date{}, false, translateErr(err)
	}
	if !d.Valid || d.String == "" {
		return calendar.Date{}, false, nil
	}
	parsed, err := calendar.Parse(d.String)
	if err != nil {
		return calendar.Date{}, false, err
	}
	return parsed, true, nil
}

// bankOpening 由对账单推出窗口起点之前的银行余额。
//
// 算法：取窗口内最早一笔**带余额**的流水，用「该笔余额 − 该笔金额」
// 反推出它发生前的余额。银行流水的余额列是账户余额快照，
// 因此这个反推是精确的，不需要用户额外提供期初数。
func (r *ReconciliationRepo) bankOpening(ctx context.Context,
	accountID int64, from, asOf calendar.Date) (*money.Money, error) {
	var (
		bal       int64
		amount    int64
		direction string
	)
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT balance, amount, direction FROM bank_flow
		 WHERE account_id = ? AND txn_date >= ? AND txn_date <= ?
		   AND balance IS NOT NULL AND status <> 'ignored'
		 ORDER BY txn_date, id LIMIT 1`,
		accountID, from.String(), asOf.String()).Scan(&bal, &amount, &direction)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, translateErr(err)
	}
	// 收入使余额增加，支出使之减少 —— 反推方向相反
	opening := bal - amount
	if direction == "out" {
		opening = bal + amount
	}
	m := money.Money(opening)
	return &m, nil
}

// flowsNotBooked 返回尚未生成凭证的流水。
func (r *ReconciliationRepo) flowsNotBooked(ctx context.Context,
	accountID int64, from, asOf calendar.Date, dir string) ([]reconciliation.Item, error) {

	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT txn_date, summary, counterparty_name, amount, COALESCE(serial_no, '')
		  FROM bank_flow
		 WHERE account_id = ? AND direction = ? AND txn_date >= ? AND txn_date <= ?
		   AND voucher_id IS NULL AND status <> 'ignored'
		 ORDER BY txn_date, id`, accountID, dir, from.String(), asOf.String())
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []reconciliation.Item
	for rows.Next() {
		var (
			date, summary, cp, serial string
			amount                    int64
		)
		if err := rows.Scan(&date, &summary, &cp, &amount, &serial); err != nil {
			return nil, err
		}
		d, err := calendar.Parse(date)
		if err != nil {
			return nil, err
		}
		label := summary
		if label == "" {
			label = cp
		}
		if label == "" {
			label = "银行流水"
		}
		ref := serial
		if ref == "" {
			ref = "流水"
		}
		out = append(out, reconciliation.Item{
			Date: d, Summary: label, Reference: ref, Amount: money.Money(amount),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	reconciliation.SortItemsByDate(out)
	return out, nil
}

// entriesNotBanked 返回银行科目上「不来自银行流水」的分录。
//
// 判定依据：该分录所属凭证没有被任何流水引用（bank_flow.voucher_id）。
func (r *ReconciliationRepo) entriesNotBanked(ctx context.Context,
	accountID int64, from, asOf calendar.Date, debit bool) ([]reconciliation.Item, error) {

	pick := "le.debit"
	if !debit {
		pick = "le.credit"
	}
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT le.biz_date, le.summary, `+pick+`, COALESCE(v.no, '')
		  FROM ledger_entry le
		  LEFT JOIN voucher v ON v.id = le.voucher_id
		 WHERE le.account_id = ? AND le.biz_date >= ? AND le.biz_date <= ?
		   AND `+pick+` > 0
		   AND NOT EXISTS (
		         SELECT 1 FROM bank_flow bf WHERE bf.voucher_id = le.voucher_id)
		 ORDER BY le.biz_date, le.id`, accountID, from.String(), asOf.String())
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []reconciliation.Item
	for rows.Next() {
		var date, summary, voucherNo string
		var amount int64
		if err := rows.Scan(&date, &summary, &amount, &voucherNo); err != nil {
			return nil, err
		}
		d, err := calendar.Parse(date)
		if err != nil {
			return nil, err
		}
		out = append(out, reconciliation.Item{
			Date: d, Summary: summary, Reference: voucherNo,
			Amount: money.Money(amount),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	reconciliation.SortItemsByDate(out)
	return out, nil
}

// lastStatementBalance 取对账单上最后一条有余额的流水的余额。
func (r *ReconciliationRepo) lastStatementBalance(ctx context.Context,
	accountID int64, asOf calendar.Date) (money.Money, bool, error) {

	var bal sql.NullInt64
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT balance FROM bank_flow
		 WHERE account_id = ? AND txn_date <= ? AND balance IS NOT NULL
		 ORDER BY txn_date DESC, id DESC LIMIT 1`,
		accountID, asOf.String()).Scan(&bal)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, translateErr(err)
	}
	if !bal.Valid {
		return 0, false, nil
	}
	return money.Money(bal.Int64), true, nil
}
