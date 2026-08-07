#!/usr/bin/env bash
#
# ServerCLI 重启脚本 = stop.sh + start.sh（保留相同参数）
#
# 用法：
#   ./scripts/restart.sh --env test --role child --instance test-child-1
#   ./scripts/restart.sh --env production --role primary --confirm-production

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

usage() {
  cat <<'EOF'
用法: ./scripts/restart.sh [选项]

选项:
  --env <test|production>   环境（默认 test）
  --role <primary|child>    节点角色（必填）
  --instance <NAME>         实例名
  --confirm-production      正式环境二次确认（production 必填）
  --skip-migrate            重启时跳过迁移（默认执行）
  -h, --help                显示帮助
EOF
}

parse_args "$@"
[ -n "$ROLE" ] || die "缺少 --role（primary / child）"
resolve_instance
require_production_confirm

CONFIRM_ARGS=""
[ "$CONFIRM_PRODUCTION" -eq 1 ] && CONFIRM_ARGS="--confirm-production"

info "重启实例: ${INSTANCE}（$ENV/${ROLE}）"
"$SCRIPT_DIR/stop.sh"   --env "$ENV" --role "$ROLE" --instance "$INSTANCE" $CONFIRM_ARGS
"$SCRIPT_DIR/start.sh"  --env "$ENV" --role "$ROLE" --instance "$INSTANCE" $CONFIRM_ARGS
