package asset_test

import (
	"strings"
	"testing"

	"miniaccount/internal/domain/asset"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

func yuan(n int64) money.Money { return money.Money(n) * money.Yuan }

func date(t *testing.T, s string) calendar.Date {
	t.Helper()
	d, err := calendar.Parse(s)
	if err != nil {
		t.Fatalf("日期 %q 解析失败: %v", s, err)
	}
	return d
}

// 一台 12,000 元的电脑，3 年（36 期），残值率 5%，2025-03-15 投入使用。
func laptop(t *testing.T) asset.FixedAsset {
	t.Helper()
	return asset.FixedAsset{
		Name: "笔记本电脑", Category: asset.CatElectronic,
		OrigValue: yuan(12000), SalvagePPM: 50_000, UsefulMonths: 36,
		StartDate:      date(t, "2025-03-15"),
		ExpenseAccount: "560205", AccumAccount: "1602",
		Status: asset.StatusInUse,
	}
}

// ---------------------------------------------------------------------------
// ★ 起提时点：当月增加当月不提，次月起提
// ---------------------------------------------------------------------------

func TestDepreciationStartsNextMonth(t *testing.T) {
	a := laptop(t)
	first := a.FirstPeriod()
	if first.Year != 2025 || first.Month != 4 {
		t.Fatalf("★ 2025-03 投入使用的固定资产，应从 2025-04 起提，实际 %s", first)
	}
	// 当月（3 月）一分钱都不提
	amt, why := a.AmountFor(period.NewKey(2025, 3), 0)
	if !amt.IsZero() {
		t.Errorf("★ 投入使用当月不该计提，却算出 %s", amt)
	}
	if !strings.Contains(why, "次月起提") {
		t.Errorf("要说明为什么是 0，实际 %q", why)
	}
	// 次月提
	amt, why = a.AmountFor(period.NewKey(2025, 4), 0)
	if amt.IsZero() {
		t.Fatalf("次月应当计提，却算出 0（%s）", why)
	}
}

func TestDepreciationLastPeriod(t *testing.T) {
	a := laptop(t)
	// 36 期，首期 2025-04 → 末期 2028-03
	last := a.LastPeriod()
	if last.Year != 2028 || last.Month != 3 {
		t.Fatalf("最后一期应为 2028-03，实际 %s", last)
	}
	// 末期之后不再提
	if amt, _ := a.AmountFor(period.NewKey(2028, 4), a.DepreciableBase()); !amt.IsZero() {
		t.Errorf("过了使用年限还在提：%s", amt)
	}
}

// ★ 尾差：应提总额必须**恰好**提完，一分不多一分不少
func TestDepreciationScheduleSumsExactly(t *testing.T) {
	cases := []struct {
		name    string
		orig    int64 // 元
		months  int
		salvage string
	}{
		{"除得尽", 12000, 36, "0"},
		{"除不尽·3 期", 100000, 3, "0"},
		{"除不尽·7 期", 10000, 7, "0"},
		{"带残值", 12000, 36, "0.05"},
		{"带残值且除不尽", 99999, 13, "0.03"},
		{"1 期", 5000, 1, "0.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ppm := int64(0)
			switch c.salvage {
			case "0.05":
				ppm = 50_000
			case "0.03":
				ppm = 30_000
			case "0.1":
				ppm = 100_000
			}
			a := asset.FixedAsset{
				Name: "测试资产", OrigValue: yuan(c.orig), SalvagePPM: ppm,
				UsefulMonths: c.months, StartDate: date(t, "2025-01-10"),
				ExpenseAccount: "560205", Status: asset.StatusInUse,
			}
			sched := a.Schedule()
			if len(sched) != c.months {
				t.Fatalf("折旧表期数 = %d，期望 %d", len(sched), c.months)
			}
			var sum money.Money
			for _, it := range sched {
				sum = sum.Add(it.Amount)
			}
			if sum != a.DepreciableBase() {
				t.Errorf("★ 各期之和 %s ≠ 应提总额 %s，差 %s —— "+
					"尾差没在最后一期兜住，这个差额会永远挂在账上",
					sum, a.DepreciableBase(), a.DepreciableBase().Sub(sum))
			}
			// 逐期累计必须与表里写的一致，且不得超过应提总额
			var cum money.Money
			for i, it := range sched {
				cum = cum.Add(it.Amount)
				if it.Cumulative != cum {
					t.Errorf("第 %d 期累计 = %s，期望 %s", i+1, it.Cumulative, cum)
				}
				if cum > a.DepreciableBase() {
					t.Fatalf("第 %d 期就提超了：累计 %s > 应提 %s", i+1, cum, a.DepreciableBase())
				}
				if !it.Amount.IsPositive() && c.orig > 0 {
					t.Errorf("第 %d 期金额为 %s", i+1, it.Amount)
				}
			}
		})
	}
}

// ★ 逐期计提（AmountFor）走出来的累计，必须和折旧表一模一样。
//
// 界面是逐月调的，折旧表是整体算的 —— 两条路算出来不一样，
// 用户就会看到「预览说最后一期 333.34，实际提了 333.33」。
func TestAmountForMatchesSchedule(t *testing.T) {
	a := asset.FixedAsset{
		Name: "除不尽资产", OrigValue: yuan(100000), SalvagePPM: 0,
		UsefulMonths: 3, StartDate: date(t, "2025-01-31"),
		ExpenseAccount: "560205", Status: asset.StatusInUse,
	}
	sched := a.Schedule()
	var cum money.Money
	k := a.FirstPeriod()
	for i, want := range sched {
		got, why := a.AmountFor(k, cum)
		if got != want.Amount {
			t.Fatalf("第 %d 期（%s）AmountFor = %s，折旧表 = %s（%s）",
				i+1, k, got, want.Amount, why)
		}
		cum = cum.Add(got)
		k = k.Next()
	}
	// 过了使用年限必须停，且说清楚原因
	if amt, why := a.AmountFor(k, cum); !amt.IsZero() {
		t.Errorf("过了使用年限还在提：%s（%s）", amt, why)
	} else if !strings.Contains(why, "年限") {
		t.Errorf("要说明为什么是 0，实际 %q", why)
	}
	// 年限内但已经提足（数据有偏差时才会出现）也要停
	if amt, why := a.AmountFor(a.FirstPeriod(), a.DepreciableBase()); !amt.IsZero() {
		t.Errorf("已提足还在提：%s（%s）", amt, why)
	} else if !strings.Contains(why, "提足") {
		t.Errorf("要说明「已提足」，实际 %q", why)
	}
}

// ★ 处置：当月照提，次月起停
func TestDisposalStopsFromNextMonth(t *testing.T) {
	a := laptop(t)
	a.Status = asset.StatusDisposed
	a.DisposedDate = date(t, "2026-02-20")

	// 处置当月照提
	amt, why := a.AmountFor(period.NewKey(2026, 2), 0)
	if amt.IsZero() {
		t.Fatalf("★ 处置当月仍应计提，却算出 0（%s）", why)
	}
	// 次月停
	amt, why = a.AmountFor(period.NewKey(2026, 3), 0)
	if !amt.IsZero() {
		t.Errorf("★ 处置次月不该再提，却算出 %s", amt)
	}
	if !strings.Contains(why, "处置") {
		t.Errorf("要说明是因为处置了，实际 %q", why)
	}
	// 折旧表也应当在处置当月截断
	sched := a.Schedule()
	if len(sched) == 0 {
		t.Fatal("折旧表为空")
	}
	lastK := sched[len(sched)-1].Period
	if lastK.Year != 2026 || lastK.Month != 2 {
		t.Errorf("折旧表应止于处置当月 2026-02，实际 %s", lastK)
	}
}

// 处置早于首次计提（当月买当月卖）—— 一期都不提
func TestDisposedBeforeFirstPeriod(t *testing.T) {
	a := laptop(t)
	a.Status = asset.StatusDisposed
	a.DisposedDate = date(t, "2025-03-20") // 与投入使用同月
	if sched := a.Schedule(); len(sched) != 0 {
		t.Errorf("投入使用当月就处置了，不该有任何折旧：%+v", sched)
	}
}

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

func TestFixedAssetValidate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*asset.FixedAsset)
		want string
	}{
		{"没名字", func(a *asset.FixedAsset) { a.Name = "  " }, "名称"},
		{"原值为零", func(a *asset.FixedAsset) { a.OrigValue = 0 }, "原值"},
		{"原值为负", func(a *asset.FixedAsset) { a.OrigValue = -1 }, "原值"},
		{"年限为零", func(a *asset.FixedAsset) { a.UsefulMonths = 0 }, "月数"},
		{"残值率 100%", func(a *asset.FixedAsset) { a.SalvagePPM = 1_000_000 }, "残值率"},
		{"残值率负数", func(a *asset.FixedAsset) { a.SalvagePPM = -1 }, "残值率"},
		{"没填费用科目", func(a *asset.FixedAsset) { a.ExpenseAccount = "" }, "费用科目"},
		{"日期非法", func(a *asset.FixedAsset) { a.StartDate = calendar.Date{} }, "日期"},
		{"已处置却没填处置日期", func(a *asset.FixedAsset) {
			a.Status = asset.StatusDisposed
		}, "处置日期"},
		{"类别乱填", func(a *asset.FixedAsset) { a.Category = "spaceship" }, "类别"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := laptop(t)
			c.mut(&a)
			err := a.Validate()
			if err == nil {
				t.Fatal("应当被拒绝")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误信息应提到 %q，实际 %v", c.want, err)
			}
		})
	}
	// 正常卡片要过
	if err := laptop(t).Validate(); err != nil {
		t.Errorf("正常卡片不该被拒: %v", err)
	}
}

// 税法最低年限：类别 → 年限
func TestCategoryMinYears(t *testing.T) {
	want := map[asset.Category]int{
		asset.CatBuilding:   20,
		asset.CatMachine:    10,
		asset.CatFurniture:  5,
		asset.CatVehicle:    4,
		asset.CatElectronic: 3,
	}
	for c, y := range want {
		if got := c.MinYears(); got != y {
			t.Errorf("%s 最低年限 = %d，期望 %d", c.Label(), got, y)
		}
		if got := c.MinMonths(); got != y*12 {
			t.Errorf("%s 最低月数 = %d，期望 %d", c.Label(), got, y*12)
		}
		if c.Label() == string(c) {
			t.Errorf("%s 没有中文名", c)
		}
	}
}

// ---------------------------------------------------------------------------
// 费用摊销
// ---------------------------------------------------------------------------

// 一年期房租 120,000 元，2025-01-01 起受益。
func rent(t *testing.T) asset.Amortization {
	t.Helper()
	return asset.Amortization{
		Name: "一年期房租", Total: yuan(120000), Months: 12,
		StartDate:      date(t, "2025-01-01"),
		ExpenseAccount: "560210", AssetAccount: "1801",
		Status: asset.AmortActive,
	}
}

// ★ 摊销从**当月**开始 —— 与固定资产的次月起提刻意不同
func TestAmortizationStartsSameMonth(t *testing.T) {
	m := rent(t)
	k := m.FirstPeriod()
	if k.Year != 2025 || k.Month != 1 {
		t.Fatalf("★ 2025-01 开始的受益期应从 2025-01 起摊，实际 %s", k)
	}
	amt, why := m.AmountFor(k, 0)
	if amt != yuan(10000) {
		t.Fatalf("首期摊销 = %s，期望 10,000.00（%s）", amt, why)
	}
	// 开始之前不摊
	if a, _ := m.AmountFor(period.NewKey(2024, 12), 0); !a.IsZero() {
		t.Errorf("受益期开始之前不该摊：%s", a)
	}
}

func TestAmortizationScheduleSumsExactly(t *testing.T) {
	cases := []struct {
		name   string
		total  int64
		months int
	}{
		{"除得尽", 120000, 12},
		{"除不尽·3 期", 10000, 3},
		{"除不尽·7 期", 100000, 7},
		{"1 期", 3333, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := asset.Amortization{
				Name: "测试", Total: yuan(c.total), Months: c.months,
				StartDate: date(t, "2025-06-15"), ExpenseAccount: "560210",
				Status: asset.AmortActive,
			}
			sched := m.Schedule()
			if len(sched) != c.months {
				t.Fatalf("摊销表期数 = %d，期望 %d", len(sched), c.months)
			}
			var sum money.Money
			for _, it := range sched {
				sum = sum.Add(it.Amount)
			}
			if sum != m.Total {
				t.Errorf("★ 各期之和 %s ≠ 待摊总额 %s，差 %s",
					sum, m.Total, m.Total.Sub(sum))
			}
			// 逐期调也要一致
			var cum money.Money
			k := m.FirstPeriod()
			for i, want := range sched {
				got, why := m.AmountFor(k, cum)
				if got != want.Amount {
					t.Fatalf("第 %d 期 AmountFor = %s，摊销表 = %s（%s）",
						i+1, got, want.Amount, why)
				}
				cum = cum.Add(got)
				k = k.Next()
			}
		})
	}
}

// 作废的项目不再摊销
func TestAmortizationVoided(t *testing.T) {
	m := rent(t)
	m.Status = asset.AmortVoided
	if amt, why := m.AmountFor(m.FirstPeriod(), 0); !amt.IsZero() {
		t.Errorf("作废项目还在摊：%s", amt)
	} else if !strings.Contains(why, "作废") {
		t.Errorf("要说明原因，实际 %q", why)
	}
	if len(m.Schedule()) != 0 {
		t.Error("作废项目不该有摊销表")
	}
}

func TestAmortizationValidate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*asset.Amortization)
		want string
	}{
		{"没名字", func(m *asset.Amortization) { m.Name = "" }, "名称"},
		{"总额为零", func(m *asset.Amortization) { m.Total = 0 }, "总额"},
		{"月数为零", func(m *asset.Amortization) { m.Months = 0 }, "月数"},
		{"日期非法", func(m *asset.Amortization) { m.StartDate = calendar.Date{} }, "日期"},
		{"没费用科目", func(m *asset.Amortization) { m.ExpenseAccount = "" }, "费用科目"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := rent(t)
			c.mut(&m)
			err := m.Validate()
			if err == nil {
				t.Fatal("应当被拒绝")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误信息应提到 %q，实际 %v", c.want, err)
			}
		})
	}
	if err := rent(t).Validate(); err != nil {
		t.Errorf("正常项目不该被拒: %v", err)
	}
}

// ★ 残值率用百万分比算，不能出现 float 抖动
func TestSalvageUsesExactIntegerMath(t *testing.T) {
	// 12,345.67 × 5% = 617.2835 → 617.28
	a := asset.FixedAsset{OrigValue: money.MustParse("12345.67"), SalvagePPM: 50_000}
	if got := a.Salvage(); got != money.MustParse("617.28") {
		t.Errorf("净残值 = %s，期望 617.28", got)
	}
	if got := a.DepreciableBase(); got != money.MustParse("11728.39") {
		t.Errorf("应提总额 = %s，期望 11728.39", got)
	}
}
