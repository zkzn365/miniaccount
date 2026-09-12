package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/report"
)

// ReportRepo 提供报表取数。
type ReportRepo struct{ db *DB }

// Reports 返回报表仓储。
func (db *DB) Reports() *ReportRepo { return &ReportRepo{db: db} }

// ---------------------------------------------------------------------------
// 解析器：以「余额」取数（资产负债表用）
// ---------------------------------------------------------------------------

// balanceResolver 把「科目取数」实现为**期末余额**。
//
// Value 返回按科目自身余额方向取正的结果：这样累计折旧返回正数，
// 报表公式 `@L(18)-@L(19)` 才能算出正确的固定资产账面价值。
// 若返回原始的「借−贷」，累计折旧是负数，相减就变成相加了。
type balanceResolver struct {
	// raw[code] = 该科目的原始净额（借 − 贷），仅叶子科目有值
	raw map[string]money.Money
	// dir[code] = 该科目的正常余额方向
	dir map[string]account.BalanceDir
	// prefixValue[prefix] = 该前缀（含全部下级叶子）的**自然方向为正**的金额。
	//
	// ⚠️ 计算方式是「先把子树全部叶子的原始净额相加，再按**该科目自身**的
	// 方向取正」，而不是「逐个叶子取正后相加」。
	//
	// 两者在子科目方向不一致时会得到完全不同的结果。典型例子：
	// 「应交税费」是贷方科目，但它的下级「应交增值税—进项税额」是**借方**科目。
	// 若逐叶子取正再相加，进项税额会以正数计入应交税费，
	// 导致资产负债表多计 96 元负债、资产与负债权益不再相等。
	prefixValue map[string]money.Money
	// analyze[prefix] = 按「叶子科目 × 往来单位」分解的原始净额
	analyze map[string][]report.UnitBalance
}

func (r *balanceResolver) Value(prefix string) money.Money { return r.prefixValue[prefix] }

func (r *balanceResolver) Analyze(prefix string) []report.UnitBalance { return r.analyze[prefix] }

var _ report.Resolver = (*balanceResolver)(nil)

// BalanceSheetResolver 构造资产负债表用的取数器。
//
// asOf 是资产负债表日（含当天）。若 fs 非空，会额外计算「年初余额」口径 ——
// 但年初余额是另一个 Resolver，由调用方分别 Compute 两次。
func (r *ReportRepo) BalanceSheetResolver(ctx context.Context, asOf calendar.Date) (report.Resolver, error) {
	return r.resolver(ctx, calendar.Date{}, asOf, modeBalance)
}

// IncomeStatementResolver 构造利润表用的取数器。
//
//	from..to 为空区间时表示「本年累计」（从年初到 to）。
func (r *ReportRepo) IncomeStatementResolver(ctx context.Context, from, to calendar.Date) (report.Resolver, error) {
	return r.resolver(ctx, from, to, modeMovement)
}

type resolverMode int

const (
	modeBalance  resolverMode = iota // 取余额
	modeMovement                     // 取发生额
)

// resolver 是两种口径共用的构造逻辑。
//
// 关键点：Value 对资产负债表意味着「期末余额」，对利润表意味着「本期发生额」。
// 公式语言不变，只是换了个 Resolver —— 这正是把取数抽象成接口的价值。
func (r *ReportRepo) resolver(ctx context.Context, from, to calendar.Date,
	mode resolverMode) (report.Resolver, error) {

	tree, err := r.db.Accounts().Tree(ctx)
	if err != nil {
		return nil, err
	}

	out := &balanceResolver{
		raw:         map[string]money.Money{},
		dir:         map[string]account.BalanceDir{},
		prefixValue: map[string]money.Money{},
		analyze:     map[string][]report.UnitBalance{},
	}
	for _, a := range tree.All() {
		out.dir[a.Code] = a.BalanceDir
	}

	switch mode {
	case modeBalance:
		err = r.fillBalances(ctx, tree, to, out)
	case modeMovement:
		err = r.fillMovements(ctx, tree, from, to, out)
	}
	if err != nil {
		return nil, err
	}

	// 前缀汇总：报表公式写的是汇总科目编码（如 5602 管理费用），
	// 而有余额的是它的叶子科目，必须展开求和 —— 但见 prefixValue 的说明：
	// 先汇总原始净额，最后才按该科目自身的方向取正。
	for _, a := range tree.All() {
		var rawSum money.Money
		for _, leaf := range tree.SubtreeLeaves(a.Code) {
			rawSum = rawSum.Add(out.raw[leaf.Code])
		}
		out.prefixValue[a.Code] = natural(rawSum, a.BalanceDir)
	}
	return out, nil
}

// fillBalances 填充「期末余额」及按方向分解的明细余额。
func (r *ReportRepo) fillBalances(ctx context.Context, tree *account.Tree,
	asOf calendar.Date, out *balanceResolver) error {

	// 1) 按「叶子科目 × 往来单位」算原始净额
	units, err := r.unitBalances(ctx, "", asOf)
	if err != nil {
		return err
	}
	// 2) 按科目汇总，换算成自然方向
	byAccount := map[string]money.Money{}
	for _, u := range units {
		byAccount[u.AccountCode] = byAccount[u.AccountCode].Add(u.Net)
	}
	for code, net := range byAccount {
		out.raw[code] = net
	}

	// 3) 前缀级的明细分解（供 @analyze 使用）
	for _, a := range tree.All() {
		var list []report.UnitBalance
		for _, leaf := range tree.SubtreeLeaves(a.Code) {
			for _, u := range units {
				if u.AccountCode == leaf.Code {
					list = append(list, u)
				}
			}
		}
		if len(list) > 0 {
			out.analyze[a.Code] = list
		}
	}
	return nil
}

// fillMovements 填充「本期发生额」。
//
// 损益类科目按发生额取数而非余额，是因为它们年中会被结转到本年利润，
// 按余额取数会得到 0 或错数。这也正是官方利润表编制说明的要求：
// 「本项目应根据『主营业务收入』科目的**发生额**合计填列」。
func (r *ReportRepo) fillMovements(ctx context.Context, tree *account.Tree,
	from, to calendar.Date, out *balanceResolver) error {

	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT a.code, SUM(le.debit), SUM(le.credit)
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		 WHERE le.biz_date BETWEEN ? AND ?
		 GROUP BY a.code`, from.String(), to.String())
	if err != nil {
		return translateErr(err)
	}
	defer rows.Close()

	type dc struct{ d, c money.Money }
	agg := map[string]dc{}
	for rows.Next() {
		var code string
		var d, c sql.NullInt64
		if err := rows.Scan(&code, &d, &c); err != nil {
			return err
		}
		agg[code] = dc{money.Money(d.Int64), money.Money(c.Int64)}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, a := range tree.All() {
		v := agg[a.Code]
		// 发生额同样只存原始净额（借−贷），方向在 prefixValue 阶段统一应用
		out.raw[a.Code] = v.d.Sub(v.c)
	}

	// 利润表的 @analyze 用得少，但保持语义一致
	for _, a := range tree.All() {
		var list []report.UnitBalance
		for _, leaf := range tree.SubtreeLeaves(a.Code) {
			if v, ok := agg[leaf.Code]; ok {
				list = append(list, report.UnitBalance{
					AccountCode: leaf.Code,
					Net:         v.d.Sub(v.c),
				})
			}
		}
		if len(list) > 0 {
			out.analyze[a.Code] = list
		}
	}
	return nil
}

// natural 把原始净额（借−贷）换算成「按科目余额方向取正」。
func natural(net money.Money, dir account.BalanceDir) money.Money {
	if dir == account.DirCredit {
		return net.Neg()
	}
	return net
}

// unitBalances 返回按「叶子科目 × 往来单位」分解的原始净额（借−贷）。
//
// 分解粒度之所以到往来单位：采用辅助核算时，同一个应收账款科目下
// 客户甲可能挂借方、客户乙挂贷方，只看科目总额无法完成
// 中国报表要求的「按明细方向分析填列」。
func (r *ReportRepo) unitBalances(ctx context.Context, _ string, asOf calendar.Date) ([]report.UnitBalance, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT a.code, le.contact_id, SUM(le.debit), SUM(le.credit)
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		 WHERE le.biz_date <= ?
		 GROUP BY a.code, le.contact_id`, asOf.String())
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []report.UnitBalance
	for rows.Next() {
		var (
			code    string
			contact sql.NullInt64
			d, c    sql.NullInt64
		)
		if err := rows.Scan(&code, &contact, &d, &c); err != nil {
			return nil, err
		}
		out = append(out, report.UnitBalance{
			AccountCode: code,
			ContactID:   toNullInt64(contact),
			Net:         money.Money(d.Int64 - c.Int64),
		})
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 科目余额表
// ---------------------------------------------------------------------------

// TrialBalanceReport 生成科目余额表（六栏式：期初借/贷、本期借/贷、期末借/贷）。
//
// 每一行同时给出该科目**自身**的发生额与余额，以及按层级顺序排列。
// 与财务报表不同，余额表是「按科目列示」的原始数据，不做分析填列 ——
// 它恰恰是会计用来核对财务报表的依据。
func (r *ReportRepo) TrialBalanceReport(ctx context.Context, k period.Key) (*report.BalanceReport, error) {
	tree, err := r.db.Accounts().Tree(ctx)
	if err != nil {
		return nil, err
	}
	rng := report.Range(k)
	dayBefore := rng.From.AddDays(-1)

	// 期初余额（截至上期最后一天）
	opening, err := r.balancesUpTo(ctx, dayBefore)
	if err != nil {
		return nil, err
	}
	// 本期发生额
	period, err := r.movementsBetween(ctx, rng.From, rng.To)
	if err != nil {
		return nil, err
	}
	// 期末余额
	closing, err := r.balancesUpTo(ctx, rng.To)
	if err != nil {
		return nil, err
	}

	// 每个科目的「自身 + 全部下级」编码表，用于汇总科目行的合并数。
	//
	// ★ 汇总科目行必须带下级合计。
	// 分录只会落在明细科目上，所以按科目直接取数的话
	// 「5602 管理费用」这一行的六栏全是 0 —— 而会计打开余额表
	// 第一眼要看的恰恰是这一行。下级有钱、上级显示 0，
	// 看起来就像账丢了钱。
	sub := make(map[string][]string, len(tree.All()))
	for _, a := range tree.All() {
		list := make([]string, 0, 4)
		list = append(list, a.Code)
		for _, d := range tree.Descendants(a.Code) {
			list = append(list, d.Code)
		}
		sub[a.Code] = list
	}
	sumBalance := func(m map[string]money.Money, code string) money.Money {
		var s money.Money
		for _, c := range sub[code] {
			s = s.Add(m[c])
		}
		return s
	}

	out := &report.BalanceReport{Period: k}
	for _, a := range tree.All() {
		obD, obC := report.SplitBalance(sumBalance(opening, a.Code))
		cbD, cbC := report.SplitBalance(sumBalance(closing, a.Code))
		var mv movement
		for _, c := range sub[a.Code] {
			if m, ok := period[c]; ok {
				mv.debit = mv.debit.Add(m.debit)
				mv.credit = mv.credit.Add(m.credit)
			}
		}
		out.Rows = append(out.Rows, report.BalanceRow{
			AccountCode:   a.Code,
			AccountName:   a.Name,
			Level:         a.Level,
			IsLeaf:        a.IsLeaf,
			BalanceDir:    a.BalanceDir,
			OpeningDebit:  obD,
			OpeningCredit: obC,
			PeriodDebit:   mv.debit,
			PeriodCredit:  mv.credit,
			ClosingDebit:  cbD,
			ClosingCredit: cbC,
		})
	}
	report.SortBalanceRows(out.Rows)
	return out, nil
}

type movement struct{ debit, credit money.Money }

// balancesUpTo 返回截至某日（含）各科目的原始净余额（借−贷）。
func (r *ReportRepo) balancesUpTo(ctx context.Context, asOf calendar.Date) (map[string]money.Money, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT a.code, SUM(le.debit) - SUM(le.credit)
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		 WHERE le.biz_date <= ?
		 GROUP BY a.code`, asOf.String())
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	out := map[string]money.Money{}
	for rows.Next() {
		var code string
		var net sql.NullInt64
		if err := rows.Scan(&code, &net); err != nil {
			return nil, err
		}
		out[code] = money.Money(net.Int64)
	}
	return out, rows.Err()
}

// movementsBetween 返回某区间各科目的借贷发生额。
func (r *ReportRepo) movementsBetween(ctx context.Context, from, to calendar.Date) (map[string]movement, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT a.code, SUM(le.debit), SUM(le.credit)
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		 WHERE le.biz_date BETWEEN ? AND ?
		 GROUP BY a.code`, from.String(), to.String())
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	out := map[string]movement{}
	for rows.Next() {
		var code string
		var d, c sql.NullInt64
		if err := rows.Scan(&code, &d, &c); err != nil {
			return nil, err
		}
		out[code] = movement{money.Money(d.Int64), money.Money(c.Int64)}
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 往来余额表
// ---------------------------------------------------------------------------

// ContactBalances 生成往来单位余额表。
//
// accountPrefix 限定科目范围（如 "1122" 只列客户应收、"2241" 只列应付类）；
// 传空字符串表示全部带辅助核算的科目。
func (r *ReportRepo) ContactBalances(ctx context.Context, k period.Key,
	accountPrefix string) ([]report.ContactBalanceRow, error) {

	rng := report.Range(k)
	dayBefore := rng.From.AddDays(-1)

	const q = `
		SELECT c.id, c.name, c.kind, a.code,
		       COALESCE(SUM(CASE WHEN le.biz_date <=  ?  THEN le.debit - le.credit ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN le.biz_date >=  ? AND le.biz_date <=  ? THEN le.debit  ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN le.biz_date >=  ? AND le.biz_date <=  ? THEN le.credit ELSE 0 END), 0),
		       COALESCE(SUM(le.debit - le.credit), 0)
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		  JOIN contact c ON c.id = le.contact_id
		 WHERE le.contact_id IS NOT NULL
		   AND a.code LIKE ? || '%'
		 GROUP BY c.id, a.code
		 ORDER BY a.code, c.name`

	rows, err := r.db.sql.QueryContext(ctx, q,
		dayBefore.String(),
		rng.From.String(), rng.To.String(),
		rng.From.String(), rng.To.String(),
		accountPrefix)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []report.ContactBalanceRow
	for rows.Next() {
		var (
			row                    report.ContactBalanceRow
			opening, d, c, closing int64
		)
		if err := rows.Scan(&row.ContactID, &row.ContactName, &row.ContactKind,
			&row.AccountCode, &opening, &d, &c, &closing); err != nil {
			return nil, err
		}
		row.Opening = money.Money(opening)
		row.Debit = money.Money(d)
		row.Credit = money.Money(c)
		row.Closing = money.Money(closing)
		out = append(out, row)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 便捷入口
// ---------------------------------------------------------------------------

// BuildBalanceSheet 生成并计算资产负债表。
//
// 返回 (期末余额表, 年初余额表, 勾稽问题)。
// 两张表都算：中国资产负债表要求同时列示「期末余额」与「年初余额」两栏。
func (r *ReportRepo) BuildBalanceSheet(ctx context.Context, asOf calendar.Date) (
	closing, opening *report.Definition, issues []report.CheckIssue, err error) {

	// 期末
	closing, err = report.LoadBalanceSheet()
	if err != nil {
		return nil, nil, nil, err
	}
	rc, err := r.BalanceSheetResolver(ctx, asOf)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := closing.Compute(rc); err != nil {
		return nil, nil, nil, err
	}

	// 年初余额 = 上年末的期末余额（官方原文：「应根据上年末资产负债表
	// 『期末余额』栏内所列数字填列」）
	opening, err = report.LoadBalanceSheet()
	if err != nil {
		return nil, nil, nil, err
	}
	yearStart, _ := calendar.New(asOf.Year, 1, 1)
	ro, err := r.BalanceSheetResolver(ctx, yearStart.AddDays(-1))
	if err != nil {
		return nil, nil, nil, err
	}
	if err := opening.Compute(ro); err != nil {
		return nil, nil, nil, err
	}

	return closing, opening, report.CheckBalanceSheet(closing), nil
}

// BuildIncomeStatement 生成并计算利润表。
//
// 返回 (本月数, 本年累计数, 勾稽问题)。官方利润表要求两栏。
func (r *ReportRepo) BuildIncomeStatement(ctx context.Context, k period.Key) (
	current, ytd *report.Definition, issues []report.CheckIssue, err error) {

	rng := report.Range(k)
	yearStart, err := calendar.New(k.Year, 1, 1)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sqlite: 年度起始日: %w", err)
	}

	current, err = report.LoadIncomeStatement()
	if err != nil {
		return nil, nil, nil, err
	}
	rc, err := r.IncomeStatementResolver(ctx, rng.From, rng.To)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := current.Compute(rc); err != nil {
		return nil, nil, nil, err
	}

	ytd, err = report.LoadIncomeStatement()
	if err != nil {
		return nil, nil, nil, err
	}
	ry, err := r.IncomeStatementResolver(ctx, yearStart, rng.To)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := ytd.Compute(ry); err != nil {
		return nil, nil, nil, err
	}

	return current, ytd, report.CheckIncomeStatement(current), nil
}
