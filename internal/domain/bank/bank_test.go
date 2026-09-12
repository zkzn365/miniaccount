package bank

import (
	"testing"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

func y(n int64) money.Money { return money.Money(n) * money.Yuan }
func i64(v int64) *int64    { return &v }

func flow(counterparty, summary string, dir Direction, amount money.Money) *Flow {
	return &Flow{
		AccountCode: "1002", TxnDate: calendar.MustParse("2025-09-11"),
		Direction: dir, Amount: amount,
		CounterpartyName: counterparty, Summary: summary,
	}
}

// ---------------------------------------------------------------------------
// 规则匹配
// ---------------------------------------------------------------------------

func TestRuleMatchCounterparty(t *testing.T) {
	r := &Rule{
		ID: 1, Enabled: true, Priority: 10,
		MatchField: MatchCounterparty, Pattern: "张三",
		CounterAccountCode: "224101",
	}
	e := NewRuleEngine([]*Rule{r})

	f := flow("张三", "转账", DirIn, y(50000))
	s := e.Match(f)
	if s == nil {
		t.Fatal("应命中规则")
	}
	if s.Layer != LayerRule {
		t.Errorf("层 = %s", s.Layer)
	}
	if s.CounterAccountCode != "224101" {
		t.Errorf("对方科目 = %q，期望 224101", s.CounterAccountCode)
	}
	if s.Confidence < 0.75 {
		t.Errorf("带文本条件的规则置信度应 ≥ 0.75，得到 %.2f", s.Confidence)
	}
	if s.RuleID == nil || *s.RuleID != 1 {
		t.Error("应记录命中的规则 id")
	}
}

func TestRuleNoMatch(t *testing.T) {
	r := &Rule{ID: 1, Enabled: true, MatchField: MatchCounterparty,
		Pattern: "张三", CounterAccountCode: "224101"}
	e := NewRuleEngine([]*Rule{r})
	if s := e.Match(flow("李四", "转账", DirIn, y(100))); s != nil {
		t.Errorf("不应命中，得到 %+v", s)
	}
}

func TestRulePriority(t *testing.T) {
	low := &Rule{ID: 1, Name: "低优先", Enabled: true, Priority: 100,
		MatchField: MatchCounterparty, Pattern: "杭州", CounterAccountCode: "5602"}
	high := &Rule{ID: 2, Name: "高优先", Enabled: true, Priority: 1,
		MatchField: MatchCounterparty, Pattern: "杭州", CounterAccountCode: "2202"}

	e := NewRuleEngine([]*Rule{low, high})
	s := e.Match(flow("杭州某某科技", "服务费", DirOut, y(1000)))
	if s == nil {
		t.Fatal("应命中")
	}
	if s.CounterAccountCode != "2202" {
		t.Errorf("应命中高优先级规则，得到 %q", s.CounterAccountCode)
	}
	// 无论传入顺序如何，结果一致
	e2 := NewRuleEngine([]*Rule{high, low})
	if got := e2.Match(flow("杭州某某科技", "服务费", DirOut, y(1000))); got.CounterAccountCode != "2202" {
		t.Errorf("优先级排序不稳定")
	}
}

func TestRuleDisabledSkipped(t *testing.T) {
	r := &Rule{ID: 1, Enabled: false, MatchField: MatchCounterparty,
		Pattern: "张三", CounterAccountCode: "224101"}
	e := NewRuleEngine([]*Rule{r})
	if s := e.Match(flow("张三", "转账", DirIn, y(100))); s != nil {
		t.Error("停用的规则不应命中")
	}
}

func TestRuleDirectionFilter(t *testing.T) {
	r := &Rule{ID: 1, Enabled: true, MatchField: MatchCounterparty,
		Pattern: "张三", Direction: DirIn, CounterAccountCode: "224101"}
	e := NewRuleEngine([]*Rule{r})
	if s := e.Match(flow("张三", "转账", DirIn, y(100))); s == nil {
		t.Error("同方向应命中")
	}
	if s := e.Match(flow("张三", "转账", DirOut, y(100))); s != nil {
		t.Error("异方向不应命中")
	}
}

func TestRuleAmountRange(t *testing.T) {
	min, max := y(1000), y(5000)
	// 「房租」出现在**摘要**里，对方户名是「房东」，所以匹配字段要选摘要
	r := &Rule{ID: 1, Enabled: true, MatchField: MatchSummary,
		Pattern: "房租", AmountMin: &min, AmountMax: &max,
		CounterAccountCode: "560210"}
	e := NewRuleEngine([]*Rule{r})

	if s := e.Match(flow("房东", "9月房租", DirOut, y(2000))); s == nil {
		t.Error("区间内应命中")
	}
	if s := e.Match(flow("房东", "9月房租", DirOut, y(500))); s != nil {
		t.Error("低于下限不应命中")
	}
	if s := e.Match(flow("房东", "9月房租", DirOut, y(9000))); s != nil {
		t.Error("高于上限不应命中")
	}
}

func TestRuleAccountFilter(t *testing.T) {
	r := &Rule{ID: 1, Enabled: true, AccountCode: "100201",
		MatchField: MatchCounterparty, Pattern: "张三", CounterAccountCode: "224101"}
	e := NewRuleEngine([]*Rule{r})

	f := flow("张三", "转账", DirIn, y(100))
	f.AccountCode = "100201"
	if s := e.Match(f); s == nil {
		t.Error("同账户应命中")
	}
	f2 := flow("张三", "转账", DirIn, y(100))
	f2.AccountCode = "100202"
	if s := e.Match(f2); s != nil {
		t.Error("不同账户不应命中")
	}
}

// 没有文本条件的规则（只按金额/方向）置信度要低一些，避免误伤
func TestRuleWithoutTextPatternHasLowerConfidence(t *testing.T) {
	r := &Rule{ID: 1, Enabled: true, MatchField: MatchCounterparty,
		Pattern: "", CounterAccountCode: "5602"}
	e := NewRuleEngine([]*Rule{r})
	s := e.Match(flow("任意", "任意", DirOut, y(100)))
	if s == nil {
		t.Fatal("无文本条件的规则仍应命中")
	}
	if s.Confidence >= 0.75 {
		t.Errorf("无文本条件的规则不应达到自动勾选阈值，得到 %.2f", s.Confidence)
	}
}

func TestRuleMatchBothField(t *testing.T) {
	r := &Rule{ID: 1, Enabled: true, MatchField: MatchBoth,
		Pattern: "股东", CounterAccountCode: "224101"}
	e := NewRuleEngine([]*Rule{r})
	// 户名不含「股东」，但摘要含
	if s := e.Match(flow("张三", "收到股东借款", DirIn, y(100))); s == nil {
		t.Error("both 模式应在摘要里命中")
	}
}

func TestRuleMemoTemplate(t *testing.T) {
	r := &Rule{ID: 1, Enabled: true, MatchField: MatchCounterparty, Pattern: "张三",
		CounterAccountCode: "224101",
		MemoTemplate:       "收到{counterparty}借款（{summary}）"}
	e := NewRuleEngine([]*Rule{r})
	s := e.Match(flow("张三", "转账", DirIn, y(100)))
	if s.Memo != "收到张三借款（转账）" {
		t.Errorf("摘要 = %q", s.Memo)
	}

	// 无模板时用对方户名
	r2 := &Rule{ID: 2, Enabled: true, MatchField: MatchCounterparty, Pattern: "张三",
		CounterAccountCode: "224101"}
	e2 := NewRuleEngine([]*Rule{r2})
	if got := e2.Match(flow("张三", "转账", DirIn, y(100))).Memo; got != "张三" {
		t.Errorf("默认摘要 = %q，期望「张三」", got)
	}
}

// ---------------------------------------------------------------------------
// 历史相似度
// ---------------------------------------------------------------------------

func TestHistoryExactCounterparty(t *testing.T) {
	h := NewHistoryMatcher([]HistoryEntry{
		{VoucherNo: "记-2025-03-0012", Direction: DirIn, Amount: y(30000),
			CounterpartyName: "张三", Summary: "借款",
			CounterAccountCode: "224101", ContactID: i64(42), Memo: "收到张三借款"},
	})
	s := h.Match(flow("张三", "借款", DirIn, y(50000)))
	if s == nil {
		t.Fatal("应命中历史")
	}
	if s.Layer != LayerHistory {
		t.Errorf("层 = %s", s.Layer)
	}
	if s.CounterAccountCode != "224101" {
		t.Errorf("对方科目 = %q", s.CounterAccountCode)
	}
	if s.ContactID == nil || *s.ContactID != 42 {
		t.Error("应带出历史往来的 contact_id")
	}
	if len(s.Evidence) == 0 {
		t.Error("应给出证据（历史凭证号）")
	}
	if s.Confidence < 0.75 {
		t.Errorf("户名完全相同应达到可勾选阈值，得到 %.2f", s.Confidence)
	}
}

func TestHistoryPartialNameMatch(t *testing.T) {
	h := NewHistoryMatcher([]HistoryEntry{
		{VoucherNo: "V1", Direction: DirOut, Amount: y(3000),
			CounterpartyName: "杭州某某办公用品有限公司", Summary: "办公用品",
			CounterAccountCode: "560206"},
	})
	// 名字被截断（银行常做脱敏）
	s := h.Match(flow("杭州某某办公用品", "采购", DirOut, y(3000)))
	if s == nil {
		t.Fatal("部分匹配应命中")
	}
	if s.Confidence >= 0.9 {
		t.Errorf("部分匹配的置信度应低于完全匹配，得到 %.2f", s.Confidence)
	}
}

func TestHistoryDirectionMustMatch(t *testing.T) {
	h := NewHistoryMatcher([]HistoryEntry{
		{VoucherNo: "V1", Direction: DirIn, CounterpartyName: "张三",
			CounterAccountCode: "224101"},
	})
	if s := h.Match(flow("张三", "转账", DirOut, y(100))); s != nil {
		t.Error("方向不同不应作为参考")
	}
}

func TestHistoryNoMatchForUnknownCounterparty(t *testing.T) {
	h := NewHistoryMatcher([]HistoryEntry{
		{VoucherNo: "V1", Direction: DirIn, CounterpartyName: "张三",
			CounterAccountCode: "224101"},
	})
	if s := h.Match(flow("完全陌生的公司", "未知业务", DirIn, y(100))); s != nil {
		t.Errorf("陌生对手方不应命中，得到 %+v", s)
	}
}

// ---------------------------------------------------------------------------
// 三层编排
// ---------------------------------------------------------------------------

type stubAI struct {
	called bool
	s      *Suggestion
}

func (a *stubAI) Suggest(f *Flow) (*Suggestion, error) {
	a.called = true
	return a.s, nil
}

func TestMatcherLayerOrder(t *testing.T) {
	rules := NewRuleEngine([]*Rule{{
		ID: 1, Enabled: true, MatchField: MatchCounterparty, Pattern: "张三",
		CounterAccountCode: "224101",
	}})
	history := NewHistoryMatcher([]HistoryEntry{{
		VoucherNo: "V1", Direction: DirIn, CounterpartyName: "张三",
		CounterAccountCode: "9999",
	}})
	ai := &stubAI{s: &Suggestion{CounterAccountCode: "8888", Confidence: 0.9}}

	m := &Matcher{Rules: rules, History: history, AI: ai}
	s := m.Match(flow("张三", "转账", DirIn, y(100)))
	if s.Layer != LayerRule {
		t.Errorf("应优先用规则，得到 %s", s.Layer)
	}
	if ai.called {
		t.Error("规则命中时不应调用 AI（省成本、避免不确定性）")
	}
}

func TestMatcherFallsBackToHistory(t *testing.T) {
	rules := NewRuleEngine([]*Rule{{
		ID: 1, Enabled: true, MatchField: MatchCounterparty, Pattern: "李四",
		CounterAccountCode: "224101",
	}})
	history := NewHistoryMatcher([]HistoryEntry{{
		VoucherNo: "V1", Direction: DirIn, CounterpartyName: "张三",
		CounterAccountCode: "224101",
	}})
	m := &Matcher{Rules: rules, History: history}
	s := m.Match(flow("张三", "借款", DirIn, y(100)))
	if s == nil || s.Layer != LayerHistory {
		t.Fatalf("规则未命中时应回落到历史，得到 %+v", s)
	}
}

func TestMatcherFallsBackToAI(t *testing.T) {
	rules := NewRuleEngine(nil)
	history := NewHistoryMatcher(nil)
	ai := &stubAI{s: &Suggestion{CounterAccountCode: "5602", Confidence: 0.8}}
	m := &Matcher{Rules: rules, History: history, AI: ai}
	s := m.Match(flow("陌生公司", "服务费", DirOut, y(1000)))
	if s == nil || s.Layer != LayerAI {
		t.Fatalf("前两层未命中时应调用 AI，得到 %+v", s)
	}
	if !ai.called {
		t.Error("应调用 AI")
	}
}

// ★ AI 是增强不是依赖：AI 挂了必须不影响记账
func TestMatcherAIFailureIsNotFatal(t *testing.T) {
	m := &Matcher{Rules: NewRuleEngine(nil), History: NewHistoryMatcher(nil),
		AI: &failingAI{}}
	s := m.Match(flow("陌生公司", "服务费", DirOut, y(1000)))
	if s != nil {
		t.Errorf("AI 失败时应返回未匹配，得到 %+v", s)
	}
}

type failingAI struct{}

func (f *failingAI) Suggest(*Flow) (*Suggestion, error) {
	return nil, errStub
}

var errStub = &stubErr{}

type stubErr struct{}

func (e *stubErr) Error() string { return "AI 服务不可用" }

// AI 完全不配置时也要能用（用户没开 AI）
func TestMatcherWithoutAI(t *testing.T) {
	m := &Matcher{Rules: NewRuleEngine(nil), History: NewHistoryMatcher(nil)}
	if s := m.Match(flow("X", "Y", DirIn, y(1))); s != nil {
		t.Errorf("无 AI 时应返回未匹配，得到 %+v", s)
	}
}

// ---------------------------------------------------------------------------
// 生成分录
// ---------------------------------------------------------------------------

// ★ 收入：借 银行存款 / 贷 对方科目
func TestBuildEntriesIncome(t *testing.T) {
	f := flow("张三", "借款", DirIn, y(50000))
	f.CounterAccount = "224101"
	f.ContactID = i64(42)
	f.Memo = "收到张三借款"

	es, err := f.BuildEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 {
		t.Fatalf("分录数 = %d，期望 2", len(es))
	}
	// 借 银行存款
	if es[0].AccountCode != "1002" || es[0].Debit != y(50000) || !es[0].Credit.IsZero() {
		t.Errorf("第 1 条 = %+v，期望 借 1002 50,000", es[0])
	}
	// 贷 对方科目
	if es[1].AccountCode != "224101" || es[1].Credit != y(50000) || !es[1].Debit.IsZero() {
		t.Errorf("第 2 条 = %+v，期望 贷 224101 50,000", es[1])
	}
	// ★ 往来单位挂在对方科目那侧 —— 挂在银行存款上没有意义
	if es[1].ContactID == nil || *es[1].ContactID != 42 {
		t.Error("往来单位应挂在对方科目一侧")
	}
	if es[0].ContactID != nil {
		t.Error("银行存款不应挂往来单位")
	}
	if es[0].Summary != "收到张三借款" || es[1].Summary != "收到张三借款" {
		t.Error("两条摘要应一致")
	}
}

// ★ 支出：借 对方科目 / 贷 银行存款
func TestBuildEntriesExpense(t *testing.T) {
	f := flow("办公用品供应商", "采购", DirOut, y(3000))
	f.CounterAccount = "560206"
	es, err := f.BuildEntries()
	if err != nil {
		t.Fatal(err)
	}
	// 借方在前：支出时对方科目是借方
	if es[0].AccountCode != "560206" || es[0].Debit != y(3000) {
		t.Errorf("第 1 条 = %+v，期望 借 560206", es[0])
	}
	if es[1].AccountCode != "1002" || es[1].Credit != y(3000) {
		t.Errorf("第 2 条 = %+v，期望 贷 1002", es[1])
	}
}

func TestBuildEntriesBalanced(t *testing.T) {
	for _, dir := range []Direction{DirIn, DirOut} {
		f := flow("X", "Y", dir, y(12345))
		f.CounterAccount = "5602"
		es, err := f.BuildEntries()
		if err != nil {
			t.Fatal(err)
		}
		var d, c money.Money
		for _, e := range es {
			d = d.Add(e.Debit)
			c = c.Add(e.Credit)
		}
		if d != c || d != y(12345) {
			t.Errorf("%s 方向借贷不平：借 %s 贷 %s", dir, d, c)
		}
	}
}

func TestBuildEntriesWithoutCounterAccount(t *testing.T) {
	f := flow("X", "Y", DirIn, y(100))
	if _, err := f.BuildEntries(); err == nil {
		t.Error("未匹配对方科目时应报错")
	}
}

func TestBuildEntriesDefaultMemo(t *testing.T) {
	f := flow("张三", "转账", DirIn, y(100))
	f.CounterAccount = "224101"
	es, _ := f.BuildEntries()
	if es[0].Summary != "转账" {
		t.Errorf("无 memo 时应回落到银行摘要，得到 %q", es[0].Summary)
	}

	f2 := flow("张三", "", DirIn, y(100))
	f2.CounterAccount = "224101"
	es2, _ := f2.BuildEntries()
	if es2[0].Summary != "张三" {
		t.Errorf("摘要也为空时应回落到对方户名，得到 %q", es2[0].Summary)
	}

	f3 := flow("", "", DirIn, y(100))
	f3.CounterAccount = "224101"
	es3, _ := f3.BuildEntries()
	if es3[0].Summary == "" {
		t.Error("全空时也应生成非空摘要")
	}
}

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

func TestFlowValidate(t *testing.T) {
	ok := flow("张三", "转账", DirIn, y(100))
	if err := ok.Validate(); err != nil {
		t.Errorf("合法流水不应报错: %v", err)
	}

	bad := []struct {
		name string
		f    *Flow
	}{
		{"方向非法", &Flow{AccountCode: "1002", TxnDate: calendar.MustParse("2025-09-01"),
			Direction: "x", Amount: y(1)}},
		{"金额为零", &Flow{AccountCode: "1002", TxnDate: calendar.MustParse("2025-09-01"),
			Direction: DirIn, Amount: 0}},
		{"金额为负", &Flow{AccountCode: "1002", TxnDate: calendar.MustParse("2025-09-01"),
			Direction: DirIn, Amount: y(-1)}},
		{"日期为空", &Flow{AccountCode: "1002", Direction: DirIn, Amount: y(1)}},
		{"缺科目", &Flow{TxnDate: calendar.MustParse("2025-09-01"),
			Direction: DirIn, Amount: y(1)}},
	}
	for _, c := range bad {
		if err := c.f.Validate(); err == nil {
			t.Errorf("%s 应报错", c.name)
		}
	}
}

func TestSuggestionActionable(t *testing.T) {
	if (&Suggestion{Confidence: 0.8}).IsActionable() != true {
		t.Error("0.8 应可自动勾选")
	}
	if (&Suggestion{Confidence: 0.7}).IsActionable() != false {
		t.Error("0.7 不应自动勾选")
	}
	if (&Suggestion{Confidence: 0.75}).IsActionable() != true {
		t.Error("0.75 是阈值，应可勾选")
	}
}
