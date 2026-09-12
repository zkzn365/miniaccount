package ai

import (
	"strings"
	"testing"
)

// 空白与换行不算「自定义」。
//
// 用户把编辑框全选删掉再保存，存进去的是一串空白；
// 不做 Normalize 的话，界面会显示「已自定义」，而提示词里多了一段空白 ——
// 用户看着「已自定义」却找不到自己改了什么。
func TestNormalizeTreatsWhitespaceAsDefault(t *testing.T) {
	// 全是空白 → 完全等于没设置
	blank := PromptConfig{
		Instructions: "  \n\t  ",
		TaskNotes:    map[string]string{"bank_flow": "   "},
	}.Normalize()
	if !blank.IsDefault() {
		t.Error("只有空白时应当算「用默认」")
	}
	if blank.Instructions != "" {
		t.Errorf("Instructions = %q，期望空串", blank.Instructions)
	}
	if _, ok := blank.TaskNotes["bank_flow"]; ok {
		t.Error("只有空白的任务附加要求不该留下")
	}

	// 有内容的那条要留下，只有空白的那条要清掉
	mixed := PromptConfig{
		Instructions: "  ",
		TaskNotes:    map[string]string{"bank_flow": "   ", "invoice": "有内容"},
	}.Normalize()
	if mixed.IsDefault() {
		t.Error("有任务附加要求时不该算「用默认」")
	}
	if _, ok := mixed.TaskNotes["bank_flow"]; ok {
		t.Error("只有空白的那条应当被清掉")
	}
	if mixed.TaskNotes["invoice"] != "有内容" {
		t.Error("有内容的那条被误删了")
	}
}

func TestPromptConfigValidate(t *testing.T) {
	long := PromptConfig{Instructions: strings.Repeat("字", MaxInstructions+1)}
	if err := long.Validate(); err == nil {
		t.Error("超长应当被拒绝")
	}
	ok := PromptConfig{Instructions: strings.Repeat("字", MaxInstructions)}
	if err := ok.Validate(); err != nil {
		t.Errorf("刚好到上限不该报错：%v", err)
	}
	note := PromptConfig{TaskNotes: map[string]string{"invoice": strings.Repeat("字", MaxTaskNote+1)}}
	if err := note.Validate(); err == nil {
		t.Error("任务附加要求超长应当被拒绝")
	}
}

func TestValidTask(t *testing.T) {
	for _, k := range KnownTasks() {
		if !ValidTask(k) {
			t.Errorf("%s 应当是合法任务", k)
		}
	}
	if ValidTask("bankflow") || ValidTask("") {
		t.Error("未知任务名不该通过")
	}
}
