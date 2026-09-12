package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/closing"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
)

// ClosingRepo 负责期末结转损益、结账与反结账。
//
// # 为什么结账必须是一个事务
//
// 一次结账要做三件事：写结转凭证、过账（连带写总账）、把期间置为 closed。
// 三者必须同生共死：
//
//   - 只写了凭证没过账 → 期间已关，凭证却还是草稿，永远无法过账
//   - 过了账没关期间     → 用户还能继续往这个月录凭证，结转数据失效
//
// Frappe Books 把这几步分散在 UI 回调里逐条执行，任一步失败都会留下
// 无法收拾的中间状态 —— 这是本项目明确要避免的。
type ClosingRepo struct{ db *DB }

// Closing 返回期末结转仓储。
func (db *DB) Closing() *ClosingRepo { return &ClosingRepo{db: db} }

// 结账相关错误。
var (
	ErrAlreadyClosed = errors.New("closing: 该期间已结账")
	ErrNotClosed     = errors.New("closing: 该期间尚未结账")
	ErrPriorOpen     = errors.New("closing: 存在未结账的在前期期间，必须按顺序结账")
	ErrLaterClosed   = errors.New("closing: 存在已结账的在后期间，必须先反结账")
	ErrHealthFailed  = errors.New("closing: 结账前体检未通过")
	ErrClosedVoucher = errors.New("closing: 结转凭证状态异常，无法冲销")
)

// ---------------------------------------------------------------------------
// 读取
// ---------------------------------------------------------------------------

// CloseInput 是一次结账的输入。
type CloseInput struct {
	Period period.Key
	// Accounts 允许覆盖默认的结转科目配置（本年利润 / 未分配利润）。
	// 为零值时使用 closing.DefaultAccounts()。
	Accounts closing.Accounts
	// PostingBy 是结账人（《会计法》要求记账凭证有记账签章）。
	PostingBy string
	// Remark 追加到结转凭证的备注。
	Remark string
	// SkipHealthCheck 为真时跳过结账前体检（仅限迁移/测试等特殊场景）。
	SkipHealthCheck bool
	// At 是结账时刻，便于测试注入固定时间。
	At time.Time
}

// CloseResult 是一次结账的结果。
type CloseResult struct {
	Period period.Key    `json:"period"`
	Plan   *closing.Plan `json:"plan"`

	// VoucherID / VoucherNo 是生成的结转凭证；无损益可转时为 0 / ""。
	VoucherID int64  `json:"voucherId"`
	VoucherNo string `json:"voucherNo"`

	// Health 是结账前体检报告，便于界面回显「检查了哪些项」。
	Health *PeriodHealth `json:"health"`
}

// Planned 报告本次结账是否真的写了凭证。
func (r *CloseResult) Planned() bool { return r.VoucherID != 0 }

// Plan 计算某期间的结账计划，**不写任何数据**。
//
// 界面上的「结账预览」直接调用它：让用户在点确认前就看清
// 本期收入多少、费用多少、是盈是亏。
func (r *ClosingRepo) Plan(ctx context.Context, k period.Key,
	ac closing.Accounts) (*closing.Plan, error) {

	if ac == (closing.Accounts{}) {
		ac = closing.DefaultAccounts()
	}
	if !k.Valid() {
		return nil, fmt.Errorf("%w: %v", closing.ErrBadPeriod, k)
	}

	var pnl []closing.PnLAccount
	var profit money.Money

	err := r.db.Read(ctx, func(q Querier) error {
		var e error
		pnl, e = pnlBalances(ctx, q, k)
		if e != nil {
			return e
		}
		// 「本年利润」的余额只在年末结转时用得上，但读它几乎不花代价，
		// 一并读出来让 Plan 的语义保持纯粹。
		profit, e = accountRawBalance(ctx, q, ac.Profit, closing.Date(k))
		return e
	})
	if err != nil {
		return nil, err
	}
	return closing.BuildPlan(k, pnl, profit, ac)
}

// Journal 返回某期间**当前生效**的结转凭证（按凭证号排序）。
//
// 「当前生效」= 已过账、未作废、且本身不是红字冲销凭证。
// 反结账后这里会变空：原结转凭证已作废，而它对应的红字凭证
// 是「撤销动作」而不是一次结账，不该混进结转台账。
func (r *ClosingRepo) Journal(ctx context.Context, k period.Key) ([]*voucher.Voucher, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT v.id FROM voucher v
		 WHERE v.source = ? AND v.year = ? AND v.month = ?
		   AND v.status <> 'voided'
		   AND v.reverses_id IS NULL
		 ORDER BY v.seq, v.id`,
		string(voucher.SourceClosing), k.Year, k.Month)
	if err != nil {
		return nil, translateErr(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// ★ 必须先关掉 rows 再逐张加载凭证。
	// 本项目用的是单连接连接池，在 rows 打开时再发查询会永久阻塞。
	rows.Close()

	out := make([]*voucher.Voucher, 0, len(ids))
	for _, id := range ids {
		v, err := r.db.Vouchers().Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 结账
// ---------------------------------------------------------------------------

// ClosePeriod 执行一次结账：结转损益（年末再加结转本年利润）→ 关闭期间。
func (r *ClosingRepo) ClosePeriod(ctx context.Context, in CloseInput) (*CloseResult, error) {
	ac := in.Accounts
	if ac == (closing.Accounts{}) {
		ac = closing.DefaultAccounts()
	}
	k := in.Period
	if !k.Valid() {
		return nil, fmt.Errorf("%w: %v", closing.ErrBadPeriod, k)
	}
	if in.PostingBy == "" {
		return nil, voucher.ErrMissingMaker
	}
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}

	// ★ 结账前体检必须在事务之外执行。
	//
	// 体检内部要跑多条只读查询（试算平衡、资产负债表、往来账龄…），
	// 而本库的连接池只有一条连接：在事务里再发查询会直接死锁。
	// 事务内会重新校验期间状态，因此这里提前体检不会造成误判。
	var health *PeriodHealth
	if !in.SkipHealthCheck {
		h, err := r.db.CheckPeriodHealth(ctx, k)
		if err != nil {
			return nil, err
		}
		health = h
		if !h.CanClose() {
			return nil, fmt.Errorf("%w: %s", ErrHealthFailed, h.Summary())
		}
	}

	var out CloseResult
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		out = CloseResult{Period: k, Health: health}

		// 1. 期间状态：必须存在、必须 open
		st, err := periodStatusTx(ctx, tx, k)
		if err != nil {
			return err
		}
		if st == period.StatusClosed {
			return fmt.Errorf("%w: %s", ErrAlreadyClosed, k)
		}
		if st != period.StatusOpen {
			return fmt.Errorf("%w: 期间 %s 状态为 %s", period.ErrNotOpen, k, st)
		}

		// 2. 顺序结账：不允许跳过前期
		prior, err := firstOpenPriorTx(ctx, tx, k)
		if err != nil {
			return err
		}
		if prior != nil {
			return fmt.Errorf("%w: %s 仍未结账", ErrPriorOpen, *prior)
		}

		// 3. 计算结转计划
		pnl, err := pnlBalances(ctx, tx, k)
		if err != nil {
			return err
		}
		profit, err := accountRawBalance(ctx, tx, ac.Profit, closing.Date(k))
		if err != nil {
			return err
		}
		plan, err := closing.BuildPlan(k, pnl, profit, ac)
		if err != nil {
			return err
		}
		out.Plan = plan

		// 4. 写结转凭证（若有）
		if plan.HasEntries() {
			if err := plan.Validate(); err != nil {
				return err
			}
			vc, err := r.buildClosingVoucher(plan, in.PostingBy, in.Remark)
			if err != nil {
				return err
			}
			ids, err := accountIDMap(ctx, tx)
			if err != nil {
				return err
			}
			res, err := r.db.Vouchers().PostInTx(ctx, tx, PostInput{
				Voucher: vc, Accounts: ids, PostingBy: in.PostingBy, At: at,
			})
			if err != nil {
				return err
			}
			out.VoucherID, out.VoucherNo = res.VoucherID, res.No
		}

		// 5. 关闭期间
		if err := r.db.Periods().SetStatus(ctx, tx, k, period.StatusClosed, in.PostingBy, &at); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// buildClosingVoucher 把结账计划变成一张凭证。
func (r *ClosingRepo) buildClosingVoucher(plan *closing.Plan,
	postingBy, remark string) (*voucher.Voucher, error) {

	vc, err := voucher.New(voucher.WordZhuan, closing.Date(plan.Period), postingBy)
	if err != nil {
		return nil, err
	}
	vc.Source = voucher.SourceClosing
	vc.Remark = plan.Summary()
	if remark != "" {
		vc.Remark += "；" + remark
	}

	for _, e := range plan.ClosingEntries {
		if err := vc.AddEntry(ledger.Entry{
			AccountCode: e.AccountCode,
			Summary:     e.Summary,
			Debit:       e.Debit,
			Credit:      e.Credit,
			Aux:         e.Aux,
		}); err != nil {
			return nil, err
		}
	}
	return vc, nil
}

// ---------------------------------------------------------------------------
// 反结账
// ---------------------------------------------------------------------------

// ReopenInput 是一次反结账的输入。
type ReopenInput struct {
	Period period.Key
	// PostingBy 是操作人。
	PostingBy string
	// At 是操作时刻。
	At time.Time
}

// ReopenResult 是一次反结账的结果。
type ReopenResult struct {
	Period period.Key `json:"period"`
	// Reversed 是本次红字冲销掉的结转凭证号。
	Reversed []string `json:"reversed"`
	// VoucherIDs 是冲销凭证的主键，便于界面跳转。
	VoucherIDs []int64 `json:"voucherIds"`
}

// ReopenPeriod 反结账：把期间置回 open，并红字冲销该期的结转凭证。
//
// 两步在同一事务内完成（内部先放开期间再写红字凭证，因为过账要求
// 期间为 open），因此不存在「期间开了但没冲销」的中间状态。
//
// # 为什么必须冲销而不是删凭证
//
// 会计凭证一经记账不得删除，只能红字冲销（《会计基础工作规范》）。
// 直接删除结转凭证会有两个后果：凭证号断号无法解释、
// 已上报的报表与账面数据失去可追溯的对应关系。
//
// 因此这里生成一张**真正的红字凭证**（借贷互换、金额仍为非负），
// 而不是把原分录标记为已冲销 —— 后者会让总账一边被过滤一边被统计，
// 借贷瞬间不平。
func (r *ClosingRepo) ReopenPeriod(ctx context.Context, in ReopenInput) (*ReopenResult, error) {
	k := in.Period
	if !k.Valid() {
		return nil, fmt.Errorf("%w: %v", closing.ErrBadPeriod, k)
	}
	if in.PostingBy == "" {
		return nil, voucher.ErrMissingMaker
	}
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}

	var out ReopenResult
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		out = ReopenResult{Period: k}

		st, err := periodStatusTx(ctx, tx, k)
		if err != nil {
			return err
		}
		if st != period.StatusClosed {
			return fmt.Errorf("%w: %s", ErrNotClosed, k)
		}

		// 顺序反结账：后面的期间还关着，就不能放开前面的
		later, err := firstClosedLaterTx(ctx, tx, k)
		if err != nil {
			return err
		}
		if later != nil {
			return fmt.Errorf("%w: %s 仍是已结账状态", ErrLaterClosed, *later)
		}

		// ★ 先把期间放开，再写红字冲销凭证。
		//
		// 顺序不能反：冲销凭证的记账日期落在被反结账的期间里，
		// 而 PostInTx 会强制校验「期间必须是 open」——
		// 期间还关着的话这张凭证根本过不了账。
		//
		// 两步在同一事务内，所以先放开并不构成风险：
		// 冲销只要失败，整个事务回滚，期间依旧保持已结账。
		if err := r.db.Periods().SetStatus(ctx, tx, k, period.StatusOpen, "", nil); err != nil {
			return err
		}

		// 冲销该期所有结转凭证
		closings, err := closingsTx(ctx, tx, k)
		if err != nil {
			return err
		}
		ids, err := accountIDMap(ctx, tx)
		if err != nil {
			return err
		}
		for _, v := range closings {
			if v.Status != voucher.StatusPosted {
				return fmt.Errorf("%w: %s 状态为 %s", ErrClosedVoucher, v.No, v.Status)
			}
			rev, err := v.BuildReversal(in.PostingBy, closing.Date(k))
			if err != nil {
				return err
			}
			res, err := r.db.Vouchers().PostInTx(ctx, tx, PostInput{
				Voucher: rev, Accounts: ids, PostingBy: in.PostingBy, At: at,
			})
			if err != nil {
				return err
			}
			out.Reversed = append(out.Reversed, v.No)
			out.VoucherIDs = append(out.VoucherIDs, res.VoucherID)

			// 原凭证标为已冲销。★ 注意：这一步只是**审计标记**，
			// 总账查询不会据此过滤任何分录 —— 冲销的效果完全由
			// 上面那张红字凭证承载。
			if err := markVoided(ctx, tx, v.ID, res.VoucherID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------------------------------------------------------------------------
// 内部查询
// ---------------------------------------------------------------------------

// pnlBalances 返回截至某期间末各**损益类明细科目**的原始净余额（借−贷），
// **按科目 + 辅助核算组合分组**。
//
// # 为什么必须按辅助核算分组
//
// 结转分录要继承原分录的辅助核算。若把「管理费用—办公费」下
// 甲部门 1,000、乙部门 2,000 合并成一条 3,000 的结转分录，会有两个后果：
//
//  1. 该科目若要求「部门」辅助核算，合并后的分录缺维度，过账直接被拒
//  2. 即使能过账，部门费用表在结转当月也会凭空出现一笔无部门的费用
//
// 因此这里按 (科目, 部门, 项目, 往来, 员工) 分组，一组生成一条结转分录。
// 分组后每组都带着原始维度，过账校验自然满足。
//
// 只取叶子科目：汇总科目的余额由其下级聚合而来，若一并结转会重复计算。
// 余额取「年初至期末」的累计数而不是「本期发生额」，是为了兼容
// 跳过中间期间直接结账的情形 —— 那时未结转的余额理应一并转走。
func pnlBalances(ctx context.Context, q Querier, k period.Key) ([]closing.PnLAccount, error) {
	rows, err := q.Query(ctx, `
		SELECT a.code, a.name, a.root_type,
		       le.contact_id, le.employee_id, le.dept_id, le.project_id,
		       SUM(le.debit) - SUM(le.credit) AS raw
		  FROM account a
		  JOIN ledger_entry le
		         ON le.account_id = a.id AND le.biz_date <= ?
		 WHERE a.root_type IN (?, ?) AND a.is_leaf = 1
		 GROUP BY a.id, a.code, a.name, a.root_type,
		          le.contact_id, le.employee_id, le.dept_id, le.project_id
		 HAVING raw <> 0
		 ORDER BY a.code`,
		closing.Date(k).String(),
		string(account.RootIncome), string(account.RootExpense))
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []closing.PnLAccount
	for rows.Next() {
		var a closing.PnLAccount
		var rt string
		var raw, contact, employee, dept, project sql.NullInt64
		if err := rows.Scan(&a.Code, &a.Name, &rt,
			&contact, &employee, &dept, &project, &raw); err != nil {
			return nil, err
		}
		a.RootType = account.RootType(rt)
		a.Raw = money.Money(raw.Int64)
		a.Aux = ledger.Aux{
			ContactID:  toNullInt64(contact),
			EmployeeID: toNullInt64(employee),
			DeptID:     toNullInt64(dept),
			ProjectID:  toNullInt64(project),
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// accountRawBalance 返回单个科目前缀截至某日的原始净余额（借−贷）。
func accountRawBalance(ctx context.Context, q Querier, code string,
	asOf calendar.Date) (money.Money, error) {

	var net sql.NullInt64
	err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(le.debit), 0) - COALESCE(SUM(le.credit), 0)
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		 WHERE a.code LIKE ? AND le.biz_date <= ?`,
		code+"%", asOf.String()).Scan(&net)
	if err != nil {
		return 0, translateErr(err)
	}
	return money.Money(net.Int64), nil
}

func periodStatusTx(ctx context.Context, tx *Tx, k period.Key) (period.Status, error) {
	var st string
	err := tx.QueryRow(ctx,
		`SELECT status FROM period WHERE year = ? AND month = ?`, k.Year, k.Month).Scan(&st)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("%w: %s", period.ErrPeriodNotFound, k)
	}
	if err != nil {
		return "", translateErr(err)
	}
	return period.Status(st), nil
}

// firstOpenPriorTx 返回最早的一个「在本期间之前、尚未结账」的期间。
//
// 仅比较年月序即可：会计期间是连续的，且建账时一次性生成到当前月。
func firstOpenPriorTx(ctx context.Context, tx *Tx, k period.Key) (*period.Key, error) {
	var y, m int
	err := tx.QueryRow(ctx, `
		SELECT year, month FROM period
		 WHERE (year < ? OR (year = ? AND month < ?)) AND status <> ?
		 ORDER BY year, month LIMIT 1`,
		k.Year, k.Year, k.Month, string(period.StatusClosed)).Scan(&y, &m)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, translateErr(err)
	}
	p := period.NewKey(y, m)
	return &p, nil
}

// firstClosedLaterTx 返回最晚的一个「在本期间之后、仍然已结账」的期间。
func firstClosedLaterTx(ctx context.Context, tx *Tx, k period.Key) (*period.Key, error) {
	var y, m int
	err := tx.QueryRow(ctx, `
		SELECT year, month FROM period
		 WHERE (year > ? OR (year = ? AND month > ?)) AND status = ?
		 ORDER BY year DESC, month DESC LIMIT 1`,
		k.Year, k.Year, k.Month, string(period.StatusClosed)).Scan(&y, &m)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, translateErr(err)
	}
	p := period.NewKey(y, m)
	return &p, nil
}

// closingsTx 返回某期间未作废的结转凭证（含分录），供冲销使用。
func closingsTx(ctx context.Context, tx *Tx, k period.Key) ([]*voucher.Voucher, error) {
	rows, err := tx.Query(ctx, `
		SELECT id FROM voucher
		 WHERE source = ? AND year = ? AND month = ?
		   AND status <> 'voided'
		   AND reverses_id IS NULL
		 ORDER BY seq, id`,
		string(voucher.SourceClosing), k.Year, k.Month)
	if err != nil {
		return nil, translateErr(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close() // ★ 单连接池：必须先把 rows 关掉再发下一次查询

	out := make([]*voucher.Voucher, 0, len(ids))
	for _, id := range ids {
		v, err := loadVoucherTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// markVoided 把原凭证标记为已作废，并把 voided_by 指向它的红字凭证。
//
// ★ 这只是**审计标记**：总账查询不会据此过滤任何分录，
// 冲销的账务效果完全由那张红字凭证承载。
// 若在这里过滤掉原分录，同一笔业务就会被「一边过滤、一边统计」，
// 借贷立刻不平 —— 这正是必须避开的老路。
func markVoided(ctx context.Context, tx *Tx, id, revID int64) error {
	res, err := tx.Exec(ctx, `
		UPDATE voucher SET status = 'voided', voided_by = ?
		 WHERE id = ? AND status = 'posted'`,
		revID, id)
	if err != nil {
		return translateErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: 凭证 %d", ErrClosedVoucher, id)
	}
	return nil
}

// accountIDMap 返回「科目编码 → 主键」，用于过账时解析分录科目。
func accountIDMap(ctx context.Context, q Querier) (map[string]int64, error) {
	rows, err := q.Query(ctx, `SELECT code, id FROM account`)
	if err != nil {
		return nil, translateErr(err)
	}
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

// loadVoucherTx 在事务内按主键加载凭证及其分录。
//
// ★ 不能用 db.Vouchers().Get：它走的是 db.sql，而当前事务已经
// 占用了唯一的那条连接，会直接死锁。
func loadVoucherTx(ctx context.Context, tx *Tx, id int64) (*voucher.Voucher, error) {
	rows, err := tx.Query(ctx, `SELECT `+voucherColumns+` FROM voucher WHERE id = ?`, id)
	if err != nil {
		return nil, translateErr(err)
	}
	if !rows.Next() {
		rows.Close()
		return nil, fmt.Errorf("%w: 凭证 id=%d", ErrNotFound, id)
	}
	v, err := scanVoucher(rows)
	if err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close() // ★ 先关 rows 再加载分录，避免单连接池自锁

	entries, err := loadEntries(ctx, tx.q, id)
	if err != nil {
		return nil, err
	}
	v.Entries = entries
	return v, nil
}
