-- ============================================================================
-- 0001_init.sql —— 初始表结构
--
-- 设计依据：docs/02-Go重写方案.md §4.2
-- 会计依据：财政部《小企业会计准则》（财会〔2011〕17号）
--
-- 与 Frappe Books 的关键差异（均为修复其在中国实务中不可用之处）：
--   1. account 有独立的 code 字段与唯一约束（对方把编码拼进名称字符串）
--   2. account 有 balance_dir 字段（对方靠 rootType 推导，备抵科目会算错）
--   3. root_type 含 cost（对方只有 5 类，生产成本被并入费用 → 报表算错）
--   4. 金额用 INTEGER 分（对方存 TEXT，聚合时 cast as real 退回浮点）
--   5. voucher 是唯一过账来源，且 (年,月,凭证字,序号) 唯一（对方全局计数器有竞态）
--   6. period 表管理会计期间与结账状态（对方只有两个日期，不能结账）
--   7. 全表建立必要索引（对方除主键外没有任何索引）
--   8. ledger_entry 金额非负 + 借贷互斥的 CHECK 约束
-- ============================================================================

-- ---------------------------------------------------------------------------
-- 账套：单行表
-- ---------------------------------------------------------------------------
CREATE TABLE book (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  company_name  TEXT    NOT NULL,
  credit_code   TEXT    NOT NULL DEFAULT '',   -- 统一社会信用代码
  legal_person  TEXT    NOT NULL DEFAULT '',
  address       TEXT    NOT NULL DEFAULT '',
  phone         TEXT    NOT NULL DEFAULT '',
  email         TEXT    NOT NULL DEFAULT '',
  bank_name     TEXT    NOT NULL DEFAULT '',
  bank_account  TEXT    NOT NULL DEFAULT '',
  standard      TEXT    NOT NULL DEFAULT '小企业会计准则',
  base_currency TEXT    NOT NULL DEFAULT 'CNY',
  tax_type      TEXT    NOT NULL DEFAULT 'general',  -- general 一般纳税人 | small 小规模
  start_year    INTEGER NOT NULL,
  start_month   INTEGER NOT NULL CHECK (start_month BETWEEN 1 AND 12),
  setup_complete INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT    NOT NULL,
  updated_at    TEXT    NOT NULL
);

-- ---------------------------------------------------------------------------
-- 会计期间
-- ---------------------------------------------------------------------------
CREATE TABLE period (
  year       INTEGER NOT NULL,
  month      INTEGER NOT NULL CHECK (month BETWEEN 1 AND 12),
  start_date TEXT    NOT NULL,                       -- YYYY-MM-DD
  end_date   TEXT    NOT NULL,
  status     TEXT    NOT NULL
             CHECK (status IN ('future','open','closed')),
  closed_at  TEXT,
  closed_by  TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (year, month)
);
CREATE INDEX ix_period_status ON period(status);

-- ---------------------------------------------------------------------------
-- 科目
-- ---------------------------------------------------------------------------
CREATE TABLE account (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  code        TEXT    NOT NULL UNIQUE,               -- 4-2-2-2 纯数字
  name        TEXT    NOT NULL,
  parent_id   INTEGER REFERENCES account(id),
  level       INTEGER NOT NULL CHECK (level BETWEEN 1 AND 4),
  is_leaf     INTEGER NOT NULL DEFAULT 1 CHECK (is_leaf IN (0,1)),
  root_type   TEXT    NOT NULL
              CHECK (root_type IN ('asset','liability','equity','cost','income','expense')),
  category    TEXT    NOT NULL,                      -- 官方五大类
  balance_dir TEXT    NOT NULL CHECK (balance_dir IN ('debit','credit')),
  aux_types   TEXT    NOT NULL DEFAULT '',           -- 逗号分隔的辅助核算维度
  is_enabled  INTEGER NOT NULL DEFAULT 1 CHECK (is_enabled IN (0,1)),
  is_preset   INTEGER NOT NULL DEFAULT 0 CHECK (is_preset IN (0,1)),
  remark      TEXT    NOT NULL DEFAULT '',
  sort_order  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX ix_account_parent ON account(parent_id);
CREATE INDEX ix_account_enabled ON account(is_enabled, is_leaf);
-- 科目编码前缀查询（报表取数要展开汇总科目的全部下级）
CREATE INDEX ix_account_code ON account(code);

-- ---------------------------------------------------------------------------
-- 往来档案（客户 / 供应商 / 员工 / 股东 / 其他单位）
--
-- 五种往来共用一张表，用 kind 区分：往来账只需要按 contact_id 聚合，
-- 不必为四种对象各建一张表。
-- ---------------------------------------------------------------------------
CREATE TABLE contact (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  code               TEXT    UNIQUE,
  kind               TEXT    NOT NULL
                     CHECK (kind IN ('customer','supplier','both','employee','shareholder','other')),
  name               TEXT    NOT NULL,
  short_name         TEXT    NOT NULL DEFAULT '',
  tax_no             TEXT    NOT NULL DEFAULT '',    -- 纳税人识别号
  id_card            TEXT    NOT NULL DEFAULT '',    -- 股东/个人往来用
  bank_name          TEXT    NOT NULL DEFAULT '',
  bank_account       TEXT    NOT NULL DEFAULT '',
  address            TEXT    NOT NULL DEFAULT '',
  phone              TEXT    NOT NULL DEFAULT '',
  email              TEXT    NOT NULL DEFAULT '',
  contact_person     TEXT    NOT NULL DEFAULT '',
  credit_limit       INTEGER NOT NULL DEFAULT 0,     -- 信用额度（分）
  default_account_id INTEGER REFERENCES account(id), -- 默认应收/应付科目
  is_enabled         INTEGER NOT NULL DEFAULT 1 CHECK (is_enabled IN (0,1)),
  remark             TEXT    NOT NULL DEFAULT '',
  created_at         TEXT    NOT NULL,
  updated_at         TEXT    NOT NULL
);
CREATE INDEX ix_contact_kind ON contact(kind, is_enabled);
CREATE INDEX ix_contact_name ON contact(name);

-- ---------------------------------------------------------------------------
-- 凭证（★ 唯一的记账入口）
-- ---------------------------------------------------------------------------
CREATE TABLE voucher (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  year         INTEGER NOT NULL,
  month        INTEGER NOT NULL CHECK (month BETWEEN 1 AND 12),
  word         TEXT    NOT NULL,                     -- 凭证字：记/收/付/转
  seq          INTEGER NOT NULL DEFAULT 0,           -- 期间内序号；草稿为 0
  no           TEXT    NOT NULL DEFAULT '',          -- 展示号：记-2025-01-0001
  biz_date     TEXT    NOT NULL,                     -- 业务/记账日期 YYYY-MM-DD
  attach_count INTEGER NOT NULL DEFAULT 0,           -- 附单据数
  remark       TEXT    NOT NULL DEFAULT '',
  status       TEXT    NOT NULL DEFAULT 'draft'
               CHECK (status IN ('draft','posted','voided')),
  source       TEXT    NOT NULL DEFAULT 'manual',
  source_id    INTEGER,                              -- 来源单据 id（流水/报销/工资…）
  created_by   TEXT    NOT NULL,
  reviewed_by  TEXT    NOT NULL DEFAULT '',
  posted_by    TEXT    NOT NULL DEFAULT '',
  posted_at    TEXT,
  reverses_id  INTEGER REFERENCES voucher(id),       -- 本凭证冲销的原凭证
  voided_by    INTEGER REFERENCES voucher(id),       -- 冲销本凭证的红字凭证
  created_by_ai INTEGER NOT NULL DEFAULT 0,          -- 是否由 AI 提议生成
  created_at   TEXT    NOT NULL,
  updated_at   TEXT    NOT NULL
);

-- 凭证字号在「期间 + 凭证字」内唯一。
-- 用**部分索引**排除草稿：草稿不占号（seq=0），
-- 这样废弃的草稿不会在账上留下断号。
CREATE UNIQUE INDEX ux_voucher_seq
  ON voucher(year, month, word, seq) WHERE status <> 'draft';
CREATE UNIQUE INDEX ux_voucher_no
  ON voucher(no) WHERE no <> '';

CREATE INDEX ix_voucher_date   ON voucher(biz_date);
CREATE INDEX ix_voucher_period ON voucher(year, month, status);
CREATE INDEX ix_voucher_source ON voucher(source, source_id);

CREATE TABLE voucher_entry (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  voucher_id INTEGER NOT NULL REFERENCES voucher(id) ON DELETE CASCADE,
  line_no    INTEGER NOT NULL,
  summary    TEXT    NOT NULL,
  account_id INTEGER NOT NULL REFERENCES account(id),
  debit      INTEGER NOT NULL DEFAULT 0 CHECK (debit  >= 0),
  credit     INTEGER NOT NULL DEFAULT 0 CHECK (credit >= 0),
  contact_id INTEGER REFERENCES contact(id),
  employee_id INTEGER,
  dept_id     INTEGER,
  project_id  INTEGER,
  -- 单条分录不能同时有借有贷，也不能都为零
  CHECK (NOT (debit > 0 AND credit > 0)),
  CHECK (debit > 0 OR credit > 0),
  UNIQUE (voucher_id, line_no)
);
CREATE INDEX ix_ve_account ON voucher_entry(account_id);
CREATE INDEX ix_ve_contact ON voucher_entry(contact_id);

-- ---------------------------------------------------------------------------
-- 总账（扁平、追加、可冲销）
-- ---------------------------------------------------------------------------
CREATE TABLE ledger_entry (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  biz_date   TEXT    NOT NULL,
  year       INTEGER NOT NULL,
  month      INTEGER NOT NULL,
  account_id INTEGER NOT NULL REFERENCES account(id),
  debit      INTEGER NOT NULL DEFAULT 0 CHECK (debit  >= 0),
  credit     INTEGER NOT NULL DEFAULT 0 CHECK (credit >= 0),
  contact_id  INTEGER REFERENCES contact(id),
  employee_id INTEGER,
  dept_id     INTEGER,
  project_id  INTEGER,
  voucher_id  INTEGER NOT NULL REFERENCES voucher(id),
  line_no     INTEGER NOT NULL,
  summary     TEXT    NOT NULL,
  -- reverted 是审计标记：「所属凭证已作废」。它**不参与报表过滤** ——
  -- 红字凭证与原凭证都保留在账上并都参与汇总，净额自然为零。
  -- 详见 voucher_repo.go 中 Balance 的说明。
  reverted    INTEGER NOT NULL DEFAULT 0 CHECK (reverted IN (0,1)),
  reverses_id INTEGER REFERENCES ledger_entry(id),
  created_at  TEXT    NOT NULL,
  CHECK (NOT (debit > 0 AND credit > 0)),
  CHECK (debit > 0 OR credit > 0)
);

-- 报表查询的主要路径：按科目 + 日期、按往来 + 日期、按期间
CREATE INDEX ix_ledger_account_date ON ledger_entry(account_id, biz_date);
CREATE INDEX ix_ledger_contact_date ON ledger_entry(contact_id, biz_date);
CREATE INDEX ix_ledger_period       ON ledger_entry(year, month);
CREATE INDEX ix_ledger_voucher      ON ledger_entry(voucher_id);

-- ---------------------------------------------------------------------------
-- 设置：键值对（单例配置）
-- ---------------------------------------------------------------------------
CREATE TABLE setting (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
