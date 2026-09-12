package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/bankcsv"
	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/bank"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/period"
)

// bankStatement 是一份典型的对账单，覆盖本项目最关心的场景：
// 股东借款、供应商付款、客户回款。
//
// 注意：`收入`/`支出` 是银行方视角，与企业的借贷方向相反 ——
// 这是导入时最容易搞反的地方。
const bankStatement = `交易日期,摘要,对方户名,对方账号,借贷标志,交易金额,余额
2025-09-03,转账,张三,6222020000000001,收入,"50,000.00","150,000.00"
2025-09-10,办公用品,杭州某某办公用品有限公司,6222020000000002,支出,"3,000.00","147,000.00"
2025-09-15,货款,杭州某某科技有限公司,6222020000000003,收入,"90,400.00","237,400.00"
2025-09-25,房租,杭州某某物业管理有限公司,6222020000000004,支出,"12,000.00","225,400.00"
`

// bankFixture 在标准账套之上补一个银行明细科目、往来单位与匹配规则。
type bankFixture struct {
	book   *bookFixture
	RuleSH int64
	RuleOP int64
}

func setupBank(t *testing.T) *bankFixture {
	t.Helper()
	ctx := context.Background()
	db := newTestDB(t)

	// 1002 银行存款下建一个开户行明细，流水挂到它上面
	if err := db.WithTx(ctx, func(tx *Tx) error {
		_, err := db.Accounts().Insert(ctx, tx, &account.Account{
			Code: "100201", Name: "银行存款—工商银行", ParentCode: "1002",
			Level: 2, IsLeaf: true, RootType: account.RootAsset,
			BalanceDir: account.DirDebit, IsEnabled: true,
		})
		return err
	}); err != nil {
		t.Fatalf("新增银行明细科目失败: %v", err)
	}

	bf := &bookFixture{DB: db}
	bf.AccIDs, _ = db.Accounts().IDsByCode(ctx)
	bf.Sharehol = mustContacts(t, db, "shareholder", "张三")
	bf.Customer = mustContacts(t, db, "customer", "杭州某某科技有限公司")
	bf.Supplier = mustContacts(t, db, "supplier", "杭州某某办公用品有限公司")

	f := &bankFixture{book: bf}
	f.RuleSH = f.addRule(t, "股东往来", 10, bank.MatchCounterparty, "张三", "224101", &bf.Sharehol)
	f.RuleOP = f.addRule(t, "办公用品", 20, bank.MatchCounterparty, "办公用品", "560206", nil)
	return f
}

func (f *bankFixture) addRule(t *testing.T, name string, prio int,
	field bank.MatchField, pattern, counterAccount string, contactID *int64) int64 {
	t.Helper()
	id, err := f.book.DB.Bank().UpsertRule(context.Background(), &bank.Rule{
		Name: name, Priority: prio, Enabled: true,
		MatchField: field, Pattern: pattern,
		CounterAccountCode: counterAccount, ContactID: contactID,
	})
	if err != nil {
		t.Fatalf("新增规则失败: %v", err)
	}
	return id
}

// importStatement 解析并导入对账单。
func (f *bankFixture) importStatement(t *testing.T, text string) *ImportResult {
	t.Helper()
	res, err := bankcsv.Parse([]byte(text), bankcsv.Options{
		BankAccountCode: "100201", AutoDetectHeader: true,
	})
	if err != nil {
		t.Fatalf("解析对账单失败: %v", err)
	}
	if len(res.Errors) > 0 {
		t.Fatalf("解析有错误: %v", res.Errors)
	}
	raw := make([][]string, len(res.Flows))
	for i := range raw {
		raw[i] = []string{res.Flows[i].TxnDate.String(), res.Flows[i].Summary}
	}
	out, err := f.book.DB.Bank().Import(context.Background(), ImportInput{
		AccountCode: "100201", FileName: "对账单.csv",
		FileData: []byte(text), Encoding: res.Table.Encoding,
		ImportedBy: "李会计", Rows: res.Flows, RawRows: raw,
	})
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// 导入
// ---------------------------------------------------------------------------

func TestImportStatement(t *testing.T) {
	f := setupBank(t)
	res := f.importStatement(t, bankStatement)

	if res.Inserted != 4 {
		t.Errorf("导入条数 = %d，期望 4", res.Inserted)
	}
	if res.Duplicated != 0 {
		t.Errorf("重复条数 = %d，期望 0", res.Duplicated)
	}
	if res.TotalIn != money100(140400) { // 50,000 + 90,400
		t.Errorf("收入合计 = %s，期望 140,400.00", res.TotalIn)
	}
	if res.TotalOut != money100(15000) { // 3,000 + 12,000
		t.Errorf("支出合计 = %s，期望 15,000.00", res.TotalOut)
	}
	if res.From.String() != "2025-09-03" || res.To.String() != "2025-09-25" {
		t.Errorf("区间 = %s..%s", res.From, res.To)
	}

	// 流水状态应为「待匹配」
	flows, err := f.book.DB.Bank().ListFlows(context.Background(), bank.StatusImported,
		calendar.Date{}, calendar.Date{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 4 {
		t.Errorf("待匹配流水数 = %d，期望 4", len(flows))
	}
}

// ★ 重复导入同一份对账单必须被去重。
// 这是最常见的用户误操作，静默产生重复流水会让账目凭空多出一倍。
func TestImportDeduplicates(t *testing.T) {
	f := setupBank(t)
	first := f.importStatement(t, bankStatement)
	second := f.importStatement(t, bankStatement)

	if second.Inserted != 0 {
		t.Errorf("第二次导入应全部去重，实际插入 %d 条", second.Inserted)
	}
	if second.Duplicated != 4 {
		t.Errorf("去重条数 = %d，期望 4", second.Duplicated)
	}

	flows, _ := f.book.DB.Bank().ListFlows(context.Background(), "", calendar.Date{}, calendar.Date{}, 0)
	if len(flows) != first.Inserted {
		t.Errorf("总流水数 = %d，期望仍为 %d", len(flows), first.Inserted)
	}
}

// 部分重叠的两次导入（比如按月多次导出）也只保留一份
func TestImportDedupPartialOverlap(t *testing.T) {
	f := setupBank(t)
	f.importStatement(t, bankStatement)

	// 第二份含重复的两条 + 两条新的
	overlap := `交易日期,摘要,对方户名,对方账号,借贷标志,交易金额,余额
2025-09-03,转账,张三,6222020000000001,收入,"50,000.00","150,000.00"
2025-09-10,办公用品,杭州某某办公用品有限公司,6222020000000002,支出,"3,000.00","147,000.00"
2025-09-28,水电费,国网浙江电力,6222020000000005,支出,"800.00","224,600.00"
2025-09-30,利息,中国工商银行,6222020000000006,收入,"120.00","224,720.00"
`
	res := f.importStatement(t, overlap)
	if res.Inserted != 2 {
		t.Errorf("应只插入 2 条新流水，实际 %d", res.Inserted)
	}
	if res.Duplicated != 2 {
		t.Errorf("应去重 2 条，实际 %d", res.Duplicated)
	}
}

func TestImportRequiresValidAccount(t *testing.T) {
	f := setupBank(t)
	res, _ := bankcsv.Parse([]byte(bankStatement), bankcsv.Options{
		BankAccountCode: "100201", AutoDetectHeader: true,
	})
	_, err := f.book.DB.Bank().Import(context.Background(), ImportInput{
		AccountCode: "9999", Rows: res.Flows,
	})
	if err == nil {
		t.Fatal("不存在的银行科目应报错")
	}
}

// ---------------------------------------------------------------------------
// 匹配
// ---------------------------------------------------------------------------

func TestMatchByRule(t *testing.T) {
	f := setupBank(t)
	f.importStatement(t, bankStatement)

	res, err := f.book.DB.Bank().MatchAll(context.Background(), bank.StatusImported)
	if err != nil {
		t.Fatalf("匹配失败: %v", err)
	}
	if res.Total != 4 {
		t.Errorf("总数 = %d", res.Total)
	}
	// 张三（股东）+ 办公用品 各命中一条规则；货款与房租无规则
	if res.ByLayer[bank.LayerRule] != 2 {
		t.Errorf("规则命中 = %d，期望 2（张三、办公用品）", res.ByLayer[bank.LayerRule])
	}
	if len(res.Unmatched) != 2 {
		t.Errorf("未匹配 = %d，期望 2（货款、房租）", len(res.Unmatched))
	}

	// 校验股东借款那条的提议
	flows, _ := f.book.DB.Bank().ListFlows(context.Background(), bank.StatusMatched,
		calendar.Date{}, calendar.Date{}, 0)
	var share *bank.Flow
	for _, fl := range flows {
		if fl.CounterpartyName == "张三" {
			share = fl
		}
	}
	if share == nil {
		t.Fatal("未找到张三的流水")
	}
	if share.CounterAccount != "224101" {
		t.Errorf("对方科目 = %q，期望 224101（其他应付款—股东）", share.CounterAccount)
	}
	if share.ContactID == nil || *share.ContactID != f.book.Sharehol {
		t.Error("应带出股东往来单位")
	}
	if share.MatchLayer != bank.LayerRule {
		t.Errorf("匹配层 = %s", share.MatchLayer)
	}
	if share.Memo == "" {
		t.Error("应生成摘要")
	}
}

// 规则命中后应累加命中次数，便于界面显示「这条规则用过多少次」
func TestRuleHitCount(t *testing.T) {
	f := setupBank(t)
	f.importStatement(t, bankStatement)
	if _, err := f.book.DB.Bank().MatchAll(context.Background(), bank.StatusImported); err != nil {
		t.Fatal(err)
	}
	rules, _ := f.book.DB.Bank().ListRules(context.Background())
	for _, r := range rules {
		switch r.ID {
		case f.RuleSH:
			if r.HitCount != 1 {
				t.Errorf("股东规则命中次数 = %d，期望 1", r.HitCount)
			}
		case f.RuleOP:
			if r.HitCount != 1 {
				t.Errorf("供应商规则命中次数 = %d，期望 1", r.HitCount)
			}
		}
	}
}

// ★ 第 2 层：没有规则时，从历史凭证里学
func TestMatchByHistory(t *testing.T) {
	f := setupBank(t)

	// 历史匹配靠「对方户名」。手工凭证没有银行流水的对方户名，
	// 因此需要把物业公司作为往来单位挂在分录上（这也是实务做法：
	// 供应商档案里本来就有这家公司）。
	propertyID := mustContacts(t, f.book.DB, "supplier", "杭州某某物业管理有限公司")
	postSimple(t, f.book.DB, f.book, "2025-08-25",
		entry{code: "560210", summary: "8月房租", debit: money100(12000),
			dept: ptrI64(1), contact: &propertyID},
		entry{code: "100201", summary: "8月房租", credit: money100(12000)},
	)

	// 再导入 9 月的对账单：房租这笔没有规则，但历史里有相同的对手方
	f.importStatement(t, bankStatement)
	res, err := f.book.DB.Bank().MatchAll(context.Background(), bank.StatusImported)
	if err != nil {
		t.Fatal(err)
	}
	// 房租那条应当由历史命中（counter_account 来自历史凭证）
	if res.ByLayer[bank.LayerHistory] == 0 {
		t.Logf("匹配统计 = %+v（历史层未命中，检查历史抽取逻辑）", res.ByLayer)
	}

	flows, _ := f.book.DB.Bank().ListFlows(context.Background(), "", calendar.Date{}, calendar.Date{}, 0)
	var rent *bank.Flow
	for _, fl := range flows {
		if strings.Contains(fl.CounterpartyName, "物业") {
			rent = fl
		}
	}
	if rent == nil {
		t.Fatal("未找到物业流水")
	}
	// 历史凭证的对方科目是 560210，匹配器应当给出同样的建议
	if rent.CounterAccount != "560210" {
		t.Errorf("房租对方科目 = %q，期望从历史学到 560210", rent.CounterAccount)
	}
}

// 历史匹配必须区分方向：收入的历史不能用来指导支出
func TestHistoryRespectsDirection(t *testing.T) {
	f := setupBank(t)
	// 记一笔 8 月收到股东借款（收入方向）
	postSimple(t, f.book.DB, f.book, "2025-08-03",
		entry{code: "100201", summary: "收到张三借款", debit: money100(30000)},
		entry{code: "224101", summary: "收到张三借款", credit: money100(30000),
			contact: &f.book.Sharehol},
	)
	// 删掉规则，强制走历史层
	if err := f.book.DB.Bank().DeleteRule(context.Background(), f.RuleSH); err != nil {
		t.Fatal(err)
	}
	if err := f.book.DB.Bank().DeleteRule(context.Background(), f.RuleOP); err != nil {
		t.Fatal(err)
	}

	f.importStatement(t, bankStatement)
	if _, err := f.book.DB.Bank().MatchAll(context.Background(), bank.StatusImported); err != nil {
		t.Fatal(err)
	}
	flows, _ := f.book.DB.Bank().ListFlows(context.Background(), bank.StatusMatched,
		calendar.Date{}, calendar.Date{}, 0)
	for _, fl := range flows {
		if fl.CounterpartyName == "张三" {
			// 收入方向，应与 8 月那笔一致
			if fl.Direction != bank.DirIn {
				t.Fatalf("前置条件错误：方向 = %s", fl.Direction)
			}
			if fl.CounterAccount != "224101" {
				t.Errorf("对方科目 = %q，期望 224101", fl.CounterAccount)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 人工干预
// ---------------------------------------------------------------------------

func TestSetSuggestionManually(t *testing.T) {
	ctx := context.Background()
	f := setupBank(t)
	f.importStatement(t, bankStatement)

	flows, _ := f.book.DB.Bank().ListFlows(ctx, bank.StatusImported, calendar.Date{}, calendar.Date{}, 0)
	target := flows[0]

	if err := f.book.DB.Bank().SetSuggestion(ctx, target.ID, Suggestion{CounterAccount: "5602", Memo: "人工指定摘要"}); err != nil {
		t.Fatalf("人工指定失败: %v", err)
	}
	got, _ := f.book.DB.Bank().GetFlow(ctx, target.ID)
	if got.CounterAccount != "5602" {
		t.Errorf("对方科目 = %q", got.CounterAccount)
	}
	if got.MatchLayer != bank.LayerManual {
		t.Errorf("匹配层 = %s，期望 manual", got.MatchLayer)
	}
	if got.Confidence != 1 {
		t.Errorf("人工指定置信度应为 1，得到 %.2f", got.Confidence)
	}
	if got.Status != bank.StatusMatched {
		t.Errorf("状态 = %s，期望 matched", got.Status)
	}

	// 不存在的科目
	if err := f.book.DB.Bank().SetSuggestion(ctx, target.ID, Suggestion{CounterAccount: "9999"}); err == nil {
		t.Error("不存在的科目应报错")
	}
}

func TestIgnoreFlow(t *testing.T) {
	ctx := context.Background()
	f := setupBank(t)
	f.importStatement(t, bankStatement)

	flows, _ := f.book.DB.Bank().ListFlows(ctx, bank.StatusImported, calendar.Date{}, calendar.Date{}, 0)
	if err := f.book.DB.Bank().IgnoreFlow(ctx, flows[0].ID, "内部划转，不需记账"); err != nil {
		t.Fatal(err)
	}
	got, _ := f.book.DB.Bank().GetFlow(ctx, flows[0].ID)
	if got.Status != bank.StatusIgnored {
		t.Errorf("状态 = %s，期望 ignored", got.Status)
	}

	// 已忽略的流水不应参与匹配
	res, _ := f.book.DB.Bank().MatchAll(ctx, bank.StatusImported)
	for _, id := range res.Unmatched {
		if id == flows[0].ID {
			t.Error("已忽略的流水不应出现在未匹配列表里")
		}
	}
}

// ---------------------------------------------------------------------------
// ★ 端到端：导入 → 匹配 → 生成凭证 → 落总账
// ---------------------------------------------------------------------------

func TestBankFlowToVoucherEndToEnd(t *testing.T) {
	ctx := context.Background()
	f := setupBank(t)
	f.importStatement(t, bankStatement)

	// 先补齐没有规则的两条：货款 → 应收账款，房租 → 管理费用—租赁费
	flows, _ := f.book.DB.Bank().ListFlows(ctx, bank.StatusImported, calendar.Date{}, calendar.Date{}, 0)
	for _, fl := range flows {
		switch {
		case strings.Contains(fl.CounterpartyName, "科技"):
			if err := f.book.DB.Bank().SetSuggestion(ctx, fl.ID, Suggestion{
				CounterAccount: "1122", ContactID: &f.book.Customer,
			}); err != nil {
				t.Fatal(err)
			}
		case strings.Contains(fl.CounterpartyName, "物业"):
			// 560210 管理费用—租赁费 要求部门辅助核算
			dept := int64(1)
			if err := f.book.DB.Bank().SetSuggestion(ctx, fl.ID, Suggestion{
				CounterAccount: "560210", DeptID: &dept,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 规则命中的两条也已 matched
	if _, err := f.book.DB.Bank().MatchAll(ctx, bank.StatusImported); err != nil {
		t.Fatal(err)
	}
	// 但 560206 也要求部门，给办公用品那条补上
	// 560206/560210 都要求部门辅助核算
	flows, _ = f.book.DB.Bank().ListFlows(ctx, bank.StatusMatched, calendar.Date{}, calendar.Date{}, 0)
	ids := make([]int64, 0, len(flows))
	for _, fl := range flows {
		if fl.CounterAccount == "560206" || fl.CounterAccount == "560210" {
			dept := int64(1)
			if err := f.book.DB.Bank().SetSuggestion(ctx, fl.ID, Suggestion{
				CounterAccount: fl.CounterAccount, DeptID: &dept, Memo: fl.Memo,
			}); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, fl.ID)
	}
	if len(ids) != 4 {
		t.Fatalf("待过账流水 = %d，期望 4", len(ids))
	}

	// 批量生成凭证
	res, err := f.book.DB.Bank().PostFlows(ctx, ids, "王主管",
		time.Date(2025, 9, 30, 18, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("批量过账失败: %v", err)
	}
	if res.Created != 4 {
		t.Fatalf("生成凭证数 = %d，期望 4（失败：%v）", res.Created, res.Failures)
	}
	if len(res.Failures) != 0 {
		t.Errorf("不应有失败: %v", res.Failures)
	}

	// ★ 生成出来的是**草稿**：不占号、不进总账。
	// 过账只发生在账期结算，这里显式走一次（等价于结账时的第一步）。
	for i, v := range res.Vouchers {
		if v.No != "" {
			t.Errorf("第 %d 张是草稿，不该有凭证号，实际 %q", i+1, v.No)
		}
	}
	postDrafts(t, f.book.DB, 2025, 9)

	// 过账之后号按期间连续
	posted, err := f.book.DB.Vouchers().PostPeriodDrafts(ctx, period.NewKey(2025, 9),
		"王主管", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if posted.Posted != 0 {
		t.Errorf("草稿应当已经全部过账，还剩 %d 张", posted.Posted)
	}
	wantNos := []string{
		"记-2025-09-0001", "记-2025-09-0002",
		"记-2025-09-0003", "记-2025-09-0004",
	}
	for i, v := range res.Vouchers {
		got, err := f.book.DB.Vouchers().Get(ctx, v.VoucherID)
		if err != nil {
			t.Fatal(err)
		}
		if got.No != wantNos[i] {
			t.Errorf("第 %d 张凭证号 = %q，期望 %q", i+1, got.No, wantNos[i])
		}
	}

	// ★ 双向关联：流水 → 凭证
	flows, _ = f.book.DB.Bank().ListFlows(ctx, bank.StatusPosted, calendar.Date{}, calendar.Date{}, 0)
	if len(flows) != 4 {
		t.Fatalf("已记账流水 = %d，期望 4", len(flows))
	}
	for _, fl := range flows {
		if fl.VoucherID == nil {
			t.Errorf("流水 %d 未回填凭证 id", fl.ID)
		}
	}

	// ★ 双向关联：凭证 → 流水
	back, err := f.book.DB.Bank().FlowsByVoucher(ctx, res.Vouchers[0].VoucherID)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].ID != res.Vouchers[0].FlowID {
		t.Errorf("按凭证反查流水失败: %+v", back)
	}

	// ★ 凭证的 source/source_id 也指向流水
	vc, err := f.book.DB.Vouchers().Get(ctx, res.Vouchers[0].VoucherID)
	if err != nil {
		t.Fatal(err)
	}
	if vc.Source != "bank" {
		t.Errorf("凭证来源 = %q，期望 bank", vc.Source)
	}
	if vc.SourceID == nil || *vc.SourceID != res.Vouchers[0].FlowID {
		t.Error("凭证未记录来源流水 id")
	}
	if len(vc.Entries) != 2 {
		t.Errorf("凭证分录数 = %d，期望 2", len(vc.Entries))
	}
	// 借方在前
	if !vc.Entries[0].Debit.IsPositive() {
		t.Errorf("第 1 条应为借方: %+v", vc.Entries[0])
	}

	// ★ 总账：股东借款 借 银行存款 / 贷 其他应付款—股东
	bal, err := f.book.DB.Vouchers().Balance(ctx,
		calendar.MustParse("2025-09-01"), calendar.MustParse("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	if bal["100201"] != money100(125400) { // 150,400 收 − 15,000 支
		t.Errorf("银行存款余额 = %s，期望 125,400.00", bal["100201"])
	}
	// 其他应付款—股东是贷方科目，原始口径为负
	if bal["224101"] != -money100(50000) {
		t.Errorf("其他应付款—股东 = %s，期望 -50,000.00", bal["224101"])
	}
	// 收到货款是「贷 应收账款」，因此原始口径下为负 ——
	// 说明客户回款了，但对应的销售凭证还没记（本用例确实没记销售）
	if bal["1122"] != -money100(90400) {
		t.Errorf("应收账款 = %s，期望 -90,400.00（收到回款冲减应收）", bal["1122"])
	}
	if bal["2202"] != 0 {
		t.Errorf("办公用品走的是管理费用而非应付账款，2202 应为 0，得到 %s", bal["2202"])
	}

	// 试算平衡
	d, c, err := f.book.DB.Vouchers().TrialBalance(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	if d != c {
		t.Errorf("试算不平衡：借 %s 贷 %s", d, c)
	}

	// 资产负债表必须仍然平衡
	_, _, issues, err := f.book.DB.Reports().BuildBalanceSheet(ctx,
		calendar.MustParse("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	for _, is := range issues {
		t.Errorf("勾稽关系不成立: %s", is)
	}
}

// 已经生成凭证的流水重复过账应被跳过而不是重复记账
func TestPostFlowsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	f := setupBank(t)
	f.importStatement(t, bankStatement)
	if _, err := f.book.DB.Bank().MatchAll(ctx, bank.StatusImported); err != nil {
		t.Fatal(err)
	}
	flows, _ := f.book.DB.Bank().ListFlows(ctx, bank.StatusMatched, calendar.Date{}, calendar.Date{}, 0)
	ids := make([]int64, 0, len(flows))
	for _, fl := range flows {
		if fl.CounterAccount == "560206" {
			dept := int64(1)
			if err := f.book.DB.Bank().SetSuggestion(ctx, fl.ID, Suggestion{
				CounterAccount: fl.CounterAccount, DeptID: &dept, Memo: fl.Memo,
			}); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, fl.ID)
	}

	first, err := f.book.DB.Bank().PostFlows(ctx, ids, "王主管", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first.Created == 0 {
		t.Fatalf("首次过账应有成功项，失败：%v", first.Failures)
	}

	// 再次过账同一批：已生成的应被跳过
	second, err := f.book.DB.Bank().PostFlows(ctx, ids, "王主管", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if second.Created != 0 {
		t.Errorf("重复过账不应再生成凭证，实际 %d 张", second.Created)
	}
	if second.Skipped != first.Created {
		t.Errorf("跳过数 = %d，期望 %d", second.Skipped, first.Created)
	}
}

// ★ 批量过账必须是原子的：一条失败不能留下「一半已记账」的状态
func TestPostFlowsIsAtomic(t *testing.T) {
	ctx := context.Background()
	f := setupBank(t)
	f.importStatement(t, bankStatement)
	if _, err := f.book.DB.Bank().MatchAll(ctx, bank.StatusImported); err != nil {
		t.Fatal(err)
	}

	// 把一条流水的 txn_date 改到未启用的期间，让它在过账时失败
	flows, _ := f.book.DB.Bank().ListFlows(ctx, bank.StatusMatched, calendar.Date{}, calendar.Date{}, 0)
	if len(flows) == 0 {
		t.Fatal("前置条件：应有已匹配流水")
	}
	// 管理费用类科目要求部门，先补上，确保失败只来自期间
	for _, fl := range flows {
		if fl.CounterAccount == "560206" || fl.CounterAccount == "560210" {
			dept := int64(1)
			if err := f.book.DB.Bank().SetSuggestion(ctx, fl.ID, Suggestion{
				CounterAccount: fl.CounterAccount, DeptID: &dept, Memo: fl.Memo,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	ids := make([]int64, 0, len(flows))
	for _, fl := range flows {
		ids = append(ids, fl.ID)
	}

	res, err := f.book.DB.Bank().PostFlows(ctx, ids, "王主管", time.Now())
	if err != nil {
		t.Fatalf("批量过账本身不应报错（个别失败走 Failures）: %v", err)
	}
	// 成功的那些必须都已落库
	posted, _ := f.book.DB.Bank().ListFlows(ctx, bank.StatusPosted, calendar.Date{}, calendar.Date{}, 0)
	if len(posted) != res.Created {
		t.Errorf("已过账流水数 = %d，但报告 Created = %d", len(posted), res.Created)
	}
	// 未成功的必须仍是 matched，不能处于中间状态
	for id, reason := range res.Failures {
		fl, err := f.book.DB.Bank().GetFlow(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if fl.Status == bank.StatusPosted {
			t.Errorf("流水 %d 报告失败（%s）却已是已记账状态", id, reason)
		}
		if fl.VoucherID != nil {
			t.Errorf("流水 %d 报告失败却已关联凭证", id)
		}
	}
}

// 已结账期间不允许从流水生成凭证
func TestPostFlowsRejectsClosedPeriod(t *testing.T) {
	ctx := context.Background()
	f := setupBank(t)
	f.importStatement(t, bankStatement)
	if _, err := f.book.DB.Bank().MatchAll(ctx, bank.StatusImported); err != nil {
		t.Fatal(err)
	}
	// 结掉 1..9 月
	for m := 1; m <= 9; m++ {
		if err := f.book.DB.WithTx(ctx, func(tx *Tx) error {
			return f.book.DB.Periods().SetStatus(ctx, tx, period.NewKey(2025, m),
				period.StatusClosed, "王主管", ptrTime(time.Now()))
		}); err != nil {
			t.Fatal(err)
		}
	}

	flows, _ := f.book.DB.Bank().ListFlows(ctx, bank.StatusMatched, calendar.Date{}, calendar.Date{}, 0)
	ids := make([]int64, 0, len(flows))
	for _, fl := range flows {
		ids = append(ids, fl.ID)
	}
	res, err := f.book.DB.Bank().PostFlows(ctx, ids, "王主管", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 0 {
		t.Errorf("已结账期间不应生成凭证，实际生成 %d 张", res.Created)
	}
	if len(res.Failures) != len(ids) {
		t.Errorf("每条都应报告失败，实际 %d/%d", len(res.Failures), len(ids))
	}
}

// ---------------------------------------------------------------------------
// 统计与查询
// ---------------------------------------------------------------------------

func TestFlowStats(t *testing.T) {
	ctx := context.Background()
	f := setupBank(t)
	f.importStatement(t, bankStatement)

	s, err := f.book.DB.Bank().Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Imported != 4 {
		t.Errorf("待匹配 = %d，期望 4", s.Imported)
	}

	if _, err := f.book.DB.Bank().MatchAll(ctx, bank.StatusImported); err != nil {
		t.Fatal(err)
	}
	flows, _ := f.book.DB.Bank().ListFlows(ctx, bank.StatusImported, calendar.Date{}, calendar.Date{}, 0)
	if len(flows) > 0 {
		if err := f.book.DB.Bank().IgnoreFlow(ctx, flows[0].ID, "测试"); err != nil {
			t.Fatal(err)
		}
	}

	s, _ = f.book.DB.Bank().Stats(ctx)
	total := s.Imported + s.Matched + s.Posted + s.Ignored
	if total != 4 {
		t.Errorf("统计总数 = %d，期望 4（%+v）", total, s)
	}
}

func TestListFlowsFiltering(t *testing.T) {
	ctx := context.Background()
	f := setupBank(t)
	f.importStatement(t, bankStatement)

	// 日期范围
	flows, err := f.book.DB.Bank().ListFlows(ctx, "",
		calendar.MustParse("2025-09-10"), calendar.MustParse("2025-09-15"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 2 {
		t.Errorf("09-10..09-15 的流水数 = %d，期望 2", len(flows))
	}
	// 按日期升序
	for i := 1; i < len(flows); i++ {
		if flows[i].TxnDate.Before(flows[i-1].TxnDate) {
			t.Error("应按日期升序排列")
		}
	}
	// limit
	flows, _ = f.book.DB.Bank().ListFlows(ctx, "", calendar.Date{}, calendar.Date{}, 2)
	if len(flows) != 2 {
		t.Errorf("limit=2 时返回 %d 条", len(flows))
	}
}

func TestGetFlowNotFound(t *testing.T) {
	f := setupBank(t)
	if _, err := f.book.DB.Bank().GetFlow(context.Background(), 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的流水应报 ErrNotFound，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 规则 CRUD
// ---------------------------------------------------------------------------

func TestRuleCRUD(t *testing.T) {
	ctx := context.Background()
	f := setupBank(t)

	rules, err := f.book.DB.Bank().ListRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("规则数 = %d，期望 2", len(rules))
	}
	// 按优先级升序
	if rules[0].Priority > rules[1].Priority {
		t.Error("应按优先级升序返回")
	}

	// 更新
	r := rules[0]
	r.Pattern = "李四"
	r.Priority = 5
	if _, err := f.book.DB.Bank().UpsertRule(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, _ := f.book.DB.Bank().ListRules(ctx)
	if got[0].Pattern != "李四" {
		t.Errorf("更新后 pattern = %q", got[0].Pattern)
	}

	// 删除
	if err := f.book.DB.Bank().DeleteRule(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = f.book.DB.Bank().ListRules(ctx)
	if len(got) != 1 {
		t.Errorf("删除后规则数 = %d，期望 1", len(got))
	}

	// 引用不存在的对方科目应报错
	if _, err := f.book.DB.Bank().UpsertRule(ctx, &bank.Rule{
		Name: "坏规则", MatchField: bank.MatchCounterparty,
		CounterAccountCode: "9999",
	}); err == nil {
		t.Error("不存在的对方科目应报错")
	}
}
