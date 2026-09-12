// Package money 提供记账用的精确金额类型。
//
// # 为什么不用 float64
//
// 会计核算是精确计算：0.1 + 0.2 在 IEEE-754 下等于 0.30000000000000004，
// 累加几千笔分录后误差会变成可见的余额错误。
// 本项目所有金额一律以「分」为单位的 int64 表示，全链路不出现浮点。
//
// # 舍入约定
//
// 统一使用四舍五入（HALF_UP，即恰好 0.5 时进位），符合中国会计惯例。
// 注意 Go 的 math.Round 是「四舍五入远离零」，对负数等同于 HALF_UP，
// 本包与之保持一致：-0.5 分舍入为 -1 分。
package money

import (
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
)

// Money 是以「分」为单位的金额。1 元 = 100。
//
// int64 的取值范围约 ±9.22e16 分（约 ±922 万亿元），
// 对小企业的账务规模绰绰有余。
type Money int64

const (
	// Zero 是零金额。
	Zero Money = 0
	// Yuan 是一元。
	Yuan Money = 100
)

// 常见错误。
var (
	ErrParse     = errors.New("money: 无法解析金额")
	ErrDivideBy0 = errors.New("money: 除数为零")
)

// ---------------------------------------------------------------------------
// 基本运算
// ---------------------------------------------------------------------------

// Add 返回 m+o。
func (m Money) Add(o Money) Money { return m + o }

// Sub 返回 m-o。
func (m Money) Sub(o Money) Money { return m - o }

// Neg 返回 -m。
func (m Money) Neg() Money { return -m }

// Abs 返回 |m|。
func (m Money) Abs() Money {
	if m < 0 {
		return -m
	}
	return m
}

// Sign 返回 -1、0 或 1。
func (m Money) Sign() int {
	switch {
	case m < 0:
		return -1
	case m > 0:
		return 1
	default:
		return 0
	}
}

// IsZero 报告 m 是否为零。
func (m Money) IsZero() bool { return m == 0 }

// IsPositive 报告 m 是否大于零。
func (m Money) IsPositive() bool { return m > 0 }

// IsNegative 报告 m 是否小于零。
func (m Money) IsNegative() bool { return m < 0 }

// MulInt 返回 m*n（不产生舍入）。溢出时结果未定义，调用方需自行保证量级。
func (m Money) MulInt(n int64) Money { return Money(int64(m) * n) }

// DivInt 返回 m/n，四舍五入到分。n 为零时返回 ErrDivideBy0。
func (m Money) DivInt(n int64) (Money, error) {
	if n == 0 {
		return 0, ErrDivideBy0
	}
	return divRoundHalfUp(int64(m), n), nil
}

// MulDiv 计算 a*b/div，四舍五入到分。
//
// ★ 为什么不是 a.MulInt(b).DivInt(div)：那两步的中间结果会溢出。
// 会计场景里「金额 × 百万分比 ÷ 1000000」到处都是（残值率、税率、
// 摊销比例），而金额上限 9e16 分乘上 1e6 早就出 int64 了。
// 这里用 128 位中间结果，一次算完。
func MulDiv(a Money, b, div int64) Money {
	return mulDivRoundHalfUp(int64(a), b, div)
}

// Sum 累加一组金额，空切片返回 Zero。
func Sum(ms ...Money) Money {
	var s Money
	for _, m := range ms {
		s += m
	}
	return s
}

// ---------------------------------------------------------------------------
// 舍入核心
// ---------------------------------------------------------------------------

// divRoundHalfUp 计算 num/den 并四舍五入（HALF_UP，远离零）。
// 用「先整除再比较余数」的方式，避免 num+den/2 造成的溢出。
func divRoundHalfUp(num, den int64) Money {
	if den == 0 {
		return 0
	}
	neg := (num < 0) != (den < 0)
	un, ud := uint64(abs64(num)), uint64(abs64(den))
	q, r := un/ud, un%ud
	if r*2 >= ud {
		q++
	}
	if neg {
		return Money(-int64(q))
	}
	return Money(int64(q))
}

// mulDivRoundHalfUp 计算 a*b/div 并四舍五入。
// 用 math/bits 做 64×64→128 位乘法后再除以 64 位，避免中间溢出。
func mulDivRoundHalfUp(a, b, div int64) Money {
	if div == 0 {
		return 0
	}
	neg := (a < 0) != (b < 0)
	ua, ub := uint64(abs64(a)), uint64(abs64(b))
	ud := uint64(abs64(div))

	hi, lo := bits.Mul64(ua, ub)
	var q uint64
	if hi >= ud {
		// 商超出 64 位：仅当金额与比率都逼近 int64 上限时可能发生，
		// 会计场景不可达。此处饱和处理，避免 panic。
		q = ^uint64(0)
	} else {
		var r uint64
		q, r = bits.Div64(hi, lo, ud)
		if r*2 >= ud {
			q++
		}
	}
	if neg {
		return Money(-int64(q))
	}
	return Money(int64(q))
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// ---------------------------------------------------------------------------
// 比率
// ---------------------------------------------------------------------------

// RateScale 是比率的定点标度：Rate 以百万分之一（ppm）为单位。
// 例如 13% = 130000，0.5% = 5000，13.5% = 135000。
const RateScale int64 = 1_000_000

// Rate 是定点比率，单位为百万分之一。
//
// 之所以不用 float64：13% 无法用二进制浮点精确表示，
// 用它乘金额会在舍入边界上产生 ±1 分的随机偏差。
type Rate int64

// RatePercent 返回整数百分数对应的 Rate。RatePercent(13) 表示 13%。
func RatePercent(p int64) Rate { return Rate(p * RateScale / 100) }

// RateBp 返回基点数（万分之一）对应的 Rate。RateBp(1300) 表示 13.00%。
func RateBp(bp int64) Rate { return Rate(bp * RateScale / 10_000) }

// RatePermille 返回千分数对应的 Rate。RatePermille(5) 表示 5‰。
func RatePermille(pm int64) Rate { return Rate(pm * RateScale / 1000) }

// RateFromFraction 返回 num/den 对应的 Rate，用于表示如 1/3 这类除不尽的比率。
func RateFromFraction(num, den int64) Rate {
	if den == 0 {
		return 0
	}
	return Rate(divRoundHalfUp(num*RateScale, den))
}

// Apply 返回 m 按比率 r 计算的结果，四舍五入到分。
//
//	r := money.RatePercent(13)
//	r.Apply(100 * money.Yuan) // 13 元
func (r Rate) Apply(m Money) Money { return mulDivRoundHalfUp(int64(m), int64(r), RateScale) }

// Add 返回两个比率之和，例如把多个税种的税率相加。
func (r Rate) Add(o Rate) Rate { return r + o }

// ApplyInverse 返回按比率**反算**的结果：m / (1 + r)，四舍五入到分。
//
// 用途：从价税合计反推不含税金额。
//
//	不含税金额 = 价税合计 / (1 + 税率)
//
// 这是发票录入的常见场景 —— 拿到发票时先看到的是价税合计，
// 需要拆出不含税金额与税额。
func (r Rate) ApplyInverse(m Money) Money {
	denom := RateScale + int64(r)
	if denom == 0 {
		return m
	}
	return mulDivRoundHalfUp(int64(m), RateScale, denom)
}

// Mul 返回两个比率之积，用于「先折扣后计税」这类复合场景。
func (r Rate) Mul(o Rate) Rate { return Rate(mulDivRoundHalfUp(int64(r), int64(o), RateScale)) }

// Float 返回比率的浮点近似，仅用于展示与日志，禁止参与金额计算。
func (r Rate) Float() float64 { return float64(r) / float64(RateScale) }

// String 返回如 "13%" 的展示形式（自动去掉多余的零）。
func (r Rate) String() string {
	return strconv.FormatFloat(r.Float()*100, 'f', -1, 64) + "%"
}

// ---------------------------------------------------------------------------
// 分摊
// ---------------------------------------------------------------------------

// Allocate 按权重把 m 拆成 len(weights) 份，保证各份之和**恰好**等于 m。
//
// 因整除产生的余数（若干分）按「最大余数法」分配；
// 余数相同时优先给索引靠后的份，与会计上「尾差计入最后一笔」的习惯一致。
//
// 权重含负数或全为零时退化为等分。weights 为空时返回空切片。
//
//	// 把 100 分按 1:1:1 分摊 → 34, 33, 33
//	money.Money(100).Allocate([]int64{1, 1, 1})
func (m Money) Allocate(weights []int64) []Money {
	n := len(weights)
	out := make([]Money, n)
	if n == 0 {
		return out
	}

	var total int64
	usable := true
	for _, w := range weights {
		if w < 0 {
			usable = false
			break
		}
		total += w
	}

	if !usable || total == 0 {
		// 等分：先取整，余数全部给最后一份以保证合计不变
		each, _ := m.DivInt(int64(n))
		for i := 0; i < n-1; i++ {
			out[i] = each
		}
		out[n-1] = m - each*Money(n-1)
		return out
	}

	neg := m < 0
	am := uint64(m.Abs())

	rems := make([]uint64, n)
	var sum uint64
	for i, w := range weights {
		// floor(am*w/total)：用 128 位乘法避免 am*w 溢出
		hi, lo := bits.Mul64(am, uint64(w))
		if hi >= uint64(total) {
			// 量级不可达，饱和保护
			out[i] = Money(0)
			continue
		}
		qq, rr := bits.Div64(hi, lo, uint64(total))
		out[i] = Money(qq)
		rems[i] = rr
		sum += qq
	}

	leftover := am - sum
	for leftover > 0 {
		best := -1
		for i := 0; i < n; i++ {
			if rems[i] == 0 {
				continue
			}
			if best == -1 || rems[i] >= rems[best] {
				best = i
			}
		}
		if best == -1 {
			best = n - 1
		}
		out[best]++
		rems[best] = 0
		leftover--
	}

	if neg {
		for i := range out {
			out[i] = -out[i]
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 格式化
// ---------------------------------------------------------------------------

// PlainString 返回不带千分位的金额字符串，如 "1234.56"、"-0.05"。
// 这是写数据库与传 API 的规范形式。
func (m Money) PlainString() string {
	neg := m < 0
	v := uint64(m.Abs())
	s := fmt.Sprintf("%d.%02d", v/100, v%100)
	if neg {
		return "-" + s
	}
	return s
}

// String 返回带千分位的展示字符串，如 "1,234.56"、"-0.05"。
// 实现 fmt.Stringer，因此 fmt.Println(m) 会输出可读形式。
func (m Money) String() string {
	s := m.PlainString()
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i:]
	}
	var b strings.Builder
	for i := 0; i < len(intPart); i++ {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(intPart[i])
	}
	out := b.String() + frac
	if neg {
		return "-" + out
	}
	return out
}

// Float 返回以「元」为单位的 float64。
//
// ⚠️ **禁止用于任何会计计算。** 它只用于必须输出浮点的外部接口 ——
// 例如写入 Excel 单元格（Excel 只认数值，且会计需要在 Excel 里直接求和）。
// 一旦把 float64 引回计算链路，0.1+0.2 这类误差就会重新出现，
// 本项目从设计上杜绝这一点。
func (m Money) Float() float64 {
	return float64(m) / 100
}

// Parse 解析金额字符串。
//
// 接受："1234.56"、"-1234.56"、"1,234.56"、"¥1234.56"、"￥1，234.56"、"+5"。
// 超过两位小数时按四舍五入处理（"1.005" → 1.01）。
func Parse(s string) (Money, error) {
	orig := s
	s = strings.TrimSpace(s)
	s = strings.NewReplacer(",", "", "，", "", "¥", "", "￥", "", " ", "", "\u00a0", "").Replace(s)
	if s == "" {
		return 0, fmt.Errorf("%w: %q", ErrParse, orig)
	}

	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}
	if s == "" {
		return 0, fmt.Errorf("%w: %q", ErrParse, orig)
	}

	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	if intPart == "" {
		intPart = "0"
	}
	if !allDigits(intPart) || !allDigits(fracPart) {
		return 0, fmt.Errorf("%w: %q", ErrParse, orig)
	}

	yuan, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil || yuan > (1<<62)/100 {
		return 0, fmt.Errorf("%w: %q", ErrParse, orig)
	}

	var cents int64
	switch {
	case len(fracPart) == 0:
		cents = 0
	case len(fracPart) == 1:
		cents = int64(fracPart[0]-'0') * 10
	default:
		cents = int64(fracPart[0]-'0')*10 + int64(fracPart[1]-'0')
		if len(fracPart) > 2 && fracPart[2] >= '5' {
			cents++
			if cents == 100 {
				cents, yuan = 0, yuan+1
			}
		}
	}

	v := Money(yuan*100 + cents)
	if neg {
		return -v, nil
	}
	return v, nil
}

// MustParse 与 Parse 相同，但解析失败时 panic。仅用于测试与常量初始化。
func MustParse(s string) Money {
	m, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return m
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 人民币大写
// ---------------------------------------------------------------------------

var (
	cnDigits = [...]string{"零", "壹", "贰", "叁", "肆", "伍", "陆", "柒", "捌", "玖"}
	cnUnits  = [...]string{"", "拾", "佰", "仟"}
	cnGroups = [...]string{"", "万", "亿", "万亿", "亿亿"}
)

// ChineseUpper 返回人民币大写金额，符合《正确填写票据和结算凭证的基本规定》
// （中国人民银行）的写法：
//
//	0            → 零元整
//	100          → 壹元整
//	123456       → 壹仟贰佰叁拾肆元伍角陆分
//	1000000      → 壹万元整
//	50           → 伍角整
//	5            → 伍分
//	10005        → 壹佰元零伍分
//	100000001    → 壹佰万元零壹分
//
// 负数前缀「负」。
func (m Money) ChineseUpper() string {
	if m == 0 {
		return "零元整"
	}
	neg := m < 0
	v := uint64(m.Abs())
	yuan := v / 100
	cents := v % 100

	var b strings.Builder
	if neg {
		b.WriteString("负")
	}

	if yuan > 0 {
		b.WriteString(upperInteger(yuan))
		b.WriteString("元")
	}

	jiao, fen := cents/10, cents%10
	switch {
	case jiao == 0 && fen == 0:
		b.WriteString("整")
	case fen == 0:
		// 到「角」为止，规定允许写「整」
		b.WriteString(cnDigits[jiao])
		b.WriteString("角整")
	case jiao == 0:
		// 有分无角：元与分之间需补「零」，如 壹佰元零伍分
		if yuan > 0 {
			b.WriteString(cnDigits[0])
		}
		b.WriteString(cnDigits[fen])
		b.WriteString("分")
	default:
		b.WriteString(cnDigits[jiao])
		b.WriteString("角")
		b.WriteString(cnDigits[fen])
		b.WriteString("分")
	}
	return b.String()
}

// upperInteger 转换整数部分（单位：元）。
func upperInteger(n uint64) string {
	if n == 0 {
		return cnDigits[0]
	}
	var groups []uint64
	for n > 0 {
		groups = append(groups, n%10000)
		n /= 10000
	}

	var b strings.Builder
	needZero := false
	for i := len(groups) - 1; i >= 0; i-- {
		g := groups[i]
		if g == 0 {
			if b.Len() > 0 {
				needZero = true
			}
			continue
		}
		// 高位组已写过，且本组不足四位（或中间有整组为零）时补「零」
		if b.Len() > 0 && (needZero || g < 1000) {
			b.WriteString(cnDigits[0])
		}
		needZero = false
		b.WriteString(upperGroup(g))
		if i < len(cnGroups) {
			b.WriteString(cnGroups[i])
		}
	}
	return b.String()
}

// upperGroup 转换 1..9999 的四位组，处理组内连零。
func upperGroup(g uint64) string {
	var b strings.Builder
	zero := false
	places := [...]uint64{1000, 100, 10, 1}
	for i, p := range places {
		d := g / p % 10
		if d == 0 {
			zero = true
			continue
		}
		if zero && b.Len() > 0 {
			b.WriteString(cnDigits[0])
		}
		zero = false
		b.WriteString(cnDigits[d])
		b.WriteString(cnUnits[3-i])
	}
	return b.String()
}
