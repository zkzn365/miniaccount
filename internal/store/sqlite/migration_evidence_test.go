package sqlite

import (
	"context"
	"strings"
	"testing"
)

// 0013 迁移把 attachment 表重建了一遍（SQLite 改不了 CHECK 约束）。
//
// ★ 重建表最危险的地方不是「新结构对不对」，而是**旧数据还在不在**：
// 用户的账套里已经挂着成千上万份附件（发票、报销单、凭证），
// 迁移写错一个列名，它们就没了 —— 而文件实体还在磁盘上，
// 表现是「附件列表全空了」。
//
// 所以这条测试走的是**真实升级路径**：先把库还原成旧形态
// （旧的 CHECK、有数据的 attachment），删掉迁移记录，再跑一次迁移。
func TestMigration0013EvidenceKeepsAttachments(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	// ---- 1. 还原成旧形态：先把附件表换回 0004 的样子 ----
	if err := db.WithTx(ctx, func(tx *Tx) error {
		steps := []string{
			`CREATE TABLE attachment_old (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				owner_type TEXT    NOT NULL
				           CHECK (owner_type IN ('invoice','expense_claim','expense_item','voucher')),
				owner_id   INTEGER NOT NULL,
				file_name  TEXT    NOT NULL,
				sha256     TEXT    NOT NULL,
				mime       TEXT    NOT NULL DEFAULT '',
				size       INTEGER NOT NULL DEFAULT 0,
				kind       TEXT    NOT NULL DEFAULT ''
				           CHECK (kind IN ('','invoice','receipt','contract','other')),
				created_at TEXT    NOT NULL,
				UNIQUE (owner_type, owner_id, sha256)
			)`,
			`INSERT INTO attachment_old
			   (id, owner_type, owner_id, file_name, sha256, mime, size, kind, created_at)
			   SELECT id, owner_type, owner_id, file_name, sha256, mime, size, kind, created_at
			     FROM attachment`,
			`DROP TABLE attachment`,
			`ALTER TABLE attachment_old RENAME TO attachment`,
			`CREATE INDEX ix_attachment_owner ON attachment(owner_type, owner_id)`,
			`CREATE INDEX ix_attachment_sha   ON attachment(sha256)`,
			// 13 号迁移建的表与记录一起撤掉，让它重跑
			`DROP TABLE audit_evidence`,
			`DELETE FROM schema_migration WHERE version = 13`,
		}
		for _, s := range steps {
			if _, err := tx.Exec(ctx, s); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("还原旧形态失败: %v", err)
	}

	// 旧形态下挂两份附件：一份凭证的、一份发票的
	hash1 := strings.Repeat("1", 64)
	hash2 := strings.Repeat("2", 64)
	if err := db.WithTx(ctx, func(tx *Tx) error {
		for _, s := range []struct {
			owner string
			id    int64
			sha   string
			name  string
		}{
			{"voucher", 1, hash1, "差旅发票.pdf"},
			{"invoice", 1, hash2, "进项发票.pdf"},
		} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO attachment
					(owner_type, owner_id, file_name, sha256, mime, size, kind, created_at)
				VALUES (?,?,?,?,?,?,'',?)`,
				s.owner, s.id, s.name, s.sha, "application/pdf", 1024, "2025-03-31T00:00:00Z"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("写入旧数据失败: %v", err)
	}

	// ---- 2. 重跑迁移 ----
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("重跑迁移失败: %v", err)
	}

	// ---- 3. 旧数据必须一条不少 ----
	rows, err := db.SQL().QueryContext(ctx,
		`SELECT owner_type, owner_id, file_name, sha256 FROM attachment ORDER BY id`)
	if err != nil {
		t.Fatalf("读附件表失败: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var owner, name, sha string
		var id int64
		if err := rows.Scan(&owner, &id, &name, &sha); err != nil {
			t.Fatal(err)
		}
		got = append(got, owner+"|"+name)
	}
	if len(got) != 2 || got[0] != "voucher|差旅发票.pdf" || got[1] != "invoice|进项发票.pdf" {
		t.Fatalf("★ 迁移后旧附件记录变了或丢了：%v", got)
	}

	// ---- 4. 新结构：审计底稿两类归属可以用了 ----
	if err := db.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO attachment
				(owner_type, owner_id, file_name, sha256, mime, size, kind, created_at)
			VALUES ('audit_adjustment', 1, '折旧计算表.xlsx', ?, '', 2048, '', ?)`,
			strings.Repeat("3", 64), "2025-03-31T00:00:00Z")
		return err
	}); err != nil {
		t.Fatalf("★ 迁移后仍然挂不上审计底稿的附件：%v", err)
	}
	// 而不认识的值仍然要被拦下（CHECK 还在）
	if err := db.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO attachment
				(owner_type, owner_id, file_name, sha256, mime, size, kind, created_at)
			VALUES ('audit_whatever', 1, 'x.pdf', ?, '', 1, '', ?)`,
			strings.Repeat("4", 64), "2025-03-31T00:00:00Z")
		return err
	}); err == nil {
		t.Error("★ 认不出的 owner_type 必须被 CHECK 拦下 —— 写错一个字母的表现是附件谁也找不着")
	}

	// ---- 5. 证据表可用，且判重生效 ----
	ins := `INSERT INTO audit_evidence
		(year, month, owner_type, owner_id, ref_kind, ref_id, ref_label,
		 sha256, file_name, file_size, note, linked_by, created_at)
		VALUES (2025, 3, 'adjustment', 1, ?, ?, ?, ?, ?, 0, ?, '李审计', ?)`
	if err := db.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, ins, "external", 0, "访谈记录", "", "", "口头说明", "2025-03-31T00:00:00Z")
		return err
	}); err != nil {
		t.Fatalf("证据表写入失败: %v", err)
	}
	if err := db.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, ins, "external", 0, "访谈记录", "", "", "口头说明", "2025-03-31T00:00:00Z")
		return err
	}); err == nil {
		t.Error("同一份资料挂到同一个结论下应当被唯一索引挡住")
	}
}
