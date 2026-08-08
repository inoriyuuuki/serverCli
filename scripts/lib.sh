#!/usr/bin/env bash
#
# ServerCLI 运维脚本公共库（仅供 scripts/*.sh source 使用，不作为独立脚本运行）
#
# 职责：通用参数解析、环境/Secret 配置加载、路径解析、依赖/端口检查、
#       构建、健康检查、日志与脱敏辅助。
#
# 安全约定：
#   - Secret 只从 <instance>.secrets.env（0600）或运行时环境注入，绝不写入日志/Git/命令行参数。
#   - 本库禁止 set -x；任何输出 Secret 都会被视为事故。
#

# 防止重复 source
if [ -n "${SERVERCLI_LIB_LOADED:-}" ]; then
  return 0 2>/dev/null || exit 0
fi
SERVERCLI_LIB_LOADED=1

# 脚本与仓库根目录（不依赖调用时 cwd）
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 二进制与子命令（可被环境变量覆盖，便于与后端实现对齐）
BIN_DIR="${BIN_DIR:-$REPO_ROOT/bin}"
CONTROL_PLANE_BIN="${CONTROL_PLANE_BIN:-servercli-control-plane}"
NODE_AGENT_BIN="${NODE_AGENT_BIN:-servercli-node-agent}"
MIGRATE_FLAG="${MIGRATE_FLAG:---migrate-only}"
BOOTSTRAP_FLAG="${BOOTSTRAP_FLAG:---bootstrap-admin}"

# 颜色（非 TTY 自动关闭）
if [ -t 1 ] && [ -t 2 ]; then
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_BOLD=$'\033[1m'; C_RESET=$'\033[0m'
else
  C_RED=""; C_GREEN=""; C_YELLOW=""; C_BOLD=""; C_RESET=""
fi

info()  { printf '%s[INFO]%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn()  { printf '%s[WARN]%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
error() { printf '%s[ERROR]%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; }

die() { # die <阶段/信息> —— 失败必须指出阶段并以非零退出
  error "$*"
  exit 1
}

require_cmd() { # require_cmd <name>
  command -v "$1" >/dev/null 2>&1 || die "缺少依赖命令: $1"
}

# ---------------------------------------------------------------------------
# 通用参数解析（各脚本只使用自己关心的字段）
# 解析后可用变量：ENV ROLE INSTANCE CONFIRM_PRODUCTION ALL FOLLOW LINES
#                KEEP_RUNNING SKIP_START REBUILD NO_BUILD NON_INTERACTIVE SKIP_MIGRATE
# ---------------------------------------------------------------------------
parse_args() {
  ENV="${SERVERCLI_ENV:-test}"
  ROLE=""
  INSTANCE=""
  CONFIRM_PRODUCTION=0
  ALL=0
  FOLLOW=0
  LINES=100
  KEEP_RUNNING=0
  SKIP_START=0
  REBUILD=0
  NO_BUILD=0
  NON_INTERACTIVE=0
  SKIP_MIGRATE=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --env)          ENV="${2:?--env 需要一个值}"; shift 2 ;;
      --env=*)        ENV="${1#*=}"; shift ;;
      --role)         ROLE="${2:?--role 需要一个值}"; shift 2 ;;
      --role=*)       ROLE="${1#*=}"; shift ;;
      --instance)     INSTANCE="${2:?--instance 需要一个值}"; shift 2 ;;
      --instance=*)   INSTANCE="${1#*=}"; shift ;;
      --confirm-production) CONFIRM_PRODUCTION=1; shift ;;
      --all)          ALL=1; shift ;;
      --follow|-f)    FOLLOW=1; shift ;;
      --lines)        LINES="${2:?--lines 需要一个值}"; shift 2 ;;
      --lines=*)      LINES="${1#*=}"; shift ;;
      --keep-running) KEEP_RUNNING=1; shift ;;
      --skip-start)   SKIP_START=1; shift ;;
      --rebuild)      REBUILD=1; shift ;;
      --no-build)     NO_BUILD=1; shift ;;
      --skip-migrate) SKIP_MIGRATE=1; shift ;;
      --non-interactive) NON_INTERACTIVE=1; shift ;;
      -h|--help)      usage; exit 0 ;;
      *) die "未知参数: ${1}（运行 --help 查看用法）" ;;
    esac
  done
  case "$ENV" in test|production) ;; *) die "非法环境: ${ENV}（仅支持 test / production）" ;; esac
  case "$ROLE" in ""|primary|child) ;; *) die "非法角色: ${ROLE}（仅支持 primary / child）" ;; esac
}

# ---------------------------------------------------------------------------
# 实例名解析与校验
# ---------------------------------------------------------------------------
resolve_instance() {
  if [ -z "$INSTANCE" ]; then
    case "$ENV/$ROLE" in
      test/primary)       INSTANCE="test-primary" ;;
      test/child)         INSTANCE="test-child-1" ;;
      production/primary) INSTANCE="production-primary" ;;
      production/child)   die "production child 必须显式指定 --instance" ;;
      *)                  die "缺少 --role（primary / child）" ;;
    esac
  fi
  case "$INSTANCE" in
    ''|*/*|*..*|*[[:space:]]*) die "非法实例名: ${INSTANCE}（仅允许字母数字、点、下划线、连字符）" ;;
  esac
}

# 正式环境必须二次确认
require_production_confirm() {
  if [ "$ENV" = "production" ] && [ "$CONFIRM_PRODUCTION" -ne 1 ]; then
    die "正式环境操作必须显式确认：请加上 --env production --confirm-production"
  fi
}

# 取监听地址中的端口（兼容 host:port / [v6]:port）
addr_port() { # addr_port <addr> [默认端口]
  local addr="$1" def="${2:-}"
  case "$addr" in
    *:*) printf '%s' "${addr##*:}" ;;
    *)   printf '%s' "${addr:-$def}" ;;
  esac
}

# ---------------------------------------------------------------------------
# 环境配置加载
#   - deploy/environments/<env>/<instance>.env           （非 Secret）
#   - deploy/environments/<env>/<instance>.secrets.env   （0600）
# 生成规则：test 环境缺失时允许从 .example 生成；production 必须手工提供。
# 加载后导出全部合法变量（跳过 shell 关键变量，避免污染脚本环境）。
# ---------------------------------------------------------------------------
CONFIG_DENY="PATH HOME SHELL USER LOGNAME PWD OLDPWD TERM _ SHLVL IFS"

load_env_file() { # load_env_file <file>
  local file="$1" line key val d
  [ -r "$file" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    # 去尾部空白
    line="${line%"${line##*[![:space:]]}"}"
    case "$line" in ''|\#*) continue ;; esac
    key="${line%%=*}"
    val="${line#*=}"
    key="$(printf '%s' "$key" | tr -d '[:space:]')"
    case "$key" in [A-Za-z_][A-Za-z0-9_]*) ;; *) continue ;; esac
    # 去除首尾引号
    val="${val#\"}"; val="${val%\"}"
    val="${val#\'}"; val="${val%\'}"
    for d in $CONFIG_DENY; do
      [ "$key" = "$d" ] && continue 2
    done
    export "$key=$val"
  done < "$file"
}

# 从 .env 文件读取单个键（只读，无副作用；status.sh 等使用）
get_cfg() { # get_cfg <file> <key> [默认值]
  local file="$1" key="$2" def="${3:-}" v=""
  if [ -f "$file" ]; then
    v="$(sed -n "s/^[[:space:]]*${key}=//p" "$file" | head -n 1 | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//" -e 's/[[:space:]]*$//')"
  fi
  printf '%s' "${v:-$def}"
}

# 应用契约默认值并导出
apply_defaults() {
  : "${APP_ENV:=$ENV}"
  : "${INSTANCE_NAME:=$INSTANCE}"
  : "${NODE_ROLE:=$ROLE}"
  : "${PRIMARY_SERVER_IP:=127.0.0.1}"
  : "${FRONTEND_ADDR:=0.0.0.0:9044}"
  : "${BACKEND_ADDR:=0.0.0.0:9045}"
  : "${DATABASE_DRIVER:=sqlite}"
  : "${AGENT_STATE_DIR:=$REPO_ROOT/state/$INSTANCE}"
  : "${LOG_DIR:=$REPO_ROOT/logs/$INSTANCE}"
  : "${COMMANDS_DIR:=$REPO_ROOT/commands}"
  : "${AI_LEASE_DEFAULT_MINUTES:=60}"
  : "${AI_LEASE_MAX_HOURS:=24}"
  : "${AI_LEASE_DISCONNECT_GRACE_SECONDS:=60}"
  : "${RETENTION_DAYS:=7}"
  : "${CLEANUP_SCHEDULE:=weekly}"
  : "${HEARTBEAT_INTERVAL_SECONDS:=30}"
  : "${OFFLINE_THRESHOLD_SECONDS:=90}"
  : "${TASK_POLL_TIMEOUT_SECONDS:=25}"
  : "${LOG_LEVEL:=info}"
  : "${HTTP_INSECURE_SKIP_VERIFY:=false}"
  : "${FRONTEND_DIST_DIR:=$REPO_ROOT/frontend/dist}"
  : "${ADMIN_USERNAME:=admin}"
  : "${HEALTH_TIMEOUT_SECONDS:=60}"
  : "${STOP_GRACE_SECONDS:=20}"
  if [ -z "${PRIMARY_BACKEND_URL:-}" ]; then
    PRIMARY_BACKEND_URL="http://127.0.0.1:$(addr_port "$BACKEND_ADDR" 9045)"
  fi
  : "${AUTHORIZED_KEYS_FILE:=$AGENT_STATE_DIR/authorized_keys}"
  : "${LEASE_SHELL_BIN:=$AGENT_STATE_DIR/bin/servercli-lease-shell}"
  export APP_ENV INSTANCE_NAME NODE_ROLE PRIMARY_SERVER_IP PRIMARY_BACKEND_URL \
         FRONTEND_ADDR BACKEND_ADDR DATABASE_DRIVER DATABASE_URL \
         AGENT_STATE_DIR LOG_DIR COMMANDS_DIR \
         AI_LEASE_DEFAULT_MINUTES AI_LEASE_MAX_HOURS AI_LEASE_DISCONNECT_GRACE_SECONDS \
         RETENTION_DAYS CLEANUP_SCHEDULE HEARTBEAT_INTERVAL_SECONDS \
         OFFLINE_THRESHOLD_SECONDS TASK_POLL_TIMEOUT_SECONDS \
         AUTHORIZED_KEYS_FILE LEASE_SHELL_BIN HTTP_INSECURE_SKIP_VERIFY \
         LOG_LEVEL FRONTEND_DIST_DIR ADMIN_USERNAME HEALTH_TIMEOUT_SECONDS STOP_GRACE_SECONDS
}

# 将相对路径按仓库根解析为绝对路径
resolve_one_path() { # resolve_one_path <VARNAME>
  local var="$1" val
  eval "val=\${$var:-}"
  case "$val" in
    /*|'') return ;;
    *) eval "$var=\"\$REPO_ROOT/\$val\"" ;;
  esac
}

resolve_paths() {
  local v
  for v in AGENT_STATE_DIR LOG_DIR COMMANDS_DIR FRONTEND_DIST_DIR AUTHORIZED_KEYS_FILE LEASE_SHELL_BIN; do
    resolve_one_path "$v"
  done
  # SQLite DATABASE_URL=file:<相对路径> 也按仓库根解析
  case "${DATABASE_URL:-}" in
    file:/*|''|postgres*|sqlite://*) ;;
    file:*) DATABASE_URL="file:$REPO_ROOT/${DATABASE_URL#file:}" ;;
  esac
  # SQLite 默认数据库文件
  if [ -z "${DATABASE_URL:-}" ]; then
    if [ "$DATABASE_DRIVER" = "sqlite" ]; then
      DATABASE_URL="file:$AGENT_STATE_DIR/servercli.db"
    fi
  fi
  export DATABASE_URL
}

# 加载实例配置；CONFIG_AUTOGENERATE=0 时缺失配置只报错不生成（status.sh 使用）
load_instance_config() {
  CONFIG_AUTOGENERATE="${CONFIG_AUTOGENERATE:-1}"
  ENV_DIR="$REPO_ROOT/deploy/environments/$ENV"
  ENV_FILE="$ENV_DIR/$INSTANCE.env"
  SECRETS_FILE="$ENV_DIR/$INSTANCE.secrets.env"

  if [ ! -f "$ENV_FILE" ]; then
    if [ "$CONFIG_AUTOGENERATE" = "1" ] && [ "$ENV" = "test" ] && [ -f "$ENV_FILE.example" ]; then
      cp "$ENV_FILE.example" "$ENV_FILE"
      info "未找到 ${ENV_FILE}，已从 .example 生成（可按需修改）"
    else
      die "缺少配置文件: ${ENV_FILE}（可从 $ENV_FILE.example 复制生成）"
    fi
  fi
  if [ ! -f "$SECRETS_FILE" ]; then
    if [ "$CONFIG_AUTOGENERATE" = "1" ] && [ "$ENV" = "test" ] && [ -f "$SECRETS_FILE.example" ]; then
      cp "$SECRETS_FILE.example" "$SECRETS_FILE"
      chmod 600 "$SECRETS_FILE"
      info "未找到 ${SECRETS_FILE}，已从 .example 生成空占位（0600）"
    else
      die "缺少 Secret 文件: ${SECRETS_FILE}（必须由运维在部署机手工创建，权限 0600）"
    fi
  fi

  # 安全基线：环境目录 0700、Secret 文件 0600
  if [ -d "$ENV_DIR" ]; then chmod 700 "$ENV_DIR"; fi
  if [ -f "$SECRETS_FILE" ]; then chmod 600 "$SECRETS_FILE"; fi

  load_env_file "$ENV_FILE"
  load_env_file "$SECRETS_FILE"

  # 配置与命令行一致性校验
  if [ -n "${APP_ENV:-}" ] && [ "$APP_ENV" != "$ENV" ]; then
    die "配置 APP_ENV=$APP_ENV 与 --env $ENV 不一致"
  fi
  if [ -n "${INSTANCE_NAME:-}" ] && [ "$INSTANCE_NAME" != "$INSTANCE" ]; then
    die "配置 INSTANCE_NAME=$INSTANCE_NAME 与 --instance $INSTANCE 不一致"
  fi
  if [ -n "${NODE_ROLE:-}" ] && [ "$NODE_ROLE" != "$ROLE" ]; then
    die "配置 NODE_ROLE=$NODE_ROLE 与 --role $ROLE 不一致"
  fi

  apply_defaults
  resolve_paths

  RUN_DIR="$REPO_ROOT/run/$INSTANCE"
  STATE_DIR="$AGENT_STATE_DIR"
  export RUN_DIR STATE_DIR
}

# ---------------------------------------------------------------------------
# 进程 / 端口 / 健康检查
# ---------------------------------------------------------------------------
pid_alive() { # pid_alive <pid>
  local pid="$1"
  [ -n "$pid" ] || return 1
  kill -0 "$pid" 2>/dev/null
}

read_pid() { # read_pid <file> —— 输出数字 PID，失败返回 1
  local f="$1" pid=""
  [ -f "$f" ] && pid="$(tr -d '[:space:]' < "$f" 2>/dev/null)"
  case "$pid" in ''|*[!0-9]*) return 1 ;; esac
  printf '%s' "$pid"
}

port_in_use() { # port_in_use <port>
  local port="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1 && return 0
  fi
  if command -v ss >/dev/null 2>&1; then
    ss -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "(^|[.:])${port}\$" && return 0
  fi
  if command -v netstat >/dev/null 2>&1; then
    netstat -an 2>/dev/null | awk '{print $4}' | grep -Eq "(^|[.:])${port}\$" && return 0
  fi
  return 1
}

require_port_free() { # require_port_free <port> <用途说明>
  local port="$1" what="$2"
  if [ -n "$port" ] && port_in_use "$port"; then
    die "端口检查失败: 端口 ${port}（${what}）已被占用，请先停止占用进程或调整配置"
  fi
}

wait_health() { # wait_health <port> <path> <超时秒> <名称>
  local port="$1" path="$2" timeout="${3:-$HEALTH_TIMEOUT_SECONDS}" name="$4"
  local i=0 url="http://127.0.0.1:$port$path"
  while [ "$i" -lt "$timeout" ]; do
    if curl -fsS --max-time 3 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
    i=$((i+1))
  done
  return 1
}

# ---------------------------------------------------------------------------
# 构建
# ---------------------------------------------------------------------------
build_backend() {
  info "阶段: 构建后端二进制到 $BIN_DIR"
  [ -d "$REPO_ROOT/backend" ] || die "阶段失败: 缺少 backend/ 源码目录（由 backend 组件提供）"
  [ -d "$REPO_ROOT/backend/cmd/control-plane" ] || die "阶段失败: 缺少 backend/cmd/control-plane"
  [ -d "$REPO_ROOT/backend/cmd/node-agent" ]    || die "阶段失败: 缺少 backend/cmd/node-agent"
  require_cmd go
  mkdir -p "$BIN_DIR"
  ( cd "$REPO_ROOT/backend" \
      && go build -o "$BIN_DIR/$CONTROL_PLANE_BIN" ./cmd/control-plane \
      && go build -o "$BIN_DIR/$NODE_AGENT_BIN" ./cmd/node-agent ) \
    || die "阶段失败: backend 构建失败（go build）"
  info "后端构建完成: $BIN_DIR/$CONTROL_PLANE_BIN, $BIN_DIR/$NODE_AGENT_BIN"
}

build_frontend() {
  if [ "$NO_BUILD" -eq 1 ]; then
    [ -f "$FRONTEND_DIST_DIR/index.html" ] \
      || die "阶段失败: 缺少前端静态资源且指定了 --no-build: $FRONTEND_DIST_DIR"
    info "阶段: 使用已存在前端静态资源（--no-build）"
    return 0
  fi
  info "阶段: 构建前端静态资源到 $FRONTEND_DIST_DIR"
  if [ ! -f "$REPO_ROOT/frontend/package.json" ]; then
    if [ "$ENV" = "production" ]; then
      die "阶段失败: 正式环境缺少 frontend/package.json，无法构建前端"
    fi
    warn "frontend/package.json 不存在（并行开发中），跳过前端构建"
    return 0
  fi
  require_cmd npm
  ( cd "$REPO_ROOT/frontend" \
      && { npm ci --no-audit --no-fund 2>/dev/null || npm install --no-audit --no-fund; } \
      && npm run build ) \
    || die "阶段失败: frontend 构建失败（npm run build）"
  info "前端构建完成: $FRONTEND_DIST_DIR"
}

ensure_control_plane_binary() { # 不存在时按需构建；--no-build 则直接失败
  if [ -x "$BIN_DIR/$CONTROL_PLANE_BIN" ]; then
    return 0
  fi
  if [ "$NO_BUILD" -eq 1 ] || [ "$REBUILD" -eq 0 ]; then
    if [ "$NO_BUILD" -eq 1 ]; then
      die "阶段失败: 二进制不存在且指定了 --no-build: $BIN_DIR/$CONTROL_PLANE_BIN"
    fi
  fi
  build_backend
}

# ---------------------------------------------------------------------------
# 脱敏（供报告输出使用）
# ---------------------------------------------------------------------------
redact() { # redact <text>
  printf '%s' "$1" | python3 -c '
import sys, re
s = sys.stdin.read()
s = re.sub(r"(\"(?:password|passwd|secret|token|credential|api[_-]?key)\"\s*:\s*\")[^\"]*", r"\1***", s, flags=re.I)
print(s.strip())
' 2>/dev/null || printf '%s' "$1"
}
