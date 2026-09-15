package aiprovider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 审计文书工具
// ---------------------------------------------------------------------------
//
// 只读；而且必须反复说清「这是草稿，不是已出具的报告」——
// 模型若用「审计意见是无保留」这种口吻回答，
// 用户很可能就此认为审计已经做完了。

type fakeDoc struct{ brief *AuditDocBrief }

func (f fakeDoc) AuditDocBrief(_ context.Context, kind string, _, _ int) (*AuditDocBrief, error) {
	b := *f.brief
	b.Kind = kind
	return &b, nil
}

func TestGetAuditDocToolIsReadOnlyAndSaysDraft(t *testing.T) {
	ts := NewToolSet(GetAuditDocTool(fakeDoc{&AuditDocBrief{Period: "2025-03"}}))
	tool, ok := ts.Get("get_audit_draft")
	if !ok {
		t.Fatal("工具集里应当有 get_audit_draft")
	}
	if tool.Terminal {
		t.Error("读文书草稿是只读的，不能是终止型")
	}
	// ★ 工具说明里必须挡住「报告已出具」这种说法
	if !strings.Contains(tool.Description, "草稿，不是已经出具的报告") {
		t.Error("★ 必须写明这是草稿 —— 否则模型会替注册会计师把报告说成已出具")
	}
	if !strings.Contains(tool.Description, "不能替注册会计师形成意见") {
		t.Error("★ 必须禁止模型替注册会计师形成意见")
	}
}

func TestRenderAuditDocBrief(t *testing.T) {
	d := &AuditDocBrief{
		Kind: "audit", KindLabel: "审计报告",
		Company: "杭州云帆软件有限公司", Period: "2025-03",
		OpinionStr: "无保留意见", CanIssue: false,
		Missing: []string{"第二名注册会计师（签字）：审计报告须两名注册会计师签名盖章"},
		Sections: []AuditDocBriefSection{
			{No: "一", Title: "审计意见", Body: "我们认为，后附的财务报表……",
				Source: "意见类型由注册会计师选择"},
		},
		Signature: "本报告须由两名注册会计师签名盖章、会计师事务所盖章后生效。",
	}
	text := renderAuditDocBrief(d)
	for _, want := range []string{
		"审计报告草稿", "杭州云帆软件有限公司", "无保留意见",
		"还不能签发", "第二名注册会计师", "一、审计意见", "来源：",
		"两名注册会计师签名盖章",
		// ★ 渲染文本末尾必须再钉一次
		"不是已出具的报告",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("文书文本里缺少 %q，实际：\n%s", want, text)
		}
	}

	// 可以签发时也要说清「待签字盖章」
	d.CanIssue = true
	d.Missing = nil
	text = renderAuditDocBrief(d)
	if !strings.Contains(text, "待签字盖章后生效") {
		t.Errorf("成文时也要说清还需签字盖章：\n%s", text)
	}
}

func TestAuditDocToolValidatesKindAndPeriod(t *testing.T) {
	tool := GetAuditDocTool(fakeDoc{&AuditDocBrief{Period: "2025-03"}})
	for _, raw := range []string{
		`{"kind":"poem","period":"2025-03"}`,
		`{"kind":"audit","period":"去年"}`,
		`{"kind":"","period":"2025-03"}`,
	} {
		if _, err := tool.Run(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("参数 %s 应当被拦下", raw)
		}
	}
	if _, err := tool.Run(context.Background(),
		json.RawMessage(`{"kind":"management","period":"2025-03"}`)); err != nil {
		t.Errorf("正常参数不该报错：%v", err)
	}
	if _, err := GetAuditDocTool(nil).Run(context.Background(),
		json.RawMessage(`{"kind":"audit","period":"2025-03"}`)); err == nil {
		t.Error("没有数据源时应当报错")
	}
}
