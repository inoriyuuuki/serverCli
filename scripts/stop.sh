#!/usr/bin/env bash
#
# ServerCLI 停止脚本：按 PID 文件优雅停止（SIGTERM → 等待 → SIGKILL 兜底）
#
# 用法：
#   ./scripts/stop.sh --env test --role child --instance test-child-1
#   ./scripts/stop.sh --env production --role primary --confirm-production
#
# 停止顺序：先 node-agent，后控制面；PID 文件随之清理。

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

usage() {
  cat <<'EOF'
用法: ./scripts/stop.sh [选项]

选项:
  --env <test|production>   环境（默认 test）
  --role <primary|child>    节点角色
  --instance <NAME>         实例名
  --confirm-production      正式环境二次确认（production 必填）
  -h, --help                显示帮助
EOF
}

parse_args "$@"
[ -n "$ROLE" ] || die "缺少 --role（primary / child）"
resolve_instance
require_production_confirm

STOP_GRACE_SECONDS="${STOP_GRACE_SECONDS:-20}"
RUN_DIR="$REPO_ROOT/run/$INSTANCE"

stopped=0
failed=0

stop_component() { # stop_component <名称> <PID文件>
  local name="$1" pidfile="$2" pid="" waited=0
  if [ -f "$pidfile" ]; then
    pid="$(read_pid "$pidfile" || true)"
  fi
  if [ -z "$pid" ]; then
    info "$name: 未运行（无有效 PID）"
    rm -f "$pidfile"
    return 0
  fi
  if ! pid_alive "$pid"; then
    info "$name: 进程已不在（pid ${pid}），清理 PID 文件"
    rm -f "$pidfile"
    return 0
  fi
  info "停止 $name (pid $pid): 发送 SIGTERM"
  kill -TERM "$pid" 2>/dev/null || true
  while [ "$waited" -lt "$STOP_GRACE_SECONDS" ] && pid_alive "$pid"; do
    sleep 1
    waited=$((waited+1))
  done
  if pid_alive "$pid"; then
    warn "$name 在 ${STOP_GRACE_SECONDS}s 内未退出，发送 SIGKILL"
    kill -KILL "$pid" 2>/dev/null || true
    sleep 1
  fi
  if pid_alive "$pid"; then
    error "无法停止 $name (pid $pid)"
    failed=1
    return 1
  fi
  rm -f "$pidfile"
  info "$name 已停止"
  stopped=1
  return 0
}

info "停止实例: ${INSTANCE}（$ENV/${ROLE}）"
stop_component "node-agent"  "$RUN_DIR/node-agent.pid"
stop_component "控制面"      "$RUN_DIR/control-plane.pid"

if [ "$failed" -eq 1 ]; then
  die "阶段失败: 存在未能停止的进程（${INSTANCE}）"
fi
if [ "$stopped" -eq 0 ]; then
  info "实例 $INSTANCE 没有在运行的服务"
else
  info "实例 $INSTANCE 已停止 ✔"
fi
