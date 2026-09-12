<script setup>
import { computed, onMounted, ref } from 'vue'
import {
  Plus, Pencil, Trash2, Calculator, Building2, CalendarClock, Info,
  ArchiveRestore, Ban, Eye,
} from 'lucide-vue-next'
import { api, notify, DRAFT_HINT } from '@/lib/api'
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
// 固定资产与费用摊销
// ---------------------------------------------------------------------------
//
// 这两件事是**唯一**两笔「什么业务都没发生、但每月必须记账」的分录。
// 用 Excel 记的问题不是麻烦，是没有一个时点会提醒你漏了：
// 业务凭证漏了银行流水会对不上，折旧漏了账面上什么都不缺 ——
// 只是费用少一块、利润多一块，而报表看上去完全正常。
//
// 所以这一页有两件事要做：
//   1. 把「每月提多少」固化成卡片（这一页的上半部分）；
//   2. 每期计提一次，并**明确告诉你上期漏没漏**（下半部分）。

const tab = ref('assets') // assets | amortizations
const data = ref(null)
const departments = ref([])
const loading = ref(true)
const busy = ref(false)
// 操作人来自本机设置（见 lib/operator.js）：全程序一份
const operator = bookkeeper

// 计提预览
const periods = ref([])
const accrualPeriod = ref('')
const preview = ref(null)
const previewOpen = ref(false)

async function load() {
  loading.value = true
  const [a, p, d] = await Promise.all([api.assets(), api.periods(), api.departments()])
  loading.value = false
  if (!a.ok) { notify(a.fault.message, 'error', a.fault.detail); return }
  data.value = a.data
  // 只要启用中的部门：停用的部门在新建凭证时选不到，
  // 把折旧挂上去，计提那天会卡在辅助核算上
  departments.value = (d.ok ? (d.data ?? []) : []).filter((x) => x.enabled)
  if (p.ok) {
    periods.value = p.data.periods ?? []
    if (!accrualPeriod.value) {
      const open = periods.value.filter((x) => x.status === 'open')
      // 默认落在**最早**的已启用期间：折旧要按期连续提，
      // 跳到最新一期会把中间几个月直接漏掉，而漏提是不报错的
      accrualPeriod.value = (open[0] ?? periods.value[0])?.label ?? ''
    }
  }
}

onMounted(async () => {
  await Promise.all([load(), loadBookkeeper()])
})

// ---------------------------------------------------------------- 固定资产

const assets = computed(() => data.value?.assets ?? [])
const amortizations = computed(() => data.value?.amortizations ?? [])
const defaults = computed(() => data.value?.defaults ?? {})
const categories = computed(() => data.value?.categories ?? [])

const totals = computed(() => {
  let orig = 0, depreciated = 0, net = 0
  for (const a of assets.value) {
    if (a.status === 'disposed') continue
    orig += a.origValue
    depreciated += a.depreciated
    net += a.netValue
  }
  return { orig, depreciated, net }
})

const assetOpen = ref(false)
const assetForm = ref(emptyAsset())

function emptyAsset() {
  return {
    id: 0, code: '', name: '', category: 'electronic', deptId: null,
    origYuan: '', salvagePercent: '5', usefulMonths: 36,
    startDate: '', expenseAccount: '', accumAccount: '',
    disposedDate: '', remark: '',
  }
}

function editAsset(a) {
  assetForm.value = a ? {
    id: a.id, code: a.code, name: a.name, category: a.category,
    deptId: a.deptId, origYuan: centsToYuanInput(a.origValue),
    salvagePercent: String(Number(a.salvagePpm) / 10000),
    usefulMonths: a.usefulMonths, startDate: a.startDate,
    expenseAccount: a.expenseAccount, accumAccount: a.accumAccount,
    disposedDate: a.disposedDate || '', remark: a.remark,
  } : { ...emptyAsset(), expenseAccount: defaults.value.depreciationExpense }
  assetOpen.value = true
}

async function saveAsset() {
  const f = assetForm.value
  if (!f.name.trim()) { notify('请填写资产名称', 'warn'); return }
  if (!f.startDate) { notify('请填写投入使用日期', 'warn'); return }
  busy.value = true
  const r = await api.saveAsset({
    id: f.id, code: f.code, name: f.name, category: f.category,
    deptId: f.deptId ?? null,
    origYuan: f.origYuan,
    // 界面填的是百分数（5 表示 5%），后端存百万分比
    salvagePpm: Math.round(Number(f.salvagePercent || 0) * 10000),
    usefulMonths: Number(f.usefulMonths) || 0,
    startDate: f.startDate,
    expenseAccount: f.expenseAccount,
    accumAccount: f.accumAccount,
    disposedDate: f.disposedDate,
    remark: f.remark,
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(f.id ? '固定资产已更新' : '固定资产已登记', 'success',
    '折旧从投入使用**次月**起提，当月不提 —— 这是准则规定的')
  assetOpen.value = false
  await load()
}

const disposeOpen = ref(false)
const disposeForm = ref({ id: 0, name: '', date: '', reason: '' })

function openDispose(a) {
  disposeForm.value = { id: a.id, name: a.name, date: '', reason: '' }
  disposeOpen.value = true
}

async function doDispose() {
  const f = disposeForm.value
  if (!f.date) { notify('请填写处置日期', 'warn'); return }
  busy.value = true
  const r = await api.disposeAsset({ id: f.id, date: f.date, reason: f.reason })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已处置「${f.name}」`, 'success',
    '处置**当月照提**折旧，次月起停；清理损益（卖了多少钱、花了多少清理费）请另做凭证')
  disposeOpen.value = false
  await load()
}

async function removeAsset(a) {
  if (!confirm(`确定删除「${a.name}」？此操作不可撤销。`)) return
  busy.value = true
  const r = await api.deleteAsset(a.id)
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已删除「${a.name}」`, 'success')
  await load()
}

// ---------------------------------------------------------------- 费用摊销

const amortOpen = ref(false)
const amortForm = ref(emptyAmort())

function emptyAmort() {
  return {
    id: 0, code: '', name: '', deptId: null, totalYuan: '',
    months: 12, startDate: '', expenseAccount: '', assetAccount: '', remark: '',
  }
}

function editAmort(m) {
  amortForm.value = m ? {
    id: m.id, code: m.code, name: m.name, deptId: m.deptId,
    totalYuan: centsToYuanInput(m.total), months: m.months, startDate: m.startDate,
    expenseAccount: m.expenseAccount, assetAccount: m.assetAccount,
    remark: m.remark,
  } : { ...emptyAmort(), expenseAccount: defaults.value.amortExpense }
  amortOpen.value = true
}

async function saveAmort() {
  const f = amortForm.value
  if (!f.name.trim()) { notify('请填写项目名称', 'warn'); return }
  if (!f.startDate) { notify('请填写开始摊销日期', 'warn'); return }
  busy.value = true
  const r = await api.saveAmortization({
    id: f.id, code: f.code, name: f.name, deptId: f.deptId ?? null,
    totalYuan: f.totalYuan, months: Number(f.months) || 0,
    startDate: f.startDate, expenseAccount: f.expenseAccount,
    assetAccount: f.assetAccount, remark: f.remark,
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(f.id ? '待摊项目已更新' : '待摊项目已登记', 'success',
    '受益期从开始当月就算，**当月即摊第一期**（与固定资产的次月不同）')
  amortOpen.value = false
  await load()
}

async function toggleAmort(m) {
  const voiding = m.status !== 'voided'
  if (voiding && !confirm(`作废「${m.name}」之后，新期间不再摊销。历史记录保留。确认？`)) return
  busy.value = true
  const r = await api.voidAmortization({ id: m.id, void: voiding })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(voiding ? `已作废「${m.name}」` : `已恢复「${m.name}」`, 'success')
  await load()
}

async function removeAmort(m) {
  if (!confirm(`确定删除「${m.name}」？此操作不可撤销。`)) return
  busy.value = true
  const r = await api.deleteAmortization(m.id)
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已删除「${m.name}」`, 'success')
  await load()
}

// ---------------------------------------------------------------- 计提

async function openPreview() {
  if (!accrualPeriod.value) { notify('请先选择要计提的期间', 'warn'); return }
  const [y, m] = accrualPeriod.value.split('-')
  busy.value = true
  const r = await api.previewAccrual({ year: Number(y), month: Number(m) })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  preview.value = r.data
  previewOpen.value = true
}

async function doAccrue() {
  if (!operator.value.trim()) { notify('请填写操作人（计提凭证的制单人）', 'warn'); return }
  const [y, m] = accrualPeriod.value.split('-')
  busy.value = true
  const r = await api.accrue({
    year: Number(y), month: Number(m), by: operator.value.trim(),
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  const n = [r.data.depreciationVoucherId, r.data.amortizationVoucherId].filter(Boolean).length
  notify(`已计提 ${r.data.period}：生成 ${n} 张凭证`, 'success', DRAFT_HINT)
  previewOpen.value = false
  await load()
}

const statusTone = (s) =>
  s === 'in_use' || s === 'active' ? 'profit'
    : s === 'disposed' || s === 'finished' ? 'muted' : 'warn'
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- 标签页 -->
    <div class="flex flex-wrap items-center gap-2">
      <Button
        v-for="t in [
          { v: 'assets', n: '固定资产', icon: Building2 },
          { v: 'amortizations', n: '费用摊销', icon: CalendarClock }]"
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
        <Button v-if="tab === 'assets'" size="sm" @click="editAsset(null)">
          <Plus /> 登记固定资产
        </Button>
        <Button v-else size="sm" @click="editAmort(null)">
          <Plus /> 新增待摊项目
        </Button>
      </div>
    </div>

    <p class="text-xs text-muted-foreground">
      折旧与摊销是<b>每月必须记、但没有任何业务发生</b>的两笔分录。
      漏提一个月不会报任何错 —— 账面上什么都不缺，只是费用少一块、利润多一块。
      所以这里的每一条都按期落记录（同一期不能提两次），
      并在计提预览里标出<b>上一期还有几条没提</b>。
    </p>

    <Spinner v-if="loading" />

    <!-- 固定资产 -->
    <template v-else-if="tab === 'assets'">
      <div class="grid grid-cols-3 gap-3">
        <Card><CardContent class="pt-5">
          <p class="text-xs text-muted-foreground">在用资产原值</p>
          <p class="num mt-1 text-lg font-semibold">{{ fmtMoney(totals.orig) }}</p>
        </CardContent></Card>
        <Card><CardContent class="pt-5">
          <p class="text-xs text-muted-foreground">累计折旧</p>
          <p class="num mt-1 text-lg font-semibold">{{ fmtMoney(totals.depreciated) }}</p>
        </CardContent></Card>
        <Card><CardContent class="pt-5">
          <p class="text-xs text-muted-foreground">账面净值</p>
          <p class="num mt-1 text-lg font-semibold">{{ fmtMoney(totals.net) }}</p>
        </CardContent></Card>
      </div>

      <Card>
        <CardHeader>
          <div class="flex items-center justify-between">
            <div>
              <CardTitle>固定资产卡片</CardTitle>
              <CardDescription>
                折旧用<b>平均年限法</b>：月折旧额 =（原值 − 预计净残值）÷ 预计使用月数。
                最后一期兜尾差，保证累计恰好等于应提总额。
                <b>当月增加的固定资产当月不提，从次月起提</b>；处置的当月照提、次月起停。
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent class="px-0">
          <EmptyState
            v-if="assets.length === 0"
            title="还没有固定资产"
            description="登记之后，每月点「计提本期折旧」就会自动生成折旧凭证，不用再手工算"
          >
            <Button size="sm" @click="editAsset(null)"><Plus /> 登记第一台</Button>
          </EmptyState>
          <table v-else class="w-full text-sm">
            <thead>
              <tr class="border-b bg-muted/40">
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">编号 / 名称</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">类别</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">部门</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">原值</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">月折旧</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">累计折旧</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">净值</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">折旧区间</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">状态</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="a in assets" :key="a.id" class="border-b last:border-0 hover:bg-accent/30">
                <td class="px-3 py-1.5">
                  <div class="font-medium">{{ a.name }}</div>
                  <div class="font-mono text-xs text-muted-foreground">{{ a.code || '—' }}</div>
                </td>
                <td class="px-3 py-1.5 text-xs text-muted-foreground">
                  {{ a.categoryLabel }}
                  <Badge v-if="a.lifeWarning" variant="warn" class="ml-1" :title="a.lifeWarning">短于税法年限</Badge>
                </td>
                <td class="px-3 py-1.5 text-xs text-muted-foreground">{{ a.deptName || '—' }}</td>
                <td class="num px-3 py-1.5">{{ fmtMoney(a.origValue) }}</td>
                <td class="num px-3 py-1.5">{{ fmtMoney(a.monthlyAmount) }}</td>
                <td class="num px-3 py-1.5 text-muted-foreground">{{ fmtMoney(a.depreciated) }}</td>
                <td class="num px-3 py-1.5">{{ fmtMoney(a.netValue) }}</td>
                <td class="px-3 py-1.5 font-mono text-xs text-muted-foreground">
                  {{ a.firstPeriod }} ~ {{ a.lastPeriod }}
                </td>
                <td class="px-3 py-1.5">
                  <Badge :variant="statusTone(a.status)">
                    {{ a.status === 'disposed' ? `${a.disposedDate} 处置` : a.statusLabel }}
                  </Badge>
                </td>
                <td class="px-3 py-1.5">
                  <div class="flex justify-end gap-0.5">
                    <Button variant="ghost" size="sm" @click="editAsset(a)"><Pencil /> 编辑</Button>
                    <Button v-if="a.status !== 'disposed'" variant="ghost" size="sm"
                            @click="openDispose(a)">
                      <Ban /> 处置
                    </Button>
                    <Button v-if="a.canDelete" variant="ghost" size="sm" class="text-destructive"
                            :disabled="busy" @click="removeAsset(a)"><Trash2 /></Button>
                    <Button v-else variant="ghost" size="sm" disabled :title="a.reason">
                      <Info class="size-3.5" />
                    </Button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </CardContent>
      </Card>
    </template>

    <!-- 费用摊销 -->
    <Card v-else>
      <CardHeader>
        <CardTitle>待摊项目</CardTitle>
        <CardDescription>
          长期待摊费用（如一年期房租、装修改造支出）在受益期内平均摊销：
          月摊销额 = 待摊总额 ÷ 摊销月数，最后一期兜尾差。
          <b>受益期从开始当月算，当月就摊第一期</b> —— 这一点与固定资产的「次月起提」不同。
        </CardDescription>
      </CardHeader>
      <CardContent class="px-0">
        <EmptyState
          v-if="amortizations.length === 0"
          title="还没有待摊项目"
          description="一笔先付掉、但要摊到好几个月里的支出，登记在这里"
        >
          <Button size="sm" @click="editAmort(null)"><Plus /> 新增一个</Button>
        </EmptyState>
        <table v-else class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40">
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">项目</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">部门</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">待摊总额</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">月摊销</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">已摊</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">剩余</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">摊销区间</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">状态</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in amortizations" :key="m.id" class="border-b last:border-0 hover:bg-accent/30">
              <td class="px-3 py-1.5">
                <div class="font-medium">{{ m.name }}</div>
                <div class="text-xs text-muted-foreground">
                  {{ m.expenseAccount }} ／ {{ m.assetAccount }} · {{ m.months }} 期
                </div>
              </td>
              <td class="px-3 py-1.5 text-xs text-muted-foreground">{{ m.deptName || '—' }}</td>
              <td class="num px-3 py-1.5">{{ fmtMoney(m.total) }}</td>
              <td class="num px-3 py-1.5">{{ fmtMoney(m.monthlyAmount) }}</td>
              <td class="num px-3 py-1.5 text-muted-foreground">{{ fmtMoney(m.amortized) }}</td>
              <td class="num px-3 py-1.5">{{ fmtMoney(m.remaining) }}</td>
              <td class="px-3 py-1.5 font-mono text-xs text-muted-foreground">
                {{ m.firstPeriod }} ~ {{ m.lastPeriod }}
              </td>
              <td class="px-3 py-1.5">
                <Badge :variant="statusTone(m.status)">{{ m.statusLabel }}</Badge>
              </td>
              <td class="px-3 py-1.5">
                <div class="flex justify-end gap-0.5">
                  <Button variant="ghost" size="sm" @click="editAmort(m)"><Pencil /> 编辑</Button>
                  <Button variant="ghost" size="sm" :disabled="busy" @click="toggleAmort(m)">
                    <ArchiveRestore class="size-3.5" /> {{ m.status === 'voided' ? '恢复' : '作废' }}
                  </Button>
                  <Button v-if="m.canDelete" variant="ghost" size="sm" class="text-destructive"
                          :disabled="busy" @click="removeAmort(m)"><Trash2 /></Button>
                  <Button v-else variant="ghost" size="sm" disabled :title="m.reason">
                    <Info class="size-3.5" />
                  </Button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </CardContent>
    </Card>

    <!-- 计提 -->
    <Card>
      <CardHeader>
        <CardTitle>计提本期折旧与摊销</CardTitle>
        <CardDescription>
          按卡片逐条算出本期该提多少，生成<b>草稿</b>凭证（折旧一张、摊销一张）。
          草稿不进总账，到「账期管理」结账时统一过账。
          同一期不能提两次 —— 重复提会让费用凭空多一块，而且不报错。
        </CardDescription>
      </CardHeader>
      <CardContent class="flex flex-wrap items-end gap-2">
        <div class="w-40">
          <Label>会计期间</Label>
          <select v-model="accrualPeriod"
                  class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
            <option v-for="p in periods" :key="p.label" :value="p.label">
              {{ p.label }}（{{ p.statusLabel }}）
            </option>
          </select>
        </div>
        <Button variant="outline" :disabled="busy" @click="openPreview">
          <Eye /> 预览本期计提
        </Button>
        <Button :disabled="busy || !accrualPeriod" @click="doAccrue">
          <Calculator /> 计提本期折旧与摊销
        </Button>
      </CardContent>
    </Card>

    <!-- 固定资产编辑 -->
    <Modal
      v-model:open="assetOpen"
      :title="assetForm.id ? '编辑固定资产' : '登记固定资产'"
      description="折旧从投入使用次月起提。科目留空则用默认：借 560205 管理费用—折旧费 / 贷 1602 累计折旧。"
      width="max-w-3xl"
    >
      <div class="grid grid-cols-3 gap-3">
        <div><Label>资产编号</Label><Input v-model="assetForm.code" class="mt-1.5" placeholder="可留空" /></div>
        <div class="col-span-2"><Label>资产名称 *</Label>
          <Input v-model="assetForm.name" class="mt-1.5" placeholder="笔记本电脑" /></div>

        <div class="col-span-2">
          <Label>类别（决定税法最低折旧年限）</Label>
          <select v-model="assetForm.category"
                  class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
            <option v-for="c in categories" :key="c.value" :value="c.value">
              {{ c.label }} —— 税法最低 {{ c.minYears }} 年
            </option>
          </select>
        </div>
        <div>
          <Label>使用部门</Label>
          <select v-model="assetForm.deptId"
                  class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
            <option :value="null">（不挂部门）</option>
            <option v-for="d in departments" :key="d.id" :value="d.id">
              {{ d.fullName || d.name }}
            </option>
          </select>
          <p class="mt-1 text-xs text-muted-foreground">
            折旧费用科目按部门辅助核算，所以这里通常必填
          </p>
        </div>

        <div><Label>原值 *</Label><Input v-model="assetForm.origYuan" class="mt-1.5 num" placeholder="12000.00" /></div>
        <div><Label>预计净残值率 %</Label>
          <Input v-model="assetForm.salvagePercent" class="mt-1.5 num" placeholder="5" /></div>
        <div><Label>预计使用月数 *</Label>
          <Input v-model="assetForm.usefulMonths" class="mt-1.5 num" placeholder="36" /></div>

        <div><Label>投入使用日期 *</Label>
          <Input v-model="assetForm.startDate" class="mt-1.5" placeholder="2025-01-10" /></div>
        <div><Label>折旧费用科目</Label>
          <Input v-model="assetForm.expenseAccount" class="mt-1.5 font-mono"
                 :placeholder="defaults.depreciationExpense" /></div>
        <div><Label>累计折旧科目</Label>
          <Input v-model="assetForm.accumAccount" class="mt-1.5 font-mono"
                 :placeholder="defaults.accumDepreciation" /></div>

        <div class="col-span-3"><Label>备注</Label><Input v-model="assetForm.remark" class="mt-1.5" /></div>

        <p class="col-span-3 text-xs text-muted-foreground">
          折旧方法固定为<b>平均年限法</b>（直线法）：月折旧额 =（原值 − 原值 × 净残值率）÷ 使用月数，
          最后一期把四舍五入的尾差补齐，保证累计恰好等于应提总额。
        </p>
      </div>
      <template #footer>
        <Button variant="ghost" @click="assetOpen = false">取消</Button>
        <Button :disabled="busy" @click="saveAsset"><Pencil /> 保存</Button>
      </template>
    </Modal>

    <!-- 处置 -->
    <Modal
      v-model:open="disposeOpen"
      :title="`处置「${disposeForm.name}」`"
      description="处置只标状态：处置当月照提折旧，次月起停。清理损益（卖价、清理费用）请另做凭证 —— 那要看实际收付了多少。"
      width="max-w-lg"
    >
      <div class="flex flex-col gap-3">
        <div><Label>处置日期 *</Label>
          <Input v-model="disposeForm.date" class="mt-1.5" placeholder="2026-03-31" /></div>
        <div><Label>处置原因</Label>
          <Input v-model="disposeForm.reason" class="mt-1.5" placeholder="报废 / 出售 / 毁损，会写进操作日志" /></div>
        <p class="text-xs text-muted-foreground">
          处置当月仍会出现在计提里（准则：当月减少的固定资产当月照提），
          下个月起自动跳过。
        </p>
      </div>
      <template #footer>
        <Button variant="ghost" @click="disposeOpen = false">取消</Button>
        <Button :disabled="busy" @click="doDispose"><Ban /> 确认处置</Button>
      </template>
    </Modal>

    <!-- 待摊项目编辑 -->
    <Modal
      v-model:open="amortOpen"
      :title="amortForm.id ? '编辑待摊项目' : '新增待摊项目'"
      description="长期待摊费用在受益期内平均摊销。默认：借 费用科目 / 贷 1801 长期待摊费用。"
      width="max-w-3xl"
    >
      <div class="grid grid-cols-2 gap-3">
        <div class="col-span-2"><Label>项目名称 *</Label>
          <Input v-model="amortForm.name" class="mt-1.5" placeholder="一年期房租 / 办公室装修" /></div>
        <div><Label>待摊总额 *</Label>
          <Input v-model="amortForm.totalYuan" class="mt-1.5 num" placeholder="60000.00" /></div>
        <div><Label>摊销月数 *</Label>
          <Input v-model="amortForm.months" class="mt-1.5 num" placeholder="12" /></div>
        <div><Label>开始摊销日期 *</Label>
          <Input v-model="amortForm.startDate" class="mt-1.5" placeholder="2025-01-01" /></div>
        <div>
          <Label>受益部门</Label>
          <select v-model="amortForm.deptId"
                  class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
            <option :value="null">（不挂部门）</option>
            <option v-for="d in departments" :key="d.id" :value="d.id">
              {{ d.fullName || d.name }}
            </option>
          </select>
        </div>
        <div><Label>费用科目</Label>
          <Input v-model="amortForm.expenseAccount" class="mt-1.5 font-mono"
                 :placeholder="defaults.amortExpense" /></div>
        <div><Label>待摊科目</Label>
          <Input v-model="amortForm.assetAccount" class="mt-1.5 font-mono"
                 :placeholder="defaults.amortAsset" /></div>
        <div class="col-span-2"><Label>备注</Label><Input v-model="amortForm.remark" class="mt-1.5" /></div>
        <p class="col-span-2 text-xs text-muted-foreground">
          <b>当月就摊第一期</b>：受益期从开始当月起算。
          这一点与固定资产的「次月起提」不同，不是笔误 ——
          固定资产是「当月增加当月不提」（准则第十四条），
          待摊费用是已发生的支出在受益期内摊销。
        </p>
      </div>
      <template #footer>
        <Button variant="ghost" @click="amortOpen = false">取消</Button>
        <Button :disabled="busy" @click="saveAmort"><Pencil /> 保存</Button>
      </template>
    </Modal>

    <!-- 计提预览 -->
    <Modal
      v-model:open="previewOpen"
      :title="`计提预览 ${preview?.period ?? ''}`"
      description="下面是本期每一条算出多少。金额为 0 的会说明为什么 —— 折旧算成 0 却不告诉你原因，比算错还难查。"
      width="max-w-4xl"
    >
      <div v-if="preview" class="flex flex-col gap-4">
        <div
          v-if="preview.previousMissing > 0"
          class="rounded-lg border border-[var(--warn)]/40 bg-[var(--warn)]/5 p-3 text-sm"
        >
          <p class="font-medium">
            上一期（{{ preview.previousPeriod }}）还有 {{ preview.previousMissing }} 条没有计提
          </p>
          <p class="mt-1 text-xs text-muted-foreground">
            漏提<b>不会报任何错</b>：账面上什么都不缺，只是费用少一块、利润多一块。
            如果确实要补，先把期间切到 {{ preview.previousPeriod }} 提一次，再提本期。
          </p>
        </div>
        <div
          v-if="preview.depreciationDone || preview.amortizationDone"
          class="rounded-lg border p-3 text-sm"
        >
          <p class="font-medium">
            本期{{ preview.depreciationDone ? '折旧' : '' }}{{ preview.depreciationDone && preview.amortizationDone ? '与' : '' }}{{ preview.amortizationDone ? '摊销' : '' }}已经计提过
          </p>
          <p class="mt-1 text-xs text-muted-foreground">
            同一期不能提两次。要改金额请先把已生成的凭证删掉（草稿才能删），或先反结账。
          </p>
        </div>

        <table class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40">
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">类别</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">对象</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">分录</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">辅助核算</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">本期金额</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(r, i) in preview.rows" :key="i" class="border-b last:border-0">
              <td class="px-3 py-1.5 text-xs text-muted-foreground">{{ r.kindLabel }}</td>
              <td class="px-3 py-1.5">{{ r.name }}</td>
              <td class="px-3 py-1.5 font-mono text-xs">
                借 {{ r.debitAccount }} / 贷 {{ r.creditAccount }}
              </td>
              <td class="px-3 py-1.5 text-xs text-muted-foreground">{{ r.auxDesc || '—' }}</td>
              <td class="num px-3 py-1.5 text-right">
                <span v-if="r.amount">{{ fmtMoney(r.amount) }}</span>
                <span v-else class="text-xs text-muted-foreground">{{ r.reason || '—' }}</span>
              </td>
            </tr>
          </tbody>
          <tfoot>
            <tr class="border-t-2 bg-muted/40 font-medium">
              <td class="px-3 py-2" colspan="4">合计（折旧 {{ fmtMoney(preview.depreciationTotal) }} ＋ 摊销 {{ fmtMoney(preview.amortizationTotal) }}）</td>
              <td class="num px-3 py-2 text-right">
                {{ fmtMoney(preview.depreciationTotal + preview.amortizationTotal) }}
              </td>
            </tr>
          </tfoot>
        </table>

        <p class="text-xs text-muted-foreground">
          点确认后生成<b>草稿</b>凭证（折旧一张、摊销一张），到账期结算时统一过账。
        </p>
      </div>
      <template #footer>
        <Button variant="ghost" @click="previewOpen = false">取消</Button>
        <Button :disabled="busy" @click="doAccrue"><Calculator /> 确认计提</Button>
      </template>
    </Modal>
  </div>
</template>
