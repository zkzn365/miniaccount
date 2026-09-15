package service

import (
	"context"
	"fmt"
	"strings"

	"miniaccount/internal/ai/aiprovider"
	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/auditdoc"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/store/sqlite"
)

// ---------------------------------------------------------------------------
// 审计与鉴证文书（草稿）
// ---------------------------------------------------------------------------
//
// 三份：审计报告、验资报告、管理建议书。
//
// ★ 这一层的职责只有一件：把**事实**摆整齐。
//
// 意见类型、认缴出资、事务所与签字人 —— 这些是执业判断，
// 由用户填；账套能提供的是数字与证据，以及一句诚实的「还缺什么」。
// 所以每个 builder 都会算出一个 `Missing` 清单，非空就不许签发。

// AuditDocView 是一份文书草稿的界面形状。
type AuditDocView struct {
	Kind      string `json:"kind"`
	KindLabel string `json:"kindLabel"`
	Title     string `json:"title"`
	Company   string `json:"company"`
	Period    string `json:"period"`
	// Opinion 仅审计报告有。
	Opinion    string `json:"opinion"`
	OpinionStr string `json:"opinionStr"`
	// Sections 是正文段落。
	Sections []AuditDocSectionView `json:"sections"`
	// Inputs 是还需要用户填的项（签字人、报告号、日期…）。
	Inputs []AuditDocInputView `json:"inputs"`
	// Missing 是出这份报告前必须补齐的东西。
	Missing []string `json:"missing"`
	// CanIssue 为真表示没有待补事项。
	CanIssue bool `json:"canIssue"`
	// Draft 恒为 true、Submittable 恒为 false。
	Draft       bool   `json:"draft"`
	Submittable bool   `json:"submittable"`
	Signature   string `json:"signature"`
	PolicyNote  string `json:"policyNote"`
	Concludes   string `json:"concludes"`
	// FullText 是整份文书的纯文本（用于打印 / 另存 / 复制）。
	FullText string `json:"fullText"`
}

// AuditDocSectionView 是一段。
type AuditDocSectionView struct {
	No     string `json:"no"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Source string `json:"source"`
}

// AuditDocInputView 是一项需要用户填的内容。
type AuditDocInputView struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Hint     string `json:"hint"`
	Value    string `json:"value"`
	FromBook bool   `json:"fromBook"`
}

// AuditDocInput 是生成文书的入参。
type AuditDocInput struct {
	Kind  string `json:"kind"`
	Year  int    `json:"year"`
	Month int    `json:"month"`
	// ---- 审计报告 ----
	Opinion    string   `json:"opinion"`
	BasisExtra []string `json:"basisExtra"`
	// ---- 验资报告 ----
	RegisteredCapital money.Money `json:"registeredCapital"`
	Evidence          []string    `json:"evidence"`
	NonCash           bool        `json:"nonCash"`
	// Shares 是各股东的认缴/出资方式/日期（实缴由账套取）。
	Shares []ShareInput `json:"shares"`
	// ---- 共同的签字信息 ----
	FirmName   string `json:"firmName"`
	CPA1       string `json:"cpa1"`
	CPA2       string `json:"cpa2"`
	ReportNo   string `json:"reportNo"`
	ReportDate string `json:"reportDate"`
}

// ShareInput 是一位股东的出资信息。
type ShareInput struct {
	Name       string      `json:"name"`
	Subscribed money.Money `json:"subscribed"`
	Method     string      `json:"method"`
	PaidDate   string      `json:"paidDate"`
}

// AuditDoc 生成一份文书草稿。
func (s *Service) AuditDoc(ctx context.Context, in AuditDocInput) (*AuditDocView, error) {
	k := period.NewKey(in.Year, in.Month)
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", in.Year, in.Month)
	}
	kind := auditdoc.Kind(strings.TrimSpace(in.Kind))
	if !kind.Valid() {
		return nil, fmt.Errorf("%w: %q", auditdoc.ErrBadKind, in.Kind)
	}
	book, err := s.Book(ctx)
	if err != nil {
		return nil, err
	}

	var doc *auditdoc.Doc
	switch kind {
	case auditdoc.KindAudit:
		doc, err = s.auditReport(ctx, k, book.CompanyName, in)
	case auditdoc.KindCapital:
		doc, err = s.capitalReport(ctx, k, book.CompanyName, in)
	case auditdoc.KindManagement:
		doc, err = s.managementLetter(ctx, k, book.CompanyName, in)
	}
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAuditDocDraft,
		Summary: fmt.Sprintf("生成%s草稿（%s）", kind.Label(), k),
		Entity:  "audit_doc", EntityID: string(kind) + "/" + k.String(),
		Operator: strings.TrimSpace(in.CPA1),
		Detail: map[string]any{
			"文书": kind.Label(), "期间": k.String(),
			"待补事项": len(doc.Missing),
			"结论":   doc.Concludes,
		},
	})
	return auditDocView(doc), nil
}

// auditReport 生成审计报告（事实全部来自底稿与报表）。
func (s *Service) auditReport(ctx context.Context, k period.Key,
	company string, in AuditDocInput) (*auditdoc.Doc, error) {

	w, err := s.Workpaper(ctx, k)
	if err != nil {
		return nil, err
	}
	bs, _, _, err := s.db.Reports().BuildBalanceSheet(ctx, endOf(k))
	if err != nil {
		return nil, err
	}
	_, pl, _, err := s.db.Reports().BuildIncomeStatement(ctx, k)
	if err != nil {
		return nil, err
	}
	ev, err := s.Evidence(ctx, k)
	if err != nil {
		return nil, err
	}

	ai := auditdoc.AuditInput{
		Company: company, Period: k.String(),
		Opinion:  auditdoc.Opinion(strings.TrimSpace(in.Opinion)),
		FirmName: in.FirmName, CPA1: in.CPA1, CPA2: in.CPA2,
		ReportNo: in.ReportNo, ReportDate: in.ReportDate,
		BasisExtra: in.BasisExtra,
	}
	// ★ 不替注册会计师选意见类型。
	//
	// 原来缺省填「无保留意见」，正文于是直接写出「我们认为……公允反映」——
	// 那等于软件替签字人形成了意见。现在缺省就是不选，正文里写明待选，
	// 并进「待补事项」。
	if ai.Opinion == "" {
		ai.Opinion = auditdoc.OpinionUnspecified
	}
	if w.Materiality != nil {
		ai.HasMateriality = true
		ai.Overall = w.Materiality.Overall
		ai.Performance = w.Materiality.Performance
		ai.Trivial = w.Materiality.Trivial
	}
	ai.MisstatementSum = w.Misstatements.Total
	ai.MisstatementNum = len(w.Misstatements.Items)
	ai.ReclassNum = w.Misstatements.ReclassCount
	ai.EvidenceMissing = ev.Unsupported
	ai.EvidenceBroken = ev.Broken

	// 资产负债表行次：30 资产总计、47 负债合计、52 所有者权益合计
	// （行 53 是「负债和所有者权益总计」，与行 30 必须相等）
	ai.Assets = lineValue(bs, 30)
	ai.Liabilities = lineValue(bs, 47)
	ai.Equity = lineValue(bs, 52)
	// 利润表行次：1 营业收入、30 利润总额
	ai.Revenue = lineValue(pl, 1)
	ai.Profit = lineValue(pl, 30)

	return auditdoc.Audit(ai)
}

// capitalReport 生成验资报告（各股东实缴来自账套）。
func (s *Service) capitalReport(ctx context.Context, k period.Key,
	company string, in AuditDocInput) (*auditdoc.Doc, error) {

	// 账上「实收资本」按股东辅助核算的余额
	rows, err := s.db.Reports().ContactBalances(ctx, k, "3001")
	if err != nil {
		return nil, err
	}
	paidByName := map[string]money.Money{}
	var bookPaid money.Money
	negative := []string{}
	for _, r := range rows {
		if r.AccountCode != "3001" {
			continue
		}
		// ★ 实收资本是贷方科目：取**贷方净额**，不能取绝对值。
		//
		// 取绝对值会把「借方余额」这个异常也当成实缴 ——
		// 而实收资本出现借方余额说明抽逃出资、错账或科目用错，
		// 验资报告里必须点出来，不能抹平成一个「已实缴」的数。
		amt := r.Closing.Neg() // Closing 是「借−贷」，贷方余额取负号
		if amt.IsNegative() {
			negative = append(negative, fmt.Sprintf("%s（%s）", r.ContactName, amt))
		}
		paidByName[r.ContactName] = amt
		bookPaid = bookPaid.Add(amt)
	}

	// 用户填的认缴/方式/日期与账上实缴合并
	shares := make([]auditdoc.Shareholder, 0, len(paidByName)+len(in.Shares))
	seen := map[string]bool{}
	for _, sh := range in.Shares {
		name := strings.TrimSpace(sh.Name)
		if name == "" {
			continue
		}
		seen[name] = true
		shares = append(shares, auditdoc.Shareholder{
			Name: name, Subscribed: sh.Subscribed, Paid: paidByName[name],
			Method: strings.TrimSpace(sh.Method), PaidDate: strings.TrimSpace(sh.PaidDate),
		})
	}
	// 账上有、用户没填的股东也要列出来（漏一个股东，验资结论就是错的）
	var missingShare []auditdoc.Shareholder
	for name, paid := range paidByName {
		if seen[name] {
			continue
		}
		missingShare = append(missingShare, auditdoc.Shareholder{Name: name, Paid: paid})
	}
	// 顺序稳定：先用户填的，再账上多出来的
	shares = append(shares, missingShare...)

	doc, err := auditdoc.Capital(auditdoc.CapitalInput{
		Company: company, Period: endOf(k).String(),
		FirmName: in.FirmName, CPA1: in.CPA1, CPA2: in.CPA2,
		ReportNo: in.ReportNo, ReportDate: in.ReportDate,
		RegisteredCapital: in.RegisteredCapital,
		BookPaidIn:        bookPaid,
		Shareholders:      shares,
		Evidence:          in.Evidence,
		NonCash:           in.NonCash,
	})
	if err != nil {
		return nil, err
	}
	if len(negative) > 0 {
		doc.Missing = append(doc.Missing, fmt.Sprintf(
			"账上「实收资本」出现**借方余额**：%s —— 实收资本是贷方科目，"+
				"借方余额说明抽逃出资、错账或科目用错。验资报告不能就这个数出具，"+
				"请先查清。", strings.Join(negative, "、")))
	}
	// 用户没填认缴的股东：不能替他把认缴等于实缴
	for _, sh := range shares {
		if sh.Subscribed.IsZero() {
			doc.Missing = append(doc.Missing, fmt.Sprintf(
				"股东「%s」的认缴出资额还没填：验资报告要写清认缴与实缴，"+
					"才能说明出资是否缴足。", sh.Name))
		}
	}
	doc.Concludes = doc.Conclude()
	return doc, nil
}

// managementLetter 生成管理建议书（发现来自体检、底稿与证据链）。
func (s *Service) managementLetter(ctx context.Context, k period.Key,
	company string, in AuditDocInput) (*auditdoc.Doc, error) {

	var findings []auditdoc.Finding

	// 1) 结账前体检的阻断项与警告项
	h, err := s.db.CheckPeriodHealth(ctx, k)
	if err == nil && h != nil {
		for _, it := range h.Items {
			if it.Level == sqlite.HealthOK {
				continue
			}
			level := "medium"
			if it.Level == sqlite.HealthError {
				level = "high"
			}
			findings = append(findings, auditdoc.Finding{
				Title: it.Title, Level: level, Detail: it.Detail,
				Suggestion: healthSuggestion(it.Key), Source: "结账前体检",
			})
		}
	}

	// 2) 底稿与证据链的缺口
	w, err := s.Workpaper(ctx, k)
	if err != nil {
		return nil, err
	}
	ev, err := s.Evidence(ctx, k)
	if err != nil {
		return nil, err
	}
	if ev.Unsupported > 0 {
		findings = append(findings, auditdoc.Finding{
			Level: "high", Title: "审计结论缺少依据",
			Detail: fmt.Sprintf("本期有 %d 项结论还没有附任何依据。", ev.Unsupported),
			Suggestion: "补齐折旧计算表、对账单、函证回函等原始资料，" +
				"把结论与资料挂上关系（审计底稿 → 审计证据链）",
			Source: "审计证据链",
		})
	}
	if ev.Broken > 0 {
		findings = append(findings, auditdoc.Finding{
			Level: "high", Title: "审计依据的原件已找不到",
			Detail:     fmt.Sprintf("有 %d 项结论的依据原件被删除或文件丢失。", ev.Broken),
			Suggestion: "重新归档原件；确实无法取得的，改写成文字说明并注明原因",
			Source:     "审计证据链",
		})
	}
	if w.Materiality == nil {
		findings = append(findings, auditdoc.Finding{
			Level: "medium", Title: "未确定重要性水平",
			Detail:     "本期底稿里没有重要性水平，无法判断未更正错报是否重大。",
			Suggestion: "在审计底稿中确定整体重要性、实际执行重要性与明显微小错报临界值",
			Source:     "审计底稿",
		})
	} else if !w.Misstatements.Total.IsZero() &&
		w.Misstatements.Total >= w.Materiality.Overall {
		findings = append(findings, auditdoc.Finding{
			Level: "high", Title: "未更正错报已达到整体重要性",
			Detail: fmt.Sprintf("未更正错报合计 %s，整体重要性 %s。",
				w.Misstatements.Total, w.Materiality.Overall),
			Suggestion: "调整这些错报（在审计底稿里生成调整凭证），" +
				"或在报告中说明不予调整的理由",
			Source: "审计底稿",
		})
	}
	for _, a := range w.Adjustments {
		if a.Posted || strings.TrimSpace(a.Evidence) != "" {
			continue
		}
		findings = append(findings, auditdoc.Finding{
			Level: "medium", Title: "审计调整未附证据",
			Detail: fmt.Sprintf("调整 %s「%s」（%s）已经登记，但没有附证据来源。",
				a.Code, a.Summary, a.Amount),
			Suggestion: "在审计证据链里为这笔调整附上折旧计算表、对账单等原始资料",
			Source:     "审计底稿",
		})
	}

	// 3) 现金为负这类「老板看得懂」的问题，由体检覆盖；这里补一条账龄提醒
	if aging, err := s.AgingReport(ctx, AgingRequest{
		AsOf: endOf(k).String(), AccountPrefix: "1122",
	}); err == nil && aging != nil && aging.Total.IsPositive() && aging.Over90.IsPositive() {
		findings = append(findings, auditdoc.Finding{
			Level: "medium", Title: "应收账款账龄偏长",
			Detail: fmt.Sprintf("应收账款合计 %s，其中账龄 90 天以上 %s（占 %s）。",
				aging.Total, aging.Over90, shareText(aging.Over90, aging.Total)),
			Suggestion: "建立按账龄的催收与对账机制；对长期挂账的款项评估坏账风险，" +
				"必要时计提坏账准备并在报表附注中说明",
			Source: "账龄分析（应收账款）",
		})
	}

	doc, err := auditdoc.Management(auditdoc.ManagementInput{
		Company: company, Period: k.String(),
		FirmName: in.FirmName, ReportNo: in.ReportNo, ReportDate: in.ReportDate,
		Findings: findings,
		Scope: []string{
			"查阅本期账簿、凭证与财务报表",
			"复核财务报表勾稽关系与科目余额",
			"检查往来款项、货币资金与固定资产",
			"复核期末结转与账期结算过程",
		},
	})
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// healthSuggestion 把体检项翻成一句可执行的建议。
func healthSuggestion(key string) string {
	switch key {
	case "trial_balance":
		return "核对本期发生额与余额，找出不平的凭证后更正（借贷不平衡不能靠尾差抹平）"
	case "draft_vouchers":
		return "确认这些草稿是否都要入账：结账时会自动过账，不要的一并删掉"
	case "voucher_sequence":
		return "检查凭证断号：断号可能是删过凭证，也可能是并发录入，查明后在日志里留痕"
	case "balance_sheet":
		return "按提示的勾稽关系逐项核对资产负债表"
	case "cash_negative":
		return "现金余额为负说明现金日记账与实存对不上，核对备用金借支与未入账的付款"
	case "contact_direction":
		return "往来方向异常（应收出现贷方余额等）通常是挂错科目，核对后做重分类调整"
	default:
		return "按体检提示的具体数字核对相关账簿"
	}
}

// shareText 返回 a 占 b 的百分比文字。
//
// 用整数运算而不是浮点：报表文字里出现 33.333333% 很不专业，
// 而这里只需要一个量级判断。
func shareText(a, b money.Money) string {
	if b.IsZero() {
		return "—"
	}
	return fmt.Sprintf("%d%%", int64(a)*100/int64(b))
}

// endOf 返回某期间的最后一天。
func endOf(k period.Key) calendar.Date {
	d, err := calendar.New(k.Year, k.Month, calendar.DaysInMonth(k.Year, k.Month))
	if err != nil {
		return calendar.Date{Year: k.Year, Month: k.Month, Day: 1}
	}
	return d
}

// auditDocView 把领域文书翻成界面形状（含纯文本全文）。
func auditDocView(d *auditdoc.Doc) *AuditDocView {
	v := &AuditDocView{
		Kind: string(d.Kind), KindLabel: d.Kind.Label(), Title: d.Title,
		Company: d.Company, Period: d.Period,
		Opinion: string(d.Opinion), OpinionStr: d.OpinionStr,
		Missing: []string{}, Draft: true, Submittable: false,
		Signature: d.Signature, PolicyNote: d.PolicyNote, Concludes: d.Concludes,
		CanIssue: d.CanIssue(),
	}
	for _, s := range d.Sections {
		v.Sections = append(v.Sections, AuditDocSectionView{
			No: s.No, Title: s.Title, Body: s.Body, Source: s.Source,
		})
	}
	for _, f := range d.Inputs {
		v.Inputs = append(v.Inputs, AuditDocInputView{
			Key: f.Key, Label: f.Label, Hint: f.Hint,
			Value: f.Value, FromBook: f.FromBook,
		})
	}
	v.Missing = append(v.Missing, d.Missing...)
	v.FullText = renderAuditDoc(d)
	return v
}

// renderAuditDoc 把文书渲染成可打印 / 可复制的纯文本。
//
// ★ 待补事项要印在最前面。
//
// 一份「看起来已经写好了」的草稿被打印出去，比打印不出来更糟：
// 用户会拿着它去盖章。所以缺什么必须印在抬头之前。
func renderAuditDoc(d *auditdoc.Doc) string {
	var b strings.Builder
	if len(d.Missing) > 0 {
		b.WriteString("【草稿 — 尚有事项待补，请勿签发】\n")
		for i, m := range d.Missing {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, m)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%s\n\n", d.Title)
	fmt.Fprintf(&b, "%s：\n%s\n\n", d.Company, d.Period)
	for _, s := range d.Sections {
		fmt.Fprintf(&b, "%s、%s\n%s\n\n", s.No, s.Title, s.Body)
	}
	fmt.Fprintf(&b, "%s\n", d.Signature)
	return b.String()
}

// AuditDocKindOption 是文书种类选项。
type AuditDocKindOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// AuditDocKindOptions 返回三种文书。
func (s *Service) AuditDocKindOptions() []AuditDocKindOption {
	out := make([]AuditDocKindOption, 0, len(auditdoc.AllKinds))
	for _, k := range auditdoc.AllKinds {
		out = append(out, AuditDocKindOption{Value: string(k), Label: k.Label()})
	}
	return out
}

// OpinionOption 是意见类型选项（带「要不要写基础段」的说明）。
type OpinionOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	// Hint 说明这种意见什么时候用。
	Hint string `json:"hint"`
}

// OpinionOptions 返回四种审计意见。
func (s *Service) OpinionOptions() []OpinionOption {
	hints := map[auditdoc.Opinion]string{
		auditdoc.OpinionUnqualified: "财务报表在所有重大方面公允反映（未被证伪时用）",
		auditdoc.OpinionQualified:   "错报单独或汇总起来影响重大，但不具有广泛性",
		auditdoc.OpinionAdverse:     "错报影响重大且具有广泛性 —— 报表整体不可信",
		auditdoc.OpinionDisclaimer:  "无法获取充分适当的审计证据，且影响重大而广泛",
	}
	out := make([]OpinionOption, 0, len(auditdoc.AllOpinions))
	for _, o := range auditdoc.AllOpinions {
		out = append(out, OpinionOption{
			Value: string(o), Label: o.Label(), Hint: hints[o],
		})
	}
	return out
}

// ShareholderPaidView 是账套里某位股东在「实收资本」上的余额。
type ShareholderPaidView struct {
	Name string      `json:"name"`
	Paid money.Money `json:"paid"`
}

// ShareholderPaid 返回账上各股东的实缴出资（供验资报告表单预填）。
func (s *Service) ShareholderPaid(ctx context.Context, year, month int) ([]ShareholderPaidView, error) {
	k := period.NewKey(year, month)
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", year, month)
	}
	rows, err := s.db.Reports().ContactBalances(ctx, k, "3001")
	if err != nil {
		return nil, err
	}
	out := []ShareholderPaidView{}
	for _, r := range rows {
		if r.AccountCode != "3001" {
			continue
		}
		// 贷方净额（Closing 是借−贷，实收资本正常为贷方 → 取负号）。
		// 异常方向原样带出去，让界面与验资报告都能看见。
		out = append(out, ShareholderPaidView{Name: r.ContactName, Paid: r.Closing.Neg()})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 给 AI 用的紧凑形状
// ---------------------------------------------------------------------------
//
// ★ 与界面读的是**同一个** AuditDoc：同一条生成路径、同一份事实。
// 模型不能替注册会计师形成意见，所以它读到的也只能是草稿，
// 而且渲染出来的文本里会反复说清「草稿、待签字盖章」。

// AuditDocBrief 生成给 AI 看的文书草稿。
func (s *Service) AuditDocBrief(ctx context.Context, kind string,
	year, month int) (*aiprovider.AuditDocBrief, error) {

	v, err := s.AuditDoc(ctx, AuditDocInput{Kind: kind, Year: year, Month: month})
	if err != nil {
		return nil, err
	}
	out := &aiprovider.AuditDocBrief{
		Kind: v.Kind, KindLabel: v.KindLabel, Title: v.Title,
		Company: v.Company, Period: v.Period, OpinionStr: v.OpinionStr,
		Missing: v.Missing, CanIssue: v.CanIssue,
		Signature: v.Signature, PolicyNote: v.PolicyNote, FullText: v.FullText,
	}
	for _, sec := range v.Sections {
		out.Sections = append(out.Sections, aiprovider.AuditDocBriefSection{
			No: sec.No, Title: sec.Title, Body: sec.Body, Source: sec.Source,
		})
	}
	return out, nil
}
