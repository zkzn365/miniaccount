package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"time"

	"miniaccount/internal/ai/aiprovider"
	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/evidence"
	"miniaccount/internal/domain/invoice"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/workpaper"
)

// ---------------------------------------------------------------------------
// 审计证据链
//
// ★ 界面与 AI 读的是**同一个** Evidence()：同一条存在性检查。
//
// 原来 AI 走的是 sqlite.AIRepo 里的另一份实现，它看不到文件系统，
// 也就报不出「原件已找不到」—— 于是界面上写着「另有 1 项的依据已经找不到」，
// 而模型对同一期说「这笔调整有依据」。两处各算一遍迟早会分叉，
// 而这一处恰恰是证据链存在的唯一理由。
// ---------------------------------------------------------------------------
//
// 底稿上的每一句结论都要有出处。这一层做三件事：
//
//	1. 把一期的结论与它们的依据拼成证据链（读）；
//	2. 挂上/摘掉一份依据（写，需人工确认）；
//	3. **点出缺证据的地方** —— 没有依据的结论、以及依据已经找不到的结论。
//
// ★ 第 3 件才是重点。
//
// 挂证据这件事本身不难；难的是「底稿上写着已取得折旧计算表，
// 而那份文件早就不在了」这种状态没人发现 —— 它比一开始就没写更糟，
// 因为它提供了一个假的确定感。所以证据的**存在性每次现查**（见 Missing）。

// EvidenceLinkView 是一条证据。
type EvidenceLinkView struct {
	ID           int64  `json:"id"`
	RefKind      string `json:"refKind"`
	RefKindLabel string `json:"refKindLabel"`
	// RefID 是单据 id（附件类为 0）。
	RefID int64 `json:"refId"`
	// RefLabel 是当时那份资料的样子（快照）。
	RefLabel string `json:"refLabel"`
	Note     string `json:"note"`
	// Hash / FileName / FileSize 是附件类证据的文件信息。
	Hash     string `json:"hash"`
	FileName string `json:"fileName"`
	FileSize int64  `json:"fileSize"`
	// HasFile 为真表示这类证据有实体文件（界面才给「打开」按钮）。
	HasFile bool `json:"hasFile"`
	// Missing 为真表示这份资料**现在找不到了**。
	Missing bool `json:"missing"`
	// Path 是附件的磁盘路径（Missing 时为空）。
	Path     string `json:"path"`
	LinkedBy string `json:"linkedBy"`
	LinkedAt string `json:"linkedAt"`
}

// EvidenceChainView 是一个结论的证据链。
type EvidenceChainView struct {
	OwnerType      string `json:"ownerType"`
	OwnerTypeLabel string `json:"ownerTypeLabel"`
	OwnerID        int64  `json:"ownerId"`
	// Title 是这个结论的一句话。
	Title string `json:"title"`
	// LinkTo 是界面上应该跳到哪儿看这个结论（如 audit_adjustment/12）。
	LinkTo    string `json:"linkTo"`
	Total     int    `json:"total"`
	Files     int    `json:"files"`
	Documents int    `json:"documents"`
	// Concludes 是这个结论的证据情况（齐 / 没有 / 丢了）。
	Concludes string             `json:"concludes"`
	Links     []EvidenceLinkView `json:"links"`
}

// EvidenceView 是一期的证据链全貌。
type EvidenceView struct {
	Period string              `json:"period"`
	Chains []EvidenceChainView `json:"chains"`
	// Unsupported / Broken 是两类问题各自的条数。
	Unsupported int `json:"unsupported"`
	Broken      int `json:"broken"`
	// Concludes 是一句话总结。
	Concludes string `json:"concludes"`
	// NextActions 是可以照着做的下一步（没有证据时最需要）。
	NextActions []string `json:"nextActions"`
}

// RefKindOption 是证据种类的一个选项（给界面下拉）。
type RefKindOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// RefKindOptions 返回证据种类的选项（给界面下拉）。
func (s *Service) RefKindOptions() []RefKindOption {
	out := make([]RefKindOption, 0, len(evidence.AllRefKinds))
	for _, k := range evidence.AllRefKinds {
		out = append(out, RefKindOption{Value: string(k), Label: k.Label()})
	}
	return out
}

// Evidence 返回某期的证据链。
func (s *Service) Evidence(ctx context.Context, k period.Key) (*EvidenceView, error) {
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", k.Year, k.Month)
	}
	links, err := s.db.Evidence().PeriodLinks(ctx, k)
	if err != nil {
		return nil, err
	}
	adjs, err := s.db.Workpapers().Adjustments(ctx, k)
	if err != nil {
		return nil, err
	}
	m, err := s.db.Workpapers().Materiality(ctx, k)
	if err != nil {
		return nil, err
	}

	// 按结论分组
	byOwner := map[string][]evidence.Link{}
	for _, l := range links {
		key := fmt.Sprintf("%s/%d", l.OwnerType, l.OwnerID)
		byOwner[key] = append(byOwner[key], l)
	}

	periodID := evidence.PeriodOwnerID(k.Year, k.Month)
	out := &EvidenceView{Period: k.String(), Chains: []EvidenceChainView{}}

	// 1. 重要性水平
	if m != nil {
		title := fmt.Sprintf("重要性水平：%s %s × %s = %s",
			m.Benchmark.Label(), m.BenchmarkAmount, workpaper.Percent(m.RatePPM), m.Overall())
		out.Chains = append(out.Chains,
			s.chainView(ctx, evidence.OwnerMateriality, periodID, title, "workpaper"))
	}
	// 2. 底稿结论
	sum := workpaper.Misstatements(adjs, m)
	out.Chains = append(out.Chains, s.chainView(ctx, evidence.OwnerConclusion, periodID,
		fmt.Sprintf("本期底稿结论：未更正错报合计 %s", sum.Total), "workpaper"))
	// 3. 每笔审计调整
	for _, a := range adjs {
		out.Chains = append(out.Chains, s.chainView(ctx, evidence.OwnerAdjustment, a.ID,
			fmt.Sprintf("%s %s %s", a.Code, a.Summary, a.Amount()),
			fmt.Sprintf("adjustment/%d", a.ID)))
	}

	set := evidence.ChainSet{Period: k.String()}
	for i := range out.Chains {
		out.Chains[i].Links = s.linkViews(ctx, byOwner[fmt.Sprintf("%s/%d",
			out.Chains[i].OwnerType, out.Chains[i].OwnerID)])
		// ★ 份数与结论都由**领域层**算，服务层不自己数一遍：
		// 两边各数一次，迟早会出现「链上显示 2 份、结论说 1 份」
		c := chainOf(out.Chains[i])
		out.Chains[i].Total = c.Total()
		out.Chains[i].Files = c.Files()
		out.Chains[i].Documents = c.Documents()
		out.Chains[i].Concludes = c.Concludes()
		set.Chains = append(set.Chains, c)
	}
	out.Unsupported = len(set.Unsupported())
	// Broken 按**结论条数**算（与领域层一致），不是按丢失资料的份数
	out.Broken = len(set.Broken())
	out.Concludes = set.Concludes()
	out.NextActions = evidenceNextActions(out)
	return out, nil
}

// EvidenceInput 是挂一份依据的入参。
type EvidenceInput struct {
	OwnerType string `json:"ownerType"`
	OwnerID   int64  `json:"ownerId"`
	RefKind   string `json:"refKind"`
	RefID     int64  `json:"refId"`
	RefLabel  string `json:"refLabel"`
	Note      string `json:"note"`
	// Hash 非空表示引用账套里已有的一份附件（内容寻址，天然去重）。
	Hash string `json:"hash"`
	// FileName / Data 用于上传一份新的附件。
	FileName string `json:"fileName"`
	Data     []byte `json:"-"`
	// By 是操作人（底稿要留痕：这份资料是谁挂上的）。
	By string `json:"by"`
}

// AddEvidence 把一份资料挂到一个结论下。
func (s *Service) AddEvidence(ctx context.Context, in EvidenceInput) (*EvidenceView, error) {
	kind := evidence.RefKind(strings.TrimSpace(in.RefKind))
	owner := evidence.OwnerType(strings.TrimSpace(in.OwnerType))
	link := evidence.Link{
		OwnerType: owner, OwnerID: in.OwnerID, RefKind: kind,
		RefID: in.RefID, RefLabel: strings.TrimSpace(in.RefLabel),
		Note: strings.TrimSpace(in.Note), SHA256: in.Hash,
	}
	if strings.TrimSpace(in.By) == "" {
		return nil, errors.New("请填写操作人 —— 底稿要记清这份依据是谁挂上的")
	}

	switch kind {
	case evidence.RefAttachment:
		// 两种来源：引用已有附件（给 hash），或新上传一份（给内容）
		if len(in.Data) > 0 {
			store, err := s.Attachments()
			if err != nil {
				return nil, err
			}
			blob, err := store.Put(in.Data, in.FileName)
			if err != nil {
				return nil, err
			}
			link.SHA256 = blob.SHA256
			link.FileName = blob.FileName
			link.FileSize = blob.Size
		} else {
			store, err := s.Attachments()
			if err != nil {
				return nil, err
			}
			name := strings.TrimSpace(in.FileName)
			if name == "" {
				name = "附件 " + shortHash(in.Hash)
			}
			// 引用已有附件时确认它真的在，否则挂上去就是一条
			// 立刻就残缺的证据
			if !store.Exists(in.Hash) {
				return nil, errors.New("这份附件不在账套里（文件可能已被清理）")
			}
			link.FileName = name
			link.FileSize = store.Size(in.Hash)
		}
		if link.RefLabel == "" {
			link.RefLabel = link.FileName
		}
	default:
		// 单据类：核对它真的存在，并把**当时的**样子记下来
		label, err := s.describeRef(ctx, kind, in.RefID)
		if err != nil {
			return nil, err
		}
		if link.RefLabel == "" {
			link.RefLabel = label
		}
	}
	link = evidence.NewLink(link, in.By, time.Now())
	if err := link.Validate(); err != nil {
		return nil, err
	}
	// 结论必须真的存在 —— 否则证据挂在一个查不到的地方
	k, err := s.ownerPeriodOf(ctx, link.OwnerType, link.OwnerID)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Evidence().Add(ctx, link); err != nil {
		if errors.Is(err, evidence.ErrDuplicate) {
			return nil, errors.New("这份资料已经挂在这个结论下了 —— 同一份资料算一份依据")
		}
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionEvidenceAdd,
		Summary: fmt.Sprintf("为%s挂依据：%s", link.OwnerType.Label(), link.RefLabel),
		Entity:  "audit_evidence", EntityID: fmt.Sprintf("%d", link.OwnerID),
		Operator: link.LinkedBy,
		Detail: map[string]any{
			"期间": k.String(), "结论": link.OwnerType.Label(),
			"结论 id": link.OwnerID, "证据种类": link.RefKind.Label(),
			"资料": link.RefLabel, "说明": link.Note,
			"文件": link.FileName, "sha256": link.SHA256,
		},
	})
	return s.Evidence(ctx, k)
}

// DeleteEvidence 摘掉一条依据。
func (s *Service) DeleteEvidence(ctx context.Context, id int64, by string) (*EvidenceView, error) {
	l, err := s.db.Evidence().Link(ctx, id)
	if err != nil {
		return nil, err
	}
	k, err := s.ownerPeriodOf(ctx, l.OwnerType, l.OwnerID)
	if err != nil {
		return nil, err
	}
	if err := s.db.Evidence().Delete(ctx, id); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionEvidenceDelete,
		Summary: fmt.Sprintf("摘掉%s的依据：%s", l.OwnerType.Label(), l.RefLabel),
		Entity:  "audit_evidence", EntityID: fmt.Sprintf("%d", l.OwnerID),
		Operator: strings.TrimSpace(by),
		Detail: map[string]any{
			"期间": k.String(), "结论": l.OwnerType.Label(),
			"资料": l.RefLabel, "文件": l.FileName,
		},
	})
	return s.Evidence(ctx, k)
}

// ownerPeriodOf 找出一条结论属于哪一期，并确认这个结论真的存在。
func (s *Service) ownerPeriodOf(ctx context.Context, owner evidence.OwnerType,
	ownerID int64) (period.Key, error) {

	switch owner {
	case evidence.OwnerAdjustment:
		a, err := s.db.Workpapers().Adjustment(ctx, ownerID)
		if err != nil {
			return period.Key{}, fmt.Errorf("要挂依据的审计调整不存在：%w", err)
		}
		return a.Period, nil
	case evidence.OwnerMateriality:
		y, m := int(ownerID/100), int(ownerID%100)
		k := period.NewKey(y, m)
		if !k.Valid() {
			return k, fmt.Errorf("evidence: 结论 id %d 里读不出会计期间", ownerID)
		}
		mt, err := s.db.Workpapers().Materiality(ctx, k)
		if err != nil {
			return k, err
		}
		if mt == nil {
			return k, fmt.Errorf("本期还没有确定重要性水平，先定门槛再挂依据")
		}
		return k, nil
	case evidence.OwnerConclusion:
		y, m := int(ownerID/100), int(ownerID%100)
		k := period.NewKey(y, m)
		if !k.Valid() {
			return k, fmt.Errorf("evidence: 结论 id %d 里读不出会计期间", ownerID)
		}
		return k, nil
	}
	return period.Key{}, fmt.Errorf("%w: %q", evidence.ErrBadOwnerType, owner)
}

// describeRef 给一份单据类证据生成「当时的样子」。
//
// ★ 名字从账套里查，不让界面或模型自己写：
// 写一个「听起来对」的名字，事后按底稿去核对会核到另一份单据上。
func (s *Service) describeRef(ctx context.Context, kind evidence.RefKind, id int64) (string, error) {
	switch kind {
	case evidence.RefVoucher:
		v, err := s.db.Vouchers().Get(ctx, id)
		if err != nil {
			return "", fmt.Errorf("依据里的凭证不存在：%w", err)
		}
		return fmt.Sprintf("%s %s %s", voucherLabel(v.No, v.ID), v.Remark, v.Amount()), nil
	case evidence.RefInvoice:
		inv, err := s.db.Invoices().GetInvoice(ctx, id)
		if err != nil {
			return "", fmt.Errorf("依据里的发票不存在：%w", err)
		}
		// 进项票的对方是销方，销项票的对方是购方 —— 别写反，
		// 底稿上「这张票是谁开给谁的」是核对的第一眼
		other := inv.SellerName
		if inv.Direction == invoice.DirOutput {
			other = inv.BuyerName
		}
		return fmt.Sprintf("%s %s 价税合计 %s", invoiceNo(inv.Code, inv.Number), other, inv.TotalAmount), nil
	case evidence.RefBankFlow:
		f, err := s.db.Bank().GetFlow(ctx, id)
		if err != nil {
			return "", fmt.Errorf("依据里的银行流水不存在：%w", err)
		}
		return fmt.Sprintf("%s %s %s %s", f.TxnDate, f.CounterpartyName, f.Summary, f.Amount), nil
	case evidence.RefExpense:
		c, err := s.db.Claims().GetClaim(ctx, id)
		if err != nil {
			return "", fmt.Errorf("依据里的报销单不存在：%w", err)
		}
		return fmt.Sprintf("%s %s %s", c.Code, c.Reason, c.TotalAmount), nil
	}
	return "", nil
}

// chainView 先占个位：链接在填充阶段补上。
func (s *Service) chainView(_ context.Context, owner evidence.OwnerType, ownerID int64,
	title, linkTo string) EvidenceChainView {

	return EvidenceChainView{
		OwnerType: string(owner), OwnerTypeLabel: owner.Label(),
		OwnerID: ownerID, Title: title, LinkTo: linkTo,
		Links: []EvidenceLinkView{},
	}
}

// linkViews 把领域对象翻成界面形状，并**现查**证据还在不在。
func (s *Service) linkViews(ctx context.Context, links []evidence.Link) []EvidenceLinkView {
	store, _ := s.Attachments()
	out := make([]EvidenceLinkView, 0, len(links))

	// 单据类证据的存在性批量查一次
	byKind := map[evidence.RefKind][]int64{}
	for _, l := range links {
		if !l.RefKind.HasFile() && l.RefID > 0 {
			byKind[l.RefKind] = append(byKind[l.RefKind], l.RefID)
		}
	}
	exists := map[evidence.RefKind]map[int64]bool{}
	for kind, ids := range byKind {
		m, err := s.db.Evidence().ExistingRefs(ctx, kind, ids)
		if err != nil {
			continue
		}
		exists[kind] = m
	}

	for _, l := range links {
		v := EvidenceLinkView{
			ID: l.ID, RefKind: string(l.RefKind), RefKindLabel: l.RefKind.Label(),
			RefID: l.RefID, RefLabel: l.RefLabel, Note: l.Note,
			Hash: l.SHA256, FileName: l.FileName, FileSize: l.FileSize,
			HasFile: l.RefKind.HasFile(), LinkedBy: l.LinkedBy, LinkedAt: l.LinkedAt,
		}
		switch {
		case l.RefKind.HasFile():
			if store != nil {
				v.Path = store.MustPath(l.SHA256)
				v.Missing = !store.Exists(l.SHA256)
			}
		case l.RefID > 0:
			if m := exists[l.RefKind]; m != nil && !m[l.RefID] {
				v.Missing = true
			}
		}
		if v.Missing {
			v.Path = ""
		}
		out = append(out, v)
	}
	return out
}

// chainOf 把界面形状还原成领域形状，好复用领域层的判定。
func chainOf(c EvidenceChainView) evidence.Chain {
	ch := evidence.Chain{
		OwnerType: evidence.OwnerType(c.OwnerType), OwnerID: c.OwnerID,
		Title: c.Title, Links: make([]evidence.Link, 0, len(c.Links)),
	}
	for _, l := range c.Links {
		ch.Links = append(ch.Links, evidence.Link{
			ID: l.ID, OwnerType: ch.OwnerType, OwnerID: c.OwnerID,
			RefKind: evidence.RefKind(l.RefKind), RefID: l.RefID,
			RefLabel: l.RefLabel, SHA256: l.Hash, FileName: l.FileName,
			Missing: l.Missing, Note: l.Note,
		})
	}
	return ch
}

// evidenceNextActions 给出「照着做」的下一步。
//
// 只说「有 3 项没有依据」等于把问题丢回给用户；
// 底稿这一步要告诉他去哪儿挂、挂什么。
func evidenceNextActions(v *EvidenceView) []string {
	var out []string
	if v.Unsupported > 0 {
		out = append(out, fmt.Sprintf(
			"有 %d 项结论还没有依据：在下面每条结论后面点「附上资料」——"+
				"附上折旧计算表、对账单、发票之类能支撑它的原件", v.Unsupported))
	}
	if v.Broken > 0 {
		out = append(out, fmt.Sprintf(
			"有 %d 项依据的原件已经找不到了（文件被清理或单据被删）："+
				"重新附一份，或者把那条依据改写成文字说明（外部资料）", v.Broken))
	}
	return out
}

// invoiceNo 拼出发票号（电子发票可能只有号码）。
func invoiceNo(code, number string) string {
	switch {
	case code != "" && number != "":
		return code + "-" + number
	case number != "":
		return number
	default:
		return code
	}
}

// shortHash 取 hash 的前 8 位，用于给没名字的附件兜底。
func shortHash(h string) string {
	h = strings.TrimSpace(h)
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// EvidenceBrief 生成给 AI 看的证据链（与界面同一个 Evidence()）。
func (s *Service) EvidenceBrief(ctx context.Context,
	k aiprovider.PeriodKey) (*aiprovider.EvidenceBrief, error) {

	v, err := s.Evidence(ctx, period.NewKey(k.Year, k.Month))
	if err != nil {
		return nil, err
	}
	out := &aiprovider.EvidenceBrief{
		Period: v.Period, Unsupported: v.Unsupported, Broken: v.Broken,
		Concludes: v.Concludes, Chains: []aiprovider.EvidenceChainBrief{},
	}
	for _, c := range v.Chains {
		chain := aiprovider.EvidenceChainBrief{
			Owner: c.OwnerTypeLabel, Title: c.Title,
			Files: c.Files, Documents: c.Documents,
			Items: []aiprovider.EvidenceItemBrief{},
		}
		for _, l := range c.Links {
			if l.Missing {
				chain.Missing++
			}
			chain.Items = append(chain.Items, aiprovider.EvidenceItemBrief{
				Kind: l.RefKindLabel, Label: l.RefLabel, Note: l.Note, By: l.LinkedBy,
				Missing: l.Missing,
			})
		}
		out.Chains = append(out.Chains, chain)
	}
	return out, nil
}
