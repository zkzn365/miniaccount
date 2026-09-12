package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"miniaccount/internal/service"
)

// cmdStatement 打印往来对账单。
//
// 这张纸是发出去盖章的，因此版式刻意做得像一张纸：
// 抬头、期初、逐笔、合计、期末、双方盖章栏。
// 默认直接输出可复制粘贴的纯文本 —— 会计常要把它贴进邮件或微信。
func cmdStatement(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("statement", flag.ExitOnError)
	db := fs.String("db", "miniaccount.db", "账套数据库文件路径")
	contact := fs.Int64("contact", 0, "往来单位 id（与 --list 二选一）")
	list := fs.Bool("list", false, "列出本期有往来发生的单位，便于挑 id")
	from := fs.String("from", "", "对账期间开始（默认当前账期）")
	to := fs.String("to", "", "对账期间结束（默认当前账期）")
	account := fs.String("account", "", "限定科目，如 1122 只看应收")
	table := fs.Bool("table", false, "改用表格输出（默认纯文本版式）")
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
		contacts, err := svc.ActiveContacts(ctx, *from, *to)
		if err != nil {
			return err
		}
		if len(contacts) == 0 {
			fmt.Println("这个期间没有往来发生。")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\t名称\t类型")
		for _, c := range contacts {
			fmt.Fprintf(w, "%d\t%s\t%s\n", c.ID, c.Name, c.KindLabel)
		}
		_ = w.Flush()
		fmt.Printf("\n用法：miniaccount statement --contact <ID>\n")
		return nil
	}

	if *contact == 0 {
		return errors.New("必须指定 --contact <往来单位 id>；不知道 id 时先加 --list")
	}

	st, err := svc.Statement(ctx, service.StatementRequest{
		ContactID: *contact, From: *from, To: *to, AccountPrefix: *account,
	})
	if err != nil {
		return err
	}

	// 纯文本版式：直接可以复制粘贴或重定向到文件打印
	if !*table {
		fmt.Print(st.Text)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Printf("往来对账单  %s（%s）\n", st.ContactName, st.ContactKindLabel)
	fmt.Printf("期间 %s 至 %s\n\n", st.From, st.To)
	fmt.Fprintf(w, "期初余额\t%s %s\t\n\n", st.OpeningDir, yuan(st.Opening.Abs()))
	fmt.Fprintln(w, "日期\t凭证号\t摘要\t借方\t贷方\t余额\t方向")
	for _, l := range st.Lines {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			l.Date, l.VoucherNo, l.Summary,
			blankIfZero(l.Debit), blankIfZero(l.Credit), yuan(l.Balance.Abs()), l.Dir)
	}
	fmt.Fprintf(w, "\t\t本期合计\t%s\t%s\t\t\n\n", yuan(st.TotalDebit), yuan(st.TotalCredit))
	fmt.Fprintf(w, "期末余额\t%s %s\t（大写：%s）\n",
		st.ClosingDir, yuan(st.Closing.Abs()), st.ClosingUpper)
	_ = w.Flush()
	return nil
}
