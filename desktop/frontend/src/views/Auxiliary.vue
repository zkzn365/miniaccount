<script setup>
import { computed, onMounted, ref } from 'vue'
import {
  Users, Plus, Pencil, Save, Building2, UserRound, Truck, Store,
} from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
import { bookkeeper, loadBookkeeper, rememberBookkeeper } from '@/lib/operator'
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

// ---------------------------------------------------------------------------
// 辅助核算设置
// ---------------------------------------------------------------------------
//
// 凭证上的辅助核算有四个维度：**往来单位（客户 / 供应商）、部门、员工**。
// 科目那边只声明「这个科目需要哪几维」（见科目管理），
// 而**每一维具体有哪些**是这里的事 —— 没有档案，录凭证时那个下拉是空的，
// 分录会被「缺少必需的辅助核算」直接拒绝。
//
// 四张表原来散在两处：客户/供应商只能靠录发票时顺手带出来，
// 部门/员工在工资页。这里把它们收到一起 —— 因为它们的**用途是同一个**：
// 记费用、记往来时挑一个。工资页关心的是工资怎么算，
// 部门与员工只是它的输入，不该把档案维护埋在里面。

const tab = ref('customers') // customers | suppliers | depts | employees
const loading = ref(true)
const busy = ref(false)
// 记账人来自本机设置（见 lib/operator.js）：全程序一份，不再各页各存一份
const operator = bookkeeper

const contacts = ref([])
const departments = ref([])
const employees = ref([])

const TABS = [
  { v: 'customers', n: '客户', icon: Store },
  { v: 'suppliers', n: '供应商', icon: Truck },
  { v: 'depts', n: '部门', icon: Building2 },
  { v: 'employees', n: '员工', icon: Users },
]

async function load() {
  loading.value = true
  const [c, d, e] = await Promise.all([
    api.contacts(''),
    api.departments(),
    api.employees(false),
  ])
  loading.value = false
  if (!c.ok) { notify(c.fault.message, 'error', c.fault.detail); return }
  contacts.value = c.data ?? []
  departments.value = d.ok ? (d.data ?? []) : []
  employees.value = e.ok ? (e.data ?? []) : []
}

onMounted(async () => {
  await Promise.all([load(), loadBookkeeper()])
})

/** 当前标签页对应的往来单位类型。 */
const contactKind = computed(() => (tab.value === 'suppliers' ? 'supplier' : 'customer'))
const contactsShown = computed(() => contacts.value.filter((c) => {
  // 「客户与供应商」两边都要出现 —— 它确实是两种身份
  if (tab.value === 'suppliers') return c.kind === 'supplier' || c.kind === 'both'
  return c.kind === 'customer' || c.kind === 'both'
}))

// ---------------------------------------------------------------- 往来单位

const contactOpen = ref(false)
const contactForm = ref(emptyContact())

function emptyContact() {
  return {
    id: 0, kind: 'customer', name: '', shortName: '', taxNo: '',
    bankName: '', bankAccount: '', contactPerson: '', phone: '',
    address: '', enabled: true, remark: '',
  }
}

function editContact(c) {
  contactForm.value = c
    ? {
        id: c.id, kind: c.kind, name: c.name, shortName: c.shortName ?? '',
        taxNo: c.taxNo ?? '', bankName: c.bankName ?? '', bankAccount: c.bankAccount ?? '',
        contactPerson: c.contactPerson ?? '', phone: c.phone ?? '',
        address: c.address ?? '', enabled: c.enabled, remark: c.remark ?? '',
      }
    // 新建时按当前标签页预置类型 —— 在「供应商」页点新建，
    // 存下去却是客户，是最容易犯也最难发现的错
    : { ...emptyContact(), kind: contactKind.value }
  contactOpen.value = true
}

async function saveContact() {
  if (!contactForm.value.name.trim()) { notify('名称不能为空', 'warn'); return }
  busy.value = true
  const r = await api.saveContact(contactForm.value)
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(contactForm.value.id ? '已更新' : '已新建', 'success')
  contactOpen.value = false
  await load()
}

async function toggleContact(c) {
  busy.value = true
  const r = await api.saveContact({ ...c, enabled: !c.enabled })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(c.enabled ? `已停用「${c.name}」` : `已启用「${c.name}」`, 'success')
  await load()
}

async function removeContact(c) {
  const u = await api.contactUsageOf(c.id)
  if (!u.ok) { notify(u.fault.message, 'error', u.fault.detail); return }
  const used = describeContactUsage(u.data)
  if (used) {
    notify(`「${c.name}」还在被使用：${used}`, 'warn',
      '已经记进账的凭证不会因为删档案而改变，所以不能连它一起清掉。' +
      '如果只是不再往来了、历史还要留，请改用「停用」。')
    return
  }
  if (!window.confirm(`确定删除「${c.name}」？此操作不可撤销。`)) return
  busy.value = true
  const r = await api.deleteContact(c.id)
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已删除「${c.name}」`, 'success')
  await load()
}

function describeContactUsage(u) {
  const parts = []
  const add = (n, what) => { if (n > 0) parts.push(`${what} ${n} 处`) }
  // ★ 没有「报销单」这一项：expense_claim 表根本没有 contact_id，
  // 往来单位与报销单之间不存在引用关系（部门那边有，别照着抄）。
  add(u.entries, '凭证')
  add(u.invoices, '发票')
  add(u.bankFlows, '银行流水')
  add(u.bankRules, '银行规则')
  return parts.join('、')
}


// ---------------------------------------------------------------------------
// 部门（原在工资页，整体迁到这里）
// ---------------------------------------------------------------------------

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
  add(u.claims, '报销单')
  return parts.join('、')
}

/** 在某个部门下新增员工 —— 建员工时必须选部门，索性从这里进去就带好。 */
function addEmployeeIn(d) {
  editEmployee(null)
  empForm.value.deptId = d.id
}

// ---------------------------------------------------------------------------
// 人事异动：离职 / 转部门 / 调薪（同原工资页）
// ---------------------------------------------------------------------------

const moveOpen = ref(false)
const moveKind = ref('transfer') // transfer | salary | resign
const moveForm = ref({})
const moveTarget = ref(null)

function openTransfer(e) {
  moveKind.value = 'transfer'
  moveTarget.value = e
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

// ---------------------------------------------------------------- 员工

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

// 部门下拉要排除「停用」的 —— 停用的部门在新建凭证时选不到，
// 把人挂上去，他的工资计提会卡在辅助核算上。
const enabledDepartments = computed(() => departments.value.filter((d) => d.enabled))
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- 标签页 -->
    <div class="flex items-center gap-2">
      <Button
        v-for="t in TABS"
        :key="t.v"
        :variant="tab === t.v ? 'default' : 'outline'"
        size="sm"
        @click="tab = t.v"
      >
        <component :is="t.icon" /> {{ t.n }}
      </Button>

      <div class="ml-auto flex items-center gap-2">
        <Input v-model="operator"
               @change="rememberBookkeeper(operator)" class="h-8 w-32" placeholder="操作人" />
        <Button v-if="tab === 'customers' || tab === 'suppliers'" size="sm" @click="editContact(null)">
          <Plus /> 新建{{ tab === 'suppliers' ? '供应商' : '客户' }}
        </Button>
        <Button v-if="tab === 'depts'" size="sm" @click="editDept(null)">
          <Plus /> 新建部门
        </Button>
        <Button v-if="tab === 'employees'" size="sm" @click="editEmployee(null)">
          <Plus /> 新增员工
        </Button>
      </div>
    </div>

    <p class="text-xs text-muted-foreground">
      凭证上的辅助核算有四个维度：<b>往来单位（客户 / 供应商）、部门、员工</b>。
      「科目管理」里声明每个科目需要哪几维，<b>这里维护每一维具体有哪些</b> ——
      没有档案，录凭证时那个下拉是空的，分录会被「缺少必需的辅助核算」直接拒绝。
      撤掉的对象请用<b>停用</b>而不是删除：停用之后新凭证选不到它，历史报表照旧。
    </p>

    <Spinner v-if="loading" />

    <!-- 客户 / 供应商 -->
    <Card v-else-if="tab === 'customers' || tab === 'suppliers'">
      <CardHeader>
        <CardTitle>{{ tab === 'suppliers' ? '供应商' : '客户' }}</CardTitle>
        <CardDescription>
          应收挂客户、应付挂供应商。简称要填 —— 银行流水里出现的常常是简称，
          「上次同样的对手方怎么记的」是最有效的记账线索。
        </CardDescription>
      </CardHeader>
      <CardContent class="px-0">
        <EmptyState
          v-if="contactsShown.length === 0"
          :title="tab === 'suppliers' ? '还没有供应商' : '还没有客户'"
          description="录采购发票与付款时需要先有供应商档案；应收账款要按客户归集"
        >
          <Button size="sm" @click="editContact(null)"><Plus /> 新建</Button>
        </EmptyState>
        <table v-else class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40">
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">名称</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">简称</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">类型</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">税号</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">联系人</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">状态</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in contactsShown" :key="c.id" class="border-b last:border-0 hover:bg-accent/30">
              <td class="px-3 py-1.5 font-medium">{{ c.name }}</td>
              <td class="px-3 py-1.5 text-xs text-muted-foreground">{{ c.shortName || '—' }}</td>
              <td class="px-3 py-1.5"><Badge variant="outline">{{ c.kindLabel }}</Badge></td>
              <td class="px-3 py-1.5 font-mono text-xs text-muted-foreground">{{ c.taxNo || '—' }}</td>
              <td class="px-3 py-1.5 text-xs text-muted-foreground">
                {{ c.contactPerson || '—' }}<span v-if="c.phone"> · {{ c.phone }}</span>
              </td>
              <td class="px-3 py-1.5">
                <Badge :variant="c.enabled ? 'profit' : 'muted'">{{ c.enabled ? '启用' : '已停用' }}</Badge>
              </td>
              <td class="px-3 py-1.5">
                <div class="flex justify-end gap-0.5">
                  <Button variant="ghost" size="sm" @click="editContact(c)"><Pencil /> 编辑</Button>
                  <Button variant="ghost" size="sm" :disabled="busy" @click="toggleContact(c)">
                    {{ c.enabled ? '停用' : '启用' }}
                  </Button>
                  <Button variant="ghost" size="sm" class="text-destructive"
                          :disabled="busy" @click="removeContact(c)">删除</Button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </CardContent>
    </Card>

    <!-- 部门（原工资页）-->
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

    <!-- 员工（原工资页）-->
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

    <!-- 往来单位编辑 -->
    <Modal
      v-model:open="contactOpen"
      :title="contactForm.id ? '编辑' + (contactForm.kind === 'supplier' ? '供应商' : '客户') : '新建' + (contactForm.kind === 'supplier' ? '供应商' : '客户')"
      description="客户与供应商都记在往来单位档案里，用「类型」区分。一个单位既是客户又是供应商时选「客户与供应商」。"
      width="max-w-2xl"
    >
      <div class="grid grid-cols-2 gap-3">
        <div class="col-span-2">
          <Label>名称 *</Label>
          <Input v-model="contactForm.name" class="mt-1.5" placeholder="杭州云帆科技有限公司" />
        </div>
        <div>
          <Label>类型</Label>
          <select v-model="contactForm.kind"
                  class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-3 text-sm">
            <option value="customer">客户</option>
            <option value="supplier">供应商</option>
            <option value="both">客户与供应商</option>
            <option value="shareholder">股东</option>
            <option value="other">其他单位</option>
          </select>
        </div>
        <div>
          <Label>简称</Label>
          <Input v-model="contactForm.shortName" class="mt-1.5" placeholder="云帆科技" />
        </div>
        <div>
          <Label>纳税人识别号</Label>
          <Input v-model="contactForm.taxNo" class="mt-1.5" />
        </div>
        <div>
          <Label>联系人</Label>
          <Input v-model="contactForm.contactPerson" class="mt-1.5" />
        </div>
        <div>
          <Label>电话</Label>
          <Input v-model="contactForm.phone" class="mt-1.5" />
        </div>
        <div>
          <Label>开户行</Label>
          <Input v-model="contactForm.bankName" class="mt-1.5" />
        </div>
        <div>
          <Label>银行账号</Label>
          <Input v-model="contactForm.bankAccount" class="mt-1.5" />
        </div>
        <div class="col-span-2">
          <Label>地址</Label>
          <Input v-model="contactForm.address" class="mt-1.5" />
        </div>
        <div class="col-span-2">
          <Label>备注</Label>
          <Input v-model="contactForm.remark" class="mt-1.5" />
        </div>
        <label class="col-span-2 flex items-center gap-2 text-sm">
          <input v-model="contactForm.enabled" type="checkbox" class="size-4 rounded border-input" />
          启用（停用后新建凭证选不到它，历史报表照旧）
        </label>
      </div>
      <template #footer>
        <div class="flex justify-end gap-2">
          <Button variant="outline" @click="contactOpen = false">取消</Button>
          <Button :disabled="busy" @click="saveContact"><Save /> 保存</Button>
        </div>
      </template>
    </Modal>

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
