-- ---------------------------------------------------------------------------
-- 固定资产与费用摊销
--
-- 这两件事原来是**完全没有**的：账套里有 1601 固定资产、1602 累计折旧、
-- 1801 长期待摊费用这些科目，但没有地方登记「这台设备多少钱、什么时候
-- 开始用、每月提多少」。用户只能每月手工敲一张折旧凭证 ——
-- 而手工敲的后果是：月份漏了没人发现，尾差算错了也没人发现，
-- 年底一看累计折旧对不上原值。
--
-- 两张卡片的形状是一样的（一笔钱先资本化，再按月转费用），
-- 所以放在一个迁移里。区别只有起算时点：
--
--   固定资产：当月增加当月不提，**次月**起提（准则第 4 号第十四条）
--   费用摊销：受益期从**当月**开始，就从当月摊
--
-- ★ 这两条不同的规则各自写进 domain/asset 的注释里，因为「为什么
-- 一个次月一个当月」是使用者最容易问、也最容易记反的地方。
-- ---------------------------------------------------------------------------

-- 固定资产卡片
CREATE TABLE fixed_asset (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  -- 资产编号，可留空（小企业常常就是按名称认）
  code          TEXT    NOT NULL DEFAULT '',
  name          TEXT    NOT NULL,
  -- 税法类别：决定最低折旧年限，只用于提示纳税调整，不做硬校验
  category      TEXT    NOT NULL DEFAULT 'electronic'
                CHECK (category IN ('building','machine','furniture','vehicle','electronic')),
  -- 使用部门。费用科目要按部门辅助核算，所以这是必填（应用层校验）
  dept_id       INTEGER REFERENCES department(id),
  -- 原值（分）
  orig_value    INTEGER NOT NULL CHECK (orig_value > 0),
  -- 预计净残值率，百万分比：5% → 50000。用整数存，避免浮点抖动
  salvage_ppm   INTEGER NOT NULL DEFAULT 50000
                CHECK (salvage_ppm >= 0 AND salvage_ppm < 1000000),
  -- 预计使用月数
  useful_months INTEGER NOT NULL CHECK (useful_months > 0),
  -- 投入使用日期（不是购买日期）：折旧从它的**次月**起提
  start_date    TEXT    NOT NULL,
  -- 折旧费的借方科目（如 560205 管理费用—折旧费）
  expense_account TEXT  NOT NULL DEFAULT '560205',
  -- 累计折旧的贷方科目
  accum_account   TEXT  NOT NULL DEFAULT '1602',
  status        TEXT    NOT NULL DEFAULT 'in_use'
                CHECK (status IN ('in_use','disposed')),
  -- 处置日期；未处置为空串
  disposed_date TEXT    NOT NULL DEFAULT '',
  remark        TEXT    NOT NULL DEFAULT '',
  created_at    TEXT    NOT NULL,
  updated_at    TEXT    NOT NULL
);
CREATE INDEX ix_fixed_asset_status ON fixed_asset(status, start_date);

-- 每期折旧记录。
--
-- 一期一行，`UNIQUE (asset_id, year, month)` 是防止**重复计提**的
-- 最后一道防线：漏提一个月不会报错（少一笔费用而已），
-- 重复提一个月也不会报错（多一笔费用），两个都是看不出来的错。
CREATE TABLE asset_depreciation (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  asset_id   INTEGER NOT NULL REFERENCES fixed_asset(id) ON DELETE CASCADE,
  year       INTEGER NOT NULL,
  month      INTEGER NOT NULL,
  -- 本期计提额（分）
  amount     INTEGER NOT NULL CHECK (amount >= 0),
  -- 计提时生成的凭证明细（草稿；过账在账期结算时统一做）
  voucher_id INTEGER REFERENCES voucher(id),
  created_at TEXT    NOT NULL,
  UNIQUE (asset_id, year, month)
);
CREATE INDEX ix_depreciation_period ON asset_depreciation(year, month);

-- 待摊项目（长期待摊费用等）
CREATE TABLE amortization (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  code            TEXT    NOT NULL DEFAULT '',
  name            TEXT    NOT NULL,
  dept_id         INTEGER REFERENCES department(id),
  -- 待摊总额（分）
  total_amount    INTEGER NOT NULL CHECK (total_amount > 0),
  -- 摊销月数
  months          INTEGER NOT NULL CHECK (months > 0),
  -- 受益期开始日期：从**当月**起摊
  start_date      TEXT    NOT NULL,
  -- 费用借方科目
  expense_account TEXT    NOT NULL,
  -- 待摊费用的贷方科目，默认 1801
  asset_account   TEXT    NOT NULL DEFAULT '1801',
  status          TEXT    NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','finished','voided')),
  remark          TEXT    NOT NULL DEFAULT '',
  created_at      TEXT    NOT NULL,
  updated_at      TEXT    NOT NULL
);
CREATE INDEX ix_amortization_status ON amortization(status, start_date);

-- 每期摊销记录，同 asset_depreciation 的道理
CREATE TABLE amortization_entry (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  amortization_id INTEGER NOT NULL REFERENCES amortization(id) ON DELETE CASCADE,
  year            INTEGER NOT NULL,
  month           INTEGER NOT NULL,
  amount          INTEGER NOT NULL CHECK (amount >= 0),
  voucher_id      INTEGER REFERENCES voucher(id),
  created_at      TEXT    NOT NULL,
  UNIQUE (amortization_id, year, month)
);
CREATE INDEX ix_amortization_entry_period ON amortization_entry(year, month);
