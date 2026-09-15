package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ★ 界面传上来的每一个字段，Go 侧都必须真的有这个字段。
//
// 这是「改了一半」bug 的第二种形态：绑定名对得上（TestFrontendBindingNamesExist
// 管这个），但**请求体里的某个字段不存在**。
//
// 后果比名字写错更隐蔽：名字写错是点下去没反应，一眼能看出；
// 少一个字段是**功能静默失效** —— 界面照常显示一个看起来完全正常的答案，
// 只是它没把用户的输入算进去。真实踩到的一次是「我确认命中例外情形」
// 那个勾：desktop.VATRateQuery 里没有 includeConditional，
// 勾了跟没勾一样，而且没有任何报错。
//
// 类型系统管不到这条缝（一边是 JS 对象的键，一边是 Go 结构体的字段），
// 所以用测试焊上：
//
//	api.someCall({ ...someForm })  →  wailsjs 绑定名  →  Go 方法第一个入参
//	→ 结构体的 json 标签 / 字段名，逐个比对 someForm 的键
//
// 只解析 `const x = ref({ ... })` 这种字面量形式 —— 解析不了的直接跳过，
// 宁可漏报也不误报：一个会误报的测试很快就会被加 ignore 注释绕过去，
// 那它就白写了。
func TestFrontendRequestFieldsExist(t *testing.T) {
	apiJS, err := os.ReadFile("frontend/src/lib/api.js")
	if err != nil {
		t.Fatalf("读 api.js 失败: %v", err)
	}
	// api 方法名 → 绑定名：someCall: (...) => call(A().SomeBinding, ...)
	bindingOf := map[string]string{}
	for _, m := range regexp.MustCompile(
		`(?m)^\s*(\w+):\s*\([^)]*\)\s*=>\s*call\(A\(\)\.(\w+)`).
		FindAllStringSubmatch(string(apiJS), -1) {
		bindingOf[m[1]] = m[2]
	}
	if len(bindingOf) == 0 {
		t.Fatal("api.js 里没有解析到任何 api 方法 → 绑定映射 —— 文件格式可能变了")
	}

	// ★ views 与 components 都要扫。
	//
	// 原来只扫 views/*.vue，而 EvidenceChain.vue（调 addEvidence /
	// deleteEvidence）住在 components 下，完全没被比对 ——
	// 防线恰好空在最容易写错的那一处。
	views, err := filepath.Glob("frontend/src/views/*.vue")
	if err != nil {
		t.Fatal(err)
	}
	comps, err := filepath.Glob("frontend/src/components/*.vue")
	if err != nil {
		t.Fatal(err)
	}
	views = append(views, comps...)

	var problems []string
	checked := 0
	for _, v := range views {
		raw, err := os.ReadFile(v)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		// 只取 <script setup> 段：模板里的对象字面量是另一回事
		if i := strings.Index(src, "<script"); i >= 0 {
			if j := strings.Index(src[i:], "</script>"); j >= 0 {
				src = src[i : i+j]
			}
		}
		forms := formLiterals(src)
		base := filepath.Base(v)
		for _, call := range callSites(src) {
			// 收集这次调用实际会发出去的键：内联的 + 展开表单的
			keys := append([]string(nil), call.inlineKeys...)
			for _, v := range call.spreadVars {
				form, ok := forms[v]
				if !ok {
					continue // 不是 ref({...}) 字面量，跳过
				}
				keys = append(keys, form...)
			}
			if len(keys) == 0 {
				continue
			}
			binding, ok := bindingOf[call.apiMethod]
			if !ok {
				problems = append(problems, base+": api."+call.apiMethod+
					" 在 api.js 里找不到对应绑定")
				continue
			}
			fields, ok := bindingRequestFields(binding)
			if !ok {
				continue // 该绑定第一个入参不是结构体（字符串、数字…）
			}
			checked++
			for _, key := range keys {
				// encoding/json 匹配字段名时**大小写不敏感**，
				// 所以 vatStatus 能落到 VATStatus —— 比对也必须这样，
				// 否则报的是一堆假警报，而假警报会让人把这个测试关掉。
				if !fields[strings.ToLower(key)] {
					problems = append(problems, base+" → api."+call.apiMethod+
						" → "+binding+": 字段 "+key+" 在 Go 侧不存在")
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("没有比对到任何一处请求体 —— 解析逻辑可能已经失效，别让它假装通过")
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Errorf("界面传了 Go 侧不存在的字段（%d 处）：\n  %s\n"+
			"（这类字段会被静默丢弃：勾了、填了都不起作用，界面也不报错）",
			len(problems), strings.Join(problems, "\n  "))
	}
	t.Logf("比对了 %d 处请求体，共 %d 个视图", checked, len(views))
}

// callArg 是一处 `api.xxx(...)` 调用的入参解析结果。
type callArg struct {
	apiMethod string
	// spreadVars 是被 `...` 展开的表单变量（如 `{ ...form }`）。
	spreadVars []string
	// inlineKeys 是直接写在调用里的键（如 `{ year, month: m }`）。
	inlineKeys []string
}

// callSites 找出所有 `api.xxx(<对象字面量>)` 调用并解析出键来源。
//
// 三种形式都要认：
//
//	api.createBook({ ...form.value })      展开表单
//	api.periods({ year: y, month: m })     内联字面量
//	api.report({ ...form, kind: 'bs' })    两者混用
//
// 只认字面量：`api.x(someVariable)` 这种跳过 —— 追它要写半个 JS 解释器，
// 而收益只是多覆盖几处，误报的代价却是让人把这个测试关掉。
func callSites(src string) []callArg {
	var out []callArg
	re := regexp.MustCompile(`api\.(\w+)\(`)
	for _, loc := range re.FindAllStringSubmatchIndex(src, -1) {
		open := loc[1] // 左括号之后
		if open >= len(src) || src[open] != '{' {
			continue
		}
		body, _ := matchBrace(src, open)
		if body == "" {
			continue
		}
		arg := callArg{apiMethod: src[loc[2]:loc[3]]}
		for _, item := range topLevelItems(body) {
			item = strings.TrimSpace(item)
			if rest, ok := strings.CutPrefix(item, "..."); ok {
				// `...form.value` 与 `...form` 都取根变量名
				name := strings.TrimSpace(rest)
				if i := strings.IndexAny(name, ". "); i >= 0 {
					name = name[:i]
				}
				if name != "" {
					arg.spreadVars = append(arg.spreadVars, name)
				}
				continue
			}
			key, _, ok := strings.Cut(item, ":")
			if !ok {
				// 简写属性 `{ year, month }`
				key = item
			}
			key = strings.TrimSpace(key)
			if key == "" || strings.ContainsAny(key, " '\"`{[(<") ||
				strings.Contains(key, "\n") {
				continue
			}
			arg.inlineKeys = append(arg.inlineKeys, key)
		}
		if len(arg.spreadVars) > 0 || len(arg.inlineKeys) > 0 {
			out = append(out, arg)
		}
	}
	return out
}

// matchBrace 从 src[open]（应为 '{'）起匹配到配对的 '}'，返回内部文本。
func matchBrace(src string, open int) (string, int) {
	depth := 0
	inStr := byte(0)
	for i := open; i < len(src); i++ {
		c := src[i]
		if inStr != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			inStr = c
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
			if depth == 0 {
				return src[open+1 : i], i + 1
			}
		}
	}
	return "", open
}

// stripComments 去掉 `//` 行注释与 `/* */` 块注释（不碰字符串里的）。
//
// ★ 必须先去注释再解析键。
//
// 表单字面量里通常每个字段上面都有一行注释说明「这个开关是干什么的」，
// 而注释里没有逗号 —— 于是 `// 说明文字\n  includeConditional: false`
// 会连成一整块，被当成一个键名，那个字段就**从比对里消失了**。
// 少了这个字段的比对，等于这个测试在最该管的地方闭着眼。
func stripComments(src string) string {
	var b strings.Builder
	inStr := byte(0)
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr != 0 {
			b.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				b.WriteByte(src[i])
				continue
			}
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch {
		case c == '\'' || c == '"' || c == '`':
			inStr = c
			b.WriteByte(c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			i += 2 + end + 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// topLevelItems 按顶层逗号切分对象字面量的内容。
func topLevelItems(body string) []string {
	body = stripComments(body)
	var items []string
	depth := 0
	inStr := byte(0)
	start := 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inStr != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			inStr = c
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, body[start:i])
				start = i + 1
			}
		}
	}
	if start < len(body) {
		items = append(items, body[start:])
	}
	return items
}

// formLiterals 解析 `const x = ref({ ... })` 的顶层键。
func formLiterals(src string) map[string][]string {
	out := map[string][]string{}
	re := regexp.MustCompile(`const\s+(\w+)\s*=\s*ref\(\s*\{`)
	for _, loc := range re.FindAllStringSubmatchIndex(src, -1) {
		open := strings.LastIndex(src[:loc[1]], "{")
		body, _ := matchBrace(src, open)
		if body == "" {
			continue
		}
		var keys []string
		for _, item := range topLevelItems(body) {
			item = strings.TrimSpace(item)
			if item == "" || strings.HasPrefix(item, "//") || strings.HasPrefix(item, "...") {
				continue
			}
			key, _, ok := strings.Cut(item, ":")
			if !ok {
				key = item
			}
			key = strings.TrimSpace(key)
			if key == "" || strings.ContainsAny(key, " '\"`{[(<") || strings.Contains(key, "\n") {
				continue
			}
			keys = append(keys, key)
		}
		if len(keys) > 0 {
			out[src[loc[2]:loc[3]]] = keys
		}
	}
	return out
}

// bindingRequestFields 反射 App 方法第一个入参的字段名与 json 标签。
func bindingRequestFields(binding string) (map[string]bool, bool) {
	m, ok := reflect.TypeOf(&App{}).MethodByName(binding)
	if !ok {
		return nil, false
	}
	ft := m.Type
	if ft.NumIn() != 2 { // 接收者 + 入参
		return nil, false
	}
	in := ft.In(1)
	for in.Kind() == reflect.Pointer {
		in = in.Elem()
	}
	if in.Kind() != reflect.Struct {
		return nil, false
	}
	fields := map[string]bool{}
	for i := 0; i < in.NumField(); i++ {
		f := in.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		// `json:"-"` 是**明确排除**：字段在，但 JSON 里永远收不到 ——
		// 与「字段根本不存在」对界面是同一个后果，必须算缺。
		if tag == "-" {
			continue
		}
		// 字段名统一小写比对：Go 的 encoding/json 是大小写不敏感匹配的，
		// 前端的 on / method / taxYuan / vatStatus 靠这条规则落到
		// On / Method / TaxYuan / VATStatus。
		fields[strings.ToLower(f.Name)] = true
		if tag == "" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name != "" {
			fields[strings.ToLower(name)] = true
		}
	}
	return fields, true
}
