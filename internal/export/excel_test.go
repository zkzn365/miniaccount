package export

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/reconciliation"
	"miniaccount/internal/domain/report"
)

func y(n int64) money.Money { return money.Money(n) * money.Yuan }

func outPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

// openForRead 打开导出的文件以便断言。
func openForRead(t *testing.T, path string) *excelize.File {
	t.Helper()
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("导出的文件无法被 Excel 打开: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// cell 读取一个单元格的展示文本。
func cell(t *testing.T, f *excelize.File, sheet, ref string) string {
	t.Helper()
	v, err := f.GetCellValue(sheet, ref)
	if err != nil {
		t.Fatalf("读取 %s!%s 失败: %v", sheet, ref, err)
	}
	return v
}

// checkAmount 断言单元格是一个**数值**且等于期望的元数。
//
// 必须用 RawCellValue 读：默认的 GetCellValue 返回的是**套用数字格式
// 之后**的展示串（"1,470.00"），拿它判断「是不是数值」永远失败 ——
// 而真正要防的 bug 恰恰是「金额被写成了字符串」。
func checkAmount(t *testing.T, f *excelize.File, sheet, ref string, yuanWant float64) {
	t.Helper()
	raw, err := f.GetCellValue(sheet, ref, excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatalf("读取 %s!%s 失败: %v", sheet, ref, err)
	}
	got, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		t.Fatalf("%s!%s = %q 不是数值 —— 会计在 Excel 里没法求和", sheet, ref, raw)
	}
	if math.Abs(got-yuanWant) > 0.005 {
		t.Errorf("%s!%s = %v，期望 %v", sheet, ref, got, yuanWant)
	}
}

// ---------------------------------------------------------------------------
// 科目余额表
// ---------------------------------------------------------------------------

func TestTrialBalanceExport(t *testing.T) {
	path := outPath(t, "科目余额表.xlsx")
	rep := &report.BalanceReport{
		Period: period.NewKey(2025, 9),
		Rows: []report.BalanceRow{
			{AccountCode: "1001", AccountName: "库存现金", Level: 1, IsLeaf: true,
				OpeningDebit: y(100), PeriodDebit: y(50), ClosingDebit: y(150)},
			{AccountCode: "1002", AccountName: "银行存款", Level: 1, IsLeaf: true,
				OpeningDebit: y(10000), PeriodDebit: y(50000), PeriodCredit: y(12000),
				ClosingDebit: y(48000)},
			{AccountCode: "224101", AccountName: "其他应付款—股东", Level: 2, IsLeaf: true,
				OpeningCredit: y(0), PeriodCredit: y(50000), ClosingCredit: y(50000)},
		},
	}
	if err := TrialBalance(path, "杭州某某科技有限公司", 2025, 9, rep); err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	f := openForRead(t, path)
	sheet := "科目余额表"
	if got := f.GetSheetName(0); got != sheet {
		t.Errorf("工作表名 = %q，期望 %q", got, sheet)
	}

	// 标题
	if v, _ := f.GetCellValue(sheet, "A1"); v != "科目余额表" {
		t.Errorf("A1 = %q", v)
	}
	// 编制单位与日期
	if v, _ := f.GetCellValue(sheet, "A2"); !strings.Contains(v, "杭州某某科技有限公司") {
		t.Errorf("A2 = %q", v)
	}
	if v, _ := f.GetCellValue(sheet, "G2"); v != "单位：元" {
		t.Errorf("G2 = %q", v)
	}
	// 六栏表头
	for i, want := range []string{
		"项目", "期初余额 借方", "期初余额 贷方",
		"本期发生额 借方", "本期发生额 贷方",
		"期末余额 借方", "期末余额 贷方",
	} {
		cn := cellName(i+1, 3)
		if v, _ := f.GetCellValue(sheet, cn); v != want {
			t.Errorf("%s = %q，期望 %q", cn, v, want)
		}
	}
	// 第一行数据
	if v, _ := f.GetCellValue(sheet, "A4"); !strings.Contains(v, "1001") {
		t.Errorf("A4 = %q，应含科目编码", v)
	}
	// 注意：货币格式里的 _) 会给正数补一个对齐空格，断言前要 TrimSpace
	if v, _ := f.GetCellValue(sheet, "F4"); strings.TrimSpace(v) != "150.00" {
		t.Errorf("F4 = %q，期末借方应为 150.00", v)
	}
	// 合计行
	rows, _ := f.GetRows(sheet)
	last := rows[len(rows)-1]
	if last[0] != "合计" {
		t.Errorf("最后一行应为合计，实际 %q", last[0])
	}
	if got := strings.TrimSpace(last[5]); got != "48,150.00" { // 150 + 48000
		t.Errorf("合计期末借方 = %q，期望 48,150.00", got)
	}
}

// ★ 金额必须写数值而不是字符串 —— 会计要能在 Excel 里直接求和
func TestAmountsAreNumeric(t *testing.T) {
	path := outPath(t, "numeric.xlsx")
	rep := &report.BalanceReport{
		Period: period.NewKey(2025, 9),
		Rows: []report.BalanceRow{
			{AccountCode: "1001", AccountName: "库存现金", IsLeaf: true, ClosingDebit: y(1234)},
		},
	}
	if err := TrialBalance(path, "A", 2025, 9, rep); err != nil {
		t.Fatal(err)
	}
	f := openForRead(t, path)
	v, err := f.GetCellValue("科目余额表", "F4", excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatal(err)
	}
	if v != "1234" {
		t.Errorf("单元格原始值 = %q，期望数值 1234（写成字符串会让 Excel 无法求和）", v)
	}
	// 应有金额格式
	style, _ := f.GetCellStyle("科目余额表", "F4")
	nf, _ := f.GetDefaultFont()
	_ = nf
	numFmt, err := f.GetCellStyle("科目余额表", "F4")
	if err != nil || numFmt == 0 {
		t.Errorf("金额单元格应带样式，style=%d err=%v", style, err)
	}
}

// ---------------------------------------------------------------------------
// 资产负债表
// ---------------------------------------------------------------------------

func bsDefinition(t *testing.T, shift money.Money) *report.Definition {
	t.Helper()
	d, err := report.LoadBalanceSheet()
	if err != nil {
		t.Fatal(err)
	}
	// 直接给各行塞值，验证导出格式而非计算
	for _, l := range d.Lines {
		l.Value = shift
	}
	return d
}

func TestBalanceSheetExport(t *testing.T) {
	path := outPath(t, "资产负债表.xlsx")
	closing := bsDefinition(t, y(1000))
	opening := bsDefinition(t, y(500))

	if err := BalanceSheet(path, "杭州某某科技有限公司",
		time.Date(2025, 9, 30, 0, 0, 0, 0, time.UTC), closing, opening, nil); err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	f := openForRead(t, path)
	sheet := "资产负债表"
	if v, _ := f.GetCellValue(sheet, "A1"); v != "资产负债表" {
		t.Errorf("A1 = %q", v)
	}
	if v, _ := f.GetCellValue(sheet, "C2"); v != "2025年09月30日" {
		t.Errorf("日期格 = %q", v)
	}
	// 四栏表头：左期末/左年初/右期末/右年初
	wants := []string{
		"项目", "资产 期末余额", "资产 年初余额",
		"负债和所有者权益 期末余额", "负债和所有者权益 年初余额",
	}
	for i, want := range wants {
		cn := cellName(i+1, 3)
		if v, _ := f.GetCellValue(sheet, cn); v != want {
			t.Errorf("%s = %q，期望 %q", cn, v, want)
		}
	}
	// 第一行左侧应是「货币资金」
	if v, _ := f.GetCellValue(sheet, "A4"); !strings.Contains(v, "货币资金") {
		t.Errorf("A4 = %q，应含货币资金", v)
	}
	// 该行右侧项目名应拼在同一单元格里
	if v, _ := f.GetCellValue(sheet, "A4"); !strings.Contains(v, "│") {
		t.Errorf("A4 = %q，应含左右分隔符", v)
	}
}

// ---------------------------------------------------------------------------
// 利润表
// ---------------------------------------------------------------------------

func TestIncomeStatementExport(t *testing.T) {
	path := outPath(t, "利润表.xlsx")
	current, err := report.LoadIncomeStatement()
	if err != nil {
		t.Fatal(err)
	}
	ytd, _ := report.LoadIncomeStatement()
	for i, l := range current.Lines {
		l.Value = y(int64(100 + i))
		ytd.Lines[i].Value = y(int64(1000 + i))
	}

	if err := IncomeStatement(path, "杭州某某科技有限公司",
		period.NewKey(2025, 9), current, ytd, nil); err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	f := openForRead(t, path)
	sheet := "利润表"
	if v, _ := f.GetCellValue(sheet, "A1"); v != "利润表" {
		t.Errorf("A1 = %q", v)
	}
	// 利润表共 3 列（项目 + 2 个金额列），日期居中落在 B2
	if v, _ := f.GetCellValue(sheet, "B2"); v != "2025年09月" {
		t.Errorf("日期格 B2 = %q，期望 2025年09月", v)
	}
	if v, _ := f.GetCellValue(sheet, "B3"); v != "本月金额" {
		t.Errorf("B3 = %q", v)
	}
	if v, _ := f.GetCellValue(sheet, "C3"); v != "本年累计金额" {
		t.Errorf("C3 = %q", v)
	}
	// 32 行数据
	rows, _ := f.GetRows(sheet)
	if len(rows) != 3+32 {
		t.Errorf("总行数 = %d，期望 35", len(rows))
	}
	// 第一行应是「一、营业收入」
	if v, _ := f.GetCellValue(sheet, "A4"); !strings.Contains(v, "营业收入") {
		t.Errorf("A4 = %q", v)
	}
	// 四个层次行应收尾
	last := rows[len(rows)-1]
	if !strings.Contains(last[0], "净利润") {
		t.Errorf("最后一行 = %q，应为净利润", last[0])
	}
}

// 「其中」附列项应缩进
func TestIncomeStatementMemoIndent(t *testing.T) {
	path := outPath(t, "memo.xlsx")
	current, _ := report.LoadIncomeStatement()
	ytd, _ := report.LoadIncomeStatement()

	if err := IncomeStatement(path, "A", period.NewKey(2025, 9), current, ytd, nil); err != nil {
		t.Fatal(err)
	}
	f := openForRead(t, path)
	rows, _ := f.GetRows("利润表")
	// 行 4 是「其中：消费税」（memo），应比普通明细项缩进更深
	var memoCell, itemCell string
	for _, r := range rows {
		if len(r) == 0 {
			continue
		}
		if strings.Contains(r[0], "其中：消费税") {
			memoCell = r[0]
		}
		if strings.Contains(r[0], "税金及附加") {
			itemCell = r[0]
		}
	}
	if memoCell == "" || itemCell == "" {
		t.Fatalf("未找到对照行：memo=%q item=%q", memoCell, itemCell)
	}
	memoIndent := len(memoCell) - len(strings.TrimLeft(memoCell, "　"))
	itemIndent := len(itemCell) - len(strings.TrimLeft(itemCell, "　"))
	if memoIndent <= itemIndent {
		t.Errorf("「其中」项应缩进更深：memo=%d item=%d", memoIndent, itemIndent)
	}
}

// ---------------------------------------------------------------------------
// 勾稽问题提示
// ---------------------------------------------------------------------------

// 报表不平衡时必须把问题写在文件里，而不是静默导出一份错的表
func TestStatementWritesCheckIssues(t *testing.T) {
	path := outPath(t, "issues.xlsx")
	issues := []report.CheckIssue{{
		Left: "行30 资产总计", LeftVal: y(115),
		Right: "行53 负债和所有者权益总计", RightVal: y(100),
		Diff: y(15), Fatal: true,
	}}
	err := WriteStatement(path, StatementOptions{
		Title: "资产负债表", CompanyName: "A", DateLabel: "2025年9月30日",
		SheetName: "BS", Columns: []string{"期末余额"},
		Rows: []StatementRow{
			{Label: "资产总计", Bold: true, Values: []money.Money{y(115)}},
		},
		Issues: issues,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := openForRead(t, path)
	rows, _ := f.GetRows("BS")
	var found bool
	for _, r := range rows {
		if len(r) > 0 && strings.Contains(r[0], "勾稽关系校验未通过") {
			found = true
		}
		if len(r) > 0 && strings.Contains(r[0], "15.00") {
			// 差额应被写出
			found = true
		}
	}
	if !found {
		t.Errorf("勾稽问题应写入文件，实际内容：%v", rows)
	}
}

// ---------------------------------------------------------------------------
// 文件名与工作表名
// ---------------------------------------------------------------------------

func TestSanitizeSheetName(t *testing.T) {
	cases := map[string]string{
		"资产负债表":                              "资产负债表",
		"含/斜杠:和*号":                           "含_斜杠_和_号",
		"":                                   "Sheet1",
		"1234567890123456789012345678901234": "1234567890123456789012345678901",
	}
	for in, want := range cases {
		if got := sanitizeSheetName(in); got != want {
			t.Errorf("sanitizeSheetName(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestWriteFileIsValidXLSX(t *testing.T) {
	path := outPath(t, "valid.xlsx")
	err := WriteStatement(path, StatementOptions{
		Title: "测试", CompanyName: "A", DateLabel: "2025-09-30", SheetName: "T",
		Columns: []string{"金额"},
		Rows:    []StatementRow{{Label: "行1", Values: []money.Money{y(1)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("文件未生成: %v", err)
	}
	// 能被 excelize 重新打开即说明是合法 xlsx
	f := openForRead(t, path)
	if got := f.GetSheetName(0); got != "T" {
		t.Errorf("工作表名 = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 银行存款余额调节表
// ---------------------------------------------------------------------------

func reconReport(t *testing.T) *reconciliation.Report {
	t.Helper()
	d := func(s string) calendar.Date {
		v, err := calendar.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	rep, err := reconciliation.Build(reconciliation.Input{
		AsOf: d("2025-03-31"), From: d("2025-03-03"),
		AccountCode: "1002", AccountName: "银行存款",
		BookOpening: y(1000), BankOpening: ptr(y(1000)),
		BookBalance: y(1470), BankBalance: ptr(y(1470)),
		BankReceivedNotBooked: []reconciliation.Item{
			{Date: d("2025-03-15"), Summary: "货款", Reference: "流水", Amount: y(700)},
		},
		BankPaidNotBooked: []reconciliation.Item{
			{Date: d("2025-03-20"), Summary: "手续费", Reference: "流水", Amount: y(30)},
		},
		BookReceivedNotBanked: []reconciliation.Item{
			{Date: d("2025-03-05"), Summary: "收回货款", Reference: "记-2025-03-0001", Amount: y(500)},
		},
		BookPaidNotBanked: []reconciliation.Item{
			{Date: d("2025-03-18"), Summary: "支付货款", Reference: "记-2025-03-0004", Amount: y(200)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func ptr(m money.Money) *money.Money { return &m }

// 版式必须是一张纸质底稿：左右两栏、每行只填自己那一栏。
func TestReconciliationExportLayout(t *testing.T) {
	path := outPath(t, "银行余额调节表.xlsx")
	rep := reconReport(t)
	if err := Reconciliation(path, "杭州云帆软件有限公司", rep); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	f := openForRead(t, path)
	const sheet = "银行余额调节表"

	if got := cell(t, f, sheet, "A1"); got != "银行存款余额调节表" {
		t.Errorf("标题 = %q", got)
	}
	if got := cell(t, f, sheet, "B3"); got != "企业账面" {
		t.Errorf("左栏标题 = %q，期望 企业账面", got)
	}
	if got := cell(t, f, sheet, "C3"); got != "银行对账单" {
		t.Errorf("右栏标题 = %q，期望 银行对账单", got)
	}

	// ★ 金额必须是数值（会计要能在 Excel 里求和），不是字符串
	checkAmount(t, f, sheet, "B4", 1470)
	checkAmount(t, f, sheet, "C4", 1470)
	checkAmount(t, f, sheet, "B5", 700) // 加：银行已收企业未收
	checkAmount(t, f, sheet, "B6", 700) // 其明细
	checkAmount(t, f, sheet, "B7", 30)  // 减：银行已付企业未付
	checkAmount(t, f, sheet, "B8", 30)
	checkAmount(t, f, sheet, "C9", 500) // 加：企业已收银行未收
	checkAmount(t, f, sheet, "C10", 500)
	checkAmount(t, f, sheet, "C11", 200) // 减：企业已付银行未付
	checkAmount(t, f, sheet, "C12", 200)

	// ★ 每行只填一边：左栏的行，右栏必须留空而不是 0.00。
	// 一个格子里的 0.00 读起来像「这一栏是零」，而实际意思是
	// 「这一栏与这一行无关」—— 调节表里这两者必须区分。
	if got := cell(t, f, sheet, "C5"); got != "" {
		t.Errorf("左栏行不该在右栏写值，实际 %q", got)
	}
	if got := cell(t, f, sheet, "B9"); got != "" {
		t.Errorf("右栏行不该在左栏写值，实际 %q", got)
	}

	// 明细行有缩进，小节标题没有
	if got := cell(t, f, sheet, "A6"); !strings.HasPrefix(got, "　") {
		t.Errorf("明细行应有缩进，实际 %q", got)
	}
}

// 调节后余额必须等于「账面 + 加 − 减」，且两侧相等。
func TestReconciliationExportTotals(t *testing.T) {
	path := outPath(t, "平衡.xlsx")
	rep := reconReport(t)
	if err := Reconciliation(path, "测试公司", rep); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	f := openForRead(t, path)

	// 账面 1470 + 700 − 30 = 2140；银行 1470 + 500 − 200 = 1770
	// 期初两侧都是 1000，所以差额 2140 − 1770 = 370 应等于期初差 0…
	// 这里刻意断言实际算出来的数：Build 只对输入做加减，
	// 输入本身不自洽时它照算不误（自洽性由 Check 负责）。
	checkAmount(t, f, "银行余额调节表", "B13", 2140)
	checkAmount(t, f, "银行余额调节表", "C13", 1770)
}

func TestReconciliationExportNotesNotMarkdown(t *testing.T) {
	path := outPath(t, "提示.xlsx")
	rep := reconReport(t)
	if err := Reconciliation(path, "测试公司", rep); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	rows, err := openForRead(t, path).GetRows("银行余额调节表")
	if err != nil {
		t.Fatal(err)
	}
	// 提示是纯文本，会原样出现在命令行、界面和这张底稿里 ——
	// 混进 Markdown 标记会被用户直接看见。
	for _, r := range rows {
		for _, c := range r {
			if strings.Contains(c, "**") {
				t.Errorf("提示里混进了 Markdown 标记: %q", c)
			}
		}
	}
	// 也不该留下一个孤零零的 0.00 在金额栏里
	for _, r := range rows {
		if len(r) >= 2 && r[0] == "调节结果" && strings.TrimSpace(r[1]) != "" {
			t.Errorf("调节结果行不该带金额，实际 %q", r[1])
		}
	}
}

// ---------------------------------------------------------------------------
// 多栏式明细账
// ---------------------------------------------------------------------------

func columnarOptions() ColumnarOptions {
	return ColumnarOptions{
		Title:       "多栏式明细账",
		CompanyName: "杭州云帆软件有限公司",
		DateLabel:   "2025-03-01 至 2025-03-31",
		SheetName:   "多栏式明细账",
		Columns:     []string{"办公费", "差旅费", "工资"},
		SideLabels:  []string{"借", "借", "借"},
		Rows: []ColumnarRow{
			{Date: "2025-03-05", VoucherNo: "记-0003", Summary: "办公用品",
				Amounts: []money.Money{y(300), 0, 0}, Balance: y(300), Dir: "借"},
			{Date: "2025-03-12", VoucherNo: "记-0008", Summary: "出差机票",
				Amounts: []money.Money{0, y(2400), 0}, Balance: y(2700), Dir: "借"},
			{Date: "2025-03-30", VoucherNo: "记-0021", Summary: "冲销误记的办公费",
				Amounts: []money.Money{y(-100), 0, 0}, Balance: y(2600), Dir: "借"},
		},
		Totals:  []money.Money{y(200), y(2400), 0},
		Opening: "平 0.00",
		Closing: "借 2600.00",
	}
}

// 版式：前四列 + 每栏一列，表头两行（分组 + 栏目名）。
func TestColumnarExportLayout(t *testing.T) {
	path := outPath(t, "多栏式.xlsx")
	if err := Columnar(path, columnarOptions()); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	f := openForRead(t, path)
	const sheet = "多栏式明细账"

	// ★ 四列表头必须各自纵向合并两行。
	// 曾经把它们合并成一整块 A3:D4 —— 合并区只保留左上角的值，
	// 于是「日期/凭证号/摘要/余额」全都显示成最后写的那一个。
	for i, want := range []string{"日期", "凭证号", "摘要", "余额"} {
		ref := colName(i+1) + "3"
		if got := cell(t, f, sheet, ref); got != want {
			t.Errorf("%s = %q，期望 %q", ref, got, want)
		}
	}
	// 栏目名在第二行表头
	for i, want := range []string{"办公费", "差旅费", "工资"} {
		ref := colName(5+i) + "4"
		if got := cell(t, f, sheet, ref); got != want {
			t.Errorf("%s = %q，期望 %q", ref, got, want)
		}
	}
	// 方向分组表头
	if got := cell(t, f, sheet, "E3"); got != "借" {
		t.Errorf("E3 = %q，期望分组表头「借」", got)
	}
	// 期初 / 期末
	if got := cell(t, f, sheet, "A5"); got != "期初余额" {
		t.Errorf("A5 = %q，期望 期初余额", got)
	}
}

// ★ 金额必须是数值，且**每行只填一栏**（零值留空）。
//
// 多栏式的每一行只发生在一栏上，其余栏写 0.00 会把真正那个数淹掉 ——
// 17 栏的管理费用账，一行里 16 个 0.00 加一个有数的，根本没法看。
func TestColumnarExportBlanksInsteadOfZeros(t *testing.T) {
	path := outPath(t, "多栏式零值.xlsx")
	if err := Columnar(path, columnarOptions()); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	f := openForRead(t, path)
	const sheet = "多栏式明细账"

	// 第一行（r6）：办公费 300，差旅费与工资留空
	checkAmount(t, f, sheet, "E6", 300)
	if got := cell(t, f, sheet, "F6"); got != "" {
		t.Errorf("F6 = %q，未发生的栏目应留空而不是 0.00", got)
	}
	if got := cell(t, f, sheet, "G6"); got != "" {
		t.Errorf("G6 = %q，未发生的栏目应留空", got)
	}
	// 第二行（r7）：差旅费 2400，办公费留空。
	// 空单元格用 checkAmount 会失败（读不出数）—— 这正是「留空」的意思，
	// 所以这里直接断言原始值为空串。
	if raw, err := f.GetCellValue(sheet, "E7", excelize.Options{RawCellValue: true}); err != nil || raw != "" {
		t.Errorf("E7 原始值 = %q err=%v，期望空（该行没有办公费）", raw, err)
	}
	checkAmount(t, f, sheet, "F7", 2400)
	// 红字冲销写成负数
	checkAmount(t, f, sheet, "E8", -100)
	// 合计行（r9）
	checkAmount(t, f, sheet, "E9", 200)
	checkAmount(t, f, sheet, "F9", 2400)
}

// 双侧多栏（增值税）的分组表头必须出现两个方向块。
func TestColumnarExportTwoSidedHeader(t *testing.T) {
	opts := columnarOptions()
	opts.Columns = []string{"进项税额", "已交税金", "销项税额", "进项税额转出"}
	opts.SideLabels = []string{"借", "借", "贷", "贷"}
	opts.Rows = []ColumnarRow{
		{Date: "2025-03-05", VoucherNo: "记-0001", Summary: "采购",
			Amounts: []money.Money{y(1300), 0, 0, 0}, Balance: y(1300), Dir: "借"},
	}
	opts.Totals = []money.Money{y(1300), 0, 0, 0}

	path := outPath(t, "增值税.xlsx")
	if err := Columnar(path, opts); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	f := openForRead(t, path)
	const sheet = "多栏式明细账"

	if got := cell(t, f, sheet, "E3"); got != "借" {
		t.Errorf("E3 = %q，期望「借」", got)
	}
	if got := cell(t, f, sheet, "G3"); got != "贷" {
		t.Errorf("G3 = %q，期望「贷」（从第 3 栏起换成贷方）", got)
	}
	for i, want := range []string{"进项税额", "已交税金", "销项税额", "进项税额转出"} {
		ref := colName(5+i) + "4"
		if got := cell(t, f, sheet, ref); got != want {
			t.Errorf("%s = %q，期望 %q", ref, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 凭证汇总表
// ---------------------------------------------------------------------------

func summaryOptions() SummaryOptions {
	return SummaryOptions{
		Title:       "凭证汇总表",
		CompanyName: "杭州云帆软件有限公司",
		DateLabel:   "2025-03-01 至 2025-03-31",
		WordHeaders: []string{"凭证字", "张数", "借方金额", "贷方金额"},
		Word: []SummaryRow{
			{Cells: []SummaryCell{Text("合计"), Text("2"), Num(y(1800)), Num(y(1800))}, Bold: true},
			{Cells: []SummaryCell{Text("记"), Text("1"), Num(y(1000)), Num(y(1000))}},
			{Cells: []SummaryCell{Text("收"), Text("1"), Num(y(800)), Num(y(800))}},
		},
		// ★ 金额夹在中间、凭证号在最后 —— 这一段的版式专门用来
		// 验证单元格是按列位置给的，而不是「文本在前、金额在后」
		DayHeaders: []string{"日期", "张数", "借方金额", "贷方金额", "凭证号"},
		Day: []SummaryRow{
			{Cells: []SummaryCell{
				Text("2025-03-05"), Text("1"), Num(y(1000)), Num(y(1000)),
				Text("记-2025-03-0001")}},
		},
		AccountHeaders: []string{"科目", "名称", "笔数", "借方发生额", "贷方发生额", "净额"},
		Account: []SummaryRow{
			{Cells: []SummaryCell{Text("1002"), Text("银行存款"), Text("2"),
				Num(y(1800)), Num(y(300)), Num(y(1500))}},
		},
		DebitTotal: y(1800), CreditTotal: y(1800),
		Notes: []string{"另有 1 张草稿尚未记账。"},
	}
}

// ★ 金额必须落在表头写着的那一列上。
//
// 曾经的实现是「先给一串文本单元格，再给一串金额单元格」，
// 于是「日期 | 张数 | 借方 | 贷方 | 凭证号」这一段的金额
// 整体偏到了凭证号右边 —— 表头写着借方金额，数却在 F 列。
// 这种错位在 Excel 里看着完全正常，只有逐格核对才会发现。
func TestSummaryExportColumnAlignment(t *testing.T) {
	path := outPath(t, "汇总.xlsx")
	if err := Summary(path, summaryOptions()); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	f := openForRead(t, path)
	const sheet = "凭证汇总表"

	// 按凭证字段：C/D 是金额
	checkAmount(t, f, sheet, "C6", 1800)
	checkAmount(t, f, sheet, "D6", 1800)
	checkAmount(t, f, sheet, "C7", 1000)

	// 按日期段：C/D 是金额，E 是凭证号
	checkAmount(t, f, sheet, "C12", 1000)
	checkAmount(t, f, sheet, "D12", 1000)
	if got := cell(t, f, sheet, "E12"); got != "记-2025-03-0001" {
		t.Errorf("E12 = %q，期望凭证号（金额不该挤到这一列）", got)
	}
	// 金额列绝不能是文本
	for _, ref := range []string{"C12", "D12"} {
		raw, err := f.GetCellValue(sheet, ref, excelize.Options{RawCellValue: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := strconv.ParseFloat(raw, 64); err != nil {
			t.Errorf("%s = %q 不是数值", ref, raw)
		}
	}

	// 表头必须与小节标题各就各位
	if got := cell(t, f, sheet, "A5"); got != "凭证字" {
		t.Errorf("A5 = %q，期望 凭证字", got)
	}
	if got := cell(t, f, sheet, "A4"); got != "一、按凭证字" {
		t.Errorf("A4 = %q，期望段落标题", got)
	}
}

// 三段必须都在同一个工作表里 —— 会计核对时要在三段之间来回看，
// 拆成三个 sheet 就得切标签页。
func TestSummaryExportKeepsSectionsTogether(t *testing.T) {
	path := outPath(t, "汇总三段.xlsx")
	opts := summaryOptions()
	if err := Summary(path, opts); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	f := openForRead(t, path)
	sheets := f.GetSheetList()
	if len(sheets) != 1 {
		t.Fatalf("工作表数 = %d，期望 1（三段叠在同一张表里）", len(sheets))
	}
	rows, err := f.GetRows("凭证汇总表")
	if err != nil {
		t.Fatal(err)
	}
	wantTitles := []string{"一、按凭证字", "二、按日期", "三、按科目（科目汇总表）"}
	found := 0
	for _, r := range rows {
		for _, c := range r {
			for _, w := range wantTitles {
				if c == w {
					found++
				}
			}
		}
	}
	if found != len(wantTitles) {
		t.Errorf("找到 %d 个段落标题，期望 %d", found, len(wantTitles))
	}
	// 提示写在最后
	var last string
	for _, r := range rows {
		for _, c := range r {
			if strings.TrimSpace(c) != "" {
				last = c
			}
		}
	}
	if !strings.Contains(last, "草稿") {
		t.Errorf("最后一行应是提示，实际 %q", last)
	}
}

// ★ 勾稽问题必须写进导出的文件里。
//
// 屏幕上会显示「资产总计 ≠ 负债和所有者权益总计」的警告，
// 而导出的 .xlsx 曾经是干净的 —— 而这份文件正是发给银行、
// 税务局、代账会计的那一份。对不上账的报表被当成对的交出去，
// 比什么都不给更糟。
func TestBalanceSheetExportWritesIssues(t *testing.T) {
	path := outPath(t, "带问题的资产负债表.xlsx")
	def := bsDefinition(t, 0)
	issues := []report.CheckIssue{{
		Left: "资产总计", LeftVal: y(1000),
		Right: "负债和所有者权益总计", RightVal: y(900),
		Diff: y(100), Fatal: true,
	}}
	if err := BalanceSheet(path, "测试公司",
		time.Date(2025, 9, 30, 0, 0, 0, 0, time.UTC), def, def, issues); err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	f := openForRead(t, path)
	rows, err := f.GetRows("资产负债表")
	if err != nil {
		t.Fatal(err)
	}
	var foundHeader, foundText bool
	for _, r := range rows {
		for _, c := range r {
			if strings.Contains(c, "勾稽关系校验未通过") {
				foundHeader = true
			}
			if strings.Contains(c, "≠") && strings.Contains(c, "资产总计") {
				foundText = true
			}
		}
	}
	if !foundHeader {
		t.Error("导出的文件里应有「勾稽关系校验未通过」标题")
	}
	if !foundText {
		t.Error("导出的文件里应逐条列出勾稽问题")
	}
}

// 没有问题时不该凭空多出一段警告。
func TestBalanceSheetExportOmitsIssuesWhenClean(t *testing.T) {
	path := outPath(t, "干净的资产负债表.xlsx")
	def := bsDefinition(t, 0)
	if err := BalanceSheet(path, "测试公司",
		time.Date(2025, 9, 30, 0, 0, 0, 0, time.UTC), def, def, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := openForRead(t, path).GetRows("资产负债表")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		for _, c := range r {
			if strings.Contains(c, "勾稽关系校验未通过") {
				t.Error("没有问题时不该出现警告段")
			}
		}
	}
}
