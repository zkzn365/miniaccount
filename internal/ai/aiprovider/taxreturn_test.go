package aiprovider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 税务计算表工具
// ---------------------------------------------------------------------------
//
// 只读；而且必须与界面同一条计算路径 ——
// 让模型自己按记忆里的税率算，它会用上过期的优惠政策。

type fakeTax struct{ brief *TaxReturnBrief }

func (f fakeTax) TaxReturnBrief(_ context.Context, kind string, _, _ int) (*TaxReturnBrief, error) {
	b := *f.brief
	b.Kind = kind
	return &b, nil
}

func TestGetTaxReturnToolIsReadOnly(t *testing.T) {
	ts := NewToolSet(GetTaxReturnTool(fakeTax{&TaxReturnBrief{Period: "2025-03"}}))
	tool, ok := ts.Get("get_tax_return")
	if !ok {
		t.Fatal("工具集里应当有 get_tax_return")
	}
	if tool.Terminal {
		t.Error("读税务计算表是只读的，不能是终止型")
	}
	// ★ 工具说明里必须挡住两件事：自己算税、以及把结果当申报表用
	if !strings.Contains(tool.Description, "不要自己按记忆里的税率算") {
		t.Error("★ 必须明确禁止模型自己算税 —— 它会用上过期的优惠政策")
	}
	if !strings.Contains(tool.Description, "不能直接用于申报") {
		t.Error("★ 必须写明结果不能直接用于申报")
	}
}

func TestRenderTaxReturn(t *testing.T) {
	b := &TaxReturnBrief{
		Kind: "vat", KindLabel: "增值税及附加", Period: "2025-03",
		Title:      "增值税及附加税费计算表",
		Identities: []string{"增值税纳税人身份：一般纳税人"},
		Concludes:  "本期应补增值税及附加 7,280.00。",
		Keys: []TaxReturnBriefRow{
			{Label: "本期应纳税额（增值税）", Amount: 650000, Note: "= 销项 − 抵扣"},
		},
		Rows: []TaxReturnBriefRow{
			{Line: "11", Label: "销项税额", Amount: 1300000,
				Source: "22210102 本期贷方发生额"},
		},
		Warnings:     []string{"有留抵 5,000.00 结转下期"},
		PolicyNote:   "口径：一般计税方法。本表不替代申报表。",
		FilingStatus: "已申报并缴纳", FilingHint: "与当前计算表一致（7,280.00）。",
	}
	text := renderTaxReturn(b)
	for _, want := range []string{
		"增值税及附加", "2025-03", "行11 销项税额", "22210102",
		"要注意", "留抵", "不替代申报表",
		// ★ 申报状态要进文本：不知道「这期报了没」，
		// 模型会在其实已经报过的期间上建议「该去申报了」
		"申报状态", "已申报并缴纳", "一致",
		// ★ 渲染出来的文本末尾必须再钉一次硬边界
		"不能直接用于申报",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("税务表文本里缺少 %q，实际：\n%s", want, text)
		}
	}
}

func TestTaxReturnToolValidatesKindAndPeriod(t *testing.T) {
	tool := GetTaxReturnTool(fakeTax{&TaxReturnBrief{Period: "2025-03"}})
	cases := []string{
		`{"kind":"stamp","period":"2025-03"}`,
		`{"kind":"vat","period":"上个月"}`,
		`{"kind":"","period":"2025-03"}`,
	}
	for _, raw := range cases {
		if _, err := tool.Run(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("参数 %s 应当被拦下", raw)
		}
	}
	if _, err := tool.Run(context.Background(),
		json.RawMessage(`{"kind":"cit","period":"2025-03"}`)); err != nil {
		t.Errorf("正常参数不该报错：%v", err)
	}
	// 没有数据源时要给一句能看懂的话，而不是 panic
	empty := GetTaxReturnTool(nil)
	if _, err := empty.Run(context.Background(),
		json.RawMessage(`{"kind":"vat","period":"2025-03"}`)); err == nil {
		t.Error("没有数据源时应当报错")
	}
}
