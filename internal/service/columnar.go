package service

import (
	"context"

	"fmt"
	"miniaccount/internal/domain/audit"
	"path/filepath"
	"strings"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/columnar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/export"
)

// ---------------------------------------------------------------------------
// 多栏式明细账
// ---------------------------------------------------------------------------

// ColumnarRequest 是多栏式明细账的参数。
type ColumnarRequest struct {
	// AccountCode 是要展开的科目，如 5602 管理费用、222101 应交增值税。
	AccountCode string
	// From / To 是期间（YYYY-MM-DD）；留空取当前可记账期间。
	From string `json:"from"`
	To   string `json:"to"`
}

// ColumnarColumnView 是一个栏目。
type ColumnarColumnView struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Side  string `json:"side"`
	// SideLabel 是「借」或「贷」，界面直接拿它显示。
	SideLabel   string `json:"sideLabel"`
	AccountCode string `json:"accountCode,omitempty"`
	// Other 为真表示这是兜底栏（有发生额没归上正常栏目）。
	Other bool `json:"other"`
}

// ColumnarRowView 是多栏式明细账上的一行。
type ColumnarRowView struct {
	Date      string `json:"date"`
	VoucherNo string `json:"voucherNo"`
	Summary   string `json:"summary"`
	// Amounts 与 Columns 一一对应，已按各栏方向取正负号。
	//
	// ★ 服务层就把它铺成与栏目等长的稠密数组，而不是只给「第几栏 + 金额」。
	// 多栏式明细账在前端是一张横向几十列的表格，稠密数组可以直接
	// v-for 出单元格；让前端自己按下标塞值反而更容易塞错列。
	Amounts []money.Money `json:"amounts"`
	// Total 是本行金额（单栏，等于 Amounts 里那个非零值）。
	Total   money.Money `json:"total"`
	Debit   money.Money `json:"debit"`
	Credit  money.Money `json:"credit"`
	Balance money.Money `json:"balance"`
	Dir     string      `json:"dir"`
}

// ColumnarView 是一张多栏式明细账。
type ColumnarView struct {
	AccountCode string `json:"accountCode"`
	AccountName string `json:"accountName"`
	From        string `json:"from"`
	To          string `json:"to"`

	Columns []ColumnarColumnView `json:"columns"`
	// DebitColumns / CreditColumns 是两侧栏目的下标，界面据此画分组表头。
	//
	// 单侧多栏（管理费用）时只有一组，双侧多栏（应交增值税）时两组都有 ——
	// 界面不必自己判断方向，照着下标分组即可。
	DebitColumns  []int `json:"debitColumns"`
	CreditColumns []int `json:"creditColumns"`

	Rows         []ColumnarRowView `json:"rows"`
	ColumnTotals []money.Money     `json:"columnTotals"`

	Opening     money.Money `json:"opening"`
	OpeningDir  string      `json:"openingDir"`
	DebitTotal  money.Money `json:"debitTotal"`
	CreditTotal money.Money `json:"creditTotal"`
	Net         money.Money `json:"net"`
	Closing     money.Money `json:"closing"`
	ClosingDir  string      `json:"closingDir"`

	Summary string   `json:"summary"`
	Notes   []string `json:"notes"`
}

// Columnar 生成多栏式明细账。
func (s *Service) Columnar(ctx context.Context, req ColumnarRequest) (*ColumnarView, error) {
	s.recordView(ctx, AuditEvent{
		Action: audit.ActionViewColumnar, Entity: "report",
		EntityID: "columnar/" + trimTo(req.AccountCode, 20),
		Summary:  "查看多栏式明细账（科目 " + trimTo(req.AccountCode, 20) + "）",
	})
	rep, err := s.columnarReport(ctx, req)
	if err != nil {
		return nil, err
	}
	return toColumnarView(rep), nil
}

// columnarReport 是界面与导出共用的取数路径。
func (s *Service) columnarReport(ctx context.Context,
	req ColumnarRequest) (*columnar.Report, error) {

	code := strings.TrimSpace(req.AccountCode)
	if code == "" {
		return nil, fmt.Errorf("请选择要展开的科目")
	}

	from, to, err := s.statementRange(ctx, req.From, req.To)
	if err != nil {
		return nil, err
	}
	return s.db.Columnar().BuildColumnar(ctx, code, from, to)
}

// ColumnarAccounts 返回可以做多栏式明细账的科目。
//
// 只列有下级明细的科目 —— 没有下级的科目做不出多栏式，
// 列出来只会让用户白选一次再看报错。
func (s *Service) ColumnarAccounts(ctx context.Context) ([]AccountOption, error) {
	list, err := s.db.Columnar().ColumnCandidates(ctx)
	if err != nil {
		return nil, err
	}
	tree, err := s.db.Accounts().Tree(ctx)
	if err != nil {
		return nil, err
	}
	byCode := map[string]*account.Account{}
	for _, a := range tree.All() {
		byCode[a.Code] = a
	}

	out := make([]AccountOption, 0, len(list))
	for _, a := range list {
		full := a.FullName(byCode)
		direction := "借"
		if a.BalanceDir != account.DirDebit {
			direction = "贷"
		}
		out = append(out, AccountOption{
			Code: a.Code, Name: a.Name, FullName: full,
			Direction:  direction,
			SearchText: a.Code + " " + a.Name + " " + full + " " + direction,
		})
	}
	return out, nil
}

// ColumnarExportResult 是多栏式明细账的导出结果。
type ColumnarExportResult struct {
	Path      string   `json:"path"`
	Title     string   `json:"title"`
	SheetName string   `json:"sheetName"`
	Rows      int      `json:"rows"`
	Columns   []string `json:"columns"`
}

// ExportColumnar 把多栏式明细账导出为 Excel。
//
// ★ 与 Columnar 共用同一个取数函数（columnarReport），
// 导出的必须是**屏幕上那一张**。这张表的数字依赖用户选的科目与期间，
// 两条取数路径一旦分家就会导出另一张表 —— 这是会计场景里最不能容忍的。
func (s *Service) ExportColumnar(ctx context.Context,
	req ColumnarRequest, dest string) (*ColumnarExportResult, error) {

	dest = strings.TrimSpace(dest)
	if dest == "" {
		return nil, fmt.Errorf("请选择导出位置")
	}
	if !strings.HasSuffix(strings.ToLower(dest), ".xlsx") {
		dest += ".xlsx"
	}

	rep, err := s.columnarReport(ctx, req)
	if err != nil {
		return nil, err
	}
	book, err := s.Book(ctx)
	if err != nil {
		return nil, err
	}

	opts := export.ColumnarOptions{
		Title:       "多栏式明细账",
		CompanyName: book.CompanyName,
		DateLabel:   fmt.Sprintf("%s 至 %s", rep.From, rep.To),
		SheetName:   "多栏式明细账",
		Totals:      rep.ColumnTotals,
		Opening:     fmt.Sprintf("%s %s", rep.OpeningDir, rep.Opening.Abs()),
		Closing:     fmt.Sprintf("%s %s", rep.ClosingDir, rep.Closing.Abs()),
		DebitTotal:  rep.DebitTotal,
		CreditTotal: rep.CreditTotal,
		Notes:       rep.Notes,
	}
	for _, c := range rep.Columns {
		opts.Columns = append(opts.Columns, c.Label)
		label := "借"
		if c.Side == columnar.SideCredit {
			label = "贷"
		}
		opts.SideLabels = append(opts.SideLabels, label)
	}
	for _, r := range rep.Rows {
		amounts := make([]money.Money, len(rep.Columns))
		if r.ColumnIndex >= 0 && r.ColumnIndex < len(amounts) {
			amounts[r.ColumnIndex] = r.Amount
		}
		opts.Rows = append(opts.Rows, export.ColumnarRow{
			Date: r.Date.String(), VoucherNo: r.VoucherNo, Summary: r.Summary,
			Amounts: amounts, Balance: r.Balance, Dir: r.Dir,
		})
	}

	if err := export.Columnar(dest, opts); err != nil {
		return nil, err
	}
	return &ColumnarExportResult{
		Path: dest, Title: rep.AccountName + " 多栏式明细账",
		SheetName: "多栏式明细账",
		Rows:      len(rep.Rows), Columns: opts.Columns,
	}, nil
}

// SuggestedColumnarName 返回多栏式明细账的默认文件名。
func (s *Service) SuggestedColumnarName(ctx context.Context,
	accountCode string) (string, error) {
	book, err := s.Book(ctx)
	if err != nil {
		return "", err
	}
	code := strings.TrimSpace(accountCode)
	if code == "" {
		code = "columnar"
	}
	// 科目编码与单位名都要清洗 —— 见 safeFileNamePart 的说明
	return filepath.Join(".", fmt.Sprintf("%s-多栏式明细账-%s.xlsx",
		safeFileNamePart(book.CompanyName), safeFileNamePart(code))), nil
}

func toColumnarView(r *columnar.Report) *ColumnarView {
	v := &ColumnarView{
		AccountCode: r.AccountCode, AccountName: r.AccountName,
		From: r.From.String(), To: r.To.String(),
		Opening: r.Opening, OpeningDir: r.OpeningDir,
		DebitTotal: r.DebitTotal, CreditTotal: r.CreditTotal, Net: r.Net(),
		Closing: r.Closing, ClosingDir: r.ClosingDir,
		Summary: r.Summary(), Notes: r.Notes,
	}
	for i, c := range r.Columns {
		side := string(c.Side)
		label := "借"
		if c.Side == columnar.SideCredit {
			label = "贷"
			v.CreditColumns = append(v.CreditColumns, i)
		} else {
			v.DebitColumns = append(v.DebitColumns, i)
		}
		v.Columns = append(v.Columns, ColumnarColumnView{
			Key: c.Key, Label: c.Label, Side: side, SideLabel: label,
			AccountCode: c.AccountCode, Other: c.Other,
		})
	}
	// 空切片而不是 nil：界面 v-for 到 null 会直接报错
	if v.DebitColumns == nil {
		v.DebitColumns = []int{}
	}
	if v.CreditColumns == nil {
		v.CreditColumns = []int{}
	}
	if v.Notes == nil {
		v.Notes = []string{}
	}

	v.ColumnTotals = r.ColumnTotals
	if v.ColumnTotals == nil {
		v.ColumnTotals = []money.Money{}
	}

	v.Rows = make([]ColumnarRowView, 0, len(r.Rows))
	for _, row := range r.Rows {
		amounts := make([]money.Money, len(r.Columns))
		if row.ColumnIndex >= 0 && row.ColumnIndex < len(amounts) {
			amounts[row.ColumnIndex] = row.Amount
		}
		v.Rows = append(v.Rows, ColumnarRowView{
			Date: row.Date.String(), VoucherNo: row.VoucherNo, Summary: row.Summary,
			Amounts: amounts, Total: row.Amount,
			Debit: row.Debit, Credit: row.Credit,
			Balance: row.Balance, Dir: row.Dir,
		})
	}
	return v
}
