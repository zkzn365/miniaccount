package service

import (
	"strings"
	"testing"

	"miniaccount/internal/ai/aiprovider"
)

// ---------------------------------------------------------------------------
// 会计 agent 能做什么、不能做什么
// ---------------------------------------------------------------------------
//
// 这一组是**边界测试**，不是功能测试。
//
// 写在注释里的约束拦不住任何人：半年后有人顺手加一个 close_period
// 工具，代码照样能编、能跑、评审也未必看得出来 ——
// 直到某天用户说了一句「这个月结了吧」，AI 就把账期关了。
//
// 所以边界要写成会红的测试。这里守两条：
//
//   1. 不可逆的动作（结账、反结账、过账、冲销、删除）一个工具都不许有
//   2. 所有会改账套的工具必须是**终止型** —— 它只能提议，执行要人按

// ★ 结账不给 AI 提议执行，是用户明确要求的。
//
// 理由：它是这一串操作里唯一「按错了要反结账才能回头」的动作。
// 其他写操作（建档案、办离职）错了还能改回来；结账错了，
// 期间已经关了，用户得先反结账、冲销结转凭证、再重来一遍。
// 让 AI 有能力按下这个按钮，收益只是省一次点击。
func TestAccountantToolsHaveNoIrreversibleActions(t *testing.T) {
	ts := accountantTools(nil)
	if ts.Len() == 0 {
		t.Fatal("工具集为空 —— 这条测试就失去意义了")
	}

	// ---- 1. 名单是**逐字写死**的 ----
	//
	// 这一条看着笨，但它正是「半年后有人顺手加一个 close_period」的拦路石：
	// 加工具的人会看到测试红，于是不得不来这里改一个字 ——
	// 而改的时候他会读到下面这段注释，知道自己在动什么。
	//
	// 拦的**不是**新工具本身，是「加工具时没人想过它该不该有」。
	// 顺序是 ToolSet 内部排好的（按名字），逐字抄一遍
	want := []string{
		"ask_user",              // 追问（终止型）
		"check_period",          // 本月体检
		"find_similar_vouchers", // 历史同类
		"get_ledger",            // 明细账
		"get_report",            // 四张报表
		"preview_payroll",       // 五险一金与个税试算
		"propose_hr_action",     // 离职 / 转部门 / 调薪（终止型）
		"propose_new_aux",       // 新建部门 / 员工（终止型）
		"search_accounts",
		"search_contacts",
		"search_departments",
		"search_employees",
	}
	got := ts.Names()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("★ 会计的工具名单变了。\n实际：%v\n期望：%v\n\n"+
			"加工具之前请先回答两个问题：\n"+
			"  1. 它会**改账套**吗？会的话必须是终止型（Terminal: true），"+
			"由用户点确认才执行；\n"+
			"  2. 它是**不可逆**的吗（结账、反结账、过账、红字冲销、删除）？\n"+
			"     是的话不要加 —— 用户明确要求过不给 AI 这个能力。"+
			"它最多只能说「可以结了，请你去账期管理点一下」。\n"+
			"确认没问题之后，把名字加进上面的 want 列表。", got, want)
	}

	// ---- 2. 名字里不许出现不可逆动作 ----
	//
	// 只看**名字**，不看描述：check_period 的说明里写着
	// 「结账前体检」「草稿未过账」—— 那是在**描述账套的状态**，
	// 不是它要做的事。按关键词扫描述会误报，而且误报的守卫很快
	// 就会被人加白名单绕过，等于没有。
	forbidden := []string{
		"close", "reopen", "post", "reverse", "delete", "void", "settle",
		"结账", "反结账", "过账", "冲销", "删除", "作废",
	}
	for _, name := range got {
		lower := strings.ToLower(name)
		for _, bad := range forbidden {
			if strings.Contains(lower, strings.ToLower(bad)) {
				t.Errorf("★ 工具名 %s 里有「%s」—— 不可逆的动作不许交给 AI", name, bad)
			}
		}
	}

	// ---- 3. 描述里不许承诺「AI 自己动手」 ----
	//
	// 这一条查的是**具体措辞**，不是单个动词：模型会照着描述理解自己的权限，
	// 描述里写「自动结账」，它就会去找这个能力（找不到就自己编一个说法）。
	for _, name := range got {
		tool, _ := ts.Get(name)
		for _, bad := range []string{
			"自动结账", "自动过账", "直接结账", "替你结账", "自动冲销",
			"自动删除", "自动作废", "自动反结账",
		} {
			if strings.Contains(tool.Description, bad) {
				t.Errorf("★ %s 的描述里写着「%s」—— 不要给模型这种承诺", name, bad)
			}
		}
	}
}

// 会改账套的工具必须是终止型：调用它只是**提议**，执行由用户确认。
//
// 反过来说：如果某个工具既会写、又不是终止型，循环就会继续往下跑，
// 模型可以自己把改动落下去 —— 那就没有「用户确认」这一步了。
func TestAccountantWriteToolsAreTerminal(t *testing.T) {
	ts := accountantTools(nil)

	// 允许改账套的（全部必须是终止型）
	writers := []string{"propose_new_aux", "propose_hr_action"}
	for _, name := range writers {
		tool, ok := ts.Get(name)
		if !ok {
			t.Fatalf("工具 %s 不见了 —— 建档案 / 办人事异动是明确要有的功能", name)
		}
		if !tool.Terminal {
			t.Errorf("★ %s 会改账套，却不是终止型 —— "+
				"这样模型可以自己把改动落下去，用户没有确认的机会", name)
		}
	}

	// 反过来：只读工具不该是终止型。
	//
	// 终止型会让这一轮立即结束，只读工具这么干等于「查一下就停下来」，
	// 用户得再催一句它才继续 —— 对话变得又慢又啰嗦。
	readers := []string{
		"search_accounts", "search_contacts", "search_departments",
		"search_employees", "find_similar_vouchers",
		"get_report", "check_period", "preview_payroll", "get_ledger",
	}
	for _, name := range readers {
		tool, ok := ts.Get(name)
		if !ok {
			t.Errorf("只读工具 %s 不见了", name)
			continue
		}
		if tool.Terminal {
			t.Errorf("%s 是只读的，不该终止这一轮 —— "+
				"查一下就停下来会让用户得多说一句", name)
		}
		if tool.Run == nil {
			t.Errorf("%s 是只读工具，必须有 Run", name)
		}
	}
}

// 每个工具都要有能落到实处的描述与参数 schema。
//
// ★ 模型选工具**完全依赖**这段描述：写成「查询」它不知道该在什么时候用，
// 写成「用户问『这个月社保扣多少』时用它」它才会用对时机。
func TestAccountantToolDescriptions(t *testing.T) {
	ts := accountantTools(nil)
	for _, name := range ts.Names() {
		tool, _ := ts.Get(name)
		if len([]rune(tool.Description)) < 20 {
			t.Errorf("%s 的描述太短，模型选不准：%q", name, tool.Description)
		}
		if len(tool.Parameters) == 0 && !tool.Terminal {
			t.Errorf("%s 没有参数 schema", name)
		}
	}
	if ts.Len() < 12 {
		t.Errorf("工具数 = %d，比预期少 —— 是不是有人删了不带测试的工具？", ts.Len())
	}
}

// ---------------------------------------------------------------------------
// CPA 答复的两条**程序判定**
// ---------------------------------------------------------------------------

// ★ submittable 一律压回 false —— 模型说了不算。
//
// 这是整份 CPA 规格里最容易被绕过的一条：模型只要在 JSON 里写
// `"submittable": true`，界面上就会出现一份「可以直接对外提交」的材料。
// 而它没有这个资格：注册会计师业务依法由会计师事务所统一受理。
func TestAnswerViewForcesSubmittableFalse(t *testing.T) {
	a := &aiprovider.Answer{
		Conclusion: "可以申报", Submittable: true,
		Text: "这份材料可以直接用于正式申报。",
	}
	v := answerView(a)
	if v.Submittable {
		t.Fatal("★ 模型声称可直接申报，程序却没有压回 false —— 硬边界失效了")
	}
	if !v.SubmittableClaimed {
		t.Error("模型声称过什么要留痕（审计时需要知道它当时怎么说的）")
	}
	if len(v.ProblemList) == 0 {
		t.Error("★ 声称可以直接对外提交，必须作为问题报出来给用户看")
	}
	found := false
	for _, p := range v.ProblemList {
		if strings.Contains(p, "申报") || strings.Contains(p, "对外") {
			found = true
		}
	}
	if !found {
		t.Errorf("问题列表里要说清是「不能对外提交」这件事：%v", v.ProblemList)
	}
}

// ★ 强制转人工是**兜底**：模型自己没说，程序也要能识别出来
func TestAnswerViewEscalatesHighRisk(t *testing.T) {
	cases := []struct {
		name string
		a    aiprovider.Answer
		want string
	}{
		{"审计意见类型", aiprovider.Answer{
			Conclusion: "拟出具保留意见", Risk: "高"}, "保留意见"},
		{"舞弊", aiprovider.Answer{
			Conclusion: "可能存在管理层舞弊", Risk: "高"}, "舞弊"},
		{"持续经营", aiprovider.Answer{
			Conclusion: "持续经营存在重大不确定性", Risk: "高"}, "持续经营"},
		{"税务处罚", aiprovider.Answer{
			Process: "涉及税务处罚", Risk: "中"}, "税务处罚"},
		{"上市公司", aiprovider.Answer{
			Findings: []string{"客户为上市公司"}, Risk: "中"}, "上市公司"},
		{"跨境", aiprovider.Answer{
			Recommendations: []string{"涉及境外付款"}, Risk: "中"}, "跨境"},
		{"需要签字盖章", aiprovider.Answer{
			Conclusion: "准备盖章", Risk: "中"}, "签字"},
		{"自评高风险", aiprovider.Answer{
			Conclusion: "没什么问题", Risk: "高"}, "高风险"},
		{"模型自己列了人工事项", aiprovider.Answer{
			Conclusion: "x", Risk: "低",
			HumanReview: []string{"请人工确认"}}, "人工"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := answerView(&c.a)
			if len(v.Escalation) == 0 {
				t.Fatalf("★ 没有转人工：%+v", c.a)
			}
			joined := strings.Join(v.Escalation, "｜")
			if !strings.Contains(joined, c.want) {
				t.Errorf("命中项里应当提到 %q，实际：%s", c.want, joined)
			}
		})
	}
}

// 反过来：一份普通的低风险答复不该把用户推去找会计师 ——
// 每次都报「需要人工复核」，用户很快就不看了
func TestAnswerViewDoesNotEscalateOrdinaryAnswer(t *testing.T) {
	a := &aiprovider.Answer{
		Conclusion: "本月试算平衡，可以结账",
		Risk:       "低",
		Text:       "资产 1000.00，负债 400.00，所有者权益 600.00。",
	}
	v := answerView(a)
	if len(v.Escalation) != 0 {
		t.Errorf("普通答复不该转人工，实际命中：%v", v.Escalation)
	}
}

// 风险等级没标注：按中等处理，并且要标出来
func TestAnswerViewMarksUnstatedRisk(t *testing.T) {
	v := answerView(&aiprovider.Answer{Conclusion: "x", Risk: "随便"})
	if !v.RiskUnstated {
		t.Error("模型乱填风险等级时要标出「未标注」")
	}
	if v.Risk != aiprovider.RiskMedium {
		t.Errorf("未标注时按中等处理，实际 %q —— 不能当低风险", v.Risk)
	}
}
