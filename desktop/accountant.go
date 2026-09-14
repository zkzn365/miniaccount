package main

import "miniaccount/internal/service"

// ---------------------------------------------------------------------------
// 会计（对话式记账）
// ---------------------------------------------------------------------------
//
// 界面上只有一个「业务描述」输入框。缺的信息由会计**问**，
// 而不是让用户先填一张表单 —— 那是会计要问的问题，不是用户想说的话。

// AccountantSendRequest 是「跟会计说一句话」。
type AccountantSendRequest struct {
	// SessionID 留空表示开一段新对话。
	SessionID string `json:"sessionId"`
	// Text 是用户说的话（第一句通常是业务描述）。
	Text string `json:"text"`
}

// AccountantSend 把用户的一句话交给会计，返回整段对话。
//
// 会计要么**问一句**（信息不够），要么**给出凭证草稿**。
// 给出的草稿仍然是草稿：采纳走 AcceptAISuggestion，
// 过账仍然要等到账期结算。
func (a *App) AccountantSend(req AccountantSendRequest) (out *service.AccountantSession, err error) {
	defer recoverTo(&err, "AccountantSend")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.AccountantSend(a.context(), service.AccountantSendInput{
		SessionID: req.SessionID, Text: req.Text,
	}))
}

// AccountantSession 读回一段对话。
func (a *App) AccountantSession(id string) (out *service.AccountantSession, err error) {
	defer recoverTo(&err, "AccountantSession")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.AccountantSession(id))
}

// AccountantReset 清空一段对话（换一笔业务重新说）。
func (a *App) AccountantReset(id string) (out *service.AccountantSession, err error) {
	defer recoverTo(&err, "AccountantReset")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.AccountantReset(id))
}
