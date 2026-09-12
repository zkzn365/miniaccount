-- ============================================================================
-- 0003_payroll.sql —— 工资模块
--
-- 设计要点：
--   1. 个税采用**累计预扣预缴法**，因此必须能查到同一员工本年度的历史累计数。
--      salary_item 上的 cum_* 字段把「计算时的累计状态」固化下来，
--      这样即使将来税率表调整，历史工资单仍可追溯「当时是怎么算的」。
--   2. 税率表与社保方案存 JSON（setting 表），便于政策调整后不发新版软件。
--   3. 计提与发放是两张凭证，各自记录 id 便于双向追溯。
-- ============================================================================

-- 员工（含外聘劳务人员）
CREATE TABLE employee (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  code    TEXT    NOT NULL UNIQUE,
  name    TEXT    NOT NULL,
  id_card TEXT    NOT NULL DEFAULT '',
  -- employee 在职员工（工资薪金所得）| labor 外聘劳务（劳务报酬所得）
  kind    TEXT    NOT NULL DEFAULT 'employee'
          CHECK (kind IN ('employee','labor')),

  dept_id  INTEGER,
  position TEXT NOT NULL DEFAULT '',

  hire_date  TEXT NOT NULL DEFAULT '',
  leave_date TEXT NOT NULL DEFAULT '',

  bank_name    TEXT NOT NULL DEFAULT '',
  bank_account TEXT NOT NULL DEFAULT '',

  -- 工资费用的归集科目。生产人员记生产成本、销售人员记销售费用、
  -- 管理人员记管理费用 —— 一律记管理费用会低估产品成本、高估期间费用。
  expense_account_id INTEGER NOT NULL REFERENCES account(id),
  company_account_id INTEGER REFERENCES account(id),

  scheme_name TEXT NOT NULL DEFAULT '',   -- 适用的社保方案；空表示不缴
  is_enabled  INTEGER NOT NULL DEFAULT 1 CHECK (is_enabled IN (0,1)),
  remark      TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX ix_employee_kind ON employee(kind, is_enabled);

-- 工资单（一个年月一张）
CREATE TABLE salary_run (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  year       INTEGER NOT NULL,
  month      INTEGER NOT NULL CHECK (month BETWEEN 1 AND 12),
  status     TEXT NOT NULL DEFAULT 'draft'
             CHECK (status IN ('draft','confirmed','posted')),

  -- 生成时所用的税率表（JSON），便于追溯
  tax_table_json TEXT NOT NULL DEFAULT '',

  -- 计提凭证与发放凭证
  accrual_voucher_id INTEGER REFERENCES voucher(id),
  payment_voucher_id INTEGER REFERENCES voucher(id),

  -- 合计（冗余，便于列表展示）
  total_gross     INTEGER NOT NULL DEFAULT 0,
  total_iit       INTEGER NOT NULL DEFAULT 0,
  total_si_self   INTEGER NOT NULL DEFAULT 0,
  total_si_company INTEGER NOT NULL DEFAULT 0,
  total_net       INTEGER NOT NULL DEFAULT 0,
  headcount       INTEGER NOT NULL DEFAULT 0,

  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (year, month)
);

-- 工资明细（一人一行）
CREATE TABLE salary_item (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id    INTEGER NOT NULL REFERENCES salary_run(id) ON DELETE CASCADE,
  employee_id INTEGER NOT NULL REFERENCES employee(id),
  line_no   INTEGER NOT NULL,

  -- 应发项目
  base_salary    INTEGER NOT NULL DEFAULT 0,
  post_allowance INTEGER NOT NULL DEFAULT 0,
  overtime_pay   INTEGER NOT NULL DEFAULT 0,
  bonus          INTEGER NOT NULL DEFAULT 0,
  other_income   INTEGER NOT NULL DEFAULT 0,

  -- 扣款
  attendance_deduction INTEGER NOT NULL DEFAULT 0,
  other_deduction      INTEGER NOT NULL DEFAULT 0,

  -- 社保公积金（个人）
  pension_self      INTEGER NOT NULL DEFAULT 0,
  medical_self      INTEGER NOT NULL DEFAULT 0,
  unemployment_self INTEGER NOT NULL DEFAULT 0,
  housing_fund_self  INTEGER NOT NULL DEFAULT 0,
  si_base            INTEGER NOT NULL DEFAULT 0,
  hf_base            INTEGER NOT NULL DEFAULT 0,

  -- 社保公积金（单位）
  pension_co      INTEGER NOT NULL DEFAULT 0,
  medical_co      INTEGER NOT NULL DEFAULT 0,
  unemployment_co INTEGER NOT NULL DEFAULT 0,
  injury_co       INTEGER NOT NULL DEFAULT 0,
  maternity_co    INTEGER NOT NULL DEFAULT 0,
  housing_fund_co INTEGER NOT NULL DEFAULT 0,

  -- 税前扣除
  special_additional INTEGER NOT NULL DEFAULT 0,  -- 专项附加扣除
  other_tax_deduction INTEGER NOT NULL DEFAULT 0, -- 其他扣除

  -- 计算结果
  gross_pay INTEGER NOT NULL DEFAULT 0,
  iit       INTEGER NOT NULL DEFAULT 0,
  net_pay   INTEGER NOT NULL DEFAULT 0,

  -- ★ 计算时的累计状态（含本月）。固化下来才能回答
  --   「为什么这个月税和上个月不一样」。
  cum_months            INTEGER NOT NULL DEFAULT 0,
  cum_income            INTEGER NOT NULL DEFAULT 0,
  cum_special_deduction INTEGER NOT NULL DEFAULT 0,
  cum_special_additional INTEGER NOT NULL DEFAULT 0,
  cum_other_deduction   INTEGER NOT NULL DEFAULT 0,
  cum_taxable           INTEGER NOT NULL DEFAULT 0,
  cum_cumulative_tax    INTEGER NOT NULL DEFAULT 0,
  cum_tax_withheld      INTEGER NOT NULL DEFAULT 0,

  note TEXT NOT NULL DEFAULT '',   -- 计算说明
  UNIQUE (run_id, employee_id)
);
CREATE INDEX ix_salary_item_employee ON salary_item(employee_id);
-- 累计预扣预缴要按「员工 + 年度」查历史，这个索引是必需的
CREATE INDEX ix_salary_item_ytd ON salary_item(employee_id, run_id);
