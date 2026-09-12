package main

import (
	"os"
	"os/exec"
	"testing"
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
	run("--test", "test/gui.test.mjs")
}
