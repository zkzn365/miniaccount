<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  TrendingUp, TrendingDown, Wallet, Landmark, AlertTriangle,
  FileClock, ArrowRight, CheckCircle2,
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
import Spinner from '@/components/ui/Spinner.vue'
import EmptyState from '@/components/ui/EmptyState.vue'

const router = useRouter()
const data = ref(null)
const loading = ref(true)

async function load() {
  loading.value = true
  const r = await api.overview()
  loading.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  data.value = r.data
}
onMounted(load)
defineExpose({ reload: load })

const balanced = computed(() => {
  const d = data.value
  if (!d) return true
  return d.assets === d.liabilities + d.equity
})

// 待办事项。首页最重要的职责不是「展示数字」，而是
// 「告诉会计现在该干什么」—— 数字在报表页随时能看。
const todos = computed(() => {
  const d = data.value
  if (!d) return []
  const out = []
  if (d.draftVouchers > 0) {
    out.push({
      key: 'draft', tone: 'warn', icon: FileClock,
      title: `${d.draftVouchers} 张草稿凭证待过账`,
      detail: '草稿不占凭证号，也不进总账 —— 有草稿说明有没做完的录入',
      to: { path: '/reports' },
    })
  }
  if (d.unpostedBankFlows > 0) {
    out.push({
      key: 'bank', tone: 'info', icon: Landmark,
      title: `${d.unpostedBankFlows} 条银行流水未生成凭证`,
      detail: '导入的流水需要匹配对方科目后才能记账',
      to: { path: '/ai' },
    })
  }
  if (!balanced.value) {
    out.push({
      key: 'bs', tone: 'error', icon: AlertTriangle,
      title: '资产负债表勾稽不成立',
      detail: `资产 ${fmtMoney(d.assets)} ≠ 负债 ${fmtMoney(d.liabilities)} + 权益 ${fmtMoney(d.equity)}`,
      to: { path: '/reports', query: { kind: 'bs' } },
    })
  }
  return [...(d.balanceSheetIssues ?? []).map((t, i) => ({
    key: `iss${i}`, tone: 'error', icon: AlertTriangle, title: '报表勾稽问题', detail: t,
    to: { path: '/reports', query: { kind: 'bs' } },
  })), ...out]
})

function pct(part, whole) {
  if (!whole) return 0
  return Math.max(0, Math.min(100, Math.round((part / whole) * 100)))
}
</script>

<template>
  <Spinner v-if="loading" />
  <EmptyState
    v-else-if="!data"
    title="读不到账套数据"
    description="请确认账套文件可读，或重新打开一个账套"
  >
    <Button variant="outline" size="sm" @click="load">重试</Button>
  </EmptyState>

  <div v-else class="flex flex-col gap-5">
    <!-- 会计恒等式：首页第一眼必须是这个。
         资产 = 负债 + 所有者权益 是会计的地基，
         它不成立的时候，下面所有数字都没有意义。 -->
    <Card :class="balanced ? '' : 'border-destructive/50'">
      <CardContent class="pt-5">
        <div class="flex items-center justify-between gap-4">
          <div>
            <p class="text-xs text-muted-foreground">会计恒等式</p>
            <p class="mt-1 flex items-baseline gap-2 text-sm">
              <span class="text-muted-foreground">资产</span>
              <span class="num text-lg font-semibold">{{ fmtMoney(data.assets) }}</span>
              <span class="text-muted-foreground">=</span>
              <span class="text-muted-foreground">负债</span>
              <span class="num font-medium">{{ fmtMoney(data.liabilities) }}</span>
              <span class="text-muted-foreground">+</span>
              <span class="text-muted-foreground">所有者权益</span>
              <span class="num font-medium">{{ fmtMoney(data.equity) }}</span>
            </p>
          </div>
          <Badge :variant="balanced ? 'profit' : 'loss'">
            <CheckCircle2 v-if="balanced" class="mr-1 size-3" />
            <AlertTriangle v-else class="mr-1 size-3" />
            {{ balanced ? '勾稽成立' : '勾稽不成立' }}
          </Badge>
        </div>

        <!-- 资产构成的直观比例：数字之外给一个「一眼看懂」的视角 -->
        <div class="mt-4 flex h-2 overflow-hidden rounded-full bg-muted">
          <div class="bg-[var(--debit)]" :style="{ width: pct(data.liabilities, data.assets) + '%' }" />
          <div class="bg-[var(--profit)]" :style="{ width: pct(data.equity, data.assets) + '%' }" />
        </div>
        <div class="mt-2 flex gap-4 text-xs text-muted-foreground">
          <span class="flex items-center gap-1.5">
            <i class="size-2 rounded-full bg-[var(--debit)]" />
            负债占比 {{ pct(data.liabilities, data.assets) }}%
          </span>
          <span class="flex items-center gap-1.5">
            <i class="size-2 rounded-full bg-[var(--profit)]" />
            权益占比 {{ pct(data.equity, data.assets) }}%
          </span>
        </div>
      </CardContent>
    </Card>

    <!-- 关键指标 -->
    <div class="grid grid-cols-4 gap-4">
      <Card>
        <CardContent class="pt-5">
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <TrendingUp class="size-3.5" /> 本期收入
          </div>
          <p class="num mt-2 text-xl font-semibold">{{ fmtMoney(data.periodIncome) }}</p>
        </CardContent>
      </Card>
      <Card>
        <CardContent class="pt-5">
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <TrendingDown class="size-3.5" /> 本期费用
          </div>
          <p class="num mt-2 text-xl font-semibold">{{ fmtMoney(data.periodExpense) }}</p>
        </CardContent>
      </Card>
      <Card>
        <CardContent class="pt-5">
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <Wallet class="size-3.5" /> 本期利润
          </div>
          <p
            class="num mt-2 text-xl font-semibold"
            :class="data.periodProfit < 0 ? 'text-[var(--loss)]' : 'text-[var(--profit)]'"
          >{{ fmtMoney(data.periodProfit) }}</p>
        </CardContent>
      </Card>
      <Card>
        <CardContent class="pt-5">
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <Landmark class="size-3.5" /> 期末现金
          </div>
          <p class="num mt-2 text-xl font-semibold">{{ fmtMoney(data.closingCash) }}</p>
        </CardContent>
      </Card>
    </div>

    <!-- 待办 -->
    <Card>
      <CardHeader>
        <CardTitle>待办事项</CardTitle>
        <CardDescription>
          当前账期 {{ data.currentPeriod || '—' }}
          <template v-if="data.latestClosedPeriod">，最近已结账 {{ data.latestClosedPeriod }}</template>
        </CardDescription>
      </CardHeader>
      <CardContent>
        <div v-if="todos.length === 0" class="flex items-center gap-2 py-4 text-sm text-muted-foreground">
          <CheckCircle2 class="size-4 text-[var(--profit)]" />
          没有待办 —— 账是干净的
        </div>
        <div v-else class="flex flex-col divide-y">
          <button
            v-for="t in todos"
            :key="t.key"
            class="flex items-center gap-3 py-3 text-left transition-colors hover:bg-accent/40"
            @click="router.push(t.to)"
          >
            <component
              :is="t.icon"
              class="size-4 shrink-0"
              :class="t.tone === 'error' ? 'text-[var(--loss)]'
                : t.tone === 'warn' ? 'text-[var(--warn)]' : 'text-muted-foreground'"
            />
            <div class="min-w-0 flex-1">
              <p class="text-sm font-medium">{{ t.title }}</p>
              <p class="truncate text-xs text-muted-foreground">{{ t.detail }}</p>
            </div>
            <ArrowRight class="size-4 shrink-0 text-muted-foreground" />
          </button>
        </div>
      </CardContent>
    </Card>
  </div>
</template>
