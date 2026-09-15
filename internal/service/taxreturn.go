package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"miniaccount/internal/ai/aiprovider"
	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/taxreturn"
)

// ---------------------------------------------------------------------------
// 税务计算表
// ---------------------------------------------------------------------------
//
// 三张表：增值税及附加、企业所得税（季度预缴）、个人所得税（工资薪金）。
//
// ★ 两张表的口径必须说清楚，它们最容易做错：
//
//	增值税        按**本期发生额**算，留抵要从上期余额滚过来
//	企业所得税    按**年初至本月累计**算（按季度算会系统性少缴）
//	个人所得税    按**年初至本月累计**算（累计预扣预缴法）
//
// 三张表都带 `Submittable=false` —— 软件不替代申报表。
// 每一行都带「这个数从哪来」，会计要能拿着去申报系统逐行核对。

// TaxReturnView 是一张税务计算表的界面形状。
type TaxReturnView struct {
	Kind        string             `json:"kind"`
	KindLabel   string             `json:"kindLabel"`
	Period      string             `json:"period"`
	Title       string             `json:"title"`
	Rows        []TaxReturnRowView `json:"rows"`
	Keys        []TaxReturnKeyView `json:"keys"`
	Warnings    []string           `json:"warnings"`
	Submittable bool               `json:"submittable"`
	PolicyNote  string             `json:"policyNote"`
	Concludes   string             `json:"concludes"`
	// Sources 是取数说明（每个数从哪个科目/报表来）。
	Sources []string `json:"sources"`
	// Identities 是算这张表时用的身份与口径（界面顶部显示）。
	Identities []TaxIdentityView `json:"identities"`
	// Inputs 是需要用户填的部分（企业所得税的纳税调整等）。
	InputFields []TaxInputFieldView `json:"inputFields"`
	// Payable 是这张表算出来的「本期应补(退)」金额。
	Payable money.Money `json:"payable"`
	// Tax / Surcharge / Paid 是它的三个分量：Payable = Tax + Surcharge − Paid。
	//
	// ★ 申报台账要按这个等式校验快照，所以分量必须一起传出来。
	// 让调用方去 Keys 里按标签找，改一个措辞就会静默取到 0。
	Tax       money.Money `json:"tax"`
	Surcharge money.Money `json:"surcharge"`
	Paid      money.Money `json:"paid"`
	// Filing 是本期该税种的申报记录（没有则为 nil）。
	Filing *TaxFilingView `json:"filing"`
	// FilingHint 是申报状态的说明（没报 / 报了但账后来改了）。
	//
	// ★ 计算表上必须显示「这期报了没」：只给一张算得漂亮的表
	// 却不告诉用户已经报过了，他很可能照着再报一次 ——
	// 而重复申报的更正很麻烦。
	FilingHint string `json:"filingHint"`
}

// TaxReturnRowView 是表的一行。
type TaxReturnRowView struct {
	Line     string      `json:"line"`
	Label    string      `json:"label"`
	Amount   money.Money `json:"amount"`
	Source   string      `json:"source"`
	Note     string      `json:"note"`
	Emphasis bool        `json:"emphasis"`
}

// TaxReturnKeyView 是一个关键数。
type TaxReturnKeyView struct {
	Label  string      `json:"label"`
	Amount money.Money `json:"amount"`
	Note   string      `json:"note"`
}

// TaxIdentityView 是一个口径/身份标签。
type TaxIdentityView struct {
	Label string `json:"label"`
	Value string `json:"value"`
	// Warn 为真表示这一项需要用户注意（如身份未填）。
	Warn bool `json:"warn"`
}

// TaxInputFieldView 是界面要提供的输入项。
type TaxInputFieldView struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Hint     string `json:"hint"`
	Amount   string `json:"amount"`
	Required bool   `json:"required"`
}

// TaxReturnInput 是算某一期某张表的入参。
type TaxReturnInput struct {
	Kind  string
	Year  int
	Month int
	// ---- 企业所得税：这几项必须人工判断，程序不猜 ----
	TaxAdjustIncrease money.Money `json:"taxAdjustIncrease"`
	TaxAdjustDecrease money.Money `json:"taxAdjustDecrease"`
	LossOffset        money.Money `json:"lossOffset"`
	// SmallLowProfit 是否按小型微利企业口径；nil 表示按账套企业规模推断。
	SmallLowProfit *bool `json:"smallLowProfit"`
	// ---- 增值税：附加税费政策 ----
	// UrbanConstructionPPM 是城建税税率（0 表示用 7%）。
	UrbanConstructionPPM int64 `json:"urbanConstructionPpm"`
}

// TaxReturn 生成一张税务计算表草稿。
// TaxReturn 生成一张税务计算表草稿（含本期的申报状态）。
func (s *Service) TaxReturn(ctx context.Context, in TaxReturnInput) (*TaxReturnView, error) {
	k := period.NewKey(in.Year, in.Month)
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", in.Year, in.Month)
	}
	kind := taxreturn.Kind(strings.TrimSpace(in.Kind))
	if !kind.Valid() {
		return nil, fmt.Errorf("%w: %q", taxreturn.ErrBadKind, in.Kind)
	}
	if err := s.requireOpenBook(ctx); err != nil {
		return nil, err
	}

	v, ret, err := s.computeTaxReturn(ctx, kind, k, in)
	if err != nil {
		return nil, err
	}

	// ★ 申报状态要挂在计算表上。
	//
	// 这里与 taxReturnRaw 分开，是因为**勾稽要用「现在算出来多少」**，
	// 而算出来多少不能再回头去查申报状态 —— 那会绕成一个环
	// （查状态 → 算表 → 再查状态），栈会直接爆掉。
	filing, hint := s.filingStatusOf(ctx, kind, k)
	v.Filing = filing
	v.FilingHint = hint

	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionTaxReturnView,
		Summary: fmt.Sprintf("生成%s（%s）", kind.Label(), k),
		Entity:  "tax_return", EntityID: string(kind) + "/" + k.String(),
		Detail: map[string]any{
			"税种": kind.Label(), "期间": k.String(),
			"结论": ret.Concludes,
		},
	})
	return v, nil
}

// taxReturnRaw 只算表、**不查申报状态**。
//
// 给勾稽与待申报提醒用：它们本来就在处理申报记录，
// 再回头查一次申报状态就会绕成一个环。
func (s *Service) taxReturnRaw(ctx context.Context, kind taxreturn.Kind,
	k period.Key, in TaxReturnInput) (*TaxReturnView, error) {

	v, _, err := s.computeTaxReturn(ctx, kind, k, in)
	return v, err
}

// computeTaxReturn 按税种算表并翻成界面形状。
func (s *Service) computeTaxReturn(ctx context.Context, kind taxreturn.Kind,
	k period.Key, in TaxReturnInput) (*TaxReturnView, *taxreturn.Return, error) {

	var (
		ret     *taxreturn.Return
		sources []string
		ident   []TaxIdentityView
		fields  []TaxInputFieldView
		err     error
	)
	switch kind {
	case taxreturn.KindVAT:
		ret, sources, ident, err = s.vatReturn(ctx, k, in)
	case taxreturn.KindCIT:
		ret, sources, ident, fields, err = s.citReturn(ctx, k, in)
	case taxreturn.KindIIT:
		ret, sources, ident, err = s.iitReturn(ctx, k)
	}
	if err != nil {
		return nil, nil, err
	}
	return taxReturnView(ret, sources, ident, fields), ret, nil
}

// vatReturn 生成增值税及附加税费计算表。
func (s *Service) vatReturn(ctx context.Context, k period.Key,
	in TaxReturnInput) (*taxreturn.Return, []string, []TaxIdentityView, error) {

	cols, err := s.db.TaxReturns().VATColumnsOf(ctx, k)
	if err != nil {
		return nil, nil, nil, err
	}
	book, err := s.Book(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	// 身份决定两件事：能不能抵扣、附加税费是否减半
	small := book.VATStatus == "small_scale"
	sur := taxreturn.DefaultSurchargeRates()
	if in.UrbanConstructionPPM > 0 {
		sur.UrbanConstructionPPM = in.UrbanConstructionPPM
	}
	// 附加税费减半（「六税两费」优惠）：增值税小规模纳税人、
	// **小型微利企业**与个体工商户都减半征收。
	//
	// ★ 原来只按小规模判，一般纳税人里的小型微利企业会**多算**附加税费。
	// 依据：财税〔2019〕13号，财政部 税务总局公告 2022 年第 10 号把
	// 范围扩到小型微利企业与个体工商户。
	lowProfit := likelySmallLowProfit(book.EnterpriseScale)
	sur.Halved = small || lowProfit

	ret, err := taxreturn.VAT(taxreturn.VATInput{
		Period: k.String(), SmallScale: small,
		OutputTax: cols.Output, OutputTaxDeducted: cols.OutputDeducted,
		InputTax: cols.Input, InputTaxTransferredOut: cols.InputTransferredOut,
		ExportTaxRefund: cols.ExportTaxRefund, ExportOffset: cols.ExportOffset,
		TaxRelief: cols.TaxRelief, Paid: cols.Paid,
		PriorCredit: cols.PriorCredit, Surcharges: sur,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	halvedWhy := ""
	switch {
	case small:
		halvedWhy = "（增值税小规模纳税人减半）"
	case lowProfit:
		halvedWhy = "（小型微利企业减半）"
	}
	ident := []TaxIdentityView{
		{Label: "增值税纳税人身份", Value: book.VATStatusLabel,
			Warn: book.VATStatus == ""},
		{Label: "计税方法", Value: pick(small, "简易计税（征收率）", "一般计税（进项可抵）")},
		{Label: "附加税费比例", Value: fmt.Sprintf("城建税 %s + 教育费附加 3%% + 地方教育附加 2%%%s",
			percentText(sur.UrbanConstructionPPM), halvedWhy)},
		{Label: "城建税税率", Value: "默认 7%（市区）；县城/镇 5%、其他 1%，请按纳税人所在地核对", Warn: true},
	}
	if !sur.Halved {
		ident = append(ident, TaxIdentityView{
			Label: "附加税费减半",
			Value: "未适用：账套既不是小规模纳税人，企业规模也不是小型/微型 —— " +
				"若实际符合小型微利企业条件（应纳税所得额 ≤ 300 万、从业人数 ≤ 300 人、" +
				"资产总额 ≤ 5,000 万元），请到设置里补填企业规模",
			Warn: book.EnterpriseScale == "",
		})
	}
	// 取数时发现的问题（如某专栏净额为负）一并挂到表上
	ret.Warnings = append(ret.Warnings, cols.Warnings...)
	if !book.CanDeductInputVAT && cols.Input.IsPositive() {
		ret.Warnings = append(ret.Warnings, fmt.Sprintf(
			"身份是「%s」而账上有进项税额 %s：这种身份不能抵扣进项，"+
				"进项税应当计入采购成本 —— 请核对是不是记错了科目。",
			book.VATStatusLabel, cols.Input))
	}
	return ret, cols.Notes, ident, nil
}

// citReturn 生成企业所得税计算表。
func (s *Service) citReturn(ctx context.Context, k period.Key, in TaxReturnInput) (
	*taxreturn.Return, []string, []TaxIdentityView, []TaxInputFieldView, error) {

	fig, err := s.db.TaxReturns().CITFiguresOf(ctx, k)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	book, err := s.Book(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	// 小型微利企业：由用户确认优先，没给就按账套的企业规模推断
	small := false
	inferred := false
	if in.SmallLowProfit != nil {
		small = *in.SmallLowProfit
	} else if likelySmallLowProfit(book.EnterpriseScale) {
		small = true
		inferred = true
	}

	ret, err := taxreturn.CIT(taxreturn.CITInput{
		Period:  taxreturn.CITPeriodLabel(k.Year, k.Month),
		Revenue: fig.Revenue, Cost: fig.Cost, Profit: fig.Profit,
		TaxAdjustIncrease: in.TaxAdjustIncrease, TaxAdjustDecrease: in.TaxAdjustDecrease,
		LossOffset: in.LossOffset, Prepaid: fig.Prepaid,
		SmallLowProfit: small,
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if inferred {
		ret.Warnings = append(ret.Warnings, fmt.Sprintf(
			"小型微利企业口径是按账套里填的「企业规模（%s）」推断的 —— "+
				"判定标准还包括从业人数（≤300 人）与资产总额（≤5,000 万元），"+
				"请确认三项都符合再按这个口径申报。",
			book.EnterpriseScaleLabel))
	}
	ident := []TaxIdentityView{
		{Label: "所属期", Value: taxreturn.CITPeriodLabel(k.Year, k.Month)},
		{Label: "征收方式", Value: "查账征收（按季预缴）"},
		{Label: "优惠口径", Value: pick(small, "小型微利企业（实际税负 5%）", "一般企业（25%）"),
			Warn: inferred},
		{Label: "取数口径", Value: "利润表**年初至本月累计**数（预缴是累计口径，不是本季数）"},
	}
	fields := []TaxInputFieldView{
		{Key: "taxAdjustIncrease", Label: "纳税调整增加额",
			Hint:   "业务招待费超支、广告费超限、罚款滞纳金、超标准捐赠等（税前扣除有限额的项目）",
			Amount: yuanText(in.TaxAdjustIncrease), Required: false},
		{Key: "taxAdjustDecrease", Label: "纳税调整减少额",
			Hint:   "免税收入、研发费加计扣除、不征税收入等",
			Amount: yuanText(in.TaxAdjustDecrease), Required: false},
		{Key: "lossOffset", Label: "弥补以前年度亏损",
			Hint:   "不超过税法规定的弥补年限（一般 5 年）",
			Amount: yuanText(in.LossOffset), Required: false},
	}
	return ret, fig.Notes, ident, fields, nil
}

// iitReturn 生成个人所得税扣缴计算表。
func (s *Service) iitReturn(ctx context.Context, k period.Key) (
	*taxreturn.Return, []string, []TaxIdentityView, error) {

	list, drafts, err := s.db.TaxReturns().IITYTDOf(ctx, k)
	if err != nil {
		return nil, nil, nil, err
	}
	table, err := s.db.Payroll().TaxTable(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	emps := make([]taxreturn.IITEmployee, 0, len(list))
	for _, e := range list {
		emps = append(emps, taxreturn.IITEmployee{
			Name: e.Name, Code: e.Code, Months: e.Months,
			Income: e.Income, SpecialDeduction: e.SpecialDeduction,
			SpecialAdditional: e.SpecialAdditional, OtherDeduction: e.OtherDeduction,
			TaxWithheld: e.TaxWithheld,
		})
	}
	ret, err := taxreturn.IIT(taxreturn.IITInput{
		Period: taxreturn.PeriodLabel(k.Year, k.Month), Table: table, Employees: emps,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	if drafts > 0 && len(emps) > 0 {
		// ★ 草稿工资单没进账，但个税是「该扣多少」的申报口径，
		// 所以要用它算；只是得让用户知道这个数是**预计数**
		ret.Warnings = append(ret.Warnings, fmt.Sprintf(
			"本期用了 %d 张**草稿**工资单的数据：这张表算的是「该扣多少」，"+
				"工资单过账之后累计数才算定下来。", drafts))
	}
	sources := []string{
		fmt.Sprintf("累计数取自本年度截至 %s 的最后一张工资单（每位员工各自最近的一张）", k),
		"固化的累计字段：工资单生成时算出来的 cum_* —— 直接读它们而不是现在重算，" +
			"否则工资单写着扣 300、税务表算出该扣 280，两个数对不上",
		"预扣率表与减除费用标准来自账套里可配置的参数表（工资模块）",
	}
	ident := []TaxIdentityView{
		{Label: "所属期", Value: taxreturn.PeriodLabel(k.Year, k.Month)},
		{Label: "计算口径", Value: "累计预扣预缴法（年初至本月累计）"},
		{Label: "扣缴人数", Value: fmt.Sprintf("%d 人", len(emps))},
		{Label: "数据来源", Value: fmt.Sprintf("工资单（其中草稿 %d 张）", drafts), Warn: drafts > 0},
	}
	return ret, sources, ident, nil
}

// taxReturnView 把领域对象翻成界面形状。
func taxReturnView(r *taxreturn.Return, sources []string,
	ident []TaxIdentityView, fields []TaxInputFieldView) *TaxReturnView {

	v := &TaxReturnView{
		Kind: string(r.Kind), KindLabel: r.Kind.Label(), Period: r.Period,
		Title: r.Title, Warnings: []string{}, Sources: []string{},
		Identities: []TaxIdentityView{}, InputFields: []TaxInputFieldView{},
		Submittable: r.Submittable, PolicyNote: r.PolicyNote, Concludes: r.Concludes,
		Payable: r.Payable, Tax: r.Tax, Surcharge: r.Surcharge, Paid: r.Paid,
	}
	v.Submittable = false // 兜底：领域层已经保证，这里再压一次
	for _, row := range r.Rows {
		v.Rows = append(v.Rows, TaxReturnRowView{
			Line: row.Line, Label: row.Label, Amount: row.Amount,
			Source: row.Source, Note: row.Note, Emphasis: row.Emphasis,
		})
	}
	for _, k := range r.Keys {
		v.Keys = append(v.Keys, TaxReturnKeyView{
			Label: k.Label, Amount: k.Amount, Note: k.Note,
		})
	}
	v.Warnings = append(v.Warnings, r.Warnings...)
	v.Sources = append(v.Sources, sources...)
	v.Identities = append(v.Identities, ident...)
	v.InputFields = append(v.InputFields, fields...)
	return v
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// requireOpenBook 确认已建账。
func (s *Service) requireOpenBook(ctx context.Context) error {
	if _, err := s.db.Books().Get(ctx); err != nil {
		return errors.New("还没有账套：请先建账")
	}
	return nil
}

// vatStatusLabel 返回增值税身份的中文名。
func vatStatusLabel(status string) string {
	switch status {
	case "general":
		return "一般纳税人"
	case "small_scale":
		return "小规模纳税人"
	case "":
		return "未填写"
	default:
		return status
	}
}

// likelySmallLowProfit 按账套里填的企业规模**推断**是否可能属于小型微利企业。
//
// 只是推断：小型微利企业要同时满足三项（应纳税所得额 ≤ 300 万、
// 从业人数 ≤ 300 人、资产总额 ≤ 5,000 万元），账套只知道规模类型。
// 所以凡是用到它的地方都要把「这是推断」写在表上，让人来确认。
func likelySmallLowProfit(scale string) bool {
	return scale == "micro" || scale == "small"
}

// percentText 把百万分比写成百分比文字。
func percentText(ppm int64) string {
	s := fmt.Sprintf("%.2f", float64(ppm)/10000)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s + "%"
}

// halvedText 返回减半优惠的说明。
func halvedText(half bool) string {
	if half {
		return "（小规模纳税人减半征收）"
	}
	return ""
}

// pick 二选一。
func pick(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// yuanText 把金额写成输入框里的「元」文字（不带千分位）。
//
// 用 PlainString 而不是 String：后者带千分位（"12,000.00"），
// 填回输入框再提交时会被金额解析当成非法输入 ——
// 界面自己生成的默认值，用户一个字没改却提交不了。
func yuanText(m money.Money) string {
	if m.IsZero() {
		return ""
	}
	return m.PlainString()
}

// ---------------------------------------------------------------------------
// 给 AI 用的紧凑形状
// ---------------------------------------------------------------------------
//
// ★ 与界面读的是**同一个** TaxReturn：同一条计算路径、同一份取数。
// 让模型自己按记忆里的税率算一遍，最坏的结果不是算错一位数，
// 而是它用了一个过期的优惠政策还说得头头是道。

// TaxReturnBrief 生成给 AI 看的税务计算表。
func (s *Service) TaxReturnBrief(ctx context.Context, kind string,
	year, month int) (*aiprovider.TaxReturnBrief, error) {

	v, err := s.TaxReturn(ctx, TaxReturnInput{
		Kind: kind, Year: year, Month: month,
	})
	if err != nil {
		return nil, err
	}
	out := &aiprovider.TaxReturnBrief{
		Kind: v.Kind, KindLabel: v.KindLabel, Period: v.Period, Title: v.Title,
		Warnings: v.Warnings, PolicyNote: v.PolicyNote, Concludes: v.Concludes,
		FilingHint: v.FilingHint,
	}
	if v.Filing != nil {
		out.FilingStatus = v.Filing.StatusLabel
	}
	for _, id := range v.Identities {
		out.Identities = append(out.Identities, id.Label+"："+id.Value)
	}
	for _, r := range v.Rows {
		out.Rows = append(out.Rows, aiprovider.TaxReturnBriefRow{
			Line: r.Line, Label: r.Label, Amount: r.Amount,
			Source: r.Source, Note: r.Note,
		})
	}
	for _, k := range v.Keys {
		out.Keys = append(out.Keys, aiprovider.TaxReturnBriefRow{
			Label: k.Label, Amount: k.Amount, Note: k.Note,
		})
	}
	return out, nil
}

// TaxReturnKindOption 是税种选项（界面标签页用）。
type TaxReturnKindOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// TaxReturnKindOptions 返回三种税。
func (s *Service) TaxReturnKindOptions() []TaxReturnKindOption {
	out := make([]TaxReturnKindOption, 0, len(taxreturn.AllKinds))
	for _, k := range taxreturn.AllKinds {
		out = append(out, TaxReturnKindOption{Value: string(k), Label: k.Label()})
	}
	return out
}
