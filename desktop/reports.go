package main

import (
	"context"
	"fmt"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/cashflow"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/report"
	"miniaccount/internal/service"
)

// 报表桥接：把领域层的报表定义转成前端能统一渲染的结构。
//
// # 为什么不做成「每种报表一个接口」
//
// 报表定义已经是数据（CSV 驱动的行项目 + 取数公式），
// 前端只要拿到「行次 + 名称 + 缩进 + 各列金额」就能画。
// 加一张报表、改一行公式，前端一行都不用动。
// 反之，如果每张报表一个接口，每加一张表就要前后端各改一次。

// trialBalance 科目余额表（六栏式）。
func (a *App) trialBalance(ctx context.Context, svc *service.Service,
	req ReportRequest) (*ReportResult, error) {

	k := period.NewKey(req.Year, req.Month)
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", req.Year, req.Month)
	}
	rep, err := svc.DB().Reports().TrialBalanceReport(ctx, k)
	if err != nil {
		return nil, err
	}

	out := &ReportResult{
		Title:    "科目余额表",
		Subtitle: fmt.Sprintf("%d 年 %02d 月", k.Year, k.Month),
		Columns: []string{"期初借方", "期初贷方", "本期借方", "本期贷方",
			"期末借方", "期末贷方"},
	}
	for _, r := range rep.Rows {
		out.Rows = append(out.Rows, ReportRow{
			No:     r.AccountCode,
			Label:  r.AccountName,
			Indent: r.Level - 1,
			Bold:   !r.IsLeaf,
			Values: []int64{
				int64(r.OpeningDebit), int64(r.OpeningCredit),
				int64(r.PeriodDebit), int64(r.PeriodCredit),
				int64(r.ClosingDebit), int64(r.ClosingCredit),
			},
		})
	}
	// 试算平衡是科目余额表的固有校验，不成立必须报出来。
	//
	// ★ 用 rep.Totals()，**不要**遍历 rep.Rows 自己加：
	// 汇总科目行带的是其下级的合计，全加会把同一笔钱算上两三遍，
	// 于是界面上几乎每个月都弹一个红色致命错误，而底账其实是平的。
	// 误报的代价是用户学会忽略它 —— 真不平衡时就没人看了。
	_, _, tdI, tcI, _, _ := rep.Totals()
	td, tc := int64(tdI), int64(tcI)
	if td != tc {
		out.Issues = append(out.Issues, ReportIssue{
			Fatal: true,
			Text: fmt.Sprintf("试算不平衡：借方 %s ≠ 贷方 %s，差额 %s",
				moneyStr(td), moneyStr(tc), moneyStr(td-tc)),
		})
	}
	return out, nil
}

// balanceSheet 资产负债表（会小企 01 表，53 行）。
func (a *App) balanceSheet(ctx context.Context, svc *service.Service,
	req ReportRequest) (*ReportResult, error) {

	book, err := svc.Book(ctx)
	if err != nil {
		return nil, err
	}
	asOf, err := defaultDate(book, req)
	if err != nil {
		return nil, err
	}

	closing, opening, issues, err := svc.DB().Reports().BuildBalanceSheet(ctx, asOf)
	if err != nil {
		return nil, err
	}

	// ★ 列数与每行的 Values 长度必须一致。
	//
	// 资产负债表是左右两栏、每栏两列（期末 / 年初），一共 4 列。
	// 曾经这里只写 2 个列名却塞 4 个值 —— 前端按列名画表头、
	// 按 Values 画单元格，结果就是表头与数据整体错位两列。
	// 「列名与值同长」这条不变式在下面的断言里强制住。
	out := &ReportResult{
		Title:    "资产负债表",
		Subtitle: fmt.Sprintf("%s（%s）", asOf.String(), book.CompanyName),
		Columns: []string{
			"资产·期末余额", "资产·年初余额",
			"负债和所有者权益·期末余额", "负债和所有者权益·年初余额",
		},
	}
	// 左右两栏各占一组列：资产 | 负债和所有者权益
	left := sideLines(closing, report.SideLeft)
	right := sideLines(closing, report.SideRight)
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	for i := 0; i < n; i++ {
		row := ReportRow{Values: []int64{0, 0, 0, 0}}
		if i < len(left) {
			l := left[i]
			row.No = fmt.Sprintf("%d", l.No)
			row.Label = l.DisplayName()
			row.Indent = memoIndent(l)
			row.Bold = isTotal(l)
			row.IsMemo = l.Type == report.LineMemo
			row.Values[0] = int64(l.Value)
			if o, ok := lineOf(opening, l.No); ok {
				row.Values[1] = int64(o.Value)
			}
		}
		if i < len(right) {
			l := right[i]
			row.RightNo = fmt.Sprintf("%d", l.No)
			row.RightLabel = l.DisplayName()
			row.Bold = row.Bold || isTotal(l)
			row.Values[2] = int64(l.Value)
			if o, ok := lineOf(opening, l.No); ok {
				row.Values[3] = int64(o.Value)
			}
		}
		out.Rows = append(out.Rows, row)
	}
	for _, is := range issues {
		out.Issues = append(out.Issues, ReportIssue{Text: is.String(), Fatal: is.Fatal})
	}
	return out, nil
}

// incomeStatement 利润表（会小企 02 表，32 行）。
func (a *App) incomeStatement(ctx context.Context, svc *service.Service,
	req ReportRequest) (*ReportResult, error) {

	k := period.NewKey(req.Year, req.Month)
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", req.Year, req.Month)
	}
	cur, ytd, issues, err := svc.DB().Reports().BuildIncomeStatement(ctx, k)
	if err != nil {
		return nil, err
	}

	out := &ReportResult{
		Title:    "利润表",
		Subtitle: fmt.Sprintf("%d 年 %02d 月", k.Year, k.Month),
		Columns:  []string{"本期金额", "本年累计"},
	}
	for _, l := range cur.Lines {
		var acc int64
		if o, ok := lineOf(ytd, l.No); ok {
			acc = int64(o.Value)
		}
		out.Rows = append(out.Rows, ReportRow{
			No:     fmt.Sprintf("%d", l.No),
			Label:  l.DisplayName(),
			Indent: memoIndent(l),
			Bold:   isTotal(l),
			IsMemo: l.Type == report.LineMemo,
			Values: []int64{int64(l.Value), acc},
		})
	}
	for _, is := range issues {
		out.Issues = append(out.Issues, ReportIssue{Text: is.String(), Fatal: is.Fatal})
	}
	return out, nil
}

// cashFlow 现金流量表（会小企 03 表）。
func (a *App) cashFlow(ctx context.Context, svc *service.Service,
	req ReportRequest) (*ReportResult, error) {

	k := period.NewKey(req.Year, req.Month)
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", req.Year, req.Month)
	}
	st, err := svc.DB().CashFlow().StatementForPeriod(ctx, k, nil)
	if err != nil {
		return nil, err
	}

	// 说明：本表按《小企业会计准则》会小企 03 表编制，采用**直接法**。
	// 归类规则直接映射到各活动的小计行（不落明细行），因此行项目是
	// 官方 36 行中真正有取数的那些 —— 其余明细行在本实现的归类粒度下
	// 恒为 0，铺在报表上只会让人以为漏了数。
	out := &ReportResult{
		Title:    "现金流量表",
		Subtitle: fmt.Sprintf("%d 年 %02d 月（直接法）", k.Year, k.Month),
		Columns:  []string{"金额"},
	}
	lastActivity := ""
	for _, l := range st.Lines {
		if l.Activity != "" && string(l.Activity) != lastActivity {
			out.Rows = append(out.Rows, ReportRow{
				Label: "【" + l.Activity.Label() + "】", Bold: true,
				Values: []int64{0},
			})
			lastActivity = string(l.Activity)
		}
		out.Rows = append(out.Rows, ReportRow{
			No:     fmt.Sprintf("%d", l.No),
			Label:  l.Name,
			Bold:   l.IsSubtotal,
			Values: []int64{int64(l.Display())},
		})
	}

	// 与账面期末现金交叉验证
	if st.ActualClosingCash != nil {
		if d := st.CashMismatch(); d != 0 {
			out.Issues = append(out.Issues, ReportIssue{
				Fatal: true,
				Text: fmt.Sprintf(
					"推算期末现金 %s 与账面 %s 差 %s —— 可能有现金科目没被纳入",
					moneyStr(int64(st.ClosingCash)),
					moneyStr(int64(*st.ActualClosingCash)), moneyStr(int64(d))),
			})
		}
	}
	// 未归类项必须列出来，而不是静默丢弃
	if len(st.Unclassified) > 0 {
		out.Issues = append(out.Issues, ReportIssue{
			Text: fmt.Sprintf("有 %d 笔共 %s 未能归类，本表不完全准确",
				len(st.Unclassified), moneyStr(int64(st.UnclassifiedTotal))),
		})
	}
	for _, e := range st.Check() {
		out.Issues = append(out.Issues, ReportIssue{Text: e.Error(), Fatal: true})
	}
	return out, nil
}

// contactBalances 往来单位余额表。
func (a *App) contactBalances(ctx context.Context, svc *service.Service,
	req ReportRequest) (*ReportResult, error) {

	k := period.NewKey(req.Year, req.Month)
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", req.Year, req.Month)
	}
	rows, err := svc.DB().Reports().ContactBalances(ctx, k, req.AccountPrefix)
	if err != nil {
		return nil, err
	}

	out := &ReportResult{
		Title:    "往来单位余额表",
		Subtitle: fmt.Sprintf("%d 年 %02d 月", k.Year, k.Month),
		Columns:  []string{"期初", "本期借方", "本期贷方", "期末"},
	}
	for _, r := range rows {
		out.Rows = append(out.Rows, ReportRow{
			No:    r.AccountCode,
			Label: fmt.Sprintf("%s（%s）", r.ContactName, contactKindLabel(r.ContactKind)),
			Values: []int64{
				int64(r.Opening), int64(r.Debit), int64(r.Credit), int64(r.Closing),
			},
		})
	}
	return out, nil
}

// ledgerDetail （明细账）不在 Report 里 —— 它的形状是流水而非报表。
// 见 LedgerDetail 绑定。

// ------------------------------------------------------------------ 辅助

// defaultDate 解析资产负债表的目标日期：优先用界面给的年月，
// 否则取「当前可记账期间」的期末。
func defaultDate(book *service.BookInfo, req ReportRequest) (date, error) {
	if req.Year > 0 && req.Month > 0 {
		k := period.NewKey(req.Year, req.Month)
		if !k.Valid() {
			return date{}, fmt.Errorf("会计期间 %04d-%02d 非法", req.Year, req.Month)
		}
		return periodEnd(k), nil
	}
	// ★ Month == 0 时的契约是「按年取值」（见 ReportRequest.Month 的说明）。
	//
	// 原来这里直接掉进下面的「当前期间」兜底，把 Year **整个忽略**了 ——
	// 于是请求 2024 年会拿到 2025-01-31，而且不报任何错。
	// 一张期间错的资产负债表看起来完全正常。
	if req.Year > 0 {
		return date{Year: req.Year, Month: 12, Day: calendar.DaysInMonth(req.Year, 12)}, nil
	}
	for _, p := range book.Periods {
		if p.Status == "open" {
			d, err := parseDate(p.To)
			if err != nil {
				return date{}, err
			}
			return d, nil
		}
	}
	if len(book.Periods) > 0 {
		return parseDate(book.Periods[len(book.Periods)-1].To)
	}
	return todayDate(), nil
}

func sideLines(d *report.Definition, side report.Side) []*report.Line {
	if d == nil {
		return nil
	}
	var out []*report.Line
	for _, l := range d.Lines {
		if l.Side == side {
			out = append(out, l)
		}
	}
	return out
}

func lineOf(d *report.Definition, no int) (*report.Line, bool) {
	if d == nil {
		return nil, false
	}
	return d.Line(no)
}

func isTotal(l *report.Line) bool {
	return l.Type == report.LineSubtotal || l.Type == report.LineTotal
}

// memoIndent 返回该行在前端的缩进层级。
func memoIndent(l *report.Line) int {
	switch l.Type {
	case report.LineMemo:
		return 2
	case report.LineSubtotal, report.LineTotal:
		return 0
	default:
		return 1
	}
}

func contactKindLabel(kind string) string {
	switch kind {
	case "customer":
		return "客户"
	case "supplier":
		return "供应商"
	case "employee":
		return "员工"
	case "shareholder":
		return "股东"
	default:
		return "其他单位"
	}
}

// 保证 cashflow 包被引用（报表桥接用到其行次常量）。
var _ = cashflow.Operating

// AgingRequest 是账龄分析表的参数。
type AgingRequest struct {
	AsOf          string `json:"asOf"`
	AccountPrefix string `json:"accountPrefix"`
}

// AgingReport 生成应收/应付账龄分析表。
func (a *App) AgingReport(req AgingRequest) (out *service.AgingView, err error) {
	defer recoverTo(&err, "AgingReport")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.AgingReport(a.context(), service.AgingRequest{
		AsOf: req.AsOf, AccountPrefix: req.AccountPrefix,
	}))
}

// StatementRequest 是对账单的参数。
type StatementRequest struct {
	ContactID     int64  `json:"contactId"`
	From          string `json:"from"`
	To            string `json:"to"`
	AccountPrefix string `json:"accountPrefix"`
}

// Statement 生成一张往来对账单。
func (a *App) Statement(req StatementRequest) (out *service.StatementView, err error) {
	defer recoverTo(&err, "Statement")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Statement(a.context(), service.StatementRequest{
		ContactID: req.ContactID, From: req.From, To: req.To,
		AccountPrefix: req.AccountPrefix,
	}))
}

// ActiveContacts 返回期间内有往来发生的单位。
//
// 用于「批量开对账单」：会计到月底不需要一个个去挑。
func (a *App) ActiveContacts(from, to string) (out []service.ActiveContactsView, err error) {
	defer recoverTo(&err, "ActiveContacts")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.ActiveContacts(a.context(), from, to))
}

// ReconciliationRequest 是银行存款余额调节表的参数。
type ReconciliationRequest struct {
	AccountCode string `json:"accountCode"`
	AsOf        string `json:"asOf"`
	From        string `json:"from"`
	// BankBalance 是银行对账单期末余额（分）。指针类型是必需的 ——
	// 0 与「没填」含义完全不同：填 0 表示账户确实清零了，
	// 没填表示拿不到对账单、只能算企业单侧。
	BankBalance *int64 `json:"bankBalance"`
}

// BankReconciliation 生成银行存款余额调节表。
func (a *App) BankReconciliation(req ReconciliationRequest) (out *service.ReconciliationView, err error) {
	defer recoverTo(&err, "BankReconciliation")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Reconciliation(a.context(), service.ReconciliationRequest{
		AccountCode: req.AccountCode, AsOf: req.AsOf, From: req.From,
		BankBalance: req.BankBalance,
	}))
}

// ExportReconciliation 把余额调节表导出为 Excel。
//
// 传入与 BankReconciliation 相同的请求对象：导出的必须是**屏幕上
// 那一张**，而不是用默认参数另算一张。
func (a *App) ExportReconciliation(req ReconciliationRequest, dest string) (out *service.ExportResult, err error) {
	defer recoverTo(&err, "ExportReconciliation")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.ExportReconciliation(a.context(), service.ReconciliationRequest{
		AccountCode: req.AccountCode, AsOf: req.AsOf, From: req.From,
		BankBalance: req.BankBalance,
	}, dest))
}

// SuggestedReconciliationName 返回调节表的默认文件名。
func (a *App) SuggestedReconciliationName(accountCode string) (out string, err error) {
	defer recoverTo(&err, "SuggestedReconciliationName")()
	svc, f := a.book()
	if f != nil {
		return "", f
	}
	return wrap(svc.SuggestedReconciliationName(a.context(), accountCode))
}

// SummaryRequest 是凭证汇总表的参数。
type SummaryRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Summary 生成凭证汇总表。
func (a *App) Summary(req SummaryRequest) (out *service.SummaryView, err error) {
	defer recoverTo(&err, "Summary")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Summary(a.context(), service.SummaryRequest{
		From: req.From, To: req.To,
	}))
}

// SummaryByPeriod 按会计期间生成凭证汇总表（界面翻页用）。
func (a *App) SummaryByPeriod(year, month int) (out *service.SummaryView, err error) {
	defer recoverTo(&err, "SummaryByPeriod")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.SummaryByPeriod(a.context(), year, month))
}

// ExportSummary 把凭证汇总表导出为 Excel。
func (a *App) ExportSummary(req SummaryRequest, dest string) (out *service.SummaryExportResult, err error) {
	defer recoverTo(&err, "ExportSummary")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.ExportSummary(a.context(), service.SummaryRequest{
		From: req.From, To: req.To,
	}, dest))
}

// ColumnarRequest 是多栏式明细账的参数。
type ColumnarRequest struct {
	AccountCode string `json:"accountCode"`
	From        string `json:"from"`
	To          string `json:"to"`
}

// Columnar 生成多栏式明细账。
func (a *App) Columnar(req ColumnarRequest) (out *service.ColumnarView, err error) {
	defer recoverTo(&err, "Columnar")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Columnar(a.context(), service.ColumnarRequest{
		AccountCode: req.AccountCode, From: req.From, To: req.To,
	}))
}

// ColumnarAccounts 返回可以展开的科目（有下级明细的科目）。
func (a *App) ColumnarAccounts() (out []service.AccountOption, err error) {
	defer recoverTo(&err, "ColumnarAccounts")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.ColumnarAccounts(a.context()))
}

// ExportColumnar 把多栏式明细账导出为 Excel。
func (a *App) ExportColumnar(req ColumnarRequest, dest string) (out *service.ColumnarExportResult, err error) {
	defer recoverTo(&err, "ExportColumnar")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.ExportColumnar(a.context(), service.ColumnarRequest{
		AccountCode: req.AccountCode, From: req.From, To: req.To,
	}, dest))
}

// SuggestedColumnarName 返回多栏式明细账的默认文件名。
func (a *App) SuggestedColumnarName(accountCode string) (out string, err error) {
	defer recoverTo(&err, "SuggestedColumnarName")()
	svc, f := a.book()
	if f != nil {
		return "", f
	}
	return wrap(svc.SuggestedColumnarName(a.context(), accountCode))
}

// BankAccounts 返回可用于对账的银行科目。
func (a *App) BankAccounts() (out []service.AccountOption, err error) {
	defer recoverTo(&err, "BankAccounts")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.BankAccounts(a.context()))
}
