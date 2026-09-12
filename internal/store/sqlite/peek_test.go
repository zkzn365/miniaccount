package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// 扫描账套目录：认出账套、跳过边车文件、坏文件不拖垮整个列表。
func TestScanBookDir(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// ① 一个真账套（走真实建账路径，别自己拼表）
	good := filepath.Join(dir, "hang-zhou-yun-fan.db")
	mkBookFile(t, good, "杭州云帆软件有限公司")

	// ② 一个不是账套的 .db（别的软件的库 / 用户误放的文件）
	alien := filepath.Join(dir, "别的软件.db")
	raw := openRawFile(t, alien)
	if err := raw.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `CREATE TABLE whatever (id INTEGER)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	// ③ 空文件
	empty := filepath.Join(dir, "empty.db")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// ④ 边车文件不该被列出来
	if err := os.WriteFile(good+"-wal", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ScanBookDir(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]BookPeek{}
	for _, b := range got {
		byName[b.FileName] = b
	}
	if len(got) != 3 {
		t.Fatalf("列出的文件数 = %d，期望 3（两个 .db 坏文件 + 一个账套）：%v", len(got), byName)
	}
	if !byName["hang-zhou-yun-fan.db"].IsBook {
		t.Errorf("★ 真账套没被认出来：%+v", byName["hang-zhou-yun-fan.db"])
	}
	if byName["hang-zhou-yun-fan.db"].CompanyName != "杭州云帆软件有限公司" {
		t.Errorf("单位名称 = %q", byName["hang-zhou-yun-fan.db"].CompanyName)
	}
	if byName["别的软件.db"].IsBook {
		t.Error("★ 不是账套的库被当成了账套 —— 打开它会往用户文件里建表")
	}
	if byName["别的软件.db"].Err == "" {
		t.Error("坏文件要说明原因，界面才能解释")
	}
	if byName["empty.db"].IsBook || byName["empty.db"].Err == "" {
		t.Error("空文件应当被标出来")
	}
	if _, ok := byName["hang-zhou-yun-fan.db-wal"]; ok {
		t.Error("★ WAL 边车文件被当成账套列出来了")
	}

	// 账套排在前面
	if !got[0].IsBook {
		t.Errorf("账套应当排在前面，实际第一个是 %s", got[0].FileName)
	}
}

// ★ 看一眼不能有副作用：绝不能往用户的文件里建表。
func TestPeekBookDoesNotTouchFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	alien := filepath.Join(dir, "别人的库.db")
	raw := openRawFile(t, alien)
	if err := raw.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `CREATE TABLE t (id INTEGER)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	before, err := os.ReadFile(alien)
	if err != nil {
		t.Fatal(err)
	}
	p := PeekBook(ctx, alien)
	if p.IsBook {
		t.Error("不该被当成账套")
	}
	after, err := os.ReadFile(alien)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Errorf("★ 只是看一眼，文件却变了：%d → %d 字节", len(before), len(after))
	}

	// 再确认：里面没有多出我们的表
	raw2 := openRawFile(t, alien)
	defer func() { _ = raw2.Close() }()
	var n int
	err = raw2.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('book','voucher','account')`).
		Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("★ 我们的表被建进了别人的文件里（%d 张）", n)
	}
}

// 目录不存在时返回空列表，不是错误 —— 第一次用软件时那个目录还没建。
func TestScanBookDirMissingDir(t *testing.T) {
	got, err := ScanBookDir(context.Background(), filepath.Join(t.TempDir(), "还没建"))
	if err != nil {
		t.Fatalf("目录不存在不该报错：%v", err)
	}
	if len(got) != 0 {
		t.Errorf("期望空列表，实际 %d 条", len(got))
	}
}

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

// mkBookFile 在指定路径建一个真账套（走真实建账路径）。
func mkBookFile(t *testing.T, path, company string) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Options{Path: path, CreateDirs: true})
	if err != nil {
		t.Fatalf("打开 %s 失败: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if _, err := db.CreateBook(ctx, CreateBookInput{
		CompanyName: company, TaxType: TaxTypeGeneral,
		StartYear: 2025, StartMonth: 1, ThroughYear: 2025,
		CurrentYear: 2025, CurrentMonth: 1,
	}); err != nil {
		t.Fatalf("建账失败: %v", err)
	}
}

// openRawFile 打开一个**裸** SQLite 文件（不迁移、不建账套）。
func openRawFile(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(context.Background(), Options{Path: path, CreateDirs: true})
	if err != nil {
		t.Fatalf("打开 %s 失败: %v", path, err)
	}
	return db
}
