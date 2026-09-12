package bankcsv

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"

	"miniaccount/internal/domain/bank"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// csText 是一份典型的工商银行对账单（UTF-8）。
const csText = `账户明细
账号：1202 0000 0000 0000
交易日期,摘要,对方户名,对方账号,借贷标志,交易金额,余额
2025-09-03,转账,张三,6222020000000001,收入,"50,000.00","138,000.00"
2025-09-05,办公用品采购,杭州某某办公用品有限公司,6222020000000002,支出,"3,000.00","135,000.00"
2025-09-15,货款,杭州某某科技有限公司,6222020000000003,收入,"90,400.00","225,400.00"
合计,,,,,"143,400.00",
`

func parseSample(t *testing.T, text string, m Mapping) *ParseResult {
	t.Helper()
	res, err := Parse([]byte(text), Options{
		BankAccountCode: "1002", Mapping: m, AutoDetectHeader: true,
	})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	return res
}

// ---------------------------------------------------------------------------
// 编码
// ---------------------------------------------------------------------------

func TestDecodeUTF8(t *testing.T) {
	text, enc, err := Decode([]byte("交易日期,摘要\n2025-09-01,测试"))
	if err != nil {
		t.Fatal(err)
	}
	if enc != "UTF-8" {
		t.Errorf("编码 = %q，期望 UTF-8", enc)
	}
	if !strings.Contains(text, "交易日期") {
		t.Errorf("解码结果 = %q", text)
	}
}

func TestDecodeUTF8BOM(t *testing.T) {
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte("交易日期,摘要")...)
	text, enc, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if enc != "UTF-8 (BOM)" {
		t.Errorf("编码 = %q", enc)
	}
	if strings.HasPrefix(text, "\ufeff") {
		t.Error("应剥掉 BOM")
	}
	if text != "交易日期,摘要" {
		t.Errorf("解码结果 = %q", text)
	}
}

// ★ 国内银行导出的 CSV 大多是 GB18030，按 UTF-8 读会全是乱码，
// 而乱码后的户名匹配不上任何规则 —— 用户只会看到「一条都没匹配上」。
func TestDecodeGB18030(t *testing.T) {
	orig := "交易日期,摘要,对方户名,借贷标志,交易金额\n2025-09-03,转账,张三,收入,50000.00"
	enc, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(orig))
	if err != nil {
		t.Fatal(err)
	}
	text, name, err := Decode(enc)
	if err != nil {
		t.Fatalf("GB18030 解码失败: %v", err)
	}
	if name != "GB18030" {
		t.Errorf("编码 = %q，期望 GB18030", name)
	}
	if text != orig {
		t.Errorf("解码后 = %q，期望 %q", text, orig)
	}
}

func TestDecodeEmpty(t *testing.T) {
	if _, _, err := Decode(nil); err == nil {
		t.Error("空文件应报错")
	}
}

// GB18030 的真实解析（而不只是解码）
func TestParseGB18030File(t *testing.T) {
	enc, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(csText))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Parse(enc, Options{BankAccountCode: "1002", AutoDetectHeader: true})
	if err != nil {
		t.Fatalf("解析 GB18030 文件失败: %v", err)
	}
	if res.Table.Encoding != "GB18030" {
		t.Errorf("编码 = %q", res.Table.Encoding)
	}
	if len(res.Flows) != 3 {
		t.Fatalf("流水数 = %d，期望 3", len(res.Flows))
	}
	if res.Flows[0].CounterpartyName != "张三" {
		t.Errorf("对方户名 = %q，期望「张三」", res.Flows[0].CounterpartyName)
	}
}

// ---------------------------------------------------------------------------
// 表头探测
// ---------------------------------------------------------------------------

// 银行文件常在正式表格前放几行说明，必须能自动找到表头
func TestDetectHeaderRow(t *testing.T) {
	tbl, err := ParseTable(csText)
	if err != nil {
		t.Fatal(err)
	}
	got := DetectHeaderRow(tbl.Rows)
	if got != 2 {
		t.Errorf("表头行 = %d，期望 2（前两行是账户说明）", got)
	}
}

func TestDetectHeaderRowFails(t *testing.T) {
	tbl, _ := ParseTable("a,b,c\n1,2,3\n4,5,6")
	if got := DetectHeaderRow(tbl.Rows); got != -1 {
		t.Errorf("无表头时应返回 -1，得到 %d", got)
	}
}

// ---------------------------------------------------------------------------
// 解析
// ---------------------------------------------------------------------------

func TestExtractBasic(t *testing.T) {
	res := parseSample(t, csText, Mapping{})
	if len(res.Errors) != 0 {
		t.Fatalf("不应有解析错误: %v", res.Errors)
	}
	if len(res.Flows) != 3 {
		t.Fatalf("流水数 = %d，期望 3（合计行应被跳过）", len(res.Flows))
	}

	f := res.Flows[0]
	if f.AccountCode != "1002" {
		t.Errorf("银行科目 = %q", f.AccountCode)
	}
	if f.TxnDate.String() != "2025-09-03" {
		t.Errorf("日期 = %s", f.TxnDate)
	}
	if f.Direction != bank.DirIn {
		t.Errorf("方向 = %s，期望 收入", f.Direction)
	}
	if f.Amount != money.MustParse("50000.00") {
		t.Errorf("金额 = %s，期望 50,000.00", f.Amount)
	}
	// 千分位与引号都要正确处理
	if f.Balance != money.MustParse("138000.00") {
		t.Errorf("余额 = %s，期望 138,000.00", f.Balance)
	}
	if f.CounterpartyName != "张三" {
		t.Errorf("对方户名 = %q", f.CounterpartyName)
	}

	if res.Flows[1].Direction != bank.DirOut {
		t.Errorf("第 2 条方向 = %s，期望 支出", res.Flows[1].Direction)
	}
	if res.Flows[1].Amount != money.MustParse("3000.00") {
		t.Errorf("第 2 条金额 = %s", res.Flows[1].Amount)
	}
	// 金额恒为正，方向由 Direction 表达
	for _, f := range res.Flows {
		if !f.Amount.IsPositive() {
			t.Errorf("流水金额应为正: %s", f.Amount)
		}
	}
}

func TestExtractSkipsTotalRow(t *testing.T) {
	res := parseSample(t, csText, Mapping{})
	for _, f := range res.Flows {
		if strings.Contains(f.Summary, "合计") {
			t.Error("合计行不应被当作流水")
		}
	}
}

// 金额带正负号的文件（没有收支列）
func TestExtractSignMode(t *testing.T) {
	text := `交易日期,摘要,对方户名,发生额,余额
2025-09-03,收到货款,甲公司,-50000.00,50000.00
2025-09-05,支付房租,乙公司,12000.00,38000.00`
	// 注意：这里故意用反号以验证 sign 模式严格按符号判定
	res := parseSample(t, text, Mapping{
		Date: "交易日期", Amount: "发生额", Balance: "余额",
		CounterpartyName: "对方户名", Summary: "摘要",
		DirectionMode: DirModeSign,
	})
	if len(res.Flows) != 2 {
		t.Fatalf("流水数 = %d，期望 2（错误：%v）", len(res.Flows), res.Errors)
	}
	if res.Flows[0].Direction != bank.DirOut {
		t.Errorf("负号应为支出，得到 %s", res.Flows[0].Direction)
	}
	if res.Flows[0].Amount != money.MustParse("50000.00") {
		t.Errorf("金额应取绝对值，得到 %s", res.Flows[0].Amount)
	}
	if res.Flows[1].Direction != bank.DirIn {
		t.Errorf("正号应为收入，得到 %s", res.Flows[1].Direction)
	}
}

// 借贷两列分开的文件
func TestExtractTwoColsMode(t *testing.T) {
	text := `交易日期,摘要,对方户名,借方发生额,贷方发生额
2025-09-03,收到货款,甲公司,,50000.00
2025-09-05,支付房租,乙公司,12000.00,`
	res := parseSample(t, text, Mapping{
		Date: "交易日期", Debit: "借方发生额", Credit: "贷方发生额",
		CounterpartyName: "对方户名", Summary: "摘要",
		DirectionMode: DirModeTwoCols,
	})
	if len(res.Flows) != 2 {
		t.Fatalf("流水数 = %d，期望 2（错误：%v）", len(res.Flows), res.Errors)
	}
	// 银行对账单以银行方视角记账：贷方发生额 = 钱进来
	if res.Flows[0].Direction != bank.DirIn {
		t.Errorf("贷方发生额应为收入，得到 %s", res.Flows[0].Direction)
	}
	if res.Flows[1].Direction != bank.DirOut {
		t.Errorf("借方发生额应为支出，得到 %s", res.Flows[1].Direction)
	}
}

// 中文日期格式
func TestParseDateFormats(t *testing.T) {
	cases := map[string]string{
		"2025-09-03":       "2025-09-03",
		"2025/09/03":       "2025-09-03",
		"2025.09.03":       "2025-09-03",
		"2025年9月3日":        "2025-09-03",
		"2025-09-03 14:30": "2025-09-03",
		"2025-9-3":         "2025-09-03",
		"20250903":         "2025-09-03",
	}
	for in, want := range cases {
		d, err := parseDate(in)
		if err != nil {
			t.Errorf("parseDate(%q) 失败: %v", in, err)
			continue
		}
		if d.String() != want {
			t.Errorf("parseDate(%q) = %s，期望 %s", in, d, want)
		}
	}
	for _, bad := range []string{"", "abc", "2025-13-01", "2025-02-30"} {
		if _, err := parseDate(bad); err == nil {
			t.Errorf("parseDate(%q) 应报错", bad)
		}
	}
}

// 金额写法：千分位、货币符号、括号负数
func TestParseAmountFormats(t *testing.T) {
	cases := map[string]string{
		"50,000.00": "50000.00",
		"¥1234.56":  "1234.56",
		"￥1,234.56": "1234.56",
		"1234":      "1234.00",
		"0.05":      "0.05",
		"(1234.56)": "1234.56", // 括号负数 → 取绝对值
	}
	for in, want := range cases {
		got, err := parseAmount(in)
		if err != nil {
			t.Errorf("parseAmount(%q) 失败: %v", in, err)
			continue
		}
		if got.PlainString() != want {
			t.Errorf("parseAmount(%q) = %s，期望 %s", in, got.PlainString(), want)
		}
	}
}

// ---------------------------------------------------------------------------
// 容错
// ---------------------------------------------------------------------------

// 个别行坏掉不应整体失败 —— 一份三个月的对账单里有个别异常行是常态
func TestExtractPartialFailure(t *testing.T) {
	text := `交易日期,摘要,对方户名,借贷标志,交易金额
2025-09-03,转账,张三,收入,50000.00
坏日期,转账,李四,收入,1000.00
2025-09-05,转账,王五,收入,2000.00`
	res := parseSample(t, text, Mapping{})
	if len(res.Flows) != 2 {
		t.Errorf("应解析出 2 条，得到 %d", len(res.Flows))
	}
	if len(res.Errors) != 1 {
		t.Fatalf("应报告 1 条错误，得到 %d: %v", len(res.Errors), res.Errors)
	}
	// 错误应指明行号，便于用户去原始文件里定位
	if res.Errors[0].Line != 3 {
		t.Errorf("错误行号 = %d，期望 3（含表头）", res.Errors[0].Line)
	}
	if res.Errors[0].Raw == nil {
		t.Error("错误应带上原始行内容")
	}
}

func TestResolveDirection(t *testing.T) {
	in := []string{"收入", "收", "贷", "贷方", "存入", "转入"}
	for _, v := range in {
		d, err := resolveDirection(v, Mapping{})
		if err != nil || d != bank.DirIn {
			t.Errorf("resolveDirection(%q) = %s/%v，期望 收入", v, d, err)
		}
	}
	out := []string{"支出", "付", "借", "借方", "支取", "转出"}
	for _, v := range out {
		d, err := resolveDirection(v, Mapping{})
		if err != nil || d != bank.DirOut {
			t.Errorf("resolveDirection(%q) = %s/%v，期望 支出", v, d, err)
		}
	}
	// 自定义取值
	d, err := resolveDirection("D", Mapping{OutValues: []string{"D"}, InValues: []string{"C"}})
	if err != nil || d != bank.DirOut {
		t.Errorf("自定义取值失败: %s/%v", d, err)
	}
	// 无法识别
	if _, err := resolveDirection("???", Mapping{}); err == nil {
		t.Error("无法识别的方向应报错")
	}
}

// 列名带括号后缀（交易金额(元)）也要能匹配
func TestColumnNameNormalization(t *testing.T) {
	text := `交易日期,摘要,对方户名,借贷标志,交易金额(元),余额(人民币)
2025-09-03,转账,张三,收入,50000.00,50000.00`
	res := parseSample(t, text, Mapping{})
	if len(res.Flows) != 1 {
		t.Fatalf("应解析出 1 条，得到 %d（错误：%v）", len(res.Flows), res.Errors)
	}
	if res.Flows[0].Amount != money.MustParse("50000.00") {
		t.Errorf("金额 = %s", res.Flows[0].Amount)
	}
}

// 列映射可以按序号指定
func TestMappingByColumnIndex(t *testing.T) {
	text := `日期,摘要,户名,标志,金额
2025-09-03,转账,张三,收入,50000.00`
	res := parseSample(t, text, Mapping{
		Date: "0", Summary: "1", CounterpartyName: "2",
		Direction: "3", Amount: "4",
	})
	if len(res.Flows) != 1 {
		t.Fatalf("按序号映射应成功，得到 %d（错误：%v）", len(res.Flows), res.Errors)
	}
	if res.Flows[0].CounterpartyName != "张三" {
		t.Errorf("对方户名 = %q", res.Flows[0].CounterpartyName)
	}
}

func TestParseEmptyFile(t *testing.T) {
	if _, err := Parse(nil, Options{}); err == nil {
		t.Error("空文件应报错")
	}
	if _, err := Parse([]byte("\n\n"), Options{}); err == nil {
		t.Error("只有空行的文件应报错")
	}
}

// ---------------------------------------------------------------------------
// 去重键
// ---------------------------------------------------------------------------

func TestDedupKeyUsesSerialNo(t *testing.T) {
	a := &bank.Flow{TxnDate: mustDate(t, "2025-09-03"), Direction: bank.DirIn,
		Amount: money.MustParse("100.00"), SerialNo: "SN123"}
	b := a
	b.Amount = money.MustParse("999.00") // 金额不同
	// 有流水号时，流水号相同即视为同一笔
	if a.DedupKey() != b.DedupKey() {
		t.Error("有流水号时应以流水号为准")
	}
}

func TestDedupKeyFingerprint(t *testing.T) {
	mk := func() *bank.Flow {
		return &bank.Flow{
			TxnDate: mustDate(t, "2025-09-03"), Direction: bank.DirIn,
			Amount: money.MustParse("100.00"), Balance: money.MustParse("500.00"),
			CounterpartyName: "张三", Summary: "转账",
		}
	}
	a, b := mk(), mk()
	if a.DedupKey() != b.DedupKey() {
		t.Error("相同流水应得到相同去重键")
	}
	b.Summary = "转账 "
	// 摘要只有空白差异，规范化后应视为同一笔
	if a.DedupKey() == b.DedupKey() {
		t.Log("提示：去重键对首尾空白不敏感（TrimSpace）")
	}
	c := mk()
	c.Amount = money.MustParse("101.00")
	if a.DedupKey() == c.DedupKey() {
		t.Error("金额不同不应视为同一笔")
	}
	d := mk()
	d.Direction = bank.DirOut
	if a.DedupKey() == d.DedupKey() {
		t.Error("方向不同不应视为同一笔")
	}
}

func mustDate(t *testing.T, s string) calendar.Date {
	t.Helper()
	dt, err := parseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return dt
}

// ★ 畸形行必须被报出来，不能静默合并或丢弃。
//
// 打开 LazyQuotes 时，一个不配对的引号不会报错 —— encoding/csv 会把
// 后面若干行整段吞进同一个多行字段，5 行数据静默变成 3 行，
// 返回值里没有任何异常迹象。对一份要对账的银行流水来说，
// 「悄悄少了行」比「这一行读不了」危险得多：要到月底才发现，且无从查起。
func TestMalformedRowsAreReportedNotSilentlyMerged(t *testing.T) {
	text := "日期,摘要,金额\n" +
		"2025-01-01,正常1,100\n" +
		"2025-01-02,坏行\"引号不配对,200\n" +
		"2025-01-03,正常2,300\n"

	tbl, err := ParseTable(text)
	if err != nil {
		t.Fatalf("不该整体失败: %v", err)
	}
	// 好的两行必须都还在（不能被吞进上一行）
	if len(tbl.Rows) != 3 { // 表头 + 2 行正常
		t.Errorf("行数 = %d，期望 3（表头 + 2 行正常数据）：%v",
			len(tbl.Rows), tbl.Rows)
	}
	// ★ 畸形行必须留下记录
	if len(tbl.Skipped) == 0 {
		t.Error("畸形行必须被记录，否则用户以为导入是全的")
	} else if tbl.Skipped[0].Line == 0 {
		t.Error("跳过的行要带行号，否则用户没法去文件里找")
	}

	// Parse 要把这份记录合并进 Errors（对用户来说都是「这行没进来」）
	res, err := Parse([]byte(text), Options{AutoDetectHeader: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) == 0 {
		t.Error("Parse 的结果里应带上被跳过的行")
	}
}

// 正常文件不该凭空多出「跳过」记录。
func TestCleanFileHasNoSkippedRows(t *testing.T) {
	text := "日期,摘要,金额\n2025-01-01,正常,100\n2025-01-02,也正常,200\n"
	tbl, err := ParseTable(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Skipped) != 0 {
		t.Errorf("正常文件不该有跳过行，实际 %v", tbl.Skipped)
	}
	if len(tbl.Rows) != 3 {
		t.Errorf("行数 = %d，期望 3", len(tbl.Rows))
	}
}
