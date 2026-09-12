package service_test

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/service"
)

// ★ 账龄不是「发生额有多大」，而是「还没收回来的钱有多久了」。
// 这条链要走通：凭证 → 带往来辅助核算的总账 → 先进先出核销 → 分桶。
func TestAgingReportFromVouchers(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	cust := mustContact(t, svc, "customer", "客户甲")

	// 1 月销售 10 万（挂应收）
	mustPost(t, svc, "2025-01-10", "1 月货款",
		service.VoucherLineInput{AccountCode: "1122", Summary: "1 月货款",
			Debit: money100(100000), ContactID: &cust},
		newVoucherLine("5001", "1 月货款", 0, money100(100000)))
	// 5 月再销售 5 万
	mustPost(t, svc, "2025-05-10", "5 月货款",
		service.VoucherLineInput{AccountCode: "1122", Summary: "5 月货款",
			Debit: money100(50000), ContactID: &cust},
		newVoucherLine("5001", "5 月货款", 0, money100(50000)))
	// 6 月收回 10 万 —— 应冲掉 1 月那笔
	mustPost(t, svc, "2025-06-10", "收回货款",
		newVoucherLine("1002", "收回货款", money100(100000), 0),
		service.VoucherLineInput{AccountCode: "1122", Summary: "收回货款",
			Credit: money100(100000), ContactID: &cust})

	rep, err := svc.AgingReport(ctx, service.AgingRequest{
		AsOf: "2025-06-30", AccountPrefix: "1122",
	})
	if err != nil {
		t.Fatalf("生成账龄表失败: %v", err)
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("行数 = %d，期望 1", len(rep.Rows))
	}
	row := rep.Rows[0]
	if row.ContactName != "客户甲" {
		t.Errorf("往来单位 = %q", row.ContactName)
	}
	if row.AccountName != "应收账款" {
		t.Errorf("科目名 = %q", row.AccountName)
	}
	// ★ 1 月那笔已被 6 月的收款冲掉，只剩 5 月的 5 万
	if row.Balance != money100(50000) {
		t.Errorf("未结清余额 = %s，期望 50000.00", row.Balance)
	}
	if len(row.Items) != 1 || row.Items[0].Amount != money100(50000) {
		t.Fatalf("未结清明细 = %+v，期望只剩 50000.00", row.Items)
	}
	if row.Items[0].Days != 51 {
		t.Errorf("账龄天数 = %d，期望 51", row.Items[0].Days)
	}
	if row.MaxDays != 51 {
		t.Errorf("最老账龄 = %d", row.MaxDays)
	}
	// 90 天以上应为 0 —— 这正是核销的意义
	if rep.Over90 != 0 {
		t.Errorf("90 天以上 = %s，期望 0", rep.Over90)
	}
	// 概览要能读
	if !strings.Contains(rep.Summary, "2025-06-30") {
		t.Errorf("概览 = %q", rep.Summary)
	}
}

// 没收回来的账要落进最老的桶并标红
func TestAgingOver90(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 12)
	cust := mustContact(t, svc, "customer", "老赖客户")

	mustPost(t, svc, "2025-01-05", "陈年货款",
		service.VoucherLineInput{AccountCode: "1122", Summary: "陈年货款",
			Debit: money100(80000), ContactID: &cust},
		newVoucherLine("5001", "陈年货款", 0, money100(80000)))

	rep, err := svc.AgingReport(ctx, service.AgingRequest{
		AsOf: "2025-12-31", AccountPrefix: "1122",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Over90 != money100(80000) {
		t.Errorf("90 天以上 = %s，期望 80000.00", rep.Over90)
	}
	// 2025-01-05 → 2025-12-31 是 360 天，落在「181—365 天」这一桶
	// （不满一年，所以不该进最后一个桶）
	b := rep.Buckets[4]
	if b.Amount != money100(80000) {
		t.Errorf("第 5 个桶（%s）= %s，期望 80000.00", b.Label, b.Amount)
	}
	if rep.Buckets[5].Amount != 0 {
		t.Errorf("「1 年以上」应为 0（只有 360 天），实际 %s", rep.Buckets[5].Amount)
	}
	// 比例合计应为 100%
	var sum float64
	for _, b := range rep.Buckets {
		sum += b.Percent
	}
	if sum < 99.5 || sum > 100.5 {
		t.Errorf("各桶占比合计 = %.1f%%，期望约 100%%", sum)
	}
}

// 应付账款的账龄方向要反过来
func TestAgingPayable(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 12)
	sup := mustContact(t, svc, "supplier", "供应商乙")

	dept := mustDepartment(t, svc, "管理部门")
	// 3 月采购 5 万（挂应付）
	mustPost(t, svc, "2025-03-10", "采购",
		service.VoucherLineInput{AccountCode: "560206", Summary: "采购",
			Debit: money100(50000), DeptID: &dept},
		service.VoucherLineInput{AccountCode: "2202", Summary: "采购",
			Credit: money100(50000), ContactID: &sup})
	// 6 月付了 2 万
	mustPost(t, svc, "2025-06-10", "付货款",
		service.VoucherLineInput{AccountCode: "2202", Summary: "付货款",
			Debit: money100(20000), ContactID: &sup},
		newVoucherLine("1002", "付货款", 0, money100(20000)))

	rep, err := svc.AgingReport(ctx, service.AgingRequest{
		AsOf: "2025-06-30", AccountPrefix: "2202",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("行数 = %d", len(rep.Rows))
	}
	row := rep.Rows[0]
	// 应付未结清 3 万
	if row.Balance != money100(-30000) {
		t.Errorf("余额 = %s，期望 -30000.00（贷方）", row.Balance)
	}
	if len(row.Items) != 1 || row.Items[0].Amount != money100(30000) {
		t.Fatalf("未结清明细 = %+v，期望 30000.00", row.Items)
	}
	// ★ 账龄从**贷方发生**的 3 月算起，不是从付款的 6 月
	if row.Items[0].Days != 112 {
		t.Errorf("账龄天数 = %d，期望 112（3 月 10 日到 6 月 30 日）", row.Items[0].Days)
	}
	// 应付合计应记在贷方
	if rep.CreditTotal != money100(30000) {
		t.Errorf("应付未结清 = %s，期望 30000.00", rep.CreditTotal)
	}
}

// 截止日期默认为当前可记账期间的期末
func TestAgingDefaultDate(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	rep, err := svc.AgingReport(ctx, service.AgingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// 账套当前期间是 2025-01
	if rep.AsOf != "2025-01-31" {
		t.Errorf("默认截止日期 = %s，期望 2025-01-31", rep.AsOf)
	}
	// 空账套也要有 6 个桶的结构，界面靠它画表头
	if len(rep.Buckets) != 6 {
		t.Errorf("区间数 = %d，期望 6", len(rep.Buckets))
	}
}

func TestAgingBadDate(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	if _, err := svc.AgingReport(ctx, service.AgingRequest{AsOf: "2025/06/30"}); err == nil {
		t.Error("非法日期格式应被拒绝")
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// mustPost 过账一张凭证。
func mustPost(t *testing.T, svc *service.Service, date, remark string,
	lines ...service.VoucherLineInput) {
	t.Helper()
	if _, err := svc.SaveAndPost(context.Background(), service.VoucherInput{
		Word: "记", Date: date, Remark: remark, CreatedBy: "李会计", Lines: lines,
	}, "王主管"); err != nil {
		t.Fatalf("过账 %s 失败: %v", date, err)
	}
}

// ★ 交叉验证：账龄的未结清合计，必须等于科目余额表上该科目的余额。
//
// 这是账龄表可用与否的**唯一硬标准**。核销逻辑写错（比如冲抵方向反了、
// 或者把不同往来单位互相冲了），单看账龄表本身是看不出问题的 ——
// 数字都「长得像那么回事」。只有拿它跟总账对，错了才会露出来。
func TestAgingReconcilesWithTrialBalance(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 12)
	dept := mustDepartment(t, svc, "管理部门")
	custA := mustContact(t, svc, "customer", "客户甲")
	custB := mustContact(t, svc, "customer", "客户乙")
	sup := mustContact(t, svc, "supplier", "供应商丙")

	// 造一批交错业务：两家客户、一家供应商，有欠有还、有早有晚
	mustPost(t, svc, "2025-01-10", "甲货款",
		service.VoucherLineInput{AccountCode: "1122", Summary: "甲货款",
			Debit: money100(100000), ContactID: &custA},
		newVoucherLine("5001", "甲货款", 0, money100(100000)))
	mustPost(t, svc, "2025-02-15", "乙货款",
		service.VoucherLineInput{AccountCode: "1122", Summary: "乙货款",
			Debit: money100(30000), ContactID: &custB},
		newVoucherLine("5001", "乙货款", 0, money100(30000)))
	mustPost(t, svc, "2025-03-20", "甲还款",
		newVoucherLine("1002", "甲还款", money100(60000), 0),
		service.VoucherLineInput{AccountCode: "1122", Summary: "甲还款",
			Credit: money100(60000), ContactID: &custA})
	mustPost(t, svc, "2025-04-05", "采购",
		service.VoucherLineInput{AccountCode: "560206", Summary: "采购",
			Debit: money100(20000), DeptID: &dept},
		service.VoucherLineInput{AccountCode: "2202", Summary: "采购",
			Credit: money100(20000), ContactID: &sup})
	mustPost(t, svc, "2025-05-10", "付供应商",
		service.VoucherLineInput{AccountCode: "2202", Summary: "付供应商",
			Debit: money100(5000), ContactID: &sup},
		newVoucherLine("1002", "付供应商", 0, money100(5000)))

	asOf := "2025-12-31"

	// 应收：未结清合计应等于 1122 的借方余额
	ar, err := svc.AgingReport(ctx, service.AgingRequest{
		AsOf: asOf, AccountPrefix: "1122",
	})
	if err != nil {
		t.Fatal(err)
	}
	arLedger := ledgerBalance(t, svc, "1122", asOf)
	if ar.Total != arLedger {
		t.Errorf("★ 应收账龄合计 %s ≠ 总账 1122 余额 %s（差 %s）",
			ar.Total, arLedger, ar.Total.Sub(arLedger))
	}
	// 甲欠 4 万、乙欠 3 万，合 7 万
	if ar.Total != money100(70000) {
		t.Errorf("应收未结清 = %s，期望 70000.00（甲 4 万 + 乙 3 万）", ar.Total)
	}
	// 两家客户各自独立
	if len(ar.Rows) != 2 {
		t.Fatalf("行数 = %d，期望 2", len(ar.Rows))
	}

	// 应付：同样要对上
	ap, err := svc.AgingReport(ctx, service.AgingRequest{
		AsOf: asOf, AccountPrefix: "2202",
	})
	if err != nil {
		t.Fatal(err)
	}
	apLedger := ledgerBalance(t, svc, "2202", asOf)
	// 应付余额在总账上是贷方（负数），账龄表上表示为正的 CreditTotal
	if ap.CreditTotal.Sub(ap.DebitTotal) != apLedger.Abs() {
		t.Errorf("★ 应付账龄净额 %s ≠ 总账 2202 余额 %s",
			ap.CreditTotal.Sub(ap.DebitTotal), apLedger.Abs())
	}
	if ap.CreditTotal != money100(15000) {
		t.Errorf("应付未结清 = %s，期望 15000.00（采购 2 万 − 已付 5 千）",
			ap.CreditTotal)
	}

	// 不分科目时，两边都要出现在同一张表里
	all, err := svc.AgingReport(ctx, service.AgingRequest{AsOf: asOf})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Rows) != 3 {
		t.Errorf("全部往来行数 = %d，期望 3（甲/乙/丙）", len(all.Rows))
	}
	if all.DebitTotal != money100(70000) {
		t.Errorf("应收合计 = %s", all.DebitTotal)
	}
	if all.CreditTotal != money100(15000) {
		t.Errorf("应付合计 = %s", all.CreditTotal)
	}
	// 各桶金额之和必须等于未结清总额 —— 分桶不能丢钱
	var bucketSum money.Money
	for _, b := range all.Buckets {
		bucketSum = bucketSum.Add(b.Amount)
	}
	if bucketSum != all.DebitTotal.Add(all.CreditTotal) {
		t.Errorf("★ 各桶合计 %s ≠ 未结清总额 %s",
			bucketSum, all.DebitTotal.Add(all.CreditTotal))
	}
}

// ledgerBalance 取某科目前缀截至某日的总账余额（借−贷）。
func ledgerBalance(t *testing.T, svc *service.Service, prefix, asOf string) money.Money {
	t.Helper()
	var net int64
	err := svc.DB().SQL().QueryRowContext(context.Background(), `
		SELECT COALESCE(SUM(le.debit), 0) - COALESCE(SUM(le.credit), 0)
		  FROM ledger_entry le JOIN account a ON a.id = le.account_id
		 WHERE a.code LIKE ? AND le.biz_date <= ?`, prefix+"%", asOf).Scan(&net)
	if err != nil {
		t.Fatalf("查询总账余额失败: %v", err)
	}
	return money.Money(net)
}
