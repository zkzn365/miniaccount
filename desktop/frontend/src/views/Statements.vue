<script setup>
import { computed, onMounted, ref } from 'vue'
import { Printer, RefreshCw, FileText, Copy, Users } from 'lucide-vue-next'
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

const contacts = ref([])
const st = ref(null)
const loading = ref(false)
const filter = ref({ contactId: null, from: '', to: '', accountPrefix: '' })

// 载入本期有往来发生的单位 —— 会计到月底不需要一个个去挑
async function loadContacts() {
  const r = await api.activeContacts(filter.value.from, filter.value.to)
  if (r.ok) contacts.value = r.data ?? []
}

async function load() {
  if (!filter.value.contactId) { notify('请选择往来单位', 'warn'); return }
  loading.value = true
  const r = await api.statement({
    contactId: filter.value.contactId,
    from: filter.value.from,
    to: filter.value.to,
    accountPrefix: filter.value.accountPrefix,
  })
  loading.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); st.value = null; return }
  st.value = r.data
}

onMounted(loadContacts)

function printPage() { window.print() }

// 复制纯文本版式 —— 会计经常要把对账单贴进邮件或微信发给对方，
// 纯文本比截图好用（对方还能直接在上面回复确认）
async function copyText() {
  if (!st.value) return
  try {
    await navigator.clipboard.writeText(st.value.text)
    notify('对账单文本已复制，可直接粘贴到邮件或微信', 'success')
  } catch (e) {
    notify('复制失败，可以手动选中右侧文本复制', 'warn')
  }
}

const dirTone = (d) => (d === '借' ? 'debit' : d === '贷' ? 'credit' : 'muted')

// 期末余额的解读：告诉用户这个数字对我们意味着什么
const closingHint = computed(() => {
  if (!st.value) return ''
  const d = st.value.closingDir
  if (d === '平') return '双方已结清'
  if (st.value.contactKindLabel.includes('客户')) {
    return d === '借' ? '对方尚欠我方' : '我方预收对方'
  }
  return d === '贷' ? '我方尚欠对方' : '我方预付对方'
})
</script>

<template>
  <div class="flex flex-col gap-4">
    <div class="flex flex-wrap items-end gap-3 no-print">
      <div class="w-64">
        <Label>往来单位</Label>
        <select
          v-model.number="filter.contactId"
          class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm"
        >
          <option :value="null">请选择</option>
          <option v-for="c in contacts" :key="c.id" :value="c.id">
            {{ c.name }}（{{ c.kindLabel }}）
          </option>
        </select>
        <p class="mt-1 text-[11px] text-muted-foreground">
          只列出<span v-if="filter.from || filter.to">该期间</span><span v-else>本期</span>有往来发生的单位
        </p>
      </div>
      <div class="w-36">
        <Label>对账期间（起）</Label>
        <Input v-model="filter.from" class="mt-1.5" placeholder="留空 = 当前账期" />
      </div>
      <div class="w-36">
        <Label>对账期间（止）</Label>
        <Input v-model="filter.to" class="mt-1.5" placeholder="留空 = 当前账期" />
      </div>
      <div class="w-40">
        <Label>科目范围</Label>
        <select
          v-model="filter.accountPrefix"
          class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm"
        >
          <option value="">全部往来科目</option>
          <option value="1122">只看应收账款</option>
          <option value="2202">只看应付账款</option>
          <option value="2203">只看预收账款</option>
          <option value="1123">只看预付账款</option>
        </select>
      </div>
      <Button :disabled="loading" @click="load">
        <RefreshCw :class="loading ? 'animate-spin' : ''" /> 生成
      </Button>
      <Button v-if="st" variant="outline" @click="printPage"><Printer /> 打印 / PDF</Button>
      <Button v-if="st" variant="outline" @click="copyText"><Copy /> 复制文本</Button>
    </div>

    <Spinner v-if="loading" />

    <EmptyState
      v-else-if="!st"
      title="选择往来单位后生成对账单"
      description="对账单是发给对方盖章核对的：期初余额 + 本期逐笔往来 + 期末余额，双方签字后即为账面一致的凭据"
    >
      <div v-if="contacts.length === 0" class="flex items-center gap-2 text-xs text-muted-foreground">
        <Users class="size-3.5" />
        这个期间还没有往来发生
      </div>
    </EmptyState>

    <template v-else>
      <!-- 一句话结论：拿到这张纸的人最先想知道的就是「到底欠多少」 -->
      <Card :class="st.closingDir === '平' ? 'border-[var(--profit)]/40' : ''">
        <CardContent class="flex items-center gap-4 pt-5">
          <div>
            <p class="text-xs text-muted-foreground">
              {{ st.from }} ~ {{ st.to }} · {{ st.contactName }}（{{ st.contactKindLabel }}）
            </p>
            <p class="mt-1.5 flex items-baseline gap-2">
              <span class="text-sm text-muted-foreground">期末余额</span>
              <Badge :variant="dirTone(st.closingDir)">{{ st.closingDir }}</Badge>
              <span class="num text-2xl font-semibold">{{ fmtMoney(st.closing.abs ? st.closing : st.closing) }}</span>
            </p>
            <p class="mt-1 text-xs text-muted-foreground">
              {{ closingHint }} · 大写：{{ st.closingUpper }}
            </p>
          </div>
          <div class="ml-auto text-right text-sm">
            <p class="text-muted-foreground">
              期初 <b class="num text-foreground">{{ st.openingDir }} {{ fmtMoney(st.opening) }}</b>
            </p>
            <p class="text-muted-foreground">
              本期借 <b class="num text-foreground">{{ fmtMoney(st.totalDebit) }}</b>
              贷 <b class="num text-foreground">{{ fmtMoney(st.totalCredit) }}</b>
            </p>
          </div>
        </CardContent>
      </Card>

      <!-- 对账单正文：版式刻意做得像一张纸 -->
      <Card>
        <CardHeader>
          <CardTitle class="text-center text-lg">往来对账单</CardTitle>
          <CardDescription class="text-center">
            对账期间 {{ st.from }} 至 {{ st.to }}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div class="mb-4 grid grid-cols-3 gap-x-6 gap-y-1 text-sm">
            <span class="text-muted-foreground">单位名称</span>
            <span class="col-span-2">{{ st.companyName }}</span>
            <span class="text-muted-foreground">往来单位</span>
            <span class="col-span-2">{{ st.contactName }}（{{ st.contactKindLabel }}）</span>
            <template v-if="st.contactTaxNo">
              <span class="text-muted-foreground">纳税人识别号</span>
              <span class="col-span-2">{{ st.contactTaxNo }}</span>
            </template>
            <template v-if="st.accountName">
              <span class="text-muted-foreground">科目</span>
              <span class="col-span-2 font-mono text-xs">{{ st.accountCode }} {{ st.accountName }}</span>
            </template>
            <template v-else-if="st.mixed">
              <span class="text-muted-foreground">科目</span>
              <span class="col-span-2 text-xs">多个（见各行标注）</span>
            </template>
          </div>

          <div class="rounded-lg border">
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b bg-muted/40">
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">日期</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">凭证号</th>
                  <th class="h-9 px-3 text-left text-xs font-medium text-muted-foreground">摘要</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">借方</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">贷方</th>
                  <th class="h-9 px-3 text-right text-xs font-medium text-muted-foreground">余额</th>
                  <th class="h-9 px-3 text-center text-xs font-medium text-muted-foreground">方向</th>
                </tr>
              </thead>
              <tbody>
                <tr class="border-b bg-muted/20">
                  <td class="px-3 py-1.5 text-xs text-muted-foreground" colspan="3">期初余额</td>
                  <td colspan="2" />
                  <td class="num px-3 py-1.5 font-medium">{{ fmtMoney(st.opening) }}</td>
                  <td class="px-3 py-1.5 text-center">
                    <Badge :variant="dirTone(st.openingDir)">{{ st.openingDir }}</Badge>
                  </td>
                </tr>
                <tr v-for="(l, i) in st.lines" :key="i" class="border-b last:border-0">
                  <td class="px-3 py-1.5 whitespace-nowrap text-xs">{{ l.date }}</td>
                  <td class="px-3 py-1.5 whitespace-nowrap font-mono text-xs">{{ l.voucherNo }}</td>
                  <td class="px-3 py-1.5">{{ l.summary }}</td>
                  <td class="num px-3 py-1.5 text-[var(--debit)]">{{ fmtMoney(l.debit, { blankZero: true }) }}</td>
                  <td class="num px-3 py-1.5 text-[var(--credit)]">{{ fmtMoney(l.credit, { blankZero: true }) }}</td>
                  <td class="num px-3 py-1.5 font-medium">{{ fmtMoney(l.balance) }}</td>
                  <td class="px-3 py-1.5 text-center">
                    <Badge :variant="dirTone(l.dir)">{{ l.dir }}</Badge>
                  </td>
                </tr>
                <tr v-if="st.lines.length === 0">
                  <td colspan="7" class="px-3 py-6 text-center text-sm text-muted-foreground">
                    本期无往来发生
                  </td>
                </tr>
                <tr class="border-t bg-muted/40 font-medium">
                  <td class="px-3 py-2" colspan="3">本期合计</td>
                  <td class="num px-3 py-2">{{ fmtMoney(st.totalDebit) }}</td>
                  <td class="num px-3 py-2">{{ fmtMoney(st.totalCredit) }}</td>
                  <td colspan="2" />
                </tr>
                <tr class="border-t-2 bg-muted/40 font-semibold">
                  <td class="px-3 py-2" colspan="3">期末余额</td>
                  <td colspan="2" />
                  <td class="num px-3 py-2">{{ fmtMoney(st.closing) }}</td>
                  <td class="px-3 py-2 text-center">
                    <Badge :variant="dirTone(st.closingDir)">{{ st.closingDir }}</Badge>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <p class="mt-3 text-center text-xs text-muted-foreground">
            本单位账面记录如上，请核对无误后签章退回。
          </p>

          <!-- 盖章栏：对账单的意义就在这两个章上 -->
          <div class="mt-8 grid grid-cols-2 gap-12 text-sm">
            <div>
              <p class="text-muted-foreground">我方（盖章）</p>
              <p class="mt-6">{{ st.companyName }}</p>
              <p class="mt-4 text-muted-foreground">日期：　　　年　　月　　日</p>
            </div>
            <div>
              <p class="text-muted-foreground">对方（盖章）</p>
              <p class="mt-6">{{ st.contactName }}</p>
              <p class="mt-4 text-muted-foreground">日期：　　　年　　月　　日</p>
            </div>
          </div>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
