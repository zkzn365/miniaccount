package main

import (
	"encoding/base64"
	"strings"

	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 审计证据链
// ---------------------------------------------------------------------------
//
// 三个绑定：读证据链、挂一份依据、摘一份依据。
//
// ★ 证据只挂在**底稿**上，不动账 —— 所以这里没有「过账」之类的词。
// 挂错的代价是底稿上多一行，删掉就完事；但它是审计结论的出处，
// 所以每一次挂与摘都进操作日志。

// Evidence 返回某期的证据链（每条结论 + 它的依据）。
func (a *App) Evidence(req PeriodRequest) (out *service.EvidenceView, err error) {
	defer recoverTo(&err, "Evidence")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	k, kerr := req.key()
	if kerr != nil {
		return nil, &Fault{Kind: FaultInvalid, Message: kerr.Error()}
	}
	return wrap(svc.Evidence(a.context(), k))
}

// EvidenceKinds 返回证据种类的选项（界面下拉用）。
func (a *App) EvidenceKinds() (out []service.RefKindOption, err error) {
	defer recoverTo(&err, "EvidenceKinds")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return svc.RefKindOptions(), nil
}

// AddEvidenceRequest 是挂一份依据的入参。
//
// 附件的两种来源：
//
//	Hash     引用账套里已有的一份附件（同一张发票可以既挂发票又作依据）
//	FileName + DataBase64   上传一份新的
type AddEvidenceRequest struct {
	OwnerType string `json:"ownerType"`
	OwnerID   int64  `json:"ownerId"`
	RefKind   string `json:"refKind"`
	RefID     int64  `json:"refId"`
	RefLabel  string `json:"refLabel"`
	Note      string `json:"note"`
	Hash      string `json:"hash"`
	// FileName / DataBase64 用于上传新附件。
	FileName   string `json:"fileName"`
	DataBase64 string `json:"dataBase64"`
	By         string `json:"by"`
}

// AddEvidence 把一份资料挂到一个结论下。
func (a *App) AddEvidence(req AddEvidenceRequest) (out *service.EvidenceView, err error) {
	defer recoverTo(&err, "AddEvidence")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	var data []byte
	if raw := strings.TrimSpace(req.DataBase64); raw != "" {
		// 去掉 data:application/pdf;base64, 前缀
		if i := strings.Index(raw, "base64,"); i >= 0 {
			raw = raw[i+len("base64,"):]
		}
		d, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
		if derr != nil {
			return nil, &Fault{Kind: FaultInvalid, Message: "附件内容不是合法的 base64"}
		}
		data = d
	}
	return wrap(svc.AddEvidence(a.context(), service.EvidenceInput{
		OwnerType: req.OwnerType, OwnerID: req.OwnerID,
		RefKind: req.RefKind, RefID: req.RefID,
		RefLabel: req.RefLabel, Note: req.Note,
		Hash: req.Hash, FileName: req.FileName, Data: data,
		By: req.By,
	}))
}

// DeleteEvidenceRequest 是摘掉一条依据的入参。
type DeleteEvidenceRequest struct {
	ID int64 `json:"id"`
	// By 是操作人（摘依据也要留痕）。
	By string `json:"by"`
}

// DeleteEvidence 摘掉一条依据。
func (a *App) DeleteEvidence(req DeleteEvidenceRequest) (out *service.EvidenceView, err error) {
	defer recoverTo(&err, "DeleteEvidence")()
	svc, f := a.book()
	if f != nil {
		return nil, f
	}
	return wrap(svc.DeleteEvidence(a.context(), req.ID, req.By))
}
