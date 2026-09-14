package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"miniaccount/internal/ai/aiprovider"
	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/ai"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
)

// AIRepo 是 AI 辅助记账的持久化访问。
type AIRepo struct{ db *DB }

// AI 返回 AI 仓储。
func (db *DB) AI() *AIRepo { return &AIRepo{db: db} }

// AI 相关错误。
var (
	ErrAIProviderNotFound = errors.New("ai: 模型服务配置不存在")
	ErrNoDefaultProvider  = errors.New("ai: 未设置默认模型服务")
)

// ---------------------------------------------------------------------------
// 服务配置
// ---------------------------------------------------------------------------

// AIProviderConfig 是一条模型服务配置。
//
// APIKey 不参与任何列表/导出：它只在本机用于发请求。
type AIProviderConfig struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // local | cloud
	BaseURL   string `json:"baseUrl"`
	Model     string `json:"model"`
	APIKey    string `json:"apiKey"`
	Enabled   bool   `json:"enabled"`
	IsDefault bool   `json:"isDefault"`
	TimeoutMS int    `json:"timeoutMs"`
	Remark    string `json:"remark"`
}

// KindValue 返回部署形态枚举。
func (c AIProviderConfig) KindValue() aiprovider.Kind {
	if c.Kind == string(aiprovider.KindCloud) {
		return aiprovider.KindCloud
	}
	return aiprovider.KindLocal
}

// SaveProvider 新增或更新一条服务配置。
//
// is_default 互斥由部分唯一索引保证，因此设置默认值前先清掉旧的 ——
// 否则第二条默认配置会撞唯一约束，报出一个用户看不懂的数据库错误。
func (r *AIRepo) SaveProvider(ctx context.Context, c *AIProviderConfig) (int64, error) {
	if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.BaseURL) == "" ||
		strings.TrimSpace(c.Model) == "" {
		return 0, fmt.Errorf("ai: 服务名、地址、模型名都不能为空")
	}
	if c.Kind != string(aiprovider.KindLocal) && c.Kind != string(aiprovider.KindCloud) {
		return 0, fmt.Errorf("ai: 部署形态必须是 local 或 cloud")
	}
	if c.TimeoutMS <= 0 {
		c.TimeoutMS = int(aiprovider.DefaultTimeout / time.Millisecond)
	}

	var id int64
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		if c.IsDefault {
			if _, err := tx.Exec(ctx,
				`UPDATE ai_provider SET is_default = 0 WHERE is_default = 1`); err != nil {
				return err
			}
		}
		if c.ID == 0 {
			res, err := tx.Exec(ctx, `
				INSERT INTO ai_provider (name, kind, base_url, model, api_key,
					enabled, is_default, timeout_ms, remark, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				c.Name, c.Kind, c.BaseURL, c.Model, nullString(&c.APIKey),
				boolInt(c.Enabled), boolInt(c.IsDefault), c.TimeoutMS, c.Remark,
				nowString(), nowString())
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		}
		res, err := tx.Exec(ctx, `
			UPDATE ai_provider SET name = ?, kind = ?, base_url = ?, model = ?,
				api_key = ?, enabled = ?, is_default = ?, timeout_ms = ?,
				remark = ?, updated_at = ?
			 WHERE id = ?`,
			c.Name, c.Kind, c.BaseURL, c.Model, nullString(&c.APIKey),
			boolInt(c.Enabled), boolInt(c.IsDefault), c.TimeoutMS, c.Remark,
			nowString(), c.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: id=%d", ErrAIProviderNotFound, c.ID)
		}
		id = c.ID
		return nil
	})
	if err != nil {
		return 0, err
	}
	c.ID = id
	return id, nil
}

// Providers 返回全部服务配置。
func (r *AIRepo) Providers(ctx context.Context) ([]AIProviderConfig, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT id, name, kind, base_url, model, COALESCE(api_key, ''),
		       enabled, is_default, timeout_ms, remark
		  FROM ai_provider ORDER BY is_default DESC, id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []AIProviderConfig
	for rows.Next() {
		var c AIProviderConfig
		var enabled, isDefault int
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.BaseURL, &c.Model,
			&c.APIKey, &enabled, &isDefault, &c.TimeoutMS, &c.Remark); err != nil {
			return nil, err
		}
		c.Enabled, c.IsDefault = enabled != 0, isDefault != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// Provider 按 id 取一条配置。
func (r *AIRepo) Provider(ctx context.Context, id int64) (*AIProviderConfig, error) {
	all, err := r.Providers(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("%w: id=%d", ErrAIProviderNotFound, id)
}

// DefaultProvider 返回默认服务配置。
//
// 没有配置时返回 aiprovider.Disabled 而不是 nil：
// nil 会让每个调用点都要判空，漏一处就是崩溃。
func (r *AIRepo) DefaultProvider(ctx context.Context) (aiprovider.Provider, error) {
	all, err := r.Providers(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range all {
		if c.IsDefault && c.Enabled {
			return c.Build()
		}
	}
	// 没有默认但有一个启用的，就用它 —— 只有一个服务时
	// 强迫用户再点一次「设为默认」是多余的仪式
	for _, c := range all {
		if c.Enabled {
			return c.Build()
		}
	}
	if len(all) == 0 {
		return aiprovider.Disabled{Reason: "还没有配置模型服务 —— " +
			"去「设置 → AI 记账助手」填一个（服务地址 + 模型名）"}, nil
	}
	// ★ 不能只说「已停用」。
	//
	// 用户看到「所有模型服务都已停用」时的第一反应是「我明明配好了」——
	// 而配置页上那行「已停用」是个灰色小徽标，跟「默认」「本地」
	// 混在一列里，没有人会把它读成「你需要点一下」。
	// 所以这里把**数量**和**下一步**都写出来。
	return aiprovider.Disabled{Reason: fmt.Sprintf(
		"配置了 %d 个模型服务，但都处于停用状态 —— "+
			"去「设置 → AI 记账助手」把要用的那个点「启用」", len(all))}, nil
}

// Build 由配置构造可用的 Provider。
func (c AIProviderConfig) Build() (aiprovider.Provider, error) {
	return aiprovider.NewOpenAICompat(aiprovider.Options{
		Name: c.Name, Model: c.Model, BaseURL: c.BaseURL, APIKey: c.APIKey,
		Kind: c.KindValue(), Timeout: time.Duration(c.TimeoutMS) * time.Millisecond,
	})
}

// DeleteProvider 删除一条配置；已产生的审计记录不受影响（冗余保存了名字）。
func (r *AIRepo) DeleteProvider(ctx context.Context, id int64) error {
	return r.db.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.Exec(ctx, `DELETE FROM ai_provider WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: id=%d", ErrAIProviderNotFound, id)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// 建议审计
// ---------------------------------------------------------------------------

// Record 写入一条建议，实现 aiprovider.Auditor。
func (r *AIRepo) Record(ctx context.Context, rec aiprovider.SuggestionRecord) (int64, error) {
	proposed := ""
	if rec.Proposed != nil {
		if b, err := json.Marshal(rec.Proposed); err == nil {
			proposed = string(b)
		}
	}
	conf := rec.Confidence
	if conf == 0 && rec.Proposed != nil {
		conf = rec.Proposed.Confidence
	}
	sum := rec.Checksum
	if sum == "" && rec.Proposed != nil {
		sum = rec.Proposed.Checksum()
	}
	layer := rec.Layer
	if layer == "" {
		layer = ai.LayerAI
	}

	var id int64
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.Exec(ctx, `
			INSERT INTO ai_suggestion (target_type, target_id, provider_name, model,
				layer, prompt_digest, raw_response, proposed_json, checksum,
				confidence, status, reject_reason, tokens_in, tokens_out, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			rec.TargetType, nullInt64(rec.TargetID), rec.Provider, rec.Model,
			string(layer), rec.PromptDigest, rec.RawResponse, proposed, sum,
			conf, rec.Status, rec.RejectReason, rec.TokensIn, rec.TokensOut, nowString())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// SuggestionRow 是审计表的一行，供界面回溯。
type SuggestionRow struct {
	ID           int64    `json:"id"`
	TargetType   string   `json:"targetType"`
	TargetID     *int64   `json:"targetId"`
	ProviderName string   `json:"providerName"`
	Model        string   `json:"model"`
	Layer        ai.Layer `json:"layer"`
	Checksum     string   `json:"checksum"`
	Confidence   float64  `json:"confidence"`
	Status       string   `json:"status"`
	RejectReason string   `json:"rejectReason"`
	Decision     string   `json:"decision"`
	FinalVoucher *int64   `json:"finalVoucher"`
	TokensIn     int      `json:"tokensIn"`
	TokensOut    int      `json:"tokensOut"`
	CreatedAt    string   `json:"createdAt"`
	DecidedAt    *string  `json:"decidedAt"`
}

// Suggestions 返回最近的建议记录。
func (r *AIRepo) Suggestions(ctx context.Context, limit int) ([]SuggestionRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT id, target_type, target_id, provider_name, model, layer, checksum,
		       confidence, status, reject_reason, COALESCE(decision, ''),
		       final_voucher_id, tokens_in, tokens_out, created_at, decided_at
		  FROM ai_suggestion ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []SuggestionRow
	for rows.Next() {
		var s SuggestionRow
		var targetID, finalVoucher sql.NullInt64
		var decidedAt sql.NullString
		if err := rows.Scan(&s.ID, &s.TargetType, &targetID, &s.ProviderName, &s.Model,
			&s.Layer, &s.Checksum, &s.Confidence, &s.Status, &s.RejectReason,
			&s.Decision, &finalVoucher, &s.TokensIn, &s.TokensOut,
			&s.CreatedAt, &decidedAt); err != nil {
			return nil, err
		}
		s.TargetID = toNullInt64(targetID)
		s.FinalVoucher = toNullInt64(finalVoucher)
		if decidedAt.Valid {
			v := decidedAt.String
			s.DecidedAt = &v
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Decide 记录人工处置，实现 aiprovider.Auditor。
func (r *AIRepo) Decide(ctx context.Context, id int64, status aiprovider.Decision,
	finalVoucherID *int64, rejectReason string) error {

	return r.db.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE ai_suggestion
			   SET decision = ?, final_voucher_id = ?,
			       reject_reason = CASE WHEN ? <> '' THEN ? ELSE reject_reason END,
			       decided_at = ?
			 WHERE id = ?`,
			string(status), nullInt64(finalVoucherID), rejectReason, rejectReason,
			nowString(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: 建议 id=%d", sql.ErrNoRows, id)
		}
		return nil
	})
}

// Feedback 记录人工对提议做的字段级修改。
//
// ★ 这是学习闭环的核心数据。用户把科目从 560206 改成 560210，
// 说明在**这个账套里**这类业务该记 560210 —— 依赖任何提示词技巧。
func (r *AIRepo) Feedback(ctx context.Context, suggestionID int64,
	fieldPath, before, after string) error {

	return r.db.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO ai_feedback (suggestion_id, field_path, before_value,
				after_value, created_at) VALUES (?,?,?,?,?)`,
			suggestionID, fieldPath, before, after, nowString())
		return err
	})
}

// AIStats 是 AI 使用统计，用于界面上的「AI 表现」面板。
type AIStats struct {
	Total int `json:"total"`
	// Proposed 是**模型真的返回了可解析结构**的次数。
	//
	// ★ 必须与 Total 分开统计。把「模型连不上」也算成护栏失误，
	// 会让一个只是没启动的服务看起来像个烂模型 —— 而这两件事的
	// 处置方式完全不同：前者去检查服务，后者才需要换模型或调提示词。
	Proposed int `json:"proposed"`
	// Valid 是通过全部护栏的次数。
	Valid    int `json:"valid"`
	Accepted int `json:"accepted"`
	Modified int `json:"modified"`
	Rejected int `json:"rejected"`
	// TokensIn / TokensOut 用于估算成本（云端）或负载（本地）。
	TokensIn  int64 `json:"tokensIn"`
	TokensOut int64 `json:"tokensOut"`
}

// AcceptanceRate 返回采纳率（含改后采纳）。
func (s AIStats) AcceptanceRate() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Accepted+s.Modified) / float64(s.Total)
}

// GuardrailPassRate 返回护栏通过率。
//
// ★ 这是判断「模型能不能用」最直接的指标。
// 一个通过率 40% 的模型，用户点三次才中一次，还不如手录。
//
// 分母是**模型真的给出了结构化提议**的次数，不含网络失败与格式错误 ——
// 见 AIStats.Proposed 的说明。没有一次成功解析时返回 0。
func (s AIStats) GuardrailPassRate() float64 {
	if s.Proposed == 0 {
		return 0
	}
	return float64(s.Valid) / float64(s.Proposed)
}

// TransportFailures 返回连解析都没走到就失败的次数（服务不可用、超时、返回自由文本）。
func (s AIStats) TransportFailures() int { return s.Total - s.Proposed }

// Stats 统计 AI 表现。
func (r *AIRepo) Stats(ctx context.Context) (*AIStats, error) {
	var s AIStats
	// proposed_json 非空 ⇔ 模型返回了可解析的结构。
	// 用这个字段而不是另加一列状态：它本身就是「解析成功」的充分证据，
	// 多存一个可能与之不一致的标志位只会带来新的不一致。
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(proposed_json <> ''), 0),
		       COALESCE(SUM(status = 'valid'), 0),
		       COALESCE(SUM(decision = 'accepted'), 0),
		       COALESCE(SUM(decision = 'modified'), 0),
		       COALESCE(SUM(decision = 'rejected'), 0),
		       COALESCE(SUM(tokens_in), 0),
		       COALESCE(SUM(tokens_out), 0)
		  FROM ai_suggestion`).
		Scan(&s.Total, &s.Proposed, &s.Valid, &s.Accepted, &s.Modified, &s.Rejected,
			&s.TokensIn, &s.TokensOut)
	if err != nil {
		return nil, translateErr(err)
	}
	return &s, nil
}

// ---------------------------------------------------------------------------
// 历史检索：★ 性价比最高的一层
// ---------------------------------------------------------------------------

// SimilarVouchers 检索与给定文本最相似的历史凭证，实现 aiprovider.Retriever。
//
// # 为什么这一层比模型更重要
//
// 小微企业 80% 的银行流水是重复的：同一个客户、同一个供应商、
// 同一个金额量级、同一类摘要。用户上次怎么记的，这次就该怎么记。
//
// 这条规则**不依赖任何模型**：
//   - 用户把 AI 整个关掉，历史检索照样能把大部分流水自动填好
//   - 模型给出的建议若与历史不一致，界面应当提醒
//   - 历史还是提示词里最有用的部分，能显著压低模型的出错率
//
// # 打分
//
// 总分 = 对方户名(0~4) + 摘要关键词重合(0~3) + 金额量级(0~2) + 时间新鲜度(0~1)
//
// 权重这样分配的理由：**对方是谁**最能决定怎么记账；
// 摘要关键词次之；金额量级只影响「是买设备还是买文具」这类判断；
// 时间新鲜度权重最低 —— 去年的记法对今年仍有参考价值，
// 只在其它条件相同时才作为决胜。
func (r *AIRepo) SimilarVouchers(ctx context.Context, text, counterparty string,
	amountMoney int64, limit int) ([]aiprovider.Example, error) {

	if limit <= 0 {
		limit = 4
	}
	cands, err := r.historyCandidates(ctx, maxCandidates)
	if err != nil {
		return nil, err
	}

	// 参考日期用今天：越近的历史越相关
	today := calendar.Today()
	textKeys := keywords(text)

	type scored struct {
		ex    aiprovider.Example
		score float64
	}
	out := make([]scored, 0, len(cands))
	for _, c := range cands {
		s := 0.0
		// ① 对方户名
		if counterparty != "" && c.counterparty != "" {
			switch {
			case c.counterparty == counterparty:
				s += 4
			case strings.Contains(c.counterparty, counterparty) ||
				strings.Contains(counterparty, c.counterparty):
				s += 2.5
			}
		}
		// ② 摘要关键词
		if len(textKeys) > 0 {
			hit := 0
			for k := range textKeys {
				if _, ok := keywords(c.text)[k]; ok {
					hit++
				}
			}
			if hit > 0 {
				ratio := float64(hit) / float64(len(textKeys))
				if ratio > 1 {
					ratio = 1
				}
				s += 3 * ratio
			}
		}
		// ③ 金额量级：同一数量级给满分，差一个数量级给一半
		if amountMoney != 0 && c.amount != 0 {
			if d := magnitudeDiff(amountMoney, c.amount); d <= 1 {
				s += 2 - float64(d)*0.5
			}
		}
		// ④ 时间新鲜度：两年内线性衰减
		if days := daysBetween(c.date, today); days >= 0 {
			switch {
			case days <= 365:
				s += 1
			case days <= 730:
				s += 1 - float64(days-365)/365
			}
		}
		if s <= 0 {
			continue
		}
		ex := c.example
		ex.Score = normalizeScore(s)
		out = append(out, scored{ex: ex, score: s})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	if len(out) > limit {
		out = out[:limit]
	}
	res := make([]aiprovider.Example, 0, len(out))
	for _, s := range out {
		res = append(res, s.ex)
	}
	return res, nil
}

// Similar 实现 aiprovider.Retriever，语义同 SimilarVouchers。
//
// 保留两个名字是刻意的：SimilarVouchers 说明了返回的是**凭证**，
// 便于读存储层代码的人一眼看懂；Similar 则是编排层接口要求的短名。
func (r *AIRepo) Similar(ctx context.Context, text, counterparty string,
	amountMoney int64, limit int) ([]aiprovider.Example, error) {
	return r.SimilarVouchers(ctx, text, counterparty, amountMoney, limit)
}

// maxCandidates 是参与打分的候选凭证上限。
//
// 打分在 Go 侧做（SQLite 没有中文分词），
// 因此先把候选集收窄再算分，避免把整本账读进内存。
const maxCandidates = 2000

// historyCandidate 是一条候选历史凭证（含其触发的原始描述）。
type historyCandidate struct {
	example      aiprovider.Example
	text         string
	counterparty string
	amount       int64
	date         calendar.Date
}

// historyCandidates 取最近的一批已过账、非结转/冲销的凭证。
//
// 排除结转与冲销：它们是期末的机械动作，不是「业务怎么记」的范例，
// 拿它们当参考会把模型带偏。
func (r *AIRepo) historyCandidates(ctx context.Context, limit int) ([]historyCandidate, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT v.id, v.no, v.biz_date, v.remark, v.source, v.source_id
		  FROM voucher v
		 WHERE v.status = 'posted'
		   AND v.source NOT IN ('closing')
		   AND v.reverses_id IS NULL
		 ORDER BY v.biz_date DESC, v.id DESC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, translateErr(err)
	}

	type head struct {
		id       int64
		no, date string
		remark   string
		source   string
		sourceID sql.NullInt64
	}
	var heads []head
	for rows.Next() {
		var h head
		if err := rows.Scan(&h.id, &h.no, &h.date, &h.remark, &h.source, &h.sourceID); err != nil {
			rows.Close()
			return nil, err
		}
		heads = append(heads, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// ★ 单连接池：必须先关 rows 再继续查询
	rows.Close()

	out := make([]historyCandidate, 0, len(heads))
	for _, h := range heads {
		entries, err := loadEntries(ctx, r.db.sql, h.id)
		if err != nil {
			return nil, err
		}
		d, err := calendar.Parse(h.date)
		if err != nil {
			continue
		}

		ex := aiprovider.Example{VoucherNo: h.no, Date: h.date, Remark: h.remark}
		var amount int64
		for _, e := range entries {
			ex.Lines = append(ex.Lines, aiprovider.ExampleLine{
				Summary: e.Summary, AccountCode: e.AccountCode,
				Debit: e.Debit, Credit: e.Credit,
				ContactName: r.contactName(ctx, e.Aux.ContactID),
			})
			// 用最大单边金额代表这笔业务的量级
			if v := int64(e.Debit); v > amount {
				amount = v
			}
			if v := int64(e.Credit); v > amount {
				amount = v
			}
		}
		ex.Amount = money.Money(amount)

		// 银行流水来源的凭证带原始摘要，是最有价值的范例
		var text, cp string
		if h.source == string(voucher.SourceBank) && h.sourceID.Valid {
			text, cp = r.flowText(ctx, h.sourceID.Int64)
		}
		if text == "" {
			text = h.remark
		}
		ex.Text = text

		out = append(out, historyCandidate{
			example: ex, text: text, counterparty: cp,
			amount: amount, date: d,
		})
	}
	return out, nil
}

// flowText 取银行流水的摘要与对方户名。
func (r *AIRepo) flowText(ctx context.Context, id int64) (text, counterparty string) {
	var summary, cp sql.NullString
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT summary, counterparty_name FROM bank_flow WHERE id = ?`, id).
		Scan(&summary, &cp)
	if err != nil {
		return "", ""
	}
	text = strings.TrimSpace(summary.String + " " + cp.String)
	return text, strings.TrimSpace(cp.String)
}

// contactName 取往来单位名称；查不到返回空串。
func (r *AIRepo) contactName(ctx context.Context, id *int64) string {
	if id == nil {
		return ""
	}
	var name string
	if err := r.db.sql.QueryRowContext(ctx,
		`SELECT name FROM contact WHERE id = ?`, *id).Scan(&name); err != nil {
		return ""
	}
	return name
}

// ---------------------------------------------------------------------------
// 打分辅助
// ---------------------------------------------------------------------------

// stopWords 是记账摘要里的高频无信息词。
//
// 「支付」「收到」「转账」这类词几乎每笔都有，
// 让它们参与关键词重合会算出虚高的相似度。
var stopWords = map[string]bool{
	"支付": true, "收到": true, "收款": true, "付款": true, "转账": true,
	"汇款": true, "入账": true, "扣款": true, "结算": true, "交易": true,
	"公司": true, "有限": true, "有限公": true, "有限公司": true,
	"摘要": true, "备注": true, "用途": true,
}

// keywords 把一段文本切成关键词集合。
//
// 中文没有空格，这里用**二元切分**（bigram）：
// 「杭州某某科技」→ 杭州/州某/某某/某科/科技。
// 不做分词是刻意的 —— 引入词典会带来维护成本与许可证问题，
// 而二元切分对「找相似摘要」这个任务已经够用。
func keywords(s string) map[string]bool {
	s = strings.TrimSpace(s)
	out := map[string]bool{}
	if s == "" {
		return out
	}
	// 先按非文字字符切开，避免跨字段拼出假关键词
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == '，' ||
			r == '。' || r == '、' || r == ';' || r == '；' || r == '/' ||
			r == '-' || r == '_' || r == ':' || r == '：' || r == '|'
	}) {
		runes := []rune(seg)
		if len(runes) == 1 {
			continue
		}
		for i := 0; i+2 <= len(runes); i++ {
			g := string(runes[i : i+2])
			if stopWords[g] {
				continue
			}
			out[g] = true
		}
	}
	return out
}

// magnitudeDiff 返回两个金额相差的数量级（0 表示同一数量级）。
func magnitudeDiff(a, b int64) int {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	if a == 0 || b == 0 {
		return 99
	}
	d := 0
	for a >= 10 && b >= 10 {
		a /= 10
		b /= 10
	}
	for a >= 10 {
		a /= 10
		d++
	}
	for b >= 10 {
		b /= 10
		d++
	}
	return d
}

// normalizeScore 把原始得分压到 0~1，便于展示与排序。
//
// 满分是 10（4+3+2+1），但实际很难全部拿满，
// 因此把 6 分以上都视作「高度相似」，避免界面上一片 0.30 让人无所适从。
func normalizeScore(s float64) float64 {
	const full = 6.0
	v := s / full
	if v > 1 {
		v = 1
	}
	return float64(int(v*100+0.5)) / 100
}

// daysBetween 返回 from 到 to 的天数差（to 更晚为正）。
//
// calendar.Date 是纯日期，没有时刻概念，因此这里用「儒略日序号」相减，
// 不引入 time.Duration —— 后者会把日期重新拖回时区问题里，
// 而本工程刻意用纯日期类型就是为了摆脱时区 off-by-one。
func daysBetween(from, to calendar.Date) int {
	return dayNumber(to) - dayNumber(from)
}

// dayNumber 返回该日期自公元 1 年 1 月 1 日起的天数序号。
func dayNumber(d calendar.Date) int {
	y, m, dd := d.Year, d.Month, d.Day
	// Howard Hinnant 的 days_from_civil：把 3 月当作一年的开始，
	// 闰日就落到了年末，不必单独处理。
	if m <= 2 {
		y--
	}
	era := y / 400
	if y < 0 {
		era = (y - 399) / 400
	}
	yoe := y - era*400
	mp := (m + 9) % 12
	doy := (153*mp+2)/5 + dd - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return era*146097 + doe
}

// ---------------------------------------------------------------------------
// 上下文来源：实现 aiprovider.ContextSource
// ---------------------------------------------------------------------------

// LedgerContext 返回校验用的科目树、期间表与往来档案。
func (r *AIRepo) LedgerContext(ctx context.Context) (*ledger.Context, error) {
	tree, err := r.db.Accounts().Tree(ctx)
	if err != nil {
		return nil, err
	}
	cal, err := r.db.Periods().Load(ctx)
	if err != nil {
		return nil, err
	}
	kinds, err := r.db.Contacts().Kinds(ctx)
	if err != nil {
		return nil, err
	}
	return &ledger.Context{Accounts: tree, Periods: cal, ContactKinds: kinds}, nil
}

// BookContext 返回账套背景。
func (r *AIRepo) BookContext(ctx context.Context) (aiprovider.BookContext, error) {
	b, err := r.db.Books().Get(ctx)
	if err != nil {
		return aiprovider.BookContext{}, err
	}
	out := aiprovider.BookContext{
		CompanyName: b.CompanyName,
		Standard:    "小企业会计准则",
		Currency:    "人民币（CNY）",
	}
	switch b.TaxType {
	case TaxTypeSmall:
		out.TaxType = "小规模纳税人"
	default:
		out.TaxType = "一般纳税人"
	}
	// 当前可记账期间取「最早的一个 open 期间」：
	// AI 补录历史流水时应当从最早的未结账期间开始，
	// 而不是默认写到今天所在的期间。
	cal, err := r.db.Periods().Load(ctx)
	if err == nil {
		for _, k := range cal.Keys() {
			if p, ok := cal.Get(k.Year, k.Month); ok && p.Status == period.StatusOpen {
				out.PeriodDesc = fmt.Sprintf("%d年%02d月", k.Year, k.Month)
				break
			}
		}
	}
	return out, nil
}

// Accounts 返回全部可记账科目，供模型在闭集里选择。
//
// ★ 只给「明细 + 启用」的科目：把 190 个科目连同汇总科目一起发过去，
// 模型会认真地往「管理费用」这种汇总科目上记，然后被护栏拦下。
// 提前收窄候选集，比事后拦下更省一次往返。
func (r *AIRepo) Accounts(ctx context.Context) ([]aiprovider.AccountBrief, error) {
	tree, err := r.db.Accounts().Tree(ctx)
	if err != nil {
		return nil, err
	}
	all := tree.All()
	byCode := make(map[string]*account.Account, len(all))
	for _, a := range all {
		byCode[a.Code] = a
	}

	var out []aiprovider.AccountBrief
	for _, a := range tree.EnabledLeaves() {
		aux := make([]string, 0, len(a.AuxTypes))
		for _, t := range a.AuxTypes {
			aux = append(aux, t.Label())
		}
		dir := "借"
		if a.BalanceDir == account.DirCredit {
			dir = "贷"
		}
		out = append(out, aiprovider.AccountBrief{
			Code: a.Code, Name: a.Name,
			FullName:  a.FullName(byCode),
			Direction: dir,
			AuxTypes:  aux,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

// Contacts 返回全部往来单位。
func (r *AIRepo) Contacts(ctx context.Context) ([]aiprovider.ContactBrief, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT id, name, kind, short_name FROM contact
		 WHERE is_enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []aiprovider.ContactBrief
	for rows.Next() {
		var c aiprovider.ContactBrief
		var kind, alias string
		if err := rows.Scan(&c.ID, &c.Name, &kind, &alias); err != nil {
			return nil, err
		}
		c.Kind = contactKindLabel(kind)
		// short_name 是单个简称，同时按常见分隔符拆一次，
		// 兼容用户把几个别名写在一起的情况
		if alias != "" {
			c.Aliases = strings.FieldsFunc(alias, func(r rune) bool {
				return r == ',' || r == '，' || r == ';' || r == '；' || r == ' '
			})
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Departments 返回部门清单（给 AI 工具用）。
//
// ★ 只列**启用**的：停用的部门在新建凭证时选不到，
// 让模型选到它，计提那天会被护栏打回。
func (r *AIRepo) Departments(ctx context.Context) ([]aiprovider.DepartmentBrief, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT id, code, name, parent_id FROM department
		 WHERE is_enabled = 1 ORDER BY sort_order, code, id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	type row struct {
		id     int64
		code   string
		name   string
		parent sql.NullInt64
	}
	var list []row
	byID := map[int64]string{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.code, &x.name, &x.parent); err != nil {
			return nil, translateErr(err)
		}
		list = append(list, x)
		byID[x.id] = x.name
	}
	if err := rows.Err(); err != nil {
		return nil, translateErr(err)
	}

	out := make([]aiprovider.DepartmentBrief, 0, len(list))
	for _, x := range list {
		full := x.name
		// 拼上上级名：账套里出现「销售部」和「销售部/华东区」时，
		// 只给短名模型分不清是哪一个
		for p := x.parent; p.Valid; {
			parent, ok := byID[p.Int64]
			if !ok {
				break
			}
			full = parent + "/" + full
			// 只往上追一层；更深的层级在 AI 场景里没必要，
			// 而且没有 parent 的 parent 可查时继续追会绕成环
			break
		}
		out = append(out, aiprovider.DepartmentBrief{
			ID: x.id, Code: x.code, Name: x.name, FullName: full,
		})
	}
	return out, nil
}

// Employees 返回员工清单（给 AI 工具用）。
//
// ★ 连**离职**的一起返回，由工具那边按参数过滤。
//
// 只列在职的看起来更"干净"，但会漏掉真实场景：补记上个月给某人的报销，
// 而那个人这个月刚离职。工具给不出 id，模型就只能空着。
func (r *AIRepo) Employees(ctx context.Context) ([]aiprovider.EmployeeBrief, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT e.id, COALESCE(e.code,''), e.name, COALESCE(d.name,''),
		       COALESCE(e.leave_date,'')
		  FROM employee e
		  LEFT JOIN department d ON d.id = e.dept_id
		 ORDER BY e.id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	var out []aiprovider.EmployeeBrief
	for rows.Next() {
		var e aiprovider.EmployeeBrief
		var leaveDate string
		if err := rows.Scan(&e.ID, &e.Code, &e.Name, &e.DeptName, &leaveDate); err != nil {
			return nil, translateErr(err)
		}
		e.Left = strings.TrimSpace(leaveDate) != ""
		out = append(out, e)
	}
	return out, rows.Err()
}

func contactKindLabel(kind string) string {
	switch kind {
	case "customer":
		return "客户"
	case "supplier":
		return "供应商"
	case "employee":
		return "员工"
	case "shareholder":
		return "股东"
	default:
		return "其他单位"
	}
}

// ---------------------------------------------------------------------------
// 编译期接口断言
// ---------------------------------------------------------------------------

var (
	_ aiprovider.ContextSource = (*AIRepo)(nil)
	_ aiprovider.Retriever     = (*AIRepo)(nil)
	_ aiprovider.Auditor       = (*AIRepo)(nil)
)

// ---------------------------------------------------------------------------
// 提示词配置
// ---------------------------------------------------------------------------

// settingAIPrompt 是提示词定制在 setting 表里的键。
const settingAIPrompt = "ai_prompt_config"

// getSetting / putSetting 复用工资仓储上的同一套设置读写：
// 「设置」在库里就是一张 key-value 表，读写逻辑只该有一份。
func (r *AIRepo) getSetting(ctx context.Context, key string, out any) (bool, error) {
	pr := &PayrollRepo{db: r.db}
	return pr.getSetting(ctx, key, out)
}

// PromptConfig 返回提示词定制；没设置过时返回零值（= 全部用出厂默认）。
//
// ★ 存的是用户的**差异**，不是整份提示词。程序升级后默认提示词
// 改进了，没自定义过的账套自动跟着变 —— 这是应该的：
// 默认提示词是程序的一部分，不是用户数据。
func (r *AIRepo) PromptConfig(ctx context.Context) (ai.PromptConfig, error) {
	var c ai.PromptConfig
	if _, err := r.getSetting(ctx, settingAIPrompt, &c); err != nil {
		return ai.PromptConfig{}, err
	}
	return c.Normalize(), nil
}

// SavePromptConfig 保存提示词定制。
func (r *AIRepo) SavePromptConfig(ctx context.Context, c ai.PromptConfig) error {
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return err
	}
	return r.db.WithTx(ctx, func(tx *Tx) error {
		pr := &PayrollRepo{db: r.db}
		return pr.putSetting(ctx, tx, settingAIPrompt, c)
	})
}

// ResetPromptConfig 清掉定制，回到出厂默认。
func (r *AIRepo) ResetPromptConfig(ctx context.Context) error {
	return r.db.WithTx(ctx, func(tx *Tx) error {
		pr := &PayrollRepo{db: r.db}
		return pr.putSetting(ctx, tx, settingAIPrompt, ai.PromptConfig{})
	})
}

// ---------------------------------------------------------------------------
// 读取单条建议（采纳时要把它取回来）
// ---------------------------------------------------------------------------

// SuggestionDetail 是一条建议的完整内容，含结构化提议原文。
//
// ★ 采纳必须从**库里**取提议，而不是让界面把提议再传回来。
// 界面传回来的东西是可以被改的（甚至可以被伪造），而审计表里的
// proposed_json 是当时模型说的原话 —— 「人改了什么」与
// 「模型说了什么」必须分得开，这是事后追溯的全部意义。
type SuggestionDetail struct {
	SuggestionRow
	// Proposed 是解析后的提议；解析失败时为 nil。
	Proposed *ai.Proposal
	// ProposedJSON 是原始 JSON 文本，解析失败时用它报错。
	ProposedJSON string
}

// Suggestion 按 id 取一条建议。
func (r *AIRepo) Suggestion(ctx context.Context, id int64) (*SuggestionDetail, error) {
	var (
		d            SuggestionDetail
		targetID     sql.NullInt64
		finalVoucher sql.NullInt64
		decidedAt    sql.NullString
		raw          string
	)
	err := r.db.sql.QueryRowContext(ctx, `
		SELECT id, target_type, target_id, provider_name, model, layer, checksum,
		       confidence, status, reject_reason, COALESCE(decision, ''),
		       final_voucher_id, tokens_in, tokens_out, created_at, decided_at,
		       proposed_json
		  FROM ai_suggestion WHERE id = ?`, id).
		Scan(&d.ID, &d.TargetType, &targetID, &d.ProviderName, &d.Model, &d.Layer,
			&d.Checksum, &d.Confidence, &d.Status, &d.RejectReason, &d.Decision,
			&finalVoucher, &d.TokensIn, &d.TokensOut, &d.CreatedAt, &decidedAt, &raw)
	if err != nil {
		return nil, translateErr(err)
	}
	d.TargetID = toNullInt64(targetID)
	d.FinalVoucher = toNullInt64(finalVoucher)
	if decidedAt.Valid {
		v := decidedAt.String
		d.DecidedAt = &v
	}
	d.ProposedJSON = raw
	if p, perr := ai.Parse(raw); perr == nil {
		d.Proposed = p
	}
	return &d, nil
}
