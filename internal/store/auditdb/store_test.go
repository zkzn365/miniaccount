package auditdb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/domain/audit"
)

func newStore(t *testing.T, maxBytes int64) *Store {
	t.Helper()
	st, err := Open(context.Background(), Options{
		Dir: t.TempDir(), MaxBytes: maxBytes,
	})
	if err != nil {
		t.Fatalf("打开日志存储失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func entry(op, action string) audit.Entry {
	return audit.Entry{
		At: audit.NowStamp(time.Now()), Operator: op,
		Action: audit.Action(action), Summary: "测试操作 " + action,
		Entity: "voucher", EntityID: "1", Source: audit.SourceGUI,
		Result: audit.ResultOK, Book: "/tmp/a.db", Company: "测试公司",
		AppVersion: "0.2.0",
		Detail:     map[string]any{"金额": "100.00"},
	}
}

// 追加的日志要带上序号与链哈希，且前后相接。
func TestAppendChainsEntries(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, 0)

	a, err := st.Append(ctx, entry("张三", "voucher.create"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Append(ctx, entry("李四", "voucher.post"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Seq != 1 || b.Seq != 2 {
		t.Errorf("序号 = %d, %d，期望 1, 2", a.Seq, b.Seq)
	}
	if a.PrevHash != audit.Genesis() {
		t.Errorf("第一条的 prev_hash 应当是 genesis，实际 %s", a.PrevHash)
	}
	if b.PrevHash != a.Hash {
		t.Error("★ 第二条没有接在第一条后面，链断了")
	}
	if a.Hash == "" || len(a.Hash) != 64 {
		t.Errorf("哈希应当是 sha256：%q", a.Hash)
	}

	// 同样的内容 + 同样的前驱 → 同样的哈希（可重复验证的前提）
	same := entry("张三", "voucher.create")
	same.Seq = a.Seq
	same.At = a.At
	same.Detail = a.Detail
	// 类别在写入时会被补全（空串 → business），重算时要一致
	same.Category = a.Category
	// Detail 是 map，重新构造后键序不同也要算出同一个哈希
	if same.ComputeHash(audit.Genesis()) != a.Hash {
		t.Error("★ 同一份内容算出了不同的哈希 —— Detail 的序列化不稳定")
	}
}

// ★ 日志不可修改、不可删除 —— 表级触发器直接拒绝。
func TestLogIsAppendOnly(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, 0)
	if _, err := st.Append(ctx, entry("张三", "voucher.create")); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", "file:"+st.file+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(ctx,
		`UPDATE audit_log SET summary = '改过了' WHERE seq = 1`); err == nil {
		t.Error("★ 日志被允许修改")
	} else if !strings.Contains(err.Error(), "不可修改") {
		t.Errorf("拒绝的理由应当是「不可修改」，实际：%v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM audit_log WHERE seq = 1`); err == nil {
		t.Error("★ 日志被允许删除")
	} else if !strings.Contains(err.Error(), "不可删除") {
		t.Errorf("拒绝的理由应当是「不可删除」，实际：%v", err)
	}
}

// 校验：完好的链通过。
func TestVerifyClean(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, 0)
	for i := 0; i < 20; i++ {
		if _, err := st.Append(ctx, entry("张三", "voucher.create")); err != nil {
			t.Fatal(err)
		}
	}
	res, err := st.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("完好的链却报不完好：%+v", res.Issues)
	}
	if res.Checked != 20 {
		t.Errorf("校验条数 = %d，期望 20", res.Checked)
	}
	if res.Head == "" || res.Head == audit.Genesis() {
		t.Error("链头应当是最后一条的哈希")
	}
}

// ★ 绕过触发器直接改库（拿到文件的人能做的事）必须被链发现。
//
// 这是「安全性」那一半的真正兜底：触发器挡得住顺手改，
// 挡不住直接用 sqlite3 改字节 —— 那种改动由哈希链证明。
func TestVerifyDetectsTampering(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, 0)
	for i := 0; i < 5; i++ {
		if _, err := st.Append(ctx, entry("张三", "voucher.create")); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()

	// 直接改库：先删掉触发器（模拟有文件访问权的人），再改内容
	db, err := sql.Open("sqlite", "file:"+st.file)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TRIGGER audit_log_no_update`,
		`UPDATE audit_log SET summary = '被改过的摘要' WHERE seq = 3`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	st2, err := Open(ctx, Options{Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	res, err := st2.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("★ 内容被改过，校验却说完好 —— 哈希链没起作用")
	}
	found := false
	for _, iss := range res.Issues {
		if iss.Seq == 3 && strings.Contains(iss.Reason, "被改过") {
			found = true
		}
	}
	if !found {
		t.Errorf("没有指出被改的是第 3 条：%+v", res.Issues)
	}
}

// ★ 删掉一整个分片文件也要被发现。
func TestVerifyDetectsMissingSegment(t *testing.T) {
	ctx := context.Background()
	// 写到超过下限就会分片
	st := newStore(t, MinMaxBytes)
	for i := 0; i < 60; i++ {
		if _, err := st.Append(ctx, entry("张三", "voucher.create")); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := st.files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 3 {
		t.Skipf("只分出 %d 片，测不出「整片被删」", len(files))
	}
	// 删掉中间那一整片
	if err := os.Remove(files[1]); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(ctx, Options{Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	res, err := st2.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("★ 中间一整片日志被删掉，校验却说完好")
	}
	joined := ""
	for _, iss := range res.Issues {
		joined += iss.Reason + " "
	}
	if !strings.Contains(joined, "接不上") {
		t.Errorf("没有指出分片断了：%s", joined)
	}
}

// 超过上限就分片，且链跨片接续。
func TestRotationKeepsChain(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, MinMaxBytes)
	var prev string
	// 4MB 上限下要写足够多条才会分片
	for i := 0; i < 20000; i++ {
		e, err := st.Append(ctx, entry("张三", "voucher.create"))
		if err != nil {
			t.Fatal(err)
		}
		if prev != "" && e.PrevHash != prev {
			t.Fatalf("第 %d 条链断了：prev=%s 期望 %s", e.Seq, e.PrevHash, prev)
		}
		prev = e.Hash
	}
	files, err := st.files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Fatalf("写到超过 4MB 上限却没分片：%v", files)
	}
	// 每个文件都不超过上限（允许 SQLite 页对齐带来的一点余量）
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > MinMaxBytes+64*1024 {
			t.Errorf("%s 超过上限：%d 字节", filepath.Base(f), fi.Size())
		}
	}
	res, err := st.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("跨片之后链应当仍然完好：%+v", res.Issues)
	}
	if res.Files != len(files) {
		t.Errorf("校验的文件数 = %d，期望 %d", res.Files, len(files))
	}
}

// 查询：按人、时间、操作、结果、关键字组合过滤。
func TestQueryFilters(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, 0)
	_, _ = st.Append(ctx, entry("张三", "voucher.create"))
	_, _ = st.Append(ctx, entry("李四", "voucher.post"))
	_, _ = st.Append(ctx, entry("张三", "period.close"))

	byOp, err := st.Query(ctx, audit.Query{Operator: "张三"})
	if err != nil {
		t.Fatal(err)
	}
	if byOp.Total != 2 {
		t.Errorf("按操作人查 = %d 条，期望 2", byOp.Total)
	}
	byAction, err := st.Query(ctx, audit.Query{Actions: []audit.Action{"voucher.post"}})
	if err != nil {
		t.Fatal(err)
	}
	if byAction.Total != 1 || byAction.Entries[0].Operator != "李四" {
		t.Errorf("按操作种类查不对：%+v", byAction.Entries)
	}
	byText, err := st.Query(ctx, audit.Query{Text: "period.close"})
	if err != nil {
		t.Fatal(err)
	}
	if byText.Total != 1 {
		t.Errorf("按关键字查 = %d 条，期望 1", byText.Total)
	}
	// 组合：操作人 + 操作种类
	combo, err := st.Query(ctx, audit.Query{
		Operator: "张三", Actions: []audit.Action{"period.close"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if combo.Total != 1 {
		t.Errorf("组合条件查 = %d 条，期望 1", combo.Total)
	}
	// 分页：最新的在前
	page, err := st.Query(ctx, audit.Query{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Seq != 3 {
		t.Errorf("第一页应当是最后一条：%+v", page.Entries)
	}
	if !page.Truncated {
		t.Error("被截断时应当标出来")
	}
	if page.Segments != 1 || page.Bytes <= 0 {
		t.Errorf("应当报告分片数与占用：%+v", page)
	}
}

// 只读模式不能写。
func TestReadOnlyRefusesAppend(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, 0)
	if _, err := st.Append(ctx, entry("张三", "voucher.create")); err != nil {
		t.Fatal(err)
	}
	dir := st.dir
	_ = st.Close()

	ro, err := Open(ctx, Options{Dir: dir, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	if _, err := ro.Append(ctx, entry("李四", "voucher.post")); err == nil {
		t.Error("★ 只读模式却写进去了")
	}
	page, err := ro.Query(ctx, audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Errorf("只读模式应当读得到：%d", page.Total)
	}
}

// ★ 日志目录与文件必须只有本人可访问。
//
// 日志里有操作人姓名、业务摘要、金额、对方户名 —— 同一台电脑多人共用时，
// 一个 0755 的目录等于把这些摊开给所有人看。
func TestPermsAreTightened(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上 os.Chmod 基本是空操作")
	}
	ctx := context.Background()
	dir := t.TempDir()
	logs := filepath.Join(dir, "logs")

	st, err := Open(ctx, Options{Dir: logs})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.Append(ctx, entry("张三", "voucher.create")); err != nil {
		t.Fatal(err)
	}

	if perm := dirPermOf(t, logs); perm != 0o700 {
		t.Errorf("★ 日志目录权限 = %04o，期望 0700", perm)
	}
	files, err := st.files()
	if err != nil || len(files) == 0 {
		t.Fatalf("没有日志文件：%v", err)
	}
	for _, f := range files {
		if perm := dirPermOf(t, f); perm != 0o600 {
			t.Errorf("★ %s 权限 = %04o，期望 0600", filepath.Base(f), perm)
		}
	}
}

// 早先建的、或被改宽过的文件，再次打开时要被收紧回来。
func TestPermsAreRepairedOnOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上 os.Chmod 基本是空操作")
	}
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(ctx, Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append(ctx, entry("张三", "voucher.create")); err != nil {
		t.Fatal(err)
	}
	files, _ := st.files()
	_ = st.Close()

	// 模拟「用户自己放宽了权限」或「早先版本建的宽权限文件」
	if err := os.Chmod(files[0], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(ctx, Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	if perm := dirPermOf(t, dir); perm != 0o700 {
		t.Errorf("★ 目录权限没有被修正：%04o", perm)
	}
	if perm := dirPermOf(t, files[0]); perm != 0o600 {
		t.Errorf("★ 文件权限没有被修正：%04o", perm)
	}
	if st2.PermError() != nil {
		t.Errorf("收紧权限不应报错：%v", st2.PermError())
	}
}

func dirPermOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}
