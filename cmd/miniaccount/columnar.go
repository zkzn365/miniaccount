package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/service"
)

// cmdColumnar 打印多栏式明细账。
//
// 终端里放不下横向几十列，所以命令行版刻意换了个排法：
// 先列各栏目合计（会计最关心的一屏），再逐笔明细并标出落在哪一栏。
// 要看真正的横向版式，用 --export 导出 Excel 或去界面。
func cmdColumnar(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("columnar", flag.ExitOnError)
	db := fs.String("db", "miniaccount.db", "账套数据库文件路径")
	account := fs.String("account", "", "要展开的科目编码，如 5602 管理费用 或 222101 应交增值税")
	from := fs.String("from", "", "期间开始（默认当前账期）")
	to := fs.String("to", "", "期间结束（默认当前账期）")
	list := fs.Bool("list", false, "列出可以展开的科目")
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
		list, err := svc.ColumnarAccounts(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "编码\t科目\t常用场景")
		for _, a := range list {
			fmt.Fprintf(w, "%s\t%s\t%s\n", a.Code, a.FullName, hintFor(a.Code))
		}
		_ = w.Flush()
		fmt.Printf("\n用法：miniaccount columnar --account <编码>\n")
		return nil
	}

	if strings.TrimSpace(*account) == "" {
		return fmt.Errorf("必须指定 --account <科目编码>；不知道有哪些科目时先加 --list")
	}

	req := service.ColumnarRequest{AccountCode: *account, From: *from, To: *to}

	if *exportTo != "" {
		res, err := svc.ExportColumnar(ctx, req, *exportTo)
		if err != nil {
			return err
		}
		fmt.Printf("✓ 已导出「%s」（%d 行 / %d 栏）\n  %s\n",
			res.Title, res.Rows, len(res.Columns), res.Path)
		return nil
	}

	rep, err := svc.Columnar(ctx, req)
	if err != nil {
		return err
	}

	fmt.Printf("多栏式明细账\n")
	fmt.Printf("科目：%s %s\n", rep.AccountCode, rep.AccountName)
	fmt.Printf("期间：%s 至 %s\n", rep.From, rep.To)
	fmt.Printf("期初余额：%s %s\n\n", rep.OpeningDir, yuan(rep.Opening.Abs()))

	// 一、各栏目合计 —— 这一屏就是多栏式明细账存在的理由
	fmt.Println("各栏目发生额：")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "方向\t栏目\t发生额")
	for i, c := range rep.Columns {
		amt := rep.ColumnTotals[i]
		mark := ""
		if c.Other {
			mark = "  ← 有发生额没落到栏目上"
		}
		fmt.Fprintf(w, "%s\t%s\t%s%s\n", c.SideLabel, c.Label, yuan(amt), mark)
	}
	_ = w.Flush()

	// ★ 区分「本期借贷发生额」与「栏目净额」：
	// 前者是总账口径的总额（要与明细账、科目余额表一致），
	// 后者是各栏目按自己方向记账后的合计。管理费用只有借方栏目、
	// 两者恰好相等；增值税两侧都有栏目，两者就不一样了。
	fmt.Printf("\n本期借方发生额：%s   本期贷方发生额：%s\n",
		yuan(rep.DebitTotal), yuan(rep.CreditTotal))
	fmt.Printf("借方栏目净额：%s   贷方栏目净额：%s\n",
		yuan(sideNet(rep, "借")), yuan(sideNet(rep, "贷")))

	// 二、逐笔明细
	fmt.Printf("\n逐笔明细：\n")
	if len(rep.Rows) == 0 {
		fmt.Println("  （本期没有发生额）")
	} else {
		w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "日期\t凭证号\t摘要\t栏目\t金额\t余额")
		for _, r := range rep.Rows {
			col := "—"
			var amt money.Money
			for i, a := range r.Amounts {
				if a != 0 {
					col = rep.Columns[i].Label
					amt = a
					break
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s %s\n",
				r.Date, r.VoucherNo, truncate(r.Summary, 20), col,
				yuan(amt), r.Dir, yuan(r.Balance.Abs()))
		}
		_ = w.Flush()
	}

	fmt.Printf("\n期末余额：%s %s\n", rep.ClosingDir, yuan(rep.Closing.Abs()))
	fmt.Println(rep.Summary)
	for _, n := range rep.Notes {
		fmt.Printf("提示：%s\n", n)
	}
	return nil
}

// sideNet 返回某一侧栏目的净额。
func sideNet(rep *service.ColumnarView, sideLabel string) money.Money {
	var sum money.Money
	for i, c := range rep.Columns {
		if c.SideLabel == sideLabel {
			sum = sum.Add(rep.ColumnTotals[i])
		}
	}
	return sum
}

// hintFor 给常见科目补一句「拿它看什么」，方便用户挑科目。
func hintFor(code string) string {
	switch code {
	case "5602":
		return "看这个月办公、差旅、工资各花了多少"
	case "222101":
		return "增值税专用格式：借方 6 栏、贷方 4 栏"
	case "5601":
		return "销售费用的构成"
	case "5603":
		return "财务费用（利息、手续费）"
	case "5001":
		return "主营业务收入的构成"
	case "5101":
		return "制造费用的构成"
	case "5001A", "5001a":
		return ""
	}
	if strings.HasPrefix(code, "56") {
		return "费用类：看明细构成"
	}
	if strings.HasPrefix(code, "5") {
		return "损益类：看明细构成"
	}
	return ""
}

// truncate 按**字符**截断，避免把一个汉字切成两半。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
