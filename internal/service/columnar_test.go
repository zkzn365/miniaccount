package service_test

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/service"
)

// seedColumnar 记一批管理费用与增值税业务。
func seedColumnar(t *testing.T, svc *service.Service) {
	t.Helper()
	dept := mustDepartment(t, svc, "管理部门")

	// 3-05 办公用品 300
	mustPost(t, svc, "2025-03-05", "办公用品",
		service.VoucherLineInput{AccountCode: "560206", Summary: "办公用品",
			Debit: money100(300), DeptID: &dept},
		newVoucherLine("1002", "办公用品", 0, money100(300)))
	// 3-12 出差机票 2400
	mustPost(t, svc, "2025-03-12", "出差机票",
		service.VoucherLineInput{AccountCode: "560207", Summary: "出差机票",
			Debit: money100(2400), DeptID: &dept},
		newVoucherLine("1002", "出差机票", 0, money100(2400)))
	// 3-20 采购：借 进项税额 1,300 / 贷 银行存款
	mustPost(t, svc, "2025-03-20", "采购",
		newVoucherLine("22210101", "进项税额", money100(1300), 0),
		newVoucherLine("1002", "采购", 0, money100(1300)))
	// 3-25 销售：借 银行存款 / 贷 销项税额 2,600
	mustPost(t, svc, "2025-03-25", "销售",
		newVoucherLine("1002", "销售", money100(2600), 0),
		newVoucherLine("22210102", "销项税额", 0, money100(2600)))
}

// ★ 这条测试走的是界面点「生成」时的同一条路径。
func TestColumnarServiceView(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedColumnar(t, svc)

	rep, err := svc.Columnar(ctx, service.ColumnarRequest{
		AccountCode: "5602", From: "2025-03-01", To: "2025-03-31",
	})
	if err != nil {
		t.Fatalf("生成多栏式明细账失败: %v", err)
	}
	if rep.AccountCode != "5602" || rep.AccountName != "管理费用" {
		t.Errorf("科目 = %s %s", rep.AccountCode, rep.AccountName)
	}
	// 17 个下级 → 17 栏
	if len(rep.Columns) != 17 {
		t.Fatalf("栏目数 = %d，期望 17", len(rep.Columns))
	}
	// 每行都要有与栏目等长的稠密金额数组 —— 界面直接 v-for 出单元格
	for i, r := range rep.Rows {
		if len(r.Amounts) != len(rep.Columns) {
			t.Fatalf("第 %d 行的金额数组长度 = %d，期望 %d（与栏目等长）",
				i+1, len(r.Amounts), len(rep.Columns))
		}
		var filled int
		for _, a := range r.Amounts {
			if a != 0 {
				filled++
			}
		}
		if filled != 1 {
			t.Errorf("第 %d 行有 %d 个非零单元格，多栏式每行只填一栏", i+1, filled)
		}
	}
	if rep.DebitTotal != money100(300+2400) {
		t.Errorf("借方发生额 = %s，期望 2700.00", rep.DebitTotal)
	}
	if rep.Closing != money100(2700) || rep.ClosingDir != "借" {
		t.Errorf("期末 = %s（%s），期望 2700.00 借", rep.Closing, rep.ClosingDir)
	}
	// 空列表必须是 []，不能是 null —— 界面 v-for 到 null 会直接报错
	if rep.CreditColumns == nil || rep.Rows == nil || rep.Notes == nil ||
		rep.ColumnTotals == nil || rep.DebitColumns == nil {
		t.Error("空切片应是 [] 而不是 nil")
	}
}

// ★ 管理费用是单侧多栏：只有借方栏，CreditColumns 必须为空。
func TestColumnarManagementIsDebitOnly(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedColumnar(t, svc)

	rep, err := svc.Columnar(ctx, service.ColumnarRequest{
		AccountCode: "5602", From: "2025-03-01", To: "2025-03-31",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.DebitColumns) != 17 {
		t.Errorf("借方栏 = %d 个，期望 17", len(rep.DebitColumns))
	}
	if len(rep.CreditColumns) != 0 {
		t.Errorf("贷方栏 = %d 个，管理费用是借方多栏，不该有贷方栏",
			len(rep.CreditColumns))
	}
	for _, c := range rep.Columns {
		if c.Side != "debit" || c.SideLabel != "借" {
			t.Errorf("栏目 %s 的方向 = %s/%s，期望 debit/借", c.Label, c.Side, c.SideLabel)
		}
	}
}

// ★ 应交增值税是双侧多栏：借方 6 栏 + 贷方 4 栏，
// 正是财会〔2016〕22号 规定的 10 个专栏。
func TestColumnarVATIsTwoSided(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedColumnar(t, svc)

	rep, err := svc.Columnar(ctx, service.ColumnarRequest{
		AccountCode: "222101", From: "2025-03-01", To: "2025-03-31",
	})
	if err != nil {
		t.Fatalf("生成应交增值税多栏式明细账失败: %v", err)
	}
	if len(rep.Columns) != 10 {
		t.Fatalf("栏目数 = %d，期望 10", len(rep.Columns))
	}
	if len(rep.DebitColumns) != 6 {
		t.Errorf("借方栏 = %d 个，期望 6", len(rep.DebitColumns))
	}
	if len(rep.CreditColumns) != 4 {
		t.Errorf("贷方栏 = %d 个，期望 4", len(rep.CreditColumns))
	}
	// 借方 1,300 进项；贷方 2,600 销项 → 应交未交 1,300（贷方余额）
	if rep.DebitTotal != money100(1300) {
		t.Errorf("借方发生额 = %s，期望 1300.00", rep.DebitTotal)
	}
	if rep.CreditTotal != money100(2600) {
		t.Errorf("贷方发生额 = %s，期望 2600.00", rep.CreditTotal)
	}
	if rep.Closing != money100(-1300) || rep.ClosingDir != "贷" {
		t.Errorf("期末 = %s（%s），期望 -1300.00 贷（应交未交）",
			rep.Closing, rep.ClosingDir)
	}
	// 借方栏的下标必须都指向借方栏目
	for _, i := range rep.DebitColumns {
		if rep.Columns[i].Side != "debit" {
			t.Errorf("下标 %d 落在 DebitColumns 里，但它是 %s 栏", i, rep.Columns[i].Side)
		}
	}
	for _, i := range rep.CreditColumns {
		if rep.Columns[i].Side != "credit" {
			t.Errorf("下标 %d 落在 CreditColumns 里，但它是 %s 栏", i, rep.Columns[i].Side)
		}
	}
}

// ★ 红字冲销：冲销后该栏目变成负数，且冲销行仍在表里。
func TestColumnarReversalViaService(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedColumnar(t, svc)
	dept := int64(1)

	before, err := svc.Columnar(ctx, service.ColumnarRequest{
		AccountCode: "5602", From: "2025-03-01", To: "2025-03-31",
	})
	if err != nil {
		t.Fatal(err)
	}
	officeBefore := colTotal(t, before, "560206")

	// 冲销办公费那 300 元：红字凭证（借贷互换）
	mustPost(t, svc, "2025-03-28", "冲销误记的办公费",
		service.VoucherLineInput{AccountCode: "560206", Summary: "冲销误记的办公费",
			Credit: money100(300), DeptID: &dept},
		newVoucherLine("1002", "冲销误记的办公费", money100(300), 0))

	after, err := svc.Columnar(ctx, service.ColumnarRequest{
		AccountCode: "5602", From: "2025-03-01", To: "2025-03-31",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := officeBefore.Sub(money100(300)); colTotal(t, after, "560206") != want {
		t.Errorf("冲销后办公费 = %s，期望 %s",
			colTotal(t, after, "560206"), want)
	}
	// 冲销行要在，且落在办公费栏
	var found bool
	for _, r := range after.Rows {
		if r.Summary != "冲销误记的办公费" {
			continue
		}
		found = true
		idx := -1
		for j, a := range r.Amounts {
			if a != 0 {
				idx = j
			}
		}
		if idx < 0 || after.Columns[idx].Key != "560206" {
			t.Errorf("冲销行没落在办公费栏")
		}
		if r.Amounts[idx] != money100(-300) {
			t.Errorf("冲销行金额 = %s，期望 -300.00（红字）", r.Amounts[idx])
		}
	}
	if !found {
		t.Error("冲销行不该从多栏式明细账里消失")
	}
	// 借贷发生额是总额：贷方多出 300
	if after.CreditTotal != before.CreditTotal.Add(money100(300)) {
		t.Errorf("贷方发生额 = %s，期望 %s",
			after.CreditTotal, before.CreditTotal.Add(money100(300)))
	}
}

// 没有下级科目的科目做不出多栏式，报错要指向正确工具。
func TestColumnarServiceRejectsLeaf(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	_, err := svc.Columnar(ctx, service.ColumnarRequest{
		AccountCode: "1002", From: "2025-03-01", To: "2025-03-31",
	})
	if err == nil {
		t.Fatal("没有下级科目应报错")
	}
	if !strings.Contains(err.Error(), "明细账") {
		t.Errorf("报错应提示改用明细账，实际 %q", err.Error())
	}
}

// 空科目要拦住。
func TestColumnarServiceRequiresAccount(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	if _, err := svc.Columnar(ctx, service.ColumnarRequest{
		From: "2025-03-01", To: "2025-03-31",
	}); err == nil {
		t.Error("未指定科目应报错")
	}
}

// 候选科目只列有下级的科目。
func TestColumnarAccountsForPicker(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	list, err := svc.ColumnarAccounts(ctx)
	if err != nil {
		t.Fatalf("取候选科目失败: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("应至少返回一个候选科目")
	}
	byCode := map[string]service.AccountOption{}
	for _, a := range list {
		byCode[a.Code] = a
		if a.Name == "" || a.FullName == "" || a.Direction == "" || a.SearchText == "" {
			t.Errorf("科目 %s 的展示字段不完整: %+v", a.Code, a)
		}
	}
	for _, want := range []string{"5602", "222101"} {
		if _, ok := byCode[want]; !ok {
			t.Errorf("候选里应包含 %s", want)
		}
	}
	for _, no := range []string{"1002", "1122"} {
		if _, ok := byCode[no]; ok {
			t.Errorf("候选里不该出现没有下级的 %s", no)
		}
	}
}

// ★ 与科目余额表交叉验证：多栏式是「横向搬了金额」的表，
// 合计必须与总账一字不差。
func TestColumnarTiesToLedger(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedColumnar(t, svc)

	rep, err := svc.Columnar(ctx, service.ColumnarRequest{
		AccountCode: "5602", From: "2025-03-01", To: "2025-03-31",
	})
	if err != nil {
		t.Fatal(err)
	}

	tb, err := svc.DB().Reports().TrialBalanceReport(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatal(err)
	}
	var wantDebit, wantCredit money.Money
	for _, row := range tb.Rows {
		if row.AccountCode == "5602" {
			wantDebit, wantCredit = row.PeriodDebit, row.PeriodCredit
		}
	}
	if rep.DebitTotal != wantDebit || rep.CreditTotal != wantCredit {
		t.Errorf("多栏式 借%s/贷%s ≠ 科目余额表 借%s/贷%s",
			rep.DebitTotal, rep.CreditTotal, wantDebit, wantCredit)
	}
	// 栏目合计之和也必须等于发生额
	var colSum money.Money
	for i, c := range rep.Columns {
		if c.Side == "debit" {
			colSum = colSum.Add(rep.ColumnTotals[i])
		}
	}
	if colSum != rep.DebitTotal {
		t.Errorf("借方栏目合计 %s ≠ 借方发生额 %s", colSum, rep.DebitTotal)
	}
}

func colTotal(t *testing.T, v *service.ColumnarView, key string) money.Money {
	t.Helper()
	for i, c := range v.Columns {
		if c.Key == key {
			return v.ColumnTotals[i]
		}
	}
	t.Fatalf("栏目 %s 不存在", key)
	return 0
}
