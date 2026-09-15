package main

import (
	"fmt"

	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 审计底稿
// ---------------------------------------------------------------------------
//
// 金额一律走**字符串元 → 分**的转换（ParseYuan），与凭证录入同一条路；
// 比例走**百万分比整数**（与固定资产残值率、重要性比例的老规矩一致）——
// 不让 float 在界面与 Go 之间来回跑。
//
// ★ 这里唯一一个会「动账」的入口是 PostAdjustment，
// 而它生成的也只是一张**草稿**凭证：过账仍然只发生在账期结算。

// Workpaper 返回某期的审计底稿（重要性水平 + 审定表 + 未更正错报 + 调整清单）。
func (a *App) Workpaper(req PeriodRequest) (out *service.WorkpaperView, err error) {
	defer recoverTo(&err, "Workpaper")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	k, kerr := req.key()
	if kerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: kerr.Error()}
	}
	return wrap(svc.Workpaper(a.context(), k))
}

// MaterialityRequest 是界面提交的重要性水平。
type MaterialityRequest struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	// Benchmark：assets 资产总额 | revenue 营业收入 | profit 利润总额 | expense 费用总额
	Benchmark string `json:"benchmark"`
	// BenchmarkAmountYuan 留空表示用账套取数。
	BenchmarkAmountYuan string `json:"benchmarkAmountYuan"`
	// 三个比例都是百万分比：5000 = 0.5%，600000 = 60%。
	// 为 0 表示用该基准的常用比例。
	RatePPM        int64  `json:"ratePpm"`
	PerformancePPM int64  `json:"performancePpm"`
	TrivialPPM     int64  `json:"trivialPpm"`
	Note           string `json:"note"`
}

// SaveMateriality 确定某期的重要性水平。
func (a *App) SaveMateriality(req MaterialityRequest) (out *service.WorkpaperView, err error) {
	defer recoverTo(&err, "SaveMateriality")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	amount, cerr := ParseYuan(req.BenchmarkAmountYuan)
	if cerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: cerr.Error()}
	}
	return wrap(svc.SaveMateriality(a.context(), service.MaterialityInput{
		Year: req.Year, Month: req.Month, Benchmark: req.Benchmark,
		BenchmarkAmount: amount,
		RatePPM:         req.RatePPM, PerformancePPM: req.PerformancePPM,
		TrivialPPM: req.TrivialPPM, Note: req.Note,
	}))
}

// DeleteMateriality 清除某期的重要性水平。
func (a *App) DeleteMateriality(req PeriodRequest) (out *service.WorkpaperView, err error) {
	defer recoverTo(&err, "DeleteMateriality")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.DeleteMateriality(a.context(), req.Year, req.Month))
}

// AdjustLineRequest 是调整分录的一行。
type AdjustLineRequest struct {
	AccountCode string `json:"accountCode"`
	Summary     string `json:"summary"`
	DebitYuan   string `json:"debitYuan"`
	CreditYuan  string `json:"creditYuan"`
	ContactID   *int64 `json:"contactId"`
	EmployeeID  *int64 `json:"employeeId"`
	DeptID      *int64 `json:"deptId"`
	ProjectID   *int64 `json:"projectId"`
}

// AdjustmentRequest 是界面提交的一笔审计调整。
type AdjustmentRequest struct {
	ID       int64               `json:"id"`
	Year     int                 `json:"year"`
	Month    int                 `json:"month"`
	Code     string              `json:"code"`
	Kind     string              `json:"kind"`
	Summary  string              `json:"summary"`
	Reason   string              `json:"reason"`
	Evidence string              `json:"evidence"`
	Lines    []AdjustLineRequest `json:"lines"`
	Operator string              `json:"operator"`
}

func (r AdjustmentRequest) toService() (service.AdjustmentInput, error) {
	in := service.AdjustmentInput{
		ID: r.ID, Year: r.Year, Month: r.Month, Code: r.Code, Kind: r.Kind,
		Summary: r.Summary, Reason: r.Reason, Evidence: r.Evidence,
		Operator: r.Operator, Lines: make([]service.AdjustLineInput, 0, len(r.Lines)),
	}
	for i, l := range r.Lines {
		debit, err := ParseYuan(l.DebitYuan)
		if err != nil {
			return in, fmt.Errorf("第 %d 行的借方金额：%w", i+1, err)
		}
		credit, err := ParseYuan(l.CreditYuan)
		if err != nil {
			return in, fmt.Errorf("第 %d 行的贷方金额：%w", i+1, err)
		}
		in.Lines = append(in.Lines, service.AdjustLineInput{
			AccountCode: l.AccountCode, Summary: l.Summary,
			Debit: debit, Credit: credit,
			ContactID: l.ContactID, EmployeeID: l.EmployeeID,
			DeptID: l.DeptID, ProjectID: l.ProjectID,
		})
	}
	return in, nil
}

// SaveAdjustment 新增或修改一笔审计调整。
func (a *App) SaveAdjustment(req AdjustmentRequest) (out *service.WorkpaperView, err error) {
	defer recoverTo(&err, "SaveAdjustment")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	in, cerr := req.toService()
	if cerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: cerr.Error()}
	}
	return wrap(svc.SaveAdjustment(a.context(), in))
}

// DeleteAdjustment 删除一笔调整（已经生成凭证的删不掉）。
func (a *App) DeleteAdjustment(id int64) (out *service.WorkpaperView, err error) {
	defer recoverTo(&err, "DeleteAdjustment")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.DeleteAdjustment(a.context(), id))
}

// PostAdjustmentRequest 是把一笔调整生成凭证的参数。
type PostAdjustmentRequest struct {
	ID int64 `json:"id"`
	// By 是操作人（调整凭证的制单人）。
	By string `json:"by"`
}

// PostAdjustment 把一笔调整生成**调整凭证草稿**。
//
// ★ 生成的是草稿：不占号、不进总账，到账期结算时才过账。
// 审计调整不例外 —— 这是全账套同一条规矩。
func (a *App) PostAdjustment(req PostAdjustmentRequest) (out *service.WorkpaperView, err error) {
	defer recoverTo(&err, "PostAdjustment")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.PostAdjustment(a.context(), req.ID, req.By))
}
