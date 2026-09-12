// Command miniaccount 是这款记账软件的命令行入口。
//
// # 为什么先做 CLI
//
// 图形界面（Wails + Vue3）还在建设中，但账务内核已经完整。
// CLI 的价值不只是「临时凑合」：
//
//   - 它证明整条链路真的能跑通 —— 建账 → 记账 → 报表 → 结账 → 备份 → 恢复，
//     而不是一堆各自绿着的单元测试
//   - 它让**每个人都验证得了**：不用装 WebView2、不用起前端 dev server，
//     一条 `miniaccount demo` 就能看到一张真实的小企业账
//   - 它是脚本化对账与回归测试的天然入口
//
// GUI 上线后 CLI 依然保留：批量导入、定时备份、账套巡检这些场景，
// 命令行比点鼠标合适得多。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/export"
	"miniaccount/internal/service"
	"miniaccount/internal/store/sqlite"
)

const usage = `miniaccount —— 面向中国小微企业的记账软件

用法：
  miniaccount <命令> [参数]

其他：
  version                 查看版本号与构建时间

账套：
  init       建账（预置小企业会计准则科目表与会计期间）
  demo       生成一个演示账套（含示例业务，可直接看报表）
  info       显示账套信息与各期间状态

报表：
  trial      科目余额表
  bs         资产负债表
  pl         利润表
  cashflow   现金流量表
  ledger     明细账（需 --account）
  aging      账龄分析表（应收/应付按 30/60/90/180/365 天分桶）
  statement  往来对账单（发给客户核对盖章用）
  recon      银行存款余额调节表（企业账与银行账对不上时查原因）
  columnar   多栏式明细账（按下级科目横向展开，如管理费用各项花销）
  summary    凭证汇总表（按凭证字/日期/科目统计张数与借贷合计）
  schemes    社保方案（工资模块的前置：不配方案算不出社保）
  vat        增值税税率政策与纳税人身份（政策是数据，带生效期）

银行流水：
  bank import   导入对账单 CSV（自动嗅探编码与列映射）
  bank match    三层匹配（规则 → 历史 → AI）
  bank list     查看流水与匹配结果
  bank post     批量生成凭证
  bank rules    查看 / 新增匹配规则

期末：
  health     结账前体检
  close      结账（--period 2025-09 --by 王主管）
  reopen     反结账

输出：
  export     导出 Excel（--report bs|pl|trial --out 文件）
  backup     备份为单个 .mabak 文件
  restore    从 .mabak 备份恢复账套（先看清单，再恢复）
  inspect    查看备份内容（不恢复）
  log        操作日志：查询、校验完整性、导出（会计监督用）
  bookkeeper 查看/设置本账套的记账人（录凭证、过账、结账时默认填它）

AI：
  ai         请求 AI 生成记账建议
  ai-config  查看/设置模型服务

通用参数：
  --db <路径>   账套文件（默认 miniaccount.db）

运行 ` + "`miniaccount <命令> -h`" + ` 查看该命令的参数。
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	ctx := context.Background()

	// ★ 日志目录在**所有命令**之前设好。
	//
	// 原来只在 openBook 里设，而 init / demo 这些自己开账套的命令
	// 不走那条路 —— 结果「建账」这个最该留痕的操作一条日志都没有，
	// 而且因为日志写失败不阻断业务，表现为「静默没日志」。
	if dir, derr := service.DefaultAuditDir(); derr == nil {
		service.SetAuditDir(dir)
		// 命令行不会主动打开日志存储（读操作可能一次都不写日志），
		// 所以这里先把权限收紧一次，再检查。
		service.EnsureAuditDir(dir)
	}
	defer service.CloseAudit()

	var err error
	switch cmd {
	case "init":
		err = cmdInit(ctx, args)
	case "demo":
		err = cmdDemo(ctx, args)
	case "info":
		err = cmdInfo(ctx, args)
	case "trial":
		err = cmdTrial(ctx, args)
	case "bs":
		err = cmdBS(ctx, args)
	case "pl":
		err = cmdPL(ctx, args)
	case "cashflow", "cf":
		err = cmdCashFlow(ctx, args)
	case "ledger":
		err = cmdLedger(ctx, args)
	case "aging":
		err = cmdAging(ctx, args)
	case "statement":
		err = cmdStatement(ctx, args)
	case "reconciliation", "recon":
		err = cmdReconciliation(ctx, args)
	case "columnar", "col":
		err = cmdColumnar(ctx, args)
	case "summary", "sum":
		err = cmdSummary(ctx, args)
	case "schemes":
		err = cmdSchemes(ctx, args)
	case "vat":
		err = cmdVAT(ctx, args)
	case "bank":
		err = cmdBank(ctx, args)
	case "health":
		err = cmdHealth(ctx, args)
	case "close":
		err = cmdClose(ctx, args)
	case "reopen":
		err = cmdReopen(ctx, args)
	case "export":
		err = cmdExport(ctx, args)
	case "backup":
		err = cmdBackup(ctx, args)
	case "inspect":
		err = cmdInspect(args)
	case "restore":
		err = cmdRestore(ctx, args)
	case "log":
		err = cmdLog(ctx, args)
	case "bookkeeper":
		err = cmdBookkeeper(ctx, args)
	case "ai":
		err = cmdAI(ctx, args)
	case "ai-config":
		err = cmdAIConfig(ctx, args)
	case "version", "-v", "--version":
		cmdVersion()
		return
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误：%v\n", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// 通用
// ---------------------------------------------------------------------------

// openBook 按 --db 打开账套。
func openBook(ctx context.Context, fs *flag.FlagSet) (*service.Service, error) {
	db := fs.Lookup("db")
	if db == nil {
		return nil, errors.New("内部错误：未注册 --db")
	}
	svc, err := service.Open(ctx, service.Options{Path: db.Value.String()})
	if err != nil {
		return nil, err
	}
	// 建账之前的空文件是合法的中间状态，提示用户而不是报错
	if _, err := svc.Book(ctx); err != nil {
		_ = svc.Shutdown()
		if errors.Is(err, service.ErrNoBook) {
			return nil, fmt.Errorf("%w：请先运行 `miniaccount init --db %s`",
				service.ErrNoBook, db.Value.String())
		}
		return nil, err
	}
	// --by 没传时用**本账套**的记账人（界面「设置」里设的那个）。
	//
	// 命令行上每次都要打一遍 --by 李会计 是没道理的，而这个人
	// 恰恰是记账签章 —— 打错一个字，凭证上的签章就与日志对不上了。
	// 必须在账套打开之后才问得到，所以放在这里。
	if by := fs.Lookup("by"); by != nil && strings.TrimSpace(by.Value.String()) == "" {
		if name, berr := svc.Bookkeeper(ctx); berr == nil && name != "" {
			_ = by.Value.Set(name)
		}
	}
	return svc, nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.String("db", "miniaccount.db", "账套数据库文件路径")
	return fs
}

// parsePeriod 解析 --period，格式 2025-09。
// byFromBook 取本账套的记账人。
//
// 单独一个函数是因为它必须在**账套打开之后**才能问 ——
// openBook 里要先把 svc 建出来。
func byFromBook(ctx context.Context, svc *service.Service) (string, error) {
	if svc == nil {
		return "", nil
	}
	return svc.Bookkeeper(ctx)
}

func parsePeriod(s string, required bool) (period.Key, error) {
	if s == "" {
		if required {
			return period.Key{}, errors.New("必须指定 --period，格式如 2025-09")
		}
		now := calendar.Today()
		k := period.NewKey(now.Year, now.Month)
		// ★ 默认期间要把「我替你选了哪一期」说出来。
		//
		// 账套里的数据往往不在今天所在的期间（演示账套在 2025 年），
		// 默认取当期会得到一张空表 —— 而空表上的「✓ 试算平衡」
		// 是一句**假保证**：底账可能根本不空，只是没看对期间。
		// 提示写 stderr，不污染可以重定向到文件的报表正文。
		fmt.Fprintf(os.Stderr,
			"（未指定 --period，按今天所在的 %s 取数）\n", k)
		return k, nil
	}
	var y, m int
	if _, err := fmt.Sscanf(s, "%d-%d", &y, &m); err != nil {
		return period.Key{}, fmt.Errorf("--period %q 格式不对，应为 2025-09", s)
	}
	k := period.NewKey(y, m)
	if !k.Valid() {
		return period.Key{}, fmt.Errorf("--period %q 不是合法会计期间", s)
	}
	return k, nil
}

func yuan(m money.Money) string { return m.String() }

// ---------------------------------------------------------------------------
// 账套
// ---------------------------------------------------------------------------

// cmdVersion 打印版本号与构建时间。
//
// ★ 这个命令是为了解决一个真实发生过的困惑：用户贴回来的命令行输出
// 来自旧二进制（科目数还是 190、只有一行纳税人身份），
// 而当时无法从输出里看出这一点 —— 只能靠人肉比对科目数才发现。
func cmdVersion() {
	fmt.Printf("miniaccount %s\n", service.AppVersion)
	if service.BuildStamp != "" {
		fmt.Printf("构建时间：%s\n", service.BuildStamp)
	}
	fmt.Println("数据库：SQLite（单文件）· 备份格式 .mabak")
	fmt.Println("★ 报告问题请连同本行版本号一起提供 ——")
	fmt.Println("  版本号对不上，看到的输出可能是旧程序产生的。")
}

func cmdInit(ctx context.Context, args []string) error {
	fs := newFlagSet("init")
	name := fs.String("name", "", "单位名称（必填）")
	code := fs.String("code", "", "统一社会信用代码")
	person := fs.String("person", "", "法定代表人")
	// ★ 两个**不同**的分类，必须分别指定。
	//
	//	--vat    增值税纳税人身份（流转税）：决定按一般计税还是简易计税、
	//	         能不能抵扣进项税额
	//	--scale  企业规模类型（所得税与统计）：决定能不能享受小型微利企业
	//	         所得税优惠
	//
	// 两者互不派生：小微企业可以自愿登记为一般纳税人；
	// 年销售额未超 500 万元通常按小规模纳税，但登记为一般纳税人同样合法。
	// 用「是不是小微」去推断「能不能抵扣进项」是这类软件最常见的算错税原因。
	// ★ 默认小规模纳税人：这款软件面向小微企业，绝大多数是小规模。
	// 这只是一个**默认值**，不是推导 —— 身份由税务登记决定，
	// 程序不按销售额自动猜（那正是这类软件算错税的常见原因）。
	vatStatus := fs.String("vat", "small_scale",
		"增值税纳税人身份：small_scale 小规模纳税人（默认）| general 一般纳税人")
	scale := fs.String("scale", "",
		"企业规模类型：micro 微型 | small 小型 | medium 中型 | large 大型（可留空）")
	vatFrom := fs.String("vat-from", "", "纳税人身份生效日 YYYY-MM-DD（默认取启用日）")
	// --tax 是历史参数，保留以免已有脚本失败；值与 --vat 等价（small → 小规模）
	tax := fs.String("tax", "", "（已废弃，请用 --vat）")
	start := fs.String("start", "", "启用期间，格式 2025-01（默认当年 1 月）")
	current := fs.String("current", "", "当前期间，格式 2025-09（默认当年当月）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("必须指定 --name（单位名称）")
	}

	startKey, err := resolveStartPeriod(*start, *current)
	if err != nil {
		return err
	}
	currentKey, err := parsePeriodOrYear(*current, "current")
	if err != nil {
		return err
	}
	if currentKey.Before(startKey) {
		return fmt.Errorf(
			"当前期间 %s 早于启用期间 %s —— 这样建出来的账套里，"+
				"所有早于启用期间的凭证都会被拒绝", currentKey, startKey)
	}

	svc, err := service.Open(ctx, service.Options{Path: fs.Lookup("db").Value.String()})
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	// ★ 谁说了算：--vat > --tax > 默认值。
	//
	// 这里必须区分「用户没传 --vat」与「用户传了 --vat=small_scale」——
	// 默认值是 small_scale 之后，老脚本里的 `--tax general`
	// 会被默认值盖住，把本该是一般纳税人的账套建错。
	// 建错身份的后果是进项能不能抵扣算反，而且事后很难发现。
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	if !explicit["vat"] && explicit["tax"] {
		switch strings.TrimSpace(*tax) {
		case "small", "small_scale":
			*vatStatus = "small_scale"
		case "general":
			*vatStatus = "general"
		}
	}
	// --vat 接受 small 作为 small_scale 的别名 —— 帮助文本里写的就是
	// 「small | general」，让用户照着写却报错是没有道理的。
	if *vatStatus == "small" {
		*vatStatus = "small_scale"
	}

	// --tax 保留只是为了让已有脚本不至于直接报「未知参数」。
	if explicit["tax"] {
		fmt.Fprintln(os.Stderr,
			"提示：--tax 已废弃，请改用 --vat（增值税纳税人身份）。"+
				"企业规模类型是另一个参数 --scale。")
	}

	info, err := svc.CreateBook(ctx, service.CreateBookInput{
		CompanyName: *name, CreditCode: *code, LegalPerson: *person,
		EnterpriseScale:        *scale,
		VATStatus:              *vatStatus,
		VATStatusEffectiveFrom: *vatFrom,
		StartYear:              startKey.Year, StartMonth: startKey.Month,
		ThroughYear: startKey.Year,
		CurrentYear: currentKey.Year, CurrentMonth: currentKey.Month,
	})
	if err != nil {
		if errors.Is(err, sqlite.ErrBookExists) {
			return fmt.Errorf("%s 里已经有一个账套了", fs.Lookup("db").Value.String())
		}
		return err
	}

	fmt.Printf("✓ 账套建好了：%s\n", info.CompanyName)
	// 两套身份分开打印，别让用户以为是一回事
	fmt.Printf("  增值税纳税人身份：%s（生效日 %s）—— %s\n",
		info.VATStatusLabel, info.VATStatusEffectiveFrom,
		map[bool]string{true: "可以抵扣进项税额",
			false: "不得抵扣进项税额，取得专票也应计入成本"}[info.CanDeductInputVAT])
	if info.EnterpriseScale == "" {
		fmt.Println("  企业规模类型：未填写（可在账套信息里补填 —— " +
			"它决定能否享受小型微利企业所得税优惠，与增值税无关）")
	} else {
		fmt.Printf("  企业规模类型：%s（所得税与统计口径，与增值税无关）\n",
			info.EnterpriseScaleLabel)
	}
	fmt.Printf("  启用期间：%s\n", info.StartPeriod)
	// 科目数从库里数，不写死：科目表加一个科目，
	// 写死的数字就成了错误信息，而且出现在建账成功的提示里。
	if accts, aerr := svc.DB().Accounts().List(ctx); aerr == nil {
		fmt.Printf("  科目表：小企业会计准则（%d 个科目）\n", len(accts))
	}
	fmt.Printf("  会计期间：%d 个\n", len(info.Periods))
	for _, p := range info.Periods {
		if p.Status == string(period.StatusOpen) {
			fmt.Printf("  当前可记账期间：%s\n", p.Label)
			break
		}
	}
	fmt.Printf("\n账套文件：%s\n", fs.Lookup("db").Value.String())
	return nil
}

// parsePeriodOrYear 解析期间；为空时按当前日期推断。
func parsePeriodOrYear(s, label string) (period.Key, error) {
	if s == "" {
		now := calendar.Today()
		if label == "start" {
			return period.NewKey(now.Year, 1), nil
		}
		return period.NewKey(now.Year, now.Month), nil
	}
	return parsePeriod(s, true)
}

// resolveStartPeriod 决定启用期间。
//
// ★ 单独一个函数，是因为这里有一个容易踩的坑：
//
//	miniaccount init --current 2025-03
//
// 「当前期间」给了 2025-03，而「启用期间」没给，若各自按默认值取
// （start = 今天所在年度的 1 月 = 2026-01），就会建出一个
// **当前期间早于启用期间**的账套 —— 所有 2025 年的凭证都会被
// 「早于账套启用期间」拒绝，而用户完全看不出为什么。
//
// 因此：只要给了当前期间，启用期间默认就取**同一年的 1 月**。
func resolveStartPeriod(start, current string) (period.Key, error) {
	if start != "" {
		return parsePeriod(start, true)
	}
	if current != "" {
		ck, err := parsePeriod(current, true)
		if err != nil {
			return period.Key{}, err
		}
		return period.NewKey(ck.Year, 1), nil
	}
	now := calendar.Today()
	return period.NewKey(now.Year, 1), nil
}

func cmdInfo(ctx context.Context, args []string) error {
	fs := newFlagSet("info")
	if err := fs.Parse(args); err != nil {
		return err
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	book, err := svc.Book(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("单位名称：%s\n", book.CompanyName)
	if book.CreditCode != "" {
		fmt.Printf("信用代码：%s\n", book.CreditCode)
	}
	if book.LegalPerson != "" {
		fmt.Printf("法定代表人：%s\n", book.LegalPerson)
	}
	// 记账人是**账套级**的（存在账套的 setting 表里）——
	// 显示出来，用户才能一眼确认「这本账签的是谁」。
	if name, kerr := svc.Bookkeeper(ctx); kerr == nil {
		if name == "" {
			fmt.Println("记账人：未设置（用 `miniaccount bookkeeper --set 姓名` 或界面「设置」里填）")
		} else {
			fmt.Printf("记账人：%s\n", name)
		}
	}
	fmt.Printf("纳税人身份：%s\n", book.TaxTypeLabel)
	fmt.Printf("启用期间：%s\n", book.StartPeriod)
	fmt.Printf("账套文件：%s\n\n", svc.Path())

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "期间\t起止日期\t状态\t凭证数\t可结账\t可反结账")
	for _, p := range book.Periods {
		fmt.Fprintf(w, "%s\t%s ~ %s\t%s\t%d\t%s\t%s\n",
			p.Label, p.From, p.To, p.StatusLabel, p.VoucherCount,
			yesNo(p.CanClose), yesNo(p.CanReopen))
	}
	return w.Flush()
}

func yesNo(b bool) string {
	if b {
		return "✓"
	}
	return ""
}

// ---------------------------------------------------------------------------
// 报表
// ---------------------------------------------------------------------------

func cmdTrial(ctx context.Context, args []string) error {
	fs := newFlagSet("trial")
	ps := fs.String("period", "", "会计期间，格式 2025-09")
	only := fs.Bool("nonzero", true, "只显示有发生额或余额的科目")
	if err := fs.Parse(args); err != nil {
		return err
	}
	k, err := parsePeriod(*ps, false)
	if err != nil {
		return err
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	rep, err := svc.DB().Reports().TrialBalanceReport(ctx, k)
	if err != nil {
		return err
	}
	fmt.Printf("科目余额表  %s\n\n", k)

	// 六栏式：借贷分列而不是用正负数 —— 会计人员看的是
	// 「这个科目挂在哪个方向」，带符号的数字反而要心算。
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "科目编码\t科目名称\t期初借方\t期初贷方\t本期借方\t本期贷方\t期末借方\t期末贷方")
	for _, r := range rep.Rows {
		if *only && r.OpeningDebit.IsZero() && r.OpeningCredit.IsZero() &&
			r.PeriodDebit.IsZero() && r.PeriodCredit.IsZero() &&
			r.ClosingDebit.IsZero() && r.ClosingCredit.IsZero() {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.AccountCode, r.AccountName,
			blankIfZero(r.OpeningDebit), blankIfZero(r.OpeningCredit),
			blankIfZero(r.PeriodDebit), blankIfZero(r.PeriodCredit),
			blankIfZero(r.ClosingDebit), blankIfZero(r.ClosingCredit))
	}
	// ★ 合计必须用 rep.Totals()，不能自己把可见行加一遍。
	//
	// 汇总科目行带的是其下级的合计，全加会把同一笔钱算上两三遍，
	// 于是几乎每个月都误报「试算不平衡」—— 底账其实是平的。
	// 这种误报最坏的地方不是难看，而是**训练用户忽略这个警告**：
	// 等到真不平衡时，没人会再看它。
	_, _, td, tc, _, _ := rep.Totals()
	fmt.Fprintf(w, "\t合计\t\t\t%s\t%s\t\t\n", yuan(td), yuan(tc))
	if err := w.Flush(); err != nil {
		return err
	}
	if td != tc {
		fmt.Printf("\n⚠ 试算不平衡：借方 %s ≠ 贷方 %s，差额 %s\n",
			td, tc, td.Sub(tc))
	} else if td.IsZero() {
		// ★ 空期间上的「试算平衡」没有信息量，而且容易被当成
		// 「账没问题」。如实说：这一期本来就没有凭证。
		fmt.Printf("\n%s 没有任何凭证，所以这张表是空的 —— "+
			"「平」在这里不代表账做对了。\n", k)
		fmt.Printf("请确认期间是否正确（本账套的启用期间见 miniaccount info）。\n")
	} else {
		fmt.Printf("\n✓ 试算平衡：借 %s = 贷 %s\n", td, tc)
	}
	return nil
}

func cmdBS(ctx context.Context, args []string) error {
	fs := newFlagSet("bs")
	asOf := fs.String("date", "", "截止日期 YYYY-MM-DD（默认账套最后一天）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	d, err := resolveDate(ctx, svc, *asOf)
	if err != nil {
		return err
	}
	def, _, issues, err := svc.DB().Reports().BuildBalanceSheet(ctx, d)
	if err != nil {
		return err
	}
	printStatement(def, "资产负债表", d.String(), true)
	return reportIssues(issues)
}

func cmdPL(ctx context.Context, args []string) error {
	fs := newFlagSet("pl")
	ps := fs.String("period", "", "会计期间，格式 2025-09")
	ytd := fs.Bool("ytd", false, "显示本年累计列")
	if err := fs.Parse(args); err != nil {
		return err
	}
	k, err := parsePeriod(*ps, false)
	if err != nil {
		return err
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	cur, acc, issues, err := svc.DB().Reports().BuildIncomeStatement(ctx, k)
	if err != nil {
		return err
	}
	if *ytd {
		printStatement2(defLines(cur), defLines(acc), "利润表", k.String(), "本期金额", "本年累计")
	} else {
		printStatement(cur, "利润表", k.String(), true)
	}
	return reportIssues(issues)
}

func cmdCashFlow(ctx context.Context, args []string) error {
	fs := newFlagSet("cashflow")
	ps := fs.String("period", "", "会计期间，格式 2025-09")
	if err := fs.Parse(args); err != nil {
		return err
	}
	k, err := parsePeriod(*ps, false)
	if err != nil {
		return err
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	st, err := svc.DB().CashFlow().StatementForPeriod(ctx, k, nil)
	if err != nil {
		return err
	}
	fmt.Printf("现金流量表  %s\n\n", k)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "行次\t项目\t金额")
	lastActivity := ""
	for _, l := range st.Lines {
		if l.Activity != "" && string(l.Activity) != lastActivity {
			fmt.Fprintf(w, "\t【%s】\t\n", l.Activity.Label())
			lastActivity = string(l.Activity)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\n", l.No, l.Name, yuan(l.Display()))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Printf("\n期初现金：%s\n", yuan(st.OpeningCash))
	fmt.Printf("期末现金：%s\n", yuan(st.ClosingCash))
	if st.ActualClosingCash != nil {
		if d := st.CashMismatch(); !d.IsZero() {
			fmt.Printf("⚠ 与账面期末现金 %s 差 %s —— 可能有现金科目没被纳入\n",
				yuan(*st.ActualClosingCash), yuan(d))
		} else {
			fmt.Printf("✓ 与账面期末现金一致\n")
		}
	}
	if len(st.Unclassified) > 0 {
		fmt.Printf("\n⚠ 有 %d 笔未能归类（合计 %s），现金流量表不完全准确：\n",
			len(st.Unclassified), yuan(st.UnclassifiedTotal))
		for i, u := range st.Unclassified {
			if i >= 10 {
				fmt.Printf("  …还有 %d 笔\n", len(st.Unclassified)-10)
				break
			}
			fmt.Printf("  %s  %s  %s  %s\n",
				u.Date, u.VoucherNo, yuan(u.Amount), u.Summary)
		}
	}
	for _, e := range st.Check() {
		fmt.Printf("⚠ 勾稽不成立：%v\n", e)
	}
	return nil
}

func cmdLedger(ctx context.Context, args []string) error {
	fs := newFlagSet("ledger")
	code := fs.String("account", "", "科目编码前缀，如 1002（必填）")
	from := fs.String("from", "", "起始日期 YYYY-MM-DD")
	to := fs.String("to", "", "截止日期 YYYY-MM-DD")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *code == "" {
		return errors.New("必须指定 --account，如 --account 1002")
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	dFrom, dTo, err := resolveRange(ctx, svc, *from, *to)
	if err != nil {
		return err
	}
	rows, err := svc.DB().Vouchers().Detail(ctx, *code, dFrom, dTo)
	if err != nil {
		return err
	}
	fmt.Printf("明细账  科目前缀 %s  %s ~ %s\n\n", *code, dFrom, dTo)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	// 「对方科目」是日记账最要紧的一列：看银行存款日记账时真正要知道的是
	// 「这笔钱从哪来、到哪去」，只给借贷金额不够。
	fmt.Fprintln(w, "日期\t凭证号\t摘要\t对方科目\t借方\t贷方\t方向\t余额")
	var balance money.Money
	for _, r := range rows {
		balance = balance.Add(r.Debit).Sub(r.Credit)
		dir := "借"
		if balance.IsNegative() {
			dir = "贷"
		}
		contra := strings.Join(r.ContraAccounts, "、")
		if contra == "" {
			contra = "—"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.BizDate, r.VoucherNo, truncate(r.Summary, 16), truncate(contra, 20),
			blankIfZero(r.Debit), blankIfZero(r.Credit), dir, yuan(balance.Abs()))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Printf("\n期末余额：%s\n", yuan(balance))
	return nil
}

func blankIfZero(m money.Money) string {
	if m.IsZero() {
		return ""
	}
	return yuan(m)
}

// ---------------------------------------------------------------------------
// 期末
// ---------------------------------------------------------------------------

func cmdHealth(ctx context.Context, args []string) error {
	fs := newFlagSet("health")
	ps := fs.String("period", "", "会计期间，格式 2025-09")
	if err := fs.Parse(args); err != nil {
		return err
	}
	k, err := parsePeriod(*ps, false)
	if err != nil {
		return err
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	h, err := svc.CheckHealth(ctx, k)
	if err != nil {
		return err
	}
	fmt.Printf("结账前体检  %s\n\n", h.Period)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "结果\t检查项\t说明")
	for _, it := range h.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\n", levelMark(it.Level), it.Title, it.Detail)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Printf("\n%s\n", h.Summary)
	if !h.CanClose {
		return errors.New("存在阻断项，不能结账")
	}
	return nil
}

func levelMark(level string) string {
	switch level {
	case "ok":
		return "✓"
	case "warn":
		return "!"
	default:
		return "✗"
	}
}

func cmdClose(ctx context.Context, args []string) error {
	fs := newFlagSet("close")
	ps := fs.String("period", "", "会计期间，格式 2025-09（必填）")
	by := fs.String("by", "", "结账人（必填）")
	preview := fs.Bool("preview", false, "只预览，不写入")
	if err := fs.Parse(args); err != nil {
		return err
	}
	k, err := parsePeriod(*ps, true)
	if err != nil {
		return err
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	pv, err := svc.PreviewClose(ctx, k)
	if err != nil {
		return err
	}
	fmt.Printf("结账预览  %s\n\n", pv.Period)
	for _, st := range pv.Steps {
		mark := "✓"
		switch {
		case st.Skipped:
			mark = "—"
		case !st.Done:
			mark = "…"
		}
		fmt.Printf("  %s %s：%s\n", mark, st.Title, st.Detail)
	}
	if len(pv.Entries) > 0 {
		fmt.Printf("\n将写入的结转分录：\n")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  科目\t摘要\t借方\t贷方\t辅助核算")
		for _, e := range pv.Entries {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n",
				e.AccountCode, e.Summary, blankIfZero(e.Debit),
				blankIfZero(e.Credit), e.AuxDesc)
		}
		_ = w.Flush()
	}
	if pv.Health != nil {
		fmt.Printf("\n%s\n", pv.Health.Summary)
		if !pv.Health.CanClose {
			return errors.New("体检未通过，不能结账")
		}
		for _, it := range pv.Health.Warnings {
			fmt.Printf("  ! %s：%s\n", it.Title, it.Detail)
		}
	}

	if *preview {
		fmt.Printf("\n（预览模式，未写入任何数据）\n")
		return nil
	}
	if *by == "" {
		return errors.New("必须指定 --by（结账人），记账凭证需要有记账签章")
	}

	res, err := svc.Close(ctx, k, *by)
	if err != nil {
		return err
	}
	fmt.Printf("\n✓ %s 已结账\n", res.Period)
	if res.VoucherCreated {
		fmt.Printf("  结转凭证：%s\n", res.VoucherNo)
	} else {
		fmt.Printf("  本期无损益，未生成结转凭证\n")
	}
	fmt.Printf("  %s\n", res.Summary)
	return nil
}

func cmdReopen(ctx context.Context, args []string) error {
	fs := newFlagSet("reopen")
	ps := fs.String("period", "", "会计期间，格式 2025-09（必填）")
	by := fs.String("by", "", "操作人（必填）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	k, err := parsePeriod(*ps, true)
	if err != nil {
		return err
	}
	if *by == "" {
		return errors.New("必须指定 --by（操作人）")
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	res, err := svc.Reopen(ctx, k, *by)
	if err != nil {
		return err
	}
	fmt.Printf("✓ %s 已反结账\n", res.Period)
	if len(res.Reversed) == 0 {
		fmt.Printf("  该期没有结转凭证需要冲销\n")
		return nil
	}
	fmt.Printf("  已红字冲销：%s\n", strings.Join(res.Reversed, "、"))
	fmt.Printf("  冲销凭证 id：%v\n", res.VoucherIDs)
	fmt.Printf("\n注意：结转凭证没有被删除，而是用红字凭证冲销 ——\n")
	fmt.Printf("      凭证号不断号，账务轨迹可追溯。\n")
	return nil
}

// ---------------------------------------------------------------------------
// 输出
// ---------------------------------------------------------------------------

func cmdExport(ctx context.Context, args []string) error {
	fs := newFlagSet("export")
	report := fs.String("report", "", "报表类型：bs | pl | trial | cashflow（必填）")
	out := fs.String("out", "", "输出文件（必填）")
	ps := fs.String("period", "", "会计期间，格式 2025-09")
	date := fs.String("date", "", "截止日期 YYYY-MM-DD（资产负债表用）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *report == "" || *out == "" {
		return errors.New("必须指定 --report 与 --out")
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	book, err := svc.Book(ctx)
	if err != nil {
		return err
	}

	switch *report {
	case "bs":
		d, err := resolveDate(ctx, svc, *date)
		if err != nil {
			return err
		}
		def, opening, issues, err := svc.DB().Reports().BuildBalanceSheet(ctx, d)
		if err != nil {
			return err
		}
		at := time.Date(d.Year, time.Month(d.Month), d.Day, 0, 0, 0, 0, time.UTC)
		// 勾稽问题一并写进导出文件 —— 发给别人的那份不能看起来比屏幕上更干净
		if err := export.BalanceSheet(*out, book.CompanyName, at, def, opening, issues); err != nil {
			return err
		}
		if len(issues) > 0 {
			fmt.Printf("  ⚠ 已写入 %d 条勾稽问题（文件里附在同一工作表下方）\n", len(issues))
		}
	case "pl":
		k, err := parsePeriod(*ps, false)
		if err != nil {
			return err
		}
		cur, ytd, issues, err := svc.DB().Reports().BuildIncomeStatement(ctx, k)
		if err != nil {
			return err
		}
		if err := export.IncomeStatement(*out, book.CompanyName, k, cur, ytd, issues); err != nil {
			return err
		}
		if len(issues) > 0 {
			fmt.Printf("  ⚠ 已写入 %d 条勾稽问题（文件里附在同一工作表下方）\n", len(issues))
		}
	case "trial":
		k, err := parsePeriod(*ps, false)
		if err != nil {
			return err
		}
		rep, err := svc.DB().Reports().TrialBalanceReport(ctx, k)
		if err != nil {
			return err
		}
		if err := export.TrialBalance(*out, book.CompanyName,
			k.Year, k.Month, rep); err != nil {
			return err
		}
	case "cashflow":
		k, err := parsePeriod(*ps, false)
		if err != nil {
			return err
		}
		st, err := svc.DB().CashFlow().StatementForPeriod(ctx, k, nil)
		if err != nil {
			return err
		}
		if err := exportCashFlow(*out, book.CompanyName, k, st); err != nil {
			return err
		}
	default:
		return fmt.Errorf("不支持的报表类型 %q（可选：bs | pl | trial | cashflow）", *report)
	}
	fmt.Printf("✓ 已导出：%s\n", *out)
	return nil
}

func cmdBackup(ctx context.Context, args []string) error {
	fs := newFlagSet("backup")
	out := fs.String("out", "", "输出文件（默认 <账套名>-<时间>.mabak）")
	noFiles := fs.Bool("no-files", false, "不包含附件（只备份数据库）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	dest := *out
	if dest == "" {
		base := strings.TrimSuffix(svc.Path(), filepath.Ext(svc.Path()))
		dest = fmt.Sprintf("%s-%s.mabak", base, time.Now().Format("20060102-150405"))
	}
	start := time.Now()
	m, err := svc.Backup(ctx, service.BackupOptions{
		Dest: dest, IncludeFiles: !*noFiles,
	})
	if err != nil {
		return err
	}
	fmt.Printf("✓ 备份完成：%s（耗时 %s）\n\n", dest, time.Since(start).Round(time.Millisecond))
	fmt.Printf("  单位名称：%s\n", m.CompanyName)
	fmt.Printf("  数据库：%s（sha256 %s…）\n", humanBytes(m.DBSize), m.DBSHA256[:16])
	fmt.Printf("  附件：%d 个，%s\n", m.FileCount, humanBytes(m.FileBytes))
	fmt.Printf("  凭证数：%d    科目数：%d\n", m.VoucherCount, m.AccountCount)
	if m.PeriodFrom != "" {
		fmt.Printf("  账务期间：%s ~ %s\n", m.PeriodFrom, m.PeriodTo)
	}
	fmt.Printf("\n备份是一个文件，直接拷走即可完整恢复。\n")
	return nil
}

// cmdRestore 从 .mabak 备份恢复账套。
//
// ★ 命令行也要能恢复。
//
// 桌面版有恢复界面，但「需要恢复」的场景恰恰包括「程序起不来」——
// 界面版恢复不了。备份的价值有一半在恢复路径上，
// 只把恢复放在界面里，等于在最需要它的时刻把它锁起来。
//
// 恢复的破坏性由 backup.Restore 内部处理：先把原账套整体挪到带时间戳的
// 暂存目录，出错回滚，成功后暂存目录**保留**（恢复错了备份时唯一的退路）。
func cmdRestore(ctx context.Context, args []string) error {
	fs := newFlagSet("restore")
	archive := fs.String("from", "", "备份文件（.mabak，必填）")
	filesDir := fs.String("files", "", "附件目录（默认 <账套名>.files）")
	// --db 由 newFlagSet 统一注册（与其它命令一致），这里只取它的值。
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*archive) == "" {
		return errors.New("必须指定 --from <备份文件.mabak>")
	}
	dest := fs.Lookup("db").Value.String()

	// 恢复前先把备份内容摊开给用户看：确认恢复的是哪一份。
	// 同事发来的备份、几份文件名相似的备份，靠文件名是分不出来的。
	m, err := (&service.Service{}).InspectBackup(*archive)
	if err != nil {
		return err
	}
	fmt.Printf("即将恢复：%s\n\n", *archive)
	fmt.Printf("  单位名称：%s\n", m.CompanyName)
	fmt.Printf("  备份时间：%s（程序 %s）\n", m.CreatedAt, m.AppVersion)
	fmt.Printf("  凭证数：%d    科目数：%d\n", m.VoucherCount, m.AccountCount)
	fmt.Printf("  附件：%d 个\n", m.FileCount)
	if m.PeriodFrom != "" {
		fmt.Printf("  账务期间：%s ~ %s\n", m.PeriodFrom, m.PeriodTo)
	}
	fmt.Printf("\n目标账套：%s\n", dest)

	fd := *filesDir
	if fd == "" {
		fd = strings.TrimSuffix(dest, filepath.Ext(dest)) + ".files"
	}

	svc := &service.Service{}
	rr, err := svc.Restore(ctx, service.RestoreOptions{
		Archive: *archive, DBPath: dest, FilesDir: fd,
	})
	if err != nil {
		return err
	}
	fmt.Printf("\n✓ 已恢复\n")
	fmt.Printf("  账套：%s\n", dest)
	fmt.Printf("  附件：%d 个 → %s\n", rr.FilesRestored, fd)
	if rr.BackupDir != "" {
		// ★ 这个路径必须打出来。恢复错了备份时它是唯一的退路，
		// 不打出来等于没有 —— 用户不会知道原来的账被挪到哪儿去了。
		fmt.Printf("\n  恢复前的原账套已整体挪到：\n    %s\n", rr.BackupDir)
		fmt.Printf("  （确认恢复无误后可以自行删除；文件名对不上时它就是你原来的账）\n")
	}
	return nil
}

// cmdLog 查询/校验/导出操作日志。
//
// 对应《企业会计信息化工作规范》里用户操作日志的「可查询性」：
// 按操作人员、时间范围、操作内容分别或组合查询。
// cmdBookkeeper 查看或设置本账套的记账人。
//
// 记账人存在**账套**里（setting 表），所以这条命令必须 --db 指向某个账套 ——
// 不同账套可以签不同的名。
func cmdBookkeeper(ctx context.Context, args []string) error {
	fs := newFlagSet("bookkeeper")
	set := fs.String("set", "", "设置记账人姓名；传空串（--set \"\"）表示清空")
	clear := fs.Bool("clear", false, "清空记账人")
	if err := fs.Parse(args); err != nil {
		return err
	}
	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	switch {
	case *clear:
		if err := svc.SetBookkeeper(ctx, ""); err != nil {
			return err
		}
		fmt.Println("✓ 已清空本账套的记账人")
		fmt.Println("  之后录凭证时需要在界面上当场填写制单人")
		return nil
	case *set != "":
		if err := svc.SetBookkeeper(ctx, *set); err != nil {
			return err
		}
		fmt.Printf("✓ 本账套的记账人：%s\n", *set)
		fmt.Println("  录凭证、过账、结账时默认填它；命令行 --by 不传时也用它")
		return nil
	}

	name, err := svc.Bookkeeper(ctx)
	if err != nil {
		return err
	}
	info, _ := svc.Book(ctx)
	if name == "" {
		fmt.Printf("账套「%s」还没有设置记账人\n", info.CompanyName)
		fmt.Println("设置：miniaccount bookkeeper --db <账套> --set 李会计")
		return nil
	}
	fmt.Printf("账套「%s」的记账人：%s\n", info.CompanyName, name)
	return nil
}

func cmdLog(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	from := fs.String("from", "", "起始时间（YYYY-MM-DD 或 2025-03-01 08:00:00）")
	to := fs.String("to", "", "截止时间")
	actor := fs.String("actor", "", "按操作人筛选")
	action := fs.String("action", "", "按操作种类筛选，如 voucher.post；all 列出全部种类")
	failed := fs.Bool("failed", false, "只看失败的操作")
	category := fs.String("category", "all", "类别：business 业务操作 | read 查看 | system 系统 | all")
	recordViews := fs.String("record-views", "", "开关「记录查看操作」：on / off")
	text := fs.String("text", "", "全文关键字（摘要、对象编号、失败原因）")
	limit := fs.Int("limit", 50, "最多显示多少条")
	verify := fs.Bool("verify", false, "校验日志完整性（哈希链）")
	exportTo := fs.String("export", "", "导出为 CSV（Excel 可直接打开）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// 只查日志时不需要账套，但账套信息能让筛选更有用；拿不到也不拦。
	svc := &service.Service{}
	if dir, err := service.DefaultAuditDir(); err == nil {
		service.SetAuditDir(dir)
	}
	defer service.CloseAudit()

	if *recordViews != "" {
		cur := service.LogSettingsOf()
		switch strings.ToLower(strings.TrimSpace(*recordViews)) {
		case "on", "true", "1", "yes":
			cur.RecordViews = true
		case "off", "false", "0", "no":
			cur.RecordViews = false
		default:
			return fmt.Errorf("--record-views 只认 on / off，收到 %q", *recordViews)
		}
		if err := service.SaveLogSettings(cur); err != nil {
			return err
		}
		state := "关闭"
		if cur.RecordViews {
			state = "开启"
		}
		fmt.Printf("✓ 记录查看操作：%s\n", state)
		if cur.RecordViews {
			fmt.Println("  （同一对象在 60 秒内的重复查看只记一次，避免刷新一次写一条）")
		}
		return nil
	}

	if *action == "all" {
		for _, o := range svc.AuditOptions() {
			fmt.Printf("%-24s %s\n", o.Value, o.Label)
		}
		return nil
	}

	if *verify {
		res, err := svc.VerifyAudit(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("日志文件：%d 个，记录：%d 条\n", res.Files, res.Checked)
		fmt.Printf("链头哈希：%s\n", res.Head)
		if res.OK {
			fmt.Println("✓ 日志完整，没有被改动过")
			return nil
		}
		fmt.Println("⚠ 日志完整性校验未通过：")
		for _, iss := range res.Issues {
			fmt.Printf("  · 序号 %d %s：%s\n", iss.Seq, iss.File, iss.Reason)
		}
		return errors.New("日志完整性校验未通过")
	}

	q := service.AuditQuery{
		Operator: *actor, From: *from, To: *to, Text: *text, Limit: *limit,
	}
	if *action != "" {
		q.Actions = []string{*action}
	}
	if *failed {
		q.Result = "failed"
	}
	switch *category {
	case "business", "read", "system":
		q.Categories = []string{*category}
	case "all", "":
		// 不限
	default:
		return fmt.Errorf("--category 只认 business / read / system / all，收到 %q", *category)
	}
	if *exportTo != "" {
		n, err := svc.ExportAudit(ctx, q, *exportTo)
		if err != nil {
			return err
		}
		fmt.Printf("✓ 已导出 %d 条日志到 %s\n", n, *exportTo)
		return nil
	}

	page, err := svc.QueryAudit(ctx, q)
	if err != nil {
		return err
	}
	if len(page.Entries) == 0 {
		fmt.Println("没有符合条件的日志。")
		fmt.Println("（日志目录：", service.AuditDir(), "）")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "时间\t操作人\t操作\t对象\t摘要\t结果")
	for _, e := range page.Entries {
		entity := e.Entity
		if e.EntityID != "" {
			entity += " " + e.EntityID
		}
		summary := truncate(e.Summary, 40)
		if e.Result == audit.ResultFailed {
			summary = truncate(e.Summary+"："+e.Message, 40)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.At, e.Operator, e.Action.Label(), entity, summary, e.Result.Label())
	}
	_ = w.Flush()
	fmt.Printf("\n共 %d 条（显示 %d 条）· 日志文件 %d 个 · 占用 %s\n",
		page.Total, len(page.Entries), page.Segments, humanBytes(page.Bytes))
	fmt.Println("提示：miniaccount log --verify 可以校验日志有没有被改动过")
	return nil
}

func cmdInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("用法：miniaccount inspect <备份文件.mabak>")
	}
	m, err := (&service.Service{}).InspectBackup(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Printf("备份文件：%s\n\n", fs.Arg(0))
	fmt.Printf("  备份格式版本：%d\n", m.FormatVersion)
	fmt.Printf("  生成程序版本：%s\n", m.AppVersion)
	fmt.Printf("  生成时间：%s\n", m.CreatedAt)
	fmt.Printf("  单位名称：%s\n", m.CompanyName)
	fmt.Printf("  数据库：%s\n", humanBytes(m.DBSize))
	fmt.Printf("  附件：%d 个，%s\n", m.FileCount, humanBytes(m.FileBytes))
	fmt.Printf("  凭证数：%d    科目数：%d\n", m.VoucherCount, m.AccountCount)
	if m.PeriodFrom != "" {
		fmt.Printf("  账务期间：%s ~ %s\n", m.PeriodFrom, m.PeriodTo)
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ---------------------------------------------------------------------------
// AI
// ---------------------------------------------------------------------------

func cmdAI(ctx context.Context, args []string) error {
	fs := newFlagSet("ai")
	text := fs.String("text", "", "业务描述（必填）")
	amount := fs.String("amount", "", "金额（元，正为收入、负为支出）")
	date := fs.String("date", "", "业务日期 YYYY-MM-DD（默认今天）")
	cp := fs.String("counterparty", "", "对方户名")
	dir := fs.String("direction", "", "资金方向：收入 | 支出")
	task := fs.String("task", "bank_flow", "任务类型：bank_flow | invoice | expense | freeform")
	noHistory := fs.Bool("no-history", false, "不检索历史凭证")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *text == "" {
		return errors.New("必须指定 --text")
	}

	amt, err := parseYuan(*amount)
	if err != nil {
		return err
	}
	d := *date
	if d == "" {
		d = calendar.Today().String()
	}

	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	_ = noHistory // 历史检索的开关在编排层，这里保留参数以便将来接线

	res, err := svc.AISuggest(ctx, service.AISuggestInput{
		Task: *task, Text: *text, Amount: amt, Date: d,
		Counterparty: *cp, Direction: *dir,
		TargetType: "freeform",
	})
	if err != nil {
		return err
	}

	fmt.Printf("AI 记账建议（%s）\n\n", service.LayerLabel(res.Layer))
	fmt.Printf("  %s\n", res.Summary)
	if res.Model != "" {
		fmt.Printf("  模型：%s   耗时：%d ms   token：%d + %d\n",
			res.Model, res.LatencyMS, res.TokensIn, res.TokensOut)
	}
	if res.Voucher != nil && len(res.Voucher.Entries) > 0 {
		fmt.Printf("\n建议凭证：\n")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  科目\t摘要\t借方\t贷方\t辅助核算")
		for _, e := range res.Voucher.Entries {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n",
				e.AccountCode, e.Summary, blankIfZero(e.Debit),
				blankIfZero(e.Credit), e.AuxDesc)
		}
		_ = w.Flush()
		fmt.Printf("  合计：%s\n", yuan(res.Voucher.Total))
	}
	if len(res.Failures) > 0 {
		fmt.Printf("\n✗ 护栏拦下了这条建议（%d 项）：\n", len(res.Failures))
		for _, f := range res.Failures {
			fmt.Printf("  ✗ %s：%s\n", f.Title, f.Detail)
		}
		fmt.Printf("\n这些内容不会被写入账本。请人工核对后手工录入。\n")
		return nil
	}
	if len(res.Warnings) > 0 {
		fmt.Printf("\n提醒：\n")
		for _, w := range res.Warnings {
			fmt.Printf("  ! %s：%s\n", w.Title, w.Detail)
		}
	}
	if res.OK {
		fmt.Printf("\n✓ 通过全部护栏。以上内容仍是草稿，需人工确认后才过账。\n")
	}
	return nil
}

func cmdAIConfig(ctx context.Context, args []string) error {
	fs := newFlagSet("ai-config")
	name := fs.String("name", "", "服务名，如「本地 Ollama」")
	url := fs.String("url", "", "服务地址（不含 /chat/completions），如 http://127.0.0.1:11434/v1")
	model := fs.String("model", "", "模型名，如 qwen2.5:7b")
	key := fs.String("key", "", "API Key（本地模型留空）")
	kind := fs.String("kind", "", "部署形态：local | cloud（留空按地址推断）")
	def := fs.Bool("default", false, "设为默认服务")
	stats := fs.Bool("stats", false, "只显示使用统计")
	if err := fs.Parse(args); err != nil {
		return err
	}

	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()

	if *name != "" || *url != "" {
		if *name == "" || *url == "" || *model == "" {
			return errors.New("配置服务需要同时给出 --name、--url、--model")
		}
		k := *kind
		if k == "" {
			k = "local"
			if !strings.Contains(*url, "127.0.0.1") && !strings.Contains(*url, "localhost") {
				k = "cloud"
			}
		}
		id, err := svc.SaveAIProvider(ctx, &sqlite.AIProviderConfig{
			Name: *name, Kind: k, BaseURL: *url, Model: *model,
			APIKey: *key, Enabled: true, IsDefault: *def,
		})
		if err != nil {
			return err
		}
		fmt.Printf("✓ 已保存模型服务（id=%d）：%s / %s\n", id, *name, *model)
		if k == "cloud" {
			fmt.Printf("\n⚠ 这是云端服务：银行流水摘要、对方户名、金额会发送到 %s。\n", *url)
			fmt.Printf("  账号与统一社会信用代码会自动打码；金额与户名默认保留，\n")
			fmt.Printf("  因为它们是判断科目的核心依据。如需完全本地，请配置 Ollama。\n")
		}
	}

	cfg, err := svc.AIConfigInfo(ctx)
	if err != nil {
		return err
	}
	if len(cfg.Providers) > 0 {
		fmt.Printf("\n已配置的模型服务：\n")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  ID\t名称\t形态\t模型\t启用\t默认")
		for _, p := range cfg.Providers {
			fmt.Fprintf(w, "  %d\t%s\t%s\t%s\t%s\t%s\n",
				p.ID, p.Name, p.Kind, p.Model, yesNo(p.Enabled), yesNo(p.IsDefault))
		}
		_ = w.Flush()
	} else {
		fmt.Printf("\n尚未配置模型服务。示例（本地 Ollama）：\n")
		fmt.Printf("  miniaccount ai-config --name '本地 Ollama' \\\n")
		fmt.Printf("      --url http://127.0.0.1:11434/v1 --model qwen2.5:7b --default\n")
	}

	if *stats || cfg.Stats.Total > 0 {
		s := cfg.Stats
		fmt.Printf("\nAI 使用统计：\n")
		fmt.Printf("  调用总数：%d\n", s.Total)
		if n := s.TransportFailures(); n > 0 {
			fmt.Printf("  服务不可用/返回无法解析：%d（不计入护栏通过率）\n", n)
		}
		fmt.Printf("  护栏通过：%d / %d（%.0f%%）\n",
			s.Valid, s.Proposed, s.GuardrailPassRate()*100)
		fmt.Printf("  采纳 %d / 改后采纳 %d / 拒绝 %d（采纳率 %.0f%%）\n",
			s.Accepted, s.Modified, s.Rejected, s.AcceptanceRate()*100)
		if s.TokensIn+s.TokensOut > 0 {
			fmt.Printf("  token：%d 输入 + %d 输出\n", s.TokensIn, s.TokensOut)
		}
	}
	return nil
}

// parseYuan 把「元」的字符串解析成「分」。
//
// 与桌面端 desktop.ParseYuan 保持同样的严格度：拒绝超过两位小数，
// 拒绝十六进制/二进制/下划线分隔等 Go 整数字面量写法。
// 金额输入不接受歧义 —— 这里松一档，账上就会多一分钱的谜题。
func parseYuan(s string) (money.Money, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, nil
	}
	body := strings.NewReplacer(
		",", "", "，", "", "¥", "", "￥", "", " ", "", "\u00a0", "", "　", "",
	).Replace(trimmed)
	if i := strings.IndexByte(body, '.'); i >= 0 {
		frac := body[i+1:]
		if strings.IndexByte(frac, '.') >= 0 {
			return 0, fmt.Errorf("金额 %q 含多个小数点", trimmed)
		}
		if len(frac) > 2 {
			return 0, fmt.Errorf("金额 %q 最多两位小数", trimmed)
		}
	}
	m, err := money.Parse(trimmed)
	if err != nil {
		return 0, fmt.Errorf("金额 %q 无法识别", trimmed)
	}
	return m, nil
}
