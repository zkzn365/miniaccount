<script setup>
import { computed, nextTick, onMounted, ref } from 'vue'
import {
  Sparkles, Send, Eraser, Lock, Info, Check, X, Wand2, Eye, RotateCcw,
} from 'lucide-vue-next'
import { api, notify, DRAFT_HINT } from '@/lib/api'
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
import Spinner from '@/components/ui/Spinner.vue'

// ---------------------------------------------------------------------------
// AI 记账助手 —— 一个会问的会计
// ---------------------------------------------------------------------------
//
// 这里**只有**一个「业务描述」输入框。
//
// 原来是五样东西：业务类型、业务描述、金额、日期、对方户名、资金方向，
// 外加一个批量记账卡片。那五样其实是**会计要问的问题**，不是用户想说的话 ——
// 用户脑子里的一笔业务是「昨天买了台打印机」，逼他先想清楚
// 「这算银行流水还是自然语言」「资金方向是支出还是收入」，
// 等于让他自己先当一遍会计。
//
// 所以改成对话：你说一句，缺什么会计问什么。问的方式也讲究 ——
// 能穷举的给选项，推荐的那个放第一个（见后端 ask_user 工具）。
//
// ★ 会计给出的仍然是**草稿**：采纳走 AcceptAISuggestion，
// 过账仍然要等到账期结算。这条从 0.2.3 起就是硬规则。

const session = ref(null)
const input = ref('')
const busy = ref(false)
const listEl = ref(null)

// 记账签章落在自然人身上（见 lib/operator.js）：全程序一份
const operator = bookkeeper
const deciding = ref(false)

const turns = computed(() => session.value?.turns ?? [])
// 最后一条若是会计的追问、且用户还没回，就把选项渲染成可点的按钮
const pendingQuestion = computed(() => {
  const t = turns.value
  if (!t.length) return null
  const last = t[t.length - 1]
  if (last.role !== 'accountant' || !last.question) return null
  return last.question
})

onMounted(async () => {
  await loadBookkeeper()
  await loadPrompt()
})

async function scrollToEnd() {
  await nextTick()
  if (listEl.value) listEl.value.scrollTop = listEl.value.scrollHeight
}

async function send(text, selected) {
  const t = (text ?? input.value).trim()
  if (!t) { notify('先说说这笔业务', 'warn'); return }
  if (!operator.value.trim()) {
    notify('请先填写操作人 —— 采纳凭证时要签这个人的名', 'warn')
    return
  }
  await rememberBookkeeper(operator.value.trim())
  busy.value = true
  input.value = ''
  const r = await api.accountantSend({
    sessionId: session.value?.id ?? '',
    text: t,
    // 点选项时把「点的是哪一项」一起传上去：会计据此知道这是从它给的
    // 选项里挑的，而不是用户自己打的字 —— 两者该不该再确认一次不同
    selected: selected ?? [],
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  session.value = r.data
  await scrollToEnd()
}

/**
 * 点选项：把选项文字当成用户回答发出去。
 *
 * ★ 去掉「（推荐）」后缀，但**用原始 label 作为 selected** ——
 * 后缀是给用户看的提示，不是选项内容的一部分；
 * 而 selected 要能与模型给出的选项对上。
 */
async function pick(opt) {
  const label = String(opt.label).replace(/（推荐）$/, '')
  await send(label, [opt.label])
}

/**
 * 确认新建辅助核算档案。
 *
 * ★ 建档这一步由**界面**做，不是模型做的：它只能提议。
 * 与「AI 产物一律先是草稿」是同一条边界 —— 改账套的动作必须由人按下去。
 * 建完把结果当成一句用户消息发回去，会计接着往下记。
 */
async function confirmAux(turn) {
  const a = turn.aux
  if (!a) return
  deciding.value = true
  let r
  if (a.kind === 'employee') {
    // 字段按 Go 侧 EmployeeRequest 的真实标签来 —— 多传一个不存在的字段
    // 会被 request_fields_test 挡下（也会被 Wails 静默忽略）
    r = await api.saveEmployee({
      id: 0, code: a.code || '', name: a.name,
      deptId: a.deptId ?? null, enabled: true,
      baseSalary: '', siBase: '', hfbBase: '', specialAdditional: '',
      siProfile: '', hireDate: '', leaveDate: '', remark: '由 AI 会计提议新建',
    })
  } else {
    r = await api.saveDepartment({
      id: 0, code: a.code || '', name: a.name, parentId: null,
      enabled: true, remark: '由 AI 会计提议新建',
    })
  }
  deciding.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已新建${a.kindLabel}「${a.name}」`, 'success')
  await send(`（已新建${a.kindLabel}「${a.name}」，请继续）`)
}

async function reset() {
  if (turns.value.length && !confirm('清空这段对话？已经生成的凭证草稿不受影响。')) return
  const r = await api.accountantReset(session.value?.id ?? '')
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  session.value = r.data
  input.value = ''
}

/** 采纳会计给出的草稿。 */
async function accept(turn) {
  if (!turn.suggestionId) { notify('这条没有可采纳的凭证', 'warn'); return }
  if (!operator.value.trim()) { notify('请填写操作人 —— 凭证需要有记账签章', 'warn'); return }
  deciding.value = true
  const r = await api.acceptAISuggestion({
    id: turn.suggestionId, createdBy: operator.value.trim(),
  })
  deciding.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(r.data.summary, 'success', DRAFT_HINT)
}

/** 拒绝：理由进审计表，用于改进提示词。 */
async function reject(turn) {
  const reason = prompt('这笔哪里不对？（会记进审计表）') ?? ''
  deciding.value = true
  const r = await api.rejectAISuggestion({
    id: turn.suggestionId, createdBy: reason,
  })
  deciding.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已记录', 'success')
}

// ---- 执业要求（提示词）----
//
// ★ 这是唯一能改「这位会计怎么记账」的地方，所以放在这一页而不是设置页：
// 用户改它的动机永远来自「刚才那笔记得不对」，那一刻他就在这一页。
//
// 可改的只有中间那段「记账要求」。硬边界（科目闭集、借贷相等、
// 金额不许改）与输出格式由程序固定 —— 前者是「不许幻觉」的底线，
// 后者是护栏解析的依据，改了会直接读不出来。界面上必须写明这一点，
// 否则用户会以为「我改了没生效」。
const promptOpen = ref(false)
const prompt = ref(null)
const promptDraft = ref({ instructions: '' })
const promptBusy = ref(false)
const preview = ref(null)

async function loadPrompt() {
  const r = await api.aiPromptConfig()
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  prompt.value = r.data
  promptDraft.value = { instructions: r.data.instructions }
}

async function savePrompt() {
  promptBusy.value = true
  const r = await api.saveAIPromptConfig({
    instructions: promptDraft.value.instructions,
    taskNotes: prompt.value?.taskNotes ?? {},
  })
  promptBusy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('执业要求已保存 —— 下一句话就会按新的来', 'success')
  preview.value = null
  await loadPrompt()
}

async function resetPrompt() {
  if (!confirm('恢复成出厂默认？你自己改过的内容会丢掉。')) return
  promptBusy.value = true
  const r = await api.resetAIPromptConfig()
  promptBusy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已恢复出厂默认', 'success')
  preview.value = null
  await loadPrompt()
}

// ★ 预览走的是**会计版**提示词 —— 也就是这个页面真正发出去的那一份。
// 渲染成单次任务版的话，用户看到的东西少一节「与用户对话」，
// 而那一节恰好是「什么时候该问」。
async function doPreview() {
  promptBusy.value = true
  const r = await api.previewAccountantPrompt()
  promptBusy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  preview.value = r.data
}

const EXAMPLES = [
  '昨天买了台打印机，3000 块，开了专票',
  '收到杭州云帆科技有限公司货款 10600',
  '付了 8 月房租 12000，银行转账',
]
</script>

<template>
  <div class="mx-auto flex max-w-3xl flex-col gap-4">
    <!-- 对话 -->
    <Card>
      <CardContent class="pt-5">
        <!-- 开场 -->
        <div v-if="!turns.length" class="flex flex-col gap-3 py-6 text-center">
          <Sparkles class="mx-auto size-6 text-primary" />
          <p class="text-sm font-medium">说说这笔业务，我来记账</p>
          <p class="text-xs text-muted-foreground">
            用大白话就行 ——「昨天买了台打印机，3000 块，开了专票」。
            缺什么我会问你，问清楚了才给凭证。
          </p>
          <div class="mt-2 flex flex-wrap justify-center gap-2">
            <Button v-for="e in EXAMPLES" :key="e" variant="outline" size="sm"
                    :disabled="busy" @click="send(e)">
              {{ e }}
            </Button>
          </div>
        </div>

        <div v-else ref="listEl" class="flex max-h-[26rem] flex-col gap-3 overflow-y-auto pr-1">
          <template v-for="(t, i) in turns" :key="i">
            <!-- 用户 -->
            <div v-if="t.role === 'user'" class="flex justify-end">
              <div class="max-w-[80%] rounded-lg bg-primary/10 px-3 py-2 text-sm">
                {{ t.text }}
              </div>
            </div>

            <!-- 会计 -->
            <div v-else class="flex flex-col gap-2">
              <div class="flex items-start gap-2">
                <Sparkles class="mt-0.5 size-4 shrink-0 text-primary" />
                <div class="min-w-0 flex-1">
                  <p v-if="t.text" class="whitespace-pre-wrap text-sm">{{ t.text }}</p>
                  <p v-else class="text-sm text-muted-foreground">（在问你）</p>
                  <p v-if="t.model" class="mt-1 text-xs text-muted-foreground">
                    {{ t.model }}
                    <span v-if="t.tokensIn || t.tokensOut">
                      · token {{ (t.tokensIn || 0) + (t.tokensOut || 0) }}
                    </span>
                  </p>
                </div>
              </div>

              <!-- 追问：选项 -->
              <div
                v-if="t.question && i === turns.length - 1"
                class="ml-6 flex flex-col gap-2 rounded-lg border p-3"
              >
                <p v-if="t.question.header" class="text-xs font-medium text-muted-foreground">
                  {{ t.question.header }}
                </p>
                <div v-if="t.question.options?.length" class="flex flex-col gap-1.5">
                  <button
                    v-for="(o, k) in t.question.options" :key="k"
                    class="rounded-md border px-3 py-2 text-left text-sm hover:bg-accent/60"
                    :class="o.recommended ? 'border-primary/50' : ''"
                    :disabled="busy"
                    @click="pick(o)"
                  >
                    <span class="font-medium">{{ o.label }}</span>
                    <span v-if="o.description" class="ml-2 text-xs text-muted-foreground">
                      {{ o.description }}
                    </span>
                  </button>
                </div>
                <p v-else class="text-xs text-muted-foreground">在下面直接回答就行</p>
              </div>

              <!-- 提议新建档案 -->
              <div v-if="t.aux" class="ml-6 rounded-lg border border-primary/40 bg-primary/5 p-3">
                <p class="text-sm font-medium">
                  建议新建{{ t.aux.kindLabel }}「{{ t.aux.name }}」
                </p>
                <p class="mt-1 text-xs text-muted-foreground">{{ t.aux.reason }}</p>
                <div class="mt-2 flex items-center gap-2">
                  <Button size="sm" :disabled="deciding || busy" @click="confirmAux(t)">
                    <Check class="size-3.5" /> 新建并继续
                  </Button>
                  <Button size="sm" variant="ghost" :disabled="deciding || busy"
                          @click="send('（先不建，换一种记法）')">
                    先不建
                  </Button>
                  <span class="text-xs text-muted-foreground">
                    建完会回到对话，接着记这笔
                  </span>
                </div>
              </div>

              <!-- 凭证草稿 -->
              <div v-if="t.voucher" class="ml-6 rounded-lg border">
                <div class="flex flex-wrap items-center gap-2 border-b bg-muted/30 px-3 py-2">
                  <Badge :variant="t.passed ? 'profit' : 'warn'">
                    {{ t.passed ? '通过护栏' : '未通过护栏' }}
                  </Badge>
                  <span class="text-xs text-muted-foreground">
                    {{ t.voucher.bizDate }} · {{ t.voucher.remark }} ·
                    {{ fmtMoney(t.voucher.total) }}
                  </span>
                  <div class="ml-auto flex items-center gap-1.5">
                    <Button v-if="t.passed" size="sm" :disabled="deciding" @click="accept(t)">
                      <Check class="size-3.5" /> 采纳为草稿
                    </Button>
                    <Button size="sm" variant="ghost" :disabled="deciding" @click="reject(t)">
                      <X class="size-3.5" /> 不对
                    </Button>
                  </div>
                </div>
                <table class="w-full text-sm">
                  <thead>
                    <tr class="border-b bg-muted/20">
                      <th class="h-8 px-3 text-left text-xs font-medium text-muted-foreground">摘要</th>
                      <th class="h-8 px-3 text-left text-xs font-medium text-muted-foreground">科目</th>
                      <th class="h-8 px-3 text-left text-xs font-medium text-muted-foreground">辅助核算</th>
                      <th class="h-8 px-3 text-right text-xs font-medium text-muted-foreground">借方</th>
                      <th class="h-8 px-3 text-right text-xs font-medium text-muted-foreground">贷方</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="(e, k) in t.voucher.entries" :key="k" class="border-b last:border-0">
                      <td class="px-3 py-1.5">{{ e.summary }}</td>
                      <td class="px-3 py-1.5 font-mono text-xs">{{ e.accountCode }}</td>
                      <td class="px-3 py-1.5 text-xs text-muted-foreground">{{ e.auxDesc || '—' }}</td>
                      <td class="num px-3 py-1.5 text-right text-[var(--debit)]">
                        {{ fmtMoney(e.debit, { blankZero: true }) }}
                      </td>
                      <td class="num px-3 py-1.5 text-right text-[var(--credit)]">
                        {{ fmtMoney(e.credit, { blankZero: true }) }}
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>

              <ul v-if="t.failures?.length"
                  class="ml-6 list-disc rounded border border-destructive/40 bg-destructive/5 py-2 pl-9 pr-3 text-xs text-[var(--loss)]">
                <li v-for="(f, k) in t.failures" :key="k">{{ f }}</li>
              </ul>
              <ul v-if="t.warnings?.length"
                  class="ml-6 list-disc rounded border border-[var(--warn)]/40 py-2 pl-9 pr-3 text-xs text-[var(--warn)]">
                <li v-for="(w, k) in t.warnings" :key="k">{{ w }}</li>
              </ul>
            </div>
          </template>

          <div v-if="busy" class="flex items-center gap-2 text-sm text-muted-foreground">
            <Spinner /> 会计正在想…
          </div>
        </div>
      </CardContent>
    </Card>

    <!-- 只有一个输入框 -->
    <Card>
      <CardContent class="flex flex-col gap-2 pt-5">
        <textarea
          v-model="input"
          rows="2"
          class="w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          :placeholder="pendingQuestion ? '回答上面的问题…' : '例：昨天买了台打印机，3000 块，开了专票'"
          :disabled="busy"
          @keydown.enter.exact.prevent="send()"
        />
        <div class="flex items-center gap-2">
          <Input v-model="operator"
                 @change="rememberBookkeeper(operator)" class="h-8 w-32" placeholder="操作人" />
          <span class="flex items-center gap-1 text-xs text-muted-foreground">
            <Info class="size-3" /> 采纳时的签章
          </span>
          <div class="ml-auto flex items-center gap-2">
            <Button v-if="turns.length" variant="ghost" size="sm" :disabled="busy" @click="reset">
              <Eraser class="size-3.5" /> 换一笔
            </Button>
            <Button :disabled="busy || !input.trim()" @click="send()">
              <Send class="size-3.5" /> 发送
            </Button>
          </div>
        </div>
        <p class="text-xs text-muted-foreground">
          Enter 发送。会计给出的凭证是<b>草稿</b>：不占凭证号、不进总账，
          到「账期管理」结账时才统一过账。
        </p>
      </CardContent>
    </Card>

    <!-- 执业要求（提示词）-->
    <Card>
      <CardHeader>
        <button class="flex w-full items-center gap-2 text-left" @click="promptOpen = !promptOpen">
          <Wand2 class="size-4 text-primary" />
          <CardTitle class="flex-1">这位会计的执业要求</CardTitle>
          <Badge :variant="prompt?.custom ? 'warn' : 'muted'">
            {{ prompt?.custom ? '已自定义' : '出厂默认' }}
          </Badge>
        </button>
        <CardDescription>
          比如「我们是小规模纳税人，进项税一律不抵扣」「房租走 560210，不要用 560204」。
          改完下一句话就生效。硬边界（科目只能选账套里有的、借贷必须相等、
          金额不许改）由程序固定，改不了 —— 那是护栏的地基。
        </CardDescription>
      </CardHeader>
      <CardContent v-if="promptOpen" class="flex flex-col gap-3">
        <div>
          <Label>记账要求</Label>
          <textarea
            v-model="promptDraft.instructions"
            rows="8"
            class="mt-1.5 w-full rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <Button size="sm" :disabled="promptBusy" @click="savePrompt">
            <Wand2 class="size-3.5" /> 保存
          </Button>
          <Button size="sm" variant="outline" :disabled="promptBusy" @click="doPreview">
            <Eye class="size-3.5" /> 预览实际提示词
          </Button>
          <Button size="sm" variant="ghost" :disabled="promptBusy || !prompt?.custom"
                  @click="resetPrompt">
            <RotateCcw class="size-3.5" /> 恢复出厂默认
          </Button>
          <span v-if="preview" class="text-xs text-muted-foreground">
            系统提示词 {{ preview.chars }} 字 · 指纹 {{ String(preview.digest).slice(0, 12) }}
          </span>
        </div>
        <div v-if="preview" class="rounded-lg border bg-muted/20">
          <p class="border-b px-3 py-1.5 text-xs font-medium text-muted-foreground">
            实际发给模型的系统提示词（含「与用户对话」一节）
          </p>
          <pre class="max-h-72 overflow-auto whitespace-pre-wrap px-3 py-2 text-xs">{{ preview.system }}</pre>
        </div>
      </CardContent>
    </Card>

    <!-- 硬边界说明 -->
    <div class="flex items-start gap-2 rounded-lg border px-3 py-2 text-xs text-muted-foreground">
      <Lock class="mt-0.5 size-3.5 shrink-0" />
      <p>
        会计能做的最坏一件事，是给出一张<b>通不过护栏</b>的凭证 —— 他不能写账。
        科目只能从账套里已有的明细科目里选，借贷必须精确相等，
        金额不许改你报的数。采纳之后落下来的仍然是草稿，
        过账要人到账期管理去按。
      </p>
    </div>
  </div>
</template>
