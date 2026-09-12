package ledger

import (
	"errors"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

// ---------------------------------------------------------------------------
// 测试夹具：一棵覆盖常见业务的小科目树 + 期间表
// ---------------------------------------------------------------------------

func mkAcc(code, name, parent string, rt account.RootType, dir account.BalanceDir, aux ...account.AuxType) *account.Account {
	return &account.Account{
		Code: code, Name: name, ParentCode: parent,
		Level: (len(code) - 2) / 2, IsLeaf: true,
		RootType: rt, BalanceDir: dir, AuxTypes: aux, IsEnabled: true,
	}
}

func testCtx(t *testing.T) *Context {
	t.Helper()
	accts := []*account.Account{
		mkAcc("1002", "银行存款", "", account.RootAsset, account.DirDebit),
		mkAcc("1122", "应收账款", "", account.RootAsset, account.DirDebit, account.AuxCustomer),
		mkAcc("1221", "其他应收款", "", account.RootAsset, account.DirDebit),
		mkAcc("122102", "其他应收款—股东", "1221", account.RootAsset, account.DirDebit, account.AuxShareholder),
		mkAcc("2241", "其他应付款", "", account.RootLiability, account.DirCredit),
		mkAcc("224101", "其他应付款—股东", "2241", account.RootLiability, account.DirCredit, account.AuxShareholder),
		mkAcc("2221", "应交税费", "", account.RootLiability, account.DirCredit),
		mkAcc("222101", "应交税费—应交增值税", "2221", account.RootLiability, account.DirCredit),
		mkAcc("22210102", "应交增值税—销项税额", "222101", account.RootLiability, account.DirCredit),
		mkAcc("5602", "管理费用", "", account.RootExpense, account.DirDebit),
		mkAcc("560207", "管理费用—差旅费", "5602", account.RootExpense, account.DirDebit, account.AuxDept),
		mkAcc("5603", "财务费用", "", account.RootExpense, account.DirDebit),
		mkAcc("560306", "财务费用—四舍五入", "5603", account.RootExpense, account.DirDebit),
		mkAcc("5001", "主营业务收入", "", account.RootIncome, account.DirCredit),
	}
	tree, err := account.NewTree(accts)
	if err != nil {
		t.Fatalf("构造科目树失败: %v", err)
	}
	cal, err := period.NewCalendar(2025, 1, 2026, period.Key{Year: 2025, Month: 9})
	if err != nil {
		t.Fatalf("构造期间表失败: %v", err)
	}
	return &Context{
		Accounts: tree,
		Periods:  cal,
		ContactKinds: map[int64]string{
			1: "customer",
			2: "supplier",
			3: "shareholder",
			4: "employee",
		},
	}
}

func i64(v int64) *int64 { return &v }

var (
	sep11 = calendar.MustParse("2025-09-11")
	amt   = func(yuan int64) money.Money { return money.Money(yuan) * money.Yuan }
)

// ---------------------------------------------------------------------------
// 基本构造与平衡
// ---------------------------------------------------------------------------

func TestSimpleBalancedPosting(t *testing.T) {
	ctx := testCtx(t)
	p := NewPosting(sep11)
	p.Debit("1002", amt(50000))
	p.CreditWith("224101", "", amt(50000), Aux{ContactID: i64(3)})
	p.SetSummary("收到张三股东借款")

	per, err := p.Validate(ctx)
	if err != nil {
		t.Fatalf("应通过校验，得到 %v", err)
	}
	if per.Key != (period.Key{Year: 2025, Month: 9}) {
		t.Errorf("期间 = %v", per.Key)
	}
	if !p.IsBalanced() {
		t.Error("应平衡")
	}
	if p.TotalDebit() != amt(50000) || p.TotalCredit() != amt(50000) {
		t.Errorf("合计 = %s / %s", p.TotalDebit(), p.TotalCredit())
	}
}

// 一借多贷（销售收入 + 销项税）
func TestOneDebitManyCredits(t *testing.T) {
	ctx := testCtx(t)
	p := NewPosting(sep11)
	p.DebitWith("1122", "销售商品", amt(11300), Aux{ContactID: i64(1)})
	p.CreditWith("5001", "销售商品", amt(10000), Aux{})
	p.CreditWith("22210102", "销项税额", amt(1300), Aux{})
	p.SetSummary("销售商品")

	if _, err := p.Validate(ctx); err != nil {
		t.Fatalf("应通过校验，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 不变式 1：分录数 ≥ 2
// ---------------------------------------------------------------------------

func TestInvariant_MinimumEntries(t *testing.T) {
	ctx := testCtx(t)

	p := NewPosting(sep11)
	if _, err := p.Validate(ctx); !errors.Is(err, ErrNoEntries) {
		t.Errorf("零分录应报 ErrNoEntries，得到 %v", err)
	}

	p = NewPosting(sep11)
	p.Debit("1002", amt(100))
	if _, err := p.Validate(ctx); !errors.Is(err, ErrTooFewEntries) {
		t.Errorf("单条分录应报 ErrTooFewEntries，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 不变式 2：借/贷恰有一个 > 0
// ---------------------------------------------------------------------------

func TestInvariant_BothSides(t *testing.T) {
	ctx := testCtx(t)
	p := NewPosting(sep11)
	p.Add(Entry{AccountCode: "1002", Summary: "x", Debit: amt(100), Credit: amt(100)})
	p.CreditWith("224101", "", amt(100), Aux{ContactID: i64(3)})
	if _, err := p.Validate(ctx); !errors.Is(err, ErrBothSides) {
		t.Errorf("同时有借有贷应报 ErrBothSides，得到 %v", err)
	}
}

func TestInvariant_NoAmount(t *testing.T) {
	ctx := testCtx(t)
	p := NewPosting(sep11)
	p.Add(Entry{AccountCode: "1002", Summary: "x"})
	p.CreditWith("224101", "", amt(100), Aux{ContactID: i64(3)})
	if _, err := p.Validate(ctx); !errors.Is(err, ErrNoAmount) {
		t.Errorf("借贷都为零应报 ErrNoAmount，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 不变式 3：金额非负
// ---------------------------------------------------------------------------

// ★ 这条是刻意与 Frappe Books 分道扬镳的地方。
//
// Frappe 允许总账分录的金额为负数（退货就是靠负数金额实现的），
// 后果是各报表对负数的处理不一致：总账 Math.abs() 掉了，
// 试算平衡表没有，现金流量表又没有 —— 同一笔退货在不同表里对不上。
//
// 本项目一律用「借贷互换」表达冲销，金额永远非负。
func TestInvariant_NegativeAmountRejected(t *testing.T) {
	ctx := testCtx(t)
	p := NewPosting(sep11)
	p.Add(Entry{AccountCode: "1002", Summary: "退货", Debit: amt(-100)})
	p.Credit("5001", amt(-100))
	_, err := p.Validate(ctx)
	if !errors.Is(err, ErrNegativeAmount) {
		t.Fatalf("负数金额应被拒绝，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 不变式 4：摘要非空
// ---------------------------------------------------------------------------

func TestInvariant_SummaryRequired(t *testing.T) {
	ctx := testCtx(t)
	p := NewPosting(sep11)
	p.Debit("1002", amt(100)) // 无摘要
	p.CreditWith("224101", "", amt(100), Aux{ContactID: i64(3)})
	if _, err := p.Validate(ctx); !errors.Is(err, ErrMissingSummary) {
		t.Errorf("缺摘要应报错，得到 %v", err)
	}
}

func TestSetSummaryFillsOnlyEmpty(t *testing.T) {
	p := NewPosting(sep11)
	p.DebitWith("1002", "已有摘要", amt(100), Aux{})
	p.CreditWith("224101", "", amt(100), Aux{ContactID: i64(3)})
	p.SetSummary("默认摘要")
	if p.entries[0].Summary != "已有摘要" {
		t.Error("不应覆盖已有摘要")
	}
	if p.entries[1].Summary != "默认摘要" {
		t.Error("应填充空摘要")
	}
}

// ---------------------------------------------------------------------------
// 不变式 5：借贷平衡
// ---------------------------------------------------------------------------

func TestInvariant_MustBalance(t *testing.T) {
	ctx := testCtx(t)
	p := NewPosting(sep11)
	p.Debit("1002", amt(100))
	p.CreditWith("224101", "", amt(99), Aux{ContactID: i64(3)})
	p.SetSummary("测试")

	_, err := p.Validate(ctx)
	if !errors.Is(err, ErrNotBalanced) {
		t.Fatalf("不平衡应报 ErrNotBalanced，得到 %v", err)
	}
	// 错误信息应包含差额，便于定位
	if got := p.Difference(); got != amt(1) {
		t.Errorf("差额 = %s，期望 1.00", got)
	}
}

// ---------------------------------------------------------------------------
// 不变式 6：科目与期间
// ---------------------------------------------------------------------------

func TestInvariant_AccountMustExist(t *testing.T) {
	ctx := testCtx(t)
	p := NewPosting(sep11)
	p.Debit("9999", amt(100))
	p.CreditWith("224101", "", amt(100), Aux{ContactID: i64(3)})
	p.SetSummary("x")
	if _, err := p.Validate(ctx); err == nil {
		t.Fatal("不存在的科目应被拒绝")
	}
}

// ★ 汇总科目不可记账。
// Frappe Books 没有这个校验，可以记到「应交税费」（有下级）上，
// 导致该科目自身余额与下级汇总重复计算。
func TestInvariant_GroupAccountNotPostable(t *testing.T) {
	ctx := testCtx(t)
	p := NewPosting(sep11)
	p.Debit("2221", amt(1300)) // 2221 有下级，是汇总科目
	p.CreditWith("224101", "", amt(1300), Aux{ContactID: i64(3)})
	p.SetSummary("x")
	_, err := p.Validate(ctx)
	if err == nil {
		t.Fatal("汇总科目应被拒绝记账")
	}
	if !strings.Contains(err.Error(), "汇总科目") {
		t.Errorf("错误信息应说明是汇总科目，得到 %v", err)
	}
}

func TestInvariant_PeriodMustBeOpen(t *testing.T) {
	ctx := testCtx(t)

	// 未来期间
	p := NewPosting(calendar.MustParse("2025-11-05"))
	p.Debit("1002", amt(100))
	p.CreditWith("224101", "", amt(100), Aux{ContactID: i64(3)})
	p.SetSummary("x")
	if _, err := p.Validate(ctx); !errors.Is(err, period.ErrNotOpen) {
		t.Errorf("未启用期间应报 ErrNotOpen，得到 %v", err)
	}

	// 结账后的期间
	if err := ctx.Periods.Close(period.Key{Year: 2025, Month: 1}, "admin", time.Now()); err != nil {
		t.Fatal(err)
	}
	p2 := NewPosting(calendar.MustParse("2025-01-20"))
	p2.Debit("1002", amt(100))
	p2.CreditWith("224101", "", amt(100), Aux{ContactID: i64(3)})
	p2.SetSummary("x")
	if _, err := p2.Validate(ctx); !errors.Is(err, period.ErrNotOpen) {
		t.Errorf("已结账期间应报 ErrNotOpen，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 不变式 7：辅助核算
// ---------------------------------------------------------------------------

// ★ 科目要求客户，分录却没填 → 必须拒绝。
// 少了这条校验，往来账会静默丢数据。
func TestInvariant_MissingAuxRejected(t *testing.T) {
	ctx := testCtx(t)
	p := NewPosting(sep11)
	p.DebitWith("1122", "销售商品", amt(11300), Aux{}) // 1122 要求 customer，未填
	p.Credit("5001", amt(11300))
	p.SetSummary("x")
	_, err := p.Validate(ctx)
	if !errors.Is(err, ErrMissingAux) {
		t.Fatalf("缺辅助核算应报 ErrMissingAux，得到 %v", err)
	}
}

func TestInvariant_AuxKindMustMatch(t *testing.T) {
	ctx := testCtx(t)
	p := NewPosting(sep11)
	// 1122 应收账款要求「客户」，但填的是股东(3)
	p.DebitWith("1122", "销售商品", amt(11300), Aux{ContactID: i64(3)})
	p.Credit("5001", amt(11300))
	p.SetSummary("x")
	_, err := p.Validate(ctx)
	if !errors.Is(err, ErrWrongAuxKind) {
		t.Fatalf("辅助核算类型不匹配应报 ErrWrongAuxKind，得到 %v", err)
	}
}

func TestAuxSatisfied(t *testing.T) {
	ctx := testCtx(t)
	// 客户正确
	p := NewPosting(sep11)
	p.DebitWith("1122", "销售商品", amt(11300), Aux{ContactID: i64(1)})
	p.Credit("5001", amt(11300))
	p.SetSummary("x")
	if _, err := p.Validate(ctx); err != nil {
		t.Errorf("客户辅助核算正确时应通过，得到 %v", err)
	}

	// 股东正确
	p = NewPosting(sep11)
	p.Debit("1002", amt(50000))
	p.CreditWith("224101", "收到借款", amt(50000), Aux{ContactID: i64(3)})
	p.SetSummary("x")
	if _, err := p.Validate(ctx); err != nil {
		t.Errorf("股东辅助核算正确时应通过，得到 %v", err)
	}

	// 部门维度的科目
	p = NewPosting(sep11)
	p.DebitWith("560207", "出差", amt(800), Aux{DeptID: i64(7)})
	p.Credit("1002", amt(800))
	p.SetSummary("x")
	if _, err := p.Validate(ctx); err != nil {
		t.Errorf("部门辅助核算正确时应通过，得到 %v", err)
	}

	// 部门缺失
	p = NewPosting(sep11)
	p.DebitWith("560207", "出差", amt(800), Aux{})
	p.Credit("1002", amt(800))
	p.SetSummary("x")
	if _, err := p.Validate(ctx); !errors.Is(err, ErrMissingAux) {
		t.Errorf("缺部门应报 ErrMissingAux，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 红字冲销
// ---------------------------------------------------------------------------

func TestReverse(t *testing.T) {
	ctx := testCtx(t)
	p := NewPosting(sep11)
	p.DebitWith("1122", "销售商品", amt(11300), Aux{ContactID: i64(1)})
	p.Credit("5001", amt(10000))
	p.Credit("22210102", amt(1300))
	p.SetSummary("销售商品")

	r := p.Reverse()
	if r.Len() != p.Len() {
		t.Fatalf("冲销后分录数 %d ≠ 原 %d", r.Len(), p.Len())
	}
	// 借贷互换
	if r.entries[0].Credit != amt(11300) || !r.entries[0].Debit.IsZero() {
		t.Errorf("第 1 行应变为贷方 %s，实际借 %s 贷 %s",
			amt(11300), r.entries[0].Debit, r.entries[0].Credit)
	}
	if r.entries[1].Debit != amt(10000) {
		t.Errorf("第 2 行应变为借方 %s，实际 %s", amt(10000), r.entries[1].Debit)
	}
	// 辅助核算应保留
	if r.entries[0].Aux.ContactID == nil || *r.entries[0].Aux.ContactID != 1 {
		t.Error("冲销应保留辅助核算")
	}
	// 冲销凭证本身也必须平衡
	if !r.IsBalanced() {
		t.Error("冲销凭证应平衡")
	}
	if _, err := r.Validate(ctx); err != nil {
		t.Errorf("冲销凭证应通过校验，得到 %v", err)
	}
	// 原凭证与冲销凭证合计为零（这是「冲销」的定义）
	if got := p.Difference().Add(r.Difference()); !got.IsZero() {
		t.Errorf("原凭证与冲销凭证合计应为零，得到 %s", got)
	}
}

func TestReverseTwiceIsIdentity(t *testing.T) {
	p := NewPosting(sep11)
	p.Debit("1002", amt(500))
	p.CreditWith("224101", "", amt(500), Aux{ContactID: i64(3)})
	p.SetSummary("x")

	back := p.Reverse().Reverse()
	for i := range p.entries {
		if back.entries[i].Debit != p.entries[i].Debit ||
			back.entries[i].Credit != p.entries[i].Credit {
			t.Errorf("两次冲销应还原：第 %d 行 %s/%s ≠ %s/%s", i+1,
				back.entries[i].Debit, back.entries[i].Credit,
				p.entries[i].Debit, p.entries[i].Credit)
		}
	}
}

// ---------------------------------------------------------------------------
// 尾差
// ---------------------------------------------------------------------------

func TestEnsureBalanceSmallDifference(t *testing.T) {
	p := NewPosting(sep11)
	p.Debit("1002", amt(113))
	p.Credit("5001", money.Money(10000))    // 100.00
	p.Credit("22210102", money.Money(1299)) // 12.99 —— 合计 112.99，差 1 分
	p.SetSummary("x")

	if p.IsBalanced() {
		t.Fatal("前置条件：应不平衡")
	}
	adj, err := p.EnsureBalance("560306", money.Money(2)) // 允许 2 分尾差
	if err != nil {
		t.Fatalf("应在尾差范围内自动调平，得到 %v", err)
	}
	if adj != money.Money(1) {
		t.Errorf("调整额 = %s，期望 0.01", adj)
	}
	if !p.IsBalanced() {
		t.Error("调整后应平衡")
	}
	// 补的分录应在贷方（借方多）
	last := p.entries[len(p.entries)-1]
	if last.AccountCode != "560306" || last.Credit != money.Money(1) {
		t.Errorf("尾差分录 = %+v", last)
	}
}

// ★ 大额不平衡必须报错，绝不能被尾差科目掩盖。
// Frappe Books 的 makeRoundOffEntry 没有金额上限，任何差额都会自动调平，
// 这会把「分录填错」伪装成「尾差」。
func TestEnsureBalanceRefusesLargeDifference(t *testing.T) {
	p := NewPosting(sep11)
	p.Debit("1002", amt(1000))
	p.Credit("5001", amt(900)) // 差 100 元
	p.SetSummary("x")

	n := p.Len()
	if _, err := p.EnsureBalance("560306", money.Money(2)); !errors.Is(err, ErrNotBalanced) {
		t.Fatalf("大额差额应拒绝调平，得到 %v", err)
	}
	if p.Len() != n {
		t.Error("拒绝时不应修改分录")
	}
}

func TestEnsureBalanceNoopWhenBalanced(t *testing.T) {
	p := NewPosting(sep11)
	p.Debit("1002", amt(100))
	p.Credit("5001", amt(100))
	p.SetSummary("x")

	adj, err := p.EnsureBalance("560306", money.Money(2))
	if err != nil || !adj.IsZero() {
		t.Errorf("已平衡时应无操作，得到 %s %v", adj, err)
	}
	if p.Len() != 2 {
		t.Errorf("已平衡时不应追加分录，实际 %d 条", p.Len())
	}
}

// ---------------------------------------------------------------------------
// 综合场景
// ---------------------------------------------------------------------------

// 需求核心场景：银行流水导入 → 股东借款
func TestScenario_ShareholderLoan(t *testing.T) {
	ctx := testCtx(t)
	// 股东转入 50,000
	p := NewPosting(sep11)
	p.DebitWith("1002", "收到张三借款", amt(50000), Aux{})
	p.CreditWith("224101", "收到张三借款", amt(50000), Aux{ContactID: i64(3)})
	if _, err := p.Validate(ctx); err != nil {
		t.Fatalf("股东借款凭证应通过，得到 %v", err)
	}

	// 归还 20,000 —— 红字方向
	r := NewPosting(calendar.MustParse("2025-09-20"))
	r.DebitWith("224101", "归还张三借款", amt(20000), Aux{ContactID: i64(3)})
	r.CreditWith("1002", "归还张三借款", amt(20000), Aux{})
	if _, err := r.Validate(ctx); err != nil {
		t.Fatalf("归还借款凭证应通过，得到 %v", err)
	}
}

// 工资计提（需求中的工资模块）
func TestScenario_SalaryAccrual(t *testing.T) {
	ctx := testCtx(t)
	// 简化版：借 管理费用 贷 应付职工薪酬
	p := NewPosting(sep11)
	p.DebitWith("560207", "计提 9 月工资", amt(30000), Aux{DeptID: i64(1)})
	p.CreditWith("224101", "计提 9 月工资", amt(30000), Aux{ContactID: i64(3)})
	if _, err := p.Validate(ctx); err != nil {
		t.Fatalf("工资计提应通过，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------
