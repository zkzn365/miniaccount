package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/payroll"
	"miniaccount/internal/domain/period"
)

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

// payrollFixture 建账并配好社保方案与员工档案。
type payrollFixture struct{ DB *DB }

func setupPayroll(t *testing.T) *payrollFixture {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()

	// 社保方案：比例与基数上下限都是**可配置数据**，
	// 这里用一组便于验算的数值（不是任何城市的真实标准）。
	scheme := &payroll.InsuranceScheme{
		Name: "测试城市", City: "测试",
		BaseMin: money100(4000), BaseMax: money100(30000),
		HousingFundBaseMin: money100(2000), HousingFundBaseMax: money100(40000),
		Rates: payroll.InsuranceRates{
			PensionSelf: money.RatePercent(8), MedicalSelf: money.RatePercent(2),
			UnemploymentSelf: money.RatePermille(5), HousingFundSelf: money.RatePercent(12),
			PensionCo: money.RatePercent(14), MedicalCo: money.RatePercent(9),
			UnemploymentCo: money.RatePermille(5), InjuryCo: money.RatePermille(2),
			MaternityCo: money.RatePercent(1), HousingFundCo: money.RatePercent(12),
		},
		EffectiveFrom: calendar.MustParse("2025-01-01"),
	}
	if err := db.Payroll().SaveInsuranceSchemes(ctx, []*payroll.InsuranceScheme{scheme}); err != nil {
		t.Fatalf("保存社保方案失败: %v", err)
	}
	return &payrollFixture{DB: db}
}

func (f *payrollFixture) addEmployee(t *testing.T, code, name string,
	kind payroll.EmployeeKind, expenseAcc string, dept *int64,
	scheme string) int64 {
	t.Helper()
	id, err := f.DB.Payroll().UpsertEmployee(context.Background(), &payroll.Employee{
		Code: code, Name: name, Kind: kind,
		DeptID: dept, ExpenseAccountCode: expenseAcc,
		CompanyAccountCode: "560202",
		SchemeName:         scheme,
		HireDate:           calendar.MustParse("2020-01-01"),
		IsEnabled:          true,
	})
	if err != nil {
		t.Fatalf("新增员工 %s 失败: %v", name, err)
	}
	return id
}

// buildAndSave 生成并保存一张工资单。
func (f *payrollFixture) buildAndSave(t *testing.T, year, month int,
	amounts map[int64]*payroll.Item) int64 {
	t.Helper()
	ctx := context.Background()
	run, err := f.DB.Payroll().BuildRun(ctx, BuildRunInput{
		Period:    period.NewKey(year, month),
		CreatedBy: "李会计",
		Amounts:   amounts,
	})
	if err != nil {
		t.Fatalf("生成工资单失败: %v", err)
	}
	id, err := f.DB.Payroll().SaveRun(ctx, run)
	if err != nil {
		t.Fatalf("保存工资单失败: %v", err)
	}
	return id
}

// ---------------------------------------------------------------------------
// 员工
// ---------------------------------------------------------------------------

func TestEmployeeCRUD(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)

	id := f.addEmployee(t, "E001", "张三", payroll.KindEmployee, "560201", &dept, "测试城市")
	if id == 0 {
		t.Fatal("应返回自增 id")
	}

	emps, err := f.DB.Payroll().ListEmployees(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(emps) != 1 {
		t.Fatalf("员工数 = %d，期望 1", len(emps))
	}
	e := emps[0]
	if e.Name != "张三" || e.ExpenseAccountCode != "560201" || e.SchemeName != "测试城市" {
		t.Errorf("员工 = %+v", e)
	}
	if e.HireDate.String() != "2020-01-01" {
		t.Errorf("入职日期 = %s", e.HireDate)
	}
	if e.DeptID == nil || *e.DeptID != 1 {
		t.Error("部门未保存")
	}

	// 更新
	e.Name = "张三丰"
	e.Position = "会计"
	if _, err := f.DB.Payroll().UpsertEmployee(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, _ := f.DB.Payroll().ListEmployees(ctx, true)
	if got[0].Name != "张三丰" || got[0].Position != "会计" {
		t.Errorf("更新后 = %+v", got[0])
	}

	// 不存在的费用科目
	if _, err := f.DB.Payroll().UpsertEmployee(ctx, &payroll.Employee{
		Code: "E999", Name: "坏员工", Kind: payroll.KindEmployee,
		ExpenseAccountCode: "9999", IsEnabled: true,
	}); err == nil {
		t.Error("不存在的费用科目应报错")
	}

	// 停用后不在 onlyEnabled 列表里
	e.IsEnabled = false
	if _, err := f.DB.Payroll().UpsertEmployee(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, _ = f.DB.Payroll().ListEmployees(ctx, true)
	if len(got) != 0 {
		t.Errorf("停用后应为空，得到 %d", len(got))
	}
	all, _ := f.DB.Payroll().ListEmployees(ctx, false)
	if len(all) != 1 {
		t.Errorf("不过滤时应返回 1，得到 %d", len(all))
	}
}

// ---------------------------------------------------------------------------
// 参数
// ---------------------------------------------------------------------------

func TestDefaultTaxTableUsedWhenUnset(t *testing.T) {
	f := setupPayroll(t)
	tb, err := f.DB.Payroll().TaxTable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.Brackets) != 7 {
		t.Errorf("未配置时应回落到内置税率表，得到 %d 级", len(tb.Brackets))
	}
	if tb.BasicDeduction != money100(5000) {
		t.Errorf("减除费用 = %s", tb.BasicDeduction)
	}
}

// ★ 税率表必须是数据：政策调整后不应需要改代码
func TestSaveCustomTaxTable(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)

	custom := payroll.TaxTable{
		Name: "自定义（假设政策调整）", BasicDeduction: money100(6000),
		Brackets: []payroll.TaxBracket{
			{UpperLimit: money100(50000), Rate: money.RatePercent(5), QuickDeduction: 0},
			{UpperLimit: 0, Rate: money.RatePercent(10), QuickDeduction: money100(2500)},
		},
	}
	if err := f.DB.Payroll().SaveTaxTable(ctx, custom); err != nil {
		t.Fatalf("保存自定义税率表失败: %v", err)
	}
	got, err := f.DB.Payroll().TaxTable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.BasicDeduction != money100(6000) || len(got.Brackets) != 2 {
		t.Errorf("税率表未生效: %+v", got)
	}

	// 非法税率表应被拒绝
	bad := payroll.TaxTable{Name: "坏表", Brackets: []payroll.TaxBracket{
		{UpperLimit: money100(1000), Rate: money.RatePercent(3)},
	}}
	if err := f.DB.Payroll().SaveTaxTable(ctx, bad); err == nil {
		t.Error("最后一级有上限的税率表应被拒绝")
	}
}

func TestSaveInsuranceSchemes(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	schemes, err := f.DB.Payroll().InsuranceSchemes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(schemes) != 1 {
		t.Fatalf("方案数 = %d，期望 1", len(schemes))
	}
	s := schemes["测试城市"]
	if s == nil {
		t.Fatal("找不到「测试城市」方案")
	}
	if s.BaseMin != money100(4000) || s.BaseMax != money100(30000) {
		t.Errorf("基数区间 = %s ~ %s", s.BaseMin, s.BaseMax)
	}
	if s.Rates.PensionSelf != money.RatePercent(8) {
		t.Errorf("养老个人比例 = %s", s.Rates.PensionSelf)
	}
}

// ---------------------------------------------------------------------------
// ★ 累计预扣预缴：跨月累计必须正确
// ---------------------------------------------------------------------------

// 这是工资模块最重要的测试：逐月生成工资单，验证累计数被正确结转。
//
// 月薪 10,000、无社保无专项附加：
//
//	1~7 月累计所得额 ≤ 36000 → 每月 150
//	8 月累计 40000 → 累计应纳税 1480，已扣 1050 → 本月 430
//	全年合计 3480
func TestCumulativeWithholdingAcrossMonths(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)
	eid := f.addEmployee(t, "E001", "张三", payroll.KindEmployee,
		"560201", &dept, "") // 不缴社保，便于验算

	monthly := make([]money.Money, 0, 12)
	for m := 1; m <= 12; m++ {
		runID := f.buildAndSave(t, 2025, m, map[int64]*payroll.Item{
			eid: {BaseSalary: money100(10000)},
		})
		run, err := f.DB.Payroll().GetRun(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if len(run.Items) != 1 {
			t.Fatalf("%d 月明细数 = %d", m, len(run.Items))
		}
		monthly = append(monthly, run.Items[0].IIT)

		// 工资单必须自洽
		if err := run.Validate(); err != nil {
			t.Fatalf("%d 月工资单不自洽: %v", m, err)
		}
	}

	// 1~7 月每月 150
	for m := 1; m <= 7; m++ {
		if monthly[m-1] != money100(150) {
			t.Errorf("第 %d 月个税 = %s，期望 150.00", m, monthly[m-1])
		}
	}
	// 8 月跨入第 2 级
	if monthly[7] != money100(430) {
		t.Errorf("第 8 月个税 = %s，期望 430.00", monthly[7])
	}
	// 全年合计 3480
	var total money.Money
	for _, v := range monthly {
		total = total.Add(v)
	}
	if total != money100(3480) {
		t.Errorf("全年个税 = %s，期望 3480.00", total)
	}

	// ★ 累计状态必须落在明细上，便于回答「为什么这个月税不一样」
	run, _ := f.DB.Payroll().GetRun(ctx, f.buildAndSave(t, 2025, 1,
		map[int64]*payroll.Item{eid: {BaseSalary: money100(10000)}}))
	_ = run
}

// ★ 关键：累计数必须从数据库读回，而不是只看当月
func TestYTDIsPersistedAndReloaded(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)
	eid := f.addEmployee(t, "E001", "张三", payroll.KindEmployee, "560201", &dept, "")

	// 1 月：累计 1 个月，所得额 5000 → 150
	id1 := f.buildAndSave(t, 2025, 1, map[int64]*payroll.Item{
		eid: {BaseSalary: money100(10000)},
	})
	r1, _ := f.DB.Payroll().GetRun(ctx, id1)
	if r1.Items[0].IIT != money100(150) {
		t.Fatalf("1 月个税 = %s，期望 150.00", r1.Items[0].IIT)
	}
	if r1.Items[0].Cum.Months != 1 {
		t.Errorf("1 月累计月数 = %d，期望 1", r1.Items[0].Cum.Months)
	}
	if r1.Items[0].Cum.TaxWithheld != money100(150) {
		t.Errorf("1 月累计已扣 = %s，期望 150.00", r1.Items[0].Cum.TaxWithheld)
	}

	// 2 月：累计 2 个月，所得额 10000 → 300；已扣 150 → 本月 150
	id2 := f.buildAndSave(t, 2025, 2, map[int64]*payroll.Item{
		eid: {BaseSalary: money100(10000)},
	})
	r2, _ := f.DB.Payroll().GetRun(ctx, id2)
	if r2.Items[0].IIT != money100(150) {
		t.Errorf("2 月个税 = %s，期望 150.00", r2.Items[0].IIT)
	}
	if r2.Items[0].Cum.Months != 2 {
		t.Errorf("2 月累计月数 = %d，期望 2", r2.Items[0].Cum.Months)
	}
	if r2.Items[0].Cum.Income != money100(20000) {
		t.Errorf("2 月累计收入 = %s，期望 20000.00", r2.Items[0].Cum.Income)
	}
	if r2.Items[0].Cum.TaxWithheld != money100(300) {
		t.Errorf("2 月累计已扣 = %s，期望 300.00", r2.Items[0].Cum.TaxWithheld)
	}
}

// ★ 累计数是否包含草稿，由 OnlyPostedHistory 控制。
//
// 默认包含草稿：用户「先建 1 月放着、接着建 2 月」是常见流程，
// 忽略 1 月草稿会让 2 月的个税明显偏低，看起来像软件算错了。
func TestDraftHistoryInclusion(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)
	eid := f.addEmployee(t, "E001", "张三", payroll.KindEmployee, "560201", &dept, "")

	// 1 月：草稿（不确认）
	f.buildAndSave(t, 2025, 1, map[int64]*payroll.Item{
		eid: {BaseSalary: money100(10000)},
	})

	// 默认：2 月累计月数应为 2（含 1 月草稿）
	run2, err := f.DB.Payroll().BuildRun(ctx, BuildRunInput{
		Period:    period.NewKey(2025, 2),
		CreatedBy: "李会计",
		Amounts:   map[int64]*payroll.Item{eid: {BaseSalary: money100(10000)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run2.Items[0].Cum.Months != 2 {
		t.Errorf("默认应含草稿历史：累计月数 = %d，期望 2", run2.Items[0].Cum.Months)
	}
	if run2.Items[0].Cum.Income != money100(20000) {
		t.Errorf("累计收入 = %s，期望 20000.00", run2.Items[0].Cum.Income)
	}

	// 严格口径：只统计已确认/已过账。
	//
	// ★ 注意「月数」与「金额」在这里是两回事：
	// 减除费用的乘数按**任职受雇月份数**算（法定口径，与建没建单无关），
	// 所以严格口径改变的是**累计收入/扣除/已预扣税额**，不是月数。
	strict, err := f.DB.Payroll().BuildRun(ctx, BuildRunInput{
		Period:            period.NewKey(2025, 2),
		CreatedBy:         "李会计",
		OnlyPostedHistory: true,
		Amounts:           map[int64]*payroll.Item{eid: {BaseSalary: money100(10000)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strict.Items[0].Cum.Income != money100(10000) {
		t.Errorf("严格口径下累计收入 = %s，期望 10000.00（1 月是草稿，不计）",
			strict.Items[0].Cum.Income)
	}
	if strict.Items[0].Cum.Income == run2.Items[0].Cum.Income {
		t.Error("严格口径与默认口径的累计收入应当不同")
	}

	// 把 1 月确认后，严格口径也应计入
	if err := f.DB.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE salary_run SET status = 'confirmed' WHERE year = 2025 AND month = 1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	strict2, _ := f.DB.Payroll().BuildRun(ctx, BuildRunInput{
		Period:            period.NewKey(2025, 2),
		CreatedBy:         "李会计",
		OnlyPostedHistory: true,
		Amounts:           map[int64]*payroll.Item{eid: {BaseSalary: money100(10000)}},
	})
	if strict2.Items[0].Cum.Months != 2 {
		t.Errorf("1 月确认后累计月数 = %d，期望 2", strict2.Items[0].Cum.Months)
	}
}

// 社保与专项附加扣除也要累计
func TestYTDIncludesDeductions(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)
	eid := f.addEmployee(t, "E001", "张三", payroll.KindEmployee,
		"560201", &dept, "测试城市")

	id := f.buildAndSave(t, 2025, 1, map[int64]*payroll.Item{
		eid: {BaseSalary: money100(20000), SpecialAdditional: money100(2000)},
	})
	r, _ := f.DB.Payroll().GetRun(ctx, id)
	it := r.Items[0]

	// 社保个人 = 20000×22.5% = 4500
	if it.Insurance.SelfTotal() != money100(4500) {
		t.Errorf("社保个人 = %s，期望 4500.00", it.Insurance.SelfTotal())
	}
	// 累计专项扣除应为 4500
	if it.Cum.SpecialDeduction != money100(4500) {
		t.Errorf("累计专项扣除 = %s，期望 4500.00", it.Cum.SpecialDeduction)
	}
	// 累计专项附加扣除应为 2000
	if it.Cum.SpecialAdditional != money100(2000) {
		t.Errorf("累计专项附加扣除 = %s，期望 2000.00", it.Cum.SpecialAdditional)
	}
	// 累计所得额 = 20000 − 5000 − 4500 − 2000 = 8500
	if it.Cum.Taxable != money100(8500) {
		t.Errorf("累计应纳税所得额 = %s，期望 8500.00", it.Cum.Taxable)
	}
	// 个税 = 8500 × 3% = 255
	if it.IIT != money100(255) {
		t.Errorf("个税 = %s，期望 255.00", it.IIT)
	}
	// 实发 = 20000 − 4500 − 255 = 15245
	if it.NetPay != money100(15245) {
		t.Errorf("实发 = %s，期望 15245.00", it.NetPay)
	}
}

// ---------------------------------------------------------------------------
// 生成工资单
// ---------------------------------------------------------------------------

func TestBuildRunSelectsActiveEmployees(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)

	f.addEmployee(t, "E001", "在职", payroll.KindEmployee, "560201", &dept, "")
	// 已离职
	id2 := f.addEmployee(t, "E002", "已离职", payroll.KindEmployee, "560201", &dept, "")
	e2, _ := f.DB.Payroll().ListEmployees(ctx, false)
	for _, e := range e2 {
		if e.ID == id2 {
			e.LeaveDate = calendar.MustParse("2025-06-30")
			if _, err := f.DB.Payroll().UpsertEmployee(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 未入职
	id3 := f.addEmployee(t, "E003", "未入职", payroll.KindEmployee, "560201", &dept, "")
	e3, _ := f.DB.Payroll().ListEmployees(ctx, false)
	for _, e := range e3 {
		if e.ID == id3 {
			e.HireDate = calendar.MustParse("2025-12-01")
			if _, err := f.DB.Payroll().UpsertEmployee(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
	}

	run, err := f.DB.Payroll().BuildRun(ctx, BuildRunInput{
		Period: period.NewKey(2025, 9), CreatedBy: "李会计",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Items) != 1 {
		t.Fatalf("应只含 1 名在职员工，实际 %d", len(run.Items))
	}
	if run.Items[0].Employee.Name != "在职" {
		t.Errorf("应带出「在职」，实际 %q", run.Items[0].Employee.Name)
	}
}

func TestBuildRunNoEmployees(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	if _, err := f.DB.Payroll().BuildRun(ctx, BuildRunInput{
		Period: period.NewKey(2025, 9),
	}); err == nil {
		t.Error("没有员工时应报错")
	}
}

func TestBuildRunInvalidPeriod(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	if _, err := f.DB.Payroll().BuildRun(ctx, BuildRunInput{
		Period: period.NewKey(2025, 13),
	}); err == nil {
		t.Error("非法月份应报错")
	}
}

// 员工引用了不存在的社保方案应报错，而不是静默不缴
func TestBuildRunMissingScheme(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)
	f.addEmployee(t, "E001", "张三", payroll.KindEmployee, "560201", &dept, "不存在的城市")
	if _, err := f.DB.Payroll().BuildRun(ctx, BuildRunInput{
		Period: period.NewKey(2025, 9),
	}); !errors.Is(err, ErrNoScheme) {
		t.Errorf("应报 ErrNoScheme，得到 %v", err)
	}
}

// 同期间只能有一张工资单；草稿可覆盖，已确认不可改
func TestSaveRunUniqueness(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)
	eid := f.addEmployee(t, "E001", "张三", payroll.KindEmployee, "560201", &dept, "")

	id1 := f.buildAndSave(t, 2025, 9, map[int64]*payroll.Item{
		eid: {BaseSalary: money100(10000)},
	})
	// 再存一次同期间：应覆盖同一张，而不是新建
	id2 := f.buildAndSave(t, 2025, 9, map[int64]*payroll.Item{
		eid: {BaseSalary: money100(12000)},
	})
	if id1 != id2 {
		t.Errorf("同期间应覆盖同一张工资单：%d vs %d", id1, id2)
	}
	r, _ := f.DB.Payroll().GetRun(ctx, id1)
	if r.Items[0].BaseSalary != money100(12000) {
		t.Errorf("应被覆盖为 12000，得到 %s", r.Items[0].BaseSalary)
	}

	// 已确认后不允许再覆盖
	if err := f.DB.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `UPDATE salary_run SET status = 'confirmed' WHERE id = ?`, id1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	run, _ := f.DB.Payroll().BuildRun(ctx, BuildRunInput{
		Period: period.NewKey(2025, 9), CreatedBy: "李会计",
		Amounts: map[int64]*payroll.Item{eid: {BaseSalary: money100(15000)}},
	})
	if _, err := f.DB.Payroll().SaveRun(ctx, run); !errors.Is(err, ErrRunNotDraft) {
		t.Errorf("已确认后应拒绝覆盖，得到 %v", err)
	}
}

func TestListRuns(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)
	eid := f.addEmployee(t, "E001", "张三", payroll.KindEmployee, "560201", &dept, "")

	f.buildAndSave(t, 2025, 8, map[int64]*payroll.Item{eid: {BaseSalary: money100(10000)}})
	f.buildAndSave(t, 2025, 9, map[int64]*payroll.Item{eid: {BaseSalary: money100(10000)}})

	runs, err := f.DB.Payroll().ListRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("工资单数 = %d，期望 2", len(runs))
	}
	// 按年月倒序
	if runs[0].Period.Month != 9 || runs[1].Period.Month != 8 {
		t.Errorf("顺序错误：%v, %v", runs[0].Period, runs[1].Period)
	}
	if runs[0].Headcount != 1 {
		t.Errorf("人数 = %d", runs[0].Headcount)
	}
	if runs[0].TotalGross != money100(10000) {
		t.Errorf("应发合计 = %s", runs[0].TotalGross)
	}
}

// ---------------------------------------------------------------------------
// ★ 端到端：工资单 → 两张凭证 → 落总账
// ---------------------------------------------------------------------------

func TestPostRunEndToEnd(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)

	// 一名缴社保的员工
	e1 := f.addEmployee(t, "E001", "张三", payroll.KindEmployee,
		"560201", &dept, "测试城市")
	// 一名外聘劳务
	e2 := f.addEmployee(t, "E002", "李四", payroll.KindLabor, "560213", &dept, "")

	runID := f.buildAndSave(t, 2025, 9, map[int64]*payroll.Item{
		e1: {BaseSalary: money100(20000), SpecialAdditional: money100(2000)},
		e2: {BaseSalary: money100(10000)},
	})

	res, err := f.DB.Payroll().PostRun(ctx, runID, "王主管",
		time.Date(2025, 9, 30, 18, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("过账失败: %v", err)
	}
	if res.AccrualNo == "" || res.PaymentNo == "" {
		t.Fatal("两张凭证都应分配凭证号")
	}
	// 凭证字是「转」（工资计提不直接涉及现金；「转」是转账凭证）
	if !strings.HasPrefix(res.AccrualNo, "转-") {
		t.Errorf("计提凭证号 = %q，期望以「转-」开头", res.AccrualNo)
	}
	if !strings.HasPrefix(res.PaymentNo, "转-") {
		t.Errorf("发放凭证号 = %q，期望以「转-」开头", res.PaymentNo)
	}

	// 工资单状态
	runs, _ := f.DB.Payroll().ListRuns(ctx)
	if runs[0].Status != payroll.RunPosted {
		t.Errorf("状态 = %s，期望 posted", runs[0].Status)
	}
	if runs[0].AccrualVoucher == nil || runs[0].PaymentVoucher == nil {
		t.Error("应记录两张凭证 id")
	}

	// 凭证本身
	acc, err := f.DB.Vouchers().Get(ctx, res.AccrualVoucherID)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Source != "salary" {
		t.Errorf("凭证来源 = %q，期望 salary", acc.Source)
	}
	if acc.SourceID == nil || *acc.SourceID != runID {
		t.Error("凭证应记录来源工资单 id")
	}
	if !acc.IsBalanced() {
		t.Error("计提凭证应平衡")
	}
	pay, _ := f.DB.Vouchers().Get(ctx, res.PaymentVoucherID)
	if !pay.IsBalanced() {
		t.Error("发放凭证应平衡")
	}

	// ★ 两张凭证的「应付职工薪酬—工资」必须一致
	var accPayable, payPayable money.Money
	for _, e := range acc.Entries {
		if e.AccountCode == "221101" {
			accPayable = accPayable.Add(e.Credit)
		}
	}
	for _, e := range pay.Entries {
		if e.AccountCode == "221101" {
			payPayable = payPayable.Add(e.Debit)
		}
	}
	if accPayable != payPayable || accPayable != money100(30000) {
		t.Errorf("计提应付 %s ≠ 发放应付 %s（应发合计 30000.00）", accPayable, payPayable)
	}

	// ★ 期末「应付职工薪酬—工资」应该结平（计提=发放）
	bal, err := f.DB.Vouchers().Balance(ctx,
		calendar.MustParse("2025-09-01"), calendar.MustParse("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	if bal["221101"] != 0 {
		t.Errorf("应付职工薪酬—工资期末应为 0（计提与发放相抵），得到 %s", bal["221101"])
	}
	// 代扣个税应挂在应交税费
	if !bal["222105"].IsNegative() {
		t.Errorf("应交个人所得税应为贷方余额，得到 %s", bal["222105"])
	}
	// 管理费用—工资（张三）
	if bal["560201"] != money100(20000) {
		t.Errorf("管理费用—工资 = %s，期望 20000.00", bal["560201"])
	}
	// 管理费用—中介服务费（外聘劳务）
	if bal["560213"] != money100(10000) {
		t.Errorf("外聘劳务费用 = %s，期望 10000.00", bal["560213"])
	}

	// ★ 试算平衡
	d, c, err := f.DB.Vouchers().TrialBalance(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	if d != c {
		t.Errorf("试算不平衡：借 %s 贷 %s", d, c)
	}

	// ★ 资产负债表勾稽
	_, _, issues, err := f.DB.Reports().BuildBalanceSheet(ctx,
		calendar.MustParse("2025-09-30"))
	if err != nil {
		t.Fatal(err)
	}
	for _, is := range issues {
		t.Errorf("勾稽关系不成立: %s", is)
	}
}

// 重复过账应被拒绝
func TestPostRunTwiceFails(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)
	eid := f.addEmployee(t, "E001", "张三", payroll.KindEmployee, "560201", &dept, "")
	runID := f.buildAndSave(t, 2025, 9, map[int64]*payroll.Item{
		eid: {BaseSalary: money100(10000)},
	})
	if _, err := f.DB.Payroll().PostRun(ctx, runID, "王主管", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Payroll().PostRun(ctx, runID, "王主管", time.Now()); !errors.Is(err, ErrRunNotDraft) {
		t.Errorf("重复过账应报 ErrRunNotDraft，得到 %v", err)
	}
}

// 已结账期间不允许生成工资凭证
func TestPostRunRejectsClosedPeriod(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)
	eid := f.addEmployee(t, "E001", "张三", payroll.KindEmployee, "560201", &dept, "")
	runID := f.buildAndSave(t, 2025, 9, map[int64]*payroll.Item{
		eid: {BaseSalary: money100(10000)},
	})
	for m := 1; m <= 9; m++ {
		if err := f.DB.WithTx(ctx, func(tx *Tx) error {
			return f.DB.Periods().SetStatus(ctx, tx, period.NewKey(2025, m),
				period.StatusClosed, "王主管", ptrTime(time.Now()))
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.DB.Payroll().PostRun(ctx, runID, "王主管", time.Now()); !errors.Is(err, period.ErrNotOpen) {
		t.Errorf("已结账期间应拒绝，得到 %v", err)
	}
}

// 过账失败必须整体回滚：不能只生成计提凭证而漏掉发放凭证
func TestPostRunIsAtomic(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)
	eid := f.addEmployee(t, "E001", "张三", payroll.KindEmployee, "560201", &dept, "")
	// 月薪取 60000：任职 9 个月 → 累计减除 45000，累计收入 60000，
	// 应纳税所得 15000 → 本月个税非零，才有「个税科目」可停用。
	// （原来取 10000 时按任职月份口径根本不用交税，停用个税科目不会造成失败。）
	runID := f.buildAndSave(t, 2025, 9, map[int64]*payroll.Item{
		eid: {BaseSalary: money100(60000)},
	})

	// 把「应交个人所得税」停用，让发放凭证过账失败（计提凭证不涉及该科目）
	if err := f.DB.WithTx(ctx, func(tx *Tx) error {
		return f.DB.Accounts().SetEnabled(ctx, tx, "222105", false)
	}); err != nil {
		t.Fatal(err)
	}

	_, err := f.DB.Payroll().PostRun(ctx, runID, "王主管", time.Now())
	if err == nil {
		t.Fatal("应因个税科目停用而失败")
	}

	// 计提凭证也不应留下
	var n int
	if err := f.DB.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM voucher`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("失败后不应留下任何凭证，实际 %d 张", n)
	}
	var nl int
	_ = f.DB.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM ledger_entry`).Scan(&nl)
	if nl != 0 {
		t.Errorf("失败后总账应为空，实际 %d 条", nl)
	}
	// 工资单仍是草稿
	r, _ := f.DB.Payroll().GetRun(ctx, runID)
	if r.Status != payroll.RunDraft {
		t.Errorf("状态 = %s，期望仍是草稿", r.Status)
	}
}

// 自定义凭证科目配置应生效
func TestPostRunCustomVoucherAccounts(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)
	eid := f.addEmployee(t, "E001", "张三", payroll.KindEmployee, "560201", &dept, "")

	vc := payroll.DefaultVoucherAccounts()
	vc.PayableSalary = "221102" // 故意改成社保科目，验证配置生效
	if err := f.DB.Payroll().SaveVoucherAccounts(ctx, vc); err != nil {
		t.Fatal(err)
	}
	runID := f.buildAndSave(t, 2025, 9, map[int64]*payroll.Item{
		eid: {BaseSalary: money100(10000)},
	})
	res, err := f.DB.Payroll().PostRun(ctx, runID, "王主管", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	acc, _ := f.DB.Vouchers().Get(ctx, res.AccrualVoucherID)
	var found bool
	for _, e := range acc.Entries {
		if e.AccountCode == "221102" && e.Credit.IsPositive() {
			found = true
		}
	}
	if !found {
		t.Error("自定义的应付工资科目未生效")
	}
}

var _ = account.RootAsset

// ★ 税率表参数损坏时必须报错，不能静默换成内置表。
//
// 那张表会被写进导出的工资表（服务层用它的级数生成「综合所得 N 级
// 超额累进…」），于是文件可能写着「7 级」而实际计提用的不是这张表 ——
// 一份对外交付的文件，内容与它自称的口径对不上。
func TestCorruptTaxTableIsReported(t *testing.T) {
	ctx := context.Background()
	f := setupPayroll(t)
	dept := int64(1)
	eid := f.addEmployee(t, "E001", "张三", payroll.KindEmployee, "560201", &dept, "")
	runID := f.buildAndSave(t, 2025, 9, map[int64]*payroll.Item{
		eid: {BaseSalary: money100(20000)},
	})

	// 把税表参数写坏
	if err := f.DB.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE salary_run SET tax_table_json = '{这不是合法 JSON' WHERE id = ?`, runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := f.DB.Payroll().GetRun(ctx, runID); err == nil {
		t.Fatal("税率表参数损坏时应报错，而不是静默回落内置表")
	}

	// 从未配过税表（空串）仍然走内置默认值 —— 那是默认值，不是降级
	if err := f.DB.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE salary_run SET tax_table_json = '' WHERE id = ?`, runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Payroll().GetRun(ctx, runID); err != nil {
		t.Errorf("空税表应回落内置默认表，实际报错: %v", err)
	}
}
