package sqlite

import (
	"context"
	"fmt"
	"strings"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/columnar"
	"miniaccount/internal/domain/money"
)

// ColumnarRepo 生成多栏式明细账。
type ColumnarRepo struct{ db *DB }

// Columnar 返回多栏式明细账仓储。
func (db *DB) Columnar() *ColumnarRepo { return &ColumnarRepo{db: db} }

// BuildColumnar 生成某科目的多栏式明细账。
//
// # 栏目就是下级科目
//
// 取该科目下的**全部可记账明细科目**作为栏目，每栏的方向取该科目自己的
// balance_dir。于是：
//
//	5602 管理费用      → 17 个借方栏目（办公费、差旅费、工资…）
//	222101 应交增值税  → 6 个借方栏目 + 4 个贷方栏目（增值税专栏）
//
// 两侧同时有栏目时，这张表就是「双侧多栏式」，正是增值税专用格式。
// 之所以能这么做，是因为财会〔2016〕22号 规定的 10 个增值税专栏
// 在本工程的科目表里就是 222101 的 10 个子科目 —— 见 docs/05。
//
// # 为什么不用「科目编码前缀」
//
// 科目编码在本工程里是可以被用户改的（新增/改名/调序），
// 用 `code LIKE '5602%'` 会在用户把某个明细科目换到别处之后算错。
// 这里改用科目树算出的 id 集合，与编码无关。
func (r *ColumnarRepo) BuildColumnar(ctx context.Context,
	accountCode string, from, to calendar.Date) (*columnar.Report, error) {

	if accountCode == "" {
		return nil, fmt.Errorf("%w: 未指定科目", ErrNotFound)
	}
	if !from.Valid() || !to.Valid() || from.After(to) {
		return nil, fmt.Errorf("%w: 期间 %s 至 %s", columnar.ErrBadRange, from, to)
	}

	tree, err := r.db.Accounts().Tree(ctx)
	if err != nil {
		return nil, err
	}
	head, ok := tree.Get(accountCode)
	if !ok {
		return nil, fmt.Errorf("%w: 科目 %s", ErrNotFound, accountCode)
	}

	// 栏目 = 该科目下的全部可记账明细科目
	//
	// ★ 必须用「严格下级」，不能用 Tree.SubtreeLeaves：
	// 后者在科目本身是明细科目时会返回它自己（这是余额汇总想要的语义），
	// 于是「1002 银行存款」会得到一个名为「银行存款」的栏目 ——
	// 一张只有一栏的多栏式明细账，既没用又难看。
	desc := tree.Descendants(accountCode)
	leaves := make([]*account.Account, 0, len(desc))
	for _, d := range desc {
		if d.IsLeaf {
			leaves = append(leaves, d)
		}
	}
	if len(leaves) == 0 {
		return nil, fmt.Errorf(
			"%w: 科目「%s %s」没有下级科目，多栏式明细账需要至少一个下级科目；"+
				"没有下级科目时请改用明细账（三栏式）",
			columnar.ErrNoColumns, head.Code, head.Name)
	}

	// 科目 id 集合：本级 + **全部**下级（含中间汇总科目）。
	//
	// 中间层也要算进去 —— 万一有分录直接记在某个汇总科目上
	// （历史上它是明细科目、后来才加了子科目），那笔钱不该凭空消失，
	// 而应进「其他」栏并在提示里点出来。少一个 id 就是少一笔钱。
	ids := make([]int64, 0, len(desc)+1)
	ids = append(ids, head.ID)
	for _, d := range desc {
		ids = append(ids, d.ID)
	}
	cols := make([]columnar.Column, 0, len(leaves))
	for _, l := range leaves {
		side := columnar.SideDebit
		if l.BalanceDir == account.DirCredit {
			side = columnar.SideCredit
		}
		cols = append(cols, columnar.Column{
			Key: l.Code, Label: shortName(l.Name),
			Side: side, AccountCode: l.Code,
		})
	}
	// 栏目顺序：借方栏目在前、贷方栏目在后，组内按科目树顺序。
	// 增值税表就是靠这一步得到「左借方 6 栏、右贷方 4 栏」的版式。
	cols = orderColumns(cols)

	// 期初余额：期间开始日之前的净额（借 − 贷）
	opening, err := r.subtreeNet(ctx, ids, calendar.Date{}, from.AddDays(-1))
	if err != nil {
		return nil, err
	}

	movs, err := r.subtreeMovements(ctx, ids, from, to)
	if err != nil {
		return nil, err
	}

	return columnar.Build(columnar.Input{
		AccountCode: head.Code, AccountName: head.Name,
		From: from, To: to,
		Columns: cols, Opening: opening, Movements: movs,
	})
}

// orderColumns 把借方栏目排在前面、贷方栏目排在后面，组内保持原顺序。
//
// 不按方向交错是刻意的：多栏式明细账的阅读方式是「先看借方这一片、
// 再看贷方那一片」，交错排列会让会计每次都重新找栏。
func orderColumns(in []columnar.Column) []columnar.Column {
	out := make([]columnar.Column, 0, len(in))
	for _, want := range []columnar.Side{columnar.SideDebit, columnar.SideCredit} {
		for _, c := range in {
			if c.Side == want {
				out = append(out, c)
			}
		}
	}
	return out
}

// shortName 从「管理费用—办公费」取出「办公费」作为栏目标题。
//
// 栏目标题必须短：一张管理费用明细账有 17 栏，
// 每栏都顶着「管理费用—办公费」这七个字，横向根本排不下。
func shortName(name string) string {
	if i := strings.LastIndex(name, "—"); i >= 0 {
		if s := strings.TrimSpace(name[i+len("—"):]); s != "" {
			return s
		}
	}
	return name
}

// subtreeNet 汇总一组科目在 [from, to] 区间的净额（借 − 贷）。
// from 为零值时表示不限起点。
func (r *ColumnarRepo) subtreeNet(ctx context.Context,
	ids []int64, from, to calendar.Date) (money.Money, error) {

	if len(ids) == 0 {
		return 0, nil
	}
	ph := placeholders(len(ids))
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}

	q := `SELECT COALESCE(SUM(debit) - SUM(credit), 0) FROM ledger_entry
	       WHERE account_id IN (` + ph + `) AND biz_date <= ?`
	args = append(args, to.String())
	if from.Valid() {
		q = `SELECT COALESCE(SUM(debit) - SUM(credit), 0) FROM ledger_entry
		      WHERE account_id IN (` + ph + `) AND biz_date >= ? AND biz_date <= ?`
		args = append(args, from.String(), to.String())
	}

	var net int64
	if err := r.db.sql.QueryRowContext(ctx, q, args...).Scan(&net); err != nil {
		return 0, translateErr(err)
	}
	return money.Money(net), nil
}

// subtreeMovements 取一组科目在 [from, to] 区间的全部分录，逐笔返回。
func (r *ColumnarRepo) subtreeMovements(ctx context.Context,
	ids []int64, from, to calendar.Date) ([]columnar.Movement, error) {

	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, from.String(), to.String())

	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT le.biz_date, COALESCE(v.no, ''), le.summary, a.code,
		       le.line_no, le.debit, le.credit
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		  LEFT JOIN voucher v ON v.id = le.voucher_id
		 WHERE le.account_id IN (`+placeholders(len(ids))+`)
		   AND le.biz_date >= ? AND le.biz_date <= ?
		 ORDER BY le.biz_date, v.no, le.voucher_id, le.line_no`, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []columnar.Movement
	for rows.Next() {
		var (
			date, no, summary, code string
			seq                     int
			debit, credit           int64
		)
		if err := rows.Scan(&date, &no, &summary, &code, &seq, &debit, &credit); err != nil {
			return nil, err
		}
		d, err := calendar.Parse(date)
		if err != nil {
			return nil, err
		}
		out = append(out, columnar.Movement{
			Date: d, VoucherNo: no, Summary: summary, AccountCode: code,
			Seq: seq, Debit: money.Money(debit), Credit: money.Money(credit),
		})
	}
	return out, rows.Err()
}

// ColumnCandidates 返回可以做多栏式明细账的科目（有下级明细的科目）。
//
// 界面用它填下拉框。只列有下级的科目 —— 没有下级的科目做不出多栏式，
// 列出来只会让用户白选一次再看到报错。
func (r *ColumnarRepo) ColumnCandidates(ctx context.Context) ([]*account.Account, error) {
	tree, err := r.db.Accounts().Tree(ctx)
	if err != nil {
		return nil, err
	}
	var out []*account.Account
	for _, a := range tree.All() {
		if hasLeafDescendant(tree, a.Code) {
			out = append(out, a)
		}
	}
	return out, nil
}

// hasLeafDescendant 报告某科目是否有可记账的下级科目。
//
// 与 BuildColumnar 用同一个判据：**严格下级**里的明细科目。
// 用 SubtreeLeaves 会让「1002 银行存款」这类没有下级的科目
// 也出现在候选列表里，用户选了才发现做不出来。
func hasLeafDescendant(tree *account.Tree, code string) bool {
	for _, d := range tree.Descendants(code) {
		if d.IsLeaf {
			return true
		}
	}
	return false
}

// placeholders 生成 "?, ?, ?" 形式的占位符串。
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
