package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/statement"
)

// ---------------------------------------------------------------------------
// 往来对账单
// ---------------------------------------------------------------------------

// StatementRequest 是对账单的参数。
type StatementRequest struct {
	// ContactID 是往来单位。
	ContactID int64
	// From / To 是对账期间（YYYY-MM-DD）。
	From string `json:"from"`
	To   string `json:"to"`
	// AccountPrefix 限定科目范围；留空表示该往来单位名下的全部科目。
	AccountPrefix string
}

// StatementLineView 是对账单上的一行。
type StatementLineView struct {
	Date      string      `json:"date"`
	VoucherNo string      `json:"voucherNo"`
	Summary   string      `json:"summary"`
	Debit     money.Money `json:"debit"`
	Credit    money.Money `json:"credit"`
	Balance   money.Money `json:"balance"`
	Dir       string      `json:"dir"`
}

// StatementView 是一张对账单。
type StatementView struct {
	CompanyName      string `json:"companyName"`
	ContactID        int64  `json:"contactId"`
	ContactName      string `json:"contactName"`
	ContactKind      string `json:"contactKind"`
	ContactKindLabel string `json:"contactKindLabel"`
	ContactTaxNo     string `json:"contactTaxNo"`
	ContactAddress   string `json:"contactAddress"`

	From string `json:"from"`
	To   string `json:"to"`

	AccountCode string `json:"accountCode,omitempty"`
	AccountName string `json:"accountName,omitempty"`
	Mixed       bool   `json:"mixed"`

	Opening    money.Money `json:"opening"`
	OpeningDir string      `json:"openingDir"`

	Lines []StatementLineView `json:"lines"`

	TotalDebit  money.Money `json:"totalDebit"`
	TotalCredit money.Money `json:"totalCredit"`

	Closing      money.Money `json:"closing"`
	ClosingDir   string      `json:"closingDir"`
	ClosingUpper string      `json:"closingUpper"`

	// Summary 是一句话概览。
	Summary string `json:"summary"`
	// Text 是可直接复制/打印的纯文本版式。
	//
	// ★ 服务层就把它渲染好，而不是让界面试着用 HTML 拼一张纸：
	// 会计经常要把对账单贴进邮件或微信发给对方，纯文本比截图好用。
	Text string `json:"text"`
}

// Statement 生成一张往来对账单。
func (s *Service) Statement(ctx context.Context, req StatementRequest) (*StatementView, error) {
	if req.ContactID == 0 {
		return nil, fmt.Errorf("请选择往来单位")
	}
	s.recordView(ctx, AuditEvent{
		Action: audit.ActionViewStatement, Entity: "report",
		EntityID: "statement/" + strconv.FormatInt(req.ContactID, 10),
		Summary:  "查看往来对账单（单位 #" + strconv.FormatInt(req.ContactID, 10) + "）",
	})
	from, to, err := s.statementRange(ctx, req.From, req.To)
	if err != nil {
		return nil, err
	}
	st, _, err := s.db.Statements().BuildStatement(ctx, req.ContactID, from, to,
		strings.TrimSpace(req.AccountPrefix))
	if err != nil {
		return nil, err
	}
	// 自检：对账单是发给对方盖章的，自己先得算对
	if errs := st.Check(); len(errs) > 0 {
		return nil, fmt.Errorf("对账单内部不一致：%v", errs)
	}
	return toStatementView(st), nil
}

// statementRange 解析对账期间；留空时取当前可记账期间的起止。
func (s *Service) statementRange(ctx context.Context, from, to string) (calendar.Date, calendar.Date, error) {
	var dFrom, dTo calendar.Date
	var err error
	if strings.TrimSpace(from) != "" {
		if dFrom, err = parseDate(strings.TrimSpace(from)); err != nil {
			return dFrom, dTo, fmt.Errorf("起始日期 %q 格式不对，应为 YYYY-MM-DD", from)
		}
	}
	if strings.TrimSpace(to) != "" {
		if dTo, err = parseDate(strings.TrimSpace(to)); err != nil {
			return dFrom, dTo, fmt.Errorf("截止日期 %q 格式不对，应为 YYYY-MM-DD", to)
		}
	}
	if dFrom.Valid() && dTo.Valid() {
		if dTo.Before(dFrom) {
			return dFrom, dTo, fmt.Errorf("截止日期不能早于起始日期")
		}
		return dFrom, dTo, nil
	}
	// 缺哪端补哪端：默认取当前可记账期间
	book, err := s.Book(ctx)
	if err != nil {
		return dFrom, dTo, err
	}
	defFrom, defTo := todayDate(), todayDate()
	for _, p := range book.Periods {
		if p.Status == "open" {
			if defFrom, err = parseDate(p.From); err != nil {
				return dFrom, dTo, err
			}
			if defTo, err = parseDate(p.To); err != nil {
				return dFrom, dTo, err
			}
			break
		}
	}
	if !dFrom.Valid() {
		dFrom = defFrom
	}
	if !dTo.Valid() {
		dTo = defTo
	}
	if dTo.Before(dFrom) {
		return dFrom, dTo, fmt.Errorf("截止日期不能早于起始日期（%s < %s）", dTo, dFrom)
	}
	return dFrom, dTo, nil
}

// ActiveContactsView 是在期间内有往来发生的单位。
type ActiveContactsView struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	KindLabel string `json:"kindLabel"`
}

// ActiveContacts 返回期间内有往来发生的单位。
//
// 用于「批量开对账单」：会计到月底不需要一个个去挑，
// 系统直接告诉他这个月哪些客户有往来。
func (s *Service) ActiveContacts(ctx context.Context, from, to string) ([]ActiveContactsView, error) {
	dFrom, dTo, err := s.statementRange(ctx, from, to)
	if err != nil {
		return nil, err
	}
	list, err := s.db.Statements().ContactsWithActivity(ctx, dFrom, dTo)
	if err != nil {
		return nil, err
	}
	out := make([]ActiveContactsView, 0, len(list))
	for _, c := range list {
		out = append(out, ActiveContactsView{
			ID: c.ID, Name: c.Name, Kind: c.Kind,
			KindLabel: contactKindName(c.Kind),
		})
	}
	return out, nil
}

func toStatementView(st *statement.Statement) *StatementView {
	v := &StatementView{
		CompanyName: st.CompanyName,
		ContactName: st.ContactName, ContactKindLabel: st.ContactKindLabel,
		ContactTaxNo: st.ContactTaxNo, ContactAddress: st.ContactAddress,
		From: st.From.String(), To: st.To.String(),
		AccountCode: st.AccountCode, AccountName: st.AccountName, Mixed: st.Mixed,
		Opening: st.Opening, OpeningDir: st.OpeningDir,
		TotalDebit: st.TotalDebit, TotalCredit: st.TotalCredit,
		Closing: st.Closing, ClosingDir: st.ClosingDir,
		ClosingUpper: st.ClosingUpper,
		Summary:      st.Summary(),
		Text:         st.FormatText(),
	}
	for _, l := range st.Lines {
		v.Lines = append(v.Lines, StatementLineView{
			Date: l.Date.String(), VoucherNo: l.VoucherNo, Summary: l.Summary,
			Debit: l.Debit, Credit: l.Credit, Balance: l.Balance, Dir: l.Dir,
		})
	}
	return v
}
