package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/service"
)

// withAudit 把日志目录指到临时目录，并返回读取入口。
//
// ★ 每个测试都要指一次：日志是进程级共享的，不隔离的话
// 两个测试会往同一个文件里写，断言就互相污染了。
func withAudit(t *testing.T) *service.Service {
	t.Helper()
	// ★ 连 HOME 一起隔离：日志设置在 ~/.mini-account/settings.json，
	// 测试绝不能去动用户真实的那个文件（受限环境下也写不进去）。
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "logs")
	service.SetAuditDir(dir)
	t.Cleanup(func() {
		service.CloseAudit()
		service.SetAuditDir("")
	})
	return newSvc(t, 3)
}

func findAudit(t *testing.T, svc *service.Service, action audit.Action) []audit.Entry {
	t.Helper()
	page, err := svc.QueryAudit(context.Background(), service.AuditQuery{
		Actions: []string{string(action)},
	})
	if err != nil {
		t.Fatalf("查日志失败: %v", err)
	}
	return page.Entries
}

// ★ 规范要求：「凭证据录入、修改、删除、过账」都要记，且修改要记前后内容。
func TestAuditRecordsVoucherLifecycle(t *testing.T) {
	svc := withAudit(t)
	ctx := context.Background()

	// 录入
	d, err := svc.SaveVoucher(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-10", Remark: "收到货款", CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			{AccountCode: "1002", Summary: "收到货款", Debit: 100000},
			{AccountCode: "5001", Summary: "收到货款", Credit: 100000},
		},
	})
	if err != nil {
		t.Fatalf("录入凭证失败: %v", err)
	}

	created := findAudit(t, svc, audit.ActionVoucherCreate)
	if len(created) != 1 {
		t.Fatalf("「录入凭证」的日志 = %d 条，期望 1", len(created))
	}
	e := created[0]
	if e.Operator != "李会计" {
		t.Errorf("操作人 = %q，期望「李会计」—— 规范要求日志必须能回答「谁做的」", e.Operator)
	}
	if e.At == "" || len(e.At) < 19 {
		t.Errorf("时间要精确到分秒，实际 %q", e.At)
	}
	if e.Entity != "voucher" || e.EntityID == "" {
		t.Errorf("没有记下操作对象：%+v", e)
	}
	if !strings.Contains(e.Summary, "录入") {
		t.Errorf("摘要应当是人话：%q", e.Summary)
	}
	// 修改前后的内容：录入时至少要有「改成了什么」
	if _, ok := e.Detail["借方合计"]; !ok {
		t.Errorf("明细里应当有金额：%+v", e.Detail)
	}

	// 修改：必须留下**修改前**的内容
	if _, err := svc.SaveVoucher(ctx, service.VoucherInput{
		ID: d.ID, Word: "记", Date: "2025-01-11", Remark: "收到货款（改）",
		CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			{AccountCode: "1002", Summary: "收到货款", Debit: 200000},
			{AccountCode: "5001", Summary: "收到货款", Credit: 200000},
		},
	}); err != nil {
		t.Fatalf("修改凭证失败: %v", err)
	}
	updated := findAudit(t, svc, audit.ActionVoucherUpdate)
	if len(updated) != 1 {
		t.Fatalf("「修改凭证」的日志 = %d 条，期望 1", len(updated))
	}
	if _, ok := updated[0].Detail["修改前"]; !ok {
		t.Errorf("★ 修改凭证没有记下修改前的内容 —— 规范明确要求记「修改前后的内容」：%+v",
			updated[0].Detail)
	}

	// 过账
	if _, err := svc.PostVoucher(ctx, d.ID, "王主管"); err != nil {
		t.Fatalf("过账失败: %v", err)
	}
	posted := findAudit(t, svc, audit.ActionVoucherPost)
	if len(posted) != 1 {
		t.Fatalf("「凭证过账」的日志 = %d 条，期望 1", len(posted))
	}
	if posted[0].Operator != "王主管" {
		t.Errorf("过账日志的操作人 = %q，期望「王主管」", posted[0].Operator)
	}
}

// 删除草稿：日志要留下这张凭证**存在过**的痕迹。
func TestAuditRecordsVoucherDeletion(t *testing.T) {
	svc := withAudit(t)
	ctx := context.Background()
	d, err := svc.SaveVoucher(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-10", Remark: "待删", CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			{AccountCode: "1002", Summary: "待删", Debit: 50000},
			{AccountCode: "5001", Summary: "待删", Credit: 50000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVoucher(ctx, d.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	got := findAudit(t, svc, audit.ActionVoucherDelete)
	if len(got) != 1 {
		t.Fatalf("「删除凭证」的日志 = %d 条，期望 1", len(got))
	}
	lines, ok := got[0].Detail["被删除的分录"]
	if !ok {
		t.Fatalf("★ 删除的凭证内容没有记下来 —— 草稿删了就没了，日志是它存在过的唯一证据：%+v",
			got[0].Detail)
	}
	if len(lines.([]any)) != 2 {
		t.Errorf("被删除的分录应当有 2 条：%+v", lines)
	}
}

// ★ 规范点名：「重新开启已结账期间」要记**期间的起止日期**。
func TestAuditRecordsPeriodReopenWithDates(t *testing.T) {
	svc := withAudit(t)
	ctx := context.Background()
	k := periodKey(2025, 1)

	// 先造一张凭证并结账
	if _, err := svc.SaveAndPost(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-10", Remark: "销售", CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			{AccountCode: "1002", Summary: "销售", Debit: 100000},
			{AccountCode: "5001", Summary: "销售", Credit: 100000},
		},
	}, "王主管"); err != nil {
		t.Fatalf("记账失败: %v", err)
	}
	if _, err := svc.Close(ctx, k, "王主管"); err != nil {
		t.Fatalf("结账失败: %v", err)
	}
	closed := findAudit(t, svc, audit.ActionPeriodClose)
	if len(closed) != 1 {
		t.Fatalf("「结账」的日志 = %d 条，期望 1", len(closed))
	}
	if closed[0].Detail["起"] != "2025-01-01" || closed[0].Detail["止"] != "2025-01-31" {
		t.Errorf("结账日志应当记下期间起止：%+v", closed[0].Detail)
	}

	if _, err := svc.Reopen(ctx, k, "王主管"); err != nil {
		t.Fatalf("反结账失败: %v", err)
	}
	reopened := findAudit(t, svc, audit.ActionPeriodReopen)
	if len(reopened) != 1 {
		t.Fatalf("「反结账」的日志 = %d 条，期望 1", len(reopened))
	}
	if reopened[0].Detail["起"] != "2025-01-01" || reopened[0].Detail["止"] != "2025-01-31" {
		t.Errorf("★ 重开期间的日志必须记下期间的起止日期（规范明确要求）：%+v",
			reopened[0].Detail)
	}
}

// 基础数据维护也要留痕（规范点名的：科目表、往来单位、人员信息）。
func TestAuditRecordsBaseData(t *testing.T) {
	svc := withAudit(t)
	ctx := context.Background()
	if _, err := svc.SaveContact(ctx, service.ContactInput{
		Kind: "customer", Name: "杭州云帆科技有限公司", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	got := findAudit(t, svc, audit.ActionContactSave)
	if len(got) != 1 {
		t.Fatalf("「维护往来单位」的日志 = %d 条，期望 1", len(got))
	}
	if !strings.Contains(got[0].Summary, "杭州云帆科技有限公司") {
		t.Errorf("摘要里要有对象名称：%q", got[0].Summary)
	}
	if got[0].Detail["类型"] != "customer" {
		t.Errorf("明细里要有属性：%+v", got[0].Detail)
	}
}

// 失败的操作也要能查到 —— 用户报「报了个错」时靠它找线索。
func TestAuditRecordsFailures(t *testing.T) {
	svc := withAudit(t)
	ctx := context.Background()

	// 借贷不平的凭证会被拦下
	if _, err := svc.SaveVoucher(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-10", Remark: "不平", CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			{AccountCode: "1002", Summary: "不平", Debit: 100000},
			{AccountCode: "4103", Summary: "不平", Credit: 90000},
		},
	}); err == nil {
		t.Fatal("借贷不平应当被拒绝")
	}

	// service 层的失败不写业务日志（业务日志只记「做了什么」），
	// 但桌面绑定层的失败会写 —— 这里直接调 RecordFailure 验证那条通路
	svc.RecordFailure(ctx, service.AuditEvent{
		Action:  audit.ActionOperationFailed,
		Summary: "操作失败：PostVoucher",
		Result:  audit.ResultFailed, Message: "借贷不平衡",
		Entity: "binding", EntityID: "PostVoucher",
	})
	got := findAudit(t, svc, audit.ActionOperationFailed)
	if len(got) != 1 {
		t.Fatalf("失败日志 = %d 条，期望 1", len(got))
	}
	if got[0].Result != audit.ResultFailed {
		t.Errorf("结果 = %q，期望 failed", got[0].Result)
	}
	if !strings.Contains(got[0].Message, "借贷不平衡") {
		t.Errorf("失败原因要记下来：%q", got[0].Message)
	}
}

// 跨账套：日志要能分清是哪本账的操作。
func TestAuditRecordsWhichBook(t *testing.T) {
	svc := withAudit(t)
	ctx := context.Background()
	if _, err := svc.SaveContact(ctx, service.ContactInput{
		Kind: "customer", Name: "某客户", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	got := findAudit(t, svc, audit.ActionContactSave)
	if len(got) != 1 {
		t.Fatal("没有日志")
	}
	if got[0].Book == "" {
		t.Error("★ 没有记下账套路径 —— 日志是跨账套的，不记这个分不清哪本账被动了")
	}
	if got[0].Company == "" {
		t.Error("没有记下单位名称")
	}
}

// 导出：CSV 能被 Excel 打开，且公式注入被挡住。
func TestExportAuditCSV(t *testing.T) {
	svc := withAudit(t)
	ctx := context.Background()
	// 摘要里塞一个以 = 开头的内容：Excel 会把它当公式执行
	svc.RecordFailure(ctx, service.AuditEvent{
		Action:  audit.ActionOperationFailed,
		Summary: `=cmd|'/c calc'!A1`, Message: "测试",
		Result: audit.ResultFailed,
	})
	dest := filepath.Join(t.TempDir(), "log.csv")
	n, err := svc.ExportAudit(ctx, service.AuditQuery{}, dest)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if n == 0 {
		t.Fatal("导出了 0 条")
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.HasPrefix(s, "\ufeff") {
		t.Error("CSV 应当带 UTF-8 BOM，否则 Excel 打开是乱码")
	}
	if strings.Contains(s, `"=cmd`) || strings.Contains(s, ",=cmd") {
		t.Errorf("★ 以 = 开头的内容没有被转义，Excel 会把它当公式执行：%s", s)
	}
	if !strings.Contains(s, "'=cmd") {
		t.Errorf("应当用前置单引号中和公式：%s", s)
	}
}

// 链的完整性：界面上的「校验」按钮走的就是这条。
func TestVerifyAuditFromService(t *testing.T) {
	svc := withAudit(t)
	ctx := context.Background()
	if _, err := svc.SaveContact(ctx, service.ContactInput{
		Kind: "customer", Name: "某客户", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.VerifyAudit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Errorf("刚写完的日志应当完好：%+v", res.Issues)
	}
	if res.Checked == 0 {
		t.Error("校验了 0 条 —— 说明日志根本没写进去")
	}
}

func periodKey(year, month int) period.Key { return period.NewKey(year, month) }

// 金额工具：确认测试里用的分与展示一致（防止把「元」当「分」写进断言）
func TestAuditMoneyUnitSanity(t *testing.T) {
	if money.Money(100000).String() != "1,000.00" {
		t.Fatalf("金额显示口径变了：%s", money.Money(100000).String())
	}
}

// ★ 查看类日志：默认**不记**，打开开关才记。
//
// 规范要审计的是业务操作；查看类一天好几百条，默认开着会把要看的淹掉。
func TestViewLoggingIsOptIn(t *testing.T) {
	svc := withAudit(t)
	ctx := context.Background()
	service.ResetViewDedup()
	// 默认设置：不记
	if service.LogSettingsOf().RecordViews {
		t.Fatal("默认应当是「不记查看操作」")
	}

	if _, err := svc.Summary(ctx, service.SummaryRequest{From: "2025-01-01", To: "2025-01-31"}); err != nil {
		t.Fatalf("查汇总表失败: %v", err)
	}
	if got := findAudit(t, svc, audit.ActionViewSummary); len(got) != 0 {
		t.Fatalf("★ 开关没开却记了查看日志：%+v", got)
	}

	// 打开开关
	cur := service.LogSettingsOf()
	cur.RecordViews = true
	if err := service.SaveLogSettings(cur); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	t.Cleanup(func() { _ = service.SaveLogSettings(service.DefaultLogSettings()) })
	service.ResetViewDedup()

	if _, err := svc.Summary(ctx, service.SummaryRequest{From: "2025-01-01", To: "2025-01-31"}); err != nil {
		t.Fatal(err)
	}
	got := findAudit(t, svc, audit.ActionViewSummary)
	if len(got) != 1 {
		t.Fatalf("开关打开后应当记一条，实际 %d 条", len(got))
	}
	if got[0].Category != audit.CategoryRead {
		t.Errorf("类别 = %q，期望 read", got[0].Category)
	}
	if got[0].Operator == "" {
		t.Error("查看日志也要有操作人")
	}
	// 类别过滤：只查业务操作时，查看日志不该出现
	page, err := svc.QueryAudit(ctx, service.AuditQuery{Categories: []string{"business"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range page.Entries {
		if e.Category == audit.CategoryRead {
			t.Errorf("★ 按「业务操作」筛选却查出了查看日志：%+v", e)
		}
	}
}

// ★ 同一对象重复查看要去重 —— 不然界面刷新一次就写一条。
func TestViewLoggingDeduplicates(t *testing.T) {
	svc := withAudit(t)
	ctx := context.Background()
	cur := service.LogSettingsOf()
	cur.RecordViews = true
	cur.ViewIntervalSeconds = 60
	if err := service.SaveLogSettings(cur); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.SaveLogSettings(service.DefaultLogSettings()) })
	service.ResetViewDedup()

	for i := 0; i < 10; i++ {
		if _, err := svc.Summary(ctx, service.SummaryRequest{From: "2025-01-01", To: "2025-01-31"}); err != nil {
			t.Fatal(err)
		}
	}
	got := findAudit(t, svc, audit.ActionViewSummary)
	if len(got) != 1 {
		t.Errorf("★ 连查 10 次记了 %d 条 —— 去重没生效，日志会被刷新淹没", len(got))
	}

	// 换一个对象要能记上（去重是按「操作 + 对象」）
	if _, err := svc.Summary(ctx, service.SummaryRequest{From: "2025-02-01", To: "2025-02-28"}); err != nil {
		t.Fatal(err)
	}
	if got := findAudit(t, svc, audit.ActionViewSummary); len(got) != 2 {
		t.Errorf("换了期间应当再记一条，实际 %d 条", len(got))
	}
}

// 业务操作与查看日志能分开导出（审计只导出业务那部分）。
func TestExportSeparatesCategories(t *testing.T) {
	svc := withAudit(t)
	ctx := context.Background()
	cur := service.LogSettingsOf()
	cur.RecordViews = true
	_ = service.SaveLogSettings(cur)
	t.Cleanup(func() { _ = service.SaveLogSettings(service.DefaultLogSettings()) })
	service.ResetViewDedup()

	if _, err := svc.SaveContact(ctx, service.ContactInput{
		Kind: "customer", Name: "某客户", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Summary(ctx, service.SummaryRequest{From: "2025-01-01", To: "2025-01-31"}); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "business.csv")
	n, err := svc.ExportAudit(ctx, service.AuditQuery{Categories: []string{"business"}}, dest)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dest)
	s := string(b)
	if !strings.Contains(s, "维护往来单位") {
		t.Error("业务日志没导出")
	}
	if strings.Contains(s, "查看凭证汇总表") {
		t.Errorf("★ 只导出业务操作时把查看日志也带上了（导出 %d 条）", n)
	}
}

// ★ 记账人是**账套级**的：同一台电脑给两家公司做账，签的名可以不同。
func TestBookkeeperIsPerBook(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	service.ResetSettingsCache()
	t.Cleanup(service.ResetSettingsCache)

	ctx := context.Background()
	a := newSvc(t, 3)
	name, err := a.Bookkeeper(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if name != "" {
		t.Errorf("新账套应当是空的，实际 %q —— 程序不能预设一个名字", name)
	}

	if err := a.SetBookkeeper(ctx, "  李会计  "); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if got, _ := a.Bookkeeper(ctx); got != "李会计" {
		t.Errorf("读回来 = %q，期望「李会计」（两边空白要去掉）", got)
	}

	// 换一本账：它有自己的记账人，不该看到上一本的
	service.ResetBookkeeperCache()
	b := newSvc(t, 3)
	if got, _ := b.Bookkeeper(ctx); got != "" {
		t.Errorf("★ 另一本账读到了 %q —— 记账人串账套了", got)
	}
	if err := b.SetBookkeeper(ctx, "王主管"); err != nil {
		t.Fatal(err)
	}
	// 再切回来，A 的还在
	service.ResetBookkeeperCache()
	if got, _ := a.Bookkeeper(ctx); got != "李会计" {
		t.Errorf("切回第一本账后 = %q，期望「李会计」", got)
	}
	if got, _ := b.Bookkeeper(ctx); got != "王主管" {
		t.Errorf("第二本账 = %q，期望「王主管」", got)
	}

	// 清空
	if err := a.SetBookkeeper(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Bookkeeper(ctx); got != "" {
		t.Errorf("清空后 = %q", got)
	}
}

// 记账人校验：超长与带换行的要拦下来。
func TestBookkeeperValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	svc := newSvc(t, 3)
	ctx := context.Background()

	if err := svc.SetBookkeeper(ctx, strings.Repeat("字", 21)); err == nil {
		t.Error("超长应当被拒绝")
	}
	if err := svc.SetBookkeeper(ctx, "李会计\n王主管"); err == nil {
		t.Error("带换行的应当被拒绝（凭证签章上会串行）")
	}
	if err := svc.SetBookkeeper(ctx, "李小二"); err != nil {
		t.Errorf("正常名字不该报错：%v", err)
	}
}

// ★ 记账人进账套、日志设置留在本机 —— 两者互不覆盖。
func TestBookkeeperInBookAndLogSettingsOnMachine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	service.ResetSettingsCache()
	t.Cleanup(service.ResetSettingsCache)

	ctx := context.Background()
	svc := newSvc(t, 3)
	if err := svc.SetBookkeeper(ctx, "李会计"); err != nil {
		t.Fatal(err)
	}
	cur := service.LogSettingsOf()
	cur.RecordViews = true
	if err := service.SaveLogSettings(cur); err != nil {
		t.Fatal(err)
	}

	// 本机设置文件里**只有**日志设置，没有记账人
	b, err := os.ReadFile(filepath.Join(home, ".mini-account", "settings.json"))
	if err != nil {
		t.Fatalf("设置文件没写出来：%v", err)
	}
	if strings.Contains(string(b), "李会计") {
		t.Errorf("★ 记账人被写进了本机配置 —— 它该在账套里，否则换本账签的还是同一个名：%s", b)
	}
	if !strings.Contains(string(b), "recordViews") {
		t.Errorf("日志设置丢了：%s", b)
	}

	// 重启后两边都在
	service.ResetSettingsCache()
	if got, _ := svc.Bookkeeper(ctx); got != "李会计" {
		t.Errorf("重启后账套里的记账人丢了：%q", got)
	}
	if !service.LogSettingsOf().RecordViews {
		t.Error("重启后日志设置丢了")
	}
}

// ★ 老版本留在本机配置里的记账人，要并入账套并把老字段清掉。
//
// 不搬的话用户打过的名字就白打了；不清的话他会翻到那个字段，
// 以为「记账人还是存在本机」。
func TestLegacyBookkeeperMigratesIntoBook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	service.ResetSettingsCache()
	t.Cleanup(service.ResetSettingsCache)

	// 造一个老版设置文件
	dir := filepath.Join(home, ".mini-account")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"bookkeeper":"李会计","audit":{"recordViews":true,"viewIntervalSeconds":60}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	svc := newSvc(t, 3)
	svc.MigrateLegacyBookkeeper(ctx)

	// ① 名字进了账套
	if got, _ := svc.Bookkeeper(ctx); got != "李会计" {
		t.Errorf("老值没有并入账套：%q", got)
	}
	// ② 本机文件里的老字段没了，日志设置还在
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "bookkeeper") {
		t.Errorf("★ 本机文件里还留着 bookkeeper 字段：%s", b)
	}
	if !strings.Contains(string(b), "recordViews") {
		t.Errorf("不该顺手把日志设置也删了：%s", b)
	}
}

// 账套里已经有名字时，老值不能覆盖它（那是别的账套的人）。
func TestLegacyBookkeeperDoesNotOverwrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	service.ResetSettingsCache()
	t.Cleanup(service.ResetSettingsCache)

	dir := filepath.Join(home, ".mini-account")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"bookkeeper":"老名字"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	svc := newSvc(t, 3)
	if err := svc.SetBookkeeper(ctx, "王主管"); err != nil {
		t.Fatal(err)
	}
	svc.MigrateLegacyBookkeeper(ctx)

	if got, _ := svc.Bookkeeper(ctx); got != "王主管" {
		t.Errorf("★ 账套里已有的记账人被本机老值覆盖成了 %q", got)
	}
}

// ★ 「跟着账套走」的真正含义：备份 → 恢复到另一台机器，签章人还在。
//
// 这条是账套级存储的核心价值。存在本机配置里的话，恢复出来的账套
// 在本机上会显示旧的签章人、在别人电脑上则是空的。
func TestBookkeeperTravelsWithBackup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	service.ResetSettingsCache()
	t.Cleanup(service.ResetSettingsCache)

	ctx := context.Background()
	svc := newSvc(t, 3)
	if err := svc.SetBookkeeper(ctx, "李会计"); err != nil {
		t.Fatal(err)
	}

	// 备份
	dir := t.TempDir()
	arc := filepath.Join(dir, "b.mabak")
	if _, err := svc.Backup(ctx, service.BackupOptions{
		Dest: arc, IncludeFiles: false,
	}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 恢复到「另一台机器」（全新的路径与附件目录）
	restoreDB := filepath.Join(t.TempDir(), "restored.db")
	restoreFiles := filepath.Join(t.TempDir(), "restored.files")
	if _, err := svc.Restore(ctx, service.RestoreOptions{
		Archive: arc, DBPath: restoreDB, FilesDir: restoreFiles,
	}); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}

	restored, err := service.Open(ctx, service.Options{Path: restoreDB})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Shutdown() }()

	got, err := restored.Bookkeeper(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "李会计" {
		t.Errorf("★ 恢复出来的账套里记账人 = %q，期望「李会计」—— 它没有跟着账套走", got)
	}
}
