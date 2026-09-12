package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"miniaccount/internal/service"
)

// ★ 附件上传走的是界面点「添加附件」时的**同一条绑定调用序列**。
//
// 界面上做不了自动化点击，但可以把它调用的那几个绑定按同样的顺序
// 和同样的参数形态跑一遍：上传（base64）→ 列附件 → 取磁盘路径 → 移除。
// 这条路一旦断了（改签名、改字段名、base64 解码、ownerType 默认值），
// 测试会先于用户发现。
func TestAttachmentUploadFlowAsGUIUsesIt(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	if _, f := a.CreateBook(service.CreateBookInput{
		CompanyName: "附件测试公司", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	}); f != nil {
		t.Fatalf("建账失败: %v", f)
	}

	// 造一张草稿凭证（界面上传附件时凭证已经存在）
	cust := contactIDOf(t, a)
	saved, f := a.SaveVoucher(VoucherRequest{
		Word: "记", Date: "2025-03-05", Remark: "收到货款", CreatedBy: "李会计",
		Lines: []VoucherLineRequest{
			{AccountCode: "1002", Summary: "收到货款", DebitYuan: "100.00"},
			{AccountCode: "1122", Summary: "收到货款", CreditYuan: "100.00",
				ContactID: &cust},
		},
	})
	if f != nil {
		t.Fatalf("存凭证失败: %v", f)
	}

	// ---- 1. 上传：界面把 File 读成 ArrayBuffer 再 base64 ----
	pdf := []byte("%PDF-1.4\n假的发票文件\n%%EOF\n")
	up, f := a.UploadAttachment(UploadAttachmentRequest{
		VoucherID:  saved.ID,
		FileName:   "增值税专用发票.pdf",
		DataBase64: base64.StdEncoding.EncodeToString(pdf),
	})
	if f != nil {
		t.Fatalf("上传附件失败: %v", f)
	}
	if up.Hash == "" || len(up.Hash) != 64 {
		t.Errorf("附件 hash = %q，期望 64 位 sha256", up.Hash)
	}
	if up.Name != "增值税专用发票.pdf" {
		t.Errorf("附件名 = %q", up.Name)
	}
	if up.Size != int64(len(pdf)) {
		t.Errorf("附件大小 = %d，期望 %d", up.Size, len(pdf))
	}

	// ---- 2. 列附件：界面每次打开凭证都会调 ----
	list, f := a.Attachments(AttachmentQueryRequest{
		OwnerType: "voucher", OwnerID: saved.ID,
	})
	if f != nil {
		t.Fatalf("列附件失败: %v", f)
	}
	if len(list) != 1 {
		t.Fatalf("附件数 = %d，期望 1", len(list))
	}
	if list[0].Hash != up.Hash {
		t.Errorf("列出的 hash = %q，期望 %q", list[0].Hash, up.Hash)
	}
	// 磁盘上原件必须真的能读到，且内容一致
	diskPath, f := a.AttachmentPath(up.Hash)
	if f != nil {
		t.Fatalf("取附件路径失败: %v", f)
	}
	got, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("附件原件读不到（%s）：%v", diskPath, err)
	}
	if string(got) != string(pdf) {
		t.Errorf("附件内容与上传的不一致")
	}
	// 内容寻址：文件名在 .files/<前两位>/<sha256> 下
	if filepath.Base(diskPath) != up.Hash {
		t.Errorf("磁盘文件名 = %q，期望就是 sha256（内容寻址）", filepath.Base(diskPath))
	}
	if filepath.Base(filepath.Dir(diskPath)) != up.Hash[:2] {
		t.Errorf("上级目录 = %q，期望 hash 前两位 %q",
			filepath.Base(filepath.Dir(diskPath)), up.Hash[:2])
	}

	// ---- 3. 同一份文件再传一次：内容寻址应去重，不占两份空间 ----
	up2, f := a.UploadAttachment(UploadAttachmentRequest{
		VoucherID:  saved.ID,
		FileName:   "同一个发票改个名.pdf",
		DataBase64: base64.StdEncoding.EncodeToString(pdf),
	})
	if f != nil {
		t.Fatalf("二次上传失败: %v", f)
	}
	if up2.Hash != up.Hash {
		t.Errorf("同一内容应得到同一 hash：%q vs %q", up2.Hash, up.Hash)
	}

	// ---- 4. 移除关联：只断开关系，不删磁盘文件 ----
	if f := a.RemoveAttachment(RemoveAttachmentRequest{
		OwnerType: "voucher", OwnerID: saved.ID, Hash: up.Hash,
	}); f != nil {
		t.Fatalf("移除附件失败: %v", f)
	}
	after, f := a.Attachments(AttachmentQueryRequest{
		OwnerType: "voucher", OwnerID: saved.ID,
	})
	if f != nil {
		t.Fatal(f)
	}
	if len(after) != 0 {
		t.Errorf("移除后附件数 = %d，期望 0", len(after))
	}
	// 原件还在 —— 界面的提示就是这么说的（可在设置里清理孤儿）
	orphans, f := a.OrphanAttachments()
	if f != nil {
		t.Fatalf("取孤儿附件失败: %v", f)
	}
	if len(orphans) == 0 {
		t.Error("移除关联后磁盘文件应仍在，并出现在孤儿列表里")
	}

	// ---- 5. 体积审计：设置页那四个数字 ----
	audit, f := a.AuditAttachments(2025, 3)
	if f != nil {
		t.Fatalf("附件审计失败: %v", f)
	}
	if audit.OrphanFiles == 0 {
		t.Error("审计里孤儿文件数应大于 0")
	}
}

// ownerType 留空时默认按凭证处理 —— 界面有几处不传这个字段。
func TestAttachmentOwnerTypeDefaultsToVoucher(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	if _, f := a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	}); f != nil {
		t.Fatal(f)
	}
	// 空 ownerType 不该报错，只返回空列表
	list, f := a.Attachments(AttachmentQueryRequest{OwnerID: 1})
	if f != nil {
		t.Fatalf("ownerType 留空应默认为 voucher，实际报错: %v", f)
	}
	if len(list) != 0 {
		t.Errorf("不该有附件，实际 %d 个", len(list))
	}
}

// 非法 base64 要给出可识别的错误，而不是写进一个坏文件。
func TestUploadAttachmentRejectsBadBase64(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	if _, f := a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	}); f != nil {
		t.Fatal(f)
	}
	_, f := a.UploadAttachment(UploadAttachmentRequest{
		VoucherID: 1, FileName: "x.pdf", DataBase64: "这不是 base64！！！",
	})
	if f == nil {
		t.Fatal("非法 base64 应报错")
	}
	// 绑定返回的是 error 接口，要经 faultOf 才能拿到类型化的 Fault
	if fo := faultOf[any](nil, f); fo == nil || fo.Kind != FaultInvalid {
		t.Errorf("错误类型 = %v，期望 FaultInvalid", f)
	}
}

// data: URL 前缀要能剥掉 —— 前端有时直接传 FileReader 的结果。
func TestUploadAttachmentAcceptsDataURLPrefix(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	if _, f := a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	}); f != nil {
		t.Fatal(f)
	}
	cid := contactIDOf(t, a)
	saved, f := a.SaveVoucher(VoucherRequest{
		Word: "记", Date: "2025-03-05", Remark: "x", CreatedBy: "李会计",
		Lines: []VoucherLineRequest{
			{AccountCode: "1002", Summary: "x", DebitYuan: "1.00"},
			{AccountCode: "1122", Summary: "x", CreditYuan: "1.00",
				ContactID: &cid},
		},
	})
	if f != nil {
		t.Fatal(f)
	}
	raw := base64.StdEncoding.EncodeToString([]byte("hello"))
	up, f := a.UploadAttachment(UploadAttachmentRequest{
		VoucherID:  saved.ID,
		FileName:   "x.txt",
		DataBase64: "data:application/pdf;base64," + raw,
	})
	if f != nil {
		t.Fatalf("带 data: 前缀的上传失败: %v", f)
	}
	p, _ := a.AttachmentPath(up.Hash)
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("内容 = %q，期望 hello（前缀没剥干净）", got)
	}
}

// contactIDOf 取一个客户往来单位 id。
func contactIDOf(t *testing.T, a *App) int64 {
	t.Helper()
	id, f := a.SaveContact(ContactRequest{Kind: "customer", Name: "测试客户"})
	if f != nil {
		t.Fatalf("建往来单位失败: %v", f)
	}
	return id
}

// ★ 明细账要带出「对方科目」—— 日记账靠它回答「这笔钱从哪来、到哪去」。
//
// 这条走的是界面 Ledger 页调用的同一个绑定。
func TestLedgerDetailCarriesContraAccount(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	if _, f := a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	}); f != nil {
		t.Fatal(f)
	}
	cid := contactIDOf(t, a)
	saved, f := a.SaveVoucher(VoucherRequest{
		Word: "记", Date: "2025-03-05", Remark: "收到货款", CreatedBy: "李会计",
		Lines: []VoucherLineRequest{
			{AccountCode: "1002", Summary: "收到货款", DebitYuan: "1060.00"},
			{AccountCode: "1122", Summary: "收到货款", CreditYuan: "1000.00", ContactID: &cid},
			{AccountCode: "5001", Summary: "确认收入", CreditYuan: "60.00"},
		},
	})
	if f != nil {
		t.Fatalf("存凭证失败: %v", f)
	}
	if _, f := a.PostVoucher(saved.ID, "王主管"); f != nil {
		t.Fatalf("过账失败: %v", f)
	}

	r, f := a.LedgerDetail(LedgerRequest{AccountPrefix: "1002"})
	if f != nil {
		t.Fatalf("取明细账失败: %v", f)
	}
	if len(r.Rows) != 1 {
		t.Fatalf("明细行数 = %d，期望 1", len(r.Rows))
	}
	row := r.Rows[0]
	if row.Contra == "" {
		t.Fatal("对方科目为空 —— 日记账没有这一列等于没做")
	}
	// 两个对方科目都要在：应收账款、主营业务收入
	for _, want := range []string{"应收账款", "主营业务收入"} {
		if !strings.Contains(row.Contra, want) {
			t.Errorf("对方科目 = %q，应含 %q", row.Contra, want)
		}
	}
	// 顺序稳定：按科目编码排（1122 在 5001 之前）
	if !strings.HasPrefix(row.Contra, "应收账款") {
		t.Errorf("对方科目 = %q，应按科目编码排（1122 在前）", row.Contra)
	}
}

// ★ 界面能传任意字符串进来的入口必须挡住越界路径。
//
// AttachmentPath 的返回值会被拿去用系统程序打开 ——
// 不校验 hash 就等于给了界面一个「打开任意文件」的能力。
// 修之前 `../../../../etc/passwd` 会被原样返回。
func TestAttachmentPathRejectsBadHash(t *testing.T) {
	a, path := newApp(t)
	a.OpenBook(path)
	if _, f := a.CreateBook(service.CreateBookInput{
		CompanyName: "X", StartYear: 2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	}); f != nil {
		t.Fatal(f)
	}
	filesDir, f := a.FilesDir()
	if f != nil {
		t.Fatalf("取附件目录失败: %v", f)
	}

	bad := []string{
		"../../../../../../../../etc/passwd",
		"../../x", "..", ".", "aa/../../../x",
		"", "abc", // 长度不对
		strings.Repeat("z", 64),                  // 64 位但非十六进制
		strings.ToUpper(strings.Repeat("a", 64)), // 大写也不收
	}
	for _, h := range bad {
		got, err := a.AttachmentPath(h)
		if err == nil {
			rel, relErr := filepath.Rel(filesDir, got)
			escaped := relErr != nil || rel == ".." ||
				strings.HasPrefix(rel, ".."+string(filepath.Separator))
			if escaped {
				t.Errorf("★ hash=%q → %s 越出了附件目录", h, got)
			} else {
				t.Errorf("hash=%q 应被拒绝，却返回了 %s", h, got)
			}
			continue
		}
		if fo := faultOf[any](nil, err); fo == nil || fo.Kind != FaultInvalid {
			t.Errorf("hash=%q 的错误类型 = %v，期望 FaultInvalid", h, err)
		}
	}

	// 合法 hash 仍然要能取到路径
	good := strings.Repeat("a", 64)
	got, err := a.AttachmentPath(good)
	if err != nil {
		t.Fatalf("合法 hash 不该报错: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("aa", good)) {
		t.Errorf("合法 hash 的路径 = %q，期望以 aa/<hash> 结尾", got)
	}
}
