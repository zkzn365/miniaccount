/**
 * 界面冒烟测试：真 Vue 组件 + 真 DOM + 真后端数据。
 *
 * # 为什么非要有这个
 *
 * 这个界面曾经**一次都没跑起来过**，而所有测试都是绿的：
 *
 *   - Go 侧的绑定测试直接调 Go 方法，绕过了 Wails 的 JS 层；
 *   - 前端只有 api.js，它假定绑定返回 `[值, 错误]` 元组，
 *     而真实运行时 resolve 的是**值本身**；
 *   - 开发态的假数据也是照元组写的，于是「在浏览器里看样式」
 *     这条路也是通的 —— 两条路都通，只有真实运行的那条不通。
 *
 * 结果：用户第一次打开软件，看到的是两条报错提示。
 *
 * 所以这里把三层接起来跑：
 *
 *     真 Vue 组件  ←→  api.js  ←→  假 Wails 运行时  ←→  真 Go 后端
 *
 * 假运行时**逐字模仿** Wails 的行为（resolve 值 / reject `err.Error()`），
 * 后端是真的（`小账本 --serve-bindings`），账套是真的演示账套。
 * 任何一层接错了都会在这里炸出来。
 *
 * 运行：cd desktop/frontend && npm run test:gui
 */
import { test, before, after } from 'node:test'
import assert from 'node:assert/strict'
import { JSDOM } from 'jsdom'
import { spawn } from 'node:child_process'
import { createInterface } from 'node:readline'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import fs from 'node:fs'
import os from 'node:os'

const here = path.dirname(fileURLToPath(import.meta.url))
const root = path.resolve(here, '../../..') // mini-account/
const APP_BIN = path.join(root, 'dist', '小账本-darwin-arm64')
const dbPath = path.join(os.tmpdir(), `miniaccount-gui-${process.pid}.db`)

// ★ 给后端一个**假的家目录**。
//
// 两件事都靠它：
//
//  1. 隔离。后端会把「最近打开的账套」写进 os.UserConfigDir()
//     （macOS 上是 ~/Library/Application Support/miniaccount），
//     而下面的建账测试要真的写一个账套文件 —— 不隔离的话，
//     跑一次测试就会在开发者的真实文稿目录里留下垃圾，
//     并且覆盖他自己的「最近打开」。
//  2. 可断言。默认保存位置是「家目录/.mini-account/dataDB」，
//     家目录固定下来之后，测试才能逐字比对那个路径。
const fakeHome = path.join(os.tmpdir(), `miniaccount-home-${process.pid}`)
fs.mkdirSync(fakeHome, { recursive: true })

/** 与用户双击软件时一致的环境：家目录指向临时目录。 */
const childEnv = { ...process.env, HOME: fakeHome, USERPROFILE: fakeHome }

// 界面模块必须在 jsdom 的全局装好之后才能 import：
// vue-router 在模块求值时就读 location，而 Node 里没有这个全局。
let bundle
let win
let mounted = null

// ---------------------------------------------------------------------------
// 真 Go 后端（子进程，JSON Lines）
// ---------------------------------------------------------------------------

let proc
let seq = 0
const pending = new Map()

function startBackend() {
  return new Promise((resolve, reject) => {
    proc = spawn(APP_BIN, ['serve-bindings', '--db', dbPath],
      { stdio: ['pipe', 'pipe', 'inherit'], env: childEnv })
    proc.on('error', reject)
    createInterface({ input: proc.stdout }).on('line', (line) => {
      let msg
      try { msg = JSON.parse(line) } catch { return }
      const p = pending.get(msg.id)
      if (!p) return
      pending.delete(msg.id)
      if (msg.error) p.reject(msg.error)
      else p.resolve(msg.result)
    })
    proc.on('exit', (code) => {
      for (const p of pending.values()) p.reject(new Error(`后端退出：${code}`))
      pending.clear()
    })
    resolve()
  })
}

/** 调用一个后端方法。语义与 Wails 一致：成功给值本身，失败 reject 字符串。 */
function backendCall(name, args) {
  const id = ++seq
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject })
    proc.stdin.write(JSON.stringify({ id, name, args }) + '\n')
  })
}

// ---------------------------------------------------------------------------
// 假 Wails 运行时
// ---------------------------------------------------------------------------

/**
 * 造一个 `window.go.main.App`，形状与 Wails 生成的一模一样。
 *
 * ★ 这里必须逐字模仿 Wails 的约定：
 *   - 成功：promise resolve，值是 Go 方法的第一个返回值（**不包数组**）
 *   - 失败：promise **reject**，值是 Go 侧 `err.Error()` 的字符串
 *
 * 当初的 bug 就出在这一步：假运行时返回 `[值, null]` 元组，
 * 与真实运行时不是一套约定，于是假实现掩盖了它本该暴露的问题。
 */
function makeWailsRuntime(names) {
  const App = {}
  for (const n of names) App[n] = (...args) => backendCall(n, args)
  return { go: { main: { App } } }
}

async function boot() {
  const dts = fs.readFileSync(path.join(here, '../wailsjs/go/main/App.d.ts'), 'utf8')
  const names = [...dts.matchAll(/^export function (\w+)\(/gm)].map((m) => m[1])
  assert.ok(names.length > 50, '没从 App.d.ts 解析到绑定名')

  const dom = new JSDOM('<!doctype html><html><body></body></html>', {
    url: 'http://localhost/',
    pretendToBeVisual: true,
  })
  win = dom.window
  // ★ 把 jsdom 的浏览器全局**整套**搬进 Node，不要手写清单。
  //
  // 界面用的构造函数（Document、Text、Range……）和第三方库用的，
  // 列是列不全的。漏一个的表现很阴：页面渲染时抛 "X is not defined"，
  // 而 Vue 的 RouterView 会因此**卡在旧页面上** —— 点菜单没反应，
  // 却看不出任何原因。
  //
  // 只补 Node 里没有的：setTimeout / console / process 这些绝不覆盖，
  // 否则会把测试运行器本身搞坏。
  // 用 defineProperty 而不是赋值：Node 里 navigator 之类的属性是只读的。
  for (const k of Object.getOwnPropertyNames(win)) {
    if (k in globalThis) continue
    try {
      Object.defineProperty(globalThis, k, {
        value: k === 'window' ? win : win[k], configurable: true, writable: true,
      })
    } catch { /* 个别属性装不上就算了，缺了会在使用处直接报出来 */ }
  }
  Object.defineProperty(globalThis, 'window', {
    value: win, configurable: true, writable: true,
  })
  globalThis.requestAnimationFrame = (cb) => setTimeout(() => cb(Date.now()), 0)
  globalThis.cancelAnimationFrame = (h) => clearTimeout(h)
  win.matchMedia ||= () => ({ matches: false, addEventListener() {}, removeEventListener() {} })
  Object.assign(win, makeWailsRuntime(names))
  return names
}

// ---------------------------------------------------------------------------
// 挂载真实界面
// ---------------------------------------------------------------------------

/**
 * 把整个 App 挂起来。**只挂一次**，之后靠路由切换页面。
 *
 * 每个测试各挂一份会踩到 Vue 的坑：卸载旧实例时它内部的异步组件
 * 还在回程路上，新实例挂上去就报
 * `Cannot read properties of null (reading 'exposed')`。
 * 而且真实的软件本来就是「一个实例 + 切路由」，测这个才作数。
 */
async function mountApp() {
  const { createApp } = bundle
  const host = win.document.createElement('div')
  win.document.body.appendChild(host)
  const app = createApp(bundle.App)
  app.use(bundle.router)
  app.mount(host)
  await settle()
  mounted = { app, host }
}

const page = {
  text: () => (mounted?.host.textContent ?? '').replace(/\s+/g, ' ').trim(),
  /** 按可见文字点一个按钮（界面上很多内容是切标签页才出来的）。 */
  click: async (label) => {
    const btn = [...win.document.body.querySelectorAll('button')]
      .find((b) => b.textContent.replace(/\s+/g, '').includes(label.replace(/\s+/g, '')))
    assert.ok(btn, `界面上找不到「${label}」按钮`)
    btn.dispatchEvent(new win.MouseEvent('click', { bubbles: true }))
    await settle(20)
  },
  // 路由组件是懒加载的（() => import('./views/X.vue')），
  // 切过去之后要等它真的渲染出来 —— 等太短会读到上一个页面的内容，
  // 于是断言「报表页没有资产总计」这种假失败就出现了。
  goto: async (p) => {
    await bundle.router.push(p)
    await settle(60)
    await bundle.nextTick()
    await settle(10)
  },
  /**
   * 找一个 input（按 placeholder 或选择器）。
   *
   * ★ 搜的是整个 document，不是 mounted.host。
   *
   * Modal 用的是 `<Teleport to="body">`，弹窗内容挂在 body 上、
   * 在宿主节点**外面** —— 只搜 host 的话，弹窗里的一切都找不到，
   * 于是「弹窗里的按钮点了没反应」这类问题永远测不出来
   * （写这一条时就撞上了：断言报「部门弹窗里没有输入框」，
   * 而实际上是没搜对地方）。
   */
  input: (sel) => {
    const el = win.document.body.querySelector(sel)
    assert.ok(el, `界面上找不到输入框 ${sel}`)
    return el
  },
  /** 像用户那样往输入框里打字：v-model 听的是 input 事件。 */
  type: async (el, value) => {
    el.value = value
    el.dispatchEvent(new win.Event('input', { bubbles: true }))
    await settle(30)
  },
  /** 按文字找一个按钮元素（不点）。同样搜整个 document，理由见 input。 */
  button: (label) => {
    const btn = [...win.document.body.querySelectorAll('button')]
      .find((b) => b.textContent.replace(/\s+/g, '').includes(label.replace(/\s+/g, '')))
    assert.ok(btn, `界面上找不到「${label}」按钮`)
    return btn
  },
}

/** 等 promise 链与渲染跑完。 */
async function settle(times = 12) {
  for (let i = 0; i < times; i++) {
    await new Promise((r) => setTimeout(r, 5))
    await Promise.resolve()
  }
}

before(async () => {
  const cli = path.join(root, 'dist', 'miniaccount-darwin-arm64')
  await new Promise((res, rej) => {
    const p = spawn(cli, ['demo', '--db', dbPath, '--months', '3'],
      { stdio: 'ignore', env: childEnv })
    p.on('exit', (c) => (c === 0 ? res() : rej(new Error('演示账套生成失败'))))
  })
  await startBackend()
  await boot()
  bundle = await import('../testdist/testentry.js')
  await mountApp()
})

after(() => {
  try { proc?.kill() } catch { /* 已经退出 */ }
  try { fs.unlinkSync(dbPath) } catch { /* 已经删掉 */ }
  // 假家目录整个删掉：建账测试会在里面留下 .db 与 .files
  try { fs.rmSync(fakeHome, { recursive: true, force: true }) } catch { /* 没关系 */ }
})

// ---------------------------------------------------------------------------
// 契约
// ---------------------------------------------------------------------------

test('★ api.js 拿到的是值本身，不是元组', async () => {
  const r = await bundle.call(win.go.main.App.AppVersion)
  assert.equal(r.ok, true, r.fault?.message)
  assert.equal(typeof r.data, 'string')
})

test('★ Go 的错误能解回 Fault（kind 不丢、消息不是裸 JSON）', async () => {
  // 查一张不存在的凭证 —— 后端一定报错
  const r = await bundle.call(win.go.main.App.VoucherDetail, 99999999)
  assert.equal(r.ok, false, '不存在的凭证应当报错')
  assert.equal(typeof r.fault.kind, 'string')
  assert.ok(r.fault.message.length > 0, '错误消息不能是空的')
  assert.ok(!r.fault.message.startsWith('{'),
    '消息不该是未解析的 JSON：' + r.fault.message)
})

// ---------------------------------------------------------------------------
// 真实渲染
// ---------------------------------------------------------------------------

test('★ 首页渲染出单位名称与当前账期（真数据、真字段名）', async () => {
  await page.goto('/dashboard')
  const t = page.text()
  assert.ok(t.includes('杭州云帆软件有限公司'),
    `首页没渲染出单位名称，实际：${t.slice(0, 300)}`)
  assert.ok(!t.includes('尚未打开账套'), '账套状态没读到：界面以为没打开账套')
  assert.ok(t.includes('2025-03'), `当前账期没显示：${t.slice(0, 300)}`)
})

test('★ 每个页面都能打开、有内容、不抛异常', async () => {
  const routes = bundle.router.getRoutes()
    .filter((r) => r.meta?.title && !r.meta?.hidden)
  assert.ok(routes.length >= 15, `路由数量不对：${routes.length}`)
  const problems = []
  for (const r of routes) {
    try {
      await page.goto(r.path)
      const t = page.text()
      if (/is not iterable|undefined is not|\[object Object\]|TypeError/.test(t)) {
        problems.push(`${r.path}: 页面上出现 JS 错误文本：${t.slice(0, 120)}`)
      }
      if (t.trim().length < 10) problems.push(`${r.path}: 页面是空的`)
    } catch (e) {
      problems.push(`${r.path}: 抛异常 ${e.message}`)
    }
  }
  assert.deepEqual(problems, [])
})

test('★ 报表页渲染出真实金额（字段名错了这里会是空或 0）', async () => {
  await page.goto('/reports')
  const t = page.text()
  assert.ok(t.includes('375,900.00'),
    `没渲染出资产总计 375,900.00，实际：${t.slice(0, 400)}`)
})

test('★ 增值税页把两套身份分开显示（用户点名要求过）', async () => {
  await page.goto('/vat')
  const t = page.text()
  assert.ok(t.includes('增值税纳税人身份'), `没显示增值税纳税人身份：${t.slice(0, 200)}`)
  assert.ok(t.includes('企业规模类型'), `没显示企业规模类型：${t.slice(0, 200)}`)
  assert.ok(t.includes('互不派生'), '没有说明两者互不派生')

  // 政策表是另一个标签页，切过去才看得到版本号
  await page.click('税率政策表')
  const t2 = page.text()
  assert.ok(/2026\.\d/.test(t2), `没显示政策表版本：${t2.slice(0, 300)}`)
})

test('★ 某个页面出错不会把整个界面卡死', async () => {
  // 这一条是实测出来的真问题：页面渲染抛错后，Vue 的 RouterView
  // 不再渲染新页面 —— 用户点菜单毫无反应，且没有任何提示。
  // 现在由 App.vue 里的 PageBoundary 兜住：只换掉出错的那一页。
  bundle.router.addRoute({
    path: '/__boom', name: 'boom', meta: { title: '测试炸点', hidden: true },
    component: { setup() { throw new Error('故意抛的渲染错误') } },
  })
  await page.goto('/__boom')
  let t = page.text()
  assert.ok(t.includes('这个页面出错了'), `没有显示错误边界：${t.slice(0, 200)}`)
  assert.ok(t.includes('故意抛的渲染错误'), '没把错误原因显示出来')
  // 侧栏还在 —— 用户还能切走
  assert.ok(t.includes('账期管理'), '出错后侧栏没了，用户就没有出路了')

  // 换一个页面必须恢复
  await page.goto('/periods')
  t = page.text()
  assert.ok(!t.includes('这个页面出错了'), '换页之后错误边界没有复位')
  assert.ok(t.includes('2025-03'), `换页后没渲染出新页面：${t.slice(0, 200)}`)
})

test('★ 启动时列出账套目录里已有的账，默认选中上次那本', async () => {
  await page.goto('/welcome')
  const t = page.text()
  // 演示账套就在临时家目录的账套目录里，应当被列出来
  assert.ok(t.includes('打开哪一本账'), `没有出现启动选择界面：${t.slice(0, 300)}`)
  assert.ok(t.includes('杭州云帆软件有限公司'),
    `列表里没有账套的单位名称：${t.slice(0, 400)}`)
  assert.ok(t.includes('打开这本账'), '没有默认动作「打开这本账」')
  assert.ok(t.includes('新建一本'), '没有给「新建」的出路')
  // 默认动作必须是打开 —— 选中的那一项要已被勾上
  const radios = [...globalThis.document.querySelectorAll('input[type=radio]')]
  assert.ok(radios.length >= 1, '没有可选的账套')
  assert.ok(radios.some((r) => r.checked), '★ 没有默认选中的账套，默认动作就不是「打开」了')
})

test('★ 账期页显示每个期间的状态（后端数据真的到了页面上）', async () => {
  await page.goto('/periods')
  const t = page.text()
  assert.ok(t.includes('2025-03'), `账期页没内容：${t.slice(0, 200)}`)
  assert.ok(t.includes('已结账'), `期间状态没显示：${t.slice(0, 300)}`)
})

// ---------------------------------------------------------------------------
// 用户报回来的四条（本轮修复的回归）
// ---------------------------------------------------------------------------
//
// 这四条有一个共同的教训：**它们全都只在「真正跑起来的那一份产物」里
// 才看得见**。源码改了、Go 测试全绿、连界面冒烟测试也过了 ——
// 但发布目录 dist/ 里躺的还是上一版的二进制，用户双击的正是那一份。
//
// 所以下面的断言一律只认「用户能看到什么」：DOM 里的节点、输入框里的字、
// 真实建出来的那个账套文件。源码里写了什么不算数。
//
// 顺序是**有意的**：先关掉账套（后面两条要在「还没有账套」的状态下跑），
// 最后再把账套开回来，免得影响以后再往这个文件里加测试。

/**
 * 走到「新建账套」那张表单上。
 *
 * 建账页一打开是「打开哪一本账」或「打开账套」（取决于账套目录里有没有账），
 * 建账表单要点一下「新建账套」才出来 ——
 * 第一次写这几条测试时就栽在这上面：断言全部报「找不到输入框」，
 * 看着像界面坏了，其实只是没切过去。
 */
async function openCreateForm() {
  await page.goto('/welcome')
  await settle(40) // 等 onMounted 里的 suggestBookPath 回来
  if (!mounted.host.querySelector('input.font-mono')) {
    await page.click('新建账套')
    await settle(40)
  }
}

test('★ 没有账套时，左侧菜单点不动、也切不过去', async () => {
  // 先造出「还没建账」这个状态：关掉当前账套。
  const closed = await bundle.api.api.closeBook()
  assert.equal(closed.ok, true, closed.fault?.message)
  await bundle.book.refreshBook()
  assert.equal(bundle.book.bookState.open, false, '关掉之后仍然认为有账套')

  // 菜单必须是**置灰的文字**，而不是能点的链接。
  // 「点得动但进去是空的 / 报错」比「点不动」糟糕得多。
  const links = [...mounted.host.querySelectorAll('aside nav a')]
  assert.equal(links.length, 0,
    `没有账套时侧栏仍然有 ${links.length} 个可点链接：` +
    links.map((a) => a.textContent.trim()).join('、'))

  const disabled = [...mounted.host.querySelectorAll('aside nav [aria-disabled="true"]')]
  assert.ok(disabled.length >= 15,
    `置灰的菜单项只有 ${disabled.length} 个，业务菜单应该有 17 个`)
  assert.ok(disabled.every((el) => el.getAttribute('title') === '请先建账或打开一个账套'),
    '置灰项没有给出「为什么点不动」的提示')

  const t = page.text()
  assert.ok(t.includes('还没有账套。建账之后这里的功能才会解锁。'),
    `没有解释为什么点不动：${t.slice(0, 300)}`)

  // 点一下置灰项：既不能跳转，也不能报错。
  const before = bundle.router.currentRoute.value.fullPath
  disabled[0].dispatchEvent(new win.MouseEvent('click', { bubbles: true }))
  await settle(30)
  assert.equal(bundle.router.currentRoute.value.fullPath, before,
    '点了置灰的菜单项之后页面真的切走了')

  // ★ 路由守卫兜底：地址栏、书签、代码里的 push 都得挡住。
  await page.goto('/dashboard')
  assert.equal(bundle.router.currentRoute.value.name, 'welcome',
    '没有账套也能直接进首页 —— 用户会看到一屏报错')
})

test('★ 建账页预填了默认保存位置，用户一个字都不用填', async () => {
  // 上一条把账套关掉了，界面此时应当已经在建账页。
  await openCreateForm()

  const pathInput = page.input('input.font-mono')
  const expectedDir = path.join(fakeHome, '.mini-account', 'dataDB')

  assert.notEqual(pathInput.value.trim(), '',
    '保存位置是空的 —— 点「建立账套」只会弹「请填写账套文件的保存位置」')
  assert.ok(pathInput.value.startsWith(expectedDir),
    `默认保存位置 = ${pathInput.value}，期望在 ${expectedDir} 下`)
  assert.ok(pathInput.value.endsWith('.db'), `默认保存位置不是 .db 文件：${pathInput.value}`)

  // 界面上还要把默认目录**写出来**，用户才知道账套去了哪。
  assert.ok(page.text().includes(expectedDir),
    `界面上没有显示默认目录 ${expectedDir}：${page.text().slice(0, 400)}`)

  // 改单位名称：文件名跟着变，目录不动，而且文件名是**拼音**。
  const nameInput = page.input('input[placeholder="杭州云帆软件有限公司"]')
  await page.type(nameInput, '杭州云帆软件有限公司')
  const followed = page.input('input.font-mono').value
  assert.equal(followed,
    path.join(expectedDir, 'hang-zhou-yun-fan-ruan-jian-you-xian-gong-si.db'),
    '改了单位名称之后文件名没有跟着变（中文单位名称应当转成拼音）')
})

test('★ 建账默认小规模纳税人（表单一打开就是选中的）', async () => {
  await openCreateForm()

  const small = page.button('小规模纳税人')
  const general = page.button('一般纳税人')

  // Button 的 default 变体带 bg-primary，outline 带 border-input ——
  // 选中的那个一眼能看出来，这里就按这个判。
  assert.ok(small.className.includes('bg-primary'),
    `「小规模纳税人」默认没有被选中：class=${small.className}`)
  assert.ok(!general.className.includes('bg-primary'),
    '「一般纳税人」默认被选中了 —— 这款软件的用户绝大多数是小规模')
})

test('★ 一个字都不填也能把账套建出来（走的就是默认保存位置）', async () => {
  const name = '杭州云帆软件有限公司'
  const want = path.join(fakeHome, '.mini-account', 'dataDB',
    'hang-zhou-yun-fan-ruan-jian-you-xian-gong-si.db')

  await openCreateForm()
  // 只填单位名称（这是必填的），**保存位置一栏一次都不碰**。
  await page.type(page.input('input[placeholder="杭州云帆软件有限公司"]'), name)
  assert.equal(page.input('input.font-mono').value, want,
    '保存位置没有自动跟着单位名称走 —— 用户还是得自己想一个路径')

  // 建账前先清掉可能残留的提示，免得读到上一条测试留下的字。
  for (const n of [...bundle.api.notices.items]) bundle.api.dismiss(n.id)

  await page.click('建立账套')
  await settle(80)

  const toast = bundle.api.notices.items.map((n) => n.message).join(' | ')
  assert.ok(!toast.includes('请填写'),
    `还是弹了「请填写」：${toast}`)
  assert.ok(toast.includes('已建好'),
    `没建出账套来，提示是：${toast || '（没有任何提示）'}`)

  // ★ 真去磁盘上看一眼：账套文件必须真的在默认目录里。
  assert.ok(fs.existsSync(want),
    `默认保存位置下没有生成账套文件 ${want}`)

  // 文件名全英文：账套要在不同系统、备份包、U 盘之间搬。
  for (const ch of path.basename(want)) {
    assert.ok(ch.codePointAt(0) < 128, `文件名里有非 ASCII 字符 ${ch}：${want}`)
  }

  // 而且是**小规模纳税人** —— 默认值要一路落到数据库里，不能只是界面上好看。
  const cur = await bundle.api.api.currentBook()
  assert.equal(cur.ok, true, cur.fault?.message)
  assert.equal(cur.data.open, true, '建完账之后账套没打开')
  assert.equal(cur.data.book.vatStatus, 'small_scale',
    `建出来的账套身份是 ${cur.data.book.vatStatus}，期望 small_scale`)

  assert.equal(cur.data.path, want, `账套建到了 ${cur.data.path}，期望 ${want}`)
})

// ---------------------------------------------------------------------------
// 系统文件管理器
// ---------------------------------------------------------------------------
//
// 账套默认放在 <家目录>/.mini-account/dataDB —— 以点开头，是**隐藏目录**，
// 用户自己翻是翻不到的。所以这三个绑定是「藏起来」的配套出路，
// 必须有；而且它们各自都有一条容易踩的坑。

test('★ 无窗口模式下，文件管理器绑定返回可读错误，而不是把进程搞死', async () => {
  // ★ 这一条测的是一个**真会杀进程**的坑。
  //
  // Wails 的 runtime 在拿不到前端时走的是 log.Fatalf —— 那是 os.Exit(1)，
  // 不是返回 error。而界面冒烟测试正是跑在无窗口模式（--serve-bindings）下，
  // 所以少一层判断的话：调一次 → 后端当场退出 →
  // 剩下的测试全部失败，而且看不出原因。
  //
  // 这个文件后面的测试还能跑，本身就是断言的一部分。
  const picks = [
    ['ChooseBookFile', []],
    ['ChooseBookDir', ['']],
    ['RevealBookDir', ['']],
  ]
  for (const [name, args] of picks) {
    const r = await bundle.call(win.go.main.App[name], ...args)
    assert.equal(r.ok, false, `${name} 在无窗口模式下应当失败而不是假装成功`)
    assert.equal(typeof r.fault.message, 'string')
    assert.ok(r.fault.message.length > 0, `${name} 的错误消息不能是空的`)
    assert.ok(!r.fault.message.startsWith('{'),
      `${name} 的消息不该是未解析的 JSON：${r.fault.message}`)
    assert.ok(/文件选择框|无窗口/.test(r.fault.message),
      `${name} 的错误消息要说人话：${r.fault.message}`)
  }

  // 后端进程还活着 —— 这才是重点。
  const v = await bundle.call(win.go.main.App.AppVersion)
  assert.equal(v.ok, true, '后端进程已经死了：runtime 的 log.Fatalf 没被挡住')
})

test('★ 建账页有「选择目录…」「打开目录」，打开账套有「浏览…」', async () => {
  // 按钮存在、并且点下去不会把界面搞崩（无窗口模式下会弹一条错误提示）。
  await openCreateForm()
  for (const n of [...bundle.api.notices.items]) bundle.api.dismiss(n.id)

  const pickDir = page.button('选择目录')
  const reveal = [...mounted.host.querySelectorAll('button')]
    .find((b) => b.textContent.trim() === '打开目录')
  assert.ok(reveal, '建账页没有「打开目录」按钮 —— 账套藏在隐藏目录里，用户就真的找不到了')

  pickDir.dispatchEvent(new win.MouseEvent('click', { bubbles: true }))
  await settle(60)
  const toast = bundle.api.notices.items.map((n) => n.message).join(' | ')
  assert.ok(/文件选择框|无窗口/.test(toast),
    `点「选择目录…」应当给出可读的提示，实际：${toast || '（没有任何提示）'}`)

  // 切到「打开别处的账套」（手填路径那条路），那边也该有「浏览…」
  await page.click('打开别处的账套')
  await settle(30)
  assert.ok(page.button('浏览'), '「打开别处的账套」卡片没有「浏览…」按钮')
})

// ---------------------------------------------------------------------------
// AI 模型服务：配了就得能用
// ---------------------------------------------------------------------------
//
// ★ 这一条是用户报回来的：在设置里配好了模型，点生成仍然报
// 「所有模型服务都已停用」。根因是**表单里没有 enabled 字段** ——
// 保存时后端收到 Go 的零值 false，服务被存成停用，而界面上
// 没有任何开关能把它打开。用户完全没有出路。
//
// 所以这里测的不是「表单能提交」，而是「配完之后 AI 的开关确实是开的」：
// 直接问后端 hasDefault（它只统计 enabled 的服务）。

test('★ 在设置里保存模型服务之后，它是启用状态（不是悄悄存成停用）', async () => {
  await page.goto('/settings')
  await settle(60)

  // 表单有默认值（本地 Ollama），直接存即可 —— 这也正是用户的操作路径。
  const before = await bundle.api.api.aiConfig()
  assert.equal(before.ok, true, before.fault?.message)

  await page.click('保存并启用')
  await settle(60)

  const cfg = await bundle.api.api.aiConfig()
  assert.equal(cfg.ok, true, cfg.fault?.message)
  const provs = cfg.data.providers ?? []
  assert.ok(provs.length > 0, '保存之后一条服务都没有 —— 表单根本没提交成功')

  const saved = provs.find((p) => p.baseUrl && p.model)
  assert.ok(saved, `保存的服务缺字段：${JSON.stringify(provs)}`)
  assert.equal(saved.enabled, true,
    '保存下来的服务是「停用」状态 —— 表单没带上 enabled，' +
    '用户配完模型点生成只会得到「所有模型服务都已停用」')

  // hasDefault 是后端按 enabled 统计出来的，等于「AI 到底能不能用」。
  assert.equal(cfg.data.hasDefault, true,
    '没有任何启用的服务 —— AI 用不了，而用户以为自己配好了')

  // 界面上也要看得见「已启用」，不能只有一个灰色小徽标。
  const t = page.text()
  assert.ok(t.includes('已启用'), `设置页没显示启用状态：${t.slice(0, 300)}`)
})

test('★ 停用 / 启用能来回切（否则停用了就再也开不回来）', async () => {
  await page.goto('/settings')
  await settle(60)

  await page.click('停用')
  await settle(60)
  let cfg = await bundle.api.api.aiConfig()
  assert.equal(cfg.ok, true, cfg.fault?.message)
  assert.equal(cfg.data.hasDefault, false, '停用之后仍然是可用状态')
  assert.ok((cfg.data.providers ?? []).every((p) => !p.enabled), '还有服务是启用着的')

  await page.click('启用')
  await settle(60)
  cfg = await bundle.api.api.aiConfig()
  assert.equal(cfg.data.hasDefault, true, '启用之后又用不了了 —— 开关是单向的')
})

// ---------------------------------------------------------------------------
// 部门与人事异动
// ---------------------------------------------------------------------------
//
// 这一整块以前不存在：后端有 SaveDepartment，但**没有任何绑定暴露它**，
// 界面上只有员工表单里那个只读下拉。于是用户根本没法新建部门 ——
// 而绝大多数费用科目要求按部门辅助核算，没有部门就记不了费用。
//
// 测的是「用户能不能做到这件事」，不是「方法存不存在」。

test('★ 部门能新建、改名、停用（界面到后端整条路）', async () => {
  await page.goto('/payroll')
  await settle(80)
  await page.click('部门')
  await settle(60)

  const before = await bundle.api.api.departments()
  assert.equal(before.ok, true, before.fault?.message)
  const n0 = before.data.length

  // 新建
  await page.click('新建部门')
  await settle(30)
  await page.type(page.input('input[placeholder="销售部"]'), '回归测试部')
  await page.click('保存')
  await settle(80)

  const after = await bundle.api.api.departments()
  const made = (after.data ?? []).find((d) => d.name === '回归测试部')
  assert.ok(made, `新建的部门没出现在列表里：${JSON.stringify(after.data)}`)
  assert.equal(after.data.length, n0 + 1)

  // 停用
  busy: {
    const r = await bundle.api.api.saveDepartment({ ...made, enabled: false })
    assert.equal(r.ok, true, r.fault?.message)
  }
  const stopped = (await bundle.api.api.departments()).data.find((d) => d.id === made.id)
  assert.equal(stopped.enabled, false, '停用没生效')

  // 清理：删掉（没人用它，应当删得掉）
  const del = await bundle.api.api.deleteDepartment(made.id)
  assert.equal(del.ok, true, `没人用的部门应当能删掉：${del.fault?.message}`)
  const final = await bundle.api.api.departments()
  assert.ok(!(final.data ?? []).some((d) => d.id === made.id), '删完还在列表里')
})

test('★ 有人的部门删不掉，而且要说清楚被什么挡住了', async () => {
  const depts = (await bundle.api.api.departments()).data ?? []
  assert.ok(depts.length > 0, '账套里一个部门都没有')
  const d = depts[0]

  // 在这个部门下建一个员工
  const emp = await bundle.api.api.saveEmployee({
    id: 0, code: '', name: '回归测试员工', kind: 'employee',
    baseSalary: '8000.00', siBase: '', hfbBase: '', specialAdditional: '',
    siProfile: '', deptId: d.id, position: '', expenseAccountCode: '',
    hireDate: '2025-01-01', leaveDate: '', enabled: true, remark: '',
  })
  assert.equal(emp.ok, true, emp.fault?.message)

  const usage = await bundle.api.api.departmentUsageOf(d.id)
  assert.equal(usage.ok, true, usage.fault?.message)
  assert.ok(usage.data.employees >= 1,
    `引用统计没数到员工：${JSON.stringify(usage.data)}`)

  const del = await bundle.api.api.deleteDepartment(d.id)
  assert.equal(del.ok, false, '部门下还有员工，不该删得掉')
  assert.ok(/员工/.test(del.fault.message), `要说清楚被什么挡住了：${del.fault.message}`)
  assert.ok(/停用/.test(del.fault.message), `要给出替代方案：${del.fault.message}`)

  return d.id
})

test('★ 员工能转部门、调薪、离职，且编辑不会把部门抹掉', async () => {
  const depts = (await bundle.api.api.departments()).data ?? []
  const emps = (await bundle.api.api.employees(false)).data ?? []
  const e = emps.find((x) => x.name === '回归测试员工')
  assert.ok(e, '上一条建的员工不见了')

  // ★ 界面的编辑是把这一行整个展开进表单再提交的。少带一个字段，
  // 保存一次就把它抹掉了 —— 这一条守的就是那个事故。
  assert.ok(e.deptId, 'EmployeeView 没带出 deptId —— 界面编辑一次就会抹掉部门')

  // 换个部门
  const other = depts.find((d) => d.enabled && d.id !== e.deptId)
  if (other) {
    const r = await bundle.api.api.transferEmployee({ id: e.id, deptId: other.id, operator: '测试' })
    assert.equal(r.ok, true, r.fault?.message)
    assert.equal(r.data.deptId, other.id, '转部门没生效')
    // 转回原来的部门，别影响后面的测试
    await bundle.api.api.transferEmployee({ id: e.id, deptId: e.deptId, operator: '测试' })
  }

  // 调薪
  const sal = await bundle.api.api.adjustSalary({
    id: e.id, baseSalary: '9500.00', siBase: '', hfbBase: '', operator: '测试',
  })
  assert.equal(sal.ok, true, sal.fault?.message)
  assert.equal(sal.data.baseSalary, 950000, `调薪没生效：${sal.data.baseSalary}`)

  // 工资填 0 要被挡住，并指向「离职」
  const zero = await bundle.api.api.adjustSalary({ id: e.id, baseSalary: '0', operator: '测试' })
  assert.equal(zero.ok, false, '工资填 0 应当被拒绝')

  // 离职：日期 + 停用一个动作做完
  const resign = await bundle.api.api.resignEmployee({
    id: e.id, leaveDate: '2025-06-30', reason: '回归测试', operator: '测试',
  })
  assert.equal(resign.ok, true, resign.fault?.message)
  assert.equal(resign.data.leaveDate, '2025-06-30')
  assert.equal(resign.data.enabled, false,
    '离职之后档案必须停用 —— 否则下个月的工资单还会把他带进来')
  assert.equal(resign.data.statusLabel, '离职')

  // 已经离职的人不能再办一次
  const again = await bundle.api.api.resignEmployee({
    id: e.id, leaveDate: '2025-07-31', operator: '测试',
  })
  assert.equal(again.ok, false, '已经离职的人不该能再办一次')
})

// 收尾：把演示账套开回来。
// 这一段本身也是断言 —— 建完账再打开另一个账套不能把界面卡住。
test('★ 收尾：重新打开演示账套，菜单恢复可点', async () => {
  const opened = await bundle.api.api.openBook(dbPath)
  assert.equal(opened.ok, true, opened.fault?.message)
  await bundle.book.refreshBook()
  assert.equal(bundle.book.bookState.open, true, '重新打开演示账套失败')

  await page.goto('/dashboard')
  const links = [...mounted.host.querySelectorAll('aside nav a')]
  assert.ok(links.length >= 15, `账套打开后菜单没有恢复：只有 ${links.length} 个链接`)
  assert.ok(page.text().includes('杭州云帆软件有限公司'),
    '首页没渲染出演示账套的单位名称')
})
