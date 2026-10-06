#!/usr/bin/env bash
# 一条本地命令启动港口岸电结算系统（后端 Go/Gin + 前端 Vue）
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND="$ROOT/backend"
FRONTEND="$ROOT/frontend"
ADDR="${ADDR:-:8080}"
WEB_ADDR="${WEB_ADDR:-http://localhost:8080}"

# ---- Go 工具链：优先使用 PATH，其次用户目录安装 ----
if ! command -v go >/dev/null 2>&1; then
  if [ -x "$HOME/.local/go/bin/go" ]; then
    export PATH="$HOME/.local/go/bin:$PATH"
  else
    echo "未找到 Go，请安装 Go 1.22+（或解压到 ~/.local/go）" >&2
    exit 1
  fi
fi

mkdir -p "$BACKEND/data"

# ---- 前端：开发模式（热更新）或使用已构建产物 ----
WEB_PID=""
cleanup() {
  [ -n "$WEB_PID" ] && kill "$WEB_PID" 2>/dev/null || true
}
trap cleanup EXIT

if [ "${1:-prod}" = "dev" ]; then
  echo ">> 前端开发服务器（Vite, http://localhost:5173，/api 代理到 8080）"
  (cd "$FRONTEND" && [ -d node_modules ] || npm install)
  (cd "$FRONTEND" && npm run dev) &
  WEB_PID=$!
else
  if [ ! -f "$BACKEND/web/dist/index.html" ]; then
    echo ">> 首次运行：构建前端..."
    (cd "$FRONTEND" && npm install && npm run build)
  fi
  echo ">> 前端由 Go 直接托管：$WEB_ADDR"
fi

echo ">> 启动后端 Gin 服务 $ADDR"
cd "$BACKEND"
exec go run ./cmd/server
