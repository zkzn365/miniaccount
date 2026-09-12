package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"miniaccount/internal/domain/money"
	"strings"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/report"
	"miniaccount/internal/domain/vat"
	"miniaccount/internal/domain/voucher"
)

// 账套相关错误。
var (
	ErrBookExists     = fmt.Errorf("sqlite: 账套已存在")
	ErrBookNotSetup   = fmt.Errorf("sqlite: 账套尚未初始化")
	ErrBadTaxType     = fmt.Errorf("sqlite: 纳税人身份非法")
	ErrBadContactKind = fmt.Errorf("sqlite: 往来单位类型非法")
)

// 纳税人身份。
const (
	// TaxTypeGeneral / TaxTypeSmall 是**历史取值**，只用于兼容旧调用方
	// 与旧备份的读取。新代码用 vat.VATGeneral / vat.VATSmallScale。
	TaxTypeGeneral = "general" // 一般纳税人：应交增值税下设进项/销项等专栏
	TaxTypeSmall   = "small"   // 小规模纳税人：只用应交增值税一个科目
)

// Book 是账套元数据。一个账套一个 SQLite 文件，因此这里是单行表。
type Book struct {
	ID           int64
	CompanyName  string
	CreditCode   string // 统一社会信用代码
	LegalPerson  string
	Address      string
	Phone        string
	Email        string
	BankName     string
	BankAccount  string
	Standard     string // 会计准则，固定为「小企业会计准则」
	BaseCurrency string // 本位币，中国小企业固定 CNY

	// TaxType 是**历史字段**，只用于读取旧备份与迁移，应用不再写它。
	//
	// 它把「企业规模类型」与「增值税纳税人身份」混成了一个值，
	// 而那是两个不同依据、不同用途的分类（详见 0010 迁移的说明）。
	// 新代码一律读 EnterpriseScale 与 VATStatus。
	TaxType string

	// EnterpriseScale 是企业规模类型（micro|small|medium|large）。
	//
	// 所得税与统计口径，依据《中小企业划型标准规定》，看营业收入与
	// 从业人员。空串表示**未填写** —— 迁移时无法从旧的 tax_type 反推，
	// 程序不替用户猜。
	EnterpriseScale string
	// VATStatus 是增值税纳税人身份（general|small_scale）。
	//
	// 流转税口径，依据《增值税法》。它是**登记身份**，不是销售额的
	// 函数：年销售额未超 500 万元通常按小规模纳税，但可以自愿登记为
	// 一般纳税人；超过 500 万元应当登记为一般纳税人。
	VATStatus string
	// VATStatusEffectiveFrom 是当前身份的生效日（YYYY-MM-DD）。
	//
	// 身份会变，跨期看账要按**业务发生日**取当时有效的身份，
	// 而不是拿今天的身份去算去年的税。
	VATStatusEffectiveFrom string

	StartYear     int // 启用年度
	StartMonth    int // 启用月份
	SetupComplete bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// StartPeriod 返回账套启用期间。
func (b *Book) StartPeriod() period.Key { return period.NewKey(b.StartYear, b.StartMonth) }

// BookRepo 是账套元数据的持久化访问。
type BookRepo struct{ db *DB }

// Books 返回账套仓储。
func (db *DB) Books() *BookRepo { return &BookRepo{db: db} }

const bookColumns = `id, company_name, credit_code, legal_person, address, phone, email,
	bank_name, bank_account, standard, base_currency, tax_type,
	enterprise_scale, vat_status, vat_status_effective_from,
	start_year, start_month,
	setup_complete, created_at, updated_at`

// Get 读取账套元数据。未初始化时返回 ErrBookNotSetup。
func (r *BookRepo) Get(ctx context.Context) (*Book, error) {
	row := r.db.sql.QueryRowContext(ctx, `SELECT `+bookColumns+` FROM book WHERE id = 1`)
	b := &Book{}
	var (
		setup              int
		createdAt, updated string
	)
	err := row.Scan(&b.ID, &b.CompanyName, &b.CreditCode, &b.LegalPerson, &b.Address,
		&b.Phone, &b.Email, &b.BankName, &b.BankAccount, &b.Standard,
		&b.BaseCurrency, &b.TaxType,
		&b.EnterpriseScale, &b.VATStatus, &b.VATStatusEffectiveFrom,
		&b.StartYear, &b.StartMonth, &setup,
		&createdAt, &updated)
	if err == sql.ErrNoRows {
		return nil, ErrBookNotSetup
	}
	if err != nil {
		return nil, translateErr(err)
	}
	b.SetupComplete = setup != 0
	b.CreatedAt = parseTime(createdAt)
	b.UpdatedAt = parseTime(updated)
	return b, nil
}

// Exists 报告账套是否已初始化。
func (r *BookRepo) Exists(ctx context.Context) (bool, error) {
	var n int
	err := r.db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM book WHERE id = 1`).Scan(&n)
	if err != nil {
		return false, translateErr(err)
	}
	return n > 0, nil
}

// Create 写入账套元数据（单行表，重复写入会被主键拦住）。
func (r *BookRepo) Create(ctx context.Context, tx *Tx, b *Book) error {
	if b.TaxType == "" {
		b.TaxType = TaxTypeGeneral
	}
	if b.TaxType != TaxTypeGeneral && b.TaxType != TaxTypeSmall {
		return fmt.Errorf("%w: %q", ErrBadTaxType, b.TaxType)
	}
	if b.Standard == "" {
		b.Standard = "小企业会计准则"
	}
	if b.BaseCurrency == "" {
		b.BaseCurrency = "CNY"
	}
	now := nowString()
	b.CreatedAt, b.UpdatedAt = time.Now().UTC(), time.Now().UTC()

	_, err := tx.Exec(ctx, `
		INSERT INTO book (id, company_name, credit_code, legal_person, address, phone, email,
			bank_name, bank_account, standard, base_currency, tax_type,
			enterprise_scale, vat_status, vat_status_effective_from,
			start_year, start_month, setup_complete, created_at, updated_at)
		VALUES (1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?)`,
		b.CompanyName, b.CreditCode, b.LegalPerson, b.Address, b.Phone, b.Email,
		b.BankName, b.BankAccount, b.Standard, b.BaseCurrency,
		// tax_type 是历史字段，与 vat_status 保持同值以免旧备份/旧查询读到空
		legacyTaxType(b.VATStatus),
		b.EnterpriseScale, b.VATStatus, b.VATStatusEffectiveFrom,
		b.StartYear, b.StartMonth, now, now)
	if err != nil {
		return err
	}
	// 身份历史的第一条记录，供跨期查询
	if _, err := tx.Exec(ctx, `
		INSERT INTO vat_status_history (status, effective_from, effective_to, note, created_at)
		VALUES (?,?,'','建账时登记',?)`,
		b.VATStatus, b.VATStatusEffectiveFrom, now); err != nil {
		return err
	}
	return nil
}

// legacyStatusFromTaxType 把历史上的 tax_type 取值翻成新的身份取值。
//
// 'small' → 'small_scale'，其余（含空）→ 'general'。
func legacyVATStatus(taxType string) string {
	if taxType == TaxTypeSmall {
		return string(vat.VATSmallScale)
	}
	return string(vat.VATGeneral)
}

// legacyTaxType 把新的纳税人身份映射回历史字段的值。
//
// 只在写入时用一次：旧备份、外部脚本可能还在读 tax_type，
// 让它与 vat_status 保持一致，比让它留空安全。
func legacyTaxType(vatStatus string) string {
	if vatStatus == "small_scale" {
		return "small"
	}
	return "general"
}

// Update 更新账套信息（不改启用期间 —— 启用期间一经确定不允许修改，
// 否则已记账的凭证会落在期间表之外）。
func (r *BookRepo) Update(ctx context.Context, b *Book) error {
	res, err := r.db.sql.ExecContext(ctx, `
		UPDATE book SET company_name = ?, credit_code = ?, legal_person = ?,
			address = ?, phone = ?, email = ?, bank_name = ?, bank_account = ?,
			tax_type = ?, setup_complete = ?, updated_at = ?
		 WHERE id = 1`,
		b.CompanyName, b.CreditCode, b.LegalPerson, b.Address, b.Phone, b.Email,
		b.BankName, b.BankAccount, b.TaxType, boolInt(b.SetupComplete), nowString())
	if err != nil {
		return translateErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBookNotSetup
	}
	return nil
}

// ---------------------------------------------------------------------------
// 建账
// ---------------------------------------------------------------------------

// CreateBookInput 是建账参数。
type CreateBookInput struct {
	CompanyName string
	CreditCode  string
	LegalPerson string
	Address     string
	Phone       string
	Email       string
	BankName    string
	BankAccount string

	// TaxType 是**历史字段**，只用于兼容旧调用方。
	// 新代码请用 VATStatus。
	TaxType string
	// EnterpriseScale 是企业规模类型（micro|small|medium|large）。
	// 允许留空 —— 建账时用户未必想得起来，界面上再提示补填。
	EnterpriseScale string
	// VATStatus 是增值税纳税人身份（general|small_scale）。
	// 留空时按 TaxType 推断，再留空则默认一般纳税人。
	VATStatus string
	// VATStatusEffectiveFrom 是身份生效日（YYYY-MM-DD）。
	// 留空时取账套启用日。
	VATStatusEffectiveFrom string

	StartYear  int
	StartMonth int
	// ThroughYear 是要预生成的期间的最后年度。为 0 时取启用年度 + 1。
	ThroughYear int
	// Current 是「当前期间」，用于决定哪些期间初始为 open。
	// 为 0 值时取启用期间。
	CurrentYear  int
	CurrentMonth int
}

// CreateBook 在一个事务内完成建账：写账套信息 + 预置科目表 + 生成会计期间。
//
// 三步必须同事务：任何一步失败都只会留下半个账套，
// 而半个账套比没有账套更糟 —— 它看起来能用，但科目不全或期间缺失，
// 用户会在记账到一半时才发现。
func (db *DB) CreateBook(ctx context.Context, in CreateBookInput) (*Book, error) {
	if in.CompanyName == "" {
		return nil, fmt.Errorf("sqlite: 公司名称不能为空")
	}
	if in.StartYear < 1900 || in.StartMonth < 1 || in.StartMonth > 12 {
		return nil, fmt.Errorf("%w: 启用期间 %04d-%02d",
			period.ErrInvalidYearMonth, in.StartYear, in.StartMonth)
	}
	// 历史字段仍要校验取值：老调用方传错值时应当报错，
	// 而不是被 legacyVATStatus 悄悄归成 general。
	if in.TaxType != "" && in.TaxType != TaxTypeGeneral && in.TaxType != TaxTypeSmall {
		return nil, fmt.Errorf("%w: %q", ErrBadTaxType, in.TaxType)
	}
	// 增值税纳税人身份：默认一般纳税人；接受旧值 'small' 以便老调用方。
	vatStatus := strings.TrimSpace(in.VATStatus)
	if vatStatus == "" {
		vatStatus = legacyVATStatus(in.TaxType)
	}
	if vatStatus != string(vat.VATGeneral) && vatStatus != string(vat.VATSmallScale) {
		return nil, fmt.Errorf("%w: %q", ErrBadTaxType, vatStatus)
	}
	// 企业规模类型：**允许留空**。
	//
	// 它不是必填 —— 建账时用户未必想得起来自己是小型还是微型，
	// 而填错的代价是「小型微利企业」优惠算错。留空并在界面上提示补填，
	// 比强制用户当场猜一个要诚实。
	scale := strings.TrimSpace(in.EnterpriseScale)
	if scale != "" && !vat.EnterpriseScale(scale).Valid() {
		return nil, fmt.Errorf("%w: 企业规模类型 %q", ErrBadTaxType, scale)
	}
	// 身份生效日：默认取账套启用日。
	//
	// 取启用日是唯一不会与已有分录冲突的选择 —— 不会出现
	// 「身份生效日之前就已经记了账」这种尴尬。
	effFrom := strings.TrimSpace(in.VATStatusEffectiveFrom)
	if effFrom == "" {
		effFrom = fmt.Sprintf("%04d-%02d-01", in.StartYear, in.StartMonth)
	} else if _, perr := calendar.Parse(effFrom); perr != nil {
		return nil, fmt.Errorf("%w: 身份生效日 %q", ErrBadTaxType, effFrom)
	}

	exists, err := db.Books().Exists(ctx)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrBookExists
	}

	through := in.ThroughYear
	if through == 0 {
		through = in.StartYear + 1
	}
	current := period.Key{Year: in.CurrentYear, Month: in.CurrentMonth}
	if current.Year == 0 {
		current = period.NewKey(in.StartYear, in.StartMonth)
	}

	cal, err := period.NewCalendar(in.StartYear, in.StartMonth, through, current)
	if err != nil {
		return nil, err
	}

	b := &Book{
		CompanyName: in.CompanyName, CreditCode: in.CreditCode,
		LegalPerson: in.LegalPerson, Address: in.Address, Phone: in.Phone,
		Email: in.Email, BankName: in.BankName, BankAccount: in.BankAccount,
		TaxType:                legacyTaxType(vatStatus),
		EnterpriseScale:        scale,
		VATStatus:              vatStatus,
		VATStatusEffectiveFrom: effFrom,
		StartYear:              in.StartYear, StartMonth: in.StartMonth,
	}

	err = db.WithTx(ctx, func(tx *Tx) error {
		if err := db.Books().Create(ctx, tx, b); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, stripTxControl(seedAccountsSQL)); err != nil {
			return fmt.Errorf("写入预置科目表: %w", err)
		}
		if err := db.Periods().Insert(ctx, tx, cal.All()); err != nil {
			return fmt.Errorf("生成会计期间: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// 结账前体检
// ---------------------------------------------------------------------------

// HealthLevel 是体检项的严重程度。
type HealthLevel string

// 体检结论等级。
const (
	HealthOK    HealthLevel = "ok"    // 通过
	HealthWarn  HealthLevel = "warn"  // 警告：不阻断结账，但建议先处理
	HealthError HealthLevel = "error" // 错误：必须处理才能结账
)

// HealthItem 是一项体检结果。
type HealthItem struct {
	Key    string      // 机器可读的检查项标识
	Title  string      // 中文标题
	Level  HealthLevel // 结论
	Detail string      // 说明（失败时给出具体数字/凭证号）
	Count  int         // 涉及的记录数
}

// PeriodHealth 是某期间的结账前体检报告。
type PeriodHealth struct {
	Period period.Key
	Items  []HealthItem
}

// CanClose 报告是否满足结账条件（不存在 error 级问题）。
func (h *PeriodHealth) CanClose() bool {
	for _, it := range h.Items {
		if it.Level == HealthError {
			return false
		}
	}
	return true
}

// Errors 返回全部阻断性问题。
func (h *PeriodHealth) Errors() []HealthItem {
	var out []HealthItem
	for _, it := range h.Items {
		if it.Level == HealthError {
			out = append(out, it)
		}
	}
	return out
}

// Summary 返回一句话体检结论，用于阻断结账时的错误提示。
func (h *PeriodHealth) Summary() string {
	if h == nil {
		return ""
	}
	errs, warns := h.Errors(), h.Warnings()
	if len(errs) == 0 {
		return fmt.Sprintf("%s 体检通过（%d 项检查，%d 项提示）",
			h.Period, len(h.Items), len(warns))
	}
	parts := make([]string, 0, len(errs))
	for _, it := range errs {
		if it.Detail != "" {
			parts = append(parts, it.Title+"："+it.Detail)
		} else {
			parts = append(parts, it.Title)
		}
	}
	return fmt.Sprintf("%s 存在 %d 项阻断问题（%s）",
		h.Period, len(errs), strings.Join(parts, "；"))
}

// Warnings 返回全部警告。
func (h *PeriodHealth) Warnings() []HealthItem {
	var out []HealthItem
	for _, it := range h.Items {
		if it.Level == HealthWarn {
			out = append(out, it)
		}
	}
	return out
}

// CheckPeriodHealth 执行结账前体检。
//
// 这是「对账」功能的核心：会计在结账前需要知道还有哪些事情没做完。
// Frappe Books 没有结账概念，也就没有这套检查，用户只能自己逐项排查。
//
// 检查项：
//
//  1. 试算平衡（Σ借 == Σ贷）—— 不成立说明账本身坏了，必须阻断
//  2. 本期无草稿凭证 —— 有草稿说明有未完成的录入
//  3. 凭证字号连续 —— 断号/重号违反《会计基础工作规范》
//  4. 资产负债表勾稽 —— 行53 == 行30，不成立必须阻断
//  5. 现金/银行负余额 —— 账面透支通常是漏记或错记
//  6. 往来余额方向异常 —— 应收出现大额贷方等
//  7. 有凭证无附件（提示）
func (db *DB) CheckPeriodHealth(ctx context.Context, k period.Key) (*PeriodHealth, error) {
	h := &PeriodHealth{Period: k}
	add := func(it HealthItem) { h.Items = append(h.Items, it) }

	// 1. 试算平衡
	d, c, err := db.Vouchers().TrialBalance(ctx, k)
	if err != nil {
		return nil, err
	}
	if d == c {
		add(HealthItem{Key: "trial_balance", Title: "试算平衡", Level: HealthOK,
			Detail: fmt.Sprintf("借方合计 %s = 贷方合计 %s", d, c)})
	} else {
		add(HealthItem{Key: "trial_balance", Title: "试算平衡", Level: HealthError,
			Detail: fmt.Sprintf("借方 %s ≠ 贷方 %s，差额 %s", d, c, d.Sub(c))})
	}

	// 2. 草稿凭证
	var drafts int
	if err := db.sql.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM voucher
		 WHERE year = ? AND month = ? AND status = 'draft'`, k.Year, k.Month).
		Scan(&drafts); err != nil {
		return nil, translateErr(err)
	}
	if drafts == 0 {
		add(HealthItem{Key: "draft_vouchers", Title: "无草稿凭证", Level: HealthOK})
	} else {
		add(HealthItem{Key: "draft_vouchers", Title: "存在草稿凭证", Level: HealthWarn,
			Count: drafts, Detail: fmt.Sprintf("本期有 %d 张草稿凭证尚未过账", drafts)})
	}

	// 3. 凭证字号连续性
	vs, err := db.Vouchers().ListByPeriod(ctx, k)
	if err != nil {
		return nil, err
	}
	seqIssues := voucher.CheckSequence(vs)
	if len(seqIssues) == 0 {
		add(HealthItem{Key: "voucher_sequence", Title: "凭证字号连续", Level: HealthOK})
	} else {
		add(HealthItem{Key: "voucher_sequence", Title: "凭证字号不连续", Level: HealthWarn,
			Count: len(seqIssues),
			Detail: fmt.Sprintf("%s %s：%s", seqIssues[0].Key, seqIssues[0].Word,
				seqIssues[0].Detail)})
	}

	// 4. 资产负债表勾稽
	asOf := report.Range(k).To
	_, _, bsIssues, err := db.Reports().BuildBalanceSheet(ctx, asOf)
	if err != nil {
		return nil, err
	}
	if len(bsIssues) == 0 {
		add(HealthItem{Key: "balance_sheet", Title: "资产负债表勾稽", Level: HealthOK,
			Detail: "资产总计 = 负债和所有者权益总计"})
	} else {
		var detail string
		for _, is := range bsIssues {
			if is.Fatal {
				detail = is.String()
				break
			}
		}
		if detail == "" {
			detail = bsIssues[0].String()
		}
		add(HealthItem{Key: "balance_sheet", Title: "资产负债表勾稽", Level: HealthError,
			Count: len(bsIssues), Detail: detail})
	}

	// 5. 现金/银行负余额（账面透支）
	neg, err := db.negativeCashAccounts(ctx, k)
	if err != nil {
		return nil, err
	}
	if len(neg) == 0 {
		add(HealthItem{Key: "negative_cash", Title: "现金及银行存款无贷方余额", Level: HealthOK})
	} else {
		add(HealthItem{Key: "negative_cash", Title: "现金或银行存款出现贷方余额",
			Level: HealthWarn, Count: len(neg), Detail: neg[0]})
	}

	// 6. 往来余额方向异常
	abn, err := db.abnormalContactBalances(ctx, k)
	if err != nil {
		return nil, err
	}
	if len(abn) == 0 {
		add(HealthItem{Key: "contact_direction", Title: "往来余额方向正常", Level: HealthOK})
	} else {
		add(HealthItem{Key: "contact_direction", Title: "往来余额方向异常", Level: HealthWarn,
			Count: len(abn), Detail: abn[0]})
	}

	return h, nil
}

// negativeCashAccounts 找出期末出现贷方余额的现金/银行科目。
//
// 现金与银行存款在现实中不可能为负（银行透支另设短期借款科目），
// 出现贷方余额几乎总是漏记收入或错记支出。
func (db *DB) negativeCashAccounts(ctx context.Context, k period.Key) ([]string, error) {
	asOf := report.Range(k).To
	rows, err := db.sql.QueryContext(ctx, `
		SELECT a.code, a.name, SUM(le.debit) - SUM(le.credit) AS net
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		 WHERE le.biz_date <= ?
		   AND a.category = '资产类'
		   AND (a.name LIKE '%现金%' OR a.name LIKE '%银行存款%')
		 GROUP BY a.id
		HAVING net < 0`, asOf.String())
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var code, name string
		var net int64
		if err := rows.Scan(&code, &name, &net); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s %s 期末贷方余额 %s", code, name, moneyOf(net)))
	}
	return out, rows.Err()
}

// abnormalContactBalances 找出往来余额方向反常的组合。
//
// 应收类科目出现贷方余额、应付类出现借方余额并不一定错
// （可能是预收/预付），但金额较大时值得核对。
func (db *DB) abnormalContactBalances(ctx context.Context, k period.Key) ([]string, error) {
	asOf := report.Range(k).To
	rows, err := db.sql.QueryContext(ctx, `
		SELECT a.code, a.name, c.name, SUM(le.debit) - SUM(le.credit) AS net
		  FROM ledger_entry le
		  JOIN account a ON a.id = le.account_id
		  JOIN contact c ON c.id = le.contact_id
		 WHERE le.biz_date <= ? AND le.contact_id IS NOT NULL
		 GROUP BY a.id, c.id
		HAVING (a.code LIKE '1122%' AND net < 0)
		    OR (a.code LIKE '2202%' AND net > 0)`, asOf.String())
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var acode, aname, cname string
		var net int64
		if err := rows.Scan(&acode, &aname, &cname, &net); err != nil {
			return nil, err
		}
		direction := "贷方"
		if net > 0 {
			direction = "借方"
		}
		out = append(out, fmt.Sprintf("%s(%s) 的往来「%s」为%s余额 %s",
			aname, acode, cname, direction, moneyOf(abs64(net))))
	}
	return out, rows.Err()
}

// moneyOf 把「分」格式化成可读金额。
func moneyOf(cents int64) string { return money.Money(cents).String() }

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// ---------------------------------------------------------------------------
// 记账人（账套级）
// ---------------------------------------------------------------------------

// settingBookkeeper 是记账人在 setting 表里的键。
//
// ★ 存进账套而不是本机配置：同一台电脑给两家公司做账，
// 签的名往往不是同一个人（代账会计尤其如此）。记账人是**这本账**的
// 签章人，跟着账套走才对；而且它随账套备份一起走 ——
// 换台电脑恢复备份，签章人还在。
const settingBookkeeper = "bookkeeper"

// Bookkeeper 返回本账套的记账人（没设过时为空串）。
func (r *BookRepo) Bookkeeper(ctx context.Context) (string, error) {
	var name string
	pr := &PayrollRepo{db: r.db}
	if _, err := pr.getSetting(ctx, settingBookkeeper, &name); err != nil {
		return "", err
	}
	return strings.TrimSpace(name), nil
}

// SetBookkeeper 设置本账套的记账人。
func (r *BookRepo) SetBookkeeper(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	return r.db.WithTx(ctx, func(tx *Tx) error {
		pr := &PayrollRepo{db: r.db}
		return pr.putSetting(ctx, tx, settingBookkeeper, name)
	})
}
