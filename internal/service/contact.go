package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"miniaccount/internal/domain/audit"
)

// ---------------------------------------------------------------------------
// 往来单位档案（客户 / 供应商 / 股东 / 其他）
// ---------------------------------------------------------------------------
//
// # 为什么需要「管理」而不只是「选择」
//
// 原来只有 `ContactOptions`：一个给凭证录入用的下拉，**只列启用中的**。
// 于是：
//
//   - 新建不了。只能在录发票时顺手带一条出来，客户档案是记账的副产品。
//   - 停用之后再也看不到，也就**改不回来** —— 列表里根本没有它。
//   - 删不掉。录错一个「测试客户」，它会永远留在下拉里。
//
// 辅助核算要成立，四个维度都得有地方**管**：客户、供应商、
// 本公司的部门与员工。部门与员工已经有档案页，往来单位这一半补在这里。

// Contact 是一条往来单位档案（含全部字段，供管理界面用）。
//
// 与 ContactOption 的区别：那个是给录入下拉用的精简版，只有 id/名称/类型；
// 这个是完整档案，包括停用的。两个都要有 —— 用同一个结构会逼着
// 下拉接口也返回税号、账号这些它不需要的东西。
type Contact struct {
	ID            int64  `json:"id"`
	Code          string `json:"code"`
	Kind          string `json:"kind"`
	KindLabel     string `json:"kindLabel"`
	Name          string `json:"name"`
	ShortName     string `json:"shortName"`
	TaxNo         string `json:"taxNo"`
	BankName      string `json:"bankName"`
	BankAccount   string `json:"bankAccount"`
	Address       string `json:"address"`
	Phone         string `json:"phone"`
	Email         string `json:"email"`
	ContactPerson string `json:"contactPerson"`
	Enabled       bool   `json:"enabled"`
	Remark        string `json:"remark"`
}

// ContactKinds 是往来单位可选的类型。
//
// 只列与「外部单位」有关的四类。employee 那一类在 contact 表里也允许
// （历史数据里有），但**新数据一律走 employee 表** —— 两张表都放员工，
// 迟早会出现「同一个人两条档案、余额对不上」。股东也走 contact，
// 因为它同时是对外往来对象。
func ContactKinds() []KindOption {
	return []KindOption{
		{Value: "customer", Label: "客户"},
		{Value: "supplier", Label: "供应商"},
		{Value: "both", Label: "客户与供应商"},
		{Value: "shareholder", Label: "股东"},
		{Value: "other", Label: "其他单位"},
	}
}

// KindOption 是一个「取值 + 中文名」的选项。
type KindOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Contacts 返回往来单位档案。
//
// kind 为空表示全部。**包含已停用的** —— 管理界面要能看到它们，
// 否则停用就变成了「消失」，用户再也没法改回来（这正是原来的问题）。
func (s *Service) Contacts(ctx context.Context, kind string) ([]Contact, error) {
	q := `SELECT id, COALESCE(code,''), kind, name, short_name, tax_no,
	             bank_name, bank_account, address, phone, email,
	             contact_person, is_enabled, remark
	        FROM contact`
	args := []any{}
	if k := strings.TrimSpace(kind); k != "" {
		q += ` WHERE kind = ?`
		args = append(args, k)
	}
	q += ` ORDER BY kind, name`

	rows, err := s.db.SQL().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	out := []Contact{}
	for rows.Next() {
		var c Contact
		var enabled int
		if err := rows.Scan(&c.ID, &c.Code, &c.Kind, &c.Name, &c.ShortName,
			&c.TaxNo, &c.BankName, &c.BankAccount, &c.Address, &c.Phone,
			&c.Email, &c.ContactPerson, &enabled, &c.Remark); err != nil {
			return nil, err
		}
		c.Enabled = enabled != 0
		c.KindLabel = contactKindName(c.Kind)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ContactUsage 报告一个往来单位被哪些地方引用。
//
// 与部门那边同一个道理：删除前必须自己数一遍，数漏了就是一句
// SQLite 的外键报错甩给用户。
//
// ★ 这里**没有**「报销单」这一项，虽然部门那边有。
// `expense_claim` 表只有 dept_id，没有 contact_id —— 往来单位与报销单
// 之间根本没有引用关系。一度照着部门那份抄了一条
// `expense_claim WHERE claimant_employee_id = ?`：它把「报销人的员工 id」
// 当成了「往来单位 id」，于是只要有一个员工号恰好等于某个客户 id，
// 那个客户就被报成「还在被使用：报销单 2 处」，删也删不掉，
// 而用户怎么找都找不到那两张所谓的报销单。
type ContactUsage struct {
	// Entries 是引用了这个往来单位的凭证。
	//
	// ★ 数的是 voucher_entry（凭证分录）而不是 ledger_entry（总账）。
	// 总账只收已记账的凭证，草稿凭证不进去 —— 但草稿凭证一样
	// 把 contact_id 落在 voucher_entry 上，而且 contact 是**真外键**
	// （`voucher_entry.contact_id REFERENCES contact(id)`），
	// 只数总账的话，删除会在 SQL 层被外键挡回来，
	// 用户看到的是一句 SQLite 报错，而不是「这个客户还有 3 张凭证」。
	// 已记账的凭证在两张表里都有，只数一张才不会重复计数。
	Entries int `json:"entries"`
	// Invoices 是发票。
	Invoices int `json:"invoices"`
	// BankFlows 是银行流水上指定的往来单位。
	BankFlows int `json:"bankFlows"`
	// BankRules 是按对方户名自动归集的银行规则。
	BankRules int `json:"bankRules"`
}

// Total 返回引用总数。
func (u ContactUsage) Total() int {
	return u.Entries + u.Invoices + u.BankFlows + u.BankRules
}

// Describe 生成一句人话。
func (u ContactUsage) Describe() string {
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d 处", what, n))
		}
	}
	add(u.Entries, "凭证")
	add(u.Invoices, "发票")
	add(u.BankFlows, "银行流水")
	add(u.BankRules, "银行规则")
	return strings.Join(parts, "、")
}

// ContactUsageOf 统计一个往来单位被引用的次数。
func (s *Service) ContactUsageOf(ctx context.Context, id int64) (ContactUsage, error) {
	var u ContactUsage
	qs := []struct {
		sql string
		out *int
	}{
		{`SELECT COUNT(*) FROM voucher_entry WHERE contact_id = ?`, &u.Entries},
		{`SELECT COUNT(*) FROM invoice WHERE contact_id = ?`, &u.Invoices},
		{`SELECT COUNT(*) FROM bank_flow WHERE contact_id = ?`, &u.BankFlows},
		{`SELECT COUNT(*) FROM bank_rule WHERE contact_id = ?`, &u.BankRules},
	}
	for _, q := range qs {
		if err := s.db.SQL().QueryRowContext(ctx, q.sql, id).Scan(q.out); err != nil {
			return u, translate(err)
		}
	}
	return u, nil
}

// DeleteContact 删除一个往来单位。
//
// 与部门同一条规则：被引用时**拒绝删除**，并说清楚被什么挡住了。
// 已经记进账的凭证不会因为删档案而改变，所以不能连它一起清掉 ——
// 想撤掉一个不再往来的客户，用「停用」。
func (s *Service) DeleteContact(ctx context.Context, id int64) error {
	var name string
	var enabled int
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT name, is_enabled FROM contact WHERE id = ?`, id).Scan(&name, &enabled)
	if err == sql.ErrNoRows {
		return fmt.Errorf("往来单位 id=%d 不存在（可能已经被删掉了）", id)
	}
	if err != nil {
		return translate(err)
	}

	u, err := s.ContactUsageOf(ctx, id)
	if err != nil {
		return err
	}
	if u.Total() > 0 {
		return fmt.Errorf(
			"「%s」还在被使用，不能删除：%s。\n"+
				"已经记进账的凭证不会因为删档案而改变，所以不能连它一起清掉。\n"+
				"如果只是不再往来了、历史还要留，请改用「停用」——"+
				"停用之后新建凭证时选不到它，历史报表照旧。",
			name, u.Describe())
	}

	if _, err := s.db.SQL().ExecContext(ctx,
		`DELETE FROM contact WHERE id = ?`, id); err != nil {
		return translate(err)
	}

	s.recordAudit(ctx, AuditEvent{
		Action:   audit.ActionContactDelete,
		Summary:  fmt.Sprintf("删除往来单位「%s」", name),
		Entity:   "contact",
		EntityID: fmt.Sprintf("%d", id),
		Detail:   map[string]any{"name": name},
	})
	return nil
}

// ContactKindName 把类型取值翻成中文（供界面直接展示）。
func ContactKindName(kind string) string { return contactKindName(kind) }
