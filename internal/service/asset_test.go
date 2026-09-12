package service_test

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/service"
)

// 固定资产：一台 12,000 元的电脑，3 年，残值 5%，2025-01-10 投入使用。
func seedAsset(t *testing.T, svc *service.Service) *service.AssetView {
	t.Helper()
	dept := mustDepartment(t, svc, "管理部门")
	v, err := svc.SaveAsset(context.Background(), service.AssetInput{
		Code: "SB-001", Name: "笔记本电脑", Category: "electronic",
		DeptID: &dept, OrigValue: money100(12000), SalvagePPM: 50_000,
		UsefulMonths: 36, StartDate: "2025-01-10",
	})
	if err != nil {
		t.Fatalf("保存固定资产失败: %v", err)
	}
	return v
}

// 一笔 60,000 元、12 期的一年的房租（长期待摊）。
func seedAmort(t *testing.T, svc *service.Service) *service.AmortizationView {
	t.Helper()
	dept := mustDepartment(t, svc, "管理部门")
	v, err := svc.SaveAmortization(context.Background(), service.AmortizationInput{
		Name: "一年期房租", DeptID: &dept, Total: money100(60000), Months: 12,
		StartDate: "2025-01-01", ExpenseAccount: "560210",
	})
	if err != nil {
		t.Fatalf("保存待摊项目失败: %v", err)
	}
	return v
}

// ★ 计提出来的凭证是**草稿**，而且当场不进总账。
//
// 这和手工凭证、工资凭证是同一条规则：过账只在账期结算。
func TestAccrueCreatesDraftVoucher(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedAsset(t, svc)

	res, err := svc.Accrue(ctx, period.NewKey(2025, 2), "李会计")
	if err != nil {
		t.Fatalf("计提失败: %v", err)
	}
	if res.DepreciationVoucherID == 0 {
		t.Fatal("应当生成一张折旧凭证")
	}
	if res.AmortizationVoucherID != 0 {
		t.Errorf("没有待摊项目，不该生成摊销凭证，实际 %d", res.AmortizationVoucherID)
	}

	d, err := svc.Voucher(ctx, res.DepreciationVoucherID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "draft" {
		t.Errorf("★ 计提凭证应当是草稿，实际 %s", d.Status)
	}
	if d.No != "" {
		t.Errorf("★ 草稿不该有凭证号，实际 %q", d.No)
	}
	if d.Source != "depreciation" {
		t.Errorf("凭证来源 = %q，期望 depreciation", d.Source)
	}

	// 总账里必须还是空的
	debit, credit, err := svc.DB().Vouchers().TrialBalance(ctx, period.NewKey(2025, 2))
	if err != nil {
		t.Fatal(err)
	}
	if !debit.IsZero() || !credit.IsZero() {
		t.Errorf("★ 还没结算，总账就有数了：借 %s 贷 %s", debit, credit)
	}

	// 结算之后才进账
	mustPostPeriod(t, svc, "2025-02")
	posted, err := svc.Voucher(ctx, res.DepreciationVoucherID)
	if err != nil {
		t.Fatal(err)
	}
	if posted.Status != "posted" || posted.No == "" {
		t.Errorf("结算后应当过账并有号：%s %q", posted.Status, posted.No)
	}
}

// 折旧凭证的金额与分录方向：借 折旧费（带部门）/ 贷 累计折旧
func TestAccrueVoucherLines(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedAsset(t, svc) // 12000, 残值 5% → 应提 11,400 / 36 = 316.666… → 316.67

	res, err := svc.Accrue(ctx, period.NewKey(2025, 2), "李会计")
	if err != nil {
		t.Fatal(err)
	}
	d, err := svc.Voucher(ctx, res.DepreciationVoucherID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Lines) != 2 {
		t.Fatalf("分录数 = %d，期望 2（一借一贷）：%+v", len(d.Lines), d.Lines)
	}
	if !d.Balanced {
		t.Error("折旧凭证必须借贷平衡")
	}
	// 借 560205 管理费用—折旧费
	if d.Lines[0].AccountCode != "560205" {
		t.Errorf("借方科目 = %s，期望 560205", d.Lines[0].AccountCode)
	}
	// (12,000 − 600) / 36 = 316.666… → 316.67
	wantMonthly, err := money100(11400).DivInt(36)
	if err != nil {
		t.Fatal(err)
	}
	if d.Lines[0].Debit != wantMonthly {
		t.Errorf("借方金额 = %s，期望 %s", d.Lines[0].Debit, wantMonthly)
	}
	// ★ 费用科目要求部门辅助核算，借方必须带部门 —— 否则过不了账
	if !strings.Contains(d.Lines[0].AuxDesc, "管理部门") {
		t.Errorf("★ 借方缺部门辅助核算：%q", d.Lines[0].AuxDesc)
	}
	// 贷 1602 累计折旧，不挂部门
	if d.Lines[1].AccountCode != "1602" {
		t.Errorf("贷方科目 = %s，期望 1602", d.Lines[1].AccountCode)
	}
	if d.Lines[1].AuxDesc != "" {
		t.Errorf("累计折旧不该挂辅助核算：%q", d.Lines[1].AuxDesc)
	}
}

// ★ 重复计提必须被拒绝。
//
// 漏提一个月不会报错（少一笔费用），重复提一个月也不会报错
// （多一笔费用）—— 两个都是看不出来的错，只能靠约束挡住。
func TestAccrueTwiceIsRejected(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedAsset(t, svc)

	k := period.NewKey(2025, 2)
	if _, err := svc.Accrue(ctx, k, "李会计"); err != nil {
		t.Fatal(err)
	}
	// 再点一次：预览要说「本期已计提过」
	pv, err := svc.PreviewAccrual(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if !pv.DepreciationDone {
		t.Error("预览应当报出「本期已计提」")
	}

	// 强行再提：应当被拒绝，且错误里说清楚是重复
	_, err = svc.Accrue(ctx, k, "李会计")
	if err == nil {
		t.Fatal("★ 同一期不该能提两次")
	}
	if !strings.Contains(err.Error(), "已经计提过") {
		t.Errorf("错误要说清楚是重复计提，实际：%v", err)
	}
}

// ★ 漏提一期不会报任何错，所以预览必须把「上一期还有几条没提」露出来
func TestAccrualPreviewReportsSkippedPreviousPeriod(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedAsset(t, svc)

	// 2 月不提，直接看 3 月
	pv, err := svc.PreviewAccrual(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatal(err)
	}
	if pv.PreviousPeriod != "2025-02" {
		t.Errorf("上一期 = %q，期望 2025-02", pv.PreviousPeriod)
	}
	if pv.PreviousMissing != 1 {
		t.Errorf("★ 上一期漏提了 1 条，预览却报 %d —— 漏提不会报错，只能这样露出来",
			pv.PreviousMissing)
	}

	// 提了 2 月之后，3 月的预览就不该再报漏提
	if _, err := svc.Accrue(ctx, period.NewKey(2025, 2), "李会计"); err != nil {
		t.Fatal(err)
	}
	pv, err = svc.PreviewAccrual(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatal(err)
	}
	if pv.PreviousMissing != 0 {
		t.Errorf("2 月已经提过了，不该再报漏提：%d", pv.PreviousMissing)
	}
}

// 折旧从**次月**起提：1 月投入使用，1 月不提、2 月才提
func TestAccrualRespectsNextMonthRule(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedAsset(t, svc) // 2025-01-10 投入使用

	pv, err := svc.PreviewAccrual(ctx, period.NewKey(2025, 1))
	if err != nil {
		t.Fatal(err)
	}
	if pv.DepreciationTotal.IsPositive() {
		t.Errorf("★ 投入使用当月不该计提，实际 %s", pv.DepreciationTotal)
	}
	var reason string
	for _, r := range pv.Rows {
		if r.Kind == "asset" {
			reason = r.Reason
		}
	}
	if !strings.Contains(reason, "次月起提") {
		t.Errorf("要说明为什么是 0，实际 %q", reason)
	}

	pv, err = svc.PreviewAccrual(ctx, period.NewKey(2025, 2))
	if err != nil {
		t.Fatal(err)
	}
	if !pv.DepreciationTotal.IsPositive() {
		t.Error("次月应当计提")
	}
}

// ★ 全周期提完，累计必须恰好等于应提总额（尾差不能留在账上）
//
// 用一笔除不尽的：10,000.00 ÷ 6 = 1,666.666… 取 1,666.67，
// 六期加起来 10,000.02，多出的 2 分必须在最后一期扣回去。
// 不扣的话，这台设备的累计折旧会**超过**原值 —— 报表上固定资产
// 净值变成负数，而每张凭证单看都是对的。
func TestAccrualOverFullLifeSumsExactly(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 7) // 1~7 月都是已启用期间
	dept := mustDepartment(t, svc, "管理部门")
	if _, err := svc.SaveAsset(ctx, service.AssetInput{
		Name: "除不尽的设备", Category: "electronic", DeptID: &dept,
		OrigValue: money100(10000), SalvagePPM: 0, UsefulMonths: 6,
		StartDate: "2025-01-10",
	}); err != nil {
		t.Fatal(err)
	}

	var total money.Money
	for m := 2; m <= 7; m++ { // 次月起，共 6 期
		res, err := svc.Accrue(ctx, period.NewKey(2025, m), "李会计")
		if err != nil {
			t.Fatalf("%d 月计提失败: %v", m, err)
		}
		total = total.Add(res.DepreciationTotal)
	}
	if total != money100(10000) {
		t.Errorf("★ 6 期累计折旧 = %s，期望 10,000.00，差 %s —— 尾差会永远挂在账上",
			total, money100(10000).Sub(total))
	}

	// 提足之后不再提
	pv, err := svc.PreviewAccrual(ctx, period.NewKey(2025, 8))
	if err != nil {
		t.Fatal(err)
	}
	if pv.DepreciationTotal.IsPositive() {
		t.Errorf("提足之后还在提：%s", pv.DepreciationTotal)
	}
	// 累计不能超过应提总额
	view, err := svc.Assets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Assets[0].Depreciated != money100(10000) {
		t.Errorf("累计折旧 = %s，期望 10,000.00", view.Assets[0].Depreciated)
	}
	if view.Assets[0].NetValue.IsNegative() {
		t.Errorf("★ 净值变成负数了：%s", view.Assets[0].NetValue)
	}
}

// 摊销：从**当月**起摊（与固定资产的次月不同）
func TestAmortizationStartsSameMonth(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedAmort(t, svc) // 2025-01-01 起受益

	pv, err := svc.PreviewAccrual(ctx, period.NewKey(2025, 1))
	if err != nil {
		t.Fatal(err)
	}
	if pv.AmortizationTotal != money100(5000) {
		t.Errorf("★ 首期摊销 = %s，期望 5,000.00（受益期从当月起）", pv.AmortizationTotal)
	}

	res, err := svc.Accrue(ctx, period.NewKey(2025, 1), "李会计")
	if err != nil {
		t.Fatal(err)
	}
	d, err := svc.Voucher(ctx, res.AmortizationVoucherID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "amortization" {
		t.Errorf("凭证来源 = %q", d.Source)
	}
	// 借 560210 管理费用—租赁费 / 贷 1801 长期待摊费用
	if len(d.Lines) != 2 ||
		d.Lines[0].AccountCode != "560210" || d.Lines[1].AccountCode != "1801" {
		t.Errorf("摊销凭证分录不对：%+v", d.Lines)
	}
}

// ★ 折旧 + 摊销同一个月：出两张凭证，各归各的
func TestAccrueBothKindsProduceTwoVouchers(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedAsset(t, svc)
	seedAmort(t, svc)

	res, err := svc.Accrue(ctx, period.NewKey(2025, 2), "李会计")
	if err != nil {
		t.Fatal(err)
	}
	if res.DepreciationVoucherID == 0 || res.AmortizationVoucherID == 0 {
		t.Fatalf("应当生成两张凭证：%+v", res)
	}
	if res.DepreciationVoucherID == res.AmortizationVoucherID {
		t.Error("折旧与摊销应当各出一张凭证 —— 混在一起事后拆不开")
	}
	if !res.DepreciationTotal.IsPositive() || !res.AmortizationTotal.IsPositive() {
		t.Errorf("两类合计都要有：%+v", res)
	}
}

// 处置之后次月起不再计提
func TestDisposeStopsAccrual(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 12)
	a := seedAsset(t, svc)

	if _, err := svc.DisposeAsset(ctx, a.ID, "2025-04-15", "报废"); err != nil {
		t.Fatalf("处置失败: %v", err)
	}
	// 处置当月照提
	pv, err := svc.PreviewAccrual(ctx, period.NewKey(2025, 4))
	if err != nil {
		t.Fatal(err)
	}
	if !pv.DepreciationTotal.IsPositive() {
		t.Error("★ 处置当月仍应计提")
	}
	// 次月停
	pv, err = svc.PreviewAccrual(ctx, period.NewKey(2025, 5))
	if err != nil {
		t.Fatal(err)
	}
	if pv.DepreciationTotal.IsPositive() {
		t.Errorf("★ 处置次月不该再提：%s", pv.DepreciationTotal)
	}
	// 重复处置要拒绝
	if _, err := svc.DisposeAsset(ctx, a.ID, "2025-06-01", ""); err == nil {
		t.Error("已经处置过的不能再处置一次")
	}
}

// ★ 费用科目要求部门辅助核算时，卡片就必须填部门 ——
// 而且要**在保存卡片时**报错，不能等到月底计提那天
func TestSaveAssetRequiresDeptWhenAccountNeedsIt(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)

	_, err := svc.SaveAsset(ctx, service.AssetInput{
		Name: "没填部门的设备", Category: "electronic",
		OrigValue: money100(1000), UsefulMonths: 12, StartDate: "2025-01-05",
	})
	if err == nil {
		t.Fatal("★ 560205 要求部门辅助核算，没填部门就应当当场报错")
	}
	if !strings.Contains(err.Error(), "部门") {
		t.Errorf("错误要指向部门：%v", err)
	}
}

// 银行/往来那种不需要部门的科目也不能硬塞 —— 这里测反向：
// 换一个不要求部门的费用科目，不填部门也应该能存
func TestSaveAssetAllowsNoDeptForPlainAccount(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	// 5601 销售费用是汇总科目（不可记账），换 5603 财务费用试试；
	// 若它也不要部门，就应当能存下来
	tree, err := svc.DB().Accounts().Tree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var plain string
	for _, a := range tree.EnabledLeaves() {
		if len(a.AuxTypes) == 0 && strings.HasPrefix(a.Code, "5603") {
			plain = a.Code
			break
		}
	}
	if plain == "" {
		t.Skip("账套里没有不要求辅助核算的财务费用明细科目")
	}
	if _, err := svc.SaveAsset(ctx, service.AssetInput{
		Name: "不挂部门的设备", Category: "electronic",
		OrigValue: money100(1000), UsefulMonths: 12, StartDate: "2025-01-05",
		ExpenseAccount: plain,
	}); err != nil {
		t.Fatalf("不要求部门的科目应当能存下来: %v", err)
	}
}

// 已经计提过的卡片不能删；一张都还没提的可以删
func TestDeleteAssetRules(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	a := seedAsset(t, svc)

	// 还没提过 → 能删
	if err := svc.DeleteAsset(ctx, a.ID); err != nil {
		t.Fatalf("还没计提过的卡片应当能删: %v", err)
	}

	// 再建一张，提一期 → 不能删
	b := seedAsset(t, svc)
	if _, err := svc.Accrue(ctx, period.NewKey(2025, 2), "李会计"); err != nil {
		t.Fatal(err)
	}
	err := svc.DeleteAsset(ctx, b.ID)
	if err == nil {
		t.Fatal("★ 已经计提过折旧的卡片不该能删 —— 凭证上「每月提多少」的依据就没了")
	}
	if !strings.Contains(err.Error(), "处置") {
		t.Errorf("要给出替代做法（改用处置）：%v", err)
	}
}

// 作废的待摊项目不再摊销，但可以恢复
func TestVoidAmortization(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	m := seedAmort(t, svc)

	if _, err := svc.VoidAmortization(ctx, m.ID, true); err != nil {
		t.Fatal(err)
	}
	pv, err := svc.PreviewAccrual(ctx, period.NewKey(2025, 2))
	if err != nil {
		t.Fatal(err)
	}
	if pv.AmortizationTotal.IsPositive() {
		t.Errorf("★ 作废的项目还在摊：%s", pv.AmortizationTotal)
	}
	// 恢复之后接着摊
	if _, err := svc.VoidAmortization(ctx, m.ID, false); err != nil {
		t.Fatal(err)
	}
	pv, err = svc.PreviewAccrual(ctx, period.NewKey(2025, 2))
	if err != nil {
		t.Fatal(err)
	}
	if !pv.AmortizationTotal.IsPositive() {
		t.Error("恢复之后应当接着摊")
	}
}

// 全周期摊销同样要恰好摊完
func TestAmortizationOverFullLifeSumsExactly(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 7)
	// 10,000 元分 6 期 —— 除不尽，尾差 0.02
	dept := mustDepartment(t, svc, "管理部门")
	if _, err := svc.SaveAmortization(ctx, service.AmortizationInput{
		Name: "除不尽的待摊", DeptID: &dept, Total: money100(10000), Months: 6,
		StartDate: "2025-01-01", ExpenseAccount: "560210",
	}); err != nil {
		t.Fatal(err)
	}
	var total money.Money
	for m := 1; m <= 6; m++ {
		res, err := svc.Accrue(ctx, period.NewKey(2025, m), "李会计")
		if err != nil {
			t.Fatalf("%d 月摊销失败: %v", m, err)
		}
		total = total.Add(res.AmortizationTotal)
	}
	if total != money100(10000) {
		t.Errorf("★ 6 期累计摊销 = %s，期望 10,000.00，差 %s",
			total, money100(10000).Sub(total))
	}
	// 摊完之后余额归零
	view, err := svc.Assets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Amortizations[0].Remaining.IsZero() {
		t.Errorf("摊完还剩 %s", view.Amortizations[0].Remaining)
	}
}

// 税法最低年限：填短了只警告，不拦
func TestShortUsefulLifeOnlyWarns(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "管理部门")
	v, err := svc.SaveAsset(ctx, service.AssetInput{
		Name: "两年提完的电脑", Category: "electronic", // 税法最低 3 年
		DeptID: &dept, OrigValue: money100(6000),
		UsefulMonths: 24, StartDate: "2025-01-05",
	})
	if err != nil {
		t.Fatalf("会计上可以按 2 年提，不该被拦住: %v", err)
	}
	if v.LifeWarning == "" {
		t.Error("★ 短于税法最低年限必须给出提示（要做纳税调整），实际没有任何提示")
	}
	if !strings.Contains(v.LifeWarning, "纳税调整") {
		t.Errorf("提示要说清楚后果：%q", v.LifeWarning)
	}
	if v.MinYears != 3 {
		t.Errorf("最低年限 = %d，期望 3", v.MinYears)
	}
}

// 档案页要把「已经提了多少、还值多少」算出来
func TestAssetsViewCarriesDepreciatedAndNetValue(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedAsset(t, svc)
	if _, err := svc.Accrue(ctx, period.NewKey(2025, 2), "李会计"); err != nil {
		t.Fatal(err)
	}

	view, err := svc.Assets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Assets) != 1 {
		t.Fatalf("固定资产数 = %d", len(view.Assets))
	}
	a := view.Assets[0]
	monthly := a.MonthlyAmount
	if !monthly.IsPositive() {
		t.Fatal("月折旧额应当为正")
	}
	if a.Depreciated != monthly {
		t.Errorf("累计折旧 = %s，期望一期 %s", a.Depreciated, monthly)
	}
	if a.NetValue != a.OrigValue.Sub(monthly) {
		t.Errorf("净值 = %s，期望 %s", a.NetValue, a.OrigValue.Sub(monthly))
	}
	if a.CanDelete {
		t.Error("已经计提过的卡片不该可删")
	}
	if a.FirstPeriod != "2025-02" {
		t.Errorf("起提期间 = %s，期望 2025-02（次月）", a.FirstPeriod)
	}
	if len(view.Categories) != 5 {
		t.Errorf("税法类别应有 5 个，实际 %d", len(view.Categories))
	}
}

// 未启用的期间不能计提（与手工凭证同一条规则）
func TestAccrueRejectsFuturePeriod(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 2) // 期间只开到 2 月
	seedAsset(t, svc)
	_, err := svc.Accrue(ctx, period.NewKey(2025, 9), "李会计")
	if err == nil {
		t.Fatal("未启用的期间不该能计提")
	}
}
