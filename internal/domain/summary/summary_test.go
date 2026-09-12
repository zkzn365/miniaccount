package summary

import (
	"strings"
	"testing"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

func y(n int64) money.Money { return money.Money(n) * money.Yuan }

func d(s string) calendar.Date { return calendar.MustParse(s) }

func acct(code, name string) map[string]AccountInfo {
	return map[string]AccountInfo{
		code: {Code: code, Name: name, FullName: name, BalanceDir: "debit"},
	}
}

// v 造一张凭证。
func v(id int64, word, no, date string, entries ...Entry) Voucher {
	return Voucher{ID: id, Word: word, No: no, Date: d(date), Entries: entries}
}

// dr / cr 造一条分录。
func dr(code string, amt money.Money) Entry {
	return Entry{AccountCode: code, Debit: amt}
}
func cr(code string, amt money.Money) Entry {
	return Entry{AccountCode: code, Credit: amt}
}

// threeVouchers 是三张覆盖记/收/付三种凭证字的凭证。
func threeVouchers() []Voucher {
	return []Voucher{
		v(1, "记", "记-2025-03-0001", "2025-03-05",
			dr("1002", y(1000)), cr("3001", y(1000))),
		v(2, "收", "收-2025-03-0001", "2025-03-05",
			dr("1002", y(500)), cr("1122", y(500))),
		v(3, "付", "付-2025-03-0001", "2025-03-10",
			dr("560206", y(300)), cr("1002", y(300))),
	}
}

// ★ 三个角度必须给出同一个合计。
//
// 这是整张表的地基：同一批凭证从凭证字、日期、科目三个角度各汇总一遍，
// 合计必须完全相等。任何一处漏算、重复算、口径不一致，这组等式立刻就破。
func TestThreeGroupingsAgree(t *testing.T) {
	r, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Vouchers: threeVouchers(),
		Accounts: acct("1002", "银行存款"),
	})
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if errs := r.Check(); len(errs) != 0 {
		t.Fatalf("不变式应成立: %v", errs)
	}

	// 三张凭证，借方 1000 + 500 + 300 = 1800
	if r.VoucherCount != 3 {
		t.Errorf("凭证张数 = %d，期望 3", r.VoucherCount)
	}
	if r.DebitTotal != y(1800) || r.CreditTotal != y(1800) {
		t.Errorf("合计 = 借%s/贷%s，期望各 1800.00", r.DebitTotal, r.CreditTotal)
	}
	if !r.Balanced() {
		t.Error("复式记账下借贷必然相等")
	}

	var wd, dd, ad money.Money
	var wc, dc, ac money.Money
	var wCount, dCount int
	for _, w := range r.WordRows {
		wd, wc = wd.Add(w.Debit), wc.Add(w.Credit)
		wCount += w.Count
	}
	for _, row := range r.DayRows {
		dd, dc = dd.Add(row.Debit), dc.Add(row.Credit)
		dCount += row.Count
	}
	for _, a := range r.AccountRows {
		ad, ac = ad.Add(a.Debit), ac.Add(a.Credit)
	}
	if wd != r.DebitTotal || wc != r.CreditTotal {
		t.Errorf("按凭证字合计 借%s/贷%s ≠ %s/%s", wd, wc, r.DebitTotal, r.CreditTotal)
	}
	if dd != r.DebitTotal || dc != r.CreditTotal {
		t.Errorf("按日期合计 借%s/贷%s ≠ %s/%s", dd, dc, r.DebitTotal, r.CreditTotal)
	}
	if ad != r.DebitTotal || ac != r.CreditTotal {
		t.Errorf("按科目合计 借%s/贷%s ≠ %s/%s", ad, ac, r.DebitTotal, r.CreditTotal)
	}
	if wCount != 3 || dCount != 3 {
		t.Errorf("张数：凭证字 %d、日期 %d，期望都是 3", wCount, dCount)
	}
}

// 按凭证字分组的张数与金额要分别正确。
func TestWordGrouping(t *testing.T) {
	r, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Vouchers: threeVouchers(),
		Accounts: acct("1002", "银行存款"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.WordRows) != 3 {
		t.Fatalf("凭证字 = %d 种，期望 3（记/收/付）", len(r.WordRows))
	}
	// 顺序按会计习惯：记 → 收 → 付 → 转，不是字典序
	want := []string{"记", "收", "付"}
	for i, w := range want {
		if r.WordRows[i].Word != w {
			t.Errorf("第 %d 个凭证字 = %q，期望 %q（记/收/付/转 的会计顺序）",
				i+1, r.WordRows[i].Word, w)
		}
	}
	if r.WordRows[0].Count != 1 || r.WordRows[0].Debit != y(1000) {
		t.Errorf("「记」字 = %d 张 / 借 %s", r.WordRows[0].Count, r.WordRows[0].Debit)
	}
	if r.WordRows[2].Debit != y(300) {
		t.Errorf("「付」字借方 = %s，期望 300.00", r.WordRows[2].Debit)
	}
}

// 按日期分组的顺序与每日金额。
func TestDayGrouping(t *testing.T) {
	r, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Vouchers: threeVouchers(),
		Accounts: acct("1002", "银行存款"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.DayRows) != 2 {
		t.Fatalf("有业务的日期 = %d 天，期望 2", len(r.DayRows))
	}
	if !r.DayRows[0].Date.Equal(d("2025-03-05")) {
		t.Errorf("第一天 = %s，期望 2025-03-05（升序）", r.DayRows[0].Date)
	}
	if r.DayRows[0].Count != 2 || r.DayRows[0].Debit != y(1500) {
		t.Errorf("03-05 = %d 张 / 借 %s，期望 2 张 / 1500.00",
			r.DayRows[0].Count, r.DayRows[0].Debit)
	}
	// 当天的凭证号要带上，便于顺着往下查
	if len(r.DayRows[0].Nos) != 2 {
		t.Errorf("03-05 的凭证号 = %v，期望 2 个", r.DayRows[0].Nos)
	}
}

// ★ 空档日：期间内一张凭证都没有的天，是月底最该被看见的东西。
func TestDayGaps(t *testing.T) {
	r, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-05"),
		Vouchers: []Voucher{
			v(1, "记", "记-1", "2025-03-01", dr("1002", y(1)), cr("3001", y(1))),
			v(2, "记", "记-2", "2025-03-05", dr("1002", y(1)), cr("3001", y(1))),
		},
		Accounts: acct("1002", "银行存款"),
	})
	if err != nil {
		t.Fatal(err)
	}
	gaps := r.DayGapDays()
	if len(gaps) != 3 {
		t.Fatalf("空档 %d 天，期望 3（03-02、03-03、03-04）", len(gaps))
	}
	for i, want := range []string{"2025-03-02", "2025-03-03", "2025-03-04"} {
		if gaps[i].String() != want {
			t.Errorf("第 %d 个空档 = %s，期望 %s", i+1, gaps[i], want)
		}
	}
}

// ★ 被红字冲销的凭证**照样计入**汇总。
//
// 红字冲销模型下原凭证与红字凭证都留在账上、都参与汇总，净额自然为零。
// 把被冲销的排除掉，这张表就和总账对不上了。
func TestVoidedVouchersStillCounted(t *testing.T) {
	r, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Vouchers: []Voucher{
			v(1, "记", "记-1", "2025-03-05", dr("560206", y(300)), cr("1002", y(300))),
			func() Voucher {
				x := v(2, "记", "记-9", "2025-03-20", dr("1002", y(300)), cr("560206", y(300)))
				x.Reversal = true
				return x
			}(),
		},
		Accounts: acct("1002", "银行存款"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.VoucherCount != 2 {
		t.Errorf("凭证张数 = %d，期望 2（原凭证 + 红字凭证）", r.VoucherCount)
	}
	if r.DebitTotal != y(600) || r.CreditTotal != y(600) {
		t.Errorf("合计 = 借%s/贷%s，期望各 600.00（总额不净额）",
			r.DebitTotal, r.CreditTotal)
	}
	if r.ReversalCount != 1 {
		t.Errorf("红字凭证数 = %d，期望 1", r.ReversalCount)
	}
	// 560206 借贷相抵为 0
	for _, a := range r.AccountRows {
		if a.AccountCode == "560206" && a.Net() != 0 {
			t.Errorf("560206 净额 = %s，期望 0（300 记入又 300 冲回）", a.Net())
		}
	}
	var noted bool
	for _, n := range r.Notes {
		if strings.Contains(n, "红字冲销凭证") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("应提示其中含红字凭证，实际 %v", r.Notes)
	}
}

// 已作废（被冲销）的凭证也要计入，并给出提示。
func TestVoidedFlagCountedAndNoted(t *testing.T) {
	x := v(1, "记", "记-1", "2025-03-05", dr("1002", y(300)), cr("3001", y(300)))
	x.Voided = true

	r, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Vouchers: []Voucher{x}, Accounts: acct("1002", "银行存款"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.VoucherCount != 1 || r.VoidedCount != 1 {
		t.Errorf("张数 = %d、已冲销 = %d，期望 1 / 1", r.VoucherCount, r.VoidedCount)
	}
	var noted bool
	for _, n := range r.Notes {
		if strings.Contains(n, "仍计入本表") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("应说明被冲销的凭证仍计入，实际 %v", r.Notes)
	}
}

// ★ 草稿不进汇总，但必须提示 —— 否则会计以为账记全了。
func TestDraftCountIsNoted(t *testing.T) {
	r, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Vouchers: threeVouchers(), Accounts: acct("1002", "银行存款"),
		DraftCount: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.VoucherCount != 3 {
		t.Errorf("凭证张数 = %d，期望 3（草稿不计入）", r.VoucherCount)
	}
	var noted bool
	for _, n := range r.Notes {
		if strings.Contains(n, "3 张草稿") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("应提示有 3 张草稿未记账，实际 %v", r.Notes)
	}
}

// ★ 一张借贷不平的凭证必须当场挡住。
//
// 这是整张表的地基：一张不平的凭证混进来，三个角度的合计会同时被带偏，
// 而且偏得一模一样 —— 互相印证反而看不出问题。
func TestRejectsUnbalancedVoucher(t *testing.T) {
	_, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Vouchers: []Voucher{
			v(1, "记", "记-1", "2025-03-05", dr("1002", y(1000)), cr("3001", y(900))),
		},
		Accounts: acct("1002", "银行存款"),
	})
	if err == nil {
		t.Fatal("借贷不平的凭证应报错")
	}
	if !strings.Contains(err.Error(), "借贷不平") {
		t.Errorf("报错应点明借贷不平，实际 %q", err.Error())
	}
}

// 同一张凭证被传两次要报错，而不是算两遍。
func TestRejectsDuplicateVoucher(t *testing.T) {
	dup := v(7, "记", "记-1", "2025-03-05", dr("1002", y(100)), cr("3001", y(100)))
	_, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Vouchers: []Voucher{dup, dup},
		Accounts: acct("1002", "银行存款"),
	})
	if err == nil {
		t.Fatal("重复凭证应报错")
	}
	if !strings.Contains(err.Error(), "重复") {
		t.Errorf("报错应点明重复，实际 %q", err.Error())
	}
}

// 没有分录的凭证要挡住。
func TestRejectsEmptyVoucher(t *testing.T) {
	_, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Vouchers: []Voucher{{ID: 1, Word: "记", No: "记-1", Date: d("2025-03-05")}},
		Accounts: acct("1002", "银行存款"),
	})
	if err == nil {
		t.Fatal("没有分录的凭证应报错")
	}
}

// 非法金额要挡住。
func TestRejectsBadAmounts(t *testing.T) {
	base := Input{From: d("2025-03-01"), To: d("2025-03-31"),
		Accounts: acct("1002", "银行存款")}

	in := base
	in.Vouchers = []Voucher{v(1, "记", "记-1", "2025-03-05",
		dr("1002", y(-1)), cr("3001", y(-1)))}
	if _, err := Build(in); err == nil {
		t.Error("负金额应报错")
	}

	in = base
	in.Vouchers = []Voucher{v(1, "记", "记-1", "2025-03-05",
		Entry{AccountCode: "1002", Debit: y(1), Credit: y(1)})}
	if _, err := Build(in); err == nil {
		t.Error("同一分录同时有借贷金额应报错")
	}

	in = base
	in.Vouchers = []Voucher{v(1, "记", "记-1", "2025-03-05",
		dr("", y(1)), cr("3001", y(1)))}
	if _, err := Build(in); err == nil {
		t.Error("缺科目的分录应报错")
	}
}

// 期间非法要挡住。
func TestRejectsBadRange(t *testing.T) {
	if _, err := Build(Input{From: d("2025-04-01"), To: d("2025-03-31")}); err == nil {
		t.Error("起始日晚于截止日应报错")
	}
	if _, err := Build(Input{To: d("2025-03-31")}); err == nil {
		t.Error("无效起始日应报错")
	}
}

// 空期间：表照样出，合计为零，并说明原因。
func TestEmptyPeriod(t *testing.T) {
	r, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Accounts: acct("1002", "银行存款"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.VoucherCount != 0 || r.DebitTotal != 0 {
		t.Errorf("空期间应全为零，实际 %d 张 / %s", r.VoucherCount, r.DebitTotal)
	}
	if !r.Balanced() {
		t.Error("零 == 零，应判为平衡")
	}
	if len(r.WordRows) != 0 || len(r.DayRows) != 0 || len(r.AccountRows) != 0 {
		t.Error("空期间不该有任何分组行")
	}
	var noted bool
	for _, n := range r.Notes {
		if strings.Contains(n, "没有已记账的凭证") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("应提示本期无凭证，实际 %v", r.Notes)
	}
	if errs := r.Check(); len(errs) != 0 {
		t.Errorf("空期间也应通过不变式: %v", errs)
	}
}

// 科目名缺失时退化为只显示编码，而不是造一个空名字。
func TestUnknownAccountFallsBackToCode(t *testing.T) {
	r, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Vouchers: threeVouchers(),
		// 故意不给科目档案
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range r.AccountRows {
		if a.AccountName == "" || a.FullName == "" {
			t.Errorf("科目 %s 的名字不应为空（应退化为编码）", a.AccountCode)
		}
	}
}

// 科目汇总行要按科目编码排序，且带全名。
func TestAccountRowsSortedAndNamed(t *testing.T) {
	r, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Vouchers: threeVouchers(),
		Accounts: map[string]AccountInfo{
			"1002":   {Code: "1002", Name: "银行存款", FullName: "银行存款", BalanceDir: "debit"},
			"1122":   {Code: "1122", Name: "应收账款", FullName: "应收账款", BalanceDir: "debit"},
			"3001":   {Code: "3001", Name: "实收资本", FullName: "实收资本", BalanceDir: "credit"},
			"560206": {Code: "560206", Name: "管理费用—办公费", FullName: "管理费用—办公费", BalanceDir: "debit"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	codes := make([]string, len(r.AccountRows))
	for i, a := range r.AccountRows {
		codes[i] = a.AccountCode
	}
	want := []string{"1002", "1122", "3001", "560206"}
	if len(codes) != len(want) {
		t.Fatalf("科目行 = %v，期望 %v", codes, want)
	}
	for i := range want {
		if codes[i] != want[i] {
			t.Errorf("科目行顺序 = %v，期望 %v", codes, want)
			break
		}
	}
	// 1002 银行存款：借 1000 + 500 = 1500，贷 300
	var bank AccountRow
	for _, a := range r.AccountRows {
		if a.AccountCode == "1002" {
			bank = a
		}
	}
	if bank.Debit != y(1500) || bank.Credit != y(300) {
		t.Errorf("1002 = 借%s/贷%s，期望 1500/300", bank.Debit, bank.Credit)
	}
	if bank.Net() != y(1200) {
		t.Errorf("1002 净额 = %s，期望 1200.00", bank.Net())
	}
	if bank.Count != 3 {
		t.Errorf("1002 出现在 %d 张凭证里，期望 3", bank.Count)
	}
}

// ★ 篡改合计后 Check 必须发现。
func TestCheckDetectsTampering(t *testing.T) {
	r, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Vouchers: threeVouchers(), Accounts: acct("1002", "银行存款"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if errs := r.Check(); len(errs) != 0 {
		t.Fatalf("初始状态应通过: %v", errs)
	}
	r.WordRows[0].Debit = r.WordRows[0].Debit.Add(y(1))
	if errs := r.Check(); len(errs) == 0 {
		t.Error("篡改某个分组的合计后 Check 应报错")
	}
}

// Summary 一句话要能直接放进界面抬头。
func TestSummaryMentionsKeyNumbers(t *testing.T) {
	r, err := Build(Input{
		From: d("2025-03-01"), To: d("2025-03-31"),
		Vouchers: threeVouchers(), Accounts: acct("1002", "银行存款"),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := r.Summary()
	for _, want := range []string{"2025-03-01", "2025-03-31", "3 张", "1,800.00"} {
		if !strings.Contains(s, want) {
			t.Errorf("概览 %q 中缺少 %q", s, want)
		}
	}
}
