// miniaccount 桌面应用入口（Wails v2）。
//
// # 构建
//
//	cd mini-account && . ./env.sh
//	go build -o miniaccount ./cmd/miniaccount          # 命令行版
//	cd desktop && wails build                          # 桌面版
//
// # 为什么 assets 用 embed
//
// 前端产物编译进二进制，用户拿到的是**一个可执行文件**，
// 不需要额外的 web 服务器、不需要装运行时。
// 这与「备份是一个文件、账套是一个文件」是同一套产品原则：
// 小微企业用户没有 IT 支持，能少一个文件就少一个。
package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"os"

	"miniaccount/internal/service"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// 无窗口模式：`小账本 --serve-bindings --db <账套>`。
	//
	// 这不是给用户用的功能，而是**让界面可以被自动化测试**的开关：
	// 没有它，「Vue 组件 ↔ api.js ↔ Wails 调用约定 ↔ Go 绑定」
	// 这条接缝就只能靠人开窗口点一遍来验证 —— 而它恰恰是出过
	// 「整个界面全废而测试全绿」的地方。详见 desktop/bindingserver.go。
	// 两种写法都认：`serve-bindings`（与其它子命令一致）与
	// `--serve-bindings`（注释里就是这么写的）。只认一种的话，
	// 另一种会**静默地起一个窗口**——在无头环境里表现为卡住不动。
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve-bindings", "--serve-bindings", "-serve-bindings":
			serveBindings(os.Args[2:])
			return
		}
	}

	app := NewApp()

	err := wails.Run(appOptions(app))
	if err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}

// appOptions 组装 Wails 的启动选项。
//
// 单独拆成一个函数是为了**能被测试**：这里的每一项都是「用户能不能
// 做某件事」的开关，写错了不会有任何报错，只会让某个功能静悄悄地不可用
// （见 desktop/appoptions_test.go）。
func appOptions(app *App) *options.App {
	return &options.App{
		Title: "小账本 — 小微企业记账",
		// 起始尺寸按「一张资产负债表能完整显示」来定：
		// 报表是这款软件的主界面，启动就被截断会让人以为功能不全。
		Width:     1280,
		Height:    860,
		MinWidth:  1024,
		MinHeight: 700,

		AssetServer: &assetserver.Options{Assets: assets},

		// 背景色与前端主题的 --background 保持一致，
		// 否则窗口出现到前端渲染完成之间会闪一下白屏。
		BackgroundColour: &options.RGBA{R: 250, G: 250, B: 249, A: 1},

		// ★ 右键菜单：不打开这个开关，右键**什么都不弹**，
		// 用户看着选中的文字却找不到「复制」。
		//
		// 键盘那一半（Cmd+C / Ctrl+C）不归这里管 —— Wails 的应用菜单里
		// 挂了标准的 Edit → Copy / Select All。两者都依赖样式表允许选中
		// （见 frontend/src/style.css 的 body{user-select:text}）。
		// 三个入口缺一个，用户就会说「这软件不能复制」。
		EnableDefaultContextMenu: true,

		OnStartup: app.startup,

		// 关闭时把账套连接放掉：SQLite 的 WAL 需要在正常关闭时合并，
		// 直接杀进程会留下一个下次打开需要恢复的库。
		OnShutdown: app.shutdown,

		Bind: []any{app},

		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			About: &mac.AboutInfo{
				Title:   "小账本",
				Message: "面向中国小微企业的本地记账软件\n账套与附件都在你自己的电脑上",
			},
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
	}
}

// serveBindings 以 JSON Lines 的方式提供界面绑定，不打开窗口。
func serveBindings(args []string) {
	fs := flag.NewFlagSet("serve-bindings", flag.ExitOnError)
	db := fs.String("db", "miniaccount.db", "账套数据库文件路径")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("参数错误: %v", err)
	}
	setupAudit()
	app := NewApp()
	if _, f := app.OpenBook(*db); f != nil {
		log.Fatalf("打开账套失败: %s", f)
	}
	defer app.shutdownHeadless()
	defer service.CloseAudit()
	// 提示写 stderr：stdout 是协议通道，混进日志会让调用方解析失败
	fmt.Fprintln(os.Stderr, "serve-bindings：等待 JSON Lines 请求（stdin → stdout）")
	if err := app.runBindingServer(os.Stdin, os.Stdout); err != nil {
		log.Fatalf("绑定服务结束: %v", err)
	}
}
