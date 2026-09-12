<script setup>
/**
 * 操作日志。
 *
 * # 这一页是给谁用的
 *
 * 财政部《企业会计信息化工作规范》对用户操作日志提的三条要求里，
 * 第三条是**可查询性**：「必须提供对各类操作的查询，以便会计监督人员
 * 筛选出想要的信息。否则，庞大的记录数据就是信息垃圾，没有实用价值。」
 *
 * 所以这一页的重点不是「显示日志」，而是**查得动**：
 * 按操作人、时间范围、操作种类、结果、关键字，单独或组合筛选 ——
 * 这正是规范里点名的四个维度。
 *
 * 「校验完整性」按钮对应第二条要求（安全性）：日志用哈希链串起来，
 * 改过、删过都看得出来。会计监督人员可以自己点一下验证，
 * 而不必相信软件的说法。
 */
import { computed, onMounted, ref } from 'vue'
import {
  Search, ShieldCheck, ShieldAlert, Download, RefreshCw, Info, AlertTriangle,
} from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
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
import Spinner from '@/components/ui/Spinner.vue'
import EmptyState from '@/components/ui/EmptyState.vue'

const loading = ref(false)
const data = ref(null)
const filter = ref({
  operator: '', from: '', to: '', action: '', result: '', text: '', limit: 100,
  // ★ 默认**不看**「查看」类：规范要审计的是业务操作，
  // 而查看类一天能有好几百条，混在一起会把要看的淹掉。
  category: 'business',
})
const settings = ref(null)
const verify = ref(null)
const expanded = ref({})

async function load() {
  loading.value = true
  const q = {
    operator: filter.value.operator,
    from: filter.value.from,
    to: filter.value.to,
    actions: filter.value.action ? [filter.value.action] : [],
    result: filter.value.result,
    text: filter.value.text,
    limit: filter.value.limit,
    categories: filter.value.category === 'all' ? [] : [filter.value.category],
  }
  const r = await api.auditLog(q)
  loading.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  data.value = r.data
}
onMounted(async () => {
  const r = await api.auditSettings()
  if (r.ok) settings.value = r.data
  await load()
})

// 打开「查看」开关。
//
// ★ 说清楚代价：打开之后日志会明显变大（一次月结能把同一张报表
// 刷新几十遍），所以同一对象的重复查看在窗口期内只记一次。
async function toggleViews() {
  const next = !settings.value?.recordViews
  const r = await api.saveAuditSettings({
    recordViews: next,
    viewIntervalSeconds: settings.value?.viewIntervalSeconds ?? 60,
  })
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  settings.value = { ...settings.value, recordViews: next }
  notify(next
    ? '已开始记录查看操作（同一对象的重复查看在 60 秒内只记一次）'
    : '已停止记录查看操作', 'success')
}

function reset() {
  filter.value = {
    operator: '', from: '', to: '', action: '', result: '', text: '', limit: 100,
    category: 'business',
  }
  load()
}

async function doVerify() {
  const r = await api.verifyAuditLog()
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  verify.value = r.data
  notify(r.data.ok
    ? `日志完整：${r.data.checked} 条记录、${r.data.files} 个文件，没有被改动过`
    : '⚠ 日志完整性校验未通过，请见下方明细', r.data.ok ? 'success' : 'error')
}

async function doExport() {
  const dest = await api.suggestExportPath('audit', new Date().getFullYear(), new Date().getMonth() + 1)
  const path = dest.ok ? dest.data : '操作日志.csv'
  const r = await api.exportAuditLog({
    operator: filter.value.operator, from: filter.value.from, to: filter.value.to,
    actions: filter.value.action ? [filter.value.action] : [],
    result: filter.value.result, text: filter.value.text,
  }, path)
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已导出 ${r.data} 条日志到 ${path}`, 'success')
}

const rows = computed(() => data.value?.page?.entries ?? [])
const actions = computed(() => data.value?.actions ?? [])
</script>

<template>
  <div class="flex flex-col gap-5">
    <Card>
      <CardHeader>
        <CardTitle>操作日志</CardTitle>
        <CardDescription>
          记录每一次会影响账簿的操作：<b>操作人、时间（精确到秒）、做了什么、
          改之前是什么样</b>。日志写在独立文件里，账套恢复不会把它一起倒退 ——
          「谁在什么时候恢复过账套」本身也会记下来。
          <br />
          日志目录：<code class="font-mono">{{ data?.dir }}</code>
          <span v-if="data?.page">
            · {{ data.page.segments }} 个文件 · {{ fmtBytes(data.page.bytes) }}
          </span>
        </CardDescription>
      </CardHeader>
      <CardContent class="flex flex-col gap-4">
        <!-- 筛选：规范点名的四个维度——操作人、时间、操作内容、结果 -->
        <div class="flex flex-wrap items-end gap-3">
          <div class="w-40">
            <Label>操作人</Label>
            <Input v-model="filter.operator" class="mt-1.5" placeholder="如：李会计"
                   @keyup.enter="load" />
          </div>
          <div class="w-40">
            <Label>起始时间</Label>
            <Input v-model="filter.from" class="mt-1.5" placeholder="2025-03-01" />
          </div>
          <div class="w-40">
            <Label>截止时间</Label>
            <Input v-model="filter.to" class="mt-1.5" placeholder="2025-03-31" />
          </div>
          <div class="w-48">
            <Label>操作种类</Label>
            <select v-model="filter.action"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option value="">全部</option>
              <option v-for="a in actions" :key="a.value" :value="a.value">{{ a.label }}</option>
            </select>
          </div>
          <div class="w-36">
            <Label>类别</Label>
            <select v-model="filter.category"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option value="business">业务操作</option>
              <option value="read">查看</option>
              <option value="system">系统</option>
              <option value="all">全部</option>
            </select>
          </div>
          <div class="w-32">
            <Label>结果</Label>
            <select v-model="filter.result"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option value="">全部</option>
              <option value="ok">成功</option>
              <option value="failed">失败</option>
            </select>
          </div>
          <div class="min-w-48 flex-1">
            <Label>关键字</Label>
            <Input v-model="filter.text" class="mt-1.5"
                   placeholder="摘要、凭证号、失败原因……" @keyup.enter="load" />
          </div>
          <Button :disabled="loading" @click="load"><Search /> 查询</Button>
          <Button variant="ghost" :disabled="loading" @click="reset">重置</Button>
        </div>

        <div class="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" @click="doVerify">
            <ShieldCheck /> 校验完整性
          </Button>
          <Button variant="outline" size="sm" @click="doExport"><Download /> 导出 CSV</Button>
          <Button variant="ghost" size="sm" :disabled="loading" @click="load">
            <RefreshCw /> 刷新
          </Button>
          <span v-if="data?.page" class="text-xs text-muted-foreground">
            共 {{ data.page.total }} 条<template v-if="data.page.truncated">（已截断）</template>
          </span>
        </div>

        <!-- 校验结果 -->
        <div v-if="verify"
             class="rounded-lg border p-3 text-sm"
             :class="verify.ok
               ? 'border-[var(--profit)]/40 bg-[var(--profit)]/5'
               : 'border-destructive/50 bg-destructive/5'">
          <p class="flex items-center gap-2 font-medium">
            <component :is="verify.ok ? ShieldCheck : ShieldAlert"
                       class="size-4"
                       :class="verify.ok ? 'text-[var(--profit)]' : 'text-destructive'" />
            {{ verify.ok ? '日志完整，没有被改动过' : '日志完整性校验未通过' }}
          </p>
          <p class="mt-1 text-xs text-muted-foreground">
            校验了 {{ verify.checked }} 条记录、{{ verify.files }} 个文件
            · 链头 <code class="font-mono">{{ (verify.head || '').slice(0, 16) }}…</code>
          </p>
          <ul v-if="verify.issues?.length" class="mt-2 flex flex-col gap-1 text-xs">
            <li v-for="(iss, i) in verify.issues" :key="i" class="flex gap-2">
              <AlertTriangle class="mt-0.5 size-3.5 shrink-0 text-destructive" />
              <span>
                <span v-if="iss.seq">序号 {{ iss.seq }} · </span>
                <span class="font-mono">{{ iss.file }}</span>：{{ iss.reason }}
              </span>
            </li>
          </ul>
        </div>

        <!-- 查看操作的开关 -->
        <div class="flex items-start gap-3 rounded-lg border p-3">
          <input :checked="!!settings?.recordViews" type="checkbox" id="record-views"
                 class="mt-0.5 size-4 shrink-0 accent-[var(--primary)]"
                 @change="toggleViews" />
          <label for="record-views" class="flex-1 cursor-pointer text-xs">
            <span class="font-medium">记录查看操作</span>
            <span class="text-muted-foreground">
              ——「谁在什么时候看了哪张报表、翻了哪本明细账」。
              <br />
              默认关闭：规范要求审计的是<b>业务操作</b>，而查看类一天能有好几百条，
              混在一起会把要查的淹掉；需要时打开，用上面的「类别」筛出来看。
              打开后同一对象的重复查看在 60 秒内只记一次（不然刷新一次就写一条）。
            </span>
          </label>
        </div>

        <p class="flex items-start gap-2 rounded-lg border bg-muted/30 p-3 text-xs text-muted-foreground">
          <Info class="mt-0.5 size-3.5 shrink-0" />
          <span>
            日志写入后<b>不可修改、不可删除</b>（数据库层面直接拒绝），
            并且每条都串在哈希链上 —— 即使绕过数据库直接改文件，
            「校验完整性」也能指出被改的是哪一条。
            这一页查到的内容可以直接导出成 CSV 交给审计。
          </span>
        </p>

        <Spinner v-if="loading" />
        <EmptyState v-else-if="rows.length === 0"
                    title="没有符合条件的日志"
                    description="换个时间范围或清掉筛选条件再试" />
        <div v-else class="overflow-auto">
          <Table>
            <thead>
              <tr>
                <Th class="w-8" />
                <Th class="w-52">时间</Th>
                <Th class="w-24">操作人</Th>
                <Th class="w-28">操作</Th>
                <Th>摘要</Th>
                <Th class="w-24">结果</Th>
              </tr>
            </thead>
            <tbody>
              <template v-for="e in rows" :key="e.seq">
                <tr class="cursor-pointer hover:bg-accent/30"
                    @click="expanded[e.seq] = !expanded[e.seq]">
                  <Td class="text-muted-foreground">{{ e.seq }}</Td>
                  <Td class="whitespace-nowrap font-mono text-xs">{{ e.at }}</Td>
                  <Td>{{ e.operator }}</Td>
                  <Td>
                    <Badge :variant="e.category === 'read' ? 'secondary'
                      : (e.result === 'failed' ? 'loss' : 'muted')">
                      {{ e.action }}
                    </Badge>
                  </Td>
                  <Td class="text-sm">{{ e.summary }}</Td>
                  <Td>
                    <Badge :variant="e.result === 'failed' ? 'loss' : 'profit'">
                      {{ e.result === 'failed' ? '失败' : '成功' }}
                    </Badge>
                  </Td>
                </tr>
                <tr v-if="expanded[e.seq]" class="bg-muted/20">
                  <Td colspan="6" class="text-xs">
                    <div class="grid grid-cols-2 gap-x-6 gap-y-1">
                      <p><span class="text-muted-foreground">对象：</span>{{ e.entity }} {{ e.entityId }}</p>
                      <p><span class="text-muted-foreground">来源：</span>{{ e.source }}</p>
                      <p><span class="text-muted-foreground">单位：</span>{{ e.company || '—' }}</p>
                      <p class="truncate"><span class="text-muted-foreground">账套：</span>{{ e.book }}</p>
                      <p><span class="text-muted-foreground">程序版本：</span>{{ e.appVersion }}</p>
                      <p v-if="e.message"><span class="text-muted-foreground">说明：</span>{{ e.message }}</p>
                    </div>
                    <pre v-if="e.detail && Object.keys(e.detail).length"
                         class="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-all rounded bg-background/60 p-2">{{ JSON.stringify(e.detail, null, 2) }}</pre>
                    <p class="mt-2 font-mono text-[10px] text-muted-foreground/70">
                      hash {{ (e.hash || '').slice(0, 16) }}… ← prev {{ (e.prevHash || '').slice(0, 16) }}…
                    </p>
                  </Td>
                </tr>
              </template>
            </tbody>
          </Table>
        </div>
      </CardContent>
    </Card>
  </div>
</template>
