package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"miniaccount/internal/service"
)

// cmdAging 打印应收/应付账龄分析表。
//
// 账龄是「对账」里最实用的一张表：它把「谁欠我钱、欠了多久」
// 一次性摊开，而不是给一个总数让人自己去翻明细账。
func cmdAging(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("aging", flag.ExitOnError)
	db := fs.String("db", "miniaccount.db", "账套数据库文件路径")
	asOf := fs.String("date", "", "截止日期 YYYY-MM-DD（默认当前账期期末）")
	prefix := fs.String("account", "", "科目范围，如 1122 只看应收、2202 只看应付")
	detail := fs.Bool("detail", false, "展开每一笔未结清明细")
	if err := fs.Parse(args); err != nil {
		return err
	}

	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()
	_ = db

	rep, err := svc.AgingReport(ctx, service.AgingRequest{
		AsOf: *asOf, AccountPrefix: *prefix,
	})
	if err != nil {
		return err
	}

	fmt.Printf("账龄分析表  截止 %s\n\n", rep.AsOf)

	// 分桶概览
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "账龄区间\t金额\t占比")
	for _, b := range rep.Buckets {
		pct := ""
		if b.Percent > 0 {
			pct = fmt.Sprintf("%.1f%%", b.Percent)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", b.Label, yuan(b.Amount), pct)
	}
	fmt.Fprintf(w, "合计\t%s\t\n", yuan(rep.Total))
	_ = w.Flush()

	if len(rep.Rows) == 0 {
		fmt.Println("\n这个范围内没有带往来辅助核算的分录。")
		return nil
	}

	fmt.Printf("\n按往来单位\n\n")
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	header := "科目\t往来单位\t未结清余额"
	for _, b := range rep.Buckets {
		header += "\t" + b.Label
	}
	header += "\t最老账龄"
	fmt.Fprintln(w, header)

	for _, r := range rep.Rows {
		line := fmt.Sprintf("%s\t%s\t%s", r.AccountCode, r.ContactName, yuan(r.Balance))
		for _, b := range r.Buckets {
			v := ""
			if !b.Amount.IsZero() {
				v = yuan(b.Amount)
			}
			line += "\t" + v
		}
		age := ""
		if r.MaxDays > 0 {
			age = fmt.Sprintf("%d 天", r.MaxDays)
		}
		fmt.Fprintf(w, "%s\t%s\n", line, age)
	}
	_ = w.Flush()

	if *detail {
		for _, r := range rep.Rows {
			if len(r.Items) == 0 {
				continue
			}
			fmt.Printf("\n%s %s 的未结清明细：\n", r.AccountCode, r.ContactName)
			dw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(dw, "  发生日期\t账龄\t未结清金额\t摘要\t凭证号\t区间")
			for _, it := range r.Items {
				fmt.Fprintf(dw, "  %s\t%d 天\t%s\t%s\t%s\t%s\n",
					it.Date, it.Days, yuan(it.Amount),
					truncateStr(it.Summary, 20), it.VoucherNo, it.BucketLabel)
			}
			_ = dw.Flush()
		}
	}

	// 一句话结论 —— 命令行下最有用的就是这一行
	fmt.Printf("\n%s\n", rep.Summary)
	if rep.Over90 > 0 {
		fmt.Printf("⚠ 其中 %s 已超过 90 天，建议优先催收/核对\n", yuan(rep.Over90))
	}
	if !*detail && rep.Over90 > 0 {
		fmt.Printf("  加 --detail 可看到具体是哪几笔\n")
	}
	_ = strings.TrimSpace
	return nil
}
