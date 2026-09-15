package taxfiling_test

import (
	"strings"
	"testing"

	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/taxfiling"
)

func y(n int64) money.Money { return money.Money(n) * money.Yuan }

// 一条典型的申报记录：2025-03 增值税，4 月 15 日申报并缴纳。
func ok() taxfiling.Filing {
	return taxfiling.Filing{
		Year: 2025, Month: 3, Kind: taxfiling.KindVAT,
		Status: taxfiling.StatusPaid, FiledDate: "2025-04-15", PaidDate: "2025-04-18",
		TaxAmount: y(6_500), Surcharge: y(780), Payable: y(7_280),
		Channel: "电子税务局", ReceiptNo: "1234567890", Operator: "李会计",
	}
}

func TestFilingValidate(t *testing.T) {
	if err := ok().Validate(); err != nil {
		t.Fatalf("基准用例应当通过：%v", err)
	}
	cases := []struct {
		name string
		mod  func(f *taxfiling.Filing)
		want string
	}{
		{"税种不认识", func(f *taxfiling.Filing) { f.Kind = "stamp" }, "税种"},
		{"属期非法", func(f *taxfiling.Filing) { f.Month = 13 }, "属期"},
		{"状态不认识", func(f *taxfiling.Filing) { f.Status = "maybe" }, "状态"},
		{"申报日期读不出来", func(f *taxfiling.Filing) { f.FiledDate = "4月15日" }, "申报日期"},
		// ★ 属期还没结束就申报 —— 一定是填错了
		{"申报早于月末", func(f *taxfiling.Filing) {
			f.FiledDate = "2025-03-20"
			f.PaidDate = "2025-03-25"
		}, "早于属期月末"},
		{"缴款早于申报", func(f *taxfiling.Filing) { f.PaidDate = "2025-04-01" }, "早于申报日期"},
		{"填了缴款日期却不是已缴纳", func(f *taxfiling.Filing) { f.Status = taxfiling.StatusFiled }, "已申报并缴纳"},
		{"说是已缴纳却没填缴款日期", func(f *taxfiling.Filing) { f.PaidDate = "" }, "缴款日期"},
		{"金额对不上", func(f *taxfiling.Filing) { f.Surcharge = y(1_000) }, "要能对上"},
		{"作废没留痕", func(f *taxfiling.Filing) { f.Status = taxfiling.StatusVoid }, "谁办的"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := ok()
			c.mod(&f)
			err := f.Validate()
			if err == nil {
				t.Fatal("应当被拦下")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("报错应当提到 %q，实际 %v", c.want, err)
			}
		})
	}
}

// 作废要留痕：谁、什么时候、为什么。
func TestVoidNeedsTrace(t *testing.T) {
	f := ok()
	f.Status = taxfiling.StatusVoid
	f.VoidedBy = "李会计"
	f.VoidedAt = "2025-05-06"
	if err := f.Validate(); err != nil {
		t.Fatalf("作废记录带齐痕迹应当通过：%v", err)
	}
	f.VoidedAt = "5月6日"
	if err := f.Validate(); err == nil {
		t.Error("作废日期读不出来时应当报错")
	}
}

// 多缴（应补为负）是合法状态：税额 + 附加 = 应补，照样要对得上。
func TestFilingAllowsNegativePayable(t *testing.T) {
	f := ok()
	f.Status = taxfiling.StatusFiled
	f.PaidDate = ""
	f.TaxAmount = y(-500)
	f.Surcharge = 0
	f.Payable = y(-500)
	if err := f.Validate(); err != nil {
		t.Fatalf("多缴应当可以登记：%v", err)
	}
}

// ★ 勾稽：账后来改了，台账里的数与现在算出来的数不一致 —— 必须报出来。
//
// 不报出来的话，谁也不会注意到「申报之后又补录了凭证」这件事。
func TestReconcile(t *testing.T) {
	f := ok() // 当时按 7,280 申报

	// 一致
	if diff, msg := f.Reconcile(y(7_280)); !diff.IsZero() {
		t.Errorf("一致时不该有差异，得到 %s（%s）", diff, msg)
	} else if !strings.Contains(msg, "一致") {
		t.Errorf("结论 = %q", msg)
	}

	// 现在算出来更多：多半是申报后补录了凭证
	diff, msg := f.Reconcile(y(8_000))
	if diff != y(720) {
		t.Errorf("差异 = %s，期望 720.00", diff)
	}
	if !strings.Contains(msg, "多 720.00") || !strings.Contains(msg, "更正申报") {
		t.Errorf("要说清差在哪、以及要不要更正：%q", msg)
	}

	// 现在算出来更少
	diff, msg = f.Reconcile(y(6_000))
	if diff != y(-1_280) {
		t.Errorf("差异 = %s，期望 -1,280.00", diff)
	}
	if !strings.Contains(msg, "少 1,280.00") {
		t.Errorf("结论 = %q", msg)
	}

	// 已作废的记录不参与勾稽
	voided := ok()
	voided.Status = taxfiling.StatusVoid
	voided.VoidedBy = "李会计"
	voided.VoidedAt = "2025-05-06"
	if diff, msg := voided.Reconcile(y(8_000)); !diff.IsZero() {
		t.Errorf("作废记录不该参与勾稽，得到 %s（%s）", diff, msg)
	}
}

func TestFilingSummaryAndLabels(t *testing.T) {
	f := ok()
	s := f.Summary()
	for _, want := range []string{"增值税", "2025-03", "已申报并缴纳", "7,280.00", "2025-04-15"} {
		if !strings.Contains(s, want) {
			t.Errorf("概览里缺少 %q：%q", want, s)
		}
	}
	for _, st := range taxfiling.AllStatuses {
		if st.Label() == "" || st.Label() == string(st) {
			t.Errorf("状态 %s 没有中文名", st)
		}
	}
	// 选项不含「已作废」：作废要走作废操作，不能靠改状态绕过留痕
	for _, o := range taxfiling.StatusOptions() {
		if o.Value == string(taxfiling.StatusVoid) {
			t.Error("状态选项里不该有「已作废」—— 作废必须留痕")
		}
		if o.Hint == "" {
			t.Errorf("状态 %s 缺少说明", o.Value)
		}
	}
	if len(taxfiling.KindOptions()) != 3 {
		t.Errorf("应当有三种税，实际 %d", len(taxfiling.KindOptions()))
	}
}
