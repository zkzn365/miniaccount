// Package expense 实现差旅报销。
//
// # 流程
//
//	录入（附票据）→ 审批 → 付款 → 生成凭证
//
// 每一步都要留痕：谁报的、谁批的、什么时候付的、凭证是哪张。
// 小微企业的报销单往往就是唯一的内控凭据，事后要能说清。
//
// # 与发票档案的关系
//
// 报销明细可以关联发票档案（`InvoiceID`）。取得专票的差旅支出
// （如住宿费）可以抵扣进项税，因此报销单要能把税额单独拆出来，
// 生成凭证时挂到「应交税费—应交增值税（进项税额）」。
package expense

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// 报销相关错误。
var (
	ErrNoClaimant = errors.New("expense: 缺少报销人")
	ErrNoItems    = errors.New("expense: 报销单没有明细")
	ErrBadAmount  = errors.New("expense: 金额不自洽")
	ErrBadStatus  = errors.New("expense: 状态不允许该操作")
	ErrNoAccount  = errors.New("expense: 明细缺少费用科目")
	ErrTraveDates = errors.New("expense: 出差起止日期不合法")
)

// Status 是报销单状态。
type Status string

// 报销单状态。
//
// 刻意保持两种审批态（draft → approved）而不是多级审批：
// 小微企业通常就是「员工提交、老板签字」，多级审批会把流程复杂化
// 而收益极低。需要多级时可在 approved 之前自行扩展。
const (
	StatusDraft    Status = "draft"    // 草稿：可改可删
	StatusApproved Status = "approved" // 已审批：金额锁定，待付款/入账
	StatusPaid     Status = "paid"     // 已付款
	StatusPosted   Status = "posted"   // 已生成凭证
	StatusRejected Status = "rejected" // 已驳回
)

// Label 返回中文名。
func (s Status) Label() string {
	switch s {
	case StatusDraft:
		return "草稿"
	case StatusApproved:
		return "已审批"
	case StatusPaid:
		return "已付款"
	case StatusPosted:
		return "已记账"
	case StatusRejected:
		return "已驳回"
	default:
		return string(s)
	}
}

// Valid 报告状态是否合法。
func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusApproved, StatusPaid, StatusPosted, StatusRejected:
		return true
	default:
		return false
	}
}

// CanEdit 报告该状态下能否修改明细。
func (s Status) CanEdit() bool { return s == StatusDraft }

// CanApprove 报告该状态下能否审批。
func (s Status) CanApprove() bool { return s == StatusDraft }

// CanPost 报告该状态下能否生成凭证。
//
// 只有已审批或已付款才能记账 —— 未经审批的报销单入账等于没有内控。
func (s Status) CanPost() bool { return s == StatusApproved || s == StatusPaid }

// ---------------------------------------------------------------------------
// 费用类别
// ---------------------------------------------------------------------------

// Category 是费用类别。
type Category string

// 常见差旅费用类别。
const (
	CatTransport     Category = "transport"     // 交通费（火车/飞机/长途汽车）
	CatAccommodation Category = "accommodation" // 住宿费
	CatMeal          Category = "meal"          // 餐费补助
	CatLocalTransit  Category = "local_transit" // 市内交通
	CatConference    Category = "conference"    // 会务费
	CatOther         Category = "other"         // 其他
)

// Label 返回中文名。
func (c Category) Label() string {
	switch c {
	case CatTransport:
		return "交通费"
	case CatAccommodation:
		return "住宿费"
	case CatMeal:
		return "餐费"
	case CatLocalTransit:
		return "市内交通"
	case CatConference:
		return "会务费"
	case CatOther:
		return "其他"
	default:
		return string(c)
	}
}

// Valid 报告类别是否合法。
func (c Category) Valid() bool {
	switch c {
	case CatTransport, CatAccommodation, CatMeal, CatLocalTransit, CatConference, CatOther:
		return true
	default:
		return false
	}
}

// AllCategories 返回全部类别。
func AllCategories() []Category {
	return []Category{CatTransport, CatAccommodation, CatMeal,
		CatLocalTransit, CatConference, CatOther}
}

// DefaultAccount 返回各类别的默认费用科目（按本项目预置科目表）。
//
// 这些只是**建议值**：实际科目由用户在明细行上指定，
// 因为不同企业的科目设置差异很大。
func (c Category) DefaultAccount() string {
	switch c {
	case CatTransport, CatLocalTransit:
		return "560207" // 管理费用—差旅费
	case CatAccommodation:
		return "560207"
	case CatMeal:
		return "560207"
	case CatConference:
		return "560213" // 管理费用—中介服务费（会务）
	default:
		return "560217" // 管理费用—其他
	}
}

// ---------------------------------------------------------------------------
// 明细
// ---------------------------------------------------------------------------

// Item 是报销单的一条明细。
type Item struct {
	ID      int64
	ClaimID int64
	LineNo  int

	Category  Category
	OccurDate calendar.Date
	Summary   string

	// Amount 是价税合计（员工实际支付的金额）。
	Amount money.Money
	// TaxAmount 是其中可抵扣的进项税额（取得专票时才有）。
	TaxAmount money.Money
	// AccountCode 是费用归属科目。
	AccountCode string

	// InvoiceID 关联发票档案；AttachmentHashes 是票据附件的 sha256。
	InvoiceID        *int64
	AttachmentHashes []string

	// DeptID 是该笔费用的部门归属（辅助核算用）。
	DeptID *int64
}

// NetAmount 返回计入费用的金额（价税合计 − 可抵扣税额）。
//
// 取得专票时，费用按不含税金额入账、税额单独挂进项税；
// 普票则价税合计全额计入费用。
func (it *Item) NetAmount() money.Money {
	return it.Amount.Sub(it.TaxAmount)
}

// Validate 校验明细。
func (it *Item) Validate() error {
	if !it.Category.Valid() {
		return fmt.Errorf("expense: 费用类别非法: %q", it.Category)
	}
	if !it.OccurDate.Valid() {
		return fmt.Errorf("expense: 费用发生日期非法: %v", it.OccurDate)
	}
	if !it.Amount.IsPositive() {
		return fmt.Errorf("%w: 金额应为正数，实际 %s", ErrBadAmount, it.Amount)
	}
	if it.TaxAmount.IsNegative() {
		return fmt.Errorf("%w: 税额不能为负", ErrBadAmount)
	}
	if it.TaxAmount > it.Amount {
		return fmt.Errorf("%w: 税额 %s 大于价税合计 %s",
			ErrBadAmount, it.TaxAmount, it.Amount)
	}
	if strings.TrimSpace(it.AccountCode) == "" {
		return fmt.Errorf("%w: %s 的「%s」", ErrNoAccount, it.Summary, it.Category.Label())
	}
	return nil
}

// ---------------------------------------------------------------------------
// 报销单
// ---------------------------------------------------------------------------

// Claim 是一张差旅报销单。
type Claim struct {
	ID   int64
	Code string // 报销单号，如 BX-2025-09-0001

	ClaimantEmployeeID int64
	DeptID             *int64

	ApplyDate calendar.Date

	// 出差信息（非差旅类报销可留空）
	TripStart   calendar.Date
	TripEnd     calendar.Date
	Destination string
	Reason      string

	Status Status

	// TotalAmount 由明细汇总得出，不单独录入。
	TotalAmount money.Money

	// VoucherID 是生成的凭证。
	VoucherID *int64
	// ApproverEmployeeID / ApprovedAt 记录审批人，报销单的内控凭据。
	ApproverEmployeeID *int64
	ApprovedAt         *time.Time

	// PayFromAccount 是付款科目（银行存款/库存现金），
	// 为空表示挂「其他应付款—员工」待付。
	PayFromAccount string
	// PayableAccount 是挂账科目，默认「其他应付款—员工」。
	PayableAccount string

	Remark string

	Items []*Item
}

// BuildCode 生成报销单号：BX-YYYY-MM-####。
func BuildCode(year, month, seq int) string {
	return fmt.Sprintf("BX-%04d-%02d-%04d", year, month, seq)
}

// SumItems 返回明细金额合计，**不修改**报销单。
//
// 单独提供这个纯函数是有必要的：校验必须用它而不是 ComputeTotal ——
// 后者会回填 TotalAmount，若在校验里调用，
// 「总金额与明细是否一致」的比较就变成了拿刚算出来的值跟自己比，永远相等。
func (c *Claim) SumItems() money.Money {
	var total money.Money
	for _, it := range c.Items {
		total = total.Add(it.Amount)
	}
	return total
}

// ComputeTotal 由明细汇总出总金额并回填。
func (c *Claim) ComputeTotal() money.Money {
	c.TotalAmount = c.SumItems()
	return c.TotalAmount
}

// TotalTax 返回可抵扣税额合计。
func (c *Claim) TotalTax() money.Money {
	var t money.Money
	for _, it := range c.Items {
		t = t.Add(it.TaxAmount)
	}
	return t
}

// TotalNet 返回计入费用的金额合计。
func (c *Claim) TotalNet() money.Money {
	var t money.Money
	for _, it := range c.Items {
		t = t.Add(it.NetAmount())
	}
	return t
}

// Validate 校验整张报销单。
func (c *Claim) Validate() error {
	if c.ClaimantEmployeeID == 0 {
		return ErrNoClaimant
	}
	if !c.ApplyDate.Valid() {
		return fmt.Errorf("expense: 申请日期非法: %v", c.ApplyDate)
	}
	if !c.Status.Valid() {
		return fmt.Errorf("%w: %q", ErrBadStatus, c.Status)
	}
	if len(c.Items) == 0 {
		return ErrNoItems
	}
	// 出差日期：要么都为空，要么成对且有序
	if !c.TripStart.IsZero() || !c.TripEnd.IsZero() {
		if c.TripStart.IsZero() || c.TripEnd.IsZero() {
			return fmt.Errorf("%w: 出差起止日期必须同时填写", ErrTraveDates)
		}
		if c.TripEnd.Before(c.TripStart) {
			return fmt.Errorf("%w: 结束 %s 早于开始 %s",
				ErrTraveDates, c.TripEnd, c.TripStart)
		}
	}

	for i, it := range c.Items {
		if err := it.Validate(); err != nil {
			return fmt.Errorf("第 %d 行: %w", i+1, err)
		}
		// 费用日期不应落在出差区间之外太远（放宽为前后 7 天，
		// 因为实际存在提前订票、事后补票的情况）
		if !c.TripStart.IsZero() {
			lo := c.TripStart.AddDays(-7)
			hi := c.TripEnd.AddDays(7)
			if !it.OccurDate.Between(lo, hi) {
				return fmt.Errorf("第 %d 行: 费用日期 %s 明显不在出差期间 %s~%s 内",
					i+1, it.OccurDate, c.TripStart, c.TripEnd)
			}
		}
	}

	// 总金额必须与明细一致（用纯函数比较，不能用会回填的 ComputeTotal）
	if sum := c.SumItems(); c.TotalAmount != sum {
		return fmt.Errorf("%w: 总金额 %s ≠ 明细合计 %s",
			ErrBadAmount, c.TotalAmount, sum)
	}
	return nil
}

// Approve 审批通过。
func (c *Claim) Approve(approverID int64, at time.Time) error {
	if !c.Status.CanApprove() {
		return fmt.Errorf("%w: 当前状态为「%s」", ErrBadStatus, c.Status.Label())
	}
	if approverID == 0 {
		return errors.New("expense: 缺少审批人")
	}
	c.Status = StatusApproved
	c.ApproverEmployeeID = &approverID
	t := at
	c.ApprovedAt = &t
	return nil
}

// Reject 驳回。
func (c *Claim) Reject(reason string) error {
	if c.Status != StatusDraft && c.Status != StatusApproved {
		return fmt.Errorf("%w: 当前状态为「%s」", ErrBadStatus, c.Status.Label())
	}
	c.Status = StatusRejected
	if reason != "" {
		if c.Remark != "" {
			c.Remark += "；"
		}
		c.Remark += "驳回原因：" + reason
	}
	return nil
}

// MarkPaid 标记为已付款。
func (c *Claim) MarkPaid(payFromAccount string) error {
	if !c.Status.CanPost() {
		return fmt.Errorf("%w: 当前状态为「%s」，需先审批", ErrBadStatus, c.Status.Label())
	}
	if payFromAccount == "" {
		return errors.New("expense: 缺少付款科目")
	}
	c.PayFromAccount = payFromAccount
	c.Status = StatusPaid
	return nil
}

// ---------------------------------------------------------------------------
// 生成凭证
// ---------------------------------------------------------------------------

// Entry 是生成凭证用的一条分录。
type Entry struct {
	AccountCode string
	Summary     string
	Debit       money.Money
	Credit      money.Money
	ContactID   *int64
	EmployeeID  *int64
	DeptID      *int64
}

// VoucherAccounts 是生成报销凭证所需的科目配置。
type VoucherAccounts struct {
	// InputTax 是「应交税费—应交增值税（进项税额）」。
	InputTax string
	// Payable 是「其他应付款—员工」。
	Payable string
	// DefaultPayFrom 是默认付款科目（银行存款）。
	DefaultPayFrom string
}

// DefaultVoucherAccounts 返回按本项目预置科目表对应的默认配置。
func DefaultVoucherAccounts() VoucherAccounts {
	return VoucherAccounts{
		InputTax:       "22210101",
		Payable:        "224102",
		DefaultPayFrom: "1002",
	}
}

// BuildEntries 生成报销凭证分录。
//
//	借：各费用科目（按明细归集，按部门辅助核算）   不含税金额
//	    应交税费—应交增值税（进项税额）            可抵扣税额
//	  贷：其他应付款—员工【报销人】                未付款时
//	      银行存款                                已付款时
//
// 费用按明细逐行列示而不是合并到一行：一张报销单可能跨越多个费用类别与
// 部门，合并后就无法按部门考核成本了。
func (c *Claim) BuildEntries(vc VoucherAccounts) ([]Entry, error) {
	if !c.Status.CanPost() {
		return nil, fmt.Errorf("%w: 当前状态为「%s」", ErrBadStatus, c.Status.Label())
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}

	summary := fmt.Sprintf("%s差旅报销 %s", c.Destination, c.Code)
	if c.Destination == "" {
		summary = "费用报销 " + c.Code
	}

	var entries []Entry
	var totalTax money.Money

	for _, it := range c.Items {
		lineSummary := it.Summary
		if lineSummary == "" {
			lineSummary = it.Category.Label()
		}
		entries = append(entries, Entry{
			AccountCode: it.AccountCode,
			Summary:     lineSummary,
			Debit:       it.NetAmount(),
			EmployeeID:  ptrI64(c.ClaimantEmployeeID),
			DeptID:      deptOf(it, c),
		})
		totalTax = totalTax.Add(it.TaxAmount)
	}

	// 进项税额单独一行
	if totalTax.IsPositive() {
		if vc.InputTax == "" {
			return nil, errors.New("expense: 有可抵扣税额但未配置进项税额科目")
		}
		entries = append(entries, Entry{
			AccountCode: vc.InputTax,
			Summary:     summary + "（进项税额）",
			Debit:       totalTax,
		})
	}

	// 贷方：已付款走银行，未付款挂其他应付款—员工
	if c.Status == StatusPaid && c.PayFromAccount != "" {
		entries = append(entries, Entry{
			AccountCode: c.PayFromAccount,
			Summary:     summary,
			Credit:      c.TotalAmount,
		})
	} else {
		payable := c.PayableAccount
		if payable == "" {
			payable = vc.Payable
		}
		entries = append(entries, Entry{
			AccountCode: payable,
			Summary:     summary,
			Credit:      c.TotalAmount,
			EmployeeID:  ptrI64(c.ClaimantEmployeeID),
		})
	}
	return entries, nil
}

func deptOf(it *Item, c *Claim) *int64 {
	if it.DeptID != nil {
		return it.DeptID
	}
	return c.DeptID
}

func ptrI64(v int64) *int64 { return &v }

// ---------------------------------------------------------------------------
// 汇总
// ---------------------------------------------------------------------------

// CategorySummary 是按类别的费用汇总。
type CategorySummary struct {
	Category Category
	Count    int
	Amount   money.Money
	Tax      money.Money
}

// SummarizeByCategory 按类别汇总明细。
func SummarizeByCategory(items []*Item) []CategorySummary {
	order := AllCategories()
	byCat := map[Category]*CategorySummary{}
	for _, it := range items {
		s := byCat[it.Category]
		if s == nil {
			s = &CategorySummary{Category: it.Category}
			byCat[it.Category] = s
		}
		s.Count++
		s.Amount = s.Amount.Add(it.Amount)
		s.Tax = s.Tax.Add(it.TaxAmount)
	}
	var out []CategorySummary
	for _, c := range order {
		if s := byCat[c]; s != nil {
			out = append(out, *s)
		}
	}
	return out
}
