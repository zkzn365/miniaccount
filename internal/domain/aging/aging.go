// Package aging 生成应收/应付账龄分析表。
//
// # 账龄怎么算
//
// 账龄不是「发票开了多久」，而是**每一笔还没收（付）回来的钱，
// 从它发生那天到截止日有多久**。
//
// 因此算法分两步：
//
//  1. **核销（settlement）**：把同一往来单位在同一科目下的收款
//     逐笔冲抵最早的应收 —— 这就是会计上说的「先进先出法核销」。
//     不核销直接按发生额分桶是错的：客户三个月前欠的 10 万
//     上个月已经付了，那笔早就不是「90 天以上」了。
//  2. **分桶**：核销后仍未收（付）的部分，按发生日期距截止日的天数
//     落进 30/60/90/180/1年+ 六个桶。
//
// # 为什么按「往来单位 × 科目」而不是只按往来单位
//
// 同一个客户可能既有应收账款（卖货）又有预收账款（先收钱），
// 两者的账龄含义完全不同。混在一起算出来的数字没法用。
package aging

import (
	"errors"
	"fmt"
	"sort"

	"miniaccount/internal/domain/account"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// 账龄相关错误。
var (
	ErrBadDate = errors.New("aging: 截止日期无效")
)

// Bucket 是一个账龄区间。
type Bucket struct {
	// Label 是区间名，如「30 天以内」「91—180 天」。
	Label string
	// MinDays / MaxDays 是天数区间（含两端）；MaxDays 为 0 表示无上限。
	MinDays, MaxDays int
	// Amount 是落在这个区间的未核销金额。
	Amount money.Money
}

// DefaultBuckets 返回按中国实务惯例的账龄区间。
//
// 分界点 30/90/180/365 不是随便定的：
// 它们大致对应「一个账期」「一个季度」「半年」「一年」，
// 也是小企业评估回款风险时最常用的档位。
func DefaultBuckets() []Bucket {
	return []Bucket{
		{Label: "30 天以内", MinDays: 0, MaxDays: 30},
		{Label: "31—60 天", MinDays: 31, MaxDays: 60},
		{Label: "61—90 天", MinDays: 61, MaxDays: 90},
		{Label: "91—180 天", MinDays: 91, MaxDays: 180},
		{Label: "181—365 天", MinDays: 181, MaxDays: 365},
		{Label: "1 年以上", MinDays: 366, MaxDays: 0},
	}
}

// Item 是一笔发生额。
type Item struct {
	Date      calendar.Date
	Amount    money.Money
	Summary   string
	VoucherNo string
}

// OpenItem 是一笔核销后仍未结清的账。
type OpenItem struct {
	Date      calendar.Date
	Amount    money.Money
	Summary   string
	VoucherNo string
	// Days 是距截止日的天数。
	Days int
	// Bucket 是它落进的区间下标。
	Bucket int
}

// ContactAging 是一个「往来单位 × 科目」的账龄。
type ContactAging struct {
	ContactID   int64
	ContactName string
	AccountCode string
	AccountName string
	// Debits / Credits 是该组合下的全部发生额，供下钻核对。
	Debits, Credits money.Money
	// Balance 是未核销余额（借为正、贷为负）。
	Balance money.Money
	// Items 是核销后仍未结清的明细，按天数从多到少。
	Items []OpenItem
	// Buckets 是各账龄区间的金额。
	Buckets []Bucket
}

// Report 是账龄分析表。
type Report struct {
	AsOf calendar.Date
	// Rows 按「科目 → 未结清金额」排序。
	Rows []ContactAging
	// Buckets 是各区间合计。
	Buckets []Bucket
	// Total 是未核销余额合计（借正贷负）。
	Total money.Money
	// DebitTotal / CreditTotal 分别是借方与贷方的未核销合计。
	//
	// 应收类科目看 DebitTotal，应付类看 CreditTotal ——
	// 同一个往来单位可能既有应收又有应付（预收/预付），
	// 分开列比一个净额更有用。
	DebitTotal, CreditTotal money.Money
}

// Entry 是一条参与账龄计算的发生额。
type Entry struct {
	ContactID   int64
	AccountCode string
	Date        calendar.Date
	// Debit / Credit 恰有一个为正。
	Debit     money.Money
	Credit    money.Money
	Summary   string
	VoucherNo string
}

// Input 是一次账龄分析的全部输入。
type Input struct {
	AsOf calendar.Date
	// Entries 是全部参与计算的发生额（任意顺序）。
	Entries []Entry
	// Accounts 提供科目名称与余额方向。
	Accounts *account.Tree
	// ContactNames 是往来单位 id → 名称。
	ContactNames map[int64]string
	// Buckets 留空时用 DefaultBuckets()。
	Buckets []Bucket
}

// Build 生成账龄分析表。
//
// 核销规则（先进先出）：
//
//	先形成的那一方排队；每一笔反向发生额依次冲抵队首，冲完为止。
//	冲不完的部分留在队里，就是「还没收回来的钱」。
//
// 方向由**科目自身的余额方向**决定，而不是由「哪个字段叫 debit」决定 ——
// 否则应付账款的账龄会全算反。
func Build(in Input) (*Report, error) {
	if !in.AsOf.Valid() {
		return nil, ErrBadDate
	}
	buckets := in.Buckets
	if len(buckets) == 0 {
		buckets = DefaultBuckets()
	}

	type key struct {
		contact int64
		account string
	}
	groups := map[key][]Entry{}
	var order []key
	for _, e := range in.Entries {
		// 截止日之后的业务不算 —— 账龄表是**时点**报表
		if e.Date.After(in.AsOf) {
			continue
		}
		k := key{e.ContactID, e.AccountCode}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e)
	}

	rep := &Report{AsOf: in.AsOf, Buckets: cloneBuckets(buckets)}
	for _, k := range order {
		entries := groups[k]
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].Date.Before(entries[j].Date)
		})

		row := ContactAging{
			ContactID:   k.contact,
			ContactName: in.ContactNames[k.contact],
			AccountCode: k.account,
			Buckets:     cloneBuckets(buckets),
		}
		if in.Accounts != nil {
			if a, ok := in.Accounts.Get(k.account); ok {
				row.AccountName = a.Name
			}
		}

		var debits, credits []Item
		for _, e := range entries {
			row.Debits = row.Debits.Add(e.Debit)
			row.Credits = row.Credits.Add(e.Credit)
			switch {
			case e.Debit.IsPositive():
				debits = append(debits, Item{e.Date, e.Debit, e.Summary, e.VoucherNo})
			case e.Credit.IsPositive():
				credits = append(credits, Item{e.Date, e.Credit, e.Summary, e.VoucherNo})
			}
		}
		row.Balance = row.Debits.Sub(row.Credits)

		// 应收/预付（借方科目）：借方形成、贷方冲抵；应付/预收则相反。
		primary, offset := debits, credits
		if in.Accounts != nil {
			if a, ok := in.Accounts.Get(k.account); ok && a.BalanceDir == account.DirCredit {
				primary, offset = credits, debits
			}
		}

		row.Items = settle(primary, offset, in.AsOf, buckets)
		for _, it := range row.Items {
			row.Buckets[it.Bucket].Amount = row.Buckets[it.Bucket].Amount.Add(it.Amount)
			rep.Buckets[it.Bucket].Amount = rep.Buckets[it.Bucket].Amount.Add(it.Amount)
		}
		rep.Total = rep.Total.Add(row.Balance)
		if row.Balance.IsNegative() {
			rep.CreditTotal = rep.CreditTotal.Add(row.Balance.Abs())
		} else {
			rep.DebitTotal = rep.DebitTotal.Add(row.Balance)
		}
		rep.Rows = append(rep.Rows, row)
	}

	// 先按科目，再按未结清金额从大到小 ——
	// 会计最先要看的是「哪个客户欠得最多」
	sort.SliceStable(rep.Rows, func(i, j int) bool {
		if rep.Rows[i].AccountCode != rep.Rows[j].AccountCode {
			return rep.Rows[i].AccountCode < rep.Rows[j].AccountCode
		}
		return rep.Rows[i].Balance.Abs() > rep.Rows[j].Balance.Abs()
	})
	return rep, nil
}

// settle 用 offset 逐笔冲抵 primary（先进先出），返回仍未结清的明细。
func settle(primary, offset []Item, asOf calendar.Date, buckets []Bucket) []OpenItem {
	remain := make([]money.Money, len(primary))
	for i, p := range primary {
		remain[i] = p.Amount
	}

	for _, o := range offset {
		left := o.Amount
		for i := range primary {
			if !left.IsPositive() {
				break
			}
			if !remain[i].IsPositive() {
				continue
			}
			take := left
			if remain[i] < take {
				take = remain[i]
			}
			remain[i] = remain[i].Sub(take)
			left = left.Sub(take)
		}
		// 冲抵完还有剩，说明收得比欠得多 —— 多出来的是预收。
		// 不作特殊处理：它会让该组合的净额变成反向，
		// 由 Balance 的正负如实反映，而不是硬塞进某个账龄桶。
	}

	var out []OpenItem
	for i, p := range primary {
		if !remain[i].IsPositive() {
			continue
		}
		days := DaysBetween(p.Date, asOf)
		out = append(out, OpenItem{
			Date: p.Date, Amount: remain[i], Summary: p.Summary,
			VoucherNo: p.VoucherNo, Days: days, Bucket: bucketOf(days, buckets),
		})
	}
	// 未结清明细按天数从多到少 —— 最该催的排最前
	sort.SliceStable(out, func(i, j int) bool { return out[i].Days > out[j].Days })
	return out
}

func bucketOf(days int, buckets []Bucket) int {
	for i, b := range buckets {
		if days < b.MinDays {
			continue
		}
		if b.MaxDays == 0 || days <= b.MaxDays {
			return i
		}
	}
	return len(buckets) - 1
}

func cloneBuckets(in []Bucket) []Bucket {
	out := make([]Bucket, len(in))
	copy(out, in)
	return out
}

// DaysBetween 返回 from 到 to 的天数差（to 更晚为正）。
//
// 用儒略日序号相减：calendar.Date 是纯日期，没有时刻概念，
// 引入 time.Duration 只会把日期重新拖回时区问题里。
func DaysBetween(from, to calendar.Date) int {
	return dayNumber(to) - dayNumber(from)
}

func dayNumber(d calendar.Date) int {
	y, m, dd := d.Year, d.Month, d.Day
	if m <= 2 {
		y--
	}
	era := y / 400
	if y < 0 {
		era = (y - 399) / 400
	}
	yoe := y - era*400
	mp := (m + 9) % 12
	doy := (153*mp+2)/5 + dd - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return era*146097 + doe
}

// Summary 返回一句话概览。
func (r *Report) Summary() string {
	if r == nil {
		return ""
	}
	var over90 money.Money
	for _, b := range r.Buckets {
		if b.MinDays > 90 {
			over90 = over90.Add(b.Amount)
		}
	}
	return fmt.Sprintf("%s 应收 %s，应付 %s；其中 90 天以上 %s",
		r.AsOf, r.DebitTotal, r.CreditTotal, over90)
}
