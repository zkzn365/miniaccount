<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  Home, CalendarRange, Table2, BookOpen, Sparkles, Settings as Cog,
  Sun, Moon, PanelLeftClose, PanelLeftOpen, FileText, Landmark, Users,
  Receipt, Plane, Hourglass, FileCheck, Scale, Columns3, Sigma, Percent,
  ScrollText, ListTree, Shapes, Building2,
} from 'lucide-vue-next'
import { refreshBook, bookState } from '@/lib/book'
import Button from '@/components/ui/Button.vue'
import Toast from '@/components/ui/Toast.vue'
import Spinner from '@/components/ui/Spinner.vue'
import PageBoundary from '@/components/PageBoundary.vue'
import logo from '@/assets/logo.png'

const route = useRoute()
const router = useRouter()
const nav = router.getRoutes().filter((r) => r.meta?.title && !r.meta?.hidden)

const icons = {
  Home, FileText, Receipt, Plane, Landmark, Users,
  CalendarRange, Table2, BookOpen, Hourglass, FileCheck, Scale, Columns3, Sigma, Percent,
  Sparkles, Settings: Cog, ScrollText, ListTree, Shapes, Building2,
}

// 账套状态统一放在 lib/book.js —— 启动流程（main.js）与路由都用它，
// 这里不再自己问一次。原来各问各的，结果是启动瞬间刷出两条重复的错误。
const book = computed(() => bookState.book)
const loading = ref(!bookState.ready)
const collapsed = ref(false)

// 深色模式跟随系统，同时允许手动切换并记住选择。
// 会计经常要在晚上加班对账，纯白界面在这种场景下很刺眼。
const dark = ref(false)
function applyTheme() {
  document.documentElement.classList.toggle('dark', dark.value)
  localStorage.setItem('theme', dark.value ? 'dark' : 'light')
}
function toggleTheme() { dark.value = !dark.value; applyTheme() }

onMounted(async () => {
  const saved = localStorage.getItem('theme')
  dark.value = saved ? saved === 'dark' : window.matchMedia?.('(prefers-color-scheme: dark)').matches
  applyTheme()
  // 启动流程已经问过一次（见 main.js）；这里只是兜底保证 loading 收尾。
  if (!bookState.ready) await refreshBook()
  loading.value = false
})

async function onBookChanged() {
  await refreshBook()
  if (!bookState.open) router.replace('/welcome')
}

// 当前期间：最早的未结账期间。
// 界面顶部常驻显示它 —— 会计必须随时知道「我现在记的是哪个月」，
// 记错月份是这类软件最常见也最难发现的操作失误。
// 没打开账套时不允许切页面。
//
// 判断放在这里而不是只靠路由守卫：菜单要**看起来**就不能点
// （置灰 + 提示），守卫只是兜底。
const canNavigate = computed(() => bookState.open)
const needsBookHint = '请先建账或打开一个账套'

const currentPeriod = computed(() => {
  const ps = book.value?.periods ?? []
  return ps.find((p) => p.status === 'open') ?? null
})
</script>

<template>
  <div class="flex h-full">
    <!-- 侧栏 -->
    <aside
      :class="[
        'flex shrink-0 flex-col border-r bg-card transition-[width] duration-200',
        collapsed ? 'w-[4.25rem]' : 'w-56',
      ]"
    >
      <div class="flex h-14 items-center gap-2 px-4">
        <!-- ★ 用应用图标本身当界面 logo，而不是另写一个「账」字方块 ——
             用户在外面看到的（Dock / 任务栏 / 访达）就是它，
             里面再换一个样子等于两套品牌。
             alt 留空：旁边的「小账本」已经把名字说出来了，
             再加一遍读屏会念两次。 -->
        <img :src="logo" alt="" class="size-7 shrink-0" />
        <span v-if="!collapsed" class="truncate text-sm font-semibold">小账本</span>
      </div>

      <!-- ★ 没建账套时菜单不可点。
           「点得动但进去是空的 / 报错」比「点不动」糟糕得多：
           用户会以为软件坏了，而不是以为自己还没建账。 -->
      <nav class="flex flex-1 flex-col gap-0.5 px-2 py-2">
        <template v-for="r in nav" :key="r.path">
          <RouterLink
            v-if="canNavigate"
            :to="r.path"
            :class="[
              'flex items-center gap-2.5 rounded-md px-2.5 py-2 text-sm transition-colors',
              route.path === r.path
                ? 'bg-accent font-medium text-accent-foreground'
                : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
            ]"
            :title="collapsed ? r.meta.title : ''"
          >
            <component :is="icons[r.meta.icon]" class="size-4 shrink-0" />
            <span v-if="!collapsed" class="truncate">{{ r.meta.title }}</span>
          </RouterLink>

          <span
            v-else
            class="flex cursor-not-allowed items-center gap-2.5 rounded-md px-2.5 py-2 text-sm text-muted-foreground/40"
            :title="needsBookHint"
            aria-disabled="true"
          >
            <component :is="icons[r.meta.icon]" class="size-4 shrink-0" />
            <span v-if="!collapsed" class="truncate">{{ r.meta.title }}</span>
          </span>
        </template>

        <p v-if="!canNavigate && !collapsed"
           class="mt-2 rounded-md border border-dashed px-2.5 py-2 text-xs text-muted-foreground">
          还没有账套。建账之后这里的功能才会解锁。
        </p>
      </nav>

      <div class="border-t p-2">
        <button
          class="flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-sm text-muted-foreground hover:bg-accent/60 hover:text-foreground"
          @click="collapsed = !collapsed"
        >
          <component :is="collapsed ? PanelLeftOpen : PanelLeftClose" class="size-4 shrink-0" />
          <span v-if="!collapsed">收起侧栏</span>
        </button>
      </div>
    </aside>

    <!-- 主区 -->
    <div class="flex min-w-0 flex-1 flex-col">
      <header class="flex h-14 shrink-0 items-center gap-4 border-b bg-card px-5">
        <div class="min-w-0 flex-1">
          <h1 class="truncate text-sm font-semibold">{{ route.meta.title }}</h1>
          <p class="truncate text-xs text-muted-foreground">
            {{ book?.companyName ?? '尚未打开账套' }}
          </p>
        </div>

        <div
          v-if="currentPeriod"
          class="flex items-center gap-2 rounded-md border px-2.5 py-1 text-xs"
          title="当前可记账期间：最早的未结账期间"
        >
          <span class="text-muted-foreground">当前账期</span>
          <span class="font-medium">{{ currentPeriod.label }}</span>
        </div>

        <RouterLink to="/settings" class="text-xs text-muted-foreground hover:text-foreground">
          {{ book?.taxTypeLabel }}
        </RouterLink>

        <Button variant="ghost" size="icon" :title="dark ? '切换到浅色' : '切换到深色'" @click="toggleTheme">
          <component :is="dark ? Sun : Moon" />
        </Button>
      </header>

      <main class="min-h-0 flex-1 overflow-auto p-5">
        <Spinner v-if="loading" />
        <!-- 页面级错误边界：某个页面渲染失败时只换掉那一页，
             侧栏照常可用，换页自动恢复。没有它的话 RouterView 会卡死。 -->
        <PageBoundary v-else :key="book?.companyName ?? 'nobook'">
          <RouterView @book-changed="onBookChanged" />
        </PageBoundary>
      </main>
    </div>

    <Toast />
  </div>
</template>
