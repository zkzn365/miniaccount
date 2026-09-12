<script setup>
import { onMounted, ref } from 'vue'
import { Search, Printer } from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
import { fmtMoney } from '@/lib/format'
import Card from '@/components/ui/Card.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Badge from '@/components/ui/Badge.vue'
import Table from '@/components/ui/Table.vue'
import Th from '@/components/ui/Th.vue'
import Td from '@/components/ui/Td.vue'
import Spinner from '@/components/ui/Spinner.vue'
import EmptyState from '@/components/ui/EmptyState.vue'

const form = ref({ accountPrefix: '1002', from: '', to: '' })

// 日记账快捷入口。
//
// 「现金日记账 / 银行存款日记账」在数据上与明细账完全是同一张表
// —— 同一个科目、同样的逐笔登记与余额累计。差别只在科目是 1001 / 1002，
// 以及出纳更关心「对方科目」那一列（这笔钱从哪来、到哪去）。
// 所以不另做一套报表，给两个按钮就够了：同一个实现、不会有两份口径。
const DIARIES = [
  { code: '1001', name: '库存现金日记账' },
  { code: '1002', name: '银行存款日记账' },
  { code: '1012', name: '其他货币资金日记账' },
]

function openDiary(code) {
  form.value.accountPrefix = code
  load()
}
const data = ref(null)
const loading = ref(false)

async function load() {
  if (!form.value.accountPrefix.trim()) { notify('请填写科目编码前缀', 'warn'); return }
  loading.value = true
  const r = await api.ledgerDetail({
    accountPrefix: form.value.accountPrefix.trim(),
    from: form.value.from,
    to: form.value.to,
  })
  loading.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); data.value = null; return }
  data.value = r.data
}
onMounted(load)

// 打印走系统对话框（内含「另存为 PDF」），样式表里的 @media print
// 负责把界面还原成一张纸的报表。
const printPage = () => window.print()

const dirVariant = (d) => (d === '借' ? 'debit' : d === '贷' ? 'credit' : 'muted')
</script>

<template>
  <div class="flex flex-col gap-4">
    <Card>
      <CardContent class="flex items-end gap-3 pt-5">
        <div class="w-44">
          <Label>科目编码前缀</Label>
          <Input v-model="form.accountPrefix" class="mt-1.5" placeholder="1002" />
          <p class="mt-1 text-[11px] text-muted-foreground">
            前缀即可，如 2241 会列出全部「其他应付款」明细
          </p>
        </div>
        <div class="w-36">
          <Label>起始日期</Label>
          <Input v-model="form.from" class="mt-1.5" placeholder="留空=账套起始" />
        </div>
        <div class="w-36">
          <Label>截止日期</Label>
          <Input v-model="form.to" class="mt-1.5" placeholder="留空=账套结束" />
        </div>
        <Button :disabled="loading" @click="load"><Search /> 查询</Button>
        <div class="flex items-center gap-1.5">
          <span class="text-xs text-muted-foreground">日记账：</span>
          <Button
            v-for="d in DIARIES" :key="d.code"
            variant="outline" size="sm" :title="d.name"
            @click="openDiary(d.code)"
          >{{ d.name.replace('日记账', '') }}</Button>
        </div>
        <Button v-if="data?.rows?.length" variant="outline" @click="printPage">
          <Printer /> 打印 / PDF
        </Button>
      </CardContent>
    </Card>

    <Card>
      <CardContent class="pt-5">
        <div class="mb-3 flex items-baseline justify-between">
          <div>
            <h2 class="text-base font-semibold">
              明细账
              <span v-if="data?.accountName" class="ml-2 text-sm font-normal text-muted-foreground">
                {{ data.accountPrefix }} {{ data.accountName }}
              </span>
            </h2>
            <p class="text-xs text-muted-foreground">
              {{ data?.from }} ~ {{ data?.to }} · 余额逐行累计 ·
              「对方科目」是同一张凭证里其他分录所在的科目：看日记账时
              真正要回答的是「这笔钱从哪来、到哪去」
            </p>
          </div>
          <div v-if="data" class="flex items-center gap-3 text-sm">
            <span class="text-muted-foreground">期初</span>
            <span class="num font-medium">{{ fmtMoney(data.openingBalance) }}</span>
            <span class="text-muted-foreground">期末</span>
            <span class="num font-semibold">{{ fmtMoney(data.closingBalance) }}</span>
          </div>
        </div>

        <Spinner v-if="loading" />
        <EmptyState
          v-else-if="!data || data.rows.length === 0"
          title="该科目在这段区间内没有发生额"
          description="换个科目前缀或放宽日期区间试试"
        />
        <div v-else class="overflow-auto">
          <Table>
            <thead>
              <tr class="border-b bg-muted/40">
                <Th>日期</Th><Th>凭证号</Th><Th>摘要</Th>
                <Th>对方科目</Th>
                <Th align="right">借方</Th><Th align="right">贷方</Th>
                <Th align="right">方向</Th><Th align="right">余额</Th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(r, i) in data.rows" :key="i" class="border-b last:border-0 hover:bg-accent/30">
                <Td class="whitespace-nowrap text-xs">{{ r.date }}</Td>
                <Td class="whitespace-nowrap font-mono text-xs">{{ r.voucher }}</Td>
                <Td>{{ r.summary }}</Td>
                <Td class="max-w-52 truncate text-xs text-muted-foreground" :title="r.contra">
                  {{ r.contra || '—' }}
                </Td>
                <Td align="right" class="text-[var(--debit)]">{{ fmtMoney(r.debit, { blankZero: true }) }}</Td>
                <Td align="right" class="text-[var(--credit)]">{{ fmtMoney(r.credit, { blankZero: true }) }}</Td>
                <Td align="right"><Badge :variant="dirVariant(r.dir)">{{ r.dir }}</Badge></Td>
                <Td align="right" class="font-medium">{{ fmtMoney(r.balance) }}</Td>
              </tr>
            </tbody>
          </Table>
          <p v-if="data.truncated" class="mt-2 text-xs text-[var(--warn)]">
            行数已达上限，结果被截断 —— 请缩小日期区间
          </p>
        </div>
      </CardContent>
    </Card>
  </div>
</template>
