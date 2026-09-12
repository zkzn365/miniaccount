package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"miniaccount/internal/service"
)

// isolatedApp 让「默认目录」落在一个临时家目录里。
//
// ★ 测试绝不能往用户真实的 ~/.mini-account/dataDB 里写东西 ——
// 那既是坏习惯，也会在受限环境下直接失败（写工作区之外被拒绝）。
func isolatedApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	a, _ := newApp(t)
	return a
}

// 默认保存位置：建账表单不再要求用户自己想一个路径。
//
// 位置是 <家目录>/.mini-account/dataDB —— 三个平台一致。
// 以点开头是隐藏目录，所以界面必须给出路（建账页的「打开目录」按钮，
// 见 dialogs.go 的 RevealBookDir）。
func TestSuggestBookPathLivesInDataDB(t *testing.T) {
	a := isolatedApp(t)

	dir, f := a.DefaultBookDir()
	if f != nil {
		t.Fatalf("取默认目录失败: %v", f)
	}
	if !strings.HasSuffix(dir, filepath.Join(".mini-account", "dataDB")) {
		t.Errorf("默认目录 = %s，期望在 <家目录>/.mini-account/dataDB 下", dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("默认目录应当已经建好：%v", err)
	}

	p, f := a.SuggestBookPath("杭州云帆软件有限公司")
	if f != nil {
		t.Fatalf("取建议路径失败: %v", f)
	}
	if filepath.Dir(p) != dir {
		t.Errorf("建议路径 %s 不在默认目录 %s 下", p, dir)
	}
	if !strings.HasSuffix(p, ".db") {
		t.Errorf("建议路径 %s 没有 .db 后缀", p)
	}
}

// ★ 文件名一律是 ASCII。
//
// 账套要在三种系统、备份包、U 盘、网盘之间搬。中文文件名在这些环节里
// 出问题的概率不低（编码、归一化、命令行、别人的脚本），
// 而用户看不出问题出在哪。
func TestSuggestBookPathIsASCII(t *testing.T) {
	a := isolatedApp(t)

	cases := []struct{ in, want string }{
		{"杭州云帆软件有限公司", "hang-zhou-yun-fan-ruan-jian-you-xian-gong-si.db"},
		{"张三/李四公司", "zhang-san-li-si-gong-si.db"},
		{"公司:2025", "gong-si-2025.db"},
		{"", "book.db"},
		{"   ", "book.db"},
		{"...", "book.db"},
	}
	for _, c := range cases {
		p, f := a.SuggestBookPath(c.in)
		if f != nil {
			t.Fatalf("%q: %v", c.in, f)
		}
		base := filepath.Base(p)
		if base != c.want {
			t.Errorf("%q → %s，期望 %s", c.in, base, c.want)
		}
		for _, r := range base {
			if r > 127 {
				t.Errorf("%q → %s 里还有非 ASCII 字符 %q", c.in, base, r)
				break
			}
		}
	}
}

// ★ 单位名称不能被直接拼进路径。
//
// 名字里有路径分隔符时，最坏的结果不是「建不出账」，
// 而是**把账套建到了别的目录**——用户完全想不到是名字的问题。
func TestSuggestBookPathStaysInsideDir(t *testing.T) {
	a := isolatedApp(t)
	dir, _ := a.DefaultBookDir()

	for _, name := range []string{"../../逃逸", `..\..\逃逸`, "/etc/passwd", "a/b/c"} {
		p, f := a.SuggestBookPath(name)
		if f != nil {
			t.Fatalf("%q: %v", name, f)
		}
		if filepath.Dir(p) != dir {
			t.Errorf("%q: 路径 %s 跑到默认目录外面了", name, p)
		}
	}
}

// 重名要避让：单位名称留空时大家都会落到 book.db，
// 不避让的话第二本账会直接盖在第一本上 —— 那是丢账。
func TestSuggestBookPathAvoidsCollision(t *testing.T) {
	a := isolatedApp(t)

	first, f := a.SuggestBookPath("")
	if f != nil {
		t.Fatal(f)
	}
	if err := os.WriteFile(first, []byte("占位"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, f := a.SuggestBookPath("")
	if f != nil {
		t.Fatal(f)
	}
	if second == first {
		t.Errorf("重名没有避让：两次都给了 %s", first)
	}
	if filepath.Base(second) != "book-2.db" {
		t.Errorf("避让后的文件名 = %s，期望 book-2.db", filepath.Base(second))
	}
}

// 用户选了一个还不存在的目录时，建账要能把目录建出来，
// 而不是报一句底层数据库错误。
func TestOpenBookCreatesParentDir(t *testing.T) {
	a, _ := newApp(t)
	dir := t.TempDir()
	nested := filepath.Join(dir, "还没建的目录", "再深一层", "book.db")

	st, f := a.OpenBook(nested)
	if f != nil {
		t.Fatalf("打开/新建账套失败: %v", f)
	}
	if st.Open {
		t.Error("刚建出来的空库还不算账套")
	}
	if _, err := os.Stat(nested); err != nil {
		t.Errorf("账套文件没建出来：%v", err)
	}
}

// ★ 建账表单的默认身份是小规模纳税人。
//
// 这条测的是**界面**的默认值，不是 Go 的推断 —— 程序任何时候
// 都不按销售额猜身份，只把表单初始值设成最常见的那一种。
func TestWelcomeDefaultsToSmallScale(t *testing.T) {
	src, err := os.ReadFile("frontend/src/views/Welcome.vue")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "vatStatus: 'small_scale'") {
		t.Error("建账表单的增值税身份默认值不是 small_scale")
	}
	if strings.Contains(s, "vatStatus: 'general'") {
		t.Error("建账表单仍把一般纳税人当默认值")
	}
}

// ★ 建账表单不能要求用户自己填路径。
//
// 原来的表现是：不填保存位置就弹「请填写账套文件的保存位置」，
// 而第一次打开软件的人根本不知道该填什么。
func TestWelcomeShipsADefaultPath(t *testing.T) {
	src, err := os.ReadFile("frontend/src/views/Welcome.vue")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, need := range []string{
		"suggestBookPath", "ensurePath", "chooseBookDir", "revealBookDir",
	} {
		if !strings.Contains(s, need) {
			t.Errorf("建账页缺少 %s：默认路径 / 选目录 / 打开目录三者缺一，用户就会卡住", need)
		}
	}
}

// ★ 没账套时不能切页面：菜单要置灰，路由也要拦住。
func TestNoBookDisablesNavigation(t *testing.T) {
	app, err := os.ReadFile("frontend/src/App.vue")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), "canNavigate") {
		t.Error("侧栏没有按「有没有账套」禁用菜单")
	}

	r, err := os.ReadFile("frontend/src/router.js")
	if err != nil {
		t.Fatal(err)
	}
	rs := string(r)
	if !strings.Contains(rs, "beforeEach") || !strings.Contains(rs, "bookState") {
		t.Error("路由没有守卫：地址栏/书签仍能进到需要账套的页面")
	}
	if !strings.Contains(rs, "name: 'welcome'") {
		t.Error("守卫没有把用户送回建账页")
	}
}

// ★ 启动时先看账套目录里有什么：有账套就先问「打开还是新建」。
func TestListBooksFindsExistingBooks(t *testing.T) {
	a := isolatedApp(t)
	dir, f := a.DefaultBookDir()
	if f != nil {
		t.Fatal(f)
	}

	// 目录里放两本账（走真实的建账入口），外加一个不是账套的 .db
	first := filepath.Join(dir, "book.db")
	if _, f := a.OpenBook(first); f != nil {
		t.Fatal(f)
	}
	if _, err := a.CreateBook(service.CreateBookInput{
		CompanyName: "甲公司", VATStatus: "small_scale",
		StartYear: 2025, StartMonth: 1, ThroughYear: 2025,
		CurrentYear: 2025, CurrentMonth: 1,
	}); err != nil {
		t.Fatalf("建账失败: %v", err)
	}
	second := filepath.Join(dir, "second.db")
	if _, f := a.OpenBook(second); f != nil {
		t.Fatal(f)
	}
	if _, err := a.CreateBook(service.CreateBookInput{
		CompanyName: "乙公司", VATStatus: "general",
		StartYear: 2025, StartMonth: 1, ThroughYear: 2025,
		CurrentYear: 2025, CurrentMonth: 1,
	}); err != nil {
		t.Fatalf("建账失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "别的软件.db"), []byte("not a db"), 0o644); err != nil {
		t.Fatal(err)
	}

	lst, f := a.ListBooks()
	if f != nil {
		t.Fatalf("扫描失败: %v", f)
	}
	if !lst.HasAny {
		t.Fatal("★ 目录里有两本账，hasAny 却是假 —— 界面就不会问「打开还是新建」")
	}
	if len(lst.Books) != 2 {
		t.Fatalf("认出来的账套数 = %d，期望 2：%+v", len(lst.Books), lst.Books)
	}
	if len(lst.Others) != 1 {
		t.Errorf("不是账套的 .db 应当单独列出来，实际 %d 条", len(lst.Others))
	}
	// 上次打开的是第二本 → 默认选中它
	if lst.Suggested != second {
		t.Errorf("★ 默认选中的是 %s，期望上次打开的 %s", lst.Suggested, second)
	}
	if lst.LastPath != second {
		t.Errorf("LastPath = %s，期望 %s", lst.LastPath, second)
	}
}

// 目录是空的：不该问「打开还是新建」，直接进新建表单。
func TestListBooksEmptyDir(t *testing.T) {
	a := isolatedApp(t)
	lst, f := a.ListBooks()
	if f != nil {
		t.Fatal(f)
	}
	if lst.HasAny || len(lst.Books) != 0 {
		t.Errorf("空目录不该有账套：%+v", lst)
	}
	if lst.Dir == "" {
		t.Error("即使没有账套，也要告诉界面目录在哪")
	}
}

// 上次打开的账套放在别处（用户自己选的路径）时，也要出现在列表里 ——
// 否则「上次用的那本」会在启动列表里凭空消失。
func TestListBooksIncludesBookOutsideDir(t *testing.T) {
	a := isolatedApp(t)
	elsewhere := filepath.Join(t.TempDir(), "放别处的账.db")
	if _, f := a.OpenBook(elsewhere); f != nil {
		t.Fatal(f)
	}
	if _, err := a.CreateBook(service.CreateBookInput{
		CompanyName: "丙公司", VATStatus: "small_scale",
		StartYear: 2025, StartMonth: 1, ThroughYear: 2025,
		CurrentYear: 2025, CurrentMonth: 1,
	}); err != nil {
		t.Fatal(err)
	}

	lst, f := a.ListBooks()
	if f != nil {
		t.Fatal(f)
	}
	if !lst.HasAny || len(lst.Books) != 1 {
		t.Fatalf("别处的账套没被列出来：%+v", lst)
	}
	if lst.Books[0].Path != elsewhere {
		t.Errorf("列出的是 %s，期望 %s", lst.Books[0].Path, elsewhere)
	}
	if lst.Suggested != elsewhere {
		t.Errorf("默认选中 = %s，期望 %s", lst.Suggested, elsewhere)
	}
}
