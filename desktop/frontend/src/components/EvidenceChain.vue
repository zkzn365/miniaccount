<script setup>
import { computed, ref, watch } from 'vue'
import {
  Paperclip, Trash2, Link2, AlertTriangle, CheckCircle2, FileSearch, ExternalLink,
} from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
import { bookkeeper } from '@/lib/operator'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import CardHeader from '@/components/ui/CardHeader.vue'
import CardTitle from '@/components/ui/CardTitle.vue'
import CardDescription from '@/components/ui/CardDescription.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Modal from '@/components/ui/Modal.vue'
import Spinner from '@/components/ui/Spinner.vue'

// ---------------------------------------------------------------------------
// 审计证据链
// ---------------------------------------------------------------------------
//
// 这一块的用处只有一个：让底稿上的结论有出处。
//
// 所以它显示的不是「有几份文件」，而是三种状态：
//
//   依据齐了  ·  还没有依据  ·  依据找不到了
//
// ★ 第三种最要紧。底稿上写着「已取得折旧计算表」，而那份文件
// 早就不在了 —— 比一开始就没写更糟，因为它提供了假的确定感。
// 所以「原件还在不在」是每次打开这一页现查的（见后端 Missing）。

const props = defineProps({
  // 会计期间，形如 "2025-03"。
  //
  // ★ 传期间字符串而不是拆好的年月：界面初始渲染时期间还没读出来，
  // 传数字会先传一个 0 进来（0000-00），组件就带着那个错期间去查了。
  period: { type: String, required: true },
  // 底稿那页的操作人（挂依据要签名）
  operator: { type: String, default: '' },
})

const data = ref(null)
const kinds = ref([])
const loading = ref(true)
const busy = ref(false)

function periodParts() {
  const [y, m] = String(props.period ?? '').split('-')
  return { year: Number(y), month: Number(m) }
}

async function load() {
  const { year, month } = periodParts()
  if (!year || !month) return
  loading.value = true
  const [e, k] = await Promise.all([
    api.evidence({ year, month }),
    api.evidenceKinds(),
  ])
  loading.value = false
  if (!e.ok) { notify(e.fault.message, 'error', e.fault.detail); return }
  data.value = e.data
  if (k.ok) kinds.value = k.data ?? []
}

// 期间一变就重查 —— 切期间时组件不会重建（是同一页），
// 只在 onMounted 里查的话，换一期看到的还是上一期的证据
watch(() => props.period, load, { immediate: true })
defineExpose({ load })

const chains = computed(() => data.value?.chains ?? [])
const byName = computed(() => props.operator || bookkeeper.value)

/** 状态徽标：齐 = 绿、没有 = 金（要补）、丢了 = 红。 */
function stateOf(c) {
  if (c.links.some((l) => l.missing)) return { label: '依据丢失', variant: 'loss' }
  if (!c.links.length) return { label: '还没有依据', variant: 'warn' }
  return { label: '依据齐', variant: 'profit' }
}

// ---------------------------------------------------------------- 挂依据

const addOpen = ref(false)
const form = ref(emptyForm())
const fileInput = ref(null)

function emptyForm() {
  return {
    ownerType: '', ownerId: 0, targetTitle: '',
    refKind: 'attachment', refId: '', refLabel: '', note: '',
    fileName: '', dataBase64: '',
  }
}

function openAdd(c) {
  form.value = { ...emptyForm(), ownerType: c.ownerType, ownerId: c.ownerId, targetTitle: c.title }
  addOpen.value = true
}

/** 需要填单据 id 的证据种类。 */
const DOC_KINDS = ['voucher', 'invoice', 'bank_flow', 'expense']
const needDocID = computed(() => DOC_KINDS.includes(form.value.refKind))
const isFile = computed(() => form.value.refKind === 'attachment')

function pickFile() {
  const el = fileInput.value
  if (!el) return
  el.value = ''
  el.onchange = async () => {
    const f = el.files?.[0]
    if (!f) return
    const buf = await f.arrayBuffer()
    form.value.fileName = f.name
    form.value.dataBase64 = b64(buf)
    if (!form.value.refLabel) form.value.refLabel = f.name
  }
  el.click()
}

// WebView 里拿不到本地文件路径（浏览器的 File 对象出于安全不暴露路径），
// 所以内容以 base64 传给 Go —— 与凭证附件同一条路。
function b64(buf) {
  const bytes = new Uint8Array(buf)
  let bin = ''
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) {
    bin += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk))
  }
  return btoa(bin)
}

async function save() {
  const f = form.value
  if (!byName.value.trim()) { notify('请先填写记账人 —— 底稿要记清这份依据是谁挂上的', 'warn'); return }
  if (isFile.value && !f.dataBase64) { notify('请选择一份文件', 'warn'); return }
  if (needDocID.value && !Number(f.refId)) { notify('请填写单据 id', 'warn'); return }
  if (!needDocID.value && !isFile.value && !f.refLabel.trim()) {
    notify('请写清这份资料是什么', 'warn')
    return
  }
  busy.value = true
  const r = await api.addEvidence({
    ownerType: f.ownerType, ownerId: f.ownerId, refKind: f.refKind,
    refId: Number(f.refId) || 0, refLabel: f.refLabel.trim(), note: f.note.trim(),
    hash: '', fileName: f.fileName, dataBase64: f.dataBase64,
    by: byName.value.trim(),
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已挂上依据', 'success')
  addOpen.value = false
  data.value = r.data
}

async function remove(link, chain) {
  if (!byName.value.trim()) { notify('请先填写记账人', 'warn'); return }
  if (!window.confirm(`摘掉这条依据？\n\n${link.refLabel}\n\n` +
    `底稿上就不再显示「${chain.title}」有这份依据了。`)) return
  busy.value = true
  const r = await api.deleteEvidence({ id: link.id, by: byName.value.trim() })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('已摘掉依据', 'success')
  data.value = r.data
}

/** 用系统程序打开原件。 */
async function openOriginal(link) {
  const r = await api.openAttachment(link.hash)
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail) }
}

function kindLabel(v) {
  return kinds.value.find((k) => k.value === v)?.label ?? v
}
</script>

<template>
  <Card>
    <CardHeader>
      <div class="flex items-start justify-between gap-3">
        <div>
          <CardTitle>审计证据链</CardTitle>
          <CardDescription>
            每条结论后面站着什么。依据分三类：<b>附件</b>（有文件）、
            <b>单据</b>（凭证 / 发票 / 流水 / 报销单）、
            <b>外部资料</b>（纸质回函、访谈记录）。
          </CardDescription>
        </div>
        <Button size="sm" variant="outline" :disabled="loading" @click="load">
          <FileSearch /> 核对原件
        </Button>
      </div>
    </CardHeader>
    <CardContent class="space-y-3">
      <Spinner v-if="loading" />

      <template v-else-if="data">
        <div class="flex items-start gap-2 rounded-lg border p-3 text-sm"
             :class="data.unsupported || data.broken
               ? 'border-[var(--warn)]/50 bg-[var(--gold)]/8' : 'bg-muted/30'">
          <component :is="data.unsupported || data.broken ? AlertTriangle : CheckCircle2"
                     class="mt-0.5 size-4 shrink-0" />
          <div>
            <p class="font-medium">{{ data.concludes }}</p>
            <ul v-if="data.nextActions?.length" class="mt-1 list-disc pl-5 text-muted-foreground">
              <li v-for="(a, i) in data.nextActions" :key="i">{{ a }}</li>
            </ul>
          </div>
        </div>

        <div v-for="c in chains" :key="c.ownerType + '/' + c.ownerId"
             class="rounded-lg border p-3">
          <div class="flex flex-wrap items-start justify-between gap-2">
            <div>
              <div class="flex flex-wrap items-center gap-2">
                <Badge variant="muted">{{ c.ownerTypeLabel }}</Badge>
                <span class="text-sm font-medium">{{ c.title }}</span>
                <Badge :variant="stateOf(c).variant">{{ stateOf(c).label }}</Badge>
              </div>
              <p class="mt-1 text-xs text-muted-foreground">{{ c.concludes }}</p>
            </div>
            <Button size="sm" variant="outline" @click="openAdd(c)">
              <Paperclip /> 附上资料
            </Button>
          </div>

          <table v-if="c.links.length" class="mt-2 w-full text-sm">
            <tbody>
              <tr v-for="l in c.links" :key="l.id" class="border-t last:border-0">
                <td class="px-2 py-1.5">
                  <div class="flex items-center gap-2">
                    <component :is="l.hasFile ? Paperclip : Link2"
                               class="size-3.5 shrink-0 text-muted-foreground" />
                    <span :class="l.missing ? 'text-[var(--loss)] line-through' : ''">
                      {{ l.refLabel || l.fileName }}
                    </span>
                    <Badge variant="muted">{{ l.refKindLabel }}</Badge>
                    <Badge v-if="l.missing" variant="loss">原件已找不到</Badge>
                  </div>
                  <div v-if="l.note" class="mt-0.5 text-xs text-muted-foreground">
                    说明：{{ l.note }}
                  </div>
                  <div class="mt-0.5 text-xs text-muted-foreground">
                    {{ l.linkedBy }} 挂上<template v-if="l.linkedAt"> · {{ l.linkedAt.slice(0, 10) }}</template>
                  </div>
                </td>
                <td class="w-40 px-2 py-1.5 text-right">
                  <div class="flex justify-end gap-1">
                    <Button v-if="l.hasFile && !l.missing" size="sm" variant="ghost"
                            title="用系统程序打开原件" @click="openOriginal(l)">
                      <ExternalLink class="size-3.5" />
                    </Button>
                    <Button size="sm" variant="ghost" :disabled="busy"
                            title="摘掉这条依据" @click="remove(l, c)">
                      <Trash2 class="size-3.5" />
                    </Button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
          <p v-else class="mt-2 text-xs text-muted-foreground">
            还没有附依据 —— 结论要有出处，复核人才判断得了。
          </p>
        </div>
      </template>
    </CardContent>

    <!-- 挂依据 -->
    <Modal v-model:open="addOpen" title="附上依据"
           description="一份资料只算一份依据；同一份资料可以同时支持多条结论（再挂一次即可）。">
      <div class="space-y-4">
        <div class="rounded-lg border bg-muted/30 p-3 text-sm">
          要证明的结论：{{ form.targetTitle }}
        </div>
        <div class="grid gap-3 sm:grid-cols-2">
          <div>
            <Label>资料类型</Label>
            <select v-model="form.refKind"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option v-for="k in kinds" :key="k.value" :value="k.value">{{ k.label }}</option>
            </select>
            <p class="mt-1 text-xs text-muted-foreground">
              会计凭证、发票、银行流水、报销单都能直接指过去，不用再存一份文件。
            </p>
          </div>
          <div v-if="needDocID">
            <Label>单据 id</Label>
            <Input v-model="form.refId" class="num mt-1.5" placeholder="12" />
            <p class="mt-1 text-xs text-muted-foreground">
              单据的名称由程序按 id 查出来记在底稿上 —— 手写名字迟早核到另一份单据上。
            </p>
          </div>
          <div v-else-if="isFile">
            <Label>文件</Label>
            <div class="mt-1.5 flex items-center gap-2">
              <Button size="sm" variant="outline" @click="pickFile">
                <Paperclip /> 选择文件
              </Button>
              <span class="text-xs text-muted-foreground">
                {{ form.fileName || '（支持 PDF / 图片 / Excel，单个 20MB 以内）' }}
              </span>
            </div>
            <input ref="fileInput" type="file" class="hidden"
                   accept=".pdf,.jpg,.jpeg,.png,.ofd,.xlsx,.xls,.docx,.csv" />
          </div>
        </div>
        <div>
          <Label>这份资料是什么</Label>
          <Input v-model="form.refLabel" class="mt-1.5"
                 placeholder="折旧计算表（底稿索引 F-3）" />
          <p class="mt-1 text-xs text-muted-foreground">
            附件的默认就是文件名；单据类留空时程序会自动填。
          </p>
        </div>
        <div>
          <Label>它说明了什么</Label>
          <textarea v-model="form.note" rows="2"
                    class="mt-1.5 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm"
                    placeholder="表上少提折旧 3,000.00，与调整金额一致" />
        </div>
      </div>
      <template #footer>
        <Button variant="outline" @click="addOpen = false">取消</Button>
        <Button :disabled="busy" @click="save">挂上</Button>
      </template>
    </Modal>
  </Card>
</template>
