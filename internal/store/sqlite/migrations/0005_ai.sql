-- ---------------------------------------------------------------------------
-- AI 辅助记账：服务配置、建议审计、学习反馈
--
-- 设计原则：
--
--   1. **AI 只提议，人只确认，账由 Go 写。** 这三张表里没有任何字段
--      能让模型直接影响总账 —— 它只能往 ai_suggestion 里写一条提议。
--
--   2. **审计必须完整。** 一条 AI 提议入账之后，事后要能回答：
--      哪个模型、什么输入、模型原话是什么、人改了什么、谁确认的。
--      缺任何一环，「AI 记错了账」就无法追溯，也无从改进。
--
--   3. **不存原始敏感数据。** prompt_digest 是提示词的 sha256，
--      不是原文 —— 银行流水摘要、对方户名不该在审计表里再存一份。
--      raw_response 存模型原话（排错必需），但可被隐私设置关闭。
-- ---------------------------------------------------------------------------

-- AI 服务配置。
CREATE TABLE ai_provider (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT    NOT NULL,                 -- 显示名，如「本地 Ollama」
  kind       TEXT    NOT NULL CHECK (kind IN ('local','cloud')),
  base_url   TEXT    NOT NULL,                 -- 不含 /chat/completions
  model      TEXT    NOT NULL,
  -- api_key 只对云端服务有意义。存的是**用户自己的**密钥，
  -- 不随备份外传（备份导出时会剔除，见 backup 模块）。
  api_key    TEXT,
  enabled    INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
  -- timeout_ms 允许为慢的本地模型单独放宽。
  timeout_ms INTEGER NOT NULL DEFAULT 180000,
  remark     TEXT    NOT NULL DEFAULT '',
  created_at TEXT    NOT NULL,
  updated_at TEXT    NOT NULL
);
-- 同时只能有一个默认服务
CREATE UNIQUE INDEX ux_ai_provider_default
  ON ai_provider(is_default) WHERE is_default = 1;

-- AI 建议审计。★ 必留表。
CREATE TABLE ai_suggestion (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  -- 建议针对的对象：bank_flow | invoice | expense_claim | freeform
  target_type     TEXT    NOT NULL,
  target_id       INTEGER,
  provider_id     INTEGER REFERENCES ai_provider(id),
  -- provider_name / model 冗余保存：服务配置会被改，
  -- 但「三个月前这条建议是哪个模型给的」不能跟着变。
  provider_name   TEXT    NOT NULL DEFAULT '',
  model           TEXT    NOT NULL DEFAULT '',
  -- 哪一层产出的：rule（规则）| history（历史检索）| ai（模型）
  layer           TEXT    NOT NULL CHECK (layer IN ('rule','history','ai')),
  -- 提示词指纹：只存 sha256，不存原文
  prompt_digest   TEXT    NOT NULL DEFAULT '',
  -- 模型原始返回，排错必需；隐私设置可令其为空
  raw_response    TEXT    NOT NULL DEFAULT '',
  -- 结构化提议（JSON），是重新校验与回放的依据
  proposed_json   TEXT    NOT NULL DEFAULT '',
  -- 提议内容指纹：同样输入重复提议时可判重
  checksum        TEXT    NOT NULL DEFAULT '',
  confidence      REAL    NOT NULL DEFAULT 0,
  -- valid 表示通过了全部护栏；invalid 表示被拦下，原因见 reject_reason
  status          TEXT    NOT NULL CHECK (status IN ('valid','invalid')),
  reject_reason   TEXT    NOT NULL DEFAULT '',
  -- 人工处置：accepted（原样采纳）| modified（改后采纳）| rejected（拒绝）
  decision        TEXT    CHECK (decision IN ('accepted','modified','rejected')),
  final_voucher_id INTEGER REFERENCES voucher(id),
  tokens_in       INTEGER NOT NULL DEFAULT 0,
  tokens_out      INTEGER NOT NULL DEFAULT 0,
  latency_ms      INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT    NOT NULL,
  decided_at      TEXT
);
CREATE INDEX ix_ai_sugg_target  ON ai_suggestion(target_type, target_id);
CREATE INDEX ix_ai_sugg_created ON ai_suggestion(created_at);
CREATE INDEX ix_ai_sugg_status  ON ai_suggestion(status, decision);

-- 学习闭环：记录人工对 AI 提议做的字段级修改。
--
-- ★ 这张表是「让 AI 越用越省」的全部依据。
-- 用户把 560206 改成 560210，这条记录比任何提示词调优都直接 ——
-- 它说明了在**这个账套里**，这类业务应该记哪个科目。
CREATE TABLE ai_feedback (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  suggestion_id INTEGER NOT NULL REFERENCES ai_suggestion(id) ON DELETE CASCADE,
  -- 字段路径，如 entries[1].account_code
  field_path    TEXT    NOT NULL,
  before_value  TEXT    NOT NULL DEFAULT '',
  after_value   TEXT    NOT NULL DEFAULT '',
  created_at    TEXT    NOT NULL
);
CREATE INDEX ix_ai_feedback_sugg ON ai_feedback(suggestion_id);
