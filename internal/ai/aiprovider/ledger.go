package aiprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"miniaccount/internal/domain/money"
)

// ---------------------------------------------------------------------------
// 账务查询工具：报表 / 期间检查 / 工资五险一金 / 明细账
// ---------------------------------------------------------------------------
//
// # 为什么这些都是**只读**的
//
// 会计 agent 要做的事里，绝大多数是「看一眼再决定」：
// 这个月平不平、应收挂了多少、这个人的五险一金扣多少。
// 这些都只是读，读错了最多是它说错一句话，用户看得见 ——
// 而且下一句就能纠正。真正要写账套的动作（记凭证、建档案、办离职）
// 一律走「提议 + 用户确认」，模型自己没有写库能力。
//
// # 输出一律是**给人看的中文文本**
//
// 不是 JSON。工具结果是回灌给模型看的，而模型接下来要用它组织中文回答；
// 给它一段带字段名的 JSON，它得先翻译一遍，而翻译正是最容易出错的一步
// （把「贷方余额」翻成「借方」这类）。直接给中文，它照抄就行。

// ---------------------------------------------------------------------------
// 报表
// ---------------------------------------------------------------------------

// ReportLine 是报表里的一行。
//
// 同一行既可能表示「发生额」（余额表、明细账），
// 也可能表示「余额」（资产负债表、现金流量表）—— 看表。
// 所以两个都给，由渲染函数决定怎么显示。
type ReportLine struct {
	Code string
	Name string
	// Debit / Credit 是本期发生额（余额表、明细账用）。
	Debit  money.Money
	Credit money.Money
	// Balance 是余额（资产负债表等用），Dir 是它的方向/侧别。
	Balance money.Money
	Dir     string
}

// ReportData 是一张报表的紧凑形态。
type ReportData struct {
	// Title 是表名，如「科目余额表」。
	Title string
	// Period 是期间描述，如「2026-09」或「截至 2026-09-30」。
	Period string
	// Lines 是明细行（已经过滤掉空行）。
	Lines []ReportLine
	// TotalDebit / TotalCredit 是合计。
	TotalDebit  money.Money
	TotalCredit money.Money
	// Issues 是勾稽检查发现的问题（资产负债表、利润表有）。
	Issues []string
}

// ReportProvider 提供报表数据。
type ReportProvider interface {
	// Report 返回 kind 对应的报表。
	//
	// kind: trial（科目余额表）| bs（资产负债表）| pl（利润表）| cashflow（现金流量表）
	Report(ctx context.Context, kind string, k PeriodKey) (*ReportData, error)
}

// PeriodCheckProvider 提供「本月检查核算」的结果。
type PeriodCheckProvider interface {
	CheckPeriod(ctx context.Context, k PeriodKey) (*PeriodCheck, error)
}

// PayrollPreviewProvider 提供工资与五险一金的试算。
type PayrollPreviewProvider interface {
	PreviewPayroll(ctx context.Context, k PeriodKey) (*PayrollPreview, error)
}

// LedgerProvider 提供明细账。
type LedgerProvider interface {
	Ledger(ctx context.Context, accountCode string, year, month int) (*ReportData, error)
}

// PeriodKey 是期间（与 domain/period.Key 同形，避免 aiprovider 依赖 period 包）。
type PeriodKey struct {
	Year  int
	Month int
}

// String 返回 2026-09。
func (k PeriodKey) String() string { return fmt.Sprintf("%04d-%02d", k.Year, k.Month) }

// Valid 报告期间是否合法。
func (k PeriodKey) Valid() bool { return k.Year > 0 && k.Month >= 1 && k.Month <= 12 }

// parsePeriod 解析 "2026-09" 或 "2026-9"。
func parsePeriod(s string) (PeriodKey, error) {
	var k PeriodKey
	s = strings.TrimSpace(s)
	if s == "" {
		return k, fmt.Errorf("请给出会计期间，格式 2026-09")
	}
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return k, fmt.Errorf("会计期间 %q 格式不对，应为 2026-09", s)
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &k.Year); err != nil {
		return k, fmt.Errorf("会计期间 %q 的年份读不出来", s)
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &k.Month); err != nil {
		return k, fmt.Errorf("会计期间 %q 的月份读不出来", s)
	}
	if !k.Valid() {
		return k, fmt.Errorf("会计期间 %q 非法", s)
	}
	return k, nil
}

// 报表种类。
var reportKinds = map[string]string{
	"trial":    "科目余额表",
	"bs":       "资产负债表",
	"pl":       "利润表",
	"cashflow": "现金流量表",
}

// GetReportTool 构造「看报表」工具。
func GetReportTool(p ReportProvider) Tool {
	return Tool{
		Name: "get_report",
		Description: "生成一张财务报表。用户问「这个月怎么样」「资产负债多少」" +
			"「这个月赚了没有」「现金流入流出」时用它 —— 不要凭记忆或推算回答，" +
			"报表里的数字必须来自这里。kind 取值：" +
			"trial 科目余额表（要逐科目余额时用）、bs 资产负债表、" +
			"pl 利润表、cashflow 现金流量表。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"kind": {"type": "string", "enum": ["trial", "bs", "pl", "cashflow"],
				         "description": "要哪张表"},
				"period": {"type": "string",
				           "description": "会计期间，格式 2026-09。用户没指定时用当前可记账期间"}
			},
			"required": ["kind", "period"]
		}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Kind   string `json:"kind"`
				Period string `json:"period"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("参数格式错误: %w", err)
			}
			title, ok := reportKinds[args.Kind]
			if !ok {
				return "", fmt.Errorf("不认识的报表种类 %q", args.Kind)
			}
			k, err := parsePeriod(args.Period)
			if err != nil {
				return "", err
			}
			d, err := p.Report(ctx, args.Kind, k)
			if err != nil {
				return "", err
			}
			_ = title // d.Title 更具体（数据源自己带的表名）
			return renderReport(d), nil
		},
	}
}

// renderReport 把报表渲染成中文文本。
//
// 金额用「1,234.56」而不是「123456 分」：模型要把它写进回答里给用户看，
// 给它分，它就得自己除以 100 —— 而它偶尔会漏掉那个小数点。
func renderReport(d *ReportData) string {
	if d == nil {
		return "没有取到报表数据。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s（%s）\n\n", d.Title, d.Period)
	if len(d.Lines) == 0 {
		b.WriteString("这张表本期没有任何数据。\n")
	}
	for _, l := range d.Lines {
		fmt.Fprintf(&b, "%s %s", l.Code, l.Name)
		if !l.Debit.IsZero() {
			fmt.Fprintf(&b, "  借方 %s", l.Debit)
		}
		if !l.Credit.IsZero() {
			fmt.Fprintf(&b, "  贷方 %s", l.Credit)
		}
		b.WriteByte('\n')
	}
	if !d.TotalDebit.IsZero() || !d.TotalCredit.IsZero() {
		fmt.Fprintf(&b, "\n合计：借方 %s，贷方 %s", d.TotalDebit, d.TotalCredit)
		if d.TotalDebit == d.TotalCredit {
			b.WriteString("（试算平衡）")
		} else {
			fmt.Fprintf(&b, "（★ 不平衡，差 %s）", d.TotalDebit.Sub(d.TotalCredit))
		}
		b.WriteByte('\n')
	}
	for _, is := range d.Issues {
		fmt.Fprintf(&b, "\n★ 勾稽问题：%s\n", is)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 期间检查（本月检查核算）
// ---------------------------------------------------------------------------

// CheckItem 是一项检查结果。
type CheckItem struct {
	Title  string
	Level  string // ok | warn | error
	Detail string
	Count  int
}

// PeriodCheck 是一个期间的体检结果。
type PeriodCheck struct {
	Period string
	Items  []CheckItem
	// CanClose 为真表示体检通过、可以结账。
	CanClose bool
	// Drafts 是本期待过账的草稿张数（结账时会自动过账）。
	Drafts int
	// Income / Expense / Profit 是本期的损益（结转预览）。
	Income  money.Money
	Expense money.Money
	Profit  money.Money
	// ClosingEntries 是结账会写入的结转分录（文字描述）。
	ClosingEntries []string
}

// CheckPeriodTool 构造「检查这个月」工具。
func CheckPeriodTool(p PeriodCheckProvider) Tool {
	return Tool{
		Name: "check_period",
		Description: "对某个会计期间做结账前体检：试算是否平衡、有没有草稿未过账、" +
			"凭证字号是否连续、资产负债表勾稽是否成立、现金是否负数、" +
			"往来方向是否异常，并给出本期损益与结转预览。" +
			"用户问「这个月能不能结账」「帮我检查一下这个月」「哪里不对」时用它。" +
			"**结论必须来自这里**，不要凭印象说「看起来没问题」。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"period": {"type": "string", "description": "会计期间，格式 2026-09"}
			},
			"required": ["period"]
		}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct{ Period string }
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("参数格式错误: %w", err)
			}
			k, err := parsePeriod(args.Period)
			if err != nil {
				return "", err
			}
			c, err := p.CheckPeriod(ctx, k)
			if err != nil {
				return "", err
			}
			return renderCheck(c), nil
		},
	}
}

func renderCheck(c *PeriodCheck) string {
	if c == nil {
		return "没有取到体检结果。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s 结账前体检\n\n", c.Period)
	for _, it := range c.Items {
		mark := "✓"
		switch it.Level {
		case "warn":
			mark = "!"
		case "error":
			mark = "✗"
		}
		fmt.Fprintf(&b, "%s %s", mark, it.Title)
		if it.Count > 0 {
			fmt.Fprintf(&b, "（%d）", it.Count)
		}
		if it.Detail != "" {
			fmt.Fprintf(&b, "：%s", it.Detail)
		}
		b.WriteByte('\n')
	}
	if c.Drafts > 0 {
		fmt.Fprintf(&b, "\n本期有 %d 张草稿凭证，结账时会先自动过账。\n", c.Drafts)
	}
	if !c.Income.IsZero() || !c.Expense.IsZero() {
		fmt.Fprintf(&b, "\n本期损益：收入 %s，费用 %s，利润 %s\n",
			c.Income, c.Expense, c.Profit)
	}
	for _, e := range c.ClosingEntries {
		fmt.Fprintf(&b, "  结转：%s\n", e)
	}
	if c.CanClose {
		b.WriteString("\n结论：体检通过，可以结账。\n")
	} else {
		b.WriteString("\n结论：★ 有阻断项，现在不能结账，上面标 ✗ 的要先处理。\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 工资与五险一金
// ---------------------------------------------------------------------------

// PayrollPerson 是工资试算里的一名员工。
type PayrollPerson struct {
	Name string
	// Gross 是应发合计。
	Gross money.Money
	// InsuranceBase 是社保缴费基数（已按上下限夹取）。
	InsuranceBase money.Money
	// 个人承担
	PensionSelf, MedicalSelf, UnemploymentSelf, HousingFundSelf money.Money
	// 单位承担
	PensionCo, MedicalCo, UnemploymentCo, InjuryCo, MaternityCo, HousingFundCo money.Money
	// IIT 是个人所得税。
	IIT money.Money
	// Net 是实发合计。
	Net money.Money
	// SchemeName 是使用的社保方案名；为空表示档案里没填（即不缴）。
	SchemeName string
	// Warning 是需要注意的问题。
	Warning string
}

// PayrollPreview 是一次工资试算。
type PayrollPreview struct {
	Period string
	People []PayrollPerson
	// Totals
	TotalGross         money.Money
	TotalInsuranceCo   money.Money
	TotalInsuranceSelf money.Money
	TotalIIT           money.Money
	TotalNet           money.Money
	// SchemeCount 是账套里配了几个社保方案。
	SchemeCount int
	// Schemes 是方案名列表。
	Schemes []string
	// Note 是整体提示（如「还没有配社保方案」）。
	Note string
}

// PreviewPayrollTool 构造「算五险一金 / 试算工资」工具。
func PreviewPayrollTool(p PayrollPreviewProvider) Tool {
	return Tool{
		Name: "preview_payroll",
		Description: "按员工的工资档案试算某个期间的工资：应发、五险一金（个人与单位分别列出）、" +
			"个人所得税、实发。**不写库**，用户确认后才需要生成工资单。" +
			"用户问「这个月社保扣多少」「五险一金一共多少」「个税多少」时用它。" +
			"★ 费率来自账套里已配的社保方案，**不要自己编费率** —— " +
			"各地各年不同，编出来的数字看着很合理，但会算错一整年。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"period": {"type": "string", "description": "会计期间，格式 2026-09"}
			},
			"required": ["period"]
		}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct{ Period string }
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("参数格式错误: %w", err)
			}
			k, err := parsePeriod(args.Period)
			if err != nil {
				return "", err
			}
			pv, err := p.PreviewPayroll(ctx, k)
			if err != nil {
				return "", err
			}
			return renderPayroll(pv), nil
		},
	}
}

func renderPayroll(pv *PayrollPreview) string {
	if pv == nil {
		return "没有取到工资试算结果。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s 工资与五险一金试算（未写库）\n\n", pv.Period)
	if pv.Note != "" {
		fmt.Fprintf(&b, "★ %s\n\n", pv.Note)
	}
	if len(pv.Schemes) > 0 {
		fmt.Fprintf(&b, "已配社保方案：%s\n\n", strings.Join(pv.Schemes, "、"))
	}
	if len(pv.People) == 0 {
		b.WriteString("这个期间没有可算工资的在职员工。\n")
		return b.String()
	}
	for _, p := range pv.People {
		fmt.Fprintf(&b, "## %s\n", p.Name)
		fmt.Fprintf(&b, "应发 %s", p.Gross)
		if p.InsuranceBase.IsPositive() {
			fmt.Fprintf(&b, "  社保基数 %s", p.InsuranceBase)
		}
		if p.SchemeName != "" {
			fmt.Fprintf(&b, "  方案 %s", p.SchemeName)
		}
		b.WriteByte('\n')
		self := money.Sum(p.PensionSelf, p.MedicalSelf, p.UnemploymentSelf, p.HousingFundSelf)
		co := money.Sum(p.PensionCo, p.MedicalCo, p.UnemploymentCo, p.InjuryCo,
			p.MaternityCo, p.HousingFundCo)
		if self.IsPositive() {
			fmt.Fprintf(&b, "个人承担合计 %s（养老 %s、医疗 %s、失业 %s、公积金 %s）\n",
				self, p.PensionSelf, p.MedicalSelf, p.UnemploymentSelf, p.HousingFundSelf)
		}
		if co.IsPositive() {
			fmt.Fprintf(&b, "单位承担合计 %s（养老 %s、医疗 %s、失业 %s、工伤 %s、生育 %s、公积金 %s）\n",
				co, p.PensionCo, p.MedicalCo, p.UnemploymentCo, p.InjuryCo,
				p.MaternityCo, p.HousingFundCo)
		}
		fmt.Fprintf(&b, "个税 %s  实发 %s\n", p.IIT, p.Net)
		if p.Warning != "" {
			fmt.Fprintf(&b, "! %s\n", p.Warning)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "合计：应发 %s，个人承担 %s，单位承担 %s，个税 %s，实发 %s\n",
		pv.TotalGross, pv.TotalInsuranceSelf, pv.TotalInsuranceCo, pv.TotalIIT, pv.TotalNet)
	return b.String()
}

// ---------------------------------------------------------------------------
// 明细账
// ---------------------------------------------------------------------------

// GetLedgerTool 构造「查明细账」工具。
func GetLedgerTool(p LedgerProvider) Tool {
	return Tool{
		Name: "get_ledger",
		Description: "查某个科目在某个月的明细账：逐笔日期、凭证号、摘要、借贷与余额。" +
			"用户问「这笔钱怎么走的」「房租记在哪几笔」「某个科目的明细」时用它。" +
			"科目编码必须是 search_accounts 返回过的。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"account_code": {"type": "string", "description": "科目编码，如 560210"},
				"period": {"type": "string", "description": "会计期间，格式 2026-09"}
			},
			"required": ["account_code", "period"]
		}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				AccountCode string `json:"account_code"`
				Period      string `json:"period"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("参数格式错误: %w", err)
			}
			code := strings.TrimSpace(args.AccountCode)
			if code == "" {
				return "", fmt.Errorf("请给出科目编码")
			}
			k, err := parsePeriod(args.Period)
			if err != nil {
				return "", err
			}
			d, err := p.Ledger(ctx, code, k.Year, k.Month)
			if err != nil {
				return "", err
			}
			if d.Title == "" {
				d.Title = "明细账 " + code
			}
			return renderReport(d), nil
		},
	}
}
