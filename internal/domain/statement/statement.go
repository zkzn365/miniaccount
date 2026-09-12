// Package statement 生成往来对账单。
//
// # 对账单是什么
//
// 一张发给客户的纸：**「到某天为止，你欠我多少；这一期间我们发生了
// 哪几笔往来」**，双方盖章确认后，它就成了双方账面一致的凭证。
//
// 它和明细账的区别在于**读者**：
//
//	明细账  给自己看的，科目编码、辅助核算、凭证字号一应俱全
//	对账单  给对方看的，只留对方关心的那几列，末尾要能盖章
//
// 因此对账单上不出现科目编码（对方不懂也不关心），
// 但必须有期初余额、本期发生、期末余额三行 —— 没有期初与期末，
// 对方没法核对，这张纸就白发了。
package statement

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// 对账单相关错误。
var (
	ErrBadRange = errors.New("statement: 期间非法")
)

// Line 是对账单上的一笔往来。
type Line struct {
	Date      calendar.Date
	VoucherNo string
	Summary   string
	// Debit / Credit 是发生额（恒为非负，恰有一个为正）。
	//
	// ★ 用「借方/贷方」而不是「增加/减少」：
	// 对账单是给对方看的，而对方也在记账 —— 它的账上这笔业务
	// 方向正好相反。写「增加/减少」会让对方不知道该往哪边记。
	// 写「借方/贷方」并附上余额方向，对方一看就明白。
	Debit  money.Money
	Credit money.Money
	// Balance 是逐行累计后的余额（带符号，借正贷负）。
	//
	// 由生成方算好而不是让对方累加：对方拿到的是一张纸，
	// 它没法验算，只能看最后那个数。中间过程给出来是为了
	// 「双方都能定位到是哪一笔对不上」。
	Balance money.Money
	// Dir 是该行余额的方向（借/贷/平）。
	Dir string
}

// Statement 是一张对账单。
type Statement struct {
	// CompanyName 是我方单位名称（抬头用）。
	CompanyName string
	// ContactName / ContactKind 是对方。
	ContactName string
	// ContactKindLabel 是「客户 / 供应商」等中文名。
	ContactKindLabel string
	// ContactTaxNo / ContactAddress 供正式对账单抬头使用。
	ContactTaxNo   string
	ContactAddress string

	From, To calendar.Date

	// AccountCode / AccountName 是本对账单涉及的科目。
	//
	// 单个科目时显示出来；多个科目时留空并在各行标明 ——
	// 一个客户可能既有应收又有预收，混合时逐行标注才不会看错。
	AccountCode string
	AccountName string
	// Mixed 为真表示本期涉及多个科目。
	Mixed bool

	// Opening 是期初余额（带符号，借正贷负）。
	Opening money.Money
	// OpeningDir 是期初余额方向（借/贷/平）。
	OpeningDir string

	Lines []Line

	// TotalDebit / TotalCredit 是本期发生额合计。
	TotalDebit, TotalCredit money.Money

	// Closing 是期末余额（带符号）。
	Closing money.Money
	// ClosingDir 是期末余额方向。
	ClosingDir string
	// ClosingUpper 是期末余额的中文大写，正式对账单上要写。
	ClosingUpper string
}

// Input 是对账单的输入。
type Input struct {
	CompanyName    string
	ContactID      int64
	ContactName    string
	ContactKind    string
	ContactTaxNo   string
	ContactAddress string
	From, To       calendar.Date
	// Entries 是该往来单位在**期间内**的发生额。
	Entries []Entry
	// OpeningEntries 是期初余额的来源（期间之前的全部发生额）。
	// 传 nil 表示账套从零开始，期初为 0。
	Opening money.Money
	// AccountNames 是科目编码 → 名称。
	AccountNames map[string]string
	// Accounts 用于判定余额方向（决定「借/贷」怎么显示）。
	Accounts *account.Tree
}

// Entry 是一条发生额。
type Entry struct {
	AccountCode string
	Date        calendar.Date
	Debit       money.Money
	Credit      money.Money
	Summary     string
	VoucherNo   string
}

// Build 生成对账单。
func Build(in Input) (*Statement, error) {
	if !in.From.Valid() || !in.To.Valid() || in.To.Before(in.From) {
		return nil, fmt.Errorf("%w: %s ~ %s", ErrBadRange, in.From, in.To)
	}

	st := &Statement{
		CompanyName:      in.CompanyName,
		ContactName:      in.ContactName,
		ContactKindLabel: contactKindLabel(in.ContactKind),
		ContactTaxNo:     in.ContactTaxNo,
		ContactAddress:   in.ContactAddress,
		From:             in.From, To: in.To,
		Opening:    in.Opening,
		OpeningDir: dirLabel(in.Opening),
	}

	// 按日期排序；同日按凭证号 —— 保证同样输入生成的表顺序稳定，
	// 否则每次打印出来行序都不一样，双方对不上。
	entries := append([]Entry(nil), in.Entries...)
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].Date.Equal(entries[j].Date) {
			return entries[i].Date.Before(entries[j].Date)
		}
		return entries[i].VoucherNo < entries[j].VoucherNo
	})

	// 科目：只有一个就写在抬头，多个则逐行标注
	codes := map[string]bool{}
	for _, e := range entries {
		codes[e.AccountCode] = true
	}
	if len(codes) == 1 {
		for c := range codes {
			st.AccountCode = c
			st.AccountName = in.AccountNames[c]
		}
	} else if len(codes) > 1 {
		st.Mixed = true
	}

	balance := in.Opening
	for _, e := range entries {
		balance = balance.Add(e.Debit).Sub(e.Credit)
		st.TotalDebit = st.TotalDebit.Add(e.Debit)
		st.TotalCredit = st.TotalCredit.Add(e.Credit)
		line := Line{
			Date: e.Date, VoucherNo: e.VoucherNo, Summary: e.Summary,
			Debit: e.Debit, Credit: e.Credit,
			Balance: balance, Dir: dirLabel(balance),
		}
		st.Lines = append(st.Lines, line)
	}

	st.Closing = balance
	st.ClosingDir = dirLabel(balance)
	st.ClosingUpper = balance.Abs().ChineseUpper()
	return st, nil
}

// Balanced 报告本期借贷发生额是否相等。
//
// 对账单上这两个数字通常不等（有期初余额在），
// 但**本期发生额借 − 贷 + 期初 = 期末** 必须成立 ——
// 这是对方核对这张纸时的第一句话。
func (s *Statement) Balanced() bool {
	return s.Opening.Add(s.TotalDebit).Sub(s.TotalCredit) == s.Closing
}

// Check 返回对账单自身的不变式问题。
func (s *Statement) Check() []error {
	var errs []error
	if !s.Balanced() {
		errs = append(errs, fmt.Errorf(
			"期初 %s + 本期借 %s − 本期贷 %s ≠ 期末 %s",
			s.Opening, s.TotalDebit, s.TotalCredit, s.Closing))
	}
	// 逐行累计必须与表尾一致
	var d, c money.Money
	bal := s.Opening
	for i, l := range s.Lines {
		d, c = d.Add(l.Debit), c.Add(l.Credit)
		bal = bal.Add(l.Debit).Sub(l.Credit)
		if l.Balance != bal {
			errs = append(errs, fmt.Errorf("第 %d 行余额 %s 与累计值 %s 不符",
				i+1, l.Balance, bal))
		}
		if l.Debit.IsNegative() || l.Credit.IsNegative() {
			errs = append(errs, fmt.Errorf("第 %d 行金额为负", i+1))
		}
		if l.Debit.IsPositive() && l.Credit.IsPositive() {
			errs = append(errs, fmt.Errorf("第 %d 行借贷同时有金额", i+1))
		}
	}
	if d != s.TotalDebit || c != s.TotalCredit {
		errs = append(errs, fmt.Errorf("表尾发生额合计与逐行累计不符"))
	}
	if bal != s.Closing {
		errs = append(errs, fmt.Errorf("逐行累计的期末 %s 与表尾 %s 不符", bal, s.Closing))
	}
	return errs
}

// Summary 返回一句话概览，便于列表页与命令行展示。
func (s *Statement) Summary() string {
	if s == nil {
		return ""
	}
	if s.Closing.IsZero() {
		return fmt.Sprintf("%s ~ %s：%s 已结清", s.From, s.To, s.ContactName)
	}
	return fmt.Sprintf("%s ~ %s：%s %s %s",
		s.From, s.To, s.ContactName, s.ClosingDir, s.Closing.Abs())
}

// dirLabel 返回余额方向的中文名。
func dirLabel(v money.Money) string {
	switch {
	case v.IsPositive():
		return "借"
	case v.IsNegative():
		return "贷"
	default:
		return "平"
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
	case "both":
		return "客户/供应商"
	default:
		return "往来单位"
	}
}

// FormatText 把对账单渲染成纯文本，供命令行与复制粘贴使用。
//
// 版式刻意做得像一张纸：抬头、期初、逐笔、合计、期末、落款。
// 会计经常要把它贴进邮件或微信发给对方，纯文本比截图好用。
func (s *Statement) FormatText() string {
	var b strings.Builder
	width := 78

	fmt.Fprintf(&b, "%s\n", center("往来对账单", width))
	b.WriteString(strings.Repeat("=", width) + "\n")
	fmt.Fprintf(&b, "单位名称：%s\n", s.CompanyName)
	fmt.Fprintf(&b, "往来单位：%s（%s）\n", s.ContactName, s.ContactKindLabel)
	if s.ContactTaxNo != "" {
		fmt.Fprintf(&b, "纳税人识别号：%s\n", s.ContactTaxNo)
	}
	fmt.Fprintf(&b, "对账期间：%s 至 %s\n", s.From, s.To)
	if s.AccountName != "" {
		fmt.Fprintf(&b, "科目：%s %s\n", s.AccountCode, s.AccountName)
	} else if s.Mixed {
		b.WriteString("科目：多个（见各行标注）\n")
	}
	b.WriteString(strings.Repeat("-", width) + "\n")

	fmt.Fprintf(&b, "期初余额：%s %s\n", s.OpeningDir, s.Opening.Abs())
	b.WriteString("\n")

	fmt.Fprintf(&b, "%-11s %-16s %-22s %12s %12s %12s\n",
		"日期", "凭证号", "摘要", "借方", "贷方", "余额")
	for _, l := range s.Lines {
		fmt.Fprintf(&b, "%-11s %-16s %-22s %12s %12s %12s %s\n",
			l.Date, truncate(l.VoucherNo, 16), truncate(l.Summary, 22),
			blankZero(l.Debit), blankZero(l.Credit), l.Balance.Abs(), l.Dir)
	}
	b.WriteString(strings.Repeat("-", width) + "\n")
	fmt.Fprintf(&b, "本期合计：借方 %s　贷方 %s\n", s.TotalDebit, s.TotalCredit)
	fmt.Fprintf(&b, "期末余额：%s %s（大写：%s）\n",
		s.ClosingDir, s.Closing.Abs(), s.ClosingUpper)

	b.WriteString("\n" + strings.Repeat("-", width) + "\n")
	b.WriteString("本单位账面记录如上，请核对无误后签章退回。\n\n")
	fmt.Fprintf(&b, "我方（盖章）：%s          对方（盖章）：%s\n\n", s.CompanyName, s.ContactName)
	b.WriteString("日期：      年    月    日          日期：      年    月    日\n")
	return b.String()
}

func center(s string, width int) string {
	runes := []rune(s)
	if len(runes) >= width {
		return s
	}
	pad := (width - len(runes)) / 2
	return strings.Repeat(" ", pad) + s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func blankZero(v money.Money) string {
	if v.IsZero() {
		return ""
	}
	return v.String()
}
