<script setup>
import { computed, onMounted, ref } from 'vue'
import {
  Upload, Wand2, CheckCircle2, Ban, RefreshCw, Landmark, Info, Plus,
  Pencil, Search,
} from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
import { bookkeeper, loadBookkeeper, rememberBookkeeper } from '@/lib/operator'
import { fmtMoney } from '@/lib/format'
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

const tab = ref('flows') // flows | rules

const flows = ref([])
const rules = ref([])
const stats = ref(null)
const loading = ref(false)
const busy = ref(false)
// 记账人来自本机设置（见 lib/operator.js）：全程序一份，不再各页各存一份
const operator = bookkeeper

const filter = ref({ status: '', counterparty: '', from: '', to: '' })

// 人工指定对方科目。三层匹配再全也总会有匹配不上的流水 ——
// 一次性的业务、新出现的对手方、摘要写得莫名其妙的支出。
// 没有这个入口，这些流水就只能在界面上「忽略」，而忽略意味着
// 这笔钱永远进不了账，银行余额与账面永远对不上。
const assignOpen = ref(false)
const assignFlow = ref(null)
const assignForm = ref({
  counterAccountCode: '', contactId: null, deptId: null,
  employeeId: null, projectId: null, memo: '',
})
const assignQuery = ref('')
const accounts = ref([])

// 导入
const importOpen = ref(false)
const importText = ref('')
const importAccount = ref('1002')
const importFileName = ref('')

// 规则
const ruleOpen = ref(false)
const ruleForm = ref({
  name: '', pattern: '', counter: '', dept: '', contact: '', direction: '',
})

async function load() {
  loading.value = true
  const [f, s, r, a] = await Promise.all([
    api.bankFlows(filter.value),
    api.bankStats(),
    api.bankRules(),
    api.accountOptions(),
  ])
  loading.value = false
  if (f.ok) flows.value = f.data ?? []
  else notify(f.fault.message, 'error', f.fault.detail)
  if (s.ok) stats.value = s.data
  if (r.ok) rules.value = r.data ?? []
  if (a.ok) accounts.value = a.data ?? []
}

// 科目选择器的过滤：编码、全名、名称任一命中即可
const filteredAccounts = computed(() => {
  const q = assignQuery.value.trim().toLowerCase()
  const all = accounts.value
  if (!q) return all.slice(0, 60)
  return all.filter((a) =>
    a.code.toLowerCase().includes(q) ||
    (a.fullName ?? a.name ?? '').toLowerCase().includes(q)).slice(0, 60)
})

// 当前选中的科目要求哪些辅助核算 —— 决定对话框里要显示哪些输入框
const needAux = computed(() => {
  const a = accounts.value.find((x) => x.code === assignForm.value.counterAccountCode)
  return a?.auxTypes ?? []
})

function openAssign(f) {
  assignFlow.value = f
  assignQuery.value = ''
  assignForm.value = {
    counterAccountCode: f.counterAccount || '',
    contactId: null, deptId: null, employeeId: null, projectId: null,
    // 摘要默认用流水摘要，用户可以改 —— 凭证行摘要不能为空
    memo: f.summary || f.counterpartyName || '',
  }
  assignOpen.value = true
}

function pickAssignAccount(a) {
  assignForm.value.counterAccountCode = a.code
  assignQuery.value = ''
}

// ---------------------------------------------------------------------------
// 行内改对方科目
// ---------------------------------------------------------------------------
//
// 银行流水一天几十条，绝大多数是一眼就能定的（水电费、房租、货款）。
// 每条都开一次弹窗、填一次表单、点一次保存，几十条下来就是一下午。
// 所以在表格里直接改。
//
// ★ 但有一个绕不开的约束：科目要求辅助核算时，只给科目是不够的 ——
// 后端会在生成分录那一步拒绝（「缺少必需的辅助核算：部门」）。
// 与其让用户在行内选完再看一个报错，不如当场把弹窗打开，
// 把科目预填进去，缺的维度在那里补齐。行内编辑省掉的是
// 「本来就不需要辅助核算」的那些流水 —— 而那正是大多数。
const editingFlowId = ref(null)

// 不需要辅助核算的科目才允许行内直接保存
const inlineAccounts = computed(() =>
  accounts.value.filter((a) => (a.auxTypes ?? []).length === 0))

async function startInlineEdit(f) {
  if (f.status === 'posted') return
  editingFlowId.value = f.id
}

function cancelInlineEdit() { editingFlowId.value = null }

async function inlineSetAccount(f, code) {
  editingFlowId.value = null
  if (!code || code === f.counterAccount) return

  const acc = accounts.value.find((a) => a.code === code)

  // 需要辅助核算 → 交给弹窗，科目预填好，用户只需补缺的那几维
  if ((acc?.auxTypes ?? []).length > 0) {
    openAssign(f)
    assignForm.value.counterAccountCode = code
    return
  }

  busy.value = true
  const r = await api.setBankSuggestion({
    flowId: f.id,
    counterAccountCode: code,
    contactId: null, deptId: null, employeeId: null, projectId: null,
    memo: f.summary || f.counterpartyName || '',
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已指定对方科目 ${r.data.counterAccount}`, 'success')
  await load()
}

async function saveAssign() {
  if (!assignForm.value.counterAccountCode) {
    notify('请选择对方科目', 'warn')
    return
  }
  busy.value = true
  const r = await api.setBankSuggestion({
    flowId: assignFlow.value.id,
    ...assignForm.value,
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已指定对方科目 ${r.data.counterAccount}，可生成凭证了`, 'success')
  assignOpen.value = false
  await load()
}
onMounted(async () => {
  // 记账人是本机设置（全程序一份），与页面数据一起加载
  await Promise.all([load(), loadBookkeeper()])
})

const totals = computed(() => {
  let inAmt = 0, outAmt = 0
  for (const f of flows.value) {
    if (f.direction === 'in') inAmt += f.amount
    else outAmt += f.amount
  }
  return { inAmt, outAmt }
})

// ---------------------------------------------------------------- 导入

function pickFile() {
  const el = document.createElement('input')
  el.type = 'file'
  el.accept = '.csv,.txt'
  el.onchange = async () => {
    const file = el.files?.[0]
    if (!file) return
    importFileName.value = file.name
    // 银行对账单多为 GB18030；用 FileReader 读成字节再转 base64，
    // 让 Go 侧的编码嗅探去判断 —— 前端用 text() 会按 UTF-8 解码，
    // 中文会全部变成乱码，而乱码进到解析器里只会报「列名识别不出来」。
    const buf = await file.arrayBuffer()
    importText.value = b64(buf)
  }
  el.click()
}

function b64(buf) {
  const bytes = new Uint8Array(buf)
  let bin = ''
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) {
    bin += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk))
  }
  return btoa(bin)
}

async function doImport() {
  if (!importText.value) { notify('请先选择对账单文件', 'warn'); return }
  busy.value = true
  const r = await api.importBankFlows({
    accountCode: importAccount.value,
    fileName: importFileName.value,
    dataBase64: importText.value,
    importedBy: operator.value || '操作人',
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  const d = r.data
  notify(`导入完成：新增 ${d.inserted} 条` +
    (d.duplicated ? `，跳过 ${d.duplicated} 条重复` : ''), 'success')
  if (d.parseErrors?.length) {
    notify(`有 ${d.parseErrors.length} 行无法解析，已跳过`, 'warn',
      d.parseErrors.slice(0, 8).join('\n'))
  }
  importOpen.value = false
  importText.value = ''
  await load()
}

// ---------------------------------------------------------------- 匹配

async function doMatch() {
  busy.value = true
  const r = await api.matchBankFlows()
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`匹配了 ${r.data.total} 条，命中 ${r.data.matched} 条` +
    (r.data.unmatched?.length ? `，${r.data.unmatched.length} 条需人工处理` : ''), 'success')
  await load()
}

async function postAll() {
  if (!operator.value.trim()) { notify('请填写记账人', 'warn'); return }
  const ids = flows.value.filter((f) => f.status === 'matched').map((f) => f.id)
  if (ids.length === 0) { notify('没有待生成凭证的流水', 'warn'); return }
  if (!confirm(`将为 ${ids.length} 条流水生成凭证，确认？`)) return
  busy.value = true
  const r = await api.postBankFlows({ ids, postingBy: operator.value.trim() })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已生成 ${r.data.created} 张凭证`, 'success')
  if (r.data.failures?.length) {
    notify(`有 ${r.data.failures.length} 条失败`, 'warn',
      r.data.failures.slice(0, 8).join('\n'))
  }
  await load()
}

async function ignoreFlow(f) {
  const r = await api.ignoreBankFlow(f.id, '人工忽略')
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  await load()
}

// ---------------------------------------------------------------- 规则

async function saveRule() {
  if (!ruleForm.value.name || !ruleForm.value.pattern || !ruleForm.value.counter) {
    notify('规则名、匹配关键词、对方科目都要填', 'warn')
    return
  }
  busy.value = true
  const r = await api.saveBankRule({
    name: ruleForm.value.name,
    pattern: ruleForm.value.pattern,
    counterAccountCode: ruleForm.value.counter,
    deptId: ruleForm.value.dept ? Number(ruleForm.value.dept) : null,
    contactId: ruleForm.value.contact ? Number(ruleForm.value.contact) : null,
    direction: ruleForm.value.direction,
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('规则已保存', 'success')
  ruleOpen.value = false
  ruleForm.value = { name: '', pattern: '', counter: '', dept: '', contact: '', direction: '' }
  await load()
}

const statusTone = (s) =>
  s === 'posted' ? 'profit' : s === 'matched' ? 'default' : s === 'ignored' ? 'muted' : 'warn'
</script>

<template>
  <div class="flex flex-col gap-4">
    <div class="flex flex-wrap items-center gap-2">
      <Button
        v-for="t in [{ v: 'flows', n: '流水' }, { v: 'rules', n: '匹配规则' }]"
        :key="t.v"
        :variant="tab === t.v ? 'default' : 'outline'"
        size="sm"
        @click="tab = t.v"
      >{{ t.n }}</Button>

      <div class="ml-auto flex items-center gap-2">
        <Input v-model="operator"
                 @change="rememberBookkeeper(operator)" class="h-8 w-32" placeholder="操作人" />
        <template v-if="tab === 'flows'">
          <Button variant="outline" size="sm" :disabled="loading" @click="load">
            <RefreshCw :class="loading ? 'animate-spin' : ''" /> 刷新
          </Button>
          <Button variant="outline" size="sm" :disabled="busy" @click="doMatch">
            <Wand2 /> 自动匹配
          </Button>
          <Button
            size="sm" :disabled="busy || !stats?.matched"
            :title="stats?.matched ? `把 ${stats.matched} 条已匹配的流水生成凭证` : '没有待生成凭证的流水'"
            @click="postAll"
          >
            <CheckCircle2 /> 生成凭证{{ stats?.matched ? `（${stats.matched}）` : '' }}
          </Button>
          <Button size="sm" @click="importOpen = true"><Upload /> 导入对账单</Button>
        </template>
        <Button v-else size="sm" @click="ruleOpen = true"><Plus /> 新增规则</Button>
      </div>
    </div>

    <Spinner v-if="loading" />

    <!-- 流水 -->
    <template v-else-if="tab === 'flows'">
      <div class="grid grid-cols-5 gap-4">
        <Card><CardContent class="pt-5">
          <p class="text-xs text-muted-foreground">待匹配</p>
          <p class="num mt-1 text-xl font-semibold">{{ stats?.imported ?? 0 }}</p>
        </CardContent></Card>
        <Card><CardContent class="pt-5">
          <p class="text-xs text-muted-foreground">已匹配</p>
          <p class="num mt-1 text-xl font-semibold">{{ stats?.matched ?? 0 }}</p>
        </CardContent></Card>
        <Card><CardContent class="pt-5">
          <p class="text-xs text-muted-foreground">已生成凭证</p>
          <p class="num mt-1 text-xl font-semibold text-[var(--profit)]">{{ stats?.posted ?? 0 }}</p>
        </CardContent></Card>
        <Card><CardContent class="pt-5">
          <p class="text-xs text-muted-foreground">收入合计</p>
          <p class="num mt-1 text-xl font-semibold text-[var(--debit)]">{{ fmtMoney(totals.inAmt) }}</p>
        </CardContent></Card>
        <Card><CardContent class="pt-5">
          <p class="text-xs text-muted-foreground">支出合计</p>
          <p class="num mt-1 text-xl font-semibold text-[var(--credit)]">{{ fmtMoney(totals.outAmt) }}</p>
        </CardContent></Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>银行流水</CardTitle>
          <CardDescription>
            匹配是三层兜底：<b>你自己定的规则</b> → <b>历史同类凭证的记法</b> → AI 推理。
            匹配结果只是提议，确认后才生成凭证。
          </CardDescription>
        </CardHeader>
        <CardContent class="px-0">
          <EmptyState
            v-if="flows.length === 0"
            title="还没有流水"
            description="导入银行导出的对账单 CSV。编码与列映射会自动识别，不用手工配置。"
          >
            <Button size="sm" @click="importOpen = true"><Upload /> 导入对账单</Button>
          </EmptyState>
          <div v-else class="overflow-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b bg-muted/40">
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">日期</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">方向</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">金额</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">对方</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">摘要</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">状态</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">
                  建议对方科目
                  <span class="font-normal">（点一下即可改）</span>
                </th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">依据</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">操作</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="f in flows" :key="f.id" class="border-b last:border-0 hover:bg-accent/30">
                  <td class="px-3 py-1.5 whitespace-nowrap text-xs">{{ f.date }}</td>
                  <td class="px-3 py-1.5">
                    <Badge :variant="f.direction === 'in' ? 'debit' : 'credit'">{{ f.directionLabel }}</Badge>
                  </td>
                  <td class="num px-3 py-1.5 font-medium">{{ fmtMoney(f.amount) }}</td>
                  <td class="max-w-[14rem] truncate px-3 py-1.5">{{ f.counterpartyName }}</td>
                  <td class="max-w-[18rem] truncate px-3 py-1.5 text-muted-foreground">{{ f.summary }}</td>
                  <td class="px-3 py-1.5"><Badge :variant="statusTone(f.status)">{{ f.statusLabel }}</Badge></td>
                  <!-- 对方科目：点一下就地改，省掉每条流水开一次弹窗 -->
                  <td class="px-3 py-1.5">
                    <select
                      v-if="editingFlowId === f.id"
                      class="h-7 w-44 rounded border border-input bg-transparent px-1 font-mono text-xs"
                      autofocus
                      @change="inlineSetAccount(f, $event.target.value)"
                      @blur="cancelInlineEdit"
                    >
                      <option value="">— 选择对方科目 —</option>
                      <option v-for="a in inlineAccounts" :key="a.code" :value="a.code">
                        {{ a.code }} {{ a.fullName || a.name }}
                      </option>
                    </select>
                    <button
                      v-else-if="f.status !== 'posted'"
                      class="rounded px-1 font-mono text-xs hover:bg-accent"
                      :title="f.counterAccount ? '点击修改对方科目' : '点击指定对方科目'"
                      @click="startInlineEdit(f)"
                    >{{ f.counterAccount || '＋ 指定' }}</button>
                    <span v-else class="px-1 font-mono text-xs">{{ f.counterAccount || '—' }}</span>
                  </td>
                  <td class="px-3 py-1.5 text-xs text-muted-foreground">{{ f.matchLayerLabel || '—' }}</td>
                  <td class="px-3 py-1.5">
                    <div class="flex justify-end gap-1">
                      <Button
                        v-if="f.status !== 'posted'"
                        variant="ghost" size="sm" title="指定对方科目"
                        @click="openAssign(f)"
                      ><Pencil /></Button>
                      <Button
                        v-if="f.status === 'imported'"
                        variant="ghost" size="sm" title="忽略这条流水"
                        @click="ignoreFlow(f)"
                      ><Ban /></Button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </CardContent>
      </Card>
    </template>

    <!-- 规则 -->
    <Card v-else>
      <CardHeader>
        <CardTitle>匹配规则</CardTitle>
        <CardDescription>
          规则是三层匹配里最可靠的一层：它由你定义，不受历史数据与模型影响。
          建议把每月重复出现的固定支出（房租、工资、水电、社保）都配上规则。
        </CardDescription>
      </CardHeader>
      <CardContent class="px-0">
        <EmptyState
          v-if="rules.length === 0"
          title="还没有匹配规则"
          description="例：摘要含「房租」→ 对方科目 560210 管理费用—租赁费，部门 1"
        >
          <Button size="sm" @click="ruleOpen = true"><Plus /> 新增规则</Button>
        </EmptyState>
        <table v-else class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40">
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">规则名</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">匹配关键词</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">对方科目</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">辅助核算</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">命中次数</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">启用</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rules" :key="r.id" class="border-b last:border-0">
              <td class="px-3 py-1.5">{{ r.name }}</td>
              <td class="px-3 py-1.5 font-mono text-xs">{{ r.pattern }}</td>
              <td class="px-3 py-1.5 font-mono text-xs">{{ r.counterAccountCode }}</td>
              <td class="px-3 py-1.5 text-xs text-muted-foreground">
                {{ [r.contactId && `往来#${r.contactId}`, r.deptId && `部门#${r.deptId}`,
                    r.employeeId && `员工#${r.employeeId}`].filter(Boolean).join(' ') || '—' }}
              </td>
              <td class="num px-3 py-1.5">{{ r.hitCount }}</td>
              <td class="px-3 py-1.5">
                <Badge :variant="r.enabled ? 'profit' : 'muted'">{{ r.enabled ? '启用' : '停用' }}</Badge>
              </td>
            </tr>
          </tbody>
        </table>
      </CardContent>
    </Card>

    <!-- 导入 -->
    <Modal
      v-model:open="importOpen"
      title="导入银行对账单"
      description="支持 CSV。编码（UTF-8 / GB18030 / UTF-16）与列映射都会自动识别。"
      width="max-w-xl"
    >
      <div class="flex flex-col gap-4">
        <div>
          <Label>银行科目</Label>
          <Input v-model="importAccount" class="mt-1.5" placeholder="1002" />
        </div>
        <div>
          <Label>对账单文件</Label>
          <div class="mt-1.5 flex items-center gap-2">
            <Button variant="outline" @click="pickFile"><Upload /> 选择文件</Button>
            <span class="text-sm text-muted-foreground">{{ importFileName || '未选择' }}</span>
          </div>
        </div>
        <div class="flex items-start gap-2 rounded-md border bg-muted/30 p-3 text-xs">
          <Info class="mt-0.5 size-3.5 shrink-0" />
          <span>
            同一份对账单重复导入会被自动去重（按流水号或
            「日期+方向+金额+余额+对方+摘要」的指纹），不会让账目凭空多出一倍。
          </span>
        </div>
      </div>
      <template #footer>
        <Button variant="ghost" @click="importOpen = false">取消</Button>
        <Button :disabled="busy || !importText" @click="doImport"><Upload /> 导入</Button>
      </template>
    </Modal>

    <!-- 人工指定对方科目 -->
    <Modal
      v-model:open="assignOpen"
      :title="assignFlow ? `指定对方科目` : ''"
      :description="assignFlow
        ? `${assignFlow.date}　${assignFlow.directionLabel}　${fmtMoney(assignFlow.amount)}　${assignFlow.counterpartyName || assignFlow.summary}`
        : ''"
      width="max-w-2xl"
    >
      <div v-if="assignFlow" class="flex flex-col gap-4">
        <div class="relative">
          <Search class="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            v-model="assignQuery"
            class="pl-8"
            placeholder="输入科目编码或名称筛选，如 5602 / 办公费"
          />
        </div>

        <div class="max-h-64 overflow-auto rounded-lg border">
          <button
            v-for="a in filteredAccounts"
            :key="a.code"
            class="flex w-full items-center gap-2 border-b px-3 py-1.5 text-left text-sm last:border-0"
            :class="assignForm.counterAccountCode === a.code
              ? 'bg-accent font-medium' : 'hover:bg-accent/50'"
            @click="pickAssignAccount(a)"
          >
            <span class="w-16 shrink-0 font-mono text-xs text-muted-foreground">{{ a.code }}</span>
            <span class="flex-1 truncate">{{ a.fullName }}</span>
            <Badge variant="muted">{{ a.direction }}</Badge>
            <Badge v-if="a.auxTypes?.length" variant="warn">{{ a.auxTypes.join('、') }}</Badge>
          </button>
          <p v-if="filteredAccounts.length === 0" class="p-6 text-center text-sm text-muted-foreground">
            没有匹配的科目
          </p>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div class="col-span-2">
            <Label>凭证行摘要</Label>
            <Input v-model="assignForm.memo" class="mt-1.5" placeholder="不能为空" />
          </div>
          <div v-if="needAux.includes('客户') || needAux.includes('供应商') || needAux.includes('股东')"
               class="col-span-2">
            <Label>往来单位（科目要求）</Label>
            <Input v-model.number="assignForm.contactId" class="mt-1.5"
                   placeholder="填往来单位 id，可在「报销 / 设置」里的往来档案查看" />
          </div>
          <div v-if="needAux.includes('部门')">
            <Label>部门 id（科目要求）</Label>
            <Input v-model.number="assignForm.deptId" class="mt-1.5" placeholder="如 1" />
          </div>
          <div v-if="needAux.includes('员工')">
            <Label>员工 id（科目要求）</Label>
            <Input v-model.number="assignForm.employeeId" class="mt-1.5" />
          </div>
        </div>

        <div v-if="needAux.length === 0 && assignForm.counterAccountCode"
             class="rounded-md border bg-muted/30 p-3 text-xs text-muted-foreground">
          该科目不需要辅助核算，直接保存即可。
        </div>
        <div v-if="needAux.length > 0"
             class="flex items-start gap-2 rounded-md border border-[var(--warn)]/40 bg-[var(--warn)]/10 p-3 text-xs">
          <Info class="mt-0.5 size-3.5 shrink-0" />
          <span>
            该科目要求「{{ needAux.join('、') }}」辅助核算，必须填写 ——
            否则生成凭证时会被拦下。
          </span>
        </div>
      </div>

      <template #footer>
        <Button variant="ghost" @click="assignOpen = false">取消</Button>
        <Button :disabled="busy || !assignForm.counterAccountCode" @click="saveAssign">
          <CheckCircle2 /> 保存并标记为已匹配
        </Button>
      </template>
    </Modal>

    <!-- 新增规则 -->
    <Modal
      v-model:open="ruleOpen"
      title="新增匹配规则"
      description="摘 要或对方户名包含关键词时命中，自动填入对方科目与辅助核算。"
      width="max-w-2xl"
    >
      <div class="grid grid-cols-2 gap-3">
        <div><Label>规则名 *</Label><Input v-model="ruleForm.name" class="mt-1.5" placeholder="房租" /></div>
        <div>
          <Label>匹配关键词 *</Label>
          <Input v-model="ruleForm.pattern" class="mt-1.5" placeholder="房租" />
        </div>
        <div>
          <Label>对方科目编码 *</Label>
          <Input v-model="ruleForm.counter" class="mt-1.5" placeholder="560210" />
        </div>
        <div>
          <Label>部门 id</Label>
          <Input v-model="ruleForm.dept" class="mt-1.5" placeholder="科目要求部门时必填" />
        </div>
        <div>
          <Label>往来单位 id</Label>
          <Input v-model="ruleForm.contact" class="mt-1.5" placeholder="科目要求客户/供应商时必填" />
        </div>
        <div>
          <Label>限定方向</Label>
          <select
            v-model="ruleForm.direction"
            class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm"
          >
            <option value="">不限</option>
            <option value="in">仅收入</option>
            <option value="out">仅支出</option>
          </select>
        </div>
      </div>
      <div class="mt-3 flex items-start gap-2 rounded-md border border-[var(--warn)]/40 bg-[var(--warn)]/10 p-3 text-xs">
        <Info class="mt-0.5 size-3.5 shrink-0" />
        <span>
          对方科目如果要求辅助核算（如管理费用要求「部门」），
          这里必须一起填 —— 否则规则会匹配成功，但生成凭证时被拦下，
          你会看到「命中 2 条却一条也记不上」。
        </span>
      </div>
      <template #footer>
        <Button variant="ghost" @click="ruleOpen = false">取消</Button>
        <Button :disabled="busy" @click="saveRule">保存规则</Button>
      </template>
    </Modal>
  </div>
</template>
