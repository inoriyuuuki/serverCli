#!/usr/bin/env bash
#
# ServerCLI 停止脚本：按 PID 文件优雅停止（SIGTERM → 等待 → SIGKILL 兜底）
#
# 用法：
#   ./scripts/stop.sh                          # 不带参数：停止全部已启动的实例（test + production）
#   ./scripts/stop.sh --confirm-production     # 同上（含正式环境时需确认）
#   ./scripts/stop.sh --env production         # 只停止 production 环境的全部实例
#   ./scripts/stop.sh --env test --role child --instance test-child-1   # 只停指定实例
#
# 停止顺序：每个实例先 node-agent，后控制面；PID 文件随之清理。
# 全部模式下扫描 run/<实例>/ 下的 PID 文件（即 start.sh 启动过的实例）。

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

usage() {
  cat <<'HELP'
用法: ./scripts/stop.sh [选项]

不带任何参数（或 --all）时，停止全部已启动的实例（test + production）。
加 --env 可只停止指定环境的全部实例；加 --role/--instance 只停止指定实例。

选项:
  --all                     停止全部实例（与不带参数等价）
  --env <test|production>   只停止该环境的全部实例（默认 test）
  --role <primary|child>    与 --instance 配合只停单个实例
  --instance <NAME>         实例名（需同时指定 --role）
  --confirm-production      正式环境二次确认（待停止实例含 production 时必须）
  -h, --help                显示帮助
HELP
}

# 模式判定：出现 --role/--instance 视为单实例模式；否则为全部模式（--all 与无参数等价）
SINGLE_MODE=0
ENV_GIVEN=0
for a in "$@"; do
  case "$a" in
    --role|--role=*|--instance|--instance=*) SINGLE_MODE=1 ;;
    --env|--env=*)                      ENV_GIVEN=1 ;;
  esac
done

parse_args "$@"

STOP_GRACE_SECONDS="${STOP_GRACE_SECONDS:-20}"
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

# ---------------------------------------------------------------------------
# 收集要停止的实例
#   - 全部模式：扫描 run/<实例>/ 下所有 PID 文件（start.sh 启动过的实例）；
#     指定 --env 时只收集该环境的实例
#   - 单实例模式：--role/--instance 指定
# ---------------------------------------------------------------------------
instances=()
if [ "$SINGLE_MODE" -eq 1 ]; then
  [ -n "$ROLE" ] || die "缺少 --role（primary / child）"
  resolve_instance
  instances=("$ENV:$INSTANCE")
else
  for pidfile in "$REPO_ROOT"/run/*/*.pid; do
    [ -f "$pidfile" ] || continue
    inst="$(basename "$(dirname "$pidfile")")"
    env="test"
    for f in "$REPO_ROOT"/deploy/environments/*/"$inst.env" \
             "$REPO_ROOT"/deploy/environments/*/"$inst.env.example"; do
      [ -f "$f" ] && { env="$(basename "$(dirname "$f")")"; break; }
    done
    if [ "$ENV_GIVEN" -eq 1 ] && [ "$env" != "$ENV" ]; then
      continue
    fi
    seen=0
    for spec in "${instances[@]+"${instances[@]}"}"; do
      if [ "${spec#*:}" = "$inst" ]; then seen=1; break; fi
    done
    [ "$seen" -eq 0 ] && instances+=("$env:$inst")
  done
fi

if [ "${#instances[@]}" -eq 0 ]; then
  info "没有运行中的实例需要停止"
  exit 0
fi

# 待停止实例包含 production 时必须显式确认
has_production=0
for spec in "${instances[@]}"; do
  [ "${spec%%:*}" = "production" ] && has_production=1
done
if [ "$has_production" -eq 1 ] && [ "$CONFIRM_PRODUCTION" -ne 1 ]; then
  die "待停止实例包含正式环境（production），必须显式确认：请加上 --confirm-production"
fi

# 逐实例停止（先 node-agent，后控制面）
for spec in "${instances[@]}"; do
  env="${spec%%:*}"
  inst="${spec#*:}"
  role="$(get_cfg "$REPO_ROOT/deploy/environments/$env/$inst.env" NODE_ROLE "primary")"
  RUN_DIR="$REPO_ROOT/run/$inst"
  info "停止实例: ${inst}（${env}/${role}）"
  stop_component "node-agent"  "$RUN_DIR/node-agent.pid"
  stop_component "控制面"      "$RUN_DIR/control-plane.pid"
done

if [ "$failed" -eq 1 ]; then
  die "阶段失败: 存在未能停止的进程"
fi
if [ "$stopped" -eq 0 ]; then
  info "没有运行中的服务需要停止"
else
  info "全部实例已停止 ✔"
fi
