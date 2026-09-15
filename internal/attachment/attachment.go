// Package attachment 管理按内容寻址（content-addressed）的附件文件。
//
// # 为什么放在文件系统而不是数据库
//
// 附件多为发票 PDF、扫描件、报销票据照片，单文件可达数 MB。
// 存进 SQLite 会让库体积迅速膨胀到 GB 级，备份、迁移、日常读写都变慢。
//
// # 为什么按内容寻址
//
// 路径是 `<sha256前2位>/<sha256>`，文件名即内容指纹：
//
//   - **天然去重**：同一张发票被多张单据引用只存一份
//   - **不可变**：内容决定路径，改名/移动单据不会让附件失效
//   - **不会冲突**：并发上传同名文件不互相覆盖
//   - **可校验**：恢复备份时重算 hash 即可发现损坏
//
// 数据库里只存 hash，不存路径 —— 因此账套目录整体移动/改名后附件依然可用。
package attachment

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 附件相关错误。
var (
	ErrTooLarge    = errors.New("attachment: 文件超过大小上限")
	ErrEmpty       = errors.New("attachment: 文件为空")
	ErrNotFound    = errors.New("attachment: 附件不存在")
	ErrBadHash     = errors.New("attachment: 校验和格式非法")
	ErrUnsupported = errors.New("attachment: 不支持的文件类型")
)

// DefaultMaxSize 是单个附件的默认大小上限（20MB）。
//
// 定这个值而不是无限：小微企业常见的是发票 PDF（几百 KB）与
// 票据照片（1~3 MB），20MB 足够覆盖，同时避免用户误传大文件
// 把备份搞得无法传输。
const DefaultMaxSize int64 = 20 << 20

// Options 控制附件仓库行为。
type Options struct {
	// Dir 是附件根目录（账套的 .files 目录）。
	Dir string
	// MaxSize 是单文件字节上限；0 表示用 DefaultMaxSize。
	MaxSize int64
	// AllowedMimes 为空表示不限制类型；否则只接受列出的 MIME。
	AllowedMimes []string
}

// Store 是一个账套的附件仓库。
type Store struct {
	dir     string
	maxSize int64
	allowed map[string]bool
}

// New 打开（必要时创建）附件仓库。
func New(opts Options) (*Store, error) {
	if opts.Dir == "" {
		return nil, errors.New("attachment: 必须指定附件目录")
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("attachment: 创建目录 %s: %w", opts.Dir, err)
	}
	maxSize := opts.MaxSize
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	s := &Store{dir: opts.Dir, maxSize: maxSize}
	if len(opts.AllowedMimes) > 0 {
		s.allowed = map[string]bool{}
		for _, m := range opts.AllowedMimes {
			s.allowed[strings.ToLower(m)] = true
		}
	}
	return s, nil
}

// Dir 返回附件根目录。
func (s *Store) Dir() string { return s.dir }

// Path 返回某个 hash 对应的绝对路径。
//
// 注意：**不要把它存进数据库**。存 hash 就够了，路径由 hash 派生，
// 这样账套目录改名或移动到别的机器后附件仍然能找到。
//
// ★ 返回 error 而不是「先校验再用」。
//
// 原来它是 `Path(hash string) string`，靠调用方自觉先调 ValidateHash。
// 而 Wails 绑定层就是漏了那一步：界面传进来的 hash 直接被拼进路径，
// `../../../../etc/passwd` 能原样返回出去，界面再用系统程序打开它 ——
// 一个「打开附件」的功能变成了任意文件读取。
//
// 「记得先校验」这种约定在跨层调用上迟早会漏；把校验放进唯一
// 的路径构造点，漏掉就编译不过。
func (s *Store) Path(hash string) (string, error) {
	if err := ValidateHash(hash); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, filepath.FromSlash(RelPath(hash))), nil
}

// MustPath 在 hash 已被校验过时使用（如刚从库里读出来的）。
// hash 非法时返回空串 —— 调用方拿到空串自然会把附件标为缺失，
// 而不是拿到一个能指向任意位置的路径。
func (s *Store) MustPath(hash string) string {
	p, err := s.Path(hash)
	if err != nil {
		return ""
	}
	return p
}

// RelPath 返回按内容寻址的相对路径：`<hash前2位>/<hash>`。
//
// 分两级目录是为了避免单目录下堆积几万个文件 ——
// 多数文件系统在此规模下目录遍历会明显变慢。
func RelPath(hash string) string {
	if len(hash) < 2 {
		return hash
	}
	return filepath.ToSlash(filepath.Join(hash[:2], hash))
}

// Blob 是一个已存储的附件。
type Blob struct {
	// SHA256 是内容指纹，也是数据库里唯一需要保存的字段。
	SHA256 string
	Size   int64
	// FileName 是用户可见的原始文件名（仅用于展示与下载时的建议名）。
	FileName string
	Mime     string
}

// Put 保存一段内容，返回其 Blob。
//
// 若相同内容已存在则直接复用（内容寻址的天然去重），
// 因此重复上传同一张发票不会占用额外空间。
func (s *Store) Put(data []byte, fileName string) (*Blob, error) {
	if len(data) == 0 {
		return nil, ErrEmpty
	}
	if int64(len(data)) > s.maxSize {
		return nil, fmt.Errorf("%w: %d 字节 > 上限 %d 字节",
			ErrTooLarge, len(data), s.maxSize)
	}

	// MIME 按内容嗅探而不是看扩展名：扩展名可以随便改，
	// 而内容嗅探能挡住「把可执行文件改名成 .pdf」这类情况。
	mime := http.DetectContentType(data)
	if s.allowed != nil && !s.allowed[lowerMime(mime)] {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, mime)
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	path := s.MustPath(hash) // hash 刚由内容算出来，必然合法

	// 已存在则复用
	if fi, err := os.Stat(path); err == nil && fi.Size() == int64(len(data)) {
		return &Blob{SHA256: hash, Size: fi.Size(), FileName: fileName, Mime: mime}, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("attachment: 创建目录: %w", err)
	}
	// 先写临时文件再改名：避免写入过程中断电留下半截文件，
	// 而半截文件的内容 hash 与文件名不符，会成为永久的坏数据。
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("attachment: 创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 改名成功后这次删除是空操作

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("attachment: 写入: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("attachment: 落盘: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("attachment: 关闭: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return nil, fmt.Errorf("attachment: 改名: %w", err)
	}
	return &Blob{SHA256: hash, Size: int64(len(data)), FileName: fileName, Mime: mime}, nil
}

// Open 打开一个附件的读取流。
func (s *Store) Open(hash string) (io.ReadCloser, error) {
	if err := ValidateHash(hash); err != nil {
		return nil, err
	}
	f, err := os.Open(s.MustPath(hash))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
		}
		return nil, err
	}
	return f, nil
}

// Get 读取附件全部内容，并校验内容与 hash 一致。
func (s *Store) Get(hash string) ([]byte, error) {
	if err := ValidateHash(hash); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.MustPath(hash))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
		}
		return nil, err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != hash {
		return nil, fmt.Errorf("attachment: 文件损坏，内容 hash %s ≠ 文件名 %s", got, hash)
	}
	return data, nil
}

// Exists 报告某个附件是否已存在。
func (s *Store) Exists(hash string) bool {
	if ValidateHash(hash) != nil {
		return false
	}
	_, err := os.Stat(s.MustPath(hash))
	return err == nil
}

// Size 返回某个 hash 的文件大小；文件不在或 hash 非法时返回 0。
//
// 单独一个方法而不是让调用方 Walk 一遍：Walk 要扫整个目录，
// 而「这条证据的文件多大」是列表页每次渲染都要问的问题。
func (s *Store) Size(hash string) int64 {
	fi, err := os.Stat(s.MustPath(hash))
	if err != nil {
		return 0
	}
	return fi.Size()
}

// Delete 删除一个附件的物理文件。
//
// 调用方必须先确认没有其他记录引用它 —— 内容寻址意味着一个文件
// 可能被多条记录共享。
func (s *Store) Delete(hash string) error {
	if err := ValidateHash(hash); err != nil {
		return err
	}
	if err := os.Remove(s.MustPath(hash)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Entry 是遍历附件目录时的一项。
type Entry struct {
	Hash string
	Size int64
}

// Walk 遍历附件目录下的全部物理文件。
//
// 用于「附件清理」：把扫出来的 hash 集合与数据库里被引用的集合比对，
// 就能找出孤儿文件（可回收）与缺失文件（备份不完整）。
func (s *Store) Walk(fn func(Entry) error) error {
	return filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		// 跳过临时文件与隐藏文件
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if ValidateHash(name) != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return fn(Entry{Hash: name, Size: info.Size()})
	})
}

// List 返回全部附件的 hash（升序），便于与数据库比对。
func (s *Store) List() ([]string, error) {
	var out []string
	err := s.Walk(func(e Entry) error {
		out = append(out, e.Hash)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// TotalSize 返回附件占用的总字节数。
func (s *Store) TotalSize() (int64, error) {
	var total int64
	err := s.Walk(func(e Entry) error {
		total += e.Size
		return nil
	})
	return total, err
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// ValidateHash 校验一个 sha256 十六进制串是否合法。
//
// 必须校验：hash 会被拼进文件路径，若不校验，
// 一个精心构造的 "hash"（如 `../../../etc/passwd`）就能读写任意文件。
func ValidateHash(hash string) error {
	if len(hash) != 64 {
		return fmt.Errorf("%w: 长度应为 64，实际 %d", ErrBadHash, len(hash))
	}
	for i := 0; i < len(hash); i++ {
		c := hash[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: 含非十六进制字符 %q", ErrBadHash, c)
		}
	}
	return nil
}

// HashOf 计算一段内容的 sha256 十六进制串。
func HashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func lowerMime(m string) string {
	if i := strings.IndexByte(m, ';'); i > 0 {
		m = m[:i]
	}
	return strings.ToLower(strings.TrimSpace(m))
}

// FriendlyMime 返回便于展示的类型说明。
func FriendlyMime(mime string) string {
	switch lowerMime(mime) {
	case "application/pdf":
		return "PDF 文档"
	case "image/jpeg":
		return "JPEG 图片"
	case "image/png":
		return "PNG 图片"
	case "image/gif":
		return "GIF 图片"
	case "image/webp":
		return "WebP 图片"
	case "text/plain":
		return "文本文件"
	case "application/zip":
		return "压缩包"
	default:
		return mime
	}
}

// IsImage 报告是否为图片（决定界面上能否内联预览）。
func IsImage(mime string) bool {
	return strings.HasPrefix(lowerMime(mime), "image/")
}
