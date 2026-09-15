-- ---------------------------------------------------------------------------
-- 审计底稿
--
-- 三张表：重要性水平、审计调整、调整分录。
--
-- # 为什么调整分录不直接做成「草稿凭证」
--
-- 审计调整分两种：**已调整**（改进了账里）与**未更正**（登记了但不入账）。
-- 后者按定义就是不进账的 —— 它是「我们发现了但决定不改」的记录。
--
-- 如果调整一登记就落成草稿凭证，那么账期结算时它会跟着其他草稿
-- 一起被自动过账，于是「未更正错报」凭空进了账。所以调整必须
-- 独立存。
--
-- # ★ 两个事实，都从凭证上算出来，不另存标记
--
--   已生成凭证   voucher_id 不为空
--   已入账       voucher_id 指的那张凭证 status='posted'
--
-- 为什么都要算而不用存：**生成凭证不等于入账**。
-- 这张软件里凭证录完只落草稿，过账只发生在账期结算 ——
-- 调整凭证也一样。所以「这笔调整进账面数了吗」这个问题，
-- 唯一的正确答案在凭证的状态里，存一份副本迟早会对不上。
--
-- 于是：
--   1. 未更正错报汇总只算**未过账**的调整 —— 凭证没过账，
--      账上那个错还在，它当然还是未更正错报
--   2. 审定数 = 账面数 + 未过账调整 —— 凭证过账后账面数里
--      已经含了它，再加一次就是重复计算
-- 两处用的是同一个判断（凭证过账了没有），口径才一致。
--
-- 存副本还有更硬的理由：凭证过账发生在**账期结算**，
-- 那一刻底稿页并不在场，没有机会去同步一个标记。
-- ---------------------------------------------------------------------------

-- 每个期间一份重要性水平。
--
-- 按期存在表里而不是放 setting：重要性水平是**逐期底稿**的一部分，
-- 明年审计时会被复核，得能查到「当时定的是多少、依据是什么」。
CREATE TABLE audit_materiality (
  year           INTEGER NOT NULL,
  month          INTEGER NOT NULL,
  -- 基准：assets 资产总额 | revenue 营业收入 | profit 利润总额 | expense 费用总额
  benchmark      TEXT    NOT NULL
                 CHECK (benchmark IN ('assets','revenue','profit','expense')),
  -- 基准金额（分）。可以由账套取数，也可以手工覆盖
  benchmark_amount INTEGER NOT NULL CHECK (benchmark_amount > 0),
  -- 三个比例，百万分比
  rate_ppm        INTEGER NOT NULL CHECK (rate_ppm > 0 AND rate_ppm < 1000000),
  performance_ppm INTEGER NOT NULL DEFAULT 600000
                  CHECK (performance_ppm > 0 AND performance_ppm <= 1000000),
  trivial_ppm     INTEGER NOT NULL DEFAULT 50000
                  CHECK (trivial_ppm >= 0 AND trivial_ppm < 1000000),
  -- 判断说明：为什么选这个基准、这个比例
  note            TEXT    NOT NULL DEFAULT '',
  updated_at      TEXT    NOT NULL,
  PRIMARY KEY (year, month)
);

-- 一笔审计调整。
CREATE TABLE audit_adjustment (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  year       INTEGER NOT NULL,
  month      INTEGER NOT NULL,
  -- 底稿索引号，如 ADJ-2026-001
  code       TEXT    NOT NULL DEFAULT '',
  -- adjust 调整（动损益）| reclass 重分类（不动损益）
  kind       TEXT    NOT NULL DEFAULT 'adjust'
             CHECK (kind IN ('adjust','reclass')),
  summary    TEXT    NOT NULL,
  -- 调整依据：底稿的核心。没有依据的调整复核人无法判断该不该调
  reason     TEXT    NOT NULL,
  -- 证据来源：哪份资料支持这笔调整
  evidence   TEXT    NOT NULL DEFAULT '',
  -- 这张调整生成的凭证。为空表示还没生成过。
  --
  -- ★ 「有没有生成过凭证」直接看这一列，不再另存一个标记 ——
  -- 两个字段表达一件事，迟早会一个说生成了、另一个说没生成。
  -- 「入没入账」也由它指的那张凭证算出来（见文件头）。
  --
  -- ON DELETE SET NULL：草稿凭证被删掉时自动清空，
  -- 这笔调整随之可以重新生成或删除。不加这一条，
  -- 外键会把那张草稿锁死 —— 用户既删不掉凭证也删不掉调整。
  voucher_id INTEGER REFERENCES voucher(id) ON DELETE SET NULL,
  created_by TEXT    NOT NULL DEFAULT '',
  reviewed_by TEXT   NOT NULL DEFAULT '',
  created_at TEXT    NOT NULL,
  updated_at TEXT    NOT NULL
);
CREATE INDEX ix_audit_adj_period ON audit_adjustment(year, month, id);

-- 调整分录的行。
--
-- 与 voucher_entry 分开存而不是复用：调整是**未入账**的拟调整，
-- 它没有凭证号、不进总账，也不该出现在任何账簿里。
CREATE TABLE audit_adjustment_line (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  adjustment_id INTEGER NOT NULL REFERENCES audit_adjustment(id) ON DELETE CASCADE,
  line_no       INTEGER NOT NULL,
  account_code  TEXT    NOT NULL,
  summary       TEXT    NOT NULL DEFAULT '',
  debit         INTEGER NOT NULL DEFAULT 0 CHECK (debit  >= 0),
  credit        INTEGER NOT NULL DEFAULT 0 CHECK (credit >= 0),
  contact_id    INTEGER,
  employee_id   INTEGER,
  dept_id       INTEGER,
  project_id    INTEGER,
  UNIQUE (adjustment_id, line_no),
  CHECK (NOT (debit > 0 AND credit > 0)),
  CHECK (debit > 0 OR credit > 0)
);
