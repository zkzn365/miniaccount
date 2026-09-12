/**
 * 当前账套的状态。
 *
 * # 为什么单独一个模块
 *
 * 「有没有账套」是**整个界面的前置条件**，而它原来只活在 App.vue 的
 * 一个 ref 里。于是每个视图都得自己去问一次，问不到就各报各的错 ——
 * 实测到的现象是：启动瞬间刷出两条错误提示，
 * 一条来自 App.vue 问 currentBook，一条来自首页问 overview，
 * 而首页本不该被渲染出来。
 *
 * 统一在这里问一次，路由与视图共用同一个答案。
 */
import { reactive } from 'vue'
import { api } from './api'
import { resetBookkeeper } from './operator'

export const bookState = reactive({
  /** 是否已经问过一次（问过之前不要拿 open=false 当结论）。 */
  ready: false,
  open: false,
  book: null,
  path: '',
})

/**
 * 重新读一次账套状态。返回 `{ ok, fault }`。
 *
 * 与界面之间只传状态、不抛异常：调用点要么忽略失败（视图会自己报错），
 * 要么自己决定怎么提示。
 */
export async function refreshBook() {
  // 记账人是账套级的：换账套先清缓存，免得显示上一本的签章人
  resetBookkeeper()
  const r = await api.currentBook()
  bookState.ready = true
  if (!r.ok) {
    // 问不到 ≠ 没有。保持 open=false，但把 ready 置上，
    // 调用方据此决定是「跳建账」还是「报错」。
    bookState.open = false
    bookState.book = null
    return { ok: false, fault: r.fault }
  }
  bookState.open = !!r.data?.open
  bookState.book = r.data?.book ?? null
  bookState.path = r.data?.path ?? ''
  return { ok: true }
}

/** 关闭账套（回欢迎页用）。 */
export function clearBook() {
  bookState.open = false
  bookState.book = null
  bookState.path = ''
}
