package aiprovider

import (
	"fmt"
	"sort"
	"strings"

	"miniaccount/internal/domain/ai"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/voucher"
)

// ---------------------------------------------------------------------------
// 输入
// ---------------------------------------------------------------------------

// Task 是要模型完成的记账任务。
type Task string

// 任务类型。
const (
	// TaskBankFlow 给一条银行流水建议分录。最常见、最有价值。
	TaskBankFlow Task = "bank_flow"
	// TaskInvoice 给一张发票建议分录。
	TaskInvoice Task = "invoice"
	// TaskExpense 给一张差旅报销单建议分录。
	TaskExpense Task = "expense"
	// TaskFreeform 自然语言记账，如「付了三个月房租 36000」。
	TaskFreeform Task = "freeform"
)

// Label 返回中文名。
func (t Task) Label() string {
	switch t {
	case TaskBankFlow:
		return "银行流水"
	case TaskInvoice:
		return "发票"
	case TaskExpense:
		return "报销单"
	case TaskFreeform:
		return "自然语言"
	default:
		return string(t)
	}
}

// Input 是构造提示词所需的全部素材。
//
// 注意这里**没有**原始敏感数据之外的东西：
// 账号、户名会按隐私设置决定是否脱敏后才填进来。
type Input struct {
	Task Task

	// Text 是待记账的原始描述（流水摘要、报销事由、用户原话）。
	Text string
	// Amount 是金额（分）。正为收入、负为支出，与银行流水口径一致。
	Amount money.Money
	// Date 是业务日期 YYYY-MM-DD。
	Date string
	// Counterparty 是对方户名 / 客户名，可能为空。
	Counterparty string
	// Direction 是资金方向的中文描述，如「收入」「支出」。
	Direction string
	// Extra 是补充信息（发票号码、税号、明细行摘要等），键值对。
	Extra map[string]string

	// ---- 上下文（由检索层提供）----

	// Book 是账套信息。
	Book BookContext
	// Accounts 是候选科目。**全量传入**（190 个），
	// 让模型在闭集里选，而不是自由生成 —— 这是消灭幻觉科目的关键。
	Accounts []AccountBrief
	// Contacts 是候选往来单位。
	Contacts []ContactBrief
	// Examples 是检索到的历史同类凭证，★ 最有效的信号。
	Examples []Example
	// Today 是今天的日期，用于让模型理解「上月」「本季度」这类相对时间。
	Today string

	// Prompt 是用户对提示词的定制。零值 = 全部用出厂默认。
	//
	// ★ 只影响中间那段「记账要求」，不影响硬边界与输出格式 ——
	// 那两段由护栏依赖，改了会直接解析失败。
	Prompt ai.PromptConfig
}

// BookContext 是账套背景。
type BookContext struct {
	CompanyName string
	// Standard 是会计准则，如「小企业会计准则」。
	Standard string
	// TaxType 是纳税人身份，如「一般纳税人」「小规模纳税人」。
	TaxType string
	// Currency 是记账本位币。
	Currency string
	// PeriodDesc 描述当前可记账期间，如「2025-03」。
	PeriodDesc string
}

// AccountBrief 是给模型看的科目摘要。
type AccountBrief struct {
	Code string
	Name string
	// FullName 含上级名称，如「管理费用—办公费」。
	FullName string
	// Direction 是余额方向：借/贷。
	Direction string
	// AuxTypes 是所需的辅助核算维度中文名。
	AuxTypes []string
}

// ContactBrief 是给模型看的往来单位摘要。
type ContactBrief struct {
	ID   int64
	Name string
	// Kind 是类型中文名，如「客户」。
	Kind string
	// Aliases 是别名/简称，如银行流水里出现的「支付宝-杭州XX科技」。
	Aliases []string
}

// Example 是一条历史凭证范例。
type Example struct {
	VoucherNo string
	Date      string
	Remark    string
	// Text 是当初触发这条凭证的原始描述（银行流水摘要等）。
	// 有了它，模型才能做「同样的描述 → 同样的记法」的类比。
	Text string
	// Amount 是当时的金额（分）。
	Amount money.Money
	Lines  []ExampleLine
	// Score 是检索相似度，便于模型知道哪个例子更相关。
	Score float64
}

// ExampleLine 是范例凭证的一条分录。
type ExampleLine struct {
	Summary     string
	AccountCode string
	AccountName string
	Debit       money.Money
	Credit      money.Money
	ContactName string
}

// auxRules 是辅助核算的取数规则。
//
// ★ 放在**共享**部分而不是对话那一节：单次任务（银行流水、发票、报销）
// 一样会撞上「科目要求部门辅助核算而没填」。曾经把它写在对话规则里，
// 结果单次任务那版提示词对这件事一个字都没提。
//
// 这一节里最要紧的是最后那两句**禁止项**：缺一个部门时，模型最容易
// 想到的绕法是换一个不需要部门的科目 —— 护栏确实会通过，但费用挂错了
// 地方，而报表照样是平的。
const auxRules = `## 辅助核算不能空着（单次任务与对话都适用）

科目后面标了「必填辅助核算：部门」的，那条分录**必须**填对应的 id。
空着会被护栏直接打回，用户看到的是「缺少必需的辅助核算」——
而这张凭证本身可能是对的，只差一个 id。

按这个顺序处理，一步都不许跳：

1. 先用 search_departments / search_employees / search_contacts 查账套里有什么
2. 查到**唯一**合适的一个 → 直接用它的 id
3. 查到**多个** → 问用户是哪一个，把查到的**都列成选项**，
   你推荐的那个放第一个（比如费用发生在哪个部门，通常从业务描述里能推出来）。
   能对话时用 ask_user 工具；单次任务问不了人，就把候选写进 warnings，
   **不要自己挑一个**
4. **一个都没有** → 能对话时用 propose_new_aux 提议新建，用户确认后再继续；
   单次任务没有这个工具，把「账套里没有部门，需要先建一个」写进 warnings

★ 第 4 步之前**不要**先出凭证。缺部门就先问部门，不要交一张
「什么都好、就是缺辅助核算」的凭证让用户自己去猜哪里不对。

★ 更不要因为缺一个部门，就换一个不需要部门辅助核算的科目去记账。
费用挂错部门，部门费用表就是错的 —— 而那张表正是老板每月要看的东西。
换个科目能让护栏通过，但账已经错了，而且错得看不出来。

`

// dialogueRules 是**对话式会计**特有的那一节系统提示词。
//
// 写法上刻意与其余各节保持一致（也参考了 deepseek-harness 的提示词风格）：
//
//   - 行为规则用**祈使句**，不写「建议」「可以」这种软话；
//   - 负面约束用破折号插入语紧跟命令（「用 X 工具 —— 不要用 Y」），
//     而不是另起一段「禁止事项」；
//   - 每条给**失败回退**（「答完之后你还会再有机会问」）；
//   - 不给 few-shot 例子 —— 例子会被当成模板照抄，
//     而记账场景里最需要的是「按业务实质判断」，不是模仿。
//
// ★ 这一节是**故意加的**，harness 那边没有对应段落（它把「何时追问」
// 完全交给模型判断）。记账不能这样：该问的不问，模型会猜一个数字，
// 而猜出来的凭证借贷平衡、所有自洽检查全过，只有金额是错的。
const dialogueRules = `# 与用户对话

用户会用一句话描述一笔业务，话说得像聊天，信息常常不全 ——
「昨天买了台打印机」「收到张三的货款」都没说金额。
这些空缺**不要猜**：猜出来的凭证照样借贷平衡、所有自洽检查全过，
但账是错的，而且数字看着很正常，没人会停手核对。

## 该问的时候

只有在缺的信息**影响科目或金额**时才问：

- 金额、含税与否、付款方式（现金 / 银行 / 挂账）—— 影响科目，必须问
- 对方是客户还是供应商、是股东还是员工 —— 影响往来科目方向，必须问
- 业务日期 —— 影响期间；用户没说就用今天，不问
- 摘要怎么写 —— 你自己写，不问

## 怎么问

用 ask_user 工具 —— 不要用一段文字提问。
工具参数是结构化的问题，界面会把它渲染成可点的选项；
写成一段文字，用户就得自己打字回你，多问两轮他就不耐烦了。

一次只问**一个**问题，问最关键的那个。用户答完之后你还会再有机会问 ——
一次抛五个问题，用户只会回你第一个，剩下四个你还得再问一遍。

参数要照下面这个写法（界面按这个渲染）：

- id：这个问题的稳定标识（如 payment / vat / dept），回答里会带回来。
  同一段对话里不要重复用同一个 id
- 每个选项给 label（用户看的一句话）与 description（一句话说清影响）
- **你推荐的那个放第一个**，并在 label 末尾加上「（推荐）」
- 能穷举就给选项；穷举不了（比如具体金额）就不要硬凑选项，留空让用户自己填
- multi_select 默认关着。只有确实可能同时成立时才开
  （例如「这笔费用涉及哪几个部门」），能单选就别开

## 什么时候不许问

- 对话里已经说过的，不要再问第二遍
- 能从账套里查到的 —— 科目叫什么、有没有这个客户、有哪些部门与员工、
  上次同样的怎么记的 —— 用 search_accounts / search_contacts /
  search_departments / search_employees / find_similar_vouchers 自己查，
  不要问用户
- 一句话里已经说清的，不要为了「确认一下」再问一遍

## 够了就出凭证

信息够了就**直接出凭证**，不要再问一句「需要我生成凭证吗」——
用户打开这个页面就是要凭证的，多问那一句只是多一次点击。

`

// ---------------------------------------------------------------------------
// 系统提示词
// ---------------------------------------------------------------------------

// SystemPrompt 是固定的系统提示词。
//
// 它做四件事：定身份、划边界、给科目清单、给输出格式。
//
// ★ 科目清单**全量**放在这里，而不是让模型自由生成编码。
// 190 个科目约 4KB token，一次调用多花几分钱，
// 换来的是「科目幻觉」这一类失效基本消失 —— 非常划算。
func SystemPrompt(in Input) string { return systemPrompt(in, false) }

// AccountantSystemPrompt 是**对话式会计**用的系统提示词。
//
// 与 SystemPrompt 的差别只有一节：「与用户对话」。
//
// 单次任务（银行流水、发票）拿不到额外信息时只能写进 warnings；
// 而对话式会计可以**回头问一句**。这一节要讲清楚三件事：
// 什么时候该问、怎么问、以及什么时候**不许**问。
//
// 其余部分（硬边界、科目闭集、输出格式）逐字复用 —— 两套提示词
// 各写一份的话，改了一处忘了另一处，护栏就会出现缝。
func AccountantSystemPrompt(in Input) string { return systemPrompt(in, true) }

func systemPrompt(in Input, dialogue bool) string {
	var b strings.Builder

	b.WriteString(`你是一名中国小微企业的资深会计，负责根据业务信息编制记账凭证。

# 你的工作边界（必须严格遵守）

1. **只能使用下面列出的科目编码。** 不许创造、不许推测、不许用「类似」的编码。
   如果找不到合适的科目，就把该行放在 warning 里说明，不要硬凑。
2. **借贷必须精确相等**（单位：分）。绝不允许出现差额，也绝不允许自己加
   「尾差」「待处理」之类的调平科目来凑数 —— 差额意味着你理解错了业务。
3. **每条分录的 debit 与 credit 恰好有一个大于 0**，另一个必须是 0。
4. **金额一律用整数「分」。** 50,000.00 元要写成 5000000。不许用小数、不许多写小数点。
5. **分录合计必须精确等于「业务信息」里给的金额，一个字都不许改。**
   那个数是用户亲眼看着填的，不需要你核算、不需要你估算、更不要「约等于」。
   实测出现过用户填 325、模型写 326：借贷照样平衡，所有自洽检查全过，
   但这种凭证一旦进账就是错的，而且数字看着很正常，没人会停手核对。
   如果因为税额拆分之类的原因凑不出这个数，把问题写进 warning，不要自己改数。
6. **科目要求辅助核算的，必须填对应的 id。** 上面已标出每个科目需要的维度。
   往来单位的 id 只能从「往来单位清单」里选。
7. **摘要必须逐行填写**，写清楚业务实质（如「支付 8 月房租」），
   不要写「结转」「费用」这种看不出内容的词。
8. 记账日期必须在当前可记账期间内。

`)

	// ---- 可编辑的记账要求 ----
	//
	// ★ 这一段是**用户可以在界面上改**的。出厂内容是下面这份默认文本，
	// 用户改过就用用户那份。放在固定骨架**中间**是有意的：
	// 前面是不可改的硬边界，后面是不可改的输出格式 —— 两头都由护栏兜着，
	// 中间放开给会计政策。
	b.WriteString(instructionsFor(in, in.Task.Label()))

	if dialogue {
		b.WriteString(dialogueRules)
	}

	b.WriteString(auxRules)

	b.WriteString(`# 参考历史凭证

下面的「历史同类凭证」是**你自己账套里**过去的记法。
当新业务与某条历史记录情形相同时，**优先采用与它一致的记法** ——
保持科目使用的一致性比追求理论最优更重要，这是审计与对账的基础。

# 输出格式

只输出一个 JSON 对象，不要任何解释文字、不要 Markdown 代码块：

{
  "voucher": {
    "word": "记",
    "biz_date": "YYYY-MM-DD",
    "remark": "一句话说明这笔业务",
    "entries": [
      {"summary": "本行摘要", "account_code": "1002", "debit": 5000000, "credit": 0,
       "contact_id": null, "employee_id": null, "dept_id": null, "project_id": null}
    ]
  },
  "confidence": 0.92,
  "reasoning": "为什么这么记，一句话",
  "evidence": ["引用的历史凭证号或科目编码"],
  "warnings": ["不确定的地方，没有就留空数组"]
}
`)

	// ---- 账套背景 ----
	b.WriteString("\n# 账套信息\n\n")
	fmt.Fprintf(&b, "- 单位名称：%s\n", orDash(in.Book.CompanyName))
	fmt.Fprintf(&b, "- 会计准则：%s\n", orDash(in.Book.Standard))
	fmt.Fprintf(&b, "- 纳税人身份：%s\n", orDash(in.Book.TaxType))
	fmt.Fprintf(&b, "- 记账本位币：%s\n", orDash(in.Book.Currency))
	fmt.Fprintf(&b, "- 当前可记账期间：%s\n", orDash(in.Book.PeriodDesc))
	if in.Today != "" {
		fmt.Fprintf(&b, "- 今天日期：%s\n", in.Today)
	}

	// ---- 科目清单（闭集）----
	b.WriteString("\n# 可用科目（只能从这里选，编码必须完全一致）\n\n")
	if len(in.Accounts) == 0 {
		b.WriteString("（科目表为空 —— 此时不应生成任何分录，请把问题写进 warnings）\n")
	} else {
		for _, a := range in.Accounts {
			fmt.Fprintf(&b, "%s %s", a.Code, a.FullName)
			if a.Direction != "" {
				fmt.Fprintf(&b, "（%s）", a.Direction)
			}
			if len(a.AuxTypes) > 0 {
				fmt.Fprintf(&b, " [必填辅助核算：%s]", strings.Join(a.AuxTypes, "、"))
			}
			b.WriteByte('\n')
		}
	}

	// ---- 往来单位清单 ----
	if len(in.Contacts) > 0 {
		b.WriteString("\n# 往来单位清单（contact_id 只能从这里选）\n\n")
		for _, c := range in.Contacts {
			fmt.Fprintf(&b, "%d %s（%s）", c.ID, c.Name, c.Kind)
			if len(c.Aliases) > 0 {
				fmt.Fprintf(&b, " 别名：%s", strings.Join(c.Aliases, "、"))
			}
			b.WriteByte('\n')
		}
	} else {
		b.WriteString("\n# 往来单位清单\n\n（账套里还没有往来单位档案）\n")
	}

	return b.String()
}

// ---------------------------------------------------------------------------
// 用户提示词
// ---------------------------------------------------------------------------

// UserPrompt 构造本次任务的用户提示词。
func UserPrompt(in Input) string {
	var b strings.Builder

	fmt.Fprintf(&b, "请为下面这笔%s业务编制记账凭证。\n\n", in.Task.Label())
	fmt.Fprintf(&b, "业务日期：%s\n", orDash(in.Date))
	if in.Direction != "" {
		fmt.Fprintf(&b, "资金方向：%s\n", in.Direction)
	}
	fmt.Fprintf(&b, "金额：%s（即 %d 分）\n", in.Amount.Abs(), int64(in.Amount.Abs()))
	if in.Counterparty != "" {
		fmt.Fprintf(&b, "对方：%s\n", in.Counterparty)
	}
	fmt.Fprintf(&b, "业务描述：%s\n", orDash(in.Text))

	if len(in.Extra) > 0 {
		b.WriteString("\n补充信息：\n")
		for _, k := range sortedKeys(in.Extra) {
			fmt.Fprintf(&b, "- %s：%s\n", k, in.Extra[k])
		}
	}

	if len(in.Examples) > 0 {
		b.WriteString("\n# 历史同类凭证（请优先与它们保持一致）\n\n")
		for i, ex := range in.Examples {
			fmt.Fprintf(&b, "## 例 %d（相似度 %.2f）%s  %s\n", i+1, ex.Score, ex.VoucherNo, ex.Date)
			if ex.Text != "" {
				fmt.Fprintf(&b, "原始描述：%s\n", ex.Text)
			}
			if ex.Remark != "" {
				fmt.Fprintf(&b, "凭证备注：%s\n", ex.Remark)
			}
			for _, l := range ex.Lines {
				switch {
				case l.Debit.IsPositive():
					fmt.Fprintf(&b, "  借 %s %s  %s", l.AccountCode, l.AccountName, l.Debit)
				case l.Credit.IsPositive():
					fmt.Fprintf(&b, "  贷 %s %s  %s", l.AccountCode, l.AccountName, l.Credit)
				default:
					continue
				}
				if l.ContactName != "" {
					fmt.Fprintf(&b, "（%s）", l.ContactName)
				}
				if l.Summary != "" {
					fmt.Fprintf(&b, "  摘要：%s", l.Summary)
				}
				b.WriteByte('\n')
			}
			b.WriteByte('\n')
		}
	}

	b.WriteString("\n请只输出 JSON 对象。")
	return b.String()
}

// ---------------------------------------------------------------------------
// 脱敏
// ---------------------------------------------------------------------------

// Privacy 控制发送前的脱敏程度。
type Privacy struct {
	// MaskAmounts 为真时把金额替换为等长的占位。
	//
	// 默认关闭：金额是判断科目的核心依据（5 万与 500 万的记法可能不同），
	// 抹掉金额会让准确率显著下降。仅在用户明确要求时开启。
	MaskAmounts bool
	// MaskNames 为真时对姓名做掩码（张三 → 张*）。
	MaskNames bool
	// MaskAccounts 为真时把银行账号、税号等长数字串替换为占位。
	MaskAccounts bool
}

// DefaultPrivacy 返回默认脱敏策略：本地模型不脱敏，云端只遮账号。
//
// 理由：本地模型数据不出机器，脱敏纯属自损准确率；
// 云端则至少要遮住账号这类可以直接用于盗刷的信息，
// 而户名与金额保留 —— 它们是判断科目的依据。
func DefaultPrivacy(kind Kind) Privacy {
	if kind.IsLocal() {
		return Privacy{}
	}
	return Privacy{MaskAccounts: true}
}

// Apply 对文本执行脱敏。
func (p Privacy) Apply(s string) string {
	if s == "" {
		return s
	}
	if p.MaskAccounts {
		s = maskLongDigits(s, 8)
	}
	if p.MaskNames {
		s = maskChineseNames(s)
	}
	return s
}

// maskLongDigits 把疑似账号/证照号的长串替换为等长占位。
//
// 命中两类：
//
//	纯数字且长度 ≥ min          —— 银行账号、身份证号
//	字母数字混合且长度 ≥ min+4  —— 统一社会信用代码（18 位，含字母）
//
// 之所以要覆盖混合串：统一社会信用代码形如 91330100MA2XXXXXXX，
// 只按「纯数字段」遮的话，字母之后的部分会原样发出去，
// 而它恰恰是最能定位到具体企业的部分。
//
// 日期（8 位）与金额（通常 3~8 位）都不在范围内，
// 保留它们是因为这两者对判断科目是必需的上下文。
func maskLongDigits(s string, min int) string {
	const mixedExtra = 4
	var b strings.Builder
	b.Grow(len(s))

	runStart, digits := -1, 0
	flush := func(end int) {
		if runStart < 0 {
			return
		}
		n := end - runStart
		seg := s[runStart:end]
		limit := min
		if digits < n { // 含字母
			limit = min + mixedExtra
		}
		if n >= limit {
			b.WriteString(strings.Repeat("*", n))
		} else {
			b.WriteString(seg)
		}
		runStart, digits = -1, 0
	}

	for i := 0; i <= len(s); i++ {
		if i < len(s) && isIdentByte(s[i]) {
			if runStart < 0 {
				runStart = i
			}
			if s[i] >= '0' && s[i] <= '9' {
				digits++
			}
			continue
		}
		flush(i)
		if i < len(s) {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// isIdentByte 报告该字节否属于标识符字符（数字或大写字母）。
//
// 只要大写字母：统一社会信用代码、银行联行号都是大写，
// 而小写字母在中文摘要里多是拼音或英文单词，遮掉会损失语义。
func isIdentByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')
}

// maskChineseNames 把「户名：张三丰」这类片段里的姓名打码，保留姓氏。
//
// 不做通用中文姓名识别 —— 那需要一份姓氏表，而且误伤率高
// （「杭州」也会被当成姓名）。这里只在明确的关键词之后动手：
// 关键词本身就说明了后面的内容是个人姓名。
//
// ★ 必须按 rune 遍历。按字节遍历时只有每个汉字的首字节落在
// UTF-8 三字节区间内，续字节不在，循环会在第一个字之后就停下 ——
// 「张三丰」会被算成长度 1，于是一个名字都遮不住。
func maskChineseNames(s string) string {
	keys := []string{
		"户名：", "户名:", "收款人：", "收款人:", "付款人：", "付款人:",
		"姓名：", "姓名:", "法人：", "法人:",
	}
	for _, k := range keys {
		idx := strings.Index(s, k)
		if idx < 0 {
			continue
		}
		head, rest := s[:idx+len(k)], s[idx+len(k):]
		runes := []rune(rest)

		n := 0
		for n < len(runes) && isHanRune(runes[n]) {
			n++
		}
		if n < 2 {
			// 少于 2 个汉字不像姓名（「张」「李」这类单字不算）
			continue
		}
		masked := string(runes[0]) + strings.Repeat("*", n-1)
		s = head + masked + string(runes[n:])
	}
	return s
}

// isHanRune 报告该字符是否属于中日韩统一表意文字基本区。
//
// 只覆盖基本区（U+4E00–U+9FFF）：扩展区的生僻字极少数场景才会出现，
// 而为了它们把判断放宽会误伤标点与全角符号。
func isHanRune(r rune) bool { return r >= 0x4E00 && r <= 0x9FFF }

// ---------------------------------------------------------------------------
// 排序与格式化辅助
// ---------------------------------------------------------------------------

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（未提供）"
	}
	return s
}

// Draft 把已通过护栏的提议落成凭证草稿。
//
// 编排层不直接调 ai.Proposal.Materialize，而是经过这里，
// 是为了让「AI 产物一律先是草稿」这条规则只有一个出口。
func Draft(p *ai.Proposal, createdBy string, at *ai.AIProvenance) (*voucher.Voucher, error) {
	return p.Materialize(createdBy, at)
}

// ---------------------------------------------------------------------------
// 可编辑的记账要求
// ---------------------------------------------------------------------------

// defaultInstructions 是「记账要求」那一段的出厂内容。
//
// ★ 它**不是**提示词的全部，只是用户可以改的那一段。
// 前面的硬边界（只能用列出的科目、借贷必须相等、金额单位是分）与
// 后面的输出格式都由程序固定 —— 改坏了护栏就直接解析失败。
const defaultInstructions = `# 记账的基本原则

- 收付实现制不认识，这是**权责发生制**：钱没动但义务发生了也要记账。
- 银行流水是「银行存款」科目的变动，你要判断的是**对方科目**。
- 一笔银行收款通常对应：收回货款（贷 应收账款）、销售收款（贷 收入+销项税）、
  借入款项（贷 短期借款/其他应付款）、股东投入（贷 实收资本）、
  利息收入（贷 财务费用，注意用红字方向即借方负数不接受，应贷财务费用）。
- 一笔银行付款通常对应：付货款（借 应付账款/原材料）、
  发工资（借 应付职工薪酬）、交税（借 应交税费）、
  买设备（借 固定资产）、付费用（借 管理费用/销售费用）、
  还借款（借 短期借款/其他应付款）。
- 增值税一般纳税人：采购取得专票时「应交税费—应交增值税—进项税额」在借方；
  销售时「应交税费—应交增值税—销项税额」在贷方。
  小规模纳税人只有「应交增值税」明细，按含税或简易计税处理。
`

// DefaultInstructions 返回出厂默认的记账要求，供界面展示与恢复默认。
func DefaultInstructions() string { return defaultInstructions }

// instructionsFor 拼出提示词里那一段「记账要求」。
//
// 组成：用户自定义（没自定义时用出厂默认）+ 该任务的附加要求。
func instructionsFor(in Input, taskName string) string {
	body := in.Prompt.Instructions
	if strings.TrimSpace(body) == "" {
		body = defaultInstructions
	}
	body = strings.TrimRight(body, "\n")

	var b strings.Builder
	b.WriteString(body)
	b.WriteByte('\n')

	if note := in.Prompt.TaskNote(string(in.Task)); note != "" {
		fmt.Fprintf(&b, "\n# 本次任务（%s）的额外要求\n\n%s\n", taskName, note)
	}
	return b.String()
}

// EffectiveInstructions 返回当前实际生效的记账要求（含任务附加要求）。
//
// 界面用它显示「现在到底用的是什么」，避免用户看着输入框猜。
func EffectiveInstructions(in Input) string {
	return instructionsFor(in, in.Task.Label())
}
