package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
)

// VoucherRepo 是凭证与总账的持久化访问。
type VoucherRepo struct{ db *DB }

// Vouchers 返回凭证仓储。
func (db *DB) Vouchers() *VoucherRepo { return &VoucherRepo{db: db} }

// 凭证相关错误。
var (
	ErrNoOpenPeriod = fmt.Errorf("%w: 没有已启用的会计期间", period.ErrNotOpen)
	ErrVoucherState = fmt.Errorf("voucher: 状态不允许该操作")
)

// ---------------------------------------------------------------------------
// 写入
// ---------------------------------------------------------------------------

// PostInput 是过账所需的全部输入。
type PostInput struct {
	Voucher *voucher.Voucher
	// Accounts 用于把科目编码解析成外键，避免每次过账都查库。
	Accounts map[string]int64
	// PostingBy 是记账人（《会计法》要求记账凭证需有记账签章）。
	PostingBy string
	// At 是过账时刻，便于测试注入固定时间。
	At time.Time
}

// PostResult 是过账结果。
type PostResult struct {
	VoucherID int64
	No        string // 分配到的凭证号，如「记-2025-09-0001」
	LedgerIDs []int64
}

// Post 在一个事务内完成过账：
//
//  1. 校验凭证当前状态允许过账
//  2. 校验所属期间为 open（数据库中的期间状态是权威）
//  3. **执行完整的领域校验**（7 条不变式）—— 存储层强制，调用方无法绕过
//  4. **分配期间内凭证序号**（靠唯一索引兜底）
//  5. 写凭证主表与分录
//  6. 写总账分录
//  7. 迁移凭证状态为 posted 并写入记账签章
//
// 这六步必须在同一事务内完成。Frappe Books 过账时逐条 insert 且没有事务，
// 中途失败会留下半张凭证的总账，且无机制修复 —— 这是本项目必须修复的缺陷。
func (r *VoucherRepo) Post(ctx context.Context, in PostInput) (*PostResult, error) {
	var out *PostResult
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		var e error
		out, e = r.PostInTx(ctx, tx, in)
		return e
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PostInTx 在**调用方给定的**事务内完成过账。
//
// 拆出这一层是为了让银行流水批量过账能复用同一套逻辑：
// 一个批次里的多条流水必须在同一个事务里全部成功或全部回滚，
// 不能每条各开一个事务。
//
// 调用方负责提交/回滚，并保证 tx 与 r.db 是同一个库。
func (r *VoucherRepo) PostInTx(ctx context.Context, tx *Tx, in PostInput) (*PostResult, error) {
	v := in.Voucher
	if v == nil {
		return nil, fmt.Errorf("%w: 凭证为空", ErrVoucherState)
	}
	if err := v.CanPost(); err != nil {
		return nil, err
	}
	k := period.Key{Year: v.BizDate.Year, Month: v.BizDate.Month}
	v.Period = k

	at := in.At
	if at.IsZero() {
		at = time.Now()
	}

	var out PostResult
	{
		// 2. 期间状态以数据库为准（内存中的 Calendar 可能已过期）
		var status string
		err := tx.QueryRow(ctx,
			`SELECT status FROM period WHERE year = ? AND month = ?`, k.Year, k.Month).Scan(&status)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: %s", period.ErrPeriodNotFound, k)
		}
		if err != nil {
			return nil, translateErr(err)
		}
		if period.Status(status) != period.StatusOpen {
			return nil, fmt.Errorf("%w: %s 当前状态为 %s",
				period.ErrNotOpen, k, period.Status(status).Label())
		}

		// 2b. 小规模纳税人不得抵扣进项税额。
		//
		// ★ 一收口在这里挡，而不是靠各业务模块自觉。
		//
		// 账套的税种在建账时确定，但科目表是**同一份** ——
		// 小规模账套同样有「应交增值税—进项税额」。于是发票模块
		// 把专票的进项税挂到 22210101 时会**静默成功**，
		// 做出一笔「借 管理费用 10000 / 借 进项税额 1300 / 贷 应付账款 11300」——
		// 费用少计 1300、进项税虚挂 1300，而小规模根本不能抵。
		//
		// 报错而不是照记：错账比记不上账难查得多。
		if err := rejectInputVATForSmallBook(ctx, tx, in.Voucher); err != nil {
			return nil, err
		}

		// 3. 领域校验 —— 必须在事务内、写任何数据之前完成
		//
		// 存储层强制校验，而不是把责任推给调用方：否则任何一个忘记调
		// Validate 的调用点都会把「记到汇总科目」「辅助核算缺失」「借贷不平衡」
		// 的凭证直接写进总账，而总账一旦脏了，后面所有报表都是错的。
		// 校验上下文在同一事务内加载，保证看到的是最新科目树与往来档案。
		vctx, err := r.validationContext(ctx, tx)
		if err != nil {
			return nil, err
		}
		if _, err := v.Validate(vctx); err != nil {
			return nil, err
		}

		// 4. 分配凭证号
		seq, err := nextSeq(ctx, tx, k, v.Word)
		if err != nil {
			return nil, err
		}
		v.Seq = seq
		v.No = voucher.FormatNo(v.Word, k, seq)

		// 5. 写凭证
		//
		// ★ 分两种情况，且必须分清：
		//
		//	v.ID == 0  新建（银行流水、工资、结转等一次性生成的凭证）
		//	v.ID != 0  **把一张已存在的草稿过账**
		//
		// 后者曾经被漏掉：无条件 INSERT 会给同一张草稿再插一行，
		// 结果是「过账后账上多出一张一模一样的草稿」——
		// 而原草稿还留在列表里，用户以为过账失败又点了一次。
		var id int64
		if v.ID == 0 {
			var err error
			id, err = insertVoucher(ctx, tx, v)
			if err != nil {
				return nil, err
			}
			v.ID = id
		} else {
			id = v.ID
			// 草稿的分录在保存时就写进了 voucher_entry，过账时整组替换，
			// 保证「界面上看到的」与「账上记的」严格一致。
			if _, err := tx.Exec(ctx,
				`DELETE FROM voucher_entry WHERE voucher_id = ?`, id); err != nil {
				return nil, translateErr(err)
			}
			if _, err := tx.Exec(ctx, `
				UPDATE voucher SET year = ?, month = ?, word = ?, biz_date = ?,
					attach_count = ?, remark = ?, source = ?, source_id = ?
				 WHERE id = ?`,
				v.Period.Year, v.Period.Month, string(v.Word), v.BizDate.String(),
				v.AttachCount, v.Remark, string(v.Source), nullInt64(v.SourceID),
				id); err != nil {
				return nil, translateErr(err)
			}
		}
		out.VoucherID = id
		out.No = v.No

		// 6. 写凭证分录 + 总账分录
		ledgerIDs, err := insertEntries(ctx, tx, v, in.Accounts, at)
		if err != nil {
			return nil, err
		}
		out.LedgerIDs = ledgerIDs

		// 7. 状态迁移
		if err := v.Post(in.PostingBy, at); err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx,
			`UPDATE voucher SET status = 'posted', seq = ?, no = ?, posted_by = ?,
			        posted_at = ?, updated_at = ? WHERE id = ?`,
			v.Seq, v.No, v.PostedBy, at.UTC().Format(time.RFC3339Nano),
			nowString(), id)
		return &out, err
	}
}

// LoadPeriodsTx 在事务内加载会计期间表。
// 供银行流水批量过账复用（同一批次必须看到同一份期间状态）。
func (r *VoucherRepo) LoadPeriodsTx(ctx context.Context, tx *Tx) (*period.Calendar, error) {
	rows, err := tx.Query(ctx, `
		SELECT year, month, status, closed_by, closed_at FROM period
		 ORDER BY year, month`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		keys   []period.Key
		status = map[period.Key]period.Status{}
		closed = map[period.Key]period.ClosedInfo{}
	)
	for rows.Next() {
		var (
			y, m     int
			st       string
			cb       string
			closedAt sql.NullString
		)
		if err := rows.Scan(&y, &m, &st, &cb, &closedAt); err != nil {
			return nil, err
		}
		kk := period.NewKey(y, m)
		keys = append(keys, kk)
		status[kk] = period.Status(st)
		closed[kk] = period.ClosedInfo{By: cb, At: parseNullTime(closedAt)}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return period.FromRows(keys, status, closed)
}

// validationContext 在同一事务内装配过账校验所需的外部事实。
func (r *VoucherRepo) validationContext(ctx context.Context, tx *Tx) (*ledger.Context, error) {
	tree, err := r.db.Accounts().TreeFrom(ctx, tx)
	if err != nil {
		return nil, err
	}
	kinds, err := r.db.Contacts().KindsFrom(ctx, tx)
	if err != nil {
		return nil, err
	}

	// 期间表也从事务内读，避免用过期状态放行
	rows, err := tx.Query(ctx, `
		SELECT year, month, status, closed_by, closed_at FROM period
		 ORDER BY year, month`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		keys   []period.Key
		status = map[period.Key]period.Status{}
		closed = map[period.Key]period.ClosedInfo{}
	)
	for rows.Next() {
		var (
			y, m     int
			st       string
			cb       string
			closedAt sql.NullString
		)
		if err := rows.Scan(&y, &m, &st, &cb, &closedAt); err != nil {
			return nil, err
		}
		kk := period.NewKey(y, m)
		keys = append(keys, kk)
		status[kk] = period.Status(st)
		closed[kk] = period.ClosedInfo{By: cb, At: parseNullTime(closedAt)}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	cal, err := period.FromRows(keys, status, closed)
	if err != nil {
		return nil, err
	}
	return &ledger.Context{Accounts: tree, Periods: cal, ContactKinds: kinds}, nil
}

// nextSeq 分配「期间 + 凭证字」内的下一个序号。
//
// 正确性由两层保证：
//
//	第一层：本连接是单连接（MaxOpenConns=1）且事务用 BEGIN IMMEDIATE，
//	        因此不存在「两个事务同时读到同一个最大值」的窗口。
//	第二层：voucher 表上的部分唯一索引 ux_voucher_seq 兜底 ——
//	        即使第一层被绕过（例如将来换成多连接），重复插入也会失败而不是静默重号。
//
// Frappe Books 的 NumberSeries 是「读 current → +1 → 查是否存在 → 写回」，
// 全程无事务也无唯一约束，并发下必然重号；而且它是**全局计数器**，
// 不按会计期间分段，与中国「凭证号按期间连续」的要求不符。
func nextSeq(ctx context.Context, tx *Tx, k period.Key, word voucher.Word) (int, error) {
	var maxSeq sql.NullInt64
	err := tx.QueryRow(ctx, `
		SELECT MAX(seq) FROM voucher
		 WHERE year = ? AND month = ? AND word = ? AND status <> 'draft'`,
		k.Year, k.Month, string(word)).Scan(&maxSeq)
	if err != nil {
		return 0, translateErr(err)
	}
	if !maxSeq.Valid {
		return 1, nil
	}
	return int(maxSeq.Int64) + 1, nil
}

func insertVoucher(ctx context.Context, tx *Tx, v *voucher.Voucher) (int64, error) {
	res, err := tx.Exec(ctx, `
		INSERT INTO voucher (year, month, word, seq, no, biz_date, attach_count, remark,
			status, source, source_id, created_by, reviewed_by, posted_by, posted_at,
			reverses_id, voided_by, created_by_ai, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.Period.Year, v.Period.Month, string(v.Word), v.Seq, v.No, v.BizDate.String(),
		v.AttachCount, v.Remark, string(v.Status), string(v.Source), nullInt64(v.SourceID),
		v.CreatedBy, v.ReviewedBy, v.PostedBy, nullTime(v.PostedAt),
		nullInt64(v.ReversesID), nullInt64(v.VoidedBy), boolInt(v.CreatedByAI),
		nowString(), nowString())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// insertEntries 写凭证分录与总账分录。
//
// 两者逐行一一对应：本工程**不合并同科目分录**。
// Frappe Books 会把同科目同方向的多行合并成一行，摘要与辅助核算随之丢失；
// 而中国实务的明细账要求逐行摘要，往来账要求逐行辅助核算。
func insertEntries(ctx context.Context, tx *Tx, v *voucher.Voucher,
	accounts map[string]int64, at time.Time) ([]int64, error) {

	ids := make([]int64, 0, len(v.Entries))
	for i, e := range v.Entries {
		accID, ok := accounts[e.AccountCode]
		if !ok {
			return nil, fmt.Errorf("%w: 科目 %q", ErrNotFound, e.AccountCode)
		}
		lineNo := i + 1

		if _, err := tx.Exec(ctx, `
			INSERT INTO voucher_entry (voucher_id, line_no, summary, account_id,
				debit, credit, contact_id, employee_id, dept_id, project_id)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			v.ID, lineNo, e.Summary, accID,
			int64(e.Debit), int64(e.Credit),
			nullInt64(e.Aux.ContactID), nullInt64(e.Aux.EmployeeID),
			nullInt64(e.Aux.DeptID), nullInt64(e.Aux.ProjectID)); err != nil {
			return nil, err
		}

		res, err := tx.Exec(ctx, `
			INSERT INTO ledger_entry (biz_date, year, month, account_id, debit, credit,
				contact_id, employee_id, dept_id, project_id, voucher_id, line_no,
				summary, reverted, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)`,
			v.BizDate.String(), v.Period.Year, v.Period.Month, accID,
			int64(e.Debit), int64(e.Credit),
			nullInt64(e.Aux.ContactID), nullInt64(e.Aux.EmployeeID),
			nullInt64(e.Aux.DeptID), nullInt64(e.Aux.ProjectID),
			v.ID, lineNo, e.Summary, at.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ---------------------------------------------------------------------------
// 查询
// ---------------------------------------------------------------------------

const voucherColumns = `id, year, month, word, seq, no, biz_date, attach_count, remark,
	status, source, source_id, created_by, reviewed_by, posted_by, posted_at,
	reverses_id, voided_by, created_by_ai, created_at, updated_at`

// Get 按 id 取凭证（含分录）。
func (r *VoucherRepo) Get(ctx context.Context, id int64) (*voucher.Voucher, error) {
	rows, err := r.db.sql.QueryContext(ctx,
		`SELECT `+voucherColumns+` FROM voucher WHERE id = ?`, id)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, fmt.Errorf("%w: 凭证 id=%d", ErrNotFound, id)
	}
	v, err := scanVoucher(rows)
	if err != nil {
		return nil, err
	}
	rows.Close()

	entries, err := loadEntries(ctx, r.db.sql, id)
	if err != nil {
		return nil, err
	}
	v.Entries = entries
	return v, nil
}

// ListByPeriod 返回某期间的全部凭证（不含分录），按凭证字与序号排列。
func (r *VoucherRepo) ListByPeriod(ctx context.Context, k period.Key) ([]*voucher.Voucher, error) {
	rows, err := r.db.sql.QueryContext(ctx,
		`SELECT `+voucherColumns+` FROM voucher
		  WHERE year = ? AND month = ?
		  ORDER BY word, seq, id`, k.Year, k.Month)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	var out []*voucher.Voucher
	for rows.Next() {
		v, err := scanVoucher(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CountBySource 统计某来源单据已生成的凭证数，用于「流水↔凭证」双向关联的去重。
func (r *VoucherRepo) CountBySource(ctx context.Context, src voucher.Source, sourceID int64) (int, error) {
	var n int
	err := r.db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM voucher WHERE source = ? AND source_id = ?`,
		string(src), sourceID).Scan(&n)
	return n, translateErr(err)
}

// loadEntries 从任意 querier 读取某凭证的分录。
//
// 做成自由函数而不是仓储方法：结账、反结账等流程需要在**事务内**
// 读分录，若绑死到仓储就只能走 db.sql —— 单连接池下必然死锁。
func loadEntries(ctx context.Context, q querier, voucherID int64) ([]ledger.Entry, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT a.code, ve.account_id, ve.summary, ve.debit, ve.credit,
		       ve.contact_id, ve.employee_id, ve.dept_id, ve.project_id
		  FROM voucher_entry ve
		  JOIN account a ON a.id = ve.account_id
		 WHERE ve.voucher_id = ?
		 ORDER BY ve.line_no`, voucherID)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []ledger.Entry
	for rows.Next() {
		var (
			e         ledger.Entry
			debit     int64
			credit    int64
			contactID sql.NullInt64
			empID     sql.NullInt64
			deptID    sql.NullInt64
			projID    sql.NullInt64
		)
		if err := rows.Scan(&e.AccountCode, &e.AccountID, &e.Summary, &debit, &credit,
			&contactID, &empID, &deptID, &projID); err != nil {
			return nil, err
		}
		e.Debit = money.Money(debit)
		e.Credit = money.Money(credit)
		e.Aux = ledger.Aux{
			ContactID:  toNullInt64(contactID),
			EmployeeID: toNullInt64(empID),
			DeptID:     toNullInt64(deptID),
			ProjectID:  toNullInt64(projID),
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanVoucher(rows *sql.Rows) (*voucher.Voucher, error) {
	v := &voucher.Voucher{}
	var (
		word, status, source       string
		bizDate                    string
		sourceID, reverses, voided sql.NullInt64
		createdByAI                int
		postedAt                   sql.NullString
		createdAt, updatedAt       string
	)
	if err := rows.Scan(&v.ID, &v.Period.Year, &v.Period.Month, &word, &v.Seq, &v.No,
		&bizDate, &v.AttachCount, &v.Remark, &status, &source, &sourceID,
		&v.CreatedBy, &v.ReviewedBy, &v.PostedBy, &postedAt,
		&reverses, &voided, &createdByAI, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	d, err := calendar.Parse(bizDate)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 凭证 %d 日期非法 %q: %w", v.ID, bizDate, err)
	}
	v.BizDate = d
	v.Word = voucher.Word(word)
	v.Status = voucher.Status(status)
	v.Source = voucher.Source(source)
	v.SourceID = toNullInt64(sourceID)
	v.ReversesID = toNullInt64(reverses)
	v.VoidedBy = toNullInt64(voided)
	v.CreatedByAI = createdByAI != 0
	v.PostedAt = parseNullTime(postedAt)
	v.CreatedAt = parseTime(createdAt)
	v.UpdatedAt = parseTime(updatedAt)
	return v, nil
}

// ---------------------------------------------------------------------------
// 总账查询
// ---------------------------------------------------------------------------

// AccountBalance 是一个科目的期间余额汇总。
type AccountBalance struct {
	AccountCode string
	AccountID   int64
	Debit       money.Money // 期初+本期 借方发生额
	Credit      money.Money
}

// Balance 返回余额 = 借方合计 − 贷方合计（原始口径，未按余额方向取反）。
// 期间参数为闭区间 [from, to]，与 calendar.Range 一致。
//
// # 为什么不过滤 reverted
//
// 本工程采用**红字凭证**模型（中国实务）而非「反向分录」模型：
//
//	原凭证过账       → 一组分录
//	作废时另开红字凭证 → 借贷互换的另一组分录，原凭证标记 status='voided'
//
// 两组分录**都保留在账上并都参与汇总**，红字凭证自然把原凭证抵消，
// 净额为零。这样做的两个好处：
//
//  1. 红字凭证是一张有独立编号、可打印、可签章的真实凭证，
//     符合中国实务与《会计法》的凭证归档要求；
//  2. 明细账里能同时看到原分录与红字分录，审计轨迹完整。
//
// 因此 reverted 只是一枚**审计标记**（表示「所属凭证已作废」），
// 用于界面标注与追溯，**不参与报表过滤**。
//
// 这一点与 Frappe Books 相反：它把原分录与镜像分录**都**标为 reverted=1
// 并在所有报表里过滤掉，代价是冲销不产生真实凭证，
// 且各报表的过滤条件一旦漏写就会算错。
func (r *VoucherRepo) Balance(ctx context.Context, from, to calendar.Date) (map[string]money.Money, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT a.code, SUM(le.debit) AS d, SUM(le.credit) AS c
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		 WHERE le.biz_date BETWEEN ? AND ?
		 GROUP BY a.code`, from.String(), to.String())
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	out := map[string]money.Money{}
	for rows.Next() {
		var code string
		var d, c sql.NullInt64
		if err := rows.Scan(&code, &d, &c); err != nil {
			return nil, err
		}
		out[code] = money.Money(d.Int64 - c.Int64)
	}
	return out, rows.Err()
}

// LedgerRow 是明细账的一行。
type LedgerRow struct {
	EntryID     int64
	BizDate     calendar.Date
	VoucherID   int64
	VoucherNo   string
	AccountCode string
	Summary     string
	Debit       money.Money
	Credit      money.Money
	ContactID   *int64
	// ContraAccounts 是**对方科目**：同一张凭证里其他分录所在的科目名。
	//
	// 日记账（现金/银行存款）最要紧的一列。会计看银行存款日记账时，
	// 真正想知道的是「这笔钱从哪来、到哪去」—— 只给借贷金额是不够的。
	//
	// 一张凭证可能有多条其他分录（如 借 银行存款 / 贷 主营业务收入
	// + 贷 应交税费），因此是**列表**而不是单个字符串；
	// 展示时用「、」连接。顺序按科目编码排，保证同样输入结果稳定。
	ContraAccounts []string
}

// Detail 返回某科目的明细账，按日期与凭证号升序。
//
// 这是「其他应付款—股东明细账」等报表的取数基础：
// 传入科目编码（或前缀）即可。
func (r *VoucherRepo) Detail(ctx context.Context, accountCodePrefix string,
	from, to calendar.Date) ([]LedgerRow, error) {

	// 对方科目：同一张凭证里其他分录的科目编码，用 group_concat 拼成一列。
	//
	// 用**科目编码**而不是科目名做拼接：编码是受校验的（只有数字），
	// 绝不会含逗号；科目名是用户可改的，万一含逗号就会把分隔符弄乱，
	// 于是一个科目被拆成两个不存在的科目。取名放到 Go 侧做。
	//
	// 不用自定义分隔符：SQLite 的 group_concat 在带 DISTINCT 时
	// 只接受一个参数，塞第二个会直接报错。
	// ★ 科目名必须在**打开游标之前**取好。
	//
	// 连接池是 MaxOpenConns(1)：游标没关之前那条唯一的连接被占着，
	// 此时再发一条查询会一直等下去 —— 直接死锁，而不是报错。
	// 这个坑在内存库上必现，在文件库上则取决于连接复用，
	// 属于「本地跑得通、换台机器就挂」的那一类。
	names, err := r.codeToName(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT le.id, le.biz_date, le.voucher_id, v.no, a.code, le.summary,
		       le.debit, le.credit, le.contact_id,
		       (SELECT group_concat(DISTINCT ca.code)
		          FROM ledger_entry cle
		          JOIN account ca ON ca.id = cle.account_id
		         WHERE cle.voucher_id = le.voucher_id AND cle.id <> le.id)
		  FROM ledger_entry le
		  JOIN account a  ON a.id = le.account_id
		  JOIN voucher v  ON v.id = le.voucher_id
		 WHERE a.code LIKE ? || '%'
		   AND le.biz_date BETWEEN ? AND ?
		 ORDER BY le.biz_date, v.no, le.line_no`,
		accountCodePrefix, from.String(), to.String())
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []LedgerRow
	for rows.Next() {
		var (
			row       LedgerRow
			dateStr   string
			d, c      int64
			contact   sql.NullInt64
			contraRaw sql.NullString
		)
		if err := rows.Scan(&row.EntryID, &dateStr, &row.VoucherID, &row.VoucherNo,
			&row.AccountCode, &row.Summary, &d, &c, &contact, &contraRaw); err != nil {
			return nil, err
		}
		dt, err := calendar.Parse(dateStr)
		if err != nil {
			return nil, err
		}
		row.BizDate = dt
		row.Debit = money.Money(d)
		row.Credit = money.Money(c)
		row.ContactID = toNullInt64(contact)
		row.ContraAccounts = contraNames(contraRaw.String, names)
		out = append(out, row)
	}
	return out, rows.Err()
}

// codeToName 返回科目编码 → 名称（短名，如「办公费」）。
//
// 用短名而不是「管理费用—办公费」：对方科目是明细账里的一列，
// 列宽有限，而「管理费用」这四个字在同一张表里往往已经由上下文给出。
func (r *VoucherRepo) codeToName(ctx context.Context) (map[string]string, error) {
	list, err := r.db.Accounts().List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list))
	for _, a := range list {
		out[a.Code] = shortAccountName(a.Name)
	}
	return out, nil
}

// contraNames 把逗号分隔的科目编码列表翻成科目名列表。
//
// 排序与去重都在这里做（SQL 的 DISTINCT 只保证去重，不保证顺序），
// 保证同样的凭证每次都得到同样的显示顺序。
func contraNames(raw string, names map[string]string) []string {
	if raw == "" {
		return nil
	}
	seen := map[string]bool{}
	var list []string
	for _, code := range strings.Split(raw, ",") {
		code = strings.TrimSpace(code)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		name, ok := names[code]
		if !ok || name == "" {
			// 科目被删过：退化为显示编码，而不是显示一个空白
			name = code
		}
		list = append(list, code+"\x00"+name)
	}
	sort.Strings(list)
	out := make([]string, 0, len(list))
	for _, v := range list {
		if i := strings.IndexByte(v, 0); i >= 0 {
			out = append(out, v[i+1:])
		}
	}
	return out
}

// shortAccountName 从「管理费用—办公费」取出「办公费」。
func shortAccountName(name string) string {
	if i := strings.LastIndex(name, "—"); i >= 0 {
		if s := strings.TrimSpace(name[i+len("—"):]); s != "" {
			return s
		}
	}
	return name
}

// TrialBalance 汇总全账套借贷合计，用于试算平衡校验。
func (r *VoucherRepo) TrialBalance(ctx context.Context, k period.Key) (debit, credit money.Money, err error) {
	var d, c sql.NullInt64
	err = r.db.sql.QueryRowContext(ctx, `
		SELECT SUM(debit), SUM(credit) FROM ledger_entry
		 WHERE year = ? AND month = ?`, k.Year, k.Month).Scan(&d, &c)
	if err != nil {
		return 0, 0, translateErr(err)
	}
	return money.Money(d.Int64), money.Money(c.Int64), nil
}

// ---------------------------------------------------------------------------
// 期间仓储
// ---------------------------------------------------------------------------

// PeriodRepo 是会计期间的持久化访问。
type PeriodRepo struct{ db *DB }

// Periods 返回期间仓储。
func (db *DB) Periods() *PeriodRepo { return &PeriodRepo{db: db} }

// Load 从数据库重建期间表。
func (r *PeriodRepo) Load(ctx context.Context) (*period.Calendar, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT year, month, start_date, end_date, status, closed_at, closed_by
		  FROM period ORDER BY year, month`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var (
		keys   []period.Key
		status = map[period.Key]period.Status{}
		closed = map[period.Key]period.ClosedInfo{}
	)
	for rows.Next() {
		var (
			y, m     int
			sd, ed   string
			st       string
			closedAt sql.NullString
			closedBy string
		)
		if err := rows.Scan(&y, &m, &sd, &ed, &st, &closedAt, &closedBy); err != nil {
			return nil, err
		}
		k := period.NewKey(y, m)
		keys = append(keys, k)
		status[k] = period.Status(st)
		closed[k] = period.ClosedInfo{By: closedBy, At: parseNullTime(closedAt)}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return period.FromRows(keys, status, closed)
}

// Insert 批量写入期间行（建账时调用）。
func (r *PeriodRepo) Insert(ctx context.Context, tx *Tx, periods []*period.Period) error {
	for _, p := range periods {
		if _, err := tx.Exec(ctx, `
			INSERT INTO period (year, month, start_date, end_date, status, closed_by)
			VALUES (?,?,?,?,?,?)`,
			p.Key.Year, p.Key.Month, p.Range.From.String(), p.Range.To.String(),
			string(p.Status), p.ClosedBy); err != nil {
			return err
		}
	}
	return nil
}

// SetStatus 更新期间状态。
func (r *PeriodRepo) SetStatus(ctx context.Context, tx *Tx, k period.Key,
	st period.Status, closedBy string, closedAt *time.Time) error {

	res, err := tx.Exec(ctx, `
		UPDATE period SET status = ?, closed_by = ?, closed_at = ?
		 WHERE year = ? AND month = ?`,
		string(st), closedBy, nullTime(closedAt), k.Year, k.Month)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", period.ErrPeriodNotFound, k)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 时间辅助
// ---------------------------------------------------------------------------

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseNullTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return nil
	}
	return &t
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ---------------------------------------------------------------------------
// 草稿的增删改
// ---------------------------------------------------------------------------

// 草稿相关错误。
var (
	ErrNotDraft      = errors.New("voucher: 只有草稿可以修改或删除")
	ErrNotReversible = errors.New("voucher: 该凭证不能红字冲销")
)

// SaveDraft 在事务内创建或更新一张**草稿**凭证。
//
// # 为什么草稿的写法和过账分开
//
// 过账（Post）会分配凭证号、写总账、改状态，是一个不可逆的账务动作；
// 而草稿只是「还没想好的输入」——它不占号、不进总账、可以反复改。
// 把两者塞进同一个方法，就会出现「保存时不小心把凭证过掉了」这类事故。
//
// 因此这里只写 voucher + voucher_entry 两张表，
// **不写 ledger_entry**，也不分配序号（seq=0，展示号为空）。
func (r *VoucherRepo) SaveDraft(ctx context.Context, in DraftInput) (*DraftResult, error) {
	v := in.Voucher
	if v == nil {
		return nil, fmt.Errorf("%w: 凭证为空", ErrVoucherState)
	}
	if in.CreatedBy == "" && v.CreatedBy == "" {
		return nil, voucher.ErrMissingMaker
	}
	if !v.BizDate.Valid() {
		return nil, voucher.ErrBadDate
	}
	k := period.Key{Year: v.BizDate.Year, Month: v.BizDate.Month}
	v.Period = k

	// 草稿也要过一遍领域校验中与「能不能记账」无关的部分：
	// 分录数、借贷平衡、摘要非空、科目存在且可记账。
	// 否则用户会一直改到点「过账」才发现问题，白费功夫。
	var out DraftResult
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		if !in.SkipValidation {
			vctx, err := r.validationContext(ctx, tx)
			if err != nil {
				return err
			}
			if _, err := v.ValidateDraft(vctx); err != nil {
				return err
			}
		}
		accounts, err := r.db.Accounts().IDsByCodeTx(ctx, tx)
		if err != nil {
			return err
		}

		if v.ID == 0 {
			id, err := insertVoucher(ctx, tx, v)
			if err != nil {
				return err
			}
			v.ID = id
		} else {
			// 只允许改草稿 —— 已过账的凭证只能红字冲销
			var status string
			if err := tx.QueryRow(ctx,
				`SELECT status FROM voucher WHERE id = ?`, v.ID).Scan(&status); err != nil {
				return translateErr(err)
			}
			if voucher.Status(status) != voucher.StatusDraft {
				return fmt.Errorf("%w（当前状态 %s）", ErrNotDraft, status)
			}
			if _, err := tx.Exec(ctx, `
				UPDATE voucher SET year = ?, month = ?, word = ?, biz_date = ?,
					attach_count = ?, remark = ?, updated_at = ?
				 WHERE id = ?`,
				k.Year, k.Month, string(v.Word), v.BizDate.String(),
				v.AttachCount, v.Remark, nowString(), v.ID); err != nil {
				return translateErr(err)
			}
			// 分录整体替换：草稿的「改一行」在界面上就是改一行，
			// 但存下来最简单也最不容易出错的表达是「整组替换」。
			// 草稿不占号、无总账，替换没有任何副作用。
			if _, err := tx.Exec(ctx,
				`DELETE FROM voucher_entry WHERE voucher_id = ?`, v.ID); err != nil {
				return translateErr(err)
			}
		}

		ids, err := insertDraftEntries(ctx, tx, v, accounts)
		if err != nil {
			return err
		}
		out.VoucherID = v.ID
		out.EntryIDs = ids
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DraftInput 是保存草稿的输入。
type DraftInput struct {
	Voucher   *voucher.Voucher
	CreatedBy string
	// SkipValidation 仅供迁移与测试使用。
	SkipValidation bool
}

// DraftResult 是保存草稿的结果。
type DraftResult struct {
	VoucherID int64
	EntryIDs  []int64
}

// DeleteDraft 删除一张草稿凭证。
//
// ★ 只有草稿能删。已过账的凭证**任何情况下都不能删** ——
// 那是会计档案，只能红字冲销。删除会让凭证号断号、
// 让已上报的报表失去可追溯的对应关系。
func (r *VoucherRepo) DeleteDraft(ctx context.Context, id int64) error {
	return r.db.WithTx(ctx, func(tx *Tx) error {
		var status string
		if err := tx.QueryRow(ctx,
			`SELECT status FROM voucher WHERE id = ?`, id).Scan(&status); err != nil {
			return translateErr(err)
		}
		if voucher.Status(status) != voucher.StatusDraft {
			return fmt.Errorf("%w（当前状态 %s）", ErrNotDraft, status)
		}
		// voucher_entry 有 ON DELETE CASCADE；草稿没有总账分录，
		// 因此删完这两处不会留下任何孤儿数据。
		if _, err := tx.Exec(ctx, `DELETE FROM voucher WHERE id = ?`, id); err != nil {
			return translateErr(err)
		}
		return nil
	})
}

// Reverse 对一张已过账凭证做红字冲销，返回冲销凭证的 id 与号。
//
// # 冲销日期
//
// 默认沿用原凭证日期 —— 这样原凭证与红字凭证落在**同一期间**，
// 该期间的净额自然归零，报表不用做任何特殊处理。
// 若原期间已结账，调用方必须显式指定一个开放期间的日期：
// 硬写回已结账期间会被过账校验拒绝，而校验拒绝是对的 ——
// 已结账期间的数据不该被事后改动。
func (r *VoucherRepo) Reverse(ctx context.Context, in ReverseInput) (*PostResult, error) {
	var out *PostResult
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		v, err := loadVoucherTx(ctx, tx, in.VoucherID)
		if err != nil {
			return err
		}
		if v.Status != voucher.StatusPosted {
			return fmt.Errorf("%w（当前状态 %s）", ErrNotReversible, v.Status)
		}
		date := in.BizDate
		if !date.Valid() {
			date = v.BizDate
		}
		rev, err := v.BuildReversal(in.By, date)
		if err != nil {
			return err
		}
		ids, err := r.db.Accounts().IDsByCodeTx(ctx, tx)
		if err != nil {
			return err
		}
		res, err := r.PostInTx(ctx, tx, PostInput{
			Voucher: rev, Accounts: ids, PostingBy: in.By, At: in.At,
		})
		if err != nil {
			return err
		}
		if err := markVoided(ctx, tx, v.ID, res.VoucherID); err != nil {
			return err
		}
		out = res
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReverseInput 是红字冲销的输入。
type ReverseInput struct {
	VoucherID int64
	By        string
	// BizDate 留空时沿用原凭证日期（保证落在同一期间）。
	BizDate calendar.Date
	At      time.Time
}

// inputVATColumns 是「应交增值税」下的借方专栏里属于**进项抵扣**的那些。
//
// 只挡这几个，不挡销项税额/转出等 —— 小规模同样要确认应交增值税。
var inputVATColumns = map[string]bool{
	"22210101": true, // 进项税额
	"22210118": true, // 销项税额抵减
	"22210110": true, // 待抵扣进项税额（旧编码，迁移后为 222119）
	"222119":   true, // 待抵扣进项税额
	"222120":   true, // 待认证进项税额
}

// rejectInputVATForSmallBook 阻止小规模纳税人账套使用进项抵扣专栏。
func rejectInputVATForSmallBook(ctx context.Context, tx *Tx, v *voucher.Voucher) error {
	if v == nil || len(v.Entries) == 0 {
		return nil
	}
	var taxType string
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(tax_type,'') FROM book WHERE id = 1`).Scan(&taxType); err != nil {
		// 读不到账套信息就放行：这是防错，不是权限校验，
		// 不该因为一个读不到就阻断正常记账。
		return nil
	}
	if taxType != TaxTypeSmall {
		return nil
	}
	for _, e := range v.Entries {
		if !inputVATColumns[e.AccountCode] {
			continue
		}
		return fmt.Errorf(
			"%w: 本账套是小规模纳税人，不得抵扣进项税额，不能使用「%s」。"+
				"采购时应将价税合计全额计入成本费用"+
				"（借 相关费用科目 全额 / 贷 银行存款或应付账款），不要拆分税额。",
			account.ErrNotAllowedForTaxType, e.AccountCode)
	}
	return nil
}

// insertDraftEntries 写草稿的分录（只写 voucher_entry，不写总账）。
func insertDraftEntries(ctx context.Context, tx *Tx, v *voucher.Voucher,
	accounts map[string]int64) ([]int64, error) {

	ids := make([]int64, 0, len(v.Entries))
	for i, e := range v.Entries {
		accID, ok := accounts[e.AccountCode]
		if !ok {
			return nil, fmt.Errorf("%w: 科目 %q", ErrNotFound, e.AccountCode)
		}
		res, err := tx.Exec(ctx, `
			INSERT INTO voucher_entry (voucher_id, line_no, account_id, summary,
				debit, credit, contact_id, employee_id, dept_id, project_id)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			v.ID, i+1, accID, e.Summary, int64(e.Debit), int64(e.Credit),
			nullInt64(e.Aux.ContactID), nullInt64(e.Aux.EmployeeID),
			nullInt64(e.Aux.DeptID), nullInt64(e.Aux.ProjectID))
		if err != nil {
			return nil, translateErr(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ListFilter 是凭证列表的筛选条件。
type ListFilter struct {
	// Year / Month 为 0 表示不限期间。
	Year, Month int
	// Status 为空表示不限；否则 draft | posted | voided
	Status string
	// Keyword 按摘要、备注或凭证号模糊匹配。
	Keyword string
	// Limit 为 0 时取 200。
	Limit int
}

// List 按条件查询凭证（不含分录），按日期倒序。
func (r *VoucherRepo) List(ctx context.Context, f ListFilter) ([]*voucher.Voucher, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	sqlText := `SELECT ` + voucherColumns + ` FROM voucher WHERE 1 = 1`
	var args []any
	if f.Year > 0 {
		sqlText += ` AND year = ?`
		args = append(args, f.Year)
	}
	if f.Month > 0 {
		sqlText += ` AND month = ?`
		args = append(args, f.Month)
	}
	if f.Status != "" {
		sqlText += ` AND status = ?`
		args = append(args, f.Status)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		sqlText += ` AND (remark LIKE ? OR no LIKE ?)`
		like := "%" + kw + "%"
		args = append(args, like, like)
	}
	sqlText += ` ORDER BY biz_date DESC, year DESC, month DESC, seq DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.sql.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	var out []*voucher.Voucher
	for rows.Next() {
		v, err := scanVoucher(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
