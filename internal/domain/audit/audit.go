// Package audit 是操作日志的领域模型。
//
// # 依据
//
// 财政部《企业会计信息化工作规范》对会计软件的用户操作日志提了三条要求
// （见〈会计软件的规矩方圆——《企业会计信息化工作规范》解读〉）：
//
//	一是完整性。将所有对会计核算结果可能形成影响的用户操作记录下来 ——
//	         数据录入、修改、插入、删除，以及基础数据（会计科目表、
//	         银行账户、辅助核算项目、人员信息）的维护。
//	二是安全性。采取技术手段保证日志中的任何信息不被用户以任何手段修改和删除。
//	三是可查询性。按操作人员、时间范围、操作内容等条件单独或组合查询。
//
// 并且明确：记录的是**业务层面**的操作（凭证录入/修改、期间开/关、
// 科目增加、未记账凭证删除、取消审核），要记「具体操作内容 + 操作人 +
// 精确到分秒的时间」，而且不同操作要记的内容不同：
//
//	科目增加     → 名称、代码、属性
//	凭证修改     → 修改了哪些项目、修改前后的内容
//	重开已结账期间 → 期间的起止日期
//
// 这个包按这三条来组织：Entry 是「一条业务操作」（完整性），
// Hash/PrevHash 构成链（安全性），Query 是查询条件（可查询性）。
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Action 是业务层面的操作种类。
//
// ★ 刻意用**业务动词**而不是 HTTP/RPC 方法名：
// 「凭证过账」是会计看得懂的事，「PostVoucher」不是。
// 日志是给会计监督人员查的（规范里的原话），不是给程序员看的调用栈。
type Action string

// 操作种类。命名规则：<对象>.<动作>。
const (
	// 账套
	ActionBookCreate  Action = "book.create"
	ActionBookOpen    Action = "book.open"
	ActionBookClose   Action = "book.close"
	ActionBookBackup  Action = "book.backup"
	ActionBookRestore Action = "book.restore"
	ActionBookDemo    Action = "book.demo"

	// 凭证
	ActionVoucherCreate  Action = "voucher.create"
	ActionVoucherUpdate  Action = "voucher.update"
	ActionVoucherDelete  Action = "voucher.delete"
	ActionVoucherPost    Action = "voucher.post"
	ActionVoucherReverse Action = "voucher.reverse"

	// 期间
	ActionPeriodClose  Action = "period.close"
	ActionPeriodReopen Action = "period.reopen"

	// 基础数据（规范点名要记的：科目表、银行账户、辅助核算项目、人员信息）
	ActionAccountSave   Action = "account.save"
	ActionAccountDelete Action = "account.delete"
	ActionContactSave   Action = "contact.save"
	// 删除单列，理由同部门：日志只剩「维护往来单位」时，
	// 事后看不出是改了名字还是把它删了。
	ActionContactDelete  Action = "contact.delete"
	ActionEmployeeSave   Action = "employee.save"
	ActionDepartmentSave Action = "department.save"
	// 部门删除单列一个动作，不并进 Save。
	//
	// 「把销售部删了」和「把销售部改了个名」在事后追责时完全是两件事，
	// 而 Save 那条日志里看不出是哪一种 —— 它只有改后的名字。
	ActionDepartmentDelete Action = "department.delete"

	// 人事异动的三个动作。
	//
	// ★ 也单列，不并进 ActionEmployeeSave。
	//
	// 这三件事各有各的金额与日期含义（改了多少钱、从哪个部门到哪个部门、
	// 哪一天离职），塞进通用的「维护员工档案」之后，日志只剩一句
	// 「改了张三的档案」，而「3 月把他从销售部调到生产部、工资从 8000
	// 调到 9500」这种问题事后必须答得出来 —— 这是工资争议里最常见的一问。
	ActionEmployeeResign   Action = "employee.resign"
	ActionEmployeeTransfer Action = "employee.transfer"
	ActionEmployeeSalary   Action = "employee.salary"
	ActionProjectSave      Action = "project.save"
	ActionBankRuleSave     Action = "bank_rule.save"

	// 业务单据
	ActionBankImport  Action = "bank.import"
	ActionBankPost    Action = "bank.post"
	ActionInvoiceSave Action = "invoice.save"
	ActionInvoicePost Action = "invoice.post"
	ActionClaimSave   Action = "claim.save"
	ActionClaimPost   Action = "claim.post"
	ActionClaimReject Action = "claim.reject"

	// 工资
	ActionPayrollBuild Action = "payroll.build"
	ActionPayrollPost  Action = "payroll.post"
	ActionSchemeSave   Action = "payroll.scheme_save"

	// 税务与 AI
	// ActionBookkeeperSet 是设置本账套的记账人（签章人）。
	ActionBookkeeperSet   Action = "book.bookkeeper_set"
	ActionVATStatusSet    Action = "vat.status_set"
	ActionVATScaleSet     Action = "vat.scale_set"
	ActionVATPolicyImport Action = "vat.policy_import"
	ActionVATPolicyReset  Action = "vat.policy_reset"
	ActionAIAccept        Action = "ai.accept"
	ActionAIReject        Action = "ai.reject"
	ActionAIPromptSave    Action = "ai.prompt_save"
	ActionAIPromptReset   Action = "ai.prompt_reset"
	ActionAIProviderSave  Action = "ai.provider_save"

	// 附件
	ActionAttachmentUpload Action = "attachment.upload"
	ActionAttachmentDelete Action = "attachment.delete"

	// 查看（可开关，默认只记业务操作）
	ActionViewReport         Action = "view.report"
	ActionViewLedger         Action = "view.ledger"
	ActionViewAging          Action = "view.aging"
	ActionViewStatement      Action = "view.statement"
	ActionViewReconciliation Action = "view.reconciliation"
	ActionViewSummary        Action = "view.summary"
	ActionViewColumnar       Action = "view.columnar"
	ActionViewVouchers       Action = "view.vouchers"
	ActionViewInvoices       Action = "view.invoices"
	ActionViewClaims         Action = "view.claims"
	ActionViewBankFlows      Action = "view.bank_flows"
	ActionViewPayroll        Action = "view.payroll"
	ActionViewAuditLog       Action = "view.audit_log"

	// 系统
	ActionOperationFailed Action = "system.failed"
)

var actionLabels = map[Action]string{
	ActionBookCreate:  "建立账套",
	ActionBookOpen:    "打开账套",
	ActionBookClose:   "关闭账套",
	ActionBookBackup:  "备份账套",
	ActionBookRestore: "恢复账套",
	ActionBookDemo:    "生成演示账套",

	ActionVoucherCreate:  "录入凭证",
	ActionVoucherUpdate:  "修改凭证",
	ActionVoucherDelete:  "删除凭证",
	ActionVoucherPost:    "凭证过账",
	ActionVoucherReverse: "红字冲销",

	ActionPeriodClose:  "结账",
	ActionPeriodReopen: "反结账",

	ActionAccountSave:      "维护会计科目",
	ActionAccountDelete:    "删除会计科目",
	ActionContactSave:      "维护往来单位",
	ActionContactDelete:    "删除往来单位",
	ActionEmployeeSave:     "维护员工档案",
	ActionDepartmentSave:   "维护部门",
	ActionDepartmentDelete: "删除部门",

	ActionEmployeeResign:   "员工离职",
	ActionEmployeeTransfer: "员工转部门",
	ActionEmployeeSalary:   "员工调薪",
	ActionProjectSave:      "维护项目",
	ActionBankRuleSave:     "维护匹配规则",

	ActionBankImport:  "导入银行流水",
	ActionBankPost:    "银行流水生成凭证",
	ActionInvoiceSave: "维护发票",
	ActionInvoicePost: "发票生成凭证",
	ActionClaimSave:   "维护报销单",
	ActionClaimPost:   "报销单生成凭证",
	ActionClaimReject: "驳回报销单",

	ActionPayrollBuild: "生成工资表",
	ActionPayrollPost:  "工资表过账",
	ActionSchemeSave:   "维护社保方案",

	ActionBookkeeperSet:   "设置记账人",
	ActionVATStatusSet:    "变更增值税纳税人身份",
	ActionVATScaleSet:     "变更企业规模类型",
	ActionVATPolicyImport: "导入税率政策表",
	ActionVATPolicyReset:  "重置税率政策表",
	ActionAIAccept:        "采纳 AI 建议",
	ActionAIReject:        "拒绝 AI 建议",
	ActionAIPromptSave:    "修改 AI 提示词",
	ActionAIPromptReset:   "重置 AI 提示词",
	ActionAIProviderSave:  "修改模型服务配置",

	ActionAttachmentUpload: "上传附件",
	ActionAttachmentDelete: "删除附件",

	ActionViewReport:         "查看报表",
	ActionViewLedger:         "查看明细账",
	ActionViewAging:          "查看账龄分析",
	ActionViewStatement:      "查看往来对账单",
	ActionViewReconciliation: "查看银行余额调节表",
	ActionViewSummary:        "查看凭证汇总表",
	ActionViewColumnar:       "查看多栏式明细账",
	ActionViewVouchers:       "查看凭证列表",
	ActionViewInvoices:       "查看发票台账",
	ActionViewClaims:         "查看报销单",
	ActionViewBankFlows:      "查看银行流水",
	ActionViewPayroll:        "查看工资表",
	ActionViewAuditLog:       "查看操作日志",

	ActionOperationFailed: "操作失败",
}

// Label 返回中文名。
func (a Action) Label() string {
	if s, ok := actionLabels[a]; ok {
		return s
	}
	return string(a)
}

// AllActions 返回全部操作种类（按标签排序），供界面做筛选下拉。
func AllActions() []Action {
	out := make([]Action, 0, len(actionLabels))
	for a := range actionLabels {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label() < out[j].Label() })
	return out
}

// Category 是日志的类别，用来把「查看」和「业务操作」分开。
//
// ★ 为什么要有这一维：规范要的是「业务层面的操作」（录入、修改、结账…），
// 而用户还希望对「谁在什么时候看了哪张报表」也留痕。两类日志的价值不同、
// 体量差好几个数量级（一天几十条 vs 一天几百条），混在一起会让
// 真正要审计的那部分被淹没。所以给一个类别维度，可以分别查看、分别导出。
type Category string

// 类别取值。
const (
	// CategoryBusiness 是会影响账簿的业务操作（规范要求的那一类）。
	CategoryBusiness Category = "business"
	// CategoryRead 是查看/查询操作（看报表、翻明细账…）。
	CategoryRead Category = "read"
	// CategorySystem 是程序自身的运行事件（启动、失败）。
	CategorySystem Category = "system"
)

// Label 返回中文名。
func (c Category) Label() string {
	switch c {
	case CategoryRead:
		return "查看"
	case CategorySystem:
		return "系统"
	default:
		return "业务操作"
	}
}

// AllCategories 返回全部类别。
func AllCategories() []Category {
	return []Category{CategoryBusiness, CategoryRead, CategorySystem}
}

// ValidCategory 判断类别是否合法。
func ValidCategory(c Category) bool {
	for _, k := range AllCategories() {
		if c == k {
			return true
		}
	}
	return false
}

// Source 是操作来自哪里。
type Source string

// 操作来源。
const (
	SourceGUI    Source = "gui"
	SourceCLI    Source = "cli"
	SourceAI     Source = "ai"
	SourceSystem Source = "system"
)

// Label 返回中文名。
func (s Source) Label() string {
	switch s {
	case SourceGUI:
		return "界面"
	case SourceCLI:
		return "命令行"
	case SourceAI:
		return "AI"
	case SourceSystem:
		return "系统"
	}
	return string(s)
}

// Result 是操作结果。
type Result string

// 结果取值。
const (
	ResultOK     Result = "ok"
	ResultFailed Result = "failed"
)

// Label 返回中文名。
func (r Result) Label() string {
	if r == ResultFailed {
		return "失败"
	}
	return "成功"
}

// Entry 是一条操作日志。
//
// ★ 字段是照着规范那三条要求挑的，不是「有什么记什么」：
//   - 操作人 / 精确到秒的时间 → Operator + At
//   - 具体操作内容 → Action + Summary + Detail（before/after）
//   - 对象 → Entity + EntityID（凭证号、科目编码、期间…）
//   - 完整性：即使操作失败也记（Result=failed），失败也是线索
type Entry struct {
	// Seq 是全局递增序号，跨文件连续。链的顺序依据。
	Seq int64 `json:"seq"`
	// At 是操作时间，精确到秒（RFC3339）。
	At string `json:"at"`
	// Operator 是操作人。★ 记账责任落在自然人身上。
	Operator string `json:"operator"`
	// Source 是操作来源（界面/命令行/AI/系统）。
	Source Source `json:"source"`
	// Action 是业务操作种类。
	Action Action `json:"action"`
	// Category 是类别（业务操作 / 查看 / 系统）。
	Category Category `json:"category"`
	// Summary 是一句人话，如「记-2025-03-0001 过账」。
	Summary string `json:"summary"`
	// Entity / EntityID 是操作对象，如 voucher / 12。
	Entity   string `json:"entity"`
	EntityID string `json:"entityId"`
	// Detail 是操作内容明细（修改前后的值等）。
	Detail map[string]any `json:"detail,omitempty"`
	// Result / Message 是结果与失败原因。
	Result  Result `json:"result"`
	Message string `json:"message,omitempty"`
	// Book / Company 是操作发生在哪个账套 —— 日志与账套分开存，
	// 不记这个就分不清「哪本账的凭证被删了」。
	Book    string `json:"book"`
	Company string `json:"company,omitempty"`
	// AppVersion 记录写入时的程序版本，便于排查「升级后才出现的问题」。
	AppVersion string `json:"appVersion,omitempty"`
	// PrevHash / Hash 构成防篡改链。
	PrevHash string `json:"prevHash"`
	Hash     string `json:"hash"`
}

// Canonical 返回参与哈希的规范串。
//
// ★ 哈希什么很关键：必须是**稳定、可重复**的输入，同一行永远算出同一个值。
// 所以字段顺序固定、用 \x1f 分隔（不会出现在正文里）、
// Detail 用平铺后的键值对而不是原始 JSON 文本
// （JSON 的键顺序、空白、数字格式都会漂移）。
func (e Entry) Canonical(prev string) string {
	var b strings.Builder
	write := func(s string) {
		b.WriteString(s)
		b.WriteByte(0x1f)
	}
	write(prev)
	write(fmt.Sprintf("%d", e.Seq))
	write(e.At)
	write(e.Operator)
	write(string(e.Source))
	write(string(e.Action))
	write(string(e.Category))
	write(e.Summary)
	write(e.Entity)
	write(e.EntityID)
	write(string(e.Result))
	write(e.Message)
	write(e.Book)
	write(e.Company)
	write(e.AppVersion)
	// Detail 展平成有序键值对：map 的遍历顺序是随机的，
	// 直接 json.Marshal 一个 map 也会有键序问题（Go 会排序，但值里的
	// 嵌套 map 同样要排）——这里统一走 flatten，保证确定性。
	for _, kv := range flatten(e.Detail, "") {
		write(kv)
	}
	return b.String()
}

// ComputeHash 计算本条日志的链式哈希。
func (e Entry) ComputeHash(prev string) string {
	sum := sha256.Sum256([]byte(e.Canonical(prev)))
	return hex.EncodeToString(sum[:])
}

// flatten 把嵌套 map 展平成 "a.b=值" 的有序切片。
//
// 用 fmt.Sprintf("%v") 而不是 JSON：值可能含 map/slice，
// JSON 序列化对 nil 与空值的处理在不同版本间会变，
// 而 %v 的输出对同一份数据是稳定的（且我们只要求可重复，不要求可解析 ——
// 明细本身另有 JSON 列存着）。
func flatten(m map[string]any, prefix string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(m))
	for _, k := range keys {
		full := k
		if prefix != "" {
			full = prefix + "." + k
		}
		switch v := m[k].(type) {
		case map[string]any:
			out = append(out, flatten(v, full)...)
		case []any:
			for i, item := range v {
				if sub, ok := item.(map[string]any); ok {
					out = append(out, flatten(sub, fmt.Sprintf("%s[%d]", full, i))...)
					continue
				}
				out = append(out, fmt.Sprintf("%s[%d]=%v", full, i, item))
			}
		default:
			out = append(out, fmt.Sprintf("%s=%v", full, v))
		}
	}
	return out
}

// genesis 是链的起点（第一个文件的 prev_hash）。
const genesis = "genesis"

// Genesis 返回链起点的 prev_hash。
func Genesis() string { return genesis }

// NowStamp 返回写入日志用的时间戳（本地时间，精确到秒）。
//
// ★ 用本地时间而不是 UTC：会计看的是「几月几号几点做的这笔账」，
// 与凭证上的日期、期间是同一套时间观念。时区偏移一并写进去，
// 换时区后仍能还原成绝对时刻。
func NowStamp(t time.Time) string {
	return t.Format("2006-01-02 15:04:05-07:00")
}

// ---------------------------------------------------------------------------
// 查询
// ---------------------------------------------------------------------------

// Query 是日志查询条件。零值表示「不加这个条件」。
//
// 对应规范的「可查询性」：按操作人员、时间范围、操作内容，
// 分别或**组合**查询。
type Query struct {
	// Operator 精确匹配操作人。
	Operator string
	// From / To 是时间范围（含端点，YYYY-MM-DD 或完整时间戳）。
	From string
	To   string
	// Action 精确匹配操作种类（可多个，任一命中）。
	Actions []Action
	// Categories 过滤类别（任一命中）；为空表示不限。
	//
	// ★ 默认界面上是「业务操作 + 系统」，把海量的「查看」挡在外面 ——
	// 想看的时候再勾上。
	Categories []Category
	// Result 过滤结果（ok / failed）。
	Result Result
	// Text 是全文关键字，匹配摘要、对象 id、失败原因。
	Text string
	// Book 限定账套路径。
	Book string
	// Limit / Offset 分页。
	Limit  int
	Offset int
}

// Page 是一页日志。
type Page struct {
	Entries []Entry `json:"entries"`
	// Total 是符合条件的总条数（分页用）。
	Total int `json:"total"`
	// Segments 是当前共有几个日志文件。
	Segments int `json:"segments"`
	// Bytes 是日志占用的总字节数。
	Bytes int64 `json:"bytes"`
	// Truncated 为真表示结果被 Limit 截断。
	Truncated bool `json:"truncated"`
}

// VerifyIssue 是一条被破坏的记录。
type VerifyIssue struct {
	Seq    int64  `json:"seq"`
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// VerifyResult 是完整性校验结果。
type VerifyResult struct {
	// OK 为真表示整条链完好。
	OK bool `json:"ok"`
	// Checked 是校验过的记录数。
	Checked int `json:"checked"`
	// Files 是校验过的文件数。
	Files int `json:"files"`
	// Issues 是发现的问题（最多前若干条）。
	Issues []VerifyIssue `json:"issues"`
	// Head 是链头哈希，可与外部留存的副本比对。
	Head string `json:"head"`
}
