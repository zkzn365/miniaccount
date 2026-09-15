package aiprovider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 证据链工具
// ---------------------------------------------------------------------------
//
// 只读；而且**不能替用户断言「原件已取得」** ——
// 账套层看不到文件系统，它只能说明底稿上记着什么。
// 让模型说「原件已取得」，比它不知道更危险：用户会据此结掉这一项。

type fakeEvidence struct{ brief *EvidenceBrief }

func (f fakeEvidence) EvidenceBrief(_ context.Context, _ PeriodKey) (*EvidenceBrief, error) {
	return f.brief, nil
}

func TestGetEvidenceToolIsReadOnly(t *testing.T) {
	ts := NewToolSet(GetEvidenceTool(fakeEvidence{}))
	tool, ok := ts.Get("get_evidence")
	if !ok {
		t.Fatal("工具集里应当有 get_evidence")
	}
	if tool.Terminal {
		t.Error("读证据链是只读的，不能是终止型")
	}
	// ★ 工具说明里必须挡住「替用户说原件已取得」这句话。
	// 这不是措辞问题：说错一次，用户就会把这一项当成做完了。
	if !strings.Contains(tool.Description, "不能说明原件还在不在") {
		t.Error("★ 工具说明必须点明它看不到原件是否还在 —— 否则模型会替用户断言「已取得」")
	}
}

func TestRenderEvidence(t *testing.T) {
	e := &EvidenceBrief{
		Period: "2025-03", Unsupported: 1,
		Concludes: "3 项结论中有 1 项还没有附依据。",
		Chains: []EvidenceChainBrief{
			{Owner: "审计调整", Title: "ADJ-202503-001 补提折旧 3,000.00",
				Items: []EvidenceItemBrief{
					{Kind: "附件", Label: "折旧计算表.xlsx", Note: "少提 3,000.00", By: "李审计"},
					{Kind: "凭证", Label: "记-2025-03-0007 计提折旧", By: "李审计"},
				}, Files: 1, Documents: 1},
			{Owner: "底稿结论", Title: "本期底稿结论：未更正错报合计 3,000.00"},
		},
	}
	text := renderEvidence(e)
	for _, want := range []string{
		"2025-03 审计证据链", "审计调整", "折旧计算表.xlsx", "少提 3,000.00",
		"附件 1、单据 1", "底稿结论", "（没有依据）",
		// 有结论没依据时要明确说「不要替用户编一份不存在的依据」
		"不要替用户编一份不存在的依据",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("证据链文本里缺少 %q，实际：\n%s", want, text)
		}
	}
}

func TestEvidenceToolParsesPeriod(t *testing.T) {
	tool := GetEvidenceTool(fakeEvidence{&EvidenceBrief{Period: "2025-03"}})
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"period":"上个月"}`)); err == nil {
		t.Error("读不出来的期间应当报错，而不是去猜")
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"period":"2025-03"}`)); err != nil {
		t.Errorf("正常期间不该报错：%v", err)
	}
}

// ---------------------------------------------------------------------------
// 提议挂依据
// ---------------------------------------------------------------------------
//
// 模型能查到「这笔调整没有依据」，也能查到账上有一张对应的凭证 ——
// 但它不能自己把两者挂上：挂错了等于给结论换了一个出处。

func TestProposeEvidenceToolIsTerminal(t *testing.T) {
	ts := NewToolSet(ProposeEvidenceTool())
	tool, ok := ts.Get("propose_evidence")
	if !ok {
		t.Fatal("工具集里应当有 propose_evidence")
	}
	if !tool.Terminal {
		t.Fatal("★ 挂依据必须由用户确认，因此必须是终止型")
	}
	if tool.RenderAsk == nil {
		t.Error("终止型工具要有 RenderAsk")
	}
	// ★ 附件不在这个工具的范围内：模型没法上传文件
	if !strings.Contains(tool.Description, "附件") ||
		!strings.Contains(tool.Description, "用户自己上传") {
		t.Error("要明确告诉模型：附件得用户自己上传")
	}
}

func TestParseEvidenceProposal(t *testing.T) {
	ok := `{
		"owner_type": "adjustment", "owner_id": 7,
		"ref_kind": "voucher", "ref_id": 12,
		"note": "这张凭证上的折旧额就是少提的那部分",
		"reason": "补提折旧的依据就在这张凭证上"
	}`
	p, err := ParseEvidenceProposal(ok)
	if err != nil {
		t.Fatalf("基准用例应当通过：%v", err)
	}
	if p.OwnerType != "adjustment" || p.RefID != 12 {
		t.Errorf("解析结果不对：%+v", p)
	}

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"结论种类不认识", `{"owner_type":"feeling","owner_id":7,"ref_kind":"voucher","ref_id":1,"reason":"r"}`, "结论种类"},
		{"没有结论 id", `{"owner_type":"adjustment","owner_id":0,"ref_kind":"voucher","ref_id":1,"reason":"r"}`, "结论 id"},
		{"资料种类不认识", `{"owner_type":"adjustment","owner_id":7,"ref_kind":"photo","reason":"r"}`, "资料种类"},
		{"单据没给 id", `{"owner_type":"adjustment","owner_id":7,"ref_kind":"voucher","reason":"r"}`, "单据 id"},
		{"外部资料没描述", `{"owner_type":"adjustment","owner_id":7,"ref_kind":"external","reason":"r"}`, "写清"},
		{"要挂附件", `{"owner_type":"adjustment","owner_id":7,"ref_kind":"attachment","ref_label":"x.pdf","reason":"r"}`, "附件"},
		{"没有理由", `{"owner_type":"adjustment","owner_id":7,"ref_kind":"voucher","ref_id":1}`, "为什么"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseEvidenceProposal(c.raw); err == nil {
				t.Fatal("应当被拦下")
			} else if !strings.Contains(err.Error(), c.want) {
				t.Errorf("报错应当提到 %q，实际 %v", c.want, err)
			}
		})
	}
}
