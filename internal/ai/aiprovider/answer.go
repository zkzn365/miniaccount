package aiprovider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 专业答复：与「记一笔账」并列的第二种产出
// ---------------------------------------------------------------------------
//
// # 为什么需要它
//
// 这个 agent 现在有两类任务：
//
//	用户在描述一笔业务   → 产出一张**凭证草稿**（已有契约，护栏盯着）
//	用户在问别的任何事   → 产出一份**专业答复**（本节）
//
// 第二类以前没有契约，模型只能硬塞进凭证 JSON —— 于是「这个月社保扣多少」
// 会被它翻译成一张莫名其妙的凭证，或者干脆答不出来。
//
// # 为什么是 JSON 而不是自由文字
//
// 因为「输出要求」里那十项（结论、依据、已取得、缺失、过程、问题、
// 风险等级、建议、需人工判断、能否对外提交）**必须能被程序检查**。
//
// 自由文字的问题不是不好看，是**没法拦**：模型可以把「未核实」
// 写成确定事实，而没有任何一处代码能发现。拆成字段之后，
// 「缺失资料为空」「风险等级没填」「声称可以直接申报」这些都能被挡下来 ——
// 见 Answer.Validate 与服务层的强制改写。

// Answer 是一次专业答复。
//
// 字段与「输出要求」那十项一一对应，一个不多一个不少：
// 多一个字段模型就会开始自由发挥，少一个就会漏掉一项要求。
type Answer struct {
	// Conclusion 是一句话结论。
	Conclusion string `json:"conclusion"`
	// Basis 是适用依据（法律/准则/政策名称与条款）。
	Basis []string `json:"basis"`
	// Obtained 是已取得的资料。
	Obtained []string `json:"obtained"`
	// Missing 是缺失的资料与待确认事项。
	Missing []string `json:"missing"`
	// Process 是计算或检查过程（含金额与算式）。
	Process string `json:"process"`
	// Findings 是发现的问题。
	Findings []string `json:"findings"`
	// Risk 是风险等级：低 / 中 / 高。
	Risk string `json:"risk"`
	// Recommendations 是调整或处理建议。
	Recommendations []string `json:"recommendations"`
	// HumanReview 是需要人工注册会计师判断的事项。
	HumanReview []string `json:"humanReview"`
	// Submittable 是模型声称「能否用于正式申报或对外提交」。
	//
	// ★ 这个值**不可信**，服务层一律压成 false（见 service 里的说明）。
	// 留字段是为了审计：模型说的和实际生效的都要留痕。
	Submittable bool `json:"submittable"`
	// PolicyNote 是政策的发布机关/适用地区/适用期间/查询日期。
	PolicyNote string `json:"policyNote"`
	// Text 是给用户看的正文。
	Text string `json:"text"`

	// RiskUnstated 为真表示模型没有（或没有按三个取值）标注风险等级。
	//
	// ★ 这个字段是**解析出来的**，不是模型给的 —— 不参与 JSON。
	// 它单独存在是因为「没标注」和「标了中等」是两件事：
	// 前者要在界面上提醒，后者不用。
	RiskUnstated bool `json:"-"`
	// RiskRaw 是模型原样给的风险等级（用于把话说明白）。
	RiskRaw string `json:"-"`
}

// 风险等级。
const (
	RiskLow    = "低"
	RiskMedium = "中"
	RiskHigh   = "高"
)

// answerEnvelope 是模型返回的外层结构：两种产出二选一。
type answerEnvelope struct {
	Answer *Answer `json:"answer"`
	// Voucher 走原有的 ai.Parse 通道，这里只用于判别。
	Voucher json.RawMessage `json:"voucher"`
}

// ParseAnswer 尝试把模型返回的内容解析成一份专业答复。
//
// 返回 (answer, true) 表示这确实是一份答复；
// (nil, false) 表示外层不是答复（那它就应当是一张凭证，走原有护栏）。
//
// ★ 判别刻意做得**显式**：只看有没有 answer 字段，不去猜。
// 「先试解析成凭证，失败了再当文字」这种回退写法最省事，
// 也最容易把一张写坏的凭证悄悄降级成一段看着挺像回事的分析 ——
// 而那张凭证本该校验失败、本该报错。
func ParseAnswer(raw string) (*Answer, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return nil, false, nil
	}
	var env answerEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		// 连 JSON 都不是：交给凭证那条路去报「解析失败」，
		// 那里的错误信息更准确
		return nil, false, nil
	}
	if env.Answer == nil {
		return nil, false, nil
	}
	env.Answer.Normalize()
	return env.Answer, true, nil
}

// Normalize 去掉空白、把风险等级归一到三个取值之一。
//
// ★ 导出并且**由使用方显式调用**（见 service 层），而不是只在
// ParseAnswer 里调一次。
//
// 原因是它维护的是一条不变式：「风险等级只能是低/中/高」。
// 只在解析入口归一，任何直接构造 Answer 的代码（测试、
// 将来别的解析路径）都会绕过它 —— 而绕过之后的表现是
// 风险等级原样透传到界面，用户看到「风险 随便」。
// 这条不变式是安全相关的（未标注必须按中等处理），不能靠调用顺序维持。
func (a *Answer) Normalize() {
	a.Conclusion = strings.TrimSpace(a.Conclusion)
	a.Process = strings.TrimSpace(a.Process)
	a.Risk = strings.TrimSpace(a.Risk)
	a.PolicyNote = strings.TrimSpace(a.PolicyNote)
	a.Text = strings.TrimSpace(a.Text)
	a.Basis = cleanList(a.Basis)
	a.Obtained = cleanList(a.Obtained)
	a.Missing = cleanList(a.Missing)
	a.Findings = cleanList(a.Findings)
	a.Recommendations = cleanList(a.Recommendations)
	a.HumanReview = cleanList(a.HumanReview)

	switch a.Risk {
	case "低", "low", "LOW":
		a.Risk = RiskLow
	case "中", "medium", "MEDIUM":
		a.Risk = RiskMedium
	case "高", "high", "HIGH":
		a.Risk = RiskHigh
	default:
		// 模型没填或填了别的：按「中」处理，不能当低 ——
		// 把没标注的东西当成低风险，正是这一节要防的事
		a.RiskRaw = a.Risk
		a.Risk = RiskMedium
		a.RiskUnstated = true
	}
}

func cleanList(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if s := strings.TrimSpace(x); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Validate 检查这份答复有没有丢掉「输出要求」里的关键项。
//
// 返回的是**问题清单**，不是「合不合法」：空着不一定错
// （确实没有缺失资料、确实没有发现问题都正常），
// 但**结论为空**、**风险等级没标注**一定是错的。
func (a *Answer) Validate() []string {
	var problems []string
	if a.Conclusion == "" && a.Text == "" {
		problems = append(problems, "结论为空：既没有 conclusion 也没有 text，"+
			"用户拿到的是个空壳")
	}
	if a.RiskUnstated {
		problems = append(problems, "风险等级没标注：模型给的是「"+
			a.RiskRaw+"」，已按中等处理")
	}
	if a.Submittable {
		// 见服务层：这个值会被强制压回 false，这里先记一笔
		problems = append(problems, "声称可直接用于正式申报或对外提交 —— "+
			"AI 产出的东西一律不能直接对外")
	}
	return problems
}

// EscalationReasons 给出**必须转人工**的命中项（空表示没有命中）。
//
// # 为什么用关键词做安全网
//
// 这一层是**兜底**，不是主要判定 —— 主要判定在提示词里（模型自己
// 把事项写进 humanReview）。兜底之所以要做，是因为漏报的代价不对称：
//
//	多报一次 → 用户多找一个会计看一眼
//	漏报一次 → 他拿着没核过的东西去申报
//
// 所以宁可误报。关键词命中的是模型**自己的答复文本**，
// 里面出现「保留意见」「持续经营」「舞弊」这些词，
// 十有八九是它真在讨论这类问题；偶尔是「本次不涉及诉讼」这种否定句，
// 那也让它复核一遍，不亏。
func (a *Answer) EscalationReasons() []string {
	hay := a.Conclusion + "\n" + a.Process + "\n" + a.Text
	for _, f := range a.Findings {
		hay += "\n" + f
	}
	for _, r := range a.Recommendations {
		hay += "\n" + r
	}

	var reasons []string
	add := func(label string, keys ...string) {
		for _, k := range keys {
			if strings.Contains(hay, k) {
				reasons = append(reasons, label)
				return
			}
		}
	}
	add("可能涉及审计意见类型（保留意见 / 否定意见 / 无法表示意见）",
		"保留意见", "否定意见", "无法表示意见")
	add("可能涉及造假、舞弊或虚假交易", "造假", "舞弊", "虚假交易", "虚假报告")
	add("涉及持续经营重大不确定性", "持续经营")
	add("涉及重大未披露关联交易", "未披露关联交易", "关联方交易未")
	add("涉及税务处罚、行政调查或诉讼", "税务处罚", "行政处罚", "税务稽查", "诉讼")
	add("涉及上市公司、金融机构或证券业务", "上市公司", "金融机构", "证券服务")
	add("涉及跨境业务", "跨境", "境外付款", "境外支付")
	add("涉及签字、盖章、申报或对外提交",
		"签字", "盖章", "正式申报", "对外提交", "对外提供")

	if a.Risk == RiskHigh {
		reasons = append(reasons, "模型自评为高风险")
	}
	if len(a.HumanReview) > 0 {
		reasons = append(reasons, "模型自己列出了需要人工判断的事项")
	}
	return reasons
}

// String 给审计与日志用的一句话摘要。
func (a *Answer) String() string {
	if a == nil {
		return ""
	}
	return fmt.Sprintf("风险%s｜依据 %d 条｜缺失 %d 项｜建议人工复核 %d 项",
		a.Risk, len(a.Basis), len(a.Missing), len(a.HumanReview))
}
