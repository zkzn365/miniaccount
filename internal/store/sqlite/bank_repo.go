package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"miniaccount/internal/domain/bank"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/voucher"
)

// BankRepo 是银行流水、导入批次与匹配规则的持久化访问。
type BankRepo struct{ db *DB }

// Bank 返回银行流水仓储。
func (db *DB) Bank() *BankRepo { return &BankRepo{db: db} }

// 银行模块错误。
var (
	ErrFlowExists    = fmt.Errorf("bank: 该流水已存在（重复导入）")
	ErrNoBankAccount = fmt.Errorf("bank: 银行科目不存在")
)

// ---------------------------------------------------------------------------
// 导入
// ---------------------------------------------------------------------------

// ImportInput 是一次导入的输入。
type ImportInput struct {
	// AccountCode 是流水挂靠的银行科目。
	AccountCode string
	FileName    string
	// FileData 是原始文件字节，用于计算文件指纹（识别重复上传同一份对账单）。
	FileData []byte
	// Encoding 是探测到的编码名，仅用于记录。
	Encoding string
	// ImportedBy 是操作人。
	ImportedBy string
	// Rows 是解析出的流水。
	Rows []*bank.Flow
	// RawRows 与 Rows 一一对应的原始行，转成 JSON 存进 raw_json 便于排错。
	RawRows [][]string
}

// ImportResult 是一次导入的结果。
type ImportResult struct {
	ImportID int64
	Total    int
	Inserted int
	// Duplicated 是因去重被跳过的条数。
	Duplicated int
	TotalIn    money.Money
	TotalOut   money.Money
	From, To   calendar.Date
}

// Import 把一批流水写入数据库。
//
// 去重靠 bank_flow 上的 UNIQUE(account_id, dedup_hash)：
// 同一份对账单被重复导入是**最常见的用户误操作**，
// 静默产生重复流水会让账目凭空多出一倍，因此必须在数据库层拦住，
// 而不是靠上层「记得先查一下」。
func (r *BankRepo) Import(ctx context.Context, in ImportInput) (*ImportResult, error) {
	if len(in.Rows) == 0 {
		return nil, fmt.Errorf("bank: 没有可导入的流水")
	}
	accIDs, err := r.db.Accounts().IDsByCode(ctx)
	if err != nil {
		return nil, err
	}
	accID, ok := accIDs[in.AccountCode]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoBankAccount, in.AccountCode)
	}

	res := &ImportResult{Total: len(in.Rows)}
	now := nowString()

	err = r.db.WithTx(ctx, func(tx *Tx) error {
		// 批次
		var fileHash string
		if len(in.FileData) > 0 {
			sum := sha256.Sum256(in.FileData)
			fileHash = hex.EncodeToString(sum[:])
		}
		bres, err := tx.Exec(ctx, `
			INSERT INTO bank_import (account_id, file_name, file_sha256, encoding,
				row_count, imported_by, imported_at)
			VALUES (?,?,?,?,?,?,?)`,
			accID, in.FileName, fileHash, in.Encoding, len(in.Rows), in.ImportedBy, now)
		if err != nil {
			return err
		}
		importID, err := bres.LastInsertId()
		if err != nil {
			return err
		}
		res.ImportID = importID

		for i, f := range in.Rows {
			if err := f.Validate(); err != nil {
				return fmt.Errorf("第 %d 条流水: %w", i+1, err)
			}
			raw := ""
			if i < len(in.RawRows) {
				if b, err := json.Marshal(in.RawRows[i]); err == nil {
					raw = string(b)
				}
			}
			inserted, err := r.insertFlow(ctx, tx, importID, accID, f, raw, now)
			if err != nil {
				return err
			}
			if !inserted {
				res.Duplicated++
				continue
			}
			res.Inserted++
			switch f.Direction {
			case bank.DirIn:
				res.TotalIn = res.TotalIn.Add(f.Amount)
			case bank.DirOut:
				res.TotalOut = res.TotalOut.Add(f.Amount)
			}
			if res.From.IsZero() || f.TxnDate.Before(res.From) {
				res.From = f.TxnDate
			}
			if res.To.IsZero() || f.TxnDate.After(res.To) {
				res.To = f.TxnDate
			}
		}

		// 回填批次汇总
		if _, err := tx.Exec(ctx, `
			UPDATE bank_import SET total_in = ?, total_out = ?, period_from = ?, period_to = ?
			 WHERE id = ?`,
			int64(res.TotalIn), int64(res.TotalOut),
			res.From.String(), res.To.String(), importID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// insertFlow 插入一条流水；命中唯一约束时返回 false 表示重复。
func (r *BankRepo) insertFlow(ctx context.Context, tx *Tx, importID, accountID int64,
	f *bank.Flow, raw, now string) (bool, error) {

	// 先查一次，避免依赖错误信息判断重复（错误信息因驱动而异）
	var exists int
	err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM bank_flow WHERE account_id = ? AND dedup_hash = ?`,
		accountID, f.DedupKey()).Scan(&exists)
	if err != nil {
		return false, translateErr(err)
	}
	if exists > 0 {
		return false, nil
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO bank_flow (import_id, account_id, txn_date, direction, amount, balance,
			counterparty_name, counterparty_account, summary, serial_no, dedup_hash,
			status, match_layer, confidence, memo, raw_json, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		importID, accountID, f.TxnDate.String(), string(f.Direction),
		int64(f.Amount), int64(f.Balance),
		f.CounterpartyName, f.CounterpartyAccount, f.Summary, f.SerialNo,
		f.DedupKey(), string(f.Status), string(f.MatchLayer), f.Confidence,
		f.Memo, raw, now, now)
	if err != nil {
		return false, err
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// 查询
// ---------------------------------------------------------------------------

const flowColumns = `f.id, f.import_id, a.code, f.account_id, f.txn_date, f.direction,
	f.amount, COALESCE(f.balance, 0), f.counterparty_name, f.counterparty_account,
	f.summary, f.serial_no, f.status, f.match_layer, f.confidence,
	COALESCE(ca.code, ''), f.contact_id, f.employee_id, f.dept_id, f.project_id,
	f.memo, f.voucher_id, f.rule_id`

// ListFlows 按状态与日期范围查询流水。
//
// status 为空表示不限；limit 为 0 表示不限。
func (r *BankRepo) ListFlows(ctx context.Context, status bank.Status,
	from, to calendar.Date, limit int) ([]*bank.Flow, error) {

	q := `SELECT ` + flowColumns + `
	        FROM bank_flow f
	        JOIN account a ON a.id = f.account_id
	        LEFT JOIN account ca ON ca.id = f.counter_account_id
	       WHERE 1 = 1`
	var args []any
	if status != "" {
		q += ` AND f.status = ?`
		args = append(args, string(status))
	}
	if !from.IsZero() {
		q += ` AND f.txn_date >= ?`
		args = append(args, from.String())
	}
	if !to.IsZero() {
		q += ` AND f.txn_date <= ?`
		args = append(args, to.String())
	}
	q += ` ORDER BY f.txn_date, f.id`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}

	rows, err := r.db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	return scanFlows(rows)
}

// GetFlow 按 id 取一条流水。
func (r *BankRepo) GetFlow(ctx context.Context, id int64) (*bank.Flow, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+flowColumns+`
		FROM bank_flow f
		JOIN account a ON a.id = f.account_id
		LEFT JOIN account ca ON ca.id = f.counter_account_id
		WHERE f.id = ?`, id)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	list, err := scanFlows(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%w: 流水 id=%d", ErrNotFound, id)
	}
	return list[0], nil
}

// FlowsByVoucher 返回某张凭证对应的流水（凭证 → 流水的反向查询）。
func (r *BankRepo) FlowsByVoucher(ctx context.Context, voucherID int64) ([]*bank.Flow, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+flowColumns+`
		FROM bank_flow f
		JOIN account a ON a.id = f.account_id
		LEFT JOIN account ca ON ca.id = f.counter_account_id
		WHERE f.voucher_id = ?`, voucherID)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	return scanFlows(rows)
}

func scanFlows(rows *sql.Rows) ([]*bank.Flow, error) {
	var out []*bank.Flow
	for rows.Next() {
		f := &bank.Flow{}
		var (
			dateStr, dir, status, layer         string
			amount, balance                     int64
			contactID, empID, deptID, projectID sql.NullInt64
			voucherID, ruleID                   sql.NullInt64
		)
		if err := rows.Scan(&f.ID, &f.ImportID, &f.AccountCode, &f.AccountID,
			&dateStr, &dir, &amount, &balance,
			&f.CounterpartyName, &f.CounterpartyAccount, &f.Summary, &f.SerialNo,
			&status, &layer, &f.Confidence,
			&f.CounterAccount, &contactID, &empID, &deptID, &projectID, &f.Memo,
			&voucherID, &ruleID); err != nil {
			return nil, err
		}
		d, err := calendar.Parse(dateStr)
		if err != nil {
			return nil, fmt.Errorf("sqlite: 流水 %d 日期非法 %q", f.ID, dateStr)
		}
		f.TxnDate = d
		f.Direction = bank.Direction(dir)
		f.Status = bank.Status(status)
		f.MatchLayer = bank.Layer(layer)
		f.Amount = money.Money(amount)
		f.Balance = money.Money(balance)
		f.ContactID = toNullInt64(contactID)
		f.EmployeeID = toNullInt64(empID)
		f.DeptID = toNullInt64(deptID)
		f.ProjectID = toNullInt64(projectID)
		if voucherID.Valid {
			v := voucherID.Int64
			f.VoucherID = &v
		}
		if ruleID.Valid {
			v := ruleID.Int64
			f.RuleID = &v
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FlowStats 是流水状态统计，用于界面上显示「还有多少笔没处理」。
type FlowStats struct {
	Imported int
	Matched  int
	Posted   int
	Ignored  int
}

// Stats 返回各状态的流水数量。
func (r *BankRepo) Stats(ctx context.Context) (*FlowStats, error) {
	rows, err := r.db.sql.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM bank_flow GROUP BY status`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	s := &FlowStats{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		switch bank.Status(st) {
		case bank.StatusImported:
			s.Imported = n
		case bank.StatusMatched:
			s.Matched = n
		case bank.StatusPosted:
			s.Posted = n
		case bank.StatusIgnored:
			s.Ignored = n
		}
	}
	return s, rows.Err()
}

// ---------------------------------------------------------------------------
// 规则
// ---------------------------------------------------------------------------

const ruleColumns = `r.id, r.name, r.priority, r.enabled, COALESCE(a.code, ''), r.direction,
	r.match_field, r.pattern, r.amount_min, r.amount_max,
	COALESCE(ca.code, ''), r.contact_id, r.employee_id, r.dept_id, r.project_id,
	r.memo_template, r.hit_count`

// ListRules 返回全部规则，按优先级升序。
func (r *BankRepo) ListRules(ctx context.Context) ([]*bank.Rule, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+ruleColumns+`
		FROM bank_rule r
		LEFT JOIN account a  ON a.id = r.account_id
		LEFT JOIN account ca ON ca.id = r.counter_account_id
		ORDER BY r.priority, r.id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	return scanRules(rows)
}

func scanRules(rows *sql.Rows) ([]*bank.Rule, error) {
	var out []*bank.Rule
	for rows.Next() {
		r := &bank.Rule{}
		var (
			enabled                                  int
			minA, maxA                               sql.NullInt64
			contactID, employeeID, deptID, projectID sql.NullInt64
			dir, field                               string
		)
		if err := rows.Scan(&r.ID, &r.Name, &r.Priority, &enabled, &r.AccountCode,
			&dir, &field, &r.Pattern, &minA, &maxA,
			&r.CounterAccountCode, &contactID, &employeeID, &deptID, &projectID,
			&r.MemoTemplate, &r.HitCount); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		r.Direction = bank.Direction(dir)
		r.MatchField = bank.MatchField(field)
		if minA.Valid {
			v := money.Money(minA.Int64)
			r.AmountMin = &v
		}
		if maxA.Valid {
			v := money.Money(maxA.Int64)
			r.AmountMax = &v
		}
		r.ContactID = toNullInt64(contactID)
		r.EmployeeID = toNullInt64(employeeID)
		r.DeptID = toNullInt64(deptID)
		r.ProjectID = toNullInt64(projectID)
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertRule 新增或更新一条规则。
func (r *BankRepo) UpsertRule(ctx context.Context, rule *bank.Rule) (int64, error) {
	if !rule.MatchField.Valid() {
		return 0, fmt.Errorf("bank: 匹配字段非法: %q", rule.MatchField)
	}
	if rule.MatchField == "" {
		rule.MatchField = bank.MatchCounterparty
	}

	var id int64
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		accIDs, err := r.db.Accounts().IDsByCodeTx(ctx, tx)
		if err != nil {
			return err
		}
		accID := int64(0)
		if rule.AccountCode != "" {
			v, ok := accIDs[rule.AccountCode]
			if !ok {
				return fmt.Errorf("%w: 银行科目 %s", ErrNoBankAccount, rule.AccountCode)
			}
			accID = v
		}
		counterID, ok := accIDs[rule.CounterAccountCode]
		if !ok {
			return fmt.Errorf("%w: 对方科目 %s", ErrNotFound, rule.CounterAccountCode)
		}

		now := nowString()
		if rule.ID == 0 {
			res, err := tx.Exec(ctx, `
				INSERT INTO bank_rule (name, priority, enabled, account_id, direction,
					match_field, pattern, amount_min, amount_max, counter_account_id,
					contact_id, employee_id, dept_id, project_id,
					memo_template, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				rule.Name, rule.Priority, boolInt(rule.Enabled),
				nullZero(accID), string(rule.Direction), string(rule.MatchField),
				rule.Pattern, nullMoney(rule.AmountMin), nullMoney(rule.AmountMax),
				counterID, nullInt64(rule.ContactID),
				nullInt64(rule.EmployeeID), nullInt64(rule.DeptID), nullInt64(rule.ProjectID),
				rule.MemoTemplate, now, now)
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		}

		_, err = tx.Exec(ctx, `
			UPDATE bank_rule SET name = ?, priority = ?, enabled = ?, account_id = ?,
				direction = ?, match_field = ?, pattern = ?, amount_min = ?, amount_max = ?,
				counter_account_id = ?, contact_id = ?, employee_id = ?, dept_id = ?,
				project_id = ?, memo_template = ?, updated_at = ?
			 WHERE id = ?`,
			rule.Name, rule.Priority, boolInt(rule.Enabled),
			nullZero(accID), string(rule.Direction), string(rule.MatchField),
			rule.Pattern, nullMoney(rule.AmountMin), nullMoney(rule.AmountMax),
			counterID, nullInt64(rule.ContactID),
			nullInt64(rule.EmployeeID), nullInt64(rule.DeptID), nullInt64(rule.ProjectID),
			rule.MemoTemplate, now, rule.ID)
		id = rule.ID
		return err
	})
	return id, err
}

// DeleteRule 删除一条规则。
func (r *BankRepo) DeleteRule(ctx context.Context, id int64) error {
	_, err := r.db.sql.ExecContext(ctx, `DELETE FROM bank_rule WHERE id = ?`, id)
	return translateErr(err)
}

// BumpRuleHits 累加规则的命中次数，用于界面上显示「这条规则用过多少次」。
func (r *BankRepo) BumpRuleHits(ctx context.Context, tx *Tx, ruleID int64) error {
	_, err := tx.Exec(ctx,
		`UPDATE bank_rule SET hit_count = hit_count + 1 WHERE id = ?`, ruleID)
	return err
}

// ---------------------------------------------------------------------------
// 匹配
// ---------------------------------------------------------------------------

// BuildMatcher 装配三层匹配器。
//
// 第 1 层用规则表；第 2 层从已过账的银行凭证里抽取历史分录。
// 第 3 层（AI）留空，由上层按需注入。
func (r *BankRepo) BuildMatcher(ctx context.Context) (*bank.Matcher, error) {
	rules, err := r.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	history, err := r.historyEntries(ctx)
	if err != nil {
		return nil, err
	}
	return &bank.Matcher{
		Rules:   bank.NewRuleEngine(rules),
		History: bank.NewHistoryMatcher(history),
	}, nil
}

// bankRoots 是预置科目表里「货币资金」类的一级科目。
// 历史相似度的范围限定在这些科目上：只有涉及银行/现金的凭证
// 才可能对一笔银行流水的记法有参考价值。
var bankRoots = []string{"1001", "1002", "1012"}

// historyEntries 从**全部已过账凭证**里抽取「银行分录 + 对方分录」的历史对。
//
// 刻意不只读 bank_flow：用户可能手工录过凭证、也可能是几个月前导入的流水。
// 「上次这笔钱是怎么记的」应当看整个账，而不是只看银行导入模块自己产生的东西。
//
// 提取逻辑：对每张含银行分录的凭证，取银行那一侧与其余分录配对，
// 每一对就是一条候选历史。方向由银行分录在借还是贷决定。
func (r *BankRepo) historyEntries(ctx context.Context) ([]bank.HistoryEntry, error) {
	const q = `
		WITH bank_vouchers AS (
			SELECT DISTINCT le.voucher_id
			  FROM ledger_entry le
			  JOIN account a ON a.id = le.account_id
			 WHERE substr(a.code, 1, 4) IN ('1001','1002','1012')
		)
		SELECT v.no, v.id, a.code, le.debit, le.credit, le.summary,
		       le.contact_id, le.employee_id, le.dept_id, le.project_id,
		       -- 对手方名称的取值优先级：
		       --   1) 分录上的往来单位（手工凭证通常靠它）
		       --   2) 该凭证对应的银行流水的对方户名（导入凭证靠它，最准）
		       --   3) 退回摘要（至少还有关键词可打分）
		       COALESCE(
		         NULLIF(c.name, ''),
		         (SELECT bf.counterparty_name FROM bank_flow bf
		           WHERE bf.voucher_id = v.id AND bf.counterparty_name <> '' LIMIT 1),
		         ''
		       ),
		       le.line_no
		  FROM ledger_entry le
		  JOIN bank_vouchers bv ON bv.voucher_id = le.voucher_id
		  JOIN voucher v       ON v.id = le.voucher_id
		  JOIN account a       ON a.id = le.account_id
		  LEFT JOIN contact c  ON c.id = le.contact_id
		 WHERE v.status = 'posted'
		 ORDER BY v.id, le.line_no`

	rows, err := r.db.sql.QueryContext(ctx, q)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	type row struct {
		voucherNo   string
		voucherID   int64
		accountCode string
		debit       money.Money
		credit      money.Money
		summary     string
		contactID   *int64
		contactName string
		lineNo      int
		// 四维辅助核算原样带出来：历史匹配的价值就在于
		// 「上次怎么记的，这次照着记」，包括辅助核算怎么填的。
		employeeID, deptID, projectID *int64
	}
	var all []row
	for rows.Next() {
		var (
			rr                                       row
			d, c                                     int64
			contactID, employeeID, deptID, projectID sql.NullInt64
		)
		if err := rows.Scan(&rr.voucherNo, &rr.voucherID, &rr.accountCode,
			&d, &c, &rr.summary, &contactID, &employeeID, &deptID, &projectID,
			&rr.contactName, &rr.lineNo); err != nil {
			return nil, err
		}
		rr.debit, rr.credit = money.Money(d), money.Money(c)
		rr.contactID = toNullInt64(contactID)
		rr.employeeID = toNullInt64(employeeID)
		rr.deptID = toNullInt64(deptID)
		rr.projectID = toNullInt64(projectID)
		all = append(all, rr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	isBank := func(code string) bool {
		if len(code) < 4 {
			return false
		}
		root := code[:4]
		for _, b := range bankRoots {
			if root == b {
				return true
			}
		}
		return false
	}

	// 按凭证分组，为每张凭证生成「银行分录 × 对方分录」的配对
	var out []bank.HistoryEntry
	for i := 0; i < len(all); {
		j := i
		for j < len(all) && all[j].voucherID == all[i].voucherID {
			j++
		}
		group := all[i:j]
		i = j

		var banks, counters []row
		for _, rr := range group {
			if isBank(rr.accountCode) {
				banks = append(banks, rr)
			} else {
				counters = append(counters, rr)
			}
		}
		if len(banks) == 0 || len(counters) == 0 {
			continue
		}

		for _, b := range banks {
			var dir bank.Direction
			var amount money.Money
			switch {
			case b.debit.IsPositive():
				dir, amount = bank.DirIn, b.debit
			case b.credit.IsPositive():
				dir, amount = bank.DirOut, b.credit
			default:
				continue
			}
			for _, c := range counters {
				name := c.contactName
				if name == "" {
					// 没有往来单位时退回摘要 —— 银行户名无法反查，
					// 但至少摘要里的关键词仍可用于相似度打分
					name = c.summary
				}
				out = append(out, bank.HistoryEntry{
					VoucherNo:          b.voucherNo,
					VoucherID:          b.voucherID,
					CounterpartyName:   name,
					Summary:            c.summary,
					Direction:          dir,
					Amount:             amount,
					CounterAccountCode: c.accountCode,
					CounterAccountID:   0,
					ContactID:          c.contactID,
					EmployeeID:         c.employeeID,
					DeptID:             c.deptID,
					ProjectID:          c.projectID,
					Memo:               c.summary,
				})
			}
		}
	}
	return out, nil
}

// MatchResult 是一次批量匹配的结果。
type MatchResult struct {
	Total   int
	Matched int
	// ByLayer 统计各层的命中数，用于了解「规则/历史/AI 各覆盖多少」。
	ByLayer map[bank.Layer]int
	// Unmatched 是没能匹配上的流水 id。
	Unmatched []int64
}

// MatchAll 对指定状态的流水批量匹配并写回提议。
//
// 匹配结果只是**提议**：写回 counter_account_id / contact_id / memo 并置为
// matched，但不生成凭证。生成凭证需要人工确认后显式调用 PostFlows。
func (r *BankRepo) MatchAll(ctx context.Context, status bank.Status) (*MatchResult, error) {
	matcher, err := r.BuildMatcher(ctx)
	if err != nil {
		return nil, err
	}
	flows, err := r.ListFlows(ctx, status, calendar.Date{}, calendar.Date{}, 0)
	if err != nil {
		return nil, err
	}

	res := &MatchResult{Total: len(flows), ByLayer: map[bank.Layer]int{}}
	accIDs, err := r.db.Accounts().IDsByCode(ctx)
	if err != nil {
		return nil, err
	}

	err = r.db.WithTx(ctx, func(tx *Tx) error {
		for _, f := range flows {
			s := matcher.Match(f)
			if s == nil {
				res.Unmatched = append(res.Unmatched, f.ID)
				continue
			}
			counterID, ok := accIDs[s.CounterAccountCode]
			if !ok {
				// 规则指向了不存在的科目 —— 跳过而不是写坏数据
				res.Unmatched = append(res.Unmatched, f.ID)
				continue
			}
			if _, err := tx.Exec(ctx, `
				UPDATE bank_flow SET counter_account_id = ?, contact_id = ?,
				employee_id = ?, dept_id = ?, project_id = ?, memo = ?,
					status = ?, match_layer = ?, confidence = ?, rule_id = ?, updated_at = ?
				 WHERE id = ?`,
				counterID, nullInt64(s.ContactID),
				nullInt64(s.EmployeeID), nullInt64(s.DeptID), nullInt64(s.ProjectID),
				s.Memo,
				string(bank.StatusMatched), string(s.Layer), s.Confidence,
				nullInt64(s.RuleID), nowString(), f.ID); err != nil {
				return err
			}
			if s.RuleID != nil {
				if err := r.BumpRuleHits(ctx, tx, *s.RuleID); err != nil {
					return err
				}
			}
			res.Matched++
			res.ByLayer[s.Layer]++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Suggestion 是人工指定（或程序设定）的一条流水记账方案。
type Suggestion struct {
	CounterAccount string
	ContactID      *int64
	EmployeeID     *int64
	DeptID         *int64
	ProjectID      *int64
	Memo           string
}

// SetSuggestion 人工指定一条流水的记账方案。
//
// 覆盖四个辅助核算维度：对方科目可能要求股东、部门、员工或项目，
// 只给往来单位会让「管理费用—办公费」这类科目无法过账。
func (r *BankRepo) SetSuggestion(ctx context.Context, flowID int64,
	sg Suggestion) error {

	return r.db.WithTx(ctx, func(tx *Tx) error {
		accIDs, err := r.db.Accounts().IDsByCodeTx(ctx, tx)
		if err != nil {
			return err
		}
		id, ok := accIDs[sg.CounterAccount]
		if !ok {
			return fmt.Errorf("%w: 对方科目 %s", ErrNotFound, sg.CounterAccount)
		}
		res, err := tx.Exec(ctx, `
			UPDATE bank_flow SET counter_account_id = ?, contact_id = ?,
				employee_id = ?, dept_id = ?, project_id = ?, memo = ?,
				status = ?, match_layer = ?, confidence = 1, updated_at = ?
			 WHERE id = ? AND status <> 'posted'`,
			id, nullInt64(sg.ContactID),
			nullInt64(sg.EmployeeID), nullInt64(sg.DeptID), nullInt64(sg.ProjectID),
			sg.Memo,
			string(bank.StatusMatched), string(bank.LayerManual), nowString(), flowID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: 流水 id=%d（可能已生成凭证）", ErrNotFound, flowID)
		}
		return nil
	})
}

// IgnoreFlow 把一条流水标记为不记账。
func (r *BankRepo) IgnoreFlow(ctx context.Context, flowID int64, reason string) error {
	res, err := r.db.sql.ExecContext(ctx, `
		UPDATE bank_flow SET status = 'ignored', ignored_reason = ?, updated_at = ?
		 WHERE id = ? AND status <> 'posted'`, reason, nowString(), flowID)
	if err != nil {
		return translateErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: 流水 id=%d", ErrNotFound, flowID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 批量生成凭证
// ---------------------------------------------------------------------------

// PostFlowResult 是批量过账的结果。
type PostFlowResult struct {
	Created int
	Skipped int
	// Vouchers 是生成的凭证 id 与凭证号。
	Vouchers []PostedVoucher
	// Failures 是失败原因（流水 id → 原因）。
	Failures map[int64]string
}

// PostedVoucher 描述一张刚生成的凭证。
type PostedVoucher struct {
	FlowID    int64
	VoucherID int64
	No        string
	Amount    money.Money
}

// PostFlows 把已匹配的流水批量生成凭证并过账。
//
// 每条流水生成一张凭证（一借一贷）。刻意不合并成一张多借多贷的凭证：
// 银行流水的每一笔都是独立的业务事实，合并后会丢失逐笔可追溯性，
// 而「这笔钱对应哪张凭证」正是对账时最常问的问题。
//
// 全部在一个事务内完成：中途失败不会留下「一半流水已记账、
// 一半还是待确认」的中间状态。
func (r *BankRepo) PostFlows(ctx context.Context, flowIDs []int64,
	postingBy string, at time.Time) (*PostFlowResult, error) {

	if len(flowIDs) == 0 {
		return &PostFlowResult{Failures: map[int64]string{}}, nil
	}
	if postingBy == "" {
		return nil, fmt.Errorf("bank: 缺少记账人")
	}

	res := &PostFlowResult{Failures: map[int64]string{}}

	err := r.db.WithTx(ctx, func(tx *Tx) error {
		accIDs, err := r.db.Accounts().IDsByCodeTx(ctx, tx)
		if err != nil {
			return err
		}
		// 科目树用于校验（对方科目必须是可记账的明细科目）
		tree, err := r.db.Accounts().TreeFrom(ctx, tx)
		if err != nil {
			return err
		}
		// kinds 由 PostInTx 内部的校验使用；这里显式取一次保证
		// 「往来单位是否存在」在生成凭证之前就被发现（给出更早、更准确的报错）
		if _, err := r.db.Contacts().KindsFrom(ctx, tx); err != nil {
			return err
		}
		cal, err := r.db.Vouchers().LoadPeriodsTx(ctx, tx)
		if err != nil {
			return err
		}

		for _, fid := range flowIDs {
			f, err := r.getFlowTx(ctx, tx, fid)
			if err != nil {
				res.Failures[fid] = err.Error()
				continue
			}
			if f.VoucherID != nil {
				res.Skipped++
				res.Failures[fid] = "已生成凭证，跳过"
				continue
			}
			if f.Status != bank.StatusMatched {
				res.Skipped++
				res.Failures[fid] = fmt.Sprintf("状态为「%s」，需先匹配", f.Status.Label())
				continue
			}

			// 校验期间与科目
			if _, err := cal.CheckPostable(f.TxnDate); err != nil {
				res.Failures[fid] = err.Error()
				continue
			}
			if err := tree.CheckPostable(f.AccountCode); err != nil {
				res.Failures[fid] = "银行科目: " + err.Error()
				continue
			}
			if err := tree.CheckPostable(f.CounterAccount); err != nil {
				res.Failures[fid] = "对方科目: " + err.Error()
				continue
			}

			entries, err := f.BuildEntries()
			if err != nil {
				res.Failures[fid] = err.Error()
				continue
			}

			// 生成凭证
			vc, err := r.postBankVoucher(ctx, tx, f, entries, accIDs, postingBy, at)
			if err != nil {
				res.Failures[fid] = err.Error()
				continue
			}

			// 回写双向关联
			if _, err := tx.Exec(ctx, `
				UPDATE bank_flow SET voucher_id = ?, status = 'posted', updated_at = ?
				 WHERE id = ?`, vc.VoucherID, nowString(), fid); err != nil {
				return err
			}
			res.Created++
			res.Vouchers = append(res.Vouchers, *vc)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (r *BankRepo) getFlowTx(ctx context.Context, tx *Tx, id int64) (*bank.Flow, error) {
	rows, err := tx.Query(ctx, `SELECT `+flowColumns+`
		FROM bank_flow f
		JOIN account a ON a.id = f.account_id
		LEFT JOIN account ca ON ca.id = f.counter_account_id
		WHERE f.id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list, err := scanFlows(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%w: 流水 id=%d", ErrNotFound, id)
	}
	return list[0], nil
}

// postBankVoucher 在给定事务内，把一条流水转换成凭证并过账。
//
// 复用 VoucherRepo.PostInTx，因此银行凭证与手工凭证走的是**同一条**过账路径：
// 同样分配凭证字号、同样走 7 条不变式校验、同样写总账。
// 刻意不为银行流水另写一套过账逻辑 —— 两套逻辑迟早会分叉，
// 而分叉的那一天，账就会对不上。
func (r *BankRepo) postBankVoucher(ctx context.Context, tx *Tx, f *bank.Flow,
	entries []bank.Entry, accIDs map[string]int64, postingBy string,
	at time.Time) (*PostedVoucher, error) {

	// 凭证日期用流水日期：银行流水是既成事实，记账日期应与实际资金
	// 变动日期一致，而不是「录入当天」。
	vc, err := voucher.New(voucher.WordJi, f.TxnDate, postingBy)
	if err != nil {
		return nil, err
	}
	vc.Source = voucher.SourceBank
	flowID := f.ID
	vc.SourceID = &flowID
	vc.Remark = fmt.Sprintf("银行流水导入：%s %s", f.Direction.Label(), f.CounterpartyName)

	for _, e := range entries {
		if err := vc.AddEntry(ledger.Entry{
			AccountCode: e.AccountCode,
			Summary:     e.Summary,
			Debit:       e.Debit,
			Credit:      e.Credit,
			Aux: ledger.Aux{
				ContactID:  e.ContactID,
				EmployeeID: e.EmployeeID,
				DeptID:     e.DeptID,
				ProjectID:  e.ProjectID,
			},
		}); err != nil {
			return nil, err
		}
	}

	res, err := r.db.Vouchers().PostInTx(ctx, tx, PostInput{
		Voucher: vc, Accounts: accIDs, PostingBy: postingBy, At: at,
	})
	if err != nil {
		return nil, err
	}
	return &PostedVoucher{
		FlowID: f.ID, VoucherID: res.VoucherID, No: res.No, Amount: f.Amount,
	}, nil
}

func nullZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullMoney(m *money.Money) any {
	if m == nil {
		return nil
	}
	return int64(*m)
}

var _ = strings.TrimSpace
