<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import {
  RefreshCw, Printer, Download, AlertTriangle, Columns3,
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
const exporting = ref(false)
const accounts = ref([])
const filter = ref({ accountCode: '', from: '', to: '' })

// 只显示有发生额的栏目：管理费用 17 栏，其中这个月动过的常常只有三五个。
// 默认隐藏空栏目，需要时一键展开看全貌 —— 这是这张表在窄屏上能不能用的关键。
const hideEmpty = ref(true)

async function loadAccounts() {
  const r = await api.columnarAccounts()
  if (!r.ok) return
  accounts.value = r.data ?? []
  if (!filter.value.accountCode && accounts.value.length) {
    // 默认落在管理费用上：这是最常用的多栏式明细账
    const mgmt = accounts.value.find((a) => a.code === '5602')
    filter.value.accountCode = mgmt ? mgmt.code : accounts.value[0].code
  }
}

async function load() {
  if (!filter.value.accountCode) {
    notify('请先选择要展开的科目', 'error')
    return
  }
  loading.value = true
  const r = await api.columnar({
    accountCode: filter.value.accountCode,
    from: filter.value.from,
    to: filter.value.to,
  })
  loading.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); data.value = null; return }
  data.value = r.data
}

onMounted(async () => { await loadAccounts(); await load() })
watch(() => filter.value.accountCode, load)

// 显示哪些栏目
const visible = computed(() => {
  const cols = data.value?.columns ?? []
  const totals = data.value?.columnTotals ?? []
  return cols
    .map((c, i) => ({ ...c, index: i, total: totals[i] ?? 0 }))
    .filter((c) => !hideEmpty.value || c.total !== 0)
})

// 有发生额的栏目数 / 总栏目数，用于提示「已隐藏 N 个无发生额的栏目」
const emptyCount = computed(() => {
  const cols = data.value?.columns ?? []
  const totals = data.value?.columnTotals ?? []
  return cols.filter((_, i) => (totals[i] ?? 0) === 0).length
})

const isDualSide = computed(() =>
  (data.value?.debitColumns?.length ?? 0) > 0 &&
  (data.value?.creditColumns?.length ?? 0) > 0)

// 可见栏目里，借方组与贷方组的跨度（用于分组表头）
function groupSpans(side) {
  const list = visible.value.filter((c) => c.side === side)
  return list.length
}

const selected = computed(() =>
  accounts.value.find((a) => a.code === filter.value.accountCode))

function printPage() { window.print() }

async function exportExcel() {
  if (!data.value) return
  exporting.value = true
  const n = await api.suggestColumnarName(filter.value.accountCode)
  const dest = n.ok ? n.data : '多栏式明细账.xlsx'
  const r = await api.exportColumnar({
    accountCode: filter.value.accountCode,
    from: filter.value.from,
    to: filter.value.to,
  }, dest)
  exporting.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已导出：${r.data.path}`, 'success')
}

// 单元格为空字符串时显示空白而不是 0.00 ——
// 多栏式每一行只填一栏，满屏的 0 会把真正那个数淹掉
function cellText(v) {
  return v ? fmtMoney(v) : ''
}

// 条形图宽度：以本表最大绝对值为基准。
// 不按总额取比例 —— 某一栏特别大时（比如工资），
// 其余栏目会被压成一条几乎看不见的线，反而丢掉了「谁多谁少」的信息。
function barWidth(v) {
  let max = 0
  for (const t of data.value?.columnTotals ?? []) {
    const a = Math.abs(t)
    if (a > max) max = a
  }
  if (!max || !v) return '0%'
  return Math.max(2, Math.round((Math.abs(v) / max) * 100)) + '%'
}
</script>

<template>
  <div class="flex flex-col gap-4">
    <div class="flex flex-wrap items-end gap-3 no-print">
      <div class="w-72">
        <Label>科目</Label>
        <select
          v-model="filter.accountCode"
          class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm"
        >
          <option v-for="a in accounts" :key="a.code" :value="a.code">
            {{ a.code }} {{ a.fullName }}
          </option>
        </select>
      </div>
      <div class="w-32">
        <Label>开始日期</Label>
        <Input v-model="filter.from" class="mt-1.5" placeholder="留空 = 账期初" />
      </div>
      <div class="w-32">
        <Label>结束日期</Label>
        <Input v-model="filter.to" class="mt-1.5" placeholder="留空 = 账期末" />
      </div>
      <Button :disabled="loading" @click="load">
        <RefreshCw :class="loading ? 'animate-spin' : ''" /> 生成
      </Button>
      <Button v-if="data" variant="outline" @click="printPage"><Printer /> 打印 / PDF</Button>
      <Button v-if="data" variant="outline" :disabled="exporting" @click="exportExcel">
        <Download /> 导出 Excel
      </Button>
      <label v-if="data && emptyCount > 0" class="flex items-center gap-1.5 text-sm text-muted-foreground">
        <input v-model="hideEmpty" type="checkbox" class="size-3.5" />
        隐藏无发生额的栏目（{{ emptyCount }} 个）
      </label>
    </div>

    <Spinner v-if="loading" />
    <EmptyState
      v-else-if="!data"
      title="还没有数据"
      description="选择一个有下级明细的科目（如 5602 管理费用），再点「生成」"
    />

    <template v-else>
      <!-- 概览：这张表一眼要回答的两个问题 —— 花了多少、哪一项花得多 -->
      <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <Card>
          <CardContent class="pt-5">
            <p class="text-xs text-muted-foreground">期初余额</p>
            <p class="num mt-1 text-xl font-semibold">
              {{ fmtMoney(data.opening) }}
              <span class="text-sm font-normal text-muted-foreground">{{ data.openingDir }}</span>
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardContent class="pt-5">
            <p class="text-xs text-muted-foreground">本期借方发生</p>
            <p class="num mt-1 text-xl font-semibold text-[var(--debit)]">
              {{ fmtMoney(data.debitTotal) }}
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardContent class="pt-5">
            <p class="text-xs text-muted-foreground">本期贷方发生</p>
            <p class="num mt-1 text-xl font-semibold text-[var(--credit)]">
              {{ fmtMoney(data.creditTotal) }}
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardContent class="pt-5">
            <p class="text-xs text-muted-foreground">期末余额</p>
            <p class="num mt-1 text-xl font-semibold">
              {{ fmtMoney(data.closing) }}
              <span class="text-sm font-normal text-muted-foreground">{{ data.closingDir }}</span>
            </p>
          </CardContent>
        </Card>
      </div>

      <!-- 栏目构成：横向条形，会计扫一眼就知道钱花在哪 -->
      <Card v-if="visible.length">
        <CardHeader>
          <CardTitle class="flex items-center gap-2">
            <Columns3 class="size-4 text-muted-foreground" />
            {{ data.accountCode }} {{ data.accountName }} · 各栏目发生额
          </CardTitle>
          <CardDescription>
            多栏式明细账的意义就在这里：把「这一个月这一项花了多少」变成一眼可见。
            空栏目已按需隐藏。
          </CardDescription>
        </CardHeader>
        <CardContent class="space-y-2">
          <div v-for="c in visible" :key="c.key" class="flex items-center gap-3">
            <span class="w-32 shrink-0 truncate text-sm" :title="c.label">
              <span
                class="mr-1 rounded px-1 text-[10px]"
                :class="c.side === 'credit'
                  ? 'bg-[var(--credit)]/12 text-[var(--credit)]'
                  : 'bg-[var(--debit)]/12 text-[var(--debit)]'"
              >{{ c.sideLabel }}</span>
              {{ c.label }}
              <span v-if="c.other" class="text-[var(--warn)]" title="有发生额没落到正常栏目上">⚠</span>
            </span>
            <div class="h-3 flex-1 overflow-hidden rounded-full bg-muted">
              <div
                class="h-full transition-all"
                :class="c.total < 0 ? 'bg-[var(--warn)]' : c.side === 'credit' ? 'bg-[var(--credit)]' : 'bg-[var(--debit)]'"
                :style="{ width: barWidth(c.total) }"
              />
            </div>
            <span class="num w-28 shrink-0 text-right text-sm font-medium">
              {{ fmtMoney(c.total) }}
            </span>
          </div>
          <p v-if="isDualSide" class="pt-1 text-xs text-muted-foreground">
            两侧都有栏目 —— 这是增值税专用格式：借方栏目增加可抵扣/已交数，
            贷方栏目增加应交数，两者相抵就是应交未交。
          </p>
        </CardContent>
      </Card>

      <!-- 正式版式 -->
      <Card>
        <CardHeader>
          <CardTitle>多栏式明细账</CardTitle>
          <CardDescription>
            {{ data.from }} 至 {{ data.to }} ·
            <span class="text-[var(--debit)]">借方 {{ groupSpans('debit') }} 栏</span>
            <template v-if="groupSpans('credit')">
              · <span class="text-[var(--credit)]">贷方 {{ groupSpans('credit') }} 栏</span>
            </template>
            · 逐笔登记，红字冲销记在本科目栏内为负数
          </CardDescription>
        </CardHeader>
        <CardContent class="px-0">
          <div class="overflow-auto">
            <table class="w-full border-collapse text-sm">
              <thead>
                <!-- 分组表头：借/贷 -->
                <tr class="border-b bg-muted/40">
                  <th rowspan="2" class="sticky left-0 z-20 h-9 min-w-24 bg-muted/40 px-2 text-left text-xs font-medium text-muted-foreground">日期</th>
                  <th rowspan="2" class="h-9 min-w-32 px-2 text-left text-xs font-medium text-muted-foreground">凭证号</th>
                  <th rowspan="2" class="h-9 min-w-40 px-2 text-left text-xs font-medium text-muted-foreground">摘要</th>
                  <th
                    v-if="groupSpans('debit')"
                    :colspan="groupSpans('debit')"
                    class="h-9 px-2 text-center text-xs font-medium text-[var(--debit)]"
                  >借方</th>
                  <th
                    v-if="groupSpans('credit')"
                    :colspan="groupSpans('credit')"
                    class="h-9 border-l px-2 text-center text-xs font-medium text-[var(--credit)]"
                  >贷方</th>
                  <th rowspan="2" class="h-9 min-w-24 px-2 text-right text-xs font-medium text-muted-foreground">余额</th>
                </tr>
                <tr class="border-b bg-muted/40">
                  <th
                    v-for="c in visible.filter((x) => x.side === 'debit')" :key="'d' + c.key"
                    class="h-8 min-w-24 px-2 text-right text-xs font-medium text-muted-foreground whitespace-nowrap"
                    :title="c.other ? '有发生额没落到正常栏目上' : c.label"
                  >
                    {{ c.label }}<span v-if="c.other" class="text-[var(--warn)]">⚠</span>
                  </th>
                  <th
                    v-for="c in visible.filter((x) => x.side === 'credit')" :key="'c' + c.key"
                    class="h-8 min-w-24 border-l px-2 text-right text-xs font-medium text-muted-foreground whitespace-nowrap"
                  >{{ c.label }}</th>
                </tr>
              </thead>
              <tbody>
                <!-- 期初行 -->
                <tr class="border-b bg-muted/20">
                  <td class="sticky left-0 z-10 bg-muted/20 px-2 py-1.5 text-xs text-muted-foreground" colspan="3">
                    期初余额
                  </td>
                  <td :colspan="visible.length" />
                  <td class="num px-2 py-1.5 text-right text-xs">
                    {{ fmtMoney(data.opening) }} {{ data.openingDir }}
                  </td>
                </tr>

                <tr v-if="data.rows.length === 0">
                  <td :colspan="visible.length + 4" class="px-4 py-6 text-center text-sm text-muted-foreground">
                    这一期间没有发生额
                  </td>
                </tr>

                <tr
                  v-for="(r, ri) in data.rows" :key="ri"
                  class="border-b hover:bg-accent/30"
                >
                  <td class="sticky left-0 z-10 bg-background px-2 py-1.5 whitespace-nowrap">{{ r.date }}</td>
                  <td class="px-2 py-1.5 font-mono text-xs whitespace-nowrap">{{ r.voucherNo }}</td>
                  <td class="max-w-60 truncate px-2 py-1.5" :title="r.summary">{{ r.summary }}</td>
                  <td
                    v-for="c in visible.filter((x) => x.side === 'debit')" :key="'d' + c.key"
                    class="num px-2 py-1.5 text-right whitespace-nowrap"
                    :class="r.amounts[c.index] < 0 ? 'text-[var(--warn)]' : ''"
                  >{{ cellText(r.amounts[c.index]) }}</td>
                  <td
                    v-for="c in visible.filter((x) => x.side === 'credit')" :key="'c' + c.key"
                    class="num border-l px-2 py-1.5 text-right whitespace-nowrap"
                    :class="r.amounts[c.index] < 0 ? 'text-[var(--warn)]' : ''"
                  >{{ cellText(r.amounts[c.index]) }}</td>
                  <td class="num px-2 py-1.5 text-right whitespace-nowrap">
                    {{ fmtMoney(r.balance) }} <span class="text-xs text-muted-foreground">{{ r.dir }}</span>
                  </td>
                </tr>
              </tbody>
              <tfoot>
                <tr class="border-t-2 bg-muted/30 font-medium">
                  <td class="sticky left-0 z-10 bg-muted/30 px-2 py-1.5 text-xs" colspan="3">本期合计</td>
                  <td
                    v-for="c in visible.filter((x) => x.side === 'debit')" :key="'d' + c.key"
                    class="num px-2 py-1.5 text-right whitespace-nowrap"
                  >{{ cellText(c.total) }}</td>
                  <td
                    v-for="c in visible.filter((x) => x.side === 'credit')" :key="'c' + c.key"
                    class="num border-l px-2 py-1.5 text-right whitespace-nowrap"
                  >{{ cellText(c.total) }}</td>
                  <td class="num px-2 py-1.5 text-right whitespace-nowrap">
                    {{ fmtMoney(data.closing) }} {{ data.closingDir }}
                  </td>
                </tr>
              </tfoot>
            </table>
          </div>
        </CardContent>
      </Card>

      <!-- 提示 -->
      <Card v-if="data.notes?.length">
        <CardContent class="flex gap-2 pt-5 text-sm text-muted-foreground">
          <AlertTriangle class="mt-0.5 size-4 shrink-0 text-[var(--warn)]" />
          <div class="space-y-1">
            <p v-for="(n, i) in data.notes" :key="i">{{ n }}</p>
          </div>
        </CardContent>
      </Card>
    </template>
  </div>
</template>

