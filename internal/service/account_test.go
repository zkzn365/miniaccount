package service_test

import (
	"context"

	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/money"
	"strings"
	"testing"

	"miniaccount/internal/service"
)

// deptID 建一个部门并返回它的 id。
//
// ★ 5602 管理费用要求「部门」辅助核算，而子科目**继承**父科目的要求 ——
// 这正是我们要的规则（少了就能绕过父科目的核算要求）。
// 所以往这些科目上记账时，凭证必须带上部门。
func deptID(t *testing.T, svc *service.Service) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := svc.SaveDepartment(ctx, service.Department{
		Name: "行政部", Enabled: true,
	})
	if err != nil {
		t.Fatalf("建部门失败: %v", err)
	}
	return id
}

// expenseLines 造一对「借 5602 系科目 / 贷 银行存款」的分录。
func expenseLines(code string, dept int64, amount money.Money) []service.VoucherLineInput {
	return []service.VoucherLineInput{
		{AccountCode: code, Summary: "测试", Debit: amount, DeptID: &dept},
		{AccountCode: "1002", Summary: "测试", Credit: amount},
	}
}

func findRow(t *testing.T, svc *service.Service, code string) *service.AccountRow {
	t.Helper()
	view, err := svc.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range view.Rows {
		if view.Rows[i].Code == code {
			return &view.Rows[i]
		}
	}
	return nil
}

// 新增一个明细科目：挂到预置科目下，编码接在父编码后面。
func TestCreateSubAccount(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()

	// ★ 编码刻意避开预置的那批：小企业会计准则的 5602 下面已经有
	// 560201~560217（工资、社保、办公费……），拿它们当「新科目」会撞唯一约束。
	// 用户加科目时也应当先看一眼编码表 —— 界面上会把已占用的编码挡在前面。
	leaf := true
	row, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "560290", Name: "研发费用（自建）", ParentCode: "5602", IsLeaf: &leaf,
	})
	if err != nil {
		t.Fatalf("新增科目失败: %v", err)
	}
	if row.Code != "560290" || row.Level != 2 || !row.IsLeaf {
		t.Errorf("新增结果不对：%+v", row)
	}
	if row.RootType != "expense" || row.BalanceDir != "debit" {
		t.Errorf("大类与方向应当随父科目：%+v", row)
	}
	if !strings.Contains(row.FullName, "研发费用（自建）") {
		t.Errorf("全名应当含本级名称：%q", row.FullName)
	}

	// 建完之后要能直接记账 —— 这正是用户加科目的目的
	dept := deptID(t, svc)
	if _, err := svc.SaveVoucher(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-10", Remark: "研发支出", CreatedBy: "李会计",
		Lines: expenseLines("560290", dept, 50000),
	}); err != nil {
		t.Fatalf("新科目应当可以直接记账：%v", err)
	}

	// 而且要在凭证录入的科目下拉里出现
	opts, err := svc.AccountOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range opts {
		if o.Code == "560290" {
			found = true
		}
	}
	if !found {
		t.Error("★ 新增的科目没有出现在可记账科目列表里")
	}
}

// ★ 子科目编码必须接在父编码后面，层级不能乱。
func TestCreateAccountValidatesCode(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()
	cases := []struct {
		name string
		in   service.AccountInput
		want string
	}{
		{"编码位数不对", service.AccountInput{Code: "56020", Name: "x", ParentCode: "5602"}, "位数字"},
		{"子科目没接在父后面", service.AccountInput{Code: "660301", Name: "x", ParentCode: "5602"}, "开头"},
		{"一级科目位数不对", service.AccountInput{Code: "66001", Name: "x"}, "位数字"},
		{"非数字", service.AccountInput{Code: "6602a1", Name: "x"}, "只能是数字"},
		{"缺名称", service.AccountInput{Code: "560202"}, "名称"},
	}
	for _, c := range cases {
		_, err := svc.SaveAccount(ctx, c.in)
		if err == nil {
			t.Errorf("%s：应当被拒绝", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：错误信息应含 %q，实际 %v", c.name, c.want, err)
		}
	}
}

// 汇总科目不能直接记账，明细科目才行 —— 新增时也要守住。
func TestCreateAccountRespectsLeafRule(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()

	group := false
	row, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "560299", Name: "一个汇总科目", ParentCode: "5602", IsLeaf: &group,
	})
	if err != nil {
		t.Fatalf("新增汇总科目失败: %v", err)
	}
	if row.IsLeaf {
		t.Error("应当是汇总科目")
	}
	if _, err := svc.SaveVoucher(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-10", Remark: "x", CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			{AccountCode: "560299", Summary: "x", Debit: 100},
			{AccountCode: "1002", Summary: "x", Credit: 100},
		},
	}); err == nil {
		t.Error("★ 汇总科目不该允许直接记账")
	}
}

// 修改：名称与备注可以改，改动要留痕（修改前后）。
func TestUpdateAccountKeepsHistory(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()
	leaf := true
	if _, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "560291", Name: "原名", ParentCode: "5602", IsLeaf: &leaf,
	}); err != nil {
		t.Fatal(err)
	}
	// AuxTypes 是**完整集合**：要保留的必须传回来（界面上的表单就是这样）
	row, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "560291", Name: "改名后", ParentCode: "5602", Remark: "备注",
		AuxTypes: []string{"dept"},
	})
	if err != nil {
		t.Fatalf("修改失败: %v", err)
	}
	if row.Name != "改名后" || row.Remark != "备注" {
		t.Errorf("修改没生效：%+v", row)
	}
}

// ★ 有分录的科目不能换上级、不能改明细属性。
func TestUpdateAccountRefusesStructuralChangeAfterUse(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()
	dept := deptID(t, svc)
	leaf := true
	if _, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "560292", Name: "某费用", ParentCode: "5602", IsLeaf: &leaf,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveVoucher(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-10", Remark: "x", CreatedBy: "李会计",
		Lines: expenseLines("560292", dept, 100),
	}); err != nil {
		t.Fatal(err)
	}

	group := false
	if _, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "560292", Name: "某费用", ParentCode: "5602", IsLeaf: &group,
	}); err == nil {
		t.Error("★ 已被分录引用过的科目不该允许改成汇总科目")
	}
	if _, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "560292", Name: "某费用", ParentCode: "6602",
	}); err == nil {
		t.Error("★ 已被分录引用过的科目不该允许换上级")
	}
}

// ★ 删除：只允许删从未用过的科目。
func TestDeleteAccountOnlyWhenUnused(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()
	dept := deptID(t, svc)

	// ① 没用过的可以删
	leaf := true
	if _, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "560293", Name: "临时科目", ParentCode: "5602", IsLeaf: &leaf,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteAccount(ctx, "560293"); err != nil {
		t.Fatalf("没用过的科目应当可以删：%v", err)
	}
	if findRow(t, svc, "560293") != nil {
		t.Error("删了还在")
	}

	// ② 预置科目不能删
	if err := svc.DeleteAccount(ctx, "5602"); err == nil {
		t.Error("★ 准则预置科目不该允许删除")
	} else if !strings.Contains(err.Error(), "预置") {
		t.Errorf("拒绝的理由要说清是预置科目：%v", err)
	}

	// ③ 有下级的不能删
	if _, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "560294", Name: "父", ParentCode: "5602", IsLeaf: &leaf,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "56029401", Name: "子", ParentCode: "560294",
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteAccount(ctx, "560294"); err == nil {
		t.Error("★ 还有下级的科目不该允许删除")
	}

	// ④ 有分录的不能删
	if _, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "560295", Name: "用过的", ParentCode: "5602", IsLeaf: &leaf,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveVoucher(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-10", Remark: "x", CreatedBy: "李会计",
		Lines: expenseLines("560295", dept, 100),
	}); err != nil {
		t.Fatal(err)
	}
	err := svc.DeleteAccount(ctx, "560295")
	if err == nil {
		t.Fatal("★ 已被分录引用的科目被删掉了 —— 历史凭证会对不上科目")
	}
	if !strings.Contains(err.Error(), "分录") {
		t.Errorf("拒绝的理由要说明被多少条分录用过：%v", err)
	}
	// 界面上也要能看出为什么不能删
	row := findRow(t, svc, "560295")
	if row == nil {
		t.Fatal("科目不见了")
	}
	if row.CanDelete {
		t.Error("★ 界面上的「可以删除」与后端判断不一致")
	}
	if row.EntryCount == 0 {
		t.Error("界面要显示被引用了多少条分录")
	}
	if !strings.Contains(row.Reason, "分录") {
		t.Errorf("界面要给出不能删的原因：%q", row.Reason)
	}
}

// ★ 停用：历史照常，但不能再用它记账。
func TestDisableAccount(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()
	dept := deptID(t, svc)
	leaf := true
	if _, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "560296", Name: "停用测试", ParentCode: "5602", IsLeaf: &leaf,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveVoucher(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-10", Remark: "x", CreatedBy: "李会计",
		Lines: expenseLines("560296", dept, 100),
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.SetAccountEnabled(ctx, "560296", false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	// 不能再记账
	if _, err := svc.SaveVoucher(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-11", Remark: "x", CreatedBy: "李会计",
		Lines: expenseLines("560296", dept, 100),
	}); err == nil {
		t.Error("★ 停用之后仍然能记账")
	}
	// 历史凭证照常读得到（科目还在）
	if _, err := svc.Vouchers(ctx, service.VoucherQuery{Year: 2025, Month: 1}); err != nil {
		t.Fatalf("停用不该影响历史凭证：%v", err)
	}
	// 停用的科目不该出现在可记账下拉里
	opts, _ := svc.AccountOptions(ctx)
	for _, o := range opts {
		if o.Code == "560296" {
			t.Error("停用的科目不该出现在可记账列表里")
		}
	}
}

// 停用汇总科目要连下级一起停 —— 否则下级还能记账，
// 而报表在汇总层看不到它。
func TestDisableGroupCascades(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()
	if err := svc.SetAccountEnabled(ctx, "5602", false); err != nil {
		t.Fatalf("停用汇总科目失败: %v", err)
	}
	opts, _ := svc.AccountOptions(ctx)
	for _, o := range opts {
		if strings.HasPrefix(o.Code, "5602") {
			t.Errorf("★ 汇总科目停用后，下级 %s 仍然可以记账", o.Code)
		}
	}
}

// 辅助核算：子科目不能比父科目少。
func TestSubAccountKeepsParentAux(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()
	parent := findRow(t, svc, "1122")
	if parent == nil || len(parent.AuxTypes) == 0 {
		t.Skip("1122 应收账款没有辅助核算要求，换一个科目测")
	}
	// 传空辅助核算：应当自动继承父科目的要求
	row, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "112299", Name: "应收—某类", ParentCode: "1122",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, need := range parent.AuxTypes {
		found := false
		for _, got := range row.AuxTypes {
			if got == need {
				found = true
			}
		}
		if !found {
			t.Errorf("★ 子科目少了父科目要求的辅助核算 %s", need)
		}
	}
}

// 大类与方向：方向由大类决定，不能单独指定。
func TestAccountDirectionFollowsRootType(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()
	// 用一个账套里肯定没有的编码
	if _, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "1499", Name: "某个资产科目", RootType: "asset", BalanceDir: "credit",
	}); err == nil {
		t.Error("★ 资产类被指定成贷方余额 —— 报表两边都会错")
	}
	row, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "1499", Name: "某个资产科目", RootType: "asset",
	})
	if err != nil {
		t.Fatalf("不指定方向时应当按大类推导：%v", err)
	}
	if row.BalanceDir != "debit" {
		t.Errorf("资产类余额方向 = %s，期望 debit", row.BalanceDir)
	}
}

// 新增科目要进操作日志（规范点名「会计科目表的维护」要留痕）。
func TestAccountChangesAreAudited(t *testing.T) {
	svc := withAudit(t)
	ctx := context.Background()
	leaf := true
	if _, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "560297", Name: "留痕测试", ParentCode: "5602", IsLeaf: &leaf,
	}); err != nil {
		t.Fatal(err)
	}
	got := findAudit(t, svc, audit.ActionAccountSave)
	if len(got) == 0 {
		t.Fatal("★ 新增科目没有进操作日志 —— 规范点名科目表维护要留痕")
	}
	last := got[0]
	if !strings.Contains(last.Summary, "560297") {
		t.Errorf("日志摘要要含科目编码：%q", last.Summary)
	}
	if last.Detail["编码"] != "560297" {
		t.Errorf("日志明细要有科目的属性：%+v", last.Detail)
	}
}

// ★ 被审计调整用过的科目不能删。
//
// 发布前审计实测：调整分录存的是科目编码（不是 id），而删科目的检查
// 只看 voucher_entry / ledger_entry / 下级，于是删掉之后审定表少一行、
// 借贷不平 —— 而底稿上看不出任何异常。
func TestDeleteAccountBlockedByAuditAdjustment(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")

	// 建一个只被审计调整用到的科目
	if _, err := svc.SaveAccount(ctx, service.AccountInput{
		Code: "560299", Name: "管理费用—审计专用", ParentCode: "5602",
		RootType: "expense", BalanceDir: "debit", AuxTypes: []string{"dept"},
	}); err != nil {
		t.Fatalf("新建科目失败: %v", err)
	}
	if _, err := svc.SaveAdjustment(ctx, service.AdjustmentInput{
		Year: 2025, Month: 3, Kind: "adjust",
		Summary: "通过新科目调整", Reason: "测试用",
		Lines: []service.AdjustLineInput{
			{AccountCode: "560299", Summary: "调整", Debit: money100(1_000), DeptID: &dept},
			{AccountCode: "1602", Summary: "调整", Credit: money100(1_000)},
		},
	}); err != nil {
		t.Fatalf("登记调整失败: %v", err)
	}

	if err := svc.DeleteAccount(ctx, "560299"); err == nil {
		t.Fatal("★ 被审计调整引用的科目不该能删 —— 删了审定表会缺一行、借贷不平")
	} else if !strings.Contains(err.Error(), "审计底稿") {
		t.Errorf("报错要说清被底稿引用着：%v", err)
	}

	// 科目管理页上也要能看出它不能删（按钮置灰 + 说明）
	tree, err := svc.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range tree.Rows {
		if row.Code != "560299" {
			continue
		}
		found = true
		if row.CanDelete {
			t.Error("★ 被底稿引用的科目在列表上不该显示为「可删除」")
		}
		if !strings.Contains(row.Reason, "审计底稿") {
			t.Errorf("要给出原因：%q", row.Reason)
		}
	}
	if !found {
		t.Fatal("科目列表里没有 560299")
	}
}
