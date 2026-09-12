package main

import (
	"strings"

	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 导出
// ---------------------------------------------------------------------------

// ExportRequest 是导出报表的参数。
type ExportRequest struct {
	// Kind 是报表类型：bs | pl | trial | cashflow | contact | payroll
	Kind string `json:"kind"`
	// Dest 是输出路径。为空时由界面先调 SuggestExportPath 拿默认名。
	Dest string `json:"dest"`
	Year int    `json:"year"`
	// Month 为 0 时按年取值。
	Month int `json:"month"`
	// AccountPrefix 供往来余额表限定科目范围。
	AccountPrefix string `json:"accountPrefix"`
	// RunID 供工资表使用。
	RunID int64 `json:"runId"`
}

// ExportReport 把一张报表或工资表导出为 .xlsx。
func (a *App) ExportReport(req ExportRequest) (out *service.ExportResult, err error) {
	defer recoverTo(&err, "ExportReport")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	if strings.TrimSpace(req.Dest) == "" {
		return nil, &Fault{Kind: FaultInvalid, Message: "请选择导出位置"}
	}
	if service.ExportKind(req.Kind) == "payroll" {
		return wrap(svc.ExportPayrollExcel(a.context(), req.RunID, req.Dest))
	}
	return wrap(svc.ExportExcel(a.context(), service.ExportOptions{
		Kind: service.ExportKind(req.Kind), Dest: req.Dest,
		Year: req.Year, Month: req.Month, AccountPrefix: req.AccountPrefix,
	}))
}

// SuggestExportPath 返回一个默认的文件名。
//
// 界面拿它填进「另存为」对话框的默认名 —— 用户不用自己拼
// 「公司名-报表名-期间」这一串，而这一串恰恰是攒了一堆文件之后
// 还能分清哪个是哪个的关键。
func (a *App) SuggestExportPath(kind string, year, month int) (out string, err error) {
	defer recoverTo(&err, "SuggestExportPath")()
	svc, f := a.book()
	if f != nil {
		return "", f
	}
	return wrap(svc.SuggestedExportName(a.context(), service.ExportKind(kind), year, month))
}
