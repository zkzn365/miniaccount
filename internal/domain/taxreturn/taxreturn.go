// Package taxreturn 生成税务计算表的**草稿**。
//
// # 为什么叫「计算表」而不是「申报表」
//
// 申报表是向税务机关提交的法律文书，它的口径由表样固定，
// 而且必须与申报系统的版本一致。这里的产物是**自己算给自己看的**：
// 账上这个月该交多少增值税、这季度预缴多少企业所得税、
// 这个月该给谁扣多少个税。
//
// 所以每一行的 `Source` 都写清「这个数从哪来」——
// 与审计底稿同一条精神：数字要有出处。会计要能拿着这张表
// 去申报系统里逐行核对，而不是照抄一份不知道从哪算出来的数。
//
// ★ 硬边界：`Return.Submittable` **永远是 false**。
//
// 这不是措辞问题。AI 与软件算出来的数字都不能直接用于正式申报 ——
// 政策有生效期、企业有特殊情况、申报表样会变，而漏报错报的
// 责任在企业与办税人身上。软件能给的是「算给你看 + 说清依据」。
package taxreturn

import (
	"fmt"
	"strings"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/payroll"
)

// Kind 是税种。
type Kind string

// 三种税。
const (
	// KindVAT 是增值税（含附加税费）。
	KindVAT Kind = "vat"
	// KindCIT 是企业所得税（季度预缴 / 年度汇算口径）。
	KindCIT Kind = "cit"
	// KindIIT 是个人所得税（工资薪金累计预扣预缴）。
	KindIIT Kind = "iit"
)

// AllKinds 列出全部税种。
var AllKinds = []Kind{KindVAT, KindCIT, KindIIT}

// Valid 报告税种是否已知。
func (k Kind) Valid() bool {
	for _, x := range AllKinds {
		if x == k {
			return true
		}
	}
	return false
}

// Label 返回中文名。
func (k Kind) Label() string {
	switch k {
	case KindVAT:
		return "增值税及附加"
	case KindCIT:
		return "企业所得税"
	case KindIIT:
		return "个人所得税（工资薪金）"
	default:
		return string(k)
	}
}

// Row 是计算表的一行。
type Row struct {
	// Line 是行次（对应申报表上的行号，方便对照）。
	Line string
	// Label 是项目名称。
	Label string
	// Amount 是金额。
	Amount money.Money
	// Source 是这个数**从哪来**（科目、报表行、上期数据…）。
	//
	// ★ 这一列是这张表的价值所在：没有它，会计只能选择相信
	// 或者不信，而两个数对不上时无从查起。
	Source string
	// Note 是补充说明（可不填）。
	Note string
	// Emphasis 为真表示这是小计 / 合计 / 应纳税额一类的关键行。
	Emphasis bool
}

// Key 是一条算出来的结论（如「本期应补增值税」）。
type Key struct {
	Label  string
	Amount money.Money
	Note   string
}

// Return 是一张税务计算表草稿。
type Return struct {
	Kind   Kind   `json:"kind"`
	Period string `json:"period"`
	Title  string `json:"title"`
	Rows   []Row  `json:"rows"`
	// Keys 是这张表算出来的几个关键数（界面顶部显示）。
	Keys []Key `json:"keys"`
	// Payable 是这张表算出来的「本期应补(退)」金额（可为负：多缴）。
	//
	// ★ 单独一个字段而不是让调用方去 Keys 里按标签找：
	// 申报台账要拿它与当时申报的数勾稽，靠字符串匹配迟早会
	// 因为改一个措辞而静默取到 0 —— 那时勾稽就变成了「永远一致」。
	Payable money.Money `json:"payable"`
	// Tax / Surcharge / Paid 是应补(退)税额的三个分量：
	//
	//	Payable = Tax + Surcharge − Paid
	//
	// ★ 三个分量必须由这里给全，不能让调用方去 Keys 里按标签猜。
	// 申报台账要按这个等式校验快照，靠字符串匹配取分量的话，
	// 改一个措辞就会静默取到 0 —— 那时校验变成「永远通过」。
	Tax       money.Money `json:"tax"`
	Surcharge money.Money `json:"surcharge"`
	// Paid 是本期已经缴过的部分（增值税的已交税金、所得税的已预缴）。
	//
	// 有它在，等式才成立：**应补(退) = 应纳 − 已缴**。
	// 少了这一项，「应纳 9,000 + 附加 1,080」与「应补 4,080」就对不上 ——
	// 而差的 6,000 正是这个月已经交掉的。
	Paid money.Money `json:"paid"`
	// Warnings 是算的过程中发现的问题（该报的都要报出来）。
	Warnings []string `json:"warnings"`
	// Submittable 永远为 false —— 见包注释。
	Submittable bool `json:"submittable"`
	// Draft 恒为 true：这是算给自己看的草稿，不是申报表。
	Draft bool `json:"draft"`
	// PolicyNote 是口径与政策说明（生效期、优惠档、免责）。
	PolicyNote string `json:"policyNote"`
	// Concludes 是一句话结论。
	Concludes string `json:"concludes"`
}

// Validate 检查一张表本身是否成形。
func (r Return) Validate() error {
	if !r.Kind.Valid() {
		return fmt.Errorf("%w: %q", ErrBadKind, r.Kind)
	}
	if strings.TrimSpace(r.Title) == "" {
		return fmt.Errorf("taxreturn: 缺少表名")
	}
	if strings.TrimSpace(r.Period) == "" {
		return fmt.Errorf("taxreturn: 缺少所属期间")
	}
	seen := map[string]bool{}
	for _, row := range r.Rows {
		if strings.TrimSpace(row.Label) == "" {
			return fmt.Errorf("taxreturn: 有一行没有项目名称")
		}
		if row.Line != "" {
			if seen[row.Line] {
				return fmt.Errorf("taxreturn: 行次 %s 重复 —— 申报表上同一行次只能出现一次", row.Line)
			}
			seen[row.Line] = true
		}
	}
	if r.Submittable {
		return fmt.Errorf("taxreturn: ★ 计算表不能被标记为「可直接申报」")
	}
	if !r.Draft {
		return fmt.Errorf("taxreturn: ★ 计算表只能是草稿")
	}
	return nil
}

// HasWarnings 报告有没有要注意的地方。
func (r Return) HasWarnings() bool { return len(r.Warnings) > 0 }

// row 造一行。
func row(line, label string, amount money.Money, source string) Row {
	return Row{Line: line, Label: label, Amount: amount, Source: source}
}

// sumRows 按行次把若干行的金额加起来。
func sumRows(rows []Row, lines ...string) money.Money {
	var s money.Money
	for _, r := range rows {
		for _, l := range lines {
			if r.Line == l {
				s = s.Add(r.Amount)
			}
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// 增值税及附加税费
// ---------------------------------------------------------------------------

// SurchargeRates 是附加税费的三档比例（百万分比）。
//
// ★ 它们是**政策数据**，不是常量：城建税按纳税人所在地分 7% / 5% / 1%，
// 教育费附加 3%，地方教育附加 2%。小规模纳税人还有减半征收的优惠。
// 所以做成可传入的比例，程序里不写死。
type SurchargeRates struct {
	// UrbanConstructionPPM 是城市维护建设税税率。
	UrbanConstructionPPM int64
	// EducationPPM 是教育费附加费率。
	EducationPPM int64
	// LocalEducationPPM 是地方教育附加费率。
	LocalEducationPPM int64
	// Halved 为真表示附加税费减半征收（小规模纳税人优惠）。
	Halved bool
}

// DefaultSurchargeRates 返回市区一般纳税人的默认附加税费比例。
func DefaultSurchargeRates() SurchargeRates {
	return SurchargeRates{
		UrbanConstructionPPM: 70_000, // 7%
		EducationPPM:         30_000, // 3%
		LocalEducationPPM:    20_000, // 2%
	}
}

// TotalPPM 返回附加税费合计比例（减半时折半）。
func (s SurchargeRates) TotalPPM() int64 {
	total := s.UrbanConstructionPPM + s.EducationPPM + s.LocalEducationPPM
	if s.Halved {
		return total / 2
	}
	return total
}

// MaxSurchargePPM 是单档附加税费比例的上限（10%）。
//
// ★ 这是**防呆**，不是政策：城建税按所在地分 7% / 5% / 1%，
// 教育费附加 3%，地方教育附加 2%，没有任何一档到得了 10%。
// 写成「比例是数据、谁都能填」而不设上限的话，
// 多敲一个零就会得出一张附加税费是增值税好几倍的表。
const MaxSurchargePPM int64 = 100_000

// Validate 检查比例是否合理。
func (s SurchargeRates) Validate() error {
	for name, v := range map[string]int64{
		"城市维护建设税": s.UrbanConstructionPPM,
		"教育费附加":   s.EducationPPM,
		"地方教育附加":  s.LocalEducationPPM,
	} {
		if v < 0 {
			return fmt.Errorf("taxreturn: %s的比例不能为负", name)
		}
		if v > MaxSurchargePPM {
			return fmt.Errorf("taxreturn: %s的比例 %s 不合常理（上限 10%%）—— 请检查是不是多敲了一个零",
				name, percentLabel(v))
		}
	}
	return nil
}

// VATInput 是算增值税需要的全部取数。
//
// 全部来自账套里「应交增值税」下的各专栏（222101xx），
// 取数在服务层做，这里只负责算。
type VATInput struct {
	// Period 是所属期间。
	Period string
	// SmallScale 为真表示小规模纳税人。
	SmallScale bool
	// OutputTax 是销项税额（贷方专栏）。
	OutputTax money.Money
	// OutputTaxDeducted 是销项税额抵减（借方专栏，减少销项）。
	OutputTaxDeducted money.Money
	// InputTax 是进项税额。
	InputTax money.Money
	// InputTaxTransferredOut 是进项税额转出（减少可抵进项）。
	InputTaxTransferredOut money.Money
	// ExportTaxRefund 是出口退税。
	ExportTaxRefund money.Money
	// ExportOffset 是出口抵减内销产品应纳税额。
	ExportOffset money.Money
	// TaxRelief 是减免税款。
	TaxRelief money.Money
	// Paid 是本期已交税金。
	Paid money.Money
	// PriorCredit 是上期期末留抵税额。
	PriorCredit money.Money
	// Surcharges 是附加税费比例。
	Surcharges SurchargeRates
}

// VAT 生成增值税及附加税费计算表。
func VAT(in VATInput) (*Return, error) {
	if err := in.Surcharges.Validate(); err != nil {
		return nil, err
	}
	output := in.OutputTax.Sub(in.OutputTaxDeducted)
	// 进项可用额 = 进项 − 转出
	input := in.InputTax.Sub(in.InputTaxTransferredOut)
	if input.IsNegative() {
		input = 0
	}
	available := input.Add(in.PriorCredit)
	// 实际抵扣不能超过销项
	used := available
	if used > output {
		used = output
	}
	if used.IsNegative() {
		used = 0
	}
	// 上期留抵 + 本期进项 大于销项时，剩下的转到下期（期末留抵）
	carry := available.Sub(used)
	if carry.IsNegative() {
		carry = 0
	}

	payable := output.Sub(used)
	// 减免税款与出口抵减内销都减少应纳税额
	payable = payable.Sub(in.TaxRelief).Sub(in.ExportOffset)
	if payable.IsNegative() {
		// ★ 减免大于应纳税额时**不产生退税**（除出口退税外）。
		// 多出来的部分不能记成负数应交，否则下期会凭空多一笔可抵。
		payable = 0
	}

	surcharge := money.MulDiv(payable, in.Surcharges.TotalPPM(), 1_000_000)
	total := payable.Add(surcharge)
	due := total.Sub(in.Paid)

	r := &Return{
		Kind: KindVAT, Draft: true, Period: in.Period, Title: "增值税及附加税费计算表",
		Rows: []Row{
			row("1", "按适用税率计税销售额（不含税）", money.Money(0),
				"主营业务收入等损益类科目本期贷方发生额（本表暂不自动取数）"),
			row("11", "销项税额", in.OutputTax, "22210102 应交增值税—销项税额 本期贷方发生额"),
			row("12", "销项税额抵减", in.OutputTaxDeducted, "22210118 应交增值税—销项税额抵减 本期借方发生额"),
			row("13", "销项税额合计（11−12）", output, "本表 11 − 12"),
			row("14", "进项税额", in.InputTax, "22210101 应交增值税—进项税额 本期借方发生额"),
			row("15", "进项税额转出", in.InputTaxTransferredOut, "22210108 应交增值税—进项税额转出 本期贷方发生额"),
			row("16", "可抵扣进项税额合计（14−15）", input, "本表 14 − 15"),
			row("17", "上期期末留抵税额", in.PriorCredit, "上期本表第 20 行「期末留抵税额」"),
			row("18", "实际抵扣税额", used, "min(本表 16 + 17, 本表 13)"),
			row("19", "应纳税额（13−18）", payable.Add(in.TaxRelief).Add(in.ExportOffset),
				"本表 13 − 18（减免与出口抵减前）"),
			row("20", "减免税款", in.TaxRelief, "22210106 应交增值税—减免税款 本期借方发生额"),
			row("21", "出口抵减内销产品应纳税额", in.ExportOffset,
				"22210109 应交增值税—出口抵减内销产品应纳税额"),
			row("22", "出口退税", in.ExportTaxRefund, "22210107 应交增值税—出口退税"),
		},
		Keys: []Key{
			{Label: "本期应纳税额（增值税）", Amount: payable,
				Note: "= 销项合计 − 实际抵扣 − 减免税款 − 出口抵减"},
			{Label: fmt.Sprintf("附加税费（合计 %s）",
				percentLabel(in.Surcharges.TotalPPM())), Amount: surcharge,
				Note: "以增值税应纳税额为基数；城建税 + 教育费附加 + 地方教育附加"},
			{Label: "本期应补(退)税额", Amount: due,
				Note: "= 应纳税额 + 附加税费 − 本期已交税金"},
			{Label: "期末留抵税额（结转下期）", Amount: carry,
				Note: "可抵进项大于销项时结转到下期继续抵扣"},
		},
		PolicyNote: "口径：一般计税方法。附加税费比例与减半优惠是**可配置的政策数据**，" +
			"请按纳税人所在地与实际身份核对；本表不替代申报表。",
	}
	// 「应纳税额」这一行要按最终数显示，前面那行是计算过程
	for i := range r.Rows {
		if r.Rows[i].Line == "19" {
			r.Rows[i].Amount = payable.Add(in.TaxRelief).Add(in.ExportOffset)
			r.Rows[i].Emphasis = true
		}
	}
	r.Rows = append(r.Rows,
		row("23", "城市维护建设税", money.MulDiv(payable, halved(in.Surcharges.UrbanConstructionPPM, in.Surcharges.Halved), 1_000_000),
			"本表应纳税额 × 城建税税率"),
		row("24", "教育费附加", money.MulDiv(payable, halved(in.Surcharges.EducationPPM, in.Surcharges.Halved), 1_000_000),
			"本表应纳税额 × 3%"),
		row("25", "地方教育附加", money.MulDiv(payable, halved(in.Surcharges.LocalEducationPPM, in.Surcharges.Halved), 1_000_000),
			"本表应纳税额 × 2%"),
		row("26", "附加税费合计（23+24+25）", surcharge, "本表 23 + 24 + 25"),
		row("27", "本期已交税金", in.Paid, "22210103 应交增值税—已交税金 本期借方发生额"),
		row("28", "本期应补(退)税额（19−20−21+26−27）", due,
			"本表 19 − 20 − 21 + 26 − 27（19 是减免与出口抵减**前**的应纳税额）"),
	)
	// ---- 该报的问题 ----
	if in.SmallScale {
		r.Warnings = append(r.Warnings,
			"账套身份是**小规模纳税人**：简易计税下进项税额不能抵扣。"+
				"若本表进项税额不为 0，请确认是不是把进项税记进了「应交增值税」——"+
				"小规模的进项税应当计入采购成本。")
	}
	if carry.IsPositive() {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"本期末有留抵税额 %s，结转到下期继续抵扣；本期不产生应纳增值税。", carry))
	}
	if due.IsNegative() {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"本期已交税金大于应纳税额 %s：多缴部分一般留抵下期，具体以申报系统为准。",
			due.Abs()))
	}
	if in.ExportTaxRefund.IsPositive() || in.ExportOffset.IsPositive() {
		r.Warnings = append(r.Warnings,
			"本表涉及出口退税 / 出口抵减内销：这两个专栏的取数与申报表需要人工复核。")
	}
	if payable.IsZero() && output.IsPositive() && input.IsZero() && in.PriorCredit.IsZero() {
		r.Warnings = append(r.Warnings,
			"有销项税额却没有可抵扣进项，且应纳税额为 0 —— 请确认取数是否完整。")
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	r.Submittable = false
	r.Draft = true
	r.Payable = due
	r.Tax = payable
	r.Surcharge = surcharge
	r.Paid = in.Paid
	r.Concludes = concludeVAT(payable, surcharge, due, carry)
	return r, nil
}

// concludeVAT 给出一句话结论。
func concludeVAT(payable, surcharge, due, carry money.Money) string {
	switch {
	case due.IsPositive():
		return fmt.Sprintf("本期应补增值税及附加 %s（其中增值税 %s）。"+
			"这是草稿：请到电子税务局按申报表逐行核对后再申报。", due, payable)
	case due.IsNegative():
		return fmt.Sprintf("本期多缴 %s，一般留抵下期；以申报系统为准。", due.Abs())
	case carry.IsPositive():
		return fmt.Sprintf("本期无应纳增值税，期末留抵 %s 结转下期。", carry)
	default:
		return "本期应纳增值税与附加为 0。"
	}
}

// halved 按减半优惠折算比例。
func halved(ppm int64, half bool) int64 {
	if half {
		return ppm / 2
	}
	return ppm
}

// percentLabel 把百万分比写成百分比文字。
func percentLabel(ppm int64) string {
	s := fmt.Sprintf("%.2f", float64(ppm)/10000)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s + "%"
}

// ---------------------------------------------------------------------------
// 企业所得税（季度预缴口径）
// ---------------------------------------------------------------------------

// 小型微利企业的判定标准（政策数据）。
//
// 现行政策：年度应纳税所得额不超过 300 万元、从业人数不超过 300 人、
// 资产总额不超过 5,000 万元，减按 25% 计入应纳税所得额、按 20% 税率缴纳，
// 实际税负 5%。**政策会变**，所以这里连口径一起写在表上。
const (
	// SmallLowProfitCap 是小型微利企业的应纳税所得额上限。
	SmallLowProfitCap = money.Money(3_000_000 * 100)
	// SmallLowProfitRatePPM 是小型微利企业的实际税负（5%）。
	SmallLowProfitRatePPM int64 = 50_000
	// StandardCITRatePPM 是一般企业的企业所得税税率（25%）。
	StandardCITRatePPM int64 = 250_000
)

// CITInput 是算企业所得税需要的取数。
type CITInput struct {
	Period string
	// Revenue / Cost 是营业收入与营业成本。
	Revenue money.Money
	Cost    money.Money
	// Profit 是利润总额。
	Profit money.Money
	// TaxAdjustIncrease / Decrease 是纳税调整增加 / 减少额。
	TaxAdjustIncrease money.Money
	TaxAdjustDecrease money.Money
	// LossOffset 是弥补以前年度亏损。
	LossOffset money.Money
	// Prepaid 是本年度已经预缴的所得税。
	Prepaid money.Money
	// SmallLowProfit 为真表示按小型微利企业口径计算。
	SmallLowProfit bool
	// RatePPM 是一般企业的适用税率；为 0 时用 25%。
	RatePPM int64
}

// CIT 生成企业所得税计算表。
func CIT(in CITInput) (*Return, error) {
	rate := in.RatePPM
	if rate <= 0 {
		rate = StandardCITRatePPM
	}
	taxable := in.Profit.Add(in.TaxAdjustIncrease).Sub(in.TaxAdjustDecrease).Sub(in.LossOffset)

	r := &Return{
		Kind: KindCIT, Draft: true, Period: in.Period,
		Title: "企业所得税计算表（季度预缴口径）",
		Rows: []Row{
			row("1", "营业收入", in.Revenue, "利润表「一、营业收入」行"),
			row("2", "营业成本", in.Cost, "利润表「二、营业成本」行"),
			row("3", "利润总额", in.Profit, "利润表「三、利润总额」行"),
			row("4", "纳税调整增加额", in.TaxAdjustIncrease,
				"手工填列（业务招待费超支、罚款滞纳金、超标准捐赠等）"),
			row("5", "纳税调整减少额", in.TaxAdjustDecrease,
				"手工填列（免税收入、加计扣除、政府补助不征税部分等）"),
			row("6", "弥补以前年度亏损", in.LossOffset, "手工填列（不超过税法规定的弥补年限）"),
		},
		PolicyNote: "口径：查账征收、按季预缴。小型微利企业判定与优惠比例是**政策数据**，" +
			"以申报时的最新政策为准；本表不替代申报表。",
	}
	taxableRow := row("7", "应纳税所得额（3+4−5−6）", taxable, "本表 3 + 4 − 5 − 6")
	taxableRow.Emphasis = true
	r.Rows = append(r.Rows, taxableRow)

	// 优惠判定
	effective := rate
	// 第 8 行只写**税率**，金额列留 0。
	//
	// 比例不是金额：原来把 ppm 当金额塞进去，表上会显示「500.00」，
	// 而它想说的是 5%。税率写进项目名称，金额列不参与计算。
	if in.SmallLowProfit && taxable <= SmallLowProfitCap {
		effective = SmallLowProfitRatePPM
		r.Rows = append(r.Rows, row("8", "适用税率 "+percentLabel(effective)+"（小型微利企业实际税负）",
			0, "政策：符合条件的小型微利企业，应纳税所得额不超过 300 万元的部分，"+
				"减按 25% 计入应纳税所得额、按 20% 税率缴纳（实际税负 5%）"))
	} else if in.SmallLowProfit {
		effective = rate
		r.Rows = append(r.Rows, row("8", "适用税率 "+percentLabel(effective)+
			"（已超过小微上限，全额按法定税率）", 0,
			"应纳税所得额超过 300 万元即不符合小型微利企业条件，全额按法定税率计算"))
	} else {
		r.Rows = append(r.Rows, row("8", "适用税率 "+percentLabel(rate)+"（法定税率）", 0,
			"法定税率 25%（或经认定的优惠税率）"))
	}

	// 应纳所得税额。
	//
	// ★ 300 万元是小型微利企业的**资格门槛**，不是分段线。
	//
	// 现行政策（财政部 税务总局公告 2023 年第 12 号，执行至 2027-12-31）：
	// 「小型微利企业」的定义本身就包含「年应纳税所得额不超过 300 万元」，
	// 符合条件时对**不超过 300 万元的部分**减按 25% 计入应纳税所得额、
	// 按 20% 税率缴纳（实际税负 5%）；一旦超过 300 万元，
	// 该企业**当年即不符合**小型微利企业条件，应全额按法定税率计算。
	//
	// 所以这里**不能分段**：500 万应纳税所得额分段算出 65 万，
	// 而正确的是 125 万 —— 少算 60 万，是会让企业被追缴的大错。
	if in.SmallLowProfit && taxable > SmallLowProfitCap {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"★ 应纳税所得额 %s 已超过小型微利企业上限 300 万元 —— "+
				"超过即**当年不符合**小型微利企业条件，本表已按法定税率 %s 全额计算。"+
				"若要按小微申报，请先核对三项判定标准（应纳税所得额、从业人数、资产总额）。",
			taxable, percentLabel(rate)))
		effective = rate
	}
	// ★ 亏损季度不产生「负的应纳所得税额」。
	//
	// taxable 为负时直接乘税率会得出 −25,000 这样的数，界面上会显示成
	// 「本期多缴 25,000」，而实际上只是亏损、本期不缴。
	tax := money.Money(0)
	if taxable.IsPositive() {
		tax = money.MulDiv(taxable, effective, 1_000_000)
	}
	taxRow := row("9", "应纳所得税额", tax, "本表 7 × 实际税负（小微分段计算）")
	taxRow.Emphasis = true
	r.Rows = append(r.Rows,
		taxRow,
		row("10", "减：本年已预缴所得税额", in.Prepaid, "本年度前几个季度已预缴的所得税合计"),
	)
	due := tax.Sub(in.Prepaid)
	dueRow := row("11", "本期应补(退)所得税额（9−10）", due, "本表 9 − 10")
	dueRow.Emphasis = true
	r.Rows = append(r.Rows, dueRow)

	r.Keys = []Key{
		{Label: "应纳税所得额", Amount: taxable, Note: "= 利润总额 + 纳税调整增加 − 减少 − 弥补亏损"},
		{Label: "应纳所得税额", Amount: tax,
			Note: fmt.Sprintf("适用税负 %s", percentLabel(effective))},
		{Label: "本期应补(退)所得税额", Amount: due, Note: "= 应纳 − 本年已预缴"},
	}

	if taxable.IsNegative() {
		r.Warnings = append(r.Warnings,
			"应纳税所得额为负（亏损）：本期**不产生**应纳所得税额（本表按 0 计），"+
				"亏损可结转以后年度弥补（一般 5 年）。")
	}
	if due.IsNegative() {
		r.Warnings = append(r.Warnings,
			"本年已预缴大于应纳所得税额：本期为多缴，一般可在后续季度抵缴或汇算清缴时申请退税。")
	}
	if in.TaxAdjustIncrease.IsZero() && in.TaxAdjustDecrease.IsZero() {
		r.Warnings = append(r.Warnings,
			"纳税调整额为 0：请确认业务招待费、广告费、罚款滞纳金、超标捐赠等"+
				"**税前扣除限额**项是否已经调整 —— 不调整会少缴税。")
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	r.Submittable = false
	r.Draft = true
	r.Payable = due
	r.Tax = tax
	r.Paid = in.Prepaid
	switch {
	case due.IsPositive():
		r.Concludes = fmt.Sprintf("本期应补企业所得税 %s。这是草稿："+
			"请核对纳税调整项后到电子税务局申报。", due)
	case due.IsNegative():
		r.Concludes = fmt.Sprintf("本期多缴企业所得税 %s，可在后续季度抵缴。", due.Abs())
	default:
		r.Concludes = "本期应补(退)企业所得税为 0。"
	}
	return r, nil
}

// ---------------------------------------------------------------------------
// 个人所得税（工资薪金累计预扣预缴）
// ---------------------------------------------------------------------------

// IITEmployee 是一位员工的累计数。
//
// 累计预扣预缴法要求用**年初到本月的累计数**算，不能只看当月 ——
// 这也是最容易做错的地方：只按当月算，前几个月会少扣，
// 年终奖一发就多扣，员工会觉得「工资算错了」。
//
// 字段与 payroll.YTD 一一对应，而且**必须带上 Months（任职月数）**：
// 减除费用是按任职月份数算 5,000 元/月的，不是按「出了几张工资单」。
// 传错这个数会静默地多扣个税。
type IITEmployee struct {
	// Name 是姓名。
	Name string
	// Code 是工号（可空）。
	Code string
	// Months 是本年度在本单位任职的月数（含本月）。
	Months int
	// Income 是累计收入。
	Income money.Money
	// TaxFreeIncome 是累计免税收入。
	TaxFreeIncome money.Money
	// SpecialDeduction 是累计专项扣除（三险一金个人部分）。
	SpecialDeduction money.Money
	// SpecialAdditional 是累计专项附加扣除。
	SpecialAdditional money.Money
	// OtherDeduction 是累计其他扣除（年金、商业健康险等）。
	OtherDeduction money.Money
	// TaxWithheld 是累计已预扣预缴税额。
	TaxWithheld money.Money
}

// YTD 把员工数据转成 payroll 的累计口径。
func (e IITEmployee) YTD() payroll.YTD {
	return payroll.YTD{
		Months: e.Months, Income: e.Income, TaxFreeIncome: e.TaxFreeIncome,
		SpecialDeduction: e.SpecialDeduction, SpecialAdditional: e.SpecialAdditional,
		OtherDeduction: e.OtherDeduction, TaxWithheld: e.TaxWithheld,
	}
}

// IITInput 是算个税扣缴表需要的取数。
type IITInput struct {
	Period string
	// Employees 是每位员工的累计数。
	Employees []IITEmployee
	// Table 是预扣率表（7 级）。
	Table payroll.TaxTable
}

// IITEmployeeResult 是一位员工算出来的结果。
type IITEmployeeResult struct {
	Name string
	Code string
	// Months 是任职月数。
	Months int
	// TaxableIncome 是累计应纳税所得额。
	TaxableIncome money.Money
	// CumulativeTax 是累计应纳税额。
	CumulativeTax money.Money
	// Withheld 是累计已预扣。
	Withheld money.Money
	// DueThisPeriod 是本期应预扣（按预扣预缴口径**不小于 0**）。
	DueThisPeriod money.Money
	// OverWithheld 为真表示累计已预扣已经超过累计应纳税额。
	//
	// ★ 预扣预缴环节**不退税**：多扣的部分在次年汇算清缴时退。
	// 所以本期应预扣是 0，而不是一个负数 —— 显示成负数会让人以为
	// 这个月要退钱，而实际上一分钱都不会退。
	OverWithheld bool
}

// IIT 生成个人所得税扣缴计算表。
func IIT(in IITInput) (*Return, error) {
	if err := in.Table.Validate(); err != nil {
		return nil, err
	}
	r := &Return{
		Kind: KindIIT, Draft: true, Period: in.Period,
		Title: "个人所得税扣缴计算表（工资薪金·累计预扣法）",
		Rows:  []Row{},
		PolicyNote: "口径：累计预扣预缴法 —— 本期应预扣 = （累计收入 − 累计免税收入 − " +
			"累计减除费用（5,000 元/月 × 任职月数）− 累计专项扣除 − 累计专项附加扣除 − " +
			"累计其他扣除）× 预扣率 − 速算扣除数 − 累计已预扣。" +
			"预扣率表与减除费用标准是**政策数据**，以申报时的最新政策为准；本表不替代扣缴申报表。",
	}
	var results []IITEmployeeResult
	var totalIncome, totalTaxable, totalTax, totalWithheld, totalDue money.Money
	for _, e := range in.Employees {
		ytd := e.YTD()
		taxable := ytd.TaxableIncome(in.Table)
		cumulative := in.Table.Tax(taxable)
		// ★ 本期应预扣用 payroll 里的那个函数算，不在这里重写一遍规则：
		// 两处各写一份，「预扣不小于 0」这条迟早会有一边漏掉。
		due := payroll.CumulativeTax(ytd, in.Table)
		results = append(results, IITEmployeeResult{
			Name: e.Name, Code: e.Code, Months: e.Months,
			TaxableIncome: taxable, CumulativeTax: cumulative,
			Withheld: e.TaxWithheld, DueThisPeriod: due,
			OverWithheld: cumulative < e.TaxWithheld,
		})
		totalIncome = totalIncome.Add(e.Income)
		totalTaxable = totalTaxable.Add(taxable)
		totalTax = totalTax.Add(cumulative)
		totalWithheld = totalWithheld.Add(e.TaxWithheld)
		totalDue = totalDue.Add(due)
	}
	for i, e := range results {
		r.Rows = append(r.Rows, Row{
			Line: fmt.Sprintf("%d", i+1),
			Label: fmt.Sprintf("%s（累计应纳税所得额 %s）",
				employeeLabel(e.Name, e.Code), e.TaxableIncome),
			Amount: e.DueThisPeriod,
			Source: fmt.Sprintf("工资模块累计口径：任职 %d 个月，累计应纳税额 %s，累计已预扣 %s",
				e.Months, e.CumulativeTax, e.Withheld),
			Note: dueNote(e),
		})
	}
	r.Rows = append(r.Rows,
		Row{Line: "合计1", Label: "累计收入合计", Amount: totalIncome,
			Source: "上列各员工累计收入之和"},
		Row{Line: "合计2", Label: "累计应纳税所得额合计", Amount: totalTaxable,
			Source: "上列各员工累计应纳税所得额之和"},
		Row{Line: "合计3", Label: "累计应纳税额合计", Amount: totalTax,
			Source: "上列各员工累计应纳税额之和", Emphasis: true},
		Row{Line: "合计4", Label: "累计已预扣税额合计", Amount: totalWithheld,
			Source: "工资模块历次计提的个税之和"},
		Row{Line: "合计5", Label: "本期应预扣预缴税额合计", Amount: totalDue,
			Source:   "各员工本期应预扣之和（累计应纳税额 − 累计已预扣，不小于 0）",
			Emphasis: true},
	)
	r.Keys = []Key{
		{Label: "本期应预扣预缴税额合计", Amount: totalDue,
			Note: fmt.Sprintf("涉及 %d 位员工", len(results))},
		{Label: "累计应纳税额合计", Amount: totalTax, Note: "年初至本月的累计口径"},
		{Label: "累计已预扣合计", Amount: totalWithheld, Note: "工资模块已计提的部分"},
	}

	for _, e := range results {
		if e.OverWithheld {
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"「%s」累计已预扣 %s，超过累计应纳税额 %s：本期不再预扣 —— "+
					"预扣预缴环节不退税，多扣的部分在**次年汇算清缴**时退。",
				e.Name, e.Withheld, e.CumulativeTax))
		}
		if e.Months <= 0 {
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"「%s」的任职月数为 0：减除费用算不出来，本期应预扣会偏大 —— "+
					"请核对入职日期。", e.Name))
		}
		if e.Months > 0 && e.Withheld.IsZero() && e.CumulativeTax.IsPositive() {
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"「%s」累计应纳税额 %s 但累计已预扣为 0：确认是本期第一次计提，"+
					"还是前几个月漏提了个税。", e.Name, e.CumulativeTax))
		}
	}
	if len(results) == 0 {
		r.Warnings = append(r.Warnings, "本期没有员工数据：请先在工资模块生成本期工资单。")
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	r.Submittable = false
	r.Draft = true
	r.Payable = totalDue
	// 个税：本期应预扣就是本期要申报缴纳的，没有「已缴」这一项
	r.Tax = totalDue
	switch {
	case len(results) == 0:
		r.Concludes = "本期没有可计算的员工，无法出具个税扣缴计算表。"
	case totalDue.IsPositive():
		r.Concludes = fmt.Sprintf("本期应预扣预缴个人所得税 %s（%d 人）。"+
			"这是草稿：请与自然人电子税务局扣缴端的计算结果核对。", totalDue, len(results))
	default:
		r.Concludes = "本期应预扣预缴个人所得税为 0。"
	}
	return r, nil
}

// employeeLabel 给出员工的显示名。
func employeeLabel(name, code string) string {
	if code == "" {
		return name
	}
	return fmt.Sprintf("%s（工号 %s）", name, code)
}

// dueNote 给出一行的说明。
func dueNote(e IITEmployeeResult) string {
	if e.OverWithheld {
		return "本期不再预扣：累计已预扣已超过累计应纳税额，多扣部分次年汇算清缴时退"
	}
	return ""
}

// PeriodLabel 把年月写成「2025 年 3 月」。
func PeriodLabel(year, month int) string {
	return fmt.Sprintf("%d 年 %d 月", year, month)
}

// QuarterOf 返回月份所属季度（1-4）。
func QuarterOf(month int) int {
	return (month-1)/3 + 1
}

// QuarterRange 返回某季度的起止月。
func QuarterRange(quarter int) (int, int, error) {
	if quarter < 1 || quarter > 4 {
		return 0, 0, fmt.Errorf("taxreturn: 季度 %d 非法", quarter)
	}
	start := (quarter-1)*3 + 1
	return start, start + 2, nil
}

// CITPeriodLabel 给出季度预缴的期间文字（如「2025 年第 2 季度」）。
func CITPeriodLabel(year, month int) string {
	return fmt.Sprintf("%d 年第 %d 季度", year, QuarterOf(month))
}

// DateInRange 报告某个日期是否落在「当年 1 月 1 日至该月末」。
//
// 累计口径的起点是**年初**，不是「本季度初」：
// 个税与企业所得税的预缴都是累计的，从季度初算起会系统性少缴。
func DateInRange(d calendar.Date, year, month int) bool {
	return d.Year == year && d.Month <= month
}

// ---------------------------------------------------------------------------
// 错误
// ---------------------------------------------------------------------------

// 税务计算表的错误。
var (
	ErrBadKind = fmt.Errorf("taxreturn: 不认识的税种")
)
