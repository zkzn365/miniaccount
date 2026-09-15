<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import {
  Calculator, AlertTriangle, RefreshCw, Info, Printer, ChevronDown, ChevronRight,
} from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
import { bookkeeper } from '@/lib/operator'
import { fmtMoney, centsToYuanInput } from '@/lib/format'
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

// ---------------------------------------------------------------------------
// 税务计算表
// ---------------------------------------------------------------------------
//
// 三张表：增值税及附加、企业所得税（季度预缴）、个人所得税（工资薪金）。
//
// ★ 名字是「计算表」不是「申报表」，这不是谦虚。
//
// 申报表是向税务机关提交的法律文书，表样、行次、口径都由官方固定，
// 而且必须与申报系统的版本一致。这里的产物是自己算给自己看的：
// 账上这个月该交多少、这个季度预缴多少、这个月该给谁扣多少。
//
// 所以每一行都写着「这个数从哪来」—— 会计要能拿着这张表
// 到申报系统里逐行核对，而不是照抄一份不知道从哪算出来的数。

const kinds = ref([])
const kind = ref('vat')
const period = ref('')
const periods = ref([])
const data = ref(null)
const loading = ref(true)
const busy = ref(false)
const showSource = ref(true)

// 申报台账（本年）
const filings = ref(null)
const filingOpen = ref(false)
const filingForm = ref(emptyFiling())

function emptyFiling() {
  return {
    id: 0, year: 0, month: 0, status: 'filed', filedDate: '', paidDate: '',
    payableYuan: '', taxAmountYuan: '', surchargeYuan: '', paidYuan: '',
    fromCurrentReturn: true, manualAmounts: false,
    channel: '电子税务局', receiptNo: '', note: '',
  }
}

// 企业所得税的纳税调整：必须人工判断，程序不猜
const citForm = ref({
  taxAdjustIncreaseYuan: '', taxAdjustDecreaseYuan: '', lossOffsetYuan: '',
  smallLowProfit: null,
})

const operator = bookkeeper

// ★ 请求序号：快速点「增值税 → 企业所得税」或快速切期间时，
// 后返回的旧响应会把新数据覆盖掉（选中的是所得税、表却是增值税的数）。
let reqSeq = 0

async function load() {
  if (!period.value) return
  const my = ++reqSeq
  const [y, m] = period.value.split('-')
  loading.value = true
  const req = { kind: kind.value, year: Number(y), month: Number(m) }
  if (kind.value === 'cit') {
    Object.assign(req, citForm.value)
  }
  const [r, f] = await Promise.all([api.taxReturn(req), api.taxFilings(Number(y))])
  if (my !== reqSeq) return // 有更新的请求在跑，丢弃这次结果
  loading.value = false
  if (!r.ok) {
    // 失败要清空：留着上一期/上一个税种的数字，用户会以为看的是这一期
    data.value = null
    notify(r.fault.message, 'error', r.fault.detail)
    return
  }
  data.value = r.data
  if (f.ok) filings.value = f.data
}

/** 打开登记申报的弹窗（默认按当前计算表填数）。 */
function openFiling(filing) {
  if (filing) {
    filingForm.value = {
      // ★ 带上这条记录自己的属期：列表里每一行都能点「改」，
      // 而页面顶部的期间标签可能已经切到别的月份了
      id: filing.id, year: filing.year, month: filing.month,
      status: filing.status,
      filedDate: filing.filedDate, paidDate: filing.paidDate,
      payableYuan: centsToYuanInput(filing.payable),
      taxAmountYuan: centsToYuanInput(filing.taxAmount),
      surchargeYuan: centsToYuanInput(filing.surcharge),
      paidYuan: centsToYuanInput(filing.paid),
      fromCurrentReturn: false, manualAmounts: true,
      channel: filing.channel, receiptNo: filing.receiptNo, note: filing.note,
    }
  } else {
    filingForm.value = emptyFiling()
  }
  filingOpen.value = true
}

async function saveFiling() {
  const f = filingForm.value
  // 改已有记录时用它自己的属期；新登记用页面当前选中的期间
  const [py, pm] = period.value.split('-')
  const y = f.id ? f.year : Number(py)
  const m = f.id ? f.month : Number(pm)
  if (!operator.value.trim()) { notify('请先填写记账人 —— 台账要记清是谁办的', 'warn'); return }
  if (!f.filedDate) { notify('请填写申报日期', 'warn'); return }
  if (f.status === 'paid' && !f.paidDate) { notify('已申报并缴纳就要填缴款日期', 'warn'); return }
  busy.value = true
  const r = await api.saveTaxFiling({
    id: f.id, kind: kind.value, year: y, month: m,
    status: f.status, filedDate: f.filedDate, paidDate: f.paidDate,
    payableYuan: f.fromCurrentReturn ? '' : f.payableYuan,
    taxAmountYuan: f.fromCurrentReturn ? '' : f.taxAmountYuan,
    surchargeYuan: f.fromCurrentReturn ? '' : f.surchargeYuan,
    paidYuan: f.fromCurrentReturn ? '' : f.paidYuan,
    fromCurrentReturn: f.fromCurrentReturn,
    manualAmounts: !f.fromCurrentReturn,
    channel: f.channel, receiptNo: f.receiptNo, note: f.note,
    operator: operator.value.trim(),
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已登记申报', 'success', '更正申报请先作废原记录，作废会留下痕迹')
  filingOpen.value = false
  await load()
}

async function voidFiling(item) {
  if (!operator.value.trim()) { notify('请先填写记账人', 'warn'); return }
  const reason = window.prompt(
    `作废「${item.kindLabel} ${item.period}」这条申报记录？

` +
    `作废原因（要写进台账，更正申报的过程要能追溯）：`, '')
  if (reason === null) return
  if (!reason.trim()) { notify('要写明作废原因', 'warn'); return }
  busy.value = true
  const r = await api.voidTaxFiling({ id: item.id, by: operator.value.trim(), reason: reason.trim() })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已作废，可以重新登记更正后的申报', 'success')
  await load()
}

onMounted(async () => {
  const k = await api.taxReturnKinds()
  if (k.ok) kinds.value = k.data ?? []
  // 期间列表与台账都只在这里取一次；期间定下来之后由 watch 触发 load
  // （不在挂载时另外再 load 一次，那会并发两次同样的请求）
  const p = await api.periods()
  if (p.ok) {
    periods.value = p.data.periods ?? []
    // 默认落在当前期间（最早的未结账月份）
    const open = periods.value.filter((x) => x.status === 'open')
    period.value = (open[0] ?? periods.value[0])?.label ?? ''
  }
})

// 换税种或换期间都要重算 —— 三张表的取数口径完全不同
watch([kind, period], load)

const rows = computed(() => data.value?.rows ?? [])
const keys = computed(() => data.value?.keys ?? [])
const pending = computed(() => filings.value?.pending ?? [])
const filingItems = computed(() => filings.value?.items ?? [])
const identities = computed(() => data.value?.identities ?? [])
const warnings = computed(() => data.value?.warnings ?? [])
const sources = computed(() => data.value?.sources ?? [])

/** 关键数的着色：负数（多缴）用红，其余正常。 */
function keyClass(k) {
  return Number(k.amount) < 0 ? 'text-[var(--loss)]' : ''
}
</script>

<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-end gap-3">
      <div>
        <Label>会计期间</Label>
        <select v-model="period"
                class="mt-1.5 h-9 rounded-md border border-input bg-transparent px-2 text-sm">
          <option v-for="p in periods" :key="p.label" :value="p.label">
            {{ p.label }}（{{ p.statusLabel }}）
          </option>
        </select>
      </div>
      <Button variant="outline" :disabled="loading" @click="load">
        <RefreshCw /> 重新算
      </Button>
      <div class="flex-1" />
      <span class="text-xs text-muted-foreground">
        记账人 {{ operator || '（未填写）' }}
      </span>
    </div>

    <!-- 三个税种：用按钮切换而不是下拉 —— 只有三个，摊开更快 -->
    <div class="flex flex-wrap gap-2">
      <Button v-for="k in kinds" :key="k.value" size="sm"
              :variant="kind === k.value ? 'default' : 'outline'"
              @click="kind = k.value">
        <Calculator class="size-3.5" /> {{ k.label }}
      </Button>
    </div>

    <Spinner v-if="loading" />

    <template v-else-if="data">
      <!-- 口径与身份：这三张表的坑几乎都在这里 -->
      <Card>
        <CardHeader>
          <div class="flex items-start justify-between gap-3">
            <div>
              <CardTitle>{{ data.title }}（{{ data.period }}）</CardTitle>
              <CardDescription>
                账套里正规算出来的口径。<b>不是申报表</b> —— 请拿着它到电子税务局逐行核对。
              </CardDescription>
            </div>
            <Badge variant="warn">不可直接申报</Badge>
          </div>
        </CardHeader>
        <CardContent class="space-y-3">
          <div class="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
            <div v-for="(id, i) in identities" :key="i"
                 class="rounded-lg border p-2.5 text-xs"
                 :class="id.warn ? 'border-[var(--warn)]/50 bg-[var(--gold)]/8' : ''">
              <div class="text-muted-foreground">{{ id.label }}</div>
              <div class="mt-0.5 font-medium">{{ id.value }}</div>
            </div>
          </div>

          <div class="text-sm font-medium">{{ data.concludes }}</div>

          <div v-if="warnings.length" class="space-y-1 rounded-lg border border-[var(--warn)]/50 bg-[var(--gold)]/8 p-3">
            <div class="flex items-center gap-2 text-sm font-medium">
              <AlertTriangle class="size-4" /> 要注意
            </div>
            <ul class="list-disc pl-5 text-xs">
              <li v-for="(w, i) in warnings" :key="i">{{ w }}</li>
            </ul>
          </div>
        </CardContent>
      </Card>

      <!-- 企业所得税：纳税调整必须人工填 -->
      <Card v-if="data.inputFields?.length">
        <CardHeader>
          <CardTitle>需要人工判断的部分</CardTitle>
          <CardDescription>
            这几项账上算不出来 —— 它们是<b>税法的口径</b>，不是会计的口径。填完点「重新算」。
          </CardDescription>
        </CardHeader>
        <CardContent class="space-y-3">
          <div class="grid gap-3 sm:grid-cols-3">
            <div v-for="f in data.inputFields" :key="f.key">
              <Label>{{ f.label }}（元）</Label>
              <Input v-if="f.key === 'taxAdjustIncrease'" v-model="citForm.taxAdjustIncreaseYuan"
                     class="num mt-1.5" placeholder="0.00" />
              <Input v-else-if="f.key === 'taxAdjustDecrease'" v-model="citForm.taxAdjustDecreaseYuan"
                     class="num mt-1.5" placeholder="0.00" />
              <Input v-else v-model="citForm.lossOffsetYuan" class="num mt-1.5" placeholder="0.00" />
              <p class="mt-1 text-xs text-muted-foreground">{{ f.hint }}</p>
            </div>
          </div>
          <div>
            <Label>优惠口径</Label>
            <div class="mt-1.5 flex gap-2">
              <Button size="sm" :variant="citForm.smallLowProfit === true ? 'default' : 'outline'"
                      @click="citForm.smallLowProfit = true; load()">
                小型微利企业（实际税负 5%）
              </Button>
              <Button size="sm" :variant="citForm.smallLowProfit === false ? 'default' : 'outline'"
                      @click="citForm.smallLowProfit = false; load()">
                一般企业（25%）
              </Button>
              <Button size="sm" variant="ghost" @click="citForm.smallLowProfit = null; load()">
                按账套规模推断
              </Button>
            </div>
            <p class="mt-1 text-xs text-muted-foreground">
              判定标准有三项：应纳税所得额 ≤ 300 万元、从业人数 ≤ 300 人、资产总额 ≤ 5,000 万元。
              软件只知道第一项，所以这里让你确认。
            </p>
          </div>
        </CardContent>
      </Card>

      <!-- 本期的申报状态：报没报，一眼可见 -->
      <Card :class="data.filing ? '' : 'border-[var(--warn)]/50'">
        <CardHeader>
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <CardTitle>本期的申报状态</CardTitle>
              <CardDescription>
                ★ 光有计算表不够：不告诉你「这期其实已经报过了」，很可能照着再报一次。
              </CardDescription>
            </div>
            <div class="flex items-center gap-2">
              <Badge v-if="data.filing" :variant="data.filing.status === 'paid' ? 'profit' : 'default'">
                {{ data.filing.statusLabel }}
              </Badge>
              <Badge v-else variant="warn">未登记申报</Badge>
              <Button size="sm" variant="outline" :disabled="busy"
                      @click="openFiling(data.filing)">
                {{ data.filing ? '修改申报记录' : '登记申报' }}
              </Button>
            </div>
          </div>
        </CardHeader>
        <CardContent class="space-y-2 text-sm">
          <p>{{ data.filingHint }}</p>
          <div v-if="data.filing" class="grid gap-2 sm:grid-cols-4">
            <div class="rounded-lg border p-2.5 text-xs">
              <div class="text-muted-foreground">申报日期</div>
              <div class="mt-0.5 font-medium">{{ data.filing.filedDate || '—' }}</div>
            </div>
            <div class="rounded-lg border p-2.5 text-xs">
              <div class="text-muted-foreground">缴款日期</div>
              <div class="mt-0.5 font-medium">{{ data.filing.paidDate || '—' }}</div>
            </div>
            <div class="rounded-lg border p-2.5 text-xs">
              <div class="text-muted-foreground">当时申报的应补(退)</div>
              <div class="num mt-0.5 font-medium">{{ fmtMoney(data.filing.payable) }}</div>
            </div>
            <div class="rounded-lg border p-2.5 text-xs">
              <div class="text-muted-foreground">现在算出来的</div>
              <div class="num mt-0.5 font-medium">{{ fmtMoney(data.filing.computed) }}</div>
            </div>
            <div class="rounded-lg border p-2.5 text-xs">
              <div class="text-muted-foreground">本期已缴</div>
              <div class="num mt-0.5 font-medium">{{ fmtMoney(data.filing.paid) }}</div>
            </div>
          </div>
          <p v-if="data.filing && data.filing.diff !== 0" class="text-[var(--loss)]">
            {{ data.filing.reconcile }}
          </p>
        </CardContent>
      </Card>

      <!-- 关键数 -->
      <Card>
        <CardHeader>
          <CardTitle>算出来的结果</CardTitle>
        </CardHeader>
        <CardContent>
          <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <div v-for="(k, i) in keys" :key="i" class="rounded-lg border p-3">
              <div class="text-xs text-muted-foreground">{{ k.label }}</div>
              <div class="num mt-1 text-lg font-semibold" :class="keyClass(k)">
                {{ fmtMoney(k.amount) }}
              </div>
              <div v-if="k.note" class="mt-0.5 text-xs text-muted-foreground">{{ k.note }}</div>
            </div>
          </div>
        </CardContent>
      </Card>

      <!-- 计算过程 -->
      <Card>
        <CardHeader>
          <div class="flex items-start justify-between gap-3">
            <div>
              <CardTitle>计算过程</CardTitle>
              <CardDescription>
                每一行的「数据来源」写清了它从哪个科目或报表来 —— 拿这张表去申报系统逐行核对。
              </CardDescription>
            </div>
            <Button size="sm" variant="outline" @click="showSource = !showSource">
              <component :is="showSource ? ChevronDown : ChevronRight" class="size-3.5" />
              {{ showSource ? '隐藏来源' : '显示来源' }}
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          <div class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b bg-muted/40">
                  <th class="h-9 w-16 px-3 text-left text-xs font-medium text-muted-foreground">行次</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">项目</th>
                  <th class="h-9 w-40 px-3 text-right text-xs font-medium text-muted-foreground">金额</th>
                  <th v-if="showSource" class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">数据来源</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(r, i) in rows" :key="i" class="border-b last:border-0"
                    :class="r.emphasis ? 'bg-[var(--gold)]/8 font-medium' : ''">
                  <td class="num px-3 py-1.5 font-mono text-xs text-muted-foreground">{{ r.line }}</td>
                  <td class="px-3 py-1.5">
                    {{ r.label }}
                    <span v-if="r.note" class="text-xs text-muted-foreground">（{{ r.note }}）</span>
                  </td>
                  <td class="num px-3 py-1.5 text-right">{{ fmtMoney(r.amount) }}</td>
                  <td v-if="showSource" class="px-3 py-1.5 text-xs text-muted-foreground">
                    {{ r.source }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </CardContent>
      </Card>

      <!-- 本年申报台账 -->
      <Card v-if="filings">
        <CardHeader>
          <CardTitle>本年申报台账（{{ filings.year }}）</CardTitle>
          <CardDescription>
            台账记的是<b>发生过的事实</b>：报了多少、什么时候报的、谁办的。
            账后来改了，它与现在算出来的数就会不一致 —— 那正是要处理的事。
          </CardDescription>
        </CardHeader>
        <CardContent class="space-y-3">
          <p class="text-sm">{{ filings.concludes }}</p>

          <div v-if="pending.length" class="rounded-lg border border-[var(--warn)]/50 bg-[var(--gold)]/8 p-3">
            <div class="text-sm font-medium">还没登记的期间（{{ pending.length }} 项）</div>
            <ul class="mt-1 space-y-1 text-xs">
              <li v-for="(p, i) in pending" :key="i">
                {{ p.period }} {{ p.kindLabel }}：{{ fmtMoney(p.payable) }}
                <span class="text-muted-foreground">—— {{ p.hint }}</span>
              </li>
            </ul>
          </div>

          <div v-if="filingItems.length" class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b bg-muted/40">
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">属期</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">税种</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">状态</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">申报 / 缴款</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">当时申报</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">现在算出</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">勾稽</th>
                  <th class="h-9 px-3" />
                </tr>
              </thead>
              <tbody>
                <tr v-for="it in filingItems" :key="it.id" class="border-b last:border-0"
                    :class="it.status === 'void' ? 'text-muted-foreground' : ''">
                  <td class="num px-3 py-1.5">{{ it.period }}</td>
                  <td class="px-3 py-1.5">{{ it.kindLabel }}</td>
                  <td class="px-3 py-1.5">
                    <Badge :variant="it.status === 'paid' ? 'profit'
                      : (it.status === 'void' ? 'muted' : 'default')">
                      {{ it.statusLabel }}
                    </Badge>
                  </td>
                  <td class="px-3 py-1.5 text-xs">
                    {{ it.filedDate || '—' }}<template v-if="it.paidDate"> / {{ it.paidDate }}</template>
                  </td>
                  <td class="num px-3 py-1.5 text-right">
                    {{ fmtMoney(it.payable) }}
                    <div v-if="it.paid" class="text-xs text-muted-foreground">
                      已缴 {{ fmtMoney(it.paid) }}
                    </div>
                  </td>
                  <td class="num px-3 py-1.5 text-right">{{ fmtMoney(it.computed) }}</td>
                  <td class="px-3 py-1.5 text-xs"
                      :class="it.diff !== 0 && it.status !== 'void' ? 'text-[var(--loss)]' : 'text-muted-foreground'">
                    {{ it.reconcile }}
                  </td>
                  <td class="px-3 py-1.5 text-right">
                    <div class="flex justify-end gap-1">
                      <Button v-if="it.status !== 'void'" size="sm" variant="ghost"
                              :disabled="busy" @click="openFiling(it)">改</Button>
                      <Button v-if="it.status !== 'void'" size="sm" variant="ghost"
                              :disabled="busy" @click="voidFiling(it)">作废</Button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-else class="text-sm text-muted-foreground">本年还没有申报记录。</p>
          <p class="text-xs text-muted-foreground">
            更正申报：先「作废」原记录（要写作废原因），再登记更正后的 ——
            两条都留在台账里，因为「这一期一共报过几次」是检查时要答的问题。
          </p>
        </CardContent>
      </Card>

      <!-- 取数与口径说明 -->
      <Card v-if="sources.length || data.policyNote">
        <CardHeader>
          <CardTitle>口径与取数说明</CardTitle>
          <CardDescription>
            报表要能被复核。这一块写清每个数从哪来、用的是哪一版口径。
          </CardDescription>
        </CardHeader>
        <CardContent class="space-y-2 text-sm">
          <div class="flex items-start gap-2 rounded-lg border bg-muted/30 p-3">
            <Info class="mt-0.5 size-4 shrink-0" />
            <div>
              <p>{{ data.policyNote }}</p>
              <p class="mt-1 text-xs text-muted-foreground">
                政策会变：税率、优惠档、减除费用标准都以<b>申报时</b>的最新规定为准。
              </p>
            </div>
          </div>
          <ul v-if="sources.length" class="list-disc space-y-1 pl-5 text-xs text-muted-foreground">
            <li v-for="(s, i) in sources" :key="i">{{ s }}</li>
          </ul>
        </CardContent>
      </Card>
    </template>
    <!-- 登记申报 -->
    <Modal v-model:open="filingOpen"
           :title="filingForm.id ? '修改申报记录' : '登记申报'"
           description="台账记的是发生过的事实：属期、申报日期、当时报了多少、谁办的。">
      <div class="space-y-4">
        <div class="rounded-lg border bg-muted/30 p-3 text-sm">
          {{ data?.kindLabel }} ·
          {{ filingForm.id
            ? `${filingForm.year}-${String(filingForm.month).padStart(2, '0')}`
            : period }}
          <span class="ml-2 text-muted-foreground">
            （属期不可改：改了就是另一条记录）
          </span>
        </div>
        <div class="grid gap-3 sm:grid-cols-2">
          <div>
            <Label>状态</Label>
            <select v-model="filingForm.status"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option value="filed">已申报</option>
              <option value="paid">已申报并缴纳</option>
            </select>
          </div>
          <div>
            <Label>申报日期</Label>
            <Input v-model="filingForm.filedDate" class="mt-1.5" placeholder="2025-04-15" />
            <p class="mt-1 text-xs text-muted-foreground">
              不能早于属期月末 —— 属期还没结束不可能完成申报。
            </p>
          </div>
          <div>
            <Label>缴款日期</Label>
            <Input v-model="filingForm.paidDate" class="mt-1.5" placeholder="2025-04-18" />
            <p class="mt-1 text-xs text-muted-foreground">状态是「已申报并缴纳」时必填。</p>
          </div>
          <div>
            <Label>申报渠道</Label>
            <Input v-model="filingForm.channel" class="mt-1.5" placeholder="电子税务局" />
          </div>
          <div>
            <Label>申报回执号 / 缴款书号</Label>
            <Input v-model="filingForm.receiptNo" class="mt-1.5" placeholder="1234567890" />
          </div>
          <div>
            <Label>经办人（记账人）</Label>
            <Input :model-value="operator" disabled class="mt-1.5" />
          </div>
        </div>

        <div class="rounded-lg border p-3">
          <label class="flex items-center gap-2 text-sm">
            <input v-model="filingForm.fromCurrentReturn" type="checkbox" :disabled="!!filingForm.id" />
            <template v-if="filingForm.id">
              金额是申报当时的快照，不能改（更正申报请先作废再登记）
            </template>
            <template v-else>
              金额按<b>当前计算表</b>填（推荐）
            </template>
          </label>
          <p class="mt-1 text-xs text-muted-foreground">
            手工抄一遍应补税额是这类台账最常见的错源：抄错一位数，
            台账与账就此分叉，而两条记录看起来都正常。
          </p>
          <div v-if="!filingForm.fromCurrentReturn" class="mt-3 grid gap-3 sm:grid-cols-4">
            <div>
              <Label>其中税额（元）</Label>
              <Input v-model="filingForm.taxAmountYuan" class="num mt-1.5" placeholder="6500.00" />
            </div>
            <div>
              <Label>附加税费（元）</Label>
              <Input v-model="filingForm.surchargeYuan" class="num mt-1.5" placeholder="780.00" />
            </div>
            <div>
              <Label>本期已缴（元）</Label>
              <Input v-model="filingForm.paidYuan" class="num mt-1.5" placeholder="0.00" />
              <p class="mt-1 text-xs text-muted-foreground">
                已交税金 / 已预缴。没有它，「应纳 9,000 + 附加 1,080」与
                「应补 4,080」就对不上 —— 差的正是已经交掉的。
              </p>
            </div>
            <div>
              <Label>应补(退)税额（元）</Label>
              <Input v-model="filingForm.payableYuan" class="num mt-1.5" placeholder="4080.00" />
              <p class="mt-1 text-xs text-muted-foreground">
                税额 + 附加税费 − 已缴 必须等于应补(退)税额。
              </p>
            </div>
          </div>
        </div>

        <div>
          <Label>备注</Label>
          <textarea v-model="filingForm.note" rows="2"
                    class="mt-1.5 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm"
                    placeholder="如「更正申报」「大厅补报」" />
        </div>
      </div>
      <template #footer>
        <Button variant="outline" @click="filingOpen = false">取消</Button>
        <Button :disabled="busy" @click="saveFiling">登记</Button>
      </template>
    </Modal>
  </div>
</template>
