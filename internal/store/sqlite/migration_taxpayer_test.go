package sqlite

import (
	"context"
	"testing"

	"miniaccount/internal/domain/vat"
)

// ★ 迁移必须把旧的 tax_type 迁到 vat_status，并**不猜**企业规模类型。
//
// tax_type 表达的是纳税人身份（下拉里写的就是「一般纳税人 / 小规模纳税人」），
// 所以它迁到 vat_status；但企业规模类型无法从它反推 ——
// 小规模纳税人可能是微型、小型，也可能是大型集团子公司。
// 猜一个会直接导致小型微利企业优惠算错。
func TestMigration0010SplitsIdentityFromScale(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct{ oldTax, wantVAT string }{
		{"small", "small_scale"},
		{"general", "general"},
	} {
		// 把库迁到 0009 为止 —— 也就是**迁移前**的样子，
		// 然后手工写入一条老账套记录
		db := newDBAtVersion(t, 9)
		if _, err := db.sql.ExecContext(ctx, `
			INSERT INTO book (id, company_name, credit_code, legal_person, address,
				phone, email, bank_name, bank_account, standard, base_currency,
				tax_type, start_year, start_month, setup_complete, created_at, updated_at)
			VALUES (1,'老账套有限公司','','','','','','','','小企业会计准则','CNY',
				?,2025,3,1,?,?)`,
			tc.oldTax, nowString(), nowString()); err != nil {
			t.Fatalf("写入老账套失败: %v", err)
		}

		// 跑剩下的迁移（0010）
		if err := db.Migrate(ctx); err != nil {
			t.Fatalf("迁移失败: %v", err)
		}

		var vatStatus, scale, effFrom string
		if err := db.sql.QueryRowContext(ctx, `
			SELECT vat_status, enterprise_scale, vat_status_effective_from
			  FROM book WHERE id = 1`).Scan(&vatStatus, &scale, &effFrom); err != nil {
			t.Fatal(err)
		}
		if vatStatus != tc.wantVAT {
			t.Errorf("tax_type=%s → vat_status=%s，期望 %s", tc.oldTax, vatStatus, tc.wantVAT)
		}
		// ★ 规模类型必须留空，由用户补填。
		//
		// 小规模纳税人可能是微型、小型，也可能是年销售额未超 500 万元的
		// 大型集团子公司 —— 从 tax_type 根本推不出来。
		// 猜一个会直接导致小型微利企业优惠算错。
		if scale != "" {
			t.Errorf("★ 企业规模类型被猜成了 %q —— 迁移无法从旧字段反推，不该替用户猜", scale)
		}
		// 生效日要落在账套启用日：唯一不会与已有分录冲突的选择
		if effFrom != "2025-03-01" {
			t.Errorf("生效日 = %q，期望 2025-03-01（账套启用日）", effFrom)
		}

		// 历史表要有对应的第一条记录
		var n int
		if err := db.sql.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM vat_status_history WHERE status = ?`,
			tc.wantVAT).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("身份历史记录数 = %d，期望 1", n)
		}
	}
}

// ★ 两套身份必须分别保存，且互不派生。
//
// 「小微企业」与「增值税小规模纳税人」是不同依据、不同用途的分类：
// 小微企业**可以**自愿登记为一般纳税人。软件用一个字段表达两件事，
// 任何「是不是小微 → 能不能抵扣进项」的推断都会算错税。
func TestScaleAndVATStatusStoredSeparately(t *testing.T) {
	ctx := context.Background()
	db := newEmptyDB(t)

	// 小微企业 + 一般纳税人 —— 完全合法的组合，
	// 而且是「为了抵扣进项、为了给大客户开专票」而自愿登记的最常见情形。
	if _, err := db.CreateBook(ctx, CreateBookInput{
		CompanyName:     "小微企业但是一般纳税人有限公司",
		EnterpriseScale: string(vat.ScaleSmall),
		VATStatus:       string(vat.VATGeneral),
		StartYear:       2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	}); err != nil {
		t.Fatalf("建账失败: %v", err)
	}

	got, err := db.Books().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.EnterpriseScale != string(vat.ScaleSmall) {
		t.Errorf("企业规模类型 = %q，期望 small", got.EnterpriseScale)
	}
	if got.VATStatus != string(vat.VATGeneral) {
		t.Errorf("增值税纳税人身份 = %q，期望 general", got.VATStatus)
	}
	// 这两者同时成立，正是本测试要钉住的事实
	if !vat.EnterpriseScale(got.EnterpriseScale).IsSmallOrMicro() {
		t.Error("小型企业应属于小微口径")
	}
	if !vat.VATStatus(got.VATStatus).CanDeductInput() {
		t.Error("一般纳税人应能抵扣进项")
	}
}

// 大企业也可以是增值税小规模纳税人（如刚成立的大型集团子公司）。
func TestLargeEnterpriseCanBeSmallScaleTaxpayer(t *testing.T) {
	ctx := context.Background()
	db := newEmptyDB(t)
	if _, err := db.CreateBook(ctx, CreateBookInput{
		CompanyName:     "大型集团子公司",
		EnterpriseScale: string(vat.ScaleLarge),
		VATStatus:       string(vat.VATSmallScale),
		StartYear:       2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	}); err != nil {
		t.Fatalf("建账失败: %v", err)
	}
	got, err := db.Books().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.EnterpriseScale != string(vat.ScaleLarge) ||
		got.VATStatus != string(vat.VATSmallScale) {
		t.Errorf("规模=%q 身份=%q", got.EnterpriseScale, got.VATStatus)
	}
	// 历史字段要与新字段一致，避免旧备份/旧脚本读到空
	if got.TaxType != TaxTypeSmall {
		t.Errorf("历史字段 tax_type = %q，期望 small", got.TaxType)
	}
}

// 企业规模类型允许留空（建账时用户未必想得起来）。
func TestEnterpriseScaleMayBeEmpty(t *testing.T) {
	ctx := context.Background()
	db := newEmptyDB(t)
	if _, err := db.CreateBook(ctx, CreateBookInput{
		CompanyName: "规模未填写有限公司",
		VATStatus:   string(vat.VATGeneral),
		StartYear:   2025, StartMonth: 1,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 3,
	}); err != nil {
		t.Fatalf("规模留空不该阻止建账: %v", err)
	}
	got, err := db.Books().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.EnterpriseScale != "" {
		t.Errorf("规模类型 = %q，期望留空", got.EnterpriseScale)
	}
}

// 身份生效日默认取账套启用日。
func TestVATStatusEffectiveFromDefaultsToStart(t *testing.T) {
	ctx := context.Background()
	db := newEmptyDB(t)
	if _, err := db.CreateBook(ctx, CreateBookInput{
		CompanyName: "X", VATStatus: string(vat.VATGeneral),
		StartYear: 2025, StartMonth: 4,
		ThroughYear: 2025, CurrentYear: 2025, CurrentMonth: 6,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := db.Books().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.VATStatusEffectiveFrom != "2025-04-01" {
		t.Errorf("生效日 = %q，期望 2025-04-01（账套启用日）",
			got.VATStatusEffectiveFrom)
	}
}

// 非法取值要挡住。
func TestCreateBookRejectsBadIdentity(t *testing.T) {
	ctx := context.Background()
	cases := []CreateBookInput{
		{CompanyName: "X", StartYear: 2025, StartMonth: 1, VATStatus: "bogus"},
		{CompanyName: "X", StartYear: 2025, StartMonth: 1, EnterpriseScale: "bogus"},
		{CompanyName: "X", StartYear: 2025, StartMonth: 1, TaxType: "bogus"},
		{CompanyName: "X", StartYear: 2025, StartMonth: 1,
			VATStatusEffectiveFrom: "2025/01/01"},
	}
	for i, in := range cases {
		db := newEmptyDB(t)
		if _, err := db.CreateBook(ctx, in); err == nil {
			t.Errorf("第 %d 个非法输入应当报错", i+1)
		}
	}
}
