import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// 给自动化测试用：把界面（含 .vue）打成一个 Node 能 import 的 ESM 包。
//
// ★ 用 lib 模式而**不是** build.ssr。
// build.ssr 会让 @vitejs/plugin-vue 按服务端渲染来编译组件，
// 组件里会被塞进 `useSSRContext()` —— 在真实 DOM 下挂载时直接报
// "Cannot read properties of undefined (reading 'modules')"。
// 测试要的是**和用户跑的那份一模一样**的客户端组件。
export default defineConfig({
  plugins: [vue()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    outDir: 'testdist',
    emptyOutDir: true,
    minify: false,
    sourcemap: false,
    lib: {
      entry: fileURLToPath(new URL('./src/testentry.js', import.meta.url)),
      formats: ['es'],
      fileName: () => 'testentry.js',
    },
    rollupOptions: { external: ['vue'] },
  },
})
