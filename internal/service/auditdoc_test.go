package service_test

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 审计与鉴证文书
// ---------------------------------------------------------------------------
//
// 这一组测试盯两件事：
//
//  1. **事实**来自账套与底稿（不是抄模板）；
//  2. **意见与结论**不许软件替人下 —— 该拦的矛盾一定要拦。

// 建一个「有底稿、有报表」的账套：一笔销售 + 一笔费用。
func seedDocBook(t *testing.T, svc *service.Service) {
	t.Helper()
	mustPost(t, svc, "2025-03-31", "销售收入",
		service.VoucherLineInput{AccountCode: "1122", Summary: "销售", Debit: money100(113_000),
			ContactID: wpPtr(mustContact(t, svc, "customer", "甲公司"))},
		service.VoucherLineInput{AccountCode: "5001", Summary: "销售", Credit: money100(100_000)},
		service.VoucherLineInput{AccountCode: "22210102", Summary: "销项税额", Credit: money100(13_000)})
	mustPost(t, svc, "2025-03-31", "结转成本",
		service.VoucherLineInput{AccountCode: "5401", Summary: "成本", Debit: money100(60_000)},
		service.VoucherLineInput{AccountCode: "1405", Summary: "成本", Credit: money100(60_000)})
}

// 审计报告：报表数字来自账套，底稿的重要性与错报来自底稿。
func TestAuditDocPullsFactsFromBooks(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedDocBook(t, svc)
	seedMateriality(t, svc, 2025, 3)
	// ★ 报告里要写「我们相信已获取充分、适当的审计证据」，
	// 所以证据链不能是空的：给两项结论各挂一份依据
	periodID := int64(2025*100 + 3)
	for _, owner := range []string{"materiality", "conclusion"} {
		if _, err := svc.AddEvidence(ctx, service.EvidenceInput{
			OwnerType: owner, OwnerID: periodID, RefKind: "external",
			RefLabel: "上年审计报告与本期访谈记录", By: "李审计",
		}); err != nil {
			t.Fatalf("挂依据失败(%s): %v", owner, err)
		}
	}

	v, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "audit", Year: 2025, Month: 3, Opinion: "unqualified",
		FirmName: "某某会计师事务所", ReportNo: "某会审字〔2026〕第 1 号",
		CPA1: "张三", CPA2: "李四", ReportDate: "2026-03-31",
	})
	if err != nil {
		t.Fatalf("生成审计报告失败: %v", err)
	}
	if !v.Draft || v.Submittable {
		t.Error("★ 软件产出的只能是草稿，且不能标记为可直接出具")
	}
	if v.OpinionStr != "无保留意见" {
		t.Errorf("意见 = %q", v.OpinionStr)
	}
	// 报表数字要真的来自账套
	main := sectionByName(t, v, "已审财务报表主要项目")
	if !strings.Contains(main, "资产总额") {
		t.Errorf("主要项目段没有资产总额：%q", main)
	}
	if strings.Contains(main, "资产总额 0.00") {
		t.Errorf("★ 资产总额取数为 0 —— 报表取数断了：%q", main)
	}
	// 重要性水平要写进「形成意见的基础」
	basis := sectionByName(t, v, "形成无保留意见的基础")
	if !strings.Contains(basis, "整体重要性") || !strings.Contains(basis, "50,000.00") {
		t.Errorf("★ 基础段没有写重要性水平：%q", basis)
	}
	// 每段都要有来源
	for _, s := range v.Sections {
		if strings.TrimSpace(s.Source) == "" {
			t.Errorf("段落「%s」没有写来源", s.Title)
		}
	}
	if !v.CanIssue {
		t.Fatalf("这份稿子应当可以签发，实际待补：%v", v.Missing)
	}
	// 待补事项为空时，全文里不该出现「请勿签发」
	if strings.Contains(v.FullText, "请勿签发") {
		t.Error("没有待补事项时不该印「请勿签发」")
	}
}

// ★ 未更正错报已达整体重要性却出无保留意见：必须拦住。
func TestAuditDocBlocksUnqualifiedWithMaterialMisstatement(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedDocBook(t, svc)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3) // 整体重要性 50,000
	// 登记一笔 60,000 的调整且不生成凭证（未更正）
	if _, err := svc.SaveAdjustment(ctx, service.AdjustmentInput{
		Year: 2025, Month: 3, Kind: "adjust",
		Summary: "补提折旧", Reason: "折旧计算表显示少提 60,000.00",
		Lines: []service.AdjustLineInput{
			{AccountCode: "560205", Summary: "补提", Debit: money100(60_000), DeptID: &dept},
			{AccountCode: "1602", Summary: "补提", Credit: money100(60_000)},
		},
	}); err != nil {
		t.Fatalf("登记调整失败: %v", err)
	}

	v, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "audit", Year: 2025, Month: 3, Opinion: "unqualified",
		FirmName: "某所", CPA1: "张三", CPA2: "李四", ReportDate: "2026-03-31",
	})
	if err != nil {
		t.Fatalf("生成审计报告失败: %v", err)
	}
	if v.CanIssue {
		t.Fatal("★ 未更正错报已达整体重要性，不该是「可以签发」的状态")
	}
	if !missingHas(v, "已达到整体重要性") {
		t.Errorf("要把矛盾写进待补事项：%v", v.Missing)
	}
	if !strings.Contains(v.FullText, "请勿签发") {
		t.Error("★ 有待补事项时，全文最前面必须印「请勿签发」—— " +
			"一份看起来写好了的草稿被拿去盖章，比打印不出来更糟")
	}

	// 改成保留意见就该放行（理由本来就该写进报告）
	v2, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "audit", Year: 2025, Month: 3, Opinion: "qualified",
		FirmName: "某所", CPA1: "张三", CPA2: "李四", ReportDate: "2026-03-31",
		BasisExtra: []string{"该事项仅影响折旧与固定资产计价，不具有广泛性。"},
	})
	if err != nil {
		t.Fatalf("生成保留意见报告失败: %v", err)
	}
	if missingHas(v2, "已达到整体重要性") {
		t.Error("已出具非无保留意见时不该再拦这一条")
	}
	if !hasSectionTitle(v2, "形成保留意见的基础") {
		t.Error("保留意见必须有「形成保留意见的基础」")
	}
}

// 没有重要性水平、缺签字人：都要进待补事项。
func TestAuditDocCollectsMissing(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedDocBook(t, svc)

	v, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "audit", Year: 2025, Month: 3, Opinion: "unqualified",
		CPA1: "张三", // 缺第二名 CPA、事务所、报告日期
	})
	if err != nil {
		t.Fatalf("生成审计报告失败: %v", err)
	}
	if v.CanIssue {
		t.Fatal("缺签字人时不该可以签发")
	}
	for _, want := range []string{"重要性水平", "会计师事务所", "两名注册会计师", "报告日期"} {
		if !missingHas(v, want) {
			t.Errorf("待补事项里缺少 %q，实际 %v", want, v.Missing)
		}
	}
	// 用户要填的项也要一并给出（界面据此渲染表单）
	if len(v.Inputs) < 5 {
		t.Errorf("要给出签字信息等输入项，实际 %d 项", len(v.Inputs))
	}
}

// ---------------------------------------------------------------------------
// 验资报告
// ---------------------------------------------------------------------------

func TestCapitalDocFromShareholderBalances(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	// 股东出资：张三 60 万、李四 40 万
	zhang := mustContact(t, svc, "shareholder", "张三")
	li := mustContact(t, svc, "shareholder", "李四")
	mustPost(t, svc, "2025-03-31", "收到投资款",
		service.VoucherLineInput{AccountCode: "1002", Summary: "投资款", Debit: money100(1_000_000)},
		service.VoucherLineInput{AccountCode: "3001", Summary: "投资款", Credit: money100(600_000),
			ContactID: &zhang},
		service.VoucherLineInput{AccountCode: "3001", Summary: "投资款", Credit: money100(400_000),
			ContactID: &li})

	// 界面上预填的实缴数
	paid, err := svc.ShareholderPaid(ctx, 2025, 3)
	if err != nil {
		t.Fatalf("取股东实缴失败: %v", err)
	}
	if len(paid) != 2 {
		t.Fatalf("应当有 2 位股东的实缴，实际 %d", len(paid))
	}
	sum := int64(0)
	for _, p := range paid {
		sum += int64(p.Paid)
	}
	if sum != int64(money100(1_000_000)) {
		t.Errorf("实缴合计 = %d，期望 %d", sum, int64(money100(1_000_000)))
	}

	v, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "capital", Year: 2025, Month: 3,
		RegisteredCapital: money100(1_000_000),
		Shares: []service.ShareInput{
			{Name: "张三", Subscribed: money100(600_000), Method: "货币", PaidDate: "2025-03-10"},
			{Name: "李四", Subscribed: money100(400_000), Method: "货币", PaidDate: "2025-03-12"},
		},
		Evidence: []string{"中国银行进账单（2025-03-10，600,000.00）",
			"中国银行进账单（2025-03-12，400,000.00）"},
		FirmName: "某所", ReportNo: "某会验字〔2025〕第 8 号",
		CPA1: "张三", CPA2: "李四", ReportDate: "2025-04-01",
	})
	if err != nil {
		t.Fatalf("生成验资报告失败: %v", err)
	}
	if !v.CanIssue {
		t.Fatalf("这份稿子应当可以签发，实际待补：%v", v.Missing)
	}
	res := sectionByName(t, v, "审验结果")
	if !strings.Contains(res, "1,000,000.00") {
		t.Errorf("审验结果没有写明实收合计：%q", res)
	}
	if !strings.Contains(res, "张三") || !strings.Contains(res, "李四") {
		t.Errorf("审验结果要列出股东：%q", res)
	}
}

// 实缴不足：验资报告的核心结论就在这个差上。
func TestCapitalDocDetectsShortfall(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	zhang := mustContact(t, svc, "shareholder", "张三")
	mustPost(t, svc, "2025-03-31", "收到首期出资",
		service.VoucherLineInput{AccountCode: "1002", Summary: "投资款", Debit: money100(300_000)},
		service.VoucherLineInput{AccountCode: "3001", Summary: "投资款", Credit: money100(300_000),
			ContactID: &zhang})

	v, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "capital", Year: 2025, Month: 3,
		RegisteredCapital: money100(1_000_000),
		Shares: []service.ShareInput{
			{Name: "张三", Subscribed: money100(1_000_000), Method: "货币"},
		},
		Evidence: []string{"中国银行进账单（2025-03-20，300,000.00）"},
		FirmName: "某所", ReportNo: "某会验字〔2025〕第 9 号",
		CPA1: "张三", CPA2: "李四", ReportDate: "2025-04-01",
	})
	if err != nil {
		t.Fatalf("生成验资报告失败: %v", err)
	}
	if v.CanIssue {
		t.Fatal("实缴不足注册资本时不该可以签发")
	}
	if !missingHas(v, "尚差") {
		t.Errorf("要说清还差多少，实际：%v", v.Missing)
	}
}

// 账上有股东、表单里漏了：不能漏掉一位股东（漏了验资结论就是错的）。
func TestCapitalDocCatchesShareholderMissingFromForm(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	zhang := mustContact(t, svc, "shareholder", "张三")
	li := mustContact(t, svc, "shareholder", "李四")
	mustPost(t, svc, "2025-03-31", "收到投资款",
		service.VoucherLineInput{AccountCode: "1002", Summary: "投资款", Debit: money100(1_000_000)},
		service.VoucherLineInput{AccountCode: "3001", Summary: "投资款", Credit: money100(600_000),
			ContactID: &zhang},
		service.VoucherLineInput{AccountCode: "3001", Summary: "投资款", Credit: money100(400_000),
			ContactID: &li})

	v, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "capital", Year: 2025, Month: 3,
		RegisteredCapital: money100(1_000_000),
		// 表单里只填了张三 —— 李四必须被带出来
		Shares:   []service.ShareInput{{Name: "张三", Subscribed: money100(600_000)}},
		Evidence: []string{"银行进账单"},
		FirmName: "某所", ReportNo: "某会验字〔2025〕第 10 号",
		CPA1: "张三", CPA2: "李四", ReportDate: "2025-04-01",
	})
	if err != nil {
		t.Fatalf("生成验资报告失败: %v", err)
	}
	res := sectionByName(t, v, "审验结果")
	if !strings.Contains(res, "李四") {
		t.Errorf("★ 账上有李四的出资，报告里必须列出来：%q", res)
	}
	if !missingHas(v, "李四") {
		t.Errorf("李四的认缴出资额没填，要进待补事项：%v", v.Missing)
	}
}

// ---------------------------------------------------------------------------
// 管理建议书
// ---------------------------------------------------------------------------

// 发现来自体检与底稿，不是编出来的。
func TestManagementDocFromHealthAndWorkpaper(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3)
	// 登记一笔没有证据的调整
	if _, err := svc.SaveAdjustment(ctx, service.AdjustmentInput{
		Year: 2025, Month: 3, Kind: "adjust",
		Summary: "补提折旧", Reason: "折旧计算表显示少提 1,000.00",
		Lines: []service.AdjustLineInput{
			{AccountCode: "560205", Summary: "补提", Debit: money100(1_000), DeptID: &dept},
			{AccountCode: "1602", Summary: "补提", Credit: money100(1_000)},
		},
	}); err != nil {
		t.Fatalf("登记调整失败: %v", err)
	}

	v, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "management", Year: 2025, Month: 3,
		FirmName: "某所", ReportNo: "某会建字〔2026〕第 1 号", ReportDate: "2026-03-31",
	})
	if err != nil {
		t.Fatalf("生成管理建议书失败: %v", err)
	}
	body := sectionByName(t, v, "发现的问题与建议")
	// 底稿缺口（这笔调整没有证据）+ 结论缺依据，都该出现在建议里
	if !strings.Contains(body, "审计调整未附证据") &&
		!strings.Contains(body, "审计结论缺少依据") {
		t.Errorf("★ 底稿里的缺口没有被写进建议书：%q", body)
	}
	if !strings.Contains(body, "建议：") {
		t.Errorf("每条发现都要给出建议：%q", body)
	}
	if !strings.Contains(sectionByName(t, v, "说明"), "不构成对财务报表的审计意见") {
		t.Error("建议书要写清它不构成审计意见")
	}
}

func TestManagementDocNoFindings(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedDocBook(t, svc)
	seedMateriality(t, svc, 2025, 3)

	v, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "management", Year: 2025, Month: 3,
		FirmName: "某所", ReportNo: "某会建字〔2026〕第 1 号", ReportDate: "2026-03-31",
	})
	if err != nil {
		t.Fatalf("生成管理建议书失败: %v", err)
	}
	if !hasSectionTitle(v, "发现的问题与建议") {
		t.Error("缺少「发现的问题与建议」一段")
	}
}

// 文书种类不认识要报错；三种文书的默认输入项各不相同。
func TestAuditDocGuardrails(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	if _, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "poem", Year: 2025, Month: 3,
	}); err == nil {
		t.Error("不认识的文书种类应当报错")
	}
	if _, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "audit", Year: 2025, Month: 13,
	}); err == nil {
		t.Error("非法期间应当报错")
	}
	kinds := svc.AuditDocKindOptions()
	if len(kinds) != 3 {
		t.Errorf("应当有三种文书，实际 %d 种", len(kinds))
	}
	ops := svc.OpinionOptions()
	if len(ops) != 4 {
		t.Errorf("应当有四种意见，实际 %d 种", len(ops))
	}
	for _, o := range ops {
		if o.Label == "" || o.Hint == "" {
			t.Errorf("意见 %s 缺少中文名或说明", o.Value)
		}
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func sectionByName(t *testing.T, v *service.AuditDocView, title string) string {
	t.Helper()
	for _, s := range v.Sections {
		if s.Title == title {
			return s.Body
		}
	}
	names := make([]string, 0, len(v.Sections))
	for _, s := range v.Sections {
		names = append(names, s.Title)
	}
	t.Fatalf("文书中没有「%s」一段，实际有 %v", title, names)
	return ""
}

func hasSectionTitle(v *service.AuditDocView, title string) bool {
	for _, s := range v.Sections {
		if s.Title == title {
			return true
		}
	}
	return false
}

func missingHas(v *service.AuditDocView, want string) bool {
	for _, m := range v.Missing {
		if strings.Contains(m, want) {
			return true
		}
	}
	return false
}

// ★ 验资报告的注册资本留空时，核心比对不能整段跳过。
//
// 发布前界面审计实测：ParseYuan("") 安静地返回 0，而 domain 里
// 「实缴 vs 注册资本」的比较在 0 时整段跳过 —— 报告却显示「待签字盖章」，
// 而那次审验实际上没有结论。
func TestCapitalRequiresRegisteredCapital(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	zhang := mustContact(t, svc, "shareholder", "张三")
	mustPost(t, svc, "2025-03-31", "收到投资款",
		service.VoucherLineInput{AccountCode: "1002", Summary: "投资款", Debit: money100(1_000_000)},
		service.VoucherLineInput{AccountCode: "3001", Summary: "投资款", Credit: money100(1_000_000),
			ContactID: &zhang})

	v, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "capital", Year: 2025, Month: 3,
		// 故意不填注册资本
		Shares:   []service.ShareInput{{Name: "张三", Subscribed: money100(1_000_000)}},
		Evidence: []string{"中国银行进账单（2025-03-20，1,000,000.00）"},
		FirmName: "某所", ReportNo: "某会验字〔2025〕第 11 号",
		CPA1: "张三", CPA2: "李四", ReportDate: "2025-04-01",
	})
	if err != nil {
		t.Fatalf("缺注册资本也应能生成草稿（缺什么写进待补事项）：%v", err)
	}
	if v.CanIssue {
		t.Fatal("★ 没填注册资本时不能是「可以签发」—— 核心比对根本没执行")
	}
	if !missingHas(v, "注册资本") {
		t.Errorf("待补事项里要点名注册资本，实际：%v", v.Missing)
	}
}

// ★ 实收资本出现借方余额（异常方向）时不能取绝对值抹平。
//
// 取绝对值会把「抽逃出资/错账」也显示成「已实缴」。
func TestCapitalReportsNegativePaidIn(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	zhang := mustContact(t, svc, "shareholder", "张三")
	// 借 3001、贷 1002：实收资本出现借方余额
	mustPost(t, svc, "2025-03-31", "退回出资（异常）",
		service.VoucherLineInput{AccountCode: "3001", Summary: "退回出资", Debit: money100(200_000),
			ContactID: &zhang},
		service.VoucherLineInput{AccountCode: "1002", Summary: "退回出资", Credit: money100(200_000)})

	paid, err := svc.ShareholderPaid(ctx, 2025, 3)
	if err != nil {
		t.Fatalf("取股东实缴失败: %v", err)
	}
	negative := false
	for _, p := range paid {
		if p.Paid.IsNegative() {
			negative = true
		}
	}
	if !negative {
		t.Errorf("★ 借方余额应当原样带出来（负数），而不是取绝对值抹平：%+v", paid)
	}

	v, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "capital", Year: 2025, Month: 3,
		RegisteredCapital: money100(1_000_000),
		Shares:            []service.ShareInput{{Name: "张三", Subscribed: money100(1_000_000)}},
		Evidence:          []string{"银行回单"},
		FirmName:          "某所", ReportNo: "某会验字〔2025〕第 12 号",
		CPA1: "张三", CPA2: "李四", ReportDate: "2025-04-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.CanIssue {
		t.Fatal("实收资本出现借方余额时不该可以签发")
	}
	if !missingHas(v, "借方余额") {
		t.Errorf("要点出「实收资本出现借方余额」这个异常，实际：%v", v.Missing)
	}
}

// ★ 没选意见类型时，正文不能写出「我们认为……公允反映」。
//
// 那是软件的口气替注册会计师形成了意见。
func TestAuditDocDoesNotChooseOpinion(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedDocBook(t, svc)
	seedMateriality(t, svc, 2025, 3)
	periodID := int64(2025*100 + 3)
	for _, owner := range []string{"materiality", "conclusion"} {
		if _, err := svc.AddEvidence(ctx, service.EvidenceInput{
			OwnerType: owner, OwnerID: periodID, RefKind: "external",
			RefLabel: "上年审计报告与访谈记录", By: "李审计",
		}); err != nil {
			t.Fatal(err)
		}
	}

	v, err := svc.AuditDoc(ctx, service.AuditDocInput{
		Kind: "audit", Year: 2025, Month: 3, // 不传 Opinion
		FirmName: "某所", ReportNo: "某会审字〔2025〕第 9 号",
		CPA1: "张三", CPA2: "李四", ReportDate: "2025-04-30",
	})
	if err != nil {
		t.Fatalf("没选意见类型时也应能生成草稿：%v", err)
	}
	if v.CanIssue {
		t.Fatal("没选意见类型时不能是「可以签发」")
	}
	if v.OpinionStr != "未选择" {
		t.Errorf("意见文字应当是「未选择」，实际 %q", v.OpinionStr)
	}
	body := ""
	for _, s := range v.Sections {
		if s.Title == "审计意见" {
			body = s.Body
		}
	}
	if strings.Contains(body, "我们认为") || strings.Contains(body, "公允反映") {
		t.Errorf("★ 没选意见时正文不该出现「我们认为……公允反映」：%q", body)
	}
	if !strings.Contains(body, "待注册会计师选择") {
		t.Errorf("正文要明说意见类型待选：%q", body)
	}
	if !missingHas(v, "还没有选择审计意见类型") {
		t.Errorf("待补事项要点名意见类型：%v", v.Missing)
	}
}
