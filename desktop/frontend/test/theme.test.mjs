/**
 * 主题色的**可读性**测试。
 *
 * # 为什么主题也需要测试
 *
 * 主题色来自一张中国传统色卡：紫罗兰 #5f469a 与郁金裙 #ffd301。
 * 这两个颜色的对比度差得很远，用法一旦对调，界面就会坏，而且是
 * **看不出「坏了」的那种坏**：
 *
 *   白字 / 紫罗兰底 = 7.2:1   ✓
 *   紫罗兰字 / 白底  = 7.3:1   ✓
 *   深字 / 郁金裙底  = 11.4:1  ✓
 *   郁金裙字 / 白底  = 1.4:1   ✗ 一片看不见的淡黄
 *
 * 最容易犯的错是把 --warn 直接改成 #ffd301 —— 看起来「用了金色」，
 * 实际上全程序的警告文字都变成了大白底上的一抹淡黄。
 * 「改完还能编译、页面也能打开」，所以只能靠算对比度来挡。
 *
 * 这个测试直接从 src/style.css 里读 oklch 值，换算成 sRGB 再按
 * WCAG 公式算对比度 —— 断言的依据是**样式表本身**，不是抄一遍常量。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const css = fs.readFileSync(path.join(here, '../src/style.css'), 'utf8')

/** 取某个选择器块里声明的 oklch 变量。 */
function block(sel) {
  const i = css.indexOf(sel + ' {')
  assert.ok(i >= 0, `style.css 里找不到 ${sel} 块`)
  const j = css.indexOf('\n}', i)
  const out = {}
  for (const m of css.slice(i, j).matchAll(/--([a-z-]+):\s*(oklch\([^)]+\))/g)) {
    out[m[1]] = m[2]
  }
  assert.ok(Object.keys(out).length > 10, `${sel} 里解析到的变量太少`)
  return out
}

// ---- oklch → sRGB → 相对亮度 → 对比度 ----

function oklchToRgb(str) {
  const m = /oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)/.exec(str)
  assert.ok(m, `看不懂的颜色值：${str}`)
  const [L, C, H] = [Number(m[1]), Number(m[2]), Number(m[3]) * Math.PI / 180]
  const a = C * Math.cos(H)
  const b = C * Math.sin(H)
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3
  const mm = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3
  const s = (L - 0.0894841775 * a - 1.2914855480 * b) ** 3
  const lin = [
    +4.0767416621 * l - 3.3077115913 * mm + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * mm - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * mm + 1.7076147010 * s,
  ]
  return lin.map((c) => {
    const v = c <= 0.0031308 ? 12.92 * c : 1.055 * Math.pow(Math.max(c, 0), 1 / 2.4) - 0.055
    return Math.round(Math.min(1, Math.max(0, v)) * 255)
  })
}

function luminance(rgb) {
  const c = rgb.map((v) => {
    const x = v / 255
    return x <= 0.04045 ? x / 12.92 : Math.pow((x + 0.055) / 1.055, 2.4)
  })
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]
}

function contrast(fg, bg) {
  const a = luminance(fg)
  const b = luminance(bg)
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
}

const hex = (rgb) => '#' + rgb.map((v) => v.toString(16).padStart(2, '0')).join('')

const light = block(':root')
const dark = { ...light, ...block('.dark') } // .dark 只覆盖一部分，其余继承

/** 一组要检查的配对：[说明, 前景变量, 背景变量, 最低对比度]。 */
const PAIRS = [
  ['主按钮：白字 / 紫罗兰底', 'primary-foreground', 'primary', 4.5],
  ['强调文字：紫罗兰 / 白底', 'primary', 'background', 4.5],
  ['警告徽标：深字 / 郁金裙底', 'gold-foreground', 'gold', 4.5],
  ['警告文字：--warn / 白底', 'warn', 'background', 4.5],
  ['正文 / 背景', 'foreground', 'background', 4.5],
  ['正文 / 卡片', 'foreground', 'card', 4.5],
  ['次要文字 / 背景', 'muted-foreground', 'background', 3],
  ['次要文字 / 卡片', 'muted-foreground', 'card', 3],
  ['选中项：文字 / 悬停底', 'accent-foreground', 'accent', 4.5],
  ['破坏性按钮：文字 / 底', 'destructive-foreground', 'destructive', 4.5],
  ['输入框边框 / 卡片（可见性）', 'input', 'card', 1.05],
  ['分隔线 / 背景（可见性）', 'border', 'background', 1.05],
]

/** 会计语义色——它们不跟着主题走，但必须始终看得清（借/贷/盈/亏）。 */
const SEMANTIC = [
  ['借方', 'debit'],
  ['贷方', 'credit'],
  ['盈利', 'profit'],
  ['亏损', 'loss'],
  ['已结账', 'closed'],
]

function check(name, vars) {
  const problems = []
  for (const [label, fgKey, bgKey, min] of PAIRS) {
    if (!(fgKey in vars)) { problems.push(`--${fgKey} 不存在`); continue }
    if (!(bgKey in vars)) { problems.push(`--${bgKey} 不存在`); continue }
    const c = contrast(oklchToRgb(vars[fgKey]), oklchToRgb(vars[bgKey]))
    if (c < min) {
      problems.push(
        `${label}：${c.toFixed(2)}:1 低于 ${min}:1 ` +
        `（${hex(oklchToRgb(vars[fgKey]))} / ${hex(oklchToRgb(vars[bgKey]))}）`)
    }
  }
  for (const [label, key] of SEMANTIC) {
    if (!(key in vars)) { problems.push(`--${key} 不存在`); continue }
    const c = contrast(oklchToRgb(vars[key]), oklchToRgb(vars.background))
    if (c < 3) {
      problems.push(`${label}色 / 背景：${c.toFixed(2)}:1 低于 3:1 —— 报表上一眼分不出来`)
    }
  }
  assert.deepEqual(problems, [], `${name}的配色有问题：\n  - ` + problems.join('\n  - '))
}

test('★ 浅色主题：每一组配对的对比度都达标', () => {
  check('浅色主题', light)
})

test('★ 深色主题：每一组配对的对比度都达标', () => {
  check('深色主题', dark)
})

// ★ 郁金裙只能当底色，不能当文字色。
//
// 这是这套配色最容易踩的坑：它是一个很亮的黄，放在白底上完全读不了。
// 所以 --gold 与 --warn 是**两个**令牌：前者做底、后者做字。
// 有人图省事把 --warn 改成 --gold，这一条会立刻红。
test('★ 郁金裙只做底色，文字用压暗过的 --warn', () => {
  const gold = contrast(oklchToRgb(light.gold), oklchToRgb(light.background))
  assert.ok(gold < 3,
    `--gold 在白底上的对比度是 ${gold.toFixed(2)}:1 —— ` +
    '如果它突然「够清楚」了，说明颜色被改过，请核对是不是把两个令牌合并了')

  const warn = contrast(oklchToRgb(light.warn), oklchToRgb(light.background))
  assert.ok(warn >= 4.5,
    `--warn 在白底上只有 ${warn.toFixed(2)}:1，警告文字会看不清 —— ` +
    '它必须是**压暗过**的金色，不能直接等于 #ffd301')

  // 两者色相要接近（都还是「金」），否则就不是这套配色了
  const hue = (v) => Number(/oklch\([\d.]+\s+[\d.]+\s+([\d.]+)/.exec(v)[1])
  const dh = Math.abs(hue(light.gold) - hue(light.warn))
  assert.ok(Math.min(dh, 360 - dh) < 25,
    `--gold 与 --warn 的色相差了 ${dh.toFixed(1)}°，不像是同一个金色`)
})

// ★ 主题色确实来自那张色卡，没有被悄悄换掉
test('★ 主色就是紫罗兰 #5f469a', () => {
  const rgb = oklchToRgb(light.primary)
  assert.deepEqual(rgb, [95, 70, 154],
    `--primary 换算成 RGB 是 ${rgb.join(' ')}，期望「95 70 154」（紫罗兰 #5f469a）`)
  const goldRgb = oklchToRgb(light.gold)
  assert.deepEqual(goldRgb, [255, 211, 1],
    `--gold 换算成 RGB 是 ${goldRgb.join(' ')}，期望「255 211 1」（郁金裙 #ffd301）`)
})
