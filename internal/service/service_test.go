package service_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/service"
	"miniaccount/internal/store/sqlite"
)

// newSvc 建一个干净的账套。
func newSvc(t *testing.T, months int) *service.Service {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	svc, err := service.Open(ctx, service.Options{
		Path: filepath.Join(dir, "test.db"),
	})
	if err != nil {
		t.Fatalf("打开账套失败: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown() })

	if _, err := svc.CreateBook(ctx, service.CreateBookInput{
		CompanyName: "服务层测试公司", TaxType: "general",
		StartYear: 2025, StartMonth: 1, ThroughYear: 2025,
		CurrentYear: 2025, CurrentMonth: months,
	}); err != nil {
		t.Fatalf("建账失败: %v", err)
	}
	return svc
}

// 未建账时必须给出可识别的错误，界面据此引导用户去建账
func TestOpenBeforeCreateBook(t *testing.T) {
	ctx := context.Background()
	svc, err := service.Open(ctx, service.Options{
		Path: filepath.Join(t.TempDir(), "empty.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Shutdown()

	if _, err := svc.Book(ctx); !errors.Is(err, service.ErrNoBook) {
		t.Fatalf("应报 ErrNoBook，实际 %v", err)
	}
}

func TestCreateBookAndInfo(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	info, err := svc.Book(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.CompanyName != "服务层测试公司" {
		t.Errorf("单位名称 = %s", info.CompanyName)
	}
	if info.TaxTypeLabel != "一般纳税人" {
		t.Errorf("纳税人身份 = %s", info.TaxTypeLabel)
	}
	if len(info.Periods) != 12 {
		t.Fatalf("期间数 = %d，期望 12", len(info.Periods))
	}

	// ★ 顺序结账规则必须由 service 算好，界面只管照画
	// 1—3 月 open，4—12 月 future
	for _, p := range info.Periods {
		switch {
		case p.Month == 1:
			if !p.CanClose {
				t.Error("第 1 月应可结账")
			}
		case p.Month >= 2 && p.Month <= 3:
			if p.CanClose {
				t.Errorf("第 %d 月前面还没结账，不该可结账", p.Month)
			}
		default:
			if p.CanClose {
				t.Errorf("第 %d 月尚未启用，不该可结账", p.Month)
			}
		}
		if p.CanReopen {
			t.Errorf("第 %d 月尚未结账，不该可反结账", p.Month)
		}
	}

	// 重复建账必须被拒绝
	if _, err := svc.CreateBook(ctx, service.CreateBookInput{
		CompanyName: "重复", StartYear: 2025, StartMonth: 1,
	}); err == nil {
		t.Fatal("重复建账应被拒绝")
	}
}

func TestOverviewOnEmptyBook(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	d, err := svc.Overview(ctx)
	if err != nil {
		t.Fatalf("概览失败: %v", err)
	}
	// 空账套上一切为 0，且不能报错
	if !d.Assets.IsZero() || !d.Equity.IsZero() {
		t.Errorf("空账套资产/权益应为 0，实际 %s / %s", d.Assets, d.Equity)
	}
	if len(d.BalanceSheetIssues) != 0 {
		t.Errorf("空账套不该有勾稽问题，实际 %v", d.BalanceSheetIssues)
	}
	// 当前期间 = 最早的 open 期间
	if d.CurrentPeriod != "2025-01" {
		t.Errorf("当前期间 = %s，期望 2025-01", d.CurrentPeriod)
	}
}

// 空期间结账：不生成凭证，但期间要关掉
func TestCloseEmptyPeriod(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	k := period.NewKey(2025, 1)

	pv, err := svc.PreviewClose(ctx, k)
	if err != nil {
		t.Fatalf("预览失败: %v", err)
	}
	if !pv.Health.CanClose {
		t.Fatalf("空账套应可结账：%s", pv.Health.Summary)
	}
	if len(pv.Entries) != 0 {
		t.Errorf("空期间不该有结转分录，实际 %d 条", len(pv.Entries))
	}
	if len(pv.Steps) == 0 {
		t.Error("应给出结账步骤说明")
	}

	res, err := svc.Close(ctx, k, "王主管")
	if err != nil {
		t.Fatalf("结账失败: %v", err)
	}
	if res.VoucherCreated {
		t.Error("空期间不该生成结转凭证")
	}

	// 结账后该期间不可再结，下一期变为可结
	info, _ := svc.Book(ctx)
	for _, p := range info.Periods {
		if p.Month == 1 {
			if p.StatusLabel != "已结账" {
				t.Errorf("1 月状态 = %s", p.StatusLabel)
			}
			if !p.CanReopen {
				t.Error("1 月结账后应可反结账")
			}
		}
		if p.Month == 2 && !p.CanClose {
			t.Error("1 月结完，2 月应可结账")
		}
	}

	// 反结账
	rr, err := svc.Reopen(ctx, k, "王主管")
	if err != nil {
		t.Fatalf("反结账失败: %v", err)
	}
	if rr.Period != "2025-01" {
		t.Errorf("反结账期间 = %s", rr.Period)
	}
	info, _ = svc.Book(ctx)
	for _, p := range info.Periods {
		if p.Month == 1 && p.StatusLabel != "已启用" {
			t.Errorf("反结账后 1 月状态 = %s，期望已启用", p.StatusLabel)
		}
	}
}

// 顺序约束不能绕过：跳过前期直接结账必须失败
func TestCloseOutOfOrder(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	if _, err := svc.Close(ctx, period.NewKey(2025, 3), "王主管"); err == nil {
		t.Fatal("跳过 1、2 月直接结 3 月应失败")
	}
	// 失败不能留下状态改动
	info, _ := svc.Book(ctx)
	for _, p := range info.Periods {
		if p.Month == 3 && p.StatusLabel != "已启用" {
			t.Errorf("失败的结账不该改动状态，3 月 = %s", p.StatusLabel)
		}
	}
}

func TestCloseRequiresPostingBy(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 1)
	if _, err := svc.Close(ctx, period.NewKey(2025, 1), ""); err == nil {
		t.Fatal("缺少结账人应报错")
	}
}

func TestHealthReport(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	h, err := svc.CheckHealth(ctx, period.NewKey(2025, 1))
	if err != nil {
		t.Fatal(err)
	}
	if !h.CanClose {
		t.Errorf("空账套应可结账：%s", h.Summary)
	}
	if len(h.Items) == 0 {
		t.Fatal("体检应至少返回一项")
	}
	for _, it := range h.Items {
		if it.Title == "" {
			t.Errorf("体检项缺少标题：%+v", it)
		}
		switch it.Level {
		case "ok", "warn", "error":
		default:
			t.Errorf("未知的体检级别 %q", it.Level)
		}
	}
	if h.Summary == "" {
		t.Error("应有一句话结论")
	}
}

// ---------------------------------------------------------------------------
// 附件与备份
// ---------------------------------------------------------------------------

func TestFilesDirAndBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	svc, err := service.Open(ctx, service.Options{Path: filepath.Join(dir, "b.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Shutdown()
	if _, err := svc.CreateBook(ctx, service.CreateBookInput{
		CompanyName: "备份测试", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// 附件目录默认落在账套同级
	if got, want := svc.FilesDir(), filepath.Join(dir, ".files"); got != want {
		t.Errorf("附件目录 = %s，期望 %s", got, want)
	}
	st, err := svc.Attachments()
	if err != nil {
		t.Fatalf("打开附件仓库失败: %v", err)
	}
	// 写一个附件进去，验证备份会带上它
	if _, err := st.Put([]byte("%PDF-1.4 假装是一张发票"), "invoice.pdf"); err != nil {
		t.Fatalf("存入附件失败: %v", err)
	}

	dest := filepath.Join(dir, "backup.mabak")
	m, err := svc.Backup(ctx, service.BackupOptions{Dest: dest, IncludeFiles: true})
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if m.CompanyName != "备份测试" {
		t.Errorf("备份里的单位名称 = %s", m.CompanyName)
	}
	if m.FileCount != 1 {
		t.Errorf("附件数 = %d，期望 1", m.FileCount)
	}
	if m.AccountCount == 0 {
		t.Error("备份应记录科目数")
	}
	if m.DBSHA256 == "" {
		t.Error("应记录数据库校验和")
	}

	// Inspect 不恢复也能看到内容
	insp, err := svc.InspectBackup(dest)
	if err != nil {
		t.Fatalf("查看备份失败: %v", err)
	}
	if insp.CompanyName != m.CompanyName || insp.FileCount != m.FileCount {
		t.Errorf("Inspect 结果与备份不一致: %+v vs %+v", insp, m)
	}

	// 恢复到一个新位置
	newDB := filepath.Join(dir, "restored.db")
	newFiles := filepath.Join(dir, "restored.files")
	if _, err := svc.Restore(ctx, service.RestoreOptions{
		Archive: dest, DBPath: newDB, FilesDir: newFiles,
	}); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	restored, err := service.Open(ctx, service.Options{Path: newDB})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Shutdown()
	info, err := restored.Book(ctx)
	if err != nil {
		t.Fatalf("恢复后的账套打不开: %v", err)
	}
	if info.CompanyName != "备份测试" {
		t.Errorf("恢复后单位名称 = %s", info.CompanyName)
	}
}

// AI 未配置时必须优雅降级：返回 Disabled 而不是崩溃或 nil
func TestAISuggestWithoutProvider(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	cfg, err := svc.AIConfigInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HasDefault {
		t.Error("新账套不该有 AI 服务")
	}
	if cfg.Stats.Total != 0 {
		t.Errorf("新账套的 AI 统计应为空，实际 %+v", cfg.Stats)
	}

	res, err := svc.AISuggest(ctx, service.AISuggestInput{
		Text: "收到货款", Amount: money.Money(100000), Date: "2025-01-15",
		TargetType: "freeform",
	})
	if err != nil {
		t.Fatalf("未配置服务不该让调用本身失败: %v", err)
	}
	// ★ 失败通过结果对象表达，而不是 panic 或返回一个「看起来成功」的空建议。
	// 界面据此在建议面板里显示一条错误提示，而不是弹全局错误框。
	if res.OK {
		t.Fatal("未配置模型服务时 OK 必须为假")
	}
	if res.Error == "" {
		t.Fatal("必须说明失败原因")
	}
	if !strings.Contains(res.Error, "未启用") &&
		!strings.Contains(res.Error, "尚未配置") {
		t.Errorf("错误信息应说明原因，实际 %q", res.Error)
	}
	if res.Voucher != nil {
		t.Error("失败时不该带出凭证内容")
	}
}

func TestAIConfigRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	id, err := svc.SaveAIProvider(ctx, &sqlite.AIProviderConfig{
		Name: "本地 Ollama", Kind: "local",
		BaseURL: "http://127.0.0.1:11434/v1", Model: "qwen2.5:7b",
		Enabled: true, IsDefault: true,
	})
	if err != nil {
		t.Fatalf("保存 AI 服务失败: %v", err)
	}
	if id == 0 {
		t.Fatal("应返回 id")
	}
	cfg, err := svc.AIConfigInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HasDefault {
		t.Error("已保存启用的服务，应视为可用")
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Model != "qwen2.5:7b" {
		t.Errorf("配置读回不一致: %+v", cfg.Providers)
	}
}

// mustContact 新增一个往来单位并返回 id。
func mustContact(t *testing.T, svc *service.Service, kind, name string) int64 {
	t.Helper()
	id, err := svc.DB().Contacts().AddContact(context.Background(), kind, name)
	if err != nil {
		t.Fatalf("新增往来单位失败: %v", err)
	}
	return id
}

func money100(yuan int64) money.Money { return money.Money(yuan) * money.Yuan }
