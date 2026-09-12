<script setup>
import { computed, onMounted, ref } from 'vue'
import {
  Plus, Save, Trash2, CheckCircle2, XCircle, FileCheck2, RefreshCw, Info,
} from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
import { bookkeeper, loadBookkeeper, rememberBookkeeper } from '@/lib/operator'
import { fmtMoney, parseYuanToCents } from '@/lib/format'
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

const rows = ref([])
const employees = ref([])
const departments = ref([])
const categories = ref([])
const loading = ref(false)
const busy = ref(false)
// 记账人来自本机设置（见 lib/operator.js）：全程序一份，不再各页各存一份
const operator = bookkeeper
const filter = ref({ status: '' })

const formOpen = ref(false)
const form = ref(emptyClaim())

const detail = ref(null)
const detailOpen = ref(false)
const rejectOpen = ref(false)
const rejectReason = ref('')

function emptyItem() {
  return {
    category: 'transport', occurDate: '', summary: '',
    amountYuan: '', taxYuan: '', accountCode: '', deptId: null,
  }
}

function emptyClaim() {
  return {
    id: 0, claimantEmployeeId: 0, deptId: null,
    applyDate: '', tripStart: '', tripEnd: '',
    destination: '', reason: '', payFromAccount: '1002', remark: '',
    items: [emptyItem()],
  }
}

async function load() {
  loading.value = true
  const [l, e, d, c, t] = await Promise.all([
    api.claims(filter.value), api.employees(true),
    api.departments(), api.claimCategories(), api.today(),
  ])
  loading.value = false
  if (l.ok) rows.value = l.data ?? []
  else notify(l.fault.message, 'error', l.fault.detail)
  if (e.ok) employees.value = e.data ?? []
  if (d.ok) departments.value = d.data ?? []
  if (c.ok) categories.value = c.data ?? []
  if (t.ok && !form.value.applyDate) form.value.applyDate = t.data
}
onMounted(async () => {
  // 记账人是本机设置（全程序一份），与页面数据一起加载
  await Promise.all([load(), loadBookkeeper()])
})

const total = computed(() => {
  let amt = 0, tax = 0
  for (const it of form.value.items) {
    amt += parseYuanToCents(it.amountYuan) ?? 0
    tax += parseYuanToCents(it.taxYuan) ?? 0
  }
  return { amt, tax, net: amt - tax }
})

function openNew() {
  form.value = emptyClaim()
  form.value.applyDate = new Date().toISOString().slice(0, 10)
  formOpen.value = true
}

function addItem() { form.value.items.push(emptyItem()) }
function removeItem(i) {
  if (form.value.items.length <= 1) { notify('至少保留一条明细', 'warn'); return }
  form.value.items.splice(i, 1)
}

// 选了类别自动带出默认科目 —— 会计科目是最容易填错也最难发现的一栏
function onCategoryChange(i) {
  const c = categories.value.find((x) => x.value === form.value.items[i].category)
  if (c) form.value.items[i].accountCode = c.account
}

async function save() {
  if (!form.value.claimantEmployeeId) { notify('请选择报销人', 'warn'); return }
  busy.value = true
  const r = await api.saveClaim(form.value)
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`报销单 ${r.data.code} 已保存（草稿）`, 'success')
  formOpen.value = false
  await load()
}

async function openDetail(row) {
  const r = await api.claimDetail(row.id)
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  detail.value = r.data
  detailOpen.value = true
}

async function approve(row) {
  // 审批人取当前操作人对应的员工；找不到时提示先建员工档案
  const me = employees.value.find((e) => e.name === operator.value.trim())
  if (!me) {
    notify(`找不到名为「${operator.value.trim() || '（空）'}」的员工。` +
      `请先在工资 → 员工档案里建立，或在操作人栏填写员工的姓名。`, 'warn')
    return
  }
  const r = await api.approveClaim({ id: row.id, approverId: me.id })
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已审批通过', 'success')
  await load()
}

async function doReject() {
  if (!rejectReason.value.trim()) { notify('请写明驳回理由', 'warn'); return }
  const r = await api.rejectClaim({ id: detail.value.id, reason: rejectReason.value.trim() })
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已驳回', 'success')
  rejectOpen.value = false
  detailOpen.value = false
  rejectReason.value = ''
  await load()
}

async function post(row) {
  if (!operator.value.trim()) { notify('请填写记账人', 'warn'); return }
  busy.value = true
  const r = await api.postClaim({ id: row.id, postingBy: operator.value.trim() })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已生成凭证 ${r.data.voucherNo}`, 'success')
  await load()
}

const statusTone = (s) =>
  s === 'posted' ? 'profit'
    : s === 'approved' || s === 'paid' ? 'default'
      : s === 'rejected' ? 'loss' : 'warn'
</script>

<template>
  <div class="flex flex-col gap-4">
    <div class="flex flex-wrap items-center gap-2">
      <select
        v-model="filter.status"
        class="h-9 rounded-md border border-input bg-transparent px-2 text-sm"
        @change="load"
      >
        <option value="">全部状态</option>
        <option value="draft">草稿</option>
        <option value="approved">已审批</option>
        <option value="paid">已付款</option>
        <option value="posted">已记账</option>
        <option value="rejected">已驳回</option>
      </select>

      <div class="ml-auto flex items-center gap-2">
        <Input v-model="operator"
                 @change="rememberBookkeeper(operator)" class="h-8 w-32" placeholder="操作人" />
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" /> 刷新
        </Button>
        <Button size="sm" @click="openNew"><Plus /> 新建报销单</Button>
      </div>
    </div>

    <Card>
      <CardHeader>
        <CardTitle>差旅与费用报销</CardTitle>
        <CardDescription>
          流程：草稿 → 提交审批 → 审批通过 → 生成凭证。
          审批通过前不能记账 —— 这是内控的基本要求。
        </CardDescription>
      </CardHeader>
      <CardContent class="px-0">
        <Spinner v-if="loading" />
        <EmptyState
          v-else-if="rows.length === 0"
          title="还没有报销单"
          description="录入差旅或费用报销，审批后一键生成凭证（含进项税额拆分）"
        >
          <Button size="sm" @click="openNew"><Plus /> 新建报销单</Button>
        </EmptyState>
        <table v-else class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40">
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">单号</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">报销人</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">申请日期</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">事由 / 目的地</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">报销金额</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">状态</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">凭证</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rows" :key="r.id" class="border-b last:border-0 hover:bg-accent/30">
              <td class="px-3 py-1.5 font-mono text-xs">{{ r.code }}</td>
              <td class="px-3 py-1.5">{{ r.claimantName }}</td>
              <td class="px-3 py-1.5 whitespace-nowrap text-xs">{{ r.applyDate }}</td>
              <td class="max-w-[20rem] truncate px-3 py-1.5">
                {{ r.reason || '—' }}
                <span v-if="r.destination" class="text-muted-foreground">· {{ r.destination }}</span>
              </td>
              <td class="num px-3 py-1.5 font-medium">{{ fmtMoney(r.totalAmount) }}</td>
              <td class="px-3 py-1.5"><Badge :variant="statusTone(r.status)">{{ r.statusLabel }}</Badge></td>
              <td class="px-3 py-1.5">
                <span v-if="r.voucherNo" class="font-mono text-xs text-[var(--profit)]">{{ r.voucherNo }}</span>
                <span v-else class="text-xs text-muted-foreground">—</span>
              </td>
              <td class="px-3 py-1.5">
                <div class="flex justify-end gap-1">
                  <Button variant="ghost" size="sm" @click="openDetail(r)">查看</Button>
                  <Button v-if="r.canApprove" variant="ghost" size="sm" @click="approve(r)">
                    <CheckCircle2 /> 审批
                  </Button>
                  <Button v-if="r.canPost" variant="ghost" size="sm" @click="post(r)">
                    <FileCheck2 /> 记账
                  </Button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </CardContent>
    </Card>

    <!-- 新建 / 编辑报销单 -->
    <Modal
      v-model:open="formOpen"
      :title="form.id ? '编辑报销单' : '新建报销单'"
      description="选了费用类别会自动带出默认科目 —— 会计科目是最容易填错、也最难发现的一栏。"
      width="max-w-6xl"
    >
      <div class="flex flex-col gap-4">
        <div class="grid grid-cols-4 gap-3">
          <div>
            <Label>报销人 *</Label>
            <select v-model.number="form.claimantEmployeeId" class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option :value="0">请选择</option>
              <option v-for="e in employees" :key="e.id" :value="e.id">{{ e.name }}</option>
            </select>
          </div>
          <div>
            <Label>部门</Label>
            <select v-model="form.deptId" class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option :value="null">未指定</option>
              <option v-for="d in departments" :key="d.id" :value="d.id">{{ d.fullName }}</option>
            </select>
          </div>
          <div><Label>申请日期 *</Label><Input v-model="form.applyDate" class="mt-1.5" placeholder="2025-03-11" /></div>
          <div>
            <Label>付款科目</Label>
            <Input v-model="form.payFromAccount" class="mt-1.5" placeholder="1002" />
          </div>
          <div><Label>出差起始</Label><Input v-model="form.tripStart" class="mt-1.5" /></div>
          <div><Label>出差结束</Label><Input v-model="form.tripEnd" class="mt-1.5" /></div>
          <div><Label>目的地</Label><Input v-model="form.destination" class="mt-1.5" /></div>
          <div><Label>事由</Label><Input v-model="form.reason" class="mt-1.5" placeholder="杭州—上海 客户拜访" /></div>
        </div>

        <div class="rounded-lg border">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b bg-muted/40">
                <th class="h-9 w-10 px-2 text-left text-xs font-medium text-muted-foreground">#</th>
                <th class="h-9 px-2 text-left text-xs font-medium text-muted-foreground">类别</th>
                <th class="h-9 px-2 text-left text-xs font-medium text-muted-foreground">发生日期</th>
                <th class="h-9 px-2 text-left text-xs font-medium text-muted-foreground">摘要</th>
                <th class="h-9 px-2 text-left text-xs font-medium text-muted-foreground">费用科目</th>
                <th class="h-9 w-28 px-2 text-right text-xs font-medium text-muted-foreground">金额（元）</th>
                <th class="h-9 w-24 px-2 text-right text-xs font-medium text-muted-foreground">可抵扣税额</th>
                <th class="h-9 w-10 px-2" />
              </tr>
            </thead>
            <tbody>
              <tr v-for="(it, i) in form.items" :key="i" class="border-b last:border-0">
                <td class="px-2 py-1 text-xs text-muted-foreground">{{ i + 1 }}</td>
                <td class="px-2 py-1">
                  <select
                    v-model="it.category"
                    class="h-8 w-full rounded border-0 bg-transparent px-1 text-sm focus:bg-accent/40 focus:outline-none"
                    @change="onCategoryChange(i)"
                  >
                    <option v-for="c in categories" :key="c.value" :value="c.value">{{ c.label }}</option>
                  </select>
                </td>
                <td class="px-2 py-1">
                  <input v-model="it.occurDate" class="w-full rounded border-0 bg-transparent px-1 py-1 text-sm focus:bg-accent/40 focus:outline-none" placeholder="2025-03-11" />
                </td>
                <td class="px-2 py-1">
                  <input v-model="it.summary" class="w-full rounded border-0 bg-transparent px-1 py-1 text-sm focus:bg-accent/40 focus:outline-none" placeholder="摘要" />
                </td>
                <td class="px-2 py-1">
                  <input v-model="it.accountCode" class="w-full rounded border-0 bg-transparent px-1 py-1 font-mono text-xs focus:bg-accent/40 focus:outline-none" placeholder="自动" />
                </td>
                <td class="px-2 py-1">
                  <input v-model="it.amountYuan" class="num w-full rounded border-0 bg-transparent px-1 py-1 text-sm focus:bg-accent/40 focus:outline-none" placeholder="0.00" />
                </td>
                <td class="px-2 py-1">
                  <input v-model="it.taxYuan" class="num w-full rounded border-0 bg-transparent px-1 py-1 text-sm focus:bg-accent/40 focus:outline-none" placeholder="0.00" />
                </td>
                <td class="px-2 py-1">
                  <button class="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground" @click="removeItem(i)">
                    <Trash2 class="size-3.5" />
                  </button>
                </td>
              </tr>
            </tbody>
            <tfoot>
              <tr class="border-t bg-muted/40 font-medium">
                <td colspan="5" class="px-2 py-2">
                  <button class="flex items-center gap-1 text-xs hover:underline" @click="addItem">
                    <Plus class="size-3.5" /> 增加一行
                  </button>
                </td>
                <td class="num px-2 py-2">{{ fmtMoney(total.amt) }}</td>
                <td class="num px-2 py-2 text-muted-foreground">{{ fmtMoney(total.tax) }}</td>
                <td />
              </tr>
            </tfoot>
          </table>
        </div>

        <div class="flex items-center gap-3 text-sm">
          <span class="text-muted-foreground">报销合计</span>
          <span class="num font-semibold">{{ fmtMoney(total.amt) }}</span>
          <span class="text-muted-foreground">其中可抵扣进项税</span>
          <span class="num text-[var(--profit)]">{{ fmtMoney(total.tax) }}</span>
          <span class="text-muted-foreground">计入费用</span>
          <span class="num">{{ fmtMoney(total.net) }}</span>
        </div>
      </div>

      <template #footer>
        <Button variant="ghost" @click="formOpen = false">取消</Button>
        <Button :disabled="busy" @click="save"><Save /> 保存草稿</Button>
      </template>
    </Modal>

    <!-- 详情 -->
    <Modal
      v-model:open="detailOpen"
      :title="detail ? `报销单 ${detail.code}` : '报销单'"
      :description="detail?.blockedReason || ''"
      width="max-w-5xl"
    >
      <div v-if="detail" class="flex flex-col gap-4">
        <div class="grid grid-cols-4 gap-3 text-sm">
          <div><p class="text-xs text-muted-foreground">报销人</p><p class="mt-0.5">{{ detail.claimantName }}</p></div>
          <div><p class="text-xs text-muted-foreground">申请日期</p><p class="mt-0.5">{{ detail.applyDate }}</p></div>
          <div>
            <p class="text-xs text-muted-foreground">状态</p>
            <p class="mt-0.5"><Badge :variant="statusTone(detail.status)">{{ detail.statusLabel }}</Badge></p>
          </div>
          <div><p class="text-xs text-muted-foreground">审批人</p><p class="mt-0.5">{{ detail.approverName || '—' }}</p></div>
          <div><p class="text-xs text-muted-foreground">出差期间</p>
            <p class="mt-0.5">{{ detail.tripStart || '—' }} ~ {{ detail.tripEnd || '—' }}</p>
          </div>
          <div><p class="text-xs text-muted-foreground">目的地</p><p class="mt-0.5">{{ detail.destination || '—' }}</p></div>
          <div class="col-span-2"><p class="text-xs text-muted-foreground">事由</p><p class="mt-0.5">{{ detail.reason || '—' }}</p></div>
        </div>

        <div class="rounded-lg border">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b bg-muted/40">
                <th class="h-8 px-3 text-left text-xs font-medium text-muted-foreground">#</th>
                <th class="h-8 px-3 text-left text-xs font-medium text-muted-foreground">类别</th>
                <th class="h-8 px-3 text-left text-xs font-medium text-muted-foreground">发生日期</th>
                <th class="h-8 px-3 text-left text-xs font-medium text-muted-foreground">摘要</th>
                <th class="h-8 px-3 text-left text-xs font-medium text-muted-foreground">科目</th>
                <th class="h-8 px-3 text-right text-xs font-medium text-muted-foreground">金额</th>
                <th class="h-8 px-3 text-right text-xs font-medium text-muted-foreground">可抵扣税额</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="it in detail.items" :key="it.lineNo" class="border-b last:border-0">
                <td class="px-3 py-1.5 text-xs text-muted-foreground">{{ it.lineNo }}</td>
                <td class="px-3 py-1.5">{{ it.categoryLabel }}</td>
                <td class="px-3 py-1.5 text-xs">{{ it.occurDate }}</td>
                <td class="px-3 py-1.5">{{ it.summary }}</td>
                <td class="px-3 py-1.5 font-mono text-xs">{{ it.accountCode }}</td>
                <td class="num px-3 py-1.5">{{ fmtMoney(it.amount) }}</td>
                <td class="num px-3 py-1.5 text-[var(--profit)]">{{ fmtMoney(it.taxAmount, { blankZero: true }) }}</td>
              </tr>
              <tr class="bg-muted/40 font-medium">
                <td colspan="5" class="px-3 py-2">合计</td>
                <td class="num px-3 py-2">{{ fmtMoney(detail.totalAmount) }}</td>
                <td class="num px-3 py-2">{{ fmtMoney(detail.totalTax) }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="flex items-start gap-2 rounded-md border bg-muted/30 p-3 text-xs">
          <Info class="mt-0.5 size-3.5 shrink-0" />
          <span>
            生成凭证时会自动拆分进项税额，并按明细逐条归集到各自的费用科目
            （含部门辅助核算）。
          </span>
        </div>
      </div>

      <template #footer>
        <Button variant="ghost" @click="detailOpen = false">关闭</Button>
        <Button
          v-if="detail?.canApprove"
          variant="outline"
          @click="rejectOpen = true"
        ><XCircle /> 驳回</Button>
        <Button
          v-if="detail?.canApprove"
          @click="approve(detail)"
        ><CheckCircle2 /> 审批通过</Button>
        <Button
          v-if="detail?.canPost"
          @click="post(detail)"
        ><FileCheck2 /> 生成凭证</Button>
      </template>
    </Modal>

    <!-- 驳回 -->
    <Modal
      v-model:open="rejectOpen"
      title="驳回报销单"
      description="必须写明理由 —— 否则报销人不知道要改什么。"
      width="max-w-lg"
    >
      <Input v-model="rejectReason" placeholder="例：缺少住宿费发票" />
      <template #footer>
        <Button variant="ghost" @click="rejectOpen = false">取消</Button>
        <Button variant="destructive" @click="doReject"><XCircle /> 确认驳回</Button>
      </template>
    </Modal>
  </div>
</template>
