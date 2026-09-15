-- ---------------------------------------------------------------------------
-- 税务申报台账
--
-- 记下「这一期这个税种报没报、什么时候报的、报了多少、谁办的」。
--
-- # 为什么这件事要落库，而计算表不需要
--
-- 计算表是账套的**派生结果**：账一变，它跟着变，所以不存副本
-- （存了就会出现两个真相）。但「申报」是**发生过的事实** ——
-- 账后来改了，已经报出去的那个数不会跟着改。
--
-- 台账里存的是**申报当时的快照**（应补税额、申报日期、回执号），
-- 再拿它与现在算出来的表勾稽：不一致就报出来，由办税人决定
-- 要不要更正申报。不存的话，这件事根本无从发现。
-- ---------------------------------------------------------------------------

CREATE TABLE tax_filing (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  year        INTEGER NOT NULL,
  month       INTEGER NOT NULL CHECK (month BETWEEN 1 AND 12),
  -- 税种：vat 增值税及附加 | cit 企业所得税 | iit 个人所得税（工资薪金）
  kind        TEXT    NOT NULL CHECK (kind IN ('vat','cit','iit')),
  -- 申报属期的文字（如「2025 年第 1 季度」）
  period_label TEXT   NOT NULL DEFAULT '',
  -- filed 已申报 | paid 已申报并缴纳 | void 已作废
  status      TEXT    NOT NULL DEFAULT 'filed'
              CHECK (status IN ('filed','paid','void')),

  filed_date  TEXT    NOT NULL,                -- 申报日期
  paid_date   TEXT    NOT NULL DEFAULT '',     -- 缴款日期

  -- ★ 申报当时的快照（分）。payable 可为负 —— 多缴也是事实
  -- ★ 四个数构成一个等式：payable = tax_amount + surcharge − paid
  -- paid 是本期已经缴过的部分（增值税的已交税金、所得税的已预缴）——
  -- 少了它，「应纳 + 附加」与「应补(退)」就对不上
  payable     INTEGER NOT NULL DEFAULT 0,
  tax_amount  INTEGER NOT NULL DEFAULT 0,
  surcharge   INTEGER NOT NULL DEFAULT 0,
  paid        INTEGER NOT NULL DEFAULT 0,

  channel     TEXT    NOT NULL DEFAULT '',     -- 电子税务局 / 办税服务厅
  receipt_no  TEXT    NOT NULL DEFAULT '',     -- 申报回执号 / 缴款书号
  operator    TEXT    NOT NULL DEFAULT '',     -- 经办人
  note        TEXT    NOT NULL DEFAULT '',

  -- 作废痕迹：不物理删除，「这一期一共报过几次」是检查时要答的问题
  voided_by     TEXT    NOT NULL DEFAULT '',
  voided_at     TEXT,
  void_reason   TEXT    NOT NULL DEFAULT '',

  created_at  TEXT    NOT NULL,
  updated_at  TEXT    NOT NULL
);

-- ★ 同一属期同一税种只有**一条有效**记录。
--
-- 用部分索引排除已作废的：更正申报时旧记录置为 void，
-- 然后可以再登记一条 —— 既留了痕迹，又不会两条并存。
CREATE UNIQUE INDEX ux_tax_filing_effective
  ON tax_filing(year, month, kind) WHERE status <> 'void';
CREATE INDEX ix_tax_filing_period ON tax_filing(year, month, id);
