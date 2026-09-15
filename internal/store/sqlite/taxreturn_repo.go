package sqlite

import (
	"context"
	"fmt"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

// TaxReturnRepo 是税务计算表的取数层。
//
// ★ 它**只搬数，不算数**：算的规则在 domain/taxreturn 里。
// 取数与算法分开，是为了让「这个数从哪来」与「这个数怎么算」
// 各自能被单独测。以前的报表把两者混在一起，
// 一个数字对不上，得先猜是取错了还是算错了。
type TaxReturnRepo struct{ db *DB }

// TaxReturns 返回税务计算表仓储。
func (db *DB) TaxReturns() *TaxReturnRepo { return &TaxReturnRepo{db: db} }

// VATColumns 是增值税各专栏的本期发生额。
//
// 专栏就是科目：财会〔2016〕22号 规定的 10 个专栏在本工程的科目表里
// 就是 222101 下的 10 个子科目，所以直接读发生额即可 ——
// 不需要另建一套「税务台账」，也就不会出现账与台账两个数。
type VATColumns struct {
	Output              money.Money // 22210102 销项税额（贷方）
	OutputDeducted      money.Money // 22210118 销项税额抵减（借方）
	Input               money.Money // 22210101 进项税额（借方）
	InputTransferredOut money.Money // 22210108 进项税额转出（贷方）
	ExportTaxRefund     money.Money // 22210107 出口退税（贷方）
	ExportOffset        money.Money // 22210109 出口抵减内销产品应纳税额（借方）
	TaxRelief           money.Money // 22210106 减免税款（借方）
	Paid                money.Money // 22210103 已交税金（借方）
	// PriorCredit 是上期期末留抵税额。
	//
	// ★ 留抵不是「本期的数」，它只能从**上期余额**来：
	// 算本期时现算上期，算上期时再算上上期 —— 账套里的余额是
	// 唯一的事实来源。取「222101 各专栏在上期期末的借方净额」，
	// 贷方余额（应交未交）不算留抵。
	PriorCredit money.Money
	// Notes 是取数说明（进底稿/表的来源列）。
	Notes []string
	// Warnings 是取数时发现的、要提醒办税人的问题（如专栏净额为负）。
	Warnings []string
}

// vatColumnCodes 把专栏编码与「借/贷」方向绑在一起。
//
// 写死方向而不是读科目表的 balance_dir：科目表可以被用户改，
// 而专栏的方向是规定死的 —— 销项税额永远在贷方。
// 从科目表读的话，用户把 22210102 的方向改成借方，
// 增值税表就会算出一个符号相反的数，而没有任何提示。
var vatColumnCodes = map[string]struct {
	Field string
	Side  string // debit | credit
}{
	"22210101": {"Input", "debit"},
	"22210102": {"Output", "credit"},
	"22210103": {"Paid", "debit"},
	"22210106": {"TaxRelief", "debit"},
	"22210107": {"ExportTaxRefund", "credit"},
	"22210108": {"InputTransferredOut", "credit"},
	"22210109": {"ExportOffset", "debit"},
	"22210118": {"OutputDeducted", "debit"},
}

// VATColumnsOf 取某期增值税各专栏的发生额。
func (r *TaxReturnRepo) VATColumnsOf(ctx context.Context, k period.Key) (*VATColumns, error) {
	rep, err := r.db.Reports().TrialBalanceReport(ctx, k)
	if err != nil {
		return nil, err
	}
	out := &VATColumns{}
	for _, row := range rep.Rows {
		meta, ok := vatColumnCodes[row.AccountCode]
		if !ok {
			continue
		}
		var v money.Money
		if meta.Side == "debit" {
			v = row.PeriodDebit.Sub(row.PeriodCredit)
		} else {
			v = row.PeriodCredit.Sub(row.PeriodDebit)
		}
		if v.IsNegative() {
			// ★ 专栏净额为负时**不能取绝对值**。
			//
			// 红字冲销会造成负数，而负数在这里是有意义的：
			// 它表示这一栏本期被冲减。取绝对值会凭空造出税额 ——
			// 发布前审计实测：1 月销项 13,000，2 月只做一笔红字冲销，
			// 取绝对值后 2 月表显示「应补 14,560.00」（含附加），
			// 而正确的应纳税额是 0。
			//
			// 原样传下去，让该栏自然冲减（应纳税额会被夹到 0），
			// 同时报出来让办税人核对。
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"%s 专栏本期净额为负（%s）：本期有红字冲销或反向发生额。"+
					"本表按净额计算（已经自然冲减），请与申报系统核对。",
				row.AccountName, v))
		}
		switch meta.Field {
		case "Input":
			out.Input = v
		case "Output":
			out.Output = v
		case "Paid":
			out.Paid = v
		case "TaxRelief":
			out.TaxRelief = v
		case "ExportTaxRefund":
			out.ExportTaxRefund = v
		case "InputTransferredOut":
			out.InputTransferredOut = v
		case "ExportOffset":
			out.ExportOffset = v
		case "OutputDeducted":
			out.OutputDeducted = v
		}
	}
	out.Notes = append(out.Notes,
		fmt.Sprintf("销项税额 = 22210102 本期贷方发生额 = %s", out.Output),
		fmt.Sprintf("进项税额 = 22210101 本期借方发生额 = %s", out.Input),
		fmt.Sprintf("已交税金 = 22210103 本期借方发生额 = %s", out.Paid))

	// 上期期末留抵
	prior, note, err := r.priorVATCredit(ctx, k)
	if err != nil {
		return nil, err
	}
	out.PriorCredit = prior
	out.Notes = append(out.Notes, note)
	return out, nil
}

// priorVATCredit 取上期期末留抵税额。
//
// 做的是「222101 各专栏在上期期末的借方净额」：
//
//	借方净额 > 0  说明进项还没抵完 → 留抵
//	贷方净额       说明有应交未交的增值税（正常已转出到 222102）→ 不是留抵
func (r *TaxReturnRepo) priorVATCredit(ctx context.Context, k period.Key) (money.Money, string, error) {
	prev := k.Prev()
	if !prev.Valid() {
		return 0, "上期留抵：本期是账套第一个月，按 0 处理", nil
	}
	rep, err := r.db.Reports().TrialBalanceReport(ctx, prev)
	if err != nil {
		// 上一期不在账套里（比如建账就是本月）：留抵按 0 处理，
		// 而不是让整张表算不出来
		return 0, fmt.Sprintf("上期留抵：账套里没有 %s，按 0 处理", prev), nil
	}
	var net money.Money
	for _, row := range rep.Rows {
		if _, ok := vatColumnCodes[row.AccountCode]; !ok {
			continue
		}
		net = net.Add(row.ClosingDebit).Sub(row.ClosingCredit)
	}
	if !net.IsPositive() {
		return 0, fmt.Sprintf(
			"上期留抵：222101 各专栏在 %s 期末无借方净额（期末余额 %s），按 0 处理", prev, net), nil
	}
	return net, fmt.Sprintf(
		"上期留抵 = 222101 各专栏在 %s 的期末借方净额 = %s", prev, net), nil
}

// CITFigures 是企业所得税需要的取数。
type CITFigures struct {
	// Revenue / Cost / Profit 是**年初至本月**的累计数（累计口径）。
	//
	// ★ 企业所得税预缴是累计口径：按「本季度」算会系统性少缴，
	// 因为纳税调整与亏损弥补都是按年看的。
	Revenue money.Money
	Cost    money.Money
	Profit  money.Money
	// Prepaid 是本年已预缴的所得税（222104 本年借方发生额）。
	Prepaid money.Money
	Notes   []string
}

// CITFiguresOf 取某期企业所得税需要的数。
func (r *TaxReturnRepo) CITFiguresOf(ctx context.Context, k period.Key) (*CITFigures, error) {
	out := &CITFigures{}
	// 利润表：本年累计口径（第二个返回值就是年初至本月）
	_, ytd, _, err := r.db.Reports().BuildIncomeStatement(ctx, k)
	if err != nil {
		return nil, err
	}
	if l, ok := ytd.Line(1); ok {
		out.Revenue = l.Value
	} else {
		out.Notes = append(out.Notes, "利润表里没有「行1 营业收入」，营业收入按 0 处理")
	}
	if l, ok := ytd.Line(2); ok {
		out.Cost = l.Value
	} else {
		out.Notes = append(out.Notes, "利润表里没有「行2 营业成本」，营业成本按 0 处理")
	}
	if l, ok := ytd.Line(30); ok {
		out.Profit = l.Value
	} else {
		out.Notes = append(out.Notes, "利润表里没有「行30 利润总额」，利润总额按 0 处理")
	}
	out.Notes = append(out.Notes,
		fmt.Sprintf("营业收入（年初至今）= 利润表行1 = %s", out.Revenue),
		fmt.Sprintf("营业成本（年初至今）= 利润表行2 = %s", out.Cost),
		fmt.Sprintf("利润总额（年初至今）= 利润表行30 = %s", out.Profit))

	// 已预缴：222104 应交企业所得税 本年借方发生额
	yearStart, err := calendar.New(k.Year, 1, 1)
	if err != nil {
		return nil, err
	}
	end, err := calendar.New(k.Year, k.Month, calendar.DaysInMonth(k.Year, k.Month))
	if err != nil {
		return nil, err
	}
	var paid int64
	// ★ 只看**借方发生额**：借方是实际缴纳，贷方是计提。
	//
	// 发布前审计实测：3 月计提（借 5801/贷 222104 3,000）+ 4 月缴纳
	// （借 222104/贷 1002 3,000），用「借方净额」取数得到 0 ——
	// 已缴 3,000 被计提冲掉，本期应补虚增 3,000；
	// 只计提未缴时还会得到 −3,000（「已预缴」为负）。
	err = r.db.sql.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(le.debit), 0)
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		 WHERE a.code = '222104' AND le.biz_date >= ? AND le.biz_date <= ?`,
		yearStart.String(), end.String()).Scan(&paid)
	if err != nil {
		return nil, translateErr(err)
	}
	if paid < 0 {
		paid = 0
	}
	out.Prepaid = money.Money(paid)
	out.Notes = append(out.Notes, fmt.Sprintf(
		"本年已预缴所得税 = 222104 应交企业所得税 年初至 %s 的**借方发生额**（实际缴纳）= %s",
		end, out.Prepaid))
	return out, nil
}

// IITEmployeeYTD 是一位员工年初至某月的累计数。
type IITEmployeeYTD struct {
	EmployeeID int64
	Name       string
	Code       string
	// Months 是累计任职月数（含本月）。
	Months int
	// Income 是累计收入（计税依据口径）。
	Income money.Money
	// SpecialDeduction / SpecialAdditional / OtherDeduction 是累计扣除。
	SpecialDeduction  money.Money
	SpecialAdditional money.Money
	OtherDeduction    money.Money
	// TaxWithheld 是**截至上月**的累计已预扣预缴税额。
	//
	// ★ 不是「截至本月」：本期应预扣 = 累计应纳税额 − 截至上月的累计已预扣。
	//
	// 这里**不能用工资单里固化的 `cum_tax_withheld`**：那个字段按
	// payroll 的口径存的是「截至本月」（= 截至上月 + 本月预扣，见
	// payroll.YTDAfter 的赋值），拿它去减，本期应预扣永远是 0 ——
	// 这是发布前审计抓到的第一个阻断问题，实测：1 月工资单个税 450、
	// 个税表本期应预扣 0。
	//
	// 口径改成「当月之前的各月 iit 之和」，与 payroll 自己的 priorYTD
	// 取数方式一致（那里也是 SUM(si.iit) 且 month < 本月）。
	TaxWithheld money.Money
	// RunStatus 是这一行数据来自哪张工资单的状态。
	RunStatus string
}

// IITYTDOf 取某期个税扣缴需要的累计数。
//
// 每位员工取**本年度不超过本期**的最后一张工资单里的累计字段 ——
// 那些累计数是生成工资单时固化下来的（见 salary_item.cum_*），
// 直接读它们而不是现在重算：工资单上写着「本月扣了 300」，
// 税务表上算出「本月该扣 280」，两个数必须能对上，
// 不能各算各的。
func (r *TaxReturnRepo) IITYTDOf(ctx context.Context, k period.Key) ([]IITEmployeeYTD, int, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT e.id, e.name, COALESCE(e.code, ''), si.cum_months, si.cum_income,
		       si.cum_special_deduction, si.cum_special_additional,
		       si.cum_other_deduction,
		       -- ★ 截至**上月**的累计已预扣（不是 si.cum_tax_withheld，
		       -- 那个含本月，见 IITEmployeeYTD.TaxWithheld 的说明）
		       COALESCE((
		           SELECT SUM(i3.iit) FROM salary_item i3
		             JOIN salary_run s3 ON s3.id = i3.run_id
		            WHERE i3.employee_id = si.employee_id
		              AND s3.year = sr.year AND s3.month < sr.month), 0),
		       sr.month, sr.status
		  FROM salary_item si
		  JOIN salary_run sr ON sr.id = si.run_id
		  JOIN employee e   ON e.id = si.employee_id
		 WHERE sr.year = ? AND sr.month <= ?
		   AND sr.month = (
		       SELECT MAX(s2.month) FROM salary_run s2
		         JOIN salary_item i2 ON i2.run_id = s2.id
		        WHERE i2.employee_id = si.employee_id
		          AND s2.year = sr.year AND s2.month <= ?)
		 ORDER BY e.code, e.id`, k.Year, k.Month, k.Month)
	if err != nil {
		return nil, 0, translateErr(err)
	}
	defer rows.Close()

	var out []IITEmployeeYTD
	for rows.Next() {
		var e IITEmployeeYTD
		var income, special, additional, other, withheld int64
		var runMonth int
		if err := rows.Scan(&e.EmployeeID, &e.Name, &e.Code, &e.Months, &income,
			&special, &additional, &other, &withheld, &runMonth, &e.RunStatus); err != nil {
			return nil, 0, translateErr(err)
		}
		e.Income = money.Money(income)
		e.SpecialDeduction = money.Money(special)
		e.SpecialAdditional = money.Money(additional)
		e.OtherDeduction = money.Money(other)
		e.TaxWithheld = money.Money(withheld)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, translateErr(err)
	}

	// 有多少张工资单还是草稿：草稿没进账，算出来的个税是**预计数**
	var drafts int
	if err := r.db.sql.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM salary_run
		 WHERE year = ? AND month <= ? AND status = 'draft'`,
		k.Year, k.Month).Scan(&drafts); err != nil {
		return nil, 0, translateErr(err)
	}
	return out, drafts, nil
}
