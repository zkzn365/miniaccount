-- ============================================================================
-- 0002_bank.sql —— 银行流水导入
--
-- 设计要点：
--   1. 流水与凭证**双向关联**：flow.voucher_id（前向）+ voucher.source_id（后向）
--   2. 去重靠 dedup_hash 唯一索引，重复导入同一份对账单不会产生重复流水
--   3. 匹配规则带优先级，高优先级先命中
--   4. 保留原始行（raw_json）—— 导入出问题时能追回原始数据重新解析
-- ============================================================================

-- 导入批次：一次上传一份对账单
CREATE TABLE bank_import (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id  INTEGER NOT NULL REFERENCES account(id),  -- 银行科目
  file_name   TEXT    NOT NULL,
  file_sha256 TEXT    NOT NULL,                          -- 同一文件重复导入时提示
  encoding    TEXT    NOT NULL DEFAULT '',               -- 探测到的编码，便于排错
  row_count   INTEGER NOT NULL DEFAULT 0,
  total_in    INTEGER NOT NULL DEFAULT 0,                -- 收入合计（分）
  total_out   INTEGER NOT NULL DEFAULT 0,                -- 支出合计（分）
  period_from TEXT    NOT NULL DEFAULT '',
  period_to   TEXT    NOT NULL DEFAULT '',
  imported_by TEXT    NOT NULL DEFAULT '',
  imported_at TEXT    NOT NULL
);
CREATE INDEX ix_bank_import_account ON bank_import(account_id, imported_at);

-- 银行流水
CREATE TABLE bank_flow (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  import_id   INTEGER NOT NULL REFERENCES bank_import(id) ON DELETE CASCADE,
  account_id  INTEGER NOT NULL REFERENCES account(id),  -- 银行科目
  txn_date    TEXT    NOT NULL,                          -- YYYY-MM-DD
  direction   TEXT    NOT NULL CHECK (direction IN ('in','out')),
  amount      INTEGER NOT NULL CHECK (amount > 0),       -- 金额（分），恒为正
  balance     INTEGER,                                   -- 银行账户余额（分），用于对账
  counterparty_name    TEXT NOT NULL DEFAULT '',
  counterparty_account TEXT NOT NULL DEFAULT '',
  summary     TEXT NOT NULL DEFAULT '',
  serial_no   TEXT NOT NULL DEFAULT '',                  -- 银行流水号

  -- 去重键：优先用流水号，没有则用「日期+方向+金额+余额+对方户名+摘要」的 sha256
  dedup_hash  TEXT NOT NULL,

  status      TEXT NOT NULL DEFAULT 'imported'
              CHECK (status IN ('imported','matched','posted','ignored')),
  -- 匹配结果来源：rule 规则 / history 历史相似 / ai 模型 / manual 人工
  match_layer TEXT NOT NULL DEFAULT '',
  confidence  REAL NOT NULL DEFAULT 0,

  -- 提议的对方科目与辅助核算（人工可改）
  --
  -- 四维都要有：流水的对方科目可能是「管理费用—办公费」（要求部门），
  -- 也可能是「其他应付款—股东」（要求股东）。只存 contact_id 会导致
  -- 这两类科目二选一没法用。
  counter_account_id INTEGER REFERENCES account(id),
  contact_id         INTEGER REFERENCES contact(id),
  employee_id        INTEGER,
  dept_id            INTEGER,
  project_id         INTEGER,
  memo               TEXT NOT NULL DEFAULT '',           -- 拟用的摘要

  voucher_id  INTEGER REFERENCES voucher(id),            -- ★ 生成凭证后回填
  rule_id     INTEGER REFERENCES bank_rule(id),
  ignored_reason TEXT NOT NULL DEFAULT '',

  raw_json    TEXT NOT NULL DEFAULT '',                   -- 原始行，排错用
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,

  -- 同一银行账户内流水号唯一；无流水号时退化为去重键
  UNIQUE (account_id, dedup_hash)
);
CREATE INDEX ix_bank_flow_import  ON bank_flow(import_id);
CREATE INDEX ix_bank_flow_status  ON bank_flow(status, txn_date);
CREATE INDEX ix_bank_flow_date    ON bank_flow(account_id, txn_date);
CREATE INDEX ix_bank_flow_voucher ON bank_flow(voucher_id);
CREATE INDEX ix_bank_flow_counter ON bank_flow(counterparty_name);

-- 匹配规则
CREATE TABLE bank_rule (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT    NOT NULL DEFAULT '',
  priority      INTEGER NOT NULL DEFAULT 100,   -- 数值小的先匹配
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  account_id    INTEGER REFERENCES account(id), -- 限定银行科目；NULL 表示不限

  direction     TEXT NOT NULL DEFAULT '' CHECK (direction IN ('','in','out')),

  -- 匹配条件（可组合，全部满足才算命中）
  match_field   TEXT NOT NULL DEFAULT 'counterparty'
                CHECK (match_field IN ('counterparty','summary','both')),
  pattern       TEXT NOT NULL DEFAULT '',       -- 子串匹配（大小写不敏感）
  amount_min    INTEGER,                        -- 金额下限（分），含
  amount_max    INTEGER,                        -- 金额上限（分），含

  -- 命中后的记账方案：对方科目 + 往来单位 + 摘要模板
  counter_account_id INTEGER NOT NULL REFERENCES account(id),
  contact_id         INTEGER REFERENCES contact(id),
  memo_template      TEXT NOT NULL DEFAULT '',  -- 支持 {counterparty} {summary} 占位符

  hit_count     INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE INDEX ix_bank_rule_priority ON bank_rule(enabled, priority);
