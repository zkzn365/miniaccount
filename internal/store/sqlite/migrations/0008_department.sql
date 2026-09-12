-- ---------------------------------------------------------------------------
-- 部门档案
--
-- 「部门」是辅助核算的四个维度之一，但原先**没有对应的档案表**：
-- ledger_entry.dept_id、employee.dept_id 都只是一个自由整数。
-- 后果是：
--
--   1. 用户得凭空记住「1 是管理部门、2 是销售部」，填错也不报错
--   2. 部门费用表只能显示「部门#1」，看不出是什么部门
--   3. 期末结转、工资计提这些聚合分录要带部门时无从取值
--
-- 员工/项目同理，但部门是最先撞上的一个（多数费用科目都要求部门），
-- 因此先补它。员工维度已经由 employee 表承担，
-- 项目维度等有实际需求时再补。
-- ---------------------------------------------------------------------------

CREATE TABLE department (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  code       TEXT    NOT NULL UNIQUE,
  name       TEXT    NOT NULL,
  -- 上级部门，支持两级以上；NULL 表示顶层
  parent_id  INTEGER REFERENCES department(id),
  is_enabled INTEGER NOT NULL DEFAULT 1 CHECK (is_enabled IN (0,1)),
  remark     TEXT    NOT NULL DEFAULT '',
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at TEXT    NOT NULL,
  updated_at TEXT    NOT NULL
);
CREATE INDEX ix_department_parent ON department(parent_id);

-- 种一个默认部门。
--
-- 为什么必须种：绝大多数费用科目（管理费用、销售费用的各个明细）
-- 都声明了按部门辅助核算，而小微企业在起步阶段往往只有一个
-- 「管理部门」的概念。不种的话用户第一次记费用就会撞上
-- 「缺少必需的辅助核算」，而账套里连一个可选部门都没有。
INSERT INTO department (code, name, remark, sort_order, created_at, updated_at)
VALUES ('001', '管理部门', '默认部门；可按实际组织架构调整', 10,
        strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now'));
