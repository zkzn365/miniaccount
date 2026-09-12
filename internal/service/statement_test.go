package service_test

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/service"
)

// seedReceivable 造一段应收账款往来，返回客户 id。
func seedReceivable(t *testing.T, svc *service.Service) int64 {
	t.Helper()
	cust := mustContact(t, svc, "customer", "杭州某某科技有限公司")
	// 期初（2 月）：销售 8 万
	mustPost(t, svc, "2025-02-10", "2 月货款",
		service.VoucherLineInput{AccountCode: "1122", Summary: "2 月货款",
			Debit: money100(80000), ContactID: &cust},
		newVoucherLine("5001", "2 月货款", 0, money100(80000)))
	// 3 月：又销售 12 万
	mustPost(t, svc, "2025-03-12", "3 月货款",
		service.VoucherLineInput{AccountCode: "1122", Summary: "3 月货款",
			Debit: money100(120000), ContactID: &cust},
		newVoucherLine("5001", "3 月货款", 0, money100(120000)))
	// 3 月：收回 6 万
	mustPost(t, svc, "2025-03-25", "收回货款",
		newVoucherLine("1002", "收回货款", money100(60000), 0),
		service.VoucherLineInput{AccountCode: "1122", Summary: "收回货款",
			Credit: money100(60000), ContactID: &cust})
	return cust
}

func TestStatementFromVouchers(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	cust := seedReceivable(t, svc)

	st, err := svc.Statement(ctx, service.StatementRequest{
		ContactID: cust, From: "2025-03-01", To: "2025-03-31",
	})
	if err != nil {
		t.Fatalf("生成对账单失败: %v", err)
	}
	if st.ContactName != "杭州某某科技有限公司" {
		t.Errorf("往来单位 = %q", st.ContactName)
	}
	if st.ContactKindLabel != "客户" {
		t.Errorf("类型 = %q", st.ContactKindLabel)
	}
	if st.CompanyName != "服务层测试公司" {
		t.Errorf("我方名称 = %q", st.CompanyName)
	}
	if st.AccountCode != "1122" || st.AccountName != "应收账款" {
		t.Errorf("科目 = %s %s", st.AccountCode, st.AccountName)
	}

	// ★ 期初必须来自 2 月的 8 万，而不是 0
	if st.Opening != money100(80000) || st.OpeningDir != "借" {
		t.Fatalf("期初 = %s %s，期望 借 80000.00", st.OpeningDir, st.Opening)
	}
	// 本期只有 3 月两笔
	if len(st.Lines) != 2 {
		t.Fatalf("行数 = %d，期望 2（2 月那笔不该出现在本期）", len(st.Lines))
	}
	if st.TotalDebit != money100(120000) || st.TotalCredit != money100(60000) {
		t.Errorf("本期合计 = 借 %s 贷 %s", st.TotalDebit, st.TotalCredit)
	}
	// 8 万 + 12 万 − 6 万 = 14 万
	if st.Closing != money100(140000) || st.ClosingDir != "借" {
		t.Errorf("期末 = %s %s，期望 借 140000.00", st.ClosingDir, st.Closing)
	}
	if st.ClosingUpper == "" {
		t.Error("正式对账单要有大写金额")
	}
	// 逐行累计
	if st.Lines[0].Balance != money100(200000) {
		t.Errorf("第 1 行余额 = %s，期望 200000.00", st.Lines[0].Balance)
	}
	if st.Lines[1].Balance != money100(140000) {
		t.Errorf("第 2 行余额 = %s，期望 140000.00", st.Lines[1].Balance)
	}
	// 纯文本版式要能直接用
	for _, want := range []string{"往来对账单", "期初余额", "期末余额", "盖章"} {
		if !strings.Contains(st.Text, want) {
			t.Errorf("文本版式缺少 %q", want)
		}
	}
}

// ★ 与总账交叉验证：期末余额必须等于总账上该往来单位的余额。
//
// 对账单是发给对方盖章的。自己算错、对方照着签了字，
// 这张纸反而成了「双方都认的错误」—— 比没有对账单更糟。
func TestStatementClosingMatchesLedger(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	cust := seedReceivable(t, svc)

	st, err := svc.Statement(ctx, service.StatementRequest{
		ContactID: cust, From: "2025-03-01", To: "2025-03-31",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 总账上该往来单位截至 3-31 的净额
	var net int64
	if err := svc.DB().SQL().QueryRowContext(ctx, `
		SELECT COALESCE(SUM(le.debit), 0) - COALESCE(SUM(le.credit), 0)
		  FROM ledger_entry le
		 WHERE le.contact_id = ? AND le.biz_date <= '2025-03-31'`, cust).Scan(&net); err != nil {
		t.Fatal(err)
	}
	if st.Closing != money.Money(net) {
		t.Errorf("★ 对账单期末 %s ≠ 总账往来余额 %s", st.Closing, money.Money(net))
	}
	// 期初 + 本期借 − 本期贷 = 期末
	if st.Opening.Add(st.TotalDebit).Sub(st.TotalCredit) != st.Closing {
		t.Errorf("期初 %s + 借 %s − 贷 %s ≠ 期末 %s",
			st.Opening, st.TotalDebit, st.TotalCredit, st.Closing)
	}
}

// 期间切换：期初要跟着走
func TestStatementOpeningFollowsPeriod(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	cust := seedReceivable(t, svc)

	// 8 月看：期初应包含 2 月与 3 月的全部往来（8 万 + 12 万 − 6 万 = 14 万）
	st, err := svc.Statement(ctx, service.StatementRequest{
		ContactID: cust, From: "2025-08-01", To: "2025-08-31",
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Opening != money100(140000) {
		t.Errorf("8 月期初 = %s，期望 140000.00", st.Opening)
	}
	if len(st.Lines) != 0 {
		t.Errorf("8 月没有往来，行数应为 0，实际 %d", len(st.Lines))
	}
	if st.Closing != st.Opening {
		t.Errorf("无发生额时期末应等于期初")
	}
	// 空期间也要能出表 —— 客户可能就是要一张「这个月我们没业务」的确认
	if !strings.Contains(st.Summary, "已结清") && st.Closing.IsZero() {
		t.Errorf("概览 = %q", st.Summary)
	}
}

// 付清之后方向要变成「平」
func TestStatementSettled(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	cust := mustContact(t, svc, "customer", "客户甲")

	mustPost(t, svc, "2025-01-10", "货款",
		service.VoucherLineInput{AccountCode: "1122", Summary: "货款",
			Debit: money100(50000), ContactID: &cust},
		newVoucherLine("5001", "货款", 0, money100(50000)))
	mustPost(t, svc, "2025-01-20", "收款",
		newVoucherLine("1002", "收款", money100(50000), 0),
		service.VoucherLineInput{AccountCode: "1122", Summary: "收款",
			Credit: money100(50000), ContactID: &cust})

	st, err := svc.Statement(ctx, service.StatementRequest{
		ContactID: cust, From: "2025-01-01", To: "2025-01-31",
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.ClosingDir != "平" {
		t.Errorf("已结清时方向应为「平」，实际 %s", st.ClosingDir)
	}
	if !strings.Contains(st.Summary, "已结清") {
		t.Errorf("概览 = %q", st.Summary)
	}
}

// 期间缺省：默认当前可记账期间
func TestStatementDefaultPeriod(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	cust := seedReceivable(t, svc)

	st, err := svc.Statement(ctx, service.StatementRequest{ContactID: cust})
	if err != nil {
		t.Fatal(err)
	}
	// 账套当前期间是 2025-01
	if st.From != "2025-01-01" || st.To != "2025-01-31" {
		t.Errorf("默认期间 = %s ~ %s，期望 2025-01-01 ~ 2025-01-31", st.From, st.To)
	}
}

func TestStatementValidation(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	cust := seedReceivable(t, svc)

	if _, err := svc.Statement(ctx, service.StatementRequest{}); err == nil {
		t.Error("缺少往来单位应被拒绝")
	}
	if _, err := svc.Statement(ctx, service.StatementRequest{
		ContactID: cust, From: "2025/03/01",
	}); err == nil {
		t.Error("非法日期格式应被拒绝")
	}
	if _, err := svc.Statement(ctx, service.StatementRequest{
		ContactID: cust, From: "2025-03-31", To: "2025-03-01",
	}); err == nil {
		t.Error("结束早于开始应被拒绝")
	}
	if _, err := svc.Statement(ctx, service.StatementRequest{
		ContactID: 999999,
	}); err == nil {
		t.Error("不存在的往来单位应报错")
	}
}

// 批量开对账单时要能挑出本期有往来的单位
func TestActiveContacts(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	cust := seedReceivable(t, svc)
	// 一个没往来的单位
	mustContact(t, svc, "supplier", "从未往来的供应商")

	list, err := svc.ActiveContacts(ctx, "2025-03-01", "2025-03-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("本期有往来的单位 = %d，期望 1", len(list))
	}
	if list[0].ID != cust || list[0].KindLabel != "客户" {
		t.Errorf("单位信息有误: %+v", list[0])
	}
}

// 多个科目时不能混淆：应收与预收要各算各的余额
func TestStatementAccountPrefix(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	cust := mustContact(t, svc, "customer", "客户甲")

	mustPost(t, svc, "2025-01-10", "货款",
		service.VoucherLineInput{AccountCode: "1122", Summary: "货款",
			Debit: money100(50000), ContactID: &cust},
		newVoucherLine("5001", "货款", 0, money100(50000)))
	mustPost(t, svc, "2025-01-15", "预收款",
		newVoucherLine("1002", "预收款", money100(20000), 0),
		service.VoucherLineInput{AccountCode: "2203", Summary: "预收款",
			Credit: money100(20000), ContactID: &cust})

	// 只看应收
	ar, err := svc.Statement(ctx, service.StatementRequest{
		ContactID: cust, From: "2025-01-01", To: "2025-01-31",
		AccountPrefix: "1122",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ar.Closing != money100(50000) {
		t.Errorf("应收期末 = %s，期望 50000.00", ar.Closing)
	}
	if ar.Mixed {
		t.Error("限定科目后不该标记为多科目")
	}

	// 全部科目：两笔都在，且标明多科目
	all, err := svc.Statement(ctx, service.StatementRequest{
		ContactID: cust, From: "2025-01-01", To: "2025-01-31",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !all.Mixed {
		t.Error("涉及两个科目时应标记为多科目")
	}
	if len(all.Lines) != 2 {
		t.Errorf("行数 = %d，期望 2", len(all.Lines))
	}
	// 5 万 − 2 万 = 3 万
	if all.Closing != money100(30000) {
		t.Errorf("合并期末 = %s，期望 30000.00", all.Closing)
	}
}
