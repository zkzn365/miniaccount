// Package payroll 实现工资计算与个人所得税预扣预缴。
//
// # 为什么个税不能用「按月算」
//
// 2019 年起中国个人所得税对工资薪金采用**累计预扣预缴法**：
// 每个月算的不是「本月收入 × 税率」，而是
//
//	累计应纳税所得额 = 累计收入 − 累计减除费用(5000×月份数)
//	                  − 累计三险一金(个人) − 累计专项附加扣除 − 累计其他扣除
//	本月应预扣 = 累计应纳税所得额 × 预扣率 − 速算扣除数 − 累计已预扣
//
// 这意味着同一个员工同样的月薪，1 月和 12 月交的税完全不同 ——
// 因为累计额会跨税率级距。
// 按月算会**少扣税**，年末汇算清缴时员工要补一大笔，体验很差。
//
// # 为什么所有税率比例都必须是「数据」
//
// 个税税率表、社保比例、缴费基数上下限**每年都可能变**，
// 且社保比例按城市不同。硬编码意味着每次政策调整都要发新版软件。
// 因此本包把它们全部做成可配置的参数结构。
package payroll

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// 工资模块错误。
var (
	ErrNoTaxBracket   = errors.New("payroll: 税率表为空或不合法")
	ErrBadEmployee    = errors.New("payroll: 员工信息不合法")
	ErrAlreadyPosted  = errors.New("payroll: 工资单已过账")
	ErrNotDraft       = errors.New("payroll: 只有草稿状态可以修改")
	ErrNegativeAmount = errors.New("payroll: 金额不能为负")
)

// ---------------------------------------------------------------------------
// 税率表
// ---------------------------------------------------------------------------

// TaxBracket 是一个预扣率级距。
type TaxBracket struct {
	// UpperLimit 是「累计预扣预缴应纳税所得额」的上限（含）。
	// 最高一级用零值 money.Money(0) 表示无上限。
	UpperLimit money.Money
	Rate       money.Rate
	// QuickDeduction 是速算扣除数。
	QuickDeduction money.Money
}

// TaxTable 是一张预扣率表。
//
// 官方《个人所得税扣缴申报管理办法》的「个人所得税预扣率表一」
// （居民个人工资、薪金所得预扣预缴适用）：
//
//	| 级数 | 累计预扣预缴应纳税所得额 | 预扣率 | 速算扣除数 |
//	|    1 | 不超过 36,000 元        |    3% |          0 |
//	|    2 | 36,000 ~ 144,000 元     |   10% |      2,520 |
//	|    3 | 144,000 ~ 300,000 元    |   20% |     16,920 |
//	|    4 | 300,000 ~ 420,000 元    |   25% |     31,920 |
//	|    5 | 420,000 ~ 660,000 元    |   30% |     52,920 |
//	|    6 | 660,000 ~ 960,000 元    |   35% |     85,920 |
//	|    7 | 超过 960,000 元         |   45% |    181,920 |
type TaxTable struct {
	Name     string
	Brackets []TaxBracket
	// BasicDeduction 是减除费用，即俗称的「起征点」，5000 元/月。
	BasicDeduction money.Money
}

// DefaultWageTaxTable 返回工资薪金所得的默认预扣率表（表一）。
func DefaultWageTaxTable() TaxTable {
	yuan := func(n int64) money.Money { return money.Money(n) * money.Yuan }
	return TaxTable{
		Name:           "个人所得税预扣率表一（工资薪金所得）",
		BasicDeduction: yuan(5000),
		Brackets: []TaxBracket{
			{UpperLimit: yuan(36000), Rate: money.RatePercent(3), QuickDeduction: 0},
			{UpperLimit: yuan(144000), Rate: money.RatePercent(10), QuickDeduction: yuan(2520)},
			{UpperLimit: yuan(300000), Rate: money.RatePercent(20), QuickDeduction: yuan(16920)},
			{UpperLimit: yuan(420000), Rate: money.RatePercent(25), QuickDeduction: yuan(31920)},
			{UpperLimit: yuan(660000), Rate: money.RatePercent(30), QuickDeduction: yuan(52920)},
			{UpperLimit: yuan(960000), Rate: money.RatePercent(35), QuickDeduction: yuan(85920)},
			{UpperLimit: 0, Rate: money.RatePercent(45), QuickDeduction: yuan(181920)},
		},
	}
}

// DefaultLaborTaxTable 返回劳务报酬所得的预扣率表（表二）。
//
//	| 级数 | 预扣预缴应纳税所得额    | 预扣率 | 速算扣除数 |
//	|    1 | 不超过 20,000 元        |   20% |          0 |
//	|    2 | 20,000 ~ 50,000 元      |   30% |      2,000 |
//	|    3 | 超过 50,000 元          |   40% |      7,000 |
type laborTable struct {
	Brackets []TaxBracket
}

// Validate 检查税率表是否自洽：级距必须递增、最后一级必须无上限。
func (t *TaxTable) Validate() error {
	if len(t.Brackets) == 0 {
		return fmt.Errorf("%w: %s", ErrNoTaxBracket, t.Name)
	}
	for i := 1; i < len(t.Brackets); i++ {
		prev, cur := t.Brackets[i-1], t.Brackets[i]
		if prev.UpperLimit.IsZero() {
			return fmt.Errorf("%w: %s 第 %d 级已无上限，之后不应再有级距",
				ErrNoTaxBracket, t.Name, i)
		}
		if cur.UpperLimit.IsPositive() && cur.UpperLimit <= prev.UpperLimit {
			return fmt.Errorf("%w: %s 第 %d 级上限 %s 未大于上一级的 %s",
				ErrNoTaxBracket, t.Name, i+1, cur.UpperLimit, prev.UpperLimit)
		}
	}
	if !t.Brackets[len(t.Brackets)-1].UpperLimit.IsZero() {
		return fmt.Errorf("%w: %s 最后一级必须无上限", ErrNoTaxBracket, t.Name)
	}
	return nil
}

// Lookup 返回给定的应纳税所得额适用的级距。
func (t *TaxTable) Lookup(taxable money.Money) TaxBracket {
	if !taxable.IsPositive() {
		return TaxBracket{}
	}
	for _, b := range t.Brackets {
		if b.UpperLimit.IsZero() || taxable <= b.UpperLimit {
			return b
		}
	}
	return t.Brackets[len(t.Brackets)-1]
}

// Tax 按级距计算应纳税额（不减已缴）。
func (t *TaxTable) Tax(taxable money.Money) money.Money {
	if !taxable.IsPositive() {
		return 0
	}
	b := t.Lookup(taxable)
	v := b.Rate.Apply(taxable).Sub(b.QuickDeduction)
	if v.IsNegative() {
		return 0
	}
	return v
}

// ---------------------------------------------------------------------------
// 累计预扣预缴
// ---------------------------------------------------------------------------

// YTD 是一个员工在本年度截至上月的累计数。
//
// 累计预扣预缴法必须知道这些数才能算本月税额，
// 因此工资单计算强依赖「本月之前的历史」。
type YTD struct {
	// Slips 是本年度**已经出过工资单**的月数（不含本月）。
	//
	// ★ 它与 Months 是两回事，必须分开存：
	// Months 决定减除费用（按任职月份数），Slips 用来发现
	// 「任职了 N 个月却只有 M 张工资单」——那意味着累计收入缺了
	// 历史月份，而减除费用却按 N 个月算，结果是**少扣**个税。
	// 这个偏差不会报错，只能主动提示。
	Slips int

	// Months 是本年度在本单位任职的月数（**含本月**）。
	//
	// ★ 减除费用 = 5000 × Months（税法所称「5000 元/月」的累计口径）。
	// 这个数取自**任职月份数**，不是「已经出了几张工资单」——
	// 两者只在「从入职起每个月都有工资单」时才相等。
	//
	// 取错的后果是静默多扣个税：年中才启用本软件的企业，
	// 员工明明已任职 12 个月，程序按 5 张工资单算，
	// 全年多扣 3500 元（见 EmployedMonths）。
	Months int

	Income            money.Money // 累计收入
	TaxFreeIncome     money.Money // 累计免税收入
	SpecialDeduction  money.Money // 累计专项扣除（三险一金个人部分）
	SpecialAdditional money.Money // 累计专项附加扣除（子女教育、赡养老人等）
	OtherDeduction    money.Money // 累计依法确定的其他扣除

	// TaxWithheld 是累计已预扣预缴税额。
	TaxWithheld money.Money
}

// TaxableIncome 返回累计预扣预缴应纳税所得额。
func (y YTD) TaxableIncome(table TaxTable) money.Money {
	v := y.Income.
		Sub(y.TaxFreeIncome).
		Sub(table.BasicDeduction.MulInt(int64(y.Months))).
		Sub(y.SpecialDeduction).
		Sub(y.SpecialAdditional).
		Sub(y.OtherDeduction)
	if v.IsNegative() {
		return 0
	}
	return v
}

// CumulativeTax 计算**本月应预扣预缴税额**（累计法）。
//
// 这是本包最重要的函数。注意最后一步：
//
//	本期应预扣 = 累计应纳税额 − 累计已预扣
//
// 结果可能为负（例如某月收入骤降），此时**按 0 处理而不是退税**：
// 中国的预扣预缴环节不退税，多缴的部分在次年汇算清缴时退。
// 若这里返回负数，会导致本月实发工资虚高，年末员工要补一大笔。
func CumulativeTax(y YTD, table TaxTable) money.Money {
	cumulative := table.Tax(y.TaxableIncome(table))
	due := cumulative.Sub(y.TaxWithheld)
	if !due.IsPositive() {
		return 0
	}
	return due
}

// ---------------------------------------------------------------------------
// 劳务报酬
// ---------------------------------------------------------------------------

// LaborRemunerationTaxable 计算劳务报酬的预扣预缴应纳税所得额。
//
//	每次收入 ≤ 4,000 元：应纳税所得额 = 收入 − 800
//	每次收入 > 4,000 元：应纳税所得额 = 收入 × (1 − 20%)
func LaborRemunerationTaxable(income money.Money) money.Money {
	if !income.IsPositive() {
		return 0
	}
	threshold := 4000 * money.Yuan
	var v money.Money
	if income <= threshold {
		v = income.Sub(800 * money.Yuan)
	} else {
		v = income.Sub(money.RatePercent(20).Apply(income))
	}
	if v.IsNegative() {
		return 0
	}
	return v
}

// laborBrackets 是劳务报酬预扣率表（表二）。
func laborBrackets() []TaxBracket {
	yuan := func(n int64) money.Money { return money.Money(n) * money.Yuan }
	return []TaxBracket{
		{UpperLimit: yuan(20000), Rate: money.RatePercent(20), QuickDeduction: 0},
		{UpperLimit: yuan(50000), Rate: money.RatePercent(30), QuickDeduction: yuan(2000)},
		{UpperLimit: 0, Rate: money.RatePercent(40), QuickDeduction: yuan(7000)},
	}
}

// LaborRemunerationTax 计算劳务报酬应预扣预缴税额。
//
// 劳务报酬**按次**预扣，不累计 —— 这是它与工资薪金最重要的区别。
func LaborRemunerationTax(income money.Money) money.Money {
	taxable := LaborRemunerationTaxable(income)
	if !taxable.IsPositive() {
		return 0
	}
	table := TaxTable{Name: "个人所得税预扣率表二（劳务报酬所得）", Brackets: laborBrackets()}
	return table.Tax(taxable)
}

// ---------------------------------------------------------------------------
// 社保公积金
// ---------------------------------------------------------------------------

// InsuranceRates 是一组社保公积金比例。
//
// 全部用 money.Rate 定点表示（百万分之一），避免浮点误差。
type InsuranceRates struct {
	// 个人承担部分
	PensionSelf      money.Rate `json:"pensionSelf"`      // 养老保险
	MedicalSelf      money.Rate `json:"medicalSelf"`      // 医疗保险
	UnemploymentSelf money.Rate `json:"unemploymentSelf"` // 失业保险
	HousingFundSelf  money.Rate `json:"housingFundSelf"`  // 住房公积金
	// 单位承担部分
	PensionCo      money.Rate `json:"pensionCo"`
	MedicalCo      money.Rate `json:"medicalCo"`
	UnemploymentCo money.Rate `json:"unemploymentCo"`
	InjuryCo       money.Rate `json:"injuryCo"`    // 工伤保险（仅单位）
	MaternityCo    money.Rate `json:"maternityCo"` // 生育保险（仅单位，多地已并入医疗）
	HousingFundCo  money.Rate `json:"housingFundCo"`
}

// InsuranceScheme 是一个城市（或单位）的社保公积金方案。
//
// ⚠️ 社保比例与缴费基数上下限**按城市、按年度**发布，且各地差异很大。
// 本工程**不预置任何具体数值** —— 预置一个看着像真的默认比例，
// 用户会直接当真使用，而那几乎必然与当地当年标准不符
// （错的比例算出来的社保和个税都是错的，且不会报错）。
// 因此只提供 SchemeTemplate：一张**费率为零**的空表，
// 用户按当地社保局公布的标准填写。这也是把它做成数据而非硬编码的原因。
type InsuranceScheme struct {
	Name string `json:"name"`
	City string `json:"city"`

	// BaseMin / BaseMax 是缴费基数上下限，通常按上年度社会平均工资的
	// 60% 和 300% 确定。零值表示不设限。
	BaseMin money.Money `json:"baseMin"`
	BaseMax money.Money `json:"baseMax"`

	// 公积金基数上下限可能与社保不同
	HousingFundBaseMin money.Money `json:"housingFundBaseMin"`
	HousingFundBaseMax money.Money `json:"housingFundBaseMax"`

	Rates InsuranceRates `json:"rates"`

	// EffectiveFrom 是该方案适用的起始年月，便于政策调整后保留历史数据。
	EffectiveFrom calendar.Date `json:"effectiveFrom"`
}

// ClampBase 把工资额夹到缴费基数区间内。
func ClampBase(amount, min, max money.Money) money.Money {
	if amount.IsNegative() {
		amount = 0
	}
	if min.IsPositive() && amount < min {
		amount = min
	}
	if max.IsPositive() && amount > max {
		amount = max
	}
	return amount
}

// SocialInsurance 是一个月的社保公积金计算结果。
type SocialInsurance struct {
	// 缴费基数（已按上下限夹取）
	Base            money.Money
	HousingFundBase money.Money

	// 个人承担
	PensionSelf      money.Money
	MedicalSelf      money.Money
	UnemploymentSelf money.Money
	HousingFundSelf  money.Money

	// 单位承担
	PensionCo      money.Money
	MedicalCo      money.Money
	UnemploymentCo money.Money
	InjuryCo       money.Money
	MaternityCo    money.Money
	HousingFundCo  money.Money
}

// SelfTotal 返回个人承担合计。
func (s SocialInsurance) SelfTotal() money.Money {
	return money.Sum(s.PensionSelf, s.MedicalSelf, s.UnemploymentSelf, s.HousingFundSelf)
}

// CompanyTotal 返回单位承担合计。
func (s SocialInsurance) CompanyTotal() money.Money {
	return money.Sum(s.PensionCo, s.MedicalCo, s.UnemploymentCo,
		s.InjuryCo, s.MaternityCo, s.HousingFundCo)
}

// CalcSocialInsurance 按方案计算某工资额对应的社保公积金。
//
//	si := scheme.Calc(grossPay)
//
// 缴费基数用**工资总额**（而不是基本工资）—— 这是法定口径，
// 也是常见的算错点。但员工在档案里申报了缴费基数时，以申报数为准，
// 见 CalcWithDeclaredBase。
func (sc *InsuranceScheme) Calc(grossPay money.Money) SocialInsurance {
	return sc.CalcWithDeclaredBase(0, 0, grossPay)
}

// CalcWithDeclaredBase 按「员工申报的缴费基数」计算社保公积金。
//
// 取值顺序（这也是实务中的顺序）：
//
//	社保基数    申报基数 > 0 ? 申报基数 : 当月工资总额
//	公积金基数  单独申报? 单独申报值 : (社保申报基数 > 0 ? 社保申报基数 : 当月工资总额)
//	最后**各自**夹到自己的上下限之间（两套上下限是不同的）
//
// ★ 申报基数必须单独给，不能用工资额顶替。
// 小微企业普遍按最低基数缴纳，而最低基数与实发工资无关：
// 月薪 2 万、按 4000 基数缴的员工，若用工资额硬算，
// 个人社保会从 900 变成 4500 —— 一个月多扣 3600 元，
// 而且个税的专项扣除跟着虚增，实发、个税、社保三处全错。
func (sc *InsuranceScheme) CalcWithDeclaredBase(
	siBase, hfBase, grossPay money.Money) SocialInsurance {

	// 社保基数的原始值：申报了就用申报的，否则用当月工资总额。
	siRaw := grossPay
	if siBase.IsPositive() {
		siRaw = siBase
	}
	// 公积金基数的原始值：单独申报 > 跟着社保的申报基数 > 当月工资总额。
	//
	// ★ 最后那一档必须是「当月工资总额」而不是「已夹过的社保基数」：
	// 社保与公积金的上下限是**两套**（公积金上限通常更高）。
	// 若拿夹过的社保基数当公积金基数，遇到「社保封顶 30000、
	// 公积金封顶 35000、工资 40000」就会把公积金也算成 30000 ——
	// 少缴 5000 的 12%，而且不报错。
	hfRaw := grossPay
	if siBase.IsPositive() {
		hfRaw = siBase
	}
	if hfBase.IsPositive() {
		hfRaw = hfBase
	}

	base := ClampBase(siRaw, sc.BaseMin, sc.BaseMax)
	hf := ClampBase(hfRaw, sc.HousingFundBaseMin, sc.HousingFundBaseMax)

	return SocialInsurance{
		Base:            base,
		HousingFundBase: hf,

		PensionSelf:      sc.Rates.PensionSelf.Apply(base),
		MedicalSelf:      sc.Rates.MedicalSelf.Apply(base),
		UnemploymentSelf: sc.Rates.UnemploymentSelf.Apply(base),
		HousingFundSelf:  sc.Rates.HousingFundSelf.Apply(hf),

		PensionCo:      sc.Rates.PensionCo.Apply(base),
		MedicalCo:      sc.Rates.MedicalCo.Apply(base),
		UnemploymentCo: sc.Rates.UnemploymentCo.Apply(base),
		InjuryCo:       sc.Rates.InjuryCo.Apply(base),
		MaternityCo:    sc.Rates.MaternityCo.Apply(base),
		HousingFundCo:  sc.Rates.HousingFundCo.Apply(hf),
	}
}

// EmployedMonths 返回某员工在 `year` 年 `month` 月为止、
// **在本单位任职受雇的月数**（含当月）。
//
// 这是《个人所得税扣缴申报管理办法（试行）》里「5000 元/月」的乘数口径：
//
//	累计减除费用 = 5000 × 纳税人当年截至本月在本单位的任职受雇月份数
//
// ★ 它不等于「已经出了几张工资单」。
// 三种常见情形下两者会分叉，而且分叉时**不会报任何错**：
//
//	年中启用软件   7 月才开始用，员工 1 月就在职
//	               → 任职 7 个月，工资单只有 1 张
//	中间漏建某月   1、3 月建了、2 月忘了
//	               → 任职 3 个月，工资单只有 2 张
//	年中入职       3 月入职
//	               → 任职 3 个月（从入职月起算），工资单 1 张
//
// 少算的每个月 = 少 5000 元扣除 = 多扣个税。
// hireDate 为零值时退回「工资单张数」口径（无法判断任职月数时的保守选择）。
func EmployedMonths(hireDate calendar.Date, year, month int) int {
	if !hireDate.Valid() {
		return 0
	}
	// 入职年份晚于计税年度：本年还没任职
	if hireDate.Year > year {
		return 0
	}
	startMonth := 1
	if hireDate.Year == year {
		startMonth = hireDate.Month
	}
	n := month - startMonth + 1
	if n < 0 {
		n = 0
	}
	if n > 12 {
		n = 12
	}
	return n
}

// ---------------------------------------------------------------------------
// 员工档案
// ---------------------------------------------------------------------------

// EmployeeKind 是人员类别。
type EmployeeKind string

// 人员类别。外聘劳务人员的个税按「劳务报酬」计算，与在职员工完全不同。
const (
	KindEmployee EmployeeKind = "employee" // 在职员工（工资薪金所得）
	KindLabor    EmployeeKind = "labor"    // 外聘劳务人员（劳务报酬所得）
)

// Label 返回中文名。
func (k EmployeeKind) Label() string {
	switch k {
	case KindEmployee:
		return "在职员工"
	case KindLabor:
		return "外聘劳务"
	default:
		return string(k)
	}
}

// Valid 报告人员类别是否合法。
func (k EmployeeKind) Valid() bool { return k == KindEmployee || k == KindLabor }

// Employee 是员工（或外聘人员）档案。
type Employee struct {
	ID     int64
	Code   string
	Name   string
	IDCard string
	Kind   EmployeeKind

	DeptID   *int64
	Position string

	HireDate  calendar.Date
	LeaveDate calendar.Date

	BankName    string
	BankAccount string
	// Phone 用于联系，也便于与个税申报表核对。
	Phone string

	// BaseSalary 是标准月基本工资（分）。
	//
	// 生成工资单时作为默认值带入 —— 绝大多数员工的绝大多数月份
	// 基本工资是不变的，让用户每月重敲一遍是把「默认值」推给了他。
	// 逐月可变的部分（加班费、考勤扣款）仍在工资单上填。
	BaseSalary money.Money
	// SIBase 是社保缴费基数（分）；为 0 表示按当月工资总额。
	//
	// ★ 必须单独一列：很多小微企业按**最低基数**缴纳，
	// 而最低基数与实发工资无关，拿基本工资硬算会算错。
	SIBase money.Money
	// HFBBase 是公积金缴费基数（分）；为 0 表示按社保基数。
	HFBBase money.Money
	// SpecialAdditional 是每月专项附加扣除合计（分）。
	//
	// 子女教育、赡养老人、住房贷款利息等，在自然人电子税务局里
	// 是**按年确认**的，因此这里存的是年度不变的月定额，
	// 不该每月重填。
	SpecialAdditional money.Money

	// ExpenseAccountCode 是工资费用的归集科目。
	//
	// 生产人员应记「生产成本—直接人工」、销售人员记「销售费用—工资」、
	// 管理人员记「管理费用—工资」。一律记管理费用会低估产品成本、
	// 高估期间费用。
	ExpenseAccountCode string
	// CompanyAccountCode 是单位承担的社保/公积金的归集科目，
	// 通常与 ExpenseAccountCode 同属一类。留空则用 ExpenseAccountCode。
	CompanyAccountCode string

	// SchemeName 指向适用的社保方案；为空表示不缴社保（如外聘劳务）。
	SchemeName string

	IsEnabled bool
	Remark    string
}

// Validate 校验员工信息。
func (e *Employee) Validate() error {
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("%w: 姓名为空", ErrBadEmployee)
	}
	if !e.Kind.Valid() {
		return fmt.Errorf("%w: 人员类别 %q", ErrBadEmployee, e.Kind)
	}
	if e.ExpenseAccountCode == "" {
		return fmt.Errorf("%w: %s 未指定工资费用科目", ErrBadEmployee, e.Name)
	}
	if e.Kind == KindEmployee && e.SchemeName == "" {
		// 在职员工原则上应缴社保；不缴是特例（如退休返聘），
		// 因此这里只提示不阻断 —— 由 EnsureInsurance 显式决定
		_ = e
	}
	return nil
}

// IsActiveIn 报告员工在某期间是否在职（用于自动带出工资单人员）。
func (e *Employee) IsActiveIn(k PeriodKey) bool {
	if !e.IsEnabled {
		return false
	}
	first := monthStart(k)
	last := monthEnd(k)
	if !e.HireDate.IsZero() && e.HireDate.After(last) {
		return false
	}
	if !e.LeaveDate.IsZero() && e.LeaveDate.Before(first) {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// 工资单
// ---------------------------------------------------------------------------

// Item 是工资单里的一名员工。
type Item struct {
	EmployeeID int64
	Employee   *Employee

	// 应发项目
	BaseSalary    money.Money // 基本工资
	PostAllowance money.Money // 岗位津贴
	OvertimePay   money.Money // 加班费
	Bonus         money.Money // 奖金
	OtherIncome   money.Money // 其他应发

	// 扣款项目（考勤等，不涉税）
	AttendanceDeduction money.Money
	OtherDeduction      money.Money

	// 社保公积金（由方案算出，也可手工覆盖）
	Insurance SocialInsurance
	// InsuranceOverridden 为真时不再按方案重算（手工调整过）。
	InsuranceOverridden bool

	// TaxWarning 是计算后发现的、需要用户注意的问题（目前只有一项：
	// 任职月份数多于工资单张数，累计收入可能缺月份 → 个税会少扣）。
	// 它由 ComputeItem 填，不是用户输入。
	TaxWarning string

	// SIBase / HFBBase 是员工档案里申报的**缴费基数**（分）。
	//
	// ★ 必须由员工档案带下来，不能在现场用工资额顶替。
	// 很多小微企业按最低基数缴纳，而最低基数与实发工资无关 ——
	// 月薪 2 万、按 4000 基数缴的员工，用工资额硬算会让
	// 个人社保从 900 变成 4500，一个月多扣 3600 元。
	// 为 0 表示未申报，此时才退回按当月工资总额计算。
	SIBase, HFBBase money.Money

	// 专项附加扣除（子女教育、继续教育、大病医疗、住房贷款利息、
	// 住房租金、赡养老人、3 岁以下婴幼儿照护）
	SpecialAdditional money.Money
	// 其他扣除（如企业年金、商业健康险）
	OtherTaxDeduction money.Money

	// 计算结果
	GrossPay money.Money // 应发合计
	IIT      money.Money // 本月个人所得税
	NetPay   money.Money // 实发合计

	// Cum 是本月计算所用的累计数（含本月），便于核对与追溯。
	Cum YTDAfter
}

// YTDAfter 记录计算后的累计状态，便于排查「为什么这个月税不一样」。
type YTDAfter struct {
	Months            int
	Income            money.Money
	SpecialDeduction  money.Money
	SpecialAdditional money.Money
	OtherDeduction    money.Money
	Taxable           money.Money
	CumulativeTax     money.Money
	TaxWithheld       money.Money
}

// Gross 计算应发合计。
//
//	应发合计 = 基本工资 + 岗位津贴 + 加班费 + 奖金 + 其他应发
//
// 注意：考勤扣款**不进**应发合计，它是发放时的减项。
// 个税的计税依据是应发合计（税前），考勤扣款在税前扣除会影响个税，
// 这里按多数单位的做法：考勤扣款在计算个税前扣除。
func (it *Item) Gross() money.Money {
	return money.Sum(it.BaseSalary, it.PostAllowance, it.OvertimePay,
		it.Bonus, it.OtherIncome)
}

// TaxableGross 返回个税计税依据（应发合计 − 考勤扣款）。
func (it *Item) TaxableGross() money.Money {
	v := it.Gross().Sub(it.AttendanceDeduction).Sub(it.OtherDeduction)
	if v.IsNegative() {
		return 0
	}
	return v
}

// ComputeResult 是一名员工的计算结果。
type ComputeResult struct {
	GrossPay money.Money
	IIT      money.Money
	NetPay   money.Money
	// Insurance 是实际采用（或重算后）的社保公积金。
	Insurance SocialInsurance
	// Cum 是本月之后的累计状态。
	Cum YTDAfter
	// Note 是计算说明，便于界面展示「税是怎么算出来的」。
	Note string
}

// ComputeItem 计算一名员工本月的工资与个税。
//
// prior 是**截至上月**的累计数；table 是税率表；scheme 是该员工适用的
// 社保方案（外聘劳务传 nil）。
//
// 在职员工走累计预扣预缴；外聘劳务人员走劳务报酬按次预扣。
func ComputeItem(it *Item, prior YTD, table TaxTable, scheme *InsuranceScheme) ComputeResult {
	// 1) 应发合计
	gross := it.Gross()

	// 2) 社保公积金
	ins := it.Insurance
	if scheme != nil && !it.InsuranceOverridden {
		// ★ 用员工档案里申报的缴费基数，而不是当月工资额。
		// 申报基数由仓储从 employee 表带下来（见 Item.SIBase 的说明）。
		ins = scheme.CalcWithDeclaredBase(it.SIBase, it.HFBBase, gross)
	}
	it.Insurance = ins

	// 3) 计税依据
	taxable := gross.Sub(it.AttendanceDeduction).Sub(it.OtherDeduction)
	if taxable.IsNegative() {
		taxable = 0
	}

	// 4) 个税
	var iit money.Money
	var note string
	if it.Employee != nil && it.Employee.Kind == KindLabor {
		// 劳务报酬：按次预扣，不累计
		iit = LaborRemunerationTax(taxable)
		note = fmt.Sprintf("劳务报酬：应纳税所得额 %s，预扣 %s",
			LaborRemunerationTaxable(taxable), iit)
	} else {
		// 工资薪金：累计预扣预缴
		cur := YTD{
			Months:            prior.Months + 1,
			Slips:             prior.Slips + 1,
			Income:            prior.Income.Add(taxable),
			TaxFreeIncome:     prior.TaxFreeIncome,
			SpecialDeduction:  prior.SpecialDeduction.Add(ins.SelfTotal()),
			SpecialAdditional: prior.SpecialAdditional.Add(it.SpecialAdditional),
			OtherDeduction:    prior.OtherDeduction.Add(it.OtherTaxDeduction),
			TaxWithheld:       prior.TaxWithheld,
		}
		iit = CumulativeTax(cur, table)
		ti := cur.TaxableIncome(table)

		// ★ 任职月份数多于工资单张数 → 累计收入缺了历史月份。
		//
		// 减除费用按任职月份算（法定口径），但累计收入只有实际建过单的那几个月。
		// 两者不匹配时个税会**少扣**，而且不会报任何错 ——
		// 年度汇算时员工要补税，代扣代缴单位要更正申报。
		// 只能把话说清楚，让用户决定是否补录历史月份。
		if cur.Months > cur.Slips {
			it.TaxWarning = fmt.Sprintf(
				"按任职月份数扣除了 %d 个月的减除费用（%s×%d），"+
					"但本年度只建过 %d 张工资单 —— 累计收入可能缺了 %d 个月。"+
					"若这些月份确实发过工资，请补录，否则个税会少扣。",
				cur.Months, table.BasicDeduction, cur.Months, cur.Slips,
				cur.Months-cur.Slips)
		}
		b := table.Lookup(ti)
		note = fmt.Sprintf("累计应纳税所得额 %s，适用 %s（速算扣除 %s），累计应纳税 %s，已预扣 %s，本月应扣 %s",
			ti, b.Rate, b.QuickDeduction, table.Tax(ti), prior.TaxWithheld, iit)

		it.Cum = YTDAfter{
			Months:            cur.Months,
			Income:            cur.Income,
			SpecialDeduction:  cur.SpecialDeduction,
			SpecialAdditional: cur.SpecialAdditional,
			OtherDeduction:    cur.OtherDeduction,
			Taxable:           ti,
			CumulativeTax:     table.Tax(ti),
			TaxWithheld:       prior.TaxWithheld.Add(iit),
		}
	}

	// 5) 实发
	net := gross.
		Sub(it.AttendanceDeduction).
		Sub(it.OtherDeduction).
		Sub(ins.SelfTotal()).
		Sub(iit)

	it.GrossPay, it.IIT, it.NetPay = gross, iit, net
	return ComputeResult{
		GrossPay: gross, IIT: iit, NetPay: net, Insurance: ins, Cum: it.Cum, Note: note,
	}
}

// ---------------------------------------------------------------------------
// 工资表（一个期间的全部人员）
// ---------------------------------------------------------------------------

// RunStatus 是工资单状态。
type RunStatus string

// 工资单状态。
const (
	RunDraft     RunStatus = "draft"     // 草稿：可改可重算
	RunConfirmed RunStatus = "confirmed" // 已确认：锁定金额
	RunPosted    RunStatus = "posted"    // 已生成凭证（凭证本身是草稿，过账在账期结算）
)

// Label 返回中文名。
func (s RunStatus) Label() string {
	switch s {
	case RunDraft:
		return "草稿"
	case RunConfirmed:
		return "已确认"
	case RunPosted:
		// 同报销单：只表示凭证已生成，凭证本身还是草稿（过账在账期结算）。
		return "已生成凭证"
	default:
		return string(s)
	}
}

// Run 是某期间（年月）的工资单。
type Run struct {
	ID     int64
	Period PeriodKey

	Status    RunStatus
	TaxTable  TaxTable
	VoucherID *int64

	Items []*Item

	CreatedBy string
}

// Totals 是工资单合计。
type Totals struct {
	Gross             money.Money // 应发合计
	AttendanceDeduct  money.Money
	OtherDeduct       money.Money
	InsuranceSelf     money.Money // 三险一金个人
	InsuranceCompany  money.Money // 三险一金单位
	SpecialAdditional money.Money
	IIT               money.Money
	Net               money.Money
	Headcount         int
}

// Totals 汇总工资单。
func (r *Run) Totals() Totals {
	var t Totals
	for _, it := range r.Items {
		t.Gross = t.Gross.Add(it.GrossPay)
		t.AttendanceDeduct = t.AttendanceDeduct.Add(it.AttendanceDeduction)
		t.OtherDeduct = t.OtherDeduct.Add(it.OtherDeduction)
		t.InsuranceSelf = t.InsuranceSelf.Add(it.Insurance.SelfTotal())
		t.InsuranceCompany = t.InsuranceCompany.Add(it.Insurance.CompanyTotal())
		t.SpecialAdditional = t.SpecialAdditional.Add(it.SpecialAdditional)
		t.IIT = t.IIT.Add(it.IIT)
		t.Net = t.Net.Add(it.NetPay)
		t.Headcount++
	}
	return t
}

// Validate 校验工资单内部的算术自洽性。
//
// 这一步很重要：实发合计必须等于「应发 − 扣款 − 个人社保 − 个税」，
// 差一分钱都会导致后面的凭证借贷不平。宁可在这里报错，
// 也不要把错误带到总账里去。
func (r *Run) Validate() error {
	if err := r.TaxTable.Validate(); err != nil {
		return err
	}
	if len(r.Items) == 0 {
		return errors.New("payroll: 工资单没有任何人员")
	}
	for i, it := range r.Items {
		who := fmt.Sprintf("第 %d 行", i+1)
		if it.Employee != nil {
			who = it.Employee.Name
		}
		if err := it.Validate(); err != nil {
			return fmt.Errorf("%s: %w", who, err)
		}
		want := it.GrossPay.
			Sub(it.AttendanceDeduction).
			Sub(it.OtherDeduction).
			Sub(it.Insurance.SelfTotal()).
			Sub(it.IIT)
		if it.NetPay != want {
			return fmt.Errorf("payroll: %s 实发 %s ≠ 应发 %s − 扣款 %s − 社保 %s − 个税 %s = %s",
				who, it.NetPay, it.GrossPay,
				it.AttendanceDeduction.Add(it.OtherDeduction),
				it.Insurance.SelfTotal(), it.IIT, want)
		}
	}
	return nil
}

// Validate 校验单条工资明细。
func (it *Item) Validate() error {
	neg := []struct {
		name string
		v    money.Money
	}{
		{"基本工资", it.BaseSalary}, {"岗位津贴", it.PostAllowance},
		{"加班费", it.OvertimePay}, {"奖金", it.Bonus}, {"其他应发", it.OtherIncome},
		{"考勤扣款", it.AttendanceDeduction}, {"其他扣款", it.OtherDeduction},
		{"专项附加扣除", it.SpecialAdditional}, {"其他扣除", it.OtherTaxDeduction},
		{"个税", it.IIT},
	}
	for _, n := range neg {
		if n.v.IsNegative() {
			return fmt.Errorf("%w: %s 为 %s", ErrNegativeAmount, n.name, n.v)
		}
	}
	if it.NetPay.IsNegative() {
		return fmt.Errorf("%w: 实发工资为 %s（扣款与社保超过应发）", ErrNegativeAmount, it.NetPay)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 生成凭证
// ---------------------------------------------------------------------------

// Entry 是生成凭证用的一条分录。
type Entry struct {
	AccountCode string
	Summary     string
	Debit       money.Money
	Credit      money.Money
	ContactID   *int64
	EmployeeID  *int64
	DeptID      *int64
}

// VoucherAccounts 是生成工资凭证所需的科目配置。
type VoucherAccounts struct {
	// PayableSalary 是「应付职工薪酬—工资」。
	PayableSalary string
	// PayableInsurance 是「应付职工薪酬—社会保险费」。
	PayableInsurance string
	// PayableHousingFund 是「应付职工薪酬—住房公积金」。
	PayableHousingFund string
	// PayableIIT 是「应交税费—应交个人所得税」。
	PayableIIT string
	// InsurancePersonal 是「其他应付款—社会保险费（个人）」。
	InsurancePersonal string
	// HousingFundPersonal 是「其他应付款—住房公积金（个人）」。
	HousingFundPersonal string
	// BankAccount 是发放工资的银行科目。
	BankAccount string
	// Summary 是凭证摘要前缀。
	Summary string
}

// DefaultVoucherAccounts 返回按本项目预置科目表对应的默认科目配置。
func DefaultVoucherAccounts() VoucherAccounts {
	return VoucherAccounts{
		PayableSalary:       "221101",
		PayableInsurance:    "221102",
		PayableHousingFund:  "221103",
		PayableIIT:          "222105",
		InsurancePersonal:   "224103",
		HousingFundPersonal: "224104",
		BankAccount:         "1002",
		Summary:             "计提工资",
	}
}

// BuildAccrualEntries 生成**计提**凭证的分录。
//
//	借：各员工所属费用科目（工资）        应发合计
//	    各员工所属费用科目（社保·单位）    单位社保
//	    各员工所属费用科目（公积金·单位）  单位公积金
//	  贷：应付职工薪酬—工资                应发合计
//	      应付职工薪酬—社会保险费          单位社保
//	      应付职工薪酬—住房公积金          单位公积金
//
// 借方按员工逐一列示而不是简单汇总：这样凭证上能看出「车间工资多少、
// 管理工资多少」，也便于按部门归集成本。
func (r *Run) BuildAccrualEntries(vc VoucherAccounts) ([]Entry, error) {
	if !r.Period.Valid() {
		return nil, fmt.Errorf("payroll: 工资期间非法: %v", r.Period)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	summary := vc.Summary
	if summary == "" {
		summary = "计提工资"
	}
	summary = fmt.Sprintf("%s %d年%02d月", summary, r.Period.Year, r.Period.Month)

	var entries []Entry
	t := r.Totals()

	// 借方：按员工归集到各自的费用科目
	for _, it := range r.Items {
		acc := it.ExpenseAccountCode()
		if acc == "" {
			return nil, fmt.Errorf("payroll: %s 未指定工资费用科目", it.employeeName())
		}
		entries = append(entries, Entry{
			AccountCode: acc,
			Summary:     summary,
			Debit:       it.GrossPay,
			EmployeeID:  ptrI64(it.EmployeeID),
			DeptID:      it.deptID(),
		})
	}

	// 借方：单位承担的社保与公积金（按员工归集）
	//
	// 这里必须带上部门维度：费用类科目（如 560202 管理费用—社会保险费）
	// 声明了按部门辅助核算，漏填会被过账校验直接拒绝。
	for _, it := range r.Items {
		acc := it.companyAccountCode()
		co := it.Insurance.CompanyTotal()
		if co.IsPositive() {
			entries = append(entries, Entry{
				AccountCode: acc,
				Summary:     summary + "（单位社保公积金）",
				Debit:       co,
				EmployeeID:  ptrI64(it.EmployeeID),
				DeptID:      it.deptID(),
			})
		}
	}

	// 贷方：应付职工薪酬
	if t.Gross.IsPositive() {
		entries = append(entries, Entry{
			AccountCode: vc.PayableSalary, Summary: summary, Credit: t.Gross,
		})
	}
	if t.InsuranceCompany.IsPositive() {
		// 单位承担部分里社保与公积金要分开挂科目
		var siCo, hfCo money.Money
		for _, it := range r.Items {
			siCo = siCo.Add(money.Sum(it.Insurance.PensionCo, it.Insurance.MedicalCo,
				it.Insurance.UnemploymentCo, it.Insurance.InjuryCo, it.Insurance.MaternityCo))
			hfCo = hfCo.Add(it.Insurance.HousingFundCo)
		}
		if siCo.IsPositive() {
			entries = append(entries, Entry{
				AccountCode: vc.PayableInsurance,
				Summary:     summary + "（单位社保）", Credit: siCo,
			})
		}
		if hfCo.IsPositive() {
			entries = append(entries, Entry{
				AccountCode: vc.PayableHousingFund,
				Summary:     summary + "（单位公积金）", Credit: hfCo,
			})
		}
	}

	return entries, nil
}

// BuildPaymentEntries 生成**发放**凭证的分录。
//
//	借：应付职工薪酬—工资                 应发合计
//	  贷：其他应付款—社会保险费（个人）    个人社保
//	      其他应付款—住房公积金（个人）    个人公积金
//	      应交税费—应交个人所得税          本月个税
//	      银行存款                        实发合计
//
// 考勤扣款不在这里体现：它已经在计提时通过「应发合计」减少了，
// 发放时按实发金额付出即可。
func (r *Run) BuildPaymentEntries(vc VoucherAccounts) ([]Entry, error) {
	if !r.Period.Valid() {
		return nil, fmt.Errorf("payroll: 工资期间非法: %v", r.Period)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	t := r.Totals()
	summary := fmt.Sprintf("发放工资 %d年%02d月", r.Period.Year, r.Period.Month)

	var entries []Entry
	entries = append(entries, Entry{
		AccountCode: vc.PayableSalary, Summary: summary, Debit: t.Gross,
	})

	// 个人承担部分：社保与公积金分别挂账
	var siSelf, hfSelf money.Money
	for _, it := range r.Items {
		siSelf = siSelf.Add(money.Sum(it.Insurance.PensionSelf,
			it.Insurance.MedicalSelf, it.Insurance.UnemploymentSelf))
		hfSelf = hfSelf.Add(it.Insurance.HousingFundSelf)
	}
	if siSelf.IsPositive() {
		entries = append(entries, Entry{
			AccountCode: vc.InsurancePersonal,
			Summary:     summary + "（代扣社保）", Credit: siSelf,
		})
	}
	if hfSelf.IsPositive() {
		entries = append(entries, Entry{
			AccountCode: vc.HousingFundPersonal,
			Summary:     summary + "（代扣公积金）", Credit: hfSelf,
		})
	}
	if t.IIT.IsPositive() {
		entries = append(entries, Entry{
			AccountCode: vc.PayableIIT,
			Summary:     summary + "（代扣个税）", Credit: t.IIT,
		})
	}
	if t.Net.IsPositive() {
		entries = append(entries, Entry{
			AccountCode: vc.BankAccount, Summary: summary, Credit: t.Net,
		})
	}
	return entries, nil
}

// ---------------------------------------------------------------------------
// Item 辅助
// ---------------------------------------------------------------------------

// ExpenseAccountCode 返回该员工的工资费用科目。
func (it *Item) ExpenseAccountCode() string {
	if it.Employee == nil {
		return ""
	}
	return it.Employee.ExpenseAccountCode
}

func (it *Item) companyAccountCode() string {
	if it.Employee == nil {
		return ""
	}
	if it.Employee.CompanyAccountCode != "" {
		return it.Employee.CompanyAccountCode
	}
	return it.Employee.ExpenseAccountCode
}

func (it *Item) deptID() *int64 {
	if it.Employee == nil {
		return nil
	}
	return it.Employee.DeptID
}

func (it *Item) employeeName() string {
	if it.Employee == nil {
		return fmt.Sprintf("员工#%d", it.EmployeeID)
	}
	return it.Employee.Name
}

func ptrI64(v int64) *int64 { return &v }

// ---------------------------------------------------------------------------
// 期间
// ---------------------------------------------------------------------------

// PeriodKey 是工资所属的会计期间（年月）。
//
// 刻意不直接依赖 period 包：工资模块只需要「年月」两个数，
// 引入整个期间状态机会让领域包之间产生不必要的耦合。
type PeriodKey struct {
	Year  int
	Month int
}

// NewPeriodKey 构造一个期间标识。
func NewPeriodKey(year, month int) PeriodKey {
	return PeriodKey{Year: year, Month: month}
}

// Valid 报告年月是否合法。
func (k PeriodKey) Valid() bool {
	return k.Year >= 1900 && k.Year <= 9999 && k.Month >= 1 && k.Month <= 12
}

// String 返回 "2025-09"。
func (k PeriodKey) String() string { return fmt.Sprintf("%04d-%02d", k.Year, k.Month) }

func monthStart(k PeriodKey) calendar.Date {
	d, _ := calendar.New(k.Year, k.Month, 1)
	return d
}

func monthEnd(k PeriodKey) calendar.Date {
	d, _ := calendar.New(k.Year, k.Month, calendar.DaysInMonth(k.Year, k.Month))
	return d
}

// SortItemsByEmployee 按员工编码排序，保证工资单输出稳定。
func SortItemsByEmployee(items []*Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Employee == nil || b.Employee == nil {
			return a.EmployeeID < b.EmployeeID
		}
		if a.Employee.Code != b.Employee.Code {
			return a.Employee.Code < b.Employee.Code
		}
		return a.EmployeeID < b.EmployeeID
	})
}

// SchemeTemplate 返回一张**费率为零**的社保方案空表。
//
// # 为什么给空表而不是「合理的默认值」
//
// 社保比例、缴费基数上下限按城市、按年度发布，各地差异很大。
// 预置一组看着像真的数字（比如养老 8%、医疗 2%），用户会直接点保存就开始用 ——
// 而那几乎必然与当地当年标准不符。错的比例算出来的社保、个税、实发
// 三处都是错的，而且**不会报任何错**：一张工资单看起来完全正常。
//
// 一张费率为零的表反而是安全的：算出来的社保是 0，用户一眼就知道
// 「还没填」，而不是「已经配好了」。界面会把它标成「未配置」。
//
// 上下限同理留零 = 不设限，并在界面上提示应当按社平工资的 60%~300% 填写。
func SchemeTemplate(name string) *InsuranceScheme {
	return &InsuranceScheme{
		Name: name,
		City: "",
		// 全部费率为 0：看起来就不像配好了
		Rates: InsuranceRates{},
	}
}

// IsConfigured 报告这个方案是否已经填过费率。
//
// 全零费率意味着「还没配」。界面据此显示「未配置」而不是让用户
// 以为社保已经算好了 —— 一个静默按 0 缴纳的工资单是最危险的。
func (sc *InsuranceScheme) IsConfigured() bool {
	if sc == nil {
		return false
	}
	r := sc.Rates
	return r.PensionSelf != 0 || r.MedicalSelf != 0 || r.UnemploymentSelf != 0 ||
		r.HousingFundSelf != 0 || r.PensionCo != 0 || r.MedicalCo != 0 ||
		r.UnemploymentCo != 0 || r.InjuryCo != 0 || r.MaternityCo != 0 ||
		r.HousingFundCo != 0
}

// Validate 校验方案是否可用于计算。
func (sc *InsuranceScheme) Validate() error {
	if sc == nil {
		return errors.New("payroll: 社保方案为空")
	}
	if strings.TrimSpace(sc.Name) == "" {
		return errors.New("payroll: 社保方案缺少名称")
	}
	if sc.BaseMin.IsNegative() || sc.BaseMax.IsNegative() {
		return fmt.Errorf("payroll: 方案「%s」的缴费基数上下限不能为负", sc.Name)
	}
	if sc.BaseMin.IsPositive() && sc.BaseMax.IsPositive() && sc.BaseMin > sc.BaseMax {
		return fmt.Errorf("payroll: 方案「%s」的缴费基数下限 %s 高于上限 %s",
			sc.Name, sc.BaseMin, sc.BaseMax)
	}
	if sc.HousingFundBaseMin.IsPositive() && sc.HousingFundBaseMax.IsPositive() &&
		sc.HousingFundBaseMin > sc.HousingFundBaseMax {
		return fmt.Errorf("payroll: 方案「%s」的公积金基数下限高于上限", sc.Name)
	}
	// 费率上限：任何一档超过 100% 一定是填错了（把「元」当成了「%」）
	for label, r := range map[string]money.Rate{
		"养老（个人）": sc.Rates.PensionSelf, "医疗（个人）": sc.Rates.MedicalSelf,
		"失业（个人）": sc.Rates.UnemploymentSelf, "公积金（个人）": sc.Rates.HousingFundSelf,
		"养老（单位）": sc.Rates.PensionCo, "医疗（单位）": sc.Rates.MedicalCo,
		"失业（单位）": sc.Rates.UnemploymentCo, "工伤（单位）": sc.Rates.InjuryCo,
		"生育（单位）": sc.Rates.MaternityCo, "公积金（单位）": sc.Rates.HousingFundCo,
	} {
		if r < 0 {
			return fmt.Errorf("payroll: 方案「%s」的%s费率为负", sc.Name, label)
		}
		if r > money.Rate(money.RateScale) {
			return fmt.Errorf(
				"payroll: 方案「%s」的%s费率 %s 超过 100%% —— 请确认填的是比例而不是金额",
				sc.Name, label, r)
		}
	}
	return nil
}
