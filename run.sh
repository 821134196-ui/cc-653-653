#!/usr/bin/env bash
# 一条本地命令启动前后端：
#   ./run.sh        生产模式：构建前端并由 Go 在 :8080 单端口提供服务
#   ./run.sh dev    开发模式：Go(:8080) + Vite(:5173，带 /api 代理热更新)
set -euo pipefail
cd "$(dirname "$0")"

# Go 工具链：优先使用 PATH 中的 go，其次用户目录安装的 go-sdk
if ! command -v go >/dev/null 2>&1; then
  if [ -x "$HOME/go-sdk/go/bin/go" ]; then
    export PATH="$HOME/go-sdk/go/bin:$PATH"
  else
    echo "未找到 Go，请先安装 Go 1.23+（或解压到 ~/go-sdk）" >&2
    exit 1
  fi
fi
export GOSUMDB=off
export SP_ADDR="${SP_ADDR:-:8080}"

if [ "${1:-}" = "dev" ]; then
  echo "▶ 开发模式：后端 http://localhost:8080  前端 http://localhost:5173"
  (cd backend && go run .) &
  BACK_PID=$!
  (cd frontend && npm install --no-audit --no-fund && npm run dev)
  kill $BACK_PID 2>/dev/null || true
  exit 0
fi

echo "▶ 构建前端..."
(cd frontend && npm install --no-audit --no-fund && npm run build)

echo "▶ 启动服务：http://localhost:8080"
cd backend
SP_FRONTEND_DIST="../frontend/dist" exec go run .
