package service

import (
	"context"

	"errors"
	"fmt"
	"miniaccount/internal/domain/audit"
	"sort"
	"strings"
	"time"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/payroll"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/store/sqlite"
)

// 工资模块相关错误。
var (
	ErrNoEmployees = errors.New("工资：还没有员工档案")
)

// defaultPayrollExpenseAccount 是工资费用的默认归集科目（管理费用—工资）。
//
// 生产人员与销售人员应当改成对应的科目：一律记管理费用会
// 低估产品成本、高估期间费用。
const defaultPayrollExpenseAccount = "560201"

// ---------------------------------------------------------------------------
// 员工
// ---------------------------------------------------------------------------

// EmployeeInput 是新增/修改员工的输入。
//
// 金额一律用 **int64 分**：界面在提交前已经把「元」转成分，
// 与凭证录入走同一套约定。
type EmployeeInput struct {
	ID   int64
	Code string
	Name string
	// Kind 是人员类别：employee 在职员工（工资薪金所得）|
	// labor 外聘劳务（劳务报酬所得）。
	//
	// ★ 这两类的个税算法完全不同：工资薪金用累计预扣预缴法，
	// 劳务报酬按**次**预扣（每次收入减除费用后按 20%~40% 预扣）。
	// 填错会让个税算错，因此必须显式区分而不是一律当员工。
	// 为空时按 employee 处理。
	Kind string
	// IDCard 是身份证号，用于专项附加扣除与个税申报。
	IDCard string
	// Phone / BankAccount 用于发放。
	Phone       string
	BankName    string
	BankAccount string
	// BaseSalary 是月基本工资（分）。
	BaseSalary money.Money
	// SIBase 是社保缴费基数（分）；为 0 时按基本工资。
	//
	// 单独一个字段是必要的：很多小微企业按最低基数缴纳，
	// 而最低基数与实发工资无关。用基本工资硬算会算错。
	SIBase money.Money
	// HFBBase 是公积金缴费基数（分）；为 0 时按 SIBase。
	HFBBase money.Money
	// SIProfile 是参保方案名（对应 insurance_scheme 表的 key）。
	SIProfile string
	// SpecialAdditional 是每月专项附加扣除（分）。
	//
	// 子女教育、赡养老人、住房贷款利息等，按月定额。
	// 实务中由员工在「个人所得税」APP 里填报，公司这边照着扣。
	SpecialAdditional money.Money
	// DeptID 是所属部门。
	//
	// ★ 不是可选项：绝大多数费用科目（管理费用、销售费用的各明细）
	// 都声明了按部门辅助核算，员工没有部门就**记不了账** ——
	// 计提分录会被「缺少必需的辅助核算」直接拒绝。
	DeptID *int64
	// Position 是岗位。
	Position string
	// ExpenseAccountCode 是工资费用的归集科目编码。
	//
	// ★ 必须能与管理人员区分开：
	// 生产人员记「生产成本—直接人工」、销售人员记「销售费用—工资」、
	// 管理人员记「管理费用—工资」。一律记管理费用会**低估产品成本、
	// 高估期间费用** —— 而这两个数字分别进利润表的营业成本与期间费用，
	// 报出去的口径就错了。
	//
	// 留空时默认「管理费用—工资」，并在界面上提示确认。
	ExpenseAccountCode string
	// CompanyAccountCode 是单位承担的社保/公积金的归集科目；
	// 留空时与 ExpenseAccountCode 相同。
	CompanyAccountCode string
	// HireDate / LeaveDate 是入职与离职日期（YYYY-MM-DD）。
	HireDate  string
	LeaveDate string
	Enabled   bool
	Remark    string
}

// EmployeeView 是界面上的员工一行。
type EmployeeView struct {
	ID                int64       `json:"id"`
	Code              string      `json:"code"`
	Name              string      `json:"name"`
	IDCard            string      `json:"idCard"`
	Phone             string      `json:"phone"`
	BankName          string      `json:"bankName"`
	BankAccount       string      `json:"bankAccount"`
	BaseSalary        money.Money `json:"baseSalary"`
	SIBase            money.Money `json:"siBase"`
	HFBBase           money.Money `json:"hfbBase"`
	SIProfile         string      `json:"siProfile"`
	SpecialAdditional money.Money `json:"specialAdditional"`
	DeptID            *int64      `json:"deptId"`
	Position          string      `json:"position"`
	// ExpenseAccountCode / ExpenseAccountName 是工资费用的归集科目。
	ExpenseAccountCode string `json:"expenseAccountCode"`
	ExpenseAccountName string `json:"expenseAccountName"`
	HireDate           string `json:"hireDate"`
	LeaveDate          string `json:"leaveDate"`
	Enabled            bool   `json:"enabled"`
	Remark             string `json:"remark"`
	// Kind 是人员类别：employee | labor。
	Kind string `json:"kind"`
	// KindLabel 是「员工 / 劳务」。
	KindLabel string `json:"kindLabel"`
	// StatusLabel 是在职/离职，供列表直接显示。
	StatusLabel string `json:"statusLabel"`
}

func kindLabel(k payroll.EmployeeKind) string {
	if k == payroll.KindLabor {
		return "劳务"
	}
	return "员工"
}

// Employees 返回员工列表。
func (s *Service) Employees(ctx context.Context, onlyEnabled bool) ([]EmployeeView, error) {
	list, err := s.db.Payroll().ListEmployees(ctx, onlyEnabled)
	if err != nil {
		return nil, err
	}
	out := make([]EmployeeView, 0, len(list))
	for _, e := range list {
		out = append(out, toEmployeeView(e))
	}
	return out, nil
}

func toEmployeeView(e *payroll.Employee) EmployeeView {
	v := EmployeeView{
		ID: e.ID, Code: e.Code, Name: e.Name, IDCard: e.IDCard,
		Kind: string(e.Kind), KindLabel: kindLabel(e.Kind),
		Phone: e.Phone, BankName: e.BankName, BankAccount: e.BankAccount,
		BaseSalary: e.BaseSalary, SIBase: e.SIBase, HFBBase: e.HFBBase,
		SIProfile: e.SchemeName, SpecialAdditional: e.SpecialAdditional,
		Enabled: e.IsEnabled, Remark: e.Remark,
		StatusLabel: "在职",
		// ★ 下面这三个字段原来漏了，后果比「列表里少显示一列」严重得多。
		//
		// 界面的「编辑」是把这一行整个展开进表单再提交的
		//（`editEmployee` 里的 `{...row}`）。字段没被带出来，表单里就是
		// 空的，**保存一次就把这个人的部门、岗位、工资费用科目全抹掉了**，
		// 而且没有任何提示。
		//
		// 抹掉部门之后，他的工资计提会被「缺少必需的辅助核算」拒绝；
		// 抹掉工资科目之后会退回默认的「管理费用—工资」，于是生产人员的
		// 工资从「生产成本—直接人工」挪进了期间费用 ——
		// 低估产品成本、高估期间费用，报出去的口径就错了。
		DeptID:             e.DeptID,
		Position:           e.Position,
		ExpenseAccountCode: e.ExpenseAccountCode,
	}
	if e.HireDate.Valid() {
		v.HireDate = e.HireDate.String()
	}
	// 状态标签的优先级：有离职日期就是「离职」，
	// 否则再看启用开关。
	//
	// 顺序不能反：离职的员工通常也会被停用，
	// 若先判启用开关，离职的人会显示成「已停用」——
	// 而「已停用」看起来像是临时关闭，不是离职。
	if e.LeaveDate.Valid() {
		v.LeaveDate = e.LeaveDate.String()
		v.StatusLabel = "离职"
	} else if !e.IsEnabled {
		v.StatusLabel = "已停用"
	}
	return v
}

// SaveEmployee 新增或修改员工。
func (s *Service) SaveEmployee(ctx context.Context, in EmployeeInput) (int64, error) {
	if strings.TrimSpace(in.Name) == "" {
		return 0, errors.New("员工姓名不能为空")
	}
	kind := payroll.EmployeeKind(strings.TrimSpace(in.Kind))
	if kind == "" {
		kind = payroll.KindEmployee
	}
	if !kind.Valid() {
		return 0, fmt.Errorf("人员类别 %q 非法（应为 employee 或 labor）", in.Kind)
	}
	// 工资费用科目：默认「管理费用—工资」。
	//
	// 给默认值而不是强制用户填，是因为小微企业一开始往往
	// 只有一个老板兼全部岗位；但界面上会把这行显式显示出来，
	// 让人有机会在生产/销售人员出现时改掉。
	expCode := strings.TrimSpace(in.ExpenseAccountCode)
	if expCode == "" {
		expCode = defaultPayrollExpenseAccount
	}
	compCode := strings.TrimSpace(in.CompanyAccountCode)
	if compCode == "" {
		compCode = expCode
	}
	e := &payroll.Employee{
		ID: in.ID, Code: in.Code, Name: strings.TrimSpace(in.Name), Kind: kind,
		IDCard: in.IDCard, Phone: in.Phone,
		BankName: in.BankName, BankAccount: in.BankAccount,
		DeptID: in.DeptID, Position: in.Position,
		BaseSalary: in.BaseSalary, SIBase: in.SIBase, HFBBase: in.HFBBase,
		SchemeName: in.SIProfile, SpecialAdditional: in.SpecialAdditional,
		ExpenseAccountCode: expCode, CompanyAccountCode: compCode,
		IsEnabled: in.Enabled, Remark: in.Remark,
	}
	if e.SIBase.IsZero() {
		// 没单独给社保基数时按基本工资 —— 这是最常见的情形，
		// 但**不覆盖**用户显式填的值：按最低基数缴纳的小微企业很多。
		e.SIBase = e.BaseSalary
	}
	if e.HFBBase.IsZero() {
		e.HFBBase = e.SIBase
	}
	if err := parseIntoDate(in.HireDate, &e.HireDate); err != nil {
		return 0, fmt.Errorf("入职日期：%w", err)
	}
	if err := parseIntoDate(in.LeaveDate, &e.LeaveDate); err != nil {
		return 0, fmt.Errorf("离职日期：%w", err)
	}
	return s.db.Payroll().UpsertEmployee(ctx, e)
}

// ---------------------------------------------------------------------------
// 工资单
// ---------------------------------------------------------------------------

// PayrollRunView 是工资单列表的一行。
type PayrollRunView struct {
	ID     int64  `json:"id"`
	Period string `json:"period"`
	Status string `json:"status"`
	// StatusLabel 是中文状态名。
	StatusLabel    string      `json:"statusLabel"`
	Headcount      int         `json:"headcount"`
	TotalGross     money.Money `json:"totalGross"`
	TotalIIT       money.Money `json:"totalIit"`
	TotalSISelf    money.Money `json:"totalSiSelf"`
	TotalSICompany money.Money `json:"totalSiCompany"`
	TotalNet       money.Money `json:"totalNet"`
	// HasAccrualVoucher / HasPaymentVoucher 标记计提与发放凭证是否已生成。
	HasAccrualVoucher bool   `json:"hasAccrualVoucher"`
	HasPaymentVoucher bool   `json:"hasPaymentVoucher"`
	CreatedBy         string `json:"createdBy"`
}

// PayrollRuns 返回工资单列表。
func (s *Service) PayrollRuns(ctx context.Context) ([]PayrollRunView, error) {
	s.recordView(ctx, AuditEvent{
		Action: audit.ActionViewPayroll, Entity: "payroll",
		EntityID: "runs", Summary: "查看工资表列表",
	})
	list, err := s.db.Payroll().ListRuns(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PayrollRunView, 0, len(list))
	for _, r := range list {
		out = append(out, PayrollRunView{
			ID: r.ID, Period: r.Period.String(), Status: string(r.Status),
			StatusLabel: runStatusLabel(r.Status),
			Headcount:   r.Headcount,
			TotalGross:  r.TotalGross, TotalIIT: r.TotalIIT,
			TotalSISelf: r.TotalSISelf, TotalSICompany: r.TotalSICompany,
			TotalNet:          r.TotalNet,
			HasAccrualVoucher: r.AccrualVoucher != nil,
			HasPaymentVoucher: r.PaymentVoucher != nil,
			CreatedBy:         r.CreatedBy,
		})
	}
	return out, nil
}

func runStatusLabel(s payroll.RunStatus) string {
	switch s {
	case payroll.RunDraft:
		return "草稿"
	case payroll.RunConfirmed:
		return "已确认"
	case payroll.RunPosted:
		return "已记账"
	default:
		return string(s)
	}
}

// PayrollItemView 是工资单里的一名员工。
type PayrollItemView struct {
	EmployeeID   int64  `json:"employeeId"`
	EmployeeName string `json:"employeeName"`
	// Gross 是应发工资。
	Gross money.Money `json:"gross"`
	// AttendanceDeduct / OtherDeduct 是考勤扣款与其他扣款。
	AttendanceDeduct money.Money `json:"attendanceDeduct"`
	OtherDeduct      money.Money `json:"otherDeduct"`
	// InsuranceBase / HousingFundBase 是本次实际使用的**缴费基数**。
	//
	// ★ 必须显示出来。这个字段以前没有，于是「员工申报了 4000 基数、
	// 程序却按 20000 应发算社保」这种错误在界面上完全看不出来 ——
	// 工资单上只有一个「个人社保」的数，会计无从判断它对不对。
	// 基数摆在应发工资旁边，一眼就能发现两者不该差这么多。
	InsuranceBase   money.Money `json:"insuranceBase"`
	HousingFundBase money.Money `json:"housingFundBase"`
	// InsuranceSelf / InsuranceCompany 是三险一金个人与单位部分。
	InsuranceSelf    money.Money `json:"insuranceSelf"`
	InsuranceCompany money.Money `json:"insuranceCompany"`
	// SpecialAdditional 是专项附加扣除。
	SpecialAdditional money.Money `json:"specialAdditional"`
	// TaxableIncome 是应纳税所得额（累计口径），便于核对。
	TaxableIncome money.Money `json:"taxableIncome"`
	// TaxWarning 是需要用户注意的个税问题（目前只有「任职月份数多于
	// 工资单张数」一项：累计收入缺月份 → 个税会少扣）。
	TaxWarning string `json:"taxWarning"`
	// IIT 是本月代扣个税。
	IIT money.Money `json:"iit"`
	// Net 是实发工资。
	Net money.Money `json:"net"`
}

// PayrollRunDetail 是一张工资单的完整内容。
type PayrollRunDetail struct {
	ID     int64  `json:"id"`
	Period string `json:"period"`
	Status string `json:"status"`
	// StatusLabel 是中文状态名。
	StatusLabel string `json:"statusLabel"`
	// TaxNote 说明本次用的税率表来源。
	TaxNote string            `json:"taxNote"`
	Items   []PayrollItemView `json:"items"`
	// Headcount 是人数。
	Headcount int `json:"headcount"`
	// 合计
	TotalGross     money.Money `json:"totalGross"`
	TotalIIT       money.Money `json:"totalIit"`
	TotalSISelf    money.Money `json:"totalSiSelf"`
	TotalSICompany money.Money `json:"totalSiCompany"`
	TotalNet       money.Money `json:"totalNet"`

	AccrualVoucherNo string `json:"accrualVoucherNo"`
	PaymentVoucherNo string `json:"paymentVoucherNo"`

	// CanConfirm / CanPost 由服务层算好，界面照着禁用按钮。
	CanConfirm bool `json:"canConfirm"`
	CanPost    bool `json:"canPost"`
}

// PayrollRun 返回一张工资单的完整内容。
func (s *Service) PayrollRun(ctx context.Context, id int64) (*PayrollRunDetail, error) {
	run, err := s.db.Payroll().GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	runt := run.Totals()
	d := &PayrollRunDetail{
		ID: run.ID, Period: run.Period.String(), Status: string(run.Status),
		StatusLabel: runStatusLabel(run.Status),
		Headcount:   runt.Headcount,
		CanConfirm:  run.Status == payroll.RunDraft,
		CanPost:     run.Status == payroll.RunDraft || run.Status == payroll.RunConfirmed,
	}
	t := run.Totals()
	d.TotalGross = t.Gross
	d.TotalIIT = t.IIT
	d.TotalSISelf = t.InsuranceSelf
	d.TotalSICompany = t.InsuranceCompany
	d.TotalNet = t.Net
	d.TaxNote = fmt.Sprintf("综合所得 %d 级超额累进（累计预扣预缴法）",
		len(run.TaxTable.Brackets))

	// 凭证号：从 salary_run 的 accrual_voucher_id / payment_voucher_id 取。
	//
	// 记完账才可能有值；草稿状态下两个都是 NULL，界面显示「—」。
	vnos, verr := s.voucherNos(ctx)
	if verr != nil {
		return nil, verr
	}
	var accrualID, paymentID *int64
	if err := s.db.SQL().QueryRowContext(ctx,
		`SELECT accrual_voucher_id, payment_voucher_id FROM salary_run WHERE id = ?`,
		id).Scan(&accrualID, &paymentID); err != nil {
		return nil, translate(err)
	}
	if accrualID != nil {
		d.AccrualVoucherNo = vnos[*accrualID]
	}
	if paymentID != nil {
		d.PaymentVoucherNo = vnos[*paymentID]
	}

	for _, it := range run.Items {
		d.Items = append(d.Items, PayrollItemView{
			EmployeeID: it.EmployeeID, EmployeeName: itemName(it),
			Gross: it.GrossPay, AttendanceDeduct: it.AttendanceDeduction,
			OtherDeduct:       it.OtherDeduction,
			TaxWarning:        it.TaxWarning,
			InsuranceBase:     it.Insurance.Base,
			HousingFundBase:   it.Insurance.HousingFundBase,
			InsuranceSelf:     it.Insurance.SelfTotal(),
			InsuranceCompany:  it.Insurance.CompanyTotal(),
			SpecialAdditional: it.SpecialAdditional,
			TaxableIncome:     it.Cum.Taxable,
			IIT:               it.IIT, Net: it.NetPay,
		})
	}
	return d, nil
}

// BuildPayrollInput 是生成工资单的输入。
type BuildPayrollInput struct {
	Year  int
	Month int
	// CreatedBy 是操作人。
	CreatedBy string
	// IncludeEmployeeIDs 为空表示全部在职员工。
	IncludeEmployeeIDs []int64
	// Amounts 是逐员工的可变项目（加班费、考勤扣款等），可为空。
	Amounts map[int64]*PayrollAmountInput
	// OnlyPostedHistory 为真时累计数只统计已确认/已过账的历史工资单。
	OnlyPostedHistory bool
}

// PayrollAmountInput 是一名员工当月的可变项目。
type PayrollAmountInput struct {
	// Gross 是应发合计（分）。为 0 时用员工档案里的基本工资。
	Gross money.Money
	// AttendanceDeduct / OtherDeduct 是扣款（分）。
	AttendanceDeduct money.Money
	OtherDeduct      money.Money
	// SpecialAdditional 是本月专项附加扣除（分）；为 0 时用档案里的值。
	SpecialAdditional money.Money
}

// BuildPayroll 按当前员工档案生成一张**草稿**工资单。
//
// # 为什么是草稿
//
// 工资涉及个税代扣代缴，算错了要更正申报。因此流程刻意分成
//
//	生成草稿 → 人工核对 → 确认 → 生成计提/发放凭证
//
// 而不是「一键算完直接记账」。中间那一步「人工核对」不是形式：
// 累计预扣预缴的结果依赖全年数据，第一个月与第十二个月的税额可能差很多，
// 而任何一处基础数据错了都会体现为「这个月个税怎么变了」。
func (s *Service) BuildPayroll(ctx context.Context, in BuildPayrollInput) (*PayrollRunDetail, error) {
	k := period.NewKey(in.Year, in.Month)
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", in.Year, in.Month)
	}
	emps, err := s.db.Payroll().ListEmployees(ctx, true)
	if err != nil {
		return nil, err
	}
	if len(emps) == 0 {
		return nil, ErrNoEmployees
	}

	amounts := map[int64]*payroll.Item{}
	for id, a := range in.Amounts {
		amounts[id] = &payroll.Item{
			BaseSalary:          a.Gross,
			AttendanceDeduction: a.AttendanceDeduct,
			OtherDeduction:      a.OtherDeduct,
			SpecialAdditional:   a.SpecialAdditional,
		}
	}

	run, err := s.db.Payroll().BuildRun(ctx, sqlite.BuildRunInput{
		Period: k, CreatedBy: in.CreatedBy,
		Amounts:            amounts,
		IncludeEmployeeIDs: in.IncludeEmployeeIDs,
		OnlyPostedHistory:  in.OnlyPostedHistory,
	})
	if err != nil {
		return nil, err
	}
	id, err := s.db.Payroll().SaveRun(ctx, run)
	if err != nil {
		return nil, err
	}
	return s.PayrollRun(ctx, id)
}

// PostPayroll 把工资单记账，生成计提与发放两张凭证。
func (s *Service) PostPayroll(ctx context.Context, id int64, postingBy string) (*PayrollRunDetail, error) {
	if strings.TrimSpace(postingBy) == "" {
		return nil, errors.New("请填写记账人")
	}
	if _, err := s.db.Payroll().PostRun(ctx, id, strings.TrimSpace(postingBy),
		time.Now()); err != nil {
		return nil, err
	}
	return s.PayrollRun(ctx, id)
}

// PayrollPreview 预演一次工资计算，**不写库**。
//
// 界面上「先看看算出来是多少」用它。带 *PayrollRunDetail 的
// 一次性成本很低，而让用户先看清数字再决定要不要落库，
// 比「先建了再删」友好得多。
func (s *Service) PayrollPreview(ctx context.Context, in BuildPayrollInput) (*PayrollRunDetail, error) {
	k := period.NewKey(in.Year, in.Month)
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", in.Year, in.Month)
	}
	amounts := map[int64]*payroll.Item{}
	for id, a := range in.Amounts {
		amounts[id] = &payroll.Item{
			BaseSalary:          a.Gross,
			AttendanceDeduction: a.AttendanceDeduct,
			OtherDeduction:      a.OtherDeduct,
			SpecialAdditional:   a.SpecialAdditional,
		}
	}
	run, err := s.db.Payroll().BuildRun(ctx, sqlite.BuildRunInput{
		Period: k, CreatedBy: in.CreatedBy,
		Amounts:            amounts,
		IncludeEmployeeIDs: in.IncludeEmployeeIDs,
		OnlyPostedHistory:  in.OnlyPostedHistory,
	})
	if err != nil {
		return nil, err
	}

	runt := run.Totals()
	d := &PayrollRunDetail{
		Period: k.String(), Status: "draft", StatusLabel: "草稿（未保存）",
		Headcount: runt.Headcount,
		TaxNote: fmt.Sprintf("综合所得 %d 级超额累进（累计预扣预缴法）",
			len(run.TaxTable.Brackets)),
	}
	t := run.Totals()
	d.TotalGross = t.Gross
	d.TotalIIT = t.IIT
	d.TotalSISelf = t.InsuranceSelf
	d.TotalSICompany = t.InsuranceCompany
	d.TotalNet = t.Net

	for _, it := range run.Items {
		d.Items = append(d.Items, PayrollItemView{
			EmployeeID: it.EmployeeID, EmployeeName: itemName(it),
			Gross: it.GrossPay, AttendanceDeduct: it.AttendanceDeduction,
			OtherDeduct:       it.OtherDeduction,
			TaxWarning:        it.TaxWarning,
			InsuranceBase:     it.Insurance.Base,
			HousingFundBase:   it.Insurance.HousingFundBase,
			InsuranceSelf:     it.Insurance.SelfTotal(),
			InsuranceCompany:  it.Insurance.CompanyTotal(),
			SpecialAdditional: it.SpecialAdditional,
			TaxableIncome:     it.Cum.Taxable,
			IIT:               it.IIT, Net: it.NetPay,
		})
	}
	return d, nil
}

// ---------------------------------------------------------------------------
// 参数表
// ---------------------------------------------------------------------------

// TaxTableInfo 是当前生效的个税税率表。
type TaxTableInfo struct {
	// Name 是表名，如「综合所得年度税率表」。
	Name string `json:"name"`
	// Brackets 是各级距。
	Brackets []TaxBracketInfo `json:"brackets"`
	// Note 说明这张表的来源与适用范围。
	Note string `json:"note"`
}

// TaxBracketInfo 是一级税率。
type TaxBracketInfo struct {
	// Upper 是累计应纳税所得额上限（分）；0 表示无上限。
	Upper money.Money `json:"upper"`
	// RatePPM 是税率（百万分之一）。
	RatePPM int64 `json:"ratePpm"`
	// RateLabel 是「3%」这样的展示文本。
	RateLabel string `json:"rateLabel"`
	// Deduction 是速算扣除数（分）。
	Deduction money.Money `json:"deduction"`
}

// TaxTable 返回当前生效的个税税率表。
func (s *Service) TaxTable(ctx context.Context) (*TaxTableInfo, error) {
	t, err := s.db.Payroll().TaxTable(ctx)
	if err != nil {
		return nil, err
	}
	out := &TaxTableInfo{
		Name: "个人所得税预扣率表一（居民个人工资、薪金所得预扣预缴适用）",
		Note: "税率与级距全部来自可配置的参数表，不硬编码在代码里 —— " +
			"个税政策每年可能调整，软件更新应当只换数据。",
	}
	for _, b := range t.Brackets {
		out.Brackets = append(out.Brackets, TaxBracketInfo{
			Upper: b.UpperLimit, RatePPM: int64(b.Rate),
			RateLabel: b.Rate.String(),
			Deduction: b.QuickDeduction,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 社保方案
// ---------------------------------------------------------------------------

// InsuranceSchemeInfo 是一个社保方案 + 它的使用情况。
type InsuranceSchemeInfo struct {
	Scheme *payroll.InsuranceScheme `json:"scheme"`
	// UsedBy 是引用该方案的员工数。
	//
	// 界面上必须显示它：删掉一个正在被使用的方案，会让那些员工的
	// 社保静默变成 0 —— 一张看起来正常、实际没扣社保的工资单。
	UsedBy int `json:"usedBy"`
	// Configured 为假表示费率还是全零（尚未按当地标准填写）。
	Configured bool `json:"configured"`
}

// InsuranceSchemes 返回全部社保方案及其使用情况。
func (s *Service) InsuranceSchemes(ctx context.Context) ([]*InsuranceSchemeInfo, error) {
	m, err := s.db.Payroll().InsuranceSchemes(ctx)
	if err != nil {
		return nil, err
	}
	emps, err := s.db.Payroll().ListEmployees(ctx, false)
	if err != nil {
		return nil, err
	}
	used := map[string]int{}
	for _, e := range emps {
		if e.SchemeName != "" {
			used[e.SchemeName]++
		}
	}

	out := make([]*InsuranceSchemeInfo, 0, len(m))
	for _, sc := range m {
		out = append(out, &InsuranceSchemeInfo{
			Scheme: sc, UsedBy: used[sc.Name], Configured: sc.IsConfigured(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scheme.Name < out[j].Scheme.Name })
	return out, nil
}

// SchemeTemplate 返回一张费率为零的空方案，供界面「新增」用。
//
// 刻意不预置任何比例：预置一组看着像真的数字，用户会直接保存就开始用，
// 而那几乎必然与当地当年标准不符，且算错了不会报任何错。
func (s *Service) SchemeTemplate(name string) *payroll.InsuranceScheme {
	return payroll.SchemeTemplate(name)
}

// SaveInsuranceSchemes 保存全部社保方案（整体替换）。
func (s *Service) SaveInsuranceSchemes(ctx context.Context,
	schemes []*payroll.InsuranceScheme) error {
	if len(schemes) == 0 {
		return fmt.Errorf("至少要保留一个社保方案（可为空表待填写）")
	}
	// 名称不能重复：员工档案按名称引用方案，重名会让引用变得不确定
	seen := map[string]bool{}
	for _, sc := range schemes {
		if err := sc.Validate(); err != nil {
			return err
		}
		if seen[sc.Name] {
			return fmt.Errorf("社保方案名称重复：%s", sc.Name)
		}
		seen[sc.Name] = true
	}
	return s.db.Payroll().SaveInsuranceSchemes(ctx, schemes)
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// itemName 取工资单里一名员工的姓名。
//
// 姓名字段在 Item 上是**非导出**的（由 Employee 指针派生），
// 服务层要展示它，因此在这里取一次。放在服务层而不是把它导出：
// 导出会让人以为可以随便改，而这个值必须与 Employee 保持一致。
func itemName(it *payroll.Item) string {
	if it.Employee != nil {
		return it.Employee.Name
	}
	return fmt.Sprintf("员工#%d", it.EmployeeID)
}

// parseIntoDate 解析可选的日期字符串。
func parseIntoDate(s string, out *dateT) error {
	s = strings.TrimSpace(s)
	if s == "" {
		*out = dateT{}
		return nil
	}
	d, err := parseDate(s)
	if err != nil {
		return fmt.Errorf("%q 格式不对，应为 YYYY-MM-DD", s)
	}
	*out = d
	return nil
}
