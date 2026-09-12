package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/cashflow"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

// cfLine 是配对待用的一条总账分录。
type cfLine struct {
	code          string
	debit, credit money.Money
	summary       string
}

// CashFlowRepo 生成现金流量表。
//
// # 为什么在存储层做「按凭证配对」
//
// 现金流量表的直接法要求把每笔现金收支与它的**对方科目**放在一起看，
// 而总账是按行存的：同一张凭证的借方行与贷方行各自独立。
// 把「同一凭证的现金行 ↔ 非现金行」配对是纯粹的取数工作，
// 放在存储层一次查完，比让领域层反复回表干净得多。
type CashFlowRepo struct{ db *DB }

// CashFlow 返回现金流量表仓储。
func (db *DB) CashFlow() *CashFlowRepo { return &CashFlowRepo{db: db} }

// Statement 生成某期间的现金流量表。
//
// classifier 传 nil 时使用默认归类规则。
func (r *CashFlowRepo) Statement(ctx context.Context, from, to calendar.Date,
	classifier *cashflow.Classifier) (*cashflow.Statement, error) {

	if classifier == nil {
		classifier = cashflow.DefaultClassifier()
	}
	var (
		entries []cashflow.CashEntry
		opening money.Money
		actual  money.Money
	)
	err := r.db.Read(ctx, func(q Querier) error {
		var e error
		if entries, e = cashEntries(ctx, q, from, to); e != nil {
			return e
		}
		// 期初 = 期初之前所有现金类科目的净额
		if opening, e = cashBalance(ctx, q, from.AddDays(-1)); e != nil {
			return e
		}
		// 期末实际余额，用于交叉验证
		actual, e = cashBalance(ctx, q, to)
		return e
	})
	if err != nil {
		return nil, err
	}

	st, err := cashflow.Build(cashflow.Input{
		From: from, To: to, CashEntries: entries, OpeningCash: opening,
	}, classifier)
	if err != nil {
		return nil, err
	}

	// ★ 交叉验证：按凭证推算出的期末现金，必须等于科目余额表上的期末现金。
	//
	// 两者不等说明有现金分录没被纳入（例如新增了「其他货币资金」明细
	// 却没加进现金科目清单）。这种事必须当场报出来，而不是让用户
	// 拿着对不上的报表自己找。
	st.ActualClosingCash = &actual
	return st, nil
}

// Entries 返回区间内涉及现金科目的分录，供界面逐笔查看。
func (r *CashFlowRepo) Entries(ctx context.Context, from, to calendar.Date) ([]cashflow.CashEntry, error) {
	var out []cashflow.CashEntry
	err := r.db.Read(ctx, func(q Querier) error {
		var e error
		out, e = cashEntries(ctx, q, from, to)
		return e
	})
	return out, err
}

// StatementForPeriod 是 Statement 的按会计期间取值版本。
func (r *CashFlowRepo) StatementForPeriod(ctx context.Context, k period.Key,
	classifier *cashflow.Classifier) (*cashflow.Statement, error) {

	rng := periodRange(k)
	return r.Statement(ctx, rng.From, rng.To, classifier)
}

// ---------------------------------------------------------------------------
// 取数
// ---------------------------------------------------------------------------

// cashEntries 把区间内的总账分录按凭证配对成现金分录。
//
// 只查「涉及现金科目的凭证」：先用子查询找出这些凭证，
// 再整张取回，这样同凭证内的对方科目自然齐全。
func cashEntries(ctx context.Context, q Querier, from, to calendar.Date) ([]cashflow.CashEntry, error) {
	cashLike, cashArgs := cashPrefixClause("a2.code")

	rows, err := q.Query(ctx, `
		SELECT le.voucher_id, COALESCE(v.no, ''), le.biz_date, a.code,
		       le.debit, le.credit, le.summary
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		  LEFT JOIN voucher v ON v.id = le.voucher_id
		 WHERE le.biz_date BETWEEN ? AND ?
		   AND le.voucher_id IN (
		         SELECT DISTINCT le2.voucher_id
		           FROM ledger_entry le2
		           JOIN account a2 ON a2.id = le2.account_id
		          WHERE le2.biz_date BETWEEN ? AND ? AND `+cashLike+`)
		 ORDER BY le.biz_date, le.voucher_id, le.line_no, le.id`,
		append([]any{from.String(), to.String(), from.String(), to.String()}, cashArgs...)...)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	// 按凭证聚拢。查询已按 voucher_id 排序，边读边收即可，不需要 map。
	var (
		out     []cashflow.CashEntry
		curID   int64
		curDate calendar.Date
		curNo   string
		cur     []cfLine
	)

	flush := func() error {
		if len(cur) == 0 {
			return nil
		}
		e, ok := pairVoucher(curNo, curDate, cur)
		if ok {
			out = append(out, e)
		}
		cur = cur[:0]
		return nil
	}

	for rows.Next() {
		var (
			vid     sql.NullInt64
			no, biz string
			ln      cfLine
			d, c    int64
		)
		if err := rows.Scan(&vid, &no, &biz, &ln.code, &d, &c, &ln.summary); err != nil {
			return nil, err
		}
		ln.debit, ln.credit = money.Money(d), money.Money(c)

		if vid.Int64 != curID {
			if err := flush(); err != nil {
				return nil, err
			}
			curID, curNo = vid.Int64, no
			date, err := calendar.Parse(biz)
			if err != nil {
				return nil, fmt.Errorf("现金流量表：凭证日期 %q 无法解析: %w", biz, err)
			}
			curDate = date
		}
		cur = append(cur, ln)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, flush()
}

// pairVoucher 把一张凭证的分录拆成「现金方」与「对方科目」。
//
// 返回 ok=false 表示这张凭证不构成现金流，应整体跳过。
func pairVoucher(no string, date calendar.Date, lines []cfLine) (cashflow.CashEntry, bool) {
	var (
		amount  money.Money
		cashAcc string
		summary string
		counter []cashflow.CounterAccount
	)
	for _, ln := range lines {
		net := ln.debit.Sub(ln.credit)
		if cashflow.IsCashAccount(ln.code) {
			amount = amount.Add(net)
			if cashAcc == "" {
				cashAcc = ln.code
			}
			if summary == "" {
				summary = ln.summary
			}
			continue
		}
		counter = append(counter, cashflow.CounterAccount{Code: ln.code, Amount: net})
	}

	// 现金科目之间互转（提现、存现、内部调拨）不构成现金流量：
	// 现金总额没变，出现在表里只会让经营/投资/筹资三块都虚增。
	if amount.IsZero() || len(counter) == 0 {
		return cashflow.CashEntry{}, false
	}
	return cashflow.CashEntry{
		Date: date, VoucherNo: no, CashAccount: cashAcc,
		Amount: amount, CounterAccounts: counter, Summary: summary,
	}, true
}

// cashBalance 返回截至某日（含）全部现金及现金等价物科目的净余额。
func cashBalance(ctx context.Context, q Querier, asOf calendar.Date) (money.Money, error) {
	like, args := cashPrefixClause("a.code")
	var net sql.NullInt64
	err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(le.debit), 0) - COALESCE(SUM(le.credit), 0)
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		 WHERE le.biz_date <= ? AND `+like,
		append([]any{asOf.String()}, args...)...).Scan(&net)
	if err != nil {
		return 0, translateErr(err)
	}
	return money.Money(net.Int64), nil
}

// cashPrefixClause 按 CashAccountRoots 生成一段 `(col LIKE ? OR ...)`。
//
// 从领域层的清单生成，而不是在 SQL 里另写一份科目前缀 ——
// 两处不一致时现金流量表会静默漏掉科目，是最难查的一类错账。
func cashPrefixClause(col string) (string, []any) {
	parts := make([]string, 0, len(cashflow.CashAccountRoots))
	args := make([]any, 0, len(cashflow.CashAccountRoots))
	for _, root := range cashflow.CashAccountRoots {
		parts = append(parts, col+" LIKE ?")
		args = append(args, root+"%")
	}
	if len(parts) == 0 {
		return "1 = 0", nil // 没有现金科目：任何行都不匹配
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

// periodRange 返回某会计期间的日期区间。
func periodRange(k period.Key) calendar.Range {
	from, _ := calendar.New(k.Year, k.Month, 1)
	to, _ := calendar.New(k.Year, k.Month, calendar.DaysInMonth(k.Year, k.Month))
	return calendar.Range{From: from, To: to}
}

// CashFlowFacade 是给界面用的便捷入口：一次拿到报表与逐笔明细。
type CashFlowFacade struct {
	Statement *cashflow.Statement
	Entries   []cashflow.CashEntry
}

// CashFlowDetail 生成某期间的现金流量表连同逐笔明细。
func (r *CashFlowRepo) CashFlowDetail(ctx context.Context, k period.Key,
	classifier *cashflow.Classifier) (*CashFlowFacade, error) {

	rng := periodRange(k)
	var f CashFlowFacade
	err := r.db.Read(ctx, func(q Querier) error {
		var e error
		if f.Entries, e = cashEntries(ctx, q, rng.From, rng.To); e != nil {
			return e
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	st, err := r.Statement(ctx, rng.From, rng.To, classifier)
	if err != nil {
		return nil, err
	}
	f.Statement = st
	return &f, nil
}
