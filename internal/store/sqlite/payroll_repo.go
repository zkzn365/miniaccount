package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/payroll"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
)

// PayrollRepo 是员工、工资单与工资参数的持久化访问。
type PayrollRepo struct{ db *DB }

// Payroll 返回工资模块仓储。
func (db *DB) Payroll() *PayrollRepo { return &PayrollRepo{db: db} }

// 工资模块错误。
var (
	ErrNoEmployee  = fmt.Errorf("payroll: 员工不存在")
	ErrRunExists   = fmt.Errorf("payroll: 该期间已有工资单")
	ErrRunNotDraft = fmt.Errorf("payroll: 工资单状态不允许该操作")
	ErrNoScheme    = fmt.Errorf("payroll: 社保方案不存在")
)

// setting 表的键名。
const (
	settingTaxTable  = "payroll.tax_table"
	settingSchemes   = "payroll.insurance_schemes"
	settingVoucherAc = "payroll.voucher_accounts"
)

// ---------------------------------------------------------------------------
// 员工
// ---------------------------------------------------------------------------

const employeeColumns = `e.id, e.code, e.name, e.id_card, e.kind, e.dept_id, e.position,
	e.hire_date, e.leave_date, e.bank_name, e.bank_account, e.phone,
	e.base_salary, e.si_base, e.hfb_base, e.special_additional,
	a.code, COALESCE(ca.code, ''), e.scheme_name, e.is_enabled, e.remark`

// ListEmployees 返回全部员工。
func (r *PayrollRepo) ListEmployees(ctx context.Context, onlyEnabled bool) ([]*payroll.Employee, error) {
	q := `SELECT ` + employeeColumns + `
	        FROM employee e
	        JOIN account a ON a.id = e.expense_account_id
	        LEFT JOIN account ca ON ca.id = e.company_account_id`
	if onlyEnabled {
		q += ` WHERE e.is_enabled = 1`
	}
	q += ` ORDER BY e.code`

	rows, err := r.db.sql.QueryContext(ctx, q)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	return scanEmployees(rows)
}

func scanEmployees(rows *sql.Rows) ([]*payroll.Employee, error) {
	var out []*payroll.Employee
	for rows.Next() {
		e := &payroll.Employee{}
		var (
			kind, hire, leave                string
			deptID                           sql.NullInt64
			enabled                          int
			salary, siBase, hfbBase, special int64
		)
		if err := rows.Scan(&e.ID, &e.Code, &e.Name, &e.IDCard, &kind, &deptID,
			&e.Position, &hire, &leave, &e.BankName, &e.BankAccount, &e.Phone,
			&salary, &siBase, &hfbBase, &special,
			&e.ExpenseAccountCode, &e.CompanyAccountCode, &e.SchemeName,
			&enabled, &e.Remark); err != nil {
			return nil, err
		}
		e.Kind = payroll.EmployeeKind(kind)
		e.IsEnabled = enabled != 0
		e.BaseSalary = money.Money(salary)
		e.SIBase = money.Money(siBase)
		e.HFBBase = money.Money(hfbBase)
		e.SpecialAdditional = money.Money(special)
		e.DeptID = toNullInt64(deptID)
		if hire != "" {
			d, err := calendar.Parse(hire)
			if err != nil {
				return nil, fmt.Errorf("sqlite: 员工 %s 入职日期非法 %q", e.Code, hire)
			}
			e.HireDate = d
		}
		if leave != "" {
			d, err := calendar.Parse(leave)
			if err != nil {
				return nil, fmt.Errorf("sqlite: 员工 %s 离职日期非法 %q", e.Code, leave)
			}
			e.LeaveDate = d
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpsertEmployee 新增或更新员工。
func (r *PayrollRepo) UpsertEmployee(ctx context.Context, e *payroll.Employee) (int64, error) {
	if err := e.Validate(); err != nil {
		return 0, err
	}
	var id int64
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		accIDs, err := r.db.Accounts().IDsByCodeTx(ctx, tx)
		if err != nil {
			return err
		}
		expID, ok := accIDs[e.ExpenseAccountCode]
		if !ok {
			return fmt.Errorf("%w: 工资费用科目 %s", ErrNotFound, e.ExpenseAccountCode)
		}
		var compID any
		if e.CompanyAccountCode != "" {
			v, ok := accIDs[e.CompanyAccountCode]
			if !ok {
				return fmt.Errorf("%w: 单位社保科目 %s", ErrNotFound, e.CompanyAccountCode)
			}
			compID = v
		}
		now := nowString()
		if e.ID == 0 {
			res, err := tx.Exec(ctx, `
				INSERT INTO employee (code, name, id_card, kind, dept_id, position,
					hire_date, leave_date, bank_name, bank_account, phone,
					base_salary, si_base, hfb_base, special_additional,
					expense_account_id, company_account_id, scheme_name,
					is_enabled, remark, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				e.Code, e.Name, e.IDCard, string(e.Kind), nullInt64(e.DeptID),
				e.Position, e.HireDate.String(), e.LeaveDate.String(),
				e.BankName, e.BankAccount, e.Phone,
				int64(e.BaseSalary), int64(e.SIBase), int64(e.HFBBase),
				int64(e.SpecialAdditional),
				expID, compID, e.SchemeName,
				boolInt(e.IsEnabled), e.Remark, now, now)
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE employee SET code = ?, name = ?, id_card = ?, kind = ?, dept_id = ?,
				position = ?, hire_date = ?, leave_date = ?, bank_name = ?,
				bank_account = ?, phone = ?, base_salary = ?, si_base = ?,
				hfb_base = ?, special_additional = ?,
				expense_account_id = ?, company_account_id = ?,
				scheme_name = ?, is_enabled = ?, remark = ?, updated_at = ?
			 WHERE id = ?`,
			e.Code, e.Name, e.IDCard, string(e.Kind), nullInt64(e.DeptID),
			e.Position, e.HireDate.String(), e.LeaveDate.String(),
			e.BankName, e.BankAccount, e.Phone,
			int64(e.BaseSalary), int64(e.SIBase), int64(e.HFBBase),
			int64(e.SpecialAdditional),
			expID, compID, e.SchemeName,
			boolInt(e.IsEnabled), e.Remark, now, e.ID)
		id = e.ID
		return err
	})
	return id, err
}

// ---------------------------------------------------------------------------
// 参数（税率表 / 社保方案）
// ---------------------------------------------------------------------------

// getSetting 读一个 JSON 设置项；不存在时返回零值。
func (r *PayrollRepo) getSetting(ctx context.Context, key string, out any) (bool, error) {
	var raw string
	err := r.db.sql.QueryRowContext(ctx,
		`SELECT value FROM setting WHERE key = ?`, key).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, translateErr(err)
	}
	if raw == "" {
		return false, nil
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return false, fmt.Errorf("sqlite: 解析设置 %s: %w", key, err)
	}
	return true, nil
}

func (r *PayrollRepo) putSetting(ctx context.Context, tx *Tx, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO setting (key, value, updated_at) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, string(b), nowString())
	return err
}

// TaxTable 返回工资薪金预扣率表；未配置时返回内置默认表。
func (r *PayrollRepo) TaxTable(ctx context.Context) (payroll.TaxTable, error) {
	var t payroll.TaxTable
	ok, err := r.getSetting(ctx, settingTaxTable, &t)
	if err != nil {
		return payroll.TaxTable{}, err
	}
	if !ok {
		t = payroll.DefaultWageTaxTable()
	}
	if len(t.Brackets) == 0 {
		t = payroll.DefaultWageTaxTable()
	}
	if err := t.Validate(); err != nil {
		return payroll.TaxTable{}, err
	}
	return t, nil
}

// SaveTaxTable 保存自定义税率表（政策调整时用）。
func (r *PayrollRepo) SaveTaxTable(ctx context.Context, t payroll.TaxTable) error {
	if err := t.Validate(); err != nil {
		return err
	}
	return r.db.WithTx(ctx, func(tx *Tx) error {
		return r.putSetting(ctx, tx, settingTaxTable, t)
	})
}

// InsuranceSchemes 返回全部社保方案（按名称索引）。
func (r *PayrollRepo) InsuranceSchemes(ctx context.Context) (map[string]*payroll.InsuranceScheme, error) {
	var list []*payroll.InsuranceScheme
	ok, err := r.getSetting(ctx, settingSchemes, &list)
	if err != nil {
		return nil, err
	}
	out := map[string]*payroll.InsuranceScheme{}
	if !ok {
		return out, nil
	}
	for _, s := range list {
		out[s.Name] = s
	}
	return out, nil
}

// SaveInsuranceSchemes 保存社保方案。
//
// ⚠️ 社保比例与基数上下限按城市、按年度发布。调用方应提供
// 经过核对的数据 —— 本项目不内置任何「权威」数值。
func (r *PayrollRepo) SaveInsuranceSchemes(ctx context.Context,
	schemes []*payroll.InsuranceScheme) error {
	// 保存前逐个校验：把「费率填成金额」「上下限写反」这类错误
	// 挡在入库之前，而不是等到算工资时才发现一张工资单全是错的。
	for _, sc := range schemes {
		if err := sc.Validate(); err != nil {
			return err
		}
	}
	return r.db.WithTx(ctx, func(tx *Tx) error {
		return r.putSetting(ctx, tx, settingSchemes, schemes)
	})
}

// VoucherAccounts 返回工资凭证的科目配置；未配置时用默认值。
func (r *PayrollRepo) VoucherAccounts(ctx context.Context) (payroll.VoucherAccounts, error) {
	var vc payroll.VoucherAccounts
	ok, err := r.getSetting(ctx, settingVoucherAc, &vc)
	if err != nil {
		return payroll.VoucherAccounts{}, err
	}
	if !ok || vc.PayableSalary == "" {
		return payroll.DefaultVoucherAccounts(), nil
	}
	return vc, nil
}

// SaveVoucherAccounts 保存工资凭证科目配置。
func (r *PayrollRepo) SaveVoucherAccounts(ctx context.Context, vc payroll.VoucherAccounts) error {
	return r.db.WithTx(ctx, func(tx *Tx) error {
		return r.putSetting(ctx, tx, settingVoucherAc, vc)
	})
}

// ---------------------------------------------------------------------------
// 累计数（累计预扣预缴的核心依赖）
// ---------------------------------------------------------------------------

// priorYTD 返回某员工在本年度截至「指定月份之前」的累计数。
//
// 这是累计预扣预缴法的必需输入。
//
// # 为什么默认把草稿也算进来
//
// 严格按会计口径只应统计「已确认/已过账」的工资单。但实际使用中，
// 用户往往是「先把 1 月建出来放一放，接着建 2 月」——
// 若忽略 1 月草稿，2 月显示的个税会明显偏低，用户会以为软件算错了。
//
// 因此默认包含草稿；需要严格口径时把 onlyPosted 置为 true
// （例如正式对外出报表时）。
func (r *PayrollRepo) priorYTD(ctx context.Context, q querier,
	employeeID int64, year, beforeMonth int, onlyPosted bool) (payroll.YTD, error) {

	statusFilter := "('draft','confirmed','posted')"
	if onlyPosted {
		statusFilter = "('confirmed','posted')"
	}

	row := q.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(si.gross_pay - si.attendance_deduction - si.other_deduction), 0),
			COALESCE(SUM(si.pension_self + si.medical_self + si.unemployment_self
			             + si.housing_fund_self), 0),
			COALESCE(SUM(si.special_additional), 0),
			COALESCE(SUM(si.other_tax_deduction), 0),
			COALESCE(SUM(si.iit), 0)
		  FROM salary_item si
		  JOIN salary_run sr ON sr.id = si.run_id
		 WHERE si.employee_id = ?
		   AND sr.year = ?
		   AND sr.month < ?
		   AND sr.status IN `+statusFilter+``,
		employeeID, year, beforeMonth)

	var (
		months           int
		income           int64
		specialDeduction int64
		specialAddition  int64
		otherDeduction   int64
		taxWithheld      int64
	)
	if err := row.Scan(&months, &income, &specialDeduction, &specialAddition,
		&otherDeduction, &taxWithheld); err != nil {
		return payroll.YTD{}, translateErr(err)
	}
	return payroll.YTD{
		Slips:             months,
		Months:            months,
		Income:            money.Money(income),
		SpecialDeduction:  money.Money(specialDeduction),
		SpecialAdditional: money.Money(specialAddition),
		OtherDeduction:    money.Money(otherDeduction),
		TaxWithheld:       money.Money(taxWithheld),
	}, nil
}

// ---------------------------------------------------------------------------
// 生成工资单
// ---------------------------------------------------------------------------

// BuildRunInput 是生成工资单的参数。
type BuildRunInput struct {
	Period period.Key
	// CreatedBy 是操作人。
	CreatedBy string
	// Amounts 是各员工的应发项目；缺省时只按员工的基本工资占位（0）。
	//
	// 与「自动按档案生成」配合：先按在职员工建行，再由界面填写金额。
	Amounts map[int64]*payroll.Item
	// IncludeEmployeeIDs 为空表示全部在职员工；否则只包含指定的。
	IncludeEmployeeIDs []int64
	// OnlyPostedHistory 为真时，累计数只统计已确认/已过账的历史工资单。
	// 默认 false（含草稿），理由见 priorYTD 的说明。
	OnlyPostedHistory bool
}

// BuildRun 按当前参数生成一张**草稿**工资单。
//
// 计算累计数时会读该员工本年度之前的已确认/已过账工资单，
// 因此**必须先有历史月份的数据**，否则首月个税会算错。
func (r *PayrollRepo) BuildRun(ctx context.Context, in BuildRunInput) (*payroll.Run, error) {
	if in.Period.Year < 1900 || in.Period.Month < 1 || in.Period.Month > 12 {
		return nil, fmt.Errorf("%w: %v", period.ErrInvalidYearMonth, in.Period)
	}

	taxTable, err := r.TaxTable(ctx)
	if err != nil {
		return nil, err
	}
	schemes, err := r.InsuranceSchemes(ctx)
	if err != nil {
		return nil, err
	}
	employees, err := r.ListEmployees(ctx, true)
	if err != nil {
		return nil, err
	}

	include := map[int64]bool{}
	for _, id := range in.IncludeEmployeeIDs {
		include[id] = true
	}

	run := &payroll.Run{
		Period:    payrollPeriodKey(in.Period),
		Status:    payroll.RunDraft,
		TaxTable:  taxTable,
		CreatedBy: in.CreatedBy,
	}

	for _, e := range employees {
		if len(include) > 0 && !include[e.ID] {
			continue
		}
		if !e.IsActiveIn(payrollPeriodKey(in.Period)) {
			continue
		}

		it := &payroll.Item{EmployeeID: e.ID, Employee: e}
		// ★ 基本工资默认取员工档案里的「标准月工资」。
		//
		// 绝大多数员工的绝大多数月份基本工资是不变的；
		// 不取默认值就等于要求用户每月把每个人的工资重敲一遍，
		// 而漏敲一个人的后果是「这个月他没发工资」—— 一个不会报错、
		// 只能靠人看出来的错误。
		it.BaseSalary = e.BaseSalary
		it.SpecialAdditional = e.SpecialAdditional
		// 申报的缴费基数必须带下去：社保按申报基数缴，不是按实发工资。
		it.SIBase, it.HFBBase = e.SIBase, e.HFBBase
		if am, ok := in.Amounts[e.ID]; ok && am != nil {
			if am.BaseSalary != 0 {
				it.BaseSalary = am.BaseSalary
			}
			if am.SpecialAdditional != 0 {
				it.SpecialAdditional = am.SpecialAdditional
			}
			it.PostAllowance = am.PostAllowance
			it.OvertimePay = am.OvertimePay
			it.Bonus = am.Bonus
			it.OtherIncome = am.OtherIncome
			it.AttendanceDeduction = am.AttendanceDeduction
			it.OtherDeduction = am.OtherDeduction
			it.OtherTaxDeduction = am.OtherTaxDeduction
			if am.InsuranceOverridden {
				it.Insurance = am.Insurance
				it.InsuranceOverridden = true
			}
		}

		prior, err := r.priorYTD(ctx, r.db.sql, e.ID, in.Period.Year, in.Period.Month,
			in.OnlyPostedHistory)
		if err != nil {
			return nil, err
		}
		// ★ 减除费用的乘数是「任职月份数」，不是「已有工资单张数」。
		//
		// priorYTD 的 COUNT(*) 统计的是**工资单**，它只用来汇总
		// 累计收入/扣除/已预扣税额这些**金额**；月数另按入职日期算。
		// 两者在「从入职起每月都有单」时才相等 —— 而年中启用软件、
		// 中间漏建某月、年中入职这三种情形都会分叉，
		// 且少算的每个月都是少 5000 元扣除、多扣个税，不会报错。
		//
		// 入职日期缺失时退回工资单张数（无法判断任职月数时的保守选择）。
		if empMonths := payroll.EmployedMonths(e.HireDate, in.Period.Year,
			in.Period.Month); empMonths > 0 {
			prior.Months = empMonths - 1 // priorYTD 是「本月之前」，ComputeItem 会 +1
		}

		var scheme *payroll.InsuranceScheme
		if e.Kind == payroll.KindEmployee && e.SchemeName != "" {
			scheme = schemes[e.SchemeName]
			if scheme == nil {
				return nil, fmt.Errorf("%w: 员工 %s 引用的方案 %q",
					ErrNoScheme, e.Name, e.SchemeName)
			}
		}
		payroll.ComputeItem(it, prior, taxTable, scheme)
		run.Items = append(run.Items, it)
	}

	if len(run.Items) == 0 {
		return nil, fmt.Errorf("payroll: 该期间没有在职员工")
	}
	payroll.SortItemsByEmployee(run.Items)
	return run, nil
}

// payrollPeriodKey 把 period.Key 转成 payroll 包用的轻量结构。
func payrollPeriodKey(k period.Key) payroll.PeriodKey {
	return payroll.NewPeriodKey(k.Year, k.Month)
}

// ---------------------------------------------------------------------------
// 保存 / 读取工资单
// ---------------------------------------------------------------------------

// SaveRun 保存工资单（草稿可覆盖，已确认/已过账不可改）。
func (r *PayrollRepo) SaveRun(ctx context.Context, run *payroll.Run) (int64, error) {
	if err := run.Validate(); err != nil {
		return 0, err
	}
	var id int64
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		// 若已存在同期间的工资单且非草稿，拒绝
		var existingID int64
		var status string
		err := tx.QueryRow(ctx,
			`SELECT id, status FROM salary_run WHERE year = ? AND month = ?`,
			run.Period.Year, run.Period.Month).Scan(&existingID, &status)
		switch {
		case err == nil:
			if payroll.RunStatus(status) != payroll.RunDraft {
				return fmt.Errorf("%w: %d年%02d月已是「%s」",
					ErrRunNotDraft, run.Period.Year, run.Period.Month,
					payroll.RunStatus(status).Label())
			}
			// 覆盖草稿：先删明细
			if _, err := tx.Exec(ctx, `DELETE FROM salary_item WHERE run_id = ?`, existingID); err != nil {
				return err
			}
			id = existingID
			if _, err := tx.Exec(ctx, `
				UPDATE salary_run SET status = ?, tax_table_json = ?, updated_at = ?
				 WHERE id = ?`,
				string(run.Status), mustJSON(run.TaxTable), nowString(), id); err != nil {
				return err
			}
		case err == sql.ErrNoRows:
			t := run.Totals()
			res, err := tx.Exec(ctx, `
				INSERT INTO salary_run (year, month, status, tax_table_json,
					total_gross, total_iit, total_si_self, total_si_company, total_net,
					headcount, created_by, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				run.Period.Year, run.Period.Month, string(run.Status),
				mustJSON(run.TaxTable),
				int64(t.Gross), int64(t.IIT), int64(t.InsuranceSelf),
				int64(t.InsuranceCompany), int64(t.Net), t.Headcount,
				run.CreatedBy, nowString(), nowString())
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			if err != nil {
				return err
			}
		default:
			return translateErr(err)
		}
		run.ID = id

		empIDs, err := r.db.Accounts().IDsByCodeTx(ctx, tx)
		if err != nil {
			return err
		}
		_ = empIDs

		for i, it := range run.Items {
			if err := r.insertSalaryItem(ctx, tx, id, i+1, it); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

func (r *PayrollRepo) insertSalaryItem(ctx context.Context, tx *Tx,
	runID int64, lineNo int, it *payroll.Item) error {

	ins := it.Insurance
	_, err := tx.Exec(ctx, `
		INSERT INTO salary_item (run_id, employee_id, line_no,
			base_salary, post_allowance, overtime_pay, bonus, other_income,
			attendance_deduction, other_deduction,
			pension_self, medical_self, unemployment_self, housing_fund_self,
			si_base, hf_base,
			pension_co, medical_co, unemployment_co, injury_co, maternity_co,
			housing_fund_co,
			special_additional, other_tax_deduction,
			gross_pay, iit, net_pay,
			cum_months, cum_income, cum_special_deduction, cum_special_additional,
			cum_other_deduction, cum_taxable, cum_cumulative_tax, cum_tax_withheld,
			note)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		runID, it.EmployeeID, lineNo,
		int64(it.BaseSalary), int64(it.PostAllowance), int64(it.OvertimePay),
		int64(it.Bonus), int64(it.OtherIncome),
		int64(it.AttendanceDeduction), int64(it.OtherDeduction),
		int64(ins.PensionSelf), int64(ins.MedicalSelf), int64(ins.UnemploymentSelf),
		int64(ins.HousingFundSelf), int64(ins.Base), int64(ins.HousingFundBase),
		int64(ins.PensionCo), int64(ins.MedicalCo), int64(ins.UnemploymentCo),
		int64(ins.InjuryCo), int64(ins.MaternityCo), int64(ins.HousingFundCo),
		int64(it.SpecialAdditional), int64(it.OtherTaxDeduction),
		int64(it.GrossPay), int64(it.IIT), int64(it.NetPay),
		it.Cum.Months, int64(it.Cum.Income), int64(it.Cum.SpecialDeduction),
		int64(it.Cum.SpecialAdditional), int64(it.Cum.OtherDeduction),
		int64(it.Cum.Taxable), int64(it.Cum.CumulativeTax), int64(it.Cum.TaxWithheld),
		"")
	return translateErr(err)
}

// RunSummary 是工资单列表项。
type RunSummary struct {
	ID             int64
	Period         period.Key
	Status         payroll.RunStatus
	Headcount      int
	TotalGross     money.Money
	TotalIIT       money.Money
	TotalSISelf    money.Money
	TotalSICompany money.Money
	TotalNet       money.Money
	AccrualVoucher *int64
	PaymentVoucher *int64
	CreatedBy      string
}

// ListRuns 返回工资单汇总列表。
func (r *PayrollRepo) ListRuns(ctx context.Context) ([]*RunSummary, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT id, year, month, status, headcount, total_gross, total_iit,
		       total_si_self, total_si_company, total_net,
		       accrual_voucher_id, payment_voucher_id, created_by
		  FROM salary_run ORDER BY year DESC, month DESC`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []*RunSummary
	for rows.Next() {
		s := &RunSummary{}
		var status string
		var gross, iit, siSelf, siCo, net int64
		var accrual, payment sql.NullInt64
		if err := rows.Scan(&s.ID, &s.Period.Year, &s.Period.Month, &status,
			&s.Headcount, &gross, &iit, &siSelf, &siCo, &net,
			&accrual, &payment, &s.CreatedBy); err != nil {
			return nil, err
		}
		s.Status = payroll.RunStatus(status)
		s.TotalGross, s.TotalIIT = money.Money(gross), money.Money(iit)
		s.TotalSISelf, s.TotalSICompany = money.Money(siSelf), money.Money(siCo)
		s.TotalNet = money.Money(net)
		s.AccrualVoucher = toNullInt64(accrual)
		s.PaymentVoucher = toNullInt64(payment)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 生成工资凭证
// ---------------------------------------------------------------------------

// PostRunResult 是工资过账结果。
type PostRunResult struct {
	AccrualVoucherID int64
	AccrualNo        string
	PaymentVoucherID int64
	PaymentNo        string
}

// PostRun 为工资单生成**计提**与**发放**两张凭证。
//
// ★ 生成的是**草稿**凭证，不是已过账凭证。
//
// 本工程里过账只发生在账期结算（见 Service.Close →
// VoucherRepo.PostPeriodDraftsInTx）：工资凭证照样先落草稿，
// 和手工录入的凭证一起，在结账时统一过账、统一分配凭证号。
// 所以这里的返回值里 No 是空的 —— 草稿不占号。
//
// 用两张凭证而不是一张：计提是费用确认（当月），发放是资金支付
// （往往在下月）。合成一张会让「当月费用」与「当月银行流水」对不上，
// 也无法处理「计提了但下月才发」的常见情形。
//
// 两张凭证与工资单在同一事务内生成，失败则整体回滚。
func (r *PayrollRepo) PostRun(ctx context.Context, runID int64,
	postingBy string, at time.Time) (*PostRunResult, error) {

	if postingBy == "" {
		return nil, fmt.Errorf("payroll: 缺少记账人")
	}
	run, err := r.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status == payroll.RunPosted {
		return nil, fmt.Errorf("%w: 工资单已过账", ErrRunNotDraft)
	}
	vc, err := r.VoucherAccounts(ctx)
	if err != nil {
		return nil, err
	}

	accrualEntries, err := run.BuildAccrualEntries(vc)
	if err != nil {
		return nil, err
	}
	paymentEntries, err := run.BuildPaymentEntries(vc)
	if err != nil {
		return nil, err
	}

	res := &PostRunResult{}
	err = r.db.WithTx(ctx, func(tx *Tx) error {
		acc, err := r.postPayrollVoucher(ctx, tx, run, accrualEntries,
			postingBy, "计提工资")
		if err != nil {
			return fmt.Errorf("计提凭证: %w", err)
		}
		res.AccrualVoucherID, res.AccrualNo = acc.VoucherID, acc.No

		pay, err := r.postPayrollVoucher(ctx, tx, run, paymentEntries,
			postingBy, "发放工资")
		if err != nil {
			return fmt.Errorf("发放凭证: %w", err)
		}
		res.PaymentVoucherID, res.PaymentNo = pay.VoucherID, pay.No

		_, err = tx.Exec(ctx, `
			UPDATE salary_run SET status = 'posted', accrual_voucher_id = ?,
				payment_voucher_id = ?, updated_at = ? WHERE id = ?`,
			res.AccrualVoucherID, res.PaymentVoucherID, nowString(), runID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// postPayrollVoucher 把一张工资凭证**存成草稿**。
//
// 名字里的 post 是历史包袱（原来这里直接过账）；现在它只落草稿，
// 过账由账期结算统一做。真正的过账入口只有一个：
// VoucherRepo.PostPeriodDraftsInTx。
func (r *PayrollRepo) postPayrollVoucher(ctx context.Context, tx *Tx,
	run *payroll.Run, entries []payroll.Entry,
	postingBy string, remark string) (*PostedVoucher, error) {

	// 工资凭证的日期取该期间最后一天：工资是整月业务，用月末日期
	// 才能落在正确的会计期间内（也避免月初日期落到上一个月）。
	bizDate, err := calendar.New(run.Period.Year, run.Period.Month,
		calendar.DaysInMonth(run.Period.Year, run.Period.Month))
	if err != nil {
		return nil, err
	}

	vc, err := voucher.New(voucher.WordZhuan, bizDate, postingBy)
	if err != nil {
		return nil, err
	}
	vc.Source = voucher.SourceSalary
	runID := run.ID
	vc.SourceID = &runID
	vc.Remark = fmt.Sprintf("%s %d年%02d月", remark, run.Period.Year, run.Period.Month)

	for _, e := range entries {
		if err := vc.AddEntry(ledger.Entry{
			AccountCode: e.AccountCode,
			Summary:     e.Summary,
			Debit:       e.Debit,
			Credit:      e.Credit,
			Aux: ledger.Aux{
				ContactID:  e.ContactID,
				EmployeeID: e.EmployeeID,
				DeptID:     e.DeptID,
			},
		}); err != nil {
			return nil, err
		}
	}

	out, err := r.db.Vouchers().SaveDraftInTx(ctx, tx, DraftInput{
		Voucher: vc, CreatedBy: postingBy, Generated: true,
	})
	if err != nil {
		return nil, err
	}
	return &PostedVoucher{VoucherID: out.VoucherID, No: ""}, nil
}

// loadRun 从数据库读回一张完整工资单（含明细）。
func (r *PayrollRepo) loadRun(ctx context.Context, id int64) (*payroll.Run, error) {
	run := &payroll.Run{}
	var status, taxJSON string
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT id, year, month, status, tax_table_json, created_by
		  FROM salary_run WHERE id = ?`, id).
		Scan(&run.ID, &run.Period.Year, &run.Period.Month, &status, &taxJSON, &run.CreatedBy)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: 工资单 id=%d", ErrNotFound, id)
	}
	if err != nil {
		return nil, translateErr(err)
	}
	run.Status = payroll.RunStatus(status)
	if taxJSON != "" {
		// ★ 税表解码失败必须报出来，不能悄悄换成内置表。
		//
		// 原来这里是 `_ = json.Unmarshal(...)`：解码失败时静默回落到
		// 内置税率表，而**这张表会被写进导出的工资表**（服务层用
		// `len(run.TaxTable.Brackets)` 生成「综合所得 N 级超额累进…」）。
		// 于是工资表可能写着「7 级」，而实际计提用的不是这张表 ——
		// 一份对外交付的文件，内容与它自称的口径对不上。
		if uerr := json.Unmarshal([]byte(taxJSON), &run.TaxTable); uerr != nil {
			return nil, fmt.Errorf(
				"%w: 工资单 %d 的税率表参数已损坏（%v）；"+
					"请重新设置个税参数后重算这张工资单",
				ErrCheckFail, id, uerr)
		}
	}
	if len(run.TaxTable.Brackets) == 0 {
		// 空税表只在「从来没配过」时出现（老账套），此时用内置表是对的 ——
		// 它是**默认值**而不是**损坏后的降级**，两者必须分开处理。
		run.TaxTable = payroll.DefaultWageTaxTable()
	}

	emps, err := r.ListEmployees(ctx, false)
	if err != nil {
		return nil, err
	}
	byID := map[int64]*payroll.Employee{}
	for _, e := range emps {
		byID[e.ID] = e
	}

	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT employee_id, base_salary, post_allowance, overtime_pay, bonus,
			other_income, attendance_deduction, other_deduction,
			pension_self, medical_self, unemployment_self, housing_fund_self,
			si_base, hf_base, pension_co, medical_co, unemployment_co,
			injury_co, maternity_co, housing_fund_co,
			special_additional, other_tax_deduction,
			gross_pay, iit, net_pay,
			cum_months, cum_income, cum_special_deduction, cum_special_additional,
			cum_other_deduction, cum_taxable, cum_cumulative_tax, cum_tax_withheld
		  FROM salary_item WHERE run_id = ? ORDER BY line_no`, id)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	for rows.Next() {
		it := &payroll.Item{}
		// 用命名变量而不是下标数组：列一多，下标极易与 SELECT 顺序漂移，
		// 而且漂移后不会编译报错、只会静默算错。
		var (
			baseSalary, postAllowance, overtimePay, bonus, otherIncome int64
			attendanceDed, otherDed                                    int64
			pensionSelf, medicalSelf, unempSelf, hfSelf                int64
			siBase, hfBase                                             int64
			pensionCo, medicalCo, unempCo, injuryCo, maternityCo, hfCo int64
			specialAdditional, otherTaxDeduction                       int64
			grossPay, iit, netPay                                      int64
			cumMonths                                                  int64
			cumIncome, cumSpecialDed, cumSpecialAdd, cumOtherDed       int64
			cumTaxable, cumCumulativeTax, cumTaxWithheld               int64
		)
		if err := rows.Scan(&it.EmployeeID,
			&baseSalary, &postAllowance, &overtimePay, &bonus, &otherIncome,
			&attendanceDed, &otherDed,
			&pensionSelf, &medicalSelf, &unempSelf, &hfSelf,
			&siBase, &hfBase,
			&pensionCo, &medicalCo, &unempCo, &injuryCo, &maternityCo, &hfCo,
			&specialAdditional, &otherTaxDeduction,
			&grossPay, &iit, &netPay,
			&cumMonths, &cumIncome, &cumSpecialDed, &cumSpecialAdd,
			&cumOtherDed, &cumTaxable, &cumCumulativeTax, &cumTaxWithheld,
		); err != nil {
			return nil, err
		}

		it.BaseSalary = money.Money(baseSalary)
		it.PostAllowance = money.Money(postAllowance)
		it.OvertimePay = money.Money(overtimePay)
		it.Bonus = money.Money(bonus)
		it.OtherIncome = money.Money(otherIncome)
		it.AttendanceDeduction = money.Money(attendanceDed)
		it.OtherDeduction = money.Money(otherDed)
		it.Insurance = payroll.SocialInsurance{
			Base: money.Money(siBase), HousingFundBase: money.Money(hfBase),
			PensionSelf: money.Money(pensionSelf), MedicalSelf: money.Money(medicalSelf),
			UnemploymentSelf: money.Money(unempSelf),
			HousingFundSelf:  money.Money(hfSelf),
			PensionCo:        money.Money(pensionCo), MedicalCo: money.Money(medicalCo),
			UnemploymentCo: money.Money(unempCo), InjuryCo: money.Money(injuryCo),
			MaternityCo: money.Money(maternityCo), HousingFundCo: money.Money(hfCo),
		}
		// 从库里读回的数值不再重算，否则一旦社保方案调整，
		// 历史工资单的数字会跟着变 —— 那等于篡改已发的工资。
		it.InsuranceOverridden = true
		it.SpecialAdditional = money.Money(specialAdditional)
		it.OtherTaxDeduction = money.Money(otherTaxDeduction)
		it.GrossPay = money.Money(grossPay)
		it.IIT = money.Money(iit)
		it.NetPay = money.Money(netPay)
		it.Cum = payroll.YTDAfter{
			Months:            int(cumMonths),
			Income:            money.Money(cumIncome),
			SpecialDeduction:  money.Money(cumSpecialDed),
			SpecialAdditional: money.Money(cumSpecialAdd),
			OtherDeduction:    money.Money(cumOtherDed),
			Taxable:           money.Money(cumTaxable),
			CumulativeTax:     money.Money(cumCumulativeTax),
			TaxWithheld:       money.Money(cumTaxWithheld),
		}
		it.Employee = byID[it.EmployeeID]
		run.Items = append(run.Items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return run, nil
}

// GetRun 返回一张完整工资单。
func (r *PayrollRepo) GetRun(ctx context.Context, id int64) (*payroll.Run, error) {
	return r.loadRun(ctx, id)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

var _ = strings.TrimSpace
