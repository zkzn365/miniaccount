package main

import "testing"

// ---------------------------------------------------------------------------
// 启动选项里那些「用户能不能做某件事」的开关
// ---------------------------------------------------------------------------
//
// 这一组字段的共同点是：写错了**不会报错**，只会让某个功能静悄悄地不可用。
// 用户看到的是一句「这软件不能复制」，而我们这边一切正常。
// 所以每一个都要有一条断言钉住。

// ★ 右键复制。
//
// 不打开这个开关，右键**什么都不弹** —— 用户明明看着选中的文字，
// 却找不到「复制」。而 Wails 的默认值是 false。
func TestContextMenuEnabledSoUsersCanRightClickCopy(t *testing.T) {
	opt := appOptions(NewApp())
	if !opt.EnableDefaultContextMenu {
		t.Error("EnableDefaultContextMenu 是关的 —— 右键不会弹出菜单，" +
			"用户选中的文字没有「复制」可点")
	}
}

// 窗口尺寸的几个下限不能被改成 0：0 在 Wails 里是「不限制」，
// 但也不给默认值，结果是一个几乎没法用的窗口。
func TestWindowHasUsableMinimumSize(t *testing.T) {
	opt := appOptions(NewApp())
	if opt.MinWidth < 800 || opt.MinHeight < 600 {
		t.Errorf("最小窗口尺寸 %dx%d 太小 —— 一张资产负债表会被截断，"+
			"用户会以为功能不全", opt.MinWidth, opt.MinHeight)
	}
	if opt.Width < opt.MinWidth || opt.Height < opt.MinHeight {
		t.Errorf("初始尺寸 %dx%d 小于最小尺寸 %dx%d",
			opt.Width, opt.Height, opt.MinWidth, opt.MinHeight)
	}
}

// 绑定与生命周期回调必须挂上 —— 少了 Bind，界面一个后端方法都调不到；
// 少了 OnShutdown，SQLite 的 WAL 不会被合并。
func TestAppOptionsWiresBindingsAndLifecycle(t *testing.T) {
	app := NewApp()
	opt := appOptions(app)

	if len(opt.Bind) != 1 || opt.Bind[0] != app {
		t.Errorf("Bind 没挂上 App 实例：%#v", opt.Bind)
	}
	if opt.OnStartup == nil {
		t.Error("OnStartup 是空的 —— 绑定的对话框拿不到窗口上下文")
	}
	if opt.OnShutdown == nil {
		t.Error("OnShutdown 是空的 —— 关窗时账套连接不会被放掉")
	}
	if opt.AssetServer == nil || opt.AssetServer.Assets == nil {
		t.Error("AssetServer 没挂上前端产物 —— 窗口会是一片空白")
	}
}
