package service_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"miniaccount/internal/ai/aiprovider"
	"miniaccount/internal/domain/ai"
	"miniaccount/internal/service"
)

// proposalJSON 造一条银行收款的提议。
//
// 往来单位 id 要填真档案里的：应收账款要求「客户」辅助核算，
// 缺了护栏会拦下来 —— 而采纳时**会重新跑一次护栏**，
// 因为账套是会变的（期间结账、科目停用、档案删除）。
func proposalJSON(contactID int64) string {
	return fmt.Sprintf(`{
  "voucher": {
    "word": "记", "biz_date": "2025-01-15", "remark": "收到货款",
    "entries": [
      {"summary": "收到货款", "account_code": "1002", "debit": 10600000, "credit": 0},
      {"summary": "收到货款", "account_code": "1122", "debit": 0, "credit": 10600000,
       "contact_id": %d}
    ]
  },
  "confidence": 0.9, "reasoning": "银行收款对应收账款",
  "evidence": [], "warnings": []
}`, contactID)
}

// newBookWithContact 建一个账套并建好一个客户档案。
func newBookWithContact(t *testing.T, svc *service.Service) int64 {
	t.Helper()
	id, err := svc.SaveContact(context.Background(), service.ContactInput{
		Kind: "customer", Name: "杭州云帆科技有限公司", Enabled: true,
	})
	if err != nil {
		t.Fatalf("建客户档案失败: %v", err)
	}
	return id
}

// ★ 「采纳」必须能从库里把提议取回来，落成**草稿**凭证。
//
// 这条链路不调用模型：提议直接从审计表读。所以它可以在没有
// 模型服务的情况下测 —— 而它恰恰是最容易出错的一段（材料化、
// 辅助核算、编号、审计回写）。
func TestAcceptAISuggestionCreatesDraft(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()

	cid := newBookWithContact(t, svc)
	p, err := ai.Parse(proposalJSON(cid))
	if err != nil {
		t.Fatalf("提议 JSON 解析失败: %v", err)
	}
	id, err := svc.AI().Record(ctx, aiprovider.SuggestionRecord{
		TargetType: "freeform", Provider: "测试", Model: "test-model",
		Layer: ai.LayerAI, PromptDigest: strings.Repeat("a", 64),
		Proposed: p, Status: "valid", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("写审计记录失败: %v", err)
	}

	res, err := svc.AcceptAISuggestion(ctx, id, "王主管")
	if err != nil {
		t.Fatalf("采纳失败: %v", err)
	}
	if res.VoucherID == 0 {
		t.Fatalf("没有生成凭证：%+v", res)
	}
	// 草稿还没有凭证号（编号在过账时分配），但提示语里不能出现空档
	if strings.Contains(res.Summary, "凭证 ，") {
		t.Errorf("★ 提示语里凭证号是空的：%q", res.Summary)
	}

	// 落下来的必须是**草稿**，不是已过账
	d, err := svc.Voucher(ctx, res.VoucherID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "draft" {
		t.Errorf("★ 采纳后应当是草稿，实际 %s —— 过账必须由人按下去", d.Status)
	}
	if d.CreatedBy != "王主管" {
		t.Errorf("制单人 = %q，期望「王主管」—— 记账责任落在自然人身上", d.CreatedBy)
	}
	if d.TotalDebit != 10600000 || d.TotalCredit != 10600000 {
		t.Errorf("金额不对：借 %d 贷 %d", d.TotalDebit, d.TotalCredit)
	}

	// 审计表要记下「被采纳了」以及落到了哪张凭证
	rec, err := svc.AI().Suggestion(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Decision != string(aiprovider.DecisionAccepted) {
		t.Errorf("decision = %q，期望 accepted", rec.Decision)
	}
	if rec.FinalVoucher == nil || *rec.FinalVoucher != res.VoucherID {
		t.Errorf("审计表里没有记下最终凭证 id：%v", rec.FinalVoucher)
	}
}

// 已经采纳过的不许再采纳一次：否则同一笔业务会被记两遍。
func TestAcceptAISuggestionIsNotRepeatable(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()
	p, _ := ai.Parse(proposalJSON(newBookWithContact(t, svc)))
	id, err := svc.AI().Record(ctx, aiprovider.SuggestionRecord{
		TargetType: "freeform", Layer: ai.LayerAI, Proposed: p, Status: "valid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptAISuggestion(ctx, id, "王主管"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptAISuggestion(ctx, id, "王主管"); err == nil {
		t.Error("★ 同一条建议被采纳了两次 —— 同一笔业务会记两遍账")
	}
}

// 没通过护栏的提议不能采纳。
func TestAcceptAISuggestionRefusesUnchecked(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()
	p, _ := ai.Parse(proposalJSON(newBookWithContact(t, svc)))
	id, err := svc.AI().Record(ctx, aiprovider.SuggestionRecord{
		TargetType: "freeform", Layer: ai.LayerAI, Proposed: p,
		Status: "invalid", RejectReason: "借贷不平衡",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptAISuggestion(ctx, id, "王主管"); err == nil {
		t.Error("★ 没通过护栏的提议被采纳了")
	}
}

// 拒绝要记进审计（带原因），并且幂等。
func TestRejectAISuggestionRecordsReason(t *testing.T) {
	svc := newSvc(t, 3)
	ctx := context.Background()
	p, _ := ai.Parse(proposalJSON(newBookWithContact(t, svc)))
	id, err := svc.AI().Record(ctx, aiprovider.SuggestionRecord{
		TargetType: "freeform", Layer: ai.LayerAI, Proposed: p, Status: "valid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RejectAISuggestion(ctx, id, "科目选错了"); err != nil {
		t.Fatal(err)
	}
	// 重复拒绝不该报错
	if err := svc.RejectAISuggestion(ctx, id, "又点了一次"); err != nil {
		t.Errorf("重复拒绝应当幂等：%v", err)
	}
	rec, err := svc.AI().Suggestion(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Decision != string(aiprovider.DecisionRejected) {
		t.Errorf("decision = %q，期望 rejected", rec.Decision)
	}
	if !strings.Contains(rec.RejectReason, "科目选错了") {
		t.Errorf("拒绝原因没记下来：%q", rec.RejectReason)
	}
}
