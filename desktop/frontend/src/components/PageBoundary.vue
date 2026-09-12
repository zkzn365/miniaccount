<script setup>
/**
 * 页面级错误边界。
 *
 * # 为什么必须有
 *
 * Vue 的 `RouterView` 一旦在渲染某个页面时抛错，**整个视图就卡住了**：
 * 换路由也不会再渲染新页面，用户看到的还是上一页的内容。
 * 实测的表现是「点了菜单没反应」，而且没有任何提示 ——
 * 用户只会以为软件坏了，或者以为是自己点错了。
 *
 * 这对记账软件尤其糟：会计会反复点、反复试，最后放弃并怀疑数据。
 *
 * 有了边界之后：
 *   - 只有出错的那个页面被替换成一张说明卡，侧栏和顶栏照常可用；
 *   - 换一个页面就自动恢复（watch 路由清掉错误）；
 *   - 错误同时写进程序日志（见 lib/diag.js），用户报问题时有据可查。
 *
 * 注意 `onErrorCaptured` 返回 **false**：这是「我已经处理了」，
 * 不让错误继续往上冒。全局处理器（main.js）仍会收到一份，
 * 因为它挂的是 window 的 error 事件 —— 两边都要有记录。
 */
import { onErrorCaptured, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { AlertTriangle, RotateCcw, Home } from 'lucide-vue-next'
import { logDiag } from '@/lib/diag'
import Card from '@/components/ui/Card.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Button from '@/components/ui/Button.vue'

const route = useRoute()
const router = useRouter()
const err = ref(null)
let lastPath = route.fullPath

onErrorCaptured((e) => {
  err.value = e ?? new Error('未知错误')
  logDiag('error', `页面「${route.meta?.title ?? route.path}」渲染失败：` +
    (err.value?.message ?? String(err.value)))
  return false
})

// ★ 换页面就清掉错误 —— 否则一个页面的 bug 会把整个界面永久冻住。
watch(() => route.fullPath, (p) => {
  if (p !== lastPath) {
    lastPath = p
    err.value = null
  }
})

function retry() {
  const p = route.fullPath
  err.value = null
  // 回同一个路径不会触发导航，先跳走再跳回来，强制重建这个页面
  router.replace('/dashboard').then(() => router.replace(p)).catch(() => {})
}
</script>

<template>
  <Card v-if="err" class="border-destructive/40">
    <CardContent class="pt-5">
      <p class="flex items-center gap-2 font-medium text-destructive">
        <AlertTriangle class="size-4" />
        这个页面出错了
      </p>
      <p class="mt-2 text-sm text-muted-foreground">
        其它页面还能正常用 —— 侧栏和顶栏没有受影响。
        错误已经记进程序日志，可以把日志一并发给我们。
      </p>
      <pre class="mt-3 max-h-40 overflow-auto whitespace-pre-wrap break-all rounded border bg-muted/30 p-3 text-xs">{{ err?.message ?? err }}</pre>
      <div class="mt-4 flex gap-2">
        <Button variant="outline" size="sm" @click="retry"><RotateCcw /> 重试这个页面</Button>
        <Button variant="ghost" size="sm" @click="router.replace('/dashboard')">
          <Home /> 回首页
        </Button>
      </div>
    </CardContent>
  </Card>
  <slot v-else />
</template>
