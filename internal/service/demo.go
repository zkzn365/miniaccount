package service

// 演示账套的生成逻辑放在**服务层**而不再是命令行里。
//
// 原先它在 cmd/miniaccount 下，于是只有命令行能用 ——
// 而「先造一个演示账套看看软件长什么样」恰恰是**第一次打开界面的人**
// 最需要的功能。放在服务层后，命令行与界面共用同一份实现，
// 不会出现「命令行生成的演示数据」与「界面生成的」不一样这种事。

import (
	"context"
	"fmt"
	"strings"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
	"miniaccount/internal/store/sqlite"
	"time"
)

// DemoYear 是演示账套使用的年度。
const DemoYear = 2025

// DemoResult 是一次演示数据生成的结果。
type DemoResult struct {
	// CompanyName 是演示账套的单位名称。
	CompanyName string `json:"companyName"`
	// Posted 是写入的已过账凭证张数。
	Posted int `json:"posted"`
	// ClosedMonths 是自动结账的月数。
	ClosedMonths int `json:"closedMonths"`
	// ToMonth 是生成到第几个月。
	ToMonth int `json:"toMonth"`
	// Warnings 是生成过程中没能过账的凭证明细。
	//
	// 必须回传给调用方：静默失败会让用户拿到一套缺几张凭证的演示数据，
	// 而演示数据的全部意义就是「让人看到一套完整的账」。
	Warnings []string `json:"warnings"`
}

// CreateDemoBook 一步建好一个带演示数据的账套。
//
// 界面上的「先生成一个演示账套看看」按钮调它 ——
// 用户不需要先想清楚单位名称、纳税人身份、启用期间这些事，
// 点一下就能看到一套完整的账。
//
// 已经建过账的会被拒绝（而不是覆盖）：覆盖等于删掉用户的账，
// 这种事不该由一个「看看演示」的按钮触发。
func (s *Service) CreateDemoBook(ctx context.Context, toMonth int) (*DemoResult, error) {
	if toMonth < 1 || toMonth > 12 {
		return nil, fmt.Errorf("生成到第几个月应在 1—12 之间，实际 %d", toMonth)
	}
	exists, err := s.db.Books().Exists(ctx)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, sqlite.ErrBookExists
	}

	if _, err := s.CreateBook(ctx, CreateBookInput{
		CompanyName: "杭州云帆软件有限公司（演示）",
		CreditCode:  "91330100MA2EXAMPLE",
		LegalPerson: "张三",
		// 演示账套按一般纳税人 + 小型企业建：
		// 一般纳税人能看到进项/销项专栏与多栏式明细账的完整形态，
		// 小型企业能看到规模类型这一栏有值。
		VATStatus:       "general",
		EnterpriseScale: "small",
		StartYear:       DemoYear, StartMonth: 1,
		ThroughYear: DemoYear,
		CurrentYear: DemoYear, CurrentMonth: toMonth,
	}); err != nil {
		return nil, err
	}

	res, err := s.SeedDemo(ctx, DemoYear, 1, toMonth)
	if err != nil {
		return nil, err
	}

	// 把之前各月结账，让「当前可记账期间」落在最后一个月。
	//
	// 这不只是为了演示好看：会计本来就该按期结账，
	// 跳过 1、2 月直接看 3 月的首页概览，看到的是 1 月的数字 ——
	// 而「当前期间永远是最早的未结账期间」正是本软件刻意坚持的规则。
	res.ToMonth = toMonth
	for m := 1; m < toMonth; m++ {
		if _, cerr := s.Close(ctx, period.NewKey(DemoYear, m), "王主管"); cerr != nil {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%d 月结账失败：%v", m, cerr))
			continue
		}
		res.ClosedMonths++
	}
	return res, nil
}

// SeedDemo 写入一套演示数据。
//
// 调用方负责先建好账套（见 Service.CreateDemoBook）。
func (s *Service) SeedDemo(ctx context.Context, year, fromMonth, toMonth int) (*DemoResult, error) {
	pg := &demoPoster{svc: s}
	if err := pg.init(ctx); err != nil {
		return nil, err
	}
	// ★ 先整体校验科目，再动手生成。
	//
	// 生成到一半才发现某个科目不存在，会留下一套「半个演示账套」——
	// 用户看到的是一个残缺的账，而且不知道该删还是该留。
	if err := pg.validateAccounts(); err != nil {
		return nil, err
	}

	cust, err := pg.contact(ctx, "customer", "杭州云帆科技有限公司")
	if err != nil {
		return nil, err
	}
	sup, err := pg.contact(ctx, "supplier", "宁波恒信办公用品有限公司")
	if err != nil {
		return nil, err
	}
	shareholder, err := pg.contact(ctx, "shareholder", "张三")
	if err != nil {
		return nil, err
	}
	deptAdmin := int64(1)
	auxC := func(id *int64) map[string]int64 {
		if id == nil {
			return nil
		}
		return map[string]int64{"contact_id": *id}
	}
	auxD := map[string]int64{"dept_id": deptAdmin}

	var posted int
	count := func(n int) { posted += n }

	// ---- 1 月：建账起始 ----
	if fromMonth <= 1 && toMonth >= 1 {
		// 股东投入 200,000
		count(pg.post("2025-01-02", "收到股东投资款",
			line{pg.acct("银行存款"), "收到股东投资款", money100(200000), 0, nil},
			line{pg.acct("实收资本"), "收到股东投资款", 0, money100(200000), auxC(&shareholder)},
		))
		// 购置办公设备 36,000（固定资产要求部门）
		count(pg.post("2025-01-05", "购置办公设备",
			line{pg.acct("固定资产"), "购置办公设备", money100(36000), 0, auxD},
			line{pg.acct("银行存款"), "购置办公设备", 0, money100(36000), nil},
		))
		// 支付一季度房租 30,000
		count(pg.post("2025-01-06", "支付一季度房租",
			line{pg.acct("管理费用—租赁费"), "支付一季度房租", money100(30000), 0, auxD},
			line{pg.acct("银行存款"), "支付一季度房租", 0, money100(30000), nil},
		))
	}

	// ---- 2 月 ----
	if fromMonth <= 2 && toMonth >= 2 {
		count(pg.post("2025-02-08", "采购办公用品",
			line{pg.acct("管理费用—办公费"), "采购办公用品", money100(4200), 0, auxD},
			line{pg.acct("应付账款"), "采购办公用品", 0, money100(4200), auxC(&sup)},
		))
		count(pg.post("2025-02-20", "销售软件服务",
			line{pg.acct("应收账款"), "销售软件服务", money100(106000), 0, auxC(&cust)},
			line{pg.acct("主营业务收入"), "销售软件服务", 0, money100(100000), nil},
			line{pg.acct("应交增值税—销项税额"), "销项税额", 0, money100(6000), nil},
		))
	}

	// ---- 3 月：最完整的一个月，覆盖现金流量的三大活动 ----
	if fromMonth <= 3 && toMonth >= 3 {
		// 收回货款 106,000
		count(pg.post("2025-03-05", "收回货款",
			line{pg.acct("银行存款"), "收回货款", money100(106000), 0, nil},
			line{pg.acct("应收账款"), "收回货款", 0, money100(106000), auxC(&cust)},
		))
		// 销售收入 150,000 + 销项税 9,000
		count(pg.post("2025-03-12", "销售软件服务",
			line{pg.acct("应收账款"), "销售软件服务", money100(159000), 0, auxC(&cust)},
			line{pg.acct("主营业务收入"), "销售软件服务", 0, money100(150000), nil},
			line{pg.acct("应交增值税—销项税额"), "销项税额", 0, money100(9000), nil},
		))
		// 缴纳上月增值税 6,000
		// ★ 这笔与上面的销项税方向相反，是验证现金流量归类的关键：
		//   销项税随货款流入 → 属于销售收款；缴税流出 → 属于支付的各项税费
		count(pg.post("2025-03-15", "缴纳增值税",
			line{pg.acct("应交增值税—已交税金"), "缴纳增值税", money100(6000), 0, nil},
			line{pg.acct("银行存款"), "缴纳增值税", 0, money100(6000), nil},
		))
		// 支付货款 4,200
		count(pg.post("2025-03-18", "支付货款",
			line{pg.acct("应付账款"), "支付货款", money100(4200), 0, auxC(&sup)},
			line{pg.acct("银行存款"), "支付货款", 0, money100(4200), nil},
		))
		// 发放 2—3 月工资 48,000
		count(pg.post("2025-03-25", "发放工资",
			line{pg.acct("应付职工薪酬—工资"), "发放工资", money100(48000), 0, nil},
			line{pg.acct("银行存款"), "发放工资", 0, money100(48000), nil},
		))
		// 计提工资 48,000（费用 → 应付职工薪酬）
		count(pg.post("2025-03-31", "计提 2—3 月工资",
			line{pg.acct("管理费用—工资"), "计提工资", money100(48000), 0, auxD},
			line{pg.acct("应付职工薪酬—工资"), "计提工资", 0, money100(48000), nil},
		))
		// 计提折旧 900（36,000 / 3 年 / 12 月 = 1,000/月，这里取 900 便于观察）
		count(pg.post("2025-03-31", "计提固定资产折旧",
			line{pg.acct("管理费用—折旧费"), "计提折旧", money100(900), 0, auxD},
			line{pg.acct("累计折旧"), "计提折旧", 0, money100(900), auxD},
		))
	}

	// ---- 后续月份：只保留最小业务量，让 3 月的报表干净可读 ----
	for m := 4; m <= toMonth; m++ {
		if m < fromMonth {
			continue
		}
		date := fmt.Sprintf("%d-%02d-15", year, m)
		count(pg.post(date, "支付办公费",
			line{pg.acct("管理费用—办公费"), "支付办公费", money100(1500), 0, auxD},
			line{pg.acct("银行存款"), "支付办公费", 0, money100(1500), nil},
		))
	}

	var keys []period.Key
	for m := fromMonth; m <= toMonth; m++ {
		keys = append(keys, period.NewKey(year, m))
	}
	_ = keys
	return &DemoResult{CompanyName: pg.company, Posted: posted,
		Warnings: pg.warnings}, nil
}

// line 是构造演示凭证的一行。
type line struct {
	code, summary string
	debit, credit money.Money
	aux           map[string]int64
}

// demoPoster 负责把演示凭证写进账套。
type demoPoster struct {
	svc     *Service
	accIDs  map[string]int64
	byName  map[string]string // 科目全名 → 编码
	company string
	// warnings 是生成过程中没能过账的凭证说明。
	warnings []string
}

func (p *demoPoster) init(ctx context.Context) error {
	ids, err := p.svc.DB().Accounts().IDsByCode(ctx)
	if err != nil {
		return err
	}
	p.accIDs = ids

	accounts, err := p.svc.DB().Accounts().List(ctx)
	if err != nil {
		return err
	}
	p.byName = make(map[string]string, len(accounts))
	for _, a := range accounts {
		p.byName[a.Name] = a.Code
	}

	book, err := p.svc.Book(ctx)
	if err != nil {
		return err
	}
	p.company = book.CompanyName
	return nil
}

// acct 按科目**全名**取编码。
//
// ★ 演示数据一律按名称取编码，不写死数字。
// 写死编码出过事：把「计提折旧」记到了 560208（业务招待费）上 ——
// 编码看着像、过账也不报错，只有对着利润表逐行看才发现错了。
// 按名称取，名字对不上就当场报错，这类错误不可能再悄悄发生。
func (p *demoPoster) acct(name string) string {
	code, ok := p.byName[name]
	if !ok {
		// 走到这里说明 validateAccounts 漏了一个名字 —— 那是程序 bug。
		// 之所以敢 panic：SeedDemo 开头会先校验全部用到的科目名，
		// 校验通过后这里不可能失败（见 demoAccountNames）。
		panic(fmt.Sprintf("演示数据：科目表中没有名为 %q 的科目", name))
	}
	return code
}

// demoAccountNames 是演示数据用到的**全部**科目名称。
//
// ★ 演示数据一律按科目**名称**取编码，不写死数字。
//
// 写死编码出过事：把「计提折旧」记到了 560208（业务招待费）上 ——
// 编码看着像、过账也不报错，只有对着利润表逐行看才发现错了。
//
// 按名称取的代价是「名字对不上就取不到」，所以生成之前**先整体校验**，
// 一次性把所有缺失的科目名报出来，而不是生成到一半才炸。
// 这样界面调用时得到的是一个可读的错误，而不是一个 panic。
var demoAccountNames = []string{
	"银行存款", "实收资本", "固定资产", "管理费用—租赁费",
	"管理费用—办公费", "管理费用—折旧费", "管理费用—工资",
	"管理费用—社会保险费", "管理费用—差旅费", "管理费用—水电费",
	"管理费用—业务招待费", "管理费用—通讯费", "管理费用—车辆使用费",
	"管理费用—中介服务费", "管理费用—其他", "主营业务收入",
	"主营业务成本", "应收账款", "应付账款", "其他应付款—股东",
	"应交税费—应交增值税", "应交增值税—进项税额",
	"应交增值税—销项税额", "应交增值税—已交税金",
	"应交税费—未交增值税", "应交税费—应交个人所得税",
	"应交税费—应交城市维护建设税", "应交税费—应交教育费附加",
	"应付职工薪酬—工资", "库存商品", "销售费用—运输费",
	"管理费用—职工福利费",
}

// validateAccounts 校验演示数据用到的科目是否都在科目表里。
func (p *demoPoster) validateAccounts() error {
	var missing []string
	for _, name := range demoAccountNames {
		if _, ok := p.byName[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"演示数据需要以下科目，但当前科目表里没有：%s。"+
				"演示数据按科目名称取编码以避开写死数字的坑，"+
				"请确认账套用的是预置科目表",
			strings.Join(missing, "、"))
	}
	return nil
}

// money100 把「元」写成 money.Money（测试与演示数据用）。
func money100(yuan int64) money.Money { return money.Money(yuan) * money.Yuan }

func (p *demoPoster) contact(ctx context.Context, kind, name string) (int64, error) {
	return p.svc.DB().Contacts().AddContact(ctx, kind, name)
}

// post 写一张演示凭证并过账，返回凭证张数（1 或 0）。
//
// 演示数据也要走**正常过账路径**，不能直接写表：
// 只有这样才能顺带验证过账校验、凭证编号、总账写入全都正常。
func (p *demoPoster) post(date, remark string, lines ...line) int {
	count, err := p.tryPost(date, remark, lines...)
	if err != nil {
		// 记下来而不是写 stderr：界面看不到标准错误，
		// 静默失败会让用户得到一套缺几张凭证的演示数据而毫无察觉。
		p.warnings = append(p.warnings,
			fmt.Sprintf("演示凭证 %s %s 过账失败：%v", date, remark, err))
		return 0
	}
	return count
}

func (p *demoPoster) tryPost(date, remark string, lines ...line) (int, error) {
	ctx := context.Background()
	d, err := calendar.Parse(date)
	if err != nil {
		return 0, err
	}
	vc, err := voucher.New(voucher.WordJi, d, "李会计")
	if err != nil {
		return 0, err
	}
	vc.Remark = remark
	for _, ln := range lines {
		e := ledger.Entry{
			AccountCode: ln.code, Summary: ln.summary,
			Debit: ln.debit, Credit: ln.credit,
		}
		if ln.aux != nil {
			if v, ok := ln.aux["contact_id"]; ok {
				id := v
				e.Aux.ContactID = &id
			}
			if v, ok := ln.aux["dept_id"]; ok {
				id := v
				e.Aux.DeptID = &id
			}
			if v, ok := ln.aux["employee_id"]; ok {
				id := v
				e.Aux.EmployeeID = &id
			}
			if v, ok := ln.aux["project_id"]; ok {
				id := v
				e.Aux.ProjectID = &id
			}
		}
		if err := vc.AddEntry(e); err != nil {
			return 0, err
		}
	}
	if _, err := p.svc.DB().Vouchers().Post(ctx, sqlite.PostInput{
		Voucher: vc, Accounts: p.accIDs, PostingBy: "王主管", At: time.Now(),
	}); err != nil {
		return 0, err
	}
	return 1, nil
}

// money100 把「元」转成「分」。
