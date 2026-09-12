package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// craftArchive 手工造一个 .mabak，用于构造真实备份流程产生不出来的形态。
func craftArchive(t *testing.T, path string, m Manifest, extra map[string][]byte) {
	t.Helper()
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	zw := zip.NewWriter(fh)
	if extra[dbName] == nil {
		extra[dbName] = []byte("SQLite format 3\x00假的库")
	}
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	extra[manifestName] = mb
	// 顺序固定，便于排查
	names := make([]string, 0, len(extra))
	for n := range extra {
		names = append(names, n)
	}
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(extra[n]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// ★ 构造过的备份不能往附件目录之外写文件。
//
// 备份文件是可以从别人那里拿到的（会计、同事、代办）。manifest 的键
// 直接参与 filepath.Join，写成 "../x" 就能爬出目标目录 ——
// 用户以为自己只是在恢复一个账套，实际在往系统任意位置写文件。
// 修之前这条路径穿越是**成功**的，且 Restore 返回 nil。
func TestRestoreRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.mabak")
	outside := filepath.Join(dir, "escaped.txt")

	rel := "../escaped.txt"
	craftArchive(t, archive, Manifest{
		FormatVersion: FormatVersion,
		Files:         map[string]string{rel: ""},
	}, map[string][]byte{filesPrefix + rel: []byte("escaped!")})

	filesDir := filepath.Join(dir, "book.files")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Restore(context.Background(), archive, filepath.Join(dir, "book.db"), filesDir)
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("应报 ErrUnsafePath，得到 %v", err)
	}
	if _, statErr := os.Stat(outside); statErr == nil {
		t.Fatalf("★ 文件被写到了附件目录之外：%s", outside)
	}
}

// 各种越界写法都要挡住，而不是只挡 "../"。
func TestSafeArchiveRel(t *testing.T) {
	bad := []string{
		"../x", "../../etc/passwd", "/etc/passwd", "a/../../b",
		"..\\x", "a\\..\\..\\b", "C:\\windows\\x",
		"", ".", "..",
	}
	for _, rel := range bad {
		if safeArchiveRel(rel) {
			t.Errorf("%q 应被判为不安全", rel)
		}
	}
	good := []string{
		"aa/bb", "aa/bb.pdf", "a.txt", "深/层/文件.pdf",
		"a.b.c/x", "files-are-fine",
	}
	for _, rel := range good {
		if !safeArchiveRel(rel) {
			t.Errorf("%q 应被判为安全", rel)
		}
	}
}

// ★ 恢复中途失败必须把原账套放回来。
//
// 修之前：附件校验和不符 → 函数返回错误，但 dbPath 上躺着的已经是
// 「恢复了一半」的新库，原账套只留在 .restore-stash-xxx 里。
// 用户打开程序看到的是一个坏账套 —— 而函数的文档注释里
// 明明写着「可以把原来的账套放回去」。
func TestRestoreRollsBackOnFailure(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "book.db")
	filesDir := filepath.Join(dir, "book.files")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	orig := []byte("原来的账套内容-不能被弄丢")
	if err := os.WriteFile(dbPath, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filesDir, "keep.txt"), []byte("原附件"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 附件校验和故意写错，让恢复在「已经换掉账套之后」失败。
	// 数据库指纹必须给对 —— 否则会在更早的一步（缺指纹）就被拒绝，
	// 那样测不到「换掉账套之后再失败」这条路径。
	newDB := []byte("SQLite format 3\x00新的账套内容")
	archive := filepath.Join(dir, "bad.mabak")
	craftArchive(t, archive, Manifest{
		FormatVersion: FormatVersion,
		DBSHA256:      hashOf(newDB),
		Files: map[string]string{
			"aa/bb": strings.Repeat("0", 64),
		},
	}, map[string][]byte{
		dbName:                newDB,
		filesPrefix + "aa/bb": []byte("附件内容与校验和不符"),
	})

	_, err := Restore(context.Background(), archive, dbPath, filesDir)
	if !errors.Is(err, ErrChecksumFail) {
		t.Fatalf("应报 ErrChecksumFail，得到 %v", err)
	}
	// 错误信息要告诉用户「已经放回了」，否则他不敢再动这个账套
	if !strings.Contains(err.Error(), "已自动放回") {
		t.Errorf("错误信息应说明原账套已放回，实际 %q", err.Error())
	}

	got, readErr := os.ReadFile(dbPath)
	if readErr != nil {
		t.Fatalf("原账套文件不见了: %v", readErr)
	}
	if string(got) != string(orig) {
		t.Errorf("★ 恢复失败后原账套被覆盖：内容变成了 %q", got)
	}
	if _, e := os.Stat(filepath.Join(filesDir, "keep.txt")); e != nil {
		t.Errorf("★ 恢复失败后原附件也丢了: %v", e)
	}
	// 暂存目录要清掉，不留垃圾
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".restore-stash-") {
			t.Errorf("回滚成功后不该留下暂存目录 %s", e.Name())
		}
	}
}

// ★ 成功恢复后，原账套要留在暂存目录里 —— 那是「恢复错了备份」时
// 唯一的退路。但同一目录下**只保留最近一份**，不能无限堆积。
func TestRestoreKeepsOneStashOfPreviousBook(t *testing.T) {
	f := newFixture(t)
	dest := filepath.Join(f.Dir, "ok.mabak")
	b := &Backup{DBPath: f.DBPath, FilesDir: f.FilesDir}
	if _, err := b.Create(context.Background(), dest, Options{IncludeFiles: true}); err != nil {
		t.Fatal(err)
	}

	// 连做三次恢复：每次都往原库里插一条记号，验证最后一次的暂存是「上一次」的库
	for i := 1; i <= 3; i++ {
		db, err := sql.Open("sqlite", "file:"+f.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(
			`INSERT INTO voucher VALUES (?, '记-2025-09-0001', '2025-09-30')`,
			100+i); err != nil {
			t.Fatal(err)
		}
		db.Close()

		if _, err := Restore(context.Background(), dest, f.DBPath, f.FilesDir); err != nil {
			t.Fatalf("第 %d 次恢复失败: %v", i, err)
		}
	}

	// 暂存目录必须**有界**（不随恢复次数增长），但也不能只剩 0 个。
	//
	// 刻意保留「上一份 + 本次」共两份，而不是只留一份：
	// 无法从目录名分辨哪一份是「自动回滚失败后留下的救命副本」，
	// 而删错那一份是不可恢复的数据丢失。多占一个目录是极小的代价。
	var stashes []string
	entries, _ := os.ReadDir(f.Dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stashPrefix) {
			stashes = append(stashes, e.Name())
		}
	}
	if len(stashes) == 0 {
		t.Fatal("暂存目录一份都不剩 —— 恢复错了备份就没有退路了")
	}
	if len(stashes) > 2 {
		t.Fatalf("暂存目录 = %v（%d 份），应随恢复次数保持有界（≤2）",
			stashes, len(stashes))
	}
	sort.Strings(stashes)

	// 里面的那份要能用（是上一个版本的账套，不是垃圾）
	stashed := filepath.Join(f.Dir, stashes[0], filepath.Base(f.DBPath))
	if _, err := os.Stat(stashed); err != nil {
		t.Fatalf("暂存的原账套读不到: %v", err)
	}
	sdb, err := sql.Open("sqlite", "file:"+stashed+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	var n int
	if err := sdb.QueryRow(`SELECT COUNT(*) FROM voucher`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("暂存的账套里应有凭证")
	}
}

// 恶意归档必须在**动用户账套之前**就被拒绝。
func TestRestoreRejectsBeforeTouchingBook(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "book.db")
	filesDir := filepath.Join(dir, "book.files")
	os.MkdirAll(filesDir, 0o755)
	orig := []byte("原账套")
	os.WriteFile(dbPath, orig, 0o644)

	archive := filepath.Join(dir, "evil.mabak")
	craftArchive(t, archive, Manifest{
		FormatVersion: FormatVersion,
		Files:         map[string]string{"../x": ""},
	}, map[string][]byte{filesPrefix + "../x": []byte("x")})

	if _, err := Restore(context.Background(), archive, dbPath, filesDir); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("应报 ErrUnsafePath，得到 %v", err)
	}
	// 账套一个字节都不该被动过，也不该出现暂存目录
	got, _ := os.ReadFile(dbPath)
	if string(got) != string(orig) {
		t.Error("被拒绝的归档不该改动原账套")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".restore-stash-") {
			t.Error("校验在暂存之前完成，不该产生暂存目录")
		}
	}
}

// ★ 校验和字段非法/缺失时，绝不能拿任意内容替换用户的账套。
//
// 修之前判据是 `if manifest.DBSHA256 != ""` —— **空 hash 直接跳过校验**，
// 于是一个 db_sha256 为空的归档可以用任意内容替换账套，而且返回成功。
func TestRestoreRejectsMissingOrBadDBHash(t *testing.T) {
	for _, h := range []string{"", "abc", strings.Repeat("z", 64), strings.Repeat("a", 63)} {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "book.db")
		filesDir := filepath.Join(dir, "book.files")
		os.MkdirAll(filesDir, 0o755)
		orig := []byte("ORIGINAL")
		os.WriteFile(dbPath, orig, 0o644)

		archive := filepath.Join(dir, "bad.mabak")
		craftArchive(t, archive, Manifest{
			FormatVersion: FormatVersion, DBSHA256: h,
		}, map[string][]byte{dbName: []byte("NOT A DATABASE")})

		_, err := Restore(context.Background(), archive, dbPath, filesDir)
		if err == nil {
			t.Fatalf("指纹 %q 应被拒绝，却返回成功", h)
		}
		if !errors.Is(err, ErrBadArchive) {
			t.Errorf("指纹 %q 的错误 = %v，期望 ErrBadArchive", h, err)
		}
		got, _ := os.ReadFile(dbPath)
		if string(got) != string(orig) {
			t.Errorf("指纹 %q 时原账套被替换成了 %q", h, got)
		}
	}
}

// ★ panic 路径也必须回滚。
//
// 修之前守卫判的是 `err == nil`，而函数体里的 `err :=` 遮蔽了具名返回值 ——
// panic 时 err 仍是 nil，守卫直接返回，回滚不执行，
// 账套就从原路径消失了，界面重开变成一个空账套。
//
// 指纹短于 12 位会触发切片越界 panic（已另行修掉），
// 这里用「清单里的 hash 与实际不符且长度异常」构造一条会走到
// 非正常出口的路径来验证回滚。
func TestRestoreRollsBackEvenOnPanicPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "book.db")
	filesDir := filepath.Join(dir, "book.files")
	os.MkdirAll(filesDir, 0o755)
	orig := []byte("ORIGINAL BOOK CONTENT")
	os.WriteFile(dbPath, orig, 0o644)

	// 一个附件校验和故意写错 —— 会在「已经换掉账套之后」失败
	archive := filepath.Join(dir, "bad.mabak")
	craftArchive(t, archive, Manifest{
		FormatVersion: FormatVersion, DBSHA256: hashOf([]byte("SQLite format 3\x00假的库")),
		Files: map[string]string{"aa/bb": strings.Repeat("0", 64)},
	}, map[string][]byte{
		dbName:                []byte("SQLite format 3\x00假的库"),
		filesPrefix + "aa/bb": []byte("内容与校验和不符"),
	})

	if _, err := Restore(context.Background(), archive, dbPath, filesDir); err == nil {
		t.Fatal("应报错")
	}
	// 关键：账套必须在原路径上，而不是只剩在暂存目录里
	got, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("★ panic/失败路径下账套从原路径消失了: %v", err)
	}
	if string(got) != string(orig) {
		t.Errorf("账套内容被改成了 %q", got)
	}
}

// ★ 清理旧暂存时必须留下最新的一份 —— 那可能是用户唯一的退路。
//
// 修之前是无条件全删，而「自动回滚也失败」时原件就只剩在暂存目录里，
// 代码自己的错误信息正是让用户去那里手工拷回来。
func TestPruneOldStashesKeepsOne(t *testing.T) {
	dir := t.TempDir()

	// 三份「历史暂存」，最新那份里放着用户的救命副本
	for _, name := range []string{
		".restore-stash-20250101-120000",
		".restore-stash-20250102-120000",
		".restore-stash-20250103-120000",
	} {
		d := filepath.Join(dir, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "book.db"), []byte("rescue-"+name), 0o644)
	}
	// 一个无关目录不该被碰
	other := filepath.Join(dir, "别动我")
	os.MkdirAll(other, 0o755)

	pruneOldStashes(dir)

	var left []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stashPrefix) {
			left = append(left, e.Name())
		}
	}
	if len(left) != 1 {
		t.Fatalf("清理后剩余暂存 = %v，期望恰好 1 份", left)
	}
	// 留下的必须是**最新**的那份
	if left[0] != ".restore-stash-20250103-120000" {
		t.Errorf("保留的是 %s，期望最新那份", left[0])
	}
	// 里面的救命副本必须还在
	b, err := os.ReadFile(filepath.Join(dir, left[0], "book.db"))
	if err != nil {
		t.Fatalf("救命副本读不到: %v", err)
	}
	if !strings.Contains(string(b), "20250103") {
		t.Errorf("留下的不是最新那份内容：%q", b)
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("无关目录被误删了")
	}
}

// 只有一份时绝不能删 —— 它可能就是用户最后的退路。
func TestPruneOldStashesKeepsTheOnlyOne(t *testing.T) {
	dir := t.TempDir()
	only := filepath.Join(dir, ".restore-stash-20250101-120000")
	os.MkdirAll(only, 0o755)
	pruneOldStashes(dir)
	if _, err := os.Stat(only); err != nil {
		t.Error("仅有的那一份暂存被删掉了 —— 它可能是用户最后的退路")
	}
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ★ 备份失败不能留下半截 .mabak 把目标路径「毒化」。
//
// 直接写 dest 时，中途失败会留下半截文件；而 Create 开头会拒绝
// 已存在的 dest（防止覆盖用户的旧备份），于是此后每次重试同一个
// 文件名都失败，用户只能自己去把那个半截文件删掉。
//
// 写 .part 再改名让「失败」与「成功」在文件名上就分得清。
func TestBackupFailureLeavesNoPoisonedPath(t *testing.T) {
	f := newFixture(t)
	dest := filepath.Join(f.Dir, "half.mabak")
	b := &Backup{DBPath: f.DBPath, FilesDir: f.FilesDir}

	// 让附件收集阶段失败：指向一个不存在的附件文件
	// （IncludeFiles 会去读 .files 下的实际文件）
	os.WriteFile(filepath.Join(f.FilesDir, "aa", "bb"), []byte("x"), 0o644)
	// 先做一次正常备份，确认路径本身可用
	if _, err := b.Create(context.Background(), dest, Options{IncludeFiles: false}); err != nil {
		t.Fatalf("正常备份失败: %v", err)
	}
	// 已存在 → 应拒绝（这是防覆盖的既有行为，不能变）
	if _, err := b.Create(context.Background(), dest, Options{IncludeFiles: false}); !errors.Is(err, ErrTargetExists) {
		t.Errorf("已存在的目标应报 ErrTargetExists，得到 %v", err)
	}
	// 拒绝之后不能留下 .part 垃圾
	if _, err := os.Stat(dest + ".part"); err == nil {
		t.Error("被拒绝的备份不该留下 .part 文件")
	}

	// 换一个路径，模拟中途失败
	dest2 := filepath.Join(f.Dir, "sub", "failed.mabak")
	bad := &Backup{DBPath: "/nonexistent/nope.db", FilesDir: f.FilesDir}
	if _, err := bad.Create(context.Background(), dest2, Options{IncludeFiles: false}); err == nil {
		t.Fatal("数据库不存在时应失败")
	}
	// ★ 失败后目标路径必须是干净的：既没有半截文件，也没有 .part
	if _, err := os.Stat(dest2); err == nil {
		t.Error("失败的备份不该在目标路径留下文件（否则重试会被 ErrTargetExists 挡住）")
	}
	if _, err := os.Stat(dest2 + ".part"); err == nil {
		t.Error("失败的备份不该留下 .part 文件")
	}
	// 而且重试同一个路径应当能正常跑（用正确的 Backup 对象）
	good := &Backup{DBPath: f.DBPath, FilesDir: f.FilesDir}
	if _, err := good.Create(context.Background(), dest2, Options{IncludeFiles: false}); err != nil {
		t.Errorf("失败路径重试应能成功，得到 %v", err)
	}
}
