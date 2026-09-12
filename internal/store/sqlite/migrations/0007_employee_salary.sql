-- ---------------------------------------------------------------------------
-- 员工档案补上「标准工资」与扣除信息
--
-- 原设计把工资金额完全放在每次生成的工资单里（BuildRunInput.Amounts），
-- 员工表只存身份与社保方案。理由是「工资每月可能不同」——
-- 这个理由对，但推不出「员工表不该有默认值」：
--
--   1. 绝大多数员工的绝大多数月份，基本工资是不变的；
--      每月重敲一遍等于把「默认值」这件事推给用户
--   2. 专项附加扣除（子女教育、赡养老人…）是**年度不变**的，
--      完全不该每月填 —— 它在自然人电子税务局里是按年确认的
--   3. 界面上的「生成工资单」应当一键出结果，再让人改个别项，
--      而不是让人先填满一张空表
--
-- 因此这里把「标准值」放进员工档案，生成工资单时作为默认值带入，
-- 逐月可变的部分（加班费、考勤扣款）仍在工资单上填。
-- ---------------------------------------------------------------------------

ALTER TABLE employee ADD COLUMN phone              TEXT    NOT NULL DEFAULT '';
-- 标准月基本工资（分）
ALTER TABLE employee ADD COLUMN base_salary        INTEGER NOT NULL DEFAULT 0;
-- 社保缴费基数（分）。0 表示按基本工资。
--
-- 单独一列是必要的：很多小微企业按**最低基数**缴纳，
-- 而最低基数与实发工资无关，拿基本工资硬算会算错。
ALTER TABLE employee ADD COLUMN si_base            INTEGER NOT NULL DEFAULT 0;
-- 公积金缴费基数（分）。0 表示按社保基数。
ALTER TABLE employee ADD COLUMN hfb_base           INTEGER NOT NULL DEFAULT 0;
-- 每月专项附加扣除合计（分）：子女教育 + 赡养老人 + 住房贷款利息 …
ALTER TABLE employee ADD COLUMN special_additional INTEGER NOT NULL DEFAULT 0;
