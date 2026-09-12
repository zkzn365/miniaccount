package sqlite

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ★ 预置 SQL 的事务控制剔除 —— 一条只在 Windows 上炸过的路径
// ---------------------------------------------------------------------------
//
// 事故经过：`stripTxControl` 原来写的是 `TrimSpace(TrimSuffix(line, ";"))`，
// **先去分号再去空白**。Git for Windows 默认 core.autocrlf=true，
// 检出的 .sql 是 CRLF，行尾是 `\r` 而不是 `;` —— TrimSuffix 扑空，
// 「BEGIN TRANSACTION;」原样留下并被当成语句执行，
// 在已有事务里再 BEGIN，Windows 上建账直接失败：
//
//	写入预置科目表: SQL logic error:
//	cannot start a transaction within a transaction (1)
//
// 而 macOS/Linux 检出的是 LF，同样这行代码一路正常。
// 这类问题本地怎么试都试不出来 —— 所以下面每一条都同时喂 LF 和 CRLF。

const sampleScript = "-- 说明\n\nBEGIN TRANSACTION;\n\n" +
	"INSERT INTO account (code) VALUES ('1001');\n" +
	"INSERT INTO account (code) VALUES ('1002');\n\nCOMMIT;\n"

// ★ 嵌入的种子脚本必须是纯 LF。
//
// 这是根因那一层的**直接**防线：不管是谁的机器、git 配成什么样，
// 只要编出来的字节里有 \r，这条测试立刻红，并且告诉你去看 .gitattributes。
// 比等到用户在 Windows 上建账失败要早得多。
func TestEmbeddedSeedScriptIsLFOnly(t *testing.T) {
	if i := strings.IndexByte(seedAccountsSQL, '\r'); i >= 0 {
		t.Fatalf("seed_accounts.sql 里出现了 \\r（偏移 %d）—— "+
			"多半是 Windows 检出时 git 把换行换成了 CRLF。\n"+
			"仓库根目录的 .gitattributes 会把检出统一成 LF；"+
			"如果它还在，检查一下它有没有生效（git check-attr text -- internal/store/sqlite/seed_accounts.sql）",
			i)
	}
}

func TestStripTxControlHandlesBothLineEndings(t *testing.T) {
	cases := []struct {
		name string
		in   string
		body string // 剔除之后必须原样留下的正文
	}{
		{"LF（macOS / Linux 检出）", sampleScript, "INSERT INTO account"},
		{"CRLF（Windows 默认检出）",
			strings.ReplaceAll(sampleScript, "\n", "\r\n"), "INSERT INTO account"},
		{"CRLF + 注释与空行都在",
			"\r\n-- x\r\n\r\nBEGIN TRANSACTION;\r\nSELECT 1;\r\nCOMMIT;\r\n\r\n", "SELECT 1;"},
		{"分号前有空格", "BEGIN TRANSACTION ;\nSELECT 1;\nCOMMIT ;\n", "SELECT 1;"},
		{"行尾有空格", "BEGIN TRANSACTION;  \nSELECT 1;\nCOMMIT;  \n", "SELECT 1;"},
		{"小写 + 制表符缩进", "\tbegin transaction;\n\tselect 1;\ncommit;\n", "select 1;"},
		{"关键字之间有多个空格", "BEGIN   TRANSACTION;\nSELECT 1;\nEND   TRANSACTION;\n", "SELECT 1;"},
	}

	for _, c := range cases {
		out, err := stripTxControl(c.in)
		if err != nil {
			t.Errorf("%s：不该报错，实际 %v", c.name, err)
			continue
		}
		if bad, found := findTxControl(out); found {
			t.Errorf("%s：还剩事务控制语句 %q —— 会在已有事务里再开一个事务，"+
				"Windows 用户就是这样建账失败的", c.name, bad)
		}
		if strings.Contains(out, "\r") {
			t.Errorf("%s：输出里还有 \\r", c.name)
		}
		// 正文不能被误删
		if !strings.Contains(out, c.body) {
			t.Errorf("%s：正文 %q 被削掉了，实际输出 %q", c.name, c.body, out)
		}
	}
}

// 正文里出现这些词不能被误删 —— 剔除只认整行。
func TestStripTxControlKeepsWordsInsideStatements(t *testing.T) {
	in := "INSERT INTO account (remark) VALUES ('期初 BEGIN TRANSACTION 余额');\n" +
		"INSERT INTO account (remark) VALUES ('COMMIT 之后结转');\n"
	out, err := stripTxControl(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "期初 BEGIN TRANSACTION 余额") ||
		!strings.Contains(out, "COMMIT 之后结转") {
		t.Errorf("正文里的字被误删了：%q", out)
	}
}

// 兜底：万一还有剔不掉的，要报一句人话，而不是把 SQLite 的
// 「cannot start a transaction within a transaction」丢给用户。
func TestStripTxControlReportsWhatItCouldNotStrip(t *testing.T) {
	// 构造一个**剔除器认不出、但 SQLite 会执行**的形态：
	// 关键字中间夹注释。真实世界里对应「有人用别的工具重写了脚本」。
	in := "BEGIN /* 事务 */ TRANSACTION;\nSELECT 1;\n"
	out, err := stripTxControl(in)
	// 这一行确实剔不掉 —— 那就必须报错，而且要说清楚是什么、往哪儿看
	if err == nil {
		t.Fatalf("剔不掉时应当报错，实际返回 %q", out)
	}
	if !strings.Contains(err.Error(), "BEGIN") {
		t.Errorf("错误里要点出是哪一句，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "gitattributes") {
		t.Errorf("错误里要指向 .gitattributes（那是最常见的原因），实际 %v", err)
	}
}
