/**
 * Wails 绑定的一层薄封装。
 *
 * # 为什么需要它
 *
 * 1. **开发态没有 Wails 运行时**：`vite dev` 直接开浏览器时 window.go 不存在，
 *    这里给出一份带演示数据的假实现，让界面能在浏览器里调样式与交互，
 *    不必每次都 `wails dev` 起一整个原生窗口。
 *
 * 2. **统一处理 Fault**：Go 侧所有绑定返回 `(结果, *Fault)`。
 *    业务失败（体检没过、期间已结账）与程序错误要分开呈现，
 *    不应该每个调用点各写一遍 if (fault)。
 *
 * ---------------------------------------------------------------------------
 * ★ Wails v2 的绑定到底是什么形状（这个搞错过一次，整个界面全废）
 * ---------------------------------------------------------------------------
 *
 * 生成的 App.d.ts 写得很清楚：
 *
 *     export function Overview(): Promise<service.Dashboard>;
 *
 * 也就是 **Promise<值>**，不是 `Promise<[值, 错误]>`。
 *
 *   - 成功：promise resolve，值是 Go 方法的第一个返回值（**没有包成数组**）
 *   - 失败：promise **reject**，值是 Go 侧 `err.Error()` 的字符串
 *
 * 证据在 Wails 源码里（internal/binding/boundMethod.go）：
 *
 *     case 2:
 *         returnValue = callResults[0].Interface()   // ← 直接就是那个值
 *         if temp, ok := callResults[1].Interface().(error); ok { err = temp }
 *
 * 没有任何一步把它包成数组。而 dispatcher 只是
 * `callbackMessage.Result = result`，JS 侧 `callbackData.resolve(message.result)`。
 *
 * 之前这里写的是 `const [data, fault] = await fn(...args)`（按元组解构），
 * 后果是**每一次调用都出错**：
 *   - 返回对象的绑定 → 解构一个对象 → `TypeError: {} is not iterable`
 *   - 返回字符串的绑定 → 字符串可解构成字符 → 静默拿到一堆垃圾
 *   - 返回数组的绑定 → 解构成「第一个元素 + 第二个元素」，**看起来没报错但是错的**
 *
 * 之所以一直没被发现：开发态用的 mockApp 是照元组写的，
 * 而它跟真实运行时**不是一套约定**。所以下面 mockApp 也改成真实形状 ——
 * 假实现必须和真实现遵守同一份契约，否则它掩盖的正是它该暴露的问题。
 *
 * 另一个必须记住的点：Go 侧把 Fault **编码成 JSON 字符串**塞进 error 里
 * （见 desktop/app.go 的 Fault.Error()），所以 reject 的值要先试着 JSON.parse，
 * 否则界面拿到的永远是 `{"kind":"no_book",...}` 这一整串，而不是「尚未打开账套」。
 */
import { reactive } from 'vue'

/** 是否运行在 Wails 运行时里。每次判断，不做缓存。 */
export function hasWails() {
  return typeof window !== 'undefined' && !!window.go?.main?.App
}

/** 开发态（vite dev / 直接开浏览器）才允许退回假数据。 */
const DEV = typeof import.meta !== 'undefined' && !!import.meta.env?.DEV

// ★ 只在**开发态**才允许没有运行时。
//
// 生产构建里 window.go 缺失意味着「界面拿不到账本，只能用假数据」——
// 而假数据是一套像模像样的公司名和金额。宁可页面上写「绑定不可用」，
// 也绝不能让用户对着演示数字记账。
//
// 判断放在**每次调用时**而不是模块加载时：那个值一旦缓存进模块作用域，
// 运行时晚一步注入（或注入失败）就永久走假数据分支，而且没有任何提示。

/** 全局错误提示队列。界面顶部挂一个 Toast 读它。 */
export const notices = reactive({ items: [] })

// ---------------------------------------------------------------------------
// 提示条停留多久
// ---------------------------------------------------------------------------
//
// **所有**提示都会自动收起，区别只在时间：
//
//   - 信息类（success / info）：**3 秒**。看一眼就够 ——
//     「已建好」「已复制」这种，多留一秒都是挡住下面的界面。
//   - 问题类（warn / error）：**5 秒**。要读的东西更多：
//     错误里常常带 detail（体检没过的那几项、勾稽差额、Go 侧的原始报错），
//     3 秒读不完。
//
// 认不出的 kind 一律按问题类处理（时间长的那一档）——
// kind 写错时宁可多留一会儿，也不要让一条没人见过的提示一闪而过。
//
// 想调就改这两个常量，别在各处散着写。用户在别处看到的「5 秒」
// 全在这里一个地方。
const TOAST_MS_INFO = 3000
const TOAST_MS_ISSUE = 5000

/** 信息类：看一眼就够的那些。其余都按问题类算。 */
const INFO_KINDS = new Set(['success', 'info'])

function toastTTL(kind) {
  return INFO_KINDS.has(kind) ? TOAST_MS_INFO : TOAST_MS_ISSUE
}

let seq = 0
export function notify(message, kind = 'error', detail = '') {
  const id = ++seq
  notices.items.push({ id, message, kind, detail })
  setTimeout(() => dismiss(id), toastTTL(kind))
  return id
}
export function dismiss(id) {
  const i = notices.items.findIndex((n) => n.id === id)
  if (i >= 0) notices.items.splice(i, 1)
}

/**
 * 调用一个 Go 绑定方法。
 *
 * 返回 `{ ok, data, fault }`。**不抛异常**：
 * 界面代码里到处写 try/catch 会把业务失败和代码 bug 混在一起，
 * 而这两者的处理方式完全不同。
 */
export async function call(fn, ...args) {
  try {
    // ★ 就是这一行：Wails 的绑定 resolve 的是**值本身**。
    // 写成 `const [data, fault] = ...` 会让每一次调用都失败（见文件头说明）。
    const data = await fn(...args)
    return { ok: true, data }
  } catch (e) {
    // 走到这里有两类原因，必须分开：
    //   - Go 侧返回了业务错误（reject 的值是 Fault 的 JSON）
    //   - 绑定本身出错：方法名写错、参数序列化失败、界面代码抛异常
    return { ok: false, fault: toFault(e) }
  }
}

/**
 * 把 reject 的值翻译成 Fault。
 *
 * Go 侧 Fault.Error() 返回的是 JSON（见 desktop/app.go），
 * 因为 Wails 只能把 error 的**字符串**透过来，Kind 与 Detail 会丢。
 * 这里把它解回来 —— 少了这一步，界面拿到的是
 * `{"kind":"no_book","message":"尚未打开账套"}` 这一整串，
 * 既看不懂，也没法按 kind 做分支（跳建账向导 / 高亮字段 / 摊开体检报告）。
 */
export function toFault(e) {
  // ① Go 侧的 Fault：JSON 字符串
  if (typeof e === 'string') {
    const parsed = tryParseFault(e)
    if (parsed) return parsed
    return { kind: 'error', message: e }
  }
  // ② JS 自己的异常（绑定名写错、解构失败、视图里的 bug）
  if (e instanceof Error) {
    return { kind: 'internal', message: e.message, detail: e.stack ?? '' }
  }
  // ③ 已经是对象（开发态 mock 直接 throw Fault 对象）
  if (e && typeof e === 'object') {
    if (typeof e.kind === 'string' && typeof e.message === 'string') return e
    if (typeof e.message === 'string') return { kind: 'internal', message: e.message }
  }
  return { kind: 'internal', message: String(e) }
}

function tryParseFault(s) {
  const t = s.trim()
  if (!t.startsWith('{')) return null
  try {
    const v = JSON.parse(t)
    if (v && typeof v === 'object' && typeof v.kind === 'string' &&
        typeof v.message === 'string') {
      return v
    }
  } catch {
    // 不是 JSON：当普通消息用
  }
  return null
}

/** 取绑定对象。 */
function A() {
  if (hasWails()) return window.go.main.App
  if (DEV) return mockApp
  // 生产构建里没有运行时：**不要**悄悄用假数据顶上。
  return missingRuntime
}

/**
 * 没有运行时时的兜底对象。
 *
 * 每个方法都返回一个 rejected promise，值是一条 Fault 的 JSON ——
 * 与真实运行时的失败形状**完全一致**，所以界面走的是同一条错误分支，
 * 而不是在半路崩掉。
 */
const missingRuntime = new Proxy({}, {
  get: () => () => Promise.reject(JSON.stringify({
    kind: 'internal',
    message: '界面与账本程序的连接没有建立（window.go 不存在）。' +
      '请用打包好的程序打开，或把软件重新启动一次。',
  })),
})

export const api = {
  currentBook: () => call(A().CurrentBook),
  openBook: (path) => call(A().OpenBook, path),
  closeBook: () => call(A().CloseBook),
  createBook: (input) => call(A().CreateBook, input),
  accounts: () => call(A().Accounts),
  saveAccount: (input) => call(A().SaveAccount, input),
  setAccountEnabled: (code, enabled) => call(A().SetAccountEnabled, code, enabled),
  deleteAccount: (code) => call(A().DeleteAccount, code),
  accountKinds: () => call(A().AccountKinds),
  defaultBookDir: () => call(A().DefaultBookDir),
  listBooks: () => call(A().ListBooks),
  suggestBookPath: (companyName) => call(A().SuggestBookPath, companyName),
  // 系统文件管理器：打开已有账套时挑文件、新建账套时挑目录、
  // 以及在 Finder / 资源管理器里打开账套目录。
  // 用户取消时返回空串（**不是错误**）。
  chooseBookFile: () => call(A().ChooseBookFile),
  chooseBookDir: (currentPath) => call(A().ChooseBookDir, currentPath),
  revealBookDir: (dir) => call(A().RevealBookDir, dir),

  overview: () => call(A().Overview),
  periods: () => call(A().Periods),
  health: (req) => call(A().Health, req),
  previewClose: (req) => call(A().PreviewClose, req),
  close: (req) => call(A().Close, req),
  reopen: (req) => call(A().Reopen, req),

  vouchers: (req) => call(A().Vouchers, req),
  voucherDetail: (id) => call(A().VoucherDetail, id),
  voucherMetaInfo: () => call(A().VoucherMetaInfo),
  saveVoucher: (req) => call(A().SaveVoucher, req),
  saveAndPost: (req) => call(A().SaveAndPost, req),
  postVoucher: (id, by) => call(A().PostVoucher, id, by),
  deleteVoucher: (id) => call(A().DeleteVoucher, id),
  reverseVoucher: (req) => call(A().ReverseVoucher, req),
  checkVoucher: (req) => call(A().CheckVoucher, req),
  accountOptions: () => call(A().AccountOptions),
  contactOptions: () => call(A().ContactOptions),

  // 银行流水
  bankFlows: (req) => call(A().BankFlows, req),
  bankStats: () => call(A().BankStats),
  importBankFlows: (req) => call(A().ImportBankFlows, req),
  matchBankFlows: () => call(A().MatchBankFlows),
  postBankFlows: (req) => call(A().PostBankFlows, req),
  ignoreBankFlow: (id, reason) => call(A().IgnoreBankFlow, id, reason),
  setBankSuggestion: (req) => call(A().SetBankSuggestion, req),
  saveBankRule: (req) => call(A().SaveBankRule, req),
  bankRules: () => call(A().BankRules),

  // 附件
  attachments: (req) => call(A().Attachments, req),
  uploadAttachment: (req) => call(A().UploadAttachment, req),
  removeAttachment: (req) => call(A().RemoveAttachment, req),
  orphanAttachments: () => call(A().OrphanAttachments),
  attachmentPath: (hash) => call(A().AttachmentPath, hash),

  // 发票
  invoices: (req) => call(A().Invoices, req),
  saveInvoice: (req) => call(A().SaveInvoice, req),
  postInvoice: (req) => call(A().PostInvoice, req),
  invoiceSummary: (dir, from, to) => call(A().InvoiceSummary, dir, from, to),
  invoiceRates: () => call(A().InvoiceRates),
  invoiceCategories: () => call(A().InvoiceCategories),

  // 报销
  claims: (req) => call(A().Claims, req),
  claimDetail: (id) => call(A().ClaimDetail, id),
  saveClaim: (req) => call(A().SaveClaim, req),
  approveClaim: (req) => call(A().ApproveClaim, req),
  rejectClaim: (req) => call(A().RejectClaim, req),
  postClaim: (req) => call(A().PostClaim, req),
  claimCategories: () => call(A().ClaimCategories),

  // 往来单位
  contactList: () => call(A().ContactList),
  saveContact: (req) => call(A().SaveContact, req),

  // 工资
  employees: (onlyEnabled) => call(A().Employees, onlyEnabled),
  saveEmployee: (req) => call(A().SaveEmployee, req),
  // 人事异动：三件事各是一个动作，而不是「让用户去编辑框里改」。
  // 每件事都有规则要校验（离职日期不能早于入职、调入的部门必须启用、
  // 工资不能填 0），而且都要在操作日志里留下能回答
  // 「什么时候从哪个部门调到哪个部门、工资改了多少」的记录。
  resignEmployee: (req) => call(A().ResignEmployee, req),
  transferEmployee: (req) => call(A().TransferEmployee, req),
  adjustSalary: (req) => call(A().AdjustSalary, req),
  payrollRuns: () => call(A().PayrollRuns),
  payrollRunDetail: (id) => call(A().PayrollRunDetail, id),
  buildPayroll: (req) => call(A().BuildPayroll, req),
  postPayroll: (id, by) => call(A().PostPayroll, id, by),
  taxTableInfo: () => call(A().TaxTableInfo),
  insuranceSchemes: () => call(A().InsuranceSchemes),
  schemeTemplate: (name) => call(A().SchemeTemplate, name),
  saveInsuranceSchemes: (list) => call(A().SaveInsuranceSchemes, list),
  departments: () => call(A().Departments),
  saveDepartment: (req) => call(A().SaveDepartment, req),
  deleteDepartment: (id) => call(A().DeleteDepartment, id),
  departmentUsageOf: (id) => call(A().DepartmentUsageOf, id),

  // 导出
  exportReport: (req) => call(A().ExportReport, req),
  suggestExportPath: (kind, year, month) => call(A().SuggestExportPath, kind, year, month),

  agingReport: (req) => call(A().AgingReport, req),
  statement: (req) => call(A().Statement, req),
  activeContacts: (from, to) => call(A().ActiveContacts, from, to),

  summary: (req) => call(A().Summary, req),
  summaryByPeriod: (y, m) => call(A().SummaryByPeriod, y, m),
  exportSummary: (req, dest) => call(A().ExportSummary, req, dest),

  // 增值税政策与纳税人身份
  vatPolicies: () => call(A().VATPolicies),
  resolveVATRate: (q) => call(A().ResolveVATRate, q),
  judgeDeduction: (req) => call(A().JudgeDeduction, req),
  vatReference: () => call(A().VATReference),
  vatIdentity: () => call(A().VATIdentity),
  setVATStatus: (req) => call(A().SetVATStatus, req),
  setEnterpriseScale: (scale) => call(A().SetEnterpriseScale, scale),
  exportVATPolicies: (dest) => call(A().ExportVATPolicies, dest),
  importVATPolicies: (src) => call(A().ImportVATPolicies, src),
  vatPolicyStatus: () => call(A().VATPolicyStatus),
  resetVATPolicies: () => call(A().ResetVATPolicies),

  // 演示账套
  createDemoBook: (months) => call(A().CreateDemoBook, months),

  columnar: (req) => call(A().Columnar, req),
  columnarAccounts: () => call(A().ColumnarAccounts),
  exportColumnar: (req, dest) => call(A().ExportColumnar, req, dest),
  suggestColumnarName: (code) => call(A().SuggestedColumnarName, code),

  bankReconciliation: (req) => call(A().BankReconciliation, req),
  bankAccounts: () => call(A().BankAccounts),
  exportReconciliation: (req, dest) => call(A().ExportReconciliation, req, dest),
  suggestReconciliationName: (code) => call(A().SuggestedReconciliationName, code),

  report: (req) => call(A().Report, req),
  ledgerDetail: (req) => call(A().LedgerDetail, req),

  filesDir: () => call(A().FilesDir),
  backup: (req) => call(A().Backup, req),
  inspectBackup: (path) => call(A().InspectBackup, path),
  restoreBackup: (req) => call(A().RestoreBackup, req),
  auditAttachments: (y, m) => call(A().AuditAttachments, y, m),

  aiSuggest: (req) => call(A().AISuggest, req),
  aiConfig: () => call(A().AIConfig),
  saveAIProvider: (cfg) => call(A().SaveAIProvider, cfg),
  aiSuggestions: (limit) => call(A().AISuggestions, limit),
  acceptAISuggestion: (req) => call(A().AcceptAISuggestion, req),
  rejectAISuggestion: (req) => call(A().RejectAISuggestion, req),
  auditLog: (q) => call(A().AuditLog, q),
  bookkeeper: () => call(A().Bookkeeper),
  saveBookkeeper: (name) => call(A().SaveBookkeeper, name),
  auditSettings: () => call(A().AuditSettings),
  saveAuditSettings: (s) => call(A().SaveAuditSettings, s),
  verifyAuditLog: () => call(A().VerifyAuditLog),
  exportAuditLog: (q, dest) => call(A().ExportAuditLog, q, dest),
  aiAgentSources: () => call(A().AIAgentSources),
  startAIAgentRun: (req) => call(A().StartAIAgentRun, req),
  aiAgentRunStatus: (id) => call(A().AIAgentRunStatus, id),
  latestAIAgentRun: () => call(A().LatestAIAgentRun),
  cancelAIAgentRun: (id) => call(A().CancelAIAgentRun, id),
  acceptAIAgentItem: (req) => call(A().AcceptAIAgentItem, req),
  rejectAIAgentItem: (req) => call(A().RejectAIAgentItem, req),
  aiPromptConfig: () => call(A().AIPromptConfig),
  saveAIPromptConfig: (input) => call(A().SaveAIPromptConfig, input),
  resetAIPromptConfig: () => call(A().ResetAIPromptConfig),
  previewAIPrompt: (task) => call(A().PreviewAIPrompt, task),

  today: () => call(A().Today),
  appVersion: () => call(A().AppVersion),
}



// ---------------------------------------------------------------------------
// 浏览器开发态假实现
// ---------------------------------------------------------------------------

const mockBook = {
  open: true,
  path: '/Users/you/演示账套.db',
  book: {
    companyName: '杭州云帆软件有限公司',
    creditCode: '91330100MA2EXAMPLE',
    legalPerson: '张三',
    taxType: 'general',
    taxTypeLabel: '一般纳税人',
    startPeriod: '2025-01',
    periods: Array.from({ length: 12 }, (_, i) => {
      const m = i + 1
      return {
        year: 2025, month: m,
        label: `2025-${String(m).padStart(2, '0')}`,
        status: m <= 3 ? 'open' : 'future',
        statusLabel: m <= 3 ? '已启用' : '未启用',
        from: `2025-${String(m).padStart(2, '0')}-01`,
        to: `2025-${String(m).padStart(2, '0')}-28`,
        voucherCount: m <= 3 ? 4 + m : 0,
        canClose: m === 1,
        canReopen: false,
      }
    }),
  },
}

// 导出给测试用：假实现必须与真实现遵守同一份契约，
// 所以它需要被测试盯着（frontend/test/api.test.mjs）。
export const mockApp = {
  CurrentBook: async () => mockBook,
  Accounts: async () => ({
    rows: [
      { id: 1, code: '5602', name: '管理费用', fullName: '管理费用', parentCode: '', level: 1,
        isLeaf: false, rootType: 'expense', rootLabel: '费用', balanceDir: 'debit', dirLabel: '借',
        auxTypes: ['dept'], auxLabels: ['部门'], isEnabled: true, isPreset: true, remark: '',
        entryCount: 12, balance: 480000, childCount: 17, canDelete: false, reason: '准则预置科目不能删除，只能停用' },
      { id: 2, code: '560201', name: '管理费用—工资', fullName: '管理费用/工资', parentCode: '5602',
        level: 2, isLeaf: true, rootType: 'expense', rootLabel: '费用', balanceDir: 'debit', dirLabel: '借',
        auxTypes: ['dept'], auxLabels: ['部门'], isEnabled: true, isPreset: true, remark: '',
        entryCount: 6, balance: 360000, childCount: 0, canDelete: false, reason: '准则预置科目不能删除，只能停用' },
      { id: 3, code: '560290', name: '研发费用（自建）', fullName: '管理费用/研发费用（自建）',
        parentCode: '5602', level: 2, isLeaf: true, rootType: 'expense', rootLabel: '费用',
        balanceDir: 'debit', dirLabel: '借', auxTypes: ['dept'], auxLabels: ['部门'],
        isEnabled: true, isPreset: false, remark: '', entryCount: 0, balance: 0, childCount: 0,
        canDelete: true, reason: '' },
    ],
    total: 3, leafCount: 2, maxLevel: 4,
    rootTypes: [{ value: 'expense', label: '费用' }],
    auxTypes: [{ value: 'dept', label: '部门' }],
  }),
  SaveAccount: async () => ({ code: '560290', name: '研发费用（自建）' }),
  SetAccountEnabled: async () => null,
  DeleteAccount: async () => null,
  AccountKinds: async () => ({
    rootTypes: [
      { value: 'asset', label: '资产' }, { value: 'liability', label: '负债' },
      { value: 'equity', label: '所有者权益' }, { value: 'cost', label: '成本' },
      { value: 'income', label: '收入' }, { value: 'expense', label: '费用' },
    ],
    auxTypes: [
      { value: 'customer', label: '客户' }, { value: 'supplier', label: '供应商' },
      { value: 'employee', label: '员工' }, { value: 'dept', label: '部门' },
      { value: 'project', label: '项目' },
    ],
    maxLevel: 4,
  }),
  DefaultBookDir: async () => '/Users/you/.mini-account/dataDB',
  ListBooks: async () => ({
    dir: '/Users/you/.mini-account/dataDB',
    books: [{
      path: '/Users/you/.mini-account/dataDB/hang-zhou-yun-fan-ruan-jian.db',
      fileName: 'hang-zhou-yun-fan-ruan-jian.db', isBook: true,
      companyName: '杭州云帆软件有限公司', creditCode: '91330100MA2EXAMPLE',
      vatStatus: 'small_scale', startPeriod: '2025-01', voucherCount: 14,
      sizeBytes: 409600, modTime: '2026-09-11T18:00:00+08:00',
    }],
    others: [], lastPath: '',
    suggested: '/Users/you/.mini-account/dataDB/hang-zhou-yun-fan-ruan-jian.db',
    hasAny: true,
  }),
  SuggestBookPath: async (name) =>
    `/Users/you/.mini-account/dataDB/${(name || 'book').trim()}.db`,
  OpenBook: async () => mockBook,
  CloseBook: async () => null,
  CreateBook: async () => mockBook,
  // 浏览器开发模式下没有系统文件选择框。给一个能看的样子，
  // 而不是抛错 —— 这里的目的只是看界面长什么样。
  ChooseBookFile: async () => '/Users/you/.mini-account/dataDB/book.db',
  ChooseBookDir: async (current) =>
    `/Users/you/.mini-account/dataDB/${(current || 'book.db').split('/').pop()}`,
  RevealBookDir: async (dir) => dir || '/Users/you/.mini-account/dataDB',
  Overview: async () => ({
    book: mockBook.book,
    currentPeriod: '2025-01', latestClosedPeriod: '',
    assets: 37590000, liabilities: 900000, equity: 36690000,
    periodIncome: 15000000, periodExpense: 4890000, periodProfit: 10110000,
    closingCash: 18180000, draftVouchers: 2, unpostedBankFlows: 5,
    missingAttachments: 0, balanceSheetIssues: [],
  }),
  Periods: async () => mockBook.book,
  Health: async () => ({
    period: '2025-01', canClose: true,
    summary: '2025-01 体检通过（6 项检查，0 项提示）',
    items: [
      { key: 'trial_balance', title: '试算平衡', level: 'ok', detail: '借方合计 372,100.00 = 贷方合计 372,100.00', count: 0 },
      { key: 'draft_vouchers', title: '存在草稿凭证', level: 'warn', detail: '本期有 2 张草稿凭证尚未过账', count: 2 },
      { key: 'voucher_sequence', title: '凭证字号连续', level: 'ok', detail: '', count: 0 },
      { key: 'balance_sheet', title: '资产负债表勾稽', level: 'ok', detail: '资产总计 = 负债和所有者权益总计', count: 0 },
      { key: 'negative_cash', title: '现金及银行存款无贷方余额', level: 'ok', detail: '', count: 0 },
      { key: 'contact_direction', title: '往来余额方向正常', level: 'ok', detail: '', count: 0 },
    ],
    errors: [], warnings: [{ key: 'draft_vouchers', title: '存在草稿凭证', level: 'warn', detail: '本期有 2 张草稿凭证尚未过账', count: 2 }],
  }),
  PreviewClose: async () => ({
    period: '2025-03',
    steps: [
      { key: 'close_pnl', title: '结转损益', detail: '收入 150,000.00，费用 48,900.00，利润 101,100.00（3 个科目）', done: true, skipped: false },
      { key: 'close_year', title: '结转本年利润', detail: '非年度末期间，年末结账时处理', done: false, skipped: true },
    ],
    income: 15000000, expense: 4890000, profit: 10110000,
    entries: [
      { accountCode: '5001', summary: '结转损益 2025年03月', debit: 15000000, credit: 0, auxDesc: '' },
      { accountCode: '560201', summary: '结转损益 2025年03月', debit: 0, credit: 4800000, auxDesc: '部门#1' },
      { accountCode: '560205', summary: '结转损益 2025年03月', debit: 0, credit: 90000, auxDesc: '部门#1' },
      { accountCode: '3103', summary: '结转损益 2025年03月', debit: 0, credit: 10110000, auxDesc: '' },
    ],
    health: null,
  }),
  Close: async () => ({ period: '2025-03', voucherNo: '转-2025-03-0001', voucherId: 16, voucherCreated: true, summary: '2025年03月：收入 150,000.00，费用 48,900.00，利润 101,100.00' }),
  Reopen: async () => ({ period: '2025-03', reversed: ['转-2025-03-0001'], voucherIds: [16] }),
  Vouchers: async () => [
    { id: 1, no: '记-2025-03-0001', word: '记', date: '2025-03-05', remark: '收回货款', status: 'posted', statusLabel: '已记账', attachCount: 1, amount: 10600000, source: 'bank', sourceLabel: '银行流水', createdByAi: false, createdBy: '李会计', postedBy: '王主管', lines: 2 },
    { id: 2, no: '记-2025-03-0002', word: '记', date: '2025-03-12', remark: '销售软件服务', status: 'posted', statusLabel: '已记账', attachCount: 0, amount: 15900000, source: 'manual', sourceLabel: '手工录入', createdByAi: false, createdBy: '李会计', postedBy: '王主管', lines: 3 },
    { id: 3, no: '', word: '记', date: '2025-03-20', remark: '付水电费（待核对）', status: 'draft', statusLabel: '草稿', attachCount: 0, amount: 86000, source: 'ai', sourceLabel: 'AI 建议', createdByAi: true, createdBy: '李会计', postedBy: '', lines: 2 },
  ],
  VoucherDetail: async () => ({
    id: 1, no: '记-2025-03-0001', word: '记', date: '2025-03-05', remark: '收回货款',
    status: 'posted', statusLabel: '已记账', attachCount: 1,
    source: 'bank', sourceLabel: '银行流水', createdByAi: false,
    createdBy: '李会计', postedBy: '王主管',
    lines: [
      { lineNo: 1, accountCode: '1002', accountName: '银行存款', summary: '收回货款', debit: 10600000, credit: 0, auxDesc: '' },
      { lineNo: 2, accountCode: '1122', accountName: '应收账款', summary: '收回货款', debit: 0, credit: 10600000, auxDesc: '杭州云帆科技有限公司' },
    ],
    totalDebit: 10600000, totalCredit: 10600000, balanced: true,
    canEdit: false, canDelete: false, canPost: false, canReverse: true,
    blockedReason: '凭证已过账。已记账的凭证不能直接修改，如需更正请用红字冲销 —— 这是《会计基础工作规范》的要求。',
  }),
  VoucherMetaInfo: async () => ({
    accounts: [
      { code: '1001', name: '库存现金', fullName: '库存现金', direction: '借', auxTypes: [], searchText: '1001 库存现金' },
      { code: '1002', name: '银行存款', fullName: '银行存款', direction: '借', auxTypes: [], searchText: '1002 银行存款' },
      { code: '1122', name: '应收账款', fullName: '应收账款', direction: '借', auxTypes: ['客户'], searchText: '1122 应收账款' },
      { code: '2202', name: '应付账款', fullName: '应付账款', direction: '贷', auxTypes: ['供应商'], searchText: '2202 应付账款' },
      { code: '5001', name: '主营业务收入', fullName: '主营业务收入', direction: '贷', auxTypes: [], searchText: '5001 主营业务收入' },
      { code: '560210', name: '管理费用—租赁费', fullName: '管理费用—租赁费', direction: '借', auxTypes: ['部门'], searchText: '560210 管理费用—租赁费' },
    ],
    contacts: [
      { id: 1, name: '杭州云帆科技有限公司', kind: 'customer', kindLabel: '客户', shortName: '云帆科技' },
      { id: 2, name: '宁波恒信办公用品有限公司', kind: 'supplier', kindLabel: '供应商', shortName: '' },
    ],
    words: ['记', '收', '付', '转'],
    currentPeriod: '2025-01', today: '2025-03-11',
  }),
  SaveVoucher: async () => ({ id: 9, no: '', status: 'draft', statusLabel: '草稿' }),
  SaveAndPost: async () => ({ id: 9, no: '记-2025-03-0009', status: 'posted', statusLabel: '已记账' }),
  PostVoucher: async () => ({ id: 9, no: '记-2025-03-0009', status: 'posted' }),
  DeleteVoucher: async () => null,
  ReverseVoucher: async () => ({ id: 10, no: '记-2025-03-0010' }),
  CheckVoucher: async () => ({ ok: true, message: '校验通过：2 条分录，借 106,000.00 = 贷 106,000.00，期间 2025-03' }),
  AccountOptions: async () => [],
  ContactOptions: async () => [],
  BankFlows: async () => [
    { id: 1, date: '2025-03-05', amount: 10600000, direction: 'in', directionLabel: '收入', counterpartyName: '杭州云帆科技有限公司', summary: '收到货款', status: 'imported', statusLabel: '待匹配', counterAccount: '', matchLayer: '', matchLayerLabel: '', confidence: 0 },
    { id: 2, date: '2025-03-12', amount: 3000000, direction: 'out', directionLabel: '支出', counterpartyName: '杭州某某物业', summary: '支付房租', status: 'matched', statusLabel: '待确认', counterAccount: '560210', matchLayer: 'rule', matchLayerLabel: '规则', confidence: 1 },
  ],
  BankStats: async () => ({ imported: 1, matched: 1, posted: 0, ignored: 0 }),
  ImportBankFlows: async () => ({ importId: 1, total: 3, inserted: 3, duplicated: 0, totalIn: 10600000, totalOut: 3600000, from: '2025-03-05', to: '2025-03-20', encoding: 'UTF-8', mapping: '收入/支出两列', parseErrors: [] }),
  MatchBankFlows: async () => ({ total: 3, matched: 2, byLayer: { rule: 2 }, unmatched: [1] }),
  PostBankFlows: async () => ({ created: 2, skipped: 0, failures: [] }),
  IgnoreBankFlow: async () => null,
  SetBankSuggestion: async () => ({ id: 1, counterAccount: '560206' }),
  SaveBankRule: async () => 1,
  BankRules: async () => [],
  Employees: async () => [
    { id: 1, code: 'E001', name: '张三', baseSalary: 1200000, siBase: 1200000, hfbBase: 1200000, specialAdditional: 200000, siProfile: '杭州标准', enabled: true, statusLabel: '在职', hireDate: '2024-03-01' },
  ],
  SaveEmployee: async () => 1,
  PayrollRuns: async () => [
    { id: 1, period: '2025-03', status: 'draft', statusLabel: '草稿', headcount: 3, totalGross: 3600000, totalIit: 12300, totalSiSelf: 380000, totalSiCompany: 900000, totalNet: 3207700, hasAccrualVoucher: false, hasPaymentVoucher: false },
  ],
  PayrollRunDetail: async () => ({ id: 1, period: '2025-03', status: 'draft', statusLabel: '草稿', taxNote: '综合所得 7 级超额累进（累计预扣预缴法）', headcount: 1, totalGross: 1200000, totalIit: 3900, totalSiSelf: 126000, totalSiCompany: 300000, totalNet: 1070100, canConfirm: true, canPost: true, items: [{ employeeId: 1, employeeName: '张三', gross: 1200000, attendanceDeduct: 0, otherDeduct: 0, insuranceSelf: 126000, insuranceCompany: 300000, specialAdditional: 200000, taxableIncome: 874000, iit: 3900, net: 1070100 }] }),
  BuildPayroll: async () => null,
  PostPayroll: async () => null,
  Attachments: async () => [
    { hash: 'a'.repeat(64), name: '发票-202503.pdf', size: 182734, mime: 'application/pdf', path: '/tmp/.files/aa/aaa', missing: false },
  ],
  UploadAttachment: async () => ({ hash: 'b'.repeat(64), name: 'x.pdf', size: 100, mime: 'application/pdf', path: '/tmp/x' }),
  RemoveAttachment: async () => null,
  OrphanAttachments: async () => [],
  AttachmentPath: async () => '/tmp/x',
  Invoices: async () => [
    { id: 1, direction: 'input', directionLabel: '进项', kind: 'special', kindLabel: '增值税专用发票', code: '3300201130', number: '12345678', invoiceDate: '2025-03-08', sellerName: '宁波恒信办公用品有限公司', buyerName: '杭州云帆软件有限公司', amountExTax: 300000, taxRatePpm: 130000, taxRateLabel: '13%', taxAmount: 39000, totalAmount: 339000, deductibleTax: 39000, costAmount: 300000, status: 'registered', statusLabel: '已登记', posted: false, voucherNo: '', attachmentCount: 0 },
  ],
  SaveInvoice: async () => ({ id: 1 }),
  PostInvoice: async () => ({ id: 1, voucherNo: '记-2025-03-0001' }),
  InvoiceSummary: async () => ({ count: 1, amountExTax: 300000, taxAmount: 39000, totalAmount: 339000, deductible: 39000, unpostedCount: 1 }),
  InvoiceRates: async () => [{ ppm: 130000, label: '13%' }, { ppm: 90000, label: '9%' }, { ppm: 60000, label: '6%' }, { ppm: 30000, label: '3%' }],
  InvoiceCategories: async () => [[]],
  Claims: async () => [
    { id: 1, code: 'BX-2025-03-0001', claimantName: '张三', applyDate: '2025-03-18', destination: '上海', reason: '客户拜访', status: 'approved', statusLabel: '已审批', totalAmount: 186000, totalTax: 6000, canEdit: false, canApprove: false, canPost: true, voucherNo: '', items: [] },
  ],
  ClaimDetail: async () => null,
  SaveClaim: async () => ({ id: 1, code: 'BX-2025-03-0002' }),
  ApproveClaim: async () => null,
  RejectClaim: async () => null,
  PostClaim: async () => ({ id: 1, voucherNo: '记-2025-03-0003' }),
  ClaimCategories: async () => [
    { value: 'transport', label: '交通费', account: '560207' },
    { value: 'accommodation', label: '住宿费', account: '560207' },
    { value: 'meal', label: '餐费补助', account: '560208' },
    { value: 'local_transit', label: '市内交通', account: '560207' },
    { value: 'conference', label: '会务费', account: '560206' },
    { value: 'other', label: '其他', account: '560217' },
  ],
  ContactList: async () => [
    { id: 1, kind: 'customer', kindLabel: '客户', name: '杭州云帆科技有限公司', shortName: '云帆科技', enabled: true },
  ],
  SaveContact: async () => 1,
  Departments: async () => [
    { id: 1, code: '001', name: '管理部门', fullName: '管理部门', enabled: true, parentId: null, remark: '' },
    { id: 2, code: '002', name: '销售部', fullName: '销售部', enabled: true, parentId: null, remark: '' },
  ],
  SaveDepartment: async () => 1,
  DeleteDepartment: async () => null,
  DepartmentUsageOf: async () => ({ employees: 2, children: 0, entries: 0, bankFlows: 0, bankRules: 0, invoices: 0, claims: 0 }),
  TaxTableInfo: async () => ({ name: '个人所得税预扣率表一', note: '税率与级距全部来自可配置的参数表', brackets: [
    { upper: 3600000, ratePPM: 30000, rateLabel: '3%', deduction: 0 },
    { upper: 14400000, ratePPM: 100000, rateLabel: '10%', deduction: 252000 },
    { upper: 30000000, ratePPM: 200000, rateLabel: '20%', deduction: 1692000 },
  ] }),
  ExportReport: async () => ({ path: '/tmp/报表.xlsx', kind: 'bs', title: '资产负债表', rows: 53, sheetName: '资产负债表' }),
  SuggestExportPath: async () => './报表.xlsx',
  Statement: async () => ({
    companyName: '杭州云帆软件有限公司', contactId: 1,
    contactName: '杭州某某科技有限公司', contactKind: 'customer', contactKindLabel: '客户',
    contactTaxNo: '91330100MA2XXXXXXX',
    from: '2025-03-01', to: '2025-03-31',
    accountCode: '1122', accountName: '应收账款', mixed: false,
    opening: 10600000, openingDir: '借',
    lines: [
      { date: '2025-03-05', voucherNo: '记-2025-03-0001', summary: '收回货款', debit: 0, credit: 10600000, balance: 0, dir: '平' },
      { date: '2025-03-12', voucherNo: '记-2025-03-0002', summary: '销售软件服务', debit: 15900000, credit: 0, balance: 15900000, dir: '借' },
    ],
    totalDebit: 15900000, totalCredit: 10600000,
    closing: 15900000, closingDir: '借', closingUpper: '壹拾伍万玖仟元整',
    summary: '2025-03-01 ~ 2025-03-31：杭州某某科技有限公司 借 159,000.00',
    text: '往来对账单\n（浏览器开发模式，此处显示占位文本）',
  }),
  ActiveContacts: async () => [
    { id: 1, name: '杭州某某科技有限公司', kind: 'customer', kindLabel: '客户' },
    { id: 2, name: '宁波恒信办公用品有限公司', kind: 'supplier', kindLabel: '供应商' },
  ],
  AgingReport: async () => ({
    asOf: '2025-03-31',
    summary: '2025-03-31 应收 159,000.00，应付 0.00；其中 90 天以上 0.00',
    total: 15900000, debitTotal: 15900000, creditTotal: 0, over90: 0,
    buckets: [
      { label: '30 天以内', amount: 5300000, percent: 33.3 },
      { label: '31—60 天', amount: 5300000, percent: 33.3 },
      { label: '61—90 天', amount: 5300000, percent: 33.3 },
      { label: '91—180 天', amount: 0, percent: 0 },
      { label: '181—365 天', amount: 0, percent: 0 },
      { label: '1 年以上', amount: 0, percent: 0 },
    ],
    rows: [{
      contactId: 2, contactName: '杭州某某科技有限公司',
      accountCode: '1122', accountName: '应收账款',
      debits: 15900000, credits: 0, balance: 15900000, maxDays: 19,
      buckets: [
        { label: '30 天以内', amount: 5300000, percent: 33.3 },
        { label: '31—60 天', amount: 5300000, percent: 33.3 },
        { label: '61—90 天', amount: 5300000, percent: 33.3 },
        { label: '91—180 天', amount: 0, percent: 0 },
        { label: '181—365 天', amount: 0, percent: 0 },
        { label: '1 年以上', amount: 0, percent: 0 },
      ],
      items: [
        { date: '2025-03-12', days: 19, amount: 5300000, summary: '销售软件服务', voucherNo: '记-2025-03-0002', bucketLabel: '30 天以内' },
      ],
    }],
  }),
  Report: async () => ({ title: '（浏览器开发模式）', subtitle: '此处显示的是占位数据', columns: ['金额'], rows: [{ no: '1', label: '请在 wails dev 下查看真实报表', indent: 0, bold: false, values: [0] }] }),
  LedgerDetail: async () => ({ accountPrefix: '1002', accountName: '银行存款', from: '2025-01-01', to: '2025-12-31', rows: [], openingBalance: 0, closingBalance: 0 }),
  FilesDir: async () => '/Users/you/.files',
  Backup: async () => ({ path: '/tmp/a.mabak', companyName: '演示', dbSize: 120000, fileCount: 3, fileBytes: 40000, voucherCount: 16, accountCount: 190 }),
  RestoreBackup: async () => ({ dbPath: '/tmp/x.db', filesDir: '/tmp/.files', restored: 3, stashed: '', companyName: '演示' }),
  AuditAttachments: async () => ({ posted: 16, withAttach: 3, without: 13, orphanFiles: 0 }),
  InspectBackup: async () => ({ path: '', companyName: '演示', dbSize: 120000, fileCount: 3, fileBytes: 40000, voucherCount: 16, accountCount: 190 }),
  AISuggest: async () => ({
    ok: true, summary: 'AI 建议（置信度 0.91）：收回应收账款', suggestionId: 1, layer: 'ai',
    confidence: 0.91, model: 'qwen2.5:7b', latencyMs: 4200, tokensIn: 1120, tokensOut: 168,
    voucher: {
      word: '记', bizDate: '2025-03-11', remark: '收到货款', total: 10600000,
      entries: [
        { accountCode: '1002', summary: '收到货款', debit: 10600000, credit: 0, auxDesc: '' },
        { accountCode: '1122', summary: '收到货款', debit: 0, credit: 10600000, auxDesc: '往来#2' },
      ],
    },
    failures: [], warnings: [],
  }),
  AcceptAISuggestion: async () => ({ voucherId: 9, voucherNo: '记-2025-03-0009', summary: '已生成草稿凭证' }),
  RejectAISuggestion: async () => null,
  Bookkeeper: async () => '李会计',
  SaveBookkeeper: async () => null,
  AuditSettings: async () => ({ recordViews: false, viewIntervalSeconds: 60 }),
  SaveAuditSettings: async () => null,
  AuditLog: async () => ({
    categories: ['business', 'read', 'system'],
    dir: '/Users/you/.mini-account/logs',
    actions: [
      { value: 'voucher.create', label: '录入凭证' },
      { value: 'voucher.post', label: '凭证过账' },
      { value: 'period.close', label: '结账' },
      { value: 'system.failed', label: '操作失败' },
    ],
    page: {
      total: 2, segments: 1, bytes: 40960, truncated: false,
      entries: [
        { seq: 2, at: '2026-09-12 10:20:31+08:00', operator: '李会计', source: 'gui',
          action: 'voucher.post', summary: '凭证过账 记-2025-03-0001', entity: 'voucher',
          entityId: '1', result: 'ok', message: '', book: '/Users/you/.mini-account/dataDB/demo.db',
          company: '杭州云帆软件有限公司', appVersion: '0.2.0-rc2', hash: 'a'.repeat(64),
          prevHash: 'b'.repeat(64), detail: { 借方合计: '106,000.00', 记账人: '王主管' } },
        { seq: 1, at: '2026-09-12 10:20:29+08:00', operator: '李会计', source: 'gui',
          action: 'voucher.create', summary: '录入凭证草稿', entity: 'voucher', entityId: '1',
          result: 'ok', message: '', book: '/Users/you/.mini-account/dataDB/demo.db',
          company: '杭州云帆软件有限公司', appVersion: '0.2.0-rc2', hash: 'b'.repeat(64),
          prevHash: 'genesis', detail: { 摘要: '收到货款' } },
      ],
    },
  }),
  VerifyAuditLog: async () => ({ ok: true, checked: 2, files: 1, head: 'a'.repeat(64), issues: [] }),
  ExportAuditLog: async () => 2,
  AIAgentSources: async () => ([
    { value: 'bank_flows', label: '银行流水' },
    { value: 'invoices', label: '发票' },
    { value: 'claims', label: '报销单' },
  ]),
  StartAIAgentRun: async () => ({
    id: 'run-1', source: 'bank_flows', state: 'running', total: 3, done: 0,
    okCount: 0, model: 'qwen2.5:7b', items: [], tokensIn: 0, tokensOut: 0,
    startedAt: '2026-09-12 15:00:00+08:00',
  }),
  AIAgentRunStatus: async () => ({
    id: 'run-1', source: 'bank_flows', state: 'done', total: 3, done: 3,
    okCount: 2, model: 'qwen2.5:7b', tokensIn: 900, tokensOut: 300,
    startedAt: '2026-09-12 15:00:00+08:00', finishedAt: '2026-09-12 15:00:24+08:00',
    items: [
      { targetType: 'bank_flow', targetId: 1, label: '2025-03-05 收入 杭州云帆科技 106,000.00',
        ok: true, suggestionId: 11, summary: '提议 2 条分录，通过全部护栏', confidence: 0.92,
        voucher: { word: '记', bizDate: '2025-03-05', remark: '收到货款', total: 10600000,
          entries: [
            { accountCode: '1002', summary: '收到货款', debit: 10600000, credit: 0, auxDesc: '' },
            { accountCode: '1122', summary: '收到货款', debit: 0, credit: 10600000, auxDesc: '客户#1' },
          ] } },
      { targetType: 'bank_flow', targetId: 2, label: '2025-03-12 支出 杭州某某物业 30,000.00',
        ok: false, suggestionId: 12, summary: '未通过护栏',
        failures: ['缺少必需的辅助核算：科目 管理费用 要求「部门」'] },
    ],
  }),
  LatestAIAgentRun: async () => null,
  CancelAIAgentRun: async () => null,
  AcceptAIAgentItem: async () => ({ voucherId: 9, voucherNo: '', summary: '已按 AI 建议生成草稿凭证 （草稿 #9），请核对后过账' }),
  RejectAIAgentItem: async () => null,
  AIPromptConfig: async () => ({
    instructions: '# 记账的基本原则\n\n- 权责发生制。\n- 银行流水判断对方科目。',
    custom: false,
    default: '# 记账的基本原则\n\n- 权责发生制。\n- 银行流水判断对方科目。',
    taskNotes: { bank_flow: '', invoice: '', expense: '', freeform: '' },
    tasks: [
      { value: 'bank_flow', label: '银行流水', note: '' },
      { value: 'invoice', label: '发票', note: '' },
      { value: 'expense', label: '报销单', note: '' },
      { value: 'freeform', label: '自然语言', note: '' },
    ],
    updatedAt: '', maxInstructions: 8000, maxTaskNote: 2000,
    locked: ['工作边界（只能用列出的科目编码、借贷必须相等）'],
  }),
  SaveAIPromptConfig: async () => null,
  ResetAIPromptConfig: async () => null,
  PreviewAIPrompt: async () => ({
    system: '（预览）你是一名中国小微企业的资深会计……', user: '（预览）请为下列业务编制记账凭证',
    digest: '0'.repeat(64), chars: 4000, task: 'bank_flow',
  }),
  AIConfig: async () => ({ providers: [{ id: 1, name: '本地 Ollama', kind: 'local', baseUrl: 'http://127.0.0.1:11434/v1', model: 'qwen2.5:7b', enabled: true, isDefault: true }], stats: { total: 12, proposed: 10, valid: 8, accepted: 5, modified: 2, rejected: 1, tokensIn: 12000, tokensOut: 1800 }, hasDefault: true }),
  SaveAIProvider: async () => 1,
  AISuggestions: async () => [],
  Today: async () => '2025-03-11',
  AppVersion: async () => '0.1.0-dev',}
