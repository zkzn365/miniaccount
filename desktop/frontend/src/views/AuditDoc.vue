<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import {
  FileSignature, AlertTriangle, Info, Printer, Copy, Check, RefreshCw, Plus, Trash2,
} from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
import { bookkeeper } from '@/lib/operator'
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

// ---------------------------------------------------------------------------
// 审计与鉴证文书
// ---------------------------------------------------------------------------
//
// 三份：审计报告、验资报告、管理建议书。
//
// ★ 软件产出的是草稿，不是可以拿去用的报告。
//
// 这三份东西都要签字盖章对外出。软件能做的是把事实摆整齐：
// 审了什么、账上是多少、底稿里还有哪些没补齐；而意见是执业判断，
// 署名的人要负责。所以：
//
//   1. 意见类型必须由人选（程序给默认值但会指出矛盾）；
//   2. 缺什么就明明白白列在「待补事项」里，而且印在全文最前面 ——
//      一份看起来已经写好了的草稿被拿去盖章，比打印不出来更糟。

const kinds = ref([])
const opinions = ref([])
const kind = ref('audit')
const period = ref('')
const periods = ref([])
const data = ref(null)
const loading = ref(true)
const loadingOpinions = ref(false)
const showFullText = ref(false)
const copied = ref(false)

// 签字信息：三家文书共用
const sign = ref({
  firmName: '', reportNo: '', cpa1: '', cpa2: '', reportDate: '',
})
const form = ref({
  opinion: 'unqualified',
  basisExtra: '',
  registeredCapitalYuan: '',
  evidence: '',
  nonCash: false,
  shares: [],
})

const operator = bookkeeper

// ★ 请求序号：快速切文书种类/期间时，旧响应会把新数据覆盖掉
let reqSeq = 0

async function load() {
  if (!period.value) return
  const my = ++reqSeq
  const [y, m] = period.value.split('-')
  loading.value = true
  const req = { kind: kind.value, year: Number(y), month: Number(m), ...sign.value }
  if (kind.value === 'audit') {
    req.opinion = form.value.opinion
    req.basisExtra = form.value.basisExtra.split('\n').map((s) => s.trim()).filter(Boolean)
  }
  if (kind.value === 'capital') {
    req.registeredCapitalYuan = form.value.registeredCapitalYuan
    req.nonCash = form.value.nonCash
    req.evidence = form.value.evidence.split('\n').map((s) => s.trim()).filter(Boolean)
    req.shares = form.value.shares.map((s) => ({
      name: s.name, subscribedYuan: s.subscribedYuan, method: s.method, paidDate: s.paidDate,
    }))
  }
  const r = await api.auditDoc(req)
  if (my !== reqSeq) return // 有更新的请求在跑，丢弃这次结果
  loading.value = false
  if (!r.ok) {
    // 失败要清空：留着上一种文书/上一期的正文，用户会以为它属于当前这一份
    data.value = null
    notify(r.fault.message, 'error', r.fault.detail)
    return
  }
  data.value = r.data
  // 首次拿到文书时，把签字信息按已填的值回填（用户改过的不覆盖）
  if (r.data.inputs?.length) {
    for (const f of r.data.inputs) {
      if (!sign.value[f.key] && f.value) sign.value[f.key] = f.value
    }
  }
}

onMounted(async () => {
  const [k, o, p] = await Promise.all([
    api.auditDocKinds(), api.auditOpinions(), api.periods(),
  ])
  if (k.ok) kinds.value = k.data ?? []
  if (o.ok) opinions.value = o.data ?? []
  // 期间列表只在这里取；期间定下来后由 watch 触发 load
  // （不在挂载时另外再 load 一次，那会并发两次同样的请求）
  if (p.ok) {
    periods.value = p.data.periods ?? []
    const open = periods.value.filter((x) => x.status === 'open')
    period.value = (open[0] ?? periods.value[0])?.label ?? ''
  }
})

// 换文书种类或期间都要重算：三种文书的取数完全不同
watch([kind, period], load)

async function loadShareholders() {
  if (!period.value) return
  const [y, m] = period.value.split('-')
  const r = await api.shareholderPaid({ year: Number(y), month: Number(m) })
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  form.value.shares = (r.data ?? []).map((s) => ({
    name: s.name, subscribedYuan: '', method: '货币', paidDate: '', paid: s.paid,
  }))
  if (!form.value.shares.length) {
    notify('账套里没有股东出资记录：请先在辅助核算里建股东，并登记实收资本', 'warn')
  }
}

function addShare() {
  form.value.shares.push({ name: '', subscribedYuan: '', method: '货币', paidDate: '', paid: 0 })
}
function removeShare(i) {
  form.value.shares.splice(i, 1)
}

async function copyText() {
  try {
    await navigator.clipboard.writeText(data.value?.fullText ?? '')
    copied.value = true
    setTimeout(() => { copied.value = false }, 2000)
    notify('已复制全文', 'success')
  } catch {
    notify('复制失败：可以选中正文手动复制', 'warn')
  }
}

function printDoc() {
  window.print()
}

const missing = computed(() => data.value?.missing ?? [])
const sections = computed(() => data.value?.sections ?? [])
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
        <RefreshCw /> 重新生成
      </Button>
      <div class="flex-1" />
      <Button variant="outline" :disabled="!data" @click="copyText">
        <component :is="copied ? Check : Copy" /> 复制全文
      </Button>
      <Button variant="outline" :disabled="!data" @click="printDoc">
        <Printer /> 打印 / 另存为 PDF
      </Button>
    </div>

    <div class="flex flex-wrap gap-2">
      <Button v-for="k in kinds" :key="k.value" size="sm"
              :variant="kind === k.value ? 'default' : 'outline'"
              @click="kind = k.value">
        <FileSignature class="size-3.5" /> {{ k.label }}
      </Button>
    </div>

    <Spinner v-if="loading" />

    <template v-else-if="data">
      <!-- 待补事项：放在最前面 -->
      <Card v-if="missing.length" class="border-[var(--loss)]/40">
        <CardHeader>
          <CardTitle class="text-[var(--loss)]">
            <span class="inline-flex items-center gap-2">
              <AlertTriangle class="size-4" /> 还不能签发（{{ missing.length }} 项待补）
            </span>
          </CardTitle>
          <CardDescription>
            这份稿子里有还没落实的事实。带着待补事项盖章出去，责任是签字人的。
          </CardDescription>
        </CardHeader>
        <CardContent>
          <ul class="list-disc space-y-1 pl-5 text-sm">
            <li v-for="(m, i) in missing" :key="i">{{ m }}</li>
          </ul>
        </CardContent>
      </Card>

      <!-- 事务所与签字信息 -->
      <Card>
        <CardHeader>
          <CardTitle>出具方与签字信息</CardTitle>
          <CardDescription>
            这些是执业判断与责任落点，程序不代填。填完点「重新生成」。
          </CardDescription>
        </CardHeader>
        <CardContent class="grid gap-3 sm:grid-cols-3">
          <div>
            <Label>会计师事务所名称</Label>
            <Input v-model="sign.firmName" class="mt-1.5" placeholder="××会计师事务所（普通合伙）" />
          </div>
          <div>
            <Label>报告文号</Label>
            <Input v-model="sign.reportNo" class="mt-1.5" placeholder="××会审字〔2025〕第 123 号" />
          </div>
          <div>
            <Label>报告日期</Label>
            <Input v-model="sign.reportDate" class="mt-1.5" placeholder="2026-03-31" />
          </div>
          <div>
            <Label>注册会计师（签字）</Label>
            <Input v-model="sign.cpa1" class="mt-1.5" placeholder="姓名" />
          </div>
          <div>
            <Label>第二名注册会计师（签字）</Label>
            <Input v-model="sign.cpa2" class="mt-1.5" placeholder="姓名" />
          </div>
          <div class="flex items-end">
            <p class="text-xs text-muted-foreground">
              审计报告与验资报告须<b>两名</b>注册会计师签名盖章，
              事务所盖章后生效；只有一名不能出具。
            </p>
          </div>
        </CardContent>
      </Card>

      <!-- 审计报告：意见类型必须由人选 -->
      <Card v-if="kind === 'audit'">
        <CardHeader>
          <CardTitle>审计意见类型</CardTitle>
          <CardDescription>
            ★ 意见是执业判断，程序只给默认值并指出矛盾 —— 它不会替你选。
          </CardDescription>
        </CardHeader>
        <CardContent class="space-y-3">
          <div class="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
            <button v-for="o in opinions" :key="o.value"
                    class="rounded-lg border p-3 text-left text-sm transition"
                    :class="form.opinion === o.value
                      ? 'border-primary bg-primary/8' : 'hover:bg-accent/40'"
                    @click="form.opinion = o.value; load()">
              <div class="font-medium">{{ o.label }}</div>
              <div class="mt-0.5 text-xs text-muted-foreground">{{ o.hint }}</div>
            </button>
          </div>
          <div>
            <Label>形成意见的基础（补充要点，每行一条）</Label>
            <textarea v-model="form.basisExtra" rows="3"
                      class="mt-1.5 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm"
                      placeholder="非无保留意见必须写清理由；这里补充程序算不出来的判断（可留空）" />
            <p class="mt-1 text-xs text-muted-foreground">
              重要性水平与未更正错报由程序从审计底稿填进「形成意见的基础」。
            </p>
          </div>
        </CardContent>
      </Card>

      <!-- 验资报告：注册资本与股东认缴 -->
      <Card v-if="kind === 'capital'">
        <CardHeader>
          <CardTitle>注册资本与出资情况</CardTitle>
          <CardDescription>
            账套里只有<b>实缴</b>（实收资本按股东辅助核算）；认缴出资额、出资方式与日期要你填。
          </CardDescription>
        </CardHeader>
        <CardContent class="space-y-3">
          <div class="grid gap-3 sm:grid-cols-2">
            <div>
              <Label>注册资本（元）</Label>
              <Input v-model="form.registeredCapitalYuan" class="num mt-1.5" placeholder="1000000.00" />
            </div>
            <div class="flex items-end gap-3">
              <Button size="sm" variant="outline" @click="loadShareholders">
                <RefreshCw /> 从账套带出股东
              </Button>
              <label class="flex items-center gap-2 text-sm">
                <input v-model="form.nonCash" type="checkbox" />
                有非货币出资
              </label>
            </div>
          </div>

          <div class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b bg-muted/40">
                  <th class="h-9 px-2 text-left text-xs font-medium text-muted-foreground">股东</th>
                  <th class="h-9 w-36 px-2 text-right text-xs font-medium text-muted-foreground">认缴（元）</th>
                  <th class="h-9 w-36 px-2 text-right text-xs font-medium text-muted-foreground">账上实缴</th>
                  <th class="h-9 w-28 px-2 text-left text-xs font-medium text-muted-foreground">出资方式</th>
                  <th class="h-9 w-32 px-2 text-left text-xs font-medium text-muted-foreground">出资日期</th>
                  <th class="h-9 w-10 px-2" />
                </tr>
              </thead>
              <tbody>
                <tr v-for="(s, i) in form.shares" :key="i" class="border-b last:border-0">
                  <td class="px-2 py-1">
                    <input v-model="s.name" class="w-full rounded border-0 bg-transparent px-1.5 py-1 text-sm"
                           placeholder="股东名称（姓名）" />
                  </td>
                  <td class="px-2 py-1">
                    <input v-model="s.subscribedYuan" class="num w-full rounded border-0 bg-transparent px-1.5 py-1 text-sm text-right"
                           placeholder="0.00" />
                  </td>
                  <td class="num px-2 py-1 text-right text-muted-foreground">{{ fmtMoney(s.paid) }}</td>
                  <td class="px-2 py-1">
                    <select v-model="s.method"
                            class="w-full rounded border-0 bg-transparent px-1 text-sm">
                      <option value="货币">货币</option>
                      <option value="实物">实物</option>
                      <option value="知识产权">知识产权</option>
                      <option value="土地使用权">土地使用权</option>
                      <option value="股权">股权</option>
                    </select>
                  </td>
                  <td class="px-2 py-1">
                    <input v-model="s.paidDate" class="w-full rounded border-0 bg-transparent px-1.5 py-1 text-sm"
                           placeholder="2025-03-10" />
                  </td>
                  <td class="px-2 py-1">
                    <button class="rounded p-1 text-muted-foreground hover:bg-accent"
                            @click="removeShare(i)">
                      <Trash2 class="size-3.5" />
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
            <div class="mt-2 flex items-center justify-between">
              <Button size="sm" variant="outline" @click="addShare"><Plus /> 加一位股东</Button>
              <span class="text-xs text-muted-foreground">
                实缴取自账套「实收资本」按股东辅助核算的余额
              </span>
            </div>
          </div>

          <div>
            <Label>验资依据（每行一条）</Label>
            <textarea v-model="form.evidence" rows="3"
                      class="mt-1.5 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm"
                      placeholder="中国银行进账单（2025-03-10，600,000.00）" />
            <p class="mt-1 text-xs text-muted-foreground">
              验资报告的结论必须有依据支撑；非货币出资还要有评估报告与财产权转移手续。
            </p>
          </div>
        </CardContent>
      </Card>

      <!-- 文书正文 -->
      <Card>
        <CardHeader>
          <div class="flex items-start justify-between gap-3">
            <div>
              <CardTitle>{{ data.title }}（{{ data.company }} · {{ data.period }}）</CardTitle>
              <CardDescription>
                每一段都标注了事实来源。签字盖章前请逐段核对。
              </CardDescription>
            </div>
            <div class="flex shrink-0 gap-2">
              <Badge variant="warn">草稿</Badge>
              <Badge v-if="data.canIssue" variant="profit">待签字盖章</Badge>
              <Badge v-else variant="loss">不可签发</Badge>
            </div>
          </div>
        </CardHeader>
        <CardContent class="space-y-4">
          <div v-if="data.opinionStr" class="text-sm">
            意见类型：<b>{{ data.opinionStr }}</b>
          </div>
          <div v-for="s in sections" :key="s.no + s.title" class="border-l-2 border-muted pl-3">
            <div class="text-sm font-medium">{{ s.no }}、{{ s.title }}</div>
            <p class="mt-1 whitespace-pre-wrap text-sm">{{ s.body }}</p>
            <p class="mt-1 text-xs text-muted-foreground">来源：{{ s.source }}</p>
          </div>
          <div class="rounded-lg border bg-muted/30 p-3 text-xs text-muted-foreground">
            <div class="flex items-start gap-2">
              <Info class="mt-0.5 size-3.5 shrink-0" />
              <div>
                <p>{{ data.signature }}</p>
                <p class="mt-1">{{ data.policyNote }}</p>
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      <!-- 全文（打印用） -->
      <Card>
        <CardHeader>
          <div class="flex items-start justify-between gap-3">
            <div>
              <CardTitle>文书全文</CardTitle>
              <CardDescription>
                复制或打印的是这一份。有待补事项时，全文最前面会印上「请勿签发」。
              </CardDescription>
            </div>
            <Button size="sm" variant="outline" @click="showFullText = !showFullText">
              {{ showFullText ? '收起' : '展开' }}
            </Button>
          </div>
        </CardHeader>
        <CardContent v-if="showFullText">
          <pre class="whitespace-pre-wrap rounded-lg border bg-muted/30 p-3 text-sm">{{ data.fullText }}</pre>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
