package sqlite

import (
	"context"
	"fmt"
	"sort"

	"miniaccount/internal/ai/aiprovider"
	"miniaccount/internal/domain/cashflow"
	"miniaccount/internal/domain/closing"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/report"
	"miniaccount/internal/domain/workpaper"
)

// ---------------------------------------------------------------------------
// 会计 agent 的账务查询
// ---------------------------------------------------------------------------
//
// 这一层只做一件事：把**已有的**报表 / 体检 / 工资试算结果，
// 裁成模型看得懂的小份。
//
// ★ 一行报表逻辑都不在这里重写。
//
// 报表是这个工程里最容易写错的地方（行次、勾稽、备抵科目取正、
// 未结转损益的处理……），而它已经有整套测试盯着。
// 给 AI 另写一套「简化版取数」，等于凭空多出一套会与真报表对不上的数字 ——
// 而用户拿 AI 说的数字去对报表、对不上时，他会怀疑报表。
// 所以这里只搬运、不计算。

// Report 返回一张报表。
func (r *AIRepo) Report(ctx context.Context, kind string, k aiprovider.PeriodKey) (
	*aiprovider.ReportData, error) {

	key := period.NewKey(k.Year, k.Month)
	switch kind {
	case "trial":
		rep, err := r.db.Reports().TrialBalanceReport(ctx, key)
		if err != nil {
			return nil, translateErr(err)
		}
		return trialToData(rep), nil

	case "bs":
		closingDef, _, issues, err := r.db.Reports().BuildBalanceSheet(ctx, report.Range(key).To)
		if err != nil {
			return nil, translateErr(err)
		}
		return definitionToData("资产负债表", k.String(), closingDef, issues), nil

	case "pl":
		cur, _, issues, err := r.db.Reports().BuildIncomeStatement(ctx, key)
		if err != nil {
			return nil, translateErr(err)
		}
		return definitionToData("利润表（本月数）", k.String(), cur, issues), nil

	case "cashflow":
		st, err := r.db.CashFlow().StatementForPeriod(ctx, key, nil)
		if err != nil {
			return nil, translateErr(err)
		}
		return cashflowToData(k, st), nil
	}
	return nil, fmt.Errorf("不认识的报表种类 %q", kind)
}

func trialToData(rep *report.BalanceReport) *aiprovider.ReportData {
	out := &aiprovider.ReportData{
		Title:  "科目余额表",
		Period: rep.Period.String(),
	}
	var td, tc money.Money
	for _, row := range rep.Rows {
		// 只给明细科目：汇总科目行是下级之和，一起给模型，
		// 它会把两边都加起来 —— 那正是「试算不平衡」最常见的来源
		if !row.IsLeaf {
			continue
		}
		// 整行都是零的跳过，否则 190 个科目全塞进去，大半没数
		if row.OpeningDebit.IsZero() && row.OpeningCredit.IsZero() &&
			row.PeriodDebit.IsZero() && row.PeriodCredit.IsZero() &&
			row.ClosingDebit.IsZero() && row.ClosingCredit.IsZero() {
			continue
		}
		bal, dir := row.ClosingDebit, "借"
		if bal.IsZero() {
			bal, dir = row.ClosingCredit, "贷"
		}
		out.Lines = append(out.Lines, aiprovider.ReportLine{
			Code: row.AccountCode, Name: row.AccountName,
			Debit: row.PeriodDebit, Credit: row.PeriodCredit,
			Balance: bal, Dir: dir,
		})
		td = td.Add(row.PeriodDebit)
		tc = tc.Add(row.PeriodCredit)
	}
	out.TotalDebit, out.TotalCredit = td, tc
	return out
}

func definitionToData(title, periodStr string,
	def *report.Definition, issues []report.CheckIssue) *aiprovider.ReportData {

	out := &aiprovider.ReportData{Title: title, Period: periodStr}
	if def == nil {
		return out
	}
	for _, l := range def.Lines {
		// 没数的行跳过：报表定义有 53 行，多数是空的
		if l.Value.IsZero() {
			continue
		}
		out.Lines = append(out.Lines, aiprovider.ReportLine{
			Code: fmt.Sprintf("行%d", l.No), Name: l.DisplayName(),
			Balance: l.Value, Dir: sideLabel(l.Side),
		})
	}
	for _, is := range issues {
		msg := fmt.Sprintf("%s %s ≠ %s %s，差 %s",
			is.Left, is.LeftVal, is.Right, is.RightVal, is.Diff)
		if is.Fatal {
			msg = "★ " + msg
		}
		out.Issues = append(out.Issues, msg)
	}
	return out
}

func sideLabel(s report.Side) string {
	switch s {
	case report.SideLeft:
		return "资产"
	case report.SideRight:
		return "负债和所有者权益"
	default:
		return ""
	}
}

func cashflowToData(k aiprovider.PeriodKey, st *cashflow.Statement) *aiprovider.ReportData {
	out := &aiprovider.ReportData{
		Title:  "现金流量表",
		Period: k.String(),
	}
	if st == nil {
		return out
	}
	for _, l := range st.Lines {
		if l.Amount.IsZero() {
			continue
		}
		out.Lines = append(out.Lines, aiprovider.ReportLine{
			Code: fmt.Sprintf("行%d", l.No), Name: l.Name, Balance: l.Amount,
		})
	}
	out.TotalDebit = st.OpeningCash
	out.TotalCredit = st.ClosingCash
	// ★ 未归类的那部分必须说出来。
	//
	// 它不是「小问题」：报表加总对不上时，会计要知道是哪几笔没归好，
	// 而静默丢掉会让模型给出一个看起来很完整的回答，实际少了钱。
	if st.UnclassifiedTotal.IsPositive() {
		out.Issues = append(out.Issues, fmt.Sprintf(
			"有 %s 的现金收支没能归类到表内任何一行（未归类合计），"+
				"表内行项目加起来会与期末现金对不上", st.UnclassifiedTotal))
	}
	return out
}

// Ledger 返回某个科目某个月的明细账。
func (r *AIRepo) Ledger(ctx context.Context, accountCode string,
	year, month int) (*aiprovider.ReportData, error) {

	k := period.NewKey(year, month)
	rng := report.Range(k)
	rows, err := r.db.Vouchers().Detail(ctx, accountCode, rng.From, rng.To)
	if err != nil {
		return nil, translateErr(err)
	}
	out := &aiprovider.ReportData{
		Title:  "明细账 " + accountCode,
		Period: k.String(),
	}
	for _, row := range rows {
		name := row.Summary
		if row.VoucherNo != "" {
			name = row.Summary + "（" + row.VoucherNo + "）"
		}
		if len(row.ContraAccounts) > 0 {
			name += " 对方：" + joinNames(row.ContraAccounts)
		}
		out.Lines = append(out.Lines, aiprovider.ReportLine{
			Code: row.BizDate.String(), Name: name,
			Debit: row.Debit, Credit: row.Credit,
		})
	}
	return out, nil
}

func joinNames(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += "、"
		}
		out += x
	}
	return out
}

// CheckPeriod 做结账前体检 + 结转预览。
func (r *AIRepo) CheckPeriod(ctx context.Context, k aiprovider.PeriodKey) (
	*aiprovider.PeriodCheck, error) {

	key := period.NewKey(k.Year, k.Month)
	out := &aiprovider.PeriodCheck{Period: key.String()}

	h, err := r.db.CheckPeriodHealth(ctx, key)
	if err != nil {
		return nil, translateErr(err)
	}
	out.CanClose = h.CanClose()
	for _, it := range h.Items {
		if it.Key == "draft_vouchers" {
			out.Drafts = it.Count
		}
		out.Items = append(out.Items, aiprovider.CheckItem{
			Title: it.Title, Level: string(it.Level),
			Detail: it.Detail, Count: it.Count,
		})
	}

	plan, err := r.db.Closing().Plan(ctx, key, closing.Accounts{})
	if err != nil {
		// 结转预览取不到不算致命：体检结果已经拿到了，
		// 硬报错会让模型连「这个月平不平」都答不出来
		return out, nil
	}
	if plan.Closing != nil {
		out.Income = plan.Closing.TotalIncome
		out.Expense = plan.Closing.TotalExpense
		out.Profit = plan.Closing.Profit
	}
	for _, e := range plan.ClosingEntries {
		amt, side := e.Debit, "借"
		if amt.IsZero() {
			amt, side = e.Credit, "贷"
		}
		out.ClosingEntries = append(out.ClosingEntries,
			fmt.Sprintf("%s %s %s %s", side, e.AccountCode, e.Summary, amt))
	}
	return out, nil
}

// PreviewPayroll 试算某期间的工资与五险一金（**不写库**）。
func (r *AIRepo) PreviewPayroll(ctx context.Context, k aiprovider.PeriodKey) (
	*aiprovider.PayrollPreview, error) {

	key := period.NewKey(k.Year, k.Month)
	out := &aiprovider.PayrollPreview{Period: key.String()}

	schemes, err := r.db.Payroll().InsuranceSchemes(ctx)
	if err != nil {
		return nil, translateErr(err)
	}
	out.SchemeCount = len(schemes)
	for name := range schemes {
		out.Schemes = append(out.Schemes, name)
	}
	sort.Strings(out.Schemes)
	if len(schemes) == 0 {
		out.Note = "账套里还没有配社保方案 —— 没有方案，社保算不出来" +
			"（留空会被静默当成按 0 缴纳，工资单看着正常、实际没扣社保）。" +
			"需要用户提供**当地当年**的缴费比例与基数上下限，不要自己编。"
	}

	// BuildRun 只算不存：它返回一张内存里的工资单。
	run, err := r.db.Payroll().BuildRun(ctx, BuildRunInput{
		Period: key, CreatedBy: "AI 试算",
	})
	if err != nil {
		return nil, translateErr(err)
	}
	t := run.Totals()
	out.TotalGross = t.Gross
	out.TotalInsuranceSelf = t.InsuranceSelf
	out.TotalInsuranceCo = t.InsuranceCompany
	out.TotalIIT = t.IIT
	out.TotalNet = t.Net

	for _, it := range run.Items {
		p := aiprovider.PayrollPerson{
			Name: it.Employee.Name,
			// 应发 = 各项收入 − 考勤与其他扣款。
			// 用 TaxableGross 的口径而不是 Gross()：界面上的「应发合计」
			// 也是扣完考勤的，两个数不一致会让用户以为 AI 算错了
			Gross:            it.TaxableGross(),
			InsuranceBase:    it.Insurance.Base,
			PensionSelf:      it.Insurance.PensionSelf,
			MedicalSelf:      it.Insurance.MedicalSelf,
			UnemploymentSelf: it.Insurance.UnemploymentSelf,
			HousingFundSelf:  it.Insurance.HousingFundSelf,
			PensionCo:        it.Insurance.PensionCo,
			MedicalCo:        it.Insurance.MedicalCo,
			UnemploymentCo:   it.Insurance.UnemploymentCo,
			InjuryCo:         it.Insurance.InjuryCo,
			MaternityCo:      it.Insurance.MaternityCo,
			HousingFundCo:    it.Insurance.HousingFundCo,
			IIT:              it.IIT,
			Net:              it.NetPay,
			SchemeName:       it.Employee.SchemeName,
			Warning:          it.TaxWarning,
		}
		out.People = append(out.People, p)
	}
	return out, nil
}

// Workpaper 读某期审计底稿的紧凑形态（供 AI 会计查证）。
//
// ★ 只读，而且**不重算**任何东西：
// 重要性水平、未更正错报合计、结论，全部复用底稿仓储与领域函数。
// 在这里重写一遍口径，AI 说的数就会与底稿页上的数不一样 ——
// 而用户会相信哪一个？两边都信，然后发现对不上。
func (r *AIRepo) Workpaper(ctx context.Context, k aiprovider.PeriodKey) (
	*aiprovider.WorkpaperBrief, error) {

	key := period.NewKey(k.Year, k.Month)
	wp := r.db.Workpapers()

	ws, err := wp.Worksheet(ctx, key)
	if err != nil {
		return nil, translateErr(err)
	}
	adjs, err := wp.Adjustments(ctx, key)
	if err != nil {
		return nil, translateErr(err)
	}

	out := &aiprovider.WorkpaperBrief{Period: key.String(), Adjustments: []aiprovider.AdjustmentBrief{}}
	if ws.Materiality != nil {
		m := ws.Materiality
		out.Materiality = &aiprovider.MaterialityBrief{
			Benchmark: m.Benchmark.Label(), BenchmarkAmount: m.BenchmarkAmount,
			Overall: m.Overall(), Performance: m.Performance(), Trivial: m.Trivial(),
			Note: m.Note,
		}
	}
	for _, a := range adjs {
		out.Adjustments = append(out.Adjustments, aiprovider.AdjustmentBrief{
			Code: a.Code, Kind: a.Kind.Label(), Summary: a.Summary,
			Reason: a.Reason, Evidence: a.Evidence, Amount: a.Amount(),
			Booked: a.Booked, Posted: a.Posted,
		})
	}
	// 未更正错报用与底稿页**同一个**领域函数算
	sum := workpaper.Misstatements(adjs, ws.Materiality)
	out.MisstatementTotal = sum.Total
	out.MisstatementCount = len(sum.Items)
	out.ReclassCount = sum.ReclassCount
	out.Concludes = sum.Concludes()
	return out, nil
}
