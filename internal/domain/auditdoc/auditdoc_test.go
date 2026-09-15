package auditdoc_test

import (
	"strings"
	"testing"

	"miniaccount/internal/domain/auditdoc"
	"miniaccount/internal/domain/money"
)

func y(n int64) money.Money { return money.Money(n) * money.Yuan }

// 一份能签发的审计报告需要的事实。
func auditInput() auditdoc.AuditInput {
	return auditdoc.AuditInput{
		Company: "杭州云帆软件有限公司", Period: "2025 年度",
		Opinion:  auditdoc.OpinionUnqualified,
		FirmName: "某某会计师事务所（普通合伙）", ReportNo: "某会审字〔2026〕第 123 号",
		CPA1: "张三", CPA2: "李四", ReportDate: "2026-03-31",
		HasMateriality: true, Overall: y(50_000), Performance: y(30_000), Trivial: y(2_500),
		MisstatementSum: y(3_000), MisstatementNum: 1, ReclassNum: 0,
		Assets: y(1_000_000), Liabilities: y(400_000), Equity: y(600_000),
		Revenue: y(800_000), Profit: y(120_000),
	}
}

func TestAuditDocumentSectionsAndSources(t *testing.T) {
	d, err := auditdoc.Audit(auditInput())
	if err != nil {
		t.Fatalf("生成审计报告失败: %v", err)
	}
	if !d.Draft {
		t.Error("★ 软件产出的必须是草稿 —— 签字盖章才生效")
	}
	if d.Submittable {
		t.Error("★ 文书不能被标记为可直接出具")
	}
	if !d.CanIssue() {
		t.Fatalf("这份稿子应当可以签发，实际待补 %v", d.Missing)
	}
	// 必备段落
	for _, want := range []string{"审计意见", "形成无保留意见的基础",
		"管理层和治理层对财务报表的责任", "注册会计师对财务报表审计的责任"} {
		if !hasSection(d, want) {
			t.Errorf("缺少段落「%s」", want)
		}
	}
	// ★ 每一段都要写清事实从哪来
	for _, s := range d.Sections {
		if strings.TrimSpace(s.Source) == "" {
			t.Errorf("段落「%s」没有写来源 —— 报告里的事实要能追到账套或底稿", s.Title)
		}
	}
	if !strings.Contains(d.Sections[0].Body, "公允反映") {
		t.Errorf("无保留意见的措辞不对：%q", d.Sections[0].Body)
	}
	if !strings.Contains(signatureOf(d), "两名注册会计师") {
		t.Errorf("生效条件要写清签字要求：%q", d.Signature)
	}
	if !strings.Contains(d.Concludes, "签字盖章") {
		t.Errorf("结论 = %q", d.Concludes)
	}
}

// ★ 未更正错报已达整体重要性，却出具无保留意见 —— 这个组合必须被拦下。
//
// 别的地方都在帮用户把材料做得好看，这一处必须反过来：
// 它把矛盾写进「待补事项」，让签字的人先回答「为什么这样还能是无保留」。
func TestAuditBlocksUnqualifiedOpinionWithMaterialMisstatement(t *testing.T) {
	in := auditInput()
	in.MisstatementSum = y(60_000) // 超过整体重要性 50,000
	d, err := auditdoc.Audit(in)
	if err != nil {
		t.Fatalf("生成审计报告失败: %v", err)
	}
	if d.CanIssue() {
		t.Fatal("★ 未更正错报已达整体重要性却出无保留意见，不该是「可以签发」的状态")
	}
	if !missingContains(d, "已达到整体重要性") {
		t.Errorf("要把矛盾说清楚，实际待补：%v", d.Missing)
	}
	if !strings.Contains(d.Concludes, "还不能签发") {
		t.Errorf("结论要说清不能签发：%q", d.Concludes)
	}

	// 同一组数字，改成保留意见就该放行（理由本来就该在报告里写出来）
	in.Opinion = auditdoc.OpinionQualified
	d2, err := auditdoc.Audit(in)
	if err != nil {
		t.Fatalf("生成保留意见报告失败: %v", err)
	}
	if missingContains(d2, "已达到整体重要性") {
		t.Error("已经出具非无保留意见时，不该再拦这一条")
	}
	if !hasSection(d2, "形成保留意见的基础") {
		t.Error("非无保留意见必须有「形成……的基础」一段")
	}
	if !strings.Contains(d2.Sections[0].Body, "除「形成保留意见的基础」部分所述事项产生的影响外") {
		t.Errorf("保留意见的措辞不对：%q", d2.Sections[0].Body)
	}
}

// 没有重要性水平、没有签字人、依据丢失：都要写进「待补事项」。
func TestAuditCollectsMissingItems(t *testing.T) {
	in := auditInput()
	in.HasMateriality = false
	in.CPA2 = ""
	in.EvidenceMissing = 2
	in.EvidenceBroken = 1
	d, err := auditdoc.Audit(in)
	if err != nil {
		t.Fatalf("生成审计报告失败: %v", err)
	}
	for _, m := range d.Missing {
		if strings.TrimSpace(m) == "" || strings.HasSuffix(strings.TrimSpace(m), "：") {
			t.Errorf("待补事项里出现了没有说明的空条目：%q", m)
		}
	}
	for _, want := range []string{"重要性水平", "两名注册会计师", "找不到", "还没有附任何依据"} {
		if !missingContains(d, want) {
			t.Errorf("待补事项里缺少 %q，实际：%v", want, d.Missing)
		}
	}
}

func TestAuditRejectsBadOpinion(t *testing.T) {
	in := auditInput()
	in.Opinion = "nice"
	if _, err := auditdoc.Audit(in); err == nil {
		t.Error("不认识的意见类型应当报错")
	}
	in = auditInput()
	in.Company = ""
	if _, err := auditdoc.Audit(in); err == nil {
		t.Error("没有单位名称应当报错")
	}
}

// 四种意见的措辞都不一样 —— 措辞错了，报告的结论就变了。
func TestAllOpinionsHaveDistinctWording(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range auditdoc.AllOpinions {
		if o.Label() == string(o) {
			t.Errorf("意见类型 %s 没有中文名", o)
		}
		in := auditInput()
		in.Opinion = o
		if o.NeedsBasis() {
			// 非无保留意见必须有一段基础；这里补一句理由
			in.BasisExtra = []string{"形成该意见的具体事项（测试用）。"}
		}
		d, err := auditdoc.Audit(in)
		if err != nil {
			t.Fatalf("%s：生成失败 %v", o.Label(), err)
		}
		body := d.Sections[0].Body
		if seen[body] {
			t.Errorf("★ %s 的意见段与另一种意见完全相同 —— 措辞错了结论就变了", o.Label())
		}
		seen[body] = true
		if o.NeedsBasis() && !hasSection(d, "形成"+o.Label()+"的基础") {
			t.Errorf("%s 缺少「形成%s的基础」", o.Label(), o.Label())
		}
	}
}

// ---------------------------------------------------------------------------
// 验资报告
// ---------------------------------------------------------------------------

func capitalInput() auditdoc.CapitalInput {
	return auditdoc.CapitalInput{
		Company: "杭州云帆软件有限公司", Period: "2026 年 1 月 15 日",
		FirmName: "某某会计师事务所", ReportNo: "某会验字〔2026〕第 8 号",
		CPA1: "张三", CPA2: "李四", ReportDate: "2026-01-20",
		RegisteredCapital: y(1_000_000), BookPaidIn: y(1_000_000),
		Shareholders: []auditdoc.Shareholder{
			{Name: "张三", Subscribed: y(600_000), Paid: y(600_000),
				Method: "货币", PaidDate: "2026-01-10"},
			{Name: "李四", Subscribed: y(400_000), Paid: y(400_000),
				Method: "货币", PaidDate: "2026-01-12"},
		},
		Evidence: []string{"中国银行进账单（2026-01-10，600,000.00）",
			"中国银行进账单（2026-01-12，400,000.00）"},
	}
}

func TestCapitalDocument(t *testing.T) {
	d, err := auditdoc.Capital(capitalInput())
	if err != nil {
		t.Fatalf("生成验资报告失败: %v", err)
	}
	if !d.CanIssue() {
		t.Fatalf("这份稿子应当可以签发，实际待补 %v", d.Missing)
	}
	body := sectionBody(d, "审验结果")
	if !strings.Contains(body, "1,000,000.00") {
		t.Errorf("审验结果里要写清实收合计：%q", body)
	}
	if !strings.Contains(body, "张三") || !strings.Contains(body, "李四") {
		t.Errorf("要列出各股东的出资：%q", body)
	}
	if !strings.Contains(d.PolicyNote, "实际收到") {
		t.Errorf("免责说明要写清只对实际收到的出资发表意见：%q", d.PolicyNote)
	}
}

// ★ 实缴与注册资本不符 —— 验资报告的核心结论就在这个差上。
func TestCapitalDetectsShortfall(t *testing.T) {
	in := capitalInput()
	in.Shareholders[1].Paid = y(100_000) // 李四只缴了 10 万
	// 账上仍按登记的 100 万挂着 → 与实缴 70 万对不上
	in.BookPaidIn = y(1_000_000)
	d, err := auditdoc.Capital(in)
	if err != nil {
		t.Fatalf("生成验资报告失败: %v", err)
	}
	if d.CanIssue() {
		t.Fatal("实缴不足注册资本时不该是「可以签发」")
	}
	if !missingContains(d, "尚差") {
		t.Errorf("要说清还差多少，实际：%v", d.Missing)
	}
	if !missingContains(d, "不一致") {
		t.Errorf("表上与账上对不上也要报出来，实际：%v", d.Missing)
	}
}

// 超出注册资本：一般应计入资本公积，也要提示。
func TestCapitalDetectsExcess(t *testing.T) {
	in := capitalInput()
	in.Shareholders[0].Paid = y(800_000) // 多缴 20 万
	in.BookPaidIn = 0                    // 不比对账面
	d, _ := auditdoc.Capital(in)
	if !missingContains(d, "超过") || !missingContains(d, "资本公积") {
		t.Errorf("超额出资要提示计入资本公积，实际：%v", d.Missing)
	}
}

// 非货币出资必须有评估与权属转移手续。
func TestCapitalRequiresNonCashEvidence(t *testing.T) {
	in := capitalInput()
	in.NonCash = true
	d, _ := auditdoc.Capital(in)
	if !missingContains(d, "非货币出资") {
		t.Errorf("有非货币出资时要提示评估与转移手续，实际：%v", d.Missing)
	}
}

func TestCapitalRequiresEvidenceAndShareholders(t *testing.T) {
	in := capitalInput()
	in.Evidence = nil
	in.Shareholders = nil
	d, err := auditdoc.Capital(in)
	if err != nil {
		t.Fatalf("缺依据时仍应能生成草稿（缺什么写进待补事项）：%v", err)
	}
	if !missingContains(d, "验资依据") {
		t.Error("没有验资依据时必须报出来")
	}
	if !missingContains(d, "股东") {
		t.Error("没有股东出资记录时必须报出来")
	}
}

// ---------------------------------------------------------------------------
// 管理建议书
// ---------------------------------------------------------------------------

func TestManagementDocument(t *testing.T) {
	d, err := auditdoc.Management(auditdoc.ManagementInput{
		Company: "杭州云帆软件有限公司", Period: "2025 年度",
		FirmName: "某某会计师事务所", ReportNo: "某会建字〔2026〕第 3 号",
		ReportDate: "2026-03-31",
		Scope:      []string{"查阅账簿与凭证", "复核报表勾稽关系"},
		Findings: []auditdoc.Finding{
			{Title: "现金余额为负", Level: "high", Detail: "1001 库存现金期末为 −1,200.00",
				Suggestion: "核对现金日记账与备用金借支", Source: "结账前体检"},
			{Title: "审计调整缺依据", Level: "medium", Detail: "ADJ-202503-001 未附折旧计算表",
				Suggestion: "补附折旧计算表", Source: "审计证据链"},
		},
	})
	if err != nil {
		t.Fatalf("生成管理建议书失败: %v", err)
	}
	if !d.CanIssue() {
		t.Fatalf("这份稿子应当可以签发，实际待补 %v", d.Missing)
	}
	body := sectionBody(d, "发现的问题与建议")
	for _, want := range []string{"【重要】", "【关注】", "现金余额为负", "建议：", "来源："} {
		if !strings.Contains(body, want) {
			t.Errorf("问题与建议段里缺少 %q：\n%s", want, body)
		}
	}
	if !strings.Contains(sectionBody(d, "说明"), "不构成对财务报表的审计意见") {
		t.Error("管理建议书要写清它不构成审计意见")
	}
}

// 一条发现都没有的建议书只是一张纸 —— 要提示。
func TestManagementWarnsWhenNoFindings(t *testing.T) {
	d, err := auditdoc.Management(auditdoc.ManagementInput{
		Company: "杭州云帆软件有限公司", Period: "2025 年度",
		FirmName: "某某会计师事务所", ReportDate: "2026-03-31",
	})
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if !missingContains(d, "没有任何发现") {
		t.Errorf("没有发现时要提示，实际：%v", d.Missing)
	}
	if strings.Contains(d.Concludes, "签字盖章后生效") {
		t.Error("有待补事项时不该说「已成形」")
	}
}

// ---------------------------------------------------------------------------
// 通用约束
// ---------------------------------------------------------------------------

func TestValidateRejectsNonDraftAndEmptySections(t *testing.T) {
	ok := auditdoc.Doc{
		Kind: auditdoc.KindManagement, Title: "管理建议书", Company: "某公司",
		Draft: true,
		Sections: []auditdoc.Section{
			{No: "一", Title: "说明", Body: "正文", Source: "来源"},
		},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("基准用例应当通过：%v", err)
	}
	cases := []struct {
		name string
		mod  func(d *auditdoc.Doc)
		want string
	}{
		{"种类不认识", func(d *auditdoc.Doc) { d.Kind = "poem" }, "种类"},
		{"没有标题", func(d *auditdoc.Doc) { d.Title = "" }, "文书名称"},
		{"没有单位", func(d *auditdoc.Doc) { d.Company = "" }, "被审计单位"},
		{"没有段落", func(d *auditdoc.Doc) { d.Sections = nil }, "没有任何段落"},
		{"段落没标题", func(d *auditdoc.Doc) { d.Sections[0].Title = "" }, "没有标题"},
		{"段落是空的", func(d *auditdoc.Doc) { d.Sections[0].Body = "" }, "是空的"},
		{"段落重复", func(d *auditdoc.Doc) {
			d.Sections = append(d.Sections, d.Sections[0])
		}, "重复"},
		{"不是草稿", func(d *auditdoc.Doc) { d.Draft = false }, "只能是草稿"},
		{"声称可直接出具", func(d *auditdoc.Doc) { d.Submittable = true }, "不能"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := ok
			d.Sections = append([]auditdoc.Section(nil), ok.Sections...)
			c.mod(&d)
			err := d.Validate()
			if err == nil {
				t.Fatal("应当被拦下")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("报错应当提到 %q，实际 %v", c.want, err)
			}
		})
	}
}

// 非无保留意见缺「形成……的基础」一段 —— 这在执业检查里等同于没有意见。
func TestValidateRequiresBasisSectionForNonUnqualified(t *testing.T) {
	d := auditdoc.Doc{
		Kind: auditdoc.KindAudit, Title: "审计报告", Company: "某公司",
		Opinion: auditdoc.OpinionAdverse, Draft: true,
		Sections: []auditdoc.Section{
			{No: "一", Title: "审计意见", Body: "……", Source: "来源"},
		},
	}
	err := d.Validate()
	if err == nil {
		t.Fatal("否定意见缺基础段应当被拦下")
	}
	if !strings.Contains(err.Error(), "形成否定意见的基础") {
		t.Errorf("报错要说清缺哪一段：%v", err)
	}
}

func TestKindAndOpinionLabels(t *testing.T) {
	for _, k := range auditdoc.AllKinds {
		if k.Label() == "" || k.Label() == string(k) {
			t.Errorf("文书种类 %s 没有中文名", k)
		}
	}
	if auditdoc.Kind("x").Valid() {
		t.Error("不认识的种类不该 Valid")
	}
	if !auditdoc.OpinionQualified.NeedsBasis() || auditdoc.OpinionUnqualified.NeedsBasis() {
		t.Error("只有非无保留意见才需要「形成……的基础」一段")
	}
	if auditdoc.Opinion("x").Valid() {
		t.Error("不认识的意见类型不该 Valid")
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func hasSection(d *auditdoc.Doc, title string) bool {
	for _, s := range d.Sections {
		if s.Title == title {
			return true
		}
	}
	return false
}

func sectionBody(d *auditdoc.Doc, title string) string {
	for _, s := range d.Sections {
		if s.Title == title {
			return s.Body
		}
	}
	return ""
}

func missingContains(d *auditdoc.Doc, want string) bool {
	for _, m := range d.Missing {
		if strings.Contains(m, want) {
			return true
		}
	}
	return false
}

func signatureOf(d *auditdoc.Doc) string { return d.Signature }
