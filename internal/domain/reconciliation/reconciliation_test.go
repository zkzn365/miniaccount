package reconciliation

import (
	"strings"
	"testing"

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

func ptr(m money.Money) *money.Money { return &m }

// ★ 教科书的场景：账面与对账单各有两笔未达，调节后必须相等
func TestClassicReconciliation(t *testing.T) {
	// 账面余额 100,000；对账单余额 110,000
	//
	// 企业账面方：
	//   加：银行已收企业未收 30,000（银行代收货款）
	//   减：银行已付企业未付 10,000（银行扣手续费）
	//   调节后 = 100,000 + 30,000 − 10,000 = 120,000
	//
	// 对账单方：
	//   加：企业已收银行未收 15,000（送存支票未到账）
	//   减：企业已付银行未付 5,000（开出的支票未兑付）
	//   调节后 = 110,000 + 15,000 − 5,000 = 120,000
	r, err := Build(Input{
		AsOf: d("2025-03-31"), AccountCode: "1002", AccountName: "银行存款",
		BookBalance: y(100000), BankBalance: ptr(y(110000)),
		BankReceivedNotBooked: []Item{
			{Date: d("2025-03-28"), Summary: "银行代收货款", Reference: "流水 1", Amount: y(30000)},
		},
		BankPaidNotBooked: []Item{
			{Date: d("2025-03-30"), Summary: "银行扣手续费", Reference: "流水 2", Amount: y(10000)},
		},
		BookReceivedNotBanked: []Item{
			{Date: d("2025-03-29"), Summary: "送存支票", Reference: "记-0001", Amount: y(15000)},
		},
		BookPaidNotBanked: []Item{
			{Date: d("2025-03-25"), Summary: "开出支票未兑", Reference: "记-0002", Amount: y(5000)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if r.BookAdd != y(30000) || r.BookLess != y(10000) {
		t.Errorf("企业账面方 = 加 %s 减 %s", r.BookAdd, r.BookLess)
	}
	if r.BookAdjusted != y(120000) {
		t.Errorf("企业账面调节后 = %s，期望 120000.00", r.BookAdjusted)
	}
	if r.BankAdd != y(15000) || r.BankLess != y(5000) {
		t.Errorf("银行方 = 加 %s 减 %s", r.BankAdd, r.BankLess)
	}
	if r.BankAdjusted == nil || *r.BankAdjusted != y(120000) {
		t.Errorf("银行方调节后 = %v，期望 120000.00", r.BankAdjusted)
	}
	// ★ 两侧相等 —— 相等说明账没记错
	if !r.Balanced() {
		t.Errorf("两侧应相等，实际差 %s", r.Difference())
	}
	if !strings.Contains(r.Summary(), "账实相符") {
		t.Errorf("概览 = %q", r.Summary())
	}
	if errs := r.Check(); len(errs) > 0 {
		t.Errorf("不变式应全部成立: %v", errs)
	}
	// 天数要算出来
	if r.Lines[0].Item.Days != 3 {
		t.Errorf("第 1 行天数 = %d，期望 3", r.Lines[0].Item.Days)
	}
}

// ★ 调节后不相等时，必须明确说是「账记错了」而不是含糊其辞
func TestUnbalancedMeansBookkeepingError(t *testing.T) {
	r, err := Build(Input{
		AsOf: d("2025-03-31"), BookBalance: y(100000), BankBalance: ptr(y(100000)),
		BankReceivedNotBooked: []Item{
			{Date: d("2025-03-28"), Amount: y(30000)},
		},
		// 少了一笔对应的未达账项，两边就对不上
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Balanced() {
		t.Fatal("两侧不该相等")
	}
	if r.Difference() != y(30000) {
		t.Errorf("差额 = %s，期望 30000.00", r.Difference())
	}
	s := r.Summary()
	if !strings.Contains(s, "账记错了") {
		t.Errorf("概览应说明可能记错账，实际 %q", s)
	}
	if !strings.Contains(s, "30,000") {
		t.Errorf("概览应给出差额，实际 %q", s)
	}
}

// 还没有对账单时要能出表，并明确说明为什么银行方是空的
func TestWithoutBankStatement(t *testing.T) {
	r, err := Build(Input{
		AsOf: d("2025-03-31"), BookBalance: y(100000),
		UnreconciledFlows: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.BankAdjusted != nil {
		t.Error("没有对账单余额时不该给出银行方调节后余额")
	}
	// 无从判断时不误报「不平衡」
	if !r.Balanced() {
		t.Error("缺少对账单余额时不该报告不平衡")
	}
	if r.Difference() != 0 {
		t.Errorf("差额应为 0，实际 %s", r.Difference())
	}
	if len(r.Notes) < 2 {
		t.Errorf("应给出提示（缺对账单 + 有未处理流水），实际 %v", r.Notes)
	}
	joined := strings.Join(r.Notes, " ")
	if !strings.Contains(joined, "对账单") {
		t.Errorf("应说明缺对账单，实际 %v", r.Notes)
	}
	if !strings.Contains(joined, "3 条流水") {
		t.Errorf("应说明有 3 条流水未生成凭证，实际 %v", r.Notes)
	}
}

// 两边完全对上时不该有明细行
func TestFullyReconciled(t *testing.T) {
	r, err := Build(Input{
		AsOf: d("2025-03-31"), BookBalance: y(100000), BankBalance: ptr(y(100000)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Lines) != 0 {
		t.Errorf("没有未达账项时不该有明细行，实际 %d 行", len(r.Lines))
	}
	if r.BookAdjusted != y(100000) {
		t.Errorf("调节后 = %s", r.BookAdjusted)
	}
	if !r.Balanced() {
		t.Error("应相等")
	}
	if !strings.Contains(strings.Join(r.Notes, " "), "完全对上") {
		t.Errorf("应说明没有未达账项，实际 %v", r.Notes)
	}
}

// 逐笔明细必须逐条列出，而不是只给一个小计 ——
// 差 1 万块时会计需要知道是哪几笔
func TestLinesListEveryItem(t *testing.T) {
	// 期初必须一起给：两侧调节后的差额**必然恰好等于期初差额**
	// （窗口内的发生额在两边完全抵消）。这里 BankOpening 比 BookOpening
	// 少 600，正好对应下面那笔「银行已收企业未收 600」——
	// 少给期初就会触发 Check 的不变式报警，这是刻意的设计。
	r, err := Build(Input{
		AsOf: d("2025-03-31"), BookBalance: y(0), BankBalance: ptr(y(0)),
		BookOpening: y(600), BankOpening: ptr(y(0)),
		BankReceivedNotBooked: []Item{
			{Date: d("2025-03-01"), Summary: "第一笔", Amount: y(100)},
			{Date: d("2025-03-02"), Summary: "第二笔", Amount: y(200)},
			{Date: d("2025-03-03"), Summary: "第三笔", Amount: y(300)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var detail, subtotal int
	for _, l := range r.Lines {
		if l.Item != nil {
			detail++
		} else if l.Subtotal {
			subtotal++
			if l.Amount != y(600) {
				t.Errorf("小计 = %s，期望 600.00", l.Amount)
			}
		}
	}
	if detail != 3 {
		t.Errorf("明细行 = %d，期望 3（逐笔列出）", detail)
	}
	if subtotal != 1 {
		t.Errorf("小计行 = %d，期望 1", subtotal)
	}
	// 小计必须等于逐笔之和
	if errs := r.Check(); len(errs) > 0 {
		t.Errorf("不变式应成立: %v", errs)
	}
}

func TestBadDate(t *testing.T) {
	if _, err := Build(Input{}); err == nil {
		t.Error("无效截止日期应报错")
	}
}

func TestCheckDetectsTampering(t *testing.T) {
	r, _ := Build(Input{
		AsOf: d("2025-03-31"), BookBalance: y(100), BankBalance: ptr(y(100)),
		BankReceivedNotBooked: []Item{{Date: d("2025-03-01"), Amount: y(50)}},
	})
	r.BookAdd = y(999)
	if errs := r.Check(); len(errs) == 0 {
		t.Error("被篡改的小计应被检出")
	}

	r2, _ := Build(Input{AsOf: d("2025-03-31"), BookBalance: y(100)})
	r2.BookAdjusted = y(999)
	if errs := r2.Check(); len(errs) == 0 {
		t.Error("被篡改的调节后余额应被检出")
	}
}

func TestSortItemsByDate(t *testing.T) {
	items := []Item{
		{Date: d("2025-03-05"), Summary: "b"},
		{Date: d("2025-03-01"), Summary: "a"},
		{Date: d("2025-03-05"), Summary: "a"},
	}
	SortItemsByDate(items)
	want := []string{"2025-03-01", "2025-03-05", "2025-03-05"}
	for i, w := range want {
		if items[i].Date.String() != w {
			t.Errorf("第 %d 个 = %s，期望 %s", i, items[i].Date, w)
		}
	}
	// 同日按摘要稳定排
	if items[1].Summary != "a" || items[2].Summary != "b" {
		t.Errorf("同日应按摘要排序: %v", items)
	}
}
