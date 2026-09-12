package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/cashflow"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/report"
	"miniaccount/internal/export"
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 日期与区间
// ---------------------------------------------------------------------------

// resolveDate 解析 --date；为空时取账套最后一个期间的截止日。
//
// 默认值刻意取「最后一天」而不是「今天」：账套里的业务通常不在今天，
// 而资产负债表是**时点**报表，取今天往往得到一张空表。
func resolveDate(ctx context.Context, svc *service.Service, s string) (calendar.Date, error) {
	if s != "" {
		d, err := calendar.Parse(s)
		if err != nil {
			return calendar.Date{}, fmt.Errorf("--date %q 格式不对，应为 YYYY-MM-DD", s)
		}
		return d, nil
	}
	book, err := svc.Book(ctx)
	if err != nil {
		return calendar.Date{}, err
	}
	if len(book.Periods) == 0 {
		return calendar.Today(), nil
	}
	// 优先取「当前可记账期间」的期末。
	// 账套会预生成到年底的期间，直接取最后一个会得到 12-31，
	// 而用户此刻关心的是手上这个月。
	for _, p := range book.Periods {
		if p.Status == string(period.StatusOpen) {
			return calendar.Parse(p.To)
		}
	}
	last := book.Periods[len(book.Periods)-1]
	return calendar.Parse(last.To)
}

// resolveRange 解析 --from/--to；为空时取账套全部期间。
func resolveRange(ctx context.Context, svc *service.Service, from, to string) (
	calendar.Date, calendar.Date, error) {

	book, err := svc.Book(ctx)
	if err != nil {
		return calendar.Date{}, calendar.Date{}, err
	}
	var defFrom, defTo calendar.Date
	if len(book.Periods) > 0 {
		defFrom, _ = calendar.Parse(book.Periods[0].From)
		defTo, _ = calendar.Parse(book.Periods[len(book.Periods)-1].To)
	}
	dFrom, dTo := defFrom, defTo
	if from != "" {
		if dFrom, err = calendar.Parse(from); err != nil {
			return dFrom, dTo, fmt.Errorf("--from %q 格式不对", from)
		}
	}
	if to != "" {
		if dTo, err = calendar.Parse(to); err != nil {
			return dFrom, dTo, fmt.Errorf("--to %q 格式不对", to)
		}
	}
	if dTo.Before(dFrom) {
		return dFrom, dTo, errors.New("--to 不能早于 --from")
	}
	return dFrom, dTo, nil
}

// ---------------------------------------------------------------------------
// 报表打印
// ---------------------------------------------------------------------------

// printStatement 打印一张单列表（或左右两栏）报表。
func printStatement(d *report.Definition, title, subtitle string, showZero bool) {
	if d == nil {
		fmt.Printf("（%s 没有数据）\n", title)
		return
	}
	fmt.Printf("%s  %s\n\n", title, subtitle)

	// 资产负债表是左右两栏，利润表是单栏
	left := linesOfSide(d, report.SideLeft)
	right := linesOfSide(d, report.SideRight)
	if len(left) > 0 && len(right) > 0 {
		printTwoColumn(left, right, showZero)
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "行次\t项目\t金额")
	for _, l := range d.Lines {
		if !showZero && l.Value.IsZero() {
			continue
		}
		fmt.Fprintf(w, "%d\t%s\t%s\n", l.No, l.DisplayName(), yuan(l.Value))
	}
	_ = w.Flush()
}

// printTwoColumn 按「资产 | 负债和所有者权益」并列打印资产负债表。
func printTwoColumn(left, right []*report.Line, showZero bool) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "行次\t资产\t金额\t行次\t负债和所有者权益\t金额")
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	for i := 0; i < n; i++ {
		var lc, rc []string
		if i < len(left) {
			if showZero || !left[i].Value.IsZero() || isTotalLine(left[i]) {
				lc = []string{
					fmt.Sprintf("%d", left[i].No), left[i].Name, yuan(left[i].Value),
				}
			}
		}
		if i < len(right) {
			if showZero || !right[i].Value.IsZero() || isTotalLine(right[i]) {
				rc = []string{
					fmt.Sprintf("%d", right[i].No), right[i].Name, yuan(right[i].Value),
				}
			}
		}
		for len(lc) < 3 {
			lc = append(lc, "")
		}
		for len(rc) < 3 {
			rc = append(rc, "")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			lc[0], lc[1], lc[2], rc[0], rc[1], rc[2])
	}
	_ = w.Flush()
}

func isTotalLine(l *report.Line) bool {
	return l.Type == report.LineTotal || l.Type == report.LineSubtotal
}

func linesOfSide(d *report.Definition, side report.Side) []*report.Line {
	var out []*report.Line
	for _, l := range d.Lines {
		if l.Side == side {
			out = append(out, l)
		}
	}
	return out
}

// defLines 把报表定义转成「行次 → 金额」映射，供双列打印使用。
func defLines(d *report.Definition) map[int]money.Money {
	out := map[int]money.Money{}
	if d == nil {
		return out
	}
	for _, l := range d.Lines {
		out[l.No] = l.Value
	}
	return out
}

// printStatement2 打印两列对比的利润表（本期 / 本年累计）。
func printStatement2(cur, acc map[int]money.Money, title, subtitle, c1, c2 string) {
	d, err := report.LoadIncomeStatement()
	if err != nil {
		fmt.Printf("（加载利润表定义失败：%v）\n", err)
		return
	}
	fmt.Printf("%s  %s\n\n", title, subtitle)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "行次\t项目\t%s\t%s\n", c1, c2)
	for _, l := range d.Lines {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
			l.No, l.DisplayName(), yuan(cur[l.No]), yuan(acc[l.No]))
	}
	_ = w.Flush()
}

// reportIssues 打印报表的勾稽问题。
//
// 勾稽不成立必须显式报出来：一张数字齐全但内部对不上的报表
// 比一张明显缺数的报表危险得多 —— 前者会被直接拿去用。
func reportIssues(issues []report.CheckIssue) error {
	if len(issues) == 0 {
		return nil
	}
	var fatal bool
	fmt.Println()
	for _, is := range issues {
		mark := "!"
		if is.Fatal {
			mark = "✗"
			fatal = true
		}
		fmt.Printf("%s 勾稽不成立：%s\n", mark, is.String())
	}
	if fatal {
		return errors.New("报表存在致命勾稽问题，请先核对账务再使用")
	}
	return nil
}

// ---------------------------------------------------------------------------
// 现金流量表导出
// ---------------------------------------------------------------------------

// exportCashFlow 把现金流量表写成 Excel。
//
// 复用 export.WriteStatement：现金流量表与其它报表一样是
// 「项目 + 金额」的行列表，没必要为它单写一套导出代码。
func exportCashFlow(path, companyName string, k period.Key,
	st *cashflow.Statement) error {

	rows := make([]export.StatementRow, 0, len(st.Lines)+3)
	lastActivity := ""
	for _, l := range st.Lines {
		if l.Activity != "" && string(l.Activity) != lastActivity {
			rows = append(rows, export.StatementRow{
				Label: "【" + l.Activity.Label() + "】", Bold: true,
			})
			lastActivity = string(l.Activity)
		}
		rows = append(rows, export.StatementRow{
			Label:  fmt.Sprintf("%d %s", l.No, l.Name),
			Values: []money.Money{l.Display()},
			Bold:   l.IsSubtotal,
		})
	}
	rows = append(rows,
		export.StatementRow{Label: "", Values: []money.Money{}},
		export.StatementRow{Label: "期初现金余额", Values: []money.Money{st.OpeningCash}},
		export.StatementRow{Label: "期末现金余额", Values: []money.Money{st.ClosingCash}, Bold: true},
	)

	return export.WriteStatement(path, export.StatementOptions{
		SheetName:   "现金流量表",
		Title:       "现金流量表",
		CompanyName: companyName,
		DateLabel:   fmt.Sprintf("%d 年 %d 月", k.Year, k.Month),
		Columns:     []string{"金额"},
		Rows:        rows,
	})
}

// ---------------------------------------------------------------------------
// 演示账套
// ---------------------------------------------------------------------------

// demoResult 是演示账套的生成结果。
type demoResult struct {
	company string
	periods []period.Key
	posted  int
}

// seedDemo 往一个刚建好的账套里写入一段真实感的小微企业业务。
//
// # 为什么需要它
//
// 空账套看不出任何问题：报表全零、勾稽恒成立、现金流量表一片空白。
// 只有放进真实形状的数据，才能验证「建账 → 记账 → 报表 → 结账 → 备份」
// 这条链路真的通，也才能让人一眼看懂这套软件能做什么。
//
// 业务设计覆盖了每个模块的边界：
//
//	股东投入     → 筹资活动、实收资本、往来辅助核算
//	含税销售     → 收入 + 销项税（两条分录行）、应收辅助核算
//	收回货款     → 经营活动流入、银行科目
//	采购未付款   → 应付账款 + 供应商辅助核算
//	发工资       → 应付职工薪酬
//	缴纳增值税   → 应交税费（★ 与销项税方向相反，用来验证归类）
//	计提折旧     → 累计折旧（资产的备抵科目）
//	购置设备     → 投资活动流出
//	支付房租水电 → 管理费用 + 部门辅助核算
//	计提结转     → 期末结转损益
func money100(yuan int64) money.Money { return money.Money(yuan) * money.Yuan }

// demoYear 是演示账套使用的年度。
const demoYear = 2025

// ---------------------------------------------------------------------------
// demo 命令
// ---------------------------------------------------------------------------

func cmdDemo(ctx context.Context, args []string) error {
	fs := newFlagSet("demo")
	toMonth := fs.Int("months", 3, "生成到第几个月（1—12）")
	force := fs.Bool("force", false, "账套已存在时覆盖")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *toMonth < 1 || *toMonth > 12 {
		return fmt.Errorf("--months 应在 1—12 之间，实际 %d", *toMonth)
	}
	dbPath := fs.Lookup("db").Value.String()

	if *force {
		if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除旧账套失败：%w", err)
		}
	}

	svc, err := service.Open(ctx, service.Options{Path: dbPath})
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	if _, err := svc.Book(ctx); err == nil {
		return fmt.Errorf("%s 已有账套，如需重建请加 --force", dbPath)
	} else if !errors.Is(err, service.ErrNoBook) {
		return err
	}

	if _, err := svc.CreateBook(ctx, service.CreateBookInput{
		CompanyName: "杭州云帆软件有限公司",
		CreditCode:  "91330100MA2EXAMPLE",
		LegalPerson: "张三",
		// 演示账套按一般纳税人 + 小型企业建，与界面上的
		// 「生成演示账套」按钮保持一致（两边共用同一份生成逻辑）。
		VATStatus:       "general",
		EnterpriseScale: "small",
		StartYear:       demoYear, StartMonth: 1,
		ThroughYear: demoYear,
		CurrentYear: demoYear, CurrentMonth: *toMonth,
	}); err != nil {
		return err
	}

	fmt.Printf("正在生成演示账套（%d 年 1 月 ~ %d 月）…\n\n", demoYear, *toMonth)
	res, err := svc.SeedDemo(ctx, demoYear, 1, *toMonth)
	if err != nil {
		return err
	}
	fmt.Printf("✓ 已写入 %d 张已过账凭证\n", res.Posted)
	for _, w := range res.Warnings {
		fmt.Printf("  ! %s\n", w)
	}

	// 把之前各月结账，让「当前可记账期间」落在最后一个月。
	//
	// 这不只是为了演示好看：会计本来就该按期结账，
	// 跳过 1、2 月直接看 3 月的首页概览，看到的是 1 月的数字 ——
	// 而「当前期间永远是最早的未结账期间」正是本软件刻意坚持的规则。
	if *toMonth > 1 {
		closed := 0
		for m := 1; m < *toMonth; m++ {
			if _, err := svc.Close(ctx, period.NewKey(demoYear, m), "王主管"); err != nil {
				fmt.Printf("  ! %d 月结账失败：%v\n", m, err)
				continue
			}
			closed++
		}
		fmt.Printf("✓ 已结账 %d 个月（1 ~ %d 月）\n", closed, *toMonth-1)
	}

	// 直接跑一遍体检与报表，证明链路是通的
	fmt.Printf("\n--- 结账前体检 ---\n")
	h, err := svc.CheckHealth(ctx, period.NewKey(demoYear, *toMonth))
	if err != nil {
		return err
	}
	for _, it := range h.Items {
		fmt.Printf("  %s %s", levelMark(it.Level), it.Title)
		if it.Detail != "" {
			fmt.Printf("：%s", it.Detail)
		}
		fmt.Println()
	}

	fmt.Printf("\n--- 首页概览 ---\n")
	d, err := svc.Overview(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("  资产总计：%s\n", yuan(d.Assets))
	fmt.Printf("  负债合计：%s\n", yuan(d.Liabilities))
	fmt.Printf("  所有者权益：%s\n", yuan(d.Equity))
	fmt.Printf("  本期收入：%s\n", yuan(d.PeriodIncome))
	fmt.Printf("  本期费用：%s\n", yuan(d.PeriodExpense))
	fmt.Printf("  本期利润：%s\n", yuan(d.PeriodProfit))
	fmt.Printf("  期末现金：%s\n", yuan(d.ClosingCash))

	fmt.Printf("\n账套文件：%s\n", dbPath)
	fmt.Printf("接下来可以试试：\n")
	fmt.Printf("  miniaccount bs       --db %s\n", dbPath)
	fmt.Printf("  miniaccount pl       --db %s --period %d-%02d\n", dbPath, demoYear, *toMonth)
	fmt.Printf("  miniaccount cashflow --db %s --period %d-%02d\n", dbPath, demoYear, *toMonth)
	fmt.Printf("  miniaccount close    --db %s --period %d-%02d --preview\n",
		dbPath, demoYear, *toMonth)
	fmt.Printf("  miniaccount backup   --db %s --out demo.mabak\n", dbPath)
	return nil
}
