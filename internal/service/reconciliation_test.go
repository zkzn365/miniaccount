package service_test

import (
	"context"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/service"
)

// importReconStatement 导入一份带余额列的对账单，供余额调节表用。
func importReconStatement(t *testing.T, svc *service.Service) []int64 {
	t.Helper()
	csv := "\ufeff交易日期,摘要,对方户名,收入金额,支出金额,余额\n" +
		"2025-03-03,转账,张三,50000.00,,150000.00\n" +
		"2025-03-10,办公用品,杭州某某办公用品有限公司,,3000.00,147000.00\n"
	if _, err := svc.ImportBankStatement(context.Background(), service.BankImportInput{
		AccountCode: "1002", FileName: "s.csv",
		Data: []byte(csv), ImportedBy: "李会计",
	}); err != nil {
		t.Fatal(err)
	}
	flows, err := svc.BankFlows(context.Background(), service.BankFlowQuery{})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, len(flows))
	for _, f := range flows {
		ids = append(ids, f.ID)
	}
	return ids
}

// seedReconOpening 录一笔与银行对账单隐含期初一致的账面期初。
//
// 两边期初必须相等，否则调节表怎么算都差那个数 ——
// 这是余额调节表最容易踩的坑，所以做成显式的辅助函数。
func seedReconOpening(t *testing.T, svc *service.Service) {
	t.Helper()
	shareholder := mustContact(t, svc, "shareholder", "张三")
	mustPost(t, svc, "2025-02-28", "期初余额",
		newVoucherLine("1002", "期初余额", money100(100000), 0),
		service.VoucherLineInput{AccountCode: "3001", Summary: "期初余额",
			Credit: money100(100000), ContactID: &shareholder})
}

// ★ 这条测试走的是界面点「生成」时的同一条路径：
// Service.Reconciliation → 仓储 → 领域。
// 界面拿到的 Status / StatusLabel / OpeningDiff 必须与领域一致，
// 否则界面会照着一个错误的结论给用户亮绿勾。
func TestReconciliationServiceView(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedReconOpening(t, svc)

	ids := importReconStatement(t, svc)
	if len(ids) != 2 {
		t.Fatalf("流水条数 = %d", len(ids))
	}

	// 还没生成凭证：两条流水都该是未达账项
	rep, err := svc.Reconciliation(ctx, service.ReconciliationRequest{
		AccountCode: "1002", AsOf: "2025-03-31",
	})
	if err != nil {
		t.Fatalf("生成调节表失败: %v", err)
	}
	if rep.AccountCode != "1002" {
		t.Errorf("科目 = %q", rep.AccountCode)
	}
	if rep.From != "2025-03-03" {
		t.Errorf("对账起始日 = %q，期望 2025-03-03（对账单首日）", rep.From)
	}
	// 银行期初由首笔流水 150,000 − 50,000 反推
	if rep.BankOpening == nil || *rep.BankOpening != money100(100000) {
		t.Errorf("银行期初 = %v，期望 100000", rep.BankOpening)
	}
	if rep.BookOpening != money100(100000) {
		t.Errorf("账面期初 = %s，期望 100000", rep.BookOpening)
	}
	if rep.OpeningDiff != 0 {
		t.Errorf("期初差额 = %s，期望 0", rep.OpeningDiff)
	}
	if len(rep.BankReceivedNotBooked) != 1 {
		t.Fatalf("银行已收企业未收 = %d 笔，期望 1", len(rep.BankReceivedNotBooked))
	}
	if got := rep.BankReceivedNotBooked[0].Amount; got != money100(50000) {
		t.Errorf("金额 = %s，期望 50000", got)
	}
	if len(rep.BankPaidNotBooked) != 1 {
		t.Fatalf("银行已付企业未付 = %d 笔，期望 1", len(rep.BankPaidNotBooked))
	}
	// 空列表必须是 []，不能是 null —— 界面 v-for 到 null 会直接报错
	if rep.BookReceivedNotBanked == nil || rep.BookPaidNotBanked == nil {
		t.Error("空的未达账项列表应是空数组而不是 nil")
	}
	// 账面 100,000 + 50,000 − 3,000 = 147,000 = 银行 147,000
	if !rep.Balanced || rep.Status != "balanced" {
		t.Errorf("应账实相符，状态 = %q，差 %s", rep.Status, rep.Difference)
	}
	if rep.BookAdjusted != money100(147000) {
		t.Errorf("调节后账面 = %s，期望 147000", rep.BookAdjusted)
	}
	if rep.BankAdjusted == nil || *rep.BankAdjusted != money100(147000) {
		t.Errorf("调节后银行 = %v，期望 147000", rep.BankAdjusted)
	}
	if rep.UnreconciledFlows != 2 {
		t.Errorf("未处理流水 = %d，期望 2", rep.UnreconciledFlows)
	}
}

// 手工传入银行余额时以它为准 —— 拿不到电子流水、
// 只能照纸质对账单敲数的场景。
func TestReconciliationManualBankBalanceWins(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	seedReconOpening(t, svc)
	importReconStatement(t, svc)

	// 全部流水都没生成凭证时，两侧调节后的差额只来自期初（此处为 0），
	// 所以敲错 1 元就该正好差 1 元 —— 这让断言能真正验证「差额算对了」。
	wrong := int64(money100(146999))
	rep, err := svc.Reconciliation(ctx, service.ReconciliationRequest{
		AccountCode: "1002", AsOf: "2025-03-31", BankBalance: &wrong,
	})
	if err != nil {
		t.Fatalf("生成调节表失败: %v", err)
	}
	if rep.BankBalance == nil || *rep.BankBalance != money.Money(wrong) {
		t.Fatalf("银行余额 = %v，期望使用手工传入的 %d", rep.BankBalance, wrong)
	}
	if rep.Status != "unbalanced" {
		t.Errorf("状态 = %q，期望 unbalanced", rep.Status)
	}
	if rep.StatusLabel == "" {
		t.Error("状态中文名不能为空，界面直接拿它显示")
	}
	if rep.Difference.Abs() != money100(1) {
		t.Errorf("差额 = %s，期望 1.00", rep.Difference.Abs())
	}
}

// 没有对账单时状态必须是 unknown —— 界面对它既不能亮绿勾
// 也不能亮红叉，两者的结论都比「不知道」更糟。
func TestReconciliationUnknownWithoutStatement(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	rep, err := svc.Reconciliation(ctx, service.ReconciliationRequest{
		AccountCode: "1002", AsOf: "2025-03-31",
	})
	if err != nil {
		t.Fatalf("生成调节表失败: %v", err)
	}
	if rep.Status != "unknown" {
		t.Errorf("状态 = %q，期望 unknown", rep.Status)
	}
	if rep.BankAdjusted != nil {
		t.Errorf("没有对账单时不该给出调节后银行余额，实际 %v", rep.BankAdjusted)
	}
}

// 起始日晚于截止日必须报错，而不是给一张空洞的表。
func TestReconciliationRejectsBadWindow(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	if _, err := svc.Reconciliation(ctx, service.ReconciliationRequest{
		AccountCode: "1002", From: "2025-04-01", AsOf: "2025-03-31",
	}); err == nil {
		t.Error("起始日晚于截止日应报错")
	}
	if _, err := svc.Reconciliation(ctx, service.ReconciliationRequest{
		AccountCode: "1002", AsOf: "2025-03-31", From: "2025/03/01",
	}); err == nil {
		t.Error("日期格式错误应报错")
	}
}

// BankAccounts 给界面填下拉框用，必须至少有一个银行科目，
// 且不能把 1002 的上级（非明细）算进来。
func TestBankAccountsForPicker(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	list, err := svc.BankAccounts(ctx)
	if err != nil {
		t.Fatalf("取银行科目失败: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("应至少返回一个银行科目")
	}
	for _, a := range list {
		if a.Code != "1002" {
			t.Errorf("科目 %s 不该出现在银行科目列表里", a.Code)
		}
		if a.Name == "" || a.FullName == "" {
			t.Errorf("科目 %s 的名称不全: %q / %q", a.Code, a.Name, a.FullName)
		}
	}
}
