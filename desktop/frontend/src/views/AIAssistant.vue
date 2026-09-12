<script setup>
import { computed, ref } from 'vue'
import {
  Sparkles, ShieldCheck, ShieldAlert, AlertTriangle, Wand2, History, Check,
} from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
import { bookkeeper, loadBookkeeper, rememberBookkeeper } from '@/lib/operator'
import { fmtMoney, parseYuanToCents } from '@/lib/format'
import Card from '@/components/ui/Card.vue'
import CardHeader from '@/components/ui/CardHeader.vue'
import CardTitle from '@/components/ui/CardTitle.vue'
import CardDescription from '@/components/ui/CardDescription.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Table from '@/components/ui/Table.vue'
import Th from '@/components/ui/Th.vue'
import Td from '@/components/ui/Td.vue'
import Spinner from '@/components/ui/Spinner.vue'

const form = ref({
  task: 'bank_flow',
  text: '',
  amountYuan: '',
  date: '',
  counterparty: '',
  direction: '',
})
const result = ref(null)
const busy = ref(false)

// ---- 采纳 / 拒绝 ----
//
// ★ 采纳只生成**草稿**，不直接过账。
// AI 提议是「填好的一页纸」；过账意味着它进了总账，
// 那一步必须有人按下去，而且要和手工录入走同一套校验。
// 记账人来自本机设置（见 lib/operator.js）
const operator = bookkeeper
const deciding = ref(false)

async function accept() {
  if (!operator.value.trim()) {
    notify('请填写确认人 —— 凭证需要有记账签章', 'warn')
    return
  }
  deciding.value = true
  const r = await api.acceptAISuggestion({
    id: result.value.suggestionId, createdBy: operator.value.trim(),
  })
  deciding.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(r.data.summary, 'success')
  result.value = null
}

async function reject() {
  const reason = prompt('这条建议哪里不对？（会记进审计表，用于改进提示词）') ?? ''
  deciding.value = true
  const r = await api.rejectAISuggestion({
    id: result.value.suggestionId, createdBy: reason,
  })
  deciding.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已记录：这条建议没用', 'success')
  result.value = null
}

// ---- AI Agent 批量记账 ----
//
// 单笔建议是「描述一笔 → 给一张凭证」；批量是把**待处理的单据**
// 交给它跑一遍（一批没记账的流水、几张待入账的发票）。
// 这是会计每天真正要干的活：月初打开软件，几十条等着记。
//
// ★ 跑完得到的是一堆草稿，不是一本账 —— 过账仍然要人按。
const agentSources = ref([])
const agentForm = ref({ source: 'bank_flows', limit: 10 })
const agentRun = ref(null)
const agentBusy = ref(false)
let agentTimer = null

async function initAgent() {
  const src = await api.aiAgentSources()
  if (src.ok) agentSources.value = src.data ?? []
  const last = await api.latestAIAgentRun()
  if (last.ok && last.data) agentRun.value = last.data
}

async function startAgent() {
  agentBusy.value = true
  const r = await api.startAIAgentRun({ ...agentForm.value })
  agentBusy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  agentRun.value = r.data
  if (r.data.total === 0) {
    notify('没有待记账的单据 —— 已经全部生成过凭证了', 'success')
    return
  }
  notify(`开始处理 ${r.data.total} 条，可以随时停止`, 'success')
  pollAgent()
}

// 轮询进度：批量跑在后台，界面只读状态。
function pollAgent() {
  clearTimeout(agentTimer)
  if (!agentRun.value || agentRun.value.state !== 'running') return
  agentTimer = setTimeout(async () => {
    const r = await api.aiAgentRunStatus(agentRun.value.id)
    if (r.ok) {
      agentRun.value = r.data
      if (r.data.state === 'done') {
        notify(`跑完了：${r.data.okCount}/${r.data.total} 条通过护栏，可以逐条采纳`,
          r.data.okCount > 0 ? 'success' : 'warn')
      } else if (r.data.state === 'cancelled') {
        notify('已停止。跑完的那些还留着，可以继续采纳', 'warn')
      }
    }
    pollAgent()
  }, 800)
}

async function cancelAgent() {
  if (!agentRun.value) return
  const r = await api.cancelAIAgentRun(agentRun.value.id)
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('正在停止…当前这条跑完就停', 'warn')
}

async function acceptAgentItem(item, index) {
  if (!operator.value.trim()) {
    notify('请填写确认人 —— 凭证需要有记账签章', 'warn')
    return
  }
  const r = await api.acceptAIAgentItem({
    runId: agentRun.value.id, index, createdBy: operator.value.trim(),
  })
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(r.data.summary, 'success')
  await refreshAgentRun()
}

async function rejectAgentItem(item, index) {
  const reason = prompt('这条哪里不对？（会记进审计表，用于改进提示词）') ?? ''
  const r = await api.rejectAIAgentItem({
    runId: agentRun.value.id, index, reason,
  })
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已记录：这条没用', 'success')
  await refreshAgentRun()
}

async function refreshAgentRun() {
  if (!agentRun.value) return
  const r = await api.aiAgentRunStatus(agentRun.value.id)
  if (r.ok) agentRun.value = r.data
}

// ---- 提示词设置 ----
//
// ★ 可改的只有中间那段「记账要求」。
// 工作边界与输出格式由程序固定 —— 前者是「不许幻觉」的底线，
// 后者是护栏解析的依据，改了会直接读不出来。
// 界面上必须写明这一点，否则用户会以为「我改了没生效」。
const promptOpen = ref(false)
const prompt = ref(null)
const promptDraft = ref({ instructions: '', taskNotes: {} })
const promptTask = ref('bank_flow')
const promptBusy = ref(false)
const preview = ref(null)

async function loadPrompt() {
  const r = await api.aiPromptConfig()
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  prompt.value = r.data
  promptDraft.value = {
    instructions: r.data.instructions,
    taskNotes: { ...(r.data.taskNotes ?? {}) },
  }
}
loadPrompt()

async function savePrompt() {
  promptBusy.value = true
  const r = await api.saveAIPromptConfig({
    instructions: promptDraft.value.instructions,
    taskNotes: promptDraft.value.taskNotes,
  })
  promptBusy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('提示词已保存 —— 下一次生成建议就会用新的', 'success')
  preview.value = null
  await loadPrompt()
}

async function resetPrompt() {
  if (!confirm('恢复成出厂默认提示词？你自己改过的内容会丢掉。')) return
  promptBusy.value = true
  const r = await api.resetAIPromptConfig()
  promptBusy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已恢复出厂默认', 'success')
  preview.value = null
  await loadPrompt()
}

// 预览走的是**真实**的提示词生成逻辑，与本页生成建议时用的同一套。
// 另写一份预览逻辑必然与真实提示词漂移，而用户是照着预览判断设置生效没有的。
async function doPreview() {
  promptBusy.value = true
  const r = await api.previewAIPrompt(promptTask.value)
  promptBusy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  preview.value = r.data
}

const TASKS = [
  { v: 'bank_flow', name: '银行流水' },
  { v: 'invoice', name: '发票' },
  { v: 'expense', name: '报销单' },
  { v: 'freeform', name: '自然语言' },
]

async function init() {
  const r = await api.today()
  if (r.ok) form.value.date = r.data
  await initAgent()
  // 记账人（本机设置）：采纳建议时要签这个人的名
  await loadBookkeeper()
  if (bookkeeper.value) operator.value = bookkeeper.value
}
init()

async function submit() {
  if (!form.value.text.trim()) { notify('请先描述这笔业务', 'warn'); return }
  const cents = parseYuanToCents(form.value.amountYuan)
  if (cents === null) { notify('金额格式不对，最多两位小数', 'warn'); return }

  busy.value = true
  result.value = null
  const r = await api.aiSuggest({
    task: form.value.task,
    text: form.value.text,
    amountYuan: form.value.amountYuan,
    date: form.value.date,
    counterparty: form.value.counterparty,
    direction: form.value.direction,
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  result.value = r.data
  if (!r.data.ok && r.data.error) notify(r.data.error, 'error')
}

const integrity = computed(() => {
  const r = result.value
  if (!r) return null
  const d = (r.voucher?.entries ?? []).reduce((s, e) => s + (e.debit || 0), 0)
  const c = (r.voucher?.entries ?? []).reduce((s, e) => s + (e.credit || 0), 0)
  return { debit: d, credit: c, balanced: d === c && d > 0 }
})
</script>

<template>
  <div class="grid grid-cols-[26rem_1fr] gap-5">
    <!-- 输入 -->
    <Card class="self-start">
      <CardHeader>
        <CardTitle class="flex items-center gap-2">
          <Sparkles class="size-4 text-primary" /> 描述这笔业务
        </CardTitle>
        <CardDescription>
          AI 只<b>提议</b>，不写账。提议通过全部护栏后仍是草稿，需人工确认。
        </CardDescription>
      </CardHeader>
      <CardContent class="flex flex-col gap-4">
        <div>
          <Label>业务类型</Label>
          <div class="mt-1.5 flex gap-1.5">
            <Button
              v-for="t in TASKS" :key="t.v"
              :variant="form.task === t.v ? 'default' : 'outline'"
              size="sm"
              @click="form.task = t.v"
            >{{ t.name }}</Button>
          </div>
        </div>

        <div>
          <Label>业务描述</Label>
          <textarea
            v-model="form.text"
            rows="3"
            class="mt-1.5 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            placeholder="例：收到杭州云帆科技有限公司货款"
          />
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <Label>金额（元）</Label>
            <Input v-model="form.amountYuan" class="mt-1.5" placeholder="106000.00" />
          </div>
          <div>
            <Label>业务日期</Label>
            <Input v-model="form.date" class="mt-1.5" placeholder="2025-03-11" />
          </div>
        </div>

        <div>
          <Label>对方户名</Label>
          <Input v-model="form.counterparty" class="mt-1.5" placeholder="选填，但填了会明显更准" />
          <p class="mt-1 text-xs text-muted-foreground">
            「上次同样的对手方怎么记的」是最有效的线索，比任何推理都准
          </p>
        </div>

        <div>
          <Label>资金方向</Label>
          <div class="mt-1.5 flex gap-1.5">
            <Button
              v-for="d in ['收入', '支出']" :key="d"
              :variant="form.direction === d ? 'default' : 'outline'"
              size="sm"
              @click="form.direction = form.direction === d ? '' : d"
            >{{ d }}</Button>
          </div>
        </div>

        <Button :disabled="busy" @click="submit">
          <Wand2 /> {{ busy ? '正在生成…' : '生成记账建议' }}
        </Button>
      </CardContent>
    </Card>

    <!-- AI Agent 批量记账 -->
    <Card class="self-start">
      <CardHeader>
        <CardTitle class="flex items-center gap-2">
          <Wand2 class="size-4 text-primary" /> 批量记账（Agent）
        </CardTitle>
        <CardDescription>
          把<b>还没生成凭证</b>的单据交给它跑一遍：一批银行流水、几张待入账的发票。
          每条都过同一套护栏，结果逐条列出来由你采纳或拒绝 ——
          <b>过账仍然要人按</b>。
        </CardDescription>
      </CardHeader>
      <CardContent class="flex flex-col gap-4">
        <div class="flex flex-wrap items-end gap-3">
          <div class="w-40">
            <Label>待记账来源</Label>
            <select v-model="agentForm.source"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option v-for="s in agentSources" :key="s.value" :value="s.value">{{ s.label }}</option>
            </select>
          </div>
          <div class="w-32">
            <Label>最多几条</Label>
            <Input v-model.number="agentForm.limit" type="number" min="1" max="50" class="mt-1.5" />
          </div>
          <Button :disabled="agentBusy || agentRun?.state === 'running'" @click="startAgent">
            <Wand2 /> {{ agentBusy ? '正在启动…' : '开始跑一批' }}
          </Button>
          <Button v-if="agentRun?.state === 'running'" variant="outline" @click="cancelAgent">
            停止
          </Button>
        </div>

        <p class="text-xs text-muted-foreground">
          每条单据一次模型调用（会消耗 token）。跑在后台，可以随时停止；
          已经跑完的部分会保留。
        </p>

        <div v-if="agentRun" class="flex flex-col gap-3">
          <div class="flex flex-wrap items-center gap-3 text-sm">
            <Badge :variant="agentRun.state === 'running' ? 'warn'
              : (agentRun.state === 'done' ? 'profit' : 'muted')">
              {{ agentRun.state === 'running' ? '进行中'
                : (agentRun.state === 'done' ? '已完成'
                : (agentRun.state === 'cancelled' ? '已停止' : '失败')) }}
            </Badge>
            <span>{{ agentRun.done }} / {{ agentRun.total }} 条</span>
            <span class="text-muted-foreground">
              通过护栏 {{ agentRun.okCount }} 条 · 模型 {{ agentRun.model }}
              · token {{ agentRun.tokensIn + agentRun.tokensOut }}
            </span>
          </div>

          <div v-if="agentRun.error" class="rounded border border-destructive/40 bg-destructive/5 p-2 text-xs">
            {{ agentRun.error }}
          </div>

          <div v-for="(it, i) in agentRun.items" :key="i"
               class="rounded-lg border p-3 text-sm"
               :class="it.decision === 'accepted' ? 'border-[var(--profit)]/40 bg-[var(--profit)]/5'
                 : (it.decision === 'rejected' ? 'opacity-60'
                 : (it.ok ? '' : 'border-[var(--warn)]/40'))">
            <div class="flex flex-wrap items-center gap-2">
              <component :is="it.ok ? ShieldCheck : ShieldAlert"
                         class="size-4 shrink-0"
                         :class="it.ok ? 'text-[var(--profit)]' : 'text-[var(--warn)]'" />
              <span class="min-w-0 flex-1 truncate">{{ it.label }}</span>
              <Badge v-if="it.decision === 'accepted'" variant="profit">已采纳</Badge>
              <Badge v-else-if="it.decision === 'rejected'" variant="muted">已拒绝</Badge>
              <span v-else-if="it.ok" class="text-xs text-muted-foreground">
                置信度 {{ Math.round((it.confidence ?? 0) * 100) }}%
              </span>
              <template v-if="it.ok && !it.decision">
                <Button size="sm" :disabled="!operator.trim()" @click="acceptAgentItem(it, i)">
                  <Check class="size-3.5" /> 采纳
                </Button>
                <Button size="sm" variant="ghost" @click="rejectAgentItem(it, i)">不对</Button>
              </template>
            </div>

            <p v-if="it.error" class="mt-1.5 text-xs text-destructive">{{ it.error }}</p>
            <ul v-if="it.failures?.length" class="mt-1.5 list-disc pl-5 text-xs text-muted-foreground">
              <li v-for="(f, k) in it.failures" :key="k">{{ f }}</li>
            </ul>
            <ul v-if="it.warnings?.length" class="mt-1.5 list-disc pl-5 text-xs text-[var(--warn)]">
              <li v-for="(w, k) in it.warnings" :key="k">{{ w }}</li>
            </ul>

            <div v-if="it.voucher" class="mt-2 overflow-auto rounded border bg-muted/20 p-2">
              <p class="text-xs text-muted-foreground">
                {{ it.voucher.bizDate }} · {{ it.voucher.remark }} ·
                {{ fmtMoney(it.voucher.total) }}
              </p>
              <table class="mt-1 w-full text-xs">
                <tbody>
                  <tr v-for="(e, k) in it.voucher.entries" :key="k" class="border-t">
                    <td class="py-0.5 pr-2 font-mono">{{ e.accountCode }}</td>
                    <td class="py-0.5 pr-2">{{ e.summary }}</td>
                    <td class="py-0.5 pr-2 text-right num">{{ fmtMoney(e.debit, { blankZero: true }) }}</td>
                    <td class="py-0.5 text-right num">{{ fmtMoney(e.credit, { blankZero: true }) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <p v-if="agentRun.items.length" class="text-xs text-muted-foreground">
            采纳后生成的是<b>草稿凭证</b>，还要在凭证页核对后过账。
            每条建议都已写进审计表（模型、提示词指纹、token 消耗）。
          </p>
        </div>
      </CardContent>
    </Card>

    <!-- 提示词设置 -->
    <Card class="self-start">
      <CardHeader>
        <button class="flex w-full items-center gap-2 text-left" @click="promptOpen = !promptOpen">
          <Wand2 class="size-4 text-primary" />
          <CardTitle class="flex-1">记账要求（提示词）</CardTitle>
          <Badge :variant="prompt?.custom ? 'warn' : 'muted'">
            {{ prompt?.custom ? '已自定义' : '出厂默认' }}
          </Badge>
        </button>
        <CardDescription>
          这里写的是<b>本账套的会计政策</b>：科目怎么选、摘要怎么写、
          辅助核算要不要挂。写法不同不影响对错，但会影响 AI 记出来像不像你。
        </CardDescription>
      </CardHeader>

      <CardContent v-if="promptOpen" class="flex flex-col gap-3">
        <div>
          <div class="flex items-center justify-between">
            <Label>记账要求</Label>
            <span class="text-xs text-muted-foreground">
              {{ (promptDraft.instructions ?? '').length }} / {{ prompt?.maxInstructions }}
            </span>
          </div>
          <textarea
            v-model="promptDraft.instructions"
            rows="14"
            spellcheck="false"
            class="mt-1.5 w-full rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs leading-relaxed shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </div>

        <div>
          <Label>按业务类型的额外要求（选填）</Label>
          <div class="mt-1.5 flex flex-wrap gap-1.5">
            <Button
              v-for="t in prompt?.tasks ?? []" :key="t.value"
              size="sm"
              :variant="promptTask === t.value ? 'default' : 'outline'"
              @click="promptTask = t.value"
            >{{ t.label }}</Button>
          </div>
          <textarea
            v-model="promptDraft.taskNotes[promptTask]"
            rows="3"
            spellcheck="false"
            class="mt-1.5 w-full rounded-md border border-input bg-transparent px-3 py-2 text-xs shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            :placeholder="`例：${promptTask === 'bank_flow' ? '银行流水优先按对方户名的历史记法，金额一致时直接沿用' : '这一类的特别注意点'}`"
          />
        </div>

        <div class="rounded-md border bg-muted/30 p-3 text-xs">
          <p class="font-medium">以下部分由程序固定，不在这里改：</p>
          <ul class="mt-1 list-disc pl-4 text-muted-foreground">
            <li v-for="(l, i) in prompt?.locked ?? []" :key="i">{{ l }}</li>
          </ul>
        </div>

        <div class="flex flex-wrap items-center gap-2">
          <Button :disabled="promptBusy" @click="savePrompt">保存</Button>
          <Button :disabled="promptBusy" variant="outline" @click="doPreview">预览实际提示词</Button>
          <Button :disabled="promptBusy" variant="ghost" @click="resetPrompt">恢复默认</Button>
        </div>

        <div v-if="preview" class="rounded-md border p-3 text-xs">
          <p class="text-muted-foreground">
            指纹 <code class="font-mono">{{ preview.digest.slice(0, 16) }}…</code>
            · 系统提示词 {{ preview.chars }} 字
            · 每次生成建议都会把这个指纹记进审计表
          </p>
          <details class="mt-2">
            <summary class="cursor-pointer text-muted-foreground">展开系统提示词</summary>
            <pre class="mt-1.5 max-h-72 overflow-auto whitespace-pre-wrap break-all rounded bg-background/60 p-2">{{ preview.system }}</pre>
          </details>
          <details class="mt-2">
            <summary class="cursor-pointer text-muted-foreground">展开本次用户提示词</summary>
            <pre class="mt-1.5 max-h-72 overflow-auto whitespace-pre-wrap break-all rounded bg-background/60 p-2">{{ preview.user }}</pre>
          </details>
        </div>
      </CardContent>
    </Card>

    <!-- 结果 -->
    <div class="flex flex-col gap-4">
      <Spinner v-if="busy" />

      <Card v-else-if="!result">
        <CardContent class="pt-5">
          <div class="flex flex-col items-center gap-2 py-12 text-center">
            <Sparkles class="size-6 text-muted-foreground" />
            <p class="text-sm text-muted-foreground">左侧填写业务信息，这里会显示 AI 的建议</p>
            <p class="max-w-md text-xs text-muted-foreground/70">
              建议会经过 14 条护栏校验：科目必须存在、借贷必须平衡、
              辅助核算必须齐全、日期必须在未结账期间内……通不过的提议不会落到账上。
            </p>
          </div>
        </CardContent>
      </Card>

      <template v-else>
        <!-- 结论横幅 -->
        <Card :class="result.ok ? 'border-[var(--profit)]/40' : 'border-destructive/50'">
          <CardContent class="flex items-center gap-3 pt-5">
            <component
              :is="result.ok ? ShieldCheck : ShieldAlert"
              class="size-5 shrink-0"
              :class="result.ok ? 'text-[var(--profit)]' : 'text-[var(--loss)]'"
            />
            <div class="min-w-0 flex-1">
              <p class="text-sm font-medium">{{ result.summary }}</p>
              <p class="mt-0.5 flex flex-wrap gap-x-3 text-xs text-muted-foreground">
                <span v-if="result.model">模型 {{ result.model }}</span>
                <span v-if="result.latencyMs">耗时 {{ result.latencyMs }} ms</span>
                <span v-if="result.tokensIn || result.tokensOut">
                  token {{ result.tokensIn }} + {{ result.tokensOut }}
                </span>
                <span v-if="result.confidence">置信度 {{ result.confidence.toFixed(2) }}</span>
                <span v-if="result.suggestionId">审计编号 #{{ result.suggestionId }}</span>
              </p>
            </div>
            <Badge :variant="result.ok ? 'profit' : 'loss'">{{ result.ok ? '可确认' : '已拦下' }}</Badge>
          </CardContent>
        </Card>

        <!-- 护栏失败明细：摊开给用户看，而不是弹一句「AI 失败了」 -->
        <Card v-if="result.failures?.length" class="border-destructive/50">
          <CardHeader>
            <CardTitle class="text-[var(--loss)]">
              护栏拦下了这条建议（{{ result.failures?.length ?? 0 }} 项）
            </CardTitle>
            <CardDescription>
              这些内容<b>不会</b>写入账本。护栏刻意不自动「调平」——
              差额意味着模型理解错了业务，用尾差科目抹平只会把错误藏进账里。
            </CardDescription>
          </CardHeader>
          <CardContent class="flex flex-col gap-2">
            <div
              v-for="(f, i) in result.failures" :key="i"
              class="rounded-md border border-destructive/30 bg-destructive/5 p-2.5 text-sm"
            >
              <p class="font-medium">{{ f.title }}</p>
              <p class="mt-0.5 break-all text-xs text-muted-foreground">{{ f.detail }}</p>
            </div>
          </CardContent>
        </Card>

        <!-- 建议凭证 -->
        <Card v-if="result.voucher?.entries?.length">
          <CardHeader>
            <CardTitle>建议凭证（草稿）</CardTitle>
            <CardDescription>
              {{ result.voucher.word }} 字 · {{ result.voucher.bizDate }} · {{ result.voucher.remark }}
            </CardDescription>
          </CardHeader>
          <CardContent class="px-0">
            <Table>
              <thead>
                <tr class="border-b bg-muted/40">
                  <Th class="pl-5">科目</Th><Th>摘要</Th>
                  <Th align="right">借方</Th><Th align="right">贷方</Th><Th class="pr-5">辅助核算</Th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(e, i) in result.voucher.entries" :key="i" class="border-b last:border-0">
                  <Td class="pl-5 font-mono text-xs">{{ e.accountCode }}</Td>
                  <Td>{{ e.summary }}</Td>
                  <Td align="right" class="text-[var(--debit)]">{{ fmtMoney(e.debit, { blankZero: true }) }}</Td>
                  <Td align="right" class="text-[var(--credit)]">{{ fmtMoney(e.credit, { blankZero: true }) }}</Td>
                  <Td class="pr-5 text-xs text-muted-foreground">{{ e.auxDesc }}</Td>
                </tr>
                <tr class="bg-muted/40 font-medium">
                  <Td class="pl-5" colspan="2">合计</Td>
                  <Td align="right">{{ fmtMoney(integrity.debit) }}</Td>
                  <Td align="right">{{ fmtMoney(integrity.credit) }}</Td>
                  <Td class="pr-5">
                    <Badge :variant="integrity.balanced ? 'profit' : 'loss'">
                      {{ integrity.balanced ? '借贷平衡' : '不平衡' }}
                    </Badge>
                  </Td>
                </tr>
              </tbody>
            </Table>
          </CardContent>
        </Card>

        <!-- 提醒项 -->
        <Card v-if="result.warnings?.length" class="border-[var(--warn)]/40">
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-sm">
              <AlertTriangle class="size-4 text-[var(--warn)]" /> 提醒（不阻断）
            </CardTitle>
          </CardHeader>
          <CardContent class="flex flex-col gap-1.5 text-sm">
            <div v-for="(w, i) in result.warnings" :key="i">
              <span class="font-medium">{{ w.title }}</span>
              <span class="ml-2 text-xs text-muted-foreground">{{ w.detail }}</span>
            </div>
          </CardContent>
        </Card>

        <!-- 采纳 / 拒绝 -->
        <Card v-if="result.ok">
          <CardContent class="flex flex-wrap items-end gap-3 pt-5">
            <div class="w-56">
              <Label>确认人</Label>
              <Input v-model="operator" class="mt-1.5"
                     placeholder="记账签章上的姓名" />
            </div>
            <Button :disabled="deciding" @click="accept">
              <Check class="size-4" /> 采纳并生成草稿凭证
            </Button>
            <Button :disabled="deciding" variant="outline" @click="reject">
              这条不对
            </Button>
            <p class="w-full text-xs text-muted-foreground">
              生成的是<b>草稿</b>：还要你核对后过账。建议编号
              <code class="font-mono">#{{ result.suggestionId }}</code>
              已写入审计表 —— 事后可以查到这条建议是哪个模型、什么时候、
              按哪一版提示词给的。
            </p>
          </CardContent>
        </Card>

        <div class="flex items-center gap-2 text-xs text-muted-foreground">
          <History class="size-3.5" />
          每次建议都会写入审计表：模型原话、结构化提议、护栏结论、token 消耗。
          事后可以回答「这条建议是哪个模型、什么时候、基于什么给的」。
        </div>
      </template>
    </div>
  </div>
</template>
