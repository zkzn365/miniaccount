package aging

import (
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

// testTree 是一棵小科目树：1122 应收（借）、2202 应付（贷）。
func testTree(t *testing.T) *account.Tree {
	t.Helper()
	tree, err := account.NewTree([]*account.Account{
		{Code: "1122", Name: "应收账款", RootType: account.RootAsset,
			BalanceDir: account.DirDebit, Level: 1, IsLeaf: true, IsEnabled: true},
		{Code: "2202", Name: "应付账款", RootType: account.RootLiability,
			BalanceDir: account.DirCredit, Level: 1, IsLeaf: true, IsEnabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// ★ 账龄不是「发生额有多大」，而是「还没收回来的钱有多久了」。
// 客户三个月前欠的 10 万上个月已经付了，那笔就不该还留在「90 天以上」。
func TestSettlementOffsetsOldestFirst(t *testing.T) {
	in := Input{
		AsOf:         d("2025-06-30"),
		Accounts:     testTree(t),
		ContactNames: map[int64]string{1: "客户甲"},
		Entries: []Entry{
			// 1 月欠 10 万
			{ContactID: 1, AccountCode: "1122", Date: d("2025-01-10"),
				Debit: y(100000), Summary: "1 月货款", VoucherNo: "记-0001"},
			// 5 月又欠 5 万
			{ContactID: 1, AccountCode: "1122", Date: d("2025-05-10"),
				Debit: y(50000), Summary: "5 月货款", VoucherNo: "记-0002"},
			// 6 月付了 10 万 —— 应先冲掉 1 月那笔
			{ContactID: 1, AccountCode: "1122", Date: d("2025-06-10"),
				Credit: y(100000), Summary: "收回货款", VoucherNo: "记-0003"},
		},
	}
	rep, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("行数 = %d", len(rep.Rows))
	}
	row := rep.Rows[0]
	if row.Balance != y(50000) {
		t.Errorf("未核销余额 = %s，期望 50000.00", row.Balance)
	}
	// ★ 只剩 5 月那笔未结清，账龄约 51 天，落在 31—60 天
	if len(row.Items) != 1 {
		t.Fatalf("未结清明细 = %d 条，期望 1（1 月那笔已被冲掉）", len(row.Items))
	}
	it := row.Items[0]
	if it.Amount != y(50000) {
		t.Errorf("未结清金额 = %s，期望 50000.00", it.Amount)
	}
	if it.Days != 51 {
		t.Errorf("账龄天数 = %d，期望 51", it.Days)
	}
	if row.Buckets[1].Amount != y(50000) {
		t.Errorf("31—60 天桶 = %s，期望 50000.00", row.Buckets[1].Amount)
	}
	// 90 天以上应该一分钱都没有 —— 这正是核销的意义
	var over90 money.Money
	for _, b := range rep.Buckets {
		if b.MinDays > 90 {
			over90 = over90.Add(b.Amount)
		}
	}
	if !over90.IsZero() {
		t.Errorf("90 天以上 = %s，期望 0（老账已被收款冲掉）", over90)
	}
}

// 完全没收回来的账应落进最老的桶
func TestUnpaidFallsIntoOldestBucket(t *testing.T) {
	rep, err := Build(Input{
		AsOf: d("2025-12-31"), Accounts: testTree(t),
		ContactNames: map[int64]string{1: "客户甲"},
		Entries: []Entry{
			{ContactID: 1, AccountCode: "1122", Date: d("2024-01-15"), Debit: y(80000)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := rep.Rows[0]
	if row.Buckets[5].Amount != y(80000) {
		t.Errorf("1 年以上桶 = %s，期望 80000.00", row.Buckets[5].Amount)
	}
	if row.Items[0].Days != 716 {
		t.Errorf("天数 = %d，期望 716", row.Items[0].Days)
	}
}

// ★ 应付账款的账龄方向要反过来：贷方是「欠别人的」，借方才是冲抵
func TestPayableAgingDirection(t *testing.T) {
	rep, err := Build(Input{
		AsOf: d("2025-06-30"), Accounts: testTree(t),
		ContactNames: map[int64]string{2: "供应商乙"},
		Entries: []Entry{
			{ContactID: 2, AccountCode: "2202", Date: d("2025-02-10"),
				Credit: y(30000), Summary: "2 月采购"},
			{ContactID: 2, AccountCode: "2202", Date: d("2025-06-20"),
				Debit: y(30000), Summary: "付货款"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := rep.Rows[0]
	if len(row.Items) != 0 {
		t.Errorf("已付清的应付不该有未结清项，实际 %+v", row.Items)
	}
	// 净额为 0
	if !row.Balance.IsZero() {
		t.Errorf("余额 = %s，期望 0", row.Balance)
	}
}

// 只付了一部分的应付：剩余部分按贷方发生的日期算账龄
func TestPartialPayable(t *testing.T) {
	rep, err := Build(Input{
		AsOf: d("2025-06-30"), Accounts: testTree(t),
		ContactNames: map[int64]string{2: "供应商乙"},
		Entries: []Entry{
			{ContactID: 2, AccountCode: "2202", Date: d("2025-01-05"), Credit: y(50000)},
			{ContactID: 2, AccountCode: "2202", Date: d("2025-03-05"), Credit: y(20000)},
			{ContactID: 2, AccountCode: "2202", Date: d("2025-06-01"), Debit: y(55000)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := rep.Rows[0]
	// 先冲最早的 5 万，再冲 3 月那笔 5 千，剩 1.5 万
	if len(row.Items) != 1 || row.Items[0].Amount != y(15000) {
		t.Fatalf("未结清明细 = %+v，期望只剩 15000.00", row.Items)
	}
	if row.Items[0].Days != 117 {
		t.Errorf("天数 = %d，期望 117（3 月 5 日到 6 月 30 日）", row.Items[0].Days)
	}
	if row.Buckets[3].Amount != y(15000) {
		t.Errorf("91—180 天桶 = %s", row.Buckets[3].Amount)
	}
}

// 不同往来单位不能互相冲抵
func TestContactsAreIndependent(t *testing.T) {
	rep, err := Build(Input{
		AsOf: d("2025-06-30"), Accounts: testTree(t),
		ContactNames: map[int64]string{1: "客户甲", 2: "客户乙"},
		Entries: []Entry{
			{ContactID: 1, AccountCode: "1122", Date: d("2025-01-10"), Debit: y(100000)},
			{ContactID: 2, AccountCode: "1122", Date: d("2025-06-01"), Credit: y(100000)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 2 {
		t.Fatalf("行数 = %d，期望 2（两个客户各自一行）", len(rep.Rows))
	}
	// 甲的欠款必须还在 —— 乙的收款不能冲掉甲的应收
	var jia *ContactAging
	for i := range rep.Rows {
		if rep.Rows[i].ContactID == 1 {
			jia = &rep.Rows[i]
		}
	}
	if jia == nil || len(jia.Items) != 1 || jia.Items[0].Amount != y(100000) {
		t.Errorf("客户甲的应收不该被客户乙的收款冲掉: %+v", jia)
	}
	// 乙多收的钱体现为贷方余额
	if rep.CreditTotal != y(100000) {
		t.Errorf("贷方合计 = %s，期望 100000.00", rep.CreditTotal)
	}
	if rep.DebitTotal != y(100000) {
		t.Errorf("借方合计 = %s，期望 100000.00", rep.DebitTotal)
	}
}

// 截止日之后的业务不进账龄表
func TestFutureEntriesExcluded(t *testing.T) {
	rep, err := Build(Input{
		AsOf: d("2025-06-30"), Accounts: testTree(t),
		ContactNames: map[int64]string{1: "客户甲"},
		Entries: []Entry{
			{ContactID: 1, AccountCode: "1122", Date: d("2025-05-01"), Debit: y(10000)},
			{ContactID: 1, AccountCode: "1122", Date: d("2025-08-01"), Debit: y(99999)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rows[0].Debits != y(10000) {
		t.Errorf("借方合计 = %s，期望只算截止日之前的 10000.00", rep.Rows[0].Debits)
	}
}

// 边界：整 30 天、整 90 天、整 365 天各落哪个桶
func TestBucketBoundaries(t *testing.T) {
	cases := []struct {
		days      int
		wantIndex int
	}{
		{0, 0}, {30, 0}, {31, 1}, {60, 1}, {61, 2}, {90, 2},
		{91, 3}, {180, 3}, {181, 4}, {365, 4}, {366, 5}, {1000, 5},
	}
	buckets := DefaultBuckets()
	for _, c := range cases {
		if got := bucketOf(c.days, buckets); got != c.wantIndex {
			t.Errorf("%d 天应落在第 %d 桶（%s），实际第 %d 桶",
				c.days, c.wantIndex, buckets[c.wantIndex].Label, got)
		}
	}
}

func TestBadDate(t *testing.T) {
	if _, err := Build(Input{}); err == nil {
		t.Error("无效截止日期应报错")
	}
}

func TestEmptyInput(t *testing.T) {
	rep, err := Build(Input{AsOf: d("2025-06-30"), Accounts: testTree(t)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 0 {
		t.Errorf("没有发生额时不该有行，实际 %d", len(rep.Rows))
	}
	// 桶结构仍然要在 —— 界面靠它画表头
	if len(rep.Buckets) != 6 {
		t.Errorf("区间数 = %d，期望 6", len(rep.Buckets))
	}
	if !rep.Total.IsZero() {
		t.Errorf("合计 = %s，期望 0", rep.Total)
	}
}

func TestSummary(t *testing.T) {
	rep, err := Build(Input{
		AsOf: d("2025-06-30"), Accounts: testTree(t),
		ContactNames: map[int64]string{1: "客户甲"},
		Entries: []Entry{
			{ContactID: 1, AccountCode: "1122", Date: d("2024-01-01"), Debit: y(1000)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := rep.Summary()
	for _, want := range []string{"2025-06-30", "90 天以上"} {
		if !contains(s, want) {
			t.Errorf("概览 %q 应含 %q", s, want)
		}
	}
}

func TestDaysBetween(t *testing.T) {
	cases := []struct {
		from, to string
		want     int
	}{
		{"2025-01-01", "2025-01-31", 30},
		{"2025-01-01", "2025-12-31", 364},
		{"2024-01-01", "2025-01-01", 366}, // 跨闰年
		{"2025-06-30", "2025-06-30", 0},
		{"2025-06-30", "2025-01-01", -180},
	}
	for _, c := range cases {
		if got := DaysBetween(d(c.from), d(c.to)); got != c.want {
			t.Errorf("DaysBetween(%s,%s) = %d，期望 %d", c.from, c.to, got, c.want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
