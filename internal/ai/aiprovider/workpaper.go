package aiprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"miniaccount/internal/domain/money"
)

// ---------------------------------------------------------------------------
// 审计底稿：读现状 + 提议调整
// ---------------------------------------------------------------------------
//
// 与账务查询那几个工具一样，读底稿是**只读**的：模型看得到重要性水平、
// 看得到未更正错报合计，才能回答「这几笔加起来算不算重大」。
//
// 而「登记一笔审计调整」是写底稿的动作，所以走
// **提议 + 用户确认**：模型调 propose_adjustment 就结束这一轮，
// 界面把提议摊成一张卡，用户按下确认才真的落进底稿。
//
// ★ 这与「AI 产物一律先是草稿」是同一条边界。
// 底稿不是账簿，但它决定账要怎么改 —— 更不能让模型自己动手。

// MaterialityBrief 是重要性水平的紧凑形态。
type MaterialityBrief struct {
	// Benchmark 是基准名，如「资产总额」。
	Benchmark string
	// BenchmarkAmount 是基准金额。
	BenchmarkAmount money.Money
	// Overall / Performance / Trivial 是三个门槛。
	Overall     money.Money
	Performance money.Money
	Trivial     money.Money
	// Note 是判断说明。
	Note string
}

// AdjustmentBrief 是一笔已登记的调整。
type AdjustmentBrief struct {
	Code     string
	Kind     string
	Summary  string
	Reason   string
	Evidence string
	Amount   money.Money
	// Booked 为真表示已经生成过调整凭证。
	Booked bool
	// Posted 为真表示那张凭证已经过账（调整的影响已在账面数里）。
	Posted bool
}

// WorkpaperBrief 是某期底稿的紧凑形态。
type WorkpaperBrief struct {
	Period string
	// Materiality 为 nil 表示还没确定重要性水平。
	Materiality *MaterialityBrief
	// Adjustments 是已登记的调整。
	Adjustments []AdjustmentBrief
	// MisstatementTotal 是未更正错报合计（只含影响损益的调整）。
	MisstatementTotal money.Money
	// MisstatementCount / ReclassCount 是笔数。
	MisstatementCount int
	ReclassCount      int
	// Concludes 是服务层给的一句话结论（含门槛比较）。
	Concludes string
}

// WorkpaperProvider 提供审计底稿。
type WorkpaperProvider interface {
	Workpaper(ctx context.Context, k PeriodKey) (*WorkpaperBrief, error)
}

// GetWorkpaperTool 构造「读审计底稿」工具。
func GetWorkpaperTool(p WorkpaperProvider) Tool {
	return Tool{
		Name: "get_workpaper",
		Description: "读某期的审计底稿：重要性水平（整体 / 实际执行 / 明显微小三个门槛）、" +
			"未更正错报合计与结论、已登记的审计调整清单。" +
			"回答「这个月赚了多少」用 get_report；回答「这几笔错报加起来算不算重大」" +
			"「重要性水平定了多少」用这个。",
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
			w, err := p.Workpaper(ctx, k)
			if err != nil {
				return "", err
			}
			return renderWorkpaper(w), nil
		},
	}
}

// renderWorkpaper 把底稿渲染成中文文本。
//
// 与别的工具一样给中文而不是 JSON：模型接下来要用它组织中文回答，
// 给它字段名它得先翻译一遍，而翻译正是最容易出错的一步。
func renderWorkpaper(w *WorkpaperBrief) string {
	if w == nil {
		return "没有读到审计底稿。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s 审计底稿\n", w.Period)

	b.WriteString("\n【重要性水平】\n")
	if w.Materiality == nil {
		b.WriteString("尚未确定 —— 没有门槛就没法判断未更正错报是否重大。\n")
		b.WriteString("请提醒用户先定重要性水平（基准 × 比例），不要在对话里替他编一个数。\n")
	} else {
		m := w.Materiality
		fmt.Fprintf(&b, "基准：%s %s\n", m.Benchmark, m.BenchmarkAmount)
		fmt.Fprintf(&b, "整体重要性：%s\n", m.Overall)
		fmt.Fprintf(&b, "实际执行重要性：%s\n", m.Performance)
		fmt.Fprintf(&b, "明显微小错报临界值：%s\n", m.Trivial)
		if m.Note != "" {
			fmt.Fprintf(&b, "判断说明：%s\n", m.Note)
		}
	}

	b.WriteString("\n【未更正错报汇总】\n")
	fmt.Fprintf(&b, "合计 %s（%d 笔，其中重分类 %d 笔不计入合计）\n",
		w.MisstatementTotal, w.MisstatementCount, w.ReclassCount)
	if w.Concludes != "" {
		b.WriteString(w.Concludes + "\n")
	}

	b.WriteString("\n【已登记的审计调整】\n")
	if len(w.Adjustments) == 0 {
		b.WriteString("（无）\n")
	}
	for _, a := range w.Adjustments {
		state := "未入账"
		switch {
		case a.Posted:
			state = "已入账（凭证已过账）"
		case a.Booked:
			state = "已生成凭证（草稿，待账期结算过账）"
		}
		fmt.Fprintf(&b, "%s %s %s %s —— %s；依据：%s",
			a.Code, a.Kind, a.Summary, a.Amount, state, a.Reason)
		if a.Evidence != "" {
			fmt.Fprintf(&b, "；证据：%s", a.Evidence)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 提议登记审计调整（需要用户确认）
// ---------------------------------------------------------------------------

// AdjustmentLineProposal 是调整分录的一行。
type AdjustmentLineProposal struct {
	// AccountCode 是科目编码。
	AccountCode string `json:"account_code"`
	// Summary 是本行摘要（可空，落库时用调整摘要补）。
	Summary string `json:"summary"`
	// Debit / Credit 是金额，单位**分**（与凭证契约同一套：
	// 12.34 元写作 1234。用元会逼模型做小数运算，那正是最容易错的地方）。
	Debit  money.Money `json:"debit"`
	Credit money.Money `json:"credit"`
	// 辅助核算 id（与凭证契约一致，snake_case）。
	ContactID  *int64 `json:"contact_id"`
	EmployeeID *int64 `json:"employee_id"`
	DeptID     *int64 `json:"dept_id"`
	ProjectID  *int64 `json:"project_id"`
}

// AdjustmentProposal 是「建议登记一笔审计调整」。
//
// ★ 模型只能**提议**，不能自己登记。
type AdjustmentProposal struct {
	// Period 是调整所属期间，格式 2026-09。
	Period string `json:"period"`
	// Kind 是 adjust（调整）| reclass（重分类）。
	Kind string `json:"kind"`
	// Summary 是调整摘要。
	Summary string `json:"summary"`
	// Reason 是调整依据（底稿的核心：为什么调）。
	Reason string `json:"reason"`
	// Evidence 是证据来源（哪份资料支持这笔调整）。
	Evidence string `json:"evidence"`
	// Lines 是分录，至少两行且借贷平衡。
	Lines []AdjustmentLineProposal `json:"lines"`
}

// 调整种类（与 domain/workpaper 同一套取值）。
const (
	AdjustKindAdjust  = "adjust"
	AdjustKindReclass = "reclass"
)

// TotalDebit / TotalCredit 是借贷合计。
func (p AdjustmentProposal) TotalDebit() money.Money {
	var s money.Money
	for _, l := range p.Lines {
		s = s.Add(l.Debit)
	}
	return s
}

// TotalCredit 返回贷方合计。
func (p AdjustmentProposal) TotalCredit() money.Money {
	var s money.Money
	for _, l := range p.Lines {
		s = s.Add(l.Credit)
	}
	return s
}

// Balanced 报告借贷是否相等。
func (p AdjustmentProposal) Balanced() bool {
	return p.TotalDebit() == p.TotalCredit() && p.TotalDebit().IsPositive()
}

// KindLabel 返回中文名。
func (p AdjustmentProposal) KindLabel() string {
	if p.Kind == AdjustKindReclass {
		return "重分类"
	}
	return "调整"
}

// Validate 检查提议本身是否成形。
//
// ★ 只挡**根本没法登记**的：期间读不出来、摘要或依据空着、借贷不平。
// 「科目存不存在、辅助核算齐不齐」不在这一层判 ——
// 那是服务层拿账套的真实科目树的活，这里没有账套。
// 用户按下确认时会在服务层再走一遍完整护栏，报错会显示在界面。
func (p AdjustmentProposal) Validate() error {
	if _, err := parsePeriod(p.Period); err != nil {
		return err
	}
	if p.Kind != AdjustKindAdjust && p.Kind != AdjustKindReclass {
		return fmt.Errorf("调整种类 %q 不认识（只能是 adjust 或 reclass）", p.Kind)
	}
	if strings.TrimSpace(p.Summary) == "" {
		return fmt.Errorf("缺少调整摘要")
	}
	if strings.TrimSpace(p.Reason) == "" {
		return fmt.Errorf("缺少调整依据 —— 没有依据的调整复核人无法判断该不该调")
	}
	if len(p.Lines) < 2 {
		return fmt.Errorf("调整分录至少需要两行，当前 %d 行", len(p.Lines))
	}
	for i, l := range p.Lines {
		if strings.TrimSpace(l.AccountCode) == "" {
			return fmt.Errorf("第 %d 行没有科目", i+1)
		}
		if l.Debit.IsNegative() || l.Credit.IsNegative() {
			return fmt.Errorf("第 %d 行的金额是负数 —— 红字调整请把借贷方向换过来写", i+1)
		}
		if l.Debit.IsPositive() && l.Credit.IsPositive() {
			return fmt.Errorf("第 %d 行借贷都有金额", i+1)
		}
		if l.Debit.IsZero() && l.Credit.IsZero() {
			return fmt.Errorf("第 %d 行没有金额", i+1)
		}
	}
	if !p.Balanced() {
		return fmt.Errorf("借贷不平衡：借 %s ≠ 贷 %s，差额 %s",
			p.TotalDebit(), p.TotalCredit(), p.TotalDebit().Sub(p.TotalCredit()))
	}
	return nil
}

// ProposeAdjustmentTool 构造「提议登记审计调整」工具（终止型）。
//
// ★ 终止型：调用它就结束这一轮，把提议交给用户确认。
// 用户点确认之后由**界面**调保存绑定，模型全程没有写库能力。
func ProposeAdjustmentTool() Tool {
	return Tool{
		Name: "propose_adjustment",
		Description: "发现账务错报、需要登记一笔审计调整时用它，然后停下来等用户确认。" +
			"只用于**审计口径的调整**（补提折旧、少计费用、往来重分类…）；" +
			"日常业务的记账用普通凭证那套，不要走这里。" +
			"amount 单位是**分**（12.34 元写 1234）。" +
			"reason 必须写清为什么调、evidence 写清依据哪份资料 —— " +
			"这两项是底稿的柱子，缺了复核人无法判断该不该调。" +
			"★ 不要用它登记「已经想好要改进账里」的日常凭证。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "period": {"type": "string", "description": "调整所属期间，格式 2026-09"},
    "kind": {"type": "string", "enum": ["adjust", "reclass"],
             "description": "adjust 调整（影响损益/资产负债）；reclass 重分类（只在报表项目间搬家，不影响损益）"},
    "summary": {"type": "string", "description": "调整摘要，如「补提 2026 年折旧」"},
    "reason": {"type": "string", "description": "调整依据：为什么调。必填"},
    "evidence": {"type": "string", "description": "证据来源：哪份资料支持这笔调整，如「折旧计算表（F-3）」"},
    "lines": {
      "type": "array",
      "description": "调整分录，至少两行，借贷必须相等",
      "items": {
        "type": "object",
        "properties": {
          "summary": {"type": "string", "description": "本行摘要，可留空"},
          "account_code": {"type": "string", "description": "科目编码，必须是 search_accounts 返回的明细科目"},
          "debit": {"type": "integer", "description": "借方金额（分），无则 0"},
          "credit": {"type": "integer", "description": "贷方金额（分），无则 0"},
          "contact_id": {"type": "integer", "description": "往来单位 id（客户/供应商/股东/其他单位）"},
          "employee_id": {"type": "integer", "description": "员工 id"},
          "dept_id": {"type": "integer", "description": "部门 id"},
          "project_id": {"type": "integer", "description": "项目 id"}
        },
        "required": ["account_code"]
      }
    }
  },
  "required": ["period", "kind", "summary", "reason", "lines"]
}`),
		Terminal: true,
		RenderAsk: func(args json.RawMessage) string {
			p, err := ParseAdjustmentProposal(string(args))
			if err != nil {
				return ""
			}
			return fmt.Sprintf("（提议登记%s %s：%s，%s）",
				p.KindLabel(), p.Period, p.Summary, p.TotalDebit())
		},
	}
}

// ParseAdjustmentProposal 解析 propose_adjustment 的参数。
func ParseAdjustmentProposal(raw string) (*AdjustmentProposal, error) {
	var p AdjustmentProposal
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	p.Period = strings.TrimSpace(p.Period)
	p.Kind = strings.TrimSpace(p.Kind)
	if p.Kind == "" {
		p.Kind = AdjustKindAdjust
	}
	p.Summary = strings.TrimSpace(p.Summary)
	p.Reason = strings.TrimSpace(p.Reason)
	p.Evidence = strings.TrimSpace(p.Evidence)
	for i := range p.Lines {
		p.Lines[i].AccountCode = strings.TrimSpace(p.Lines[i].AccountCode)
		p.Lines[i].Summary = strings.TrimSpace(p.Lines[i].Summary)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}
