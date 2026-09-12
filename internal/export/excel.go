// Package export 把报表导出为 Excel / CSV。
//
// # 为什么 Excel 比 PDF 更重要
//
// 小微企业会计拿到财务报表后的第一件事通常是**再加工**：把数字粘进
// 老板要的汇总表、和税务申报表对一遍、发给代账公司。
// PDF 只能看，Excel 能算。因此 Excel 是一等公民，PDF 只在需要盖章归档时用。
//
// 汇率、千分位、负数、合计行、打印区域这些细节都要处理 ——
// 会计一眼就能看出导出的表能不能直接用。
package export

import (
	"fmt"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/reconciliation"
	"miniaccount/internal/domain/report"
)

// 中文 Excel 的常用格式。
const (
	// moneyFormat 是金额格式：千分位 + 两位小数，负数带括号（会计习惯）。
	// 括号表示负数是中国财务报表的通行写法，红色是 Excel 内置的第 3 段。
	moneyFormat = `#,##0.00_);(#,##0.00)`
	// titleFormat 用于表头。
	titleFormat = `@`
)

// 样式集合，避免每写一个单元格就新建一个 Style。
type styles struct {
	title   int
	header  int
	subHead int
	text    int
	amount  int
	total   int
	bold    int
	date    int
}

func newStyles(f *excelize.File) (*styles, error) {
	s := &styles{}
	var err error

	// 大标题：16 号加粗居中
	if s.title, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 16, Family: "宋体"},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	}); err != nil {
		return nil, err
	}
	// 表头：加粗、居中、浅灰底、四边细框
	if s.header, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 11, Family: "宋体"},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"F2F2F2"}},
		Border:    thinBorder(),
	}); err != nil {
		return nil, err
	}
	if s.subHead, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 11, Family: "宋体"},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"},
		Border:    thinBorder(),
	}); err != nil {
		return nil, err
	}
	if s.text, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Size: 11, Family: "宋体"},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"},
		Border:    thinBorder(),
	}); err != nil {
		return nil, err
	}
	if s.amount, err = f.NewStyle(&excelize.Style{
		Font:         &excelize.Font{Size: 11, Family: "宋体"},
		Alignment:    &excelize.Alignment{Horizontal: "right", Vertical: "center"},
		CustomNumFmt: strPtr(moneyFormat),
		Border:       thinBorder(),
	}); err != nil {
		return nil, err
	}
	if s.total, err = f.NewStyle(&excelize.Style{
		Font:         &excelize.Font{Bold: true, Size: 11, Family: "宋体"},
		Alignment:    &excelize.Alignment{Horizontal: "right", Vertical: "center"},
		CustomNumFmt: strPtr(moneyFormat),
		Border:       thinBorder(),
		Fill:         excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"FAFAFA"}},
	}); err != nil {
		return nil, err
	}
	if s.bold, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 11, Family: "宋体"},
		Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"},
		Border:    thinBorder(),
	}); err != nil {
		return nil, err
	}
	if s.date, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Size: 11, Family: "宋体"},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border:    thinBorder(),
	}); err != nil {
		return nil, err
	}
	return s, nil
}

func thinBorder() []excelize.Border {
	c := "BFBFBF"
	return []excelize.Border{
		{Type: "left", Color: c, Style: 1},
		{Type: "right", Color: c, Style: 1},
		{Type: "top", Color: c, Style: 1},
		{Type: "bottom", Color: c, Style: 1},
	}
}

func strPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// 财务报表
// ---------------------------------------------------------------------------

// StatementOptions 控制财务报表的导出。
type StatementOptions struct {
	// Title 是报表标题，如「资产负债表」。
	Title string
	// CompanyName 填在「编制单位」处。
	CompanyName string
	// DateLabel 是报表日期的展示文本，如「2025年9月30日」或「2025年9月」。
	DateLabel string
	// Unit 是金额单位说明，默认「元」。
	Unit string
	// SheetName 是工作表名。
	SheetName string
	// ExtraColumns 是除主数据外的附加列标题（如资产负债表的两栏）。
	Columns []string
	// Rows 是数据行。
	Rows []StatementRow
	// Issues 是勾稽关系问题，会作为备注写在工作表下方。
	Issues []report.CheckIssue
}

// StatementRow 是导出的一行。
type StatementRow struct {
	// Label 是行标签（科目名 / 项目名）。
	Label string
	// Indent 是缩进层级，用于科目树与「其中」项。
	Indent int
	// Bold 表示合计行。
	Bold bool
	// Values 是各列的金额，长度应与 Columns 一致。
	Values []money.Money
	// IsText 为真时把 Values 当作文本渲染（用于纯文本列）。
	IsText bool
	// SkipZero 为真时金额为零的单元格留空而不是写 0.00。
	//
	// 余额调节表这类「左右两栏、每行只填一边」的版式需要它：
	// 一个格子里的 0.00 读起来像「这一栏是零」，而实际意思是
	// 「这一栏与这一行无关」。金额表里 0 和空白是两回事。
	SkipZero bool
	// Note 是行尾备注。
	Note string
}

// WriteStatement 把财务报表写入一个 Excel 文件。
func WriteStatement(path string, opts StatementOptions) error {
	f := excelize.NewFile()
	defer f.Close()

	sheet := opts.SheetName
	if sheet == "" {
		sheet = "Sheet1"
	}
	// 重命名默认工作表；工作表名不能超 31 字符且不能含 : \ / ? * [ ]
	sheet = sanitizeSheetName(sheet)
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return err
	}
	st, err := newStyles(f)
	if err != nil {
		return err
	}

	unit := opts.Unit
	if unit == "" {
		unit = "元"
	}
	cols := opts.Columns
	if len(cols) == 0 {
		cols = []string{""}
	}
	nCol := len(cols) + 1 // 第 1 列是项目名

	// ---- 标题区 ----
	// 行1：报表标题（合并居中）
	titleCell, _ := excelize.CoordinatesToCellName(1, 1)
	endTitle, _ := excelize.CoordinatesToCellName(nCol, 1)
	_ = f.MergeCell(sheet, titleCell, endTitle)
	_ = f.SetCellValue(sheet, titleCell, opts.Title)
	_ = f.SetCellStyle(sheet, titleCell, endTitle, st.title)
	_ = f.SetRowHeight(sheet, 1, 28)

	// 行2：编制单位 / 日期 / 单位
	_ = f.SetCellValue(sheet, "A2", "编制单位："+opts.CompanyName)
	_ = f.SetCellStyle(sheet, "A2", "A2", st.text)
	if nCol >= 2 {
		midCell, _ := excelize.CoordinatesToCellName(nCol/2+1, 2)
		_ = f.SetCellValue(sheet, midCell, opts.DateLabel)
		_ = f.SetCellStyle(sheet, midCell, midCell, st.date)
	}
	unitCell, _ := excelize.CoordinatesToCellName(nCol, 2)
	_ = f.SetCellValue(sheet, unitCell, "单位："+unit)
	_ = f.SetCellStyle(sheet, unitCell, unitCell, st.date)

	// 行3：列头
	headRow := 3
	_ = f.SetCellValue(sheet, cellName(1, headRow), "项目")
	_ = f.SetCellStyle(sheet, cellName(1, headRow), cellName(1, headRow), st.header)
	for i, c := range cols {
		cn := cellName(i+2, headRow)
		_ = f.SetCellValue(sheet, cn, c)
		_ = f.SetCellStyle(sheet, cn, cn, st.header)
	}

	// ---- 数据区 ----
	row := headRow + 1
	for _, r := range opts.Rows {
		label := strings.Repeat("　", r.Indent) + r.Label
		if r.Note != "" {
			label += "　" + r.Note
		}
		lc := cellName(1, row)
		_ = f.SetCellValue(sheet, lc, label)
		switch {
		case r.Bold:
			_ = f.SetCellStyle(sheet, lc, lc, st.subHead)
		default:
			_ = f.SetCellStyle(sheet, lc, lc, st.text)
		}
		for i := range cols {
			var v money.Money
			if i < len(r.Values) {
				v = r.Values[i]
			}
			cn := cellName(i+2, row)
			if r.IsText {
				_ = f.SetCellValue(sheet, cn, v.PlainString())
				_ = f.SetCellStyle(sheet, cn, cn, st.text)
				continue
			}
			if r.SkipZero && v == 0 {
				_ = f.SetCellStyle(sheet, cn, cn, st.amount)
				continue
			}
			// 金额写数值而不是字符串：会计要能在 Excel 里直接求和
			_ = f.SetCellValue(sheet, cn, v.Float())
			if r.Bold {
				_ = f.SetCellStyle(sheet, cn, cn, st.total)
			} else {
				_ = f.SetCellStyle(sheet, cn, cn, st.amount)
			}
		}
		row++
	}

	// ---- 勾稽关系备注 ----
	if len(opts.Issues) > 0 {
		row++
		warnCell := cellName(1, row)
		_ = f.SetCellValue(sheet, warnCell, "⚠ 勾稽关系校验未通过")
		_ = f.SetCellStyle(sheet, warnCell, warnCell, st.header)
		row++
		for _, is := range opts.Issues {
			_ = f.SetCellValue(sheet, cellName(1, row), "  "+is.String())
			row++
		}
	}

	// 列宽：项目列宽一些，金额列固定 16
	_ = f.SetColWidth(sheet, "A", "A", 34)
	if nCol >= 2 {
		_ = f.SetColWidth(sheet, "B", colName(nCol), 16)
	}
	// 冻结表头与项目列
	_ = f.SetPanes(sheet, &excelize.Panes{
		Freeze: true, Split: false,
		XSplit: 1, YSplit: headRow,
		TopLeftCell: cellName(2, headRow+1), ActivePane: "bottomRight",
	})
	if err := f.SaveAs(path); err != nil {
		return fmt.Errorf("export: 保存 %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 凭证汇总表
// ---------------------------------------------------------------------------

// SummaryOptions 控制凭证汇总表的导出。
type SummaryOptions struct {
	Title       string
	CompanyName string
	DateLabel   string
	Unit        string
	// Word / Day / Account 三段，各自带标题。
	Word    []SummaryRow
	Day     []SummaryRow
	Account []SummaryRow
	// DebitTotal / CreditTotal 是总合计。
	DebitTotal, CreditTotal money.Money
	// Headers 是三段各自的表头。
	WordHeaders, DayHeaders, AccountHeaders []string
	Notes                                   []string
}

// SummaryCell 是汇总表里的一个单元格。
//
// ★ 单元格是**按列位置显式给出**的，而不是「先给一串文本、再给一串金额」。
// 后一种设计看着省事，但它默认了金额永远紧跟在文本后面 ——
// 一旦某一段的版式是「日期 | 张数 | 借方 | 贷方 | 凭证号」
// （金额夹在中间），金额就会整体偏到凭证号右边去，
// 而表头还老老实实写着「借方金额」。这种错位在 Excel 里
// 看起来完全正常，只有逐格核对才会发现。
type SummaryCell struct {
	// Text 是文本内容（IsAmount 为假时使用）。
	Text string
	// Amount 是金额（IsAmount 为真时写成数值，可在 Excel 里求和）。
	Amount money.Money
	// IsAmount 为真表示这一格是金额。
	IsAmount bool
}

// Text 构造一个文本单元格。
func Text(s string) SummaryCell { return SummaryCell{Text: s} }

// Num 构造一个金额单元格。
func Num(m money.Money) SummaryCell { return SummaryCell{Amount: m, IsAmount: true} }

// SummaryRow 是汇总表里的一行。
type SummaryRow struct {
	// Cells 按列顺序给出，长度不必等于表头列数（尾部空列可省略）。
	Cells []SummaryCell
	// Bold 表示合计行。
	Bold bool
}

// Summary 导出凭证汇总表。
//
// 三段（按凭证字 / 按日期 / 按科目）叠在**同一个工作表**里，
// 而不是拆成三个 sheet：会计核对时要在三段之间来回看
// （张数对不上、金额对不上），拆开就得切标签页，
// 而这三段的合计本来就该完全相等 —— 放在一起才看得出这件事。
func Summary(path string, opts SummaryOptions) error {
	f := excelize.NewFile()
	defer f.Close()

	sheet := sanitizeSheetName("凭证汇总表")
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return err
	}
	st, err := newStyles(f)
	if err != nil {
		return err
	}

	unit := opts.Unit
	if unit == "" {
		unit = "元"
	}
	// 列数取三段里最宽的那一段
	nCol := 4
	for _, h := range [][]string{opts.WordHeaders, opts.DayHeaders, opts.AccountHeaders} {
		if len(h) > nCol {
			nCol = len(h)
		}
	}
	lastCol := colName(nCol)

	_ = f.MergeCell(sheet, "A1", lastCol+"1")
	_ = f.SetCellValue(sheet, "A1", opts.Title)
	_ = f.SetCellStyle(sheet, "A1", lastCol+"1", st.title)
	_ = f.SetRowHeight(sheet, 1, 28)

	_ = f.SetCellValue(sheet, "A2", "编制单位："+opts.CompanyName)
	_ = f.SetCellStyle(sheet, "A2", "A2", st.text)
	midCol := colName(nCol/2 + 1)
	_ = f.SetCellValue(sheet, midCol+"2", opts.DateLabel)
	_ = f.SetCellStyle(sheet, midCol+"2", midCol+"2", st.date)
	_ = f.SetCellValue(sheet, lastCol+"2", "单位："+unit)
	_ = f.SetCellStyle(sheet, lastCol+"2", lastCol+"2", st.date)

	row := 4
	writeBlock := func(title string, headers []string, rows []SummaryRow) {
		if len(rows) == 0 && len(headers) == 0 {
			return
		}
		// 小节标题
		_ = f.MergeCell(sheet, cellName(1, row), cellName(nCol, row))
		_ = f.SetCellValue(sheet, cellName(1, row), title)
		_ = f.SetCellStyle(sheet, cellName(1, row), cellName(nCol, row), st.subHead)
		row++

		// 表头
		for i, h := range headers {
			cn := cellName(i+1, row)
			_ = f.SetCellValue(sheet, cn, h)
			_ = f.SetCellStyle(sheet, cn, cn, st.header)
		}
		row++

		for _, r := range rows {
			for i, c := range r.Cells {
				cn := cellName(i+1, row)
				switch {
				case c.IsAmount:
					// 金额写数值，会计要能在 Excel 里直接求和
					_ = f.SetCellValue(sheet, cn, c.Amount.Float())
					if r.Bold {
						_ = f.SetCellStyle(sheet, cn, cn, st.total)
					} else {
						_ = f.SetCellStyle(sheet, cn, cn, st.amount)
					}
				default:
					_ = f.SetCellValue(sheet, cn, c.Text)
					if r.Bold {
						_ = f.SetCellStyle(sheet, cn, cn, st.subHead)
					} else {
						_ = f.SetCellStyle(sheet, cn, cn, st.text)
					}
				}
			}
			row++
		}
		row++ // 段间空一行
	}

	writeBlock("一、按凭证字", opts.WordHeaders, opts.Word)
	writeBlock("二、按日期", opts.DayHeaders, opts.Day)
	writeBlock("三、按科目（科目汇总表）", opts.AccountHeaders, opts.Account)

	if len(opts.Notes) > 0 {
		hc := cellName(1, row)
		_ = f.SetCellValue(sheet, hc, "提示")
		_ = f.SetCellStyle(sheet, hc, hc, st.header)
		row++
		for _, n := range opts.Notes {
			_ = f.SetCellValue(sheet, cellName(1, row), "  "+n)
			row++
		}
	}

	_ = f.SetColWidth(sheet, "A", "A", 14)
	_ = f.SetColWidth(sheet, "B", "B", 28)
	if nCol >= 3 {
		_ = f.SetColWidth(sheet, "C", lastCol, 18)
	}
	if err := f.SaveAs(path); err != nil {
		return fmt.Errorf("export: 保存 %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 多栏式明细账
// ---------------------------------------------------------------------------

// ColumnarOptions 控制多栏式明细账的导出。
type ColumnarOptions struct {
	Title       string
	CompanyName string
	DateLabel   string
	SheetName   string
	// Columns 是栏目标题（短名，如「办公费」）。
	Columns []string
	// SideLabels 与 Columns 一一对应，是「借」或「贷」，
	// 会作为第二行分组表头合并显示。
	SideLabels []string
	// Rows 是数据行，Amounts 与 Columns 等长。
	Rows []ColumnarRow
	// Totals 是各栏目合计，与 Columns 等长。
	Totals []money.Money
	// Opening / Closing 是期初、期末余额文本。
	Opening, Closing string
	// DebitTotal / CreditTotal 是本期借贷发生额合计。
	DebitTotal, CreditTotal money.Money
	Notes                   []string
}

// ColumnarRow 是多栏式明细账的一行。
type ColumnarRow struct {
	Date      string
	VoucherNo string
	Summary   string
	// Amounts 与 Columns 等长；零值留空（多栏式每行只填一栏）。
	Amounts []money.Money
	Balance money.Money
	Dir     string
}

// Columnar 导出多栏式明细账。
//
// # 为什么这张表特别值得导出
//
// 它是**横向**的表：管理费用 17 栏、应交增值税两侧共 10 栏，
// 终端里根本排不下，界面上也要横向滚动。Excel 才是它天然的去处 ——
// 会计把栏目横向拉宽、冻结前四列、直接打印成 A3 或横向 A4 归档。
//
// 版式：前四列是日期/凭证号/摘要/余额，之后每栏一列。
// 表头两行：第一行按方向合并（借 / 贷），第二行是栏目名。
func Columnar(path string, opts ColumnarOptions) error {
	f := excelize.NewFile()
	defer f.Close()

	sheet := sanitizeSheetName(opts.SheetName)
	if sheet == "" {
		sheet = "多栏式明细账"
	}
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return err
	}
	st, err := newStyles(f)
	if err != nil {
		return err
	}

	nCol := 4 + len(opts.Columns)
	lastCol := colName(nCol)

	// 行1：标题
	_ = f.MergeCell(sheet, "A1", lastCol+"1")
	_ = f.SetCellValue(sheet, "A1", opts.Title)
	_ = f.SetCellStyle(sheet, "A1", lastCol+"1", st.title)
	_ = f.SetRowHeight(sheet, 1, 28)

	// 行2：编制单位 / 期间 / 单位
	_ = f.SetCellValue(sheet, "A2", "编制单位："+opts.CompanyName)
	_ = f.SetCellStyle(sheet, "A2", "A2", st.text)
	midCol := colName(nCol/2 + 1)
	_ = f.SetCellValue(sheet, midCol+"2", opts.DateLabel)
	_ = f.SetCellStyle(sheet, midCol+"2", midCol+"2", st.date)
	_ = f.SetCellValue(sheet, lastCol+"2", "单位：元")
	_ = f.SetCellStyle(sheet, lastCol+"2", lastCol+"2", st.date)

	// 行3：分组表头（借 / 贷）—— 把方向标出来，
	// 否则「进项税额」和「销项税额」并排放在一起看不出谁增谁减。
	headRow := 3
	// ★ 前四列各自纵向合并两行，**不能**合并成一整块 A3:D4 ——
	// 合并区域只保留左上角那个单元格的值，写进去的另外三个标题会被丢掉，
	// 结果四列表头全都显示成最后写的那一个。
	for i, h := range []string{"日期", "凭证号", "摘要", "余额"} {
		cn := colName(i + 1)
		_ = f.MergeCell(sheet, cn+"3", cn+"4")
		_ = f.SetCellValue(sheet, cn+"3", h)
		_ = f.SetCellStyle(sheet, cn+"3", cn+"4", st.header)
	}

	q := 5
	for q <= nCol {
		side := ""
		if q-5 < len(opts.SideLabels) {
			side = opts.SideLabels[q-5]
		}
		// 把连续的同一方向合并成一个跨列表头
		end := q
		for end+1 <= nCol && end-5+1 < len(opts.SideLabels) &&
			opts.SideLabels[end-5+1] == side {
			end++
		}
		from, to := cellName(q, headRow), cellName(end, headRow)
		if side == "" {
			side = "栏目"
		}
		_ = f.MergeCell(sheet, from, to)
		_ = f.SetCellValue(sheet, from, side)
		_ = f.SetCellStyle(sheet, from, to, st.header)
		q = end + 1
	}

	// 行4：栏目名
	for i, c := range opts.Columns {
		cn := cellName(5+i, headRow+1)
		_ = f.SetCellValue(sheet, cn, c)
		_ = f.SetCellStyle(sheet, cn, cn, st.header)
	}

	// 数据区
	row := headRow + 2
	// 期初行
	_ = f.SetCellValue(sheet, cellName(1, row), "期初余额")
	_ = f.SetCellStyle(sheet, cellName(1, row), cellName(4, row), st.subHead)
	_ = f.SetCellValue(sheet, cellName(4, row), opts.Opening)
	_ = f.SetCellStyle(sheet, cellName(4, row), cellName(4, row), st.amount)
	row++

	for _, r := range opts.Rows {
		_ = f.SetCellValue(sheet, cellName(1, row), r.Date)
		_ = f.SetCellValue(sheet, cellName(2, row), r.VoucherNo)
		_ = f.SetCellValue(sheet, cellName(3, row), r.Summary)
		for i := range opts.Columns {
			var v money.Money
			if i < len(r.Amounts) {
				v = r.Amounts[i]
			}
			cn := cellName(5+i, row)
			_ = f.SetCellStyle(sheet, cn, cn, st.amount)
			// 多栏式的每一行只填一栏，其余留空 ——
			// 满屏的 0.00 会把真正的那一个数淹掉
			if v != 0 {
				_ = f.SetCellValue(sheet, cn, v.Float())
			}
		}
		_ = f.SetCellValue(sheet, cellName(4, row), r.Balance.Float())
		_ = f.SetCellStyle(sheet, cellName(4, row), cellName(4, row), st.amount)
		for c := 1; c <= 3; c++ {
			_ = f.SetCellStyle(sheet, cellName(c, row), cellName(c, row), st.text)
		}
		row++
	}

	// 合计行
	_ = f.SetCellValue(sheet, cellName(1, row), "本期合计")
	_ = f.SetCellStyle(sheet, cellName(1, row), cellName(3, row), st.subHead)
	for i := range opts.Columns {
		var v money.Money
		if i < len(opts.Totals) {
			v = opts.Totals[i]
		}
		cn := cellName(5+i, row)
		_ = f.SetCellStyle(sheet, cn, cn, st.total)
		if v != 0 {
			_ = f.SetCellValue(sheet, cn, v.Float())
		}
	}
	row++

	// 期末行
	_ = f.SetCellValue(sheet, cellName(1, row), "期末余额")
	_ = f.SetCellStyle(sheet, cellName(1, row), cellName(4, row), st.subHead)
	_ = f.SetCellValue(sheet, cellName(4, row), opts.Closing)
	_ = f.SetCellStyle(sheet, cellName(4, row), cellName(4, row), st.total)
	row += 2

	// 提示区
	if len(opts.Notes) > 0 {
		hc := cellName(1, row)
		_ = f.SetCellValue(sheet, hc, "提示")
		_ = f.SetCellStyle(sheet, hc, hc, st.header)
		row++
		for _, n := range opts.Notes {
			_ = f.SetCellValue(sheet, cellName(1, row), "  "+n)
			row++
		}
	}

	// 列宽与冻结
	_ = f.SetColWidth(sheet, "A", "A", 12)
	_ = f.SetColWidth(sheet, "B", "B", 16)
	_ = f.SetColWidth(sheet, "C", "C", 24)
	_ = f.SetColWidth(sheet, "D", "D", 14)
	if nCol >= 5 {
		_ = f.SetColWidth(sheet, "E", lastCol, 12)
	}
	// 冻结前四列和两行表头：横向几十栏时这是能不能用的关键
	_ = f.SetPanes(sheet, &excelize.Panes{
		Freeze: true, Split: false,
		XSplit: 4, YSplit: headRow + 1,
		TopLeftCell: cellName(5, headRow+2), ActivePane: "bottomRight",
	})
	if err := f.SaveAs(path); err != nil {
		return fmt.Errorf("export: 保存 %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 银行存款余额调节表
// ---------------------------------------------------------------------------

// Reconciliation 导出银行存款余额调节表。
//
// 版式照抄纸质底稿：左边企业账面、右边银行对账单，各自「加/减」未达账项，
// 底部两侧调节后余额。会计把这张表打印出来签字归档，
// 或者贴进给税务/银行的说明里。
func Reconciliation(path, companyName string, rep *reconciliation.Report) error {
	if rep == nil {
		return fmt.Errorf("export: 调节表为空")
	}

	// 每行只填自己那一栏，另一栏留空（SkipZero）——
	// 这正是纸质调节表的写法。
	row2 := func(label string, indent int, bold bool, b, k money.Money) StatementRow {
		return StatementRow{
			Label: label, Indent: indent, Bold: bold,
			Values: []money.Money{b, k}, SkipZero: true,
		}
	}

	rows := []StatementRow{
		row2("账面余额", 0, true, rep.BookBalance, deref(rep.BankBalance)),
	}
	detail := func(title string, items []reconciliation.Item, b, k bool) {
		if len(items) == 0 {
			return
		}
		var sum money.Money
		for _, it := range items {
			sum = sum.Add(it.Amount)
		}
		if b {
			rows = append(rows, row2(title, 0, true, sum, 0))
		} else {
			rows = append(rows, row2(title, 0, true, 0, sum))
		}
		for _, it := range items {
			label := fmt.Sprintf("%s %s（%s，%d 天）",
				it.Date, it.Summary, it.Reference, it.Days)
			if b {
				rows = append(rows, row2(label, 1, false, it.Amount, 0))
			} else {
				rows = append(rows, row2(label, 1, false, 0, it.Amount))
			}
		}
	}
	detail("加：银行已收、企业未收", rep.BankReceivedNotBooked, true, false)
	detail("减：银行已付、企业未付", rep.BankPaidNotBooked, true, false)
	detail("加：企业已收、银行未收", rep.BookReceivedNotBanked, false, true)
	detail("减：企业已付、银行未付", rep.BookPaidNotBanked, false, true)

	rows = append(rows, StatementRow{
		Label: "调节后余额", Bold: true,
		Values:   []money.Money{rep.BookAdjusted, deref(rep.BankAdjusted)},
		SkipZero: true,
	})
	// 状态行只写文字：不给 Values，配合 SkipZero 就不会留下一个
	// 莫名其妙的 0.00 在金额栏里。
	rows = append(rows, StatementRow{
		Label: "调节结果", Bold: true, Note: rep.Summary(), SkipZero: true,
	})

	opts := StatementOptions{
		Title:       "银行存款余额调节表",
		CompanyName: companyName,
		DateLabel:   fmt.Sprintf("%s 至 %s", rep.From, rep.AsOf),
		SheetName:   "银行余额调节表",
		Columns:     []string{"企业账面", "银行对账单"},
		Rows:        rows,
	}
	if err := WriteStatement(path, opts); err != nil {
		return err
	}
	return appendReconNotes(path, rep)
}

// appendReconNotes 把期末提示补在表下方。
//
// 提示里最要紧的是期初差额那句 —— 它直接告诉会计该去查什么。
// 单独写一步是因为 WriteStatement 的备注区只吃 report.CheckIssue，
// 而这里的提示是自由文本，不该为它去伪造勾稽问题的类型。
func appendReconNotes(path string, rep *reconciliation.Report) error {
	// 直接用领域的提示，不再自己拼一条 —— rep.Notes 里的期初提示
	// 已经包含账面/银行/差额三个数，重拼一遍只会让底稿上出现两段
	// 意思相同、措辞不同的话。
	notes := append([]string{}, rep.Notes...)
	if len(notes) == 0 {
		return nil
	}

	f, err := excelize.OpenFile(path)
	if err != nil {
		return fmt.Errorf("export: 打开 %s 补写提示: %w", path, err)
	}
	defer f.Close()
	sheet := sanitizeSheetName("银行余额调节表")
	st, err := newStyles(f)
	if err != nil {
		return err
	}
	// 从已用区域往下两行开始写，避免压在数据上
	rows, err := f.GetRows(sheet)
	if err != nil {
		return err
	}
	row := len(rows) + 2
	hc := cellName(1, row)
	_ = f.SetCellValue(sheet, hc, "提示")
	_ = f.SetCellStyle(sheet, hc, hc, st.header)
	row++
	for _, n := range notes {
		_ = f.SetCellValue(sheet, cellName(1, row), "  "+n)
		row++
	}
	if err := f.SaveAs(path); err != nil {
		return fmt.Errorf("export: 保存 %s: %w", path, err)
	}
	return nil
}

func deref(m *money.Money) money.Money {
	if m == nil {
		return 0
	}
	return *m
}

// ---------------------------------------------------------------------------
// 资产负债表
// ---------------------------------------------------------------------------

// BalanceSheet 导出资产负债表（会小企 01 表）。
//
// 中国资产负债表是左右对照版式：左侧资产、右侧负债和所有者权益，
// 各两栏（期末余额 / 年初余额）。这里按会计通用做法导出为
// 「左表 | 右表」并列的六列布局，与打印版式一致。
func BalanceSheet(path string, companyName string, asOf time.Time,
	closing, opening *report.Definition, issues []report.CheckIssue) error {

	left := filterSide(closing, report.SideLeft)
	right := filterSide(closing, report.SideRight)
	leftOpen := indexByNo(opening, report.SideLeft)
	rightOpen := indexByNo(opening, report.SideRight)

	n := len(left)
	if len(right) > n {
		n = len(right)
	}

	rows := make([]StatementRow, 0, n)
	for i := 0; i < n; i++ {
		var vals []money.Money
		var label string
		var bold bool
		indent := 0

		if i < len(left) {
			l := left[i]
			label = l.DisplayName()
			bold = l.Type == report.LineSubtotal || l.Type == report.LineTotal
			indent = indentOf(l)
			vals = append(vals, l.Value)
			if o, ok := leftOpen[l.No]; ok {
				vals = append(vals, o.Value)
			} else {
				vals = append(vals, 0)
			}
		} else {
			vals = append(vals, 0, 0)
		}

		if i < len(right) {
			r := right[i]
			// 右侧项目名单独一列承载，这里拼在一起保持行对齐
			if label != "" {
				label = label + "　│　" + r.DisplayName()
			} else {
				label = "　│　" + r.DisplayName()
			}
			if r.Type == report.LineSubtotal || r.Type == report.LineTotal {
				bold = true
			}
			vals = append(vals, r.Value)
			if o, ok := rightOpen[r.No]; ok {
				vals = append(vals, o.Value)
			} else {
				vals = append(vals, 0)
			}
		} else {
			vals = append(vals, 0, 0)
		}

		rows = append(rows, StatementRow{Label: label, Indent: indent, Bold: bold, Values: vals})
	}

	return WriteStatement(path, StatementOptions{
		Title:       "资产负债表",
		CompanyName: companyName,
		DateLabel:   asOf.Format("2006年01月02日"),
		SheetName:   "资产负债表",
		Columns:     []string{"资产 期末余额", "资产 年初余额", "负债和所有者权益 期末余额", "负债和所有者权益 年初余额"},
		Rows:        rows,
		// ★ 勾稽问题必须写进导出文件。
		//
		// 屏幕上会显示「资产总计 ≠ 负债和所有者权益总计」的警告，
		// 而导出的 .xlsx 却是干净的 —— 而这份文件正是发给银行、
		// 税务局、代账会计的那一份。对不上账的报表被当成对的交出去，
		// 比什么都不给更糟。
		Issues: issues,
	})
}

// ---------------------------------------------------------------------------
// 利润表
// ---------------------------------------------------------------------------

// IncomeStatement 导出利润表（会小企 02 表）。
//
// 「其中」附列项（memo）缩进显示且不加粗，与官方版式一致。
func IncomeStatement(path string, companyName string, k period.Key,
	current, ytd *report.Definition, issues []report.CheckIssue) error {

	openIdx := indexByNo(ytd, report.SideSingle)

	rows := make([]StatementRow, 0, len(current.Lines))
	for _, l := range current.Lines {
		v := l.Value
		var open money.Money
		if o, ok := openIdx[l.No]; ok {
			open = o.Value
		}
		bold := l.Type == report.LineSubtotal || l.Type == report.LineTotal
		rows = append(rows, StatementRow{
			Label:  l.DisplayName(),
			Indent: indentOf(l),
			Bold:   bold,
			Values: []money.Money{v, open},
		})
	}

	return WriteStatement(path, StatementOptions{
		Title:       "利润表",
		CompanyName: companyName,
		DateLabel:   fmt.Sprintf("%d年%02d月", k.Year, k.Month),
		SheetName:   "利润表",
		Columns:     []string{"本月金额", "本年累计金额"},
		Rows:        rows,
		// 与资产负债表同理：勾稽问题必须跟着文件走
		Issues: issues,
	})
}

// ---------------------------------------------------------------------------
// 科目余额表 / 明细账
// ---------------------------------------------------------------------------

// TrialBalance 导出科目余额表（六栏式）。
func TrialBalance(path, companyName string, year, month int, rep *report.BalanceReport) error {
	rows := make([]StatementRow, 0, len(rep.Rows))
	for _, r := range rep.Rows {
		label := r.AccountCode + " " + r.AccountName
		rows = append(rows, StatementRow{
			Label: label,
			Bold:  !r.IsLeaf,
			Values: []money.Money{
				r.OpeningDebit, r.OpeningCredit,
				r.PeriodDebit, r.PeriodCredit,
				r.ClosingDebit, r.ClosingCredit,
			},
		})
	}
	obD, obC, pd, pc, cbD, cbC := rep.Totals()
	rows = append(rows, StatementRow{
		Label: "合计", Bold: true,
		Values: []money.Money{obD, obC, pd, pc, cbD, cbC},
	})

	return WriteStatement(path, StatementOptions{
		Title:       "科目余额表",
		CompanyName: companyName,
		DateLabel:   fmt.Sprintf("%d年%02d月", year, month),
		SheetName:   "科目余额表",
		Columns: []string{
			"期初余额 借方", "期初余额 贷方",
			"本期发生额 借方", "本期发生额 贷方",
			"期末余额 借方", "期末余额 贷方",
		},
		Rows: rows,
	})
}

// ContactBalances 导出往来单位余额表。
func ContactBalances(path, companyName string, year, month int,
	rows []report.ContactBalanceRow) error {

	out := make([]StatementRow, 0, len(rows)+1)
	var totOpen, totD, totC, totClose money.Money
	for _, r := range rows {
		out = append(out, StatementRow{
			Label: fmt.Sprintf("%s %s（%s）", r.AccountCode, r.ContactName, kindLabel(r.ContactKind)),
			Values: []money.Money{
				r.Opening, r.Debit, r.Credit,
				r.Closing,
			},
		})
		totOpen = totOpen.Add(r.Opening)
		totD = totD.Add(r.Debit)
		totC = totC.Add(r.Credit)
		totClose = totClose.Add(r.Closing)
	}
	out = append(out, StatementRow{
		Label: "合计", Bold: true,
		Values: []money.Money{totOpen, totD, totC, totClose},
	})

	return WriteStatement(path, StatementOptions{
		Title:       "往来单位余额表",
		CompanyName: companyName,
		DateLabel:   fmt.Sprintf("%d年%02d月", year, month),
		SheetName:   "往来余额表",
		Columns:     []string{"期初余额", "本期借方", "本期贷方", "期末余额（借正贷负）"},
		Rows:        out,
	})
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func filterSide(d *report.Definition, side report.Side) []*report.Line {
	var out []*report.Line
	for _, l := range d.Lines {
		if l.Side == side {
			out = append(out, l)
		}
	}
	return out
}

func indexByNo(d *report.Definition, side report.Side) map[int]*report.Line {
	out := map[int]*report.Line{}
	if d == nil {
		return out
	}
	for _, l := range d.Lines {
		if side == "" || l.Side == side {
			out[l.No] = l
		}
	}
	return out
}

// indentOf 返回报表行的缩进层级。
//
// 官方版式用缩进区分层级：「其中」附列项比明细项再深一级。
func indentOf(l *report.Line) int {
	switch l.Type {
	case report.LineMemo:
		return 2
	case report.LineSubtotal:
		return 0
	case report.LineTotal:
		return 0
	default:
		if strings.Contains(l.Name, "减：") || strings.Contains(l.Name, "加：") {
			return 1
		}
		return 1
	}
}

func kindLabel(kind string) string {
	switch kind {
	case "customer":
		return "客户"
	case "supplier":
		return "供应商"
	case "both":
		return "客户/供应商"
	case "employee":
		return "员工"
	case "shareholder":
		return "股东"
	case "other":
		return "其他单位"
	default:
		return kind
	}
}

// 说明：原先把期间参数定义成本包私有的 periodKey 以避免依赖 period 包，
// 但那会让调用方（CLI / Wails 绑定）根本传不进参数 ——
// 跨包无法引用未导出类型的签名。改用 period.Key 后，
// 调用方可以直接把界面上的期间传进来。

func cellName(col, row int) string {
	n, _ := excelize.CoordinatesToCellName(col, row)
	return n
}

func colName(col int) string {
	n, _ := excelize.ColumnNumberToName(col)
	return n
}

// sanitizeSheetName 清理工作表名：Excel 不允许 : \ / ? * [ ] 且长度 ≤ 31。
func sanitizeSheetName(s string) string {
	repl := strings.NewReplacer(":", "_", "\\", "_", "/", "_", "?", "_",
		"*", "_", "[", "_", "]", "_")
	s = repl.Replace(s)
	rs := []rune(s)
	if len(rs) > 31 {
		s = string(rs[:31])
	}
	if s == "" {
		s = "Sheet1"
	}
	return s
}
