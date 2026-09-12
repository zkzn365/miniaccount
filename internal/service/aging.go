package service

import (
	"context"
	"fmt"
	"strings"

	"miniaccount/internal/domain/aging"
	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// ---------------------------------------------------------------------------
// 账龄分析
// ---------------------------------------------------------------------------

// AgingRequest 是账龄分析表的参数。
type AgingRequest struct {
	// AsOf 是截止日期（YYYY-MM-DD）；留空取当前可记账期间的期末。
	AsOf string
	// AccountPrefix 限定科目范围：1122 只看应收、2202 只看应付、空为全部。
	AccountPrefix string
}

// AgingBucketView 是一个账龄区间。
type AgingBucketView struct {
	Label  string      `json:"label"`
	Amount money.Money `json:"amount"`
	// Percent 是该区间占未结清总额的百分比（0—100，保留一位小数）。
	//
	// 界面按它画占比条 —— 会计最关心的是「多少比例的钱压了三个月以上」，
	// 而这个比例看绝对金额看不出来。
	Percent float64 `json:"percent"`
}

// AgingItemView 是一笔未结清明细。
type AgingItemView struct {
	Date      string      `json:"date"`
	Days      int         `json:"days"`
	Amount    money.Money `json:"amount"`
	Summary   string      `json:"summary"`
	VoucherNo string      `json:"voucherNo"`
	// BucketLabel 是它落进的区间名，便于界面着色。
	BucketLabel string `json:"bucketLabel"`
}

// AgingRowView 是一行「往来单位 × 科目」。
type AgingRowView struct {
	ContactID   int64  `json:"contactId"`
	ContactName string `json:"contactName"`
	AccountCode string `json:"accountCode"`
	AccountName string `json:"accountName"`
	// Debits / Credits 是本期发生额合计，供核对。
	Debits  money.Money `json:"debits"`
	Credits money.Money `json:"credits"`
	// Balance 是未核销余额（借正贷负）。
	Balance money.Money `json:"balance"`
	// Buckets 是各区间金额。
	Buckets []AgingBucketView `json:"buckets"`
	// Items 是未结清明细，按天数从多到少。
	Items []AgingItemView `json:"items"`
	// MaxDays 是最老一笔的账龄，便于界面排序与标红。
	MaxDays int `json:"maxDays"`
}

// AgingView 是账龄分析表。
type AgingView struct {
	AsOf string `json:"asOf"`
	// Buckets 是各区间合计。
	Buckets []AgingBucketView `json:"buckets"`
	Rows    []AgingRowView    `json:"rows"`
	// Total / DebitTotal / CreditTotal 见 aging.Report。
	Total       money.Money `json:"total"`
	DebitTotal  money.Money `json:"debitTotal"`
	CreditTotal money.Money `json:"creditTotal"`
	// Summary 是一句话概览。
	Summary string `json:"summary"`
	// Over90 是 90 天以上的未结清金额 —— 最该催的那部分。
	Over90 money.Money `json:"over90"`
}

// AgingReport 生成账龄分析表。
func (s *Service) AgingReport(ctx context.Context, req AgingRequest) (*AgingView, error) {
	asOf, err := s.resolveAgingDate(ctx, req.AsOf)
	if err != nil {
		return nil, err
	}
	s.recordView(ctx, AuditEvent{
		Action: audit.ActionViewAging, Entity: "report",
		EntityID: "aging/" + asOf.String(),
		Summary:  "查看账龄分析（截至 " + asOf.String() + "）",
		Detail:   map[string]any{"科目范围": req.AccountPrefix, "截止日": asOf.String()},
	})
	rep, err := s.db.Aging().BuildAging(ctx, asOf, strings.TrimSpace(req.AccountPrefix))
	if err != nil {
		return nil, err
	}
	return toAgingView(rep), nil
}

func (s *Service) resolveAgingDate(ctx context.Context, s2 string) (calendar.Date, error) {
	if strings.TrimSpace(s2) != "" {
		d, err := parseDate(strings.TrimSpace(s2))
		if err != nil {
			return calendar.Date{}, fmt.Errorf("截止日期 %q 格式不对，应为 YYYY-MM-DD", s2)
		}
		return d, nil
	}
	// 默认取当前可记账期间的期末 —— 与首页、报表的默认口径一致
	book, err := s.Book(ctx)
	if err != nil {
		return calendar.Date{}, err
	}
	for _, p := range book.Periods {
		if p.Status == "open" {
			return parseDate(p.To)
		}
	}
	if len(book.Periods) > 0 {
		return parseDate(book.Periods[len(book.Periods)-1].To)
	}
	return todayDate(), nil
}

func toAgingView(rep *aging.Report) *AgingView {
	// ★ 预置成空切片：没有往来余额时这两个字段是 nil，
	// 而 Go 会把 nil 编成 JSON 的 null —— 界面拿它当数组用就崩。
	// 空账套（还没有任何往来）恰好是这个状态。
	out := &AgingView{
		AsOf: rep.AsOf.String(), Total: rep.Total,
		DebitTotal: rep.DebitTotal, CreditTotal: rep.CreditTotal,
		Summary: rep.Summary(),
		Buckets: []AgingBucketView{},
		Rows:    []AgingRowView{},
	}
	for _, b := range rep.Buckets {
		out.Buckets = append(out.Buckets, AgingBucketView{
			Label: b.Label, Amount: b.Amount, Percent: percentOf(b.Amount, rep),
		})
		if b.MinDays > 90 {
			out.Over90 = out.Over90.Add(b.Amount)
		}
	}
	for _, r := range rep.Rows {
		row := AgingRowView{
			ContactID: r.ContactID, ContactName: r.ContactName,
			AccountCode: r.AccountCode, AccountName: r.AccountName,
			Debits: r.Debits, Credits: r.Credits, Balance: r.Balance,
			// ★ 行内的两个数组也要预置。某一行恰好没有明细（全额核销完、
			// 只剩一个余额方向）时它们是 nil → JSON 里是 null →
			// 界面拿它当数组用就崩。空账套到不了这个分支
			//（没有行），只有有数据的账套才走得出来。
			Buckets: []AgingBucketView{},
			Items:   []AgingItemView{},
		}
		if row.ContactName == "" {
			row.ContactName = fmt.Sprintf("往来#%d", r.ContactID)
		}
		for _, b := range r.Buckets {
			row.Buckets = append(row.Buckets, AgingBucketView{
				Label: b.Label, Amount: b.Amount,
				Percent: percentOf(b.Amount, rep),
			})
		}
		for _, it := range r.Items {
			label := ""
			if it.Bucket >= 0 && it.Bucket < len(rep.Buckets) {
				label = rep.Buckets[it.Bucket].Label
			}
			row.Items = append(row.Items, AgingItemView{
				Date: it.Date.String(), Days: it.Days, Amount: it.Amount,
				Summary: it.Summary, VoucherNo: it.VoucherNo, BucketLabel: label,
			})
			if it.Days > row.MaxDays {
				row.MaxDays = it.Days
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

// percentOf 计算某个桶占未结清总额的比例。
//
// 分母用「全部未结清金额之和」而不是净额：净额在应收应付并存时
// 会被互相抵消，算出来的比例可能大于 100%。
func percentOf(amount money.Money, rep *aging.Report) float64 {
	var sum money.Money
	for _, b := range rep.Buckets {
		sum = sum.Add(b.Amount)
	}
	if sum <= 0 {
		return 0
	}
	// 用整数运算再转浮点，避免在金额上引入浮点误差
	p := float64(amount) / float64(sum) * 100
	return float64(int(p*10+0.5)) / 10
}
