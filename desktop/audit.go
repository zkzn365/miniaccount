package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"miniaccount/internal/domain/audit"
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 操作日志
// ---------------------------------------------------------------------------
//
// 日志存在**独立的**数据库里，放在账套目录的兄弟目录下：
//
//	~/.mini-account/dataDB/   账套
//	~/.mini-account/logs/     操作日志（按 100MB 分片）
//
// 这样「把账套恢复到昨天的备份」不会把日志一起倒退 ——
// 恢复这件事本身会成为日志里醒目的一条。

// auditDir 返回日志目录。
//
// 位置由 service.DefaultAuditDir 决定（与命令行共用一份逻辑）——
// 两边各写一遍的话，命令行写的日志与界面查的日志会不在一个地方，
// 而用户只会觉得「日志是空的」。
func auditDir() (string, error) { return service.DefaultAuditDir() }

// setupAudit 在进程启动时把日志目录告诉服务层。
//
// 失败不致命：日志写不了不该让用户打不开软件。但要在 stderr 说清楚，
// 因为「审计时发现没有日志」比「启动时报个错」严重得多。
func setupAudit() {
	dir, err := auditDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[audit] 定位日志目录失败，操作日志将不可用：%v\n", err)
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "[audit] 建不了日志目录 %s：%v\n", dir, err)
		return
	}
	service.SetAuditDir(dir)
	// 目录权限在存储打开时统一收紧（见 auditdb.tighten），
	// 这里只把失败原因讲清楚：日志里有操作人、金额、对方户名，
	// 「以为收紧了其实没有」比不收更危险。
	service.WarnIfAuditNotPrivate()
}

// migrateLegacyBookkeeper 把老版本留存在本机配置里的记账人并进当前账套。
//
// ★ 没有打开账套时**什么都不做** —— 不能顺手把本机那个老值删掉：
// 删了就再也搬不进账套，用户打过的名字就真丢了。
// 所以这个方法既在启动时调（那时通常没账套，等于空转），
// 也在每次打开账套之后调 —— 等有了账套再搬。
func (a *App) migrateLegacyBookkeeper() {
	defer func() { _ = recover() }()
	svc, f := a.book()
	if f != nil {
		return
	}
	svc.MigrateLegacyBookkeeper(a.context())
}

// ---------------------------------------------------------------------------
// AI Agent 批量记账
// ---------------------------------------------------------------------------

// AIAgentSources 返回可选的待记账来源（界面下拉用）。
func (a *App) AIAgentSources() (out []ServiceSourceOption, err error) {
	defer recoverTo(&err, "AIAgentSources")()
	list := service.AllAgentSources()
	out = make([]ServiceSourceOption, 0, len(list))
	for _, s := range list {
		out = append(out, ServiceSourceOption{Value: string(s), Label: s.Label()})
	}
	return out, nil
}

// ServiceSourceOption 是一个下拉选项。
type ServiceSourceOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// StartAIAgentRun 起一轮批量记账（后台跑，界面轮询进度）。
func (a *App) StartAIAgentRun(req service.AIAgentRequest) (out *service.AIAgentRun, err error) {
	defer recoverTo(&err, "StartAIAgentRun")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.StartAIAgentRun(a.context(), req))
}

// AIAgentRunStatus 查询进度。
func (a *App) AIAgentRunStatus(id string) (out *service.AIAgentRun, err error) {
	defer recoverTo(&err, "AIAgentRunStatus")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.AIAgentRunStatus(id))
}

// LatestAIAgentRun 返回最近一次运行（界面重开时显示上次结果）。
func (a *App) LatestAIAgentRun() (out *service.AIAgentRun, err error) {
	defer recoverTo(&err, "LatestAIAgentRun")()
	svc, f := a.book()
	if f != nil {
		return nil, nil // 没账套时不算错
	}
	return svc.LatestAIAgentRun(), nil
}

// CancelAIAgentRun 停止一轮运行。
func (a *App) CancelAIAgentRun(id string) (err error) {
	defer recoverTo(&err, "CancelAIAgentRun")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	return classify(svc.CancelAIAgentRun(id))
}

// AIAgentItemRequest 是对批量结果里某一条的处置。
type AIAgentItemRequest struct {
	RunID string `json:"runId"`
	Index int    `json:"index"`
	// CreatedBy 是确认人（采纳时必填）。
	CreatedBy string `json:"createdBy"`
	// Reason 是拒绝原因。
	Reason string `json:"reason"`
}

// AcceptAIAgentItem 采纳批量结果里的一条（落成草稿凭证）。
func (a *App) AcceptAIAgentItem(req AIAgentItemRequest) (out *service.AIAcceptResult, err error) {
	defer recoverTo(&err, "AcceptAIAgentItem")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	by := strings.TrimSpace(req.CreatedBy)
	if by == "" {
		return nil, &Fault{Kind: FaultInvalid,
			Message: "请填写确认人 —— 凭证需要有记账签章"}
	}
	return wrap(svc.AcceptAIAgentItem(a.context(), req.RunID, req.Index, by))
}

// RejectAIAgentItem 拒绝批量结果里的一条。
func (a *App) RejectAIAgentItem(req AIAgentItemRequest) (err error) {
	defer recoverTo(&err, "RejectAIAgentItem")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	return classify(svc.RejectAIAgentItem(a.context(), req.RunID, req.Index, req.Reason))
}

// Bookkeeper 返回**当前账套**的记账人。
//
// ★ 它是账套级的：同一台电脑给两家公司做账，可以签不同的名；
// 它随账套备份一起走。没打开账套时返回空串（不是错误）——
// 欢迎页上问这个不成立，也不该报错。
func (a *App) Bookkeeper() (out string, err error) {
	defer recoverTo(&err, "Bookkeeper")()
	svc, f := a.book()
	if f != nil {
		return "", nil
	}
	return wrap(svc.Bookkeeper(a.context()))
}

// SaveBookkeeper 设置当前账套的记账人（空串表示不预设）。
//
// 它只是「默认填谁」，不是「替谁签字」：界面上看得见，也能当场改，
// 每一次落章都会进操作日志。
func (a *App) SaveBookkeeper(name string) (err error) {
	defer recoverTo(&err, "SaveBookkeeper")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	return classify(svc.SetBookkeeper(a.context(), name))
}

// AuditSettings 返回日志设置（含「是否记录查看操作」）。
func (a *App) AuditSettings() (out service.LogSettings, err error) {
	defer recoverTo(&err, "AuditSettings")()
	return service.LogSettingsOf(), nil
}

// SaveAuditSettings 保存日志设置。
func (a *App) SaveAuditSettings(s service.LogSettings) (err error) {
	defer recoverTo(&err, "SaveAuditSettings")()
	return classify(service.SaveLogSettings(s))
}

// AuditQueryRequest 是日志查询的入参（界面直接传上来）。
type AuditQueryRequest = service.AuditQuery

// AuditLogPage 是日志查询结果。
type AuditLogPage struct {
	Page *audit.Page `json:"page"`
	// Dir 是日志目录，界面要显示（用户得知道日志在哪、有多大）。
	Dir string `json:"dir"`
	// Actions 是筛选下拉的选项。
	Actions []service.AuditActionOption `json:"actions"`
	// Categories 是类别筛选的选项。
	Categories []audit.Category `json:"categories"`
}

// AuditLog 查询操作日志。
func (a *App) AuditLog(q AuditQueryRequest) (out AuditLogPage, err error) {
	defer recoverTo(&err, "AuditLog")()
	svc := &service.Service{}
	page, qerr := svc.QueryAudit(a.context(), q)
	if qerr != nil {
		return AuditLogPage{}, classify(qerr)
	}
	dir, _ := auditDir()
	// 查日志本身也留痕（属于「查看」类，开关开着才记）。
	// 监督人员翻过日志这件事，本身也是审计信息。
	svc.RecordView(a.context(), service.AuditEvent{
		Action: audit.ActionViewAuditLog, Entity: "audit_log", EntityID: "query",
		Summary: "查看操作日志",
		Detail:  map[string]any{"操作人": q.Operator, "起": q.From, "止": q.To, "关键字": q.Text},
	})
	return AuditLogPage{
		Page: page, Dir: dir, Actions: svc.AuditOptions(),
		Categories: audit.AllCategories(),
	}, nil
}

// VerifyAuditLog 校验日志完整性（哈希链）。
//
// 这是规范里「安全性」那一半的验证手段：日志不能被改删，
// 而「有没有被改过」由链来证明。界面上给一个按钮，
// 会计监督人员随时可以自己验一次。
func (a *App) VerifyAuditLog() (out *audit.VerifyResult, err error) {
	defer recoverTo(&err, "VerifyAuditLog")()
	svc := &service.Service{}
	return wrap(svc.VerifyAudit(a.context()))
}

// ExportAuditLog 把日志导出成 CSV（带 BOM，Excel 直接打开）。
func (a *App) ExportAuditLog(q AuditQueryRequest, dest string) (out int, err error) {
	defer recoverTo(&err, "ExportAuditLog")()
	if strings.TrimSpace(dest) == "" {
		return 0, &Fault{Kind: FaultInvalid, Message: "请选择导出位置"}
	}
	svc := &service.Service{}
	return wrap(svc.ExportAudit(ctxOf(a), q, dest))
}

func ctxOf(a *App) context.Context { return a.context() }

// ---------------------------------------------------------------------------
// 失败的操作也要留痕
// ---------------------------------------------------------------------------

// auditFailure 记录一次失败的绑定调用。
//
// ★ 为什么要记失败：用户报「点了没反应」「报了个错」时，
// 日志里如果只有成功的操作，就什么都查不到。而失败恰恰是最需要线索的地方。
//
// 只记**失败**，不记成功的读操作：读操作量大且没有业务含义，
// 全记下来会把真正有用的业务日志淹掉（规范要的是「业务层面的操作」）。
// 成功的业务写操作由 service 层各自记录（带修改前后内容）。
func auditFailure(op string, fault *Fault) {
	if fault == nil {
		return
	}
	// 这个方法在 defer 里跑：它自己 panic 会把栈展开变成崩溃。
	defer func() { _ = recover() }()
	svc := &service.Service{}
	svc.RecordFailure(context.Background(), service.AuditEvent{
		Action:   audit.ActionOperationFailed,
		Category: audit.CategorySystem,
		Summary:  "操作失败：" + op,
		Entity:   "binding", EntityID: op,
		Source:  audit.SourceGUI,
		Result:  audit.ResultFailed,
		Message: fault.Message,
		Detail: map[string]any{
			"失败类别": string(fault.Kind),
			"方法":   op,
		},
	})
}

// logStartup 记一条「程序启动」。
//
// 它把每次启动钉在时间轴上：排查问题时先看「那天几点开的程序」，
// 再看之后发生了什么 —— 没有这条，日志是一堆悬空的操作。
func (a *App) logStartup() {
	defer func() { _ = recover() }()
	svc := &service.Service{}
	svc.RecordFailure(context.Background(), service.AuditEvent{
		Action:   audit.ActionBookOpen,
		Category: audit.CategorySystem,
		Summary:  fmt.Sprintf("启动程序（%s）", service.AppVersion),
		Source:   audit.SourceGUI,
		Result:   audit.ResultOK,
		Detail:   map[string]any{"时间": time.Now().Format(time.RFC3339)},
	})
}
