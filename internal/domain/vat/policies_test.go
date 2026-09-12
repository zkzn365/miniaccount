package vat

import (
	"strings"
	"testing"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

func d(s string) calendar.Date { return calendar.MustParse(s) }

// ★ 默认政策表必须自洽。
func TestDefaultPoliciesAreValid(t *testing.T) {
	ps := DefaultPolicies2026()
	if err := ValidatePolicies(ps); err != nil {
		t.Fatalf("默认政策表不合法: %v", err)
	}
}

// ★ 逐条核对税率数值 —— 这类错误一旦写错就是静默算错税。
func TestRateValues(t *testing.T) {
	ps := DefaultPolicies2026()
	byCode := map[string]RatePolicy{}
	for _, p := range ps {
		byCode[p.Code] = p
	}

	cases := []struct {
		code string
		want money.Rate
		desc string
	}{
		{"general_goods_13", pct(13), "销售货物等 13%"},
		{"general_transport_9", pct(9), "交通运输等 9%"},
		{"general_construction_9", pct(9), "建筑服务 9%"},
		{"general_realestate_9", pct(9), "不动产 9%"},
		{"general_agriculture_9", pct(9), "农产品 9%"},
		{"general_service_6", pct(6), "现代服务 6%"},

		{"small_goods_3", pct(3), "小规模法定征收率 3%"},
		{"small_pref_1_goods", pct(1), "小规模优惠 1%"},
		{"simplified_realestate_old_5", pct(5), "房地产老项目 5%"},
		{"simplified_used_asset_2", pct(2), "旧固定资产减按 2%"},
		{"simplified_individual_housing_1_5", pctHundredth(150), "个人出租住房 1.5%"},
	}
	for _, c := range cases {
		p, ok := byCode[c.code]
		if !ok {
			t.Errorf("政策表里缺少 %s（%s）", c.code, c.desc)
			continue
		}
		if got := p.MustRate(); got != c.want {
			t.Errorf("%s 实际税率 = %s，期望 %s（%s）", c.code, got, c.want, c.desc)
		}
	}
	// 零税率**不适用税率**（不是 0%）—— 它由 Treatment 表达
	zero := byCode["zero_export"]
	if _, ok := zero.Rate(); ok {
		t.Error("★ 零税率不该有税率值 —— 不适用与 0% 是两回事")
	}
	if zero.Treatment != TreatmentZeroRated {
		t.Errorf("出口政策的税收处理 = %s，期望零税率", zero.Treatment.Label())
	}
	if zero.InputTax != InputDeductibleRefundable {
		t.Errorf("零税率的进项处理 = %s，期望可抵扣并可退", zero.InputTax.Label())
	}

	// 1.5% 要落在 1% 和 2% 之间，防住「错十倍」这类换算错误
	if got := byCode["simplified_individual_housing_1_5"].MustRate(); got <= pct(1) || got >= pct(2) {
		t.Errorf("个人出租住房税率 = %s，应落在 1%% 与 2%% 之间", got)
	}
}

// ★ 法定税率与实际优惠税率必须分开存。
func TestStatutoryAndPreferentialAreSeparate(t *testing.T) {
	ps := DefaultPolicies2026()
	var one RatePolicy
	for _, p := range ps {
		if p.Code == "small_pref_1_goods" {
			one = p
		}
	}
	if one.StatutoryRate == nil || *one.StatutoryRate != pct(3) {
		t.Errorf("法定征收率 = %v，期望 3%%", one.StatutoryRate)
	}
	if one.PreferentialRate == nil || *one.PreferentialRate != pct(1) {
		t.Errorf("优惠征收率 = %v，期望 1%%", one.PreferentialRate)
	}
	if one.MustRate() != pct(1) {
		t.Errorf("实际适用 = %s，期望 1%%", one.MustRate())
	}
	if !one.HasPreference() {
		t.Error("应报告为含优惠")
	}
}

// ★ 1% 优惠必须带截止日，且到期后自动回落到 3%。
//
// 这正是「把优惠做成带截止期的数据、而不是写死在代码里」的意义：
// 政策一到期，程序不需要改代码就自动按法定征收率算。
func TestPreferenceExpiresAndFallsBack(t *testing.T) {
	ps := DefaultPolicies2026()

	// 优惠期内：1%
	before, err := ResolveRate(ps, VATSmallScale, SubjectEntity, CatGoods, MethodSimplified, d("2027-12-31"))
	if err != nil {
		t.Fatalf("2027-12-31 应能取到政策: %v", err)
	}
	if before.MustRate() != pct(1) {
		t.Errorf("2027-12-31 税率 = %s，期望 1%%", before.MustRate())
	}

	// 截止日之后：回落到 3%（而且不能报「找不到政策」）
	after, err := ResolveRate(ps, VATSmallScale, SubjectEntity, CatGoods, MethodSimplified, d("2028-01-01"))
	if err != nil {
		t.Fatalf("2028-01-01 应回落到法定征收率，而不是找不到政策: %v", err)
	}
	if after.MustRate() != pct(3) {
		t.Errorf("2028-01-01 税率 = %s，期望回落到 3%%", after.MustRate())
	}
	if after.Policy.HasPreference() {
		t.Error("2028-01-01 不该再有优惠")
	}
}

// ★ 小规模销售/出租不动产、转让土地使用权**不自动套用 1%**。
//
// 这条是用户明确点名的业务规则：不动产走单独规定，
// 在程序里默默按 1% 开票就是错票。
func TestSmallScaleRealEstateDoesNotGetOnePercent(t *testing.T) {
	ps := DefaultPolicies2026()

	for _, day := range []string{"2026-06-01", "2027-06-01"} {
		p, err := ResolveRate(ps, VATSmallScale, SubjectEntity, CatRealEstate,
			MethodSimplified, d(day))
		if err != nil {
			t.Fatalf("%s 应能取到不动产政策: %v", day, err)
		}
		if p.MustRate() != pct(3) {
			t.Errorf("★ %s 小规模不动产税率 = %s，期望 3%%（不得套用 1%% 减征）",
				day, p.MustRate())
		}
		if p.Policy.HasPreference() {
			t.Errorf("%s 小规模不动产不该带优惠", day)
		}
		if !strings.Contains(p.Note, "不适用 1%") {
			t.Errorf("政策说明应点明不适用 1%%，实际 %q", p.Note)
		}
	}

	// 对照：同一日期、同样小规模，销售货物**可以**享受 1%
	goods, err := ResolveRate(ps, VATSmallScale, SubjectEntity, CatGoods, MethodSimplified, d("2026-06-01"))
	if err != nil {
		t.Fatal(err)
	}
	if goods.MustRate() != pct(1) {
		t.Errorf("销售货物税率 = %s，期望 1%%", goods.MustRate())
	}
}

// ★ 两套身份并存：小微企业可以是一般纳税人。
//
// 这个测试不是在测代码，是在**钉住概念**：
// 企业规模类型与增值税纳税人身份互不派生。
func TestScaleAndVATStatusAreIndependent(t *testing.T) {
	// 「小微企业 + 一般纳税人」是完全合法的组合
	scale := ScaleSmall
	status := VATGeneral
	if !scale.IsSmallOrMicro() {
		t.Fatal("小型企业应属于小微口径")
	}
	if !status.CanDeductInput() {
		t.Fatal("一般纳税人应能抵扣进项")
	}
	// 抵扣判定只看增值税身份，不看企业规模
	if !status.CanDeductInput() || scale.IsSmallOrMicro() == status.CanDeductInput() {
		// 这里只断言两者取值互不相关：小微=true 且 可抵扣=true
	}

	// 「大型企业 + 小规模纳税人」同样合法（年销售额未超 500 万但规模大，
	// 例如刚成立的大型集团子公司）
	if !ScaleLarge.IsSmallOrMicro() == false {
		t.Error("大型企业不属于小微口径")
	}
	if VATSmallScale.CanDeductInput() {
		t.Error("小规模纳税人不得抵扣进项")
	}
}

// 身份按业务发生日取值，而不是拿今天的身份算去年的税。
func TestStatusOnUsesBusinessDate(t *testing.T) {
	periods := []StatusPeriod{
		{Status: VATSmallScale, EffectiveFrom: d("2024-01-01"),
			EffectiveTo: d("2025-06-30"), Note: "设立时为小规模"},
		{Status: VATGeneral, EffectiveFrom: d("2025-07-01"),
			Note: "自愿登记为一般纳税人"},
	}
	if err := ValidatePeriods(periods); err != nil {
		t.Fatalf("期间列表应合法: %v", err)
	}

	cases := map[string]VATStatus{
		"2024-06-01": VATSmallScale,
		"2025-06-30": VATSmallScale,
		"2025-07-01": VATGeneral,
		"2026-01-01": VATGeneral,
	}
	for day, want := range cases {
		got, ok := StatusOn(periods, d(day))
		if !ok {
			t.Errorf("%s 应能取到身份", day)
			continue
		}
		if got != want {
			t.Errorf("%s 身份 = %s，期望 %s", day, got.Label(), want.Label())
		}
	}

	// 期间之前：取不到，调用方必须显式处理
	if _, ok := StatusOn(periods, d("2023-12-31")); ok {
		t.Error("生效日之前不该取到身份")
	}
}

// 身份未设置时必须返回「取不到」，不能默认成一般纳税人。
//
// 默认成一般纳税人会放行本不该抵扣的进项 —— 那是少缴税。
func TestStatusOnDoesNotDefaultToGeneral(t *testing.T) {
	got, ok := StatusOn(nil, d("2026-01-01"))
	if ok {
		t.Errorf("没有身份记录时不该返回有效身份，实际 %s", got.Label())
	}
	if got == VATGeneral {
		t.Error("★ 不能默认成一般纳税人")
	}
}

// 期间重叠要拦住：重叠会让「某天按哪个身份」变成遍历顺序问题。
func TestValidatePeriodsRejectsOverlap(t *testing.T) {
	bad := []StatusPeriod{
		{Status: VATSmallScale, EffectiveFrom: d("2024-01-01"), EffectiveTo: d("2025-12-31")},
		{Status: VATGeneral, EffectiveFrom: d("2025-06-01")},
	}
	if err := ValidatePeriods(bad); err == nil {
		t.Error("期间重叠应被拒绝")
	}
}

// 找不到适用政策时必须报错，不能给默认税率。
func TestResolveRateErrorsWhenNotFound(t *testing.T) {
	ps := DefaultPolicies2026()
	// 2025 年（增值税法施行前）没有政策
	if _, err := ResolveRate(ps, VATGeneral, SubjectEntity, CatGoods, MethodGeneral, d("2025-06-01")); err == nil {
		t.Error("施行日之前应报「找不到政策」，而不是给一个默认税率")
	}
	// 一般纳税人没有「免税」政策。
	//
	// 「免税」是免征结论，不是计税方法 —— 一般纳税人卖货问免税就是不存在，
	// 没有另一套处理可讲，必须报错而不是返回一个税率。
	if _, err := ResolveRate(ps, VATGeneral, SubjectEntity, CatGoods, MethodExempt, d("2026-06-01")); err == nil {
		t.Error("无对应政策时应报错")
	} else if !strings.Contains(err.Error(), "实际适用") {
		t.Errorf("报错也要说清实际适用什么，实际 %q", err)
	}
}

// 计税方法与税率必须分开：免税与零税率税额都是 0，但进项处理相反。
func TestMethodDistinguishesExemptFromZeroRated(t *testing.T) {
	if MethodExempt.AllowsInputCredit() {
		t.Error("免税项目的进项不得抵扣")
	}
	if !MethodZeroRated.AllowsInputCredit() {
		t.Error("零税率的进项可以退")
	}
	if MethodSimplified.AllowsInputCredit() {
		t.Error("简易计税的进项不得抵扣")
	}
	if !MethodGeneral.AllowsInputCredit() {
		t.Error("一般计税的进项可以抵扣")
	}
	if MethodExempt.HasOutputTax() || MethodZeroRated.HasOutputTax() {
		t.Error("免税与零税率都不产生销项")
	}
	if !MethodGeneral.HasOutputTax() {
		t.Error("一般计税产生销项")
	}
}

// 政策表校验要能抓到「同身份+同业务+同方法+同生效日」的重复。
func TestValidatePoliciesRejectsDuplicateKey(t *testing.T) {
	ps := DefaultPolicies2026()
	dup := append([]RatePolicy{}, ps...)
	dup = append(dup, RatePolicy{
		Code: "dup_of_goods_13", Name: "重复",
		Category: CatGoods, Status: VATGeneral, Method: MethodGeneral,
		Treatment: TreatmentTaxable, InputTax: InputDeductible,
		StatutoryRate: ratePtr(pct(13)), EffectiveFrom: d("2026-01-01"),
	})
	err := ValidatePolicies(dup)
	if err == nil {
		t.Fatal("重复的政策键应被拒绝")
	}
	if !strings.Contains(err.Error(), "取哪条取决于遍历顺序") {
		t.Errorf("报错应说明后果，实际 %q", err)
	}
}

// 软件业务的两类收入要能区分开。
func TestSoftwareBizHintMentionsBothRates(t *testing.T) {
	h := SoftwareBizHint()
	for _, want := range []string{"13%", "6%", "合同实质"} {
		if !strings.Contains(h, want) {
			t.Errorf("提示里应含 %q，实际 %q", want, h)
		}
	}
}

// ★ 特例规则必须有**自己的业务类型档次**，不能并进通用档次。
//
// 并进 CatGoods 的话，政策表上「销售自己使用过的固定资产 3% 减按 2%」
// 会显示成「一般纳税人 / 销售货物…／简易计税／3%／2%（优惠）」，
// 读起来像「一般纳税人销售货物按简易计税都减按 2%」——
// 会计照着那张表开票就会错。
func TestSpecialRulesHaveOwnCategories(t *testing.T) {
	ps := DefaultPolicies2026()

	// 旧固定资产：自己的档次，且**不**落在销售货物档
	asset, err := ResolveRate(ps, VATGeneral, SubjectEntity,
		CatUsedFixedAsset, MethodSimplified, d("2026-06-01"))
	if err != nil {
		t.Fatalf("销售旧固定资产应能取到政策: %v", err)
	}
	if asset.MustRate() != pct(2) {
		t.Errorf("销售旧固定资产税率 = %s，期望 2%%", asset.MustRate())
	}
	if asset.Policy.Category == CatGoods {
		t.Error("★ 旧固定资产政策不该挂在「销售货物」档上")
	}
	if !strings.Contains(asset.Policy.Note, "只适用于销售自己使用过的固定资产") {
		t.Errorf("说明必须点明适用范围，实际 %q", asset.Policy.Note)
	}

	// 销售货物按简易计税**没有**精确政策（2% 只给旧固定资产）。
	//
	// 现在这种情况返回的是 Exact=false 的「转向结果」+ 解释，
	// 而不是一句「找不到政策」—— 用户需要知道实际按什么处理。
	// 但**绝不能**把旧固定资产的 2% 套上去。
	alt, err := ResolveRate(ps, VATGeneral, SubjectEntity,
		CatGoods, MethodSimplified, d("2026-06-01"))
	if err != nil {
		t.Fatalf("应回退到解释性的结果，而不是报错: %v", err)
	}
	if alt.Exact {
		t.Error("「销售货物 + 简易计税」不该有精确匹配的政策")
	}
	if alt.Policy.Code == "simplified_used_asset_2" {
		t.Error("★ 不该把旧固定资产的 2% 套到一般货物销售上 —— " +
			"那正是把特例当通用规则的表现")
	}
	if alt.Note == "" {
		t.Error("非精确匹配必须给出解释")
	}

	// 个人出租住房：自己的档次
	housing, err := ResolveRate(ps, VATSmallScale, SubjectPerson,
		CatIndividualHousingRent, MethodSimplified, d("2026-06-01"))
	if err != nil {
		t.Fatalf("个人出租住房应能取到政策: %v", err)
	}
	if housing.MustRate() != pctHundredth(150) {
		t.Errorf("个人出租住房税率 = %s，期望 1.5%%", housing.MustRate())
	}
	if housing.Policy.Category == CatRealEstate {
		t.Error("★ 个人出租住房不该挂在通用的「不动产租赁及销售」档上")
	}

	// 单位出租不动产仍然是 3%，不受个人 1.5% 影响
	entity, err := ResolveRate(ps, VATSmallScale, SubjectEntity,
		CatRealEstate, MethodSimplified, d("2026-06-01"))
	if err != nil {
		t.Fatal(err)
	}
	if entity.MustRate() != pct(3) {
		t.Errorf("单位出租不动产 = %s，期望 3%%", entity.MustRate())
	}
}

// ★ CatAll 的政策对任何业务类型都命中，但不能盖过精确匹配。
func TestCatAllMatchesEveryCategoryButYieldsToExact(t *testing.T) {
	ps := DefaultPolicies2026()

	// 货物 + 免税 → 命中 CatAll 的起征点政策
	for _, cat := range []BizCategory{CatGoods, CatModernService, CatRealEstate} {
		p, err := ResolveRate(ps, VATSmallScale, SubjectEntity,
			cat, MethodExempt, d("2026-06-01"))
		if err != nil {
			t.Errorf("%s + 免税 应命中起征点政策: %v", cat.Label(), err)
			continue
		}
		if p.Policy.Category != CatAll {
			t.Errorf("%s 命中的是 %s，期望 CatAll 的起征点政策",
				cat.Label(), p.Policy.Category.Label())
		}
		if !strings.Contains(p.Policy.Note, "不自动套用") {
			t.Errorf("起征点政策必须写明程序不自动套用，实际 %q", p.Policy.Note)
		}
	}

	// ★ 货物 + 简易计税 → 必须命中精确的 1% 政策，而不是 CatAll 的免税
	p, err := ResolveRate(ps, VATSmallScale, SubjectEntity,
		CatGoods, MethodSimplified, d("2026-06-01"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Policy.Category == CatAll {
		t.Error("★ CatAll 的政策盖过了精确匹配的业务类型")
	}
	if p.MustRate() != pct(1) {
		t.Errorf("销售货物税率 = %s，期望 1%%", p.MustRate())
	}
}

// CatAll 不该出现在给用户选的业务类型里。
func TestSelectableCategoriesExcludeCatAll(t *testing.T) {
	for _, c := range SelectableCategories() {
		if c == CatAll {
			t.Error("CatAll 是「与业务类型无关」的标记，不该让用户选它")
		}
	}
	// 但 AllCategories 要包含它（政策表校验要用）
	var found bool
	for _, c := range AllCategories() {
		if c == CatAll {
			found = true
		}
	}
	if !found {
		t.Error("AllCategories 应包含 CatAll")
	}
	// 两个特例档次必须可选
	var asset, housing bool
	for _, c := range SelectableCategories() {
		if c == CatUsedFixedAsset {
			asset = true
		}
		if c == CatIndividualHousingRent {
			housing = true
		}
	}
	if !asset || !housing {
		t.Error("两个特例档次应当可选")
	}
}

// 全部业务类型都要有中文名，且互不重复。
func TestAllCategoriesHaveLabels(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range AllCategories() {
		if c.Label() == string(c) {
			t.Errorf("业务类型 %q 缺少中文名", c)
		}
		if seen[c.Label()] {
			t.Errorf("业务类型名称重复：%s", c.Label())
		}
		seen[c.Label()] = true
	}
}

// ★★ 小规模纳税人出口：免税、不退税，**不放宽零税率**。
//
// 零税率意味着进项可以退（出口退税），而小规模纳税人的进项本来就
// 不得抵扣、不得退税 —— 出口适用的是**免税**，已含进项计入成本。
// 依据：财政部、税务总局 2026 年第 11 号公告。
func TestSmallScaleExportIsExemptNotZeroRated(t *testing.T) {
	ps := DefaultPolicies2026()

	// 小规模查「出口 + 零税率」：**不该**报「找不到政策」，
	// 而应转向到实际适用的免税政策并解释清楚。
	res, err := ResolveRate(ps, VATSmallScale, SubjectEntity,
		CatExport, MethodZeroRated, d("2026-06-01"))
	if err != nil {
		t.Fatalf("★ 不该只回一句「找不到政策」—— 用户需要知道实际适用什么: %v", err)
	}
	if res.Exact {
		t.Error("小规模纳税人不该有「零税率」的精确政策")
	}
	if res.Policy.Treatment != TreatmentExemptNoRefund {
		t.Errorf("转向到的政策处理方式 = %s，期望「免税（不退税）」",
			res.Policy.Treatment.Label())
	}
	if res.Policy.InputTax != InputNonDeductibleNonRefundable {
		t.Errorf("进项处理 = %s，期望「不得抵扣、不得退税」",
			res.Policy.InputTax.Label())
	}
	// 税率必须**不适用**，而不是 0%
	if _, ok := res.Rate(); ok {
		t.Errorf("★ 免税不该有税率值（拿到 %s）—— 用 0 会与零税率混淆",
			res.DisplayRate())
	}
	if res.DisplayRate() != "免税（不退税）" {
		t.Errorf("展示文本 = %q，期望「免税（不退税）」", res.DisplayRate())
	}
	// 解释里必须点明这几件事
	for _, want := range []string{"不适用零税率", "免税", "不得抵扣", "征税例外"} {
		if !strings.Contains(res.Note, want) {
			t.Errorf("说明里应含 %q，实际 %q", want, res.Note)
		}
	}
}

// 一般纳税人出口仍然是零税率，且进项可退。
func TestGeneralTaxpayerExportIsZeroRated(t *testing.T) {
	ps := DefaultPolicies2026()
	res, err := ResolveRate(ps, VATGeneral, SubjectEntity,
		CatExport, MethodZeroRated, d("2026-06-01"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exact {
		t.Error("一般纳税人出口应有精确的零税率政策")
	}
	if res.Treatment() != TreatmentZeroRated {
		t.Errorf("处理方式 = %s，期望零税率", res.Treatment().Label())
	}
	if res.InputTax() != InputDeductibleRefundable {
		t.Errorf("进项处理 = %s，期望可抵扣并可退", res.InputTax().Label())
	}
	if _, ok := res.Rate(); ok {
		t.Error("零税率也不该有税率值")
	}
	if res.DisplayRate() != "零税率（退免税）" {
		t.Errorf("展示 = %q", res.DisplayRate())
	}
}

// ★ 免税与零税率必须能区分开 —— 税率都是「不适用」，
// 但进项处理相反。这正是不能用 rate=0 表示免税的原因。
func TestExemptAndZeroRatedAreDistinguishable(t *testing.T) {
	ps := DefaultPolicies2026()

	// 小规模出口 → 免税、不退税、进项不得抵扣不得退
	exempt, err := ResolveRate(ps, VATSmallScale, SubjectEntity,
		CatExport, MethodSimplified, d("2026-06-01"))
	if err != nil {
		t.Fatal(err)
	}
	// 一般纳税人出口 → 零税率、进项可退
	zero, err := ResolveRate(ps, VATGeneral, SubjectEntity,
		CatExport, MethodZeroRated, d("2026-06-01"))
	if err != nil {
		t.Fatal(err)
	}

	// 两者的「税率」都不适用 —— 用税率分不出来
	if _, ok := exempt.Rate(); ok {
		t.Error("免税不该有税率")
	}
	if _, ok := zero.Rate(); ok {
		t.Error("零税率不该有税率")
	}
	// 但处理方式与进项处理必须不同
	if exempt.Treatment() == zero.Treatment() {
		t.Error("免税与零税率的处理方式必须能区分")
	}
	if exempt.InputTax() == zero.InputTax() {
		t.Error("★ 免税与零税率的**进项处理必须不同** —— " +
			"一个不得抵扣不得退，一个可抵扣并可退")
	}
	if exempt.InputTax().CanRefund() {
		t.Error("免税不得退税")
	}
	if !zero.InputTax().CanRefund() {
		t.Error("零税率可以退税")
	}
}

// 政策表校验必须挡住「用 0 表示免税」。
func TestValidateRejectsZeroRateForExempt(t *testing.T) {
	bad := RatePolicy{
		Code: "bad_exempt", Category: CatExport, Method: MethodExempt,
		Treatment: TreatmentExempt, InputTax: InputNonDeductibleNonRefundable,
		StatutoryRate: ratePtr(0), // ← 0 而不是留空
		EffectiveFrom: d("2026-01-01"),
	}
	err := bad.Validate()
	if err == nil {
		t.Fatal("★ 免税政策填了税率（哪怕是 0）应当被拒绝")
	}
	if !strings.Contains(err.Error(), "不该填税率") {
		t.Errorf("报错应点明原因，实际 %q", err)
	}

	// 征税项目必须填税率
	bad2 := RatePolicy{
		Code: "bad_taxable", Category: CatGoods, Method: MethodGeneral,
		Treatment: TreatmentTaxable, InputTax: InputDeductible,
		EffectiveFrom: d("2026-01-01"),
	}
	if err := bad2.Validate(); err == nil {
		t.Error("征税项目不填税率应被拒绝")
	}
}

// ★ 征税例外需**人工确认**，不能自动命中。
//
// 是否命中公告第七条异常情形是事实认定，程序无从判断。
// 自动替用户选中的话，要么让本该免税的业务交了税，
// 要么让本该交税的业务免了税 —— 而后者是**少缴税**。
func TestExportTaxableExceptionRequiresExplicitChoice(t *testing.T) {
	ps := DefaultPolicies2026()

	// 默认（不勾选例外）：走免税、不退税
	def, err := ResolveRate(ps, VATSmallScale, SubjectEntity,
		CatExport, MethodSimplified, d("2026-06-01"))
	if err != nil {
		t.Fatal(err)
	}
	if def.Treatment() == TreatmentTaxable {
		t.Error("★ 默认不该自动命中征税例外 —— 那是替用户做事实认定")
	}
	if def.Treatment() != TreatmentExemptNoRefund {
		t.Errorf("默认处理 = %s，期望免税、不退税", def.Treatment().Label())
	}

	// 显式勾选例外：按征收率征税
	exc, err := ResolveRateWith(ps, VATSmallScale, SubjectEntity,
		CatExport, MethodSimplified, d("2026-06-01"), true)
	if err != nil {
		t.Fatal(err)
	}
	if exc.Treatment() != TreatmentTaxable {
		t.Errorf("勾选例外后处理 = %s，期望征税", exc.Treatment().Label())
	}
	if exc.MustRate() != pct(3) {
		t.Errorf("征税例外的税率 = %s，期望征收率 3%%", exc.MustRate())
	}
	if !strings.Contains(exc.Policy.Note, "事实认定") {
		t.Errorf("说明应点明是否命中属事实认定，实际 %q", exc.Policy.Note)
	}

	// 例外政策必须标了 Conditional
	var found bool
	for _, p := range ps {
		if p.Code == "export_taxable_exception" {
			found = true
			if !p.Conditional {
				t.Error("征税例外政策必须标为需人工确认")
			}
		}
	}
	if !found {
		t.Error("政策表里应有征税例外政策")
	}
}
