package main

import (
	"miniaccount/internal/domain/account"
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 科目管理
// ---------------------------------------------------------------------------

// Accounts 返回科目表（含使用情况），供科目管理页用。
func (a *App) Accounts() (out *service.AccountsView, err error) {
	defer recoverTo(&err, "Accounts")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.Accounts(a.context()))
}

// SaveAccount 新增或修改一个科目（编码已存在即修改）。
func (a *App) SaveAccount(in service.AccountInput) (out *service.AccountRow, err error) {
	defer recoverTo(&err, "SaveAccount")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.SaveAccount(a.context(), in))
}

// SetAccountEnabled 启用/停用科目。
func (a *App) SetAccountEnabled(code string, enabled bool) (err error) {
	defer recoverTo(&err, "SetAccountEnabled")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	return classify(svc.SetAccountEnabled(a.context(), code, enabled))
}

// DeleteAccount 删除一个从未用过的科目。
func (a *App) DeleteAccount(code string) (err error) {
	defer recoverTo(&err, "DeleteAccount")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	return classify(svc.DeleteAccount(a.context(), code))
}

// AccountKinds 返回「大类 / 辅助核算」的选项（界面表单用）。
//
// 单独一个绑定而不是塞进 Accounts()：表单在新增时才需要它，
// 而科目表每次进页面都要拉。
type AccountKindsView struct {
	RootTypes []AccountKindOption `json:"rootTypes"`
	AuxTypes  []AccountKindOption `json:"auxTypes"`
	MaxLevel  int                 `json:"maxLevel"`
}

// AccountKindOption 是一个选项。
type AccountKindOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// AccountKinds 返回科目大类与辅助核算的选项。
func (a *App) AccountKinds() (out AccountKindsView, err error) {
	defer recoverTo(&err, "AccountKinds")()
	out.MaxLevel = 4
	out.RootTypes = []AccountKindOption{
		{Value: string(account.RootAsset), Label: "资产"},
		{Value: string(account.RootLiability), Label: "负债"},
		{Value: string(account.RootEquity), Label: "所有者权益"},
		{Value: string(account.RootCost), Label: "成本"},
		{Value: string(account.RootIncome), Label: "收入"},
		{Value: string(account.RootExpense), Label: "费用"},
	}
	out.AuxTypes = []AccountKindOption{
		{Value: string(account.AuxCustomer), Label: "客户"},
		{Value: string(account.AuxSupplier), Label: "供应商"},
		{Value: string(account.AuxEmployee), Label: "员工"},
		{Value: string(account.AuxDept), Label: "部门"},
		{Value: string(account.AuxProject), Label: "项目"},
	}
	return out, nil
}
