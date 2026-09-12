package service_test

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/service"
)

// 往来单位档案（辅助核算的「客户 / 供应商」这一维）。
//
// # 这一组测试守的是什么
//
// 辅助核算要成立，四个维度都得有地方**管**：客户、供应商、部门、员工。
// 部门与员工原来也没有档案页（后端有方法、没绑定），往来单位更差 ——
// 只有一个「只列启用中的」下拉，于是新建不了、停用之后再也看不到、
// 也就改不回来。这里守的是补上的那一半：能建、能停用、能删，
// 而**删除必须被引用挡住**（contact_id 是真外键，删错了就是一句
// SQLite 报错甩给用户）。

// TestContactListIncludesDisabled 停用之后档案必须**仍然看得见**。
//
// 这正是旧下拉的根本问题：停用即消失，用户连把它改回来都做不到。
func TestContactListIncludesDisabled(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	id, err := svc.SaveContact(ctx, service.ContactInput{
		Kind: "customer", Name: "停用客户", Enabled: true,
	})
	if err != nil {
		t.Fatalf("新增往来单位失败: %v", err)
	}
	if _, err := svc.SaveContact(ctx, service.ContactInput{
		ID: id, Kind: "customer", Name: "停用客户", Enabled: false,
	}); err != nil {
		t.Fatalf("停用失败: %v", err)
	}

	all, err := svc.Contacts(ctx, "")
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	var got *service.Contact
	for i := range all {
		if all[i].ID == id {
			got = &all[i]
		}
	}
	if got == nil {
		t.Fatal("停用之后档案整个消失了 —— 用户再也看不到它、也就改不回来")
	}
	if got.Enabled {
		t.Error("停用没生效")
	}
	if got.KindLabel != "客户" {
		t.Errorf("类型中文名 = %q，期望「客户」", got.KindLabel)
	}

	// 按类型筛选：客户档案不该出现在供应商列表里
	sups, err := svc.Contacts(ctx, "supplier")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range sups {
		if c.ID == id {
			t.Error("客户出现在了供应商列表里")
		}
	}
}

// TestContactUsageIgnoresEmployeeIDCollision 是那个「报销单」假引用的回归测试。
//
// expense_claim 表**没有** contact_id —— 往来单位与报销单之间不存在引用关系。
// 但统计里一度照着部门那份抄了一条
// `SELECT COUNT(*) FROM expense_claim WHERE claimant_employee_id = ?`：
// 把「报销人的员工 id」当成了「往来单位 id」。
//
// 两张表的 id 各自从 1 开始，所以只要有一个员工号恰好等于某个客户 id
// （这是**常态**，不是巧合），那个客户就会被报成「还在被使用：报销单 2 处」，
// 删也删不掉，而用户怎么找都找不到那两张所谓的报销单。
func TestContactUsageIgnoresEmployeeIDCollision(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	// 先建员工和报销单：它们会占用 employee/expense_claim 的 id 1、2……
	emp := saveTestEmployee(t, svc)
	dept := mustDepartment(t, svc, "管理部门")
	if _, err := svc.SaveClaim(ctx, claimInput(emp, dept)); err != nil {
		t.Fatalf("保存报销单失败: %v", err)
	}

	// 再建往来单位：contact 表的 id 也是从 1 开始，于是两者撞号
	cust, err := svc.SaveContact(ctx, service.ContactInput{
		Kind: "customer", Name: "只撞号没往来", Enabled: true,
	})
	if err != nil {
		t.Fatalf("新增往来单位失败: %v", err)
	}
	if cust != emp {
		t.Skipf("contact id=%d 与 employee id=%d 没撞上，这条测试失去意义", cust, emp)
	}

	u, err := svc.ContactUsageOf(ctx, cust)
	if err != nil {
		t.Fatalf("统计引用失败: %v", err)
	}
	if u.Total() != 0 {
		t.Fatalf("「只撞号没往来」被报成 %d 处引用（%s）—— "+
			"报销单表里根本没有 contact_id，这是把员工号当成了往来单位号",
			u.Total(), u.Describe())
	}

	// 没被引用就必须删得掉
	if err := svc.DeleteContact(ctx, cust); err != nil {
		t.Fatalf("没被引用的往来单位应当能删掉，实际：%v", err)
	}
	list, err := svc.Contacts(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.ID == cust {
			t.Fatal("删完还在列表里")
		}
	}
}

// TestContactUsageCountsDraftVouchers 草稿凭证也算引用。
//
// ★ 数的是 voucher_entry（凭证分录）而不是 ledger_entry（总账）：
// 总账只收已记账的凭证，草稿不进去 —— 但草稿一样把 contact_id 落在
// voucher_entry 上，而 contact 是**真外键**。只数总账的话，删除会在
// SQL 层被外键挡回来，用户看到的是一句 SQLite 报错，
// 而不是「这个客户还有 1 张凭证，请改用停用」。
func TestContactUsageCountsDraftVouchers(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	cust := mustContact(t, svc, "customer", "草稿凭证客户")

	// 只存草稿，不过账
	if _, err := svc.SaveVoucher(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-10", Remark: "草稿", CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			{AccountCode: "1122", Summary: "赊销", Debit: money100(1000), ContactID: &cust},
			newVoucherLine("5001", "赊销", 0, money100(1000)),
		},
	}); err != nil {
		t.Fatalf("存草稿失败: %v", err)
	}

	u, err := svc.ContactUsageOf(ctx, cust)
	if err != nil {
		t.Fatalf("统计引用失败: %v", err)
	}
	if u.Entries < 1 {
		t.Fatalf("草稿凭证没算进引用：%s —— 删除会被外键挡回一句 SQLite 报错",
			u.Describe())
	}

	err = svc.DeleteContact(ctx, cust)
	if err == nil {
		t.Fatal("还有凭证引用着，不该删得掉")
	}
	msg := err.Error()
	if strings.Contains(msg, "FOREIGN KEY") || strings.Contains(msg, "constraint failed") {
		t.Fatalf("报的是 SQLite 的裸错误，用户看不懂：%v", err)
	}
	if !strings.Contains(msg, "凭证") {
		t.Errorf("要说清楚被什么挡住了：%v", err)
	}
	if !strings.Contains(msg, "停用") {
		t.Errorf("要给出替代方案（改用「停用」）：%v", err)
	}
}

// TestDeleteContactRefusesWhenInvoiceUsesIt 发票引用也算，同样要给出人话。
func TestDeleteContactRefusesWhenInvoiceUsesIt(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	sup := mustContact(t, svc, "supplier", "开了票的供应商")

	if _, err := svc.SaveInvoice(ctx, service.InvoiceInput{
		Direction: "input", Kind: "special", Number: "N-001",
		InvoiceDate: "2025-01-05", SellerName: "开了票的供应商",
		ContactID: &sup, AmountExTax: money100(1130), TaxRatePPM: 130000,
	}); err != nil {
		t.Fatalf("存发票失败: %v", err)
	}

	u, err := svc.ContactUsageOf(ctx, sup)
	if err != nil {
		t.Fatal(err)
	}
	if u.Invoices < 1 {
		t.Fatalf("发票没算进引用：%s", u.Describe())
	}
	if err := svc.DeleteContact(ctx, sup); err == nil {
		t.Fatal("还有发票引用着，不该删得掉")
	}
}

// TestDeleteContactUnknown 删一个不存在的 id 要给可识别的说法，而不是静默成功。
func TestDeleteContactUnknown(t *testing.T) {
	svc := newSvc(t, 3)
	err := svc.DeleteContact(context.Background(), 987654)
	if err == nil {
		t.Fatal("删不存在的往来单位应当报错")
	}
	if !strings.Contains(err.Error(), "不存在") {
		t.Errorf("错误信息要说清楚原因：%v", err)
	}
}

// TestSaveContactRejectsEmptyName 名称是唯一必填项。
func TestSaveContactRejectsEmptyName(t *testing.T) {
	svc := newSvc(t, 3)
	if _, err := svc.SaveContact(context.Background(), service.ContactInput{
		Kind: "customer", Name: "   ",
	}); err == nil {
		t.Fatal("名称为空应当被拒绝")
	}
}
