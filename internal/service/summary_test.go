package service_test

import (
	"context"
	"testing"

	"miniaccount/internal/domain/period"
	"miniaccount/internal/service"
)

// ★ 这条测试走的是界面点「生成」时的同一条路径。
func TestSummaryServiceView(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedColumnar(t, svc) // 4 张凭证

	rep, err := svc.Summary(ctx, service.SummaryRequest{
		From: "2025-03-01", To: "2025-03-31",
	})
	if err != nil {
		t.Fatalf("生成凭证汇总表失败: %v", err)
	}
	if rep.VoucherCount != 4 {
		t.Errorf("凭证张数 = %d，期望 4", rep.VoucherCount)
	}
	if !rep.Balanced {
		t.Errorf("借贷不平：借 %s ≠ 贷 %s", rep.DebitTotal, rep.CreditTotal)
	}
	if rep.DebitTotal != rep.CreditTotal {
		t.Error("复式记账下借贷必然相等")
	}
	// 空列表必须是 []，不能是 null
	if rep.WordRows == nil || rep.DayRows == nil || rep.AccountRows == nil ||
		rep.Notes == nil {
		t.Error("空切片应是 [] 而不是 nil")
	}
	// 按科目段的净额与方向要算好，界面直接显示
	var bank *service.SummaryAccountView
	for i := range rep.AccountRows {
		if rep.AccountRows[i].AccountCode == "1002" {
			bank = &rep.AccountRows[i]
		}
	}
	if bank == nil {
		t.Fatal("按科目段应有 1002 银行存款")
	}
	if bank.Net != bank.Debit.Sub(bank.Credit) {
		t.Errorf("净额 = %s，期望 借%s − 贷%s",
			bank.Net, bank.Debit, bank.Credit)
	}
	if bank.Dir != "借" && bank.Dir != "贷" && bank.Dir != "平" {
		t.Errorf("方向 = %q，期望 借/贷/平", bank.Dir)
	}
}

// ★ 三个角度的合计必须完全相等 —— 这是这张表能拿去对账的资格。
func TestSummaryServiceThreeGroupingsTie(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedColumnar(t, svc)

	rep, err := svc.Summary(ctx, service.SummaryRequest{
		From: "2025-03-01", To: "2025-03-31",
	})
	if err != nil {
		t.Fatal(err)
	}
	var wd, dd, ad int64
	var wc, dc, ac int64
	var wCount, dCount int
	for _, w := range rep.WordRows {
		wd += int64(w.Debit)
		wc += int64(w.Credit)
		wCount += w.Count
	}
	for _, r := range rep.DayRows {
		dd += int64(r.Debit)
		dc += int64(r.Credit)
		dCount += r.Count
	}
	for _, a := range rep.AccountRows {
		ad += int64(a.Debit)
		ac += int64(a.Credit)
	}
	if wd != int64(rep.DebitTotal) || wc != int64(rep.CreditTotal) {
		t.Errorf("按凭证字 借%d/贷%d ≠ %s/%s", wd, wc, rep.DebitTotal, rep.CreditTotal)
	}
	if dd != int64(rep.DebitTotal) || dc != int64(rep.CreditTotal) {
		t.Errorf("按日期 借%d/贷%d ≠ %s/%s", dd, dc, rep.DebitTotal, rep.CreditTotal)
	}
	if ad != int64(rep.DebitTotal) || ac != int64(rep.CreditTotal) {
		t.Errorf("按科目 借%d/贷%d ≠ %s/%s", ad, ac, rep.DebitTotal, rep.CreditTotal)
	}
	if wCount != rep.VoucherCount || dCount != rep.VoucherCount {
		t.Errorf("张数：凭证字 %d、日期 %d、总数 %d",
			wCount, dCount, rep.VoucherCount)
	}
}

// ★ 与科目余额表交叉验证：两者同源于总账，本期发生额必须一致。
func TestSummaryTiesToTrialBalance(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedColumnar(t, svc)

	rep, err := svc.Summary(ctx, service.SummaryRequest{
		From: "2025-03-01", To: "2025-03-31",
	})
	if err != nil {
		t.Fatal(err)
	}
	tb, err := svc.DB().Reports().TrialBalanceReport(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatal(err)
	}
	_, _, pd, pc, _, _ := tb.Totals()
	if rep.DebitTotal != pd || rep.CreditTotal != pc {
		t.Errorf("凭证汇总表 借%s/贷%s ≠ 科目余额表 借%s/贷%s",
			rep.DebitTotal, rep.CreditTotal, pd, pc)
	}

	// 逐科目比对
	tbByCode := map[string][2]int64{}
	for _, row := range tb.Rows {
		if row.IsLeaf {
			tbByCode[row.AccountCode] = [2]int64{
				int64(row.PeriodDebit), int64(row.PeriodCredit)}
		}
	}
	for _, a := range rep.AccountRows {
		want, ok := tbByCode[a.AccountCode]
		if !ok {
			t.Errorf("科目 %s 不在科目余额表的明细科目里", a.AccountCode)
			continue
		}
		if int64(a.Debit) != want[0] || int64(a.Credit) != want[1] {
			t.Errorf("科目 %s：汇总表 借%s/贷%s，余额表 借%d/贷%d",
				a.AccountCode, a.Debit, a.Credit, want[0], want[1])
		}
	}
}

// 按会计期间生成（界面翻页用）。
func TestSummaryByPeriod(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedColumnar(t, svc)

	byMonth, err := svc.SummaryByPeriod(ctx, 2025, 3)
	if err != nil {
		t.Fatalf("按期间生成失败: %v", err)
	}
	byRange, err := svc.Summary(ctx, service.SummaryRequest{
		From: "2025-03-01", To: "2025-03-31",
	})
	if err != nil {
		t.Fatal(err)
	}
	if byMonth.VoucherCount != byRange.VoucherCount ||
		byMonth.DebitTotal != byRange.DebitTotal {
		t.Errorf("按期间 (%d 张/%s) 与按起止日 (%d 张/%s) 不一致",
			byMonth.VoucherCount, byMonth.DebitTotal,
			byRange.VoucherCount, byRange.DebitTotal)
	}
	// 非法期间要挡住
	if _, err := svc.SummaryByPeriod(ctx, 2025, 13); err == nil {
		t.Error("月份 13 应报错")
	}
	if _, err := svc.SummaryByPeriod(ctx, 0, 3); err == nil {
		t.Error("年份 0 应报错")
	}
}

// 空期间：表照样出，全为零并说明原因。
func TestSummaryServiceEmptyPeriod(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	rep, err := svc.Summary(ctx, service.SummaryRequest{
		From: "2025-01-01", To: "2025-01-31",
	})
	if err != nil {
		t.Fatalf("空期间应能出表: %v", err)
	}
	if rep.VoucherCount != 0 || rep.DebitTotal != 0 {
		t.Errorf("空期间应全为零，实际 %d 张 / %s", rep.VoucherCount, rep.DebitTotal)
	}
	if !rep.Balanced {
		t.Error("零 == 零，应判为平衡")
	}
	if len(rep.WordRows) != 0 {
		t.Errorf("空期间不该有凭证字分组，实际 %d 行", len(rep.WordRows))
	}
}
