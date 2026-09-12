package sqlite

import (
	"context"
	"fmt"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/vat"
)

// settingVATPolicies 是税率政策表在 setting 表里的键。
const settingVATPolicies = "vat_rate_policies"

// settingVATPolicySource 记录政策表**从哪来**，决定升级时能不能直接换掉。
//
// 没有这个标记，「内置表改了但用户库里还是旧的」就没法安全处理：
// 一律不换，用户永远用着旧规则（1% 优惠到期了还在按 1% 开票）；
// 一律强制换，用户自己改过的税率会被悄悄覆盖掉。
const settingVATPolicySource = "vat_rate_policy_source"

const (
	policySourceBuiltin = "builtin" // 内置默认表，用户没动过
	policySourceCustom  = "custom"  // 用户改过或导入过
)

// VATRepo 是增值税政策的持久化访问。
type VATRepo struct{ db *DB }

// getSetting / putSetting 复用工资仓储上的同一套设置读写。
//
// 「设置」在库里就是一张 key-value 表，读写逻辑只该有一份 ——
// 每个新模块各写一遍，迟早有一处忘记 ON CONFLICT 或者忘记判空串。
func (r *VATRepo) getSetting(ctx context.Context, key string, out any) (bool, error) {
	pr := &PayrollRepo{db: r.db}
	return pr.getSetting(ctx, key, out)
}

// VATRates 返回增值税政策仓储。
func (db *DB) VATRates() *VATRepo { return &VATRepo{db: db} }

// Policies 返回当前生效的政策表。
//
// 没存过时写入默认表（2026 年版），这样界面上第一次打开就有东西可看，
// 而不是一个空下拉。默认表全部带政策依据与生效期，
// 用户可按现行规定修改 —— 政策变化时改数据即可，不必改代码。
func (r *VATRepo) Policies(ctx context.Context) ([]vat.RatePolicy, error) {
	list, ok, err := r.storedPolicies(ctx)
	if err != nil {
		return nil, err
	}
	if !ok || len(list) == 0 {
		return r.loadBuiltinPolicies(ctx)
	}
	// ★ 内置表升级：只有「用户没动过」才自动整体换新。
	//
	// 政策是会变的（1% 优惠到期、出口规则调整），内置表跟着版本走；
	// 但用户自己核过、改过的表不能被悄悄覆盖 —— 那是账套里的计税依据。
	if src, err := r.policySource(ctx); err == nil && src == policySourceBuiltin {
		if versionOf(list) != vat.PolicyVersion2026 {
			return r.loadBuiltinPolicies(ctx)
		}
	}
	return list, nil
}

// storedPolicies 读库里存的政策表（不做默认值补种、不做升级判断）。
func (r *VATRepo) storedPolicies(ctx context.Context) ([]vat.RatePolicy, bool, error) {
	var list []vat.RatePolicy
	ok, err := r.getSetting(ctx, settingVATPolicies, &list)
	if err != nil {
		return nil, false, err
	}
	if !ok || len(list) == 0 {
		return nil, false, nil
	}
	return list, true, nil
}

// policySourceUnknown 表示老账套没有来源标记 —— 无法判断用户改没改过。
const policySourceUnknown = "unknown"

func (r *VATRepo) policySource(ctx context.Context) (string, error) {
	var src string
	ok, err := r.getSetting(ctx, settingVATPolicySource, &src)
	if err != nil {
		return policySourceUnknown, err
	}
	if !ok || src == "" {
		// 老账套没有这个标记：**无法判断**用户改没改过。
		//
		// 归到「未标记」是为了不覆盖 —— 宁可让用户手动点一下，
		// 也不能把他核过的计税依据删掉。但界面上要说清是「无法确认」，
		// 不能谎称「你改过」。
		return policySourceUnknown, nil
	}
	return src, nil
}

// loadBuiltinPolicies 写入内置默认表并标记来源。
func (r *VATRepo) loadBuiltinPolicies(ctx context.Context) ([]vat.RatePolicy, error) {
	def := vat.DefaultPolicies2026()
	if err := r.savePolicies(ctx, def, policySourceBuiltin); err != nil {
		return nil, err
	}
	return def, nil
}

// SavePolicies 整体替换政策表（用户编辑或导入），来源标记为自定义。
func (r *VATRepo) SavePolicies(ctx context.Context, policies []vat.RatePolicy) error {
	return r.savePolicies(ctx, policies, policySourceCustom)
}

// ResetPolicies 把政策表恢复成当前版本的内置默认表。
func (r *VATRepo) ResetPolicies(ctx context.Context) error {
	_, err := r.loadBuiltinPolicies(ctx)
	return err
}

func (r *VATRepo) savePolicies(ctx context.Context, policies []vat.RatePolicy,
	source string) error {

	if err := vat.ValidatePolicies(policies); err != nil {
		return err
	}
	return r.db.WithTx(ctx, func(tx *Tx) error {
		pr := &PayrollRepo{db: r.db}
		if err := pr.putSetting(ctx, tx, settingVATPolicies, policies); err != nil {
			return err
		}
		return pr.putSetting(ctx, tx, settingVATPolicySource, source)
	})
}

// PolicyVersion 返回当前政策表的版本号（取第一条的版本）。
func (r *VATRepo) PolicyVersion(ctx context.Context) (string, error) {
	list, err := r.Policies(ctx)
	if err != nil {
		return "", err
	}
	return versionOf(list), nil
}

// PolicyStatus 报告政策表的版本与来源，供界面提示升级。
type PolicyStatus struct {
	// Version 是账套里正在用的版本。
	Version string
	// Origin 是政策表来源：builtin（内置未改）/ custom（用户改过）/
	// unknown（老账套没标记，无法判断）。
	Origin string
	// Builtin 是真表示这份表是内置默认表（用户没改过，可自动升级）。
	Builtin bool
	// UpdateAvailable 为真表示内置表已经更新，而账套还在用旧版。
	UpdateAvailable bool
	// BuiltinVersion 是当前程序内置的版本号。
	BuiltinVersion string
}

// PolicyStatusOf 报告政策表是否需要更新。
//
// ★ 「需人工确认」：用户改过的表不自动覆盖，只提示 ——
// 覆盖用户核过的计税依据，比让他用一个旧版本更糟。
func (r *VATRepo) PolicyStatusOf(ctx context.Context) (PolicyStatus, error) {
	st := PolicyStatus{BuiltinVersion: vat.PolicyVersion2026}
	list, ok, err := r.storedPolicies(ctx)
	if err != nil {
		return st, err
	}
	if !ok {
		st.Version, st.Origin = st.BuiltinVersion, policySourceBuiltin
		st.Builtin, st.UpdateAvailable = true, false
		return st, nil
	}
	src, err := r.policySource(ctx)
	if err != nil {
		return st, err
	}
	st.Version = versionOf(list)
	st.Origin = src
	st.Builtin = src == policySourceBuiltin
	st.UpdateAvailable = st.Version != st.BuiltinVersion
	return st, nil
}

func versionOf(list []vat.RatePolicy) string {
	if len(list) == 0 {
		return ""
	}
	return list[0].Version
}

// StatusHistory 返回纳税人身份变更历史，按生效日升序。
func (r *VATRepo) StatusHistory(ctx context.Context) ([]vat.StatusPeriod, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT status, effective_from, effective_to, note
		  FROM vat_status_history ORDER BY effective_from`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	var out []vat.StatusPeriod
	for rows.Next() {
		var status, from, to, note string
		if err := rows.Scan(&status, &from, &to, &note); err != nil {
			return nil, err
		}
		dFrom, err := calendar.Parse(from)
		if err != nil {
			return nil, fmt.Errorf("sqlite: 身份历史生效日 %q: %w", from, err)
		}
		p := vat.StatusPeriod{
			Status: vat.VATStatus(status), EffectiveFrom: dFrom, Note: note,
		}
		if to != "" {
			dTo, err := calendar.Parse(to)
			if err != nil {
				return nil, fmt.Errorf("sqlite: 身份历史失效日 %q: %w", to, err)
			}
			p.EffectiveTo = dTo
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetVATStatus 变更纳税人身份，并把上一段历史自动闭合。
//
// 身份变更在实务里是会发生的事（小规模自愿登记为一般纳税人），
// 而跨期看账必须按**业务发生日**取当时有效的身份 ——
// 拿今天的身份去算去年的税一定错。所以这里维护的是**历史**，
// 不是覆盖一个字段。
func (r *VATRepo) SetVATStatus(ctx context.Context, status vat.VATStatus,
	from calendar.Date, note string) error {

	if !status.Valid() {
		return fmt.Errorf("%w: 纳税人身份 %q", ErrBadTaxType, status)
	}
	if !from.Valid() {
		return fmt.Errorf("sqlite: 身份生效日无效")
	}

	return r.db.WithTx(ctx, func(tx *Tx) error {
		// 闭合所有「至今有效」且生效日早于新起点的记录
		if _, err := tx.Exec(ctx, `
			UPDATE vat_status_history SET effective_to = ?
			 WHERE effective_to = '' AND effective_from < ?`,
			from.AddDays(-1).String(), from.String()); err != nil {
			return translateErr(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO vat_status_history (status, effective_from, effective_to, note, created_at)
			VALUES (?,?,'',?,?)`,
			string(status), from.String(), note, nowString()); err != nil {
			return translateErr(err)
		}
		// 当前身份字段跟着最新一条走
		_, err := tx.Exec(ctx, `
			UPDATE book SET vat_status = ?, vat_status_effective_from = ?,
			                tax_type = ?, updated_at = ?
			 WHERE id = 1`,
			string(status), from.String(), legacyTaxType(string(status)), nowString())
		return translateErr(err)
	})
}

// SetEnterpriseScale 设置企业规模类型。
//
// 与纳税人身份**分开设置**：它是所得税与统计口径，
// 与增值税怎么算无关。允许传空串（清空 = 未填写）。
func (r *VATRepo) SetEnterpriseScale(ctx context.Context,
	scale vat.EnterpriseScale) error {

	if scale != "" && !scale.Valid() {
		return fmt.Errorf("sqlite: 企业规模类型 %q", scale)
	}
	res, err := r.db.sql.ExecContext(ctx,
		`UPDATE book SET enterprise_scale = ?, updated_at = ? WHERE id = 1`,
		string(scale), nowString())
	if err != nil {
		return translateErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBookNotSetup
	}
	return nil
}
