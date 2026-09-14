package aiprovider

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 注册会计师专业智能 Agent：身份、边界、输出、转人工
// ---------------------------------------------------------------------------
//
// 这一组守的是一份**规格**（用户给的职位说明），不是某个函数的行为。
// 所以断言方式也不同于别处：逐条检查提示词里有没有那几件事。
//
// 为什么要这么细：这份规格里绝大多数条目是**约束**，而约束失效时
// 没有任何东西会报错 —— 模型照样给出漂亮的回答，只是它开始
// 以注册会计师的口吻签字画押了。那种失效只能靠事后追查，
// 而追查的成本远高于在这里写一条断言。

// ★ 身份：必须说清「我不是注册会计师」
func TestCPAPromptStatesIdentityBoundaries(t *testing.T) {
	sys := AccountantSystemPrompt(Input{Task: TaskFreeform})
	for _, want := range []string{
		// 三件事：不是自然人、没有证书、不是事务所
		"不是自然人",
		"不持有",
		"不属于",
		"不得冒充注册会计师",
		"会计师事务所统一受理",
		"证明效力",
		// 头衔只能用这个
		"CPA 专业智能助手",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("★ 身份部分缺少 %q —— 用户会把它当注册会计师用", want)
		}
	}
}

// ★ 执业依据：十类规范 + 政策时效
func TestCPAPromptListsAuthorities(t *testing.T) {
	sys := AccountantSystemPrompt(Input{Task: TaskFreeform})
	for _, want := range []string{
		"会计法", "注册会计师法", "企业会计准则", "小企业会计准则",
		"审计准则", "职业道德守则", "企业内部控制基本规范",
		"增值税", "企业所得税", "个人所得税",
		"公司法", "证券法",
		"财政部", "国家税务总局", "中国注册会计师协会",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("执业依据里缺少 %q", want)
		}
	}
	// 政策时效：必须标明四项
	for _, want := range []string{"政策名称", "发布机关", "适用地区", "适用期间"} {
		if !strings.Contains(sys, want) {
			t.Errorf("政策时效要求里缺少 %q", want)
		}
	}
	// 2026 修订版 2027-01-01 施行这件事要写进去
	if !strings.Contains(sys, "2027 年 1 月 1 日") {
		t.Error("没有写明新《注册会计师法》的施行日期")
	}
}

// ★ 四项职责
func TestCPAPromptCoversDuties(t *testing.T) {
	sys := AccountantSystemPrompt(Input{Task: TaskFreeform})
	for _, want := range []string{
		"会计核算", "税务", "财务报表审计", "验资",
		"尽职调查", "内部控制评价", "成本核算", "现金流分析",
		"持续经营", "重要性水平", "审计工作底稿", "未更正错报",
		"无保留", "保留", "否定意见", "无法表示意见",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("职责部分缺少 %q", want)
		}
	}
}

// ★ 职业行为十条：逐条查关键词
func TestCPAPromptProfessionalConduct(t *testing.T) {
	sys := AccountantSystemPrompt(Input{Task: TaskFreeform})
	for _, want := range []string{
		"诚信", "客观", "独立", "公正",
		"职业怀疑",
		"利益冲突",
		"不因用户要求而改变专业结论",
		"不隐瞒重大错报",
		"保密",
		"区分事实、假设、专业判断与不确定事项",
		"算式",
		"证据来源",
		"人工审批记录",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("职业行为里缺少 %q", want)
		}
	}
}

// ★ 标准流程：12 步的顺序不能乱
func TestCPAPromptWorkflowOrder(t *testing.T) {
	sys := AccountantSystemPrompt(Input{Task: TaskFreeform})
	steps := []string{
		"明确业务类型",
		"确认企业所在地",
		"检查资料是否完整",
		"列出缺失的资料",
		"确定适用",
		"开展计算",
		"交叉验证",
		"给出初步结论",
		"列出风险、限制条件",
		"生成工作底稿",
		"提交人工注册会计师复核",
		"获得人工确认后",
	}
	at := -1
	for _, step := range steps {
		i := strings.Index(sys, step)
		if i < 0 {
			t.Errorf("标准流程里缺少「%s」", step)
			continue
		}
		if i < at {
			t.Errorf("★ 流程顺序不对：「%s」出现在了它上一步的前面 —— "+
				"顺序是这套流程的全部意义，颠倒过来就是另一回事了", step)
		}
		at = i
	}
}

// ★ 硬边界：那张表的每一行都要在
func TestCPAPromptHardBoundaries(t *testing.T) {
	sys := AccountantSystemPrompt(Input{Task: TaskFreeform})
	for _, want := range []string{
		"虚构凭证或业务",
		"未经确认直接申报",
		"独立作出最终法定审计意见",
		"以注册会计师身份签字、盖章",
		"独立签发具有证明效力的验资报告",
		"替代注册会计师作最终判断",
		"未经授权直接提交申报",
		"宣称自己持有中国注册会计师证书",
		"协助造假、隐瞒收入、虚开发票",
		// 那句总结
		"「AI 可以完成全部工作流程」与「AI 可以独立完成全部法定执业行为」",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("硬边界里缺少 %q", want)
		}
	}
}

// ★ 强制转人工：十二条触发条件一条都不能少
func TestCPAPromptMandatoryEscalation(t *testing.T) {
	sys := AccountantSystemPrompt(Input{Task: TaskFreeform})
	triggers := []string{
		"财务造假", "虚假交易", "管理层舞弊",
		"证据互相矛盾", "资料明显缺失",
		"保留意见", "否定意见", "无法表示意见",
		"持续经营",
		"未披露关联交易",
		"会计估计没有可靠依据",
		"拒绝提供银行流水",
		"上市公司", "金融机构", "证券服务",
		"跨境",
		"税务处罚", "行政调查", "诉讼",
		"签字", "盖章", "申报", "付款",
		"找不到有效且最新的官方政策依据",
	}
	for _, want := range triggers {
		if !strings.Contains(sys, want) {
			t.Errorf("强制转人工里缺少 %q", want)
		}
	}
	// 「宁可多报」这个取向必须写出来，否则模型会自己权衡着少报
	if !strings.Contains(sys, "宁可多报") {
		t.Error("没有说明「宁可多报」—— 模型会倾向于少报")
	}
}

// ★ 输出要求十项：与 Answer 的字段一一对应
func TestCPAPromptOutputRequirements(t *testing.T) {
	sys := AccountantSystemPrompt(Input{Task: TaskFreeform})
	for _, want := range []string{
		"conclusion", "basis", "obtained", "missing", "process",
		"findings", "risk", "recommendations", "humanReview",
		"submittable", "text",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("输出契约里缺少字段 %q", want)
		}
	}
	// 「不许把可能写成事实」这条要出现在两处：职业行为与输出纪律
	if strings.Count(sys, "尚未核实") < 2 {
		t.Error("「不许把尚未核实的内容写成确定事实」应当反复强调")
	}
	// "十项一项都不要省"
	if !strings.Contains(sys, "一项都不要省") {
		t.Error("没有强调十项不能漏")
	}
}

// 单次任务（银行流水/发票建议）**不该**背这套身份 ——
// 那条路上没有对话，写进去只是白白稀释注意力
func TestCPAFrameworkOnlyInDialoguePrompt(t *testing.T) {
	plain := SystemPrompt(Input{Task: TaskBankFlow})
	if strings.Contains(plain, "CPA 专业智能助手") {
		t.Error("单次任务的提示词里不该出现 CPA 身份 —— 那条路上没有人可以对话，也不需要审计意见类型")
	}
	acct := AccountantSystemPrompt(Input{Task: TaskFreeform})
	if !strings.Contains(acct, "CPA 专业智能助手") {
		t.Error("对话式会计的提示词里必须有 CPA 身份")
	}
}

// 身份那一段必须排在**最前面**。
//
// 模型对提示词开头最敏感；把「我是谁、我不能做什么」放在几千字的
// 科目清单后面，等于没说。
func TestCPAFrameworkComesEarly(t *testing.T) {
	sys := AccountantSystemPrompt(Input{Task: TaskFreeform})
	identity := strings.Index(sys, "不是自然人")
	accounts := strings.Index(sys, "# 可用科目")
	if identity < 0 || accounts < 0 {
		t.Fatal("锚点没找到")
	}
	if identity > accounts {
		t.Error("★ 身份说明排在了科目清单后面 —— 那等于没说")
	}
}
