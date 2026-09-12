package service_test

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 发票
// ---------------------------------------------------------------------------

func TestSaveInvoiceComputesTax(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	// 只给不含税金额与税率，税额与合计应由服务层算出。
	//
	// ★ 界面上只让用户填两个数字是刻意的：让人同时填价、税、合计，
	// 就是给他们机会填出「价 + 税 ≠ 合计」的自相矛盾数据。
	v, err := svc.SaveInvoice(ctx, service.InvoiceInput{
		Direction: "input", Kind: "special",
		Code: "3300201130", Number: "12345678", InvoiceDate: "2025-01-08",
		SellerName:  "宁波恒信办公用品有限公司",
		AmountExTax: money100(10000), TaxRatePPM: 130000, // 13%
	})
	if err != nil {
		t.Fatalf("保存发票失败: %v", err)
	}
	if v.TaxAmount != money100(1300) {
		t.Errorf("税额 = %s，期望 1300.00", v.TaxAmount)
	}
	if v.TotalAmount != money100(11300) {
		t.Errorf("价税合计 = %s，期望 11300.00", v.TotalAmount)
	}
	// 进项专票的税额可抵扣，费用只计不含税部分
	if v.DeductibleTax != money100(1300) {
		t.Errorf("可抵扣税额 = %s，期望 1300.00", v.DeductibleTax)
	}
	if v.CostAmount != money100(10000) {
		t.Errorf("计入费用 = %s，期望 10000.00", v.CostAmount)
	}
	if v.TaxRateLabel != "13%" {
		t.Errorf("税率标签 = %s", v.TaxRateLabel)
	}
	if v.Posted {
		t.Error("新保存的发票不该标记为已入账")
	}
}

// ★ 只有专票可抵扣
func TestInvoiceDeductibleOnlyForSpecial(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	for _, c := range []struct {
		kind string
		want money.Money
	}{
		{"special", money100(1300)},
		{"general", 0},
		{"e_general", 0},
	} {
		v, err := svc.SaveInvoice(ctx, service.InvoiceInput{
			Direction: "input", Kind: c.kind,
			Number: "N-" + c.kind, InvoiceDate: "2025-01-08",
			SellerName: "某供应商", AmountExTax: money100(10000), TaxRatePPM: 130000,
		})
		if err != nil {
			t.Fatalf("%s 保存失败: %v", c.kind, err)
		}
		if v.DeductibleTax != c.want {
			t.Errorf("%s 的可抵扣税额 = %s，期望 %s", c.kind, v.DeductibleTax, c.want)
		}
	}
}

// 给价税合计与税率时反算不含税与税额
func TestSaveInvoiceReverseFromTotal(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	v, err := svc.SaveInvoice(ctx, service.InvoiceInput{
		Direction: "input", Kind: "special",
		Number: "N-REV", InvoiceDate: "2025-01-08", SellerName: "某供应商",
		TotalAmount: money100(11300), TaxRatePPM: 130000,
	})
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if v.AmountExTax != money100(10000) {
		t.Errorf("反算的不含税金额 = %s，期望 10000.00", v.AmountExTax)
	}
	if v.TaxAmount != money100(1300) {
		t.Errorf("反算的税额 = %s，期望 1300.00", v.TaxAmount)
	}
}

func TestPostInvoiceCreatesVoucher(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	sup := mustContact(t, svc, "supplier", "宁波恒信办公用品")
	dept := mustDepartment(t, svc, "管理部门")

	// 进项票：借 费用 + 进项税 / 贷 应付账款
	v, err := svc.SaveInvoice(ctx, service.InvoiceInput{
		Direction: "input", Kind: "special",
		Number: "N-IN-1", InvoiceDate: "2025-01-08",
		SellerName: "宁波恒信办公用品", ContactID: &sup,
		AmountExTax: money100(10000), TaxRatePPM: 130000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostInvoice(ctx, v.ID, "", "", nil); err == nil {
		t.Fatal("缺少记账人应被拒绝")
	}

	posted, err := svc.PostInvoice(ctx, v.ID, "王主管", "", &dept)
	if err != nil {
		t.Fatalf("发票生成凭证失败: %v", err)
	}
	// ★ 发票现在生成的是**草稿**凭证：没有任何凭证号（草稿不占号），
	// 但发票自己确实已经挂上了那张凭证。过账发生在账期结算。
	if !posted.Posted || posted.VoucherLabel == "" {
		t.Errorf("应标记为已生成凭证: %+v", posted)
	}
	if posted.VoucherNo != "" {
		t.Errorf("★ 还没结算就不该有凭证号（草稿不占号），实际 %q", posted.VoucherNo)
	}

	// ★ 发票是单据、凭证是账：这张凭证明细要能对上
	vouchers, _ := svc.Vouchers(ctx, service.VoucherQuery{Year: 2025, Month: 1})
	if len(vouchers) != 1 {
		t.Fatalf("凭证数 = %d，期望 1", len(vouchers))
	}
	d, err := svc.Voucher(ctx, vouchers[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var debit, credit money.Money
	codes := map[string]money.Money{}
	for _, l := range d.Lines {
		debit, credit = debit.Add(l.Debit), credit.Add(l.Credit)
		codes[l.AccountCode] = codes[l.AccountCode].Add(l.Debit).Sub(l.Credit)
	}
	if debit != credit {
		t.Errorf("凭证借贷不平：%s vs %s", debit, credit)
	}
	// 借：管理费用（不含税）+ 进项税额
	if codes["22210101"] != money100(1300) {
		t.Errorf("进项税额 = %s，期望 1300.00", codes["22210101"])
	}
	// 贷：应付账款（价税合计）
	if codes["2202"] != money100(-11300) {
		t.Errorf("应付账款 = %s，期望 -11300.00", codes["2202"])
	}

	// 重复生成要被拒绝
	if _, err := svc.PostInvoice(ctx, v.ID, "王主管", "", &dept); err == nil {
		t.Fatal("同一张发票不该重复生成凭证")
	}
}

func TestPostOutputInvoice(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	cust := mustContact(t, svc, "customer", "杭州云帆科技")
	dept := mustDepartment(t, svc, "管理部门")

	v, err := svc.SaveInvoice(ctx, service.InvoiceInput{
		Direction: "output", Kind: "special",
		Number: "N-OUT-1", InvoiceDate: "2025-01-15",
		BuyerName: "杭州云帆科技", ContactID: &cust,
		AmountExTax: money100(50000), TaxRatePPM: 60000, // 6%
	})
	if err != nil {
		t.Fatal(err)
	}
	posted, err := svc.PostInvoice(ctx, v.ID, "王主管", "", &dept)
	if err != nil {
		t.Fatalf("销项票生成凭证失败: %v", err)
	}
	if !posted.Posted {
		t.Error("应标记为已入账")
	}

	d, _ := svc.Voucher(ctx, mustVoucherID(t, svc, 2025, 1))
	codes := map[string]money.Money{}
	for _, l := range d.Lines {
		codes[l.AccountCode] = codes[l.AccountCode].Add(l.Debit).Sub(l.Credit)
	}
	// 借：应收账款（价税合计）
	if codes["1122"] != money100(53000) {
		t.Errorf("应收账款 = %s，期望 53000.00", codes["1122"])
	}
	// 贷：主营业务收入（不含税）
	if codes["5001"] != money100(-50000) {
		t.Errorf("主营业务收入 = %s，期望 -50000.00", codes["5001"])
	}
	// 贷：销项税额
	if codes["22210102"] != money100(-3000) {
		t.Errorf("销项税额 = %s，期望 -3000.00", codes["22210102"])
	}
}

func TestInvoiceListAndSummary(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	for i, n := range []string{"A1", "A2"} {
		if _, err := svc.SaveInvoice(ctx, service.InvoiceInput{
			Direction: "input", Kind: "special",
			Number: n, InvoiceDate: "2025-01-0" + string(rune('1'+i)),
			SellerName: "供应商甲", AmountExTax: money100(1000), TaxRatePPM: 130000,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 一张销项票
	if _, err := svc.SaveInvoice(ctx, service.InvoiceInput{
		Direction: "output", Kind: "general",
		Number: "B1", InvoiceDate: "2025-01-05",
		BuyerName: "客户乙", AmountExTax: money100(2000), TaxRatePPM: 60000,
	}); err != nil {
		t.Fatal(err)
	}

	inputs, err := svc.Invoices(ctx, service.InvoiceQuery{Direction: "input"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 2 {
		t.Errorf("进项票 = %d 张，期望 2", len(inputs))
	}
	outputs, _ := svc.Invoices(ctx, service.InvoiceQuery{Direction: "output"})
	if len(outputs) != 1 {
		t.Errorf("销项票 = %d 张，期望 1", len(outputs))
	}

	s, err := svc.InvoiceSummary(ctx, "input", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Count != 2 || s.AmountExTax != money100(2000) {
		t.Errorf("进项汇总 = %+v", s)
	}
	// ★ 「尚未入账」比金额更有用：它是「账做完没有」的直接指标
	if s.UnpostedCount != 2 {
		t.Errorf("未入账张数 = %d，期望 2", s.UnpostedCount)
	}
}

// ---------------------------------------------------------------------------
// 差旅报销
// ---------------------------------------------------------------------------

func claimInput(employeeID int64, deptID int64) service.ClaimInput {
	return service.ClaimInput{
		ClaimantEmployeeID: employeeID, DeptID: &deptID,
		ApplyDate: "2025-01-20", TripStart: "2025-01-15", TripEnd: "2025-01-18",
		Destination: "上海", Reason: "客户拜访", PayFromAccount: "1002",
		Items: []service.ClaimItemInput{
			{Category: "transport", OccurDate: "2025-01-15",
				Summary: "高铁票 杭州—上海", Amount: money100(553), TaxAmount: money100(16)},
			{Category: "accommodation", OccurDate: "2025-01-16",
				Summary: "住宿 2 晚", Amount: money100(800), TaxAmount: money100(45)},
			{Category: "meal", OccurDate: "2025-01-17",
				Summary: "餐费", Amount: money100(300)},
		},
	}
}

func TestClaimLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	emp := saveTestEmployee(t, svc)
	dept := mustDepartment(t, svc, "管理部门")

	c, err := svc.SaveClaim(ctx, claimInput(emp, dept))
	if err != nil {
		t.Fatalf("保存报销单失败: %v", err)
	}
	if c.Code == "" {
		t.Error("应自动生成报销单号")
	}
	if c.Status != "draft" {
		t.Errorf("新保存的应是草稿，实际 %s", c.Status)
	}
	// 合计 = 553 + 800 + 300
	if c.TotalAmount != money100(1653) {
		t.Errorf("报销合计 = %s，期望 1653.00", c.TotalAmount)
	}
	if c.TotalTax != money100(61) {
		t.Errorf("可抵扣税额合计 = %s，期望 61.00", c.TotalTax)
	}
	if len(c.Items) != 3 {
		t.Fatalf("明细数 = %d", len(c.Items))
	}
	// ★ 类别自动带出默认科目 —— 科目是最容易填错也最难发现的一栏
	for _, it := range c.Items {
		if it.AccountCode == "" {
			t.Errorf("%s 应自动带出默认科目", it.CategoryLabel)
		}
	}
	// 草稿可以审批，不能记账
	if !c.CanApprove || c.CanPost {
		t.Errorf("草稿应可审批、不可记账: %+v", c)
	}
	if c.BlockedReason == "" {
		t.Error("不可记账时要说明原因")
	}

	// 驳回必须给理由
	if _, err := svc.RejectClaim(ctx, c.ID, "  "); err == nil {
		t.Error("空驳回理由应被拒绝")
	}

	// 审批需要审批人
	if _, err := svc.ApproveClaim(ctx, c.ID, 0); err == nil {
		t.Error("缺少审批人应被拒绝")
	}
	approved, err := svc.ApproveClaim(ctx, c.ID, emp)
	if err != nil {
		t.Fatalf("审批失败: %v", err)
	}
	if approved.Status != "approved" {
		t.Errorf("状态 = %s，期望 approved", approved.Status)
	}
	if approved.ApproverName == "" {
		t.Error("应显示审批人姓名")
	}
	if !approved.CanPost {
		t.Error("审批通过后应可记账")
	}

	// 记账
	if _, err := svc.PostClaim(ctx, c.ID, ""); err == nil {
		t.Error("缺少记账人应被拒绝")
	}
	posted, err := svc.PostClaim(ctx, c.ID, "王主管")
	if err != nil {
		t.Fatalf("报销单记账失败: %v", err)
	}
	// ★ 同上：生成的是草稿凭证，没有号；过账在账期结算
	if posted.VoucherLabel == "" {
		t.Error("应有凭证标识（草稿也要能认出来是哪一张）")
	}
	if posted.VoucherNo != "" {
		t.Errorf("★ 还没结算就不该有凭证号，实际 %q", posted.VoucherNo)
	}

	// 试算必须平衡
	rep, _ := svc.DB().Reports().TrialBalanceReport(ctx, period.NewKey(2025, 1))
	_, _, d, cr, _, _ := rep.Totals()
	if d != cr {
		t.Errorf("报销记账后试算不平衡：借 %s ≠ 贷 %s", d, cr)
	}
}

func TestClaimValidation(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	emp := saveTestEmployee(t, svc)
	dept := mustDepartment(t, svc, "管理部门")

	// 没有报销人
	in := claimInput(0, dept)
	if _, err := svc.SaveClaim(ctx, in); err == nil {
		t.Error("缺少报销人应被拒绝")
	}
	// 没有明细
	in = claimInput(emp, dept)
	in.Items = nil
	if _, err := svc.SaveClaim(ctx, in); err == nil {
		t.Error("没有明细应被拒绝")
	}
	// 日期格式
	in = claimInput(emp, dept)
	in.ApplyDate = "2025/01/20"
	if _, err := svc.SaveClaim(ctx, in); err == nil {
		t.Error("非法日期格式应被拒绝")
	}
	// 明细缺发生日期
	in = claimInput(emp, dept)
	in.Items[0].OccurDate = ""
	if _, err := svc.SaveClaim(ctx, in); err == nil {
		t.Error("明细缺发生日期应被拒绝")
	}
	// 明细日期格式
	in = claimInput(emp, dept)
	in.Items[0].OccurDate = "01-15"
	if _, err := svc.SaveClaim(ctx, in); err == nil {
		t.Error("明细日期格式错误应被拒绝")
	}
}

func TestClaimCategories(t *testing.T) {
	svc := newSvc(t, 3)

	cats := svc.ClaimCategories()
	if len(cats) == 0 {
		t.Fatal("应返回费用类别")
	}
	for _, c := range cats {
		if c.Value == "" || c.Label == "" {
			t.Errorf("类别不完整: %+v", c)
		}
		if c.Account == "" {
			t.Errorf("类别 %s 应有默认科目", c.Value)
		}
	}
}

// ---------------------------------------------------------------------------
// 往来单位档案
// ---------------------------------------------------------------------------

func TestSaveContact(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	id, err := svc.SaveContact(ctx, service.ContactInput{
		Kind: "customer", Name: "杭州云帆科技有限公司",
		ShortName: "云帆科技", TaxNo: "91330100MA2EXAMPLE",
		BankName: "工商银行杭州分行", BankAccount: "1202020000000001",
		ContactPerson: "王经理", Phone: "0571-88880000",
		Address: "杭州市西湖区", Enabled: true,
	})
	if err != nil {
		t.Fatalf("保存往来单位失败: %v", err)
	}
	if id == 0 {
		t.Fatal("应返回 id")
	}

	opts, err := svc.ContactOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 1 {
		t.Fatalf("往来单位数 = %d", len(opts))
	}
	if opts[0].Name != "杭州云帆科技有限公司" || opts[0].KindLabel != "客户" {
		t.Errorf("读回不一致: %+v", opts[0])
	}
	if opts[0].ShortName != "云帆科技" {
		t.Errorf("简称 = %q", opts[0].ShortName)
	}

	// 修改
	if _, err := svc.SaveContact(ctx, service.ContactInput{
		ID: id, Kind: "supplier", Name: "杭州云帆科技有限公司", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	opts, _ = svc.ContactOptions(ctx)
	if opts[0].KindLabel != "供应商" {
		t.Errorf("修改后类型 = %s", opts[0].KindLabel)
	}

	if _, err := svc.SaveContact(ctx, service.ContactInput{Name: "  "}); err == nil {
		t.Error("空名称应被拒绝")
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// saveTestEmployee 建一个员工并返回 id。
func saveTestEmployee(t *testing.T, svc *service.Service) int64 {
	t.Helper()
	dept := mustDepartment(t, svc, "管理部门")
	id, err := svc.SaveEmployee(context.Background(), service.EmployeeInput{
		Code: "E001", Name: "张三", BaseSalary: money100(10000),
		Enabled: true, DeptID: &dept,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// mustVoucherID 取某期间的第一张凭证 id。
func mustVoucherID(t *testing.T, svc *service.Service, year, month int) int64 {
	t.Helper()
	list, err := svc.Vouchers(context.Background(),
		service.VoucherQuery{Year: year, Month: month})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("该期间没有凭证")
	}
	return list[0].ID
}

var _ = strings.TrimSpace
