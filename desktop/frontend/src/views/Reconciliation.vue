<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import {
  RefreshCw, Printer, AlertTriangle, CheckCircle2, HelpCircle, Landmark, ArrowRight,
  Download,
} from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
import { fmtMoney } from '@/lib/format'
import Card from '@/components/ui/Card.vue'
import CardHeader from '@/components/ui/CardHeader.vue'
import CardTitle from '@/components/ui/CardTitle.vue'
import CardDescription from '@/components/ui/CardDescription.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Spinner from '@/components/ui/Spinner.vue'
import EmptyState from '@/components/ui/EmptyState.vue'

const data = ref(null)
const loading = ref(false)
const accounts = ref([])
const filter = ref({ accountCode: '', asOf: '', from: '', bankBalance: '' })
// 手工输入的银行余额是「元」，后端要「分」——
// 在这里转而不是让用户算，避免会计对着 10000000 数零。
const bankCents = ref(null)

async function loadAccounts() {
  const r = await api.bankAccounts()
  if (!r.ok) return
  accounts.value = r.data ?? []
  if (!filter.value.accountCode && accounts.value.length) {
    filter.value.accountCode = accounts.value[0].code
  }
}

async function load() {
  if (!filter.value.accountCode) {
    notify('请先选择要核对的银行科目', 'error')
    return
  }
  // 余额留空 → 自动取对账单最后一笔余额；填 0 是合法值（账户清零），
  // 所以这里用「字符串非空」判断而不是真值判断。
  if (filter.value.bankBalance === '') {
    bankCents.value = null
  } else {
    const n = Number(String(filter.value.bankBalance).replace(/,/g, ''))
    if (!Number.isFinite(n)) {
      notify('银行对账单余额不是有效数字', 'error')
      return
    }
    bankCents.value = Math.round(n * 100)
  }

  loading.value = true
  const r = await api.bankReconciliation({
    accountCode: filter.value.accountCode,
    asOf: filter.value.asOf,
    from: filter.value.from,
    bankBalance: bankCents.value,
  })
  loading.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); data.value = null; return }
  data.value = r.data
}

onMounted(async () => { await loadAccounts(); await load() })

// 切账户立刻重算：会计对比两个账户时不想每次再点一次「生成」
watch(() => filter.value.accountCode, load)

const sections = computed(() => [
  {
    side: 'book', kind: 'add', label: '加：银行已收、企业未收',
    items: data.value?.bankReceivedNotBooked ?? [],
    hint: '银行已经收到、企业账上还没记 —— 如银行代收的货款',
  },
  {
    side: 'book', kind: 'less', label: '减：银行已付、企业未付',
    items: data.value?.bankPaidNotBooked ?? [],
    hint: '银行已经扣款、企业账上还没记 —— 如手续费、代扣水电',
  },
  {
    side: 'bank', kind: 'add', label: '加：企业已收、银行未收',
    items: data.value?.bookReceivedNotBanked ?? [],
    hint: '企业入账了、银行还没处理 —— 如已送存未入账的支票',
  },
  {
    side: 'bank', kind: 'less', label: '减：企业已付、银行未付',
    items: data.value?.bookPaidNotBanked ?? [],
    hint: '企业记账了、银行还没扣款 —— 如已开出未兑付的支票',
  },
])

const bookSections = computed(() => sections.value.filter((s) => s.side === 'book'))
const bankSections = computed(() => sections.value.filter((s) => s.side === 'bank'))

function sectionTotal(items) {
  return items.reduce((a, it) => a + (it.amount ?? 0), 0)
}

// 三态而不是布尔：没导入对账单时「无从判断」，
// 亮绿勾会谎报账实相符、亮红叉会冤枉账记错了，两者都让会计误判。
const tone = computed(() => {
  const s = data.value?.status
  if (s === 'balanced') return { variant: 'profit', icon: CheckCircle2, cls: 'text-[var(--profit)]' }
  if (s === 'unbalanced') return { variant: 'loss', icon: AlertTriangle, cls: 'text-[var(--loss)]' }
  return { variant: 'muted', icon: HelpCircle, cls: 'text-muted-foreground' }
})

// 导出复用「当前屏幕上的这张表」的参数 —— 导出的必须是同一张表。
// Wails 的 save dialog 由 Go 侧弹（SaveFileDialog），
// 前端只负责把参数传过去。
const exporting = ref(false)
async function exportExcel() {
  if (!data.value) return
  exporting.value = true
  const n = await api.suggestReconciliationName(filter.value.accountCode)
  const dest = n.ok ? n.data : '银行余额调节表.xlsx'
  const r = await api.exportReconciliation({
    accountCode: filter.value.accountCode,
    asOf: filter.value.asOf,
    from: filter.value.from,
    bankBalance: bankCents.value,
  }, dest)
  exporting.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已导出：${r.data.path}`, 'success')
}

// 模板里不能直接写 window.print()：Vue 模板只暴露白名单全局，
// window 不在其中，写进去会静默变成 undefined 并在点击时报错。
function printPage() { window.print() }

const hasStatement = computed(() => data.value?.bankBalance !== null && data.value?.bankBalance !== undefined)
const unreconciledCount = computed(() =>
  sections.value.reduce((a, s) => a + s.items.length, 0))
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- 参数栏 -->
    <div class="flex flex-wrap items-end gap-3 no-print">
      <div class="w-56">
        <Label>银行科目</Label>
        <select
          v-model="filter.accountCode"
          class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm"
        >
          <option v-for="a in accounts" :key="a.code" :value="a.code">
            {{ a.code }} {{ a.name }}
          </option>
        </select>
      </div>
      <div class="w-36">
        <Label>截止日期</Label>
        <Input v-model="filter.asOf" class="mt-1.5" placeholder="留空 = 当前账期" />
      </div>
      <div class="w-36">
        <Label>对账起始日</Label>
        <Input v-model="filter.from" class="mt-1.5" placeholder="留空 = 对账单首日" />
      </div>
      <div class="w-44">
        <Label>银行对账单余额（元）</Label>
        <Input
          v-model="filter.bankBalance" class="mt-1.5"
          placeholder="留空 = 取流水末笔余额"
        />
      </div>
      <Button :disabled="loading" @click="load">
        <RefreshCw :class="loading ? 'animate-spin' : ''" /> 生成
      </Button>
      <Button v-if="data" variant="outline" @click="printPage"><Printer /> 打印 / PDF</Button>
      <Button v-if="data" variant="outline" :disabled="exporting" @click="exportExcel">
        <Download /> 导出 Excel
      </Button>
    </div>

    <Spinner v-if="loading" />
    <EmptyState
      v-else-if="!data"
      title="还没有数据"
      description="先选择银行科目，再点「生成」"
    />

    <template v-else>
      <!-- 结论：这张表唯一要回答的问题 -->
      <Card :class="data.status === 'unbalanced' ? 'border-[var(--loss)]/50' : ''">
        <CardContent class="flex flex-wrap items-center gap-x-6 gap-y-3 pt-5">
          <div class="flex items-center gap-2">
            <component :is="tone.icon" class="size-5" :class="tone.cls" />
            <span class="text-base font-semibold" :class="tone.cls">{{ data.statusLabel }}</span>
          </div>
          <div>
            <p class="text-xs text-muted-foreground">调节后账面余额</p>
            <p class="num text-lg font-semibold">{{ fmtMoney(data.bookAdjusted) }}</p>
          </div>
          <ArrowRight class="size-4 text-muted-foreground" />
          <div>
            <p class="text-xs text-muted-foreground">调节后银行余额</p>
            <p class="num text-lg font-semibold">
              {{ hasStatement ? fmtMoney(data.bankAdjusted) : '—' }}
            </p>
          </div>
          <div v-if="data.status === 'unbalanced'">
            <p class="text-xs text-muted-foreground">差额</p>
            <p class="num text-lg font-semibold text-[var(--loss)]">
              {{ fmtMoney(Math.abs(data.difference)) }}
            </p>
          </div>
          <div class="ml-auto text-right text-xs text-muted-foreground">
            <p>{{ data.accountCode }} {{ data.accountName }}</p>
            <p>对账期间 {{ data.from }} 至 {{ data.asOf }}</p>
          </div>
        </CardContent>
      </Card>

      <!-- ★ 期初差额：最该先说的一句话 -->
      <Card v-if="data.openingDiff !== 0" class="border-[var(--warn)]/50">
        <CardHeader>
          <CardTitle class="flex items-center gap-2 text-[var(--warn)]">
            <AlertTriangle class="size-4" /> 先核对期初，不要急着翻未达账项
          </CardTitle>
          <CardDescription>
            期初余额两侧不一致：账面 <b class="num">{{ fmtMoney(data.bookOpening) }}</b>、
            银行 <b class="num">{{ fmtMoney(data.bankOpening) }}</b>，
            差 <b class="num">{{ fmtMoney(Math.abs(data.openingDiff)) }}</b>。
            <br />
            对账窗口内的发生额在两边会完全抵消，因此
            <b>两侧调节后的差额必然恰好等于这个期初差额</b> ——
            把期初对平，这张表自然就平了。
          </CardDescription>
        </CardHeader>
      </Card>

      <!-- 两栏对照的正式版式 -->
      <Card>
        <CardHeader>
          <CardTitle>银行存款余额调节表</CardTitle>
          <CardDescription>
            左边把企业账调到银行口径，右边把银行账调到企业口径。
            两边调完必须相等 —— 相等说明账没记错。
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <!-- 企业账面方 -->
            <div class="rounded-lg border p-4">
              <div class="flex items-center gap-2 border-b pb-2">
                <Landmark class="size-4 text-muted-foreground" />
                <span class="font-medium">企业账面</span>
              </div>
              <div class="mt-3 flex items-baseline justify-between">
                <span class="text-sm">账面余额</span>
                <span class="num text-base font-semibold">{{ fmtMoney(data.bookBalance) }}</span>
              </div>
              <div class="mt-3 space-y-3">
                <div v-for="s in bookSections" :key="s.label">
                  <div class="flex items-baseline justify-between text-sm">
                    <span :class="s.items.length ? '' : 'text-muted-foreground'">{{ s.label }}</span>
                    <span class="num" :class="s.items.length ? 'font-medium' : 'text-muted-foreground'">
                      {{ fmtMoney(sectionTotal(s.items), { blankZero: true }) || '—' }}
                    </span>
                  </div>
                  <ul v-if="s.items.length" class="mt-1 space-y-0.5">
                    <li
                      v-for="(it, i) in s.items" :key="i"
                      class="flex items-baseline justify-between gap-2 pl-3 text-xs"
                    >
                      <span class="truncate text-muted-foreground" :title="it.summary">
                        {{ it.date }} · {{ it.summary }}
                        <span class="font-mono">{{ it.reference }}</span>
                        <span v-if="it.days > 30" class="text-[var(--warn)]">（挂 {{ it.days }} 天）</span>
                      </span>
                      <span class="num shrink-0">{{ fmtMoney(it.amount) }}</span>
                    </li>
                  </ul>
                  <p v-else class="mt-0.5 pl-3 text-[11px] text-muted-foreground">{{ s.hint }}</p>
                </div>
              </div>
              <div class="mt-3 flex items-baseline justify-between border-t pt-2">
                <span class="text-sm font-medium">调节后余额</span>
                <span class="num text-base font-semibold">{{ fmtMoney(data.bookAdjusted) }}</span>
              </div>
            </div>

            <!-- 银行对账单方 -->
            <div class="rounded-lg border p-4">
              <div class="flex items-center gap-2 border-b pb-2">
                <Landmark class="size-4 text-muted-foreground" />
                <span class="font-medium">银行对账单</span>
              </div>
              <div class="mt-3 flex items-baseline justify-between">
                <span class="text-sm">对账单余额</span>
                <span v-if="hasStatement" class="num text-base font-semibold">
                  {{ fmtMoney(data.bankBalance) }}
                </span>
                <span v-else class="text-xs text-muted-foreground">未导入对账单</span>
              </div>
              <div class="mt-3 space-y-3">
                <div v-for="s in bankSections" :key="s.label">
                  <div class="flex items-baseline justify-between text-sm">
                    <span :class="s.items.length ? '' : 'text-muted-foreground'">{{ s.label }}</span>
                    <span class="num" :class="s.items.length ? 'font-medium' : 'text-muted-foreground'">
                      {{ fmtMoney(sectionTotal(s.items), { blankZero: true }) || '—' }}
                    </span>
                  </div>
                  <ul v-if="s.items.length" class="mt-1 space-y-0.5">
                    <li
                      v-for="(it, i) in s.items" :key="i"
                      class="flex items-baseline justify-between gap-2 pl-3 text-xs"
                    >
                      <span class="truncate text-muted-foreground" :title="it.summary">
                        {{ it.date }} · {{ it.summary }}
                        <span class="font-mono">{{ it.reference }}</span>
                        <span v-if="it.days > 30" class="text-[var(--warn)]">（挂 {{ it.days }} 天）</span>
                      </span>
                      <span class="num shrink-0">{{ fmtMoney(it.amount) }}</span>
                    </li>
                  </ul>
                  <p v-else class="mt-0.5 pl-3 text-[11px] text-muted-foreground">{{ s.hint }}</p>
                </div>
              </div>
              <div class="mt-3 flex items-baseline justify-between border-t pt-2">
                <span class="text-sm font-medium">调节后余额</span>
                <span v-if="hasStatement" class="num text-base font-semibold">
                  {{ fmtMoney(data.bankAdjusted) }}
                </span>
                <span v-else class="text-xs text-muted-foreground">—</span>
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      <!-- 提示与下一步 -->
      <Card v-if="data.notes?.length || data.unreconciledFlows > 0">
        <CardHeader>
          <CardTitle>下一步</CardTitle>
        </CardHeader>
        <CardContent class="space-y-2 text-sm">
          <p v-for="(n, i) in data.notes" :key="i" class="text-muted-foreground">{{ n }}</p>
          <p v-if="data.unreconciledFlows > 0" class="text-muted-foreground">
            还有 <b>{{ data.unreconciledFlows }}</b> 笔银行流水未生成凭证。
            <router-link to="/bank" class="text-[var(--debit)] underline">
              去银行流水页处理
            </router-link>
            —— 处理完这些未达账项会自动减少。
          </p>
          <p v-if="unreconciledCount === 0 && data.status === 'balanced'" class="text-[var(--profit)]">
            没有未达账项，两边完全对上了。
          </p>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
