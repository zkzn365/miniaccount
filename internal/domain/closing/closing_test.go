package closing

import (
	"errors"
	"testing"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

func y(n int64) money.Money { return money.Money(n) * money.Yuan }

var k202509 = period.NewKey(2025, 9)

func ac() Accounts { return DefaultAccounts() }

// 一个典型的盈利月份：收入 80,000，费用 15,000
func profitablePnL() []PnLAccount {
	return []PnLAccount{
		{Code: "5001", Name: "主营业务收入", RootType: account.RootIncome, Raw: y(-80000)},
		{Code: "5602", Name: "管理费用", RootType: account.RootExpense, Raw: y(15000)},
	}
}

// ---------------------------------------------------------------------------
// 结转损益
// ---------------------------------------------------------------------------

func TestBuildEntriesProfit(t *testing.T) {
	res, err := BuildEntries(k202509, profitablePnL(), ac(), "")
	if err != nil {
		t.Fatalf("结转失败: %v", err)
	}
	if res.TotalIncome != y(80000) {
		t.Errorf("收入合计 = %s", res.TotalIncome)
	}
	if res.TotalExpense != y(15000) {
		t.Errorf("费用合计 = %s", res.TotalExpense)
	}
	if res.Profit != y(65000) {
		t.Errorf("利润 = %s，期望 65000.00", res.Profit)
	}
	if res.AccountCount != 2 {
		t.Errorf("参与科目数 = %d，期望 2", res.AccountCount)
	}

	// 借贷平衡
	var d, c money.Money
	for _, e := range res.Entries {
		d, c = d.Add(e.Debit), c.Add(e.Credit)
	}
	if d != c || d != y(80000) {
		t.Errorf("借贷 = %s / %s，期望各 80000.00", d, c)
	}

	// 收入类应被借记冲平
	var incomeEntry, expenseEntry, profitEntry *Entry
	for i := range res.Entries {
		switch res.Entries[i].AccountCode {
		case "5001":
			incomeEntry = &res.Entries[i]
		case "5602":
			expenseEntry = &res.Entries[i]
		case "3103":
			profitEntry = &res.Entries[i]
		}
	}
	if incomeEntry == nil || incomeEntry.Debit != y(80000) {
		t.Errorf("收入类应借记冲平，得到 %+v", incomeEntry)
	}
	if expenseEntry == nil || expenseEntry.Credit != y(15000) {
		t.Errorf("费用类应贷记冲平，得到 %+v", expenseEntry)
	}
	if profitEntry == nil || profitEntry.Credit != y(65000) {
		t.Errorf("盈利应贷记本年利润，得到 %+v", profitEntry)
	}
}

// ★ 亏损方向相反：借本年利润
func TestBuildEntriesLoss(t *testing.T) {
	pnl := []PnLAccount{
		{Code: "5001", Name: "主营业务收入", RootType: account.RootIncome, Raw: y(-10000)},
		{Code: "5602", Name: "管理费用", RootType: account.RootExpense, Raw: y(30000)},
	}
	res, err := BuildEntries(k202509, pnl, ac(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Profit != y(-20000) {
		t.Errorf("利润 = %s，期望 -20000.00（亏损）", res.Profit)
	}
	var profitEntry *Entry
	for i := range res.Entries {
		if res.Entries[i].AccountCode == "3103" {
			profitEntry = &res.Entries[i]
		}
	}
	if profitEntry == nil || profitEntry.Debit != y(20000) {
		t.Errorf("亏损应借记本年利润，得到 %+v", profitEntry)
	}
	var d, c money.Money
	for _, e := range res.Entries {
		d, c = d.Add(e.Debit), c.Add(e.Credit)
	}
	if d != c {
		t.Errorf("借贷不平：%s vs %s", d, c)
	}
}

// 零余额科目不进凭证（避免凭证上出现 0.00 的干扰行）
func TestBuildEntriesSkipsZeroBalances(t *testing.T) {
	pnl := []PnLAccount{
		{Code: "5001", Name: "主营业务收入", RootType: account.RootIncome, Raw: y(-1000)},
		{Code: "5051", Name: "其他业务收入", RootType: account.RootIncome, Raw: 0},
		{Code: "5301", Name: "营业外收入", RootType: account.RootIncome, Raw: 0},
		{Code: "5602", Name: "管理费用", RootType: account.RootExpense, Raw: y(400)},
	}
	res, err := BuildEntries(k202509, pnl, ac(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.AccountCount != 2 {
		t.Errorf("参与科目数 = %d，期望 2（零余额应跳过）", res.AccountCount)
	}
	for _, e := range res.Entries {
		if e.AccountCode == "5051" || e.AccountCode == "5301" {
			t.Errorf("零余额科目不应出现在凭证上: %+v", e)
		}
	}
}

// 红字冲销后余额方向反转时，结转方向也应反转
func TestBuildEntriesReversedBalance(t *testing.T) {
	// 收入出现借方余额（红字冲销超过原收入）
	pnl := []PnLAccount{
		{Code: "5001", Name: "主营业务收入", RootType: account.RootIncome, Raw: y(500)},
	}
	res, err := BuildEntries(k202509, pnl, ac(), "")
	if err != nil {
		t.Fatal(err)
	}
	var e *Entry
	for i := range res.Entries {
		if res.Entries[i].AccountCode == "5001" {
			e = &res.Entries[i]
		}
	}
	if e == nil || e.Credit != y(500) || !e.Debit.IsZero() {
		t.Errorf("借方余额的收入科目应贷记冲平，得到 %+v", e)
	}
	if res.Profit != y(-500) {
		t.Errorf("利润 = %s，期望 -500.00", res.Profit)
	}
}

// 只有收入或只有费用时也要平衡
func TestBuildEntriesOnlyIncome(t *testing.T) {
	res, err := BuildEntries(k202509, []PnLAccount{
		{Code: "5001", Name: "主营业务收入", RootType: account.RootIncome, Raw: y(-5000)},
	}, ac(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalExpense != 0 || res.Profit != y(5000) {
		t.Errorf("费用 = %s，利润 = %s", res.TotalExpense, res.Profit)
	}
}

func TestBuildEntriesErrors(t *testing.T) {
	// 无损益可转
	if _, err := BuildEntries(k202509, nil, ac(), ""); !errors.Is(err, ErrNoPnLAccount) {
		t.Errorf("无损益应报 ErrNoPnLAccount，得到 %v", err)
	}
	// 全是零余额
	if _, err := BuildEntries(k202509, []PnLAccount{
		{Code: "5001", RootType: account.RootIncome, Raw: 0},
	}, ac(), ""); !errors.Is(err, ErrNoPnLAccount) {
		t.Errorf("零余额应报 ErrNoPnLAccount，得到 %v", err)
	}
	// 期间非法
	if _, err := BuildEntries(period.NewKey(2025, 13), profitablePnL(), ac(), ""); !errors.Is(err, ErrBadPeriod) {
		t.Errorf("非法期间应报 ErrBadPeriod，得到 %v", err)
	}
	// 非损益类科目混入
	if _, err := BuildEntries(k202509, []PnLAccount{
		{Code: "1002", Name: "银行存款", RootType: account.RootAsset, Raw: y(100)},
	}, ac(), ""); err == nil {
		t.Error("非损益类科目应报错")
	}
	// 未指定本年利润科目
	if _, err := BuildEntries(k202509, profitablePnL(), Accounts{}, ""); err == nil {
		t.Error("未指定本年利润科目应报错")
	}
}

// 自定义摘要
func TestBuildEntriesCustomSummary(t *testing.T) {
	res, _ := BuildEntries(k202509, profitablePnL(), ac(), "月末结转")
	for _, e := range res.Entries {
		if e.Summary != "月末结转" {
			t.Errorf("摘要 = %q，期望「月末结转」", e.Summary)
		}
	}
	// 默认摘要应含年月
	res2, _ := BuildEntries(k202509, profitablePnL(), ac(), "")
	if res2.Entries[0].Summary == "" {
		t.Error("默认摘要不应为空")
	}
}

// 结转后所有损益类科目应归零（数学验证）
func TestClosingZeroesPnLAccounts(t *testing.T) {
	pnl := []PnLAccount{
		{Code: "5001", RootType: account.RootIncome, Raw: y(-80000)},
		{Code: "5051", RootType: account.RootIncome, Raw: y(-2000)},
		{Code: "5111", RootType: account.RootIncome, Raw: y(-500)},
		{Code: "5401", RootType: account.RootExpense, Raw: y(50000)},
		{Code: "5602", RootType: account.RootExpense, Raw: y(15000)},
		{Code: "5603", RootType: account.RootExpense, Raw: y(800)},
	}
	res, err := BuildEntries(k202509, pnl, ac(), "")
	if err != nil {
		t.Fatal(err)
	}
	// 模拟过账：把每条分录加回到原余额上，结果必须为零
	delta := map[string]money.Money{}
	for _, e := range res.Entries {
		delta[e.AccountCode] = delta[e.AccountCode].Add(e.Debit).Sub(e.Credit)
	}
	for _, a := range pnl {
		if got := a.Raw.Add(delta[a.Code]); !got.IsZero() {
			t.Errorf("科目 %s 结转后余额 = %s，期望 0", a.Code, got)
		}
	}
	// 利润 = 82500 − 65800 = 16700
	want := y(82500).Sub(y(65800))
	if res.Profit != want {
		t.Errorf("利润 = %s，期望 %s", res.Profit, want)
	}
}

// ---------------------------------------------------------------------------
// 年末结转本年利润
// ---------------------------------------------------------------------------

func TestBuildYearEndEntriesProfit(t *testing.T) {
	// 本年利润贷方余额 65,000（原始净额为负 = 贷方）
	entries, err := BuildYearEndEntries(period.NewKey(2025, 12), y(-65000), ac(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("分录数 = %d，期望 2", len(entries))
	}
	if entries[0].AccountCode != "3103" || entries[0].Debit != y(65000) {
		t.Errorf("应借本年利润，得到 %+v", entries[0])
	}
	if entries[1].AccountCode != "310401" || entries[1].Credit != y(65000) {
		t.Errorf("应贷未分配利润，得到 %+v", entries[1])
	}
}

func TestBuildYearEndEntriesLoss(t *testing.T) {
	// 本年利润借方余额 20,000（亏损）
	entries, err := BuildYearEndEntries(period.NewKey(2025, 12), y(20000), ac(), "")
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].AccountCode != "310401" || entries[0].Debit != y(20000) {
		t.Errorf("亏损应借未分配利润，得到 %+v", entries[0])
	}
	if entries[1].AccountCode != "3103" || entries[1].Credit != y(20000) {
		t.Errorf("亏损应贷本年利润，得到 %+v", entries[1])
	}
}

func TestBuildYearEndEntriesZero(t *testing.T) {
	entries, err := BuildYearEndEntries(period.NewKey(2025, 12), 0, ac(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("无利润可转时不应产生分录，得到 %+v", entries)
	}
}

func TestBuildYearEndEntriesErrors(t *testing.T) {
	if _, err := BuildYearEndEntries(period.NewKey(2025, 12), y(-1),
		Accounts{Profit: "3103", RetainedEarnings: "3103"}, ""); !errors.Is(err, ErrSameAccount) {
		t.Errorf("两个科目相同应报 ErrSameAccount，得到 %v", err)
	}
	if _, err := BuildYearEndEntries(period.NewKey(2025, 13), y(-1), ac(), ""); !errors.Is(err, ErrBadPeriod) {
		t.Errorf("非法期间应报错，得到 %v", err)
	}
	if _, err := BuildYearEndEntries(period.NewKey(2025, 12), y(-1), Accounts{}, ""); err == nil {
		t.Error("缺科目应报错")
	}
}

func TestIsYearEnd(t *testing.T) {
	if !IsYearEnd(period.NewKey(2025, 12)) {
		t.Error("12 月应为年末")
	}
	for m := 1; m <= 11; m++ {
		if IsYearEnd(period.NewKey(2025, m)) {
			t.Errorf("%d 月不应为年末", m)
		}
	}
}

// ---------------------------------------------------------------------------
// 结账计划
// ---------------------------------------------------------------------------

func TestBuildPlanMidYear(t *testing.T) {
	plan, err := BuildPlan(k202509, profitablePnL(), 0, ac())
	if err != nil {
		t.Fatal(err)
	}
	if plan.YearEnd {
		t.Error("9 月不应是年末")
	}
	if plan.Closing == nil || plan.Closing.Profit != y(65000) {
		t.Errorf("结转结果 = %+v", plan.Closing)
	}
	if len(plan.ClosingEntries) != 3 {
		t.Errorf("分录数 = %d，期望 3", len(plan.ClosingEntries))
	}
	if err := plan.Validate(); err != nil {
		t.Errorf("计划应自洽: %v", err)
	}
	if !plan.HasEntries() {
		t.Error("应有分录")
	}
	// 步骤：结转损益（完成）+ 年末结转（跳过）
	if len(plan.Steps) != 2 {
		t.Fatalf("步骤数 = %d，期望 2", len(plan.Steps))
	}
	if !plan.Steps[0].Done {
		t.Error("结转损益应标记完成")
	}
	if !plan.Steps[1].Skipped {
		t.Error("9 月不应做年末结转，应标记跳过")
	}
	if plan.Summary() == "" {
		t.Error("应有一句话总结")
	}
}

func TestBuildPlanYearEnd(t *testing.T) {
	// ★ profitBalance 传的是**本次结转之前**的余额。
	// 这里本年利润原本为 0，本期赚 65,000，年末应将 65,000 转入未分配利润。
	plan, err := BuildPlan(period.NewKey(2025, 12), profitablePnL(), 0, ac())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.YearEnd {
		t.Error("12 月应为年末")
	}
	// 结转损益 3 条 + 年末结转 2 条
	if len(plan.ClosingEntries) != 5 {
		t.Errorf("分录数 = %d，期望 5", len(plan.ClosingEntries))
	}
	if err := plan.Validate(); err != nil {
		t.Errorf("含年末结转的计划也应自洽: %v", err)
	}
	// 年末结转步骤不应被跳过
	if plan.Steps[1].Skipped {
		t.Error("12 月应做年末结转")
	}
	// 年末结转金额应等于「结转前 0 − 本期利润 65,000」的绝对值
	var yeDebit, yeCredit money.Money
	for _, e := range plan.ClosingEntries {
		if e.AccountCode == "310401" {
			yeCredit = yeCredit.Add(e.Credit)
			yeDebit = yeDebit.Add(e.Debit)
		}
	}
	if yeCredit != y(65000) || !yeDebit.IsZero() {
		t.Errorf("未分配利润应贷记 65,000，实际借 %s 贷 %s", yeDebit, yeCredit)
	}
	// 整张凭证（含结转损益与年末结转）必须仍然平衡
	var d, c money.Money
	for _, e := range plan.ClosingEntries {
		d, c = d.Add(e.Debit), c.Add(e.Credit)
	}
	if d != c {
		t.Errorf("借贷不平：借 %s 贷 %s", d, c)
	}
}

// ★ 年中已结转过的利润，年末要连同本期一起转走，不能只转 12 月这一期
func TestBuildPlanYearEndCarriesPriorMonths(t *testing.T) {
	// 1—11 月已累计把 20,000 转入本年利润（贷方余额 → raw = −20,000），
	// 12 月又赚 65,000，年末应转走 85,000。
	plan, err := BuildPlan(period.NewKey(2025, 12), profitablePnL(), y(-20000), ac())
	if err != nil {
		t.Fatal(err)
	}
	var credit money.Money
	for _, e := range plan.ClosingEntries {
		if e.AccountCode == "310401" {
			credit = credit.Add(e.Credit)
		}
	}
	if credit != y(85000) {
		t.Errorf("未分配利润应贷记 85,000，实际 %s", credit)
	}
}

// 没有本期损益时，年末结转只处理账上已有的本年利润
func TestBuildPlanYearEndWithoutPnL(t *testing.T) {
	plan, err := BuildPlan(period.NewKey(2025, 12), nil, y(-20000), ac())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Closing != nil {
		t.Error("无损益时不应有结转结果")
	}
	var credit money.Money
	for _, e := range plan.ClosingEntries {
		credit = credit.Add(e.Credit)
	}
	if credit != y(20000) {
		t.Errorf("应把已有的 20,000 本年利润转入未分配利润，实际 %s", credit)
	}
}

func TestBuildPlanNoPnL(t *testing.T) {
	plan, err := BuildPlan(k202509, nil, 0, ac())
	if err != nil {
		t.Fatalf("无损益不应报错: %v", err)
	}
	if plan.Closing != nil {
		t.Error("无损益时不应有结转结果")
	}
	if plan.HasEntries() {
		t.Error("无损益时不应有分录")
	}
	// 标记为跳过而不是失败
	if !plan.Steps[0].Skipped {
		t.Error("无损益应标记跳过")
	}
	if plan.Summary() == "" {
		t.Error("应有一句话总结")
	}
}

func TestPlanValidateRejectsBadEntries(t *testing.T) {
	// 借贷不平
	p := &Plan{ClosingEntries: []Entry{
		{AccountCode: "5001", Debit: y(100)},
		{AccountCode: "3103", Credit: y(99)},
	}}
	if err := p.Validate(); !errors.Is(err, ErrNotBalanced) {
		t.Errorf("借贷不平应被检出，得到 %v", err)
	}
	// 同时有借有贷
	p2 := &Plan{ClosingEntries: []Entry{
		{AccountCode: "5001", Debit: y(100), Credit: y(100)},
	}}
	if err := p2.Validate(); err == nil {
		t.Error("同时有借有贷应被检出")
	}
	// 金额为零
	p3 := &Plan{ClosingEntries: []Entry{{AccountCode: "5001"}}}
	if err := p3.Validate(); err == nil {
		t.Error("零金额分录应被检出")
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func TestPnLAccountDirection(t *testing.T) {
	income := PnLAccount{Raw: y(-100)}
	if income.Direction() != "debit" {
		t.Errorf("贷方余额应借记冲平，得到 %s", income.Direction())
	}
	expense := PnLAccount{Raw: y(100)}
	if expense.Direction() != "credit" {
		t.Errorf("借方余额应贷记冲平，得到 %s", expense.Direction())
	}
	zero := PnLAccount{Raw: 0}
	if zero.Direction() != "" {
		t.Errorf("零余额不应有方向，得到 %s", zero.Direction())
	}
}

func TestDate(t *testing.T) {
	if got := Date(period.NewKey(2025, 9)).String(); got != "2025-09-30" {
		t.Errorf("9 月末 = %s", got)
	}
	if got := Date(period.NewKey(2025, 2)).String(); got != "2025-02-28" {
		t.Errorf("2025-02 末 = %s", got)
	}
	if got := Date(period.NewKey(2024, 2)).String(); got != "2024-02-29" {
		t.Errorf("2024-02 末 = %s", got)
	}
}

func TestReversePlan(t *testing.T) {
	s := ReversePlan("转-2025-09-0005", k202509)
	if !s.Done || s.Key != "reverse_closing" {
		t.Errorf("反结账步骤 = %+v", s)
	}
	if s.Detail == "" {
		t.Error("应说明冲销哪张凭证")
	}
}
