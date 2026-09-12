package service

import (
	"context"
	"strconv"

	"fmt"
	"miniaccount/internal/domain/audit"
	"strings"
	"time"

	"miniaccount/internal/domain/expense"
	"miniaccount/internal/domain/money"
)

// ---------------------------------------------------------------------------
// 差旅报销
// ---------------------------------------------------------------------------

// ClaimItemInput 是报销单的一条明细。
type ClaimItemInput struct {
	// Category 是费用类别：transport / hotel / meal / entertain / office / other。
	Category string
	// OccurDate 是发生日期。
	OccurDate string
	Summary   string
	// Amount 是含税金额（分）。
	Amount money.Money
	// TaxAmount 是可抵扣税额（分）。
	TaxAmount money.Money
	// AccountCode 是费用归集科目；留空时按类别取默认科目。
	AccountCode string
	// DeptID 是部门。
	DeptID *int64
	// InvoiceID 关联的发票。
	InvoiceID *int64
}

// ClaimInput 是界面提交的报销单。
type ClaimInput struct {
	ID int64
	// ClaimantEmployeeID 是报销人。
	ClaimantEmployeeID int64
	// DeptID 是报销人所属部门。
	DeptID *int64
	// ApplyDate 是申请日期。
	ApplyDate string
	// TripStart / TripEnd 是出差起止。
	TripStart   string
	TripEnd     string
	Destination string
	Reason      string
	// PayFromAccount 是付款科目（如 1002 银行存款）。
	PayFromAccount string
	Remark         string
	Items          []ClaimItemInput
}

// ClaimItemView 是报销单的一条明细。
type ClaimItemView struct {
	LineNo        int         `json:"lineNo"`
	Category      string      `json:"category"`
	CategoryLabel string      `json:"categoryLabel"`
	OccurDate     string      `json:"occurDate"`
	Summary       string      `json:"summary"`
	Amount        money.Money `json:"amount"`
	TaxAmount     money.Money `json:"taxAmount"`
	// NetAmount 是 Amount − TaxAmount。
	NetAmount   money.Money `json:"netAmount"`
	AccountCode string      `json:"accountCode"`
	DeptID      *int64      `json:"deptId"`
}

// ClaimView 是界面上的报销单。
type ClaimView struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	// ClaimantEmployeeID / ClaimantName 是报销人。
	ClaimantEmployeeID int64  `json:"claimantEmployeeId"`
	ClaimantName       string `json:"claimantName"`
	DeptID             *int64 `json:"deptId"`

	ApplyDate   string `json:"applyDate"`
	TripStart   string `json:"tripStart"`
	TripEnd     string `json:"tripEnd"`
	Destination string `json:"destination"`
	Reason      string `json:"reason"`

	Status string `json:"status"`
	// StatusLabel 是中文状态名。
	StatusLabel string `json:"statusLabel"`
	// TotalAmount 是报销合计。
	TotalAmount money.Money `json:"totalAmount"`
	// TotalTax 是可抵扣税额合计。
	TotalTax money.Money `json:"totalTax"`

	ApproverEmployeeID *int64 `json:"approverEmployeeId"`
	ApproverName       string `json:"approverName"`
	ApprovedAt         string `json:"approvedAt"`

	PayFromAccount string `json:"payFromAccount"`
	Remark         string `json:"remark"`

	Items []ClaimItemView `json:"items"`

	// VoucherNo 是已生成凭证的凭证号。
	VoucherNo string `json:"voucherNo"`

	// CanEdit / CanApprove / CanPost 由服务层算好，界面照着禁用按钮。
	CanEdit    bool `json:"canEdit"`
	CanApprove bool `json:"canApprove"`
	CanPost    bool `json:"canPost"`
	// BlockedReason 说明为什么不能操作。
	BlockedReason string `json:"blockedReason"`
}

// ClaimQuery 是报销单列表的筛选条件。
type ClaimQuery struct {
	Status string
	From   string
	To     string
}

// Claims 返回报销单列表。
func (s *Service) Claims(ctx context.Context, q ClaimQuery) ([]ClaimView, error) {
	from, to, err := s.resolveDateRange(ctx, q.From, q.To)
	if err != nil {
		return nil, err
	}
	s.recordView(ctx, AuditEvent{
		Action: audit.ActionViewClaims, Entity: "claim",
		EntityID: "claims/" + trimTo(q.Status, 10),
		Summary:  "查看报销单列表",
		Detail:   map[string]any{"状态": q.Status, "起": from.String(), "止": to.String()},
	})
	list, err := s.db.Claims().ListClaims(ctx, expense.Status(strings.TrimSpace(q.Status)), from, to)
	if err != nil {
		return nil, err
	}
	names, err := s.employeeNames(ctx)
	if err != nil {
		return nil, err
	}
	vnos, err := s.voucherNos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ClaimView, 0, len(list))
	for _, c := range list {
		out = append(out, toClaimView(c, names, vnos))
	}
	return out, nil
}

// Claim 返回一张报销单的完整内容。
func (s *Service) Claim(ctx context.Context, id int64) (*ClaimView, error) {
	c, err := s.db.Claims().GetClaim(ctx, id)
	if err != nil {
		return nil, err
	}
	names, err := s.employeeNames(ctx)
	if err != nil {
		return nil, err
	}
	vnos, err := s.voucherNos(ctx)
	if err != nil {
		return nil, err
	}
	v := toClaimView(c, names, vnos)
	return &v, nil
}

func toClaimView(c *expense.Claim, names map[int64]string, vnos map[int64]string) ClaimView {
	v := ClaimView{
		ID: c.ID, Code: c.Code,
		ClaimantEmployeeID: c.ClaimantEmployeeID,
		ClaimantName:       names[c.ClaimantEmployeeID],
		DeptID:             c.DeptID,
		ApplyDate:          c.ApplyDate.String(),
		Destination:        c.Destination, Reason: c.Reason,
		Status: string(c.Status), StatusLabel: c.Status.Label(),
		TotalAmount: c.TotalAmount, TotalTax: c.TotalTax(),
		ApproverEmployeeID: c.ApproverEmployeeID,
		PayFromAccount:     c.PayFromAccount, Remark: c.Remark,
	}
	if c.TripStart.Valid() {
		v.TripStart = c.TripStart.String()
	}
	if c.TripEnd.Valid() {
		v.TripEnd = c.TripEnd.String()
	}
	if c.ApproverEmployeeID != nil {
		v.ApproverName = names[*c.ApproverEmployeeID]
	}
	if c.ApprovedAt != nil {
		v.ApprovedAt = c.ApprovedAt.Format(time.RFC3339)
	}
	if c.VoucherID != nil {
		v.VoucherNo = vnos[*c.VoucherID]
	}

	for i, it := range c.Items {
		v.Items = append(v.Items, ClaimItemView{
			LineNo:   i + 1,
			Category: string(it.Category), CategoryLabel: it.Category.Label(),
			OccurDate: it.OccurDate.String(), Summary: it.Summary,
			Amount: it.Amount, TaxAmount: it.TaxAmount,
			NetAmount: it.NetAmount(), AccountCode: it.AccountCode,
			DeptID: it.DeptID,
		})
	}

	// 状态机规则只算一次，界面照着禁用按钮
	v.CanEdit = c.Status.CanEdit()
	v.CanApprove = c.Status.CanApprove()
	v.CanPost = c.Status.CanPost()
	switch c.Status {
	case expense.StatusDraft:
		v.BlockedReason = "报销单还是草稿：先提交审批，审批通过后才能生成凭证。"
	case expense.StatusRejected:
		v.BlockedReason = "报销单已被驳回，请修改后重新提交。"
	case expense.StatusPosted:
		v.BlockedReason = "报销单已生成凭证，只能查看。"
	}
	return v
}

// employeeNames 返回员工 id → 姓名。
func (s *Service) employeeNames(ctx context.Context) (map[int64]string, error) {
	list, err := s.db.Payroll().ListEmployees(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(list))
	for _, e := range list {
		out[e.ID] = e.Name
	}
	return out, nil
}

// SaveClaim 保存一张报销单草稿。
func (s *Service) SaveClaim(ctx context.Context, in ClaimInput) (*ClaimView, error) {
	c := &expense.Claim{
		// 新单据一律从草稿开始 —— 报销是「先提交、后审批」的流程，
		// 直接落成已审批就把内控的那一步跳过去了。
		Status: expense.StatusDraft,
		ID:     in.ID, ClaimantEmployeeID: in.ClaimantEmployeeID,
		DeptID: in.DeptID, Destination: in.Destination, Reason: in.Reason,
		PayFromAccount: in.PayFromAccount, Remark: in.Remark,
	}
	var err error
	if c.ApplyDate, err = requiredDate(in.ApplyDate, "申请日期"); err != nil {
		return nil, err
	}
	if c.TripStart, err = optionalDate(in.TripStart, "出差起始日期"); err != nil {
		return nil, err
	}
	if c.TripEnd, err = optionalDate(in.TripEnd, "出差结束日期"); err != nil {
		return nil, err
	}
	if c.ClaimantEmployeeID == 0 {
		return nil, fmt.Errorf("请选择报销人")
	}

	for i, it := range in.Items {
		cat := expense.Category(strings.TrimSpace(it.Category))
		if cat == "" {
			cat = expense.CatOther
		}
		acc := strings.TrimSpace(it.AccountCode)
		if acc == "" {
			// 类别自带默认科目 —— 让用户少填一个字段，
			// 而这一栏恰恰是最容易填错也最难发现的
			acc = cat.DefaultAccount()
		}
		occ, err := requiredDate(it.OccurDate, fmt.Sprintf("第 %d 行的发生日期", i+1))
		if err != nil {
			return nil, err
		}
		c.Items = append(c.Items, &expense.Item{
			Category: cat, OccurDate: occ,
			Summary: strings.TrimSpace(it.Summary), Amount: it.Amount,
			TaxAmount: it.TaxAmount, AccountCode: acc,
			DeptID: it.DeptID, InvoiceID: it.InvoiceID,
		})
	}
	if len(c.Items) == 0 {
		return nil, fmt.Errorf("报销单至少要有一条明细")
	}

	id, err := s.db.Claims().SaveClaim(ctx, c)
	if err != nil {
		return nil, err
	}
	return s.Claim(ctx, id)
}

// ApproveClaim 审批通过。
func (s *Service) ApproveClaim(ctx context.Context, id, approverID int64) (*ClaimView, error) {
	if approverID == 0 {
		return nil, fmt.Errorf("请选择审批人")
	}
	if err := s.db.Claims().ApproveClaim(ctx, id, approverID, time.Now()); err != nil {
		return nil, err
	}
	return s.Claim(ctx, id)
}

// RejectClaim 驳回。
func (s *Service) RejectClaim(ctx context.Context, id int64, reason string) (*ClaimView, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("驳回必须写明理由 —— 否则报销人不知道要改什么")
	}
	if err := s.db.Claims().RejectClaim(ctx, id, reason); err != nil {
		return nil, err
	}
	return s.Claim(ctx, id)
}

// PostClaim 为报销单生成凭证。
func (s *Service) PostClaim(ctx context.Context, id int64, postingBy string) (*ClaimView, error) {
	if strings.TrimSpace(postingBy) == "" {
		return nil, fmt.Errorf("请填写记账人")
	}
	if _, err := s.db.Claims().PostClaim(ctx, id, strings.TrimSpace(postingBy),
		time.Now()); err != nil {
		return nil, err
	}
	return s.Claim(ctx, id)
}

// ClaimCategories 返回可选的费用类别。
func (s *Service) ClaimCategories() []CategoryOption {
	var out []CategoryOption
	for _, c := range expense.AllCategories() {
		out = append(out, CategoryOption{
			Value: string(c), Label: c.Label(), Account: c.DefaultAccount(),
		})
	}
	return out
}

// CategoryOption 是一个费用类别。
type CategoryOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	// Account 是该类别的默认费用科目。
	Account string `json:"account"`
}

// ---------------------------------------------------------------------------
// 往来单位档案
// ---------------------------------------------------------------------------

// ContactInput 是新增/修改往来单位的输入。
type ContactInput struct {
	ID   int64
	Kind string
	Name string
	// ShortName 是简称，银行流水里出现的常是它。
	ShortName string
	TaxNo     string
	// BankName / BankAccount 用于付款。
	BankName      string
	BankAccount   string
	ContactPerson string
	Phone         string
	Address       string
	Enabled       bool
	Remark        string
}

// SaveContact 新增或修改往来单位。
func (s *Service) SaveContact(ctx context.Context, in ContactInput) (id int64, err error) {
	defer func() {
		if err != nil {
			return
		}
		// 规范点名：基础数据（辅助核算项目）的维护要留痕
		s.recordAudit(ctx, AuditEvent{
			Action:  audit.ActionContactSave,
			Summary: fmt.Sprintf("维护往来单位「%s」", strings.TrimSpace(in.Name)),
			Entity:  "contact", EntityID: strconv.FormatInt(id, 10),
			Detail: map[string]any{
				"名称": in.Name, "类型": in.Kind, "简称": in.ShortName,
				"税号": in.TaxNo, "启用": in.Enabled,
			},
		})
	}()
	return s.saveContact(ctx, in)
}

func (s *Service) saveContact(ctx context.Context, in ContactInput) (int64, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return 0, fmt.Errorf("往来单位名称不能为空")
	}
	// 规范点名：基础数据（辅助核算项目、人员信息）的维护也要留痕
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = "customer"
	}
	var id int64
	err := s.db.WithTx(ctx, func(tx *sqliteTx) error {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if in.ID == 0 {
			res, err := tx.Exec(ctx, `
				INSERT INTO contact (kind, name, short_name, tax_no, bank_name,
					bank_account, contact_person, phone, address, is_enabled,
					remark, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				kind, name, in.ShortName, in.TaxNo, in.BankName, in.BankAccount,
				in.ContactPerson, in.Phone, in.Address, boolI(in.Enabled),
				in.Remark, now, now)
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		}
		res, err := tx.Exec(ctx, `
			UPDATE contact SET kind = ?, name = ?, short_name = ?, tax_no = ?,
				bank_name = ?, bank_account = ?, contact_person = ?, phone = ?,
				address = ?, is_enabled = ?, remark = ?, updated_at = ?
			 WHERE id = ?`,
			kind, name, in.ShortName, in.TaxNo, in.BankName, in.BankAccount,
			in.ContactPerson, in.Phone, in.Address, boolI(in.Enabled),
			in.Remark, now, in.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("往来单位 id=%d 不存在", in.ID)
		}
		id = in.ID
		return nil
	})
	return id, err
}

// requiredDate 解析必填日期。
func requiredDate(s, field string) (dateT, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return dateT{}, fmt.Errorf("请填写%s", field)
	}
	d, err := parseDate(s)
	if err != nil {
		return dateT{}, fmt.Errorf("%s %q 格式不对，应为 YYYY-MM-DD", field, s)
	}
	return d, nil
}

// optionalDate 解析可选日期。
func optionalDate(s, field string) (dateT, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return dateT{}, nil
	}
	d, err := parseDate(s)
	if err != nil {
		return dateT{}, fmt.Errorf("%s %q 格式不对，应为 YYYY-MM-DD", field, s)
	}
	return d, nil
}
