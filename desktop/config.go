package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

// 这里用 Go 的 `date` 类型做别名，避免与 time.Time 混淆。
//
// 账套里的日期一律是**纯日历日期**（YYYY-MM-DD，无时区），
// 与 Frappe Books 把会计日期存成「本地日期对应的 UTC 午夜」不同 ——
// 后者会让日期筛选出现 off-by-one。
type date = calendar.Date

func parseDate(s string) (date, error) { return calendar.Parse(s) }
func todayDate() date                  { return calendar.Today() }

func periodEnd(k period.Key) date {
	d, err := calendar.New(k.Year, k.Month, calendar.DaysInMonth(k.Year, k.Month))
	if err != nil {
		return calendar.Today()
	}
	return d
}

// moneyStr 把「分」格式化成可读金额，仅用于错误提示。
func moneyStr(cents int64) string { return money.Money(cents).String() }

// ---------------------------------------------------------------------------
// 最近打开的账套
// ---------------------------------------------------------------------------

// appConfig 是桌面端自己的配置，与账套数据分开存放。
//
// ★ 绝不能塞进账套数据库：账套是会计档案，会被备份、会被审计；
// 「上次打开的是哪个文件」是**这台机器上这个用户**的偏好，
// 把它写进账套会让同一份账在不同电脑上互相覆盖。
type appConfig struct {
	LastBookPath string `json:"lastBookPath"`
}

// configPath 返回配置文件路径。
//
// 用 os.UserConfigDir 而不是写当前目录：桌面应用的工作目录
// 由启动方式决定（双击 / 命令行 / 快捷方式各不相同），
// 写相对路径会导致「换个方式启动就丢了配置」。
func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "miniaccount", "settings.json"), nil
}

func loadConfig() appConfig {
	var c appConfig
	p, err := configPath()
	if err != nil {
		return c
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return c
	}
	_ = json.Unmarshal(b, &c)
	return c
}

func saveConfig(c appConfig) {
	p, err := configPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	// 配置写失败不影响主流程：它只影响「下次启动的默认路径」。
	//
	// 但要**原子写**：直接 WriteFile 在掉电/进程被杀时会留下半截
	// settings.json，下次启动 json.Unmarshal 失败，「最近打开的账套」
	// 就丢了 —— 用户每次开机都要重新选账套，而且不知道为什么。
	// 先写临时文件再 rename，让读到的要么是旧的完整配置、要么是新的。
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
	}
}

// lastBookPath 返回上次打开的账套路径（配置不可读时为空）。
func lastBookPath() string { return loadConfig().LastBookPath }

// rememberBookPath 记住本次打开的账套。
func rememberBookPath(path string) {
	if path == "" {
		return
	}
	c := loadConfig()
	if c.LastBookPath == path {
		return
	}
	c.LastBookPath = path
	saveConfig(c)
}
