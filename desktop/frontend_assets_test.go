package main

import (
	"io/fs"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ★ 编进二进制里的那份界面，必须就是改好的那一份
// ---------------------------------------------------------------------------
//
// # 这一条测试是拿真实事故换来的
//
// 用户报回来四条 bug：建账不填保存位置就弹提示、没有默认位置、
// 没有账套时菜单还能点、默认纳税人身份不对。
//
// 源码里其实**四条都改好了**，`go test ./...` 全绿，前端冒烟测试也全过 ——
// 因为：
//
//   - 前端冒烟测试（test/gui.test.mjs）跑的是 `testdist/testentry.js`，
//     那是**从 src 现打**的包；
//   - Go 测试直接调 Go 方法，根本不经过界面。
//
// 而用户双击的是 `dist/小账本.app`，里面的界面产物是
// `desktop/frontend/dist` 经 `//go:embed` 编进去的 ——
// 那一份停在改动之前的构建上。**两条测试路径都绕开了它。**
//
// 所以这里直接读那份 embed 进来的产物，按用户看得见的文字逐条对。
// 它会在「改了 src 但没重新 npm run build」时立刻失败，
// 而这正是当初没人发现的原因。
//
// 修法：`cd mini-account && ./build.sh`（它会跑 wails build → npm run build）。
func TestEmbeddedFrontendHasTheReportedFixes(t *testing.T) {
	bundle := embeddedFrontendJS(t)

	cases := []struct {
		why   string
		need  string
		where string
	}{
		{
			why:   "没有账套时菜单要置灰并说明原因",
			need:  "还没有账套。建账之后这里的功能才会解锁。",
			where: "App.vue",
		},
		{
			why:   "建账页要预填默认保存位置（不能再让用户自己填）",
			need:  "suggestBookPath",
			where: "lib/api.js + Welcome.vue",
		},
		{
			why:   "界面上要显示默认目录，用户才知道账套去了哪",
			need:  "defaultBookDir",
			where: "lib/api.js + Welcome.vue",
		},
		{
			why:   "建账默认小规模纳税人",
			need:  `vatStatus:"small_scale"`,
			where: "Welcome.vue 的表单默认值",
		},
	}

	for _, c := range cases {
		if !strings.Contains(bundle, c.need) {
			t.Errorf("编进二进制的界面里没有「%s」（%s）。\n"+
				"源码里可能已经改好了，但 desktop/frontend/dist 是旧的 —— "+
				"用户双击 dist/小账本.app 时看到的还是老界面。\n"+
				"修：cd mini-account && ./build.sh",
				c.why, c.where)
		}
	}

	// 反向的一条：旧的那句提示不能再出现在产物里。
	// 它是「用户被卡在第一步」的直接原因。
	if strings.Contains(bundle, "请填写账套文件的保存位置") {
		t.Error("产物里还有「请填写账套文件的保存位置」—— " +
			"建账的第一屏仍然是个死胡同")
	}
}

// embeddedFrontendJS 把所有编进二进制的前端 JS 拼在一起。
//
// 文件名带 hash（index-C7spkVNX.js），所以只能遍历，
// 不能写死路径 —— 写死的话每次构建都会「测试失败」，
// 然后被人顺手改成跳过。
func embeddedFrontendJS(t *testing.T) string {
	t.Helper()

	var (
		js    strings.Builder
		files int
	)
	err := fs.WalkDir(assets, "frontend/dist", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".js") {
			return err
		}
		b, rerr := fs.ReadFile(assets, path)
		if rerr != nil {
			return rerr
		}
		files++
		js.Write(b)
		js.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatalf("读不了 embed 进来的前端产物：%v", err)
	}
	if files == 0 {
		t.Fatal("embed 里一个 .js 都没有 —— frontend/dist 是空的，" +
			"跑一次 wails build 或 cd desktop/frontend && npm run build")
	}
	t.Logf("检查了 %d 个前端产物文件，共 %d 字节", files, js.Len())
	return js.String()
}

// embeddedFrontendCSS 把所有编进二进制的 CSS 拼在一起。
func embeddedFrontendCSS(t *testing.T) string {
	t.Helper()

	var (
		css   strings.Builder
		files int
	)
	err := fs.WalkDir(assets, "frontend/dist", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".css") {
			return err
		}
		b, rerr := fs.ReadFile(assets, path)
		if rerr != nil {
			return rerr
		}
		files++
		css.Write(b)
		return nil
	})
	if err != nil {
		t.Fatalf("读不了 embed 进来的样式表：%v", err)
	}
	if files == 0 {
		t.Fatal("embed 里一个 .css 都没有 —— frontend/dist 是空的")
	}
	return css.String()
}

// ★ 界面上的文字必须能选中 —— 这是「可以复制」的前提。
//
// 样式表里一句 `user-select: none` 就能把整件事废掉，而且**界面上
// 看不出任何异常**：文字照常显示，只是拖选不动、右键没有复制、
// Cmd+C 复制了个空。用户只会说「这软件不能复制」。
//
// 所以这里直接读**编进二进制的那份样式表**，而不是读 src/style.css：
// 源码对了但没重新构建，用户手上的还是旧的（这条教训见文件头）。
func TestEmbeddedCSSAllowsTextSelection(t *testing.T) {
	css := embeddedFrontendCSS(t)

	// minify 之后是 `user-select:text`（可能带 -webkit- 前缀）。
	if !strings.Contains(css, "user-select:text") {
		t.Error("编进二进制的样式表里没有 user-select:text —— " +
			"界面上的文字拖不中，也就复制不了")
	}

	// 反向：不能再有把选中关掉的规则。
	// body 上写一句 none，上面那句就被盖掉了，而界面上完全看不出来。
	if strings.Contains(css, "user-select:none") {
		t.Errorf("样式表里还有 user-select:none —— " +
			"被它盖住的那部分文字选不中、复制不了")
	}
}
