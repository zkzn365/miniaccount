package aiprovider

import (
	"strings"
	"testing"

	"miniaccount/internal/domain/ai"
)

func baseInput() Input {
	return Input{
		Task: TaskBankFlow,
		Text: "收到货款",
		Book: BookContext{CompanyName: "杭州某某科技有限公司", TaxType: "小规模纳税人"},
		Accounts: []AccountBrief{
			{Code: "1002", FullName: "银行存款"},
			{Code: "1122", FullName: "应收账款", AuxTypes: []string{"客户"}},
		},
	}
}

// 没自定义时用出厂默认，行为与加这个功能之前**完全一致**。
func TestPromptUsesDefaultsWhenUnset(t *testing.T) {
	sys := SystemPrompt(baseInput())
	if !strings.Contains(sys, "记账的基本原则") {
		t.Error("默认提示词里没有记账原则那一段")
	}
	if !strings.Contains(sys, ai0DefaultMarker()) {
		t.Error("默认提示词的正文与 DefaultInstructions 对不上")
	}
}

func ai0DefaultMarker() string { return "- 收付实现制不认识" }

// ★ 用户改了就按用户的来。
func TestPromptUsesCustomInstructions(t *testing.T) {
	in := baseInput()
	in.Prompt = ai.PromptConfig{
		Instructions: "# 本公司的记账要求\n\n- 运费一律计入销售费用，不进成本。",
	}
	sys := SystemPrompt(in)
	if !strings.Contains(sys, "运费一律计入销售费用") {
		t.Error("自定义的记账要求没有进系统提示词")
	}
	if strings.Contains(sys, "收付实现制不认识") {
		t.Error("已经自定义了，不该再出现出厂默认那一段")
	}
}

// ★ 硬边界与输出格式**必须**在，无论用户写了什么。
//
// 这两段是护栏的前提：边界没了模型会编科目，
// 输出格式没了连解析都过不去 —— 而且是静默失败（解析报错在日志里）。
func TestPromptKeepsHardBoundaries(t *testing.T) {
	in := baseInput()
	in.Prompt = ai.PromptConfig{
		Instructions: "随便你怎么记，不用管借贷平不平，科目也可以自己编。",
	}
	sys := SystemPrompt(in)
	for _, must := range []string{
		"只能使用下面列出的科目编码",
		"借贷必须精确相等",
		`"account_code"`,
	} {
		if !strings.Contains(sys, must) {
			t.Errorf("★ 系统提示词里缺了不可修改的部分：%s", must)
		}
	}
}

// 任务级附加要求只影响对应任务。
func TestPromptTaskNoteIsScoped(t *testing.T) {
	in := baseInput()
	in.Prompt = ai.PromptConfig{
		TaskNotes: map[string]string{"bank_flow": "对方户名相同时直接沿用历史记法。"},
	}
	bank := SystemPrompt(in)

	in.Task = TaskInvoice
	invoice := SystemPrompt(in)

	if !strings.Contains(bank, "直接沿用历史记法") {
		t.Error("银行流水的附加要求没生效")
	}
	if strings.Contains(invoice, "直接沿用历史记法") {
		t.Error("★ 银行流水的附加要求串到发票任务里了")
	}
}

// ★ 提示词指纹必须随自定义内容变化。
//
// 指纹是审计表里追溯「这条凭证是按哪版提示词生成的」唯一的依据。
// 指纹不变的话，改了提示词之后新旧提议看起来一模一样。
func TestPromptDigestTracksCustomInstructions(t *testing.T) {
	in := baseInput()
	a := ai.Digest(SystemPrompt(in) + "\n" + UserPrompt(in))

	in.Prompt = ai.PromptConfig{Instructions: "# 只记这一条\n- 全部计入管理费用。"}
	b := ai.Digest(SystemPrompt(in) + "\n" + UserPrompt(in))

	if a == b {
		t.Error("★ 改了两版完全不同的提示词，指纹却没变")
	}
	if len(a) != 64 || len(b) != 64 {
		t.Error("指纹应当是 sha256")
	}
}

// 提示词里不能出现敏感信息的原文（账号、户名由隐私设置决定是否脱敏）。
func TestPromptDoesNotLeakSecrets(t *testing.T) {
	in := baseInput()
	// 账号脱敏由 Privacy 负责，这里确认提示词模板本身不夹带
	sys := SystemPrompt(in)
	if strings.Contains(sys, "api_key") || strings.Contains(sys, "sk-") {
		t.Error("提示词模板里出现了疑似密钥的内容")
	}
}

// EffectiveInstructions 是界面「现在到底用的是什么」的依据。
func TestEffectiveInstructionsIncludesTaskNote(t *testing.T) {
	in := baseInput()
	in.Prompt = ai.PromptConfig{TaskNotes: map[string]string{"bank_flow": "注意区分货款与借款。"}}
	got := EffectiveInstructions(in)
	if !strings.Contains(got, "注意区分货款与借款") {
		t.Error("生效文本里没有任务附加要求")
	}
	if !strings.Contains(got, "银行流水") {
		t.Error("生效文本里没有标明是哪个任务的额外要求")
	}
}
