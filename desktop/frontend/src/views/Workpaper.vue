<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import {
  Plus, Pencil, Trash2, Info, FileText, AlertTriangle, CheckCircle2,
  Calculator, ScrollText, RefreshCw,
} from 'lucide-vue-next'
import { api, notify, DRAFT_HINT } from '@/lib/api'
import { bookkeeper, loadBookkeeper } from '@/lib/operator'
import { fmtMoney, centsToYuanInput } from '@/lib/format'
import Card from '@/components/ui/Card.vue'
import CardHeader from '@/components/ui/CardHeader.vue'
import CardTitle from '@/components/ui/CardTitle.vue'
import CardDescription from '@/components/ui/CardDescription.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Modal from '@/components/ui/Modal.vue'
import Spinner from '@/components/ui/Spinner.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import EvidenceChain from '@/components/EvidenceChain.vue'

// ---------------------------------------------------------------------------
// 审计底稿
// ---------------------------------------------------------------------------
//
// 这一页回答三个问题，按重要性从下往上：
//
//   1. 多少钱以上必须处理？（重要性水平 —— 三个门槛）
//   2. 发现的错报加起来算不算重大？（未更正错报汇总）
//   3. 账面数调完之后是多少？（审定表）
//
// ★ 底稿与账的关系只有一条，但最容易做错：
//
//	生成凭证 ≠ 已入账。
//
// 凭证录完只落草稿，过账只发生在账期结算 —— 调整凭证也一样。
// 所以一笔调整「改了没改到账上」只能看凭证的状态：
//   - 没过账：审定数里要加上它，未更正错报里也要列上它
//   - 过了账：账面数里已经含了它，不能再加一次
//
// 界面上的「状态」一列就是给这件事看的，
// 不要看到「已生成凭证」就以为账已经改了。

const period = ref('')
const periods = ref([])
const data = ref(null)
const loading = ref(true)
const busy = ref(false)
const operator = bookkeeper

const meta = ref({ accounts: [], contacts: [], departments: [], employees: [] })

const materiality = computed(() => data.value?.materiality ?? null)
const benchmarks = computed(() => data.value?.benchmarks ?? [])
const worksheet = computed(() => data.value?.worksheet ?? [])
const adjustments = computed(() => data.value?.adjustments ?? [])
const summary = computed(() => data.value?.misstatements ?? null)

// 审定表里有调整的行排前面：复核人先看动过的地方
const worksheetSorted = computed(() =>
  [...worksheet.value].sort((a, b) => (b.adjusted ? 1 : 0) - (a.adjusted ? 1 : 0)))

// ★ 请求序号：快速切期间时，旧请求可能后返回并把新数据覆盖掉
// （界面选中的是 2025-03，表格里却是 2025-02 的数）。
// 只认最后一次请求的结果。
let reqSeq = 0

async function load() {
  if (!period.value) return
  const my = ++reqSeq
  const [y, m] = period.value.split('-')
  loading.value = true
  const [w, opt, dept, emp, contacts] = await Promise.all([
    api.workpaper({ year: Number(y), month: Number(m) }),
    api.accountOptions(),
    api.departments(),
    api.employees(true),
    api.contactOptions(),
  ])
  if (my !== reqSeq) return // 已经有更新的请求在跑，丢弃这次结果
  loading.value = false
  if (!w.ok) {
    // ★ 失败要把页面清空：留着上一期的数字比报错更危险 ——
    // 用户会以为看的是这一期
    data.value = null
    notify(w.fault.message, 'error', w.fault.detail)
    return
  }
  data.value = w.data
  meta.value = {
    accounts: opt.ok ? opt.data ?? [] : [],
    contacts: contacts.ok ? contacts.data ?? [] : [],
    departments: (dept.ok ? dept.data ?? [] : []).filter((d) => d.enabled),
    employees: emp.ok ? emp.data ?? [] : [],
  }
}

onMounted(async () => {
  await loadBookkeeper()
  // 期间列表只取一次；期间定下来之后由 watch 触发 load，
  // 不在挂载时另外再 load 一次（那会并发两次同样的请求）
  const p = await api.periods()
  if (p.ok) {
    periods.value = p.data.periods ?? []
    const open = periods.value.filter((x) => x.status === 'open')
    // 默认落在**当前**期间（最早的未结账月份），
    // 而不是最新一期：审计通常是从头往后做的
    period.value = (open[0] ?? periods.value[0])?.label ?? ''
  }
})

// 期间一变就重查（挂载时 period 为空会被 load 自己挡掉）
watch(period, load)

// ------------------------------------------------------------ 重要性水平

const matOpen = ref(false)
const matForm = ref(emptyMat())

function emptyMat() {
  return {
    benchmark: 'assets', benchmarkAmountYuan: '',
    ratePercent: '0.5', performancePercent: '60', trivialPercent: '5',
    note: '',
  }
}

/** 基准 → 它的常用比例（用于切换基准时自动带出）。 */
function defaultRateLabel(value) {
  return benchmarks.value.find((b) => b.value === value)?.defaultRateLabel ?? '0.5%'
}
/** 基准的账套取数金额。 */
function benchmarkOf(value) {
  return benchmarks.value.find((b) => b.value === value)
}

function openMat() {
  const m = materiality.value
  if (m) {
    matForm.value = {
      benchmark: m.benchmark,
      benchmarkAmountYuan: centsToYuanInput(m.benchmarkAmount),
      ratePercent: String(Number(m.ratePpm) / 10000),
      performancePercent: String(Number(m.performancePpm) / 10000),
      trivialPercent: String(Number(m.trivialPpm) / 10000),
      note: m.note ?? '',
    }
  } else {
    const f = emptyMat()
    const first = benchmarks.value.find((b) => b.usable) ?? benchmarkOf('assets')
    if (first) {
      f.benchmark = first.value
      f.ratePercent = String(Number(first.defaultRatePpm) / 10000)
      f.benchmarkAmountYuan = centsToYuanInput(first.amount)
    }
    matForm.value = f
  }
  matOpen.value = true
}

// 换基准：金额与比例都跟着换回默认值。
// 留着上一个基准的金额是最容易犯的错 —— 10,000,000 × 0.5% 和
// 2,000,000 × 0.5% 都能算出一个数，只是那个数是错的
function onBenchmarkChange() {
  const b = benchmarkOf(matForm.value.benchmark)
  if (!b) return
  matForm.value.benchmarkAmountYuan = centsToYuanInput(b.amount)
  matForm.value.ratePercent = String(Number(b.defaultRatePpm) / 10000)
}

/** 百分比字符串 → 百万分比整数。0.5% → 5000。 */
function toPPM(s) {
  const n = Number(String(s ?? '').replace('%', '').trim())
  if (!Number.isFinite(n)) return 0
  return Math.round(n * 10000)
}

async function saveMat() {
  const f = matForm.value
  // ★ 基准金额允许留空：留空表示「用账套取数」（后端如此约定）。
  // 原来这里强制必填，而取数为 0 时输入框本来就是空的 ——
  // 用户被卡在「说明写着默认用账套取数、却提示必须填」的矛盾里。
  if (!f.benchmarkAmountYuan.trim()) {
    const b = benchmarkOf(f.benchmark)
    if (!b || !b.usable) {
      notify('这个基准在账套里的取数是 0，请换一个基准或手工填基准金额', 'warn')
      return
    }
  }
  if (!(toPPM(f.ratePercent) > 0)) { notify('整体重要性比例必须大于 0', 'warn'); return }
  const [y, m] = period.value.split('-')
  busy.value = true
  const r = await api.saveMateriality({
    year: Number(y), month: Number(m),
    benchmark: f.benchmark, benchmarkAmountYuan: f.benchmarkAmountYuan.trim(),
    ratePpm: toPPM(f.ratePercent),
    performancePpm: toPPM(f.performancePercent),
    trivialPpm: toPPM(f.trivialPercent),
    note: f.note,
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已确定本期重要性水平', 'success')
  matOpen.value = false
  await load()
}

async function deleteMat() {
  const [y, m] = period.value.split('-')
  busy.value = true
  const r = await api.deleteMateriality({ year: Number(y), month: Number(m) })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已清除本期重要性水平', 'success')
  await load()
}

// -------------------------------------------------------------- 审计调整

const adjOpen = ref(false)
const adjForm = ref(emptyAdj())

function emptyAdj() {
  return {
    id: 0, code: '', kind: 'adjust', summary: '', reason: '', evidence: '',
    lines: [
      emptyLine(), emptyLine(),
    ],
  }
}
function emptyLine() {
  return {
    accountCode: '', summary: '', debitYuan: '', creditYuan: '',
    contactId: null, employeeId: null, deptId: null, projectId: null,
  }
}

function accountOf(code) {
  return meta.value.accounts.find((a) => a.code === code)
}
/** 当前行需要的辅助核算维度（中文标签）。 */
function needAux(line) {
  return accountOf(line.accountCode)?.auxTypes ?? []
}
/** 辅助核算标签 → 往来单位类型（只有往来四类需要过滤出对应 kind）。 */
const CONTACT_KIND = { 客户: 'customer', 供应商: 'supplier', 股东: 'shareholder', 其他单位: 'other' }
function contactsFor(label) {
  const kind = CONTACT_KIND[label]
  if (!kind) return []
  return meta.value.contacts.filter((c) => c.kind === kind)
}

function openAdj(a) {
  if (a) {
    adjForm.value = {
      id: a.id, code: a.code, kind: a.kind, summary: a.summary,
      reason: a.reason, evidence: a.evidence,
      lines: a.lines.map((l) => ({
        accountCode: l.accountCode, summary: l.summary,
        debitYuan: centsToYuanInput(l.debit), creditYuan: centsToYuanInput(l.credit),
        contactId: l.contactId ?? null, employeeId: l.employeeId ?? null,
        deptId: l.deptId ?? null, projectId: l.projectId ?? null,
      })),
    }
  } else {
    const f = emptyAdj()
    f.summary = ''
    adjForm.value = f
  }
  adjOpen.value = true
}

// 换科目时清掉辅助核算：旧维度在新科目上可能不合法，
// 留着会被服务端拒绝，而用户看不出为什么
function onAccountChange(line) {
  line.contactId = line.employeeId = line.deptId = line.projectId = null
}

function addLine() {
  adjForm.value.lines.push(emptyLine())
}
function removeLine(i) {
  if (adjForm.value.lines.length <= 2) {
    notify('复式记账至少需要两行', 'warn')
    return
  }
  adjForm.value.lines.splice(i, 1)
}

const adjDiff = computed(() => {
  let d = 0
  for (const l of adjForm.value.lines) {
    d += toCents(l.debitYuan) - toCents(l.creditYuan)
  }
  return d
})
function toCents(s) {
  const n = Number(String(s ?? '').replace(/,/g, '').trim())
  if (!Number.isFinite(n)) return 0
  return Math.round(n * 100)
}

async function saveAdj() {
  const f = adjForm.value
  if (!f.summary.trim()) { notify('请填写调整摘要', 'warn'); return }
  if (!f.reason.trim()) {
    notify('请填写调整依据 —— 没有依据的调整复核人无法判断该不该调', 'warn')
    return
  }
  if (adjDiff.value !== 0) {
    notify(`借贷不平：差额 ${fmtMoney(Math.abs(adjDiff.value))}`, 'warn')
    return
  }
  const [y, m] = period.value.split('-')
  busy.value = true
  const r = await api.saveAdjustment({
    id: f.id, year: Number(y), month: Number(m), code: f.code,
    kind: f.kind, summary: f.summary.trim(), reason: f.reason.trim(),
    evidence: f.evidence.trim(), operator: operator.value.trim(),
    lines: f.lines.map((l) => ({
      accountCode: l.accountCode, summary: l.summary,
      debitYuan: l.debitYuan, creditYuan: l.creditYuan,
      contactId: l.contactId, employeeId: l.employeeId,
      deptId: l.deptId, projectId: l.projectId,
    })),
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已登记审计调整', 'success')
  adjOpen.value = false
  await load()
}

async function removeAdj(a) {
  if (!window.confirm(`确定删除调整 ${a.code}「${a.summary}」？`)) return
  busy.value = true
  const r = await api.deleteAdjustment(a.id)
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已删除审计调整', 'success')
  await load()
}

// ★ 生成的是草稿凭证：过账仍然只在账期结算。
async function bookAdj(a) {
  if (!operator.value.trim()) { notify('请填写操作人（调整凭证的制单人）', 'warn'); return }
  if (!window.confirm(
    `把调整 ${a.code}「${a.summary}」生成调整凭证？\n\n` +
    `生成的是草稿：不占号、不进总账，到账期结算时才过账。`)) return
  busy.value = true
  const r = await api.postAdjustment({ id: a.id, by: operator.value.trim() })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`调整 ${a.code} 已生成调整凭证`, 'success', DRAFT_HINT)
  await load()
}

/** 状态徽标的颜色：已入账=绿、已生成草稿=金、只在底稿=灰。 */
function stateVariant(a) {
  if (a.posted) return 'profit'
  if (a.booked) return 'warn'
  return 'muted'
}
</script>

<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-end gap-3">
      <div>
        <Label>会计期间</Label>
        <select v-model="period"
                class="mt-1.5 h-9 rounded-md border border-input bg-transparent px-2 text-sm"
>
          <option v-for="p in periods" :key="p.label" :value="p.label">
            {{ p.label }}（{{ p.statusLabel }}）
          </option>
        </select>
      </div>
      <Button variant="outline" :disabled="loading" @click="load">
        <RefreshCw /> 刷新
      </Button>
      <div class="flex-1" />
      <div class="flex items-center gap-2">
        <Label>记账人</Label>
        <Input v-model="operator" class="h-9 w-32" placeholder="姓名" />
      </div>
    </div>

    <Spinner v-if="loading" />

    <template v-else-if="data">
      <!-- ------------------------------------------------------ 重要性水平 -->
      <Card>
        <CardHeader>
          <div class="flex items-start justify-between gap-3">
            <div>
              <CardTitle>重要性水平（{{ data.period }}）</CardTitle>
              <CardDescription>
                多少钱以上的错报必须处理 —— 没有门槛就没法说「超没超」。
                三个门槛都由「基准 × 比例」算出来，算式留在底稿上供复核。
              </CardDescription>
            </div>
            <div class="flex shrink-0 gap-2">
              <Button size="sm" variant="outline" @click="openMat">
                <Pencil /> {{ materiality ? '修改' : '确定重要性' }}
              </Button>
              <Button v-if="materiality" size="sm" variant="outline"
                      :disabled="busy" @click="deleteMat">
                <Trash2 /> 清除
              </Button>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          <div v-if="!materiality" class="rounded-lg border border-dashed p-4 text-sm">
            <div class="flex items-start gap-2">
              <AlertTriangle class="mt-0.5 size-4 shrink-0 text-[var(--warn)]" />
              <div>
                <p class="font-medium">本期还没有确定重要性水平。</p>
                <p class="mt-1 text-muted-foreground">
                  未更正错报汇总会照常列出每一笔，但<b>判断不了它们是否重大</b> ——
                  这是底稿里唯一一处不能靠软件补上的判断。
                </p>
              </div>
            </div>
          </div>

          <div v-else class="space-y-3">
            <div class="grid gap-3 sm:grid-cols-3">
              <div class="rounded-lg border p-3">
                <div class="text-xs text-muted-foreground">整体重要性</div>
                <div class="num mt-1 text-lg font-semibold">{{ fmtMoney(materiality.overall) }}</div>
                <div class="mt-0.5 text-xs text-muted-foreground">
                  {{ materiality.benchmarkName }} × {{ materiality.rateLabel }}
                </div>
              </div>
              <div class="rounded-lg border p-3">
                <div class="text-xs text-muted-foreground">实际执行重要性</div>
                <div class="num mt-1 text-lg font-semibold">{{ fmtMoney(materiality.performance) }}</div>
                <div class="mt-0.5 text-xs text-muted-foreground">
                  整体 × {{ materiality.performanceLabel }}
                </div>
              </div>
              <div class="rounded-lg border p-3">
                <div class="text-xs text-muted-foreground">明显微小错报临界值</div>
                <div class="num mt-1 text-lg font-semibold">{{ fmtMoney(materiality.trivial) }}</div>
                <div class="mt-0.5 text-xs text-muted-foreground">
                  整体 × {{ materiality.trivialLabel }}（低于它不必逐笔累积）
                </div>
              </div>
            </div>
            <div v-if="materiality.note" class="text-sm text-muted-foreground">
              判断说明：{{ materiality.note }}
            </div>
            <details class="rounded-lg border bg-muted/30 p-3 text-sm">
              <summary class="cursor-pointer font-medium">算式（复核用）</summary>
              <ul class="mt-2 space-y-1">
                <li v-for="(line, i) in materiality.explain" :key="i" class="num">
                  {{ line }}
                </li>
              </ul>
            </details>
          </div>
        </CardContent>
      </Card>

      <!-- -------------------------------------------------- 未更正错报汇总 -->
      <Card v-if="summary">
        <CardHeader>
          <CardTitle>未更正错报汇总</CardTitle>
          <CardDescription>
            发现了但<b>没有改进账里</b>的错报。只算还没过账的调整 ——
            凭证一过账，账面数里就含了它，再算一遍等于把已经改过的错又报一次。
          </CardDescription>
        </CardHeader>
        <CardContent class="space-y-3">
          <div class="flex items-start gap-2 rounded-lg border p-3 text-sm"
               :class="summary.hasMateriality && summary.total >= summary.performance
                 ? 'border-[var(--loss)]/40 bg-[var(--loss)]/8'
                 : 'bg-muted/30'">
            <component :is="summary.hasMateriality && summary.total >= summary.performance
              ? AlertTriangle : CheckCircle2"
              class="mt-0.5 size-4 shrink-0" />
            <div>
              <div class="num font-medium">合计 {{ fmtMoney(summary.total) }}</div>
              <p class="mt-1 text-muted-foreground">{{ summary.concludes }}</p>
            </div>
          </div>

          <div v-if="summary.items.length" class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b bg-muted/40">
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">底稿索引</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">摘要</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">种类</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">依据</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">金额</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="it in summary.items" :key="it.code" class="border-b last:border-0">
                  <td class="num px-3 py-1.5 font-mono text-xs">{{ it.code }}</td>
                  <td class="px-3 py-1.5">{{ it.summary }}</td>
                  <td class="px-3 py-1.5">
                    <Badge :variant="it.kind === 'reclass' ? 'muted' : 'debit'">
                      {{ it.kindLabel }}
                    </Badge>
                  </td>
                  <td class="px-3 py-1.5 text-muted-foreground">{{ it.reason }}</td>
                  <td class="num px-3 py-1.5 text-right">{{ fmtMoney(it.amount) }}</td>
                </tr>
              </tbody>
            </table>
            <p v-if="summary.reclassCount" class="mt-2 text-xs text-muted-foreground">
              另有 {{ summary.reclassCount }} 笔重分类：不动损益，因此不计入合计，
              但要列出来让复核人看到。
            </p>
          </div>
          <p v-else class="text-sm text-muted-foreground">本期没有未更正错报。</p>
        </CardContent>
      </Card>

      <!-- ---------------------------------------------------------- 审定表 -->
      <Card>
        <CardHeader>
          <CardTitle>审定表</CardTitle>
          <CardDescription>
            审定数 = 账面数 + <b>未入账</b>调整。已过账的调整在账面数里，不再加第二次。
            金额按「借正贷负」列示。
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div v-if="!worksheetSorted.length" class="text-sm text-muted-foreground">
            本期没有余额也没有调整 —— 审定表是空的。
          </div>
          <div v-else class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b bg-muted/40">
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">科目</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">账面数</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">调整借方</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">调整贷方</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">审定数</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in worksheetSorted" :key="r.accountCode"
                    class="border-b last:border-0"
                    :class="r.adjusted ? 'bg-[var(--gold)]/8' : ''">
                  <td class="px-3 py-1.5">
                    <span class="font-mono text-xs text-muted-foreground">{{ r.accountCode }}</span>
                    {{ r.accountName }}
                    <Badge v-if="r.trivial" variant="muted" class="ml-1">低于临界值</Badge>
                  </td>
                  <td class="num px-3 py-1.5 text-right">{{ fmtMoney(r.bookBalance) }}</td>
                  <td class="num px-3 py-1.5 text-right">{{ fmtMoney(r.adjustDebit, { blankZero: true }) }}</td>
                  <td class="num px-3 py-1.5 text-right">{{ fmtMoney(r.adjustCredit, { blankZero: true }) }}</td>
                  <td class="num px-3 py-1.5 text-right font-medium">{{ fmtMoney(r.audited) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </CardContent>
      </Card>

      <!-- ------------------------------------------------------ 审计调整 -->
      <Card>
        <CardHeader>
          <div class="flex items-start justify-between gap-3">
            <div>
              <CardTitle>审计调整（{{ data.period }}）</CardTitle>
              <CardDescription>
                登记发现的调整。其中「调整」动损益、「重分类」只在项目之间搬家 ——
                重分类不计入错报合计。
              </CardDescription>
            </div>
            <Button size="sm" @click="openAdj(null)"><Plus /> 登记调整</Button>
          </div>
        </CardHeader>
        <CardContent>
          <EmptyState v-if="!adjustments.length" title="本期还没有调整"
                      description="发现错报后在这里登记：写清摘要、依据与证据，再决定改不改账。" />
          <div v-else class="space-y-3">
            <div v-for="a in adjustments" :key="a.id" class="rounded-lg border p-3">
              <div class="flex flex-wrap items-start justify-between gap-2">
                <div>
                  <div class="flex items-center gap-2">
                    <span class="num font-mono text-xs text-muted-foreground">{{ a.code }}</span>
                    <span class="font-medium">{{ a.summary }}</span>
                    <Badge :variant="a.kind === 'reclass' ? 'muted' : 'debit'">{{ a.kindLabel }}</Badge>
                    <Badge :variant="stateVariant(a)">{{ a.stateLabel }}</Badge>
                    <Badge v-if="!a.balanced" variant="loss">借贷不平</Badge>
                  </div>
                  <div class="mt-1 text-sm text-muted-foreground">
                    依据：{{ a.reason }}
                    <template v-if="a.evidence">　证据：{{ a.evidence }}</template>
                  </div>
                  <div v-if="a.problems?.length" class="mt-1 text-sm text-[var(--loss)]">
                    {{ a.problems.join('；') }}
                  </div>
                </div>
                <div class="flex shrink-0 items-center gap-2">
                  <span class="num font-medium">{{ fmtMoney(a.amount) }}</span>
                  <Button v-if="!a.booked" size="sm" variant="outline" :disabled="busy"
                          title="生成调整凭证草稿（过账仍在账期结算）"
                          @click="bookAdj(a)">
                    <FileText /> 生成凭证
                  </Button>
                  <Button v-else-if="!a.posted" size="sm" variant="outline" disabled
                          title="已经生成过草稿凭证；要重新生成请先删掉那张草稿">
                    <FileText /> 已生成草稿
                  </Button>
                  <span v-else class="text-xs text-muted-foreground">{{ a.voucherLabel }}</span>
                  <Button v-if="!a.booked" size="sm" variant="outline" @click="openAdj(a)">
                    <Pencil />
                  </Button>
                  <Button v-if="!a.booked" size="sm" variant="outline" :disabled="busy"
                          @click="removeAdj(a)">
                    <Trash2 />
                  </Button>
                </div>
              </div>
              <table class="mt-2 w-full text-sm">
                <tbody>
                  <tr v-for="l in a.lines" :key="l.lineNo" class="border-t last:border-0">
                    <td class="px-2 py-1">
                      <span class="font-mono text-xs text-muted-foreground">{{ l.accountCode }}</span>
                      {{ l.accountName }}
                      <span v-if="l.auxDesc" class="text-xs text-muted-foreground">[{{ l.auxDesc }}]</span>
                    </td>
                    <td class="px-2 py-1 text-muted-foreground">{{ l.summary }}</td>
                    <td class="num w-32 px-2 py-1 text-right">
                      {{ fmtMoney(l.debit, { blankZero: true }) }}
                    </td>
                    <td class="num w-32 px-2 py-1 text-right">
                      {{ fmtMoney(l.credit, { blankZero: true }) }}
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </CardContent>
      </Card>

      <!-- -------------------------------------------------------- 证据链 -->
      <EvidenceChain :period="data.period" :operator="operator" />
    </template>

    <!-- -------------------------------------------------------- 重要性弹窗 -->
    <Modal v-model:open="matOpen" title="确定重要性水平"
           description="基准与比例由注册会计师判断；软件负责把算式算对、把依据留下。">
      <div class="space-y-4">
        <div class="grid gap-3 sm:grid-cols-2">
          <div>
            <Label>基准</Label>
            <select v-model="matForm.benchmark"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm"
                    @change="onBenchmarkChange">
              <option v-for="b in benchmarks" :key="b.value" :value="b.value">
                {{ b.label }} —— 取数 {{ fmtMoney(b.amount) }}
                <template v-if="!b.usable">（取数为 0/负数，不宜作基准）</template>
              </option>
            </select>
          </div>
          <div>
            <Label>基准金额（元）</Label>
            <Input v-model="matForm.benchmarkAmountYuan" class="num mt-1.5" placeholder="10000000.00" />
            <p class="mt-1 text-xs text-muted-foreground">
              默认用账套取数；也可以手工覆盖（例如用调整后的预计数）。
            </p>
          </div>
        </div>
        <div class="grid gap-3 sm:grid-cols-3">
          <div>
            <Label>整体重要性（%）</Label>
            <Input v-model="matForm.ratePercent" class="num mt-1.5" placeholder="0.5" />
            <p class="mt-1 text-xs text-muted-foreground">常用 {{ defaultRateLabel(matForm.benchmark) }}</p>
          </div>
          <div>
            <Label>实际执行重要性（%）</Label>
            <Input v-model="matForm.performancePercent" class="num mt-1.5" placeholder="60" />
            <p class="mt-1 text-xs text-muted-foreground">占整体重要性的比例，常用 50%–75%</p>
          </div>
          <div>
            <Label>明显微小错报临界值（%）</Label>
            <Input v-model="matForm.trivialPercent" class="num mt-1.5" placeholder="5" />
            <p class="mt-1 text-xs text-muted-foreground">占整体重要性的比例，常用 3%–5%</p>
          </div>
        </div>
        <div>
          <Label>判断说明</Label>
          <textarea v-model="matForm.note" rows="2"
                    class="mt-1.5 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm"
                    placeholder="为什么选这个基准、这个比例（复核时最需要看到的一句）" />
        </div>
        <div class="rounded-lg border bg-muted/30 p-3 text-sm">
          <div class="flex items-center gap-2 font-medium"><Calculator class="size-4" /> 预览</div>
          <ul class="num mt-2 space-y-1">
            <li>整体重要性 = {{ matForm.benchmarkAmountYuan || '0.00' }} × {{ matForm.ratePercent || '0' }}%
              = {{ fmtMoney(Math.round(toCents(matForm.benchmarkAmountYuan) * toPPM(matForm.ratePercent) / 1000000)) }}</li>
          </ul>
        </div>
      </div>
      <template #footer>
        <Button variant="outline" @click="matOpen = false">取消</Button>
        <Button :disabled="busy" @click="saveMat">保存</Button>
      </template>
    </Modal>

    <!-- -------------------------------------------------------- 调整弹窗 -->
    <Modal v-model:open="adjOpen" :title="adjForm.id ? '修改审计调整' : '登记审计调整'"
           description="摘要、依据、证据是底稿的三根柱子；分录必须借贷平衡。" width="max-w-5xl">
      <div class="space-y-4">
        <div class="grid gap-3 sm:grid-cols-3">
          <div>
            <Label>种类</Label>
            <select v-model="adjForm.kind"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option value="adjust">调整（动损益 / 资产负债）</option>
              <option value="reclass">重分类（不损益，只在项目间搬家）</option>
            </select>
          </div>
          <div class="sm:col-span-2">
            <Label>摘要</Label>
            <Input v-model="adjForm.summary" class="mt-1.5" placeholder="补提 2025 年折旧" />
          </div>
        </div>
        <div class="grid gap-3 sm:grid-cols-2">
          <div>
            <Label>调整依据（必填）</Label>
            <textarea v-model="adjForm.reason" rows="2"
                      class="mt-1.5 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm"
                      placeholder="折旧计算表显示少提 3,000.00，按平均年限法补提" />
          </div>
          <div>
            <Label>证据来源</Label>
            <textarea v-model="adjForm.evidence" rows="2"
                      class="mt-1.5 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm"
                      placeholder="折旧计算表（底稿索引 F-3）" />
          </div>
        </div>

        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b bg-muted/40">
                <th class="h-9 w-8 px-2 text-left text-xs font-medium text-muted-foreground">#</th>
                <th class="h-9 px-2 text-left text-xs font-medium text-muted-foreground">摘要</th>
                <th class="h-9 px-2 text-left text-xs font-medium text-muted-foreground">科目</th>
                <th class="h-9 w-40 px-2 text-left text-xs font-medium text-muted-foreground">辅助核算</th>
                <th class="h-9 w-32 px-2 text-right text-xs font-medium text-muted-foreground">借方（元）</th>
                <th class="h-9 w-32 px-2 text-right text-xs font-medium text-muted-foreground">贷方（元）</th>
                <th class="h-9 w-10 px-2" />
              </tr>
            </thead>
            <tbody>
              <tr v-for="(l, i) in adjForm.lines" :key="i" class="border-b last:border-0">
                <td class="px-2 py-1 text-xs text-muted-foreground">{{ i + 1 }}</td>
                <td class="px-2 py-1">
                  <input v-model="l.summary"
                         class="w-full rounded border-0 bg-transparent px-1.5 py-1 text-sm focus:bg-accent/40 focus:outline-none"
                         placeholder="留空用调整摘要" />
                </td>
                <td class="px-2 py-1">
                  <select v-model="l.accountCode"
                          class="w-full rounded border-0 bg-transparent px-1.5 py-1 text-sm focus:bg-accent/40 focus:outline-none"
                          @change="onAccountChange(l)">
                    <option value="">选择科目…</option>
                    <option v-for="a in meta.accounts" :key="a.code" :value="a.code">
                      {{ a.code }} {{ a.fullName }}
                    </option>
                  </select>
                </td>
                <td class="px-2 py-1">
                  <div v-if="needAux(l).length" class="space-y-1">
                    <template v-for="label in needAux(l)" :key="label">
                      <select v-if="label === '部门'" v-model="l.deptId"
                              class="w-full rounded border border-input bg-transparent px-1 py-0.5 text-xs">
                        <option :value="null">选择部门…</option>
                        <option v-for="d in meta.departments" :key="d.id" :value="d.id">
                          {{ d.fullName || d.name }}
                        </option>
                      </select>
                      <select v-else-if="label === '员工'" v-model="l.employeeId"
                              class="w-full rounded border border-input bg-transparent px-1 py-0.5 text-xs">
                        <option :value="null">选择员工…</option>
                        <option v-for="e in meta.employees" :key="e.id" :value="e.id">{{ e.name }}</option>
                      </select>
                      <select v-else-if="contactsFor(label).length" v-model="l.contactId"
                              class="w-full rounded border border-input bg-transparent px-1 py-0.5 text-xs">
                        <option :value="null">选择{{ label }}…</option>
                        <option v-for="c in contactsFor(label)" :key="c.id" :value="c.id">{{ c.name }}</option>
                      </select>
                      <span v-else class="block text-xs text-muted-foreground">
                        {{ label }}：暂无档案
                      </span>
                    </template>
                  </div>
                  <span v-else class="text-xs text-muted-foreground">—</span>
                </td>
                <td class="px-2 py-1">
                  <input v-model="l.debitYuan"
                         class="num w-full rounded border-0 bg-transparent px-1.5 py-1 text-sm focus:bg-accent/40 focus:outline-none"
                         placeholder="0.00" />
                </td>
                <td class="px-2 py-1">
                  <input v-model="l.creditYuan"
                         class="num w-full rounded border-0 bg-transparent px-1.5 py-1 text-sm focus:bg-accent/40 focus:outline-none"
                         placeholder="0.00" />
                </td>
                <td class="px-2 py-1">
                  <button class="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground"
                          title="删除本行" @click="removeLine(i)">
                    <Trash2 class="size-3.5" />
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
          <div class="mt-2 flex items-center justify-between">
            <Button size="sm" variant="outline" @click="addLine"><Plus /> 加一行</Button>
            <span class="num text-sm"
                  :class="adjDiff === 0 ? 'text-[var(--profit)]' : 'text-[var(--loss)]'">
              {{ adjDiff === 0 ? '借贷平衡' : `差额 ${fmtMoney(Math.abs(adjDiff))}` }}
            </span>
          </div>
        </div>

        <div class="flex items-start gap-2 rounded-lg border bg-muted/30 p-3 text-xs text-muted-foreground">
          <Info class="mt-0.5 size-3.5 shrink-0" />
          <div>
            科目的辅助核算必须填齐 —— 这一条与手工凭证完全一致，登记时就会检查。
            需要某个部门/客户而档案里还没有时，请先到「辅助核算」页建档，
            <b>不要为了让分录存下来而换一个不需要辅助核算的科目</b>。
          </div>
        </div>
      </div>
      <template #footer>
        <Button variant="outline" @click="adjOpen = false">取消</Button>
        <!-- 按钮文字不能与弹窗标题上的「登记调整」重复：
             同一个词在页面上出现两次，测试与辅助工具都只能拿到第一个 -->
        <Button :disabled="busy" @click="saveAdj">
          <ScrollText /> {{ adjForm.id ? '保存修改' : '保存调整' }}
        </Button>
      </template>
    </Modal>
  </div>
</template>
