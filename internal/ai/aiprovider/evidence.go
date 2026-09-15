package aiprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 审计证据链（只读）
// ---------------------------------------------------------------------------
//
// 会计 agent 要能回答「这笔调整凭什么调」「这期的结论有没有依据」。
// 它只能读：证据的挂与摘都改底稿，属于写操作。
//
// ★ 这里**不判断原件还在不在**（那是文件系统的事，账套层才知道）。
// 所以工具说明里点名让模型不要替用户断言「原件在」——
// 它只能说「底稿上记着有这么一份资料」。要核对原件，
// 让用户到「审计底稿」页点「核对原件」。

// EvidenceItemBrief 是一份依据。
type EvidenceItemBrief struct {
	// Kind 是证据种类的中文名（附件 / 凭证 / 发票 / 外部资料…）。
	Kind string
	// Label 是这份资料的样子（快照）。
	Label string
	// Note 是它说明了什么。
	Note string
	// By 是谁挂上的。
	By string
	// Missing 为真表示这份原件现在找不到了。
	Missing bool
}

// EvidenceChainBrief 是一条结论的证据链。
type EvidenceChainBrief struct {
	// Owner 是结论的种类中文名（重要性水平 / 审计调整 / 底稿结论）。
	Owner string
	// Title 是结论本身。
	Title string
	// Items 是它的依据。
	Items []EvidenceItemBrief
	// Files / Documents 是附件类与单据类的份数。
	Files     int
	Documents int
	// Missing 是已经找不到原件的份数。
	Missing int
}

// EvidenceBrief 是一期的证据链。
type EvidenceBrief struct {
	Period string
	Chains []EvidenceChainBrief
	// Unsupported 是一条依据都没有的结论条数。
	Unsupported int
	// Broken 是**依据已经找不到**（原件被删、单据被删）的结论条数。
	//
	// ★ 必须有这一项：只报「有几份依据」而不报「那份依据还在不在」，
	// 模型就会向用户断言「这笔调整有依据」，而原件早已不在账套里 ——
	// 那正是证据链这一层存在的唯一理由。
	Broken int
	// Concludes 是一句话总结。
	Concludes string
}

// EvidenceProvider 提供审计证据链。
type EvidenceProvider interface {
	// EvidenceBrief 返回某期的证据链。
	//
	// 方法名与 service.Service 上已有的 Evidence（返回界面形状）区分开：
	// 同一个类型上不能有两个同名方法，而 AI 与界面必须共用同一条取数路径。
	EvidenceBrief(ctx context.Context, k PeriodKey) (*EvidenceBrief, error)
}

// GetEvidenceTool 构造「读审计证据链」工具。
func GetEvidenceTool(p EvidenceProvider) Tool {
	return Tool{
		Name: "get_evidence",
		Description: "读某期审计底稿的证据链：每项结论（重要性水平、每笔审计调整、底稿结论）" +
			"后面挂了哪些依据（附件 / 凭证 / 发票 / 银行流水 / 报销单 / 合同 / 外部资料）。" +
			"回答「这笔调整凭什么调」「这个结论有没有依据」用它。" +
			"★ 它只能说明底稿上**记着**哪些依据，不能说明原件还在不在 —— " +
			"不要替用户断言「原件已取得」，要核对原件请让他到「审计底稿」页点「核对原件」。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "period": {"type": "string", "description": "会计期间，格式 2026-09"}
  },
  "required": ["period"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Period string `json:"period"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("参数格式错误: %w", err)
			}
			k, err := parsePeriod(args.Period)
			if err != nil {
				return "", err
			}
			e, err := p.EvidenceBrief(ctx, k)
			if err != nil {
				return "", err
			}
			return renderEvidence(e), nil
		},
	}
}

// renderEvidence 把证据链渲染成中文文本。
func renderEvidence(e *EvidenceBrief) string {
	if e == nil {
		return "没有读到审计证据链。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s 审计证据链\n", e.Period)
	if e.Concludes != "" {
		b.WriteString(e.Concludes + "\n")
	}
	if e.Unsupported > 0 {
		fmt.Fprintf(&b, "★ 其中 %d 项结论还没有任何依据 —— "+
			"这类漏洞要在出报告前补上，不要替用户编一份不存在的依据。\n", e.Unsupported)
	}
	if e.Broken > 0 {
		fmt.Fprintf(&b, "★ 另有 %d 项结论的依据**已经找不到原件**（文件被清理或单据被删）—— "+
			"**不要**对用户说这些结论「有依据」；要如实说原件已不在账套里，"+
			"并建议重新附一份或改写成文字说明。\n", e.Broken)
	}
	for _, c := range e.Chains {
		fmt.Fprintf(&b, "\n【%s】%s\n", c.Owner, c.Title)
		if len(c.Items) == 0 {
			b.WriteString("（没有依据）\n")
			continue
		}
		fmt.Fprintf(&b, "依据 %d 份（附件 %d、单据 %d）", len(c.Items), c.Files, c.Documents)
		if c.Missing > 0 {
			fmt.Fprintf(&b, "，其中 %d 份**原件已找不到**", c.Missing)
		}
		b.WriteString("：\n")
		for _, it := range c.Items {
			fmt.Fprintf(&b, "- [%s] %s", it.Kind, it.Label)
			if it.Note != "" {
				fmt.Fprintf(&b, "；说明：%s", it.Note)
			}
			if it.Missing {
				b.WriteString("　★ 原件已找不到")
			}
			if it.By != "" {
				fmt.Fprintf(&b, "（%s 挂上）", it.By)
			}
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 提议挂依据（需要用户确认）
// ---------------------------------------------------------------------------
//
// 模型能查到「这笔调整还没有依据」，也能查到账上有一张对应的凭证 ——
// 但它**不能自己把两者挂上**：证据链是底稿的一部分，
// 挂错了等于给结论换了一个出处，而复核人是照着底稿核的。
//
// 所以走与建档案、登记调整同一条路：提议 → 用户确认 → 界面调绑定。

// EvidenceProposal 是「建议把某份资料挂到某个结论下」。
type EvidenceProposal struct {
	// OwnerType 是结论种类：adjustment | materiality | conclusion。
	OwnerType string `json:"owner_type"`
	// OwnerID 是结论 id：审计调整用调整行 id；
	// 重要性水平与底稿结论用期间号（如 202503）。
	OwnerID int64 `json:"owner_id"`
	// RefKind 是资料种类：voucher | invoice | bank_flow | expense | contract | external。
	RefKind string `json:"ref_kind"`
	// RefID 是单据 id（voucher / invoice / bank_flow / expense 必填）。
	RefID int64 `json:"ref_id"`
	// RefLabel 是这份资料是什么（外部资料必填；单据类可留空，由程序按 id 查名字）。
	RefLabel string `json:"ref_label"`
	// Note 是它说明了什么。
	Note string `json:"note"`
	// Reason 是为什么要挂它（会显示给用户）。
	Reason string `json:"reason"`
}

// 结论种类（与 domain/evidence 同一套取值）。
const (
	EvidenceOwnerMateriality = "materiality"
	EvidenceOwnerAdjustment  = "adjustment"
	EvidenceOwnerConclusion  = "conclusion"
)

// Valid 报告提议是否成形。
func (p EvidenceProposal) Valid() error {
	switch p.OwnerType {
	case EvidenceOwnerMateriality, EvidenceOwnerAdjustment, EvidenceOwnerConclusion:
	default:
		return fmt.Errorf("结论种类 %q 不认识（只能是 materiality / adjustment / conclusion）", p.OwnerType)
	}
	if p.OwnerID <= 0 {
		return fmt.Errorf("缺少结论 id")
	}
	switch p.RefKind {
	case "voucher", "invoice", "bank_flow", "expense":
		if p.RefID <= 0 {
			return fmt.Errorf("%s 类资料必须给出单据 id", p.RefKind)
		}
	case "contract", "external":
		// 这两类没有账套单据，必须有文字描述
		if strings.TrimSpace(p.RefLabel) == "" {
			return fmt.Errorf("%s 类资料必须写清它是什么", p.RefKind)
		}
	case "attachment":
		return fmt.Errorf("附件要上传文件，模型不能提议挂附件 —— " +
			"请让用户到「审计底稿」页点「附上资料」上传，或改指凭证 / 发票 / 外部资料")
	default:
		return fmt.Errorf("资料种类 %q 不认识", p.RefKind)
	}
	return nil
}

// ProposeEvidenceTool 构造「提议挂一份依据」工具（终止型）。
func ProposeEvidenceTool() Tool {
	return Tool{
		Name: "propose_evidence",
		Description: "发现某条结论缺少依据、而账套里正好有对应的凭证 / 发票 / 银行流水 / " +
			"报销单时，用它提议把这份资料挂上去，然后停下来等用户确认。" +
			"结论 id：审计调整用 get_workpaper 返回的调整 id；" +
			"重要性水平与底稿结论用**期间号**（如 2025-03 写作 202503）。" +
			"★ 附件（扫描件）不在这个工具的范围内 —— 那要用户自己上传。" +
			"reason 写清为什么这份资料能支撑这条结论。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "owner_type": {"type": "string", "enum": ["materiality", "adjustment", "conclusion"],
                   "description": "结论种类"},
    "owner_id": {"type": "integer",
                 "description": "结论 id：调整用调整 id；重要性水平/底稿结论用期间号如 202503"},
    "ref_kind": {"type": "string", "enum": ["voucher", "invoice", "bank_flow", "expense", "contract", "external"],
                 "description": "资料种类"},
    "ref_id": {"type": "integer", "description": "单据 id（凭证/发票/流水/报销单必填）"},
    "ref_label": {"type": "string", "description": "这份资料是什么（外部资料必填）"},
    "note": {"type": "string", "description": "它说明了什么"},
    "reason": {"type": "string", "description": "为什么这份资料能支撑这条结论。必填"}
  },
  "required": ["owner_type", "owner_id", "ref_kind", "reason"]
}`),
		Terminal: true,
		RenderAsk: func(args json.RawMessage) string {
			p, err := ParseEvidenceProposal(string(args))
			if err != nil {
				return ""
			}
			return fmt.Sprintf("（提议为%s挂一份依据：%s）%s",
				ownerLabelOf(p.OwnerType), refLabelOf(p), p.Reason)
		},
	}
}

// ParseEvidenceProposal 解析 propose_evidence 的参数。
func ParseEvidenceProposal(raw string) (*EvidenceProposal, error) {
	var p EvidenceProposal
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	p.OwnerType = strings.TrimSpace(p.OwnerType)
	p.RefKind = strings.TrimSpace(p.RefKind)
	p.RefLabel = strings.TrimSpace(p.RefLabel)
	p.Note = strings.TrimSpace(p.Note)
	p.Reason = strings.TrimSpace(p.Reason)
	if p.Reason == "" {
		return nil, fmt.Errorf("要写清为什么这份资料能支撑这条结论")
	}
	if err := p.Valid(); err != nil {
		return nil, err
	}
	return &p, nil
}

// ownerLabelOf 返回结论种类的中文名。
func ownerLabelOf(t string) string {
	switch t {
	case EvidenceOwnerMateriality:
		return "重要性水平"
	case EvidenceOwnerAdjustment:
		return "审计调整"
	case EvidenceOwnerConclusion:
		return "底稿结论"
	default:
		return t
	}
}

// refLabelOf 给出资料的一句话描述。
func refLabelOf(p *EvidenceProposal) string {
	if p.RefLabel != "" {
		return p.RefLabel
	}
	return fmt.Sprintf("%s #%d", p.RefKind, p.RefID)
}
