package expense

import (
	"testing"
	"time"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

func y(n int64) money.Money { return money.Money(n) * money.Yuan }
func i64(v int64) *int64    { return &v }

func tripClaim() *Claim {
	dept := int64(1)
	return &Claim{
		Code:               "BX-2025-09-0001",
		ClaimantEmployeeID: 7,
		DeptID:             &dept,
		ApplyDate:          calendar.MustParse("2025-09-20"),
		TripStart:          calendar.MustParse("2025-09-10"),
		TripEnd:            calendar.MustParse("2025-09-14"),
		Destination:        "上海",
		Reason:             "参加行业展会",
		Status:             StatusDraft,
		Items: []*Item{
			{Category: CatTransport, OccurDate: calendar.MustParse("2025-09-10"),
				Summary: "高铁往返", Amount: y(1200), AccountCode: "560207"},
			{Category: CatAccommodation, OccurDate: calendar.MustParse("2025-09-11"),
				Summary: "住宿 4 晚", Amount: y(1600), TaxAmount: y(96), // 6% 专票
				AccountCode: "560207"},
			{Category: CatMeal, OccurDate: calendar.MustParse("2025-09-12"),
				Summary: "餐费", Amount: y(400), AccountCode: "560207"},
		},
	}
}

// ---------------------------------------------------------------------------
// 金额汇总
// ---------------------------------------------------------------------------

func TestComputeTotalAndNet(t *testing.T) {
	c := tripClaim()
	if got := c.ComputeTotal(); got != y(3200) {
		t.Errorf("总金额 = %s，期望 3200.00", got)
	}
	if c.TotalAmount != y(3200) {
		t.Errorf("ComputeTotal 应回填 TotalAmount，得到 %s", c.TotalAmount)
	}
	// 可抵扣税额只有住宿那 96
	if got := c.TotalTax(); got != y(96) {
		t.Errorf("可抵扣税额 = %s，期望 96.00", got)
	}
	// 计入费用的金额 = 3200 − 96
	if got := c.TotalNet(); got != y(3104) {
		t.Errorf("费用金额 = %s，期望 3104.00", got)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("合法报销单不应报错: %v", err)
	}
}

func TestItemNetAmount(t *testing.T) {
	// 有专票：费用按不含税金额
	withTax := &Item{Amount: y(1060), TaxAmount: y(60)}
	if got := withTax.NetAmount(); got != y(1000) {
		t.Errorf("含税明细的费用金额 = %s，期望 1000.00", got)
	}
	// 无票/普票：价税合计全额计入
	noTax := &Item{Amount: y(1000)}
	if got := noTax.NetAmount(); got != y(1000) {
		t.Errorf("无税明细的费用金额 = %s，期望 1000.00", got)
	}
}

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

func TestValidateRejectsProblems(t *testing.T) {
	cases := map[string]func(*Claim){
		"缺报销人":    func(c *Claim) { c.ClaimantEmployeeID = 0 },
		"无明细":     func(c *Claim) { c.Items = nil },
		"申请日期非法":  func(c *Claim) { c.ApplyDate = calendar.Date{} },
		"状态非法":    func(c *Claim) { c.Status = "bogus" },
		"只有出差开始":  func(c *Claim) { c.TripEnd = calendar.Date{} },
		"出差日期倒置":  func(c *Claim) { c.TripEnd = calendar.MustParse("2025-09-01") },
		"金额与明细不符": func(c *Claim) { c.TotalAmount = y(9999) },
	}
	for name, mutate := range cases {
		c := tripClaim()
		c.ComputeTotal()
		mutate(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s 应报错", name)
		}
	}
}

func TestItemValidate(t *testing.T) {
	base := &Item{Category: CatTransport, OccurDate: calendar.MustParse("2025-09-10"),
		Amount: y(100), AccountCode: "560207"}
	if err := base.Validate(); err != nil {
		t.Errorf("合法明细不应报错: %v", err)
	}

	bad := map[string]func(*Item){
		"类别非法":  func(i *Item) { i.Category = "bogus" },
		"日期非法":  func(i *Item) { i.OccurDate = calendar.Date{} },
		"金额为零":  func(i *Item) { i.Amount = 0 },
		"金额为负":  func(i *Item) { i.Amount = y(-1) },
		"税额为负":  func(i *Item) { i.TaxAmount = y(-1) },
		"税额超总额": func(i *Item) { i.TaxAmount = y(200) },
		"缺科目":   func(i *Item) { i.AccountCode = "" },
	}
	for name, mutate := range bad {
		it := *base
		mutate(&it)
		if err := it.Validate(); err == nil {
			t.Errorf("%s 应报错", name)
		}
	}
}

// 费用日期明显不在出差期间内应被拦住（放宽前后 7 天）
func TestDateOutsideTripRejected(t *testing.T) {
	c := tripClaim()
	c.Items = []*Item{{
		Category: CatMeal, OccurDate: calendar.MustParse("2025-08-01"),
		Summary: "很久以前的餐费", Amount: y(100), AccountCode: "560207",
	}}
	c.ComputeTotal()
	if err := c.Validate(); err == nil {
		t.Error("费用日期远离出差期间应报错")
	}
	// 前后 7 天内允许（提前订票、事后补票）
	c.Items[0].OccurDate = calendar.MustParse("2025-09-07") // 出差前 3 天
	c.ComputeTotal()
	if err := c.Validate(); err != nil {
		t.Errorf("前后 7 天内应允许，得到 %v", err)
	}
}

// 非差旅报销（不填出差日期）也应能通过
func TestValidateWithoutTrip(t *testing.T) {
	c := &Claim{
		ClaimantEmployeeID: 7, ApplyDate: calendar.MustParse("2025-09-20"),
		Status: StatusDraft,
		Items: []*Item{{
			Category: CatOther, OccurDate: calendar.MustParse("2025-09-15"),
			Summary: "办公用品", Amount: y(200), AccountCode: "560206",
		}},
	}
	c.ComputeTotal()
	if err := c.Validate(); err != nil {
		t.Errorf("不填出差信息应合法: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 状态流转
// ---------------------------------------------------------------------------

func TestStatusTransitions(t *testing.T) {
	c := tripClaim()
	c.ComputeTotal()

	// 草稿：可改可审批，不可过账
	if !c.Status.CanEdit() || !c.Status.CanApprove() {
		t.Error("草稿应可改可审批")
	}
	if c.Status.CanPost() {
		t.Error("草稿不应可过账（未审批入账等于没有内控）")
	}
	if _, err := c.BuildEntries(DefaultVoucherAccounts()); err == nil {
		t.Error("草稿生成凭证应报错")
	}

	// 审批
	at := time.Date(2025, 9, 21, 10, 0, 0, 0, time.UTC)
	if err := c.Approve(9, at); err != nil {
		t.Fatalf("审批失败: %v", err)
	}
	if c.Status != StatusApproved {
		t.Errorf("状态 = %s", c.Status)
	}
	if c.ApproverEmployeeID == nil || *c.ApproverEmployeeID != 9 {
		t.Error("未记录审批人")
	}
	if c.ApprovedAt == nil {
		t.Error("未记录审批时间")
	}
	// 已审批不可再改、不可重复审批
	if c.Status.CanEdit() {
		t.Error("已审批不应可改")
	}
	if err := c.Approve(9, at); err == nil {
		t.Error("重复审批应报错")
	}
	if !c.Status.CanPost() {
		t.Error("已审批应可过账")
	}

	// 付款
	if err := c.MarkPaid("1002"); err != nil {
		t.Fatalf("标记付款失败: %v", err)
	}
	if c.Status != StatusPaid || c.PayFromAccount != "1002" {
		t.Errorf("付款后状态 = %s，科目 = %q", c.Status, c.PayFromAccount)
	}
}

func TestApproveRequiresApprover(t *testing.T) {
	c := tripClaim()
	c.ComputeTotal()
	if err := c.Approve(0, time.Now()); err == nil {
		t.Error("缺少审批人应报错")
	}
}

func TestReject(t *testing.T) {
	c := tripClaim()
	c.ComputeTotal()
	if err := c.Reject("发票不全"); err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusRejected {
		t.Errorf("状态 = %s", c.Status)
	}
	if c.Remark == "" || !contains(c.Remark, "发票不全") {
		t.Errorf("驳回原因应记入备注，得到 %q", c.Remark)
	}
	if c.Status.CanPost() {
		t.Error("已驳回不应可过账")
	}
}

// ---------------------------------------------------------------------------
// 生成凭证
// ---------------------------------------------------------------------------

// ★ 未付款：借费用+进项税 / 贷其他应付款—员工
func TestBuildEntriesUnpaid(t *testing.T) {
	c := tripClaim()
	c.ComputeTotal()
	_ = c.Approve(9, time.Now())

	vc := DefaultVoucherAccounts()
	entries, err := c.BuildEntries(vc)
	if err != nil {
		t.Fatalf("生成分录失败: %v", err)
	}

	// 借贷平衡
	var d, cr money.Money
	for _, e := range entries {
		d, cr = d.Add(e.Debit), cr.Add(e.Credit)
	}
	if d != cr {
		t.Fatalf("借贷不平：借 %s 贷 %s", d, cr)
	}
	if d != y(3200) {
		t.Errorf("借方合计 = %s，期望 3200.00", d)
	}

	// 三条费用分录（高铁 1200、住宿 1600−96=1504、餐费 400）
	var feeLines int
	var feeTotal, taxTotal money.Money
	for _, e := range entries {
		if e.AccountCode == "560207" {
			feeLines++
			feeTotal = feeTotal.Add(e.Debit)
		}
		if e.AccountCode == vc.InputTax {
			taxTotal = taxTotal.Add(e.Debit)
		}
	}
	if feeLines != 3 {
		t.Errorf("费用行数 = %d，期望 3（按明细逐行列示，便于按部门归集）", feeLines)
	}
	if feeTotal != y(3104) {
		t.Errorf("费用合计 = %s，期望 3104.00", feeTotal)
	}
	if taxTotal != y(96) {
		t.Errorf("进项税额 = %s，期望 96.00", taxTotal)
	}

	// 贷方挂其他应付款—员工
	var payable *Entry
	for i := range entries {
		if entries[i].AccountCode == vc.Payable {
			payable = &entries[i]
		}
	}
	if payable == nil {
		t.Fatal("未付款时应挂其他应付款")
	}
	if payable.Credit != y(3200) {
		t.Errorf("应付员工 = %s，期望 3200.00", payable.Credit)
	}
	if payable.EmployeeID == nil || *payable.EmployeeID != 7 {
		t.Error("应付员工分录应带员工辅助核算")
	}
}

// 已付款：贷方走银行存款
func TestBuildEntriesPaid(t *testing.T) {
	c := tripClaim()
	c.ComputeTotal()
	_ = c.Approve(9, time.Now())
	if err := c.MarkPaid("1002"); err != nil {
		t.Fatal(err)
	}
	vc := DefaultVoucherAccounts()
	entries, err := c.BuildEntries(vc)
	if err != nil {
		t.Fatal(err)
	}
	var bank *Entry
	for i := range entries {
		if entries[i].AccountCode == "1002" {
			bank = &entries[i]
		}
	}
	if bank == nil {
		t.Fatal("已付款时应贷银行存款")
	}
	if bank.Credit != y(3200) {
		t.Errorf("银行付款 = %s，期望 3200.00", bank.Credit)
	}
	var d, cr money.Money
	for _, e := range entries {
		d, cr = d.Add(e.Debit), cr.Add(e.Credit)
	}
	if d != cr {
		t.Errorf("借贷不平：%s vs %s", d, cr)
	}
}

// 没有可抵扣税额时不应出现进项税分录
func TestBuildEntriesWithoutTax(t *testing.T) {
	c := &Claim{
		ClaimantEmployeeID: 7, ApplyDate: calendar.MustParse("2025-09-20"),
		Status: StatusApproved,
		Items: []*Item{{
			Category: CatMeal, OccurDate: calendar.MustParse("2025-09-15"),
			Summary: "餐费", Amount: y(300), AccountCode: "560207",
		}},
	}
	c.ComputeTotal()
	vc := DefaultVoucherAccounts()
	entries, err := c.BuildEntries(vc)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.AccountCode == vc.InputTax {
			t.Error("无可抵扣税额时不应出现进项税额分录")
		}
	}
}

// 明细的费用科目优先于默认值（可按类别设置不同科目）
func TestItemAccountRespected(t *testing.T) {
	c := tripClaim()
	c.Items[0].AccountCode = "560212" // 车辆使用费
	c.ComputeTotal()
	_ = c.Approve(9, time.Now())

	entries, err := c.BuildEntries(DefaultVoucherAccounts())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.AccountCode == "560212" {
			found = true
		}
	}
	if !found {
		t.Error("明细指定的科目未生效")
	}
}

// 部门辅助核算：明细未指定时回落到报销单的部门
func TestDeptFallback(t *testing.T) {
	dept := int64(3)
	c := tripClaim()
	c.DeptID = &dept
	c.ComputeTotal()
	_ = c.Approve(9, time.Now())

	entries, err := c.BuildEntries(DefaultVoucherAccounts())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.AccountCode == "560207" && e.DeptID == nil {
			t.Error("明细未指定部门时应回落到报销单的部门")
		}
	}
	// 明细自己指定时优先
	itemDept := int64(5)
	c.Items[0].DeptID = &itemDept
	entries, _ = c.BuildEntries(DefaultVoucherAccounts())
	if entries[0].DeptID == nil || *entries[0].DeptID != 5 {
		t.Error("明细自己的部门应优先")
	}
}

// ---------------------------------------------------------------------------
// 汇总与辅助
// ---------------------------------------------------------------------------

func TestSummarizeByCategory(t *testing.T) {
	c := tripClaim()
	sums := SummarizeByCategory(c.Items)
	if len(sums) != 3 {
		t.Fatalf("类别数 = %d，期望 3", len(sums))
	}
	// 应按固定顺序输出（交通、住宿、餐费）
	if sums[0].Category != CatTransport {
		t.Errorf("第 1 类 = %s，期望交通费", sums[0].Category)
	}
	if sums[1].Category != CatAccommodation {
		t.Errorf("第 2 类 = %s，期望住宿费", sums[1].Category)
	}
	if sums[1].Tax != y(96) {
		t.Errorf("住宿税额 = %s，期望 96.00", sums[1].Tax)
	}
}

func TestBuildCode(t *testing.T) {
	if got := BuildCode(2025, 9, 1); got != "BX-2025-09-0001" {
		t.Errorf("BuildCode = %q", got)
	}
	if got := BuildCode(2025, 12, 1234); got != "BX-2025-12-1234" {
		t.Errorf("BuildCode = %q", got)
	}
}

func TestCategoryDefaultAccount(t *testing.T) {
	for _, c := range AllCategories() {
		if c.DefaultAccount() == "" {
			t.Errorf("%s 应有默认科目", c.Label())
		}
		if c.Label() == "" {
			t.Errorf("%s 应有中文名", c)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
