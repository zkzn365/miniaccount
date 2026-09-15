package workpaper_test

import (
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/workpaper"
)

func yuan(n int64) money.Money { return money.Money(n) * money.Yuan }

func k2026() period.Key { return period.NewKey(2026, 9) }

// 一份常用的重要性水平：资产总额 1,000 万 × 0.5%。
func assetsMat(t *testing.T) workpaper.Materiality {
	t.Helper()
	m := workpaper.NewMateriality(k2026(), workpaper.BenchmarkAssets, yuan(10_000_000))
	if err := m.Validate(); err != nil {
		t.Fatalf("默认的重要性水平不该校验失败：%v", err)
	}
	return m
}

// ---------------------------------------------------------------------------
// ★ 重要性水平的三个数：基准 × 比例，以及两个派生门槛
// ---------------------------------------------------------------------------

func TestMaterialityComputesThreeLevels(t *testing.T) {
	m := assetsMat(t)
	// 资产 10,000,000.00 × 0.5% = 50,000.00
	if got := m.Overall(); got != yuan(50_000) {
		t.Errorf("整体重要性 = %s，期望 50,000.00", got)
	}
	// 实际执行 = 50,000 × 60% = 30,000
	if got := m.Performance(); got != yuan(30_000) {
		t.Errorf("实际执行重要性 = %s，期望 30,000.00", got)
	}
	// 明显微小 = 50,000 × 5% = 2,500
	if got := m.Trivial(); got != yuan(2_500) {
		t.Errorf("明显微小错报临界值 = %s，期望 2,500.00", got)
	}
	// 三个数必须递减：整体 > 实际执行 > 明显微小
	if !(m.Overall() > m.Performance() && m.Performance() > m.Trivial()) {
		t.Errorf("三个门槛必须递减：%s / %s / %s", m.Overall(), m.Performance(), m.Trivial())
	}
}

// 基准不同，常用比例不同 —— 利润总额用 5%、资产用 0.5%
func TestBenchmarkDefaultRates(t *testing.T) {
	want := map[workpaper.Benchmark]int64{
		workpaper.BenchmarkAssets:  5_000,
		workpaper.BenchmarkRevenue: 10_000,
		workpaper.BenchmarkProfit:  50_000,
		workpaper.BenchmarkExpense: 10_000,
	}
	for b, ppm := range want {
		if got := b.DefaultRatePPM(); got != ppm {
			t.Errorf("%s 常用比例 = %s，期望 %s",
				b.Label(), workpaper.Percent(got), workpaper.Percent(ppm))
		}
		if b.Label() == string(b) {
			t.Errorf("%s 没有中文名", b)
		}
		if !b.Valid() {
			t.Errorf("%s 应当是可识别的基准", b)
		}
	}
	if workpaper.Benchmark("spaceship").Valid() {
		t.Error("乱填的基准不该被认作合法")
	}
}

// ★ 底稿上必须能答出「这个数怎么来的」
func TestMaterialityExplainsItsArithmetic(t *testing.T) {
	m := assetsMat(t)
	lines := m.Explain()
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"资产总额", "10,000,000.00", "0.5%", "50,000.00",
		"30,000.00", "2,500.00", "60%", "5%",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("算式说明里缺少 %q：\n%s", want, joined)
		}
	}
	// 每一行都应当是一个等式（含 =）
	for i, l := range lines {
		if i == 0 {
			continue // 第一行是「基准：xxx」
		}
		if !strings.Contains(l, "=") {
			t.Errorf("第 %d 行不是算式：%q", i+1, l)
		}
	}
}

func TestMaterialityValidate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*workpaper.Materiality)
		want string
	}{
		{"基准乱填", func(m *workpaper.Materiality) { m.Benchmark = "spaceship" }, "基准类型"},
		{"基准金额为零", func(m *workpaper.Materiality) { m.BenchmarkAmount = 0 }, "基准金额"},
		{"基准金额为负", func(m *workpaper.Materiality) { m.BenchmarkAmount = -1 }, "基准金额"},
		{"比例为零", func(m *workpaper.Materiality) { m.RatePPM = 0 }, "比例"},
		{"比例 100%", func(m *workpaper.Materiality) { m.RatePPM = 1_000_000 }, "比例"},
		{"实际执行比例为零", func(m *workpaper.Materiality) { m.PerformancePPM = 0 }, "比例"},
		// 基准小到整体重要性被舍入成 0：门槛为零等于任何错报都要处理
		{"基准小到重要性舍入为 0", func(m *workpaper.Materiality) {
			m.BenchmarkAmount = money.Money(1) // 1 分
			m.RatePPM = 5_000                  // 0.5% → 0.005 分 → 舍入为 0
		}, "门槛为零"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := assetsMat(t)
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
}

// ---------------------------------------------------------------------------
// ★ 调整分录：不平就是记错了
// ---------------------------------------------------------------------------

func adj(t *testing.T, kind workpaper.AdjustKind, lines ...workpaper.AdjustLine) workpaper.Adjustment {
	t.Helper()
	for i := range lines {
		lines[i].LineNo = i + 1
	}
	return workpaper.Adjustment{
		Period: k2026(), Code: "ADJ-2026-001", Kind: kind,
		Summary: "补提折旧", Reason: "固定资产台账与账面不符", Evidence: "固定资产台账",
		Lines: lines,
	}
}

func TestAdjustmentValidate(t *testing.T) {
	ok := adj(t, workpaper.KindAdjust,
		workpaper.AdjustLine{AccountCode: "560205", Debit: yuan(1000)},
		workpaper.AdjustLine{AccountCode: "1602", Credit: yuan(1000)})
	if err := ok.Validate(); err != nil {
		t.Fatalf("正常的调整不该被拒：%v", err)
	}
	if !ok.Balanced() {
		t.Error("借贷相等应报告为平衡")
	}
	if ok.Amount() != yuan(1000) {
		t.Errorf("金额 = %s，期望 1,000.00", ok.Amount())
	}

	bad := []struct {
		name string
		mut  func(*workpaper.Adjustment)
		want string
	}{
		{"期间非法", func(a *workpaper.Adjustment) { a.Period = period.Key{} }, "期间"},
		{"种类乱填", func(a *workpaper.Adjustment) { a.Kind = "spaceship" }, "种类"},
		{"没有摘要", func(a *workpaper.Adjustment) { a.Summary = "  " }, "摘要"},
		{"没有依据", func(a *workpaper.Adjustment) { a.Reason = "" }, "调整依据"},
		{"只有一条", func(a *workpaper.Adjustment) { a.Lines = a.Lines[:1] }, "两条"},
		{"没有科目", func(a *workpaper.Adjustment) { a.Lines[0].AccountCode = "" }, "没有科目"},
		{"金额为负", func(a *workpaper.Adjustment) { a.Lines[0].Debit = -1 }, "金额为负"},
		{"两个方向都填", func(a *workpaper.Adjustment) {
			a.Lines[0].Debit, a.Lines[0].Credit = yuan(1), yuan(1)
		}, "恰好一个方向"},
		{"借贷不平", func(a *workpaper.Adjustment) { a.Lines[1].Credit = yuan(999) }, "不平"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			a := adj(t, workpaper.KindAdjust,
				workpaper.AdjustLine{AccountCode: "560205", Debit: yuan(1000)},
				workpaper.AdjustLine{AccountCode: "1602", Credit: yuan(1000)})
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
}

// 不平的差额要写出来 —— 只说「不平」，用户还得自己减一遍
func TestUnbalancedMessageShowsDifference(t *testing.T) {
	a := adj(t, workpaper.KindAdjust,
		workpaper.AdjustLine{AccountCode: "560205", Debit: yuan(1000)},
		workpaper.AdjustLine{AccountCode: "1602", Credit: yuan(900)})
	err := a.Validate()
	if err == nil {
		t.Fatal("应当报不平")
	}
	if !strings.Contains(err.Error(), "100.00") {
		t.Errorf("要写出差额 100.00，实际：%v", err)
	}
}

// ---------------------------------------------------------------------------
// ★ 审定表：审定数 = 账面 + 未入账调整
// ---------------------------------------------------------------------------

func TestWorksheetRowAuditedIsBookPlusUnpostedAdjust(t *testing.T) {
	r := workpaper.WorksheetRow{
		AccountCode: "1122", AccountName: "应收账款",
		BookBalance:  yuan(100_000),
		AdjustDebit:  yuan(5_000),
		AdjustCredit: yuan(1_000),
	}
	// 100,000 + 5,000 − 1,000 = 104,000
	if got := r.Audited(); got != yuan(104_000) {
		t.Errorf("审定数 = %s，期望 104,000.00", got)
	}
	if !r.Adjusted() {
		t.Error("有调整时应报告 Adjusted")
	}

	// 贷方余额的科目：账面为负
	r2 := workpaper.WorksheetRow{
		AccountCode: "2202", BookBalance: -yuan(50_000), AdjustCredit: yuan(2_000),
	}
	if got := r2.Audited(); got != -yuan(52_000) {
		t.Errorf("贷方科目的审定数 = %s，期望 -52,000.00（贷方余额增加）", got)
	}

	// 没调整时审定数 = 账面数
	r3 := workpaper.WorksheetRow{BookBalance: yuan(7)}
	if r3.Audited() != yuan(7) || r3.Adjusted() {
		t.Error("没有调整时审定数应当等于账面数")
	}
}

// ★ 这条是整个底稿最容易算错的地方：已入账的调整不能再加一次
func TestMisstatementsExcludePosted(t *testing.T) {
	posted := adj(t, workpaper.KindAdjust,
		workpaper.AdjustLine{AccountCode: "560205", Debit: yuan(5000)},
		workpaper.AdjustLine{AccountCode: "1602", Credit: yuan(5000)})
	posted.Code = "ADJ-001"
	posted.Posted = true // 凭证已经过账，账改了

	// 金额要**高于明显微小错报临界值**（2,500）：这条用例验的是
	// 「已过账的不算」，别让「低于临界值不累积」这条新规则混进来
	open := adj(t, workpaper.KindAdjust,
		workpaper.AdjustLine{AccountCode: "560207", Debit: yuan(4000)},
		workpaper.AdjustLine{AccountCode: "1002", Credit: yuan(4000)})
	open.Code = "ADJ-002"

	m := assetsMat(t)
	s := workpaper.Misstatements([]workpaper.Adjustment{posted, open}, &m)

	if len(s.Items) != 1 {
		t.Fatalf("未更正错报应当只算未入账的那 1 笔，实际 %d 笔：%+v", len(s.Items), s.Items)
	}
	if s.Items[0].Code != "ADJ-002" {
		t.Errorf("留下的应当是未入账那笔，实际 %s", s.Items[0].Code)
	}
	if s.Total != yuan(4000) {
		t.Errorf("★ 合计 = %s，期望 4,000.00 —— "+
			"把已入账的也算进来，等于把已经改过的错又报了一次", s.Total)
	}
}

// ★ 低于明显微小错报临界值的错报**不必累积**（CAS 1251）。
//
// 原来一律累加：13 笔各 4,900 元（每笔都低于 5,000 的临界值）
// 会凑出 63,700，把「没超过实际执行重要性」说成「超过了」。
// 但**照样列出来**，只是不计入合计 —— 该看见的不能藏。
func TestMisstatementsSkipTrivialFromTotal(t *testing.T) {
	m := assetsMat(t) // 明显微小临界值 2,500
	small := adj(t, workpaper.KindAdjust,
		workpaper.AdjustLine{AccountCode: "560207", Debit: yuan(2000)},
		workpaper.AdjustLine{AccountCode: "1002", Credit: yuan(2000)})
	small.Code = "ADJ-001"
	big := adj(t, workpaper.KindAdjust,
		workpaper.AdjustLine{AccountCode: "560207", Debit: yuan(4000)},
		workpaper.AdjustLine{AccountCode: "1002", Credit: yuan(4000)})
	big.Code = "ADJ-002"

	s := workpaper.Misstatements([]workpaper.Adjustment{small, big}, &m)
	if len(s.Items) != 2 {
		t.Fatalf("两笔都要列出来（低于临界值的也不能藏），实际 %d 笔", len(s.Items))
	}
	if s.TrivialCount != 1 {
		t.Errorf("低于临界值的笔数 = %d，期望 1", s.TrivialCount)
	}
	if !s.Items[0].Trivial {
		t.Error("2,000 低于临界值 2,500，应当标 Trivial")
	}
	if s.Total != yuan(4000) {
		t.Errorf("★ 合计 = %s，期望只含 4,000.00（低于临界值的不累积）", s.Total)
	}
	// 全部低于临界值时，合计为 0，但要说明口径
	only := workpaper.Misstatements([]workpaper.Adjustment{small}, &m)
	if !only.Total.IsZero() {
		t.Errorf("只有低于临界值的错报时合计应为 0，实际 %s", only.Total)
	}
	if !strings.Contains(only.Concludes(), "不必累积") {
		t.Errorf("结论要说清为什么不累积，实际：%s", only.Concludes())
	}
	// 没有重要性水平时不过滤（宁可让人看到全部）
	noMat := workpaper.Misstatements([]workpaper.Adjustment{small}, nil)
	if noMat.Total != yuan(2000) {
		t.Errorf("没有门槛时不该过滤，合计应为 2,000.00，实际 %s", noMat.Total)
	}
}

// ★ 生成过凭证、但凭证还是草稿的，仍然算未更正错报。
//
// 过账只在账期结算 —— 草稿没进账，账上那个错就还在。
// 这条正是 Booked 与 Posted 要分开的理由：若把「已生成凭证」
// 当成「已入账」，未更正错报会在结算前凭空消失，审定数也会少加它。
func TestMisstatementsStillCountBookedButUnposted(t *testing.T) {
	draft := adj(t, workpaper.KindAdjust,
		workpaper.AdjustLine{AccountCode: "560205", Debit: yuan(3000)},
		workpaper.AdjustLine{AccountCode: "1602", Credit: yuan(3000)})
	draft.Code = "ADJ-001"
	draft.Booked = true // 凭证生成了，但还躺在草稿里

	m := assetsMat(t)
	s := workpaper.Misstatements([]workpaper.Adjustment{draft}, &m)

	if len(s.Items) != 1 {
		t.Fatalf("草稿凭证没进账，这笔仍是未更正错报，实际明细 %d 笔", len(s.Items))
	}
	if s.Total != yuan(3000) {
		t.Errorf("★ 合计 = %s，期望 3,000.00 —— "+
			"「生成凭证」不等于「入账」，草稿不算已更正", s.Total)
	}
}

// ★ 重分类不计入错报合计：它不改变利润，计进去会让合计虚高
func TestMisstatementsExcludeReclassFromTotal(t *testing.T) {
	m := assetsMat(t)
	reclass := adj(t, workpaper.KindReclass,
		workpaper.AdjustLine{AccountCode: "1122", Debit: yuan(8000)},
		workpaper.AdjustLine{AccountCode: "1123", Credit: yuan(8000)})
	adjust := adj(t, workpaper.KindAdjust,
		workpaper.AdjustLine{AccountCode: "560207", Debit: yuan(4000)},
		workpaper.AdjustLine{AccountCode: "1002", Credit: yuan(4000)})

	s := workpaper.Misstatements([]workpaper.Adjustment{reclass, adjust}, &m)
	if s.Total != yuan(4000) {
		t.Errorf("合计应当只含调整的 4,000.00，实际 %s", s.Total)
	}
	if s.ReclassCount != 1 {
		t.Errorf("重分类笔数 = %d，期望 1 —— 不计入合计但要报出来", s.ReclassCount)
	}
	if len(s.Items) != 2 {
		t.Errorf("明细应当两笔都列出来（重分类也要看得到），实际 %d 笔", len(s.Items))
	}
}

// ---------------------------------------------------------------------------
// ★ 结论：与三个门槛比，说清超没超
// ---------------------------------------------------------------------------

func TestMisstatementConcludesAgainstThresholds(t *testing.T) {
	m := assetsMat(t) // 整体 50,000 / 实际执行 30,000 / 明显微小 2,500

	mk := func(amount int64) []workpaper.Adjustment {
		a := adj(t, workpaper.KindAdjust,
			workpaper.AdjustLine{AccountCode: "560207", Debit: yuan(amount)},
			workpaper.AdjustLine{AccountCode: "1002", Credit: yuan(amount)})
		return []workpaper.Adjustment{a}
	}

	cases := []struct {
		amount int64
		want   string
	}{
		{5000, "尚未构成重大错报"}, // 高于明显微小临界值 2,500，低于实际执行 30,000
		{35_000, "超过实际执行重要性"},
		{60_000, "已达到整体重要性"},
	}
	for _, c := range cases {
		s := workpaper.Misstatements(mk(c.amount), &m)
		got := s.Concludes()
		if !strings.Contains(got, c.want) {
			t.Errorf("错报 %d 的结论应当提到 %q，实际：%s", c.amount, c.want, got)
		}
	}

	// 没有重要性水平时不能假装能判断
	noMat := workpaper.Misstatements(mk(1), nil)
	if !strings.Contains(noMat.Concludes(), "尚未确定重要性水平") {
		t.Errorf("没配门槛时要直说判不了，实际：%s", noMat.Concludes())
	}
	if noMat.HasMateriality {
		t.Error("没配门槛时 HasMateriality 应为 false")
	}

	// 一笔都没有
	empty := workpaper.Misstatements(nil, &m)
	if !strings.Contains(empty.Concludes(), "没有未更正错报") {
		t.Errorf("空汇总的结论不对：%s", empty.Concludes())
	}
}

// 只有重分类时，合计为零，但结论不能说「没有错报」
func TestConcludesWhenOnlyReclass(t *testing.T) {
	m := assetsMat(t)
	reclass := adj(t, workpaper.KindReclass,
		workpaper.AdjustLine{AccountCode: "1122", Debit: yuan(8000)},
		workpaper.AdjustLine{AccountCode: "1123", Credit: yuan(8000)})
	s := workpaper.Misstatements([]workpaper.Adjustment{reclass}, &m)
	got := s.Concludes()
	if !strings.Contains(got, "重分类") || !strings.Contains(got, "不影响损益") {
		t.Errorf("只有重分类时要说清楚「不影响损益」，实际：%s", got)
	}
}

// 没配重要性水平时，明细一律全给（不替用户过滤）
func TestExceedsTrivialWithoutMateriality(t *testing.T) {
	w := workpaper.Worksheet{Period: k2026()}
	if !w.ExceedsTrivial(yuan(1)) {
		t.Error("没有门槛时不该替用户过滤掉小额 —— 一律显示")
	}
	m := assetsMat(t)
	w.Materiality = &m // 明显微小 2,500
	if w.ExceedsTrivial(yuan(1000)) {
		t.Error("低于明显微小错报临界值的，应当报告为「不必累积」")
	}
	if !w.ExceedsTrivial(yuan(2500)) {
		t.Error("达到临界值的应当计入")
	}
	if !w.ExceedsTrivial(-yuan(3000)) {
		t.Error("判断要看绝对值 —— 贷方的大额错报同样重要")
	}
}
