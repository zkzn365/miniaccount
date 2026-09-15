package service_test

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 税务计算表
// ---------------------------------------------------------------------------
//
// 三张表各自的坑不一样，这一组测试逐个盯：
//
//	增值税      专栏取数 + 留抵从上期滚过来
//	企业所得税  累计口径 + 小微分段
//	个人所得税  累计口径 + 草稿工资单只能说「预计数」

// taxReturn 取一张表。
func taxReturn(t *testing.T, svc *service.Service,
	kind string, year, month int, in service.TaxReturnInput) *service.TaxReturnView {
	t.Helper()
	in.Kind, in.Year, in.Month = kind, year, month
	v, err := svc.TaxReturn(context.Background(), in)
	if err != nil {
		t.Fatalf("生成 %s 表失败: %v", kind, err)
	}
	return v
}

// rowAmount 取表里某一行的金额。
func rowAmount(t *testing.T, v *service.TaxReturnView, line string) int64 {
	t.Helper()
	for _, r := range v.Rows {
		if r.Line == line {
			return int64(r.Amount)
		}
	}
	t.Fatalf("表里没有第 %s 行", line)
	return 0
}

// keyAmount 取表里某个关键数。
func keyAmount(t *testing.T, v *service.TaxReturnView, label string) int64 {
	t.Helper()
	for _, k := range v.Keys {
		if k.Label == label {
			return int64(k.Amount)
		}
	}
	labels := make([]string, 0, len(v.Keys))
	for _, k := range v.Keys {
		labels = append(labels, k.Label)
	}
	t.Fatalf("表里没有关键数 %q，实际有 %v", label, labels)
	return 0
}

// ---------------------------------------------------------------------------
// 增值税
// ---------------------------------------------------------------------------

// 本期的销项与进项从 222101 各专栏取数，应纳税额与附加税费要算对。
func TestTaxReturnVATFromLedgerColumns(t *testing.T) {
	svc := newSvc(t, 6)
	mustPost(t, svc, "2025-03-31", "确认收入",
		service.VoucherLineInput{AccountCode: "1122", Summary: "销售", Debit: money100(113_000),
			ContactID: wpPtr(mustContact(t, svc, "customer", "甲公司"))},
		service.VoucherLineInput{AccountCode: "5001", Summary: "销售", Credit: money100(100_000)},
		service.VoucherLineInput{AccountCode: "22210102", Summary: "销项税额", Credit: money100(13_000)})
	mustPost(t, svc, "2025-03-31", "取得进项发票",
		service.VoucherLineInput{AccountCode: "1403", Summary: "采购", Debit: money100(50_000)},
		service.VoucherLineInput{AccountCode: "22210101", Summary: "进项税额", Debit: money100(6_500)},
		service.VoucherLineInput{AccountCode: "2202", Summary: "采购", Credit: money100(56_500),
			ContactID: wpPtr(mustContact(t, svc, "supplier", "乙公司"))})

	v := taxReturn(t, svc, "vat", 2025, 3, service.TaxReturnInput{})
	if got := rowAmount(t, v, "11"); got != int64(money100(13_000)) {
		t.Errorf("第 11 行销项税额 = %d，期望 %d（22210102 贷方发生额）",
			got, int64(money100(13_000)))
	}
	if got := rowAmount(t, v, "14"); got != int64(money100(6_500)) {
		t.Errorf("第 14 行进项税额 = %d，期望 %d", got, int64(money100(6_500)))
	}
	// 应纳税额 = 13,000 − 6,500 = 6,500
	if got := keyAmount(t, v, "本期应纳税额（增值税）"); got != int64(money100(6_500)) {
		t.Errorf("应纳税额 = %d，期望 %d", got, int64(money100(6_500)))
	}
	// 附加税费 12% = 780
	if got := keyAmount(t, v, "附加税费（合计 12%）"); got != int64(money100(780)) {
		t.Errorf("附加税费 = %d，期望 %d（6,500 × 12%%）", got, int64(money100(780)))
	}
	if got := keyAmount(t, v, "本期应补(退)税额"); got != int64(money100(7_280)) {
		t.Errorf("应补税额 = %d，期望 %d", got, int64(money100(7_280)))
	}
	if v.Submittable {
		t.Error("★ 税务计算表永远不能标记为可直接申报")
	}
	if !strings.Contains(v.PolicyNote, "不替代申报表") {
		t.Errorf("口径说明要写清不替代申报表：%q", v.PolicyNote)
	}
	// 每一行都要有来源
	for _, r := range v.Rows {
		if strings.TrimSpace(r.Source) == "" {
			t.Errorf("第 %s 行「%s」没有数据来源 —— 会计没法拿它去核对", r.Line, r.Label)
		}
	}
	// 身份也要在表上写清
	if len(v.Identities) == 0 {
		t.Error("表上要写清用的身份与口径")
	}
}

// ★ 上期留抵要从上期余额滚过来，而不是当期的数。
func TestTaxReturnVATCarriesPriorCredit(t *testing.T) {
	svc := newSvc(t, 6)
	// 2 月：进项 20,000、销项 5,000 → 期末留抵 15,000
	mustPost(t, svc, "2025-02-28", "2 月采购",
		service.VoucherLineInput{AccountCode: "1403", Summary: "采购", Debit: money100(153_000)},
		service.VoucherLineInput{AccountCode: "22210101", Summary: "进项税额", Debit: money100(20_000)},
		service.VoucherLineInput{AccountCode: "2202", Summary: "采购", Credit: money100(173_000),
			ContactID: wpPtr(mustContact(t, svc, "supplier", "乙公司"))})
	mustPost(t, svc, "2025-02-28", "2 月销售",
		service.VoucherLineInput{AccountCode: "1122", Summary: "销售", Debit: money100(5_000),
			ContactID: wpPtr(mustContact(t, svc, "customer", "甲公司"))},
		service.VoucherLineInput{AccountCode: "22210102", Summary: "销项税额", Credit: money100(5_000)})

	// 3 月：只有销项 10,000 —— 留抵 15,000 应当把它全抵掉，还应留 5,000
	mustPost(t, svc, "2025-03-31", "3 月销售",
		service.VoucherLineInput{AccountCode: "1122", Summary: "销售", Debit: money100(10_000),
			ContactID: wpPtr(mustContact(t, svc, "customer", "甲公司"))},
		service.VoucherLineInput{AccountCode: "22210102", Summary: "销项税额", Credit: money100(10_000)})

	v := taxReturn(t, svc, "vat", 2025, 3, service.TaxReturnInput{})
	if got := rowAmount(t, v, "17"); got != int64(money100(15_000)) {
		t.Errorf("第 17 行上期留抵 = %d，期望 %d（从上期期末借方净额来）",
			got, int64(money100(15_000)))
	}
	if got := keyAmount(t, v, "本期应纳税额（增值税）"); got != 0 {
		t.Errorf("留抵足够抵扣时不应纳税，实际 %d", got)
	}
	if got := keyAmount(t, v, "期末留抵税额（结转下期）"); got != int64(money100(5_000)) {
		t.Errorf("期末留抵 = %d，期望 %d", got, int64(money100(5_000)))
	}
	if !hasWarning(v, "留抵") {
		t.Error("有留抵时必须报出来，否则用户看不懂为什么应纳是 0")
	}
}

// 小规模纳税人：附加税费减半，而且账上有进项时要提示。
func TestTaxReturnVATSmallScale(t *testing.T) {
	svc := newSvc(t, 6)
	if err := svc.SetVATStatus(context.Background(), "small_scale", "2025-01-01", "测试"); err != nil {
		t.Fatalf("设置纳税人身份失败: %v", err)
	}
	mustPost(t, svc, "2025-03-31", "销售",
		service.VoucherLineInput{AccountCode: "1122", Summary: "销售", Debit: money100(10_300),
			ContactID: wpPtr(mustContact(t, svc, "customer", "甲公司"))},
		service.VoucherLineInput{AccountCode: "5001", Summary: "销售", Credit: money100(10_000)},
		service.VoucherLineInput{AccountCode: "22210102", Summary: "销项税额", Credit: money100(300)})

	v := taxReturn(t, svc, "vat", 2025, 3, service.TaxReturnInput{})
	// 附加 12% → 减半 6%：300 × 6% = 18
	if got := keyAmount(t, v, "附加税费（合计 6%）"); got != int64(money100(18)) {
		t.Errorf("小规模减半后附加税费 = %d，期望 %d（300 × 6%%）", got, int64(money100(18)))
	}
	found := false
	for _, id := range v.Identities {
		if strings.Contains(id.Value, "减半") {
			found = true
		}
	}
	if !found {
		t.Error("表上要写清附加税费享受了减半优惠")
	}
}

// ---------------------------------------------------------------------------
// 企业所得税
// ---------------------------------------------------------------------------

func TestTaxReturnCITFromIncomeStatement(t *testing.T) {
	svc := newSvc(t, 6)
	// 3 月做一笔销售：收入 100,000、成本 60,000
	mustPost(t, svc, "2025-03-31", "销售",
		service.VoucherLineInput{AccountCode: "1122", Summary: "销售", Debit: money100(100_000),
			ContactID: wpPtr(mustContact(t, svc, "customer", "甲公司"))},
		service.VoucherLineInput{AccountCode: "5001", Summary: "销售", Credit: money100(100_000)})
	mustPost(t, svc, "2025-03-31", "结转成本",
		service.VoucherLineInput{AccountCode: "5401", Summary: "成本", Debit: money100(60_000)},
		service.VoucherLineInput{AccountCode: "1405", Summary: "成本", Credit: money100(60_000)})

	v := taxReturn(t, svc, "cit", 2025, 3, service.TaxReturnInput{})
	if got := rowAmount(t, v, "1"); got != int64(money100(100_000)) {
		t.Errorf("营业收入 = %d，期望 %d（利润表行1，累计口径）", got, int64(money100(100_000)))
	}
	if got := rowAmount(t, v, "3"); got != int64(money100(40_000)) {
		t.Errorf("利润总额 = %d，期望 %d", got, int64(money100(40_000)))
	}
	// 一般企业 25% → 10,000
	if got := keyAmount(t, v, "应纳所得税额"); got != int64(money100(10_000)) {
		t.Errorf("应纳所得税额 = %d，期望 %d（40,000 × 25%%）", got, int64(money100(10_000)))
	}
	if !hasWarning(v, "纳税调整") {
		t.Error("★ 纳税调整额为 0 时必须提醒 —— 限额项不调整会少缴税")
	}
	// 界面要给出可填的输入项
	if len(v.InputFields) < 3 {
		t.Errorf("企业所得税表要给出纳税调整等输入项，实际 %d 项", len(v.InputFields))
	}
}

// ★ 企业所得税是**累计口径**：3 月的收入要含 1、2 月，不能只算 3 月。
func TestTaxReturnCITIsCumulative(t *testing.T) {
	svc := newSvc(t, 6)
	mustPost(t, svc, "2025-01-31", "1 月销售",
		service.VoucherLineInput{AccountCode: "1122", Summary: "销售", Debit: money100(30_000),
			ContactID: wpPtr(mustContact(t, svc, "customer", "甲公司"))},
		service.VoucherLineInput{AccountCode: "5001", Summary: "销售", Credit: money100(30_000)})
	mustPost(t, svc, "2025-03-31", "3 月销售",
		service.VoucherLineInput{AccountCode: "1122", Summary: "销售", Debit: money100(70_000),
			ContactID: wpPtr(mustContact(t, svc, "customer", "甲公司"))},
		service.VoucherLineInput{AccountCode: "5001", Summary: "销售", Credit: money100(70_000)})

	m1 := taxReturn(t, svc, "cit", 2025, 1, service.TaxReturnInput{})
	if got := rowAmount(t, m1, "1"); got != int64(money100(30_000)) {
		t.Errorf("1 月营业收入 = %d，期望 %d", got, int64(money100(30_000)))
	}
	m3 := taxReturn(t, svc, "cit", 2025, 3, service.TaxReturnInput{})
	if got := rowAmount(t, m3, "1"); got != int64(money100(100_000)) {
		t.Errorf("★ 3 月的营业收入应当是**年初至本月累计** 100,000.00，实际 %d —— "+
			"按本季数算会系统性少缴", got)
	}
	if got := rowAmount(t, m3, "4"); got != 0 {
		t.Errorf("没填纳税调整时第 4 行应当是 0，实际 %d", got)
	}
}

// 纳税调整与弥补亏损要参与计算，小微要分段。
func TestTaxReturnCITAdjustmentsAndSmallLowProfit(t *testing.T) {
	svc := newSvc(t, 6)
	mustPost(t, svc, "2025-03-31", "销售",
		service.VoucherLineInput{AccountCode: "1122", Summary: "销售", Debit: money100(500_000),
			ContactID: wpPtr(mustContact(t, svc, "customer", "甲公司"))},
		service.VoucherLineInput{AccountCode: "5001", Summary: "销售", Credit: money100(500_000)})

	yes := true
	v := taxReturn(t, svc, "cit", 2025, 3, service.TaxReturnInput{
		TaxAdjustIncrease: money100(20_000), // 招待费超支等
		TaxAdjustDecrease: money100(10_000),
		LossOffset:        money100(10_000),
		SmallLowProfit:    &yes,
	})
	// 应纳税所得额 = 500,000 + 20,000 − 10,000 − 10,000 = 500,000
	if got := rowAmount(t, v, "7"); got != int64(money100(500_000)) {
		t.Errorf("应纳税所得额 = %d，期望 %d", got, int64(money100(500_000)))
	}
	// 小微 5% → 25,000
	if got := keyAmount(t, v, "应纳所得税额"); got != int64(money100(25_000)) {
		t.Errorf("小微应纳所得税额 = %d，期望 %d", got, int64(money100(25_000)))
	}
	// 明确给了口径就不该再提示「按企业规模推断」
	if hasWarning(v, "企业规模") {
		t.Error("用户明确选了小微口径时不该再按账套规模推断并提示")
	}
}

// 已预缴取自 222104 的本年借方发生额。
func TestTaxReturnCITPrepaidFromLedger(t *testing.T) {
	svc := newSvc(t, 6)
	mustPost(t, svc, "2025-03-31", "缴纳一季度所得税",
		service.VoucherLineInput{AccountCode: "222104", Summary: "缴纳所得税", Debit: money100(3_000)},
		service.VoucherLineInput{AccountCode: "1002", Summary: "缴纳所得税", Credit: money100(3_000)})

	v := taxReturn(t, svc, "cit", 2025, 3, service.TaxReturnInput{})
	if got := rowAmount(t, v, "10"); got != int64(money100(3_000)) {
		t.Errorf("已预缴所得税 = %d，期望 %d（222104 本年借方发生额）", got, int64(money100(3_000)))
	}
}

// ---------------------------------------------------------------------------
// 个人所得税
// ---------------------------------------------------------------------------

// 个税表要读工资单里固化的累计字段，而不是现在重算。
func TestTaxReturnIITFromPayrollRuns(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	// 员工必须有部门：工资费用科目按部门辅助核算
	dept := mustDepartment(t, svc, "生产部")
	if _, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Code: "E001", Name: "张三", BaseSalary: money100(20_000),
		Enabled: true, DeptID: &dept,
	}); err != nil {
		t.Fatalf("保存员工失败: %v", err)
	}

	// 1 月与 2 月各生成一张工资单（累计口径）
	for _, m := range []int{1, 2} {
		if _, err := svc.BuildPayroll(ctx, service.BuildPayrollInput{
			Year: 2025, Month: m, CreatedBy: "李会计", OnlyPostedHistory: false,
		}); err != nil {
			t.Fatalf("生成 %d 月工资单失败: %v", m, err)
		}
	}

	v := taxReturn(t, svc, "iit", 2025, 2, service.TaxReturnInput{})
	if len(v.Rows) == 0 {
		t.Fatal("个税表里应当有员工行")
	}
	// 2 个月 × 20,000 = 40,000 累计收入
	row := findRowByLabel(t, v, "累计收入合计")
	if int64(row.Amount) != int64(money100(40_000)) {
		t.Errorf("累计收入合计 = %d，期望 %d（读的是工资单固化的累计数）",
			int64(row.Amount), int64(money100(40_000)))
	}
	if !strings.Contains(row.Source, "累计收入") {
		t.Errorf("合计行要给出来源：%q", row.Source)
	}
	want := int64(0)
	for _, r := range v.Rows {
		if r.Line == "合计5" {
			want = int64(r.Amount)
		}
	}
	if got := keyAmount(t, v, "本期应预扣预缴税额合计"); got != want {
		t.Errorf("关键数与合计行不一致：%d vs %d", got, want)
	}
	// 数据来源要说清是工资单，并注明草稿张数
	foundSrc := false
	for _, s := range v.Sources {
		if strings.Contains(s, "工资单") {
			foundSrc = true
		}
	}
	if !foundSrc {
		t.Error("要写清累计数取自工资单")
	}
	if !hasWarning(v, "草稿") {
		t.Error("★ 用草稿工资单算出来的个税是预计数，必须报出来")
	}
}

func TestTaxReturnIITNoPayroll(t *testing.T) {
	svc := newSvc(t, 6)
	v := taxReturn(t, svc, "iit", 2025, 3, service.TaxReturnInput{})
	if !strings.Contains(v.Concludes, "没有可计算的员工") {
		t.Errorf("没有工资单时的结论 = %q", v.Concludes)
	}
	if !hasWarning(v, "工资模块") {
		t.Error("要告诉用户去哪儿生成工资单")
	}
}

// 税种不认识、期间非法都要报错。
func TestTaxReturnGuardrails(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	if _, err := svc.TaxReturn(ctx, service.TaxReturnInput{
		Kind: "stamp", Year: 2025, Month: 3,
	}); err == nil {
		t.Error("不认识的税种应当报错")
	}
	if _, err := svc.TaxReturn(ctx, service.TaxReturnInput{
		Kind: "vat", Year: 2025, Month: 13,
	}); err == nil {
		t.Error("非法期间应当报错")
	}
	// 没建账时给一句能照着做的提示
	empty, err := service.Open(ctx, service.Options{Path: t.TempDir() + "/empty.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Shutdown()
	if _, err := empty.TaxReturn(ctx, service.TaxReturnInput{
		Kind: "vat", Year: 2025, Month: 3,
	}); err == nil {
		t.Error("没建账时应当报错")
	} else if !strings.Contains(err.Error(), "建账") {
		t.Errorf("提示要指向建账：%v", err)
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func hasWarning(v *service.TaxReturnView, want string) bool {
	for _, w := range v.Warnings {
		if strings.Contains(w, want) {
			return true
		}
	}
	return false
}

func findRowByLabel(t *testing.T, v *service.TaxReturnView, label string) service.TaxReturnRowView {
	t.Helper()
	for _, r := range v.Rows {
		if r.Label == label {
			return r
		}
	}
	t.Fatalf("表里没有「%s」这一行，实际有 %d 行", label, len(v.Rows))
	return service.TaxReturnRowView{}
}

// ★ 个税表的「本期应预扣」必须等于**工资单上本月实际代扣**的个税。
//
// 这条是发布前审计抓到的阻断问题：取数取的是「≤ 本月的最后一张工资单」
// 里固化的 cum_tax_withheld，而那个字段按 payroll 的口径**含本月**，
// 于是「累计应纳税额 − 累计已预扣」恒为 0 —— 个税表的主数字永远是 0，
// 照着办会系统性少扣缴。
//
// 原来的测试拿「关键数」与「合计行」互相比，两边都是 0，所以没抓到。
// 这里断言**绝对数**。
func TestTaxReturnIITMatchesPayrollWithholding(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	if _, err := svc.SaveEmployee(ctx, service.EmployeeInput{
		Code: "E001", Name: "张三", BaseSalary: money100(20_000),
		Enabled: true, DeptID: &dept,
	}); err != nil {
		t.Fatalf("保存员工失败: %v", err)
	}

	// 1、2 月各生成一张工资单，记下每月工资单上的个税
	want := map[int]money.Money{}
	for _, m := range []int{1, 2} {
		d, err := svc.BuildPayroll(ctx, service.BuildPayrollInput{
			Year: 2025, Month: m, CreatedBy: "李会计",
		})
		if err != nil {
			t.Fatalf("生成 %d 月工资单失败: %v", m, err)
		}
		want[m] = d.TotalIIT
	}
	if want[1].IsZero() {
		t.Fatal("前提不成立：1 月工资单上没有个税，这条测试就失去意义了")
	}

	for _, m := range []int{1, 2} {
		v := taxReturn(t, svc, "iit", 2025, m, service.TaxReturnInput{})
		if v.Payable != want[m] {
			t.Errorf("★ %d 月个税表的本期应预扣 = %s，而工资单上代扣的是 %s —— "+
				"两个数必须一致（累计预扣：本月应预扣 = 累计应纳税额 − 截至上月累计已预扣）",
				m, v.Payable, want[m])
		}
		if v.Payable.IsZero() || v.Payable.IsNegative() {
			t.Errorf("%d 月本期应预扣应当是正数，实际 %s", m, v.Payable)
		}
	}

	// 1 月的「累计已预扣」必须是 0（此前没有任何月份）
	v1 := taxReturn(t, svc, "iit", 2025, 1, service.TaxReturnInput{})
	for _, r := range v1.Rows {
		if r.Line == "合计4" && !r.Amount.IsZero() {
			t.Errorf("1 月的累计已预扣应当是 0，实际 %s", r.Amount)
		}
	}
	// 2 月的「累计已预扣」应当是 1 月那一笔
	v2 := taxReturn(t, svc, "iit", 2025, 2, service.TaxReturnInput{})
	for _, r := range v2.Rows {
		if r.Line == "合计4" && r.Amount != want[1] {
			t.Errorf("2 月的累计已预扣应当是 1 月的 %s，实际 %s", want[1], r.Amount)
		}
	}
}

// ★ 企业所得税的「已预缴」只看**借方发生额**（实际缴纳），不看计提。
//
// 发布前审计实测：计提（借 5801/贷 222104）之后用「借方净额」取数，
// 已缴 3,000 被计提冲掉变成 0，本期应补虚增 3,000。
func TestTaxReturnCITPrepaidIgnoresAccrual(t *testing.T) {
	svc := newSvc(t, 6)
	// 3 月计提所得税 3,000（借 5801 / 贷 222104）
	mustPost(t, svc, "2025-03-31", "计提一季度所得税",
		service.VoucherLineInput{AccountCode: "5801", Summary: "计提所得税", Debit: money100(3_000)},
		service.VoucherLineInput{AccountCode: "222104", Summary: "计提所得税", Credit: money100(3_000)})

	v := taxReturn(t, svc, "cit", 2025, 3, service.TaxReturnInput{})
	if got := rowAmount(t, v, "10"); got != 0 {
		t.Errorf("只计提未缴纳时「已预缴」应当是 0，实际 %d（为负说明把计提当成了缴纳）", got)
	}

	// 4 月实际缴纳
	mustPost(t, svc, "2025-04-30", "缴纳一季度所得税",
		service.VoucherLineInput{AccountCode: "222104", Summary: "缴纳所得税", Debit: money100(3_000)},
		service.VoucherLineInput{AccountCode: "1002", Summary: "缴纳所得税", Credit: money100(3_000)})
	v4 := taxReturn(t, svc, "cit", 2025, 4, service.TaxReturnInput{})
	if got := rowAmount(t, v4, "10"); got != int64(money100(3_000)) {
		t.Errorf("★ 已预缴应当是实际缴纳的 3,000.00，实际 %d", got)
	}
}

// ★ 某专栏本期净额为负（红字冲销）时**不能取绝对值**。
//
// 发布前审计实测：1 月销项 13,000，2 月只做一笔红字冲销，
// 取绝对值后 2 月表显示「应补 14,560.00」，而正确的应纳税额是 0。
func TestTaxReturnVATNegativeColumnIsNotAbsolute(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	d := mustSave(t, svc, service.VoucherInput{
		Word: "记", Date: "2025-01-31", Remark: "销售", CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			{AccountCode: "1122", Summary: "销售", Debit: money100(113_000),
				ContactID: wpPtr(mustContact(t, svc, "customer", "甲公司"))},
			{AccountCode: "5001", Summary: "销售", Credit: money100(100_000)},
			{AccountCode: "22210102", Summary: "销项税额", Credit: money100(13_000)},
		},
	})
	mustPostPeriod(t, svc, "2025-01")

	// 2 月红字冲销这张凭证（冲销凭证是立即过账的）
	if _, err := svc.ReverseVoucher(ctx, d.ID, "王主管", "2025-02-28"); err != nil {
		t.Fatalf("红字冲销失败: %v", err)
	}

	v := taxReturn(t, svc, "vat", 2025, 2, service.TaxReturnInput{})
	// 销项税额应当被冲减（负数），而不是变成正的 13,000
	if got := rowAmount(t, v, "11"); got > 0 {
		t.Errorf("★ 冲销后销项税额应当被冲减（≤0），实际 %d —— 取绝对值会凭空造出税额", got)
	}
	if got := keyAmount(t, v, "本期应纳税额（增值税）"); got != 0 {
		t.Errorf("★ 冲销后应纳税额应当是 0，实际 %d（含附加 14,560 的算法就是这里错的）", got)
	}
	if !hasWarning(v, "负") {
		t.Errorf("专栏净额为负必须报出来，实际警告：%v", v.Warnings)
	}
}
