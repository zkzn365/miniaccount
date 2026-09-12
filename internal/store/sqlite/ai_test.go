package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/ai/aiprovider"
	"miniaccount/internal/domain/ai"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/voucher"
)

// ---------------------------------------------------------------------------
// 服务配置
// ---------------------------------------------------------------------------

func TestAIProviderCRUD(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	id, err := db.AI().SaveProvider(ctx, &AIProviderConfig{
		Name: "本地 Ollama", Kind: "local",
		BaseURL: "http://127.0.0.1:11434/v1", Model: "qwen2.5:7b",
		Enabled: true, IsDefault: true,
	})
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if id == 0 {
		t.Fatal("应返回 id")
	}

	got, err := db.AI().Provider(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "本地 Ollama" || !got.IsDefault {
		t.Errorf("读回不一致: %+v", got)
	}
	// 未指定超时时用默认值
	if got.TimeoutMS != int(aiprovider.DefaultTimeout/time.Millisecond) {
		t.Errorf("超时应取默认值，实际 %d", got.TimeoutMS)
	}
	// Build 应产出可用的 Provider
	p, err := got.Build()
	if err != nil {
		t.Fatal(err)
	}
	if p.Model() != "qwen2.5:7b" || !p.Kind().IsLocal() {
		t.Errorf("Provider 构造有误: %s/%s", p.Model(), p.Kind())
	}
}

// ★ 同时只能有一个默认服务 —— 由部分唯一索引 + 事务内先清后设保证
func TestAIProviderSingleDefault(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	a, _ := db.AI().SaveProvider(ctx, &AIProviderConfig{
		Name: "A", Kind: "local", BaseURL: "http://127.0.0.1:1/v1",
		Model: "m1", Enabled: true, IsDefault: true,
	})
	b, _ := db.AI().SaveProvider(ctx, &AIProviderConfig{
		Name: "B", Kind: "cloud", BaseURL: "https://api.example.com/v1",
		Model: "m2", Enabled: true, IsDefault: true, APIKey: "sk-x",
	})

	all, err := db.AI().Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("应有 2 条配置，实际 %d", len(all))
	}
	for _, c := range all {
		if c.ID == a && c.IsDefault {
			t.Error("A 的默认标记应被 B 顶掉")
		}
		if c.ID == b && !c.IsDefault {
			t.Error("B 应为默认")
		}
	}
	def, err := db.AI().DefaultProvider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if def.Model() != "m2" {
		t.Errorf("默认服务 = %s，期望 m2", def.Model())
	}
	if def.Kind().IsLocal() {
		t.Error("https 地址应判为云端")
	}
}

func TestAIProviderValidation(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	bad := []AIProviderConfig{
		{Kind: "local", BaseURL: "u", Model: "m"},            // 缺名字
		{Name: "n", Kind: "local", Model: "m"},               // 缺地址
		{Name: "n", Kind: "local", BaseURL: "u"},             // 缺模型
		{Name: "n", Kind: "weird", BaseURL: "u", Model: "m"}, // 形态非法
	}
	for i, c := range bad {
		cc := c
		if _, err := db.AI().SaveProvider(ctx, &cc); err == nil {
			t.Errorf("第 %d 条非法配置应被拒绝", i+1)
		}
	}
}

// 没有配置时返回 Disabled 而不是 nil，避免调用点判空疏漏。
//
// ★ 断言的是「这条消息有没有用」，不是它的字面。
// 这句话会一路飘到界面上变成用户看到的报错，所以他必须能从中读出
// **下一步做什么** —— 只说「没有模型服务」等于让他自己去猜在哪儿配。
func TestAIDefaultProviderWhenNone(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	p, err := db.AI().DefaultProvider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatal("应返回 Disabled 而不是 nil")
	}
	_, cerr := p.Complete(ctx, aiprovider.Request{})
	if !errors.Is(cerr, aiprovider.ErrDisabled) {
		t.Errorf("应报 ErrDisabled，实际 %v", cerr)
	}
	msg := cerr.Error()
	if !strings.Contains(msg, "没有配置") {
		t.Errorf("应说明是「还没配」，实际 %v", msg)
	}
	if !strings.Contains(msg, "设置") {
		t.Errorf("要告诉用户去哪儿配，实际 %v", msg)
	}
	// 消息里不该再裹一层包名前缀：用户读的是「怎么解决」，
	// 不是「哪个 Go 包报的」。
	if strings.Contains(msg, "ai: ") || strings.Contains(msg, "未启用") {
		t.Errorf("消息里不该带包的报错前缀，实际 %v", msg)
	}
}

// 全部停用时也要明确报「已停用」而不是「没配置」。
//
// 这两种情况用户要做的事完全不同：一个是去**新建**，
// 一个是去把已有的那个**打开**。说错了就是让他白跑一趟 ——
// 这正是用户实际报回来的那个问题。
func TestAIDefaultProviderAllDisabled(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if _, err := db.AI().SaveProvider(ctx, &AIProviderConfig{
		Name: "A", Kind: "local", BaseURL: "http://127.0.0.1:1/v1",
		Model: "m", Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AI().SaveProvider(ctx, &AIProviderConfig{
		Name: "B", Kind: "local", BaseURL: "http://127.0.0.1:1/v1",
		Model: "m2", Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	p, _ := db.AI().DefaultProvider(ctx)
	_, err := p.Complete(ctx, aiprovider.Request{})
	msg := err.Error()

	if !strings.Contains(msg, "停用") {
		t.Errorf("应说明是停用而不是没配，实际 %v", msg)
	}
	if !strings.Contains(msg, "2") {
		t.Errorf("要说清楚配了几个（这里有 2 个），实际 %v", msg)
	}
	if !strings.Contains(msg, "启用") {
		t.Errorf("要告诉用户「启用」这个动作，实际 %v", msg)
	}
	if strings.Contains(msg, "还没有配置") || strings.Contains(msg, "尚未配置") {
		t.Errorf("已经配了 2 个，不能说成没配 —— 用户会被指去重做一遍，实际 %v", msg)
	}
}

func TestAIProviderDelete(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	id, _ := db.AI().SaveProvider(ctx, &AIProviderConfig{
		Name: "A", Kind: "local", BaseURL: "http://127.0.0.1:1/v1", Model: "m",
	})
	if err := db.AI().DeleteProvider(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := db.AI().DeleteProvider(ctx, id); !errors.Is(err, ErrAIProviderNotFound) {
		t.Errorf("重复删除应报未找到，实际 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 建议审计
// ---------------------------------------------------------------------------

func TestAIAuditRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	prop := mustProposal(t, `{"voucher":{"word":"记","biz_date":"2025-09-11",
	  "remark":"收回应收账款","entries":[
	  {"summary":"收回货款","account_code":"1002","debit":9040000,"credit":0},
	  {"summary":"收回货款","account_code":"1122","debit":0,"credit":9040000,"contact_id":1}]},
	  "confidence":0.88,"reasoning":"历史同类","evidence":["记-2025-01-0003"],"warnings":[]}`)

	target := int64(42)
	id, err := db.AI().Record(ctx, aiprovider.SuggestionRecord{
		TargetType: "bank_flow", TargetID: &target,
		Provider: "本地 Ollama", Model: "qwen2.5:7b", Layer: ai.LayerAI,
		PromptDigest: ai.Digest("some prompt"), RawResponse: "{...}",
		Proposed: prop, Confidence: 0.88, Status: "valid",
		TokensIn: 1200, TokensOut: 180,
	})
	if err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
	if id == 0 {
		t.Fatal("应返回 id")
	}

	rows, err := db.AI().Suggestions(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("应有 1 条记录，实际 %d", len(rows))
	}
	r := rows[0]
	if r.TargetType != "bank_flow" || r.TargetID == nil || *r.TargetID != 42 {
		t.Errorf("对象信息有误: %+v", r)
	}
	if r.Layer != ai.LayerAI || r.Status != "valid" {
		t.Errorf("层/状态有误: %s/%s", r.Layer, r.Status)
	}
	if r.Confidence != 0.88 {
		t.Errorf("置信度 = %v", r.Confidence)
	}
	if r.Checksum == "" {
		t.Error("应自动算出指纹")
	}
	if r.Decision != "" || r.DecidedAt != nil {
		t.Error("尚未处置时不该有处置信息")
	}
	if r.TokensIn != 1200 || r.TokensOut != 180 {
		t.Errorf("token = %d/%d", r.TokensIn, r.TokensOut)
	}
}

func TestAIAuditDecide(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	id, _ := db.AI().Record(ctx, aiprovider.SuggestionRecord{
		TargetType: "freeform", Layer: ai.LayerAI, Status: "valid",
	})

	// final_voucher_id 有外键约束，必须指向真实凭证
	seedBook(t, db)
	var vid int64
	if err := db.SQL().QueryRow(`SELECT MIN(id) FROM voucher`).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	if err := db.AI().Decide(ctx, id, aiprovider.DecisionModified, &vid, ""); err != nil {
		t.Fatal(err)
	}
	rows, _ := db.AI().Suggestions(ctx, 1)
	if rows[0].Decision != "modified" {
		t.Errorf("处置 = %s，期望 modified", rows[0].Decision)
	}
	if rows[0].FinalVoucher == nil || *rows[0].FinalVoucher != vid {
		t.Errorf("应记录最终凭证 id=%d，实际 %v", vid, rows[0].FinalVoucher)
	}
	if rows[0].DecidedAt == nil {
		t.Error("应记录处置时间")
	}

	// 拒绝时可以带原因，且不会覆盖已有的处置时间语义
	id2, _ := db.AI().Record(ctx, aiprovider.SuggestionRecord{
		TargetType: "freeform", Layer: ai.LayerAI, Status: "invalid",
	})
	if err := db.AI().Decide(ctx, id2, aiprovider.DecisionRejected, nil,
		"摘要看不懂"); err != nil {
		t.Fatal(err)
	}
	rows2, _ := db.AI().Suggestions(ctx, 1)
	if !strings.Contains(rows2[0].RejectReason, "摘要看不懂") {
		t.Errorf("应记录拒绝原因，实际 %q", rows2[0].RejectReason)
	}
	// 处置不存在的记录应报错
	if err := db.AI().Decide(ctx, 9999, aiprovider.DecisionAccepted, nil, ""); err == nil {
		t.Error("处置不存在的建议应报错")
	}
}

func TestAIFeedbackAndStats(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	// 4 条记录：
	//   ① 原样采纳  ② 改后采纳  ③ 被护栏拦下（模型给出了提议，科目是幻觉的）
	//   ④ 模型连不上（连解析都没走到）★ 不计入护栏通过率
	prop := mustProposal(t, `{"voucher":{"word":"记","biz_date":"2025-09-11",
	  "remark":"x","entries":[
	  {"summary":"s","account_code":"1002","debit":100,"credit":0},
	  {"summary":"s","account_code":"1002","debit":0,"credit":100}]},
	  "confidence":0.9,"reasoning":"r","evidence":[],"warnings":[]}`)

	id1, _ := db.AI().Record(ctx, aiprovider.SuggestionRecord{
		TargetType: "bank_flow", Layer: ai.LayerAI, Status: "valid",
		Confidence: 0.9, Proposed: prop})
	id2, _ := db.AI().Record(ctx, aiprovider.SuggestionRecord{
		TargetType: "bank_flow", Layer: ai.LayerAI, Status: "valid",
		Confidence: 0.7, Proposed: prop})
	_, _ = db.AI().Record(ctx, aiprovider.SuggestionRecord{
		TargetType: "bank_flow", Layer: ai.LayerAI, Status: "invalid",
		Proposed:     prop,
		RejectReason: "[阻断] 第1行 科目不存在：\"9999\" 不在当前账套的科目表中"})
	_, _ = db.AI().Record(ctx, aiprovider.SuggestionRecord{
		TargetType: "bank_flow", Layer: ai.LayerAI, Status: "invalid",
		RejectReason: "ai: 无法连接 http://127.0.0.1:11434/v1"})

	if err := db.AI().Decide(ctx, id1, aiprovider.DecisionAccepted, nil, ""); err != nil {
		t.Fatalf("记录采纳失败: %v", err)
	}
	if err := db.AI().Decide(ctx, id2, aiprovider.DecisionModified, nil, ""); err != nil {
		t.Fatalf("记录改后采纳失败: %v", err)
	}

	// ★ 字段级修改是学习闭环的核心数据
	if err := db.AI().Feedback(ctx, id2, "entries[1].account_code", "560206", "560210"); err != nil {
		t.Fatalf("写反馈失败: %v", err)
	}

	st, err := db.AI().Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 4 || st.Proposed != 3 || st.Valid != 2 {
		t.Errorf("统计有误: %+v", st)
	}
	if st.Accepted != 1 || st.Modified != 1 || st.Rejected != 0 {
		t.Errorf("处置统计有误: %+v", st)
	}
	// 采纳率 = (1+1)/4
	if got := st.AcceptanceRate(); got < 0.49 || got > 0.51 {
		t.Errorf("采纳率 = %.2f", got)
	}
	// ★ 护栏通过率 = 2/3（分母剔除「连不上」那条）
	if got := st.GuardrailPassRate(); got < 0.66 || got > 0.67 {
		t.Errorf("护栏通过率 = %.2f，期望 0.67（分母应剔除服务不可用的记录）", got)
	}
	if got := st.TransportFailures(); got != 1 {
		t.Errorf("服务不可用次数 = %d，期望 1", got)
	}
}

// 审计表里不能出现提示词原文
func TestAIAuditStoresDigestNotPrompt(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	prompt := "收到杭州某某科技有限公司货款 90400 元"
	_, err := db.AI().Record(ctx, aiprovider.SuggestionRecord{
		TargetType: "bank_flow", Layer: ai.LayerAI, Status: "valid",
		PromptDigest: ai.Digest(prompt),
	})
	if err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := db.SQL().QueryRow(
		`SELECT prompt_digest FROM ai_suggestion`).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(digest, "杭州") || strings.Contains(digest, "90400") {
		t.Errorf("审计表不该存提示词原文，实际 %q", digest)
	}
	if len(digest) != 64 {
		t.Errorf("应是 sha256，实际长度 %d", len(digest))
	}
}

// ---------------------------------------------------------------------------
// 上下文来源
// ---------------------------------------------------------------------------

func TestAIContextSource(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	lctx, err := db.AI().LedgerContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if lctx.Accounts.Len() != 191 {
		t.Errorf("科目数 = %d，期望 191", lctx.Accounts.Len())
	}
	if len(lctx.ContactKinds) == 0 {
		t.Error("应带回往来档案类型")
	}

	book, err := db.AI().BookContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if book.CompanyName == "" || book.TaxType == "" {
		t.Errorf("账套上下文不完整: %+v", book)
	}
	// 当前可记账期间应取最早的 open 期间（2025-01），而不是最晚的
	if book.PeriodDesc != "2025年01月" {
		t.Errorf("可记账期间 = %s，期望 2025年01月（最早的未结账期间）", book.PeriodDesc)
	}

	accts, err := db.AI().Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// ★ 只给明细科目：把汇总科目发给模型只会诱导它往汇总科目上记
	for _, a := range accts {
		if a.Code == "2221" {
			t.Error("汇总科目 2221 不该出现在候选里")
		}
		if a.Code == "560206" && len(a.AuxTypes) == 0 {
			t.Error("管理费用—办公费应标注需要的辅助核算")
		}
	}
	var hasBanks bool
	for _, a := range accts {
		if a.Code == "1002" {
			hasBanks = a.Direction == "借"
		}
	}
	if !hasBanks {
		t.Error("应含 1002 银行存款且方向为借")
	}
}

// ---------------------------------------------------------------------------
// 历史检索：★ 性价比最高的一层
// ---------------------------------------------------------------------------

// aiBook 造出一本有重复业务的小账：
//
//	3 月：支付宝-杭州XX科技 收到货款      → 借 1002 / 贷 1122
//	4 月：支付宝-杭州XX科技 收到货款      → 同上（应当被检索到）
//	5 月：国家电网 支付电费              → 借 560210 / 贷 1002
func aiBook(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	accIDs, err := db.Accounts().IDsByCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cust := mustContacts(t, db, "customer", "杭州某某科技有限公司")
	dept := int64(1)

	post := func(date, remark, summary string, lines ...ledger.Entry) int64 {
		t.Helper()
		v, err := voucher.New(voucher.WordJi, mustDate(date), "李会计")
		if err != nil {
			t.Fatal(err)
		}
		v.Remark = remark
		for _, e := range lines {
			if err := v.AddEntry(e); err != nil {
				t.Fatal(err)
			}
		}
		res, err := db.Vouchers().Post(ctx, PostInput{
			Voucher: v, Accounts: accIDs, PostingBy: "王主管", At: time.Now()})
		if err != nil {
			t.Fatalf("过账 %s 失败: %v", date, err)
		}
		return res.VoucherID
	}

	// 一次导入批次，后面的流水都挂它下面
	var importID int64
	if err := db.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.Exec(ctx, `
			INSERT INTO bank_import (account_id, file_name, file_sha256, encoding,
				row_count, imported_by, imported_at)
			VALUES (?, 'test.csv', 'sha-test', 'utf-8', 3, '李会计', ?)`,
			accIDs["1002"], nowString())
		if err != nil {
			return err
		}
		importID, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatalf("建导入批次失败: %v", err)
	}

	// 银行流水来源的凭证，带原始摘要 —— 检索时最有价值
	postFlow := func(date, summary, counterparty string, amount int64,
		lines ...ledger.Entry) {
		t.Helper()
		vid := post(date, "银行流水导入："+summary, summary, lines...)
		// 补一条 bank_flow，把凭证与流水串起来
		dir := "in"
		amt := amount
		if amt < 0 {
			dir, amt = "out", -amt
		}
		if err := db.WithTx(ctx, func(tx *Tx) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO bank_flow (import_id, account_id, txn_date, direction,
					amount, balance, counterparty_name, summary, dedup_hash,
					status, voucher_id, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, 'posted', ?, ?, ?)`,
				importID, accIDs["1002"], date, dir, amt, counterparty, summary,
				"dedup-"+date+summary, vid, nowString(), nowString())
			return err
		}); err != nil {
			t.Fatalf("写流水失败: %v", err)
		}
	}

	aux := func(id *int64) ledger.Aux { return ledger.Aux{ContactID: id} }

	postFlow("2025-03-10", "收到货款", "支付宝-杭州某某科技有限公司", 9040000,
		ledger.Entry{AccountCode: "1002", Summary: "收到货款", Debit: money100(90400)},
		ledger.Entry{AccountCode: "1122", Summary: "收到货款",
			Credit: money100(90400), Aux: aux(&cust)},
	)
	postFlow("2025-04-10", "收到货款", "支付宝-杭州某某科技有限公司", 8500000,
		ledger.Entry{AccountCode: "1002", Summary: "收到货款", Debit: money100(85000)},
		ledger.Entry{AccountCode: "1122", Summary: "收到货款",
			Credit: money100(85000), Aux: aux(&cust)},
	)
	postFlow("2025-05-08", "支付电费", "国家电网杭州供电公司", -320000,
		ledger.Entry{AccountCode: "560210", Summary: "支付电费",
			Debit: money100(3200), Aux: ledger.Aux{DeptID: &dept}},
		ledger.Entry{AccountCode: "1002", Summary: "支付电费", Credit: money100(3200)},
	)
}

func TestSimilarVouchersByCounterparty(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)
	aiBook(t, db)

	got, err := db.AI().SimilarVouchers(ctx,
		"收到货款", "支付宝-杭州某某科技有限公司", 9000000, 3)
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("同一对手方的历史货款应当命中")
	}
	// 最相似的必须是对手方完全相同的两条
	top := got[0]
	if !strings.Contains(top.Text, "支付宝") && !strings.Contains(top.Remark, "流水") {
		t.Errorf("最相似的一条应来自同一对手方，实际 %+v", top)
	}
	if top.Score <= 0 || top.Score > 1 {
		t.Errorf("相似度应归一化到 (0,1]，实际 %v", top.Score)
	}
	// 范例必须带完整分录，模型才能照抄
	if len(top.Lines) == 0 {
		t.Fatal("范例必须包含分录")
	}
	var hasBank, hasAR bool
	for _, l := range top.Lines {
		if l.AccountCode == "1002" {
			hasBank = true
		}
		if l.AccountCode == "1122" {
			hasAR = true
			if l.ContactName == "" {
				t.Error("往来科目应带上对方名称，帮助模型判断")
			}
		}
	}
	if !hasBank || !hasAR {
		t.Errorf("范例分录不完整: %+v", top.Lines)
	}
	// 排序必须降序
	for i := 1; i < len(got); i++ {
		if got[i].Score > got[i-1].Score {
			t.Errorf("结果未按相似度降序: %v", got)
		}
	}
}

// 完全不同的对手方不应拿到高分
func TestSimilarVouchersRanking(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)
	aiBook(t, db)

	got, err := db.AI().SimilarVouchers(ctx,
		"支付电费", "国家电网杭州供电公司", -320000, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("应至少有结果")
	}
	if !strings.Contains(got[0].Text, "电费") {
		t.Errorf("电费应当排第一，实际 %+v", got[0])
	}
}

// ★ 结转凭证不能当范例：它是机械动作，不是「业务怎么记」的参考
func TestSimilarVouchersExcludesClosing(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)
	aiBook(t, db)
	for m := 1; m <= 9; m++ {
		closeP(t, db, 2025, m, "王主管")
	}

	got, err := db.AI().SimilarVouchers(ctx, "结转损益", "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, ex := range got {
		if strings.HasPrefix(ex.VoucherNo, "转-") {
			t.Errorf("结转凭证不该作为范例：%s", ex.VoucherNo)
		}
	}
}

func TestSimilarVouchersLimit(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)
	aiBook(t, db)

	got, err := db.AI().SimilarVouchers(ctx, "收到货款", "", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 1 {
		t.Errorf("limit=1 时最多返回 1 条，实际 %d", len(got))
	}
	// 空文本、空对手方时也不应报错
	if _, err := db.AI().SimilarVouchers(ctx, "", "", 0, 3); err != nil {
		t.Errorf("空输入不该报错: %v", err)
	}
}

func TestKeywordsBigram(t *testing.T) {
	k := keywords("收到杭州某某科技有限公司货款")
	for _, want := range []string{"杭州", "某某", "科技", "货款"} {
		if !k[want] {
			t.Errorf("关键词应含 %q，实际 %v", want, keysOfSet(k))
		}
	}
	// 停用词不该出现
	for _, bad := range []string{"收到", "有限"} {
		if k[bad] {
			t.Errorf("停用词 %q 不该参与打分", bad)
		}
	}
	// 空输入
	if len(keywords("")) != 0 {
		t.Error("空文本应得到空集合")
	}
	// 单字段跳过
	if len(keywords("付")) != 0 {
		t.Error("单字段不构成关键词")
	}
}

func keysOfSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestMagnitudeDiff(t *testing.T) {
	cases := []struct {
		a, b int64
		want int
	}{
		{9000000, 8500000, 0}, // 同一数量级
		{9000000, 900000, 1},  // 差一个数量级
		{0, 100, 99},          // 零值不可比
		{-9000000, 8500000, 0},
	}
	for _, c := range cases {
		if got := magnitudeDiff(c.a, c.b); got != c.want {
			t.Errorf("magnitudeDiff(%d,%d) = %d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestNormalizeScore(t *testing.T) {
	if got := normalizeScore(0); got != 0 {
		t.Errorf("0 分应归一到 0，实际 %v", got)
	}
	if got := normalizeScore(6); got != 1 {
		t.Errorf("满分应归一到 1，实际 %v", got)
	}
	if got := normalizeScore(99); got != 1 {
		t.Errorf("超满分应截断到 1，实际 %v", got)
	}
	if got := normalizeScore(3); got != 0.5 {
		t.Errorf("3 分应归一到 0.5，实际 %v", got)
	}
}

func TestDaysBetween(t *testing.T) {
	cases := []struct {
		from, to string
		want     int
	}{
		{"2025-03-01", "2025-03-31", 30},
		{"2025-01-01", "2025-12-31", 364},
		{"2024-03-01", "2025-03-01", 365}, // 跨闰年 2 月
		{"2025-03-31", "2025-03-01", -30},
		{"2025-03-01", "2025-03-01", 0},
	}
	for _, c := range cases {
		if got := daysBetween(mustDate(c.from), mustDate(c.to)); got != c.want {
			t.Errorf("daysBetween(%s,%s) = %d，期望 %d", c.from, c.to, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 端到端：假模型 + 真存储
// ---------------------------------------------------------------------------

// fakeModel 返回预设内容。
type fakeModel struct{ content string }

func (f *fakeModel) Name() string          { return "假模型" }
func (f *fakeModel) Model() string         { return "fake-1" }
func (f *fakeModel) Kind() aiprovider.Kind { return aiprovider.KindLocal }
func (f *fakeModel) Complete(context.Context, aiprovider.Request) (*aiprovider.Response, error) {
	return &aiprovider.Response{Content: f.content, Model: "fake-1"}, nil
}

// 把真实存储当作编排层的三个依赖，验证接口确实对得上、数据确实通
func TestSuggesterEndToEndWithRealStore(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)
	aiBook(t, db)

	// ★ contact_id 必须是真实的客户 id —— 护栏会校验类型是否与科目匹配。
	// 这里刻意查库取，而不是猜一个数字：猜错正是要拦下的那类问题。
	var custID int64
	if err := db.SQL().QueryRow(
		`SELECT id FROM contact WHERE name = ?`,
		"杭州某某科技有限公司").Scan(&custID); err != nil {
		t.Fatalf("找不到客户档案: %v", err)
	}

	content := fmt.Sprintf(`{"voucher":{"word":"记","biz_date":"2025-09-11",
	  "remark":"收到货款","entries":[
	  {"summary":"收到货款","account_code":"1002","debit":9040000,"credit":0},
	  {"summary":"收到货款","account_code":"1122","debit":0,"credit":9040000,"contact_id":%d}]},
	  "confidence":0.91,"reasoning":"与 3 月同类业务一致","evidence":[],"warnings":[]}`, custID)

	s := &aiprovider.Suggester{
		Provider:  &fakeModel{content: content},
		Context:   db.AI(),
		Retriever: db.AI(),
		Auditor:   db.AI(),
		Now:       func() time.Time { return time.Date(2025, 9, 11, 0, 0, 0, 0, time.UTC) },
	}

	res := s.Suggest(ctx, aiprovider.Input{
		Task: aiprovider.TaskBankFlow, Text: "收到货款",
		Amount: money100(90400), Date: "2025-09-11", Direction: "收入",
		Counterparty: "支付宝-杭州某某科技有限公司",
	}, "bank_flow", nil)

	if res.Err != nil {
		t.Fatalf("端到端建议失败: %v", res.Err)
	}
	if !res.OK() {
		t.Fatalf("应通过护栏: %s", res.Summary())
	}
	if res.SuggestionID == 0 {
		t.Fatal("应写入审计表并返回 id")
	}
	// 审计确实落库了
	rows, err := db.AI().Suggestions(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "valid" {
		t.Fatalf("审计记录有误: %+v", rows)
	}
	var digest string
	if err := db.SQL().QueryRow(
		`SELECT prompt_digest FROM ai_suggestion WHERE id = ?`,
		rows[0].ID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest == "" {
		t.Error("应记录提示词指纹")
	}

	// 采纳 → 落草稿
	v, err := s.Accept(ctx, res, "李会计", nil)
	if err != nil {
		t.Fatalf("采纳失败: %v", err)
	}
	if v.Status != voucher.StatusDraft || !v.CreatedByAI {
		t.Errorf("应生成 AI 来源的草稿: %+v", v)
	}
	if len(v.Entries) != 2 {
		t.Fatalf("分录数 = %d", len(v.Entries))
	}

	// 处置已记录
	rows, _ = db.AI().Suggestions(ctx, 5)
	if rows[0].Decision != "accepted" {
		t.Errorf("处置 = %s，期望 accepted", rows[0].Decision)
	}
}

func mustProposal(t *testing.T, raw string) *ai.Proposal {
	t.Helper()
	p, err := ai.Parse(raw)
	if err != nil {
		t.Fatalf("构造提议失败: %v", err)
	}
	return p
}

// ---------------------------------------------------------------------------
// ★ Agent 工具：走真实存储
// ---------------------------------------------------------------------------

// Agent 的三个工具背后是本账套的真实数据。这里验证接口确实对得上、
// 数据确实通 —— 用假 provider 测不出「工具查的是不是这个账套」。
func TestAgentToolsAgainstRealStore(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)
	aiBook(t, db)

	repo := db.AI()
	ts := aiprovider.NewToolSet(
		aiprovider.SearchAccountsTool(repo),
		aiprovider.SearchContactsTool(repo),
		aiprovider.FindSimilarVouchersTool(repo),
	)
	if ts.Len() != 3 {
		t.Fatalf("工具数 = %d，期望 3", ts.Len())
	}

	// ---- search_accounts ----
	tool, _ := ts.Get("search_accounts")
	out, err := tool.Run(ctx, json.RawMessage(`{"keyword":"办公费"}`))
	if err != nil {
		t.Fatalf("搜索科目失败: %v", err)
	}
	if !strings.Contains(out, "560206") {
		t.Errorf("应命中 560206 管理费用—办公费，实际 %q", out)
	}
	// 汇总科目不该出现（工具与提示词走同一份科目清单）
	out, _ = tool.Run(ctx, json.RawMessage(`{"keyword":"管理费用"}`))
	if strings.Contains(out, "5602 管理费用\n") {
		t.Errorf("汇总科目不该出现在可记账列表里：%q", out)
	}

	// ---- search_contacts ----
	tool, _ = ts.Get("search_contacts")
	out, err = tool.Run(ctx, json.RawMessage(`{"keyword":"科技"}`))
	if err != nil {
		t.Fatalf("搜索往来单位失败: %v", err)
	}
	if !strings.Contains(out, "杭州某某科技有限公司") {
		t.Errorf("应命中客户档案，实际 %q", out)
	}
	// 没命中时要明确禁止编造 id
	out, _ = tool.Run(ctx, json.RawMessage(`{"keyword":"不存在的公司"}`))
	if !strings.Contains(out, "不要编造") {
		t.Errorf("没命中时应禁止编造 id，实际 %q", out)
	}

	// ---- find_similar_vouchers ----
	tool, _ = ts.Get("find_similar_vouchers")
	out, err = tool.Run(ctx, json.RawMessage(
		`{"text":"收到货款","counterparty":"支付宝-杭州某某科技有限公司","limit":3}`))
	if err != nil {
		t.Fatalf("检索历史凭证失败: %v", err)
	}
	if !strings.Contains(out, "借 1002") || !strings.Contains(out, "贷 1122") {
		t.Errorf("应返回历史凭证的完整分录，实际 %q", out)
	}
	// 结转凭证不能当范例
	if strings.Contains(out, "转-") {
		t.Errorf("结转凭证不该作为范例：%q", out)
	}
}

// Agent 循环走真实存储：模型先查科目、再查历史，最后给答案。
func TestAgentLoopWithRealStore(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	repo := db.AI()
	ts := aiprovider.NewToolSet(
		aiprovider.SearchAccountsTool(repo),
		aiprovider.FindSimilarVouchersTool(repo),
	)

	// 脚本模型：第一轮查科目，第二轮查历史，第三轮给最终答案
	scripted := &scriptedToolModel{steps: []*aiprovider.Response{
		{ToolCalls: []aiprovider.ToolCall{{
			ID: "c1", Name: "search_accounts", Arguments: `{"keyword":"应收账款"}`,
		}}},
		{ToolCalls: []aiprovider.ToolCall{{
			ID: "c2", Name: "find_similar_vouchers",
			Arguments: `{"text":"收到货款","limit":2}`,
		}}},
		{Content: `{"voucher":{"word":"记","biz_date":"2025-09-11","remark":"收到货款",
		  "entries":[
		  {"summary":"收到货款","account_code":"1002","debit":9040000,"credit":0},
		  {"summary":"收到货款","account_code":"1122","debit":0,"credit":9040000,
		   "contact_id":2}]},
		  "confidence":0.9,"reasoning":"依据历史","evidence":[],"warnings":[]}`},
	}}

	agent := &aiprovider.Agent{
		Provider: scripted, Tools: ts, Options: aiprovider.DefaultAgentOptions(),
	}
	res, err := agent.Run(ctx, "你是会计", "为「收到货款 90,400」编制凭证")
	if err != nil {
		t.Fatalf("Agent 循环失败: %v", err)
	}
	if res.Rounds != 3 {
		t.Errorf("轮数 = %d，期望 3", res.Rounds)
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("工具调用数 = %d，期望 2", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Name != "search_accounts" ||
		res.ToolCalls[1].Name != "find_similar_vouchers" {
		t.Errorf("工具调用顺序有误: %+v", res.ToolCalls)
	}
	for _, tc := range res.ToolCalls {
		if tc.Err != "" {
			t.Errorf("工具 %s 执行失败: %s", tc.Name, tc.Err)
		}
		if tc.Result == "" {
			t.Errorf("工具 %s 没有返回内容", tc.Name)
		}
	}

	// ★ 最终答案要走**同一套护栏** —— 多了一轮工具调用不该放松校验
	prop, err := aiprovider.ParseAgentResult(res)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	lctx, err := repo.LedgerContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	report, err := prop.Validate(lctx)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed() {
		t.Fatalf("Agent 的产出应通过护栏: %s", report.Summary())
	}
}

// scriptedToolModel 按脚本依次返回响应，用于驱动 Agent 循环。
type scriptedToolModel struct {
	steps []*aiprovider.Response
	idx   int
}

func (s *scriptedToolModel) Name() string  { return "脚本模型" }
func (s *scriptedToolModel) Model() string { return "scripted" }
func (s *scriptedToolModel) Kind() aiprovider.Kind {
	return aiprovider.KindLocal
}
func (s *scriptedToolModel) Complete(_ context.Context,
	_ aiprovider.Request) (*aiprovider.Response, error) {
	if s.idx >= len(s.steps) {
		return &aiprovider.Response{Content: "{}"}, nil
	}
	r := s.steps[s.idx]
	s.idx++
	return r, nil
}
