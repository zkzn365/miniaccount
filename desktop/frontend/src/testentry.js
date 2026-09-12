/**
 * 测试入口：把界面模块打成一个 Node 能直接 import 的包。
 *
 * 组件是 .vue 单文件，Node 不认；所以先让 vite 以 SSR 模式打一遍，
 * 产物在 testdist/ 里，测试再 import 它。
 * 这样测试跑的是**真的组件**，不是另写一份。
 */
// createApp 也从这里出：测试如果自己 `import('vue')`，会拿到**另一个**
// Vue 实例（CJS/ESM 两份），组件挂载时报 "Cannot read properties of
// undefined (reading 'modules')" —— 这类双实例问题很难认出来。
export { createApp, nextTick } from 'vue'
export { default as App } from './App.vue'
export { default as router } from './router.js'
export * as api from './lib/api.js'
export * as book from './lib/book.js'
export { notify, toFault, call } from './lib/api.js'
