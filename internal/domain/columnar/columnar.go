// Package columnar 生成多栏式明细账。
//
// # 它和三栏式明细账的区别
//
// 三栏式是「借方 | 贷方 | 余额」三列，一行一笔，所有业务挤在同一列里。
// 多栏式把**发生额较多的一侧横向拆开**，一个下级科目占一栏：
//
//	管理费用明细账（借方多栏）
//	日期    凭证号   摘要      办公费  差旅费  工资  ...  合计    余额
//	03-05  记-0003  办公用品   300                      300    300
//	03-12  记-0008  出差机票           2,400            2,400  2,700
//	03-31  记-0021  结转损益   -300   -2,400           -2,700     0
//
// 一眼就能看出「这个月哪一项花得多」，而三栏式必须把整本账翻一遍
// 再用计算器按科目加总。这就是它存在的全部理由。
//
// # 栏目从哪来
//
// **栏目就是下级科目，不需要任何额外配置。**
//
// 这一点值得强调，因为很多实现把它做成了需要用户手工定义的「分析栏目」，
// 于是同一个概念在系统里有两套来源，迟早对不上。本工程的做法是：
// 科目树已经表达了「管理费用由哪些项目构成」，那就直接拿它当栏目。
//
// 增值税专栏也一样 —— 财会〔2016〕22号 规定的 10 个专栏
// （进项税额、销项税额抵减、已交税金……）在科目表里就是
// 「应交税费—应交增值税」下的 10 个子科目，各自带着余额方向。
// 于是借方 6 栏、贷方 4 栏的版式不用任何特殊代码就能生成。
//
// # 红字：为什么用净额法而不是教科书上的「贷方栏」
//
// 教科书版式里，借方多栏式只设**一个**贷方栏，所有贷方发生额
// （冲销、结转）都记到那一个栏里。本工程不这样做，而是让每一栏
// 记自己那一项的**净额**：冲销 300 元办公费就记成「办公费 −300」。
//
// 理由是本工程用**红字凭证**做冲销（见 ledger 包）：冲销是一张
// 借贷互换的真实凭证。如果把它塞进通用的贷方栏，
// 「冲掉的是哪一项费用」这个信息就丢了 —— 而那恰恰是看多栏式
// 明细账时最想知道的事。净额法保住了它：
//
//	03-05  办公用品   300      办公费栏
//	03-20  冲销误记  -300      办公费栏（而不是笼统的「贷方」）
//
// 顺带的好处：月末结转损益时每一栏都归零，账面自然呈现
// 「本月累计发生 → 全部结转」的完整过程，不必再去三栏式里对。
package columnar

import (
	"errors"
	"fmt"
	"sort"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// 多栏式明细账相关错误。
var (
	ErrBadRange    = errors.New("columnar: 期间非法")
	ErrNoColumns   = errors.New("columnar: 没有栏目")
	ErrBadColumn   = errors.New("columnar: 栏目定义非法")
	ErrBadMovement = errors.New("columnar: 发生额非法")
)

// Side 是栏目所属的方向。
type Side string

// 两种方向。
const (
	SideDebit  Side = "debit"
	SideCredit Side = "credit"
)

// Valid 报告方向是否合法。
func (s Side) Valid() bool { return s == SideDebit || s == SideCredit }

// Opposite 返回相反方向。
func (s Side) Opposite() Side {
	if s == SideDebit {
		return SideCredit
	}
	return SideDebit
}

// Label 返回方向的中文名。
func (s Side) Label() string {
	if s == SideCredit {
		return "贷"
	}
	return "借"
}

// Column 是一个栏目。
type Column struct {
	// Key 是栏目标识，自动分栏时就是科目编码。
	Key string
	// Label 是栏目标题，如「办公费」「进项税额」。
	Label string
	// Side 是该栏目所属方向。借方栏目记借方发生额，贷方栏目记贷方发生额。
	Side Side
	// AccountCode 是该栏目归集的科目编码。
	AccountCode string
	// Other 为真表示这是「兜底栏」—— 记在父科目本身、
	// 或不在任何栏目科目上的发生额。它由本包按需自动补上。
	//
	// 兜底栏出现本身就是个信号：说明有人绕开了明细科目直接记账，
	// 或者调用方给的栏目列表漏了科目。Report.Notes 会点出来。
	Other bool
}

// Movement 是一笔发生额（一条总账分录）。
type Movement struct {
	Date      calendar.Date
	VoucherNo string
	Summary   string
	// AccountCode 是这笔分录落在哪个科目上（最末级）。
	AccountCode string
	// Seq 是同一张凭证内的行号，用于稳定排序。
	Seq int
	// Debit / Credit 恒为非负，恰有一个大于零（与 ledger_entry 的约束一致）。
	Debit, Credit money.Money
}

// Input 是多栏式明细账的输入。
type Input struct {
	AccountCode string
	AccountName string
	From, To    calendar.Date

	// Columns 是栏目定义，由调用方按科目树给出（含各自方向）。
	// 顺序即显示顺序：借方栏目在前、贷方栏目在后是惯例，但不强制。
	Columns []Column

	// Opening 是期初余额，带符号：借正贷负。
	Opening money.Money

	Movements []Movement
}

// Row 是多栏式明细账上的一行（一笔发生额）。
type Row struct {
	Date      calendar.Date
	VoucherNo string
	Summary   string
	// ColumnIndex 指向 Report.Columns 中的栏目。
	ColumnIndex int
	// Amount 是计入该栏目的金额，**带符号**：
	// 该栏目方向的增加为正，红字（冲销）为负。
	Amount money.Money
	// Debit / Credit 是这笔分录的原始借贷发生额（恒非负）。
	// 保留它们是为了能和明细账、科目余额表逐笔对上。
	Debit, Credit money.Money
	// Balance 是逐行累计后的余额（带符号，借正贷负）。
	Balance money.Money
	// Dir 是该行余额方向的中文名（借/贷/平）。
	Dir string
}

// Report 是一张多栏式明细账。
type Report struct {
	AccountCode string
	AccountName string
	From, To    calendar.Date

	// Columns 是最终使用的栏目（含自动补上的兜底栏）。
	Columns []Column

	Opening    money.Money
	OpeningDir string

	Rows []Row

	// ColumnTotals 与 Columns 一一对应，是各栏目的本期合计。
	ColumnTotals []money.Money

	// DebitTotal / CreditTotal 是本期借方、贷方发生额合计（总额，不净额）。
	//
	// ★ 这两个数必须与三栏式明细账、科目余额表**完全一致** ——
	// 它们是最容易出岔子的地方：多栏式把金额搬到了横向的栏目里，
	// 一旦某笔发生额没归上栏目或被算了两次，
	// 总额就对不上，而表面上每栏看起来都很合理。
	// 所以 Check() 把它写成了不变式，测试也逐笔交叉验证。
	DebitTotal, CreditTotal money.Money

	Closing    money.Money
	ClosingDir string

	// Notes 是给用户看的提示。
	Notes []string
}

// Build 生成多栏式明细账。
func Build(in Input) (*Report, error) {
	if !in.From.Valid() || !in.To.Valid() {
		return nil, ErrBadRange
	}
	if in.From.After(in.To) {
		return nil, ErrBadRange
	}
	if len(in.Columns) == 0 {
		return nil, ErrNoColumns
	}

	// 栏目索引：科目编码 → 栏目下标。同一个科目出现两次是配置错误。
	colOf := make(map[string]int, len(in.Columns))
	for i, c := range in.Columns {
		if c.Side == "" {
			// 方向缺省按借方：绝大多数多栏式账是借方多栏。
			in.Columns[i].Side = SideDebit
		}
		if !in.Columns[i].Side.Valid() {
			return nil, fmt.Errorf("%w: 栏目 %q 的方向 %q", ErrBadColumn, c.Label, c.Side)
		}
		if c.AccountCode != "" {
			if _, dup := colOf[c.AccountCode]; dup {
				return nil, fmt.Errorf("%w: 科目 %s 被指定为两个栏目", ErrBadColumn, c.AccountCode)
			}
			colOf[c.AccountCode] = i
		}
	}

	cols := in.Columns
	rows := make([]Row, 0, len(in.Movements))

	var (
		debitTotal, creditTotal money.Money
		balance                 = in.Opening
		otherDebit, otherCredit money.Money
	)
	// otherCol 是按需补上的兜底栏下标（借、贷各一个）。
	otherCol := map[Side]int{SideDebit: -1, SideCredit: -1}

	// 逐笔登记。排序在最后做，但归集顺序不影响合计，
	// 只有余额的逐行累计依赖顺序，所以先按发生顺序处理。
	movs := append([]Movement(nil), in.Movements...)
	sort.SliceStable(movs, func(i, j int) bool {
		if !movs[i].Date.Equal(movs[j].Date) {
			return movs[i].Date.Before(movs[j].Date)
		}
		if movs[i].VoucherNo != movs[j].VoucherNo {
			return movs[i].VoucherNo < movs[j].VoucherNo
		}
		return movs[i].Seq < movs[j].Seq
	})

	for _, m := range movs {
		if m.Debit < 0 || m.Credit < 0 {
			return nil, fmt.Errorf("%w: %s 的金额为负（借贷方应恒为非负）",
				ErrBadMovement, m.VoucherNo)
		}
		if m.Debit > 0 && m.Credit > 0 {
			return nil, fmt.Errorf("%w: %s 同时有借方和贷方金额",
				ErrBadMovement, m.VoucherNo)
		}
		if m.Debit == 0 && m.Credit == 0 {
			// 零金额分录入不了库，但防御一下：跳过而不是造一行空行。
			continue
		}

		idx, ok := colOf[m.AccountCode]
		if !ok {
			// 兜底：记在父科目本身，或调用方漏配了栏目。
			side := SideDebit
			if m.Credit > 0 {
				side = SideCredit
			}
			if otherCol[side] < 0 {
				cols = append(cols, Column{
					Key: "~other~" + string(side), Label: "其他",
					Side: side, Other: true,
				})
				otherCol[side] = len(cols) - 1
			}
			idx = otherCol[side]
			if side == SideDebit {
				otherDebit = otherDebit.Add(m.Debit)
			} else {
				otherCredit = otherCredit.Add(m.Credit)
			}
		}

		// ★ 净额：按栏目自己的方向记，红字自然成为负数。
		var amt money.Money
		if cols[idx].Side == SideDebit {
			amt = m.Debit.Sub(m.Credit)
		} else {
			amt = m.Credit.Sub(m.Debit)
		}

		balance = balance.Add(m.Debit).Sub(m.Credit)
		debitTotal = debitTotal.Add(m.Debit)
		creditTotal = creditTotal.Add(m.Credit)

		rows = append(rows, Row{
			Date: m.Date, VoucherNo: m.VoucherNo, Summary: m.Summary,
			ColumnIndex: idx, Amount: amt,
			Debit: m.Debit, Credit: m.Credit,
			Balance: balance, Dir: dirLabel(balance),
		})
	}

	// 各栏合计
	totals := make([]money.Money, len(cols))
	for _, r := range rows {
		totals[r.ColumnIndex] = totals[r.ColumnIndex].Add(r.Amount)
	}

	// 相邻的兜底栏排在末尾会影响阅读：把「其他」栏挪到同方向的最后一栏。
	cols, totals, rows = moveOtherToEnd(cols, totals, rows)

	r := &Report{
		AccountCode: in.AccountCode, AccountName: in.AccountName,
		From: in.From, To: in.To,
		Columns: cols, Opening: in.Opening, OpeningDir: dirLabel(in.Opening),
		Rows: rows, ColumnTotals: totals,
		DebitTotal: debitTotal, CreditTotal: creditTotal,
		Closing: balance, ClosingDir: dirLabel(balance),
	}
	r.buildNotes(otherDebit, otherCredit)
	return r, nil
}

// moveOtherToEnd 把兜底栏挪到同方向的末尾，并同步修正行里的下标。
//
// 兜底栏是异常情况的产物，出现在中间会打断「办公费、差旅费、工资…」
// 这样的正常栏目序列。
func moveOtherToEnd(cols []Column, totals []money.Money, rows []Row) ([]Column, []money.Money, []Row) {
	// 目标顺序：非兜底的借方栏、借方兜底栏、非兜底的贷方栏、贷方兜底栏
	var order []int
	for _, side := range []Side{SideDebit, SideCredit} {
		for i, c := range cols {
			if c.Side == side && !c.Other {
				order = append(order, i)
			}
		}
		for i, c := range cols {
			if c.Side == side && c.Other {
				order = append(order, i)
			}
		}
	}
	// 是否已经是目标顺序（常见情况，省掉一次重排）
	sorted := true
	for k, v := range order {
		if k != v {
			sorted = false
			break
		}
	}
	if sorted {
		return cols, totals, rows
	}

	remap := make([]int, len(cols))
	newCols := make([]Column, 0, len(cols))
	newTotals := make([]money.Money, 0, len(totals))
	for newIdx, oldIdx := range order {
		remap[oldIdx] = newIdx
		newCols = append(newCols, cols[oldIdx])
		newTotals = append(newTotals, totals[oldIdx])
	}
	for i := range rows {
		rows[i].ColumnIndex = remap[rows[i].ColumnIndex]
	}
	return newCols, newTotals, rows
}

func (r *Report) buildNotes(otherDebit, otherCredit money.Money) {
	if otherDebit > 0 || otherCredit > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf(
			"有 %s 借方、%s 贷方发生额没有落在任何栏目上，已归入「其他」栏。"+
				"通常是直接记在了上级科目上，或栏目里漏了对应科目。",
			otherDebit, otherCredit))
	}
	if len(r.Rows) == 0 {
		r.Notes = append(r.Notes,
			"这一期间没有发生额。若期初余额不为零，说明本期未动过这个科目。")
	}
}

// Net 返回本期净发生额（借 − 贷）。
func (r *Report) Net() money.Money {
	if r == nil {
		return 0
	}
	return r.DebitTotal.Sub(r.CreditTotal)
}

// SideTotal 返回某一方向全部栏目的合计。
func (r *Report) SideTotal(side Side) money.Money {
	if r == nil {
		return 0
	}
	var sum money.Money
	for i, c := range r.Columns {
		if c.Side == side {
			sum = sum.Add(r.ColumnTotals[i])
		}
	}
	return sum
}

// ColumnTotal 按栏目标识取合计，找不到返回 false。
func (r *Report) ColumnTotal(key string) (money.Money, bool) {
	if r == nil {
		return 0, false
	}
	for i, c := range r.Columns {
		if c.Key == key {
			return r.ColumnTotals[i], true
		}
	}
	return 0, false
}

// Balanced 报告栏目合计与借贷发生额是否自洽。
//
// 这是这张表唯一的硬约束：**横向加总必须等于纵向加总**。
// 栏目是把金额搬到了横向上，一旦某笔发生额没归上栏目或被算了两次，
// 表面上每一栏都合情合理，只有这条等式能发现。
func (r *Report) Balanced() bool {
	if r == nil {
		return false
	}
	return r.SideTotal(SideDebit).Sub(r.SideTotal(SideCredit)) == r.Net()
}

// Check 返回多栏式明细账自身的不变式问题。
//
// 这里查的是「程序算错了」，不是「账记错了」——
// 账的问题（比如余额方向不对）由往来/账龄那些表去发现。
func (r *Report) Check() []error {
	if r == nil {
		return []error{fmt.Errorf("columnar: 报表为空")}
	}
	var errs []error

	// 1. 横向合计 == 纵向净额
	if !r.Balanced() {
		errs = append(errs, fmt.Errorf(
			"栏目合计 借 %s − 贷 %s 不等于本期净发生额 %s",
			r.SideTotal(SideDebit), r.SideTotal(SideCredit), r.Net()))
	}

	// 2. 期末余额 == 期初 + 本期净额
	if r.Closing != r.Opening.Add(r.Net()) {
		errs = append(errs, fmt.Errorf(
			"期末余额 %s 不等于期初 %s + 本期净额 %s",
			r.Closing, r.Opening, r.Net()))
	}

	// 3. 每栏合计 == 该栏各行之和
	byCol := make([]money.Money, len(r.Columns))
	for i, row := range r.Rows {
		if row.ColumnIndex < 0 || row.ColumnIndex >= len(r.Columns) {
			errs = append(errs, fmt.Errorf("第 %d 行的栏目下标 %d 越界",
				i+1, row.ColumnIndex))
			continue
		}
		byCol[row.ColumnIndex] = byCol[row.ColumnIndex].Add(row.Amount)
	}
	for i := range r.Columns {
		if byCol[i] != r.ColumnTotals[i] {
			errs = append(errs, fmt.Errorf("栏目「%s」合计 %s 与逐行之和 %s 不符",
				r.Columns[i].Label, r.ColumnTotals[i], byCol[i]))
		}
	}

	// 4. 借/贷发生额合计 == 逐行之和（保证没有漏计或重复计）
	var d, c money.Money
	for _, row := range r.Rows {
		d = d.Add(row.Debit)
		c = c.Add(row.Credit)
	}
	if d != r.DebitTotal || c != r.CreditTotal {
		errs = append(errs, fmt.Errorf(
			"发生额合计 借 %s/贷 %s 与逐行之和 借 %s/贷 %s 不符",
			r.DebitTotal, r.CreditTotal, d, c))
	}

	// 5. 余额逐行累计必须与最后一行一致
	if n := len(r.Rows); n > 0 && r.Rows[n-1].Balance != r.Closing {
		errs = append(errs, fmt.Errorf("最后一行的余额 %s 与期末余额 %s 不符",
			r.Rows[n-1].Balance, r.Closing))
	}
	return errs
}

// Summary 返回一句话概览。
func (r *Report) Summary() string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("%s %s 至 %s：借方 %s、贷方 %s，期末%s %s",
		r.AccountName, r.From, r.To, r.DebitTotal, r.CreditTotal,
		r.ClosingDir, r.Closing.Abs())
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
