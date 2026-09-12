package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// 夹具：造一个「真实」账套文件 + 附件目录
// ---------------------------------------------------------------------------

type fixture struct {
	Dir      string
	DBPath   string
	FilesDir string
	// Attachments 是期望被备份的附件相对路径集合。
	Attachments map[string][]byte
}

// newFixture 创建一个带 WAL、带附件、带真实数据的账套。
func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{
		Dir:         dir,
		DBPath:      filepath.Join(dir, "测试公司.db"),
		FilesDir:    filepath.Join(dir, "测试公司.files"),
		Attachments: map[string][]byte{},
	}

	// 建库（带 WAL，这样裸拷 .db 会丢数据的场景才成立）
	db, err := sql.Open("sqlite", "file:"+f.DBPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE book (id INTEGER PRIMARY KEY, company_name TEXT, credit_code TEXT);
		CREATE TABLE account (id INTEGER PRIMARY KEY, code TEXT, name TEXT);
		CREATE TABLE voucher (id INTEGER PRIMARY KEY, no TEXT, biz_date TEXT);
		CREATE TABLE ledger_entry (id INTEGER PRIMARY KEY, biz_date TEXT, debit INTEGER);
		INSERT INTO book VALUES (1, '杭州某某科技有限公司', '91330100MA2XXXXXXX');
		INSERT INTO account VALUES (1, '1002', '银行存款');
		INSERT INTO voucher VALUES (1, '记-2025-09-0001', '2025-09-11');
		INSERT INTO ledger_entry VALUES (1, '2025-09-11', 5000000);
	`)
	if err != nil {
		t.Fatal(err)
	}

	// 附件：两张「发票」PDF + 一张图片，按内容寻址存放
	files := map[string][]byte{
		"invoice-001.pdf": []byte("%PDF-1.4 增值税专用发票 第一张 ..."),
		"invoice-002.pdf": []byte("%PDF-1.4 增值税普通发票 第二张 ..."),
		"receipt.jpg":     []byte("\xff\xd8\xff\xe0 差旅报销单照片 ..."),
	}
	for name, data := range files {
		hash := SHA256Of(data)
		rel := BlobPath(hash)
		abs := filepath.Join(f.FilesDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, data, 0o644); err != nil {
			t.Fatal(err)
		}
		f.Attachments[rel] = data
		_ = name
	}
	return f
}

func (f *fixture) backup(t *testing.T, opts Options) string {
	t.Helper()
	dest := filepath.Join(f.Dir, "backups", "test.mabak")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	b := &Backup{
		DBPath: f.DBPath, FilesDir: f.FilesDir,
		AppVersion: "0.1.0-test", CompanyName: "杭州某某科技有限公司",
	}
	if _, err := b.Create(context.Background(), dest, opts); err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	return dest
}

// ---------------------------------------------------------------------------
// 备份
// ---------------------------------------------------------------------------

func TestCreateBackup(t *testing.T) {
	f := newFixture(t)
	dest := f.backup(t, Options{IncludeFiles: true})

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("备份文件不存在: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("备份文件为空")
	}

	// 归档内容
	zr, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatalf("备份不是合法的 zip: %v", err)
	}
	defer zr.Close()

	names := map[string]bool{}
	for _, ze := range zr.File {
		names[ze.Name] = true
	}
	if !names[manifestName] {
		t.Error("归档缺少 manifest.json")
	}
	if !names[dbName] {
		t.Error("归档缺少 book.db")
	}
	for rel := range f.Attachments {
		if !names[filesPrefix+rel] {
			t.Errorf("归档缺少附件 %s", rel)
		}
	}
}

func TestManifest(t *testing.T) {
	f := newFixture(t)
	dest := f.backup(t, Options{IncludeFiles: true})

	m, err := Inspect(dest)
	if err != nil {
		t.Fatalf("读取 manifest 失败: %v", err)
	}
	if m.FormatVersion != FormatVersion {
		t.Errorf("格式版本 = %d", m.FormatVersion)
	}
	if m.AppVersion != "0.1.0-test" {
		t.Errorf("应用版本 = %q", m.AppVersion)
	}
	if m.CompanyName != "杭州某某科技有限公司" {
		t.Errorf("公司名 = %q（应从快照里读出来）", m.CompanyName)
	}
	if m.CreditCode != "91330100MA2XXXXXXX" {
		t.Errorf("统一社会信用代码 = %q", m.CreditCode)
	}
	if m.FileCount != 3 {
		t.Errorf("附件数 = %d，期望 3", m.FileCount)
	}
	if m.FileBytes == 0 {
		t.Error("附件总字节数应为正")
	}
	if m.VoucherCount != 1 {
		t.Errorf("凭证数 = %d，期望 1", m.VoucherCount)
	}
	if m.AccountCount != 1 {
		t.Errorf("科目数 = %d，期望 1", m.AccountCount)
	}
	if m.PeriodFrom != "2025-09-11" || m.PeriodTo != "2025-09-11" {
		t.Errorf("账务期间 = %s..%s", m.PeriodFrom, m.PeriodTo)
	}
	if m.DBSHA256 == "" || m.DBSize == 0 {
		t.Error("数据库快照信息缺失")
	}
	for rel, hash := range m.Files {
		if hash != SHA256Of(f.Attachments[rel]) {
			t.Errorf("附件 %s 的校验和不对", rel)
		}
	}
}

// ★ 备份必须包含 WAL 里的最新数据。
//
// 这正是「直接复制 db 文件」做不到的：数据还在 -wal 里时裸拷 .db 会丢。
// VACUUM INTO 会先把 WAL 合并进快照。
func TestBackupCapturesWALData(t *testing.T) {
	f := newFixture(t)

	// 再写一条，此时数据很可能还在 WAL 中
	db, err := sql.Open("sqlite", "file:"+f.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO voucher VALUES (2, '记-2025-09-0002', '2025-09-20')`); err != nil {
		t.Fatal(err)
	}
	// 故意不 Checkpoint、不 Close —— 模拟「应用正在运行时备份」
	// 数据此刻仍在 WAL 文件里
	defer db.Close()

	// 裸拷 .db（对照组）
	rawCopy := filepath.Join(f.Dir, "raw.db")
	data, err := os.ReadFile(f.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rawCopy, data, 0o644); err != nil {
		t.Fatal(err)
	}
	rawDB, err := sql.Open("sqlite", "file:"+rawCopy+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var rawCount int
	_ = rawDB.QueryRow(`SELECT COUNT(*) FROM voucher`).Scan(&rawCount)
	rawDB.Close()

	// 备份（VACUUM INTO）
	dest := f.backup(t, Options{IncludeFiles: false})
	m, err := Inspect(dest)
	if err != nil {
		t.Fatal(err)
	}

	if m.VoucherCount != 2 {
		t.Errorf("备份中凭证数 = %d，期望 2（VACUUM INTO 应合并 WAL）", m.VoucherCount)
	}
	t.Logf("裸拷 .db 读到 %d 条凭证；VACUUM INTO 读到 %d 条", rawCount, m.VoucherCount)
	if rawCount >= 2 {
		t.Skip("本次运行 WAL 已被自动 checkpoint，对照实验不成立（不影响备份正确性）")
	}
}

func TestBackupWithoutFiles(t *testing.T) {
	f := newFixture(t)
	dest := f.backup(t, Options{IncludeFiles: false})

	m, err := Inspect(dest)
	if err != nil {
		t.Fatal(err)
	}
	if m.FileCount != 0 || len(m.Files) != 0 {
		t.Errorf("兼容模式下不应包含附件，实际 %d 个", m.FileCount)
	}

	zr, _ := zip.OpenReader(dest)
	defer zr.Close()
	for _, ze := range zr.File {
		if len(ze.Name) > len(filesPrefix) && ze.Name[:len(filesPrefix)] == filesPrefix {
			t.Errorf("兼容模式下不应有附件条目 %s", ze.Name)
		}
	}
}

func TestBackupRefusesOverwrite(t *testing.T) {
	f := newFixture(t)
	dest := f.backup(t, Options{IncludeFiles: true})

	b := &Backup{DBPath: f.DBPath, FilesDir: f.FilesDir}
	if _, err := b.Create(context.Background(), dest, Options{}); !errors.Is(err, ErrTargetExists) {
		t.Fatalf("重复备份到同一路径应报 ErrTargetExists，得到 %v", err)
	}
}

func TestBackupNoFilesDir(t *testing.T) {
	// 没有附件目录是正常情况（用户还没上传过附件）
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "book.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE book (id INTEGER PRIMARY KEY, company_name TEXT, credit_code TEXT);
	         CREATE TABLE account (id INTEGER PRIMARY KEY);
	         CREATE TABLE voucher (id INTEGER PRIMARY KEY, biz_date TEXT);
	         CREATE TABLE ledger_entry (id INTEGER PRIMARY KEY, biz_date TEXT);
	         INSERT INTO book VALUES (1, 'A', 'B');`)
	db.Close()

	dest := filepath.Join(dir, "x.mabak")
	b := &Backup{DBPath: dbPath, FilesDir: filepath.Join(dir, "missing.files")}
	if _, err := b.Create(context.Background(), dest, Options{IncludeFiles: true}); err != nil {
		t.Fatalf("附件目录不存在时不应报错: %v", err)
	}
}

func TestBackupProgress(t *testing.T) {
	f := newFixture(t)
	dest := filepath.Join(f.Dir, "p.mabak")
	var calls int
	var lastDone, lastTotal int
	b := &Backup{DBPath: f.DBPath, FilesDir: f.FilesDir}
	if _, err := b.Create(context.Background(), dest, Options{
		IncludeFiles: true,
		Progress: func(done, total int) {
			calls++
			lastDone, lastTotal = done, total
		},
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("进度回调次数 = %d，期望 3", calls)
	}
	if lastDone != 3 || lastTotal != 3 {
		t.Errorf("最终进度 = %d/%d", lastDone, lastTotal)
	}
}

// ---------------------------------------------------------------------------
// 恢复
// ---------------------------------------------------------------------------

func TestRestoreRoundTrip(t *testing.T) {
	f := newFixture(t)
	dest := f.backup(t, Options{IncludeFiles: true})

	// 破坏原账套：删库、删附件
	if err := os.Remove(f.DBPath); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(f.DBPath + suffix)
	}
	if err := os.RemoveAll(f.FilesDir); err != nil {
		t.Fatal(err)
	}

	res, err := Restore(context.Background(), dest, f.DBPath, f.FilesDir)
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if !res.DBRestored {
		t.Error("应报告数据库已恢复")
	}
	if res.FilesRestored != 3 {
		t.Errorf("恢复附件数 = %d，期望 3", res.FilesRestored)
	}

	// 数据库内容完整
	db, err := sql.Open("sqlite", "file:"+f.DBPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name, no string
	if err := db.QueryRow(`SELECT company_name FROM book WHERE id = 1`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "杭州某某科技有限公司" {
		t.Errorf("恢复后公司名 = %q", name)
	}
	if err := db.QueryRow(`SELECT no FROM voucher WHERE id = 1`).Scan(&no); err != nil {
		t.Fatal(err)
	}
	if no != "记-2025-09-0001" {
		t.Errorf("恢复后凭证号 = %q", no)
	}

	// 附件逐字节一致
	for rel, want := range f.Attachments {
		got, err := os.ReadFile(filepath.Join(f.FilesDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("附件 %s 未恢复: %v", rel, err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("附件 %s 内容不一致", rel)
		}
	}

	// ★ 这个场景里原账套是**测试自己删掉的**，本来就没什么可暂存。
	//
	// 所以正确的行为是**不建**暂存目录：建一个空目录再告诉用户
	// 「你原来的账都在这儿」，比不提示更坏 —— 用户会以为旧数据被留着了。
	// 真正测「有旧账套时会不会暂存」的是 TestRestoreStashesExisting。
	if res.BackupDir != "" {
		t.Errorf("没有旧账套可暂存时不该建暂存目录，实际 %s", res.BackupDir)
	}
	if _, err := os.Stat(res.BackupDir); res.BackupDir != "" && err == nil {
		t.Errorf("暂存目录不该存在：%s", res.BackupDir)
	}
}

// ★ 空账套位置恢复：不建空暂存目录，但恢复本身要成功。
func TestRestoreIntoEmptyLocationLeavesNoStash(t *testing.T) {
	f := newFixture(t)
	dest := f.backup(t, Options{IncludeFiles: true})

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fresh.db")
	filesDir := filepath.Join(dir, "fresh.files")

	res, err := Restore(context.Background(), dest, dbPath, filesDir)
	if err != nil {
		t.Fatalf("恢复到空位置失败: %v", err)
	}
	if !res.DBRestored {
		t.Error("应报告数据库已恢复")
	}
	if res.BackupDir != "" {
		t.Errorf("没有旧账套时不该建暂存目录，实际 %s", res.BackupDir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stashPrefix) {
			t.Errorf("★ 空位置恢复后留下了空暂存目录：%s", e.Name())
		}
	}
	// 恢复出来的账套要真的能用
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT company_name FROM book WHERE id = 1`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "杭州某某科技有限公司" {
		t.Errorf("恢复后公司名 = %q", name)
	}
}

// 恢复前必须把原账套暂存，而不是直接删 —— 用户的数据比磁盘空间值钱
func TestRestoreStashesExisting(t *testing.T) {
	f := newFixture(t)
	dest := f.backup(t, Options{IncludeFiles: false})

	// 在原库里写一条「恢复后会消失」的记录
	db, _ := sql.Open("sqlite", "file:"+f.DBPath)
	db.Exec(`INSERT INTO voucher VALUES (99, '记-2025-09-0099', '2025-09-30')`)
	db.Close()

	res, err := Restore(context.Background(), dest, f.DBPath, f.FilesDir)
	if err != nil {
		t.Fatal(err)
	}
	stashed := filepath.Join(res.BackupDir, filepath.Base(f.DBPath))
	if _, err := os.Stat(stashed); err != nil {
		t.Fatalf("原账套应被暂存到 %s: %v", stashed, err)
	}
	// 暂存的那份应仍含那条记录
	sdb, err := sql.Open("sqlite", "file:"+stashed+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	var n int
	if err := sdb.QueryRow(`SELECT COUNT(*) FROM voucher WHERE id = 99`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("暂存的账套应保留恢复前的数据")
	}
}

// ★ 附件损坏必须被检出并中止，而不是静默恢复出一份缺文件的账套
func TestRestoreDetectsCorruptAttachment(t *testing.T) {
	f := newFixture(t)

	// 造一个「附件内容被改过」的归档
	dest := filepath.Join(f.Dir, "corrupt.mabak")
	b := &Backup{DBPath: f.DBPath, FilesDir: f.FilesDir}
	if _, err := b.Create(context.Background(), dest, Options{IncludeFiles: true}); err != nil {
		t.Fatal(err)
	}
	// 重写归档：把一个附件的字节换掉，但 manifest 里的校验和不变
	corruptArchive(t, dest, f)

	target := filepath.Join(f.Dir, "restored.db")
	_, err := Restore(context.Background(), dest, target, filepath.Join(f.Dir, "restored.files"))
	if !errors.Is(err, ErrChecksumFail) {
		t.Fatalf("附件损坏应报 ErrChecksumFail，得到 %v", err)
	}
}

// manifest 缺失或损坏应报 ErrBadArchive
func TestRestoreBadArchive(t *testing.T) {
	dir := t.TempDir()

	// 不是 zip
	notZip := filepath.Join(dir, "notzip.mabak")
	os.WriteFile(notZip, []byte("this is not a zip"), 0o644)
	if _, err := Restore(context.Background(), notZip, filepath.Join(dir, "a.db"), ""); !errors.Is(err, ErrBadArchive) {
		t.Errorf("非 zip 应报 ErrBadArchive，得到 %v", err)
	}

	// 是 zip 但没有 manifest
	noManifest := filepath.Join(dir, "nomanifest.mabak")
	fh, _ := os.Create(noManifest)
	zw := zip.NewWriter(fh)
	w, _ := zw.Create("other.txt")
	w.Write([]byte("x"))
	zw.Close()
	fh.Close()
	if _, err := Restore(context.Background(), noManifest, filepath.Join(dir, "b.db"), ""); !errors.Is(err, ErrBadArchive) {
		t.Errorf("缺 manifest 应报 ErrBadArchive，得到 %v", err)
	}
}

// 更高版本的备份必须拒绝恢复，而不是冒险读进来
func TestRestoreRejectsNewerFormat(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "future.mabak")
	fh, _ := os.Create(dest)
	zw := zip.NewWriter(fh)
	w, _ := zw.Create(manifestName)
	json.NewEncoder(w).Encode(map[string]any{
		"format_version": FormatVersion + 1,
		"company_name":   "来自未来",
	})
	zw.Close()
	fh.Close()

	_, err := Restore(context.Background(), dest, filepath.Join(dir, "x.db"), "")
	if !errors.Is(err, ErrVersionTooNew) {
		t.Fatalf("应报 ErrVersionTooNew，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 内容寻址
// ---------------------------------------------------------------------------

func TestBlobPath(t *testing.T) {
	// 相同内容 → 相同路径（天然去重）
	a := SHA256Of([]byte("同一张发票"))
	b := SHA256Of([]byte("同一张发票"))
	if BlobPath(a) != BlobPath(b) {
		t.Error("相同内容应得到相同路径")
	}
	// 不同内容 → 不同路径
	if BlobPath(a) == BlobPath(SHA256Of([]byte("另一张发票"))) {
		t.Error("不同内容应得到不同路径")
	}
	// 路径形式：<前2位>/<完整hash>
	got := BlobPath(a)
	want := a[:2] + "/" + a
	if got != want {
		t.Errorf("BlobPath = %q，期望 %q", got, want)
	}
	if len(a) != 64 {
		t.Errorf("sha256 十六进制串长度 = %d，期望 64", len(a))
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// corruptArchive 重写归档，把第一个附件的字节换掉（manifest 保持不变）。
func corruptArchive(t *testing.T, archivePath string, f *fixture) {
	t.Helper()
	src, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	type item struct {
		name string
		data []byte
	}
	var items []item
	for _, ze := range src.File {
		rc, err := ze.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buf []byte
		buf = make([]byte, ze.UncompressedSize64)
		n, _ := rc.Read(buf)
		rc.Close()
		buf = buf[:n]

		// 把第一个附件的首字节翻转
		if len(items) > 0 && len(buf) > 0 && len(ze.Name) > len(filesPrefix) &&
			ze.Name[:len(filesPrefix)] == filesPrefix {
			buf[0] ^= 0xFF
		}
		items = append(items, item{ze.Name, buf})
	}
	src.Close()

	out, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	for _, it := range items {
		w, err := zw.Create(it.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(it.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}
