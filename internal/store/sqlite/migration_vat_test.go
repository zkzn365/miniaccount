package sqlite

import (
	"context"
	"strings"
	"testing"
)

// 0009 迁移必须能在**旧账套**上把增值税结构改对。
//
// 直接把新库还原成旧形态再跑一次迁移，比只断言「新库是对的」有价值得多：
// 已经建过账的用户走的正是这条路径 —— 他们的库里有旧编码、
// 旧父子关系，而且可能已经有分录挂在那些科目上。
func TestMigration0009VATRestructure(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	// ---- 1. 还原成旧形态 ----
	// （旧形态：22210110-22210117 挂在 222101 下；出口退税是借方；没有 22210118）
	if err := db.WithTx(ctx, func(tx *Tx) error {
		steps := []string{
			`UPDATE account SET balance_dir = 'debit' WHERE code = '22210107'`,
		}
		for _, s := range steps {
			if _, err := tx.Exec(ctx, s); err != nil {
				return err
			}
		}
		// 八个别名为「专栏」实为明细科目的，搬回 222101 下、改回旧编码
		old := []struct{ code, name string }{
			{"222119", "应交增值税—待抵扣进项税额"},
			{"222120", "应交增值税—待认证进项税额"},
			{"222121", "应交增值税—待转销项税额"},
			{"222122", "应交增值税—增值税留抵税额"},
			{"222123", "应交增值税—简易计税"},
			{"222124", "应交增值税—预交增值税"},
			{"222125", "应交增值税—转让金融商品应交增值税"},
			{"222126", "应交增值税—代扣代交增值税"},
		}
		for i, o := range old {
			newCode := "2221011" + string(rune('0'+i))
			if i == 7 {
				newCode = "22210117"
			}
			if _, err := tx.Exec(ctx, `
				UPDATE account SET parent_id = (SELECT id FROM account WHERE code='222101'),
				                   code = ?, name = ?
				 WHERE code = ?`, newCode, o.name, o.code); err != nil {
				return err
			}
		}
		// 删掉新增的销项税额抵减
		if _, err := tx.Exec(ctx,
			`DELETE FROM account WHERE code = '22210118'`); err != nil {
			return err
		}
		// 迁移记录删掉，让它重跑
		_, err := tx.Exec(ctx,
			`DELETE FROM schema_migration WHERE version = 9`)
		return err
	}); err != nil {
		t.Fatalf("还原旧形态失败: %v", err)
	}

	// 确认真的回到了旧形态
	before, err := db.Accounts().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byCode := map[string]bool{}
	for _, a := range before {
		byCode[a.Code] = true
	}
	if byCode["22210118"] {
		t.Fatal("前置条件不成立：销项税额抵减应已删除")
	}
	if !byCode["22210117"] {
		t.Fatal("前置条件不成立：旧编码 22210117 应存在")
	}

	// ---- 2. 重跑迁移 ----
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("重跑迁移失败: %v", err)
	}

	// ---- 3. 校验结果 ----
	after, err := db.Accounts().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	info := map[string]struct {
		parent string
		dir    string
		name   string
		level  int
	}{}
	for _, a := range after {
		info[a.Code] = struct {
			parent string
			dir    string
			name   string
			level  int
		}{a.ParentCode, string(a.BalanceDir), a.Name, a.Level}
	}

	// 出口退税必须是贷方
	if got := info["22210107"].dir; got != "credit" {
		t.Errorf("出口退税方向 = %q，期望 credit", got)
	}
	// 销项税额抵减必须补上，且是借方
	if a, ok := info["22210118"]; !ok {
		t.Error("销项税额抵减未被补上")
	} else {
		if a.parent != "222101" {
			t.Errorf("销项税额抵减的父科目 = %q，期望 222101", a.parent)
		}
		if a.dir != "debit" {
			t.Errorf("销项税额抵减方向 = %q，期望 debit", a.dir)
		}
	}
	// 8 个明细科目必须挂到 2221 下、换成新编码、且不再用旧编码
	for _, c := range []string{"222119", "222120", "222121", "222122",
		"222123", "222124", "222125", "222126"} {
		a, ok := info[c]
		if !ok {
			t.Errorf("科目 %s 不存在", c)
			continue
		}
		if a.parent != "2221" {
			t.Errorf("%s 的父科目 = %q，期望 2221", c, a.parent)
		}
		// ★ 必须用 HasPrefix 而不是切片：一个汉字在 UTF-8 里占 3 字节，
		// a.name[:4] 取到的是「应交」加半个字，永远比不相等。
		if !strings.HasPrefix(a.name, "应交税费—") {
			t.Errorf("%s 的名称 = %q，应以「应交税费—」开头", c, a.name)
		}
		// ★ 层级必须跟着编码走：6 位编码 = 2 级。
		// 漏改的后果不是显示难看 —— 科目树加载会做
		// 「层级与编码长度一致」的完整性校验，直接打不开账套。
		if a.level != 2 {
			t.Errorf("%s 的层级 = %d，期望 2（6 位编码 = 2 级）", c, a.level)
		}
	}
	for _, c := range []string{"22210110", "22210111", "22210112", "22210113",
		"22210114", "22210115", "22210116", "22210117"} {
		if _, ok := info[c]; ok {
			t.Errorf("旧编码 %s 应已被替换掉", c)
		}
	}

	// 222101 下必须恰好是 10 个专栏
	var n int
	if err := db.sql.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM account
		 WHERE parent_id = (SELECT id FROM account WHERE code='222101')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Errorf("应交增值税下的专栏数 = %d，期望 10", n)
	}
	// 222101 仍是汇总科目（不可记账）
	if info["222101"].parent != "2221" {
		t.Errorf("222101 的父科目 = %q", info["222101"].parent)
	}
}

// 迁移碰到用户自己维护过的科目时不能乱动。
//
// 判据是「编码 + 名称 + 父科目」三重匹配。
// 这里模拟用户把「出口退税」改名去派别的用场 —— 名称对不上，
// 迁移就必须放手。
func TestMigration0009LeavesUserAccountsAlone(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	// 模拟用户把「出口退税」改成了自己需要的科目
	if err := db.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE account SET balance_dir = 'debit', name = '应交增值税—出口退税(自定)'
			 WHERE code = '22210107'`); err != nil {
			return err
		}
		// 再模拟一个「用户手工删掉了 22210110」的账套：
		// 迁移必须能容忍科目缺失，而不是报错或造出半个结构
		if _, err := tx.Exec(ctx, `DELETE FROM account WHERE code = '222119'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM schema_migration WHERE version = 9`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("重跑迁移失败: %v", err)
	}

	var dir, name string
	if err := db.sql.QueryRowContext(ctx,
		`SELECT balance_dir, name FROM account WHERE code = '22210107'`).
		Scan(&dir, &name); err != nil {
		t.Fatal(err)
	}
	if dir != "debit" || name != "应交增值税—出口退税(自定)" {
		t.Errorf("用户改过的科目被迁移动了：dir=%q name=%q", dir, name)
	}
	// 被用户删掉的明细科目不该被迁移擅自造回来
	var n int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM account WHERE code = '222119'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("用户删掉的科目被迁移擅自恢复了")
	}
}

// 迁移可以重复执行而不出错（幂等）。
func TestMigration0009Idempotent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	for i := 0; i < 3; i++ {
		if err := db.WithTx(ctx, func(tx *Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM schema_migration WHERE version = 9`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx); err != nil {
			t.Fatalf("第 %d 次重跑迁移失败: %v", i+1, err)
		}
	}

	var n int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM account WHERE code LIKE '2221%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 37 {
		t.Errorf("2221 系列科目数 = %d，期望 37（1 + 1 + 10 专栏 + 25 明细）", n)
	}
}
