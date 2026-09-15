// Package voucher 实现记账凭证聚合根。
//
// # 为什么凭证是唯一的记账入口
//
// Frappe Books 里总账分录可以来自 6 种单据（销售发票、采购发票、付款、日记账、
// 发货、收货），每种各自往总账写数据。中国实务不允许这样：《会计法》要求
// **每一笔账都必须有记账凭证**，凭证是唯一的、可归档、可签章的记账依据。
//
// 因此本工程把「凭证」设为唯一的过账来源：
//
//	银行流水 ─┐
//	工资    ─┤
//	报销    ─┼─→ 生成凭证 ─→ 过账 ─→ 总账
//	发票    ─┤
//	手工录入 ─┘
//
// 这让「流水↔凭证双向关联」「报销→凭证」「发票→凭证」三个需求
// 退化成同一套机制。
package voucher

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
)

// Word 是凭证字。中国实务按业务类型选择：
//
//	记 —— 通用转账凭证（最常用）
//	收 —— 收款凭证（借方必有现金/银行存款）
//	付 —— 付款凭证（贷方必有现金/银行存款）
//	转 —— 转账凭证（不涉及现金/银行存款）
type Word string

// 常用凭证字。
const (
	WordJi    Word = "记"
	WordShou  Word = "收"
	WordFu    Word = "付"
	WordZhuan Word = "转"
)

// AllWords 列出内置凭证字。
var AllWords = []Word{WordJi, WordShou, WordFu, WordZhuan}

// Valid 报告凭证字是否合法。
//
// 允许内置的「记收付转」，也允许用户自定义，但必须由**中文字符**组成
// 且不超过 2 字（如「银」「现」）。ASCII 字母不作为凭证字 ——
// 凭证的书写与归档都有中文规范，混入字母会让凭证字号在纸质归档时对不上。
func (w Word) Valid() bool {
	if w == "" {
		return false
	}
	rs := []rune(w)
	if len(rs) > 2 {
		return false
	}
	for _, r := range rs {
		if !isCJK(r) {
			return false
		}
	}
	return true
}

// isCJK 报告 r 是否为中日韩统一表意文字。
func isCJK(r rune) bool {
	return r >= 0x4E00 && r <= 0x9FFF
}

// Status 是凭证状态。
type Status string

// 凭证状态。
const (
	StatusDraft  Status = "draft"  // 草稿：可改可删
	StatusPosted Status = "posted" // 已过账：只读，只能红字冲销
	StatusVoided Status = "voided" // 已作废：只读
)

// Label 返回中文名。
func (s Status) Label() string {
	switch s {
	case StatusDraft:
		return "草稿"
	case StatusPosted:
		return "已过账"
	case StatusVoided:
		return "已作废"
	default:
		return string(s)
	}
}

// Source 标识凭证的来源，用于「流水↔凭证」「报销↔凭证」的双向关联。
type Source string

// 凭证来源。
const (
	SourceManual  Source = "manual"  // 手工录入
	SourceBank    Source = "bank"    // 银行流水导入
	SourceSalary  Source = "salary"  // 工资计提/发放
	SourceExpense Source = "expense" // 差旅报销
	SourceInvoice Source = "invoice" // 发票
	SourceClosing Source = "closing" // 期末结转损益
	SourceOpening Source = "opening" // 期初余额
	SourceAI      Source = "ai"      // AI 提议后人工确认
	// SourceDepreciation 是固定资产折旧计提。
	SourceDepreciation Source = "depreciation"
	// SourceAmortization 是长期待摊费用摊销。
	SourceAmortization Source = "amortization"
	// SourceAudit 是审计调整生成的凭证。
	//
	// 它与其他来源的区别在于：这张凭证后面站着一笔**调整底稿**
	// （谁调的、依据是什么、证据在哪）。查账时点开凭证要能回到那张底稿。
	SourceAudit Source = "audit"
)

// Label 返回中文名。
func (s Source) Label() string {
	switch s {
	case SourceManual:
		return "手工"
	case SourceBank:
		return "银行流水"
	case SourceSalary:
		return "工资"
	case SourceExpense:
		return "报销"
	case SourceInvoice:
		return "发票"
	case SourceClosing:
		return "期末结转"
	case SourceOpening:
		return "期初余额"
	case SourceAI:
		return "AI 建议"
	case SourceDepreciation:
		return "折旧"
	case SourceAmortization:
		return "摊销"
	case SourceAudit:
		return "审计调整"
	default:
		return string(s)
	}
}

// 凭证相关错误。
var (
	ErrBadWord        = errors.New("voucher: 非法凭证字")
	ErrBadDate        = errors.New("voucher: 凭证日期无效")
	ErrNoEntries      = errors.New("voucher: 凭证没有分录")
	ErrNotDraft       = errors.New("voucher: 只有草稿状态可以修改")
	ErrAlreadyPosted  = errors.New("voucher: 凭证已过账")
	ErrNotPosted      = errors.New("voucher: 凭证尚未过账")
	ErrAlreadyVoided  = errors.New("voucher: 凭证已作废")
	ErrMissingMaker   = errors.New("voucher: 缺少制单人")
	ErrMissingPoster  = errors.New("voucher: 缺少记账人")
	ErrSequenceBroken = errors.New("voucher: 凭证字号不连续")
	ErrDuplicateNo    = errors.New("voucher: 凭证字号重复")
	ErrFuturePeriod   = errors.New("voucher: 凭证日期晚于当前会计期间")
)

// ---------------------------------------------------------------------------
// 凭证字号
// ---------------------------------------------------------------------------

// seqWidth 是凭证序号的补零宽度。中国实务常见 4 位（0001）。
const seqWidth = 4

// FormatNo 生成凭证展示号，如 "记-2025-01-0001"。
func FormatNo(w Word, k period.Key, seq int) string {
	return fmt.Sprintf("%s-%04d-%02d-%0*d", w, k.Year, k.Month, seqWidth, seq)
}

// ParseNo 解析凭证展示号，用于导入与校验。
func ParseNo(no string) (Word, period.Key, int, error) {
	parts := strings.Split(no, "-")
	if len(parts) != 4 {
		return "", period.Key{}, 0, fmt.Errorf("%w: %q", ErrBadWord, no)
	}
	var k period.Key
	var seq int
	if _, err := fmt.Sscanf(parts[1], "%d", &k.Year); err != nil {
		return "", period.Key{}, 0, fmt.Errorf("%w: %q", ErrBadWord, no)
	}
	if _, err := fmt.Sscanf(parts[2], "%d", &k.Month); err != nil {
		return "", period.Key{}, 0, fmt.Errorf("%w: %q", ErrBadWord, no)
	}
	if _, err := fmt.Sscanf(parts[3], "%d", &seq); err != nil {
		return "", period.Key{}, 0, fmt.Errorf("%w: %q", ErrBadWord, no)
	}
	w := Word(parts[0])
	if !w.Valid() || !k.Valid() || seq < 1 {
		return "", period.Key{}, 0, fmt.Errorf("%w: %q", ErrBadWord, no)
	}
	return w, k, seq, nil
}

// ---------------------------------------------------------------------------
// Voucher
// ---------------------------------------------------------------------------

// Voucher 是一张记账凭证。
type Voucher struct {
	ID int64

	// Period 是凭证所属会计期间，由 BizDate 推导后固化，
	// 便于按期间查询而不必每次从日期算。
	Period period.Key

	Word Word
	Seq  int    // 期间内 + 凭证字内的连续序号
	No   string // 展示号，如 "记-2025-01-0001"

	BizDate     calendar.Date
	AttachCount int    // 附单据数
	Remark      string // 备注

	Status Status
	Source Source
	// SourceID 指向来源单据（银行流水 id / 报销单 id / 工资单 id …），
	// 与 Source 一起构成「凭证 ↔ 来源」的双向关联。
	SourceID *int64

	// 制单、审核、记账三个签章。中国实务要求这三个责任人可追溯。
	CreatedBy  string
	ReviewedBy string
	PostedBy   string
	PostedAt   *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time

	// Entries 是分录。复用 ledger.Entry —— 本工程里凭证分录
	// 就是过账分录，不需要中间转换类型。
	Entries []ledger.Entry

	// ReversesID 指向被本凭证冲销的原凭证（红字冲销时设置）。
	ReversesID *int64
	// VoidedBy 指向冲销本凭证的那张红字凭证。
	VoidedBy *int64

	// CreatedByAI 标记这张凭证的初稿由 AI 提议生成。
	//
	// ★ 它记录的是**来源**而不是责任：凭证仍然必须由人确认后过账，
	// CreatedBy / PostedBy 上签的始终是自然人的名字。
	// 保留这个标记是为了事后能统计「AI 提议的采纳率与准确率」，
	// 以及在审计时区分「人录的」与「AI 建议、人确认的」。
	CreatedByAI bool
}

// New 创建一张草稿凭证。
func New(w Word, bizDate calendar.Date, createdBy string) (*Voucher, error) {
	if !w.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrBadWord, w)
	}
	if !bizDate.Valid() {
		return nil, fmt.Errorf("%w: %v", ErrBadDate, bizDate)
	}
	if createdBy == "" {
		return nil, ErrMissingMaker
	}
	return &Voucher{
		Word:      w,
		BizDate:   bizDate,
		Period:    period.Key{Year: bizDate.Year, Month: bizDate.Month},
		Status:    StatusDraft,
		Source:    SourceManual,
		CreatedBy: createdBy,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

// AddEntry 追加一条分录。仅草稿可改。
func (v *Voucher) AddEntry(e ledger.Entry) error {
	if err := v.CanEdit(); err != nil {
		return err
	}
	v.Entries = append(v.Entries, e)
	return nil
}

// AddEntries 批量追加分录。
func (v *Voucher) AddEntries(es ...ledger.Entry) error {
	for _, e := range es {
		if err := v.AddEntry(e); err != nil {
			return err
		}
	}
	return nil
}

// SetSource 设置来源单据，建立「凭证 ↔ 流水/报销单/工资单」的关联。
func (v *Voucher) SetSource(s Source, id *int64) error {
	if err := v.CanEdit(); err != nil {
		return err
	}
	v.Source = s
	v.SourceID = id
	return nil
}

// TotalDebit 返回借方合计。
func (v *Voucher) TotalDebit() money.Money {
	var t money.Money
	for _, e := range v.Entries {
		t = t.Add(e.Debit)
	}
	return t
}

// TotalCredit 返回贷方合计。
func (v *Voucher) TotalCredit() money.Money {
	var t money.Money
	for _, e := range v.Entries {
		t = t.Add(e.Credit)
	}
	return t
}

// Amount 返回凭证金额（借方合计），用于列表展示。
func (v *Voucher) Amount() money.Money { return v.TotalDebit() }

// IsBalanced 报告借贷是否相等。
func (v *Voucher) IsBalanced() bool { return v.TotalDebit().Sub(v.TotalCredit()).IsZero() }

// BuildPosting 由凭证分录构造过账计划。
func (v *Voucher) BuildPosting() *ledger.Posting {
	p := ledger.NewPosting(v.BizDate)
	for _, e := range v.Entries {
		p.Add(e)
	}
	return p
}

// Validate 校验凭证自身与其过账计划，通过则返回所属会计期间。
func (v *Voucher) Validate(ctx *ledger.Context) (*period.Period, error) {
	if !v.Word.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrBadWord, v.Word)
	}
	if !v.BizDate.Valid() {
		return nil, fmt.Errorf("%w: %v", ErrBadDate, v.BizDate)
	}
	if len(v.Entries) == 0 {
		return nil, ErrNoEntries
	}
	if v.CreatedBy == "" {
		return nil, ErrMissingMaker
	}
	// 凭证的实际期间必须与固化字段一致，防止改日期后期间未同步
	k := period.Key{Year: v.BizDate.Year, Month: v.BizDate.Month}
	if v.Period != k {
		return nil, fmt.Errorf("voucher: 凭证期间 %s 与日期 %s 不一致",
			v.Period, v.BizDate)
	}
	return v.BuildPosting().Validate(ctx)
}

// ValidateDraft 校验一张草稿是否已经成形（不要求期间已启用）。
//
// 界面在每次「保存草稿」时调用它：借贷不平、科目不能记账、
// 摘要为空、辅助核算缺失这类问题当场就能指出来，
// 而不是等用户点了「过账」才发现，白改一遍。
//
// 唯一放行的是「期间尚未启用 / 已结账」—— 那是过账时才需要拦的事。
func (v *Voucher) ValidateDraft(ctx *ledger.Context) (*period.Period, error) {
	if !v.Word.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrBadWord, v.Word)
	}
	if !v.BizDate.Valid() {
		return nil, fmt.Errorf("%w: %v", ErrBadDate, v.BizDate)
	}
	if len(v.Entries) == 0 {
		return nil, ErrNoEntries
	}
	// 草稿允许尚未填写制单人：用户可能先记下来再补签章。
	// 但过账时 Validate 会强制要求，那时必须有。

	k := period.Key{Year: v.BizDate.Year, Month: v.BizDate.Month}
	if v.Period != k {
		return nil, fmt.Errorf("voucher: 凭证期间 %s 与日期 %s 不一致",
			v.Period, v.BizDate)
	}
	if err := v.BuildPosting().ValidateDraft(ctx); err != nil {
		return nil, err
	}
	per, ok := ctx.Periods.PeriodOf(v.BizDate)
	if !ok {
		return nil, fmt.Errorf("%w: %s", period.ErrPeriodNotFound, v.BizDate)
	}
	return per, nil
}

// ---------------------------------------------------------------------------
// 状态机
// ---------------------------------------------------------------------------

// CanEdit 报告凭证当前是否允许修改。
func (v *Voucher) CanEdit() error {
	switch v.Status {
	case StatusDraft:
		return nil
	case StatusPosted:
		return fmt.Errorf("%w：已过账的凭证不能修改，如需更正请先红字冲销", ErrNotDraft)
	case StatusVoided:
		return fmt.Errorf("%w：已作废的凭证不能修改", ErrAlreadyVoided)
	default:
		return fmt.Errorf("%w：未知状态 %q", ErrNotDraft, v.Status)
	}
}

// CanDelete 报告凭证当前是否允许删除。
//
// 只有草稿可以删除。已过账的凭证**永远不删除** ——
// 必须用红字冲销，让原凭证与冲销凭证都留在账上。
func (v *Voucher) CanDelete() error { return v.CanEdit() }

// CanPost 报告凭证当前是否允许过账。
func (v *Voucher) CanPost() error {
	switch v.Status {
	case StatusDraft:
		return nil
	case StatusPosted:
		return ErrAlreadyPosted
	case StatusVoided:
		return ErrAlreadyVoided
	default:
		return fmt.Errorf("voucher: 未知状态 %q", v.Status)
	}
}

// CanVoid 报告凭证当前是否允许作废（红字冲销）。
func (v *Voucher) CanVoid() error {
	switch v.Status {
	case StatusPosted:
		return nil
	case StatusDraft:
		return fmt.Errorf("%w：草稿凭证直接删除即可", ErrNotPosted)
	case StatusVoided:
		return ErrAlreadyVoided
	default:
		return fmt.Errorf("voucher: 未知状态 %q", v.Status)
	}
}

// Post 过账凭证：状态迁移 + 记账签章。
//
// 本方法**不写数据库**。调用方（service 层）必须在一个事务内依次完成：
// 分配凭证字号 → 写凭证与分录 → 写总账分录 → 调用本方法迁移状态。
// 保持领域层纯粹是刻意的：这样全部规则都能用内存对象做单元测试。
func (v *Voucher) Post(by string, at time.Time) error {
	if err := v.CanPost(); err != nil {
		return err
	}
	if by == "" {
		return ErrMissingPoster
	}
	v.Status = StatusPosted
	v.PostedBy = by
	t := at
	v.PostedAt = &t
	v.UpdatedAt = at
	return nil
}

// Void 作废凭证（红字冲销后的状态迁移）。
func (v *Voucher) Void(by string, at time.Time, reversalID int64) error {
	if err := v.CanVoid(); err != nil {
		return err
	}
	v.Status = StatusVoided
	v.VoidedBy = &reversalID
	v.UpdatedAt = at
	return nil
}

// BuildReversal 构造冲销本凭证的红字凭证。
//
// 红字凭证的规则：
//   - 借贷互换（金额保持非负，不用负数金额）
//   - 摘要前缀「冲销」
//   - 金额与原凭证完全一致
//   - 业务日期默认为原凭证日期（保证落在同一会计期间）；
//     若原期间已结账，调用方应传入当前开放期间并告知用户
func (v *Voucher) BuildReversal(by string, bizDate calendar.Date) (*Voucher, error) {
	if err := v.CanVoid(); err != nil {
		return nil, err
	}
	if by == "" {
		return nil, ErrMissingMaker
	}
	if !bizDate.Valid() {
		return nil, fmt.Errorf("%w: %v", ErrBadDate, bizDate)
	}

	rev, err := New(v.Word, bizDate, by)
	if err != nil {
		return nil, err
	}
	rev.Source = v.Source
	rev.SourceID = v.SourceID
	rev.Remark = "冲销 " + v.No
	rev.AttachCount = 0

	origID := v.ID
	rev.ReversesID = &origID

	for _, e := range v.Entries {
		ne := e
		ne.Debit, ne.Credit = e.Credit, e.Debit // 借贷互换
		ne.Summary = reversalSummary(e.Summary)
		rev.Entries = append(rev.Entries, ne)
	}
	return rev, nil
}

func reversalSummary(s string) string {
	const prefix = "冲销 "
	if strings.HasPrefix(s, prefix) {
		return s
	}
	if s == "" {
		return "冲销"
	}
	return prefix + s
}

// ---------------------------------------------------------------------------
// 序号连续性检查
// ---------------------------------------------------------------------------

// SequenceIssue 描述一处凭证字号问题。
type SequenceIssue struct {
	Key    period.Key
	Word   Word
	Kind   string // "duplicate" | "gap"
	Detail string
}

// CheckSequence 检查一组凭证在同一期间、同一凭证字下是否重号或断号。
//
// 中国《会计基础工作规范》要求凭证号连续编号、不得重号或跳号。
// 这是月结前体检的一项。
//
// 传入的凭证应属于同一账套；不同期间/凭证字会分别检查。
func CheckSequence(vs []*Voucher) []SequenceIssue {
	type key struct {
		k period.Key
		w Word
	}
	groups := map[key][]int{}
	for _, v := range vs {
		// ★ 草稿要跳过：草稿**不占号**，Seq 是 0。
		//
		// 不跳过的后果不是「偶尔误报」—— 本工程里凭证录完只落草稿、
		// 到账期结算才统一过账，所以任何一个有录入的期间都会带着
		// 若干张 Seq=0 的草稿进来，于是每一期结账都会撞上
		// 「应从 1 开始，实际从 0 开始」这条**假**警告。
		// 天天报的假警告等于没有警告。
		//
		// 作废凭证则仍然计入：它确实占着号位。
		if v.Status == StatusDraft {
			continue
		}
		k := key{v.Period, v.Word}
		groups[k] = append(groups[k], v.Seq)
	}

	var issues []SequenceIssue
	for k, seqs := range groups {
		sort.Ints(seqs)
		seen := map[int]bool{}
		for i, s := range seqs {
			if seen[s] {
				issues = append(issues, SequenceIssue{
					Key: k.k, Word: k.w, Kind: "duplicate",
					Detail: fmt.Sprintf("序号 %d 重复", s),
				})
				continue
			}
			seen[s] = true
			if i == 0 {
				// 每期从 1 开始
				if s != 1 {
					issues = append(issues, SequenceIssue{
						Key: k.k, Word: k.w, Kind: "gap",
						Detail: fmt.Sprintf("应从 1 开始，实际从 %d 开始", s),
					})
				}
				continue
			}
			if s != seqs[i-1]+1 {
				issues = append(issues, SequenceIssue{
					Key: k.k, Word: k.w, Kind: "gap",
					Detail: fmt.Sprintf("%d 与 %d 之间断号", seqs[i-1], s),
				})
			}
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Key != issues[j].Key {
			return issues[i].Key.Before(issues[j].Key)
		}
		return issues[i].Word < issues[j].Word
	})
	return issues
}
