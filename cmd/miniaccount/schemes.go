package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/payroll"
)

// cmdSchemes 查看 / 导入社保方案。
//
// 用「导出成 JSON → 编辑 → 导回」的方式而不是十几个命令行参数：
// 一个方案有 10 个费率加 4 个基数上下限，用参数表达既难写也难核对，
// 而省市之间的差异只有那张表本身才说得清。
func cmdSchemes(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("payroll schemes", flag.ExitOnError)
	db := fs.String("db", "miniaccount.db", "账套数据库文件路径")
	list := fs.Bool("list", false, "列出全部方案")
	dump := fs.Bool("export", false, "把现有方案导出为 JSON（可编辑后再用 --import 导回）")
	tpl := fs.String("template", "", "生成一张费率为零的空方案 JSON，写到指定文件")
	importFrom := fs.String("import", "", "从 JSON 文件导入方案（整体替换）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	svc, err := openBook(ctx, fs)
	if err != nil {
		return err
	}
	defer svc.Shutdown()
	_ = db

	switch {
	case *tpl != "":
		sc := payroll.SchemeTemplate("请改成你的城市名")
		b, _ := json.MarshalIndent([]*payroll.InsuranceScheme{sc}, "", "  ")
		if err := os.WriteFile(*tpl, append(b, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Printf("✓ 已写出空方案模板：%s\n", *tpl)
		fmt.Println()
		fmt.Println("  这张表的费率全是 0 —— 是故意的。")
		fmt.Println("  社保比例与基数上下限按城市、按年度发布，各地差异很大，")
		fmt.Println("  预置一组看着像真的数字会被直接当真使用，而算错了不会报任何错。")
		fmt.Println("  请按当地社保局公布的当年标准填写，然后用 --import 导回。")
		return nil

	case *importFrom != "":
		b, err := os.ReadFile(*importFrom)
		if err != nil {
			return fmt.Errorf("读取 %s 失败：%w", *importFrom, err)
		}
		var schemes []*payroll.InsuranceScheme
		if err := json.Unmarshal(b, &schemes); err != nil {
			return fmt.Errorf("解析 %s 失败（应为方案数组）：%w", *importFrom, err)
		}
		if err := svc.SaveInsuranceSchemes(ctx, schemes); err != nil {
			return err
		}
		fmt.Printf("✓ 已导入 %d 个社保方案\n", len(schemes))
		for _, sc := range schemes {
			mark := "已配置"
			if !sc.IsConfigured() {
				mark = "★ 费率全为 0，尚未配置（按这个方案算出来的社保是 0）"
			}
			fmt.Printf("  %s（%s）%s\n", sc.Name, sc.City, mark)
		}
		return nil
	}

	list2, err := svc.InsuranceSchemes(ctx)
	if err != nil {
		return err
	}

	if *dump {
		only := make([]*payroll.InsuranceScheme, 0, len(list2))
		for _, info := range list2 {
			only = append(only, info.Scheme)
		}
		sort.Slice(only, func(i, j int) bool { return only[i].Name < only[j].Name })
		b, _ := json.MarshalIndent(only, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	_ = list
	if len(list2) == 0 {
		fmt.Println("还没有任何社保方案。")
		fmt.Println()
		fmt.Println("工资模块需要至少一个方案，否则员工的社保无法计算：")
		fmt.Printf("  miniaccount payroll schemes --template 方案.json   # 生成空模板\n")
		fmt.Printf("  # 按当地社保局标准填写后：\n")
		fmt.Printf("  miniaccount payroll schemes --import 方案.json\n")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "方案\t城市\t社保基数\t公积金基数\t个人合计\t单位合计\t引用员工\t状态")
	for _, info := range list2 {
		sc := info.Scheme
		r := sc.Rates
		self := r.PensionSelf + r.MedicalSelf + r.UnemploymentSelf + r.HousingFundSelf
		co := r.PensionCo + r.MedicalCo + r.UnemploymentCo + r.InjuryCo +
			r.MaternityCo + r.HousingFundCo
		status := "已配置"
		if !info.Configured {
			status = "★ 费率全为 0（未配置）"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			sc.Name, sc.City,
			baseRange(sc.BaseMin, sc.BaseMax),
			baseRange(sc.HousingFundBaseMin, sc.HousingFundBaseMax),
			rateSum(self), rateSum(co), info.UsedBy, status)
	}
	_ = w.Flush()

	fmt.Println()
	fmt.Println("提示：管理 → 导出 → 编辑 → 导入")
	fmt.Println("  miniaccount payroll schemes --export > 方案.json")
	fmt.Println("  miniaccount payroll schemes --import 方案.json")
	return nil
}

// baseRange 把上下限写成人看得懂的形式；零表示不设限。
func baseRange(min, max money.Money) string {
	lo, hi := "不限", "不限"
	if min.IsPositive() {
		lo = min.String()
	}
	if max.IsPositive() {
		hi = max.String()
	}
	return lo + " ~ " + hi
}

// rateSum 把若干费率加起来显示成百分比。
// Rate.String() 自己就带 %，不要再乘一遍。
func rateSum(r money.Rate) string { return r.String() }
