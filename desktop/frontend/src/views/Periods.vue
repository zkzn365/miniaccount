<script setup>
import { computed, onMounted, ref } from 'vue'
import {
  CheckCircle2, AlertTriangle, Info, Lock, Unlock, Eye, ShieldCheck,
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
import Table from '@/components/ui/Table.vue'
import Th from '@/components/ui/Th.vue'
import Td from '@/components/ui/Td.vue'
import Spinner from '@/components/ui/Spinner.vue'

const book = ref(null)
const loading = ref(true)
// 记账人来自本机设置（见 lib/operator.js）：全程序一份，不再各页各存一份
const operator = bookkeeper

const preview = ref(null)
const health = ref(null)
const dialog = ref(null) // 'close' | 'reopen' | 'health'
const busy = ref(false)

// ★ 弹窗针对的是**哪一个期间**，单独记一份。
//
// 原来 doClose / doReopen 从 preview 上取 year/month，而
// `ClosingPreview` 里根本没有这两个字段（它只有 `period` 字符串）——
// 于是取到 undefined，JSON.stringify 又把 undefined 的键丢掉，
// Go 侧收到的是零值：
//
//     会计期间 0000-00 非法
//
// 结账和反结账因此**完全不可用**，而且报错信息指向的是「0000-00」，
// 看不出问题出在「字段没传过去」。
//
// 期间是打开弹窗那一刻选定的，就应当由界面自己拿着 ——
// 不要指望后端返回的预览对象顺带带着它。
const target = ref({ year: 0, month: 0, label: '' })

async function load() {
  loading.value = true
  const r = await api.periods()
  loading.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  book.value = r.data
}
onMounted(async () => {
  // 记账人是本机设置（全程序一份），与页面数据一起加载
  await Promise.all([load(), loadBookkeeper()])
})

const periods = computed(() => book.value?.periods ?? [])

function tone(p) {
  if (p.status === 'closed') return 'muted'
  if (p.status === 'open') return 'profit'
  return 'outline'
}

async function openClose(p) {
  dialog.value = 'close'
  target.value = { year: p.year, month: p.month, label: p.label }
  preview.value = null
  health.value = null
  const [pv, h] = await Promise.all([
    api.previewClose({ year: p.year, month: p.month }),
    api.health({ year: p.year, month: p.month }),
  ])
  if (pv.ok) preview.value = pv.data
  else notify(pv.fault.message, 'error', pv.fault.detail)
  if (h.ok) health.value = h.data
}

async function openReopen(p) {
  dialog.value = 'reopen'
  target.value = { year: p.year, month: p.month, label: p.label }
  preview.value = { period: p.label, reversed: [], steps: [] }
  const h = await api.health({ year: p.year, month: p.month })
  health.value = h.ok ? h.data : null
}

async function openHealth(p) {
  dialog.value = 'health'
  health.value = null
  const h = await api.health({ year: p.year, month: p.month })
  if (h.ok) health.value = h.data
  else notify(h.fault.message, 'error', h.fault.detail)
}

async function doClose() {
  if (!operator.value.trim()) { notify('请先填写操作人 —— 记账凭证需要有记账签章', 'warn'); return }
  localStorage.setItem('operator', operator.value.trim())
  busy.value = true
  const r = await api.close({
    year: target.value.year, month: target.value.month,
    by: operator.value.trim(),
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  // ★ 结账会先把本期草稿全部过账 —— 这是账套里唯一的过账时机，
  // 所以必须报出张数：「结账成功」与「结账成功，顺便把 12 张草稿
  // 记进了账」是两件事，后者用户得知道。
  const posted = r.data.postedDrafts > 0
    ? `，已过账本期 ${r.data.postedDrafts} 张草稿` : ''
  const closing = r.data.voucherCreated ? `，结转凭证 ${r.data.voucherNo}` : '（本期无损益）'
  notify(`已结账 ${r.data.period}${posted}${closing}`, 'success')
  dialog.value = null
  await load()
}

async function doReopen() {
  if (!operator.value.trim()) { notify('请先填写操作人', 'warn'); return }
  busy.value = true
  const r = await api.reopen({
    year: target.value.year, month: target.value.month,
    by: operator.value.trim(),
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  const n = r.data.reversed?.length ?? 0
  notify(n > 0
    ? `${r.data.period} 已反结账，红字冲销 ${r.data.reversed.join('、')}`
    : `${r.data.period} 已反结账（该期没有结转凭证）`, 'success')
  dialog.value = null
  await load()
}

const levelIcon = { ok: CheckCircle2, warn: AlertTriangle, error: AlertTriangle }
const levelClass = {
  ok: 'text-[var(--profit)]',
  warn: 'text-[var(--warn)]',
  error: 'text-[var(--loss)]',
}
</script>

<template>
  <Spinner v-if="loading" />
  <div v-else-if="book" class="flex flex-col gap-5">
    <Card>
      <CardHeader>
        <CardTitle>{{ book.companyName }}</CardTitle>
        <CardDescription>
          {{ book.taxTypeLabel }} · 启用期间 {{ book.startPeriod }}
          <template v-if="book.creditCode"> · {{ book.creditCode }}</template>
        </CardDescription>
      </CardHeader>
      <CardContent>
        <div class="flex items-end gap-3">
          <div class="w-64">
            <Label>操作人</Label>
            <Input v-model="operator"
                 @change="rememberBookkeeper(operator)" class="mt-1.5" placeholder="结账 / 反结账时签名用" />
          </div>
          <p class="pb-2 text-xs text-muted-foreground">
            《会计法》要求记账凭证有记账签章，因此结账必须记名
          </p>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>会计期间</CardTitle>
        <CardDescription>
          结账必须按顺序进行；反结账必须从最后一个月往前。
          已结账的期间不能再记凭证。
        </CardDescription>
      </CardHeader>
      <CardContent class="px-0">
        <Table>
          <thead>
            <tr class="border-b">
              <Th class="pl-5">期间</Th>
              <Th>起止日期</Th>
              <Th>状态</Th>
              <Th align="right">凭证数</Th>
              <Th class="pr-5" align="right">操作</Th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in periods" :key="p.label" class="border-b last:border-0 hover:bg-accent/30">
              <Td class="pl-5 font-medium">{{ p.label }}</Td>
              <Td class="text-xs text-muted-foreground">{{ p.from }} ~ {{ p.to }}</Td>
              <Td><Badge :variant="tone(p)">{{ p.statusLabel }}</Badge></Td>
              <Td align="right">{{ p.voucherCount }}</Td>
              <Td class="pr-5">
                <div class="flex justify-end gap-1">
                  <Button variant="ghost" size="sm" title="结账前体检" @click="openHealth(p)">
                    <ShieldCheck /> 体检
                  </Button>
                  <Button
                    v-if="p.canClose" variant="outline" size="sm" @click="openClose(p)"
                  >
                    <Lock /> 结账
                  </Button>
                  <Button
                    v-if="p.canReopen" variant="outline" size="sm" @click="openReopen(p)"
                  >
                    <Unlock /> 反结账
                  </Button>
                </div>
              </Td>
            </tr>
          </tbody>
        </Table>
      </CardContent>
    </Card>
  </div>

  <!-- 结账 / 反结账 / 体检 共用同一个弹窗 -->
  <Modal
    v-model:open="dialog"
    :title="dialog === 'close' ? '结账' : dialog === 'reopen' ? '反结账' : '结账前体检'"
    :description="dialog === 'reopen'
      ? '反结账会用红字凭证冲销该期的结转凭证，而不是删除它 —— 凭证号不断号，轨迹可追溯。'
      : '确认前请先看清这一步会写哪些分录。'"
  >
    <div v-if="preview" class="flex flex-col gap-5">
      <!-- 结账步骤 -->
      <div v-if="preview.steps?.length" class="flex flex-col gap-2">
        <p class="text-sm font-medium">这一步会做什么</p>
        <div v-for="s in preview.steps" :key="s.key" class="flex items-start gap-2 text-sm">
          <span
            class="mt-0.5 grid size-4 shrink-0 place-items-center rounded-full text-xs"
            :class="s.skipped ? 'bg-muted text-muted-foreground' : 'bg-[var(--profit)]/15 text-[var(--profit)]'"
          >{{ s.skipped ? '—' : '✓' }}</span>
          <div>
            <p :class="s.skipped ? 'text-muted-foreground' : ''">{{ s.title }}</p>
            <p class="text-xs text-muted-foreground">{{ s.detail }}</p>
          </div>
        </div>
      </div>

      <!-- 本期草稿：结账会顺手把它们过账 -->
      <div
        v-if="dialog === 'close' && preview.draftCount > 0"
        class="rounded-lg border border-[var(--warn)]/40 bg-[var(--warn)]/5 p-3 text-sm"
      >
        <p class="font-medium">结账会先把本期 {{ preview.draftCount }} 张草稿过账</p>
        <p class="mt-1 text-xs text-muted-foreground">
          凭证录入后只存草稿（不占凭证号、不进总账），过账只发生在账期结算。
          点「确认结账」会按业务日期顺序过账、自动分配凭证号，然后才结转损益、关期间。
        </p>
        <ul v-if="preview.draftSamples?.length" class="mt-2 flex flex-col gap-0.5">
          <li v-for="(d, i) in preview.draftSamples" :key="i"
              class="text-xs text-muted-foreground">· {{ d }}</li>
          <li v-if="preview.draftCount > preview.draftSamples.length"
              class="text-xs text-muted-foreground">
            · …等共 {{ preview.draftCount }} 张
          </li>
        </ul>
      </div>

      <!-- 损益概览 -->
      <div v-if="preview.income || preview.expense" class="grid grid-cols-3 gap-3">
        <div class="rounded-lg border p-3">
          <p class="text-xs text-muted-foreground">本期收入</p>
          <p class="num mt-1 font-medium">{{ fmtMoney(preview.income) }}</p>
        </div>
        <div class="rounded-lg border p-3">
          <p class="text-xs text-muted-foreground">本期费用</p>
          <p class="num mt-1 font-medium">{{ fmtMoney(preview.expense) }}</p>
        </div>
        <div class="rounded-lg border p-3">
          <p class="text-xs text-muted-foreground">本期利润</p>
          <p
            class="num mt-1 font-semibold"
            :class="preview.profit < 0 ? 'text-[var(--loss)]' : 'text-[var(--profit)]'"
          >{{ fmtMoney(preview.profit) }}</p>
        </div>
      </div>

      <!-- 将写入的分录 -->
      <div v-if="preview.entries?.length">
        <p class="mb-2 text-sm font-medium">将写入的结转分录</p>
        <div class="rounded-lg border">
          <Table>
            <thead>
              <tr class="border-b bg-muted/40">
                <Th>科目</Th><Th>摘要</Th>
                <Th align="right">借方</Th><Th align="right">贷方</Th><Th>辅助核算</Th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(e, i) in preview.entries" :key="i" class="border-b last:border-0">
                <Td class="font-mono text-xs">{{ e.accountCode }}</Td>
                <Td class="text-xs">{{ e.summary }}</Td>
                <Td align="right" class="text-[var(--debit)]">{{ fmtMoney(e.debit, { blankZero: true }) }}</Td>
                <Td align="right" class="text-[var(--credit)]">{{ fmtMoney(e.credit, { blankZero: true }) }}</Td>
                <Td class="text-xs text-muted-foreground">{{ e.auxDesc }}</Td>
              </tr>
            </tbody>
          </Table>
        </div>
      </div>

      <!-- 体检 -->
      <div v-if="health">
        <p class="mb-2 text-sm font-medium">
          结账前体检
          <span class="ml-2 font-normal text-muted-foreground">{{ health.summary }}</span>
        </p>
        <div class="flex flex-col gap-1.5">
          <div v-for="it in health.items" :key="it.key" class="flex items-start gap-2 text-sm">
            <component
              :is="levelIcon[it.level]" class="mt-0.5 size-3.5 shrink-0"
              :class="levelClass[it.level]"
            />
            <div class="min-w-0">
              <span :class="it.level === 'error' ? 'text-[var(--loss)]' : ''">{{ it.title }}</span>
              <span v-if="it.detail" class="ml-2 text-xs text-muted-foreground">{{ it.detail }}</span>
            </div>
          </div>
        </div>
        <div
          v-if="!health.canClose"
          class="mt-3 flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm"
        >
          <AlertTriangle class="mt-0.5 size-4 shrink-0 text-[var(--loss)]" />
          <span>存在阻断项，不能结账。请先处理上面的红色项。</span>
        </div>
      </div>
    </div>

    <template #footer>
      <Button variant="ghost" @click="dialog = null">取消</Button>
      <Button
        v-if="dialog === 'close'"
        :disabled="busy || (health && !health.canClose)"
        @click="doClose"
      >
        <Lock /> 确认结账
      </Button>
      <Button v-else-if="dialog === 'reopen'" variant="destructive" :disabled="busy" @click="doReopen">
        <Unlock /> 确认反结账
      </Button>
    </template>
  </Modal>
</template>
