import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'
import fs from 'node:fs'
import path from 'node:path'

const OUT_DIR = fileURLToPath(new URL('./dist', import.meta.url))

/**
 * ★ 构建完把 `.gitkeep` 写回 dist/。
 *
 * 为什么需要这么个插件：`emptyOutDir: true` 会把 dist/ 整个清空，
 * 包括那个占位文件 —— 而 `//go:embed all:frontend/dist` 要求这个目录
 * **在编译时存在**（见 .gitignore 里的说明）。
 *
 * 不写回的话，开发者每次 `npm run build` 之后 `git status` 里都会多出
 * 一条「deleted: desktop/frontend/dist/.gitkeep」，看着像是自己弄坏了
 * 什么东西，而顺手把这个删除提交上去的后果是：下一个 clone 的人
 * 连 `go build` 都过不去。
 *
 * 让它自己长回来，比写一条「记得手动 checkout 回来」的文档可靠。
 */
function keepDistPlaceholder() {
  return {
    name: 'keep-dist-placeholder',
    closeBundle() {
      fs.mkdirSync(OUT_DIR, { recursive: true })
      fs.writeFileSync(
        path.join(OUT_DIR, '.gitkeep'),
        '这个目录是 `//go:embed all:frontend/dist` 的落点，编译时**必须存在**。\n' +
          '\n' +
          '干净检出时里面只有这个文件 —— 前端产物由\n' +
          '`cd desktop/frontend && npm run build`（或 `wails build`）生成。\n' +
          '直接 go build / go test 能过，但界面上不会有东西。\n' +
          '\n' +
          '这个文件由 vite.config.js 的 keepDistPlaceholder 插件在每次\n' +
          '构建后写回：emptyOutDir 会把它删掉，不写回的话 git status\n' +
          '永远是脏的。\n',
      )
    },
  }
}

// Wails 的 dev 模式会注入一个反向代理，因此这里不需要配 server.proxy。
// 只需要把 `@` 指到 src，与 shadcn-vue 的默认别名保持一致 ——
// 组件里的 `import { cn } from '@/lib/utils'` 才能直接照抄社区示例。
export default defineConfig({
  plugins: [vue(), tailwindcss(), keepDistPlaceholder()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    // 产物直接给 Go 的 embed 用，文件名带 hash 没有意义，
    // 反而会让「构建产物在哪」这类排查变麻烦。
    outDir: 'dist',
    emptyOutDir: true,
  },
})
