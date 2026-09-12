package main

import (
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 增值税政策与纳税人身份
// ---------------------------------------------------------------------------
//
// 原先这一整块只有命令行入口（miniaccount vat），界面上看不到也改不了。
// 而「这个月该按几个点开票」「这笔进项能不能抵」恰恰是会计天天要问的问题，
// 命令行不是它们该待的地方。

// VATPolicies 返回全部增值税税率政策。
func (a *App) VATPolicies() (out []service.VATPolicyView, err error) {
	defer recoverTo(&err, "VATPolicies")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.VATPolicies(a.context()))
}

// VATRateQuery 是查适用税率的参数。
type VATRateQuery struct {
	Status   string `json:"status"`
	Subject  string `json:"subject"`
	Category string `json:"category"`
	Method   string `json:"method"`
	On       string `json:"on"`
	// IncludeConditional 对应界面上「我确认命中例外情形」那个勾。
	//
	// ★ 这个字段漏掉过一次：界面把 includeConditional 发上来了，
	// 但这一层没有它，于是被静默丢弃 —— 勾了跟没勾一样，
	// 而且界面照常显示结果。传递链上任何一层少一个字段都是这样：
	// 不报错，只是用户的输入不起作用。
	IncludeConditional bool `json:"includeConditional"`
}

// ResolveVATRate 按「身份 + 主体 + 业务 + 方法 + 日期」查出适用税率。
func (a *App) ResolveVATRate(q VATRateQuery) (out *service.VATRateResult, err error) {
	defer recoverTo(&err, "ResolveVATRate")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.ResolveVATRate(a.context(), service.VATRateQuery{
		Status: q.Status, Subject: q.Subject, Category: q.Category,
		Method: q.Method, On: q.On,
		IncludeConditional: q.IncludeConditional,
	}))
}

// DeductionRequest 是进项抵扣判定的参数。
//
// 字段直接复用服务层结构：多一层镜像结构就多一处漏字段的机会，
// 而这里每一个字段都直接决定「能不能抵」，漏一个就是算错税。
type DeductionRequest = service.DeductionRequest

// JudgeDeduction 判定一笔进项税额能否抵扣。
func (a *App) JudgeDeduction(req DeductionRequest) (out *service.DeductionView, err error) {
	defer recoverTo(&err, "JudgeDeduction")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.JudgeDeduction(a.context(), req))
}

// VATReference 返回凭证类型、计税方法、业务类型等参照表。
func (a *App) VATReference() (out *service.VATReferenceInfo, err error) {
	defer recoverTo(&err, "VATReference")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	// VATReference 不返回 error（它只是构造一张静态参照表），
	// 但绑定必须是 (T, error) —— 见 wrap 的说明。
	return wrap(svc.VATReference(), nil)
}

// VATIdentity 返回账套的两套身份（增值税纳税人身份 + 企业规模类型）。
func (a *App) VATIdentity() (out *service.VATIdentityInfo, err error) {
	defer recoverTo(&err, "VATIdentity")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.VATIdentity(a.context()))
}

// SetVATStatusRequest 是变更纳税人身份的请求。
type SetVATStatusRequest struct {
	Status string `json:"status"`
	From   string `json:"from"`
	Note   string `json:"note"`
}

// SetVATStatus 变更增值税纳税人身份（会留下历史，按业务发生日取值）。
func (a *App) SetVATStatus(req SetVATStatusRequest) (err error) {
	defer recoverTo(&err, "SetVATStatus")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	if serr := svc.SetVATStatus(a.context(), req.Status, req.From, req.Note); serr != nil {
		return classify(serr)
	}
	return nil
}

// SetEnterpriseScale 设置企业规模类型（所得税与统计口径，与增值税无关）。
func (a *App) SetEnterpriseScale(scale string) (err error) {
	defer recoverTo(&err, "SetEnterpriseScale")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	if serr := svc.SetEnterpriseScale(a.context(), scale); serr != nil {
		return classify(serr)
	}
	return nil
}

// ExportVATPolicies 把政策表导出为 JSON（可在界面外编辑后导回）。
func (a *App) ExportVATPolicies(dest string) (err error) {
	defer recoverTo(&err, "ExportVATPolicies")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	if serr := svc.ExportVATPolicies(a.context(), dest); serr != nil {
		return classify(serr)
	}
	return nil
}

// ImportVATPolicies 从 JSON 导入政策表。
func (a *App) ImportVATPolicies(src string) (out int, err error) {
	defer recoverTo(&err, "ImportVATPolicies")()
	svc, f := a.book()
	if f != nil {
		return 0, f
	}
	return wrap(svc.ImportVATPolicies(a.context(), src))
}

// VATPolicyStatus 报告政策表版本，界面据此提示「内置政策表已更新」。
func (a *App) VATPolicyStatus() (out service.VATPolicyStatus, err error) {
	defer recoverTo(&err, "VATPolicyStatus")()
	svc, f := a.book()
	if f != nil {
		return service.VATPolicyStatus{}, f
	}
	return wrap(svc.PolicyStatus(a.context()))
}

// ResetVATPolicies 把政策表恢复成当前版本的内置默认表。
//
// 会丢掉用户自己改过的税率，界面必须先确认。
func (a *App) ResetVATPolicies() (err error) {
	defer recoverTo(&err, "ResetVATPolicies")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	return classify(svc.ResetVATPolicies(a.context()))
}

// ---------------------------------------------------------------------------
// 演示账套
// ---------------------------------------------------------------------------

// CreateDemoBook 一步生成一个带演示数据的账套。
//
// 原先只有命令行有 demo 命令，而「先造一个演示账套看看软件长什么样」
// 恰恰是第一次打开界面的人最需要的功能。现在两边共用同一份生成逻辑。
//
// 已经建过账的会被拒绝而不是覆盖 —— 覆盖等于删掉用户的账，
// 这种事不该由一个「看看演示」的按钮触发。
func (a *App) CreateDemoBook(toMonth int) (out *service.DemoResult, err error) {
	defer recoverTo(&err, "CreateDemoBook")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.CreateDemoBook(a.context(), toMonth))
}
