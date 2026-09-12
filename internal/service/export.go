package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"miniaccount/internal/domain/cashflow"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/report"
	"miniaccount/internal/export"
)

// ExportKind 是导出的报表类型。
type ExportKind string

// 可导出的报表。
const (
	ExportBalanceSheet    ExportKind = "bs"
	ExportIncomeStatement ExportKind = "pl"
	ExportTrialBalance    ExportKind = "trial"
	ExportCashFlow        ExportKind = "cashflow"
	ExportContactBalances ExportKind = "contact"
)

// ExportOptions 是一次导出的参数。
type ExportOptions struct {
	Kind ExportKind
	// Dest 是输出文件路径（.xlsx）。
	Dest string
	// Year / Month 指定期间；Month 为 0 时按年取值。
	Year, Month int
	// AccountPrefix 供往来余额表限定科目范围。
	AccountPrefix string
}

// ExportResult 是导出结果，供界面显示「存到哪了、有多少行」。
type ExportResult struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Rows  int    `json:"rows"`
	// SheetName 是工作表名，便于用户打开后直接定位。
	SheetName string `json:"sheetName"`
	// Issues 是导出时一并写入文件的勾稽问题数。
	//
	// 界面据此提示用户「这份文件里带了 N 条校验未通过」——
	// 不能让他以为导出的是一份「干净」的报表。
	Issues int `json:"issues"`
}

// ExportExcel 把一张报表导出为 .xlsx。
//
// # 为什么导出放在 Go 侧而不是前端
//
// 前端的表格是**渲染结果**，而报表是**数据**。从前端导 Excel 只能
// 把屏幕上看到的东西抄一遍，于是：
//
//   - 被折叠/滚动截断的行会丢
//   - 金额变成字符串，Excel 里没法求和
//   - 「其中」附列项的层级、勾稽问题都表达不出来
//
// 因此导出走服务端：直接取数 → 写 xlsx（金额写成**数值**）→ 落到磁盘。
// 前端只负责选路径。
func (s *Service) ExportExcel(ctx context.Context, opts ExportOptions) (*ExportResult, error) {
	dest := strings.TrimSpace(opts.Dest)
	if dest == "" {
		return nil, fmt.Errorf("请选择导出位置")
	}
	if !strings.HasSuffix(strings.ToLower(dest), ".xlsx") {
		dest += ".xlsx"
	}
	book, err := s.Book(ctx)
	if err != nil {
		return nil, err
	}
	company := book.CompanyName
	res := &ExportResult{Path: dest}

	switch opts.Kind {
	case ExportBalanceSheet:
		k := period.NewKey(opts.Year, opts.Month)
		if !k.Valid() {
			return nil, fmt.Errorf("会计期间 %04d-%02d 非法", opts.Year, opts.Month)
		}
		asOf := periodEnd(k)
		closing, opening, issues, err := s.db.Reports().BuildBalanceSheet(ctx, asOf)
		if err != nil {
			return nil, err
		}
		at := time.Date(asOf.Year, time.Month(asOf.Month), asOf.Day, 0, 0, 0, 0, time.UTC)
		// ★ 勾稽问题要一路传到导出文件里。
		//
		// 原来这里是 `_ = issues`：屏幕上会显示「资产总计 ≠ 负债和
		// 所有者权益总计」的警告，而导出的 .xlsx 却是干净的 ——
		// 而这份文件正是发给银行、税务局、代账会计的那一份。
		if err := export.BalanceSheet(dest, company, at, closing, opening, issues); err != nil {
			return nil, err
		}
		res.Title, res.SheetName = "资产负债表", "资产负债表"
		res.Rows = len(closing.Lines)
		res.Issues = len(issues)

	case ExportIncomeStatement:
		k := period.NewKey(opts.Year, opts.Month)
		if !k.Valid() {
			return nil, fmt.Errorf("会计期间 %04d-%02d 非法", opts.Year, opts.Month)
		}
		cur, ytd, issues, err := s.db.Reports().BuildIncomeStatement(ctx, k)
		if err != nil {
			return nil, err
		}
		if err := export.IncomeStatement(dest, company, k, cur, ytd, issues); err != nil {
			return nil, err
		}
		res.Title, res.SheetName = "利润表", "利润表"
		res.Rows = len(cur.Lines)
		res.Issues = len(issues)

	case ExportTrialBalance:
		k := period.NewKey(opts.Year, opts.Month)
		if !k.Valid() {
			return nil, fmt.Errorf("会计期间 %04d-%02d 非法", opts.Year, opts.Month)
		}
		rep, err := s.db.Reports().TrialBalanceReport(ctx, k)
		if err != nil {
			return nil, err
		}
		if err := export.TrialBalance(dest, company, k.Year, k.Month, rep); err != nil {
			return nil, err
		}
		res.Title, res.SheetName = "科目余额表", "科目余额表"
		res.Rows = len(rep.Rows)

	case ExportCashFlow:
		k := period.NewKey(opts.Year, opts.Month)
		if !k.Valid() {
			return nil, fmt.Errorf("会计期间 %04d-%02d 非法", opts.Year, opts.Month)
		}
		st, err := s.db.CashFlow().StatementForPeriod(ctx, k, nil)
		if err != nil {
			return nil, err
		}
		if err := exportCashFlow(dest, company, k, st); err != nil {
			return nil, err
		}
		res.Title, res.SheetName = "现金流量表", "现金流量表"
		res.Rows = len(st.Lines)

	case ExportContactBalances:
		k := period.NewKey(opts.Year, opts.Month)
		if !k.Valid() {
			return nil, fmt.Errorf("会计期间 %04d-%02d 非法", opts.Year, opts.Month)
		}
		rows, err := s.db.Reports().ContactBalances(ctx, k, opts.AccountPrefix)
		if err != nil {
			return nil, err
		}
		if err := export.ContactBalances(dest, company, k.Year, k.Month, rows); err != nil {
			return nil, err
		}
		res.Title, res.SheetName = "往来单位余额表", "往来余额表"
		res.Rows = len(rows)

	default:
		return nil, fmt.Errorf("不支持的导出类型 %q", opts.Kind)
	}
	return res, nil
}

// SuggestedExportName 返回一个默认文件名。
//
// 形如「杭州云帆软件有限公司-资产负债表-2025-03.xlsx」。
// 带上单位名与期间是为了让用户**攒了一堆导出文件之后还分得清**——
// 小微企业的老板往往把报表直接发给会计或银行，
// 文件名里没有这些信息，对方只能一个个打开看。
func (s *Service) SuggestedExportName(ctx context.Context, kind ExportKind, year, month int) (string, error) {
	book, err := s.Book(ctx)
	if err != nil {
		return "", err
	}
	name := map[ExportKind]string{
		ExportBalanceSheet:    "资产负债表",
		ExportIncomeStatement: "利润表",
		ExportTrialBalance:    "科目余额表",
		ExportCashFlow:        "现金流量表",
		ExportContactBalances: "往来单位余额表",
	}[kind]
	if name == "" {
		name = "报表"
	}
	stamp := fmt.Sprintf("%d-%02d", year, month)
	if month == 0 {
		stamp = fmt.Sprintf("%d", year)
	}
	return filepath.Join(".", fmt.Sprintf("%s-%s-%s.xlsx",
		safeFileNamePart(book.CompanyName), name, stamp)), nil
}

// safeFileNamePart 清洗将要拼进文件名的用户输入。
//
// ★ 三处默认文件名曾经各写了一遍同样的替换 —— 而其中两处
// **只清洗了单位名、把用户传进来的科目编码原样拼进去**：
//
//	SuggestedReconciliationName("../../../../tmp/pwned")
//	  → "../../../../tmp/pwned.xlsx"
//
// 前端「取默认名 → 直接导出」这条最自然的实现就会把文件写到账套目录之外。
// 抽成一个函数，让「忘了清洗」变成写不出来，而不是靠记得。
func safeFileNamePart(s string) string {
	return strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_",
	).Replace(s)
}

// exportCashFlow 把现金流量表写成 Excel。
//
// 复用 export.WriteStatement：现金流量表与其它报表一样是
// 「项目 + 金额」的行列表，没必要为它单写一套导出代码。
func exportCashFlow(path, companyName string, k period.Key,
	st *cashflow.Statement) error {

	rows := make([]export.StatementRow, 0, len(st.Lines)+3)
	lastActivity := ""
	for _, l := range st.Lines {
		if l.Activity != "" && string(l.Activity) != lastActivity {
			rows = append(rows, export.StatementRow{
				Label: "【" + l.Activity.Label() + "】", Bold: true,
			})
			lastActivity = string(l.Activity)
		}
		rows = append(rows, export.StatementRow{
			Label:  fmt.Sprintf("%d %s", l.No, l.Name),
			Values: []money.Money{l.Display()},
			Bold:   l.IsSubtotal,
		})
	}
	rows = append(rows,
		export.StatementRow{Label: "", Values: []money.Money{}},
		export.StatementRow{Label: "期初现金余额", Values: []money.Money{st.OpeningCash}},
		export.StatementRow{Label: "期末现金余额", Values: []money.Money{st.ClosingCash}, Bold: true},
	)

	var issues []report.CheckIssue
	if d := st.CashMismatch(); d != 0 {
		issues = append(issues, report.CheckIssue{
			Left: "推算期末现金", LeftVal: st.ClosingCash,
			Right: "账面期末现金", RightVal: *st.ActualClosingCash,
			Diff: d, Fatal: true,
		})
	}

	return export.WriteStatement(path, export.StatementOptions{
		SheetName:   "现金流量表",
		Title:       "现金流量表",
		CompanyName: companyName,
		DateLabel:   fmt.Sprintf("%d 年 %d 月（直接法）", k.Year, k.Month),
		Columns:     []string{"金额"},
		Rows:        rows,
		Issues:      issues,
	})
}

// ExportPayrollExcel 把工资单导出为 .xlsx。
//
// 工资表是小微企业除报表外最常被要求导出的东西 ——
// 银行代发、员工核对、社保申报都要用。因此单独一个入口。
func (s *Service) ExportPayrollExcel(ctx context.Context, runID int64, dest string) (*ExportResult, error) {
	if strings.TrimSpace(dest) == "" {
		return nil, fmt.Errorf("请选择导出位置")
	}
	if !strings.HasSuffix(strings.ToLower(dest), ".xlsx") {
		dest += ".xlsx"
	}
	run, err := s.PayrollRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	book, err := s.Book(ctx)
	if err != nil {
		return nil, err
	}

	rows := make([]export.StatementRow, 0, len(run.Items)+2)
	for _, it := range run.Items {
		rows = append(rows, export.StatementRow{
			Label: it.EmployeeName,
			Values: []money.Money{
				it.Gross, it.AttendanceDeduct, it.OtherDeduct,
				it.InsuranceSelf, it.InsuranceCompany,
				it.SpecialAdditional, it.IIT, it.Net,
			},
		})
	}
	rows = append(rows, export.StatementRow{
		Label: "合计", Bold: true,
		Values: []money.Money{
			run.TotalGross, 0, 0, run.TotalSISelf, run.TotalSICompany,
			0, run.TotalIIT, run.TotalNet,
		},
	})

	if err := export.WriteStatement(dest, export.StatementOptions{
		SheetName:   "工资表",
		Title:       "工资表",
		CompanyName: book.CompanyName,
		DateLabel:   run.Period + "　" + run.TaxNote,
		Columns: []string{
			"应发工资", "考勤扣款", "其他扣款", "个人社保",
			"单位社保", "专项附加扣除", "代扣个税", "实发工资",
		},
		Rows: rows,
	}); err != nil {
		return nil, err
	}
	return &ExportResult{
		Path: dest, Kind: "payroll", Title: "工资表",
		Rows: len(run.Items), SheetName: "工资表",
	}, nil
}
