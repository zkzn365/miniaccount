package statement

import (
	"strings"
	"testing"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

func y(v int64) money.Money { return money.Money(v) * money.Yuan }

func d(s string) calendar.Date {
	dt, err := calendar.Parse(s)
	if err != nil {
		panic(err)
	}
	return dt
}

func testAccounts(t *testing.T) *account.Tree {
	t.Helper()
	tree, err := account.NewTree([]*account.Account{
		{Code: "1122", Name: "应收账款", RootType: account.RootAsset,
			BalanceDir: account.DirDebit, Level: 1, IsLeaf: true, IsEnabled: true},
		{Code: "2203", Name: "预收账款", RootType: account.RootLiability,
			BalanceDir: account.DirCredit, Level: 1, IsLeaf: true, IsEnabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func baseInput() Input {
	return Input{
		CompanyName: "杭州云帆软件有限公司",
		ContactID:   1, ContactName: "杭州某某科技有限公司", ContactKind: "customer",
		ContactTaxNo: "91330100MA2XXXXXXX",
		From:         d("2025-03-01"), To: d("2025-03-31"),
		Opening:      y(50000),
		AccountNames: map[string]string{"1122": "应收账款"},
		Accounts:     nil,
		Entries: []Entry{
			{AccountCode: "1122", Date: d("2025-03-10"), Debit: y(80000),
				Summary: "销售软件服务", VoucherNo: "记-2025-03-0001"},
			{AccountCode: "1122", Date: d("2025-03-20"), Credit: y(60000),
				Summary: "收回货款", VoucherNo: "记-2025-03-0005"},
		},
	}
}

func TestBuildStatement(t *testing.T) {
	st, err := Build(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if st.ContactKindLabel != "客户" {
		t.Errorf("往来类型 = %q", st.ContactKindLabel)
	}
	if st.AccountCode != "1122" || st.AccountName != "应收账款" {
		t.Errorf("科目 = %s %s", st.AccountCode, st.AccountName)
	}
	if st.Mixed {
		t.Error("单科目时不该标记为多科目")
	}
	if st.Opening != y(50000) || st.OpeningDir != "借" {
		t.Errorf("期初 = %s %s", st.OpeningDir, st.Opening)
	}
	if st.TotalDebit != y(80000) || st.TotalCredit != y(60000) {
		t.Errorf("合计 = 借 %s 贷 %s", st.TotalDebit, st.TotalCredit)
	}
	// 期初 5 万 + 借 8 万 − 贷 6 万 = 7 万
	if st.Closing != y(70000) || st.ClosingDir != "借" {
		t.Errorf("期末 = %s %s，期望 借 70000.00", st.ClosingDir, st.Closing)
	}
	if len(st.Lines) != 2 {
		t.Fatalf("行数 = %d", len(st.Lines))
	}
	// 逐行累计
	if st.Lines[0].Balance != y(130000) {
		t.Errorf("第 1 行余额 = %s，期望 130000.00", st.Lines[0].Balance)
	}
	if st.Lines[1].Balance != y(70000) {
		t.Errorf("第 2 行余额 = %s，期望 70000.00", st.Lines[1].Balance)
	}
	if !st.Balanced() {
		t.Error("期初 + 借 − 贷 应等于期末")
	}
	if errs := st.Check(); len(errs) > 0 {
		t.Errorf("不变式应全部成立: %v", errs)
	}
	// 大写金额
	if st.ClosingUpper == "" || !strings.Contains(st.ClosingUpper, "柒万") {
		t.Errorf("大写 = %q，期望含「柒万」", st.ClosingUpper)
	}
}

// 同一天多笔要按凭证号稳定排序 —— 否则每次打印行序不同，双方对不上
func TestStableOrdering(t *testing.T) {
	in := baseInput()
	in.Entries = []Entry{
		{AccountCode: "1122", Date: d("2025-03-10"), Debit: y(100),
			Summary: "第二张", VoucherNo: "记-2025-03-0002"},
		{AccountCode: "1122", Date: d("2025-03-10"), Debit: y(200),
			Summary: "第一张", VoucherNo: "记-2025-03-0001"},
		{AccountCode: "1122", Date: d("2025-03-05"), Debit: y(300),
			Summary: "更早", VoucherNo: "记-2025-03-0009"},
	}
	st, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"记-2025-03-0009", "记-2025-03-0001", "记-2025-03-0002"}
	for i, w := range want {
		if st.Lines[i].VoucherNo != w {
			t.Errorf("第 %d 行 = %s，期望 %s（同日按凭证号排）",
				i+1, st.Lines[i].VoucherNo, w)
		}
	}
}

// 期初为贷方（客户预付款）时余额方向要正确
func TestCreditOpeningBalance(t *testing.T) {
	in := baseInput()
	in.Opening = y(-30000) // 客户先付了 3 万
	in.Entries = []Entry{
		{AccountCode: "1122", Date: d("2025-03-10"), Debit: y(50000),
			Summary: "销售", VoucherNo: "记-0001"},
	}
	st, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if st.OpeningDir != "贷" {
		t.Errorf("期初方向 = %s，期望 贷", st.OpeningDir)
	}
	// −3 万 + 5 万 = 2 万（借）
	if st.Closing != y(20000) || st.ClosingDir != "借" {
		t.Errorf("期末 = %s %s，期望 借 20000.00", st.ClosingDir, st.Closing)
	}
	if !st.Balanced() {
		t.Error("应平衡")
	}
}

// ★ 借贷相抵为零时要显示「平」，而不是空
func TestZeroBalance(t *testing.T) {
	in := baseInput()
	in.Opening = 0
	in.Entries = []Entry{
		{AccountCode: "1122", Date: d("2025-03-10"), Debit: y(50000), VoucherNo: "记-0001"},
		{AccountCode: "1122", Date: d("2025-03-20"), Credit: y(50000), VoucherNo: "记-0002"},
	}
	st, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if st.ClosingDir != "平" {
		t.Errorf("方向 = %s，期望 平", st.ClosingDir)
	}
	if !strings.Contains(st.Summary(), "已结清") {
		t.Errorf("概览 = %q，应说明已结清", st.Summary())
	}
}

// 涉及多个科目时要标注出来
func TestMixedAccounts(t *testing.T) {
	in := baseInput()
	in.Entries = []Entry{
		{AccountCode: "1122", Date: d("2025-03-10"), Debit: y(50000), VoucherNo: "记-0001"},
		{AccountCode: "2203", Date: d("2025-03-15"), Credit: y(20000), VoucherNo: "记-0002"},
	}
	in.AccountNames["2203"] = "预收账款"
	st, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Mixed {
		t.Error("两个科目时应标记为多科目")
	}
	if st.AccountCode != "" {
		t.Errorf("多科目时不该在抬头写单个科目，实际 %q", st.AccountCode)
	}
}

func TestBadRange(t *testing.T) {
	in := baseInput()
	in.To = d("2025-02-01")
	if _, err := Build(in); err == nil {
		t.Error("结束早于开始应报错")
	}
	in = baseInput()
	in.From = calendar.Date{}
	if _, err := Build(in); err == nil {
		t.Error("无效起始日期应报错")
	}
}

// Check 要能抓出人为构造的错误
func TestCheckDetectsErrors(t *testing.T) {
	st, err := Build(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	// 篡改一行的余额
	st.Lines[0].Balance = y(999999)
	if errs := st.Check(); len(errs) == 0 {
		t.Error("被篡改的行余额应被检出")
	}

	st2, _ := Build(baseInput())
	st2.Closing = y(1)
	if errs := st2.Check(); len(errs) == 0 {
		t.Error("表尾与累计不符应被检出")
	}

	st3, _ := Build(baseInput())
	st3.TotalDebit = y(1)
	if errs := st3.Check(); len(errs) == 0 {
		t.Error("发生额合计不符应被检出")
	}
}

// 空期间也要能出一张「本期无往来」的对账单 ——
// 客户可能就是要一张「这个月我们没业务」的确认
func TestEmptyPeriod(t *testing.T) {
	in := baseInput()
	in.Entries = nil
	st, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Lines) != 0 {
		t.Errorf("行数 = %d", len(st.Lines))
	}
	if st.Closing != st.Opening {
		t.Errorf("无发生额时期末应等于期初，实际 %s vs %s", st.Closing, st.Opening)
	}
	if !st.Balanced() {
		t.Error("应平衡")
	}
}

func TestFormatText(t *testing.T) {
	st, _ := Build(baseInput())
	out := st.FormatText()
	for _, want := range []string{
		"往来对账单", "杭州云帆软件有限公司", "杭州某某科技有限公司",
		"对账期间：2025-03-01 至 2025-03-31",
		"期初余额", "期末余额", "本期合计",
		"记-2025-03-0001", "销售软件服务",
		"盖章", "日期：", // 落款
	} {
		if !strings.Contains(out, want) {
			t.Errorf("对账单文本应含 %q\n---\n%s", want, out)
		}
	}
	// 大写金额要出现
	if !strings.Contains(out, st.ClosingUpper) {
		t.Errorf("应含大写金额 %q", st.ClosingUpper)
	}
}

func TestContactKindLabels(t *testing.T) {
	for kind, want := range map[string]string{
		"customer": "客户", "supplier": "供应商", "employee": "员工",
		"shareholder": "股东", "both": "客户/供应商", "未知": "往来单位",
	} {
		if got := contactKindLabel(kind); got != want {
			t.Errorf("%s → %q，期望 %q", kind, got, want)
		}
	}
}
