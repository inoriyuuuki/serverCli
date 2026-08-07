#!/bin/bash
# =============================================================================
# check.sh - 判断本机 serverCli 实例是否运行
# 输出: 1=运行中  0=未运行（供 install_common.sh 判断）
# =============================================================================
PROJECT_DIR="${1:-$(cd "$(dirname "$0")" && pwd)}"
cd "$PROJECT_DIR"

if [ ! -f .instance ]; then
    echo "0"
    exit 0
fi
INSTANCE=$(cat .instance)

case "$INSTANCE" in
  production-primary)
    if curl -sf --max-time 3 http://127.0.0.1:9043/health/live >/dev/null 2>&1; then
        echo "1"
    else
        echo "0"
    fi
    ;;
  production-*)
    PID_FILE="run/$INSTANCE/node-agent.pid"
    if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
        echo "1"
    else
        echo "0"
    fi
    ;;
  *)
    echo "0"
    ;;
esac
