-- ---------------------------------------------------------------------------
-- 增值税专栏与明细科目的结构修正
--
-- 依据：财政部《增值税会计处理规定》（财会〔2016〕22号）第一条。
-- 原文明确区分了两个层次：
--
--   在「应交税费」科目下设「应交增值税」「未交增值税」「预交增值税」
--   「待抵扣进项税额」「待认证进项税额」「待转销项税额」「增值税留抵税额」
--   「简易计税」「转让金融商品应交增值税」「代扣代交增值税」等**明细科目**；
--   其中「应交增值税」明细账内再设「进项税额」「销项税额抵减」「已交税金」
--   「转出未交增值税」「减免税款」「出口抵减内销产品应纳税额」「销项税额」
--   「出口退税」「进项税额转出」「转出多交增值税」等**专栏**。
--
-- 即：专栏是「应交增值税」这个明细账内部的**分析栏目**，
-- 与「应交税费」下的其他明细科目不是一回事。
--
-- 原先的科目表把两者混在了 222101 下面（17 个子科目 = 9 个专栏 + 8 个明细科目），
-- 并漏掉了「销项税额抵减」，还把「出口退税」的方向记成了借方。三处后果：
--
--   1. 应交增值税的多栏式明细账会凭空多出 8 栏，账面「应交增值税」
--      混进了本不属于它的待抵扣/待认证/简易计税等余额
--   2. 漏掉销项税额抵减 → 差额征税业务无处可记
--   3. 出口退税方向错 → 它会被当成借方专栏，而制度规定它是贷方专栏
--      （借：应收出口退税款 / 贷：应交税费—应交增值税（出口退税）），
--      于是应交增值税的专栏合计与余额都会算错
--
-- ## 这里为什么不用 is_preset 做守卫
--
-- is_preset 的含义是「准则预置不可删」，只有财政部规定的 66 个一级科目
-- 带这个标记；125 个建议明细科目都是 0（可改可删）。
-- 拿它当守卫会让整段迁移对明细科目完全失效。
--
-- 改用「编码 + 名称 + 父科目」三重匹配：三者同时命中才动手。
-- 这比 is_preset 更准 —— 用户把某个明细科目改了名去派别的用场，
-- 名称就对不上，迁移不会误伤。
--
-- 重编码是安全的：ledger_entry 等表引用的是 account.id 而不是 code。
-- ---------------------------------------------------------------------------

-- 1) 出口退税：借方 → 贷方（贷方专栏）
UPDATE account SET balance_dir = 'credit'
 WHERE code = '22210107'
   AND name = '应交增值税—出口退税'
   AND parent_id = (SELECT id FROM account WHERE code = '222101')
   AND balance_dir = 'debit';

-- 2) 补上漏掉的借方专栏「销项税额抵减」
INSERT INTO account (code, name, parent_id, level, is_leaf, root_type, category,
                     balance_dir, aux_types, is_enabled, is_preset, remark, sort_order)
SELECT '22210118', '应交增值税—销项税额抵减',
       (SELECT id FROM account WHERE code = '222101'), 3, 1,
       'liability', '负债类', 'debit', '', 1, 0,
       '★ 借方专栏：差额征税扣减销售额而减少的销项',
       590
 WHERE EXISTS (SELECT 1 FROM account WHERE code = '222101')
   AND NOT EXISTS (SELECT 1 FROM account WHERE code = '22210118');

-- 3) 把 8 个「明细科目」从「应交增值税」下移到「应交税费」下。
--    编码一并换成 222119–222126 —— 保留原编码 22210110–22210117 会误导人
--    以为它们仍挂在应交增值税下面（编码前缀就是科目层级）。
UPDATE account SET parent_id = (SELECT id FROM account WHERE code = '2221'),
                   code = '222119', level = 2, name = '应交税费—待抵扣进项税额', sort_order = 890
 WHERE code = '22210110' AND name = '应交增值税—待抵扣进项税额'
   AND NOT EXISTS (SELECT 1 FROM account WHERE code = '222119');

UPDATE account SET parent_id = (SELECT id FROM account WHERE code = '2221'),
                   code = '222120', level = 2, name = '应交税费—待认证进项税额', sort_order = 900
 WHERE code = '22210111' AND name = '应交增值税—待认证进项税额'
   AND NOT EXISTS (SELECT 1 FROM account WHERE code = '222120');

UPDATE account SET parent_id = (SELECT id FROM account WHERE code = '2221'),
                   code = '222121', level = 2, name = '应交税费—待转销项税额', sort_order = 910
 WHERE code = '22210112' AND name = '应交增值税—待转销项税额'
   AND NOT EXISTS (SELECT 1 FROM account WHERE code = '222121');

UPDATE account SET parent_id = (SELECT id FROM account WHERE code = '2221'),
                   code = '222122', level = 2, name = '应交税费—增值税留抵税额', sort_order = 920
 WHERE code = '22210113' AND name = '应交增值税—增值税留抵税额'
   AND NOT EXISTS (SELECT 1 FROM account WHERE code = '222122');

UPDATE account SET parent_id = (SELECT id FROM account WHERE code = '2221'),
                   code = '222123', level = 2, name = '应交税费—简易计税', sort_order = 930
 WHERE code = '22210114' AND name = '应交增值税—简易计税'
   AND NOT EXISTS (SELECT 1 FROM account WHERE code = '222123');

UPDATE account SET parent_id = (SELECT id FROM account WHERE code = '2221'),
                   code = '222124', level = 2, name = '应交税费—预交增值税', sort_order = 940
 WHERE code = '22210115' AND name = '应交增值税—预交增值税'
   AND NOT EXISTS (SELECT 1 FROM account WHERE code = '222124');

UPDATE account SET parent_id = (SELECT id FROM account WHERE code = '2221'),
                   code = '222125', level = 2, name = '应交税费—转让金融商品应交增值税', sort_order = 950
 WHERE code = '22210116' AND name = '应交增值税—转让金融商品应交增值税'
   AND NOT EXISTS (SELECT 1 FROM account WHERE code = '222125');

UPDATE account SET parent_id = (SELECT id FROM account WHERE code = '2221'),
                   code = '222126', level = 2, name = '应交税费—代扣代交增值税', sort_order = 960
 WHERE code = '22210117' AND name = '应交增值税—代扣代交增值税'
   AND NOT EXISTS (SELECT 1 FROM account WHERE code = '222126');

-- 3b) level 必须跟着编码走。
--
-- 这一条是踩出来的：编码从 8 位（22210110）改成 6 位（222119）之后，
-- 层级应当是 2 级而不是 3 级。漏改的后果不是「显示不好看」——
-- 科目树加载时会用「层级与编码长度一致」做完整性校验，直接**打不开账套**。
-- 迁移改了编码就必须同步改 level、parent_id、is_leaf 这三样。
UPDATE account SET level = 2 WHERE code IN
  ('222119','222120','222121','222122','222123','222124','222125','222126');

-- 4) 222101 / 2221 有子科目就必须是汇总科目（与科目表的既定规则一致）。
--    正常情况下它们本来就是 0；这一步只防「用户手工删光子科目后
--    又被标成明细科目」这类边界情况。
UPDATE account SET is_leaf = 0
 WHERE code = '222101'
   AND EXISTS (SELECT 1 FROM account WHERE parent_id = account.id);

UPDATE account SET is_leaf = 0
 WHERE code = '2221'
   AND EXISTS (SELECT 1 FROM account WHERE parent_id = account.id);
