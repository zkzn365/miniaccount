package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"miniaccount/internal/domain/calendar"

	"miniaccount/internal/domain/period"
	"miniaccount/internal/service"
)

// TestEndToEndFullCycle 走一遍小微企业一个完整月份的账务流程。
//
// # 这个测试要证明什么
//
// 单元测试保证「每个零件是对的」，这个测试保证「装起来能用」：
//
//	建账 → 录期初/业务凭证 → 导入银行流水 → 匹配 → 生成凭证
//	     → 工资 → 发票 → 报销 → 出报表 → 结账 → 导出 → 备份 → 恢复
//
// ★ 关键在**每一步之间是连着的**：前面产生的数据必须是后面步骤的输入，
// 而且最后的报表要能对上。任何一处断链，这里就会失败。
//
// 这也是「每阶段可运行可测试」这条要求的落脚点 ——
// 不是一个一个绿着的孤立测试，而是一条能跑通的业务链。
func TestEndToEndFullCycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "公司账.db")

	svc, err := service.Open(ctx, service.Options{Path: dbPath})
	if err != nil {
		t.Fatalf("打开账套失败: %v", err)
	}
	defer svc.Shutdown()

	// ---- 1. 建账 ----
	book, err := svc.CreateBook(ctx, service.CreateBookInput{
		CompanyName: "杭州云帆软件有限公司",
		CreditCode:  "91330100MA2E2ETEST",
		LegalPerson: "张三",
		TaxType:     "general",
		StartYear:   2025, StartMonth: 1, ThroughYear: 2025,
		CurrentYear: 2025, CurrentMonth: 3,
	})
	if err != nil {
		t.Fatalf("建账失败: %v", err)
	}
	if len(book.Periods) != 12 {
		t.Fatalf("会计期间数 = %d，期望 12", len(book.Periods))
	}
	t.Logf("✓ 建账：%s，%d 个会计期间", book.CompanyName, len(book.Periods))

	// ---- 2. 基础档案 ----
	shareholder := mustContact(t, svc, "shareholder", "张三")
	customer := mustContact(t, svc, "customer", "杭州某某科技有限公司")
	supplier := mustContact(t, svc, "supplier", "宁波恒信办公用品")
	dept := mustDepartment(t, svc, "管理部门")

	empID, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Code: "E001", Name: "李会计", BaseSalary: money100(12000),
		SpecialAdditional: money100(2000), DeptID: &dept, Enabled: true,
	})
	if err != nil {
		t.Fatalf("建员工失败: %v", err)
	}
	t.Logf("✓ 基础档案：往来单位 3 个、部门 1 个、员工 1 名")

	// ---- 3. 期初：股东投入 ----
	if _, err := svc.SaveAndPost(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-02", Remark: "收到股东投资款",
		CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			newVoucherLine("1002", "收到投资款", money100(200000), 0),
			{AccountCode: "3001", Summary: "收到投资款",
				Credit: money100(200000), ContactID: &shareholder},
		},
	}, "王主管"); err != nil {
		t.Fatalf("股东投入凭证失败: %v", err)
	}
	t.Logf("✓ 股东投入 200,000.00")

	// ---- 4. 银行流水：导入 → 匹配 → 生成凭证 ----
	csv := "\ufeff交易日期,摘要,对方户名,收入金额,支出金额,余额\n" +
		"2025-02-10,收到货款,杭州某某科技有限公司,106000.00,,106000.00\n" +
		"2025-02-15,支付房租,杭州某某物业,,30000.00,76000.00\n"
	imp, err := svc.ImportBankStatement(ctx, service.BankImportInput{
		AccountCode: "1002", FileName: "对账单.csv",
		Data: []byte(csv), ImportedBy: "李会计",
	})
	if err != nil {
		t.Fatalf("导入银行流水失败: %v", err)
	}
	if imp.Inserted != 2 {
		t.Fatalf("导入条数 = %d，期望 2", imp.Inserted)
	}
	if imp.TotalIn != money100(106000) || imp.TotalOut != money100(30000) {
		t.Errorf("导入汇总有误: 收 %s 支 %s", imp.TotalIn, imp.TotalOut)
	}
	t.Logf("✓ 导入流水 2 条（编码 %s）", imp.Encoding)

	// 重复导入必须被去重
	imp2, err := svc.ImportBankStatement(ctx, service.BankImportInput{
		AccountCode: "1002", FileName: "对账单.csv",
		Data: []byte(csv), ImportedBy: "李会计",
	})
	if err != nil {
		t.Fatal(err)
	}
	if imp2.Inserted != 0 || imp2.Duplicated != 2 {
		t.Errorf("重复导入应全部去重，实际新增 %d 跳过 %d",
			imp2.Inserted, imp2.Duplicated)
	}
	t.Logf("✓ 重复导入被去重（新增 0，跳过 2）")

	// 配规则后匹配
	if _, err := svc.SaveBankRule(ctx, service.BankRuleInput{
		Name: "房租", Pattern: "房租", CounterAccountCode: "560210",
		DeptID: &dept,
	}); err != nil {
		t.Fatalf("建规则失败: %v", err)
	}
	mres, err := svc.MatchBankFlows(ctx)
	if err != nil {
		t.Fatalf("匹配失败: %v", err)
	}
	if mres.Matched == 0 {
		t.Fatal("至少应命中房租那条规则")
	}
	t.Logf("✓ 匹配 %d/%d 条（命中来源 %v）", mres.Matched, mres.Total, mres.ByLayer)

	// 手工给没匹配上的收款指定对方科目，再批量生成凭证
	flows, err := svc.BankFlows(ctx, service.BankFlowQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var matched []int64
	for _, f := range flows {
		if f.Status == "matched" {
			matched = append(matched, f.ID)
		}
	}
	if len(matched) == 0 {
		t.Fatal("应有已匹配的流水")
	}
	postRes, err := svc.PostBankFlows(ctx, matched, "王主管")
	if err != nil {
		t.Fatalf("流水生成凭证失败: %v", err)
	}
	if postRes.Created != len(matched) {
		t.Errorf("生成凭证 %d 张，期望 %d", postRes.Created, len(matched))
	}
	if len(postRes.Failures) > 0 {
		t.Fatalf("有流水生成失败: %v", postRes.Failures)
	}
	t.Logf("✓ 流水生成凭证 %d 张", postRes.Created)

	// ---- 5. 工资 ----
	run, err := svc.BuildPayroll(ctx, service.BuildPayrollInput{
		Year: 2025, Month: 2, CreatedBy: "李会计",
	})
	if err != nil {
		t.Fatalf("生成工资单失败: %v", err)
	}
	if run.TotalIIT <= 0 {
		t.Errorf("月薪 12000 应有个税，实际 %s", run.TotalIIT)
	}
	posted, err := svc.PostPayroll(ctx, run.ID, "王主管")
	if err != nil {
		t.Fatalf("工资记账失败: %v", err)
	}
	if posted.Status != "posted" {
		t.Errorf("工资单状态 = %s", posted.Status)
	}
	t.Logf("✓ 工资：应发 %s，个税 %s，实发 %s",
		posted.TotalGross, posted.TotalIIT, posted.TotalNet)
	_ = empID

	// ---- 6. 发票 ----
	inv, err := svc.SaveInvoice(ctx, service.InvoiceInput{
		Direction: "input", Kind: "special",
		Number: "E2E-INV-001", InvoiceDate: "2025-02-20",
		SellerName: "宁波恒信办公用品", ContactID: &supplier,
		AmountExTax: money100(3000), TaxRatePPM: 130000,
	})
	if err != nil {
		t.Fatalf("录入发票失败: %v", err)
	}
	if inv.DeductibleTax != money100(390) {
		t.Errorf("可抵扣税额 = %s，期望 390.00", inv.DeductibleTax)
	}
	if _, err := svc.PostInvoice(ctx, inv.ID, "王主管", "", &dept); err != nil {
		t.Fatalf("发票生成凭证失败: %v", err)
	}
	t.Logf("✓ 发票：不含税 %s，进项税 %s", inv.AmountExTax, inv.DeductibleTax)

	// ---- 7. 差旅报销 ----
	claim, err := svc.SaveClaim(ctx, service.ClaimInput{
		ClaimantEmployeeID: empID, DeptID: &dept,
		ApplyDate: "2025-02-25", TripStart: "2025-02-22", TripEnd: "2025-02-24",
		Destination: "上海", Reason: "客户拜访",
		Items: []service.ClaimItemInput{
			{Category: "transport", OccurDate: "2025-02-22",
				Summary: "高铁票", Amount: money100(553)},
			{Category: "accommodation", OccurDate: "2025-02-23",
				Summary: "住宿 2 晚", Amount: money100(800)},
		},
	})
	if err != nil {
		t.Fatalf("建报销单失败: %v", err)
	}
	if claim.TotalAmount != money100(1353) {
		t.Errorf("报销合计 = %s，期望 1353.00", claim.TotalAmount)
	}
	if _, err := svc.ApproveClaim(ctx, claim.ID, empID); err != nil {
		t.Fatalf("审批失败: %v", err)
	}
	if _, err := svc.PostClaim(ctx, claim.ID, "王主管"); err != nil {
		t.Fatalf("报销记账失败: %v", err)
	}
	t.Logf("✓ 报销：%s 元，已审批并记账", claim.TotalAmount)

	// ---- 8. 手工补一笔销售 ----
	if _, err := svc.SaveAndPost(ctx, service.VoucherInput{
		Word: "记", Date: "2025-02-28", Remark: "销售软件服务",
		CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			{AccountCode: "1122", Summary: "销售软件服务",
				Debit: money100(106000), ContactID: &customer},
			newVoucherLine("5001", "销售软件服务", 0, money100(100000)),
			newVoucherLine("22210102", "销项税额", 0, money100(6000)),
		},
	}, "王主管"); err != nil {
		t.Fatalf("销售凭证失败: %v", err)
	}

	// ---- 9. 报表：全部要能出来，且勾稽成立 ----
	k := period.NewKey(2025, 2)

	trial, err := svc.DB().Reports().TrialBalanceReport(ctx, k)
	if err != nil {
		t.Fatalf("科目余额表失败: %v", err)
	}
	_, _, td, tc, _, _ := trial.Totals()
	if td != tc {
		t.Errorf("★ 试算不平衡：借 %s ≠ 贷 %s", td, tc)
	}

	asOf := mustDate(t, "2025-02-28")
	_, _, issues, err := svc.DB().Reports().BuildBalanceSheet(ctx, asOf)
	if err != nil {
		t.Fatalf("资产负债表失败: %v", err)
	}
	for _, is := range issues {
		if is.Fatal {
			t.Errorf("★ 资产负债表勾稽不成立: %s", is.String())
		}
	}

	pl, _, plIssues, err := svc.DB().Reports().BuildIncomeStatement(ctx, k)
	if err != nil {
		t.Fatalf("利润表失败: %v", err)
	}
	for _, is := range plIssues {
		if is.Fatal {
			t.Errorf("★ 利润表勾稽不成立: %s", is.String())
		}
	}
	incomeLine, _ := pl.Line(1)
	if incomeLine == nil || !incomeLine.Value.IsPositive() {
		t.Errorf("营业收入应为正，实际 %v", incomeLine)
	}

	cf, err := svc.DB().CashFlow().StatementForPeriod(ctx, k, nil)
	if err != nil {
		t.Fatalf("现金流量表失败: %v", err)
	}
	for _, e := range cf.Check() {
		t.Errorf("★ 现金流量表勾稽不成立: %v", e)
	}
	if cf.ActualClosingCash != nil {
		if d := cf.CashMismatch(); d != 0 {
			t.Errorf("★ 推算期末现金与账面差 %s", d)
		}
	}
	t.Logf("✓ 报表：试算平衡、资产负债表勾稽成立、利润表营业收入 %s、现金流量表期末现金 %s",
		incomeLine.Value, cf.ClosingCash)

	// ---- 10. 附件 ----
	vouchers, err := svc.Vouchers(ctx, service.VoucherQuery{Year: 2025, Month: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(vouchers) == 0 {
		t.Fatal("2 月应有凭证")
	}
	target := vouchers[0].ID
	pdf := []byte("%PDF-1.4\n假的发票内容\n%%EOF\n")
	att, err := svc.AttachToVoucher(ctx, target, "发票.pdf", pdf)
	if err != nil {
		t.Fatalf("挂附件失败: %v", err)
	}
	if len(att.Hash) != 64 {
		t.Errorf("附件哈希应为主 sha256，实际 %q", att.Hash)
	}
	// ★ 附件要能列出来 —— 这是本次修的那个 bug
	list, err := svc.ListAttachments(ctx, "voucher", target)
	if err != nil {
		t.Fatalf("列出附件失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("附件数 = %d，期望 1", len(list))
	}
	if list[0].Missing {
		t.Error("附件文件不该丢失")
	}
	// 附单据数要跟着走
	detail, err := svc.Voucher(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if detail.AttachCount != 1 {
		t.Errorf("附单据数 = %d，期望 1", detail.AttachCount)
	}
	// 重复挂同一份文件是幂等的，不该让附单据数变成 2
	if _, err := svc.AttachToVoucher(ctx, target, "发票.pdf", pdf); err != nil {
		t.Fatal(err)
	}
	detail, _ = svc.Voucher(ctx, target)
	if detail.AttachCount != 1 {
		t.Errorf("重复上传后附单据数 = %d，期望仍为 1", detail.AttachCount)
	}
	t.Logf("✓ 附件：1 个（sha256 %s…）", att.Hash[:12])

	// ---- 11. 结账：必须先结 1 月 ----
	//
	// ★ 顺序结账不是形式：跳过 1 月直接结 2 月，
	// 1 月之后补录的凭证就不会被任何一次结转覆盖到，
	// 账面上会永远差这一块。所以这里也顺带验证这条规则真的挡得住。
	if _, err := svc.Close(ctx, period.NewKey(2025, 2), "王主管"); err == nil {
		t.Fatal("★ 跳过 1 月直接结 2 月必须被拒绝")
	}
	if _, err := svc.Close(ctx, period.NewKey(2025, 1), "王主管"); err != nil {
		t.Fatalf("结 1 月失败: %v", err)
	}

	// ---- 12. 结账前体检 ----
	health, err := svc.CheckHealth(ctx, k)
	if err != nil {
		t.Fatalf("体检失败: %v", err)
	}
	if !health.CanClose {
		t.Fatalf("★ 结账前体检未通过：%s", health.Summary)
	}
	t.Logf("✓ 体检通过：%s", health.Summary)

	// ---- 13. 结账 ----
	closeRes, err := svc.Close(ctx, k, "王主管")
	if err != nil {
		t.Fatalf("结账失败: %v", err)
	}
	if !closeRes.VoucherCreated {
		t.Error("2 月有损益，应生成结转凭证")
	}
	// 结账后损益类科目必须归零
	trial2, _ := svc.DB().Reports().TrialBalanceReport(ctx, k)
	for _, r := range trial2.Rows {
		if strings.HasPrefix(r.AccountCode, "5") && r.IsLeaf {
			if !r.ClosingDebit.IsZero() || !r.ClosingCredit.IsZero() {
				t.Errorf("结转后损益科目 %s 仍有余额：借 %s 贷 %s",
					r.AccountCode, r.ClosingDebit, r.ClosingCredit)
			}
		}
	}
	// 结账后再记账必须被拒绝
	if _, err := svc.SaveAndPost(ctx, service.VoucherInput{
		Word: "记", Date: "2025-02-28", Remark: "结账后补记",
		Lines: []service.VoucherLineInput{
			newVoucherLine("1002", "补记", money100(1), 0),
			newVoucherLine("5001", "补记", 0, money100(1)),
		},
	}, "王主管"); err == nil {
		t.Error("★ 已结账期间必须拒绝记账")
	}
	t.Logf("✓ 结账：结转凭证 %s，损益科目已归零", closeRes.VoucherNo)

	// ---- 14. 导出 Excel ----
	bsFile := filepath.Join(dir, "资产负债表.xlsx")
	expRes, err := svc.ExportExcel(ctx, service.ExportOptions{
		Kind: service.ExportBalanceSheet, Dest: bsFile, Year: 2025, Month: 2,
	})
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if fi, err := os.Stat(expRes.Path); err != nil || fi.Size() < 3000 {
		t.Fatalf("导出的文件有问题: %v", err)
	}
	t.Logf("✓ 导出：%s（%d 行）", filepath.Base(expRes.Path), expRes.Rows)

	// ---- 15. 备份为单个文件 ----
	bakFile := filepath.Join(dir, "备份.mabak")
	manifest, err := svc.Backup(ctx, service.BackupOptions{
		Dest: bakFile, IncludeFiles: true,
	})
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if manifest.CompanyName != "杭州云帆软件有限公司" {
		t.Errorf("备份里的单位名称 = %s", manifest.CompanyName)
	}
	if manifest.FileCount != 1 {
		t.Errorf("备份应带上 1 个附件，实际 %d", manifest.FileCount)
	}
	if manifest.VoucherCount == 0 {
		t.Error("备份应记录凭证数")
	}
	t.Logf("✓ 备份：%d 张凭证、%d 个附件、%d 字节",
		manifest.VoucherCount, manifest.FileCount, manifest.DBSize)

	// ---- 16. 恢复：恢复到新位置并验证数据一致 ----
	restoreDB := filepath.Join(dir, "恢复的账.db")
	restoreFiles := filepath.Join(dir, "恢复的账.files")
	rr, err := svc.Restore(ctx, service.RestoreOptions{
		Archive: bakFile, DBPath: restoreDB, FilesDir: restoreFiles,
	})
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if rr.FilesRestored != 1 {
		t.Errorf("恢复的附件数 = %d，期望 1", rr.FilesRestored)
	}

	restored, err := service.Open(ctx, service.Options{Path: restoreDB})
	if err != nil {
		t.Fatalf("打开恢复的账套失败: %v", err)
	}
	defer restored.Shutdown()

	rb, err := restored.Book(ctx)
	if err != nil {
		t.Fatalf("恢复的账套读不出信息: %v", err)
	}
	if rb.CompanyName != "杭州云帆软件有限公司" {
		t.Errorf("恢复后单位名称 = %s", rb.CompanyName)
	}
	// 期间状态也要恢复：2 月应是已结账
	var febStatus string
	for _, p := range rb.Periods {
		if p.Month == 2 {
			febStatus = p.StatusLabel
		}
	}
	if febStatus != "已结账" {
		t.Errorf("恢复后 2 月状态 = %s，期望「已结账」", febStatus)
	}
	// 凭证数要一致。★ 这里必须**重新查一次原账套**再比：
	// 上面那个 vouchers 是结账之前取的，结账又生成了一张结转凭证 ——
	// 拿一个过期快照当基准，比出来的差异是假的。
	nowVouchers, err := svc.Vouchers(ctx, service.VoucherQuery{Year: 2025, Month: 2})
	if err != nil {
		t.Fatal(err)
	}
	rv, err := restored.Vouchers(ctx, service.VoucherQuery{Year: 2025, Month: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rv) != len(nowVouchers) {
		t.Errorf("恢复后凭证数 = %d，原为 %d", len(rv), len(nowVouchers))
	}
	// 附件也要能读出来
	rl, err := restored.ListAttachments(ctx, "voucher", target)
	if err != nil {
		t.Fatal(err)
	}
	if len(rl) != 1 || rl[0].Missing {
		t.Errorf("恢复后附件不完整: %+v", rl)
	}
	t.Logf("✓ 恢复：单位、期间状态、%d 张凭证、附件全部一致", len(rv))

	// ---- 收尾：账套文件确实落盘了 ----
	if fi, err := os.Stat(dbPath); err != nil || fi.Size() == 0 {
		t.Fatalf("账套文件不存在或为空: %v", err)
	}
	t.Logf("✓ 全流程通过")
}

// TestEndToEndOpeningBalances 验证「期初余额 → 试算平衡 → 报表」这条线。
//
// 这是新用户上手的第一件事：把手工账的余额录进来。
// 录不平就出不了报表，因此期初平衡校验必须真的挡住。
func TestEndToEndOpeningBalances(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	// 一张平衡的期初凭证：银行存款 + 库存现金 = 实收资本
	shareholder := mustContact(t, svc, "shareholder", "张三")
	if _, err := svc.SaveAndPost(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-01", Remark: "期初余额",
		CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			newVoucherLine("1002", "期初余额", money100(150000), 0),
			newVoucherLine("1001", "期初余额", money100(5000), 0),
			{AccountCode: "3001", Summary: "期初余额",
				Credit: money100(155000), ContactID: &shareholder},
		},
	}, "王主管"); err != nil {
		t.Fatalf("期初凭证失败: %v", err)
	}

	// 不平衡的期初必须被拒绝
	if _, err := svc.SaveAndPost(ctx, service.VoucherInput{
		Word: "记", Date: "2025-01-01", Remark: "不平衡的期初",
		Lines: []service.VoucherLineInput{
			newVoucherLine("1002", "期初", money100(1000), 0),
			newVoucherLine("5001", "期初", 0, money100(999)),
		},
	}, "王主管"); err == nil {
		t.Error("★ 借贷不平的期初必须被拒绝")
	}

	asOf := mustDate(t, "2025-01-31")
	bs, _, issues, err := svc.DB().Reports().BuildBalanceSheet(ctx, asOf)
	if err != nil {
		t.Fatal(err)
	}
	for _, is := range issues {
		if is.Fatal {
			t.Errorf("期初录入后资产负债表勾稽不成立: %s", is.String())
		}
	}
	assets, _ := bs.Line(30) // 行30 资产总计
	if assets == nil || assets.Value != money100(155000) {
		t.Errorf("资产总计 = %v，期望 155000.00", assets)
	}
}

// mustDate 解析一个纯日历日期（本包用的是 calendar.Date，无时区）。
func mustDate(t *testing.T, s string) calendar.Date {
	t.Helper()
	d, err := calendar.Parse(s)
	if err != nil {
		t.Fatalf("日期 %q 无法解析: %v", s, err)
	}
	return d
}
