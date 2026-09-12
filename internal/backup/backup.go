// Package backup 实现账套备份与恢复。
//
// # 需求与约束
//
//	「账套备份、恢复（直接复制 db 文件）」
//	「附件本地保存」
//
// 这两条并不冲突，做法是**分开存储、合并备份**：
//
//	<账套目录>/
//	  ├── 公司名.db              业务数据
//	  ├── 公司名.files/          附件（内容寻址）
//	  └── backups/公司名_*.mabak  备份 = 上述两者的压缩包
//
// # 为什么不能裸拷 .db
//
// 需求原文写的是「直接复制 db 文件」，但那样在 WAL 模式下**不安全**：
//
//   - SQLite 默认 journal_mode=WAL，最新数据可能还在 -wal 文件里，
//     只拷 .db 会丢掉最近的凭证；
//   - 应用运行中拷贝可能拿到写到一半的页。
//
// 因此本包用 SQLite 的 `VACUUM INTO` 生成**一致性快照**。
// 对用户而言效果一样（打开即可用），但保证完整。
// 这是必要的技术修正，不是需求变更。
package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"miniaccount/internal/attachment"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// 备份相关错误。
var (
	ErrBadArchive    = errors.New("backup: 备份文件格式非法")
	ErrVersionTooNew = errors.New("backup: 备份由更高版本的软件创建")
	ErrChecksumFail  = errors.New("backup: 附件校验和不匹配")
	ErrTargetExists  = errors.New("backup: 目标已存在")
	// ErrUnsafePath 表示归档里的路径会写到目标目录之外。
	//
	// 这是一个**安全**问题而不只是数据问题：备份文件是可以从别人那里
	// 拿到的（会计、同事、代办），一个构造过的 .mabak 可以借恢复之名
	// 往磁盘上任意位置写文件。
	ErrUnsafePath = errors.New("backup: 归档内含有非法路径")
)

// 归档内的固定路径。
const (
	manifestName = "manifest.json"
	// stashPrefix 是恢复前暂存原账套的目录名前缀。
	stashPrefix = ".restore-stash-"
	dbName      = "book.db"
	filesPrefix = "files/"
)

// FormatVersion 是 .mabak 归档格式版本。
const FormatVersion = 1

// Manifest 是备份的元数据，写在归档首位。
type Manifest struct {
	FormatVersion int    `json:"format_version"`
	AppVersion    string `json:"app_version"`
	CompanyName   string `json:"company_name"`
	CreditCode    string `json:"credit_code,omitempty"`
	CreatedAt     string `json:"created_at"`
	// DBSize 是数据库快照的字节数，用于恢复时的快速合理性检查。
	DBSize int64 `json:"db_size"`
	// DBSHA256 是数据库快照的校验和。
	DBSHA256 string `json:"db_sha256"`
	// Files 是附件清单：相对路径 → 校验和。
	//
	// 逐个记录校验和是为了恢复时能发现损坏 ——
	// 备份文件本身可能被网络传输损坏或部分覆盖。
	Files map[string]string `json:"files"`
	// FileCount / FileBytes 便于界面上显示体积构成。
	FileCount int   `json:"file_count"`
	FileBytes int64 `json:"file_bytes"`
	// PeriodFrom / PeriodTo 提示备份涵盖的账务期间，便于用户分辨。
	PeriodFrom   string `json:"period_from,omitempty"`
	PeriodTo     string `json:"period_to,omitempty"`
	VoucherCount int    `json:"voucher_count"`
	AccountCount int    `json:"account_count"`
}

// Backup 封装一次备份任务。
type Backup struct {
	// DBPath 是账套数据库路径。
	DBPath string
	// FilesDir 是附件目录。为空表示该账套没有附件。
	FilesDir string
	// AppVersion 写入 manifest，便于将来判断兼容性。
	AppVersion string
	// CompanyName 写入 manifest。
	CompanyName string
	CreditCode  string
}

// Options 控制备份行为。
type Options struct {
	// IncludeFiles 为 false 时只备份数据库（「兼容模式」）。
	// 界面上必须明确警告附件会丢失。
	IncludeFiles bool
	// Progress 在每写入一个附件后回调，用于显示进度。
	Progress func(done, total int)
}

// Create 生成一个 .mabak 备份文件。
func (b *Backup) Create(ctx context.Context, dest string, opts Options) (*Manifest, error) {
	if b.DBPath == "" {
		return nil, errors.New("backup: 未指定数据库路径")
	}
	if _, err := os.Stat(dest); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrTargetExists, dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, fmt.Errorf("backup: 创建备份目录: %w", err)
	}

	// 1) 一致性快照：VACUUM INTO 而不是拷贝文件
	tmpDir, err := os.MkdirTemp(filepath.Dir(dest), ".mabak-tmp-*")
	if err != nil {
		return nil, fmt.Errorf("backup: 创建临时目录: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	snapshot := filepath.Join(tmpDir, dbName)
	if err := vacuumInto(ctx, b.DBPath, snapshot); err != nil {
		return nil, err
	}

	// 2) 收集附件清单
	files, totalBytes, err := collectFiles(b.FilesDir, opts.IncludeFiles)
	if err != nil {
		return nil, err
	}

	// 3) 组装 manifest
	dbHash, dbSize, err := fileSHA256(snapshot)
	if err != nil {
		return nil, err
	}
	m := &Manifest{
		FormatVersion: FormatVersion,
		AppVersion:    b.AppVersion,
		CompanyName:   b.CompanyName,
		CreditCode:    b.CreditCode,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		DBSize:        dbSize,
		DBSHA256:      dbHash,
		Files:         map[string]string{},
		FileCount:     len(files),
		FileBytes:     totalBytes,
	}
	for _, f := range files {
		m.Files[f.rel] = f.hash
	}
	// 顺带读出账套概况，便于用户分辨备份
	fillBookStats(ctx, snapshot, m)

	// 4) 写 zip
	//
	// ★ 先写 .part 再改名。
	//
	// 直接写 dest 的话，中途失败（磁盘满、进程被杀）会留下一个**半截
	// .mabak**；而本函数开头会拒绝已存在的 dest（那是为了防止覆盖
	// 用户的旧备份），于是此后每次重试同一个文件名都失败，
	// 用户只能自己去把那个半截文件删掉 —— 而他多半不知道。
	//
	// 写 .part 让「失败」和「成功」在文件名上就分得清：
	// 失败时删掉 .part，dest 要么完整存在、要么根本不存在。
	part := dest + ".part"
	_ = os.Remove(part) // 上一次失败可能留下的
	f, err := os.Create(part)
	if err != nil {
		return nil, fmt.Errorf("backup: 创建备份文件: %w", err)
	}
	_ = f
	// 任何失败出口都清掉半成品
	keepPart := false
	defer func() {
		if !keepPart {
			_ = os.Remove(part)
		}
	}()

	zw := zip.NewWriter(f)
	// manifest 放在最前：即便归档后半部分损坏，也能读出元信息
	if err := writeJSON(zw, manifestName, m); err != nil {
		return nil, err
	}
	if err := writeFileToZip(zw, dbName, snapshot); err != nil {
		return nil, err
	}
	for i, file := range files {
		if err := writeFileToZip(zw, filesPrefix+file.rel, file.abs); err != nil {
			return nil, err
		}
		if opts.Progress != nil {
			opts.Progress(i+1, len(files))
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("backup: 关闭归档: %w", err)
	}
	// 关文件、落盘，再改名。少了 Sync 的话掉电可能留下一个
	// 「文件在、内容不全」的归档 —— 而它看起来是完整的。
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("backup: 落盘: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("backup: 关闭文件: %w", err)
	}
	if err := os.Rename(part, dest); err != nil {
		return nil, fmt.Errorf("backup: 落成 %s: %w", dest, err)
	}
	keepPart = true // 已改名，.part 不存在了
	return m, nil
}

// RestoreResult 描述一次恢复的结果。
type RestoreResult struct {
	Manifest *Manifest
	// DBRestored 表示账套数据库已写回。
	DBRestored bool
	// FilesRestored 是恢复的附件数量。
	FilesRestored int
	// BackupDir 是恢复前原账套被挪去的暂存目录。
	//
	// 恢复**成功**后它也保留 —— 那是「恢复错了备份」时唯一的退路，
	// 而这个错误在实务里相当常见（同事发来的备份、几份备份文件名一样）。
	// 为免无限堆积，同一目录下只保留最近一份，建新暂存时清掉旧的。
	// 界面必须把这个路径告诉用户，否则等于没有。
	BackupDir string
}

// Restore 从一个 .mabak 恢复账套。
//
// 恢复前会把现有 db 与附件目录整体**改名暂存**而不是删除：
// 万一恢复过程中失败（磁盘满、归档损坏），可以把原来的账套放回去。
// 用户的数据比磁盘空间值钱得多。
func Restore(ctx context.Context, archivePath, dbPath, filesDir string) (res *RestoreResult, err error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("%w: 打开 %s: %v", ErrBadArchive, archivePath, err)
	}
	defer zr.Close()

	// 1) 先读 manifest
	var manifest *Manifest
	for _, f := range zr.File {
		if f.Name != manifestName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: 读取 manifest: %v", ErrBadArchive, err)
		}
		manifest = &Manifest{}
		err = json.NewDecoder(rc).Decode(manifest)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%w: 解析 manifest: %v", ErrBadArchive, err)
		}
		break
	}
	if manifest == nil {
		return nil, fmt.Errorf("%w: 缺少 %s", ErrBadArchive, manifestName)
	}
	if manifest.FormatVersion > FormatVersion {
		return nil, fmt.Errorf("%w: 备份格式 v%d，本程序支持到 v%d",
			ErrVersionTooNew, manifest.FormatVersion, FormatVersion)
	}

	// ★ 先校验归档里的路径，**再**动用户的账套。
	//
	// 校验放在暂存之前是有意的：一个恶意归档应当在任何文件被改动之前
	// 就被拒绝。放到后面即使能回滚，也已经动过用户的账套了。
	if err := validateManifestPaths(manifest); err != nil {
		return nil, err
	}

	res = &RestoreResult{Manifest: manifest}

	// 2) 暂存现有文件
	stash, serr := stashExisting(dbPath, filesDir)
	if serr != nil {
		return nil, serr
	}
	res.BackupDir = stash

	// ★ 从这里开始，账套已经被挪走了 —— 任何失败都必须把它放回来。
	//
	// 之前没有这一步：附件校验和不符时函数直接返回错误，而 dbPath 上
	// 躺着的已经是「恢复了一半」的新库，原账套只留在
	// .restore-stash-xxx 里，用户打开程序看到的是一个坏账套 ——
	// 与本函数注释里承诺的「可以把原来的账套放回去」不符。
	// ★ 判据是「有没有走到成功出口」，**不能**判 `err == nil`。
	//
	// 原来写的是 `if err == nil { return }`，而函数体内有
	// `got, _, err := fileSHA256(...)` 这样的 := —— 它产生了一个**新的** err，
	// 把具名返回值遮蔽了。于是 panic 时具名 err 仍是 nil，守卫直接返回，
	// 回滚根本不执行，账套就从原路径消失了。
	//
	// 用「成功标志」而不是「err 是否为 nil」：只有走到函数末尾那一句
	// 才置位，其余任何出口（包括 panic）都会回滚。
	success := false
	defer func() {
		if success {
			return
		}
		if stash == "" {
			// 本来就没有旧账套要回滚 —— 删掉恢复了一半的新文件即可，
			// 不能对空串调 unstash（那会把当前目录当成暂存目录）。
			_ = os.Remove(dbPath)
			_ = os.Remove(dbPath + "-wal")
			_ = os.Remove(dbPath + "-shm")
			return
		}
		if rbErr := unstash(stash, dbPath, filesDir); rbErr != nil {
			err = fmt.Errorf(
				"%w（原账套自动回滚失败：%v；原件在 %s，请手工把里面的文件拷回账套位置）",
				err, rbErr, stash)
			return
		}
		if err == nil {
			// panic 路径：err 是 nil，这里补一个错误，
			// 否则调用方会以为恢复成功了
			err = fmt.Errorf("%w: 恢复过程异常中断（原账套已自动放回）", ErrBadArchive)
			return
		}
		err = fmt.Errorf("%w（原账套已自动放回）", err)
	}()

	// 3) 解压数据库
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("backup: 创建账套目录: %w", err)
	}
	tmpDB := dbPath + ".restoring"
	if err := extractOne(zr, dbName, tmpDB); err != nil {
		return nil, err
	}
	// 校验数据库快照
	// ★ hash 必须存在且合法。
	//
	// 原来写的是 `if manifest.DBSHA256 != ""`：**空 hash 直接跳过校验**，
	// 于是构造一个 db_sha256 为空的归档，就能用任意内容替换掉用户的账套，
	// 而且 Restore 返回成功。正常备份永远带 64 位十六进制 hash，
	// 所以「缺失」只可能来自构造或篡改 —— 必须当场拒绝。
	if err := validateDBHash(manifest.DBSHA256); err != nil {
		os.Remove(tmpDB)
		return nil, err
	}
	got, _, herr := fileSHA256(tmpDB)
	if herr != nil {
		return nil, herr
	}
	if got != manifest.DBSHA256 {
		os.Remove(tmpDB)
		// 切片前先截断：hash 长度已由 validateDBHash 保证是 64，
		// 但这里仍用 shortHash 而不是 [:12] —— 报错路径不该再引入一次 panic 机会
		return nil, fmt.Errorf("%w: 数据库快照 %s ≠ %s",
			ErrChecksumFail, shortHash(got), shortHash(manifest.DBSHA256))
	}
	// 释放 WAL/SHM，避免旧库的 WAL 污染新库
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(dbPath + suffix)
	}
	if err := os.Rename(tmpDB, dbPath); err != nil {
		return nil, fmt.Errorf("backup: 写回账套数据库: %w", err)
	}
	res.DBRestored = true

	// 4) 解压附件（逐个校验 sha256）
	if len(manifest.Files) > 0 && filesDir != "" {
		if err := os.MkdirAll(filesDir, 0o755); err != nil {
			return nil, fmt.Errorf("backup: 创建附件目录: %w", err)
		}
		// 按路径排序，保证恢复顺序稳定、进度可预期
		paths := make([]string, 0, len(manifest.Files))
		for p := range manifest.Files {
			paths = append(paths, p)
		}
		sort.Strings(paths)

		for _, rel := range paths {
			// 双保险：manifest 已经整体校验过，这里再确认一次落点没跑出去。
			// 少一道检查的代价是「用户以为在恢复账套，实际在往系统目录写文件」。
			if !safeArchiveRel(rel) {
				return nil, fmt.Errorf("%w: %q", ErrUnsafePath, rel)
			}
			want := manifest.Files[rel]
			target := filepath.Join(filesDir, filepath.FromSlash(rel))
			if !withinDir(filesDir, target) {
				return nil, fmt.Errorf("%w: %q 会写到 %s 之外",
					ErrUnsafePath, rel, filesDir)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, err
			}
			if err := extractOne(zr, filesPrefix+rel, target); err != nil {
				return nil, err
			}
			if want != "" {
				got, _, err := fileSHA256(target)
				if err != nil {
					return nil, err
				}
				if got != want {
					return nil, fmt.Errorf("%w: 附件 %s", ErrChecksumFail, rel)
				}
			}
			res.FilesRestored++
		}
	}
	success = true
	return res, nil
}

// validateManifestPaths 检查 manifest 里记录的附件相对路径是否安全。
//
// 判据三条，任一不满足就拒绝整个归档：
//   - 不能是绝对路径（/etc/passwd、C:\...）
//   - 按 "/" 与 "\" 切开后不能含 ".." 段
//   - 清洗之后必须仍落在目标目录之内（双保险，防住上面两条没想到的写法）
//
// 为什么必须做：备份文件是可以从别人那里拿到的。一个构造过的 .mabak
// 只要在 manifest 的键里写 "../.ssh/authorized_keys"，
// 恢复时就会往那儿写 —— 用户以为自己只是在恢复一个账套。
func validateManifestPaths(m *Manifest) error {
	for rel := range m.Files {
		if !safeArchiveRel(rel) {
			return fmt.Errorf("%w: %q", ErrUnsafePath, rel)
		}
	}
	return nil
}

// safeArchiveRel 报告一个归档内的相对路径是否可以安全地落到目标目录下。
func safeArchiveRel(rel string) bool {
	if rel == "" {
		return false
	}
	// 绝对路径（含 Windows 盘符）
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") {
		return false
	}
	if len(rel) >= 2 && rel[1] == ':' {
		return false
	}
	// 逐段检查：先把反斜杠也当分隔符（zip 里两种都可能出现）
	norm := strings.ReplaceAll(rel, "\\", "/")
	for _, seg := range strings.Split(norm, "/") {
		if seg == ".." {
			return false
		}
	}
	// 双保险：清洗之后不能爬出目标目录
	cleaned := filepath.Clean(filepath.FromSlash(norm))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return false
	}
	// "." 与 "" 清洗后是目录自身：附件必须有文件名
	if cleaned == "." || filepath.Base(cleaned) == "." {
		return false
	}
	return true
}

// unstash 把暂存的原账套放回去，用于恢复失败时回滚。
//
// 顺序很重要：先清掉半成品，再放回原件。反过来会把原件覆盖掉。
func unstash(stash, dbPath, filesDir string) error {
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		_ = os.Remove(p)
	}
	if filesDir != "" {
		_ = os.RemoveAll(filesDir)
	}

	var firstErr error
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		src := filepath.Join(stash, filepath.Base(p))
		if _, e := os.Stat(src); e != nil {
			continue
		}
		if e := os.Rename(src, p); e != nil && firstErr == nil {
			firstErr = e
		}
	}
	if filesDir != "" {
		src := filepath.Join(stash, filepath.Base(filesDir))
		if _, e := os.Stat(src); e == nil {
			if e := os.Rename(src, filesDir); e != nil && firstErr == nil {
				firstErr = e
			}
		}
	}
	// 只有全部放回成功才清掉暂存目录 —— 失败时留着，用户还能手工救
	if firstErr == nil {
		_ = os.RemoveAll(stash)
	}
	return firstErr
}

// pruneOldStashes 清理旧的暂存目录，**保留最新的一份**。
//
// ★ 这里必须留一份，不能全删。
//
// 之前是无条件全删。而「自动回滚也失败」时，用户的原账套就只剩在
// 暂存目录里 —— 代码自己的错误信息正是让他去那里手工拷回来
// （「原件在 %s，可手工拷回」）。全删等于把唯一的救命副本毁掉，
// 而触发路径甚至不需要用户做错什么：一次失败的恢复就够了。
//
// 目录名带时间戳（YYYYMMDD-HHMMSS），所以字典序即时间序。
// 失败静默忽略：清理是尽力而为，清不掉不该阻断恢复。
func pruneOldStashes(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var stashes []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), stashPrefix) {
			stashes = append(stashes, e.Name())
		}
	}
	if len(stashes) <= 1 {
		return // 仅有的那一份可能就是用户最后的退路
	}
	sort.Strings(stashes)
	for _, name := range stashes[:len(stashes)-1] {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			// 删不掉要说出来：磁盘空间和「用户以为清理过了」都是真问题
			fmt.Fprintf(os.Stderr, "[backup] 清理旧暂存目录 %s 失败: %v\n", name, err)
		}
	}
}

// validateDBHash 校验备份清单里的数据库快照指纹。
//
// 缺失或格式不对一律拒绝 —— 这是防止「用任意内容替换用户账套」的那道门。
func validateDBHash(h string) error {
	if h == "" {
		return fmt.Errorf("%w: 备份清单缺少数据库指纹（可能是构造过的归档）", ErrBadArchive)
	}
	if err := attachment.ValidateHash(h); err != nil {
		return fmt.Errorf("%w: 数据库指纹 %q 不是合法的 sha256", ErrBadArchive, h)
	}
	return nil
}

// shortHash 取指纹前 12 位用于报错展示；长度不足时原样返回。
func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// withinDir 报告 target 是否落在 dir 之内（或就是 dir 本身）。
func withinDir(dir, target string) bool {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absTarget)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Inspect 只读取备份的 manifest，不落盘。界面上预览用。
func Inspect(archivePath string) (*Manifest, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("%w: 打开 %s: %v", ErrBadArchive, archivePath, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != manifestName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		m := &Manifest{}
		if err := json.NewDecoder(rc).Decode(m); err != nil {
			return nil, fmt.Errorf("%w: 解析 manifest: %v", ErrBadArchive, err)
		}
		return m, nil
	}
	return nil, fmt.Errorf("%w: 缺少 %s", ErrBadArchive, manifestName)
}

// ---------------------------------------------------------------------------
// SQLite 一致性快照
// ---------------------------------------------------------------------------

// vacuumInto 用 SQLite 的 VACUUM INTO 生成一致性快照。
//
// 相比拷贝文件的好处：
//   - 自动合并 WAL，不丢最新事务
//   - 得到的是数据库页级一致的副本，不会出现「写到一半的页」
//   - 顺带整理碎片，备份文件通常比原库小
func vacuumInto(ctx context.Context, dbPath, dest string) error {
	// 以只读方式打开源库：备份不应该改动源库
	dsn := "file:" + dbPath + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("backup: 打开账套: %w", err)
	}
	defer db.Close()

	// VACUUM INTO 的目标路径不能已存在
	_ = os.Remove(dest)
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		return fmt.Errorf("backup: 生成一致性快照: %w", err)
	}
	return nil
}

// fillBookStats 从快照里读取账套概况写进 manifest。
// 读不到不算错误 —— 备份本身仍然有效。
func fillBookStats(ctx context.Context, snapshot string, m *Manifest) {
	db, err := sql.Open("sqlite", "file:"+snapshot+"?mode=ro")
	if err != nil {
		return
	}
	defer db.Close()

	_ = db.QueryRowContext(ctx, `SELECT company_name, credit_code FROM book WHERE id = 1`).
		Scan(&m.CompanyName, &m.CreditCode)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM voucher`).Scan(&m.VoucherCount)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account`).Scan(&m.AccountCount)

	var from, to sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT MIN(biz_date), MAX(biz_date) FROM ledger_entry`).
		Scan(&from, &to)
	if from.Valid {
		m.PeriodFrom = from.String
	}
	if to.Valid {
		m.PeriodTo = to.String
	}
}

// ---------------------------------------------------------------------------
// 附件收集
// ---------------------------------------------------------------------------

type fileEntry struct {
	abs  string // 绝对路径
	rel  string // 归档内的相对路径（使用 / 分隔）
	hash string // sha256
}

// collectFiles 遍历附件目录，返回清单与总字节数。
//
// 附件目录按内容寻址（.files/<hash前2位>/<hash>），因此文件名本身就是校验和；
// 这里仍然重新计算一遍，既是为了容错（用户可能手工改过文件名），
// 也让 manifest 自带可信的校验依据。
func collectFiles(filesDir string, include bool) ([]fileEntry, int64, error) {
	if !include || filesDir == "" {
		return nil, 0, nil
	}
	info, err := os.Stat(filesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil // 没有附件目录是正常情况
		}
		return nil, 0, fmt.Errorf("backup: 读取附件目录: %w", err)
	}
	if !info.IsDir() {
		return nil, 0, fmt.Errorf("backup: %s 不是目录", filesDir)
	}

	var out []fileEntry
	var total int64
	err = filepath.WalkDir(filesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(filesDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// 跳过临时文件与系统文件
		if strings.HasPrefix(filepath.Base(rel), ".") {
			return nil
		}
		hash, size, err := fileSHA256(path)
		if err != nil {
			return err
		}
		out = append(out, fileEntry{abs: path, rel: rel, hash: hash})
		total += size
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("backup: 遍历附件: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, total, nil
}

// ---------------------------------------------------------------------------
// zip 辅助
// ---------------------------------------------------------------------------

func writeJSON(zw *zip.Writer, name string, v any) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("backup: 写入 %s: %w", name, err)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("backup: 编码 %s: %w", name, err)
	}
	return nil
}

func writeFileToZip(zw *zip.Writer, name, path string) error {
	src, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("backup: 打开 %s: %w", path, err)
	}
	defer src.Close()

	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("backup: 写入 %s: %w", name, err)
	}
	if _, err := io.Copy(w, src); err != nil {
		return fmt.Errorf("backup: 拷贝 %s: %w", path, err)
	}
	return nil
}

func extractOne(zr *zip.ReadCloser, name, dest string) error {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%w: 打开 %s: %v", ErrBadArchive, name, err)
		}
		defer rc.Close()

		out, err := os.Create(dest)
		if err != nil {
			return fmt.Errorf("backup: 创建 %s: %w", dest, err)
		}
		defer out.Close()
		if _, err := io.Copy(out, rc); err != nil {
			return fmt.Errorf("backup: 解压 %s: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("%w: 归档内缺少 %s", ErrBadArchive, name)
}

// stashExisting 把现有的 db 与附件目录改名暂存，返回暂存目录。
//
// ★ 没有东西可暂存时返回空串，**不建目录**。
//
// 原先无论有没有旧账套都先 Mkdir 一个 .restore-stash-xxx。于是
// 「往一个空目录里恢复备份」会在那儿留下一个空目录，而调用方会把
// 它当成「你原来的账都在这儿」报给用户 —— 用户看到的是一个空文件夹，
// 却以为旧数据被安全地留着了。空目录不是退路，是误导。
func stashExisting(dbPath, filesDir string) (string, error) {
	// 先看有没有东西要挪走；没有就什么都不做。
	if !hasAnythingToStash(dbPath, filesDir) {
		return "", nil
	}

	// 清掉更早的暂存：只保留最近一份。
	// 不清的话每次恢复都留下一整套账套 + 附件，几十次之后磁盘就满了，
	// 而用户根本不知道那些隐藏目录是什么。
	dir := filepath.Dir(dbPath)
	pruneOldStashes(dir)

	// ★ 目录名必须唯一，否则同一秒内的两次恢复会撞名。
	//
	// 时间戳只精确到秒。而「保留一份旧暂存」之后，撞名的后果是
	// os.Rename(filesDir, stash/filesDir) 报 "file exists" ——
	// 恢复直接失败。同一秒内连做两次恢复并不罕见：
	// 第一次恢复错了备份，用户立刻再恢复一次正确的，就是这种情况。
	//
	// 用 os.Mkdir 而不是 MkdirAll：只有 Mkdir 会在目录已存在时报错，
	// 我们据此加后缀重试。父目录必然存在（账套就在里面）。
	stamp := time.Now().Format("20060102-150405")
	stash := filepath.Join(dir, stashPrefix+stamp)
	for i := 2; ; i++ {
		err := os.Mkdir(stash, 0o755)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("backup: 创建暂存目录: %w", err)
		}
		if i > 100 {
			return "", fmt.Errorf("backup: 暂存目录 %s 已被占用且无法生成唯一名", stash)
		}
		stash = filepath.Join(dir, fmt.Sprintf("%s%s-%d", stashPrefix, stamp, i))
	}
	moved := false
	restore := func() {
		// 任一 rename 失败时把已移动的放回去
		if !moved {
			return
		}
		_ = os.Rename(filepath.Join(stash, filepath.Base(dbPath)), dbPath)
		if filesDir != "" {
			_ = os.Rename(filepath.Join(stash, filepath.Base(filesDir)), filesDir)
		}
	}

	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := os.Rename(p, filepath.Join(stash, filepath.Base(p))); err != nil {
			restore()
			return "", fmt.Errorf("backup: 暂存 %s: %w", p, err)
		}
		moved = true
	}
	if filesDir != "" {
		if _, err := os.Stat(filesDir); err == nil {
			if err := os.Rename(filesDir, filepath.Join(stash, filepath.Base(filesDir))); err != nil {
				restore()
				return "", fmt.Errorf("backup: 暂存附件目录: %w", err)
			}
			moved = true
		}
	}
	return stash, nil
}

// ---------------------------------------------------------------------------
// 校验和
// ---------------------------------------------------------------------------

// fileSHA256 返回文件的 sha256 十六进制串与字节数。
func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("backup: 打开 %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("backup: 计算校验和 %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// SHA256Of 计算一段字节的 sha256，供附件内容寻址使用。
func SHA256Of(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// BlobPath 返回按内容寻址的附件相对路径：<hash前2位>/<hash>。
//
// 之所以按内容而非按单据组织：
//
//   - 天然去重：同一张发票被多张单据引用只存一份
//   - 不可变：文件内容即文件名，改名/移动单据不会失效
//   - 不冲突：并发上传同名文件不互相覆盖
//   - 可校验：恢复时重算 hash 即可发现损坏
func BlobPath(hash string) string {
	if len(hash) < 2 {
		return hash
	}
	return filepath.ToSlash(filepath.Join(hash[:2], hash))
}

// hasAnythingToStash 判断恢复前是否有需要暂存的文件。
//
// 判据与 stashExisting 实际会挪的东西完全一致（db、-wal、-shm、附件目录）——
// 两处判据不一致就会出现「建了目录但什么都没挪进去」的空暂存。
func hasAnythingToStash(dbPath, filesDir string) bool {
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	if filesDir != "" {
		if fi, err := os.Stat(filesDir); err == nil && fi.IsDir() {
			// 空附件目录也不值得为它留一份「退路」
			if entries, err := os.ReadDir(filesDir); err == nil && len(entries) > 0 {
				return true
			}
		}
	}
	return false
}
