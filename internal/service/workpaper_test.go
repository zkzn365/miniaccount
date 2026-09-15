package service_test

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/domain/period"
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 审计底稿
// ---------------------------------------------------------------------------
//
// 这一组测试盯的是「底稿上的数是不是对的」，以及一条最容易做错的边界：
//
//	**生成凭证 ≠ 已入账。**
//
// 凭证录完只落草稿，过账只在账期结算 —— 调整凭证也一样。
// 所以「这笔调整改了没改到账上」只能看凭证的状态，
// 而底稿的审定数与未更正错报都跟着它走。

// wpPtr 返回一个 int64 的指针（辅助核算维度用）。
func wpPtr(v int64) *int64 { return &v }

// seedMateriality 定一期的重要性水平：基准 1,000 万，比例 0.5%。
//
//	整体重要性        50,000.00
//	实际执行重要性    30,000.00（整体的 60%）
//	明显微小错报临界值 2,500.00（整体的 5%）
func seedMateriality(t *testing.T, svc *service.Service, y, m int) *service.WorkpaperView {
	t.Helper()
	v, err := svc.SaveMateriality(context.Background(), service.MaterialityInput{
		Year: y, Month: m, Benchmark: "assets",
		BenchmarkAmount: money100(10_000_000),
		Note:            "小型企业，以资产总额为基准",
	})
	if err != nil {
		t.Fatalf("保存重要性水平失败: %v", err)
	}
	return v
}

// addAdjustment 登记一笔调整，返回这笔调整的 id。
func addAdjustment(t *testing.T, svc *service.Service,
	in service.AdjustmentInput) (int64, *service.WorkpaperView) {
	t.Helper()
	v, err := svc.SaveAdjustment(context.Background(), in)
	if err != nil {
		t.Fatalf("登记审计调整失败: %v", err)
	}
	return lastAdjustmentID(t, v), v
}

func lastAdjustmentID(t *testing.T, v *service.WorkpaperView) int64 {
	t.Helper()
	if len(v.Adjustments) == 0 {
		t.Fatal("底稿里应当有调整")
	}
	return v.Adjustments[len(v.Adjustments)-1].ID
}

// 少提折旧 3,000：借 管理费用—折旧费 / 贷 累计折旧。
//
// 560205 要求部门辅助核算，所以这笔自带一个部门 ——
// 用它顺带验证辅助核算一路传到凭证。
func depAdjustmentInput(t *testing.T, svc *service.Service, y, m int,
	dept int64, amount int64) service.AdjustmentInput {
	t.Helper()
	return service.AdjustmentInput{
		Year: y, Month: m, Kind: "adjust",
		Summary:  "补提 2025 年折旧",
		Reason:   "折旧计算表显示少提 3,000.00，按平均年限法补提",
		Evidence: "折旧计算表（底稿索引 F-3）",
		Operator: "李审计",
		Lines: []service.AdjustLineInput{
			{AccountCode: "560205", Summary: "补提折旧", Debit: money100(amount), DeptID: &dept},
			{AccountCode: "1602", Summary: "补提折旧", Credit: money100(amount)},
		},
	}
}

// ---------------------------------------------------------------------------
// 重要性水平
// ---------------------------------------------------------------------------

func TestSaveMaterialityComputesThresholds(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)

	v := seedMateriality(t, svc, 2025, 3)
	if v.Materiality == nil {
		t.Fatal("保存后底稿上应当有重要性水平")
	}
	m := v.Materiality
	if m.Overall != money100(50000) {
		t.Errorf("整体重要性 = %s，期望 50,000.00（1,000 万 × 0.5%%）", m.Overall)
	}
	if m.Performance != money100(30000) {
		t.Errorf("实际执行重要性 = %s，期望 30,000.00（整体的 60%%）", m.Performance)
	}
	if m.Trivial != money100(2500) {
		t.Errorf("明显微小错报临界值 = %s，期望 2,500.00（整体的 5%%）", m.Trivial)
	}
	if m.RateLabel != "0.5%" || m.PerformanceLabel != "60%" || m.TrivialLabel != "5%" {
		t.Errorf("比例文字 = %q / %q / %q，期望 0.5%% / 60%% / 5%%",
			m.RateLabel, m.PerformanceLabel, m.TrivialLabel)
	}
	// ★ 底稿上必须能答出「这个数怎么来的」：只给金额是没法复核的
	if len(m.Explain) != 4 {
		t.Fatalf("算式说明应当有 4 行（基准 + 三个门槛），实际 %d 行：%v",
			len(m.Explain), m.Explain)
	}
	if !strings.Contains(m.Explain[1], "10,000,000.00") ||
		!strings.Contains(m.Explain[1], "0.5%") ||
		!strings.Contains(m.Explain[1], "50,000.00") {
		t.Errorf("整体重要性的算式没写全：%q", m.Explain[1])
	}

	// 读回来还是同一套
	again, err := svc.Workpaper(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatalf("读底稿失败: %v", err)
	}
	if again.Materiality == nil || again.Materiality.Overall != m.Overall {
		t.Fatalf("读回的重要性水平不一致: %+v", again.Materiality)
	}
	if !again.Misstatements.HasMateriality {
		t.Error("有重要性水平时 HasMateriality 应当为真")
	}
}

// 基准不给金额时用账套自动取数（这里账套是空的，取数为 0，要能挡住）。
func TestSaveMaterialityRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)

	if _, err := svc.SaveMateriality(ctx, service.MaterialityInput{
		Year: 2025, Month: 3, Benchmark: "cash", BenchmarkAmount: money100(100),
	}); err == nil {
		t.Error("不认识的基准应当报错")
	}
	if _, err := svc.SaveMateriality(ctx, service.MaterialityInput{
		Year: 2025, Month: 13, Benchmark: "assets", BenchmarkAmount: money100(100),
	}); err == nil {
		t.Error("非法期间应当报错")
	}
	// 空账套取不到资产总额：不能悄悄存一个 0 的重要性
	if _, err := svc.SaveMateriality(ctx, service.MaterialityInput{
		Year: 2025, Month: 3, Benchmark: "assets",
	}); err == nil {
		t.Error("取数为 0 时应当报错，而不是存一个 0 的重要性水平")
	}
}

func TestDeleteMateriality(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedMateriality(t, svc, 2025, 3)

	v, err := svc.DeleteMateriality(ctx, 2025, 3)
	if err != nil {
		t.Fatalf("清除重要性水平失败: %v", err)
	}
	if v.Materiality != nil {
		t.Error("清除后不该还有重要性水平")
	}
	if v.Misstatements.HasMateriality {
		t.Error("没有门槛时 HasMateriality 应当为假")
	}
	if !strings.Contains(v.Misstatements.Concludes, "尚未确定重要性水平") {
		t.Errorf("没有门槛时结论应当说清「没法判断」，实际 %q", v.Misstatements.Concludes)
	}
	// 没有门槛就不该过滤：宁可让用户看到全部
	if v.Misstatements.Trivial != 0 || v.Misstatements.Overall != 0 {
		t.Errorf("没有门槛时三个门槛都应当是 0，实际 %s / %s",
			v.Misstatements.Overall, v.Misstatements.Trivial)
	}
}

// ---------------------------------------------------------------------------
// 调整分录：登记、编号、护栏
// ---------------------------------------------------------------------------

func TestAdjustmentAutoNumbering(t *testing.T) {
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")

	_, v := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))
	if got := v.Adjustments[0].Code; got != "ADJ-202503-001" {
		t.Errorf("首笔编号 = %q，期望 ADJ-202503-001（底稿之间要能互相引用）", got)
	}
	in := depAdjustmentInput(t, svc, 2025, 3, dept, 500)
	in.Summary = "补提 2 月折旧"
	_, v = addAdjustment(t, svc, in)
	if got := v.Adjustments[1].Code; got != "ADJ-202503-002" {
		t.Errorf("第二笔编号 = %q，期望 ADJ-202503-002", got)
	}
	// 别的期间各自从头编
	in = depAdjustmentInput(t, svc, 2025, 4, dept, 100)
	_, v = addAdjustment(t, svc, in)
	if got := v.Adjustments[0].Code; got != "ADJ-202504-001" {
		t.Errorf("4 月首笔编号 = %q，期望 ADJ-202504-001", got)
	}
}

// 登记阶段就要把凭证那套护栏走一遍：问题现在报，用户手里正拿着那一行。
func TestAdjustmentGuardrails(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")

	base := depAdjustmentInput(t, svc, 2025, 3, dept, 3000)

	cases := []struct {
		name string
		mod  func(in *service.AdjustmentInput)
		want string
	}{
		{"借贷不平", func(in *service.AdjustmentInput) {
			in.Lines[1].Credit = money100(2999)
		}, "借贷不平"},
		{"没有依据", func(in *service.AdjustmentInput) {
			in.Reason = ""
		}, "依据"},
		{"只有一行", func(in *service.AdjustmentInput) {
			in.Lines = in.Lines[:1]
		}, "两"},
		{"科目不存在", func(in *service.AdjustmentInput) {
			in.Lines[0].AccountCode = "999999"
		}, "不存在"},
		{"汇总科目", func(in *service.AdjustmentInput) {
			in.Lines[0].AccountCode = "5602"
		}, "5602"},
		{"缺部门辅助核算", func(in *service.AdjustmentInput) {
			// 560205 要求部门：这是实务里最常被漏掉的一项，
			// 也是「生成凭证时才报错」最让人恼火的一处
			in.Lines[0].DeptID = nil
		}, "辅助核算"},
		{"同一行借贷都有", func(in *service.AdjustmentInput) {
			in.Lines[0].Credit = money100(1)
		}, "行"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base
			in.Lines = append([]service.AdjustLineInput(nil), base.Lines...)
			c.mod(&in)
			v, err := svc.SaveAdjustment(ctx, in)
			if err == nil {
				t.Fatalf("应当被拦下，实际存进去了：%+v", v.Adjustments)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("报错应当提到 %q，实际：%v", c.want, err)
			}
		})
	}
	if _, err := svc.SaveAdjustment(ctx, base); err != nil {
		t.Fatalf("基准用例本身应当能存下来，实际 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 审定表与未更正错报
// ---------------------------------------------------------------------------

// 审定数 = 账面数 + 未入账调整。
func TestWorksheetBookPlusAdjustment(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3)

	// 账面：本月已过账的折旧 1,000（借 560205 / 贷 1602）
	mustPost(t, svc, "2025-03-31", "计提本月折旧",
		service.VoucherLineInput{AccountCode: "560205", Summary: "计提折旧",
			Debit: money100(1000), DeptID: &dept},
		service.VoucherLineInput{AccountCode: "1602", Summary: "计提折旧",
			Credit: money100(1000)})

	addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	v, err := svc.Workpaper(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatalf("读底稿失败: %v", err)
	}
	row := wpRow(t, v, "560205")
	if row.BookBalance != money100(1000) {
		t.Errorf("账面数 = %s，期望 1,000.00", row.BookBalance)
	}
	if row.AdjustDebit != money100(3000) {
		t.Errorf("调整借方 = %s，期望 3,000.00", row.AdjustDebit)
	}
	if row.Audited != money100(4000) {
		t.Errorf("★ 审定数 = %s，期望 4,000.00（账面 1,000 + 调整 3,000）", row.Audited)
	}
	if !row.Adjusted {
		t.Error("有调整的行应当标记 Adjusted")
	}
	// 累计折旧是贷方科目：审定数按借正贷负
	acc := wpRow(t, v, "1602")
	if acc.BookBalance != -money100(1000) {
		t.Errorf("累计折旧账面数 = %s，期望 -1,000.00（贷方为负）", acc.BookBalance)
	}
	if acc.Audited != -money100(4000) {
		t.Errorf("累计折旧审定数 = %s，期望 -4,000.00", acc.Audited)
	}

	// 未更正错报：这笔还没入账，要报出来并给出结论
	if len(v.Misstatements.Items) != 1 {
		t.Fatalf("未更正错报应当有 1 笔，实际 %d 笔", len(v.Misstatements.Items))
	}
	if v.Misstatements.Total != money100(3000) {
		t.Errorf("未更正错报合计 = %s，期望 3,000.00", v.Misstatements.Total)
	}
	if !strings.Contains(v.Misstatements.Concludes, "低于实际执行重要性") {
		t.Errorf("3,000 低于 30,000 的实际执行重要性，结论应说明尚未构成重大错报，实际 %q",
			v.Misstatements.Concludes)
	}
	if v.Adjustments[0].StateLabel != "未入账（仅登记在底稿）" {
		t.Errorf("状态文字 = %q", v.Adjustments[0].StateLabel)
	}
}

// ★ 生成凭证之后、过账之前：底稿口径一个字都不能变。
//
// 这是这条规则最容易做错的地方 ——
// 若把「已生成凭证」当成「已入账」，审定数会少加这 3,000，
// 未更正错报会凭空消失。
func TestBookedButUnpostedKeepsWorksheetHonest(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3)

	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))
	v, err := svc.PostAdjustment(ctx, id, "李审计")
	if err != nil {
		t.Fatalf("生成调整凭证失败: %v", err)
	}

	adj := v.Adjustments[0]
	if !adj.Booked {
		t.Error("生成凭证后 Booked 应当为真")
	}
	if adj.Posted {
		t.Error("★ 生成的只是草稿，Posted 必须为假 —— 过账只在账期结算")
	}
	if adj.VoucherID == nil {
		t.Fatal("应当记下凭证 id")
	}
	if !strings.Contains(adj.StateLabel, "草稿") {
		t.Errorf("状态文字应当点明还是草稿，实际 %q", adj.StateLabel)
	}

	// 凭证本身：草稿、来源是审计调整
	d, err := svc.Voucher(ctx, *adj.VoucherID)
	if err != nil {
		t.Fatalf("读凭证失败: %v", err)
	}
	if d.Status != "draft" {
		t.Errorf("调整凭证的状态 = %s，期望 draft（过账只在账期结算）", d.Status)
	}
	if d.Source != "audit" || d.SourceLabel != "审计调整" {
		t.Errorf("凭证来源 = %s/%s，期望 audit/审计调整", d.Source, d.SourceLabel)
	}

	// 底稿：审定数照旧加它，未更正错报照旧列它
	row := wpRow(t, v, "560205")
	if row.AdjustDebit != money100(3000) || row.Audited != money100(3000) {
		t.Errorf("★ 凭证还没过账，审定数仍应含这 3,000：调整借方 %s、审定数 %s",
			row.AdjustDebit, row.Audited)
	}
	if v.Misstatements.Total != money100(3000) {
		t.Errorf("★ 草稿没进账，这笔仍是未更正错报，合计 = %s，期望 3,000.00",
			v.Misstatements.Total)
	}

	// ★ 账期结算把它们一起过账 —— 这时底稿口径才该翻转
	if _, err := svc.PostPeriodDrafts(ctx, period.NewKey(2025, 3), "王主管"); err != nil {
		t.Fatalf("过账本期草稿失败: %v", err)
	}
	v, err = svc.Workpaper(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatalf("读底稿失败: %v", err)
	}
	adj = v.Adjustments[0]
	if !adj.Posted {
		t.Fatal("凭证过账后 Posted 应当为真")
	}
	if adj.StateLabel != "已入账（凭证已过账）" {
		t.Errorf("状态文字 = %q", adj.StateLabel)
	}
	row = wpRow(t, v, "560205")
	if row.AdjustDebit != 0 {
		t.Errorf("已入账的调整不该再加进审定数，调整借方 = %s", row.AdjustDebit)
	}
	if row.BookBalance != money100(3000) || row.Audited != money100(3000) {
		t.Errorf("过账后账面数里已经含了它：账面 %s、审定 %s，都应当是 3,000.00",
			row.BookBalance, row.Audited)
	}
	if len(v.Misstatements.Items) != 0 || v.Misstatements.Total != 0 {
		t.Errorf("★ 已过账就不是未更正错报了，实际合计 %s（%d 笔）",
			v.Misstatements.Total, len(v.Misstatements.Items))
	}
	if !strings.Contains(v.Misstatements.Concludes, "没有未更正错报") {
		t.Errorf("结论 = %q", v.Misstatements.Concludes)
	}
}

// 重分类：登记了、也报出来，但不计入错报合计。
func TestReclassCountedSeparately(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedMateriality(t, svc, 2025, 3)

	in := service.AdjustmentInput{
		Year: 2025, Month: 3, Kind: "reclass",
		Summary: "其他应收款重分类至应收账款",
		Reason:  "同一客户的往来挂错科目",
		Lines: []service.AdjustLineInput{
			{AccountCode: "1122", Summary: "重分类", Debit: money100(8000),
				ContactID: wpPtr(mustContact(t, svc, "customer", "甲公司"))},
			{AccountCode: "122103", Summary: "重分类", Credit: money100(8000),
				ContactID: wpPtr(mustContact(t, svc, "other", "乙公司"))},
		},
	}
	if _, err := svc.SaveAdjustment(ctx, in); err != nil {
		t.Fatalf("登记重分类失败: %v", err)
	}
	v, _ := svc.Workpaper(ctx, period.NewKey(2025, 3))
	if v.Misstatements.ReclassCount != 1 {
		t.Errorf("重分类笔数 = %d，期望 1", v.Misstatements.ReclassCount)
	}
	if v.Misstatements.Total != 0 {
		t.Errorf("★ 重分类不动损益，不该计入错报合计，实际 %s", v.Misstatements.Total)
	}
	if len(v.Misstatements.Items) != 1 {
		t.Errorf("重分类也要列在明细里让复核人看到，实际 %d 笔", len(v.Misstatements.Items))
	}
}

// ---------------------------------------------------------------------------
// 生成调整凭证：护栏、幂等、次序
// ---------------------------------------------------------------------------

func TestPostAdjustmentRequiresOperator(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	if _, err := svc.PostAdjustment(ctx, id, "  "); err == nil {
		t.Error("不填操作人应当被拦下 —— 调整凭证要有人负责")
	}
	if _, err := svc.PostAdjustment(ctx, id, "李审计"); err != nil {
		t.Fatalf("填了操作人应当能生成: %v", err)
	}
}

// ★ 同一笔调整不能生成两张凭证 —— 那会让账上重复计一次。
func TestPostAdjustmentTwiceIsRefused(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	v, err := svc.PostAdjustment(ctx, id, "李审计")
	if err != nil {
		t.Fatalf("首次生成失败: %v", err)
	}
	first := *v.Adjustments[0].VoucherID

	if _, err := svc.PostAdjustment(ctx, id, "李审计"); err == nil {
		t.Fatal("第二次生成应当被拦下")
	} else if !strings.Contains(err.Error(), "已经生成过凭证") {
		t.Errorf("报错应当说清已经生成过，实际 %v", err)
	}

	// 账上只该有那一张
	list, err := svc.Vouchers(ctx, service.VoucherQuery{Year: 2025, Month: 3})
	if err != nil {
		t.Fatalf("列凭证失败: %v", err)
	}
	n := 0
	for _, v := range list {
		if v.Source == "audit" {
			n++
			if v.ID != first {
				t.Errorf("调整凭证 id = %d，期望 %d", v.ID, first)
			}
		}
	}
	if n != 1 {
		t.Errorf("账上应当只有 1 张调整凭证，实际 %d 张：%+v", n, list)
	}
}

// 已生成凭证的调整：改不动、也删不掉 —— 否则底稿与凭证会对不上。
func TestBookedAdjustmentIsLocked(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	in := depAdjustmentInput(t, svc, 2025, 3, dept, 3000)
	id, _ := addAdjustment(t, svc, in)
	if _, err := svc.PostAdjustment(ctx, id, "李审计"); err != nil {
		t.Fatalf("生成凭证失败: %v", err)
	}

	in.ID = id
	in.Lines[0].Debit = money100(9999) // 改金额
	in.Lines[1].Credit = money100(9999)
	if _, err := svc.SaveAdjustment(ctx, in); err == nil {
		t.Error("已经生成凭证的调整不该能改 —— 否则底稿金额与凭证金额对不上")
	} else if !strings.Contains(err.Error(), "已经生成凭证") {
		t.Errorf("报错应当说清原因，实际 %v", err)
	}

	if _, err := svc.DeleteAdjustment(ctx, id); err == nil {
		t.Error("已经生成凭证的调整不该能删 —— 会留下一张没有底稿的凭证")
	}

	// 把那张草稿凭证删掉之后，底稿随之解锁：可以重来
	adj, err := svc.Workpaper(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatalf("读底稿失败: %v", err)
	}
	if err := svc.DeleteVoucher(ctx, *adj.Adjustments[0].VoucherID); err != nil {
		t.Fatalf("删除草稿凭证失败: %v", err)
	}
	after, err := svc.Workpaper(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatalf("读底稿失败: %v", err)
	}
	if after.Adjustments[0].Booked || after.Adjustments[0].VoucherID != nil {
		t.Errorf("凭证删掉后不该还显示「已生成凭证」：%+v", after.Adjustments[0])
	}
	if _, err := svc.DeleteAdjustment(ctx, id); err != nil {
		t.Errorf("凭证已删，调整应当可以删除，实际 %v", err)
	}
}

// 删除调整后，未更正错报与审定数都要跟着变。
func TestDeleteAdjustmentUpdatesWorksheet(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3)
	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	v, err := svc.DeleteAdjustment(ctx, id)
	if err != nil {
		t.Fatalf("删除调整失败: %v", err)
	}
	if len(v.Adjustments) != 0 {
		t.Errorf("删除后不该还有调整，实际 %d 笔", len(v.Adjustments))
	}
	if v.Misstatements.Total != 0 {
		t.Errorf("删除后未更正错报合计应为 0，实际 %s", v.Misstatements.Total)
	}
	for _, r := range v.Worksheet {
		if r.AdjustDebit != 0 || r.AdjustCredit != 0 {
			t.Errorf("删除后 %s 不该还有调整：借 %s 贷 %s",
				r.AccountCode, r.AdjustDebit, r.AdjustCredit)
		}
	}
}

// wpRow 找审定表里的一行。
func wpRow(t *testing.T, v *service.WorkpaperView, code string) service.WorksheetRowView {
	t.Helper()
	for _, r := range v.Worksheet {
		if r.AccountCode == code {
			return r
		}
	}
	t.Fatalf("审定表里没有科目 %s，实际有 %d 行", code, len(v.Worksheet))
	return service.WorksheetRowView{}
}

// ★ 调整分录的视图必须带上辅助核算的四个 id。
//
// 发布前界面审计实测：界面「修改」一笔带部门的调整时，
// 视图里没有 deptId，部门下拉是空的，保存被「缺少必需的辅助核算」拦下 ——
// 而用户在界面上看着那一行是齐的。
func TestAdjustLineViewExposesAuxIDs(t *testing.T) {
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	cust := mustContact(t, svc, "customer", "甲公司")

	v, err := svc.SaveAdjustment(context.Background(), service.AdjustmentInput{
		Year: 2025, Month: 3, Kind: "adjust",
		Summary: "补提折旧", Reason: "折旧表少提",
		Lines: []service.AdjustLineInput{
			{AccountCode: "560205", Summary: "补提", Debit: money100(1000), DeptID: &dept},
			{AccountCode: "1122", Summary: "补提", Credit: money100(1000), ContactID: &cust},
		},
	})
	if err != nil {
		t.Fatalf("登记调整失败: %v", err)
	}
	lines := v.Adjustments[0].Lines
	if lines[0].DeptID == nil || *lines[0].DeptID != dept {
		t.Fatalf("★ 行里必须带出 deptId（否则界面改一下就丢辅助核算）：%+v", lines[0])
	}
	if lines[1].ContactID == nil || *lines[1].ContactID != cust {
		t.Errorf("★ 行里必须带出 contactId：%+v", lines[1])
	}
	// 原样回传应当能存下来（这正是界面「修改」的动作）
	if _, err := svc.SaveAdjustment(context.Background(), service.AdjustmentInput{
		ID: v.Adjustments[0].ID, Year: 2025, Month: 3, Kind: "adjust",
		Summary: "补提折旧", Reason: "折旧表少提",
		Lines: []service.AdjustLineInput{
			{AccountCode: "560205", Summary: "补提", Debit: money100(1000),
				DeptID: lines[0].DeptID},
			{AccountCode: "1122", Summary: "补提", Credit: money100(1000),
				ContactID: lines[1].ContactID},
		},
	}); err != nil {
		t.Fatalf("★ 照视图回传应当能保存（界面的「修改」就是这个动作）：%v", err)
	}
}

// ★ 删掉中间一笔调整之后，新登记的编号不能与已有的重号。
//
// 发布前审计实测：原来用 len(list)+1，删掉 002 再新增又得到 002。
// 底稿之间靠编号互相引用，重号等于引用失效。
func TestAdjustmentNumberingAfterDelete(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")

	var codes []string
	for i := 0; i < 3; i++ {
		in := depAdjustmentInput(t, svc, 2025, 3, dept, 100)
		in.Summary = "补提折旧 " + string(rune('A'+i))
		_, v := addAdjustment(t, svc, in)
		codes = append(codes, v.Adjustments[len(v.Adjustments)-1].Code)
	}
	if codes[2] != "ADJ-202503-003" {
		t.Fatalf("前置条件不成立，编号：%v", codes)
	}
	// 删掉中间那一笔
	v, err := svc.Workpaper(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatal(err)
	}
	middle := v.Adjustments[1].ID
	if _, err := svc.DeleteAdjustment(ctx, middle); err != nil {
		t.Fatalf("删除调整失败: %v", err)
	}
	// 再新增一笔：应当是 004，不是 003
	in := depAdjustmentInput(t, svc, 2025, 3, dept, 200)
	in.Summary = "补提折旧 D"
	_, v2 := addAdjustment(t, svc, in)
	got := v2.Adjustments[len(v2.Adjustments)-1].Code
	if got != "ADJ-202503-004" {
		t.Errorf("★ 删掉中间一笔之后新编号应当是 ADJ-202503-004，实际 %s（重号会让引用失效）", got)
	}
	seen := map[string]bool{}
	for _, a := range v2.Adjustments {
		if seen[a.Code] {
			t.Errorf("★ 编号重复：%s", a.Code)
		}
		seen[a.Code] = true
	}
}

// 手填的编号撞了已有的，要给一句能看懂的话（而不是数据库错误）。
func TestAdjustmentRejectsDuplicateCode(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	_, v := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 100))
	first := v.Adjustments[0].Code

	in := depAdjustmentInput(t, svc, 2025, 3, dept, 200)
	in.Code = first
	if _, err := svc.SaveAdjustment(ctx, in); err == nil {
		t.Fatal("手填一个已有的编号应当被拦下")
	} else if !strings.Contains(err.Error(), "已经被另一笔调整用了") {
		t.Errorf("报错要说清是重号：%v", err)
	}
}
