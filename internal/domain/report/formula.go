// Package report 实现账簿与财务报表的取数与计算。
//
// # 为什么需要公式引擎
//
// Frappe Books 的资产负债表是「遍历科目树 + 按 rootType 分三段」——
// 科目即报表行。中国报表不是这样：报表行是**固定的、准则规定的项目**，
// 每个项目对应一组科目，并且有法定的勾稽关系。例如：
//
//	货币资金      = 库存现金 + 银行存款 + 其他货币资金
//	固定资产账面价值 = 固定资产原价 − 累计折旧
//	流动资产合计   = 行1+行2+…+行9+行14      ← 引用其它行
//	应收账款      = 应收账款明细借方余额 + 预收账款明细借方余额  ← 按方向拆分
//
// 因此必须有一个能表达「科目取数、行引用、按方向拆分」的公式语言。
package report

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/money"
)

// 公式相关错误。
var (
	ErrFormulaSyntax   = errors.New("report: 公式语法错误")
	ErrFormulaEval     = errors.New("report: 公式求值失败")
	ErrUnknownLine     = errors.New("report: 引用了不存在的报表行")
	ErrForwardRef      = errors.New("report: 只能引用前面的行")
	ErrUnknownFunction = errors.New("report: 未知的公式函数")
)

// ---------------------------------------------------------------------------
// 取数接口
// ---------------------------------------------------------------------------

// UnitBalance 是按「科目叶子 × 往来单位」分解的余额，原始口径（借 − 贷）。
//
// 之所以要分解到这个粒度：中国报表的「分析填列」规则要求按**明细**的
// 余额方向拆分。例如应收账款项目取应收账款各明细的**借方**余额，
// 而贷方余额要转列到预收账款 —— 只看科目总额是做不出来的。
//
// 采用辅助核算时，真正的「明细」是往来单位：同一个 1122 科目下，
// 客户甲可能挂借方、客户乙挂贷方。因此分解粒度是「科目 × 往来」。
type UnitBalance struct {
	AccountCode string
	ContactID   *int64
	Net         money.Money // 借 − 贷
}

// Resolver 为公式提供取数。
//
// 关键设计：`Value` 对资产负债表意味着「期末余额」，对利润表意味着
// 「本期发生额」—— 由实现方决定，公式语言本身不变。
// 这样同一套公式引擎可以服务余额表和发生额表两类报表。
type Resolver interface {
	// Value 返回科目前缀下全部叶子科目的取数之和，**按科目自身余额方向取正**。
	//
	// 之所以取正：这样「累计折旧」返回的是正数，公式 `@L(18)-@L(19)`
	// 才能得到正确的固定资产账面价值。若返回原始的借−贷，
	// 累计折旧会是负数，相减就变成了相加。
	Value(prefix string) money.Money

	// Analyze 返回科目前缀下按「科目叶子 × 往来单位」分解的原始净额。
	// 供 @analyze 使用。
	Analyze(prefix string) []UnitBalance
}

// ---------------------------------------------------------------------------
// 公式 AST
// ---------------------------------------------------------------------------

type node interface {
	eval(c *evalCtx) (money.Money, error)
	String() string
}

// accountNode 是科目取数项，如 "1001"。
type accountNode struct{ code string }

func (n *accountNode) String() string { return n.code }
func (n *accountNode) eval(c *evalCtx) (money.Money, error) {
	return c.resolver.Value(n.code), nil
}

// lineRefNode 是行引用，如 "@L(18)"。
type lineRefNode struct{ lineNo int }

func (n *lineRefNode) String() string { return fmt.Sprintf("@L(%d)", n.lineNo) }
func (n *lineRefNode) eval(c *evalCtx) (money.Money, error) {
	v, ok := c.lines[n.lineNo]
	if !ok {
		return 0, fmt.Errorf("%w: 行 %d（尚未计算，或该行不存在）",
			ErrUnknownLine, n.lineNo)
	}
	return v, nil
}

// analyzeNode 是「按明细方向拆分」，如 "@analyze(1122,2203,debit)"。
//
// 语义：扫描两个前缀下的全部「科目 × 往来」单元，取净额符号与 dir 一致者，
// 累加其绝对值。
//
//	@analyze(1122,2203,debit)   → 应收明细的借方余额 + 预收明细的借方余额
//	@analyze(2203,1122,credit)  → 预收明细的贷方余额 + 应收明细的贷方余额
type analyzeNode struct {
	prefixes []string
	dir      account.BalanceDir
}

func (n *analyzeNode) String() string {
	dir := "debit"
	if n.dir == account.DirCredit {
		dir = "credit"
	}
	return fmt.Sprintf("@analyze(%s,%s)", strings.Join(n.prefixes, ","), dir)
}

func (n *analyzeNode) eval(c *evalCtx) (money.Money, error) {
	var total money.Money
	for _, p := range n.prefixes {
		for _, u := range c.resolver.Analyze(p) {
			if u.Net.IsZero() {
				continue
			}
			isDebit := u.Net.IsPositive()
			if isDebit == (n.dir == account.DirDebit) {
				total = total.Add(u.Net.Abs())
			}
		}
	}
	return total, nil
}

// sumNode 是多行求和，如 "@sum(16,17,20)"。
type sumNode struct{ lines []int }

func (n *sumNode) String() string {
	parts := make([]string, len(n.lines))
	for i, l := range n.lines {
		parts[i] = strconv.Itoa(l)
	}
	return "@sum(" + strings.Join(parts, ",") + ")"
}

func (n *sumNode) eval(c *evalCtx) (money.Money, error) {
	var total money.Money
	for _, l := range n.lines {
		v, ok := c.lines[l]
		if !ok {
			return 0, fmt.Errorf("%w: 行 %d", ErrUnknownLine, l)
		}
		total = total.Add(v)
	}
	return total, nil
}

// binNode 是加减二元运算。
type binNode struct {
	op   byte // '+' 或 '-'
	l, r node
}

func (n *binNode) String() string {
	return "(" + n.l.String() + " " + string(n.op) + " " + n.r.String() + ")"
}

func (n *binNode) eval(c *evalCtx) (money.Money, error) {
	l, err := n.l.eval(c)
	if err != nil {
		return 0, err
	}
	r, err := n.r.eval(c)
	if err != nil {
		return 0, err
	}
	if n.op == '-' {
		return l.Sub(r), nil
	}
	return l.Add(r), nil
}

// ---------------------------------------------------------------------------
// 求值上下文
// ---------------------------------------------------------------------------

type evalCtx struct {
	resolver Resolver
	lines    map[int]money.Money
}

// ---------------------------------------------------------------------------
// Formula
// ---------------------------------------------------------------------------

// Formula 是一条已编译的报表取数公式。
type Formula struct {
	src  string
	root node
	// refs 是本公式引用到的行号，供依赖检查与自环检测使用。
	refs []int
}

// String 返回公式原文。
func (f *Formula) String() string { return f.src }

// Refs 返回公式引用到的行号。
func (f *Formula) Refs() []int { return f.refs }

// Empty 报告公式是否为空（例如「其他流动资产」这类需要手工填列的行）。
func (f *Formula) Empty() bool { return f.root == nil }

// Eval 求值。lines 必须已包含本公式引用到的全部行。
func (f *Formula) Eval(resolver Resolver, lines map[int]money.Money) (money.Money, error) {
	if f.root == nil {
		return 0, nil
	}
	return f.root.eval(&evalCtx{resolver: resolver, lines: lines})
}

// ---------------------------------------------------------------------------
// 解析
// ---------------------------------------------------------------------------

// ParseFormula 解析报表取数公式。
//
// 支持的形式：
//
//	1001                        科目取数（含全部下级）
//	1401+1402-1407+1405         加减组合
//	@L(18)-@L(19)               引用其它行的结果
//	@analyze(1122,2203,debit)   按明细余额方向拆分（中国报表特有）
//	@sum(16,17,20)              多行求和
//
// 空字符串是合法的，表示该行不由公式取数（需手工填列或留空）。
func ParseFormula(src string) (*Formula, error) {
	f := &Formula{src: src}
	s := strings.TrimSpace(src)
	if s == "" {
		return f, nil
	}
	p := &parser{src: s}
	n, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos < len(p.src) {
		return nil, fmt.Errorf("%w: %q 第 %d 个字符处有多余内容 %q",
			ErrFormulaSyntax, src, p.pos+1, p.src[p.pos:])
	}
	f.root = n
	f.refs = collectRefs(n)
	return f, nil
}

// MustParseFormula 与 ParseFormula 相同，失败时 panic。仅用于测试与内置定义。
func MustParseFormula(src string) *Formula {
	f, err := ParseFormula(src)
	if err != nil {
		panic(err)
	}
	return f
}

type parser struct {
	src string
	pos int
}

func (p *parser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *parser) peek() byte {
	if p.pos >= len(p.src) {
		return 0
	}
	return p.src[p.pos]
}

// parseExpr 解析加减表达式：term (('+'|'-') term)*
func (p *parser) parseExpr() (node, error) {
	left, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		op := p.peek()
		if op != '+' && op != '-' {
			return left, nil
		}
		p.pos++
		right, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		left = &binNode{op: op, l: left, r: right}
	}
}

// parseTerm 解析一个取数项。
func (p *parser) parseTerm() (node, error) {
	p.skipSpace()
	if p.peek() == '@' {
		return p.parseFunc()
	}
	// 科目编码：4/6/8 位数字
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		p.pos++
	}
	if p.pos == start {
		return nil, fmt.Errorf("%w: 第 %d 个字符处应为科目编码或 @函数，实际 %q",
			ErrFormulaSyntax, p.pos+1, p.rest())
	}
	code := p.src[start:p.pos]
	if err := validateAccountCode(code); err != nil {
		return nil, err
	}
	return &accountNode{code: code}, nil
}

func (p *parser) rest() string {
	if p.pos >= len(p.src) {
		return ""
	}
	end := p.pos + 20
	if end > len(p.src) {
		end = len(p.src)
	}
	return p.src[p.pos:end]
}

// parseFunc 解析 @name(args...)。
func (p *parser) parseFunc() (node, error) {
	p.pos++ // 跳过 @
	start := p.pos
	for p.pos < len(p.src) && isAlpha(p.src[p.pos]) {
		p.pos++
	}
	name := strings.ToLower(p.src[start:p.pos])
	p.skipSpace()
	if p.peek() != '(' {
		return nil, fmt.Errorf("%w: @%s 后应为 (", ErrFormulaSyntax, name)
	}
	p.pos++ // 跳过 (
	args, err := p.parseArgs()
	if err != nil {
		return nil, err
	}
	if p.peek() != ')' {
		return nil, fmt.Errorf("%w: @%s 缺少 )", ErrFormulaSyntax, name)
	}
	p.pos++ // 跳过 )

	switch name {
	case "l":
		if len(args) != 1 {
			return nil, fmt.Errorf("%w: @L 需要 1 个参数，实际 %d", ErrFormulaSyntax, len(args))
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 {
			return nil, fmt.Errorf("%w: @L 的参数应为行号，实际 %q", ErrFormulaSyntax, args[0])
		}
		return &lineRefNode{lineNo: n}, nil

	case "sum":
		if len(args) == 0 {
			return nil, fmt.Errorf("%w: @sum 至少需要 1 个参数", ErrFormulaSyntax)
		}
		lines := make([]int, 0, len(args))
		for _, a := range args {
			n, err := strconv.Atoi(a)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("%w: @sum 的参数应为行号，实际 %q", ErrFormulaSyntax, a)
			}
			lines = append(lines, n)
		}
		return &sumNode{lines: lines}, nil

	case "analyze":
		if len(args) < 2 || len(args) > 3 {
			return nil, fmt.Errorf("%w: @analyze 需要 2~3 个参数（科目,科目[,方向]），实际 %d",
				ErrFormulaSyntax, len(args))
		}
		n := &analyzeNode{dir: account.DirDebit}
		for _, a := range args[:2] {
			if err := validateAccountCode(a); err != nil {
				return nil, err
			}
			n.prefixes = append(n.prefixes, a)
		}
		if len(args) == 3 {
			switch strings.ToLower(args[2]) {
			case "debit":
				n.dir = account.DirDebit
			case "credit":
				n.dir = account.DirCredit
			default:
				return nil, fmt.Errorf("%w: @analyze 的方向应为 debit 或 credit，实际 %q",
					ErrFormulaSyntax, args[2])
			}
		}
		return n, nil

	default:
		return nil, fmt.Errorf("%w: @%s", ErrUnknownFunction, name)
	}
}

// parseArgs 解析逗号分隔的参数列表，支持嵌套括号。
func (p *parser) parseArgs() ([]string, error) {
	var args []string
	var cur strings.Builder
	depth := 1
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		switch ch {
		case '(':
			depth++
			cur.WriteByte(ch)
		case ')':
			depth--
			if depth == 0 {
				if s := strings.TrimSpace(cur.String()); s != "" {
					args = append(args, s)
				}
				return args, nil
			}
			cur.WriteByte(ch)
		case ',':
			if depth == 1 {
				args = append(args, strings.TrimSpace(cur.String()))
				cur.Reset()
			} else {
				cur.WriteByte(ch)
			}
		default:
			cur.WriteByte(ch)
		}
		p.pos++
	}
	return nil, fmt.Errorf("%w: 参数列表未闭合", ErrFormulaSyntax)
}

func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

// validateAccountCode 校验科目编码：纯数字且长度为 4/6/8。
func validateAccountCode(code string) error {
	switch len(code) {
	case 4, 6, 8:
	default:
		return fmt.Errorf("%w: 科目编码 %q 长度应为 4/6/8", ErrFormulaSyntax, code)
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return fmt.Errorf("%w: 科目编码 %q 应为纯数字", ErrFormulaSyntax, code)
		}
	}
	return nil
}

// collectRefs 收集公式引用到的行号。
func collectRefs(n node) []int {
	var out []int
	var walk func(node)
	walk = func(x node) {
		switch t := x.(type) {
		case *lineRefNode:
			out = append(out, t.lineNo)
		case *sumNode:
			out = append(out, t.lines...)
		case *binNode:
			walk(t.l)
			walk(t.r)
		}
	}
	if n != nil {
		walk(n)
	}
	return out
}
