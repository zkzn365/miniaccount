// Package summary 生成凭证汇总表。
//
// # 这张表解决什么
//
// 记账凭证是一张一张的。月底会计要回答的问题却是成批的：
//
//	这个月一共记了多少张凭证？借贷各多少钱？
//	各凭证字（记/收/付/转）分别多少张？
//	哪几天没记账？
//	每个科目的本期借贷发生额是多少？（→ 科目汇总表，据以登总账）
//
// 一个一个数是不现实的。凭证汇总表把同一批凭证从**三个角度**各汇总一遍：
//
//	按凭证字  记 12 张、收 5 张、付 8 张、转 3 张
//	按日期    03-05 有 3 张、03-06 一张都没有 …
//	按科目    1002 借方 150,000 / 贷方 12,000 …
//
// # 三个角度为什么放在一张表里
//
// 因为它们汇总的是**同一批数据**，合计必须完全相等。于是它们互为验算：
// 按凭证字加起来是 28 张、按日期加起来也是 28 张、按科目算出来的
// 借方合计与按凭证字算出来的一模一样 —— 任何一处漏算、重复算、
// 口径不一致，这组等式立刻就破。
//
// 这比「只给一个合计」有用得多：会计拿着一屏数字去核对总账时，
// 需要的是一个**能自证的**数，而不是一个要无条件相信的数。
//
// # 与科目余额表的区别
//
//	科目余额表  按科目列六栏（期初/本期/期末 × 借贷），带余额
//	科目汇总表  只列本期借贷发生额，不带余额，用于登总账
//
// 后者是「科目汇总表账务处理程序」的核心：先编科目汇总表试算平衡，
// 再据以登记总账。两者数据同源，但用途不同，所以都保留。
package summary

import (
	"errors"
	"fmt"
	"sort"

	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
)

// 凭证汇总表相关错误。
var (
	ErrBadRange   = errors.New("summary: 期间非法")
	ErrNoVouchers = errors.New("summary: 没有凭证")
	ErrBadVoucher = errors.New("summary: 凭证数据非法")
)

// Entry 是一条分录（只保留汇总需要的字段）。
type Entry struct {
	AccountCode string
	Debit       money.Money
	Credit      money.Money
}

// Voucher 是一张已记账凭证。
type Voucher struct {
	// ID 是凭证 id，用于去重与排序。
	ID int64
	// Word 是凭证字：记 / 收 / 付 / 转。
	Word string
	// No 是展示号，如「记-2025-03-0001」。
	No   string
	Date calendar.Date
	// Entries 是这张凭证的全部分录。
	Entries []Entry

	// Voided 为真表示这张凭证已被红字冲销。
	//
	// ★ 它**照样计入汇总** —— 红字冲销模型下，原凭证与红字凭证
	// 都留在账上并都参与汇总，净额自然为零。把它们排除掉
	// 反而会让汇总表和总账对不上。
	Voided bool
	// Reversal 为真表示这张凭证本身就是一张红字冲销凭证。
	Reversal bool
	// Attachments 是附单据数，用于和纸质单据核对。
	Attachments int
}

// AccountInfo 是科目档案里汇总要用到的部分。
//
// 由调用方提供而不是让本包去查库：本包对「科目」的全部认知
// 就是「一个编码对应一个名字」，不该为此依赖存储层。
type AccountInfo struct {
	Code string
	Name string
	// FullName 是含上级的全名，如「管理费用—办公费」。
	FullName string
	// BalanceDir 是科目余额方向（debit/credit）。
	BalanceDir string
}

// Input 是凭证汇总表的输入。
type Input struct {
	From, To calendar.Date

	// Vouchers 是期间内**已记账**的凭证（含已被红字冲销的）。
	Vouchers []Voucher

	// Accounts 提供科目名称；某个编码缺失时退化为只显示编码。
	Accounts map[string]AccountInfo

	// DraftCount 是期间内尚未记账的草稿数。
	//
	// 必须单独告诉用户：草稿不占凭证号、不进总账，
	// 也就**不进这张汇总表**。月底汇总出来 28 张、系统里躺着 3 张草稿，
	// 不提示的话会计会以为账已经记全了。
	DraftCount int
}

// WordRow 是按凭证字汇总的一行。
type WordRow struct {
	// Word 是凭证字：记/收/付/转。
	Word  string
	Count int
	// Debit / Credit 是该凭证字下的借贷发生额合计。
	Debit, Credit money.Money
}

// DayRow 是按日期汇总的一行。
type DayRow struct {
	Date  calendar.Date
	Count int
	// Nos 是这一天的凭证号，便于顺着往下查。
	Nos           []string
	Debit, Credit money.Money
}

// AccountRow 是按科目汇总的一行（科目汇总表）。
type AccountRow struct {
	AccountCode string
	AccountName string
	FullName    string
	// Debit / Credit 是该科目本期的借贷发生额。
	Debit, Credit money.Money
	// Count 是该科目本期的分录条数（即涉及多少笔业务）。
	//
	// 不是「凭证张数」：一张凭证可能同时有该科目的借方与贷方两条分录
	// （如银行存款一收一付），那种情况下这张凭证对该科目算了两次业务，
	// 而正是它值得被看见。
	Count int
}

// Net 返回该科目的净发生额（借 − 贷）。
func (r AccountRow) Net() money.Money { return r.Debit.Sub(r.Credit) }

// Report 是一张凭证汇总表。
type Report struct {
	From, To calendar.Date

	WordRows    []WordRow
	DayRows     []DayRow
	AccountRows []AccountRow

	// VoucherCount 是计入汇总的凭证张数。
	VoucherCount int
	DebitTotal   money.Money
	CreditTotal  money.Money

	// DraftCount 是未记账的草稿数（不计入上面的合计）。
	DraftCount int
	// VoidedCount 是其中已被红字冲销的凭证数。
	VoidedCount int
	// ReversalCount 是其中本身为红字冲销凭证的张数。
	ReversalCount int
	// AttachmentTotal 是附单据数合计。
	AttachmentTotal int

	Notes []string
}

// Build 生成凭证汇总表。
func Build(in Input) (*Report, error) {
	if !in.From.Valid() || !in.To.Valid() || in.From.After(in.To) {
		return nil, ErrBadRange
	}

	r := &Report{
		From: in.From, To: in.To,
		DraftCount: in.DraftCount,
	}

	// three 分别累计三个角度，最后必须完全相等
	var wordDebit, wordCredit, dayDebit, dayCredit, acctDebit, acctCredit money.Money

	wordIdx := map[string]int{}
	dayIdx := map[string]int{}
	acctIdx := map[string]int{}

	seen := map[int64]bool{}
	for _, v := range in.Vouchers {
		if v.ID != 0 {
			if seen[v.ID] {
				// 同一张凭证被传了两次：宁可报错也不要算两遍。
				// 「合计比总账多出来的那部分」是最难查的一类错。
				return nil, fmt.Errorf("%w: 凭证 id=%d 重复", ErrBadVoucher, v.ID)
			}
			seen[v.ID] = true
		}
		if len(v.Entries) == 0 {
			return nil, fmt.Errorf("%w: 凭证 %s 没有分录", ErrBadVoucher, v.No)
		}

		var vDebit, vCredit money.Money
		for _, e := range v.Entries {
			if e.AccountCode == "" {
				return nil, fmt.Errorf("%w: 凭证 %s 有分录缺少科目", ErrBadVoucher, v.No)
			}
			if e.Debit < 0 || e.Credit < 0 {
				return nil, fmt.Errorf("%w: 凭证 %s 出现负金额（余额应恒为非负）",
					ErrBadVoucher, v.No)
			}
			if e.Debit > 0 && e.Credit > 0 {
				return nil, fmt.Errorf("%w: 凭证 %s 同一条分录同时有借贷金额",
					ErrBadVoucher, v.No)
			}
			vDebit = vDebit.Add(e.Debit)
			vCredit = vCredit.Add(e.Credit)

			// 按科目
			if _, ok := acctIdx[e.AccountCode]; !ok {
				info := in.Accounts[e.AccountCode]
				name, full := info.Name, info.FullName
				if name == "" {
					name = e.AccountCode
				}
				if full == "" {
					full = name
				}
				acctIdx[e.AccountCode] = len(r.AccountRows)
				r.AccountRows = append(r.AccountRows, AccountRow{
					AccountCode: e.AccountCode, AccountName: name, FullName: full,
				})
			}
			ai := &r.AccountRows[acctIdx[e.AccountCode]]
			ai.Debit = ai.Debit.Add(e.Debit)
			ai.Credit = ai.Credit.Add(e.Credit)
			ai.Count++
			acctDebit = acctDebit.Add(e.Debit)
			acctCredit = acctCredit.Add(e.Credit)
		}

		// ★ 每张凭证自身必须借贷平衡。
		// 这是整张表的地基：一张不平的凭证混进来，
		// 三个角度的合计会同时被带偏，而且偏得一模一样 ——
		// 互相印证反而看不出问题。所以在这里单独挡住。
		if vDebit != vCredit {
			return nil, fmt.Errorf("%w: 凭证 %s 借贷不平（借 %s / 贷 %s）",
				ErrBadVoucher, v.No, vDebit, vCredit)
		}

		r.VoucherCount++
		r.DebitTotal = r.DebitTotal.Add(vDebit)
		r.CreditTotal = r.CreditTotal.Add(vCredit)
		if v.Voided {
			r.VoidedCount++
		}
		if v.Reversal {
			r.ReversalCount++
		}
		r.AttachmentTotal += v.Attachments

		// 按凭证字
		word := v.Word
		if word == "" {
			word = "记"
		}
		wi, ok := wordIdx[word]
		if !ok {
			wi = len(r.WordRows)
			wordIdx[word] = wi
			r.WordRows = append(r.WordRows, WordRow{Word: word})
		}
		r.WordRows[wi].Count++
		r.WordRows[wi].Debit = r.WordRows[wi].Debit.Add(vDebit)
		r.WordRows[wi].Credit = r.WordRows[wi].Credit.Add(vCredit)
		wordDebit = wordDebit.Add(vDebit)
		wordCredit = wordCredit.Add(vCredit)

		// 按日期
		key := v.Date.String()
		di, ok := dayIdx[key]
		if !ok {
			di = len(r.DayRows)
			dayIdx[key] = di
			r.DayRows = append(r.DayRows, DayRow{Date: v.Date})
		}
		r.DayRows[di].Count++
		if v.No != "" {
			r.DayRows[di].Nos = append(r.DayRows[di].Nos, v.No)
		}
		r.DayRows[di].Debit = r.DayRows[di].Debit.Add(vDebit)
		r.DayRows[di].Credit = r.DayRows[di].Credit.Add(vCredit)
		dayDebit = dayDebit.Add(vDebit)
		dayCredit = dayCredit.Add(vCredit)
	}

	// 凭证字排序：按内置顺序（记/收/付/转），未知的排后面按字典序。
	sort.SliceStable(r.WordRows, func(i, j int) bool {
		return wordOrder(r.WordRows[i].Word) < wordOrder(r.WordRows[j].Word)
	})
	sort.SliceStable(r.DayRows, func(i, j int) bool {
		return r.DayRows[i].Date.Before(r.DayRows[j].Date)
	})
	sort.SliceStable(r.AccountRows, func(i, j int) bool {
		return r.AccountRows[i].AccountCode < r.AccountRows[j].AccountCode
	})

	// 三个角度的合计必须完全相等。不等说明上面某一步漏了或重了，
	// 属于程序 bug —— 报出来而不是照着一个错的数往下走。
	if wordDebit != r.DebitTotal || wordCredit != r.CreditTotal ||
		dayDebit != r.DebitTotal || dayCredit != r.CreditTotal ||
		acctDebit != r.DebitTotal || acctCredit != r.CreditTotal {
		return nil, fmt.Errorf(
			"%w: 三个角度的合计不一致：凭证字 借%s/贷%s、日期 借%s/贷%s、"+
				"科目 借%s/贷%s，总合计 借%s/贷%s",
			ErrBadVoucher, wordDebit, wordCredit, dayDebit, dayCredit,
			acctDebit, acctCredit, r.DebitTotal, r.CreditTotal)
	}

	r.buildNotes()
	return r, nil
}

// wordOrder 给出凭证字的排序权重。
//
// 记/收/付/转 是会计习惯的顺序（也是现金/银行存款收付业务在前、
// 转账业务在后），按字典序排会变成「付、收、转、记」，读起来别扭。
func wordOrder(w string) int {
	switch w {
	case "记":
		return 0
	case "收":
		return 1
	case "付":
		return 2
	case "转":
		return 3
	default:
		return 100
	}
}

func (r *Report) buildNotes() {
	if r.DraftCount > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf(
			"另有 %d 张草稿尚未记账，未计入本表。"+
				"草稿不占凭证号、不进总账，但月底汇总时要记得它们还躺在系统里。",
			r.DraftCount))
	}
	if r.ReversalCount > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf(
			"其中 %d 张是红字冲销凭证。红字凭证本身也是一张正常凭证，"+
				"与被冲的原凭证一起参与汇总，净额自然为零。",
			r.ReversalCount))
	}
	if r.VoidedCount > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf(
			"其中 %d 张已被红字冲销，但**仍计入本表** —— "+
				"把它们排除掉，这张表就和总账对不上了。",
			r.VoidedCount))
	}
	if r.VoucherCount == 0 {
		r.Notes = append(r.Notes, "这一期间没有已记账的凭证。")
	}
}

// DayGapDays 返回期间内「一张凭证都没有」的天数。
//
// 这是这张表最实用的一处：月底会计一眼就能看出哪几天漏记了，
// 而不是等结账体检时才发现某张凭证忘了录。
func (r *Report) DayGapDays() []calendar.Date {
	if r == nil || !r.From.Valid() || !r.To.Valid() {
		return nil
	}
	has := make(map[string]bool, len(r.DayRows))
	for _, d := range r.DayRows {
		has[d.Date.String()] = true
	}
	var gaps []calendar.Date
	for d := r.From; !d.After(r.To); d = d.AddDays(1) {
		if !has[d.String()] {
			gaps = append(gaps, d)
		}
	}
	return gaps
}

// Balanced 报告借方合计是否等于贷方合计。
//
// 复式记账下这个等式必然成立。不成立只有两种可能：
// 账被写坏了，或者这张表算错了。两种都必须当场发现。
func (r *Report) Balanced() bool {
	if r == nil {
		return false
	}
	return r.DebitTotal == r.CreditTotal
}

// Check 返回凭证汇总表自身的不变式问题。
func (r *Report) Check() []error {
	if r == nil {
		return []error{fmt.Errorf("summary: 报表为空")}
	}
	var errs []error

	// 1. 借贷平衡
	if !r.Balanced() {
		errs = append(errs, fmt.Errorf(
			"借贷不平：借 %s ≠ 贷 %s", r.DebitTotal, r.CreditTotal))
	}

	// 2~4. 三个角度的合计都必须等于总合计
	checkGroup := func(name string, debit, credit money.Money, count int) {
		if debit != r.DebitTotal || credit != r.CreditTotal {
			errs = append(errs, fmt.Errorf(
				"%s汇总 借%s/贷%s 不等于总合计 借%s/贷%s",
				name, debit, credit, r.DebitTotal, r.CreditTotal))
		}
		if count != r.VoucherCount {
			errs = append(errs, fmt.Errorf(
				"%s汇总的张数 %d 不等于凭证总数 %d", name, count, r.VoucherCount))
		}
	}

	var wd, wc money.Money
	var wCount int
	for _, w := range r.WordRows {
		wd = wd.Add(w.Debit)
		wc = wc.Add(w.Credit)
		wCount += w.Count
	}
	checkGroup("凭证字", wd, wc, wCount)

	var dd, dc money.Money
	dCount := 0
	for _, d := range r.DayRows {
		dd = dd.Add(d.Debit)
		dc = dc.Add(d.Credit)
		dCount += d.Count
	}
	checkGroup("日期", dd, dc, dCount)

	var ad, ac money.Money
	for _, a := range r.AccountRows {
		ad = ad.Add(a.Debit)
		ac = ac.Add(a.Credit)
	}
	// 科目的 Count 是分录条数，一张凭证有好几条分录，
	// 所以它天然大于凭证张数，不参与张数校验。
	if ad != r.DebitTotal || ac != r.CreditTotal {
		errs = append(errs, fmt.Errorf(
			"科目汇总 借%s/贷%s 不等于总合计 借%s/贷%s",
			ad, ac, r.DebitTotal, r.CreditTotal))
	}

	return errs
}

// Summary 返回一句话概览。
func (r *Report) Summary() string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("%s 至 %s：已记账凭证 %d 张，借方合计 %s、贷方合计 %s",
		r.From, r.To, r.VoucherCount, r.DebitTotal, r.CreditTotal)
}
