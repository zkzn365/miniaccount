package sqlite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ★ 回调 panic 不能把唯一那条连接占死。
//
// 连接池是 MaxOpenConns(1)，事务又是 BEGIN IMMEDIATE 的写事务。
// 原来只在 error 路径回滚，panic 时 *sql.Tx 被遗弃 ——
// 那条唯一的连接就此卡住，**此后每一次数据库调用都永久挂起**
// （不是报错，是挂起；desktop 的 recoverTo 还会兜住 panic 让进程活着，
// 于是变成界面静默卡死）。
func TestWithTxRollsBackOnPanic(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	func() {
		defer func() { _ = recover() }() // 模拟 desktop 层的 recoverTo
		_ = db.WithTx(ctx, func(tx *Tx) error {
			if _, err := tx.Exec(ctx,
				`INSERT INTO contact (kind, name, created_at, updated_at)
				 VALUES ('customer','panic 前写入', ?, ?)`,
				nowString(), nowString()); err != nil {
				return err
			}
			panic("模拟领域层 panic")
		})
	}()

	// ★ 关键：panic 之后数据库必须仍然可用。
	// 用一个短超时，卡住时给出「挂起」而不是让整个测试超时。
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var n int
	if err := db.sql.QueryRowContext(cctx,
		`SELECT COUNT(*) FROM contact WHERE name = 'panic 前写入'`).Scan(&n); err != nil {
		t.Fatalf("★ panic 之后连接被占死，查询失败/挂起：%v", err)
	}
	if n != 0 {
		t.Errorf("panic 前写入的行应被回滚，实际还剩 %d 行", n)
	}
	// 事务也必须还能开
	if err := db.WithTx(cctx, func(tx *Tx) error { return nil }); err != nil {
		t.Fatalf("★ panic 之后无法再开事务：%v", err)
	}
}

// ★ Close 与并发读不能打架。
//
// 原来 Close 会把 db.sql 置 nil，而所有仓储都直接读这个字段 ——
// 「切换账套时界面正在刷新」就会让读者拿到 nil 的 *sql.DB，
// database/sql 随即空指针崩溃。
func TestCloseDoesNotRaceWithReaders(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 4 个读者持续查询
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// 拿到的可能是 nil（已关闭），但**绝不能 panic**
				h := db.SQL()
				if h == nil {
					continue
				}
				var n int
				_ = h.QueryRowContext(ctx, `SELECT COUNT(*) FROM account`).Scan(&n)
			}
		}()
	}

	// 写者反复关闭
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = db.Close()
			time.Sleep(time.Millisecond)
		}
		close(stop)
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("并发 Close 与读取卡住了")
	}
}

// Close 必须幂等，且关闭后返回 ErrClosed 而不是崩。
func TestCloseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("首次 Close 失败: %v", err)
	}
	if err := db.Close(); !errors.Is(err, ErrClosed) {
		t.Errorf("重复 Close 应返回 ErrClosed，实际 %v", err)
	}
	if err := db.WithTx(ctx, func(tx *Tx) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Errorf("关闭后开事务应返回 ErrClosed，实际 %v", err)
	}
	if err := db.Read(ctx, func(q Querier) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Errorf("关闭后只读事务应返回 ErrClosed，实际 %v", err)
	}
	if db.SQL() != nil {
		t.Error("关闭后 SQL() 应返回 nil")
	}
}
