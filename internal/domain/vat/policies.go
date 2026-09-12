package vat

import (
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// 政策依据（写成常量便于界面展示与审计追溯）。
const (
	// BasisVATLaw 《中华人民共和国增值税法》，2026-01-01 起施行。
	BasisVATLaw = "《中华人民共和国增值税法》"
	// BasisImplementationRegulations 《增值税法实施条例》。
	BasisImplementationRegulations = "《中华人民共和国增值税法实施条例》"
	// BasisAnnouncement2026No10 财政部、税务总局 2026 年第 10 号公告
	// （小规模纳税人减免、简易计税等）。
	BasisAnnouncement2026No10 = "财政部、税务总局 2026 年第 10 号公告"
	// BasisAnnouncement2026No25 财政部、税务总局 2026 年第 25 号公告
	// （2026-09-01 起进一步明确不得抵扣范围）。
	BasisAnnouncement2026No25 = "财政部、税务总局 2026 年第 25 号公告"
	// BasisAnnouncement2026No11 财政部、税务总局 2026 年第 11 号公告
	// （出口货物、跨境服务与无形资产的增值税处理；小规模纳税人出口
	// 适用免税、不退税）。
	BasisAnnouncement2026No11 = "财政部、税务总局 2026 年第 11 号公告"
)

// 政策版本号。政策调整时递增，便于区分历史数据是用哪一版算的。
const PolicyVersion2026 = "2026.2"

// ratePtr 把税率取成指针。
//
// ★ 政策表里税率是**可空**的：nil 表示「不适用税率」（免税、零税率、不征税），
// 而不是「税率是 0」。用 0 表示免税会与零税率混淆 ——
// 前者进项不得抵扣也不得退，后者进项可以退。
func ratePtr(r money.Rate) *money.Rate { return &r }

// pct 把百分比整数写成 money.Rate（百万分之一）。
//
//	RateScale = 1_000_000 表示 100%，因此 1% = 10_000。
//	13% → 130_000，3% → 30_000。
func pct(n int64) money.Rate { return money.Rate(n * money.RateScale / 100) }

// pctHundredth 把「百分比的百分之一」写成 money.Rate。
//
//	150 → 1.5%（个人出租住房的减征率）
//
// 单独一个函数而不是复用 pct：1.5% 这类非整数百分比
// 用 pct 表达会写成 pct(1500/1000) 这种看不出对错的算式 ——
// 我第一版就是这么写的，算出来是 15%，错了十倍。
func pctHundredth(n int64) money.Rate {
	return money.Rate(n * money.RateScale / 10000)
}

// DefaultPolicies2026 返回 2026 年适用的增值税税率政策表。
//
// # 这是**默认数据**，不是写死的逻辑
//
// 全部经 ValidatePolicies 校验，并保存在账套设置里，用户可改。
// 之所以给出这一份，是因为空表会让「找不到适用政策」变成一个
// 用户无法自己解决的死局；但**每一项都带政策依据与生效期**，
// 政策变化时改数据即可，不必改代码。
//
// ⚠️ 发布前请按现行有效文件逐条复核 —— 尤其是小规模 1% 优惠的
// 截止日（本表按 2027-12-31）与不动产相关规则。程序不替你判断
// 某项业务属于哪一类，那要按合同实质与税收分类编码定。
func DefaultPolicies2026() []RatePolicy {
	// 增值税法施行日
	lawFrom := calendar.Date{Year: 2026, Month: 1, Day: 1}
	// 小规模 1% 优惠截止日（含当日）
	prefTo := calendar.Date{Year: 2027, Month: 12, Day: 31}
	// ★ 法定征收率政策的起始日 = 优惠截止日的次日。
	//
	// 同一档业务（身份+业务+方法）在时间轴上**只能有一条政策生效**，
	// 否则「某天该按哪个税率」就取决于遍历顺序。
	// 所以不写成「两条都从施行日生效、靠优先级挑」，
	// 而是让它们**首尾相接**：
	//
	//	2026-01-01 ~ 2027-12-31   优惠政策（法定 3%，实际减按 1%）
	//	2028-01-01 ~              法定政策（3%）
	//
	// 优惠到期后程序自动回落到 3%，不需要改代码，也不会出现
	// 「同一天两条政策都算命中」这种说不清的情况。
	statutoryFrom := prefTo.AddDays(1)

	return []RatePolicy{
		// ---- 一般计税：13% ----
		{
			Code: "general_goods_13", Name: "销售货物等 13%",
			Category: CatGoods, Status: VATGeneral, Method: MethodGeneral,
			Treatment: TreatmentTaxable, InputTax: InputDeductible,
			StatutoryRate: ratePtr(pct(13)), EffectiveFrom: lawFrom,
			LegalBasis: BasisVATLaw, Version: PolicyVersion2026,
			Note: "销售货物、加工修理修配劳务、有形动产租赁、进口货物",
		},
		// ---- 一般计税：9% ----
		{
			Code: "general_transport_9", Name: "交通运输、邮政、基础电信 9%",
			Category: CatTransportPostal, Status: VATGeneral, Method: MethodGeneral,
			Treatment: TreatmentTaxable, InputTax: InputDeductible,
			StatutoryRate: ratePtr(pct(9)), EffectiveFrom: lawFrom,
			LegalBasis: BasisVATLaw, Version: PolicyVersion2026,
		},
		{
			Code: "general_construction_9", Name: "建筑服务 9%",
			Category: CatConstruction, Status: VATGeneral, Method: MethodGeneral,
			Treatment: TreatmentTaxable, InputTax: InputDeductible,
			StatutoryRate: ratePtr(pct(9)), EffectiveFrom: lawFrom,
			LegalBasis: BasisVATLaw, Version: PolicyVersion2026,
		},
		{
			Code: "general_realestate_9", Name: "不动产租赁及销售、土地使用权转让 9%",
			Category: CatRealEstate, Status: VATGeneral, Method: MethodGeneral,
			Treatment: TreatmentTaxable, InputTax: InputDeductible,
			StatutoryRate: ratePtr(pct(9)), EffectiveFrom: lawFrom,
			LegalBasis: BasisVATLaw, Version: PolicyVersion2026,
			Note: "一般计税下适用 9%。房地产老项目等可选择 5% 简易计税，见单独政策",
		},
		{
			Code: "general_agriculture_9", Name: "农产品等 9%",
			Category: CatAgriculture, Status: VATGeneral, Method: MethodGeneral,
			Treatment: TreatmentTaxable, InputTax: InputDeductible,
			StatutoryRate: ratePtr(pct(9)), EffectiveFrom: lawFrom,
			LegalBasis: BasisVATLaw, Version: PolicyVersion2026,
		},
		// ---- 一般计税：6% ----
		{
			Code: "general_service_6", Name: "现代服务等 6%",
			Category: CatModernService, Status: VATGeneral, Method: MethodGeneral,
			Treatment: TreatmentTaxable, InputTax: InputDeductible,
			StatutoryRate: ratePtr(pct(6)), EffectiveFrom: lawFrom,
			LegalBasis: BasisVATLaw, Version: PolicyVersion2026,
			Note: "信息技术、咨询、广告、金融、生活服务及其他无形资产。" +
				"软件产品销售属 13%（见 goods 一档），SaaS、实施、维护与" +
				"信息技术服务属本档 6%，应按合同实质与税收分类编码判断",
		},
		// ---- 零税率 ----
		{
			Code: "zero_export", Name: "出口货物及跨境服务、无形资产（零税率）",
			Category: CatExport, Status: VATGeneral, Method: MethodZeroRated,
			// ★ 零税率：税率**留空**（不适用），进项可抵扣并可退。
			// 写 0 会与「免税」混淆 —— 免税的进项不得抵扣也不得退。
			Treatment: TreatmentZeroRated, InputTax: InputDeductibleRefundable,
			EffectiveFrom: lawFrom,
			LegalBasis:    BasisVATLaw, Version: PolicyVersion2026,
			Note: "零税率与免税不同：零税率的进项可以退（出口退税），" +
				"免税的进项不得抵扣也不得退。" +
				"★ 本政策只适用于采用一般计税的一般纳税人，不放宽给小规模纳税人",
		},
		// ---- 小规模纳税人出口：免税、不退税 ----
		//
		// ★ 小规模纳税人**不适用零税率**。
		//
		// 零税率意味着进项可以退（出口退税），而小规模纳税人的进项
		// 本来就不得抵扣、不得退税 —— 出口货物适用的是**免税**，
		// 已含的进项计入成本。
		// 依据：财政部、税务总局 2026 年第 11 号公告。
		{
			// 出口货物与符合范围的跨境服务、无形资产，在小规模纳税人下
			// 是**同一种处理**（免税、不退税），所以是**一条**政策覆盖两种情形。
			// 拆成两条会落在同一个「身份+主体+业务+方法+生效日」键上 ——
			// 那样「某天该用哪条」就取决于遍历顺序。
			Code:     "small_export_exempt",
			Name:     "小规模纳税人出口货物、跨境服务与无形资产（免税、不退税）",
			Category: CatExport, Status: VATSmallScale, Method: MethodSimplified,
			Treatment:     TreatmentExemptNoRefund,
			InputTax:      InputNonDeductibleNonRefundable,
			EffectiveFrom: lawFrom,
			LegalBasis:    BasisAnnouncement2026No11, Version: PolicyVersion2026,
			Note: "小规模纳税人出口货物适用增值税免税，不退税；" +
				"符合范围的跨境服务、无形资产采用简易计税时同样按免税处理。" +
				"相关进项税额不得抵扣也不得退税，应计入成本。" +
				"★ 零税率只适用于采用一般计税的一般纳税人，不放宽给小规模纳税人",
		},
		{
			Code:     "export_taxable_exception",
			Name:     "出口业务命中异常情形（按征收率征税）",
			Category: CatExport, Status: VATSmallScale, Method: MethodSimplified,
			// ★ 需人工确认：是否命中公告第七条异常情形属**事实认定**，
			// 程序无从判断。替用户选了就可能让本该交税的业务免了税 ——
			// 那是少缴税。
			Conditional:   true,
			Treatment:     TreatmentTaxable,
			InputTax:      InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), EffectiveFrom: lawFrom,
			LegalBasis: BasisAnnouncement2026No11, Version: PolicyVersion2026,
			Note: "命中公告第七条异常情形的出口业务，不适用免税，" +
				"按征收率计算缴纳增值税。是否命中属事实认定，" +
				"请按公告条款与实际业务判断后再勾选本政策",
		},

		// ---- 小规模纳税人：3% 法定征收率 ----
		{
			Code: "small_goods_3", Name: "小规模销售货物等 3%（法定征收率）",
			Category: CatGoods, Status: VATSmallScale, Method: MethodSimplified,
			Treatment:     TreatmentTaxable,
			InputTax:      InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), EffectiveFrom: statutoryFrom,
			LegalBasis: BasisVATLaw, Version: PolicyVersion2026,
			Note: "1% 优惠到期后按法定征收率 3% 执行",
		},
		{
			Code: "small_service_3", Name: "小规模提供服务等 3%（法定征收率）",
			Category: CatModernService, Status: VATSmallScale, Method: MethodSimplified,
			Treatment:     TreatmentTaxable,
			InputTax:      InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), EffectiveFrom: statutoryFrom,
			LegalBasis: BasisVATLaw, Version: PolicyVersion2026,
		},
		{
			Code: "small_transport_3", Name: "小规模交通运输等 3%（法定征收率）",
			Category: CatTransportPostal, Status: VATSmallScale, Method: MethodSimplified,
			Treatment:     TreatmentTaxable,
			InputTax:      InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), EffectiveFrom: statutoryFrom,
			LegalBasis: BasisVATLaw, Version: PolicyVersion2026,
		},
		{
			Code: "small_construction_3", Name: "小规模建筑服务 3%（法定征收率）",
			Category: CatConstruction, Status: VATSmallScale, Method: MethodSimplified,
			Treatment:     TreatmentTaxable,
			InputTax:      InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), EffectiveFrom: statutoryFrom,
			LegalBasis: BasisVATLaw, Version: PolicyVersion2026,
		},
		{
			Code: "small_agriculture_3", Name: "小规模农产品等 3%（法定征收率）",
			Category: CatAgriculture, Status: VATSmallScale, Method: MethodSimplified,
			Treatment:     TreatmentTaxable,
			InputTax:      InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), EffectiveFrom: statutoryFrom,
			LegalBasis: BasisVATLaw, Version: PolicyVersion2026,
		},
		// ---- 小规模：不动产**单独规则**，不自动套用 1% ----
		{
			Code: "small_realestate_3", Name: "小规模销售、出租不动产或转让土地使用权 3%",
			Category: CatRealEstate, Status: VATSmallScale, Method: MethodSimplified,
			Treatment: TreatmentTaxable, InputTax: InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), EffectiveFrom: lawFrom,
			LegalBasis: BasisAnnouncement2026No10, Version: PolicyVersion2026,
			// ★ 这一条刻意**不给优惠税率**，也刻意不设截止日 ——
			//
			// 小规模纳税人销售、出租不动产或者转让土地使用权，
			// **不能**自动套用 1% 减征 —— 它走单独的业务规则。
			// 如果在这里也填 1%，程序就会默默按 1% 开票，
			// 而正确做法是按 3% 征收率（另有规定的从其规定）。
			// 需要 1% 的情形必须由用户显式选择相应政策。
			Note: "★ 不适用 1% 减征，按 3% 征收率。" +
				"销售、出租不动产或转让土地使用权有单独规定，请勿套用其他业务的优惠",
		},
		// ---- 小规模：1% 优惠（2026—2027），带截止日 ----
		{
			Code: "small_pref_1_goods", Name: "小规模减按 1%（销售货物等）",
			Category: CatGoods, Status: VATSmallScale, Method: MethodSimplified,
			Treatment:     TreatmentTaxable,
			InputTax:      InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), PreferentialRate: ratePtr(pct(1)),
			EffectiveFrom: lawFrom, EffectiveTo: prefTo,
			LegalBasis: BasisAnnouncement2026No10, Version: PolicyVersion2026,
			Note: "法定征收率 3%，2026-01-01 至 2027-12-31 减按 1%。" +
				"★ 截止日后本政策失效，程序会回落到 3% —— 这正是把优惠做成" +
				"带截止期的数据、而不是写死在代码里的原因",
		},
		{
			Code: "small_pref_1_service", Name: "小规模减按 1%（服务等）",
			Category: CatModernService, Status: VATSmallScale, Method: MethodSimplified,
			Treatment:     TreatmentTaxable,
			InputTax:      InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), PreferentialRate: ratePtr(pct(1)),
			EffectiveFrom: lawFrom, EffectiveTo: prefTo,
			LegalBasis: BasisAnnouncement2026No10, Version: PolicyVersion2026,
		},
		{
			Code: "small_pref_1_transport", Name: "小规模减按 1%（交通运输等）",
			Category: CatTransportPostal, Status: VATSmallScale, Method: MethodSimplified,
			Treatment:     TreatmentTaxable,
			InputTax:      InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), PreferentialRate: ratePtr(pct(1)),
			EffectiveFrom: lawFrom, EffectiveTo: prefTo,
			LegalBasis: BasisAnnouncement2026No10, Version: PolicyVersion2026,
		},
		{
			Code: "small_pref_1_construction", Name: "小规模减按 1%（建筑服务）",
			Category: CatConstruction, Status: VATSmallScale, Method: MethodSimplified,
			Treatment:     TreatmentTaxable,
			InputTax:      InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), PreferentialRate: ratePtr(pct(1)),
			EffectiveFrom: lawFrom, EffectiveTo: prefTo,
			LegalBasis: BasisAnnouncement2026No10, Version: PolicyVersion2026,
		},
		{
			Code: "small_pref_1_agriculture", Name: "小规模减按 1%（农产品等）",
			Category: CatAgriculture, Status: VATSmallScale, Method: MethodSimplified,
			Treatment:     TreatmentTaxable,
			InputTax:      InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), PreferentialRate: ratePtr(pct(1)),
			EffectiveFrom: lawFrom, EffectiveTo: prefTo,
			LegalBasis: BasisAnnouncement2026No10, Version: PolicyVersion2026,
		},
		// ---- 特殊规则：一般纳税人的简易计税 ----
		{
			Code:     "simplified_realestate_old_5",
			Name:     "房地产老项目等 5% 简易计税",
			Category: CatRealEstate, Status: VATGeneral, Method: MethodSimplified,
			Treatment: TreatmentTaxable, InputTax: InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(5)), EffectiveFrom: lawFrom, EffectiveTo: prefTo,
			LegalBasis: BasisAnnouncement2026No10, Version: PolicyVersion2026,
			Note: "一般纳税人房地产老项目等，2026—2027 年可选择 5% 简易计税。" +
				"选择后该项目对应的进项不得抵扣",
		},
		{
			Code: "simplified_used_asset_2",
			Name: "销售自己使用过的固定资产 3% 减按 2%",
			// ★ 单独一档，不并进 CatGoods ——
			// 并进去的话政策表上会显示成「一般纳税人销售货物……减按 2%」，
			// 读起来像通用规则，而它只适用于这一类特定资产。
			Category:  CatUsedFixedAsset,
			Status:    VATGeneral,
			Method:    MethodSimplified,
			Treatment: TreatmentTaxable, InputTax: InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), PreferentialRate: ratePtr(pct(2)),
			EffectiveFrom: lawFrom,
			LegalBasis:    BasisAnnouncement2026No10, Version: PolicyVersion2026,
			Note: "销售旧固定资产等特殊规则。须先确认资产是否已抵扣过进项；" +
				"★ 本政策只适用于销售自己使用过的固定资产，" +
				"不是「销售货物按简易计税一律 2%」",
		},
		{
			Code: "simplified_individual_housing_1_5",
			Name: "个人出租住房 3% 减按 1.5%",
			// ★ 主体限定为「个人」—— 单位或个体工商户出租住房不适用。
			// 少了这个维度，这条就会和「小规模出租不动产 3%」落在
			// 同一个键上，而政策表校验会直接拒绝（取哪条说不清）。
			Subject: SubjectPerson,
			// ★ 同样单独一档：并进 CatRealEstate 会让它看起来像
			// 「不动产租赁一律 1.5%」。
			Category:      CatIndividualHousingRent,
			Status:        VATSmallScale,
			Method:        MethodSimplified,
			Treatment:     TreatmentTaxable,
			InputTax:      InputNonDeductibleNonRefundable,
			StatutoryRate: ratePtr(pct(3)), PreferentialRate: ratePtr(pctHundredth(150)),
			EffectiveFrom: lawFrom,
			LegalBasis:    BasisAnnouncement2026No10, Version: PolicyVersion2026,
			Note: "个人出租住房 3% 减按 1.5%，与个体工商户出租住房适用规则不同",
		},
		// ---- 小规模免税与按次纳税起征点（以「零税率政策」的形式表达）----
		{
			// ★ 起征点的三种口径合成**一条**政策。
			//
			// 月销售额 10 万、季度 30 万、按次 1000 元是同一项起征点规定的
			// 不同口径，拆成两条会落在同一个「身份+主体+业务+方法+生效日」
			// 键上 —— 那样「某天该用哪条」就取决于遍历顺序，
			// 而政策表校验正是为了拒绝这种说不清的状态。
			Code: "small_exempt_threshold",
			Name: "小规模起征点以下免征",
			// ★ CatAll：起征点与业务类型无关，看的是纳税期累计销售额。
			// 标成 CatOther 会让小规模会计在「销售货物」那一行找不到它。
			Category: CatAll, Status: VATSmallScale, Method: MethodExempt,
			Treatment: TreatmentExempt, InputTax: InputNonDeductibleNonRefundable,
			EffectiveFrom: lawFrom, EffectiveTo: prefTo,
			LegalBasis: BasisAnnouncement2026No10, Version: PolicyVersion2026,
			Note: "按期纳税：月销售额不超过 10 万元（季度不超过 30 万元）免征；" +
				"按次纳税：每次（日）销售额不超过 1000 元免征。" +
				"是否达到起征点要按纳税期累计判断，程序不自动套用 —— " +
				"请按实际申报口径确认后再选本政策",
		},
		{
			Code: "not_taxable_other", Name: "不征税项目",
			Category: CatAll, Status: "", Method: MethodNotTaxable,
			Treatment: TreatmentNotTaxable, InputTax: InputNotApplicable,
			EffectiveFrom: lawFrom,
			LegalBasis:    BasisVATLaw, Version: PolicyVersion2026,
			Note: "不属于增值税征税范围，不产生销项，也不涉及进项转出",
		},
	}
}

// SoftwareBizHint 返回「销售软件」本身的税务提示。
//
// 这不是税率政策，而是给用户（以及我们自己）的一个提醒：
// 软件业务的两类收入适用不同税率，必须按合同实质拆分。
func SoftwareBizHint() string {
	return "软件产品销售通常适用 13%；SaaS、实施、维护或信息技术服务" +
		"通常适用 6%。应按合同实质与税收分类编码判断，不要一律按一个税率开票。"
}
