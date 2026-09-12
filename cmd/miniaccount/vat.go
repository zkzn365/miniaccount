package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/vat"
	"miniaccount/internal/service"
)

// cmdVAT 查看增值税税率政策与纳税人身份。
//
// 政策是**数据**不是代码：每条都带法定税率、实际优惠税率、生效失效日期
// 与政策依据。政策调整时改数据即可，不必等新版本 ——
// 而 1% 优惠这类阶段性政策写死在代码里，到期后用户会一直按错税率开票。
func cmdVAT(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("vat", flag.ExitOnError)
	db := fs.String("db", "miniaccount.db", "账套数据库文件路径")
	policies := fs.Bool("policies", false, "列出全部税率政策")
	on := fs.String("on", "", "按指定日期筛选政策（YYYY-MM-DD，默认今天）")
	// 查税率
	rateOf := fs.Bool("rate", false, "按「身份+主体+业务+方法+日期」查适用税率")
	status := fs.String("status", "", "纳税人身份：general | small_scale")
	subject := fs.String("subject", "entity", "经营主体：entity 单位 | self_employed 个体工商户 | person 个人")
	category := fs.String("category", "", "业务类型")
	// 默认留空，由服务层按纳税人身份推断：
	// 一般纳税人 → 一般计税，小规模纳税人 → 简易计税。
	// 给一个写死的默认值会把这条推断堵死 —— 小规模查「销售货物」
	// 会报「找不到一般计税的政策」，而它本来该查简易计税。
	method := fs.String("method", "", "计税方法：general|simplified|exempt|zero_rated|not_taxable（留空按身份推断）")
	// ★ 例外情形（如出口命中公告第七条异常情形）默认**不**参与匹配。
	// 是否命中属事实认定，程序替用户选了就可能少缴税；
	// 只有用户自己确认命中，才把这个开关打开。
	conditional := fs.Bool("conditional", false, "我已确认命中例外情形（如出口异常情形），把该政策纳入匹配")
	// 进项抵扣判定
	deduct := fs.Bool("deduct", false, "判定一笔进项税额能否抵扣")
	voucher := fs.String("voucher", "", "扣税凭证类型（见 --ref）")
	taxAmt := fs.String("tax", "", "凭证注明的进项税额（元）")
	forSimplified := fs.Bool("for-simplified", false, "该支出用于简易计税或免税项目")
	abnormal := fs.Bool("abnormal-loss", false, "非正常损失")
	welfare := fs.Bool("welfare", false, "集体福利、个人消费或交际应酬")
	catering := fs.Bool("catering", false, "直接消费的餐饮、居民日常、娱乐服务")
	loan := fs.Bool("loan-interest", false, "贷款利息及直接相关的顾问费、手续费、咨询费")
	nonTaxable := fs.Bool("non-taxable", false, "对应特定非应税交易")
	equity := fs.Bool("equity-transfer", false, "股权转让、股息红利或部分境外交易")
	longAsset := fs.Bool("long-term-asset", false, "形成长期资产（固定资产/无形资产/不动产）")
	mixedUse := fs.Bool("mixed-use", false, "该长期资产混合用于一般计税与不得抵扣项目")
	assetVal := fs.String("asset-value", "", "长期资产单项原值（元）")
	credited := fs.Bool("credited", false, "该笔进项已经抵扣过（决定是「不予抵扣」还是「进项转出」）")
	ref := fs.Bool("ref", false, "列出凭证类型、计税方法与不得抵扣原因")

	exportTo := fs.String("export", "", "把政策表导出为 JSON（可编辑后用 --import 导回）")
	importFrom := fs.String("import", "", "从 JSON 导入政策表（整体替换）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()
	_ = db

	day := calendar.Today()
	if *on != "" {
		d, perr := calendar.Parse(*on)
		if perr != nil {
			return fmt.Errorf("日期 %q 无效（应为 YYYY-MM-DD）：%w", *on, perr)
		}
		day = d
	}

	// 参照表
	if *ref {
		r := svc.VATReference()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "扣税凭证类型\t可否抵扣")
		for _, o := range r.VoucherKinds {
			mark := "不可抵扣"
			if o.Deductible {
				mark = "可以抵扣"
			}
			fmt.Fprintf(w, "%s（%s）\t%s\n", o.Label, o.Value, mark)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "不得抵扣原因\t需进项税额转出")
		for _, o := range r.NonDeductibleReasons {
			mark := "不需要"
			if o.TransferOut {
				mark = "需要"
			}
			fmt.Fprintf(w, "%s（%s）\t%s\n", o.Label, o.Value, mark)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "业务类型")
		for _, o := range r.Categories {
			fmt.Fprintf(w, "%s（%s）\n", o.Label, o.Value)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "计税方法\t进项可否抵扣")
		for _, o := range r.Methods {
			mark := "不可抵扣"
			if o.Deductible {
				mark = "可以抵扣"
			}
			fmt.Fprintf(w, "%s（%s）\t%s\n", o.Label, o.Value, mark)
		}
		fmt.Fprintln(w)
		fmt.Printf("混合用途长期资产分界点：%s（超过则先抵扣、后按年度调整）\n",
			r.LongTermAssetThreshold)
		fmt.Printf("软件业务提示：%s\n", r.SoftwareHint)
		return nil
	}

	// 进项抵扣判定
	if *deduct {
		res, err := svc.JudgeDeduction(ctx, service.DeductionRequest{
			On: *on, Voucher: *voucher, TaxYuan: *taxAmt, Method: *method,
			ForSimplifiedOrExempt: *forSimplified,
			AbnormalLoss:          *abnormal,
			CollectiveWelfare:     *welfare,
			CateringRecreation:    *catering,
			LoanInterest:          *loan,
			NonTaxableTransaction: *nonTaxable,
			EquityTransfer:        *equity,
			IsLongTermAsset:       *longAsset,
			MixedUse:              *mixedUse,
			AssetValueYuan:        *assetVal,
			AlreadyCredited:       *credited,
		})
		if err != nil {
			return err
		}
		fmt.Printf("业务日期：%s（增值税纳税人身份：%s）\n",
			res.VATStatusOnDate, res.VATStatusLabel)
		fmt.Printf("扣税凭证：%s\n", res.VoucherLabel)
		fmt.Printf("计税方法：%s\n", res.MethodLabel)
		fmt.Println()
		if res.Deductible {
			fmt.Printf("✓ 可以抵扣：%s\n", yuan(res.DeductibleAmount))
		} else {
			fmt.Printf("✗ 不得抵扣：%s\n", res.ReasonLabel)
			if res.TransferOut > 0 {
				fmt.Printf("  需做进项税额转出：%s\n", yuan(res.TransferOut))
			}
			if res.IncludedInCost > 0 {
				fmt.Printf("  计入成本或资产价值：%s\n", yuan(res.IncludedInCost))
			}
		}
		fmt.Printf("\n说明：%s\n", res.Note)
		return nil
	}

	// 导入 / 导出
	switch {
	case *exportTo != "":
		if err := svc.ExportVATPolicies(ctx, *exportTo); err != nil {
			return err
		}
		fmt.Printf("✓ 已导出税率政策表：%s\n", *exportTo)
		return nil
	case *importFrom != "":
		n, err := svc.ImportVATPolicies(ctx, *importFrom)
		if err != nil {
			return err
		}
		fmt.Printf("✓ 已导入 %d 条税率政策\n", n)
		return nil
	}

	// 单条查询
	if *rateOf {
		res, err := svc.ResolveVATRate(ctx, service.VATRateQuery{
			Status: *status, Subject: *subject,
			Category: *category, Method: *method, On: *on,
			IncludeConditional: *conditional,
		})
		if err != nil {
			return err
		}
		fmt.Printf("业务日期：%s\n", day)
		fmt.Printf("纳税人身份：%s\n", res.StatusLabel)
		fmt.Printf("经营主体：%s\n", res.SubjectLabel)
		fmt.Printf("业务类型：%s\n", res.CategoryLabel)
		fmt.Printf("计税方法：%s\n", res.MethodLabel)
		if res.Conditional {
			// 例外政策是用户自己勾出来的，结果里必须留痕，
			// 否则截图或打印出来就看不出这个税率是有前提的。
			fmt.Printf("★ 需人工确认：本结果按「%s」给出（例外情形）\n", res.Name)
		}
		if !res.Exact {
			// 非精确匹配时税率**不是**用户问的那一档，
			// 只印一个税率会让人以为问到了。
			fmt.Printf("\n⚠ 未找到与你所问情形精确匹配的政策。\n")
		}
		fmt.Printf("税收处理：%s\n", res.TreatmentLabel)
		fmt.Printf("法定税率/征收率：%s\n", res.StatutoryRate)
		if res.PreferentialRate != "" {
			fmt.Printf("优惠税率：%s\n", res.PreferentialRate)
		}
		fmt.Printf("★ 实际适用：%s\n", res.Rate)
		if !res.RateApplicable {
			fmt.Printf("  （本档不适用税率，不是 0%%）\n")
		}
		fmt.Printf("进项税额：%s\n", res.InputTaxLabel)
		fmt.Printf("政策依据：%s\n", res.LegalBasis)
		if res.Note != "" {
			fmt.Printf("说明：%s\n", res.Note)
		}
		if res.ExpiresOn != "" {
			fmt.Printf("⚠ 该政策截止 %s，到期后会自动回落到法定税率\n", res.ExpiresOn)
		}
		return nil
	}

	// 默认：列出政策表
	list, err := svc.VATPolicies(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("增值税税率政策表（%d 条，政策版本 %s）\n", len(list), list[0].Version)
	fmt.Printf("查询日期：%s\n", day)
	// ★ 政策表版本落后时必须提示。
	//
	// 政策是会变的（1% 优惠到期、出口规则调整），用户手里是一份旧表
	// 就会一直按旧税率开票 —— 而他从界面上看不出来。
	if st, err := svc.PolicyStatus(ctx); err == nil && st.UpdateAvailable {
		fmt.Printf("⚠ 政策表版本 %s，程序内置已是 %s —— ", st.Version, st.BuiltinVersion)
		switch st.Origin {
		case "builtin":
			fmt.Println("本次读取会自动换成新版")
		case "custom":
			fmt.Println("这份表你改过，程序不覆盖；" +
				"确认后可载入新版内置表（会丢掉你的改动）")
		default:
			fmt.Println("本账套是旧版本建的，无法确认这份表改没改过，" +
				"所以程序不覆盖；请自行核对后决定是否载入新版内置表")
		}
	}
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	// ★ 列表里必须带**政策名称**。
	//
	// 「销售自己使用过的固定资产 3% 减按 2%」与「个人出租住房 3% 减按 1.5%」
	// 这类特例规则，光看「业务类型」是认不出来的 —— 而它们恰恰是
	// 最容易被误当成通用规则的两条。
	fmt.Fprintln(w, "身份/主体\t政策\t业务类型\t计税方法\t税收处理\t法定\t实际\t生效期")
	for _, p := range list {
		if !p.ActiveOn {
			continue
		}
		identity := p.StatusLabel
		if identity == "" {
			identity = "不限"
		}
		// 主体限定要显示出来：个人出租住房 1.5% 与单位出租不动产 3%
		// 是**两条不同的政策**，只显示「小规模纳税人」看不出区别。
		if p.SubjectLabel != "" && p.SubjectLabel != "不限" {
			identity = p.SubjectLabel + "·" + identity
		}
		period := p.EffectiveFrom
		if p.EffectiveTo != "" {
			period += " ~ " + p.EffectiveTo
		} else {
			period += " 起"
		}
		actual := p.Rate
		if p.PreferentialRate != "" {
			actual += "（优惠）"
		}
		// 税收处理单列一栏：免税、零税率、不征税的「实际」都是空的，
		// 只看税率那一栏分不出它们 —— 而三者的进项处理完全不同。
		treatment := p.TreatmentLabel
		if p.Conditional {
			treatment += "·需确认"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			identity, truncate(p.Name, 26), truncate(p.CategoryLabel, 18),
			p.MethodLabel, treatment, p.StatutoryRate, actual, period)
	}
	_ = w.Flush()
	_ = policies

	fmt.Println()
	fmt.Println("说明：")
	fmt.Println("  · 法定税率与实际优惠税率分开存 —— " +
		"优惠到期后能看出某张发票当时适用哪个")
	fmt.Println("  · 每条政策都有生效/失效日期，程序按业务发生日取当时有效的政策")
	fmt.Println("  · 小规模销售/出租不动产、转让土地使用权不自动套用 1% 减征，走单独规则")
	fmt.Println("  · " + vat.SoftwareBizHint())
	fmt.Println("  · 标「全部业务类型」的政策与业务类型无关（起征点、不征税）")
	fmt.Println("  · 「税收处理」是免税 / 零税率 / 不征税时，税率栏为空是不适用税率，" +
		"不是 0% —— 免税的进项不得抵扣，零税率的进项可以退")
	fmt.Println("  · 标「需确认」的是例外情形（如出口命中公告第七条），" +
		"属事实认定，默认不参与匹配，确认命中后才按它算")
	fmt.Println()
	fmt.Println("可用业务类型：")
	for _, c := range vat.SelectableCategories() {
		fmt.Printf("  %-26s %s\n", c, c.Label())
	}
	fmt.Println()
	fmt.Println("导出后可用编辑器修改，再导回：")
	fmt.Println("  miniaccount vat --export policies.json")
	fmt.Printf("  miniaccount vat --import policies.json\n")
	return nil
}
