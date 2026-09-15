package service_test

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 税务申报台账
// ---------------------------------------------------------------------------
//
// 这一组测试盯的是台账存在的理由：
//
//	**申报是发生过的事实，计算表是派生结果。**
//
// 所以：同一期同一税种只能有一条有效记录；更正要先作废（留痕）；
// 而账后来改了之后，「当时报了多少」与「现在算出多少」的差必须被看见。

// 一个已申报的增值税期间：销项 13,000、进项 6,500 → 应补 7,280。
func seedVATPeriod(t *testing.T, svc *service.Service) {
	t.Helper()
	mustPost(t, svc, "2025-03-31", "销售",
		service.VoucherLineInput{AccountCode: "1122", Summary: "销售", Debit: money100(113_000),
			ContactID: wpPtr(mustContact(t, svc, "customer", "甲公司"))},
		service.VoucherLineInput{AccountCode: "5001", Summary: "销售", Credit: money100(100_000)},
		service.VoucherLineInput{AccountCode: "22210102", Summary: "销项税额", Credit: money100(13_000)})
	mustPost(t, svc, "2025-03-31", "采购",
		service.VoucherLineInput{AccountCode: "1403", Summary: "采购", Debit: money100(50_000)},
		service.VoucherLineInput{AccountCode: "22210101", Summary: "进项税额", Debit: money100(6_500)},
		service.VoucherLineInput{AccountCode: "2202", Summary: "采购", Credit: money100(56_500),
			ContactID: wpPtr(mustContact(t, svc, "supplier", "乙公司"))})
}

// 登记一条申报：金额默认按当前计算表填，不用人抄。
func TestTaxFilingSaveFromCurrentReturn(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedVATPeriod(t, svc)

	v, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		Kind: "vat", Year: 2025, Month: 3,
		Status: "paid", FiledDate: "2025-04-15", PaidDate: "2025-04-18",
		Channel: "电子税务局", ReceiptNo: "1234567890", Operator: "李会计",
	})
	if err != nil {
		t.Fatalf("登记申报失败: %v", err)
	}
	if len(v.Items) != 1 {
		t.Fatalf("台账里应当有 1 条，实际 %d 条", len(v.Items))
	}
	item := v.Items[0]
	// 应补 = 6,500（增值税）+ 780（附加 12%）= 7,280
	if int64(item.Payable) != int64(money100(7_280)) {
		t.Errorf("★ 应补(退)税额 = %s，期望 7,280.00 —— 金额应当按当前计算表填，"+
			"不该让人手抄", item.Payable)
	}
	if int64(item.TaxAmount) != int64(money100(6_500)) {
		t.Errorf("其中税额 = %s，期望 6,500.00", item.TaxAmount)
	}
	if int64(item.Surcharge) != int64(money100(780)) {
		t.Errorf("附加税费 = %s，期望 780.00", item.Surcharge)
	}
	if item.StatusLabel != "已申报并缴纳" {
		t.Errorf("状态 = %q", item.StatusLabel)
	}
	if item.Operator != "李会计" {
		t.Errorf("经办人 = %q", item.Operator)
	}
	// 刚登记完，与当前计算表应当一致
	if !strings.Contains(item.Reconcile, "一致") {
		t.Errorf("刚登记的记录应当与计算表一致：%q", item.Reconcile)
	}
}

// 同一属期同一税种只能有一条有效记录 —— 更正要先作废。
func TestTaxFilingRejectsDuplicate(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedVATPeriod(t, svc)
	in := service.TaxFilingInput{
		Kind: "vat", Year: 2025, Month: 3,
		Status: "filed", FiledDate: "2025-04-15", Operator: "李会计",
	}
	if _, err := svc.SaveTaxFiling(ctx, in); err != nil {
		t.Fatalf("首次登记失败: %v", err)
	}
	if _, err := svc.SaveTaxFiling(ctx, in); err == nil {
		t.Fatal("同一期同一税种登记两次应当被拦下")
	} else if !strings.Contains(err.Error(), "已经登记过申报") {
		t.Errorf("报错要说清已经报过、以及怎么更正：%v", err)
	}
}

// ★ 更正申报：作废旧记录（留痕）→ 登记新记录，两条都在台账里。
func TestTaxFilingVoidThenRefile(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedVATPeriod(t, svc)
	v, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		Kind: "vat", Year: 2025, Month: 3,
		Status: "filed", FiledDate: "2025-04-15", Operator: "李会计",
	})
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	id := v.Items[0].ID

	// 作废要留痕：谁、为什么
	if _, err := svc.VoidTaxFiling(ctx, id, "", "报错了"); err == nil {
		t.Error("作废不填经办人应当被拦下")
	}
	if _, err := svc.VoidTaxFiling(ctx, id, "李会计", "  "); err == nil {
		t.Error("作废不写原因应当被拦下")
	}
	v2, err := svc.VoidTaxFiling(ctx, id, "李会计", "应补税额填错了，更正申报")
	if err != nil {
		t.Fatalf("作废失败: %v", err)
	}
	if v2.Voided != 1 || v2.Effective != 0 {
		t.Fatalf("作废后应当是 0 条有效 / 1 条作废，实际 %d / %d", v2.Effective, v2.Voided)
	}
	if v2.Items[0].VoidedBy != "李会计" || v2.Items[0].VoidReason == "" {
		t.Errorf("作废痕迹没记全：%+v", v2.Items[0])
	}

	// 作废之后可以重新登记
	v3, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		Kind: "vat", Year: 2025, Month: 3,
		Status: "filed", FiledDate: "2025-04-20", Operator: "李会计",
		Note: "更正申报",
	})
	if err != nil {
		t.Fatalf("作废后重新登记失败: %v", err)
	}
	if v3.Effective != 1 || v3.Voided != 1 {
		t.Errorf("应当 1 条有效 + 1 条作废（痕迹要留着），实际 %d / %d",
			v3.Effective, v3.Voided)
	}
	if len(v3.Items) != 2 {
		t.Errorf("★ 台账里应当能看到两条（报过、作废、重报），实际 %d 条", len(v3.Items))
	}
	// 作废记录不参与勾稽
	for _, it := range v3.Items {
		if it.StatusLabel == "已作废" && !strings.Contains(it.Reconcile, "不参与勾稽") {
			t.Errorf("作废记录的勾稽说明不对：%q", it.Reconcile)
		}
	}
}

// ★ 申报之后账又改了：差异必须报出来。
//
// 不报出来的话，申报数与账面数会悄悄分叉，等税务检查时才发现。
func TestTaxFilingReconcilesAfterLaterEntries(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedVATPeriod(t, svc)
	if _, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		Kind: "vat", Year: 2025, Month: 3,
		Status: "paid", FiledDate: "2025-04-15", PaidDate: "2025-04-18",
		Operator: "李会计",
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}

	// 申报之后又开了一张销项票：现在算出来比当时申报的多 1,300
	mustPost(t, svc, "2025-03-31", "补录销售",
		service.VoucherLineInput{AccountCode: "1122", Summary: "补录销售", Debit: money100(11_300),
			ContactID: wpPtr(mustContact(t, svc, "customer", "甲公司"))},
		service.VoucherLineInput{AccountCode: "5001", Summary: "补录销售", Credit: money100(10_000)},
		service.VoucherLineInput{AccountCode: "22210102", Summary: "销项税额", Credit: money100(1_300)})

	v, err := svc.TaxFilings(ctx, 2025)
	if err != nil {
		t.Fatalf("读台账失败: %v", err)
	}
	item := v.Items[0]
	if int64(item.Diff) == 0 {
		t.Fatalf("★ 账改了之后台账与计算表应当有差异，实际一致（台账 %s / 现在 %s）",
			item.Payable, item.Computed)
	}
	if !strings.Contains(item.Reconcile, "更正申报") {
		t.Errorf("勾稽结论要说明要不要更正申报：%q", item.Reconcile)
	}
	if int64(item.Diff) != int64(money100(1_456)) {
		// 1,300 增值税 + 156 附加（12%）= 1,456
		t.Errorf("差异 = %s，期望 1,456.00（1,300 增值税 + 12%% 附加）", item.Diff)
	}
}

// 计算表上要显示申报状态 —— 否则用户可能照着再报一次。
func TestTaxReturnShowsFilingStatus(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedVATPeriod(t, svc)

	// 报之前：提示还没登记
	v := taxReturn(t, svc, "vat", 2025, 3, service.TaxReturnInput{})
	if v.Filing != nil {
		t.Error("还没登记时不该有申报记录")
	}
	if !strings.Contains(v.FilingHint, "还没有登记申报记录") {
		t.Errorf("提示 = %q", v.FilingHint)
	}
	if int64(v.Payable) == 0 {
		t.Error("★ 计算表应当给出应付金额（申报台账要用它做快照）")
	}

	if _, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		Kind: "vat", Year: 2025, Month: 3,
		Status: "filed", FiledDate: "2025-04-15", Operator: "李会计",
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	v2 := taxReturn(t, svc, "vat", 2025, 3, service.TaxReturnInput{})
	if v2.Filing == nil {
		t.Fatal("★ 报完之后计算表上必须显示申报状态")
	}
	if v2.Filing.StatusLabel != "已申报" {
		t.Errorf("状态 = %q", v2.Filing.StatusLabel)
	}
	if !strings.Contains(v2.FilingHint, "一致") {
		t.Errorf("勾稽提示 = %q", v2.FilingHint)
	}
}

// 待申报提醒：本年已启用但没登记的期间要列出来。
func TestTaxFilingPendingReminder(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3) // 2025-01 ~ 03
	seedVATPeriod(t, svc)

	v, err := svc.TaxFilings(ctx, 2025)
	if err != nil {
		t.Fatalf("读台账失败: %v", err)
	}
	if len(v.Pending) == 0 {
		t.Fatal("★ 有已启用期间却没有申报记录时，必须提醒")
	}
	found := false
	for _, p := range v.Pending {
		if p.Period == "2025-03" && p.Kind == "vat" {
			found = true
			if !strings.Contains(p.Hint, "登记") {
				t.Errorf("提醒要给出下一步动作：%q", p.Hint)
			}
		}
	}
	if !found {
		t.Errorf("待申报列表里没有 2025-03 的增值税：%+v", v.Pending)
	}
	if !strings.Contains(v.Concludes, "还没有登记任何申报记录") {
		t.Errorf("结论 = %q", v.Concludes)
	}
}

// 台账的几道护栏。
func TestTaxFilingGuardrails(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedVATPeriod(t, svc)
	base := service.TaxFilingInput{
		Kind: "vat", Year: 2025, Month: 3,
		Status: "filed", FiledDate: "2025-04-15", Operator: "李会计",
	}
	cases := []struct {
		name string
		mod  func(in *service.TaxFilingInput)
		want string
	}{
		{"没有经办人", func(in *service.TaxFilingInput) { in.Operator = "  " }, "经办人"},
		{"税种不认识", func(in *service.TaxFilingInput) { in.Kind = "stamp" }, "税种"},
		{"期间非法", func(in *service.TaxFilingInput) { in.Month = 13 }, "期间"},
		{"申报日期早于月末", func(in *service.TaxFilingInput) { in.FiledDate = "2025-03-10" }, "早于属期月末"},
		{"状态不认识", func(in *service.TaxFilingInput) { in.Status = "maybe" }, "状态"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base
			c.mod(&in)
			if _, err := svc.SaveTaxFiling(ctx, in); err == nil {
				t.Fatal("应当被拦下")
			} else if !strings.Contains(err.Error(), c.want) {
				t.Errorf("报错应当提到 %q，实际 %v", c.want, err)
			}
		})
	}
	if _, err := svc.SaveTaxFiling(ctx, base); err != nil {
		t.Fatalf("基准用例本身应当通过：%v", err)
	}
}

// 三种税的属期文字：企业所得税按季度说。
func TestTaxFilingPeriodLabel(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	v, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		Kind: "cit", Year: 2025, Month: 5,
		Status: "filed", FiledDate: "2025-07-15", Operator: "李会计",
	})
	if err != nil {
		t.Fatalf("登记企业所得税申报失败: %v", err)
	}
	if !strings.Contains(v.Items[0].PeriodLabel, "第 2 季度") {
		t.Errorf("企业所得税的属期应当写成季度（2025 年第 2 季度），实际 %q",
			v.Items[0].PeriodLabel)
	}
	// 个税按月
	v2, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		Kind: "iit", Year: 2025, Month: 5,
		Status: "filed", FiledDate: "2025-06-15", Operator: "李会计",
	})
	if err != nil {
		t.Fatalf("登记个税申报失败: %v", err)
	}
	for _, it := range v2.Items {
		if it.Kind == "iit" && it.PeriodLabel != "2025-05" {
			t.Errorf("个税的属期应当按月（2025-05），实际 %q", it.PeriodLabel)
		}
	}
}

// ★ 作废过的记录不能被改回来，金额快照也不能直接改。
//
// 发布前审计实测：登记 → 作废 → 用同一个 id 再存一次（改金额、换经办人），
// 记录会复活成「已申报」，voided_by / void_reason 被清空 ——
// 「作废留痕」变成一句空话。
func TestTaxFilingCannotReviveVoided(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedVATPeriod(t, svc)
	v, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		Kind: "vat", Year: 2025, Month: 3, Status: "filed",
		FiledDate: "2025-04-15", Operator: "李会计",
	})
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	id := v.Items[0].ID
	if _, err := svc.VoidTaxFiling(ctx, id, "李会计", "报错了"); err != nil {
		t.Fatalf("作废失败: %v", err)
	}

	// 用同一个 id 改回来：必须被拒
	if _, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		ID: id, Kind: "vat", Year: 2025, Month: 3, Status: "filed",
		FiledDate: "2025-04-20", Operator: "王会计",
		FromCurrent: true,
	}); err == nil {
		t.Fatal("★ 作废过的记录不该能改回来 —— 否则作废留痕没有意义")
	} else if !strings.Contains(err.Error(), "已经作废") {
		t.Errorf("报错要说清已作废、并指向「新登记」：%v", err)
	}

	// 台账里那条仍然是作废状态，痕迹都在
	after, err := svc.TaxFilings(ctx, 2025)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range after.Items {
		if it.ID != id {
			continue
		}
		if it.StatusLabel != "已作废" {
			t.Errorf("状态应当还是「已作废」，实际 %q", it.StatusLabel)
		}
		if it.VoidedBy != "李会计" || it.VoidReason == "" {
			t.Errorf("作废痕迹被清掉了：%+v", it)
		}
	}
}

// ★ 金额是「申报当时的快照」，编辑已有记录时不能被改写。
func TestTaxFilingAmountIsSnapshot(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedVATPeriod(t, svc)
	v, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		Kind: "vat", Year: 2025, Month: 3, Status: "filed",
		FiledDate: "2025-04-15", Operator: "李会计",
	})
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	id := v.Items[0].ID
	original := v.Items[0].Payable

	// 只改备注/回执：允许
	v2, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		ID: id, Kind: "vat", Year: 2025, Month: 3, Status: "paid",
		FiledDate: "2025-04-15", PaidDate: "2025-04-18",
		ReceiptNo: "R-2", Note: "补记缴款日期", Operator: "李会计",
	})
	if err != nil {
		t.Fatalf("改备注/回执应当允许：%v", err)
	}
	if v2.Items[0].Payable != original {
		t.Errorf("金额快照不该变：%s → %s", original, v2.Items[0].Payable)
	}

	// 改金额：必须被拒
	if _, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		ID: id, Kind: "vat", Year: 2025, Month: 3, Status: "paid",
		FiledDate: "2025-04-15", PaidDate: "2025-04-18",
		Payable: money100(9_999), TaxAmount: money100(9_999), Surcharge: 0, Paid: 0,
		Operator: "李会计",
	}); err == nil {
		t.Fatal("★ 申报金额是当时的快照，不该能直接改（更正走作废+新登记）")
	}
}

// ★ 待申报提醒：只提醒**已过完且已启用**的期间，且企业所得税按季度。
//
// 发布前审计实测：原来把 1..12 月 × 3 税种全列成待申报 ——
// 2025-01 启用的账套会把 2025-04~12（还没发生的未来期间）也列上，
// 企业所得税还会按 12 个月列（实际只有 4 个季度）。
func TestTaxFilingPendingOnlyEndedPeriods(t *testing.T) {
	svc := newSvc(t, 3) // 2025-01 ~ 03 已启用，其余未启用
	v, err := svc.TaxFilings(context.Background(), 2025)
	if err != nil {
		t.Fatalf("读台账失败: %v", err)
	}
	for _, p := range v.Pending {
		if strings.HasPrefix(p.Period, "2025-04") || strings.HasPrefix(p.Period, "2025-05") {
			t.Errorf("★ 未启用的未来期间不该出现在待申报里：%+v", p)
		}
		if p.Kind == "cit" {
			// 企业所得税只在 3/6/9/12 月有申报
			if !strings.HasSuffix(p.Period, "-03") && !strings.HasSuffix(p.Period, "-06") &&
				!strings.HasSuffix(p.Period, "-09") && !strings.HasSuffix(p.Period, "-12") {
				t.Errorf("★ 企业所得税是季度申报，不该出现在 %s：%+v", p.Period, p)
			}
		}
	}
	// 已结束的 2025-03 应当在里面（测试环境的「今天」晚于 2025-03）
	found := false
	for _, p := range v.Pending {
		if p.Period == "2025-03" && p.Kind == "vat" {
			found = true
		}
	}
	if !found {
		t.Errorf("已过完的 2025-03 增值税应当提醒，实际待申报：%+v", v.Pending)
	}
}

// ★ 用户手工填的 0（零申报）不能被「按当前计算表填」悄悄改写。
//
// 发布前界面审计实测：服务层按「四个金额都是 0」推断「没填」，
// 于是手工清成 0 的零申报会被当前计算表覆盖 —— 台账记的是
// 「发生过的事实」，不能被派生数字改写。
func TestTaxFilingManualZeroIsKept(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedVATPeriod(t, svc) // 本期计算表算出来是 7,280

	v, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		Kind: "vat", Year: 2025, Month: 3, Status: "filed",
		FiledDate: "2025-04-15", Operator: "李会计",
		// 用户明确手工填 0（零申报）
		ManualAmounts: true,
	})
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	item := v.Items[0]
	if !item.Payable.IsZero() {
		t.Errorf("★ 手工填的 0 被改写成 %s —— 台账不能替用户改数", item.Payable)
	}
	// 从计算表带出来时（ManualAmounts=false 且没填）才允许覆盖
	v2, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		Kind: "iit", Year: 2025, Month: 3, Status: "filed",
		FiledDate: "2025-04-15", Operator: "李会计",
		FromCurrent: true,
	})
	if err != nil {
		t.Fatalf("按计算表登记失败: %v", err)
	}
	for _, it := range v2.Items {
		if it.Kind == "iit" && it.Payable.IsZero() && it.Computed.IsZero() {
			// 没有工资单时两边都是 0，这条只是确认不会报错
			continue
		}
	}
}

// ★ 同一属期的申报记录，属期本身不能改（界面在全年列表上点「改」）。
//
// 发布前审计实测：原来拿**入参**的属期去校验申报日期，
// 于是在列表里改 1 月的记录会被报「申报日期早于 6 月月末」——
// 明明没错却不让人改。
func TestTaxFilingEditUsesRecordOwnPeriod(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	// 1 月的申报（申报日期 2 月）
	v, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		Kind: "vat", Year: 2025, Month: 1, Status: "filed",
		FiledDate: "2025-02-15", Operator: "李会计",
	})
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	id := v.Items[0].ID

	// 照记录**自己的**属期回传（界面现在就是这么做的）：应当成功
	if _, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		ID: id, Kind: "vat", Year: 2025, Month: 1, Status: "paid",
		FiledDate: "2025-02-15", PaidDate: "2025-02-20",
		ReceiptNo: "R-9", Operator: "李会计",
	}); err != nil {
		t.Fatalf("照记录自己的属期改应当成功：%v", err)
	}

	// 传一个别的属期：必须明确拒绝「属期不能改」
	if _, err := svc.SaveTaxFiling(ctx, service.TaxFilingInput{
		ID: id, Kind: "vat", Year: 2025, Month: 6, Status: "paid",
		FiledDate: "2025-02-15", Operator: "李会计",
	}); err == nil {
		t.Fatal("改属期应当被拒绝")
	} else if !strings.Contains(err.Error(), "不能改成") {
		t.Errorf("要讲清属期不能改：%v", err)
	}
}
