/**
 * 绑定层契约测试。
 *
 * # 为什么这个文件存在
 *
 * `lib/api.js` 曾经假定 Wails 的绑定返回 `[值, 错误]` 元组，
 * 而真实运行时 resolve 的是**值本身**、失败时是 **reject**。
 * 后果是每一次调用都出错：返回对象的绑定抛
 * `TypeError: {} is not iterable`，返回数组的绑定静默拿到错的元素。
 *
 * 而它一直没被发现，因为开发态的 mockApp 是照元组写的 ——
 * **假实现和真实现不是一套约定**，于是假实现掩盖的正是它该暴露的问题。
 *
 * 所以这里测的不是「函数能不能跑」，而是**契约本身**：
 *   1. Wails 的两种结局（resolve 值 / reject Fault 的 JSON）都要认；
 *   2. 非数组的值**不能**被解构（那正是当初崩掉的原因）；
 *   3. mockApp 必须与真实现同形状。
 *
 * 运行：cd desktop/frontend && node --test
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'

// api.js 在浏览器外会走「没有运行时」的分支，但 call/toFault 与 mockApp
// 都是纯函数/纯数据，可以直接测。
const { call, toFault, mockApp, notify, notices, dismiss } =
  await import('../src/lib/api.js')

test('绑定成功时 resolve 的是值本身，不是元组', async () => {
  const binding = async () => ({ companyName: '杭州云帆软件有限公司' })
  const r = await call(binding)
  assert.equal(r.ok, true)
  assert.deepEqual(r.data, { companyName: '杭州云帆软件有限公司' })
})

test('★ 返回对象不能被当成元组解构（这正是当初崩掉的那一行）', async () => {
  // 非数组、非字符串的值：旧写法 `const [data, fault] = value` 会抛
  // TypeError: {} is not iterable，于是每一次调用都变成「内部错误」。
  const r = await call(async () => ({ open: false }))
  assert.equal(r.ok, true, '普通对象必须原样作为 data 返回')
  assert.deepEqual(r.data, { open: false })
})

test('返回数组时拿到的是数组本身，不是它的前两个元素', async () => {
  const rows = [{ id: 1 }, { id: 2 }, { id: 3 }]
  const r = await call(async () => rows)
  assert.equal(r.ok, true)
  assert.equal(r.data.length, 3, '数组不能被解构成「第一个 + 第二个」')
})

test('返回字符串时拿到的是整串', async () => {
  const r = await call(async () => '2026-09-11')
  assert.equal(r.ok, true)
  assert.equal(r.data, '2026-09-11', '字符串解构会得到字符，必须避免')
})

test('Go 侧的 Fault 是 JSON 字符串，要解回对象', async () => {
  const binding = async () => {
    throw JSON.stringify({ kind: 'no_book', message: '尚未打开账套' })
  }
  const r = await call(binding)
  assert.equal(r.ok, false)
  assert.equal(r.fault.kind, 'no_book', 'kind 必须解出来：界面靠它决定跳建账向导')
  assert.equal(r.fault.message, '尚未打开账套')
})

test('Fault 里的 detail 也要带出来', async () => {
  const binding = async () => {
    throw JSON.stringify({ kind: 'blocked', message: '本期体检未通过', detail: '存在 2 张草稿凭证' })
  }
  const r = await call(binding)
  assert.equal(r.fault.detail, '存在 2 张草稿凭证')
})

test('不是 JSON 的错误字符串照原样当消息用', async () => {
  const r = await call(async () => { throw 'method not registered' })
  assert.equal(r.ok, false)
  assert.equal(r.fault.message, 'method not registered')
  assert.equal(r.fault.kind, 'error')
})

test('界面自己的异常归到 internal，并保留栈', async () => {
  const r = await call(async () => { throw new TypeError('x is not a function') })
  assert.equal(r.ok, false)
  assert.equal(r.fault.kind, 'internal')
  assert.equal(r.fault.message, 'x is not a function')
  assert.ok(r.fault.detail.includes('TypeError'), '要保留栈，便于定位')
})

test('传参原样透传给绑定', async () => {
  let got = null
  await call(async (...args) => { got = args; return 1 }, { year: 2025 }, 3)
  assert.deepEqual(got, [{ year: 2025 }, 3])
})

test('toFault 幂等：已经是 Fault 对象就原样返回', () => {
  const f = { kind: 'invalid', message: '金额格式不对' }
  assert.equal(toFault(f), f)
})

// ---------------------------------------------------------------------------
// 提示条停留多久
// ---------------------------------------------------------------------------
//
// 这些是行为约定，不是实现细节：不同类别的提示停留多久，
// 直接决定用户有没有机会看清它。三档各测边界 ——
// 「跑一会儿就没了」那种断言挡不住 3 秒变 5 秒。
//
//   信息类（success / info）  3 秒
//   问题类（warn / error）    5 秒

/** 清空提示队列（它是模块级状态，测试之间会互相影响）。 */
function drain() {
  for (const n of [...notices.items]) dismiss(n.id)
}

test('★ 信息类提示 3 秒后自动关闭', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  drain()

  const info = notify('已在文件管理器里打开 /Users/you/.mini-account/dataDB', 'info')
  const ok = notify('账套「杭州云帆软件有限公司」已建好', 'success')
  assert.equal(notices.items.length, 2)

  // 差 1 毫秒还在 —— 3 秒整就收掉的话，一句 20 字的中文提示还没读完。
  t.mock.timers.tick(2999)
  assert.equal(notices.items.length, 2, '不到 3 秒就消失了')

  t.mock.timers.tick(1)
  assert.equal(notices.items.length, 0, '过了 3 秒还没消失')
  assert.ok(!notices.items.some((n) => n.id === info || n.id === ok))

  drain()
})

test('★ 警告与错误 5 秒后自动关闭（比信息类多 2 秒）', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  drain()

  notify('本期体检未通过', 'error', '存在 2 张草稿凭证\n凭证字号不连续')
  notify('算不出默认保存位置，请手工填写账套文件路径', 'warn')

  // 3 秒：信息类该走了，问题类还得在。
  t.mock.timers.tick(3000)
  assert.equal(notices.items.length, 2,
    '警告/错误不该和信息类一起在 3 秒消失 —— 它们要读的东西更多')

  t.mock.timers.tick(1999)
  assert.equal(notices.items.length, 2, '不到 5 秒就消失了')

  t.mock.timers.tick(1)
  assert.equal(notices.items.length, 0, '过了 5 秒还没消失')

  drain()
})

test('默认 kind 是 error，所以 notify(msg) 也是 5 秒', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  drain()

  notify('出错了')
  assert.equal(notices.items[0].kind, 'error')

  t.mock.timers.tick(3000)
  assert.equal(notices.items.length, 1, '默认的 error 不该按信息类的 3 秒走')

  t.mock.timers.tick(2000)
  assert.equal(notices.items.length, 0)
  drain()
})

// 认不出的 kind 按问题类处理（时间长的那一档）：
// kind 写错时宁可多留一会儿，也不要让一条没人见过的提示一闪而过。
test('认不出的 kind 按问题类算，不会一闪而过', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  drain()

  notify('拼错的 kind', 'erorr')

  t.mock.timers.tick(3000)
  assert.equal(notices.items.length, 1, '未知 kind 被当成信息类了')

  t.mock.timers.tick(2000)
  assert.equal(notices.items.length, 0)
  drain()
})

test('提示可以手动提前关掉', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  drain()

  const id = notify('一条错误', 'error')
  dismiss(id)
  assert.equal(notices.items.length, 0)

  // 手动关掉之后定时器再到点，不能出错（dismiss 对不存在的 id 是空操作）
  t.mock.timers.tick(10_000)
  assert.equal(notices.items.length, 0)
})

// ---------------------------------------------------------------------------
// 假实现必须与真实现同形状
// ---------------------------------------------------------------------------

test('★ mockApp 返回的是值，不是 [值, null] 元组', async () => {
  for (const [name, fn] of Object.entries(mockApp)) {
    const v = await fn()
    if (Array.isArray(v)) {
      assert.ok(
        !(v.length === 2 && v[1] === null),
        `${name} 还在返回元组 —— 假实现与真实现不是一套约定了`,
      )
    }
  }
})

test('mockApp 的 currentBook 给的是带 open 的对象', async () => {
  const v = await mockApp.CurrentBook()
  assert.equal(typeof v, 'object')
  assert.equal(v.open, true)
  assert.ok(v.book?.companyName, '顶栏要显示单位名称')
})

test('★ mockApp 的字段名必须是 camelCase（与 Go 侧标签同一套规则）', async () => {
  // 以「用户数据」做键的字段不参与：里面的键不是界面字段名，
  // 而是任务名 / 用户输入（taskNotes 的键就是 bank_flow 这类任务名）。
  // 把它们也算进来，测试就会去要求用户数据符合驼峰命名 —— 那是荒谬的。
  const DATA_MAPS = ['taskNotes']
  const bad = []
  const walk = (v, path) => {
    if (Array.isArray(v)) return v.forEach((x, i) => walk(x, `${path}[${i}]`))
    if (!v || typeof v !== 'object') return
    if (DATA_MAPS.some((m) => path.endsWith('.' + m))) return
    for (const [k, val] of Object.entries(v)) {
      if (/^[a-z]/.test(k) && !/^[a-z][a-z0-9]*(?:[A-Z][a-z0-9]*)*$/.test(k)) {
        bad.push(`${path}.${k}`)
      }
      walk(val, `${path}.${k}`)
    }
  }
  for (const [name, fn] of Object.entries(mockApp)) {
    walk(await fn(), name)
  }
  assert.deepEqual(bad, [], '假数据里的字段名不符合界面取值约定')
})
