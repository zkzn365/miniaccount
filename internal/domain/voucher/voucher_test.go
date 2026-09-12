package voucher

import (
	"errors"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

func mkAcc(code, name, parent string, rt account.RootType, dir account.BalanceDir, aux ...account.AuxType) *account.Account {
	return &account.Account{
		Code: code, Name: name, ParentCode: parent,
		Level: (len(code) - 2) / 2, IsLeaf: true,
		RootType: rt, BalanceDir: dir, AuxTypes: aux, IsEnabled: true,
	}
}

func testCtx(t *testing.T) *ledger.Context {
	t.Helper()
	tree, err := account.NewTree([]*account.Account{
		mkAcc("1002", "银行存款", "", account.RootAsset, account.DirDebit),
		mkAcc("1122", "应收账款", "", account.RootAsset, account.DirDebit, account.AuxCustomer),
		mkAcc("2241", "其他应付款", "", account.RootLiability, account.DirCredit),
		mkAcc("224101", "其他应付款—股东", "2241", account.RootLiability, account.DirCredit, account.AuxShareholder),
		mkAcc("5602", "管理费用", "", account.RootExpense, account.DirDebit),
		mkAcc("560207", "管理费用—差旅费", "5602", account.RootExpense, account.DirDebit, account.AuxDept),
		mkAcc("5001", "主营业务收入", "", account.RootIncome, account.DirCredit),
	})
	if err != nil {
		t.Fatalf("构造科目树失败: %v", err)
	}
	cal, err := period.NewCalendar(2025, 1, 2026, period.Key{Year: 2025, Month: 9})
	if err != nil {
		t.Fatalf("构造期间表失败: %v", err)
	}
	return &ledger.Context{
		Accounts:     tree,
		Periods:      cal,
		ContactKinds: map[int64]string{1: "customer", 3: "shareholder"},
	}
}

var (
	d0901 = calendar.MustParse("2025-09-01")
	d0911 = calendar.MustParse("2025-09-11")
	amt   = func(y int64) money.Money { return money.Money(y) * money.Yuan }
	i64   = func(v int64) *int64 { return &v }
)

// 一张平衡的两行凭证
func sampleVoucher(t *testing.T) *Voucher {
	t.Helper()
	v, err := New(WordJi, d0911, "张三")
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if err := v.AddEntries(
		ledger.Entry{AccountCode: "1002", Summary: "收到股东借款", Debit: amt(50000)},
		ledger.Entry{AccountCode: "224101", Summary: "收到股东借款", Credit: amt(50000),
			Aux: ledger.Aux{ContactID: i64(3)}},
	); err != nil {
		t.Fatalf("AddEntries 失败: %v", err)
	}
	return v
}

// ---------------------------------------------------------------------------
// 凭证字与字号
// ---------------------------------------------------------------------------

func TestFormatNo(t *testing.T) {
	cases := []struct {
		w    Word
		k    period.Key
		seq  int
		want string
	}{
		{WordJi, period.NewKey(2025, 1), 1, "记-2025-01-0001"},
		{WordJi, period.NewKey(2025, 12), 999, "记-2025-12-0999"},
		{WordShou, period.NewKey(2025, 9), 12345, "收-2025-09-12345"},
		{WordFu, period.NewKey(2026, 3), 7, "付-2026-03-0007"},
		{WordZhuan, period.NewKey(2025, 6), 42, "转-2025-06-0042"},
	}
	for _, c := range cases {
		if got := FormatNo(c.w, c.k, c.seq); got != c.want {
			t.Errorf("FormatNo(%s,%v,%d) = %q，期望 %q", c.w, c.k, c.seq, got, c.want)
		}
	}
}

func TestParseNoRoundTrip(t *testing.T) {
	for _, c := range []struct {
		w   Word
		k   period.Key
		seq int
	}{
		{WordJi, period.NewKey(2025, 1), 1},
		{WordShou, period.NewKey(2025, 9), 12345},
		{WordZhuan, period.NewKey(2026, 12), 500},
	} {
		no := FormatNo(c.w, c.k, c.seq)
		w, k, seq, err := ParseNo(no)
		if err != nil {
			t.Fatalf("ParseNo(%q) 失败: %v", no, err)
		}
		if w != c.w || k != c.k || seq != c.seq {
			t.Errorf("往返失败 %q → %s/%v/%d", no, w, k, seq)
		}
	}
}

func TestParseNoErrors(t *testing.T) {
	for _, s := range []string{"", "记-2025-01", "记-2025-01-0001-9", "X-2025-01-0001",
		"记-2025-13-0001", "记-2025-01-0000", "abc"} {
		if _, _, _, err := ParseNo(s); err == nil {
			t.Errorf("ParseNo(%q) 应报错", s)
		}
	}
}

func TestWordValid(t *testing.T) {
	for _, w := range AllWords {
		if !w.Valid() {
			t.Errorf("%s 应合法", w)
		}
	}
	// 允许自定义短凭证字
	for _, w := range []Word{"银", "现", "转记"} {
		if !w.Valid() {
			t.Errorf("自定义凭证字 %s 应允许", w)
		}
	}
	for _, w := range []Word{"", "转账凭证", "JW"} {
		if w.Valid() {
			t.Errorf("%q 不应合法", w)
		}
	}
}

// ---------------------------------------------------------------------------
// 创建
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	v, err := New(WordJi, d0911, "张三")
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if v.Status != StatusDraft {
		t.Errorf("新凭证状态 = %s，期望 draft", v.Status)
	}
	if v.Source != SourceManual {
		t.Errorf("默认来源 = %s，期望 manual", v.Source)
	}
	if v.Period != (period.NewKey(2025, 9)) {
		t.Errorf("期间 = %v，期望 2025-09", v.Period)
	}
	if v.CreatedBy != "张三" {
		t.Errorf("制单人 = %q", v.CreatedBy)
	}
}

func TestNewRejectsBadInput(t *testing.T) {
	if _, err := New(Word(""), d0911, "张三"); !errors.Is(err, ErrBadWord) {
		t.Errorf("空凭证字应报错，得到 %v", err)
	}
	if _, err := New(WordJi, calendar.Date{}, "张三"); !errors.Is(err, ErrBadDate) {
		t.Errorf("空日期应报错，得到 %v", err)
	}
	if _, err := New(WordJi, d0911, ""); !errors.Is(err, ErrMissingMaker) {
		t.Errorf("缺制单人应报错，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 合计与平衡
// ---------------------------------------------------------------------------

func TestTotalsAndBalance(t *testing.T) {
	v := sampleVoucher(t)
	if v.TotalDebit() != amt(50000) || v.TotalCredit() != amt(50000) {
		t.Errorf("合计 = %s / %s", v.TotalDebit(), v.TotalCredit())
	}
	if !v.IsBalanced() {
		t.Error("应平衡")
	}
	if v.Amount() != amt(50000) {
		t.Errorf("Amount = %s", v.Amount())
	}
}

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

func TestValidate(t *testing.T) {
	ctx := testCtx(t)
	v := sampleVoucher(t)
	per, err := v.Validate(ctx)
	if err != nil {
		t.Fatalf("应通过校验，得到 %v", err)
	}
	if per.Key != (period.NewKey(2025, 9)) {
		t.Errorf("期间 = %v", per.Key)
	}
}

func TestValidateUnbalanced(t *testing.T) {
	ctx := testCtx(t)
	v, _ := New(WordJi, d0911, "张三")
	_ = v.AddEntries(
		ledger.Entry{AccountCode: "1002", Summary: "x", Debit: amt(100)},
		ledger.Entry{AccountCode: "5001", Summary: "x", Credit: amt(99)},
	)
	if _, err := v.Validate(ctx); !errors.Is(err, ledger.ErrNotBalanced) {
		t.Errorf("不平衡应报 ErrNotBalanced，得到 %v", err)
	}
}

func TestValidateNoEntries(t *testing.T) {
	ctx := testCtx(t)
	v, _ := New(WordJi, d0911, "张三")
	if _, err := v.Validate(ctx); !errors.Is(err, ErrNoEntries) {
		t.Errorf("无分录应报 ErrNoEntries，得到 %v", err)
	}
}

// 凭证日期改动后必须同步期间，否则按期间查询会漏掉这张凭证
func TestValidateDetectsPeriodDrift(t *testing.T) {
	ctx := testCtx(t)
	v := sampleVoucher(t)
	v.BizDate = calendar.MustParse("2025-08-15") // 只改日期，不改 Period
	_, err := v.Validate(ctx)
	if err == nil {
		t.Fatal("期间与日期不一致应报错")
	}
	if !strings.Contains(err.Error(), "不一致") {
		t.Errorf("错误信息应说明不一致，得到 %v", err)
	}
}

func TestValidateRejectsClosedPeriod(t *testing.T) {
	ctx := testCtx(t)
	// 结账必须按顺序：先把 1..9 月逐月结掉，9 月才能结
	for m := 1; m <= 9; m++ {
		if err := ctx.Periods.Close(period.NewKey(2025, m), "admin", time.Now()); err != nil {
			t.Fatalf("结账 %d 月失败: %v", m, err)
		}
	}
	v := sampleVoucher(t)
	if _, err := v.Validate(ctx); !errors.Is(err, period.ErrNotOpen) {
		t.Errorf("已结账期间应报 ErrNotOpen，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 状态机
// ---------------------------------------------------------------------------

func TestPostLifecycle(t *testing.T) {
	ctx := testCtx(t)
	v := sampleVoucher(t)

	// 草稿：可改可删可过账
	if err := v.CanEdit(); err != nil {
		t.Errorf("草稿应可改，得到 %v", err)
	}
	if err := v.CanDelete(); err != nil {
		t.Errorf("草稿应可删，得到 %v", err)
	}
	if err := v.CanPost(); err != nil {
		t.Errorf("草稿应可过账，得到 %v", err)
	}
	// 草稿不能作废（直接删即可）
	if err := v.CanVoid(); !errors.Is(err, ErrNotPosted) {
		t.Errorf("草稿作废应报 ErrNotPosted，得到 %v", err)
	}

	// 过账
	per, err := v.Validate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = per
	now := time.Now()
	if err := v.Post("李四", now); err != nil {
		t.Fatalf("过账失败: %v", err)
	}
	if v.Status != StatusPosted {
		t.Errorf("状态 = %s，期望 posted", v.Status)
	}
	if v.PostedBy != "李四" || v.PostedAt == nil {
		t.Error("记账签章未写入")
	}

	// 已过账：不可改、不可删、不可重复过账、可作废
	if err := v.CanEdit(); !errors.Is(err, ErrNotDraft) {
		t.Errorf("已过账不可改，得到 %v", err)
	}
	if err := v.CanDelete(); err == nil {
		t.Error("已过账不可删")
	}
	if err := v.CanPost(); !errors.Is(err, ErrAlreadyPosted) {
		t.Errorf("重复过账应报 ErrAlreadyPosted，得到 %v", err)
	}
	if err := v.CanVoid(); err != nil {
		t.Errorf("已过账应可作废，得到 %v", err)
	}

	// 作废
	if err := v.Void("李四", now, 99); err != nil {
		t.Fatalf("作废失败: %v", err)
	}
	if v.Status != StatusVoided {
		t.Errorf("状态 = %s，期望 voided", v.Status)
	}
	if v.VoidedBy == nil || *v.VoidedBy != 99 {
		t.Error("未记录冲销凭证 ID")
	}
	// 已作废：全部拒绝
	if err := v.CanPost(); !errors.Is(err, ErrAlreadyVoided) {
		t.Errorf("已作废不可过账，得到 %v", err)
	}
	if err := v.CanVoid(); !errors.Is(err, ErrAlreadyVoided) {
		t.Errorf("已作废不可重复作废，得到 %v", err)
	}
}

func TestPostRequiresPoster(t *testing.T) {
	v := sampleVoucher(t)
	if err := v.Post("", time.Now()); !errors.Is(err, ErrMissingPoster) {
		t.Errorf("缺记账人应报错，得到 %v", err)
	}
}

// 已过账凭证不得再改分录 —— 这是「账不能事后改」的底线
func TestAddEntryBlockedAfterPost(t *testing.T) {
	v := sampleVoucher(t)
	if err := v.Post("李四", time.Now()); err != nil {
		t.Fatal(err)
	}
	err := v.AddEntry(ledger.Entry{AccountCode: "1002", Summary: "x", Debit: amt(1)})
	if !errors.Is(err, ErrNotDraft) {
		t.Fatalf("已过账不得追加分录，得到 %v", err)
	}
	if len(v.Entries) != 2 {
		t.Errorf("分录数应保持 2，实际 %d", len(v.Entries))
	}
}

// ---------------------------------------------------------------------------
// 红字冲销
// ---------------------------------------------------------------------------

func TestBuildReversal(t *testing.T) {
	ctx := testCtx(t)
	v := sampleVoucher(t)
	if err := v.Post("李四", time.Now()); err != nil {
		t.Fatal(err)
	}
	v.ID = 7

	rev, err := v.BuildReversal("李四", v.BizDate)
	if err != nil {
		t.Fatalf("构造冲销凭证失败: %v", err)
	}

	// 借贷互换，金额不变
	if rev.Entries[0].Credit != amt(50000) || !rev.Entries[0].Debit.IsZero() {
		t.Errorf("第 1 行应变为贷方 50000，实际 借%s 贷%s", rev.Entries[0].Debit, rev.Entries[0].Credit)
	}
	if rev.Entries[1].Debit != amt(50000) {
		t.Errorf("第 2 行应变为借方 50000，实际 %s", rev.Entries[1].Debit)
	}
	// 辅助核算保留
	if rev.Entries[1].Aux.ContactID == nil || *rev.Entries[1].Aux.ContactID != 3 {
		t.Error("冲销应保留辅助核算")
	}
	// 摘要带「冲销」前缀
	for _, e := range rev.Entries {
		if !strings.HasPrefix(e.Summary, "冲销") {
			t.Errorf("摘要应带冲销前缀，实际 %q", e.Summary)
		}
	}
	// 关联原凭证
	if rev.ReversesID == nil || *rev.ReversesID != 7 {
		t.Error("应记录被冲销的原凭证 ID")
	}
	if !strings.Contains(rev.Remark, v.No) && v.No != "" {
		t.Errorf("备注应含原凭证号，实际 %q", rev.Remark)
	}
	// 冲销凭证本身必须平衡且能通过校验
	if !rev.IsBalanced() {
		t.Error("冲销凭证应平衡")
	}
	if _, err := rev.Validate(ctx); err != nil {
		t.Errorf("冲销凭证应通过校验，得到 %v", err)
	}
	// 冲销凭证是草稿
	if rev.Status != StatusDraft {
		t.Errorf("冲销凭证应为草稿，实际 %s", rev.Status)
	}
}

func TestBuildReversalOnDraftFails(t *testing.T) {
	v := sampleVoucher(t)
	if _, err := v.BuildReversal("李四", v.BizDate); !errors.Is(err, ErrNotPosted) {
		t.Errorf("草稿不可冲销，得到 %v", err)
	}
}

func TestReversalSummaryNotDoublePrefixed(t *testing.T) {
	v, _ := New(WordJi, d0911, "张三")
	_ = v.AddEntries(
		ledger.Entry{AccountCode: "1002", Summary: "冲销 收到借款", Debit: amt(100)},
		ledger.Entry{AccountCode: "5001", Summary: "", Credit: amt(100)},
	)
	_ = v.Post("李四", time.Now())
	rev, err := v.BuildReversal("李四", v.BizDate)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Entries[0].Summary != "冲销 收到借款" {
		t.Errorf("不应重复加前缀，实际 %q", rev.Entries[0].Summary)
	}
	if rev.Entries[1].Summary != "冲销" {
		t.Errorf("空摘要应变为「冲销」，实际 %q", rev.Entries[1].Summary)
	}
}

// 冲销凭证可以用不同的业务日期（原期间已结账时用当前开放期间）
func TestBuildReversalIntoAnotherPeriod(t *testing.T) {
	ctx := testCtx(t)
	v := sampleVoucher(t)
	_ = v.Post("李四", time.Now())

	rev, err := v.BuildReversal("李四", calendar.MustParse("2025-10-05"))
	if err != nil {
		t.Fatal(err)
	}
	if rev.Period != (period.NewKey(2025, 10)) {
		t.Errorf("冲销凭证期间 = %v，期望 2025-10", rev.Period)
	}
	// 但 2025-10 尚未启用，校验应被期间拦住
	if _, err := rev.Validate(ctx); !errors.Is(err, period.ErrNotOpen) {
		t.Errorf("未启用期间应报 ErrNotOpen，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 凭证字号连续性检查
// ---------------------------------------------------------------------------

func TestCheckSequenceOK(t *testing.T) {
	var vs []*Voucher
	for i := 1; i <= 5; i++ {
		v, _ := New(WordJi, d0911, "张三")
		v.Seq, v.No = i, FormatNo(WordJi, v.Period, i)
		vs = append(vs, v)
	}
	if issues := CheckSequence(vs); len(issues) != 0 {
		t.Errorf("连续编号不应有问题，得到 %v", issues)
	}
}

func TestCheckSequenceGap(t *testing.T) {
	var vs []*Voucher
	for _, i := range []int{1, 2, 4, 5} { // 缺 3
		v, _ := New(WordJi, d0911, "张三")
		v.Seq = i
		vs = append(vs, v)
	}
	issues := CheckSequence(vs)
	if len(issues) != 1 {
		t.Fatalf("应检出 1 处断号，得到 %v", issues)
	}
	if issues[0].Kind != "gap" || !strings.Contains(issues[0].Detail, "断号") {
		t.Errorf("问题 = %+v", issues[0])
	}
}

func TestCheckSequenceDuplicate(t *testing.T) {
	var vs []*Voucher
	for _, i := range []int{1, 2, 2, 3} { // 3 重复
		v, _ := New(WordJi, d0911, "张三")
		v.Seq = i
		vs = append(vs, v)
	}
	issues := CheckSequence(vs)
	var dup int
	for _, is := range issues {
		if is.Kind == "duplicate" {
			dup++
		}
	}
	if dup != 1 {
		t.Errorf("应检出 1 处重号，得到 %v", issues)
	}
}

func TestCheckSequenceMustStartAtOne(t *testing.T) {
	v, _ := New(WordJi, d0911, "张三")
	v.Seq = 3
	issues := CheckSequence([]*Voucher{v})
	if len(issues) != 1 || !strings.Contains(issues[0].Detail, "从 1 开始") {
		t.Errorf("应从 1 开始，得到 %v", issues)
	}
}

// 不同期间 / 不同凭证字分别编号，互不影响
func TestCheckSequenceSeparatesGroups(t *testing.T) {
	mk := func(w Word, date calendar.Date, seq int) *Voucher {
		v, _ := New(w, date, "张三")
		v.Seq = seq
		return v
	}
	vs := []*Voucher{
		mk(WordJi, d0911, 1),
		mk(WordJi, d0911, 2),
		mk(WordShou, d0911, 1), // 另一凭证字，从 1 开始，不应算断号
		mk(WordJi, d0901, 1),   // 另一日期同期间，仍属同组 → 与上面的 1、2 合并检查
	}
	issues := CheckSequence(vs)
	// 记-2025-09 组内 seq = {1,2} ∪ {1} = {1,1,2} → 1 处重号
	var dup int
	for _, is := range issues {
		if is.Kind == "duplicate" {
			dup++
		}
	}
	if dup != 1 {
		t.Errorf("跨日同期间应合并检查并检出重号，得到 %v", issues)
	}
}

// ---------------------------------------------------------------------------
// 来源关联（流水↔凭证、报销↔凭证）
// ---------------------------------------------------------------------------

func TestSetSource(t *testing.T) {
	v := sampleVoucher(t)
	if err := v.SetSource(SourceBank, i64(123)); err != nil {
		t.Fatal(err)
	}
	if v.Source != SourceBank || v.SourceID == nil || *v.SourceID != 123 {
		t.Errorf("来源 = %s/%v", v.Source, v.SourceID)
	}
	// 过账后不可再改来源
	_ = v.Post("李四", time.Now())
	if err := v.SetSource(SourceManual, nil); !errors.Is(err, ErrNotDraft) {
		t.Errorf("过账后不可改来源，得到 %v", err)
	}
}

func TestSourceLabels(t *testing.T) {
	cases := map[Source]string{
		SourceManual: "手工", SourceBank: "银行流水", SourceSalary: "工资",
		SourceExpense: "报销", SourceInvoice: "发票", SourceClosing: "期末结转",
		SourceOpening: "期初余额", SourceAI: "AI 建议",
	}
	for s, want := range cases {
		if got := s.Label(); got != want {
			t.Errorf("%s.Label() = %q，期望 %q", s, got, want)
		}
	}
}
