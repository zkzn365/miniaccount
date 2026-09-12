package main

import (
	"strings"
	"testing"

	"miniaccount/internal/service"
)

// ★ 界面上的「先生成演示账套看看」走的就是这条绑定。
//
// 原先演示数据只在命令行里生成（cmd/miniaccount 下的 seedDemo），
// 界面上根本没有这个功能 —— 而它恰恰是第一次打开软件的人最需要的：
// 还没想清楚单位名称、纳税人身份、启用期间该怎么填，
// 就想先看看这套账长什么样。
func TestCreateDemoBookAsGUIUsesIt(t *testing.T) {
	a, path := newApp(t)
	if _, f := a.OpenBook(path); f != nil {
		t.Fatalf("打开账套失败: %v", f)
	}

	res, f := a.CreateDemoBook(3)
	if f != nil {
		t.Fatalf("生成演示账套失败: %v", f)
	}
	if res.Posted == 0 {
		t.Error("演示账套应当写入凭证")
	}
	if res.ClosedMonths != 2 {
		t.Errorf("生成到 3 月时应结账 2 个月，实际 %d", res.ClosedMonths)
	}
	// 中途过账失败必须报出来，不能静默给一套残缺的演示数据
	for _, w := range res.Warnings {
		t.Errorf("生成过程有告警：%s", w)
	}

	// 演示账套要能立刻出报表 —— 这正是它存在的意义
	if _, f := a.Report(ReportRequest{Kind: "bs", Year: service.DemoYear, Month: 3}); f != nil {
		t.Errorf("演示账套应能出资产负债表: %v", f)
	}
	if _, f := a.Report(ReportRequest{Kind: "pl", Year: service.DemoYear, Month: 3}); f != nil {
		t.Errorf("演示账套应能出利润表: %v", f)
	}
	ov, f := a.Overview()
	if f != nil {
		t.Fatalf("演示账套应能出首页概览: %v", f)
	}
	if ov.Assets == 0 {
		t.Error("演示账套的资产总计不该是 0")
	}

	// 身份要是「一般纳税人 + 小型企业」——
	// 一般纳税人能看到进项/销项专栏与多栏式明细账的完整形态。
	id, f := a.VATIdentity()
	if f != nil {
		t.Fatalf("取身份失败: %v", f)
	}
	if id.VATStatus != "general" {
		t.Errorf("演示账套的增值税身份 = %q，期望 general", id.VATStatus)
	}
	if !id.EnterpriseScaleSet {
		t.Error("演示账套应当填了企业规模类型，好让用户看到这一栏有值")
	}
}

// ★ 已经有账套时不能覆盖。
//
// 覆盖等于删掉用户的账 —— 这种事不该由一个「看看演示」的按钮触发。
func TestCreateDemoBookRefusesExistingBook(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	if _, f := a.CreateBook(service.CreateBookInput{
		CompanyName: "正式账套", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	}); f != nil {
		t.Fatal(f)
	}

	if _, f := a.CreateDemoBook(3); f == nil {
		t.Fatal("★ 已有账套时生成演示数据必须被拒绝，而不是覆盖")
	}

	// 原账套必须原封不动
	st, f := a.CurrentBook()
	if f != nil {
		t.Fatal(f)
	}
	if st.Book == nil || st.Book.CompanyName != "正式账套" {
		t.Errorf("原账套被动过了：%+v", st.Book)
	}
}

// 月份越界要挡住。
func TestCreateDemoBookRejectsBadMonth(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	for _, m := range []int{0, -1, 13, 99} {
		if _, f := a.CreateDemoBook(m); f == nil {
			t.Errorf("月份 %d 应被拒绝", m)
		}
	}
}

// ★ 增值税政策与抵扣判定也要能从界面调。
func TestVATBindingsWorkAsGUIUsesThem(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	if _, f := a.CreateBook(service.CreateBookInput{
		CompanyName: "X", VATStatus: "general",
		StartYear: 2026, StartMonth: 1,
		ThroughYear: 2026, CurrentYear: 2026, CurrentMonth: 6,
	}); f != nil {
		t.Fatal(f)
	}

	// 政策表
	list, f := a.VATPolicies()
	if f != nil {
		t.Fatalf("取政策失败: %v", f)
	}
	if len(list) == 0 {
		t.Fatal("政策表不该为空")
	}
	// 每条政策都要有中文名与政策依据 —— 界面上直接显示
	for _, p := range list {
		if p.Name == "" || p.LegalBasis == "" || p.CategoryLabel == "" {
			t.Errorf("政策 %s 的展示字段不全：%+v", p.Code, p)
		}
	}

	// 参照表
	ref, f := a.VATReference()
	if f != nil {
		t.Fatalf("取参照表失败: %v", f)
	}
	if len(ref.VoucherKinds) == 0 || len(ref.Categories) == 0 || len(ref.Methods) == 0 {
		t.Error("参照表的三个下拉都不该为空")
	}

	// 查税率：一般纳税人销售货物 → 13%
	rate, f := a.ResolveVATRate(VATRateQuery{
		Status: "general", Subject: "entity", Category: "goods", On: "2026-06-01",
	})
	if f != nil {
		t.Fatalf("查税率失败: %v", f)
	}
	if rate.Rate != "13%" {
		t.Errorf("一般纳税人销售货物税率 = %s，期望 13%%", rate.Rate)
	}

	// 抵扣判定：一般纳税人 + 专票 → 可抵
	d, f := a.JudgeDeduction(DeductionRequest{
		On: "2026-06-01", Voucher: "special_invoice", TaxYuan: "1300",
	})
	if f != nil {
		t.Fatalf("抵扣判定失败: %v", f)
	}
	if !d.Deductible {
		t.Errorf("一般纳税人取得专票应可抵扣，实际 %s", d.ReasonLabel)
	}

	// 换小规模身份后再判 —— 同一个绑定，结论必须翻转
	if f := a.SetVATStatus(SetVATStatusRequest{
		Status: "small_scale", From: "2026-07-01", Note: "转为小规模",
	}); f != nil {
		t.Fatalf("变更身份失败: %v", f)
	}
	d2, f := a.JudgeDeduction(DeductionRequest{
		On: "2026-08-01", Voucher: "special_invoice", TaxYuan: "1300",
	})
	if f != nil {
		t.Fatal(f)
	}
	if d2.Deductible {
		t.Error("★ 转为小规模后取得专票也不得抵扣")
	}
	if d2.IncludedInCost == 0 {
		t.Error("不得抵扣时税额应计入成本")
	}
	// 但 6 月那笔（身份还是一般纳税人）重新判仍然可抵 ——
	// 身份按**业务发生日**取，不是拿今天的身份算过去的税
	d3, f := a.JudgeDeduction(DeductionRequest{
		On: "2026-06-01", Voucher: "special_invoice", TaxYuan: "1300",
	})
	if f != nil {
		t.Fatal(f)
	}
	if !d3.Deductible {
		t.Error("★ 按业务发生日的身份判定：6 月时还是一般纳税人，应可抵扣")
	}
}

// ★ 用户点名的行为：查「小规模 + 出口 + 零税率」不能回一句「找不到政策」。
//
// 要回答的是：你的身份不适用零税率；出口适用**免税、不退税**；
// 相关进项税额不得抵扣也不得退税；存在征税例外时需另行判断。
//
// 这条测到**绑定层**，是因为它同时焊住三件事：
//  1. 转向结果带没带说明（Exact=false 时必须带）；
//  2. 税收处理与进项处理有没有一路传到界面；
//  3. 界面上的「我确认命中例外情形」这个勾**有没有真的生效**。
//
// 第 3 条尤其容易断：中间任何一层结构体少一个字段，勾了也白勾，
// 而且不报错 —— 界面照常显示一个看起来很正常的答案。
func TestSmallScaleExportAsGUIUsesIt(t *testing.T) {
	a, path := newApp(t)
	if _, f := a.OpenBook(path); f != nil {
		t.Fatalf("打开账套失败: %v", f)
	}
	if _, f := a.CreateDemoBook(1); f != nil {
		t.Fatalf("生成演示账套失败: %v", f)
	}
	// 演示账套是一般纳税人，切成小规模才能问出这个问题
	if f := a.SetVATStatus(SetVATStatusRequest{
		Status: "small_scale", From: "2025-01-01", Note: "转小规模",
	}); f != nil {
		t.Fatalf("变更身份失败: %v", f)
	}

	// ① 小规模查出口零税率 —— 必须给出转向说明，而不是报错
	res, f := a.ResolveVATRate(VATRateQuery{
		Status: "small_scale", Subject: "entity",
		Category: "export", Method: "zero_rated", On: "2026-06-01",
	})
	if f != nil {
		t.Fatalf("★ 不该报「找不到政策」：%v", f)
	}
	if res.Exact {
		t.Error("小规模不适用零税率，不该是精确匹配")
	}
	if res.Treatment != "EXEMPT_NO_REFUND" {
		t.Errorf("税收处理 = %s，期望 EXEMPT_NO_REFUND（免税、不退税）", res.Treatment)
	}
	if res.InputTax != "NON_DEDUCTIBLE_NON_REFUNDABLE" {
		t.Errorf("进项处理 = %s，期望不得抵扣、不得退税", res.InputTax)
	}
	// 免税不是 0%：税率必须留空
	if res.RateApplicable {
		t.Errorf("免税档不该适用税率，实际 Rate=%q", res.Rate)
	}
	if res.Rate == "0%" || res.StatutoryRate == "0%" {
		t.Errorf("★ 免税不能用 0%% 表示，实际 Rate=%q Statutory=%q",
			res.Rate, res.StatutoryRate)
	}
	if !strings.Contains(res.Note, "不适用零税率") {
		t.Errorf("说明必须点明「不适用零税率」，实际 %q", res.Note)
	}
	if !strings.Contains(res.Note, "免税") || !strings.Contains(res.Note, "另行判断") {
		t.Errorf("说明要说清免税与征税例外，实际 %q", res.Note)
	}

	// ② 不勾「确认命中例外」→ 结论是免税；勾上 → 才变成按征收率征税
	if res.Conditional {
		t.Error("默认不该命中例外政策")
	}
	exc, f := a.ResolveVATRate(VATRateQuery{
		Status: "small_scale", Subject: "entity",
		Category: "export", Method: "simplified", On: "2026-06-01",
		IncludeConditional: true,
	})
	if f != nil {
		t.Fatalf("勾选例外情形后查税率失败: %v", f)
	}
	if !exc.Conditional {
		t.Error("★ 勾了「确认命中例外」却没生效 —— " +
			"绑定层很可能漏传了 includeConditional")
	}
	if exc.Treatment != "TAXABLE" || exc.Rate != "3%" {
		t.Errorf("命中例外情形应按征收率征税，实际 %s / %s",
			exc.Treatment, exc.Rate)
	}

	// ③ 一般纳税人查同一业务仍是零税率、进项可退 —— 放宽给小规模是错的
	gen, f := a.ResolveVATRate(VATRateQuery{
		Status: "general", Subject: "entity",
		Category: "export", Method: "zero_rated", On: "2026-06-01",
	})
	if f != nil {
		t.Fatalf("一般纳税人查出口零税率失败: %v", f)
	}
	if !gen.Exact || gen.Treatment != "ZERO_RATED" {
		t.Errorf("一般纳税人出口应是零税率的精确匹配，实际 exact=%v %s",
			gen.Exact, gen.Treatment)
	}
	if gen.InputTax != "DEDUCTIBLE_REFUNDABLE" {
		t.Errorf("零税率的进项可抵扣可退，实际 %s", gen.InputTax)
	}
}
