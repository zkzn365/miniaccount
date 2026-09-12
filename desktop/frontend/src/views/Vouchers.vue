<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  Plus, Search, RefreshCw, Trash2, Undo2, FileText,
  Sparkles, Landmark, Paperclip, Pencil,
} from 'lucide-vue-next'
import { api, notify, DRAFT_HINT } from '@/lib/api'
import { bookkeeper, loadBookkeeper, rememberBookkeeper } from '@/lib/operator'
import { fmtMoney } from '@/lib/format'
import Card from '@/components/ui/Card.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Input from '@/components/ui/Input.vue'
import Modal from '@/components/ui/Modal.vue'
import Spinner from '@/components/ui/Spinner.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import VoucherEditor from '@/components/VoucherEditor.vue'

const route = useRoute()
const router = useRouter()

// ★ 过账只发生在账期结算，凭证页上没有任何「记账」按钮。
//
// 这条规则要跟用户说清楚（DRAFT_HINT 见 lib/api.js），
// 否则他会一直找那个按钮，或者以为草稿已经进账了。

const rows = ref([])
const periods = ref([])
const loading = ref(false)
const filter = ref({
  period: route.query.period ?? '',
  status: '',
  keyword: '',
})

const editorOpen = ref(false)
const editingId = ref(0)
const detail = ref(null)
const detailOpen = ref(false)
const reverseOpen = ref(false)
// 记账人来自本机设置（见 lib/operator.js）：全程序一份，不再各页各存一份
const operator = bookkeeper

async function loadPeriods() {
  const r = await api.periods()
  if (!r.ok) return
  periods.value = r.data.periods
  if (!filter.value.period) {
    const cur = periods.value.find((p) => p.status === 'open')
    filter.value.period = cur?.label ?? periods.value[0]?.label ?? ''
  }
}

async function load() {
  loading.value = true
  const [y, m] = (filter.value.period || '-').split('-')
  const r = await api.vouchers({
    year: Number(y) || 0,
    month: Number(m) || 0,
    status: filter.value.status,
    keyword: filter.value.keyword,
  })
  loading.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); rows.value = []; return }
  rows.value = r.data ?? []
}

onMounted(async () => {
  // 记账人是本机设置（全程序一份），与页面数据一起加载
  await loadPeriods()
  await Promise.all([load(), loadBookkeeper()])
})
watch(() => [filter.value.period, filter.value.status], () => {
  router.replace({ query: { period: filter.value.period } })
  load()
})

const totals = computed(() => {
  let debit = 0, drafts = 0
  for (const r of rows.value) {
    if (r.status !== 'voided') debit += r.amount
    if (r.status === 'draft') drafts++
  }
  return { debit, drafts }
})

const statusTone = (s) => (s === 'posted' ? 'profit' : s === 'draft' ? 'warn' : 'muted')

function newVoucher() {
  editingId.value = 0
  editorOpen.value = true
}

async function editVoucher(row) {
  editingId.value = row.id
  editorOpen.value = true
}

async function openDetail(row) {
  const r = await api.voucherDetail(row.id)
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  detail.value = r.data
  detailOpen.value = true
}

async function onSaved(saved) {
  editorOpen.value = false
  notify(`草稿${saved.no ? ' ' + saved.no : ''}已保存`,
    'success', DRAFT_HINT)
  await load()
}

async function removeDraft(row) {
  if (!confirm(`确认删除草稿「${row.remark || row.no || '未命名'}」？\n草稿不占凭证号，删除不会造成断号。`)) return
  const r = await api.deleteVoucher(row.id)
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('草稿已删除', 'success')
  await load()
}

async function doReverse() {
  if (!operator.value.trim()) { notify('请填写操作人', 'warn'); return }
  const r = await api.reverseVoucher({ id: detail.value.id, by: operator.value.trim() })
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已红字冲销，生成凭证 ${r.data.no}`, 'success')
  reverseOpen.value = false
  detailOpen.value = false
  await load()
}

const sourceIcon = (s) =>
  s === 'bank' ? Landmark : s === 'ai' ? Sparkles : s === 'closing' ? Undo2 : FileText
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- 工具条 -->
    <div class="flex flex-wrap items-center gap-2">
      <select
        v-model="filter.period"
        class="h-9 rounded-md border border-input bg-transparent px-2 text-sm"
      >
        <option value="">全部期间</option>
        <option v-for="p in periods" :key="p.label" :value="p.label">
          {{ p.label }}（{{ p.statusLabel }}）
        </option>
      </select>

      <select
        v-model="filter.status"
        class="h-9 rounded-md border border-input bg-transparent px-2 text-sm"
      >
        <option value="">全部状态</option>
        <option value="draft">草稿</option>
        <option value="posted">已记账</option>
        <option value="voided">已冲销</option>
      </select>

      <div class="relative w-64">
        <Search class="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          v-model="filter.keyword"
          class="pl-8"
          placeholder="搜摘要或凭证号，回车查询"
          @keyup.enter="load"
        />
      </div>

      <Button variant="outline" size="sm" :disabled="loading" @click="load">
        <RefreshCw :class="loading ? 'animate-spin' : ''" /> 刷新
      </Button>

      <div class="ml-auto flex items-center gap-2">
        <Badge v-if="totals.drafts" variant="warn" :title="DRAFT_HINT">
          {{ totals.drafts }} 张草稿待过账
        </Badge>
        <Button @click="newVoucher"><Plus /> 录入凭证</Button>
      </div>
    </div>

    <Card>
      <CardContent class="px-0 pt-0">
        <Spinner v-if="loading" />
        <EmptyState
          v-else-if="rows.length === 0"
          title="这个期间还没有凭证"
          description="点右上角「录入凭证」开始记账。凭证先存草稿（不占凭证号），到账期结算时统一过账。"
        >
          <Button size="sm" @click="newVoucher"><Plus /> 录入第一张凭证</Button>
        </EmptyState>

        <table v-else class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40">
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">凭证号</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">日期</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">摘要</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">来源</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">金额</th>
              <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">状态</th>
              <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="r in rows" :key="r.id"
              class="border-b last:border-0 hover:bg-accent/30"
              :class="r.status === 'voided' ? 'opacity-55' : ''"
            >
              <td class="px-3 py-1.5">
                <button
                  class="font-mono text-xs hover:underline"
                  :class="r.status === 'draft' ? 'text-muted-foreground' : ''"
                  @click="openDetail(r)"
                >
                  {{ r.no || '（草稿）' }}
                </button>
              </td>
              <td class="px-3 py-1.5 whitespace-nowrap text-xs">{{ r.date }}</td>
              <td class="max-w-[22rem] truncate px-3 py-1.5">
                <span class="mr-1.5 inline-flex align-middle text-muted-foreground">
                  <component :is="sourceIcon(r.source)" class="size-3.5" />
                </span>
                {{ r.remark || '（无摘要）' }}
                <span v-if="r.attachCount" class="ml-1.5 inline-flex items-center gap-0.5 text-xs text-muted-foreground">
                  <Paperclip class="size-3" />{{ r.attachCount }}
                </span>
              </td>
              <td class="px-3 py-1.5 text-xs text-muted-foreground">
                {{ r.sourceLabel }}
                <Badge v-if="r.createdByAi" variant="muted" class="ml-1">AI</Badge>
              </td>
              <td class="num px-3 py-1.5 font-medium">{{ fmtMoney(r.amount) }}</td>
              <td class="px-3 py-1.5">
                <Badge :variant="statusTone(r.status)">{{ r.statusLabel }}</Badge>
              </td>
              <td class="px-3 py-1.5">
                <div class="flex justify-end gap-1">
                  <Button
                    v-if="r.status === 'draft'"
                    variant="ghost" size="sm" title="编辑草稿" @click="editVoucher(r)"
                  >
                    <Pencil /> 编辑
                  </Button>
                  <Button
                    v-if="r.status === 'draft'"
                    variant="ghost" size="sm" title="删除草稿" @click="removeDraft(r)"
                  >
                    <Trash2 />
                  </Button>
                  <Button variant="ghost" size="sm" title="查看详情" @click="openDetail(r)">
                    <FileText />
                  </Button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </CardContent>
    </Card>

    <!-- 录入 / 编辑 -->
    <VoucherEditor
      v-model:open="editorOpen"
      :voucher-id="editingId"
      @saved="onSaved"
    />

    <!-- 详情 -->
    <Modal
      v-model:open="detailOpen"
      :title="detail ? `凭证 ${detail.no || '（草稿）'}` : '凭证详情'"
      :description="detail?.blockedReason || ''"
      width="max-w-4xl"
    >
      <div v-if="detail" class="flex flex-col gap-4">
        <div class="grid grid-cols-4 gap-3 text-sm">
          <div><p class="text-xs text-muted-foreground">凭证字</p><p class="mt-0.5">{{ detail.word }}</p></div>
          <div><p class="text-xs text-muted-foreground">日期</p><p class="mt-0.5">{{ detail.date }}</p></div>
          <div><p class="text-xs text-muted-foreground">状态</p>
            <p class="mt-0.5"><Badge :variant="statusTone(detail.status)">{{ detail.statusLabel }}</Badge></p>
          </div>
          <div><p class="text-xs text-muted-foreground">附单据数</p><p class="mt-0.5">{{ detail.attachCount }} 张</p></div>
          <div><p class="text-xs text-muted-foreground">制单人</p><p class="mt-0.5">{{ detail.createdBy || '—' }}</p></div>
          <div><p class="text-xs text-muted-foreground">记账人</p><p class="mt-0.5">{{ detail.postedBy || '—' }}</p></div>
          <div class="col-span-2"><p class="text-xs text-muted-foreground">摘要</p><p class="mt-0.5">{{ detail.remark || '—' }}</p></div>
        </div>

        <div v-if="detail.reversesNo" class="rounded-md border border-[var(--warn)]/40 bg-[var(--warn)]/10 p-2.5 text-xs">
          本凭证是红字冲销凭证，冲销的是 <b>{{ detail.reversesNo }}</b>。
        </div>
        <div v-if="detail.voidedByNo" class="rounded-md border border-[var(--warn)]/40 bg-[var(--warn)]/10 p-2.5 text-xs">
          本凭证已被 <b>{{ detail.voidedByNo }}</b> 红字冲销。原凭证与红字凭证都保留在账上，净额为零。
        </div>

        <div class="rounded-lg border">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b bg-muted/40">
                <th class="h-8 px-3 text-left text-xs font-medium text-muted-foreground">#</th>
                <th class="h-8 px-3 text-left text-xs font-medium text-muted-foreground">摘要</th>
                <th class="h-8 px-3 text-left text-xs font-medium text-muted-foreground">科目</th>
                <th class="h-8 px-3 text-left text-xs font-medium text-muted-foreground">辅助核算</th>
                <th class="h-8 px-3 text-right text-xs font-medium text-muted-foreground">借方</th>
                <th class="h-8 px-3 text-right text-xs font-medium text-muted-foreground">贷方</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="l in detail.lines" :key="l.lineNo" class="border-b last:border-0">
                <td class="px-3 py-1.5 text-xs text-muted-foreground">{{ l.lineNo }}</td>
                <td class="px-3 py-1.5">{{ l.summary }}</td>
                <td class="px-3 py-1.5">
                  <span class="font-mono text-xs">{{ l.accountCode }}</span>
                  <span class="ml-1.5">{{ l.accountName }}</span>
                </td>
                <td class="px-3 py-1.5 text-xs text-muted-foreground">{{ l.auxDesc || '—' }}</td>
                <td class="num px-3 py-1.5 text-[var(--debit)]">{{ fmtMoney(l.debit, { blankZero: true }) }}</td>
                <td class="num px-3 py-1.5 text-[var(--credit)]">{{ fmtMoney(l.credit, { blankZero: true }) }}</td>
              </tr>
              <tr class="bg-muted/40 font-medium">
                <td class="px-3 py-1.5" colspan="4">合计</td>
                <td class="num px-3 py-1.5">{{ fmtMoney(detail.totalDebit) }}</td>
                <td class="num px-3 py-1.5">{{ fmtMoney(detail.totalCredit) }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="flex items-center gap-2">
          <Badge :variant="detail.balanced ? 'profit' : 'loss'">
            {{ detail.balanced ? '借贷平衡' : '借贷不平衡' }}
          </Badge>
          <span class="text-xs text-muted-foreground">
            {{ detail.lines.length }} 条分录
          </span>
        </div>
      </div>

      <template #footer>
        <Input v-model="operator"
                 @change="rememberBookkeeper(operator)" class="mr-auto h-8 w-40" placeholder="操作人" />
        <Button variant="ghost" @click="detailOpen = false">关闭</Button>
        <Button
          v-if="detail?.canEdit"
          variant="outline"
          @click="detailOpen = false; editVoucher({ id: detail.id })"
        >
          <Pencil /> 编辑
        </Button>
        <Button v-if="detail?.canReverse" variant="destructive" @click="reverseOpen = true">
          <Undo2 /> 红字冲销
        </Button>
      </template>
    </Modal>

    <!-- 冲销确认 -->
    <Modal
      v-model:open="reverseOpen"
      title="红字冲销"
      description="冲销会生成一张借贷互换的新凭证，而不是删除原凭证。凭证号不断号，账务轨迹可追溯。"
      width="max-w-xl"
    >
      <div v-if="detail" class="flex flex-col gap-3 text-sm">
        <p>将对凭证 <b>{{ detail.no }}</b>（{{ detail.date }}，{{ fmtMoney(detail.totalDebit) }}）生成红字冲销凭证。</p>
        <p class="text-xs text-muted-foreground">
          冲销凭证默认沿用原凭证日期，这样两张凭证落在同一期间，该期间的净额自然归零。
          若原期间已结账，请先反结账。
        </p>
      </div>
      <template #footer>
        <Button variant="ghost" @click="reverseOpen = false">取消</Button>
        <Button variant="destructive" @click="doReverse"><Undo2 /> 确认冲销</Button>
      </template>
    </Modal>
  </div>
</template>
