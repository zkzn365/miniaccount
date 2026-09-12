<script setup>
import { computed, onMounted, ref } from 'vue'
import {
  Users, Plus, Calculator, CheckCircle2, Pencil, Info, Download, Save, Trash2,
} from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
import { bookkeeper, loadBookkeeper, rememberBookkeeper } from '@/lib/operator'
import { fmtMoney, centsToYuanInput, parseYuanToCents } from '@/lib/format'
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

const tab = ref('runs') // runs | employees | tax
const departments = ref([])

const runs = ref([])
const employees = ref([])
const taxTable = ref(null)

// 社保方案。★ 这是工资模块的前置：没有方案，员工的社保算不出来，
// 而「留空」会被静默当成按 0 缴纳 —— 一张看起来正常、实际没扣社保的工资单。
const schemes = ref([])
const schemeEditing = ref(null)
const schemeBusy = ref(false)
const loading = ref(false)
const busy = ref(false)
// 记账人来自本机设置（见 lib/operator.js）：全程序一份，不再各页各存一份
const operator = bookkeeper

// 预览 / 详情
const detail = ref(null)
const detailOpen = ref(false)
const buildOpen = ref(false)
const buildForm = ref({
  period: '', onlyPosted: false, save: true,
})

// 员工编辑
const empOpen = ref(false)
const empForm = ref(emptyEmployee())

function emptyEmployee() {
  return {
    id: 0, code: '', name: '', idCard: '', phone: '',
    bankName: '', bankAccount: '',
    baseSalary: '', siBase: '', hfbBase: '', specialAdditional: '',
    siProfile: '', hireDate: '', leaveDate: '', enabled: true, remark: '',
  }
}

// ---------------------------------------------------------------------------
// 部门管理
// ---------------------------------------------------------------------------
//
// ★ 这一整块以前不存在：后端有 SaveDepartment，但没有任何绑定暴露它，
// 界面上也只有员工表单里那个只读的下拉。于是用户**根本没法新建部门**，
// 而绝大多数费用科目要求按部门辅助核算 —— 没有部门就记不了费用。

const deptOpen = ref(false)
const deptForm = ref(emptyDept())

/** 今天的日期 YYYY-MM-DD。
 *
 * ★ 不能用 toISOString().slice(0,10) —— 那是 UTC。东八区下午 8 点之后
 * 它会返回「明天」，离职日期默认值就错了一天，而且是看不出来的那种错。 */
function todayISO() {
  const d = new Date()
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

function emptyDept() {
  return { id: 0, code: '', name: '', parentId: null, enabled: true, remark: '' }
}

/** 某个部门下的在职员工数（列表里直接显示，省得点进去数）。 */
function headcountOf(deptId) {
  return employees.value.filter((e) => e.deptId === deptId && !e.leaveDate).length
}

function editDept(d) {
  deptForm.value = d
    ? { id: d.id, code: d.code, name: d.name, parentId: d.parentId, enabled: d.enabled, remark: d.remark ?? '' }
    : emptyDept()
  deptOpen.value = true
}

async function saveDept() {
  if (!deptForm.value.name.trim()) { notify('部门名称不能为空', 'warn'); return }
  busy.value = true
  const r = await api.saveDepartment(deptForm.value)
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(deptForm.value.id ? '部门已更新' : '部门已新建', 'success')
  deptOpen.value = false
  await load()
}

async function toggleDept(d) {
  busy.value = true
  const r = await api.saveDepartment({ ...d, enabled: !d.enabled })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(d.enabled ? `已停用「${d.name}」` : `已启用「${d.name}」`, 'success')
  await load()
}

async function removeDept(d) {
  // ★ 先问一次「这个部门被什么引用着」，把话说明白再让用户点确认。
  //
  // 直接删然后弹报错是最差的做法：用户已经在心里做了「删掉」的决定，
  // 却被一句错误打回来，还得自己去猜是哪个员工挂在下面。
  const u = await api.departmentUsageOf(d.id)
  if (!u.ok) { notify(u.fault.message, 'error', u.fault.detail); return }
  const used = describeUsage(u.data)
  if (used) {
    notify(`「${d.name}」还在被使用：${used}`, 'warn',
      '已经记进账的凭证不会因为删部门而改变，所以不能连它一起清掉。' +
      '如果这个部门只是撤了、历史还要留，请改用「停用」。')
    return
  }
  if (!window.confirm(`确定删除部门「${d.name}」？此操作不可撤销。`)) return
  busy.value = true
  const r = await api.deleteDepartment(d.id)
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已删除部门「${d.name}」`, 'success')
  await load()
}

function describeUsage(u) {
  const parts = []
  const add = (n, what) => { if (n > 0) parts.push(`${what} ${n} 处`) }
  add(u.employees, '员工')
  add(u.children, '下级部门')
  add(u.entries, '凭证')
  add(u.bankFlows, '银行流水')
  add(u.bankRules, '银行规则')
  add(u.invoices, '发票')
  add(u.claims, '报销单')
  return parts.join('、')
}

/** 在某个部门下新增员工 —— 建员工时必须选部门，索性从这里进去就带好。 */
function addEmployeeIn(d) {
  editEmployee(null)
  empForm.value.deptId = d.id
}

// ---------------------------------------------------------------------------
// 人事异动：离职 / 转部门 / 调薪
// ---------------------------------------------------------------------------

const moveOpen = ref(false)
const moveKind = ref('transfer') // transfer | salary | resign
const moveForm = ref({})
const moveTarget = ref(null)

function openTransfer(e) {
  moveKind.value = 'transfer'
  moveTarget.value = e
  // 默认选一个「不是当前部门」的，省得用户点开就看到「已经在 X 了」
  const other = departments.value.find((d) => d.enabled && d.id !== e.deptId)
  moveForm.value = { deptId: other?.id ?? null, reason: '' }
  moveOpen.value = true
}

function openSalary(e) {
  moveKind.value = 'salary'
  moveTarget.value = e
  moveForm.value = {
    baseSalary: centsToYuanInput(e.baseSalary),
    siBase: centsToYuanInput(e.siBase),
    hfbBase: centsToYuanInput(e.hfbBase),
    reason: '',
  }
  moveOpen.value = true
}

function openResign(e) {
  moveKind.value = 'resign'
  moveTarget.value = e
  moveForm.value = { leaveDate: todayISO(), reason: '' }
  moveOpen.value = true
}

async function submitMove() {
  const e = moveTarget.value
  busy.value = true
  let r
  if (moveKind.value === 'transfer') {
    r = await api.transferEmployee({
      id: e.id, deptId: moveForm.value.deptId ?? 0,
      reason: moveForm.value.reason, operator: operator.value,
    })
  } else if (moveKind.value === 'salary') {
    r = await api.adjustSalary({
      id: e.id, baseSalary: moveForm.value.baseSalary,
      siBase: moveForm.value.siBase, hfbBase: moveForm.value.hfbBase,
      reason: moveForm.value.reason, operator: operator.value,
    })
  } else {
    r = await api.resignEmployee({
      id: e.id, leaveDate: moveForm.value.leaveDate,
      reason: moveForm.value.reason, operator: operator.value,
    })
  }
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify({ transfer: '已转部门', salary: '已调薪', resign: '已办理离职' }[moveKind.value], 'success')
  moveOpen.value = false
  await load()
}

async function loadSchemes() {
  const r = await api.insuranceSchemes()
  if (r.ok) schemes.value = r.data ?? []
}

async function newScheme() {
  const r = await api.schemeTemplate('')
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  // 模板的费率全是 0 —— 这是故意的，见 SchemeTemplate 的说明
  schemeEditing.value = { ...r.data, isNew: true }
}

async function saveSchemes() {
  if (!schemeEditing.value) return
  const next = schemes.value.map((x) => x.scheme)
  const i = next.findIndex((x) => x.name === schemeEditing.value.name)
  if (i >= 0) next[i] = schemeEditing.value
  else next.push(schemeEditing.value)

  schemeBusy.value = true
  const r = await api.saveInsuranceSchemes(next)
  schemeBusy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已保存社保方案「${schemeEditing.value.name}」`, 'success')
  schemeEditing.value = null
  await loadSchemes()
}

async function removeScheme(name) {
  const used = schemes.value.find((x) => x.scheme.name === name)?.usedBy ?? 0
  if (used > 0) {
    notify(`「${name}」正在被 ${used} 名员工使用，不能删除`, 'error',
      '请先把那些员工的参保方案改成别的，或改回空（不缴社保）。')
    return
  }
  if (!confirm(`删除社保方案「${name}」？`)) return
  const next = schemes.value.map((x) => x.scheme).filter((x) => x.name !== name)
  if (next.length === 0) {
    notify('至少要保留一个社保方案', 'warn')
    return
  }
  const r = await api.saveInsuranceSchemes(next)
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已删除', 'success')
  await loadSchemes()
}

async function editScheme(scheme) {
  // 深拷贝：编辑期间不该动到列表里的那一份
  schemeEditing.value = JSON.parse(JSON.stringify(scheme))
}

// 费率在界面上按「百分比」填，模型里是百万分之一（ppm）。
// 8% → 80000 ppm，所以在界面上除以 10000。
function ppmToPct(v) { return v ? +(v / 10000).toFixed(4) : 0 }
function pctToPpm(v) { return Math.round((Number(v) || 0) * 10000) }
// 合计费率的展示
function pctText(ppm) { return ppm ? (ppm / 10000).toFixed(2).replace(/\.?0+$/, '') + '%' : '0%' }

const RATE_FIELDS = [
  { key: 'pensionSelf', label: '养老（个人）' },
  { key: 'medicalSelf', label: '医疗（个人）' },
  { key: 'unemploymentSelf', label: '失业（个人）' },
  { key: 'housingFundSelf', label: '公积金（个人）' },
  { key: 'pensionCo', label: '养老（单位）' },
  { key: 'medicalCo', label: '医疗（单位）' },
  { key: 'unemploymentCo', label: '失业（单位）' },
  { key: 'injuryCo', label: '工伤（单位）' },
  { key: 'maternityCo', label: '生育（单位）' },
  { key: 'housingFundCo', label: '公积金（单位）' },
]

async function load() {
  loading.value = true
  const [r, e, t, d] = await Promise.all([
    api.payrollRuns(), api.employees(false), api.taxTableInfo(), api.departments(),
  ])
  loading.value = false
  if (r.ok) runs.value = r.data ?? []
  if (e.ok) employees.value = e.data ?? []
  if (t.ok) taxTable.value = t.data
  await loadSchemes()
  if (d.ok) departments.value = d.data ?? []
}

onMounted(async () => {
  // 记账人是本机设置（全程序一份），与页面数据一起加载
  await Promise.all([load(), loadBookkeeper()])
  const b = await api.periods()
  if (b.ok) {
    const cur = b.data.periods.find((p) => p.status === 'open')
    buildForm.value.period = cur?.label ?? b.data.periods[0]?.label ?? ''
  }
})

const totals = computed(() => {
  const t = { gross: 0, iit: 0, net: 0, headcount: 0 }
  for (const r of runs.value) {
    if (r.status === 'voided') continue
    t.gross += r.totalGross
    t.iit += r.totalIit
    t.net += r.totalNet
  }
  return t
})

// ---------------------------------------------------------------- 工资单

async function doBuild() {
  const [y, m] = (buildForm.value.period || '-').split('-')
  if (!y || !m) { notify('请选择会计期间', 'warn'); return }
  busy.value = true
  const r = await api.buildPayroll({
    year: Number(y), month: Number(m),
    createdBy: operator.value || '制单人',
    onlyPostedHistory: buildForm.value.onlyPosted,
    save: buildForm.value.save,
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  detail.value = r.data
  detailOpen.value = true
  buildOpen.value = false
  if (buildForm.value.save) {
    notify('工资单已生成（草稿），请核对后再记账', 'success')
    await load()
  }
}

async function openRun(row) {
  const r = await api.payrollRunDetail(row.id)
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  detail.value = r.data
  detailOpen.value = true
}

async function postRun() {
  if (!operator.value.trim()) { notify('请填写记账人', 'warn'); return }
  busy.value = true
  const r = await api.postPayroll(detail.value.id, operator.value.trim())
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`工资单已记账，生成计提与发放两张凭证`, 'success')
  detail.value = r.data
  await load()
}

async function exportPayroll() {
  const path = window.prompt('导出到（完整路径）',
    `/tmp/工资表-${detail.value.period}.xlsx`)
  if (!path) return
  const r = await api.exportReport({ kind: 'payroll', dest: path, runId: detail.value.id })
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已导出：${r.data.path}（${r.data.rows} 行）`, 'success')
}

// ---------------------------------------------------------------- 员工

function editEmployee(row) {
  empForm.value = row ? {
    ...row,
    baseSalary: row.baseSalary ? (row.baseSalary / 100).toFixed(2) : '',
    siBase: row.siBase ? (row.siBase / 100).toFixed(2) : '',
    hfbBase: row.hfbBase ? (row.hfbBase / 100).toFixed(2) : '',
    specialAdditional: row.specialAdditional ? (row.specialAdditional / 100).toFixed(2) : '',
  } : emptyEmployee()
  empOpen.value = true
}

async function saveEmployee() {
  if (!empForm.value.name.trim()) { notify('请填写员工姓名', 'warn'); return }
  busy.value = true
  const r = await api.saveEmployee(empForm.value)
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('员工档案已保存', 'success')
  empOpen.value = false
  await load()
}

const statusTone = (s) => (s === 'posted' ? 'profit' : s === 'confirmed' ? 'default' : 'warn')
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- 标签页 -->
    <div class="flex items-center gap-2">
      <Button
        v-for="t in [
          { v: 'runs', n: '工资单' }, { v: 'depts', n: '部门' },
          { v: 'employees', n: '员工档案' }, { v: 'schemes', n: '社保方案' },
          { v: 'tax', n: '个税参数' }]"
        :key="t.v"
        :variant="tab === t.v ? 'default' : 'outline'"
        size="sm"
        @click="tab = t.v"
      >{{ t.n }}</Button>

      <div class="ml-auto flex items-center gap-2">
        <Input v-model="operator"
                 @change="rememberBookkeeper(operator)" class="h-8 w-32" placeholder="操作人" />
        <Button v-if="tab === 'runs'" size="sm" @click="buildOpen = true">
          <Calculator /> 生成工资单
        </Button>
        <Button v-if="tab === 'depts'" size="sm" @click="editDept(null)">
          <Plus /> 新建部门
        </Button>
        <Button v-if="tab === 'employees'" size="sm" @click="editEmployee(null)">
          <Plus /> 新增员工
        </Button>
      </div>
    </div>

    <Spinner v-if="loading" />

    <!-- 工资单列表 -->
    <template v-else-if="tab === 'runs'">
      <div class="grid grid-cols-4 gap-4">
        <Card><CardContent class="pt-5">
          <p class="text-xs text-muted-foreground">工资单数</p>
          <p class="num mt-1 text-xl font-semibold">{{ runs.length }}</p>
        </CardContent></Card>
        <Card><CardContent class="pt-5">
          <p class="text-xs text-muted-foreground">应发合计</p>
          <p class="num mt-1 text-xl font-semibold">{{ fmtMoney(totals.gross) }}</p>
        </CardContent></Card>
        <Card><CardContent class="pt-5">
          <p class="text-xs text-muted-foreground">代扣个税合计</p>
          <p class="num mt-1 text-xl font-semibold text-[var(--credit)]">{{ fmtMoney(totals.iit) }}</p>
        </CardContent></Card>
        <Card><CardContent class="pt-5">
          <p class="text-xs text-muted-foreground">实发合计</p>
          <p class="num mt-1 text-xl font-semibold">{{ fmtMoney(totals.net) }}</p>
        </CardContent></Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>工资单</CardTitle>
          <CardDescription>
            个税按《个人所得税扣缴申报管理办法》的<b>累计预扣预缴法</b>计算。
            工资单先生成为草稿，核对无误后再记账。
          </CardDescription>
        </CardHeader>
        <CardContent class="px-0">
          <EmptyState
            v-if="runs.length === 0"
            title="还没有工资单"
            description="先在「员工档案」里录入员工，再点「生成工资单」"
          >
            <Button size="sm" @click="buildOpen = true"><Calculator /> 生成工资单</Button>
          </EmptyState>
          <table v-else class="w-full text-sm">
            <thead>
              <tr class="border-b bg-muted/40">
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">期间</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">状态</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">人数</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">应发合计</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">个人社保</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">代扣个税</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">实发合计</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">凭证</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in runs" :key="r.id" class="border-b last:border-0 hover:bg-accent/30">
                <td class="px-3 py-1.5 font-medium">{{ r.period }}</td>
                <td class="px-3 py-1.5"><Badge :variant="statusTone(r.status)">{{ r.statusLabel }}</Badge></td>
                <td class="num px-3 py-1.5">{{ r.headcount }}</td>
                <td class="num px-3 py-1.5">{{ fmtMoney(r.totalGross) }}</td>
                <td class="num px-3 py-1.5 text-muted-foreground">{{ fmtMoney(r.totalSiSelf) }}</td>
                <td class="num px-3 py-1.5 text-[var(--credit)]">{{ fmtMoney(r.totalIit) }}</td>
                <td class="num px-3 py-1.5 font-medium">{{ fmtMoney(r.totalNet) }}</td>
                <td class="px-3 py-1.5">
                  <div class="flex gap-1">
                    <Badge v-if="r.hasAccrualVoucher" variant="muted">计提</Badge>
                    <Badge v-if="r.hasPaymentVoucher" variant="muted">发放</Badge>
                    <span v-if="!r.hasAccrualVoucher && !r.hasPaymentVoucher" class="text-xs text-muted-foreground">—</span>
                  </div>
                </td>
                <td class="px-3 py-1.5">
                  <div class="flex justify-end gap-1">
                    <Button variant="ghost" size="sm" @click="openRun(r)">查看</Button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </CardContent>
      </Card>
    </template>

    <!-- 员工档案 -->
    <!-- 部门 -->
    <Card v-else-if="tab === 'depts'">
      <CardHeader>
        <CardTitle>部门</CardTitle>
        <CardDescription>
          绝大多数费用科目按<b>部门</b>辅助核算 —— 没有部门，费用凭证过不了账，
          员工也没法计提工资。这里可以新建、改名、调整上下级；
          <b>撤掉的部门用「停用」，不要删</b>：停用之后新凭证选不到它，历史报表照旧。
        </CardDescription>
      </CardHeader>
      <CardContent class="px-0">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40">
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">编码</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">名称</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">上级</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">状态</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">在职员工</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="d in departments" :key="d.id" class="border-b last:border-0 hover:bg-accent/30">
              <td class="px-3 py-1.5 font-mono text-xs">{{ d.code }}</td>
              <td class="px-3 py-1.5 font-medium">{{ d.name }}</td>
              <td class="px-3 py-1.5 text-xs text-muted-foreground">
                {{ departments.find((x) => x.id === d.parentId)?.name || '—' }}
              </td>
              <td class="px-3 py-1.5">
                <Badge :variant="d.enabled ? 'profit' : 'muted'">
                  {{ d.enabled ? '启用' : '已停用' }}
                </Badge>
              </td>
              <td class="num px-3 py-1.5 text-muted-foreground">{{ headcountOf(d.id) }}</td>
              <td class="px-3 py-1.5">
                <div class="flex justify-end gap-0.5">
                  <Button variant="ghost" size="sm" @click="addEmployeeIn(d)">
                    <Plus /> 加员工
                  </Button>
                  <Button variant="ghost" size="sm" @click="editDept(d)"><Pencil /> 编辑</Button>
                  <Button variant="ghost" size="sm" :disabled="busy" @click="toggleDept(d)">
                    {{ d.enabled ? '停用' : '启用' }}
                  </Button>
                  <Button variant="ghost" size="sm" class="text-destructive"
                          :disabled="busy" @click="removeDept(d)">删除</Button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </CardContent>
    </Card>

    <Card v-else-if="tab === 'employees'">
      <CardHeader>
        <CardTitle>员工档案</CardTitle>
        <CardDescription>
          「标准月工资」与「专项附加扣除」都存在档案里，生成工资单时自动带入 ——
          这两项绝大多数月份是不变的，不该每月重填。
        </CardDescription>
      </CardHeader>
      <CardContent class="px-0">
        <EmptyState
          v-if="employees.length === 0"
          title="还没有员工"
          description="录入员工后才能生成工资单"
        >
          <Button size="sm" @click="editEmployee(null)"><Plus /> 新增员工</Button>
        </EmptyState>
        <table v-else class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40">
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">编码</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">姓名</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">状态</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">标准月工资</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">社保基数</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">专项附加</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">部门</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">工资科目</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">社保方案</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">入职</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in employees" :key="e.id" class="border-b last:border-0 hover:bg-accent/30">
              <td class="px-3 py-1.5 font-mono text-xs">{{ e.code }}</td>
              <td class="px-3 py-1.5 font-medium">{{ e.name }}</td>
              <td class="px-3 py-1.5">
                <Badge :variant="e.enabled ? 'profit' : 'muted'">{{ e.statusLabel }}</Badge>
              </td>
              <td class="num px-3 py-1.5">{{ fmtMoney(e.baseSalary) }}</td>
              <td class="num px-3 py-1.5 text-muted-foreground">{{ fmtMoney(e.siBase) }}</td>
              <td class="num px-3 py-1.5 text-muted-foreground">{{ fmtMoney(e.specialAdditional) }}</td>
              <td class="px-3 py-1.5 text-xs text-muted-foreground">
                {{ departments.find((d) => d.id === e.deptId)?.name || '—' }}
              </td>
              <td class="px-3 py-1.5 font-mono text-xs text-muted-foreground">
                {{ e.expenseAccountCode || '—' }}
              </td>
              <td class="px-3 py-1.5 text-xs text-muted-foreground">{{ e.siProfile || '不缴' }}</td>
              <td class="px-3 py-1.5 text-xs text-muted-foreground">{{ e.hireDate || '—' }}</td>
              <td class="px-3 py-1.5">
                <div class="flex justify-end gap-0.5">
                  <!-- ★ 三个异动做成显式动作，而不是「进编辑框自己改」。
                       编辑框里改离职日期要连「启用」的勾一起去掉，漏一步
                       就是一个已经离职的人继续出现在下个月的工资单里；
                       而调薪、转部门在编辑框里改完，事后也查不出改过什么
                       （日志只剩一句「维护员工档案」）。 -->
                  <Button variant="ghost" size="sm" @click="editEmployee(e)"><Pencil /> 编辑</Button>
                  <Button v-if="!e.leaveDate" variant="ghost" size="sm"
                          @click="openTransfer(e)">转部门</Button>
                  <Button v-if="!e.leaveDate" variant="ghost" size="sm"
                          @click="openSalary(e)">调薪</Button>
                  <Button v-if="!e.leaveDate" variant="ghost" size="sm"
                          class="text-[var(--warn)]" @click="openResign(e)">离职</Button>
                  <Badge v-else variant="muted">{{ e.leaveDate }} 离职</Badge>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </CardContent>
    </Card>

    <!-- 社保方案 -->
    <Card v-else-if="tab === 'schemes'">
      <CardHeader>
        <div class="flex items-center justify-between">
          <div>
            <CardTitle>社保方案</CardTitle>
            <CardDescription>
              社保比例与缴费基数上下限按城市、按年度发布，各地差异很大，
              因此本软件不预置任何数值 ——
              预置一组看着像真的比例会被直接当真使用，而算错了不会报任何错。
              请按当地社保局公布的当年标准填写。
            </CardDescription>
          </div>
          <Button size="sm" @click="newScheme"><Plus /> 新增方案</Button>
        </div>
      </CardHeader>
      <CardContent class="px-0">
        <EmptyState
          v-if="schemes.length === 0"
          title="还没有社保方案"
          description="工资模块需要至少一个方案，否则员工的社保算不出来（留空会被当成按 0 缴纳）"
        />
        <table v-else class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40">
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">方案</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">城市</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">社保基数上下限</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">个人合计</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">单位合计</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">引用员工</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">状态</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in schemes" :key="row.scheme.name" class="border-b hover:bg-accent/30">
              <td class="px-3 py-2 font-medium">{{ row.scheme.name }}</td>
              <td class="px-3 py-2 text-muted-foreground">{{ row.scheme.city || '—' }}</td>
              <td class="num px-3 py-2 text-xs">
                {{ fmtMoney(row.scheme.baseMin) }} ~ {{ row.scheme.baseMax || '不限' }}
              </td>
              <td class="num px-3 py-2 text-right">
                {{ pctText(row.scheme.rates.pensionSelf + row.scheme.rates.medicalSelf
                  + row.scheme.rates.unemploymentSelf + row.scheme.rates.housingFundSelf) }}
              </td>
              <td class="num px-3 py-2 text-right">
                {{ pctText(row.scheme.rates.pensionCo + row.scheme.rates.medicalCo
                  + row.scheme.rates.unemploymentCo + row.scheme.rates.injuryCo
                  + row.scheme.rates.maternityCo + row.scheme.rates.housingFundCo) }}
              </td>
              <td class="num px-3 py-2 text-right">{{ row.usedBy }}</td>
              <td class="px-3 py-2">
                <Badge v-if="row.configured" variant="muted">已配置</Badge>
                <Badge v-else variant="warn">费率全为 0</Badge>
              </td>
              <td class="px-3 py-2">
                <div class="flex justify-end gap-1">
                  <Button variant="ghost" size="sm" title="编辑" @click="editScheme(row.scheme)">
                    <Pencil />
                  </Button>
                  <Button variant="ghost" size="sm" title="删除" @click="removeScheme(row.scheme.name)">
                    <Trash2 />
                  </Button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </CardContent>
    </Card>

    <!-- 部门编辑 -->
    <Modal
      v-model:open="deptOpen"
      :title="deptForm.id ? '编辑部门' : '新建部门'"
      description="部门是费用类科目辅助核算的必填维度 —— 没有部门，费用凭证会被「缺少必需的辅助核算」拒绝。"
      width="max-w-lg"
    >
      <div class="flex flex-col gap-3">
        <div>
          <Label>部门名称 *</Label>
          <Input v-model="deptForm.name" class="mt-1.5" placeholder="销售部" />
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <Label>编码</Label>
            <Input v-model="deptForm.code" class="mt-1.5" placeholder="留空则用名称" />
          </div>
          <div>
            <Label>上级部门</Label>
            <select v-model="deptForm.parentId"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-3 text-sm">
              <option :value="null">（顶层）</option>
              <option v-for="d in departments.filter((x) => x.id !== deptForm.id)"
                      :key="d.id" :value="d.id">{{ d.fullName || d.name }}</option>
            </select>
          </div>
        </div>
        <div>
          <Label>备注</Label>
          <Input v-model="deptForm.remark" class="mt-1.5" />
        </div>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="deptForm.enabled" type="checkbox" class="size-4 rounded border-input" />
          启用（停用后新建凭证选不到它，历史报表照旧）
        </label>
      </div>
      <template #footer>
        <div class="flex justify-end gap-2">
          <Button variant="outline" @click="deptOpen = false">取消</Button>
          <Button :disabled="busy" @click="saveDept"><Save /> 保存</Button>
        </div>
      </template>
    </Modal>

    <!-- 人事异动：转部门 / 调薪 / 离职 -->
    <Modal
      v-model:open="moveOpen"
      :title="{ transfer: '转部门', salary: '调薪', resign: '办理离职' }[moveKind]"
      :description="{
        transfer: '只改档案上的所属部门。已经生成的工资单各自固化了当时的部门，不会跟着变。',
        salary: '只改档案上的标准工资。已经生成的工资单各自固化了当时的基本工资与社保基数，不会跟着变；下个月生成时才按新标准算。',
        resign: '写离职日期并停用档案。离职之后他不会再被自动带进新的工资单；已计提的工资不受影响。',
      }[moveKind]"
      width="max-w-lg"
    >
      <div v-if="moveTarget" class="flex flex-col gap-3">
        <p class="text-sm">
          <span class="text-muted-foreground">员工：</span>
          <span class="font-medium">{{ moveTarget.name }}</span>
          <span class="ml-2 text-xs text-muted-foreground">{{ moveTarget.code }}</span>
        </p>

        <template v-if="moveKind === 'transfer'">
          <div>
            <Label>调入部门 *</Label>
            <select v-model="moveForm.deptId"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-3 text-sm">
              <option v-for="d in departments.filter((x) => x.enabled)" :key="d.id" :value="d.id">
                {{ d.fullName || d.name }}
              </option>
            </select>
            <p class="mt-1 text-xs text-muted-foreground">
              只有启用的部门能选 —— 停用的部门在新建凭证时是选不到的，调进去工资会卡在辅助核算上。
            </p>
          </div>
        </template>

        <template v-else-if="moveKind === 'salary'">
          <div>
            <Label>新月基本工资（元）*</Label>
            <Input v-model="moveForm.baseSalary" class="mt-1.5 num" placeholder="8000.00" />
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div>
              <Label>社保基数（元）</Label>
              <Input v-model="moveForm.siBase" class="mt-1.5 num" placeholder="留空跟随工资" />
            </div>
            <div>
              <Label>公积金基数（元）</Label>
              <Input v-model="moveForm.hfbBase" class="mt-1.5 num" placeholder="留空跟随社保" />
            </div>
          </div>
          <p class="text-xs text-muted-foreground">
            很多小微企业按最低基数缴纳，所以基数与工资分开填 —— 强制同步会逼你填一个错的数。
          </p>
        </template>

        <template v-else>
          <div>
            <Label>离职日期 *</Label>
            <Input v-model="moveForm.leaveDate" class="mt-1.5" placeholder="2026-03-31" />
            <p class="mt-1 text-xs text-muted-foreground">
              它决定这个人从哪个月起不再进工资单。不能早于入职日期。
            </p>
          </div>
        </template>

        <div>
          <Label>原因（选填）</Label>
          <Input v-model="moveForm.reason" class="mt-1.5" placeholder="会写进操作日志" />
        </div>
      </div>
      <template #footer>
        <div class="flex justify-end gap-2">
          <Button variant="outline" @click="moveOpen = false">取消</Button>
          <Button :disabled="busy"
                  :variant="moveKind === 'resign' ? 'destructive' : 'default'"
                  @click="submitMove">
            {{ { transfer: '确认调动', salary: '确认调薪', resign: '确认离职' }[moveKind] }}
          </Button>
        </div>
      </template>
    </Modal>

    <!-- 社保方案编辑 -->
    <Modal
      v-if="schemeEditing"
      :open="!!schemeEditing"
      title="社保方案"
      description="填的是比例（如 8 表示 8%），不是金额。基数上下限留空表示不设限，通常按上年度社会平均工资的 60% 和 300% 填写。"
      width="max-w-3xl"
      @update:open="(v) => { if (!v) schemeEditing = null }"
    >
      <div class="flex flex-col gap-4">
        <div class="grid grid-cols-2 gap-3">
          <div>
            <Label>方案名称</Label>
            <Input v-model="schemeEditing.name" class="mt-1.5" placeholder="如：杭州标准" />
          </div>
          <div>
            <Label>城市</Label>
            <Input v-model="schemeEditing.city" class="mt-1.5" placeholder="如：杭州" />
          </div>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <Label>社保基数下限（元）</Label>
            <Input v-model="schemeEditing.baseMinYuan" class="mt-1.5"
                   :placeholder="fmtMoney(schemeEditing.baseMin)" />
          </div>
          <div>
            <Label>社保基数上限（元）</Label>
            <Input v-model="schemeEditing.baseMaxYuan" class="mt-1.5"
                   :placeholder="fmtMoney(schemeEditing.baseMax)" />
          </div>
        </div>

        <div>
          <Label>费率（百分比）</Label>
          <div class="mt-2 grid grid-cols-2 gap-x-6 gap-y-2">
            <div v-for="f in RATE_FIELDS" :key="f.key" class="flex items-center gap-2">
              <span class="w-32 shrink-0 text-sm">{{ f.label }}</span>
              <Input
                :model-value="ppmToPct(schemeEditing.rates[f.key])"
                class="h-8"
                @update:model-value="(v) => schemeEditing.rates[f.key] = pctToPpm(v)"
              />
              <span class="text-xs text-muted-foreground">%</span>
            </div>
          </div>
        </div>
      </div>
      <template #footer>
        <Button variant="ghost" @click="schemeEditing = null">取消</Button>
        <Button :disabled="schemeBusy" @click="saveSchemes">保存</Button>
      </template>
    </Modal>

    <!-- 个税参数 -->
    <Card v-else-if="tab === 'tax' && taxTable">
      <CardHeader>
        <CardTitle>{{ taxTable.name }}</CardTitle>
        <CardDescription>{{ taxTable.note }}</CardDescription>
      </CardHeader>
      <CardContent>
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40">
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">级数</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">累计应纳税所得额上限</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">预扣率</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">速算扣除数</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(b, i) in taxTable.brackets" :key="i" class="border-b last:border-0">
              <td class="px-3 py-1.5">{{ i + 1 }}</td>
              <td class="num px-3 py-1.5">
                {{ b.upper === 0 ? '无上限' : fmtMoney(b.upper) }}
              </td>
              <td class="num px-3 py-1.5 font-medium">{{ b.rateLabel }}</td>
              <td class="num px-3 py-1.5">{{ fmtMoney(b.deduction) }}</td>
            </tr>
          </tbody>
        </table>
        <div class="mt-4 flex items-start gap-2 rounded-md border bg-muted/30 p-3 text-xs">
          <Info class="mt-0.5 size-3.5 shrink-0" />
          <span>
            税率表与社保方案都是可配置的数据，不硬编码在代码里。
            个税政策每年可能调整，软件更新应当只换数据，不改逻辑。
            实际的社保比例与基数上下限请以当地社保局公布的标准为准。
          </span>
        </div>
      </CardContent>
    </Card>

    <!-- 生成工资单 -->
    <Modal
      v-model:open="buildOpen"
      title="生成工资单"
      description="按员工档案里的标准工资计算。个税用累计预扣预缴法，会读取该员工本年度之前各月的工资单。"
      width="max-w-xl"
    >
      <div class="flex flex-col gap-4">
        <div>
          <Label>会计期间</Label>
          <Input v-model="buildForm.period" class="mt-1.5" placeholder="2025-03" />
        </div>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="buildForm.onlyPosted" type="checkbox" class="size-4 rounded border-input" />
          累计数只统计已确认/已过账的历史工资单
          <span class="text-xs text-muted-foreground">（默认含草稿，更保险）</span>
        </label>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="buildForm.save" type="checkbox" class="size-4 rounded border-input" />
          生成后保存
          <span class="text-xs text-muted-foreground">（不勾选则只预演，不写库）</span>
        </label>
        <div class="flex items-start gap-2 rounded-md border bg-muted/30 p-3 text-xs">
          <Info class="mt-0.5 size-3.5 shrink-0" />
          <span>
            累计预扣预缴法的税额依赖全年数据：第一个月与第十二个月的税额可能差很多。
            如果这是本年度第一次做工资，之后各月请按期生成，否则累计数会不准。
          </span>
        </div>
      </div>
      <template #footer>
        <Button variant="ghost" @click="buildOpen = false">取消</Button>
        <Button :disabled="busy" @click="doBuild">
          <Calculator /> {{ buildForm.save ? '生成并保存' : '预演一下' }}
        </Button>
      </template>
    </Modal>

    <!-- 工资单详情 -->
    <Modal
      v-model:open="detailOpen"
      :title="detail ? `工资单 ${detail.period}` : '工资单'"
      :description="detail?.taxNote"
      width="max-w-6xl"
    >
      <div v-if="detail" class="flex flex-col gap-4">
        <div class="flex items-center gap-3">
          <Badge :variant="statusTone(detail.status)">{{ detail.statusLabel }}</Badge>
          <span class="text-sm text-muted-foreground">{{ detail.headcount }} 人</span>
          <div class="ml-auto flex gap-3 text-sm">
            <span class="text-muted-foreground">应发 <b class="num text-foreground">{{ fmtMoney(detail.totalGross) }}</b></span>
            <span class="text-muted-foreground">个税 <b class="num text-foreground">{{ fmtMoney(detail.totalIit) }}</b></span>
            <span class="text-muted-foreground">实发 <b class="num text-foreground">{{ fmtMoney(detail.totalNet) }}</b></span>
          </div>
        </div>

        <div class="overflow-auto rounded-lg border">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b bg-muted/40">
                <th class="h-8 px-2 text-left text-xs font-medium text-muted-foreground">姓名</th>
                <th class="h-8 px-2 text-right text-xs font-medium text-muted-foreground">应发</th>
                <th class="h-8 px-2 text-right text-xs font-medium text-muted-foreground">考勤扣款</th>
                <th class="h-8 px-2 text-right text-xs font-medium text-muted-foreground">其他扣款</th>
                <th class="h-8 px-2 text-right text-xs font-medium text-muted-foreground">个人社保</th>
                <th class="h-8 px-2 text-right text-xs font-medium text-muted-foreground">单位社保</th>
                <th class="h-8 px-2 text-right text-xs font-medium text-muted-foreground">专项附加</th>
                <th class="h-8 px-2 text-right text-xs font-medium text-muted-foreground">累计应纳税所得额</th>
                <th class="h-8 px-2 text-right text-xs font-medium text-muted-foreground">代扣个税</th>
                <th class="h-8 px-2 text-right text-xs font-medium text-muted-foreground">实发</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(it, i) in detail.items" :key="i" class="border-b last:border-0">
                <td class="px-2 py-1.5">{{ it.employeeName }}</td>
                <td class="num px-2 py-1.5">{{ fmtMoney(it.gross) }}</td>
                <td class="num px-2 py-1.5 text-muted-foreground">{{ fmtMoney(it.attendanceDeduct, { blankZero: true }) }}</td>
                <td class="num px-2 py-1.5 text-muted-foreground">{{ fmtMoney(it.otherDeduct, { blankZero: true }) }}</td>
                <td class="num px-2 py-1.5 text-muted-foreground">
                  {{ fmtMoney(it.insuranceSelf, { blankZero: true }) }}
                  <!-- 任职月份数多于工资单张数：累计收入缺月份，个税会少扣。
                       不能只在计算说明里写，账面上要看得见。 -->
                  <span
                    v-if="it.taxWarning"
                    class="ml-1 cursor-help text-[var(--warn)]"
                    :title="it.taxWarning"
                  >⚠</span>
                </td>
                <td class="num px-2 py-1.5 text-muted-foreground">{{ fmtMoney(it.insuranceCompany, { blankZero: true }) }}</td>
                <td class="num px-2 py-1.5 text-muted-foreground">{{ fmtMoney(it.specialAdditional, { blankZero: true }) }}</td>
                <td class="num px-2 py-1.5 text-muted-foreground">{{ fmtMoney(it.taxableIncome) }}</td>
                <td class="num px-2 py-1.5 text-[var(--credit)]">{{ fmtMoney(it.iit, { blankZero: true }) }}</td>
                <td class="num px-2 py-1.5 font-medium">{{ fmtMoney(it.net) }}</td>
              </tr>
              <tr class="bg-muted/40 font-medium">
                <td class="px-2 py-2">合计</td>
                <td class="num px-2 py-2">{{ fmtMoney(detail.totalGross) }}</td>
                <td colspan="3" />
                <td class="num px-2 py-2">{{ fmtMoney(detail.totalSiCompany) }}</td>
                <td />
                <td class="num px-2 py-2">{{ fmtMoney(detail.totalIit) }}</td>
                <td class="num px-2 py-2">{{ fmtMoney(detail.totalNet) }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <p class="text-xs text-muted-foreground">
          「累计应纳税所得额」是累计口径，不是本月数 ——
          累计预扣预缴法下，本月税额由年初至今的累计数决定。
        </p>
      </div>

      <template #footer>
        <Input v-model="operator"
                 @change="rememberBookkeeper(operator)" class="mr-auto h-8 w-32" placeholder="记账人" />
        <Button variant="ghost" @click="detailOpen = false">关闭</Button>
        <Button v-if="detail?.id" variant="outline" @click="exportPayroll">
          <Download /> 导出 Excel
        </Button>
        <Button v-if="detail?.canPost && detail?.id" :disabled="busy" @click="postRun">
          <CheckCircle2 /> 记账
        </Button>
      </template>
    </Modal>

    <!-- 员工编辑 -->
    <Modal
      v-model:open="empOpen"
      :title="empForm.id ? '编辑员工' : '新增员工'"
      width="max-w-3xl"
    >
      <div class="grid grid-cols-3 gap-3">
        <div><Label>工号</Label><Input v-model="empForm.code" class="mt-1.5" /></div>
        <div><Label>姓名 *</Label><Input v-model="empForm.name" class="mt-1.5" /></div>
        <div><Label>身份证号</Label><Input v-model="empForm.idCard" class="mt-1.5" /></div>
        <div><Label>手机</Label><Input v-model="empForm.phone" class="mt-1.5" /></div>
        <div><Label>开户行</Label><Input v-model="empForm.bankName" class="mt-1.5" /></div>
        <div><Label>银行账号</Label><Input v-model="empForm.bankAccount" class="mt-1.5" /></div>

        <div>
          <Label>标准月工资（元）</Label>
          <Input v-model="empForm.baseSalary" class="mt-1.5" placeholder="8000.00" />
        </div>
        <div>
          <Label>社保基数（元）</Label>
          <Input v-model="empForm.siBase" class="mt-1.5" placeholder="留空 = 按工资" />
          <p class="mt-1 text-[11px] text-muted-foreground">按最低基数缴纳的填最低基数</p>
        </div>
        <div>
          <Label>公积金基数（元）</Label>
          <Input v-model="empForm.hfbBase" class="mt-1.5" placeholder="留空 = 按社保基数" />
        </div>
        <div>
          <Label>专项附加扣除（元/月）</Label>
          <Input v-model="empForm.specialAdditional" class="mt-1.5" placeholder="2000.00" />
          <p class="mt-1 text-[11px] text-muted-foreground">子女教育、赡养老人等合计</p>
        </div>
        <div>
          <Label>部门</Label>
          <select
            v-model="empForm.deptId"
            class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm"
          >
            <option :value="null">未指定</option>
            <option v-for="d in departments" :key="d.id" :value="d.id">{{ d.fullName }}</option>
          </select>
          <p class="mt-1 text-[11px] text-[var(--warn)]">费用科目要求部门，不填记不了账</p>
        </div>
        <div>
          <Label>工资费用科目</Label>
          <Input v-model="empForm.expenseAccountCode" class="mt-1.5" placeholder="留空 = 560201 管理费用—工资" />
          <p class="mt-1 text-[11px] text-muted-foreground">
            生产人员填生产成本，销售人员填销售费用
          </p>
        </div>
        <div>
          <Label>社保方案</Label>
          <Input v-model="empForm.siProfile" class="mt-1.5" placeholder="留空 = 不缴社保" />
        </div>
        <div class="flex items-end">
          <label class="flex items-center gap-2 pb-2 text-sm">
            <input v-model="empForm.enabled" type="checkbox" class="size-4 rounded border-input" />
            在职
          </label>
        </div>

        <div><Label>入职日期</Label><Input v-model="empForm.hireDate" class="mt-1.5" placeholder="2025-01-01" /></div>
        <div><Label>离职日期</Label><Input v-model="empForm.leaveDate" class="mt-1.5" /></div>
        <div><Label>备注</Label><Input v-model="empForm.remark" class="mt-1.5" /></div>
      </div>
      <template #footer>
        <Button variant="ghost" @click="empOpen = false">取消</Button>
        <Button :disabled="busy" @click="saveEmployee"><Save /> 保存</Button>
      </template>
    </Modal>
  </div>
</template>
