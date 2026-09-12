package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/expense"
	"miniaccount/internal/domain/invoice"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
)

// InvoiceRepo 是发票档案的持久化访问。
type InvoiceRepo struct{ db *DB }

// Invoices 返回发票档案仓储。
func (db *DB) Invoices() *InvoiceRepo { return &InvoiceRepo{db: db} }

// 发票与报销模块错误。
var (
	ErrInvoiceExists = fmt.Errorf("invoice: 该发票已登记")
	ErrClaimExists   = fmt.Errorf("expense: 报销单号已存在")
)

const invoiceColumns = `i.id, i.direction, i.kind, i.code, i.number, i.invoice_date,
	i.seller_name, i.seller_tax_no, i.buyer_name, i.buyer_tax_no,
	i.amount_ex_tax, i.tax_rate, i.tax_amount, i.total_amount,
	i.category, i.status, i.contact_id, i.voucher_id, i.remark`

// ListInvoices 按方向与日期范围查询发票。
func (r *InvoiceRepo) ListInvoices(ctx context.Context, dir invoice.Direction,
	from, to calendar.Date, status invoice.Status) ([]*invoice.Invoice, error) {

	q := `SELECT ` + invoiceColumns + ` FROM invoice i WHERE 1 = 1`
	var args []any
	if dir != "" {
		q += ` AND i.direction = ?`
		args = append(args, string(dir))
	}
	if status != "" {
		q += ` AND i.status = ?`
		args = append(args, string(status))
	}
	if !from.IsZero() {
		q += ` AND i.invoice_date >= ?`
		args = append(args, from.String())
	}
	if !to.IsZero() {
		q += ` AND i.invoice_date <= ?`
		args = append(args, to.String())
	}
	q += ` ORDER BY i.invoice_date, i.id`

	rows, err := r.db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	return scanInvoices(rows)
}

func scanInvoices(rows *sql.Rows) ([]*invoice.Invoice, error) {
	var out []*invoice.Invoice
	for rows.Next() {
		inv := &invoice.Invoice{}
		var (
			dir, kind, status string
			dateStr           string
			amountEx, taxRate int64
			taxAmt, totalAmt  int64
			contactID         sql.NullInt64
			voucherID         sql.NullInt64
		)
		if err := rows.Scan(&inv.ID, &dir, &kind, &inv.Code, &inv.Number, &dateStr,
			&inv.SellerName, &inv.SellerTaxNo, &inv.BuyerName, &inv.BuyerTaxNo,
			&amountEx, &taxRate, &taxAmt, &totalAmt,
			&inv.Category, &status, &contactID, &voucherID, &inv.Remark); err != nil {
			return nil, err
		}
		d, err := calendar.Parse(dateStr)
		if err != nil {
			return nil, fmt.Errorf("sqlite: 发票 %d 日期非法 %q", inv.ID, dateStr)
		}
		inv.InvoiceDate = d
		inv.Direction = invoice.Direction(dir)
		inv.Kind = invoice.Kind(kind)
		inv.Status = invoice.Status(status)
		inv.AmountExTax = money.Money(amountEx)
		inv.TaxRate = money.Rate(taxRate)
		inv.TaxAmount = money.Money(taxAmt)
		inv.TotalAmount = money.Money(totalAmt)
		inv.ContactID = toNullInt64(contactID)
		inv.VoucherID = toNullInt64(voucherID)
		out = append(out, inv)
	}
	return out, rows.Err()
}

// GetInvoice 按 id 取发票。
func (r *InvoiceRepo) GetInvoice(ctx context.Context, id int64) (*invoice.Invoice, error) {
	rows, err := r.db.sql.QueryContext(ctx,
		`SELECT `+invoiceColumns+` FROM invoice i WHERE i.id = ?`, id)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	list, err := scanInvoices(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%w: 发票 id=%d", ErrNotFound, id)
	}
	inv := list[0]
	hashes, err := r.db.Attachments().HashesOf(ctx, "invoice", id)
	if err != nil {
		return nil, err
	}
	inv.AttachmentHashes = hashes
	return inv, nil
}

// CreateInvoice 登记一张发票。
//
// 判重交给数据库的部分唯一索引：同一张发票被重复登记是很常见的误操作，
// 而重复登记会直接导致进项税额被多抵扣。
func (r *InvoiceRepo) CreateInvoice(ctx context.Context, inv *invoice.Invoice) (int64, error) {
	if err := inv.Validate(); err != nil {
		return 0, err
	}
	var id int64
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.Exec(ctx, `
			INSERT INTO invoice (direction, kind, code, number, invoice_date,
				seller_name, seller_tax_no, buyer_name, buyer_tax_no,
				amount_ex_tax, tax_rate, tax_amount, total_amount,
				category, status, contact_id, remark, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			string(inv.Direction), string(inv.Kind), inv.Code, inv.Number,
			inv.InvoiceDate.String(), inv.SellerName, inv.SellerTaxNo,
			inv.BuyerName, inv.BuyerTaxNo,
			int64(inv.AmountExTax), int64(inv.TaxRate), int64(inv.TaxAmount),
			int64(inv.TotalAmount), inv.Category, string(inv.Status),
			nullInt64(inv.ContactID), inv.Remark, nowString(), nowString())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		inv.ID = id
		return r.db.Attachments().linkTx(ctx, tx, "invoice", id, inv.AttachmentHashes)
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// UpdateInvoiceStatus 更新发票状态。
func (r *InvoiceRepo) UpdateInvoiceStatus(ctx context.Context, id int64,
	status invoice.Status) error {
	if !status.Valid() {
		return fmt.Errorf("%w: %q", invoice.ErrBadStatus, status)
	}
	res, err := r.db.sql.ExecContext(ctx,
		`UPDATE invoice SET status = ?, updated_at = ? WHERE id = ?`,
		string(status), nowString(), id)
	if err != nil {
		return translateErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: 发票 id=%d", ErrNotFound, id)
	}
	return nil
}

// InvoiceSummary 按期间与方向汇总发票，便于与申报表核对。
func (r *InvoiceRepo) InvoiceSummary(ctx context.Context, dir invoice.Direction,
	from, to calendar.Date) (invoice.Summary, error) {
	invs, err := r.ListInvoices(ctx, dir, from, to, "")
	if err != nil {
		return invoice.Summary{}, err
	}
	return invoice.Summarize(invs), nil
}

// ---------------------------------------------------------------------------
// 报销单
// ---------------------------------------------------------------------------

// ClaimRepo 是报销单的持久化访问。
type ClaimRepo struct{ db *DB }

// Claims 返回报销单仓储。
func (db *DB) Claims() *ClaimRepo { return &ClaimRepo{db: db} }

const claimColumns = `c.id, c.code, c.claimant_employee_id, c.dept_id, c.apply_date,
	c.trip_start, c.trip_end, c.destination, c.reason, c.status, c.total_amount,
	c.voucher_id, c.approver_employee_id, c.approved_at,
	COALESCE(pa.code, ''), COALESCE(ya.code, ''), c.remark`

// ListClaims 按状态与日期查询报销单。
func (r *ClaimRepo) ListClaims(ctx context.Context, status expense.Status,
	from, to calendar.Date) ([]*expense.Claim, error) {

	q := `SELECT ` + claimColumns + `
	       FROM expense_claim c
	       LEFT JOIN account pa ON pa.id = c.pay_from_account_id
	       LEFT JOIN account ya ON ya.id = c.payable_account_id
	      WHERE 1 = 1`
	var args []any
	if status != "" {
		q += ` AND c.status = ?`
		args = append(args, string(status))
	}
	if !from.IsZero() {
		q += ` AND c.apply_date >= ?`
		args = append(args, from.String())
	}
	if !to.IsZero() {
		q += ` AND c.apply_date <= ?`
		args = append(args, to.String())
	}
	q += ` ORDER BY c.apply_date, c.id`

	rows, err := r.db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []*expense.Claim
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanClaim(rows *sql.Rows) (*expense.Claim, error) {
	c := &expense.Claim{}
	var (
		dateStr, tripStart, tripEnd, status string
		total                               int64
		deptID, voucherID, approverID       sql.NullInt64
		approvedAt                          sql.NullString
	)
	if err := rows.Scan(&c.ID, &c.Code, &c.ClaimantEmployeeID, &deptID, &dateStr,
		&tripStart, &tripEnd, &c.Destination, &c.Reason, &status, &total,
		&voucherID, &approverID, &approvedAt, &c.PayFromAccount,
		&c.PayableAccount, &c.Remark); err != nil {
		return nil, err
	}
	d, err := calendar.Parse(dateStr)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 报销单 %d 日期非法 %q", c.ID, dateStr)
	}
	c.ApplyDate = d
	if tripStart != "" {
		if td, err := calendar.Parse(tripStart); err == nil {
			c.TripStart = td
		}
	}
	if tripEnd != "" {
		if td, err := calendar.Parse(tripEnd); err == nil {
			c.TripEnd = td
		}
	}
	c.Status = expense.Status(status)
	c.TotalAmount = money.Money(total)
	c.DeptID = toNullInt64(deptID)
	c.VoucherID = toNullInt64(voucherID)
	c.ApproverEmployeeID = toNullInt64(approverID)
	c.ApprovedAt = parseNullTime(approvedAt)
	return c, nil
}

// GetClaim 按 id 取报销单（含明细与附件）。
func (r *ClaimRepo) GetClaim(ctx context.Context, id int64) (*expense.Claim, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+claimColumns+`
		FROM expense_claim c
		LEFT JOIN account pa ON pa.id = c.pay_from_account_id
		LEFT JOIN account ya ON ya.id = c.payable_account_id
		WHERE c.id = ?`, id)
	if err != nil {
		return nil, translateErr(err)
	}
	var c *expense.Claim
	for rows.Next() {
		c, err = scanClaim(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
	}
	rows.Close()
	if c == nil {
		return nil, fmt.Errorf("%w: 报销单 id=%d", ErrNotFound, id)
	}

	items, err := r.loadItems(ctx, id)
	if err != nil {
		return nil, err
	}
	c.Items = items
	return c, nil
}

func (r *ClaimRepo) loadItems(ctx context.Context, claimID int64) ([]*expense.Item, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT i.id, i.claim_id, i.line_no, i.category, i.occur_date, i.summary,
		       i.amount, i.tax_amount, a.code, i.dept_id, i.invoice_id
		  FROM expense_item i
		  JOIN account a ON a.id = i.account_id
		 WHERE i.claim_id = ? ORDER BY i.line_no`, claimID)
	if err != nil {
		return nil, translateErr(err)
	}

	var out []*expense.Item
	for rows.Next() {
		it := &expense.Item{}
		var (
			cat, dateStr string
			amount, tax  int64
			deptID       sql.NullInt64
			invoiceID    sql.NullInt64
		)
		if err := rows.Scan(&it.ID, &it.ClaimID, &it.LineNo, &cat, &dateStr,
			&it.Summary, &amount, &tax, &it.AccountCode, &deptID, &invoiceID); err != nil {
			rows.Close()
			return nil, err
		}
		d, err := calendar.Parse(dateStr)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("sqlite: 报销明细 %d 日期非法 %q", it.ID, dateStr)
		}
		it.OccurDate = d
		it.Category = expense.Category(cat)
		it.Amount = money.Money(amount)
		it.TaxAmount = money.Money(tax)
		it.DeptID = toNullInt64(deptID)
		it.InvoiceID = toNullInt64(invoiceID)
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// ★ 必须先关闭 rows 再查附件。
	//
	// 本项目的连接池是单连接（MaxOpenConns=1，见 db.go 的说明），
	// 在 rows 未关闭时发起新查询会**死锁**：唯一那条连接还被外层结果集占着。
	rows.Close()

	for _, it := range out {
		hashes, err := r.db.Attachments().HashesOf(ctx, "expense_item", it.ID)
		if err != nil {
			return nil, err
		}
		it.AttachmentHashes = hashes
	}
	return out, nil
}

// SaveClaim 保存报销单（草稿可覆盖）。
//
// 报销单号由调用方生成或留空自动生成（BX-YYYY-MM-####）。
func (r *ClaimRepo) SaveClaim(ctx context.Context, c *expense.Claim) (int64, error) {
	if c.Code == "" {
		code, err := r.nextClaimCode(ctx, c.ApplyDate)
		if err != nil {
			return 0, err
		}
		c.Code = code
	}
	c.ComputeTotal()
	if err := c.Validate(); err != nil {
		return 0, err
	}

	var id int64
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		accIDs, err := r.db.Accounts().IDsByCodeTx(ctx, tx)
		if err != nil {
			return err
		}

		var existingStatus string
		err = tx.QueryRow(ctx, `SELECT status FROM expense_claim WHERE code = ?`, c.Code).
			Scan(&existingStatus)
		switch {
		case err == nil:
			if !expense.Status(existingStatus).CanEdit() {
				return fmt.Errorf("%w: 报销单 %s 状态为「%s」",
					expense.ErrBadStatus, c.Code, expense.Status(existingStatus).Label())
			}
			if err := tx.QueryRow(ctx,
				`SELECT id FROM expense_claim WHERE code = ?`, c.Code).Scan(&id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`DELETE FROM expense_item WHERE claim_id = ?`, id); err != nil {
				return err
			}
			var payFrom, payable any
			if c.PayFromAccount != "" {
				v, ok := accIDs[c.PayFromAccount]
				if !ok {
					return fmt.Errorf("%w: 付款科目 %s", ErrNotFound, c.PayFromAccount)
				}
				payFrom = v
			}
			if c.PayableAccount != "" {
				v, ok := accIDs[c.PayableAccount]
				if !ok {
					return fmt.Errorf("%w: 挂账科目 %s", ErrNotFound, c.PayableAccount)
				}
				payable = v
			}
			_, err := tx.Exec(ctx, `
				UPDATE expense_claim SET claimant_employee_id = ?, dept_id = ?,
					apply_date = ?, trip_start = ?, trip_end = ?, destination = ?,
					reason = ?, status = ?, total_amount = ?,
					pay_from_account_id = ?, payable_account_id = ?, remark = ?,
					updated_at = ?
				 WHERE id = ?`,
				c.ClaimantEmployeeID, nullInt64(c.DeptID), c.ApplyDate.String(),
				c.TripStart.String(), c.TripEnd.String(), c.Destination, c.Reason,
				string(c.Status), int64(c.TotalAmount), payFrom, payable, c.Remark,
				nowString(), id)
			if err != nil {
				return err
			}
		case err == sql.ErrNoRows:
			var payFrom, payable any
			if c.PayFromAccount != "" {
				v, ok := accIDs[c.PayFromAccount]
				if !ok {
					return fmt.Errorf("%w: 付款科目 %s", ErrNotFound, c.PayFromAccount)
				}
				payFrom = v
			}
			if c.PayableAccount != "" {
				v, ok := accIDs[c.PayableAccount]
				if !ok {
					return fmt.Errorf("%w: 挂账科目 %s", ErrNotFound, c.PayableAccount)
				}
				payable = v
			}
			res, err := tx.Exec(ctx, `
				INSERT INTO expense_claim (code, claimant_employee_id, dept_id,
					apply_date, trip_start, trip_end, destination, reason, status,
					total_amount, pay_from_account_id, payable_account_id, remark,
					created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				c.Code, c.ClaimantEmployeeID, nullInt64(c.DeptID), c.ApplyDate.String(),
				c.TripStart.String(), c.TripEnd.String(), c.Destination, c.Reason,
				string(c.Status), int64(c.TotalAmount), payFrom, payable, c.Remark,
				nowString(), nowString())
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			if err != nil {
				return err
			}
		default:
			return translateErr(err)
		}
		c.ID = id

		for i, it := range c.Items {
			accID, ok := accIDs[it.AccountCode]
			if !ok {
				return fmt.Errorf("%w: 第 %d 行的费用科目 %s",
					ErrNotFound, i+1, it.AccountCode)
			}
			res, err := tx.Exec(ctx, `
				INSERT INTO expense_item (claim_id, line_no, category, occur_date,
					summary, amount, tax_amount, account_id, dept_id, invoice_id)
				VALUES (?,?,?,?,?,?,?,?,?,?)`,
				id, i+1, string(it.Category), it.OccurDate.String(), it.Summary,
				int64(it.Amount), int64(it.TaxAmount), accID,
				nullInt64(it.DeptID), nullInt64(it.InvoiceID))
			if err != nil {
				return err
			}
			itemID, err := res.LastInsertId()
			if err != nil {
				return err
			}
			it.ID, it.ClaimID, it.LineNo = itemID, id, i+1
			if err := r.db.Attachments().linkTx(ctx, tx, "expense_item",
				itemID, it.AttachmentHashes); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// nextClaimCode 生成下一个报销单号。
func (r *ClaimRepo) nextClaimCode(ctx context.Context, d calendar.Date) (string, error) {
	prefix := fmt.Sprintf("BX-%04d-%02d-", d.Year, d.Month)
	var maxCode sql.NullString
	err := r.db.sql.QueryRowContext(ctx,
		`SELECT MAX(code) FROM expense_claim WHERE code LIKE ? || '%'`, prefix).
		Scan(&maxCode)
	if err != nil {
		return "", translateErr(err)
	}
	seq := 1
	if maxCode.Valid && len(maxCode.String) >= 4 {
		var n int
		if _, err := fmt.Sscanf(maxCode.String[len(maxCode.String)-4:], "%d", &n); err == nil {
			seq = n + 1
		}
	}
	return expense.BuildCode(d.Year, d.Month, seq), nil
}

// ApproveClaim 审批报销单。
func (r *ClaimRepo) ApproveClaim(ctx context.Context, id, approverID int64,
	at time.Time) error {
	c, err := r.GetClaim(ctx, id)
	if err != nil {
		return err
	}
	if err := c.Approve(approverID, at); err != nil {
		return err
	}
	_, err = r.db.sql.ExecContext(ctx, `
		UPDATE expense_claim SET status = ?, approver_employee_id = ?,
			approved_at = ?, updated_at = ? WHERE id = ?`,
		string(c.Status), approverID, at.UTC().Format(time.RFC3339Nano),
		nowString(), id)
	return translateErr(err)
}

// RejectClaim 驳回报销单。
func (r *ClaimRepo) RejectClaim(ctx context.Context, id int64, reason string) error {
	c, err := r.GetClaim(ctx, id)
	if err != nil {
		return err
	}
	if err := c.Reject(reason); err != nil {
		return err
	}
	_, err = r.db.sql.ExecContext(ctx,
		`UPDATE expense_claim SET status = ?, remark = ?, updated_at = ? WHERE id = ?`,
		string(c.Status), c.Remark, nowString(), id)
	return translateErr(err)
}

// ---------------------------------------------------------------------------
// 报销单生成凭证
// ---------------------------------------------------------------------------

// PostClaimResult 是报销单过账结果。
type PostClaimResult struct {
	VoucherID int64
	No        string
	Amount    money.Money
}

// PostClaim 为报销单生成凭证并过账。
func (r *ClaimRepo) PostClaim(ctx context.Context, claimID int64,
	postingBy string, at time.Time) (*PostClaimResult, error) {

	if postingBy == "" {
		return nil, fmt.Errorf("expense: 缺少记账人")
	}
	c, err := r.GetClaim(ctx, claimID)
	if err != nil {
		return nil, err
	}
	vc := expense.DefaultVoucherAccounts()
	entries, err := c.BuildEntries(vc)
	if err != nil {
		return nil, err
	}

	var res PostClaimResult
	err = r.db.WithTx(ctx, func(tx *Tx) error {
		accIDs, err := r.db.Accounts().IDsByCodeTx(ctx, tx)
		if err != nil {
			return err
		}
		vcDoc, err := voucher.New(voucher.WordZhuan, c.ApplyDate, postingBy)
		if err != nil {
			return err
		}
		vcDoc.Source = voucher.SourceExpense
		cid := c.ID
		vcDoc.SourceID = &cid
		vcDoc.Remark = fmt.Sprintf("差旅报销 %s", c.Code)

		for _, e := range entries {
			if err := vcDoc.AddEntry(ledger.Entry{
				AccountCode: e.AccountCode,
				Summary:     e.Summary,
				Debit:       e.Debit,
				Credit:      e.Credit,
				Aux: ledger.Aux{
					ContactID:  e.ContactID,
					EmployeeID: e.EmployeeID,
					DeptID:     e.DeptID,
				},
			}); err != nil {
				return err
			}
		}

		out, err := r.db.Vouchers().PostInTx(ctx, tx, PostInput{
			Voucher: vcDoc, Accounts: accIDs, PostingBy: postingBy, At: at,
		})
		if err != nil {
			return err
		}
		res.VoucherID, res.No, res.Amount = out.VoucherID, out.No, c.TotalAmount

		_, err = tx.Exec(ctx, `
			UPDATE expense_claim SET status = 'posted', voucher_id = ?, updated_at = ?
			 WHERE id = ?`, out.VoucherID, nowString(), claimID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// ---------------------------------------------------------------------------
// 附件元数据
// ---------------------------------------------------------------------------

// AttachmentRepo 是附件元数据的持久化访问。
//
// 文件实体由 internal/attachment.Store 负责，这里只管数据库记录。
type AttachmentRepo struct{ db *DB }

// Attachments 返回附件仓储。
func (db *DB) Attachments() *AttachmentRepo { return &AttachmentRepo{db: db} }

// Meta 是附件元数据。
type Meta struct {
	ID       int64
	OwnerTyp string
	OwnerID  int64
	FileName string
	SHA256   string
	Mime     string
	Size     int64
	Kind     string
}

// HashesOf 返回某单据的附件 hash 列表。
func (r *AttachmentRepo) HashesOf(ctx context.Context, ownerType string,
	ownerID int64) ([]string, error) {
	metas, err := r.ListOf(ctx, ownerType, ownerID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(metas))
	for _, m := range metas {
		out = append(out, m.SHA256)
	}
	return out, nil
}

// ListOf 返回某单据的全部附件元数据。
func (r *AttachmentRepo) ListOf(ctx context.Context, ownerType string,
	ownerID int64) ([]*Meta, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT id, owner_type, owner_id, file_name, sha256, mime, size, kind
		  FROM attachment WHERE owner_type = ? AND owner_id = ?
		 ORDER BY id`, ownerType, ownerID)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []*Meta
	for rows.Next() {
		m := &Meta{}
		if err := rows.Scan(&m.ID, &m.OwnerTyp, &m.OwnerID, &m.FileName,
			&m.SHA256, &m.Mime, &m.Size, &m.Kind); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// linkTx 在事务内把一组 hash 关联到某单据（已存在则忽略）。
func (r *AttachmentRepo) linkTx(ctx context.Context, tx *Tx, ownerType string,
	ownerID int64, hashes []string) error {
	for _, h := range hashes {
		if h == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT OR IGNORE INTO attachment
				(owner_type, owner_id, file_name, sha256, mime, size, kind, created_at)
			VALUES (?,?,?,?,?,0,'',?)`,
			ownerType, ownerID, "", h, "", nowString()); err != nil {
			return err
		}
	}
	return nil
}

// LinkDetailed 关联附件并记录文件名与类型（上传时用）。
func (r *AttachmentRepo) LinkDetailed(ctx context.Context, ownerType string,
	ownerID int64, fileName, sha256, mime string, size int64, kind string) error {
	return r.db.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT OR REPLACE INTO attachment
				(owner_type, owner_id, file_name, sha256, mime, size, kind, created_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			ownerType, ownerID, fileName, sha256, mime, size, kind, nowString())
		return err
	})
}

// AllHashes 返回全部被引用的附件 hash（去重），供「附件清理」比对。
func (r *AttachmentRepo) AllHashes(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.sql.QueryContext(ctx,
		`SELECT sha256, COUNT(*) FROM attachment GROUP BY sha256`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var h string
		var n int
		if err := rows.Scan(&h, &n); err != nil {
			return nil, err
		}
		out[h] = n
	}
	return out, rows.Err()
}

// DeleteAttachment 删除一条附件关联。
func (r *AttachmentRepo) DeleteAttachment(ctx context.Context, id int64) error {
	res, err := r.db.sql.ExecContext(ctx, `DELETE FROM attachment WHERE id = ?`, id)
	if err != nil {
		return translateErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: 附件 id=%d", ErrNotFound, id)
	}
	return nil
}

// UnreferencedHashes 返回数据库里已无记录的附件 hash。
//
// 与 attachment.Store 扫出来的物理文件列表比对，
// 就能得到可回收的孤儿文件与缺失文件。
func (r *AttachmentRepo) UnreferencedHashes(ctx context.Context,
	onDisk []string) ([]string, error) {
	refs, err := r.AllHashes(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, h := range onDisk {
		if _, ok := refs[h]; !ok {
			out = append(out, h)
		}
	}
	return out, nil
}

// MissingHashes 返回数据库里有记录但磁盘上找不到的附件。
//
// 这直接说明备份不完整或文件被误删，必须显式告警而不是静默跳过。
func (r *AttachmentRepo) MissingHashes(ctx context.Context,
	onDisk []string) ([]string, error) {
	refs, err := r.AllHashes(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, h := range onDisk {
		have[h] = true
	}
	var out []string
	for h := range refs {
		if !have[h] {
			out = append(out, h)
		}
	}
	return out, nil
}

// sortStrings 保持输出稳定（供测试与界面展示）。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

var (
	_ = sortStrings
	_ = period.Key{}
)

// ---------------------------------------------------------------------------
// 发票生成凭证
// ---------------------------------------------------------------------------

// InvoiceVoucherAccounts 是发票生成凭证所需的科目配置。
type InvoiceVoucherAccounts struct {
	// DefaultExpense 是进项发票的费用归集科目（未指定时用）。
	DefaultExpense string
	// DefaultIncome 是销项发票的收入科目。
	DefaultIncome string
	// Payable 是进项发票未付款时的「应付账款」。
	Payable string
	// Receivable 是销项发票未收款时的「应收账款」。
	Receivable string
	// InputTax 是「应交税费—应交增值税—进项税额」。
	InputTax string
	// OutputTax 是「应交税费—应交增值税—销项税额」。
	OutputTax string
}

// DefaultInvoiceVoucherAccounts 返回按本项目预置科目表的默认配置。
func DefaultInvoiceVoucherAccounts() InvoiceVoucherAccounts {
	return InvoiceVoucherAccounts{
		DefaultExpense: "560206", // 管理费用—办公费
		DefaultIncome:  "5001",   // 主营业务收入
		Payable:        "2202",   // 应付账款
		Receivable:     "1122",   // 应收账款
		InputTax:       "22210101",
		OutputTax:      "22210102",
	}
}

// PostInvoiceResult 是发票生成凭证的结果。
type PostInvoiceResult struct {
	VoucherID int64
	No        string
}

// PostInvoice 为一张发票生成凭证。
//
// # 为什么发票**不**直接记账，而是先生成凭证
//
// 发票是**单据**，凭证是**账**。一张发票可能：
//
//   - 已收到货但没付款 → 借费用+进项税 / 贷应付账款
//   - 已付款            → 借费用+进项税 / 贷银行存款
//   - 是销项票          → 借应收账款 / 贷收入+销项税
//
// 这三种情况的科目组合完全不同，而发票档案本身并不知道钱付没付。
// 因此这里只做「有确定答案」的那一半：进项票挂应付、销项票挂应收，
// 之后由收付款凭证去冲。发票与凭证双向关联（voucher.source_id），
// 界面上一眼能看出哪张票已经入账、哪张还没有。
func (r *InvoiceRepo) PostInvoice(ctx context.Context, invoiceID int64,
	postingBy string, at time.Time, expenseAccount string,
	deptID *int64) (*PostInvoiceResult, error) {

	if postingBy == "" {
		return nil, fmt.Errorf("invoice: 缺少记账人")
	}
	inv, err := r.GetInvoice(ctx, invoiceID)
	if err != nil {
		return nil, err
	}
	if inv.VoucherID != nil {
		return nil, fmt.Errorf("%w: 发票 %s 已生成过凭证", ErrConflict, inv.Identity())
	}
	if err := inv.Validate(); err != nil {
		return nil, err
	}

	vc := DefaultInvoiceVoucherAccounts()
	if expenseAccount != "" {
		vc.DefaultExpense = expenseAccount
	}

	var res PostInvoiceResult
	err = r.db.WithTx(ctx, func(tx *Tx) error {
		accIDs, err := r.db.Accounts().IDsByCodeTx(ctx, tx)
		if err != nil {
			return err
		}

		v, err := voucher.New(voucher.WordJi, inv.InvoiceDate, postingBy)
		if err != nil {
			return err
		}
		v.Source = voucher.SourceInvoice
		id := inv.ID
		v.SourceID = &id
		summary := invoiceSummary(inv)
		v.Remark = summary

		var entries []ledger.Entry
		switch inv.Direction {
		case invoice.DirInput:
			// 进项：借 费用 + 进项税 / 贷 应付账款
			entries = append(entries, ledger.Entry{
				AccountCode: vc.DefaultExpense, Summary: summary,
				Debit: inv.CostAmount(),
				Aux:   ledger.Aux{ContactID: inv.ContactID, DeptID: deptID},
			})
			if tax := inv.DeductibleTax(); tax.IsPositive() {
				entries = append(entries, ledger.Entry{
					AccountCode: vc.InputTax, Summary: summary + "（进项税额）",
					Debit: tax,
				})
			}
			entries = append(entries, ledger.Entry{
				AccountCode: vc.Payable, Summary: summary,
				Credit: inv.TotalAmount, Aux: ledger.Aux{ContactID: inv.ContactID},
			})
		case invoice.DirOutput:
			// 销项：借 应收账款 / 贷 收入 + 销项税
			entries = append(entries, ledger.Entry{
				AccountCode: vc.Receivable, Summary: summary,
				Debit: inv.TotalAmount, Aux: ledger.Aux{ContactID: inv.ContactID},
			})
			entries = append(entries, ledger.Entry{
				AccountCode: vc.DefaultIncome, Summary: summary,
				Credit: inv.AmountExTax,
			})
			if inv.TaxAmount.IsPositive() {
				entries = append(entries, ledger.Entry{
					AccountCode: vc.OutputTax, Summary: summary + "（销项税额）",
					Credit: inv.TaxAmount,
				})
			}
		default:
			return fmt.Errorf("invoice: 发票 %s 的进销方向非法", inv.Identity())
		}

		for i, e := range entries {
			if err := v.AddEntry(e); err != nil {
				return fmt.Errorf("第 %d 行：%w", i+1, err)
			}
		}

		pres, err := r.db.Vouchers().PostInTx(ctx, tx, PostInput{
			Voucher: v, Accounts: accIDs, PostingBy: postingBy, At: at,
		})
		if err != nil {
			return err
		}
		// ★ 发票状态的取值由 CHECK 约束限定为
		// pending / verified / booked / voided —— 生成凭证对应 booked（已入账），
		// 不是 'posted'。写错值会被数据库直接拒绝。
		if _, err := tx.Exec(ctx, `
			UPDATE invoice SET voucher_id = ?, status = 'booked', updated_at = ?
			 WHERE id = ?`, pres.VoucherID, nowString(), inv.ID); err != nil {
			return translateErr(err)
		}
		res.VoucherID, res.No = pres.VoucherID, pres.No
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// invoiceSummary 拼出凭证摘要。
//
// 摘要里带发票号是刻意的：税务检查时是从发票倒查凭证，
// 摘要里有号码才能一眼对上。
func invoiceSummary(inv *invoice.Invoice) string {
	who := inv.SellerName
	if inv.Direction == invoice.DirOutput {
		who = inv.BuyerName
	}
	switch {
	case who != "" && inv.Number != "":
		return fmt.Sprintf("%s %s（发票号 %s）",
			inv.Direction.Label(), who, inv.Number)
	case who != "":
		return fmt.Sprintf("%s %s", inv.Direction.Label(), who)
	default:
		return inv.Direction.Label() + "发票"
	}
}
