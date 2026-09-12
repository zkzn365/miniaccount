package main

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/service"
)

// ---------------------------------------------------------------------------
// 录入界面走的是这一层，因此这里要覆盖「元 → 分」的换算
// ---------------------------------------------------------------------------

func bookWithAccounts(t *testing.T) *App {
	t.Helper()
	a, path := newApp(t)
	if _, e := a.OpenBook(path); e != nil {
		t.Fatal(e)
	}
	if _, e := a.CreateBook(service.CreateBookInput{
		CompanyName: "录入测试公司", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	}); e != nil {
		t.Fatal(e)
	}
	return a
}

func voucherReq() VoucherRequest {
	return VoucherRequest{
		Word: "记", Date: "2025-01-15", Remark: "收到股东投资款",
		CreatedBy: "李会计", PostedBy: "王主管",
		Lines: []VoucherLineRequest{
			{AccountCode: "1002", Summary: "收到投资款", DebitYuan: "100000"},
			{AccountCode: "3001", Summary: "收到投资款", CreditYuan: "100,000.00"},
		},
	}
}

// ★ 金额从界面传过来是「元」的字符串，必须精确换算成分。
//
// 界面绝不能自己算「分」：JS 的 number 是 IEEE754 双精度，
// 一旦经过任何浮点运算就可能差一分 —— 而账上差一分，试算平衡就不成立。
func TestVoucherYuanToCents(t *testing.T) {
	a := bookWithAccounts(t)

	// 3001 实收资本要求股东辅助核算。金额填对、辅助核算留空 ——
	// 这是录入时最常见的漏填，必须被明确拦下。
	req := voucherReq()
	_, err := a.SaveVoucher(req)
	if err == nil {
		t.Fatal("缺股东辅助核算应被拒绝")
	}
	f := faultOf[any](nil, err)
	if f == nil || f.Kind != FaultInvalid {
		t.Fatalf("应报 FaultInvalid，实际 %v", err)
	}
	if !strings.Contains(f.Message, "股东") {
		t.Errorf("错误信息应点明缺的维度，实际 %q", f.Message)
	}
}

// 小数金额必须精确到分
func TestVoucherDecimalAmounts(t *testing.T) {
	a := bookWithAccounts(t)

	req := voucherReq()
	req.Lines = []VoucherLineRequest{
		{AccountCode: "1002", Summary: "付手续费", DebitYuan: "0.01"},
		{AccountCode: "560303", Summary: "付手续费", CreditYuan: "0.01"},
	}
	d, err := a.SaveVoucher(req)
	if f := faultOf(d, err); f != nil {
		t.Fatalf("保存失败: %v", f)
	}
	if d.TotalDebit != money.Money(1) {
		t.Errorf("借方 = %d 分，期望 1 分（0.01 元）", d.TotalDebit)
	}
	if !d.Balanced {
		t.Error("应借贷平衡")
	}
}

// 带千分位与全角字符的金额（用户从 Excel 粘过来）
func TestVoucherFormattedAmounts(t *testing.T) {
	a := bookWithAccounts(t)
	shareholder := mustContactDesktop(t, a, "shareholder", "张三")

	req := voucherReq()
	req.Lines[1].CreditYuan = "　１００,０００.00　" // 全角空格
	req.Lines[1].ContactID = &shareholder

	// 全角数字不是合法金额，应被明确拒绝而不是静默当成 0
	_, err := a.SaveVoucher(req)
	if err == nil {
		t.Fatal("全角数字应被拒绝")
	}

	// 半角千分位是合法的
	req.Lines[1].CreditYuan = " 100,000.00 "
	d, e2 := a.SaveVoucher(req)
	if f := faultOf(d, e2); f != nil {
		t.Fatalf("带千分位的金额应被接受: %v", f)
	}
	if d.TotalCredit != money.Money(10000000) {
		t.Errorf("贷方 = %s，期望 100,000.00", d.TotalCredit)
	}
}

// 非法金额要在**保存之前**就被挡住
func TestVoucherRejectsBadAmount(t *testing.T) {
	a := bookWithAccounts(t)
	for _, bad := range []string{"1.234", "abc", "0x10", "1e3"} {
		req := voucherReq()
		req.Lines[0].DebitYuan = bad
		_, err := a.SaveVoucher(req)
		if err == nil {
			t.Errorf("金额 %q 应被拒绝", bad)
			continue
		}
		f := faultOf[any](nil, err)
		if f == nil || f.Kind != FaultInvalid {
			t.Errorf("金额 %q 应报 FaultInvalid，实际 %v", bad, err)
		}
		// 错误信息要指到具体哪一行
		if !strings.Contains(f.Message, "第 1 行") {
			t.Errorf("错误信息应指明行号，实际 %q", f.Message)
		}
	}
}

// ---------------------------------------------------------------------------
// 完整的录入 → 过账 → 冲销
// ---------------------------------------------------------------------------

func TestVoucherLifecycle(t *testing.T) {
	a := bookWithAccounts(t)
	shareholder := mustContactDesktop(t, a, "shareholder", "张三")

	req := voucherReq()
	req.Lines[1].ContactID = &shareholder

	// 存草稿
	d, err := a.SaveVoucher(req)
	if f := faultOf(d, err); f != nil {
		t.Fatalf("保存失败: %v", f)
	}
	if d.Status != "draft" || d.No != "" {
		t.Errorf("草稿不该有凭证号: status=%s no=%q", d.Status, d.No)
	}
	if !d.CanPost || !d.CanEdit {
		t.Error("草稿应可过账可编辑")
	}

	// 列表里能看到
	list, lerr := a.Vouchers(VoucherQueryRequest{Year: 2025, Month: 1})
	if f := faultOf(list, lerr); f != nil {
		t.Fatal(f)
	}
	if len(list) != 1 || list[0].Status != "draft" {
		t.Fatalf("列表结果不对: %+v", list)
	}
	if list[0].Lines != 2 {
		t.Errorf("列表应带回分录行数，实际 %d", list[0].Lines)
	}

	// ★ 界面层没有任何「把一张凭证直接过账」的绑定。
	//
	// 这是本工程的硬规则：凭证录完只落草稿，过账只发生在账期结算。
	// 用反射守着它，比在注释里写一句「不要加回来」有用 ——
	// 加回来的时候这条测试会红。
	for _, name := range []string{"SaveAndPost", "PostVoucher"} {
		if m := reflect.ValueOf(a).MethodByName(name); m.IsValid() {
			t.Errorf("★ 绑定 %s 又回来了。凭证录完不能直接过账 —— "+
				"过账只发生在账期结算（CloseBook 内部先过账本期草稿）。", name)
		}
	}

	// 结算（界面上是结账）之后才过账
	postPeriodDrafts(t, a, 2025, 1)
	posted, perr := a.VoucherDetail(d.ID)
	if f := faultOf(posted, perr); f != nil {
		t.Fatalf("读凭证失败: %v", f)
	}
	if posted.Status != "posted" || posted.No == "" {
		t.Errorf("结算后应有号: %+v", posted)
	}
	if posted.CanEdit {
		t.Error("已过账不该可编辑")
	}
	if posted.BlockedReason == "" {
		t.Error("应说明为什么不能改")
	}

	// 红字冲销
	rev, rerr := a.ReverseVoucher(ReverseVoucherRequest{ID: d.ID, By: "王主管"})
	if f := faultOf(rev, rerr); f != nil {
		t.Fatalf("红字冲销失败: %v", f)
	}
	if rev.ReversesNo != posted.No {
		t.Errorf("冲销凭证应记录原凭证号，实际 %q", rev.ReversesNo)
	}
	if !rev.Balanced {
		t.Error("冲销凭证应借贷平衡")
	}

	// 原凭证仍在账上，只是标记为已冲销
	orig, _ := a.VoucherDetail(d.ID)
	if orig.Status != "voided" {
		t.Errorf("原凭证状态 = %s，期望 voided", orig.Status)
	}
	if len(orig.Lines) != 2 {
		t.Error("原凭证的分录必须保留 —— 会计凭证不得删除")
	}
	if orig.VoidedByNo != rev.No {
		t.Errorf("原凭证应指向冲销凭证，实际 %q", orig.VoidedByNo)
	}
}

// ★ 存凭证只会存下**一张草稿**：不占号、不进总账。
//
// 原来这里测的是「保存并记账一步完成」。那个绑定已经删掉了 ——
// 它让「过账只在结算时」名存实亡。现在要守的是反面：
// 存完就是草稿，而且账上不许多出任何东西。
func TestSaveVoucherOnlyCreatesDraft(t *testing.T) {
	a := bookWithAccounts(t)
	sh := mustContactDesktop(t, a, "shareholder", "张三")

	req := voucherReq()
	req.Lines[1].ContactID = &sh

	d, err := a.SaveVoucher(req)
	if f := faultOf(d, err); f != nil {
		t.Fatalf("存凭证失败: %v", f)
	}
	if d.Status != "draft" {
		t.Errorf("状态 = %s，期望 draft", d.Status)
	}
	if d.No != "" {
		t.Errorf("★ 草稿不该有凭证号，实际 %q", d.No)
	}
	if d.PostedBy != "" {
		t.Errorf("★ 还没过账就不该有记账签章，实际 %q", d.PostedBy)
	}

	list, _ := a.Vouchers(VoucherQueryRequest{Year: 2025, Month: 1})
	if len(list) != 1 {
		t.Fatalf("账上只应有一张凭证，实际 %d 张", len(list))
	}

	// 总账里必须还是空的 —— 这正是「录完凭证不算记账」
	led, _ := a.LedgerDetail(LedgerRequest{AccountPrefix: "1002"})
	if led != nil && len(led.Rows) != 0 {
		t.Errorf("★ 还没结算，明细账就该是空的，实际 %d 行", len(led.Rows))
	}
}

// ---------------------------------------------------------------------------
// 科目与辅助核算
// ---------------------------------------------------------------------------

func TestVoucherMetaInfo(t *testing.T) {
	a := bookWithAccounts(t)
	mustContactDesktop(t, a, "customer", "杭州云帆科技有限公司")

	meta, err := a.VoucherMetaInfo()
	if f := faultOf(meta, err); f != nil {
		t.Fatal(f)
	}
	if len(meta.Accounts) == 0 {
		t.Fatal("应返回可记账科目")
	}
	if len(meta.Contacts) != 1 {
		t.Errorf("往来单位数 = %d", len(meta.Contacts))
	}
	if len(meta.Words) != 4 {
		t.Errorf("凭证字应有 4 个，实际 %v", meta.Words)
	}
	if meta.Today == "" {
		t.Error("应返回今天的日期")
	}
	if meta.CurrentPeriod != "2025-01" {
		t.Errorf("当前期间 = %s，期望 2025-01", meta.CurrentPeriod)
	}

	// 汇总科目不能出现在可记账列表里
	for _, ac := range meta.Accounts {
		if ac.Code == "2211" {
			t.Error("汇总科目 2211 不该出现在可记账列表里")
		}
	}
	// 明细科目要带辅助核算要求
	for _, ac := range meta.Accounts {
		if ac.Code == "1122" {
			if len(ac.AuxTypes) == 0 || ac.AuxTypes[0] != "客户" {
				t.Errorf("应收账款应标注需要客户，实际 %v", ac.AuxTypes)
			}
		}
	}
}

// 需要往来辅助核算的科目，不填要当场拦下
func TestAuxRequiredOnSave(t *testing.T) {
	a := bookWithAccounts(t)

	req := voucherReq()
	req.Lines = []VoucherLineRequest{
		{AccountCode: "1122", Summary: "销售", DebitYuan: "1130"},
		{AccountCode: "5001", Summary: "销售", CreditYuan: "1000"},
		{AccountCode: "22210102", Summary: "销项税", CreditYuan: "130"},
	}
	_, err := a.SaveVoucher(req)
	if err == nil {
		t.Fatal("应收账款缺客户辅助核算应被拒绝")
	}
	f := faultOf[any](nil, err)
	if !strings.Contains(f.Message, "客户") {
		t.Errorf("应点明缺少「客户」，实际 %q", f.Message)
	}
}

// ---------------------------------------------------------------------------
// 校验按钮
// ---------------------------------------------------------------------------

func TestCheckVoucherBinding(t *testing.T) {
	a := bookWithAccounts(t)

	req := voucherReq()
	req.Lines[0].DebitYuan = "1000"
	req.Lines[1].CreditYuan = "900"
	r, err := a.CheckVoucher(req)
	if f := faultOf(r, err); f != nil {
		t.Fatal(f)
	}
	if r.OK {
		t.Error("借贷不平不该报告为通过")
	}
	// ★ 不平衡是「结论」而不是「错误」：界面要显示在平衡提示条上
	if !strings.Contains(r.Message, "不平衡") || !strings.Contains(r.Message, "差额") {
		t.Errorf("结论应说明不平衡与差额，实际 %q", r.Message)
	}
}

// ---------------------------------------------------------------------------
// 附件
// ---------------------------------------------------------------------------

func TestUploadAttachment(t *testing.T) {
	a := bookWithAccounts(t)
	sh := mustContactDesktop(t, a, "shareholder", "张三")
	req := voucherReq()
	req.Lines[1].ContactID = &sh
	d, _ := a.SaveVoucher(req)

	pdf := []byte("%PDF-1.4\n假装这是一张发票\n")
	info, err := a.UploadAttachment(UploadAttachmentRequest{
		VoucherID:  d.ID,
		FileName:   "发票.pdf",
		DataBase64: base64.StdEncoding.EncodeToString(pdf),
	})
	if f := faultOf(info, err); f != nil {
		t.Fatalf("上传附件失败: %v", f)
	}
	if info.Hash == "" || len(info.Hash) != 64 {
		t.Errorf("应返回 sha256 作为内容指纹，实际 %q", info.Hash)
	}
	if info.Path == "" {
		t.Error("应返回附件的磁盘路径")
	}
	// 附单据数要跟着涨
	after, _ := a.VoucherDetail(d.ID)
	if after.AttachCount != 1 {
		t.Errorf("附单据数 = %d，期望 1", after.AttachCount)
	}

	// data: URL 前缀要能识别
	info2, err := a.UploadAttachment(UploadAttachmentRequest{
		VoucherID:  d.ID,
		FileName:   "发票2.pdf",
		DataBase64: "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(pdf),
	})
	if f := faultOf(info2, err); f != nil {
		t.Fatalf("带 data: 前缀的附件应被接受: %v", f)
	}
	// ★ 内容寻址：同样的内容只存一份
	if info2.Hash != info.Hash {
		t.Error("相同内容应得到相同的哈希（内容寻址天然去重）")
	}

	// 非法 base64 要明确报错
	if _, e := a.UploadAttachment(UploadAttachmentRequest{
		VoucherID: d.ID, FileName: "x.pdf", DataBase64: "这不是 base64!!",
	}); e == nil {
		t.Error("非法 base64 应被拒绝")
	}
}

// ---------------------------------------------------------------------------
// 绑定契约（补：凭证相关方法）
// ---------------------------------------------------------------------------

func TestVoucherBindingsReturnNilInterface(t *testing.T) {
	a := bookWithAccounts(t)
	sh := mustContactDesktop(t, a, "shareholder", "张三")
	req := voucherReq()
	req.Lines[1].ContactID = &sh
	d, _ := a.SaveVoucher(req)

	type pair struct {
		name string
		e    error
	}
	var checks []pair
	add := func(n string, e error) { checks = append(checks, pair{n, e}) }

	_, e := a.Vouchers(VoucherQueryRequest{Year: 2025, Month: 1})
	add("Vouchers", e)
	_, e = a.VoucherDetail(d.ID)
	add("VoucherDetail", e)
	_, e = a.VoucherMetaInfo()
	add("VoucherMetaInfo", e)
	_, e = a.AccountOptions()
	add("AccountOptions", e)
	_, e = a.ContactOptions()
	add("ContactOptions", e)
	_, e = a.CheckVoucher(req)
	add("CheckVoucher", e)
	_, e = a.PreviewClose(PeriodRequest{Year: 2025, Month: 1})
	add("PreviewClose", e)

	for _, c := range checks {
		if c.e != nil {
			t.Errorf("%s 成功时第二个返回值不是 nil 接口（实际 %#v）—— "+
				"Wails 会把它当成失败并 nil 解引用崩溃", c.name, c.e)
		}
	}
}

// mustContactDesktop 新增往来单位。
func mustContactDesktop(t *testing.T, a *App, kind, name string) int64 {
	t.Helper()
	svc := a.svc
	if svc == nil {
		t.Fatal("账套未打开")
	}
	id, err := svc.DB().Contacts().AddContact(a.context(), kind, name)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

var _ = errors.Is

// postPeriodDrafts 把某期的草稿全部过账（测试用）。
//
// ★ 界面上没有这个动作：过账只发生在账期结算，走 CloseBook。
// 但很多测试要的是「账上有一张已过账的凭证」，而不是「把这一期结掉」——
// 走 CloseBook 会顺带结转损益、关期间，把测试的前提搞乱。
func postPeriodDrafts(t *testing.T, a *App, year, month int) {
	t.Helper()
	svc, f := a.book()
	if f != nil {
		t.Fatalf("账套不可用: %v", f)
	}
	if _, err := svc.PostPeriodDrafts(a.context(),
		period.NewKey(year, month), "王主管"); err != nil {
		t.Fatalf("过账 %d-%02d 的草稿失败: %v", year, month, err)
	}
}
