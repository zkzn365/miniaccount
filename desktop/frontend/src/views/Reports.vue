<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { AlertTriangle, Download, RefreshCw, Printer } from 'lucide-vue-next'
import { api, notify } from '@/lib/api'
import { fmtMoney } from '@/lib/format'
import Card from '@/components/ui/Card.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Spinner from '@/components/ui/Spinner.vue'
import EmptyState from '@/components/ui/EmptyState.vue'

const route = useRoute()
const router = useRouter()

const REPORTS = [
  { kind: 'bs', name: '资产负债表', hint: '会小企 01 表 · 53 行' },
  { kind: 'pl', name: '利润表', hint: '会小企 02 表 · 32 行' },
  { kind: 'cashflow', name: '现金流量表', hint: '会小企 03 表 · 直接法' },
  { kind: 'trial', name: '科目余额表', hint: '六栏式 · 借贷分列' },
  { kind: 'contact', name: '往来单位余额表', hint: '按客户/供应商汇总' },
]

const kind = ref(route.query.kind ?? 'bs')
const year = ref(Number(route.query.year) || new Date().getFullYear())
const month = ref(Number(route.query.month) || new Date().getMonth() + 1)
const prefix = ref('')

const data = ref(null)
const loading = ref(false)

// 资产负债表是时点报表，只需要年月；
// 明细类报表需要区间。这里统一用「年月」，由 Go 侧转成日期区间。
const needPrefix = computed(() => kind.value === 'contact')

async function load() {
  loading.value = true
  const r = await api.report({
    kind: kind.value,
    year: year.value,
    month: month.value,
    accountPrefix: prefix.value,
  })
  loading.value = false
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); data.value = null; return }
  data.value = r.data
}

// 切换报表时把状态写进 URL：这样刷新页面、或者从首页跳过来，
// 看到的还是同一张表。桌面应用里「刷新回到默认页」很烦人。
watch([kind, year, month], () => {
  router.replace({ query: { kind: kind.value, year: year.value, month: month.value } })
  load()
})
onMounted(load)

const rows = computed(() => data.value?.rows ?? [])
const columns = computed(() => data.value?.columns ?? [])
const fatal = computed(() => (data.value?.issues ?? []).some((i) => i.fatal))

// 资产负债表是左右两栏：前两列是资产侧，后两列是负债和权益侧。
const isBS = computed(() => kind.value === 'bs')

const periods = ref([])
onMounted(async () => {
  const r = await api.periods()
  if (r.ok) periods.value = r.data.periods
})

// 打印走系统打印对话框 —— 里面就有「另存为 PDF」。
//
// 不自己生成 PDF 是刻意的：中文字体动辄十几 MB，能商用的
// （思源系列，SIL OFL）也要随程序分发；而操作系统的打印子系统
// 本来就有正确的字体、分页与 PDF 导出能力。
// 样式表里的 @media print 负责把界面还原成一张纸的报表。
function printReport() {
  window.print()
}

// 导出走 Go 侧：直接取数写 xlsx（金额写成数值，Excel 里能直接求和），
// 而不是把屏幕上渲染出来的表格抄一遍 —— 后者会丢被滚动截断的行，
// 且金额会变成字符串。
async function exportExcel() {
  const hint = await api.suggestExportPath(kind.value, year.value, month.value)
  const def = hint.ok ? hint.data : '报表.xlsx'
  const path = window.prompt('导出到（完整路径，可自行修改文件名）', def)
  if (!path) return
  const r = await api.exportReport({
    kind: kind.value, dest: path,
    year: year.value, month: month.value,
    accountPrefix: prefix.value,
  })
  if (!r.ok) { notify(r.fault.message, 'error', r.fault.detail); return }
  notify(`已导出：${r.data.path}（${r.data.rows} 行）`, 'success')
}

function pickPeriod(p) {
  year.value = p.year
  month.value = p.month
}
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- 报表切换 -->
    <div class="flex flex-wrap items-center gap-2">
      <Button
        v-for="r in REPORTS"
        :key="r.kind"
        :variant="kind === r.kind ? 'default' : 'outline'"
        size="sm"
        :title="r.hint"
        @click="kind = r.kind"
      >
        {{ r.name }}
      </Button>

      <div class="ml-auto flex items-center gap-2">
        <template v-if="needPrefix">
          <Input v-model="prefix" class="h-8 w-28" placeholder="科目前缀" />
        </template>
        <select
          class="h-8 rounded-md border border-input bg-transparent px-2 text-sm"
          :value="`${year}-${month}`"
          @change="(e) => { const [y, m] = e.target.value.split('-'); year = +y; month = +m }"
        >
          <option v-for="p in periods" :key="p.label" :value="p.label">
            {{ p.label }}（{{ p.statusLabel }}）
          </option>
        </select>
        <Input v-model.number="year" type="number" class="h-8 w-20" />
        <Input v-model.number="month" type="number" class="h-8 w-16" />
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" /> 刷新
        </Button>
        <Button variant="ghost" size="sm" title="打印或另存为 PDF" @click="printReport">
          <Printer /> 打印 / PDF
        </Button>
        <Button variant="ghost" size="sm" :disabled="!data" @click="exportExcel">
          <Download /> 导出 Excel
        </Button>
      </div>
    </div>

    <!-- 勾稽问题：必须显眼。
         一张数字齐全但内部对不上的报表，比一张明显缺数的报表危险得多。 -->
    <div
      v-for="(iss, i) in (data?.issues ?? [])"
      :key="i"
      class="flex items-start gap-2 rounded-lg border p-3 text-sm"
      :class="iss.fatal
        ? 'border-destructive/50 bg-destructive/10'
        : 'border-[var(--warn)]/50 bg-[var(--warn)]/10'"
    >
      <AlertTriangle class="mt-0.5 size-4 shrink-0" :class="iss.fatal ? 'text-[var(--loss)]' : 'text-[var(--warn)]'" />
      <span>{{ iss.text }}</span>
    </div>

    <Card>
      <CardContent class="pt-5">
        <div class="mb-3 flex items-baseline justify-between">
          <div>
            <h2 class="text-base font-semibold">{{ data?.title ?? '报表' }}</h2>
            <p class="text-xs text-muted-foreground">{{ data?.subtitle }}</p>
          </div>
          <p class="text-xs text-muted-foreground">单位：元</p>
        </div>

        <Spinner v-if="loading" />
        <EmptyState
          v-else-if="rows.length === 0"
          title="这张表暂时是空的"
          description="换个期间试试，或者先录入凭证"
        />

        <div v-else class="overflow-auto">
          <!-- 资产负债表：左右两栏各自成表。
               官方表的左栏是「资产」，右栏是「负债和所有者权益」，
               两栏行数不同但顶对齐 —— 这是会计看了几十年的版式。 -->
          <table v-if="isBS" class="w-full text-sm">
            <thead>
              <tr class="border-b">
                <th class="h-8 px-2 text-left text-xs font-medium text-muted-foreground">行次</th>
                <th class="h-8 px-2 text-left text-xs font-medium text-muted-foreground">资产</th>
                <th class="h-8 px-2 text-right text-xs font-medium text-muted-foreground">期末余额</th>
                <th class="h-8 px-2 text-right text-xs font-medium text-muted-foreground">年初余额</th>
                <th class="h-8 w-px bg-border p-0" />
                <th class="h-8 px-2 text-left text-xs font-medium text-muted-foreground">行次</th>
                <th class="h-8 px-2 text-left text-xs font-medium text-muted-foreground">负债和所有者权益</th>
                <th class="h-8 px-2 text-right text-xs font-medium text-muted-foreground">期末余额</th>
                <th class="h-8 px-2 text-right text-xs font-medium text-muted-foreground">年初余额</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="(row, i) in rows" :key="i"
                class="border-b last:border-0"
                :class="row.bold ? 'bg-muted/40 font-medium' : 'hover:bg-accent/30'"
              >
                <td class="px-2 py-1 font-mono text-xs text-muted-foreground">{{ row.no }}</td>
                <td class="px-2 py-1">
                  <span :class="row.isMemo ? 'text-muted-foreground' : ''">{{ row.label }}</span>
                </td>
                <td class="num px-2 py-1" :class="row.values[0] < 0 ? 'text-[var(--loss)]' : ''">
                  {{ fmtMoney(row.values[0], { blankZero: true }) }}
                </td>
                <td class="num px-2 py-1 text-muted-foreground">
                  {{ fmtMoney(row.values[1], { blankZero: true }) }}
                </td>
                <td class="w-px bg-border p-0" />
                <td class="px-2 py-1 font-mono text-xs text-muted-foreground">{{ row.rightNo }}</td>
                <td class="px-2 py-1">
                  <span :class="row.rightLabel ? '' : 'text-transparent'">{{ row.rightLabel || '—' }}</span>
                </td>
                <td class="num px-2 py-1" :class="row.values[2] < 0 ? 'text-[var(--loss)]' : ''">
                  {{ fmtMoney(row.values[2], { blankZero: true }) }}
                </td>
                <td class="num px-2 py-1 text-muted-foreground">
                  {{ fmtMoney(row.values[3], { blankZero: true }) }}
                </td>
              </tr>
            </tbody>
          </table>

          <!-- 其它报表：统一的「行次 + 项目 + 若干金额列」 -->
          <table v-else class="w-full text-sm">
            <thead>
              <tr class="border-b">
                <th class="h-8 px-2 text-left text-xs font-medium text-muted-foreground">行次</th>
                <th class="h-8 px-2 text-left text-xs font-medium text-muted-foreground">项目</th>
                <th
                  v-for="(c, i) in columns" :key="i"
                  class="h-8 px-2 text-right text-xs font-medium text-muted-foreground whitespace-nowrap"
                >{{ c }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="(row, i) in rows" :key="i"
                class="border-b last:border-0"
                :class="row.bold ? 'bg-muted/40 font-medium' : 'hover:bg-accent/30'"
              >
                <td class="px-2 py-1 font-mono text-xs text-muted-foreground">{{ row.no }}</td>
                <td class="px-2 py-1">
                  <span
                    :style="{ paddingLeft: (row.indent ?? 0) * 0.9 + 'rem' }"
                    :class="row.isMemo ? 'text-muted-foreground' : ''"
                  >{{ row.label }}</span>
                </td>
                <td
                  v-for="(v, j) in row.values" :key="j"
                  class="num px-2 py-1"
                  :class="v < 0 ? 'text-[var(--loss)]' : ''"
                >{{ fmtMoney(v, { blankZero: true }) }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <p v-if="isBS" class="mt-3 text-xs text-muted-foreground">
          资产总计应等于负债和所有者权益总计 —— 不相等时上方会出现红色提示。
        </p>
      </CardContent>
    </Card>
  </div>
</template>
