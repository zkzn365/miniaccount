#!/bin/sh
# 发布构建：命令行版 + 桌面版。
#
# 用法：
#   ./build.sh              # 用 service.AppVersion 里的版本号
#   ./build.sh 0.2.0-rc3    # 指定版本号
#
# # 为什么要有这个脚本
#
# 版本号曾经只写在 service.AppVersion 里，构建时没人管它，
# 于是出现过「用户贴回来的输出其实是旧二进制产生的」这种
# 无法从输出本身判断的情况。现在两件事都由脚本保证：
#   1. 命令行版与桌面版用**同一个**版本号；
#   2. `miniaccount version` 能打出来，报告问题时可以对照。
set -e
cd "$(dirname "$0")"
. ./env.sh

VERSION="${1:-}"
if [ -n "$VERSION" ]; then
  LDFLAGS="-X miniaccount/internal/service.AppVersion=$VERSION"
fi

STAMP="$(date '+%Y-%m-%dT%H:%M:%S%z')"
LDFLAGS="$LDFLAGS -X miniaccount/internal/service.BuildStamp=$STAMP"

# -s -w 去掉符号表与调试信息：体积从 22M 降到 15M 左右。
# 代价是 panic 的栈里没有行号 —— 但每个绑定都套了 recoverTo，
# 报出来的是「哪个方法崩了」而不是裸栈，够定位。
LDFLAGS="$LDFLAGS -s -w"

mkdir -p dist

echo "==> 命令行版"
go build -trimpath -ldflags "$LDFLAGS" -o dist/miniaccount-darwin-arm64 ./cmd/miniaccount

echo "==> 桌面版（wails build）"
# wails 自己调 go build，ldflags 要经 -ldflags 参数传进去（注意两边的引号）
( cd desktop && wails build -clean -ldflags "$LDFLAGS" )

# 产物改名成带语气的名字：用户看到的应该是「小账本」，
# 而不是一个英文的 app 目录名。
# ★ 先删再拷。
#
# `cp -R src dist/小账本.app` 在目标已存在时会把 src 拷**进**去，
# 于是 dist/小账本.app/miniaccount.app/... 一层层套下去 ——
# 而且不报错。上一次构建的残留就这么留在了发布产物里。
rm -rf "dist/小账本.app"
cp -R "desktop/build/bin/miniaccount.app" "dist/小账本.app"
# 可执行文件单独放一份，便于直接双击运行与做完整性校验
cp "desktop/build/bin/miniaccount.app/Contents/MacOS/小账本" dist/小账本-darwin-arm64

echo
echo "==> 产物"
./dist/miniaccount-darwin-arm64 version
ls -lh dist/

# ---------------------------------------------------------------------------
# 界面冒烟测试
# ---------------------------------------------------------------------------
#
# 这一步发现的第一个问题就是「整个界面跑不起来而所有测试全绿」：
# Go 侧的测试直接调 Go 方法，绕过了 Wails 的 JS 调用约定；
# 前端只有 api.js，而它对那个约定的理解是错的。
# 所以发布前必须把三层接起来跑一遍（见 desktop/frontend/test/gui.test.mjs）。
if command -v node >/dev/null 2>&1 && [ -d desktop/frontend/node_modules ]; then
  echo
  echo "==> 界面冒烟测试"
  ( cd desktop/frontend && npm run test:gui )
else
  echo
  echo "==> 跳过界面冒烟测试（没有 node 或未安装前端依赖）"
fi
