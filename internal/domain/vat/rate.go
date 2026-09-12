package vat

import (
	"fmt"
	"sort"
	"strings"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// TaxationMethod 是计税方法。
//
// ★ 它必须与「税率」分开存。
// 同一个业务按不同计税方法算出来的税额完全不同：
//
//	一般计税   销项 = 不含税销售额 × 税率，进项可抵
//	简易计税   应纳 = 不含税销售额 × 征收率，进项**不得**抵扣
//	免税       应纳 = 0，对应的进项也不得抵扣（已抵的要转出）
//	零税率     销项 = 0，但进项**可以**退（出口退税）
//	不征税     不属于增值税征税范围，不产生销项也不涉及进项转出
//
// 「免税」与「零税率」都体现为税额为零，但进项处理相反 ——
// 只存一个「税率 0」是分不出来的。
type TaxationMethod string

// 计税方法。
const (
	MethodGeneral    TaxationMethod = "general"     // 一般计税
	MethodSimplified TaxationMethod = "simplified"  // 简易计税
	MethodExempt     TaxationMethod = "exempt"      // 免税
	MethodZeroRated  TaxationMethod = "zero_rated"  // 零税率
	MethodNotTaxable TaxationMethod = "not_taxable" // 不征税
)

// AllMethods 列出全部计税方法。
func AllMethods() []TaxationMethod {
	return []TaxationMethod{MethodGeneral, MethodSimplified, MethodExempt,
		MethodZeroRated, MethodNotTaxable}
}

// Valid 报告计税方法是否合法。
func (m TaxationMethod) Valid() bool {
	for _, x := range AllMethods() {
		if m == x {
			return true
		}
	}
	return false
}

// Label 返回中文名。
func (m TaxationMethod) Label() string {
	switch m {
	case MethodGeneral:
		return "一般计税"
	case MethodSimplified:
		return "简易计税"
	case MethodExempt:
		return "免税"
	case MethodZeroRated:
		return "零税率"
	case MethodNotTaxable:
		return "不征税"
	default:
		return string(m)
	}
}

// AllowsInputCredit 报告该方法下进项税额**原则上**能否抵扣。
//
//	一般计税  可以
//	零税率    可以（并可申请退税）
//	简易计税  不可以
//	免税      不可以
//	不征税    不涉及
func (m TaxationMethod) AllowsInputCredit() bool {
	return m == MethodGeneral || m == MethodZeroRated
}

// ---------------------------------------------------------------------------
// 纳税人主体
// ---------------------------------------------------------------------------

// Subject 是经营主体类型。
//
// ★ 它与「纳税人身份」也是两回事：同一项业务，单位和个人适用不同规则。
// 最典型的是出租不动产 ——
//
//	单位 / 个体工商户出租住房   按征收率计税
//	**个人**出租住房            3% 减按 1.5%
//
// 没有这个维度，「个人出租住房减按 1.5%」这条规则在模型里根本表达不出来，
// 只能要么丢掉、要么和「小规模出租不动产 3%」撞在同一个键上
// （那正是本包的政策表校验会拒绝的情形）。
type Subject string

// 经营主体类型。
const (
	SubjectAny          Subject = ""              // 不限
	SubjectEntity       Subject = "entity"        // 单位（企业）
	SubjectSelfEmployed Subject = "self_employed" // 个体工商户
	SubjectPerson       Subject = "person"        // 自然人（个人）
)

// AllSubjects 列出全部主体类型。
func AllSubjects() []Subject {
	return []Subject{SubjectAny, SubjectEntity, SubjectSelfEmployed, SubjectPerson}
}

// Label 返回中文名。
func (s Subject) Label() string {
	switch s {
	case SubjectAny:
		return "不限"
	case SubjectEntity:
		return "单位"
	case SubjectSelfEmployed:
		return "个体工商户"
	case SubjectPerson:
		return "个人"
	default:
		return string(s)
	}
}

// Valid 报告主体类型是否合法。
func (s Subject) Valid() bool {
	for _, x := range AllSubjects() {
		if s == x {
			return true
		}
	}
	return false
}

// matches 报告政策的主体限定是否覆盖给定主体。
//
// 政策写 SubjectAny（空）表示不限定主体；写具体主体时只对该主体生效。
func (s Subject) matches(got Subject) bool {
	return s == SubjectAny || s == got
}

// ---------------------------------------------------------------------------
// 业务类型
// ---------------------------------------------------------------------------

// BizCategory 是增值税业务类型（对应税率档次）。
//
// 按《增值税法》与现行税率结构划分。具体某项业务属于哪一类，
// 应按合同实质与税收分类编码判断，程序不替用户判定 ——
// 这里只提供可选项与各自的税率政策。
type BizCategory string

// 业务类型。
const (
	// CatGoods 销售货物、加工修理修配劳务、有形动产租赁、进口货物 → 13%
	CatGoods BizCategory = "goods"
	// CatTransportPostal 交通运输、邮政、基础电信 → 9%
	CatTransportPostal BizCategory = "transport_postal"
	// CatConstruction 建筑服务 → 9%
	CatConstruction BizCategory = "construction"
	// CatRealEstate 不动产租赁及销售、土地使用权转让 → 9%
	CatRealEstate BizCategory = "real_estate"
	// CatAgriculture 部分农产品等 → 9%
	CatAgriculture BizCategory = "agriculture"
	// CatModernService 信息技术、咨询、广告、金融、生活服务及其他无形资产 → 6%
	CatModernService BizCategory = "modern_service"
	// CatExport 出口货物及规定范围内的跨境服务、无形资产 → 0%
	CatExport BizCategory = "export"
	// CatUsedFixedAsset 销售自己使用过的固定资产等 → 3% 减按 2%。
	//
	// ★ 单独一档而不是并进 CatGoods。
	//
	// 并进 CatGoods 的话，政策表上会显示成
	// 「一般纳税人 / 销售货物… / 简易计税 / 3% / 2%（优惠）」——
	// 读起来像「一般纳税人销售货物按简易计税都减按 2%」，
	// 而实际只适用于**销售自己使用过的固定资产**这一类特定资产。
	// 会计照着那张表开票就会错。
	CatUsedFixedAsset BizCategory = "used_fixed_asset"
	// CatIndividualHousingRent 个人出租住房 → 3% 减按 1.5%。
	//
	// 理由同上：并进 CatRealEstate 会让「个人出租住房 1.5%」
	// 看起来像是「不动产租赁一律 1.5%」。
	CatIndividualHousingRent BizCategory = "individual_housing_rent"

	// CatAll 表示**适用于全部业务类型**，用于起征点免税这类
	// 与业务类型无关的政策。
	//
	// 用 CatOther 表达是错的：起征点看的是纳税期累计销售额，
	// 货物、服务、不动产都算。标成「其他」会让小规模会计
	// 在「销售货物」那一行找不到免税政策。
	CatAll BizCategory = "all"

	// CatOther 其他（由用户按实际情况判定）
	CatOther BizCategory = "other"
)

// AllCategories 列出全部业务类型。
func AllCategories() []BizCategory {
	return []BizCategory{CatGoods, CatTransportPostal, CatConstruction,
		CatRealEstate, CatAgriculture, CatModernService, CatExport,
		CatUsedFixedAsset, CatIndividualHousingRent, CatAll, CatOther}
}

// SelectableCategories 是**给用户选**的业务类型。
//
// 与 AllCategories 的区别：CatAll 不是一项业务，而是一个
// 「与业务类型无关」的标记，不该出现在录入界面的下拉里。
func SelectableCategories() []BizCategory {
	return []BizCategory{CatGoods, CatTransportPostal, CatConstruction,
		CatRealEstate, CatAgriculture, CatModernService, CatExport,
		CatUsedFixedAsset, CatIndividualHousingRent, CatOther}
}

// Label 返回中文名。
func (c BizCategory) Label() string {
	switch c {
	case CatGoods:
		return "销售货物、加工修理修配、有形动产租赁、进口货物"
	case CatTransportPostal:
		return "交通运输、邮政、基础电信"
	case CatConstruction:
		return "建筑服务"
	case CatRealEstate:
		return "不动产租赁及销售、土地使用权转让"
	case CatAgriculture:
		return "农产品等"
	case CatModernService:
		return "信息技术、咨询、广告、金融、生活服务及其他无形资产"
	case CatExport:
		return "出口货物及跨境服务、无形资产"
	case CatUsedFixedAsset:
		return "销售自己使用过的固定资产等"
	case CatIndividualHousingRent:
		return "个人出租住房"
	case CatAll:
		return "全部业务类型"
	case CatOther:
		return "其他（按合同实质与税收分类编码判断）"
	default:
		return string(c)
	}
}

// Valid 报告业务类型是否合法。
func (c BizCategory) Valid() bool {
	for _, x := range AllCategories() {
		if c == x {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 税率政策
// ---------------------------------------------------------------------------

// RatePolicy 是一条**带生效期**的增值税税率政策。
//
// # 为什么法定税率与实际税率要分开存
//
// 小规模纳税人的法定征收率是 3%，但 2026—2027 年减按 1% 征收。
// 报表要能同时回答两个问题：「法定是多少」「实际按多少算的」——
// 只存一个数，等优惠到期时无法判断某张发票当时适用哪个。
//
// # 为什么必须有生效/失效日期
//
// 1% 优惠、房地产老项目 5% 简易计税、销售旧固定资产 3% 减按 2%，
// 都是**阶段性或特定业务**的规则。写死在代码里，政策一到期
// 用户只能等新版本，而在此之前他会一直按错税率开票。
type RatePolicy struct {
	// Code 是政策唯一码，如 "general_13"、"small_1pct"。
	Code string
	// Name 是政策名称。
	Name string
	// Category 是适用业务类型。
	Category BizCategory
	// Status 是适用纳税人身份；空表示两种身份都适用。
	Status VATStatus
	// Subject 是适用经营主体；空表示不限。
	Subject Subject
	// Method 是计税方法。
	Method TaxationMethod

	// Treatment 是销项侧的**税收处理方式**：征税 / 免税 / 免税不退税 /
	// 零税率 / 不征税。
	//
	// ★ 它不能由税率反推：零税率与免税的税率都不适用，
	// 但进项处理完全相反。
	Treatment TaxTreatment
	// InputTax 是进项税额的处理方式：可抵扣 / 可抵扣并可退 /
	// 不得抵扣不得退 / 不涉及。
	InputTax InputTaxTreatment

	// StatutoryRate 是**法定**税率或征收率（百万分之一）。
	//
	// ★ 可为空（nil）。`nil` 表示「这项业务不适用税率」——
	// 免税、零税率、不征税都落在这里，而**不是**「税率是 0」。
	//
	// 用 0 表示免税会分不出「零税率」与「免税」：
	// 前者进项可以退，后者进项不得抵扣也不得退。
	StatutoryRate *money.Rate
	// PreferentialRate 是**实际适用**的优惠税率；nil 表示无优惠，
	// 实际按 StatutoryRate 执行。
	PreferentialRate *money.Rate

	// EffectiveFrom / EffectiveTo 是政策有效期（含端点）。
	// EffectiveTo 为零值表示长期有效。
	EffectiveFrom calendar.Date
	EffectiveTo   calendar.Date

	// Conditional 为真表示这条政策**不能自动命中**，必须由用户显式勾选。
	//
	// ★ 用于「是否命中某条例外情形」这类**事实认定**。
	//
	// 例：出口业务命中公告第七条异常情形的，不适用免税、按征收率征税。
	// 程序无从判断某笔出口是否命中异常情形 —— 替用户选了，
	// 要么让本该免税的业务交了税，要么让本该交税的业务免了税，
	// 而后者是**少缴税**。
	//
	// 因此这类政策在自动解析时被跳过，只在用户明确勾选
	// 「包含需人工确认的例外情形」时才参与匹配。
	Conditional bool

	// LegalBasis 是政策依据，如「财政部、税务总局 2026 年第 10 号公告」。
	LegalBasis string
	// Version 是政策版本，便于政策调整后区分历史数据。
	Version string
	// Note 是补充说明（适用条件、例外情形）。
	Note string
}

// Rate 返回**实际适用**的税率。
//
// 第二个返回值为假表示「不适用税率」—— 免税、零税率、不征税。
// 调用方必须处理这一情形，不能把假当成 0%。
func (p RatePolicy) Rate() (money.Rate, bool) {
	if p.PreferentialRate != nil {
		return *p.PreferentialRate, true
	}
	if p.StatutoryRate != nil {
		return *p.StatutoryRate, true
	}
	return 0, false
}

// MustRate 在确信适用税率时取值；不适用时返回 0。
//
// 只用于展示拼接等**已经判过 Treatment** 的场景。
func (p RatePolicy) MustRate() money.Rate {
	r, _ := p.Rate()
	return r
}

// DisplayRate 返回给人看的税率文本。
//
// 不适用税率时返回「免税」「零税率」「不征税」这类**文字**，
// 而不是一个 0% —— 界面上显示 0% 会被读成「税率为零」，
// 而免税与零税率的进项处理是相反的。
func (p RatePolicy) DisplayRate() string {
	if r, ok := p.Rate(); ok {
		return r.String()
	}
	return p.Treatment.Label()
}

// HasPreference 报告该政策是否含优惠。
func (p RatePolicy) HasPreference() bool {
	if p.PreferentialRate == nil {
		return false
	}
	if p.StatutoryRate == nil {
		return true
	}
	return *p.PreferentialRate != *p.StatutoryRate
}

// StatutoryDisplay 返回法定税率的展示文本（不适用时为空）。
func (p RatePolicy) StatutoryDisplay() string {
	if p.StatutoryRate == nil {
		return ""
	}
	return p.StatutoryRate.String()
}

// PreferentialDisplay 返回优惠税率的展示文本（无优惠时为空）。
func (p RatePolicy) PreferentialDisplay() string {
	if p.PreferentialRate == nil {
		return ""
	}
	return p.PreferentialRate.String()
}

// ActiveOn 报告政策在指定日期是否有效。
func (p RatePolicy) ActiveOn(on calendar.Date) bool {
	if !p.EffectiveFrom.Valid() || on.Before(p.EffectiveFrom) {
		return false
	}
	if p.EffectiveTo.Valid() && on.After(p.EffectiveTo) {
		return false
	}
	return true
}

// HasOutputTax 报告该方法是否产生销项税额。
//
// ★ 「免税」与「零税率」都体现为税额为零，但**进项处理相反**：
// 免税对应的进项不得抵扣（已抵的要转出），零税率对应的进项可以退。
// 只存一个「税率 0」是分不出这两种情况的 —— 这正是计税方法必须
// 与税率分开存的原因。
func (m TaxationMethod) HasOutputTax() bool {
	return m != MethodExempt && m != MethodZeroRated && m != MethodNotTaxable
}

// HasOutputTax 报告该政策是否产生销项税额。
func (p RatePolicy) HasOutputTax() bool { return p.Method.HasOutputTax() }

// Validate 校验政策。
func (p RatePolicy) Validate() error {
	if strings.TrimSpace(p.Code) == "" {
		return fmt.Errorf("vat: 税率政策缺少编码")
	}
	if !p.Category.Valid() {
		return fmt.Errorf("vat: 税率政策 %s 的业务类型 %q 未知", p.Code, p.Category)
	}
	if !p.Method.Valid() {
		return fmt.Errorf("vat: 税率政策 %s 的计税方法 %q 未知", p.Code, p.Method)
	}
	if p.Status != "" && !p.Status.Valid() {
		return fmt.Errorf("vat: 税率政策 %s 的纳税人身份 %q 未知", p.Code, p.Status)
	}
	if !p.Subject.Valid() {
		return fmt.Errorf("vat: 税率政策 %s 的经营主体 %q 未知", p.Code, p.Subject)
	}
	if !p.EffectiveFrom.Valid() {
		return fmt.Errorf("vat: 税率政策 %s 缺少生效日期", p.Code)
	}
	if p.EffectiveTo.Valid() && p.EffectiveTo.Before(p.EffectiveFrom) {
		return fmt.Errorf("vat: 税率政策 %s 的失效日早于生效日", p.Code)
	}
	if !p.Treatment.Valid() {
		return fmt.Errorf("vat: 税率政策 %s 的税收处理方式 %q 未知", p.Code, p.Treatment)
	}
	if !p.InputTax.Valid() {
		return fmt.Errorf("vat: 税率政策 %s 的进项处理方式 %q 未知", p.Code, p.InputTax)
	}
	// ★ 税率可空性与税收处理方式必须自洽。
	//
	//	征税        → 必须有税率或征收率
	//	免税/零税率/不征税 → 必须**没有**税率（用 nil，不是 0）
	//
	// 这条恒等式防的是「用 0 表示免税」——那正是让人分不出
	// 零税率与免税的根源。
	if p.Treatment.RequiresRate() {
		if p.StatutoryRate == nil {
			return fmt.Errorf(
				"vat: 税率政策 %s 是征税项目，必须填法定税率或征收率"+
					"（免税/零税率/不征税才留空；留空表示不适用，不是 0%%）", p.Code)
		}
	} else if p.StatutoryRate != nil {
		return fmt.Errorf(
			"vat: 税率政策 %s 是%s，不该填税率 —— "+
				"税率字段留空表示「不适用」，用 0 表示会与零税率混淆",
			p.Code, p.Treatment.Label())
	}
	for _, r := range []*money.Rate{p.StatutoryRate, p.PreferentialRate} {
		if r == nil {
			continue
		}
		if *r < 0 {
			return fmt.Errorf("vat: 税率政策 %s 的税率不能为负", p.Code)
		}
		if *r > money.Rate(money.RateScale) {
			return fmt.Errorf("vat: 税率政策 %s 的税率超过 100%%", p.Code)
		}
	}
	return nil
}

// Resolution 是一次税率查询的结果。
//
// ★ 它带一个 Exact 标志与解释文本，因为「查不到」不该只回一句报错。
//
// 最典型的情形：小规模纳税人查「出口 + 零税率」。
// 零税率只适用于采用一般计税的一般纳税人，所以确实没有精确匹配的政策；
// 但这时回一句「找不到政策」是**没用**的 —— 用户真正需要知道的是
// 「你的身份不适用零税率，出口适用免税、不退税，进项不得抵扣也不得退税」。
//
// 因此查不到精确匹配时，会退而找同一业务类型下**确实适用**的政策，
// 并把差异说清楚。Exact 为假时调用方**必须**把 Note 显示给用户，
// 不能只显示税率。
type Resolution struct {
	Policy RatePolicy
	// Exact 为真表示这是精确匹配（身份、主体、业务、方法、日期全中）。
	Exact bool
	// Note 是随结果一起必须显示给用户的说明。
	//
	// 精确匹配时它是政策自身的说明（适用条件、例外情形）；
	// Exact 为假时它是「为什么不适用你问的那个 + 实际适用什么」。
	Note string
	// RequestedMethod 是用户请求的计税方法（用于解释差异）。
	RequestedMethod TaxationMethod
}

// Rate 返回实际适用的税率；第二个值为假表示不适用税率。
func (r Resolution) Rate() (money.Rate, bool) { return r.Policy.Rate() }

// DisplayRate 返回给人看的税率文本（不适用时是「免税」这类文字）。
func (r Resolution) DisplayRate() string { return r.Policy.DisplayRate() }

// MustRate 返回实际适用税率；不适用（免税/零税率/不征税）时返回 0。
func (r Resolution) MustRate() money.Rate { return r.Policy.MustRate() }

// Treatment 返回税收处理方式。
func (r Resolution) Treatment() TaxTreatment { return r.Policy.Treatment }

// HasPreference 表示命中的政策是否带阶段性优惠（如小规模 1% 减征）。
func (r Resolution) HasPreference() bool { return r.Policy.HasPreference() }

// InputTax 返回进项税额处理方式。
func (r Resolution) InputTax() InputTaxTreatment { return r.Policy.InputTax }

// ResolveRate 在政策表里挑出「某身份、某主体、某业务、某方法、某日期」
// 适用的政策。
//
// 选择规则（按优先级）：
//
//  1. 生效期覆盖业务发生日
//  2. 身份匹配（政策未指定身份则两种都算匹配）
//  3. 主体匹配（政策未限定主体则不限）
//  4. 业务类型与方法都匹配（CatAll 对任何业务类型都命中）
//  5. 业务类型精确匹配优先于 CatAll；主体限定优先于不限；生效日更晚优先
//
// 找不到精确匹配时**不直接报错**，而是退一步找同一业务类型下
// 该身份真正适用的政策，返回 Exact=false + 一段解释。
// 连退一步都找不到时才报错。
func ResolveRate(policies []RatePolicy, status VATStatus, subject Subject,
	category BizCategory, method TaxationMethod, on calendar.Date) (Resolution, error) {
	return ResolveRateWith(policies, status, subject, category, method, on, false)
}

// ResolveRateWith 与 ResolveRate 相同，但可以指定是否**包含需人工确认的
// 例外情形**（如出口命中公告第七条异常情形）。
//
// 默认不包含：例外情形属事实认定，替用户选了就可能少缴税。
func ResolveRateWith(policies []RatePolicy, status VATStatus, subject Subject,
	category BizCategory, method TaxationMethod, on calendar.Date,
	includeConditional bool) (Resolution, error) {

	if best, ok := pick(policies, status, subject, category, method, on,
		includeConditional); ok {
		// 精确匹配也要带上政策自己的说明 —— 像「小规模不动产不适用 1% 减征」
		// 这种话，正是用户在该条政策上最需要看到的。
		return Resolution{
			Policy: best, Exact: true, RequestedMethod: method, Note: best.Note,
		}, nil
	}

	// 退一步：同一业务类型下，这个身份**究竟**适用什么政策？
	//
	// 这一步只用于**解释**，不会拿它当结果去算税 —— 返回的
	// Exact=false 让调用方必须把说明显示出来。
	if alt, ok := pickAnyMethod(policies, status, subject, category, on); ok {
		// 「退一步」只在**用户问的是一件可以换答案的事**时才有意义。
		//
		// 简易计税、零税率属于「计税方法」层面 —— 用户问「能不能简易计税 /
		// 是不是零税率」，回答「不是，实际按 XX 处理」是有效信息。
		// 典型：小规模查出口零税率 → 实际免税不退税。
		//
		// 免税、不征税属于「免征结论」层面 —— 一般纳税人卖货问免税，
		// 就是不存在，没有另一套处理可讲。这时**报错**比返回一个税率安全，
		// 否则用户可能真去开免税发票；但仍把「实际适用什么」写进错误信息。
		if alt.Treatment != TreatmentTaxable ||
			method == MethodSimplified || method == MethodZeroRated {
			return Resolution{
				Policy: alt, Exact: false, RequestedMethod: method,
				Note: joinNote(alt.Note,
					explainMismatch(status, subject, category, method, alt)),
			}, nil
		}
		return Resolution{}, fmt.Errorf(
			"vat: %s 的「%s」不适用%s；该业务实际适用 %s（%s %s）—— "+
				"请改用%s，或核对税率政策表",
			status.Label(), category.Label(), method.Label(),
			alt.Name, alt.Treatment.Label(), alt.DisplayRate(), alt.Method.Label())
	}

	return Resolution{}, fmt.Errorf(
		"vat: 找不到适用的税率政策（%s / %s / %s / %s / %s）—— "+
			"请检查税率政策表的生效期与业务类型，不要按默认税率计算",
		status.Label(), subject.Label(), category.Label(), method.Label(), on)
}

// pick 找精确匹配（业务类型与方法都要对得上）。
func pick(policies []RatePolicy, status VATStatus, subject Subject,
	category BizCategory, method TaxationMethod, on calendar.Date,
	includeConditional bool) (RatePolicy, bool) {

	var (
		best    RatePolicy
		hasBest bool
	)
	for _, p := range policies {
		if !p.ActiveOn(on) {
			continue
		}
		// 需人工确认的例外情形不参与自动匹配
		if p.Conditional && !includeConditional {
			continue
		}
		if p.Status != "" && p.Status != status {
			continue
		}
		if !p.Subject.matches(subject) {
			continue
		}
		if p.Category != category && p.Category != CatAll {
			continue
		}
		if p.Method != method {
			continue
		}
		// 优先级（从高到低）：
		//  1. 业务类型精确匹配 优先于 CatAll
		//  2. 主体限定 优先于「不限主体」
		//     （个人出租住房 1.5% 必须盖过小规模出租不动产 3%）
		//  3. 用户**显式请求**的例外情形 优先于一般政策
		//     （勾选了「包含需人工确认的例外」就说明他认定命中了例外）
		//  4. 生效日更晚的优先
		pExact := p.Category == category
		bExact := best.Category == category
		pSubj := p.Subject != SubjectAny
		bSubj := best.Subject != SubjectAny
		pExc := p.Conditional && includeConditional
		bExc := best.Conditional && includeConditional
		better := !hasBest ||
			(pExact && !bExact) ||
			(pExact == bExact && pSubj && !bSubj) ||
			(pExact == bExact && pSubj == bSubj && pExc && !bExc) ||
			(pExact == bExact && pSubj == bSubj && pExc == bExc &&
				p.EffectiveFrom.After(best.EffectiveFrom))
		if better {
			best, hasBest = p, true
		}
	}
	return best, hasBest
}

// pickAnyMethod 在业务类型完全匹配的前提下，不限计税方法再找一次。
//
// 用于「身份与业务都合法，只是计税方法不对」这种情形 ——
// 典型就是小规模查出口零税率：业务类型对，但零税率的方法
// 是「一般计税」以外的处理，实际适用的是免税。
func pickAnyMethod(policies []RatePolicy, status VATStatus, subject Subject,
	category BizCategory, on calendar.Date) (RatePolicy, bool) {

	var (
		best    RatePolicy
		hasBest bool
	)
	for _, p := range policies {
		if !p.ActiveOn(on) {
			continue
		}
		// 需人工确认的例外情形不参与「退一步解释」——
		// 拿它来解释会让用户以为「本来就要交税」。
		if p.Conditional {
			continue
		}
		if p.Status != "" && p.Status != status {
			continue
		}
		if !p.Subject.matches(subject) {
			continue
		}
		// 这里要求**精确**的业务类型：退一步是为了解释
		// 「同一业务在这个身份下按什么处理」，用 CatAll 的
		// 起征点政策来解释出口零税率是没有意义的。
		if p.Category != category {
			continue
		}
		if !hasBest {
			best, hasBest = p, true
		}
	}
	return best, hasBest
}

// joinNote 把政策自身的说明与「为什么换了一条」的解释拼成一段。
func joinNote(policyNote, explain string) string {
	switch {
	case policyNote == "":
		return explain
	case explain == "":
		return policyNote
	default:
		return policyNote + "\n" + explain
	}
}

// explainMismatch 拼出「为什么不适用你问的那个方法、实际适用什么」。
func explainMismatch(status VATStatus, subject Subject, category BizCategory,
	method TaxationMethod, alt RatePolicy) string {

	var b strings.Builder
	fmt.Fprintf(&b, "当前纳税人身份不适用%s。", method.Label())

	// 零税率是最常被误用的一档，单独把话说透
	if method == MethodZeroRated {
		fmt.Fprintf(&b, "匹配政策：%s；相关进项税额%s。",
			alt.Name, alt.InputTax.Label())
		if status == VATSmallScale {
			b.WriteString("零税率意味着进项可以退（出口退税），" +
				"而小规模纳税人的进项本来就不得抵扣、不得退税 —— " +
				"出口适用的是免税，已含进项计入成本。")
			b.WriteString("存在征税例外时需另行判断。")
		}
		return b.String()
	}

	fmt.Fprintf(&b, "匹配政策：%s（%s / %s / %s）。",
		alt.Name, alt.Treatment.Label(), alt.Method.Label(), alt.DisplayRate())
	return b.String()
}

// PoliciesFor 返回某身份在某日期的全部有效政策，按税率降序。
// 界面用它填「业务类型 → 税率」的下拉。
func PoliciesFor(policies []RatePolicy, status VATStatus, subject Subject,
	on calendar.Date) []RatePolicy {

	var out []RatePolicy
	for _, p := range policies {
		if !p.ActiveOn(on) {
			continue
		}
		if p.Status != "" && p.Status != status {
			continue
		}
		if !p.Subject.matches(subject) {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, oki := out[i].Rate()
		rj, okj := out[j].Rate()
		if oki != okj {
			return oki // 有税率的排前面
		}
		if oki && ri != rj {
			return ri > rj
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// ValidatePolicies 校验整张政策表，并检查「同身份+同业务+同方法+同生效日」
// 不重复（重复会让 ResolveRate 的结果依赖遍历顺序）。
func ValidatePolicies(policies []RatePolicy) error {
	if len(policies) == 0 {
		return fmt.Errorf("vat: 税率政策表为空")
	}
	seen := map[string]bool{}
	type key struct {
		status      VATStatus
		subject     Subject
		category    BizCategory
		method      TaxationMethod
		from        string
		conditional bool
	}
	keys := map[key]string{}
	for _, p := range policies {
		if err := p.Validate(); err != nil {
			return err
		}
		if seen[p.Code] {
			return fmt.Errorf("vat: 税率政策编码重复：%s", p.Code)
		}
		seen[p.Code] = true
		k := key{p.Status, p.Subject, p.Category, p.Method,
			p.EffectiveFrom.String(), p.Conditional}
		if prev, dup := keys[k]; dup {
			return fmt.Errorf(
				"vat: 政策 %s 与 %s 的「身份+主体+业务+方法+生效日+是否需人工确认」"+
					"完全相同，取哪条取决于遍历顺序", prev, p.Code)
		}
		keys[k] = p.Code
	}
	return nil
}
