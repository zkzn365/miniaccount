package main

import (
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 税务计算表
// ---------------------------------------------------------------------------
//
// 只读：这三张表不改账，也不替代申报表。金额一律走
// **字符串元 → 分**的转换（ParseYuan），与凭证录入同一条路。

// TaxReturnRequest 是算一张税务计算表的入参。
type TaxReturnRequest struct {
	// Kind：vat 增值税及附加 | cit 企业所得税 | iit 个人所得税
	Kind  string `json:"kind"`
	Year  int    `json:"year"`
	Month int    `json:"month"`
	// ---- 企业所得税：这几项必须人工判断，程序不猜 ----
	TaxAdjustIncreaseYuan string `json:"taxAdjustIncreaseYuan"`
	TaxAdjustDecreaseYuan string `json:"taxAdjustDecreaseYuan"`
	LossOffsetYuan        string `json:"lossOffsetYuan"`
	SmallLowProfit        *bool  `json:"smallLowProfit"`
	// UrbanConstructionPpm 是城建税税率（百万分比，70000 = 7%）；0 表示用默认。
	UrbanConstructionPPM int64 `json:"urbanConstructionPpm"`
}

// TaxReturn 生成一张税务计算表草稿。
func (a *App) TaxReturn(req TaxReturnRequest) (out *service.TaxReturnView, err error) {
	defer recoverTo(&err, "TaxReturn")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	inc, cerr := ParseYuan(req.TaxAdjustIncreaseYuan)
	if cerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: "纳税调整增加额：" + cerr.Error()}
	}
	dec, cerr := ParseYuan(req.TaxAdjustDecreaseYuan)
	if cerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: "纳税调整减少额：" + cerr.Error()}
	}
	loss, cerr := ParseYuan(req.LossOffsetYuan)
	if cerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: "弥补以前年度亏损：" + cerr.Error()}
	}
	return wrap(svc.TaxReturn(a.context(), service.TaxReturnInput{
		Kind: req.Kind, Year: req.Year, Month: req.Month,
		TaxAdjustIncrease: inc, TaxAdjustDecrease: dec, LossOffset: loss,
		SmallLowProfit:       req.SmallLowProfit,
		UrbanConstructionPPM: req.UrbanConstructionPPM,
	}))
}

// TaxReturnKinds 返回三种税的选项（界面标签页用）。
func (a *App) TaxReturnKinds() (out []service.TaxReturnKindOption, err error) {
	defer recoverTo(&err, "TaxReturnKinds")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return svc.TaxReturnKindOptions(), nil
}
