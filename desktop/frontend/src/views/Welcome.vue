<script setup>
import { onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { FolderOpen, FilePlus2, ShieldCheck, Sparkles, FolderSearch } from 'lucide-vue-next'
import { api, notify, hasWails } from '@/lib/api'
import { refreshBook } from '@/lib/book'
import Card from '@/components/ui/Card.vue'
import CardHeader from '@/components/ui/CardHeader.vue'
import CardTitle from '@/components/ui/CardTitle.vue'
import CardDescription from '@/components/ui/CardDescription.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'

const emit = defineEmits(['book-changed'])
const router = useRouter()

// 建账/打开成功之后：先把账套状态刷新好，再跳转。
//
// ★ 顺序不能反。路由守卫要拦「没有账套还往业务页面走」，
// 而 refreshBook 是异步的 —— 先跳转就会被守卫挡回欢迎页，
// 用户看到的是「建完账又回来了」。
async function onReady() {
  await refreshBook()
  emit('book-changed')
}

// choose | open | create
//
// ★ 有账套时先进入 choose：启动就替用户决定「打开哪一本」是危险的 ——
// 他可能想新建，也可能想打开另一本。默认选中上次那本，
// 直接回车就是打开，代价一次点击。
const mode = ref('open')
const openPath = ref('')

// ---- 账套目录扫描 ----
const books = ref(null)     // BookList
const picked = ref('')      // 选中的账套路径
const scanning = ref(true)

async function scanBooks() {
  scanning.value = true
  const r = await api.listBooks()
  scanning.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  books.value = r.data
  picked.value = r.data.suggested ?? ''
  // 目录里有账套 → 先问「打开还是新建」；没有 → 直接进新建表单
  mode.value = r.data.hasAny ? 'choose' : 'create'
  if (!r.data.hasAny) await loadDefaultPath()
}
scanBooks()

function fmtSize(n) {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB']
  let i = 0, v = n
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${u[i]}`
}

function fmtTime(s) {
  if (!s) return ''
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-` +
    `${String(d.getDate()).padStart(2, '0')} ${String(d.getHours()).padStart(2, '0')}:` +
    `${String(d.getMinutes()).padStart(2, '0')}`
}

async function openPicked() {
  if (!picked.value) { notify('请先选一个账套', 'warn'); return }
  busy.value = true
  const r = await api.openBook(picked.value)
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  if (!r.data.open) {
    newPath.value = r.data.path ?? picked.value
    pathEdited.value = true
    mode.value = 'create'
    notify('这个文件还不是账套，请先建账', 'warn')
    return
  }
  await onReady()
  router.replace('/dashboard')
}
const busy = ref(false)

const form = ref({
  companyName: '',
  creditCode: '',
  legalPerson: '',
  // 增值税纳税人身份（流转税）—— 决定能否抵扣进项。
  //
  // ★ 默认小规模纳税人：这款软件面向小微企业，绝大多数是小规模。
  // 这只是一个**表单默认值**，用户点一下就改了 —— 与「程序按销售额
  // 自动推导身份」是两件事，后者绝对不做（身份由税务登记决定）。
  vatStatus: 'small_scale',
  // 企业规模类型（所得税与统计）—— 决定能否享受小型微利企业优惠
  enterpriseScale: '',
  vatStatusEffectiveFrom: '',
  startYear: new Date().getFullYear(),
  startMonth: 1,
  currentYear: new Date().getFullYear(),
  currentMonth: new Date().getMonth() + 1,
})
// 账套保存位置。
//
// ★ 有默认值，用户什么都不用填就能建账。
// 原来这一栏是空的，点「建立账套」只会弹「请填写账套文件的保存位置」——
// 而第一次打开软件的人根本不知道该填什么。
const newPath = ref('')
const defaultDir = ref('')
// pathEdited 记录用户有没有自己改过路径。
// 改过之后就不再跟着单位名称变 —— 手填的路径被覆盖是最恼人的。
const pathEdited = ref(false)
const savingHint = ref('')

async function loadDefaultPath() {
  const r = await api.suggestBookPath(form.value.companyName)
  if (r.ok) {
    if (!pathEdited.value || !newPath.value.trim()) newPath.value = r.data
    const d = await api.defaultBookDir()
    if (d.ok) defaultDir.value = d.data
  } else {
    savingHint.value = r.fault.message
  }
}
onMounted(() => { if (mode.value !== 'choose') loadDefaultPath() })

// 单位名称变了，文件名跟着变（用户自己改过路径就不再动它）
watch(() => form.value.companyName, () => {
  if (!pathEdited.value) loadDefaultPath()
})

function onPathInput() {
  pathEdited.value = true
}

// 路径为空时用默认建议，而不是弹一个「请填写」
async function ensurePath() {
  if (newPath.value.trim()) return newPath.value.trim()
  const r = await api.suggestBookPath(form.value.companyName)
  if (!r.ok) return ''
  newPath.value = r.data
  return r.data
}

// ---------------------------------------------------------------------------
// 系统文件管理器
// ---------------------------------------------------------------------------
//
// 账套默认放在 <家目录>/.mini-account/dataDB —— 以点开头，是**隐藏目录**，
// 用户自己翻是翻不到的。所以这里必须给出路：
//
//   浏览…      调起系统文件选择框，直接挑 dataDB 里的 .db
//   选择目录…   调起目录选择框，换一个保存位置（文件名不变）
//   打开目录    在 Finder / 资源管理器里打开账套目录，自己看
//
// ★ 用户取消时返回空串，那**不是错误** —— 不能弹红条。
// 「点了取消然后被骂一句」是这类界面里最常见的无礼行为。
const picking = ref(false)

async function pickBookFile() {
  if (picking.value) return
  picking.value = true
  const r = await api.chooseBookFile()
  picking.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  if (r.data) openPath.value = r.data
}

async function pickBookDir() {
  if (picking.value) return
  picking.value = true
  const r = await api.chooseBookDir(newPath.value.trim())
  picking.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  if (r.data) {
    newPath.value = r.data
    // 这一步等于用户自己改了路径，之后不再跟着单位名称变 ——
    // 手填/手选的位置被覆盖是最恼人的。
    pathEdited.value = true
  }
}

async function revealDir() {
  const r = await api.revealBookDir(newPath.value.trim() || openPath.value.trim())
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已在文件管理器里打开 ${r.data}`, 'info')
}

async function doOpen() {
  if (!openPath.value.trim()) { notify('请填写账套文件路径', 'warn'); return }
  busy.value = true
  const r = await api.openBook(openPath.value.trim())
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  if (!r.data.open) {
    // 文件存在但还没建账 —— 直接切到建账表单，路径沿用
    newPath.value = r.data.path ?? openPath.value.trim()
    mode.value = 'create'
    notify('这个文件还不是账套，请先建账', 'warn')
    return
  }
  await onReady()
  router.replace('/dashboard')
}

// 生成演示账套。
//
// 原先只有命令行有 demo 命令，而「先造一个演示账套看看软件长什么样」
// 恰恰是第一次打开界面的人最需要的功能 —— 他还没想清楚单位名称、
// 纳税人身份、启用期间该怎么填，就想先看看这套账长什么样。
//
// 已经建过账的会被拒绝而不是覆盖：覆盖等于删掉用户的账，
// 这种事不该由一个「看看演示」的按钮触发。
const demoMonths = ref(3)
async function doDemo() {
  const target = await ensurePath()
  if (!target) { notify('算不出默认保存位置，请手工填写账套文件路径', 'warn'); return }
  busy.value = true
  const o = await api.openBook(target)
  if (!o.ok) { busy.value = false; notify(o.fault.message, 'error', o.fault.detail); return }
  const r = await api.createDemoBook(demoMonths.value)
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(
    `演示账套已生成：${r.data.posted} 张凭证、已结账 ${r.data.closedMonths} 个月`,
    'success')
  for (const w of r.data.warnings ?? []) notify(w, 'warn')
  await onReady()
  router.replace('/dashboard')
}

async function doCreate() {
  if (!form.value.companyName.trim()) { notify('请填写单位名称', 'warn'); return }
  const target = await ensurePath()
  if (!target) { notify('算不出默认保存位置，请手工填写账套文件路径', 'warn'); return }
  busy.value = true
  const o = await api.openBook(target)
  if (!o.ok) { busy.value = false; notify(o.fault.message, 'error', o.fault.detail); return }
  const r = await api.createBook({ ...form.value, throughYear: form.value.startYear })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`账套「${r.data.book.companyName}」已建好，191 个科目与 12 个会计期间已就位`, 'success')
  await onReady()
  router.replace('/dashboard')
}
</script>

<template>
  <div class="mx-auto flex max-w-3xl flex-col gap-5 pt-6">
    <div class="text-center">
      <div class="mx-auto grid size-12 place-items-center rounded-xl bg-primary text-lg font-bold text-primary-foreground">
        账
      </div>
      <h1 class="mt-3 text-xl font-semibold">小账本</h1>
      <p class="mt-1 text-sm text-muted-foreground">
        面向中国小微企业的记账软件 · 账套与附件都在你自己的电脑上
      </p>
      <p v-if="!hasWails()" class="mt-2 text-xs text-[var(--warn)]">
        当前是浏览器开发模式，数据是假的。真实功能请在 wails dev 下使用。
      </p>
    </div>

    <div class="flex justify-center gap-2">
      <Button :variant="mode === 'choose' ? 'default' : 'outline'"
              @click="mode = 'choose'; scanBooks()">
        <FolderOpen /> 已有账套{{ books?.books?.length ? `（${books.books.length}）` : '' }}
      </Button>
      <Button :variant="mode === 'open' ? 'default' : 'outline'" @click="mode = 'open'">
        <FolderOpen /> 打开别处的账套
      </Button>
      <Button :variant="mode === 'create' ? 'default' : 'outline'"
              @click="mode = 'create'; loadDefaultPath()">
        <FilePlus2 /> 新建账套
      </Button>
    </div>

    <!-- ① 启动选择：目录里已经有账套 -->
    <Card v-if="mode === 'choose'">
      <CardHeader>
        <CardTitle>打开哪一本账</CardTitle>
        <CardDescription>
          账套目录：<code class="font-mono">{{ books?.dir }}</code>
          <br />
          确认一下要打开的还是新建 —— 打开一本已有的账，之后随时可以在
          「设置与备份」里换。
        </CardDescription>
      </CardHeader>
      <CardContent class="flex flex-col gap-3">
        <Spinner v-if="scanning" />

        <template v-else-if="books?.books?.length">
          <label
            v-for="b in books.books" :key="b.path"
            class="flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors"
            :class="picked === b.path ? 'border-primary bg-accent/40' : 'hover:bg-accent/20'"
          >
            <input v-model="picked" type="radio" :value="b.path"
                   class="mt-1 size-4 shrink-0 accent-[var(--primary)]" />
            <div class="min-w-0 flex-1">
              <p class="flex flex-wrap items-center gap-2 text-sm font-medium">
                {{ b.companyName || b.fileName }}
                <Badge v-if="b.path === books.lastPath" variant="debit">上次打开</Badge>
                <Badge v-if="b.path === books.suggested && b.path !== books.lastPath"
                       variant="muted">最近改动</Badge>
              </p>
              <p class="mt-0.5 text-xs text-muted-foreground">
                {{ b.voucherCount }} 张凭证 · 启用 {{ b.startPeriod || '—' }} ·
                {{ fmtSize(b.sizeBytes) }} · 最后改动 {{ fmtTime(b.modTime) }}
              </p>
              <p class="mt-0.5 truncate font-mono text-xs text-muted-foreground/70">
                {{ b.fileName }}
              </p>
            </div>
          </label>

          <div class="flex flex-wrap items-center gap-2">
            <Button :disabled="busy || !picked" size="lg" @click="openPicked">
              <FolderOpen /> {{ busy ? '正在打开…' : '打开这本账' }}
            </Button>
            <Button :disabled="busy" variant="outline" size="lg" @click="mode = 'create'">
              <FilePlus2 /> 新建一本
            </Button>
          </div>
        </template>

        <template v-else>
          <p class="text-sm text-muted-foreground">
            账套目录里还没有账套，先新建一本。
          </p>
          <Button size="lg" @click="mode = 'create'"><FilePlus2 /> 新建账套</Button>
        </template>

        <!-- 目录里不是账套的 .db：单独列出来，别让用户以为账套坏了 -->
        <div v-if="books?.others?.length" class="rounded-lg border border-dashed p-3 text-xs">
          <p class="font-medium text-muted-foreground">
            目录里还有 {{ books.others.length }} 个 .db 文件不是账套：
          </p>
          <ul class="mt-1 flex flex-col gap-0.5 text-muted-foreground">
            <li v-for="o in books.others" :key="o.path">
              <code class="font-mono">{{ o.fileName }}</code> —— {{ o.err }}
            </li>
          </ul>
        </div>
      </CardContent>
    </Card>

    <Card v-else-if="mode === 'open'">
      <CardHeader>
        <CardTitle>打开账套</CardTitle>
        <CardDescription>选择一个 .db 文件。文件不存在时会新建一个空库，接着引导你建账。</CardDescription>
      </CardHeader>
      <CardContent class="flex flex-col gap-3">
        <div class="flex items-end gap-3">
          <div class="flex-1">
            <Label>账套文件路径</Label>
            <Input v-model="openPath" class="mt-1.5" placeholder="/Users/you/.mini-account/dataDB/book.db" />
          </div>
          <!-- ★ 直接调起系统的文件管理器，而不是让用户自己把路径敲进来。
               账套目录是隐藏的（.mini-account），手敲路径基本不可能敲对。 -->
          <Button variant="outline" :disabled="picking" title="在系统文件管理器里选择账套文件"
                  @click="pickBookFile">
            <FolderSearch /> 浏览…
          </Button>
          <Button :disabled="busy" @click="doOpen"><FolderOpen /> 打开</Button>
        </div>
        <p class="text-xs text-muted-foreground">
          点「浏览…」会打开系统的文件管理器，从账套目录里挑一个 .db。
          <button class="underline underline-offset-2 hover:text-foreground" @click="revealDir">
            打开账套目录
          </button>
        </p>
      </CardContent>
    </Card>

    <Card v-else>
      <CardHeader>
        <CardTitle>新建账套</CardTitle>
        <CardDescription>
          建账会一次性预置《小企业会计准则》190 个科目与 12×N 个会计期间。
          任何一步失败都会整体回滚 —— 不会留下半个账套。
        </CardDescription>
      </CardHeader>
      <CardContent class="flex flex-col gap-4">
        <div class="grid grid-cols-2 gap-3">
          <div class="col-span-2">
            <Label>单位名称 *</Label>
            <Input v-model="form.companyName" class="mt-1.5" placeholder="杭州云帆软件有限公司" />
          </div>
          <div>
            <Label>统一社会信用代码</Label>
            <Input v-model="form.creditCode" class="mt-1.5" />
          </div>
          <div>
            <Label>法定代表人</Label>
            <Input v-model="form.legalPerson" class="mt-1.5" />
          </div>
        </div>

        <!-- ★ 两个**互不派生**的分类，必须分别填写。
             「小微企业」是所得税与统计口径（看营业收入与从业人员），
             「小规模纳税人」是流转税口径（看年应征增值税销售额是否超 500 万元）。
             小微企业可以自愿登记为一般纳税人；销售额未超 500 万元通常按小规模
             纳税，但登记为一般纳税人同样合法。用一个字段表达两件事，
             任何「是不是小微 → 能不能抵扣进项」的推断都会算错税。 -->
        <div>
          <Label>增值税纳税人身份</Label>
          <div class="mt-1.5 flex gap-2">
            <Button
              v-for="t in [
                { v: 'general', n: '一般纳税人',
                  d: '可以抵扣进项税额，按一般计税（特定业务可选简易计税）' },
                { v: 'small_scale', n: '小规模纳税人',
                  d: '不得抵扣进项税额（取得专用发票也不能），按征收率简易计税' }]"
              :key="t.v"
              :variant="form.vatStatus === t.v ? 'default' : 'outline'"
              :title="t.d"
              @click="form.vatStatus = t.v"
            >{{ t.n }}</Button>
          </div>
          <p class="mt-1 text-xs text-muted-foreground">
            依据《增值税法》，看年应征增值税销售额是否超过 500 万元。
            未超过的通常按小规模纳税人纳税，但<b>可以自愿登记为一般纳税人</b>，
            所以请按实际税务登记填写，程序不按销售额自动推导。
          </p>
        </div>

        <div>
          <Label>企业规模类型（可留空）</Label>
          <div class="mt-1.5 flex gap-2">
            <Button
              v-for="t in [
                { v: 'micro', n: '微型' }, { v: 'small', n: '小型' },
                { v: 'medium', n: '中型' }, { v: 'large', n: '大型' }]"
              :key="t.v"
              :variant="form.enterpriseScale === t.v ? 'default' : 'outline'"
              @click="form.enterpriseScale = form.enterpriseScale === t.v ? '' : t.v"
            >{{ t.n }}</Button>
          </div>
          <p class="mt-1 text-xs text-muted-foreground">
            依据《中小企业划型标准规定》，看营业收入与从业人员，
            决定能否享受<b>小型微利企业所得税</b>优惠。
            <br />
            ⚠️ 它<b>与增值税怎么算无关</b> —— 小微企业完全可能是一般纳税人。
            现在拿不准可以留空，之后再补填。
          </p>
        </div>

        <div class="grid grid-cols-4 gap-3">
          <div><Label>启用年度</Label><Input v-model.number="form.startYear" type="number" class="mt-1.5" /></div>
          <div><Label>启用月份</Label><Input v-model.number="form.startMonth" type="number" class="mt-1.5" /></div>
          <div><Label>当前年度</Label><Input v-model.number="form.currentYear" type="number" class="mt-1.5" /></div>
          <div><Label>当前月份</Label><Input v-model.number="form.currentMonth" type="number" class="mt-1.5" /></div>
        </div>

        <div>
          <Label>账套文件保存到</Label>
          <div class="mt-1.5 flex items-center gap-2">
            <Input v-model="newPath" class="font-mono text-xs"
                   @input="onPathInput" />
            <Button variant="outline" :disabled="picking" title="在系统文件管理器里选择目录"
                    @click="pickBookDir">
              <FolderSearch /> 选择目录…
            </Button>
          </div>
          <p v-if="defaultDir" class="mt-1 text-xs text-muted-foreground">
            默认放在 <code class="font-mono">{{ defaultDir }}</code>，
            改单位名称时文件名会自动跟着变（中文会转成拼音）。
            文件名一律用英文：账套要在不同系统、备份包、U 盘之间搬，
            中文名容易在这些环节里出问题。
            <button class="underline underline-offset-2 hover:text-foreground" @click="revealDir">
              打开目录
            </button>
          </p>
          <p class="mt-1 text-xs text-muted-foreground">
            附件存在同级的 <code class="font-mono">.files</code> 目录里，
            备份时整个 <code class="font-mono">dataDB</code> 目录拷走即可。
          </p>
          <p v-if="savingHint" class="mt-1 text-xs text-[var(--warn)]">{{ savingHint }}</p>
        </div>

        <div class="flex items-start gap-2 rounded-md border bg-muted/30 p-3 text-xs">
          <ShieldCheck class="mt-0.5 size-3.5 shrink-0 text-[var(--profit)]" />
          <span>
            账套是一个标准 SQLite 文件，你随时可以用任何 SQLite 工具打开它 ——
            数据是你的，不被锁在这款软件里。
          </span>
        </div>

        <div class="flex flex-wrap items-center gap-3">
          <Button :disabled="busy" size="lg" @click="doCreate">
            <FilePlus2 /> {{ busy ? '正在建账…' : '建立账套' }}
          </Button>
          <span class="text-xs text-muted-foreground">或者</span>
          <Button :disabled="busy" variant="outline" size="lg" @click="doDemo">
            <Sparkles /> 先生成演示账套看看
          </Button>
          <div class="w-28">
            <Label class="text-xs">生成到第几个月</Label>
            <Input v-model.number="demoMonths" type="number" min="1" max="12"
                   class="mt-1 h-8" />
          </div>
        </div>
        <p class="text-xs text-muted-foreground">
          演示账套会写入一套完整的业务数据（销售、采购、工资、银行流水、
          期末结转）并自动结账，让你直接看到报表、账簿与对账工具的实际样子。
          单位名称与身份都用演示值，不影响你之后建正式账套。
        </p>
      </CardContent>
    </Card>
  </div>
</template>
