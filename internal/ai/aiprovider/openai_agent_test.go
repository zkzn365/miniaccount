package aiprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 端到端：真 provider 客户端 ↔ 假模型服务（**按真实接口的规则校验**）
// ---------------------------------------------------------------------------
//
// # 为什么值得写这么个假服务
//
// 这一条是拿用户报的一个 bug 换来的。当时的现象是：AI 会计问了问题，
// 用户一点选项就报
//
//	HTTP 400 invalid_request_error: An assistant message with 'tool_calls'
//	must be followed by tool messages responding to each 'tool_call_id'.
//
// 根子在对话历史里留下了一条**没人应答的 tool_calls**
//（终止型工具 ask_user 调用后直接结束本轮，没写 tool 结果）。
//
// 这一类 bug 有两个特点，决定了不能只靠上面的单元测试：
//
//  1. **只有真接口才会拒**。假 provider 是个 Go struct，它照单全收，
//     什么畸形历史都收得下 —— 上面的 scriptProvider 测试就漏掉了它。
//  2. **它废掉的是整段对话**。坏历史一旦写进会话，之后每一次请求
//     都被拒，用户只能重开。不是「这一句失败了」，是「这个功能不能用了」。
//
// 所以这里起一个真的 HTTP 服务，把接口的硬性要求**逐字实现**一遍。
// 不需要 API key、不联网、毫秒级，但它会像一个真接口那样拒绝非法请求。
//
// ★ 这也补上了一个空白：在此之前，AI 这条链路（provider 客户端 →
// Agent 循环 → 会计）**没有任何测试覆盖到 HTTP 这一层**。

// assertOpenAIProtocol 复刻 OpenAI 对消息序列的硬性要求。
//
// 规则只有一条：assistant 消息里出现的每一个 tool_call_id，
// 都必须紧接着在 tool 消息里得到应答。
func assertOpenAIProtocol(msgs []map[string]any) error {
	for i := 0; i < len(msgs); i++ {
		if msgs[i]["role"] != "assistant" {
			continue
		}
		calls, _ := msgs[i]["tool_calls"].([]any)
		if len(calls) == 0 {
			continue
		}
		answered := map[string]bool{}
		for j := i + 1; j < len(msgs); j++ {
			if msgs[j]["role"] != "tool" {
				break
			}
			id, _ := msgs[j]["tool_call_id"].(string)
			answered[id] = true
		}
		for _, c := range calls {
			m, _ := c.(map[string]any)
			id, _ := m["id"].(string)
			if !answered[id] {
				return fmt.Errorf("An assistant message with 'tool_calls' must be "+
					"followed by tool messages responding to each 'tool_call_id'. "+
					"(insufficient tool messages following tool_calls message) "+
					"[历史：%s]", rolesOf(msgs))
			}
		}
	}
	return nil
}

func rolesOf(msgs []map[string]any) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		role, _ := m["role"].(string)
		if n := len(asSlice(m["tool_calls"])); n > 0 {
			role += fmt.Sprintf("(tool_calls=%d)", n)
		}
		if id, ok := m["tool_call_id"].(string); ok {
			role += "(" + id + ")"
		}
		parts = append(parts, role)
	}
	return strings.Join(parts, " → ")
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// fakeOpenAIServer 起一个假服务：第一次调用要求问用户，之后给出凭证。
//
// 它**校验协议**，非法请求按真接口的行为返回 400 —— 这正是复现那个 bug
// 所需要的：如果实现又退回到「留下悬空 tool_calls」，这个测试会以
// 一模一样的 400 失败。
type fakeOpenAIServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls int
	seen  []string
	// 第几次调用要求提问（默认 1）
	askOn int
}

func newFakeOpenAIServer(t *testing.T) *fakeOpenAIServer {
	t.Helper()
	f := &fakeOpenAIServer{askOn: 1}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)

		f.mu.Lock()
		f.calls++
		n := f.calls
		f.seen = append(f.seen, rolesOf(req.Messages))
		f.mu.Unlock()

		// ★ 真接口怎么拒，这里就怎么拒
		if err := assertOpenAIProtocol(req.Messages); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": err.Error(), "type": "invalid_request_error",
				},
			})
			return
		}

		var message map[string]any
		if n == f.askOn {
			args, _ := json.Marshal(map[string]any{
				"id": "payment", "header": "付款方式",
				"question": "这台打印机是怎么付款的？",
				"options": []map[string]any{
					{"label": "银行转账，取得专用发票（推荐）", "description": "可抵扣进项税"},
					{"label": "现金支付，只有收据", "description": "全额计入费用"},
				},
			})
			message = map[string]any{
				"role": "assistant", "content": "",
				"tool_calls": []map[string]any{{
					"id": "call_1", "type": "function",
					"function": map[string]any{"name": "ask_user", "arguments": string(args)},
				}},
			}
		} else {
			message = map[string]any{
				"role": "assistant",
				"content": `{"voucher":{"word":"记","biz_date":"2025-03-11",` +
					`"remark":"购打印机","entries":[` +
					`{"summary":"购打印机","account_code":"560206","debit":300000,"credit":0,"dept_id":1},` +
					`{"summary":"购打印机","account_code":"1002","debit":0,"credit":300000}]},` +
					`"confidence":0.9,"reasoning":"办公费，银行付款","evidence":[],"warnings":[]}`,
			}
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "fake-1",
			"choices": []map[string]any{{
				"index": 0, "message": message,
				"finish_reason": map[bool]string{true: "tool_calls", false: "stop"}[n == f.askOn],
			}},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeOpenAIServer) sequences() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

// ★ 用户报的那个 bug 的端到端回归。
//
// 会计问一句 → 用户点选项 → 会计给凭证。中间那次请求带的是
// 「上一轮的提问 + 这一轮的回答」，历史必须**协议合法**。
func TestAccountantAskThenAnswerOverHTTP(t *testing.T) {
	srv := newFakeOpenAIServer(t)
	prov, err := NewOpenAICompat(Options{
		Name: "假模型", Model: "fake-1", BaseURL: srv.URL,
		Kind: KindLocal, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &Accountant{Provider: prov, Tools: NewToolSet(AskUserTool())}
	ctx := context.Background()

	// 第一句：会计提问
	history := []Message{
		{Role: "system", Content: "你是会计"},
		{Role: "user", Content: "业务描述：昨天买了台打印机"},
	}
	reply, history, err := a.Reply(ctx, history)
	if err != nil {
		t.Fatalf("第一轮不该报错: %v", err)
	}
	if reply.Question == nil || reply.Question.ID != "payment" {
		t.Fatalf("第一轮应当提问：%+v", reply)
	}

	// 用户点选项（结构化回答）
	history = append(history, Message{
		Role: "user",
		Content: AnswerMessage(reply.Question, AccountantAnswer{
			QuestionID: "payment",
			Selected:   []string{"银行转账，取得专用发票（推荐）"},
		}),
	})

	// ★ 第二轮：这里曾经返回 HTTP 400，整段对话就此废掉
	reply2, _, err := a.Reply(ctx, history)
	if err != nil {
		t.Fatalf("★ 用户点选项之后不该报错（真接口会 400）：%v", err)
	}
	if reply2.Proposal == nil {
		t.Fatalf("第二轮应当给出凭证：%+v", reply2)
	}

	// 服务端看到的历史：**不允许出现带 tool_calls 的 assistant 消息**
	seqs := srv.sequences()
	if len(seqs) != 2 {
		t.Fatalf("应当调了 2 次模型，实际 %d 次：%v", len(seqs), seqs)
	}
	if strings.Contains(seqs[1], "tool_calls") {
		t.Errorf("★ 第二轮请求里带着 tool_calls，而它没有对应的 tool 消息 —— "+
			"真接口会直接 400。实际历史：%s", seqs[1])
	}
	// 提问与回答都要在历史里（不然对话就断了）
	if !strings.Contains(seqs[1], "assistant") {
		t.Errorf("第二轮看不见第一轮的提问：%s", seqs[1])
	}
}

// ★ 一整段对话跑下来，每一次请求都必须协议合法。
//
// 上面那条守的是「点选项」这个具体场景；这一条守的是**任意轮次**：
// 只要实现里又冒出「写了一条 tool_calls 就提前返回」的路径，
// 假服务就会在那一轮返回 400，测试立刻红。
func TestAccountantMultiTurnStaysProtocolValid(t *testing.T) {
	srv := newFakeOpenAIServer(t)
	srv.askOn = 3 // 第三轮才问
	prov, err := NewOpenAICompat(Options{
		Name: "假模型", Model: "fake-1", BaseURL: srv.URL, Kind: KindLocal,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &Accountant{Provider: prov, Tools: NewToolSet(AskUserTool())}
	history := []Message{
		{Role: "system", Content: "你是会计"},
		{Role: "user", Content: "业务描述：昨天买了台打印机"},
	}
	ctx := context.Background()
	for turn := 1; turn <= 4; turn++ {
		reply, next, err := a.Reply(ctx, history)
		if err != nil {
			t.Fatalf("第 %d 轮报错（历史会一直坏下去）：%v\n服务端看到：%v",
				turn, err, srv.sequences())
		}
		history = next
		if reply.Question != nil {
			history = append(history, Message{
				Role: "user",
				Content: AnswerMessage(reply.Question, AccountantAnswer{
					QuestionID: reply.Question.ID, Custom: "银行转账 3000",
				}),
			})
		}
	}
	// 每一次请求都不能带悬空的 tool_calls
	for i, seq := range srv.sequences() {
		if strings.Contains(seq, "tool_calls") && !strings.Contains(seq, "tool(") {
			t.Errorf("第 %d 次请求的历史里有没应答的 tool_calls：%s", i+1, seq)
		}
	}
}

// 假服务本身要能把非法请求拒掉 —— 否则上面两条测试就是假绿。
//
// 这一条是**元测试**：验证「测谎仪」本身工作正常。
func TestFakeServerRejectsDanglingToolCalls(t *testing.T) {
	srv := newFakeOpenAIServer(t)
	body := `{"model":"fake-1","messages":[
	  {"role":"user","content":"hi"},
	  {"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function",
	    "function":{"name":"ask_user","arguments":"{}"}}]},
	  {"role":"user","content":"answer"}
	]}`
	resp, err := http.Post(srv.URL+"/chat/completions", "application/json",
		strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("★ 假服务没有拒绝悬空的 tool_calls（状态 %d）—— "+
			"那它挡不住那个 bug，上面两条测试就是假绿", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "must be followed by tool messages") {
		t.Errorf("拒的理由要和真接口一致，实际：%s", raw)
	}
}
