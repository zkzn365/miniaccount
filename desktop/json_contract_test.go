package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// ★ 过绑定的结构体字段，JSON 名必须是 camelCase。
//
// 这是「改了一半」bug 的第三种形态，也是最隐蔽的一种。
//
// Wails 用 encoding/json 把返回值发给前端，而 Go 的默认规则是
// **无标签就用字段名原样** —— `CompanyName` 发出去就是 `"CompanyName"`。
// 界面里写的是 `book.companyName`，于是拿到 undefined：
//
//   - 不报错
//   - 不崩溃
//   - 页面上那一格就是**空的**
//
// 比报错难查得多：报错至少告诉你哪里不对，空值什么都不说。
// 真实的例子：`service.Dashboard` 一个标签都没写，首页的金额、
// 待办数、勾稽问题全是 undefined；`service.BookInfo` 同理，
// 顶栏永远显示「尚未打开账套」。
//
// 为什么要用测试焊住：这个约定跨了语言边界，类型系统两边都管不到。
// 少写一个标签不会有任何提示，只会让界面上少一个数字。
func TestBoundStructsUseCamelCaseJSON(t *testing.T) {
	app := reflect.TypeOf(&App{})

	// 收集所有过绑定的结构体（含嵌套），键为类型名。
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type, depth int)
	walk = func(rt reflect.Type, depth int) {
		if depth > 8 || rt == nil {
			return
		}
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice ||
			rt.Kind() == reflect.Array {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		// 标准库与第三方类型不归我们管
		if !strings.HasPrefix(rt.PkgPath(), "miniaccount/") {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if !f.IsExported() {
				continue
			}
			walk(f.Type, depth+1)
		}
	}

	var offenders []string
	checked := 0
	for i := 0; i < app.NumMethod(); i++ {
		m := app.Method(i)
		ft := m.Type
		// 注意 m.Type **含接收者**（NumIn 会把接收者算进去），
		// 但返回值里没有接收者，所以这里从 0 开始遍历。
		for j := 0; j < ft.NumOut(); j++ {
			walk(ft.Out(j), 0)
		}
	}

	for rt := range seen {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if !f.IsExported() {
				continue
			}
			// 匿名（嵌入）字段不参与：encoding/json 会把它的字段**提升**到外层，
			// 所以「没有标签」在这里是正常的，不是漏写。
			if f.Anonymous {
				continue
			}
			name, ok := jsonName(f)
			if !ok {
				offenders = append(offenders,
					rt.Name()+"."+f.Name+"（没有 json 标签 → 发出去就是 "+
						f.Name+"）")
				continue
			}
			checked++
			if !isCamel(name) {
				offenders = append(offenders,
					rt.Name()+"."+f.Name+"（标签是 "+name+"）")
			}
		}
	}

	if checked == 0 {
		t.Fatal("没有收集到任何字段 —— 反射逻辑可能失效了，别让它假装通过")
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("有 %d 个字段的 JSON 名不是 camelCase：\n  %s\n"+
			"（界面按 camelCase 取值，对不上就是 undefined：不报错、不崩溃，只是空的）",
			len(offenders), strings.Join(offenders, "\n  "))
	}
	t.Logf("检查了 %d 个结构体 / %d 个字段", len(seen), checked)
}

func jsonName(f reflect.StructField) (string, bool) {
	tag := f.Tag.Get("json")
	if tag == "" {
		return "", false
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" || name == "-" {
		return "", false
	}
	return name, true
}

// isCamel 判断首字母小写的驼峰名（允许 dbSha256 这种带数字的）。
//
// ★ 连写的大写缩写串（VAT、IDs、MS）一律要拆开并小写：
// 界面按 `voucherIds` / `vatStatusLabel` / `latencyMs` 取值。
// 反过来写成 `voucherIDs` 也能自洽，但两种写法一旦并存，
// 「下一个字段该写哪个」就只能靠猜 —— 而猜错的表现是界面上少一个数字。
func isCamel(s string) bool {
	if s == "" {
		return false
	}
	r := []rune(s)[0]
	if !unicode.IsLower(r) {
		return false
	}
	// 下划线说明是 snake_case，也不合约定（界面统一用驼峰）
	if strings.Contains(s, "_") {
		return false
	}
	// 不允许连写的大写缩写：VAT / IDs / MS / URL
	run := 0
	for _, c := range s {
		if unicode.IsUpper(c) {
			run++
			if run >= 2 {
				return false
			}
			continue
		}
		run = 0
	}
	return true
}
