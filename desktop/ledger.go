package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/service"
)

// contextT / sqlNullInt64 是别名，只为让下面几个取数函数的签名短一些。
//
// 这层代码不对外暴露，短签名换来的是「一眼能看完一个函数的职责」。
type contextT = context.Context
type sqlNullInt64 = sql.NullInt64

// ---------------------------------------------------------------------------
// 明细账
// ---------------------------------------------------------------------------

// LedgerRequest 是查明细账的参数。
type LedgerRequest struct {
	// AccountPrefix 是科目编码前缀，如 "1002"、"2241"。
	AccountPrefix string `json:"accountPrefix"`
	// From / To 是日期区间（YYYY-MM-DD），留空表示账套全期间。
	From string `json:"from"`
	To   string `json:"to"`
	// Limit 限制返回行数；0 表示默认上限。
	Limit int `json:"limit"`
}

// LedgerRow 是明细账的一行。
type LedgerRow struct {
	Date    string `json:"date"`
	Voucher string `json:"voucher"`
	Summary string `json:"summary"`
	// Debit / Credit 是本期发生额（分）。
	Debit  int64 `json:"debit"`
	Credit int64 `json:"credit"`
	// Balance 是**逐行累计**的余额（分），带符号（借正贷负）。
	//
	// 余额由 Go 侧算好而不是让前端累加：前端的数字是 JS number，
	// 大额账套累加到千亿分（十亿元）时仍安全，但一旦有人改了
	// 分页或筛选逻辑，前端的累加就会与账面不符 —— 而余额错了
	// 整张明细账就没有意义。
	Balance int64 `json:"balance"`
	// Dir 是该行的余额方向：借 / 贷 / 平。
	Dir string `json:"dir"`
	// Contra 是**对方科目**，如「主营业务收入」「办公费、应交税费」。
	//
	// 日记账（现金/银行存款）最要紧的一列：会计看银行存款日记账时
	// 真正想知道的是「这笔钱从哪来、到哪去」，只给借贷金额是不够的。
	// 一张凭证可能有多条其他分录，此时按「、」连接。
	Contra string `json:"contra"`
}

// LedgerResult 是明细账的返回。
type LedgerResult struct {
	AccountPrefix string      `json:"accountPrefix"`
	AccountName   string      `json:"accountName"`
	From          string      `json:"from"`
	To            string      `json:"to"`
	Rows          []LedgerRow `json:"rows"`
	// OpeningBalance 是区间开始前的余额（分，带符号）。
	OpeningBalance int64 `json:"openingBalance"`
	// ClosingBalance 是区间结束时的余额（分，带符号）。
	ClosingBalance int64 `json:"closingBalance"`
	// Truncated 为真表示行数被 Limit 截断。
	Truncated bool `json:"truncated"`
}

// LedgerDetail 返回某科目的明细账。
func (a *App) LedgerDetail(req LedgerRequest) (out *LedgerResult, err error) {
	defer recoverTo(&err, "LedgerDetail")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	prefix := strings.TrimSpace(req.AccountPrefix)
	if prefix == "" {
		return nil, &Fault{Kind: FaultInvalid, Message: "请填写科目编码前缀，如 1002"}
	}

	ctx := a.context()
	from, to, err := resolveRange(ctx, svc, req.From, req.To)
	if err != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: err.Error()}
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 2000
	}

	rows, err := svc.DB().Vouchers().Detail(ctx, prefix, from, to)
	if err != nil {
		return nil, err
	}

	result := &LedgerResult{
		AccountPrefix: prefix,
		From:          from.String(),
		To:            to.String(),
	}
	if acc, err := svc.DB().Accounts().GetByCode(ctx, prefix); err == nil {
		result.AccountName = acc.Name
	}

	// 期初 = 区间开始前的累计净额
	opening, err := prefixBalanceBefore(ctx, svc, prefix, from)
	if err != nil {
		return nil, err
	}
	result.OpeningBalance = int64(opening)

	balance := opening
	for _, r := range rows {
		if len(result.Rows) >= limit {
			result.Truncated = true
			break
		}
		balance = balance.Add(r.Debit).Sub(r.Credit)
		result.Rows = append(result.Rows, LedgerRow{
			Date: r.BizDate.String(), Voucher: r.VoucherNo, Summary: r.Summary,
			Debit: int64(r.Debit), Credit: int64(r.Credit),
			Balance: int64(balance), Dir: balanceDirLabel(balance),
			Contra: strings.Join(r.ContraAccounts, "、"),
		})
	}
	result.ClosingBalance = int64(balance)
	return result, nil
}

// prefixBalanceBefore 返回某科目前缀在给定日期之前的累计净额（借−贷）。
//
// 这里是整份代码里少数几处直接写 SQL 的桌面层代码，因此更要写清楚理由：
// 「按科目前缀汇总某日之前的余额」在存储层没有现成的公开方法，
// 而它只是明细账的一个取数细节，不值得为它往 store 里加一个专用接口。
func prefixBalanceBefore(ctx contextT, svc *service.Service,
	prefix string, before date) (money.Money, error) {

	var net sqlNullInt64
	err := svc.DB().SQL().QueryRowContext(ctx, `
		SELECT COALESCE(SUM(le.debit), 0) - COALESCE(SUM(le.credit), 0)
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		 WHERE a.code LIKE ? AND le.biz_date < ?`,
		prefix+"%", before.String()).Scan(&net)
	if err != nil {
		return 0, err
	}
	return money.Money(net.Int64), nil
}

// resolveRange 解析日期区间；留空时取账套的全部会计期间。
func resolveRange(ctx contextT, svc *service.Service,
	from, to string) (date, date, error) {

	book, err := svc.Book(ctx)
	if err != nil {
		return date{}, date{}, err
	}
	var dFrom, dTo date
	if len(book.Periods) > 0 {
		if dFrom, err = calendar.Parse(book.Periods[0].From); err != nil {
			return date{}, date{}, err
		}
		if dTo, err = calendar.Parse(book.Periods[len(book.Periods)-1].To); err != nil {
			return date{}, date{}, err
		}
	} else {
		dFrom, dTo = calendar.Today(), calendar.Today()
	}
	if strings.TrimSpace(from) != "" {
		if dFrom, err = calendar.Parse(from); err != nil {
			return date{}, date{}, fmt.Errorf("起始日期 %q 格式不对，应为 YYYY-MM-DD", from)
		}
	}
	if strings.TrimSpace(to) != "" {
		if dTo, err = calendar.Parse(to); err != nil {
			return date{}, date{}, fmt.Errorf("截止日期 %q 格式不对，应为 YYYY-MM-DD", to)
		}
	}
	if dTo.Before(dFrom) {
		return date{}, date{}, fmt.Errorf("截止日期不能早于起始日期")
	}
	return dFrom, dTo, nil
}

// balanceDirLabel 返回余额的中文方向。
func balanceDirLabel(v money.Money) string {
	switch {
	case v.IsPositive():
		return "借"
	case v.IsNegative():
		return "贷"
	default:
		return "平"
	}
}
