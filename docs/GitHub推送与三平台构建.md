# GitHub 推送与三平台构建

## 目标

将本次建账修复、回归测试与开发规范推送到 `zkzn365/miniaccount` 的 main 分支，并在 GitHub Actions 实际验证 Windows、macOS、Linux 构建，提供可下载产物。

## 调查与故障依据

- 远程 origin 与本地 main 同步，当前账号有仓库管理权限。
- 现有 release.yml 已有三平台矩阵：Windows amd64、macOS universal、Linux amd64；main push 只测试，标签或手动触发才打包。
- 历史运行 https://github.com/zkzn365/miniaccount/actions/runs/34991585585 的 Windows checkout 报 `invalid path: internal/ai/aiprovider/aux.go`。AUX 是 Windows 保留设备名，带扩展名也不可用；应改名，不能关闭 Git 的路径保护。
- macOS 历史构建成功，但压缩包只包含 .app，漏了已编译的 CLI；CLI 也只按 runner 架构编译，与通用版交付约定不符。
- 现有手动触发会自动发布 Release。本次只要求推送与三平台编译，因此增加明确发布开关，默认仅上传 Actions artifacts。

## 技术方案与涉及模块

1. 将 `internal/ai/aiprovider/aux.go` 重命名为 `auxiliary.go`，文件内容完全保持，不改业务逻辑；已扫描跟踪文件，当前只有这一个 Windows 保留名冲突。
2. release.yml 增加 workflow_dispatch.publish 布尔输入，默认 false。仅 push 标签自动发布；手动（包括指定标签）仅在 publish=true 时发布，且使用独立 dev 标签。Release 新建标签绑定本次 GITHUB_SHA。默认 contents:read，仅 release 作业授予 contents:write。
3. 三平台构建前各运行建账及种子事务回归用例；Linux 仍保留 go vet 与完整 go test 门禁。
4. macOS CLI 分别编译 amd64/arm64，用 lipo 合并；将 .app 与 CLI 放入单独 staging 目录后一起压缩，不再遗漏 CLI。
5. 保持现有前端先构建、Wails 固定版本、Linux GTK/WebKit 检测与产物命名约定。

## 接口与数据流

无业务接口或数据库结构变化。构建接口增加 `publish`，手动输入 `version=0.2.5,publish=false` 触发验证。

Git 提交 → main 推送 → CI 测试；手动触发同一提交 → Linux 完整测试 → 三平台建账回归与原生 runner 构建 → upload-artifact；仅显式发布或版本标签进入 release job。

## 边界条件

- 本次只推送 main 并手动构建（publish=false），不重写历史、不覆盖旧标签或正式 Release；产物关联当前 SHA。原有标签发布重跑仍保留覆盖同名产物的行为，使用者须知。
- 不提交账套、缓存、本机工具或二进制到源码仓库。
- macOS Universal 覆盖 Intel/Apple Silicon；Windows、Linux 当前只覆盖 x64。
- Linux GUI 依赖 GTK3/WebKitGTK，构建成功不代表任意发行版均可运行。
- Windows/macOS 包不新增开发者签名或公证。
- Actions 失败时读具体日志定位，修复仅限三平台构建所必需的内容。

## 后续操作

普通推送 main 运行测试。需要三平台包时，在 GitHub Actions 的「构建与发布」页面选择 Run workflow，指定分支和版本，publish 保持 false；完成后从该次运行的 Artifacts 下载 macos-universal、windows-amd64、linux-amd64。只有需要发布 GitHub Release 时才开启 publish，或推送新的 v* 标签。

等价命令：

```bash
gh workflow run release.yml --repo zkzn365/miniaccount --ref main -f version=0.2.5 -f publish=false
```

## 验证

先验证重命名前后内容一致、Windows 文件名扫描、工作流静态检查，再执行本地 Go 构建和单元测试。已完成的本地 macOS 打包与 37 项 GUI 冒烟测试保留为前序证据。远程三平台以同一 SHA 的 Actions 实际成功及产物列表为最终依据，结果在会话交付链接。
