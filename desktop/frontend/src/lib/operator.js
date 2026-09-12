/**
 * 记账人：全程序共用的一份。
 *
 * # 为什么收拢成一个模块
 *
 * 原来凭证、工资、发票、报销、银行流水、账期、AI 七个页面各自
 * `ref(localStorage.getItem('operator'))`，各存各的 —— 结果同一个人
 * 在凭证上叫「李会计」、在工资表上叫「小李」，事后按操作人查日志根本查不全。
 *
 * 现在只有一个来源：**本账套的记账人**（存在账套的 setting 表里）。
 * 页面绑定同一个 ref，改了立刻回写。
 *
 * ★ 它是账套级的：同一台电脑给两家公司做账，可以签不同的名；
 * 换账套时必须把缓存清掉（见 resetBookkeeper），否则新账套会显示
 * 上一本的签章人 —— 而签章人错了，凭证上签的就是别人的名字。
 *
 * ★ 它是「默认填谁」，不是「替谁签字」：
 *   - 名字显示在页面上，看得见也改得动；
 *   - 空着就是不填，程序不会用「系统」之类的名字顶上；
 *   - 每一次落章都进操作日志。
 */
import { ref } from 'vue'
import { api } from './api'

export const bookkeeper = ref('')
export const bookkeeperReady = ref(false)

let loading = null

/** 读一次本机记账人（多次调用只发一次请求）。 */
export async function loadBookkeeper() {
  if (bookkeeperReady.value) return bookkeeper.value
  if (!loading) {
    loading = api.bookkeeper().then((r) => {
      if (r.ok) bookkeeper.value = r.data ?? ''
      bookkeeperReady.value = true
      return bookkeeper.value
    })
  }
  return loading
}

/**
 * 清掉缓存 —— **切换账套时必须调用**。
 *
 * 记账人是账套级的，留着上一个账套的值会让新账套显示别人的名字。
 */
export function resetBookkeeper() {
  bookkeeper.value = ''
  bookkeeperReady.value = false
  loading = null
}

/** 记住这个人（空串表示不预设）。 */
export async function rememberBookkeeper(name) {
  const v = (name ?? '').trim()
  bookkeeper.value = v
  const r = await api.saveBookkeeper(v)
  return r
}

/**
 * 页面用的入口：拿到记账人，并在用户改动时回写。
 *
 * 用法：
 *   const operator = useOperator()      // 一个 ref，初始为空
 *   onMounted(() => operator.load())    // 挂载时带出记住的人
 */
export function useOperator() {
  return {
    value: bookkeeper,
    load: loadBookkeeper,
    save: rememberBookkeeper,
  }
}
