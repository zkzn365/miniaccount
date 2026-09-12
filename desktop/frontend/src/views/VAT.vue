<script setup>
import { computed, onMounted, ref } from 'vue'
import {
  RefreshCw, Percent, Calculator, ShieldCheck, ShieldAlert, Info,
  ChevronDown, ChevronRight, Download, Upload, AlertTriangle,
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

const tab = ref('rate') // rate | policies | deduct
const loading = ref(false)
const identity = ref(null)
const policies = ref([])
const reference = ref(null)
const expanded = ref({})
const policyStatus = ref(null)

// ---- 税率查询 ----
const rateForm = ref({
  status: '', subject: 'entity', category: 'goods', method: '', on: '',
  // ★ 例外情形（如出口命中公告第七条异常情形）默认不参与匹配。
  // 是否命中属事实认定，程序替用户选了就可能少缴税；
  // 只有用户自己确认命中，才把这个开关打开。
  includeConditional: false,
})
const rateResult = ref(null)
const rateError = ref('')

// ---- 抵扣判定 ----
const deductForm = ref({
  on: '', voucher: 'special_invoice', taxYuan: '', method: '',
  forSimplifiedOrExempt: false, abnormalLoss: false, collectiveWelfare: false,
  cateringRecreation: false, loanInterest: false, nonTaxableTransaction: false,
  equityTransfer: false, isLongTermAsset: false, mixedUse: false,
  assetValueYuan: '', alreadyCredited: false,
})
const deductResult = ref(null)
const deductError = ref('')

async function load() {
  loading.value = true
  const [id, p, r, st] = await Promise.all([
    api.vatIdentity(), api.vatPolicies(), api.vatReference(), api.vatPolicyStatus(),
  ])
  loading.value = false
  if (id.ok) identity.value = id.data
  else notify(id.fault.message, 'error', id.fault.detail)
  if (p.ok) policies.value = p.data ?? []
  if (r.ok) reference.value = r.data
  if (st.ok) policyStatus.value = st.data
  // 默认按账套登记的身份查
  if (identity.value?.vatStatus && !rateForm.value.status) {
    rateForm.value.status = identity.value.vatStatus
  }
}
onMounted(load)

async function resolveRate() {
  rateError.value = ''
  rateResult.value = null
  const r = await api.resolveVATRate({ ...rateForm.value })
  if (!r.ok) { rateError.value = r.fault.message; return }
  rateResult.value = r.data
}

// 载入新版内置政策表。会丢掉用户自己改过的税率，所以先确认。
async function resetPolicies() {
  const ok = window.confirm(
    '载入新版内置政策表会覆盖当前政策表，你自己改过的税率会丢失。\n\n' +
    '如果只是个别税率要调整，建议先「导出」，改完再导入。\n\n确定要载入吗？')
  if (!ok) return
  const r = await api.resetVATPolicies()
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已载入新版内置政策表', 'success')
  await load()
}

async function judgeDeduction() {
  deductError.value = ''
  deductResult.value = null
  if (!deductForm.value.taxYuan) {
    deductError.value = '请填写凭证上注明的进项税额'
    return
  }
  const r = await api.judgeDeduction({ ...deductForm.value })
  if (!r.ok) { deductError.value = r.fault.message; return }
  deductResult.value = r.data
}

// 只有当天有效的政策才默认展示；过期的折叠起来
const activePolicies = computed(() => policies.value.filter((p) => p.activeOn))
const expiredPolicies = computed(() => policies.value.filter((p) => !p.activeOn))

const statusOptions = [
  { v: 'general', n: '一般纳税人' },
  { v: 'small_scale', n: '小规模纳税人' },
]
const subjectOptions = [
  { v: 'entity', n: '单位' },
  { v: 'self_employed', n: '个体工商户' },
  { v: 'person', n: '个人' },
]

// 这些是「事实认定」而不是系统能判断的：用户勾选，程序据此判定
const USAGE_FLAGS = [
  { key: 'forSimplifiedOrExempt', label: '用于简易计税或免税项目' },
  { key: 'abnormalLoss', label: '非正常损失' },
  { key: 'collectiveWelfare', label: '集体福利、个人消费或交际应酬' },
  { key: 'cateringRecreation', label: '直接消费的餐饮、居民日常、娱乐服务' },
  { key: 'loanInterest', label: '贷款利息及直接相关的顾问费、手续费、咨询费' },
  { key: 'nonTaxableTransaction', label: '对应特定非应税交易' },
  { key: 'equityTransfer', label: '股权转让、股息红利或部分境外交易' },
]

// 小规模 + 专票也要计入成本 —— 界面上要看得见「税额去哪了」
const deductibleAmount = computed(() => deductResult.value?.deductibleAmount ?? 0)
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- 身份：两套概念分开展示 -->
    <Card v-if="identity">
      <CardContent class="grid gap-4 pt-5 md:grid-cols-2">
        <div class="rounded-lg border p-4">
          <p class="flex items-center gap-1.5 text-sm font-medium">
            <Percent class="size-4 text-muted-foreground" />
            增值税纳税人身份
          </p>
          <p class="mt-2">
            <Badge :variant="identity.canDeductInputVat ? 'debit' : 'warn'">
              {{ identity.vatStatusLabel }}
            </Badge>
            <span class="ml-2 text-xs text-muted-foreground">
              生效于 {{ identity.vatStatusEffectiveFrom }}
            </span>
          </p>
          <p class="mt-2 text-xs text-muted-foreground">
            {{ identity.canDeductInputVat
              ? '可以抵扣进项税额，按一般计税（特定业务可选简易计税）'
              : '不得抵扣进项税额 —— 取得专用发票也应将税额计入成本或资产价值' }}
          </p>
          <details v-if="identity.statusHistory?.length > 1" class="mt-2">
            <summary class="cursor-pointer text-xs text-muted-foreground">
              身份变更历史（{{ identity.statusHistory.length }} 段）
            </summary>
            <ul class="mt-1 space-y-0.5 text-xs text-muted-foreground">
              <li v-for="(h, i) in identity.statusHistory" :key="i">
                {{ h.from }} ~ {{ h.to || '至今' }}：{{ h.statusLabel }}
                <span v-if="h.note">· {{ h.note }}</span>
              </li>
            </ul>
          </details>
        </div>

        <div class="rounded-lg border p-4">
          <p class="flex items-center gap-1.5 text-sm font-medium">
            企业规模类型
          </p>
          <p class="mt-2">
            <Badge v-if="identity.enterpriseScaleSet" variant="muted">
              {{ identity.enterpriseScaleLabel }}
            </Badge>
            <Badge v-else variant="warn">未填写</Badge>
          </p>
          <p class="mt-2 text-xs text-muted-foreground">
            依据《中小企业划型标准规定》，看营业收入与从业人员，
            决定能否享受<b>小型微利企业所得税</b>优惠。
            <br />
            ⚠️ 它与上面那一项<b>互不派生</b> —— 小微企业完全可能是一般纳税人。
            程序不会拿「是不是小微」去推断「能不能抵扣进项」。
          </p>
        </div>
      </CardContent>
    </Card>

    <!-- 标签页 -->
    <div class="flex gap-1 rounded-lg border p-0.5 no-print w-fit">
      <button
        v-for="t in [
          { v: 'rate', n: '查适用税率', i: Calculator },
          { v: 'deduct', n: '进项能否抵扣', i: ShieldCheck },
          { v: 'policies', n: '税率政策表', i: Percent }]"
        :key="t.v"
        class="flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm transition-colors"
        :class="tab === t.v ? 'bg-muted font-medium' : 'text-muted-foreground hover:bg-muted/50'"
        @click="tab = t.v"
      >
        <component :is="t.i" class="size-3.5" />
        {{ t.n }}
      </button>
    </div>

    <Spinner v-if="loading" />

    <!-- ① 查适用税率 -->
    <Card v-else-if="tab === 'rate'">
      <CardHeader>
        <CardTitle>这项业务按几个点开票</CardTitle>
        <CardDescription>
          程序按<b>业务发生日</b>取当时有效的政策，并把法定税率与实际优惠税率
          分别列出来。查不到时会直接报错，不给一个默认税率 ——
          默认成 13% 或 0% 都会静默算错税。
        </CardDescription>
      </CardHeader>
      <CardContent class="flex flex-col gap-4">
        <div class="flex flex-wrap items-end gap-3">
          <div class="w-40">
            <Label>纳税人身份</Label>
            <select v-model="rateForm.status"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option v-for="o in statusOptions" :key="o.v" :value="o.v">{{ o.n }}</option>
            </select>
          </div>
          <div class="w-36">
            <Label>经营主体</Label>
            <select v-model="rateForm.subject"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option v-for="o in subjectOptions" :key="o.v" :value="o.v">{{ o.n }}</option>
            </select>
          </div>
          <div class="w-72">
            <Label>业务类型</Label>
            <select v-model="rateForm.category"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option v-for="o in reference?.categories ?? []" :key="o.value" :value="o.value">
                {{ o.label }}
              </option>
            </select>
          </div>
          <div class="w-40">
            <Label>计税方法</Label>
            <select v-model="rateForm.method"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option value="">按身份推断</option>
              <option v-for="o in reference?.methods ?? []" :key="o.value" :value="o.value">
                {{ o.label }}
              </option>
            </select>
          </div>
          <div class="w-36">
            <Label>业务发生日</Label>
            <Input v-model="rateForm.on" class="mt-1.5" placeholder="留空 = 今天" />
          </div>
          <Button @click="resolveRate"><Calculator /> 查税率</Button>
        </div>

        <label class="flex w-fit cursor-pointer items-start gap-2 rounded-lg border p-3 text-xs">
          <input v-model="rateForm.includeConditional" type="checkbox"
                 class="mt-0.5 size-4 shrink-0 accent-[var(--primary)]" />
          <span>
            <b>我确认命中例外情形</b>
            <span class="text-muted-foreground">
              （如出口业务命中公告第七条异常情形）。
              例外情形要按公告条款与实际业务判断，属于<b>事实认定</b>，
              所以默认不参与匹配 —— 勾了才会按征收率给出结果。
            </span>
          </span>
        </label>

        <div v-if="rateError" class="rounded-lg border border-[var(--warn)]/50 bg-[var(--warn)]/5 p-4 text-sm">
          <p class="flex items-start gap-2">
            <AlertTriangle class="mt-0.5 size-4 shrink-0 text-[var(--warn)]" />
            <span>{{ rateError }}</span>
          </p>
        </div>

        <div v-if="rateResult" class="rounded-lg border p-4">
          <!-- ★ 非精确匹配：你问的那一档**不适用**，显示的是实际适用的那一档。
               不把这个说清楚，用户会以为问到了。 -->
          <p v-if="rateResult.exact === false"
             class="mb-3 flex items-start gap-2 rounded border border-[var(--warn)]/50 bg-[var(--warn)]/10 p-3 text-sm">
            <AlertTriangle class="mt-0.5 size-4 shrink-0 text-[var(--warn)]" />
            <span>
              没有与你所问情形<b>精确匹配</b>的政策。下面给出的是该身份下实际适用的处理，
              不要按你原先问的那一档开票。
            </span>
          </p>
          <p v-if="rateResult.conditional"
             class="mb-3 flex items-start gap-2 rounded border border-[var(--warn)]/50 bg-[var(--warn)]/10 p-3 text-sm">
            <ShieldAlert class="mt-0.5 size-4 shrink-0 text-[var(--warn)]" />
            <span>
              本结果按<b>需人工确认的例外情形</b>给出（{{ rateResult.name }}）。
              请在凭证与备查资料里留痕，说明认定命中的依据。
            </span>
          </p>

          <div class="flex items-baseline gap-4">
            <div>
              <p class="text-xs text-muted-foreground">实际适用</p>
              <p class="text-3xl font-semibold text-[var(--debit)]">{{ rateResult.rate }}</p>
              <p v-if="!rateResult.rateApplicable" class="text-xs text-muted-foreground">
                本档不适用税率，不是 0%
              </p>
            </div>
            <div v-if="rateResult.preferentialRate" class="text-sm">
              <p class="text-muted-foreground">
                法定 {{ rateResult.statutoryRate }} → 优惠 {{ rateResult.preferentialRate }}
              </p>
            </div>
            <div v-else class="text-sm text-muted-foreground">
              法定税率即实际适用（无优惠）
            </div>
          </div>

          <dl class="mt-4 grid grid-cols-2 gap-x-6 gap-y-1 text-sm">
            <template v-for="(row, i) in [
              ['业务日期', rateResult.effectiveFrom && rateForm.on || '今天'],
              ['纳税人身份', rateResult.statusLabel],
              ['经营主体', rateResult.subjectLabel],
              ['业务类型', rateResult.categoryLabel],
              ['计税方法', rateResult.methodLabel],
              ['税收处理', rateResult.treatmentLabel],
              ['进项税额', rateResult.inputTaxLabel],
              ['政策依据', rateResult.legalBasis],
            ]" :key="i">
              <dt class="text-muted-foreground">{{ row[0] }}</dt><dd>{{ row[1] }}</dd>
            </template>
          </dl>

          <p v-if="rateResult.note"
             class="mt-3 whitespace-pre-line rounded border bg-muted/30 p-3 text-xs">
            {{ rateResult.note }}
          </p>
          <p v-if="rateResult.expiresOn" class="mt-2 flex items-center gap-1.5 text-xs text-[var(--warn)]">
            <AlertTriangle class="size-3.5" />
            该政策截止 {{ rateResult.expiresOn }}，到期后程序会自动回落到法定税率，
            不需要改代码
          </p>
        </div>

        <p v-if="reference?.softwareHint" class="rounded-lg border bg-muted/30 p-3 text-xs text-muted-foreground">
          <Info class="mr-1 inline size-3.5" />{{ reference.softwareHint }}
        </p>
      </CardContent>
    </Card>

    <!-- ② 进项能否抵扣 -->
    <Card v-else-if="tab === 'deduct'">
      <CardHeader>
        <CardTitle>这笔进项税额能不能抵扣</CardTitle>
        <CardDescription>
          程序按「纳税人身份 → 计税方法 → 凭证类型 → 用途 → 长期资产」的顺序判定，
          并区分两件账务处理完全不同的事：
          「不予抵扣（税额计入成本）」与「已抵扣的需做进项税额转出」。
        </CardDescription>
      </CardHeader>
      <CardContent class="flex flex-col gap-4">
        <div class="flex flex-wrap items-end gap-3">
          <div class="w-36">
            <Label>业务发生日</Label>
            <Input v-model="deductForm.on" class="mt-1.5" placeholder="留空 = 今天" />
          </div>
          <div class="w-72">
            <Label>扣税凭证</Label>
            <select v-model="deductForm.voucher"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option v-for="o in reference?.voucherKinds ?? []" :key="o.value" :value="o.value">
                {{ o.label }}{{ o.deductible ? '' : '（不可抵扣）' }}
              </option>
            </select>
          </div>
          <div class="w-40">
            <Label>进项税额（元）</Label>
            <Input v-model="deductForm.taxYuan" class="mt-1.5" placeholder="1300.00" />
          </div>
          <div class="w-40">
            <Label>计税方法</Label>
            <select v-model="deductForm.method"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option value="">按身份推断</option>
              <option v-for="o in reference?.methods ?? []" :key="o.value" :value="o.value">
                {{ o.label }}
              </option>
            </select>
          </div>
        </div>

        <div>
          <Label>用途（这些是事实认定，请按实际情况勾选）</Label>
          <div class="mt-2 grid grid-cols-1 gap-1.5 md:grid-cols-2">
            <label v-for="f in USAGE_FLAGS" :key="f.key"
                   class="flex items-center gap-2 text-sm">
              <input v-model="deductForm[f.key]" type="checkbox" class="size-3.5" />
              {{ f.label }}
            </label>
          </div>
        </div>

        <div class="rounded-lg border p-3">
          <label class="flex items-center gap-2 text-sm">
            <input v-model="deductForm.isLongTermAsset" type="checkbox" class="size-3.5" />
            形成长期资产（固定资产 / 无形资产 / 不动产）
          </label>
          <div v-if="deductForm.isLongTermAsset" class="mt-2 flex flex-wrap items-end gap-3 pl-6">
            <div class="w-40">
              <Label>单项原值（元）</Label>
              <Input v-model="deductForm.assetValueYuan" class="mt-1.5" placeholder="6000000" />
            </div>
            <label class="flex items-center gap-2 text-sm">
              <input v-model="deductForm.mixedUse" type="checkbox" class="size-3.5" />
              混合用于一般计税与不得抵扣项目
            </label>
            <span class="text-xs text-muted-foreground">
              单项原值 ≤ {{ fmtMoney(reference?.longTermAssetThreshold ?? 0) }}
              符合条件可全额抵扣；超过则先抵扣、后按年度调整
            </span>
          </div>
        </div>

        <div class="flex items-center gap-4">
          <label class="flex items-center gap-2 text-sm">
            <input v-model="deductForm.alreadyCredited" type="checkbox" class="size-3.5" />
            该笔进项已经抵扣过（决定是「不予抵扣」还是「进项税额转出」）
          </label>
          <Button @click="judgeDeduction"><ShieldCheck /> 判定</Button>
        </div>

        <div v-if="deductError" class="rounded-lg border border-[var(--warn)]/50 bg-[var(--warn)]/5 p-4 text-sm">
          <p class="flex items-start gap-2">
            <AlertTriangle class="mt-0.5 size-4 shrink-0 text-[var(--warn)]" />
            <span>{{ deductError }}</span>
          </p>
        </div>

        <div v-if="deductResult"
             class="rounded-lg border p-4"
             :class="deductResult.deductible
               ? 'border-[var(--profit)]/50 bg-[var(--profit)]/5'
               : 'border-[var(--loss)]/40 bg-[var(--loss)]/5'">
          <p class="flex items-center gap-2 text-base font-semibold"
             :class="deductResult.deductible ? 'text-[var(--profit)]' : 'text-[var(--loss)]'">
            <component :is="deductResult.deductible ? ShieldCheck : ShieldAlert" class="size-5" />
            {{ deductResult.deductible ? '可以抵扣' : '不得抵扣' }}
            <span v-if="deductResult.deductible" class="num ml-2">
              {{ fmtMoney(deductibleAmount) }}
            </span>
          </p>
          <p v-if="!deductResult.deductible" class="mt-1 text-sm">
            原因：{{ deductResult.reasonLabel }}
          </p>

          <div v-if="deductResult.transferOut > 0"
               class="mt-3 rounded border border-[var(--warn)]/50 bg-[var(--warn)]/10 p-3 text-sm">
            <b>需做进项税额转出：{{ fmtMoney(deductResult.transferOut) }}</b>
            <p class="mt-1 text-xs text-muted-foreground">
              这笔进项此前已经抵扣过，事后发现属于不得抵扣范围，所以要转出。
            </p>
          </div>
          <div v-if="deductResult.includedInCost > 0"
               class="mt-3 rounded border bg-muted/30 p-3 text-sm">
            <b>应计入成本或资产价值：{{ fmtMoney(deductResult.includedInCost) }}</b>
            <p class="mt-1 text-xs text-muted-foreground">
              从未抵扣过，不存在转出问题 —— 税额直接进成本。
            </p>
          </div>

          <p class="mt-3 text-sm">{{ deductResult.note }}</p>

          <p class="mt-3 text-xs text-muted-foreground">
            判定依据：{{ deductResult.vatStatusOnDate }} 的增值税纳税人身份为
            <b>{{ deductResult.vatStatusLabel }}</b>
            <span v-if="!deductResult.identityKnown" class="text-[var(--loss)]">
              （该日期没有身份记录，已按「不得抵扣」处理）
            </span>
            · 凭证「{{ deductResult.voucherLabel }}」· 计税方法「{{ deductResult.methodLabel }}」
          </p>
        </div>
      </CardContent>
    </Card>

    <!-- ③ 政策表 -->
    <Card v-else>
      <CardHeader>
        <div class="flex items-start justify-between gap-4">
          <div>
            <CardTitle>增值税税率政策表</CardTitle>
            <CardDescription>
              政策是数据不是代码：每条都带法定税率、实际优惠税率、生效失效日期
              与政策依据。1% 这类阶段性优惠到期后程序会自动回落到法定税率，
              不需要等新版本。
              <br />
              ⚠️ 发布或换地区前请按现行有效文件核对本表 ——
              程序不替你判断某项业务属于哪一类。
            </CardDescription>
          </div>
          <div class="flex shrink-0 flex-col items-end gap-2">
            <div class="flex gap-2">
              <Button variant="outline" size="sm"
                      @click="api.exportVATPolicies('vat-policies.json')
                        .then((r) => notify(r.ok ? '已导出 vat-policies.json' : r.fault.message,
                          r.ok ? 'success' : 'error'))">
                <Download /> 导出
              </Button>
              <Button v-if="policyStatus?.updateAvailable" variant="outline" size="sm"
                      class="border-[var(--warn)]/50 text-[var(--warn)]"
                      @click="resetPolicies">
                <RefreshCw /> 载入新版内置表
              </Button>
            </div>
            <p v-if="policyStatus" class="text-xs text-muted-foreground">
              当前版本 {{ policyStatus.version }}
              <span v-if="policyStatus.updateAvailable">
                · 内置表已更新到
                <b class="text-[var(--warn)]">{{ policyStatus.builtinVersion }}</b>
              </span>
            </p>
          </div>
        </div>
      </CardHeader>
      <CardContent class="px-0">
        <div v-if="policyStatus?.updateAvailable"
             class="mx-4 mb-4 rounded-lg border border-[var(--warn)]/50 bg-[var(--warn)]/5 p-3 text-sm">
          <p class="flex items-start gap-2">
            <AlertTriangle class="mt-0.5 size-4 shrink-0 text-[var(--warn)]" />
            <span>
              当前用的是政策表版本 <b>{{ policyStatus.version }}</b>，
              程序内置的已经是 <b>{{ policyStatus.builtinVersion }}</b>。
              <template v-if="policyStatus.builtin">
                这份表没被改过，程序会在下次打开时自动换成新版。
              </template>
              <template v-else-if="policyStatus.origin === 'custom'">
                这份表你改过，所以程序不会自动覆盖 ——
                要不要换成新版内置表，请你自己定。建议先「导出」备份。
              </template>
              <template v-else>
                本账套是旧版本建的，程序无法确认这份表改没改过，
                所以不自动覆盖。请核对后决定是否载入新版内置表，
                载入前建议先「导出」备份。
              </template>
            </span>
          </p>
        </div>

        <EmptyState v-if="activePolicies.length === 0"
                    title="没有生效中的政策" description="请检查政策表的生效期" />
        <div v-else class="overflow-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b bg-muted/40">
                <th class="h-9 w-8 px-2" />
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">政策</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">身份 / 主体</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">业务类型</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">计税方法</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">税收处理</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">法定</th>
                <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">实际</th>
                <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">生效期</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="p in activePolicies" :key="p.code">
                <tr class="cursor-pointer border-b hover:bg-accent/30"
                    @click="expanded[p.code] = !expanded[p.code]">
                  <td class="px-2 py-1.5 text-muted-foreground">
                    <component :is="expanded[p.code] ? ChevronDown : ChevronRight" class="size-3.5" />
                  </td>
                  <td class="px-3 py-1.5 font-medium">{{ p.name }}</td>
                  <td class="px-3 py-1.5">
                    <Badge variant="muted">{{ p.statusLabel }}</Badge>
                    <Badge v-if="p.subjectLabel !== '不限'" variant="warn" class="ml-1">
                      {{ p.subjectLabel }}
                    </Badge>
                  </td>
                  <td class="max-w-56 truncate px-3 py-1.5 text-muted-foreground"
                      :title="p.categoryLabel">{{ p.categoryLabel }}</td>
                  <td class="px-3 py-1.5">{{ p.methodLabel }}</td>
                  <td class="px-3 py-1.5">
                    <Badge :variant="p.treatment === 'TAXABLE' ? 'muted' : 'warn'">
                      {{ p.treatmentLabel }}
                    </Badge>
                    <Badge v-if="p.conditional" variant="warn" class="ml-1"
                           title="需人工确认的例外情形，默认不参与匹配">需确认</Badge>
                  </td>
                  <td class="num px-3 py-1.5 text-right text-muted-foreground">
                    {{ p.statutoryRate }}
                  </td>
                  <td class="num px-3 py-1.5 text-right font-medium"
                      :class="p.preferentialRate ? 'text-[var(--warn)]' : ''">
                    {{ p.rate }}
                    <span v-if="p.preferentialRate" class="ml-1 text-[10px]">优惠</span>
                  </td>
                  <td class="px-3 py-1.5 text-xs text-muted-foreground whitespace-nowrap">
                    {{ p.effectiveFrom }} ~ {{ p.effectiveTo || '长期' }}
                  </td>
                </tr>
                <tr v-if="expanded[p.code]" class="border-b bg-muted/20">
                  <td colspan="9" class="px-6 py-3 text-xs">
                    <p><span class="text-muted-foreground">进项税额：</span>{{ p.inputTaxLabel }}</p>
                    <p><span class="text-muted-foreground">政策依据：</span>{{ p.legalBasis }}</p>
                    <p class="mt-1"><span class="text-muted-foreground">政策版本：</span>{{ p.version }}</p>
                    <p v-if="p.note" class="mt-1 text-muted-foreground">{{ p.note }}</p>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>

        <p v-if="expiredPolicies.length" class="mt-4 px-4 text-xs text-muted-foreground">
          另有 {{ expiredPolicies.length }} 条已过期的政策（不影响当前计算）：
          {{ expiredPolicies.map((p) => p.name).join('、') }}
        </p>
      </CardContent>
    </Card>
  </div>
</template>
