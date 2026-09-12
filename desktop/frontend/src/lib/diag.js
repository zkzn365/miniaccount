/**
 * 把界面侧的诊断信息写进程序日志。
 *
 * # 为什么需要它
 *
 * 桌面应用没有开发者工具。界面里抛出来的异常，用户看到的只是一个提示条
 * （甚至什么都没有），**不留任何痕迹** —— 用户来报「点了没反应」时，
 * 我们手上没有版本号之外的任何线索，只能靠猜。
 *
 * Wails 留了一条写入 Go 日志的通道：`WailsInvoke('L' + 级别 + 文本)`。
 * 界面把它用在两个地方：
 *   - 启动结果（有没有账套、跳到了哪一页）
 *   - 未捕获的异常
 *
 * 于是「用户报错 → 看日志」这条路是通的，而不是靠反复问用户点了什么。
 *
 * 浏览器预览（vite dev）里没有运行时，退化成 console。
 */

const LEVELS = { print: 'P', debug: 'D', info: 'I', warn: 'W', error: 'E', fatal: 'F' }

export function logDiag(level, text) {
  const line = `[界面] ${text}`
  if (typeof window !== 'undefined' && typeof window.WailsInvoke === 'function') {
    try {
      window.WailsInvoke('L' + (LEVELS[level] ?? 'P') + line)
    } catch {
      // 日志写不进去不该影响界面本身
    }
  }
  if (level === 'error' || level === 'fatal') console.error(line)
  else console.log(line)
}
