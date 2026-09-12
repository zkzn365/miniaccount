package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 只看一眼：这个文件是不是账套、是哪家公司的
// ---------------------------------------------------------------------------
//
// ★ 用**只读**连接，而且**绝不跑迁移**。
//
// 启动时要列出账套目录里的文件，逐个判断「是不是账套」。
// 如果这里复用正常的 Open，就会对那些根本不是账套的文件
// （用户误放的备份、别的软件的库、空文件）执行建表 ——
// 等于我们主动往用户的文件里写东西。列个目录而已，不该有副作用。
//
// 只读打开还有一个好处：账套正在被另一个进程使用时（比如用户开了两个窗口），
// 这里读得到、不会抢锁、也不会把 WAL 搅乱。

// BookPeek 是一个账套文件的外观信息。
type BookPeek struct {
	// Path 是文件的绝对路径。
	Path string `json:"path"`
	// FileName 是文件名，界面直接显示。
	FileName string `json:"fileName"`
	// IsBook 为真表示这个文件确实是一个账套（有 book 表且有记录）。
	IsBook bool `json:"isBook"`
	// CompanyName 等单位信息仅在 IsBook 为真时有意义。
	CompanyName string `json:"companyName"`
	CreditCode  string `json:"creditCode"`
	VATStatus   string `json:"vatStatus"`
	// StartPeriod 是启用期间，如 2025-01。
	StartPeriod string `json:"startPeriod"`
	// VoucherCount 是凭证张数 —— 用户靠它分辨「哪本是正式账」。
	VoucherCount int `json:"voucherCount"`
	// SizeBytes / ModTime 用于排序与显示。
	SizeBytes int64  `json:"sizeBytes"`
	ModTime   string `json:"modTime"`
	// Err 说明为什么这个文件不能当账套用（不是账套/读不了/版本太新）。
	// 有值时 IsBook 必为假。
	Err string `json:"err,omitempty"`
}

// PeekBook 只读地看一眼某个文件是不是账套。
//
// 任何失败都写进返回值的 Err 字段而不是返回 error：
// 列目录时一个坏文件不该让整个列表失败 —— 用户还要打开别的好账套。
func PeekBook(ctx context.Context, path string) BookPeek {
	p := BookPeek{Path: path, FileName: filepath.Base(path)}

	fi, err := os.Stat(path)
	if err != nil {
		p.Err = "读不到这个文件：" + err.Error()
		return p
	}
	if fi.IsDir() {
		p.Err = "这是一个目录，不是账套文件"
		return p
	}
	p.SizeBytes = fi.Size()
	p.ModTime = fi.ModTime().Format(time.RFC3339)

	if fi.Size() == 0 {
		p.Err = "空文件（还没建过账）"
		return p
	}

	db, err := Open(ctx, Options{Path: path, ReadOnly: true})
	if err != nil {
		p.Err = "打不开：" + err.Error()
		return p
	}
	defer func() { _ = db.Close() }()

	if !db.hasTable(ctx, "book") {
		p.Err = "不是账套文件（没有 book 表）"
		return p
	}

	var (
		name, credit, vat     sql.NullString
		startYear, startMonth sql.NullInt64
		setup                 sql.NullInt64
		vouchers              int
	)
	err = db.sql.QueryRowContext(ctx, `
		SELECT company_name, credit_code, vat_status, start_year, start_month,
		       COALESCE(setup_complete, 0)
		  FROM book WHERE id = 1`).
		Scan(&name, &credit, &vat, &startYear, &startMonth, &setup)
	if errors.Is(err, sql.ErrNoRows) {
		p.Err = "还是空库（建账没做完）"
		return p
	}
	if err != nil {
		p.Err = "读不出账套信息：" + err.Error()
		return p
	}

	p.IsBook = true
	p.CompanyName = name.String
	p.CreditCode = credit.String
	p.VATStatus = vat.String
	if startYear.Valid && startMonth.Valid {
		p.StartPeriod = fmt.Sprintf("%04d-%02d", startYear.Int64, startMonth.Int64)
	}
	// 凭证数读不出来不算「不是账套」：老版本可能没有这张表
	_ = db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM voucher`).Scan(&vouchers)
	p.VoucherCount = vouchers
	return p
}

// hasTable 判断库里有没有某张表。
func (db *DB) hasTable(ctx context.Context, name string) bool {
	var n int
	err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		name).Scan(&n)
	return err == nil && n > 0
}

// ScanBookDir 列出目录下所有 .db 文件的外观信息，按修改时间倒序。
//
// 目录不存在时返回空列表而不是错误：第一次使用软件时那个目录还没建，
// 这不是异常。
func ScanBookDir(ctx context.Context, dir string) ([]BookPeek, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, translateErr(err)
	}
	var out []BookPeek
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		// 只看 .db；WAL/SHM 边车文件不能当账套列出来
		if !strings.EqualFold(filepath.Ext(e.Name()), ".db") {
			continue
		}
		out = append(out, PeekBook(ctx, filepath.Join(dir, e.Name())))
	}
	sort.SliceStable(out, func(i, j int) bool {
		// 账套在前；同类按修改时间倒序（最近在用的更可能是要打开的那个）
		if out[i].IsBook != out[j].IsBook {
			return out[i].IsBook
		}
		return out[i].ModTime > out[j].ModTime
	})
	return out, nil
}
