package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"miniaccount/internal/ai/aiprovider"
	"miniaccount/internal/domain/ai"
)

// ---------------------------------------------------------------------------
// 会计（对话式记账）
// ---------------------------------------------------------------------------
//
// # 为什么要做成对话，而不是一个「生成建议」按钮
//
// 用户脑子里的一笔业务是「昨天买了台打印机」，不是一个填好的表单。
// 原来的界面要求他先选业务类型、再填金额、日期、对方户名、资金方向 ——
// 那是**会计要问的问题**，不是用户想说的话。结果是：
// 填不全就生成，生成了不对再改，改完还是不对。
//
// 真实的会计不是这样。他先听你说，缺什么问什么，问够了才动笔。
// 这一节就是把「会问」这件事补上：
//
//	用户：昨天买了台打印机
//	会计：这台打印机多少钱？（追问，带选项）
//	用户：3000，开了专票
//	会计：（给出凭证草稿）
//
// # 会话存在哪
//
// 存在**内存**里，与批量记账的 run 一样（见 aiagent.go）。
// 理由也一样：这是一次操作过程的中间状态，不是账务数据 ——
// 账务数据是那张凭证草稿，它一落库就有完整轨迹（建议记录 + 操作日志）。
// 把对话也持久化，换来的是「下次打开还能看到上次聊到哪」，
// 而那正好是最容易**过期**的东西：一周前的半截对话，
// 用户自己都不记得上下文了，接着聊只会更错。
//
// 代价是重启丢会话。可以接受：重开一个，重新描述一句就是了。

// AccountantOption 是一个可选回答。
type AccountantOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	// Recommended 为真表示这是会计推荐的那个（选项里的第一个）。
	Recommended bool `json:"recommended"`
}

// AccountantQuestion 是会计的追问。
type AccountantQuestion struct {
	Header   string             `json:"header"`
	Question string             `json:"question"`
	Options  []AccountantOption `json:"options"`
}

// AccountantTurn 是对话里的一条消息。
type AccountantTurn struct {
	// Role 是 user（用户说的）或 accountant（会计说的）。
	Role string `json:"role"`
	// Text 是正文。
	Text string `json:"text"`
	// Question 非空表示这一条是**在问用户**。
	Question *AccountantQuestion `json:"question,omitempty"`
	// Voucher 非空表示这一条给出了凭证草稿。
	Voucher *VoucherDraft `json:"voucher,omitempty"`
	// Failures / Warnings 是护栏结论，界面要摊开展示。
	Failures []string `json:"failures,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	// SuggestionID 是这条草稿对应的建议记录 id（采纳时要用）。
	SuggestionID int64 `json:"suggestionId,omitempty"`
	// Passed 为真表示这张草稿通过了全部护栏，可以采纳。
	Passed bool `json:"passed,omitempty"`
	// Model / Tokens 是这个回合的成本。
	Model     string `json:"model,omitempty"`
	TokensIn  int    `json:"tokensIn,omitempty"`
	TokensOut int    `json:"tokensOut,omitempty"`
	// At 是这条消息的时间。
	At string `json:"at"`
}

// AccountantSession 是一次对话。
type AccountantSession struct {
	ID      string           `json:"id"`
	Turns   []AccountantTurn `json:"turns"`
	Asks    int              `json:"asks"`
	Busy    bool             `json:"busy"`
	Updated string           `json:"updated"`
}

// accountantStore 是进程内的会话表。
type accountantStore struct {
	mu       sync.Mutex
	sessions map[string]*accountantSessionState
	order    []string
}

type accountantSessionState struct {
	view    AccountantSession
	history []aiprovider.Message
	// pending 是「上一个问题」，用户下一句话就是回答它。
	pending *aiprovider.AccountantQuestion
	// input 是第一句业务描述（用于护栏里的金额比对与历史检索）。
	input aiprovider.Input
	// lctxDigest 等是审计要用的。
	digest string
}

var accountantSessions = &accountantStore{sessions: map[string]*accountantSessionState{}}

// maxAccountantSessions 是最多同时留几段对话。
//
// 与批量记账的 run 一样要有个上限：每次对话都带着完整的模型上下文
// （科目表、往来单位清单加起来几十 KB），留着不放会一直吃内存。
const maxAccountantSessions = 20

func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------------------
// 对外接口
// ---------------------------------------------------------------------------

// AccountantSendInput 是「跟会计说一句话」。
type AccountantSendInput struct {
	// SessionID 留空表示开一段新对话。
	SessionID string
	// Text 是用户说的话（第一句通常是业务描述）。
	Text string
}

// AccountantSend 把用户的一句话交给会计，返回会计的应答。
//
// 会计要么**问一句**（信息不够），要么**给出凭证草稿**。
// 两条路都走同一套护栏 —— 问了几轮不影响校验的松紧。
func (s *Service) AccountantSend(ctx context.Context, in AccountantSendInput) (*AccountantSession, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, errors.New("请先描述这笔业务")
	}
	prov, err := s.db.AI().DefaultProvider(ctx)
	if err != nil {
		return nil, err
	}
	if prov == nil {
		return nil, errors.New("没有可用的模型服务。请先到「设置 → AI 记账助手」配置并启用一个")
	}

	id := strings.TrimSpace(in.SessionID)
	accountantSessions.mu.Lock()
	st := accountantSessions.sessions[id]
	if st == nil {
		id = newSessionID()
		st = &accountantSessionState{view: AccountantSession{ID: id, Turns: []AccountantTurn{}}}
		accountantSessions.sessions[id] = st
		accountantSessions.order = append(accountantSessions.order, id)
		accountantSessions.pruneLocked()
	}
	st.view.Busy = true
	accountantSessions.mu.Unlock()

	defer func() {
		accountantSessions.mu.Lock()
		st.view.Busy = false
		accountantSessions.mu.Unlock()
	}()

	// 1. 第一次说话：把账套上下文装进来（科目表、往来单位、历史同类）
	if len(st.history) == 0 {
		if err := s.accountantPrime(ctx, st, text); err != nil {
			return nil, err
		}
	}

	// 2. 用户这一句进历史。若上一轮是追问，把「这是在回答什么」带上 ——
	//    只回「银行」两个字，模型下一轮可能当成一句新的业务描述。
	userText := text
	if st.pending != nil {
		userText = aiprovider.QuestionMessage(st.pending, text)
		st.pending = nil
	} else {
		userText = "业务描述：" + text
	}
	st.history = append(st.history, aiprovider.Message{Role: "user", Content: userText})

	// 3. 跑一轮会计
	acct := &aiprovider.Accountant{
		Provider: prov,
		Tools: aiprovider.NewToolSet(
			aiprovider.AskUserTool(),
			aiprovider.SearchAccountsTool(s.db.AI()),
			aiprovider.SearchContactsTool(s.db.AI()),
			aiprovider.FindSimilarVouchersTool(s.db.AI()),
		),
		Options: aiprovider.AgentOptions{
			MaxRounds: 4, MaxTokens: 2048, Temperature: 0,
		},
		Asked: st.view.Asks,
	}
	reply, history, rerr := acct.Reply(ctx, st.history)
	st.history = history
	if rerr != nil {
		// 出错也要把「用户说了什么」留在会话里，否则用户会以为消息丢了
		// 又重发一遍，而模型那边其实已经收到了。
		st.view.Turns = append(st.view.Turns, AccountantTurn{
			Role: "user", Text: text, At: time.Now().Format(time.RFC3339),
		})
		return &st.view, rerr
	}

	st.view.Turns = append(st.view.Turns, AccountantTurn{
		Role: "user", Text: text, At: time.Now().Format(time.RFC3339),
	})

	if reply.Question != nil {
		st.pending = reply.Question
		st.view.Asks++
		st.view.Turns = append(st.view.Turns, AccountantTurn{
			Role: "accountant",
			Text: reply.Question.Question,
			Question: &AccountantQuestion{
				Header:   reply.Question.Header,
				Question: reply.Question.Question,
				Options:  toServiceOptions(reply.Question.Options),
			},
			Model: reply.Model, TokensIn: reply.TokensIn, TokensOut: reply.TokensOut,
			At: time.Now().Format(time.RFC3339),
		})
		accountantSessions.touch(st)
		return &st.view, nil
	}

	// 4. 给了凭证：走**与单次建议完全相同**的护栏与审计
	turn := AccountantTurn{
		Role: "accountant", Text: reply.Say,
		Model: reply.Model, TokensIn: reply.TokensIn, TokensOut: reply.TokensOut,
		At: time.Now().Format(time.RFC3339),
	}
	if reply.Proposal != nil {
		res := s.accountantValidate(ctx, prov, reply, st.input)
		if res != nil {
			turn.SuggestionID = res.SuggestionID
			if res.Proposal != nil {
				turn.Voucher = s.voucherDraftOf(ctx, res.Proposal)
			}
			if res.Report != nil {
				turn.Passed = res.Report.Passed()
				turn.Failures = checkTitles(res.Report.Failures())
				turn.Warnings = checkTitles(res.Report.Warnings())
			}
			if res.Err != nil {
				turn.Text = strings.TrimSpace(turn.Text + "\n" + res.Err.Error())
			}
		}
	}
	st.view.Turns = append(st.view.Turns, turn)
	accountantSessions.touch(st)
	return &st.view, nil
}

// AccountantSession 读回一段对话。
func (s *Service) AccountantSession(id string) (*AccountantSession, error) {
	accountantSessions.mu.Lock()
	defer accountantSessions.mu.Unlock()
	st := accountantSessions.sessions[id]
	if st == nil {
		return nil, fmt.Errorf("对话不存在或已过期（重启会清空）")
	}
	out := st.view
	out.Turns = append([]AccountantTurn(nil), st.view.Turns...)
	return &out, nil
}

// AccountantReset 清空一段对话（换一笔业务重新说）。
func (s *Service) AccountantReset(id string) (*AccountantSession, error) {
	accountantSessions.mu.Lock()
	defer accountantSessions.mu.Unlock()
	st := accountantSessions.sessions[id]
	if st == nil {
		return &AccountantSession{ID: "", Turns: []AccountantTurn{}}, nil
	}
	st.view.Turns = []AccountantTurn{}
	st.view.Asks = 0
	st.history = nil
	st.pending = nil
	st.input = aiprovider.Input{}
	return &AccountantSession{ID: st.view.ID, Turns: []AccountantTurn{}}, nil
}

// accountantPrime 装上下文并写下第一轮历史。
func (s *Service) accountantPrime(ctx context.Context, st *accountantSessionState, text string) error {
	prov, err := s.db.AI().DefaultProvider(ctx)
	if err != nil {
		return err
	}
	repo := s.db.AI()
	// 账套上下文（期间日历、科目树）由 AI 仓储在检索与护栏里各自加载，
	// 这里只需要给提示词用的那几段。
	book, err := repo.BookContext(ctx)
	if err != nil {
		return fmt.Errorf("加载账套信息失败: %w", err)
	}
	accts, err := repo.Accounts(ctx)
	if err != nil {
		return fmt.Errorf("加载科目表失败: %w", err)
	}
	contacts, err := repo.Contacts(ctx)
	if err != nil {
		return fmt.Errorf("加载往来单位失败: %w", err)
	}
	prompt, err := repo.PromptConfig(ctx)
	if err != nil {
		return err
	}

	in := aiprovider.Input{
		Task: aiprovider.TaskFreeform, Text: text,
		Book: book, Accounts: accts, Contacts: contacts,
		Today: time.Now().Format("2006-01-02"), Prompt: prompt,
	}
	// 历史同类凭证：检索失败不阻断 —— 它只是加分项
	if ex, rerr := repo.Similar(ctx, text, "", 0, 4); rerr == nil {
		in.Examples = ex
	}
	system := aiprovider.AccountantSystemPrompt(in)
	st.digest = ai.Digest(system)
	st.input = in
	privacy := aiprovider.DefaultPrivacy(prov.Kind())
	st.history = []aiprovider.Message{
		{Role: "system", Content: privacy.Apply(system)},
	}
	return nil
}

// accountantValidate 走与单次建议相同的护栏。
func (s *Service) accountantValidate(ctx context.Context, prov aiprovider.Provider,
	reply *aiprovider.AccountantReply, in aiprovider.Input) *aiprovider.Result {
	lctx, err := s.db.AI().LedgerContext(ctx)
	if err != nil {
		return nil
	}
	sg := aiSuggester(s, prov)
	// 目标类型写 accountant：审计表里要能分开「单次建议」与「对话里给出的」，
	// 两者的采纳率不是一回事。
	return sg.ValidateProposal(ctx, &aiprovider.Response{
		Content: reply.Raw, Model: reply.Model,
		TokensIn: reply.TokensIn, TokensOut: reply.TokensOut,
	}, lctx, s.accountantDigest(in), in, "accountant", nil)
}

// accountantDigest 复算提示词指纹（与首轮登录时算的是同一段文本）。
func (s *Service) accountantDigest(in aiprovider.Input) string {
	return ai.Digest(aiprovider.AccountantSystemPrompt(in))
}

// checkTitles 把护栏结论转成界面直接显示的一行行文字。
//
// 带 detail 的写成「标题：细节」——「科目不存在」这种标题看不出是哪个科目，
// 而用户要改的恰恰是那个科目。
func checkTitles(cs []ai.Check) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if c.Detail != "" {
			out = append(out, c.Title+"："+c.Detail)
			continue
		}
		out = append(out, c.Title)
	}
	return out
}

func toServiceOptions(opts []aiprovider.AccountantOption) []AccountantOption {
	out := make([]AccountantOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, AccountantOption{
			Label: o.Label, Description: o.Description, Recommended: o.Recommended,
		})
	}
	return out
}

// voucherDraftOf 把提议落成界面要的凭证草稿形状。
//
// 与单次建议（AISuggest）用的是同一个函数：同一个提议，
// 从这里看和从那里看必须是同一张凭证。
//
// ★ 辅助核算渲染成**名称**（「管理部门」）而不是「部门#1」——
// 会计写给你的草稿上写着「部门#1」，你没法核对它对不对。
func (s *Service) voucherDraftOf(ctx context.Context,
	prop *ai.Proposal) *VoucherDraft {
	v, err := prop.Materialize("AI 提议", nil)
	if err != nil {
		return nil
	}
	names, _ := s.contactNames(ctx)
	deptNames, _ := s.departmentNames(ctx)
	out := &VoucherDraft{
		Word: string(v.Word), BizDate: v.BizDate.String(),
		Remark: v.Remark, Total: v.TotalDebit(),
	}
	for _, e := range v.Entries {
		out.Entries = append(out.Entries, ClosingEntry{
			AccountCode: e.AccountCode, Summary: e.Summary,
			Debit: e.Debit, Credit: e.Credit,
			AuxDesc: describeAux(e.Aux, names, deptNames),
		})
	}
	return out
}

func (st *accountantStore) touch(state *accountantSessionState) {
	accountantSessions.mu.Lock()
	defer accountantSessions.mu.Unlock()
	state.view.Updated = time.Now().Format(time.RFC3339)
}

// pruneLocked 丢掉最老的会话。调用方必须已持锁。
func (st *accountantStore) pruneLocked() {
	for len(st.order) > maxAccountantSessions {
		old := st.order[0]
		st.order = st.order[1:]
		delete(st.sessions, old)
	}
}
