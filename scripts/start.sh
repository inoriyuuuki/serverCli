#!/usr/bin/env bash
#
# ServerCLI 一键启动脚本（从源码构建 + 迁移 + 管理员初始化 + 启动 + 健康检查）
#
# 用法：
#   ./scripts/start.sh --env test --role primary [--instance test-primary]
#   ./scripts/start.sh --env test --role child --instance test-child-1
#   ./scripts/start.sh --env production --role primary --confirm-production
#
# 流程：参数与依赖/端口检查 → 加载配置 → 构建前端 → 构建后端 →
#       迁移（migrate.sh）→ 初始化/验证管理员（bootstrap-admin.sh）→
#       创建 state/log/run 目录（0700）→ 启动控制面 + node-agent →
#       健康检查（/health/live、/health/ready、/version）→ 输出环境/版本/访问地址。
# 失败时以非零退出并指出失败阶段；所有报告脱敏，不输出 Secret。

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

usage() {
  cat <<'EOF'
用法: ./scripts/start.sh [选项]

选项:
  --env <test|production>      环境（默认 test）
  --role <primary|child>       节点角色（必填）
  --instance <NAME>            实例名（默认 test-primary / test-child-1 / production-primary）
  --confirm-production         正式环境二次确认（production 必填）
  --skip-migrate               跳过数据库迁移（默认执行）
  --no-build                   使用已存在的二进制，不重新构建
  -h, --help                   显示帮助

示例:
  ./scripts/start.sh --env test --role primary
  ./scripts/start.sh --env test --role child --instance test-child-1
  ./scripts/start.sh --env production --role primary --confirm-production
EOF
}

parse_args "$@"
[ -n "$ROLE" ] || die "缺少 --role（primary / child）"
resolve_instance
require_production_confirm
load_instance_config

# ---------------------------------------------------------------------------
# 阶段 1：参数与依赖检查（go、node/npm、端口占用）
# ---------------------------------------------------------------------------
info "阶段 1/9: 参数与依赖检查"
case "$ENV/$ROLE" in
  production/child) die "暂不支持 production child 一键启动（正式子节点请单独部署 node-agent）" ;;
esac
if [ "$NO_BUILD" -eq 0 ]; then
  require_cmd go
  if command -v node >/dev/null 2>&1 || command -v npm >/dev/null 2>&1; then
    :
  else
    die "依赖检查失败: 缺少 node/npm（构建前端需要）"
  fi
else
  info "--no-build：跳过 Go/Node 依赖检查，使用已存在产物"
fi
command -v curl >/dev/null 2>&1 || warn "缺少 curl，健康检查将受限（建议安装）"
BACKEND_PORT="$(addr_port "$BACKEND_ADDR" 9045)"
FRONTEND_PORT="$(addr_port "$FRONTEND_ADDR" 9044)"
require_port_free "$BACKEND_PORT" "后端 backend"
require_port_free "$FRONTEND_PORT" "前端 frontend"

# 已运行则幂等退出（提示使用 restart）
if [ -f "$RUN_DIR/control-plane.pid" ] && pid_alive "$(read_pid "$RUN_DIR/control-plane.pid" || true)"; then
  info "实例 $INSTANCE 的控制面已在运行（pid $(read_pid "$RUN_DIR/control-plane.pid")），无需重复启动；如需重启请运行 ./scripts/restart.sh"
  exit 0
fi

# ---------------------------------------------------------------------------
# 阶段 2：加载配置（Secret 只来自 0600 secrets 文件，不回显）
# ---------------------------------------------------------------------------
info "阶段 2/9: 加载配置 $ENV_DIR/$INSTANCE.env + $INSTANCE.secrets.env"
info "配置摘要: 角色=$NODE_ROLE 环境=$APP_ENV 前端=$FRONTEND_ADDR 后端=$BACKEND_ADDR 数据库=$DATABASE_DRIVER"

# ---------------------------------------------------------------------------
# 阶段 3：构建前端
# ---------------------------------------------------------------------------
build_frontend

# ---------------------------------------------------------------------------
# 阶段 4：构建后端（两个二进制到 bin/）
# ---------------------------------------------------------------------------
if [ "$NO_BUILD" -eq 1 ]; then
  [ -x "$BIN_DIR/$CONTROL_PLANE_BIN" ] || die "阶段失败: 二进制不存在且指定了 --no-build: $BIN_DIR/$CONTROL_PLANE_BIN"
  [ -x "$BIN_DIR/$NODE_AGENT_BIN" ]     || die "阶段失败: 二进制不存在且指定了 --no-build: $BIN_DIR/$NODE_AGENT_BIN"
  info "阶段 4/9: 使用已存在二进制（--no-build）"
else
  build_backend
fi

# ---------------------------------------------------------------------------
# 阶段 5：迁移（幂等）
# ---------------------------------------------------------------------------
if [ "$SKIP_MIGRATE" -eq 0 ]; then
  CONFIRM_ARGS=""
  [ "$CONFIRM_PRODUCTION" -eq 1 ] && CONFIRM_ARGS="--confirm-production"
  "$SCRIPT_DIR/migrate.sh" --env "$ENV" --role "$ROLE" --instance "$INSTANCE" $CONFIRM_ARGS --no-build
else
  warn "阶段 5/9: 已跳过数据库迁移（--skip-migrate）"
fi

# ---------------------------------------------------------------------------
# 阶段 6：初始化/验证管理员（幂等；无密码且非交互时测试环境跳过）
# ---------------------------------------------------------------------------
CONFIRM_ARGS=""
[ "$CONFIRM_PRODUCTION" -eq 1 ] && CONFIRM_ARGS="--confirm-production"
"$SCRIPT_DIR/bootstrap-admin.sh" --env "$ENV" --role "$ROLE" --instance "$INSTANCE" $CONFIRM_ARGS --no-build

# ---------------------------------------------------------------------------
# 阶段 7：创建 state/log/run 目录（0700）
# ---------------------------------------------------------------------------
info "阶段 7/9: 创建 state/log/run 目录（权限 0700）"
mkdir -p "$STATE_DIR" "$LOG_DIR" "$RUN_DIR"
chmod 700 "$STATE_DIR" "$LOG_DIR" "$RUN_DIR"

# ---------------------------------------------------------------------------
# 阶段 8：启动控制面与 node-agent
# ---------------------------------------------------------------------------
info "阶段 8/9: 启动服务"

start_control_plane() {
  if [ -f "$RUN_DIR/control-plane.pid" ] && pid_alive "$(read_pid "$RUN_DIR/control-plane.pid" || true)"; then
    info "控制面已在运行，跳过启动"
    return 0
  fi
  (
    cd "$REPO_ROOT"
    nohup "$BIN_DIR/$CONTROL_PLANE_BIN" >>"$LOG_DIR/control-plane.log" 2>&1 &
    echo $! > "$RUN_DIR/control-plane.pid"
  )
  local pid
  pid="$(read_pid "$RUN_DIR/control-plane.pid" || true)"
  info "控制面已启动 (pid ${pid:-unknown})，日志: $LOG_DIR/control-plane.log"
}

start_node_agent() {
  if [ "${START_NODE_AGENT:-1}" != "1" ]; then
    warn "START_NODE_AGENT!=1，跳过 node-agent"
    return 0
  fi
  if [ -f "$RUN_DIR/node-agent.pid" ] && pid_alive "$(read_pid "$RUN_DIR/node-agent.pid" || true)"; then
    info "node-agent 已在运行，跳过启动"
    return 0
  fi
  (
    cd "$REPO_ROOT"
    # node-agent 不需要管理员 Secret，避免额外进程持有
    unset ADMIN_INITIAL_PASSWORD ADMIN_INITIAL_PASSWORD_FILE 2>/dev/null || true
    nohup "$BIN_DIR/$NODE_AGENT_BIN" >>"$LOG_DIR/node-agent.log" 2>&1 &
    echo $! > "$RUN_DIR/node-agent.pid"
  )
  local pid
  pid="$(read_pid "$RUN_DIR/node-agent.pid" || true)"
  info "node-agent 已启动 (pid ${pid:-unknown})，日志: $LOG_DIR/node-agent.log"
}

start_control_plane
start_node_agent

# ---------------------------------------------------------------------------
# 阶段 9：健康检查（/health/live、/health/ready、/version）
# ---------------------------------------------------------------------------
info "阶段 9/9: 健康检查 http://127.0.0.1:$BACKEND_PORT"
wait_health "$BACKEND_PORT" "/health/live"  "$HEALTH_TIMEOUT_SECONDS" "控制面存活" \
  || die "阶段失败: 控制面 /health/live 未就绪（日志: $LOG_DIR/control-plane.log）"
wait_health "$BACKEND_PORT" "/health/ready" "$HEALTH_TIMEOUT_SECONDS" "控制面就绪" \
  || die "阶段失败: 控制面 /health/ready 未就绪（数据库/迁移/后台任务异常，日志: $LOG_DIR/control-plane.log）"

VERSION_JSON="$(curl -fsS --max-time 5 "http://127.0.0.1:$BACKEND_PORT/version" 2>/dev/null || true)"
VERSION_TEXT=""
if [ -n "$VERSION_JSON" ]; then
  if command -v python3 >/dev/null 2>&1; then
    VERSION_TEXT="$(printf '%s' "$VERSION_JSON" | python3 -c '
import json,sys
try:
    d=json.load(sys.stdin)
    parts=[str(d.get(k)) for k in ("version","build_time","commit") if d.get(k)]
    print(" ".join(parts))
except Exception:
    sys.stdout.write("")
' 2>/dev/null || true)"
  fi
  [ -n "$VERSION_TEXT" ] || VERSION_TEXT="$VERSION_JSON"
fi

# 主 Agent 心跳仅能通过控制面 API 确认（需登录）；此处校验进程存活
if [ "$NODE_ROLE" = "primary" ]; then
  sleep 2
  AGENT_PID="$(read_pid "$RUN_DIR/node-agent.pid" || true)"
  if [ -n "$AGENT_PID" ] && pid_alive "$AGENT_PID"; then
    info "主 node-agent 进程存活 (pid $AGENT_PID)"
  else
    warn "主 node-agent 未检测到存活进程，请查看 $LOG_DIR/node-agent.log"
  fi
fi

echo
info "启动成功 ✔"
echo "  环境        : $APP_ENV"
echo "  实例        : $INSTANCE_NAME ($NODE_ROLE)"
echo "  数据库      : $DATABASE_DRIVER"
echo "  版本        : ${VERSION_TEXT:-<unknown>}"
echo "  前端访问    : http://${PRIMARY_SERVER_IP}:${FRONTEND_PORT}"
echo "  后端 API    : http://${PRIMARY_SERVER_IP}:${BACKEND_PORT}"
echo "  控制面 PID  : $(read_pid "$RUN_DIR/control-plane.pid" 2>/dev/null || echo n/a)"
echo "  node-agent  : $(read_pid "$RUN_DIR/node-agent.pid" 2>/dev/null || echo n/a)"
echo "  日志目录    : $LOG_DIR"
echo "  状态/运行   : $STATE_DIR / $RUN_DIR"
echo "  提示        : Secret 不写入日志与仓库；管理员密码见 deploy/environments/$ENV/$INSTANCE.secrets.env"
