package report

import (
	"embed"
	"encoding/csv"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

//go:embed defs/*.csv
var defsFS embed.FS

// 报表定义相关错误。
var (
	ErrBadDefinition = errors.New("report: 报表定义非法")
	ErrNotBalanced   = errors.New("report: 报表勾稽关系不成立")
)

// LineType 是报表行的种类。
type LineType string

// 报表行种类。中国报表里「其中」附列项（memo）与明细项（item）是两类：
// memo 行不参与小计，只是把某个明细项的构成拆开显示。
const (
	LineItem     LineType = "item"     // 明细项，参与小计
	LineMemo     LineType = "memo"     // 「其中」附列项，不参与小计
	LineSubtotal LineType = "subtotal" // 小计
	LineTotal    LineType = "total"    // 总计
)

// Side 是资产负债表左右两侧。
type Side string

// 报表侧。
const (
	SideLeft   Side = "left"   // 资产
	SideRight  Side = "right"  // 负债和所有者权益
	SideSingle Side = "single" // 利润表等单列报表
)

// Line 是报表中的一行。
type Line struct {
	No      int      // 官方行次
	Side    Side     // 所在侧
	Section string   // 所属小节（流动资产、流动负债…）
	Name    string   // 项目名称
	Type    LineType // 行种类
	Formula *Formula // 取数公式，可能为空（手工填列）
	Note    string   // 备注（官方填列口径摘要）

	// Value 是求值结果，由 Statement.Compute 填充。
	Value money.Money
}

// IsComputed 报告该行是否由公式取数。
func (l *Line) IsComputed() bool { return l.Formula != nil && !l.Formula.Empty() }

// MemoPrefix 是「其中」附列项在报表上的固定前缀。
//
// 中国报表里附列项一律写作「其中：XXX」，这一点不分软件、不分行业。
const MemoPrefix = "其中："

// DisplayName 返回报表上该行应显示的名称。
//
// ★ 前缀由**行类型**决定，而不是写死在名称里。
// 曾把「其中：」直接写进定义文件的名称字段，结果是：
//   - 展示层再加一次前缀，出现「其中：其中：消费税」
//   - 名称里带了排版痕迹，做模糊匹配 / 导出 CSV 时都要额外处理
//
// 数据存名称，表现层加前缀 —— 这个分工必须在数据结构上定死。
func (l *Line) DisplayName() string {
	if l.Type == LineMemo {
		return MemoPrefix + l.Name
	}
	return l.Name
}

// Definition 是一张报表的定义。
type Definition struct {
	Code  string // balance_sheet | income_statement
	Name  string
	Lines []*Line

	byNo map[int]*Line
}

// Line 按行次取行。
func (d *Definition) Line(no int) (*Line, bool) {
	l, ok := d.byNo[no]
	return l, ok
}

// ---------------------------------------------------------------------------
// 加载内置定义
// ---------------------------------------------------------------------------

// LoadBalanceSheet 加载内置的资产负债表定义（会小企 01 表，53 行）。
func LoadBalanceSheet() (*Definition, error) {
	return loadCSV("defs/balance_sheet_lines.csv", "balance_sheet", "资产负债表")
}

// LoadIncomeStatement 加载内置的利润表定义（会小企 02 表，32 行）。
func LoadIncomeStatement() (*Definition, error) {
	return loadCSV("defs/income_statement_lines.csv", "income_statement", "利润表")
}

func loadCSV(path, code, name string) (*Definition, error) {
	f, err := defsFS.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: 打开 %s: %w", ErrBadDefinition, path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // 容忍不同列数（两张表的列不同）
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: 解析 %s: %w", ErrBadDefinition, path, err)
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("%w: %s 没有数据行", ErrBadDefinition, path)
	}

	if code == "balance_sheet" {
		return parseBalanceSheet(code, name, rows)
	}
	return parseIncomeStatement(code, name, rows)
}

func parseBalanceSheet(code, name string, rows [][]string) (*Definition, error) {
	d := &Definition{Code: code, Name: name, byNo: map[int]*Line{}}
	// 表头：line_no,side,section,name,line_type,formula,note
	for i, rec := range rows[1:] {
		if len(rec) < 5 || strings.TrimSpace(rec[0]) == "" {
			continue
		}
		no, err := strconv.Atoi(strings.TrimSpace(rec[0]))
		if err != nil {
			return nil, fmt.Errorf("%w: 第 %d 行行次非法: %q", ErrBadDefinition, i+2, rec[0])
		}
		f, err := ParseFormula(cell(rec, 5))
		if err != nil {
			return nil, fmt.Errorf("%w: 行 %d 公式错误: %w", ErrBadDefinition, no, err)
		}
		d.Lines = append(d.Lines, &Line{
			No:      no,
			Side:    Side(strings.TrimSpace(rec[1])),
			Section: strings.TrimSpace(rec[2]),
			Name:    strings.TrimSpace(rec[3]),
			Type:    LineType(strings.TrimSpace(rec[4])),
			Formula: f,
			Note:    cell(rec, 6),
		})
	}
	return d.finish()
}

func parseIncomeStatement(code, name string, rows [][]string) (*Definition, error) {
	d := &Definition{Code: code, Name: name, byNo: map[int]*Line{}}
	// 表头：line_no,name,line_type,formula,note
	for i, rec := range rows[1:] {
		if len(rec) < 3 || strings.TrimSpace(rec[0]) == "" {
			continue
		}
		no, err := strconv.Atoi(strings.TrimSpace(rec[0]))
		if err != nil {
			return nil, fmt.Errorf("%w: 第 %d 行行次非法: %q", ErrBadDefinition, i+2, rec[0])
		}
		f, err := ParseFormula(cell(rec, 3))
		if err != nil {
			return nil, fmt.Errorf("%w: 行 %d 公式错误: %w", ErrBadDefinition, no, err)
		}
		d.Lines = append(d.Lines, &Line{
			No:      no,
			Side:    SideSingle,
			Name:    strings.TrimSpace(rec[1]),
			Type:    LineType(strings.TrimSpace(rec[2])),
			Formula: f,
			Note:    cell(rec, 4),
		})
	}
	return d.finish()
}

func cell(rec []string, i int) string {
	if i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}

// finish 校验定义自身的一致性。
func (d *Definition) finish() (*Definition, error) {
	if len(d.Lines) == 0 {
		return nil, fmt.Errorf("%w: %s 没有任何行", ErrBadDefinition, d.Code)
	}
	prev := 0
	for _, l := range d.Lines {
		if l.No <= prev {
			return nil, fmt.Errorf("%w: %s 行次不是升序（%d 之后是 %d）",
				ErrBadDefinition, d.Code, prev, l.No)
		}
		prev = l.No
		if l.Name == "" {
			return nil, fmt.Errorf("%w: %s 行 %d 缺少名称", ErrBadDefinition, d.Code, l.No)
		}
		switch l.Type {
		case LineItem, LineMemo, LineSubtotal, LineTotal:
		default:
			return nil, fmt.Errorf("%w: %s 行 %d 的行类型非法: %q",
				ErrBadDefinition, d.Code, l.No, l.Type)
		}
		// 只能引用前面的行：这样按行次顺序单向求值即可，无需拓扑排序，
		// 也天然排除了自环与循环引用。
		for _, ref := range l.Formula.Refs() {
			if _, ok := d.byNo[ref]; !ok && ref >= l.No {
				return nil, fmt.Errorf("%w: %s 行 %d 引用了后面的行 %d",
					ErrBadDefinition, d.Code, l.No, ref)
			}
		}
		d.byNo[l.No] = l
	}
	// 引用的行必须真实存在
	for _, l := range d.Lines {
		for _, ref := range l.Formula.Refs() {
			if _, ok := d.byNo[ref]; !ok {
				return nil, fmt.Errorf("%w: %s 行 %d 引用了不存在的行 %d",
					ErrBadDefinition, d.Code, l.No, ref)
			}
		}
	}
	return d, nil
}

// ---------------------------------------------------------------------------
// 求值
// ---------------------------------------------------------------------------

// Compute 按行次顺序求值，结果写回各行的 Value。
//
// 单向顺序求值是可行的，因为 finish 已强制公式只能引用**前面的行**
// （官方勾稽关系也都是这样写的：行20 = 行18−行19，行53 = 行47+行52）。
func (d *Definition) Compute(r Resolver) error {
	lines := make(map[int]money.Money, len(d.Lines))
	for _, l := range d.Lines {
		if !l.IsComputed() {
			l.Value = 0
			lines[l.No] = 0
			continue
		}
		v, err := l.Formula.Eval(r, lines)
		if err != nil {
			return fmt.Errorf("报表 %s 行 %d(%s) 求值失败: %w", d.Code, l.No, l.Name, err)
		}
		l.Value = v
		lines[l.No] = v
	}
	return nil
}

// ---------------------------------------------------------------------------
// 勾稽关系校验
// ---------------------------------------------------------------------------

// CheckIssue 描述一处勾稽关系不成立。
type CheckIssue struct {
	Left     string
	LeftVal  money.Money
	Right    string
	RightVal money.Money
	Diff     money.Money
	Fatal    bool
}

// String 返回可读描述。
func (c CheckIssue) String() string {
	return fmt.Sprintf("%s(%s) ≠ %s(%s)，差额 %s",
		c.Left, c.LeftVal, c.Right, c.RightVal, c.Diff)
}

// CheckBalanceSheet 校验资产负债表的官方勾稽关系。
//
// 这些等式直接来自财政部附录原文（见 docs/03 §5.1）。其中最要紧的是
// 行53 = 行30（资产总计 = 负债和所有者权益总计）——它不成立意味着账本身
// 有问题，必须显式告警。Frappe Books 完全没有这类检查，
// 报表数字错了也不会提示，用户只能自己拿计算器核对。
func CheckBalanceSheet(d *Definition) []CheckIssue {
	var issues []CheckIssue

	val := func(no int) (money.Money, bool) {
		l, ok := d.Line(no)
		if !ok {
			return 0, false
		}
		return l.Value, true
	}
	// sumEq 校验「行 no == 指定若干行之和」
	sumEq := func(no int, parts ...int) {
		got, ok := val(no)
		if !ok {
			return
		}
		var want money.Money
		for _, p := range parts {
			if v, ok := val(p); ok {
				want = want.Add(v)
			}
		}
		if got != want {
			issues = append(issues, CheckIssue{
				Left: fmt.Sprintf("行%d %s", no, lineName(d, no)), LeftVal: got,
				Right: "各明细行之和", RightVal: want,
				Diff: got.Sub(want), Fatal: false,
			})
		}
	}
	// diffEq 校验「行 no == 行 a − 行 b」
	diffEq := func(no, a, b int) {
		got, ok := val(no)
		if !ok {
			return
		}
		av, ok1 := val(a)
		bv, ok2 := val(b)
		if !ok1 || !ok2 {
			return
		}
		want := av.Sub(bv)
		if got != want {
			issues = append(issues, CheckIssue{
				Left: fmt.Sprintf("行%d %s", no, lineName(d, no)), LeftVal: got,
				Right: fmt.Sprintf("行%d−行%d", a, b), RightVal: want,
				Diff: got.Sub(want), Fatal: true,
			})
		}
	}
	// sameEq 校验「行 a == 行 b」
	sameEq := func(a, b int) {
		av, ok1 := val(a)
		bv, ok2 := val(b)
		if !ok1 || !ok2 || av == bv {
			return
		}
		issues = append(issues, CheckIssue{
			Left: fmt.Sprintf("行%d %s", a, lineName(d, a)), LeftVal: av,
			Right: fmt.Sprintf("行%d %s", b, lineName(d, b)), RightVal: bv,
			Diff: av.Sub(bv), Fatal: true,
		})
	}

	// ---- 官方勾稽关系，逐条照录（docs/03 §5.1）----
	sumEq(15, 1, 2, 3, 4, 5, 6, 7, 8, 9, 14)              // 流动资产合计
	diffEq(20, 18, 19)                                    // 固定资产账面价值
	sumEq(29, 16, 17, 20, 21, 22, 23, 24, 25, 26, 27, 28) // 非流动资产合计
	sumEq(30, 15, 29)                                     // 资产总计
	sumEq(41, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40)     // 流动负债合计
	sumEq(46, 42, 43, 44, 45)                             // 非流动负债合计
	sumEq(47, 41, 46)                                     // 负债合计
	sumEq(52, 48, 49, 50, 51)                             // 所有者权益合计
	sumEq(53, 47, 52)                                     // 负债和所有者权益总计

	// ★ 最关键的一条：资产总计必须等于负债和所有者权益总计
	sameEq(30, 53)

	return issues
}

// lineName 取行的名称，用于告警文案。
func lineName(d *Definition, no int) string {
	if l, ok := d.Line(no); ok {
		return l.Name
	}
	return ""
}

// CheckIncomeStatement 校验利润表的官方勾稽关系。
func CheckIncomeStatement(d *Definition) []CheckIssue {
	var issues []CheckIssue
	get := func(no int) (money.Money, bool) {
		l, ok := d.Line(no)
		if !ok {
			return 0, false
		}
		return l.Value, true
	}
	expect := func(desc string, got money.Money, want money.Money) {
		if got != want {
			issues = append(issues, CheckIssue{
				Left: desc, LeftVal: got, Right: "公式计算值", RightVal: want,
				Diff: got.Sub(want), Fatal: true,
			})
		}
	}

	// 行21 = 行1−行2−行3−行11−行14−行18+行20
	if v, ok := get(21); ok {
		var want money.Money
		for _, spec := range []struct {
			no  int
			sgn int
		}{{1, 1}, {2, -1}, {3, -1}, {11, -1}, {14, -1}, {18, -1}, {20, 1}} {
			if p, ok := get(spec.no); ok {
				if spec.sgn > 0 {
					want = want.Add(p)
				} else {
					want = want.Sub(p)
				}
			}
		}
		expect("行21 营业利润", v, want)
	}
	// 行30 = 行21+行22−行24
	if v, ok := get(30); ok {
		var want money.Money
		if p, ok := get(21); ok {
			want = want.Add(p)
		}
		if p, ok := get(22); ok {
			want = want.Add(p)
		}
		if p, ok := get(24); ok {
			want = want.Sub(p)
		}
		expect("行30 利润总额", v, want)
	}
	// 行32 = 行30−行31
	if v, ok := get(32); ok {
		var want money.Money
		if p, ok := get(30); ok {
			want = want.Add(p)
		}
		if p, ok := get(31); ok {
			want = want.Sub(p)
		}
		expect("行32 净利润", v, want)
	}
	return issues
}

// ---------------------------------------------------------------------------
// 账簿：科目余额表
// ---------------------------------------------------------------------------

// BalanceRow 是科目余额表的一行。
type BalanceRow struct {
	AccountCode string
	AccountName string
	Level       int
	IsLeaf      bool
	BalanceDir  account.BalanceDir

	OpeningDebit  money.Money // 期初借方余额
	OpeningCredit money.Money // 期初贷方余额
	PeriodDebit   money.Money // 本期借方发生额
	PeriodCredit  money.Money // 本期贷方发生额
	ClosingDebit  money.Money // 期末借方余额
	ClosingCredit money.Money // 期末贷方余额
}

// BalanceReport 是科目余额表。
//
// 与中国实务的三栏/六栏式余额表一致：期初借、期初贷、本期借、本期贷、
// 期末借、期末贷。**借贷分列而不是用正负数**，因为会计人员看的是
// 「这个科目挂在哪个方向」，用带符号的数字反而要心算。
type BalanceReport struct {
	Period period.Key
	Rows   []BalanceRow
}

// TotalsDebit/Credit 返回各列合计，用于核对「期初借 = 期初贷」这类等式。
func (r *BalanceReport) Totals() (obD, obC, pd, pc, cbD, cbC money.Money) {
	for _, row := range r.Rows {
		// ★ 只加**明细科目**行。
		//
		// 汇总科目行带的是其下级的合计（会计看余额表就是要一眼看到
		// 「管理费用这个月花了多少」，而不是一个个下级去加），
		// 于是把全部行加起来会把同一笔钱算上两遍、三遍，
		// 试算平衡当场就不平了。分录只可能落在明细科目上，
		// 所以「全部明细科目行之和」正是全账发生额。
		if !row.IsLeaf {
			continue
		}
		obD = obD.Add(row.OpeningDebit)
		obC = obC.Add(row.OpeningCredit)
		pd = pd.Add(row.PeriodDebit)
		pc = pc.Add(row.PeriodCredit)
		cbD = cbD.Add(row.ClosingDebit)
		cbC = cbC.Add(row.ClosingCredit)
	}
	return
}

// SplitBalance 把一个净额按余额方向拆成借/贷两列。
//
// 规则：净额（借−贷）为正则记借方，为负则记贷方（取绝对值）。
// 这正是中国报表「按方向分列」的基础。
func SplitBalance(net money.Money) (debit, credit money.Money) {
	if net.IsPositive() {
		return net, 0
	}
	if net.IsNegative() {
		return 0, net.Abs()
	}
	return 0, 0
}

// ---------------------------------------------------------------------------
// 账簿：往来余额表
// ---------------------------------------------------------------------------

// ContactBalanceRow 是往来单位余额表的一行。
type ContactBalanceRow struct {
	ContactID   int64
	ContactName string
	ContactKind string
	AccountCode string

	Opening money.Money
	Debit   money.Money
	Credit  money.Money
	Closing money.Money
}

// SortBalanceRows 按科目编码升序排列，便于输出稳定。
func SortBalanceRows(rows []BalanceRow) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].AccountCode < rows[j].AccountCode })
}

// Range 返回某期间的日期闭区间。
func Range(k period.Key) calendar.Range {
	from, _ := calendar.New(k.Year, k.Month, 1)
	to, _ := calendar.New(k.Year, k.Month, calendar.DaysInMonth(k.Year, k.Month))
	return calendar.Range{From: from, To: to}
}
