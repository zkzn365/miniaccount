package columnar

import (
	"strings"
	"testing"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

func y(n int64) money.Money { return money.Money(n) * money.Yuan }

func d(s string) calendar.Date { return calendar.MustParse(s) }

// mgmtColumns 是管理费用最常见的 4 个明细栏目（借方多栏）。
func mgmtColumns() []Column {
	return []Column{
		{Key: "560206", Label: "办公费", Side: SideDebit, AccountCode: "560206"},
		{Key: "560207", Label: "差旅费", Side: SideDebit, AccountCode: "560207"},
		{Key: "560201", Label: "工资", Side: SideDebit, AccountCode: "560201"},
	}
}

// ★ 多栏式明细账的核心价值：一眼看出「哪一项花得多」。
func TestColumnsSplitBySubAccount(t *testing.T) {
	r, err := Build(Input{
		AccountCode: "5602", AccountName: "管理费用",
		From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: mgmtColumns(),
		Movements: []Movement{
			{Date: d("2025-03-05"), VoucherNo: "记-0003", Summary: "办公用品",
				AccountCode: "560206", Seq: 1, Debit: y(300)},
			{Date: d("2025-03-12"), VoucherNo: "记-0008", Summary: "出差机票",
				AccountCode: "560207", Seq: 1, Debit: y(2400)},
			{Date: d("2025-03-20"), VoucherNo: "记-0015", Summary: "订书机",
				AccountCode: "560206", Seq: 1, Debit: y(120)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if want := y(420); mustCol(t, r, "560206") != want {
		t.Errorf("办公费 = %s，期望 %s", mustCol(t, r, "560206"), want)
	}
	if want := y(2400); mustCol(t, r, "560207") != want {
		t.Errorf("差旅费 = %s，期望 %s", mustCol(t, r, "560207"), want)
	}
	// 没有发生的栏目必须是零，而不是消失 —— 空栏目本身也是信息
	if got := mustCol(t, r, "560201"); got != 0 {
		t.Errorf("工资栏 = %s，期望 0", got)
	}
	if len(r.Columns) != 3 {
		t.Errorf("栏目数 = %d，期望 3（没发生的栏目也要留着）", len(r.Columns))
	}
	if r.DebitTotal != y(2820) {
		t.Errorf("借方合计 = %s，期望 2820", r.DebitTotal)
	}
	if r.Closing != y(2820) {
		t.Errorf("期末余额 = %s，期望 2820", r.Closing)
	}
	if errs := r.Check(); len(errs) != 0 {
		t.Errorf("不变式应成立: %v", errs)
	}
}

// ★ 红字冲销必须落在**本科目那一栏**里成为负数，
// 而不是被塞进一个笼统的「贷方」栏。
//
// 这是本包最刻意的设计决定：冲销时最想知道的是「冲掉的是哪一项」。
func TestReversalGoesNegativeInItsOwnColumn(t *testing.T) {
	r, err := Build(Input{
		AccountCode: "5602", AccountName: "管理费用",
		From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: mgmtColumns(),
		Movements: []Movement{
			// 3-05 误记 300 元办公费
			{Date: d("2025-03-05"), VoucherNo: "记-0003", Summary: "办公用品",
				AccountCode: "560206", Seq: 1, Debit: y(300)},
			// 3-20 红字凭证冲销（借贷互换：贷 560206），是一张有编号的真实凭证
			{Date: d("2025-03-20"), VoucherNo: "记-0009", Summary: "冲销记-0003",
				AccountCode: "560206", Seq: 2, Credit: y(300)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := mustCol(t, r, "560206"); got != 0 {
		t.Errorf("办公费净额 = %s，期望 0（300 记入又 300 冲回）", got)
	}
	// 冲销行必须还在，且金额为负、栏目为办公费
	var found bool
	for _, row := range r.Rows {
		if row.VoucherNo == "记-0009" {
			found = true
			if r.Columns[row.ColumnIndex].Key != "560206" {
				t.Errorf("冲销行落到了「%s」栏，期望「办公费」栏",
					r.Columns[row.ColumnIndex].Label)
			}
			if row.Amount != y(-300) {
				t.Errorf("冲销行金额 = %s，期望 -300（红字）", row.Amount)
			}
		}
	}
	if !found {
		t.Error("冲销行不该被吞掉 —— 逐笔登记是多栏式明细账的基本要求")
	}
	// 借贷发生额是**总额**：300 借、300 贷，不能净成 0
	if r.DebitTotal != y(300) || r.CreditTotal != y(300) {
		t.Errorf("发生额合计 = 借 %s / 贷 %s，期望 300 / 300（总额不净额）",
			r.DebitTotal, r.CreditTotal)
	}
	if r.Closing != 0 {
		t.Errorf("期末余额 = %s，期望 0", r.Closing)
	}
	if errs := r.Check(); len(errs) != 0 {
		t.Errorf("不变式应成立: %v", errs)
	}
}

// ★ 月末结转损益：每一栏都归零，账面完整呈现
// 「本月累计发生 → 全部结转」的过程。
func TestClosingSettlementZeroesEveryColumn(t *testing.T) {
	r, err := Build(Input{
		AccountCode: "5602", AccountName: "管理费用",
		From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: mgmtColumns(),
		Movements: []Movement{
			{Date: d("2025-03-05"), VoucherNo: "记-0003", AccountCode: "560206",
				Seq: 1, Debit: y(300)},
			{Date: d("2025-03-12"), VoucherNo: "记-0008", AccountCode: "560207",
				Seq: 1, Debit: y(2400)},
			{Date: d("2025-03-31"), VoucherNo: "记-0031", Summary: "结转本年利润",
				AccountCode: "560206", Seq: 1, Credit: y(300)},
			{Date: d("2025-03-31"), VoucherNo: "记-0031", Summary: "结转本年利润",
				AccountCode: "560207", Seq: 2, Credit: y(2400)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"560206", "560207", "560201"} {
		if got := mustCol(t, r, key); got != 0 {
			t.Errorf("结转后 %s 栏 = %s，期望 0", key, got)
		}
	}
	if r.Closing != 0 || r.ClosingDir != "平" {
		t.Errorf("结转后期末 = %s（%s），期望 0（平）", r.Closing, r.ClosingDir)
	}
	if r.DebitTotal != y(2700) || r.CreditTotal != y(2700) {
		t.Errorf("发生额 = 借 %s / 贷 %s，期望各 2700", r.DebitTotal, r.CreditTotal)
	}
}

// ★ 增值税专栏：借方 6 栏 + 贷方 4 栏，两侧同时展开。
//
// 栏目直接来自科目表里「应交税费—应交增值税」的 10 个子科目，
// 各自的余额方向决定它落在哪一侧 —— 不需要任何硬编码的专栏表。
func TestVATColumnsBothSides(t *testing.T) {
	cols := []Column{
		{Key: "22210101", Label: "进项税额", Side: SideDebit, AccountCode: "22210101"},
		{Key: "22210103", Label: "已交税金", Side: SideDebit, AccountCode: "22210103"},
		{Key: "22210102", Label: "销项税额", Side: SideCredit, AccountCode: "22210102"},
		{Key: "22210108", Label: "进项税额转出", Side: SideCredit, AccountCode: "22210108"},
	}
	r, err := Build(Input{
		AccountCode: "222101", AccountName: "应交税费—应交增值税",
		From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: cols,
		Movements: []Movement{
			// 采购：借 进项税额 13,000
			{Date: d("2025-03-05"), VoucherNo: "记-0001", Summary: "采购",
				AccountCode: "22210101", Seq: 1, Debit: y(13000)},
			// 销售：贷 销项税额 26,000
			{Date: d("2025-03-10"), VoucherNo: "记-0002", Summary: "销售",
				AccountCode: "22210102", Seq: 2, Credit: y(26000)},
			// 非正常损失转出进项 2,000：
			// 分录是「借 待处理财产损溢 / 贷 应交税费—应交增值税（进项税额转出）」，
			// 所以这笔在 22210108 上是**贷方** —— 它是贷方专栏
			{Date: d("2025-03-18"), VoucherNo: "记-0003", Summary: "进项转出",
				AccountCode: "22210108", Seq: 2, Credit: y(2000)},
			// 缴纳上月税款
			{Date: d("2025-03-25"), VoucherNo: "记-0004", Summary: "缴纳增值税",
				AccountCode: "22210103", Seq: 1, Debit: y(5000)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 借方栏：13,000 + 5,000 = 18,000
	if got := r.SideTotal(SideDebit); got != y(18000) {
		t.Errorf("借方栏目合计 = %s，期望 18000", got)
	}
	// 贷方栏：26,000 + 2,000 = 28,000
	if got := r.SideTotal(SideCredit); got != y(28000) {
		t.Errorf("贷方栏目合计 = %s，期望 28000", got)
	}
	// 应交未交 = 28,000 − 18,000 = 10,000（贷方余额）
	if r.Closing != y(-10000) {
		t.Errorf("期末余额 = %s，期望 -10000（贷方，即应交未交 10,000）", r.Closing)
	}
	if r.ClosingDir != "贷" {
		t.Errorf("期末方向 = %q，期望 贷", r.ClosingDir)
	}
	// 进项税额转出虽然分录在借方、但栏目是贷方栏 —— 它增加应交数
	if got := mustCol(t, r, "22210108"); got != y(2000) {
		t.Errorf("进项税额转出栏 = %s，期望 +2000（贷方栏目，增加应交数）", got)
	}
	if errs := r.Check(); len(errs) != 0 {
		t.Errorf("不变式应成立: %v", errs)
	}
}

// 期初余额必须逐行累计进去，不能只算本期。
func TestOpeningBalanceCarriesThrough(t *testing.T) {
	r, err := Build(Input{
		AccountCode: "5602", AccountName: "管理费用",
		From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: mgmtColumns(),
		Opening: y(1000),
		Movements: []Movement{
			{Date: d("2025-03-05"), VoucherNo: "记-0003", AccountCode: "560206",
				Seq: 1, Debit: y(300)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.OpeningDir != "借" {
		t.Errorf("期初方向 = %q，期望 借", r.OpeningDir)
	}
	if r.Rows[0].Balance != y(1300) {
		t.Errorf("第一行余额 = %s，期望 1300（1000 期初 + 300）", r.Rows[0].Balance)
	}
	if r.Closing != y(1300) {
		t.Errorf("期末余额 = %s，期望 1300", r.Closing)
	}
}

// 没有落到任何栏目上的发生额，进「其他」兜底栏，并且要提示出来。
//
// 这是异常信号：说明有人直接记在了上级科目上、或者栏目配漏了。
// 静默丢掉才是最坏的处理 —— 那张表看起来一切正常，但金额对不上。
func TestOrphanMovementGoesToOtherColumnWithNote(t *testing.T) {
	r, err := Build(Input{
		AccountCode: "5602", AccountName: "管理费用",
		From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: mgmtColumns(),
		Movements: []Movement{
			{Date: d("2025-03-05"), VoucherNo: "记-0003", AccountCode: "560206",
				Seq: 1, Debit: y(300)},
			// 560217「管理费用—其他」不在栏目列表里
			{Date: d("2025-03-06"), VoucherNo: "记-0004", AccountCode: "560217",
				Seq: 1, Debit: y(77)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Columns) != 4 {
		t.Fatalf("栏目数 = %d，期望 4（3 个正常栏 + 1 个兜底栏）", len(r.Columns))
	}
	last := r.Columns[len(r.Columns)-1]
	if !last.Other || last.Label != "其他" {
		t.Errorf("最后一栏应是兜底栏，实际 %+v", last)
	}
	// 兜底栏必须在正常栏目之后，不能插在中间打断阅读
	for i, c := range r.Columns[:len(r.Columns)-1] {
		if c.Other {
			t.Errorf("第 %d 栏是兜底栏，应排在末尾", i)
		}
	}
	if got := r.ColumnTotals[len(r.ColumnTotals)-1]; got != y(77) {
		t.Errorf("兜底栏合计 = %s，期望 77", got)
	}
	// 总额仍然要含它，否则这张表就和总账对不上了
	if r.DebitTotal != y(377) {
		t.Errorf("借方合计 = %s，期望 377（兜底栏也要计入总额）", r.DebitTotal)
	}
	var noted bool
	for _, n := range r.Notes {
		if strings.Contains(n, "其他") && strings.Contains(n, "77.00") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("应提示有发生额未归入栏目，实际 %v", r.Notes)
	}
	if errs := r.Check(); len(errs) != 0 {
		t.Errorf("不变式应成立: %v", errs)
	}
}

// 贷方发生的孤儿要进「贷方兜底栏」，不能和借方混在一起。
func TestOrphanCreditGetsItsOwnOtherColumn(t *testing.T) {
	r, err := Build(Input{
		AccountCode: "5602", AccountName: "管理费用",
		From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: mgmtColumns(),
		Movements: []Movement{
			{Date: d("2025-03-06"), VoucherNo: "记-0004", AccountCode: "560217",
				Seq: 1, Debit: y(77)},
			{Date: d("2025-03-07"), VoucherNo: "记-0005", AccountCode: "560218",
				Seq: 1, Credit: y(30)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var others []Column
	for _, c := range r.Columns {
		if c.Other {
			others = append(others, c)
		}
	}
	if len(others) != 2 {
		t.Fatalf("兜底栏 = %d 个，期望 2（借贷各一个）", len(others))
	}
	if others[0].Side != SideDebit || others[0].Label != "其他" {
		t.Errorf("第一个兜底栏 = %+v", others[0])
	}
	if others[1].Side != SideCredit {
		t.Errorf("第二个兜底栏方向 = %q，期望 credit", others[1].Side)
	}
	// 贷方栏目里的金额按贷方为正
	if r.ColumnTotals[len(r.ColumnTotals)-1] != y(30) {
		t.Errorf("贷方兜底栏 = %s，期望 +30", r.ColumnTotals[len(r.ColumnTotals)-1])
	}
	if r.Closing != y(47) { // 借 77 − 贷 30 = 净借 47
		t.Errorf("期末余额 = %s，期望 +47（借方余额）", r.Closing)
	}
	if r.ClosingDir != "借" {
		t.Errorf("期末方向 = %q，期望 借", r.ClosingDir)
	}
	if errs := r.Check(); len(errs) != 0 {
		t.Errorf("不变式应成立: %v", errs)
	}
}

// 逐笔登记：一行一笔，不多不少，且按日期稳定排序。
func TestOneRowPerMovementInDateOrder(t *testing.T) {
	r, err := Build(Input{
		AccountCode: "5602", AccountName: "管理费用",
		From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: mgmtColumns(),
		Movements: []Movement{
			// 故意乱序给进去
			{Date: d("2025-03-20"), VoucherNo: "记-0015", AccountCode: "560206", Seq: 2, Debit: y(10)},
			{Date: d("2025-03-05"), VoucherNo: "记-0003", AccountCode: "560206", Seq: 1, Debit: y(20)},
			{Date: d("2025-03-05"), VoucherNo: "记-0003", AccountCode: "560207", Seq: 2, Debit: y(30)},
			{Date: d("2025-03-05"), VoucherNo: "记-0002", AccountCode: "560206", Seq: 1, Debit: y(40)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 4 {
		t.Fatalf("行数 = %d，期望 4（逐笔登记）", len(r.Rows))
	}
	want := []string{"记-0002", "记-0003", "记-0003", "记-0015"}
	for i, w := range want {
		if r.Rows[i].VoucherNo != w {
			t.Errorf("第 %d 行凭证号 = %q，期望 %q（同日按凭证号排）",
				i+1, r.Rows[i].VoucherNo, w)
		}
	}
	// 同一天同一张凭证内的两行按行号排：记-0003 的第 1 行（办公费）
	// 必须排在第 2 行（差旅费）前面
	if r.Columns[r.Rows[1].ColumnIndex].Key != "560206" ||
		r.Columns[r.Rows[2].ColumnIndex].Key != "560207" {
		t.Errorf("同凭证内的行序不对：第 2 行在「%s」栏、第 3 行在「%s」栏",
			r.Columns[r.Rows[1].ColumnIndex].Label,
			r.Columns[r.Rows[2].ColumnIndex].Label)
	}
	// 余额必须逐行累计
	var sum money.Money
	for i, row := range r.Rows {
		sum = sum.Add(row.Amount)
		if row.Balance != sum {
			t.Errorf("第 %d 行余额 = %s，期望 %s", i+1, row.Balance, sum)
		}
	}
}

// 金额非法的分录要报错，而不是悄悄算出一个错数。
func TestRejectsBadAmounts(t *testing.T) {
	base := Input{
		AccountCode: "5602", AccountName: "管理费用",
		From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: mgmtColumns(),
	}
	in := base
	in.Movements = []Movement{{Date: d("2025-03-05"), AccountCode: "560206",
		Debit: y(1), Credit: y(1)}}
	if _, err := Build(in); err == nil {
		t.Error("同时有借贷金额应报错")
	}

	in = base
	in.Movements = []Movement{{Date: d("2025-03-05"), AccountCode: "560206",
		Debit: y(-1)}}
	if _, err := Build(in); err == nil {
		t.Error("负金额应报错")
	}
}

// 零金额分录跳过，不产生空行。
func TestZeroAmountSkipped(t *testing.T) {
	r, err := Build(Input{
		AccountCode: "5602", AccountName: "管理费用",
		From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: mgmtColumns(),
		Movements: []Movement{
			{Date: d("2025-03-05"), VoucherNo: "记-0003", AccountCode: "560206", Seq: 1},
			{Date: d("2025-03-06"), VoucherNo: "记-0004", AccountCode: "560206", Seq: 1, Debit: y(5)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 1 {
		t.Errorf("行数 = %d，期望 1（零金额分录跳过）", len(r.Rows))
	}
}

// 参数与栏目定义的错误要在 Build 阶段就挡住。
func TestBuildRejectsBadInput(t *testing.T) {
	ok := Input{
		AccountCode: "5602", From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: mgmtColumns(),
	}

	bad := ok
	bad.From = calendar.Date{}
	if _, err := Build(bad); err == nil {
		t.Error("无效期间应报错")
	}

	bad = ok
	bad.From, bad.To = d("2025-04-01"), d("2025-03-31")
	if _, err := Build(bad); err == nil {
		t.Error("起始日晚于截止日应报错")
	}

	bad = ok
	bad.Columns = nil
	if _, err := Build(bad); err == nil {
		t.Error("没有栏目应报错")
	}

	// 同一个科目被指定为两个栏目：会让人以为金额进了两栏
	bad = ok
	bad.Columns = append(mgmtColumns(),
		Column{Key: "dup", Label: "重复", Side: SideDebit, AccountCode: "560206"})
	if _, err := Build(bad); err == nil {
		t.Error("同一科目映射到两个栏目应报错")
	}

	bad = ok
	bad.Columns = []Column{{Key: "x", Label: "方向错", Side: "sideways", AccountCode: "560206"}}
	if _, err := Build(bad); err == nil {
		t.Error("非法栏目方向应报错")
	}
}

// 栏目方向缺省为借方：绝大多数多栏式账是借方多栏。
func TestColumnSideDefaultsToDebit(t *testing.T) {
	r, err := Build(Input{
		AccountCode: "5602", From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: []Column{{Key: "560206", Label: "办公费", AccountCode: "560206"}},
		Movements: []Movement{
			{Date: d("2025-03-05"), AccountCode: "560206", Debit: y(300)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Columns[0].Side != SideDebit {
		t.Errorf("栏目方向 = %q，期望默认 debit", r.Columns[0].Side)
	}
	if r.ColumnTotals[0] != y(300) {
		t.Errorf("栏目合计 = %s", r.ColumnTotals[0])
	}
}

// 空期间的账也要能出：栏目留着、行是空的、期初照旧。
func TestEmptyPeriodKeepsColumns(t *testing.T) {
	r, err := Build(Input{
		AccountCode: "5602", AccountName: "管理费用",
		From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: mgmtColumns(),
		Opening: y(500),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Columns) != 3 {
		t.Errorf("栏目数 = %d，期望 3（没有发生额也要留着栏目）", len(r.Columns))
	}
	if len(r.Rows) != 0 {
		t.Errorf("行数 = %d，期望 0", len(r.Rows))
	}
	if r.Closing != y(500) || r.ClosingDir != "借" {
		t.Errorf("期末 = %s（%s），期望 500 借", r.Closing, r.ClosingDir)
	}
	var noted bool
	for _, n := range r.Notes {
		if strings.Contains(n, "没有发生额") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("应提示本期无发生额，实际 %v", r.Notes)
	}
	if errs := r.Check(); len(errs) != 0 {
		t.Errorf("不变式应成立: %v", errs)
	}
}

// 篡改合计后 Check 必须发现 —— 这条不变式是防止
// 「横向搬了金额、纵向对不上」这类静默错误的唯一手段。
func TestCheckDetectsTampering(t *testing.T) {
	r, err := Build(Input{
		AccountCode: "5602", AccountName: "管理费用",
		From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: mgmtColumns(),
		Movements: []Movement{
			{Date: d("2025-03-05"), AccountCode: "560206", Debit: y(300)},
			{Date: d("2025-03-06"), AccountCode: "560207", Debit: y(200)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if errs := r.Check(); len(errs) != 0 {
		t.Fatalf("初始状态应通过: %v", errs)
	}

	r.ColumnTotals[0] = r.ColumnTotals[0].Add(y(1))
	if errs := r.Check(); len(errs) == 0 {
		t.Error("篡改栏目合计后 Check 应报错")
	}
}

// Summary 一句话要能直接放进界面抬头。
func TestSummaryMentionsKeyNumbers(t *testing.T) {
	r, err := Build(Input{
		AccountCode: "5602", AccountName: "管理费用",
		From: d("2025-03-01"), To: d("2025-03-31"),
		Columns: mgmtColumns(),
		Movements: []Movement{
			{Date: d("2025-03-05"), AccountCode: "560206", Debit: y(300)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := r.Summary()
	for _, want := range []string{"管理费用", "2025-03-01", "2025-03-31", "300.00"} {
		if !strings.Contains(s, want) {
			t.Errorf("概览 %q 中缺少 %q", s, want)
		}
	}
}

func mustCol(t *testing.T, r *Report, key string) money.Money {
	t.Helper()
	v, ok := r.ColumnTotal(key)
	if !ok {
		t.Fatalf("栏目 %s 不存在", key)
	}
	return v
}
