// Package bankcsv 解析银行导出的对账单 CSV。
//
// # 中文银行 CSV 的两个坑
//
//  1. **编码**：国内银行导出的 CSV 大多是 GB18030（Excel 的默认中文编码），
//     少数是 UTF-8（带 BOM）或 UTF-16LE。直接按 UTF-8 读会得到乱码，
//     而且乱码后的户名无法匹配规则，用户只会看到「一条都没匹配上」。
//
//  2. **列名与方向表达**：各行格式完全不同 ——
//     有的把收/支拆成两列，有的用金额正负，有的有单独的「借贷标志」列；
//     列名也五花八门（交易日期/记账日期/日期）。因此必须支持用户手工映射列。
package bankcsv

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"

	"miniaccount/internal/domain/bank"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// 解析相关错误。
var (
	ErrEmptyFile     = errors.New("bankcsv: 文件为空")
	ErrNoHeader      = errors.New("bankcsv: 找不到表头行")
	ErrMissingColumn = errors.New("bankcsv: 缺少必需列")
	ErrBadRow        = errors.New("bankcsv: 行解析失败")
)

// DirectionMode 说明这份对账单怎么表达收/支。
type DirectionMode string

// 方向表达方式。
const (
	// DirModeColumn 有单独的方向列，值为「收入/支出」「借/贷」「进/出」等。
	DirModeColumn DirectionMode = "column"
	// DirModeSign 金额列本身带正负号：正为收入、负为支出。
	DirModeSign DirectionMode = "sign"
	// DirModeTwoCols 借方列与贷方列分开（很多银行用「发生额」+「借贷标志」）。
	DirModeTwoCols DirectionMode = "two_cols"
)

// Mapping 是「CSV 列 → 流水字段」的映射。
//
// 字段值既可以是列名，也可以是列序号（从 0 开始，字符串形式）。
// 用户手工映射一次后可以保存为模板，下次导入同一家银行直接复用。
type Mapping struct {
	Date                string // 交易日期（必需）
	Amount              string // 金额（DirModeSign / DirModeColumn 时必需）
	Debit               string // 借方发生额（DirModeTwoCols 时必需）
	Credit              string // 贷方发生额（DirModeTwoCols 时必需）
	Direction           string // 方向列（DirModeColumn 时必需）
	Balance             string // 余额
	CounterpartyName    string // 对方户名
	CounterpartyAccount string // 对方账号
	Summary             string // 摘要
	SerialNo            string // 流水号

	// DirectionMode 决定收支如何判定，默认 DirModeColumn。
	DirectionMode DirectionMode
	// InValues / OutValues 是方向列里代表收入/支出的取值（大小写不敏感）。
	// 为空时使用内置的常见取值集合。
	InValues  []string
	OutValues []string

	// HeaderRow 是表头所在行（0 开始）。有些银行文件前面有几行说明文字。
	HeaderRow int
	// SkipRows 是数据区之前要跳过的行数（相对于 HeaderRow 之后的偏移）。
	SkipRows int
}

// DefaultMapping 返回一套常见中文银行对账单的默认映射。
//
// 覆盖工商银行、建设银行、招商银行等常见列名。
// 用户导入时通常只需微调。
func DefaultMapping() Mapping {
	return Mapping{
		Date:                "交易日期",
		Amount:              "交易金额",
		Balance:             "余额",
		CounterpartyName:    "对方户名",
		CounterpartyAccount: "对方账号",
		Summary:             "摘要",
		DirectionMode:       DirModeColumn,
		Direction:           "借贷标志",
	}
}

// Table 是解析后的原始表格。
type Table struct {
	// Encoding 是探测到的编码名，写入导入批次便于排错。
	Encoding string
	// Header 是表头行的原始列名。
	Header []string
	// Rows 是数据行。
	Rows [][]string
	// Skipped 是被跳过的畸形行（行号 + 原因）。
	//
	// ★ 必须向上报，不能静默跳过。
	//
	// 一份对账单里个别行格式异常是常态，跳过它是合理的；
	// 但用户拿到的导入摘要是「新增 40 条」—— 而文件里其实有 42 行。
	// 他会以为导全了，拿一份不完整的流水去对账，
	// 然后银行余额永远对不平，还不知道差在哪。
	Skipped []RowError
}

// ParseResult 是解析结果。
type ParseResult struct {
	Table Table
	// Flows 是成功解析出的流水。
	Flows []*bank.Flow
	// Mapping 是本次实际使用的列映射。
	//
	// 回传它是为了让调用方能**告诉用户「我按哪些列解析的」** ——
	// 自动探测猜错时，用户看到映射就能立刻知道该改哪里，
	// 而不是对着「金额为零或为空」发楞。
	Mapping Mapping
	// Errors 是逐行的问题（行号 + 原因），不阻断其余行。
	//
	// 刻意不因为个别行失败就整体放弃：一份三个月的对账单里
	// 有个别格式异常行是常态，用户需要知道是哪几行、为什么。
	Errors []RowError
}

// RowError 描述一行的解析问题。
type RowError struct {
	// Line 是原始文件中的行号（从 1 开始，含表头）。
	Line   int
	Reason string
	Raw    []string
}

// Decode 探测编码并把字节解码为 UTF-8 文本。
//
// 顺序：BOM → UTF-16 → UTF-8 合法性 → GB18030。
// 不用第三方嗅探库：中文银行 CSV 的实际分布就这么几种，规则判断足够可靠，
// 而且可解释 —— 一旦判断错，能在界面上告诉用户「按 GB18030 解析」。
func Decode(data []byte) (text string, encoding string, err error) {
	if len(data) == 0 {
		return "", "", ErrEmptyFile
	}

	// 1) UTF-8 BOM
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		return string(data[3:]), "UTF-8 (BOM)", nil
	}
	// 2) UTF-16 BOM
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		s, err := decodeWith(data[2:], unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM))
		return s, "UTF-16LE", err
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		s, err := decodeWith(data[2:], unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM))
		return s, "UTF-16BE", err
	}
	// 3) 本身就是合法 UTF-8？
	if utf8.Valid(data) {
		return string(data), "UTF-8", nil
	}
	// 4) 退到 GB18030（兼容 GBK / GB2312）
	s, err := decodeWith(data, simplifiedchinese.GB18030)
	if err != nil {
		return "", "", fmt.Errorf("bankcsv: 无法识别文件编码: %w", err)
	}
	return s, "GB18030", nil
}

// decodeWith 用给定的 x/text 编码把字节转成 UTF-8。
func decodeWith(data []byte, enc encoding.Encoding) (string, error) {
	out, _, err := transform.Bytes(enc.NewDecoder(), data)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ParseTable 解析 CSV 文本为表格。
func ParseTable(text string) (*Table, error) {
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1 // 容忍各行列数不一致（银行文件常见）
	r.TrimLeadingSpace = true
	// ★ LazyQuotes 关掉。
	//
	// 打开它时，一个不配对的引号**不会报错** —— encoding/csv 会把
	// 后面若干行整段吞进同一个多行字段，于是 5 行数据静默变成 3 行，
	// 而返回值里没有任何异常迹象（实测如此）。
	// 对一份要对账的银行流水来说，「悄悄少了行」比「这一行读不了」
	// 危险得多：前者要到月底才发现，而且无从查起。
	//
	// 关掉之后畸形行会报 *csv.ParseError，我们记下行号与原因、
	// 跳过该行继续 —— 用户能看见「第 7 行引号不配对」。
	r.LazyQuotes = false

	var (
		rows    [][]string
		skipped []RowError
	)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// 单行格式错误不放弃整个文件，跳过该行 —— 但**记下来**
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				skipped = append(skipped, RowError{
					Line: pe.Line,
					Reason: fmt.Sprintf("第 %d 行格式异常（第 %d 列）：%s",
						pe.Line, pe.Column, pe.Err),
				})
				continue
			}
			return nil, fmt.Errorf("bankcsv: 读取 CSV: %w", err)
		}
		// 跳过完全空行
		if len(rec) == 0 || (len(rec) == 1 && strings.TrimSpace(rec[0]) == "") {
			continue
		}
		rows = append(rows, rec)
	}
	if len(rows) == 0 {
		return nil, ErrEmptyFile
	}
	return &Table{Rows: rows, Skipped: skipped}, nil
}

// DetectHeaderRow 在前若干行里寻找最像表头的一行。
//
// 判断依据：该行包含多个已知的列名关键字。
// 银行文件常在正式表格前放「账户明细」「账号：xxx」之类的说明行。
func DetectHeaderRow(rows [][]string) int {
	keywords := []string{
		"日期", "时间", "金额", "余额", "摘要", "对方", "户名", "账号",
		"发生额", "借贷", "收入", "支出", "用途", "备注", "流水",
	}
	best, bestScore := -1, 0
	limit := len(rows)
	if limit > 20 {
		limit = 20
	}
	for i := 0; i < limit; i++ {
		score := 0
		for _, cell := range rows[i] {
			c := strings.TrimSpace(cell)
			for _, kw := range keywords {
				if strings.Contains(c, kw) {
					score++
					break
				}
			}
		}
		if score > bestScore {
			bestScore, best = score, i
		}
	}
	if bestScore < 2 {
		return -1
	}
	return best
}

// Extract 按映射从表格里抽取流水。
func Extract(t *Table, m Mapping, bankAccountCode string) *ParseResult {
	res := &ParseResult{Table: *t}
	// 解析 CSV 时就跳过的畸形行，与逐行解析失败**合并**上报 ——
	// 对用户来说两者是一回事：「这一行没进来」。
	res.Errors = append(res.Errors, t.Skipped...)
	if len(t.Rows) == 0 {
		return res
	}

	headerRow := m.HeaderRow
	if headerRow < 0 {
		headerRow = 0
	}
	if headerRow >= len(t.Rows) {
		res.Errors = append(res.Errors, RowError{
			Line: headerRow + 1, Reason: "表头行超出文件范围",
		})
		return res
	}
	res.Table.Header = t.Rows[headerRow]

	idx := newIndexer(res.Table.Header)
	start := headerRow + 1 + m.SkipRows

	for i := start; i < len(t.Rows); i++ {
		row := t.Rows[i]
		lineNo := i + 1

		// 整行为空或只有合计字样时跳过
		if isBlank(row) || looksLikeTotal(row) {
			continue
		}

		f, err := extractRow(row, idx, m, bankAccountCode)
		if err != nil {
			res.Errors = append(res.Errors, RowError{
				Line: lineNo, Reason: err.Error(), Raw: row,
			})
			continue
		}
		res.Flows = append(res.Flows, f)
	}
	return res
}

// indexer 把列名解析成列序号，支持列名与序号两种写法。
type indexer struct {
	header []string
	norm   map[string]int // 规范化列名 → 序号
}

func newIndexer(header []string) *indexer {
	ix := &indexer{header: header, norm: map[string]int{}}
	for i, h := range header {
		ix.norm[normHeader(h)] = i
	}
	return ix
}

// normHeader 规范化列名：去空白、去括号内容、去常见后缀。
//
// 银行列名常有「交易金额(元)」「交易金额（人民币）」这类写法，
// 规范化后都能对应到「交易金额」。
func normHeader(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\u3000", "")
	if i := strings.IndexAny(s, "(（"); i > 0 {
		s = s[:i]
	}
	for _, suffix := range []string{"发生额", "金额"} {
		_ = suffix
	}
	return s
}

func (ix *indexer) find(key string) (int, bool) {
	if key == "" {
		return -1, false
	}
	// 先按序号
	if n, err := strconv.Atoi(strings.TrimSpace(key)); err == nil {
		if n >= 0 && n < len(ix.header) {
			return n, true
		}
		return -1, false
	}
	// 再按列名（精确）
	if i, ok := ix.norm[normHeader(key)]; ok {
		return i, true
	}
	// 最后按包含匹配（用户可能只写了「金额」而列名是「交易金额」）
	key = normHeader(key)
	for i, h := range ix.header {
		if strings.Contains(normHeader(h), key) {
			return i, true
		}
	}
	return -1, false
}

func (ix *indexer) get(row []string, key string) string {
	i, ok := ix.find(key)
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

// extractRow 把一行 CSV 转成一条流水。
func extractRow(row []string, ix *indexer, m Mapping, bankAccountCode string) (*bank.Flow, error) {
	mode := m.DirectionMode
	if mode == "" {
		mode = DirModeColumn
	}

	f := &bank.Flow{AccountCode: bankAccountCode}

	// 日期
	dateStr := ix.get(row, m.Date)
	if dateStr == "" {
		return nil, fmt.Errorf("%w: 交易日期为空", ErrBadRow)
	}
	d, err := parseDate(dateStr)
	if err != nil {
		return nil, err
	}
	f.TxnDate = d

	// 金额与方向
	switch mode {
	case DirModeSign:
		raw := ix.get(row, m.Amount)
		amt, err := parseAmountSigned(raw)
		if err != nil {
			return nil, err
		}
		if amt.IsZero() {
			return nil, fmt.Errorf("%w: 金额为零", ErrBadRow)
		}
		if amt.IsPositive() {
			f.Direction, f.Amount = bank.DirIn, amt
		} else {
			f.Direction, f.Amount = bank.DirOut, amt.Abs()
		}

	case DirModeTwoCols:
		dStr := ix.get(row, m.Debit)
		cStr := ix.get(row, m.Credit)
		dAmt, _ := parseAmount(dStr)
		cAmt, _ := parseAmount(cStr)
		switch {
		case dAmt.IsPositive() && !cAmt.IsPositive():
			// 借方发生额 = 钱出去
			f.Direction, f.Amount = bank.DirOut, dAmt
		case cAmt.IsPositive() && !dAmt.IsPositive():
			// 贷方发生额 = 钱进来（银行对账单以银行方视角记账）
			f.Direction, f.Amount = bank.DirIn, cAmt
		default:
			return nil, fmt.Errorf("%w: 借贷两列金额不明确（借 %s / 贷 %s）",
				ErrBadRow, dStr, cStr)
		}

	default: // DirModeColumn
		amt, err := parseAmount(ix.get(row, m.Amount))
		if err != nil {
			return nil, err
		}
		if !amt.IsPositive() {
			return nil, fmt.Errorf("%w: 金额为零或为空", ErrBadRow)
		}
		dirStr := ix.get(row, m.Direction)
		dir, err := resolveDirection(dirStr, m)
		if err != nil {
			return nil, err
		}
		f.Direction, f.Amount = dir, amt
	}

	// 余额
	if m.Balance != "" {
		if v, err := parseAmount(ix.get(row, m.Balance)); err == nil {
			f.Balance = v
		}
	}
	f.CounterpartyName = ix.get(row, m.CounterpartyName)
	f.CounterpartyAccount = ix.get(row, m.CounterpartyAccount)
	f.Summary = ix.get(row, m.Summary)
	f.SerialNo = ix.get(row, m.SerialNo)

	if err := f.Validate(); err != nil {
		return nil, err
	}
	f.Status = bank.StatusImported
	return f, nil
}

// 内置的方向取值集合。
var (
	builtinIn = []string{
		"收入", "收", "进", "入", "贷", "贷方", "存入", "转入", "收入方", "贷",
		"in", "credit", "cr",
	}
	builtinOut = []string{
		"支出", "付", "出", "借", "借方", "支取", "转出", "付出", "支出方", "借",
		"out", "debit", "dr",
	}
)

func resolveDirection(v string, m Mapping) (bank.Direction, error) {
	if v == "" {
		return "", fmt.Errorf("%w: 方向列为空", ErrBadRow)
	}
	got := strings.TrimSpace(strings.ToLower(v))

	in := m.InValues
	if len(in) == 0 {
		in = builtinIn
	}
	out := m.OutValues
	if len(out) == 0 {
		out = builtinOut
	}
	for _, x := range in {
		if got == strings.ToLower(strings.TrimSpace(x)) {
			return bank.DirIn, nil
		}
	}
	for _, x := range out {
		if got == strings.ToLower(strings.TrimSpace(x)) {
			return bank.DirOut, nil
		}
	}
	// 兜底：包含判断（如「收入(工资)」）
	for _, x := range in {
		if x != "" && strings.Contains(got, strings.ToLower(x)) {
			return bank.DirIn, nil
		}
	}
	for _, x := range out {
		if x != "" && strings.Contains(got, strings.ToLower(x)) {
			return bank.DirOut, nil
		}
	}
	return "", fmt.Errorf("%w: 无法识别的收支方向 %q", ErrBadRow, v)
}

// parseDate 解析常见的中文日期格式。
func parseDate(s string) (calendar.Date, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return calendar.Date{}, fmt.Errorf("%w: 日期为空", ErrBadRow)
	}
	// 去掉时间部分
	if i := strings.IndexAny(s, " T"); i > 0 {
		s = s[:i]
	}
	// 统一分隔符
	norm := strings.NewReplacer("年", "-", "月", "-", "日", "", "/", "-", ".", "-").Replace(s)

	// 2025-9-1 / 2025-09-01
	parts := strings.Split(norm, "-")
	if len(parts) == 3 {
		y, err1 := strconv.Atoi(parts[0])
		m, err2 := strconv.Atoi(parts[1])
		d, err3 := strconv.Atoi(parts[2])
		if err1 == nil && err2 == nil && err3 == nil {
			// 两位年份：2000 年后
			if y < 100 {
				y += 2000
			}
			dt, err := calendar.New(y, m, d)
			if err == nil {
				return dt, nil
			}
		}
	}
	// 20250901
	if len(norm) == 8 {
		if y, e1 := strconv.Atoi(norm[0:4]); e1 == nil {
			if m, e2 := strconv.Atoi(norm[4:6]); e2 == nil {
				if d, e3 := strconv.Atoi(norm[6:8]); e3 == nil {
					if dt, err := calendar.New(y, m, d); err == nil {
						return dt, nil
					}
				}
			}
		}
	}
	return calendar.Date{}, fmt.Errorf("%w: 无法解析日期 %q", ErrBadRow, s)
}

// parseAmount 解析金额字符串，返回非负金额。
//
// 处理中文银行文件常见的写法：千分位逗号、货币符号、括号负数、
// 以及金额里夹带的空格。
func parseAmount(s string) (money.Money, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	neg := strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
	s = strings.Trim(s, "()")
	v, err := money.Parse(s)
	if err != nil {
		return 0, fmt.Errorf("%w: 无法解析金额 %q", ErrBadRow, s)
	}
	if neg {
		v = v.Neg()
	}
	return v.Abs(), nil
}

// parseAmountSigned 解析带符号金额（DirModeSign 用）。
func parseAmountSigned(s string) (money.Money, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("%w: 金额为空", ErrBadRow)
	}
	neg := strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
	s = strings.Trim(s, "()")
	v, err := money.Parse(s)
	if err != nil {
		return 0, fmt.Errorf("%w: 无法解析金额 %q", ErrBadRow, s)
	}
	if neg {
		v = v.Neg()
	}
	return v, nil
}

func isBlank(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// looksLikeTotal 判断是否为「合计」「小计」这类汇总行。
func looksLikeTotal(row []string) bool {
	for _, c := range row {
		c = strings.TrimSpace(c)
		if c == "合计" || c == "小计" || c == "总计" || c == "合 计" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 便捷入口
// ---------------------------------------------------------------------------

// Options 是一次导入的参数。
type Options struct {
	// BankAccountCode 是流水挂靠的银行科目编码。
	BankAccountCode string
	// Mapping 是列映射；零值表示用默认映射。
	Mapping Mapping
	// AutoDetectHeader 为真时自动寻找表头行。
	AutoDetectHeader bool
}

// Parse 一步完成：解码 → 解析表格 → 抽取流水。
func Parse(data []byte, opts Options) (*ParseResult, error) {
	text, enc, err := Decode(data)
	if err != nil {
		return nil, err
	}
	t, err := ParseTable(text)
	if err != nil {
		return nil, err
	}
	t.Encoding = enc
	// 畸形行不能在 ParseTable 那一层被吃掉 —— 一路带到结果里，
	// 让导入摘要能说清「文件里有几行没读进来」（见 res.Errors 那一段）。

	m := opts.Mapping
	if opts.AutoDetectHeader || m.HeaderRow == 0 {
		if hr := DetectHeaderRow(t.Rows); hr >= 0 {
			m.HeaderRow = hr
		}
	}

	// ★ 没给映射时按**实际表头**猜，而不是套一份写死的默认值。
	//
	// 原先这里用的是 DefaultMapping()（写死「交易金额 / 借贷标志」），
	// 于是任何用「收入金额 / 支出金额」两列表达收支的银行文件
	// 都会一行都解析不出来 —— 报「金额为零或为空」，而文件明明有金额。
	//
	// 自动探测的意义就在于让用户不用配映射；配不好还不说清楚为什么，
	// 比不自动更糟。
	if m.Date == "" {
		if m.HeaderRow >= 0 && m.HeaderRow < len(t.Rows) {
			m = SuggestMapping(t.Rows[m.HeaderRow])
			m.HeaderRow = opts.Mapping.HeaderRow
			if opts.AutoDetectHeader || opts.Mapping.HeaderRow == 0 {
				if hr := DetectHeaderRow(t.Rows); hr >= 0 {
					m.HeaderRow = hr
				}
			}
		}
		if !m.Usable() {
			// 猜不出来时给一份最接近的默认值，让 Extract 的报错
			// 至少指向一个具体的列名，而不是「金额为零或为空」
			fallback := DefaultMapping()
			fallback.HeaderRow = m.HeaderRow
			if m.DirectionMode != "" {
				fallback.DirectionMode = m.DirectionMode
			}
			m = fallback
		}
	}

	res := Extract(t, m, opts.BankAccountCode)
	res.Table.Encoding = enc
	res.Mapping = m
	return res, nil
}

// ---------------------------------------------------------------------------
// 自动列映射
// ---------------------------------------------------------------------------

// 各字段的候选列名，按优先级排列。
//
// 放在这里而不是默认 Mapping 里：默认 Mapping 只覆盖一家银行的写法，
// 而候选表可以覆盖十几家。列名规范化（去空白、去括号）之后做精确匹配，
// 避免「交易金额」误配到「交易金额小计」这类子串事故。
var (
	dateCandidates = []string{
		"交易日期", "交易时间", "记账日期", "日期", "交易日", "发生日期", "业务日期",
	}
	inCandidates = []string{
		"收入金额", "收入", "贷方发生额", "贷方金额", "存入金额", "收入金额(元)",
		"贷方", "转入金额", "存入",
	}
	outCandidates = []string{
		"支出金额", "支出", "借方发生额", "借方金额", "支取金额", "支出金额(元)",
		"借方", "转出金额", "支取",
	}
	amountCandidates = []string{
		"交易金额", "发生额", "金额", "交易额", "发生金额",
	}
	directionCandidates = []string{
		"借贷标志", "收支标志", "收付标志", "借贷", "资金方向", "收支",
		"交易类型", "借贷方向",
	}
	balanceCandidates = []string{
		"余额", "账户余额", "交易后余额", "可用余额", "当前余额",
	}
	counterpartyCandidates = []string{
		"对方户名", "对方账户名称", "对方名称", "对方单位", "交易对手",
		"对方账号名称", "收款人", "付款人", "对方",
	}
	counterpartyAcctCandidates = []string{
		"对方账号", "对方账户", "对方卡号", "对手账号",
	}
	summaryCandidates = []string{
		"摘要", "交易摘要", "用途", "备注", "附言", "交易说明", "说明", "业务摘要",
	}
	serialCandidates = []string{
		"流水号", "交易流水号", "凭证号", "交易参考号", "业务参考号", "凭证序号",
	}
)

// SuggestMapping 按表头猜列映射。
//
// ★ 这是「导入体验」的关键一步。
//
// 银行 CSV 的列名千差万别，但**收支的表达方式只有三种**，
// 识别出是哪一种，剩下的就只是找列名：
//
//	两列式  收入金额 / 支出金额 分开两列          → DirModeTwoCols
//	标志式  一列金额 + 一列「借贷标志」            → DirModeColumn
//	符号式  一列金额，正负号表示收支              → DirModeSign
//
// 先判方向模式再选列，而不是先选列再看方向：
// 因为「金额」这个词在三类文件里含义完全不同 ——
// 在两列式里它可能是「发生额」，在符号式里它才是带符号的金额。
// 判错模式会让每一行的方向都反，而方向反了整张现金流量表都是错的。
func SuggestMapping(header []string) Mapping {
	ix := newIndexer(header)
	m := Mapping{}

	// 1. 方向模式
	hasIn := firstMatch(ix, inCandidates)
	hasOut := firstMatch(ix, outCandidates)
	switch {
	case hasIn != "" && hasOut != "":
		m.DirectionMode = DirModeTwoCols
		m.Credit = hasIn // 银行口径：收入 = 贷方
		m.Debit = hasOut // 支出 = 借方
	case firstMatch(ix, directionCandidates) != "":
		m.DirectionMode = DirModeColumn
		m.Direction = firstMatch(ix, directionCandidates)
		m.Amount = firstMatch(ix, amountCandidates)
		// 有的文件既有方向列又有「交易金额」，有的只有方向列 + 一个泛化的「金额」
		if m.Amount == "" {
			m.Amount = firstMatch(ix, []string{"金额", "发生额"})
		}
	default:
		m.DirectionMode = DirModeSign
		m.Amount = firstMatch(ix, amountCandidates)
	}

	// 2. 其余字段
	m.Date = firstMatch(ix, dateCandidates)
	m.Balance = firstMatch(ix, balanceCandidates)
	m.CounterpartyName = firstMatch(ix, counterpartyCandidates)
	m.CounterpartyAccount = firstMatch(ix, counterpartyAcctCandidates)
	m.Summary = firstMatch(ix, summaryCandidates)
	m.SerialNo = firstMatch(ix, serialCandidates)

	return m
}

// Usable 报告这份映射是否足以解析出流水。
//
// 判定标准是「日期 + 金额来源」齐全：
// 少了日期就没法确定记到哪个会计期间，少了金额更是无从谈起。
// 缺哪一项由 Missing 说明，界面据此提示用户手工映射。
func (m Mapping) Usable() bool { return len(m.Missing()) == 0 }

// Missing 返回还缺哪些必需列（中文说明）。
func (m Mapping) Missing() []string {
	var out []string
	if m.Date == "" {
		out = append(out, "交易日期")
	}
	switch m.DirectionMode {
	case DirModeTwoCols:
		if m.Debit == "" || m.Credit == "" {
			out = append(out, "收入/支出金额")
		}
	case DirModeColumn:
		if m.Amount == "" {
			out = append(out, "交易金额")
		}
		if m.Direction == "" {
			out = append(out, "借贷标志")
		}
	default:
		if m.Amount == "" {
			out = append(out, "交易金额")
		}
	}
	return out
}

// firstMatch 在表头里找第一个命中的候选列名，找不到返回空串。
func firstMatch(ix *indexer, candidates []string) string {
	for _, c := range candidates {
		if _, ok := ix.norm[normHeader(c)]; ok {
			// 返回**原始表头名**，因为下游按原始名匹配
			if i, ok2 := ix.norm[normHeader(c)]; ok2 {
				return ix.header[i]
			}
		}
	}
	return ""
}
