package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// ★ 前端契约测试跟着 go test 一起跑。
//
// `lib/api.js` 是跨语言边界那一层：Go 的返回值、Go 的错误、
// Wails 的调用约定，全部在这里落地成前端的形状。
// 它错一次，整个界面就全错 —— 而且错得**不响**：
// 返回对象的绑定会抛 `{} is not iterable`，返回数组的绑定
// 会静默拿到「第一个元素 + 第二个元素」。
//
// 这类错 Go 侧的类型系统看不见（那边是 JS），Go 的测试也测不到，
// 所以用 node 跑 frontend/test/api.test.mjs，并让它在 go test 里跑起来。
// 没装 node 时跳过，而不是失败：CI 上没装 node 不该挡住后端测试。
func TestFrontendBindingContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未安装 node，跳过前端契约测试")
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(node, args...)
		cmd.Dir = "frontend"
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("前端测试失败（%v）：\n%s", args, out)
		}
		t.Logf("%s", out)
	}
	run("--test", "test/api.test.mjs")

	// 界面冒烟测试要有真实产物才能跑：两个发布二进制 + 测试用的前端包。
	// 缺了就跳过（干净检出上不该因为「没构建过」而失败），
	// 但要跑一次完整验证时用 `./build.sh` 或 `npm run test:gui`。
	need := []string{
		"../dist/小账本-darwin-arm64",
		"../dist/miniaccount-darwin-arm64",
		"frontend/testdist/testentry.js",
	}
	for _, p := range need {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("缺少 %s —— 界面冒烟测试需要先构建（见 build.sh）", p)
		}
	}

	// ★ testdist 是**构建产物**，`node --test test/gui.test.mjs`
	// 不会重建它（只有 npm run test:gui 会）。
	//
	// 于是 go test 跑的是**上一次**构建出来的界面：改了 .vue 再跑
	// go test，测的还是旧代码 —— 改对了显示红、改错了显示绿，
	// 两种都发生过。这里先比时间戳，过期就重建。
	rebuildTestBundleIfStale(t)
	run("--test", "test/gui.test.mjs")
}

// rebuildTestBundleIfStale 发现 src/ 里有比 testdist 更新的文件就重建测试包。
//
// 只在过期时重建：新鲜时走的是「什么都不做」，不会给每次 go test
// 都加上一次 vite 构建。
func rebuildTestBundleIfStale(t *testing.T) {
	t.Helper()
	const bundle = "frontend/testdist/testentry.js"
	fi, err := os.Stat(bundle)
	if err != nil {
		return // 上面已经跳过了，这里只为防御
	}
	newest, newestPath := time.Time{}, ""
	for _, root := range []string{"frontend/src", "frontend/vite.test.config.js", "frontend/package.json"} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil // 走不进去就算了，别让清理逻辑挡住测试
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if info.ModTime().After(newest) {
				newest, newestPath = info.ModTime(), p
			}
			return nil
		})
	}
	if !newest.After(fi.ModTime()) {
		return
	}

	// 用 node_modules 里那个 vite 直接跑，不经过 npx —— npx 会去联网查最新版。
	// 路径要**先转成绝对路径**：exec 在 chdir 到 Dir 之后才 exec，
	// 相对路径会变成 frontend/frontend/... 而报「no such file」。
	vite, err := filepath.Abs(filepath.Join("frontend", "node_modules", ".bin", "vite"))
	if err != nil {
		t.Fatalf("拼 vite 路径失败: %v", err)
	}
	if _, err := os.Stat(vite); err != nil {
		t.Skipf("界面源码 %s 比 testdist 新（%s > %s），但没装前端依赖、无法重建 —— "+
			"先 cd desktop/frontend && npm install",
			newestPath, newest.Format(time.RFC3339), fi.ModTime().Format(time.RFC3339))
	}
	t.Logf("界面源码 %s 比 testdist 新，先重建测试包", newestPath)
	cmd := exec.Command(vite, "build", "--config", "vite.test.config.js")
	cmd.Dir = "frontend"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("重建测试包失败：%v\n%s", err, out)
	}
}
