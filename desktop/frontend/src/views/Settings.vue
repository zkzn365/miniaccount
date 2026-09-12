<script setup>
import { computed, onMounted, ref } from 'vue'
import {
  Save, HardDriveDownload, ShieldCheck, Cpu, Cloud, Info, FolderOpen,
  Upload, Paperclip, AlertTriangle,
} from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
import { bookkeeper, loadBookkeeper, rememberBookkeeper } from '@/lib/operator'
import { fmtBytes } from '@/lib/format'
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
import Separator from '@/components/ui/Separator.vue'
import Spinner from '@/components/ui/Spinner.vue'

const book = ref(null)
const filesDir = ref('')
const version = ref('')
const aiCfg = ref(null)
const backupResult = ref(null)
const attachAudit = ref(null)
const orphans = ref([])
const restoreOpen = ref(false)
const restoreArchive = ref('')
const restoreTarget = ref('')
const restorePreview = ref(null)
// 恢复成功后原账套的去处。用常驻面板而不是 toast：
// toast 3.2 秒就消失，而这是一条用户要抄下来的路径。
const restoreStash = ref('')
const restoreCompany = ref('')
const backupPath = ref('')
const includeFiles = ref(true)
const emit = defineEmits(['book-changed'])
const busy = ref(false)

const providerForm = ref({
  name: '本地 Ollama', kind: 'local',
  baseUrl: 'http://127.0.0.1:11434/v1', model: 'qwen2.5:7b',
  apiKey: '', isDefault: true,
})

async function load() {
  const [b, f, v, a, au, or] = await Promise.all([
    api.periods(), api.filesDir(), api.appVersion(), api.aiConfig(),
    api.auditAttachments(0, 0), api.orphanAttachments(),
  ])
  if (b.ok) book.value = b.data
  if (f.ok) filesDir.value = f.data
  if (v.ok) version.value = v.data
  if (a.ok) aiCfg.value = a.data
  if (au.ok) attachAudit.value = au.data
  if (or.ok) orphans.value = or.data ?? []
}
// 记账人（本机设置，全程序共用一份）
const bookkeeperDraft = ref('')
const savingBookkeeper = ref(false)

async function saveBookkeeper() {
  savingBookkeeper.value = true
  const r = await rememberBookkeeper(bookkeeperDraft.value)
  savingBookkeeper.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  bookkeeperDraft.value = bookkeeper.value
  notify(bookkeeper.value
    ? `已记住：${bookkeeper.value} —— 录凭证时会自动填上`
    : '已清空：录凭证时需要当场填写制单人', 'success')
}

onMounted(async () => {
  await load()
  await loadBookkeeper()
  bookkeeperDraft.value = bookkeeper.value
})

const stats = computed(() => aiCfg.value?.stats ?? null)

async function saveProvider() {
  busy.value = true
  const r = await api.saveAIProvider(providerForm.value)
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify('模型服务已保存', 'success')
  await load()
}

async function doBackup() {
  if (!backupPath.value.trim()) {
    notify('请先填写备份文件的保存路径，例如 /Users/you/备份/2025-03.mabak', 'warn')
    return
  }
  busy.value = true
  backupResult.value = null
  const r = await api.backup({ dest: backupPath.value.trim(), includeFiles: includeFiles.value })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  backupResult.value = r.data
  notify('备份完成', 'success')
}

// ---------------------------------------------------------------- 恢复

// 恢复前必须先看清单：确认这是哪个账套、什么时候备的份。
// 恢复错备份的代价是把现有的账覆盖掉，而这个动作不可逆
// （虽然实现上会把旧账套改名保存成 .bak，但用户未必知道去哪找）。
async function openRestore() {
  restoreOpen.value = true
  restorePreview.value = null
  restoreArchive.value = ''
  restoreTarget.value = book.value ? '' : ''
}

async function inspectArchive() {
  if (!restoreArchive.value.trim()) { notify('请填写备份文件路径', 'warn'); return }
  const r = await api.inspectBackup(restoreArchive.value.trim())
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); restorePreview.value = null; return }
  restorePreview.value = r.data
}

async function doRestore() {
  if (!restorePreview.value) { notify('请先确认备份文件', 'warn'); return }
  // ★ 有没有「当前账套」要说准。
  //
  // 原话是「当前账套不会删除 —— 它会被挪到带时间戳的目录里」，
  // 但在没打开账套（或恢复到新位置）时根本没有当前账套，
  // 这句话会让用户以为旧数据被安全留着了 —— 而那个目录是空的。
  const hasCurrent = !!book.value
  if (!confirm(
    `即将用「${restorePreview.value.companyName}」恢复账套。\n\n` +
    (hasCurrent
      ? `当前账套不会删除 —— 它会被整体挪到一个带时间戳的目录里\n` +
        `（.restore-stash-），万一恢复的是错的备份可以拿回来。\n` +
        `恢复成功后这个目录里只保留最近一份。\n\n`
      : `当前没有打开账套，会直接恢复到账套位置，没有旧账套需要保留。\n\n`) +
    `确认继续？`)) return
  busy.value = true
  const r = await api.restoreBackup({
    archive: restoreArchive.value.trim(),
    dbPath: restoreTarget.value.trim(),
  })
  busy.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  // ★ 必须把「原来的账套在哪」告诉用户。
  // 之前这句提示只说「已恢复」，而原账套被挪进了一个隐藏目录 ——
  // 用户想退回上一版时根本不知道去哪儿找。
  restoreCompany.value = r.data.companyName
  restoreStash.value = r.data.stashed || ''
  notify(`已恢复「${r.data.companyName}」（${r.data.restored} 个附件）`, 'success')
  restoreOpen.value = false
  emit('book-changed')
  await load()
}

function suggestPath() {
  const stamp = new Date().toISOString().slice(0, 10)
  backupPath.value = `${stamp}-小账本备份.mabak`
}

const passRate = computed(() => {
  const s = stats.value
  if (!s || !s.proposed) return null
  return Math.round((s.valid / s.proposed) * 100)
})
const acceptRate = computed(() => {
  const s = stats.value
  if (!s || !s.total) return null
  return Math.round(((s.accepted + s.modified) / s.total) * 100)
})
</script>

<template>
  <div class="flex max-w-4xl flex-col gap-5">
    <!-- 记账人（本机） -->
    <Card>
      <CardHeader>
        <CardTitle>记账人</CardTitle>
        <CardDescription>
          录凭证、过账、结账、生成工资表时默认填这个人 —— 不用每张凭证重打一遍名字。
          <br />
          它是<b>本账套</b>的设置：同一台电脑给两家公司做账可以签不同的名，
          也会随账套备份一起走。
          <br />
          它只是<b>默认填谁</b>，不是替谁签字：名字在凭证上看得见、也能当场改，
          每一次落章都会记进操作日志。留空则不预设，程序不会拿「系统」之类的名字顶上。
        </CardDescription>
      </CardHeader>
      <CardContent class="flex items-end gap-3">
        <div class="w-56">
          <Label>姓名</Label>
          <Input v-model="bookkeeperDraft" class="mt-1.5"
                 placeholder="如：李会计" @keyup.enter="saveBookkeeper" />
        </div>
        <Button :disabled="savingBookkeeper" @click="saveBookkeeper">
          {{ savingBookkeeper ? '保存中…' : '记住这个人' }}
        </Button>
        <p class="mb-1 text-xs text-muted-foreground">
          存在账套里（<code class="font-mono">setting</code> 表），
          命令行 <code class="font-mono">--by</code> 不传时也用它。
        </p>
      </CardContent>
    </Card>

    <!-- 账套 -->
    <Card>
      <CardHeader>
        <CardTitle>账套信息</CardTitle>
        <CardDescription>账套是一个独立的 SQLite 文件，附件放在同级的 .files 目录</CardDescription>
      </CardHeader>
      <CardContent v-if="book" class="flex flex-col gap-2 text-sm">
        <div class="flex justify-between border-b py-1.5">
          <span class="text-muted-foreground">单位名称</span><span>{{ book.companyName }}</span>
        </div>
        <div class="flex justify-between border-b py-1.5">
          <span class="text-muted-foreground">信用代码</span><span>{{ book.creditCode || '—' }}</span>
        </div>
        <div class="flex justify-between border-b py-1.5">
          <span class="text-muted-foreground">法定代表人</span><span>{{ book.legalPerson || '—' }}</span>
        </div>
        <div class="flex justify-between border-b py-1.5">
          <span class="text-muted-foreground">增值税纳税人身份</span>
          <span>
            <Badge :variant="book.canDeductInputVat ? 'debit' : 'warn'">
              {{ book.vatStatusLabel }}
            </Badge>
            <span class="ml-2 text-xs text-muted-foreground">
              {{ book.canDeductInputVat ? '可以抵扣进项税额' : '不得抵扣进项税额' }}
              · 生效于 {{ book.vatStatusEffectiveFrom }}
            </span>
          </span>
          <!-- ★ 与上一行是**两个不同**的分类：上面是流转税口径（能不能抵扣），
               下面是所得税与统计口径（能不能享受小型微利优惠）。 -->
          <span class="text-muted-foreground">企业规模类型</span>
          <span>
            <template v-if="book.enterpriseScale">
              <Badge variant="muted">{{ book.enterpriseScaleLabel }}</Badge>
            </template>
            <template v-else>
              <Badge variant="warn">未填写</Badge>
              <span class="ml-2 text-xs text-muted-foreground">
                决定能否享受小型微利企业所得税优惠，与增值税抵扣无关；
                不影响日常记账，可随时补填
              </span>
            </template>
          </span>
        </div>
        <div class="flex justify-between border-b py-1.5">
          <span class="text-muted-foreground">启用期间</span><span>{{ book.startPeriod }}</span>
        </div>
        <div class="flex justify-between py-1.5">
          <span class="flex items-center gap-1.5 text-muted-foreground">
            <FolderOpen class="size-3.5" /> 附件目录
          </span>
          <span class="font-mono text-xs">{{ filesDir }}</span>
        </div>
      </CardContent>
    </Card>

    <!-- 备份 -->
    <Card>
      <CardHeader>
        <CardTitle class="flex items-center gap-2">
          <HardDriveDownload class="size-4" /> 备份
        </CardTitle>
        <CardDescription>
          备份是一个文件：数据库快照与全部附件一起打包成 .mabak。
          附件按 sha256 内容寻址，同一张发票被多个凭证引用也只存一份。
        </CardDescription>
      </CardHeader>
      <CardContent class="flex flex-col gap-4">
        <div class="flex items-end gap-3">
          <div class="flex-1">
            <Label>保存到</Label>
            <Input v-model="backupPath" class="mt-1.5" placeholder="/Users/you/备份/2025-03.mabak" />
          </div>
          <Button variant="outline" @click="suggestPath">自动命名</Button>
          <Button :disabled="busy" @click="doBackup"><Save /> 开始备份</Button>
          <Button variant="outline" @click="openRestore"><Upload /> 从备份恢复</Button>
        </div>

        <label class="flex items-center gap-2 text-sm">
          <input v-model="includeFiles" type="checkbox" class="size-4 rounded border-input" />
          包含附件
          <span v-if="!includeFiles" class="text-xs text-[var(--warn)]">
            ⚠ 不包含附件时，恢复后发票 PDF 等原件会丢失
          </span>
        </label>

        <div v-if="backupResult" class="rounded-lg border bg-muted/30 p-4 text-sm">
          <p class="mb-2 flex items-center gap-2 font-medium">
            <ShieldCheck class="size-4 text-[var(--profit)]" /> 备份完成
          </p>
          <div class="grid grid-cols-2 gap-x-6 gap-y-1 text-xs">
            <span class="text-muted-foreground">单位名称</span><span>{{ backupResult.companyName }}</span>
            <span class="text-muted-foreground">数据库</span><span>{{ fmtBytes(backupResult.dbSize) }}</span>
            <span class="text-muted-foreground">附件</span>
            <span>{{ backupResult.fileCount }} 个 · {{ fmtBytes(backupResult.fileBytes) }}</span>
            <span class="text-muted-foreground">凭证数</span><span>{{ backupResult.voucherCount }}</span>
            <span class="text-muted-foreground">科目数</span><span>{{ backupResult.accountCount }}</span>
            <span class="text-muted-foreground">账务期间</span>
            <span>{{ backupResult.periodFrom }} ~ {{ backupResult.periodTo }}</span>
            <span class="text-muted-foreground">校验和</span>
            <span class="truncate font-mono">{{ backupResult.dbSha256?.slice(0, 24) }}…</span>
          </div>
        </div>
      </CardContent>
    </Card>

    <!-- 附件 -->
    <Card>
      <CardHeader>
        <CardTitle class="flex items-center gap-2">
          <Paperclip class="size-4" /> 附件
        </CardTitle>
        <CardDescription>
          附件按 sha256 内容寻址存在账套同级的 .files 目录里，
          同一份文件挂到多张凭证上只占一份空间。
        </CardDescription>
      </CardHeader>
      <CardContent class="flex flex-col gap-3">
        <div v-if="attachAudit" class="grid grid-cols-4 gap-3">
          <div class="rounded-lg border p-3">
            <p class="text-xs text-muted-foreground">已记账凭证</p>
            <p class="num mt-1 text-lg font-semibold">{{ attachAudit.posted }}</p>
          </div>
          <div class="rounded-lg border p-3">
            <p class="text-xs text-muted-foreground">附有单据</p>
            <p class="num mt-1 text-lg font-semibold text-[var(--profit)]">{{ attachAudit.withAttach }}</p>
          </div>
          <div class="rounded-lg border p-3">
            <p class="text-xs text-muted-foreground">缺附件</p>
            <p class="num mt-1 text-lg font-semibold"
               :class="attachAudit.without ? 'text-[var(--warn)]' : ''">
              {{ attachAudit.without }}
            </p>
          </div>
          <div class="rounded-lg border p-3">
            <p class="text-xs text-muted-foreground">孤儿文件</p>
            <p class="num mt-1 text-lg font-semibold">{{ attachAudit.orphanFiles }}</p>
          </div>
        </div>
        <p class="text-xs text-muted-foreground">
          《会计基础工作规范》要求记账凭证附有原始单据。
          小微企业的现实是「发票在微信里、报销单在抽屉里」——
          这个数字让会计知道还差多少张没补。
        </p>
        <div v-if="orphans.length" class="rounded-md border border-[var(--warn)]/40 bg-[var(--warn)]/10 p-3 text-xs">
          <p class="flex items-center gap-1.5 font-medium">
            <AlertTriangle class="size-3.5" />
            磁盘上有 {{ orphans.length }} 个文件没有被任何单据引用
          </p>
          <p class="mt-1 text-muted-foreground">
            通常是删掉发票或凭证后留下的。软件不会自动删——
            自动删用户数据的代价太大，请自行确认后再清理 .files 目录。
          </p>
        </div>
      </CardContent>
    </Card>

    <!-- AI -->
    <Card>
      <CardHeader>
        <CardTitle class="flex items-center gap-2">
          <Cpu class="size-4" /> AI 记账助手
        </CardTitle>
        <CardDescription>
          默认推荐本地模型 —— 银行流水、工资、客户名单不该离开你的电脑。
        </CardDescription>
      </CardHeader>
      <CardContent class="flex flex-col gap-4">
        <div v-if="aiCfg?.providers?.length">
          <p class="mb-2 text-sm font-medium">已配置的服务</p>
          <div class="rounded-lg border">
            <Table>
              <thead>
                <tr class="border-b bg-muted/40">
                  <Th>名称</Th><Th>形态</Th><Th>模型</Th><Th>地址</Th><Th>状态</Th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="p in aiCfg.providers" :key="p.id" class="border-b last:border-0">
                  <Td>{{ p.name }}</Td>
                  <Td>
                    <Badge :variant="p.kind === 'local' ? 'profit' : 'warn'">
                      <component :is="p.kind === 'local' ? Cpu : Cloud" class="mr-1 size-3" />
                      {{ p.kind === 'local' ? '本地' : '云端' }}
                    </Badge>
                  </Td>
                  <Td class="font-mono text-xs">{{ p.model }}</Td>
                  <Td class="font-mono text-xs text-muted-foreground">{{ p.baseUrl }}</Td>
                  <Td>
                    <Badge v-if="p.isDefault" variant="default">默认</Badge>
                    <Badge v-else-if="!p.enabled" variant="muted">已停用</Badge>
                  </Td>
                </tr>
              </tbody>
            </Table>
          </div>
        </div>

        <Separator />

        <div class="grid grid-cols-2 gap-3">
          <div>
            <Label>服务名</Label>
            <Input v-model="providerForm.name" class="mt-1.5" />
          </div>
          <div>
            <Label>部署形态</Label>
            <div class="mt-1.5 flex gap-1.5">
              <Button
                v-for="k in [{ v: 'local', n: '本地（数据不出机器）' }, { v: 'cloud', n: '云端 API' }]"
                :key="k.v"
                :variant="providerForm.kind === k.v ? 'default' : 'outline'"
                size="sm"
                @click="providerForm.kind = k.v"
              >{{ k.n }}</Button>
            </div>
          </div>
          <div>
            <Label>服务地址</Label>
            <Input v-model="providerForm.baseUrl" class="mt-1.5" placeholder="http://127.0.0.1:11434/v1" />
          </div>
          <div>
            <Label>模型名</Label>
            <Input v-model="providerForm.model" class="mt-1.5" placeholder="qwen2.5:7b" />
          </div>
          <div v-if="providerForm.kind === 'cloud'" class="col-span-2">
            <Label>API Key</Label>
            <Input v-model="providerForm.apiKey" class="mt-1.5" type="password" placeholder="sk-…" />
          </div>
        </div>

        <div
          v-if="providerForm.kind === 'cloud'"
          class="flex items-start gap-2 rounded-md border border-[var(--warn)]/50 bg-[var(--warn)]/10 p-3 text-xs"
        >
          <Info class="mt-0.5 size-3.5 shrink-0" />
          <span>
            开启云端后，银行流水摘要、对方户名、金额会发送到该服务商。
            账号与统一社会信用代码会自动打码；金额与户名默认保留，
            因为它们是判断科目的核心依据。如需数据完全不出本机，请配置本地 Ollama。
          </span>
        </div>

        <div class="flex items-center gap-3">
          <Button :disabled="busy" @click="saveProvider"><Save /> 保存模型服务</Button>
          <span class="text-xs text-muted-foreground">
            本地模型示例：先装 Ollama，执行 <code class="rounded bg-muted px-1">ollama pull qwen2.5:7b</code>
          </span>
        </div>

        <!-- AI 表现 -->
        <template v-if="stats && stats.total > 0">
          <Separator />
          <div>
            <p class="mb-2 text-sm font-medium">AI 表现</p>
            <div class="grid grid-cols-4 gap-3">
              <div class="rounded-lg border p-3">
                <p class="text-xs text-muted-foreground">调用总数</p>
                <p class="num mt-1 text-lg font-semibold">{{ stats.total }}</p>
              </div>
              <div class="rounded-lg border p-3">
                <p class="text-xs text-muted-foreground">护栏通过率</p>
                <p class="num mt-1 text-lg font-semibold">
                  {{ passRate === null ? '—' : passRate + '%' }}
                </p>
                <p class="text-[11px] text-muted-foreground">{{ stats.valid }} / {{ stats.proposed }}</p>
              </div>
              <div class="rounded-lg border p-3">
                <p class="text-xs text-muted-foreground">采纳率</p>
                <p class="num mt-1 text-lg font-semibold">
                  {{ acceptRate === null ? '—' : acceptRate + '%' }}
                </p>
                <p class="text-[11px] text-muted-foreground">
                  原样 {{ stats.accepted }} · 改后 {{ stats.modified }} · 拒绝 {{ stats.rejected }}
                </p>
              </div>
              <div class="rounded-lg border p-3">
                <p class="text-xs text-muted-foreground">服务不可用</p>
                <p class="num mt-1 text-lg font-semibold">{{ stats.total - stats.proposed }}</p>
                <p class="text-[11px] text-muted-foreground">不计入护栏通过率</p>
              </div>
            </div>
            <p class="mt-2 text-xs text-muted-foreground">
              护栏通过率是判断「这个模型能不能用」最直接的指标 ——
              通过率 40% 的模型，用户点三次才中一次，还不如手录。
            </p>
          </div>
        </template>
      </CardContent>
    </Card>

    <!-- 恢复成功后的常驻提示：原账套去哪了 -->
    <Card v-if="restoreStash" class="border-[var(--debit)]/40">
      <CardContent class="flex items-start gap-3 pt-5 text-sm">
        <Info class="mt-0.5 size-4 shrink-0 text-[var(--debit)]" />
        <div class="flex-1">
          <p class="font-medium">已恢复「{{ restoreCompany }}」</p>
          <p class="mt-1 text-muted-foreground">
            恢复前的账套保存在下面这个目录里。如需退回上一版，
            把该目录里的 <code class="font-mono">book.db</code> 与
            <code class="font-mono">.files</code> 拷回账套位置即可。
          </p>
          <p class="mt-1 break-all font-mono text-xs">{{ restoreStash }}</p>
        </div>
        <Button variant="ghost" size="sm" @click="restoreStash = ''">知道了</Button>
      </CardContent>
    </Card>

    <!-- 从备份恢复 -->
    <Modal
      v-model:open="restoreOpen"
      title="从备份恢复"
      description="恢复会覆盖当前账套。当前账套不会被删除 —— 它会被整体挪到一个带时间戳的 .restore-stash- 目录里（同一目录下只保留最近一份），万一恢复的是错的备份可以拿回来。"
      width="max-w-xl"
    >
      <div class="flex flex-col gap-4">
        <div class="flex items-end gap-2">
          <div class="flex-1">
            <Label>备份文件（.mabak）</Label>
            <Input v-model="restoreArchive" class="mt-1.5" placeholder="/Users/you/备份/2025-03.mabak" />
          </div>
          <Button variant="outline" @click="inspectArchive">查看内容</Button>
        </div>

        <div v-if="restorePreview" class="rounded-lg border bg-muted/30 p-4 text-xs">
          <p class="mb-2 font-medium">备份内容</p>
          <div class="grid grid-cols-2 gap-x-6 gap-y-1">
            <span class="text-muted-foreground">单位名称</span><span>{{ restorePreview.companyName }}</span>
            <span class="text-muted-foreground">备份时间</span><span>{{ restorePreview.createdAt }}</span>
            <span class="text-muted-foreground">数据库</span><span>{{ fmtBytes(restorePreview.dbSize) }}</span>
            <span class="text-muted-foreground">附件</span>
            <span>{{ restorePreview.fileCount }} 个 · {{ fmtBytes(restorePreview.fileBytes) }}</span>
            <span class="text-muted-foreground">凭证数</span><span>{{ restorePreview.voucherCount }}</span>
            <span class="text-muted-foreground">账务期间</span>
            <span>{{ restorePreview.periodFrom }} ~ {{ restorePreview.periodTo }}</span>
          </div>
        </div>

        <div>
          <Label>恢复到哪个账套文件（留空 = 覆盖当前）</Label>
          <Input v-model="restoreTarget" class="mt-1.5" placeholder="留空则覆盖当前账套" />
        </div>
      </div>
      <template #footer>
        <Button variant="ghost" @click="restoreOpen = false">取消</Button>
        <Button variant="destructive" :disabled="busy || !restorePreview" @click="doRestore">
          <Upload /> 确认恢复
        </Button>
      </template>
    </Modal>

    <p class="pb-4 text-center text-xs text-muted-foreground">
      小账本 {{ version }} · 独立实现，不含任何第三方记账软件的代码
    </p>
  </div>
</template>
