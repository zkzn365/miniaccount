package service

import (
	"context"
	"encoding/json"
	"fmt"
	"miniaccount/internal/domain/audit"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// 记账人
// ---------------------------------------------------------------------------
//
// # 为什么要有「记账人」这个设置
//
// 中国实务里，一张记账凭证上要有**制单、审核、记账**三个签章；
// 本软件把它们都落在自然人身上（见 voucher 的 CreatedBy / PostedBy）。
//
// 但小微企业往往就是一个人在做账，于是每录一张凭证都要重打一遍自己的名字 ——
// 界面上一共七个地方要填「操作人」，每个地方各记各的（还各存一份在
// WebView 的 localStorage 里），既啰嗦又容易前后不一致：
// 同一个人在凭证上叫「李会计」、在工资表上叫「小李」，
// 事后按操作人查日志就查不全。
//
// 所以设一个**本机的记账人**：录凭证、过账、结账、生成工资表时
// 自动带上这个人，需要时还能当场改。
//
// # 它是不是「匿名签章」
//
// 不是，而且必须防住这一点：
//
//   - 名字会**显示在界面上**（凭证编辑器的制单人一栏看得见），
//     不是藏在背后偷偷签的；
//   - 每一次落章都会进操作日志（谁、什么时候、做了什么），
//     对不上时能查出来；
//   - 空着就是不填 —— 程序**绝不**用「系统」「管理员」之类的名字顶上去。
//
// 换句话说：省的是打字，不是责任。
//
// # 存在哪里：**账套里**
//
// 存进账套的 setting 表，而不是本机配置。理由：
//
//   - 同一台电脑给两家公司做账时，签的名往往不是同一个人
//     （代账会计尤其如此）—— 记账人是**这本账**的签章人；
//   - 它随账套备份一起走：换台电脑恢复备份，签章人还在；
//   - 命令行的 `--by` 不传时也从账套里取，界面与命令行永远一致。
//
// 日志设置（记不记查看操作）仍然放在本机 —— 那管的是这台机器上的日志文件，
// 与具体哪本账无关。
const maxBookkeeperLen = 20

// 记账人缓存：按账套路径缓存，切换账套时自动失效。
var (
	bookkeeperMu  sync.RWMutex
	bookkeeperVal *string
	bookkeeperFor string // 缓存对应的账套路径
)

// Bookkeeper 返回**当前账套**的记账人（没设过、或没打开账套时为空串）。
func (s *Service) Bookkeeper(ctx context.Context) (string, error) {
	if s == nil || s.db == nil {
		return "", nil
	}
	bookkeeperMu.RLock()
	if bookkeeperVal != nil && bookkeeperFor == s.path {
		v := *bookkeeperVal
		bookkeeperMu.RUnlock()
		return v, nil
	}
	bookkeeperMu.RUnlock()

	name, err := s.db.Books().Bookkeeper(ctx)
	if err != nil {
		return "", err
	}
	bookkeeperMu.Lock()
	bookkeeperVal, bookkeeperFor = &name, s.path
	bookkeeperMu.Unlock()
	return name, nil
}

// SetBookkeeper 设置**当前账套**的记账人。
//
// 传空串表示「不预设」—— 界面上仍然要求当场填写，
// 而不是留一个空签章过去。
func (s *Service) SetBookkeeper(ctx context.Context, name string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("还没有打开账套，无法设置记账人")
	}
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxBookkeeperLen {
		return fmt.Errorf("记账人姓名太长（%d 个字，上限 %d）",
			utf8.RuneCountInString(name), maxBookkeeperLen)
	}
	if strings.ContainsAny(name, "\n\r\t") {
		return fmt.Errorf("记账人姓名不能包含换行或制表符")
	}
	before, _ := s.Bookkeeper(ctx)
	if before == name {
		return nil
	}
	if err := s.db.Books().SetBookkeeper(ctx, name); err != nil {
		return err
	}
	bookkeeperMu.Lock()
	bookkeeperVal, bookkeeperFor = &name, s.path
	bookkeeperMu.Unlock()

	// 换签章人是基础数据的变更，要留痕（规范点名基础数据维护要记）。
	s.recordAudit(ctx, AuditEvent{
		Action: audit.ActionBookkeeperSet,
		Summary: func() string {
			if name == "" {
				return "清空本账套的记账人"
			}
			return "设置本账套的记账人为「" + name + "」"
		}(),
		Entity: "book", EntityID: "bookkeeper",
		Detail: map[string]any{"原记账人": before, "新记账人": name},
	})
	return nil
}

// ResetBookkeeperCache 清记账人缓存（切换账套后必须调，否则会读到上一本的）。
func ResetBookkeeperCache() {
	bookkeeperMu.Lock()
	bookkeeperVal, bookkeeperFor = nil, ""
	bookkeeperMu.Unlock()
}

// appSettings 是 ~/.mini-account/settings.json 的内容。
//
// ★ 这个文件是「这台机器上谁在用、日志怎么记」，
// 与界面自己的状态（上次打开的账套，存在系统配置目录）分开：
// 它要被**命令行也读到**，所以放在 ~/.mini-account 下。
type appSettings struct {
	// Audit 是日志设置。
	//
	// ★ 这里**没有**记账人：它是账套级的（见本文件开头），
	// 存进账套的 setting 表。留在本机的话，同一台电脑给两家公司
	// 做账就只能签同一个名 —— 而代账会计恰恰不是这样。
	Audit LogSettings `json:"audit"`
}

// loadSettingsFile 读设置文件；读不到时返回零值（不是错误）。
func loadSettingsFile() appSettings {
	var s appSettings
	p, err := settingsPath()
	if err != nil {
		return s
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(b, &s); err != nil {
		// 文件坏了也不能让程序起不来：设置只影响默认值。
		// 但要留个痕 —— 否则「设置自己丢了」会变成一桩无头案。
		_, _ = os.Stderr.WriteString("[settings] 设置文件解析失败（将使用默认值）：" + err.Error() + "\n")
		return appSettings{}
	}
	return s
}

// saveSettingsFile 原子地写设置文件。
func saveSettingsFile(s appSettings) error {
	p, err := settingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	// 规范化后再落盘：文件里留一个 0，读的人（包括未来的自己）
	// 会以为窗口是 0 秒，而实际生效的是默认 60 —— 写进去的必须是生效值。
	if s.Audit.ViewIntervalSeconds <= 0 {
		s.Audit.ViewIntervalSeconds = DefaultLogSettings().ViewIntervalSeconds
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// 先写临时文件再改名：半截的 settings.json 会让下次启动读不出设置，
	// 表现为「记账人自己没了」而用户不知道为什么。
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// 同步刷新缓存里的日志设置
	logSettingsMu.Lock()
	v := s.Audit
	if v.ViewIntervalSeconds <= 0 {
		v.ViewIntervalSeconds = DefaultLogSettings().ViewIntervalSeconds
	}
	logSettingsVal = &v
	logSettingsMu.Unlock()
	return nil
}

// ResetSettingsCache 清掉设置缓存（测试用）。
func ResetSettingsCache() {
	ResetBookkeeperCache()
	logSettingsMu.Lock()
	logSettingsVal = nil
	logSettingsMu.Unlock()
}

// ---------------------------------------------------------------------------
// 老版本的清理：把留存在本机配置里的记账人搬进账套
// ---------------------------------------------------------------------------

// legacyBookkeeper 读老版设置文件里的 bookkeeper 字段（早先的版本存在本机）。
//
// 返回一个**只读**结构：字段没了就是空串。
func legacyBookkeeper() string {
	p, err := settingsPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var raw map[string]any
	if json.Unmarshal(b, &raw) != nil {
		return ""
	}
	v, _ := raw["bookkeeper"].(string)
	return strings.TrimSpace(v)
}

// MigrateLegacyBookkeeper 把老版本留在本机配置里的记账人搬进当前账套。
//
// ★ 为什么要搬：记账人一度存在本机（`~/.mini-account/settings.json`），
// 改成账套级之后，那个字段就成了**没人读的垃圾** —— 但它还躺在文件里，
// 用户翻到会以为「记账人还是存在本机」。所以：
//
//	① 当前账套没设记账人时，把老值接过来（不丢用户打过的名字）；
//	② 无论有没有接，都把那个字段从本机文件里删掉。
//
// 只在**当前账套**缺值时接手：账套里已经有名字的话，本机那个旧值
// 属于别的账套（或者早先的试验），不能覆盖。
func (s *Service) MigrateLegacyBookkeeper(ctx context.Context) {
	legacy := legacyBookkeeper()
	if legacy == "" {
		return
	}
	if s != nil && s.db != nil {
		if cur, err := s.Bookkeeper(ctx); err == nil && cur == "" {
			if err := s.SetBookkeeper(ctx, legacy); err == nil {
				_, _ = os.Stderr.WriteString(
					"[settings] 已把本机配置里的记账人「" + legacy + "」并入当前账套\n")
			}
		}
	}
	stripLegacyBookkeeper()
}

// CleanLegacyBookkeeper 只清本机文件里的老字段（没有账套时用）。
func CleanLegacyBookkeeper() { stripLegacyBookkeeper() }

// stripLegacyBookkeeper 从本机设置文件里删掉老的 bookkeeper 字段。
func stripLegacyBookkeeper() {
	p, err := settingsPath()
	if err != nil {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var raw map[string]any
	if json.Unmarshal(b, &raw) != nil {
		return
	}
	if _, ok := raw["bookkeeper"]; !ok {
		return
	}
	delete(raw, "bookkeeper")
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
	}
}
