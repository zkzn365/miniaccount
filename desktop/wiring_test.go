package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ★ 前端调用的每一个绑定都必须真实存在。
//
// 这是一整类「改了一半」的 bug 的机械防线：Go 侧改了方法名、或者前端
// 写错了一个字母，界面**不会报错也不会提示** —— Wails 的调用是
// 运行时按名字找的，找不到就是点击后静默无事发生，或者一句
// 「Cannot read properties of undefined」。
//
// 类型系统管不到这条缝（一边是 JS 字符串，一边是 Go 方法名），
// 所以用测试把它焊上：解析 api.js 里所有 A().Xxx 的引用，
// 与 wails 生成的 App.d.ts 对照。
func TestFrontendBindingNamesExist(t *testing.T) {
	apiJS, err := os.ReadFile("frontend/src/lib/api.js")
	if err != nil {
		t.Fatalf("读 api.js 失败: %v", err)
	}
	dts, err := os.ReadFile("frontend/wailsjs/go/main/App.d.ts")
	if err != nil {
		t.Fatalf("读 App.d.ts 失败（是否跑过 wails build？）: %v", err)
	}

	// 生成侧：export function Xxx(...): ...
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^export function (\w+)\(`).
		FindAllStringSubmatch(string(dts), -1) {
		declared[m[1]] = true
	}
	if len(declared) == 0 {
		t.Fatal("App.d.ts 里没有解析到任何绑定 —— 文件格式可能变了")
	}

	// 调用侧：A().Xxx
	used := map[string]bool{}
	for _, m := range regexp.MustCompile(`A\(\)\.(\w+)`).
		FindAllStringSubmatch(string(apiJS), -1) {
		used[m[1]] = true
	}
	if len(used) == 0 {
		t.Fatal("api.js 里没有解析到任何绑定调用 —— 文件格式可能变了")
	}

	// 反向也要查：声明了但前端从没用过的绑定不算错（可能给别处用），
	// 但前端用了却没声明的一定错。
	var missing []string
	for name := range used {
		if !declared[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("api.js 调用了 %d 个不存在的绑定：%v\n"+
			"（Go 侧方法名与前端不一致，界面上点击会静默失败）",
			len(missing), missing)
	}

	// 顺带报一下覆盖率，便于发现「绑定了但界面没用上」的死绑定
	if t.Failed() {
		return
	}
	t.Logf("绑定总数 %d，前端使用 %d，覆盖 %.0f%%",
		len(declared), len(used),
		float64(len(used))/float64(len(declared))*100)
}

// ★ 视图里引用的 api.* / notify 方法必须在 api.js 里真的有。
//
// 与上一条同一个道理，只是再往里一层：api.js 是前端的门面，
// 写错一个方法名同样只在点击时才炸。
func TestViewCallsExistInAPI(t *testing.T) {
	apiJS, err := os.ReadFile("frontend/src/lib/api.js")
	if err != nil {
		t.Fatal(err)
	}
	exported := map[string]bool{}
	// export const api = { ... } 里的方法名
	apiBlock := string(apiJS)
	if i := strings.Index(apiBlock, "export const api"); i >= 0 {
		apiBlock = apiBlock[i:]
	}
	for _, m := range regexp.MustCompile(`(?m)^\s{2}(\w+):`).
		FindAllStringSubmatch(apiBlock, -1) {
		exported[m[1]] = true
	}
	if len(exported) == 0 {
		t.Fatal("api.js 里没有解析到任何方法 —— 文件格式可能变了")
	}

	views, err := os.ReadDir("frontend/src/views")
	if err != nil {
		t.Fatal(err)
	}
	comps, err := os.ReadDir("frontend/src/components")
	if err != nil {
		t.Fatal(err)
	}

	var missing []string
	scan := func(dir string, entries []os.DirEntry) {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".vue") {
				continue
			}
			b, err := os.ReadFile(dir + "/" + e.Name())
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range regexp.MustCompile(`api\.(\w+)\(`).
				FindAllStringSubmatch(string(b), -1) {
				if !exported[m[1]] {
					missing = append(missing, e.Name()+" → api."+m[1])
				}
			}
		}
	}
	scan("frontend/src/views", views)
	scan("frontend/src/components", comps)

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("界面调用了 %d 个 api.js 里不存在的方法：\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// ★ 每一个导出的绑定都必须有 panic 护栏。
//
// Wails 绑定的是 *App 的**全部**导出方法，任何一个 panic 都会把整个
// 应用带崩。recoverTo 只能用在有 error 返回值的签名上，
// 所以「没有 error 的方法」很容易被漏掉 —— Today 和 AppVersion
// 就漏了很久。这条测试把「漏没漏」变成机械可查的。
func TestEveryBindingHasPanicGuard(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	// 绑定方法签名：func (a *App) Name(...)
	// 只认**导出**方法（首字母大写）—— 小写的 book/context/report
	// 是内部辅助函数，Wails 不会绑定它们。
	reBind := regexp.MustCompile(`(?m)^func \(a \*App\) ([A-Z]\w*)\(`)
	// 生命周期钩子不是绑定，Wails 不会把它们暴露给前端
	skip := map[string]bool{"startup": true, "shutdown": true, "domReady": true}

	var unguarded []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		// 按方法切段，检查每一段里有没有 recover 调用
		locs := reBind.FindAllStringSubmatchIndex(src, -1)
		for i, loc := range locs {
			method := src[loc[2]:loc[3]]
			if skip[method] {
				continue
			}
			end := len(src)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			body := src[loc[0]:end]
			if !strings.Contains(body, "recoverTo(") &&
				!strings.Contains(body, "recoverValue(") {
				unguarded = append(unguarded, name+" → "+method)
			}
		}
	}
	sort.Strings(unguarded)
	if len(unguarded) > 0 {
		t.Errorf("有 %d 个绑定没有 panic 护栏（一旦 panic 会崩掉整个应用）：\n  %s",
			len(unguarded), strings.Join(unguarded, "\n  "))
	}
}
