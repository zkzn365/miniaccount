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
  /**
   * 按可见文字**精确**点一个按钮。
   *
   * 标签页要用它：includes 匹配下「部门」会命中「新建部门」，
   * 而后者在别的标签页里，点不到 —— 测试会以「找不到按钮」这种
   * 看不出原因的形态失败。
   */
  clickExact: async (label) => {
    const norm = (x) => x.replace(/\s+/g, '')
    const btn = [...win.document.body.querySelectorAll('button')]
      .find((b) => norm(b.textContent) === norm(label))
    assert.ok(btn, `界面上找不到「${label}」按钮（精确匹配）`)
    btn.dispatchEvent(new win.MouseEvent('click', { bubbles: true }))
    await settle(20)
  },
  /** 按文字找一个按钮元素（不点）。同样搜整个 document，理由见 input。 */
  button: (label) => {
    const btn = [...win.document.body.querySelectorAll('button')]
      .find((b) => b.textContent.replace(/\s+/g, '').includes(label.replace(/\s+/g, '')))
    assert.ok(btn, `界面上找不到「${label}」按钮`)
    return btn
  },
}

/** nextPeriodLabel 给出某期间的下一个月，如 2026-01 → 2026-02。 */
function nextPeriodLabel(p) {
  const m = p.month === 12 ? 1 : p.month + 1
  const y = p.month === 12 ? p.year + 1 : p.year
  return `${y}-${String(m).padStart(2, '0')}`
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
// 账期管理：结账 / 反结账
// ---------------------------------------------------------------------------
//
// ★ 这一条是拿用户报回来的 bug 换来的：
//
//     会计期间 0000-00 非法
//
// 界面从 `preview.year` 取期间，而 `ClosingPreview` 里根本没有这两个
// 字段（只有 `period` 字符串）—— 取到 undefined，JSON.stringify 又把
// undefined 的键丢掉，Go 收到零值。于是**结账和反结账完全不可用**，
// 而且报错指向「0000-00」，看不出是「字段没传过去」。
//
// 这类 bug 的共同点是：按钮点得动、弹窗也正常，只有最后那一下才炸。
// 所以这里必须真的点下去，不能只看页面渲染出来了没有。

// ★ 凭证录完只是草稿；过账发生在账期结算。
//
// 这是用户点名要的规则：「不能做完凭证就过账，账期结算的时候才能过账」。
// 守它要守三处，缺一处这条规则就会慢慢漏掉：
//
//   1. 编辑器里**没有**「保存并记账」这个按钮；
//   2. 存完账上还是空的（明细账里查不到它）；
//   3. 结账之后它才进账，而且结账提示要报出「过了几张草稿」。
test('★ 凭证录完只是草稿，结账时才过账', async () => {
  // 1. 编辑器里不该有直接过账的按钮
  await page.goto('/vouchers')
  await settle(80)
  await page.click('录入凭证')
  await settle(60)
  const labels = [...win.document.body.querySelectorAll('button')]
    .map((b) => b.textContent.replace(/\s+/g, ''))
  assert.ok(!labels.some((t) => t.includes('保存并记账')),
    `★ 凭证编辑器里还有「保存并记账」—— 做完凭证就能过账，规则就破了。实际按钮：${labels}`)
  assert.ok(labels.some((t) => t.includes('保存草稿')),
    `编辑器里找不到「保存草稿」：${labels}`)
  await page.click('取消')
  await settle(30)

  // 界面这一层也拿不到「保存并过账」/「单张过账」的口子
  assert.equal(bundle.api.api.saveAndPost, undefined,
    '★ api.saveAndPost 还在 —— 界面上随时能把一张刚敲完的凭证记进总账')
  assert.equal(bundle.api.api.postVoucher, undefined,
    '★ api.postVoucher 还在 —— 凭证列表里又能逐张过账了')

  // 2. 在一个**还开着**的期间里录一张凭证
  const per = await bundle.api.api.periods()
  const openOne = (per.data.periods ?? []).find((p) => p.status === 'open')
  assert.ok(openOne, '没有可记账的期间')
  const day = `${openOne.year}-${String(openOne.month).padStart(2, '0')}-15`

  // 科目：只用不要求辅助核算的可记账明细科目，别把这条测试
  // 变成「辅助核算配没配对」的测试
  const opts = (await bundle.api.api.accountOptions()).data ?? []
  const free = opts.filter((a) => !(a.auxTypes ?? []).length)
  const debitAcc = free.find((a) => a.direction === '借')
  const creditAcc = free.find((a) => a.direction === '贷')
  assert.ok(debitAcc && creditAcc && debitAcc.code !== creditAcc.code,
    `找不到两个不要求辅助核算的科目：${JSON.stringify(free.slice(0, 6))}`)

  const saved = await bundle.api.api.saveVoucher({
    id: 0, word: '记', date: day, remark: '草稿规则回归测试', createdBy: '李会计',
    lines: [
      { accountCode: debitAcc.code, summary: '草稿规则回归测试', debitYuan: '100.00', creditYuan: '' },
      { accountCode: creditAcc.code, summary: '草稿规则回归测试', debitYuan: '', creditYuan: '100.00' },
    ],
  })
  assert.equal(saved.ok, true, saved.fault?.message)
  assert.equal(saved.data.status, 'draft', `存下来应当是草稿，实际 ${saved.data.status}`)
  assert.equal(saved.data.no, '', `★ 草稿不该有凭证号，实际 ${saved.data.no}`)

  // ★ 账上必须还是空的 —— 这一条是整条规则的核心
  const led0 = await bundle.api.api.ledgerDetail({ accountPrefix: debitAcc.code })
  assert.equal(led0.ok, true, led0.fault?.message)
  assert.equal((led0.data.rows ?? []).length, 0,
    `★ 还没结算，明细账里就有这张凭证了：${JSON.stringify(led0.data.rows?.slice(0, 2))}`)

  // 页面上它显示的是草稿
  await page.goto('/vouchers')
  await settle(80)
  assert.ok(page.text().includes('草稿'), `凭证列表里没标出草稿：${page.text().slice(0, 200)}`)

  // 3. 结账 —— 过账就在这一步
  await page.goto('/periods')
  await settle(80)
  await page.type(page.input('input[placeholder="结账 / 反结账时签名用"]'), '回归测试员')
  await settle(30)
  for (const n of [...bundle.api.notices.items]) bundle.api.dismiss(n.id)
  await page.click('结账')
  await settle(150)

  // 预览要把「结账会先过账几张草稿」说在前面
  const pv = await bundle.api.api.previewClose({ year: openOne.year, month: openOne.month })
  assert.equal(pv.ok, true, pv.fault?.message)
  assert.ok(pv.data.draftCount >= 1,
    `预览没报出待过账的草稿张数：${JSON.stringify({ draftCount: pv.data.draftCount })}`)

  await page.click('确认结账')
  await settle(200)
  const toast = bundle.api.notices.items.map((n) => n.message).join(' | ')
  assert.ok(/过账本期 \d+ 张草稿/.test(toast),
    `结账提示没说过账了几张草稿：${toast || '（没有提示）'}`)

  // ★ 结算之后它才进账
  const led1 = await bundle.api.api.ledgerDetail({ accountPrefix: debitAcc.code })
  assert.equal((led1.data.rows ?? []).length, 1,
    `★ 结账之后明细账里应当有这 1 行，实际 ${(led1.data.rows ?? []).length} 行`)
  const detail = await bundle.api.api.voucherDetail(saved.data.id)
  assert.equal(detail.data.status, 'posted', '结算后凭证应当是已过账')
  assert.ok(detail.data.no, '结算后应当分配到凭证号')

  // 收尾：反结账回来，别影响后面的测试
  await page.goto('/periods')
  await settle(80)
  await page.click('反结账')
  await settle(120)
  await page.click('确认反结账')
  await settle(150)
})

// ★ 固定资产与费用摊销：登记 → 计提 → 草稿凭证。
//
// 这两件事是唯一两笔「什么业务都没发生、但每月必须记账」的分录。
// 这一条守三件事：
//
//   1. 固定资产**次月**起提、待摊项目**当月**就摊（两条规则不一样，
//      而且都不能记反）；
//   2. 计提出来的凭证是**草稿**（与手工凭证同一条规则，不进总账）；
//   3. 计提预览里，金额为 0 的必须给出原因 —— 算成 0 却不说话，
//      比算错还难查。
test('★ 固定资产次月起提、待摊当月就摊，计提出来是草稿', async () => {
  await page.goto('/assets')
  await settle(80)

  // 用**第一个**已启用期间：这样次月也是已启用的，
  // 「次月起提」才验证得了（用最后一个的话，次月还没启用，提不了）
  const per = await bundle.api.api.periods()
  const opens = (per.data.periods ?? []).filter((p) => p.status === 'open')
  assert.ok(opens.length >= 2, `已启用期间不足两个：${opens.length}`)
  const first = opens[0]
  const next = opens[1]
  assert.equal(nextPeriodLabel(first), next.label, '已启用期间不连续，这条测试的前提不成立')
  const day = `${first.year}-${String(first.month).padStart(2, '0')}-15`

  // ---- 1. 登记一张固定资产（走界面）----
  await page.click('登记固定资产')
  await settle(60)
  await page.type(page.input('input[placeholder="笔记本电脑"]'), '回归测试电脑')
  await page.type(page.input('input[placeholder="12000.00"]'), '12000')
  await page.type(page.input('input[placeholder="36"]'), '36')
  await page.type(page.input('input[placeholder="2025-01-10"]'), day)
  // 部门：折旧费用科目按部门辅助核算，必须选一个。
  // ★ 按 option 的 value 挑（数字 id），不能按「value 非空」——
  // 「（不挂部门）」那一项的 value 在浏览器里回落到它的文字，非空但不是部门。
  const deptSel = [...win.document.body.querySelectorAll('select')]
    .find((sel) => [...sel.options].some((o) => /^\d+$/.test(o.value)))
  assert.ok(deptSel, '固定资产表单里没有部门下拉 —— 折旧挂不上部门，计提那天会被拒绝')
  const deptOpt = [...deptSel.options].find((o) => /^\d+$/.test(o.value))
  deptSel.value = deptOpt.value
  deptSel.dispatchEvent(new win.Event('change', { bubbles: true }))
  await settle(30)
  await page.click('保存')
  await settle(150)

  const after = await bundle.api.api.assets()
  assert.equal(after.ok, true, after.fault?.message)
  const made = (after.data.assets ?? []).find((a) => a.name === '回归测试电脑')
  assert.ok(made, `登记的固定资产没出现在档案里。提示：${
    bundle.api.notices.items.map((n) => n.message).join(' | ') || '（无）'}`)
  // 12,000 × (1 − 5%) ÷ 36 = 316.666… → 316.67
  assert.equal(made.monthlyAmount, 31667, `月折旧额 = ${made.monthlyAmount}，期望 31667 分`)
  assert.equal(made.firstPeriod, next.label,
    `★ 投入使用 ${day}，起提期间应是次月 ${next.label}，实际 ${made.firstPeriod}`)
  assert.ok(page.text().includes('回归测试电脑'),
    `登记的资产没渲染到表格里：${page.text().slice(0, 300)}`)

  // ---- 2. 待摊项目：受益期从当月起算 ----
  await page.clickExact('费用摊销')
  await settle(60)
  await page.click('新增待摊项目')
  await settle(60)
  await page.type(page.input('input[placeholder="一年期房租 / 办公室装修"]'), '回归测试待摊')
  await page.type(page.input('input[placeholder="60000.00"]'), '6000')
  await page.type(page.input('input[placeholder="12"]'), '6')
  await page.type(page.input('input[placeholder="2025-01-01"]'), day)
  const deptSel2 = [...win.document.body.querySelectorAll('select')]
    .find((sel) => [...sel.options].some((o) => /^\d+$/.test(o.value)))
  if (deptSel2) {
    const o = [...deptSel2.options].find((x) => /^\d+$/.test(x.value))
    deptSel2.value = o.value
    deptSel2.dispatchEvent(new win.Event('change', { bubbles: true }))
    await settle(30)
  }
  await page.click('保存')
  await settle(150)

  const am = await bundle.api.api.assets()
  const madeAm = (am.data.amortizations ?? []).find((m) => m.name === '回归测试待摊')
  assert.ok(madeAm, `登记的待摊项目没出现：${JSON.stringify(am.data.amortizations)}`)
  assert.equal(madeAm.monthlyAmount, 100000, `月摊销额 = ${madeAm.monthlyAmount}，期望 100000 分`)
  assert.equal(madeAm.firstPeriod, first.label,
    `★ 待摊项目受益期从**当月**起算，起摊期间应是 ${first.label}，实际 ${madeAm.firstPeriod}`)

  // ---- 3. 计提预览：0 的必须说原因，有的要算对 ----
  const pv = await bundle.api.api.previewAccrual({ year: first.year, month: first.month })
  assert.equal(pv.ok, true, pv.fault?.message)
  const assetRow = (pv.data.rows ?? []).find((r) => r.name === '回归测试电脑')
  assert.ok(assetRow, '预览里没有那张固定资产')
  assert.equal(assetRow.amount, 0, `★ 投入使用当月不该计提，实际 ${assetRow.amount}`)
  assert.ok(/次月起提/.test(assetRow.reason || ''),
    `★ 算成 0 却不说原因（比算错还难查）：${JSON.stringify(assetRow)}`)
  const amRow = (pv.data.rows ?? []).find((r) => r.name === '回归测试待摊')
  assert.ok(amRow, '预览里没有那个待摊项目')
  assert.equal(amRow.amount, 100000, `★ 待摊当月就该摊第一期，实际 ${amRow.amount}`)

  await page.goto('/assets')
  await settle(80)
  await page.click('预览本期计提')
  await settle(150)
  // ★ 断言要落在**弹窗里那一行**上，不能用整页文本：
  // 「次月起提」这几个字在保存成功的提示里也有，整页搜索会假绿。
  const rows = [...win.document.body.querySelectorAll('tbody tr')]
  const row = rows.find((tr) => tr.textContent.includes('回归测试电脑'))
  assert.ok(row, `预览弹窗里没有那张固定资产那一行：${page.text().slice(0, 400)}`)
  assert.ok(/次月起提/.test(row.textContent),
    `★ 金额算成 0 却没在界面上说原因（比算错还难查）：${row.textContent}`)
  await page.click('取消')
  await settle(30)

  // ---- 4. 当月计提：只有摊销那一张 ----
  await page.type(page.input('input[placeholder="操作人"]'), '回归测试员')
  await settle(30)
  for (const n of [...bundle.api.notices.items]) bundle.api.dismiss(n.id)
  const acc1 = await bundle.api.api.accrue({ year: first.year, month: first.month, by: '回归测试员' })
  assert.equal(acc1.ok, true, acc1.fault?.message)
  assert.ok(acc1.data.amortizationVoucherId > 0, `应生成摊销凭证：${JSON.stringify(acc1.data)}`)
  assert.equal(acc1.data.depreciationVoucherId, 0,
    '★ 固定资产当月不提，不该生成折旧凭证')

  const v = await bundle.api.api.voucherDetail(acc1.data.amortizationVoucherId)
  assert.equal(v.data.status, 'draft', `★ 计提凭证应当是草稿，实际 ${v.data.status}`)
  assert.equal(v.data.no, '', `★ 草稿不该有凭证号，实际 ${v.data.no}`)
  assert.equal(v.data.source, 'amortization', `凭证来源 = ${v.data.source}`)
  // ★ 没结算就还没进账
  const led = await bundle.api.api.ledgerDetail({ accountPrefix: '1801' })
  assert.equal((led.data.rows ?? []).length, 0,
    `★ 还没结算，长期待摊费用就有数了：${JSON.stringify(led.data.rows?.slice(0, 2))}`)

  // 同一期不能再提一次
  const again = await bundle.api.api.accrue({ year: first.year, month: first.month, by: '回归测试员' })
  assert.equal(again.ok, false, '★ 同一期不该能提两次 —— 重复提会让费用凭空多一块，而且不报错')
  assert.ok(/已经(计提|摊销)过/.test(again.fault?.message ?? ''),
    `重复计提的错误要说清楚是重复：${again.fault?.message}`)

  // ---- 5. 次月：这回固定资产提上了 ----
  const acc2 = await bundle.api.api.accrue({ year: next.year, month: next.month, by: '回归测试员' })
  assert.equal(acc2.ok, true, acc2.fault?.message)
  assert.ok(acc2.data.depreciationVoucherId > 0,
    '★ 次月应当生成折旧凭证（当月增加当月不提，次月起提）')
  const dep = await bundle.api.api.voucherDetail(acc2.data.depreciationVoucherId)
  assert.equal(dep.data.lines.length, 2, `折旧凭证应当一借一贷：${JSON.stringify(dep.data.lines)}`)
  assert.equal(dep.data.lines[0].accountCode, '560205', '借方应是管理费用—折旧费')
  assert.equal(dep.data.lines[1].accountCode, '1602', '贷方应是累计折旧')
  assert.ok(/管理/.test(dep.data.lines[0].auxDesc || ''),
    `★ 折旧凭证的借方缺部门辅助核算（凭证上显示的是「部门#1」也说明名字没解析）：${dep.data.lines[0].auxDesc}`)

  // 首页待办要把「待过账」和「结账时过账」说清楚（别让人满界面找过账按钮）
  await page.goto('/dashboard')
  await settle(80)
  const t = page.text()
  assert.ok(/张凭证待过账/.test(t), `首页没提示待过账：${t.slice(0, 300)}`)
  assert.ok(/结账/.test(t), `首页该说清楚过账发生在结账：${t.slice(0, 300)}`)
})

// ★ 凭证录入的列序：摘要在前、科目在后。
//
// 这是记账凭证的惯例列序（摘要 → 科目 → 借方 → 贷方），
// 也是全程序其他表格的列序 —— 只有凭证编辑器和凭证详情是反的。
// 反着排的后果是录入时手指先落在科目上：写下「买打印机」之前
// 得先想好记哪个科目，而实际记账时人是先想到事、再想科目。
test('★ 凭证录入：摘要排第一列、科目第二列', async () => {
  await page.goto('/vouchers')
  await settle(80)
  await page.click('录入凭证')
  await settle(80)

  const tables = [...win.document.body.querySelectorAll('table')]
  const t = tables.find((tb) => tb.querySelector('input[placeholder="本行摘要"]'))
  assert.ok(t, '凭证编辑器里没找到分录表')

  const heads = [...t.querySelectorAll('thead th')].map((th) => th.textContent.trim())
  assert.deepEqual(heads.slice(0, 4), ['#', '摘要', '科目', '辅助核算'],
    `★ 列序不对，实际：${JSON.stringify(heads)}`)

  // 表体也要跟着换 —— 只改表头会变成「标题与内容错位」，比原来更糟
  const row = t.querySelector('tbody tr')
  assert.ok(row, '分录表没有行')
  const cells = [...row.children]
  assert.ok(cells[1].querySelector('input[placeholder="本行摘要"]'),
    `★ 第 2 列应当是摘要输入框，实际是：${cells[1].textContent.trim()}`)
  assert.ok(/选择科目/.test(cells[2].textContent),
    `★ 第 3 列应当是科目，实际是：${cells[2].textContent.trim()}`)

  await page.click('取消')
  await settle(30)
})

// 凭证详情（只读）用同一套列序，否则「录的时候摘要在前、
// 查的时候科目前面」，看着像两张不同的表
test('★ 凭证详情的列序与录入一致', async () => {
  const list = await bundle.api.api.vouchers({ year: 0, month: 0 })
  assert.equal(list.ok, true, list.fault?.message)
  const one = (list.data ?? [])[0]
  if (!one) {
    // 当前账套一张凭证都没有时，先建一张再查
    const d = await bundle.api.api.saveVoucher({
      id: 0, word: '记', date: '2026-01-20', remark: '列序回归测试', createdBy: '李会计',
      lines: [
        { accountCode: '1001', summary: '列序回归测试', debitYuan: '1.00', creditYuan: '' },
        { accountCode: '5001', summary: '列序回归测试', debitYuan: '', creditYuan: '1.00' },
      ],
    })
    assert.equal(d.ok, true, d.fault?.message)
  }

  const after = await bundle.api.api.vouchers({ year: 0, month: 0 })
  const v = (after.data ?? [])[0]
  await page.goto('/vouchers')
  await settle(80)
  await bundle.api.api.voucherDetail(v.id) // 预热：确保详情能取到
  const btn = [...win.document.body.querySelectorAll('button')]
    .find((b) => (b.getAttribute('title') || '') === '查看详情')
  assert.ok(btn, '凭证列表里没有「查看详情」按钮')
  btn.dispatchEvent(new win.MouseEvent('click', { bubbles: true }))
  await settle(150)

  const tables = [...win.document.body.querySelectorAll('table')]
  const t = tables.find((tb) => {
    const h = [...tb.querySelectorAll('thead th')].map((x) => x.textContent.trim())
    return h.includes('辅助核算') && h.includes('借方') && h.includes('贷方')
  })
  assert.ok(t, '凭证详情里没找到分录表')
  const heads = [...t.querySelectorAll('thead th')].map((th) => th.textContent.trim())
  assert.deepEqual(heads.slice(0, 4), ['#', '摘要', '科目', '辅助核算'],
    `★ 凭证详情的列序与录入不一致，实际：${JSON.stringify(heads)}`)
})

// ★ AI 记账助手：只有一个「业务描述」输入框。
//
// 原来有业务类型、金额、日期、对方户名、资金方向五样，外加一个批量记账卡片。
// 那五样是**会计要问的问题**，不是用户想说的话 —— 逼用户先想清楚
// 「这算银行流水还是自然语言」「资金方向是支出还是收入」，
// 等于让他自己先当一遍会计。
//
// 改成对话之后，缺什么由会计问。这条测试守的就是「别又加回来」。
test('★ AI 记账助手只剩一个业务描述框，其余交给会计问', async () => {
  await page.goto('/ai')
  await settle(100)

  const t = page.text()
  for (const gone of ['业务类型', '资金方向', '对方户名', '批量记账']) {
    assert.ok(!t.includes(gone),
      `★ 「${gone}」还在 —— 那是会计要问的问题，不该让用户先填：${t.slice(0, 400)}`)
  }

  // 输入框：只有**一个** textarea（业务描述），不是五个字段
  const areas = [...win.document.body.querySelectorAll('textarea')]
  assert.equal(areas.length, 1,
    `页面上应当只有一个输入框（业务描述），实际 ${areas.length} 个`)
  assert.ok(/例：/.test(areas[0].getAttribute('placeholder') || ''),
    `那个框应当是业务描述：${areas[0].getAttribute('placeholder')}`)

  // 页面上该有的：操作人（采纳时签章）与发送
  const btns = [...win.document.body.querySelectorAll('button')]
    .map((b) => b.textContent.replace(/\s+/g, ''))
  assert.ok(btns.some((x) => x.includes('发送')), `找不到发送按钮：${btns}`)
  // 「换一笔」只在聊起来之后才出现（还没说话时没什么可换的）
  assert.ok(!btns.some((x) => x.includes('换一笔')),
    `还没说话就出现了「换一笔」：${btns}`)

  // 硬边界要写在页面上（会计不能写账这件事，用户得知道）
  assert.ok(/通不过护栏|不能写账/.test(t), `页面上没写清会计的能力边界：${t.slice(0, 400)}`)

  // 批量记账删干净了：页面上没有，绑定层也没有
  assert.ok(!t.includes('批量记账'), `页面上还留着批量记账：${t.slice(0, 400)}`)
  const dts = fs.readFileSync(path.join(here, '../wailsjs/go/main/App.d.ts'), 'utf8')
  for (const gone of ['StartAIAgentRun', 'AIAgentRunStatus', 'LatestAIAgentRun',
    'CancelAIAgentRun', 'AcceptAIAgentItem', 'RejectAIAgentItem', 'AIAgentSources']) {
    assert.ok(!dts.includes(gone),
      `★ 绑定 ${gone} 还在 —— 批量记账应当端到端删干净`)
  }
})

// ★ 执业要求（提示词）留在页面上：这是唯一能改「这位会计怎么记账」的地方
test('★ AI 记账助手能改这位会计的执业要求', async () => {
  await page.goto('/ai')
  await settle(100)
  const t = page.text()
  assert.ok(t.includes('执业要求'), `页面上没有执业要求一块：${t.slice(0, 300)}`)

  await page.click('执业要求')
  await settle(80)
  const box = [...win.document.body.querySelectorAll('textarea')]
    .find((a) => (a.value || '').length > 50)
  assert.ok(box, '执业要求展开后没有可编辑的文本框')
  assert.ok(box.value.length > 50,
    `编辑框里应当是当前生效的全文，实际只有 ${box.value.length} 字`)

  for (const label of ['保存', '预览实际提示词', '恢复出厂默认']) {
    assert.ok([...win.document.body.querySelectorAll('button')]
      .some((b) => b.textContent.replace(/\s+/g, '').includes(label)),
    `找不到「${label}」按钮`)
  }

  // ★ 预览必须是**会计版**的提示词 —— 里面要有一节「与用户对话」。
  // 渲染成单次任务版的话，用户看到的少一节，而那一节恰好是
  // 「什么时候该问」。
  const pv = await bundle.api.api.previewAccountantPrompt()
  assert.equal(pv.ok, true, pv.fault?.message)
  assert.ok(pv.data.system.includes('与用户对话'),
    '★ 预览出来的不是会计版提示词 —— 少了「与用户对话」那一节')
  assert.ok(pv.data.system.includes('ask_user'),
    '会计版提示词里该讲清楚用哪个工具问')
  assert.ok(pv.data.system.includes('只能使用下面列出的科目编码'),
    '会计版丢了硬边界（科目闭集）—— 预览与实际用的必须是同一份')
})

// 没有配置模型时，要给一句**能照着做**的提示，而不是静默失败
test('★ 没配模型时，会计给出可操作的提示', async () => {
  const r = await bundle.api.api.accountantSend({ sessionId: '', text: '昨天买了台打印机' })
  assert.equal(r.ok, false, '没配模型时不该假装成功')
  const msg = r.fault?.message ?? ''
  assert.ok(/模型|设置/.test(msg),
    `提示要指向「去哪儿配」：${msg}`)
  assert.ok(!/undefined|null|panic/i.test(msg), `不要漏出内部错误：${msg}`)
})

test('★ 结账真的能结掉（不是只把弹窗打开）', async () => {
  await page.goto('/periods')
  await settle(80)

  // 结账要签名（记账凭证需要有记账签章），先把操作人填上。
  // 这一步是测试自己发现的：不填的话 doClose 直接 warn 返回，
  // 断言只会看到「期间还是 open」，看不出是卡在哪。
  await page.type(page.input('input[placeholder="结账 / 反结账时签名用"]'), '回归测试员')
  await settle(30)

  const before = await bundle.api.api.periods()
  assert.equal(before.ok, true, before.fault?.message)
  const openOne = (before.data.periods ?? []).find((p) => p.status === 'open')
  assert.ok(openOne, `演示账套里没有可结账的期间：${JSON.stringify(before.data.periods)}`)

  // 打开结账弹窗（这一步原来就是好的，问题在下一步）
  await page.click('结账')
  await settle(120)

  // ★ 弹窗里必须带着「结哪个期间」。缺了它，请求里的 year/month 就是
  // undefined，Go 侧收到 0 —— 报的就是「会计期间 0000-00 非法」。
  const preview = await bundle.api.api.previewClose({ year: openOne.year, month: openOne.month })
  assert.equal(preview.ok, true, preview.fault?.message)
  assert.equal(preview.data.year, openOne.year,
    'ClosingPreview 没带 year —— 界面从它取值就会拿到 undefined')
  assert.equal(preview.data.month, openOne.month, 'ClosingPreview 没带 month')

  // 真点「确认结账」
  await page.click('确认结账')
  await settle(150)

  const after = await bundle.api.api.periods()
  const same = (after.data.periods ?? []).find(
    (p) => p.year === openOne.year && p.month === openOne.month)
  const toast = bundle.api.notices.items.map((n) => n.message).join(' | ')
  assert.equal(same.status, 'closed',
    `结账没生效，${openOne.label} 还是 ${same.status}。提示：${toast || '（没有提示）'}` +
    '\n若提示是「0000-00 非法」，说明请求里的 year/month 没传过去')

  // 收尾：反结账回来
  await page.goto('/periods')
  await settle(80)
  await page.click('反结账')
  await settle(120)
  await page.click('确认反结账')
  await settle(150)
  const back = await bundle.api.api.periods()
  const reopened = (back.data.periods ?? []).find(
    (p) => p.year === openOne.year && p.month === openOne.month)
  assert.equal(reopened.status, 'open',
    `反结账没生效，${openOne.label} 还是 ${reopened.status}`)
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

/** 辅助核算页的一个标签页。★ 部门与员工已经**从工资页迁到这里** ——
 *  它们的用途是辅助核算（记费用、记往来时挑一个），工资页只关心工资怎么算。 */
async function openAux(tabName) {
  await page.goto('/auxiliary')
  await settle(80)
  await page.clickExact(tabName)
  await settle(60)
}

test('★ 部门能新建、改名、停用（界面到后端整条路）', async () => {
  await openAux('部门')

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
  // 不只是接口里有 —— 表格里也要真出现那一行
  assert.ok(page.text().includes('回归测试部'),
    `新建的部门没渲染到表格里，页面上是：${page.text().slice(0, 400)}`)

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

test('★ 辅助核算页四个维度都在，且各自读的是自己的档案', async () => {
  // 用户要的是「辅助核算设置，这里面需要有客户、供应商、
  // 本公司的部门和员工（需要迁移过来）」—— 四个缺一个，
  // 那个维度在录凭证时就是空下拉，分录会被「缺少必需的辅助核算」拒绝。
  //
  // ★ 这一条跑在「建账」测试之后，当前账套是刚建出来的**空**账套，
  // 所以断言取「表头 或 空态」，两者都说明这一页真的渲染出来了。
  // 「有数据时表里真的出现那一行」由前面两条测试用真数据守。
  await page.goto('/auxiliary')
  await settle(80)

  const wants = [
    { tab: '客户', btn: '新建客户', marks: ['名称', '还没有客户'] },
    { tab: '供应商', btn: '新建供应商', marks: ['税号', '还没有供应商'] },
    { tab: '部门', btn: '新建部门', marks: ['上级', '还没有部门'] },
    { tab: '员工', btn: '新增员工', marks: ['标准月工资', '还没有员工'] },
  ]
  for (const w of wants) {
    await page.clickExact(w.tab)
    await settle(60)
    const t = page.text()
    assert.ok(t.includes(w.btn),
      `「${w.tab}」这一页没有「${w.btn}」按钮 —— 这一维建不了档案。实际：${t.slice(0, 300)}`)
    assert.ok(w.marks.some((m) => t.includes(m)),
      `「${w.tab}」这一页既没有表头也没有空态，说明根本没渲染：${t.slice(0, 400)}`)
  }

  // 四个绑定都要通（标签页画出来了不等于数据接上了）
  const bound = [
    ['客户', await bundle.api.api.contacts('customer')],
    ['供应商', await bundle.api.api.contacts('supplier')],
    ['部门', await bundle.api.api.departments()],
    ['员工', await bundle.api.api.employees(false)],
  ]
  for (const [what, r] of bound) {
    assert.equal(r.ok, true, `${what} 的绑定报错：${r.fault?.message}`)
    assert.ok(Array.isArray(r.data), `${what} 返回的不是数组：${JSON.stringify(r.data)}`)
  }
})

test('★ 工资页不再维护部门与员工档案（已迁到辅助核算）', async () => {
  await page.goto('/payroll')
  await settle(80)
  const t = page.text()
  assert.ok(!t.includes('新建部门'),
    `工资页还留着「新建部门」—— 同一份档案两处维护，改一处漏一处。实际：${t.slice(0, 300)}`)
  assert.ok(!t.includes('新增员工'),
    `工资页还留着「新增员工」—— 档案维护应当只在辅助核算页。实际：${t.slice(0, 300)}`)
  // 工资单本身还在
  assert.ok(t.includes('工资单'), `工资页把工资单也删掉了：${t.slice(0, 300)}`)
})

test('★ 往来单位（客户 / 供应商）能在界面上建、停用、删', async () => {
  // 这一条守的是「档案管理」这一半：原来只有 ContactOptions，
  // 一个只列启用中的下拉 —— 新建不了，停用之后再也看不到、也就改不回来。
  await openAux('客户')

  const before = await bundle.api.api.contacts('')
  assert.equal(before.ok, true, before.fault?.message)
  const n0 = before.data.length

  await page.click('新建客户')
  await settle(30)
  await page.type(page.input('input[placeholder="杭州云帆科技有限公司"]'), '回归测试客户')
  await page.click('保存')
  await settle(120)

  const after = await bundle.api.api.contacts('')
  const made = (after.data ?? []).find((c) => c.name === '回归测试客户')
  assert.ok(made, `新建的客户没出现在列表里：${JSON.stringify(after.data)}`)
  assert.equal(after.data.length, n0 + 1)
  assert.equal(made.kind, 'customer', `在「客户」页新建的，类型却是 ${made.kind}`)
  if (made.enabled !== undefined) assert.equal(made.enabled, true, '新建出来就是停用的')
  assert.ok(page.text().includes('回归测试客户'),
    `新建的客户没渲染到表格里，页面上是：${page.text().slice(0, 400)}`)

  // 停用之后**仍然看得见**（能改回来），这是与旧下拉的根本区别
  const off = await bundle.api.api.saveContact({ ...made, enabled: false })
  assert.equal(off.ok, true, off.fault?.message)
  const still = (await bundle.api.api.contacts('')).data.find((c) => c.id === made.id)
  assert.ok(still, '停用之后档案直接消失了 —— 用户再也改不回来')
  assert.equal(still.enabled, false, '停用没生效')

  // 没被引用，应当删得掉
  const del = await bundle.api.api.deleteContact(made.id)
  assert.equal(del.ok, true, `没人用的客户应当能删掉：${del.fault?.message}`)
  const final = await bundle.api.api.contacts('')
  assert.ok(!(final.data ?? []).some((c) => c.id === made.id), '删完还在列表里')
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

// ---------------------------------------------------------------------------
// 审计底稿：界面 → 绑定 → 领域 整条路
// ---------------------------------------------------------------------------
//
// 这一条守的是全账套最容易被做错的一处口径：
//
//	**生成凭证 ≠ 已入账。**
//
// 凭证录完只落草稿，过账只在账期结算 —— 调整凭证也一样。
// 所以「生成调整凭证」之后，审定数里**仍然**要加着这笔调整、
// 未更正错报里也**仍然**要列着它。若界面把「已生成凭证」当成「已入账」，
// 用户会看到错报凭空消失、审定数少了一块 —— 而账其实一点没改。
test('★ 审计底稿：三个门槛算得对，生成凭证不等于已入账', async () => {
  await page.goto('/workpaper')
  await settle(80)

  const per = await bundle.api.api.periods()
  // 用最后**一期已启用**的期间。
  //
  // ★ 不能用未启用的期间：调整凭证同样是凭证，往一个没开的月份里
  // 塞草稿会被 `期间未启用` 拦下（这条规则本身是对的 ——
  // 没开的月份结账时不会再跑，那张草稿会永远躺在账套里没人看见）。
  // 底稿可以按任意期间编，但「生成凭证」这一步要求期间是开的。
  const opens = (per.data.periods ?? []).filter((p) => p.status === 'open')
  assert.ok(opens.length > 0, '演示账套没有已启用的期间')
  const target = opens[opens.length - 1]
  const y = target.year
  const m = String(target.month).padStart(2, '0')

  // 期间下拉在**第一个 select**（页面上只有这一个 select 在卡片外）
  const perSel = [...win.document.body.querySelectorAll('select')][0]
  perSel.value = target.label
  perSel.dispatchEvent(new win.Event('change', { bubbles: true }))
  await settle(120)

  // ---- 1. 确定重要性水平（走弹窗）----
  await page.click('确定重要性')
  await settle(60)
  await page.type(page.input('input[placeholder="10000000.00"]'), '10000000')
  await page.type(page.input('input[placeholder="0.5"]'), '0.5')
  await page.type(page.input('input[placeholder="60"]'), '60')
  await page.type(page.input('input[placeholder="5"]'), '5')
  await page.click('保存')
  await settle(200)

  let text = page.text()
  // 10,000,000 × 0.5% = 50,000；× 60% = 30,000；× 5% = 2,500
  for (const want of ['整体重要性', '实际执行重要性', '明显微小错报临界值',
    '50,000.00', '30,000.00', '2,500.00']) {
    assert.ok(text.includes(want),
      `底稿上没有「${want}」—— 重要性水平没算出来或没渲染：${text.slice(0, 400)}`)
  }
  const wp0 = await bundle.api.api.workpaper({ year: y, month: target.month })
  assert.equal(wp0.ok, true, wp0.fault?.message)
  assert.equal(wp0.data.materiality.overall, 5000000, '整体重要性应当是 50,000.00 元')
  assert.equal(wp0.data.materiality.performance, 3000000, '实际执行重要性应当是 30,000.00 元')
  assert.equal(wp0.data.materiality.trivial, 250000, '明显微小错报临界值应当是 2,500.00 元')

  // ---- 2. 登记一笔调整（走弹窗）----
  await page.click('登记调整')
  await settle(60)
  await page.type(page.input('input[placeholder="补提 2025 年折旧"]'), '补提折旧（回归测试）')
  const reasonBox = [...win.document.body.querySelectorAll('textarea')]
    .find((a) => /折旧计算表/.test(a.getAttribute('placeholder') || ''))
  assert.ok(reasonBox, '调整弹窗里没有「调整依据」输入框 —— 依据是底稿的柱子')
  await page.type(reasonBox, '折旧计算表显示少提 3,000.00')

  // 两行分录：560205 借 3000 / 1602 贷 3000
  // ★ 科目下拉要按**行**取，不能按「哪个下拉里有这个科目」找：
  // 两行的科目下拉选项完全一样，用 find(code) 永远命中第一行 ——
  // 于是第二行一直是空的，提交时报「第 2 行没有科目」，
  // 而错误信息看起来像是服务端的问题。
  const acctSelects = () => [...win.document.body.querySelectorAll('select')]
    .filter((sel) => [...sel.options].some((o) => o.value === '560205'))
  const pick = async (row, code) => {
    const sel = acctSelects()[row]
    assert.ok(sel, `分录第 ${row + 1} 行的科目下拉找不到`)
    sel.value = code
    sel.dispatchEvent(new win.Event('change', { bubbles: true }))
    await settle(60)
  }
  await pick(0, '560205')
  await pick(1, '1602')

  // 560205 要部门辅助核算：界面上必须出现部门下拉，而且必须填 ——
  // 这一条与手工凭证完全一致（少了它服务端会打回「缺少必需的辅助核算」）
  // ★ 部门下拉要排除科目下拉：科目编码（1001、560205）也全是数字，
  // 按「有数字选项」找会命中第一行的科目下拉 —— 于是测试自己把
  // 第一行的科目改成了「库存现金」，而报错看起来像服务端的问题。
  const accts = acctSelects()
  const auxSel = [...win.document.body.querySelectorAll('select')]
    .filter((s) => !accts.includes(s))
    .find((s) => [...s.options].some((o) => /^\d+$/.test(o.value)))
  assert.ok(auxSel, '★ 560205 要求部门辅助核算，界面上却没给出部门下拉')
  auxSel.value = [...auxSel.options].find((o) => /^\d+$/.test(o.value)).value
  auxSel.dispatchEvent(new win.Event('change', { bubbles: true }))
  await settle(30)

  const amounts = [...win.document.body.querySelectorAll('input[placeholder="0.00"]')]
  assert.equal(amounts.length, 4, `两行应当有 4 个金额框，实际 ${amounts.length}`)
  await page.type(amounts[0], '3000') // 第 1 行借方
  await page.type(amounts[3], '3000') // 第 2 行贷方
  // ★ 断言必须看 document.body：Modal 用 Teleport 挂在 body 上，
  // 在宿主节点**外面** —— 只看 page.text()（宿主）永远读不到弹窗里的字
  const modalText = win.document.body.textContent.replace(/\s+/g, ' ')
  assert.ok(modalText.includes('借贷平衡'),
    `借贷相等时应当显示「借贷平衡」：${modalText.slice(0, 300)}`)

  await page.click('保存调整')
  await settle(200)

  const wp1 = await bundle.api.api.workpaper({ year: y, month: target.month })
  const adj = (wp1.data.adjustments ?? [])[0]
  assert.ok(adj, `登记的调整没落进底稿：${
    bundle.api.notices.items.map((n) => n.message).join(' | ') || '（无提示）'}`)
  assert.equal(adj.amount, 300000, `调整金额 = ${adj.amount}，期望 300000 分`)
  assert.equal(adj.booked, false, '刚登记的调整不该有凭证')
  assert.equal(adj.stateLabel, '未入账（仅登记在底稿）', `状态文字 = ${adj.stateLabel}`)
  assert.equal(wp1.data.misstatements.total, 300000, '未更正错报合计应当含这 3,000.00')
  assert.ok(page.text().includes('补提折旧（回归测试）'),
    `登记的调整没渲染出来：${page.text().slice(0, 400)}`)
  // 审定表里也要看得到它（560205 的调整借方）
  const row = (wp1.data.worksheet ?? []).find((r) => r.accountCode === '560205')
  assert.ok(row, '审定表里没有 560205')
  assert.equal(row.adjustDebit, 300000,
    `审定表里 560205 的调整借方应当是 3,000.00，实际 ${JSON.stringify(row)}` +
    `（调整 = ${JSON.stringify(adj)}）`)

  // ---- 3. 生成调整凭证：底稿口径必须**一个字都不变** ----
  //
  // 这一步在界面上要点确认框，jsdom 里拿不到；而且它生成的是草稿凭证，
  // 属于「写」的动作。这里直接调绑定，界面侧的渲染在下一段验。
  const posted = await bundle.api.api.postAdjustment({ id: adj.id, by: '回归测试员' })
  assert.equal(posted.ok, true, posted.fault?.message)

  // ★ 要先离开再回来：路由到**同一个**路径时 Vue 不会重建组件，
  // 于是页面上还挂着改之前的数据 —— 断言「界面显示草稿」会假红
  await page.goto('/dashboard')
  await page.goto('/workpaper')
  await settle(150)
  const wp2 = await bundle.api.api.workpaper({ year: y, month: target.month })
  const adj2 = (wp2.data.adjustments ?? [])[0]
  assert.equal(adj2.booked, true, '生成凭证后 booked 应当为真')
  assert.equal(adj2.posted, false,
    '★ 生成的只是草稿 —— posted 必须为假，过账只在账期结算')
  assert.ok(/草稿/.test(adj2.stateLabel),
    `状态文字要点明还是草稿，实际「${adj2.stateLabel}」`)
  assert.equal(wp2.data.misstatements.total, 300000,
    '★ 草稿没进账，这笔仍是未更正错报 —— 合计不能变')
  const row2 = (wp2.data.worksheet ?? []).find((r) => r.accountCode === '560205')
  assert.equal(row2.adjustDebit, 300000,
    '★ 凭证没过账，审定数里仍然要加着这笔调整')
  assert.ok(page.text().includes('草稿'),
    `界面上要说清「已生成凭证（草稿）」：${page.text().slice(0, 400)}`)

  // 那张凭证本身是草稿，来源是审计调整
  const v = await bundle.api.api.voucherDetail(adj2.voucherId)
  assert.equal(v.ok, true, v.fault?.message)
  assert.equal(v.data.status, 'draft', '调整凭证必须是草稿')
  assert.equal(v.data.source, 'audit', `凭证来源 = ${v.data.source}，期望 audit`)
})

// ---------------------------------------------------------------------------
// 审计证据链：结论 → 依据 → 原件还在不在
// ---------------------------------------------------------------------------
//
// 这一条守的是证据链上最要紧的一件事：
//
//	**底稿上写着「依据：折旧计算表」，而那份文件早就不在了。**
//
// 这比一开始就没写依据更糟 —— 它提供了一个假的确定感。
// 所以「原件还在不在」必须是每次打开这一页现查的，
// 而不是挂上那一刻拍下的一个标记。
test('★ 审计证据链：挂依据、判重、原件丢了要说出来', async () => {
  await page.goto('/dashboard')
  await page.goto('/workpaper')
  await settle(120)

  const per = await bundle.api.api.periods()
  const opens = (per.data.periods ?? []).filter((p) => p.status === 'open')
  assert.ok(opens.length > 0, '演示账套没有已启用的期间')
  const target = opens[opens.length - 1]
  const sel = [...win.document.body.querySelectorAll('select')][0]
  sel.value = target.label
  sel.dispatchEvent(new win.Event('change', { bubbles: true }))
  await settle(120)

  // 定重要性水平（若上一轮没定）
  let w = await bundle.api.api.workpaper({ year: target.year, month: target.month })
  assert.equal(w.ok, true, w.fault?.message)
  if (!w.data.materiality) {
    const m = await bundle.api.api.saveMateriality({
      year: target.year, month: target.month, benchmark: 'assets',
      benchmarkAmountYuan: '10000000', ratePpm: 5000,
      performancePpm: 600000, trivialPpm: 50000, note: '回归测试',
    })
    assert.equal(m.ok, true, m.fault?.message)
  }

  // 登记一笔调整（走接口、不走弹窗：弹窗那条路上一轮已经验过了）
  const dept = (await bundle.api.api.departments()).data?.find((d) => d.enabled)
  assert.ok(dept, '演示账套没有启用中的部门')
  const adj = await bundle.api.api.saveAdjustment({
    id: 0, year: target.year, month: target.month, code: '', kind: 'adjust',
    summary: '证据链回归测试', reason: '核对折旧计算表后发现少提',
    evidence: '', operator: '回归测试员',
    lines: [
      { accountCode: '560205', summary: '补提', debitYuan: '100.00', creditYuan: '', deptId: dept.id },
      { accountCode: '1602', summary: '补提', debitYuan: '', creditYuan: '100.00' },
    ],
  })
  assert.equal(adj.ok, true, adj.fault?.message)
  const adjID = (adj.data.adjustments ?? []).find((a) => a.summary === '证据链回归测试')?.id
  assert.ok(adjID, `调整没登记上：${JSON.stringify(adj.data.adjustments)}`)

  // 挂一份附件（走接口：弹窗里的文件选择框在 jsdom 里点不出文件）
  const content = Buffer.from('资产,本期折旧\n电脑,1000\n').toString('base64')
  const added = await bundle.api.api.addEvidence({
    ownerType: 'adjustment', ownerId: adjID, refKind: 'attachment',
    refId: 0, refLabel: '', note: '表上少提 100.00',
    hash: '', fileName: '折旧计算表.csv', dataBase64: content, by: '回归测试员',
  })
  assert.equal(added.ok, true, added.fault?.message)

  const ev = await bundle.api.api.evidence({ year: target.year, month: target.month })
  assert.equal(ev.ok, true, ev.fault?.message)
  const chain = (ev.data.chains ?? []).find((c) => c.ownerType === 'adjustment' && c.ownerId === adjID)
  assert.ok(chain, '证据链里没有那笔调整')
  assert.equal(chain.total, 1, `应当有 1 份依据，实际 ${chain.total}`)
  const link = chain.links[0]
  assert.equal(link.hasFile, true, '附件类依据应当带文件')
  assert.equal(link.missing, false, '文件刚上传，不该报丢失')
  assert.ok(link.hash && link.path, '应当给出 hash 与磁盘路径')

  // 同一份文件再挂一次：不许算两份依据
  const dup = await bundle.api.api.addEvidence({
    ownerType: 'adjustment', ownerId: adjID, refKind: 'attachment',
    refId: 0, refLabel: '', note: '', hash: link.hash, fileName: '折旧计算表.csv',
    dataBase64: '', by: '回归测试员',
  })
  assert.equal(dup.ok, false, '同一份资料挂两次应当被拦下')
  assert.ok(/已经挂在这个结论下/.test(dup.fault?.message ?? ''),
    `报错要说清是重复：${dup.fault?.message}`)

  // 界面上要能看到它
  await page.goto('/dashboard')
  await page.goto('/workpaper')
  await settle(200)
  let body = win.document.body.textContent.replace(/\s+/g, ' ')
  assert.ok(body.includes('审计证据链'), `工作底稿页没有证据链一块：${body.slice(0, 300)}`)
  assert.ok(body.includes('折旧计算表.csv'),
    `挂上的依据没渲染出来。提示：${
      bundle.api.notices.items.map((n) => n.message).join(' | ') || '（无）'}` +
    ` 后端数据：${JSON.stringify(chain)}`)
  assert.ok(body.includes('证据链回归测试'), '证据链里应当能看到那笔调整')

  // ---- 原件不见了：必须报出来 ----
  const filePath = path.join(path.dirname(dbPath), '.files', link.hash.slice(0, 2), link.hash)
  assert.ok(fs.existsSync(filePath), `附件实体不在预期位置：${filePath}`)
  fs.unlinkSync(filePath)

  const ev2 = await bundle.api.api.evidence({ year: target.year, month: target.month })
  const chain2 = (ev2.data.chains ?? []).find((c) => c.ownerType === 'adjustment' && c.ownerId === adjID)
  assert.equal(chain2.links[0].missing, true,
    '★ 文件实体已经删掉，必须报「原件已找不到」—— 否则底稿会提供假的确定感')
  assert.equal(ev2.data.broken, 1, `broken 应当是 1，实际 ${ev2.data.broken}`)
  assert.ok(/找不到/.test(ev2.data.concludes),
    `结论要说清是原件丢了：${ev2.data.concludes}`)
  assert.ok(!/还没有附依据/.test(ev2.data.chains
    .find((c) => c.ownerType === 'adjustment' && c.ownerId === adjID).concludes),
    '★ 证据丢了不能说成「没有证据」—— 补的方式完全不同')

  // 界面上也要说得出「原件已找不到」
  await page.goto('/dashboard')
  await page.goto('/workpaper')
  await settle(200)
  body = win.document.body.textContent.replace(/\s+/g, ' ')
  assert.ok(body.includes('原件已找不到'),
    `界面上没标出原件丢了：${body.slice(0, 400)}`)

  // 收尾：把这笔测试调整删掉，别影响别的用例
  const del = await bundle.api.api.deleteAdjustment(adjID)
  assert.equal(del.ok, true, del.fault?.message)
})

// ---------------------------------------------------------------------------
// 税务计算表：三张表的口径与「不可直接申报」
// ---------------------------------------------------------------------------
//
// 这一条守的是三张表各自最容易做错的口径：
//
//	增值税      专栏取数 + 留抵从上期滚过来
//	企业所得税  **累计口径**（按本季算会系统性少缴）+ 纳税调整必须人工填
//	个人所得税  累计预扣（读工资单固化的累计字段）
//
// 以及一条硬边界：算出来的东西**不能直接用于申报**。
test('★ 税务计算表：三张表算得出数，且标明不可直接申报', async () => {
  await page.goto('/dashboard')
  await page.goto('/taxreturn')
  await settle(150)

  const per = await bundle.api.api.periods()
  const opens = (per.data.periods ?? []).filter((p) => p.status === 'open')
  assert.ok(opens.length > 0, '演示账套没有已启用的期间')
  const target = opens[opens.length - 1]
  const sel = [...win.document.body.querySelectorAll('select')][0]
  sel.value = target.label
  sel.dispatchEvent(new win.Event('change', { bubbles: true }))
  await settle(150)

  // ---- 1. 增值税 ----
  const vat = await bundle.api.api.taxReturn({ kind: 'vat', year: target.year, month: target.month })
  assert.equal(vat.ok, true, vat.fault?.message)
  assert.equal(vat.data.submittable, false,
    '★ 税务计算表不能被标记为可直接申报')
  assert.ok(vat.data.rows.length > 0, '增值税表没有行')
  for (const r of vat.data.rows) {
    assert.ok((r.source || '').trim().length > 0,
      `第 ${r.line} 行「${r.label}」没有数据来源 —— 会计没法拿它去核对`)
  }
  assert.ok(vat.data.keys.length > 0, '增值税表没有关键数')
  assert.ok(vat.data.identities.some((i) => /纳税人身份/.test(i.label)),
    '增值税表要写清用的纳税人身份')
  // 附加税费是否减半取决于身份，所以身份必须在表上
  const smallScale = (vat.data.identities.find((i) => /纳税人身份/.test(i.label)) || {}).value
  assert.ok(/一般纳税人|小规模纳税人|未填写/.test(smallScale), `身份文字不对：${smallScale}`)

  // 界面上要看得见这些数
  let body = win.document.body.textContent.replace(/\s+/g, ' ')
  assert.ok(body.includes('不可直接申报'), `页面上没有写明不可直接申报：${body.slice(0, 300)}`)
  assert.ok(body.includes('数据来源'), `页面上没有「数据来源」一列：${body.slice(0, 300)}`)
  const needAmount = (vat.data.keys[0].amount / 100).toLocaleString('zh-CN', {
    minimumFractionDigits: 2, maximumFractionDigits: 2,
  })
  assert.ok(body.includes(needAmount),
    `页面没渲染出关键数 ${needAmount}：${body.slice(0, 400)}`)

  // ---- 2. 企业所得税：累计口径 + 纳税调整人工填 ----
  const cit1 = await bundle.api.api.taxReturn({
    kind: 'cit', year: target.year, month: target.month,
  })
  assert.equal(cit1.ok, true, cit1.fault?.message)
  assert.ok(cit1.data.inputFields.length >= 3,
    '企业所得税要给出纳税调整等输入项 —— 这几项账上算不出来')

  // 填一笔纳税调整增加额：应纳所得税额必须按**适用税率**跟着变
  //
  // ★ 不能写死 25%：账套是企业规模为小型/微型时会按 5% 实际税负算
  // （这正是「超过 300 万即不符合小微条件」那条规则的入口）。
  // 从表上的「适用税率」行读出比例再算 —— 顺便验证了那一行确实写着税率。
  const rateRow = cit1.data.rows.find((r) => r.line === '8')
  assert.ok(rateRow, `企业所得税表里应当有「适用税率」一行：${JSON.stringify(cit1.data.rows)}`)
  const m = /(\d+(?:\.\d+)?)%/.exec(rateRow.label)
  assert.ok(m, `「适用税率」行里要写清百分比：${rateRow.label}`)
  const ratePpm = Math.round(Number(m[1]) * 10000) // 百分比 → 百万分比

  const before = cit1.data.keys.find((k) => k.label === '应纳所得税额').amount
  const cit2 = await bundle.api.api.taxReturn({
    kind: 'cit', year: target.year, month: target.month,
    taxAdjustIncreaseYuan: '10000',
  })
  assert.equal(cit2.ok, true, cit2.fault?.message)
  const after = cit2.data.keys.find((k) => k.label === '应纳所得税额').amount
  const want = Math.round(1000000 * ratePpm / 1000000) // 10,000 元 → 分，乘税率
  assert.equal(after - before, want,
    `纳税调整增加 10,000 应让应纳所得税额增加 ${want / 100}（税率 ${m[1]}%），实际差 ${(after - before) / 100}`)

  // 在界面上切到企业所得税标签页：输入框与「优惠口径」都要在
  await page.click('企业所得税')
  await settle(150)
  body = win.document.body.textContent.replace(/\s+/g, ' ')
  assert.ok(body.includes('纳税调整增加额'), `企业所得税页没有纳税调整输入：${body.slice(0, 400)}`)
  assert.ok(/小型微利企业/.test(body), '页面上要让用户确认优惠口径')
  assert.ok(/累计|年初/.test(body), `企业所得税是累计口径，页面上要说清：${body.slice(0, 400)}`)

  // ---- 3. 个人所得税 ----
  await page.click('个人所得税')
  await settle(150)
  const iit = await bundle.api.api.taxReturn({ kind: 'iit', year: target.year, month: target.month })
  assert.equal(iit.ok, true, iit.fault?.message)
  body = win.document.body.textContent.replace(/\s+/g, ' ')
  assert.ok(body.includes('个人所得税'), `个税页没渲染：${body.slice(0, 300)}`)
  if (iit.data.rows.length === 0) {
    // 演示账套没有工资单也该给出一句能照着做的话
    assert.ok(/工资/.test(body), `没有工资单时要告诉用户去哪儿生成：${body.slice(0, 400)}`)
  } else {
    assert.ok(body.includes('累计'), `个税是累计预扣口径，页面上要说清：${body.slice(0, 400)}`)
  }
})

// ---------------------------------------------------------------------------
// 审计与鉴证文书：事实来自账套，意见由人选
// ---------------------------------------------------------------------------
//
// 这一条守两件事：
//
//  1. 文书里的**事实**来自账套与底稿（不是抄模板填空）；
//  2. 缺什么就明明白白列出来，而且**全文最前面印「请勿签发」**——
//     一份看起来已经写好了的草稿被拿去盖章，比打印不出来更糟。
test('★ 审计文书：三份草稿都标明不可直接出具，待补事项印在最前面', async () => {
  await page.goto('/dashboard')
  await page.goto('/auditdoc')
  await settle(150)

  const per = await bundle.api.api.periods()
  const opens = (per.data.periods ?? []).filter((p) => p.status === 'open')
  assert.ok(opens.length > 0, '演示账套没有已启用的期间')
  const target = opens[opens.length - 1]
  const sel = [...win.document.body.querySelectorAll('select')][0]
  sel.value = target.label
  sel.dispatchEvent(new win.Event('change', { bubbles: true }))
  await settle(150)

  // ---- 1. 审计报告：事实来自账套 ----
  const audit = await bundle.api.api.auditDoc({
    kind: 'audit', year: target.year, month: target.month,
    opinion: 'unqualified', firmName: '回归测试会计师事务所',
    reportNo: '测会审字〔2025〕第 1 号', cpa1: '张三', cpa2: '李四',
    reportDate: '2026-03-31',
  })
  assert.equal(audit.ok, true, audit.fault?.message)
  assert.equal(audit.data.draft, true, '★ 软件产出的必须是草稿')
  assert.equal(audit.data.submittable, false, '★ 不能标记为可直接出具')
  assert.ok(audit.data.sections.length >= 4, '审计报告至少要有意见、基础、管理层责任、注册会计师责任四段')
  for (const s of audit.data.sections) {
    assert.ok((s.source || '').trim().length > 0,
      `段落「${s.title}」没有写事实来源 —— 报告里的事实要能追到账套或底稿`)
  }
  assert.ok(/两名注册会计师/.test(audit.data.signature),
    '生效条件要写清签字要求')
  // 报表数字真的来自账套：主要项目段不能是 0
  const main = audit.data.sections.find((s) => /主要项目/.test(s.title))
  assert.ok(main, '审计报告里没有「已审财务报表主要项目」一段')
  assert.ok(!/资产总额 0\.00/.test(main.body),
    `★ 资产总额取数为 0 —— 报表取数断了：${main.body}`)

  // 缺什么要列出来（演示账套的底稿大概率没有证据链/重要性水平）
  if (!audit.data.canIssue) {
    assert.ok(audit.data.missing.length > 0, '不能签发的稿子必须列出待补事项')
    assert.ok(/请勿签发/.test(audit.data.fullText),
      '★ 有待补事项时，全文最前面必须印「请勿签发」')
  }

  // ---- 2. 意见类型必须由人选 ----
  const ops = await bundle.api.api.auditOpinions()
  assert.equal(ops.ok, true, ops.fault?.message)
  assert.equal(ops.data.length, 4, '四种意见都要可选')
  for (const o of ops.data) {
    assert.ok(o.label && o.hint, `意见 ${o.value} 缺少中文名或适用说明`)
  }
  // 否定意见必须带出「形成否定意见的基础」一段
  const adverse = await bundle.api.api.auditDoc({
    kind: 'audit', year: target.year, month: target.month,
    opinion: 'adverse', firmName: '某所', reportNo: '测会审字〔2025〕第 2 号',
    cpa1: '张三', cpa2: '李四', reportDate: '2026-03-31',
  })
  assert.equal(adverse.ok, true, adverse.fault?.message)
  assert.ok(adverse.data.sections.some((s) => /形成否定意见的基础/.test(s.title)),
    '★ 非无保留意见必须有「形成……的基础」一段')
  const adverseBody = adverse.data.sections.find((s) => s.title === '审计意见').body
  assert.ok(/未能公允反映/.test(adverseBody),
    `否定意见要明说「未能公允反映」：${adverseBody}`)
  assert.ok(!/公允反映了/.test(adverseBody),
    `★ 否定意见里出现了无保留意见的措辞「公允反映了」—— 措辞错了结论就变了：${adverseBody}`)

  // ---- 3. 管理建议书：发现来自体检与底稿 ----
  const mgmt = await bundle.api.api.auditDoc({
    kind: 'management', year: target.year, month: target.month,
    firmName: '回归测试会计师事务所', reportNo: '测会建字〔2025〕第 1 号',
    reportDate: '2026-03-31',
  })
  assert.equal(mgmt.ok, true, mgmt.fault?.message)
  const advice = mgmt.data.sections.find((s) => /发现的问题与建议/.test(s.title))
  assert.ok(advice, '管理建议书要有「发现的问题与建议」一段')
  assert.ok(/不构成对财务报表的审计意见/.test(
    mgmt.data.sections.find((s) => s.title === '说明').body),
    '建议书要写清它不构成审计意见')

  // ---- 4. 界面上要看得见 ----
  await page.goto('/dashboard')
  await page.goto('/auditdoc')
  await settle(200)
  const body = win.document.body.textContent.replace(/\s+/g, ' ')
  assert.ok(body.includes('审计报告'), `文书页没渲染：${body.slice(0, 300)}`)
  assert.ok(body.includes('注册会计师'), '页面上要有签字信息一块')
  assert.ok(/草稿|待签字盖章|还不能签发/.test(body),
    `页面上要说清这是草稿：${body.slice(0, 400)}`)
  // 文号是我们刚填的 → 说明表单值真的传下去了（跨页面重进后从输入框回填）
  assert.ok(body.includes('审计意见') || body.includes('形成'), '页面上要能看到正文段落')
})

// ---------------------------------------------------------------------------
// 税务申报台账：报没报、报了多少、账后来改了没有
// ---------------------------------------------------------------------------
//
// 这一条守的是台账存在的理由：
//
//	**申报是发生过的事实，计算表是派生结果。**
//
// 所以：同一期同一税种只能有一条有效记录；更正要先作废（留痕）；
// 而申报之后账又改了，「当时报了多少」与「现在算出多少」的差必须看得见。
test('★ 申报台账：登记、重复拦截、作废重报、与计算表对得上', async () => {
  await page.goto('/dashboard')
  await page.goto('/taxreturn')
  await settle(150)

  const per = await bundle.api.api.periods()
  const opens = (per.data.periods ?? []).filter((p) => p.status === 'open')
  assert.ok(opens.length > 0, '演示账套没有已启用的期间')
  const target = opens[opens.length - 1]
  const sel = [...win.document.body.querySelectorAll('select')][0]
  sel.value = target.label
  sel.dispatchEvent(new win.Event('change', { bubbles: true }))
  await settle(150)

  // ---- 1. 报之前：计算表上要显示「还没登记」 ----
  const before = await bundle.api.api.taxReturn({
    kind: 'vat', year: target.year, month: target.month,
  })
  assert.equal(before.ok, true, before.fault?.message)
  assert.equal(before.data.filing, null,
    '还没登记时不该有申报记录（这条用例要求该期间尚未登记过）')
  assert.ok(/还没有登记申报记录/.test(before.data.filingHint),
    `计算表上要提示还没登记：${before.data.filingHint}`)

  // ---- 2. 登记：金额按当前计算表填（不用人抄） ----
  const filed = await bundle.api.api.saveTaxFiling({
    id: 0, kind: 'vat', year: target.year, month: target.month,
    status: 'filed', filedDate: nextMonthDay(target, 15), paidDate: '',
    payableYuan: '', taxAmountYuan: '', surchargeYuan: '', paidYuan: '',
    fromCurrentReturn: true, channel: '电子税务局', receiptNo: 'REG-1',
    note: '', operator: '回归测试员',
  })
  assert.equal(filed.ok, true, filed.fault?.message)
  const item = filed.data.items.find((it) => it.period === target.label && it.kind === 'vat')
  assert.ok(item, `台账里没有刚登记的记录：${JSON.stringify(filed.data.items)}`)
  assert.equal(item.payable, before.data.payable,
    '★ 金额应当按当前计算表填，不该让人手抄')
  assert.equal(item.diff, 0, `刚登记应当与计算表一致：${item.reconcile}`)

  // 计算表上要显示申报状态
  const after = await bundle.api.api.taxReturn({
    kind: 'vat', year: target.year, month: target.month,
  })
  assert.ok(after.data.filing, '★ 报完之后计算表上必须显示申报状态')
  assert.ok(/一致/.test(after.data.filingHint), `勾稽提示：${after.data.filingHint}`)

  // ---- 3. 同一期同一税种不能再登记一条 ----
  const dup = await bundle.api.api.saveTaxFiling({
    id: 0, kind: 'vat', year: target.year, month: target.month,
    status: 'filed', filedDate: nextMonthDay(target, 20), paidDate: '',
    payableYuan: '', taxAmountYuan: '', surchargeYuan: '', paidYuan: '',
    fromCurrentReturn: true, channel: '', receiptNo: '', note: '', operator: '回归测试员',
  })
  assert.equal(dup.ok, false, '同一属期同一税种登记两次应当被拦下')
  assert.ok(/已经登记过申报/.test(dup.fault?.message ?? ''),
    `报错要说清已经报过：${dup.fault?.message}`)

  // ---- 4. 申报日期不能早于属期月末 ----
  const voided0 = await bundle.api.api.saveTaxFiling({
    id: 0, kind: 'iit', year: target.year, month: target.month,
    status: 'filed', filedDate: `${target.label}-10`, paidDate: '',
    payableYuan: '', taxAmountYuan: '', surchargeYuan: '',
    fromCurrentReturn: true, channel: '', receiptNo: '', note: '', operator: '回归测试员',
  })
  assert.equal(voided0.ok, false, '申报日期早于属期月末应当被拦下')

  // ---- 5. 更正申报：作废（留痕）→ 重新登记 ----
  const bad = await bundle.api.api.voidTaxFiling({ id: item.id, by: '回归测试员', reason: '' })
  assert.equal(bad.ok, false, '作废不写原因应当被拦下')
  const voided = await bundle.api.api.voidTaxFiling({
    id: item.id, by: '回归测试员', reason: '应补税额填错，更正申报',
  })
  assert.equal(voided.ok, true, voided.fault?.message)
  assert.equal(voided.data.voided, 1, '作废数应当是 1')
  assert.equal(voided.data.effective, 0, '作废后不该还有有效记录')
  const refiled = await bundle.api.api.saveTaxFiling({
    id: 0, kind: 'vat', year: target.year, month: target.month,
    status: 'paid', filedDate: nextMonthDay(target, 20), paidDate: nextMonthDay(target, 22),
    payableYuan: '', taxAmountYuan: '', surchargeYuan: '', paidYuan: '',
    fromCurrentReturn: true, channel: '电子税务局', receiptNo: 'REG-2',
    note: '更正申报', operator: '回归测试员',
  })
  assert.equal(refiled.ok, true, refiled.fault?.message)
  assert.equal(refiled.data.effective, 1, '重报后应当有 1 条有效')
  assert.equal(refiled.data.voided, 1, '★ 作废记录必须留着（痕迹）')
  assert.ok(refiled.data.items.some((it) => it.status === 'void'),
    '台账里要能看到作废的那一条')

  // ---- 6. 界面上要看得见台账 ----
  await page.goto('/dashboard')
  await page.goto('/taxreturn')
  await settle(200)
  const body = win.document.body.textContent.replace(/\s+/g, ' ')
  assert.ok(body.includes('本年申报台账'), `页面上没有台账一块：${body.slice(0, 400)}`)
  assert.ok(body.includes(target.label), '台账里要能看到刚登记的属期')
  assert.ok(/已申报|已作废/.test(body), `台账里要能看到状态：${body.slice(0, 400)}`)
  assert.ok(/更正申报/.test(body), '页面上要写清更正的流程')
})

/** nextMonthDay 给出某期间**次月**的某一天（申报日期不能在属期内）。 */
function nextMonthDay(p, day) {
  const m = p.month === 12 ? 1 : p.month + 1
  const y = p.month === 12 ? p.year + 1 : p.year
  return `${y}-${String(m).padStart(2, '0')}-${String(day).padStart(2, '0')}`
}
