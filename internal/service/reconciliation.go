package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/reconciliation"
	"miniaccount/internal/export"
)

// ---------------------------------------------------------------------------
// 银行存款余额调节表
// ---------------------------------------------------------------------------

// ReconciliationRequest 是余额调节表的参数。
type ReconciliationRequest struct {
	// AccountCode 是银行科目编码，默认 1002（银行存款）。
	AccountCode string
	// AsOf 是截止日期（YYYY-MM-DD）；留空取当前可记账期间的期末。
	AsOf string
	// From 是对账窗口起点（YYYY-MM-DD，含）；留空自动取对账单覆盖的第一天。
	From string
	// BankBalance 是银行对账单上的期末余额（分）。
	//
	// 留空时自动取导入的流水里最后一条带余额的记录。之所以允许手工传：
	// 银行对账单的期末余额是「银行说了算」的外部事实，
	// 有的客户拿不到电子流水，只能照纸质对账单敲一个数进来。
	BankBalance *int64
}

// ReconciliationItemView 是一条未达账项。
type ReconciliationItemView struct {
	Date      string      `json:"date"`
	Days      int         `json:"days"`
	Summary   string      `json:"summary"`
	Reference string      `json:"reference"`
	Amount    money.Money `json:"amount"`
}

// ReconciliationLineView 是调节表上的一行。
type ReconciliationLineView struct {
	Side     string      `json:"side"`
	Kind     string      `json:"kind"`
	Label    string      `json:"label"`
	Amount   money.Money `json:"amount"`
	Subtotal bool        `json:"subtotal"`
}

// ReconciliationView 是一张银行存款余额调节表。
type ReconciliationView struct {
	AccountCode string `json:"accountCode"`
	AccountName string `json:"accountName"`
	AsOf        string `json:"asOf"`
	From        string `json:"from"`

	// BookOpening / BankOpening 是窗口之前的期初余额。
	// BankOpening 为 nil 表示流水里没有余额列，推不出银行期初。
	BookOpening money.Money  `json:"bookOpening"`
	BankOpening *money.Money `json:"bankOpening"`
	// OpeningDiff = 账面期初 − 银行期初。两侧调节后若不等，
	// 差额必然恰好等于它 —— 界面应先把这句话告诉用户。
	OpeningDiff money.Money `json:"openingDiff"`

	BookBalance  money.Money `json:"bookBalance"`
	BookAdjusted money.Money `json:"bookAdjusted"`

	// BankBalance / BankAdjusted 为 nil 表示没导入对账单，
	// 此时只能算出企业侧的调节结果，无法判定账实是否相符。
	BankBalance  *money.Money `json:"bankBalance"`
	BankAdjusted *money.Money `json:"bankAdjusted"`

	BankReceivedNotBooked []ReconciliationItemView `json:"bankReceivedNotBooked"`
	BankPaidNotBooked     []ReconciliationItemView `json:"bankPaidNotBooked"`
	BookReceivedNotBanked []ReconciliationItemView `json:"bookReceivedNotBanked"`
	BookPaidNotBanked     []ReconciliationItemView `json:"bookPaidNotBanked"`

	Lines []ReconciliationLineView `json:"lines"`

	UnreconciledFlows int `json:"unreconciledFlows"`
	// Status 是三态调节结果：balanced / unbalanced / unknown。
	//
	// ★ 界面必须看它、而不是 Balanced —— Balanced 在没导入对账单时
	// 为 true（无从判断），照它画绿勾就是谎报账实相符。
	Status      string      `json:"status"`
	StatusLabel string      `json:"statusLabel"`
	Balanced    bool        `json:"balanced"`
	Difference  money.Money `json:"difference"`
	Summary     string      `json:"summary"`
	Notes       []string    `json:"notes"`
}

// UnreconciledCount 是未达账项的总条数。
func (v *ReconciliationView) UnreconciledCount() int {
	if v == nil {
		return 0
	}
	return len(v.BankReceivedNotBooked) + len(v.BankPaidNotBooked) +
		len(v.BookReceivedNotBanked) + len(v.BookPaidNotBanked)
}

// Reconciliation 生成银行存款余额调节表。
func (s *Service) Reconciliation(ctx context.Context, req ReconciliationRequest) (*ReconciliationView, error) {
	s.recordView(ctx, AuditEvent{
		Action: audit.ActionViewReconciliation, Entity: "report",
		EntityID: "recon", Summary: "查看银行存款余额调节表",
	})
	rep, err := s.reconReport(ctx, req)
	if err != nil {
		return nil, err
	}
	return toReconciliationView(rep), nil
}

// reconReport 是界面与导出**共用**的取数路径。
//
// 两条路走同一个函数、返回同一个领域对象，是「屏幕上看到的」
// 与「导出文件里的」永远一致的结构性保证 —— 靠约定去维护一致性
// 在会计软件里迟早会破。
func (s *Service) reconReport(ctx context.Context,
	req ReconciliationRequest) (*reconciliation.Report, error) {
	_, end, err := s.statementRange(ctx, req.AsOf, req.AsOf)
	if err != nil {
		return nil, err
	}

	code := strings.TrimSpace(req.AccountCode)
	if code == "" {
		code = "1002"
	}

	// 窗口起点：留空时由仓储按对账单覆盖范围自动取
	var start calendar.Date
	if f := strings.TrimSpace(req.From); f != "" {
		d, err := calendar.Parse(f)
		if err != nil {
			return nil, fmt.Errorf("对账起始日 %q 无效（应为 YYYY-MM-DD）：%w", f, err)
		}
		if d.After(end) {
			return nil, fmt.Errorf("对账起始日 %s 不能晚于截止日 %s", d, end)
		}
		start = d
	}

	var bankBal *money.Money
	if req.BankBalance != nil {
		m := money.Money(*req.BankBalance)
		bankBal = &m
	}

	return s.db.Reconciliation().BuildReconciliationFrom(ctx, code, start, end, bankBal)
}

// ExportReconciliation 把余额调节表导出为 Excel。
//
// ★ 刻意接收与 Reconciliation 完全相同的请求对象，
// 而不是复用 ExportExcel 的期间参数。理由：这张表的数字**依赖用户的输入**
// （截止日、起始日、手工敲的银行余额）。两条取数路径一旦分家，
// 用户导出的文件就可能和屏幕上看到的不是同一张表 ——
// 而这种不一致在会计场景里是最不能容忍的。
func (s *Service) ExportReconciliation(ctx context.Context,
	req ReconciliationRequest, dest string) (*ExportResult, error) {

	dest = strings.TrimSpace(dest)
	if dest == "" {
		return nil, fmt.Errorf("请选择导出位置")
	}
	if !strings.HasSuffix(strings.ToLower(dest), ".xlsx") {
		dest += ".xlsx"
	}

	rep, err := s.reconReport(ctx, req)
	if err != nil {
		return nil, err
	}
	book, err := s.Book(ctx)
	if err != nil {
		return nil, err
	}
	if err := export.Reconciliation(dest, book.CompanyName, rep); err != nil {
		return nil, err
	}
	return &ExportResult{
		Path: dest, Kind: "recon", Title: "银行存款余额调节表",
		SheetName: "银行余额调节表",
		Rows:      5 + rep.UnreconciledCount(),
	}, nil
}

// SuggestedReconciliationName 返回调节表的默认文件名。
func (s *Service) SuggestedReconciliationName(ctx context.Context,
	accountCode string) (string, error) {
	book, err := s.Book(ctx)
	if err != nil {
		return "", err
	}
	code := strings.TrimSpace(accountCode)
	if code == "" {
		code = "1002"
	}
	// ★ 科目编码是**用户传进来的**，必须一起清洗。
	// 原来只洗了单位名，于是 accountCode 传 "../../../tmp/x" 就会
	// 生成一个爬出账套目录的默认文件名。
	return filepath.Join(".", fmt.Sprintf("%s-银行余额调节表-%s.xlsx",
		safeFileNamePart(book.CompanyName), safeFileNamePart(code))), nil
}

// BankAccounts 返回可用于对账的银行科目。
//
// 界面用它填下拉框 —— 一般企业会有多个银行账户（基本户、一般户），
// 余额调节表必须按账户分别做，不能混在一起。
func (s *Service) BankAccounts(ctx context.Context) ([]AccountOption, error) {
	list, err := s.db.Accounts().List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AccountOption, 0, 4)
	for _, a := range list {
		if !a.IsLeaf || a.RootType != account.RootAsset {
			continue
		}
		if !strings.HasPrefix(a.Code, "1002") {
			continue
		}
		out = append(out, AccountOption{
			Code: a.Code, Name: a.Name, FullName: a.Code + " " + a.Name,
			Direction: "借",
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有找到银行存款科目（1002）")
	}
	return out, nil
}

func reconStatusLabel(status string) string {
	switch status {
	case reconciliation.StatusBalanced:
		return "账实相符"
	case reconciliation.StatusUnbalanced:
		return "账实不符"
	default:
		return "无法判断（未导入对账单）"
	}
}

func toReconciliationView(r *reconciliation.Report) *ReconciliationView {
	v := &ReconciliationView{
		AccountCode: r.AccountCode, AccountName: r.AccountName,
		AsOf: r.AsOf.String(), From: r.From.String(),
		BookOpening: r.BookOpening, BankOpening: r.BankOpening,
		OpeningDiff: r.OpeningDiff(),
		BookBalance: r.BookBalance, BookAdjusted: r.BookAdjusted,
		BankBalance: r.BankBalance, BankAdjusted: r.BankAdjusted,
		UnreconciledFlows: r.UnreconciledFlows,
		Status:            r.Status(),
		StatusLabel:       reconStatusLabel(r.Status()),
		Balanced:          r.Balanced(),
		Difference:        r.Difference(),
		Summary:           r.Summary(),
		Notes:             r.Notes,
	}
	conv := func(items []reconciliation.Item) []ReconciliationItemView {
		if len(items) == 0 {
			return []ReconciliationItemView{}
		}
		out := make([]ReconciliationItemView, 0, len(items))
		for _, it := range items {
			out = append(out, ReconciliationItemView{
				Date: it.Date.String(), Days: it.Days, Summary: it.Summary,
				Reference: it.Reference, Amount: it.Amount,
			})
		}
		return out
	}
	v.BankReceivedNotBooked = conv(r.BankReceivedNotBooked)
	v.BankPaidNotBooked = conv(r.BankPaidNotBooked)
	v.BookReceivedNotBanked = conv(r.BookReceivedNotBanked)
	v.BookPaidNotBanked = conv(r.BookPaidNotBanked)
	for _, l := range r.Lines {
		v.Lines = append(v.Lines, ReconciliationLineView{
			Side: l.Side, Kind: l.Kind, Label: l.Label,
			Amount: l.Amount, Subtotal: l.Subtotal,
		})
	}
	return v
}
