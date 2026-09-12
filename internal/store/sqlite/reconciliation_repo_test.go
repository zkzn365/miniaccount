package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/domain/bank"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/reconciliation"
	"miniaccount/internal/domain/voucher"
)

// reconciliationFixture 在银行夹具之上补一笔期初余额凭证。
//
// 期初是必需的：银行对账单的第一条流水余额（150,000）隐含了期初 100,000，
// 企业账上如果没有对应的 100,000，「账面 vs 银行」从一开始就差一截，
// 调节表永远平不了。这也正是现实中会计必须先做期初导入的原因。
type reconciliationFixture struct {
	*bankFixture
	OpeningVoucherID int64
}

func setupReconciliation(t *testing.T) *reconciliationFixture {
	t.Helper()
	ctx := context.Background()
	f := &reconciliationFixture{bankFixture: setupBank(t)}

	v, err := voucher.New(voucher.WordJi, calendar.MustParse("2025-08-31"), "李会计")
	if err != nil {
		t.Fatalf("建凭证失败: %v", err)
	}
	v.Remark = "期初余额"
	if err := v.AddEntry(ledger.Entry{
		AccountCode: "100201", Summary: "期初余额", Debit: money.Money(10_000_000),
	}); err != nil {
		t.Fatalf("加分录失败: %v", err)
	}
	// 3001 实收资本要求「股东」辅助核算 —— 期初投入资本必须落到具体股东名下
	if err := v.AddEntry(ledger.Entry{
		AccountCode: "3001", Summary: "期初余额", Credit: money.Money(10_000_000),
		Aux: ledger.Aux{ContactID: &f.book.Sharehol},
	}); err != nil {
		t.Fatalf("加分录失败: %v", err)
	}

	saved, err := f.book.DB.Vouchers().SaveDraft(ctx, DraftInput{
		Voucher: v, CreatedBy: "李会计",
	})
	if err != nil {
		t.Fatalf("保存期初凭证失败: %v", err)
	}
	loaded, err := f.book.DB.Vouchers().Get(ctx, saved.VoucherID)
	if err != nil {
		t.Fatalf("读取期初凭证失败: %v", err)
	}
	res, err := f.book.DB.Vouchers().Post(ctx, PostInput{
		Voucher: loaded, Accounts: f.book.AccIDs,
		PostingBy: "李会计", At: time.Now(),
	})
	if err != nil {
		t.Fatalf("过账期初凭证失败: %v", err)
	}
	f.OpeningVoucherID = res.VoucherID
	return f
}

// matchAll 让四条流水全部就绪：规则命中两条，
// 剩下两条（货款、房租）由人工指定对方科目 —— 这正是实际使用中的顺序。
func (f *reconciliationFixture) matchAll(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	flows, err := f.book.DB.Bank().ListFlows(ctx, bank.StatusImported, calendar.Date{}, calendar.Date{}, 0)
	if err != nil {
		t.Fatalf("取流水失败: %v", err)
	}
	for _, fl := range flows {
		switch {
		case strings.Contains(fl.CounterpartyName, "科技"):
			if err := f.book.DB.Bank().SetSuggestion(ctx, fl.ID, Suggestion{
				CounterAccount: "1122", ContactID: &f.book.Customer,
			}); err != nil {
				t.Fatalf("指定对方科目失败: %v", err)
			}
		case strings.Contains(fl.CounterpartyName, "物业"):
			dept := int64(1)
			if err := f.book.DB.Bank().SetSuggestion(ctx, fl.ID, Suggestion{
				CounterAccount: "560210", DeptID: &dept,
			}); err != nil {
				t.Fatalf("指定对方科目失败: %v", err)
			}
		}
	}
	if _, err := f.book.DB.Bank().MatchAll(ctx, bank.StatusImported); err != nil {
		t.Fatalf("匹配失败: %v", err)
	}
	// 规则命中的 560206 管理费用—办公费也要求部门辅助核算
	matched, err := f.book.DB.Bank().ListFlows(ctx, bank.StatusMatched, calendar.Date{}, calendar.Date{}, 0)
	if err != nil {
		t.Fatalf("取流水失败: %v", err)
	}
	dept := int64(1)
	for _, fl := range matched {
		if fl.DeptID == nil || *fl.DeptID == 0 {
			if fl.CounterAccount == "560206" || fl.CounterAccount == "560210" {
				if err := f.book.DB.Bank().SetSuggestion(ctx, fl.ID, Suggestion{
					CounterAccount: fl.CounterAccount, DeptID: &dept, Memo: fl.Memo,
				}); err != nil {
					t.Fatalf("补部门失败: %v", err)
				}
			}
		}
	}
}

// postFlows 把指定序号的流水匹配后过账。
func (f *reconciliationFixture) postFlows(t *testing.T, idx ...int) {
	t.Helper()
	ctx := context.Background()
	f.matchAll(t)

	flows, err := f.book.DB.Bank().ListFlows(ctx, "", calendar.Date{}, calendar.Date{}, 0)
	if err != nil {
		t.Fatalf("取流水失败: %v", err)
	}
	if len(flows) == 0 {
		t.Fatal("没有流水")
	}
	ids := make([]int64, 0, len(idx))
	for _, i := range idx {
		if i >= len(flows) {
			t.Fatalf("流水序号 %d 越界（共 %d 条）", i, len(flows))
		}
		ids = append(ids, flows[i].ID)
	}
	res, err := f.book.DB.Bank().PostFlows(ctx, ids, "李会计", time.Now())
	if err != nil {
		t.Fatalf("过账流水失败: %v", err)
	}
	// PostFlows 把单笔失败塞进 Failures 而返回 nil error ——
	// 不检查 Created 会让「一条都没过」被当成成功。
	if res.Created != len(ids) {
		t.Fatalf("期望过账 %d 条，实际 %d 条，失败：%v", len(ids), res.Created, res.Failures)
	}
}

// asOf 是调节表的截止日。
var reconAsOf = calendar.Date{Year: 2025, Month: 9, Day: 30}

// TestReconciliationFullyPosted 全部流水已记账时，账面与银行应完全一致。
func TestReconciliationFullyPosted(t *testing.T) {
	ctx := context.Background()
	f := setupReconciliation(t)
	f.importStatement(t, bankStatement)
	f.postFlows(t, 0, 1, 2, 3)

	rep, err := f.book.DB.Reconciliation().BuildReconciliation(ctx, "100201", reconAsOf, nil)
	if err != nil {
		t.Fatalf("生成调节表失败: %v", err)
	}

	// 账面：期初 100,000 + 50,000 − 3,000 + 90,400 − 12,000 = 225,400
	if want := money.Money(22_540_000); rep.BookBalance != want {
		t.Errorf("账面余额 = %s，期望 %s", rep.BookBalance, want)
	}
	// 银行对账单最后一条余额 225,400，应被自动取到
	if rep.BankBalance == nil {
		t.Fatal("没有自动取到银行对账单余额")
	}
	if want := money.Money(22_540_000); *rep.BankBalance != want {
		t.Errorf("银行余额 = %s，期望 %s", *rep.BankBalance, want)
	}

	// ★ 期初 100,000 在两边都有（企业账上的期初凭证、
	// 对账单首笔余额隐含的期初），因此**不该**被当成未达账项。
	// 否则这张表永远差一个期初数，看着像一笔查不出来的错账。
	if n := rep.UnreconciledCount(); n != 0 {
		t.Errorf("不该有未达账项，实际 %d 笔", n)
	}
	if want := money.Money(10_000_000); rep.BookOpening != want {
		t.Errorf("账面期初 = %s，期望 %s", rep.BookOpening, want)
	}
	if rep.BankOpening == nil || *rep.BankOpening != money.Money(10_000_000) {
		t.Errorf("银行期初 = %v，期望 100000（由首笔流水 150,000 − 50,000 反推）", rep.BankOpening)
	}
	if rep.OpeningDiff() != 0 {
		t.Errorf("期初差额 = %s，期望 0", rep.OpeningDiff())
	}

	if !rep.Balanced() {
		t.Errorf("应调节相符，差 %s", rep.Difference())
	}
	if got := rep.Status(); got != reconciliation.StatusBalanced {
		t.Errorf("状态 = %q，期望 balanced", got)
	}
	if want := money.Money(22_540_000); rep.BookAdjusted != want {
		t.Errorf("调节后账面 = %s，期望 %s", rep.BookAdjusted, want)
	}
	if rep.BankAdjusted == nil || *rep.BankAdjusted != money.Money(22_540_000) {
		t.Errorf("调节后银行 = %v，期望 225400", rep.BankAdjusted)
	}
	// Check 的不变式（含「差额必然等于期初差额」）必须全部通过
	if errs := rep.Check(); len(errs) != 0 {
		t.Errorf("调节表自身不变式不成立: %v", errs)
	}
	if rep.UnreconciledFlows != 0 {
		t.Errorf("未处理流水 = %d，期望 0", rep.UnreconciledFlows)
	}
}

// TestReconciliationUnpostedFlow 有一笔流水未记账时，
// 它应落在「银行已付企业未付」，并且调节后两侧仍然相等。
//
// 这条断言是这张表的全部意义：**未达账项不该破坏平衡**。
// 如果加了未达账项之后两侧不相等，说明账本身记错了。
func TestReconciliationUnpostedFlow(t *testing.T) {
	ctx := context.Background()
	f := setupReconciliation(t)
	f.importStatement(t, bankStatement)
	// 只过前三条，把 09-25 的 12,000 房租付款留着不记
	f.postFlows(t, 0, 1, 2)

	rep, err := f.book.DB.Reconciliation().BuildReconciliation(ctx, "100201", reconAsOf, nil)
	if err != nil {
		t.Fatalf("生成调节表失败: %v", err)
	}

	// 账面少了那笔 12,000：237,400
	if want := money.Money(23_740_000); rep.BookBalance != want {
		t.Errorf("账面余额 = %s，期望 %s", rep.BookBalance, want)
	}
	if len(rep.BankPaidNotBooked) != 1 {
		t.Fatalf("应有 1 笔「银行已付企业未付」，实际 %d 笔", len(rep.BankPaidNotBooked))
	}
	item := rep.BankPaidNotBooked[0]
	if want := money.Money(1_200_000); item.Amount != want {
		t.Errorf("未达金额 = %s，期望 %s", item.Amount, want)
	}
	if item.Summary != "房租" {
		t.Errorf("未达摘要 = %q，期望 房租", item.Summary)
	}
	if item.Days != 5 { // 09-25 → 09-30
		t.Errorf("未达天数 = %d，期望 5", item.Days)
	}
	if rep.UnreconciledFlows != 1 {
		t.Errorf("未处理流水 = %d，期望 1", rep.UnreconciledFlows)
	}

	// 账面 237,400 − 未达 12,000 = 225,400 = 银行对账单余额
	if !rep.Balanced() {
		t.Errorf("未达账项不该破坏平衡，实际差 %s", rep.Difference())
	}
	if want := money.Money(22_540_000); rep.BookAdjusted != want {
		t.Errorf("调节后账面 = %s，期望 %s", rep.BookAdjusted, want)
	}
}

// TestReconciliationManualBankBalance 手工传银行余额时优先用它。
//
// 场景：拿不到电子流水，会计照纸质对账单敲了一个期末余额进来。
func TestReconciliationManualBankBalance(t *testing.T) {
	ctx := context.Background()
	f := setupReconciliation(t)
	f.importStatement(t, bankStatement)
	f.postFlows(t, 0, 1, 2, 3)

	// 故意敲错 1 元：调节表应算出差额并判定不符
	wrong := int64(22_540_000 - 100)
	rep, err := f.book.DB.Reconciliation().BuildReconciliation(ctx, "100201", reconAsOf, (*money.Money)(&wrong))
	if err != nil {
		t.Fatalf("生成调节表失败: %v", err)
	}
	if rep.BankBalance == nil || *rep.BankBalance != money.Money(wrong) {
		t.Fatalf("应使用手工传入的银行余额，实际 %v", rep.BankBalance)
	}
	if rep.Balanced() {
		t.Error("银行余额敲错了 1 元，不该判定为相符")
	}
	if want := money.Money(100); rep.Difference().Abs() != want {
		t.Errorf("差额 = %s，期望 %s", rep.Difference().Abs(), want)
	}
	if rep.BankAdjusted == nil || *rep.BankAdjusted != money.Money(22_540_000-100) {
		t.Errorf("调节后银行 = %v，期望 225399", rep.BankAdjusted)
	}
}

// TestReconciliationNoStatement 没有导入对账单时，
// 只能算出企业侧结果，银行侧留空且不谎称「相符」。
func TestReconciliationNoStatement(t *testing.T) {
	ctx := context.Background()
	f := setupReconciliation(t)

	rep, err := f.book.DB.Reconciliation().BuildReconciliation(ctx, "100201", reconAsOf, nil)
	if err != nil {
		t.Fatalf("生成调节表失败: %v", err)
	}
	if rep.BankBalance != nil {
		t.Errorf("没有对账单时银行余额应为空，实际 %v", rep.BankBalance)
	}
	if rep.BankAdjusted != nil {
		t.Error("没有对账单时不该给出调节后银行余额")
	}
	// ★ Balanced() 在缺银行余额时返回 true（无从判断，不误报），
	// 所以判断「能不能下结论」必须看 Status。
	if got := rep.Status(); got != reconciliation.StatusUnknown {
		t.Errorf("状态 = %q，期望 unknown", got)
	}
	// 账面只有期初 100,000
	if want := money.Money(10_000_000); rep.BookBalance != want {
		t.Errorf("账面余额 = %s，期望 %s", rep.BookBalance, want)
	}
	// 没有对账单就没有可比对的流水，未达账项自然是空的 ——
	// 硬凑出「企业已收银行未收 100,000」等于凭空造一笔差异。
	if n := rep.UnreconciledCount(); n != 0 {
		t.Errorf("没有对账单时不该有未达账项，实际 %d 笔", n)
	}
	if rep.BankOpening != nil {
		t.Errorf("没有对账单时银行期初应为空，实际 %v", rep.BankOpening)
	}
	// 账面余额全部来自期初，调节后不变
	if rep.BookAdjusted != rep.BookBalance {
		t.Errorf("调节后账面 = %s，期望等于账面余额 %s", rep.BookAdjusted, rep.BookBalance)
	}
}

// TestReconciliationIgnoredFlowExcluded 已忽略的流水不该出现在未达账项里。
//
// 忽略意味着「这笔不用管」（如银行手续费已单独入账、
// 或重复导入的垃圾行）。把它算进未达账项会让调节表永远差那么几笔，
// 会计每次都要手工排除，等于没做自动化。
func TestReconciliationIgnoredFlowExcluded(t *testing.T) {
	ctx := context.Background()
	f := setupReconciliation(t)
	f.importStatement(t, bankStatement)

	flows, err := f.book.DB.Bank().ListFlows(ctx, "", calendar.Date{}, calendar.Date{}, 0)
	if err != nil {
		t.Fatalf("取流水失败: %v", err)
	}
	if err := f.book.DB.Bank().IgnoreFlow(ctx, flows[3].ID, "手续费已单独入账"); err != nil {
		t.Fatalf("忽略流水失败: %v", err)
	}

	rep, err := f.book.DB.Reconciliation().BuildReconciliation(ctx, "100201", reconAsOf, nil)
	if err != nil {
		t.Fatalf("生成调节表失败: %v", err)
	}
	for _, it := range rep.BankPaidNotBooked {
		if want := money.Money(1_200_000); it.Amount == want {
			t.Errorf("已忽略的 12,000 房租不该出现在未达账项里")
		}
	}
	// 未忽略但也没记账的还有 3 条（含办公用品 3,000 与货款 90,400）
	if rep.UnreconciledFlows != 3 {
		t.Errorf("未处理流水 = %d，期望 3", rep.UnreconciledFlows)
	}
	if len(rep.BankPaidNotBooked) != 1 || rep.BankPaidNotBooked[0].Summary != "办公用品" {
		t.Errorf("未达账项应只剩办公用品那条，实际 %+v", rep.BankPaidNotBooked)
	}
}

// TestReconciliationAsOfCutoff 截止日之后发生的事不该进表。
//
// 「6 月 30 日的调节表」必须只反映 6 月 30 日之前的事，
// 否则跨期的表每次重算数字都不一样，没法作为底稿留档。
func TestReconciliationAsOfCutoff(t *testing.T) {
	ctx := context.Background()
	f := setupReconciliation(t)
	f.importStatement(t, bankStatement)
	f.postFlows(t, 0, 1, 2, 3)

	// 截止到 09-10：只剩 09-03 收 50,000 与 09-10 付 3,000
	cutoff := calendar.Date{Year: 2025, Month: 9, Day: 10}
	rep, err := f.book.DB.Reconciliation().BuildReconciliation(ctx, "100201", cutoff, nil)
	if err != nil {
		t.Fatalf("生成调节表失败: %v", err)
	}
	// 账面：期初 100,000 + 50,000 − 3,000 = 147,000
	if want := money.Money(14_700_000); rep.BookBalance != want {
		t.Errorf("账面余额 = %s，期望 %s", rep.BookBalance, want)
	}
	// 银行：09-10 那条流水余额 147,000
	if rep.BankBalance == nil || *rep.BankBalance != money.Money(14_700_000) {
		t.Errorf("银行余额 = %v，期望 147000", rep.BankBalance)
	}
	// 09-10 之后的流水与凭证都不该出现
	if n := len(rep.BankReceivedNotBooked) + len(rep.BankPaidNotBooked); n != 0 {
		t.Errorf("截止日之后的流水不该进表，实际 %d 笔", n)
	}
	if !rep.Balanced() {
		t.Errorf("应调节相符，差 %s", rep.Difference())
	}
}

// TestReconciliationUnknownAccount 科目不存在时报错而不是给一张空表。
//
// 给空表最危险：会计会以为「这个账户没钱」，而实际是编码敲错了。
func TestReconciliationUnknownAccount(t *testing.T) {
	ctx := context.Background()
	f := setupReconciliation(t)

	if _, err := f.book.DB.Reconciliation().BuildReconciliation(
		ctx, "999999", reconAsOf, nil); err == nil {
		t.Fatal("不存在的科目应报错")
	}
}

// TestReconciliationNonBankAccountNoDoubleCount 非银行科目上的分录
// 不该被当成银行科目的未达账项。
//
// 判定「企业已记银行未记」用的是「该科目上的分录」，
// 如果科目 id 传错成了应收账款，整张表就全错了。
func TestReconciliationScopedToAccount(t *testing.T) {
	ctx := context.Background()
	f := setupReconciliation(t)
	f.importStatement(t, bankStatement)
	f.postFlows(t, 0, 1, 2, 3)

	// 期初凭证的对方科目 3001 实收资本上有贷方 100,000。
	// 它不该出现在「企业已付银行未付」里。
	rep, err := f.book.DB.Reconciliation().BuildReconciliation(ctx, "100201", reconAsOf, nil)
	if err != nil {
		t.Fatalf("生成调节表失败: %v", err)
	}
	if len(rep.BookPaidNotBanked) != 0 {
		t.Errorf("银行科目上没有贷方分录，不该有「企业已付银行未付」，实际 %d 笔",
			len(rep.BookPaidNotBanked))
	}
	// 期初在窗口之外，被归入期初余额；窗口内全部流水都已记账，
	// 因此企业侧也不该有任何未达账项。
	if len(rep.BookReceivedNotBanked) != 0 {
		t.Errorf("企业侧不该有未达账项（期初已在窗口外），实际 %d 笔",
			len(rep.BookReceivedNotBanked))
	}
	// 而且账面期初必须正好是那 100,000，证明期初没被漏算
	if want := money.Money(10_000_000); rep.BookOpening != want {
		t.Errorf("账面期初 = %s，期望 %s", rep.BookOpening, want)
	}
}

// TestReconciliationOpeningDiffDiagnosesGap 期初不一致时，
// 两侧差额必须**恰好等于**期初差额。
//
// 这条性质的价值在于把「查不出来的一笔错账」变成一句可执行的话：
// 差额 47,800 不是某笔未达账项记错了，而是**期初就没对上**，
// 会计该去核对期初，而不是抱着四类未达账项翻一晚上。
func TestReconciliationOpeningDiffDiagnosesGap(t *testing.T) {
	ctx := context.Background()
	f := setupReconciliation(t)

	// 期初只记 60,000，比银行隐含的 100,000 少 40,000
	err := f.book.DB.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE ledger_entry SET debit = ? WHERE account_id = ?
			  AND voucher_id = ? AND debit > 0`,
			6_000_000, f.book.AccIDs["100201"], f.OpeningVoucherID)
		return err
	})
	if err != nil {
		t.Fatalf("调整期初失败: %v", err)
	}

	f.importStatement(t, bankStatement)
	f.postFlows(t, 0, 1, 2, 3)

	rep, err := f.book.DB.Reconciliation().BuildReconciliation(ctx, "100201", reconAsOf, nil)
	if err != nil {
		t.Fatalf("生成调节表失败: %v", err)
	}

	if want := money.Money(6_000_000); rep.BookOpening != want {
		t.Fatalf("账面期初 = %s，期望 %s", rep.BookOpening, want)
	}
	if rep.BankOpening == nil || *rep.BankOpening != money.Money(10_000_000) {
		t.Fatalf("银行期初 = %v，期望 100000", rep.BankOpening)
	}
	// 期初差 −40,000，两侧调节后的差额必须一模一样
	if want := money.Money(-4_000_000); rep.OpeningDiff() != want {
		t.Errorf("期初差额 = %s，期望 %s", rep.OpeningDiff(), want)
	}
	if rep.Difference() != rep.OpeningDiff() {
		t.Errorf("调节差额 %s 不等于期初差额 %s —— 这条不变式破了，"+
			"说明两侧取数口径不一致", rep.Difference(), rep.OpeningDiff())
	}
	if got := rep.Status(); got != reconciliation.StatusUnbalanced {
		t.Errorf("状态 = %q，期望 unbalanced", got)
	}
	// 提示里必须点名「期初」，否则会计不知道该往哪查
	var told bool
	for _, n := range rep.Notes {
		if strings.Contains(n, "期初") && strings.Contains(n, "40,000.00") {
			told = true
		}
	}
	if !told {
		t.Errorf("提示应说明期初差额，实际 %v", rep.Notes)
	}
	// Check 不该因此报错 —— 这是账的问题，不是程序的问题
	if errs := rep.Check(); len(errs) != 0 {
		t.Errorf("期初不一致时 Check 不应报程序性错误: %v", errs)
	}
}
