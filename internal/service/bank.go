package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"miniaccount/internal/bankcsv"
	"miniaccount/internal/domain/audit"
	"miniaccount/internal/domain/bank"
	"miniaccount/internal/domain/calendar"
	"miniaccount/internal/domain/money"
	"miniaccount/internal/store/sqlite"
)

// ---------------------------------------------------------------------------
// 银行流水
// ---------------------------------------------------------------------------

// BankFlowView 是界面上的一条流水。
type BankFlowView struct {
	ID     int64       `json:"id"`
	Date   string      `json:"date"`
	Amount money.Money `json:"amount"`
	// Direction 是 in / out。
	Direction string `json:"direction"`
	// DirectionLabel 是「收入 / 支出」。
	DirectionLabel   string      `json:"directionLabel"`
	CounterpartyName string      `json:"counterpartyName"`
	Summary          string      `json:"summary"`
	SerialNo         string      `json:"serialNo"`
	Balance          money.Money `json:"balance"`

	Status string `json:"status"`
	// StatusLabel 是中文状态名。
	StatusLabel string `json:"statusLabel"`

	// CounterAccount 是建议（或已确认）的对方科目编码。
	CounterAccount string `json:"counterAccount"`
	// MatchLayer 是匹配来源：rule / history / ai。
	MatchLayer string `json:"matchLayer"`
	// MatchLayerLabel 是「规则 / 历史 / AI」。
	MatchLayerLabel string  `json:"matchLayerLabel"`
	Confidence      float64 `json:"confidence"`
	Memo            string  `json:"memo"`

	// VoucherNo 是生成凭证后的凭证号。
	VoucherNo string `json:"voucherNo"`
}

// BankFlowQuery 是流水列表的筛选条件。
type BankFlowQuery struct {
	Status string
	// From / To 是日期区间（YYYY-MM-DD），留空表示不限。
	From  string `json:"from"`
	To    string `json:"to"`
	Limit int
}

// BankFlows 返回银行流水列表。
func (s *Service) BankFlows(ctx context.Context, q BankFlowQuery) ([]BankFlowView, error) {
	from, to, err := s.resolveDateRange(ctx, q.From, q.To)
	if err != nil {
		return nil, err
	}
	s.recordView(ctx, AuditEvent{
		Action: audit.ActionViewBankFlows, Entity: "bank_flow",
		EntityID: "flows/" + trimTo(q.Status, 10),
		Summary:  "查看银行流水",
		Detail:   map[string]any{"状态": q.Status, "起": from.String(), "止": to.String()},
	})
	st := bank.Status(strings.TrimSpace(q.Status))
	flows, err := s.db.Bank().ListFlows(ctx, st, from, to, q.Limit)
	if err != nil {
		return nil, err
	}
	// 凭证号一次性查出来，避免每条流水单独查一次
	vnos, err := s.voucherNos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]BankFlowView, 0, len(flows))
	for _, f := range flows {
		v := BankFlowView{
			ID: f.ID, Date: f.TxnDate.String(), Amount: f.Amount,
			Direction: string(f.Direction), DirectionLabel: f.Direction.Label(),
			CounterpartyName: f.CounterpartyName, Summary: f.Summary,
			SerialNo: f.SerialNo, Balance: f.Balance,
			Status: string(f.Status), StatusLabel: f.Status.Label(),
			CounterAccount: f.CounterAccount, Memo: f.Memo,
			MatchLayer: string(f.MatchLayer), MatchLayerLabel: f.MatchLayer.Label(),
			Confidence: f.Confidence,
		}
		if f.VoucherID != nil {
			v.VoucherNo = vnos[*f.VoucherID]
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) voucherNos(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `SELECT id, no FROM voucher`)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var no string
		if err := rows.Scan(&id, &no); err != nil {
			return nil, err
		}
		out[id] = no
	}
	return out, rows.Err()
}

// resolveDateRange 解析日期区间；留空时取账套全部会计期间。
func (s *Service) resolveDateRange(ctx context.Context, from, to string) (
	calendar.Date, calendar.Date, error) {

	book, err := s.Book(ctx)
	if err != nil {
		return calendar.Date{}, calendar.Date{}, err
	}
	var dFrom, dTo calendar.Date
	if len(book.Periods) > 0 {
		dFrom, err = parseDate(book.Periods[0].From)
		if err != nil {
			return dFrom, dTo, err
		}
		dTo, err = parseDate(book.Periods[len(book.Periods)-1].To)
		if err != nil {
			return dFrom, dTo, err
		}
	} else {
		dFrom, dTo = todayDate(), todayDate()
	}
	if strings.TrimSpace(from) != "" {
		if dFrom, err = parseDate(from); err != nil {
			return dFrom, dTo, fmt.Errorf("起始日期 %q 格式不对，应为 YYYY-MM-DD", from)
		}
	}
	if strings.TrimSpace(to) != "" {
		if dTo, err = parseDate(to); err != nil {
			return dFrom, dTo, fmt.Errorf("截止日期 %q 格式不对，应为 YYYY-MM-DD", to)
		}
	}
	if dTo.Before(dFrom) {
		return dFrom, dTo, fmt.Errorf("截止日期不能早于起始日期")
	}
	return dFrom, dTo, nil
}

// BankStats 是流水的各状态数量。
type BankStats struct {
	Imported int `json:"imported"`
	Matched  int `json:"matched"`
	Posted   int `json:"posted"`
	Ignored  int `json:"ignored"`
}

// BankStats 返回流水的状态统计。
func (s *Service) BankStats(ctx context.Context) (*BankStats, error) {
	st, err := s.db.Bank().Stats(ctx)
	if err != nil {
		return nil, err
	}
	return &BankStats{
		Imported: st.Imported, Matched: st.Matched,
		Posted: st.Posted, Ignored: st.Ignored,
	}, nil
}

// BankImportInput 是导入对账单的输入。
type BankImportInput struct {
	AccountCode string
	FileName    string
	// Data 是文件的**原始字节**。
	//
	// ★ 必须是原始字节而不是文本：银行对账单大量使用 GB18030，
	// 前端若用 text() 读会按 UTF-8 解码，中文全变乱码，
	// 而乱码进到解析器里只会报「列名识别不出来」——
	// 用户完全想不到是编码问题。
	Data       []byte
	ImportedBy string
}

// BankImportResult 是一次导入的结果。
type BankImportResult struct {
	ImportID   int64       `json:"importId"`
	Total      int         `json:"total"`
	Inserted   int         `json:"inserted"`
	Duplicated int         `json:"duplicated"`
	TotalIn    money.Money `json:"totalIn"`
	TotalOut   money.Money `json:"totalOut"`
	From       string      `json:"from"`
	To         string      `json:"to"`
	// Encoding 是探测到的编码名。
	Encoding string `json:"encoding"`
	// Mapping 是实际使用的列映射描述，供用户核对。
	Mapping string `json:"mapping"`
	// ParseErrors 是逐行的解析问题（最多前若干条）。
	ParseErrors []string `json:"parseErrors"`
}

// ImportBankStatement 导入一份银行对账单。
func (s *Service) ImportBankStatement(ctx context.Context, in BankImportInput) (*BankImportResult, error) {
	if len(in.Data) == 0 {
		return nil, fmt.Errorf("对账单文件是空的")
	}
	if strings.TrimSpace(in.AccountCode) == "" {
		return nil, fmt.Errorf("请指定银行科目")
	}

	parsed, err := bankcsv.Parse(in.Data, bankcsv.Options{
		BankAccountCode:  in.AccountCode,
		AutoDetectHeader: true,
	})
	if err != nil {
		return nil, err
	}

	res := &BankImportResult{
		Encoding: parsed.Table.Encoding,
		Mapping:  describeMapping(parsed.Mapping),
		Total:    len(parsed.Flows),
	}
	for _, e := range parsed.Errors {
		res.ParseErrors = append(res.ParseErrors,
			fmt.Sprintf("第 %d 行：%s", e.Line, e.Reason))
	}
	if len(parsed.Flows) == 0 {
		miss := parsed.Mapping.Missing()
		if len(miss) > 0 {
			return nil, fmt.Errorf(
				"识别不出这些必需的列：%s\n文件表头是：%v\n"+
					"请确认这是银行导出的对账单，或把表头改成常见列名后重试",
				strings.Join(miss, "、"), parsed.Table.Header)
		}
		return nil, fmt.Errorf("这份文件里没有解析出任何流水")
	}

	imp, err := s.db.Bank().Import(ctx, sqlite.ImportInput{
		AccountCode: in.AccountCode, FileName: in.FileName, FileData: in.Data,
		Encoding: parsed.Table.Encoding, ImportedBy: in.ImportedBy,
		Rows: parsed.Flows,
	})
	if err != nil {
		return nil, err
	}
	res.ImportID = imp.ImportID
	res.Inserted = imp.Inserted
	res.Duplicated = imp.Duplicated
	res.TotalIn = imp.TotalIn
	res.TotalOut = imp.TotalOut
	if imp.From.Valid() {
		res.From = imp.From.String()
		res.To = imp.To.String()
	}
	return res, nil
}

// describeMapping 把列映射渲染成一句人话。
func describeMapping(m bankcsv.Mapping) string {
	var b strings.Builder
	switch m.DirectionMode {
	case bankcsv.DirModeTwoCols:
		fmt.Fprintf(&b, "收入/支出两列（收入=%q 支出=%q）", m.Credit, m.Debit)
	case bankcsv.DirModeColumn:
		fmt.Fprintf(&b, "金额列 + 方向列（金额=%q 方向=%q）", m.Amount, m.Direction)
	default:
		fmt.Fprintf(&b, "带符号金额（金额=%q）", m.Amount)
	}
	fmt.Fprintf(&b, "　日期=%q 摘要=%q 对方=%q", m.Date, m.Summary, m.CounterpartyName)
	return b.String()
}

// BankMatchResult 是一次批量匹配的结果。
type BankMatchResult struct {
	Total     int            `json:"total"`
	Matched   int            `json:"matched"`
	ByLayer   map[string]int `json:"byLayer"`
	Unmatched []int64        `json:"unmatched"`
}

// MatchBankFlows 对未匹配的流水做批量匹配。
func (s *Service) MatchBankFlows(ctx context.Context) (*BankMatchResult, error) {
	res, err := s.db.Bank().MatchAll(ctx, bank.StatusImported)
	if err != nil {
		return nil, err
	}
	out := &BankMatchResult{
		Total: res.Total, Matched: res.Matched,
		ByLayer: map[string]int{}, Unmatched: res.Unmatched,
	}
	for l, n := range res.ByLayer {
		out.ByLayer[string(l)] = n
	}
	return out, nil
}

// BankPostResult 是批量生成凭证的结果。
type BankPostResult struct {
	Created  int      `json:"created"`
	Skipped  int      `json:"skipped"`
	Failures []string `json:"failures"`
}

// PostBankFlows 把已匹配的流水批量生成凭证。
func (s *Service) PostBankFlows(ctx context.Context, ids []int64, postingBy string) (*BankPostResult, error) {
	if strings.TrimSpace(postingBy) == "" {
		return nil, fmt.Errorf("请填写记账人")
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("没有待生成凭证的流水")
	}
	res, err := s.db.Bank().PostFlows(ctx, ids, strings.TrimSpace(postingBy), time.Now())
	if err != nil {
		return nil, err
	}
	out := &BankPostResult{Created: res.Created, Skipped: res.Skipped}
	// 失败原因按流水 id 排序输出，保证同一批数据的报错顺序稳定 ——
	// map 遍历顺序随机，直接输出会让同一次操作每次看到的顺序都不同。
	failIDs := make([]int64, 0, len(res.Failures))
	for id := range res.Failures {
		failIDs = append(failIDs, id)
	}
	sortInt64(failIDs)
	for _, id := range failIDs {
		out.Failures = append(out.Failures,
			fmt.Sprintf("流水 %d：%s", id, res.Failures[id]))
	}
	return out, nil
}

func sortInt64(a []int64) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// IgnoreBankFlow 忽略一条流水（如银行手续费以外的手续、内部划转）。
func (s *Service) IgnoreBankFlow(ctx context.Context, id int64, reason string) error {
	return s.db.Bank().IgnoreFlow(ctx, id, reason)
}

// ---------------------------------------------------------------------------
// 匹配规则
// ---------------------------------------------------------------------------

// BankRuleInput 是新增规则的输入。
type BankRuleInput struct {
	Name               string
	Pattern            string
	CounterAccountCode string
	Direction          string
	ContactID          *int64
	EmployeeID         *int64
	DeptID             *int64
	ProjectID          *int64
}

// SaveBankRule 新增一条匹配规则。
func (s *Service) SaveBankRule(ctx context.Context, in BankRuleInput) (int64, error) {
	if strings.TrimSpace(in.Pattern) == "" {
		return 0, fmt.Errorf("匹配关键词不能为空")
	}
	if strings.TrimSpace(in.CounterAccountCode) == "" {
		return 0, fmt.Errorf("对方科目不能为空")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = in.CounterAccountCode + " " + in.Pattern
	}
	return s.db.Bank().UpsertRule(ctx, &bank.Rule{
		Name: name, Pattern: strings.TrimSpace(in.Pattern),
		CounterAccountCode: strings.TrimSpace(in.CounterAccountCode),
		Direction:          bank.Direction(in.Direction),
		MatchField:         bank.MatchBoth,
		ContactID:          in.ContactID,
		EmployeeID:         in.EmployeeID,
		DeptID:             in.DeptID,
		ProjectID:          in.ProjectID,
		Enabled:            true, Priority: 100,
	})
}

// BankRuleView 是界面上的一条规则。
type BankRuleView struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Pattern            string `json:"pattern"`
	CounterAccountCode string `json:"counterAccountCode"`
	Direction          string `json:"direction"`
	ContactID          *int64 `json:"contactId"`
	EmployeeID         *int64 `json:"employeeId"`
	DeptID             *int64 `json:"deptId"`
	ProjectID          *int64 `json:"projectId"`
	HitCount           int    `json:"hitCount"`
	Enabled            bool   `json:"enabled"`
}

// BankRules 返回全部匹配规则。
func (s *Service) BankRules(ctx context.Context) ([]BankRuleView, error) {
	rules, err := s.db.Bank().ListRules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]BankRuleView, 0, len(rules))
	for _, r := range rules {
		out = append(out, BankRuleView{
			ID: r.ID, Name: r.Name, Pattern: r.Pattern,
			CounterAccountCode: r.CounterAccountCode,
			Direction:          string(r.Direction),
			ContactID:          r.ContactID, EmployeeID: r.EmployeeID,
			DeptID: r.DeptID, ProjectID: r.ProjectID,
			HitCount: r.HitCount, Enabled: r.Enabled,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 人工指定记账方案
// ---------------------------------------------------------------------------

// BankSuggestionInput 是人工为一条流水指定记账方案。
type BankSuggestionInput struct {
	// CounterAccountCode 是对方科目编码。
	CounterAccountCode string
	// 四维辅助核算。科目要求哪一维就必须给哪一维，否则生成凭证时会被拦下。
	ContactID  *int64
	EmployeeID *int64
	DeptID     *int64
	ProjectID  *int64
	// Memo 是本行摘要；留空时按流水摘要生成。
	Memo string
}

// SetBankSuggestion 人工指定一条流水的记账方案。
//
// # 为什么必须有这个入口
//
// 三层匹配（规则 → 历史 → AI）再全，也总会有匹配不上的流水：
// 一次性的业务、新出现的对手方、摘要写得莫名其妙的支出。
// 没有人工指定，这些流水就只能在界面上「忽略」——
// 而忽略意味着这笔钱**永远进不了账**，银行余额与账面永远对不上。
//
// 人工指定出来的结果与自动匹配**走完全相同的后续流程**：
// 同样是 status=matched、同样要靠 PostFlows 生成凭证。
// 区别只在于 match_layer=manual，界面上标成「人工」——
// 这样复盘时能看出哪些是软件猜的、哪些是人定的。
func (s *Service) SetBankSuggestion(ctx context.Context, flowID int64,
	in BankSuggestionInput) (*BankFlowView, error) {

	code := strings.TrimSpace(in.CounterAccountCode)
	if code == "" {
		return nil, fmt.Errorf("请选择对方科目")
	}
	// 先确认科目可记账，再落库。
	//
	// 让错误在这里出现，而不是等用户点「生成凭证」时才报 ——
	// 那时他面对的是几十条流水的一批操作，不知道是哪一条出了问题。
	tree, err := s.db.Accounts().Tree(ctx)
	if err != nil {
		return nil, err
	}
	if err := tree.CheckPostable(code); err != nil {
		if strings.Contains(err.Error(), "不存在") {
			// 给出的名字比编码更有用
			return nil, fmt.Errorf("对方科目 %q 不存在", code)
		}
		return nil, err
	}
	// ★ 辅助核算也要在这里检查，而不是等生成凭证时。
	//
	// 流水是**批量**处理的：用户一次勾几十条点「生成凭证」，
	// 事后被告知「有 3 条失败，因为科目要求部门」—— 那时他得
	// 一条条翻回去找是哪三条。在指定科目的那一刻就说清楚，
	// 问题的范围就缩小到了「当前这一条」。
	acc, _ := tree.Get(code)
	need := map[string]bool{}
	for _, t := range acc.AuxTypes {
		need[t.Label()] = true
	}
	var missing []string
	if need["客户"] || need["供应商"] || need["股东"] || need["其他单位"] {
		if in.ContactID == nil {
			missing = append(missing, "往来单位")
		}
	}
	if need["部门"] && in.DeptID == nil {
		missing = append(missing, "部门")
	}
	if need["员工"] && in.EmployeeID == nil {
		missing = append(missing, "员工")
	}
	if need["项目"] && in.ProjectID == nil {
		missing = append(missing, "项目")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("科目 %s(%s) 要求「%s」辅助核算，必须填写后才能生成凭证",
			acc.Name, acc.Code, strings.Join(missing, "、"))
	}

	memo := strings.TrimSpace(in.Memo)
	if memo == "" {
		memo = defaultBankMemo(ctx, s, flowID)
	}
	if err := s.db.Bank().SetSuggestion(ctx, flowID, sqlite.Suggestion{
		CounterAccount: code,
		ContactID:      in.ContactID,
		EmployeeID:     in.EmployeeID,
		DeptID:         in.DeptID,
		ProjectID:      in.ProjectID,
		Memo:           memo,
	}); err != nil {
		return nil, err
	}
	// 回读一条，让界面拿到更新后的状态
	list, err := s.BankFlows(ctx, BankFlowQuery{Limit: 100000})
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == flowID {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("流水 id=%d 不存在", flowID)
}

// defaultBankMemo 按流水内容拼一句摘要。
//
// 优先用摘要，其次对方户名，最后退回日期 + 「银行流水」。
// 一层层退是因为凭证行摘要**不能为空**（过账校验会拒绝），
// 而银行流水里三者都可能缺失。
func defaultBankMemo(ctx context.Context, s *Service, flowID int64) string {
	var summary, cp, date sql.NullString
	err := s.db.SQL().QueryRowContext(ctx, `
		SELECT summary, counterparty_name, txn_date FROM bank_flow WHERE id = ?`,
		flowID).Scan(&summary, &cp, &date)
	if err != nil {
		return "银行流水"
	}
	for _, cand := range []string{summary.String, cp.String} {
		if strings.TrimSpace(cand) != "" {
			return strings.TrimSpace(cand)
		}
	}
	if date.String != "" {
		return date.String + " 银行流水"
	}
	return "银行流水"
}
