// Package calendar 提供「只有日期、没有时间」的 Date 类型。
//
// # 为什么不用 time.Time
//
// 会计日期是**日历日期**，不是时间点。用 time.Time 承载会计日期会引入
// 一整类时区 bug —— 这在 Frappe Books 里真实发生过：
//
//   - 它把记账日期存成「本地日期对应的 UTC 午夜」的 Datetime 字符串；
//   - 日期筛选退化成**字符串比较**，于是
//     '2025-09-11T00:00:00.000Z' <= '2025-09-11' 为**假**；
//   - 结果是报表的 toDate 当天被静默排除，各处靠「+1 天」打补丁，
//     而补丁并不一致（总账 +1 天、费用报表另写一套）。
//
// 本项目从根本上避免这个问题：会计日期只用 Date，
// 内部以 Y/M/D 三个整数存储，比较是数值比较，永不涉及时区。
package calendar

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// 日期相关错误。
var (
	ErrBadDate    = errors.New("calendar: 非法日期")
	ErrBadFormat  = errors.New("calendar: 日期格式应为 YYYY-MM-DD")
	ErrOutOfRange = errors.New("calendar: 日期超出可表示范围")
)

// Date 是一个日历日期（公历）。零值表示「未设置」，用 IsZero 判断。
type Date struct {
	Year  int `json:"year"`
	Month int `json:"month"` // 1..12
	Day   int `json:"day"`   // 1..31
}

// New 构造一个日期并校验其合法性（含闰年、月长）。
func New(year, month, day int) (Date, error) {
	d := Date{Year: year, Month: month, Day: day}
	if !d.Valid() {
		return Date{}, fmt.Errorf("%w: %04d-%02d-%02d", ErrBadDate, year, month, day)
	}
	return d, nil
}

// MustNew 与 New 相同，非法时 panic。仅用于测试与常量。
func MustNew(year, month, day int) Date {
	d, err := New(year, month, day)
	if err != nil {
		panic(err)
	}
	return d
}

// Parse 解析 "YYYY-MM-DD" 形式的日期。
func Parse(s string) (Date, error) {
	s = strings.TrimSpace(s)
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return Date{}, fmt.Errorf("%w: %q", ErrBadFormat, s)
	}
	y, ok1 := atoi(s[0:4])
	m, ok2 := atoi(s[5:7])
	day, ok3 := atoi(s[8:10])
	if !ok1 || !ok2 || !ok3 {
		return Date{}, fmt.Errorf("%w: %q", ErrBadFormat, s)
	}
	return New(y, m, day)
}

// MustParse 与 Parse 相同，失败时 panic。
func MustParse(s string) Date {
	d, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

func atoi(s string) (int, bool) {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// IsZero 报告日期是否为未设置状态。
func (d Date) IsZero() bool { return d.Year == 0 && d.Month == 0 && d.Day == 0 }

// Valid 报告日期是否为真实存在的公历日期。
func (d Date) Valid() bool {
	if d.Year < 1 || d.Year > 9999 {
		return false
	}
	if d.Month < 1 || d.Month > 12 {
		return false
	}
	return d.Day >= 1 && d.Day <= DaysInMonth(d.Year, d.Month)
}

// DaysInMonth 返回某年某月的天数（正确处理闰年）。
func DaysInMonth(year, month int) int {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if IsLeap(year) {
			return 29
		}
		return 28
	default:
		return 0
	}
}

// IsLeap 报告是否闰年。
func IsLeap(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

// String 返回 "YYYY-MM-DD"。这是写数据库与传 API 的规范形式。
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// Compare 返回 -1、0 或 1。
func (d Date) Compare(o Date) int {
	if d.Year != o.Year {
		return sign(d.Year - o.Year)
	}
	if d.Month != o.Month {
		return sign(d.Month - o.Month)
	}
	return sign(d.Day - o.Day)
}

// Before 报告 d 是否早于 o。
func (d Date) Before(o Date) bool { return d.Compare(o) < 0 }

// After 报告 d 是否晚于 o。
func (d Date) After(o Date) bool { return d.Compare(o) > 0 }

// Equal 报告两个日期是否相同。
func (d Date) Equal(o Date) bool { return d.Compare(o) == 0 }

// Between 报告 d 是否在 [from, to] 闭区间内。
// 会计上「本期」永远是闭区间，用闭区间可避免开闭混用导致的漏记。
func (d Date) Between(from, to Date) bool {
	return d.Compare(from) >= 0 && d.Compare(to) <= 0
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}

// AddDays 返回偏移 n 天后的日期（n 可为负）。
func (d Date) AddDays(n int) Date {
	if d.IsZero() {
		return d
	}
	// 转成儒略日序数做加减，避免逐月进位逻辑
	jd := d.toJDN() + n
	out, _ := fromJDN(jd)
	return out
}

// AddMonths 返回偏移 n 个月后的日期，遵循**月末对齐**规则：
//
//   - 若原日期是其所在月的最后一天，结果取目标月的最后一天
//     （2025-09-30 + 1 月 → 2025-10-31，而非 10-30）
//   - 否则若目标月没有该日，取目标月最后一天
//     （2025-01-31 + 1 月 → 2025-02-28）
//
// 月末对齐符合会计期间运算的直觉：季末减三个月应当仍是季末。
// Frappe Books 的 _fixMonthsJump 想解决同一问题，但只在「目标月更短」时
// 才对位，方向是反的 —— 9 月 30 日加一月会得到 10 月 30 日。
func (d Date) AddMonths(n int) Date {
	if d.IsZero() {
		return d
	}
	total := d.Year*12 + (d.Month - 1) + n
	y := total / 12
	m := total%12 + 1
	if m <= 0 {
		m += 12
		y--
	}
	targetMax := DaysInMonth(y, m)
	day := d.Day
	if d.Day == DaysInMonth(d.Year, d.Month) {
		// 原日期是所在月最后一天 → 对齐到目标月最后一天
		day = targetMax
	} else if day > targetMax {
		day = targetMax
	}
	out, err := New(y, m, day)
	if err != nil {
		return d
	}
	return out
}

// FirstDayOfMonth 返回所在月的第一天。
func (d Date) FirstDayOfMonth() Date {
	if d.IsZero() {
		return d
	}
	out, _ := New(d.Year, d.Month, 1)
	return out
}

// LastDayOfMonth 返回所在月的最后一天。
func (d Date) LastDayOfMonth() Date {
	if d.IsZero() {
		return d
	}
	out, _ := New(d.Year, d.Month, DaysInMonth(d.Year, d.Month))
	return out
}

// YearMonth 返回 (年, 月)，用于按会计期间分组。
func (d Date) YearMonth() (int, int) { return d.Year, d.Month }

// toJDN 转换为儒略日序数（Julian Day Number），用于日期加减。
func (d Date) toJDN() int {
	a := (14 - d.Month) / 12
	y := d.Year + 4800 - a
	m := d.Month + 12*a - 3
	return d.Day + (153*m+2)/5 + 365*y + y/4 - y/100 + y/400 - 32045
}

// fromJDN 由儒略日序数还原日期。
func fromJDN(jdn int) (Date, error) {
	a := jdn + 32044
	b := (4*a + 3) / 146097
	c := a - 146097*b/4
	d := (4*c + 3) / 1461
	e := c - 1461*d/4
	m := (5*e + 2) / 153
	day := e - (153*m+2)/5 + 1
	month := m + 3 - 12*(m/10)
	year := 100*b + d - 4800 + m/10
	return New(year, month, day)
}

// Time 返回该日期在 **UTC** 的零点。
//
// 仅在必须与 time.Time 交互时使用（如文件时间戳、日志）。
// 注意：传入本地时区会立刻引入时区 bug，因此这里强制 UTC。
func (d Date) Time() time.Time {
	if d.IsZero() {
		return time.Time{}
	}
	return time.Date(d.Year, time.Month(d.Month), d.Day, 0, 0, 0, 0, time.UTC)
}

// FromTime 取 t 在**其自身时区**下的日历日期。
//
//	FromTime(time.Date(2025, 9, 11, 23, 30, 0, 0, time.Local)) → 2025-09-11
//
// 这修正了「先转 UTC 再取日期」在时区偏移下会整体偏移一天的问题。
func FromTime(t time.Time) Date {
	y, m, dd := t.Date()
	d, err := New(y, int(m), dd)
	if err != nil {
		return Date{}
	}
	return d
}

// Today 返回本地时区的今天。
func Today() Date { return FromTime(time.Now()) }

// Range 是闭区间 [From, To]。
type Range struct {
	From Date
	To   Date
}

// Contains 报告 d 是否落在区间内。
func (r Range) Contains(d Date) bool { return d.Between(r.From, r.To) }

// String 返回 "from..to"。
func (r Range) String() string { return r.From.String() + ".." + r.To.String() }

// Days 返回区间天数（含两端）；空区间返回 0。
func (r Range) Days() int {
	if r.From.IsZero() || r.To.IsZero() || r.To.Before(r.From) {
		return 0
	}
	return r.To.toJDN() - r.From.toJDN() + 1
}
