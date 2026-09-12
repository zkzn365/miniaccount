// Package cashflow 生成现金流量表（会小企 03 表）。
//
// # 编制方法
//
// 现金流量表有两种编法：
//
//	直接法：逐笔分析现金收支，按性质归类
//	间接法：从净利润出发，调整非现金项目
//
// 本包采用**直接法**，但归类是**规则驱动**的：
// 对每一笔涉及现金/银行存款的分录，看它在同一张凭证里的**对方科目**，
// 按对方科目的性质归入经营/投资/筹资活动。
//
// 例如「借 管理费用—工资 / 贷 银行存款」，对方科目是费用类，
// 归入「经营活动—支付给职工以及为职工支付的现金」。
//
// # 这个方法的边界
//
// 规则驱动意味着归类依赖科目设置。用户把某笔支出记到哪个科目，
// 就决定了它出现在现金流量表的哪一行 —— 这与手工编表时的判断过程一致，
// 但不会有「软件自己猜」的不确定性。
//
// 无法归类的分录会被放进「其他」并**列出来**，而不是静默丢弃：
// 现金流量表加总对不上时，会计需要知道是哪几笔没归好。
package cashflow

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// 现金流量表相关错误。
var (
	ErrNotBalanced = errors.New("cashflow: 现金及现金等价物净增加额与期初期末不符")
)

// Activity 是现金流量的大类。
type Activity string

// 三大活动。
const (
	Operating Activity = "operating" // 经营活动
	Investing Activity = "investing" // 投资活动
	Financing Activity = "financing" // 筹资活动
)

// Label 返回中文名。
func (a Activity) Label() string {
	switch a {
	case Operating:
		return "经营活动"
	case Investing:
		return "投资活动"
	case Financing:
		return "筹资活动"
	default:
		return string(a)
	}
}

// Line 是现金流量表的一个行项目。
type Line struct {
	// No 是官方行次（会小企 03 表）。
	No int
	// Activity 是所属大类。
	Activity Activity
	// Name 是项目名称。
	Name string
	// Inflow 为真表示这是「收到的现金」行，否则是「支付的现金」行。
	Inflow bool
	// Amount 是**带符号**的现金流：流入为正、流出为负。
	//
	// 全表统一用带符号金额，各级小计与净额就都是简单相加，
	// 不会出现「这一层该加、那一层该减」的方向混乱。
	// 界面展示流出行时用 Display() 取绝对值 —— 行标签本身已经
	// 写明了「支付的…」，再显示负号会让人以为是红字冲销。
	Amount money.Money
	// MatchedAccounts 是归入本行的对方科目前缀。
	MatchedAccounts []string
	// IsSubtotal 为真表示这是小计/净额行。
	IsSubtotal bool
}

// 官方行次（会小企 03 表，简化版）。
const (
	lineSalesCash         = 1  // 销售产成品、商品、提供劳务收到的现金
	lineTaxRefund         = 2  // 收到的税费返还
	lineOtherOperatingIn  = 3  // 收到其他与经营活动有关的现金
	lineOperatingInTotal  = 4  // 经营活动现金流入小计
	lineBuyCash           = 5  // 购买原材料、商品、接受劳务支付的现金
	lineStaffCash         = 6  // 支付的职工薪酬
	lineTaxPaid           = 7  // 支付的各项税费
	lineOtherOperatingOut = 8  // 支付其他与经营活动有关的现金
	lineOperatingOutTotal = 9  // 经营活动现金流出小计
	lineOperatingNet      = 10 // 经营活动产生的现金流量净额

	lineInvestInTotal  = 14 // 投资活动现金流入小计
	lineInvestOutTotal = 18 // 投资活动现金流出小计
	lineInvestNet      = 19 // 投资活动产生的现金流量净额

	lineFinanceInTotal  = 26 // 筹资活动现金流入小计
	lineFinanceOutTotal = 32 // 筹资活动现金流出小计
	lineFinanceNet      = 33 // 筹资活动产生的现金流量净额

	lineNetIncrease = 34 // 现金及现金等价物净增加额
	lineOpeningCash = 35 // 加：期初现金余额
	lineClosingCash = 36 // 期末现金余额
)

// Rule 把某个对方科目映射到现金流量表的某一行。
type Rule struct {
	// AccountPrefix 是对方科目的编码前缀，如 "1122" 表示应收账款及其下级。
	AccountPrefix string
	// LineNo 是目标行次。
	LineNo int
	// Flow 限定适用的现金流动方向："" 不限、"in" 仅流入、"out" 仅流出。
	//
	// 之所以需要这个限定：同一个「应交税费」科目在两种场景下含义完全不同 ——
	//
	//	收到含税货款（现金流入）→ 销项税是**销售收款的一部分**
	//	向税局缴税（现金流出）  → 才是「支付的各项税费」
	//
	// 只按科目前缀归类会把收到的销项税算成税费支出，
	// 使经营活动净额凭空多出一倍税额。
	Flow string
}

// Classifier 是归类规则集合。
type Classifier struct {
	rules []Rule
}

// DefaultClassifier 返回按《小企业会计准则》常见科目设置的默认归类规则。
//
// 规则按前缀长度从长到短匹配，因此更具体的规则优先。
func DefaultClassifier() *Classifier {
	return &Classifier{rules: []Rule{
		// ---- 经营活动 ----
		// 收入与应收 → 销售商品、提供劳务收到的现金
		{AccountPrefix: "5001", LineNo: lineSalesCash}, {AccountPrefix: "5051", LineNo: lineSalesCash},
		{AccountPrefix: "1122", LineNo: lineSalesCash}, {AccountPrefix: "1121", LineNo: lineSalesCash},
		{AccountPrefix: "2203", LineNo: lineSalesCash}, // 预收账款：收到的预收款也是销售回款
		// 成本、存货、应付 → 购买商品、接受劳务支付的现金
		{AccountPrefix: "5401", LineNo: lineBuyCash}, {AccountPrefix: "5402", LineNo: lineBuyCash},
		{AccountPrefix: "1401", LineNo: lineBuyCash}, {AccountPrefix: "1402", LineNo: lineBuyCash}, {AccountPrefix: "1403", LineNo: lineBuyCash},
		{AccountPrefix: "1405", LineNo: lineBuyCash}, {AccountPrefix: "1408", LineNo: lineBuyCash}, {AccountPrefix: "1411", LineNo: lineBuyCash},
		{AccountPrefix: "2202", LineNo: lineBuyCash}, {AccountPrefix: "1123", LineNo: lineBuyCash}, {AccountPrefix: "2201", LineNo: lineBuyCash},
		{AccountPrefix: "4001", LineNo: lineBuyCash}, {AccountPrefix: "4101", LineNo: lineBuyCash},
		// 职工薪酬 → 支付的职工薪酬
		{AccountPrefix: "2211", LineNo: lineStaffCash},
		{AccountPrefix: "5601", LineNo: lineOtherOperatingOut}, // 销售费用（其中工资部分通常也已归入薪酬）
		{AccountPrefix: "5602", LineNo: lineOtherOperatingOut}, // 管理费用
		{AccountPrefix: "5603", LineNo: lineOtherOperatingOut}, // 财务费用（手续费）
		{AccountPrefix: "560207", LineNo: lineOtherOperatingOut},
		// 税费。★ 必须按现金方向区分：
		//   收到含税货款 → 销项税属于销售收款（准则要求销售收到的现金按含税口径）
		//   向税局缴税   → 才是「支付的各项税费」
		{AccountPrefix: "2221", LineNo: lineSalesCash, Flow: "in"},
		{AccountPrefix: "2221", LineNo: lineTaxPaid, Flow: "out"},
		{AccountPrefix: "5403", LineNo: lineTaxPaid, Flow: "out"}, {AccountPrefix: "5801", LineNo: lineTaxPaid, Flow: "out"},
		// 其他经营
		{AccountPrefix: "1221", LineNo: lineOtherOperatingIn}, {AccountPrefix: "2241", LineNo: lineOtherOperatingIn},
		{AccountPrefix: "224102", LineNo: lineOtherOperatingOut}, // 报销付款
		{AccountPrefix: "5301", LineNo: lineOtherOperatingIn},    // 营业外收入
		{AccountPrefix: "5711", LineNo: lineOtherOperatingOut},   // 营业外支出

		// ---- 投资活动 ----
		{AccountPrefix: "1601", LineNo: lineInvestOutTotal}, {AccountPrefix: "1602", LineNo: lineInvestOutTotal},
		{AccountPrefix: "1604", LineNo: lineInvestOutTotal}, {AccountPrefix: "1605", LineNo: lineInvestOutTotal},
		{AccountPrefix: "1606", LineNo: lineInvestOutTotal}, {AccountPrefix: "1701", LineNo: lineInvestOutTotal},
		{AccountPrefix: "1702", LineNo: lineInvestOutTotal}, {AccountPrefix: "1801", LineNo: lineInvestOutTotal},
		{AccountPrefix: "1501", LineNo: lineInvestOutTotal}, {AccountPrefix: "1511", LineNo: lineInvestOutTotal},
		{AccountPrefix: "1101", LineNo: lineInvestInTotal}, {AccountPrefix: "5111", LineNo: lineInvestInTotal},
		{AccountPrefix: "1131", LineNo: lineInvestInTotal}, {AccountPrefix: "1132", LineNo: lineInvestInTotal},

		// ---- 筹资活动 ----
		{AccountPrefix: "2001", LineNo: lineFinanceInTotal}, {AccountPrefix: "2501", LineNo: lineFinanceInTotal},
		{AccountPrefix: "3001", LineNo: lineFinanceInTotal}, {AccountPrefix: "3002", LineNo: lineFinanceInTotal},
		{AccountPrefix: "2231", LineNo: lineFinanceOutTotal}, {AccountPrefix: "2232", LineNo: lineFinanceOutTotal},
		{AccountPrefix: "2701", LineNo: lineFinanceOutTotal},
	}}
}

// WithRule 追加一条归类规则。
func (c *Classifier) WithRule(prefix string, lineNo int) *Classifier {
	c.rules = append(c.rules, Rule{AccountPrefix: prefix, LineNo: lineNo})
	return c
}

// Classify 返回某对方科目应归入的行次；无法归类时返回 0。
//
// 匹配「最长前缀优先」：这样 "224102"（其他应付款—员工）会优先于 "2241" 命中，
// 使得报销付款能归到经营支出而不是笼统的「其他」。
func (c *Classifier) Classify(accountCode string) int {
	return c.ClassifyFlow(accountCode, 0)
}

// ClassifyFlow 按对方科目与现金流动方向归类。
//
// flow > 0 表示现金流入、< 0 表示流出、= 0 表示不限。
// 先按「最长前缀 + 方向匹配」选规则；没有方向匹配的规则时，
// 回退到无方向限定的规则。
func (c *Classifier) ClassifyFlow(accountCode string, flow money.Money) int {
	dir := ""
	if flow.IsPositive() {
		dir = "in"
	} else if flow.IsNegative() {
		dir = "out"
	}

	best, bestLen := 0, 0
	fallback, fallbackLen := 0, 0
	for _, r := range c.rules {
		if !strings.HasPrefix(accountCode, r.AccountPrefix) {
			continue
		}
		n := len(r.AccountPrefix)
		switch r.Flow {
		case "":
			if n > fallbackLen {
				fallback, fallbackLen = r.LineNo, n
			}
		default:
			if r.Flow == dir && n > bestLen {
				best, bestLen = r.LineNo, n
			}
		}
	}
	if best != 0 {
		return best
	}
	return fallback
}

// ---------------------------------------------------------------------------
// 输入数据
// ---------------------------------------------------------------------------

// CashEntry 是一笔涉及现金/银行存款的分录，及其对应的对方科目。
type CashEntry struct {
	// Date 是业务日期。
	Date calendar.Date
	// VoucherNo 是凭证号，便于追溯。
	VoucherNo string
	// CashAccount 是现金/银行科目编码。
	CashAccount string
	// Amount 是现金变动金额（借−贷）：正为流入，负为流出。
	Amount money.Money
	// CounterAccounts 是同一凭证里的对方科目编码。
	//
	// 一笔现金分录可能对应多个对方科目（如收款同时涉及收入与销项税），
	// 因此这里是列表；分摊金额按对方科目的金额比例分配。
	CounterAccounts []CounterAccount
	// Summary 是摘要，便于展示未归类项。
	Summary string
}

// CounterAccount 是对方科目及其在该笔业务中的金额。
type CounterAccount struct {
	Code   string
	Amount money.Money // 带符号：借为正、贷为负
}

// Unclassified 是一条没能归类的现金分录。
type Unclassified struct {
	Date      calendar.Date
	VoucherNo string
	Amount    money.Money
	Summary   string
	// CounterAccounts 是它的全部对方科目，便于用户判断该加哪条规则。
	CounterAccounts []string
}

// ---------------------------------------------------------------------------
// 报表
// ---------------------------------------------------------------------------

// Statement 是现金流量表。
type Statement struct {
	From, To calendar.Date

	// Lines 是按行次升序的行项目。
	Lines []Line

	// OpeningCash 是期初现金余额。
	OpeningCash money.Money
	// ClosingCash 是期末现金余额。
	ClosingCash money.Money

	// Unclassified 是未能归类的分录。
	//
	// **刻意暴露而不是静默丢弃**：现金流量表加总对不上时，
	// 会计需要知道是哪几笔没归好、该补哪条规则。
	Unclassified      []Unclassified
	UnclassifiedTotal money.Money

	// ActualClosingCash 是**科目余额表**上的期末现金余额。
	//
	// 它是独立于本表推算过程的一条交叉验证：两者不等，说明有现金
	// 分录没被纳入（例如新增了「其他货币资金」明细却没登记为
	// 现金等价物）。为 nil 表示调用方没有提供这项校验。
	ActualClosingCash *money.Money
}

// CashMismatch 返回推算期末现金与账面期末现金的差额。
//
// 返回 0 表示对得上（或没有提供账面数）。
func (s *Statement) CashMismatch() money.Money {
	if s.ActualClosingCash == nil {
		return 0
	}
	return s.ClosingCash.Sub(*s.ActualClosingCash)
}

// Display 返回该行用于展示的金额：流出行取绝对值。
func (l Line) Display() money.Money {
	if l.IsSubtotal || l.Inflow {
		return l.Amount
	}
	return l.Amount.Abs()
}

// Line 按行次取行。
func (s *Statement) Line(no int) (Line, bool) {
	for _, l := range s.Lines {
		if l.No == no {
			return l, true
		}
	}
	return Line{}, false
}

// AmountOf 返回某行金额（不存在则为 0）。
func (s *Statement) AmountOf(no int) money.Money {
	if l, ok := s.Line(no); ok {
		return l.Amount
	}
	return 0
}

// Check 校验现金流量表自身的勾稽关系。
//
//	净增加额 = 经营净额 + 投资净额 + 筹资净额
//	期末现金 = 期初现金 + 净增加额
func (s *Statement) Check() []error {
	var errs []error

	net := s.AmountOf(lineNetIncrease)
	sum := s.AmountOf(lineOperatingNet).
		Add(s.AmountOf(lineInvestNet)).
		Add(s.AmountOf(lineFinanceNet))
	if net != sum {
		errs = append(errs, fmt.Errorf(
			"现金及现金等价物净增加额 %s ≠ 经营 %s + 投资 %s + 筹资 %s = %s",
			net, s.AmountOf(lineOperatingNet), s.AmountOf(lineInvestNet),
			s.AmountOf(lineFinanceNet), sum))
	}

	if want := s.OpeningCash.Add(net); s.ClosingCash != want {
		errs = append(errs, fmt.Errorf(
			"期末现金 %s ≠ 期初现金 %s + 净增加额 %s = %s",
			s.ClosingCash, s.OpeningCash, net, want))
	}
	return errs
}

// ---------------------------------------------------------------------------
// 生成
// ---------------------------------------------------------------------------

// Input 是生成现金流量表所需的输入。
type Input struct {
	From, To calendar.Date
	// CashEntries 是全部涉及现金/银行的分录（含对方科目）。
	CashEntries []CashEntry
	// OpeningCash 是期初现金及现金等价物余额。
	OpeningCash money.Money
	// OpeningUnreconciled 是「期初现金 + 本期净流量」与「期末实际余额」
	// 之间的差额中，属于期初的部分（正常情况下为 0）。
	// 这里保留字段是为了将来支持「上期未归类」的追溯调整。
	OpeningUnreconciled money.Money
}

// Build 生成现金流量表。
//
// 归类逻辑：
//
//  1. 对每笔现金分录，按对方科目前缀查规则，得到目标行次
//  2. 金额按对方科目的比例分摊到各行（一笔收款可能同时涉及收入与销项税）
//  3. 无法归类的整笔记入 Unclassified，不参与行项目加总
//
// 之所以按对方科目**比例分摊**而不是把整笔金额记到某一类：
// 一笔现金可能同时对应多个对方科目（如付款同时冲应付账款与其他应付款），
// 各科目可能归入不同的行。按比例分摊能保证每个行次都拿到它应得的部分，
// 且分摊不丢分。
//
// ★ 收到含税货款时，销项税与收入同归「销售…收到的现金」：
// 《小企业会计准则》要求该行按**含税**口径列报，销项税是随货款
// 一起收到的现金，不是「支付的各项税费」。
// 归类规则用 Flow 字段按现金方向区分这两种情形，
// 详见 DefaultClassifier 中「应交税费」的两条规则。
func Build(in Input, cl *Classifier) (*Statement, error) {
	if cl == nil {
		cl = DefaultClassifier()
	}
	if !in.From.Valid() || !in.To.Valid() || in.To.Before(in.From) {
		return nil, fmt.Errorf("cashflow: 期间非法 %s ~ %s", in.From, in.To)
	}

	amounts := map[int]money.Money{}
	var unclassified []Unclassified
	var unclassifiedTotal money.Money
	var netCash money.Money

	for _, e := range in.CashEntries {
		netCash = netCash.Add(e.Amount)

		if len(e.CounterAccounts) == 0 {
			unclassified = append(unclassified, Unclassified{
				Date: e.Date, VoucherNo: e.VoucherNo, Amount: e.Amount,
				Summary: e.Summary,
			})
			unclassifiedTotal = unclassifiedTotal.Add(e.Amount)
			continue
		}

		// 把现金变动额按对方科目的金额比例分摊
		allocated := allocateByCounter(e.Amount, e.CounterAccounts)

		var matched money.Money
		var unmatchedCodes []string
		for i, ca := range e.CounterAccounts {
			part := allocated[i]
			lineNo := cl.ClassifyFlow(ca.Code, e.Amount)
			if lineNo == 0 {
				unmatchedCodes = append(unmatchedCodes, ca.Code)
				continue
			}
			amounts[lineNo] = amounts[lineNo].Add(part)
			matched = matched.Add(part)
		}

		// 部分或全部对方科目没归类时，整笔记为未归类，
		// 而不是只把已归类的部分算进去 —— 半截的数字比没有更糟。
		if len(unmatchedCodes) > 0 {
			unclassified = append(unclassified, Unclassified{
				Date: e.Date, VoucherNo: e.VoucherNo, Amount: e.Amount,
				Summary: e.Summary, CounterAccounts: unmatchedCodes,
			})
			unclassifiedTotal = unclassifiedTotal.Add(e.Amount)
			// 回滚已归类部分
			for i, ca := range e.CounterAccounts {
				if ln := cl.ClassifyFlow(ca.Code, e.Amount); ln != 0 {
					amounts[ln] = amounts[ln].Sub(allocated[i])
				}
			}
			_ = matched
		}
	}

	st := &Statement{
		From: in.From, To: in.To,
		OpeningCash:       in.OpeningCash,
		ClosingCash:       in.OpeningCash.Add(netCash),
		Unclassified:      unclassified,
		UnclassifiedTotal: unclassifiedTotal,
	}
	// ★ 期初/期末现金必须同时写进「行项目」，而不只是结构体字段。
	//
	// 官方表的第 35、36 行就是这两个数。只在字段里存着，
	// 按行次渲染的界面与导出会得到两行 0 —— 而右下角的期末现金
	// 又是对的，看上去像是同一张表自相矛盾。
	amounts[lineOpeningCash] = st.OpeningCash
	amounts[lineClosingCash] = st.ClosingCash
	st.Lines = buildLines(amounts)
	sort.Slice(st.Unclassified, func(i, j int) bool {
		return st.Unclassified[i].Date.Before(st.Unclassified[j].Date)
	})
	return st, nil
}

// allocateByCounter 把现金变动额按对方科目的金额比例分摊。
//
// 用最大余数法保证各份之和恰好等于原额（分毫不丢）。
// 对方科目金额全为零时退化为等分。
func allocateByCounter(amount money.Money, counters []CounterAccount) []money.Money {
	weights := make([]int64, len(counters))
	var total int64
	for i, c := range counters {
		w := int64(c.Amount.Abs())
		weights[i] = w
		total += w
	}
	if total == 0 {
		return amount.Allocate(make([]int64, len(counters)))
	}
	return amount.Allocate(weights)
}

// buildLines 把行次 → 金额的映射整理成报表行。
//
// # 符号约定
//
// **所有行金额都是带符号的现金流**：流入为正、流出为负。
// 好处是各级净额都是简单相加，不会出现「小计该加还是该减」的混乱：
//
//	经营活动净额 = 流入小计 + 流出小计   （流出小计本身为负）
//	净增加额     = 经营 + 投资 + 筹资
//
// 界面上把流出行的金额取绝对值展示（行标签已写明「支付的…」），
// 但数据层保持带符号，避免来回取反算错。
func buildLines(amounts map[int]money.Money) []Line {
	byNo := map[int]money.Money{}
	for no, v := range amounts {
		byNo[no] = v
	}

	// 经营活动：明细行本身即带符号现金流，小计直接累加
	byNo[lineOperatingInTotal] = sumOf(byNo, lineSalesCash, lineTaxRefund, lineOtherOperatingIn)
	byNo[lineOperatingOutTotal] = sumOf(byNo, lineBuyCash, lineStaffCash,
		lineTaxPaid, lineOtherOperatingOut)
	byNo[lineOperatingNet] = byNo[lineOperatingInTotal].Add(byNo[lineOperatingOutTotal])

	// 投资与筹资活动：规则直接映射到小计行，没有更细的明细行
	byNo[lineInvestNet] = byNo[lineInvestInTotal].Add(byNo[lineInvestOutTotal])
	byNo[lineFinanceNet] = byNo[lineFinanceInTotal].Add(byNo[lineFinanceOutTotal])

	byNo[lineNetIncrease] = byNo[lineOperatingNet].
		Add(byNo[lineInvestNet]).Add(byNo[lineFinanceNet])

	out := make([]Line, 0, len(lineDefs()))
	for _, d := range lineDefs() {
		l := d
		l.Amount = byNo[l.No]
		out = append(out, l)
	}
	return out
}

func sumOf(m map[int]money.Money, nos ...int) money.Money {
	var t money.Money
	for _, n := range nos {
		t = t.Add(m[n])
	}
	return t
}

// lineDefs 返回现金流量表的行定义（按官方行次）。
func lineDefs() []Line {
	return []Line{
		{No: lineSalesCash, Activity: Operating, Name: "销售产成品、商品、提供劳务收到的现金", Inflow: true},
		{No: lineTaxRefund, Activity: Operating, Name: "收到的税费返还", Inflow: true},
		{No: lineOtherOperatingIn, Activity: Operating, Name: "收到其他与经营活动有关的现金", Inflow: true},
		{No: lineOperatingInTotal, Activity: Operating, Name: "经营活动现金流入小计", Inflow: true, IsSubtotal: true},
		{No: lineBuyCash, Activity: Operating, Name: "购买原材料、商品、接受劳务支付的现金", Inflow: false},
		{No: lineStaffCash, Activity: Operating, Name: "支付的职工薪酬", Inflow: false},
		{No: lineTaxPaid, Activity: Operating, Name: "支付的各项税费", Inflow: false},
		{No: lineOtherOperatingOut, Activity: Operating, Name: "支付其他与经营活动有关的现金", Inflow: false},
		{No: lineOperatingOutTotal, Activity: Operating, Name: "经营活动现金流出小计", Inflow: false, IsSubtotal: true},
		{No: lineOperatingNet, Activity: Operating, Name: "经营活动产生的现金流量净额", IsSubtotal: true},

		{No: lineInvestInTotal, Activity: Investing, Name: "投资活动现金流入小计", Inflow: true, IsSubtotal: true},
		{No: lineInvestOutTotal, Activity: Investing, Name: "投资活动现金流出小计", Inflow: false, IsSubtotal: true},
		{No: lineInvestNet, Activity: Investing, Name: "投资活动产生的现金流量净额", IsSubtotal: true},

		{No: lineFinanceInTotal, Activity: Financing, Name: "筹资活动现金流入小计", Inflow: true, IsSubtotal: true},
		{No: lineFinanceOutTotal, Activity: Financing, Name: "筹资活动现金流出小计", Inflow: false, IsSubtotal: true},
		{No: lineFinanceNet, Activity: Financing, Name: "筹资活动产生的现金流量净额", IsSubtotal: true},

		{No: lineNetIncrease, Activity: "", Name: "现金及现金等价物净增加额", IsSubtotal: true},
		{No: lineOpeningCash, Activity: "", Name: "加：期初现金余额"},
		{No: lineClosingCash, Activity: "", Name: "期末现金余额", IsSubtotal: true},
	}
}

// CashAccountRoots 返回视为「现金及现金等价物」的科目前缀。
//
// 按《小企业会计准则》，现金等价物是期限短、流动性强、
// 易于转换为已知金额现金、价值变动风险很小的投资。
// 实务中小微企业通常只把库存现金与银行存款计入。
var CashAccountRoots = []string{"1001", "1002", "1012"}

// IsCashAccount 报告某科目是否属于现金及现金等价物。
func IsCashAccount(code string) bool {
	for _, root := range CashAccountRoots {
		if strings.HasPrefix(code, root) {
			return true
		}
	}
	return false
}

// Summary 返回报表的一句话概览。
func (s *Statement) Summary() string {
	return fmt.Sprintf("%s ~ %s：经营净额 %s，投资净额 %s，筹资净额 %s，净增加 %s，期末现金 %s",
		s.From, s.To,
		s.AmountOf(lineOperatingNet), s.AmountOf(lineInvestNet),
		s.AmountOf(lineFinanceNet), s.AmountOf(lineNetIncrease), s.ClosingCash)
}

// VerifyClosingCash 用实际科目余额交叉验证期末现金。
//
// 与 Build 推算出的 ClosingCash 比对，不一致说明有现金分录没被纳入
// （例如漏了某个银行明细科目）。
func VerifyClosingCash(st *Statement, actual money.Money) error {
	if st.ClosingCash != actual {
		return fmt.Errorf("%w: 推算期末现金 %s，实际科目余额 %s（差 %s）",
			ErrNotBalanced, st.ClosingCash, actual, st.ClosingCash.Sub(actual))
	}
	return nil
}

// AccountsFor 返回归入某行的对方科目前缀，便于界面提示。
func (cl *Classifier) AccountsFor(lineNo int) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range cl.rules {
		if r.LineNo != lineNo || seen[r.AccountPrefix] {
			continue
		}
		seen[r.AccountPrefix] = true
		out = append(out, r.AccountPrefix)
	}
	sort.Strings(out)
	return out
}

var _ = account.RootAsset
