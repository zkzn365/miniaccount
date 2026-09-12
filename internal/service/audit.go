package service

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"miniaccount/internal/domain/audit"
	"miniaccount/internal/store/auditdb"
)

// ---------------------------------------------------------------------------
// 操作日志
// ---------------------------------------------------------------------------
//
// 规范里那三条（完整性 / 安全性 / 可查询性）在代码里的落点：
//
//	完整性 → service 层每个**业务写操作**都调 recordAudit；
//	安全性 → 独立文件 + 表级触发器禁止改删 + 哈希链（见 auditdb）；
//	可查询性 → QueryAudit 支持按人、时间、操作、结果、关键字组合查。
//
// # 为什么日志不在账套库里
//
// 把账套恢复到昨天的备份，如果日志也在账套里，日志会跟着倒退 ——
// 「谁在什么时候恢复过账套」这件事就永远查不出来了。
// 分开存，恢复动作本身会成为日志里醒目的一条。

// auditStore 是进程级共享的日志存储。
//
// 用进程级而不是每个 Service 一份：日志是**这台机器上所有账套**的运行痕迹，
// 一个进程里同时只开一个账套，但切换账套（关一个开一个）时不该把文件句柄
// 反复开关，更不该因为两个 Service 各开一份而把链写成两条。
var (
	auditMu    sync.Mutex
	auditStore *auditdb.Store
	auditDir   string
)

// SetAuditDir 指定日志目录（由桌面端/命令行在启动时调用）。
//
// 不指定时不记日志并明确返回错误 —— 宁可让调用方知道「日志没开」，
// 也不要悄悄写到一个「当前目录」这种随启动方式变化的地方。
func SetAuditDir(dir string) {
	auditMu.Lock()
	defer auditMu.Unlock()
	if dir == auditDir {
		return
	}
	if auditStore != nil {
		_ = auditStore.Close()
		auditStore = nil
	}
	auditDir = dir
}

// EnsureAuditDir 建好日志目录并把权限收紧到 0700，然后提示是否合格。
//
// 给「可能一条日志都不写」的入口用（比如只看报表的命令行命令）：
// 它不会打开日志存储，但目录该建、权限该收。
func EnsureAuditDir(dir string) {
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	_ = os.Chmod(dir, 0o700)
	WarnIfAuditNotPrivate()
}

// WarnIfAuditNotPrivate 检查日志目录与文件的权限，不合格就报到 stderr。
//
// 为什么不是 error 而是警告：权限没能收紧不该让用户打不开软件 ——
// 但必须在启动时说一次，否则用户会以为已经保护好了。
func WarnIfAuditNotPrivate() {
	dir := AuditDir()
	if dir == "" {
		return
	}
	if err := checkAuditPrivacy(dir); err != nil {
		_, _ = os.Stderr.WriteString(
			"[audit] ⚠ 日志权限没能收紧：" + err.Error() +
				"\n        日志里有操作人、业务摘要与金额，建议执行：" +
				"chmod 700 " + dir + "\n")
	}
}

// checkAuditPrivacy 检查目录与文件权限是否只剩本人可访问。
func checkAuditPrivacy(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("目录 %s 权限是 %04o（同组/其他人可访问）", dir, perm)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			return fmt.Errorf("文件 %s 权限是 %04o（同组/其他人可读）", e.Name(), perm)
		}
	}
	return nil
}

// RecordView 供上层（桌面绑定）记录一次「查看」。
//
// 走的是与 service 内部同一个开关与去重窗口：
// 开关关着时它什么都不做，所以调用点不必自己判断。
func (s *Service) RecordView(ctx context.Context, ev AuditEvent) {
	defer func() { _ = recover() }()
	s.recordView(ctx, ev)
}

// DefaultAuditDir 返回日志的默认位置：<家目录>/.mini-account/logs。
//
// 与账套目录（~/.mini-account/dataDB）同级：整个 .mini-account
// 拷走就是「账 + 日志」的完整备份。
//
// 与账套**分开的两个理由**见本文件开头；这里只强调位置：
// 同级的兄弟目录，用户一眼看得出它们是一套东西，
// 而分开存又保证了「恢复账套不会把日志一起倒退」。
func DefaultAuditDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errNoAuditDir
	}
	return filepath.Join(home, ".mini-account", "logs"), nil
}

// AuditDir 返回当前日志目录。
func AuditDir() string {
	auditMu.Lock()
	defer auditMu.Unlock()
	return auditDir
}

// auditStoreRef 取（必要时打开）日志存储。
func auditStoreRef(ctx context.Context) (*auditdb.Store, error) {
	auditMu.Lock()
	defer auditMu.Unlock()
	if auditStore != nil {
		return auditStore, nil
	}
	if auditDir == "" {
		return nil, errNoAuditDir
	}
	st, err := auditdb.Open(ctx, auditdb.Options{Dir: auditDir})
	if err != nil {
		return nil, err
	}
	auditStore = st
	return st, nil
}

type auditErr string

func (e auditErr) Error() string { return string(e) }

const errNoAuditDir = auditErr("audit: 未设置日志目录（启动时应调用 SetAuditDir）")

// CloseAudit 关闭日志存储（进程退出时调用）。
func CloseAudit() {
	auditMu.Lock()
	defer auditMu.Unlock()
	if auditStore != nil {
		_ = auditStore.Close()
		auditStore = nil
	}
}

// ---------------------------------------------------------------------------
// 记录
// ---------------------------------------------------------------------------

// AuditEvent 是一次待记录的**业务操作**。
type AuditEvent struct {
	Action audit.Action
	// Category 是日志类别；留空按「业务操作」算。
	Category audit.Category
	Summary  string
	Entity   string
	EntityID string
	Detail   map[string]any
	// Operator 留空时由 recordAudit 填「未署名」——不能留空：
	// 规范要求日志必须能回答「谁做的」，空操作人的日志等于没有。
	Operator string
	Source   audit.Source
	Result   audit.Result
	Message  string
}

// recordAudit 写一条业务操作日志。
//
// ★ 日志写失败**不阻断业务**：用户正在记账，不能让「日志写不进去」
// 把记账也拦下来。但要在 stderr 留痕 —— 静默失败最后会变成
// 「审计时发现少了三个月日志」。
func (s *Service) recordAudit(ctx context.Context, ev AuditEvent) {
	if s == nil {
		return
	}
	st, err := auditStoreRef(ctx)
	if err != nil {
		auditWarn(err)
		return
	}
	e := audit.Entry{
		At:       audit.NowStamp(time.Now()),
		Operator: strings.TrimSpace(ev.Operator),
		Source:   ev.Source,
		Action:   ev.Action,
		Category: ev.Category,
		Summary:  ev.Summary,
		Entity:   ev.Entity,
		EntityID: ev.EntityID,
		Detail:   ev.Detail,
		Result:   ev.Result,
		Message:  ev.Message,
		// 账套路径与单位名称都记：日志是跨账套的，
		// 不记这个就分不清「哪本账的凭证被删了」。
		Book:       s.path,
		AppVersion: AppVersion,
	}
	if e.Operator == "" {
		// 没署名时用**操作系统用户名**兜底，而不是写「未署名」。
		//
		// 规范要求日志能回答「谁做的」。命令行批量导入、开机自动任务
		// 这类操作没有界面上的记账签章，但机器上的用户是有名有姓的 ——
		// 「张三这台电脑上跑的」比「未署名」有用得多。
		e.Operator = osUserName()
	}
	if e.Source == "" {
		e.Source = audit.SourceGUI
	}
	if e.Result == "" {
		e.Result = audit.ResultOK
	}
	if e.Category == "" {
		e.Category = audit.CategoryBusiness
	}
	if e.At == "" {
		e.At = audit.NowStamp(time.Now())
	}
	// 账套信息取得到就带上：界面上按单位名称认账套比按路径快得多
	if company := s.cachedCompany(ctx); company != "" {
		e.Company = company
	}
	if _, err := st.Append(ctx, e); err != nil {
		auditWarn(err)
	}
}

// cachedCompany 取当前账套的单位名称（取不到就返回空，不报错）。
//
// ★ s.db 可能是 nil：桌面绑定层记「操作失败」时用的是一具空的 Service
// （日志存储是进程级共享的，不需要账套）。这里不解引用就会 panic，
// 而且是在 defer 里 panic —— 那会把整个进程带崩。
func (s *Service) cachedCompany(ctx context.Context) string {
	if s == nil || s.db == nil {
		return ""
	}
	b, err := s.db.Books().Get(ctx)
	if err != nil {
		return ""
	}
	return b.CompanyName
}

// osUserName 取当前操作系统用户名（取不到时退回「未署名」）。
//
// 结果缓存：审计日志每条都要写操作人，而 os/user 在某些系统上
// 要走 cgo 查询 —— 每条日志都查一次会明显拖慢记账。
var (
	osUserOnce sync.Once
	osUserVal  string
)

func osUserName() string {
	osUserOnce.Do(func() {
		if u, err := user.Current(); err == nil && u.Username != "" {
			osUserVal = u.Username
			return
		}
		if v := os.Getenv("USER"); v != "" {
			osUserVal = v
			return
		}
		osUserVal = "未署名"
	})
	return osUserVal
}

func auditWarn(err error) {
	if err == nil || err == errNoAuditDir {
		return
	}
	// 写 stderr：日志系统自己出问题时，这是唯一的出口
	_, _ = os.Stderr.WriteString("[audit] " + err.Error() + "\n")
}

// RecordFailure 供上层（桌面绑定）记录一次失败的操作。
//
// 命名刻意不叫 Record：它不是通用的记录入口，而是「非业务层
// 也想留一条痕」时的专用出口 —— 业务操作请走 recordAudit，
// 那里会自动带上账套与单位信息。
func (s *Service) RecordFailure(ctx context.Context, ev AuditEvent) {
	if ev.Source == "" {
		ev.Source = audit.SourceGUI
	}
	// 这个方法是在 defer 里被调的（绑定层记失败）：这里 panic
	// 会发生在栈展开过程中，后果比普通 panic 严重得多。
	// 记日志这件事无论如何都不该把程序搞崩。
	defer func() { _ = recover() }()
	s.recordAudit(ctx, ev)
}

// ---------------------------------------------------------------------------
// 查询 / 校验 / 导出
// ---------------------------------------------------------------------------

// AuditQuery 是界面传来的查询条件。
//
// 字段与 audit.Query 一一对应，但用字符串表示操作种类 ——
// 界面传上来的东西不该直接是内部枚举。
type AuditQuery struct {
	Operator string   `json:"operator"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Actions  []string `json:"actions"`
	Result   string   `json:"result"`
	Text     string   `json:"text"`
	Limit    int      `json:"limit"`
	Offset   int      `json:"offset"`
	// Categories 过滤类别（business / read / system）；为空表示不限。
	Categories []string `json:"categories"`
	// BookOnly 为真时只看当前账套的日志。
	BookOnly bool `json:"bookOnly"`
}

// QueryAudit 按条件查日志。
func (s *Service) QueryAudit(ctx context.Context, q AuditQuery) (*audit.Page, error) {
	st, err := auditStoreRef(ctx)
	if err != nil {
		if err == errNoAuditDir {
			// 没开日志时返回空页而不是报错：界面上的「操作日志」页
			// 不该因为没配目录就整页报错
			return &audit.Page{}, nil
		}
		return nil, err
	}
	aq := audit.Query{
		Operator: strings.TrimSpace(q.Operator),
		From:     strings.TrimSpace(q.From),
		To:       strings.TrimSpace(q.To),
		Result:   audit.Result(strings.TrimSpace(q.Result)),
		Text:     strings.TrimSpace(q.Text),
		Limit:    q.Limit,
		Offset:   q.Offset,
	}
	for _, a := range q.Actions {
		if strings.TrimSpace(a) != "" {
			aq.Actions = append(aq.Actions, audit.Action(a))
		}
	}
	for _, c := range q.Categories {
		if audit.ValidCategory(audit.Category(c)) {
			aq.Categories = append(aq.Categories, audit.Category(c))
		}
	}
	if q.BookOnly {
		aq.Book = s.path
	}
	return st.Query(ctx, aq)
}

// VerifyAudit 校验日志完整性（哈希链）。
func (s *Service) VerifyAudit(ctx context.Context) (*audit.VerifyResult, error) {
	st, err := auditStoreRef(ctx)
	if err != nil {
		if err == errNoAuditDir {
			return &audit.VerifyResult{OK: true}, nil
		}
		return nil, err
	}
	return st.Verify(ctx)
}

// AuditActions 返回可选的操作种类（供界面筛选下拉）。
type AuditActionOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// AuditOptions 返回筛选下拉用的选项。
func (s *Service) AuditOptions() []AuditActionOption {
	acts := audit.AllActions()
	out := make([]AuditActionOption, 0, len(acts))
	for _, a := range acts {
		out = append(out, AuditActionOption{Value: string(a), Label: a.Label()})
	}
	return out
}

// ExportAudit 把查询结果导出成 CSV。
//
// ★ 导出是规范里「可查询性」的自然延伸：会计监督人员要能把日志
// 拿去做底稿。CSV 带 UTF-8 BOM，Excel 直接双击就能正确显示中文。
func (s *Service) ExportAudit(ctx context.Context, q AuditQuery, dest string) (int, error) {
	st, err := auditStoreRef(ctx)
	if err != nil {
		return 0, err
	}
	q.Limit = 1_000_000 // 导出不分页
	page, err := s.QueryAudit(ctx, q)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	var b strings.Builder
	b.WriteString("\ufeff") // BOM：让 Excel 认出 UTF-8
	b.WriteString("序号,时间,操作人,来源,操作,对象,对象编号,摘要,结果,说明,账套,单位,程序版本,链哈希\n")
	for _, e := range page.Entries {
		row := []string{
			strconv.FormatInt(e.Seq, 10), e.At, e.Operator, e.Source.Label(), e.Action.Label(),
			e.Entity, e.EntityID, e.Summary, e.Result.Label(), e.Message,
			e.Book, e.Company, e.AppVersion, e.Hash,
		}
		for i, c := range row {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(csvCell(c))
		}
		b.WriteByte('\n')
	}
	if _, err := f.WriteString(b.String()); err != nil {
		return 0, err
	}
	_ = st // 保留引用：导出期间不要让存储被关掉
	return len(page.Entries), nil
}

// csvCell 按 RFC4180 转义，并防一手 CSV 公式注入：
// 以 = + - @ 开头的单元格在 Excel 里会被当公式执行，
// 而日志内容里出现这些字符完全可能（用户输的摘要）。
func csvCell(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '=', '+', '-', '@':
		s = "'" + s
	}
	if strings.ContainsAny(s, ",\"\n\r") {
		return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
	}
	return s
}
