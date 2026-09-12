// Package account 定义会计科目及其树形结构。
//
// 与 Frappe Books 的关键差异（本项目必须原生支持、否则中国会计无法使用）：
//
//   - 科目有独立的 4-2-2-2 编码字段（Frappe 把编码拼进科目名称字符串）
//   - 有「余额方向」字段，因此备抵科目（累计折旧/累计摊销/商品进销差价）能正确处理
//   - 根类型含 asset/liability/equity/cost/income/expense 六类
//     （Frappe 只有五类，没有「成本类」，导致生产成本被并入费用、报表算错）
//   - 科目可声明辅助核算维度，过账时强制校验
package account

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"miniaccount/internal/domain/money"
)

// RootType 是科目的会计要素归属，决定余额方向与报表归属。
type RootType string

// 六大类。注意：中国《小企业会计准则》的官方分类是五大类
// （资产/负债/所有者权益/成本/损益），其中「损益类」在计算时
// 必须拆成 income 与 expense，否则无法判断余额方向。
const (
	RootAsset     RootType = "asset"     // 资产类
	RootLiability RootType = "liability" // 负债类
	RootEquity    RootType = "equity"    // 所有者权益类
	RootCost      RootType = "cost"      // 成本类
	RootIncome    RootType = "income"    // 损益类—收入
	RootExpense   RootType = "expense"   // 损益类—费用
)

// AllRootTypes 按会计要素顺序列出全部根类型。
var AllRootTypes = []RootType{
	RootAsset, RootLiability, RootEquity, RootCost, RootIncome, RootExpense,
}

// Valid 报告 rt 是否为已知的根类型。
func (rt RootType) Valid() bool {
	for _, r := range AllRootTypes {
		if r == rt {
			return true
		}
	}
	return false
}

// Category 是《小企业会计准则》的官方五大类，用于科目表展示与筛选。
type Category string

// 官方五大类。
const (
	CatAsset     Category = "资产类"
	CatLiability Category = "负债类"
	CatEquity    Category = "所有者权益类"
	CatCost      Category = "成本类"
	CatProfit    Category = "损益类"
)

// CategoryOf 返回根类型对应的官方大类。
func CategoryOf(rt RootType) Category {
	switch rt {
	case RootAsset:
		return CatAsset
	case RootLiability:
		return CatLiability
	case RootEquity:
		return CatEquity
	case RootCost:
		return CatCost
	case RootIncome, RootExpense:
		return CatProfit
	default:
		return ""
	}
}

// BalanceDir 是科目的余额方向。
type BalanceDir string

// 余额方向。
const (
	DirDebit  BalanceDir = "debit"  // 借方
	DirCredit BalanceDir = "credit" // 贷方
)

// Valid 报告方向是否合法。
func (d BalanceDir) Valid() bool { return d == DirDebit || d == DirCredit }

// String 返回中文名。
func (d BalanceDir) String() string {
	switch d {
	case DirDebit:
		return "借"
	case DirCredit:
		return "贷"
	default:
		return ""
	}
}

// NaturalBalanceDir 返回根类型的「自然」余额方向。
//
// 注意这**只是默认值**：资产类中的备抵科目（1407 商品进销差价、
// 1602 累计折旧、1622 生产性生物资产累计折旧、1702 累计摊销）
// 余额方向与所属大类相反，必须在 Account.BalanceDir 上显式覆盖。
func NaturalBalanceDir(rt RootType) BalanceDir {
	switch rt {
	case RootAsset, RootCost, RootExpense:
		return DirDebit
	case RootLiability, RootEquity, RootIncome:
		return DirCredit
	default:
		return DirDebit
	}
}

// AuxType 是辅助核算维度。
type AuxType string

// 辅助核算维度。小微企业需要的维度就是这几个，用固定列存储即可；
// 通用维度子表会让往来账查询复杂数倍（详见方案 §4.3）。
const (
	AuxCustomer    AuxType = "customer"    // 客户
	AuxSupplier    AuxType = "supplier"    // 供应商
	AuxEmployee    AuxType = "employee"    // 员工
	AuxShareholder AuxType = "shareholder" // 股东
	AuxDept        AuxType = "dept"        // 部门
	AuxProject     AuxType = "project"     // 项目
	AuxOther       AuxType = "other"       // 其他外部单位
)

// AllAuxTypes 列出全部辅助核算维度。
var AllAuxTypes = []AuxType{
	AuxCustomer, AuxSupplier, AuxEmployee, AuxShareholder, AuxDept, AuxProject, AuxOther,
}

// Valid 报告维度是否已知。
func (a AuxType) Valid() bool {
	for _, t := range AllAuxTypes {
		if t == a {
			return true
		}
	}
	return false
}

// Label 返回中文名。
func (a AuxType) Label() string {
	switch a {
	case AuxCustomer:
		return "客户"
	case AuxSupplier:
		return "供应商"
	case AuxEmployee:
		return "员工"
	case AuxShareholder:
		return "股东"
	case AuxDept:
		return "部门"
	case AuxProject:
		return "项目"
	case AuxOther:
		return "其他单位"
	default:
		return string(a)
	}
}

// ---------------------------------------------------------------------------
// Account
// ---------------------------------------------------------------------------

// Account 是一个会计科目。
//
// Code 是 4-2-2-2 的纯数字编码（一级 4 位、二级 6 位、三级 8 位）。
// 用纯数字而非 2221.01 这种带点的写法，是为了 ORDER BY code 能直接得到
// 正确顺序、且 LIKE '2221%' 前缀匹配可用；带点的展示形式属于 UI 层的事。
type Account struct {
	ID         int64
	Code       string
	Name       string
	ParentID   *int64
	ParentCode string // 冗余保存父编码，便于不查库即可校验
	Level      int
	IsLeaf     bool // 是否明细科目（只有明细科目允许记账）
	RootType   RootType
	// Category 由 RootType 推导，不单独存储以免不一致。
	BalanceDir BalanceDir
	AuxTypes   []AuxType
	IsEnabled  bool
	IsPreset   bool // 准则预置科目，不允许删除
	Remark     string
	SortOrder  int
}

// Category 返回官方五大类。
func (a *Account) Category() Category { return CategoryOf(a.RootType) }

// FullName 沿父链拼出全路径名，如「应交税费/应交税费—应交增值税/进项税额」。
// 需要调用方提供全套科目用于查父。
func (a *Account) FullName(all map[string]*Account) string {
	parts := []string{a.Name}
	seen := map[string]bool{a.Code: true}
	cur := a
	for cur.ParentCode != "" {
		if seen[cur.ParentCode] {
			break // 防御成环
		}
		seen[cur.ParentCode] = true
		p, ok := all[cur.ParentCode]
		if !ok {
			break
		}
		parts = append(parts, p.Name)
		cur = p
	}
	// 反转
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "/")
}

// DisplayCode 返回便于阅读的编码形式，每两位一组用点分隔：
// "22210101" → "2221.01.01"。仅用于展示。
func (a *Account) DisplayCode() string {
	if len(a.Code) <= 4 {
		return a.Code
	}
	var b strings.Builder
	b.WriteString(a.Code[:4])
	for i := 4; i < len(a.Code); i += 2 {
		b.WriteByte('.')
		b.WriteString(a.Code[i : i+2])
	}
	return b.String()
}

// RequiresAux 报告该科目是否要求填写辅助核算。
func (a *Account) RequiresAux() bool { return len(a.AuxTypes) > 0 }

// SupportsAux 报告该科目是否允许某个辅助核算维度。
func (a *Account) SupportsAux(t AuxType) bool {
	for _, x := range a.AuxTypes {
		if x == t {
			return true
		}
	}
	return false
}

// Balance 返回「正常方向为正」的余额。
//
// 约定：先算 debit−credit，若科目正常方向是贷方则取反。
// 这样资产/费用类借方为正，负债/权益/收入类贷方为正，
// 而备抵科目因为 BalanceDir 是贷方，也能得到正确的正数余额。
func (a *Account) Balance(debit, credit money.Money) money.Money {
	v := debit.Sub(credit)
	if a.BalanceDir == DirCredit {
		return v.Neg()
	}
	return v
}

// ---------------------------------------------------------------------------
// 错误
// ---------------------------------------------------------------------------

// 科目树校验错误。
var (
	ErrEmptyCode       = errors.New("account: 科目编码不能为空")
	ErrBadCodeLength   = errors.New("account: 科目编码长度必须是 4/6/8 位")
	ErrNonDigitCode    = errors.New("account: 科目编码必须是纯数字")
	ErrDuplicateCode   = errors.New("account: 科目编码重复")
	ErrParentNotFound  = errors.New("account: 父科目不存在")
	ErrCodeParentMatch = errors.New("account: 子科目编码必须以父科目编码开头")
	ErrBadLevel        = errors.New("account: 层级与编码长度不一致")
	ErrCycle           = errors.New("account: 科目树存在环")
	ErrBadRootType     = errors.New("account: 未知的根类型")
	ErrBadBalanceDir   = errors.New("account: 未知的余额方向")
	ErrBadAuxType      = errors.New("account: 未知的辅助核算维度")

	// 下面三个是 CheckPostable 的失败原因。
	//
	// ★ 做成哨兵错误而不是普通 fmt.Errorf：界面需要据此**区分处置**——
	// 「科目不存在」要把用户引回科目选择器，
	// 「是汇总科目」要提示改用明细科目，
	// 「已停用」要提示去启用或换科目。三种都只能匹配字符串的话，
	// 界面就只能在错误文本里找关键词 —— 文案一改就失效。
	ErrNotFound = errors.New("account: 科目不存在")
	// ErrNotAllowedForTaxType 表示该科目在当前纳税人身份下不可使用。
	//
	// 典型场景：小规模纳税人不得抵扣进项税额，
	// 因此不能往「应交增值税—进项税额」这类专栏上记账。
	ErrNotAllowedForTaxType = errors.New("account: 当前纳税人身份下不允许使用该科目")
	ErrNotLeaf              = errors.New("account: 科目是汇总科目，不能直接记账")
	ErrDisabled             = errors.New("account: 科目已停用")
)

// ---------------------------------------------------------------------------
// Tree
// ---------------------------------------------------------------------------

// Tree 是一棵按编码组织的科目树，构建时完成全部结构校验。
type Tree struct {
	byCode   map[string]*Account
	children map[string][]*Account
	roots    []*Account
	ordered  []*Account // 按编码升序
}

// NewTree 由扁平科目列表构建科目树，并执行结构与语义校验。
//
// 校验项（这些正是常见科目表缺陷的检查点）：
//
//  1. 编码非空、纯数字、长度为 4/6/8
//  2. 编码唯一
//  3. 父科目必须存在
//  4. 子编码必须以父编码开头（防止「1231.01 挂在 1221 下」这类错乱）
//  5. 层级与编码长度一致
//  6. 无环
//  7. 有子科目的科目必须 isGroup（不可记账）
//  8. 根类型与余额方向合法
//  9. 辅助核算维度合法
func NewTree(accounts []*Account) (*Tree, error) {
	t := &Tree{
		byCode:   make(map[string]*Account, len(accounts)),
		children: make(map[string][]*Account),
	}

	for _, a := range accounts {
		if a.Code == "" {
			return nil, fmt.Errorf("%w（科目 %q）", ErrEmptyCode, a.Name)
		}
		if !isDigits(a.Code) {
			return nil, fmt.Errorf("%w: %q", ErrNonDigitCode, a.Code)
		}
		switch len(a.Code) {
		case 4, 6, 8:
		default:
			return nil, fmt.Errorf("%w: %q 长度 %d", ErrBadCodeLength, a.Code, len(a.Code))
		}
		if _, dup := t.byCode[a.Code]; dup {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateCode, a.Code)
		}
		if !a.RootType.Valid() {
			return nil, fmt.Errorf("%w: %q（科目 %s）", ErrBadRootType, a.RootType, a.Code)
		}
		if !a.BalanceDir.Valid() {
			return nil, fmt.Errorf("%w: %q（科目 %s）", ErrBadBalanceDir, a.BalanceDir, a.Code)
		}
		for _, x := range a.AuxTypes {
			if !x.Valid() {
				return nil, fmt.Errorf("%w: %q（科目 %s）", ErrBadAuxType, x, a.Code)
			}
		}
		wantLevel := (len(a.Code) - 2) / 2
		if a.Level != wantLevel {
			return nil, fmt.Errorf("%w: %q 应为 %d 级，实际 %d 级",
				ErrBadLevel, a.Code, wantLevel, a.Level)
		}
		t.byCode[a.Code] = a
	}

	// 父子关系与编码前缀一致性
	for _, a := range accounts {
		if a.Level == 1 {
			if a.ParentCode != "" {
				return nil, fmt.Errorf("%w: 一级科目 %q 不应有父科目 %q",
					ErrCodeParentMatch, a.Code, a.ParentCode)
			}
			t.roots = append(t.roots, a)
			continue
		}
		if a.ParentCode == "" {
			return nil, fmt.Errorf("%w: 科目 %q 是 %d 级但未指定父科目",
				ErrParentNotFound, a.Code, a.Level)
		}
		p, ok := t.byCode[a.ParentCode]
		if !ok {
			return nil, fmt.Errorf("%w: %q 的父科目 %q", ErrParentNotFound, a.Code, a.ParentCode)
		}
		if !strings.HasPrefix(a.Code, p.Code) {
			return nil, fmt.Errorf("%w: %q 的父科目是 %q(%s)",
				ErrCodeParentMatch, a.Code, p.Name, p.Code)
		}
		if p.Level != a.Level-1 {
			return nil, fmt.Errorf("%w: %q 是 %d 级，父科目 %q 是 %d 级",
				ErrBadLevel, a.Code, a.Level, p.Code, p.Level)
		}
		t.children[p.Code] = append(t.children[p.Code], a)
	}

	// 有子科目者不可记账
	for code, kids := range t.children {
		if len(kids) > 0 {
			t.byCode[code].IsLeaf = false
		}
	}

	// 环检测
	if err := t.detectCycle(); err != nil {
		return nil, err
	}

	// 排序
	t.ordered = make([]*Account, len(accounts))
	copy(t.ordered, accounts)
	sort.Slice(t.ordered, func(i, j int) bool { return t.ordered[i].Code < t.ordered[j].Code })
	sort.Slice(t.roots, func(i, j int) bool { return t.roots[i].Code < t.roots[j].Code })
	for k := range t.children {
		kids := t.children[k]
		sort.Slice(kids, func(i, j int) bool { return kids[i].Code < kids[j].Code })
	}
	return t, nil
}

func (t *Tree) detectCycle() error {
	const (
		white = 0 // 未访问
		gray  = 1 // 访问中
		black = 2 // 已完成
	)
	state := make(map[string]int, len(t.byCode))
	var walk func(code string) error
	walk = func(code string) error {
		switch state[code] {
		case gray:
			return fmt.Errorf("%w: 在 %q 处", ErrCycle, code)
		case black:
			return nil
		}
		state[code] = gray
		for _, kid := range t.children[code] {
			if err := walk(kid.Code); err != nil {
				return err
			}
		}
		state[code] = black
		return nil
	}
	for _, r := range t.roots {
		if err := walk(r.Code); err != nil {
			return err
		}
	}
	// 不在任何根下的孤立节点（理论上前面已拦截，这里兜底）
	for code := range t.byCode {
		if state[code] == white {
			if err := walk(code); err != nil {
				return err
			}
		}
	}
	return nil
}

// Get 按编码取科目。
func (t *Tree) Get(code string) (*Account, bool) {
	a, ok := t.byCode[code]
	return a, ok
}

// MustGet 按编码取科目，不存在则 panic。仅用于测试与已校验的数据。
func (t *Tree) MustGet(code string) *Account {
	a, ok := t.byCode[code]
	if !ok {
		panic("account: 科目不存在 " + code)
	}
	return a
}

// Len 返回科目总数。
func (t *Tree) Len() int { return len(t.byCode) }

// All 返回按编码升序排列的全部科目。
func (t *Tree) All() []*Account { return t.ordered }

// Roots 返回全部一级科目。
func (t *Tree) Roots() []*Account { return t.roots }

// Children 返回直接下级科目。
func (t *Tree) Children(code string) []*Account { return t.children[code] }

// Descendants 返回全部下级科目（不含自身），深度优先、按编码升序。
func (t *Tree) Descendants(code string) []*Account {
	var out []*Account
	var walk func(string)
	walk = func(c string) {
		for _, kid := range t.children[c] {
			out = append(out, kid)
			walk(kid.Code)
		}
	}
	walk(code)
	return out
}

// Ancestors 返回全部上级科目，由近及远。
func (t *Tree) Ancestors(code string) []*Account {
	var out []*Account
	seen := map[string]bool{}
	cur, ok := t.byCode[code]
	if !ok {
		return nil
	}
	for cur.ParentCode != "" && !seen[cur.ParentCode] {
		seen[cur.ParentCode] = true
		p, ok := t.byCode[cur.ParentCode]
		if !ok {
			break
		}
		out = append(out, p)
		cur = p
	}
	return out
}

// SubtreeLeaves 返回以 code 为根的子树中全部**明细科目**（含自身）。
//
// 报表取数必须用它：汇总科目本身没有余额，公式写 "5602" 时要展开成
// 它下面所有叶子科目，否则利润表「管理费用」会取到 0。
func (t *Tree) SubtreeLeaves(code string) []*Account {
	self, ok := t.byCode[code]
	if !ok {
		return nil
	}
	if self.IsLeaf {
		return []*Account{self}
	}
	var out []*Account
	for _, d := range t.Descendants(code) {
		if d.IsLeaf {
			out = append(out, d)
		}
	}
	return out
}

// ByPrefix 返回编码以 prefix 开头的全部科目（含自身），按编码升序。
// 用于报表公式的前缀匹配取数。
func (t *Tree) ByPrefix(prefix string) []*Account {
	var out []*Account
	for _, a := range t.ordered {
		if strings.HasPrefix(a.Code, prefix) {
			out = append(out, a)
		}
	}
	return out
}

// CheckPostable 校验某个科目是否可用于记账。
//
// 这是过账前的必检项之一。Frappe Books 没有这个校验，
// 因此可以直接记到汇总科目上，导致上下级科目数字重复计算。
func (t *Tree) CheckPostable(code string) error {
	a, ok := t.byCode[code]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, code)
	}
	if !a.IsLeaf {
		return fmt.Errorf("%w：%s(%s)", ErrNotLeaf, a.Name, a.Code)
	}
	if !a.IsEnabled {
		return fmt.Errorf("%w：%s(%s)", ErrDisabled, a.Name, a.Code)
	}
	return nil
}

// EnabledLeaves 返回全部启用且可记账的科目。
func (t *Tree) EnabledLeaves() []*Account {
	var out []*Account
	for _, a := range t.ordered {
		if a.IsLeaf && a.IsEnabled {
			out = append(out, a)
		}
	}
	return out
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// Label 返回根类型的中文名（界面上显示用）。
func (rt RootType) Label() string {
	switch rt {
	case RootAsset:
		return "资产"
	case RootLiability:
		return "负债"
	case RootEquity:
		return "所有者权益"
	case RootCost:
		return "成本"
	case RootIncome:
		return "收入"
	case RootExpense:
		return "费用"
	}
	return string(rt)
}

// ValidateCode 校验科目编码是否符合 4-2-2-2 的分级规则。
//
// 规则：一级 4 位，其后每级 2 位，最长 4 级（4+2+2+2 = 10 位）。
// 用长度判定层级，而不是相信调用方传进来的 level —— 两者不一致时，
// 报表按编码前缀展开、凭证按 level 判层级，就会各说各话。
func ValidateCode(code string) error {
	n := len(code)
	switch {
	case n == 4:
		return nil
	case n == 6, n == 8, n == 10:
		return nil
	}
	return fmt.Errorf("科目编码应当是 4/6/8/10 位数字（4-2-2-2 分级），当前 %q 是 %d 位",
		code, n)
}

// LevelOfCode 由编码长度推出层级（4→1、6→2、8→3、10→4）。
func LevelOfCode(code string) int {
	switch len(code) {
	case 4:
		return 1
	case 6:
		return 2
	case 8:
		return 3
	case 10:
		return 4
	}
	return 0
}
