package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"miniaccount/internal/bankcsv"
	"miniaccount/internal/domain/bank"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/store/sqlite"
)

// cmdBank 是银行流水导入子命令组。
//
// 银行流水是这款软件里**最高频的入口**：小微企业会计每个月
// 真正要花时间的不是录凭证，而是把几十上百条流水变成分录。
// 因此这条链路必须能一键跑通：
//
//	import  读对账单 → 去重入库
//	match   按「规则 → 历史 → AI」三层匹配，写回对方科目提议
//	list    看看匹配成了什么样
//	post    确认后批量生成凭证
func cmdBank(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(`用法：miniaccount bank <import|match|list|post|rules> [参数]

  import  导入银行对账单 CSV
  match   对未匹配的流水做批量匹配
  list    列出流水及其匹配状态
  post    把已匹配的流水批量生成凭证
  rules   查看或新增匹配规则`)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "import":
		return bankImport(ctx, rest)
	case "match":
		return bankMatch(ctx, rest)
	case "list":
		return bankList(ctx, rest)
	case "post":
		return bankPost(ctx, rest)
	case "rules":
		return bankRules(ctx, rest)
	default:
		return fmt.Errorf("未知子命令 %q", sub)
	}
}

func bankImport(ctx context.Context, args []string) error {
	fs := newFlagSet("bank import")
	file := fs.String("file", "", "对账单文件（CSV，自动嗅探编码）（必填）")
	account := fs.String("account", "1002", "流水挂靠的银行科目编码")
	by := fs.String("by", "", "操作人")
	dryRun := fs.Bool("dry-run", false, "只解析不写库，用于先看一眼列映射对不对")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("必须指定 --file")
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("读取 %s 失败：%w", *file, err)
	}

	// 一步到位：编码嗅探 → 表格切分 → 表头探测 → 列映射 → 逐行提取
	parsed, err := bankcsv.Parse(data, bankcsv.Options{
		BankAccountCode:  *account,
		AutoDetectHeader: true,
	})
	if err != nil {
		return err
	}
	fmt.Printf("编码：%s\n", parsed.Table.Encoding)
	fmt.Printf("表头列：%v\n", parsed.Table.Header)
	// ★ 把实际用的列映射打出来。自动探测猜错时，用户看到这一行
	// 就知道该改哪里，而不是对着「金额为零或为空」发楞。
	m := parsed.Mapping
	fmt.Printf("列映射（%s）：日期=%q 摘要=%q 对方=%q 余额=%q",
		directionModeLabel(m.DirectionMode), m.Date, m.Summary,
		m.CounterpartyName, m.Balance)
	switch m.DirectionMode {
	case "two_cols":
		fmt.Printf(" 收入列=%q 支出列=%q\n", m.Credit, m.Debit)
	case "column":
		fmt.Printf(" 金额=%q 方向列=%q\n", m.Amount, m.Direction)
	default:
		fmt.Printf(" 金额=%q（正负号表示收支）\n", m.Amount)
	}
	if miss := m.Missing(); len(miss) > 0 {
		fmt.Printf("\n⚠ 有必需列没识别出来：%v\n", miss)
		fmt.Printf("  这份文件的表头是 %v\n", parsed.Table.Header)
		fmt.Printf("  请把表头改成常见列名（交易日期 / 摘要 / 收入金额 / 支出金额 …）后重试。\n")
		return nil
	}
	fmt.Println()

	// 逐行的问题要**列出来**，而不是静默丢弃。
	// 一份三个月的对账单里有几行格式异常是常态，
	// 用户需要知道是哪几行、为什么 —— 否则他会以为导入是全成功的。
	if len(parsed.Errors) > 0 {
		fmt.Printf("有 %d 行无法解析：\n", len(parsed.Errors))
		for i, e := range parsed.Errors {
			if i >= 10 {
				fmt.Printf("  …还有 %d 行\n", len(parsed.Errors)-10)
				break
			}
			fmt.Printf("  第 %d 行：%s\n", e.Line, e.Reason)
		}
		fmt.Println()
	}

	rows := parsed.Flows
	if len(rows) == 0 {
		fmt.Println("这份文件里没有解析出任何流水。")
		return nil
	}

	var in, out money.Money
	for _, r := range rows {
		if r.Direction == bank.DirIn {
			in = in.Add(r.Amount)
		} else {
			out = out.Add(r.Amount)
		}
	}
	fmt.Printf("解析到 %d 条流水：收入合计 %s，支出合计 %s\n", len(rows), in, out)
	if *dryRun {
		fmt.Println("\n（--dry-run：未写库。列映射看着不对就调 CSV 的表头）")
		return nil
	}
	if *by == "" {
		return errors.New("必须指定 --by（操作人）")
	}

	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	res, err := svc.DB().Bank().Import(ctx, sqlite.ImportInput{
		AccountCode: *account, FileName: *file, FileData: data,
		Encoding: parsed.Table.Encoding, ImportedBy: *by, Rows: rows,
	})
	if err != nil {
		return err
	}

	fmt.Printf("\n✓ 导入完成\n")
	fmt.Printf("  新增 %d 条", res.Inserted)
	if res.Duplicated > 0 {
		// ★ 去重必须说出来。静默跳过会让用户以为导入失败，
		// 而静默重复会让账目凭空多出一倍 —— 两种都不能沉默。
		fmt.Printf("，跳过 %d 条重复（同一份对账单重复导入是最常见的误操作，已在数据库层拦住）",
			res.Duplicated)
	}
	fmt.Println()
	if res.From.Valid() {
		fmt.Printf("  账务期间：%s ~ %s\n", res.From, res.To)
	}
	fmt.Printf("\n下一步：miniaccount bank match --db <账套>\n")
	return nil
}

func bankMatch(ctx context.Context, args []string) error {
	fs := newFlagSet("bank match")
	if err := fs.Parse(args); err != nil {
		return err
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	res, err := svc.DB().Bank().MatchAll(ctx, bank.StatusImported)
	if err != nil {
		return err
	}
	fmt.Printf("匹配了 %d 条流水，命中 %d 条\n\n", res.Total, res.Matched)
	if len(res.ByLayer) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "命中来源\t条数\t说明")
		for _, l := range []bank.Layer{bank.LayerRule, bank.LayerHistory, bank.LayerAI} {
			if n := res.ByLayer[l]; n > 0 {
				fmt.Fprintf(w, "%s\t%d\t%s\n", l.Label(), n, layerHint(l))
			}
		}
		_ = w.Flush()
	}
	if len(res.Unmatched) > 0 {
		fmt.Printf("\n有 %d 条没能匹配上，需要人工指定对方科目：\n", len(res.Unmatched))
		fmt.Printf("  miniaccount bank list --status imported\n")
	}
	fmt.Printf("\n匹配只是提议，不会生成凭证。确认后用 bank post 生成。\n")
	return nil
}

func layerHint(l bank.Layer) string {
	switch l {
	case bank.LayerRule:
		return "你自己定的规则，最可靠"
	case bank.LayerHistory:
		return "历史上同一对手方的记法 —— 性价比最高的一层"
	default:
		return "模型推理"
	}
}

func bankList(ctx context.Context, args []string) error {
	fs := newFlagSet("bank list")
	status := fs.String("status", "", "状态：imported | matched | posted | ignored")
	limit := fs.Int("limit", 100, "最多显示多少条")
	if err := fs.Parse(args); err != nil {
		return err
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	st := bank.Status(*status)
	from, to := farPast(), farFuture()
	flows, err := svc.DB().Bank().ListFlows(ctx, st, from, to, *limit)
	if err != nil {
		return err
	}
	if len(flows) == 0 {
		fmt.Println("没有符合条件的流水。")
		return nil
	}

	accs, err := svc.DB().Accounts().IDsByCode(ctx)
	if err != nil {
		return err
	}
	nameOf := map[int64]string{}
	for code, id := range accs {
		nameOf[id] = code
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "日期\t方向\t金额\t对方\t摘要\t状态\t建议对方科目\t依据")
	for _, f := range flows {
		sug := f.CounterAccount
		if sug == "" && f.CounterAccount != "" {
			sug = nameOf[0]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			f.TxnDate, f.Direction.Label(), yuan(f.Amount),
			truncateStr(f.CounterpartyName, 18), truncateStr(f.Summary, 22),
			f.Status.Label(), sug, f.MatchLayer.Label())
	}
	_ = w.Flush()

	stats, err := svc.DB().Bank().Stats(ctx)
	if err == nil {
		fmt.Printf("\n待匹配 %d，已匹配 %d，已生成凭证 %d，已忽略 %d\n",
			stats.Imported, stats.Matched, stats.Posted, stats.Ignored)
	}
	return nil
}

func bankPost(ctx context.Context, args []string) error {
	fs := newFlagSet("bank post")
	by := fs.String("by", "", "记账人（必填）")
	all := fs.Bool("all", false, "把全部已匹配的流水生成凭证")
	ids := fs.String("ids", "", "指定流水 id，逗号分隔")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *by == "" {
		return errors.New("必须指定 --by（记账人）")
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	var flowIDs []int64
	if *all {
		flows, err := svc.DB().Bank().ListFlows(ctx, bank.StatusMatched,
			farPast(), farFuture(), 10000)
		if err != nil {
			return err
		}
		for _, f := range flows {
			flowIDs = append(flowIDs, f.ID)
		}
	} else if *ids != "" {
		for _, s := range strings.Split(*ids, ",") {
			var id int64
			if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &id); err != nil {
				return fmt.Errorf("流水 id %q 不是数字", s)
			}
			flowIDs = append(flowIDs, id)
		}
	} else {
		return errors.New("请指定 --all 或 --ids")
	}
	if len(flowIDs) == 0 {
		fmt.Println("没有待生成凭证的流水。")
		return nil
	}

	res, err := svc.DB().Bank().PostFlows(ctx, flowIDs, *by, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("✓ 已为 %d 条流水生成凭证", res.Created)
	if res.Skipped > 0 {
		fmt.Printf("，跳过 %d 条（已生成过或有未解决的问题）", res.Skipped)
	}
	fmt.Println()
	for i, p := range res.Vouchers {
		if i >= 20 {
			fmt.Printf("  …还有 %d 条\n", len(res.Vouchers)-20)
			break
		}
		fmt.Printf("  %s  %s\n", p.No, p.Amount)
	}
	if len(res.Failures) > 0 {
		fmt.Printf("\n有 %d 条失败：\n", len(res.Failures))
		n := 0
		for id, reason := range res.Failures {
			if n >= 10 {
				fmt.Printf("  …还有 %d 条\n", len(res.Failures)-n)
				break
			}
			fmt.Printf("  流水 %d：%s\n", id, reason)
			n++
		}
	}
	return nil
}

func bankRules(ctx context.Context, args []string) error {
	fs := newFlagSet("bank rules")
	add := fs.Bool("add", false, "新增一条规则")
	name := fs.String("name", "", "规则名")
	pattern := fs.String("match", "", "匹配关键词（对摘要与对方户名做包含匹配）")
	counter := fs.String("counter", "", "对方科目编码")
	contact := fs.String("contact", "", "往来单位 id（科目要求客户/供应商/股东时必填）")
	dept := fs.Int64("dept", 0, "部门 id（科目要求部门时必填）")
	employee := fs.Int64("employee", 0, "员工 id")
	project := fs.Int64("project", 0, "项目 id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	if *add {
		if *name == "" || *pattern == "" || *counter == "" {
			return errors.New("新增规则需要同时给出 --name、--match、--counter")
		}
		rule := &bank.Rule{
			Name: *name, Pattern: *pattern, CounterAccountCode: *counter,
			// 默认匹配「摘要或对方户名」：这两个字段合起来
			// 覆盖了银行流水里几乎全部有信息量的内容。
			MatchField: bank.MatchBoth,
			Enabled:    true, Priority: 100,
		}
		// ★ 四维辅助核算都要能配。
		//
		// 一条「房租 → 管理费用—租赁费」的规则若不能带「部门」，
		// 匹配会成功，生成凭证时却被「缺少必需的辅助核算」拦下 ——
		// 用户看到「命中 2 条」却一条也记不上，而且看不出为什么。
		if *contact != "" {
			id, err := parseID(*contact)
			if err != nil {
				return fmt.Errorf("--contact %q 不是数字", *contact)
			}
			rule.ContactID = &id
		}
		if *dept != 0 {
			v := *dept
			rule.DeptID = &v
		}
		if *employee != 0 {
			v := *employee
			rule.EmployeeID = &v
		}
		if *project != 0 {
			v := *project
			rule.ProjectID = &v
		}

		id, err := svc.DB().Bank().UpsertRule(ctx, rule)
		if err != nil {
			return err
		}
		aux := describeRuleAux(rule)
		fmt.Printf("✓ 规则已保存（id=%d）：摘要或对方户名含「%s」→ 对方科目 %s%s\n",
			id, *pattern, *counter, aux)
		return nil
	}

	rules, err := svc.DB().Bank().ListRules(ctx)
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		fmt.Println("还没有匹配规则。")
		fmt.Println("\n规则是三层匹配里最可靠的一层：它由你定义，不受模型影响。")
		fmt.Println("示例：")
		fmt.Println("  miniaccount bank rules --add --name 房租 \\")
		fmt.Println("      --match 房租 --counter 560210")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\t规则名\t匹配\t对方科目\t辅助核算\t命中次数\t启用")
	for _, r := range rules {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%d\t%s\n",
			r.ID, r.Name, r.Pattern, r.CounterAccountCode,
			strings.TrimSpace(describeRuleAux(r)), r.HitCount, yesNo(r.Enabled))
	}
	return w.Flush()
}

// farPast / farFuture 用于「不限日期」的查询。
//
// 用哨兵日期而不是给 ListFlows 加一个可空参数：
// 命令行查流水时想看的本来就是「全部」，硬要用户先算日期区间是多余的。
// describeRuleAux 把规则带的辅助核算渲染成一句话。
func describeRuleAux(r *bank.Rule) string {
	var parts []string
	if r.ContactID != nil {
		parts = append(parts, fmt.Sprintf("往来#%d", *r.ContactID))
	}
	if r.EmployeeID != nil {
		parts = append(parts, fmt.Sprintf("员工#%d", *r.EmployeeID))
	}
	if r.DeptID != nil {
		parts = append(parts, fmt.Sprintf("部门#%d", *r.DeptID))
	}
	if r.ProjectID != nil {
		parts = append(parts, fmt.Sprintf("项目#%d", *r.ProjectID))
	}
	if len(parts) == 0 {
		return ""
	}
	return "  [" + strings.Join(parts, " ") + "]"
}

// parseID 把命令行传来的 id 字符串解析成 int64。
func parseID(s string) (int64, error) {
	var id int64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &id); err != nil {
		return 0, err
	}
	return id, nil
}

func directionModeLabel(m bankcsv.DirectionMode) string {
	switch m {
	case bankcsv.DirModeTwoCols:
		return "收入/支出两列"
	case bankcsv.DirModeColumn:
		return "金额列 + 方向列"
	case bankcsv.DirModeSign:
		return "带符号金额"
	default:
		return string(m)
	}
}

func farPast() calendar.Date   { d, _ := calendar.New(1900, 1, 1); return d }
func farFuture() calendar.Date { d, _ := calendar.New(2999, 12, 31); return d }

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
