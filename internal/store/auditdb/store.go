// Package auditdb 是操作日志的持久化：**独立的** SQLite 文件，按大小分片。
//
// # 为什么与账套分开存
//
//   - 日志要记「谁在什么时候动了哪本账」。存进账套本身的话，
//     把账套恢复到昨天的备份，日志也跟着倒退 —— 恢复这件事就查不出来了。
//   - 账套是要被审计、被拷来拷去的会计档案；日志是运行痕迹，
//     体量与生命周期都不同（用户要求单文件不超过 100MB）。
//   - 账套文件坏了（打不开）时，日志还在 —— 而这正是最需要日志的时候。
//
// # 分片与链
//
// 每个分片是一个独立的 .db 文件，超过上限（默认 100MB）就封存、开新片。
// 序号（seq）跨片连续，哈希链也跨片：
// 新片第一行的 prev_hash = 上一片最后一行的 hash（记在 audit_segment 里）。
// 所以「删掉一整个文件」同样会被校验发现 —— 链断了。
package auditdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"miniaccount/internal/domain/audit"
)

// 默认分片上限：100MB。
//
// 用户明确要求单文件控制在 100MB 以内。SQLite 在这个体量下
// 查询、备份、拷贝都很舒服；再大就会出现「拷一份要等半天」这种体验问题。
const DefaultMaxBytes int64 = 100 * 1024 * 1024

// MinMaxBytes 是分片上限的下限。
//
// ★ SQLite 建完表本身就有几十 KB（页分配 + sqlite_master + 索引）。
// 上限设得比这还小的话，刚建好的空片就已经「超限」，
// 于是每写一行就换一个文件 —— 上万个小文件，比不限制还糟。
// 所以低于这个值一律抬上来。
const MinMaxBytes int64 = 4 * 1024 * 1024

// segmentPrefix 是日志文件名的前缀。
const segmentPrefix = "audit-"

// Store 是操作日志存储。
//
// 并发：日志会被界面与后台任务同时写，内部用一把锁串行化 —— 写入本身
// 是「读链头 + 插入」的复合动作，必须在同一把锁里完成，否则链会分叉。
type Store struct {
	dir      string
	maxBytes int64

	mu   sync.Mutex
	db   *sql.DB
	file string
	// seq 是已写入的最大序号；head 是当前链头哈希。
	seq  int64
	head string
	// bytes 是当前分片的近似大小，避免每次写入都去问文件系统。
	bytes int64
	// readOnly 为真时只读打开已有分片，用于导出/校验。
	readOnly bool
	// permErr 是收紧权限时的错误（非致命，但要能报出来）。
	permErr error
}

// Options 是打开日志存储的参数。
type Options struct {
	// Dir 是日志目录。
	Dir string
	// MaxBytes 是单片上限；<=0 时用 DefaultMaxBytes。
	MaxBytes int64
	// ReadOnly 只读打开（导出、校验用）。
	ReadOnly bool
}

// 权限：日志比账套更要紧。
//
// ★ 账套是「账」，日志是「谁在什么时候动了账」—— 它同时包含
// 操作人姓名、业务摘要、金额、对方户名。同一台电脑多人共用时，
// 一个 0755 的目录等于把这些摊开给所有人看。
//
// 所以：目录 0700（只有本人能进），文件 0600（只有本人能读）。
// 只收紧、不放松 —— 用户自己 chmod 成更宽的话由他决定，
// 我们每次打开都会再收紧一次（这是有意为之：这是安全默认值）。
const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// tighten 把目录与其中的日志文件收紧到 0700 / 0600。
//
// Windows 上 os.Chmod 基本是空操作（只有只读位），所以那里跳过 ——
// 报一个「权限设置失败」的假警告，只会教用户忽略警告。
func tighten(dir string, files []string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	note(os.Chmod(dir, dirPerm))
	for _, f := range files {
		note(os.Chmod(f, filePerm))
	}
	return firstErr
}

// Open 打开（必要时创建）日志存储。
func Open(ctx context.Context, opts Options) (*Store, error) {
	if strings.TrimSpace(opts.Dir) == "" {
		return nil, errors.New("auditdb: 必须指定日志目录")
	}
	max := opts.MaxBytes
	if max <= 0 {
		max = DefaultMaxBytes
	}
	if max < MinMaxBytes {
		max = MinMaxBytes
	}
	s := &Store{dir: opts.Dir, maxBytes: max, readOnly: opts.ReadOnly}
	if !opts.ReadOnly {
		if err := os.MkdirAll(opts.Dir, dirPerm); err != nil {
			return nil, fmt.Errorf("auditdb: 创建日志目录 %s: %w", opts.Dir, err)
		}
	}
	if err := s.openActive(ctx); err != nil {
		return nil, err
	}
	// 每次打开都收紧一次：既保住新建的，也顺手修好早先建的（以及
	// 用户或别的程序改宽过的）。失败不阻断 —— 打不开日志比权限宽更糟，
	// 但要把原因说出来，别让「以为收紧了其实没有」。
	if !opts.ReadOnly {
		if files, ferr := s.files(); ferr == nil {
			s.permErr = tighten(opts.Dir, files)
		}
	}
	return s, nil
}

// PermError 返回收紧权限时的错误（没有则为 nil）。
func (s *Store) PermError() error { return s.permErr }

// Close 关闭当前分片。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// Dir 返回日志目录。
func (s *Store) Dir() string { return s.dir }

// files 返回按文件名排序的分片路径（文件名里的序号是零填充的，可直接排）。
func (s *Store) files() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), segmentPrefix) {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		out = append(out, filepath.Join(s.dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

// segmentNo 从文件名里取出分片序号（audit-0001.db → 1）。
func segmentNo(path string) int {
	name := strings.TrimSuffix(filepath.Base(path), ".db")
	name = strings.TrimPrefix(name, segmentPrefix)
	n := 0
	_, _ = fmt.Sscanf(name, "%d", &n)
	return n
}

// openActive 打开最后一个分片；没有就建第一个。
func (s *Store) openActive(ctx context.Context) error {
	files, err := s.files()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		if s.readOnly {
			return nil
		}
		return s.createSegment(ctx, 1, audit.Genesis(), 0)
	}
	last := files[len(files)-1]
	if err := s.openFile(ctx, last); err != nil {
		return err
	}
	// 从文件里恢复链头与序号 —— 不能靠内存，重启后要接着写
	row := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq),0), COALESCE(MAX(segment_seq),0) FROM audit_log`)
	var seq int64
	var segSeq int64
	if err := row.Scan(&seq, &segSeq); err != nil {
		return fmt.Errorf("auditdb: 读链头失败: %w", err)
	}
	s.seq = seq
	s.head = audit.Genesis()
	var h sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT hash FROM audit_log ORDER BY segment_seq DESC LIMIT 1`).Scan(&h); err == nil && h.Valid {
		s.head = h.String
	}
	if fi, err := os.Stat(last); err == nil {
		s.bytes = fi.Size()
	}
	return nil
}

// createSegment 建一个新分片，并把「上一片的链头」写进元数据。
func (s *Store) createSegment(ctx context.Context, no int, prevHash string, prevSeq int64) error {
	path := filepath.Join(s.dir, fmt.Sprintf("%s%04d.db", segmentPrefix, no))
	// 新文件一开始就用 0600 建：先建后 chmod 之间有一个短暂窗口，
	// 而日志文件一旦被读到，内容就已经泄露出去了。
	if f, ferr := os.OpenFile(path, os.O_CREATE|os.O_RDWR, filePerm); ferr == nil {
		_ = f.Close()
	}
	db, err := openSQLite(path, false)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, filePerm)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return fmt.Errorf("auditdb: 建表失败: %w", err)
	}
	if err := migrateSegment(ctx, db); err != nil {
		_ = db.Close()
		return err
	}
	// 记下本片的起点：prev_hash 是上一片的链头，prev_seq 是上一片的末序号。
	// ★ 这一行是跨片校验的唯一依据 —— 没有它，删掉中间一个文件
	// 会让剩下的两片各自看起来都是完好的。
	_, err = db.ExecContext(ctx, `
		INSERT INTO audit_segment (id, segment_no, prev_hash, prev_seq, opened_at)
		VALUES (1, ?, ?, ?, ?)`,
		no, prevHash, prevSeq, audit.NowStamp(time.Now()))
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("auditdb: 写分片元数据失败: %w", err)
	}
	if s.db != nil {
		_ = s.db.Close()
	}
	s.db = db
	s.file = path
	s.bytes = 0
	// ★ 链状态必须跟着走：新片的第一行要引用上一片的末行哈希。
	// 这里漏了的话，新片第一行的 prev_hash 会是空串 ——
	// 校验时表现为「与上一条接不上」，而那条日志其实完全正常。
	s.head = prevHash
	s.seq = prevSeq
	return nil
}

// migrateSegment 给老分片补上后加的列。
//
// ★ 日志表上有「禁止 UPDATE」的触发器，但 ALTER TABLE ADD COLUMN
// 是 DDL，不触发它 —— 补列是安全的。老记录一律算 business：
// 这个功能之前写的全是业务操作。
func migrateSegment(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(audit_log)`)
	if err != nil {
		return err
	}
	hasCategory := false
	for rows.Next() {
		var (
			cid       int
			name, typ string
			notNull   int
			dflt      sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "category" {
			hasCategory = true
		}
	}
	rows.Close()
	if hasCategory {
		return nil
	}
	_, err = db.ExecContext(ctx,
		`ALTER TABLE audit_log ADD COLUMN category TEXT NOT NULL DEFAULT 'business'`)
	return err
}

// openFile 打开一个已有的分片（只读模式也一样）。
func (s *Store) openFile(ctx context.Context, path string) error {
	db, err := openSQLite(path, s.readOnly)
	if err != nil {
		return err
	}
	if s.db != nil {
		_ = s.db.Close()
	}
	s.db = db
	s.file = path
	return nil
}

// openSQLite 打开日志库。
//
// 与账套库不同：日志不需要 WAL 的高并发（写入是串行的），
// 用默认的 journal 模式 + synchronous(FULL) 更看重「掉电也不丢」——
// 日志丢一条，链条就断了，而断链会被校验发现并报「日志被改动过」，
// 那是个假警报，必须避免。
func openSQLite(path string, readOnly bool) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	if readOnly {
		q.Set("mode", "ro")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("auditdb: 打开 %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("auditdb: 连接 %s: %w", path, err)
	}
	return db, nil
}

// schema 是日志库的表结构。
//
// ★ audit_log 上有 UPDATE / DELETE 触发器，直接 RAISE(ABORT)。
//
// 这就是规范里「安全性：保证日志中的任何信息不被用户以任何手段修改和删除」
// 的技术手段。它不是万能的（拿到文件的人可以绕开 SQLite 直接改字节），
// 所以还有哈希链兜底：改过就一定能被发现。两层加起来才叫「不可篡改」——
// 触发器挡住顺手改，链负责证明有没有被改。
const schema = `
CREATE TABLE IF NOT EXISTS audit_log (
  segment_seq  INTEGER PRIMARY KEY AUTOINCREMENT,
  seq          INTEGER NOT NULL,
  at           TEXT    NOT NULL,
  operator     TEXT    NOT NULL DEFAULT '',
  source       TEXT    NOT NULL DEFAULT '',
  action       TEXT    NOT NULL,
  category     TEXT    NOT NULL DEFAULT 'business',
  summary      TEXT    NOT NULL DEFAULT '',
  entity       TEXT    NOT NULL DEFAULT '',
  entity_id    TEXT    NOT NULL DEFAULT '',
  detail_json  TEXT    NOT NULL DEFAULT '',
  result       TEXT    NOT NULL DEFAULT 'ok',
  message      TEXT    NOT NULL DEFAULT '',
  book         TEXT    NOT NULL DEFAULT '',
  company      TEXT    NOT NULL DEFAULT '',
  app_version  TEXT    NOT NULL DEFAULT '',
  prev_hash    TEXT    NOT NULL,
  hash         TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_seq    ON audit_log(seq);
CREATE INDEX IF NOT EXISTS idx_audit_at     ON audit_log(at);
CREATE INDEX IF NOT EXISTS idx_audit_actor  ON audit_log(operator);
CREATE INDEX IF NOT EXISTS idx_audit_action   ON audit_log(action);
CREATE INDEX IF NOT EXISTS idx_audit_category ON audit_log(category);

CREATE TABLE IF NOT EXISTS audit_segment (
  id          INTEGER PRIMARY KEY CHECK (id = 1),
  segment_no  INTEGER NOT NULL,
  prev_hash   TEXT    NOT NULL,
  prev_seq    INTEGER NOT NULL,
  opened_at   TEXT    NOT NULL
);

CREATE TRIGGER IF NOT EXISTS audit_log_no_update
BEFORE UPDATE ON audit_log
BEGIN
  SELECT RAISE(ABORT, '审计日志不可修改');
END;

CREATE TRIGGER IF NOT EXISTS audit_log_no_delete
BEFORE DELETE ON audit_log
BEGIN
  SELECT RAISE(ABORT, '审计日志不可删除');
END;
`

// Append 追加一条日志，返回补全了 Seq/Hash 的条目。
//
// 写入前会检查分片大小，超过上限就先封存再开新片 —— 链跨片接续。
func (s *Store) Append(ctx context.Context, e audit.Entry) (audit.Entry, error) {
	if s.readOnly {
		return e, errors.New("auditdb: 只读模式不能写入")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		if err := s.openActive(ctx); err != nil {
			return e, err
		}
	}
	if err := s.rotateIfNeeded(ctx); err != nil {
		return e, err
	}

	// ★ 先把类别补全再算哈希。
	//
	// 哈希是对「这一行的内容」算的；如果算的时候 Category 是空串、
	// 存进库里却是默认值 'business'，读回来重算必然对不上 ——
	// 校验会把每一条都报成「被改过」，那是比漏报更糟的假警报。
	e.Category = categoryOf(e)
	e.Seq = s.seq + 1
	e.PrevHash = s.head
	e.Hash = e.ComputeHash(s.head)
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	detail, err := json.Marshal(e.Detail)
	if err != nil {
		// 明细序列化失败不该丢掉这条日志：降级成「明细不可序列化」，
		// 主字段照记。日志的价值在于「发生过什么」，明细是加分项。
		detail = []byte(`{"_error":"明细无法序列化"}`)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO audit_log (seq, at, operator, source, action, category, summary,
			entity, entity_id, detail_json, result, message, book, company,
			app_version, prev_hash, hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.Seq, e.At, e.Operator, string(e.Source), string(e.Action),
		string(categoryOf(e)), e.Summary,
		e.Entity, e.EntityID, string(detail), string(e.Result), e.Message,
		e.Book, e.Company, e.AppVersion, e.PrevHash, e.Hash)
	if err != nil {
		return e, fmt.Errorf("auditdb: 写日志失败: %w", err)
	}
	s.seq = e.Seq
	s.head = e.Hash
	s.bytes += int64(len(detail)) + 256 // 粗估一行开销，够用来触发轮转
	return e, nil
}

// categoryOf 取条目类别，零值时按业务操作算。
func categoryOf(e audit.Entry) audit.Category {
	if e.Category == "" {
		return audit.CategoryBusiness
	}
	return e.Category
}

// rotateIfNeeded 超过上限就封存当前分片、开下一片。
//
// 用「文件实际大小」而不是估算值来判断，避免长跑之后估算漂移
// 导致文件超过上限 —— 用户的要求是硬上限（100MB）。
func (s *Store) rotateIfNeeded(ctx context.Context) error {
	if s.file == "" {
		return nil
	}
	fi, err := os.Stat(s.file)
	if err != nil {
		return nil // 文件不见了也要能继续写
	}
	if fi.Size() < s.maxBytes {
		s.bytes = fi.Size()
		return nil
	}
	// ★ 空片不轮转。
	//
	// 「文件超过上限」与「里面有内容」是两件事：文件大可能只是
	// 表结构与索引占了地方。对空片轮转等于永远写不进东西，
	// 而每次轮转都新建一个文件 —— 写一行换一个文件。
	if s.seq == 0 || !s.hasRows(ctx) {
		s.bytes = fi.Size()
		return nil
	}
	files, err := s.files()
	if err != nil {
		return err
	}
	next := 1
	if len(files) > 0 {
		next = segmentNo(files[len(files)-1]) + 1
	}
	return s.createSegment(ctx, next, s.head, s.seq)
}

// ---------------------------------------------------------------------------
// 查询与校验
// ---------------------------------------------------------------------------

// hasRows 判断当前分片里有没有日志行。
func (s *Store) hasRows(ctx context.Context) bool {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// Query 在所有分片里查日志。
//
// 分片是**从旧到新**扫的：时间范围过滤下先扫到旧片能早点结束；
// 而界面上最常看的是「最近发生了什么」，所以带 Limit 时会先收集再统一排序。
func (s *Store) Query(ctx context.Context, q audit.Query) (*audit.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	files, err := s.files()
	if err != nil {
		return nil, err
	}
	page := &audit.Page{Segments: len(files)}
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			page.Bytes += fi.Size()
		}
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	var all []audit.Entry
	for _, f := range files {
		rows, err := s.queryFile(ctx, f, q)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Seq > all[j].Seq })

	page.Total = len(all)
	if q.Offset > 0 {
		if q.Offset >= len(all) {
			all = nil
		} else {
			all = all[q.Offset:]
		}
	}
	if len(all) > limit {
		all = all[:limit]
		page.Truncated = true
	}
	page.Entries = all
	return page, nil
}

func (s *Store) queryFile(ctx context.Context, path string, q audit.Query) ([]audit.Entry, error) {
	db, err := openSQLite(path, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	var (
		where []string
		args  []any
	)
	if q.Operator != "" {
		where = append(where, "operator = ?")
		args = append(args, q.Operator)
	}
	// 时间比较用字符串：格式固定为 "2006-01-02 15:04:05-07:00"，
	// 同一年内字典序即时间序。跨时区混写时会有偏差，所以界面上
	// 给的范围是按本地时间填的 —— 见 audit.NowStamp 的说明。
	if q.From != "" {
		where = append(where, "at >= ?")
		args = append(args, q.From)
	}
	if q.To != "" {
		where = append(where, "at <= ?")
		args = append(args, q.To)
	}
	if q.Result != "" {
		where = append(where, "result = ?")
		args = append(args, string(q.Result))
	}
	if q.Book != "" {
		where = append(where, "book = ?")
		args = append(args, q.Book)
	}
	if len(q.Actions) > 0 {
		ph := make([]string, len(q.Actions))
		for i, a := range q.Actions {
			ph[i] = "?"
			args = append(args, string(a))
		}
		where = append(where, "action IN ("+strings.Join(ph, ",")+")")
	}
	if q.Text != "" {
		where = append(where, "(summary LIKE ? OR entity_id LIKE ? OR message LIKE ? OR detail_json LIKE ?)")
		like := "%" + q.Text + "%"
		args = append(args, like, like, like, like)
	}

	if len(q.Categories) > 0 {
		ph := make([]string, len(q.Categories))
		for i, c := range q.Categories {
			ph[i] = "?"
			args = append(args, string(c))
		}
		where = append(where, "category IN ("+strings.Join(ph, ",")+")")
	}

	sqlText := `SELECT segment_seq, seq, at, operator, source, action, category,
		summary, entity, entity_id, detail_json, result, message, book, company,
		app_version, prev_hash, hash FROM audit_log`
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	sqlText += " ORDER BY seq"

	rows, err := db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("auditdb: 查询 %s: %w", filepath.Base(path), err)
	}
	defer rows.Close()

	var out []audit.Entry
	for rows.Next() {
		var (
			segSeq int64
			e      audit.Entry
			detail string
		)
		if err := rows.Scan(&segSeq, &e.Seq, &e.At, &e.Operator, &e.Source,
			&e.Action, &e.Category, &e.Summary, &e.Entity, &e.EntityID, &detail,
			&e.Result, &e.Message, &e.Book, &e.Company, &e.AppVersion,
			&e.PrevHash, &e.Hash); err != nil {
			return nil, err
		}
		if detail != "" {
			_ = json.Unmarshal([]byte(detail), &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Verify 校验整条链。
//
// 逐条重算哈希，并检查三条：
//  1. 本行 hash == 重算值（内容没被改）
//  2. 本行 prev_hash == 上一行的 hash（顺序没被换、行没被删）
//  3. 分片的 prev_hash / prev_seq 与上一片的末行对得上（文件没被整片拿走）
//
// 返回的问题只保留前若干条：一旦被改过，后面几乎每一行都会对不上，
// 全列出来反而看不到第一条（而第一条才是关键线索）。
func (s *Store) Verify(ctx context.Context) (*audit.VerifyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	files, err := s.files()
	if err != nil {
		return nil, err
	}
	res := &audit.VerifyResult{OK: true, Files: len(files)}
	const maxIssues = 20

	var (
		wantSeq  int64 = 1
		wantHash       = audit.Genesis()
		// wantHead 是「下一条应当引用的上一行哈希」
		wantHead = audit.Genesis()
	)
	for _, f := range files {
		db, err := openSQLite(f, true)
		if err != nil {
			res.OK = false
			res.Issues = append(res.Issues, audit.VerifyIssue{
				File: filepath.Base(f), Reason: "打不开这个日志文件：" + err.Error()})
			continue
		}
		// 分片元数据：这里能发现「整片被删掉」
		var prevHash string
		var prevSeq, segNo int64
		err = db.QueryRowContext(ctx,
			`SELECT segment_no, prev_hash, prev_seq FROM audit_segment WHERE id = 1`).
			Scan(&segNo, &prevHash, &prevSeq)
		if err != nil {
			res.OK = false
			res.Issues = append(res.Issues, audit.VerifyIssue{
				File: filepath.Base(f), Reason: "分片元数据缺失或损坏"})
		} else if segNo > 1 && (prevHash != wantHash || prevSeq != wantSeq-1) {
			res.OK = false
			res.Issues = append(res.Issues, audit.VerifyIssue{
				Reason: fmt.Sprintf(
					"第 %d 片的起点与上一片接不上（上一片末行 seq=%d hash=%s…）—— "+
						"中间的日志文件可能被删掉或替换过",
					segNo, wantSeq-1, short(wantHash))})
		}

		// 进入新分片时，链头应当是上一片的末行哈希
		wantHead = wantHash

		rows, err := db.QueryContext(ctx, `SELECT seq, at, operator, source, action,
			category, summary, entity, entity_id, detail_json, result, message, book,
			company, app_version, prev_hash, hash FROM audit_log ORDER BY seq`)
		if err != nil {
			res.OK = false
			res.Issues = append(res.Issues, audit.VerifyIssue{
				File: filepath.Base(f), Reason: "读不出来：" + err.Error()})
			_ = db.Close()
			continue
		}
		for rows.Next() {
			var e audit.Entry
			var detail string
			if err := rows.Scan(&e.Seq, &e.At, &e.Operator, &e.Source, &e.Action,
				&e.Category, &e.Summary, &e.Entity, &e.EntityID, &detail, &e.Result,
				&e.Message, &e.Book, &e.Company, &e.AppVersion,
				&e.PrevHash, &e.Hash); err != nil {
				rows.Close()
				_ = db.Close()
				return nil, err
			}
			if detail != "" {
				_ = json.Unmarshal([]byte(detail), &e.Detail)
			}
			res.Checked++
			wantHash = e.Hash
			wantSeq = e.Seq + 1

			if e.Seq != int64(res.Checked) && len(res.Issues) < maxIssues {
				// 序号不连续 = 有记录被删掉（触发器挡不住的直接改库）
				res.OK = false
				res.Issues = append(res.Issues, audit.VerifyIssue{
					Seq: e.Seq, File: filepath.Base(f),
					Reason: fmt.Sprintf("序号不连续：期望 %d，实际 %d —— 有记录被删掉",
						int64(res.Checked), e.Seq)})
			}
			// ① 内容有没有被改
			if e.ComputeHash(e.PrevHash) != e.Hash && len(res.Issues) < maxIssues {
				res.OK = false
				res.Issues = append(res.Issues, audit.VerifyIssue{
					Seq: e.Seq, File: filepath.Base(f),
					Reason: "内容与哈希对不上 —— 这条记录被改过"})
			}
			// ② 与上一行接不接得上（顺序被换、或中间少了一条）
			if e.PrevHash != wantHead && len(res.Issues) < maxIssues {
				res.OK = false
				res.Issues = append(res.Issues, audit.VerifyIssue{
					Seq: e.Seq, File: filepath.Base(f),
					Reason: fmt.Sprintf("与上一条接不上：本条记的上一条哈希是 %s…，"+
						"实际是 %s… —— 中间有记录被删或被换过",
						short(e.PrevHash), short(wantHead))})
			}
			wantHead = e.Hash
		}
		rows.Close()
		_ = db.Close()
	}
	if len(res.Issues) > maxIssues {
		res.Issues = res.Issues[:maxIssues]
	}
	res.Head = s.head
	if res.Head == "" {
		res.Head = audit.Genesis()
	}
	return res, nil
}

func short(h string) string {
	if len(h) <= 8 {
		return h
	}
	return h[:8]
}
