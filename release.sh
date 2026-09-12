#!/bin/sh
# 发一个版本：打标签 → 推送 → GitHub Actions 自动出三个平台的包。
#
# 用法：
#   ./release.sh 0.2.0            # 正式版（会拿到 Releases 页面的 Latest）
#   ./release.sh 0.2.0-rc1        # 预发布（标 pre-release，不抢 Latest）
#
# 跑完去看 Actions：https://github.com/zkzn365/miniaccount/actions
#
# # 为什么要这么个脚本
#
# 「打标签」这件事看着只有一行 `git tag`，但每一步都能踩坑，而且
# **踩了要等 CI 跑完才发现**（三个平台，好几分钟）：
#
#   - 工作区没提交干净 → 打出来的标签对应的代码不是你以为的那份
#   - 本地落后于远端 → 标签打在一个别人已经改过的提交上
#   - 标签名写错（v 忘了、写成 0.2 或 1.0.0.0）→ 产物名会很难看
#   - 标签已经存在 → git 报错，而如果加 -f 就会**悄悄把已有的版本
#     挪到新提交上**，已经发出去的包和标签就对不上了
#
# 所以这里把这些检查都做在前面，全过了才真的打标签。
set -e
cd "$(dirname "$0")"

red()  { printf '\033[31m%s\033[0m\n' "$1"; }
warn() { printf '\033[33m%s\033[0m\n' "$1"; }
ok()   { printf '\033[32m%s\033[0m\n' "$1"; }

die() { red "✗ $1"; exit 1; }

V="${1:-}"
if [ -z "$V" ]; then
  cat <<'EOF'
用法：./release.sh <版本号>

  正式版    ./release.sh 0.2.0
  预发布    ./release.sh 0.2.0-rc1      # 带连字符就是预发布

版本号规则：主.次.修订，可选 -预发布后缀（semver）。
打出来的标签是 v<版本号>，CI 会把 v 去掉当版本号用。
EOF
  exit 1
fi

TAG="v$V"

# ---------------------------------------------------------------------------
# 1. 版本号形状
# ---------------------------------------------------------------------------
# 只接受 主.次.修订 加可选的 -后缀。挡掉 0.2 / 1.0.0.0 / v0.2.0
# 这些「看着也行」的写法 —— 它们会一路传到产物文件名、
# macOS 的 CFBundleShortVersionString 和 Windows 的 exe 版本信息里。
case "$V" in
  v*) die "版本号不用带 v —— 直接写 ${V#v}，标签会自动加 v" ;;
esac
if ! printf '%s' "$V" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'; then
  die "版本号 «${V}» 不是 semver。要 主.次.修订 三段，例如 0.2.0 或 0.2.0-rc1"
fi

# ---------------------------------------------------------------------------
# 2. 工作区干净
# ---------------------------------------------------------------------------
if [ -n "$(git status --porcelain)" ]; then
  red "✗ 工作区有未提交的改动："
  git status --short
  die "先提交（或 stash）再发版 —— 否则标签对应的代码不是你现在看到的这份"
fi
ok "✓ 工作区干净"

# ---------------------------------------------------------------------------
# 3. 在 main 上，且与远端一致
# ---------------------------------------------------------------------------
BRANCH="$(git rev-parse --abbrev-ref HEAD)"
[ "$BRANCH" = "main" ] || die "当前在 «${BRANCH}» 分支上，发版应当在 main 上"

git fetch -q origin main
LOCAL="$(git rev-parse HEAD)"
REMOTE="$(git rev-parse origin/main)"
if [ "$LOCAL" != "$REMOTE" ]; then
  if git merge-base --is-ancestor "$LOCAL" "$REMOTE" 2>/dev/null; then
    die "本地落后于 origin/main，先 git pull"
  elif git merge-base --is-ancestor "$REMOTE" "$LOCAL" 2>/dev/null; then
    warn "⚠ 本地领先 origin/main（有还没推的提交）"
    warn "  标签会打在这些提交上，但它们不在远端 —— CI 拉不到，构建会失败"
    die "先 git push origin main"
  else
    die "本地与 origin/main 已分叉，先处理分支"
  fi
fi
ok "✓ main 与远端一致（$(git rev-parse --short HEAD)）"

# ---------------------------------------------------------------------------
# 4. 标签没被用过
# ---------------------------------------------------------------------------
# ★ 这一条不能省，也**不能靠 -f 绕过**：
# 标签一旦推上去并出了包，它就是那个版本的唯一凭据。
# 把它挪到别的提交上，已经下载了旧包的人手里的版本号就对不上了。
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
  red "✗ 标签 $TAG 已存在，指向 $(git rev-parse --short "$TAG")"
  cat <<EOF

  要重新发同一个版本，先把它删掉再重打：

    git tag -d $TAG
    git push origin :refs/tags/$TAG
    ./release.sh $V

  如果只是想让 CI 重跑一遍（代码没变），**不用删标签** ——
  去 Actions 页面点 Re-run failed jobs 就行，
  CI 会用 --clobber 覆盖同名产物。

  如果这次是新版本，换一个号：./release.sh <更大的版本号>
EOF
  exit 1
fi
ok "✓ 标签 $TAG 还没用过"

# ---------------------------------------------------------------------------
# 5. 预发布提示
# ---------------------------------------------------------------------------
case "$V" in
  *-*) warn "⚠ 带后缀 «${V#*-}»，会发成 pre-release（不抢 Releases 的 Latest 标记）" ;;
esac

# ---------------------------------------------------------------------------
# 6. 打标签并推送 —— 推上去就是触发 CI
# ---------------------------------------------------------------------------
# 用附注标签（-a）而不是轻量标签：它带作者、日期与说明，
# Releases 页面上显示的也是这条说明。
git tag -a "$TAG" -m "小账本 $V"

echo
echo "即将推送标签 ${TAG}（指向 $(git rev-parse --short HEAD)），"
echo "推送后会触发 GitHub Actions 构建 macOS / Windows / Linux 三个包。"
printf '继续？[y/N] '
read -r ans
case "$ans" in
  y|Y) ;;
  *) git tag -d "$TAG" >/dev/null; warn "已取消（本地标签也删掉了）"; exit 0 ;;
esac

git push origin "$TAG"

echo
ok "✓ 标签 $TAG 已推送"
cat <<EOF

接下来：
  1. 构建进度  https://github.com/zkzn365/miniaccount/actions
  2. 构建产物  https://github.com/zkzn365/miniaccount/releases/tag/$TAG
     （三个平台各一个压缩包，里含界面版与命令行版）

两个平台的构建我没法在本地验证过，第一次发版留意一下：
  - Linux 用的是 WebKitGTK 4.1，依赖 libwebkit2gtk-4.1-dev
  - macOS 走 darwin/universal（Intel + Apple 芯片双架构）
红了就去看日志，把失败的那步贴出来。
EOF
