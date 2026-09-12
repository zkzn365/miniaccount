package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/attachment"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/expense"
	"miniaccount/internal/domain/invoice"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/payroll"
	"miniaccount/internal/domain/period"
)

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

// ieFixture 建账 + 一名员工 + 一个附件仓库。
type ieFixture struct {
	DB       *DB
	Files    *attachment.Store
	Dir      string
	Employee int64
	Approver int64
}

func setupIE(t *testing.T) *ieFixture {
	t.Helper()
	ctx := context.Background()
	db := newTestDB(t)
	dir := t.TempDir()

	files, err := attachment.New(attachment.Options{
		Dir: filepath.Join(dir, "测试公司.files"),
	})
	if err != nil {
		t.Fatalf("创建附件仓库失败: %v", err)
	}

	dept := int64(1)
	empID, err := db.Payroll().UpsertEmployee(ctx, &payroll.Employee{
		Code: "E001", Name: "张三", Kind: payroll.KindEmployee,
		DeptID: &dept, ExpenseAccountCode: "560207",
		HireDate: calendar.MustParse("2020-01-01"), IsEnabled: true,
	})
	if err != nil {
		t.Fatalf("新增员工失败: %v", err)
	}
	apprID, err := db.Payroll().UpsertEmployee(ctx, &payroll.Employee{
		Code: "E002", Name: "李经理", Kind: payroll.KindEmployee,
		ExpenseAccountCode: "560213",
		HireDate:           calendar.MustParse("2020-01-01"), IsEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &ieFixture{DB: db, Files: files, Dir: dir, Employee: empID, Approver: apprID}
}

// putAttachment 把一段内容存进附件仓库并返回 hash。
func (f *ieFixture) putAttachment(t *testing.T, name, content string) string {
	t.Helper()
	// 伪造 PDF 魔数，让 MIME 嗅探识别为 PDF
	data := append([]byte("%PDF-1.4\n"), []byte(content)...)
	blob, err := f.Files.Put(data, name)
	if err != nil {
		t.Fatalf("保存附件失败: %v", err)
	}
	return blob.SHA256
}

func fakePDF(content string) []byte {
	return append([]byte("%PDF-1.4\n"), []byte(content)...)
}

// ---------------------------------------------------------------------------
// 发票档案
// ---------------------------------------------------------------------------

func TestCreateInvoice(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)

	inv := &invoice.Invoice{
		Direction: invoice.DirInput, Kind: invoice.KindSpecial,
		Code: "033002100111", Number: "12345678",
		InvoiceDate: calendar.MustParse("2025-09-10"),
		SellerName:  "杭州某某办公用品有限公司",
		SellerTaxNo: "91330100MA2XXXXXXX",
		BuyerName:   "测试公司", BuyerTaxNo: "91330100MA2YYYYYYY",
		AmountExTax: money100(1000), TaxRate: money.RatePercent(13),
		Category: "办公用品", Status: invoice.StatusPending,
	}
	inv.ComputeTax()

	id, err := f.DB.Invoices().CreateInvoice(ctx, inv)
	if err != nil {
		t.Fatalf("登记发票失败: %v", err)
	}
	if id == 0 {
		t.Fatal("应返回 id")
	}

	got, err := f.DB.Invoices().GetInvoice(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.AmountExTax != money100(1000) || got.TaxAmount != money100(130) ||
		got.TotalAmount != money100(1130) {
		t.Errorf("金额 = %s / %s / %s", got.AmountExTax, got.TaxAmount, got.TotalAmount)
	}
	if got.TaxRate != money.RatePercent(13) {
		t.Errorf("税率 = %s", got.TaxRate)
	}
	// 专票可抵扣
	if got.DeductibleTax() != money100(130) {
		t.Errorf("可抵扣税额 = %s", got.DeductibleTax())
	}
}

// ★ 同一张发票重复登记必须被拦住 —— 重复登记会直接导致进项税被多抵扣
func TestInvoiceDuplicateRejected(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)

	mk := func() *invoice.Invoice {
		inv := &invoice.Invoice{
			Direction: invoice.DirInput, Kind: invoice.KindSpecial,
			Code: "033002100111", Number: "12345678",
			InvoiceDate: calendar.MustParse("2025-09-10"),
			AmountExTax: money100(1000), TaxRate: money.RatePercent(13),
			Status: invoice.StatusPending,
		}
		inv.ComputeTax()
		return inv
	}
	if _, err := f.DB.Invoices().CreateInvoice(ctx, mk()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Invoices().CreateInvoice(ctx, mk()); err == nil {
		t.Fatal("重复登记同一张发票应被拒绝")
	}

	// 号码相同但方向不同视为不同发票（进项与销项可能碰巧同号）
	other := mk()
	other.Direction = invoice.DirOutput
	if _, err := f.DB.Invoices().CreateInvoice(ctx, other); err != nil {
		t.Errorf("方向不同应允许: %v", err)
	}

	// 作废后可以重新登记
	invs, _ := f.DB.Invoices().ListInvoices(ctx, invoice.DirInput,
		calendar.Date{}, calendar.Date{}, "")
	if len(invs) != 1 {
		t.Fatalf("进项发票数 = %d", len(invs))
	}
	if err := f.DB.Invoices().UpdateInvoiceStatus(ctx, invs[0].ID, invoice.StatusVoided); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Invoices().CreateInvoice(ctx, mk()); err != nil {
		t.Errorf("作废后重新登记应允许: %v", err)
	}
}

// 电子发票没有代码，应按「方向 + 号码」判重
func TestInvoiceDuplicateWithoutCode(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)

	mk := func() *invoice.Invoice {
		inv := &invoice.Invoice{
			Direction: invoice.DirInput, Kind: invoice.KindEGeneral,
			Number:      "25317000000012345678", // 20 位电子发票号
			InvoiceDate: calendar.MustParse("2025-09-10"),
			AmountExTax: money100(200), TaxRate: money.RatePercent(6),
			Status: invoice.StatusPending,
		}
		inv.ComputeTax()
		return inv
	}
	if _, err := f.DB.Invoices().CreateInvoice(ctx, mk()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Invoices().CreateInvoice(ctx, mk()); err == nil {
		t.Fatal("无代码的电子发票也应判重")
	}
}

func TestInvoiceSummaryByPeriod(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)

	// 两张进项专票 + 一张进项普票
	mk := func(num string, kind invoice.Kind, exTax int64) *invoice.Invoice {
		inv := &invoice.Invoice{
			Direction: invoice.DirInput, Kind: kind,
			Code: "033002100111", Number: num,
			InvoiceDate: calendar.MustParse("2025-09-10"),
			AmountExTax: money100(exTax), TaxRate: money.RatePercent(13),
			Status: invoice.StatusVerified,
		}
		inv.ComputeTax()
		return inv
	}
	for _, inv := range []*invoice.Invoice{
		mk("00000001", invoice.KindSpecial, 1000),
		mk("00000002", invoice.KindSpecial, 2000),
		mk("00000003", invoice.KindGeneral, 500),
	} {
		if _, err := f.DB.Invoices().CreateInvoice(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}

	s, err := f.DB.Invoices().InvoiceSummary(ctx, invoice.DirInput,
		calendar.MustParse("2025-09-01"), calendar.MustParse("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Count != 3 {
		t.Errorf("张数 = %d，期望 3", s.Count)
	}
	if s.AmountExTax != money100(3500) {
		t.Errorf("不含税合计 = %s，期望 3500.00", s.AmountExTax)
	}
	// 只有两张专票可抵扣：1000×13% + 2000×13% = 390
	if s.DeductibleTax != money100(390) {
		t.Errorf("可抵扣税额 = %s，期望 390.00（仅专票）", s.DeductibleTax)
	}
}

// ---------------------------------------------------------------------------
// 差旅报销
// ---------------------------------------------------------------------------

// buildClaim 造一张带附件的报销单。
func (f *ieFixture) buildClaim(t *testing.T) *expense.Claim {
	t.Helper()
	ticket := f.putAttachment(t, "高铁票.pdf", "高铁票内容")
	hotel := f.putAttachment(t, "住宿发票.pdf", "住宿专票内容")
	meal := f.putAttachment(t, "餐费小票.pdf", "餐费内容")

	dept := int64(1)
	c := &expense.Claim{
		ClaimantEmployeeID: f.Employee,
		DeptID:             &dept,
		ApplyDate:          calendar.MustParse("2025-09-20"),
		TripStart:          calendar.MustParse("2025-09-10"),
		TripEnd:            calendar.MustParse("2025-09-14"),
		Destination:        "上海",
		Reason:             "参加行业展会",
		Status:             expense.StatusDraft,
		Items: []*expense.Item{
			{Category: expense.CatTransport, OccurDate: calendar.MustParse("2025-09-10"),
				Summary: "高铁往返", Amount: money100(1200), AccountCode: "560207",
				AttachmentHashes: []string{ticket}},
			{Category: expense.CatAccommodation, OccurDate: calendar.MustParse("2025-09-11"),
				Summary: "住宿 4 晚", Amount: money100(1600), TaxAmount: money100(96),
				AccountCode: "560207", AttachmentHashes: []string{hotel}},
			{Category: expense.CatMeal, OccurDate: calendar.MustParse("2025-09-12"),
				Summary: "餐费", Amount: money100(400), AccountCode: "560207",
				AttachmentHashes: []string{meal}},
		},
	}
	c.ComputeTotal()
	return c
}

func TestSaveAndLoadClaim(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)
	c := f.buildClaim(t)

	id, err := f.DB.Claims().SaveClaim(ctx, c)
	if err != nil {
		t.Fatalf("保存报销单失败: %v", err)
	}
	if c.Code == "" {
		t.Fatal("应自动生成报销单号")
	}
	if !strings.HasPrefix(c.Code, "BX-2025-09-") {
		t.Errorf("报销单号 = %q，期望 BX-2025-09-####", c.Code)
	}

	got, err := f.DB.Claims().GetClaim(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalAmount != money100(3200) {
		t.Errorf("总金额 = %s，期望 3200.00", got.TotalAmount)
	}
	if len(got.Items) != 3 {
		t.Fatalf("明细数 = %d，期望 3", len(got.Items))
	}
	// 明细顺序与金额
	if got.Items[0].Amount != money100(1200) || got.Items[1].Amount != money100(1600) {
		t.Errorf("明细金额顺序错误: %s, %s", got.Items[0].Amount, got.Items[1].Amount)
	}
	// ★ 附件 hash 必须持久化
	for i, it := range got.Items {
		if len(it.AttachmentHashes) != 1 {
			t.Errorf("第 %d 条明细的附件数 = %d，期望 1", i+1, len(it.AttachmentHashes))
		}
	}
	// 附件文件应真的在磁盘上，且能被读回
	if err := f.Files.Walk(func(e attachment.Entry) error { return nil }); err != nil {
		t.Fatal(err)
	}
	files, _ := f.Files.List()
	if len(files) != 3 {
		t.Errorf("磁盘附件数 = %d，期望 3", len(files))
	}
	for _, h := range files {
		if _, err := f.Files.Get(h); err != nil {
			t.Errorf("附件 %s 读取失败: %v", h, err)
		}
	}
}

// 报销单号应逐月递增
func TestClaimCodeSequence(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)
	var codes []string
	for i := 0; i < 3; i++ {
		c := f.buildClaim(t)
		if _, err := f.DB.Claims().SaveClaim(ctx, c); err != nil {
			t.Fatal(err)
		}
		codes = append(codes, c.Code)
	}
	want := []string{"BX-2025-09-0001", "BX-2025-09-0002", "BX-2025-09-0003"}
	for i := range want {
		if codes[i] != want[i] {
			t.Errorf("第 %d 个单号 = %q，期望 %q", i+1, codes[i], want[i])
		}
	}
}

func TestApproveAndRejectClaim(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)
	c := f.buildClaim(t)
	id, err := f.DB.Claims().SaveClaim(ctx, c)
	if err != nil {
		t.Fatal(err)
	}

	at := time.Date(2025, 9, 21, 10, 0, 0, 0, time.UTC)
	if err := f.DB.Claims().ApproveClaim(ctx, id, f.Approver, at); err != nil {
		t.Fatalf("审批失败: %v", err)
	}
	got, _ := f.DB.Claims().GetClaim(ctx, id)
	if got.Status != expense.StatusApproved {
		t.Errorf("状态 = %s，期望 approved", got.Status)
	}
	if got.ApproverEmployeeID == nil || *got.ApproverEmployeeID != f.Approver {
		t.Error("未记录审批人")
	}
	if got.ApprovedAt == nil {
		t.Error("未记录审批时间")
	}
	// 已审批不可再改
	c2 := f.buildClaim(t)
	c2.Code = got.Code
	if _, err := f.DB.Claims().SaveClaim(ctx, c2); err == nil {
		t.Error("已审批的报销单不应允许覆盖")
	}

	// 另一张单驳回
	c3 := f.buildClaim(t)
	id3, _ := f.DB.Claims().SaveClaim(ctx, c3)
	if err := f.DB.Claims().RejectClaim(ctx, id3, "发票不全"); err != nil {
		t.Fatal(err)
	}
	got3, _ := f.DB.Claims().GetClaim(ctx, id3)
	if got3.Status != expense.StatusRejected {
		t.Errorf("状态 = %s，期望 rejected", got3.Status)
	}
}

// ---------------------------------------------------------------------------
// ★ 端到端：报销单 → 凭证 → 总账
// ---------------------------------------------------------------------------

func TestPostClaimEndToEnd(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)
	c := f.buildClaim(t)
	id, err := f.DB.Claims().SaveClaim(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.DB.Claims().ApproveClaim(ctx, id, f.Approver,
		time.Date(2025, 9, 21, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	res, err := f.DB.Claims().PostClaim(ctx, id, "王主管",
		time.Date(2025, 9, 21, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("过账失败: %v", err)
	}
	// ★ 生成的是草稿：不占号、不进总账（过账在账期结算）
	if res.No != "" {
		t.Errorf("草稿不该有凭证号，实际 %q", res.No)
	}
	if res.VoucherID == 0 {
		t.Fatal("应生成凭证")
	}

	// 凭证
	vc, err := f.DB.Vouchers().Get(ctx, res.VoucherID)
	if err != nil {
		t.Fatal(err)
	}
	if vc.Source != "expense" {
		t.Errorf("凭证来源 = %q，期望 expense", vc.Source)
	}
	if vc.SourceID == nil || *vc.SourceID != id {
		t.Error("凭证应记录来源报销单 id")
	}
	if !vc.IsBalanced() {
		t.Error("凭证应平衡")
	}

	// 报销单状态
	got, _ := f.DB.Claims().GetClaim(ctx, id)
	if got.Status != expense.StatusPosted {
		t.Errorf("状态 = %s，期望 posted", got.Status)
	}
	if got.VoucherID == nil || *got.VoucherID != res.VoucherID {
		t.Error("未回填凭证 id")
	}

	// ★ 总账：先过账本期草稿（等价于账期结算的第一步）。
	// 凭证是草稿的时候总账里什么都没有 —— 这正是新规则要的效果。
	postDrafts(t, f.DB, 2025, 9)
	bal, err := f.DB.Vouchers().Balance(ctx,
		calendar.MustParse("2025-09-01"), calendar.MustParse("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	// 管理费用—差旅费 = 3200 − 96
	if bal["560207"] != money100(3104) {
		t.Errorf("管理费用—差旅费 = %s，期望 3104.00", bal["560207"])
	}
	// 进项税额 = 96（借方）
	if bal["22210101"] != money100(96) {
		t.Errorf("进项税额 = %s，期望 96.00", bal["22210101"])
	}
	// 其他应付款—员工（贷方）应为 -3200
	if bal["224102"] != -money100(3200) {
		t.Errorf("其他应付款—员工 = %s，期望 -3200.00", bal["224102"])
	}

	// 试算平衡
	d, cr, err := f.DB.Vouchers().TrialBalance(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	if d != cr {
		t.Errorf("试算不平衡：借 %s 贷 %s", d, cr)
	}

	// 资产负债表勾稽
	_, _, issues, err := f.DB.Reports().BuildBalanceSheet(ctx,
		calendar.MustParse("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	for _, is := range issues {
		t.Errorf("勾稽关系不成立: %s", is)
	}
}

// 未审批的报销单不允许过账（没有内控的入账）
func TestPostClaimRequiresApproval(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)
	c := f.buildClaim(t)
	id, _ := f.DB.Claims().SaveClaim(ctx, c)

	if _, err := f.DB.Claims().PostClaim(ctx, id, "王主管", time.Now()); err == nil {
		t.Fatal("未审批的报销单不应允许过账")
	}
	// 修好审批后再过账应成功
	if err := f.DB.Claims().ApproveClaim(ctx, id, f.Approver, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Claims().PostClaim(ctx, id, "王主管", time.Now()); err != nil {
		t.Errorf("审批后应可过账: %v", err)
	}
}

func TestPostClaimRequiresPoster(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)
	c := f.buildClaim(t)
	id, _ := f.DB.Claims().SaveClaim(ctx, c)
	_ = f.DB.Claims().ApproveClaim(ctx, id, f.Approver, time.Now())
	if _, err := f.DB.Claims().PostClaim(ctx, id, "", time.Now()); err == nil {
		t.Error("缺少记账人应报错")
	}
}

// 已付款的报销单：贷方走银行存款
func TestPostClaimPaid(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)
	c := f.buildClaim(t)
	c.PayFromAccount = "1002"
	id, _ := f.DB.Claims().SaveClaim(ctx, c)
	_ = f.DB.Claims().ApproveClaim(ctx, id, f.Approver, time.Now())
	got, _ := f.DB.Claims().GetClaim(ctx, id)
	_ = got.MarkPaid("1002")
	if err := f.DB.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE expense_claim SET status = 'paid' WHERE id = ?`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	res, err := f.DB.Claims().PostClaim(ctx, id, "王主管", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	vc, _ := f.DB.Vouchers().Get(ctx, res.VoucherID)
	var found bool
	for _, e := range vc.Entries {
		if e.AccountCode == "1002" && e.Credit == money100(3200) {
			found = true
		}
	}
	if !found {
		t.Error("已付款应贷银行存款 3200.00")
	}
}

// ---------------------------------------------------------------------------
// 附件完整性
// ---------------------------------------------------------------------------

// ★ 数据库记录的附件必须在磁盘上真的存在
func TestAttachmentIntegrity(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)
	c := f.buildClaim(t)
	if _, err := f.DB.Claims().SaveClaim(ctx, c); err != nil {
		t.Fatal(err)
	}

	onDisk, err := f.Files.List()
	if err != nil {
		t.Fatal(err)
	}
	missing, err := f.DB.Attachments().MissingHashes(ctx, onDisk)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("不应有缺失的附件，实际 %v", missing)
	}

	unref, err := f.DB.Attachments().UnreferencedHashes(ctx, onDisk)
	if err != nil {
		t.Fatal(err)
	}
	if len(unref) != 0 {
		t.Errorf("不应有孤儿附件，实际 %v", unref)
	}
}

// 文件被删后必须能被检出（说明备份不完整或误删）
func TestMissingAttachmentDetected(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)
	c := f.buildClaim(t)
	if _, err := f.DB.Claims().SaveClaim(ctx, c); err != nil {
		t.Fatal(err)
	}
	// 删掉一个物理文件
	onDisk, _ := f.Files.List()
	if len(onDisk) == 0 {
		t.Fatal("前置条件：应有附件")
	}
	if err := f.Files.Delete(onDisk[0]); err != nil {
		t.Fatal(err)
	}

	after, _ := f.Files.List()
	missing, err := f.DB.Attachments().MissingHashes(ctx, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 {
		t.Errorf("应检出 1 个缺失附件，实际 %d", len(missing))
	}
}

// 孤儿附件（数据库无引用）应能被检出以便回收
func TestOrphanAttachmentDetected(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)
	c := f.buildClaim(t)
	if _, err := f.DB.Claims().SaveClaim(ctx, c); err != nil {
		t.Fatal(err)
	}
	// 再传一个不挂到任何单据的附件
	if _, err := f.Files.Put(fakePDF("孤儿"), "孤儿.pdf"); err != nil {
		t.Fatal(err)
	}
	onDisk, _ := f.Files.List()
	unref, err := f.DB.Attachments().UnreferencedHashes(ctx, onDisk)
	if err != nil {
		t.Fatal(err)
	}
	if len(unref) != 1 {
		t.Errorf("应检出 1 个孤儿附件，实际 %d", len(unref))
	}
}

// 同一份附件被多个单据引用时应只占一份空间，删除引用也不应影响另一个
func TestSharedAttachment(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)
	shared := f.putAttachment(t, "共享票据.pdf", "同一份内容")

	// 两张报销单引用同一份附件
	for i := 0; i < 2; i++ {
		c := f.buildClaim(t)
		c.Items[0].AttachmentHashes = []string{shared}
		if _, err := f.DB.Claims().SaveClaim(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	onDisk, _ := f.Files.List()
	// 每张报销单造 3 个附件，但内容相同 → 内容寻址去重后只有 3 个物理文件；
	// 再加上共享的那个，共 4 个
	if len(onDisk) != 4 {
		t.Errorf("磁盘附件数 = %d，期望 4（内容寻址已去重）", len(onDisk))
	}
	// 引用了两次
	refs, err := f.DB.Attachments().AllHashes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if refs[shared] != 2 {
		t.Errorf("共享附件的引用数 = %d，期望 2", refs[shared])
	}
}

func TestDeleteAttachmentRecord(t *testing.T) {
	ctx := context.Background()
	f := setupIE(t)
	c := f.buildClaim(t)
	id, err := f.DB.Claims().SaveClaim(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := f.DB.Attachments().ListOf(ctx, "expense_item", c.Items[0].ID)
	if len(items) == 0 {
		t.Fatalf("前置条件：明细 %d 应有附件", c.Items[0].ID)
	}
	if err := f.DB.Attachments().DeleteAttachment(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := f.DB.Attachments().DeleteAttachment(ctx, items[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("重复删除应报 ErrNotFound，得到 %v", err)
	}
	_ = id

	// 附件文件本身仍在磁盘上（可能被其他单据引用），需由清理工具决定是否回收
	onDisk, _ := f.Files.List()
	unref, _ := f.DB.Attachments().UnreferencedHashes(ctx, onDisk)
	if len(unref) != 1 {
		t.Errorf("删除引用后应产生 1 个孤儿附件，实际 %d", len(unref))
	}
}

// ---------------------------------------------------------------------------
// 附件仓库与数据库的 TypeScript 风格「接口对齐」检查
// ---------------------------------------------------------------------------

func TestAttachmentStorePathUnderBookDir(t *testing.T) {
	f := setupIE(t)
	h := f.putAttachment(t, "x.pdf", "内容")
	path := f.Files.MustPath(h)
	// 必须落在账套目录内（这样备份打包时能一起带走）
	rel, err := filepath.Rel(f.Dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("附件路径 %q 不在账套目录 %q 内", path, f.Dir)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("附件文件不存在: %v", err)
	}
}
