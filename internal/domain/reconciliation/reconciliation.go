// Package reconciliation 生成银行存款余额调节表。
//
// # 这张表要解决什么
//
// 账面上的银行存款余额，与银行对账单上的余额，**几乎永远不会相等** ——
// 不是因为记错了账，而是因为两边记账的时间差：
//
//	银行已经收到、企业还没记账      如银行直接代收的货款
//	银行已经付出、企业还没记账      如银行直接扣的手续费、代扣的水电
//	企业已经入账、银行还没处理      如企业开出但对方还没去兑的支票
//	企业已经入账、银行还没处理      如企业送存但银行还没入账的支票
//
// 这四类叫**未达账项**。调节表把它们逐项列出来，两边各自加减，
// 得到的「调节后余额」必须相等 —— 相等就说明账没记错，
// 不相等就说明**真有一笔账记错了**，得回去查。
//
// # 为什么这张表有实用价值
//
// 它是会计唯一能自查「我的银行账到底对不对」的工具。
// 只对总额是没用的：差 1 万块，可能是四笔未达账项，也可能是一笔记错，
// 摊开来才知道。
package reconciliation

import (
	"errors"
	"fmt"
	"sort"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// 调节表相关错误。
var (
	ErrBadDate = errors.New("reconciliation: 截止日期无效")
)

// Item 是一笔未达账项。
type Item struct {
	Date calendar.Date
	// Summary 是摘要（流水摘要或凭证摘要）。
	Summary string
	// Reference 是流水号或凭证号，便于回去查。
	Reference string
	Amount    money.Money
	// Days 是距截止日的天数，便于判断这笔挂了多久。
	Days int
}

// Input 是调节表的输入。
type Input struct {
	AsOf calendar.Date

	// From 是对账窗口的起点（含）。留空表示从最早一笔流水之日起。
	//
	// ★ 为什么必须有这个窗口，而不能整本账一起算：
	//
	// 期初余额在**两边都存在** —— 企业账上的 100,000 与银行对账单里
	// 隐含的 100,000 是同一笔钱。如果把它当成「企业已收、银行未收」，
	// 这张表就永远差一个期初数，而且差额恰好等于期初，看起来
	// 像一笔查不出来的错账。窗口之前的余额归入期初，只对窗口内的
	// 发生额找未达账项，才是会计实际的做法。
	From calendar.Date

	AccountCode string
	AccountName string

	// BookOpening 是窗口起点之前的账面余额（借−贷）。
	BookOpening money.Money
	// BankOpening 是窗口起点之前的银行对账单余额。
	//
	// 由对账单推出：最早一笔流水的余额 − 该笔流水金额。
	// 流水里没有余额列时为 nil。
	BankOpening *money.Money

	// BookBalance 是企业账面上的银行存款余额（借−贷）。
	BookBalance money.Money
	// BankBalance 是银行对账单上的余额。
	//
	// 为 nil 表示还没导入对账单、或对账单里没有余额列 ——
	// 此时表照样出，只是「银行方」一栏留空，并在提示里说明。
	BankBalance *money.Money

	// BankReceivedNotBooked 是「银行已收、企业未收」（流水收入但未生成凭证）。
	BankReceivedNotBooked []Item
	// BankPaidNotBooked 是「银行已付、企业未付」。
	BankPaidNotBooked []Item
	// BookReceivedNotBanked 是「企业已收、银行未收」（账面收入但流水里没有）。
	BookReceivedNotBanked []Item
	// BookPaidNotBanked 是「企业已付、银行未付」。
	BookPaidNotBanked []Item

	// UnreconciledFlows 是尚未生成凭证的流水条数。
	//
	// 它不是调节表的一部分，只是一个**提示**：这些流水会全部落进
	// 「银行已收/已付、企业未收/未付」，先处理完它们，表会更干净。
	UnreconciledFlows int
}

// Report 是银行存款余额调节表。
type Report struct {
	AsOf        calendar.Date
	From        calendar.Date
	AccountCode string
	AccountName string

	// BookOpening / BankOpening 是窗口起点之前的期初余额。
	//
	// ★ 两者的差额是这张表最有用的一个诊断：**两侧调节后若不等，
	// 差额必然恰好等于期初差额**（窗口内两边发生额会完全抵消）。
	// 所以差额对不上时先核对期初，不必逐笔去翻未达账项。
	BookOpening money.Money
	BankOpening *money.Money

	// ---- 企业账面方 ----
	BookBalance money.Money
	// BookAdd / BookLess 是「加：银行已收企业未收」「减：银行已付企业未付」。
	BookAdd, BookLess money.Money
	// BookAdjusted 是调节后的余额。
	BookAdjusted money.Money

	// ---- 银行对账单方 ----
	BankBalance *money.Money
	// BankAdd / BankLess 是「加：企业已收银行未收」「减：企业已付银行未付」。
	BankAdd, BankLess money.Money
	// BankAdjusted 是调节后的余额；BankBalance 为 nil 时无意义。
	BankAdjusted *money.Money

	// Lines 是按调节表版式排好的行（含逐笔明细与小计）。
	Lines []Line

	// 四类未达账项的原始明细。
	//
	// 与 Lines 是**冗余**的：Lines 负责「怎么排给人看」，这四个切片负责
	// 「数据是什么」。界面要按类别单独开折叠面板、导出要分 sheet、
	// 测试要断言某一类有哪几笔 —— 这些都需要按类别直接取到明细，
	// 从 Lines 里反解会让调用方到处写 side/kind 过滤。
	// 两处都由 Build 一次性填好，不存在不同步的可能。
	BankReceivedNotBooked []Item
	BankPaidNotBooked     []Item
	BookReceivedNotBanked []Item
	BookPaidNotBanked     []Item

	// UnreconciledFlows 是尚未生成凭证的流水条数。
	UnreconciledFlows int
	// Notes 是给用户看的提示。
	Notes []string
}

// Line 是调节表上的一行（四个加减小节各若干行 + 小计）。
type Line struct {
	// Side 是 book（企业账面方）或 bank（银行对账单方）。
	Side string
	// Kind 是 add（加项）或 less（减项）。
	Kind string
	// Label 是小节名，如「银行已收企业未收」。
	Label string
	// Item 为空表示这是一行小计。
	Item *Item
	// Subtotal 为真表示这是小节合计行。
	Subtotal bool
	// Amount 是小计金额（仅 Subtotal 行有意义）。
	Amount money.Money
}

// 两侧与两种增减。
const (
	SideBook = "book"
	SideBank = "bank"
	KindAdd  = "add"
	KindLess = "less"
)

// Build 生成银行存款余额调节表。
func Build(in Input) (*Report, error) {
	if !in.AsOf.Valid() {
		return nil, ErrBadDate
	}

	// 窗口起点缺省为截止日：没有流水时窗口退化为一天，
	// 所有余额都归入期初，未达账项为空 —— 这也正是「没导入对账单
	// 就找不到未达账项」的诚实表达。
	from := in.From
	if !from.Valid() {
		from = in.AsOf
	}

	r := &Report{
		AsOf: in.AsOf, From: from,
		AccountCode: in.AccountCode, AccountName: in.AccountName,
		BookOpening: in.BookOpening, BankOpening: in.BankOpening,
		BookBalance: in.BookBalance, BankBalance: in.BankBalance,
		UnreconciledFlows: in.UnreconciledFlows,
	}

	// 企业账面方：
	//
	//	加：银行已收企业未收 —— 银行替我们收到了钱，我们账上还没记
	//	减：银行已付企业未付 —— 银行替我们付了钱，我们账上还没记
	//
	// 这两类都是「银行那边已经动了、企业账上还没动」，
	// 所以调节的方向是把它们补进企业账面。
	for i := range in.BankReceivedNotBooked {
		it := in.BankReceivedNotBooked[i]
		it.Days = DaysBetween(it.Date, in.AsOf)
		in.BankReceivedNotBooked[i] = it
		r.BookAdd = r.BookAdd.Add(it.Amount)
	}
	for i := range in.BankPaidNotBooked {
		it := in.BankPaidNotBooked[i]
		it.Days = DaysBetween(it.Date, in.AsOf)
		in.BankPaidNotBooked[i] = it
		r.BookLess = r.BookLess.Add(it.Amount)
	}
	r.BookAdjusted = r.BookBalance.Add(r.BookAdd).Sub(r.BookLess)

	// 银行对账单方：
	//
	//	加：企业已收银行未收 —— 我们记了收入，银行还没入账
	//	减：企业已付银行未付 —— 我们记了支出，银行还没扣款
	for i := range in.BookReceivedNotBanked {
		it := in.BookReceivedNotBanked[i]
		it.Days = DaysBetween(it.Date, in.AsOf)
		in.BookReceivedNotBanked[i] = it
		r.BankAdd = r.BankAdd.Add(it.Amount)
	}
	for i := range in.BookPaidNotBanked {
		it := in.BookPaidNotBanked[i]
		it.Days = DaysBetween(it.Date, in.AsOf)
		in.BookPaidNotBanked[i] = it
		r.BankLess = r.BankLess.Add(it.Amount)
	}
	if in.BankBalance != nil {
		v := in.BankBalance.Add(r.BankAdd).Sub(r.BankLess)
		r.BankAdjusted = &v
	}

	r.buildLines(in)
	r.BankReceivedNotBooked = in.BankReceivedNotBooked
	r.BankPaidNotBooked = in.BankPaidNotBooked
	r.BookReceivedNotBanked = in.BookReceivedNotBanked
	r.BookPaidNotBanked = in.BookPaidNotBanked
	r.buildNotes()
	return r, nil
}

// buildLines 按调节表的固定版式排线。
//
// 每笔未达账项**逐笔列出**（而不是只给一个小计）：
// 差 1 万块时，会计需要知道是哪几笔，而不是「未达账项合计 1 万」。
func (r *Report) buildLines(in Input) {
	add := func(side, kind, label string, items []Item) {
		for i := range items {
			it := items[i]
			r.Lines = append(r.Lines, Line{
				Side: side, Kind: kind, Label: label, Item: &it,
			})
		}
		if len(items) > 0 {
			var sum money.Money
			for _, it := range items {
				sum = sum.Add(it.Amount)
			}
			r.Lines = append(r.Lines, Line{
				Side: side, Kind: kind, Label: label + " 小计",
				Subtotal: true, Amount: sum,
			})
		}
	}
	add(SideBook, KindAdd, "银行已收、企业未收", in.BankReceivedNotBooked)
	add(SideBook, KindLess, "银行已付、企业未付", in.BankPaidNotBooked)
	add(SideBank, KindAdd, "企业已收、银行未收", in.BookReceivedNotBanked)
	add(SideBank, KindLess, "企业已付、银行未付", in.BookPaidNotBanked)
}

func (r *Report) buildNotes() {
	if r.BankBalance == nil {
		r.Notes = append(r.Notes,
			"还没有导入银行对账单（或对账单里没有余额列），"+
				"因此「银行对账单余额」一栏为空。导入对账单后再看这张表才有意义。")
	}
	if r.UnreconciledFlows > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf(
			"有 %d 条流水尚未生成凭证，它们会全部落在「银行已收/已付、企业未收/未付」里。"+
				"先处理完这些流水，调节表会更干净。", r.UnreconciledFlows))
	}
	if len(r.Lines) == 0 && r.BankBalance != nil {
		r.Notes = append(r.Notes, "没有未达账项 —— 两边完全对上了。")
	}
	// 期初差额是最该先说的一句话：它一不等于零，两侧就永远平不了，
	// 而会计的本能反应是去逐笔翻未达账项 —— 白费一晚上。
	if d := r.OpeningDiff(); d != 0 {
		// 提示是**纯文本**，会原样出现在命令行、界面和 Excel 底稿里 ——
		// 任何 Markdown 标记（如 **强调**）都会被用户看见，
		// 所以这里只用中文标点和「★」这类真正通用的符号。
		note := fmt.Sprintf(
			"期初余额两侧不一致：账面 %s、银行 %s，差 %s。"+
				"两侧调节后的差额必然恰好等于这个数 —— "+
				"先把期初核对一致，再去看待摊的未达账项。",
			r.BookOpening, openingText(r.BankOpening), d.Abs())
		r.Notes = append(r.Notes, note)
	}
}

// UnreconciledCount 返回未达账项的总笔数。
func (r *Report) UnreconciledCount() int {
	if r == nil {
		return 0
	}
	return len(r.BankReceivedNotBooked) + len(r.BankPaidNotBooked) +
		len(r.BookReceivedNotBanked) + len(r.BookPaidNotBanked)
}

// OpeningDiff 返回期初余额的差额（账面 − 银行）。
//
// 没有银行期初时返回 0（无从比较）。
func (r *Report) OpeningDiff() money.Money {
	if r == nil || r.BankOpening == nil {
		return 0
	}
	return r.BookOpening.Sub(*r.BankOpening)
}

func openingText(m *money.Money) string {
	if m == nil {
		return "（未知）"
	}
	return m.String()
}

// Balanced 报告两侧调节后余额是否相等。
//
// 相等说明账没记错；不相等说明**真有一笔账记错了**，得回去查。
// 银行对账单余额缺失时返回 true（无从判断，不误报）。
func (r *Report) Balanced() bool {
	if r == nil || r.BankAdjusted == nil {
		return true
	}
	return r.BookAdjusted == *r.BankAdjusted
}

// 调节结果。三态而不是布尔：**「不知道」和「相符」是两件事**。
//
// 只给 bool 的话，没导入对账单时只能二选一 ——
// 返回 true 会让界面亮起绿勾（谎报账实相符），
// 返回 false 会让界面报红（冤枉账记错了）。两者都会让会计误判。
const (
	// StatusBalanced 两侧调节后余额相等，账实相符。
	StatusBalanced = "balanced"
	// StatusUnbalanced 两侧不等 —— 真有一笔账记错了。
	StatusUnbalanced = "unbalanced"
	// StatusUnknown 没有银行对账单余额，无从判断。
	StatusUnknown = "unknown"
)

// Status 返回三态调节结果。
func (r *Report) Status() string {
	if r == nil || r.BankAdjusted == nil {
		return StatusUnknown
	}
	if r.Balanced() {
		return StatusBalanced
	}
	return StatusUnbalanced
}

// Difference 返回两侧调节后余额的差额（缺对账单余额时为 0）。
func (r *Report) Difference() money.Money {
	if r == nil || r.BankAdjusted == nil {
		return 0
	}
	return r.BookAdjusted.Sub(*r.BankAdjusted)
}

// Check 返回调节表自身的不变式问题。
func (r *Report) Check() []error {
	var errs []error
	if r.BookAdjusted != r.BookBalance.Add(r.BookAdd).Sub(r.BookLess) {
		errs = append(errs, fmt.Errorf("企业账面方调节后余额算错"))
	}
	if r.BankAdjusted != nil {
		if *r.BankAdjusted != *r.BankBalance+r.BankAdd-r.BankLess {
			errs = append(errs, fmt.Errorf("银行对账单方调节后余额算错"))
		}
		// 窗口内的发生额在两侧完全抵消，因此差额只能来自期初。
		// 这条不变式一旦破了，说明两张表的取数口径不一致（例如
		// 一 side 用了不同日期边界），属于程序 bug 而不是账的问题。
		if r.Difference() != r.OpeningDiff() {
			errs = append(errs, fmt.Errorf(
				"调节差额 %s 应恰好等于期初差额 %s", r.Difference(), r.OpeningDiff()))
		}
	}
	// 逐笔明细的合计必须等于小计
	bookAdd, bookLess := sumItems(r.Lines, SideBook, KindAdd), sumItems(r.Lines, SideBook, KindLess)
	bankAdd, bankLess := sumItems(r.Lines, SideBank, KindAdd), sumItems(r.Lines, SideBank, KindLess)
	if bookAdd != r.BookAdd || bookLess != r.BookLess ||
		bankAdd != r.BankAdd || bankLess != r.BankLess {
		errs = append(errs, fmt.Errorf("明细合计与调节表小计不符"))
	}
	for _, l := range r.Lines {
		if l.Item != nil && l.Item.Amount.IsNegative() {
			errs = append(errs, fmt.Errorf("第 %q 行金额为负", l.Label))
			break
		}
	}
	return errs
}

func sumItems(lines []Line, side, kind string) money.Money {
	var sum money.Money
	for _, l := range lines {
		if l.Side == side && l.Kind == kind && l.Item != nil {
			sum = sum.Add(l.Item.Amount)
		}
	}
	return sum
}

// Summary 返回一句话概览。
func (r *Report) Summary() string {
	if r == nil {
		return ""
	}
	if r.BankAdjusted == nil {
		return fmt.Sprintf("%s 账面余额 %s（未导入对账单，无法核对）",
			r.AsOf, r.BookBalance)
	}
	if r.Balanced() {
		return fmt.Sprintf("%s 两侧调节后余额均为 %s，账实相符",
			r.AsOf, r.BookAdjusted)
	}
	return fmt.Sprintf("%s 两侧调节后余额不符，差 %s —— 说明有账记错了，需逐笔核对",
		r.AsOf, r.Difference().Abs())
}

// DaysBetween 返回 from 到 to 的天数差。
func DaysBetween(from, to calendar.Date) int {
	return dayNumber(to) - dayNumber(from)
}

func dayNumber(d calendar.Date) int {
	y, m, dd := d.Year, d.Month, d.Day
	if m <= 2 {
		y--
	}
	era := y / 400
	if y < 0 {
		era = (y - 399) / 400
	}
	yoe := y - era*400
	mp := (m + 9) % 12
	doy := (153*mp+2)/5 + dd - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return era*146097 + doe
}

// SortItemsByDate 把未达账项按日期升序排好（同日按摘要），
// 保证同样输入生成的表顺序稳定。
func SortItemsByDate(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].Date.Equal(items[j].Date) {
			return items[i].Date.Before(items[j].Date)
		}
		return items[i].Summary < items[j].Summary
	})
}
