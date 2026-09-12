package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/service"
	"miniaccount/internal/store/sqlite"
)

// ---------------------------------------------------------------------------
// 凭证录入：★ 界面上的主路径
// ---------------------------------------------------------------------------

// newVoucherLine 构造一条分录输入。
func newVoucherLine(code, summary string, debit, credit money.Money) service.VoucherLineInput {
	return service.VoucherLineInput{
		AccountCode: code, Summary: summary, Debit: debit, Credit: credit,
	}
}

// 存一张合法草稿，返回它的详情。
func saveDraft(t *testing.T, svc *service.Service,
	in service.VoucherInput) *service.VoucherDetail {
	t.Helper()
	d, err := svc.SaveVoucher(context.Background(), in)
	if err != nil {
		t.Fatalf("保存草稿失败: %v", err)
	}
	return d
}

func basicInput() service.VoucherInput {
	return service.VoucherInput{
		Word: "记", Date: "2025-01-15", Remark: "收到股东投资款",
		CreatedBy: "李会计",
		Lines: []service.VoucherLineInput{
			newVoucherLine("1002", "收到投资款", money100(100000), 0),
			newVoucherLine("3001", "收到投资款", 0, money100(100000)),
		},
	}
}

func TestSaveAndPostVoucher(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	// 3001 实收资本要股东辅助核算
	in := basicInput()
	shareholder := mustContact(t, svc, "shareholder", "张三")
	in.Lines[1].ContactID = &shareholder

	d := saveDraft(t, svc, in)
	if d.Status != "draft" {
		t.Errorf("新保存的应是草稿，实际 %s", d.Status)
	}
	if d.No != "" {
		t.Errorf("草稿不该有凭证号，实际 %q", d.No)
	}
	if !d.Balanced {
		t.Error("借贷应平衡")
	}
	if len(d.Lines) != 2 {
		t.Fatalf("分录数 = %d", len(d.Lines))
	}
	if d.Lines[0].AccountName != "银行存款" {
		t.Errorf("科目名 = %q", d.Lines[0].AccountName)
	}
	if !strings.Contains(d.Lines[1].AuxDesc, "张三") {
		t.Errorf("辅助核算应显示名称而不是 id，实际 %q", d.Lines[1].AuxDesc)
	}
	// 状态机由服务端算好给界面
	if !d.CanPost || !d.CanEdit || !d.CanDelete {
		t.Errorf("草稿应可编辑/可删除/可过账: %+v", d)
	}

	posted, err := svc.PostVoucher(ctx, d.ID, "王主管")
	if err != nil {
		t.Fatalf("过账失败: %v", err)
	}
	if posted.Status != "posted" {
		t.Errorf("状态 = %s", posted.Status)
	}
	if posted.No == "" {
		t.Error("过账后应有凭证号")
	}
	if !strings.HasPrefix(posted.No, "记-2025-01-") {
		t.Errorf("凭证号格式 = %q", posted.No)
	}
	if posted.PostedBy != "王主管" {
		t.Errorf("记账人 = %q", posted.PostedBy)
	}
	// 已过账的凭证不能再改
	if posted.CanEdit || posted.CanDelete {
		t.Error("已过账的凭证不该可编辑或删除")
	}
	if posted.BlockedReason == "" {
		t.Error("不可编辑时要说明原因 —— 只说「状态不允许」等于没说")
	}
	if !strings.Contains(posted.BlockedReason, "红字冲销") {
		t.Errorf("原因应指出正确的做法，实际 %q", posted.BlockedReason)
	}
}

// ★ 草稿不占凭证号：写十张草稿再逐张过账，号必须连着
func TestDraftsDoNotConsumeNumbers(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	sh := mustContact(t, svc, "shareholder", "张三")

	mk := func(remark string) int64 {
		in := basicInput()
		in.Remark = remark
		in.Lines[1].ContactID = &sh
		return saveDraft(t, svc, in).ID
	}
	ids := []int64{mk("第一笔"), mk("第二笔"), mk("第三笔")}

	// 先删掉中间那张，再过账其余两张
	if err := svc.DeleteVoucher(ctx, ids[1]); err != nil {
		t.Fatalf("删除草稿失败: %v", err)
	}
	nos := []string{}
	for _, id := range []int64{ids[0], ids[2]} {
		d, err := svc.PostVoucher(ctx, id, "王主管")
		if err != nil {
			t.Fatalf("过账失败: %v", err)
		}
		nos = append(nos, d.No)
	}
	if nos[0] != "记-2025-01-0001" || nos[1] != "记-2025-01-0002" {
		t.Errorf("草稿不该占号，两次过账应为 0001/0002，实际 %v", nos)
	}
}

// 只有草稿能删
func TestDeletePostedVoucherFails(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	sh := mustContact(t, svc, "shareholder", "张三")
	in := basicInput()
	in.Lines[1].ContactID = &sh
	d := saveDraft(t, svc, in)
	if _, err := svc.PostVoucher(ctx, d.ID, "王主管"); err != nil {
		t.Fatal(err)
	}
	err := svc.DeleteVoucher(ctx, d.ID)
	if err == nil {
		t.Fatal("已过账凭证必须拒绝删除")
	}
	if !errors.Is(err, sqlite.ErrNotDraft) {
		t.Errorf("应报 ErrNotDraft，实际 %v", err)
	}
}

// 已过账的凭证不能改 —— 只能红字冲销
func TestEditPostedVoucherFails(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	sh := mustContact(t, svc, "shareholder", "张三")
	in := basicInput()
	in.Lines[1].ContactID = &sh
	d := saveDraft(t, svc, in)
	if _, err := svc.PostVoucher(ctx, d.ID, "王主管"); err != nil {
		t.Fatal(err)
	}

	in.ID = d.ID
	in.Remark = "改了摘要"
	_, err := svc.SaveVoucher(ctx, in)
	if err == nil {
		t.Fatal("已过账凭证必须拒绝修改")
	}
	if !strings.Contains(err.Error(), "草稿") {
		t.Errorf("错误信息应说明只有草稿能改，实际 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

func TestSaveDraftValidation(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	sh := mustContact(t, svc, "shareholder", "张三")

	cases := []struct {
		name string
		mut  func(*service.VoucherInput)
		want string
	}{
		{"借贷不平", func(in *service.VoucherInput) {
			in.Lines[1].Credit = money100(99999)
		}, "平衡"},
		{"科目不存在", func(in *service.VoucherInput) {
			in.Lines[0].AccountCode = "9999"
		}, "9999"},
		{"科目是汇总科目", func(in *service.VoucherInput) {
			in.Lines[0].AccountCode = "2211" // 应付职工薪酬是汇总科目
		}, ""},
		{"缺辅助核算", func(in *service.VoucherInput) {
			// 1122 应收账款要客户
			in.Lines[0].AccountCode = "1122"
		}, "客户"},
		{"只有一条分录", func(in *service.VoucherInput) {
			in.Lines = in.Lines[:1]
		}, "分录"},
		{"摘名为空", func(in *service.VoucherInput) {
			in.Lines[0].Summary = ""
		}, "摘要"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := basicInput()
			in.Lines[1].ContactID = &sh
			c.mut(&in)
			_, err := svc.SaveVoucher(ctx, in)
			if err == nil {
				t.Fatalf("%s 应被拒绝", c.name)
			}
			if c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误信息应提到 %q，实际 %v", c.want, err)
			}
		})
	}
}

// ★ 草稿允许落在未启用的期间，但过账必须被拦下
func TestDraftAllowsClosedPeriodButPostDoesNot(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	sh := mustContact(t, svc, "shareholder", "张三")

	// 先把 1 月结账
	if _, err := svc.Close(ctx, period.NewKey(2025, 1), "王主管"); err != nil {
		t.Fatal(err)
	}

	in := basicInput()
	in.Lines[1].ContactID = &sh
	// 草稿：能存下来（用户可能先按记忆记下，过几天再处理）
	d, err := svc.SaveVoucher(ctx, in)
	if err != nil {
		t.Fatalf("草稿应允许落在已结账期间（真正要拦的是过账）：%v", err)
	}
	// 过账：必须被拦下
	_, err = svc.PostVoucher(ctx, d.ID, "王主管")
	if err == nil {
		t.Fatal("已结账期间必须拒绝过账")
	}
	if !errors.Is(err, period.ErrNotOpen) {
		t.Errorf("应报 ErrNotOpen，实际 %v", err)
	}
}

func TestPostRequiresPostedBy(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	sh := mustContact(t, svc, "shareholder", "张三")
	in := basicInput()
	in.Lines[1].ContactID = &sh
	d := saveDraft(t, svc, in)

	if _, err := svc.PostVoucher(ctx, d.ID, ""); err == nil {
		t.Fatal("缺少记账人应报错")
	}
	if _, err := svc.PostVoucher(ctx, d.ID, "   "); err == nil {
		t.Fatal("空白记账人应报错")
	}
}

// 空行应被静默跳过，而不是报「金额为零」
func TestBlankLinesAreSkipped(t *testing.T) {
	svc := newSvc(t, 3)
	sh := mustContact(t, svc, "shareholder", "张三")

	in := basicInput()
	in.Lines[1].ContactID = &sh
	// 界面上总会留着一行空的等录入
	in.Lines = append(in.Lines,
		service.VoucherLineInput{},                             // 全空
		service.VoucherLineInput{AccountCode: "", Summary: ""}, // 空壳
	)
	d := saveDraft(t, svc, in)
	if len(d.Lines) != 2 {
		t.Errorf("空行应被跳过，实际保存了 %d 条分录", len(d.Lines))
	}
}

// ---------------------------------------------------------------------------
// 红字冲销
// ---------------------------------------------------------------------------

func TestReverseVoucher(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	sh := mustContact(t, svc, "shareholder", "张三")
	in := basicInput()
	in.Lines[1].ContactID = &sh
	d := saveDraft(t, svc, in)
	posted, err := svc.PostVoucher(ctx, d.ID, "王主管")
	if err != nil {
		t.Fatal(err)
	}

	rev, err := svc.ReverseVoucher(ctx, posted.ID, "王主管", "")
	if err != nil {
		t.Fatalf("红字冲销失败: %v", err)
	}
	if rev.No == posted.No {
		t.Error("冲销凭证应有独立的凭证号")
	}
	if rev.ReversesNo != posted.No {
		t.Errorf("冲销凭证应记录被冲销的凭证号，实际 %q", rev.ReversesNo)
	}
	// ★ 借贷互换、金额仍为非负
	if rev.Lines[0].Credit != money100(100000) || !rev.Lines[0].Debit.IsZero() {
		t.Errorf("第 1 行应借贷互换，实际借 %s 贷 %s", rev.Lines[0].Debit, rev.Lines[0].Credit)
	}
	if rev.TotalDebit != rev.TotalCredit {
		t.Error("冲销凭证应借贷平衡")
	}

	// 原凭证标记为已冲销，但**仍在账上**
	orig, err := svc.Voucher(ctx, posted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if orig.Status != "voided" {
		t.Errorf("原凭证状态 = %s，期望 voided", orig.Status)
	}
	if orig.VoidedByNo != rev.No {
		t.Errorf("原凭证应指向冲销凭证，实际 %q", orig.VoidedByNo)
	}
	if len(orig.Lines) != 2 {
		t.Error("原凭证的分录必须保留 —— 会计凭证不得删除")
	}
}

// 已冲销的凭证不能再冲销
func TestReverseTwiceFails(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	sh := mustContact(t, svc, "shareholder", "张三")
	in := basicInput()
	in.Lines[1].ContactID = &sh
	d := saveDraft(t, svc, in)
	if _, err := svc.PostVoucher(ctx, d.ID, "王主管"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReverseVoucher(ctx, d.ID, "王主管", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReverseVoucher(ctx, d.ID, "王主管", ""); err == nil {
		t.Fatal("重复冲销应被拒绝")
	}
}

// ---------------------------------------------------------------------------
// 录入辅助
// ---------------------------------------------------------------------------

func TestAccountOptions(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)

	opts, err := svc.AccountOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) == 0 {
		t.Fatal("应返回可记账科目")
	}
	byCode := map[string]service.AccountOption{}
	for _, o := range opts {
		byCode[o.Code] = o
	}
	// 汇总科目不该出现
	if _, ok := byCode["2211"]; ok {
		t.Error("汇总科目不该出现在可记账列表里")
	}
	if _, ok := byCode["2221"]; ok {
		t.Error("汇总科目 2221 不该出现")
	}
	// 明细科目应在，且标注了辅助核算
	ar, ok := byCode["1122"]
	if !ok {
		t.Fatal("应收账款应在可记账列表里")
	}
	if len(ar.AuxTypes) == 0 || ar.AuxTypes[0] != "客户" {
		t.Errorf("应收账款应标注需要客户辅助核算，实际 %v", ar.AuxTypes)
	}
	if ar.SearchText == "" {
		t.Error("应预拼搜索串 —— 让前端每次输入都遍历 190 个科目是浪费")
	}
	if ar.Direction != "借" {
		t.Errorf("应收账款方向 = %s", ar.Direction)
	}
}

func TestContactOptions(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	mustContact(t, svc, "customer", "杭州云帆科技有限公司")
	mustContact(t, svc, "supplier", "宁波恒信办公用品")

	opts, err := svc.ContactOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 2 {
		t.Fatalf("往来单位数 = %d，期望 2", len(opts))
	}
	for _, o := range opts {
		if o.KindLabel == "" {
			t.Errorf("%s 缺少类型中文名", o.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// 列表与查询
// ---------------------------------------------------------------------------

func TestVoucherList(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	sh := mustContact(t, svc, "shareholder", "张三")

	// 三张：两张过账、一张留作草稿
	for i, remark := range []string{"第一笔", "第二笔"} {
		in := basicInput()
		in.Remark = remark
		in.Date = "2025-01-1" + string(rune('0'+i))
		in.Lines[1].ContactID = &sh
		d := saveDraft(t, svc, in)
		if _, err := svc.PostVoucher(ctx, d.ID, "王主管"); err != nil {
			t.Fatal(err)
		}
	}
	draftIn := basicInput()
	draftIn.Remark = "还没想好的第三笔"
	draftIn.Lines[1].ContactID = &sh
	saveDraft(t, svc, draftIn)

	all, err := svc.Vouchers(ctx, service.VoucherQuery{Year: 2025, Month: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("凭证数 = %d，期望 3", len(all))
	}
	// ★ 列表不带分录，但行数要算出来
	for _, v := range all {
		if v.Lines != 2 {
			t.Errorf("凭证 %s 的分录行数 = %d，期望 2", v.No, v.Lines)
		}
		if v.StatusLabel == "" {
			t.Errorf("凭证 %s 缺少状态中文名", v.No)
		}
	}

	// 按状态筛选
	drafts, _ := svc.Vouchers(ctx, service.VoucherQuery{Year: 2025, Month: 1, Status: "draft"})
	if len(drafts) != 1 || drafts[0].Remark != "还没想好的第三笔" {
		t.Errorf("草稿筛选结果不对: %+v", drafts)
	}
	// 关键词
	kw, _ := svc.Vouchers(ctx, service.VoucherQuery{Year: 2025, Month: 1, Keyword: "第二笔"})
	if len(kw) != 1 {
		t.Errorf("关键词筛选结果不对: %+v", kw)
	}
	// 其它期间应为空
	other, _ := svc.Vouchers(ctx, service.VoucherQuery{Year: 2025, Month: 2})
	if len(other) != 0 {
		t.Errorf("2 月不该有凭证，实际 %d 张", len(other))
	}
}

// ---------------------------------------------------------------------------
// 校验按钮
// ---------------------------------------------------------------------------

func TestCheckVoucher(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t, 3)
	sh := mustContact(t, svc, "shareholder", "张三")

	in := basicInput()
	in.Lines[1].ContactID = &sh
	res, err := svc.CheckVoucher(ctx, in)
	if err != nil {
		t.Fatalf("合法凭证应校验通过: %v", err)
	}
	if !res.OK {
		t.Errorf("应报告通过，实际 %q", res.Message)
	}

	// 借贷不平要单独报出来（这是最值得说清的一句话）
	in.Lines[1].Credit = money100(99999)
	res, err = svc.CheckVoucher(ctx, in)
	if err != nil {
		t.Fatalf("借贷不平应作为结论返回而不是错误: %v", err)
	}
	// ★ OK 必须是显式的 false，而不是靠调用方从文案里找「不平衡」三个字
	if res.OK {
		t.Error("借贷不平不该报告为通过")
	}
	if !strings.Contains(res.Message, "不平衡") {
		t.Errorf("应报出借贷不平衡，实际 %q", res.Message)
	}
	if !strings.Contains(res.Message, "差额") {
		t.Errorf("应给出差额，实际 %q", res.Message)
	}
}
