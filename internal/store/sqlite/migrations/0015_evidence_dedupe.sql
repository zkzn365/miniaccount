-- ---------------------------------------------------------------------------
-- 审计证据的判重键：分「单据类」与「描述类」两种
--
-- # 为什么
--
-- 0013 给 audit_evidence 加的是表级 UNIQUE：
--
--	UNIQUE (owner_type, owner_id, ref_kind, ref_id, sha256)
--
-- 这张键对**单据类**（凭证/发票/银行流水/报销单）与**附件**是对的：
-- 同一张单据、同一份文件挂两次没有意义。
--
-- 但对**描述类**（合同、外部资料 —— 只有文字、没有单据 id、没有文件）
-- 就会退化：这些行的 ref_id 恒为 0、sha256 恒为空，于是同一结论下
-- 「合同」只能挂一份、「外部资料」只能挂一份 ——
-- 第二份访谈记录、第二份回函会被拒，而它们本来是不同的资料。
--
-- 所以改成两个**部分唯一索引**：
--   单据/附件类 → 按 (owner, ref_kind, ref_id, sha256) 判重
--   描述类     → 按 (owner, ref_kind, ref_label) 判重（同样的名字才算重复）
--
-- SQLite 改不了表级约束，只能重建表（同 0013 的做法），数据原样搬过去。
-- ---------------------------------------------------------------------------

CREATE TABLE audit_evidence_v2 (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  year        INTEGER NOT NULL,
  month       INTEGER NOT NULL,
  owner_type  TEXT    NOT NULL
              CHECK (owner_type IN ('materiality','adjustment','conclusion')),
  owner_id    INTEGER NOT NULL,
  ref_kind    TEXT    NOT NULL
              CHECK (ref_kind IN ('attachment','voucher','invoice','bank_flow',
                                  'expense','contract','external')),
  ref_id      INTEGER NOT NULL DEFAULT 0,
  ref_label   TEXT    NOT NULL DEFAULT '',
  sha256      TEXT    NOT NULL DEFAULT '',
  file_name   TEXT    NOT NULL DEFAULT '',
  file_size   INTEGER NOT NULL DEFAULT 0,
  note        TEXT    NOT NULL DEFAULT '',
  linked_by   TEXT    NOT NULL DEFAULT '',
  created_at  TEXT    NOT NULL
);

INSERT INTO audit_evidence_v2
  (id, year, month, owner_type, owner_id, ref_kind, ref_id, ref_label,
   sha256, file_name, file_size, note, linked_by, created_at)
  SELECT id, year, month, owner_type, owner_id, ref_kind, ref_id, ref_label,
         sha256, file_name, file_size, note, linked_by, created_at
    FROM audit_evidence;

DROP TABLE audit_evidence;
ALTER TABLE audit_evidence_v2 RENAME TO audit_evidence;

CREATE INDEX ix_evidence_owner  ON audit_evidence(owner_type, owner_id, id);
CREATE INDEX ix_evidence_period ON audit_evidence(year, month, id);
CREATE INDEX ix_evidence_sha    ON audit_evidence(sha256);

-- 单据类与附件：同一份资料挂两次不算两份依据
CREATE UNIQUE INDEX ux_evidence_doc
  ON audit_evidence(owner_type, owner_id, ref_kind, ref_id, sha256)
  WHERE ref_kind NOT IN ('contract','external');

-- 描述类：同一段文字挂两次才算重复（不同的访谈记录/合同各算一份）
CREATE UNIQUE INDEX ux_evidence_desc
  ON audit_evidence(owner_type, owner_id, ref_kind, ref_label)
  WHERE ref_kind IN ('contract','external');
