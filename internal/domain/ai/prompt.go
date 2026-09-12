package ai

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 提示词配置
// ---------------------------------------------------------------------------
//
// # 为什么提示词要能改
//
// 「这笔业务该怎么记」在不同企业里答案不同：有的把运费计入销售费用，
// 有的计入营业成本；有的要求往来必须挂项目，有的不挂。
// 这些是**会计政策**，不是程序的 bug —— 写死在代码里，
// 用户只能每次生成后再手工改一遍，AI 的价值就没了。
//
// # 边界：能改什么、不能改什么
//
// 能改：记账原则、科目使用偏好、摘要写法、辅助核算要求、行业惯例。
// 不能改：输出格式（JSON 结构）与硬边界（只能用列出的科目、借贷必须相等）。
//   - 输出格式由护栏解析，改了会直接解析失败；
//   - 硬边界是「不许幻觉」的底线，删掉它等于把护栏关掉。
//
// 所以可编辑区是**固定骨架中间的一段**，而不是整份提示词。
// 界面必须把这件事说清楚，否则用户会以为「我改了没生效」。
const (
	// MaxInstructions 是自定义记账要求的长度上限。
	//
	// 8000 字节约 4000 个汉字，够写一份像样的会计政策了。
	// 再长就该考虑是不是把「科目表本身」抄进来了 ——
	// 科目清单由程序生成，抄进来只会过期。
	MaxInstructions = 8000
	// MaxTaskNote 是单个任务附加要求的长度上限。
	MaxTaskNote = 2000
)

// PromptConfig 是用户对提示词的定制。零值表示「全部用默认」。
type PromptConfig struct {
	// Instructions 是记账要求那一段的内容。
	//
	// 空串表示用出厂默认（见 aiprovider.DefaultInstructions）。
	// ★ 不要把默认值抄进来存下 —— 那样程序升级了默认提示词，
	// 用户的账套还停在旧版本上，而且看不出为什么。
	Instructions string `json:"instructions"`
	// TaskNotes 是按任务类型的附加要求，键为任务名（bank_flow / invoice /
	// expense / freeform）。
	//
	// 分任务是因为四类业务的关注点不同：银行流水最要紧的是「对方科目猜对」，
	// 报销单最要紧的是「费用科目与部门辅助核算」。
	TaskNotes map[string]string `json:"taskNotes,omitempty"`
	// UpdatedAt 是最后修改时间（RFC3339），仅供界面显示。
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// TaskNote 返回某任务的附加要求（没有则为空串）。
func (c PromptConfig) TaskNote(task string) string {
	if len(c.TaskNotes) == 0 {
		return ""
	}
	return strings.TrimSpace(c.TaskNotes[task])
}

// IsDefault 报告是否全部用默认值。
func (c PromptConfig) IsDefault() bool {
	if strings.TrimSpace(c.Instructions) != "" {
		return false
	}
	for _, v := range c.TaskNotes {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

// Normalize 清掉空白与空条目，让「看起来是空的」与「真的是空的」一致。
//
// 不做这一步的话，用户把编辑框全选删掉再保存，存进去的是一串空格与换行，
// 之后 IsDefault 判定为 false、界面显示「已自定义」，
// 而实际提示词里多了一段空白。
func (c PromptConfig) Normalize() PromptConfig {
	out := PromptConfig{
		Instructions: strings.TrimSpace(c.Instructions),
		UpdatedAt:    c.UpdatedAt,
	}
	for k, v := range c.TaskNotes {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		if out.TaskNotes == nil {
			out.TaskNotes = map[string]string{}
		}
		out.TaskNotes[k] = v
	}
	return out
}

// Validate 检查长度等硬约束。
//
// 上限不是「怕模型读不完」，而是防止用户把整份科目表、整年的流水
// 贴进来 —— 那会让每次调用都慢且贵，而问题往往出在别处。
func (c PromptConfig) Validate() error {
	n := len([]rune(c.Instructions))
	if n > MaxInstructions {
		return fmt.Errorf("记账要求太长：%d 字，上限 %d 字", n, MaxInstructions)
	}
	for k, v := range c.TaskNotes {
		if n := len([]rune(v)); n > MaxTaskNote {
			return fmt.Errorf("「%s」的附加要求太长：%d 字，上限 %d 字", k, n, MaxTaskNote)
		}
	}
	return nil
}

// KnownTasks 是允许设置附加要求的任务名。
func KnownTasks() []string {
	return []string{"bank_flow", "invoice", "expense", "freeform"}
}

// ValidTask 判断任务名是否合法。
func ValidTask(t string) bool {
	for _, k := range KnownTasks() {
		if k == t {
			return true
		}
	}
	return false
}
