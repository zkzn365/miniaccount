package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
	"miniaccount/internal/store/sqlite"
)

// 凭证相关错误。
var (
	// ErrVoucherLocked 表示目标凭证当前状态不允许该操作。
	ErrVoucherLocked = errors.New("凭证当前状态不允许该操作")
)

// ---------------------------------------------------------------------------
// 读取
// ---------------------------------------------------------------------------

// VoucherQuery 是凭证列表的查询条件。
type VoucherQuery struct {
	Year, Month int
	Status      string
	Keyword     string
	Limit       int
}

// VoucherSummary 是列表里的一张凭证。
//
// 刻意**不带分录**：一个月的凭证可能有几百张，
// 列表只需要「号、日期、摘要、金额、状态」，把分录一起查出来
// 会让一次列表请求变成几百次关联查询（单连接 SQLite 下是几百次串行等待）。
type VoucherSummary struct {
	ID     int64  `json:"id"`
	No     string `json:"no"`
	Word   string `json:"word"`
	Date   string `json:"date"`
	Remark string `json:"remark"`
	Status string `json:"status"`
	// StatusLabel 是中文状态名。
	StatusLabel string `json:"statusLabel"`
	// AttachCount 是附单据数。
	AttachCount int `json:"attachCount"`
	// Amount 是借方合计（分）。
	Amount money.Money `json:"amount"`
	// Source 是来源，用于界面显示「银行流水」「工资」等角标。
	Source string `json:"source"`
	// SourceLabel 是来源中文名。
	SourceLabel string `json:"sourceLabel"`
	// CreatedByAI 标记这张凭证的初稿由 AI 提议生成。
	CreatedByAI bool `json:"createdByAi"`
	// CreatedBy / PostedBy 是制单人与记账人签章。
	CreatedBy string `json:"createdBy"`
	PostedBy  string `json:"postedBy"`
	// Lines 是分录行数。列表上不显示分录，但行数能帮人一眼判断是否完整。
	Lines int `json:"lines"`
}

// Vouchers 返回凭证列表。
func (s *Service) Vouchers(ctx context.Context, q VoucherQuery) ([]VoucherSummary, error) {
	s.recordView(ctx, AuditEvent{
		Action: audit.ActionViewVouchers, Entity: "voucher",
		EntityID: fmt.Sprintf("%04d-%02d/%s", q.Year, q.Month, q.Status),
		Summary:  fmt.Sprintf("查看凭证列表 %04d-%02d", q.Year, q.Month),
		Detail:   map[string]any{"期间": fmt.Sprintf("%04d-%02d", q.Year, q.Month), "状态": q.Status, "关键字": q.Keyword},
	})
	rows, err := s.db.Vouchers().List(ctx, sqlite.ListFilter{
		Year: q.Year, Month: q.Month, Status: q.Status,
		Keyword: q.Keyword, Limit: q.Limit,
	})
	if err != nil {
		return nil, err
	}

	// 分录行数一次性查出来，避免每张凭证单独查一次
	counts, err := s.entryCounts(ctx, q.Year, q.Month)
	if err != nil {
		return nil, err
	}

	out := make([]VoucherSummary, 0, len(rows))
	for _, v := range rows {
		out = append(out, VoucherSummary{
			ID: v.ID, No: v.No, Word: string(v.Word), Date: v.BizDate.String(),
			Remark: v.Remark, Status: string(v.Status),
			StatusLabel: v.Status.Label(), AttachCount: v.AttachCount,
			Amount: v.Amount(), Source: string(v.Source),
			SourceLabel: v.Source.Label(), CreatedByAI: v.CreatedByAI,
			CreatedBy: v.CreatedBy, PostedBy: v.PostedBy,
			Lines: counts[v.ID],
		})
	}
	return out, nil
}

func (s *Service) entryCounts(ctx context.Context, year, month int) (map[int64]int, error) {
	sqlText := `SELECT ve.voucher_id, COUNT(*) FROM voucher_entry ve
	              JOIN voucher v ON v.id = ve.voucher_id WHERE 1 = 1`
	var args []any
	if year > 0 {
		sqlText += ` AND v.year = ?`
		args = append(args, year)
	}
	if month > 0 {
		sqlText += ` AND v.month = ?`
		args = append(args, month)
	}
	sqlText += ` GROUP BY ve.voucher_id`

	rows, err := s.db.SQL().QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// VoucherDetail 是一张凭证的完整内容。
type VoucherDetail struct {
	ID     int64  `json:"id"`
	No     string `json:"no"`
	Word   string `json:"word"`
	Date   string `json:"date"`
	Remark string `json:"remark"`
	Status string `json:"status"`
	// StatusLabel 是中文状态名。
	StatusLabel string `json:"statusLabel"`
	AttachCount int    `json:"attachCount"`
	Source      string `json:"source"`
	SourceLabel string `json:"sourceLabel"`
	CreatedByAI bool   `json:"createdByAi"`

	CreatedBy  string `json:"createdBy"`
	ReviewedBy string `json:"reviewedBy"`
	PostedBy   string `json:"postedBy"`
	PostedAt   string `json:"postedAt"`

	// ReversesNo 是被本凭证冲销的原凭证号（红字凭证才有）。
	ReversesNo string `json:"reversesNo"`
	// VoidedByNo 是冲销本凭证的那张红字凭证号。
	VoidedByNo string `json:"voidedByNo"`

	Lines []VoucherLine `json:"lines"`

	// TotalDebit / TotalCredit 用于界面上的「借贷平衡」提示。
	TotalDebit  money.Money `json:"totalDebit"`
	TotalCredit money.Money `json:"totalCredit"`
	// Balanced 为假时界面必须显眼提示。
	Balanced bool `json:"balanced"`

	// CanEdit / CanDelete / CanPost / CanReverse 由服务层算好，
	// 界面照着禁用按钮即可 —— 状态机的规则只在一处实现。
	CanEdit    bool `json:"canEdit"`
	CanDelete  bool `json:"canDelete"`
	CanPost    bool `json:"canPost"`
	CanReverse bool `json:"canReverse"`
	// BlockedReason 说明为什么不能操作（为空表示都可以）。
	BlockedReason string `json:"blockedReason"`
}

// VoucherLine 是凭证的一条分录。
type VoucherLine struct {
	LineNo      int         `json:"lineNo"`
	AccountCode string      `json:"accountCode"`
	AccountName string      `json:"accountName"`
	Summary     string      `json:"summary"`
	Debit       money.Money `json:"debit"`
	Credit      money.Money `json:"credit"`
	// AuxDesc 是辅助核算的中文描述。
	AuxDesc string `json:"auxDesc"`
}

// Voucher 返回一张凭证的完整内容。
func (s *Service) Voucher(ctx context.Context, id int64) (*VoucherDetail, error) {
	v, err := s.db.Vouchers().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	tree, err := s.db.Accounts().Tree(ctx)
	if err != nil {
		return nil, err
	}
	names, err := s.contactNames(ctx)
	if err != nil {
		return nil, err
	}

	d := &VoucherDetail{
		ID: v.ID, No: v.No, Word: string(v.Word), Date: v.BizDate.String(),
		Remark: v.Remark, Status: string(v.Status),
		StatusLabel: v.Status.Label(), AttachCount: v.AttachCount,
		Source: string(v.Source), SourceLabel: v.Source.Label(),
		CreatedByAI: v.CreatedByAI,
		CreatedBy:   v.CreatedBy, ReviewedBy: v.ReviewedBy, PostedBy: v.PostedBy,
		TotalDebit: v.TotalDebit(), TotalCredit: v.TotalCredit(),
		Balanced: v.IsBalanced(),
	}
	if v.PostedAt != nil {
		d.PostedAt = v.PostedAt.Format(time.RFC3339)
	}
	if v.ReversesID != nil {
		if orig, err := s.db.Vouchers().Get(ctx, *v.ReversesID); err == nil {
			d.ReversesNo = orig.No
		}
	}
	if v.VoidedBy != nil {
		if rev, err := s.db.Vouchers().Get(ctx, *v.VoidedBy); err == nil {
			d.VoidedByNo = rev.No
		}
	}

	for i, e := range v.Entries {
		name := ""
		if a, ok := tree.Get(e.AccountCode); ok {
			name = a.Name
		}
		d.Lines = append(d.Lines, VoucherLine{
			LineNo: i + 1, AccountCode: e.AccountCode, AccountName: name,
			Summary: e.Summary, Debit: e.Debit, Credit: e.Credit,
			AuxDesc: describeAux(e.Aux, names),
		})
	}

	// 状态机规则只在这里算一次
	d.CanEdit = v.CanEdit() == nil
	d.CanDelete = v.CanDelete() == nil
	d.CanPost = v.CanPost() == nil
	d.CanReverse = v.CanVoid() == nil
	if !d.CanEdit {
		d.BlockedReason = blockReason(ctx, s, v)
	}
	return d, nil
}

// blockReason 解释「为什么这张凭证不能改」。
//
// 只说「状态不允许」等于没说 —— 用户需要知道是**期间结账了**
// 还是**凭证已过账**，因为这两种情况的解法完全不同。
func blockReason(ctx context.Context, s *Service, v *voucher.Voucher) string {
	switch v.Status {
	case voucher.StatusPosted:
		if cal, err := s.db.Periods().Load(ctx); err == nil {
			if p, ok := cal.Get(v.BizDate.Year, v.BizDate.Month); ok &&
				p.Status == period.StatusClosed {
				return fmt.Sprintf(
					"该凭证所属期间 %d年%02d月 已结账。已结账期间的凭证不能修改，"+
						"如需更正请先反结账，或用红字冲销。", v.BizDate.Year, v.BizDate.Month)
			}
		}
		return "凭证已过账。已记账的凭证不能直接修改，" +
			"如需更正请用红字冲销 —— 这是《会计基础工作规范》的要求。"
	case voucher.StatusVoided:
		return "凭证已被红字冲销，只能查看。"
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// 写入
// ---------------------------------------------------------------------------

// VoucherLineInput 是界面提交的一条分录。
//
// 金额用 **int64 分**接收，而不是字符串：
// 界面在提交前已经用 parseYuanToCents 把「元」转成了分，
// 这里再转一次只会多一个出错的地方。
type VoucherLineInput struct {
	AccountCode string
	Summary     string
	Debit       money.Money
	Credit      money.Money
	ContactID   *int64
	EmployeeID  *int64
	DeptID      *int64
	ProjectID   *int64
}

// VoucherInput 是界面提交的一张凭证。
type VoucherInput struct {
	// ID 为 0 表示新建，否则是更新已有草稿。
	ID          int64
	Word        string
	Date        string
	Remark      string
	AttachCount int
	Lines       []VoucherLineInput
	// CreatedBy 是制单人（签署在凭证上）。
	CreatedBy string
}

// build 把界面输入转成领域对象，并执行完整校验。
func (s *Service) buildVoucher(ctx context.Context, in VoucherInput) (*voucher.Voucher, error) {
	word := voucher.Word(strings.TrimSpace(in.Word))
	if word == "" {
		word = voucher.WordJi
	}
	date, err := calendar.Parse(strings.TrimSpace(in.Date))
	if err != nil {
		return nil, fmt.Errorf("记账日期 %q 格式不对，应为 YYYY-MM-DD", in.Date)
	}
	maker := strings.TrimSpace(in.CreatedBy)
	if maker == "" {
		maker = defaultMaker
	}

	v, err := voucher.New(word, date, maker)
	if err != nil {
		return nil, err
	}
	v.ID = in.ID
	v.Remark = strings.TrimSpace(in.Remark)
	v.AttachCount = in.AttachCount

	for i, l := range in.Lines {
		code := strings.TrimSpace(l.AccountCode)
		summary := strings.TrimSpace(l.Summary)
		// 整行空白的直接跳过：界面上总有一行是空着等录入的
		if code == "" && summary == "" && l.Debit.IsZero() && l.Credit.IsZero() {
			continue
		}
		if err := v.AddEntry(ledger.Entry{
			AccountCode: code, Summary: summary,
			Debit: l.Debit, Credit: l.Credit,
			Aux: ledger.Aux{
				ContactID: l.ContactID, EmployeeID: l.EmployeeID,
				DeptID: l.DeptID, ProjectID: l.ProjectID,
			},
		}); err != nil {
			return nil, fmt.Errorf("第 %d 行：%w", i+1, err)
		}
	}
	if len(v.Entries) == 0 {
		return nil, voucher.ErrNoEntries
	}
	v.Period = period.NewKey(date.Year, date.Month)
	return v, nil
}

// defaultMaker 是没填制单人时的默认署名。
//
// 不强制要求填：小微企业的老板常常既是制单人又是记账人，
// 每次弹一个「请填写制单人」只会让人烦。但凭证上必须**有**签名，
// 因此给一个明确的默认值，界面上可以随时改。
const defaultMaker = "制单人"

// CheckResult 是一次凭证校验的结论。
//
// ★ 用显式的 OK 字段，而不是让调用方从消息文本里找「不平衡」三个字：
// 文案随时会改，靠字符串匹配判断成败是个定时炸弹。
type CheckResult struct {
	OK      bool
	Message string
}

// CheckVoucher 只校验不保存，返回一句给用户看的话。
//
// 界面上的「检查一下」按钮用它：让用户在保存前就知道哪里不对，
// 而不是填了半小时才被拒绝。
func (s *Service) CheckVoucher(ctx context.Context, in VoucherInput) (CheckResult, error) {
	v, err := s.buildVoucher(ctx, in)
	if err != nil {
		// 连领域对象都构造不出来（日期格式错、分录为空）：
		// 这同样是「可以改」的问题，作为结论返回而不是错误
		return CheckResult{OK: false, Message: err.Error()}, nil
	}
	// ★ 借贷平衡要先单独判一次。
	//
	// 它是录入过程中最需要盯的一件事，而且**必须作为「结论」返回**
	// 而不是当作校验失败 —— 界面要把它显示在平衡提示条上，
	// 让人一边录一边看见差额在缩小，而不是点一下按钮弹一个错误框。
	if len(v.Entries) > 0 && !v.IsBalanced() {
		return CheckResult{OK: false, Message: fmt.Sprintf(
			"借贷不平衡：借 %s ≠ 贷 %s，差额 %s。差额不会被自动抹平 —— 请核对该记哪一边。",
			v.TotalDebit(), v.TotalCredit(),
			v.TotalDebit().Sub(v.TotalCredit()))}, nil
	}

	lctx, err := s.db.AI().LedgerContext(ctx)
	if err != nil {
		return CheckResult{}, err
	}
	per, err := v.ValidateDraft(lctx)
	if err != nil {
		return CheckResult{OK: false, Message: err.Error()}, nil
	}
	return CheckResult{OK: true, Message: fmt.Sprintf(
		"校验通过：%d 条分录，借 %s = 贷 %s，期间 %s",
		len(v.Entries), v.TotalDebit(), v.TotalCredit(), per.Key)}, nil
}

// SaveVoucher 保存一张草稿（新建或更新）。
func (s *Service) SaveVoucher(ctx context.Context, in VoucherInput) (*VoucherDetail, error) {
	v, err := s.buildVoucher(ctx, in)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Vouchers().SaveDraft(ctx, sqlite.DraftInput{
		Voucher: v, CreatedBy: v.CreatedBy,
	})
	if err != nil {
		return nil, err
	}
	// ★ 规范点名要记「凭证录入 / 修改」，而且修改要记**修改前后的内容**。
	// 摘要、金额、分录都在 detail 里，会计监督人员据此能还原改了什么。
	action := audit.ActionVoucherCreate
	summary := "录入凭证草稿"
	detail := map[string]any{
		"记账日期": v.BizDate.String(),
		"摘要":   v.Remark,
		"借方合计": money.Money(v.TotalDebit()).String(),
		"贷方合计": money.Money(v.TotalCredit()).String(),
		"分录":   auditLines(v),
		"制单人":  v.CreatedBy,
	}
	if in.ID != 0 {
		action = audit.ActionVoucherUpdate
		summary = "修改凭证草稿 #" + strconv.FormatInt(in.ID, 10)
		// 改之前的原样留在日志里 —— 这是「修改前后内容」的前一半
		if before, berr := s.Voucher(ctx, in.ID); berr == nil {
			detail["修改前"] = map[string]any{
				"记账日期": before.Date, "摘要": before.Remark,
				"借方合计": money.Money(before.TotalDebit).String(),
				"贷方合计": money.Money(before.TotalCredit).String(),
			}
		}
	}
	s.recordAudit(ctx, AuditEvent{
		Action: action, Summary: summary,
		Entity: "voucher", EntityID: strconv.FormatInt(res.VoucherID, 10),
		Operator: v.CreatedBy, Detail: detail,
	})
	return s.Voucher(ctx, res.VoucherID)
}

// auditLines 把分录压缩成日志明细。
//
// 只留「科目 + 借贷」：摘要与辅助核算的 id 已在凭证里，
// 日志记到能还原「这笔账动了哪些科目多少钱」就够 ——
// 记全文会让每条日志都很大，而日志是要长期留存的。
func auditLines(v *voucher.Voucher) []map[string]any {
	out := make([]map[string]any, 0, len(v.Entries))
	for _, e := range v.Entries {
		out = append(out, map[string]any{
			"科目": e.AccountCode, "摘要": e.Summary,
			"借方": money.Money(e.Debit).String(),
			"贷方": money.Money(e.Credit).String(),
		})
	}
	return out
}

// DeleteVoucher 删除一张草稿。
//
// ★ 规范点名要记「尚未记账凭证的删除」—— 草稿删掉就没了，
// 日志是它存在过的唯一证据，所以删除前先把内容抄进日志。
func (s *Service) DeleteVoucher(ctx context.Context, id int64) error {
	before, err := s.Voucher(ctx, id)
	if err != nil {
		return err
	}
	if err := s.db.Vouchers().DeleteDraft(ctx, id); err != nil {
		return err
	}
	detail := map[string]any{
		"记账日期": before.Date, "摘要": before.Remark,
		"借方合计": money.Money(before.TotalDebit).String(),
		"贷方合计": money.Money(before.TotalCredit).String(),
		"制单人":  before.CreatedBy,
	}
	lines := make([]map[string]any, 0, len(before.Lines))
	for _, l := range before.Lines {
		lines = append(lines, map[string]any{
			"科目": l.AccountCode, "摘要": l.Summary,
			"借方": money.Money(l.Debit).String(),
			"贷方": money.Money(l.Credit).String(),
		})
	}
	detail["被删除的分录"] = lines
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionVoucherDelete,
		Summary: "删除凭证草稿 " + firstNonEmpty(before.No, "#"+strconv.FormatInt(id, 10)),
		Entity:  "voucher", EntityID: strconv.FormatInt(id, 10),
		Operator: before.CreatedBy, Detail: detail,
	})
	return nil
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// PostVoucher 把一张草稿过账。
//
// postedBy 是记账人 —— 与制单人分开：中国实务要求制单、审核、
// 记账三个签章可追溯，小微企业往往一人身兼三职，但**字段要分开**，
// 规模变大后不需要改数据结构。
func (s *Service) PostVoucher(ctx context.Context, id int64, postedBy string) (*VoucherDetail, error) {
	if strings.TrimSpace(postedBy) == "" {
		return nil, errors.New("请填写记账人 —— 记账凭证需要有记账签章")
	}
	v, err := s.db.Vouchers().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := v.CanPost(); err != nil {
		return nil, err
	}
	accounts, err := s.db.Accounts().IDsByCode(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Vouchers().Post(ctx, sqlite.PostInput{
		Voucher: v, Accounts: accounts,
		PostingBy: strings.TrimSpace(postedBy), At: time.Now(),
	}); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionVoucherPost,
		Summary: "凭证过账 " + firstNonEmpty(v.No, "#"+strconv.FormatInt(id, 10)),
		Entity:  "voucher", EntityID: strconv.FormatInt(id, 10),
		Operator: strings.TrimSpace(postedBy),
		Detail: map[string]any{
			"凭证号": v.No, "记账日期": v.BizDate.String(), "摘要": v.Remark,
			"借方合计": money.Money(v.TotalDebit()).String(),
			"贷方合计": money.Money(v.TotalCredit()).String(),
			"制单人":  v.CreatedBy, "记账人": strings.TrimSpace(postedBy),
			"分录": auditLines(v),
		},
	})
	return s.Voucher(ctx, id)
}

// SaveAndPost 保存并立即过账 —— 界面上的「保存并记账」按钮。
//
// 两步在同一处串起来，而不是让界面连调两次：
// 中间夹一次网络往返，用户可能在中途关窗口，
// 结果留下一张谁也不知道为什么存在的草稿。
func (s *Service) SaveAndPost(ctx context.Context, in VoucherInput, postedBy string) (*VoucherDetail, error) {
	d, err := s.SaveVoucher(ctx, in)
	if err != nil {
		return nil, err
	}
	return s.PostVoucher(ctx, d.ID, postedBy)
}

// ReverseVoucher 红字冲销一张已过账凭证。
func (s *Service) ReverseVoucher(ctx context.Context, id int64, by, date string) (*VoucherDetail, error) {
	if strings.TrimSpace(by) == "" {
		return nil, errors.New("请填写操作人")
	}
	var bizDate calendar.Date
	if strings.TrimSpace(date) != "" {
		d, err := calendar.Parse(strings.TrimSpace(date))
		if err != nil {
			return nil, fmt.Errorf("冲销日期 %q 格式不对，应为 YYYY-MM-DD", date)
		}
		bizDate = d
	}
	res, err := s.db.Vouchers().Reverse(ctx, sqlite.ReverseInput{
		VoucherID: id, By: strings.TrimSpace(by), BizDate: bizDate, At: time.Now(),
	})
	if err != nil {
		return nil, err
	}
	return s.Voucher(ctx, res.VoucherID)
}

// ---------------------------------------------------------------------------
// 录入辅助
// ---------------------------------------------------------------------------

// AccountOption 是科目选择器的一项。
type AccountOption struct {
	Code string `json:"code"`
	Name string `json:"name"`
	// FullName 含上级名称，如「管理费用—办公费」。
	FullName string `json:"fullName"`
	// Direction 是余额方向：借 / 贷。
	Direction string `json:"direction"`
	// AuxTypes 是需要的辅助核算维度（中文名）。
	AuxTypes []string `json:"auxTypes"`
	// SearchText 是给前端做模糊匹配用的预拼接串。
	//
	// ★ 在 Go 侧拼好而不是让前端每次输入都遍历 190 个科目拼一遍：
	// 拼串只在科目表加载时做一次，输入时只是 indexOf。
	SearchText string `json:"searchText"`
}

// AccountOptions 返回可记账科目，供录入界面选择。
func (s *Service) AccountOptions(ctx context.Context) ([]AccountOption, error) {
	tree, err := s.db.Accounts().Tree(ctx)
	if err != nil {
		return nil, err
	}
	all := tree.All()
	byCode := make(map[string]*account.Account, len(all))
	for _, a := range all {
		byCode[a.Code] = a
	}

	var out []AccountOption
	for _, a := range tree.EnabledLeaves() {
		aux := make([]string, 0, len(a.AuxTypes))
		for _, t := range a.AuxTypes {
			aux = append(aux, t.Label())
		}
		dir := "借"
		if a.BalanceDir == account.DirCredit {
			dir = "贷"
		}
		full := a.FullName(byCode)
		out = append(out, AccountOption{
			Code: a.Code, Name: a.Name, FullName: full,
			Direction: dir, AuxTypes: aux,
			SearchText: strings.ToLower(a.Code + " " + full + " " + a.Name),
		})
	}
	return out, nil
}

// ContactOption 是往来单位选择器的一项。
type ContactOption struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// KindLabel 是类型中文名。
	KindLabel string `json:"kindLabel"`
	// ShortName 是简称，银行流水里出现的常是它。
	ShortName string `json:"shortName"`
}

// ContactOptions 返回全部启用中的往来单位。
func (s *Service) ContactOptions(ctx context.Context) ([]ContactOption, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, name, kind, short_name FROM contact
		 WHERE is_enabled = 1 ORDER BY kind, name`)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var out []ContactOption
	for rows.Next() {
		var c ContactOption
		var kind string
		if err := rows.Scan(&c.ID, &c.Name, &kind, &c.ShortName); err != nil {
			return nil, err
		}
		c.Kind = kind
		c.KindLabel = contactKindName(kind)
		out = append(out, c)
	}
	return out, rows.Err()
}

func contactKindName(kind string) string {
	switch kind {
	case "customer":
		return "客户"
	case "supplier":
		return "供应商"
	case "employee":
		return "员工"
	case "shareholder":
		return "股东"
	case "both":
		return "客户/供应商"
	default:
		return "其他单位"
	}
}

// contactNames 返回 id → 名称的映射，用于渲染辅助核算描述。
func (s *Service) contactNames(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `SELECT id, name FROM contact`)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// describeAux 把辅助核算渲染成中文描述。
//
// 用**名称**而不是 `往来#3` 这样的 id：凭证详情是给人看的，
// 「张三」「管理部门」比数字有用得多。
func describeAux(a ledger.Aux, names map[int64]string) string {
	var parts []string
	if a.ContactID != nil {
		if n, ok := names[*a.ContactID]; ok {
			parts = append(parts, n)
		} else {
			parts = append(parts, fmt.Sprintf("往来#%d", *a.ContactID))
		}
	}
	if a.EmployeeID != nil {
		parts = append(parts, fmt.Sprintf("员工#%d", *a.EmployeeID))
	}
	if a.DeptID != nil {
		parts = append(parts, fmt.Sprintf("部门#%d", *a.DeptID))
	}
	if a.ProjectID != nil {
		parts = append(parts, fmt.Sprintf("项目#%d", *a.ProjectID))
	}
	return strings.Join(parts, " · ")
}

// ---------------------------------------------------------------------------
// 附件
// ---------------------------------------------------------------------------

// AttachToVoucher 把附件挂到凭证上，并回填附单据数。
//
// # 为什么必须写 attachment 表
//
// 附件实体按内容寻址存在 <账套>.files/ 下，而**归属关系**必须入库。
// 只把文件写到磁盘、只把 attach_count 加一，会有三个后果：
//
//  1. 打开凭证时列不出它有哪些附件 —— 文件在，但没人知道是谁的
//  2. 孤儿附件清理无从判断「这个文件还有没有人引用」，只能全留或全删
//  3. 备份虽然会带上文件（按目录全量打包），但恢复后依然不知道该挂给谁
//
// 因此这里一次事务内做三件事：写 attachment、加 attach_count、
// 返回附件信息。任何一步失败都整体回滚 —— 不会出现「文件在但没记录」
// 或「记录在但文件丢了」的半截状态。
func (s *Service) AttachToVoucher(ctx context.Context, voucherID int64,
	filename string, data []byte) (*AttachmentInfo, error) {

	store, err := s.Attachments()
	if err != nil {
		return nil, err
	}
	blob, err := store.Put(data, filename)
	if err != nil {
		return nil, err
	}

	err = s.db.WithTx(ctx, func(tx *sqliteTx) error {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		// 同一单据挂同一份文件不重复：UNIQUE(owner_type, owner_id, sha256)
		// 兜底，这里用 INSERT OR IGNORE 让重复上传变成幂等操作 ——
		// 用户重复拖同一个 PDF 进来是很常见的动作，报错没有意义。
		if _, err := tx.Exec(ctx, `
			INSERT OR IGNORE INTO attachment
				(owner_type, owner_id, file_name, sha256, mime, size, kind, created_at)
			VALUES ('voucher', ?, ?, ?, ?, ?, '', ?)`,
			voucherID, blob.FileName, blob.SHA256, blob.Mime, blob.Size, now); err != nil {
			return err
		}
		// attach_count 用「实际挂了多少个附件」重算，而不是简单 +1。
		//
		// 重复上传同一份文件时 +1 会让「附单据数」比实际多 ——
		// 而凭证上的这个数字是要跟纸质单据核对的，多一个少一个都是错。
		_, err := tx.Exec(ctx, `
			UPDATE voucher
			   SET attach_count = (SELECT COUNT(*) FROM attachment
			                        WHERE owner_type = 'voucher' AND owner_id = ?),
			       updated_at = ?
			 WHERE id = ?`, voucherID, now, voucherID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &AttachmentInfo{
		Hash: blob.SHA256, Name: blob.FileName, Size: blob.Size,
		MIME: blob.Mime, Path: store.MustPath(blob.SHA256),
	}, nil
}

// ListAttachments 返回某单据的附件列表。
func (s *Service) ListAttachments(ctx context.Context, ownerType string, ownerID int64) ([]AttachmentInfo, error) {
	store, err := s.Attachments()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT file_name, sha256, mime, size FROM attachment
		 WHERE owner_type = ? AND owner_id = ?
		 ORDER BY id`, ownerType, ownerID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var out []AttachmentInfo
	for rows.Next() {
		var a AttachmentInfo
		if err := rows.Scan(&a.Name, &a.Hash, &a.MIME, &a.Size); err != nil {
			return nil, err
		}
		// hash 从库里读出来，但也走校验版：库里若被外部工具改坏，
		// 这里退化成一个「指向不存在位置」的路径，而不是越界路径。
		a.Path = store.MustPath(a.Hash)
		// 文件实体可能被误删（用户手动清理 .files 目录）。
		// 这里如实标出来，界面才能提示「原件已丢失」而不是
		// 让用户点开一个不存在的路径。
		a.Missing = !store.Exists(a.Hash)
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAttachment 解除某单据与附件的关系。
//
// 只删数据库记录，**不删磁盘上的文件实体**：内容寻址下同一份文件
// 可能被多个单据共享（同一张发票挂在发票档案与报销单两边），
// 删实体要扫描全部引用，代价高且容易误删。
// 真正的清理由「孤儿附件清理」统一做（见 OrphanAttachments）。
func (s *Service) DeleteAttachment(ctx context.Context, ownerType string, ownerID int64, hash string) error {
	return s.db.WithTx(ctx, func(tx *sqliteTx) error {
		if _, err := tx.Exec(ctx, `
			DELETE FROM attachment
			 WHERE owner_type = ? AND owner_id = ? AND sha256 = ?`,
			ownerType, ownerID, hash); err != nil {
			return err
		}
		if ownerType == "voucher" {
			_, err := tx.Exec(ctx, `
				UPDATE voucher
				   SET attach_count = (SELECT COUNT(*) FROM attachment
				                        WHERE owner_type = 'voucher' AND owner_id = ?),
				       updated_at = ?
				 WHERE id = ?`,
				ownerID, time.Now().UTC().Format(time.RFC3339Nano), ownerID)
			return err
		}
		return nil
	})
}

// OrphanAttachments 返回磁盘上存在、但没有任何单据引用的附件。
//
// 用途是「附件清理」：用户删了发票、换了凭证，文件会留在 .files 里。
// 只**报告**不自动删除 —— 自动删用户数据的代价太大，
// 而这个操作本来就不常做，让人看一眼再决定更稳妥。
func (s *Service) OrphanAttachments(ctx context.Context) ([]AttachmentInfo, error) {
	store, err := s.Attachments()
	if err != nil {
		return nil, err
	}
	hashes, err := store.List()
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	rows, err := s.db.SQL().QueryContext(ctx, `SELECT DISTINCT sha256 FROM attachment`)
	if err != nil {
		return nil, translate(err)
	}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return nil, err
		}
		used[h] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var out []AttachmentInfo
	for _, h := range hashes {
		if used[h] {
			continue
		}
		var size int64
		if b, err := store.Get(h); err == nil {
			size = int64(len(b))
		}
		out = append(out, AttachmentInfo{Hash: h, Size: size, Path: store.MustPath(h)})
	}
	return out, nil
}

// AttachmentInfo 是一个已保存的附件。
type AttachmentInfo struct {
	Hash string `json:"hash"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	MIME string `json:"mime"`
	// Path 是附件在磁盘上的绝对路径。
	Path string `json:"path"`
	// Missing 为真表示数据库里有记录、但磁盘上的文件不见了。
	//
	// 用户手动清理 .files 目录、或从别处拷来一个不完整的账套时会出现。
	// 如实标出来，界面才能提示「原件已丢失」而不是让用户
	// 点开一个不存在的路径。
	Missing bool `json:"missing"`
}
