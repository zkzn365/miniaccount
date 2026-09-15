// Package main 是桌面应用（Wails v2）的入口。
//
// # 这一层只做两件事
//
//  1. 把 internal/service 的方法暴露成前端可调用的绑定
//  2. 管理「当前打开的账套」这一份进程级状态
//
// 它**不做任何账务判断**。所有校验、取数、事务都在 service 与领域层里，
// 这一层只负责：把界面传来的 JSON 转成 Go 参数、调用 service、
// 把 Go 的错误转成界面能显示的结构化结果。
//
// # 为什么错误也要结构化
//
// Wails 的绑定在 Go 返回 error 时会让前端拿到一个 rejected promise，
// 错误文本会丢栈、丢类型，前端只能 `alert(e)`。
// 对记账软件来说这远远不够 —— 用户需要知道
// 「是哪个科目的哪一行错了」「是阻断项还是提醒项」。
// 因此这里把业务失败包成带 kind 的结果对象，让界面能分别渲染。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"miniaccount/internal/backup"
	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
	"miniaccount/internal/service"
	"miniaccount/internal/store/sqlite"
)

// App 是暴露给前端的绑定对象。
//
// ★ 所有导出方法都必须是**并发安全**的：Wails 可能从不同
// WebView 线程同时调用。这里用一把读写锁保护「当前账套」这一份状态。
type App struct {
	ctx context.Context

	mu   sync.RWMutex
	svc  *service.Service
	path string
}

// NewApp 创建应用。
func NewApp() *App { return &App{} }

// ---------------------------------------------------------------- 生命周期

// startup 由 Wails 在窗口就绪后调用。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// shutdown 在窗口关闭时调用，释放账套连接。
//
// SQLite 的 WAL 模式需要在正常关闭时做 checkpoint；
// 直接杀进程会留下一个「下次打开需要恢复」的库文件 ——
// 对记账软件来说，用户看到「数据库需要恢复」的提示会直接怀疑数据丢了。
func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.svc != nil {
		_ = a.svc.Shutdown()
		a.svc = nil
	}
}

func (a *App) context() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// ---------------------------------------------------------------- 结果包装

// FaultKind 是失败的类别，界面据此决定怎么呈现。
type FaultKind string

// 失败类别。
const (
	// FaultNoBook 表示还没打开账套 —— 界面应跳到建账/打开向导。
	FaultNoBook FaultKind = "no_book"
	// FaultBlocked 是业务规则阻断（体检未过、期间已结账……）。
	// 界面应把 Detail 摊开展示，而不是弹一句「操作失败」。
	FaultBlocked FaultKind = "blocked"
	// FaultInvalid 是输入不合法，界面应高亮对应字段。
	FaultInvalid FaultKind = "invalid"
	// FaultIO 是文件/数据库层面的错误。
	FaultIO FaultKind = "io"
	// FaultInternal 是兜底：不该发生，发生了就要能看到细节。
	FaultInternal FaultKind = "internal"
)

// Fault 是一次失败的完整描述。
type Fault struct {
	Kind    FaultKind `json:"kind"`
	Message string    `json:"message"`
	// Detail 是可选的细节（如体检报告、逐行校验结论）。
	Detail string `json:"detail,omitempty"`
}

// Error 让 Fault 满足 error 接口。
//
// # 为什么返回的是 JSON 而不是一句人话
//
// Wails 的绑定层在出错时只能把 `err.Error()` 的**字符串**透给前端，
// Fault 的 Kind 与 Detail 会全部丢掉。而界面恰恰需要 Kind 来决定动作：
// `no_book` 跳建账向导、`blocked` 摊开体检报告、`invalid` 高亮字段 ——
// 只拿到一句消息就只能统一弹个红框。
//
// 把结构化信息编码进消息里，是这一层唯一能把它完整带过去的办法。
// 前端 lib/api.js 负责解析；解析失败时退回「整串当消息用」。
//
// # 为什么必须 nil 安全
//
// Go 里「接口持有 nil 指针」不等于「接口本身是 nil」。
// Wails 的 BoundMethod.Call 对第二个返回值做 `.(error)` 类型断言，
// 断言**只看类型不看值** —— 一个 nil 的 *Fault 会让断言成功，
// 于是每一次**成功**的调用都被当成失败，并在调用 Error() 时
// nil 解引用崩溃。这一行是整个桌面端最容易踩的坑。
func (f *Fault) Error() string {
	if f == nil {
		return ""
	}
	b, err := json.Marshal(f)
	if err != nil {
		return f.Message
	}
	return string(b)
}

// classify 把 service/领域层的错误翻译成界面能区分的类别。
//
// ★ 这份映射是这一层**唯一**的实质逻辑，也是它存在的理由：
// 领域层的错误哨兵很精确，但前端不该 import Go 的错误值。
// 翻译一次，界面就能用 `fault.kind` 做分支。
func classify(err error) *Fault {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, service.ErrNoBook),
		errors.Is(err, sqlite.ErrBookNotSetup):
		return &Fault{Kind: FaultNoBook, Message: err.Error()}

	case errors.Is(err, sqlite.ErrHealthFailed),
		errors.Is(err, sqlite.ErrAlreadyClosed),
		errors.Is(err, sqlite.ErrNotClosed),
		errors.Is(err, sqlite.ErrPriorOpen),
		errors.Is(err, sqlite.ErrLaterClosed),
		errors.Is(err, sqlite.ErrClosedVoucher),
		errors.Is(err, period.ErrNotOpen),
		errors.Is(err, period.ErrPeriodNotFound):
		return &Fault{Kind: FaultBlocked, Message: err.Error()}

	case errors.Is(err, sqlite.ErrBookExists),
		errors.Is(err, sqlite.ErrBadTaxType),
		errors.Is(err, sqlite.ErrBadContactKind),
		errors.Is(err, period.ErrInvalidYearMonth):
		return &Fault{Kind: FaultInvalid, Message: err.Error()}

	// 数据库层的约束冲突：**这是「你的数据有问题」，不是程序坏了**。
	//
	// 归到 FaultInvalid 而不是 FaultInternal，界面才会按「可以改」展示。
	// 唯一约束最常见的来源是「同一件事登记了两次」——所以把话说到点子上。
	case errors.Is(err, sqlite.ErrConflict):
		return &Fault{Kind: FaultInvalid, Message: "这条记录与已有的重复了：" +
			strings.TrimPrefix(err.Error(), "sqlite: ") +
			"\n（同一件事只需要登记一次；若是更正，请先作废原记录再登记新的）"}

	case errors.Is(err, sqlite.ErrCheckFail),
		errors.Is(err, sqlite.ErrForeignKey):
		return &Fault{Kind: FaultInvalid, Message: "数据不符合约束：" +
			strings.TrimPrefix(err.Error(), "sqlite: ") +
			"\n请检查填的内容，或先补齐被引用的那条记录"}

	// 凭证本身的填写错误。
	//
	// ★ 这一类必须归到「输入不合法」而不是「内部错误」：
	// 借贷不平、摘要为空、辅助核算没填，全都是用户**可以改**的问题，
	// 界面要能高亮对应字段并给出可操作的提示。
	// 如果归到 internal，用户看到的是「发生内部错误」——
	// 他会以为软件坏了，而不是自己漏填了一行。
	case errors.Is(err, ledger.ErrNoEntries),
		errors.Is(err, ledger.ErrTooFewEntries),
		errors.Is(err, ledger.ErrNotBalanced),
		errors.Is(err, ledger.ErrBothSides),
		errors.Is(err, ledger.ErrNoAmount),
		errors.Is(err, ledger.ErrNegativeAmount),
		errors.Is(err, ledger.ErrZeroAmount),
		errors.Is(err, ledger.ErrMissingSummary),
		errors.Is(err, ledger.ErrMissingAux),
		errors.Is(err, ledger.ErrWrongAuxKind),
		errors.Is(err, ledger.ErrAccountMissing),
		errors.Is(err, account.ErrNotFound),
		errors.Is(err, account.ErrNotAllowedForTaxType),
		// ★ 存储层的「记录不存在」必须归到「找不到」而不是「内部错误」。
		//
		// 漏了这一条时，点开一张已被删除的凭证会看到
		// 「发生内部错误：sqlite: 记录不存在: 凭证 id=5」——
		// 用户会以为软件坏了，而实际情况是「这张凭证已经不在了」。
		// store 层三十多处都用这个哨兵，漏掉它的影响面很广。
		errors.Is(err, sqlite.ErrNotFound),
		errors.Is(err, account.ErrNotLeaf),
		errors.Is(err, account.ErrDisabled),
		errors.Is(err, voucher.ErrBadWord),
		errors.Is(err, voucher.ErrBadDate),
		errors.Is(err, voucher.ErrNoEntries),
		errors.Is(err, voucher.ErrNotDraft),
		errors.Is(err, sqlite.ErrNotDraft),
		errors.Is(err, account.ErrEmptyCode),
		errors.Is(err, account.ErrNonDigitCode),
		errors.Is(err, account.ErrCodeParentMatch):
		return &Fault{Kind: FaultInvalid, Message: err.Error()}

	// 凭证当前状态不允许该操作：这是**业务规则阻断**，不是输入错误。
	// 界面应当解释原因并给出正确的做法（红字冲销 / 先反结账）。
	case errors.Is(err, voucher.ErrAlreadyPosted),
		errors.Is(err, voucher.ErrAlreadyVoided),
		errors.Is(err, voucher.ErrNotPosted),
		errors.Is(err, voucher.ErrMissingMaker),
		errors.Is(err, voucher.ErrMissingPoster),
		errors.Is(err, sqlite.ErrNotReversible):
		return &Fault{Kind: FaultBlocked, Message: err.Error()}

	case errors.Is(err, backup.ErrBadArchive),
		errors.Is(err, backup.ErrChecksumFail),
		errors.Is(err, backup.ErrVersionTooNew),
		errors.Is(err, os.ErrNotExist),
		errors.Is(err, os.ErrPermission),
		errors.Is(err, sqlite.ErrClosed),
		strings.Contains(err.Error(), "数据库"),
		strings.Contains(err.Error(), "备份"):
		return &Fault{Kind: FaultIO, Message: err.Error()}

	default:
		return &Fault{Kind: FaultInternal, Message: err.Error()}
	}
}

// wrap 把 service 层的 (值, error) 转成绑定层的 (值, error)。
//
// ★ 这是整个绑定层最关键的一行。
//
// 注意签名上的第二个 error 是**接口**。如果写 `return v, classify(err)`
// 而 classify 返回一个 nil 的 *Fault，那么这个 error 接口**不是 nil** ——
// 它持有一个类型为 *Fault 的空指针。Wails 的 BoundMethod.Call 会对它做
// `.(error)` 断言（只看类型、不看值），断言成功即判定为失败，
// 随后调用 Error() 直接 nil 解引用崩溃。
//
// 后果是：所有成功的调用全部报错并让进程进入错误处理路径。
// 所以成功分支必须返回**字面量 nil**，让接口本身为 nil。
func wrap[T any](v T, err error) (T, error) {
	f := classify(err)
	if f == nil {
		return v, nil
	}
	var zero T
	return zero, f
}

// ---------------------------------------------------------------- 账套

// BookStatus 是界面启动时要问的第一件事：现在有没有账套可看。
type BookStatus struct {
	// Open 为真表示已经打开了一个账套。
	Open bool `json:"open"`
	// Path 是账套文件路径。
	Path string `json:"path"`
	// Book 为 nil 表示尚未建账。
	Book *service.BookInfo `json:"book,omitempty"`
	// RecentPath 是最近使用过的账套路径（来自配置，可为空）。
	RecentPath string `json:"recentPath,omitempty"`
}

// CurrentBook 返回当前账套状态。
//
// 界面在启动时先调它：没账套就进建账向导，有账套就直接进首页。
func (a *App) CurrentBook() (out BookStatus, err error) {
	defer recoverTo(&err, "CurrentBook")()
	a.mu.RLock()
	svc, path := a.svc, a.path
	a.mu.RUnlock()

	if svc == nil {
		return BookStatus{Open: false, RecentPath: lastBookPath()}, nil
	}
	book, err := svc.Book(a.context())
	if err != nil {
		if errors.Is(err, service.ErrNoBook) {
			// 文件在但不是账套：仍算「未建账」，但要告诉界面是哪条路径
			return BookStatus{Open: false, Path: path, RecentPath: path}, nil
		}
		return BookStatus{}, classify(err)
	}
	return BookStatus{Open: true, Path: path, Book: book}, nil
}

// OpenBook 打开一个账套文件。文件不存在时会新建一个空库（尚未建账）。
func (a *App) OpenBook(path string) (out BookStatus, err error) {
	defer recoverTo(&err, "OpenBook")()
	if strings.TrimSpace(path) == "" {
		return BookStatus{}, &Fault{Kind: FaultInvalid, Message: "账套路径不能为空"}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return BookStatus{}, &Fault{Kind: FaultInvalid, Message: err.Error()}
	}
	// ★ 父目录不存在就建出来。
	//
	// 用户在界面上填的是「保存到哪」，不是一个已经存在的文件路径。
	// 原来直接把路径交给 SQLite，父目录不存在时报的是
	// 「unable to open database file」这种底层错误 ——
	// 用户看不出是「目录不存在」，只会以为软件坏了。
	if dir := filepath.Dir(abs); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return BookStatus{}, &Fault{
				Kind:    FaultIO,
				Message: "建不了目录 " + dir + "：" + err.Error(),
			}
		}
	}

	svc, err := service.Open(a.context(), service.Options{Path: abs})
	if err != nil {
		return BookStatus{}, classify(err)
	}

	a.mu.Lock()
	if a.svc != nil {
		_ = a.svc.Shutdown() // 先关旧的，避免单文件被两个连接同时持有
	}
	a.svc, a.path = svc, abs
	a.mu.Unlock()

	rememberBookPath(abs)
	// 打开账套之后是搬运「老版本存在本机的记账人」的时机
	a.migrateLegacyBookkeeper()
	return a.CurrentBook()
}

// CloseBook 关闭当前账套。
func (a *App) CloseBook() (err error) {
	defer recoverTo(&err, "CloseBook")()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.svc == nil {
		return nil
	}
	shutdownErr := a.svc.Shutdown()
	a.svc, a.path = nil, ""
	// ★ 必须显式判断：classify(nil) 返回的是 nil 的 *Fault，
	// 直接 `return classify(err)` 会装箱成一个**非 nil 的 error 接口**，
	// Wails 就会把「成功关闭」当成失败。详见 wrap 的注释。
	if shutdownErr == nil {
		return nil
	}
	return classify(shutdownErr)
}

// book 取当前账套；未打开时返回 FaultNoBook。
func (a *App) book() (*service.Service, *Fault) {
	a.mu.RLock()
	svc := a.svc
	a.mu.RUnlock()
	if svc == nil {
		return nil, &Fault{Kind: FaultNoBook, Message: "尚未打开账套"}
	}
	return svc, nil
}

// CreateBook 建账。
func (a *App) CreateBook(in service.CreateBookInput) (out BookStatus, err error) {
	defer recoverTo(&err, "CreateBook")()
	svc, f := a.book()
	if f != nil {
		return BookStatus{}, f
	}
	if _, cerr := svc.CreateBook(a.context(), in); cerr != nil {
		// 走 classify：界面要靠 Kind 把用户引回「打开账套」，
		// 而不是弹一句看不懂的数据库错误
		return BookStatus{}, classify(cerr)
	}
	rememberBookPath(a.path)
	return a.CurrentBook()
}

// ---------------------------------------------------------------- 首页

// Overview 返回首页概览。
func (a *App) Overview() (out *service.Dashboard, err error) {
	defer recoverTo(&err, "Overview")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Overview(a.context()))
}

// ---------------------------------------------------------------- 账期

// Periods 返回账套信息与全部期间状态。
func (a *App) Periods() (out *service.BookInfo, err error) {
	defer recoverTo(&err, "Periods")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Book(a.context()))
}

// PeriodRequest 是「针对某个期间」的操作参数。
//
// ★ 单独定义一个请求结构而不是用位置参数：
// Wails 生成的 JS 绑定对多参数方法只能按位置传，
// 而 `Close(2025, 3, "王主管")` 这种调用在界面上极容易写反。
type PeriodRequest struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	// By 是操作人（结账/反结账必填）。
	By string `json:"by,omitempty"`
}

func (r PeriodRequest) key() (period.Key, error) {
	k := period.NewKey(r.Year, r.Month)
	if !k.Valid() {
		return k, fmt.Errorf("会计期间 %04d-%02d 非法", r.Year, r.Month)
	}
	return k, nil
}

// Health 执行结账前体检。
func (a *App) Health(req PeriodRequest) (out *service.HealthInfo, err error) {
	defer recoverTo(&err, "Health")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	k, err := req.key()
	if err != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: err.Error()}
	}
	return wrap(svc.CheckHealth(a.context(), k))
}

// PreviewClose 预览结账，不写任何数据。
//
// 界面应当**先调这个再调 Close**：让用户在点确认前看清
// 本期收入多少、费用多少、是盈是亏、会写哪几条分录。
func (a *App) PreviewClose(req PeriodRequest) (out *service.ClosingPreview, err error) {
	defer recoverTo(&err, "PreviewClose")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	k, err := req.key()
	if err != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: err.Error()}
	}
	return wrap(svc.PreviewClose(a.context(), k))
}

// Close 执行结账。
func (a *App) Close(req PeriodRequest) (out *service.CloseResult, err error) {
	defer recoverTo(&err, "Close")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	k, err := req.key()
	if err != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: err.Error()}
	}
	if strings.TrimSpace(req.By) == "" {
		return nil, &Fault{Kind: FaultInvalid,
			Message: "请填写结账人 —— 记账凭证需要有记账签章"}
	}
	return wrap(svc.Close(a.context(), k, req.By))
}

// Reopen 反结账。
func (a *App) Reopen(req PeriodRequest) (out *service.ReopenResult, err error) {
	defer recoverTo(&err, "Reopen")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	k, err := req.key()
	if err != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: err.Error()}
	}
	if strings.TrimSpace(req.By) == "" {
		return nil, &Fault{Kind: FaultInvalid, Message: "请填写操作人"}
	}
	return wrap(svc.Reopen(a.context(), k, req.By))
}

// ---------------------------------------------------------------- 报表

// ReportRequest 是取报表的参数。
type ReportRequest struct {
	// Kind 是报表类型：trial | bs | pl | cashflow | contact
	Kind string `json:"kind"`
	Year int    `json:"year"`
	// Month 为 0 时按年取值（资产负债表用年末日）。
	Month int `json:"month"`
	// AccountPrefix 供往来余额表与明细账使用。
	AccountPrefix string `json:"accountPrefix,omitempty"`
}

// ReportResult 是统一的报表返回结构。
//
// ★ 刻意用同一个结构承载所有报表，而不是每种报表一个类型。
// 前端只需要一套渲染组件（行 + 列 + 缩进 + 加粗 + 勾稽提示），
// 加一张新报表不用改前端。这也是报表定义做成数据驱动的直接收益。
type ReportResult struct {
	Title    string      `json:"title"`
	Subtitle string      `json:"subtitle"`
	Columns  []string    `json:"columns"`
	Rows     []ReportRow `json:"rows"`
	// Issues 是勾稽问题。**必须展示**：一张数字齐全但内部对不上的
	// 报表比一张明显缺数的报表危险得多。
	Issues []ReportIssue `json:"issues,omitempty"`
}

// ReportRow 是报表的一行。
type ReportRow struct {
	// No 是官方行次（官方报表才有）。
	No string `json:"no,omitempty"`
	// Label 是项目名称。
	Label string `json:"label"`
	// RightNo 是右栏的官方行次。
	RightNo string `json:"rightNo,omitempty"`
	// RightLabel 是资产负债表**右栏**的项目名，其它报表为空。
	//
	// 单独一个字段而不是拼成「资产项　│　负债项」：
	// 拼成一个字符串后前端就没法把两栏拆开做对齐 ——
	// 而资产负债表是左右两栏各自成表的，项目名必须分列显示。
	RightLabel string `json:"rightLabel,omitempty"`
	// Indent 是缩进层级。
	Indent int `json:"indent"`
	// Bold 用于小计/总计行。
	Bold bool `json:"bold"`
	// Values 是各列金额（分）。用字符串传输会在前端又变回浮点，
	// 因此直接用整数分，由前端统一格式化。
	Values []int64 `json:"values"`
	// IsMemo 标记「其中」附列项。
	IsMemo bool `json:"isMemo,omitempty"`
}

// ReportIssue 是一条勾稽问题。
type ReportIssue struct {
	Text  string `json:"text"`
	Fatal bool   `json:"fatal"`
}

// reportBuilder 由各报表类型实现，把领域报表转成统一结构。
func (a *App) report(ctx context.Context, svc *service.Service,
	req ReportRequest) (*ReportResult, error) {

	// 报表查看在**这一层**记：报表是两边（界面与命令行）各自渲染的，
	// service 层没有对应的入口。界面是「谁看了哪张报表」最主要的场合。
	svc.RecordView(ctx, service.AuditEvent{
		Action: audit.ActionViewReport, Entity: "report",
		EntityID: fmt.Sprintf("%s/%04d-%02d", req.Kind, req.Year, req.Month),
		Summary:  "查看报表：" + reportKindLabel(req.Kind),
		Detail: map[string]any{
			"报表": reportKindLabel(req.Kind), "期间": fmt.Sprintf("%04d-%02d", req.Year, req.Month),
		},
	})

	switch req.Kind {
	case "trial":
		return a.trialBalance(ctx, svc, req)
	case "bs":
		return a.balanceSheet(ctx, svc, req)
	case "pl":
		return a.incomeStatement(ctx, svc, req)
	case "cashflow":
		return a.cashFlow(ctx, svc, req)
	case "contact":
		return a.contactBalances(ctx, svc, req)
	default:
		return nil, fmt.Errorf("不支持的报表类型 %q", req.Kind)
	}
}

// Report 生成一张报表。
func (a *App) Report(req ReportRequest) (out *ReportResult, err error) {
	defer recoverTo(&err, "Report")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(a.report(a.context(), svc, req))
}

// ---------------------------------------------------------------- 凭证

// VoucherListRequest 是查凭证列表的参数。
type VoucherListRequest struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	// Status 为空表示全部；否则 draft | posted | voided
	Status string `json:"status,omitempty"`
	// Limit 为 0 时取默认上限。
	Limit int `json:"limit,omitempty"`
}

// ---------------------------------------------------------------- 附件与备份

// FilesDir 返回当前账套的附件目录。
func (a *App) FilesDir() (out string, err error) {
	defer recoverTo(&err, "FilesDir")()
	svc, f := a.book()
	if f != nil {
		return "", f
	}
	return svc.FilesDir(), nil
}

// BackupRequest 是备份参数。
type BackupRequest struct {
	Dest string `json:"dest"`
	// IncludeFiles 为假时只备份数据库；界面必须明确警告附件会丢失。
	IncludeFiles bool `json:"includeFiles"`
}

// Backup 打包备份为一个 .mabak 文件。
func (a *App) Backup(req BackupRequest) (out *backupManifestView, err error) {
	defer recoverTo(&err, "Backup")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	if strings.TrimSpace(req.Dest) == "" {
		return nil, &Fault{Kind: FaultInvalid, Message: "请选择备份文件的保存位置"}
	}
	m, err := svc.Backup(a.context(), service.BackupOptions{
		Dest: req.Dest, IncludeFiles: req.IncludeFiles,
	})
	if err != nil {
		return nil, classify(err)
	}
	return &backupManifestView{
		Path: req.Dest, FormatVersion: m.FormatVersion, AppVersion: m.AppVersion,
		CompanyName: m.CompanyName, CreatedAt: m.CreatedAt,
		DBSize: m.DBSize, DBSHA256: m.DBSHA256,
		FileCount: m.FileCount, FileBytes: m.FileBytes,
		PeriodFrom: m.PeriodFrom, PeriodTo: m.PeriodTo,
		VoucherCount: m.VoucherCount, AccountCount: m.AccountCount,
	}, nil
}

// backupManifestView 是给界面看的备份摘要（不含附件清单，那个可能上千条）。
type backupManifestView struct {
	Path          string `json:"path"`
	FormatVersion int    `json:"formatVersion"`
	AppVersion    string `json:"appVersion"`
	CompanyName   string `json:"companyName"`
	CreatedAt     string `json:"createdAt"`
	DBSize        int64  `json:"dbSize"`
	DBSHA256      string `json:"dbSha256"`
	FileCount     int    `json:"fileCount"`
	FileBytes     int64  `json:"fileBytes"`
	PeriodFrom    string `json:"periodFrom"`
	PeriodTo      string `json:"periodTo"`
	VoucherCount  int    `json:"voucherCount"`
	AccountCount  int    `json:"accountCount"`
}

// InspectBackup 只读查看备份内容，用于「恢复前确认」。
func (a *App) InspectBackup(path string) (out *backupManifestView, err error) {
	defer recoverTo(&err, "InspectBackup")()
	svc := &service.Service{}
	m, err := svc.InspectBackup(path)
	if err != nil {
		return nil, classify(err)
	}
	return &backupManifestView{
		Path: path, FormatVersion: m.FormatVersion, AppVersion: m.AppVersion,
		CompanyName: m.CompanyName, CreatedAt: m.CreatedAt,
		DBSize: m.DBSize, DBSHA256: m.DBSHA256,
		FileCount: m.FileCount, FileBytes: m.FileBytes,
		PeriodFrom: m.PeriodFrom, PeriodTo: m.PeriodTo,
		VoucherCount: m.VoucherCount, AccountCount: m.AccountCount,
	}, nil
}

// ---------------------------------------------------------------- AI

// AISuggestRequest 是请求 AI 建议的参数。
//
// 金额用**字符串**接收：界面上的输入框给的是「元」，
// 而 Go 契约里是「分」。用 float64 传会在 0.1+0.2 这类值上出错，
// 因此界面传元字符串，由这里解析成精确的分。
type AISuggestRequest struct {
	Task         string `json:"task"`
	Text         string `json:"text"`
	AmountYuan   string `json:"amountYuan"`
	Date         string `json:"date"`
	Counterparty string `json:"counterparty"`
	Direction    string `json:"direction"`
}

// AISuggest 请求 AI 生成记账建议。
func (a *App) AISuggest(req AISuggestRequest) (out *service.AISuggestResult, err error) {
	defer recoverTo(&err, "AISuggest")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	amt, err := ParseYuan(req.AmountYuan)
	if err != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: err.Error()}
	}
	res, err := svc.AISuggest(a.context(), service.AISuggestInput{
		Task: req.Task, Text: req.Text, Amount: amt, Date: req.Date,
		Counterparty: req.Counterparty, Direction: req.Direction,
		TargetType: "freeform",
	})
	if err != nil {
		return nil, classify(err)
	}
	// ★ 护栏不通过不是 Fault：界面要把失败明细摊开展示，
	// 而不是弹一句「AI 失败了」。这两者的界面动作完全不同。
	return res, nil
}

// AIConfig 返回 AI 配置与使用统计。
func (a *App) AIConfig() (out *service.AIConfig, err error) {
	defer recoverTo(&err, "AIConfig")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.AIConfigInfo(a.context()))
}

// SaveAIProvider 保存模型服务配置。
func (a *App) SaveAIProvider(c sqlite.AIProviderConfig) (out int64, err error) {
	defer recoverTo(&err, "SaveAIProvider")()
	svc, f := a.book()
	if f != nil {
		return 0, f
	}
	return wrap(svc.SaveAIProvider(a.context(), &c))
}

// AISuggestions 返回最近的 AI 建议记录（审计）。
func (a *App) AISuggestions(limit int) (out []sqlite.SuggestionRow, err error) {
	defer recoverTo(&err, "AISuggestions")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	rows, serr := svc.AI().Suggestions(a.context(), limit)
	return wrap(nonNil(rows), serr)
}

// AIAcceptRequest 是采纳一条 AI 建议的入参。
type AIAcceptRequest struct {
	ID int64 `json:"id"`
	// CreatedBy 是**确认人**：记账责任落在自然人身上，
	// 模型名只进审计表，不进凭证签章。
	CreatedBy string `json:"createdBy"`
}

// AcceptAISuggestion 把一条通过护栏的建议落成凭证**草稿**。
func (a *App) AcceptAISuggestion(req AIAcceptRequest) (out *service.AIAcceptResult, err error) {
	defer recoverTo(&err, "AcceptAISuggestion")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	by := strings.TrimSpace(req.CreatedBy)
	if by == "" {
		return nil, &Fault{Kind: FaultInvalid,
			Message: "请填写确认人 —— 凭证需要有记账签章"}
	}
	return wrap(svc.AcceptAISuggestion(a.context(), req.ID, by))
}

// RejectAISuggestion 记录「这条建议没用」，附原因。
func (a *App) RejectAISuggestion(req AIAcceptRequest) (err error) {
	defer recoverTo(&err, "RejectAISuggestion")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	return classify(svc.RejectAISuggestion(a.context(), req.ID, req.CreatedBy))
}

// AIPromptConfig 返回提示词设置（含出厂默认全文，供界面展示与恢复默认）。
func (a *App) AIPromptConfig() (out *service.AIPromptView, err error) {
	defer recoverTo(&err, "AIPromptConfig")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.AIPromptConfig(a.context()))
}

// SaveAIPromptConfig 保存提示词设置。
func (a *App) SaveAIPromptConfig(in service.AIPromptInput) (err error) {
	defer recoverTo(&err, "SaveAIPromptConfig")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	return classify(svc.SaveAIPromptConfig(a.context(), in))
}

// ResetAIPromptConfig 把提示词恢复成出厂默认。
func (a *App) ResetAIPromptConfig() (err error) {
	defer recoverTo(&err, "ResetAIPromptConfig")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	return classify(svc.ResetAIPromptConfig(a.context()))
}

// PreviewAccountantPrompt 渲染对话式会计**真实**会用的那份提示词。
//
// 与 PreviewAIPrompt 分开：AI 记账助手页面上用的是会计版，
// 预览必须是同一份 —— 否则用户改完设置点预览，看到的是另一份东西。
func (a *App) PreviewAccountantPrompt() (out *service.AIPromptPreview, err error) {
	defer recoverTo(&err, "PreviewAccountantPrompt")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.PreviewAccountantPrompt(a.context()))
}

// PreviewAIPrompt 渲染当前设置下**真实**会发给模型的提示词。
func (a *App) PreviewAIPrompt(task string) (out *service.AIPromptPreview, err error) {
	defer recoverTo(&err, "PreviewAIPrompt")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.PreviewAIPrompt(a.context(), task))
}

// reportKindLabel 把报表代号转成中文名（日志里记人话）。
func reportKindLabel(kind string) string {
	switch kind {
	case "trial":
		return "科目余额表"
	case "bs":
		return "资产负债表"
	case "pl":
		return "利润表"
	case "cashflow":
		return "现金流量表"
	case "contact":
		return "往来单位余额表"
	}
	return kind
}

// ---------------------------------------------------------------- 工具

// ParseYuan 把界面输入的「元」字符串解析成「分」。
//
// # 为什么金额一律走字符串解析
//
// 界面输入「0.1」时，如果让它变成 JSON number，浏览器里的 0.1
// 在 IEEE754 下并不是 0.1。一旦用它乘 100 再取整，
// 就会出现 9.999999 被截成 9 分的经典事故 —— 而账上少一分钱，
// 试算平衡就不成立。
//
// # 为什么不用 fmt.Sscanf
//
// Sscanf 的 %d 按 **Go 整数字面量**解析，会接受 `0x10`(=16)、
// `0b101`(=5)、`1_000`(=1000)。金额输入框里出现这些只可能是
// 用户误操作或数据损坏，当作合法金额静默接受是最糟的处理方式。
// 这里全部转给 money.Parse —— 它用 strconv.ParseInt(s, 10, 64)，
// 严格十进制，不接受任何前缀与下划线。
func ParseYuan(s string) (money.Money, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, nil
	}

	// 先做「最多两位小数」的前置检查。
	//
	// money.Parse 对超过两位小数是**四舍五入**（导入银行流水时这是对的），
	// 但用户在输入框里敲 1.005 再被静默改成 1.01，等于软件替人改了金额。
	// 这里选择报错让人看见。
	body := strings.NewReplacer(
		",", "", "，", "", "¥", "", "￥", "",
		" ", "", "\u00a0", "", "　", "",
	).Replace(trimmed)
	if i := strings.IndexByte(body, '.'); i >= 0 {
		frac := body[i+1:]
		if strings.IndexByte(frac, '.') >= 0 {
			return 0, fmt.Errorf("金额 %q 含多个小数点", trimmed)
		}
		if len(frac) > 2 {
			return 0, fmt.Errorf("金额 %q 最多两位小数", trimmed)
		}
	}

	m, err := money.Parse(trimmed)
	if err != nil {
		return 0, fmt.Errorf("金额 %q 无法识别", trimmed)
	}
	return m, nil
}

// Today 返回今天的日期，供界面上的日期选择器做默认值。
//
// 走 Go 而不是让浏览器取本地时间：账套里的日期是**纯日历日期**，
// 由一处统一提供可以避免「浏览器时区与记账时区不一致」这类问题。
func (a *App) Today() (out string) {
	// ★ 这两个绑定没有 error 返回值，套不上 recoverTo —— 但**不能因此不兜底**：
	// Wails 绑定的是 *App 的全部导出方法，任何一个 panic 都会把整个应用带崩。
	// 这里的 recover 只是把「崩掉」降级成「返回零值」，够用了。
	defer recoverValue("Today")
	return calendar.Today().String()
}

// AppVersion 返回程序版本，界面在「关于」里显示。
func (a *App) AppVersion() (out string) {
	defer recoverValue("AppVersion")
	return service.AppVersion
}

// ---------------------------------------------------------------------------
// 恢复与账套维护
// ---------------------------------------------------------------------------

// RestoreRequest 是恢复备份的参数。
type RestoreRequest struct {
	// Archive 是 .mabak 文件路径。
	Archive string `json:"archive"`
	// DBPath 是恢复到哪个账套文件；为空时覆盖当前账套。
	DBPath string `json:"dbPath"`
}

// RestoreResult 是恢复结果。
type RestoreResult struct {
	DBPath   string `json:"dbPath"`
	FilesDir string `json:"filesDir"`
	// Restored 是恢复的附件数。
	Restored int `json:"restored"`
	// Stashed 是恢复前被改名保存的旧账套路径（为空表示没有旧账套）。
	//
	// ★ 恢复绝不能直接删掉现有账套：万一恢复的是错的备份，
	// 用户还能从 .bak 文件退回去。删除是不可逆的，
	// 而恢复失败的代价是「用户的账没了」。
	Stashed     string `json:"stashed"`
	CompanyName string `json:"companyName"`
}

// RestoreBackup 从 .mabak 恢复账套。
//
// 恢复前会先把当前账套关掉：SQLite 文件被连接持有的时候
// 替换它会让进程里的连接指向一个已被删除的 inode。
func (a *App) RestoreBackup(req RestoreRequest) (out *RestoreResult, err error) {
	defer recoverTo(&err, "RestoreBackup")()
	if strings.TrimSpace(req.Archive) == "" {
		return nil, &Fault{Kind: FaultInvalid, Message: "请选择要恢复的备份文件"}
	}

	a.mu.Lock()
	cur := a.svc
	curPath := a.path
	a.mu.Unlock()

	// 先看清单：恢复前确认是哪个账套、什么时间备份的
	m, ierr := (&service.Service{}).InspectBackup(req.Archive)
	if ierr != nil {
		return nil, classify(ierr)
	}

	dbPath := strings.TrimSpace(req.DBPath)
	if dbPath == "" {
		if curPath == "" {
			return nil, &Fault{Kind: FaultInvalid,
				Message: "请指定恢复到哪个账套文件"}
		}
		dbPath = curPath
	}
	filesDir := filepath.Join(filepath.Dir(dbPath), ".files")

	// 关掉当前连接再动文件
	if cur != nil {
		_ = cur.Shutdown()
		a.mu.Lock()
		a.svc, a.path = nil, ""
		a.mu.Unlock()
	}

	res, rerr := backup.Restore(a.context(), req.Archive, dbPath, filesDir)
	if rerr != nil {
		// 恢复失败后把原来的账套重新打开，让用户至少还能用
		if curPath != "" {
			if svc, e := service.Open(a.context(), service.Options{Path: curPath}); e == nil {
				a.mu.Lock()
				a.svc, a.path = svc, curPath
				a.mu.Unlock()
			}
		}
		return nil, classify(rerr)
	}

	// 打开恢复后的账套
	svc, oerr := service.Open(a.context(), service.Options{Path: dbPath})
	if oerr != nil {
		return nil, classify(oerr)
	}
	a.mu.Lock()
	a.svc, a.path = svc, dbPath
	a.mu.Unlock()
	rememberBookPath(dbPath)

	out = &RestoreResult{
		DBPath: dbPath, FilesDir: filesDir,
		Restored: res.FilesRestored, CompanyName: m.CompanyName,
		Stashed: res.BackupDir,
	}
	return out, nil
}

// VoucherWithAttachment 报告某期间有多少张凭证带了附件。
//
// 这是「对账」的一部分：《会计基础工作规范》要求记账凭证附有原始单据，
// 而小微企业的现实是「发票在微信里、报销单在抽屉里」。
// 这个数字让会计知道还差多少张没补。
type AttachmentAudit struct {
	Posted     int `json:"posted"`
	WithAttach int `json:"withAttach"`
	Without    int `json:"without"`
	// OrphanFiles 是磁盘上没有被任何单据引用的附件数。
	OrphanFiles int `json:"orphanFiles"`
}

// AuditAttachments 统计凭证的附件覆盖情况。
func (a *App) AuditAttachments(year, month int) (out *AttachmentAudit, err error) {
	defer recoverTo(&err, "AuditAttachments")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	ctx := a.context()
	audit := &AttachmentAudit{}

	var qerr error
	if year > 0 && month > 0 {
		qerr = svc.DB().SQL().QueryRowContext(ctx, `
			SELECT COUNT(*), COALESCE(SUM(attach_count > 0), 0)
			  FROM voucher WHERE status = 'posted' AND year = ? AND month = ?`,
			year, month).Scan(&audit.Posted, &audit.WithAttach)
	} else {
		qerr = svc.DB().SQL().QueryRowContext(ctx, `
			SELECT COUNT(*), COALESCE(SUM(attach_count > 0), 0)
			  FROM voucher WHERE status = 'posted'`).
			Scan(&audit.Posted, &audit.WithAttach)
	}
	if qerr != nil {
		return nil, classify(qerr)
	}
	audit.Without = audit.Posted - audit.WithAttach

	orphans, oerr := svc.OrphanAttachments(ctx)
	if oerr == nil {
		audit.OrphanFiles = len(orphans)
	}
	return audit, nil
}
