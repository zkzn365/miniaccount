package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/asset"
	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
	"miniaccount/internal/store/sqlite"
)

// ---------------------------------------------------------------------------
// 固定资产与费用摊销
// ---------------------------------------------------------------------------
//
// # 为什么这两件事要进「账套」而不是进 Excel
//
// 折旧和摊销是**唯一**两笔「什么业务都没发生、但每月必须记账」的分录。
// 用 Excel 记的问题不是麻烦，是**没有一个时点会提醒你漏了**：
// 业务凭证漏了，银行流水会对不上；折旧漏了，账面上什么都不缺 ——
// 只是费用少了一块、利润多了一块，而报表看上去完全正常。
//
// 所以这里做的事是：把「每月要提多少」固化成卡片，
// 每期计提写一条**带唯一约束**的记录。重复提会被数据库挡住，
// 漏提则会在计提预览里以「本期未计提」的形式露出来。
//
// ★ 生成的是**草稿**凭证：过账只发生在账期结算（见 Service.Close），
// 与手工凭证、工资凭证走同一条路。

// ---------------------------------------------------------------------------
// 固定资产
// ---------------------------------------------------------------------------

// AssetInput 是界面提交的固定资产卡片。
type AssetInput struct {
	ID           int64
	Code         string
	Name         string
	Category     string
	DeptID       *int64
	OrigValue    money.Money
	SalvagePPM   int64
	UsefulMonths int
	StartDate    string
	// ExpenseAccount / AccumAccount 留空用默认值。
	ExpenseAccount string
	AccumAccount   string
	DisposedDate   string
	Remark         string
}

// 固定资产模块的默认科目。
const (
	defaultDepreciationExpense = "560205" // 管理费用—折旧费
	defaultAccumDepreciation   = "1602"   // 累计折旧
	defaultAssetAccount        = "1601"   // 固定资产
	defaultAmortExpense        = "560210" // 管理费用—租赁费（待摊费用常见来源）
	defaultAmortAsset          = "1801"   // 长期待摊费用
)

// AssetView 是界面上的固定资产一行。
type AssetView struct {
	ID            int64  `json:"id"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Category      string `json:"category"`
	CategoryLabel string `json:"categoryLabel"`
	DeptID        *int64 `json:"deptId"`
	DeptName      string `json:"deptName"`
	// OrigValue 是原值。
	OrigValue money.Money `json:"origValue"`
	// SalvagePPM 是净残值率（百万分比），SalvageRateLabel 是给人看的。
	SalvagePPM       int64       `json:"salvagePpm"`
	SalvageRateLabel string      `json:"salvageRateLabel"`
	SalvageValue     money.Money `json:"salvageValue"`
	UsefulMonths     int         `json:"usefulMonths"`
	StartDate        string      `json:"startDate"`
	ExpenseAccount   string      `json:"expenseAccount"`
	AccumAccount     string      `json:"accumAccount"`
	Status           string      `json:"status"`
	StatusLabel      string      `json:"statusLabel"`
	DisposedDate     string      `json:"disposedDate"`
	Remark           string      `json:"remark"`

	// MonthlyAmount 是常规月份的折旧额。
	MonthlyAmount money.Money `json:"monthlyAmount"`
	// Depreciated 是累计已提折旧。
	Depreciated money.Money `json:"depreciated"`
	// NetValue 是账面净值 = 原值 − 累计折旧。
	NetValue money.Money `json:"netValue"`
	// FirstPeriod / LastPeriod 是起提与止提的期间（让人一眼看出错位没有）。
	FirstPeriod string `json:"firstPeriod"`
	LastPeriod  string `json:"lastPeriod"`
	// MinYears 是该类别税法最低折旧年限；LifeWarning 非空表示短于它。
	MinYears    int    `json:"minYears"`
	LifeWarning string `json:"lifeWarning"`
	// CanDelete 为假时 Reason 说明为什么。
	CanDelete bool   `json:"canDelete"`
	Reason    string `json:"reason"`
}

// AmortizationInput 是界面提交的待摊项目。
type AmortizationInput struct {
	ID             int64
	Code           string
	Name           string
	DeptID         *int64
	Total          money.Money
	Months         int
	StartDate      string
	ExpenseAccount string
	AssetAccount   string
	Remark         string
}

// AmortizationView 是界面上的待摊项目一行。
type AmortizationView struct {
	ID             int64       `json:"id"`
	Code           string      `json:"code"`
	Name           string      `json:"name"`
	DeptID         *int64      `json:"deptId"`
	DeptName       string      `json:"deptName"`
	Total          money.Money `json:"total"`
	Months         int         `json:"months"`
	StartDate      string      `json:"startDate"`
	ExpenseAccount string      `json:"expenseAccount"`
	AssetAccount   string      `json:"assetAccount"`
	Status         string      `json:"status"`
	StatusLabel    string      `json:"statusLabel"`
	Remark         string      `json:"remark"`

	MonthlyAmount money.Money `json:"monthlyAmount"`
	Amortized     money.Money `json:"amortized"`
	Remaining     money.Money `json:"remaining"`
	FirstPeriod   string      `json:"firstPeriod"`
	LastPeriod    string      `json:"lastPeriod"`
	CanDelete     bool        `json:"canDelete"`
	Reason        string      `json:"reason"`
}

// AssetsView 是固定资产 / 摊销页的全部数据。
type AssetsView struct {
	// Assets 是固定资产卡片。
	Assets []AssetView `json:"assets"`
	// Amortizations 是待摊项目。
	Amortizations []AmortizationView `json:"amortizations"`
	// Defaults 是新建表单的默认科目。
	Defaults AssetDefaults `json:"defaults"`
	// Categories 是税法类别选项。
	Categories []AssetCategoryOption `json:"categories"`
}

// AssetDefaults 是新建卡片时的默认科目。
type AssetDefaults struct {
	DepreciationExpense string `json:"depreciationExpense"`
	AccumDepreciation   string `json:"accumDepreciation"`
	AssetAccount        string `json:"assetAccount"`
	AmortExpense        string `json:"amortExpense"`
	AmortAsset          string `json:"amortAsset"`
}

// AssetCategoryOption 是一个税法类别选项。
type AssetCategoryOption struct {
	Value     string `json:"value"`
	Label     string `json:"label"`
	MinYears  int    `json:"minYears"`
	MinMonths int    `json:"minMonths"`
}

// Assets 返回固定资产与待摊项目的全部档案。
func (s *Service) Assets(ctx context.Context) (*AssetsView, error) {
	rows, err := s.db.Assets().Assets(ctx)
	if err != nil {
		return nil, err
	}
	amorts, err := s.db.Assets().Amortizations(ctx)
	if err != nil {
		return nil, err
	}
	deptNames, err := s.departmentNames(ctx)
	if err != nil {
		return nil, err
	}
	out := &AssetsView{
		Assets:        []AssetView{},
		Amortizations: []AmortizationView{},
		Defaults: AssetDefaults{
			DepreciationExpense: defaultDepreciationExpense,
			AccumDepreciation:   defaultAccumDepreciation,
			AssetAccount:        defaultAssetAccount,
			AmortExpense:        defaultAmortExpense,
			AmortAsset:          defaultAmortAsset,
		},
	}
	for _, c := range asset.Categories() {
		out.Categories = append(out.Categories, AssetCategoryOption{
			Value: string(c), Label: c.Label(),
			MinYears: c.MinYears(), MinMonths: c.MinMonths(),
		})
	}
	for _, a := range rows {
		v := toAssetView(a, deptNames)
		total, err := s.db.Assets().DepreciatedTotal(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		v.Depreciated = total
		v.NetValue = a.OrigValue.Sub(total)
		// ★ 能不能删，看的是**有没有计提记录**，不是「折旧表排没排」。
		// 一张下个月才开始提的卡片，折旧表是满的但记录是空的 ——
		// 按折旧表判断会把它的删除按钮藏起来，用户以为删不掉。
		// 计提额恒为正，所以「累计为 0」等价于「一条记录都没有」。
		v.CanDelete = !total.IsPositive()
		if !v.CanDelete {
			v.Reason = "已经计提过折旧，不能删除（折旧凭证是账的一部分）；" +
				"不再使用请改用「处置」——处置当月照提，次月起停"
		}
		out.Assets = append(out.Assets, v)
	}
	for _, m := range amorts {
		v := toAmortView(m, deptNames)
		total, err := s.db.Assets().AmortizedTotal(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		v.Amortized = total
		v.Remaining = m.Total.Sub(total)
		v.CanDelete = !total.IsPositive()
		if !v.CanDelete {
			v.Reason = "已经摊销过，不能删除（摊销凭证是账的一部分）；" +
				"不再摊销请改用「作废」——作废之后新期间不再摊，历史保留"
		}
		out.Amortizations = append(out.Amortizations, v)
	}
	return out, nil
}

func toAssetView(a asset.FixedAsset, deptNames map[int64]string) AssetView {
	v := AssetView{
		ID: a.ID, Code: a.Code, Name: a.Name,
		Category: string(a.Category), CategoryLabel: a.Category.Label(),
		DeptID: a.DeptID, OrigValue: a.OrigValue,
		SalvagePPM: a.SalvagePPM, SalvageValue: a.Salvage(),
		SalvageRateLabel: formatRate(a.SalvagePPM),
		UsefulMonths:     a.UsefulMonths,
		StartDate:        a.StartDate.String(),
		ExpenseAccount:   a.ExpenseAccount, AccumAccount: a.AccumAccount,
		Status: string(a.Status), StatusLabel: assetStatusLabel(a.Status),
		Remark:        a.Remark,
		MonthlyAmount: a.MonthlyAmount(),
		FirstPeriod:   a.FirstPeriod().String(),
		LastPeriod:    a.LastDepreciablePeriod().String(),
		MinYears:      a.Category.MinYears(),
	}
	if a.DeptID != nil {
		v.DeptName = deptNames[*a.DeptID]
	}
	if a.DisposedDate.Valid() {
		v.DisposedDate = a.DisposedDate.String()
	}
	if a.UsefulMonths > 0 && a.Category.MinMonths() > 0 &&
		a.UsefulMonths < a.Category.MinMonths() {
		v.LifeWarning = fmt.Sprintf(
			"预计使用 %d 个月，短于税法的 %d 年（%d 个月）。"+
				"会计上可以这样提，但每期要做纳税调整（税法只认最低年限内的折旧）。",
			a.UsefulMonths, a.Category.MinYears(), a.Category.MinMonths())
	}
	return v
}

func toAmortView(m asset.Amortization, deptNames map[int64]string) AmortizationView {
	v := AmortizationView{
		ID: m.ID, Code: m.Code, Name: m.Name, DeptID: m.DeptID,
		Total: m.Total, Months: m.Months, StartDate: m.StartDate.String(),
		ExpenseAccount: m.ExpenseAccount, AssetAccount: m.AssetAccount,
		Status: string(m.Status), StatusLabel: amortStatusLabel(m.Status),
		Remark:        m.Remark,
		MonthlyAmount: m.MonthlyAmount(),
		FirstPeriod:   m.FirstPeriod().String(),
		LastPeriod:    m.LastPeriod().String(),
	}
	if m.DeptID != nil {
		v.DeptName = deptNames[*m.DeptID]
	}
	return v
}

func assetStatusLabel(s asset.Status) string {
	if s == asset.StatusDisposed {
		return "已处置"
	}
	return "在用"
}

func amortStatusLabel(s asset.AmortStatus) string {
	switch s {
	case asset.AmortFinished:
		return "已摊完"
	case asset.AmortVoided:
		return "已作废"
	default:
		return "摊销中"
	}
}

// formatRate 把百万分比写成百分比字符串，去掉多余的零。
func formatRate(ppm int64) string {
	// 50000 → 5%，25000 → 2.5%，12345 → 1.2345%
	s := strconv.FormatFloat(float64(ppm)/10000, 'f', -1, 64)
	return s + "%"
}

// departmentNames 返回 id → 部门名。
func (s *Service) departmentNames(ctx context.Context) (map[int64]string, error) {
	list, err := s.Departments(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(list))
	for _, d := range list {
		if d.FullName != "" {
			out[d.ID] = d.FullName
		} else {
			out[d.ID] = d.Name
		}
	}
	return out, nil
}

// SaveAsset 新增或修改一张固定资产卡片。
func (s *Service) SaveAsset(ctx context.Context, in AssetInput) (*AssetView, error) {
	a := asset.FixedAsset{
		ID: in.ID, Code: strings.TrimSpace(in.Code), Name: strings.TrimSpace(in.Name),
		Category: asset.Category(in.Category), DeptID: in.DeptID,
		OrigValue: in.OrigValue, SalvagePPM: in.SalvagePPM,
		UsefulMonths:   in.UsefulMonths,
		ExpenseAccount: firstNonEmpty(in.ExpenseAccount, defaultDepreciationExpense),
		AccumAccount:   firstNonEmpty(in.AccumAccount, defaultAccumDepreciation),
		Status:         asset.StatusInUse, Remark: in.Remark,
	}
	if a.Category == "" {
		a.Category = asset.CatElectronic
	}
	if strings.TrimSpace(in.StartDate) == "" {
		return nil, errors.New("请填写投入使用日期 —— 折旧从它的次月起提，填错整个折旧表就错位一格")
	}
	d, err := calendar.Parse(strings.TrimSpace(in.StartDate))
	if err != nil {
		return nil, fmt.Errorf("投入使用日期 %q 格式不对，应为 YYYY-MM-DD", in.StartDate)
	}
	a.StartDate = d
	if strings.TrimSpace(in.DisposedDate) != "" {
		dd, err := calendar.Parse(strings.TrimSpace(in.DisposedDate))
		if err != nil {
			return nil, fmt.Errorf("处置日期 %q 格式不对，应为 YYYY-MM-DD", in.DisposedDate)
		}
		a.Status = asset.StatusDisposed
		a.DisposedDate = dd
	}
	if in.ID != 0 {
		// 改卡片时要保住已有的处置状态：界面上「处置」是单独的动作
		if old, err := s.db.Assets().Asset(ctx, in.ID); err == nil {
			if old.Status == asset.StatusDisposed && a.Status != asset.StatusDisposed {
				a.Status = old.Status
				a.DisposedDate = old.DisposedDate
			}
		}
	}
	if err := s.checkAssetAccounts(ctx, a); err != nil {
		return nil, err
	}
	id, err := s.db.Assets().SaveAsset(ctx, a)
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAssetSave,
		Summary: fmt.Sprintf("维护固定资产「%s」（原值 %s）", a.Name, a.OrigValue),
		Entity:  "fixed_asset", EntityID: strconv.FormatInt(id, 10),
		Detail: map[string]any{
			"名称": a.Name, "原值": a.OrigValue.String(),
			"预计使用月数": a.UsefulMonths,
			"净残值率":   formatRate(a.SalvagePPM),
			"投入使用":   a.StartDate.String(),
			"折旧费用科目": a.ExpenseAccount,
			"累计折旧科目": a.AccumAccount,
		},
	})
	saved, err := s.db.Assets().Asset(ctx, id)
	if err != nil {
		return nil, err
	}
	names, _ := s.departmentNames(ctx)
	v := toAssetView(*saved, names)
	if t, err := s.db.Assets().DepreciatedTotal(ctx, id); err == nil {
		v.Depreciated = t
		v.NetValue = saved.OrigValue.Sub(t)
	}
	return &v, nil
}

// checkAssetAccounts 在**保存卡片时**就检查费用科目与部门。
//
// 不检查的后果是：卡片存得好好的，到月底计提那天才报
// 「缺少必需的辅助核算」—— 那时用户已经忘了这张卡是怎么填的，
// 而且报错指向的是凭证、不是卡片。
func (s *Service) checkAssetAccounts(ctx context.Context, a asset.FixedAsset) error {
	tree, err := s.db.Accounts().Tree(ctx)
	if err != nil {
		return err
	}
	byCode := map[string]*accountRef{}
	for _, acc := range tree.All() {
		byCode[acc.Code] = &accountRef{
			code: acc.Code, name: acc.Name, leaf: acc.IsLeaf,
			enabled: acc.IsEnabled, aux: acc.AuxTypes,
		}
	}
	if err := checkPostable(byCode, a.ExpenseAccount, "折旧费用科目"); err != nil {
		return err
	}
	if err := checkPostable(byCode, a.AccumAccount, "累计折旧科目"); err != nil {
		return err
	}
	if byCode[a.AccumAccount].needsAux(account.AuxDept) {
		return fmt.Errorf("累计折旧科目「%s」要求按部门辅助核算 —— "+
			"累计折旧是按资产整体计提的，不该挂到某个部门上。"+
			"请换一个不要求部门辅助核算的科目（默认 1602）", a.AccumAccount)
	}
	if byCode[a.ExpenseAccount].needsAux(account.AuxDept) && a.DeptID == nil {
		return fmt.Errorf("折旧费用科目「%s」要求按部门辅助核算，因此必须填使用部门 —— "+
			"否则计提那天凭证会被「缺少必需的辅助核算」拒绝", a.ExpenseAccount)
	}
	if a.DeptID != nil {
		if _, err := s.enabledDepartment(ctx, *a.DeptID); err != nil {
			return err
		}
	}
	return nil
}

type accountRef struct {
	code, name string
	leaf       bool
	enabled    bool
	aux        []account.AuxType
}

func (a *accountRef) needsAux(kind account.AuxType) bool {
	for _, x := range a.aux {
		if x == kind {
			return true
		}
	}
	return false
}

func checkPostable(byCode map[string]*accountRef, code, what string) error {
	acc, ok := byCode[code]
	if !ok {
		return fmt.Errorf("%s「%s」不存在", what, code)
	}
	if !acc.leaf {
		return fmt.Errorf("%s「%s %s」是汇总科目，不能直接记账 —— 请选明细科目",
			what, code, acc.name)
	}
	if !acc.enabled {
		return fmt.Errorf("%s「%s %s」已停用", what, code, acc.name)
	}
	return nil
}

// DisposeAsset 处置一张固定资产卡片。
//
// 只标记状态：**处置当月照提折旧**，次月起停（准则第十四条）。
// 处置本身的分录（固定资产清理、净损益）不在这个动作里 ——
// 那要看卖了多少钱、发生了多少清理费用，是另一张手工凭证。
func (s *Service) DisposeAsset(ctx context.Context, id int64, date, reason string) (*AssetView, error) {
	a, err := s.db.Assets().Asset(ctx, id)
	if err != nil {
		return nil, err
	}
	d, err := calendar.Parse(strings.TrimSpace(date))
	if err != nil {
		return nil, fmt.Errorf("处置日期 %q 格式不对，应为 YYYY-MM-DD", date)
	}
	if a.Status == asset.StatusDisposed {
		return nil, fmt.Errorf("「%s」已经处置过了（%s）", a.Name, a.DisposedDate)
	}
	a.Status = asset.StatusDisposed
	a.DisposedDate = d
	// 处置日期必须晚于投入使用日期，否则一期折旧都提不出来
	if d.Before(a.StartDate) {
		return nil, fmt.Errorf("处置日期 %s 早于投入使用日期 %s", d, a.StartDate)
	}
	if _, err := s.db.Assets().SaveAsset(ctx, *a); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAssetDispose,
		Summary: fmt.Sprintf("处置固定资产「%s」", a.Name),
		Entity:  "fixed_asset", EntityID: strconv.FormatInt(id, 10),
		Detail: map[string]any{
			"名称": a.Name, "处置日期": d.String(),
			"说明": firstNonEmpty(reason, "（未填原因）"),
			"提示": "处置当月照提折旧，次月起停；清理损益请另做凭证",
		},
	})
	names, _ := s.departmentNames(ctx)
	v := toAssetView(*a, names)
	if t, err := s.db.Assets().DepreciatedTotal(ctx, id); err == nil {
		v.Depreciated = t
		v.NetValue = a.OrigValue.Sub(t)
	}
	return &v, nil
}

// DeleteAsset 删除一张从未计提过折旧的卡片。
func (s *Service) DeleteAsset(ctx context.Context, id int64) error {
	a, err := s.db.Assets().Asset(ctx, id)
	if err != nil {
		return err
	}
	if err := s.db.Assets().DeleteAsset(ctx, id); err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAssetDelete,
		Summary: fmt.Sprintf("删除固定资产「%s」", a.Name),
		Entity:  "fixed_asset", EntityID: strconv.FormatInt(id, 10),
		Detail: map[string]any{"名称": a.Name, "原值": a.OrigValue.String()},
	})
	return nil
}

// ---------------------------------------------------------------------------
// 费用摊销
// ---------------------------------------------------------------------------

// SaveAmortization 新增或修改一个待摊项目。
func (s *Service) SaveAmortization(ctx context.Context, in AmortizationInput) (*AmortizationView, error) {
	m := asset.Amortization{
		ID: in.ID, Code: strings.TrimSpace(in.Code), Name: strings.TrimSpace(in.Name),
		DeptID: in.DeptID, Total: in.Total, Months: in.Months,
		ExpenseAccount: strings.TrimSpace(in.ExpenseAccount),
		AssetAccount:   firstNonEmpty(in.AssetAccount, defaultAmortAsset),
		Status:         asset.AmortActive, Remark: in.Remark,
	}
	if strings.TrimSpace(in.StartDate) == "" {
		return nil, errors.New("请填写开始摊销日期 —— 受益期从当月起摊，填错整个摊销表就错位一格")
	}
	d, err := calendar.Parse(strings.TrimSpace(in.StartDate))
	if err != nil {
		return nil, fmt.Errorf("开始摊销日期 %q 格式不对，应为 YYYY-MM-DD", in.StartDate)
	}
	m.StartDate = d
	if in.ID != 0 {
		if old, err := s.db.Assets().Amortization(ctx, in.ID); err == nil {
			// 状态由「作废 / 恢复」单独控制，改卡片不该把它改回去
			m.Status = old.Status
		}
	}
	if err := s.checkAmortAccounts(ctx, m); err != nil {
		return nil, err
	}
	id, err := s.db.Assets().SaveAmortization(ctx, m)
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAmortSave,
		Summary: fmt.Sprintf("维护待摊项目「%s」（%s / %d 期）", m.Name, m.Total, m.Months),
		Entity:  "amortization", EntityID: strconv.FormatInt(id, 10),
		Detail: map[string]any{
			"名称": m.Name, "待摊总额": m.Total.String(),
			"摊销月数": m.Months, "开始摊销": m.StartDate.String(),
			"费用科目": m.ExpenseAccount, "待摊科目": m.AssetAccount,
		},
	})
	saved, err := s.db.Assets().Amortization(ctx, id)
	if err != nil {
		return nil, err
	}
	names, _ := s.departmentNames(ctx)
	v := toAmortView(*saved, names)
	if t, err := s.db.Assets().AmortizedTotal(ctx, id); err == nil {
		v.Amortized = t
		v.Remaining = saved.Total.Sub(t)
	}
	return &v, nil
}

func (s *Service) checkAmortAccounts(ctx context.Context, m asset.Amortization) error {
	tree, err := s.db.Accounts().Tree(ctx)
	if err != nil {
		return err
	}
	byCode := map[string]*accountRef{}
	for _, acc := range tree.All() {
		byCode[acc.Code] = &accountRef{
			code: acc.Code, name: acc.Name, leaf: acc.IsLeaf,
			enabled: acc.IsEnabled, aux: acc.AuxTypes,
		}
	}
	if err := checkPostable(byCode, m.ExpenseAccount, "摊销费用科目"); err != nil {
		return err
	}
	if err := checkPostable(byCode, m.AssetAccount, "待摊费用科目"); err != nil {
		return err
	}
	if byCode[m.AssetAccount].needsAux(account.AuxDept) {
		return fmt.Errorf("待摊费用科目「%s」要求按部门辅助核算 —— "+
			"待摊费用是按项目整体摊销的，不该挂到某个部门上。"+
			"请换一个不要求部门辅助核算的科目（默认 1801）", m.AssetAccount)
	}
	if byCode[m.ExpenseAccount].needsAux(account.AuxDept) && m.DeptID == nil {
		return fmt.Errorf("摊销费用科目「%s」要求按部门辅助核算，因此必须填受益部门 —— "+
			"否则摊销那天凭证会被「缺少必需的辅助核算」拒绝", m.ExpenseAccount)
	}
	if m.DeptID != nil {
		if _, err := s.enabledDepartment(ctx, *m.DeptID); err != nil {
			return err
		}
	}
	return nil
}

// VoidAmortization 作废 / 恢复一个待摊项目。
func (s *Service) VoidAmortization(ctx context.Context, id int64, void bool) (*AmortizationView, error) {
	m, err := s.db.Assets().Amortization(ctx, id)
	if err != nil {
		return nil, err
	}
	if void {
		if m.Status == asset.AmortVoided {
			return nil, fmt.Errorf("「%s」已经是作废状态", m.Name)
		}
		m.Status = asset.AmortVoided
	} else {
		if m.Status != asset.AmortVoided {
			return nil, fmt.Errorf("「%s」不是作废状态，无需恢复", m.Name)
		}
		m.Status = asset.AmortActive
	}
	if _, err := s.db.Assets().SaveAmortization(ctx, *m); err != nil {
		return nil, err
	}
	action := "作废待摊项目「"
	if !void {
		action = "恢复待摊项目「"
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAmortSave,
		Summary: action + m.Name + "」",
		Entity:  "amortization", EntityID: strconv.FormatInt(id, 10),
		Detail: map[string]any{"名称": m.Name, "状态": amortStatusLabel(m.Status)},
	})
	names, _ := s.departmentNames(ctx)
	v := toAmortView(*m, names)
	if t, err := s.db.Assets().AmortizedTotal(ctx, id); err == nil {
		v.Amortized = t
		v.Remaining = m.Total.Sub(t)
	}
	return &v, nil
}

// DeleteAmortization 删除一个从未摊销过的项目。
func (s *Service) DeleteAmortization(ctx context.Context, id int64) error {
	m, err := s.db.Assets().Amortization(ctx, id)
	if err != nil {
		return err
	}
	if err := s.db.Assets().DeleteAmortization(ctx, id); err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAmortDelete,
		Summary: fmt.Sprintf("删除待摊项目「%s」", m.Name),
		Entity:  "amortization", EntityID: strconv.FormatInt(id, 10),
		Detail: map[string]any{"名称": m.Name, "待摊总额": m.Total.String()},
	})
	return nil
}

// ---------------------------------------------------------------------------
// 计提
// ---------------------------------------------------------------------------

// AccrualRow 是计提预览里的一行。
type AccrualRow struct {
	Kind      string `json:"kind"`
	KindLabel string `json:"kindLabel"`
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	// Amount 是本期金额；为 0 时 Reason 说明为什么。
	Amount money.Money `json:"amount"`
	// DebitAccount / CreditAccount 是本行会用的科目。
	DebitAccount  string `json:"debitAccount"`
	DebitName     string `json:"debitName"`
	CreditAccount string `json:"creditAccount"`
	CreditName    string `json:"creditName"`
	// AuxDesc 是本行的辅助核算描述（部门名等）。
	AuxDesc string `json:"auxDesc"`
	// Reason 非空表示本期不提，且说明原因。
	Reason string `json:"reason"`
}

// AccrualPreview 是本期计提预览，**不写任何数据**。
type AccrualPreview struct {
	Period string `json:"period"`
	// Rows 是逐条明细（含本期为 0 的，附原因）。
	Rows []AccrualRow `json:"rows"`
	// DepreciationTotal / AmortizationTotal 是两类的合计。
	DepreciationTotal money.Money `json:"depreciationTotal"`
	AmortizationTotal money.Money `json:"amortizationTotal"`
	// DepreciationDone / AmortizationDone 表示本期**已经计提过**。
	DepreciationDone bool `json:"depreciationDone"`
	AmortizationDone bool `json:"amortizationDone"`
	// PreviousPeriod 是上一期，供界面提示「上期还没提」。
	PreviousPeriod string `json:"previousPeriod"`
	// PreviousMissing 是上一期漏提的条数 —— 漏提不会报错，只能这样露出来。
	PreviousMissing int `json:"previousMissing"`
}

// PreviewAccrual 给出某期间的计提预览。
func (s *Service) PreviewAccrual(ctx context.Context, k period.Key) (*AccrualPreview, error) {
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", k.Year, k.Month)
	}
	plan, err := s.buildAccrual(ctx, k)
	if err != nil {
		return nil, err
	}
	out := &AccrualPreview{
		Period:            k.String(),
		Rows:              plan.rows,
		DepreciationTotal: plan.depTotal,
		AmortizationTotal: plan.amortTotal,
		PreviousPeriod:    k.Prev().String(),
	}
	out.DepreciationDone, err = s.accrualDone(ctx, k, asset.KindAsset)
	if err != nil {
		return nil, err
	}
	out.AmortizationDone, err = s.accrualDone(ctx, k, asset.KindAmortization)
	if err != nil {
		return nil, err
	}
	// ★ 漏提一个月**不会报任何错**（少一笔费用而已，报表看上去完全正常），
	// 所以只能反过来查：上一期本该提的每一条，逐条确认有没有落记录。
	// 只判断「上一期整体提过没有」是不够的 —— 上期提过之后又新增一张卡片，
	// 那张卡片就漏了，而整体判断会说「提过了」。
	prev, err := s.buildAccrual(ctx, k.Prev())
	if err != nil {
		return nil, err
	}
	for _, r := range prev.rows {
		if !r.Amount.IsPositive() || r.Reason != "" {
			continue
		}
		done, err := s.accrualItemDone(ctx, k.Prev(), asset.Kind(r.Kind), r.ID)
		if err != nil {
			return nil, err
		}
		if !done {
			out.PreviousMissing++
		}
	}
	return out, nil
}

// accrualItemDone 报告某一条（一张卡片 / 一个待摊项目）在某期是否已计提。
func (s *Service) accrualItemDone(ctx context.Context, k period.Key,
	kind asset.Kind, id int64) (bool, error) {
	if kind == asset.KindAsset {
		return s.db.Assets().HasDepreciationIn(ctx, id, k)
	}
	return s.db.Assets().HasAmortizationIn(ctx, id, k)
}

// accrualDone 报告某期间的某一类是否已经计提过。
func (s *Service) accrualDone(ctx context.Context, k period.Key, kind asset.Kind) (bool, error) {
	if kind == asset.KindAsset {
		rows, err := s.db.Assets().Depreciations(ctx, k)
		if err != nil {
			return false, err
		}
		return len(rows) > 0, nil
	}
	rows, err := s.db.Assets().AmortizationEntries(ctx, k)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

type accrualPlan struct {
	rows       []AccrualRow
	depTotal   money.Money
	amortTotal money.Money
	// 实际要写的记录
	assets []assetAccrual
	amorts []assetAccrual
}

type assetAccrual struct {
	id     int64
	name   string
	amount money.Money
	// deptID 是这一行要挂的部门（费用科目按部门辅助核算时要填）。
	deptID *int64
}

// buildAccrual 算出某期间该提多少，**不写库**。
func (s *Service) buildAccrual(ctx context.Context, k period.Key) (*accrualPlan, error) {
	p := &accrualPlan{rows: []AccrualRow{}}
	if !k.Valid() {
		return p, nil
	}
	deptNames, err := s.departmentNames(ctx)
	if err != nil {
		return nil, err
	}
	tree, err := s.db.Accounts().Tree(ctx)
	if err != nil {
		return nil, err
	}
	accName := func(code string) string {
		for _, a := range tree.All() {
			if a.Code == code {
				return a.Name
			}
		}
		return ""
	}

	list, err := s.db.Assets().Assets(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range list {
		before, err := s.db.Assets().DepreciatedBefore(ctx, a.ID, k)
		if err != nil {
			return nil, err
		}
		amt, why := a.AmountFor(k, before)
		aux := ""
		if a.DeptID != nil {
			aux = deptNames[*a.DeptID]
		}
		row := AccrualRow{
			Kind: string(asset.KindAsset), KindLabel: asset.KindAsset.Label(),
			ID: a.ID, Name: a.Name, Amount: amt,
			DebitAccount: a.ExpenseAccount, DebitName: accName(a.ExpenseAccount),
			CreditAccount: a.AccumAccount, CreditName: accName(a.AccumAccount),
			AuxDesc: aux, Reason: why,
		}
		p.rows = append(p.rows, row)
		if amt.IsPositive() {
			p.depTotal = p.depTotal.Add(amt)
			p.assets = append(p.assets, assetAccrual{id: a.ID, name: a.Name, amount: amt, deptID: a.DeptID})
		}
	}

	amorts, err := s.db.Assets().Amortizations(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range amorts {
		before, err := s.db.Assets().AmortizedBefore(ctx, m.ID, k)
		if err != nil {
			return nil, err
		}
		amt, why := m.AmountFor(k, before)
		if m.Status == asset.AmortVoided {
			why = "项目已作废"
		}
		aux := ""
		if m.DeptID != nil {
			aux = deptNames[*m.DeptID]
		}
		p.rows = append(p.rows, AccrualRow{
			Kind: string(asset.KindAmortization), KindLabel: asset.KindAmortization.Label(),
			ID: m.ID, Name: m.Name, Amount: amt,
			DebitAccount: m.ExpenseAccount, DebitName: accName(m.ExpenseAccount),
			CreditAccount: m.AssetAccount, CreditName: accName(m.AssetAccount),
			AuxDesc: aux, Reason: why,
		})
		if amt.IsPositive() {
			p.amortTotal = p.amortTotal.Add(amt)
			p.amorts = append(p.amorts, assetAccrual{id: m.ID, name: m.Name, amount: amt, deptID: m.DeptID})
		}
	}
	return p, nil
}

// AccrualResult 是一次计提的结果。
type AccrualResult struct {
	Period string `json:"period"`
	// DepreciationVoucherID / AmortizationVoucherID 是生成的两张草稿凭证；
	// 为 0 表示本期那一类没有可提的。
	DepreciationVoucherID int64       `json:"depreciationVoucherId"`
	AmortizationVoucherID int64       `json:"amortizationVoucherId"`
	DepreciationTotal     money.Money `json:"depreciationTotal"`
	AmortizationTotal     money.Money `json:"amortizationTotal"`
	// Skipped 是本期没提的条目及原因。
	Skipped []string `json:"skipped"`
}

// Accrue 计提某期间的折旧与摊销，生成**草稿**凭证。
//
// 折旧与摊销各出一张凭证：一张凭证里既有折旧又有摊销，
// 事后想单独看「本月折旧多少」就得靠摘要去滤，报表也拆不开。
//
// ★ 重复计提会被拒绝（每张资产每期只有一条记录，库里有唯一约束）。
// 漏提一个月不会报任何错，所以预览会额外提示「上一期还有 N 条没提」。
func (s *Service) Accrue(ctx context.Context, k period.Key, operator string) (*AccrualResult, error) {
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", k.Year, k.Month)
	}
	if strings.TrimSpace(operator) == "" {
		return nil, errors.New("请填写操作人")
	}
	plan, err := s.buildAccrual(ctx, k)
	if err != nil {
		return nil, err
	}
	out := &AccrualResult{
		Period: k.String(), Skipped: []string{},
		DepreciationTotal: plan.depTotal, AmortizationTotal: plan.amortTotal,
	}
	for _, r := range plan.rows {
		if r.Amount.IsZero() && r.Reason != "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s「%s」：%s",
				r.KindLabel, r.Name, r.Reason))
		}
	}

	if plan.depTotal.IsPositive() {
		id, err := s.accrueOne(ctx, k, operator, asset.KindAsset, plan)
		if err != nil {
			return nil, err
		}
		out.DepreciationVoucherID = id
	}
	if plan.amortTotal.IsPositive() {
		id, err := s.accrueOne(ctx, k, operator, asset.KindAmortization, plan)
		if err != nil {
			return nil, err
		}
		out.AmortizationVoucherID = id
	}
	return out, nil
}

// accrueOne 生成一张计提凭证（草稿）并写下逐条记录。
func (s *Service) accrueOne(ctx context.Context, k period.Key, operator string,
	kind asset.Kind, plan *accrualPlan) (int64, error) {
	// 凭证日期取期间最后一天：折旧/摊销是整月业务，
	// 用月末日期才能落在正确的会计期间内。
	bizDate, err := calendar.New(k.Year, k.Month, calendar.DaysInMonth(k.Year, k.Month))
	if err != nil {
		return 0, err
	}
	word := voucher.WordZhuan
	doc, err := voucher.New(word, bizDate, strings.TrimSpace(operator))
	if err != nil {
		return 0, err
	}
	doc.Source = voucher.SourceManual

	var items []assetAccrual
	var creditCode, remark string
	switch kind {
	case asset.KindAsset:
		items = plan.assets
		remark = fmt.Sprintf("计提 %d年%02d月 固定资产折旧", k.Year, k.Month)
		doc.Source = voucher.SourceDepreciation
	case asset.KindAmortization:
		items = plan.amorts
		remark = fmt.Sprintf("摊销 %d年%02d月 长期待摊费用", k.Year, k.Month)
		doc.Source = voucher.SourceAmortization
	}
	doc.Remark = remark

	// 借方：一条一行（本工程不合并同科目分录 —— 摘要与辅助核算会丢）
	for _, it := range items {
		row := findRow(plan.rows, kind, it.id)
		if row == nil {
			return 0, fmt.Errorf("内部错误：计提明细里找不到 %s #%d", kind.Label(), it.id)
		}
		if creditCode == "" {
			creditCode = row.CreditAccount
		}
		e := ledger.Entry{
			AccountCode: row.DebitAccount,
			Summary:     fmt.Sprintf("%s %s", kind.Label(), row.Name),
			Debit:       it.amount,
		}
		if it.deptID != nil {
			e.Aux.DeptID = it.deptID
		}
		if err := doc.AddEntry(e); err != nil {
			return 0, fmt.Errorf("第 %d 行：%w", len(doc.Entries)+1, err)
		}
	}
	// 贷方：一笔合计
	if err := doc.AddEntry(ledger.Entry{
		AccountCode: creditCode,
		Summary:     remark,
		Credit:      sumOf(items),
	}); err != nil {
		return 0, err
	}
	var voucherID int64
	err = s.db.WithTx(ctx, func(tx *sqliteTx) error {
		res, err := s.db.Vouchers().SaveDraftInTx(ctx, tx, sqlite.DraftInput{
			Voucher: doc, CreatedBy: strings.TrimSpace(operator), Generated: true,
		})
		if err != nil {
			return err
		}
		voucherID = res.VoucherID
		for _, it := range items {
			switch kind {
			case asset.KindAsset:
				if err := s.db.Assets().SaveDepreciationInTx(ctx, tx,
					it.id, it.name, k, it.amount, &voucherID); err != nil {
					return err
				}
			case asset.KindAmortization:
				if err := s.db.Assets().SaveAmortizationEntryInTx(ctx, tx,
					it.id, it.name, k, it.amount, &voucherID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionAssetAccrue,
		Summary: fmt.Sprintf("%s %s", remark, "（草稿）"),
		Entity:  "voucher", EntityID: strconv.FormatInt(voucherID, 10),
		Operator: strings.TrimSpace(operator),
		Detail: map[string]any{
			"期间": k.String(), "类别": kind.Label(),
			"张数": len(items), "合计": sumOf(items).String(),
		},
	})
	return voucherID, nil
}

func findRow(rows []AccrualRow, kind asset.Kind, id int64) *AccrualRow {
	for i := range rows {
		if rows[i].Kind == string(kind) && rows[i].ID == id {
			return &rows[i]
		}
	}
	return nil
}

func sumOf(items []assetAccrual) money.Money {
	var s money.Money
	for _, it := range items {
		s = s.Add(it.amount)
	}
	return s
}
