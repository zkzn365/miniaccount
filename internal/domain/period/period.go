// Package period 实现会计期间及结账/反结账状态机。
//
// Frappe Books 只有 fiscalYearStart/End 两个日期，**没有期间概念、不能结账**，
// fiscal year 仅仅用来给报表划日期区间。这在中国实务里不可用：
//
//   - 无法出月报（每月必须结账并出报表）
//   - 已结账月份的数据可以被随意改动，账目不可信
//   - 没有期末结转损益这个动作，利润表与资产负债表永远对不上
//
// 本包把期间作为一等概念，并强制以下规则：
//
//	规则 1  只能向 open 状态的期间过账
//	规则 2  一张凭证的所有分录必须落在同一期间
//	规则 3  凭证日期必须落在该期间的起止日内
//	规则 4  结账是顺序的：前一期间已结账，本期才能结账
//	规则 5  反结账是逆序的：后一期间未结账，本期才能反结账
//	规则 6  已结账期间禁止任何写入
package period

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"miniaccount/internal/domain/calendar"
)

// Status 是会计期间的状态。
type Status string

// 期间状态。
const (
	StatusFuture Status = "future" // 未启用（未来期间）
	StatusOpen   Status = "open"   // 已启用，可录入可过账
	StatusClosed Status = "closed" // 已结账，禁止任何写入
)

// Label 返回中文名。
func (s Status) Label() string {
	switch s {
	case StatusFuture:
		return "未启用"
	case StatusOpen:
		return "已启用"
	case StatusClosed:
		return "已结账"
	default:
		return string(s)
	}
}

// 期间相关错误。
var (
	ErrPeriodNotFound   = errors.New("period: 会计期间不存在")
	ErrBeforeBookStart  = errors.New("period: 早于账套启用期间")
	ErrNotOpen          = errors.New("period: 会计期间未启用，不能过账")
	ErrAlreadyClosed    = errors.New("period: 会计期间已结账")
	ErrNotClosed        = errors.New("period: 会计期间尚未结账")
	ErrPrevNotClosed    = errors.New("period: 上一会计期间尚未结账，不能结账本期")
	ErrNextNotClosed    = errors.New("period: 下一会计期间已结账，不能反结账本期")
	ErrCrossPeriod      = errors.New("period: 一张凭证的分录不能跨会计期间")
	ErrBadPeriodRange   = errors.New("period: 非法期间范围")
	ErrInvalidYearMonth = errors.New("period: 非法年月")
)

// Key 是会计期间的标识（公历年度 + 月份）。
//
// 中国《会计法》与《小企业会计准则》要求会计年度为公历 1 月 1 日至 12 月 31 日，
// 因此不存在自定义会计年度起始月的问题，直接用 (年, 月) 即可。
type Key struct {
	Year  int
	Month int
}

// NewKey 构造一个会计期间标识。
//
// 提供构造器而非让调用方写字面量：跨包使用 period.Key{2025, 9}
// 这种无键字面量既过不了 go vet，可读性也差。
func NewKey(year, month int) Key { return Key{Year: year, Month: month} }

// Valid 报告年月是否合法。
func (k Key) Valid() bool {
	return k.Year >= 1 && k.Year <= 9999 && k.Month >= 1 && k.Month <= 12
}

// String 返回 "2025-09"。
func (k Key) String() string { return fmt.Sprintf("%04d-%02d", k.Year, k.Month) }

// Compare 返回 -1、0 或 1。
func (k Key) Compare(o Key) int {
	if k.Year != o.Year {
		return sign(k.Year - o.Year)
	}
	return sign(k.Month - o.Month)
}

// Before 报告 k 是否早于 o。
func (k Key) Before(o Key) bool { return k.Compare(o) < 0 }

// Next 返回下一个期间。
func (k Key) Next() Key {
	if k.Month == 12 {
		return Key{k.Year + 1, 1}
	}
	return Key{k.Year, k.Month + 1}
}

// Prev 返回上一个期间。
func (k Key) Prev() Key {
	if k.Month == 1 {
		return Key{k.Year - 1, 12}
	}
	return Key{k.Year, k.Month - 1}
}

// QueryYear 返回期间所属的查询年度，便于按年筛选。
func (k Key) QueryYear() int { return k.Year }

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

// Period 是一个会计期间。
type Period struct {
	Key      Key
	Range    calendar.Range // 该期间的起止日期（含两端）
	Status   Status
	ClosedAt *time.Time
	ClosedBy string
}

// IsOpen 报告期间是否可录入。
func (p *Period) IsOpen() bool { return p.Status == StatusOpen }

// IsClosed 报告期间是否已结账。
func (p *Period) IsClosed() bool { return p.Status == StatusClosed }

// IsFrozen 报告期间是否处于未启用状态（尚未开放录入）。
func (p *Period) IsFrozen() bool { return p.Status == StatusFuture }

// String 返回如 "2025-09 (2025-09-01..2025-09-30, 已结账)"。
func (p *Period) String() string {
	return fmt.Sprintf("%s (%s, %s)", p.Key, p.Range, p.Status.Label())
}

// ---------------------------------------------------------------------------
// Calendar
// ---------------------------------------------------------------------------

// Calendar 是一个账套的全部会计期间。
//
// 它是有序的、连续的：从账套启用期间一直到最后一个已生成的期间，
// 不存在空洞 —— 这是顺序结账规则能够成立的前提。
type Calendar struct {
	bookStart Key
	periods   map[Key]*Period
	keys      []Key // 升序
}

// NewCalendar 生成从 (startYear, startMonth) 到 throughYear 年 12 月的
// 全部月度期间，初始状态为：
//
//   - 启用期间（startYear/startMonth）→ open
//   - 其后到「当前期间」→ open
//   - 更后面的 → future
//
// current 用于决定哪些期间默认开放；传零值则以启用期间为准。
func NewCalendar(startYear, startMonth, throughYear int, current Key) (*Calendar, error) {
	start := Key{startYear, startMonth}
	if !start.Valid() {
		return nil, fmt.Errorf("%w: 启用期间 %04d-%02d", ErrInvalidYearMonth, startYear, startMonth)
	}
	if throughYear < startYear {
		return nil, fmt.Errorf("%w: 结束年度 %d 早于启用年度 %d",
			ErrBadPeriodRange, throughYear, startYear)
	}
	if current.Year == 0 {
		current = start
	}

	c := &Calendar{
		bookStart: start,
		periods:   make(map[Key]*Period),
	}
	for k := start; k.Year <= throughYear; k = k.Next() {
		r := calendar.Range{From: firstDay(k), To: lastDay(k)}
		st := StatusFuture
		if k.Compare(current) <= 0 {
			st = StatusOpen
		}
		c.periods[k] = &Period{Key: k, Range: r, Status: st}
		c.keys = append(c.keys, k)
	}
	sort.Slice(c.keys, func(i, j int) bool { return c.keys[i].Before(c.keys[j]) })
	return c, nil
}

func firstDay(k Key) calendar.Date {
	d, _ := calendar.New(k.Year, k.Month, 1)
	return d
}

func lastDay(k Key) calendar.Date {
	d, _ := calendar.New(k.Year, k.Month, calendar.DaysInMonth(k.Year, k.Month))
	return d
}

// BookStart 返回账套启用期间。
func (c *Calendar) BookStart() Key { return c.bookStart }

// Keys 返回全部期间，按时间升序。
func (c *Calendar) Keys() []Key { return c.keys }

// Len 返回期间总数。
func (c *Calendar) Len() int { return len(c.keys) }

// Get 按年月取期间。
func (c *Calendar) Get(y, m int) (*Period, bool) {
	p, ok := c.periods[Key{y, m}]
	return p, ok
}

// PeriodOf 返回某个日期所在的会计期间。
func (c *Calendar) PeriodOf(d calendar.Date) (*Period, bool) {
	if d.IsZero() {
		return nil, false
	}
	p, ok := c.periods[Key{d.Year, d.Month}]
	return p, ok
}

// All 返回全部期间（升序）。
func (c *Calendar) All() []*Period {
	out := make([]*Period, 0, len(c.keys))
	for _, k := range c.keys {
		out = append(out, c.periods[k])
	}
	return out
}

// First 返回第一个期间。
func (c *Calendar) First() *Period { return c.periods[c.keys[0]] }

// Last 返回最后一个期间。
func (c *Calendar) Last() *Period { return c.periods[c.keys[len(c.keys)-1]] }

// OfYear 返回某年度的 12 个期间（若未生成到该年度则为实际数量）。
func (c *Calendar) OfYear(year int) []*Period {
	var out []*Period
	for _, k := range c.keys {
		if k.Year == year {
			out = append(out, c.periods[k])
		}
	}
	return out
}

// OpenPeriods 返回全部 open 状态的期间。
func (c *Calendar) OpenPeriods() []*Period {
	var out []*Period
	for _, k := range c.keys {
		if c.periods[k].IsOpen() {
			out = append(out, c.periods[k])
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 过账校验（规则 1、2、3、6）
// ---------------------------------------------------------------------------

// CheckPostable 校验某个日期是否可以过账。
//
// 这是过账前的必检项。失败时返回的错误区分了具体原因，
// 便于前端给出准确提示。
func (c *Calendar) CheckPostable(d calendar.Date) (*Period, error) {
	if d.IsZero() {
		return nil, fmt.Errorf("%w: 日期为空", ErrPeriodNotFound)
	}
	// 注意：复合字面量出现在 if 的初始化子句里必须加括号，否则与 { 产生歧义
	k := Key{Year: d.Year, Month: d.Month}
	if k.Before(c.bookStart) {
		return nil, fmt.Errorf("%w: %s 早于账套启用期间 %s",
			ErrBeforeBookStart, d, c.bookStart)
	}
	p, ok := c.PeriodOf(d)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrPeriodNotFound, d)
	}
	switch p.Status {
	case StatusOpen:
		return p, nil
	case StatusClosed:
		return nil, fmt.Errorf("%w: %s 已结账，如需修改请先反结账", ErrNotOpen, p.Key)
	default:
		return nil, fmt.Errorf("%w: %s 尚未启用", ErrNotOpen, p.Key)
	}
}

// CheckSamePeriod 校验一组日期是否落在同一期间（规则 2）。
//
// 跨期凭证在中国实务中不允许：它会让两个月的账都无法独立说清。
// 需要跨期时应拆成两张凭证。
func (c *Calendar) CheckSamePeriod(dates ...calendar.Date) (*Period, error) {
	var first *Period
	for _, d := range dates {
		p, err := c.CheckPostable(d)
		if err != nil {
			return nil, err
		}
		if first == nil {
			first = p
			continue
		}
		if p.Key != first.Key {
			return nil, fmt.Errorf("%w: %s 与 %s 分属 %s、%s",
				ErrCrossPeriod, d, first.Range.From, p.Key, first.Key)
		}
	}
	if first == nil {
		return nil, fmt.Errorf("%w: 未提供日期", ErrPeriodNotFound)
	}
	return first, nil
}

// CheckWritable 校验某个期间是否允许写入（新增/修改/删除凭证）。
func (c *Calendar) CheckWritable(k Key) (*Period, error) {
	p, ok := c.periods[k]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrPeriodNotFound, k)
	}
	if p.IsClosed() {
		return nil, fmt.Errorf("%w: %s 已结账，禁止修改", ErrAlreadyClosed, k)
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// 结账 / 反结账（规则 4、5）
// ---------------------------------------------------------------------------

// CanClose 校验某个期间是否可以结账（规则 4：顺序结账）。
//
// 顺序结账的意义：月度报表是逐月累积的，6 月的数据依赖 5 月已定稿。
// 允许跳月结账会让期中数据长期处于不确定状态。
func (c *Calendar) CanClose(k Key) error {
	p, ok := c.periods[k]
	if !ok {
		return fmt.Errorf("%w: %s", ErrPeriodNotFound, k)
	}
	if p.IsClosed() {
		return fmt.Errorf("%w: %s", ErrAlreadyClosed, k)
	}
	// 上一期间必须存在且已结账；账套启用期间例外
	prev := k.Prev()
	if !prev.Before(c.bookStart) {
		pp, ok := c.periods[prev]
		if !ok {
			return fmt.Errorf("%w: %s", ErrPeriodNotFound, prev)
		}
		if !pp.IsClosed() {
			return fmt.Errorf("%w: 本期 %s，上一期间 %s 状态为%s",
				ErrPrevNotClosed, k, prev, pp.Status.Label())
		}
	}
	return nil
}

// Close 结账某个期间。
//
// 调用方（service 层）必须在此之前完成：
//   - 期末结转损益凭证的生成与过账
//   - 结账前体检（试算平衡、资产负债表勾稽、未处理流水等）
//
// 本函数只负责状态迁移，不生成凭证 —— 保持领域层纯粹。
func (c *Calendar) Close(k Key, by string, at time.Time) error {
	if err := c.CanClose(k); err != nil {
		return err
	}
	p := c.periods[k]
	p.Status = StatusClosed
	p.ClosedBy = by
	t := at
	p.ClosedAt = &t

	// 结账后自动启用下一期间，让用户可以立即开始下月记账
	nk := k.Next()
	if np, ok := c.periods[nk]; ok && np.Status == StatusFuture {
		np.Status = StatusOpen
	}
	return nil
}

// CanReopen 校验某个期间是否可以反结账（规则 5：逆序反结账）。
func (c *Calendar) CanReopen(k Key) error {
	p, ok := c.periods[k]
	if !ok {
		return fmt.Errorf("%w: %s", ErrPeriodNotFound, k)
	}
	if !p.IsClosed() {
		return fmt.Errorf("%w: %s 当前状态为%s", ErrNotClosed, k, p.Status.Label())
	}
	nk := k.Next()
	if np, ok := c.periods[nk]; ok && np.IsClosed() {
		return fmt.Errorf("%w: 下一期间 %s 已结账，请先反结账 %s",
			ErrNextNotClosed, nk, nk)
	}
	return nil
}

// Reopen 反结账某个期间。
//
// 调用方必须在此之前**冲销该期的期末结转损益凭证**，
// 否则损益类科目会残留已结转的状态，导致利润表数据错误。
func (c *Calendar) Reopen(k Key) error {
	if err := c.CanReopen(k); err != nil {
		return err
	}
	p := c.periods[k]
	p.Status = StatusOpen
	p.ClosedBy = ""
	p.ClosedAt = nil

	// 反结账后，后续期间若已启用则退回未来状态，避免出现两个开放期间
	// 造成「先记 10 月再记 9 月」的乱序。
	for nk := k.Next(); ; nk = nk.Next() {
		np, ok := c.periods[nk]
		if !ok {
			break
		}
		if np.Status == StatusOpen {
			np.Status = StatusFuture
		}
	}
	return nil
}

// currentOpen 返回当前应当记账的期间（最早的那个 open 期间）。
func (c *Calendar) currentOpen() *Period {
	for _, k := range c.keys {
		if c.periods[k].IsOpen() {
			return c.periods[k]
		}
	}
	return nil
}

// Current 返回当前应记账的期间（最早的 open 期间）；全部结账时为 nil。
func (c *Calendar) Current() *Period { return c.currentOpen() }

// EnsureThrough 保证期间已生成到指定的年月（含）。
//
// 账套跨年后需要调用它来扩展期间表，否则新年度无法记账。
func (c *Calendar) EnsureThrough(k Key) {
	if !k.Valid() {
		return
	}
	last := c.keys[len(c.keys)-1]
	for cur := last.Next(); !k.Before(cur); cur = cur.Next() {
		c.periods[cur] = &Period{
			Key:    cur,
			Range:  calendar.Range{From: firstDay(cur), To: lastDay(cur)},
			Status: StatusFuture,
		}
		c.keys = append(c.keys, cur)
	}
}

// YearRange 返回某年度涵盖的日期闭区间。
func (c *Calendar) YearRange(year int) (calendar.Range, error) {
	from, err := calendar.New(year, 1, 1)
	if err != nil {
		return calendar.Range{}, fmt.Errorf("%w: %d 年", ErrInvalidYearMonth, year)
	}
	to, _ := calendar.New(year, 12, 31)
	return calendar.Range{From: from, To: to}, nil
}

// CloseSummary 汇总各期间的结账状态，用于「期间管理」界面与结账前体检。
type CloseSummary struct {
	Key      Key
	Status   Status
	ClosedBy string
	ClosedAt *time.Time
}

// Summary 返回全部期间的结账状态摘要。
func (c *Calendar) Summary() []CloseSummary {
	out := make([]CloseSummary, 0, len(c.keys))
	for _, k := range c.keys {
		p := c.periods[k]
		out = append(out, CloseSummary{
			Key: p.Key, Status: p.Status, ClosedBy: p.ClosedBy, ClosedAt: p.ClosedAt,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// 从持久化数据重建
// ---------------------------------------------------------------------------

// ClosedInfo 记录一个期间的结账信息。
type ClosedInfo struct {
	By string
	At *time.Time
}

// FromRows 从存储层读出的行重建 Calendar。
//
// 之所以不让 Calendar 直接查数据库：领域层保持纯粹，
// 全部持久化细节留在 store 包，这样期间状态机可以用内存数据完整单测。
func FromRows(keys []Key, status map[Key]Status, closed map[Key]ClosedInfo) (*Calendar, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: 期间表为空", ErrPeriodNotFound)
	}
	sorted := make([]Key, len(keys))
	copy(sorted, keys)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Before(sorted[j]) })

	// 期间必须连续，否则顺序结账规则无法成立
	for i := 1; i < len(sorted); i++ {
		if sorted[i-1].Next() != sorted[i] {
			return nil, fmt.Errorf("%w: %s 与 %s 之间缺期间",
				ErrBadPeriodRange, sorted[i-1], sorted[i])
		}
	}

	c := &Calendar{
		bookStart: sorted[0],
		periods:   make(map[Key]*Period, len(sorted)),
		keys:      sorted,
	}
	for _, k := range sorted {
		p := &Period{
			Key:   k,
			Range: calendar.Range{From: firstDay(k), To: lastDay(k)},
			// 缺失状态时按未启用处理，宁可拒绝过账也不误放行
			Status: StatusFuture,
		}
		if st, ok := status[k]; ok {
			p.Status = st
		}
		if ci, ok := closed[k]; ok {
			p.ClosedBy = ci.By
			p.ClosedAt = ci.At
		}
		c.periods[k] = p
	}
	return c, nil
}
