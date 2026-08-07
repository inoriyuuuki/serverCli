#!/usr/bin/env bash
#
# ServerCLI 状态检查：输出实例 PID / 健康 / 端口 / 数据库类型
#
# 用法：
#   ./scripts/status.sh --env test --all
#   ./scripts/status.sh --env test --role primary
#   ./scripts/status.sh --env production --role primary
#
# 只读脚本，不需要 --confirm-production；正式环境会显示醒目提示。

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

usage() {
  cat <<'EOF'
用法: ./scripts/status.sh [选项]

选项:
  --env <test|production>   环境（默认 test）
  --role <primary|child>    节点角色（与 --instance 二选一）
  --instance <NAME>         指定实例
  --all                     列出该环境全部实例
  -h, --help                显示帮助
EOF
}

parse_args "$@"

# 收集要检查的实例
instances=()
if [ "$ALL" -eq 1 ]; then
  # 优先读取实际存在的 *.env 配置；没有则回退到已知默认
  map_ok=0
  for f in "$REPO_ROOT"/deploy/environments/"$ENV"/*.env; do
    if [ -f "$f" ]; then
      instances+=("$(basename "$f" .env)")
      map_ok=1
    fi
  done
  if [ "$map_ok" -eq 0 ]; then
    case "$ENV" in
      test)       instances=(test-primary test-child-1) ;;
      production) instances=(production-primary) ;;
    esac
  fi
else
  [ -n "$ROLE" ] || ROLE="primary"
  resolve_instance
  instances=("$INSTANCE")
fi

[ "${#instances[@]}" -gt 0 ] || die "没有可检查的实例（${ENV}）"

if [ "$ENV" = "production" ]; then
  echo "${C_RED}${C_BOLD}========== 正式环境 (PRODUCTION) ==========${C_RESET}"
fi

printf '%-18s %-9s %-14s %-8s %-8s %-9s %-13s %s\n' \
  "实例" "角色" "PID(控制面)" "前端" "后端" "数据库" "健康(live/ready)" "状态"
printf '%s\n' "----------------------------------------------------------------------------------------------"

failed=0
for inst in "${instances[@]}"; do
  ENV_DIR="$REPO_ROOT/deploy/environments/$ENV"
  ENV_FILE="$ENV_DIR/$inst.env"
  RUN_DIR="$REPO_ROOT/run/$inst"

  role="$(get_cfg "$ENV_FILE" NODE_ROLE "$([ "$inst" = "test-child-1" ] && echo child || echo primary)")"
  front_addr="$(get_cfg "$ENV_FILE" FRONTEND_ADDR "")"
  back_addr="$(get_cfg "$ENV_FILE" BACKEND_ADDR "")"
  db="$(get_cfg "$ENV_FILE" DATABASE_DRIVER "sqlite")"
  front_port="$(addr_port "$front_addr" "")"
  back_port="$(addr_port "$back_addr" "")"
  [ -n "$back_port" ] || back_port="$([ "$ENV" = "production" ] && echo 9043 || { [ "$inst" = "test-child-1" ] && echo 9047 || echo 9045; })"
  [ -n "$front_port" ] || front_port="$([ "$ENV" = "production" ] && echo 9042 || { [ "$inst" = "test-child-1" ] && echo 9046 || echo 9044; })"

  cp_pid="$(read_pid "$RUN_DIR/control-plane.pid" || true)"
  ag_pid="$(read_pid "$RUN_DIR/node-agent.pid" || true)"
  cp_alive=0; ag_alive=0
  [ -n "$cp_pid" ] && pid_alive "$cp_pid" && cp_alive=1
  [ -n "$ag_pid" ] && pid_alive "$ag_pid" && ag_alive=1

  live="-"; ready="-"; state="stopped"
  if [ "$cp_alive" -eq 1 ]; then
    curl -fsS --max-time 3 "http://127.0.0.1:$back_port/health/live"  >/dev/null 2>&1 && live="ok" || live="FAIL"
    curl -fsS --max-time 3 "http://127.0.0.1:$back_port/health/ready" >/dev/null 2>&1 && ready="ok" || ready="FAIL"
    if [ "$live" = "ok" ] && [ "$ready" = "ok" ]; then state="running"; else state="degraded"; failed=1; fi
  else
    [ -n "$cp_pid" ] && state="dead-pid"
  fi

  pid_text="${cp_pid:--}/${ag_pid:--}"
  printf '%-18s %-9s %-14s %-8s %-8s %-9s %-13s %s\n' \
    "$inst" "$role" "$pid_text" "$front_port" "$back_port" "$db" "$live/$ready" "$state"
done

echo
if [ "$failed" -eq 1 ]; then
  warn "存在健康/状态异常的实例"
  exit 1
fi
info "状态检查完成"
