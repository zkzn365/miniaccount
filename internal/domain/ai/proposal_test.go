package ai

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
)

// ---------------------------------------------------------------------------
// 测试脚手架
// ---------------------------------------------------------------------------

// testCtx 构造一个小而全的校验上下文：
//
//	1002 银行存款        无辅助核算
//	1122 应收账款        要求客户
//	2202 应付账款        要求供应商
//	1001 库存现金        无辅助核算
//	5001 主营业务收入    无辅助核算
//	3103 本年利润        无辅助核算
//	1111 汇总科目        不可记账
//
// 期间 2025-01 ~ 2025-03，其中 2025-02 已结账。
func testCtx(t *testing.T) *ledger.Context {
	t.Helper()
	tree, err := account.NewTree([]*account.Account{
		{Code: "1001", Name: "库存现金", RootType: account.RootAsset, IsLeaf: true,
			BalanceDir: account.DirDebit, Level: 1, IsEnabled: true},
		{Code: "1002", Name: "银行存款", RootType: account.RootAsset, IsLeaf: true,
			BalanceDir: account.DirDebit, Level: 1, IsEnabled: true},
		{Code: "1122", Name: "应收账款", RootType: account.RootAsset, IsLeaf: true,
			BalanceDir: account.DirDebit, Level: 1, IsEnabled: true,
			AuxTypes: []account.AuxType{account.AuxCustomer}},
		{Code: "2202", Name: "应付账款", RootType: account.RootLiability, IsLeaf: true,
			BalanceDir: account.DirCredit, Level: 1, IsEnabled: true,
			AuxTypes: []account.AuxType{account.AuxSupplier}},
		{Code: "3103", Name: "本年利润", RootType: account.RootEquity, IsLeaf: true,
			BalanceDir: account.DirCredit, Level: 1, IsEnabled: true},
		{Code: "5001", Name: "主营业务收入", RootType: account.RootIncome, IsLeaf: true,
			BalanceDir: account.DirCredit, Level: 1, IsEnabled: true,
			AuxTypes: []account.AuxType{account.AuxDept}},
		// 汇总科目：有下级，不可记账
		{Code: "1111", Name: "应收票据汇总", RootType: account.RootAsset, IsLeaf: false,
			BalanceDir: account.DirDebit, Level: 1, IsEnabled: true},
		{Code: "111101", Name: "应收票据—银行承兑", ParentCode: "1111",
			RootType: account.RootAsset, IsLeaf: true, BalanceDir: account.DirDebit,
			Level: 2, IsEnabled: true},
	})
	if err != nil {
		t.Fatalf("构造科目树失败: %v", err)
	}
	cal, err := period.NewCalendar(2025, 1, 2025, period.NewKey(2025, 3))
	if err != nil {
		t.Fatal(err)
	}
	// 把 2 月标为已结账
	if p, ok := cal.Get(2025, 2); ok {
		p.Status = period.StatusClosed
	}
	return &ledger.Context{
		Accounts: tree, Periods: cal,
		ContactKinds: map[int64]string{
			1: "customer", 2: "supplier", 3: "shareholder", 4: "employee",
		},
	}
}

// goodJSON 是一份处处合法的模型输出。
const goodJSON = `{
  "voucher": {
    "word": "记",
    "biz_date": "2025-03-11",
    "remark": "收到张三股东借款",
    "entries": [
      {"summary": "收到借款", "account_code": "1002", "debit": 5000000, "credit": 0},
      {"summary": "收到借款", "account_code": "3103", "debit": 0, "credit": 5000000}
    ]
  },
  "confidence": 0.92,
  "reasoning": "摘要含『借款』且历史同类分录均记入本年利润",
  "evidence": ["voucher:记-2025-01-0012"],
  "warnings": []
}`

// num 把字面量包成 json.Number（模拟解码后的形态）。
func num(s string) json.Number { return json.Number(s) }

func mustParse(t *testing.T, raw string) *Proposal {
	t.Helper()
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	return p
}

func checkByKey(t *testing.T, r *Report, key string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("报告里没有 %q 这一项，实际有 %v", key, keysOf(r))
	return Check{}
}

func keysOf(r *Report) []string {
	out := make([]string, 0, len(r.Checks))
	for _, c := range r.Checks {
		out = append(out, c.Key)
	}
	return out
}

// ---------------------------------------------------------------------------
// 解析：严格性
// ---------------------------------------------------------------------------

func TestParseAcceptsGoodJSON(t *testing.T) {
	p := mustParse(t, goodJSON)
	if p.Confidence != 0.92 {
		t.Errorf("置信度 = %v", p.Confidence)
	}
	if len(p.Voucher.Entries) != 2 {
		t.Fatalf("分录数 = %d", len(p.Voucher.Entries))
	}
	if p.Voucher.Entries[0].AccountCode != "1002" {
		t.Errorf("科目 = %s", p.Voucher.Entries[0].AccountCode)
	}
}

// 容忍 ```json 围栏：本地模型常见，且不改变语义
func TestParseStripsCodeFence(t *testing.T) {
	p := mustParse(t, "```json\n"+goodJSON+"\n```")
	if p.Voucher.BizDate != "2025-03-11" {
		t.Errorf("日期 = %s", p.Voucher.BizDate)
	}
}

// ★ 未知字段必须拒绝：多返回一个字段说明模型没按契约走
func TestParseRejectsUnknownField(t *testing.T) {
	bad := `{"voucher":{"word":"记","biz_date":"2025-03-11","remark":"x",
	  "entries":[]},"confidence":0.9,"reasoning":"r","evidence":[],
	  "warnings":[],"extra_field":123}`
	_, err := Parse(bad)
	if !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("未知字段应被拒绝，实际 %v", err)
	}
	// 分录里的未知字段同样拒绝
	bad2 := strings.Replace(goodJSON, `"credit": 0}`, `"credit": 0, "tax_rate": 0.13}`, 1)
	if _, err := Parse(bad2); !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("分录内未知字段应被拒绝，实际 %v", err)
	}
}

// ★ 重复键必须拒绝：encoding/json 默认「后者胜出」，
// 于是被校验过的值与最终生效的值可以不是同一个
func TestParseRejectsDuplicateKeys(t *testing.T) {
	bad := strings.Replace(goodJSON,
		`"account_code": "1002"`, `"account_code": "1002", "account_code": "9999"`, 1)
	_, err := Parse(bad)
	if !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("重复键应被拒绝，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "重复的键") {
		t.Errorf("错误信息应点明重复键，实际 %v", err)
	}
}

func TestParseRejectsTrailingContent(t *testing.T) {
	if _, err := Parse(goodJSON + `{"another":1}`); !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("尾随内容应被拒绝，实际 %v", err)
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	for _, s := range []string{"", "   ", "```json\n```"} {
		if _, err := Parse(s); !errors.Is(err, ErrMalformedJSON) {
			t.Errorf("空输入 %q 应报 ErrMalformedJSON，实际 %v", s, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 金额：绝不接受浮点、字符串、指数
// ---------------------------------------------------------------------------

func TestParseCentsStrict(t *testing.T) {
	ok := map[string]money.Money{
		"0": 0, "1": 1, "5000": 5000, "999999999999": 999999999999,
	}
	for lit, want := range ok {
		got, err := ParseCents(num(lit))
		if err != nil {
			t.Errorf("ParseCents(%s) 报错: %v", lit, err)
			continue
		}
		if got != want {
			t.Errorf("ParseCents(%s) = %d，期望 %d", lit, got, want)
		}
	}
	bad := []string{"5000.00", "-1", "1e3", "0x10", " 5000", "5000 ", "+1", "abc", ""}
	for _, lit := range bad {
		if _, err := ParseCents(num(lit)); !errors.Is(err, ErrBadAmount) {
			t.Errorf("ParseCents(%q) 应被拒绝，实际 %v", lit, err)
		}
	}
}

// ★ 字符串形式的金额必须拒绝 —— 用 int64 反序列化会把它悄悄变成合法值
func TestParseRejectsQuotedAmount(t *testing.T) {
	bad := strings.Replace(goodJSON, `"debit": 5000000`, `"debit": "5000000"`, 1)
	if _, err := Parse(bad); !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("字符串金额应被拒绝，实际 %v", err)
	}
}

func TestParseRejectsDecimalAmount(t *testing.T) {
	bad := strings.Replace(goodJSON, `"debit": 5000000`, `"debit": 5000.00`, 1)
	p, err := Parse(bad)
	if err != nil {
		t.Fatalf("解析阶段不该报错（金额合法性由护栏判断）: %v", err)
	}
	r, err := p.Validate(testCtx(t), Expect{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Passed() {
		t.Fatal("小数金额必须被护栏拦下")
	}
	c := checkByKey(t, r, "amount")
	if c.Level != CheckFail {
		t.Errorf("金额项应为 fail，实际 %s：%s", c.Level, c.Detail)
	}
}

// ---------------------------------------------------------------------------
// 护栏：科目
// ---------------------------------------------------------------------------

// ★ AI 最常见的失效 —— 幻觉一个不存在的科目编码
func TestGuardrailRejectsHallucinatedAccount(t *testing.T) {
	p := mustParse(t, strings.Replace(goodJSON, `"1002"`, `"6666"`, 1))
	r, err := p.Validate(testCtx(t), Expect{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Passed() {
		t.Fatal("不存在的科目必须被拦下")
	}
	c := checkByKey(t, r, "account")
	if !strings.Contains(c.Detail, "6666") {
		t.Errorf("报错应点明科目编码，实际 %q", c.Detail)
	}
}

func TestGuardrailRejectsGroupAccount(t *testing.T) {
	p := mustParse(t, strings.Replace(goodJSON, `"1002"`, `"1111"`, 1))
	r, _ := p.Validate(testCtx(t), Expect{})
	if r.Passed() {
		t.Fatal("汇总科目不能记账")
	}
	c := checkByKey(t, r, "account")
	if !strings.Contains(c.Detail, "汇总") && !strings.Contains(c.Detail, "明细") {
		t.Errorf("报错应说明原因，实际 %q", c.Detail)
	}
}

// ---------------------------------------------------------------------------
// 护栏：借贷与金额
// ---------------------------------------------------------------------------

// ★ 绝不自动调平：差额必须暴露，不能用尾差科目抹平
func TestGuardrailRejectsUnbalanced(t *testing.T) {
	p := mustParse(t, strings.Replace(goodJSON, `"credit": 5000000`, `"credit": 4999999`, 1))
	r, _ := p.Validate(testCtx(t), Expect{})
	if r.Passed() {
		t.Fatal("借贷不平必须被拦下")
	}
	c := checkByKey(t, r, "balanced")
	if !strings.Contains(c.Detail, "差额") {
		t.Errorf("应报出差额，实际 %q", c.Detail)
	}
	// 报告里不能出现任何「自动调平」的痕迹
	for _, c := range r.Checks {
		if strings.Contains(c.Detail, "已调整") || strings.Contains(c.Detail, "已调平") {
			t.Errorf("护栏不得自动调平，却出现了：%s", c.Detail)
		}
	}
}

func TestGuardrailRejectsBothSides(t *testing.T) {
	bad := strings.Replace(goodJSON, `"debit": 5000000, "credit": 0`,
		`"debit": 5000000, "credit": 5000000`, 1)
	p := mustParse(t, bad)
	r, _ := p.Validate(testCtx(t), Expect{})
	if r.Passed() {
		t.Fatal("借贷同时有金额必须被拦下")
	}
}

func TestGuardrailRejectsZeroAmount(t *testing.T) {
	bad := strings.Replace(goodJSON, `"debit": 5000000`, `"debit": 0`, 1)
	p := mustParse(t, bad)
	r, _ := p.Validate(testCtx(t), Expect{})
	if r.Passed() {
		t.Fatal("金额为零必须被拦下")
	}
}

func TestGuardrailRejectsEmptySummary(t *testing.T) {
	bad := strings.Replace(goodJSON, `"summary": "收到借款", "account_code": "1002"`,
		`"summary": "", "account_code": "1002"`, 1)
	p := mustParse(t, bad)
	r, _ := p.Validate(testCtx(t), Expect{})
	if r.Passed() {
		t.Fatal("空摘要必须被拦下")
	}
}

func TestGuardrailRejectsSingleEntry(t *testing.T) {
	bad := `{"voucher":{"word":"记","biz_date":"2025-03-11","remark":"x",
	  "entries":[{"summary":"s","account_code":"1002","debit":100,"credit":0}]},
	  "confidence":0.9,"reasoning":"r","evidence":[],"warnings":[]}`
	p := mustParse(t, bad)
	r, _ := p.Validate(testCtx(t), Expect{})
	if r.Passed() {
		t.Fatal("单条分录必须被拦下")
	}
}

func TestGuardrailRejectsNoEntries(t *testing.T) {
	bad := strings.Replace(goodJSON,
		`"entries": [
      {"summary": "收到借款", "account_code": "1002", "debit": 5000000, "credit": 0},
      {"summary": "收到借款", "account_code": "3103", "debit": 0, "credit": 5000000}
    ]`,
		`"entries": []`, 1)
	p := mustParse(t, bad)
	r, _ := p.Validate(testCtx(t), Expect{})
	if r.Passed() {
		t.Fatal("空提议必须被拦下")
	}
}

// ---------------------------------------------------------------------------
// 护栏：日期与期间
// ---------------------------------------------------------------------------

func TestGuardrailRejectsClosedPeriod(t *testing.T) {
	p := mustParse(t, strings.Replace(goodJSON, "2025-03-11", "2025-02-11", 1))
	r, _ := p.Validate(testCtx(t), Expect{})
	if r.Passed() {
		t.Fatal("已结账期间必须被拦下")
	}
	c := checkByKey(t, r, "biz_date")
	if !strings.Contains(c.Detail, "已结账") {
		t.Errorf("应说明期间已结账，实际 %q", c.Detail)
	}
}

func TestGuardrailRejectsFuturePeriod(t *testing.T) {
	p := mustParse(t, strings.Replace(goodJSON, "2025-03-11", "2025-08-11", 1))
	r, _ := p.Validate(testCtx(t), Expect{})
	if r.Passed() {
		t.Fatal("未启用期间必须被拦下")
	}
	c := checkByKey(t, r, "biz_date")
	if !strings.Contains(c.Detail, "尚未启用") {
		t.Errorf("应说明期间未启用，实际 %q", c.Detail)
	}
}

func TestGuardrailRejectsBadDate(t *testing.T) {
	for _, s := range []string{"2025/03/11", "20250311", "三月十一日", ""} {
		p := mustParse(t, strings.Replace(goodJSON, "2025-03-11", s, 1))
		r, _ := p.Validate(testCtx(t), Expect{})
		if r.Passed() {
			t.Errorf("非法日期 %q 必须被拦下", s)
		}
	}
}

// ---------------------------------------------------------------------------
// 护栏：辅助核算
// ---------------------------------------------------------------------------

func TestGuardrailRequiresAux(t *testing.T) {
	// 1122 要求客户，模型没给
	bad := strings.Replace(goodJSON, `"account_code": "1002"`, `"account_code": "1122"`, 1)
	p := mustParse(t, bad)
	r, _ := p.Validate(testCtx(t), Expect{})
	if r.Passed() {
		t.Fatal("缺少必需的辅助核算必须被拦下")
	}
	c := checkByKey(t, r, "aux")
	if !strings.Contains(c.Detail, "客户") {
		t.Errorf("应点明缺少的维度，实际 %q", c.Detail)
	}
}

func TestGuardrailAcceptsAuxWhenGiven(t *testing.T) {
	bad := strings.Replace(goodJSON, `"account_code": "1002"`,
		`"account_code": "1122", "contact_id": 1`, 1)
	p := mustParse(t, bad)
	r, _ := p.Validate(testCtx(t), Expect{})
	if !r.Passed() {
		t.Fatalf("给了正确的辅助核算应通过，实际 %s", r.Summary())
	}
}

// ★ 往来单位类型必须与科目声明一致：客户科目不能挂供应商
func TestGuardrailRejectsWrongContactKind(t *testing.T) {
	bad := strings.Replace(goodJSON, `"account_code": "1002"`,
		`"account_code": "1122", "contact_id": 2`, 1) // 2 是供应商
	p := mustParse(t, bad)
	r, _ := p.Validate(testCtx(t), Expect{})
	if r.Passed() {
		t.Fatal("往来单位类型不匹配必须被拦下")
	}
	c := checkByKey(t, r, "contact")
	if !strings.Contains(c.Detail, "customer") || !strings.Contains(c.Detail, "supplier") {
		t.Errorf("应点明类型冲突，实际 %q", c.Detail)
	}
}

func TestGuardrailRejectsUnknownContact(t *testing.T) {
	bad := strings.Replace(goodJSON, `"account_code": "1002"`,
		`"account_code": "1122", "contact_id": 999`, 1)
	p := mustParse(t, bad)
	r, _ := p.Validate(testCtx(t), Expect{})
	if r.Passed() {
		t.Fatal("不存在的往来单位必须被拦下")
	}
	c := checkByKey(t, r, "contact")
	if !strings.Contains(c.Detail, "999") {
		t.Errorf("应点明 id，实际 %q", c.Detail)
	}
}

// 科目没声明的维度被填了值 → 提醒而不是阻断（多填不影响账务正确性）
func TestGuardrailWarnsExtraAux(t *testing.T) {
	bad := strings.Replace(goodJSON, `"account_code": "1002", "debit": 5000000, "credit": 0`,
		`"account_code": "1002", "debit": 5000000, "credit": 0, "dept_id": 7`, 1)
	p := mustParse(t, bad)
	r, _ := p.Validate(testCtx(t), Expect{})
	if !r.Passed() {
		t.Fatalf("多填维度只是提醒，不应阻断：%s", r.Summary())
	}
	if len(r.Warnings()) == 0 {
		t.Fatal("应产生一条提醒")
	}
}

// ---------------------------------------------------------------------------
// 护栏：置信度
// ---------------------------------------------------------------------------

func TestGuardrailConfidence(t *testing.T) {
	// 越界 → 阻断
	for _, v := range []string{"-0.1", "1.5"} {
		p := mustParse(t, strings.Replace(goodJSON, `"confidence": 0.92`, `"confidence": `+v, 1))
		r, _ := p.Validate(testCtx(t), Expect{})
		if r.Passed() {
			t.Errorf("置信度 %s 越界必须被拦下", v)
		}
	}
	// 偏低 → 提醒但不阻断
	p := mustParse(t, strings.Replace(goodJSON, `"confidence": 0.92`, `"confidence": 0.4`, 1))
	r, _ := p.Validate(testCtx(t), Expect{})
	if !r.Passed() {
		t.Fatalf("低置信度不应阻断，实际 %s", r.Summary())
	}
	c := checkByKey(t, r, "confidence")
	if c.Level != CheckWarn {
		t.Errorf("应为 warn，实际 %s", c.Level)
	}
}

// ---------------------------------------------------------------------------
// 成功路径
// ---------------------------------------------------------------------------

func TestValidatePassesGoodProposal(t *testing.T) {
	p := mustParse(t, goodJSON)
	r, err := p.Validate(testCtx(t), Expect{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Passed() {
		t.Fatalf("应当通过，实际 %s", r.Summary())
	}
	if len(r.Warnings()) != 0 {
		t.Errorf("不应有提醒，实际 %v", r.Warnings())
	}
	if r.Checksum == "" || len(r.Checksum) != 16 {
		t.Errorf("指纹应是非空 16 位十六进制，实际 %q", r.Checksum)
	}
	if r.Summary() == "" {
		t.Error("应有一句话结论")
	}
}

// ---------------------------------------------------------------------------
// ★ 金额必须与用户填的那个数一致
// ---------------------------------------------------------------------------
//
// 这一条是**实测**出来的：用户填 325 元记「请客户吃饭」，模型写成了 326 元。
// 借贷照样平衡、科目存在、辅助核算也齐，二十多条护栏一条都没拦住 ——
// 界面上显示的甚至是绿色的「借贷平衡」。
//
// 上面那些检查全是「提议自己跟自己自洽」；一份金额写错的凭证完全可以自洽。
// 所以要拿用户当初填的数对一次，那是这一笔业务里唯一确定的事实。

func TestValidateAmountMustMatchInput(t *testing.T) {
	// goodJSON 的两条分录各 50000.00 元
	p := mustParse(t, goodJSON)

	// 用户填的就是 50000 元 → 过
	r, err := p.Validate(testCtx(t), Expect{Amount: money.Money(5000000)})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Passed() {
		t.Fatalf("金额一致时不该拦，实际 %s", r.Summary())
	}
	if c := checkByKey(t, r, "amount_match"); c.Level != CheckOK {
		t.Errorf("金额一致时应留下一条 OK 记录，便于事后核对，实际 %v", c.Level)
	}

	// 用户填 325 元、模型写 326 元 → **必须拦**
	r2, err := p.Validate(testCtx(t), Expect{Amount: money.Money(32500)})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Passed() {
		t.Fatal("金额对不上却放行了 —— 会计一眼扫过去不会停，这种错会直接进账")
	}
	c := checkByKey(t, r2, "amount_match")
	if c.Level != CheckFail {
		t.Errorf("金额不符应当是阻断项（CheckFail），实际 %v", c.Level)
	}
	// 提示里要同时给出两个数，否则用户不知道该信哪个
	if !strings.Contains(c.Detail, "325.00") || !strings.Contains(c.Detail, "50,000.00") {
		t.Errorf("提示要把「你填的」和「模型写的」都摆出来，实际 %q", c.Detail)
	}
}

// 方向不影响核对：填 -325（支出）而凭证合计 325，是同一笔钱。
func TestValidateAmountComparesAbsoluteValue(t *testing.T) {
	p := mustParse(t, goodJSON)
	r, err := p.Validate(testCtx(t), Expect{Amount: money.Money(-5000000)})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Passed() {
		t.Errorf("收支方向不该被当成金额不符，实际 %s", r.Summary())
	}
}

// 没填金额时不做核对 —— 自由文本记账本来就可能没有明确金额。
func TestValidateSkipsAmountCheckWhenInputIsZero(t *testing.T) {
	p := mustParse(t, goodJSON)
	r, err := p.Validate(testCtx(t), Expect{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Passed() {
		t.Fatalf("没填金额却因为金额被拦了，实际 %s", r.Summary())
	}
	for _, c := range r.Checks {
		if c.Key == "amount_match" {
			t.Error("没填金额时不该留下金额核对记录 —— 会让人以为核对过了")
		}
	}
}

func TestValidateRequiresContext(t *testing.T) {
	p := mustParse(t, goodJSON)
	if _, err := p.Validate(nil, Expect{}); err == nil {
		t.Fatal("缺少上下文应报错")
	}
}

// ---------------------------------------------------------------------------
// 指纹
// ---------------------------------------------------------------------------

// ★ 只覆盖影响账务的字段：reasoning 变了但账没变，指纹应当不变
func TestChecksumIgnoresNonAccountingFields(t *testing.T) {
	a := mustParse(t, goodJSON)
	b := mustParse(t, strings.Replace(goodJSON,
		`"reasoning": "摘要含『借款』且历史同类分录均记入本年利润"`,
		`"reasoning": "另一种说法"`, 1))
	b.Confidence = 0.55
	if a.Checksum() != b.Checksum() {
		t.Error("reasoning / confidence 变化不应改变指纹")
	}

	// 金额变化必须改变指纹
	c := mustParse(t, strings.Replace(goodJSON, `"debit": 5000000`, `"debit": 5000001`, 1))
	if a.Checksum() == c.Checksum() {
		t.Error("金额变化必须改变指纹")
	}
	// 辅助核算变化必须改变指纹
	d := mustParse(t, strings.Replace(goodJSON,
		`"account_code": "1002", "debit"`, `"account_code": "1002", "contact_id": 1, "debit"`, 1))
	if a.Checksum() == d.Checksum() {
		t.Error("辅助核算变化必须改变指纹")
	}
}

// ---------------------------------------------------------------------------
// 落地为草稿
// ---------------------------------------------------------------------------

func TestMaterializeCreatesDraft(t *testing.T) {
	p := mustParse(t, goodJSON)
	v, err := p.Materialize("李会计", &AIProvenance{
		Provider: "本地模型", Model: "qwen2.5-7b", Layer: LayerAI,
		PromptDigest: Digest("收到借款 50000"), Confidence: p.Confidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != voucher.StatusDraft {
		t.Errorf("AI 产物一律先是草稿，实际 %s", v.Status)
	}
	if v.Source != voucher.SourceAI {
		t.Errorf("来源应为 ai，实际 %s", v.Source)
	}
	if !v.CreatedByAI {
		t.Error("应标记为 AI 提议")
	}
	if v.CreatedBy != "李会计" {
		t.Errorf("制单人应为确认的人，实际 %s", v.CreatedBy)
	}
	if len(v.Entries) != 2 {
		t.Fatalf("分录数 = %d", len(v.Entries))
	}
	if v.Entries[0].Debit != money.Money(5000000) {
		t.Errorf("借方 = %s", v.Entries[0].Debit)
	}
	if v.TotalDebit() != v.TotalCredit() {
		t.Error("生成的草稿应借贷平衡")
	}
}

func TestMaterializeRequiresMaker(t *testing.T) {
	p := mustParse(t, goodJSON)
	if _, err := p.Materialize("", nil); err == nil {
		t.Fatal("缺少制单人应报错")
	}
}

// 生成出的草稿必须能通过完整的领域校验（护栏与过账校验同源）
func TestMaterializePassesPostingValidation(t *testing.T) {
	ctx := testCtx(t)
	p := mustParse(t, strings.Replace(goodJSON, `"account_code": "1002"`,
		`"account_code": "1122", "contact_id": 1`, 1))
	r, _ := p.Validate(ctx, Expect{})
	if !r.Passed() {
		t.Fatalf("护栏应先通过：%s", r.Summary())
	}
	v, err := p.Materialize("李会计", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate(ctx); err != nil {
		t.Fatalf("生成的草稿应能通过过账校验: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 排序
// ---------------------------------------------------------------------------

func TestRankPrefersCheapLayers(t *testing.T) {
	got := Rank([]Scored{
		{Layer: LayerAI, Confidence: 0.99, Source: "模型"},
		{Layer: LayerHistory, Confidence: 0.60, Source: "历史"},
		{Layer: LayerRule, Confidence: 0.50, Source: "规则"},
		{Layer: LayerHistory, Confidence: 0.80, Source: "历史2"},
	})
	want := []string{"规则", "历史2", "历史", "模型"}
	for i, s := range got {
		if s.Source != want[i] {
			t.Errorf("第 %d 位 = %s，期望 %s（完整顺序 %v）", i, s.Source, want[i], sourcesOf(got))
		}
	}
}

func sourcesOf(ss []Scored) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Source)
	}
	return out
}

func TestLayerLabels(t *testing.T) {
	for l, want := range map[Layer]string{
		LayerRule: "规则", LayerHistory: "历史", LayerAI: "AI",
	} {
		if got := l.Label(); got != want {
			t.Errorf("%s.Label() = %s，期望 %s", l, got, want)
		}
	}
}

func TestDigestIsStable(t *testing.T) {
	a := Digest("收到杭州某某科技有限公司货款 90400")
	if a != Digest("收到杭州某某科技有限公司货款 90400") {
		t.Error("同样输入应得到同样指纹")
	}
	if len(a) != 64 {
		t.Errorf("完整 sha256 应为 64 位十六进制，实际 %d", len(a))
	}
	if a == Digest("换个输入") {
		t.Error("不同输入不应碰撞")
	}
}
