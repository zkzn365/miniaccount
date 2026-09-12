package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/service"
	"miniaccount/internal/store/sqlite"
)

// ---------------------------------------------------------------------------
// 金额解析：★ 全链路唯一的「元 → 分」入口
// ---------------------------------------------------------------------------

func TestParseYuan(t *testing.T) {
	ok := map[string]money.Money{
		"":           0,
		"0":          0,
		"1":          money.Yuan,
		"0.1":        10,
		"0.01":       1,
		"106000":     10600000,
		"106000.00":  10600000,
		"106000.5":   10600050,
		"106,000.00": 10600000, // 从 Excel 粘过来的千分位
		"1，234.56":   123456,   // 全角逗号
		"-3000":      -300000,
		"-0.01":      -1,
		"+12.34":     1234,
		" 88.80 ":    8880,
		"　99　":       9900, // 全角空格
		"0.00":       0,
		".5":         50,
	}
	for in, want := range ok {
		got, err := ParseYuan(in)
		if err != nil {
			t.Errorf("ParseYuan(%q) 报错: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseYuan(%q) = %d，期望 %d", in, got, want)
		}
	}

	// ★ 0x10 / 0b101 / 1_000 是 Go 的整数字面量写法。
	// 曾经这里用 fmt.Sscanf("%d") 解析，它会把 0x10 当成 16 —— 静默接受。
	bad := []string{"1.234", "abc", "1.2.3", "--1", "1e3", "0x10", "0b101", "1_000", "三百"}
	for _, in := range bad {
		if _, err := ParseYuan(in); err == nil {
			t.Errorf("ParseYuan(%q) 应被拒绝", in)
		}
	}
}

// ★ 金额绝不能经过 float64。
//
// 界面上的 0.1 在 IEEE754 下是 0.1000000000000000055…，
// 一旦用它乘 100 再取整，就会出现 9.999999 被截成 9 分的经典事故。
// 这里逐分遍历一遍常见输入，确认没有任何一分钱的漂移。
func TestParseYuanNoFloatDrift(t *testing.T) {
	for cents := 0; cents < 2000; cents++ {
		yuan := money.Money(cents)
		s := strings.TrimSpace(yuan.String()) // 形如 "12.34"
		got, err := ParseYuan(s)
		if err != nil {
			t.Fatalf("ParseYuan(%q) 报错: %v", s, err)
		}
		if got != yuan {
			t.Fatalf("ParseYuan(%q) = %d，期望 %d（金额出现漂移）", s, got, cents)
		}
	}
}

// ---------------------------------------------------------------------------
// 错误分类：★ 界面靠它决定「弹什么、给什么操作」
// ---------------------------------------------------------------------------

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want FaultKind
	}{
		{service.ErrNoBook, FaultNoBook},
		{sqlite.ErrBookNotSetup, FaultNoBook},
		{sqlite.ErrHealthFailed, FaultBlocked},
		{sqlite.ErrAlreadyClosed, FaultBlocked},
		{sqlite.ErrPriorOpen, FaultBlocked},
		{sqlite.ErrLaterClosed, FaultBlocked},
		{sqlite.ErrBookExists, FaultInvalid},
		{sqlite.ErrBadTaxType, FaultInvalid},
		{errors.New("某种没见过的错误"), FaultInternal},
	}
	for _, c := range cases {
		f := classify(c.err)
		if f == nil {
			t.Errorf("classify(%v) 返回 nil", c.err)
			continue
		}
		if f.Kind != c.want {
			t.Errorf("classify(%v).Kind = %s，期望 %s", c.err, f.Kind, c.want)
		}
		if f.Message == "" {
			t.Errorf("classify(%v) 没有消息", c.err)
		}
	}
	if classify(nil) != nil {
		t.Error("nil 错误应返回 nil 而不是一个假 Fault")
	}
}

// ---------------------------------------------------------------------------
// 账套生命周期（走真实的 Service，不用 mock）
// ---------------------------------------------------------------------------

func newApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	a := NewApp()
	a.ctx = context.Background()
	t.Cleanup(func() { a.shutdown(context.Background()) })
	return a, path
}

// 未打开账套时，所有需要账套的方法都必须给出 FaultNoBook，
// 而不是 panic 或返回零值 —— 界面靠这个跳到欢迎页。
func TestMethodsWithoutBook(t *testing.T) {
	a, _ := newApp(t)

	if st, f := a.CurrentBook(); f != nil || st.Open {
		t.Errorf("未打开账套时 CurrentBook 应返回 open=false，实际 %+v / %v", st, f)
	}
	type check struct {
		name string
		f    *Fault
	}
	checks := []check{
		{"Overview", faultOf(a.Overview())},
		{"Periods", faultOf(a.Periods())},
		{"Health", faultOf(a.Health(PeriodRequest{Year: 2025, Month: 1}))},
		{"PreviewClose", faultOf(a.PreviewClose(PeriodRequest{Year: 2025, Month: 1}))},
		{"Close", faultOf(a.Close(PeriodRequest{Year: 2025, Month: 1, By: "王"}))},
		{"Reopen", faultOf(a.Reopen(PeriodRequest{Year: 2025, Month: 1, By: "王"}))},
		{"Report", faultOf(a.Report(ReportRequest{Kind: "bs", Year: 2025, Month: 1}))},
		{"LedgerDetail", faultOf(a.LedgerDetail(LedgerRequest{AccountPrefix: "1002"}))},
		{"FilesDir", faultOf(a.FilesDir())},
		{"AIConfig", faultOf(a.AIConfig())},
		{"AISuggestions", faultOf(a.AISuggestions(10))},
	}
	for _, c := range checks {
		if c.f == nil {
			t.Errorf("%s 未打开账套时应返回 Fault", c.name)
			continue
		}
		if c.f.Kind != FaultNoBook {
			t.Errorf("%s 的错误类别 = %s，期望 %s", c.name, c.f.Kind, FaultNoBook)
		}
	}
}

func TestCreateBookAndOverview(t *testing.T) {
	a, path := newApp(t)

	if f := faultOf(a.OpenBook(path)); f != nil {
		t.Fatalf("打开失败: %v", f)
	}
	st, cerr := a.CurrentBook()
	if f := faultOf(st, cerr); f != nil {
		t.Fatal(f)
	}
	if st.Open {
		t.Fatal("新文件还不是账套，不该报告已打开")
	}

	info, cerr2 := a.CreateBook(service.CreateBookInput{
		CompanyName: "桌面端测试公司", TaxType: "general",
		StartYear: 2025, StartMonth: 1, ThroughYear: 2025,
		CurrentYear: 2025, CurrentMonth: 3,
	})
	if f := faultOf(info, cerr2); f != nil {
		t.Fatalf("建账失败: %v", f)
	}
	if !info.Open || info.Book.CompanyName != "桌面端测试公司" {
		t.Fatalf("建账后状态不对: %+v", info)
	}
	if len(info.Book.Periods) != 12 {
		t.Errorf("期间数 = %d，期望 12", len(info.Book.Periods))
	}

	d, oerr := a.Overview()
	if f := faultOf(d, oerr); f != nil {
		t.Fatal(f)
	}
	if d.CurrentPeriod != "2025-01" {
		t.Errorf("当前期间 = %s，期望 2025-01", d.CurrentPeriod)
	}
	if len(d.BalanceSheetIssues) != 0 {
		t.Errorf("空账套不该有勾稽问题: %v", d.BalanceSheetIssues)
	}
}

// 重复建账应返回「输入不合法」而不是内部错误 ——
// 界面据此把用户引回「打开账套」而不是弹一个看不懂的数据库错误。
func TestCreateBookTwice(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	in := service.CreateBookInput{
		CompanyName: "A", StartYear: 2025, StartMonth: 1, ThroughYear: 2025,
		CurrentYear: 2025, CurrentMonth: 1,
	}
	if _, e := a.CreateBook(in); e != nil {
		t.Fatal(e)
	}
	_, e := a.CreateBook(in)
	f := faultOf[any](nil, e)
	if f == nil {
		t.Fatal("重复建账应失败")
	}
	if f.Kind != FaultInvalid {
		t.Errorf("错误类别 = %s，期望 %s（界面要能区分并引导用户）", f.Kind, FaultInvalid)
	}
}

// 打开另一个账套时必须先关掉旧的：
// 单文件 SQLite 被两个连接同时持有会互相锁死。
func TestOpenBookSwitchesCleanly(t *testing.T) {
	a, first := newApp(t)
	dir := filepath.Dir(first)
	second := filepath.Join(dir, "second.db")

	a.OpenBook(first)
	info, e1 := a.CreateBook(service.CreateBookInput{
		CompanyName: "第一个", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 1,
	})
	if f := faultOf(info, e1); f != nil {
		t.Fatal(f)
	}
	if info.Book.CompanyName != "第一个" {
		t.Fatal("第一个账套没建好")
	}

	// 切到第二个
	if f := faultOf(a.OpenBook(second)); f != nil {
		t.Fatalf("切换账套失败: %v", f)
	}
	if _, e := a.CreateBook(service.CreateBookInput{
		CompanyName: "第二个", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 1,
	}); e != nil {
		t.Fatalf("第二个账套建账失败: %v", e)
	}
	st, _ := a.CurrentBook()
	if st.Book.CompanyName != "第二个" {
		t.Errorf("当前账套 = %s，期望「第二个」", st.Book.CompanyName)
	}

	// 切回第一个应能正常打开（说明旧连接确实被释放了）
	if f := faultOf(a.OpenBook(first)); f != nil {
		t.Fatalf("切回第一个账套失败（连接可能没释放）: %v", f)
	}
	st, _ = a.CurrentBook()
	if st.Book.CompanyName != "第一个" {
		t.Errorf("当前账套 = %s，期望「第一个」", st.Book.CompanyName)
	}
}

// ---------------------------------------------------------------------------
// 期间与结账
// ---------------------------------------------------------------------------

func TestPeriodRequestValidation(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	})

	for _, req := range []PeriodRequest{{Year: 2025, Month: 0}, {Year: 0, Month: 1}, {Year: 2025, Month: 13}} {
		_, e := a.Health(req)
		f := faultOf[any](nil, e)
		if f == nil || f.Kind != FaultInvalid {
			t.Errorf("非法期间 %+v 应报 FaultInvalid，实际 %v", req, f)
		}
	}
}

// 结账必须记名 —— 《会计法》要求记账凭证有记账签章
func TestCloseRequiresOperator(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	})

	_, ce := a.Close(PeriodRequest{Year: 2025, Month: 1})
	f := faultOf[any](nil, ce)
	if f == nil || f.Kind != FaultInvalid {
		t.Fatalf("缺少结账人应报 FaultInvalid，实际 %v", f)
	}
	if !strings.Contains(f.Message, "签章") {
		t.Errorf("应说明原因，实际 %q", f.Message)
	}
}

func TestClosePreviewThenClose(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	})

	pv, perr := a.PreviewClose(PeriodRequest{Year: 2025, Month: 1})
	if f := faultOf(pv, perr); f != nil {
		t.Fatal(f)
	}
	if pv.Health == nil || !pv.Health.CanClose {
		t.Fatalf("空账套应可结账: %+v", pv.Health)
	}
	if len(pv.Steps) == 0 {
		t.Error("预览应给出结账步骤")
	}

	// 预览不得写任何数据
	info, _ := a.Periods()
	if info.Periods[0].Status != "open" {
		t.Errorf("预览后 1 月状态 = %s，期望仍为 open", info.Periods[0].Status)
	}

	res, cerr3 := a.Close(PeriodRequest{Year: 2025, Month: 1, By: "王主管"})
	if f := faultOf(res, cerr3); f != nil {
		t.Fatalf("结账失败: %v", f)
	}
	if res.VoucherCreated {
		t.Error("空期间不该生成结转凭证")
	}

	info, _ = a.Periods()
	if info.Periods[0].Status != "closed" {
		t.Errorf("结账后 1 月状态 = %s", info.Periods[0].Status)
	}
	// 顺序结账规则由 Go 侧算好给界面
	if !info.Periods[0].CanReopen {
		t.Error("1 月结账后应可反结账")
	}
	if !info.Periods[1].CanClose {
		t.Error("1 月结完，2 月应可结账")
	}
}

// 跳过前期直接结账必须被拦下，且不能留下状态改动
func TestCloseOutOfOrder(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	})

	_, ce := a.Close(PeriodRequest{Year: 2025, Month: 3, By: "王主管"})
	f := faultOf[any](nil, ce)
	if f == nil {
		t.Fatal("跳过前期结账应失败")
	}
	if f.Kind != FaultBlocked {
		t.Errorf("错误类别 = %s，期望 %s", f.Kind, FaultBlocked)
	}
	info, _ := a.Periods()
	for _, p := range info.Periods {
		if p.Month == 3 && p.Status != "open" {
			t.Errorf("失败的结账不该改动状态，3 月 = %s", p.Status)
		}
	}
}

// ---------------------------------------------------------------------------
// 报表桥接：★ 前端只认一种结构，转换错了整张表都是错的
// ---------------------------------------------------------------------------

func TestReportsOnEmptyBook(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	})

	for _, kind := range []string{"trial", "bs", "pl", "cashflow", "contact"} {
		r, e := a.Report(ReportRequest{Kind: kind, Year: 2025, Month: 3})
		if f := faultOf(r, e); f != nil {
			t.Errorf("报表 %s 失败: %v", kind, f)
			continue
		}
		if r.Title == "" {
			t.Errorf("报表 %s 没有标题", kind)
		}
		if len(r.Columns) == 0 {
			t.Errorf("报表 %s 没有列定义", kind)
		}
		// ★ 列名与每行的值必须同长 —— 不一致会让表头与数据整体错位。
		// 这是前端唯一能依赖的结构不变式。
		for _, row := range r.Rows {
			if len(row.Values) != len(r.Columns) {
				t.Errorf("报表 %s 第 %q 行的金额列数 = %d，期望 %d（与列名数一致）",
					kind, row.Label, len(row.Values), len(r.Columns))
				break
			}
		}
	}
}

func TestReportUnknownKind(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 1,
	})
	_, re := a.Report(ReportRequest{Kind: "不存在", Year: 2025, Month: 1})
	f := faultOf[any](nil, re)
	if f == nil {
		t.Fatal("未知报表类型应报错")
	}
	if f.Kind != FaultInternal {
		t.Errorf("类别 = %s", f.Kind)
	}
}

// 资产负债表必须是 53 行（会小企 01 表的官方行数）
func TestBalanceSheetShape(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	})

	r, rerr := a.Report(ReportRequest{Kind: "bs", Year: 2025, Month: 3})
	if f := faultOf(r, rerr); f != nil {
		t.Fatal(f)
	}
	if len(r.Columns) != 4 {
		t.Errorf("资产负债表应有 4 列（左右两栏 × 期末/年初），实际 %d", len(r.Columns))
	}
	// 左右两栏行数不同，取较多的一侧；53 行官方表拆成两栏后约 30 行
	if len(r.Rows) < 25 || len(r.Rows) > 35 {
		t.Errorf("行数 = %d，与 53 行官方表拆两栏的预期不符", len(r.Rows))
	}
	// ★ 左右两栏的项目名必须分列，前端才能各自对齐渲染。
	// 拼成一个字符串就没法拆开。
	var withRight int
	for _, row := range r.Rows {
		if row.RightLabel != "" {
			withRight++
		}
		if strings.Contains(row.Label, "│") {
			t.Errorf("项目名不该把左右两栏拼在一起: %q", row.Label)
		}
	}
	if withRight == 0 {
		t.Error("资产负债表应有右栏项目名")
	}
	// 左栏第一行的行次应是官方行次
	if r.Rows[0].No == "" {
		t.Error("资产负债表的行应带官方行次")
	}

	// 空账套上勾稽应成立
	for _, iss := range r.Issues {
		if iss.Fatal {
			t.Errorf("空账套不该有致命勾稽问题: %s", iss.Text)
		}
	}
}

// 利润表必须有 32 行，且附列项带 IsMemo 标记
func TestIncomeStatementShape(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	})

	r, rerr := a.Report(ReportRequest{Kind: "pl", Year: 2025, Month: 3})
	if f := faultOf(r, rerr); f != nil {
		t.Fatal(f)
	}
	if len(r.Rows) != 32 {
		t.Errorf("利润表行数 = %d，期望 32", len(r.Rows))
	}
	if len(r.Columns) != 2 {
		t.Errorf("利润表应有「本期金额 / 本年累计」两列，实际 %d", len(r.Columns))
	}
	var memos int
	for _, row := range r.Rows {
		if row.IsMemo {
			memos++
			// 附列项的名称由 Go 侧统一渲染（report.Line.DisplayName），
			// 因此这里**应当**带「其中：」前缀 —— 前端不再自己拼，
			// 免得两边各加一次变成「其中：其中：消费税」。
			if !strings.HasPrefix(row.Label, "其中：") {
				t.Errorf("附列项名称应带「其中：」前缀（由 Go 统一渲染）: %q", row.Label)
			}
		}
	}
	if memos == 0 {
		t.Error("利润表应有「其中」附列项")
	}
}

// 现金流量表 36 行，且第 35、36 行必须等于期初/期末现金
func TestCashFlowShape(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	})

	r, rerr := a.Report(ReportRequest{Kind: "cashflow", Year: 2025, Month: 3})
	if f := faultOf(r, rerr); f != nil {
		t.Fatal(f)
	}
	// 有取数的官方行项目 + 3 个活动分组标题。
	// 归类规则直接映射到小计行，因此明细行不出现（恒为 0 的行只会误导）。
	if len(r.Rows) != 22 {
		t.Errorf("行数 = %d，期望 22（19 个行项目 + 3 个分组标题）", len(r.Rows))
	}
	// 三大活动都必须出现
	for _, name := range []string{"【经营活动】", "【投资活动】", "【筹资活动】"} {
		var found bool
		for _, row := range r.Rows {
			if row.Label == name {
				found = true
			}
		}
		if !found {
			t.Errorf("现金流量表缺少分组 %s", name)
		}
	}
	var opening, closing int64 = -1, -1
	for _, row := range r.Rows {
		switch row.No {
		case "35":
			opening = row.Values[0]
		case "36":
			closing = row.Values[0]
		}
	}
	if opening == -1 || closing == -1 {
		t.Fatal("现金流量表缺少第 35/36 行")
	}
	// ★ 这两行必须真的被填上，不能是 0 —— 曾经这里返回过两张空行
	if opening != 0 || closing != 0 {
		t.Errorf("空账套上期初/期末现金应为 0，实际 %d / %d", opening, closing)
	}
	// 至少确认它们是「行项目」而不是漏掉的占位
	var found bool
	for _, row := range r.Rows {
		if row.No == "36" && row.Bold {
			found = true
		}
	}
	if !found {
		t.Error("第 36 行应作为小计行加粗显示")
	}
}

// ---------------------------------------------------------------------------
// 明细账
// ---------------------------------------------------------------------------

func TestLedgerDetail(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	})

	// 空科目：不该报错，只是没有行
	r, lerr := a.LedgerDetail(LedgerRequest{AccountPrefix: "1002"})
	if f := faultOf(r, lerr); f != nil {
		t.Fatal(f)
	}
	if len(r.Rows) != 0 {
		t.Errorf("空账套不该有明细，实际 %d 行", len(r.Rows))
	}
	if r.OpeningBalance != 0 || r.ClosingBalance != 0 {
		t.Errorf("空账套余额应为 0，实际 %d / %d", r.OpeningBalance, r.ClosingBalance)
	}
	if r.AccountName != "银行存款" {
		t.Errorf("科目名 = %q，期望「银行存款」", r.AccountName)
	}

	// 缺科目前缀
	if _, e := a.LedgerDetail(LedgerRequest{}); faultOf[any](nil, e) == nil ||
		faultOf[any](nil, e).Kind != FaultInvalid {
		t.Errorf("缺少科目前缀应报 FaultInvalid，实际 %v", e)
	}
	// 日期格式
	if _, e := a.LedgerDetail(LedgerRequest{
		AccountPrefix: "1002", From: "2025/01/01",
	}); faultOf[any](nil, e) == nil || faultOf[any](nil, e).Kind != FaultInvalid {
		t.Errorf("非法日期应报 FaultInvalid，实际 %v", e)
	}
}

// ---------------------------------------------------------------------------
// 备份
// ---------------------------------------------------------------------------

func TestBackupRoundTrip(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "备份测试公司", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 1,
	})

	dest := filepath.Join(filepath.Dir(path), "backup.mabak")
	m, berr := a.Backup(BackupRequest{Dest: dest, IncludeFiles: true})
	if f := faultOf(m, berr); f != nil {
		t.Fatalf("备份失败: %v", f)
	}
	if m.CompanyName != "备份测试公司" {
		t.Errorf("备份里的单位名称 = %s", m.CompanyName)
	}
	if m.AccountCount == 0 || m.DBSHA256 == "" {
		t.Errorf("备份摘要不完整: %+v", m)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("备份文件没写出来: %v", err)
	}

	// Inspect 不恢复也能看到内容
	insp, ierr := a.InspectBackup(dest)
	if f := faultOf(insp, ierr); f != nil {
		t.Fatal(f)
	}
	if insp.CompanyName != m.CompanyName || insp.VoucherCount != m.VoucherCount {
		t.Errorf("Inspect 与备份不一致: %+v vs %+v", insp, m)
	}

	// 空路径要给出可操作的提示
	if _, e := a.Backup(BackupRequest{}); faultOf[any](nil, e) == nil ||
		faultOf[any](nil, e).Kind != FaultInvalid {
		t.Errorf("空路径应报 FaultInvalid，实际 %v", e)
	}
}

// ---------------------------------------------------------------------------
// AI
// ---------------------------------------------------------------------------

// 未配置模型服务时，AISuggest 必须返回一个「看起来就是失败」的结果，
// 而不是 panic、也不是一个空的成功。
func TestAISuggestWithoutProvider(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 1,
	})

	res, aerr := a.AISuggest(AISuggestRequest{
		Task: "bank_flow", Text: "收到货款", AmountYuan: "106000.00", Date: "2025-01-15",
	})
	if f := faultOf(res, aerr); f != nil {
		t.Fatalf("未配置服务不该让调用本身失败: %v", f)
	}
	if res.OK {
		t.Fatal("未配置服务时 OK 必须为假")
	}
	if res.Error == "" {
		t.Fatal("必须说明失败原因")
	}

	// 金额格式错误要被挡在调用之前
	if _, e := a.AISuggest(AISuggestRequest{Text: "x", AmountYuan: "1.234"}); faultOf[any](nil, e) == nil ||
		faultOf[any](nil, e).Kind != FaultInvalid {
		t.Errorf("非法金额应报 FaultInvalid，实际 %v", e)
	}
}

func TestAIConfigAndStats(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 1,
	})

	cfg, cerr := a.AIConfig()
	if f := faultOf(cfg, cerr); f != nil {
		t.Fatal(f)
	}
	if cfg.HasDefault {
		t.Error("新账套不该有可用的 AI 服务")
	}

	id, serr := a.SaveAIProvider(sqlite.AIProviderConfig{
		Name: "本地 Ollama", Kind: "local",
		BaseURL: "http://127.0.0.1:11434/v1", Model: "qwen2.5:7b",
		Enabled: true, IsDefault: true,
	})
	if f := faultOf(id, serr); f != nil {
		t.Fatalf("保存服务失败: %v", f)
	}
	if id == 0 {
		t.Fatal("应返回 id")
	}

	cfg, _ = a.AIConfig()
	_ = cfg
	if !cfg.HasDefault || len(cfg.Providers) != 1 {
		t.Errorf("配置读回不一致: %+v", cfg)
	}
}

// ---------------------------------------------------------------------------
// 其余绑定
// ---------------------------------------------------------------------------

func TestSimpleBindings(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)

	if a.Today() == "" {
		t.Error("Today 不该为空")
	}
	if a.AppVersion() == "" {
		t.Error("AppVersion 不该为空")
	}

	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 1,
	})
	dir, derr := a.FilesDir()
	if f := faultOf(dir, derr); f != nil {
		t.Fatal(f)
	}
	if !strings.HasSuffix(dir, ".files") {
		t.Errorf("附件目录 = %s，应以 .files 结尾", dir)
	}
}

// 配置文件里的路径记忆不该影响账套数据
func TestConfigIsSeparateFromBook(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	rememberBookPath("/tmp/some/book.db")
	if got := lastBookPath(); got != "/tmp/some/book.db" {
		t.Errorf("lastBookPath = %q", got)
	}

	// 配置文件必须是合法 JSON —— 损坏的配置会让下次启动静默丢路径
	p, err := configPath()
	if err != nil {
		t.Skipf("该平台没有用户配置目录: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("配置文件没写出来: %v", err)
	}
	var c appConfig
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("配置文件不是合法 JSON: %v", err)
	}
	if c.LastBookPath != "/tmp/some/book.db" {
		t.Errorf("配置内容 = %+v", c)
	}
}

// CloseBook 之后所有方法都应回到 FaultNoBook
func TestCloseBook(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 1,
	})
	if e := a.CloseBook(); e != nil {
		t.Fatalf("关闭账套失败: %v", e)
	}
	if _, e := a.Overview(); faultOf[any](nil, e) == nil ||
		faultOf[any](nil, e).Kind != FaultNoBook {
		t.Errorf("关闭后应报 FaultNoBook，实际 %v", e)
	}
	if e := a.CloseBook(); e != nil {
		t.Errorf("重复关闭应幂等，实际 %v", e)
	}
}

// faultOf 从绑定的 (值, error) 返回值里取出 *Fault。
//
// ★ 绑定层刻意把第二个返回值声明成 **error 接口** 而不是 *Fault：
// 一个 nil 的 *Fault 装箱成 error 之后**不是 nil**，
// Wails 的类型断言会把它当成失败，并在 Error() 里 nil 解引用崩溃。
// 声明成接口后，成功分支返回字面量 nil，接口本身才真的是 nil。
//
// 这个 helper 负责把接口还原成具体类型，供测试做细粒度断言。
func faultOf[T any](_ T, err error) *Fault {
	if err == nil {
		return nil
	}
	if f, ok := err.(*Fault); ok {
		return f
	}
	return &Fault{Kind: FaultInternal, Message: err.Error()}
}

// ---------------------------------------------------------------------------
// ★ 绑定契约：成功时第二个返回值必须是**真正的 nil 接口**
// ---------------------------------------------------------------------------

// TestSuccessfulCallsReturnNilInterface 守住整个桌面端最容易踩的那个坑。
//
// Go 里「接口持有 nil 指针」不等于「接口本身是 nil」：
//
//	var f *Fault = nil
//	var e error = f      // e != nil ！它持有一个类型为 *Fault 的空指针
//
// Wails 的 BoundMethod.Call 对第二个返回值做 `.(error)` 类型断言，
// **只看类型不看值**。断言一旦成功就判定为失败，随后调用 Error()
// 直接 nil 解引用 —— 结果是：**每一次成功的调用都被报告成崩溃**。
//
// 因此绑定层的第二个返回值声明成 error 接口，成功分支返回字面量 nil。
// 这个测试逐个方法验证「成功路径的 error 确实是 nil」，
// 以后任何人把签名改回 *Fault 或写出 `return v, classify(nil)`
// 都会在这里被拦下。
func TestSuccessfulCallsReturnNilInterface(t *testing.T) {
	a, path := newApp(t)
	if _, e := a.OpenBook(path); e != nil {
		t.Fatal(e)
	}
	// 逐个检查：接口本身必须是 nil，而不是「持有 nil 指针的接口」
	type pair struct {
		name string
		e    error
	}
	var checks []pair
	add := func(name string, e error) { checks = append(checks, pair{name, e}) }

	_, e := a.CreateBook(service.CreateBookInput{
		CompanyName: "绑定契约公司", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	})
	add("CreateBook", e)
	_, e = a.CurrentBook()
	add("CurrentBook", e)
	_, e = a.Overview()
	add("Overview", e)
	_, e = a.Periods()
	add("Periods", e)
	_, e = a.Health(PeriodRequest{Year: 2025, Month: 1})
	add("Health", e)
	_, e = a.PreviewClose(PeriodRequest{Year: 2025, Month: 1})
	add("PreviewClose", e)
	_, e = a.Report(ReportRequest{Kind: "bs", Year: 2025, Month: 1})
	add("Report", e)
	_, e = a.LedgerDetail(LedgerRequest{AccountPrefix: "1002"})
	add("LedgerDetail", e)
	_, e = a.FilesDir()
	add("FilesDir", e)
	_, e = a.AIConfig()
	add("AIConfig", e)
	_, e = a.AISuggestions(10)
	add("AISuggestions", e)
	_, e = a.Backup(BackupRequest{Dest: filepath.Join(filepath.Dir(path), "b.mabak"), IncludeFiles: true})
	add("Backup", e)

	for _, c := range checks {
		if c.e != nil {
			t.Errorf("%s 成功时第二个返回值不是 nil 接口（实际 %#v）—— "+
				"Wails 会把它当成失败并 nil 解引用崩溃", c.name, c.e)
		}
	}

	// 收尾：CloseBook 同样必须返回真 nil
	if e := a.CloseBook(); e != nil {
		t.Errorf("CloseBook 成功时应返回 nil 接口，实际 %#v", e)
	}
}

// 失败路径必须返回一个**非 nil 且可解析**的 error，
// 其 Error() 是 JSON 编码的 Fault —— 前端靠它拿到 Kind 与 Detail。
func TestFailureReturnsParseableFault(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)

	_, err := a.Overview() // 未建账 → 应失败
	if err == nil {
		t.Fatal("未建账时应返回错误")
	}

	var f Fault
	if e := json.Unmarshal([]byte(err.Error()), &f); e != nil {
		t.Fatalf("Error() 应是可解析的 JSON（前端靠它拿 Kind）：%v\n原文：%s", e, err.Error())
	}
	if f.Kind != FaultNoBook {
		t.Errorf("Kind = %s，期望 %s", f.Kind, FaultNoBook)
	}
	if f.Message == "" {
		t.Error("Message 不该为空")
	}
}

// Fault.Error() 必须在 nil 接收者上安全。
//
// 这是防「接口持有 nil 指针」二次伤害的最后一道保险：
// 万一将来有人又写出会装箱 nil 的代码，至少不会崩进程。
func TestNilFaultErrorIsSafe(t *testing.T) {
	var f *Fault
	if got := f.Error(); got != "" {
		t.Errorf("nil Fault 的 Error() 应返回空串，实际 %q", got)
	}
}

// ★ 报表请求只给 Year、不给 Month 时，契约是「按年取值」。
//
// 原来这个分支直接把 Year **整个忽略**掉，掉进「当前期间」兜底 ——
// 请求 2024 年会得到 2025-01-31，而且不报任何错。
// 一张期间错的资产负债表看起来完全正常，这是最坏的一类错误。
func TestReportYearOnlyDoesNotSilentlyUseCurrentPeriod(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	if _, f := a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	}); f != nil {
		t.Fatal(f)
	}

	// 只给年份：应取该年 12 月 31 日
	r, f := a.Report(ReportRequest{Kind: "bs", Year: 2024, Month: 0})
	if f != nil {
		t.Fatalf("按年取值不该报错: %v", f)
	}
	if !strings.Contains(r.Subtitle, "2024") {
		t.Errorf("请求 2024 年，返回的报表日期是 %q —— Year 被忽略了", r.Subtitle)
	}
	if strings.Contains(r.Subtitle, "2025") {
		t.Errorf("请求 2024 年，却拿到了 2025 年的报表：%q", r.Subtitle)
	}

	// 明确的年月仍然照常工作
	r2, f := a.Report(ReportRequest{Kind: "bs", Year: 2025, Month: 3})
	if f != nil {
		t.Fatal(f)
	}
	if !strings.Contains(r2.Subtitle, "2025") {
		t.Errorf("按年月取值 = %q", r2.Subtitle)
	}
}

// ★ 「记录不存在」必须报成「找不到」，而不是「内部错误」。
//
// 漏了 sqlite.ErrNotFound 时，点开一张已被删除的凭证会看到
// 「发生内部错误：sqlite: 记录不存在: 凭证 id=5」——
// 用户会以为软件坏了。store 层三十多处都用这个哨兵。
func TestNotFoundIsNotReportedAsInternal(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	if _, f := a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	}); f != nil {
		t.Fatal(f)
	}

	_, e1 := a.VoucherDetail(999999)
	e2 := a.DeleteVoucher(999999)
	cases := []struct {
		name string
		err  error
	}{
		{"VoucherDetail", e1},
		{"DeleteVoucher", e2},
	}
	for _, c := range cases {
		if c.err == nil {
			t.Errorf("%s：不存在的 id 应报错", c.name)
			continue
		}
		f := faultOf[any](nil, c.err)
		if f == nil {
			continue
		}
		if f.Kind == FaultInternal {
			t.Errorf("%s：报成了「内部错误」（%s）—— 应报成「找不到」",
				c.name, f.Message)
		}
	}
}
