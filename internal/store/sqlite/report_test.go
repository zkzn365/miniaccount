package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/report"
	"miniaccount/internal/domain/voucher"
)

// ---------------------------------------------------------------------------
// 夹具：记一批真实凭证，覆盖资产/负债/权益/收入/费用五类
// ---------------------------------------------------------------------------

// bookFixture 在一个账套里记若干张凭证，返回往来单位 id 映射。
type bookFixture struct {
	DB       *DB
	AccIDs   map[string]int64
	Sharehol int64 // 股东
	Customer int64 // 客户
	Supplier int64 // 供应商
}

// seedBook 构造一个 2025 年 9 月的账套，业务如下：
//
//	① 股东投入实收资本 100,000       借 银行存款 / 贷 实收资本
//	② 收到股东借款 50,000            借 银行存款 / 贷 其他应付款—股东
//	③ 采购办公用品 3,000（未付款）     借 管理费用—办公费 / 贷 应付账款
//	④ 销售商品 80,000 + 销项税 10,400 借 应收账款 / 贷 主营业务收入 + 应交增值税
//	⑤ 支付房租 12,000                借 管理费用—租赁费 / 贷 银行存款
//
//	合计：银行存款 150,000−12,000 = 138,000
//	      应收账款 90,400
//	      管理费用 15,000
//	      应付账款 3,000；应交税费 10,400；其他应付款 50,000；实收资本 100,000
//	      主营业务收入 80,000
//
//	资产 228,400 = 负债 63,400 + 权益(100,000 + 净利润 65,000) ✓
func seedBook(t *testing.T, db *DB) *bookFixture {
	t.Helper()
	ctx := context.Background()
	f := &bookFixture{DB: db}
	var err error
	f.AccIDs, err = db.Accounts().IDsByCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.Sharehol = mustContacts(t, db, "shareholder", "张三")
	f.Customer = mustContacts(t, db, "customer", "杭州某某科技有限公司")
	f.Supplier = mustContacts(t, db, "supplier", "办公用品供应商")

	type line struct {
		code, summary string
		debit, credit money.Money
		contact       *int64
		dept          *int64
	}
	deptAdmin := int64(1) // 管理部门
	post := func(date string, lines ...line) {
		t.Helper()
		v, err := voucher.New(voucher.WordJi, calendar.MustParse(date), "李会计")
		if err != nil {
			t.Fatal(err)
		}
		for _, ln := range lines {
			e := ledger.Entry{
				AccountCode: ln.code, Summary: ln.summary,
				Debit: ln.debit, Credit: ln.credit,
			}
			if ln.contact != nil {
				e.Aux.ContactID = ln.contact
			}
			if ln.dept != nil {
				e.Aux.DeptID = ln.dept
			}
			if err := v.AddEntry(e); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Vouchers().Post(ctx, PostInput{
			Voucher: v, Accounts: f.AccIDs, PostingBy: "王主管", At: time.Now()}); err != nil {
			t.Fatalf("过账失败: %v", err)
		}
	}

	// ① 股东投入实收资本
	post("2025-09-01",
		line{code: "1002", summary: "收到实收资本", debit: money100(100000)},
		line{code: "3001", summary: "收到实收资本", credit: money100(100000), contact: &f.Sharehol},
	)
	// ② 收到股东借款
	post("2025-09-05",
		line{code: "1002", summary: "收到张三借款", debit: money100(50000)},
		line{code: "224101", summary: "收到张三借款", credit: money100(50000), contact: &f.Sharehol},
	)
	// ③ 采购办公用品（未付款）
	// 2202 是明细科目（供应商走辅助核算，不建明细），因此可以直接记账
	post("2025-09-10",
		line{code: "560206", summary: "采购办公用品", debit: money100(3000), dept: &deptAdmin},
		line{code: "2202", summary: "采购办公用品", credit: money100(3000), contact: &f.Supplier},
	)
	// ④ 销售商品
	post("2025-09-15",
		line{code: "1122", summary: "销售商品", debit: money100(90400), contact: &f.Customer},
		line{code: "5001", summary: "销售商品", credit: money100(80000)},
		line{code: "22210102", summary: "销项税额", credit: money100(10400)},
	)
	// ⑤ 支付房租
	post("2025-09-25",
		line{code: "560210", summary: "支付 9 月房租", debit: money100(12000), dept: &deptAdmin},
		line{code: "1002", summary: "支付 9 月房租", credit: money100(12000)},
	)

	return f
}

// ---------------------------------------------------------------------------
// 科目余额表
// ---------------------------------------------------------------------------

func TestTrialBalanceReport(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	f := seedBook(t, db)
	_ = f

	rep, err := db.Reports().TrialBalanceReport(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatalf("生成科目余额表失败: %v", err)
	}
	if len(rep.Rows) != 191 {
		t.Fatalf("行数 = %d，期望 191", len(rep.Rows))
	}

	byCode := map[string]report.BalanceRow{}
	for _, r := range rep.Rows {
		byCode[r.AccountCode] = r
	}

	// 银行存款：期初 0，本期借 150,000 贷 12,000，期末借 138,000
	b := byCode["1002"]
	if b.PeriodDebit != money100(150000) || b.PeriodCredit != money100(12000) {
		t.Errorf("银行存款发生额 = 借%s 贷%s", b.PeriodDebit, b.PeriodCredit)
	}
	if b.ClosingDebit != money100(138000) || b.ClosingCredit != 0 {
		t.Errorf("银行存款期末 = 借%s 贷%s，期望 借138,000.00", b.ClosingDebit, b.ClosingCredit)
	}

	// 其他应付款—股东：贷方 50,000
	s := byCode["224101"]
	if s.ClosingCredit != money100(50000) || s.ClosingDebit != 0 {
		t.Errorf("其他应付款—股东期末 = 借%s 贷%s，期望 贷50,000.00", s.ClosingDebit, s.ClosingCredit)
	}

	// 应收账款：借方 90,400
	ar := byCode["1122"]
	if ar.ClosingDebit != money100(90400) {
		t.Errorf("应收账款期末借 = %s，期望 90,400.00", ar.ClosingDebit)
	}

	// 主营业务收入：贷方发生额 80,000
	rev := byCode["5001"]
	if rev.PeriodCredit != money100(80000) {
		t.Errorf("主营业务收入贷方发生额 = %s，期望 80,000.00", rev.PeriodCredit)
	}

	// ★ 试算平衡：借方合计 = 贷方合计（期初、本期、期末三对都要成立）
	obD, obC, pd, pc, cbD, cbC := rep.Totals()
	if obD != obC {
		t.Errorf("期初借方 %s ≠ 期初贷方 %s", obD, obC)
	}
	if pd != pc {
		t.Errorf("本期借方 %s ≠ 本期贷方 %s", pd, pc)
	}
	if cbD != cbC {
		t.Errorf("期末借方 %s ≠ 期末贷方 %s", cbD, cbC)
	}
	// 本期发生额 = 所有凭证金额之和 × 2（每张凭证借贷各一遍）
	// ①②③④⑤ 金额：100000+50000+3000+90400+12000+15000... 直接算借方合计
	wantDebit := money100(100000).Add(money100(50000)).Add(money100(3000)).
		Add(money100(90400)).Add(money100(12000))
	if pd != wantDebit {
		t.Errorf("本期借方发生额 = %s，期望 %s", pd, wantDebit)
	}
}

// ---------------------------------------------------------------------------
// ★ 资产负债表：全链路 + 官方勾稽关系
// ---------------------------------------------------------------------------

func TestBuildBalanceSheetEndToEnd(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	asOf := calendar.MustParse("2025-09-30")
	closing, opening, issues, err := db.Reports().BuildBalanceSheet(ctx, asOf)
	if err != nil {
		t.Fatalf("生成资产负债表失败: %v", err)
	}
	if len(issues) != 0 {
		for _, is := range issues {
			t.Errorf("勾稽关系不成立: %s", is)
		}
		t.Fatal("资产负债表勾稽关系必须全部成立")
	}

	get := func(d *report.Definition, no int) money.Money {
		l, ok := d.Line(no)
		if !ok {
			t.Fatalf("行 %d 不存在", no)
		}
		return l.Value
	}

	// 货币资金 = 138,000
	if got := get(closing, 1); got != money100(138000) {
		t.Errorf("行1 货币资金 = %s，期望 138,000.00", got)
	}
	// 应收账款 = 90,400（@analyze 取借方余额）
	if got := get(closing, 4); got != money100(90400) {
		t.Errorf("行4 应收账款 = %s，期望 90,400.00", got)
	}
	// 流动资产合计 = 138,000 + 90,400 = 228,400
	if got := get(closing, 15); got != money100(228400) {
		t.Errorf("行15 流动资产合计 = %s，期望 228,400.00", got)
	}
	if got := get(closing, 30); got != money100(228400) {
		t.Errorf("行30 资产总计 = %s，期望 228,400.00", got)
	}
	// 应付账款 = 3,000
	if got := get(closing, 33); got != money100(3000) {
		t.Errorf("行33 应付账款 = %s，期望 3,000.00", got)
	}
	// 应交税费 = 10,400
	if got := get(closing, 36); got != money100(10400) {
		t.Errorf("行36 应交税费 = %s，期望 10,400.00", got)
	}
	// 其他应付款 = 50,000
	if got := get(closing, 39); got != money100(50000) {
		t.Errorf("行39 其他应付款 = %s，期望 50,000.00", got)
	}
	// 流动负债合计 = 3,000 + 10,400 + 50,000 = 63,400
	if got := get(closing, 41); got != money100(63400) {
		t.Errorf("行41 流动负债合计 = %s，期望 63,400.00", got)
	}
	// 实收资本 = 100,000
	if got := get(closing, 48); got != money100(100000) {
		t.Errorf("行48 实收资本 = %s，期望 100,000.00", got)
	}
	// ★ 行53 = 行30（资产 = 负债 + 所有者权益）
	if get(closing, 53) != get(closing, 30) {
		t.Errorf("行53(%s) ≠ 行30(%s)", get(closing, 53), get(closing, 30))
	}

	// 年初余额：本年 1 月 1 日前无业务，应全为 0
	if got := get(opening, 30); got != 0 {
		t.Errorf("年初资产总计 = %s，期望 0.00", got)
	}

	// ★ 隐含验证：净利润必须被自动计入「未分配利润」
	// 收入 80,000 − 管理费用 15,000 = 65,000；权益 = 100,000 + 65,000 = 165,000
	// 负债 63,400 + 权益 165,000 = 228,400 = 资产 ✓
	equity := get(closing, 52)
	if equity != money100(165000) {
		t.Errorf("行52 所有者权益合计 = %s，期望 165,000.00（含本年净利润 65,000）", equity)
	}
}

// ---------------------------------------------------------------------------
// ★ 利润表：两栏（本月数 / 本年累计数）
// ---------------------------------------------------------------------------

func TestBuildIncomeStatementEndToEnd(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	current, ytd, issues, err := db.Reports().BuildIncomeStatement(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatalf("生成利润表失败: %v", err)
	}
	if len(issues) != 0 {
		for _, is := range issues {
			t.Errorf("勾稽关系不成立: %s", is)
		}
		t.Fatal("利润表勾稽关系必须全部成立")
	}

	get := func(d *report.Definition, no int) money.Money {
		l, ok := d.Line(no)
		if !ok {
			t.Fatalf("行 %d 不存在", no)
		}
		return l.Value
	}

	// 营业收入 = 主营业务收入 80,000
	if got := get(current, 1); got != money100(80000) {
		t.Errorf("行1 营业收入 = %s，期望 80,000.00", got)
	}
	// 管理费用 = 办公费 3,000 + 租赁费 12,000 = 15,000
	if got := get(current, 14); got != money100(15000) {
		t.Errorf("行14 管理费用 = %s，期望 15,000.00", got)
	}
	// 营业利润 = 80,000 − 15,000 = 65,000
	if got := get(current, 21); got != money100(65000) {
		t.Errorf("行21 营业利润 = %s，期望 65,000.00", got)
	}
	// 无营业外收支、无所得税 → 利润总额 = 净利润 = 65,000
	if got := get(current, 30); got != money100(65000) {
		t.Errorf("行30 利润总额 = %s，期望 65,000.00", got)
	}
	if got := get(current, 32); got != money100(65000) {
		t.Errorf("行32 净利润 = %s，期望 65,000.00", got)
	}
	// 本月数 = 本年累计数（业务都发生在 9 月）
	if get(current, 32) != get(ytd, 32) {
		t.Errorf("本月净利 %s ≠ 本年累计净利 %s",
			get(current, 32), get(ytd, 32))
	}
}

// 跨月：7、8、9 三个月的累计数应等于各月之和
func TestIncomeStatementYTD(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	f := seedBook(t, db)

	// 7 月再记一笔收入 20,000（无税，简化）
	v, _ := voucher.New(voucher.WordJi, calendar.MustParse("2025-07-10"), "李会计")
	_ = v.AddEntries(
		ledger.Entry{AccountCode: "1122", Summary: "7月销售", Debit: money100(20000),
			Aux: ledger.Aux{ContactID: &f.Customer}},
		ledger.Entry{AccountCode: "5001", Summary: "7月销售", Credit: money100(20000)},
	)
	if _, err := db.Vouchers().Post(ctx, PostInput{
		Voucher: v, Accounts: f.AccIDs, PostingBy: "王主管", At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	current, ytd, _, err := db.Reports().BuildIncomeStatement(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	get := func(d *report.Definition, no int) money.Money {
		l, _ := d.Line(no)
		return l.Value
	}
	// 9 月本月 = 80,000
	if got := get(current, 1); got != money100(80000) {
		t.Errorf("9 月营业收入 = %s，期望 80,000.00", got)
	}
	// 本年累计 = 7 月 20,000 + 9 月 80,000 = 100,000
	if got := get(ytd, 1); got != money100(100000) {
		t.Errorf("本年累计营业收入 = %s，期望 100,000.00", got)
	}
}

// ---------------------------------------------------------------------------
// 往来余额表 / 明细账（对账用）
// ---------------------------------------------------------------------------

func TestContactBalances(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	f := seedBook(t, db)

	rows, err := db.Reports().ContactBalances(ctx, period.NewKey(2025, 9), "")
	if err != nil {
		t.Fatalf("生成往来余额表失败: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("往来余额表不应为空")
	}

	type key struct {
		account string
		contact int64
	}
	byKey := map[key]report.ContactBalanceRow{}
	for _, r := range rows {
		byKey[key{r.AccountCode, r.ContactID}] = r
	}

	// 客户应收 90,400
	ar, ok := byKey[key{"1122", f.Customer}]
	if !ok {
		t.Fatal("缺少客户应收行")
	}
	if ar.Closing != money100(90400) {
		t.Errorf("客户应收期末 = %s，期望 90,400.00", ar.Closing)
	}
	if ar.ContactName != "杭州某某科技有限公司" {
		t.Errorf("客户名 = %q", ar.ContactName)
	}
	if ar.ContactKind != "customer" {
		t.Errorf("往来类型 = %q，期望 customer", ar.ContactKind)
	}

	// 股东往来：收到实收资本 100,000 与借款 50,000，都在贷方
	sh, ok := byKey[key{"224101", f.Sharehol}]
	if !ok {
		t.Fatal("缺少股东往来行")
	}
	if sh.Closing != -money100(50000) {
		t.Errorf("其他应付款—股东期末 = %s，期望 −50,000.00（贷方）", sh.Closing)
	}
	// 按科目筛选
	only, err := db.Reports().ContactBalances(ctx, period.NewKey(2025, 9), "2241")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range only {
		if r.AccountCode[:4] != "2241" {
			t.Errorf("按 2241 筛选却出现 %s", r.AccountCode)
		}
	}
}

// 需求点名的「其他应付款—股东明细账」
func TestShareholderLedger(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	rows, err := db.Vouchers().Detail(ctx, "224101",
		calendar.MustParse("2025-09-01"), calendar.MustParse("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("明细账行数 = %d，期望 1", len(rows))
	}
	if rows[0].Credit != money100(50000) {
		t.Errorf("贷方 = %s，期望 50,000.00", rows[0].Credit)
	}
	if rows[0].VoucherNo != "记-2025-09-0002" {
		t.Errorf("凭证号 = %q", rows[0].VoucherNo)
	}
	if rows[0].ContactID == nil {
		t.Error("明细账必须带出往来单位，否则无法按股东筛选")
	}
}

// ---------------------------------------------------------------------------
// 期初余额：上期结转
// ---------------------------------------------------------------------------

func TestOpeningBalanceCarriesForward(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	// 10 月的科目余额表：期初应等于 9 月的期末
	oct, err := db.Reports().TrialBalanceReport(ctx, period.NewKey(2025, 10))
	if err != nil {
		t.Fatal(err)
	}
	sep, err := db.Reports().TrialBalanceReport(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	sepClose := map[string]money.Money{}
	for _, r := range sep.Rows {
		sepClose[r.AccountCode] = r.ClosingDebit.Sub(r.ClosingCredit)
	}
	for _, r := range oct.Rows {
		got := r.OpeningDebit.Sub(r.OpeningCredit)
		if got != sepClose[r.AccountCode] {
			t.Errorf("科目 %s 10 月期初 = %s，9 月期末 = %s",
				r.AccountCode, got, sepClose[r.AccountCode])
		}
		if !r.PeriodDebit.IsZero() || !r.PeriodCredit.IsZero() {
			t.Errorf("科目 %s 10 月不应有发生额", r.AccountCode)
		}
	}
}

// 10 月的资产负债表应与 9 月末一致（没有新业务）
func TestBalanceSheetUnchangedWithNoActivity(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	sep, _, _, err := db.Reports().BuildBalanceSheet(ctx, calendar.MustParse("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	oct, _, issues, err := db.Reports().BuildBalanceSheet(ctx, calendar.MustParse("2025-10-31"))
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("10 月报表勾稽关系不成立: %v", issues)
	}
	for _, no := range []int{1, 4, 15, 30, 41, 48, 52, 53} {
		ls, _ := sep.Line(no)
		lo, _ := oct.Line(no)
		if ls.Value != lo.Value {
			t.Errorf("行%d（%s）9月=%s 10月=%s，无业务时应相同",
				no, ls.Name, ls.Value, lo.Value)
		}
	}
}

// ★ 汇总科目行必须带下级合计。
//
// 分录只会落在明细科目上，所以按科目直接取数的话
// 「5602 管理费用」这一行的六栏全是 0 —— 而会计打开余额表
// 第一眼要看的恰恰是这一行。下级有钱、上级显示 0，
// 看起来就像账丢了钱。
func TestTrialBalanceRollsUpParentRows(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db) // 其中记了 560206 办公费 3,000 与 560210 房租 12,000

	rep, err := db.Reports().TrialBalanceReport(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatalf("生成科目余额表失败: %v", err)
	}

	var parent report.BalanceRow
	var leafPeriod, leafClosing money.Money
	var leaves int
	for _, r := range rep.Rows {
		if r.AccountCode == "5602" {
			parent = r
		}
		if strings.HasPrefix(r.AccountCode, "5602") && r.IsLeaf {
			leaves++
			leafPeriod = leafPeriod.Add(r.PeriodDebit)
			leafClosing = leafClosing.Add(r.ClosingDebit)
		}
	}
	if leaves == 0 {
		t.Fatal("夹具里 5602 没有任何明细科目发生额")
	}
	if parent.PeriodDebit == 0 {
		t.Fatal("5602 汇总行的本期发生额是 0 —— 汇总行必须带下级合计")
	}
	if parent.PeriodDebit != leafPeriod {
		t.Errorf("5602 汇总行本期借方 = %s，明细行之和 = %s",
			parent.PeriodDebit, leafPeriod)
	}
	if parent.ClosingDebit != leafClosing {
		t.Errorf("5602 汇总行期末借方 = %s，明细行之和 = %s",
			parent.ClosingDebit, leafClosing)
	}
	// 具体数字也要对得上：3,000 办公费 + 12,000 房租
	if parent.PeriodDebit != money100(15000) {
		t.Errorf("5602 本期借方 = %s，期望 15,000.00", parent.PeriodDebit)
	}

	// ★ 试算仍然平衡：Totals() 只加明细科目行，不会因为汇总行而重复计算
	_, _, pd, pc, cbD, cbC := rep.Totals()
	if pd != pc {
		t.Errorf("试算不平衡：本期借 %s ≠ 贷 %s", pd, pc)
	}
	if cbD != cbC {
		t.Errorf("试算不平衡：期末借 %s ≠ 贷 %s", cbD, cbC)
	}
	// 而且这个合计必须等于全账分录之和
	var rawDebit, rawCredit money.Money
	rows, err := db.sql.QueryContext(ctx,
		`SELECT COALESCE(SUM(debit),0), COALESCE(SUM(credit),0)
		   FROM ledger_entry WHERE year = 2025 AND month = 9`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		if err := rows.Scan(&rawDebit, &rawCredit); err != nil {
			t.Fatal(err)
		}
	}
	rows.Close()
	if pd != rawDebit || pc != rawCredit {
		t.Errorf("科目余额表合计 借%s/贷%s ≠ 总账分录 借%s/贷%s",
			pd, pc, rawDebit, rawCredit)
	}
}
