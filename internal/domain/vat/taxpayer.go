// Package vat 承载增值税的**纳税人身份、计税方法与税率政策**。
//
// # 这个包存在的第一个理由：两个概念被混为一谈
//
// 「小微企业」与「增值税小规模纳税人」是**两套完全不同的分类**，
// 依据不同、用途不同、判定标准也不同：
//
//	企业规模类型      所得税与统计概念
//	                  依据《中小企业划型标准规定》，看营业收入与从业人员，
//	                  决定能不能享受小型微利企业所得税优惠
//	增值税纳税人身份  流转税概念
//	                  依据《增值税法》，看年应征增值税销售额是否超过 500 万元，
//	                  决定按一般计税还是简易计税、能不能抵扣进项
//
// 两者**不存在推导关系**：
//
//   - 一家年销售额 300 万的公司，是小微企业，通常也是小规模纳税人；
//     但它**可以自愿登记为一般纳税人**（为了抵扣进项、为了给大客户开专票）。
//     此时它同时是「小微企业」和「一般纳税人」。
//   - 一家年销售额 800 万的公司，必须登记为一般纳税人，
//     但只要满足从业人数与资产总额条件，仍然可能是**小微企业**。
//
// 所以软件必须**分别保存**这两项，任何一处用「是不是小微」去推断
// 「能不能抵扣进项」，都会算错税。本工程把它们做成两个独立字段。
//
// # 第二个理由：税率是**带生效期的数据**，不是代码里的常量
//
// 2026 年的小规模纳税人 1% 优惠征收率有明确的截止日（2027-12-31），
// 房地产老项目 5% 简易计税、销售旧固定资产 3% 减按 2%、
// 个人出租住房 3% 减按 1.5% 都是**阶段性或特定业务**的规则。
// 把它们写死在代码里，等到政策到期或调整，用户只能等新版本 ——
// 而在此之前他会一直按错税率开票。
//
// 因此本包只提供**判定逻辑**，具体的税率、征收率、优惠幅度、
// 生效失效日期、政策依据全部来自可配置的政策表（见 RatePolicy）。
package vat

import (
	"fmt"
	"strings"

	"miniaccount/internal/domain/calendar"
)

// ---------------------------------------------------------------------------
// 企业规模类型（所得税与统计概念）
// ---------------------------------------------------------------------------

// EnterpriseScale 是企业规模类型。
//
// 依据《中小企业划型标准规定》（工信部联企业〔2011〕300号），
// 按行业看**营业收入**与**从业人员**划分。它决定的是企业所得税优惠
// （小型微利企业）等事项，**与增值税怎么算无关**。
type EnterpriseScale string

// 企业规模类型。
const (
	ScaleMicro  EnterpriseScale = "micro"  // 微型
	ScaleSmall  EnterpriseScale = "small"  // 小型
	ScaleMedium EnterpriseScale = "medium" // 中型
	ScaleLarge  EnterpriseScale = "large"  // 大型
)

// AllScales 列出全部规模类型。
func AllScales() []EnterpriseScale {
	return []EnterpriseScale{ScaleMicro, ScaleSmall, ScaleMedium, ScaleLarge}
}

// Valid 报告规模类型是否合法。
func (s EnterpriseScale) Valid() bool {
	for _, x := range AllScales() {
		if s == x {
			return true
		}
	}
	return false
}

// Label 返回中文名。
func (s EnterpriseScale) Label() string {
	switch s {
	case ScaleMicro:
		return "微型企业"
	case ScaleSmall:
		return "小型企业"
	case ScaleMedium:
		return "中型企业"
	case ScaleLarge:
		return "大型企业"
	default:
		return string(s)
	}
}

// IsSmallOrMicro 报告是否属于「小微企业」口径。
//
// ⚠️ 只有**所得税与统计**口径会用到它。增值税抵扣判定绝不能调这个函数 ——
// 小微企业完全可能是一般纳税人。
func (s EnterpriseScale) IsSmallOrMicro() bool {
	return s == ScaleMicro || s == ScaleSmall
}

// ---------------------------------------------------------------------------
// 增值税纳税人身份（流转税概念）
// ---------------------------------------------------------------------------

// VATStatus 是增值税纳税人身份。
type VATStatus string

// 纳税人身份。
const (
	// VATGeneral 一般纳税人：可以抵扣进项，按一般计税（特定业务可选简易计税）。
	VATGeneral VATStatus = "general"
	// VATSmallScale 小规模纳税人：不得抵扣进项，按征收率简易计税。
	//
	// ★ 它是**登记身份**，不是销售额的函数：
	// 年应征增值税销售额未超过 500 万元的，通常按小规模纳税人纳税，
	// 但**可以**登记为一般纳税人；超过 500 万元的，应当登记为一般纳税人。
	// 所以程序不按销售额自动推导身份，只存用户登记的结果。
	VATSmallScale VATStatus = "small_scale"
)

// AllVATStatuses 列出全部身份。
func AllVATStatuses() []VATStatus { return []VATStatus{VATGeneral, VATSmallScale} }

// Valid 报告身份是否合法。
func (s VATStatus) Valid() bool {
	return s == VATGeneral || s == VATSmallScale
}

// Label 返回中文名。
func (s VATStatus) Label() string {
	switch s {
	case VATGeneral:
		return "一般纳税人"
	case VATSmallScale:
		return "小规模纳税人"
	default:
		return string(s)
	}
}

// CanDeductInput 报告该身份**原则上**能否抵扣进项税额。
//
// 注意「原则上」：小规模一律不能抵扣（取得专用发票也不能，
// 应计入成本或资产价值）；一般纳税人还要看是否用于一般计税项目、
// 凭证是否合法、是否属于不得抵扣范围 —— 见 JudgeDeduction。
func (s VATStatus) CanDeductInput() bool { return s == VATGeneral }

// StatusPeriod 是一段纳税人身份的适用期间。
//
// 身份会变：小规模纳税人可以自愿登记为一般纳税人（通常自登记之日起），
// 一般纳税人转回小规模也有规定情形。跨期看账时必须按**业务发生日**
// 取当时有效的身份，而不是拿今天的身份去算去年的税。
type StatusPeriod struct {
	Status VATStatus
	// EffectiveFrom 是身份生效日（含）。
	EffectiveFrom calendar.Date
	// EffectiveTo 是身份失效日（含）；零值表示至今有效。
	EffectiveTo calendar.Date
	// Note 是变更说明（如「自愿登记为一般纳税人」）。
	Note string
}

// StatusOn 返回在指定日期生效的纳税人身份。
//
// 账套至少要有一条记录（建账时写入）。找不到时返回零值与 false ——
// 调用方必须显式处理「身份未设置」，而不是默认成一般纳税人：
// 默认成一般纳税人会**放行本不该抵扣的进项**。
func StatusOn(periods []StatusPeriod, on calendar.Date) (VATStatus, bool) {
	var (
		best    StatusPeriod
		hasBest bool
	)
	for _, p := range periods {
		if !p.EffectiveFrom.Valid() || on.Before(p.EffectiveFrom) {
			continue
		}
		if p.EffectiveTo.Valid() && on.After(p.EffectiveTo) {
			continue
		}
		// 命中多条时取生效日最晚的那条（期间不该重叠，见 ValidatePeriods）
		if !hasBest || p.EffectiveFrom.After(best.EffectiveFrom) {
			best, hasBest = p, true
		}
	}
	if !hasBest {
		return "", false
	}
	return best.Status, true
}

// ValidatePeriods 校验身份期间列表。
func ValidatePeriods(periods []StatusPeriod) error {
	if len(periods) == 0 {
		return fmt.Errorf("vat: 必须至少有一条纳税人身份记录")
	}
	for i, p := range periods {
		if !p.Status.Valid() {
			return fmt.Errorf("vat: 第 %d 条纳税人身份 %q 未知", i+1, p.Status)
		}
		if !p.EffectiveFrom.Valid() {
			return fmt.Errorf("vat: 第 %d 条纳税人身份缺少生效日期", i+1)
		}
		if p.EffectiveTo.Valid() && p.EffectiveTo.Before(p.EffectiveFrom) {
			return fmt.Errorf("vat: 第 %d 条纳税人身份的失效日早于生效日", i+1)
		}
	}
	for i := 0; i < len(periods); i++ {
		for j := i + 1; j < len(periods); j++ {
			if overlap(periods[i], periods[j]) {
				return fmt.Errorf("vat: 纳税人身份期间重叠（第 %d 条与第 %d 条）", i+1, j+1)
			}
		}
	}
	return nil
}

func overlap(a, b StatusPeriod) bool {
	aEnd := a.EffectiveTo
	bEnd := b.EffectiveTo
	// a 在 b 开始之前结束 → 不重叠
	if aEnd.Valid() && aEnd.Before(b.EffectiveFrom) {
		return false
	}
	if bEnd.Valid() && bEnd.Before(a.EffectiveFrom) {
		return false
	}
	return true
}

// NormalizeScale 把历史数据里的自由文本归一成规模类型。
//
// 老账套只有 tax_type（general|small）一个字段。迁移时**不能**
// 把小规模直接当成微型企业 —— 那是两个概念；但也确实无法反推。
// 因此迁移只保留纳税人身份，规模类型置空并提示用户补填，
// 由这里把空值显示成「未填写」而不是猜一个。
func NormalizeScale(s string) EnterpriseScale {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "micro", "微型", "微型企业":
		return ScaleMicro
	case "small", "小型", "小型企业":
		return ScaleSmall
	case "medium", "中型", "中型企业":
		return ScaleMedium
	case "large", "大型", "大型企业":
		return ScaleLarge
	default:
		return ""
	}
}
