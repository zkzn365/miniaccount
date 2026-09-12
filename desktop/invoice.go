package main

import (
	"fmt"
	"strings"

	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 发票与报销的绑定
// ---------------------------------------------------------------------------

// InvoiceRequest 是界面提交的一张发票。
//
// ★ 金额用「元」的字符串传，由这里转成分 —— 与凭证、工资同一套约定。
type InvoiceRequest struct {
	ID          int64  `json:"id"`
	Direction   string `json:"direction"`
	Kind        string `json:"kind"`
	Code        string `json:"code"`
	Number      string `json:"number"`
	InvoiceDate string `json:"invoiceDate"`

	SellerName  string `json:"sellerName"`
	SellerTaxNo string `json:"sellerTaxNo"`
	BuyerName   string `json:"buyerName"`
	BuyerTaxNo  string `json:"buyerTaxNo"`

	AmountExTax string `json:"amountExTax"`
	TaxRatePPM  int64  `json:"taxRatePpm"`
	TaxAmount   string `json:"taxAmount"`
	TotalAmount string `json:"totalAmount"`

	Category       string `json:"category"`
	ContactID      *int64 `json:"contactId"`
	Remark         string `json:"remark"`
	ExpenseAccount string `json:"expenseAccount"`
	// DeptID 是费用归集部门 —— 费用科目大多要求部门辅助核算。
	DeptID *int64 `json:"deptId"`
}

func (r InvoiceRequest) toService() (service.InvoiceInput, error) {
	out := service.InvoiceInput{
		ID: r.ID, Direction: r.Direction, Kind: r.Kind,
		Code: r.Code, Number: r.Number, InvoiceDate: r.InvoiceDate,
		SellerName: r.SellerName, SellerTaxNo: r.SellerTaxNo,
		BuyerName: r.BuyerName, BuyerTaxNo: r.BuyerTaxNo,
		TaxRatePPM: r.TaxRatePPM, Category: r.Category,
		ContactID: r.ContactID, Remark: r.Remark,
		ExpenseAccount: r.ExpenseAccount, DeptID: r.DeptID,
	}
	for _, f := range []struct {
		name string
		in   string
		out  *int64
	}{
		{"不含税金额", r.AmountExTax, (*int64)(&out.AmountExTax)},
		{"税额", r.TaxAmount, (*int64)(&out.TaxAmount)},
		{"价税合计", r.TotalAmount, (*int64)(&out.TotalAmount)},
	} {
		m, err := ParseYuan(f.in)
		if err != nil {
			return out, errField(f.name, err)
		}
		*f.out = int64(m)
	}
	return out, nil
}

// InvoiceQueryRequest 是发票列表的筛选条件。
type InvoiceQueryRequest struct {
	Direction string `json:"direction"`
	Status    string `json:"status"`
	From      string `json:"from"`
	To        string `json:"to"`
}

// Invoices 返回发票列表。
func (a *App) Invoices(req InvoiceQueryRequest) (out []service.InvoiceView, err error) {
	defer recoverTo(&err, "Invoices")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Invoices(a.context(), service.InvoiceQuery{
		Direction: req.Direction, Status: req.Status, From: req.From, To: req.To,
	}))
}

// SaveInvoice 新增或修改发票。
func (a *App) SaveInvoice(req InvoiceRequest) (out *service.InvoiceView, err error) {
	defer recoverTo(&err, "SaveInvoice")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	in, cerr := req.toService()
	if cerr != nil {
		return nil, cerr
	}
	return wrap(svc.SaveInvoice(a.context(), in))
}

// PostInvoiceRequest 是发票生成凭证的参数。
type PostInvoiceRequest struct {
	ID int64 `json:"id"`
	// PostingBy 是记账人。
	PostingBy string `json:"postingBy"`
	// ExpenseAccount 是进项票的费用归集科目。
	ExpenseAccount string `json:"expenseAccount"`
	// DeptID 是费用归集部门 —— 费用科目大多要求部门辅助核算。
	DeptID *int64 `json:"deptId"`
}

// PostInvoice 为一张发票生成凭证。
func (a *App) PostInvoice(req PostInvoiceRequest) (out *service.InvoiceView, err error) {
	defer recoverTo(&err, "PostInvoice")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	if strings.TrimSpace(req.PostingBy) == "" {
		return nil, &Fault{Kind: FaultInvalid, Message: "请填写记账人"}
	}
	return wrap(svc.PostInvoice(a.context(), req.ID, req.PostingBy,
		req.ExpenseAccount, req.DeptID))
}

// InvoiceSummaryView 返回发票汇总。
func (a *App) InvoiceSummary(direction, from, to string) (out *service.InvoiceSummaryView, err error) {
	defer recoverTo(&err, "InvoiceSummary")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.InvoiceSummary(a.context(), direction, from, to))
}

// InvoiceRates 返回常用税率。
func (a *App) InvoiceRates() (out []service.TaxRateOption, err error) {
	defer recoverTo(&err, "InvoiceRates")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	// 税率是常量，不需要账套；但保持「未打开账套时统一报错」的一致性
	return svc.InvoiceRates(), nil
}

// InvoiceCategories 返回可选的费用类别。
func (a *App) InvoiceCategories() (out []service.CategoryOption, err error) {
	defer recoverTo(&err, "InvoiceCategories")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return svc.ClaimCategories(), nil
}

// ---------------------------------------------------------------------------
// 报销单
// ---------------------------------------------------------------------------

// ClaimItemRequest 是报销单的一条明细。
type ClaimItemRequest struct {
	Category    string `json:"category"`
	OccurDate   string `json:"occurDate"`
	Summary     string `json:"summary"`
	AmountYuan  string `json:"amountYuan"`
	TaxYuan     string `json:"taxYuan"`
	AccountCode string `json:"accountCode"`
	DeptID      *int64 `json:"deptId"`
	InvoiceID   *int64 `json:"invoiceId"`
}

// ClaimRequest 是界面提交的报销单。
type ClaimRequest struct {
	ID                 int64              `json:"id"`
	ClaimantEmployeeID int64              `json:"claimantEmployeeId"`
	DeptID             *int64             `json:"deptId"`
	ApplyDate          string             `json:"applyDate"`
	TripStart          string             `json:"tripStart"`
	TripEnd            string             `json:"tripEnd"`
	Destination        string             `json:"destination"`
	Reason             string             `json:"reason"`
	PayFromAccount     string             `json:"payFromAccount"`
	Remark             string             `json:"remark"`
	Items              []ClaimItemRequest `json:"items"`
}

func (r ClaimRequest) toService() (service.ClaimInput, error) {
	out := service.ClaimInput{
		ID: r.ID, ClaimantEmployeeID: r.ClaimantEmployeeID, DeptID: r.DeptID,
		ApplyDate: r.ApplyDate, TripStart: r.TripStart, TripEnd: r.TripEnd,
		Destination: r.Destination, Reason: r.Reason,
		PayFromAccount: r.PayFromAccount, Remark: r.Remark,
	}
	for i, it := range r.Items {
		amt, err := ParseYuan(it.AmountYuan)
		if err != nil {
			return out, errField(fmtLine(i, "金额"), err)
		}
		tax, err := ParseYuan(it.TaxYuan)
		if err != nil {
			return out, errField(fmtLine(i, "税额"), err)
		}
		out.Items = append(out.Items, service.ClaimItemInput{
			Category: it.Category, OccurDate: it.OccurDate, Summary: it.Summary,
			Amount: amt, TaxAmount: tax, AccountCode: it.AccountCode,
			DeptID: it.DeptID, InvoiceID: it.InvoiceID,
		})
	}
	return out, nil
}

// ClaimQueryRequest 是报销单列表的筛选条件。
type ClaimQueryRequest struct {
	Status string `json:"status"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// Claims 返回报销单列表。
func (a *App) Claims(req ClaimQueryRequest) (out []service.ClaimView, err error) {
	defer recoverTo(&err, "Claims")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Claims(a.context(), service.ClaimQuery{
		Status: req.Status, From: req.From, To: req.To,
	}))
}

// ClaimDetail 返回一张报销单的完整内容。
func (a *App) ClaimDetail(id int64) (out *service.ClaimView, err error) {
	defer recoverTo(&err, "ClaimDetail")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Claim(a.context(), id))
}

// SaveClaim 保存一张报销单草稿。
func (a *App) SaveClaim(req ClaimRequest) (out *service.ClaimView, err error) {
	defer recoverTo(&err, "SaveClaim")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	in, cerr := req.toService()
	if cerr != nil {
		return nil, cerr
	}
	return wrap(svc.SaveClaim(a.context(), in))
}

// ClaimActionRequest 是审批/记账类操作的参数。
type ClaimActionRequest struct {
	ID int64 `json:"id"`
	// ApproverID 是审批人（员工 id）。
	ApproverID int64 `json:"approverId"`
	// PostingBy 是记账人。
	PostingBy string `json:"postingBy"`
	// Reason 是驳回理由。
	Reason string `json:"reason"`
}

// ApproveClaim 审批通过。
func (a *App) ApproveClaim(req ClaimActionRequest) (out *service.ClaimView, err error) {
	defer recoverTo(&err, "ApproveClaim")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.ApproveClaim(a.context(), req.ID, req.ApproverID))
}

// RejectClaim 驳回。
func (a *App) RejectClaim(req ClaimActionRequest) (out *service.ClaimView, err error) {
	defer recoverTo(&err, "RejectClaim")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.RejectClaim(a.context(), req.ID, req.Reason))
}

// PostClaim 为报销单生成凭证。
func (a *App) PostClaim(req ClaimActionRequest) (out *service.ClaimView, err error) {
	defer recoverTo(&err, "PostClaim")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	if strings.TrimSpace(req.PostingBy) == "" {
		return nil, &Fault{Kind: FaultInvalid, Message: "请填写记账人"}
	}
	return wrap(svc.PostClaim(a.context(), req.ID, req.PostingBy))
}

// ClaimCategories 返回费用类别。
func (a *App) ClaimCategories() (out []service.CategoryOption, err error) {
	defer recoverTo(&err, "ClaimCategories")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return svc.ClaimCategories(), nil
}

// ---------------------------------------------------------------------------
// 往来单位档案
// ---------------------------------------------------------------------------

// ContactRequest 是界面提交的往来单位。
type ContactRequest struct {
	ID            int64  `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	ShortName     string `json:"shortName"`
	TaxNo         string `json:"taxNo"`
	BankName      string `json:"bankName"`
	BankAccount   string `json:"bankAccount"`
	ContactPerson string `json:"contactPerson"`
	Phone         string `json:"phone"`
	Address       string `json:"address"`
	Enabled       bool   `json:"enabled"`
	Remark        string `json:"remark"`
}

// SaveContact 新增或修改往来单位。
func (a *App) SaveContact(req ContactRequest) (out int64, err error) {
	defer recoverTo(&err, "SaveContact")()
	svc, f := a.book()
	if f != nil {
		return 0, f
	}
	return wrap(svc.SaveContact(a.context(), service.ContactInput{
		ID: req.ID, Kind: req.Kind, Name: req.Name, ShortName: req.ShortName,
		TaxNo: req.TaxNo, BankName: req.BankName, BankAccount: req.BankAccount,
		ContactPerson: req.ContactPerson, Phone: req.Phone, Address: req.Address,
		Enabled: req.Enabled, Remark: req.Remark,
	}))
}

// ContactDetail 是往来单位档案的完整信息。
type ContactDetail struct {
	ID            int64  `json:"id"`
	Kind          string `json:"kind"`
	KindLabel     string `json:"kindLabel"`
	Name          string `json:"name"`
	ShortName     string `json:"shortName"`
	TaxNo         string `json:"taxNo"`
	BankName      string `json:"bankName"`
	BankAccount   string `json:"bankAccount"`
	ContactPerson string `json:"contactPerson"`
	Phone         string `json:"phone"`
	Address       string `json:"address"`
	Enabled       bool   `json:"enabled"`
	Remark        string `json:"remark"`
}

// ContactList 返回全部往来单位（含停用）。
func (a *App) ContactList() (out []ContactDetail, err error) {
	defer recoverTo(&err, "ContactList")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	rows, qerr := svc.DB().SQL().QueryContext(a.context(), `
		SELECT id, kind, name, short_name, tax_no, bank_name, bank_account,
		       contact_person, phone, address, is_enabled, remark
		  FROM contact ORDER BY kind, name`)
	if qerr != nil {
		return nil, classify(qerr)
	}
	defer rows.Close()
	for rows.Next() {
		var c ContactDetail
		var enabled int
		if err := rows.Scan(&c.ID, &c.Kind, &c.Name, &c.ShortName, &c.TaxNo,
			&c.BankName, &c.BankAccount, &c.ContactPerson, &c.Phone,
			&c.Address, &enabled, &c.Remark); err != nil {
			return nil, classify(err)
		}
		c.Enabled = enabled != 0
		c.KindLabel = contactKindLabel(c.Kind)
		out = append(out, c)
	}
	return out, classify(rows.Err())
}

// fmtLine 拼出「第 N 行 XXX」的字段名，让错误信息能定位到具体行。
//
// 报销单动辄十几行明细，只报「金额格式不对」等于让用户自己一行行找。
func fmtLine(i int, field string) string {
	return fmt.Sprintf("第 %d 行%s", i+1, field)
}
