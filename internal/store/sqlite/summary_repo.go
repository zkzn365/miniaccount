package sqlite

import (
	"context"
	"fmt"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/summary"
)

// SummaryRepo 生成凭证汇总表。
type SummaryRepo struct{ db *DB }

// Summary 返回凭证汇总仓储。
func (db *DB) Summary() *SummaryRepo { return &SummaryRepo{db: db} }

// BuildSummary 生成某期间的凭证汇总表。
//
// # 取数口径
//
// 凭证与其分录**一起从 `ledger_entry` 侧取**，而不是先取 voucher 再取
// `voucher_entry`。理由是这张表必须和总账对得上：
//
//   - `ledger_entry` 才是总账，报表全部由它汇总而来；
//   - `voucher_entry` 是凭证自身的行，两者理论上镜像，但一旦因为
//     某个 bug 出现不一致，从凭证侧取数会得到一张「与总账不符」的汇总表，
//     而这种不符极难发现 —— 表面看每个数都对。
//
// 从总账侧取数则天然不可能与总账不符。
//
// # 哪些凭证计入
//
// 有总账分录的凭证一律计入：已记账的（posted）与已被红字冲销的（voided）。
// 后者**必须**计入 —— 红字冲销模型下原凭证与红字凭证都留在账上、
// 都参与汇总，排除任何一个都会让汇总表和总账对不上。
// 草稿没有总账分录，因此天然不在其中，只统计张数用于提示。
func (r *SummaryRepo) BuildSummary(ctx context.Context,
	from, to calendar.Date) (*summary.Report, error) {

	if !from.Valid() || !to.Valid() || from.After(to) {
		return nil, fmt.Errorf("%w: 期间 %s 至 %s", summary.ErrBadRange, from, to)
	}

	vouchers, drafts, err := r.loadVouchers(ctx, from, to)
	if err != nil {
		return nil, err
	}
	accounts, err := r.accountInfo(ctx)
	if err != nil {
		return nil, err
	}

	return summary.Build(summary.Input{
		From: from, To: to,
		Vouchers: vouchers, Accounts: accounts, DraftCount: drafts,
	})
}

// loadVouchers 取期间内的凭证明细与草稿数。
func (r *SummaryRepo) loadVouchers(ctx context.Context,
	from, to calendar.Date) ([]summary.Voucher, int, error) {

	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT v.id, v.word, COALESCE(v.no, ''), le.biz_date,
		       CASE WHEN v.status = 'voided' THEN 1 ELSE 0 END,
		       CASE WHEN v.reverses_id IS NOT NULL THEN 1 ELSE 0 END,
		       v.attach_count,
		       a.code, le.debit, le.credit
		  FROM ledger_entry le
		  JOIN voucher v ON v.id = le.voucher_id
		  JOIN account a ON a.id = le.account_id
		 WHERE le.biz_date >= ? AND le.biz_date <= ?
		 ORDER BY le.biz_date, v.word, v.seq, v.id, le.line_no`,
		from.String(), to.String())
	if err != nil {
		return nil, 0, translateErr(err)
	}
	defer rows.Close()

	var (
		out  []summary.Voucher
		byID = map[int64]int{}
	)
	for rows.Next() {
		var (
			id, voided, reversal, attach int
			word, no, date, code         string
			debit, credit                int64
		)
		if err := rows.Scan(&id, &word, &no, &date, &voided, &reversal, &attach,
			&code, &debit, &credit); err != nil {
			return nil, 0, err
		}
		d, err := calendar.Parse(date)
		if err != nil {
			return nil, 0, err
		}

		idx, ok := byID[int64(id)]
		if !ok {
			idx = len(out)
			byID[int64(id)] = idx
			out = append(out, summary.Voucher{
				ID: int64(id), Word: word, No: no, Date: d,
				Voided: voided != 0, Reversal: reversal != 0,
				Attachments: attach,
			})
		}
		out[idx].Entries = append(out[idx].Entries, summary.Entry{
			AccountCode: code,
			Debit:       money.Money(debit),
			Credit:      money.Money(credit),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// 草稿：不占凭证号、不进总账，但月底必须让会计知道它们还在。
	var drafts int
	if err := r.db.sql.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM voucher
		 WHERE status = 'draft' AND biz_date >= ? AND biz_date <= ?`,
		from.String(), to.String()).Scan(&drafts); err != nil {
		return nil, 0, translateErr(err)
	}
	return out, drafts, nil
}

// accountInfo 取科目档案里汇总要用的部分。
func (r *SummaryRepo) accountInfo(ctx context.Context) (map[string]summary.AccountInfo, error) {
	list, err := r.db.Accounts().List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]summary.AccountInfo, len(list))
	for _, a := range list {
		out[a.Code] = summary.AccountInfo{
			Code: a.Code, Name: shortName(a.Name),
			FullName: a.Name, BalanceDir: string(a.BalanceDir),
		}
	}
	return out, nil
}
