#!/usr/bin/env bash
#
# ServerCLI 管理员初始化脚本：交互式或从环境注入创建/验证管理员
#
# 用法：
#   ./scripts/bootstrap-admin.sh --env test --role primary
#   ./scripts/bootstrap-admin.sh --env production --role primary --confirm-production
#
# Secret 约定：
#   - 密码只通过 ADMIN_INITIAL_PASSWORD / ADMIN_INITIAL_PASSWORD_FILE（0600 文件）运行时注入，
#     或交互式输入；绝不作为命令行参数出现、绝不写入日志。
#   - 幂等：已存在管理员时后端验证并跳过创建。

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

usage() {
  cat <<'EOF'
用法: ./scripts/bootstrap-admin.sh [选项]

选项:
  --env <test|production>   环境（默认 test）
  --role <primary|child>    节点角色（必填）
  --instance <NAME>         实例名
  --confirm-production      正式环境二次确认（production 必填）
  --non-interactive         非交互模式（无密码时正式环境直接失败）
  --no-build                使用已存在二进制，不构建
  -h, --help                显示帮助

密码注入优先级:
  1) ADMIN_INITIAL_PASSWORD_FILE（0600 文件，由 secrets 或环境提供）
  2) ADMIN_INITIAL_PASSWORD（secrets 运行时值）
  3) 交互式输入（TTY）
EOF
}

parse_args "$@"
[ -n "$ROLE" ] || die "缺少 --role（primary / child）"
resolve_instance
require_production_confirm
load_instance_config

# ---------------------------------------------------------------------------
# 阶段 1：确保二进制
# ---------------------------------------------------------------------------
if [ -x "$BIN_DIR/$CONTROL_PLANE_BIN" ]; then
  :
elif [ "$NO_BUILD" -eq 1 ]; then
  die "阶段失败: 二进制不存在且指定了 --no-build: $BIN_DIR/$CONTROL_PLANE_BIN"
else
  build_backend
fi

# ---------------------------------------------------------------------------
# 阶段 2：获取管理员初始密码（运行时注入，不落盘不回显）
# ---------------------------------------------------------------------------
obtain_password() {
  if [ -n "${ADMIN_INITIAL_PASSWORD_FILE:-}" ] && [ -r "$ADMIN_INITIAL_PASSWORD_FILE" ]; then
    chmod 600 "$ADMIN_INITIAL_PASSWORD_FILE" 2>/dev/null || true
    info "使用 ADMIN_INITIAL_PASSWORD_FILE 注入初始密码（文件: ${ADMIN_INITIAL_PASSWORD_FILE}）"
    return 0
  fi
  if [ -n "${ADMIN_INITIAL_PASSWORD:-}" ]; then
    info "使用 ADMIN_INITIAL_PASSWORD（secrets 运行时注入）初始化管理员"
    return 0
  fi
  if [ "$NON_INTERACTIVE" -eq 1 ] || [ ! -t 0 ]; then
    if [ "$ENV" = "production" ]; then
      die "阶段失败: 正式环境需要管理员初始密码——请在 secrets 中设置 ADMIN_INITIAL_PASSWORD 或 ADMIN_INITIAL_PASSWORD_FILE"
    fi
    warn "未提供 ADMIN_INITIAL_PASSWORD 且非交互，跳过管理员初始化（可稍后运行 ./scripts/bootstrap-admin.sh）"
    return 2
  fi
  printf '请输入管理员初始密码（不回显）: ' >&2
  IFS= read -rs password
  printf '\n' >&2
  ADMIN_INITIAL_PASSWORD="$password"
  export ADMIN_INITIAL_PASSWORD
  info "已从交互式输入注入初始密码"
  return 0
}

obtain_password || rc=$?
rc="${rc:-0}"
if [ "$rc" = "2" ]; then
  exit 0
fi

# ---------------------------------------------------------------------------
# 阶段 3：运行初始化/验证（幂等）
# ---------------------------------------------------------------------------
info "阶段 3/3: 运行管理员初始化/验证（${BOOTSTRAP_FLAG}，幂等）"
( cd "$REPO_ROOT" && exec "$BIN_DIR/$CONTROL_PLANE_BIN" $BOOTSTRAP_FLAG ) \
  || die "阶段失败: 管理员初始化/验证未成功（${BOOTSTRAP_FLAG}）"
info "管理员初始化/验证完成 ✔（已存在则跳过）"
