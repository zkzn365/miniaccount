// Package auditdoc 生成审计与鉴证类文书的**草稿**。
//
// 三种：审计报告、验资报告、管理建议书。
//
// # 为什么叫草稿
//
// 这三份东西都是要签字盖章对外出的。软件能做的是把**事实部分**
// 摆整齐：审了什么、查了哪些、账上是多少、底稿里有哪些没补齐；
// 而不能替注册会计师形成意见 —— 意见是执业判断，署名的人要负责。
//
// 所以这一层遵循两条硬规则：
//
//  1. `Doc.Draft` 恒为 true、`Doc.Submittable` 恒为 false；
//  2. 报告里**出现的每一个数都能追到账套或底稿**（每个段落带 Source）。
//
// # 一条真正的质量护栏
//
// 若未更正错报合计已经达到整体重要性，而意见类型还是「无保留」，
// 这不是可以静默通过的组合。软件在这里**拒绝生成一份体面的报告**：
// 它把矛盾写进 `Missing`，让签字的人先回答「为什么这样还能是无保留」。
// 别的地方都在帮用户把材料做得好看，这一处必须反过来。
package auditdoc

import (
	"fmt"
	"strings"

	"miniaccount/internal/domain/money"
)

// Kind 是文书的种类。
type Kind string

// 三种文书。
const (
	// KindAudit 是审计报告。
	KindAudit Kind = "audit"
	// KindCapital 是验资报告。
	KindCapital Kind = "capital"
	// KindManagement 是管理建议书。
	KindManagement Kind = "management"
)

// AllKinds 列出全部文书。
var AllKinds = []Kind{KindAudit, KindCapital, KindManagement}

// Valid 报告种类是否已知。
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
	case KindAudit:
		return "审计报告"
	case KindCapital:
		return "验资报告"
	case KindManagement:
		return "管理建议书"
	default:
		return string(k)
	}
}

// Opinion 是审计意见类型。
type Opinion string

// 意见类型：四种意见，外加「还没选」。
const (
	// OpinionUnspecified 表示注册会计师**还没有选择**意见类型。
	//
	// ★ 它不是第五种意见，而是「未填」：软件不替签字人形成意见。
	// 未选时正文写「意见类型待注册会计师选择」，并进待补事项。
	OpinionUnspecified Opinion = ""
	// OpinionUnqualified 是无保留意见。
	OpinionUnqualified Opinion = "unqualified"
	// OpinionQualified 是保留意见。
	OpinionQualified Opinion = "qualified"
	// OpinionAdverse 是否定意见。
	OpinionAdverse Opinion = "adverse"
	// OpinionDisclaimer 是无法表示意见。
	OpinionDisclaimer Opinion = "disclaimer"
)

// AllOpinions 列出四种意见。
var AllOpinions = []Opinion{
	OpinionUnqualified, OpinionQualified, OpinionAdverse, OpinionDisclaimer,
}

// Valid 报告意见类型是否已知。
//
// ★ 「未选」（空串）也算合法状态 —— 草稿可以还没选，但它不等于任何一种意见。
func (o Opinion) Valid() bool {
	if o == OpinionUnspecified {
		return true
	}
	for _, x := range AllOpinions {
		if x == o {
			return true
		}
	}
	return false
}

// Chosen 报告注册会计师是否已经选了意见类型。
//
// 用它而不是 `!= ""`：把「未选」的判断收在一个地方，
// 将来加意见类型时不会漏。
func (o Opinion) Chosen() bool { return o != OpinionUnspecified && o.Valid() }

// Label 返回中文名。
func (o Opinion) Label() string {
	switch o {
	case OpinionUnqualified:
		return "无保留意见"
	case OpinionQualified:
		return "保留意见"
	case OpinionAdverse:
		return "否定意见"
	case OpinionDisclaimer:
		return "无法表示意见"
	case OpinionUnspecified:
		return "未选择"
	default:
		return string(o)
	}
}

// NeedsBasis 报告这种意见是否必须写「形成……的基础」一段。
//
// ★ 除无保留意见外都必须写。这不是格式要求：
// 非无保留意见的**理由**就是报告的核心内容，
// 没有理由的意见书在执业检查里等同于没有意见。
func (o Opinion) NeedsBasis() bool { return o != OpinionUnqualified && o.Valid() }

// Section 是文书的一段。
type Section struct {
	// No 是段号（「一」「二」…）。
	No string
	// Title 是段标题。
	Title string
	// Body 是正文。
	Body string
	// Source 是这一段的事实从哪来（账套 / 底稿 / 用户填写）。
	Source string
}

// InputField 是一份文书需要用户提供的项。
type InputField struct {
	Key   string
	Label string
	// Hint 说明这一项为什么必须由人来填。
	Hint string
	// Value 是当前值（可能来自账套）。
	Value string
	// FromBook 为真表示已由账套自动带出。
	FromBook bool
}

// Doc 是一份文书草稿。
type Doc struct {
	Kind    Kind
	Title   string
	Company string
	Period  string
	// Opinion 仅审计报告有。
	Opinion    Opinion
	OpinionStr string
	Sections   []Section
	// Inputs 是还需要用户填的项（签字人、报告号、日期…）。
	Inputs []InputField
	// Missing 是出这份报告前必须补齐的东西。
	//
	// ★ 非空时这份稿子**不能签发**。界面上要按「待办」显示，
	// 而不是当作一份可以打印的报告。
	Missing []string
	// Draft 恒为 true：软件产出的是草稿，签字盖章才生效。
	Draft bool
	// Submittable 恒为 false。
	Submittable bool
	// Signature 是生效条件（签字盖章要求）。
	Signature string
	// PolicyNote 是执业依据与免责说明。
	PolicyNote string
	// Concludes 是一句话结论。
	Concludes string
}

// Validate 检查一份文书草稿是否成形。
func (d Doc) Validate() error {
	if !d.Kind.Valid() {
		return fmt.Errorf("%w: %q", ErrBadKind, d.Kind)
	}
	if strings.TrimSpace(d.Title) == "" {
		return fmt.Errorf("auditdoc: 缺少文书名称")
	}
	if strings.TrimSpace(d.Company) == "" {
		return fmt.Errorf("auditdoc: 缺少被审计单位名称")
	}
	if len(d.Sections) == 0 {
		return fmt.Errorf("auditdoc: 文书没有任何段落")
	}
	seen := map[string]bool{}
	for _, s := range d.Sections {
		if strings.TrimSpace(s.Title) == "" {
			return fmt.Errorf("auditdoc: 有一段没有标题")
		}
		if strings.TrimSpace(s.Body) == "" {
			return fmt.Errorf("auditdoc: 「%s」这一段是空的", s.Title)
		}
		if seen[s.Title] {
			return fmt.Errorf("auditdoc: 「%s」这一段重复了", s.Title)
		}
		seen[s.Title] = true
	}
	if d.Kind == KindAudit {
		if !d.Opinion.Valid() {
			return fmt.Errorf("%w: %q", ErrBadOpinion, d.Opinion)
		}
		if d.Opinion == OpinionUnspecified {
			// 「还没选意见」是合法的草稿状态，但仍要有基础段
			if !seen["形成审计意见的基础"] {
				return fmt.Errorf("auditdoc: 还没有选意见类型时，" +
					"仍要有「形成审计意见的基础」一段")
			}
			return nil
		}
		if d.Opinion.NeedsBasis() && !seen["形成"+d.Opinion.Label()+"的基础"] {
			return fmt.Errorf("auditdoc: 出具%s却没有「形成%s的基础」一段 —— "+
				"非无保留意见的理由就是报告的核心", d.Opinion.Label(), d.Opinion.Label())
		}
	}
	// ★ 这两条是硬规则，不是默认值
	if !d.Draft {
		return fmt.Errorf("auditdoc: 软件产出的文书只能是草稿")
	}
	if d.Submittable {
		return fmt.Errorf("auditdoc: 文书不能被标记为「可直接出具」—— 须由注册会计师签字盖章")
	}
	return nil
}

// CanIssue 报告这份稿子能不能签发。
//
// ★ 只有「没有待补事项」的稿子才能进入签发流程。
func (d Doc) CanIssue() bool { return len(d.Missing) == 0 }

// Conclude 给出一句话结论（含能否签发的判断）。
func (d Doc) Conclude() string {
	if !d.CanIssue() {
		return fmt.Sprintf("这份%s还不能签发：还有 %d 项要补齐（见「待补事项」）。",
			d.Kind.Label(), len(d.Missing))
	}
	return fmt.Sprintf("%s草稿已成形，待签字盖章后生效。", d.Kind.Label())
}

// defaultSignature 返回生效条件。
func defaultSignature(kind Kind) string {
	switch kind {
	case KindAudit, KindCapital:
		return "本报告须由两名注册会计师签名盖章、会计师事务所盖章后生效；" +
			"软件生成的是草稿，不具备证明效力。"
	default:
		return "本建议书须由项目负责人签字、会计师事务所盖章后送出。"
	}
}

// defaultPolicyNote 返回执业依据与免责说明。
func defaultPolicyNote(kind Kind) string {
	base := "文书中的数字均可追到账套或审计底稿（每段标注来源）；" +
		"意见与措辞由签字注册会计师负责。"
	switch kind {
	case KindAudit:
		return "执业依据：《中国注册会计师审计准则》。" + base +
			"审计意见只能由注册会计师形成 —— 软件不判断意见类型是否恰当，" +
			"但会提示「未更正错报已达重要性却仍出具无保留意见」这类矛盾。"
	case KindCapital:
		return "执业依据：《中国注册会计师审计准则第 1602 号——验资》。" + base +
			"验资只就**实际收到**的出资发表意见，不对未来的出资义务作保证。"
	default:
		return base + "管理建议书不是审计意见的一部分，也不构成对报表的保证。"
	}
}

// ---------------------------------------------------------------------------
// 审计报告
// ---------------------------------------------------------------------------

// AuditInput 是生成审计报告需要的事实。
//
// 全部来自账套与审计底稿；签字人、报告号、日期由用户填。
type AuditInput struct {
	Company string
	Period  string
	Opinion Opinion
	// FirmName / CPA1 / CPA2 / ReportNo / ReportDate 由用户填。
	FirmName   string
	CPA1       string
	CPA2       string
	ReportNo   string
	ReportDate string

	// ---- 底稿事实 ----
	HasMateriality  bool
	Overall         money.Money
	Performance     money.Money
	Trivial         money.Money
	MisstatementSum money.Money
	MisstatementNum int
	ReclassNum      int
	EvidenceMissing int
	EvidenceBroken  int

	// ---- 报表主要项目 ----
	Assets      money.Money
	Liabilities money.Money
	Equity      money.Money
	Revenue     money.Money
	Profit      money.Money

	// BasisExtra 是注册会计师补充的「形成意见的基础」要点。
	BasisExtra []string
}

// Audit 生成审计报告草稿。
func Audit(in AuditInput) (*Doc, error) {
	if strings.TrimSpace(in.Company) == "" {
		return nil, fmt.Errorf("auditdoc: 缺少被审计单位名称")
	}
	if !in.Opinion.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrBadOpinion, in.Opinion)
	}
	if strings.TrimSpace(in.Period) == "" {
		return nil, fmt.Errorf("auditdoc: 缺少审计期间")
	}

	d := &Doc{
		Kind: KindAudit, Title: "审计报告", Company: in.Company, Period: in.Period,
		Opinion: in.Opinion, OpinionStr: in.Opinion.Label(),
		Draft: true, Submittable: false, Missing: []string{},
		Signature: defaultSignature(KindAudit), PolicyNote: defaultPolicyNote(KindAudit),
	}

	// 一、审计意见
	//
	// ★ 意见类型没选之前，正文里**不写**「我们认为……公允反映」——
	// 那句话是报告的结论本身，只能由签字人下，软件替他说就是替他形成意见。
	opinionBody := fmt.Sprintf(
		"我们审计了%s（以下简称「贵公司」）%s的财务报表，"+
			"包括 %s 的资产负债表、利润表、现金流量表以及财务报表附注。",
		in.Company, in.Period, in.Period)
	if in.Opinion.Chosen() {
		opinionBody += "\n我们认为，" + opinionSentence(in)
	} else {
		opinionBody += "\n【意见类型待注册会计师选择】—— 软件不形成审计意见，" +
			"选定之前这一稿不能用于任何用途。"
		d.Missing = append(d.Missing,
			"还没有选择审计意见类型：无保留 / 保留 / 否定 / 无法表示意见，由注册会计师判断。"+
				"在此之前报告正文不会写出「我们认为……」这一句。")
	}
	d.Sections = append(d.Sections, Section{
		No: "一", Title: "审计意见", Body: opinionBody,
		Source: "意见类型由注册会计师选择；报表项目与金额取自账套",
	})

	// 二、形成审计意见的基础
	//
	// ★ 还没选意见时标题是「形成审计意见的基础」（不能写成「形成未选择的基础」）。
	basisTitle := "形成审计意见的基础"
	if in.Opinion.Chosen() {
		basisTitle = "形成" + in.Opinion.Label() + "的基础"
	}
	body := []string{"我们按照中国注册会计师审计准则的规定执行了审计工作。"}
	if in.Opinion.Chosen() {
		body = append(body, fmt.Sprintf(
			"我们相信，我们获取的审计证据是充分、适当的，为发表%s提供了基础。",
			in.Opinion.Label()))
	} else {
		body = append(body, "我们相信，我们获取的审计证据是充分、适当的。")
	}
	if in.HasMateriality {
		body = append(body, fmt.Sprintf(
			"本期重要性水平：整体重要性 %s、实际执行重要性 %s、明显微小错报临界值 %s（见审计底稿）。",
			in.Overall, in.Performance, in.Trivial))
	} else {
		// ★ 没有重要性水平就出具报告，是「基础」一段里最硬的缺口
		d.Missing = append(d.Missing,
			"本期还没有确定重要性水平：没有门槛就无法说明「多少金额以上的错报需要处理」——"+
				"请先在「审计底稿」页确定重要性水平。")
		body = append(body, "本期重要性水平：**尚未确定**（请在审计底稿中补齐）。")
	}
	body = append(body, fmt.Sprintf(
		"登记的审计调整共 %d 笔（其中重分类 %d 笔，不影响损益）；"+
			"未更正错报合计 %s。",
		in.MisstatementNum, in.ReclassNum, in.MisstatementSum))
	body = append(body, in.BasisExtra...)
	d.Sections = append(d.Sections, Section{
		No: "二", Title: basisTitle,
		Body:   strings.Join(body, "\n"),
		Source: "重要性水平、未更正错报取自审计底稿（审计底稿页）",
	})

	// 三、管理层责任
	d.Sections = append(d.Sections, Section{
		No: "三", Title: "管理层和治理层对财务报表的责任",
		Body: "管理层负责按照小企业会计准则的规定编制财务报表，" +
			"使其实现公允反映，并设计、执行和维护必要的内部控制，" +
			"以使财务报表不存在由于舞弊或错误导致的重大错报。\n" +
			"治理层负责监督贵公司的财务报告过程。",
		Source: "准则标准段落",
	})

	// 四、注册会计师责任
	d.Sections = append(d.Sections, Section{
		No: "四", Title: "注册会计师对财务报表审计的责任",
		Body: "我们的目标是对财务报表整体是否不存在由于舞弊或错误导致的重大错报" +
			"获取合理保证，并出具包含审计意见的审计报告。合理保证是高水平的保证，" +
			"但并不能保证按照审计准则执行的审计在某一重大错报存在时总能发现。\n" +
			"在按照审计准则执行审计工作的过程中，我们运用职业判断，并保持职业怀疑。",
		Source: "准则标准段落",
	})

	// 五、报表主要项目（给签字人一眼核对）
	d.Sections = append(d.Sections, Section{
		No: "五", Title: "已审财务报表主要项目",
		Body: fmt.Sprintf("资产总额 %s；负债总额 %s；所有者权益 %s；\n"+
			"营业收入 %s；利润总额 %s。",
			in.Assets, in.Liabilities, in.Equity, in.Revenue, in.Profit),
		Source: "资产负债表（期末）、利润表（本期），取自账套报表引擎",
	})

	// 签字信息
	d.Inputs = []InputField{
		{Key: "firmName", Label: "会计师事务所名称", Hint: "报告抬头要写事务所全称",
			Value: in.FirmName, FromBook: false},
		{Key: "reportNo", Label: "报告文号", Hint: "如「××会审字〔2025〕第 123 号」",
			Value: in.ReportNo},
		{Key: "cpa1", Label: "注册会计师（签字）", Hint: "审计报告须两名注册会计师签名盖章",
			Value: in.CPA1},
		{Key: "cpa2", Label: "第二名注册会计师（签字）",
			Hint:  "审计报告须两名注册会计师签名盖章 —— 只有一名不能出具",
			Value: in.CPA2},
		{Key: "reportDate", Label: "报告日期", Hint: "不应早于取得充分适当审计证据的日期",
			Value: in.ReportDate},
	}
	for _, f := range d.Inputs {
		if strings.TrimSpace(f.Value) == "" {
			d.Missing = append(d.Missing, fmt.Sprintf("%s：%s", f.Label, f.Hint))
		}
	}

	// ---- ★ 质量护栏：未更正错报已达重要性，却出具无保留意见 ----
	if in.HasMateriality && !in.Overall.IsZero() && in.MisstatementSum >= in.Overall &&
		in.Opinion == OpinionUnqualified {
		d.Missing = append(d.Missing, fmt.Sprintf(
			"★ 未更正错报合计 %s 已达到整体重要性 %s，而意见类型是「无保留意见」——"+
				"这个组合在准则下缺乏依据。要么调整这些错报（登记调整并生成凭证），"+
				"要么由注册会计师说明为什么仍可出具无保留意见。",
			in.MisstatementSum, in.Overall))
	}
	// ---- 依据丢失：报告里说「获取了充分适当的证据」，而依据找不到了 ----
	if in.EvidenceBroken > 0 {
		d.Missing = append(d.Missing, fmt.Sprintf(
			"有 %d 项结论的依据现在找不到了（原件被删或文件丢失）——"+
				"报告里写着「我们相信已获取充分、适当的审计证据」，而这些依据不在账套里。",
			in.EvidenceBroken))
	}
	if in.EvidenceMissing > 0 {
		d.Missing = append(d.Missing, fmt.Sprintf(
			"有 %d 项结论还没有附任何依据：证据链不完整的部分不能在报告里被说成「已获取」。",
			in.EvidenceMissing))
	}

	if err := d.Validate(); err != nil {
		return nil, err
	}
	d.Concludes = d.Conclude()
	return d, nil
}

// opinionSentence 给出「我们认为……」这一句。
func opinionSentence(in AuditInput) string {
	switch in.Opinion {
	case OpinionUnqualified:
		return "后附的财务报表在所有重大方面按照小企业会计准则的规定编制，" +
			"公允反映了贵公司" + in.Period + "的财务状况以及经营成果和现金流量。"
	case OpinionQualified:
		return "除「形成保留意见的基础」部分所述事项产生的影响外，" +
			"后附的财务报表在所有重大方面按照小企业会计准则的规定编制，" +
			"公允反映了贵公司的财务状况以及经营成果和现金流量。"
	case OpinionAdverse:
		return "由于「形成否定意见的基础」部分所述事项的重要性，" +
			"后附的财务报表没有在所有重大方面按照小企业会计准则的规定编制，" +
			"未能公允反映贵公司的财务状况以及经营成果和现金流量。"
	default:
		return "我们不对后附的财务报表发表审计意见。" +
			"由于「形成无法表示意见的基础」部分所述事项的重要性，" +
			"我们无法获取充分、适当的审计证据以作为对财务报表发表审计意见的基础。"
	}
}

// ---------------------------------------------------------------------------
// 验资报告
// ---------------------------------------------------------------------------

// Shareholder 是一位股东的出资情况。
type Shareholder struct {
	Name string
	// Subscribed / Paid 是认缴与实缴（分）。
	Subscribed money.Money
	Paid       money.Money
	// Method 是出资方式（货币 / 实物 / 知识产权…）。
	Method string
	// PaidDate 是出资日期。
	PaidDate string
}

// CapitalInput 是生成验资报告需要的事实。
type CapitalInput struct {
	Company    string
	Period     string
	FirmName   string
	CPA1       string
	CPA2       string
	ReportNo   string
	ReportDate string
	// RegisteredCapital 是登记的注册资本（由用户填，账套里没有这个字段）。
	RegisteredCapital money.Money
	// BookPaidIn 是账上「实收资本」科目的余额合计。
	BookPaidIn money.Money
	// Shareholders 是各股东的认缴与实缴。
	Shareholders []Shareholder
	// Evidence 是验资依据（银行进账单、评估报告、财产权转移手续…）。
	Evidence []string
	// NonCash 为真表示有非货币出资 —— 那必须有评估与权属转移手续。
	NonCash bool
}

// Capital 生成验资报告草稿。
func Capital(in CapitalInput) (*Doc, error) {
	if strings.TrimSpace(in.Company) == "" {
		return nil, fmt.Errorf("auditdoc: 缺少被审验单位名称")
	}
	d := &Doc{
		Kind: KindCapital, Title: "验资报告", Company: in.Company, Period: in.Period,
		Draft: true, Submittable: false, Missing: []string{},
		Signature: defaultSignature(KindCapital), PolicyNote: defaultPolicyNote(KindCapital),
	}

	var paid money.Money
	var rows []string
	for _, s := range in.Shareholders {
		paid = paid.Add(s.Paid)
		rows = append(rows, fmt.Sprintf("%s：认缴 %s，实缴 %s%s%s",
			s.Name, s.Subscribed, s.Paid,
			optionalText("，出资方式 "+s.Method), optionalText("，出资日期 "+s.PaidDate)))
	}

	d.Sections = append(d.Sections, Section{
		No: "一", Title: "审验范围与依据",
		Body: fmt.Sprintf("我们接受委托，审验了%s截至 %s 止申请设立登记/变更登记"+
			"的注册资本实收情况。\n"+
			"我们按照《中国注册会计师审计准则第 1602 号——验资》的规定执行了审验工作。",
			in.Company, in.Period),
		Source: "被审验单位与期间由用户填写",
	})
	d.Sections = append(d.Sections, Section{
		No: "二", Title: "审验结果",
		Body: fmt.Sprintf("经审验，截至 %s 止，贵公司已收到全体股东缴纳的注册资本（实收资本）"+
			"合计 %s。\n%s",
			in.Period, paid, strings.Join(rows, "\n")),
		Source: "各股东出资金额取自账套「实收资本」按股东辅助核算的余额；" +
			"出资方式与日期由用户填写",
	})
	d.Sections = append(d.Sections, Section{
		No: "三", Title: "验资依据",
		Body:   evidenceBody(in.Evidence),
		Source: "由用户填列（银行进账单、评估报告、财产权转移手续等）",
	})
	d.Sections = append(d.Sections, Section{
		No: "四", Title: "本报告的使用限制",
		Body: "本报告仅供贵公司申请办理设立登记/变更登记及据以向股东签发出资证明书时使用，" +
			"不应被视为对贵公司持续经营能力、偿债能力或其他事项的保证。",
		Source: "准则标准段落",
	})

	// ★ 注册资本没填 → 直接进待补事项。
	//
	// 原来「0 就跳过比对」，于是用户留空注册资本时，验资报告最核心的
	// 「实缴 vs 注册资本」这一步**整段不执行**，报告却显示「待签字盖章」。
	// 发布前界面审计实测过这条路径。
	if in.RegisteredCapital.IsZero() {
		d.Missing = append(d.Missing,
			"还没有填注册资本：验资报告的核心结论就是「实缴与注册资本是否相符」，"+
				"这个数不填，本次审验等于没有结论。")
	}
	// 实缴与注册资本不符：验资报告的核心结论就在这个差上
	if !in.RegisteredCapital.IsZero() && paid != in.RegisteredCapital {
		diff := in.RegisteredCapital.Sub(paid)
		if diff.IsPositive() {
			d.Missing = append(d.Missing, fmt.Sprintf(
				"注册资本 %s，而实际收到 %s，尚差 %s —— "+
					"验资只能就**实际收到**的出资发表意见。若为分期出资，"+
					"请在报告中写明本次实收情况与后续出资安排。",
				in.RegisteredCapital, paid, diff))
		} else {
			d.Missing = append(d.Missing, fmt.Sprintf(
				"实际收到的出资 %s **超过**注册资本 %s（多出 %s）——"+
					"超出部分一般应计入资本公积，请核对后再出具。",
				paid, in.RegisteredCapital, diff.Abs()))
		}
	}
	// 账上与表上对不上
	if !in.BookPaidIn.IsZero() && in.BookPaidIn != paid {
		d.Missing = append(d.Missing, fmt.Sprintf(
			"各股东实缴合计 %s 与账上「实收资本」科目余额 %s 不一致 ——"+
				"两个数必须对得上，否则说明有出资没进账或有账没对上人。",
			paid, in.BookPaidIn))
	}
	if len(in.Shareholders) == 0 {
		d.Missing = append(d.Missing, "没有任何股东出资记录：请先在账套里用股东辅助核算登记实收资本。")
	}
	if len(in.Evidence) == 0 {
		d.Missing = append(d.Missing, "没有填验资依据：验资报告的结论必须有依据支撑"+
			"（银行进账单、评估报告、财产权转移手续等）。")
	}
	if in.NonCash {
		d.Missing = append(d.Missing, "有非货币出资：必须有资产评估报告与财产权转移手续，"+
			"并在报告中说明评估机构与评估价值 —— 请确认依据已列入。")
	}
	for _, f := range capitalInputs(in) {
		if strings.TrimSpace(f.Value) == "" {
			d.Missing = append(d.Missing, fmt.Sprintf("%s：%s", f.Label, f.Hint))
		}
	}
	d.Inputs = capitalInputs(in)

	if err := d.Validate(); err != nil {
		return nil, err
	}
	d.Concludes = d.Conclude()
	return d, nil
}

// evidenceBody 拼出验资依据那一段。
//
// ★ 没填也要给一段文字，而不是空字符串。
//
// 空正文会让整份稿子连校验都过不去 —— 用户看到的是一句报错，
// 而不是「这里还缺东西，补上来就能出」。缺什么由 Missing 点名。
func evidenceBody(list []string) string {
	if len(list) == 0 {
		return "（尚未填列验资依据）"
	}
	return strings.Join(list, "\n")
}

// capitalInputs 返回验资报告需要用户填的项。
func capitalInputs(in CapitalInput) []InputField {
	return []InputField{
		{Key: "firmName", Label: "会计师事务所名称", Value: in.FirmName},
		{Key: "reportNo", Label: "报告文号", Hint: "如「××会验字〔2025〕第 123 号」",
			Value: in.ReportNo},
		{Key: "cpa1", Label: "注册会计师（签字）",
			Hint: "验资报告须两名注册会计师签名盖章", Value: in.CPA1},
		{Key: "cpa2", Label: "注册会计师（签字）", Value: in.CPA2},
		{Key: "reportDate", Label: "报告日期",
			Hint: "不应早于取得充分适当验资证据的日期", Value: in.ReportDate},
	}
}

// ---------------------------------------------------------------------------
// 管理建议书
// ---------------------------------------------------------------------------

// Finding 是一条发现与建议。
type Finding struct {
	// No 是序号。
	No string
	// Title 是问题。
	Title string
	// Detail 是具体情况（含金额、单据号）。
	Detail string
	// Suggestion 是建议。
	Suggestion string
	// Source 是这个发现从哪来（体检、底稿、报表…）。
	Source string
	// Level 是严重程度：high | medium | low。
	Level string
}

// LevelLabel 返回严重程度的中文名。
func (f Finding) LevelLabel() string {
	switch f.Level {
	case "high":
		return "重要"
	case "medium":
		return "关注"
	default:
		return "提示"
	}
}

// ManagementInput 是生成管理建议书需要的事实与发现。
type ManagementInput struct {
	Company    string
	Period     string
	FirmName   string
	ReportNo   string
	ReportDate string
	// Scope 是我们做了哪些工作。
	Scope []string
	// Findings 是发现的问题。
	Findings []Finding
}

// Management 生成管理建议书草稿。
func Management(in ManagementInput) (*Doc, error) {
	if strings.TrimSpace(in.Company) == "" {
		return nil, fmt.Errorf("auditdoc: 缺少单位名称")
	}
	d := &Doc{
		Kind: KindManagement, Title: "管理建议书", Company: in.Company, Period: in.Period,
		Draft: true, Submittable: false, Missing: []string{},
		Signature:  defaultSignature(KindManagement),
		PolicyNote: defaultPolicyNote(KindManagement),
	}

	scope := in.Scope
	if len(scope) == 0 {
		scope = []string{"查阅账簿与凭证", "复核报表勾稽关系", "检查往来与实物资产"}
	}
	d.Sections = append(d.Sections, Section{
		No: "一", Title: "我们执行的工作",
		Body:   "在审计过程中，我们执行了以下工作：\n- " + strings.Join(scope, "\n- "),
		Source: "由项目组填列",
	})

	// 二、发现的问题与建议
	var b strings.Builder
	if len(in.Findings) == 0 {
		b.WriteString("本次审计过程中未发现需要书面提出的事项。")
		d.Missing = append(d.Missing,
			"没有任何发现：这份建议书如果只写「未发现问题」，它就只是一张纸。"+
				"请确认体检结果、底稿缺口、往来异常等确实没有需要提出的。")
	}
	for i, f := range in.Findings {
		no := f.No
		if no == "" {
			no = fmt.Sprintf("%d", i+1)
		}
		fmt.Fprintf(&b, "%s.【%s】%s\n", no, f.LevelLabel(), f.Title)
		fmt.Fprintf(&b, "   情况：%s\n", f.Detail)
		fmt.Fprintf(&b, "   建议：%s\n", f.Suggestion)
		if f.Source != "" {
			fmt.Fprintf(&b, "   （来源：%s）\n", f.Source)
		}
	}
	d.Sections = append(d.Sections, Section{
		No: "二", Title: "发现的问题与建议", Body: b.String(),
		Source: "结账前体检、审计底稿、账龄与往来分析的结果",
	})

	// 三、说明
	d.Sections = append(d.Sections, Section{
		No: "三", Title: "说明",
		Body: "本建议书仅供贵公司管理层改进内部管理参考，" +
			"不构成对财务报表的审计意见，也不减轻管理层应承担的责任。",
		Source: "准则标准段落",
	})

	d.Inputs = []InputField{
		{Key: "firmName", Label: "会计师事务所名称", Value: in.FirmName},
		{Key: "reportNo", Label: "文书号", Value: in.ReportNo},
		{Key: "reportDate", Label: "日期", Value: in.ReportDate},
	}
	for _, f := range d.Inputs {
		if strings.TrimSpace(f.Value) == "" {
			d.Missing = append(d.Missing, fmt.Sprintf("%s（管理建议书也要有出具方与日期）", f.Label))
		}
	}

	if err := d.Validate(); err != nil {
		return nil, err
	}
	d.Concludes = d.Conclude()
	return d, nil
}

// optionalText 在非空时返回「前缀 + 值」。
func optionalText(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return s
}

// ---------------------------------------------------------------------------
// 错误
// ---------------------------------------------------------------------------

// 文书的错误。
var (
	ErrBadKind    = fmt.Errorf("auditdoc: 不认识的文书种类")
	ErrBadOpinion = fmt.Errorf("auditdoc: 不认识的审计意见类型")
)
