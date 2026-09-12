package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/summary"
	"miniaccount/internal/export"
)

// ---------------------------------------------------------------------------
// 凭证汇总表
// ---------------------------------------------------------------------------

// SummaryRequest 是凭证汇总表的参数。
type SummaryRequest struct {
	// From / To 是期间（YYYY-MM-DD）；留空取当前可记账期间。
	From string `json:"from"`
	To   string `json:"to"`
}

// SummaryWordView 是按凭证字汇总的一行。
type SummaryWordView struct {
	Word   string      `json:"word"`
	Label  string      `json:"label"`
	Count  int         `json:"count"`
	Debit  money.Money `json:"debit"`
	Credit money.Money `json:"credit"`
}

// SummaryDayView 是按日期汇总的一行。
type SummaryDayView struct {
	Date   string      `json:"date"`
	Count  int         `json:"count"`
	Nos    []string    `json:"nos"`
	Debit  money.Money `json:"debit"`
	Credit money.Money `json:"credit"`
}

// SummaryAccountView 是按科目汇总的一行。
type SummaryAccountView struct {
	AccountCode string      `json:"accountCode"`
	AccountName string      `json:"accountName"`
	FullName    string      `json:"fullName"`
	Count       int         `json:"count"`
	Debit       money.Money `json:"debit"`
	Credit      money.Money `json:"credit"`
	Net         money.Money `json:"net"`
	// Dir 是净额方向的中文名（借/贷/平），界面直接显示。
	Dir string `json:"dir"`
}

// SummaryView 是一张凭证汇总表。
type SummaryView struct {
	From string `json:"from"`
	To   string `json:"to"`

	WordRows    []SummaryWordView    `json:"wordRows"`
	DayRows     []SummaryDayView     `json:"dayRows"`
	AccountRows []SummaryAccountView `json:"accountRows"`

	VoucherCount int         `json:"voucherCount"`
	DebitTotal   money.Money `json:"debitTotal"`
	CreditTotal  money.Money `json:"creditTotal"`
	Balanced     bool        `json:"balanced"`

	DraftCount      int `json:"draftCount"`
	VoidedCount     int `json:"voidedCount"`
	ReversalCount   int `json:"reversalCount"`
	AttachmentTotal int `json:"attachmentTotal"`

	// GapDays 是期间内一张凭证都没有的天数（只给数字，界面按需展开）。
	GapDays int `json:"gapDays"`

	Summary string   `json:"summary"`
	Notes   []string `json:"notes"`
}

// Summary 生成凭证汇总表。
func (s *Service) Summary(ctx context.Context, req SummaryRequest) (*SummaryView, error) {
	s.recordView(ctx, AuditEvent{
		Action: audit.ActionViewSummary, Entity: "report",
		EntityID: "summary/" + trimTo(req.From, 10) + "~" + trimTo(req.To, 10),
		Summary:  "查看凭证汇总表",
		Detail:   map[string]any{"起": req.From, "止": req.To},
	})
	rep, err := s.summaryReport(ctx, req)
	if err != nil {
		return nil, err
	}
	return toSummaryView(rep), nil
}

// summaryReport 是界面与导出共用的取数路径。
func (s *Service) summaryReport(ctx context.Context,
	req SummaryRequest) (*summary.Report, error) {

	from, to, err := s.statementRange(ctx, req.From, req.To)
	if err != nil {
		return nil, err
	}
	return s.db.Summary().BuildSummary(ctx, from, to)
}

// SummaryByPeriod 按会计期间生成凭证汇总表。
//
// 界面上的「上一期 / 下一期」翻页用它：按期间比按起止日更符合
// 会计的操作习惯 —— 月底汇总的对象天然是一个会计期间。
func (s *Service) SummaryByPeriod(ctx context.Context, year, month int) (*SummaryView, error) {
	if year <= 0 || month < 1 || month > 12 {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", year, month)
	}
	from, to := monthBounds(year, month)
	rep, err := s.db.Summary().BuildSummary(ctx, from, to)
	if err != nil {
		return nil, err
	}
	return toSummaryView(rep), nil
}

// monthBounds 返回某月的首末日期。
func monthBounds(year, month int) (calendar.Date, calendar.Date) {
	first := calendar.Date{Year: year, Month: month, Day: 1}
	last := calendar.Date{Year: year, Month: month,
		Day: calendar.DaysInMonth(year, month)}
	return first, last
}

// SummaryExportResult 是凭证汇总表的导出结果。
type SummaryExportResult struct {
	Path      string `json:"path"`
	Title     string `json:"title"`
	SheetName string `json:"sheetName"`
	Rows      int    `json:"rows"`
}

// ExportSummary 把凭证汇总表导出为 Excel。
//
// ★ 与 Summary 共用同一个取数函数（summaryReport），
// 导出的必须是屏幕上那一张。
func (s *Service) ExportSummary(ctx context.Context,
	req SummaryRequest, dest string) (*SummaryExportResult, error) {

	dest = strings.TrimSpace(dest)
	if dest == "" {
		return nil, fmt.Errorf("请选择导出位置")
	}
	if !strings.HasSuffix(strings.ToLower(dest), ".xlsx") {
		dest += ".xlsx"
	}
	rep, err := s.summaryReport(ctx, req)
	if err != nil {
		return nil, err
	}
	book, err := s.Book(ctx)
	if err != nil {
		return nil, err
	}
	v := toSummaryView(rep)

	opts := export.SummaryOptions{
		Title:       "凭证汇总表",
		CompanyName: book.CompanyName,
		DateLabel:   fmt.Sprintf("%s 至 %s", rep.From, rep.To),
		DebitTotal:  rep.DebitTotal, CreditTotal: rep.CreditTotal,
		Notes: rep.Notes,
	}
	// 三段用同一套「文本列 + 金额列」结构，
	// 合计行补在最前面，会计要先看到总数再看明细。
	opts.WordHeaders = []string{"凭证字", "张数", "借方金额", "贷方金额"}
	opts.Word = append(opts.Word, export.SummaryRow{
		Cells: []export.SummaryCell{
			export.Text("合计"), export.Text(itoa(v.VoucherCount)),
			export.Num(v.DebitTotal), export.Num(v.CreditTotal),
		},
		Bold: true,
	})
	for _, w := range v.WordRows {
		opts.Word = append(opts.Word, export.SummaryRow{
			Cells: []export.SummaryCell{
				export.Text(w.Label), export.Text(itoa(w.Count)),
				export.Num(w.Debit), export.Num(w.Credit),
			},
		})
	}

	// 日期段：金额在第 3、4 列，凭证号在第 5 列 —— 单元格按下标给出，
	// 不会因为「文本在前、金额在后」的假设而整体错位。
	opts.DayHeaders = []string{"日期", "张数", "借方金额", "贷方金额", "凭证号"}
	for _, d := range v.DayRows {
		opts.Day = append(opts.Day, export.SummaryRow{
			Cells: []export.SummaryCell{
				export.Text(d.Date), export.Text(itoa(d.Count)),
				export.Num(d.Debit), export.Num(d.Credit),
				export.Text(strings.Join(d.Nos, " ")),
			},
		})
	}
	if v.GapDays > 0 {
		opts.Notes = append(opts.Notes,
			fmt.Sprintf("期间内有 %d 天一张凭证都没有。", v.GapDays))
	}

	opts.AccountHeaders = []string{"科目", "名称", "笔数", "借方发生额", "贷方发生额", "净额"}
	for _, a := range v.AccountRows {
		opts.Account = append(opts.Account, export.SummaryRow{
			Cells: []export.SummaryCell{
				export.Text(a.AccountCode), export.Text(a.AccountName),
				export.Text(itoa(a.Count)),
				export.Num(a.Debit), export.Num(a.Credit), export.Num(a.Net),
			},
		})
	}
	opts.Account = append(opts.Account, export.SummaryRow{
		Cells: []export.SummaryCell{
			export.Text("合计"), export.Text(""), export.Text(""),
			export.Num(v.DebitTotal), export.Num(v.CreditTotal), export.Num(0),
		},
		Bold: true,
	})

	if err := export.Summary(dest, opts); err != nil {
		return nil, err
	}
	rows := len(opts.Word) + len(opts.Day) + len(opts.Account)
	return &SummaryExportResult{
		Path: dest, Title: "凭证汇总表", SheetName: "凭证汇总表", Rows: rows,
	}, nil
}

// 科目汇总表的「张数」列是文本，这里把整数转成字符串。
func itoa(n int) string { return strconv.Itoa(n) }

func toSummaryView(r *summary.Report) *SummaryView {
	v := &SummaryView{
		From: r.From.String(), To: r.To.String(),
		VoucherCount: r.VoucherCount,
		DebitTotal:   r.DebitTotal, CreditTotal: r.CreditTotal,
		Balanced:   r.Balanced(),
		DraftCount: r.DraftCount, VoidedCount: r.VoidedCount,
		ReversalCount: r.ReversalCount, AttachmentTotal: r.AttachmentTotal,
		GapDays: len(r.DayGapDays()),
		Summary: r.Summary(), Notes: r.Notes,
	}
	// 空切片而不是 nil：界面 v-for 到 null 会直接报错
	v.WordRows = make([]SummaryWordView, 0, len(r.WordRows))
	v.DayRows = make([]SummaryDayView, 0, len(r.DayRows))
	v.AccountRows = make([]SummaryAccountView, 0, len(r.AccountRows))
	if v.Notes == nil {
		v.Notes = []string{}
	}

	for _, w := range r.WordRows {
		v.WordRows = append(v.WordRows, SummaryWordView{
			Word: w.Word, Label: w.Word, Count: w.Count,
			Debit: w.Debit, Credit: w.Credit,
		})
	}
	for _, d := range r.DayRows {
		nos := d.Nos
		if nos == nil {
			nos = []string{}
		}
		v.DayRows = append(v.DayRows, SummaryDayView{
			Date: d.Date.String(), Count: d.Count, Nos: nos,
			Debit: d.Debit, Credit: d.Credit,
		})
	}
	for _, a := range r.AccountRows {
		net := a.Net()
		dir := "平"
		if net.IsPositive() {
			dir = "借"
		} else if net.IsNegative() {
			dir = "贷"
		}
		v.AccountRows = append(v.AccountRows, SummaryAccountView{
			AccountCode: a.AccountCode, AccountName: a.AccountName,
			FullName: a.FullName, Count: a.Count,
			Debit: a.Debit, Credit: a.Credit, Net: net, Dir: dir,
		})
	}
	return v
}
