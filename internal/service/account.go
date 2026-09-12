package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/store/sqlite"
)

// ---------------------------------------------------------------------------
// 科目管理
// ---------------------------------------------------------------------------
//
// 建账时预置了《小企业会计准则》的科目表，但**准则科目不够用**是常态：
// 一家公司的「管理费用」下可能要分「研发费」「安全生产费」，
// 或者要按项目单独核算。原来这些只能记在预置科目里靠备注区分 ——
// 报出来就是一锅粥。
//
// # 危险在哪
//
// 科目表是**所有报表的地基**：报表按科目树汇总、凭证按科目校验辅助核算、
// 余额方向决定余额算哪边。所以这里的每一条规则都是在防「改了之后
// historical 账就对不上了」：
//
//   - 有分录/有余额的科目**不能删**，只能停用（历史凭证还要靠它显示）；
//   - 有分录之后**不能改编码、父科目、余额方向**（账已经按旧编码记了）；
//   - 子科目的辅助核算**不能比父科目少**（少了就能绕过父科目的核算要求）；
//   - 预置科目不能删，但可以停用 —— 用户确实用不上某个科目是合理的。
//
// # 留痕
//
// 规范的完整性要求里点名了「会计科目表的维护」。所以增删改都进操作日志，
// 并且记**修改前后的值**。

// AccountInput 是新增/修改科目的入参。
type AccountInput struct {
	// Code 是科目编码（4-2-2-2，纯数字）。
	Code string `json:"code"`
	// Name 是科目名称。
	Name string `json:"name"`
	// ParentCode 是上级科目编码；留空表示一级科目。
	ParentCode string `json:"parentCode"`
	// RootType 是六大类：asset|liability|equity|cost|income|expense。
	// 修改已有科目时忽略（不能改大类）。
	RootType string `json:"rootType"`
	// BalanceDir 是余额方向：debit|credit；留空按大类推导。
	BalanceDir string `json:"balanceDir"`
	// AuxTypes 是需要的辅助核算维度：customer|supplier|employee|dept|project。
	AuxTypes []string `json:"auxTypes"`
	// Remark 是备注。
	Remark string `json:"remark"`
	// IsLeaf 为真表示明细科目（可记账）；为假是汇总科目。
	// 新增时留空按「明细科目」处理 —— 用户加科目通常是为了记账。
	IsLeaf *bool `json:"isLeaf"`
}

// AccountRow 是科目管理界面上的一行。
type AccountRow struct {
	ID         int64    `json:"id"`
	Code       string   `json:"code"`
	Name       string   `json:"name"`
	FullName   string   `json:"fullName"`
	ParentCode string   `json:"parentCode"`
	Level      int      `json:"level"`
	IsLeaf     bool     `json:"isLeaf"`
	RootType   string   `json:"rootType"`
	RootLabel  string   `json:"rootLabel"`
	BalanceDir string   `json:"balanceDir"`
	DirLabel   string   `json:"dirLabel"`
	AuxTypes   []string `json:"auxTypes"`
	AuxLabels  []string `json:"auxLabels"`
	IsEnabled  bool     `json:"isEnabled"`
	IsPreset   bool     `json:"isPreset"`
	Remark     string   `json:"remark"`

	// EntryCount 是引用过这个科目的分录行数。
	//
	// ★ 界面上必须显示它：用户想删一个科目时，得先知道它已经被用过 ——
	// 「不能删」的原因要看得见，否则就是一句没头没脑的拒绝。
	EntryCount int `json:"entryCount"`
	// Balance 是当前余额（分）；有余额的科目同样不能删。
	Balance money.Money `json:"balance"`
	// ChildCount 是下级科目数。
	ChildCount int `json:"childCount"`
	// CanDelete 为假时，Reason 说明为什么不能删。
	CanDelete bool   `json:"canDelete"`
	Reason    string `json:"reason,omitempty"`
}

// AccountsView 是科目管理页的数据。
type AccountsView struct {
	Rows []AccountRow `json:"rows"`
	// Total / LeafCount 是统计。
	Total     int `json:"total"`
	LeafCount int `json:"leafCount"`
	// RootTypes 是六大类的选项（新增科目时用）。
	RootTypes []AccountOptionItem `json:"rootTypes"`
	AuxTypes  []AccountOptionItem `json:"auxTypes"`
	// MaxLevel 是允许的最大层级。
	MaxLevel int `json:"maxLevel"`
}

// AccountOptionItem 是一个下拉选项。
type AccountOptionItem struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Accounts 返回科目表（含使用情况），供管理界面用。
func (s *Service) Accounts(ctx context.Context) (*AccountsView, error) {
	list, err := s.db.Accounts().List(ctx)
	if err != nil {
		return nil, err
	}
	usage, err := s.db.Accounts().Usage(ctx)
	if err != nil {
		return nil, err
	}
	byCode := make(map[string]*account.Account, len(list))
	for _, a := range list {
		byCode[a.Code] = a
	}

	out := &AccountsView{
		MaxLevel: 4,
		RootTypes: []AccountOptionItem{
			{Value: string(account.RootAsset), Label: "资产"},
			{Value: string(account.RootLiability), Label: "负债"},
			{Value: string(account.RootEquity), Label: "所有者权益"},
			{Value: string(account.RootCost), Label: "成本"},
			{Value: string(account.RootIncome), Label: "损益—收入"},
			{Value: string(account.RootExpense), Label: "损益—费用"},
		},
		AuxTypes: []AccountOptionItem{
			{Value: string(account.AuxCustomer), Label: "客户"},
			{Value: string(account.AuxSupplier), Label: "供应商"},
			{Value: string(account.AuxEmployee), Label: "员工"},
			{Value: string(account.AuxDept), Label: "部门"},
			{Value: string(account.AuxProject), Label: "项目"},
		},
	}
	for _, a := range list {
		u := usage[a.ID]
		row := AccountRow{
			ID: a.ID, Code: a.Code, Name: a.Name,
			FullName: a.FullName(byCode), ParentCode: a.ParentCode,
			Level: a.Level, IsLeaf: a.IsLeaf,
			RootType: string(a.RootType), RootLabel: a.RootType.Label(),
			BalanceDir: string(a.BalanceDir), DirLabel: a.BalanceDir.String(),
			IsEnabled: a.IsEnabled, IsPreset: a.IsPreset, Remark: a.Remark,
			EntryCount: u.Entries, Balance: u.Balance, ChildCount: u.Children,
		}
		for _, t := range a.AuxTypes {
			row.AuxTypes = append(row.AuxTypes, string(t))
			row.AuxLabels = append(row.AuxLabels, t.Label())
		}
		row.CanDelete, row.Reason = deleteBlockers(a, u)
		out.Rows = append(out.Rows, row)
		out.Total++
		if a.IsLeaf {
			out.LeafCount++
		}
	}
	sort.SliceStable(out.Rows, func(i, j int) bool { return out.Rows[i].Code < out.Rows[j].Code })
	return out, nil
}

// deleteBlockers 判断一个科目能不能删，并给出不能删的原因。
//
// ★ 这个判断要**同时**给界面看（置灰删除按钮 + 说明为什么）——
// 只在后端拦下来，用户看到的是「点了没反应」。
func deleteBlockers(a *account.Account, u sqlite.AccountUsage) (bool, string) {
	switch {
	case a.IsPreset:
		return false, "准则预置科目不能删除，只能停用"
	case u.Children > 0:
		return false, fmt.Sprintf("还有 %d 个下级科目，先处理下级", u.Children)
	case u.Entries > 0:
		return false, fmt.Sprintf("已被 %d 条分录引用过，删除会让历史凭证对不上科目", u.Entries)
	case u.Balance != 0:
		return false, "还有余额，先把余额结平"
	}
	return true, ""
}

// SaveAccount 新增或修改一个科目。
//
// Code 已存在 = 修改；不存在 = 新增。
func (s *Service) SaveAccount(ctx context.Context, in AccountInput) (*AccountRow, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	in.ParentCode = strings.TrimSpace(in.ParentCode)
	if in.Code == "" {
		return nil, fmt.Errorf("请填写科目编码")
	}
	if in.Name == "" {
		return nil, fmt.Errorf("请填写科目名称")
	}
	if !isDigits(in.Code) {
		return nil, fmt.Errorf("科目编码只能是数字（如 6602 或 660201），当前 %q", in.Code)
	}
	if err := account.ValidateCode(in.Code); err != nil {
		return nil, err
	}

	existing, err := s.db.Accounts().GetByCode(ctx, in.Code)
	found := err == nil
	if err != nil {
		// ★ 用 errors.Is 判「不存在」，不要拿错误文本去匹配。
		//
		// 这里踩过一次：原来是 strings.Contains(err.Error(), "不存在")，
		// 而存储层返回的是 `sqlite: 记录不存在: 科目 "6602"` ——
		// 文本对得上纯属巧合；换成别的包装就漏判，于是「修改已有科目」
		// 会走到新增分支，最后撞在 UNIQUE 约束上，
		// 用户看到的是一句数据库错误。
		if !isNotFound(err) {
			return nil, err
		}
	}

	if found {
		return s.updateAccount(ctx, existing, in)
	}
	return s.createAccount(ctx, in)
}

func (s *Service) createAccount(ctx context.Context, in AccountInput) (*AccountRow, error) {
	// ★ 挂在已有科目下面时，大类**从父科目继承**。
	//
	// 界面上加子科目时还要再选一次「资产/负债/费用」是荒谬的 ——
	// 父科目已经决定了，而且选错了就会被下面的校验拒掉，
	// 用户只会觉得「我明明挂在管理费用下面，怎么还要选大类」。
	root := account.RootType(strings.TrimSpace(in.RootType))
	if root == "" && in.ParentCode != "" {
		if p, perr := s.db.Accounts().GetByCode(ctx, in.ParentCode); perr == nil {
			root = p.RootType
		}
	}
	if !root.Valid() {
		return nil, fmt.Errorf("请选择科目大类（资产/负债/权益/成本/收入/费用）")
	}
	dir := account.NaturalBalanceDir(root)
	if bd := strings.TrimSpace(in.BalanceDir); bd != "" {
		d := account.BalanceDir(bd)
		if !d.Valid() {
			return nil, fmt.Errorf("余额方向只能是 debit 或 credit")
		}
		if d != dir {
			// 方向由大类决定：资产类记成贷方余额，报表两边都会错。
			return nil, fmt.Errorf("%s类的余额方向应当是「%s」—— "+
				"方向由科目大类决定，不能单独指定", root.Label(), dir.String())
		}
	}

	level := 1
	var parent *account.Account
	if in.ParentCode != "" {
		p, perr := s.db.Accounts().GetByCode(ctx, in.ParentCode)
		if perr != nil {
			return nil, fmt.Errorf("上级科目 %s 不存在", in.ParentCode)
		}
		if p.IsLeaf {
			// 在明细科目下加子科目：这个明细自己就变成汇总科目了，
			// 而它可能已经被用过 —— 那会让它的历史分录失去归属。
			used, uerr := s.db.Accounts().UsageOf(ctx, p.ID)
			if uerr == nil && used.Entries > 0 {
				return nil, fmt.Errorf(
					"上级科目「%s」已经是明细科目且被 %d 条分录用过，"+
						"不能再往下加 —— 请换一个汇总科目作上级",
					p.FullName(nil), used.Entries)
			}
		}
		if p.RootType != root {
			return nil, fmt.Errorf("上级科目「%s」属于%s类，新科目不能挂在它下面",
				p.Name, p.RootType.Label())
		}
		level = p.Level + 1
		if level > 4 {
			return nil, fmt.Errorf("科目最多 4 级（4-2-2-2），「%s」已经是第 %d 级",
				p.FullName(nil), p.Level)
		}
		if !strings.HasPrefix(in.Code, p.Code) {
			return nil, fmt.Errorf("子科目编码要以父科目编码开头：上级是 %s，"+
				"新科目应当是 %sxx 这样的形式", p.Code, p.Code)
		}
		if len(in.Code) != len(p.Code)+2 {
			return nil, fmt.Errorf("每级 2 位数字：上级 %s（%d 位），"+
				"新科目应当是 %d 位", p.Code, len(p.Code), len(p.Code)+2)
		}
		parent = p
	} else if len(in.Code) != 4 {
		return nil, fmt.Errorf("一级科目编码是 4 位数字（如 6602），当前 %q", in.Code)
	}

	// 辅助核算：子科目必须**包含**父科目的要求（不能减少，
	// 否则挂到子科目上的分录就绕过了父科目的核算要求）。
	aux := parseAuxTypes(in.AuxTypes)
	if parent != nil {
		for _, need := range parent.AuxTypes {
			if !containsAux(aux, need) {
				aux = append(aux, need)
			}
		}
	}

	isLeaf := true
	if in.IsLeaf != nil {
		isLeaf = *in.IsLeaf
	}
	a := &account.Account{
		Code: in.Code, Name: in.Name, Level: level, IsLeaf: isLeaf,
		RootType: root, BalanceDir: dir, AuxTypes: aux,
		IsEnabled: true, IsPreset: false, Remark: strings.TrimSpace(in.Remark),
	}
	if parent != nil {
		a.ParentID = &parent.ID
		a.ParentCode = parent.Code
	}

	var newID int64
	err := s.db.WithTx(ctx, func(tx *sqlite.Tx) error {
		id, ierr := s.db.Accounts().Insert(ctx, tx, a)
		if ierr != nil {
			return ierr
		}
		newID = id
		// 父科目从「明细」变成「汇总」：它自己不能再记账了
		if parent != nil && parent.IsLeaf {
			if uerr := s.db.Accounts().SetLeaf(ctx, tx, parent.Code, false); uerr != nil {
				return uerr
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.ID = newID

	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAccountSave,
		Summary: fmt.Sprintf("新增科目 %s %s", a.Code, a.Name),
		Entity:  "account", EntityID: a.Code,
		Detail: map[string]any{
			"编码": a.Code, "名称": a.Name, "上级": a.ParentCode,
			"大类": a.RootType.Label(), "余额方向": a.BalanceDir.String(),
			"明细科目": a.IsLeaf, "辅助核算": auxLabels(a.AuxTypes),
			"备注": a.Remark,
		},
	})
	return s.accountRow(ctx, a.Code)
}

func (s *Service) updateAccount(ctx context.Context, old *account.Account,
	in AccountInput) (*AccountRow, error) {

	usage, err := s.db.Accounts().UsageOf(ctx, old.ID)
	if err != nil {
		return nil, err
	}
	name := in.Name
	remark := strings.TrimSpace(in.Remark)
	aux := parseAuxTypes(in.AuxTypes)
	isLeaf := old.IsLeaf
	if in.IsLeaf != nil {
		isLeaf = *in.IsLeaf
	}

	// ★ 已经有分录的科目：编码/父科目/方向/明细与否都不能改。
	//
	// 历史凭证按编码关联科目，改了编码等于把那些凭证挂到一个
	// 不存在的科目上 —— 报表会凭空少掉一块，而且很难查。
	if usage.Entries > 0 {
		if in.ParentCode != old.ParentCode {
			return nil, fmt.Errorf(
				"「%s」已经被 %d 条分录用过，不能换上级科目 —— "+
					"换了之后那些凭证会对不上账", old.FullName(nil), usage.Entries)
		}
		if isLeaf != old.IsLeaf {
			return nil, fmt.Errorf("「%s」已经被用过，不能再改「明细/汇总」属性",
				old.FullName(nil))
		}
	}
	if usage.Children > 0 && isLeaf {
		return nil, fmt.Errorf("「%s」下面还有 %d 个下级科目，不能改成明细科目",
			old.Name, usage.Children)
	}
	// 辅助核算只能加不能减：减了之后已有分录里填的维度就失去了依据，
	// 而且新分录可以绕过父科目的核算要求。
	for _, had := range old.AuxTypes {
		if !containsAux(aux, had) {
			return nil, fmt.Errorf("不能去掉「%s」要求的辅助核算 —— "+
				"已有分录是按它填的；要停用请用「停用科目」", had.Label())
		}
	}
	if old.ParentCode != "" {
		parent, perr := s.db.Accounts().GetByCode(ctx, old.ParentCode)
		if perr == nil {
			for _, need := range parent.AuxTypes {
				if !containsAux(aux, need) {
					aux = append(aux, need)
				}
			}
		}
	}

	before := map[string]any{
		"名称": old.Name, "备注": old.Remark,
		"辅助核算": auxLabels(old.AuxTypes), "明细科目": old.IsLeaf,
	}
	if err := s.db.Accounts().UpdateMeta(ctx, old.Code, name, remark, aux, isLeaf); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAccountSave,
		Summary: fmt.Sprintf("修改科目 %s %s", old.Code, name),
		Entity:  "account", EntityID: old.Code,
		Detail: map[string]any{
			"修改前": before,
			"修改后": map[string]any{
				"名称": name, "备注": remark,
				"辅助核算": auxLabels(aux), "明细科目": isLeaf,
			},
		},
	})
	return s.accountRow(ctx, old.Code)
}

// SetAccountEnabled 启用/停用科目。
//
// 停用不是删除：历史凭证照常显示与汇总，只是**不能再往上记账**。
// 这是「这个科目我不用了」的正确表达方式。
func (s *Service) SetAccountEnabled(ctx context.Context, code string, enabled bool) error {
	code = strings.TrimSpace(code)
	a, err := s.db.Accounts().GetByCode(ctx, code)
	if err != nil {
		return err
	}
	if a.IsEnabled == enabled {
		return nil
	}
	// 停用汇总科目时，下级也要一起停 —— 否则下级还能记账，
	// 而报表在汇总层看不到它，取数就断了。
	affected := []string{a.Code}
	if !enabled && !a.IsLeaf {
		tree, terr := s.db.Accounts().Tree(ctx)
		if terr == nil {
			for _, d := range tree.Descendants(a.Code) {
				affected = append(affected, d.Code)
			}
		}
	}
	err = s.db.WithTx(ctx, func(tx *sqlite.Tx) error {
		for _, c := range affected {
			if serr := s.db.Accounts().SetEnabled(ctx, tx, c, enabled); serr != nil {
				return serr
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	word := "停用"
	if enabled {
		word = "启用"
	}
	summary := fmt.Sprintf("%s科目 %s %s", word, a.Code, a.Name)
	if len(affected) > 1 {
		summary += fmt.Sprintf("（连同 %d 个下级）", len(affected)-1)
	}
	s.recordAudit(ctx, AuditEvent{
		Action: audit.ActionAccountSave, Summary: summary,
		Entity: "account", EntityID: a.Code,
		Detail: map[string]any{"启用": enabled, "影响的科目": affected},
	})
	return nil
}

// DeleteAccount 删除一个从未用过的科目。
func (s *Service) DeleteAccount(ctx context.Context, code string) error {
	code = strings.TrimSpace(code)
	a, err := s.db.Accounts().GetByCode(ctx, code)
	if err != nil {
		return err
	}
	usage, err := s.db.Accounts().UsageOf(ctx, a.ID)
	if err != nil {
		return err
	}
	if ok, reason := deleteBlockers(a, usage); !ok {
		return fmt.Errorf("不能删除「%s」：%s", a.FullName(nil), reason)
	}
	if err := s.db.Accounts().Delete(ctx, a.ID); err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAccountDelete,
		Summary: fmt.Sprintf("删除科目 %s %s", a.Code, a.Name),
		Entity:  "account", EntityID: a.Code,
		Detail: map[string]any{
			"编码": a.Code, "名称": a.Name, "大类": a.RootType.Label(),
			"（删除时无分录、无余额、无下级）": true,
		},
	})
	return nil
}

// accountRow 重新查一行出来（保证返回的是库里真实的状态）。
func (s *Service) accountRow(ctx context.Context, code string) (*AccountRow, error) {
	view, err := s.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	for i := range view.Rows {
		if view.Rows[i].Code == code {
			return &view.Rows[i], nil
		}
	}
	return nil, fmt.Errorf("科目 %s 保存后读不回来", code)
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseAuxTypes(in []string) []account.AuxType {
	var out []account.AuxType
	seen := map[account.AuxType]bool{}
	for _, s := range in {
		t := account.AuxType(strings.TrimSpace(s))
		if !t.Valid() || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func containsAux(list []account.AuxType, t account.AuxType) bool {
	for _, x := range list {
		if x == t {
			return true
		}
	}
	return false
}

func auxLabels(list []account.AuxType) []string {
	out := make([]string, 0, len(list))
	for _, t := range list {
		out = append(out, t.Label())
	}
	return out
}

// isNotFound 判断「科目不存在」。
//
// 认两个哨兵：存储层的 sqlite.ErrNotFound 与领域层的 account.ErrNotFound
// （两者都可能被包上来）。用 errors.Is 而不是匹配错误文本 ——
// 文本会随包装方式变化，而这类判断一旦漏了就会把「改」当成「增」。
func isNotFound(err error) bool {
	return errors.Is(err, sqlite.ErrNotFound) || errors.Is(err, account.ErrNotFound)
}
