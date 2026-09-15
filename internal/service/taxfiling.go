package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/taxfiling"
	"miniaccount/internal/domain/taxreturn"
)

// ---------------------------------------------------------------------------
// 税务申报台账
// ---------------------------------------------------------------------------
//
// 记下「这一期这个税种报没报、什么时候报的、报了多少、谁办的」。
//
// ★ 它是**发生过的事实**，不是账套的派生结果 —— 所以落库（0014）。
//
// 台账里存的是申报当时的快照；读的时候再拿它与现在算出来的表勾稽：
// 账后来改了（补录凭证、调整），两个数就会不一致，而这件事必须被看见。
// 看不见的后果是：申报数与账面数悄悄分叉，等到税务检查时才发现。

// TaxFilingView 是一条申报记录。
type TaxFilingView struct {
	ID          int64  `json:"id"`
	Year        int    `json:"year"`
	Month       int    `json:"month"`
	Period      string `json:"period"`
	PeriodLabel string `json:"periodLabel"`
	Kind        string `json:"kind"`
	KindLabel   string `json:"kindLabel"`
	Status      string `json:"status"`
	StatusLabel string `json:"statusLabel"`
	FiledDate   string `json:"filedDate"`
	PaidDate    string `json:"paidDate"`
	// ---- 申报当时的快照 ----
	Payable   money.Money `json:"payable"`
	TaxAmount money.Money `json:"taxAmount"`
	Surcharge money.Money `json:"surcharge"`
	// Paid 是申报时账上已经缴过的部分（增值税的已交税金、所得税的已预缴）。
	Paid money.Money `json:"paid"`
	// ---- 回执 ----
	Channel   string `json:"channel"`
	ReceiptNo string `json:"receiptNo"`
	Operator  string `json:"operator"`
	Note      string `json:"note"`
	// ---- 作废痕迹 ----
	VoidedBy   string `json:"voidedBy"`
	VoidedAt   string `json:"voidedAt"`
	VoidReason string `json:"voidReason"`
	// Computed 是**现在**按账算出来的应补(退)税额。
	Computed money.Money `json:"computed"`
	// Diff 是「现在算出来的 − 当时申报的」。
	Diff money.Money `json:"diff"`
	// Reconcile 是勾稽结论（一致 / 差多少 / 要不要更正）。
	Reconcile string `json:"reconcile"`
	// Summary 是一句话（列表用）。
	Summary string `json:"summary"`
}

// TaxFilingListView 是某年的申报台账。
type TaxFilingListView struct {
	Year  int             `json:"year"`
	Items []TaxFilingView `json:"items"`
	// Effective / Voided 是有效与已作废的条数。
	Effective int `json:"effective"`
	Voided    int `json:"voided"`
	// Pending 是**本年已启用期间里还没有申报记录**的税种（供提醒）。
	Pending []TaxFilingPendingView `json:"pending"`
	// Concludes 是一句话总结。
	Concludes string `json:"concludes"`
}

// TaxFilingPendingView 是一条「还没登记申报」的提醒。
type TaxFilingPendingView struct {
	Period    string `json:"period"`
	Kind      string `json:"kind"`
	KindLabel string `json:"kindLabel"`
	// Payable 是当前算出来的应补(退)税额。
	Payable money.Money `json:"payable"`
	// Hint 说明下一步做什么。
	Hint string `json:"hint"`
}

// TaxFilingInput 是登记一条申报记录的入参。
type TaxFilingInput struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	Year  int    `json:"year"`
	Month int    `json:"month"`
	// Status：filed 已申报 | paid 已申报并缴纳。
	Status    string `json:"status"`
	FiledDate string `json:"filedDate"`
	PaidDate  string `json:"paidDate"`
	// ---- 金额。FromCurrentReturn 为真时由程序按当前计算表填 ----
	// 等式：Payable = TaxAmount + Surcharge − Paid
	Payable     money.Money `json:"payable"`
	TaxAmount   money.Money `json:"taxAmount"`
	Surcharge   money.Money `json:"surcharge"`
	Paid        money.Money `json:"paid"`
	FromCurrent bool        `json:"fromCurrentReturn"`
	// ManualAmounts 为真表示调用方**手工填了**金额（含显式填 0）。
	ManualAmounts bool   `json:"manualAmounts"`
	Channel       string `json:"channel"`
	ReceiptNo     string `json:"receiptNo"`
	Note          string `json:"note"`
	// Operator 是经办人（台账要记清是谁办的）。
	Operator string `json:"operator"`
}

// SaveTaxFiling 登记（或修改）一条申报记录。
func (s *Service) SaveTaxFiling(ctx context.Context, in TaxFilingInput) (*TaxFilingListView, error) {
	k := period.NewKey(in.Year, in.Month)
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", in.Year, in.Month)
	}
	kind := taxfiling.Kind(strings.TrimSpace(in.Kind))
	if !kind.Valid() {
		return nil, fmt.Errorf("%w: %q", taxfiling.ErrBadKind, in.Kind)
	}
	by := strings.TrimSpace(in.Operator)
	if by == "" {
		return nil, errors.New("请填写经办人 —— 申报台账要记清是谁办的")
	}

	// ★ 改一条已有记录时，属期与税种以**数据库里的那条**为准。
	//
	// 界面上的「改」按钮在全年列表里，而页面顶部的期间标签可能是别的月份：
	// 原来拿入参的属期去校验申报日期，于是改 1 月的记录会被报
	// 「申报日期早于 6 月月末」—— 明明没错却不让人改。
	year, month := in.Year, in.Month
	if in.ID != 0 {
		cur, err := s.db.TaxFilings().FilingByID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if cur.Year != in.Year || cur.Month != in.Month || cur.Kind != kind {
			return nil, fmt.Errorf(
				"这条申报记录的属期是 %s %s，不能改成 %s %s —— "+
					"属期与税种定了就不能改（改了就是另一条记录）。"+
					"要改请作废这一条，再按正确属期登记。",
				cur.Kind.Label(), cur.Period(), kind.Label(), period.NewKey(in.Year, in.Month))
		}
		// ★ 已作废的记录不能改回来，金额快照也不能改。
		//
		// 否则「作废留痕」就是一句空话：拿同一个 id 再存一次，
		// 记录就复活成「已申报」，voided_by / void_reason 被清空、
		// 金额被覆盖 —— 发布前审计实测过这条路径。
		// 更正申报的正确做法是「作废 + 新登记」，两条都留在台账里。
		if cur.Status == taxfiling.StatusVoid {
			return nil, fmt.Errorf(
				"这条申报记录已经作废了（%s 作废，原因：%s）—— "+
					"作废的记录不能改回来。更正申报请**新登记**一条，两条都会留在台账里。",
				cur.VoidedAt, cur.VoidReason)
		}
		if in.Payable != 0 || in.TaxAmount != 0 || in.Surcharge != 0 || in.Paid != 0 {
			// 金额只能来自「按当前计算表填」或与记录里已有的一致
			ret, rerr := s.taxReturnRaw(ctx, kind, cur.Period(), TaxReturnInput{
				Kind: string(kind), Year: cur.Year, Month: cur.Month,
			})
			if rerr != nil {
				return nil, rerr
			}
			same := (in.Payable == 0 || in.Payable == cur.Payable) &&
				(in.TaxAmount == 0 || in.TaxAmount == cur.TaxAmount) &&
				(in.Surcharge == 0 || in.Surcharge == cur.Surcharge) &&
				(in.Paid == 0 || in.Paid == cur.Paid)
			if !same && (in.Payable != ret.Payable || in.TaxAmount != ret.Tax) {
				return nil, fmt.Errorf(
					"申报记录里的金额是**申报当时的快照**，不能直接改（现在这条记的是 "+
						"应补(退) %s）—— 账改了就该更正申报：先作废这一条，再按当期数登记新的。",
					cur.Payable)
			}
			in.Payable, in.TaxAmount, in.Surcharge, in.Paid =
				cur.Payable, cur.TaxAmount, cur.Surcharge, cur.Paid
		} else {
			in.Payable, in.TaxAmount, in.Surcharge, in.Paid =
				cur.Payable, cur.TaxAmount, cur.Surcharge, cur.Paid
		}
		year, month = cur.Year, cur.Month
		k = period.NewKey(year, month)
	}

	f := taxfiling.Filing{
		ID: in.ID, Year: year, Month: month, Kind: kind,
		PeriodLabel: periodLabelOf(kind, k),
		Status:      taxfiling.Status(strings.TrimSpace(in.Status)),
		FiledDate:   strings.TrimSpace(in.FiledDate),
		PaidDate:    strings.TrimSpace(in.PaidDate),
		Payable:     in.Payable, TaxAmount: in.TaxAmount, Surcharge: in.Surcharge,
		Paid:    in.Paid,
		Channel: strings.TrimSpace(in.Channel), ReceiptNo: strings.TrimSpace(in.ReceiptNo),
		Operator: by, Note: strings.TrimSpace(in.Note),
	}
	if f.Status == "" {
		f.Status = taxfiling.StatusFiled
	}
	// ★ 金额默认按**当前计算表**填。
	//
	// 让人手抄一遍应补税额，是这类台账最常见的错源：
	// 抄错一位数，台账与账就此分叉，而两条记录看起来都正常。
	//
	// 四个分量（税额 / 附加 / 已缴 / 应补退）全部直接取计算表给的值，
	// **不按标签去猜**：改一个措辞就会静默取到 0，
	// 那时「税额 + 附加 − 已缴 = 应补退」这条校验就变成了永远通过。
	// ★ 「没填」与「显式填 0」是两回事：前者按当前计算表填，后者是用户的
	// 零申报事实，不能被覆盖（台账记的是发生过的事）。
	if in.FromCurrent || (!in.ManualAmounts && f.Payable.IsZero() && f.TaxAmount.IsZero() &&
		f.Surcharge.IsZero() && f.Paid.IsZero()) {
		ret, err := s.taxReturnRaw(ctx, kind, k, TaxReturnInput{
			Kind: string(kind), Year: in.Year, Month: in.Month,
		})
		if err != nil {
			return nil, err
		}
		f.Payable = ret.Payable
		f.TaxAmount = ret.Tax
		f.Surcharge = ret.Surcharge
		f.Paid = ret.Paid
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}

	// 已登记过的（有效记录）不能再存一条：更正要先作废
	if f.ID == 0 {
		old, err := s.db.TaxFilings().Filing(ctx, kind, k)
		if err != nil {
			return nil, err
		}
		if old != nil {
			return nil, fmt.Errorf(
				"%s %s 已经登记过申报了（%s，应补(退) %s）—— "+
					"同一属期同一税种只能有一条有效记录。要更正请先作废那条，"+
					"再登记新的（作废记录会留下痕迹）。",
				kind.Label(), k, old.FiledDate, old.Payable)
		}
	}

	id, err := s.db.TaxFilings().SaveFiling(ctx, f)
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionTaxFilingSave,
		Summary: fmt.Sprintf("登记%s申报：%s %s", kind.Label(), k, f.Status.Label()),
		Entity:  "tax_filing", EntityID: fmt.Sprintf("%d", id),
		Operator: by,
		Detail: map[string]any{
			"税种": kind.Label(), "属期": k.String(), "状态": f.Status.Label(),
			"申报日期": f.FiledDate, "缴款日期": f.PaidDate,
			"应补(退)税额": f.Payable.String(),
			"其中税额":    f.TaxAmount.String(),
			"附加税费":    f.Surcharge.String(),
			"本期已缴":    f.Paid.String(),
			"申报渠道":    f.Channel, "回执号": f.ReceiptNo, "备注": f.Note,
		},
	})
	return s.TaxFilings(ctx, in.Year)
}

// VoidTaxFiling 作废一条申报记录（更正申报的第一步）。
func (s *Service) VoidTaxFiling(ctx context.Context, id int64, by, reason string) (*TaxFilingListView, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		return nil, errors.New("请填写经办人 —— 作废申报记录要留痕")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, errors.New("请写明作废原因 —— 更正申报的过程也要能追溯")
	}
	f, err := s.db.TaxFilings().FilingByID(ctx, id)
	if err != nil {
		return nil, err
	}
	at := time.Now().UTC().Format("2006-01-02")
	if err := s.db.TaxFilings().VoidFiling(ctx, id, by, at, reason); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionTaxFilingVoid,
		Summary: fmt.Sprintf("作废申报记录：%s", f.Summary()),
		Entity:  "tax_filing", EntityID: fmt.Sprintf("%d", id),
		Operator: by,
		Detail: map[string]any{
			"税种": f.Kind.Label(), "属期": f.Period().String(),
			"原申报日期": f.FiledDate, "原应补(退)税额": f.Payable.String(),
			"作废原因": reason, "作废日期": at,
		},
	})
	return s.TaxFilings(ctx, f.Year)
}

// TaxFilings 返回某年的申报台账。
func (s *Service) TaxFilings(ctx context.Context, year int) (*TaxFilingListView, error) {
	if year < 1900 || year > 9999 {
		return nil, fmt.Errorf("年度 %d 非法", year)
	}
	rows, err := s.db.TaxFilings().FilingsOfYear(ctx, year)
	if err != nil {
		return nil, err
	}
	out := &TaxFilingListView{Year: year, Items: []TaxFilingView{}, Pending: []TaxFilingPendingView{}}
	for _, f := range rows {
		v := s.taxFilingView(ctx, f)
		out.Items = append(out.Items, v)
		if f.Status == taxfiling.StatusVoid {
			out.Voided++
		} else {
			out.Effective++
		}
	}

	// 本年**已经过完**、且账套已启用的期间，还没有申报记录 → 提醒
	//
	// ★ 三个过滤条件缺一不可（发布前审计抓到过：原来是 1..12 月 × 3 税种全列）：
	//   1. 期间已启用（未启用的未来期间当然还没到申报的时候）；
	//   2. 期间**已经过完**（月末早于今天）——当月还没结束就谈不上申报；
	//   3. 企业所得税只有 4 个季度（3/6/9/12 月），按 12 个月列会多出 8 条。
	book, err := s.db.Books().Get(ctx)
	if err == nil {
		status := map[string]period.Status{}
		if info, berr := s.Book(ctx); berr == nil {
			for _, pi := range info.Periods {
				status[pi.Label] = period.Status(pi.Status)
			}
		}
		today := calendar.Today()
		for m := 1; m <= 12; m++ {
			k := period.NewKey(year, m)
			if k.Before(book.StartPeriod()) {
				continue // 期初之前：账套还没启用
			}
			if st, ok := status[k.String()]; ok && st == period.StatusFuture {
				continue // 未启用的未来期间
			}
			if !endOf(k).Before(today) {
				continue // 本期还没过完
			}
			for _, kind := range taxreturn.AllKinds {
				// 企业所得税按季申报：只有季末那个月才有申报
				if kind == taxreturn.KindCIT && k.Month%3 != 0 {
					continue
				}
				f, err := s.db.TaxFilings().Filing(ctx, kind, k)
				if err != nil {
					continue
				}
				if f != nil {
					continue
				}
				ret, err := s.taxReturnRaw(ctx, kind, k, TaxReturnInput{
					Kind: string(kind), Year: year, Month: m,
				})
				if err != nil {
					continue // 算不出来就不提醒，而不是报错挡住整张台账
				}
				out.Pending = append(out.Pending, TaxFilingPendingView{
					Period: k.String(), Kind: string(kind), KindLabel: kind.Label(),
					Payable: ret.Payable,
					Hint: fmt.Sprintf("本期算出来应补(退) %s，还没有登记申报记录；"+
						"报完之后到这一页登记，台账才能回答「这期报了没」。", ret.Payable),
				})
			}
		}
	}
	out.Concludes = concludeFilings(out)
	return out, nil
}

// taxFilingView 把一条记录翻成界面形状，并**现算**勾稽差异。
func (s *Service) taxFilingView(ctx context.Context, f taxfiling.Filing) TaxFilingView {
	v := TaxFilingView{
		ID: f.ID, Year: f.Year, Month: f.Month, Period: f.Period().String(),
		PeriodLabel: f.PeriodLabel, Kind: string(f.Kind), KindLabel: f.Kind.Label(),
		Status: string(f.Status), StatusLabel: f.Status.Label(),
		FiledDate: f.FiledDate, PaidDate: f.PaidDate,
		Payable: f.Payable, TaxAmount: f.TaxAmount, Surcharge: f.Surcharge,
		Paid:    f.Paid,
		Channel: f.Channel, ReceiptNo: f.ReceiptNo, Operator: f.Operator, Note: f.Note,
		VoidedBy: f.VoidedBy, VoidedAt: f.VoidedAt, VoidReason: f.VoidReason,
		Summary: f.Summary(),
	}
	// 勾稽：拿台账里的数与现在算出来的表比。
	// ★ 走 taxReturnRaw 而不是 TaxReturn：后者会回头来查申报状态，
	// 而这里正在处理申报记录 —— 绕成环会直接爆栈。
	ret, err := s.taxReturnRaw(ctx, f.Kind, f.Period(), TaxReturnInput{
		Kind: string(f.Kind), Year: f.Year, Month: f.Month,
	})
	if err != nil {
		v.Reconcile = "当前计算表算不出来，无法勾稽：" + err.Error()
		return v
	}
	v.Computed = ret.Payable
	diff, msg := f.Reconcile(ret.Payable)
	v.Diff = diff
	v.Reconcile = msg
	return v
}

// FilingOf 返回某属期某税种的有效申报记录（可能为 nil）。
func (s *Service) FilingOf(ctx context.Context, kind taxfiling.Kind,
	k period.Key) (*taxfiling.Filing, error) {
	return s.db.TaxFilings().Filing(ctx, kind, k)
}

// periodLabelOf 给出申报属期的文字（企业所得税按季度说）。
func periodLabelOf(kind taxfiling.Kind, k period.Key) string {
	if kind == taxfiling.KindCIT {
		return taxreturn.CITPeriodLabel(k.Year, k.Month)
	}
	return k.String()
}

// concludeFilings 给出台账的一句话总结。
func concludeFilings(v *TaxFilingListView) string {
	switch {
	case v.Effective == 0 && len(v.Pending) == 0:
		return "本年还没有申报记录，也没有需要登记的期间。"
	case v.Effective == 0:
		return fmt.Sprintf("本年还没有登记任何申报记录，有 %d 项待申报 —— "+
			"报完之后到这一页登记，台账才能回答「这期报了没」。", len(v.Pending))
	case len(v.Pending) > 0:
		return fmt.Sprintf("本年已登记 %d 条申报（其中作废 %d 条），"+
			"另有 %d 项还没有登记。", v.Effective, v.Voided, len(v.Pending))
	default:
		return fmt.Sprintf("本年已登记 %d 条申报（其中作废 %d 条），没有待登记的期间。",
			v.Effective, v.Voided)
	}
}

// TaxFilingKindOption 是税种选项。
type TaxFilingKindOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// TaxFilingKindOptions 返回三种税。
func (s *Service) TaxFilingKindOptions() []TaxFilingKindOption {
	out := make([]TaxFilingKindOption, 0, len(taxreturn.AllKinds))
	for _, k := range taxreturn.AllKinds {
		out = append(out, TaxFilingKindOption{Value: string(k), Label: k.Label()})
	}
	return out
}

// ---- 让申报状态出现在计算表上 ----

// filingStatusOf 查某期某税种的申报状态，并算出勾稽结论。
//
// ★ 计算表上必须显示「这期报了没」。
//
// 只给一张算得很漂亮的表、却不告诉用户「这一期其实已经报过了」，
// 用户很可能照着它再报一次 —— 而重复申报的更正很麻烦。
func (s *Service) filingStatusOf(ctx context.Context, kind taxreturn.Kind,
	k period.Key) (*TaxFilingView, string) {

	f, err := s.db.TaxFilings().Filing(ctx, taxfiling.Kind(kind), k)
	if err != nil || f == nil {
		return nil, "本期还没有登记申报记录：报完之后到「申报台账」登记，" +
			"台账才能回答「这期报了没」。"
	}
	v := s.taxFilingView(ctx, *f)
	return &v, v.Reconcile
}
