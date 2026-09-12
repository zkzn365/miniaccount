package service

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"miniaccount/internal/domain/audit"
)

// ---------------------------------------------------------------------------
// 日志设置
// ---------------------------------------------------------------------------
//
// # 为什么「查看操作」要单独一个开关，而且默认关
//
// 规范要的是**业务层面**的操作（录入、修改、结账…），一天几十条。
// 而「谁在什么时候看了哪张报表」是另一类需求 —— 它有用（排查
// 「这个数字是谁先看到的」「报表什么时候被人翻过」），但体量差好几个
// 数量级：一次月结前后，会计能把同一张资产负债表刷新几十遍。
//
// 全量记下来会把真正要审计的那部分淹掉，所以：默认只记业务操作，
// 需要时打开开关，两类日志在查询与导出时可以分开。
//
// 设置存在 ~/.mini-account/settings.json，与日志目录同级 ——
// 边用命令行边用界面时，两边看到的是同一个开关。

// LogSettings 是日志相关的设置。
type LogSettings struct {
	// RecordViews 为真时记录查看/查询类操作。
	RecordViews bool `json:"recordViews"`
	// ViewIntervalSeconds 是同一对象重复查看的去重窗口（秒）。
	//
	// ★ 没有它的话，界面上一次刷新、一次翻页、一次按下 F5
	// 都会写一条 —— 日志会以肉眼可见的速度膨胀，而信息量为零。
	// 窗口内对同一个「操作 + 对象」只记第一次。
	ViewIntervalSeconds int `json:"viewIntervalSeconds"`
}

// DefaultLogSettings 是出厂设置：只记业务操作。
func DefaultLogSettings() LogSettings {
	return LogSettings{RecordViews: false, ViewIntervalSeconds: 60}
}

// settingsPath 返回设置文件路径（~/.mini-account/settings.json）。
func settingsPath() (string, error) {
	dir, err := DefaultAuditDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(dir), "settings.json"), nil
}

var (
	logSettingsMu  sync.RWMutex
	logSettingsVal *LogSettings
)

// LogSettingsOf 返回当前日志设置。
func LogSettingsOf() LogSettings {
	logSettingsMu.RLock()
	if logSettingsVal != nil {
		v := *logSettingsVal
		logSettingsMu.RUnlock()
		return v
	}
	logSettingsMu.RUnlock()

	if _, err := settingsPath(); err != nil {
		return DefaultLogSettings()
	}
	s := loadSettingsFile().Audit
	if s == (LogSettings{}) {
		s = DefaultLogSettings()
	}
	if s.ViewIntervalSeconds <= 0 {
		s.ViewIntervalSeconds = DefaultLogSettings().ViewIntervalSeconds
	}
	logSettingsMu.Lock()
	logSettingsVal = &s
	logSettingsMu.Unlock()
	return s
}

// SaveLogSettings 保存日志设置。
func SaveLogSettings(s LogSettings) error {
	if s.ViewIntervalSeconds <= 0 {
		s.ViewIntervalSeconds = DefaultLogSettings().ViewIntervalSeconds
	}
	if s.ViewIntervalSeconds > 3600 {
		s.ViewIntervalSeconds = 3600
	}
	f := loadSettingsFile()
	f.Audit = s
	if err := saveSettingsFile(f); err != nil {
		return err
	}
	logSettingsMu.Lock()
	logSettingsVal = &s
	logSettingsMu.Unlock()
	return nil
}

// ---------------------------------------------------------------------------
// 查看类日志
// ---------------------------------------------------------------------------

var (
	viewMu   sync.Mutex
	viewSeen = map[string]time.Time{}
)

// recordView 记一条「查看」日志。
//
// 调用点很多（每个查询入口一处），所以这里把三件事一起做了：
// 开关判断、去重、写盘。业务代码只管调它。
func (s *Service) recordView(ctx context.Context, ev AuditEvent) {
	set := LogSettingsOf()
	if !set.RecordViews {
		return
	}
	ev.Category = audit.CategoryRead
	ev.Result = audit.ResultOK
	if ev.Source == "" {
		ev.Source = audit.SourceGUI
	}
	// 同一「操作 + 对象 + 操作人」在窗口内只记一次
	key := string(ev.Action) + "|" + ev.EntityID + "|" + ev.Operator
	if !markView(key, time.Duration(set.ViewIntervalSeconds)*time.Second) {
		return
	}
	s.recordAudit(ctx, ev)
}

// markView 判断这次查看要不要记（第一次为真，窗口内重复为假）。
func markView(key string, window time.Duration) bool {
	now := time.Now()
	viewMu.Lock()
	defer viewMu.Unlock()
	if last, ok := viewSeen[key]; ok && now.Sub(last) < window {
		return false
	}
	// 顺手清掉过期项：不然界面开着一天，这个 map 会一直长
	if len(viewSeen) > 2048 {
		for k, t := range viewSeen {
			if now.Sub(t) >= window {
				delete(viewSeen, k)
			}
		}
	}
	viewSeen[key] = now
	return true
}

// ResetViewDedup 清空查看去重窗口（测试用）。
func ResetViewDedup() {
	viewMu.Lock()
	viewSeen = map[string]time.Time{}
	viewMu.Unlock()
}

// trimTo 把过长的字符串截断（查看日志里不记录完整参数，避免把
// 报表请求的明细塞进日志）。
func trimTo(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
