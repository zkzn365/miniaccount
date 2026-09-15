package main

import (
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 税务申报台账
// ---------------------------------------------------------------------------
//
// 记下「这一期这个税种报没报、什么时候报的、报了多少、谁办的」。
//
// ★ 它是**发生过的事实**，所以要落库（0014）；而计算表是派生结果，不落。
// 金额走**字符串元 → 分**（ParseYuan），与全程序同一条路。

// TaxFilingRequest 是登记一条申报记录的入参。
type TaxFilingRequest struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	Year  int    `json:"year"`
	Month int    `json:"month"`
	// Status：filed 已申报 | paid 已申报并缴纳。
	Status    string `json:"status"`
	FiledDate string `json:"filedDate"`
	PaidDate  string `json:"paidDate"`
	// ---- 金额：FromCurrentReturn 为真时由程序按当前计算表填 ----
	PayableYuan   string `json:"payableYuan"`
	TaxAmountYuan string `json:"taxAmountYuan"`
	SurchargeYuan string `json:"surchargeYuan"`
	// PaidYuan 是本期已缴（增值税的已交税金、所得税的已预缴）。
	PaidYuan    string `json:"paidYuan"`
	FromCurrent bool   `json:"fromCurrentReturn"`
	// ManualAmounts 为真表示界面**手工填了**金额（含显式填 0）。
	//
	// ★ 必须与「没填」区分开：原来服务层按「四个金额都是 0」推断
	// 「没填」，于是用户手工清成 0（零申报）会被当前计算表覆盖 ——
	// 台账记的是「发生过的事实」，不能被派生数字悄悄改写。
	ManualAmounts bool   `json:"manualAmounts"`
	Channel       string `json:"channel"`
	ReceiptNo     string `json:"receiptNo"`
	Note          string `json:"note"`
	// Operator 是经办人（台账要记清是谁办的）。
	Operator string `json:"operator"`
}

// SaveTaxFiling 登记（或修改）一条申报记录。
func (a *App) SaveTaxFiling(req TaxFilingRequest) (out *service.TaxFilingListView, err error) {
	defer recoverTo(&err, "SaveTaxFiling")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	payable, perr := ParseYuan(req.PayableYuan)
	if perr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: "应补(退)税额：" + perr.Error()}
	}
	tax, terr := ParseYuan(req.TaxAmountYuan)
	if terr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: "其中税额：" + terr.Error()}
	}
	sur, serr := ParseYuan(req.SurchargeYuan)
	if serr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: "附加税费：" + serr.Error()}
	}
	paid, aerr := ParseYuan(req.PaidYuan)
	if aerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: "本期已缴：" + aerr.Error()}
	}
	return wrap(svc.SaveTaxFiling(a.context(), service.TaxFilingInput{
		ID: req.ID, Kind: req.Kind, Year: req.Year, Month: req.Month,
		Status: req.Status, FiledDate: req.FiledDate, PaidDate: req.PaidDate,
		Payable: payable, TaxAmount: tax, Surcharge: sur, Paid: paid,
		FromCurrent: req.FromCurrent, ManualAmounts: req.ManualAmounts,
		Channel: req.Channel, ReceiptNo: req.ReceiptNo, Note: req.Note,
		Operator: req.Operator,
	}))
}

// VoidTaxFilingRequest 是作废一条申报记录（更正申报的第一步）的入参。
type VoidTaxFilingRequest struct {
	ID int64 `json:"id"`
	// By 是经办人、Reason 是作废原因 —— 两者都要留痕。
	By     string `json:"by"`
	Reason string `json:"reason"`
}

// VoidTaxFiling 作废一条申报记录。
func (a *App) VoidTaxFiling(req VoidTaxFilingRequest) (out *service.TaxFilingListView, err error) {
	defer recoverTo(&err, "VoidTaxFiling")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.VoidTaxFiling(a.context(), req.ID, req.By, req.Reason))
}

// TaxFilings 返回某年的申报台账。
func (a *App) TaxFilings(year int) (out *service.TaxFilingListView, err error) {
	defer recoverTo(&err, "TaxFilings")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.TaxFilings(a.context(), year))
}

// TaxFilingKinds 返回三种税的选项。
func (a *App) TaxFilingKinds() (out []service.TaxFilingKindOption, err error) {
	defer recoverTo(&err, "TaxFilingKinds")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return svc.TaxFilingKindOptions(), nil
}
