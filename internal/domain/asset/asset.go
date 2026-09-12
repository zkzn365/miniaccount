// Package asset 是固定资产与费用摊销的领域逻辑。
//
// # 这两件事为什么放在一起
//
// 它们的形状是同一个：一笔钱先资本化（固定资产 / 长期待摊费用），
// 再在若干个月里按直线法转成费用。用户要填的东西也几乎一样
// （原值、年限、起算日、费用科目、部门），产出也一样（一张计提凭证）。
// 分成两个模块会让人做两遍同样的表单、两遍同样的校验。
//
// # 与工资模块的区别
//
// 工资每月重算（考勤、增减员都在变），所以要「生成工资单」这一层快照。
// 折旧与摊销是**算出来的**：给定原值、年限、起算日，任何一期的金额
// 都是确定的，不需要用户每月确认一次。所以这里只有卡片 + 计提记录，
// 没有「本期工资单」式的中间单据。
package asset

import (
	"errors"
	"fmt"
	"strings"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

// ---------------------------------------------------------------------------
// 税法类别与最低折旧年限
// ---------------------------------------------------------------------------

// Category 是固定资产的税法类别。
//
// ★ 类别不是「给资产分个组好看」，它对应《企业所得税法实施条例》
// 第六十条的最低折旧年限。会计上当然可以缩得更短（比如电子设备按 2 年提），
// 但那样每期都要做纳税调整 —— 小微企业没这个精力。所以：
// 默认给出最低年限，用户填短了**只警告不拦**，并在提示里把
// 「要纳税调整」说清楚。
type Category string

// 税法类别。
const (
	CatBuilding   Category = "building"   // 房屋、建筑物
	CatMachine    Category = "machine"    // 飞机、火车、轮船、机器、机械和其他生产设备
	CatFurniture  Category = "furniture"  // 与生产经营活动有关的器具、工具、家具等
	CatVehicle    Category = "vehicle"    // 飞机、火车、轮船以外的运输工具
	CatElectronic Category = "electronic" // 电子设备
)

// Categories 返回全部类别（界面下拉用）。
func Categories() []Category {
	return []Category{CatBuilding, CatMachine, CatFurniture, CatVehicle, CatElectronic}
}

// Valid 报告类别是否可识别。
func (c Category) Valid() bool {
	for _, x := range Categories() {
		if x == c {
			return true
		}
	}
	return false
}

// Label 返回中文名。
func (c Category) Label() string {
	switch c {
	case CatBuilding:
		return "房屋、建筑物"
	case CatMachine:
		return "机器、机械和其他生产设备"
	case CatFurniture:
		return "器具、工具、家具"
	case CatVehicle:
		return "运输工具（不含飞机、火车、轮船）"
	case CatElectronic:
		return "电子设备"
	default:
		return string(c)
	}
}

// MinYears 是税法规定的最低折旧年限。
func (c Category) MinYears() int {
	switch c {
	case CatBuilding:
		return 20
	case CatMachine:
		return 10
	case CatFurniture:
		return 5
	case CatVehicle:
		return 4
	case CatElectronic:
		return 3
	default:
		return 0
	}
}

// MinMonths 是最低折旧年限的月数。
func (c Category) MinMonths() int { return c.MinYears() * 12 }

// ---------------------------------------------------------------------------
// 固定资产卡片
// ---------------------------------------------------------------------------

// Status 是固定资产的状态。
type Status string

// 固定资产状态。
const (
	// StatusInUse 在用：按期计提折旧。
	StatusInUse Status = "in_use"
	// StatusDisposed 已处置：不再计提。
	StatusDisposed Status = "disposed"
)

// FixedAsset 是一张固定资产卡片。
type FixedAsset struct {
	ID   int64
	Code string
	Name string
	// Category 决定最低折旧年限（只用于提示）。
	Category Category
	// DeptID 是使用部门。
	//
	// ★ 不是可选项：折旧要借「管理费用—折旧费」，而这类费用科目
	// 都声明了按部门辅助核算 —— 没有部门，折旧凭证会被
	// 「缺少必需的辅助核算」直接拒绝，而报错要到计提那天才出现。
	DeptID *int64
	// OrigValue 是原值。
	OrigValue money.Money
	// SalvagePPM 是预计净残值率，百万分比：5% → 50000。
	SalvagePPM int64
	// UsefulMonths 是预计使用月数。
	UsefulMonths int
	// StartDate 是**投入使用**日期（不是购买日期）。
	//
	// ★ 折旧从投入使用次月**起**提：当月增加的固定资产当月不提。
	// 填错这个日期，第一期的折旧就整个错位一格。
	StartDate calendar.Date
	// ExpenseAccount 是折旧费的借方科目，如 560205。
	ExpenseAccount string
	// AccumAccount 是累计折旧的贷方科目，默认 1602。
	AccumAccount string
	// Status 在用 / 已处置。
	Status Status
	// DisposedDate 是处置日期；零值表示未处置。
	DisposedDate calendar.Date
	Remark       string
}

// 固定资产相关错误。
var (
	ErrNoName         = errors.New("asset: 固定资产名称不能为空")
	ErrBadOrigValue   = errors.New("asset: 原值必须大于零")
	ErrBadUsefulLife  = errors.New("asset: 预计使用月数必须大于零")
	ErrBadSalvage     = errors.New("asset: 预计净残值率必须在 0% ~ 100% 之间")
	ErrBadStartDate   = errors.New("asset: 投入使用日期不合法")
	ErrMissingAccount = errors.New("asset: 缺少折旧费用科目")
	ErrMissingDept    = errors.New("asset: 缺少使用部门")
	ErrBadCategory    = errors.New("asset: 固定资产类别不合法")
)

// Validate 检查卡片本身是否可用。
func (a FixedAsset) Validate() error {
	if strings.TrimSpace(a.Name) == "" {
		return ErrNoName
	}
	if a.OrigValue <= 0 {
		return ErrBadOrigValue
	}
	if a.UsefulMonths <= 0 {
		return ErrBadUsefulLife
	}
	if a.SalvagePPM < 0 || a.SalvagePPM >= ppmScale {
		return fmt.Errorf("%w（当前 %.2f%%）", ErrBadSalvage, a.SalvageRate()*100)
	}
	if !a.StartDate.Valid() {
		return ErrBadStartDate
	}
	if strings.TrimSpace(a.ExpenseAccount) == "" {
		return ErrMissingAccount
	}
	if a.Category != "" && !a.Category.Valid() {
		return fmt.Errorf("%w: %q", ErrBadCategory, a.Category)
	}
	if a.Status == StatusDisposed && !a.DisposedDate.Valid() {
		return fmt.Errorf("asset: 已处置的固定资产必须填处置日期")
	}
	if a.SalvageValue() >= a.OrigValue {
		return fmt.Errorf("asset: 预计净残值 %s 不能大于等于原值 %s",
			a.SalvageValue(), a.OrigValue)
	}
	return nil
}

// 残值率用百万分比存，避免浮点。
const ppmScale = 1_000_000

// SalvageRate 返回残值率（小数，如 0.05）。
func (a FixedAsset) SalvageRate() float64 {
	return float64(a.SalvagePPM) / float64(ppmScale)
}

// Salvage 是预计净残值，四舍五入到分。
func (a FixedAsset) Salvage() money.Money {
	return money.MulDiv(a.OrigValue, a.SalvagePPM, ppmScale)
}

// SalvageValue 同 Salvage，名字更贴近「值」的读法。
func (a FixedAsset) SalvageValue() money.Money { return a.Salvage() }

// DepreciableBase 是应提折旧总额 = 原值 − 预计净残值。
func (a FixedAsset) DepreciableBase() money.Money {
	return a.OrigValue.Sub(a.Salvage())
}

// MonthlyAmount 是常规月份的折旧额（最后一期可能不同，见 AmountFor）。
func (a FixedAsset) MonthlyAmount() money.Money {
	if a.UsefulMonths <= 0 {
		return 0
	}
	base := a.DepreciableBase()
	// base 一定非负（Validate 已保证净残值 < 原值）。
	return money.MulDiv(base, 1, int64(a.UsefulMonths))
}

// FirstPeriod 是**第一笔**折旧所在的期间：投入使用次月。
//
// 《企业会计准则第 4 号——固定资产》第十四条：
// 当月增加的固定资产，当月不计提折旧，从下月起计提；
// 当月减少的固定资产，当月仍计提折旧，从下月起不计提。
func (a FixedAsset) FirstPeriod() period.Key {
	k := period.NewKey(a.StartDate.Year, a.StartDate.Month)
	return k.Next()
}

// LastPeriod 是最后一笔折旧所在的期间（按预计使用月数）。
func (a FixedAsset) LastPeriod() period.Key {
	k := a.FirstPeriod()
	for i := 1; i < a.UsefulMonths; i++ {
		k = k.Next()
	}
	return k
}

// LastDepreciablePeriod 是这张卡片实际能提折旧的最后一个期间。
//
// 处置当月照提，从次月起停 —— 所以取「按年限算出的最后一期」与
// 「处置当月」中较早的那个。
func (a FixedAsset) LastDepreciablePeriod() period.Key {
	last := a.LastPeriod()
	if a.Status == StatusDisposed && a.DisposedDate.Valid() {
		d := period.NewKey(a.DisposedDate.Year, a.DisposedDate.Month)
		if d.Before(last) {
			return d
		}
	}
	return last
}

// Covers 报告某个期间是否在这张卡片的折旧区间内（不考虑是否已提足）。
func (a FixedAsset) Covers(k period.Key) bool {
	if !k.Valid() {
		return false
	}
	return !k.Before(a.FirstPeriod()) && !a.LastDepreciablePeriod().Before(k)
}

// ---------------------------------------------------------------------------
// 费用摊销
// ---------------------------------------------------------------------------

// Kind 区分固定资产与待摊项目，供统一的计提接口使用。
type Kind string

// 计提对象的种类。
const (
	KindAsset        Kind = "asset"
	KindAmortization Kind = "amortization"
)

// Label 返回中文名。
func (k Kind) Label() string {
	switch k {
	case KindAsset:
		return "固定资产折旧"
	case KindAmortization:
		return "费用摊销"
	default:
		return string(k)
	}
}

// AmortStatus 是待摊项目的状态。
type AmortStatus string

// 待摊项目状态。
const (
	// AmortActive 在摊：按期摊销。
	AmortActive AmortStatus = "active"
	// AmortFinished 已摊完。
	AmortFinished AmortStatus = "finished"
	// AmortVoided 已作废（填错了，不再摊，但保留记录）。
	AmortVoided AmortStatus = "voided"
)

// Amortization 是一个待摊项目（长期待摊费用等）。
type Amortization struct {
	ID   int64
	Code string
	Name string
	// DeptID 是受益部门（费用科目要按部门辅助核算）。
	DeptID *int64
	// Total 是待摊总额。
	Total money.Money
	// Months 是摊销月数。
	Months int
	// StartDate 是**受益期开始**日期：从当月就开始摊。
	//
	// ★ 与固定资产的「次月起提」刻意不同，不是笔误：
	// 固定资产是「当月增加当月不提」（准则第十四条），
	// 而长期待摊费用是已发生的支出在受益期内摊销，
	// 受益期从当月开始，就从当月摊。
	StartDate calendar.Date
	// ExpenseAccount 是费用借方科目（如管理费用—其他）。
	ExpenseAccount string
	// AssetAccount 是待摊费用的贷方科目，默认 1801 长期待摊费用。
	AssetAccount string
	Status       AmortStatus
	Remark       string
}

// 摊销相关错误。
var (
	ErrAmortNoName       = errors.New("amortization: 项目名称不能为空")
	ErrAmortBadTotal     = errors.New("amortization: 待摊总额必须大于零")
	ErrAmortBadMonths    = errors.New("amortization: 摊销月数必须大于零")
	ErrAmortBadStartDate = errors.New("amortization: 开始摊销日期不合法")
	ErrAmortMissingAcc   = errors.New("amortization: 缺少费用科目")
)

// Validate 检查待摊项目是否可用。
func (m Amortization) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return ErrAmortNoName
	}
	if m.Total <= 0 {
		return ErrAmortBadTotal
	}
	if m.Months <= 0 {
		return ErrAmortBadMonths
	}
	if !m.StartDate.Valid() {
		return ErrAmortBadStartDate
	}
	if strings.TrimSpace(m.ExpenseAccount) == "" {
		return ErrAmortMissingAcc
	}
	return nil
}

// FirstPeriod 是第一笔摊销所在的期间：开始当月。
func (m Amortization) FirstPeriod() period.Key {
	return period.NewKey(m.StartDate.Year, m.StartDate.Month)
}

// LastPeriod 是最后一笔摊销所在的期间。
func (m Amortization) LastPeriod() period.Key {
	k := m.FirstPeriod()
	for i := 1; i < m.Months; i++ {
		k = k.Next()
	}
	return k
}

// MonthlyAmount 是常规月份的摊销额（最后一期可能不同）。
func (m Amortization) MonthlyAmount() money.Money {
	if m.Months <= 0 {
		return 0
	}
	return money.MulDiv(m.Total, 1, int64(m.Months))
}

// Covers 报告某个期间是否在摊销区间内。
func (m Amortization) Covers(k period.Key) bool {
	if !k.Valid() || m.Status == AmortVoided {
		return false
	}
	return !k.Before(m.FirstPeriod()) && !m.LastPeriod().Before(k)
}

// Installment 是折旧 / 摊销表里的一期。
type Installment struct {
	Period period.Key
	// Seq 是第几期，从 1 开始。
	Seq int
	// Amount 是本期的金额。
	Amount money.Money
	// Cumulative 是**含本期**的累计额。
	Cumulative money.Money
	// IsLast 标记最后一期 —— 尾差在这一期抹平。
	IsLast bool
}

// Schedule 给出固定资产生命周期内的完整折旧表。
//
// 界面用它做「这张卡以后每月提多少」的预览。**不写库**。
func (a FixedAsset) Schedule() []Installment {
	return schedule(a.FirstPeriod(), a.LastDepreciablePeriod(),
		a.UsefulMonths, a.DepreciableBase())
}

// Schedule 给出待摊项目的完整摊销表。
func (m Amortization) Schedule() []Installment {
	if m.Status == AmortVoided {
		return nil
	}
	return schedule(m.FirstPeriod(), m.LastPeriod(), m.Months, m.Total)
}

// schedule 是折旧与摊销共用的直线法排期。
//
// # 尾差必须由最后一期兜底
//
// 月额是四舍五入出来的：100,000.00 / 3 = 33,333.333… 取 33,333.33，
// 三期加起来只有 99,999.99，差 1 分。**这 1 分必须在最后一期补上**，
// 否则应提总额永远提不完，固定资产清理时会挂着一个 1 分的余额，
// 而账面上看不出任何异常 —— 这种尾差能挂很多年。
func schedule(first, last period.Key, months int, total money.Money) []Installment {
	if months <= 0 || !first.Valid() || !last.Valid() || last.Before(first) {
		return nil
	}
	monthly := total
	if months > 0 {
		monthly = money.MulDiv(total, 1, int64(months))
	}
	out := make([]Installment, 0, months)
	var cum money.Money
	k := first
	for i := 1; i <= months; i++ {
		amt := monthly
		isLast := i == months
		if isLast {
			// 最后一期兜底：把前面四舍五入丢掉的尾差一次补齐。
			amt = total.Sub(cum)
		}
		cum = cum.Add(amt)
		out = append(out, Installment{
			Period: k, Seq: i, Amount: amt, Cumulative: cum, IsLast: isLast,
		})
		if k == last {
			// 处置早于预计年限：中间的期数作废，后面的不再排
			break
		}
		k = k.Next()
	}
	return out
}

// AmountFor 给出某个期间应计提的金额。
//
// already 是**截至该期间之前**已经计提的累计额（调用方从库里读）。
// 返回 0 表示本期不提，reason 说明为什么 —— 界面要把它显示出来，
// 否则用户只会看到「本期折旧 0.00」而不知道为什么。
func (a FixedAsset) AmountFor(k period.Key, already money.Money) (money.Money, string) {
	if a.Status == StatusDisposed && a.DisposedDate.Valid() {
		if period.NewKey(a.DisposedDate.Year, a.DisposedDate.Month).Before(k) {
			return 0, fmt.Sprintf("已于 %s 处置，本月起不再计提", a.DisposedDate)
		}
	}
	if !k.Valid() {
		return 0, "会计期间不合法"
	}
	if k.Before(a.FirstPeriod()) {
		return 0, fmt.Sprintf("尚未开始计提（投入使用 %s，次月起提）", a.StartDate)
	}
	if a.LastDepreciablePeriod().Before(k) {
		return 0, "已超过预计使用年限"
	}
	base := a.DepreciableBase()
	remain := base.Sub(already)
	if !remain.IsPositive() {
		return 0, "已提足，不再计提"
	}
	amt := a.MonthlyAmount()
	if k == a.LastDepreciablePeriod() || amt > remain {
		// 最后一期（或剩余不足一个月额）用剩余数兜底，消灭尾差。
		amt = remain
	}
	if amt > remain {
		amt = remain
	}
	return amt, ""
}

// AmountFor 给出待摊项目某个期间应摊销的金额。
func (m Amortization) AmountFor(k period.Key, already money.Money) (money.Money, string) {
	if m.Status == AmortVoided {
		return 0, "项目已作废"
	}
	if !k.Valid() {
		return 0, "会计期间不合法"
	}
	if k.Before(m.FirstPeriod()) {
		return 0, fmt.Sprintf("尚未开始摊销（受益期自 %s 起）", m.StartDate)
	}
	if m.LastPeriod().Before(k) {
		return 0, "已摊完"
	}
	remain := m.Total.Sub(already)
	if !remain.IsPositive() {
		return 0, "已摊完"
	}
	amt := m.MonthlyAmount()
	if k == m.LastPeriod() || amt > remain {
		amt = remain
	}
	if amt > remain {
		amt = remain
	}
	return amt, ""
}
