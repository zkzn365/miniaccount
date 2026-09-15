package service_test

import (
	"context"
	"strings"
	"testing"

	"miniaccount/internal/ai/aiprovider"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 审计证据链
// ---------------------------------------------------------------------------
//
// 这一组测试盯的是同一个问题的三个面：
//
//	结论有依据吗？依据还在吗？两者说的话是不是同一句？
//
// 最后一条最容易糊：把「没有证据」与「证据丢了」说成一句话，
// 用户会去建一份新证据，而实际上该做的是把丢掉的原件找回来。

// actionContains 报告下一步里有没有提到某件事。
func actionContains(actions []string, want string) bool {
	for _, a := range actions {
		if strings.Contains(a, want) {
			return true
		}
	}
	return false
}

// wpChain 找某一个结论的证据链。
func wpChain(t *testing.T, v *service.EvidenceView, ownerType string, ownerID int64) service.EvidenceChainView {
	t.Helper()
	for _, c := range v.Chains {
		if c.OwnerType == ownerType && c.OwnerID == ownerID {
			return c
		}
	}
	t.Fatalf("证据链里没有 %s/%d，实际有 %d 条", ownerType, ownerID, len(v.Chains))
	return service.EvidenceChainView{}
}

// 期初：一期的证据链里应当有「底稿结论」这一条，而且它还没有依据。
func TestEvidenceStartsWithUnsupportedConclusion(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3)
	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	v, err := svc.Evidence(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatalf("读证据链失败: %v", err)
	}
	// 重要性水平 + 底稿结论 + 1 笔调整 = 3 条链
	if len(v.Chains) != 3 {
		t.Fatalf("证据链应当有 3 条，实际 %d 条：%+v", len(v.Chains), v.Chains)
	}
	if v.Unsupported != 3 {
		t.Errorf("3 条结论都还没有依据，unsupported 应当是 3，实际 %d", v.Unsupported)
	}
	if !strings.Contains(v.Concludes, "还没有附依据") {
		t.Errorf("结论 = %q", v.Concludes)
	}
	if len(v.NextActions) == 0 {
		t.Error("★ 只报「没有依据」不够 —— 要给出照着做的下一步")
	} else if !strings.Contains(v.NextActions[0], "附上资料") {
		t.Errorf("下一步要指向具体动作，实际 %q", v.NextActions[0])
	}

	adj := wpChain(t, v, "adjustment", id)
	if adj.Title == "" || !strings.Contains(adj.Title, "补提 2025 年折旧") {
		t.Errorf("调整链的标题应当带上它的摘要，实际 %q", adj.Title)
	}
	if adj.LinkTo != "adjustment/1" {
		t.Errorf("调整链应当指回底稿上的那笔调整，实际 %q", adj.LinkTo)
	}
	if !strings.Contains(adj.Concludes, "还没有附任何依据") {
		t.Errorf("调整链的结论 = %q", adj.Concludes)
	}
}

// 挂一份**外部资料**（纸质回函之类）：没有文件，但也是一份依据。
func TestAddExternalEvidence(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3)
	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	v, err := svc.AddEvidence(ctx, service.EvidenceInput{
		OwnerType: "adjustment", OwnerID: id,
		RefKind: "external", RefLabel: "折旧计算表（纸质，已归档 F-3）",
		Note: "表上少提 3,000.00", By: "李审计",
	})
	if err != nil {
		t.Fatalf("挂外部资料失败: %v", err)
	}
	adj := wpChain(t, v, "adjustment", id)
	if adj.Total != 1 || adj.Documents != 1 || adj.Files != 0 {
		t.Errorf("份数不对：共 %d（附件 %d / 单据 %d）", adj.Total, adj.Files, adj.Documents)
	}
	if v.Unsupported != 2 {
		t.Errorf("挂了一条依据后应当剩 2 条没有依据，实际 %d", v.Unsupported)
	}
	if v.Broken != 0 {
		t.Errorf("外部资料没有实体，不该算「丢了」，实际 broken=%d", v.Broken)
	}
	l := adj.Links[0]
	if l.LinkedBy != "李审计" || l.LinkedAt == "" {
		t.Errorf("依据要记清是谁什么时候挂的：%+v", l)
	}
	if l.HasFile || l.Missing {
		t.Errorf("外部资料既没有文件，也不该报丢失：%+v", l)
	}
	if !strings.Contains(adj.Concludes, "依据齐全") {
		t.Errorf("有一条依据时应当说齐，实际 %q", adj.Concludes)
	}
}

// ★ 上传附件 → 文件在 → 文件被清理掉之后必须报「找不到了」。
//
// 这一条是整条证据链的要害：底稿上写着「依据：折旧计算表.xlsx」，
// 而那份文件早就不在了 —— 比一开始就没写更糟，因为它给了假的确定感。
func TestAttachmentEvidenceDetectsMissingFile(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3)
	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	v, err := svc.AddEvidence(ctx, service.EvidenceInput{
		OwnerType: "adjustment", OwnerID: id, RefKind: "attachment",
		FileName: "折旧计算表.csv", Data: []byte("资产,本期折旧\n电脑,1000\n"),
		Note: "少提 3,000.00", By: "李审计",
	})
	if err != nil {
		t.Fatalf("上传附件失败: %v", err)
	}
	adj := wpChain(t, v, "adjustment", id)
	l := adj.Links[0]
	if !l.HasFile || l.Hash == "" || l.FileSize == 0 {
		t.Fatalf("附件类证据应当带上文件信息：%+v", l)
	}
	if l.Missing {
		t.Fatal("文件刚上传，不该报丢失")
	}
	if l.Path == "" {
		t.Fatal("有文件的证据应当给出磁盘路径，界面要靠它打开原件")
	}

	// 用户手动清理了 .files 目录（这是真实会发生的事）
	store, err := svc.Attachments()
	if err != nil {
		t.Fatalf("取附件仓库失败: %v", err)
	}
	if err := store.Delete(l.Hash); err != nil {
		t.Fatalf("删文件实体失败: %v", err)
	}

	v2, err := svc.Evidence(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatalf("重新读证据链失败: %v", err)
	}
	adj2 := wpChain(t, v2, "adjustment", id)
	l2 := adj2.Links[0]
	if !l2.Missing {
		t.Fatal("★ 文件实体已经删掉，必须报「找不到了」—— 否则底稿会提供假的确定感")
	}
	if l2.Path != "" {
		t.Errorf("文件已丢失时不该再给出路径，实际 %q", l2.Path)
	}
	if v2.Broken != 1 {
		t.Errorf("broken 应当是 1，实际 %d", v2.Broken)
	}
	if !strings.Contains(v2.Concludes, "找不到") {
		t.Errorf("★ 结论要说清是「原件丢了」而不是「没证据」：%q", v2.Concludes)
	}
	if strings.Contains(adj2.Concludes, "还没有附任何依据") {
		t.Error("★ 证据丢失不能说成「没有证据」—— 补的方式完全不同")
	}
	// 两类问题（还没有依据 / 依据丢了）都要给出下一步
	if !actionContains(v2.NextActions, "找不到") {
		t.Errorf("下一步里要说清丢失的那一类怎么办，实际 %v", v2.NextActions)
	}
	if !actionContains(v2.NextActions, "附上资料") {
		t.Errorf("下一步里也要说清还没附依据的那一类怎么办，实际 %v", v2.NextActions)
	}
}

// 单据类证据：挂一张凭证，快照里要有它的号与摘要；凭证被删掉之后要报丢失。
func TestVoucherEvidenceSnapshotAndDangle(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3)
	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	d, err := svc.SaveVoucher(ctx, service.VoucherInput{
		Word: "记", Date: "2025-03-31", Remark: "计提本月折旧", CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			{AccountCode: "560205", Summary: "计提折旧", Debit: money100(1000), DeptID: &dept},
			{AccountCode: "1602", Summary: "计提折旧", Credit: money100(1000)},
		},
	})
	if err != nil {
		t.Fatalf("存凭证失败: %v", err)
	}

	v, err := svc.AddEvidence(ctx, service.EvidenceInput{
		OwnerType: "adjustment", OwnerID: id, RefKind: "voucher", RefID: d.ID,
		Note: "这张凭证上的折旧额就是少提的那部分", By: "李审计",
	})
	if err != nil {
		t.Fatalf("挂凭证依据失败: %v", err)
	}
	l := wpChain(t, v, "adjustment", id).Links[0]
	if l.Missing {
		t.Fatal("凭证还在，不该报丢失")
	}
	// ★ 快照要写清「当时是哪一张」，不能只存 id
	if !strings.Contains(l.RefLabel, "计提本月折旧") {
		t.Errorf("★ 依据的标签应当记下**当时**那张凭证的样子，实际 %q", l.RefLabel)
	}
	if l.RefID != d.ID {
		t.Errorf("单据 id = %d，期望 %d", l.RefID, d.ID)
	}

	// 草稿可以删；删掉之后这条依据就悬空了
	if err := svc.DeleteVoucher(ctx, d.ID); err != nil {
		t.Fatalf("删凭证失败: %v", err)
	}
	v2, _ := svc.Evidence(ctx, period.NewKey(2025, 3))
	l2 := wpChain(t, v2, "adjustment", id).Links[0]
	if !l2.Missing {
		t.Error("★ 凭证已经删掉，这条依据必须报「找不到了」")
	}
	if l2.RefLabel == "" {
		t.Error("即使单据没了，快照也要留着 —— 底稿要能说出当时依据的是哪一张")
	}
}

// 同一份资料挂两次不该算两份依据。
func TestAddEvidenceRejectsDuplicate(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3)
	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	in := service.EvidenceInput{
		OwnerType: "adjustment", OwnerID: id, RefKind: "external",
		RefLabel: "折旧计算表（纸质）", By: "李审计",
	}
	if _, err := svc.AddEvidence(ctx, in); err != nil {
		t.Fatalf("首次挂依据失败: %v", err)
	}
	if _, err := svc.AddEvidence(ctx, in); err == nil {
		t.Fatal("同一份资料挂两次应当被拦下 —— 重复挂不会更可信，只会让份数虚高")
	} else if !strings.Contains(err.Error(), "已经挂在这个结论下") {
		t.Errorf("报错要说清原因，实际 %v", err)
	}
}

// 挂依据时的几道护栏。
func TestAddEvidenceGuardrails(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	base := service.EvidenceInput{
		OwnerType: "adjustment", OwnerID: id, RefKind: "external",
		RefLabel: "访谈记录", By: "李审计",
	}
	cases := []struct {
		name string
		mod  func(in *service.EvidenceInput)
		want string
	}{
		{"没有操作人", func(in *service.EvidenceInput) { in.By = "  " }, "操作人"},
		{"结论种类不认识", func(in *service.EvidenceInput) { in.OwnerType = "feeling" }, "结论种类"},
		{"证据种类不认识", func(in *service.EvidenceInput) { in.RefKind = "photo" }, "证据种类"},
		{"什么都没写", func(in *service.EvidenceInput) { in.RefLabel = "" }, "要写清"},
		{"调整不存在", func(in *service.EvidenceInput) { in.OwnerID = 99999 }, "不存在"},
		{"凭证不存在", func(in *service.EvidenceInput) {
			in.RefKind, in.RefID, in.RefLabel = "voucher", 99999, ""
		}, "凭证不存在"},
		{"引用的附件不在账套里", func(in *service.EvidenceInput) {
			in.RefKind = "attachment"
			in.Hash = strings.Repeat("a", 64)
			in.FileName = "不存在的.pdf"
		}, "不在账套里"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base
			c.mod(&in)
			if _, err := svc.AddEvidence(ctx, in); err == nil {
				t.Fatal("应当被拦下")
			} else if !strings.Contains(err.Error(), c.want) {
				t.Errorf("报错应当提到 %q，实际 %v", c.want, err)
			}
		})
	}
	if _, err := svc.AddEvidence(ctx, base); err != nil {
		t.Fatalf("基准用例本身应当通过：%v", err)
	}
}

// 重要性水平还没定，就不该给它挂依据 —— 那等于给一个不存在的门槛找理由。
func TestEvidenceForMaterialityRequiresMateriality(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	periodID := int64(2025*100 + 3)

	_, err := svc.AddEvidence(ctx, service.EvidenceInput{
		OwnerType: "materiality", OwnerID: periodID, RefKind: "external",
		RefLabel: "上年审计报告", By: "李审计",
	})
	if err == nil {
		t.Fatal("本期还没定重要性水平，不该能给它挂依据")
	}
	if !strings.Contains(err.Error(), "先定门槛") {
		t.Errorf("报错应当说清先做什么，实际 %v", err)
	}

	seedMateriality(t, svc, 2025, 3)
	v, err := svc.AddEvidence(ctx, service.EvidenceInput{
		OwnerType: "materiality", OwnerID: periodID, RefKind: "external",
		RefLabel: "上年审计报告（资产总额 1,200 万）",
		Note:     "按上年资产总额的 0.5% 确定", By: "李审计",
	})
	if err != nil {
		t.Fatalf("定了门槛之后应当能挂依据：%v", err)
	}
	m := wpChain(t, v, "materiality", periodID)
	if m.Total != 1 {
		t.Fatalf("重要性水平链上应当有 1 份依据，实际 %d", m.Total)
	}
	if !strings.Contains(m.Title, "资产总额") {
		t.Errorf("重要性水平链的标题应当写清门槛，实际 %q", m.Title)
	}
}

// 摘掉一条依据：它能被删掉，而且删完链上确实少了一条。
func TestDeleteEvidence(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3)
	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	v, err := svc.AddEvidence(ctx, service.EvidenceInput{
		OwnerType: "adjustment", OwnerID: id, RefKind: "external",
		RefLabel: "访谈记录", By: "李审计",
	})
	if err != nil {
		t.Fatalf("挂依据失败: %v", err)
	}
	linkID := wpChain(t, v, "adjustment", id).Links[0].ID

	v2, err := svc.DeleteEvidence(ctx, linkID, "李审计")
	if err != nil {
		t.Fatalf("摘依据失败: %v", err)
	}
	if got := wpChain(t, v2, "adjustment", id).Total; got != 0 {
		t.Errorf("摘掉之后不该还有依据，实际 %d 条", got)
	}
	if _, err := svc.DeleteEvidence(ctx, linkID, "李审计"); err == nil {
		t.Error("再摘一次应当报「找不到」")
	}
}

// ★ 删掉一笔调整，挂在它下面的证据也要一起走。
//
// 留着的话，审计轨迹上会有一条「依据：折旧计算表」挂在一个
// 已经不存在的结论下 —— 而底稿的全部意义就是可追溯。
func TestDeletingAdjustmentRemovesItsEvidence(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3)
	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	if _, err := svc.AddEvidence(ctx, service.EvidenceInput{
		OwnerType: "adjustment", OwnerID: id, RefKind: "attachment",
		FileName: "折旧表.csv", Data: []byte("a,b\n1,2\n"), By: "李审计",
	}); err != nil {
		t.Fatalf("挂附件失败: %v", err)
	}
	if _, err := svc.DeleteAdjustment(ctx, id); err != nil {
		t.Fatalf("删除调整失败: %v", err)
	}

	v, err := svc.Evidence(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatalf("读证据链失败: %v", err)
	}
	for _, c := range v.Chains {
		if c.OwnerType == "adjustment" {
			t.Errorf("调整已删除，它的证据链不该还在：%+v", c)
		}
	}
	// 文件实体不删（可能还被别的单据引用），但记录必须清掉
	rows, err := svc.DB().SQL().QueryContext(ctx,
		`SELECT COUNT(*) FROM audit_evidence WHERE owner_type = 'adjustment' AND owner_id = ?`, id)
	if err != nil {
		t.Fatalf("查证据表失败: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("调整删除后还留着 %d 条证据", n)
		}
	}
}

// ★ 描述类证据（外部资料 / 合同）按**文字**判重，不是「每样只能挂一份」。
//
// 发布前审计实测：0013 的表级 UNIQUE 对 ref_id=0、sha256=” 的描述类会退化，
// 于是第二份访谈记录、第二份回函挂不上 —— 而它们本来是不同的资料。
func TestAddEvidenceAllowsMultipleDescriptive(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3)
	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	for _, label := range []string{"访谈记录（财务经理）", "访谈记录（仓库主管）", "应收账款函证回函"} {
		if _, err := svc.AddEvidence(ctx, service.EvidenceInput{
			OwnerType: "adjustment", OwnerID: id, RefKind: "external",
			RefLabel: label, By: "李审计",
		}); err != nil {
			t.Fatalf("★ 第二份外部资料应当能挂上（%s）：%v", label, err)
		}
	}
	// 同一段文字再挂一次仍然算重复
	if _, err := svc.AddEvidence(ctx, service.EvidenceInput{
		OwnerType: "adjustment", OwnerID: id, RefKind: "external",
		RefLabel: "访谈记录（财务经理）", By: "李审计",
	}); err == nil {
		t.Error("同一段文字挂两次应当算重复")
	}

	v, err := svc.Evidence(ctx, period.NewKey(2025, 3))
	if err != nil {
		t.Fatal(err)
	}
	if got := wpChain(t, v, "adjustment", id).Total; got != 3 {
		t.Errorf("三份不同的外部资料都该挂着，实际 %d 份", got)
	}
}

// ★ AI 读到的证据链必须与界面一致，包括「原件已找不到」。
//
// 发布前审计实测：AI 走的是另一份实现，报不出 Broken，
// 于是界面上写着「另有 1 项的依据已经找不到」，而模型对同一期说
// 「这笔调整有依据」—— 恰好把证据链存在的理由绕过去了。
func TestEvidenceBriefReportsBroken(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	dept := mustDepartment(t, svc, "生产部")
	seedMateriality(t, svc, 2025, 3)
	id, _ := addAdjustment(t, svc, depAdjustmentInput(t, svc, 2025, 3, dept, 3000))

	d, err := svc.SaveVoucher(ctx, service.VoucherInput{
		Word: "记", Date: "2025-03-31", Remark: "计提折旧", CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			{AccountCode: "560205", Summary: "计提", Debit: money100(1000), DeptID: &dept},
			{AccountCode: "1602", Summary: "计提", Credit: money100(1000)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddEvidence(ctx, service.EvidenceInput{
		OwnerType: "adjustment", OwnerID: id, RefKind: "voucher", RefID: d.ID,
		By: "李审计",
	}); err != nil {
		t.Fatalf("挂凭证依据失败: %v", err)
	}

	brief, err := svc.EvidenceBrief(ctx, aiprovider.PeriodKey{Year: 2025, Month: 3})
	if err != nil {
		t.Fatalf("生成给 AI 的证据链失败: %v", err)
	}
	if brief.Broken != 0 {
		t.Errorf("凭证还在，不该有「找不到」：%d", brief.Broken)
	}

	// 删掉那张凭证 → 依据悬空
	if err := svc.DeleteVoucher(ctx, d.ID); err != nil {
		t.Fatalf("删除凭证失败: %v", err)
	}
	brief, err = svc.EvidenceBrief(ctx, aiprovider.PeriodKey{Year: 2025, Month: 3})
	if err != nil {
		t.Fatal(err)
	}
	if brief.Broken != 1 {
		t.Errorf("★ AI 也要看到「有 1 项依据已找不到」，实际 %d", brief.Broken)
	}
	total := 0
	for _, c := range brief.Chains {
		total += c.Missing
	}
	if total != 1 {
		t.Errorf("链上也要标出找不到的份数，实际 %d", total)
	}
	// 渲染出来的文本必须明确告诉模型不要断言「有依据」
	// （renderEvidence 在 aiprovider 内部，这里只验数据层）
}

// ★ 删掉重要性水平时，挂在它下面的证据也要一起走。
//
// 不清的话：证据行还在（链上看不到），等重新确定重要性水平，
// 上一版门槛的依据会静默复活挂到新结论上。
func TestDeleteMaterialityClearsItsEvidence(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 6)
	seedMateriality(t, svc, 2025, 3)
	periodID := int64(2025*100 + 3)
	if _, err := svc.AddEvidence(ctx, service.EvidenceInput{
		OwnerType: "materiality", OwnerID: periodID, RefKind: "external",
		RefLabel: "上年审计报告", By: "李审计",
	}); err != nil {
		t.Fatalf("挂依据失败: %v", err)
	}
	if _, err := svc.DeleteMateriality(ctx, 2025, 3); err != nil {
		t.Fatalf("删除重要性水平失败: %v", err)
	}
	rows, err := svc.DB().SQL().QueryContext(ctx,
		`SELECT COUNT(*) FROM audit_evidence WHERE owner_type='materiality' AND owner_id = ?`, periodID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("★ 删掉重要性水平之后还留着 %d 条依据 —— 重设门槛时它们会静默复活", n)
		}
	}
}
