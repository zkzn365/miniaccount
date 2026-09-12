package attachment

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 造一段像 PDF 的内容（http.DetectContentType 按魔数嗅探）。
func fakePDF(marker string) []byte {
	return append([]byte("%PDF-1.4\n"), []byte(marker)...)
}

func fakePNG(marker string) []byte {
	// PNG 魔数
	return append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte(marker)...)
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(Options{Dir: filepath.Join(t.TempDir(), ".files")})
	if err != nil {
		t.Fatalf("创建附件仓库失败: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// 存取与内容寻址
// ---------------------------------------------------------------------------

func TestPutAndGet(t *testing.T) {
	s := newStore(t)
	data := fakePDF("增值税专用发票")

	blob, err := s.Put(data, "发票.pdf")
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if len(blob.SHA256) != 64 {
		t.Errorf("hash 长度 = %d，期望 64", len(blob.SHA256))
	}
	if blob.Size != int64(len(data)) {
		t.Errorf("大小 = %d，期望 %d", blob.Size, len(data))
	}
	if blob.FileName != "发票.pdf" {
		t.Errorf("文件名 = %q", blob.FileName)
	}
	if !strings.Contains(blob.Mime, "pdf") {
		t.Errorf("MIME = %q，期望识别为 PDF", blob.Mime)
	}

	got, err := s.Get(blob.SHA256)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("读回的内容与写入不一致")
	}
}

// ★ 内容寻址的核心特性：相同内容只存一份
func TestContentAddressingDeduplicates(t *testing.T) {
	s := newStore(t)
	data := fakePDF("同一张发票")

	b1, err := s.Put(data, "发票A.pdf")
	if err != nil {
		t.Fatal(err)
	}
	b2, err := s.Put(data, "发票B.pdf") // 不同文件名、相同内容
	if err != nil {
		t.Fatal(err)
	}
	if b1.SHA256 != b2.SHA256 {
		t.Errorf("相同内容应得到相同 hash：%s vs %s", b1.SHA256, b2.SHA256)
	}

	// 磁盘上只有一个文件
	files, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("物理文件数 = %d，期望 1（已去重）", len(files))
	}
}

func TestDifferentContentDifferentHash(t *testing.T) {
	s := newStore(t)
	b1, _ := s.Put(fakePDF("发票一"), "a.pdf")
	b2, _ := s.Put(fakePDF("发票二"), "b.pdf")
	if b1.SHA256 == b2.SHA256 {
		t.Error("不同内容应得到不同 hash")
	}
	files, _ := s.List()
	if len(files) != 2 {
		t.Errorf("物理文件数 = %d，期望 2", len(files))
	}
}

// ★ 路径分两级目录，避免单目录堆积几万个文件
func TestPathSharding(t *testing.T) {
	s := newStore(t)
	blob, _ := s.Put(fakePDF("x"), "x.pdf")

	want := blob.SHA256[:2] + "/" + blob.SHA256
	if got := RelPath(blob.SHA256); got != want {
		t.Errorf("RelPath = %q，期望 %q", got, want)
	}
	// 实际路径应真的落在分片目录下
	dir := filepath.Dir(s.MustPath(blob.SHA256))
	if filepath.Base(dir) != blob.SHA256[:2] {
		t.Errorf("分片目录 = %q，期望 %q", filepath.Base(dir), blob.SHA256[:2])
	}
}

// 账套目录整体移动后附件仍然能找到（因为只存 hash 不存绝对路径）
func TestMovingStoreDirectoryKeepsWorking(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "旧目录", ".files")
	s1, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	data := fakePDF("发票")
	blob, _ := s1.Put(data, "发票.pdf")

	// 模拟账套整体改名
	newDir := filepath.Join(root, "新目录", ".files")
	if err := os.MkdirAll(filepath.Dir(newDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, newDir); err != nil {
		t.Fatal(err)
	}
	s2, err := New(Options{Dir: newDir})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.Get(blob.SHA256)
	if err != nil {
		t.Fatalf("移动后应仍能读取: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("移动后内容不一致")
	}
}

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

// ★ 文件被篡改必须被发现 —— 内容 hash 与文件名不符
func TestCorruptionDetected(t *testing.T) {
	s := newStore(t)
	blob, _ := s.Put(fakePDF("原始内容"), "发票.pdf")

	// 绕过 Store 直接改文件（模拟磁盘损坏或人为篡改）
	if err := os.WriteFile(s.MustPath(blob.SHA256), fakePDF("被改过的内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(blob.SHA256); err == nil {
		t.Fatal("内容被篡改应报错")
	}
}

// ★ hash 会被拼进文件路径，必须严格校验，否则可读写任意文件
func TestValidateHashRejectsPathTraversal(t *testing.T) {
	bad := []string{
		"",
		"abc",
		"../../../etc/passwd",
		strings.Repeat("g", 64), // 非十六进制
		strings.Repeat("A", 64), // 大写不接受
		strings.Repeat("a", 63), // 长度不足
		strings.Repeat("a", 65), // 长度超
		"..%2f..%2fetc%2fpasswd" + strings.Repeat("a", 40),
	}
	for _, h := range bad {
		if err := ValidateHash(h); err == nil {
			t.Errorf("ValidateHash(%q) 应报错", h)
		}
	}
	// 合法
	if err := ValidateHash(strings.Repeat("a", 64)); err != nil {
		t.Errorf("合法的 64 位十六进制串不应报错: %v", err)
	}
}

// 路径穿越的 hash 不能读到目录外的文件
func TestPathTraversalBlocked(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("机密"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Dir: filepath.Join(root, ".files")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("../../secret.txt"); err == nil {
		t.Fatal("路径穿越应被拒绝")
	}
	if _, err := s.Open("../../secret.txt"); err == nil {
		t.Fatal("Open 也应拒绝路径穿越")
	}
	if s.Exists("../../secret.txt") {
		t.Fatal("Exists 应返回 false")
	}
}

// ---------------------------------------------------------------------------
// 限制
// ---------------------------------------------------------------------------

func TestSizeLimit(t *testing.T) {
	s, err := New(Options{Dir: t.TempDir(), MaxSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 101)
	if _, err := s.Put(big, "big.bin"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("超限应报 ErrTooLarge，得到 %v", err)
	}
	// 恰好等于上限应允许
	ok := make([]byte, 100)
	ok[0] = 'x'
	if _, err := s.Put(ok, "ok.bin"); err != nil {
		t.Errorf("等于上限应允许，得到 %v", err)
	}
}

func TestEmptyFile(t *testing.T) {
	s := newStore(t)
	if _, err := s.Put(nil, "empty"); !errors.Is(err, ErrEmpty) {
		t.Errorf("空文件应报 ErrEmpty，得到 %v", err)
	}
}

// MIME 按内容嗅探，不看扩展名 —— 挡住「把别的文件改名成 .pdf」
func TestMimeSniffedNotFromExtension(t *testing.T) {
	s := newStore(t)
	// PNG 内容，但取名 .pdf
	blob, err := s.Put(fakePNG("图片"), "其实是图片.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(blob.Mime, "png") {
		t.Errorf("MIME = %q，应嗅探出 png 而不是按扩展名当 pdf", blob.Mime)
	}
}

func TestAllowedMimes(t *testing.T) {
	s, err := New(Options{
		Dir:          t.TempDir(),
		AllowedMimes: []string{"application/pdf", "image/png", "image/jpeg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(fakePDF("发票"), "a.pdf"); err != nil {
		t.Errorf("PDF 应被允许: %v", err)
	}
	// zip 内容不在白名单
	zipData := []byte{0x50, 0x4B, 0x03, 0x04}
	zipData = append(zipData, make([]byte, 100)...)
	if _, err := s.Put(zipData, "a.zip"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("白名单外的类型应被拒绝，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 遍历与清理
// ---------------------------------------------------------------------------

func TestWalkAndList(t *testing.T) {
	s := newStore(t)
	for i, name := range []string{"a.pdf", "b.pdf", "c.png"} {
		data := fakePDF(string(rune('a' + i)))
		if strings.HasSuffix(name, ".png") {
			data = fakePNG("c")
		}
		if _, err := s.Put(data, name); err != nil {
			t.Fatal(err)
		}
	}
	files, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("文件数 = %d，期望 3", len(files))
	}
	// 升序
	for i := 1; i < len(files); i++ {
		if files[i] < files[i-1] {
			t.Error("List 应按 hash 升序返回")
		}
	}

	var total int64
	if err := s.Walk(func(e Entry) error {
		if e.Size <= 0 {
			t.Errorf("大小应为正: %+v", e)
		}
		total += e.Size
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.TotalSize()
	if err != nil {
		t.Fatal(err)
	}
	if got != total {
		t.Errorf("TotalSize = %d，与 Walk 累计 %d 不一致", got, total)
	}
}

// 遍历应跳过临时文件与隐藏文件（否则写入中途会被误当成附件）
func TestWalkSkipsTempAndHidden(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(fakePDF("真附件"), "a.pdf"); err != nil {
		t.Fatal(err)
	}
	// 手工造临时文件与隐藏文件
	os.WriteFile(filepath.Join(dir, ".tmp-123"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "notahash.txt"), []byte("x"), 0o644)

	files, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("文件数 = %d，期望 1（临时/隐藏/非 hash 文件应跳过）：%v", len(files), files)
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)
	blob, _ := s.Put(fakePDF("待删"), "x.pdf")
	if !s.Exists(blob.SHA256) {
		t.Fatal("前置条件：文件应存在")
	}
	if err := s.Delete(blob.SHA256); err != nil {
		t.Fatal(err)
	}
	if s.Exists(blob.SHA256) {
		t.Error("删除后应不存在")
	}
	// 重复删除不报错（幂等）
	if err := s.Delete(blob.SHA256); err != nil {
		t.Errorf("重复删除应幂等，得到 %v", err)
	}
	// 非法 hash
	if err := s.Delete("bad"); err == nil {
		t.Error("非法 hash 应报错")
	}
}

func TestOpenNotFound(t *testing.T) {
	s := newStore(t)
	missing := strings.Repeat("a", 64)
	if _, err := s.Open(missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的附件应报 ErrNotFound，得到 %v", err)
	}
	if _, err := s.Get(missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get 也应报 ErrNotFound，得到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func TestHashOf(t *testing.T) {
	a := HashOf([]byte("同一内容"))
	b := HashOf([]byte("同一内容"))
	c := HashOf([]byte("不同内容"))
	if a != b {
		t.Error("相同内容应得到相同 hash")
	}
	if a == c {
		t.Error("不同内容应得到不同 hash")
	}
	if len(a) != 64 {
		t.Errorf("hash 长度 = %d", len(a))
	}
}

func TestFriendlyMime(t *testing.T) {
	cases := map[string]string{
		"application/pdf": "PDF 文档",
		"image/jpeg":      "JPEG 图片",
		"image/png":       "PNG 图片",
		"application/zip": "压缩包",
		"unknown/type":    "unknown/type",
	}
	for in, want := range cases {
		if got := FriendlyMime(in); got != want {
			t.Errorf("FriendlyMime(%q) = %q，期望 %q", in, got, want)
		}
	}
	// 带 charset 的应被归一化
	if got := FriendlyMime("text/plain; charset=utf-8"); got != "文本文件" {
		t.Errorf("带参数的 MIME 应归一化，得到 %q", got)
	}
}

func TestIsImage(t *testing.T) {
	if !IsImage("image/png") || !IsImage("image/jpeg") {
		t.Error("图片类型应识别为图片")
	}
	if IsImage("application/pdf") {
		t.Error("PDF 不应识别为图片")
	}
}

func TestNewRequiresDir(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Error("未指定目录应报错")
	}
}
