package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/evidence"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
	"miniaccount/internal/domain/workpaper"
)

// WorkpaperRepo 是审计底稿的持久化访问。
type WorkpaperRepo struct{ db *DB }

// Workpapers 返回审计底稿仓储。
func (db *DB) Workpapers() *WorkpaperRepo { return &WorkpaperRepo{db: db} }

// ---------------------------------------------------------------------------
// 重要性水平
// ---------------------------------------------------------------------------

// SaveMateriality 保存某期的重要性水平。
func (r *WorkpaperRepo) SaveMateriality(ctx context.Context, m workpaper.Materiality) error {
	if err := m.Validate(); err != nil {
		return err
	}
	return r.db.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO audit_materiality (year, month, benchmark, benchmark_amount,
				rate_ppm, performance_ppm, trivial_ppm, note, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)
			ON CONFLICT(year, month) DO UPDATE SET
				benchmark = excluded.benchmark,
				benchmark_amount = excluded.benchmark_amount,
				rate_ppm = excluded.rate_ppm,
				performance_ppm = excluded.performance_ppm,
				trivial_ppm = excluded.trivial_ppm,
				note = excluded.note,
				updated_at = excluded.updated_at`,
			m.Period.Year, m.Period.Month, string(m.Benchmark), int64(m.BenchmarkAmount),
			m.RatePPM, m.PerformancePPM, m.TrivialPPM, m.Note, nowString())
		return translateErr(err)
	})
}

// Materiality 读某期的重要性水平；没有配置时返回 (nil, nil)。
func (r *WorkpaperRepo) Materiality(ctx context.Context, k period.Key) (*workpaper.Materiality, error) {
	var m workpaper.Materiality
	var benchmark string
	var amount int64
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT benchmark, benchmark_amount, rate_ppm, performance_ppm, trivial_ppm, note
		  FROM audit_materiality WHERE year = ? AND month = ?`, k.Year, k.Month).
		Scan(&benchmark, &amount, &m.RatePPM, &m.PerformancePPM, &m.TrivialPPM, &m.Note)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, translateErr(err)
	}
	m.Period = k
	m.Benchmark = workpaper.Benchmark(benchmark)
	m.BenchmarkAmount = money.Money(amount)
	return &m, nil
}

// DeleteMateriality 删掉某期的重要性水平。
//
// ★ 挂在它下面的证据要一起清。
//
// 不清的话：证据行还在（只是链上看不到），等用户重新确定重要性水平，
// **旧的依据会静默复活**挂到新结论上 —— 而那份依据是给上一个门槛用的。
func (r *WorkpaperRepo) DeleteMateriality(ctx context.Context, k period.Key) error {
	return r.db.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(ctx,
			`DELETE FROM audit_evidence
			  WHERE owner_type = 'materiality' AND owner_id = ?`,
			evidence.PeriodOwnerID(k.Year, k.Month)); err != nil {
			return translateErr(err)
		}
		_, err := tx.Exec(ctx,
			`DELETE FROM audit_materiality WHERE year = ? AND month = ?`, k.Year, k.Month)
		return translateErr(err)
	})
}

// ---------------------------------------------------------------------------
// 基准金额取数
// ---------------------------------------------------------------------------

// BenchmarkAmounts 是四个基准的自动取数结果。
type BenchmarkAmounts struct {
	Assets  money.Money // 资产总额
	Revenue money.Money // 营业收入
	Profit  money.Money // 利润总额
	Expense money.Money // 费用总额
	// Note 说明取数口径（底稿上要写清楚这个数是怎么来的）。
	Note []string
}

// Benchmarks 从账套取四个基准金额。
//
// ★ 数字全部来自**已有的报表引擎**，不另算一套。
//
// 资产与费用从科目余额表按科目大类汇总；利润总额直接读利润表的
// 「行30 利润总额」—— 那一行有官方勾稽公式，自己再推一遍
// 迟早会与利润表对不上，而底稿上的数与企业报表对不上是致命的。
func (r *WorkpaperRepo) Benchmarks(ctx context.Context, k period.Key) (*BenchmarkAmounts, error) {
	out := &BenchmarkAmounts{}

	tree, err := r.db.Accounts().Tree(ctx)
	if err != nil {
		return nil, err
	}
	rootOf := map[string]string{}
	for _, a := range tree.All() {
		rootOf[a.Code] = string(a.RootType)
	}

	rep, err := r.db.Reports().TrialBalanceReport(ctx, k)
	if err != nil {
		return nil, err
	}
	for _, row := range rep.Rows {
		if !row.IsLeaf {
			continue
		}
		switch rootOf[row.AccountCode] {
		case "asset":
			// 期末借 − 贷：累计折旧这类备抵科目本身就是贷方余额，
			// 相减之后自动起抵减作用。余额本身就是累计口径。
			out.Assets = out.Assets.Add(row.ClosingDebit).Sub(row.ClosingCredit)
		}
	}
	out.Note = append(out.Note,
		fmt.Sprintf("资产总额 = 资产类明细科目**期末**借方净额合计 = %s（余额是时点数）", out.Assets))
	// ★ 费用总额也取**年初至本期**：与下面的营业收入、利润总额保持同一口径。
	//
	// 原来取「本期发生额」，于是同一张底稿上三个基准混着两个口径：
	// 资产是期末余额、营业收入是当月数、费用是当月数。选定 12 月做年度审计时，
	// 营业收入只有 12 月一个月，门槛会算小一个数量级。
	start, err := calendar.New(k.Year, 1, 1)
	if err != nil {
		return nil, err
	}
	end, err := calendar.New(k.Year, k.Month, calendar.DaysInMonth(k.Year, k.Month))
	if err != nil {
		return nil, err
	}
	var expense int64
	err = r.db.sql.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(le.debit) - SUM(le.credit), 0)
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		 WHERE a.root_type = 'expense' AND a.is_leaf = 1
		   AND le.biz_date >= ? AND le.biz_date <= ?`,
		start.String(), end.String()).Scan(&expense)
	if err != nil {
		return nil, translateErr(err)
	}
	if expense < 0 {
		expense = 0
	}
	out.Expense = money.Money(expense)
	out.Note = append(out.Note, fmt.Sprintf(
		"费用总额 = 费用类明细科目**年初至本期**借方净额合计 = %s", out.Expense))

	// 营业收入与利润总额走利润表的**本年累计**栏 —— 它有官方取数公式。
	//
	// ★ 用累计而不是本期：年度审计（期间选 12 月）时按本期取只有 12 月
	// 一个月的收入，门槛会算小一个数量级（全年 1,200 万 → 1% 应为 12 万，
	// 取当月 100 万会算成 1 万）。审计的基准本来就是「被审计期间的规模」，
	// 而期间是整年。
	_, ytd, _, err := r.db.Reports().BuildIncomeStatement(ctx, k)
	if err != nil {
		return nil, err
	}
	if l, ok := ytd.Line(1); ok {
		out.Revenue = l.Value
		out.Note = append(out.Note,
			fmt.Sprintf("营业收入 = 利润表「行1 营业收入」**年初至本期**累计 = %s", out.Revenue))
	} else {
		out.Note = append(out.Note, "营业收入：利润表里没有「行1 营业收入」，取数为 0")
	}
	if l, ok := ytd.Line(30); ok {
		out.Profit = l.Value
		out.Note = append(out.Note,
			fmt.Sprintf("利润总额 = 利润表「行30 利润总额」**年初至本期**累计 = %s", out.Profit))
	} else {
		out.Note = append(out.Note, "利润总额：利润表里没有「行30 利润总额」，取数为 0")
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 审计调整
// ---------------------------------------------------------------------------

// SaveAdjustment 新增或修改一笔审计调整。
func (r *WorkpaperRepo) SaveAdjustment(ctx context.Context, a workpaper.Adjustment) (int64, error) {
	if err := a.Validate(); err != nil {
		return 0, err
	}
	var id int64
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		now := nowString()
		// ★ 编号在**同一个事务里**取，而且取的是「已有编号的最大序号 + 1」。
		//
		// 原来用 len(列表)+1，于是删掉中间一笔（001/002/003 → 删 002）
		// 之后再新增，又得到 003，与已有的一笔重号 ——
		// 而底稿之间是靠编号互相引用的，重号等于引用失效。
		// 放在事务里取，也顺手挡掉了并发下的撞号。
		if a.ID == 0 && a.Code == "" {
			code, err := nextAdjustmentCode(ctx, tx, a.Period)
			if err != nil {
				return err
			}
			a.Code = code
		}
		if a.ID == 0 {
			res, err := tx.Exec(ctx, `
				INSERT INTO audit_adjustment (year, month, code, kind, summary, reason,
					evidence, voucher_id, created_by, reviewed_by, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
				a.Period.Year, a.Period.Month, a.Code, string(a.Kind), a.Summary,
				a.Reason, a.Evidence, nullInt64(a.VoucherID), a.CreatedBy, a.ReviewedBy, now, now)
			if err != nil {
				return translateErr(err)
			}
			id, err = res.LastInsertId()
			if err != nil {
				return translateErr(err)
			}
		} else {
			res, err := tx.Exec(ctx, `
				UPDATE audit_adjustment SET year = ?, month = ?, code = ?, kind = ?,
					summary = ?, reason = ?, evidence = ?, updated_at = ?
				 WHERE id = ?`,
				a.Period.Year, a.Period.Month, a.Code, string(a.Kind), a.Summary,
				a.Reason, a.Evidence, now, a.ID)
			if err != nil {
				return translateErr(err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("%w: 审计调整 id=%d", ErrNotFound, a.ID)
			}
			id = a.ID
			// 分录整组替换：调整的「改一行」在界面上就是改一行，
			// 但存下来最简单也最不容易出错的表达是整组替换
			if _, err := tx.Exec(ctx,
				`DELETE FROM audit_adjustment_line WHERE adjustment_id = ?`, id); err != nil {
				return translateErr(err)
			}
		}
		for i, l := range a.Lines {
			if _, err := tx.Exec(ctx, `
				INSERT INTO audit_adjustment_line (adjustment_id, line_no, account_code,
					summary, debit, credit, contact_id, employee_id, dept_id, project_id)
				VALUES (?,?,?,?,?,?,?,?,?,?)`,
				id, i+1, l.AccountCode, l.Summary, int64(l.Debit), int64(l.Credit),
				nullInt64(l.ContactID), nullInt64(l.EmployeeID),
				nullInt64(l.DeptID), nullInt64(l.ProjectID)); err != nil {
				return translateErr(err)
			}
		}
		return nil
	})
	return id, err
}

// Adjustments 返回某期的全部审计调整（含分录）。
func (r *WorkpaperRepo) Adjustments(ctx context.Context, k period.Key) ([]workpaper.Adjustment, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT id, code, kind, summary, reason, evidence, voucher_id,
		       created_by, reviewed_by
		  FROM audit_adjustment WHERE year = ? AND month = ? ORDER BY id`,
		k.Year, k.Month)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []workpaper.Adjustment
	var ids []int64
	for rows.Next() {
		a, err := scanAdjustment(rows.Scan, k)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
		ids = append(ids, a.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, translateErr(err)
	}
	// 分录一次性取回来再按 id 分发：逐笔查一次是 N+1 条查询，
	// 而底稿动辄几十笔，在只有一条连接的连接池上会把界面拖到卡住
	if len(ids) > 0 {
		lines, err := r.linesOf(ctx, ids)
		if err != nil {
			return nil, err
		}
		for i := range out {
			out[i].Lines = lines[out[i].ID]
		}
	}
	// ★ 「已生成凭证」看 voucher_id，「已入账」看那张凭证的状态。
	// 生成凭证只落草稿，到账期结算才过账 ——
	// 所以有 voucher_id 只说明「凭证生成了」，入没入账得看那张凭证。
	if err := r.resolvePosted(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// resolvePosted 用凭证的真实状态填 Adjustment.Booked / Posted。
//
// 绝大多数情况下两者是白算的：voucher_id 有值就是「已生成」，
// 再查一次状态就知道「入没入账」。留着这一步是为了兜住
// voucher_id 指向一张不存在的凭证（理论上 ON DELETE SET NULL
// 会替我们清掉，但账套可能是从别的版本升上来的）——
// 那时必须当作「没生成过」，否则那笔调整会卡在
// 「显示已生成凭证、点开却没有」的状态里，审定数还少加了它。
func (r *WorkpaperRepo) resolvePosted(ctx context.Context, list []workpaper.Adjustment) error {
	ids := make([]int64, 0, len(list))
	for _, a := range list {
		if a.VoucherID != nil {
			ids = append(ids, *a.VoucherID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	status, err := r.voucherStatusOf(ctx, ids)
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].VoucherID == nil {
			continue
		}
		st, ok := status[*list[i].VoucherID]
		if !ok {
			// 悬空：voucher_id 指向一张已经不存在的凭证。
			//
			// ★ 只**如实报告**，不在这里改库。
			//
			// 原来顺手把它清成 NULL —— 那是「读的时候写库」：
			// AI 的 get_workpaper 只是读底稿，却会改到账套库，
			// 而且绕过 WithTx。正常路径上这张凭证是被外键
			// ON DELETE SET NULL 清掉的（见 0012），这里只需要
			// 不让那笔调整卡在「显示已生成凭证、点开却没有」的状态。
			list[i].Booked = false
			list[i].Posted = false
			list[i].VoucherID = nil
			continue
		}
		list[i].Booked = true
		list[i].Posted = st == string(voucher.StatusPosted)
	}
	return nil
}

// voucherStatusOf 批量取凭证状态。
func (r *WorkpaperRepo) voucherStatusOf(ctx context.Context, ids []int64) (map[int64]string, error) {
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := r.db.sql.QueryContext(ctx,
		`SELECT id, status FROM voucher WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, translateErr(err)
		}
		out[id] = st
	}
	return out, translateErr(rows.Err())
}

// Adjustment 按 id 读一笔。
func (r *WorkpaperRepo) Adjustment(ctx context.Context, id int64) (*workpaper.Adjustment, error) {
	row := r.db.sql.QueryRowContext(ctx, `
		SELECT id, year, month, code, kind, summary, reason, evidence,
		       voucher_id, created_by, reviewed_by
		  FROM audit_adjustment WHERE id = ?`, id)
	var (
		a    workpaper.Adjustment
		kind string
		vid  sql.NullInt64
		y, m int
	)
	if err := row.Scan(&a.ID, &y, &m, &a.Code, &kind, &a.Summary, &a.Reason,
		&a.Evidence, &vid, &a.CreatedBy, &a.ReviewedBy); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: 审计调整 id=%d", ErrNotFound, id)
		}
		return nil, translateErr(err)
	}
	a.Period = period.NewKey(y, m)
	a.Kind = workpaper.AdjustKind(kind)
	if vid.Valid {
		v := vid.Int64
		a.VoucherID = &v
	}
	lines, err := r.linesOf(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	a.Lines = lines[id]
	list := []workpaper.Adjustment{a}
	if err := r.resolvePosted(ctx, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

// DeleteAdjustment 删除一笔调整。
func (r *WorkpaperRepo) DeleteAdjustment(ctx context.Context, id int64) error {
	return r.db.WithTx(ctx, func(tx *Tx) error {
		// 已经生成凭证的不能删：凭证（哪怕是草稿）是账的一部分，
		// 删了调整，那张凭证就没了依据
		var vid sql.NullInt64
		if err := tx.QueryRow(ctx,
			`SELECT voucher_id FROM audit_adjustment WHERE id = ?`, id).Scan(&vid); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("%w: 审计调整 id=%d", ErrNotFound, id)
			}
			return translateErr(err)
		}
		// 凭证被删掉了就不算「有凭证」—— 那时调整可以正常删除
		if vid.Valid {
			var one int
			err := tx.QueryRow(ctx, `SELECT 1 FROM voucher WHERE id = ?`, vid.Int64).Scan(&one)
			if err == nil {
				return fmt.Errorf("这笔调整已经生成凭证了，不能删除。\n" +
					"凭证是账的一部分，删掉调整它就没有依据了。\n" +
					"要撤销请先删掉那张凭证（凭证页），或改为「重分类」保留记录")
			}
			if err != sql.ErrNoRows {
				return translateErr(err)
			}
		}
		// 挂在它下面的证据也要一起走。
		//
		// ★ audit_evidence 的 owner_id 是多态的（可能是调整、也可能是一期
		// 的重要性水平），所以加不了外键 —— 只能在这里显式清。
		// 不清的后果不是「多几行垃圾」：底稿里那条「依据：折旧计算表」
		// 会留在一个已经不存在的结论下，而审计轨迹本来就该是可追溯的。
		if _, err := tx.Exec(ctx,
			`DELETE FROM audit_evidence WHERE owner_type = 'adjustment' AND owner_id = ?`,
			id); err != nil {
			return translateErr(err)
		}
		// 底稿上的附件归属也一起解掉（文件实体留给「孤儿附件清理」，
		// 同一份文件可能还挂在凭证或发票上）
		if _, err := tx.Exec(ctx,
			`DELETE FROM attachment WHERE owner_type = 'audit_adjustment' AND owner_id = ?`,
			id); err != nil {
			return translateErr(err)
		}
		// 分录有 ON DELETE CASCADE
		_, err := tx.Exec(ctx, `DELETE FROM audit_adjustment WHERE id = ?`, id)
		return translateErr(err)
	})
}

// MarkBookedInTx 记下这笔调整生成的凭证 id。
//
// ★ 只是「凭证生成了」，不是「已入账」—— 那张凭证还是草稿。
// 入没入账由凭证的状态决定，读的时候算（见 resolvePosted）。
//
// ★ 必须与「生成凭证」在同一个事务里，否则会出现
// 「凭证生成了、调整上却没有凭证 id」—— 用户会再点一次生成，
// 于是同一笔调整在账上出现两张凭证。
func (r *WorkpaperRepo) MarkBookedInTx(ctx context.Context, tx *Tx, id, voucherID int64) error {
	_, err := tx.Exec(ctx,
		`UPDATE audit_adjustment SET voucher_id = ?, updated_at = ? WHERE id = ?`,
		voucherID, nowString(), id)
	return translateErr(err)
}

// ---------------------------------------------------------------------------
// 扫描
// ---------------------------------------------------------------------------

type scanFn2 func(dest ...any) error

func scanAdjustment(scan scanFn2, k period.Key) (*workpaper.Adjustment, error) {
	var a workpaper.Adjustment
	var kind string
	var vid sql.NullInt64
	if err := scan(&a.ID, &a.Code, &kind, &a.Summary, &a.Reason, &a.Evidence,
		&vid, &a.CreatedBy, &a.ReviewedBy); err != nil {
		return nil, translateErr(err)
	}
	a.Period = k
	a.Kind = workpaper.AdjustKind(kind)
	if vid.Valid {
		v := vid.Int64
		a.VoucherID = &v
	}
	return &a, nil
}

// linesOf 批量取分录，返回 adjustment_id → 行。
func (r *WorkpaperRepo) linesOf(ctx context.Context, ids []int64) (map[int64][]workpaper.AdjustLine, error) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT adjustment_id, line_no, account_code, summary, debit, credit,
		       contact_id, employee_id, dept_id, project_id
		  FROM audit_adjustment_line
		 WHERE adjustment_id IN (`+strings.Join(placeholders, ",")+`)
		 ORDER BY adjustment_id, line_no`, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	out := map[int64][]workpaper.AdjustLine{}
	for rows.Next() {
		var (
			aid                int64
			l                  workpaper.AdjustLine
			d, c               int64
			cid, eid, did, pid sql.NullInt64
		)
		if err := rows.Scan(&aid, &l.LineNo, &l.AccountCode, &l.Summary, &d, &c,
			&cid, &eid, &did, &pid); err != nil {
			return nil, translateErr(err)
		}
		l.Debit, l.Credit = money.Money(d), money.Money(c)
		l.ContactID = nullToPtr(cid)
		l.EmployeeID = nullToPtr(eid)
		l.DeptID = nullToPtr(did)
		l.ProjectID = nullToPtr(pid)
		out[aid] = append(out[aid], l)
	}
	return out, translateErr(rows.Err())
}

func nullToPtr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}

// Worksheet 生成某期的审定表。
//
// 账面数来自科目余额表（**借正贷负**），调整只取**未入账**的那些 ——
// 已入账的调整已经体现在账面数里了，再加一次就是重复计算。
func (r *WorkpaperRepo) Worksheet(ctx context.Context, k period.Key) (*workpaper.Worksheet, error) {
	rep, err := r.db.Reports().TrialBalanceReport(ctx, k)
	if err != nil {
		return nil, err
	}
	adjs, err := r.Adjustments(ctx, k)
	if err != nil {
		return nil, err
	}

	// 未入账调整按科目归集
	type pair struct{ debit, credit money.Money }
	byCode := map[string]*pair{}
	for _, a := range adjs {
		if a.Posted {
			continue // ★ 已入账的不再加
		}
		for _, l := range a.Lines {
			p := byCode[l.AccountCode]
			if p == nil {
				p = &pair{}
				byCode[l.AccountCode] = p
			}
			p.debit = p.debit.Add(l.Debit)
			p.credit = p.credit.Add(l.Credit)
		}
	}

	out := &workpaper.Worksheet{Period: k, Rows: []workpaper.WorksheetRow{}}
	for _, row := range rep.Rows {
		if !row.IsLeaf {
			continue // 只列明细科目，汇总行是下级之和
		}
		bal := row.ClosingDebit.Sub(row.ClosingCredit)
		var adjD, adjC money.Money
		if p := byCode[row.AccountCode]; p != nil {
			adjD, adjC = p.debit, p.credit
		}
		// 整行都是零的跳过，否则 190 个科目全塞进去大半没数
		if bal.IsZero() && adjD.IsZero() && adjC.IsZero() {
			continue
		}
		out.Rows = append(out.Rows, workpaper.WorksheetRow{
			AccountCode: row.AccountCode, AccountName: row.AccountName,
			BookBalance: bal, AdjustDebit: adjD, AdjustCredit: adjC,
		})
	}
	m, err := r.Materiality(ctx, k)
	if err != nil {
		return nil, err
	}
	out.Materiality = m
	return out, nil
}

// nextAdjustmentCode 在事务里算出下一个调整编号。
//
// 取的是已有编号的最大序号 + 1，而不是「条数 + 1」：
// 删过中间一笔之后，条数会小于最大序号，用条数会撞号。
// 解析不出序号的旧编号（用户手填的）跳过，不影响取最大值。
func nextAdjustmentCode(ctx context.Context, tx *Tx, k period.Key) (string, error) {
	rows, err := tx.Query(ctx,
		`SELECT code FROM audit_adjustment WHERE year = ? AND month = ?`, k.Year, k.Month)
	if err != nil {
		return "", translateErr(err)
	}
	defer rows.Close()
	max := 0
	prefix := fmt.Sprintf("ADJ-%04d%02d-", k.Year, k.Month)
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return "", translateErr(err)
		}
		if !strings.HasPrefix(code, prefix) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(code, prefix))
		if err != nil {
			continue
		}
		if n > max {
			max = n
		}
	}
	if err := rows.Err(); err != nil {
		return "", translateErr(err)
	}
	return fmt.Sprintf("%s%03d", prefix, max+1), nil
}
