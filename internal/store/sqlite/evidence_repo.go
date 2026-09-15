package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"miniaccount/internal/domain/evidence"
	"miniaccount/internal/domain/period"
)

// EvidenceRepo 是审计证据链的持久化访问。
type EvidenceRepo struct{ db *DB }

// Evidence 返回证据链仓储。
func (db *DB) Evidence() *EvidenceRepo { return &EvidenceRepo{db: db} }

// evidenceColumns 是统一的列清单，读写两头照着它来。
const evidenceColumns = `id, owner_type, owner_id, ref_kind, ref_id, ref_label,
	sha256, file_name, file_size, note, linked_by, created_at`

// ownerPeriod 算出结论所属的期间。
//
// 调整类结论用调整自己的行 id 反查（它的期间在 audit_adjustment 里）；
// 重要性水平与底稿结论是「一期一条」，id 就是期间号（202503）。
func ownerPeriodTx(ctx context.Context, q querier, ownerType string,
	ownerID int64) (period.Key, error) {

	if ownerType == string(evidence.OwnerAdjustment) {
		var y, m int
		err := q.QueryRowContext(ctx,
			`SELECT year, month FROM audit_adjustment WHERE id = ?`, ownerID).Scan(&y, &m)
		if err == sql.ErrNoRows {
			return period.Key{}, fmt.Errorf("%w: 审计调整 id=%d", ErrNotFound, ownerID)
		}
		if err != nil {
			return period.Key{}, translateErr(err)
		}
		return period.NewKey(y, m), nil
	}
	// 202503 → 2025-03
	y, m := int(ownerID/100), int(ownerID%100)
	k := period.NewKey(y, m)
	if !k.Valid() {
		return k, fmt.Errorf("evidence: 结论 id %d 里读不出会计期间", ownerID)
	}
	return k, nil
}

// Add 登记一条证据。
//
// 同一份资料挂到同一个结论下只算一次：重复挂不会让结论更可信，
// 只会让「有几份证据」这个数字虚高。这里靠唯一索引挡住，
// 返回 ErrDuplicate 让服务层决定是报错还是当成幂等。
func (r *EvidenceRepo) Add(ctx context.Context, l evidence.Link) (int64, error) {
	if err := l.Validate(); err != nil {
		return 0, err
	}
	var id int64
	var k period.Key
	err := r.db.WithTx(ctx, func(tx *Tx) error {
		// ★ 归属期间与存在性也在这个事务里查（原来是事务外先查，见 S5）
		key, err := ownerPeriodTx(ctx, tx.q, string(l.OwnerType), l.OwnerID)
		if err != nil {
			return err
		}
		k = key
		// ★ 判重放在**同一个事务里查**，而不是靠 INSERT ... ON CONFLICT。
		//
		// 两个原因：
		//  1. 描述类（合同/外部资料）的判重键是 ref_label，与单据类不同，
		//     没法用一条 ON CONFLICT 表达（0015 把它们拆成了两个部分唯一索引）；
		//  2. 原来的写法把「查重」交给数据库，插入前的存在性检查却在事务外
		//     （TOCTOU）：并发删除调整时可能留下指向不存在结论的证据行。
		//     现在查与写在同一个事务里，两个问题一起解决。
		var exists int
		if descriptiveRef(l.RefKind) {
			err := tx.QueryRow(ctx, `
				SELECT COUNT(*) FROM audit_evidence
				 WHERE owner_type = ? AND owner_id = ? AND ref_kind = ? AND ref_label = ?`,
				string(l.OwnerType), l.OwnerID, string(l.RefKind), l.RefLabel).Scan(&exists)
			if err != nil {
				return translateErr(err)
			}
		} else {
			err := tx.QueryRow(ctx, `
				SELECT COUNT(*) FROM audit_evidence
				 WHERE owner_type = ? AND owner_id = ? AND ref_kind = ?
				   AND ref_id = ? AND sha256 = ?`,
				string(l.OwnerType), l.OwnerID, string(l.RefKind),
				l.RefID, l.SHA256).Scan(&exists)
			if err != nil {
				return translateErr(err)
			}
		}
		if exists > 0 {
			return evidence.ErrDuplicate
		}
		res, err := tx.Exec(ctx, `
			INSERT INTO audit_evidence
				(year, month, owner_type, owner_id, ref_kind, ref_id, ref_label,
				 sha256, file_name, file_size, note, linked_by, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			k.Year, k.Month, string(l.OwnerType), l.OwnerID, string(l.RefKind),
			l.RefID, l.RefLabel, l.SHA256, l.FileName, l.FileSize, l.Note,
			l.LinkedBy, l.LinkedAt)
		if err != nil {
			return translateErr(err)
		}
		id, err = res.LastInsertId()
		return translateErr(err)
	})
	return id, err
}

// descriptiveRef 报告这种证据是不是「描述类」（只有文字，没有单据 id 与文件）。
func descriptiveRef(k evidence.RefKind) bool {
	return k == evidence.RefContract || k == evidence.RefExternal
}

// Links 返回某个结论下的全部证据。
func (r *EvidenceRepo) Links(ctx context.Context, ownerType string,
	ownerID int64) ([]evidence.Link, error) {

	return r.query(ctx, `WHERE owner_type = ? AND owner_id = ? ORDER BY id`,
		ownerType, ownerID)
}

// PeriodLinks 返回某期的全部证据。
func (r *EvidenceRepo) PeriodLinks(ctx context.Context, k period.Key) ([]evidence.Link, error) {
	return r.query(ctx, `WHERE year = ? AND month = ? ORDER BY owner_type, owner_id, id`,
		k.Year, k.Month)
}

// Link 按 id 读一条。
func (r *EvidenceRepo) Link(ctx context.Context, id int64) (*evidence.Link, error) {
	list, err := r.query(ctx, `WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%w: 证据 id=%d", ErrNotFound, id)
	}
	return &list[0], nil
}

// Delete 删掉一条证据关联。
func (r *EvidenceRepo) Delete(ctx context.Context, id int64) error {
	return r.db.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.Exec(ctx, `DELETE FROM audit_evidence WHERE id = ?`, id)
		if err != nil {
			return translateErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: 证据 id=%d", ErrNotFound, id)
		}
		return nil
	})
}

// query 按条件取证据行。
func (r *EvidenceRepo) query(ctx context.Context, where string, args ...any) ([]evidence.Link, error) {
	rows, err := r.db.sql.QueryContext(ctx,
		`SELECT `+evidenceColumns+` FROM audit_evidence `+where, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	out := []evidence.Link{}
	for rows.Next() {
		var (
			l         evidence.Link
			ownerType string
			refKind   string
			sha       sql.NullString
			fileName  sql.NullString
			fileSize  sql.NullInt64
			refLabel  sql.NullString
			note      sql.NullString
			linkedBy  sql.NullString
			linkedAt  sql.NullString
			refID     sql.NullInt64
		)
		if err := rows.Scan(&l.ID, &ownerType, &l.OwnerID, &refKind, &refID,
			&refLabel, &sha, &fileName, &fileSize, &note, &linkedBy, &linkedAt); err != nil {
			return nil, translateErr(err)
		}
		l.OwnerType = evidence.OwnerType(ownerType)
		l.RefKind = evidence.RefKind(refKind)
		l.RefID = refID.Int64
		l.RefLabel = refLabel.String
		l.SHA256 = sha.String
		l.FileName = fileName.String
		l.FileSize = fileSize.Int64
		l.Note = note.String
		l.LinkedBy = linkedBy.String
		l.LinkedAt = linkedAt.String
		out = append(out, l)
	}
	return out, translateErr(rows.Err())
}

// ExistingRefs 找出这些单据 id 里**还在**的那些。
//
// ★ 存在性必须现查，不能存成标记：凭证被红冲、发票被删之后，
// 底稿上那条「依据：凭证 #7」就悬空了 —— 而底稿自己不知道。
// 用户看到的必须是「这份资料已经找不到了」，而不是一份看起来很正常的依据。
func (r *EvidenceRepo) ExistingRefs(ctx context.Context, kind evidence.RefKind,
	ids []int64) (map[int64]bool, error) {

	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	var table string
	switch kind {
	case evidence.RefVoucher:
		table = "voucher"
	case evidence.RefInvoice:
		table = "invoice"
	case evidence.RefBankFlow:
		table = "bank_flow"
	case evidence.RefExpense:
		table = "expense_claim"
	default:
		// 附件、合同、外部资料不走这张表
		return out, nil
	}
	ph := ""
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		if i > 0 {
			ph += ","
		}
		ph += "?"
		args = append(args, id)
	}
	rows, err := r.db.sql.QueryContext(ctx,
		`SELECT id FROM `+table+` WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, translateErr(err)
		}
		out[id] = true
	}
	return out, translateErr(rows.Err())
}

// EvidenceCounts 按期统计每个结论挂了几份证据（供 AI 与列表页快速用）。
func (r *EvidenceRepo) EvidenceCounts(ctx context.Context, k period.Key) (map[string]int, error) {
	rows, err := r.db.sql.QueryContext(ctx, `
		SELECT owner_type, owner_id, COUNT(*) FROM audit_evidence
		 WHERE year = ? AND month = ? GROUP BY owner_type, owner_id`,
		k.Year, k.Month)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var ownerType string
		var ownerID int64
		var n int
		if err := rows.Scan(&ownerType, &ownerID, &n); err != nil {
			return nil, translateErr(err)
		}
		out[evidenceKey(ownerType, ownerID)] = n
	}
	return out, translateErr(rows.Err())
}

// evidenceKey 是 (结论种类, id) 的字符串键，给 map 用。
func evidenceKey(ownerType string, ownerID int64) string {
	return fmt.Sprintf("%s/%d", ownerType, ownerID)
}
