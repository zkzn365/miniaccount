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
STAMP="$(date '+%Y-%m-%dT%H:%M:%S%z')"

# ---------------------------------------------------------------------------
# ★ 版本号有**两个**落点，少写一个就会出现「同一个软件两个版本号」
# ---------------------------------------------------------------------------
#
#   1. Go 二进制里的 service.AppVersion —— `miniaccount version`、
#      备份清单、操作日志读的是它，靠 -ldflags 注入；
#   2. desktop/wails.json 的 info.productVersion —— macOS 的
#      CFBundleShortVersionString（访达「显示简介」）与 Windows exe
#      「属性 → 详细信息」读的是它。它是个**静态文件**，没人写就永远
#      是仓库里那个值。
#
# 原来是只做第 1 步，于是本地构建出来的 .app 报 0.1.0、命令行报
# 0.2.0-rc2 —— 同一个软件两个号，报问题时对不上。
# CI 那边（.github/workflows/release.yml）一直两步都做，
# 所以只有本地构建会漂。
#
# 不传版本号时沿用 service.AppVersion，本地构建就该是那个号。
if [ -z "$VERSION" ]; then
  VERSION="$(sed -n 's/^var AppVersion = "\(.*\)"$/\1/p' internal/service/service.go | head -1)"
fi
[ -n "$VERSION" ] || { echo "取不到版本号" >&2; exit 1; }

LDFLAGS="-X miniaccount/internal/service.AppVersion=$VERSION"
LDFLAGS="$LDFLAGS -X miniaccount/internal/service.BuildStamp=$STAMP"

node -e '
  const fs = require("fs");
  const p = "desktop/wails.json";
  const j = JSON.parse(fs.readFileSync(p, "utf8"));
  j.info.productVersion = process.argv[1];
  fs.writeFileSync(p, JSON.stringify(j, null, 2) + "\n");
' "$VERSION"
echo "==> 版本号 $VERSION（已写入 service.AppVersion 与 wails.json）"

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
