package sqlite

import (
	"context"
	"errors"
	"testing"
)

// ★ 有上级的科目必须能按编码单独查出来。
//
// 这是一条**一直存在**的 bug，被科目管理功能撞出来了：
// scanAccounts 要求「结果集里必须有父科目」，而单行查询（GetByCode）
// 的结果集里只有它自己 —— 于是任何一个有上级的科目都查不出来，
// 报的还是「记录不存在」。调用方信了这句话，转头去新建，
// 最后撞在唯一约束上，用户看到一句数据库错误。
func TestGetByCodeWorksForChildAccounts(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	// 5602 是预置的汇总科目，560201 是它下面的明细 —— 一定有上级
	a, err := db.Accounts().GetByCode(ctx, "560201")
	if err != nil {
		t.Fatalf("★ 查有上级的科目失败：%v", err)
	}
	if a.Code != "560201" || a.ParentCode != "5602" {
		t.Errorf("查出来的不对：code=%s parent=%s", a.Code, a.ParentCode)
	}
}

// 真的不存在时，仍然要报 ErrNotFound。
func TestGetByCodeStillReportsNotFound(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if _, err := db.Accounts().GetByCode(ctx, "999999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的科目应当报 ErrNotFound，实际 %v", err)
	}
}
