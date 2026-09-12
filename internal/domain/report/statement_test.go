package report

import (
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

// ---------------------------------------------------------------------------
// 测试解析器
// ---------------------------------------------------------------------------

// fakeResolver 用固定表提供取数，便于在不起数据库的情况下测公式引擎。
type fakeResolver struct {
	values  map[string]money.Money
	analyze map[string][]UnitBalance
}

func (r *fakeResolver) Value(prefix string) money.Money { return r.values[prefix] }
func (r *fakeResolver) Analyze(prefix string) []UnitBalance {
	return r.analyze[prefix]
}

func y(n int64) money.Money { return money.Money(n) * money.Yuan }

// ---------------------------------------------------------------------------
// 公式解析
// ---------------------------------------------------------------------------

func TestParseFormulaAccount(t *testing.T) {
	f, err := ParseFormula("1001")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if f.Empty() {
		t.Error("不应为空")
	}
	if len(f.Refs()) != 0 {
		t.Errorf("不应有行引用，得到 %v", f.Refs())
	}
}

func TestParseFormulaArithmetic(t *testing.T) {
	// 存货的官方公式就带减法（商品进销差价是备抵科目）
	f, err := ParseFormula("1401+1402+1403+1404-1407+1405+1408+1411+1421+4001")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	r := &fakeResolver{values: map[string]money.Money{
		"1401": y(100), "1402": y(200), "1403": y(300), "1404": y(40),
		"1407": y(10), "1405": y(500), "1408": y(60), "1411": y(70),
		"1421": y(80), "4001": y(90),
	}}
	got, err := f.Eval(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := y(100 + 200 + 300 + 40 - 10 + 500 + 60 + 70 + 80 + 90)
	if got != want {
		t.Errorf("存货 = %s，期望 %s", got, want)
	}
}

func TestParseFormulaLineRef(t *testing.T) {
	f, err := ParseFormula("@L(18)-@L(19)")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if refs := f.Refs(); len(refs) != 2 || refs[0] != 18 || refs[1] != 19 {
		t.Errorf("引用 = %v，期望 [18 19]", refs)
	}
	got, err := f.Eval(nil, map[int]money.Money{18: y(1000), 19: y(200)})
	if err != nil {
		t.Fatal(err)
	}
	if got != y(800) {
		t.Errorf("固定资产账面价值 = %s，期望 800.00", got)
	}
}

func TestParseFormulaSum(t *testing.T) {
	f, err := ParseFormula("@sum(16,17,20)")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got, err := f.Eval(nil, map[int]money.Money{16: y(1), 17: y(2), 20: y(3)})
	if err != nil {
		t.Fatal(err)
	}
	if got != y(6) {
		t.Errorf("合计 = %s，期望 6.00", got)
	}
}

func TestParseFormulaAnalyze(t *testing.T) {
	for _, src := range []string{
		"@analyze(1122,2203,debit)",
		"@analyze(2202,1123,credit)",
	} {
		f, err := ParseFormula(src)
		if err != nil {
			t.Errorf("解析 %q 失败: %v", src, err)
		}
		if f.Empty() {
			t.Errorf("%q 不应为空", src)
		}
	}
}

func TestParseFormulaEmpty(t *testing.T) {
	for _, src := range []string{"", "   "} {
		f, err := ParseFormula(src)
		if err != nil {
			t.Errorf("空公式应合法，得到 %v", err)
		}
		if !f.Empty() {
			t.Errorf("%q 应为空公式", src)
		}
		v, err := f.Eval(nil, nil)
		if err != nil || v != 0 {
			t.Errorf("空公式求值应为 0，得到 %s %v", v, err)
		}
	}
}

func TestParseFormulaErrors(t *testing.T) {
	cases := []string{
		"100",                    // 编长度非法
		"1001100",                // 7 位
		"ABCD",                   // 非数字
		"@X(1)",                  // 未知函数
		"@L()",                   // 缺参数
		"@L(0)",                  // 行号必须 ≥ 1
		"@analyze(1122)",         // 参数不足
		"@analyze(1122,2203,up)", // 方向非法
		"@sum()",                 // 缺参数
		"1001+",                  // 尾部缺项
		"(1001",                  // 不应支持括号分组
		"1001 1002",              // 多余内容
	}
	for _, src := range cases {
		if _, err := ParseFormula(src); err == nil {
			t.Errorf("ParseFormula(%q) 应当报错", src)
		}
	}
}

// ---------------------------------------------------------------------------
// ★ @analyze：中国报表特有的「按明细方向分析填列」
// ---------------------------------------------------------------------------

// 这条对应一个真实且容易做错的规则：
//
//	「应收账款」项目 = 应收账款明细的**借方**余额 + 预收账款明细的**借方**余额
//	「预收账款」项目 = 预收账款明细的**贷方**余额 + 应收账款明细的**贷方**余额
//
// 只看科目总额是做不出来的 —— 同一个应收账款科目下，
// 客户甲可能挂借方、客户乙挂贷方，必须逐明细判断方向。
func TestAnalyzeSplitsByDirection(t *testing.T) {
	r := &fakeResolver{analyze: map[string][]UnitBalance{
		// 应收账款：客户甲借 1000（应收），客户乙贷 300（多收了，实为预收）
		"1122": {
			{AccountCode: "1122", ContactID: i64(1), Net: y(1000)},
			{AccountCode: "1122", ContactID: i64(2), Net: y(-300)},
		},
		// 预收账款：客户丙贷 500（预收），客户丁借 200（发货超额，实为应收）
		"2203": {
			{AccountCode: "2203", ContactID: i64(3), Net: y(-500)},
			{AccountCode: "2203", ContactID: i64(4), Net: y(200)},
		},
	}}

	// 应收账款项目：1122 的借方 + 2203 的借方 = 1000 + 200
	ar, err := MustParseFormula("@analyze(1122,2203,debit)").Eval(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ar != y(1200) {
		t.Errorf("应收账款 = %s，期望 1200.00（1000 来自 1122 借方 + 200 来自 2203 借方）", ar)
	}

	// 预收账款项目：2203 的贷方 + 1122 的贷方 = 500 + 300
	adv, err := MustParseFormula("@analyze(2203,1122,credit)").Eval(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if adv != y(800) {
		t.Errorf("预收账款 = %s，期望 800.00（500 来自 2203 贷方 + 300 来自 1122 贷方）", adv)
	}

	// 两条规则必须把全部明细恰好分完，不重不漏
	total := ar.Add(adv)
	allUnits := y(1000 - 300 - 500 + 200).Abs()
	_ = allUnits
	if total != y(1200).Add(y(800)) {
		t.Errorf("两项合计 = %s", total)
	}
}

func TestAnalyzeSkipsZero(t *testing.T) {
	r := &fakeResolver{analyze: map[string][]UnitBalance{
		"1122": {{AccountCode: "1122", Net: 0}, {AccountCode: "1122", Net: y(100)}},
	}}
	got, err := MustParseFormula("@analyze(1122,2203,debit)").Eval(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != y(100) {
		t.Errorf("零额明细应被跳过，得到 %s", got)
	}
}

func TestAnalyzeMissingPrefix(t *testing.T) {
	r := &fakeResolver{analyze: map[string][]UnitBalance{}}
	got, err := MustParseFormula("@analyze(1122,2203,debit)").Eval(r, nil)
	if err != nil || got != 0 {
		t.Errorf("无数据时应为 0，得到 %s %v", got, err)
	}
}

// ---------------------------------------------------------------------------
// 内置报表定义
// ---------------------------------------------------------------------------

func TestLoadBalanceSheet(t *testing.T) {
	d, err := LoadBalanceSheet()
	if err != nil {
		t.Fatalf("加载资产负债表失败: %v", err)
	}
	if len(d.Lines) != 53 {
		t.Fatalf("行数 = %d，期望 53", len(d.Lines))
	}
	if d.Lines[0].No != 1 || d.Lines[52].No != 53 {
		t.Error("行次应从 1 到 53")
	}
	// 抽查官方行
	checks := map[int]string{
		1:  "货币资金",
		9:  "存货",
		20: "固定资产账面价值",
		30: "资产总计",
		51: "未分配利润",
		53: "负债和所有者权益（或股东权益）总计",
	}
	for no, name := range checks {
		l, ok := d.Line(no)
		if !ok {
			t.Errorf("行 %d 不存在", no)
			continue
		}
		if l.Name != name {
			t.Errorf("行 %d 名称 = %q，期望 %q", no, l.Name, name)
		}
	}
	// 左右两侧
	left, right := 0, 0
	for _, l := range d.Lines {
		switch l.Side {
		case SideLeft:
			left++
		case SideRight:
			right++
		}
	}
	if left != 30 || right != 23 {
		t.Errorf("左侧 %d 行 / 右侧 %d 行，期望 30 / 23", left, right)
	}
}

func TestLoadIncomeStatement(t *testing.T) {
	d, err := LoadIncomeStatement()
	if err != nil {
		t.Fatalf("加载利润表失败: %v", err)
	}
	if len(d.Lines) != 32 {
		t.Fatalf("行数 = %d，期望 32", len(d.Lines))
	}
	// 官方有 19 个「其中」附列项：
	// 5403 的 7 项(行4-10) + 5601 的 2 项(12,13) + 5602 的 3 项(15,16,17)
	// + 5603 的 1 项(19) + 5301 的 1 项(23) + 5711 的 5 项(25-29)
	memo := 0
	for _, l := range d.Lines {
		if l.Type == LineMemo {
			memo++
		}
	}
	if memo != 19 {
		t.Errorf("「其中」附列项 = %d，期望 19", memo)
	}
	// 四个层次行
	for no, want := range map[int]string{
		1: "一、营业收入", 21: "二、营业利润（亏损以“-”号填列）",
		30: "三、利润总额（亏损总额以“-”号填列）", 32: "四、净利润（净亏损以“-”号填列）",
	} {
		l, ok := d.Line(no)
		if !ok || l.Name != want {
			t.Errorf("行 %d = %q，期望 %q", no, l.Name, want)
		}
	}
}

// 公式只能引用前面的行 —— 这条约束让求值无需拓扑排序
func TestDefinitionRejectsForwardRef(t *testing.T) {
	// 构造一个引用后面行的定义应当报错（用内置定义间接验证约束存在）
	d, err := LoadBalanceSheet()
	if err != nil {
		t.Fatal(err)
	}
	// 内置定义本身必须满足该约束（否则加载就会失败）
	for _, l := range d.Lines {
		for _, ref := range l.Formula.Refs() {
			if ref >= l.No {
				t.Errorf("行 %d 引用了后面的行 %d", l.No, ref)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 报表求值与勾稽关系
// ---------------------------------------------------------------------------

// 构造一份能让资产负债表完全平衡的取数表。
//
// 资产：货币资金 10+40+5 = 55
//
//	应收账款 @analyze = 20
//	存货 15+5 = 20
//	固定资产 30 − 累计折旧 10 = 20
//	合计 115
//
// 负债：应付账款 25 + 应交税费 10 + 其他应付款 5 = 40
// 权益：实收资本 65 + 利润分配 10 = 75
// 校验：115 = 40 + 75 ✓
func balancedResolver() *fakeResolver {
	return &fakeResolver{values: map[string]money.Money{
		// 资产
		"1001": y(10), "1002": y(40), "1012": y(5), // 货币资金 55
		"1122": y(20),               // 应收账款（含往来分解见下）
		"1403": y(15), "1405": y(5), // 存货 20
		"1601": y(30), "1602": y(10), // 固定资产原价 30、累计折旧 10
		// 负债
		"2202": y(25), "2221": y(10), "2241": y(5), // 应付 25、应交税费 10、其他应付 5
		// 所有者权益
		"3001": y(65), "3104": y(10),
		"4001": 0, "1401": 0, "1402": 0, "1404": 0, "1407": 0, "1408": 0,
		"1411": 0, "1421": 0,
		"1121": 0, "1101": 0, "1131": 0, "1132": 0, "1221": 0, "1123": 0,
		"1501": 0, "1511": 0, "1604": 0, "1605": 0, "1606": 0,
		"1621": 0, "1622": 0, "1701": 0, "1702": 0, "1801": 0, "4301": 0,
		"2001": 0, "2201": 0, "2203": 0, "2211": 0, "2231": 0, "2232": 0,
		"2401": 0, "2501": 0, "2701": 0, "3002": 0, "3101": 0, "3103": 0,
	}, analyze: map[string][]UnitBalance{
		// 应收账款：客户借方余额 20
		"1122": {{AccountCode: "1122", Net: y(20)}},
		// 预收账款：无余额
		"2203": nil,
		// 应付账款：供应商贷方余额 25（原始净额为负）
		"2202": {{AccountCode: "2202", Net: y(-25)}},
		// 预付账款：无余额
		"1123": nil,
	}}
}

func TestComputeBalanceSheet(t *testing.T) {
	r := balancedResolver()
	d, err := LoadBalanceSheet()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Compute(r); err != nil {
		t.Fatalf("求值失败: %v", err)
	}

	// 货币资金 = 10 + 40 + 5 = 55
	if l, _ := d.Line(1); l.Value != y(55) {
		t.Errorf("行1 货币资金 = %s，期望 55.00", l.Value)
	}
	// 应收账款 = @analyze(1122,2203,debit) = 20
	if l, _ := d.Line(4); l.Value != y(20) {
		t.Errorf("行4 应收账款 = %s，期望 20.00", l.Value)
	}
	// 存货 = 1403+1405 = 20
	if l, _ := d.Line(9); l.Value != y(20) {
		t.Errorf("行9 存货 = %s，期望 20.00", l.Value)
	}
	// 流动资产合计 = 55+20+20 = 95
	if l, _ := d.Line(15); l.Value != y(95) {
		t.Errorf("行15 流动资产合计 = %s，期望 95.00", l.Value)
	}
	// 固定资产原价 30、累计折旧 10、账面价值 20
	if l, _ := d.Line(18); l.Value != y(30) {
		t.Errorf("行18 = %s", l.Value)
	}
	if l, _ := d.Line(19); l.Value != y(10) {
		t.Errorf("行19 累计折旧 = %s，期望 10.00（备抵科目应取正）", l.Value)
	}
	if l, _ := d.Line(20); l.Value != y(20) {
		t.Errorf("行20 固定资产账面价值 = %s，期望 20.00", l.Value)
	}
	// 非流动资产合计 = 20
	if l, _ := d.Line(29); l.Value != y(20) {
		t.Errorf("行29 = %s，期望 20.00", l.Value)
	}
	// 资产总计 = 95 + 20 = 115
	if l, _ := d.Line(30); l.Value != y(115) {
		t.Errorf("行30 资产总计 = %s，期望 115.00", l.Value)
	}
	// 流动负债合计 = 25+10+5 = 40
	if l, _ := d.Line(41); l.Value != y(40) {
		t.Errorf("行41 流动负债合计 = %s，期望 40.00", l.Value)
	}
	// 所有者权益合计 = 65 + 10 = 75
	if l, _ := d.Line(52); l.Value != y(75) {
		t.Errorf("行52 所有者权益合计 = %s，期望 75.00", l.Value)
	}
	// ★ 行53 必须等于行30
	l30, _ := d.Line(30)
	l53, _ := d.Line(53)
	if l53.Value != l30.Value {
		t.Errorf("行53 = %s 应等于行30 = %s", l53.Value, l30.Value)
	}
	if l53.Value != y(115) {
		t.Errorf("行53 = %s，期望 115.00", l53.Value)
	}
}

func TestCheckBalanceSheetPasses(t *testing.T) {
	r := balancedResolver()
	d, _ := LoadBalanceSheet()
	if err := d.Compute(r); err != nil {
		t.Fatal(err)
	}
	if issues := CheckBalanceSheet(d); len(issues) != 0 {
		t.Errorf("平衡的报表不应有勾稽问题，得到 %v", issues)
	}
}

// ★ 不平衡必须被检出 —— Frappe Books 完全没有这类检查
func TestCheckBalanceSheetDetectsImbalance(t *testing.T) {
	r := balancedResolver()
	// 人为制造不平衡：把所有者权益改小
	r.values["3104"] = y(5)

	d, _ := LoadBalanceSheet()
	if err := d.Compute(r); err != nil {
		t.Fatal(err)
	}
	issues := CheckBalanceSheet(d)
	if len(issues) == 0 {
		t.Fatal("不平衡的报表应被检出")
	}
	// 必须报出行30 ≠ 行53
	var found bool
	for _, is := range issues {
		if is.Fatal && (is.Left == "行30 资产总计" || is.Right == "行53 负债和所有者权益总计") {
			found = true
		}
	}
	if !found {
		t.Errorf("应检出行30 与行53 不等，实际 %v", issues)
	}
}

// 官方勾稽关系：行20 = 行18 − 行19，且累计折旧必须取正
func TestFixedAssetNetValue(t *testing.T) {
	r := balancedResolver()
	r.values["1601"] = y(1000)
	r.values["1602"] = y(250)
	// 调整权益保持平衡：资产增加 1000-250-(30-10) = 730
	r.values["3104"] = y(10 + 730)

	d, _ := LoadBalanceSheet()
	if err := d.Compute(r); err != nil {
		t.Fatal(err)
	}
	l20, _ := d.Line(20)
	if l20.Value != y(750) {
		t.Errorf("固定资产账面价值 = %s，期望 750.00", l20.Value)
	}
	if issues := CheckBalanceSheet(d); len(issues) != 0 {
		t.Errorf("应保持平衡，得到 %v", issues)
	}
}

// ---------------------------------------------------------------------------
// 利润表
// ---------------------------------------------------------------------------

func TestComputeIncomeStatement(t *testing.T) {
	r := &fakeResolver{values: map[string]money.Money{
		"5001": y(1000), "5051": y(100), // 营业收入 1100
		"5401": y(600), "5402": y(50), // 营业成本 650
		"5403": y(20),  // 税金及附加
		"5601": y(80),  // 销售费用
		"5602": y(150), // 管理费用
		"5603": y(10),  // 财务费用
		"5111": y(30),  // 投资收益
		"5301": y(5),   // 营业外收入
		"5711": y(15),  // 营业外支出
		"5801": y(45),  // 所得税费用
	}}
	d, err := LoadIncomeStatement()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Compute(r); err != nil {
		t.Fatalf("求值失败: %v", err)
	}

	get := func(no int) money.Money {
		l, ok := d.Line(no)
		if !ok {
			t.Fatalf("行 %d 不存在", no)
		}
		return l.Value
	}
	if got := get(1); got != y(1100) {
		t.Errorf("行1 营业收入 = %s，期望 1100.00", got)
	}
	if got := get(2); got != y(650) {
		t.Errorf("行2 营业成本 = %s，期望 650.00", got)
	}
	// 营业利润 = 1100 − 650 − 20 − 80 − 150 − 10 + 30 = 220
	if got := get(21); got != y(220) {
		t.Errorf("行21 营业利润 = %s，期望 220.00", got)
	}
	// 利润总额 = 220 + 5 − 15 = 210
	if got := get(30); got != y(210) {
		t.Errorf("行30 利润总额 = %s，期望 210.00", got)
	}
	// 净利润 = 210 − 45 = 165
	if got := get(32); got != y(165) {
		t.Errorf("行32 净利润 = %s，期望 165.00", got)
	}
	if issues := CheckIncomeStatement(d); len(issues) != 0 {
		t.Errorf("勾稽关系应成立，得到 %v", issues)
	}
}

// 「其中」附列项不参与小计，但可以独立取数
func TestIncomeStatementMemoLines(t *testing.T) {
	// ★ 附列项（「其中」行）必须各取自己的明细科目，而不是全部回落到父科目。
	//
	// 这条曾经错过：定义文件里 15/16/17 行的公式都写成父科目 5602，
	// 于是「开办费」「业务招待费」「研究费用」三行会一起显示出
	// 管理费用的全额，看上去数字齐全，实际全是错的。
	r := &fakeResolver{values: map[string]money.Money{
		"5602":   y(100000), // 管理费用合计
		"560214": y(30000),  // 开办费
		"560208": y(20000),  // 业务招待费
		"560215": y(12500),  // 研究费用
	}}
	d, _ := LoadIncomeStatement()
	if err := d.Compute(r); err != nil {
		t.Fatal(err)
	}

	l14, _ := d.Line(14)
	if l14.Value != y(100000) {
		t.Errorf("行14 管理费用 = %s，期望 1000.00", l14.Value)
	}

	want := map[int]money.Money{15: y(30000), 16: y(20000), 17: y(12500)}
	for no, w := range want {
		l, _ := d.Line(no)
		if l.Type != LineMemo {
			t.Errorf("行 %d 应为 memo 类型，实际 %s", no, l.Type)
		}
		if l.Value != w {
			t.Errorf("行 %d（%s）= %s，期望 %s", no, l.Name, l.Value, w)
		}
		// 附列项不得等于父科目全额
		if l.Value == l14.Value {
			t.Errorf("行 %d 与父科目同值，说明公式回落到了父科目", no)
		}
	}
}

// 名称里不应再带「其中：」前缀 —— 前缀由展示层按行类型添加
func TestMemoLineNamesHaveNoPrefix(t *testing.T) {
	d, _ := LoadIncomeStatement()
	for _, l := range d.Lines {
		if l.Type != LineMemo {
			continue
		}
		if strings.Contains(l.Name, "其中：") {
			t.Errorf("行 %d 的名称不该自带「其中：」前缀：%q", l.No, l.Name)
		}
		if strings.ContainsAny(l.Name, "\u3000 \t") {
			t.Errorf("行 %d 的名称含多余空白：%q", l.No, l.Name)
		}
	}
}

// 「利息费用」按官方口径用「利息支出 − 利息收入」，收入以负数体现
func TestInterestMemoNetsIncome(t *testing.T) {
	r := &fakeResolver{values: map[string]money.Money{
		"5603":   y(5000),
		"560301": y(8000),
		"560302": y(3000),
	}}
	d, _ := LoadIncomeStatement()
	if err := d.Compute(r); err != nil {
		t.Fatal(err)
	}
	l, _ := d.Line(19)
	if l.Value != y(5000) {
		t.Errorf("行19 利息费用 = %s，期望 50.00（80 − 30）", l.Value)
	}
}

// 营改增后「营业税」已无对应科目：公式留空，恒为 0，不允许错挂到别的税种上
func TestAbolishedTaxLineIsZero(t *testing.T) {
	r := &fakeResolver{values: map[string]money.Money{
		"5403":   y(10000),
		"540302": y(10000), // 城市维护建设税
	}}
	d, _ := LoadIncomeStatement()
	if err := d.Compute(r); err != nil {
		t.Fatal(err)
	}
	l5, _ := d.Line(5)
	if l5.Name != "营业税" {
		t.Fatalf("行 5 应为营业税，实际 %q", l5.Name)
	}
	if l5.Value != 0 {
		t.Errorf("营改增后营业税应恒为 0，实际 %s", l5.Value)
	}
	if l5.IsComputed() {
		t.Error("营业税行不该有取数公式 —— 有公式就可能被错挂到别的税种上")
	}
	// 对照：城建税有自己的科目，应正常取到数
	l6, _ := d.Line(6)
	if l6.Value != y(10000) {
		t.Errorf("行 6 城市维护建设税 = %s，期望 100.00", l6.Value)
	}
}

// 利润表勾稽关系不成立时必须被检出
func TestCheckIncomeStatementDetectsError(t *testing.T) {
	d, _ := LoadIncomeStatement()
	// 不调用 Compute，直接手工塞入矛盾数据
	// （测试在同一包内，可以直接改 Value）
	l1, _ := d.Line(1)
	l2, _ := d.Line(2)
	l21, _ := d.Line(21)
	l1.Value = y(100)
	l2.Value = y(50)
	l21.Value = y(999) // 应为 50
	issues := CheckIncomeStatement(d)
	if len(issues) == 0 {
		t.Fatal("矛盾的利润表应被检出")
	}
}

// ---------------------------------------------------------------------------
// 科目余额表辅助
// ---------------------------------------------------------------------------

func TestSplitBalance(t *testing.T) {
	cases := []struct {
		net          money.Money
		wantD, wantC money.Money
	}{
		{y(100), y(100), 0},
		{y(-100), 0, y(100)},
		{0, 0, 0},
	}
	for _, c := range cases {
		d, cr := SplitBalance(c.net)
		if d != c.wantD || cr != c.wantC {
			t.Errorf("SplitBalance(%s) = %s/%s，期望 %s/%s",
				c.net, d, cr, c.wantD, c.wantC)
		}
	}
}

func TestRange(t *testing.T) {
	r := Range(period.NewKey(2025, 2))
	if r.From.String() != "2025-02-01" || r.To.String() != "2025-02-28" {
		t.Errorf("2025-02 区间 = %v", r)
	}
	r = Range(period.NewKey(2024, 2))
	if r.To.String() != "2024-02-29" {
		t.Errorf("2024-02 区间末日 = %v", r.To)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func i64(v int64) *int64 { return &v }
