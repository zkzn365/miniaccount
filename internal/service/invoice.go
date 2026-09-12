package service

import (
	"context"

	"fmt"
	"miniaccount/internal/domain/audit"
	"strings"
	"time"

	"miniaccount/internal/domain/expense"
	"miniaccount/internal/domain/invoice"
	"miniaccount/internal/domain/money"
)

// ---------------------------------------------------------------------------
// 发票
// ---------------------------------------------------------------------------

// InvoiceInput 是界面提交的一张发票。
//
// 金额用 **int64 分**：界面在提交前已经把「元」转成分，
// 与凭证录入、工资走同一套约定。
type InvoiceInput struct {
	ID int64
	// Direction 是 input（进项）| output（销项）。
	Direction string
	// Kind 是发票种类：
	//   special     增值税专用发票（可抵扣进项）
	//   general     增值税普通发票
	//   e_special   电子专用发票（可抵扣）
	//   e_general   电子普通发票
	//   other       其他（定额发票、机动车发票等）
	Kind string
	// Code / Number 是发票代码与号码。
	Code   string
	Number string
	// InvoiceDate 是开票日期。
	InvoiceDate string

	SellerName  string
	SellerTaxNo string
	BuyerName   string
	BuyerTaxNo  string

	// AmountExTax 是不含税金额。
	AmountExTax money.Money
	// TaxRatePPM 是税率（百万分之一），如 13% → 130000。
	TaxRatePPM int64
	// TaxAmount 是税额。为 0 时按 AmountExTax × TaxRate 计算。
	TaxAmount money.Money
	// TotalAmount 是价税合计。为 0 时按 AmountExTax + TaxAmount 计算。
	TotalAmount money.Money

	Category  string
	ContactID *int64
	Remark    string
	// ExpenseAccount 是进项票的费用归集科目（生成凭证时用）。
	ExpenseAccount string
	// DeptID 是费用归集到哪个部门（进项票生成凭证时必填）。
	//
	// ★ 不是可选项：绝大多数费用科目都声明了按部门辅助核算，
	// 不填会被过账校验直接拒绝。
	DeptID *int64
}

// InvoiceView 是界面上的发票一行。
type InvoiceView struct {
	ID int64 `json:"id"`
	// Direction / DirectionLabel 是进销方向。
	Direction      string `json:"direction"`
	DirectionLabel string `json:"directionLabel"`
	// Kind / KindLabel 是发票种类。
	Kind      string `json:"kind"`
	KindLabel string `json:"kindLabel"`

	Code        string `json:"code"`
	Number      string `json:"number"`
	InvoiceDate string `json:"invoiceDate"`
	SellerName  string `json:"sellerName"`
	BuyerName   string `json:"buyerName"`

	AmountExTax money.Money `json:"amountExTax"`
	TaxRatePPM  int64       `json:"taxRatePpm"`
	// TaxRateLabel 是「13%」。
	TaxRateLabel string      `json:"taxRateLabel"`
	TaxAmount    money.Money `json:"taxAmount"`
	TotalAmount  money.Money `json:"totalAmount"`
	// DeductibleTax 是可抵扣税额（仅进项专票）。
	DeductibleTax money.Money `json:"deductibleTax"`
	// CostAmount 是应计入成本费用的金额。
	CostAmount money.Money `json:"costAmount"`

	Category string `json:"category"`
	Status   string `json:"status"`
	// StatusLabel 是中文状态名。
	StatusLabel string `json:"statusLabel"`
	ContactID   *int64 `json:"contactId"`
	Remark      string `json:"remark"`

	// VoucherNo 是已生成凭证的凭证号；为空表示尚未入账。
	VoucherNo string `json:"voucherNo"`
	// Posted 为真表示已生成凭证。
	Posted bool `json:"posted"`
	// AttachmentCount 是附件数量。
	AttachmentCount int `json:"attachmentCount"`
}

// InvoiceQuery 是发票列表的筛选条件。
type InvoiceQuery struct {
	// Direction 为空表示全部。
	Direction string
	// Status 为空表示全部。
	Status string
	// From / To 是开票日期区间（YYYY-MM-DD）。
	From string `json:"from"`
	To   string `json:"to"`
}

// Invoices 返回发票列表。
func (s *Service) Invoices(ctx context.Context, q InvoiceQuery) ([]InvoiceView, error) {
	from, to, err := s.resolveDateRange(ctx, q.From, q.To)
	if err != nil {
		return nil, err
	}
	s.recordView(ctx, AuditEvent{
		Action: audit.ActionViewInvoices, Entity: "invoice",
		EntityID: "invoices/" + trimTo(q.Direction, 10),
		Summary:  "查看发票台账",
		Detail:   map[string]any{"方向": q.Direction, "起": from.String(), "止": to.String()},
	})
	list, err := s.db.Invoices().ListInvoices(ctx,
		invoice.Direction(strings.TrimSpace(q.Direction)),
		from, to, invoice.Status(strings.TrimSpace(q.Status)))
	if err != nil {
		return nil, err
	}
	vnos, err := s.voucherNos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]InvoiceView, 0, len(list))
	for _, inv := range list {
		out = append(out, toInvoiceView(inv, vnos))
	}
	return out, nil
}

func toInvoiceView(inv *invoice.Invoice, vnos map[int64]string) InvoiceView {
	v := InvoiceView{
		ID:        inv.ID,
		Direction: string(inv.Direction), DirectionLabel: inv.Direction.Label(),
		Kind: string(inv.Kind), KindLabel: inv.Kind.Label(),
		Code: inv.Code, Number: inv.Number,
		InvoiceDate: inv.InvoiceDate.String(),
		SellerName:  inv.SellerName, BuyerName: inv.BuyerName,
		AmountExTax: inv.AmountExTax, TaxRatePPM: int64(inv.TaxRate),
		TaxRateLabel: inv.TaxRate.String(),
		TaxAmount:    inv.TaxAmount, TotalAmount: inv.TotalAmount,
		DeductibleTax: inv.DeductibleTax(), CostAmount: inv.CostAmount(),
		Category: inv.Category,
		Status:   string(inv.Status), StatusLabel: inv.Status.Label(),
		ContactID: inv.ContactID, Remark: inv.Remark,
		AttachmentCount: len(inv.AttachmentHashes),
	}
	if inv.VoucherID != nil {
		v.Posted = true
		v.VoucherNo = vnos[*inv.VoucherID]
	}
	return v
}

// SaveInvoice 新增或修改发票。
func (s *Service) SaveInvoice(ctx context.Context, in InvoiceInput) (*InvoiceView, error) {
	inv := &invoice.Invoice{
		ID:        in.ID,
		Direction: invoice.Direction(strings.TrimSpace(in.Direction)),
		Kind:      invoice.Kind(strings.TrimSpace(in.Kind)),
		Code:      strings.TrimSpace(in.Code), Number: strings.TrimSpace(in.Number),
		SellerName: in.SellerName, SellerTaxNo: in.SellerTaxNo,
		BuyerName: in.BuyerName, BuyerTaxNo: in.BuyerTaxNo,
		AmountExTax: in.AmountExTax, TaxRate: money.Rate(in.TaxRatePPM),
		TaxAmount: in.TaxAmount, TotalAmount: in.TotalAmount,
		Category: in.Category, ContactID: in.ContactID, Remark: in.Remark,
	}
	_ = in.DeptID // 部门在生成凭证时用；保存时不入发票表
	d, err := parseDate(in.InvoiceDate)
	if err != nil {
		return nil, fmt.Errorf("开票日期 %q 格式不对，应为 YYYY-MM-DD", in.InvoiceDate)
	}
	inv.InvoiceDate = d
	// 发票状态：新录入的默认「未认证」。
	//
	// 增值税专用发票需要在税局平台上勾选认证后才能抵扣；
	// 「未认证 → 已认证 → 已入账」这条链在实务里是要对上的，
	// 因此不给它一个默认值就等于少了一整个环节。
	if inv.Status == "" {
		inv.Status = invoice.StatusPending
	}

	// 三个金额里给两个就能推出第三个 —— 界面只让用户填两个，
	// 剩下的由这里补，避免用户填出「价 + 税 ≠ 合计」的自相矛盾数据。
	switch {
	case inv.AmountExTax.IsZero() && !inv.TotalAmount.IsZero() && inv.TaxRate > 0:
		inv.ReverseFromTotal()
	case inv.TaxAmount.IsZero() && !inv.AmountExTax.IsZero():
		inv.ComputeTax()
	}
	if inv.TotalAmount.IsZero() && !inv.AmountExTax.IsZero() {
		inv.TotalAmount = inv.AmountExTax.Add(inv.TaxAmount)
	}

	if err := inv.Validate(); err != nil {
		return nil, err
	}
	id, err := s.db.Invoices().CreateInvoice(ctx, inv)
	if err != nil {
		return nil, err
	}
	got, err := s.db.Invoices().GetInvoice(ctx, id)
	if err != nil {
		return nil, err
	}
	vnos, err := s.voucherNos(ctx)
	if err != nil {
		return nil, err
	}
	v := toInvoiceView(got, vnos)
	return &v, nil
}

// PostInvoice 为一张发票生成凭证。
func (s *Service) PostInvoice(ctx context.Context, id int64,
	postingBy, expenseAccount string, deptID *int64) (*InvoiceView, error) {
	if strings.TrimSpace(postingBy) == "" {
		return nil, fmt.Errorf("请填写记账人")
	}
	if _, err := s.db.Invoices().PostInvoice(ctx, id,
		strings.TrimSpace(postingBy), time.Now(), expenseAccount, deptID); err != nil {
		return nil, err
	}
	got, err := s.db.Invoices().GetInvoice(ctx, id)
	if err != nil {
		return nil, err
	}
	vnos, err := s.voucherNos(ctx)
	if err != nil {
		return nil, err
	}
	v := toInvoiceView(got, vnos)
	return &v, nil
}

// InvoiceSummaryView 是发票汇总。
type InvoiceSummaryView struct {
	Count       int         `json:"count"`
	AmountExTax money.Money `json:"amountExTax"`
	TaxAmount   money.Money `json:"taxAmount"`
	TotalAmount money.Money `json:"totalAmount"`
	Deductible  money.Money `json:"deductible"`
	// UnpostedCount 是尚未生成凭证的张数。
	//
	// ★ 这个数字比金额更有用：发票入没入账是「账做完没有」的直接指标，
	// 而金额大不大跟做没做完没有关系。
	UnpostedCount int `json:"unpostedCount"`
}

// InvoiceSummary 返回发票汇总。
func (s *Service) InvoiceSummary(ctx context.Context, dir string, from, to string) (*InvoiceSummaryView, error) {
	list, err := s.Invoices(ctx, InvoiceQuery{Direction: dir, From: from, To: to})
	if err != nil {
		return nil, err
	}
	out := &InvoiceSummaryView{Count: len(list)}
	for _, v := range list {
		out.AmountExTax = out.AmountExTax.Add(v.AmountExTax)
		out.TaxAmount = out.TaxAmount.Add(v.TaxAmount)
		out.TotalAmount = out.TotalAmount.Add(v.TotalAmount)
		out.Deductible = out.Deductible.Add(v.DeductibleTax)
		if !v.Posted {
			out.UnpostedCount++
		}
	}
	return out, nil
}

// InvoiceRates 返回常用税率，供界面下拉选择。
func (s *Service) InvoiceRates() []TaxRateOption {
	out := make([]TaxRateOption, 0, 8)
	for _, r := range invoice.CommonRates() {
		out = append(out, TaxRateOption{PPM: int64(r), Label: r.String()})
	}
	return out
}

// TaxRateOption 是一个可选税率。
type TaxRateOption struct {
	PPM   int64  `json:"ppm"`
	Label string `json:"label"`
}

// InvoiceCategories 返回可选的费用类别。
func (s *Service) InvoiceCategories() []string {
	var out []string
	for _, c := range expense.AllCategories() {
		out = append(out, string(c))
	}
	return out
}
