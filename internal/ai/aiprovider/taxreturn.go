package aiprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"miniaccount/internal/domain/money"
)

// ---------------------------------------------------------------------------
// 税务计算表（只读）
// ---------------------------------------------------------------------------
//
// 「这个月要交多少增值税」「这个季度预缴多少所得税」——
// 会计 agent 答这类问题的唯一正当来源是账套里那张计算表，
// 而不是它自己按记忆里的税率算一遍。
//
// ★ 所以它只读，而且读的是与界面上**完全相同**的那张表
// （同一个 TaxReturnProvider，同一条计算路径）。
// 让模型自己算税，最坏的结果不是算错一位数，而是它用了一个
// 记忆里过期的优惠政策，还说得头头是道。
//
// ★ 硬边界：算出来的东西**不能直接用于申报**。工具说明里写明，
// 模型在回答里也必须说清这一点。

// TaxReturnBriefRow 是表的一行。
type TaxReturnBriefRow struct {
	Line   string
	Label  string
	Amount money.Money
	Source string
	Note   string
}

// TaxReturnBrief 是一张税务计算表的紧凑形态。
type TaxReturnBrief struct {
	Kind      string
	KindLabel string
	Period    string
	Title     string
	Rows      []TaxReturnBriefRow
	// Keys 是几个关键数（应纳税额等）。
	Keys []TaxReturnBriefRow
	// Warnings 是算的过程中发现的问题。
	Warnings []string
	// Identities 是用的身份与口径（纳税人身份、累计口径…）。
	Identities []string
	// PolicyNote 是政策与免责说明。
	PolicyNote string
	// Concludes 是一句话结论。
	Concludes string
	// FilingStatus 是本期的申报状态（如「已申报并缴纳」）；空表示还没登记。
	//
	// ★ 模型必须知道「这期报了没」：不知道的话，它会在用户其实
	// 已经报过的期间上，建议「该去申报了」。
	FilingStatus string
	// FilingHint 是申报状态的说明（没报 / 报了但账后来改了）。
	FilingHint string
}

// TaxReturnProvider 提供税务计算表。
type TaxReturnProvider interface {
	// TaxReturnBrief 生成某期某税种的计算表。
	// kind: vat（增值税及附加）| cit（企业所得税）| iit（个人所得税）
	TaxReturnBrief(ctx context.Context, kind string, year, month int) (*TaxReturnBrief, error)
}

// GetTaxReturnTool 构造「读税务计算表」工具。
func GetTaxReturnTool(p TaxReturnProvider) Tool {
	return Tool{
		Name: "get_tax_return",
		Description: "读某期的税务计算表：增值税及附加、企业所得税（季度预缴）、" +
			"个人所得税（工资薪金累计预扣）。回答「这个月要交多少税」「预缴多少所得税」" +
			"用它 —— 这是账套里正规算出来的口径，**不要自己按记忆里的税率算**。" +
			"★ 表上的数不能直接用于申报：政策有生效期、企业有特殊情况，" +
			"回答时必须说清「这是计算表草稿，请到电子税务局核对后再申报」。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "kind": {"type": "string", "enum": ["vat", "cit", "iit"],
             "description": "vat 增值税及附加；cit 企业所得税；iit 个人所得税（工资薪金）"},
    "period": {"type": "string", "description": "会计期间，格式 2026-09"}
  },
  "required": ["kind", "period"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Kind   string `json:"kind"`
				Period string `json:"period"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("参数格式错误: %w", err)
			}
			kind := strings.TrimSpace(args.Kind)
			switch kind {
			case "vat", "cit", "iit":
			default:
				return "", fmt.Errorf("税种 %q 不认识（只能是 vat / cit / iit）", args.Kind)
			}
			k, err := parsePeriod(args.Period)
			if err != nil {
				return "", err
			}
			if p == nil {
				return "", fmt.Errorf("当前没有可用的税务计算表数据源")
			}
			t, err := p.TaxReturnBrief(ctx, kind, k.Year, k.Month)
			if err != nil {
				return "", err
			}
			return renderTaxReturn(t), nil
		},
	}
}

// renderTaxReturn 把税务计算表渲染成中文文本。
func renderTaxReturn(t *TaxReturnBrief) string {
	if t == nil {
		return "没有读到税务计算表。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s（%s）：%s\n", t.KindLabel, t.Period, t.Title)
	for _, id := range t.Identities {
		fmt.Fprintf(&b, "口径：%s\n", id)
	}
	if t.Concludes != "" {
		b.WriteString(t.Concludes + "\n")
	}
	if t.FilingStatus != "" {
		fmt.Fprintf(&b, "本期申报状态：**%s**。%s\n", t.FilingStatus, t.FilingHint)
	} else {
		fmt.Fprintf(&b, "本期申报状态：还没登记申报记录。%s\n", t.FilingHint)
	}
	b.WriteString("\n【关键数】\n")
	for _, k := range t.Keys {
		fmt.Fprintf(&b, "%s：%s", k.Label, k.Amount)
		if k.Note != "" {
			fmt.Fprintf(&b, "（%s）", k.Note)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n【计算过程】\n")
	for _, r := range t.Rows {
		line := r.Line
		if line != "" {
			line = "行" + line + " "
		}
		fmt.Fprintf(&b, "%s%s：%s", line, r.Label, r.Amount)
		if r.Source != "" {
			fmt.Fprintf(&b, "　← %s", r.Source)
		}
		if r.Note != "" {
			fmt.Fprintf(&b, "（%s）", r.Note)
		}
		b.WriteByte('\n')
	}
	if len(t.Warnings) > 0 {
		b.WriteString("\n【要注意】\n")
		for _, w := range t.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}
	if t.PolicyNote != "" {
		fmt.Fprintf(&b, "\n口径说明：%s\n", t.PolicyNote)
	}
	b.WriteString("\n★ 这是计算表草稿，**不能直接用于申报**：" +
		"请到电子税务局按申报表逐行核对，政策以申报时的最新规定为准。\n")
	return b.String()
}
