// Package sqlite 提供基于 SQLite 的持久化实现。
//
// # 为什么自己写迁移器
//
// 迁移逻辑只有几十行，而引入 goose / golang-migrate 会多一个依赖树。
// 本工程以「闭源商用」为目标，依赖越少、许可证审计面越小越好
// （见 PROVENANCE.md §2）。因此这里内嵌 SQL 并自行记录版本。
//
// # 与 Frappe Books 迁移机制的区别
//
// Frappe 的 migrate() 会比对 schema 定义与实际表结构，自动 ADD/DROP COLUMN。
// 那份实现有丢数据风险：字段从定义里消失就会被直接 DROP。本工程采用
// **显式、只向前、可审计**的迁移脚本，每个脚本一次性编写并永久保留。
package sqlite

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// nowString 返回统一的时间戳字符串（UTC、RFC3339）。
//
// 全库时间戳一律 UTC；只有 biz_date（会计日期）用 YYYY-MM-DD 的日历日期。
// 这两者绝不混用 —— Frappe Books 把会计日期存成「本地日期对应的 UTC 午夜」，
// 导致日期筛选出现 off-by-one，本项目从存储层就把两者分开。
func nowString() string { return time.Now().UTC().Format(time.RFC3339Nano) }

//go:embed migrations/*.sql
var migrationFS embed.FS

//go:embed seed_accounts.sql
var seedAccountsSQL string

// migration 是一个版本化迁移脚本。
type migration struct {
	Version int
	Name    string
	SQL     string
}

// loadMigrations 读取并排序内嵌的迁移脚本。
// 文件名格式为 NNNN_name.sql。
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("sqlite: 读取迁移目录: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".sql")
		idx := strings.IndexByte(base, '_')
		if idx <= 0 {
			return nil, fmt.Errorf("sqlite: 迁移文件名应为 NNNN_name.sql，实际 %q", e.Name())
		}
		var v int
		if _, err := fmt.Sscanf(base[:idx], "%d", &v); err != nil {
			return nil, fmt.Errorf("sqlite: 迁移版本号非法: %q", e.Name())
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{Version: v, Name: base[idx+1:], SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// migrationTableDDL 是迁移记录表。用 IF NOT EXISTS 保证幂等。
const migrationTableDDL = `
CREATE TABLE IF NOT EXISTS schema_migration (
  version    INTEGER PRIMARY KEY,
  name       TEXT    NOT NULL,
  applied_at TEXT    NOT NULL
);`

// Migrate 按版本顺序执行尚未应用的迁移。可重复调用。
func (db *DB) Migrate(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if _, err := db.sql.ExecContext(ctx, migrationTableDDL); err != nil {
		return fmt.Errorf("sqlite: 建迁移记录表: %w", err)
	}

	applied := map[int]bool{}
	rows, err := db.sql.QueryContext(ctx, `SELECT version FROM schema_migration`)
	if err != nil {
		return fmt.Errorf("sqlite: 读取已应用迁移: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		// 每个迁移单独一个事务：失败则整体回滚，不会留下半套表结构
		err := db.WithTx(ctx, func(tx *Tx) error {
			if _, err := tx.Exec(ctx, m.SQL); err != nil {
				return fmt.Errorf("迁移 %04d_%s 执行失败: %w", m.Version, m.Name, err)
			}
			_, err := tx.Exec(ctx,
				`INSERT INTO schema_migration (version, name, applied_at) VALUES (?, ?, ?)`,
				m.Version, m.Name, nowString())
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// AppliedMigrations 返回已应用的迁移版本号，升序。
func (db *DB) AppliedMigrations(ctx context.Context) ([]int, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT version FROM schema_migration ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 科目种子数据
// ---------------------------------------------------------------------------

// SeedChartOfAccounts 写入《小企业会计准则》预置科目表。
//
// 种子 SQL 由 docs/data/gen_full_coa.py 生成，包含 66 个一级科目
// 与 125 个实务明细科目，共 191 个（见 docs/05-完整科目表说明.md）。
// 编码、余额方向、辅助核算建议全部取自财政部附录原文。
//
// 幂等：已存在任何科目时直接返回，不重复写入。
func (db *DB) SeedChartOfAccounts(ctx context.Context) error {
	var n int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM account`).Scan(&n); err != nil {
		return fmt.Errorf("sqlite: 统计科目: %w", err)
	}
	if n > 0 {
		return nil
	}
	return db.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(ctx, stripTxControl(seedAccountsSQL)); err != nil {
			return fmt.Errorf("sqlite: 写入预置科目表: %w", err)
		}
		return nil
	})
}

// stripTxControl 去掉脚本里的 BEGIN TRANSACTION / COMMIT / END TRANSACTION。
//
// 种子脚本由 docs/data/gen_full_coa.py 生成，自带事务控制以便单独执行；
// 但它在这里是被我们自己的事务包裹着执行的，嵌套 BEGIN 会被 SQLite 拒绝
// （"cannot start a transaction within a transaction"）。
//
// 只按**整行精确匹配**剔除，因此脚本正文里出现这些词不会受影响。
func stripTxControl(script string) string {
	var b strings.Builder
	for _, line := range strings.Split(script, "\n") {
		switch strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(line, ";"))) {
		case "BEGIN TRANSACTION", "BEGIN", "COMMIT", "END TRANSACTION", "END":
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
