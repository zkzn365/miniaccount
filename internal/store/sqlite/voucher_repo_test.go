package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
)

// ---------------------------------------------------------------------------
// ★ 草稿过账：不能变成两张凭证
// ---------------------------------------------------------------------------

// draftBook 建账并返回科目 id 映射 + 一个往来单位。
func draftBook(t *testing.T) (*DB, map[string]int64, int64) {
	t.Helper()
	db := newTestDB(t)
	accIDs, err := db.Accounts().IDsByCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sh := mustContacts(t, db, "shareholder", "张三")
	return db, accIDs, sh
}

func draftVoucher(t *testing.T, sh int64) *voucher.Voucher {
	t.Helper()
	v, err := voucher.New(voucher.WordJi, calendar.MustParse("2025-09-15"), "李会计")
	if err != nil {
		t.Fatal(err)
	}
	v.Remark = "收到股东投资款"
	if err := v.AddEntry(ledger.Entry{
		AccountCode: "1002", Summary: "收到投资款",
		Debit: money100(100000),
	}); err != nil {
		t.Fatal(err)
	}
	if err := v.AddEntry(ledger.Entry{
		AccountCode: "3001", Summary: "收到投资款", Credit: money100(100000),
		Aux: ledger.Aux{ContactID: &sh},
	}); err != nil {
		t.Fatal(err)
	}
	return v
}

// ★ 必须先存草稿、再过账，验证账上**只有一张**凭证。
//
// 这是一个真实发生过的 bug：PostInTx 无条件 INSERT，
// 于是「把已有草稿过账」变成了「再插一张一模一样的凭证」——
// 原草稿还留在列表里，用户以为过账失败又点了一次，
// 结果同一笔业务在账上记了三遍。
func TestPostExistingDraftDoesNotDuplicate(t *testing.T) {
	ctx := context.Background()
	db, accIDs, sh := draftBook(t)

	v := draftVoucher(t, sh)
	saved, err := db.Vouchers().SaveDraft(ctx, DraftInput{Voucher: v, CreatedBy: "李会计"})
	if err != nil {
		t.Fatalf("保存草稿失败: %v", err)
	}
	if saved.VoucherID == 0 {
		t.Fatal("应返回凭证 id")
	}

	// 过账
	loaded, err := db.Vouchers().Get(ctx, saved.VoucherID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Vouchers().Post(ctx, PostInput{
		Voucher: loaded, Accounts: accIDs, PostingBy: "王主管", At: time.Now(),
	})
	if err != nil {
		t.Fatalf("过账失败: %v", err)
	}
	if res.VoucherID != saved.VoucherID {
		t.Errorf("过账应更新原草稿（id=%d），实际新建了 id=%d",
			saved.VoucherID, res.VoucherID)
	}

	// 账上只能有一张凭证
	assertVoucherCount(t, db, 1)

	got, err := db.Vouchers().Get(ctx, saved.VoucherID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != voucher.StatusPosted {
		t.Errorf("状态 = %s，期望 posted", got.Status)
	}
	if got.No == "" {
		t.Error("过账后应有凭证号")
	}
	if len(got.Entries) != 2 {
		t.Errorf("分录数 = %d，期望 2（不能因为替换而丢失）", len(got.Entries))
	}
	// 总账也要跟着写
	var ledgerRows int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM ledger_entry WHERE voucher_id = ?`, saved.VoucherID).
		Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 2 {
		t.Errorf("总账分录数 = %d，期望 2", ledgerRows)
	}
	// 草稿表里不该再有残留
	var entryRows int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM voucher_entry WHERE voucher_id = ?`, saved.VoucherID).
		Scan(&entryRows); err != nil {
		t.Fatal(err)
	}
	if entryRows != 2 {
		t.Errorf("凭证分录数 = %d，期望 2（过账前应先清掉草稿的行）", entryRows)
	}
}

// 连续过账多张草稿：凭证号必须连续，不能因为重复插入而跳号
func TestPostMultipleDraftsKeepsSequence(t *testing.T) {
	ctx := context.Background()
	db, accIDs, sh := draftBook(t)

	var ids []int64
	for i := 0; i < 3; i++ {
		v := draftVoucher(t, sh)
		v.Remark = "第 " + string(rune('一'+i)) + " 笔"
		s, err := db.Vouchers().SaveDraft(ctx, DraftInput{Voucher: v, CreatedBy: "李会计"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.VoucherID)
	}

	var nos []string
	for _, id := range ids {
		loaded, _ := db.Vouchers().Get(ctx, id)
		res, err := db.Vouchers().Post(ctx, PostInput{
			Voucher: loaded, Accounts: accIDs, PostingBy: "王主管", At: time.Now(),
		})
		if err != nil {
			t.Fatalf("过账失败: %v", err)
		}
		nos = append(nos, res.No)
	}
	want := []string{"记-2025-09-0001", "记-2025-09-0002", "记-2025-09-0003"}
	for i := range want {
		if nos[i] != want[i] {
			t.Errorf("第 %d 张凭证号 = %s，期望 %s（完整：%v）", i+1, nos[i], want[i], nos)
		}
	}
	assertVoucherCount(t, db, 3)
}

// 过账时若改了草稿内容，改动要落到账上（而不是保留旧值）
func TestPostDraftAppliesEdits(t *testing.T) {
	ctx := context.Background()
	db, accIDs, sh := draftBook(t)

	v := draftVoucher(t, sh)
	s, err := db.Vouchers().SaveDraft(ctx, DraftInput{Voucher: v, CreatedBy: "李会计"})
	if err != nil {
		t.Fatal(err)
	}

	// 改摘要与金额
	loaded, _ := db.Vouchers().Get(ctx, s.VoucherID)
	loaded.Remark = "改过的摘要"
	loaded.AttachCount = 3
	loaded.Entries[0].Debit = money100(88888)
	loaded.Entries[0].Summary = "改过的摘要"
	loaded.Entries[1].Credit = money100(88888)
	loaded.Entries[1].Summary = "改过的摘要"
	if _, err := db.Vouchers().SaveDraft(ctx, DraftInput{
		Voucher: loaded, CreatedBy: "李会计",
	}); err != nil {
		t.Fatalf("重新保存草稿失败: %v", err)
	}

	final, _ := db.Vouchers().Get(ctx, s.VoucherID)
	if _, err := db.Vouchers().Post(ctx, PostInput{
		Voucher: final, Accounts: accIDs, PostingBy: "王主管", At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	got, _ := db.Vouchers().Get(ctx, s.VoucherID)
	if got.Remark != "改过的摘要" {
		t.Errorf("摘要 = %q，期望「改过的摘要」", got.Remark)
	}
	if got.AttachCount != 3 {
		t.Errorf("附单据数 = %d，期望 3", got.AttachCount)
	}
	if got.TotalDebit() != money100(88888) {
		t.Errorf("金额 = %s，期望 888.88", got.TotalDebit())
	}
	// 试算必须仍然平衡
	d, c, err := db.Vouchers().TrialBalance(ctx, period.NewKey(2025, 9))
	if err != nil {
		t.Fatal(err)
	}
	if d != c {
		t.Errorf("试算不平衡：借 %s ≠ 贷 %s", d, c)
	}
}

func assertVoucherCount(t *testing.T, db *DB, want int) {
	t.Helper()
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM voucher`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Errorf("凭证数 = %d，期望 %d", n, want)
	}
}

// ---------------------------------------------------------------------------
// 草稿的增删改查
// ---------------------------------------------------------------------------

// 只有草稿能删
func TestDeleteDraftOnly(t *testing.T) {
	ctx := context.Background()
	db, accIDs, sh := draftBook(t)

	v := draftVoucher(t, sh)
	s, _ := db.Vouchers().SaveDraft(ctx, DraftInput{Voucher: v, CreatedBy: "李会计"})

	if err := db.Vouchers().DeleteDraft(ctx, s.VoucherID); err != nil {
		t.Fatalf("删除草稿失败: %v", err)
	}
	assertVoucherCount(t, db, 0)
	// 分录也要跟着删干净（外键 CASCADE）
	var n int
	_ = db.SQL().QueryRow(`SELECT COUNT(*) FROM voucher_entry`).Scan(&n)
	if n != 0 {
		t.Errorf("残留凭证分录 %d 条", n)
	}

	// 已过账的凭证不能删
	v2 := draftVoucher(t, sh)
	s2, _ := db.Vouchers().SaveDraft(ctx, DraftInput{Voucher: v2, CreatedBy: "李会计"})
	loaded, _ := db.Vouchers().Get(ctx, s2.VoucherID)
	if _, err := db.Vouchers().Post(ctx, PostInput{
		Voucher: loaded, Accounts: accIDs, PostingBy: "王主管", At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Vouchers().DeleteDraft(ctx, s2.VoucherID); !errors.Is(err, ErrNotDraft) {
		t.Errorf("已过账凭证应拒绝删除并报 ErrNotDraft，实际 %v", err)
	}
	assertVoucherCount(t, db, 1)
}

// 重复保存草稿是「替换」而不是「追加分录」
func TestSaveDraftReplacesEntries(t *testing.T) {
	ctx := context.Background()
	db, _, sh := draftBook(t)

	v := draftVoucher(t, sh)
	s, _ := db.Vouchers().SaveDraft(ctx, DraftInput{Voucher: v, CreatedBy: "李会计"})

	for i := 0; i < 3; i++ {
		loaded, _ := db.Vouchers().Get(ctx, s.VoucherID)
		if _, err := db.Vouchers().SaveDraft(ctx, DraftInput{
			Voucher: loaded, CreatedBy: "李会计",
		}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = db.SQL().QueryRow(
		`SELECT COUNT(*) FROM voucher_entry WHERE voucher_id = ?`, s.VoucherID).Scan(&n)
	if n != 2 {
		t.Errorf("反复保存后分录数 = %d，期望 2（每次应是整组替换）", n)
	}
}

// 草稿不占号：写十张草稿，凭证表的 seq 全是 0、no 全为空
func TestDraftsHaveNoNumber(t *testing.T) {
	ctx := context.Background()
	db, _, sh := draftBook(t)

	for i := 0; i < 5; i++ {
		v := draftVoucher(t, sh)
		if _, err := db.Vouchers().SaveDraft(ctx, DraftInput{
			Voucher: v, CreatedBy: "李会计",
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.SQL().Query(`SELECT seq, no FROM voucher`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var no string
		if err := rows.Scan(&seq, &no); err != nil {
			t.Fatal(err)
		}
		if seq != 0 || no != "" {
			t.Errorf("草稿不该占号，实际 seq=%d no=%q", seq, no)
		}
	}
}

// ★ 小规模纳税人不得抵扣进项税额，而且必须**报错**而不是照记。
//
// 科目表是同一份，小规模账套同样有「应交增值税—进项税额」。
// 不挡的话，发票模块把专票进项税挂上去会静默成功，做出一笔
// 「借 费用 10000 / 借 进项税额 1300 / 贷 应付账款 11300」——
// 费用少计 1300、进项税虚挂 1300，而小规模根本不能抵。
func TestSmallTaxpayerCannotPostInputVAT(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		taxType string
		wantOK  bool
	}{
		{"general", true},
		{"small", false},
	} {
		db := newTestDB(t)
		if err := db.WithTx(ctx, func(tx *Tx) error {
			_, err := tx.Exec(ctx, `UPDATE book SET tax_type = ? WHERE id = 1`, tc.taxType)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		accIDs, err := db.Accounts().IDsByCode(ctx)
		if err != nil {
			t.Fatal(err)
		}
		dept := int64(1)

		v, err := voucher.New(voucher.WordJi, calendar.MustParse("2025-09-05"), "李会计")
		if err != nil {
			t.Fatal(err)
		}
		// 借 办公费 10000 / 借 进项税额 1300 / 贷 银行存款 11300
		if err := v.AddEntry(ledger.Entry{
			AccountCode: "560206", Summary: "采购", Debit: money100(10000),
			Aux: ledger.Aux{DeptID: &dept}}); err != nil {
			t.Fatal(err)
		}
		if err := v.AddEntry(ledger.Entry{
			AccountCode: "22210101", Summary: "采购", Debit: money100(1300)}); err != nil {
			t.Fatal(err)
		}
		if err := v.AddEntry(ledger.Entry{
			AccountCode: "1002", Summary: "采购", Credit: money100(11300)}); err != nil {
			t.Fatal(err)
		}

		_, perr := db.Vouchers().Post(ctx, PostInput{
			Voucher: v, Accounts: accIDs, PostingBy: "王主管", At: time.Now()})

		if tc.wantOK && perr != nil {
			t.Errorf("%s：一般纳税人应能抵扣进项，实际报错 %v", tc.taxType, perr)
		}
		if !tc.wantOK {
			if perr == nil {
				t.Fatalf("★ %s：小规模纳税人挂进项税额竟然过账成功了", tc.taxType)
			}
			// 报错要讲清「该怎么记」，否则用户只知道不行、不知道怎么行
			msg := perr.Error()
			if !strings.Contains(msg, "小规模") || !strings.Contains(msg, "全额计入") {
				t.Errorf("报错应说明规则与正确做法，实际 %q", msg)
			}
			// 而且不能留下任何分录
			var n int
			if err := db.sql.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM ledger_entry`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("被拒绝的凭证不该留下 %d 条总账分录", n)
			}
		}
	}
}
