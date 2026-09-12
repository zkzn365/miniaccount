// Package aiprovider 对接大模型服务，把「提议」这一步真正做出来。
//
// # 这个包与 internal/domain/ai 的分工
//
//	domain/ai      契约 + 护栏（纯逻辑，不联网，无副作用）
//	aiprovider     提示词构造、HTTP 调用、编排（检索 → 生成 → 校验 → 记账审计）
//
// 分开的好处是护栏可以用假模型测透，而换模型供应商不必碰护栏。
//
// # 支持的部署形态
//
//	本地：Ollama（http://127.0.0.1:11434/v1）、llama.cpp server、vLLM
//	云端：DeepSeek、通义千问、OpenAI 及一切 OpenAI 兼容接口
//
// 全部走 OpenAI 兼容的 /v1/chat/completions，一套代码覆盖两种形态。
// 默认走本地 —— 财务数据不该默认离开本机。
package aiprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 调用相关错误。
var (
	ErrDisabled     = errors.New("ai: AI 功能未启用")
	ErrNoProvider   = errors.New("ai: 没有可用的模型服务")
	ErrBadResponse  = errors.New("ai: 模型返回了无法解析的响应")
	ErrUpstreamFail = errors.New("ai: 模型服务返回错误")
)

// ---------------------------------------------------------------------------
// 请求 / 响应
// ---------------------------------------------------------------------------

// Request 是一次模型调用。
type Request struct {
	System string
	User   string
	// Messages 是多轮对话历史（Agent 循环用）。
	//
	// 非空时**忽略** System / User —— 历史里已经包含它们。
	// 分成两个入口而不是让调用方自己拼：单轮调用是最常见的用法，
	// 逼着每个人去拼一个 messages 数组只会让简单的事情变复杂。
	Messages []Message
	// Tools 是本次可用的工具（为空表示不做 function calling）。
	Tools    []ToolSpec
	JSONMode bool
	// Temperature 建议填 0：记账不需要创造力，
	// 需要的是同样的输入给出同样的科目。
	Temperature float64
	MaxTokens   int
}

// Message 是一条对话消息。
type Message struct {
	// Role 是 system | user | assistant | tool。
	Role string
	// Content 是消息正文。
	Content string
	// ToolCalls 是 assistant 消息里的工具调用请求。
	ToolCalls []ToolCall
	// ToolCallID 是 tool 消息对应的调用 id。
	ToolCallID string
}

// ToolSpec 描述一个可调用的工具（JSON Schema）。
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolCall 是模型请求的一次工具调用。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Response 是一次模型调用的结果。
type Response struct {
	Content string
	// ToolCalls 是模型请求调用的工具（为空表示它给了最终答案）。
	ToolCalls []ToolCall
	Model     string
	// TokensIn / TokensOut 用于成本统计与用量上限。
	TokensIn  int
	TokensOut int
	// Latency 是调用耗时，界面可据此提示「本地模型较慢」。
	Latency time.Duration
}

// Provider 是一个模型服务。
//
// 实现必须尊重 ctx 的取消：用户点了「取消」就该立刻停，
// 而不是等模型把 800 个 token 吐完。
type Provider interface {
	// Name 返回服务名，写进审计表。
	Name() string
	// Model 返回模型名，写进审计表。
	Model() string
	// Kind 返回 local 或 cloud —— 决定是否需要隐私提示。
	Kind() Kind
	// Complete 执行一次补全。
	Complete(ctx context.Context, req Request) (*Response, error)
}

// Kind 是服务部署形态。
type Kind string

// 部署形态。
const (
	KindLocal Kind = "local"
	KindCloud Kind = "cloud"
)

// IsLocal 报告数据是否留在本机。
func (k Kind) IsLocal() bool { return k == KindLocal }

// ---------------------------------------------------------------------------
// OpenAI 兼容客户端
// ---------------------------------------------------------------------------

// OpenAICompat 对接一切 OpenAI 兼容的 /v1/chat/completions。
//
// 这是刻意的选择：本地 Ollama、llama.cpp、vLLM 与云端 DeepSeek、
// 通义、OpenAI 都实现了这个协议，一套客户端覆盖全部，
// 用户换服务商不需要改任何配置以外的东西。
type OpenAICompat struct {
	name    string
	model   string
	kind    Kind
	baseURL string
	apiKey  string
	client  *http.Client
}

// Options 构造 OpenAICompat 的参数。
type Options struct {
	// Name 是显示名，如「本地 Ollama」「DeepSeek」。
	Name string
	// Model 是模型名，如 qwen2.5:7b-instruct、deepseek-chat。
	Model string
	// BaseURL 是服务根地址，**不含** /chat/completions。
	// 如 http://127.0.0.1:11434/v1。
	BaseURL string
	// APIKey 留空适用于绝大多数本地服务。
	APIKey string
	// Kind 为空时按 BaseURL 推断：指向本机即 local。
	Kind Kind
	// Timeout 为 0 时取 DefaultTimeout。
	Timeout time.Duration
	// Client 允许注入自定义 HTTP 客户端（测试用）。
	Client *http.Client
}

// DefaultTimeout 是单次调用的默认超时。
//
// 本地 7B 模型生成一张凭证的 JSON 通常 3~15 秒，
// 老机器上可能到 60 秒；给到 3 分钟留足余量，
// 同时保证不会无限挂住界面。
const DefaultTimeout = 3 * time.Minute

// NewOpenAICompat 构造一个 OpenAI 兼容客户端。
func NewOpenAICompat(o Options) (*OpenAICompat, error) {
	if strings.TrimSpace(o.BaseURL) == "" {
		return nil, fmt.Errorf("%w: BaseURL 为空", ErrNoProvider)
	}
	if strings.TrimSpace(o.Model) == "" {
		return nil, fmt.Errorf("%w: Model 为空", ErrNoProvider)
	}
	kind := o.Kind
	if kind == "" {
		kind = inferKind(o.BaseURL)
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	name := o.Name
	if name == "" {
		name = o.Model
	}
	return &OpenAICompat{
		name: name, model: o.Model, kind: kind,
		baseURL: strings.TrimRight(o.BaseURL, "/"), apiKey: o.APIKey,
		client: client,
	}, nil
}

// inferKind 按地址推断部署形态。
//
// 指向本机的一律视为本地 —— 猜错的代价不对称：
// 把云端误判为本地会漏掉隐私提示，把本地误判为云端只是多弹一次窗。
func inferKind(baseURL string) Kind {
	u := strings.ToLower(baseURL)
	for _, host := range []string{
		"127.0.0.1", "localhost", "0.0.0.0", "[::1]", "host.docker.internal",
	} {
		if strings.Contains(u, host) {
			return KindLocal
		}
	}
	return KindCloud
}

// Name 返回服务名。
func (c *OpenAICompat) Name() string { return c.name }

// Model 返回模型名。
func (c *OpenAICompat) Model() string { return c.model }

// Kind 返回部署形态。
func (c *OpenAICompat) Kind() Kind { return c.kind }

// ---- 协议结构 ----

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls 在 assistant 消息里出现，表示模型请求调用工具。
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
	// ToolCallID 在 tool 消息里出现，指回它响应的是哪次调用。
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// chatTool 是请求里的工具声明（OpenAI 兼容格式）。
type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

type chatRequest struct {
	Model       string          `json:"model"`
	Messages    []chatMessage   `json:"messages"`
	Temperature float64         `json:"temperature"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream"`
	Format      json.RawMessage `json:"format,omitempty"`          // Ollama 用
	ResponseFmt *respFmt        `json:"response_format,omitempty"` // OpenAI 用
	Tools       []chatTool      `json:"tools,omitempty"`
}

type respFmt struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Complete 执行一次补全。
func (c *OpenAICompat) Complete(ctx context.Context, req Request) (*Response, error) {
	start := time.Now()

	body := chatRequest{
		Model:       c.model,
		Messages:    buildMessages(req),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      false,
	}
	if req.JSONMode {
		// 两种写法同时给出，服务端认哪个用哪个：
		// OpenAI/DeepSeek 用 response_format，Ollama 用 format。
		body.Format = json.RawMessage(`"json"`)
		body.ResponseFmt = &respFmt{Type: "json_object"}
	}
	if len(req.Tools) > 0 {
		body.Tools = make([]chatTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			var ct chatTool
			ct.Type = "function"
			ct.Function.Name = t.Name
			ct.Function.Description = t.Description
			ct.Function.Parameters = t.Parameters
			body.Tools = append(body.Tools, ct)
		}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ai: 序列化请求失败: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("ai: 构造请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: 无法连接 %s: %v", ErrUpstreamFail, c.baseURL, err)
	}
	defer resp.Body.Close()

	// 限制读取长度：模型服务异常时可能吐回一个巨大的 HTML 错误页，
	// 直接读进内存会把界面拖死。
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: 读取响应失败: %v", ErrUpstreamFail, err)
	}

	var out chatResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("%w: HTTP %d，内容无法解析为 JSON（前 200 字节：%s）",
			ErrBadResponse, resp.StatusCode, truncate(payload, 200))
	}
	if out.Error != nil {
		return nil, fmt.Errorf("%w: HTTP %d %s: %s",
			ErrUpstreamFail, resp.StatusCode, out.Error.Type, out.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d（%s）",
			ErrUpstreamFail, resp.StatusCode, truncate(payload, 200))
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("%w: 响应里没有 choices", ErrBadResponse)
	}

	model := out.Model
	if model == "" {
		model = c.model
	}
	msg := out.Choices[0].Message
	// 工具调用有两种表达：OpenAI 风格的 tool_calls 数组，
	// 以及部分本地模型直接吐的 JSON。这里只处理前者 ——
	// 后者本来就该被当成「模型没按契约走」，由解析层拒绝。
	calls := make([]ToolCall, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		calls = append(calls, ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
		})
	}
	return &Response{
		Content:   msg.Content,
		ToolCalls: calls,
		Model:     model,
		TokensIn:  out.Usage.PromptTokens,
		TokensOut: out.Usage.CompletionTokens,
		Latency:   time.Since(start),
	}, nil
}

// buildMessages 把 Request 转成协议里的消息数组。
//
// 多轮历史优先：Agent 循环会把 system/user/assistant/tool 全都放进
// Messages，此时再拼 System/User 会重复。
func buildMessages(req Request) []chatMessage {
	if len(req.Messages) > 0 {
		out := make([]chatMessage, 0, len(req.Messages))
		for _, m := range req.Messages {
			cm := chatMessage{
				Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID,
			}
			for _, tc := range m.ToolCalls {
				var ctc chatToolCall
				ctc.ID = tc.ID
				ctc.Type = "function"
				ctc.Function.Name = tc.Name
				ctc.Function.Arguments = tc.Arguments
				cm.ToolCalls = append(cm.ToolCalls, ctc)
			}
			out = append(out, cm)
		}
		return out
	}
	return []chatMessage{
		{Role: "system", Content: req.System},
		{Role: "user", Content: req.User},
	}
}

// maxResponseBytes 是单次响应体读取上限（1 MiB）。
const maxResponseBytes = 1 << 20

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// ---------------------------------------------------------------------------
// 空实现
// ---------------------------------------------------------------------------

// Disabled 是「AI 关闭」模式下的 Provider。
//
// 显式提供一个会报错的实现，而不是用 nil 表示关闭：
// nil 会让调用点遍布 `if provider != nil` 判断，
// 漏掉一处的后果就是空指针崩溃。
type Disabled struct{ Reason string }

// Name 返回服务名。
func (d Disabled) Name() string { return "未启用" }

// Model 返回模型名。
func (d Disabled) Model() string { return "" }

// Kind 返回部署形态。
func (d Disabled) Kind() Kind { return KindLocal }

// Complete 永远返回 ErrDisabled。
func (d Disabled) Complete(context.Context, Request) (*Response, error) {
	if d.Reason != "" {
		return nil, fmt.Errorf("%w: %s", ErrDisabled, d.Reason)
	}
	return nil, ErrDisabled
}
