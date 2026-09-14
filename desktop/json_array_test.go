package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// ★ 绑定返回的 JSON 里，数组字段不能是 null
// ---------------------------------------------------------------------------
//
// # 这一条是拿一个真实崩溃换来的
//
// 用户报「明细账这个页面出错了」：
//
//	null is not an object (evaluating 'o.value.rows.length')
//
// 根子在于 **Go 把 nil 切片编成 JSON 的 `null`，而不是 `[]`**。
// `LedgerResult.Rows` 在没有任何分录时是 nil，于是界面拿到
// `{"rows": null}`，而它写的是 `data.rows.length` —— 直接抛异常。
//
// 更麻烦的是它**只在没数据时出现**：开发时账套里都是演示数据，
// 每个科目都有分录，`rows` 永远非空，怎么点都不会崩。
// 用户新建一个账套、点开明细账，就撞上了。
//
// 所以这里不是修一个字段，而是把**这一类**都挡住：
// 拿一个**空账套**（没有任何凭证）去调所有会返回列表的绑定，
// 用反射把每个切片字段找出来，是 nil 就报。
//
// 空账套是关键 —— 有数据的账套测不出这个问题。
// ★ 两种账套都要跑：
//
//   - **空账套**：所有列表都是空的，nil 切片最容易露出来
//     （用户报的那次崩溃就是空账套 + 明细账）；
//   - **演示账套**：数据齐全，能走到「行存在但行内数组为空」这类分支，
//     那是空账套到不了的。
//
// 只测一种都会漏。
func TestBindingJSONHasNoNullArrays(t *testing.T) {
	t.Run("空账套", func(t *testing.T) {
		a, path := newApp(t)
		if f := faultOf(a.OpenBook(path)); f != nil {
			t.Fatal(f)
		}
		if _, err := a.CreateBook(service.CreateBookInput{
			CompanyName: "空账套", TaxType: "general",
			StartYear: 2025, StartMonth: 1, ThroughYear: 2025,
			CurrentYear: 2025, CurrentMonth: 3,
		}); err != nil {
			t.Fatal(err)
		}
		checkNoNullArrays(t, a)
	})

	t.Run("演示账套（有数据）", func(t *testing.T) {
		a, path := newApp(t)
		if f := faultOf(a.OpenBook(path)); f != nil {
			t.Fatal(f)
		}
		// CreateDemoBook 自己会建账（界面上「先生成演示账套看看」也是这么走的），
		// 先 CreateBook 反而会撞「账套已存在」。
		if _, err := a.CreateDemoBook(3); err != nil {
			t.Fatalf("生成演示账套失败: %v", err)
		}
		checkNoNullArrays(t, a)
	})
}

// checkNoNullArrays 逐个调用会返回列表的绑定，检查 JSON 里的数组字段不是 null。
func checkNoNullArrays(t *testing.T, a *App) {
	t.Helper()
	_ = context.Background()

	// 全是**会返回列表或含列表**的绑定 —— 界面上每一页读的都是这些。
	calls := []struct {
		name string
		call func() any
	}{
		{"LedgerDetail", func() any { v, _ := a.LedgerDetail(LedgerRequest{AccountPrefix: "1002"}); return v }},
		{"LedgerDetail(无此科目)", func() any { v, _ := a.LedgerDetail(LedgerRequest{AccountPrefix: "9999"}); return v }},
		{"Columnar", func() any { v, _ := a.Columnar(ColumnarRequest{AccountCode: "5602"}); return v }},
		{"Summary", func() any { v, _ := a.Summary(SummaryRequest{}); return v }},
		{"AgingReport", func() any { v, _ := a.AgingReport(AgingRequest{}); return v }},
		{"Statement", func() any { v, _ := a.Statement(StatementRequest{}); return v }},
		{"Overview", func() any { v, _ := a.Overview(); return v }},
		{"Periods", func() any { v, _ := a.Periods(); return v }},
		{"Vouchers", func() any { v, _ := a.Vouchers(VoucherQueryRequest{}); return v }},
		{"BankFlows", func() any { v, _ := a.BankFlows(BankFlowQueryRequest{}); return v }},
		{"Invoices", func() any { v, _ := a.Invoices(InvoiceQueryRequest{}); return v }},
		{"Claims", func() any { v, _ := a.Claims(ClaimQueryRequest{}); return v }},
		{"PayrollRuns", func() any { v, _ := a.PayrollRuns(); return v }},
		{"Employees", func() any { v, _ := a.Employees(false); return v }},
		{"Departments", func() any { v, _ := a.Departments(); return v }},
		{"AccountOptions", func() any { v, _ := a.AccountOptions(); return v }},
		{"ContactOptions", func() any { v, _ := a.ContactOptions(); return v }},
		{"Contacts", func() any { v, _ := a.Contacts(""); return v }},
		{"Contacts(按类型)", func() any { v, _ := a.Contacts("customer"); return v }},
		{"ContactKinds", func() any { return a.ContactKinds() }},
		{"ContactUsageOf", func() any { v, _ := a.ContactUsageOf(1); return v }},
		{"DepartmentUsageOf", func() any { v, _ := a.DepartmentUsageOf(1); return v }},
		{"Attachments", func() any { v, _ := a.Attachments(AttachmentQueryRequest{}); return v }},
		{"AISuggestions", func() any { v, _ := a.AISuggestions(20); return v }},
		{"AIConfig", func() any { v, _ := a.AIConfig(); return v }},
		{"Report(资产负债表)", func() any {
			v, _ := a.Report(ReportRequest{Kind: "balance_sheet"})
			return v
		}},
		{"Report(利润表)", func() any {
			v, _ := a.Report(ReportRequest{Kind: "income_statement"})
			return v
		}},
		// 下面这几个是「界面在嵌套结构里取 .length」时才暴露的 ——
		// 顶层数组没问题是假象，崩点在行内数组上。
		{"BankReconciliation", func() any { v, _ := a.BankReconciliation(ReconciliationRequest{}); return v }},
		{"VoucherDetail", func() any { v, _ := a.VoucherDetail(1); return v }},
	}

	for _, c := range calls {
		v := c.call()
		if v == nil {
			continue // 绑定自己失败了，不是这条测试要管的
		}
		// 先确认它真能编成 JSON —— 顺便覆盖「有字段编不出来」这类问题
		if _, err := json.Marshal(v); err != nil {
			t.Errorf("%s: 序列化失败: %v", c.name, err)
			continue
		}
		var bad []string
		collectNilSlices(reflect.ValueOf(v), c.name, &bad)
		for _, p := range bad {
			t.Errorf("%s: 数组字段 %s 是 null。\n"+
				"    Go 的 nil 切片会编成 JSON 的 null，而界面写的是 `x.rows.length` ——\n"+
				"    在有数据的账套上永远测不出来（演示数据里每个科目都有分录），\n"+
				"    用户新建一个空账套点进去就崩。\n"+
				"    修法：给该字段一个空切片（[]T{}），别留 nil。", c.name, p)
		}
	}
}

// collectNilSlices 用反射找出所有 nil 的切片字段。
//
// 走的是**结构体标签里的 JSON 名**，这样报出来的路径与前端
// `data.rows` 一模一样 —— 否则报个 Go 字段名，还得再去对一遍。
func collectNilSlices(v reflect.Value, path string, out *[]string) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return
		}
		collectNilSlices(v.Elem(), path, out)

	case reflect.Slice:
		if v.IsNil() {
			*out = append(*out, path)
			return
		}
		// 切片元素也可能是结构体（里面还有列表）
		for i := 0; i < v.Len(); i++ {
			collectNilSlices(v.Index(i), fmt.Sprintf("%s[%d]", path, i), out)
		}

	case reflect.Map:
		if v.IsNil() {
			return
		}
		iter := v.MapRange()
		for iter.Next() {
			collectNilSlices(iter.Value(), fmt.Sprintf("%s[%v]", path, iter.Key()), out)
		}

	case reflect.Struct:
		typ := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := typ.Field(i)
			if f.PkgPath != "" { // 未导出字段不参与 JSON
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			collectNilSlices(v.Field(i), path+"."+name, out)
		}
	}
}
