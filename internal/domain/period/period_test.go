package period

import (
	"errors"
	"testing"
	"time"

	"miniaccount/internal/domain/calendar"
)

func newCal(t *testing.T, startYear, startMonth, throughYear int, current Key) *Calendar {
	t.Helper()
	c, err := NewCalendar(startYear, startMonth, throughYear, current)
	if err != nil {
		t.Fatalf("NewCalendar 失败: %v", err)
	}
	return c
}

// 典型场景：2025 年 1 月启用，已生成到 2026 年底，当前是 2025 年 9 月。
func sampleCal(t *testing.T) *Calendar {
	t.Helper()
	return newCal(t, 2025, 1, 2026, Key{2025, 9})
}

// ---------------------------------------------------------------------------
// Key
// ---------------------------------------------------------------------------

func TestKeyNextPrev(t *testing.T) {
	if got := (Key{2025, 12}).Next(); got != (Key{2026, 1}) {
		t.Errorf("2025-12 的下期 = %v，期望 2026-01", got)
	}
	if got := (Key{2025, 1}).Prev(); got != (Key{2024, 12}) {
		t.Errorf("2025-01 的上期 = %v，期望 2024-12", got)
	}
	if got := (Key{2025, 9}).String(); got != "2025-09" {
		t.Errorf("String = %q", got)
	}
	if (Key{2025, 13}).Valid() || (Key{2025, 0}).Valid() || (Key{0, 1}).Valid() {
		t.Error("非法年月应判定为 invalid")
	}
}

// ---------------------------------------------------------------------------
// Calendar 构建
// ---------------------------------------------------------------------------

func TestNewCalendar(t *testing.T) {
	c := sampleCal(t)
	// 2025-01 .. 2026-12 = 24 个期间
	if c.Len() != 24 {
		t.Fatalf("期间数 = %d，期望 24", c.Len())
	}
	if c.BookStart() != (Key{2025, 1}) {
		t.Errorf("启用期间 = %v", c.BookStart())
	}
	// 到 2025-09 为止是 open（9 个），其后是 future（15 个）
	if got := len(c.OpenPeriods()); got != 9 {
		t.Errorf("已启用期间数 = %d，期望 9", got)
	}
	// 期间连续、无空洞
	keys := c.Keys()
	for i := 1; i < len(keys); i++ {
		if keys[i-1].Next() != keys[i] {
			t.Fatalf("期间不连续: %v 之后是 %v", keys[i-1], keys[i])
		}
	}
}

func TestNewCalendarErrors(t *testing.T) {
	if _, err := NewCalendar(2025, 13, 2026, Key{}); err == nil {
		t.Error("非法启用月份应报错")
	}
	if _, err := NewCalendar(2026, 1, 2025, Key{}); err == nil {
		t.Error("结束年度早于启用年度应报错")
	}
}

func TestPeriodRange(t *testing.T) {
	c := sampleCal(t)
	p := c.periods[Key{2025, 9}]
	if p.Range.From.String() != "2025-09-01" || p.Range.To.String() != "2025-09-30" {
		t.Errorf("2025-09 区间 = %v", p.Range)
	}
	// 二月闰年
	c2 := newCal(t, 2024, 1, 2024, Key{2024, 12})
	if p := c2.periods[Key{2024, 2}]; p.Range.To.String() != "2024-02-29" {
		t.Errorf("2024-02 区间末日 = %v，期望 2024-02-29", p.Range.To)
	}
	c3 := newCal(t, 2025, 1, 2025, Key{2025, 12})
	if p := c3.periods[Key{2025, 2}]; p.Range.To.String() != "2025-02-28" {
		t.Errorf("2025-02 区间末日 = %v，期望 2025-02-28", p.Range.To)
	}
}

func TestPeriodOf(t *testing.T) {
	c := sampleCal(t)
	p, ok := c.PeriodOf(calendar.MustParse("2025-09-15"))
	if !ok || p.Key != (Key{2025, 9}) {
		t.Fatalf("PeriodOf 得到 %v %v", p, ok)
	}
	// 边界：1 日与末日都应落在本期
	for _, s := range []string{"2025-09-01", "2025-09-30"} {
		p, ok := c.PeriodOf(calendar.MustParse(s))
		if !ok || p.Key != (Key{2025, 9}) {
			t.Errorf("%s 应属于 2025-09，得到 %v", s, p)
		}
	}
	// 区间外
	if p, ok := c.PeriodOf(calendar.MustParse("2024-12-31")); ok {
		t.Errorf("2024-12-31 不在期间表内，却得到 %v", p)
	}
}

// ---------------------------------------------------------------------------
// 过账校验
// ---------------------------------------------------------------------------

func TestCheckPostable(t *testing.T) {
	c := sampleCal(t)

	// open 期间可过账
	if _, err := c.CheckPostable(calendar.MustParse("2025-09-11")); err != nil {
		t.Errorf("已启用期间应可过账，得到 %v", err)
	}
	// future 期间不可过账
	if _, err := c.CheckPostable(calendar.MustParse("2025-10-01")); !errors.Is(err, ErrNotOpen) {
		t.Errorf("未启用期间应报 ErrNotOpen，得到 %v", err)
	}
	// 早于账套启用期间
	if _, err := c.CheckPostable(calendar.MustParse("2024-12-31")); !errors.Is(err, ErrBeforeBookStart) {
		t.Errorf("早于启用期间应报 ErrBeforeBookStart，得到 %v", err)
	}
	// 期间表之外（晚于最后生成的期间）
	if _, err := c.CheckPostable(calendar.MustParse("2027-01-15")); !errors.Is(err, ErrPeriodNotFound) {
		t.Errorf("期间表外应报 ErrPeriodNotFound，得到 %v", err)
	}
	// 空日期
	if _, err := c.CheckPostable(calendar.Date{}); !errors.Is(err, ErrPeriodNotFound) {
		t.Errorf("空日期应报错，得到 %v", err)
	}
}

func TestCheckPostableOnClosedPeriod(t *testing.T) {
	c := sampleCal(t)
	if err := c.Close(Key{2025, 1}, "admin", time.Now()); err != nil {
		t.Fatalf("结账失败: %v", err)
	}
	_, err := c.CheckPostable(calendar.MustParse("2025-01-15"))
	if !errors.Is(err, ErrNotOpen) {
		t.Fatalf("已结账期间应拒绝过账，得到 %v", err)
	}
	// 错误信息应提示先反结账
	if err.Error() == "" {
		t.Error("错误信息不应为空")
	}
}

// ★ 规则 2：一张凭证不能跨期
func TestCheckSamePeriod(t *testing.T) {
	c := sampleCal(t)
	same := []calendar.Date{
		calendar.MustParse("2025-09-01"),
		calendar.MustParse("2025-09-15"),
		calendar.MustParse("2025-09-30"),
	}
	p, err := c.CheckSamePeriod(same...)
	if err != nil {
		t.Fatalf("同期日期应通过，得到 %v", err)
	}
	if p.Key != (Key{2025, 9}) {
		t.Errorf("期间 = %v", p.Key)
	}

	// 跨期：取两个都处于 open 的相邻期间，确保报的是跨期而不是「未启用」
	cross := []calendar.Date{
		calendar.MustParse("2025-08-31"),
		calendar.MustParse("2025-09-01"),
	}
	if _, err := c.CheckSamePeriod(cross...); !errors.Is(err, ErrCrossPeriod) {
		t.Errorf("跨期应报 ErrCrossPeriod，得到 %v", err)
	}

	if _, err := c.CheckSamePeriod(); !errors.Is(err, ErrPeriodNotFound) {
		t.Errorf("空参数应报错，得到 %v", err)
	}
}

func TestCheckWritable(t *testing.T) {
	c := sampleCal(t)
	if _, err := c.CheckWritable(Key{2025, 9}); err != nil {
		t.Errorf("open 期间应可写，得到 %v", err)
	}
	if err := c.Close(Key{2025, 1}, "admin", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CheckWritable(Key{2025, 1}); !errors.Is(err, ErrAlreadyClosed) {
		t.Errorf("已结账期间应报 ErrAlreadyClosed，得到 %v", err)
	}
	if _, err := c.CheckWritable(Key{2030, 1}); !errors.Is(err, ErrPeriodNotFound) {
		t.Errorf("不存在的期间应报 ErrPeriodNotFound，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 结账（规则 4：顺序）
// ---------------------------------------------------------------------------

func TestCloseIsSequential(t *testing.T) {
	c := sampleCal(t)

	// 直接结 3 月应当失败 —— 1、2 月都还没结
	err := c.CanClose(Key{2025, 3})
	if !errors.Is(err, ErrPrevNotClosed) {
		t.Fatalf("跳月结账应报 ErrPrevNotClosed，得到 %v", err)
	}

	// 按顺序结 1、2 月
	if err := c.Close(Key{2025, 1}, "admin", time.Now()); err != nil {
		t.Fatalf("结 1 月失败: %v", err)
	}
	if err := c.Close(Key{2025, 2}, "admin", time.Now()); err != nil {
		t.Fatalf("结 2 月失败: %v", err)
	}
	// 现在可以结 3 月
	if err := c.Close(Key{2025, 3}, "admin", time.Now()); err != nil {
		t.Fatalf("结 3 月失败: %v", err)
	}
	if !c.periods[Key{2025, 3}].IsClosed() {
		t.Error("3 月应为已结账")
	}
}

func TestCloseBookStartPeriodHasNoPrev(t *testing.T) {
	c := sampleCal(t)
	// 启用期间（2025-01）没有上一期间，应可直接结账
	if err := c.CanClose(Key{2025, 1}); err != nil {
		t.Fatalf("启用期间应可直接结账，得到 %v", err)
	}
}

func TestCloseTwiceFails(t *testing.T) {
	c := sampleCal(t)
	if err := c.Close(Key{2025, 1}, "admin", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(Key{2025, 1}, "admin", time.Now()); !errors.Is(err, ErrAlreadyClosed) {
		t.Errorf("重复结账应报 ErrAlreadyClosed，得到 %v", err)
	}
}

// 结账后应自动启用下一期间，用户可立即记下月账
func TestCloseOpensNextPeriod(t *testing.T) {
	c := newCal(t, 2025, 1, 2025, Key{2025, 1}) // 只有 1 月是 open
	if !c.periods[Key{2025, 2}].IsFrozen() {
		t.Fatal("前置条件：2 月应为 future")
	}
	if err := c.Close(Key{2025, 1}, "admin", time.Now()); err != nil {
		t.Fatal(err)
	}
	if !c.periods[Key{2025, 2}].IsOpen() {
		t.Error("结账 1 月后，2 月应自动启用")
	}
}

// ---------------------------------------------------------------------------
// 反结账（规则 5：逆序）
// ---------------------------------------------------------------------------

func TestReopenIsReverseSequential(t *testing.T) {
	c := sampleCal(t)
	for _, k := range []Key{{2025, 1}, {2025, 2}, {2025, 3}} {
		if err := c.Close(k, "admin", time.Now()); err != nil {
			t.Fatalf("结账 %v 失败: %v", k, err)
		}
	}

	// 3 月未反结账时，不能反结 2 月
	err := c.CanReopen(Key{2025, 2})
	if !errors.Is(err, ErrNextNotClosed) {
		t.Fatalf("跳月反结账应报 ErrNextNotClosed，得到 %v", err)
	}

	// 逆序反结账可以
	for _, k := range []Key{{2025, 3}, {2025, 2}, {2025, 1}} {
		if err := c.Reopen(k); err != nil {
			t.Fatalf("反结账 %v 失败: %v", k, err)
		}
	}
	if !c.periods[Key{2025, 1}].IsOpen() {
		t.Error("1 月应回到已启用状态")
	}
}

func TestReopenNotClosedFails(t *testing.T) {
	c := sampleCal(t)
	if err := c.Reopen(Key{2025, 5}); !errors.Is(err, ErrNotClosed) {
		t.Errorf("未结账期间反结账应报 ErrNotClosed，得到 %v", err)
	}
}

// 反结账后，后续已启用的期间应退回 future，
// 避免出现「9 月和 10 月同时开放」导致乱序记账。
func TestReopenFreezesLaterPeriods(t *testing.T) {
	c := newCal(t, 2025, 1, 2025, Key{2025, 3}) // 1~3 月 open
	if err := c.Close(Key{2025, 1}, "admin", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.Reopen(Key{2025, 1}); err != nil {
		t.Fatal(err)
	}
	// 1 月回到 open 后，2、3 月应退回 future
	for _, k := range []Key{{2025, 2}, {2025, 3}} {
		if c.periods[k].Status != StatusFuture {
			t.Errorf("%v 应退回未启用，实际为 %s", k, c.periods[k].Status.Label())
		}
	}
	if got := len(c.OpenPeriods()); got != 1 {
		t.Errorf("开放期间数 = %d，期望 1", got)
	}
}

// ---------------------------------------------------------------------------
// Current / EnsureThrough
// ---------------------------------------------------------------------------

func TestCurrent(t *testing.T) {
	c := sampleCal(t)
	// 最早的 open 期间是 2025-01
	if got := c.Current(); got == nil || got.Key != (Key{2025, 1}) {
		t.Errorf("Current = %v，期望 2025-01", got)
	}
	// 全部结账后为 nil
	for _, k := range c.Keys() {
		_ = c.Close(k, "admin", time.Now())
	}
	if got := c.Current(); got != nil {
		t.Errorf("全部结账后 Current 应为 nil，得到 %v", got)
	}
}

func TestEnsureThrough(t *testing.T) {
	c := newCal(t, 2025, 1, 2025, Key{2025, 1})
	if c.Len() != 12 {
		t.Fatalf("初始期间数 = %d，期望 12", c.Len())
	}
	c.EnsureThrough(Key{2026, 6})
	if c.Len() != 18 {
		t.Fatalf("扩展后期间数 = %d，期望 18", c.Len())
	}
	if p, ok := c.Get(2026, 6); !ok || p.Status != StatusFuture {
		t.Errorf("2026-06 应存在且为未启用，得到 %v", p)
	}
	// 重复调用不应重复追加
	c.EnsureThrough(Key{2026, 3})
	if c.Len() != 18 {
		t.Errorf("重复扩展后期间数 = %d，期望仍为 18", c.Len())
	}
	// 非法年月应被忽略
	c.EnsureThrough(Key{2026, 13})
	if c.Len() != 18 {
		t.Errorf("非法年月不应改动期间表")
	}
}

func TestYearRange(t *testing.T) {
	c := sampleCal(t)
	r, err := c.YearRange(2025)
	if err != nil {
		t.Fatal(err)
	}
	if r.From.String() != "2025-01-01" || r.To.String() != "2025-12-31" {
		t.Errorf("年度区间 = %v", r)
	}
	if r.Days() != 365 {
		t.Errorf("2025 年天数 = %d，期望 365", r.Days())
	}
}

func TestOfYear(t *testing.T) {
	c := sampleCal(t)
	if got := len(c.OfYear(2025)); got != 12 {
		t.Errorf("2025 年期间数 = %d，期望 12", got)
	}
	if got := len(c.OfYear(2026)); got != 12 {
		t.Errorf("2026 年期间数 = %d，期望 12", got)
	}
	if got := len(c.OfYear(2030)); got != 0 {
		t.Errorf("2030 年期间数 = %d，期望 0", got)
	}
}

func TestSummary(t *testing.T) {
	c := sampleCal(t)
	if err := c.Close(Key{2025, 1}, "张三", time.Now()); err != nil {
		t.Fatal(err)
	}
	s := c.Summary()
	if len(s) != 24 {
		t.Fatalf("摘要条数 = %d，期望 24", len(s))
	}
	if s[0].Status != StatusClosed || s[0].ClosedBy != "张三" || s[0].ClosedAt == nil {
		t.Errorf("第一条摘要 = %+v", s[0])
	}
	if s[1].Status != StatusOpen {
		t.Errorf("第二条应为已启用，实际 %s", s[1].Status.Label())
	}
}

func TestPeriodString(t *testing.T) {
	c := sampleCal(t)
	p := c.periods[Key{2025, 9}]
	want := "2025-09 (2025-09-01..2025-09-30, 已启用)"
	if got := p.String(); got != want {
		t.Errorf("String = %q，期望 %q", got, want)
	}
}
