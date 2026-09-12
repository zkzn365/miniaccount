-- ---------------------------------------------------------------------------
-- 拆分「企业规模类型」与「增值税纳税人身份」
--
-- # 为什么必须拆
--
-- 原先 book 表只有一个 tax_type（general | small），把两个**完全不同**的
-- 概念混成了一个字段：
--
--   企业规模类型      所得税与统计概念。依据《中小企业划型标准规定》，
--                     看营业收入与从业人员，决定能不能享受小型微利企业
--                     所得税优惠。
--   增值税纳税人身份  流转税概念。依据《增值税法》，看年应征增值税销售额
--                     是否超过 500 万元，决定按一般计税还是简易计税、
--                     能不能抵扣进项税额。
--
-- 两者**不存在推导关系**：
--
--   - 年销售额 300 万的公司是小微企业，通常也是小规模纳税人，
--     但**可以自愿登记为一般纳税人**（为抵扣进项、为给大客户开专票）。
--     此时它同时是「小微企业」和「一般纳税人」。
--   - 年销售额 800 万的公司必须登记为一般纳税人，
--     但只要满足从业人数与资产总额条件，仍可能是小微企业。
--
-- 用一个字段表达两件事的直接后果：任何「是不是小微 → 能不能抵扣进项」
-- 的推断都会算错税。
--
-- # 迁移为什么不猜
--
-- 老账套的 tax_type 表达的是**纳税人身份**（下拉里写的就是
-- 「一般纳税人 / 小规模纳税人」），因此它迁到 vat_status。
-- 企业规模类型**无法从它反推** —— 小规模纳税人可能是微型、小型、
-- 也可能（年销售额未超 500 万的大型集团子公司）是大型企业。
-- 所以规模类型留空，由界面提示用户补填，而不是替用户猜一个。
-- ---------------------------------------------------------------------------

-- 1) 企业规模类型（所得税/统计口径）—— 留空表示未填写
ALTER TABLE book ADD COLUMN enterprise_scale TEXT NOT NULL DEFAULT '';

-- 2) 增值税纳税人身份（流转税口径）
ALTER TABLE book ADD COLUMN vat_status TEXT NOT NULL DEFAULT 'general';

-- 3) 纳税人身份的生效日期。
--
-- 身份会变：小规模可以自愿登记为一般纳税人（通常自登记之日起），
-- 一般纳税人转回小规模也有规定情形。跨期看账时必须按**业务发生日**
-- 取当时有效的身份，而不是拿今天的身份去算去年的税。
ALTER TABLE book ADD COLUMN vat_status_effective_from TEXT NOT NULL DEFAULT '';

-- 4) 把老字段迁到新字段。
--
-- tax_type 存的就是纳税人身份，一一对应；'small' → 'small_scale'。
UPDATE book SET vat_status = CASE
    WHEN tax_type = 'small' THEN 'small_scale'
    ELSE 'general'
  END,
  -- 生效日取账套启用日：老账套无从知道身份何时生效，
  -- 取启用日是唯一不会与已有分录冲突的选择（不会出现
  -- 「身份生效日之前就已记账」的尴尬）。
  vat_status_effective_from = printf('%04d-%02d-01', start_year, start_month);

-- 5) 身份变更历史。
--
-- 只存当前身份 + 生效日不足以支撑跨期查询：一张 2025 年 6 月的发票
-- 该按小规模还是按一般纳税人算，取决于**当时**的身份。
-- 每次变更追加一条，历史期间自动闭合。
CREATE TABLE vat_status_history (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  status         TEXT    NOT NULL CHECK (status IN ('general','small_scale')),
  effective_from TEXT    NOT NULL,               -- 含当日
  effective_to   TEXT    NOT NULL DEFAULT '',    -- 含当日；'' 表示至今有效
  note           TEXT    NOT NULL DEFAULT '',
  created_at     TEXT    NOT NULL
);
CREATE INDEX ix_vat_status_from ON vat_status_history(effective_from);

-- 把当前身份作为第一条历史记录写入
INSERT INTO vat_status_history (status, effective_from, effective_to, note, created_at)
SELECT vat_status, vat_status_effective_from, '',
       '由建账时的纳税人身份迁移而来', strftime('%Y-%m-%dT%H:%M:%SZ','now')
  FROM book WHERE id = 1;

-- 6) tax_type 保留但改名为历史字段的语义说明。
--    SQLite 不支持改列注释，这里用一条记录说明：应用不再读它，
--    仅保留以免破坏已有备份的兼容性。
