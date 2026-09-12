package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/service"
)

// cmdReconciliation 打印银行存款余额调节表。
//
// 这是会计月底最常做的一张底稿：把「企业账上的钱」和「银行账上的钱」
// 各自调到同一个数，剩下的差额逐笔找出原因。
func cmdReconciliation(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("reconciliation", flag.ExitOnError)
	db := fs.String("db", "miniaccount.db", "账套数据库文件路径")
	account := fs.String("account", "1002", "银行科目编码")
	asOf := fs.String("asof", "", "截止日期（默认当前账期期末）")
	from := fs.String("from", "", "对账起始日（默认对账单覆盖的第一天）")
	bank := fs.String("bank", "", "银行对账单期末余额（元）；留空自动取流水里最后一条余额")
	list := fs.Bool("list", false, "列出可用于对账的银行科目")
	exportTo := fs.String("export", "", "导出为 Excel 文件（.xlsx）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()
	_ = db

	if *list {
		accounts, err := svc.BankAccounts(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "编码\t名称")
		for _, a := range accounts {
			fmt.Fprintf(w, "%s\t%s\n", a.Code, a.Name)
		}
		_ = w.Flush()
		return nil
	}

	var bankCents *int64
	if *bank != "" {
		// 用 money.Parse 而不是 fmt.Sscanf：Sscanf 会把 0x10、1_000
		// 这类字面量当合法金额，会计输入「1,000」时反而报错。
		m, err := money.Parse(*bank)
		if err != nil {
			return fmt.Errorf("银行对账单余额 %q 无法识别：%w", *bank, err)
		}
		v := int64(m)
		bankCents = &v
	}

	req := service.ReconciliationRequest{
		AccountCode: *account, AsOf: *asOf, From: *from, BankBalance: bankCents,
	}

	if *exportTo != "" {
		res, err := svc.ExportReconciliation(ctx, req, *exportTo)
		if err != nil {
			return err
		}
		fmt.Printf("✓ 已导出「%s」（%d 行）\n  %s\n", res.Title, res.Rows, res.Path)
		return nil
	}

	rep, err := svc.Reconciliation(ctx, req)
	if err != nil {
		return err
	}

	fmt.Printf("银行存款余额调节表\n")
	fmt.Printf("科目：%s %s\n", rep.AccountCode, rep.AccountName)
	fmt.Printf("对账期间：%s 至 %s\n", rep.From, rep.AsOf)
	fmt.Printf("期初余额：账面 %s / 银行 %s\n", yuan(rep.BookOpening), bankOrDash(rep.BankOpening))
	if rep.OpeningDiff != 0 {
		fmt.Printf("★ 期初两侧差 %s —— 先把这个数对平，否则下面永远差这么多。\n",
			yuan(rep.OpeningDiff.Abs()))
	}
	fmt.Println()

	// 左右对照的两栏版式。用制表符而不是等宽手绘表格：
	// 中文在等宽字体下宽度是英文的两倍，手画的对齐线一定会歪。
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "企业账面余额\t\t银行对账单余额\t")
	fmt.Fprintf(w, "%s\t\t%s\t\n",
		yuan(rep.BookBalance), bankOrDash(rep.BankBalance))

	section(w, "加：银行已收企业未收", rep.BankReceivedNotBooked)
	section(w, "加：企业已收银行未收", rep.BookReceivedNotBanked)
	section(w, "减：银行已付企业未付", rep.BankPaidNotBooked)
	section(w, "减：企业已付银行未付", rep.BookPaidNotBanked)

	_ = w.Flush()
	fmt.Println()

	fmt.Fprintf(w, "调节后账面余额\t%s\t", yuan(rep.BookAdjusted))
	if rep.BankAdjusted != nil {
		fmt.Fprintf(w, "调节后银行余额\t%s\n", yuan(*rep.BankAdjusted))
	} else {
		fmt.Fprintln(w, "调节后银行余额\t（未导入对账单）")
	}
	_ = w.Flush()

	fmt.Println()
	if rep.BankAdjusted != nil && !rep.Balanced {
		fmt.Printf("差额：%s\n", yuan(rep.Difference.Abs()))
	}
	fmt.Println(rep.Summary)
	for _, n := range rep.Notes {
		fmt.Printf("提示：%s\n", n)
	}
	if rep.UnreconciledFlows > 0 {
		fmt.Printf("\n还有 %d 笔银行流水未生成凭证，处理完这些未达账项会自动减少。\n",
			rep.UnreconciledFlows)
	}
	return nil
}

// section 打印一类未达账项的逐笔明细。
func section(w *tabwriter.Writer, title string, items []service.ReconciliationItemView) {
	if len(items) == 0 {
		return
	}
	var sum money.Money
	for _, it := range items {
		sum = sum.Add(it.Amount)
	}
	fmt.Fprintf(w, "\t\t%s\t%s\n", title, yuan(sum))
	for _, it := range items {
		ref := it.Reference
		if ref == "" {
			ref = "—"
		}
		fmt.Fprintf(w, "\t%s\t%s（%s，%d 天）\t%s\n",
			it.Date, it.Summary, ref, it.Days, yuan(it.Amount))
	}
}

func bankOrDash(m *money.Money) string {
	if m == nil {
		return "（未导入对账单）"
	}
	return yuan(*m)
}
