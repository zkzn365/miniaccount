package main

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/service"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ---------------------------------------------------------------------------
// 凭证录入的绑定
// ---------------------------------------------------------------------------

// VoucherQueryRequest 是凭证列表的查询参数。
type VoucherQueryRequest struct {
	Year    int    `json:"year"`
	Month   int    `json:"month"`
	Status  string `json:"status,omitempty"`
	Keyword string `json:"keyword,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// Vouchers 返回凭证列表。
func (a *App) Vouchers(req VoucherQueryRequest) (out []service.VoucherSummary, err error) {
	defer recoverTo(&err, "Vouchers")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Vouchers(a.context(), service.VoucherQuery{
		Year: req.Year, Month: req.Month,
		Status: req.Status, Keyword: req.Keyword, Limit: req.Limit,
	}))
}

// VoucherDetail 返回一张凭证的完整内容。
func (a *App) VoucherDetail(id int64) (out *service.VoucherDetail, err error) {
	defer recoverTo(&err, "VoucherDetail")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Voucher(a.context(), id))
}

// VoucherLineRequest 是界面提交的一条分录。
//
// ★ 金额用**字符串「元」**从界面传过来，由这里转成「分」。
//
// 为什么不让界面自己转：JS 的 number 是 IEEE754 双精度，
// 界面上算出来的「分」一旦经过任何浮点运算就可能差一分 ——
// 而账上差一分，试算平衡就不成立。
// 界面只负责把用户敲的原文传过来，解析与换算全部在 Go 侧完成，
// 且走的是严格十进制的 money.Parse。
//
// ★ 用指针接收可选的辅助核算 id：JSON 里 `0` 与「没填」是两回事，
// 用 0 表示「没填」会让 id 恰好为 0 的数据无法表达。
type VoucherLineRequest struct {
	AccountCode string `json:"accountCode"`
	Summary     string `json:"summary"`
	// DebitYuan / CreditYuan 是「元」的字符串，如 "1,234.56"。
	DebitYuan  string `json:"debitYuan"`
	CreditYuan string `json:"creditYuan"`
	ContactID  *int64 `json:"contactId"`
	EmployeeID *int64 `json:"employeeId"`
	DeptID     *int64 `json:"deptId"`
	ProjectID  *int64 `json:"projectId"`
}

// VoucherRequest 是界面提交的一张凭证。
type VoucherRequest struct {
	ID          int64                `json:"id"`
	Word        string               `json:"word"`
	Date        string               `json:"date"`
	Remark      string               `json:"remark"`
	AttachCount int                  `json:"attachCount"`
	Lines       []VoucherLineRequest `json:"lines"`
	// CreatedBy 是制单人签章。
	CreatedBy string `json:"createdBy"`
	// PostedBy 是记账人签章。
	//
	// ★ 存草稿用不到它：记账签章是在**过账**那一刻盖上去的，
	// 而本工程只在账期结算时过账（用账期管理里填的操作人）。
	// 字段留着是为了兼容旧调用方传参，不再参与存草稿。
	PostedBy string `json:"postedBy"`
}

// toService 把界面输入转成 service 输入，同时完成「元 → 分」的换算。
func (r VoucherRequest) toService() (service.VoucherInput, error) {
	out := service.VoucherInput{
		ID: r.ID, Word: r.Word, Date: r.Date, Remark: r.Remark,
		AttachCount: r.AttachCount, CreatedBy: r.CreatedBy,
	}
	for i, l := range r.Lines {
		d, err := ParseYuan(l.DebitYuan)
		if err != nil {
			return out, fmt.Errorf("第 %d 行借方：%w", i+1, err)
		}
		c, err := ParseYuan(l.CreditYuan)
		if err != nil {
			return out, fmt.Errorf("第 %d 行贷方：%w", i+1, err)
		}
		out.Lines = append(out.Lines, service.VoucherLineInput{
			AccountCode: l.AccountCode, Summary: l.Summary,
			Debit: d, Credit: c,
			ContactID: l.ContactID, EmployeeID: l.EmployeeID,
			DeptID: l.DeptID, ProjectID: l.ProjectID,
		})
	}
	return out, nil
}

// SaveVoucher 保存一张草稿（新建或更新）。
func (a *App) SaveVoucher(req VoucherRequest) (out *service.VoucherDetail, err error) {
	defer recoverTo(&err, "SaveVoucher")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	in, cerr := req.toService()
	if cerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: cerr.Error()}
	}
	return wrap(svc.SaveVoucher(a.context(), in))
}

// ★ 这里原来有两个绑定：SaveAndPost（保存并记账）与 PostVoucher
//（把一张草稿过账）。两个都删掉了。
//
// 凭证录完只能存草稿；过账发生在账期结算，由 CloseBook 内部统一完成。
// 留着它们，界面上随时能把一张刚敲完的凭证直接记进总账 ——
// 「过账只在结算时」这条规则也就名存实亡，而它一旦名存实亡，
// 「凭证号连续」「期间完整」就又变回靠人自觉的事了。
//
// 想「把这一期记进账」只有一条路：结账。

// DeleteVoucher 删除一张草稿。
func (a *App) DeleteVoucher(id int64) (err error) {
	defer recoverTo(&err, "DeleteVoucher")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	if derr := svc.DeleteVoucher(a.context(), id); derr != nil {
		return classify(derr)
	}
	return nil
}

// ReverseVoucherRequest 是红字冲销的参数。
type ReverseVoucherRequest struct {
	ID int64 `json:"id"`
	// By 是操作人。
	By string `json:"by"`
	// Date 留空时沿用原凭证日期（保证落在同一期间）。
	Date string `json:"date,omitempty"`
}

// ReverseVoucher 红字冲销一张已过账凭证。
func (a *App) ReverseVoucher(req ReverseVoucherRequest) (out *service.VoucherDetail, err error) {
	defer recoverTo(&err, "ReverseVoucher")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.ReverseVoucher(a.context(), req.ID, req.By, req.Date))
}

// CheckVoucher 只做校验、不保存 —— 界面上的「检查一下」按钮。
//
// 让用户在保存前就知道哪里不对，而不是填了半小时才被拒绝。
func (a *App) CheckVoucher(req VoucherRequest) (out *VoucherCheckResult, err error) {
	defer recoverTo(&err, "CheckVoucher")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	in, cerr := req.toService()
	if cerr != nil {
		return &VoucherCheckResult{OK: false, Message: cerr.Error()}, nil
	}
	res, verr := svc.CheckVoucher(a.context(), in)
	if verr != nil {
		return &VoucherCheckResult{OK: false, Message: verr.Error()}, nil
	}
	return &VoucherCheckResult{OK: res.OK, Message: res.Message}, nil
}

// VoucherCheckResult 是一次凭证校验的结果。
type VoucherCheckResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// 录入辅助
// ---------------------------------------------------------------------------

// AccountOptions 返回可记账科目，供录入界面的科目选择器使用。
func (a *App) AccountOptions() (out []service.AccountOption, err error) {
	defer recoverTo(&err, "AccountOptions")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.AccountOptions(a.context()))
}

// ContactOptions 返回往来单位，供辅助核算选择器使用。
func (a *App) ContactOptions() (out []service.ContactOption, err error) {
	defer recoverTo(&err, "ContactOptions")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	opts, serr := svc.ContactOptions(a.context())
	return wrap(nonNil(opts), serr)
}

// VoucherMeta 返回录入界面的全部元数据。
//
// ★ 一次取回，而不是让界面连调三次。
// 单连接 SQLite 下，三次往返就是三次串行等待；
// 而这三份数据本来就是「打开录入界面时一起要用的」。
type VoucherMeta struct {
	Accounts []service.AccountOption `json:"accounts"`
	Contacts []service.ContactOption `json:"contacts"`
	// Words 是可选凭证字。
	Words []string `json:"words"`
	// CurrentPeriod 是当前可记账期间，供默认日期用。
	CurrentPeriod string `json:"currentPeriod"`
	Today         string `json:"today"`
}

// VoucherMetaInfo 返回凭证录入所需的元数据。
func (a *App) VoucherMetaInfo() (out *VoucherMeta, err error) {
	defer recoverTo(&err, "VoucherMetaInfo")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	ctx := a.context()
	accounts, aerr := svc.AccountOptions(ctx)
	if aerr != nil {
		return nil, classify(aerr)
	}
	contacts, cerr := svc.ContactOptions(ctx)
	if cerr != nil {
		return nil, classify(cerr)
	}
	book, berr := svc.Book(ctx)
	if berr != nil {
		return nil, classify(berr)
	}

	meta := &VoucherMeta{
		Accounts: accounts, Contacts: contacts,
		Words: []string{"记", "收", "付", "转"},
		Today: todayDate().String(),
	}
	for _, p := range book.Periods {
		if p.Status == "open" {
			meta.CurrentPeriod = p.Label
			break
		}
	}
	return meta, nil
}

// ---------------------------------------------------------------------------
// 附件
// ---------------------------------------------------------------------------

// UploadAttachmentRequest 携带一个文件的内容。
//
// 用 base64 而不是让前端传路径：WebView 里拿不到本地文件路径
// （浏览器的 File 对象出于安全不暴露路径），而账套文件可能在
// 任何位置。base64 会多占 33% 内存，但对一张几 MB 的发票 PDF
// 完全可以接受 —— 换来的是「拖进来就能存」的体验。
type UploadAttachmentRequest struct {
	VoucherID int64  `json:"voucherId"`
	FileName  string `json:"fileName"`
	// DataBase64 是文件内容的 base64 编码（可带 data: URL 前缀）。
	DataBase64 string `json:"dataBase64"`
}

// UploadAttachment 把附件挂到凭证上。
func (a *App) UploadAttachment(req UploadAttachmentRequest) (out *service.AttachmentInfo, err error) {
	defer recoverTo(&err, "UploadAttachment")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	raw := req.DataBase64
	// 去掉 data:application/pdf;base64, 前缀
	if i := strings.Index(raw, "base64,"); i >= 0 {
		raw = raw[i+len("base64,"):]
	}
	data, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if derr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: "附件内容不是合法的 base64"}
	}
	return wrap(svc.AttachToVoucher(a.context(), req.VoucherID, req.FileName, data))
}

// OpenAttachment 用系统默认程序打开一份附件。
//
// ★ 参数是 **hash 而不是路径**。
//
// 界面若能传任意路径，这个绑定就成了「打开任意文件」的入口
// （配合社工，可以诱导用户点开一个伪装成发票的可执行文件）。
// hash 只能指向账套 .files 目录里已有的文件，路径由 store 校验后派生。
func (a *App) OpenAttachment(hash string) (err error) {
	defer recoverTo(&err, "OpenAttachment")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	store, serr := svc.Attachments()
	if serr != nil {
		return classify(serr)
	}
	p, perr := store.Path(hash)
	if perr != nil {
		return &Fault{Kind: FaultInvalid, Message: "附件标识不合法"}
	}
	if !store.Exists(hash) {
		return &Fault{Kind: FaultIO, Message: "这份原件的文件已经不在账套里了"}
	}
	runtime.BrowserOpenURL(a.context(), "file://"+filepath.ToSlash(p))
	return nil
}

// AttachmentPath 返回附件的磁盘路径，供「用系统程序打开」使用。
//
// ★ hash 必须校验。这是**界面可以直接传任意字符串**的入口，
// 而返回值会被拿去用系统程序打开 —— 不校验就等于给了界面一个
// 「打开任意文件」的能力（`../../../../etc/passwd` 会原样返回）。
// 校验放在 Store.Path 里，这里就不必再记得做一次。
func (a *App) AttachmentPath(hash string) (out string, err error) {
	defer recoverTo(&err, "AttachmentPath")()
	svc, f := a.book()
	if f != nil {
		return "", f
	}
	store, serr := svc.Attachments()
	if serr != nil {
		return "", classify(serr)
	}
	p, perr := store.Path(hash)
	if perr != nil {
		return "", &Fault{Kind: FaultInvalid, Message: "附件标识不合法"}
	}
	return p, nil
}

// 保证 money 包被引用（VoucherSummary.Amount 的类型来自它）。
var _ = money.Money(0)

// AttachmentQueryRequest 是查附件的参数。
type AttachmentQueryRequest struct {
	// OwnerType 是 owner：voucher | invoice | expense_claim | expense_item。
	OwnerType string `json:"ownerType"`
	OwnerID   int64  `json:"ownerId"`
}

// Attachments 返回某单据的附件列表。
func (a *App) Attachments(req AttachmentQueryRequest) (out []service.AttachmentInfo, err error) {
	defer recoverTo(&err, "Attachments")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	owner := req.OwnerType
	if owner == "" {
		owner = "voucher"
	}
	list, serr := svc.ListAttachments(a.context(), owner, req.OwnerID)
	return wrap(nonNil(list), serr)
}

// RemoveAttachmentRequest 是解除附件关联的参数。
type RemoveAttachmentRequest struct {
	OwnerType string `json:"ownerType"`
	OwnerID   int64  `json:"ownerId"`
	Hash      string `json:"hash"`
}

// RemoveAttachment 解除某单据与附件的关系（不删磁盘文件）。
func (a *App) RemoveAttachment(req RemoveAttachmentRequest) (err error) {
	defer recoverTo(&err, "RemoveAttachment")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	owner := req.OwnerType
	if owner == "" {
		owner = "voucher"
	}
	if derr := svc.DeleteAttachment(a.context(), owner, req.OwnerID, req.Hash); derr != nil {
		return classify(derr)
	}
	return nil
}

// OrphanAttachments 返回磁盘上没有被任何单据引用的附件。
//
// 只报告不自动删：自动删用户数据的代价太大，而这个操作
// 本来就不常做，让人看一眼再决定更稳妥。
func (a *App) OrphanAttachments() (out []service.AttachmentInfo, err error) {
	defer recoverTo(&err, "OrphanAttachments")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.OrphanAttachments(a.context()))
}
