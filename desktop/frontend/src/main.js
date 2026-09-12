import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import { notify, toFault } from './lib/api'
import { logDiag } from './lib/diag'
import { refreshBook, bookState } from './lib/book'
import './style.css'

// ---------------------------------------------------------------------------
// 界面自身的异常必须看得见
// ---------------------------------------------------------------------------
//
// 桌面应用没有开发者工具，页面里抛出来的异常默认**什么都不会发生**：
// 用户看到的是「点了没反应」或者一块空白，而他没有任何办法说明
// 到底哪一步不对。记账软件尤其不能这样 ——
// 用户会以为是自己的操作问题，反复重试。
//
// 这里把未捕获的异常与未处理的 promise 失败都转成提示条。
// 同一个错误只提示一次：一个渲染循环里的异常能在几百毫秒内刷满屏幕。
const seen = new Set()
function surface(label, e) {
  const fault = toFault(e)
  const key = `${label}:${fault.message}`
  if (seen.has(key)) return
  seen.add(key)
  notify(`${label}：${fault.message}`, 'error', fault.detail || '')
  // 程序日志里也留一份：用户报「点了没反应」时，这是我们唯一的线索
  logDiag('error', `${label}：${fault.message}` +
    (fault.detail ? `\n${fault.detail}` : ''))
}
window.addEventListener('error', (ev) => surface('界面出错', ev.error ?? ev.message))
window.addEventListener('unhandledrejection', (ev) => surface('异步操作失败', ev.reason))

// ---------------------------------------------------------------------------
// 先问清楚「有没有账套」，再决定第一个页面
// ---------------------------------------------------------------------------
//
// ★ 顺序很重要。
//
// 挂载之后再判断的话，首页（它一上来就要账套数据）已经被渲染出来了，
// 于是启动瞬间必然刷出两条错误提示：一条来自首页，一条来自这里。
// 用户看到的第一屏就是报错 —— 而其实什么都没做错，只是还没建账。
async function boot() {
  const r = await refreshBook()
  if (!r.ok || !bookState.open) {
    if (!r.ok) {
      // 连账套状态都问不到（绑定不可用等）。说清楚，并且仍然去欢迎页：
      // 停在一个需要账套的页面上只会继续报错。
      notify(r.fault.message, 'error', r.fault.detail || '')
      logDiag('error', `读取账套状态失败：${r.fault.message}`)
    } else {
      logDiag('info', '界面启动：还没有账套，进入建账页')
    }
    await router.replace('/welcome')
  } else {
    logDiag('info', `界面启动：已打开账套「${bookState.book?.companyName ?? ''}」`)
  }
  createApp(App).use(router).mount('#app')
  logDiag('info', '界面已挂载')
}

boot()
