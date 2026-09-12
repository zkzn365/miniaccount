<script setup>
import { computed, onMounted, ref } from 'vue'
import {
  RefreshCw, Printer, Download, AlertTriangle, CheckCircle2, CalendarX2,
  FileText, ListTree, Hash,
} from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
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
import Spinner from '@/components/ui/Spinner.vue'
import EmptyState from '@/components/ui/EmptyState.vue'

const data = ref(null)
const loading = ref(false)
const exporting = ref(false)
const filter = ref({ from: '', to: '' })
// 三段用标签页切换：会计核对时是「先看凭证字对不对、再看日期有没有漏、
// 最后看科目能不能登总账」，一段一段来，不是同时盯三张表。
const tab = ref('word')

const TABS = [
  { key: 'word', label: '按凭证字', icon: Hash,
    hint: '这个月记了多少张凭证、借贷各多少钱' },
  { key: 'day', label: '按日期', icon: ListTree,
    hint: '哪几天记了账、哪几天一张都没有' },
  { key: 'account', label: '按科目', icon: FileText,
    hint: '科目汇总表，据以登记总账' },
]

async function load() {
  loading.value = true
  const r = await api.summary({ from: filter.value.from, to: filter.value.to })
  loading.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); data.value = null; return }
  data.value = r.data
}
onMounted(load)

function printPage() { window.print() }

async function exportExcel() {
  if (!data.value) return
  exporting.value = true
  const stamp = `${data.value.from}_${data.value.to}`
  const r = await api.exportSummary(
    { from: filter.value.from, to: filter.value.to },
    `凭证汇总表-${stamp}.xlsx`)
  exporting.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已导出：${r.data.path}`, 'success')
}

// 三段里最大的一段用于提示「哪段最值得看」
const sections = computed(() => ({
  word: data.value?.wordRows ?? [],
  day: data.value?.dayRows ?? [],
  account: data.value?.accountRows ?? [],
}))

const currentHint = computed(() =>
  TABS.find((t) => t.key === tab.value)?.hint ?? '')

// 占比条：以凭证张数为分母。用整数百分比，避免一长串小数。
function pct(n, total) {
  if (!total) return '0%'
  return Math.max(2, Math.round((n / total) * 100)) + '%'
}
function pctText(n, total) {
  if (!total) return '0%'
  return Math.round((n / total) * 100) + '%'
}
</script>

<template>
  <div class="flex flex-col gap-4">
    <div class="flex flex-wrap items-end gap-3 no-print">
      <div class="w-36">
        <Label>开始日期</Label>
        <Input v-model="filter.from" class="mt-1.5" placeholder="留空 = 账期初" />
      </div>
      <div class="w-36">
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
    </div>

    <Spinner v-if="loading" />
    <EmptyState
      v-else-if="!data"
      title="还没有数据"
      description="选择一个期间，再点「生成」"
    />

    <template v-else>
      <!-- 总览：这张表第一眼要回答的四个数 -->
      <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <Card>
          <CardContent class="pt-5">
            <p class="text-xs text-muted-foreground">已记账凭证</p>
            <p class="num mt-1 text-xl font-semibold">{{ data.voucherCount }} 张</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent class="pt-5">
            <p class="text-xs text-muted-foreground">借方合计</p>
            <p class="num mt-1 text-xl font-semibold text-[var(--debit)]">
              {{ fmtMoney(data.debitTotal) }}
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardContent class="pt-5">
            <p class="text-xs text-muted-foreground">贷方合计</p>
            <p class="num mt-1 text-xl font-semibold text-[var(--credit)]">
              {{ fmtMoney(data.creditTotal) }}
            </p>
          </CardContent>
        </Card>
        <Card :class="data.balanced ? '' : 'border-[var(--loss)]/60'">
          <CardContent class="pt-5">
            <p class="flex items-center gap-1.5 text-xs text-muted-foreground">
              <component
                :is="data.balanced ? CheckCircle2 : AlertTriangle"
                class="size-3.5"
                :class="data.balanced ? 'text-[var(--profit)]' : 'text-[var(--loss)]'"
              />
              试算
            </p>
            <p
              class="mt-1 text-xl font-semibold"
              :class="data.balanced ? 'text-[var(--profit)]' : 'text-[var(--loss)]'"
            >{{ data.balanced ? '借贷平衡' : '借贷不平' }}</p>
          </CardContent>
        </Card>
      </div>

      <!-- 需要留意的事：草稿、冲销、空档日 -->
      <Card
        v-if="data.draftCount || data.gapDays || data.reversalCount"
        class="border-[var(--warn)]/40"
      >
        <CardContent class="flex flex-wrap gap-x-6 gap-y-2 pt-5 text-sm">
          <span v-if="data.draftCount" class="flex items-center gap-1.5">
            <FileText class="size-4 text-[var(--warn)]" />
            <b>{{ data.draftCount }}</b> 张草稿未记账，<span class="text-muted-foreground">未计入本表</span>
            <router-link to="/vouchers" class="text-[var(--debit)] underline">去处理</router-link>
          </span>
          <span v-if="data.gapDays" class="flex items-center gap-1.5">
            <CalendarX2 class="size-4 text-[var(--warn)]" />
            <b>{{ data.gapDays }}</b> 天一张凭证都没有
          </span>
          <span v-if="data.reversalCount" class="flex items-center gap-1.5">
            <AlertTriangle class="size-4 text-muted-foreground" />
            其中 <b>{{ data.reversalCount }}</b> 张是红字冲销凭证
          </span>
        </CardContent>
      </Card>

      <!-- 三段 -->
      <Card>
        <CardHeader>
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div>
              <CardTitle>凭证汇总表</CardTitle>
              <CardDescription>
                {{ data.from }} 至 {{ data.to }} · {{ currentHint }}
              </CardDescription>
            </div>
            <div class="flex gap-1 rounded-lg border p-0.5 no-print">
              <button
                v-for="t in TABS" :key="t.key"
                class="flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm transition-colors"
                :class="tab === t.key
                  ? 'bg-muted font-medium'
                  : 'text-muted-foreground hover:bg-muted/50'"
                @click="tab = t.key"
              >
                <component :is="t.icon" class="size-3.5" />
                {{ t.label }}
              </button>
            </div>
          </div>
        </CardHeader>
        <CardContent class="px-0">
          <!-- 一、按凭证字 -->
          <div v-if="tab === 'word'" class="overflow-auto">
            <EmptyState
              v-if="sections.word.length === 0"
              title="这一期间没有记账凭证"
              description="草稿不计入汇总；如有草稿请先记账"
            />
            <table v-else class="w-full text-sm">
              <thead>
                <tr class="border-b bg-muted/40">
                  <th class="h-9 px-4 text-left text-xs font-medium text-muted-foreground">凭证字</th>
                  <th class="h-9 px-4 text-right text-xs font-medium text-muted-foreground">张数</th>
                  <th class="h-9 px-4 text-right text-xs font-medium text-muted-foreground">借方金额</th>
                  <th class="h-9 px-4 text-right text-xs font-medium text-muted-foreground">贷方金额</th>
                  <th class="h-9 px-4 text-left text-xs font-medium text-muted-foreground">占比</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in sections.word" :key="r.word" class="border-b hover:bg-accent/30">
                  <td class="px-4 py-2">
                    <Badge variant="muted">{{ r.label }}</Badge>
                  </td>
                  <td class="num px-4 py-2 text-right">{{ r.count }}</td>
                  <td class="num px-4 py-2 text-right">{{ fmtMoney(r.debit) }}</td>
                  <td class="num px-4 py-2 text-right">{{ fmtMoney(r.credit) }}</td>
                  <td class="px-4 py-2">
                    <div class="flex items-center gap-2">
                      <div class="h-2 w-32 overflow-hidden rounded-full bg-muted">
                        <div
                          class="h-full bg-[var(--debit)]"
                          :style="{ width: pct(r.count, data.voucherCount) }"
                        />
                      </div>
                      <span class="text-xs text-muted-foreground">
                        {{ pctText(r.count, data.voucherCount) }}
                      </span>
                    </div>
                  </td>
                </tr>
              </tbody>
              <tfoot>
                <tr class="border-t-2 bg-muted/30 font-medium">
                  <td class="px-4 py-2">合计</td>
                  <td class="num px-4 py-2 text-right">{{ data.voucherCount }}</td>
                  <td class="num px-4 py-2 text-right">{{ fmtMoney(data.debitTotal) }}</td>
                  <td class="num px-4 py-2 text-right">{{ fmtMoney(data.creditTotal) }}</td>
                  <td />
                </tr>
              </tfoot>
            </table>
          </div>

          <!-- 二、按日期 -->
          <div v-else-if="tab === 'day'" class="overflow-auto">
            <EmptyState
              v-if="sections.day.length === 0"
              title="这一期间没有记账凭证"
              description="草稿不计入汇总；如有草稿请先记账"
            />
            <table v-else class="w-full text-sm">
              <thead>
                <tr class="border-b bg-muted/40">
                  <th class="h-9 px-4 text-left text-xs font-medium text-muted-foreground">日期</th>
                  <th class="h-9 px-4 text-right text-xs font-medium text-muted-foreground">张数</th>
                  <th class="h-9 px-4 text-right text-xs font-medium text-muted-foreground">借方金额</th>
                  <th class="h-9 px-4 text-right text-xs font-medium text-muted-foreground">贷方金额</th>
                  <th class="h-9 px-4 text-left text-xs font-medium text-muted-foreground">凭证号</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in sections.day" :key="r.date" class="border-b hover:bg-accent/30">
                  <td class="px-4 py-2 whitespace-nowrap">{{ r.date }}</td>
                  <td class="num px-4 py-2 text-right">{{ r.count }}</td>
                  <td class="num px-4 py-2 text-right">{{ fmtMoney(r.debit) }}</td>
                  <td class="num px-4 py-2 text-right">{{ fmtMoney(r.credit) }}</td>
                  <td class="px-4 py-2 font-mono text-[11px] text-muted-foreground">
                    {{ r.nos.join(' ') }}
                  </td>
                </tr>
              </tbody>
              <tfoot>
                <tr class="border-t-2 bg-muted/30 font-medium">
                  <td class="px-4 py-2">合计</td>
                  <td class="num px-4 py-2 text-right">{{ data.voucherCount }}</td>
                  <td class="num px-4 py-2 text-right">{{ fmtMoney(data.debitTotal) }}</td>
                  <td class="num px-4 py-2 text-right">{{ fmtMoney(data.creditTotal) }}</td>
                  <td />
                </tr>
              </tfoot>
            </table>
          </div>

          <!-- 三、按科目 -->
          <div v-else class="overflow-auto">
            <EmptyState
              v-if="sections.account.length === 0"
              title="这一期间没有发生额"
              description="没有任何科目在本期被记账"
            />
            <table v-else class="w-full text-sm">
              <thead>
                <tr class="border-b bg-muted/40">
                  <th class="h-9 px-4 text-left text-xs font-medium text-muted-foreground">科目</th>
                  <th class="h-9 px-4 text-left text-xs font-medium text-muted-foreground">名称</th>
                  <th class="h-9 px-4 text-right text-xs font-medium text-muted-foreground">笔数</th>
                  <th class="h-9 px-4 text-right text-xs font-medium text-muted-foreground">借方发生额</th>
                  <th class="h-9 px-4 text-right text-xs font-medium text-muted-foreground">贷方发生额</th>
                  <th class="h-9 px-4 text-right text-xs font-medium text-muted-foreground">净额</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in sections.account" :key="r.accountCode" class="border-b hover:bg-accent/30">
                  <td class="px-4 py-2 font-mono text-xs text-muted-foreground">{{ r.accountCode }}</td>
                  <td class="px-4 py-2">{{ r.accountName }}</td>
                  <td class="num px-4 py-2 text-right text-muted-foreground">{{ r.count }}</td>
                  <td class="num px-4 py-2 text-right">{{ fmtMoney(r.debit) }}</td>
                  <td class="num px-4 py-2 text-right">{{ fmtMoney(r.credit) }}</td>
                  <td class="num px-4 py-2 text-right font-medium">
                    {{ fmtMoney(r.net) }}
                    <span class="ml-1 text-xs text-muted-foreground">{{ r.dir }}</span>
                  </td>
                </tr>
              </tbody>
              <tfoot>
                <tr class="border-t-2 bg-muted/30 font-medium">
                  <td class="px-4 py-2" colspan="3">合计</td>
                  <td class="num px-4 py-2 text-right">{{ fmtMoney(data.debitTotal) }}</td>
                  <td class="num px-4 py-2 text-right">{{ fmtMoney(data.creditTotal) }}</td>
                  <td class="num px-4 py-2 text-right">
                    {{ fmtMoney(data.debitTotal - data.creditTotal) }}
                  </td>
                </tr>
              </tfoot>
            </table>
          </div>
        </CardContent>
      </Card>

      <!-- 三段必须相等：这是这张表能拿去对账的资格 -->
      <Card>
        <CardContent class="pt-5 text-sm text-muted-foreground">
          三个角度的合计必须完全相等 —— 按凭证字 {{ data.voucherCount }} 张、
          按日期 {{ data.dayRows.reduce((a, r) => a + r.count, 0) }} 张，
          借方合计都是 <span class="num font-medium">{{ fmtMoney(data.debitTotal) }}</span>。
          任何一处漏算或重复算，这组等式立刻就破，所以这张表可以拿去核对总账。
        </CardContent>
      </Card>
    </template>
  </div>
</template>

