#!/usr/bin/env bash
#
# ServerCLI 日志查看：tail 实例日志
#
# 用法：
#   ./scripts/logs.sh --env test --role primary --follow
#   ./scripts/logs.sh --env test --role child --instance test-child-1 --lines 200
#
# --role primary → control-plane.log；--role child → node-agent.log；
# 不指定 --role 时同时跟踪控制面与 node-agent 两个日志。

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

usage() {
  cat <<'EOF'
用法: ./scripts/logs.sh [选项]

选项:
  --env <test|production>   环境（默认 test）
  --role <primary|child>    选择组件日志（primary=控制面，child=node-agent）
  --instance <NAME>         实例名
  --follow, -f              持续跟踪（tail -f）
  --lines <N>               显示最后 N 行（默认 100）
  -h, --help                显示帮助
EOF
}

parse_args "$@"
[ -n "$ROLE" ] || ROLE=""
resolve_instance

LOG_DIR="$(get_cfg "$REPO_ROOT/deploy/environments/$ENV/$INSTANCE.env" LOG_DIR "$REPO_ROOT/logs/$INSTANCE")"
case "$LOG_DIR" in
  /*) ;;
  *) LOG_DIR="$REPO_ROOT/$LOG_DIR" ;;
esac

files=()
case "$ROLE" in
  primary) files+=("$LOG_DIR/control-plane.log") ;;
  child)   files+=("$LOG_DIR/node-agent.log") ;;
  *)       files+=("$LOG_DIR/control-plane.log" "$LOG_DIR/node-agent.log") ;;
esac

existing=()
for f in "${files[@]}"; do
  [ -f "$f" ] && existing+=("$f")
done

if [ "${#existing[@]}" -eq 0 ]; then
  die "没有可查看的日志文件（${LOG_DIR}）——请先运行 ./scripts/start.sh --env $ENV --role ${ROLE:-primary} --instance $INSTANCE"
fi

TAIL_ARGS=(-n "$LINES")
[ "$FOLLOW" -eq 1 ] && TAIL_ARGS+=(-f)

exec tail "${TAIL_ARGS[@]}" -- "${existing[@]}"
