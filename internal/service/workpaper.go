package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/ledger"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/domain/period"
	"miniaccount/internal/domain/voucher"
	"miniaccount/internal/domain/workpaper"
	"miniaccount/internal/store/sqlite"
)

// ---------------------------------------------------------------------------
// 审计底稿
// ---------------------------------------------------------------------------
//
// 三张能算的表：重要性水平、审定表、未更正错报汇总。
//
// ★ 与凭证那条路的关系：审计调整可以**一键生成调整凭证草稿**。
//
// 草稿仍然要走完整的护栏（科目存在且可记账、辅助核算齐、借贷平），
// 生成之后与其他草稿一样，到账期结算才过账 —— 凭证那条路的规矩
// 一个字都没改。
//
// 而「未更正错报」按定义不进账：它只是底稿上的一行。
// 这个区别由 workpaper.Adjustment.Posted 一个字段表达，
// 它同时决定「算不算未更正错报」与「审定数里加不加」——
// 见 domain/workpaper 的包注释。

// WorkpaperView 是审计底稿页的全部数据。
type WorkpaperView struct {
	Period string `json:"period"`
	// Materiality 是本期的三个门槛；为 nil 表示还没配。
	Materiality *MaterialityView `json:"materiality"`
	// Benchmarks 是四个基准的自动取数结果（供选择基准）。
	Benchmarks []BenchmarkView `json:"benchmarks"`
	// Worksheet 是审定表。
	Worksheet []WorksheetRowView `json:"worksheet"`
	// Misstatements 是未更正错报汇总。
	Misstatements MisstatementSummaryView `json:"misstatements"`
	// Adjustments 是本期全部调整。
	Adjustments []AdjustmentView `json:"adjustments"`
}

// MaterialityView 是重要性水平（含三个算出来的门槛与算式）。
type MaterialityView struct {
	Benchmark     string `json:"benchmark"`
	BenchmarkName string `json:"benchmarkName"`
	// BenchmarkAmount 是基准金额。
	BenchmarkAmount  money.Money `json:"benchmarkAmount"`
	RatePPM          int64       `json:"ratePpm"`
	RateLabel        string      `json:"rateLabel"`
	PerformancePPM   int64       `json:"performancePpm"`
	PerformanceLabel string      `json:"performanceLabel"`
	TrivialPPM       int64       `json:"trivialPpm"`
	TrivialLabel     string      `json:"trivialLabel"`
	// Overall / Performance / Trivial 是三个门槛。
	Overall     money.Money `json:"overall"`
	Performance money.Money `json:"performance"`
	Trivial     money.Money `json:"trivial"`
	Note        string      `json:"note"`
	// Explain 是算式逐行说明 —— 底稿上要能答出「这个数怎么来的」。
	Explain []string `json:"explain"`
}

// BenchmarkView 是一个可选基准与它的自动取数金额。
type BenchmarkView struct {
	Value        string      `json:"value"`
	Label        string      `json:"label"`
	Amount       money.Money `json:"amount"`
	DefaultRate  int64       `json:"defaultRatePpm"`
	DefaultLabel string      `json:"defaultRateLabel"`
	// Usable 为假表示取数是 0 或负数，选它算不出有意义的重要性。
	Usable bool `json:"usable"`
}

// WorksheetRowView 是审定表的一行。
type WorksheetRowView struct {
	AccountCode  string      `json:"accountCode"`
	AccountName  string      `json:"accountName"`
	BookBalance  money.Money `json:"bookBalance"`
	AdjustDebit  money.Money `json:"adjustDebit"`
	AdjustCredit money.Money `json:"adjustCredit"`
	Audited      money.Money `json:"audited"`
	Adjusted     bool        `json:"adjusted"`
	// Trivial 为真表示该科目调整金额低于明显微小错报临界值（不必累积）。
	Trivial bool `json:"trivial"`
}

// AdjustmentView 是一笔审计调整。
type AdjustmentView struct {
	ID        int64       `json:"id"`
	Code      string      `json:"code"`
	Kind      string      `json:"kind"`
	KindLabel string      `json:"kindLabel"`
	Summary   string      `json:"summary"`
	Reason    string      `json:"reason"`
	Evidence  string      `json:"evidence"`
	Amount    money.Money `json:"amount"`
	Balanced  bool        `json:"balanced"`
	// Booked 为真表示已经生成过调整凭证（可能还是草稿）。
	Booked bool `json:"booked"`
	// Posted 为真表示那张凭证**已经过账**，调整已在账面数里。
	//
	// ★ 与 Booked 分开：生成凭证只落草稿，过账只在账期结算。
	// 界面靠这两个字段告诉用户「这笔改了没改到账上」。
	Posted    bool   `json:"posted"`
	VoucherID *int64 `json:"voucherId"`
	// StateLabel 是给界面直接显示的状态名。
	StateLabel string `json:"stateLabel"`
	// VoucherLabel 是界面显示的凭证标识（草稿没有号）。
	VoucherLabel string           `json:"voucherLabel"`
	Lines        []AdjustLineView `json:"lines"`
	CreatedBy    string           `json:"createdBy"`
	ReviewedBy   string           `json:"reviewedBy"`
	// Problems 是这笔调整本身的问题（借贷不平、缺依据…）。
	Problems []string `json:"problems"`
}

// AdjustLineView 是调整分录的一行。
type AdjustLineView struct {
	LineNo      int         `json:"lineNo"`
	AccountCode string      `json:"accountCode"`
	AccountName string      `json:"accountName"`
	Summary     string      `json:"summary"`
	Debit       money.Money `json:"debit"`
	Credit      money.Money `json:"credit"`
	// AuxDesc 是辅助核算的文字描述（给人看）。
	AuxDesc string `json:"auxDesc"`
	// ★ 四个辅助核算 id 必须一起给出来。
	//
	// 界面「修改」一笔调整时要把整行原样回传；少了这四个 id，
	// 部门/客户就被丢掉了，保存时会被「缺少必需的辅助核算」拦下 ——
	// 而用户在界面上看着那一行是齐的。发布前界面审计实测过这条路径。
	ContactID  *int64 `json:"contactId"`
	EmployeeID *int64 `json:"employeeId"`
	DeptID     *int64 `json:"deptId"`
	ProjectID  *int64 `json:"projectId"`
}

// MisstatementSummaryView 是未更正错报汇总。
type MisstatementSummaryView struct {
	Items          []MisstatementView `json:"items"`
	Total          money.Money        `json:"total"`
	Overall        money.Money        `json:"overall"`
	Performance    money.Money        `json:"performance"`
	Trivial        money.Money        `json:"trivial"`
	HasMateriality bool               `json:"hasMateriality"`
	ReclassCount   int                `json:"reclassCount"`
	// TrivialCount 是低于明显微小临界值、因此不计入合计的笔数。
	TrivialCount int `json:"trivialCount"`
	// Concludes 是「这些加起来算不算重大」的结论。
	Concludes string `json:"concludes"`
}

// MisstatementView 是一条未更正错报。
type MisstatementView struct {
	Code      string      `json:"code"`
	Summary   string      `json:"summary"`
	Kind      string      `json:"kind"`
	KindLabel string      `json:"kindLabel"`
	Amount    money.Money `json:"amount"`
	Reason    string      `json:"reason"`
	// Trivial 为真表示低于明显微小临界值（列出但不计入合计）。
	Trivial bool `json:"trivial"`
}

// ---------------------------------------------------------------------------
// 读
// ---------------------------------------------------------------------------

// Workpaper 返回某期的审计底稿。
func (s *Service) Workpaper(ctx context.Context, k period.Key) (*WorkpaperView, error) {
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", k.Year, k.Month)
	}
	ws, err := s.db.Workpapers().Worksheet(ctx, k)
	if err != nil {
		return nil, err
	}
	adjs, err := s.db.Workpapers().Adjustments(ctx, k)
	if err != nil {
		return nil, err
	}
	bench, err := s.db.Workpapers().Benchmarks(ctx, k)
	if err != nil {
		return nil, err
	}
	names, err := s.accountNames(ctx)
	if err != nil {
		return nil, err
	}
	contactNames, _ := s.contactNames(ctx)
	deptNames, _ := s.departmentNames(ctx)

	out := &WorkpaperView{
		Period:      k.String(),
		Worksheet:   []WorksheetRowView{},
		Adjustments: []AdjustmentView{},
	}
	if ws.Materiality != nil {
		out.Materiality = toMaterialityView(*ws.Materiality)
	}
	for _, b := range workpaper.Benchmarks() {
		amount := benchAmount(bench, b)
		out.Benchmarks = append(out.Benchmarks, BenchmarkView{
			Value: string(b), Label: b.Label(), Amount: amount,
			DefaultRate:  b.DefaultRatePPM(),
			DefaultLabel: workpaper.Percent(b.DefaultRatePPM()),
			Usable:       amount.IsPositive(),
		})
	}
	for _, r := range ws.Rows {
		row := WorksheetRowView{
			AccountCode: r.AccountCode, AccountName: r.AccountName,
			BookBalance: r.BookBalance, AdjustDebit: r.AdjustDebit,
			AdjustCredit: r.AdjustCredit, Audited: r.Audited(), Adjusted: r.Adjusted(),
		}
		// 调整额低于明显微小错报临界值的标出来 —— 底稿上不必逐笔累积
		if r.Adjusted() && !ws.ExceedsTrivial(r.AdjustDebit.Sub(r.AdjustCredit)) {
			row.Trivial = true
		}
		out.Worksheet = append(out.Worksheet, row)
	}
	for _, a := range adjs {
		v := AdjustmentView{
			ID: a.ID, Code: a.Code, Kind: string(a.Kind), KindLabel: a.Kind.Label(),
			Summary: a.Summary, Reason: a.Reason, Evidence: a.Evidence,
			Amount: a.Amount(), Balanced: a.Balanced(),
			Booked: a.Booked, Posted: a.Posted, StateLabel: adjustmentState(a),
			VoucherID: a.VoucherID, CreatedBy: a.CreatedBy, ReviewedBy: a.ReviewedBy,
			Lines: []AdjustLineView{},
		}
		if a.VoucherID != nil {
			v.VoucherLabel = s.voucherLabelOf(ctx, *a.VoucherID)
		}
		if err := a.Validate(); err != nil {
			v.Problems = append(v.Problems, err.Error())
		}
		for _, l := range a.Lines {
			v.Lines = append(v.Lines, AdjustLineView{
				LineNo: l.LineNo, AccountCode: l.AccountCode,
				AccountName: names[l.AccountCode], Summary: l.Summary,
				Debit: l.Debit, Credit: l.Credit,
				ContactID: l.ContactID, EmployeeID: l.EmployeeID,
				DeptID: l.DeptID, ProjectID: l.ProjectID,
				AuxDesc: describeAux(ledger.Aux{
					ContactID: l.ContactID, EmployeeID: l.EmployeeID,
					DeptID: l.DeptID, ProjectID: l.ProjectID,
				}, contactNames, deptNames),
			})
		}
		out.Adjustments = append(out.Adjustments, v)
	}

	sum := workpaper.Misstatements(adjs, ws.Materiality)
	ms := MisstatementSummaryView{
		Items: []MisstatementView{}, Total: sum.Total,
		Overall: sum.Overall, Performance: sum.Performance, Trivial: sum.Trivial,
		HasMateriality: sum.HasMateriality, ReclassCount: sum.ReclassCount,
		TrivialCount: sum.TrivialCount,
		Concludes:    sum.Concludes(),
	}
	for _, it := range sum.Items {
		ms.Items = append(ms.Items, MisstatementView{
			Code: it.Code, Summary: it.Summary, Kind: string(it.Kind),
			KindLabel: it.Kind.Label(), Amount: it.Amount, Reason: it.Reason,
			Trivial: it.Trivial,
		})
	}
	out.Misstatements = ms
	return out, nil
}

// adjustmentState 给出一句话状态。
//
// ★ 这句话要答的是「这笔改了没改到账上」，而不是「有没有登记」。
// 底稿上最容易误读的就是这个：看到「已生成凭证」就以为账已经改了，
// 而账期一结算那张草稿才真的过账。中间这段时间，
// 审定数里仍然加着它、未更正错报里也仍然列着它。
func adjustmentState(a workpaper.Adjustment) string {
	switch {
	case a.Posted:
		return "已入账（凭证已过账）"
	case a.Booked:
		return "已生成凭证（草稿，待账期结算过账）"
	default:
		return "未入账（仅登记在底稿）"
	}
}

func benchAmount(b *sqlite.BenchmarkAmounts, kind workpaper.Benchmark) money.Money {
	if b == nil {
		return 0
	}
	switch kind {
	case workpaper.BenchmarkAssets:
		return b.Assets
	case workpaper.BenchmarkRevenue:
		return b.Revenue
	case workpaper.BenchmarkProfit:
		return b.Profit
	case workpaper.BenchmarkExpense:
		return b.Expense
	}
	return 0
}

func toMaterialityView(m workpaper.Materiality) *MaterialityView {
	return &MaterialityView{
		Benchmark: string(m.Benchmark), BenchmarkName: m.Benchmark.Label(),
		BenchmarkAmount: m.BenchmarkAmount,
		RatePPM:         m.RatePPM, RateLabel: workpaper.Percent(m.RatePPM),
		PerformancePPM:   m.PerformancePPM,
		PerformanceLabel: workpaper.Percent(m.PerformancePPM),
		TrivialPPM:       m.TrivialPPM, TrivialLabel: workpaper.Percent(m.TrivialPPM),
		Overall: m.Overall(), Performance: m.Performance(), Trivial: m.Trivial(),
		Note: m.Note, Explain: m.Explain(),
	}
}

// accountNames 返回 科目编码 → 名称。
func (s *Service) accountNames(ctx context.Context) (map[string]string, error) {
	tree, err := s.db.Accounts().Tree(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, a := range tree.All() {
		out[a.Code] = a.Name
	}
	return out, nil
}

// voucherLabelOf 给出凭证的界面标识（草稿没有号）。
func (s *Service) voucherLabelOf(ctx context.Context, id int64) string {
	v, err := s.db.Vouchers().Get(ctx, id)
	if err != nil {
		return fmt.Sprintf("凭证 #%d（已删除）", id)
	}
	return voucherLabel(v.No, v.ID)
}

// ---------------------------------------------------------------------------
// 写
// ---------------------------------------------------------------------------

// MaterialityInput 是保存重要性水平的入参。
type MaterialityInput struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	// Benchmark 是基准类型。
	Benchmark string `json:"benchmark"`
	// BenchmarkAmount 是基准金额；为 0 表示用账套的自动取数。
	BenchmarkAmount money.Money `json:"benchmarkAmount"`
	RatePPM         int64       `json:"ratePpm"`
	PerformancePPM  int64       `json:"performancePpm"`
	TrivialPPM      int64       `json:"trivialPpm"`
	Note            string      `json:"note"`
}

// SaveMateriality 保存某期的重要性水平。
func (s *Service) SaveMateriality(ctx context.Context, in MaterialityInput) (*WorkpaperView, error) {
	k := period.NewKey(in.Year, in.Month)
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", in.Year, in.Month)
	}
	b := workpaper.Benchmark(strings.TrimSpace(in.Benchmark))
	if !b.Valid() {
		return nil, fmt.Errorf("%w: %q", workpaper.ErrBadBenchmark, in.Benchmark)
	}
	amount := in.BenchmarkAmount
	if amount.IsZero() {
		// 没给就用账套取数 —— 界面上默认就是这么填的
		bench, err := s.db.Workpapers().Benchmarks(ctx, k)
		if err != nil {
			return nil, err
		}
		amount = benchAmount(bench, b)
	}
	rate := in.RatePPM
	if rate == 0 {
		rate = b.DefaultRatePPM()
	}
	perf := in.PerformancePPM
	if perf == 0 {
		perf = workpaper.DefaultPerformancePPM
	}
	triv := in.TrivialPPM
	if triv == 0 {
		triv = workpaper.DefaultTrivialPPM
	}
	m := workpaper.Materiality{
		Period: k, Benchmark: b, BenchmarkAmount: amount,
		RatePPM: rate, PerformancePPM: perf, TrivialPPM: triv,
		Note: strings.TrimSpace(in.Note),
	}
	if err := s.db.Workpapers().SaveMateriality(ctx, m); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action: audit.ActionWorkpaperSave,
		Summary: fmt.Sprintf("确定 %s 的重要性水平（%s %s × %s = %s）",
			k, b.Label(), amount, workpaper.Percent(rate), m.Overall()),
		Entity: "period", EntityID: k.String(),
		Detail: map[string]any{
			"期间": k.String(), "基准": b.Label(), "基准金额": amount.String(),
			"整体重要性":     m.Overall().String(),
			"实际执行重要性":   m.Performance().String(),
			"明显微小错报临界值": m.Trivial().String(),
			"说明":        m.Note,
		},
	})
	return s.Workpaper(ctx, k)
}

// DeleteMateriality 删掉某期的重要性水平。
func (s *Service) DeleteMateriality(ctx context.Context, year, month int) (*WorkpaperView, error) {
	k := period.NewKey(year, month)
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", year, month)
	}
	if err := s.db.Workpapers().DeleteMateriality(ctx, k); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionWorkpaperSave,
		Summary: "清除 " + k.String() + " 的重要性水平",
		Entity:  "period", EntityID: k.String(),
	})
	return s.Workpaper(ctx, k)
}

// AdjustmentInput 是保存审计调整的入参。
type AdjustmentInput struct {
	ID       int64             `json:"id"`
	Year     int               `json:"year"`
	Month    int               `json:"month"`
	Code     string            `json:"code"`
	Kind     string            `json:"kind"`
	Summary  string            `json:"summary"`
	Reason   string            `json:"reason"`
	Evidence string            `json:"evidence"`
	Lines    []AdjustLineInput `json:"lines"`
	Operator string            `json:"operator"`
}

// AdjustLineInput 是调整分录的一行。
type AdjustLineInput struct {
	AccountCode string      `json:"accountCode"`
	Summary     string      `json:"summary"`
	Debit       money.Money `json:"debit"`
	Credit      money.Money `json:"credit"`
	ContactID   *int64      `json:"contactId"`
	EmployeeID  *int64      `json:"employeeId"`
	DeptID      *int64      `json:"deptId"`
	ProjectID   *int64      `json:"projectId"`
}

// SaveAdjustment 新增或修改一笔审计调整。
func (s *Service) SaveAdjustment(ctx context.Context, in AdjustmentInput) (*WorkpaperView, error) {
	k := period.NewKey(in.Year, in.Month)
	if !k.Valid() {
		return nil, fmt.Errorf("会计期间 %04d-%02d 非法", in.Year, in.Month)
	}
	a := workpaper.Adjustment{
		ID: in.ID, Period: k,
		Code: strings.TrimSpace(in.Code), Kind: workpaper.AdjustKind(in.Kind),
		Summary: strings.TrimSpace(in.Summary), Reason: strings.TrimSpace(in.Reason),
		Evidence:  strings.TrimSpace(in.Evidence),
		CreatedBy: strings.TrimSpace(in.Operator),
	}
	if a.Kind == "" {
		a.Kind = workpaper.KindAdjust
	}
	// 编号留空 → 由仓储在写入事务里取「已有最大序号 + 1」（见
	// nextAdjustmentCode）。不在这里算：这里算出来的号可能被
	// 并发写入或刚才的删除挤掉，而重号会让底稿之间的引用失效。
	if a.Code != "" {
		// 手填的号要挡重号：底稿之间靠编号互相引用
		list, err := s.db.Workpapers().Adjustments(ctx, k)
		if err != nil {
			return nil, err
		}
		for _, old := range list {
			if old.ID != a.ID && strings.EqualFold(old.Code, a.Code) {
				return nil, fmt.Errorf(
					"编号 %s 已经被另一笔调整用了（%s）—— 底稿之间靠编号互相引用，"+
						"重号会让引用失效。留空则由程序自动编号。", a.Code, old.Summary)
			}
		}
	}
	for _, l := range in.Lines {
		a.Lines = append(a.Lines, workpaper.AdjustLine{
			AccountCode: strings.TrimSpace(l.AccountCode), Summary: l.Summary,
			Debit: l.Debit, Credit: l.Credit,
			ContactID: l.ContactID, EmployeeID: l.EmployeeID,
			DeptID: l.DeptID, ProjectID: l.ProjectID,
		})
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	// ★ 已经生成过凭证的调整不许再改。
	//
	// 改得掉的话，底稿上的金额与凭证上的金额就对不上了 ——
	// 而两边都写着「同一笔调整」。要改先把那张凭证处理掉
	// （草稿删掉、已过账的红冲），底稿随之解锁。
	if a.ID != 0 {
		old, err := s.db.Workpapers().Adjustment(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		if old.Booked {
			return nil, fmt.Errorf("调整 %s 已经生成凭证 %s，改不动了 —— "+
				"请先删掉那张凭证（已过账的走红字冲销），再回来改底稿",
				old.Code, s.voucherLabelOf(ctx, derefOr(old.VoucherID, 0)))
		}
	}
	// 走凭证那套护栏 —— 与手工凭证同一条要求。
	//
	// ★ 在这里挡而不是等生成凭证时才挡：底稿编到一半发现科目不存在、
	// 部门没填，用户还得回头找是哪一行；现在就告诉他，他手里正拿着那行。
	if err := s.validateAdjustment(ctx, a); err != nil {
		return nil, err
	}
	id, err := s.db.Workpapers().SaveAdjustment(ctx, a)
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionWorkpaperSave,
		Summary: fmt.Sprintf("登记审计调整 %s：%s（%s）", a.Code, a.Summary, a.Amount()),
		Entity:  "audit_adjustment", EntityID: fmt.Sprintf("%d", id),
		Operator: a.CreatedBy,
		Detail: map[string]any{
			"期间": k.String(), "编号": a.Code, "种类": a.Kind.Label(),
			"摘要": a.Summary, "依据": a.Reason, "证据": a.Evidence,
			"金额": a.Amount().String(),
		},
	})
	return s.Workpaper(ctx, k)
}

// validateAdjustment 借用凭证那套护栏校验一笔调整。
//
// ★ 不自己重写一套校验。
//
// 调整最终要变成凭证，两边若各有一套判断，迟早会出现
// 「底稿能存下来、生成凭证时却被拦下」—— 用户面对的是一句
// 他无法从底稿上看出来的错误。所以这里直接构造一张凭证，
// 交给与手工凭证完全相同的 CheckVoucher：
// 分录数、金额方向、摘要、借贷平衡、科目可记账、辅助核算齐全，
// 一条不少，报错还带行号。
func (s *Service) validateAdjustment(ctx context.Context, a workpaper.Adjustment) error {
	day, err := calendar.New(a.Period.Year, a.Period.Month,
		calendar.DaysInMonth(a.Period.Year, a.Period.Month))
	if err != nil {
		return err
	}
	in := VoucherInput{
		Word: string(voucher.WordZhuan), Date: day.String(),
		Remark:    fmt.Sprintf("审计调整 %s：%s", a.Code, a.Summary),
		CreatedBy: a.CreatedBy,
		Lines:     make([]VoucherLineInput, 0, len(a.Lines)),
	}
	for _, l := range a.Lines {
		// 行摘要空着就用调整的摘要 —— 与生成凭证时同一套兜底，
		// 否则校验和生成会对同一笔调整给出不同结论
		summary := strings.TrimSpace(l.Summary)
		if summary == "" {
			summary = a.Summary
		}
		in.Lines = append(in.Lines, VoucherLineInput{
			AccountCode: l.AccountCode, Summary: summary,
			Debit: l.Debit, Credit: l.Credit,
			ContactID: l.ContactID, EmployeeID: l.EmployeeID,
			DeptID: l.DeptID, ProjectID: l.ProjectID,
		})
	}
	res, err := s.CheckVoucher(ctx, in)
	if err != nil {
		return err
	}
	if !res.OK {
		// ★ 用 %w 而不是 errors.New(...)：CheckVoucher 的结论里带着
		// ledger.ErrMissingAux 这类哨兵，断了错误链之后绑定层就没法
		// 把它归成「用户能改的问题」，界面会显示成「发生内部错误」。
		return fmt.Errorf("调整分录还不能生成凭证：%w", faultOf(res.Message))
	}
	return nil
}

// faultOf 把一句校验结论包成可被 errors.Is 识别的错误。
//
// 结论是纯文本（界面要直接显示），但错误链不能断 ——
// 这里按关键词还原成对应的哨兵，识别不出时仍返回原文。
func faultOf(msg string) error {
	switch {
	case strings.Contains(msg, "辅助核算"):
		return fmt.Errorf("%w：%s", ledger.ErrMissingAux, msg)
	case strings.Contains(msg, "科目不存在"):
		return fmt.Errorf("%w：%s", ledger.ErrAccountMissing, msg)
	case strings.Contains(msg, "借贷不平"):
		return fmt.Errorf("%w：%s", ledger.ErrNotBalanced, msg)
	default:
		return errors.New(msg)
	}
}

// DeleteAdjustment 删除一笔调整。
//
// ★ 已经生成凭证的调整不许删。
//
// 删得掉的话，账上会留下一张「谁也不知道为什么存在」的调整凭证 ——
// 凭证还在，底稿没了，审计轨迹就从这里断开。
// 要改就先删那张草稿凭证，顺序反过来做。
func (s *Service) DeleteAdjustment(ctx context.Context, id int64) (*WorkpaperView, error) {
	a, err := s.db.Workpapers().Adjustment(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.Booked {
		return nil, fmt.Errorf("调整 %s 已经生成凭证 %s，不能直接删除 —— "+
			"请先删除那张凭证，否则账上会留下一张没有底稿的凭证",
			a.Code, s.voucherLabelOf(ctx, derefOr(a.VoucherID, 0)))
	}
	if err := s.db.Workpapers().DeleteAdjustment(ctx, id); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionWorkpaperDelete,
		Summary: fmt.Sprintf("删除审计调整 %s：%s", a.Code, a.Summary),
		Entity:  "audit_adjustment", EntityID: fmt.Sprintf("%d", id),
		Detail: map[string]any{"金额": a.Amount().String(), "依据": a.Reason},
	})
	return s.Workpaper(ctx, a.Period)
}

// PostAdjustment 把一笔调整生成**调整凭证草稿**。
//
// ★ 走的是与手工凭证、工资凭证完全相同的那条路：SaveDraftInTx。
//
// 于是科目可记账、辅助核算齐、借贷平衡这些护栏一条不少；
// 而草稿不占号、不进总账，到账期结算时才过账 ——
// 「凭证录完只落草稿，过账只发生在账期结算」这条规矩
// 在审计调整上一样成立。
func (s *Service) PostAdjustment(ctx context.Context, id int64, operator string) (*WorkpaperView, error) {
	if strings.TrimSpace(operator) == "" {
		return nil, errors.New("请填写操作人 —— 调整凭证要有人负责")
	}
	a, err := s.db.Workpapers().Adjustment(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.Booked {
		if a.Posted {
			return nil, fmt.Errorf("调整 %s 的凭证 %s 已经过账了，不能再来一张 —— "+
				"改账请按红字冲销走",
				a.Code, s.voucherLabelOf(ctx, derefOr(a.VoucherID, 0)))
		}
		return nil, fmt.Errorf("调整 %s 已经生成过凭证了（%s，草稿）—— "+
			"同一笔调整生成两张凭证会让账重复计一次。\n"+
			"要重新生成，请先删掉那张草稿凭证",
			a.Code, s.voucherLabelOf(ctx, derefOr(a.VoucherID, 0)))
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}

	// 凭证日期取该期间最后一天：调整是针对这一期的，
	// 用月末日期才能落回正确的会计期间
	bizDate, err := calendar.New(a.Period.Year, a.Period.Month,
		calendar.DaysInMonth(a.Period.Year, a.Period.Month))
	if err != nil {
		return nil, err
	}
	doc, err := voucher.New(voucher.WordZhuan, bizDate, strings.TrimSpace(operator))
	if err != nil {
		return nil, err
	}
	doc.Source = voucher.SourceAudit
	doc.Remark = fmt.Sprintf("审计调整 %s：%s", a.Code, a.Summary)
	for i, l := range a.Lines {
		summary := strings.TrimSpace(l.Summary)
		if summary == "" {
			summary = a.Summary
		}
		if err := doc.AddEntry(ledger.Entry{
			AccountCode: l.AccountCode, Summary: summary,
			Debit: l.Debit, Credit: l.Credit,
			Aux: ledger.Aux{
				ContactID: l.ContactID, EmployeeID: l.EmployeeID,
				DeptID: l.DeptID, ProjectID: l.ProjectID,
			},
		}); err != nil {
			return nil, fmt.Errorf("第 %d 行：%w", i+1, err)
		}
	}

	var voucherID int64
	err = s.db.WithTx(ctx, func(tx *sqliteTx) error {
		res, err := s.db.Vouchers().SaveDraftInTx(ctx, tx, sqlite.DraftInput{
			Voucher: doc, CreatedBy: strings.TrimSpace(operator), Generated: true,
		})
		if err != nil {
			return err
		}
		voucherID = res.VoucherID
		// ★ 标记与建凭证必须在同一个事务里。
		// 分开做会出现「凭证生成了、调整还写着未入账」——
		// 那笔就会被算两次：一次在审定数里（未入账），
		// 一次在账面数里（已入账）。
		return s.db.Workpapers().MarkBookedInTx(ctx, tx, id, voucherID)
	})
	if err != nil {
		return nil, err
	}

	s.recordAudit(ctx, AuditEvent{
		Action:  audit.ActionWorkpaperPost,
		Summary: fmt.Sprintf("审计调整 %s 生成调整凭证", a.Code),
		Entity:  "audit_adjustment", EntityID: fmt.Sprintf("%d", id),
		Operator: strings.TrimSpace(operator),
		Detail: map[string]any{
			"期间": a.Period.String(), "编号": a.Code, "摘要": a.Summary,
			"金额": a.Amount().String(),
			"凭证": s.voucherLabelOf(ctx, voucherID),
			"说明": "生成的是草稿凭证：不占号、不进总账，到账期结算时才过账",
		},
	})
	return s.Workpaper(ctx, a.Period)
}

func derefOr(p *int64, def int64) int64 {
	if p == nil {
		return def
	}
	return *p
}
