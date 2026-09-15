-- ---------------------------------------------------------------------------
-- 审计证据链
--
-- 让底稿上的每一句结论都能追溯到具体资料。
--
-- # 为什么证据不直接挂在调整上
--
-- 一张折旧计算表可能同时支持「补提折旧」与「重要性水平」两项结论；
-- 一张发票可能既是某笔调整的依据，也是另一笔的依据。
-- 关联做成独立的一张表（谁 → 哪份资料），才能回答
-- 「这份资料支持了哪几项结论」—— 那正是复核时要问的问题。
-- ---------------------------------------------------------------------------

-- 1) attachment 允许挂到审计底稿上。
--
-- SQLite 改不了 CHECK 约束，只能重建表把数据搬过去。
-- 这是一次性的、由迁移框架保证只跑一遍。
--
-- ★ 为什么不干脆去掉 CHECK：owner_type 是拼出来的字符串，
-- 写错一个字母的表现是「附件挂上去了但谁也找不着」（查的是另一个值）。
-- 让数据库挡住它，比事后写脚本捞数据便宜得多。
CREATE TABLE attachment_v2 (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  owner_type TEXT    NOT NULL
             CHECK (owner_type IN ('invoice','expense_claim','expense_item','voucher',
                                   'audit_adjustment','audit_materiality')),
  owner_id   INTEGER NOT NULL,
  file_name  TEXT    NOT NULL,
  sha256     TEXT    NOT NULL,
  mime       TEXT    NOT NULL DEFAULT '',
  size       INTEGER NOT NULL DEFAULT 0,
  kind       TEXT    NOT NULL DEFAULT ''
             CHECK (kind IN ('','invoice','receipt','contract','other')),
  created_at TEXT    NOT NULL,
  UNIQUE (owner_type, owner_id, sha256)
);
INSERT INTO attachment_v2
  (id, owner_type, owner_id, file_name, sha256, mime, size, kind, created_at)
  SELECT id, owner_type, owner_id, file_name, sha256, mime, size, kind, created_at
    FROM attachment;
DROP TABLE attachment;
ALTER TABLE attachment_v2 RENAME TO attachment;
CREATE INDEX ix_attachment_owner ON attachment(owner_type, owner_id);
CREATE INDEX ix_attachment_sha   ON attachment(sha256);

-- 2) 证据关联。
--
-- owner_type + owner_id 是**被证明的结论**：
--   materiality / conclusion 用期间号当 id（202503），一期一条
--   adjustment 用审计调整的行 id
--
-- ref_kind 是**证据本身**：
--   attachment  账套里的附件（sha256 有实体文件）
--   voucher / invoice / bank_flow / expense  账套里已有的单据（记 id）
--   contract / external  纸质或外部资料（只有描述）
CREATE TABLE audit_evidence (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  year        INTEGER NOT NULL,
  month       INTEGER NOT NULL,
  owner_type  TEXT    NOT NULL
              CHECK (owner_type IN ('materiality','adjustment','conclusion')),
  owner_id    INTEGER NOT NULL,
  ref_kind    TEXT    NOT NULL
              CHECK (ref_kind IN ('attachment','voucher','invoice','bank_flow',
                                  'expense','contract','external')),
  -- 单据类证据的 id（文件类为 0）
  ref_id      INTEGER NOT NULL DEFAULT 0,
  -- ★ 当时那份资料的样子（快照）。
  -- 凭证以后可能被红冲、发票可能被改，而底稿要回答的是
  -- 「当时据以判断的是哪一份」。只存 id 的话，回头看会得到一份
  -- 与当时不同的资料，而底稿上还写着「依据：凭证 #7」。
  ref_label   TEXT    NOT NULL DEFAULT '',
  -- 附件类证据的文件信息
  sha256      TEXT    NOT NULL DEFAULT '',
  file_name   TEXT    NOT NULL DEFAULT '',
  file_size   INTEGER NOT NULL DEFAULT 0,
  -- 这份资料说明了什么
  note        TEXT    NOT NULL DEFAULT '',
  linked_by   TEXT    NOT NULL DEFAULT '',
  created_at  TEXT    NOT NULL,
  -- 同一份资料挂到同一个结论下只算一次
  UNIQUE (owner_type, owner_id, ref_kind, ref_id, sha256)
);
CREATE INDEX ix_evidence_owner  ON audit_evidence(owner_type, owner_id, id);
CREATE INDEX ix_evidence_period ON audit_evidence(year, month, id);
CREATE INDEX ix_evidence_sha    ON audit_evidence(sha256);
