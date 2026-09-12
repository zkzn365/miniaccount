#!/bin/sh
# 开发环境变量。
#
# 受限环境下 ~/Library/Caches、~/code/go 等路径不可写，
# 因此把 Go 的构建缓存、模块缓存、校验和数据库全部重定向到工程内目录。
#
# 用法： . ./env.sh
export GOCACHE="$PWD/.gocache"
export GOTMPDIR="$PWD/.gotmp"
export GOPATH="$PWD/.gopath"
export GOMODCACHE="$GOPATH/pkg/mod"
mkdir -p "$GOCACHE" "$GOTMPDIR" "$GOMODCACHE"

# GOBIN 也指到工程内。
#
# 受限环境下 `go install` 默认会往 ~/code/go/bin 写，
# 那会在沙箱里被拒绝 —— 而错误信息只说「operation not permitted」，
# 不告诉你是哪一步。显式指到工程内，问题就从「装不上」变成「装在这儿」。
export GOBIN="$PWD/.gopath/bin"
export PATH="$GOBIN:$PATH"

# npm 缓存同理：默认的 ~/.npm 可能不可写。
export npm_config_cache="$PWD/.npm-cache"
