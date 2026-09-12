package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // 纯 Go 驱动，无 CGO
)

// 数据库层错误。上层用 errors.Is 判断，不依赖驱动措辞。
var (
	ErrClosed     = errors.New("sqlite: 数据库已关闭")
	ErrNotFound   = errors.New("sqlite: 记录不存在")
	ErrConflict   = errors.New("sqlite: 唯一约束冲突")
	ErrCheckFail  = errors.New("sqlite: 约束校验失败")
	ErrForeignKey = errors.New("sqlite: 外键约束失败")
)

// DB 包装一个 SQLite 连接。
//
// 单用户桌面应用刻意使用**单连接**：SQLite 的写操作本来就是串行的，
// 开放连接池只会制造 SQLITE_BUSY。单连接还有一个关键好处 ——
// 「事务内先查后写」不会与其他事务交错，这是凭证字号分配正确性的前提
// （另一重保障是 voucher 表上的唯一索引）。
type DB struct {
	// sql 在 Open 时写入一次，此后**永不改动**。
	//
	// ★ 这一点是刻意的：Close 以前会把它置 nil，而所有仓储都用
	// `r.db.sql` 直接读它 —— 于是「切换账套时界面正在刷新」
	// 会让读者拿到 nil 的 *sql.DB，`database/sql` 随即空指针崩溃
	// （实测 0.05 秒内 12 次 panic）。不置 nil 就没有这个写，
	// 也就没有数据竞争，读者最多拿到一个已关闭的句柄，
	// database/sql 会规规矩矩地返回 ErrClosed 而不是崩。
	sql  *sql.DB
	path string

	mu     sync.RWMutex
	closed bool
}

// Options 控制打开行为。
type Options struct {
	// Path 是数据库文件路径。":memory:" 表示内存库（测试用）。
	Path string
	// ReadOnly 以只读方式打开（用于备份校验、报表只读连接）。
	ReadOnly bool
	// CreateDirs 为真时自动创建父目录。
	CreateDirs bool
}

// Open 打开（必要时创建）一个 SQLite 数据库。
func Open(ctx context.Context, opts Options) (*DB, error) {
	path := opts.Path
	if path == "" {
		return nil, errors.New("sqlite: 必须指定数据库路径")
	}
	if path != ":memory:" && opts.CreateDirs {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("sqlite: 创建目录 %s: %w", dir, err)
			}
		}
	}

	sdb, err := sql.Open("sqlite", buildDSN(path, opts.ReadOnly))
	if err != nil {
		return nil, fmt.Errorf("sqlite: 打开 %s: %w", path, err)
	}
	sdb.SetMaxOpenConns(1)
	sdb.SetMaxIdleConns(1)
	sdb.SetConnMaxLifetime(0)

	db := &DB{sql: sdb, path: path}
	if err := sdb.PingContext(ctx); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("sqlite: 连接 %s: %w", path, err)
	}
	return db, nil
}

// buildDSN 构造 modernc.org/sqlite 的连接串。
//
// 关键参数：
//
//	_txlock=immediate  让 BeginTx 使用 BEGIN IMMEDIATE，事务一开始就取写锁。
//	                   默认的 DEFERRED 会在「先读后写」时出现升级锁失败，
//	                   导致凭证字号分配偶发 SQLITE_BUSY。
//	foreign_keys(1)    SQLite 默认不启用外键，必须逐连接打开。
//	busy_timeout       遇锁等待而非立即报错。
//	journal_mode(WAL)  读写并发更好，且崩溃恢复更安全。
func buildDSN(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if readOnly {
		q.Set("mode", "ro")
	} else {
		q.Set("_txlock", "immediate")
	}
	if path == ":memory:" {
		// 共享缓存内存库，保证同一进程内看到同一份数据
		return "file::memory:?cache=shared&" + q.Encode()
	}
	// ★ "file:" 开头的路径交给驱动原样解释，用于**各自独立**的内存库。
	//
	// 这个分支是给测试用的：":memory:" 映射成 `file::memory:?cache=shared`，
	// 那是**进程级共享**的一份库 —— 同一个包里建两次账套，第二次会撞
	// 「账套已存在」，两个测试实际上在操作同一份数据。
	// 传 "file:testdb_7?mode=memory&cache=shared" 就得到各自独立的一份。
	if strings.HasPrefix(path, "file:") {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		return path + sep + q.Encode()
	}
	return "file:" + path + "?" + q.Encode()
}

// Path 返回数据库文件路径。
func (db *DB) Path() string { return db.path }

// Close 关闭数据库。可重复调用。
func (db *DB) Close() error {
	db.mu.Lock()
	if db.closed {
		db.mu.Unlock()
		return ErrClosed
	}
	db.closed = true
	h := db.sql
	db.mu.Unlock()

	// 在锁外关：sql.DB.Close 会等在用连接归还，
	// 持锁等待会把并发的所有调用一起冻住。
	return h.Close()
}

// conn 返回底层句柄；已关闭时返回 ErrClosed。
//
// 所有需要「确认还开着」的路径都走它，而不是直接读 db.sql ——
// 判断与使用之间必须没有窗口，否则又是一次 TOCTOU。
func (db *DB) conn() (*sql.DB, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed || db.sql == nil {
		return nil, ErrClosed
	}
	return db.sql, nil
}

// SQL 暴露底层 *sql.DB，供报表模块做只读查询。
// **写操作一律走 WithTx**，以保证事务性。
//
// 已关闭时返回 nil —— 调用方应当把它当作「查不到数据」而不是崩溃。
// 返回 error 会改变所有调用方的签名，而它们的语义本来就是只读查询。
func (db *DB) SQL() *sql.DB {
	h, _ := db.conn()
	return h
}

// ---------------------------------------------------------------------------
// 事务
// ---------------------------------------------------------------------------

// querier 是 *sql.DB 与 *sql.Tx 的公共接口，让仓储代码可以
// 同时用于「事务内」与「事务外」两种场景而不必写两遍。
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Tx 是一个写事务句柄。
type Tx struct {
	q querier
}

// Exec 执行不返回结果集的语句。
func (t *Tx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := t.q.ExecContext(ctx, query, args...)
	return res, translateErr(err)
}

// Query 执行返回多行的查询；调用方负责关闭 rows。
func (t *Tx) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := t.q.QueryContext(ctx, query, args...)
	return rows, translateErr(err)
}

// QueryRow 执行返回单行的查询。
func (t *Tx) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return t.q.QueryRowContext(ctx, query, args...)
}

// WithTx 在一个事务内执行 fn：fn 返回 nil 则提交，否则回滚。
//
// 所有写操作都必须经由它。这是本项目相对 Frappe Books 的关键修复之一：
// 对方过账一张发票时**逐条 insert 总账分录且没有事务**，
// 中途失败（磁盘满、进程被杀、约束冲突）会留下「半张凭证的总账」，
// 而且没有任何机制能发现或修复它。
func (db *DB) WithTx(ctx context.Context, fn func(tx *Tx) error) (err error) {
	h, cerr := db.conn()
	if cerr != nil {
		return cerr
	}
	tx, err := h.BeginTx(ctx, nil) // DSN 里的 _txlock=immediate 使其为 BEGIN IMMEDIATE
	if err != nil {
		return translateErr(err)
	}

	// ★ 回滚必须挂在 defer 上，不能只在 error 路径回滚。
	//
	// fn 里一旦 panic（越界、nil 解引用、map 写入……），原来那份实现
	// 会把 *sql.Tx 直接遗弃。而连接池只有 1 条连接、事务又是
	// BEGIN IMMEDIATE 的写事务 —— 那条唯一的连接就此卡住，
	// **此后每一次数据库调用都会永久挂起**（不是报错，是挂起）。
	// 更糟的是 desktop 层的 recoverTo 会兜住 panic 让进程活着，
	// 于是「本该崩溃」变成了「界面静默卡死」。
	done := false
	defer func() {
		if !done {
			_ = tx.Rollback()
		}
	}()

	if err = fn(&Tx{q: tx}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return translateErr(err)
	}
	done = true
	return nil
}

// Read 在一个只读事务内执行 fn，保证多条查询看到同一份快照。
// 报表需要这种一致性。
func (db *DB) Read(ctx context.Context, fn func(q Querier) error) error {
	if db.sql == nil {
		return ErrClosed
	}
	h, cerr := db.conn()
	if cerr != nil {
		return cerr
	}
	tx, err := h.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return translateErr(err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(&Tx{q: tx})
}

// Querier 是只读查询接口，供报表模块使用。
type Querier interface {
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) *sql.Row
}

// ---------------------------------------------------------------------------
// 错误翻译
// ---------------------------------------------------------------------------

// translateErr 把 SQLite 的原始错误翻译成可识别的哨兵错误。
//
// Frappe Books 用 JS 正则匹配错误信息来判断约束类型，脆弱且依赖驱动措辞。
// 这里做一次性翻译，上层只需 errors.Is(err, ErrConflict)。
func translateErr(err error) error {
	if err == nil {
		return nil
	}
	// ★ 驱动层的「没有行」要翻成存储层的 ErrNotFound。
	//
	// 不翻的话，界面拿到的是裸的 `sql: no rows in result set` ——
	// 这句话对会计毫无意义，而且分类函数认不出它，
	// 于是一律归成「内部错误」，用户以为软件坏了。
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, err.Error())
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"):
		return fmt.Errorf("%w: %s", ErrConflict, msg)
	case strings.Contains(msg, "FOREIGN KEY constraint failed"):
		return fmt.Errorf("%w: %s", ErrForeignKey, msg)
	case strings.Contains(msg, "CHECK constraint failed"):
		return fmt.Errorf("%w: %s", ErrCheckFail, msg)
	default:
		return err
	}
}

// ---------------------------------------------------------------------------
// 列读写辅助
// ---------------------------------------------------------------------------

// nullInt64 把 *int64 写入可空列。
func nullInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// toNullInt64 把可空列读成 *int64。
func toNullInt64(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// nullString 把 *string 写入可空列。
func nullString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// toNullString 把可空列读成 *string。
func toNullString(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}
