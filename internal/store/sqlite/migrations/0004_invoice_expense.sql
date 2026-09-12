-- ============================================================================
-- 0004_invoice_expense.sql —— 发票档案、差旅报销、附件
--
-- 设计要点：
--   1. 附件只存 sha256，不存路径 —— 路径由 hash 派生，
--      账套目录改名/移动后附件依然可用（见 internal/attachment）。
--   2. 发票按「方向 + 代码 + 号码」判重：同一张发票被重复登记很常见。
--   3. 报销单的明细行可以关联发票档案，从而把进项税额拆出来抵扣。
-- ============================================================================

-- ---------------------------------------------------------------------------
-- 附件（元数据；文件实体在 <账套>.files/ 目录下按内容寻址存放）
-- ---------------------------------------------------------------------------
CREATE TABLE attachment (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  owner_type TEXT    NOT NULL
             CHECK (owner_type IN ('invoice','expense_claim','expense_item','voucher')),
  owner_id   INTEGER NOT NULL,
  file_name  TEXT    NOT NULL,             -- 原始文件名（展示与下载用）
  sha256     TEXT    NOT NULL,             -- 内容寻址键
  mime       TEXT    NOT NULL DEFAULT '',
  size       INTEGER NOT NULL DEFAULT 0,
  kind       TEXT    NOT NULL DEFAULT ''   -- invoice | receipt | contract | other
             CHECK (kind IN ('','invoice','receipt','contract','other')),
  created_at TEXT    NOT NULL,
  -- 同一单据内不重复挂同一份文件；但不同单据可以共享同一份物理文件
  UNIQUE (owner_type, owner_id, sha256)
);
CREATE INDEX ix_attachment_owner ON attachment(owner_type, owner_id);
CREATE INDEX ix_attachment_sha   ON attachment(sha256);

-- ---------------------------------------------------------------------------
-- 发票档案
-- ---------------------------------------------------------------------------
CREATE TABLE invoice (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  direction TEXT    NOT NULL CHECK (direction IN ('input','output')),
  kind      TEXT    NOT NULL
            CHECK (kind IN ('special','general','e_special','e_general','other')),

  code      TEXT    NOT NULL DEFAULT '',   -- 发票代码（电子发票可能为空）
  number    TEXT    NOT NULL,              -- 发票号码

  invoice_date TEXT NOT NULL,

  seller_name    TEXT NOT NULL DEFAULT '',
  seller_tax_no  TEXT NOT NULL DEFAULT '',
  buyer_name     TEXT NOT NULL DEFAULT '',
  buyer_tax_no   TEXT NOT NULL DEFAULT '',

  -- 金额三分法：不含税金额 + 税额 = 价税合计（必须自洽）
  amount_ex_tax INTEGER NOT NULL DEFAULT 0,
  tax_rate      INTEGER NOT NULL DEFAULT 0,   -- 百万分之一
  tax_amount    INTEGER NOT NULL DEFAULT 0,
  total_amount  INTEGER NOT NULL DEFAULT 0,

  category TEXT NOT NULL DEFAULT '',

  status     TEXT NOT NULL DEFAULT 'pending'
             CHECK (status IN ('pending','verified','booked','voided')),
  contact_id INTEGER REFERENCES contact(id),
  voucher_id INTEGER REFERENCES voucher(id),

  remark     TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- ★ 判重：同一张发票不应重复登记。
-- 用部分索引排除空代码（电子发票没有代码）与已作废的发票。
CREATE UNIQUE INDEX ux_invoice_identity
  ON invoice(direction, code, number) WHERE code <> '' AND status <> 'voided';
CREATE UNIQUE INDEX ux_invoice_number
  ON invoice(direction, number) WHERE code = '' AND status <> 'voided';

CREATE INDEX ix_invoice_date     ON invoice(invoice_date);
CREATE INDEX ix_invoice_dir_stat ON invoice(direction, status, invoice_date);
CREATE INDEX ix_invoice_voucher  ON invoice(voucher_id);
CREATE INDEX ix_invoice_contact  ON invoice(contact_id);

-- ---------------------------------------------------------------------------
-- 差旅报销
-- ---------------------------------------------------------------------------
CREATE TABLE expense_claim (
  id   INTEGER PRIMARY KEY AUTOINCREMENT,
  code TEXT NOT NULL UNIQUE,

  claimant_employee_id INTEGER NOT NULL REFERENCES employee(id),
  dept_id              INTEGER,

  apply_date TEXT NOT NULL,

  trip_start  TEXT NOT NULL DEFAULT '',
  trip_end    TEXT NOT NULL DEFAULT '',
  destination TEXT NOT NULL DEFAULT '',
  reason      TEXT NOT NULL DEFAULT '',

  status TEXT NOT NULL DEFAULT 'draft'
         CHECK (status IN ('draft','approved','paid','posted','rejected')),

  total_amount INTEGER NOT NULL DEFAULT 0,

  voucher_id           INTEGER REFERENCES voucher(id),
  approver_employee_id INTEGER REFERENCES employee(id),
  approved_at          TEXT,

  pay_from_account_id INTEGER REFERENCES account(id),
  payable_account_id  INTEGER REFERENCES account(id),

  remark     TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX ix_claim_status   ON expense_claim(status, apply_date);
CREATE INDEX ix_claim_claimant ON expense_claim(claimant_employee_id);
CREATE INDEX ix_claim_voucher  ON expense_claim(voucher_id);

CREATE TABLE expense_item (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  claim_id INTEGER NOT NULL REFERENCES expense_claim(id) ON DELETE CASCADE,
  line_no  INTEGER NOT NULL,

  category   TEXT NOT NULL
             CHECK (category IN ('transport','accommodation','meal',
                                 'local_transit','conference','other')),
  occur_date TEXT NOT NULL,
  summary    TEXT NOT NULL DEFAULT '',

  -- amount 是价税合计（员工实际支付）；tax_amount 是其中可抵扣的进项税
  amount     INTEGER NOT NULL DEFAULT 0 CHECK (amount >= 0),
  tax_amount INTEGER NOT NULL DEFAULT 0 CHECK (tax_amount >= 0),

  account_id INTEGER NOT NULL REFERENCES account(id),
  dept_id    INTEGER,
  invoice_id INTEGER REFERENCES invoice(id),

  UNIQUE (claim_id, line_no)
);
CREATE INDEX ix_expense_item_claim   ON expense_item(claim_id);
CREATE INDEX ix_expense_item_invoice ON expense_item(invoice_id);
