// Package workpaper 是审计底稿的领域逻辑。
//
// # 包名为什么叫 workpaper 而不是 audit
//
// 因为 `internal/domain/audit` 已经存在了，它是**操作日志**
// （财政部《企业会计信息化工作规范》要求的那个审计日志）。
// 两个「audit」在中文里都叫审计，在代码里必须分开 ——
// 一个是「谁在什么时候改了什么」，一个是「这个数该不该调」。
//
// # 为什么一个记账软件要有这个
//
// 不是因为「别人有我们也要有」。是因为**期末复核与审计调整是同一件事**：
// 拿账面的数，找出该改的地方，改掉或者记下来。
// 事务所做的是法定那一遍，老板自己（或代账会计）做的是每月一遍 ——
// 前者要出报告，后者只要「这个数对不对」。
//
// 所以这里的产出不是一份文档，是三张**能算的表**：
//
//	重要性水平表   超过多少的错报需要处理（一个金额门槛，不是一句形容词）
//	审定表         账面数 + 调整 = 审定数，逐科目
//	未更正错报汇总 登记了但没入账的调整，合计与门槛比
//
// # 一条必须守住的语义
//
// **已入账的调整不再计入审定数。**
//
//	审定数 = 账面数 + **未入账**的调整
//
// 已入账的调整已经体现在账面数里了，再加一次就是重复计算。
// 而这个错误的表现很阴：审定数与账面数的差额刚好是调整额的两倍，
// 看着像调对了，实际调重了。
package workpaper

import (
	"errors"
	"fmt"
	"strings"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

// 百万分比。
const ppmScale = 1_000_000

// ---------------------------------------------------------------------------
// 重要性水平
// ---------------------------------------------------------------------------

// Benchmark 是确定重要性水平所用的基准。
//
// 选哪个基准是有讲究的：盈利稳定的企业用利润总额，亏损或微利用营业收入，
// 资产密集型企业用资产总额。软件不该替用户决定，但要给出各自的常用比例，
// 并把他选的那个原样写进底稿 —— 底稿上要能答出「这个数是怎么来的」。
type Benchmark string

// 基准类型。
const (
	BenchmarkAssets  Benchmark = "assets"  // 资产总额
	BenchmarkRevenue Benchmark = "revenue" // 营业收入
	BenchmarkProfit  Benchmark = "profit"  // 利润总额
	BenchmarkExpense Benchmark = "expense" // 费用总额
)

// Benchmarks 返回全部基准（界面下拉用）。
func Benchmarks() []Benchmark {
	return []Benchmark{BenchmarkAssets, BenchmarkRevenue, BenchmarkProfit, BenchmarkExpense}
}

// Label 返回中文名。
func (b Benchmark) Label() string {
	switch b {
	case BenchmarkAssets:
		return "资产总额"
	case BenchmarkRevenue:
		return "营业收入"
	case BenchmarkProfit:
		return "利润总额"
	case BenchmarkExpense:
		return "费用总额"
	default:
		return string(b)
	}
}

// Valid 报告基准是否可识别。
func (b Benchmark) Valid() bool {
	for _, x := range Benchmarks() {
		if x == b {
			return true
		}
	}
	return false
}

// DefaultRatePPM 是该基准的常用比例。
//
// ★ 这只是**起点**，不是标准答案。
//
// 实务里 0.5%~5% 都有人用，比例要结合企业规模、行业、报表使用人的
// 需求来判断。给一个常用值是为了让用户不必凭空想一个数，
// 但底稿上必须把「基准 × 比例 = 重要性」这个算式写出来 ——
// 换一个比例是专业判断，改一个数不是。
func (b Benchmark) DefaultRatePPM() int64 {
	switch b {
	case BenchmarkAssets:
		return 5_000 // 0.5%
	case BenchmarkRevenue:
		return 10_000 // 1%
	case BenchmarkProfit:
		return 50_000 // 5%
	case BenchmarkExpense:
		return 10_000 // 1%
	default:
		return 10_000
	}
}

// 两个派生门槛的默认比例。
//
// 实际执行重要性（通常取整体的 50%~75%）：留出余地，
// 因为审计中发现的问题汇总起来还可能往上加。
// 明显微小错报临界值（通常取整体的 5%）：低于它的错报不必累积。
const (
	DefaultPerformancePPM = 600_000 // 60%
	DefaultTrivialPPM     = 50_000  // 5%
)

// Materiality 是一个期间的审计重要性水平。
type Materiality struct {
	Period period.Key
	// Benchmark 是选定的基准。
	Benchmark Benchmark
	// BenchmarkAmount 是基准金额（由账套取数或手工填）。
	BenchmarkAmount money.Money
	// RatePPM 是整体重要性的比例。
	RatePPM int64
	// PerformancePPM / TrivialPPM 是派生两个门槛的比例。
	PerformancePPM int64
	TrivialPPM     int64
	// Note 是判断说明（为什么选这个基准、这个比例）。
	Note string
}

// NewMateriality 用某个基准的常用比例建一份默认的重要性水平。
func NewMateriality(k period.Key, b Benchmark, amount money.Money) Materiality {
	return Materiality{
		Period: k, Benchmark: b, BenchmarkAmount: amount,
		RatePPM:        b.DefaultRatePPM(),
		PerformancePPM: DefaultPerformancePPM,
		TrivialPPM:     DefaultTrivialPPM,
	}
}

// Materiality 相关错误。
var (
	ErrBadBenchmark = errors.New("workpaper: 基准类型不合法")
	ErrBadRate      = errors.New("workpaper: 比例必须在 0% 与 100% 之间")
	ErrNoBenchmark  = errors.New("workpaper: 基准金额必须大于零")
)

// Validate 检查这份重要性水平能不能用。
func (m Materiality) Validate() error {
	if !m.Benchmark.Valid() {
		return fmt.Errorf("%w: %q", ErrBadBenchmark, m.Benchmark)
	}
	if m.BenchmarkAmount <= 0 {
		return fmt.Errorf("%w（当前 %s）—— 基准金额为零时算出来的重要性是零，"+
			"等于任何错报都要处理，那不是一个门槛", ErrNoBenchmark, m.BenchmarkAmount)
	}
	if m.RatePPM <= 0 || m.RatePPM >= ppmScale {
		return fmt.Errorf("%w（整体重要性 %.4f%%）", ErrBadRate, m.Rate()*100)
	}
	if m.PerformancePPM <= 0 || m.PerformancePPM > ppmScale {
		return fmt.Errorf("%w（实际执行重要性 %.4f%%）", ErrBadRate, m.PerformanceRate()*100)
	}
	if m.TrivialPPM < 0 || m.TrivialPPM >= ppmScale {
		return fmt.Errorf("%w（明显微小错报 %.4f%%）", ErrBadRate, m.TrivialRate()*100)
	}
	// ★ 整体重要性被舍入成 0 —— 门槛为零等于「任何错报都要处理」，
	// 那不是门槛。真实场景：基准金额填了个很小的数（比如误填了「1」），
	// 或者比例填了 0.0001%。不拦的话，底稿上会出现一个 0.00 的
	// 「整体重要性」，而后面所有比较都变成「全都超了」。
	if m.Overall() <= 0 {
		return fmt.Errorf("整体重要性算出来是 %s（基准 %s × %s）—— "+
			"门槛为零等于任何错报都要处理，那不是门槛。"+
			"请检查基准金额与比例是不是填错了",
			m.Overall(), m.BenchmarkAmount, Percent(m.RatePPM))
	}
	// ★ 两个派生门槛也不能被舍入成 0。
	//
	// 比例合法（>0）不等于门槛非零：比例填 1ppm 时，
	// 实际执行重要性与明显微小都会算成 0.00，而 0 就不是门槛 ——
	// 「低于临界值的错报不必累积」会变成「每一笔都要累积」。
	if m.Performance() <= 0 {
		return fmt.Errorf("实际执行重要性算出来是 %s（整体 %s × %s）—— "+
			"门槛为零就不是门槛，请调大比例",
			m.Performance(), m.Overall(), Percent(m.PerformancePPM))
	}
	if m.Trivial() <= 0 {
		return fmt.Errorf("明显微小错报临界值算出来是 %s（整体 %s × %s）—— "+
			"低于它的错报不必累积，临界值为零等于每一笔都要累积，请调大比例",
			m.Trivial(), m.Overall(), Percent(m.TrivialPPM))
	}
	if m.Trivial() >= m.Overall() {
		return fmt.Errorf("明显微小错报临界值 %s 不该大于等于整体重要性 %s —— "+
			"前者是「不必累积」的下限，后者是「必须处理」的上限",
			m.Trivial(), m.Overall())
	}
	return nil
}

// Rate 返回整体重要性比例（小数）。
func (m Materiality) Rate() float64 { return float64(m.RatePPM) / ppmScale }

// PerformanceRate 返回实际执行重要性比例。
func (m Materiality) PerformanceRate() float64 { return float64(m.PerformancePPM) / ppmScale }

// TrivialRate 返回明显微小错报比例。
func (m Materiality) TrivialRate() float64 { return float64(m.TrivialPPM) / ppmScale }

// Overall 是整体重要性水平：基准 × 比例。
func (m Materiality) Overall() money.Money {
	return money.MulDiv(m.BenchmarkAmount, m.RatePPM, ppmScale)
}

// Performance 是实际执行重要性水平。
func (m Materiality) Performance() money.Money {
	return money.MulDiv(m.Overall(), m.PerformancePPM, ppmScale)
}

// Trivial 是明显微小错报临界值。
func (m Materiality) Trivial() money.Money {
	return money.MulDiv(m.Overall(), m.TrivialPPM, ppmScale)
}

// Explain 给出算式逐行说明。
//
// ★ 底稿上必须能答出「这个数怎么来的」。
// 只给一个金额的重要性水平是没法复核的 —— 复核人无法判断
// 那个数是「算出来的」还是「填进去的」。
func (m Materiality) Explain() []string {
	return []string{
		fmt.Sprintf("基准：%s %s", m.Benchmark.Label(), m.BenchmarkAmount),
		fmt.Sprintf("整体重要性 = %s × %s = %s",
			m.BenchmarkAmount, Percent(m.RatePPM), m.Overall()),
		fmt.Sprintf("实际执行重要性 = %s × %s = %s",
			m.Overall(), Percent(m.PerformancePPM), m.Performance()),
		fmt.Sprintf("明显微小错报临界值 = %s × %s = %s",
			m.Overall(), Percent(m.TrivialPPM), m.Trivial()),
	}
}

// Percent 把百万分比写成百分比字符串（去掉多余的零）。
func Percent(ppm int64) string {
	s := fmt.Sprintf("%.4f", float64(ppm)/10000)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s + "%"
}

// ---------------------------------------------------------------------------
// 审计调整
// ---------------------------------------------------------------------------

// AdjustKind 是调整的种类。
//
// 两者的区别不是形式：**重分类不动损益，调整动损益**。
// 汇总未更正错报时重分类不计入 —— 它不改变利润，
// 计进去会让「错报合计」虚高，把没超门槛的说成超了。
type AdjustKind string

// 调整种类。
const (
	// KindAdjust 是调整：影响损益或资产负债的实质改动。
	KindAdjust AdjustKind = "adjust"
	// KindReclass 是重分类：只在报表项目之间搬家，不影响损益与净资产。
	KindReclass AdjustKind = "reclass"
)

// Valid 报告种类是否可识别。
func (k AdjustKind) Valid() bool { return k == KindAdjust || k == KindReclass }

// Label 返回中文名。
func (k AdjustKind) Label() string {
	if k == KindReclass {
		return "重分类"
	}
	return "调整"
}

// AdjustLine 是调整分录的一行。
type AdjustLine struct {
	LineNo      int
	AccountCode string
	Summary     string
	Debit       money.Money
	Credit      money.Money
	// 辅助核算（与凭证一致）。
	ContactID  *int64
	EmployeeID *int64
	DeptID     *int64
	ProjectID  *int64
}

// NetDebit 返回该行的净借方（借 − 贷）。
//
// 审定表按它累加：借方正、贷方负，与科目的「借正贷负」口径一致。
func (l AdjustLine) NetDebit() money.Money { return l.Debit.Sub(l.Credit) }

// Adjustment 是一笔审计调整。
type Adjustment struct {
	ID      int64
	Period  period.Key
	Code    string
	Kind    AdjustKind
	Summary string
	// Reason 是调整依据（底稿的核心：为什么调）。
	Reason string
	// Evidence 是证据来源（哪份资料支持这笔调整）。
	Evidence string
	// Booked 为真表示已经生成过调整凭证（VoucherID 记着是哪一张）。
	//
	// ★ Booked 不等于 Posted：这张软件里凭证录完只落草稿，
	// 过账只发生在账期结算。所以「凭证已生成」和「账已改」是两件事。
	Booked bool
	// Posted 为真表示那张调整凭证**已经过账**，调整的影响已经在账面数里。
	//
	// ★ 它决定这笔要不要计入「未更正错报」，
	// 也决定审定数里要不要加它 —— 见包注释。
	//
	// 这个字段**不落库**：它由凭证的状态算出来（store 层负责）。
	// 存一份副本迟早会对不上 —— 凭证过账了而底稿还写着未入账，
	// 那笔调整就会被算两次：一次在审定数里、一次在账面数里。
	Posted    bool
	VoucherID *int64
	Lines     []AdjustLine
	// CreatedBy / ReviewedBy 是编制人与复核人（底稿必须留痕）。
	CreatedBy  string
	ReviewedBy string
}

// TotalDebit 返回借方合计。
func (a Adjustment) TotalDebit() money.Money {
	var s money.Money
	for _, l := range a.Lines {
		s = s.Add(l.Debit)
	}
	return s
}

// TotalCredit 返回贷方合计。
func (a Adjustment) TotalCredit() money.Money {
	var s money.Money
	for _, l := range a.Lines {
		s = s.Add(l.Credit)
	}
	return s
}

// Amount 是这笔调整的金额（借方合计）。
func (a Adjustment) Amount() money.Money { return a.TotalDebit() }

// Balanced 报告借贷是否相等。
func (a Adjustment) Balanced() bool {
	return a.TotalDebit() == a.TotalCredit() && a.TotalDebit().IsPositive()
}

// Adjustment 相关错误。
var (
	ErrNoSummary  = errors.New("workpaper: 调整摘要不能为空")
	ErrNoReason   = errors.New("workpaper: 必须写明调整依据")
	ErrNotPaired  = errors.New("workpaper: 调整分录至少要有两条")
	ErrUnbalanced = errors.New("workpaper: 借贷不平")
)

// Validate 检查这笔调整能不能用。
func (a Adjustment) Validate() error {
	if !a.Period.Valid() {
		return fmt.Errorf("workpaper: 会计期间 %s 非法", a.Period)
	}
	if !a.Kind.Valid() {
		return fmt.Errorf("workpaper: 调整种类 %q 非法（应为 adjust 或 reclass）", a.Kind)
	}
	if strings.TrimSpace(a.Summary) == "" {
		return ErrNoSummary
	}
	if strings.TrimSpace(a.Reason) == "" {
		return ErrNoReason
	}
	if len(a.Lines) < 2 {
		return ErrNotPaired
	}
	for i, l := range a.Lines {
		if strings.TrimSpace(l.AccountCode) == "" {
			return fmt.Errorf("workpaper: 第 %d 行没有科目", i+1)
		}
		if l.Debit.IsNegative() || l.Credit.IsNegative() {
			return fmt.Errorf("workpaper: 第 %d 行金额为负 —— 请用相反方向的分录表达", i+1)
		}
		if l.Debit.IsPositive() == l.Credit.IsPositive() {
			return fmt.Errorf("workpaper: 第 %d 行必须恰好一个方向有金额（借 %s / 贷 %s）",
				i+1, l.Debit, l.Credit)
		}
	}
	if !a.Balanced() {
		return fmt.Errorf("%w（借 %s / 贷 %s，差 %s）—— 调整分录不平就是记错了，"+
			"不能靠尾差科目抹平",
			ErrUnbalanced, a.TotalDebit(), a.TotalCredit(),
			a.TotalDebit().Sub(a.TotalCredit()).Abs())
	}
	return nil
}

// ---------------------------------------------------------------------------
// 审定表
// ---------------------------------------------------------------------------

// WorksheetRow 是审定表的一行。
type WorksheetRow struct {
	// AccountCode / AccountName 是科目。
	AccountCode string
	AccountName string
	// BookBalance 是账面余额，**借正贷负**。
	//
	// 用带符号的一个数而不是借贷两列：审定数的算式是加法
	// （账面 + 调整），分成两列就得先判断方向再加，
	// 而判断方向正是最容易写错的一步。
	BookBalance money.Money
	// AdjustDebit / AdjustCredit 是**未入账**调整的借贷发生额。
	AdjustDebit  money.Money
	AdjustCredit money.Money
}

// Audited 返回审定余额（借正贷负）。
func (r WorksheetRow) Audited() money.Money {
	return r.BookBalance.Add(r.AdjustDebit).Sub(r.AdjustCredit)
}

// Adjusted 报告这个科目有没有调整。
func (r WorksheetRow) Adjusted() bool {
	return r.AdjustDebit.IsPositive() || r.AdjustCredit.IsPositive()
}

// Worksheet 是审定表。
type Worksheet struct {
	Period period.Key
	Rows   []WorksheetRow
	// Materiality 非空时用于判断哪些行超过了明显微小错报临界值。
	Materiality *Materiality
}

// ExceedsTrivial 报告某个金额是否达到「需要累积」的程度。
//
// 没有配重要性水平时一律返回 true：不知道门槛时，
// 宁可让用户看到全部，也不要替他过滤掉。
func (w Worksheet) ExceedsTrivial(amount money.Money) bool {
	if w.Materiality == nil {
		return true
	}
	return amount.Abs() >= w.Materiality.Trivial()
}

// ---------------------------------------------------------------------------
// 未更正错报汇总
// ---------------------------------------------------------------------------

// Misstatement 是未更正错报汇总里的一条。
type Misstatement struct {
	Code    string
	Summary string
	// Amount 是对损益（或资产负债）的影响额。
	Amount money.Money
	Kind   AdjustKind
	// Reason 是调整依据，汇总表上要能看到。
	Reason string
	// Trivial 为真表示这笔低于明显微小错报临界值 —— 列出但不计入合计。
	Trivial bool
}

// MisstatementSummary 是未更正错报汇总表。
type MisstatementSummary struct {
	// Items 是逐笔明细。
	Items []Misstatement
	// Total 是合计（只含 KindAdjust）。
	Total money.Money
	// Overall / Performance / Trivial 是三个门槛，便于界面直接比较。
	Overall     money.Money
	Performance money.Money
	Trivial     money.Money
	// HasMateriality 为假表示还没配重要性水平。
	HasMateriality bool
	// ReclassCount 是重分类的笔数（不计入合计，但要报出来）。
	ReclassCount int
	// TrivialCount 是低于明显微小错报临界值的笔数（列出但不计入合计）。
	TrivialCount int
}

// Misstatements 汇总一批调整里的未更正错报。
//
// ★ 只统计**未过账**的（Posted 为假）。
//
// 生成过凭证但那张凭证还是草稿的，**仍然算未更正错报** ——
// 过账只在账期结算，草稿没进账，账上那个错就还在。
// 已过账的才是真改进账里了，再算一遍未更正错报，
// 等于把已经改过的错又报了一次 —— 用户会以为还有问题没处理。
func Misstatements(adjustments []Adjustment, m *Materiality) *MisstatementSummary {
	out := &MisstatementSummary{Items: []Misstatement{}}
	if m != nil {
		out.HasMateriality = true
		out.Overall = m.Overall()
		out.Performance = m.Performance()
		out.Trivial = m.Trivial()
	}
	for _, a := range adjustments {
		if a.Posted {
			continue // 已入账，不再是未更正错报
		}
		if a.Kind == KindReclass {
			out.ReclassCount++
		}
		amount := a.Amount()
		// ★ 低于明显微小错报临界值的，**不必累积**。
		//
		// 准则（CAS 1251）说的是「累积识别出的错报，除非明显微小」。
		// 原来一律累加，于是 13 笔各 4,900 元（每笔都低于 5,000 的临界值）
		// 会凑出 63,700，把「未超过实际执行重要性」说成「超过」。
		//
		// 但**照样列出来**（Items 里保留），只是不计入合计 ——
		// 该让人看见的不能藏，判断口径要写在结论里。
		trivial := m != nil && !m.Trivial().IsZero() && amount.Abs() < m.Trivial()
		if trivial {
			out.TrivialCount++
		}
		out.Items = append(out.Items, Misstatement{
			Code: a.Code, Summary: a.Summary, Kind: a.Kind,
			Amount: amount, Reason: a.Reason, Trivial: trivial,
		})
		if a.Kind == KindAdjust && !trivial {
			out.Total = out.Total.Add(amount)
		}
	}
	return out
}

// Concludes 给出一句结论：未更正错报合计与门槛的关系。
//
// 这句话是底稿的意义所在：只列一堆金额，复核人还得自己判断
// 「这些加起来算不算重大」。
func (s MisstatementSummary) Concludes() string {
	if !s.HasMateriality {
		return "尚未确定重要性水平，无法判断未更正错报是否重大 —— " +
			"没有门槛就没法说「超没超」。"
	}
	if len(s.Items) == 0 {
		return "本期没有未更正错报。"
	}
	switch {
	case s.Total.IsZero() && s.TrivialCount > 0:
		return fmt.Sprintf("登记的 %d 笔调整都低于明显微小错报临界值 %s（或为不影响损益的重分类）——"+
			"按准则这类错报不必累积，但已逐笔列出供复核。", len(s.Items), s.Trivial)
	case s.Total.IsZero():
		return fmt.Sprintf("登记了 %d 笔调整，但都是重分类，不影响损益。", len(s.Items))
	case s.Total >= s.Overall:
		return fmt.Sprintf("★ 未更正错报合计 %s 已达到整体重要性 %s —— "+
			"不调整的话，报表整体可能被认定为存在重大错报。", s.Total, s.Overall)
	case s.Total >= s.Performance:
		return fmt.Sprintf("未更正错报合计 %s 超过实际执行重要性 %s、"+
			"未达整体重要性 %s —— 需要评估它与其他未发现错报叠加后是否变得重大。",
			s.Total, s.Performance, s.Overall)
	default:
		return fmt.Sprintf("未更正错报合计 %s，低于实际执行重要性 %s，尚未构成重大错报。",
			s.Total, s.Performance)
	}
}
