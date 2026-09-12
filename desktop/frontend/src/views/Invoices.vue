<script setup>
import { computed, onMounted, ref } from 'vue'
import { Plus, Save, FileCheck2, Info, RefreshCw } from 'lucide-vue-next'
import { api, notify, DRAFT_HINT } from '@/lib/api'
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

const tab = ref('input') // input | output
const rows = ref([])
const summary = ref(null)
const rates = ref([])
const categories = ref([])
const contacts = ref([])
const departments = ref([])
const loading = ref(false)
const busy = ref(false)
// 记账人来自本机设置（见 lib/operator.js）：全程序一份，不再各页各存一份
const operator = bookkeeper
const filter = ref({ from: '', to: '' })

const formOpen = ref(false)
const form = ref(emptyInvoice())

function emptyInvoice() {
  return {
    id: 0, direction: 'input', kind: 'special',
    code: '', number: '', invoiceDate: '',
    sellerName: '', sellerTaxNo: '', buyerName: '', buyerTaxNo: '',
    amountExTax: '', taxRatePpm: 130000, taxAmount: '', totalAmount: '',
    category: '', contactId: null, remark: '', expenseAccount: '', deptId: null,
  }
}

async function load() {
  loading.value = true
  const [l, s, r, c, ct, t, d] = await Promise.all([
    api.invoices({ direction: tab.value, from: filter.value.from, to: filter.value.to }),
    api.invoiceSummary(tab.value, filter.value.from, filter.value.to),
    api.invoiceRates(), api.invoiceCategories(), api.contactList(),
    api.today(), api.departments(),
  ])
  if (d.ok) departments.value = d.data ?? []
  loading.value = false
  if (l.ok) rows.value = l.data ?? []
  else notify(l.fault.message, 'error', l.fault.detail)
  if (s.ok) summary.value = s.data
  if (r.ok) rates.value = r.data ?? []
  if (c.ok) categories.value = c.data ?? []
  if (ct.ok) contacts.value = ct.data ?? []
  if (t.ok && !form.value.invoiceDate) form.value.invoiceDate = t.data
}
onMounted(async () => {
  // 记账人是本机设置（全程序一份），与页面数据一起加载
  await Promise.all([load(), loadBookkeeper()])
})

function switchTab(v) { tab.value = v; load() }

// 价税合计 = 不含税 + 税额。界面上只让用户填两个，第三个实时算出来 ——
// 让用户同时填三个，就是给他们机会填出「价 + 税 ≠ 合计」的自相矛盾数据。
const computed2 = computed(() => {
  const ex = parseYuanToCents(form.value.amountExTax) ?? 0
  const rate = Number(form.value.taxRatePpm) || 0
  const tax = Math.round((ex * rate) / 1000000)
  return { ex, tax, total: ex + tax }
})

const taxPreview = computed(() => fmtMoney(computed2.value.tax))
const totalPreview = computed(() => fmtMoney(computed2.value.total))

function openNew() {
  form.value = emptyInvoice()
  form.value.direction = tab.value
  form.value.invoiceDate = new Date().toISOString().slice(0, 10)
  formOpen.value = true
}

async function save() {
  if (!form.value.number && !form.value.code) {
    notify('请至少填写发票号码或代码 —— 发票是税务凭据，没有标识无法追溯', 'warn')
    return
  }
  busy.value = true
  // 税额与合计由前端算好后提交；Go 侧仍会校验并允许留空让它自己算
  const payload = {
    ...form.value,
    taxAmount: (computed2.value.tax / 100).toFixed(2),
    totalAmount: (computed2.value.total / 100).toFixed(2),
  }
  const r = await api.saveInvoice(payload)
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('发票已保存', 'success')
  formOpen.value = false
  await load()
}

async function post(row) {
  if (!operator.value.trim()) { notify('请填写记账人', 'warn'); return }
  if (!confirm(`为发票 ${row.number || row.code} 生成凭证？\n\n进项票会自动挂「应付账款」，销项票挂「应收账款」。`)) return
  busy.value = true
  const r = await api.postInvoice({
    id: row.id, postingBy: operator.value.trim(),
    expenseAccount: form.value.expenseAccount,
    deptId: form.value.deptId,
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已生成凭证（${r.data.voucherLabel || r.data.voucherNo}）`, 'success', DRAFT_HINT)
  await load()
}

const kindTone = (k) => (k === 'special' ? 'default' : 'muted')
</script>

<template>
  <div class="flex flex-col gap-4">
    <div class="flex flex-wrap items-center gap-2">
      <Button
        v-for="t in [{ v: 'input', n: '进项发票' }, { v: 'output', n: '销项发票' }]"
        :key="t.v"
        :variant="tab === t.v ? 'default' : 'outline'"
        size="sm"
        @click="switchTab(t.v)"
      >{{ t.n }}</Button>

      <div class="ml-auto flex items-center gap-2">
        <Input v-model="operator"
                 @change="rememberBookkeeper(operator)" class="h-8 w-32" placeholder="记账人" />
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" /> 刷新
        </Button>
        <Button size="sm" @click="openNew"><Plus /> 录入发票</Button>
      </div>
    </div>

    <div v-if="summary" class="grid grid-cols-5 gap-4">
      <Card><CardContent class="pt-5">
        <p class="text-xs text-muted-foreground">张数</p>
        <p class="num mt-1 text-xl font-semibold">{{ summary.count }}</p>
      </CardContent></Card>
      <Card><CardContent class="pt-5">
        <p class="text-xs text-muted-foreground">不含税金额</p>
        <p class="num mt-1 text-xl font-semibold">{{ fmtMoney(summary.amountExTax) }}</p>
      </CardContent></Card>
      <Card><CardContent class="pt-5">
        <p class="text-xs text-muted-foreground">税额</p>
        <p class="num mt-1 text-xl font-semibold">{{ fmtMoney(summary.taxAmount) }}</p>
      </CardContent></Card>
      <Card><CardContent class="pt-5">
        <p class="text-xs text-muted-foreground">可抵扣税额</p>
        <p class="num mt-1 text-xl font-semibold text-[var(--profit)]">{{ fmtMoney(summary.deductible) }}</p>
      </CardContent></Card>
      <Card><CardContent class="pt-5">
        <p class="text-xs text-muted-foreground">尚未入账</p>
        <p class="num mt-1 text-xl font-semibold"
           :class="summary.unpostedCount ? 'text-[var(--warn)]' : ''">
          {{ summary.unpostedCount }}
        </p>
        <p class="text-[11px] text-muted-foreground">张</p>
      </CardContent></Card>
    </div>

    <Card>
      <CardHeader>
        <CardTitle>{{ tab === 'input' ? '进项发票' : '销项发票' }}</CardTitle>
        <CardDescription>
          发票是<b>单据</b>，凭证是<b>账</b>。生成凭证时进项票挂「应付账款」、
          销项票挂「应收账款」，之后由收付款凭证去冲。
          <span v-if="tab === 'input'">只有<b>专用发票</b>的进项税额可抵扣。</span>
        </CardDescription>
      </CardHeader>
      <CardContent class="px-0">
        <Spinner v-if="loading" />
        <EmptyState
          v-else-if="rows.length === 0"
          title="还没有发票"
          description="录入发票后可以一键生成凭证，发票与凭证会双向关联"
        >
          <Button size="sm" @click="openNew"><Plus /> 录入第一张</Button>
        </EmptyState>
        <div v-else class="overflow-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b bg-muted/40">
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">开票日期</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">种类</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">发票号</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">
                  {{ tab === 'input' ? '销方' : '购方' }}
                </th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">不含税</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">税率</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">税额</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">价税合计</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">凭证</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in rows" :key="r.id" class="border-b last:border-0 hover:bg-accent/30">
                <td class="px-3 py-1.5 whitespace-nowrap text-xs">{{ r.invoiceDate }}</td>
                <td class="px-3 py-1.5"><Badge :variant="kindTone(r.kind)">{{ r.kindLabel }}</Badge></td>
                <td class="px-3 py-1.5 font-mono text-xs">{{ r.number || r.code }}</td>
                <td class="max-w-[16rem] truncate px-3 py-1.5">
                  {{ tab === 'input' ? r.sellerName : r.buyerName }}
                </td>
                <td class="num px-3 py-1.5">{{ fmtMoney(r.amountExTax) }}</td>
                <td class="num px-3 py-1.5 text-muted-foreground">{{ r.taxRateLabel }}</td>
                <td class="num px-3 py-1.5">{{ fmtMoney(r.taxAmount) }}</td>
                <td class="num px-3 py-1.5 font-medium">{{ fmtMoney(r.totalAmount) }}</td>
                <td class="px-3 py-1.5">
                  <span v-if="r.posted" class="font-mono text-xs text-[var(--warn)]">
                    {{ r.voucherLabel || r.voucherNo || '草稿' }}
                  </span>
                  <Badge v-else variant="warn">未入账</Badge>
                </td>
                <td class="px-3 py-1.5">
                  <div class="flex justify-end gap-1">
                    <Button v-if="!r.posted" variant="ghost" size="sm" @click="post(r)">
                      <FileCheck2 /> 生成凭证
                    </Button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </CardContent>
    </Card>

    <!-- 录入发票 -->
    <Modal
      v-model:open="formOpen"
      title="录入发票"
      description="税额与价税合计由「不含税金额 × 税率」自动算出，不必手工填 —— 让人同时填三个数字，就是给他们机会填出互相矛盾的数据。"
      width="max-w-3xl"
    >
      <div class="grid grid-cols-3 gap-3">
        <div>
          <Label>进销方向</Label>
          <select v-model="form.direction" class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
            <option value="input">进项（收到的票）</option>
            <option value="output">销项（开出的票）</option>
          </select>
        </div>
        <div>
          <Label>发票种类</Label>
          <select v-model="form.kind" class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
            <option value="special">增值税专用发票</option>
            <option value="general">增值税普通发票</option>
            <option value="e_special">电子专用发票</option>
            <option value="e_general">电子普通发票</option>
            <option value="other">其他发票</option>
          </select>
        </div>
        <div><Label>开票日期</Label><Input v-model="form.invoiceDate" class="mt-1.5" placeholder="2025-03-11" /></div>

        <div><Label>发票代码</Label><Input v-model="form.code" class="mt-1.5" /></div>
        <div><Label>发票号码</Label><Input v-model="form.number" class="mt-1.5" /></div>
        <div>
          <Label>往来单位</Label>
          <select v-model="form.contactId" class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
            <option :value="null">未指定</option>
            <option v-for="c in contacts" :key="c.id" :value="c.id">{{ c.name }}</option>
          </select>
        </div>

        <div class="col-span-3 grid grid-cols-2 gap-3">
          <div>
            <Label>{{ form.direction === 'input' ? '销方名称' : '购方名称' }}</Label>
            <Input
              :model-value="form.direction === 'input' ? form.sellerName : form.buyerName"
              class="mt-1.5"
              @update:model-value="(v) => form.direction === 'input' ? (form.sellerName = v) : (form.buyerName = v)"
            />
          </div>
          <div>
            <Label>纳税人识别号</Label>
            <Input
              :model-value="form.direction === 'input' ? form.sellerTaxNo : form.buyerTaxNo"
              class="mt-1.5"
              @update:model-value="(v) => form.direction === 'input' ? (form.sellerTaxNo = v) : (form.buyerTaxNo = v)"
            />
          </div>
        </div>

        <div>
          <Label>不含税金额（元）</Label>
          <Input v-model="form.amountExTax" class="mt-1.5" placeholder="10000.00" />
        </div>
        <div>
          <Label>税率</Label>
          <select v-model.number="form.taxRatePpm" class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
            <option v-for="r in rates" :key="r.ppM" :value="r.ppM">{{ r.label }}</option>
            <option :value="0">免税 / 0%</option>
          </select>
        </div>
        <div>
          <Label>费用科目（进项用）</Label>
          <Input v-model="form.expenseAccount" class="mt-1.5" placeholder="留空 = 560206 办公费" />
        </div>
        <div>
          <Label>费用归属部门（进项用）</Label>
          <select v-model="form.deptId" class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
            <option :value="null">未指定</option>
            <option v-for="d in departments" :key="d.id" :value="d.id">{{ d.fullName }}</option>
          </select>
          <p class="mt-1 text-[11px] text-[var(--warn)]">费用科目要求部门，不填记不了账</p>
        </div>

        <div class="col-span-3 grid grid-cols-2 gap-3 rounded-lg border bg-muted/30 p-3">
          <div>
            <p class="text-xs text-muted-foreground">自动算出的税额</p>
            <p class="num mt-1 font-medium">{{ taxPreview }}</p>
          </div>
          <div>
            <p class="text-xs text-muted-foreground">价税合计</p>
            <p class="num mt-1 font-semibold">{{ totalPreview }}</p>
          </div>
        </div>
      </div>

      <div class="mt-3 flex items-start gap-2 rounded-md border bg-muted/30 p-3 text-xs">
        <Info class="mt-0.5 size-3.5 shrink-0" />
        <span>
          只有<b>进项专用发票</b>的税额可以抵扣。生成凭证时：
          进项 → 借「费用 + 进项税额」/ 贷「应付账款」；
          销项 → 借「应收账款」/ 贷「收入 + 销项税额」。
        </span>
      </div>

      <template #footer>
        <Button variant="ghost" @click="formOpen = false">取消</Button>
        <Button :disabled="busy" @click="save"><Save /> 保存</Button>
      </template>
    </Modal>
  </div>
</template>
