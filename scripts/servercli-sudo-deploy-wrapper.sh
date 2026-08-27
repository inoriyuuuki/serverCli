#!/usr/bin/env bash
#
# servercli-sudo-deploy-wrapper.sh — 固定 sudo wrapper（部署 Hook 唯一 root 提权入口）
#
# 契约（与 runner 一致；runner 由 backend/internal/agent/deployment_runner.go
#       runHook 以固定形式调用，本 wrapper 是本机唯一允许以 root 执行部署
#       hook 的入口，runner 绝不回退到裸 root shell）
# ---------------------------------------------------------------------------
# 调用形式：
#     <wrapper> /bin/bash <hook-rel-path> [--key value ...]
#     或       <wrapper> /bin/bash <hook-rel-path> [--key=value ...]
#
#   - $1 必须恰好为 "/bin/bash"（仅允许 bash 解释器，拒绝其他解释器）。
#   - $2 为 hook 相对路径（相对于 --rendered-dir），例如 hooks/install.sh。
#     必须满足：不以 / 开头、不含 ".." 组件、仅含 [A-Za-z0-9._/-]。
#   - 必须提供 --rendered-dir <绝对目录>；hook 绝对路径 =
#         <rendered-dir>/<hook-rel-path>
#     且 realpath（符号链接解析后）必须仍位于 rendered-dir 内；文件必须
#     存在、为常规文件且非符号链接。
#   - 其余参数仅允许白名单键（见 ALLOWED_KEYS），值字符集
#         ^[A-Za-z0-9._:/+=-]*$   （* 允许空值，如 --image-tag ""）
#     禁止空格 / 引号 / 分号 / 管道 / 换行 / $() 等 shell 元字符。
#     未知键、位置参数、缺失值一律 deny 126。
#   - 绝不 eval / source / 拼接 shell 字符串；最终以参数数组
#         exec /bin/bash "$hook" "${RUN_ARGS[@]}"
#     直接执行。
#
# 安装路径（root 所有，0700）：
#     install -m 0700 -o root -g root \
#         scripts/servercli-sudo-deploy-wrapper.sh \
#         /usr/local/libexec/servercli-deploy-wrapper
#
# sudoers 精确行（与 runner 对应，禁止通配符 / 宽松路径）：
#     servercli-ai ALL=(root) NOPASSWD: /usr/local/libexec/servercli-deploy-wrapper
#
# 与 runner 的对应关系：
#     runHook（以 sudo -n 提权）以下列形式调用本 wrapper：
#         sudo -n <wrapper> /bin/bash <hook-rel-path> \
#             --rendered-dir <dir> --operation-id ... --node-id ...
#     wrapper 负责 root 提权 + 白名单校验；hook 脚本本身永远以
#     /bin/bash 执行，参数为 --key=value 数组。
# ---------------------------------------------------------------------------

set -u

WRAPPER_NAME="servercli-sudo-deploy-wrapper"
INTERPRETER="/bin/bash"

# 允许的参数键（严格白名单）
ALLOWED_KEYS="--operation-id --target-id --node-id --feature-key --release-id --version --release-version --config-hash --data-dir --config-dir --rendered-dir --config-file --release-dir --deployment-root-dir --image-tag --port --previous-release-dir --current-release-dir --env --backup-file --restore-dir --force-delete"
# 参数值允许的字符集（无空格/引号/分号/管道/换行/$() 等 shell 元字符）
VALUE_RE='^[A-Za-z0-9._:/+=-]*$'   # * allows optional empty values (e.g. --image-tag "")
# hook 相对路径允许的字符集
HOOK_REL_RE='^[A-Za-z0-9._/-]+$'

log_err() { printf '%s\n' "[$WRAPPER_NAME] ERROR: $*" >&2; }
deny() {
  log_err "$*"
  exit 126
}

# 1) 必须 root（sudo 提权后为 UID 0；非 root 直接运行同样拒绝）
if [ "$(id -u)" -ne 0 ]; then
  deny "must run as root (invoke via sudo)"
fi

# 2) 固定调用形式：$1 必须恰好为 "/bin/bash"，$2 为 hook 相对路径
[ $# -ge 2 ] || deny "usage: $WRAPPER_NAME /bin/bash <hook-rel-path> [--key value ...]"
[ "$1" = "$INTERPRETER" ] || deny "only interpreter $INTERPRETER is allowed"
shift
HOOK_REL="$1"
shift

# 3) hook 相对路径校验：不以 / 开头、不含 ".." 组件、字符集白名单
case "$HOOK_REL" in
  /*) deny "hook path must be relative: $HOOK_REL" ;;
esac
printf '%s' "$HOOK_REL" | grep -Eq "$HOOK_REL_RE" \
  || deny "hook path contains illegal characters (allowed: [A-Za-z0-9._/-]): $HOOK_REL"
IFS='/' read -r -a _hook_parts <<< "$HOOK_REL"
for _c in "${_hook_parts[@]}"; do
  [ "$_c" != ".." ] || deny "hook path contains '..' component: $HOOK_REL"
done

# 4) 解析其余参数：只允许白名单键（--key value 或 --key=value）
declare -a RUN_ARGS=()
RENDERED_DIR=""
while [ $# -gt 0 ]; do
  arg="$1"
  shift
  case "$arg" in
    --*)
      key="${arg%%=*}"
      case " $ALLOWED_KEYS " in
        *" $key "*)
          if [ "$key" != "$arg" ]; then
            val="${arg#*=}"
          else
            [ $# -gt 0 ] || deny "missing value for $key"
            val="$1"
            shift
          fi
          # printf '%s\n'（带换行）让空值成为空行，从而匹配 VALUE_RE 的 *；
          # 若用 '%s' 空值无换行，grep 读 0 行会误判不匹配。
          if ! printf '%s\n' "$val" | grep -Eq "$VALUE_RE"; then
            deny "invalid value for $key (allowed: [A-Za-z0-9._:/+=-])"
          fi
          if [ "$key" = "--rendered-dir" ]; then
            [ -z "$RENDERED_DIR" ] || deny "duplicate --rendered-dir"
            case "$val" in
              /*) ;;
              *) deny "--rendered-dir must be an absolute path" ;;
            esac
            RENDERED_DIR="$val"
          fi
          # 以空格对形式传给 hook（seed hooks 用 `--key) shift 2` 解析）
          RUN_ARGS+=("$key" "$val")
          ;;
        *)
          deny "unknown option: $arg"
          ;;
      esac
      ;;
    *)
      deny "positional/shell argument not allowed: $arg"
      ;;
  esac
done

# 5) 锚定目录必须提供：优先 --rendered-dir（install/update/backup/health），
#    回滚使用 --previous-release-dir（hook 位于上一 release 目录）
if [ -z "$RENDERED_DIR" ]; then
  _prev=""
  _i=0
  while [ "$_i" -lt "${#RUN_ARGS[@]}" ]; do
    if [ "${RUN_ARGS[$_i]}" = "--previous-release-dir" ]; then
      _prev="${RUN_ARGS[$((_i+1))]:-}"
      break
    fi
    _i=$((_i+2))
  done
  [ -n "$_prev" ] || deny "missing required --rendered-dir (or --previous-release-dir for rollback)"
  RENDERED_DIR="$_prev"
fi

# 6) hook 绝对路径 = rendered-dir + "/" + hook-rel-path，必须仍位于
#    rendered-dir 内（realpath/前缀校验），文件必须为常规文件且非符号链接
HOOK="$RENDERED_DIR/$HOOK_REL"
[ -L "$HOOK" ] && deny "hook must not be a symlink: $HOOK"
[ -f "$HOOK" ] || deny "hook not found or not a regular file: $HOOK"
# 解析 rendered-dir 与 hook 所在目录的符号链接（cd -P），再做前缀校验
_RDIR="$(cd -P -- "$RENDERED_DIR" 2>/dev/null && pwd -P)" \
  || deny "cannot resolve rendered-dir: $RENDERED_DIR"
_HOOK_DIR="$(cd -P -- "$(dirname -- "$HOOK")" 2>/dev/null && pwd -P)" \
  || deny "cannot resolve hook directory: $HOOK"
HOOK_ABS="$_HOOK_DIR/$(basename -- "$HOOK")"
case "$HOOK_ABS" in
  "$_RDIR"/*) ;;
  *) deny "hook escapes rendered-dir (resolved path outside): $HOOK_ABS" ;;
esac

# 7) 固定 PATH：sudo 的 secure_path 会剥离 /usr/local/bin（docker/compose
#    等部署工具所在目录），这里统一补齐后再 exec，保证 hook 行为一致。
export PATH="/usr/local/bin:/usr/local/sbin:/usr/sbin:/sbin:/bin:/usr/sbin:/usr/bin"

# 8) 直接 exec，绝不 eval / source / 拼接 shell 字符串
exec /bin/bash "$HOOK_ABS" "${RUN_ARGS[@]+"${RUN_ARGS[@]}"}"
