package sqlite

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/vat"
)

// vatRatePtr 造一个税率指针（政策表里税率是可空的）。
func vatRatePtr(r money.Rate) *money.Rate { return &r }

// ★ 政策表要持久化，且第一次访问时写入默认表。
func TestVATPoliciesPersist(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	list, err := db.VATRates().Policies(ctx)
	if err != nil {
		t.Fatalf("取政策失败: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("首次访问应写入默认政策表，而不是返回空表")
	}
	// 再取一次应当来自库，而不是重新生成（数量与版本一致）
	again, err := db.VATRates().Policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(list) {
		t.Errorf("两次取到的条数不同：%d vs %d", len(list), len(again))
	}

	// 改动后应当存住
	modified := append([]vat.RatePolicy{}, list...)
	modified[0].Note = "用户改过的说明"
	if err := db.VATRates().SavePolicies(ctx, modified); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got, err := db.VATRates().Policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Note != "用户改过的说明" {
		t.Errorf("改动没存住：%q", got[0].Note)
	}
}

// 非法政策表要挡住，不能存进去等算税时才炸。
func TestSavePoliciesValidates(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	bad := []vat.RatePolicy{{
		Code: "x", Category: vat.CatGoods, Method: vat.MethodGeneral,
		Treatment: vat.TreatmentTaxable, InputTax: vat.InputDeductible,
		StatutoryRate: vatRatePtr(money.Rate(0)),
		EffectiveFrom: calendar.MustParse("2026-01-01"),
	}}
	// 缺生效日
	bad[0].EffectiveFrom = calendar.Date{}
	if err := db.VATRates().SavePolicies(ctx, bad); err == nil {
		t.Error("缺生效日应被拒绝")
	}
	// 空表
	if err := db.VATRates().SavePolicies(ctx, nil); err == nil {
		t.Error("空政策表应被拒绝")
	}
	// 税率超过 100%
	bad[0].EffectiveFrom = calendar.MustParse("2026-01-01")
	bad[0].Treatment = vat.TreatmentTaxable
	bad[0].InputTax = vat.InputDeductible
	bad[0].StatutoryRate = vatRatePtr(money.Rate(2_000_000))
	if err := db.VATRates().SavePolicies(ctx, bad); err == nil {
		t.Error("税率超过 100% 应被拒绝")
	}
}

// ★ 身份变更要留历史，并按业务发生日取值。
//
// 小规模自愿登记为一般纳税人在实务里很常见，而跨期看账必须按
// **业务发生日**取当时有效的身份 —— 拿今天的身份去算去年的税一定错。
func TestVATStatusHistoryTracksChanges(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	// newTestDB 建账时已写入第一条（一般纳税人）
	hist, err := db.VATRates().StatusHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("初始历史条数 = %d，期望 1", len(hist))
	}

	// 2025-07-01 起转为小规模
	if err := db.VATRates().SetVATStatus(ctx, vat.VATSmallScale,
		calendar.MustParse("2025-07-01"), "自愿登记为小规模纳税人"); err != nil {
		t.Fatalf("变更失败: %v", err)
	}

	hist, err = db.VATRates().StatusHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("变更后历史条数 = %d，期望 2", len(hist))
	}
	// 前一段要自动闭合到变更日前一天
	if hist[0].EffectiveTo.String() != "2025-06-30" {
		t.Errorf("前一段的失效日 = %q，期望 2025-06-30", hist[0].EffectiveTo)
	}
	if hist[0].Status != vat.VATGeneral {
		t.Errorf("前一段身份 = %s", hist[0].Status.Label())
	}
	if hist[1].Status != vat.VATSmallScale || hist[1].EffectiveTo.Valid() {
		t.Errorf("后一段身份 = %s，失效日期望为空（至今有效）",
			hist[1].Status.Label())
	}

	// ★ 按业务发生日取值
	cases := map[string]vat.VATStatus{
		"2025-06-30": vat.VATGeneral,    // 变更前一天
		"2025-07-01": vat.VATSmallScale, // 变更当天
		"2026-01-01": vat.VATSmallScale,
	}
	for day, want := range cases {
		st, ok := vat.StatusOn(hist, calendar.MustParse(day))
		if !ok {
			t.Errorf("%s 应能取到身份", day)
			continue
		}
		if st != want {
			t.Errorf("%s 身份 = %s，期望 %s", day, st.Label(), want.Label())
		}
	}

	// 当前身份字段也要跟着更新
	b, err := db.Books().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b.VATStatus != string(vat.VATSmallScale) {
		t.Errorf("book.vat_status = %q，期望 small_scale", b.VATStatus)
	}
	if b.VATStatusEffectiveFrom != "2025-07-01" {
		t.Errorf("生效日 = %q，期望 2025-07-01", b.VATStatusEffectiveFrom)
	}
	// 历史字段同步，免得旧脚本读到不一致的值
	if b.TaxType != TaxTypeSmall {
		t.Errorf("历史字段 tax_type = %q，期望 small", b.TaxType)
	}
}

// ★ 企业规模类型与纳税人身份分开设置，互不影响。
func TestSetScaleDoesNotTouchVATStatus(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	before, err := db.Books().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// 设成小微企业 —— 不应改变纳税人身份
	if err := db.VATRates().SetEnterpriseScale(ctx, vat.ScaleMicro); err != nil {
		t.Fatalf("设置规模失败: %v", err)
	}
	after, err := db.Books().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.EnterpriseScale != string(vat.ScaleMicro) {
		t.Errorf("规模 = %q，期望 micro", after.EnterpriseScale)
	}
	if after.VATStatus != before.VATStatus {
		t.Errorf("★ 设置企业规模类型改变了增值税纳税人身份：%q → %q",
			before.VATStatus, after.VATStatus)
	}

	// 反过来：改身份不该动规模
	if err := db.VATRates().SetVATStatus(ctx, vat.VATSmallScale,
		calendar.MustParse("2026-01-01"), "登记为小规模"); err != nil {
		t.Fatal(err)
	}
	final, err := db.Books().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if final.EnterpriseScale != string(vat.ScaleMicro) {
		t.Errorf("★ 改增值税身份动了企业规模类型：%q", final.EnterpriseScale)
	}

	// 规模可以清空（表示未填写）
	if err := db.VATRates().SetEnterpriseScale(ctx, ""); err != nil {
		t.Fatal(err)
	}
	cleared, err := db.Books().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.EnterpriseScale != "" {
		t.Errorf("规模应可清空，实际 %q", cleared.EnterpriseScale)
	}
}

// 非法规模类型要挡住。
func TestSetScaleRejectsUnknown(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := db.VATRates().SetEnterpriseScale(ctx, "bogus"); err == nil {
		t.Error("未知规模类型应被拒绝")
	}
}

// 政策表默认值里，小规模不动产**不带** 1% 优惠。
//
// 这条在领域层已经测过，这里再验一次是因为它要经过
// 「序列化 → 存库 → 读回」这一整圈 —— JSON 往返丢字段是常见事故。
func TestStoredPoliciesKeepRealEstateRule(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	list, err := db.VATRates().Policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err := vat.ResolveRate(list, vat.VATSmallScale, vat.SubjectEntity,
		vat.CatRealEstate, vat.MethodSimplified, calendar.MustParse("2026-06-01"))
	if err != nil {
		t.Fatalf("取不动产政策失败: %v", err)
	}
	if p.HasPreference() {
		t.Errorf("★ 存库往返后小规模不动产带上了优惠：%s", p.DisplayRate())
	}
	if !strings.Contains(p.Note, "不适用 1%") {
		t.Errorf("说明应点明不适用 1%%，实际 %q", p.Note)
	}
}

// ★ 内置政策表升级：用户没改过的表要跟着版本走，改过的表不能被覆盖。
//
// 两头都是坑：
//   - 一律不换 → 1% 优惠 2028 年到期了，用户还在按 1% 开票；
//   - 一律强换 → 用户核过、改过的计税依据被悄悄删掉。
func TestBuiltinPolicyTableUpgradesButCustomIsPreserved(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	list, err := db.VATRates().Policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st, err := db.VATRates().PolicyStatusOf(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Builtin {
		t.Error("首次写入的应是内置表")
	}
	if st.UpdateAvailable {
		t.Errorf("刚写入的内置表不该提示更新：%s vs %s", st.Version, st.BuiltinVersion)
	}
	if st.Version != vat.PolicyVersion2026 {
		t.Errorf("版本 = %s，期望 %s", st.Version, vat.PolicyVersion2026)
	}

	// 模拟「账套里存的是旧版本内置表」：改掉版本号再存回去。
	old := make([]vat.RatePolicy, len(list))
	copy(old, list)
	for i := range old {
		old[i].Version = "2020.1"
	}
	if err := db.VATRates().savePolicies(ctx, old, policySourceBuiltin); err != nil {
		t.Fatal(err)
	}
	got, err := db.VATRates().Policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Version != vat.PolicyVersion2026 {
		t.Errorf("★ 没改过的内置表应自动升到 %s，实际 %v",
			vat.PolicyVersion2026, versionOf(got))
	}

	// 用户改过的表：即使版本落后也只提示，不覆盖。
	custom := make([]vat.RatePolicy, len(list))
	copy(custom, list)
	for i := range custom {
		custom[i].Version = "2020.1"
	}
	custom[0].Name = "用户自己改过的名字"
	if err := db.VATRates().SavePolicies(ctx, custom); err != nil {
		t.Fatal(err)
	}
	st, err = db.VATRates().PolicyStatusOf(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Builtin {
		t.Error("导入过的表不该再算内置表")
	}
	if !st.UpdateAvailable {
		t.Error("版本落后应提示可更新")
	}
	got, err = db.VATRates().Policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Name != "用户自己改过的名字" {
		t.Errorf("★ 用户改过的政策表被覆盖了：%q", got[0].Name)
	}

	// 老账套（没有来源标记）：无法判断改没改过 → 不覆盖，也不谎称「你改过」。
	pr := &PayrollRepo{db: db}
	if err := db.WithTx(ctx, func(tx *Tx) error {
		return pr.putSetting(ctx, tx, settingVATPolicySource, "")
	}); err != nil {
		t.Fatal(err)
	}
	st, err = db.VATRates().PolicyStatusOf(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Origin != policySourceUnknown {
		t.Errorf("没有来源标记的账套 Origin = %q，期望 %q", st.Origin, policySourceUnknown)
	}
	if st.Builtin {
		t.Error("★ 无法确认改没改过的表不能当成内置表自动覆盖")
	}
	if !st.UpdateAvailable {
		t.Error("旧版本表应提示可更新")
	}
	got, err = db.VATRates().Policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Name != "用户自己改过的名字" {
		t.Errorf("★ 无标记的旧表被覆盖了：%q", got[0].Name)
	}

	// 明确重置后才换回内置表
	if err := db.VATRates().ResetPolicies(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = db.VATRates().Policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Name == "用户自己改过的名字" {
		t.Error("重置后应回到内置表")
	}
	if v := versionOf(got); v != vat.PolicyVersion2026 {
		t.Errorf("重置后版本 = %s，期望 %s", v, vat.PolicyVersion2026)
	}
}
