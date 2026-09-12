package main

import (
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 固定资产与费用摊销
// ---------------------------------------------------------------------------
//
// 金额一律走**字符串元 → 分**的转换（ParseYuan），与凭证录入同一条路：
// 界面提交的是 "12000.00"，Go 侧收到字符串再解析，
// 不让 float 在两边之间来回跑。

// AssetRequest 是界面提交的固定资产卡片。
type AssetRequest struct {
	ID           int64  `json:"id"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	DeptID       *int64 `json:"deptId"`
	OrigYuan     string `json:"origYuan"`
	SalvagePPM   int64  `json:"salvagePpm"`
	UsefulMonths int    `json:"usefulMonths"`
	StartDate    string `json:"startDate"`
	// ExpenseAccount / AccumAccount 留空用默认值。
	ExpenseAccount string `json:"expenseAccount"`
	AccumAccount   string `json:"accumAccount"`
	DisposedDate   string `json:"disposedDate"`
	Remark         string `json:"remark"`
}

func (r AssetRequest) toService() (service.AssetInput, error) {
	orig, err := ParseYuan(r.OrigYuan)
	if err != nil {
		return service.AssetInput{}, err
	}
	return service.AssetInput{
		ID: r.ID, Code: r.Code, Name: r.Name, Category: r.Category,
		DeptID: r.DeptID, OrigValue: orig, SalvagePPM: r.SalvagePPM,
		UsefulMonths: r.UsefulMonths, StartDate: r.StartDate,
		ExpenseAccount: r.ExpenseAccount, AccumAccount: r.AccumAccount,
		DisposedDate: r.DisposedDate, Remark: r.Remark,
	}, nil
}

// Assets 返回固定资产与待摊项目档案。
func (a *App) Assets() (out *service.AssetsView, err error) {
	defer recoverTo(&err, "Assets")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Assets(a.context()))
}

// SaveAsset 新增或修改一张固定资产卡片。
func (a *App) SaveAsset(req AssetRequest) (out *service.AssetView, err error) {
	defer recoverTo(&err, "SaveAsset")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	in, cerr := req.toService()
	if cerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: cerr.Error()}
	}
	return wrap(svc.SaveAsset(a.context(), in))
}

// DisposeAssetRequest 是处置固定资产的参数。
type DisposeAssetRequest struct {
	ID   int64  `json:"id"`
	Date string `json:"date"`
	// Reason 是处置原因，写进操作日志。
	Reason string `json:"reason"`
}

// DisposeAsset 处置一张固定资产卡片。
//
// 只标状态：处置当月照提折旧，次月起停。清理损益请另做凭证。
func (a *App) DisposeAsset(req DisposeAssetRequest) (out *service.AssetView, err error) {
	defer recoverTo(&err, "DisposeAsset")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.DisposeAsset(a.context(), req.ID, req.Date, req.Reason))
}

// DeleteAsset 删除一张从未计提过折旧的卡片。
func (a *App) DeleteAsset(id int64) (err error) {
	defer recoverTo(&err, "DeleteAsset")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	if derr := svc.DeleteAsset(a.context(), id); derr != nil {
		return classify(derr)
	}
	return nil
}

// AmortizationRequest 是界面提交的待摊项目。
type AmortizationRequest struct {
	ID             int64  `json:"id"`
	Code           string `json:"code"`
	Name           string `json:"name"`
	DeptID         *int64 `json:"deptId"`
	TotalYuan      string `json:"totalYuan"`
	Months         int    `json:"months"`
	StartDate      string `json:"startDate"`
	ExpenseAccount string `json:"expenseAccount"`
	AssetAccount   string `json:"assetAccount"`
	Remark         string `json:"remark"`
}

func (r AmortizationRequest) toService() (service.AmortizationInput, error) {
	total, err := ParseYuan(r.TotalYuan)
	if err != nil {
		return service.AmortizationInput{}, err
	}
	return service.AmortizationInput{
		ID: r.ID, Code: r.Code, Name: r.Name, DeptID: r.DeptID,
		Total: total, Months: r.Months, StartDate: r.StartDate,
		ExpenseAccount: r.ExpenseAccount, AssetAccount: r.AssetAccount,
		Remark: r.Remark,
	}, nil
}

// SaveAmortization 新增或修改一个待摊项目。
func (a *App) SaveAmortization(req AmortizationRequest) (out *service.AmortizationView, err error) {
	defer recoverTo(&err, "SaveAmortization")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	in, cerr := req.toService()
	if cerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: cerr.Error()}
	}
	return wrap(svc.SaveAmortization(a.context(), in))
}

// VoidAmortizationRequest 是作废 / 恢复待摊项目的参数。
type VoidAmortizationRequest struct {
	ID   int64 `json:"id"`
	Void bool  `json:"void"`
}

// VoidAmortization 作废或恢复一个待摊项目。
func (a *App) VoidAmortization(req VoidAmortizationRequest) (out *service.AmortizationView, err error) {
	defer recoverTo(&err, "VoidAmortization")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.VoidAmortization(a.context(), req.ID, req.Void))
}

// DeleteAmortization 删除一个从未摊销过的项目。
func (a *App) DeleteAmortization(id int64) (err error) {
	defer recoverTo(&err, "DeleteAmortization")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	if derr := svc.DeleteAmortization(a.context(), id); derr != nil {
		return classify(derr)
	}
	return nil
}

// PreviewAccrual 预览某期间的折旧与摊销，**不写任何数据**。
func (a *App) PreviewAccrual(req PeriodRequest) (out *service.AccrualPreview, err error) {
	defer recoverTo(&err, "PreviewAccrual")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	k, kerr := req.key()
	if kerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: kerr.Error()}
	}
	return wrap(svc.PreviewAccrual(a.context(), k))
}

// AccrueRequest 是计提折旧与摊销的参数。
type AccrueRequest struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	// By 是操作人（计提凭证的制单人）。
	By string `json:"by"`
}

// Accrue 计提本期的折旧与摊销，生成**草稿**凭证。
//
// ★ 草稿不进总账：到账期结算（结账）时与其他草稿一起过账。
func (a *App) Accrue(req AccrueRequest) (out *service.AccrualResult, err error) {
	defer recoverTo(&err, "Accrue")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	k, kerr := PeriodRequest{Year: req.Year, Month: req.Month}.key()
	if kerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: kerr.Error()}
	}
	return wrap(svc.Accrue(a.context(), k, req.By))
}
