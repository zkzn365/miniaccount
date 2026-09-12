<script setup>
/**
 * 科目管理。
 *
 * # 为什么需要这一页
 *
 * 建账时预置的是《小企业会计准则》的科目表，但**准则科目不够用**是常态：
 * 「管理费用」下要分「研发费」「安全生产费」，或者要按项目单独核算。
 * 以前这些只能硬记在预置科目里靠备注区分，报出来就是一锅粥。
 *
 * # 这一页的设计原则：把「为什么不能」摆在台面上
 *
 * 科目表是所有报表的地基，所以有几条硬规则（有分录的不能删、
 * 有分录的不能换上级、子科目的辅助核算不能比父科目少……）。
 * 界面上每一行都直接显示**它被用过多少次、有没有下级、能不能删**，
 * 不能删的还要写明原因 —— 只在后端拦下来，用户看到的就是「点了没反应」。
 */
import { computed, onMounted, ref } from 'vue'
import {
  Plus, Search, Pencil, Trash2, Power, RefreshCw, Info, AlertTriangle,
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
import Table from '@/components/ui/Table.vue'
import Th from '@/components/ui/Th.vue'
import Td from '@/components/ui/Td.vue'
import Spinner from '@/components/ui/Spinner.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Modal from '@/components/ui/Modal.vue'

const loading = ref(false)
const data = ref(null)
const kinds = ref(null)
const keyword = ref('')
const onlyCustom = ref(false)

// 编辑表单
const formOpen = ref(false)
const editing = ref(null) // 编辑时的原科目（null = 新增）
const form = ref(blank())
const saving = ref(false)

function blank() {
  return {
    code: '', name: '', parentCode: '', rootType: '', balanceDir: '',
    auxTypes: [], remark: '', isLeaf: true,
  }
}

async function load() {
  loading.value = true
  const r = await api.accounts()
  loading.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  data.value = r.data
  if (!kinds.value) {
    const k = await api.accountKinds()
    if (k.ok) kinds.value = k.data
  }
}
onMounted(load)

const rows = computed(() => {
  const all = data.value?.rows ?? []
  const kw = keyword.value.trim()
  return all.filter((r) => {
    if (onlyCustom.value && r.isPreset) return false
    if (!kw) return true
    return r.code.includes(kw) || r.name.includes(kw) || (r.fullName ?? '').includes(kw)
  })
})

const stats = computed(() => ({
  total: data.value?.total ?? 0,
  leaf: data.value?.leafCount ?? 0,
  custom: (data.value?.rows ?? []).filter((r) => !r.isPreset).length,
}))

// ---- 新增 / 修改 ----

// 「新增下级」：把父科目的编码与大类直接带进来，用户只需要补后两位。
function addChild(parent) {
  editing.value = null
  form.value = {
    ...blank(),
    parentCode: parent.code,
    rootType: parent.rootType,
    auxTypes: [...(parent.auxTypes ?? [])],
    code: parent.code,
  }
  formOpen.value = true
}

function addRoot() {
  editing.value = null
  form.value = blank()
  formOpen.value = true
}

function edit(row) {
  editing.value = row
  form.value = {
    code: row.code, name: row.name, parentCode: row.parentCode,
    rootType: row.rootType, balanceDir: row.balanceDir,
    auxTypes: [...(row.auxTypes ?? [])], remark: row.remark, isLeaf: row.isLeaf,
  }
  formOpen.value = true
}

async function save() {
  // 新增下级时用户只补了后两位，这里拼成完整编码
  const payload = { ...form.value }
  if (!editing.value && payload.parentCode && payload.code.length < 6) {
    payload.code = payload.parentCode + payload.code
  }
  saving.value = true
  const r = await api.saveAccount(payload)
  saving.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(editing.value ? `已保存科目 ${r.data.code}` : `已新增科目 ${r.data.code} ${r.data.name}`, 'success')
  formOpen.value = false
  await load()
}

async function toggle(row) {
  const next = !row.isEnabled
  const r = await api.setAccountEnabled(row.code, next)
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`${next ? '已启用' : '已停用'}「${row.fullName || row.name}」` +
    (next ? '' : '—— 历史凭证照常显示，只是不能再往上记账'), 'success')
  await load()
}

async function remove(row) {
  if (!confirm(`删除科目「${row.fullName || row.name}」？\n\n它没有被任何凭证用过，删除后不可恢复。`)) return
  const r = await api.deleteAccount(row.code)
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已删除科目 ${row.code}`, 'success')
  await load()
}

function toggleAux(v) {
  const i = form.value.auxTypes.indexOf(v)
  if (i >= 0) form.value.auxTypes.splice(i, 1)
  else form.value.auxTypes.push(v)
}
</script>

<template>
  <div class="flex flex-col gap-5">
    <Card>
      <CardHeader>
        <div class="flex flex-wrap items-start justify-between gap-4">
          <div>
            <CardTitle>科目管理</CardTitle>
            <CardDescription>
              准则科目不够用是常态 —— 在这里加自己的明细科目
              （比如「管理费用」下的「研发费用」）。新增后<b>立刻可以在凭证里选到</b>，
              报表也会自动汇总进去。
              <br />
              编码规则：一级 4 位，每级 2 位，最多 4 级（如 <code class="font-mono">5602</code> →
              <code class="font-mono">560290</code>）。
            </CardDescription>
          </div>
          <div class="flex shrink-0 gap-2">
            <Button @click="addRoot"><Plus /> 新增一级科目</Button>
            <Button variant="outline" size="sm" :disabled="loading" @click="load">
              <RefreshCw /> 刷新
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent class="flex flex-col gap-4">
        <div class="flex flex-wrap items-center gap-3">
          <div class="w-64">
            <Input v-model="keyword" placeholder="按编码或名称搜索" />
          </div>
          <label class="flex cursor-pointer items-center gap-2 text-sm">
            <input v-model="onlyCustom" type="checkbox" class="size-4 accent-[var(--primary)]" />
            只看自建科目
          </label>
          <span class="text-xs text-muted-foreground">
            共 {{ stats.total }} 个科目（明细 {{ stats.leaf }} 个 · 自建 {{ stats.custom }} 个）
          </span>
        </div>

        <p class="flex items-start gap-2 rounded-lg border bg-muted/30 p-3 text-xs text-muted-foreground">
          <Info class="mt-0.5 size-3.5 shrink-0" />
          <span>
            <b>已经被用过的科目不能删</b>（历史凭证会指向一个不存在的科目，报表凭空少一块），
            只能<b>停用</b>：停用后不能再往上记账，但历史凭证照常显示与汇总。
            每一行都标出了它被引用过多少次。
          </span>
        </p>

        <Spinner v-if="loading" />
        <EmptyState v-else-if="rows.length === 0" title="没有匹配的科目"
                    description="换个关键词，或清掉「只看自建科目」" />
        <div v-else class="overflow-auto">
          <Table>
            <thead>
              <tr>
                <Th class="w-28">编码</Th>
                <Th>科目名称</Th>
                <Th class="w-20">大类</Th>
                <Th class="w-16">方向</Th>
                <Th class="w-40">辅助核算</Th>
                <Th class="w-24 text-right">余额</Th>
                <Th class="w-20 text-right">被引用</Th>
                <Th class="w-24">状态</Th>
                <Th class="w-40 text-right">操作</Th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in rows" :key="r.code" class="border-b hover:bg-accent/20">
                <Td class="font-mono text-xs">
                  <span :style="{ paddingLeft: (r.level - 1) * 10 + 'px' }">{{ r.code }}</span>
                </Td>
                <Td>
                  <span :class="r.isLeaf ? '' : 'font-medium'">{{ r.name }}</span>
                  <Badge v-if="!r.isLeaf" variant="muted" class="ml-1.5">汇总</Badge>
                  <Badge v-if="!r.isPreset" variant="debit" class="ml-1.5">自建</Badge>
                </Td>
                <Td class="text-xs text-muted-foreground">{{ r.rootLabel }}</Td>
                <Td class="text-xs">{{ r.dirLabel }}</Td>
                <Td class="text-xs">
                  <span v-if="r.auxLabels?.length">{{ r.auxLabels.join('、') }}</span>
                  <span v-else class="text-muted-foreground">—</span>
                </Td>
                <Td class="num text-right text-xs">{{ fmtMoney(r.balance) }}</Td>
                <Td class="num text-right text-xs">
                  <span :class="r.entryCount > 0 ? 'text-[var(--warn)]' : 'text-muted-foreground'">
                    {{ r.entryCount }}
                  </span>
                </Td>
                <Td>
                  <Badge :variant="r.isEnabled ? 'profit' : 'muted'">
                    {{ r.isEnabled ? '启用' : '停用' }}
                  </Badge>
                </Td>
                <Td class="text-right">
                  <div class="flex justify-end gap-1">
                    <Button v-if="!r.isLeaf" size="sm" variant="ghost"
                            title="在这个汇总科目下加子科目" @click="addChild(r)">
                      <Plus class="size-3.5" />
                    </Button>
                    <Button size="sm" variant="ghost" title="修改" @click="edit(r)">
                      <Pencil class="size-3.5" />
                    </Button>
                    <Button size="sm" variant="ghost"
                            :title="r.isEnabled ? '停用（历史凭证照常）' : '启用'"
                            @click="toggle(r)">
                      <Power class="size-3.5" />
                    </Button>
                    <Button size="sm" variant="ghost" :disabled="!r.canDelete"
                            :title="r.canDelete ? '删除' : r.reason"
                            @click="remove(r)">
                      <Trash2 class="size-3.5"
                              :class="r.canDelete ? 'text-destructive' : 'text-muted-foreground/40'" />
                    </Button>
                  </div>
                </Td>
              </tr>
            </tbody>
          </Table>
        </div>
      </CardContent>
    </Card>

    <!-- 新增 / 修改 -->
    <Modal v-if="formOpen" :title="editing ? `修改科目 ${editing.code}` : '新增科目'"
           @close="formOpen = false">
      <div class="flex flex-col gap-3">
        <div class="grid grid-cols-2 gap-3">
          <div>
            <Label>上级科目</Label>
            <Input v-model="form.parentCode" class="mt-1.5 font-mono"
                   :disabled="!!editing" placeholder="留空 = 一级科目" />
            <p class="mt-1 text-xs text-muted-foreground">
              {{ editing ? '已有科目不能换上级（历史凭证按编码关联）'
                : '填上级编码（如 5602）；大类与辅助核算会自动继承' }}
            </p>
          </div>
          <div>
            <Label>科目编码 *</Label>
            <Input v-model="form.code" class="mt-1.5 font-mono" :disabled="!!editing" />
            <p class="mt-1 text-xs text-muted-foreground">
              {{ editing ? '编码不能改' : '一级 4 位；子科目 = 父编码 + 2 位' }}
            </p>
          </div>
        </div>

        <div>
          <Label>科目名称 *</Label>
          <Input v-model="form.name" class="mt-1.5" placeholder="如：研发费用" />
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <Label>科目大类</Label>
            <select v-model="form.rootType" :disabled="!!editing"
                    class="mt-1.5 h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm">
              <option value="">请选择</option>
              <option v-for="o in kinds?.rootTypes ?? []" :key="o.value" :value="o.value">
                {{ o.label }}
              </option>
            </select>
            <p class="mt-1 text-xs text-muted-foreground">
              {{ form.parentCode ? '有上级时留空即自动继承' : '决定余额方向与报表归集' }}
            </p>
          </div>
          <div>
            <Label>科目属性</Label>
            <div class="mt-1.5 flex gap-2">
              <Button :variant="form.isLeaf ? 'default' : 'outline'" size="sm"
                      @click="form.isLeaf = true">明细科目（可记账）</Button>
              <Button :variant="!form.isLeaf ? 'default' : 'outline'" size="sm"
                      @click="form.isLeaf = false">汇总科目</Button>
            </div>
          </div>
        </div>

        <div>
          <Label>辅助核算（可从父科目继承，不能比父科目少）</Label>
          <div class="mt-1.5 flex flex-wrap gap-2">
            <Button v-for="o in kinds?.auxTypes ?? []" :key="o.value" size="sm"
                    :variant="form.auxTypes.includes(o.value) ? 'default' : 'outline'"
                    @click="toggleAux(o.value)">{{ o.label }}</Button>
          </div>
        </div>

        <div>
          <Label>备注</Label>
          <Input v-model="form.remark" class="mt-1.5" placeholder="选填" />
        </div>

        <p v-if="editing && editing.entryCount > 0"
           class="flex items-start gap-2 rounded border border-[var(--warn)]/50 bg-[var(--warn)]/5 p-2 text-xs">
          <AlertTriangle class="mt-0.5 size-3.5 shrink-0 text-[var(--warn)]" />
          <span>
            这个科目已经被 {{ editing.entryCount }} 条分录引用过 ——
            可改的只有名称、备注与辅助核算（只增不减），编码与上级都锁住了。
          </span>
        </p>

        <div class="flex justify-end gap-2 pt-1">
          <Button variant="ghost" @click="formOpen = false">取消</Button>
          <Button :disabled="saving" @click="save">{{ saving ? '保存中…' : '保存' }}</Button>
        </div>
      </div>
    </Modal>
  </div>
</template>
