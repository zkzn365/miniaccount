package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/voucher"
)

// sep / oct 是夹具用的日期。
var (
	sep1  = calendar.MustParse("2025-09-01")
	sep30 = calendar.MustParse("2025-09-30")
)

// postLines 记一张凭证，lines 是「科目,摘要,借,贷」四元组。
func postLines(t *testing.T, db *DB, accIDs map[string]int64,
	date, remark string, lines ...[4]any) {
	t.Helper()
	ctx := context.Background()
	v, err := voucher.New(voucher.WordJi, calendar.MustParse(date), "李会计")
	if err != nil {
		t.Fatalf("建凭证失败: %v", err)
	}
	v.Remark = remark
	for _, ln := range lines {
		e := ledger.Entry{
			AccountCode: ln[0].(string),
			Summary:     ln[1].(string),
			Debit:       money.Money(ln[2].(int64)),
			Credit:      money.Money(ln[3].(int64)),
		}
		// 5602xx 各明细要求部门辅助核算
		if strings.HasPrefix(e.AccountCode, "56") {
			dept := int64(1)
			e.Aux = ledger.Aux{DeptID: &dept}
		}
		if err := v.AddEntry(e); err != nil {
			t.Fatalf("加分录失败（%s）：%v", e.AccountCode, err)
		}
	}
	saved, err := db.Vouchers().SaveDraft(ctx, DraftInput{Voucher: v, CreatedBy: "李会计"})
	if err != nil {
		t.Fatalf("保存凭证失败: %v", err)
	}
	loaded, err := db.Vouchers().Get(ctx, saved.VoucherID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Vouchers().Post(ctx, PostInput{
		Voucher: loaded, Accounts: accIDs, PostingBy: "李会计", At: time.Now(),
	}); err != nil {
		t.Fatalf("过账失败: %v", err)
	}
}

// ★ 对方科目：同一张凭证里其他分录所在的科目。
//
// 日记账（现金/银行存款）最要紧的一列 —— 会计看银行存款日记账时
// 真正想知道的是「这笔钱从哪来、到哪去」，只给借贷金额不够。
func TestDetailIncludesContraAccounts(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seedBook(t, db)

	rows, err := db.Vouchers().Detail(ctx, "1002", sep1, sep30)
	if err != nil {
		t.Fatalf("取明细账失败: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("夹具里 1002 应有发生额")
	}

	bySummary := map[string]LedgerRow{}
	for _, r := range rows {
		bySummary[r.Summary] = r
	}

	// ① 借 1002 / 贷 3001 实收资本 —— 单一对方科目
	if r, ok := bySummary["收到实收资本"]; ok {
		if len(r.ContraAccounts) != 1 || r.ContraAccounts[0] != "实收资本" {
			t.Errorf("对方科目 = %v，期望 [实收资本]", r.ContraAccounts)
		}
	} else {
		t.Error("没找到「收到实收资本」那一行")
	}

	// ④ 借 1122 / 贷 5001 + 贷 22210102 —— 这一笔与 1002 无关，
	// 但 1002 上另有销售收款；下面单独验多对方科目的情形
	for _, r := range rows {
		if r.AccountCode == "1002" && r.Credit > 0 {
			// 贷 1002 的行，对方科目应指向借方那一侧
			if len(r.ContraAccounts) == 0 {
				t.Errorf("「%s」这行的对方科目为空", r.Summary)
			}
		}
	}
}

// 多对方科目：一张凭证里除本行外的所有科目都要列出来。
func TestDetailContraAccountsMultiple(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	f := seedBook(t, db)

	// 借 1002 100,000 / 贷 5001 80,000 + 贷 22210102 20,000
	postLines(t, db, f.AccIDs, "2025-09-28", "多对方科目测试",
		[4]any{"1002", "销售收入", int64(money100(100000)), int64(0)},
		[4]any{"5001", "销售收入", int64(0), int64(money100(80000))},
		[4]any{"22210102", "销项税额", int64(0), int64(money100(20000))},
	)

	rows, err := db.Vouchers().Detail(ctx, "1002", sep1, sep30)
	if err != nil {
		t.Fatal(err)
	}
	var got *LedgerRow
	for i := range rows {
		if rows[i].Summary == "销售收入" {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatal("没找到销售收入那一行")
	}
	if len(got.ContraAccounts) != 2 {
		t.Fatalf("对方科目 = %v，期望 2 个", got.ContraAccounts)
	}
	joined := strings.Join(got.ContraAccounts, "、")
	if !strings.Contains(joined, "主营业务收入") || !strings.Contains(joined, "销项税额") {
		t.Errorf("对方科目 = %q，应同时含主营业务收入与销项税额", joined)
	}
	// 顺序必须稳定：按**科目编码**排，也就是科目表的顺序。
	// 22210102 应交增值税在 5001 主营业务收入之前 ——
	// 这正是会计翻科目表时的顺序，比按名称拼音排更符合直觉。
	want := []string{"销项税额", "主营业务收入"}
	for i := range want {
		if got.ContraAccounts[i] != want[i] {
			t.Errorf("顺序 = %v，期望 %v（按科目编码排）",
				got.ContraAccounts, want)
			break
		}
	}
}

// 单边凭证（理论上不该有）不该 panic，对方科目为空。
func TestDetailContraAccountsSingleLine(t *testing.T) {
	got := contraNames("", map[string]string{})
	if len(got) != 0 {
		t.Errorf("空输入应得到空列表，实际 %v", got)
	}
	// 科目表里查不到的编码退化为显示编码，而不是空白
	got = contraNames("9999", map[string]string{})
	if len(got) != 1 || got[0] != "9999" {
		t.Errorf("查不到的科目应退化为编码，实际 %v", got)
	}
}
