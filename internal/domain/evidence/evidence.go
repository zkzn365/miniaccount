// Package evidence 实现审计证据链：让底稿上的每一句结论都能追溯到
// 具体的资料。
//
// # 为什么这件事单独做一层
//
// 审计底稿的价值不在「写了什么结论」，而在**结论后面站着什么**。
// 一句「少提折旧 3,000.00」如果孤零零躺在底稿上，复核人无法判断
// 它是算出来的、猜出来的，还是抄来的 —— 他只能选择相信或者不信。
// 而审计这个行业恰好建立在「不相信」上。
//
// 所以每一条需要判断的结论都要挂上它的依据，而且依据分两类：
//
//	文件类（attachment）  发票 PDF、折旧计算表、合同扫描件 —— 有实体
//	单据类（voucher/invoice/bank_flow/expense）  账套里已有的单据 —— 有 id
//	外部类（external）    纸质资料、口头说明、函证回函 —— 只有文字描述
//
// # 三种「缺证据」都要说得出名字
//
//  1. **没有证据**：结论一条依据都没有。这是最常见的漏洞。
//  2. **证据丢了**：关联还在，但附件文件实体不见了（用户清理过 .files）。
//  3. **证据悬空**：关联指向的凭证/发票被删了。
//
// 三者都不能只是「静默通过」——底稿上写着「已取得折旧计算表」，
// 而文件早就不在了，比一开始就没写更糟：它提供了一个**假的确定感**。
package evidence

import (
	"fmt"
	"strings"
	"time"
)

// OwnerType 是被证明的对象（结论挂在哪）。
type OwnerType string

// 结论的种类。
const (
	// OwnerMateriality 是重要性水平 —— 它的依据是「为什么选这个基准与比例」。
	OwnerMateriality OwnerType = "materiality"
	// OwnerAdjustment 是一笔审计调整 —— 它的依据决定这笔调整该不该做。
	OwnerAdjustment OwnerType = "adjustment"
	// OwnerConclusion 是一期底稿的整体结论（未更正错报是否重大等）。
	OwnerConclusion OwnerType = "conclusion"
)

// AllOwnerTypes 列出全部结论种类。
var AllOwnerTypes = []OwnerType{OwnerMateriality, OwnerAdjustment, OwnerConclusion}

// Valid 报告种类是否已知。
func (t OwnerType) Valid() bool {
	for _, x := range AllOwnerTypes {
		if x == t {
			return true
		}
	}
	return false
}

// Label 返回中文名。
func (t OwnerType) Label() string {
	switch t {
	case OwnerMateriality:
		return "重要性水平"
	case OwnerAdjustment:
		return "审计调整"
	case OwnerConclusion:
		return "底稿结论"
	default:
		return string(t)
	}
}

// RefKind 是证据的种类。
type RefKind string

// 证据种类。
const (
	// RefAttachment 是本账套里的附件（有实体文件，按 sha256 内容寻址）。
	RefAttachment RefKind = "attachment"
	// RefVoucher 是账套里的一张凭证。
	RefVoucher RefKind = "voucher"
	// RefInvoice 是发票档案里的一张发票。
	RefInvoice RefKind = "invoice"
	// RefBankFlow 是一条银行流水。
	RefBankFlow RefKind = "bank_flow"
	// RefExpense 是一张报销单。
	RefExpense RefKind = "expense"
	// RefContract 是一份合同（账套里没有实体，记文字描述）。
	RefContract RefKind = "contract"
	// RefExternal 是外部资料（纸质回函、访谈记录、第三方报告…）。
	RefExternal RefKind = "external"
)

// AllRefKinds 列出全部证据种类。
var AllRefKinds = []RefKind{
	RefAttachment, RefVoucher, RefInvoice, RefBankFlow,
	RefExpense, RefContract, RefExternal,
}

// Valid 报告种类是否已知。
func (k RefKind) Valid() bool {
	for _, x := range AllRefKinds {
		if x == k {
			return true
		}
	}
	return false
}

// Label 返回中文名。
func (k RefKind) Label() string {
	switch k {
	case RefAttachment:
		return "附件"
	case RefVoucher:
		return "凭证"
	case RefInvoice:
		return "发票"
	case RefBankFlow:
		return "银行流水"
	case RefExpense:
		return "报销单"
	case RefContract:
		return "合同"
	case RefExternal:
		return "外部资料"
	default:
		return string(k)
	}
}

// HasFile 报告这种证据有没有实体文件。
//
// ★ 只有附件（以及将来的扫描件）需要检查文件实体；
// 凭证、发票这些的「实体」就是账套里的记录，它们丢失的表现是
// **记录没了**，由 store 层查 id 是否存在来判断。
func (k RefKind) HasFile() bool { return k == RefAttachment }

// Link 是一条证据关联。
type Link struct {
	ID int64
	// OwnerType / OwnerID 是被证明的结论。
	OwnerType OwnerType
	OwnerID   int64
	// RefKind 是证据种类。
	RefKind RefKind
	// RefID 是单据 id（文件类为 0）。
	RefID int64
	// RefLabel 是**当时**这份资料的样子（如「记-2025-03-0007 计提折旧 12,000.00」）。
	//
	// ★ 存快照而不是每次现查：凭证以后可能被红冲、发票可能被改，
	// 而底稿要回答的是「当时据以判断的是哪一份」。
	// 只存 id 的话，回头看会得到一份与当时不同的资料，而底稿上写着
	// 「依据：凭证 #7」—— 谁也不知道那是不是同一张。
	RefLabel string
	// SHA256 / FileName 是附件类证据的文件信息。
	SHA256   string
	FileName string
	FileSize int64
	// Note 是「这份资料说明了什么」。
	Note string
	// LinkedBy / LinkedAt 是谁什么时候挂上的。
	LinkedBy string
	LinkedAt string
	// Missing 为真表示资料已经找不到了（文件实体丢失 / 单据被删）。
	//
	// ★ 由 store 层查证后填，不落库：落库的话它会随时间失真，
	// 而「证据还在不在」这件事必须每次现查。
	Missing bool
}

// Validate 检查一条关联是否成立。
func (l Link) Validate() error {
	if !l.OwnerType.Valid() {
		return fmt.Errorf("%w: %q", ErrBadOwnerType, l.OwnerType)
	}
	if l.OwnerID <= 0 {
		return fmt.Errorf("%w: 结论 id 必须大于 0", ErrBadOwnerID)
	}
	if !l.RefKind.Valid() {
		return fmt.Errorf("%w: %q", ErrBadRefKind, l.RefKind)
	}
	switch l.RefKind {
	case RefAttachment:
		// 附件必须带 sha256 —— 没有它就不是「一份可以核对的文件」，
		// 只是一句描述。想写描述请用「外部资料」那一类。
		if strings.TrimSpace(l.SHA256) == "" {
			return fmt.Errorf("%w：附件类证据必须带有文件（sha256 为空）", ErrIncomplete)
		}
		if strings.TrimSpace(l.FileName) == "" {
			return fmt.Errorf("%w：附件类证据缺少文件名", ErrIncomplete)
		}
	case RefVoucher, RefInvoice, RefBankFlow, RefExpense:
		if l.RefID <= 0 {
			return fmt.Errorf("%w：%s类证据必须给出单据 id", ErrIncomplete, l.RefKind.Label())
		}
	}
	// ★ 不论哪一类都要有说明。
	//
	// 「依据：折旧计算表.pdf」本身说明不了什么 —— 复核人要的是
	// 「这份表说明了什么」。这一句是底稿的灵魂，不能省。
	if strings.TrimSpace(l.RefLabel) == "" && strings.TrimSpace(l.Note) == "" {
		return fmt.Errorf("%w：要写清这份资料是什么（资料名称或它说明了什么）", ErrIncomplete)
	}
	return nil
}

// Identity 返回这条关联的判重键。
//
// 同一份资料对同一个结论只挂一次：重复挂不会更可信，
// 只会让「有几份证据」这个数字虚高。
func (l Link) Identity() string {
	if l.RefKind == RefAttachment {
		return fmt.Sprintf("%s/%d/%s/%s", l.OwnerType, l.OwnerID, l.RefKind, l.SHA256)
	}
	return fmt.Sprintf("%s/%d/%s/%d", l.OwnerType, l.OwnerID, l.RefKind, l.RefID)
}

// Chain 是一个结论的证据链。
type Chain struct {
	OwnerType OwnerType
	OwnerID   int64
	// Title 是这个结论的一句话（如「ADJ-202503-001 补提折旧 3,000.00」）。
	Title string
	Links []Link
}

// Total 返回证据条数。
func (c Chain) Total() int { return len(c.Links) }

// Files 返回附件类证据的条数。
func (c Chain) Files() int {
	n := 0
	for _, l := range c.Links {
		if l.RefKind.HasFile() {
			n++
		}
	}
	return n
}

// Documents 返回单据类证据的条数。
func (c Chain) Documents() int { return c.Total() - c.Files() }

// Missing 返回已经找不到的资料。
func (c Chain) Missing() []Link {
	var out []Link
	for _, l := range c.Links {
		if l.Missing {
			out = append(out, l)
		}
	}
	return out
}

// Empty 报告这个结论一条证据都没有。
func (c Chain) Empty() bool { return len(c.Links) == 0 }

// Concludes 给出一句结论。
//
// ★ 「没有证据」与「证据丢了」要分开说：前者是活没干完，
// 后者是干了但资料没保住 —— 补的方式完全不同。
func (c Chain) Concludes() string {
	if c.Empty() {
		return fmt.Sprintf("%s还没有附任何依据 —— 结论要有出处，复核人才判断得了。",
			c.OwnerType.Label())
	}
	if n := len(c.Missing()); n > 0 {
		return fmt.Sprintf("有 %d 份依据现在找不到了（原件被删或文件丢失）—— "+
			"底稿上写着它，但它已经不在账套里，请补上或注明。", n)
	}
	return fmt.Sprintf("依据齐全：%d 份（附件 %d、单据 %d）。",
		c.Total(), c.Files(), c.Documents())
}

// ChainSet 是一期底稿的全部证据链。
type ChainSet struct {
	Period string
	Chains []Chain
}

// Unsupported 返回一条证据都没有的结论。
func (s ChainSet) Unsupported() []Chain {
	var out []Chain
	for _, c := range s.Chains {
		if c.Empty() {
			out = append(out, c)
		}
	}
	return out
}

// Broken 返回有资料找不到的结论。
func (s ChainSet) Broken() []Chain {
	var out []Chain
	for _, c := range s.Chains {
		if len(c.Missing()) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// Concludes 给出一期底稿的证据链结论。
func (s ChainSet) Concludes() string {
	empty, broken := len(s.Unsupported()), len(s.Broken())
	switch {
	case len(s.Chains) == 0:
		return "本期底稿还没有需要举证的结论。"
	case empty == 0 && broken == 0:
		return fmt.Sprintf("%d 项结论的依据都齐。", len(s.Chains))
	case empty > 0 && broken > 0:
		return fmt.Sprintf("%d 项结论还没有依据，另有 %d 项的依据已经找不到 —— "+
			"这两类都要在出报告前处理掉。", empty, broken)
	case empty > 0:
		return fmt.Sprintf("%d 项结论还没有附依据 —— 没有出处的结论复核不了。", empty)
	default:
		return fmt.Sprintf("%d 项结论的依据已经找不到原件，请补上或注明。", broken)
	}
}

// NewLink 造一条带时间与操作人的关联。
func NewLink(l Link, by string, now time.Time) Link {
	l.LinkedBy = strings.TrimSpace(by)
	l.LinkedAt = now.UTC().Format(time.RFC3339Nano)
	return l
}

// ---------------------------------------------------------------------------
// 错误
// ---------------------------------------------------------------------------

// 证据链的错误。
var (
	ErrBadOwnerType = fmt.Errorf("evidence: 不认识的结论种类")
	ErrBadOwnerID   = fmt.Errorf("evidence: 结论 id 非法")
	ErrBadRefKind   = fmt.Errorf("evidence: 不认识的证据种类")
	ErrIncomplete   = fmt.Errorf("evidence: 证据不完整")
	ErrDuplicate    = fmt.Errorf("evidence: 这份资料已经挂在这个结论下了")
)

// PeriodOwnerID 给出「按期挂」的结论 id。
//
// 重要性水平与底稿结论是**一期一条**，没有自己的表与主键，
// 用 202503 这样的期间号当 id：它天然唯一、可读，
// 而且与底稿页上显示的期间是同一个数，查起来对得上。
// 审计调整则用它自己的行 id。
func PeriodOwnerID(year, month int) int64 { return int64(year*100 + month) }
