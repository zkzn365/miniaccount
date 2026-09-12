package main

import (
	"encoding/base64"
	"strings"

	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 银行流水的绑定
// ---------------------------------------------------------------------------

// BankFlowQueryRequest 是流水列表的筛选条件。
type BankFlowQueryRequest struct {
	Status string `json:"status"`
	From   string `json:"from"`
	To     string `json:"to"`
	Limit  int    `json:"limit"`
}

// BankFlows 返回银行流水列表。
func (a *App) BankFlows(req BankFlowQueryRequest) (out []service.BankFlowView, err error) {
	defer recoverTo(&err, "BankFlows")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.BankFlows(a.context(), service.BankFlowQuery{
		Status: req.Status, From: req.From, To: req.To, Limit: req.Limit,
	}))
}

// BankStats 返回流水的状态统计。
func (a *App) BankStats() (out *service.BankStats, err error) {
	defer recoverTo(&err, "BankStats")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.BankStats(a.context()))
}

// BankImportRequest 是导入对账单的参数。
//
// ★ 文件内容用 base64 传原始**字节**。
//
// 不能让前端把文件读成字符串再传：银行对账单大量使用 GB18030，
// 前端按 UTF-8 解码会把中文全变成乱码，而乱码进到解析器里
// 只会报「列名识别不出来」—— 用户完全想不到是编码问题。
// 传字节由 Go 侧做编码嗅探，才可能识别正确。
type BankImportRequest struct {
	AccountCode string `json:"accountCode"`
	FileName    string `json:"fileName"`
	DataBase64  string `json:"dataBase64"`
	ImportedBy  string `json:"importedBy"`
}

// ImportBankFlows 导入一份银行对账单。
func (a *App) ImportBankFlows(req BankImportRequest) (out *service.BankImportResult, err error) {
	defer recoverTo(&err, "ImportBankFlows")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	raw := req.DataBase64
	if i := strings.Index(raw, "base64,"); i >= 0 {
		raw = raw[i+len("base64,"):]
	}
	data, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if derr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: "对账单内容不是合法的 base64"}
	}
	return wrap(svc.ImportBankStatement(a.context(), service.BankImportInput{
		AccountCode: req.AccountCode, FileName: req.FileName,
		Data: data, ImportedBy: req.ImportedBy,
	}))
}

// MatchBankFlows 对未匹配的流水做批量匹配。
func (a *App) MatchBankFlows() (out *service.BankMatchResult, err error) {
	defer recoverTo(&err, "MatchBankFlows")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.MatchBankFlows(a.context()))
}

// BankPostRequest 是批量生成凭证的参数。
type BankPostRequest struct {
	IDs []int64 `json:"ids"`
	// PostingBy 是记账人。
	PostingBy string `json:"postingBy"`
}

// PostBankFlows 把已匹配的流水批量生成凭证。
func (a *App) PostBankFlows(req BankPostRequest) (out *service.BankPostResult, err error) {
	defer recoverTo(&err, "PostBankFlows")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	if strings.TrimSpace(req.PostingBy) == "" {
		return nil, &Fault{Kind: FaultInvalid,
			Message: "请填写记账人 —— 凭证需要有记账签章"}
	}
	return wrap(svc.PostBankFlows(a.context(), req.IDs, req.PostingBy))
}

// IgnoreBankFlow 忽略一条流水。
func (a *App) IgnoreBankFlow(id int64, reason string) (err error) {
	defer recoverTo(&err, "IgnoreBankFlow")()
	svc, f := a.book()
	if f != nil {
		return f
	}
	if ierr := svc.IgnoreBankFlow(a.context(), id, reason); ierr != nil {
		return classify(ierr)
	}
	return nil
}

// BankRuleRequest 是新增规则的参数。
type BankRuleRequest struct {
	Name               string `json:"name"`
	Pattern            string `json:"pattern"`
	CounterAccountCode string `json:"counterAccountCode"`
	Direction          string `json:"direction"`
	ContactID          *int64 `json:"contactId"`
	EmployeeID         *int64 `json:"employeeId"`
	DeptID             *int64 `json:"deptId"`
	ProjectID          *int64 `json:"projectId"`
}

// SaveBankRule 新增一条匹配规则。
func (a *App) SaveBankRule(req BankRuleRequest) (out int64, err error) {
	defer recoverTo(&err, "SaveBankRule")()
	svc, f := a.book()
	if f != nil {
		return 0, f
	}
	return wrap(svc.SaveBankRule(a.context(), service.BankRuleInput{
		Name: req.Name, Pattern: req.Pattern,
		CounterAccountCode: req.CounterAccountCode, Direction: req.Direction,
		ContactID: req.ContactID, EmployeeID: req.EmployeeID,
		DeptID: req.DeptID, ProjectID: req.ProjectID,
	}))
}

// BankRules 返回全部匹配规则。
func (a *App) BankRules() (out []service.BankRuleView, err error) {
	defer recoverTo(&err, "BankRules")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.BankRules(a.context()))
}

// BankSuggestionRequest 是人工为一条流水指定记账方案的参数。
type BankSuggestionRequest struct {
	FlowID int64 `json:"flowId"`
	// CounterAccountCode 是对方科目编码。
	CounterAccountCode string `json:"counterAccountCode"`
	// 四维辅助核算。科目要求哪一维就必须给哪一维。
	ContactID  *int64 `json:"contactId"`
	EmployeeID *int64 `json:"employeeId"`
	DeptID     *int64 `json:"deptId"`
	ProjectID  *int64 `json:"projectId"`
	Memo       string `json:"memo"`
}

// SetBankSuggestion 人工指定一条流水的记账方案。
func (a *App) SetBankSuggestion(req BankSuggestionRequest) (out *service.BankFlowView, err error) {
	defer recoverTo(&err, "SetBankSuggestion")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.SetBankSuggestion(a.context(), req.FlowID, service.BankSuggestionInput{
		CounterAccountCode: req.CounterAccountCode,
		ContactID:          req.ContactID,
		EmployeeID:         req.EmployeeID,
		DeptID:             req.DeptID,
		ProjectID:          req.ProjectID,
		Memo:               req.Memo,
	}))
}
