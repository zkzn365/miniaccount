package main

import (
	"fmt"
	"strings"

	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 审计与鉴证文书（草稿）
// ---------------------------------------------------------------------------
//
// 三份：审计报告、验资报告、管理建议书。
//
// ★ 这一层只做两件事：把用户填的字收上来，把账套里的事实喂给服务层。
// 意见类型、认缴出资、签字人一律由界面传 —— 程序不替人做执业判断。
//
// 金额走**字符串元 → 分**（ParseYuan），与凭证录入同一条路。

// AuditDocRequest 是生成一份文书的入参。
type AuditDocRequest struct {
	// Kind：audit 审计报告 | capital 验资报告 | management 管理建议书
	Kind  string `json:"kind"`
	Year  int    `json:"year"`
	Month int    `json:"month"`

	// ---- 审计报告 ----
	Opinion    string   `json:"opinion"`
	BasisExtra []string `json:"basisExtra"`

	// ---- 验资报告 ----
	RegisteredCapitalYuan string         `json:"registeredCapitalYuan"`
	Evidence              []string       `json:"evidence"`
	NonCash               bool           `json:"nonCash"`
	Shares                []ShareRequest `json:"shares"`

	// ---- 共同的签字信息 ----
	FirmName   string `json:"firmName"`
	CPA1       string `json:"cpa1"`
	CPA2       string `json:"cpa2"`
	ReportNo   string `json:"reportNo"`
	ReportDate string `json:"reportDate"`
}

// ShareRequest 是一位股东的出资信息（实缴由账套取，这里只填认缴与方式）。
type ShareRequest struct {
	Name           string `json:"name"`
	SubscribedYuan string `json:"subscribedYuan"`
	Method         string `json:"method"`
	PaidDate       string `json:"paidDate"`
}

// AuditDoc 生成一份文书草稿。
func (a *App) AuditDoc(req AuditDocRequest) (out *service.AuditDocView, err error) {
	defer recoverTo(&err, "AuditDoc")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	// ★ 注册资本必须填。
	//
	// ParseYuan("") 会安静地返回 0，而验资报告的核心结论就是
	// 「实缴与注册资本是否相符」—— 少了这个数，那次审验等于没有结论，
	// 而界面还会显示「待签字盖章」。发布前界面审计实测过这条路径。
	if kindIsCapital(req.Kind) && strings.TrimSpace(req.RegisteredCapitalYuan) == "" {
		return nil, &Fault{Kind: FaultInvalid,
			Message: "请填写注册资本 —— 验资报告的核心结论就是「实缴与注册资本是否相符」，不能留空"}
	}
	repCapital, cerr := ParseYuan(req.RegisteredCapitalYuan)
	if cerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: "注册资本：" + cerr.Error()}
	}
	in := service.AuditDocInput{
		Kind: req.Kind, Year: req.Year, Month: req.Month,
		Opinion: req.Opinion, BasisExtra: req.BasisExtra,
		RegisteredCapital: repCapital, Evidence: req.Evidence, NonCash: req.NonCash,
		FirmName: req.FirmName, CPA1: req.CPA1, CPA2: req.CPA2,
		ReportNo: req.ReportNo, ReportDate: req.ReportDate,
	}
	for i, sh := range req.Shares {
		sub, serr := ParseYuan(sh.SubscribedYuan)
		if serr != nil {
			return nil, &Fault{Kind: FaultInvalid,
				Message: fmt.Sprintf("第 %d 位股东的认缴出资额：%s", i+1, serr.Error())}
		}
		in.Shares = append(in.Shares, service.ShareInput{
			Name: sh.Name, Subscribed: sub, Method: sh.Method, PaidDate: sh.PaidDate,
		})
	}
	return wrap(svc.AuditDoc(a.context(), in))
}

// AuditDocKinds 返回三种文书的选项。
func (a *App) AuditDocKinds() (out []service.AuditDocKindOption, err error) {
	defer recoverTo(&err, "AuditDocKinds")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return svc.AuditDocKindOptions(), nil
}

// AuditOpinions 返回四种审计意见的选项（含什么时候用）。
func (a *App) AuditOpinions() (out []service.OpinionOption, err error) {
	defer recoverTo(&err, "AuditOpinions")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return svc.OpinionOptions(), nil
}

// ShareholderPaidRequest 是查股东实缴的参数。
type ShareholderPaidRequest struct {
	Year  int `json:"year"`
	Month int `json:"month"`
}

// ShareholderPaid 返回账上各股东的实缴出资（验资报告表单预填）。
func (a *App) ShareholderPaid(req ShareholderPaidRequest) (out []service.ShareholderPaidView, err error) {
	defer recoverTo(&err, "ShareholderPaid")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.ShareholderPaid(a.context(), req.Year, req.Month))
}

// kindIsCapital 报告是不是验资报告。
func kindIsCapital(kind string) bool { return strings.TrimSpace(kind) == "capital" }
