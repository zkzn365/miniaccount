package calendar

import (
	"testing"
	"time"
)

func TestNewAndValid(t *testing.T) {
	ok := [][3]int{{2025, 9, 11}, {2024, 2, 29}, {2000, 2, 29}, {1, 1, 1}, {9999, 12, 31}}
	for _, c := range ok {
		if _, err := New(c[0], c[1], c[2]); err != nil {
			t.Errorf("New(%d,%d,%d) 应合法，得到 %v", c[0], c[1], c[2], err)
		}
	}
	bad := [][3]int{{2025, 2, 29}, {2023, 2, 29}, {1900, 2, 29}, {2025, 13, 1},
		{2025, 0, 1}, {2025, 4, 31}, {2025, 1, 0}, {0, 1, 1}}
	for _, c := range bad {
		if _, err := New(c[0], c[1], c[2]); err == nil {
			t.Errorf("New(%d,%d,%d) 应非法", c[0], c[1], c[2])
		}
	}
}

func TestParse(t *testing.T) {
	d, err := Parse("2025-09-11")
	if err != nil || d.Year != 2025 || d.Month != 9 || d.Day != 11 {
		t.Fatalf("Parse 失败: %v %v", d, err)
	}
	for _, s := range []string{"2025-9-11", "20250911", "2025/09/11", "", "abc", "2025-13-01", "2025-02-30"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) 应报错", s)
		}
	}
}

func TestStringRoundTrip(t *testing.T) {
	for _, s := range []string{"2025-01-01", "2024-02-29", "1999-12-31", "0001-01-01", "9999-12-31"} {
		d := MustParse(s)
		if got := d.String(); got != s {
			t.Errorf("%s → %s", s, got)
		}
	}
}

func TestCompare(t *testing.T) {
	a, b := MustParse("2025-09-11"), MustParse("2025-09-12")
	if !a.Before(b) || !b.After(a) || a.Equal(b) {
		t.Error("比较结果错误")
	}
	if a.Compare(a) != 0 {
		t.Error("自比较应为 0")
	}
	// 跨年、跨月
	if !MustParse("2024-12-31").Before(MustParse("2025-01-01")) {
		t.Error("跨年比较错误")
	}
	if !MustParse("2025-01-31").Before(MustParse("2025-02-01")) {
		t.Error("跨月比较错误")
	}
}

func TestBetween(t *testing.T) {
	from, to := MustParse("2025-09-01"), MustParse("2025-09-30")
	// 闭区间：两端都必须包含 —— 会计上「本期」是闭区间
	if !MustParse("2025-09-01").Between(from, to) {
		t.Error("起始日应包含在区间内")
	}
	if !MustParse("2025-09-30").Between(from, to) {
		t.Error("结束日应包含在区间内")
	}
	if MustParse("2025-08-31").Between(from, to) {
		t.Error("区间外日期不应包含")
	}
	if MustParse("2025-10-01").Between(from, to) {
		t.Error("区间外日期不应包含")
	}
}

func TestAddDays(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"2025-09-11", 1, "2025-09-12"},
		{"2025-09-11", -1, "2025-09-10"},
		{"2025-09-30", 1, "2025-10-01"},
		{"2025-12-31", 1, "2026-01-01"},
		{"2024-02-28", 1, "2024-02-29"},
		{"2023-02-28", 1, "2023-03-01"},
		{"2025-01-01", -1, "2024-12-31"},
		{"2025-09-11", 0, "2025-09-11"},
		{"2025-09-11", 365, "2026-09-11"},
	}
	for _, c := range cases {
		if got := MustParse(c.in).AddDays(c.n).String(); got != c.want {
			t.Errorf("%s AddDays(%d) = %s，期望 %s", c.in, c.n, got, c.want)
		}
	}
}

// 月末对齐：1 月 31 日加一个月应落到 2 月末，而不是 3 月 2/3 日。
func TestAddMonthsMonthEndAlignment(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"2025-01-31", 1, "2025-02-28"},
		{"2024-01-31", 1, "2024-02-29"},
		{"2025-03-31", 1, "2025-04-30"},
		{"2025-08-31", 1, "2025-09-30"},
		{"2025-09-30", 1, "2025-10-31"},
		{"2025-09-15", 1, "2025-10-15"},
		{"2025-01-15", 12, "2026-01-15"},
		{"2025-01-15", -1, "2024-12-15"},
		{"2025-01-15", -13, "2023-12-15"},
	}
	for _, c := range cases {
		if got := MustParse(c.in).AddMonths(c.n).String(); got != c.want {
			t.Errorf("%s AddMonths(%d) = %s，期望 %s", c.in, c.n, got, c.want)
		}
	}
}

func TestMonthBoundaries(t *testing.T) {
	d := MustParse("2024-02-15")
	if got := d.FirstDayOfMonth().String(); got != "2024-02-01" {
		t.Errorf("FirstDayOfMonth = %s", got)
	}
	if got := d.LastDayOfMonth().String(); got != "2024-02-29" {
		t.Errorf("LastDayOfMonth = %s", got)
	}
	if got := MustParse("2023-02-15").LastDayOfMonth().String(); got != "2023-02-28" {
		t.Errorf("平年 2 月末 = %s", got)
	}
	if got := MustParse("2025-12-31").LastDayOfMonth().String(); got != "2025-12-31" {
		t.Errorf("12 月末 = %s", got)
	}
}

// ★ 时区回归测试。
//
// Frappe Books 把会计日期存成「本地日期的 UTC 午夜」Datetime，
// 日期筛选退化为字符串比较，导致 '2025-09-11T00:00:00.000Z' <= '2025-09-11'
// 为假 —— 报表的 toDate 当天被静默漏掉。
//
// 本类型只有 Y/M/D，不涉及时区，因此不存在这个问题。
func TestNoTimezoneDrift(t *testing.T) {
	// 在任意时区的 23:59 取日期，都应是当天而非次日
	loc := time.FixedZone("UTC+8", 8*3600)
	for _, hh := range []int{0, 12, 23} {
		tm := time.Date(2025, 9, 11, hh, 59, 59, 0, loc)
		if got := FromTime(tm).String(); got != "2025-09-11" {
			t.Errorf("在 %s 的 %02d:59:59 取日期得到 %s，期望 2025-09-11", loc, hh, got)
		}
	}
	loc = time.FixedZone("UTC-5", -5*3600)
	for _, hh := range []int{0, 12, 23} {
		tm := time.Date(2025, 9, 11, hh, 0, 0, 0, loc)
		if got := FromTime(tm).String(); got != "2025-09-11" {
			t.Errorf("在 %s 的 %02d:00:00 取日期得到 %s，期望 2025-09-11", loc, hh, got)
		}
	}
	// Time() 强制 UTC，避免后续比较漂移
	d := MustParse("2025-09-11")
	if z, off := d.Time().Zone(); z != "UTC" || off != 0 {
		t.Errorf("Time() 应返回 UTC，得到 %s/%d", z, off)
	}
}

func TestTimeRoundTrip(t *testing.T) {
	for _, s := range []string{"2025-01-01", "2024-02-29", "1999-12-31"} {
		d := MustParse(s)
		if got := FromTime(d.Time()).String(); got != s {
			t.Errorf("往返失败 %s → %s", s, got)
		}
	}
}

func TestZeroDate(t *testing.T) {
	var d Date
	if !d.IsZero() {
		t.Error("零值应为 IsZero")
	}
	if d.Valid() {
		t.Error("零值不应 Valid")
	}
	if d.String() != "" {
		t.Errorf("零值的 String 应为空，得到 %q", d.String())
	}
	if !d.Time().IsZero() {
		t.Error("零值的 Time 应返回零 time.Time")
	}
}

func TestRange(t *testing.T) {
	r := Range{From: MustParse("2025-09-01"), To: MustParse("2025-09-30")}
	if !r.Contains(MustParse("2025-09-15")) {
		t.Error("区间应包含 09-15")
	}
	if r.Contains(MustParse("2025-10-01")) {
		t.Error("区间不应包含 10-01")
	}
	if got := r.Days(); got != 30 {
		t.Errorf("9 月天数 = %d，期望 30", got)
	}
	if got := (Range{From: MustParse("2025-01-01"), To: MustParse("2025-12-31")}).Days(); got != 365 {
		t.Errorf("2025 年天数 = %d，期望 365", got)
	}
	if got := (Range{From: MustParse("2024-01-01"), To: MustParse("2024-12-31")}).Days(); got != 366 {
		t.Errorf("2024 年天数 = %d，期望 366", got)
	}
}

func TestDaysInMonthAndLeap(t *testing.T) {
	for _, y := range []int{2023, 2024, 2025, 1900, 2000, 2100, 2400} {
		want := 28
		if IsLeap(y) {
			want = 29
		}
		if got := DaysInMonth(y, 2); got != want {
			t.Errorf("%d 年 2 月天数 = %d，期望 %d", y, got, want)
		}
	}
	if !IsLeap(2000) || IsLeap(1900) || IsLeap(2100) || !IsLeap(2400) {
		t.Error("世纪闰年规则错误")
	}
}
