package account

import (
	"errors"
	"testing"

	"miniaccount/internal/domain/money"
)

// 构造一棵符合《小企业会计准则》片段的小科目树，用于测试。
func sampleAccounts() []*Account {
	mk := func(code, name, parent string, rt RootType, dir BalanceDir, aux ...AuxType) *Account {
		lvl := (len(code) - 2) / 2
		return &Account{
			Code: code, Name: name, ParentCode: parent, Level: lvl,
			IsLeaf: true, RootType: rt, BalanceDir: dir,
			AuxTypes: aux, IsEnabled: true,
		}
	}
	return []*Account{
		mk("1002", "银行存款", "", RootAsset, DirDebit),
		mk("1122", "应收账款", "", RootAsset, DirDebit, AuxCustomer),
		mk("1601", "固定资产", "", RootAsset, DirDebit),
		mk("1602", "累计折旧", "", RootAsset, DirCredit), // ★ 备抵科目
		mk("2202", "应付账款", "", RootLiability, DirCredit, AuxSupplier),
		mk("220201", "应付账款—暂估", "2202", RootLiability, DirCredit, AuxSupplier),
		mk("2221", "应交税费", "", RootLiability, DirCredit),
		mk("222101", "应交税费—应交增值税", "2221", RootLiability, DirCredit),
		mk("22210102", "应交增值税—销项税额", "222101", RootLiability, DirCredit),
		mk("2241", "其他应付款", "", RootLiability, DirCredit),
		mk("224101", "其他应付款—股东", "2241", RootLiability, DirCredit, AuxShareholder),
		mk("4001", "生产成本", "", RootCost, DirDebit),
		mk("5602", "管理费用", "", RootExpense, DirDebit, AuxDept),
		mk("560207", "管理费用—差旅费", "5602", RootExpense, DirDebit, AuxDept),
		mk("5603", "财务费用", "", RootExpense, DirDebit),
		mk("560306", "财务费用—四舍五入", "5603", RootExpense, DirDebit),
	}
}

func mustTree(t *testing.T, accts []*Account) *Tree {
	t.Helper()
	tree, err := NewTree(accts)
	if err != nil {
		t.Fatalf("NewTree 失败: %v", err)
	}
	return tree
}

// ---------------------------------------------------------------------------
// 余额方向
// ---------------------------------------------------------------------------

func TestNaturalBalanceDir(t *testing.T) {
	cases := map[RootType]BalanceDir{
		RootAsset:     DirDebit,
		RootLiability: DirCredit,
		RootEquity:    DirCredit,
		RootCost:      DirDebit,
		RootIncome:    DirCredit,
		RootExpense:   DirDebit,
	}
	for rt, want := range cases {
		if got := NaturalBalanceDir(rt); got != want {
			t.Errorf("NaturalBalanceDir(%s) = %s，期望 %s", rt, got, want)
		}
	}
}

// 核心：备抵科目必须能得到正确的正数余额。
// 累计折旧（贷方科目）发生贷方 100 元时，余额应显示为 +100，而非 -100。
func TestContraAccountBalance(t *testing.T) {
	tree := mustTree(t, sampleAccounts())

	dep := tree.MustGet("1602") // 累计折旧，BalanceDir = credit
	if got := dep.Balance(0, 10000); got != 10000 {
		t.Errorf("累计折旧贷方 100 元余额 = %v，期望 10000 分", got)
	}

	bank := tree.MustGet("1002") // 银行存款，BalanceDir = debit
	if got := bank.Balance(10000, 0); got != 10000 {
		t.Errorf("银行存款借方 100 元余额 = %v，期望 10000 分", got)
	}
	if got := bank.Balance(0, 10000); got != -10000 {
		t.Errorf("银行存款贷方 100 元（透支）余额 = %v，期望 -10000 分", got)
	}

	ap := tree.MustGet("2202") // 应付账款，贷方科目
	if got := ap.Balance(0, 5000); got != 5000 {
		t.Errorf("应付账款贷方余额 = %v，期望 5000 分", got)
	}
}

// ---------------------------------------------------------------------------
// 树的构建与校验
// ---------------------------------------------------------------------------

func TestNewTreeBasic(t *testing.T) {
	tree := mustTree(t, sampleAccounts())

	if tree.Len() != 16 {
		t.Fatalf("科目数 = %d，期望 16", tree.Len())
	}
	// 有子科目的科目必须变成汇总科目（不可记账）
	if tree.MustGet("2221").IsLeaf {
		t.Error("2221 应交税费有下级，应为汇总科目")
	}
	if tree.MustGet("222101").IsLeaf {
		t.Error("222101 应交增值税有下级，应为汇总科目")
	}
	// 叶子科目保持可记账
	if !tree.MustGet("22210102").IsLeaf {
		t.Error("22210102 销项税额无下级，应为明细科目")
	}
	// 2202 有下级 220201
	if tree.MustGet("2202").IsLeaf {
		t.Error("2202 应付账款有下级，应为汇总科目")
	}
}

func TestTreeChildrenDescendantsAncestors(t *testing.T) {
	tree := mustTree(t, sampleAccounts())

	if got := len(tree.Children("2221")); got != 1 {
		t.Errorf("2221 的直接下级数 = %d，期望 1", got)
	}
	desc := tree.Descendants("2221")
	if len(desc) != 2 {
		t.Fatalf("2221 的后代数 = %d，期望 2（222101, 22210102）", len(desc))
	}
	if desc[0].Code != "222101" || desc[1].Code != "22210102" {
		t.Errorf("后代顺序错误: %s, %s", desc[0].Code, desc[1].Code)
	}
	anc := tree.Ancestors("22210102")
	if len(anc) != 2 || anc[0].Code != "222101" || anc[1].Code != "2221" {
		t.Errorf("祖先链错误: %v", codes(anc))
	}
	if got := tree.Roots(); len(got) != 10 {
		t.Errorf("一级科目数 = %d，期望 10", len(got))
	}
}

// ★ 报表取数的关键：公式写汇总科目时必须展开为叶子科目
func TestSubtreeLeaves(t *testing.T) {
	tree := mustTree(t, sampleAccounts())

	leaves := tree.SubtreeLeaves("2221")
	if len(leaves) != 1 || leaves[0].Code != "22210102" {
		t.Errorf("2221 的叶子科目应为 [22210102]，得到 %v", codes(leaves))
	}
	// 叶子自身返回自身
	leaves = tree.SubtreeLeaves("1002")
	if len(leaves) != 1 || leaves[0].Code != "1002" {
		t.Errorf("1002 是叶子，应返回自身，得到 %v", codes(leaves))
	}
	// 管理费用有 1 个下级
	leaves = tree.SubtreeLeaves("5602")
	if len(leaves) != 1 || leaves[0].Code != "560207" {
		t.Errorf("5602 的叶子应为 [560207]，得到 %v", codes(leaves))
	}
}

func TestByPrefix(t *testing.T) {
	tree := mustTree(t, sampleAccounts())
	got := tree.ByPrefix("2221")
	if len(got) != 3 {
		t.Errorf("前缀 2221 命中 %d 个，期望 3", len(got))
	}
}

// ---------------------------------------------------------------------------
// 各类非法输入必须被拒绝
// ---------------------------------------------------------------------------

func TestRejectsBadCode(t *testing.T) {
	cases := []struct {
		name  string
		accts []*Account
		want  error
	}{
		{
			"空编码",
			[]*Account{{Code: "", Name: "X", Level: 1, RootType: RootAsset, BalanceDir: DirDebit}},
			ErrEmptyCode,
		},
		{
			"非数字编码",
			[]*Account{{Code: "100A", Name: "X", Level: 1, RootType: RootAsset, BalanceDir: DirDebit}},
			ErrNonDigitCode,
		},
		{
			"长度非法",
			[]*Account{{Code: "100", Name: "X", Level: 1, RootType: RootAsset, BalanceDir: DirDebit}},
			ErrBadCodeLength,
		},
		{
			"编码重复",
			[]*Account{
				{Code: "1001", Name: "A", Level: 1, RootType: RootAsset, BalanceDir: DirDebit},
				{Code: "1001", Name: "B", Level: 1, RootType: RootAsset, BalanceDir: DirDebit},
			},
			ErrDuplicateCode,
		},
		{
			"父科目不存在",
			[]*Account{
				{Code: "999901", Name: "孤儿", ParentCode: "9999", Level: 2,
					RootType: RootAsset, BalanceDir: DirDebit},
			},
			ErrParentNotFound,
		},
		{
			"未知根类型",
			[]*Account{{Code: "1001", Name: "X", Level: 1, RootType: "bogus", BalanceDir: DirDebit}},
			ErrBadRootType,
		},
		{
			"未知余额方向",
			[]*Account{{Code: "1001", Name: "X", Level: 1, RootType: RootAsset, BalanceDir: "up"}},
			ErrBadBalanceDir,
		},
		{
			"未知辅助核算维度",
			[]*Account{{Code: "1001", Name: "X", Level: 1, RootType: RootAsset,
				BalanceDir: DirDebit, AuxTypes: []AuxType{"bogus"}}},
			ErrBadAuxType,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewTree(c.accts)
			if !errors.Is(err, c.want) {
				t.Errorf("得到 %v，期望包含 %v", err, c.want)
			}
		})
	}
}

// ★ 这条对应用户下载的那份 CSV 里的真实缺陷：
//
//	「员工」编码 1231.01，父科目却写「其他应收款」(1221)
//	——编码与父科目不匹配。
//	而 1231 本身已被「坏账准备」占用，两个科目会互相踩。
func TestRejectsCodeParentMismatch(t *testing.T) {
	accts := []*Account{
		{Code: "1221", Name: "其他应收款", Level: 1, RootType: RootAsset, BalanceDir: DirDebit},
		{Code: "1231", Name: "坏账准备", Level: 1, RootType: RootAsset, BalanceDir: DirCredit},
		// 员工挂在 1221 下，但编码是 1231.01 —— 必须被拒
		{Code: "123101", Name: "员工", ParentCode: "1221", Level: 2,
			RootType: RootAsset, BalanceDir: DirDebit},
	}
	_, err := NewTree(accts)
	if !errors.Is(err, ErrCodeParentMatch) {
		t.Fatalf("得到 %v，期望 %v", err, ErrCodeParentMatch)
	}
}

// 层级与编码长度必须一致：6 位编码就是 2 级。
func TestRejectsLevelMismatch(t *testing.T) {
	accts := []*Account{
		{Code: "2221", Name: "应交税费", Level: 1, RootType: RootLiability, BalanceDir: DirCredit},
		// 6 位编码却是 3 级
		{Code: "222101", Name: "应交增值税", ParentCode: "2221", Level: 3,
			RootType: RootLiability, BalanceDir: DirCredit},
	}
	_, err := NewTree(accts)
	if !errors.Is(err, ErrBadLevel) {
		t.Fatalf("得到 %v，期望 %v", err, ErrBadLevel)
	}
}

// 一级科目不应带父科目。
func TestRejectsRootWithParent(t *testing.T) {
	accts := []*Account{
		{Code: "1001", Name: "库存现金", Level: 1, RootType: RootAsset, BalanceDir: DirDebit},
		{Code: "1002", Name: "银行存款", ParentCode: "1001", Level: 1,
			RootType: RootAsset, BalanceDir: DirDebit},
	}
	if _, err := NewTree(accts); err == nil {
		t.Fatal("一级科目带父科目应当报错")
	}
}

// ---------------------------------------------------------------------------
// 记账校验
// ---------------------------------------------------------------------------

func TestCheckPostable(t *testing.T) {
	tree := mustTree(t, sampleAccounts())

	if err := tree.CheckPostable("560207"); err != nil {
		t.Errorf("明细科目应当可记账，得到 %v", err)
	}
	// 汇总科目不可记账
	if err := tree.CheckPostable("2221"); err == nil {
		t.Error("汇总科目 2221 不应允许记账")
	}
	if err := tree.CheckPostable("222101"); err == nil {
		t.Error("汇总科目 222101 不应允许记账")
	}
	// 不存在的科目
	if err := tree.CheckPostable("9999"); err == nil {
		t.Error("不存在的科目应当报错")
	}
	// 停用科目
	accts := sampleAccounts()
	for _, a := range accts {
		if a.Code == "1002" {
			a.IsEnabled = false
		}
	}
	tree2 := mustTree(t, accts)
	if err := tree2.CheckPostable("1002"); err == nil {
		t.Error("已停用科目不应允许记账")
	}
}

func TestEnabledLeaves(t *testing.T) {
	tree := mustTree(t, sampleAccounts())
	leaves := tree.EnabledLeaves()
	for _, a := range leaves {
		if !a.IsLeaf {
			t.Errorf("%s 出现在可记账列表里但不是明细科目", a.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// 展示辅助
// ---------------------------------------------------------------------------

func TestDisplayCode(t *testing.T) {
	cases := map[string]string{
		"1001":     "1001",
		"222101":   "2221.01",
		"22210102": "2221.01.02",
	}
	for in, want := range cases {
		a := &Account{Code: in}
		if got := a.DisplayCode(); got != want {
			t.Errorf("DisplayCode(%s) = %s，期望 %s", in, got, want)
		}
	}
}

func TestFullName(t *testing.T) {
	tree := mustTree(t, sampleAccounts())
	all := make(map[string]*Account)
	for _, a := range tree.All() {
		all[a.Code] = a
	}
	got := tree.MustGet("22210102").FullName(all)
	want := "应交税费/应交税费—应交增值税/应交增值税—销项税额"
	if got != want {
		t.Errorf("FullName = %q，期望 %q", got, want)
	}
}

func TestRequiresAux(t *testing.T) {
	tree := mustTree(t, sampleAccounts())
	if !tree.MustGet("224101").RequiresAux() {
		t.Error("其他应付款—股东应要求辅助核算")
	}
	if !tree.MustGet("224101").SupportsAux(AuxShareholder) {
		t.Error("其他应付款—股东应支持股东维度")
	}
	if tree.MustGet("224101").SupportsAux(AuxCustomer) {
		t.Error("其他应付款—股东不应支持客户维度")
	}
	if tree.MustGet("1002").RequiresAux() {
		t.Error("银行存款默认不要求辅助核算")
	}
}

func TestBalanceWithMoney(t *testing.T) {
	tree := mustTree(t, sampleAccounts())
	// 应收账款借 1000 贷 300 → 余额 700
	got := tree.MustGet("1122").Balance(money.Money(100000), money.Money(30000))
	if got != 70000 {
		t.Errorf("应收账款余额 = %v，期望 70000 分", got)
	}
}

func codes(as []*Account) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Code
	}
	return out
}
