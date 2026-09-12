package money

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 舍入
// ---------------------------------------------------------------------------

func TestDivIntRounding(t *testing.T) {
	// 100 分 / 3 应四舍五入为 33 分；200 分 / 3 = 66.67 → 67
	cases := []struct {
		m, n int64
		want Money
	}{
		{100, 3, 33},   // 33.33 → 33
		{200, 3, 67},   // 66.67 → 67
		{250, 2, 125},  // 125.00
		{1, 2, 1},      // 0.5 → 1（HALF_UP）
		{-1, 2, -1},    // -0.5 → -1（远离零，非银行家舍入）
		{-100, 3, -33}, // -33.33 → -33
		{0, 7, 0},
		{7, 1, 7},
	}
	for _, c := range cases {
		got, err := Money(c.m).DivInt(c.n)
		if err != nil {
			t.Fatalf("%d/%d 意外报错: %v", c.m, c.n, err)
		}
		if got != c.want {
			t.Errorf("%d/%d = %v，期望 %v", c.m, c.n, got, c.want)
		}
	}
}

func TestDivIntByZero(t *testing.T) {
	if _, err := Money(100).DivInt(0); err == nil {
		t.Fatal("除以零应当报错")
	}
}

func TestRateApply(t *testing.T) {
	thirteen := RatePercent(13)
	cases := []struct {
		name string
		rate Rate
		in   Money
		want Money
	}{
		// 113 元的 13% = 14.69 元 = 1469 分
		{"13% of 113.00", thirteen, 113 * Yuan, 1469},
		// 100 元的 13% = 13 元
		{"13% of 100.00", thirteen, 100 * Yuan, 13 * Yuan},
		// 0.01 元的 13% = 0.0013 → 0.00 元 = 0 分
		{"13% of 0.01", thirteen, 1, 0},
		// 0.05 元的 13% = 0.0065 → 0.01 元 = 1 分
		{"13% of 0.05", thirteen, 5, 1},
		// 6% 的 6 分 = 0.36 分 → 0 分
		{"6% of 0.06", RatePercent(6), 6, 0},
		// 负数金额：-100 元的 13% = -13 元
		{"13% of -100.00", thirteen, -100 * Yuan, -13 * Yuan},
	}
	for _, c := range cases {
		if got := c.rate.Apply(c.in); got != c.want {
			t.Errorf("%s: 得到 %v，期望 %v", c.name, got, c.want)
		}
	}
}

func TestRateConstruction(t *testing.T) {
	cases := []struct {
		name string
		got  Rate
		want Rate
	}{
		{"RatePercent(13)", RatePercent(13), 130_000},
		{"RateBp(1300)", RateBp(1300), 130_000},
		{"RatePermille(5)", RatePermille(5), 5_000},
		{"RatePercent(0)", RatePercent(0), 0},
		{"RatePercent(100)", RatePercent(100), 1_000_000},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d，期望 %d", c.name, c.got, c.want)
		}
	}
}

// 关键回归：13% 若用 float64 计算，在若干金额上会差 1 分。
// 用定点比率必须稳定。
func TestRateApplyIsExactOverRange(t *testing.T) {
	r := RatePercent(13)
	for cents := int64(0); cents <= 100_000; cents++ {
		got := r.Apply(Money(cents))
		// 手算参照：cents*13/100，HALF_UP
		num := cents * 13
		want := Money(num / 100)
		if (num%100)*2 >= 100 {
			want++
		}
		if got != want {
			t.Fatalf("%d 分的 13%% = %v，期望 %v", cents, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 分摊
// ---------------------------------------------------------------------------

func TestAllocateSumInvariant(t *testing.T) {
	// 分摊的核心不变式：各份之和必须恰好等于原金额（分毫不丢）
	amounts := []Money{1, 2, 3, 7, 99, 100, 101, 12345, -100, -1, 0, 999999}
	weightsList := [][]int64{
		{1, 1, 1},
		{1, 2, 3},
		{3, 3, 3, 3, 3, 3, 3},
		{1, 0, 0},
		{0, 0, 0},  // 全零 → 等分
		{-1, 2, 3}, // 含负数 → 等分
		{100, 200},
		{5},
	}
	for _, amt := range amounts {
		for _, w := range weightsList {
			got := amt.Allocate(w)
			if len(got) != len(w) {
				t.Fatalf("Allocate(%v, %v) 长度 %d，期望 %d", amt, w, len(got), len(w))
			}
			var sum Money
			for _, p := range got {
				sum += p
			}
			if sum != amt {
				t.Errorf("Allocate(%v, %v) = %v，合计 %v ≠ %v", amt, w, got, sum, amt)
			}
		}
	}
}

func TestAllocateSpecific(t *testing.T) {
	// 100 分按 1:1:1 → 34, 33, 33（余数给靠后的份）
	got := Money(100).Allocate([]int64{1, 1, 1})
	want := []Money{33, 33, 34}
	// 最大余数法 + 相同余数取靠后：100/3 → 每份 33，余 1 分给最后一份
	if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("100 按 1:1:1 分摊 = %v，期望 %v", got, want)
	}

	// 100 分按 1:2:1 → 25, 50, 25
	got = Money(100).Allocate([]int64{1, 2, 1})
	if got[0] != 25 || got[1] != 50 || got[2] != 25 {
		t.Errorf("100 按 1:2:1 = %v，期望 [25 50 25]", got)
	}

	// 负数分摊
	got = Money(-100).Allocate([]int64{1, 1, 1})
	var sum Money
	for _, p := range got {
		sum += p
	}
	if sum != -100 {
		t.Errorf("负数分摊合计 = %v，期望 -100", sum)
	}

	// 空权重
	if got := Money(100).Allocate(nil); len(got) != 0 {
		t.Errorf("空权重应返回空切片，得到 %v", got)
	}
}

// ---------------------------------------------------------------------------
// 解析与格式化
// ---------------------------------------------------------------------------

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Money
	}{
		{"1234.56", 123456},
		{"-1234.56", -123456},
		{"1,234.56", 123456},
		{"¥1234.56", 123456},
		{"￥1，234.56", 123456},
		{"+5", 500},
		{"5", 500},
		{"0", 0},
		{"0.00", 0},
		{".5", 50},
		{"1.", 100},
		{"  12.30  ", 1230},
		// 超过两位小数 → 四舍五入
		{"1.005", 101},
		{"1.004", 100},
		{"1.999", 200},
		{"-1.005", -101},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q) 报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"", "abc", "1.2.3", "1,2a", "--5", "¥"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) 应当报错", in)
		}
	}
}

func TestFormatRoundTrip(t *testing.T) {
	for _, m := range []Money{0, 1, -1, 99, 100, 123456, -123456, 999999999} {
		got, err := Parse(m.PlainString())
		if err != nil {
			t.Fatalf("Parse(%q) 报错: %v", m.PlainString(), err)
		}
		if got != m {
			t.Errorf("往返失败: %v → %q → %v", m, m.PlainString(), got)
		}
	}
}

func TestString(t *testing.T) {
	cases := []struct {
		in   Money
		want string
	}{
		{0, "0.00"},
		{123456, "1,234.56"},
		{-123456, "-1,234.56"},
		{100, "1.00"},
		{5, "0.05"},
		{-5, "-0.05"},
		{100000000, "1,000,000.00"},
		{999, "9.99"},
		{1000, "10.00"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("%d.String() = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 人民币大写
// ---------------------------------------------------------------------------

func TestChineseUpper(t *testing.T) {
	cases := []struct {
		in   Money
		want string
	}{
		{0, "零元整"},
		{100, "壹元整"},
		{200, "贰元整"},
		{1000, "壹拾元整"},
		{123456, "壹仟贰佰叁拾肆元伍角陆分"},
		{1000000, "壹万元整"},
		{100000000, "壹佰万元整"},
		{10000000000, "壹亿元整"},
		// 有分无角：需补「零」
		{10005, "壹佰元零伍分"},
		{100000001, "壹佰万元零壹分"},
		// 只有角 / 只有分（不足一元，不写「零元」）
		{50, "伍角整"},
		{5, "伍分"},
		// 到角为止
		{10050, "壹佰元伍角整"},
		// 组内连零
		{100500, "壹仟零伍元整"},
		{105000, "壹仟零伍拾元整"},
		{1000100, "壹万零壹元整"},
		// 跨组
		{12345678, "壹拾贰万叁仟肆佰伍拾陆元柒角捌分"},
		// 负数
		{-100, "负壹元整"},
	}
	for _, c := range cases {
		if got := c.in.ChineseUpper(); got != c.want {
			t.Errorf("%d.ChineseUpper() = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 大写金额里不允许出现阿拉伯数字或半角字符，这是票据的硬要求。
func TestChineseUpperHasNoASCII(t *testing.T) {
	for _, m := range []Money{0, 1, 10, 100, 12345, 99999999, 100000000} {
		s := m.ChineseUpper()
		for _, r := range s {
			if r < 0x4e00 {
				t.Errorf("%d.ChineseUpper() = %q 含非中文字符 %q", m, s, r)
				break
			}
		}
		if !strings.HasSuffix(s, "整") && !strings.HasSuffix(s, "分") {
			// 到元为止须写「整」，有分则不写；「角」结尾时本实现写「角整」
			t.Errorf("%d.ChineseUpper() = %q 结尾不符合规范", m, s)
		}
	}
}

// ---------------------------------------------------------------------------
// 边界
// ---------------------------------------------------------------------------

func TestLargeAmountsDoNotOverflow(t *testing.T) {
	// 100 亿元 × 13%，中间乘积约 1.3e13，远小于 int64 上限，不应溢出
	big := Money(100_0000_0000 * 100) // 100 亿元
	got := RatePercent(13).Apply(big)
	want := Money(13_0000_0000 * 100) // 13 亿元
	if got != want {
		t.Errorf("100 亿元的 13%% = %v，期望 %v", got, want)
	}
}

func TestSum(t *testing.T) {
	if got := Sum(); got != 0 {
		t.Errorf("Sum() = %v，期望 0", got)
	}
	if got := Sum(1, 2, 3); got != 6 {
		t.Errorf("Sum(1,2,3) = %v，期望 6", got)
	}
}
