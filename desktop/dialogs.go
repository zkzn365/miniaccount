package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ---------------------------------------------------------------------------
// 系统文件管理器
// ---------------------------------------------------------------------------
//
// # 为什么需要这一组绑定
//
// 账套默认放在 <家目录>/.mini-account/dataDB —— 以点开头，是**隐藏目录**。
// 用户自己翻是翻不到的。藏起来却又不给入口，才是真正的问题；
// 藏起来但一键可达，是可以接受的。所以：
//
//	ChooseBookFile  打开已有账套：调起系统文件选择框，直接挑 dataDB 里的 .db
//	ChooseBookDir   新建账套：调起目录选择框，改保存位置
//	RevealBookDir   在 Finder / 资源管理器 / 文件里打开 dataDB，让用户自己看
//
// # ★ 无窗口时必须挡住，否则整个进程会死
//
// Wails 的 runtime 在拿不到前端时走的是 `log.Fatalf`（见
// wails/v2/pkg/runtime/runtime.go 的 getFrontend）—— 那是 `os.Exit(1)`，
// **不是返回 error**。
//
// 而本工程有一个无窗口模式：`小账本 --serve-bindings`，
// 界面冒烟测试（desktop/frontend/test/gui.test.mjs）正是靠它把
// 「真 Vue 组件 ↔ 真 api.js ↔ 真 Go 后端」接起来跑的。
// 在那个模式下调一次这三个方法，后端进程会当场退出，
// 表现是「所有测试一起失败、且看不出原因」。
//
// 所以这里先判断有没有窗口，没有就返回一条 Fault。

// guiReady 检查是否运行在有窗口的实例里；否返回一条 Fault。
//
// a.ctx 由 Wails 在窗口就绪后通过 startup 注入（见 app.go）。
// 无窗口模式（serve-bindings）下它始终是 nil。
func (a *App) guiReady() *Fault {
	if a.ctx == nil {
		return &Fault{
			Kind: FaultInternal,
			Message: "当前是无窗口模式，打不开系统的文件选择框。" +
				"请用打包好的程序打开，或手工填写路径。",
		}
	}
	return nil
}

// ChooseBookFile 让用户在系统文件管理器里挑一个账套文件。
//
// 返回值：
//   - 选了：账套文件的绝对路径
//   - 取消：空串（**不是错误** —— 取消是正常操作，界面不该弹红条）
func (a *App) ChooseBookFile() (out string, err error) {
	defer recoverTo(&err, "ChooseBookFile")()

	if f := a.guiReady(); f != nil {
		return "", f
	}
	// 默认位置必须是**已经存在**的目录：Wails 在
	// DefaultDirectory 不存在时会直接报 "default directory does not exist"，
	// 用户看到的就是「打不开选择框」。DefaultBookDir 会把它建出来。
	dir, f := a.DefaultBookDir()
	if f != nil {
		return "", f
	}

	p, derr := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "选择账套文件",
		DefaultDirectory: dir,
		// 显示隐藏文件：账套目录本身就是隐藏的（.mini-account），
		// 不打开这个开关，用户从别处进到那一层会看不到任何东西。
		ShowHiddenFiles: true,
		Filters: []runtime.FileFilter{
			{DisplayName: "账套文件 (*" + bookFileExt + ")", Pattern: "*" + bookFileExt},
			{DisplayName: "所有文件 (*.*)", Pattern: "*"},
		},
	})
	if derr != nil {
		return "", &Fault{Kind: FaultIO, Message: "打不开文件选择框：" + derr.Error()}
	}
	return p, nil
}

// ChooseBookDir 让用户挑一个目录，作为账套的保存位置。
//
// currentPath 是界面上当前填的路径：换目录之后**文件名保持不变**，
// 只换前面那一段。整条路径在 Go 侧拼，免得前端去操心
// Windows 的反斜杠 —— 混着用两种分隔符的路径显示出来就不像样了。
func (a *App) ChooseBookDir(currentPath string) (out string, err error) {
	defer recoverTo(&err, "ChooseBookDir")()

	if f := a.guiReady(); f != nil {
		return "", f
	}

	// 选择框的默认位置：优先用用户当前填的那个目录（它存在的话），
	// 否则回到默认账套目录。
	start := a.startDirFor(currentPath)

	p, derr := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:                "选择账套保存位置",
		DefaultDirectory:     start,
		ShowHiddenFiles:      true,
		CanCreateDirectories: true,
	})
	if derr != nil {
		return "", &Fault{Kind: FaultIO, Message: "打不开目录选择框：" + derr.Error()}
	}
	if p == "" {
		return "", nil // 用户取消
	}
	return filepath.Join(p, a.fileNameFor(currentPath)), nil
}

// RevealBookDir 在系统文件管理器里打开账套目录。
//
// 参数为空时打开默认账套目录。返回实际打开的目录 ——
// 界面可以顺手把它显示出来，用户就知道自己该去哪儿找。
func (a *App) RevealBookDir(dir string) (out string, err error) {
	defer recoverTo(&err, "RevealBookDir")()

	if f := a.guiReady(); f != nil {
		return "", f
	}

	target := strings.TrimSpace(dir)
	if target == "" {
		d, f := a.DefaultBookDir()
		if f != nil {
			return "", f
		}
		target = d
	} else {
		target = filepath.Dir(target)
		if target == "." {
			d, f := a.DefaultBookDir()
			if f != nil {
				return "", f
			}
			target = d
		}
	}

	// 目录不存在就先建出来。用户点了「打开目录」却什么都没发生，
	// 是最让人摸不着头脑的一种反馈。
	if merr := os.MkdirAll(target, 0o755); merr != nil {
		return "", &Fault{
			Kind:    FaultIO,
			Message: "建不了目录 " + target + "：" + merr.Error(),
		}
	}

	// 用 file:// 交给系统默认处理程序：Finder / 资源管理器 /
	// xdg-open 都会把它当「打开这个文件夹」。
	runtime.BrowserOpenURL(a.ctx, "file://"+filepath.ToSlash(target))
	return target, nil
}

// ---------------------------------------------------------------------------
// 内部小工具
// ---------------------------------------------------------------------------

// startDirFor 决定选择框应该从哪里打开。
func (a *App) startDirFor(currentPath string) string {
	if d := filepath.Dir(strings.TrimSpace(currentPath)); d != "" && d != "." {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			return d
		}
	}
	// 当前路径的目录不存在（用户刚改过、或者第一次进来），
	// 退回默认目录 —— 它一定存在。
	if d, f := a.DefaultBookDir(); f == nil {
		return d
	}
	return ""
}

// fileNameFor 从当前路径里取出文件名；取不到就给一个默认的。
//
// 换目录时文件名不变，是用户预期：他只是想换个地方放，
// 不是想改名字。
func (a *App) fileNameFor(currentPath string) string {
	base := filepath.Base(strings.TrimSpace(currentPath))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return defaultBookBaseName + bookFileExt
	}
	// 文件名必须是安全的：它会被拼进一条新路径。
	// 这里复用建账那一套规则（只留 ASCII），而不是再写一遍。
	if slug := pinyinSlug(strings.TrimSuffix(base, bookFileExt)); slug != "" {
		return slug + bookFileExt
	}
	return defaultBookBaseName + bookFileExt
}
