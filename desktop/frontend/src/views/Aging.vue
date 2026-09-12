<script setup>
import { computed, onMounted, ref } from 'vue'
import {
  RefreshCw, Printer, AlertTriangle, Download, ChevronRight, ChevronDown,
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
const filter = ref({ asOf: '', accountPrefix: '' })
const expanded = ref({})

async function load() {
  loading.value = true
  const r = await api.agingReport({
    asOf: filter.value.asOf,
    accountPrefix: filter.value.accountPrefix,
  })
  loading.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); data.value = null; return }
  data.value = r.data
}
onMounted(load)

function toggle(id) { expanded.value[id] = !expanded.value[id] }

function printPage() { window.print() }

// 账龄越老越红：30 天以内正常、90 天以上要盯、一年以上基本是坏账
const bucketTone = (label) => {
  if (label.includes('1 年')) return 'loss'
  if (label.startsWith('91') || label.startsWith('181')) return 'warn'
  return 'muted'
}

const rows = computed(() => data.value?.rows ?? [])
const buckets = computed(() => data.value?.buckets ?? [])

// 只统计「有未结清」的行，避免空行干扰
const liveRows = computed(() => rows.value.filter((r) => !r.balance === 0))

function fmtPercent(p) {
  if (!p) return ''
  return `${p}%`
}
</script>

<template>
  <div class="flex flex-col gap-4">
    <div class="flex flex-wrap items-end gap-3 no-print">
      <div class="w-40">
        <Label>截止日期</Label>
        <Input v-model="filter.asOf" class="mt-1.5" placeholder="留空 = 当前账期期末" />
      </div>
      <div class="w-48">
        <Label>科目范围</Label>
        <select
          v-model="filter.accountPrefix"
          class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm"
        >
          <option value="">全部往来科目</option>
          <option value="1122">只看应收账款</option>
          <option value="2202">只看应付账款</option>
          <option value="1123">只看预付账款</option>
          <option value="2203">只看预收账款</option>
          <option value="2241">只看其他应付款</option>
        </select>
      </div>
      <Button :disabled="loading" @click="load">
        <RefreshCw :class="loading ? 'animate-spin' : ''" /> 生成
      </Button>
      <Button v-if="data" variant="outline" @click="printPage"><Printer /> 打印 / PDF</Button>
      <Button v-if="data" variant="outline" @click="$emit('export')"><Download /> 导出 Excel</Button>
    </div>

    <Spinner v-if="loading" />
    <EmptyState
      v-else-if="!data"
      title="还没有数据"
      description="账龄按「往来单位 × 科目」计算，需要先有带往来辅助核算的凭证"
    />

    <template v-else>
      <!-- 概览：会计最先要看的是「多少钱压了多久」 -->
      <div class="grid grid-cols-3 gap-4">
        <Card>
          <CardContent class="pt-5">
            <p class="text-xs text-muted-foreground">应收未结清</p>
            <p class="num mt-1 text-xl font-semibold text-[var(--debit)]">
              {{ fmtMoney(data.debitTotal) }}
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardContent class="pt-5">
            <p class="text-xs text-muted-foreground">应付未结清</p>
            <p class="num mt-1 text-xl font-semibold text-[var(--credit)]">
              {{ fmtMoney(data.creditTotal) }}
            </p>
          </CardContent>
        </Card>
        <Card :class="data.over90 > 0 ? 'border-[var(--warn)]/50' : ''">
          <CardContent class="pt-5">
            <p class="flex items-center gap-1.5 text-xs text-muted-foreground">
              <AlertTriangle v-if="data.over90 > 0" class="size-3.5 text-[var(--warn)]" />
              90 天以上
            </p>
            <p class="num mt-1 text-xl font-semibold"
               :class="data.over90 > 0 ? 'text-[var(--warn)]' : ''">
              {{ fmtMoney(data.over90) }}
            </p>
            <p class="text-[11px] text-muted-foreground">最该催的那部分</p>
          </CardContent>
        </Card>
      </div>

      <!-- 账龄分布 -->
      <Card>
        <CardHeader>
          <CardTitle>账龄分布</CardTitle>
          <CardDescription>
            {{ data.asOf }} · {{ data.summary }}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div class="flex h-3 overflow-hidden rounded-full bg-muted">
            <div
              v-for="(b, i) in buckets" :key="i"
              :style="{ width: (b.percent || 0) + '%' }"
              :class="[
                'transition-all',
                i === 0 ? 'bg-[var(--profit)]'
                  : i === 1 ? 'bg-[var(--debit)]'
                    : i === 2 ? 'bg-[var(--warn)]'
                      : i === 3 ? 'bg-[var(--warn)]/80'
                        : 'bg-[var(--loss)]',
              ]"
              :title="`${b.label}：${fmtMoney(b.amount)}（${b.percent}%）`"
            />
          </div>
          <div class="mt-3 grid grid-cols-6 gap-2">
            <div v-for="(b, i) in buckets" :key="i" class="rounded-lg border p-2">
              <p class="text-[11px] text-muted-foreground">{{ b.label }}</p>
              <p class="num mt-0.5 text-sm font-medium">{{ fmtMoney(b.amount) }}</p>
              <p class="text-[11px] text-muted-foreground">{{ fmtPercent(b.percent) }}</p>
            </div>
          </div>
        </CardContent>
      </Card>

      <!-- 明细 -->
      <Card>
        <CardHeader>
          <CardTitle>按往来单位</CardTitle>
          <CardDescription>
            账龄按<b>先进先出法核销</b>计算：每一笔收款依次冲抵最早的应收，
            冲掉的部分就不再计入账龄。点任意一行可以展开未结清明细。
          </CardDescription>
        </CardHeader>
        <CardContent class="px-0">
          <EmptyState
            v-if="rows.length === 0"
            title="没有往来余额"
            description="这个范围内没有带往来辅助核算的分录"
          />
          <div v-else class="overflow-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b bg-muted/40">
                  <th class="h-9 w-8 px-2" />
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">科目</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">往来单位</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">未结清余额</th>
                  <th
                    v-for="b in buckets" :key="b.label"
                    class="h-9 px-3 text-right text-xs font-medium text-muted-foreground whitespace-nowrap"
                  >{{ b.label }}</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">最老账龄</th>
                </tr>
              </thead>
              <tbody>
                <template v-for="r in rows" :key="r.accountCode + '-' + r.contactId">
                  <tr
                    class="cursor-pointer border-b hover:bg-accent/30"
                    @click="toggle(r.accountCode + '-' + r.contactId)"
                  >
                    <td class="px-2 py-1.5 text-muted-foreground">
                      <component
                        :is="expanded[r.accountCode + '-' + r.contactId] ? ChevronDown : ChevronRight"
                        class="size-3.5"
                      />
                    </td>
                    <td class="px-3 py-1.5">
                      <span class="font-mono text-xs text-muted-foreground">{{ r.accountCode }}</span>
                      <span class="ml-1.5">{{ r.accountName }}</span>
                    </td>
                    <td class="px-3 py-1.5 font-medium">{{ r.contactName }}</td>
                    <td class="num px-3 py-1.5 font-medium"
                        :class="r.balance < 0 ? 'text-[var(--credit)]' : ''">
                      {{ fmtMoney(r.balance) }}
                    </td>
                    <td
                      v-for="(b, i) in r.buckets" :key="i"
                      class="num px-3 py-1.5"
                      :class="b.amount && i >= 3 ? 'text-[var(--warn)] font-medium' : ''"
                    >{{ fmtMoney(b.amount, { blankZero: true }) }}</td>
                    <td class="num px-3 py-1.5">
                      <Badge v-if="r.maxDays" :variant="r.maxDays > 90 ? 'warn' : 'muted'">
                        {{ r.maxDays }} 天
                      </Badge>
                      <span v-else class="text-xs text-muted-foreground">—</span>
                    </td>
                  </tr>
                  <!-- 展开：未结清明细 -->
                  <tr v-if="expanded[r.accountCode + '-' + r.contactId]">
                    <td colspan="12" class="bg-muted/20 px-6 py-2">
                      <table class="w-full text-xs">
                        <thead>
                          <tr class="text-muted-foreground">
                            <th class="py-1 text-left font-medium">发生日期</th>
                            <th class="py-1 text-right font-medium">账龄</th>
                            <th class="py-1 text-right font-medium">未结清金额</th>
                            <th class="py-1 text-left font-medium">摘要</th>
                            <th class="py-1 text-left font-medium">凭证号</th>
                            <th class="py-1 text-left font-medium">区间</th>
                          </tr>
                        </thead>
                        <tbody>
                          <tr v-for="(it, k) in r.items" :key="k" class="border-t">
                            <td class="py-1">{{ it.date }}</td>
                            <td class="num py-1">{{ it.days }} 天</td>
                            <td class="num py-1 font-medium">{{ fmtMoney(it.amount) }}</td>
                            <td class="py-1">{{ it.summary }}</td>
                            <td class="py-1 font-mono">{{ it.voucherNo }}</td>
                            <td class="py-1">
                              <Badge :variant="bucketTone(it.bucketLabel)">{{ it.bucketLabel }}</Badge>
                            </td>
                          </tr>
                        </tbody>
                      </table>
                      <p v-if="r.items.length === 0" class="py-1 text-muted-foreground">
                        没有未结清明细（余额为零或为预收/预付）。
                      </p>
                    </td>
                  </tr>
                </template>
              </tbody>
            </table>
          </div>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
