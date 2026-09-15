package aiprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 审计与鉴证文书（只读）
// ---------------------------------------------------------------------------
//
// 会计 agent 要能回答「审计报告里怎么写的」「还差什么才能出报告」。
// 它只能读：生成与修改文书都是写操作。
//
// ★ 硬边界写死在工具说明与渲染文本里：
//
//	这是**草稿**，不是已经出具的报告。模型不能替注册会计师形成意见，
//	也不能说「报告已经出好了」—— 签字盖章才生效。
//
// 这一条必须由程序反复说。让模型用一份草稿的口吻回答
// 「审计意见是无保留」，用户很可能就此认为审计已经做完。

// AuditDocBriefSection 是文书的一段。
type AuditDocBriefSection struct {
	No     string
	Title  string
	Body   string
	Source string
}

// AuditDocBrief 是一份文书草稿的紧凑形态。
type AuditDocBrief struct {
	Kind      string
	KindLabel string
	Title     string
	Company   string
	Period    string
	// OpinionStr 仅审计报告有。
	OpinionStr string
	// Sections 是正文段落。
	Sections []AuditDocBriefSection
	// Missing 是还不能签发的原因。
	Missing []string
	// CanIssue 为真表示没有待补事项（仍然要签字盖章才生效）。
	CanIssue bool
	// Signature / PolicyNote 是生效条件与依据。
	Signature  string
	PolicyNote string
	FullText   string
}

// AuditDocProvider 提供审计与鉴证文书草稿。
type AuditDocProvider interface {
	// AuditDocBrief 生成某种文书的草稿。
	// kind: audit（审计报告）| capital（验资报告）| management（管理建议书）
	AuditDocBrief(ctx context.Context, kind string, year, month int) (*AuditDocBrief, error)
}

// GetAuditDocTool 构造「读审计文书草稿」工具。
func GetAuditDocTool(p AuditDocProvider) Tool {
	return Tool{
		Name: "get_audit_draft",
		Description: "读某期的审计与鉴证文书**草稿**：审计报告、验资报告、管理建议书。" +
			"回答「审计报告里怎么写的」「还差什么才能出报告」「给管理层提了哪些建议」用它。" +
			"★ 这是草稿，不是已经出具的报告：必须说清「草稿、待签字盖章」，" +
			"绝不能说「报告已出具」「审计意见已确定」——签字盖章才生效。" +
			"你也不能替注册会计师形成意见或修改意见类型。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "kind": {"type": "string", "enum": ["audit", "capital", "management"],
             "description": "audit 审计报告；capital 验资报告；management 管理建议书"},
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
			case "audit", "capital", "management":
			default:
				return "", fmt.Errorf("文书种类 %q 不认识（只能是 audit / capital / management）", args.Kind)
			}
			k, err := parsePeriod(args.Period)
			if err != nil {
				return "", err
			}
			if p == nil {
				return "", fmt.Errorf("当前没有可用的文书数据源")
			}
			d, err := p.AuditDocBrief(ctx, kind, k.Year, k.Month)
			if err != nil {
				return "", err
			}
			return renderAuditDocBrief(d), nil
		},
	}
}

// renderAuditDocBrief 把文书草稿渲染成中文文本。
func renderAuditDocBrief(d *AuditDocBrief) string {
	if d == nil {
		return "没有读到文书草稿。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "【%s草稿】（%s · %s）\n", d.KindLabel, d.Company, d.Period)
	if d.OpinionStr != "" {
		fmt.Fprintf(&b, "意见类型（由注册会计师选择）：%s\n", d.OpinionStr)
	}
	if d.CanIssue {
		b.WriteString("状态：草稿已成文，**待签字盖章后生效**。\n")
	} else {
		fmt.Fprintf(&b, "状态：★ **还不能签发**，还有 %d 项待补：\n", len(d.Missing))
		for i, m := range d.Missing {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, m)
		}
	}
	for _, s := range d.Sections {
		fmt.Fprintf(&b, "\n%s、%s\n%s\n", s.No, s.Title, s.Body)
		if s.Source != "" {
			fmt.Fprintf(&b, "（来源：%s）\n", s.Source)
		}
	}
	if d.Signature != "" {
		fmt.Fprintf(&b, "\n%s\n", d.Signature)
	}
	b.WriteString("\n★ 回答时请说清：这是软件生成的**草稿**，不是已出具的报告；" +
		"意见与措辞由签字注册会计师负责。\n")
	return b.String()
}
