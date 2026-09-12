package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/vat"
)

// ---------------------------------------------------------------------------
// 增值税政策
// ---------------------------------------------------------------------------

// VATPolicyView 是一条税率政策的展示形态。
//
// 数值都转成给人看的字符串：界面与命令行直接显示，
// 不再各自做一次「百万分之一 → 百分比」的换算 ——
// 那种换算写两遍必然有一天只改一处。
type VATPolicyView struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	Category      string `json:"category"`
	CategoryLabel string `json:"categoryLabel"`
	Status        string `json:"status"`
	StatusLabel   string `json:"statusLabel"`
	Subject       string `json:"subject"`
	SubjectLabel  string `json:"subjectLabel"`
	Method        string `json:"method"`
	MethodLabel   string `json:"methodLabel"`

	// Treatment 是销项侧的税收处理方式（征税 / 免税 / 免税不退税 /
	// 零税率 / 不征税）。
	Treatment      string `json:"treatment"`
	TreatmentLabel string `json:"treatmentLabel"`
	// InputTax 是进项税额的处理方式。
	InputTax      string `json:"inputTax"`
	InputTaxLabel string `json:"inputTaxLabel"`

	// StatutoryRate 是**法定**税率或征收率（如「3%」）。
	//
	// ★ 空串表示「不适用税率」—— 免税、零税率、不征税。
	// 它不是 「0%」：零税率与免税的税率都不适用，但进项处理相反。
	StatutoryRate string `json:"statutoryRate"`
	// PreferentialRate 是**实际适用**的优惠税率；空串表示无优惠。
	PreferentialRate string `json:"preferentialRate"`
	// Rate 是给人看的实际税率文本；不适用税率时是「免税」这类文字。
	Rate string `json:"rate"`
	// RateApplicable 为假表示本政策不适用税率（免税 / 零税率 / 不征税）。
	RateApplicable bool `json:"rateApplicable"`

	// HasPreference 为真表示实际税率与法定税率不同（有优惠）。
	HasPreference bool `json:"hasPreference"`

	// ---- 查询结果的匹配情况（仅 ResolveVATRate 填）----
	// Conditional 为真表示这条政策是**需人工确认的例外情形**
	// （如出口命中公告第七条异常情形）。
	//
	// 例外情形属事实认定，程序不替用户选 —— 界面上要单独标出来，
	// 并提供一个「我确认命中例外」的开关。
	Conditional bool `json:"conditional"`
	// Exact 为假表示没有精确匹配，Policy 是「实际适用的那一条」，
	// 界面**必须**把 Note 显示出来，不能只显示税率。
	Exact bool `json:"exact"`
	// Note 是给用户的说明（Exact 为假时解释为什么不适用）。
	Note string `json:"note"`

	EffectiveFrom string `json:"effectiveFrom"`
	EffectiveTo   string `json:"effectiveTo"`
	ActiveOn      bool   `json:"activeOn"`
	ExpiresOn     string `json:"expiresOn"`

	LegalBasis string `json:"legalBasis"`
	Version    string `json:"version"`
}

// VATPolicies 返回全部税率政策（按当前日期标注是否有效）。
func (s *Service) VATPolicies(ctx context.Context) ([]VATPolicyView, error) {
	list, err := s.db.VATRates().Policies(ctx)
	if err != nil {
		return nil, err
	}
	today := calendar.Today()
	out := make([]VATPolicyView, 0, len(list))
	for _, p := range list {
		out = append(out, toVATPolicyView(p, today))
	}
	return out, nil
}

func toVATPolicyView(p vat.RatePolicy, on calendar.Date) VATPolicyView {
	v := VATPolicyView{
		Code: p.Code, Name: p.Name,
		Category: string(p.Category), CategoryLabel: p.Category.Label(),
		Status: string(p.Status), StatusLabel: p.Status.Label(),
		Subject: string(p.Subject), SubjectLabel: p.Subject.Label(),
		Method: string(p.Method), MethodLabel: p.Method.Label(),
		Treatment:      string(p.Treatment),
		TreatmentLabel: p.Treatment.Label(),
		InputTax:       string(p.InputTax),
		InputTaxLabel:  p.InputTax.Label(),
		StatutoryRate:  p.StatutoryDisplay(),
		Rate:           p.DisplayRate(),
		RateApplicable: p.Treatment.RequiresRate(),
		HasPreference:  p.HasPreference(),
		Conditional:    p.Conditional,
		EffectiveFrom:  p.EffectiveFrom.String(),
		ActiveOn:       p.ActiveOn(on),
		// 政策自身的说明要一路带到界面 ——
		// 「小规模不动产不适用 1% 减征」这类话，
		// 正是用户在该条政策上最需要看到的。
		Note:       p.Note,
		LegalBasis: p.LegalBasis,
		Version:    p.Version,
	}
	if p.Status == "" {
		v.StatusLabel = "不限"
	}
	if p.Subject == "" {
		v.SubjectLabel = "不限"
	}
	v.PreferentialRate = p.PreferentialDisplay()
	if p.EffectiveTo.Valid() {
		v.EffectiveTo = p.EffectiveTo.String()
		v.ExpiresOn = p.EffectiveTo.String()
	}
	return v
}

// VATRateQuery 是按「身份+主体+业务+方法+日期」查税率的参数。
type VATRateQuery struct {
	Status   string
	Subject  string
	Category string
	Method   string
	On       string
	// IncludeConditional 表示**用户已确认命中例外情形**
	// （如出口命中公告第七条异常情形），要求把该例外政策纳入匹配。
	//
	// 默认 false：例外情形属事实认定，替用户选了就可能少缴税。
	IncludeConditional bool
}

// VATRateResult 是查到的适用税率。
type VATRateResult struct {
	VATPolicyView
}

// ResolveVATRate 按业务情形查出适用税率。
//
// ★ 查不到时**报错**，不给默认税率。
// 默认成 13% 或 0% 都会静默算错税，而用户不会知道。
func (s *Service) ResolveVATRate(ctx context.Context, q VATRateQuery) (*VATRateResult, error) {
	list, err := s.db.VATRates().Policies(ctx)
	if err != nil {
		return nil, err
	}

	day := calendar.Today()
	if strings.TrimSpace(q.On) != "" {
		d, perr := calendar.Parse(q.On)
		if perr != nil {
			return nil, fmt.Errorf("业务日期 %q 无效（应为 YYYY-MM-DD）", q.On)
		}
		day = d
	}

	status, err := parseVATStatus(q.Status, s)
	if err != nil {
		return nil, err
	}
	subject := vat.Subject(strings.TrimSpace(q.Subject))
	if subject == "" {
		subject = vat.SubjectEntity
	}
	if !subject.Valid() {
		return nil, fmt.Errorf("经营主体 %q 未知", q.Subject)
	}
	category := vat.BizCategory(strings.TrimSpace(q.Category))
	if !category.Valid() {
		return nil, fmt.Errorf("业务类型 %q 未知；可选：%s", q.Category, categoryList())
	}
	method := vat.TaxationMethod(strings.TrimSpace(q.Method))
	if method == "" {
		if status == vat.VATSmallScale {
			method = vat.MethodSimplified
		} else {
			method = vat.MethodGeneral
		}
	}
	if !method.Valid() {
		return nil, fmt.Errorf("计税方法 %q 未知", q.Method)
	}

	res, err := vat.ResolveRateWith(list, status, subject, category, method, day,
		q.IncludeConditional)
	if err != nil {
		return nil, err
	}
	v := toVATPolicyView(res.Policy, day)
	// ★ 匹配说明必须一路带到界面。
	//
	// 「小规模查出口零税率」这类情形不能只回一句「找不到政策」——
	// 用户真正需要知道的是「你的身份不适用零税率，出口适用免税、不退税」。
	//
	// 精确匹配时 Note 是政策自身的说明，非精确时是「为什么换了一条」，
	// 两者都要显示。
	v.Exact = res.Exact
	v.Note = res.Note
	return &VATRateResult{VATPolicyView: v}, nil
}

// parseVATStatus 解析纳税人身份；留空时取账套当前登记的身份。
func parseVATStatus(s string, svc *Service) (vat.VATStatus, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		b, err := svc.db.Books().Get(context.Background())
		if err == nil && b.VATStatus != "" {
			t = b.VATStatus
		}
	}
	// 兼容命令行里更自然的写法
	if t == "small" {
		t = string(vat.VATSmallScale)
	}
	st := vat.VATStatus(t)
	if !st.Valid() {
		return "", fmt.Errorf("纳税人身份 %q 未知；可选 general | small_scale", s)
	}
	return st, nil
}

func categoryList() string {
	var b []string
	for _, c := range vat.AllCategories() {
		b = append(b, string(c))
	}
	return strings.Join(b, " | ")
}

// ExportVATPolicies 把政策表导出为 JSON，便于编辑后导回。
func (s *Service) ExportVATPolicies(ctx context.Context, dest string) error {
	list, err := s.db.VATRates().Policies(ctx)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dest, append(b, '\n'), 0o644)
}

// ImportVATPolicies 从 JSON 导入政策表（整体替换）。
func (s *Service) ImportVATPolicies(ctx context.Context, src string) (int, error) {
	b, err := os.ReadFile(src)
	if err != nil {
		return 0, fmt.Errorf("读取 %s 失败：%w", src, err)
	}
	var list []vat.RatePolicy
	if err := json.Unmarshal(b, &list); err != nil {
		return 0, fmt.Errorf("解析 %s 失败（应为政策数组）：%w", src, err)
	}
	if err := s.db.VATRates().SavePolicies(ctx, list); err != nil {
		return 0, err
	}
	return len(list), nil
}

// VATPolicyStatus 报告政策表版本与是否需要更新。
type VATPolicyStatus struct {
	Version        string `json:"version"`
	BuiltinVersion string `json:"builtinVersion"`
	// Origin 是 builtin / custom / unknown —— 界面据此说清
	// 「这份表是你改过的」还是「无法确认改没改过」。
	Origin          string `json:"origin"`
	Builtin         bool   `json:"builtin"`
	UpdateAvailable bool   `json:"updateAvailable"`
}

// PolicyStatus 返回政策表版本状态。
//
// ★ 内置表升级时**不**自动覆盖用户改过的表，只提示。
// 直接覆盖等于把用户核过的计税依据删掉。
func (s *Service) PolicyStatus(ctx context.Context) (VATPolicyStatus, error) {
	st, err := s.db.VATRates().PolicyStatusOf(ctx)
	if err != nil {
		return VATPolicyStatus{}, err
	}
	return VATPolicyStatus{
		Version: st.Version, BuiltinVersion: st.BuiltinVersion,
		Origin: st.Origin, Builtin: st.Builtin,
		UpdateAvailable: st.UpdateAvailable,
	}, nil
}

// ResetVATPolicies 把政策表恢复成当前版本的内置默认表。
//
// 只在用户**明确确认**后调用 —— 会丢掉他自己改过的税率。
func (s *Service) ResetVATPolicies(ctx context.Context) error {
	return s.db.VATRates().ResetPolicies(ctx)
}

// SetVATStatus 变更增值税纳税人身份。
//
// 与 SetEnterpriseScale **分开**：这是流转税口径，决定能否抵扣进项。
func (s *Service) SetVATStatus(ctx context.Context, status string, from string, note string) (err error) {
	defer func() {
		if err == nil {
			s.recordAudit(ctx, AuditEvent{
				Action:  audit.ActionVATStatusSet,
				Summary: "变更增值税纳税人身份为 " + status,
				Entity:  "book", EntityID: "vat_status",
				Detail: map[string]any{"身份": status, "生效日": from, "备注": note},
			})
		}
	}()
	return s.setVATStatus(ctx, status, from, note)
}

func (s *Service) setVATStatus(ctx context.Context, status string, from string, note string) error {
	if status == "small" {
		status = string(vat.VATSmallScale)
	}
	st := vat.VATStatus(status)
	if !st.Valid() {
		return fmt.Errorf("纳税人身份 %q 未知；可选 general | small_scale", status)
	}
	var d calendar.Date
	if strings.TrimSpace(from) != "" {
		parsed, err := calendar.Parse(from)
		if err != nil {
			return fmt.Errorf("生效日 %q 无效（应为 YYYY-MM-DD）", from)
		}
		d = parsed
	} else {
		d = calendar.Today()
	}
	return s.db.VATRates().SetVATStatus(ctx, st, d, note)
}

// SetEnterpriseScale 设置企业规模类型（所得税与统计口径）。
func (s *Service) SetEnterpriseScale(ctx context.Context, scale string) (err error) {
	defer func() {
		if err == nil {
			s.recordAudit(ctx, AuditEvent{
				Action:  audit.ActionVATScaleSet,
				Summary: "变更企业规模类型为 " + scale,
				Entity:  "book", EntityID: "enterprise_scale",
				Detail: map[string]any{"规模类型": scale},
			})
		}
	}()
	return s.setEnterpriseScale(ctx, scale)
}

func (s *Service) setEnterpriseScale(ctx context.Context, scale string) error {
	sc := vat.EnterpriseScale(strings.TrimSpace(scale))
	if sc != "" && !sc.Valid() {
		return fmt.Errorf("企业规模类型 %q 未知；可选 micro | small | medium | large", scale)
	}
	return s.db.VATRates().SetEnterpriseScale(ctx, sc)
}

// VATIdentityInfo 是账套的两套身份，供界面同时展示。
type VATIdentityInfo struct {
	// VATStatus 是增值税纳税人身份（流转税）。
	VATStatus              string                `json:"vatStatus"`
	VATStatusLabel         string                `json:"vatStatusLabel"`
	VATStatusEffectiveFrom string                `json:"vatStatusEffectiveFrom"`
	CanDeductInputVAT      bool                  `json:"canDeductInputVat"`
	StatusHistory          []VATStatusPeriodView `json:"statusHistory"`

	// EnterpriseScale 是企业规模类型（所得税与统计）。
	EnterpriseScale      string `json:"enterpriseScale"`
	EnterpriseScaleLabel string `json:"enterpriseScaleLabel"`
	EnterpriseScaleSet   bool   `json:"enterpriseScaleSet"`
}

// VATStatusPeriodView 是一段身份期间。
type VATStatusPeriodView struct {
	Status      string `json:"status"`
	StatusLabel string `json:"statusLabel"`
	From        string `json:"from"`
	To          string `json:"to"`
	Note        string `json:"note"`
}

// VATIdentity 返回账套的两套身份。
func (s *Service) VATIdentity(ctx context.Context) (*VATIdentityInfo, error) {
	b, err := s.db.Books().Get(ctx)
	if err != nil {
		return nil, err
	}
	info, err := s.Book(ctx)
	if err != nil {
		return nil, err
	}
	out := &VATIdentityInfo{
		VATStatus:              info.VATStatus,
		VATStatusLabel:         info.VATStatusLabel,
		VATStatusEffectiveFrom: info.VATStatusEffectiveFrom,
		CanDeductInputVAT:      info.CanDeductInputVAT,
		EnterpriseScale:        info.EnterpriseScale,
		EnterpriseScaleLabel:   info.EnterpriseScaleLabel,
		EnterpriseScaleSet:     info.EnterpriseScale != "",
	}
	_ = b

	hist, err := s.db.VATRates().StatusHistory(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range hist {
		v := VATStatusPeriodView{
			Status: string(p.Status), StatusLabel: p.Status.Label(),
			From: p.EffectiveFrom.String(), Note: p.Note,
		}
		if p.EffectiveTo.Valid() {
			v.To = p.EffectiveTo.String()
		}
		out.StatusHistory = append(out.StatusHistory, v)
	}
	if out.StatusHistory == nil {
		out.StatusHistory = []VATStatusPeriodView{}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 进项抵扣判定
// ---------------------------------------------------------------------------

// DeductionRequest 是一次进项抵扣判定的请求（界面 / 命令行）。
type DeductionRequest struct {
	// On 是业务发生日；留空取今天。身份按这一天取值。
	On string
	// Voucher 是扣税凭证类型。
	Voucher string
	// TaxYuan 是凭证上注明的进项税额（元）。
	TaxYuan string
	// Method 是计税方法；留空按身份推断。
	Method string

	ForSimplifiedOrExempt bool `json:"forSimplifiedOrExempt"`
	AbnormalLoss          bool `json:"abnormalLoss"`
	CollectiveWelfare     bool `json:"collectiveWelfare"`
	CateringRecreation    bool `json:"cateringRecreation"`
	LoanInterest          bool `json:"loanInterest"`
	NonTaxableTransaction bool `json:"nonTaxableTransaction"`
	EquityTransfer        bool `json:"equityTransfer"`

	IsLongTermAsset bool   `json:"isLongTermAsset"`
	MixedUse        bool   `json:"mixedUse"`
	AssetValueYuan  string `json:"assetValueYuan"`
	AlreadyCredited bool   `json:"alreadyCredited"`
}

// DeductionView 是抵扣判定的结论。
type DeductionView struct {
	Deductible       bool        `json:"deductible"`
	Reason           string      `json:"reason"`
	ReasonLabel      string      `json:"reasonLabel"`
	DeductibleAmount money.Money `json:"deductibleAmount"`
	TransferOut      money.Money `json:"transferOut"`
	IncludedInCost   money.Money `json:"includedInCost"`
	Note             string      `json:"note"`

	// 判定时的上下文，便于用户核对「程序是按什么身份判的」
	VATStatusLabel  string `json:"vatStatusLabel"`
	VATStatusOnDate string `json:"vatStatusOnDate"`
	IdentityKnown   bool   `json:"identityKnown"`
	VoucherLabel    string `json:"voucherLabel"`
	MethodLabel     string `json:"methodLabel"`
}

// JudgeDeduction 判定一笔进项税额能否抵扣。
func (s *Service) JudgeDeduction(ctx context.Context, req DeductionRequest) (*DeductionView, error) {
	day := calendar.Today()
	if strings.TrimSpace(req.On) != "" {
		d, err := calendar.Parse(req.On)
		if err != nil {
			return nil, fmt.Errorf("业务日期 %q 无效（应为 YYYY-MM-DD）", req.On)
		}
		day = d
	}

	// ★ 身份按**业务发生日**取，不是拿今天的身份去算去年的税
	hist, err := s.db.VATRates().StatusHistory(ctx)
	if err != nil {
		return nil, err
	}
	status, known := vat.StatusOn(hist, day)

	method := vat.TaxationMethod(strings.TrimSpace(req.Method))
	if method == "" {
		if status == vat.VATSmallScale {
			method = vat.MethodSimplified
		} else {
			method = vat.MethodGeneral
		}
	}

	taxAmount, err := money.Parse(strings.TrimSpace(req.TaxYuan))
	if err != nil {
		return nil, fmt.Errorf("进项税额 %q 无法识别：%w", req.TaxYuan, err)
	}
	var assetValue money.Money
	if strings.TrimSpace(req.AssetValueYuan) != "" {
		assetValue, err = money.Parse(req.AssetValueYuan)
		if err != nil {
			return nil, fmt.Errorf("资产原值 %q 无法识别：%w", req.AssetValueYuan, err)
		}
	}

	kind := vat.VoucherKind(strings.TrimSpace(req.Voucher))
	res := vat.JudgeDeduction(vat.DeductionInput{
		Status: status, IdentityKnown: known, Method: method,
		Voucher: kind, TaxAmount: taxAmount,
		ForSimplifiedOrExempt: req.ForSimplifiedOrExempt,
		AbnormalLoss:          req.AbnormalLoss,
		CollectiveWelfare:     req.CollectiveWelfare,
		CateringRecreation:    req.CateringRecreation,
		LoanInterest:          req.LoanInterest,
		NonTaxableTransaction: req.NonTaxableTransaction,
		EquityTransfer:        req.EquityTransfer,
		IsLongTermAsset:       req.IsLongTermAsset,
		MixedUse:              req.MixedUse,
		AssetOriginalValue:    assetValue,
		AlreadyCredited:       req.AlreadyCredited,
	})

	return &DeductionView{
		Deductible: res.Deductible, Reason: string(res.Reason),
		ReasonLabel:      res.Reason.Label(),
		DeductibleAmount: res.DeductibleAmount,
		TransferOut:      res.TransferOut, IncludedInCost: res.IncludedInCost,
		Note:            res.Note,
		VATStatusLabel:  status.Label(),
		VATStatusOnDate: day.String(),
		IdentityKnown:   known,
		VoucherLabel:    kind.Label(),
		MethodLabel:     method.Label(),
	}, nil
}

// VATReference 返回给界面用的静态参照表（凭证类型、不得抵扣原因、业务类型）。
func (s *Service) VATReference() *VATReferenceInfo {
	out := &VATReferenceInfo{}
	for _, k := range vat.AllVoucherKinds() {
		out.VoucherKinds = append(out.VoucherKinds, VATOption{
			Value: string(k), Label: k.Label(), Deductible: k.CanDeduct(),
		})
	}
	for _, r := range []vat.NonDeductibleReason{
		vat.ReasonNotEligibleTaxpayer, vat.ReasonInvalidVoucher,
		vat.ReasonSimplifiedOrExempt, vat.ReasonAbnormalLoss,
		vat.ReasonCollectiveWelfare, vat.ReasonCateringRecreation,
		vat.ReasonLoanInterest, vat.ReasonNonTaxableTransaction,
		vat.ReasonEquityTransfer,
	} {
		out.NonDeductibleReasons = append(out.NonDeductibleReasons, VATOption{
			Value: string(r), Label: r.Label(), TransferOut: r.RequiresTransferOut(),
		})
	}
	for _, c := range vat.AllCategories() {
		out.Categories = append(out.Categories, VATOption{
			Value: string(c), Label: c.Label(),
		})
	}
	for _, m := range vat.AllMethods() {
		out.Methods = append(out.Methods, VATOption{
			Value: string(m), Label: m.Label(), Deductible: m.AllowsInputCredit(),
		})
	}
	out.SoftwareHint = vat.SoftwareBizHint()
	out.LongTermAssetThreshold = vat.LongTermAssetThreshold
	return out
}

// VATOption 是一个下拉选项。
type VATOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Deductible  bool   `json:"deductible"`
	TransferOut bool   `json:"transferOut"`
}

// VATReferenceInfo 是增值税相关的静态参照表。
type VATReferenceInfo struct {
	VoucherKinds           []VATOption `json:"voucherKinds"`
	NonDeductibleReasons   []VATOption `json:"nonDeductibleReasons"`
	Categories             []VATOption `json:"categories"`
	Methods                []VATOption `json:"methods"`
	SoftwareHint           string      `json:"softwareHint"`
	LongTermAssetThreshold money.Money `json:"longTermAssetThreshold"`
}
