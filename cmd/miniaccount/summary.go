package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"miniaccount/internal/service"
)

// cmdSummary 打印凭证汇总表。
//
// 这张表有三个角度（凭证字 / 日期 / 科目），命令行里全塞进一屏会很乱，
// 所以按 --by 分开输出，默认 all 时三段依次打印。
func cmdSummary(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("summary", flag.ExitOnError)
	db := fs.String("db", "miniaccount.db", "账套数据库文件路径")
	from := fs.String("from", "", "期间开始（默认当前账期）")
	to := fs.String("to", "", "期间结束（默认当前账期）")
	by := fs.String("by", "all", "输出哪一段：all | word 凭证字 | day 日期 | account 科目")
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

	req := service.SummaryRequest{From: *from, To: *to}

	if *exportTo != "" {
		res, err := svc.ExportSummary(ctx, req, *exportTo)
		if err != nil {
			return err
		}
		fmt.Printf("✓ 已导出「%s」（%d 行）\n  %s\n", res.Title, res.Rows, res.Path)
		return nil
	}

	rep, err := svc.Summary(ctx, req)
	if err != nil {
		return err
	}

	fmt.Printf("凭证汇总表\n")
	fmt.Printf("期间：%s 至 %s\n", rep.From, rep.To)
	fmt.Printf("已记账凭证 %d 张，借方合计 %s，贷方合计 %s\n\n",
		rep.VoucherCount, yuan(rep.DebitTotal), yuan(rep.CreditTotal))

	want := func(name string) bool { return *by == "all" || *by == name }

	if want("word") {
		fmt.Println("一、按凭证字")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "凭证字\t张数\t借方金额\t贷方金额")
		for _, r := range rep.WordRows {
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\n",
				r.Label, r.Count, yuan(r.Debit), yuan(r.Credit))
		}
		fmt.Fprintf(w, "合计\t%d\t%s\t%s\n",
			rep.VoucherCount, yuan(rep.DebitTotal), yuan(rep.CreditTotal))
		_ = w.Flush()
		fmt.Println()
	}

	if want("day") {
		fmt.Println("二、按日期")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "日期\t张数\t借方金额\t凭证号")
		for _, r := range rep.DayRows {
			nos := ""
			for i, n := range r.Nos {
				if i > 0 {
					nos += " "
				}
				nos += n
			}
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\n",
				r.Date, r.Count, yuan(r.Debit), truncate(nos, 46))
		}
		_ = w.Flush()
		if rep.GapDays > 0 {
			fmt.Printf("★ 期间内有 %d 天一张凭证都没有。\n", rep.GapDays)
		}
		fmt.Println()
	}

	if want("account") {
		fmt.Println("三、按科目（科目汇总表，据以登总账）")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "科目\t名称\t笔数\t借方发生额\t贷方发生额\t净额")
		for _, r := range rep.AccountRows {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s %s\n",
				r.AccountCode, r.AccountName, r.Count,
				yuan(r.Debit), yuan(r.Credit), r.Dir, yuan(r.Net.Abs()))
		}
		fmt.Fprintf(w, "合计\t\t\t%s\t%s\t\n",
			yuan(rep.DebitTotal), yuan(rep.CreditTotal))
		_ = w.Flush()
		fmt.Println()
	}

	if !rep.Balanced {
		fmt.Println("★ 借贷不平 —— 账被写坏了，或这张表算错了，两种都必须当场查。")
	}
	for _, n := range rep.Notes {
		fmt.Printf("提示：%s\n", n)
	}
	return nil
}
