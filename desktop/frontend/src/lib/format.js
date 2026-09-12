/**
 * 金额格式化。
 *
 * ★ 全链路金额都是「分」（int64），前端只做**显示**转换，绝不参与运算。
 *
 * 为什么不在 Go 侧直接返回格式化好的字符串：
 *   - 排序、求和、条件着色都需要数值
 *   - 但 JS 的 number 是 IEEE754 双精度，18014398509481983 之后就丢精度
 *   - 折中：Go 传「分」的整数（Number.isSafeInteger 到 9007 万亿元，够用），
 *     前端只在渲染那一刻转成字符串；所有加减乘除都在 Go 侧完成。
 */

const YUAN = 100

/** 把「分」格式化成带千分位的「元」，负数用括号（会计惯例）。 */
export function fmtMoney(cents, { paren = true, blankZero = false } = {}) {
  if (cents === null || cents === undefined) return ''
  const n = Number(cents)
  if (!Number.isFinite(n)) return ''
  if (blankZero && n === 0) return ''
  const neg = n < 0
  const abs = Math.abs(n)
  const yuan = Math.floor(abs / YUAN)
  const frac = abs % YUAN
  const s = `${yuan.toLocaleString('zh-CN')}.${String(frac).padStart(2, '0')}`
  if (!neg) return s
  // 会计报表里负数写 (1,234.00) 而不是 -1,234.00：
  // 减号又细又短，一列数字里很容易看漏。
  return paren ? `(${s})` : `-${s}`
}

/** 把「分」转成不带千分位的「元」字符串，用于输入框。 */
export function centsToYuanInput(cents) {
  if (cents === null || cents === undefined) return ''
  const n = Number(cents)
  if (!Number.isFinite(n) || n === 0) return ''
  const neg = n < 0
  const abs = Math.abs(n)
  const s = `${Math.floor(abs / YUAN)}.${String(abs % YUAN).padStart(2, '0')}`
  return neg ? `-${s}` : s
}

/** 把用户输入的「元」字符串解析成「分」。返回 null 表示格式非法。 */
export function parseYuanToCents(text) {
  if (text === null || text === undefined) return 0
  let s = String(text).trim().replace(/[,，\s　]/g, '')
  if (s === '') return 0
  let neg = false
  if (s[0] === '-') { neg = true; s = s.slice(1) }
  else if (s[0] === '+') { s = s.slice(1) }
  const m = /^(\d*)(?:\.(\d{0,2}))?$/.exec(s)
  if (!m) return null
  const yuan = m[1] === '' ? 0 : Number(m[1])
  const frac = (m[2] ?? '').padEnd(2, '0')
  const cents = yuan * YUAN + Number(frac)
  if (!Number.isSafeInteger(cents)) return null
  return neg ? -cents : cents
}

/** 文件体积。 */
export function fmtBytes(n) {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0, v = n
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

/** 期间显示。 */
export function fmtPeriod(year, month) {
  return `${year}-${String(month).padStart(2, '0')}`
}
