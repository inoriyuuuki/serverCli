#!/bin/bash
# =============================================================================
# servercli-lease-shell - AI Lease 会话包装器
# 由 init 的 restore_after_serverCli 安装到各实例 LEASE_SHELL_BIN（自动替换
# __PRIMARY_BACKEND_URL__ 为正式主控地址）。OpenSSH 通过 authorized_keys 的
# command= 调用本包装器；先向控制面校验 Lease 仍 active，再执行受限命令。
#
# 权限档位（与申请时的 permission_profile 对应，由控制面 /status 返回）：
#   read-only / operator : 以 servercli-ai 普通用户执行（无提权）
#   admin                : 经 sudo -n 提权到 root 执行（需节点配置 NOPASSWD sudoers）
# 用法: servercli-lease-shell --lease <lease_id> [command...]
# =============================================================================
PRIMARY_BACKEND_URL="__PRIMARY_BACKEND_URL__"
LEASE_ID=""

while [ $# -gt 0 ]; do
    case "$1" in
        --lease)
            LEASE_ID="${2:-}"
            shift 2
            ;;
        --)
            shift
            break
            ;;
        *)
            break
            ;;
    esac
done

if [ -z "$LEASE_ID" ]; then
    echo "servercli-lease-shell: missing --lease <id>" >&2
    exit 1
fi

# 1) 校验 Lease 仍 active（GET /api/v1/ai/leases/{id}/status 为公开接口）
resp=$(curl -fsS --max-time 5 "$PRIMARY_BACKEND_URL/api/v1/ai/leases/$LEASE_ID/status" 2>/dev/null) || {
    echo "servercli-lease-shell: lease 校验失败（控制面不可达）" >&2
    exit 1
}
status=$(printf '%s' "$resp" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
    print(d.get("lease", {}).get("status", ""))
except Exception:
    print("")
' 2>/dev/null)
if [ "$status" != "active" ]; then
    echo "servercli-lease-shell: lease $LEASE_ID 状态=$status 不可用" >&2
    exit 1
fi
profile=$(printf '%s' "$resp" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
    print(d.get("lease", {}).get("permission_profile", ""))
except Exception:
    print("")
' 2>/dev/null)

# 2) 受限环境
export HOME="/home/servercli-ai"
export PATH="/usr/local/bin:/usr/bin:/bin"
umask 077

# 3) 权限档位：admin 级 Lease 经 sudo 提权到 root；其余保持 servercli-ai 普通权限
_run_as_root=0
if [ "$profile" = "admin" ]; then
    if ! command -v sudo >/dev/null 2>&1; then
        echo "servercli-lease-shell: admin 级 Lease 需要 sudo，但系统未安装 sudo" >&2
        exit 1
    fi
    _run_as_root=1
fi

# 4) 执行请求的命令或受限交互 shell
if [ -n "${SSH_ORIGINAL_COMMAND:-}" ]; then
    if [ "$_run_as_root" -eq 1 ]; then
        exec sudo -n /bin/bash -c "$SSH_ORIGINAL_COMMAND"
    fi
    exec /bin/bash -c "$SSH_ORIGINAL_COMMAND"
fi
if [ "$_run_as_root" -eq 1 ]; then
    exec sudo -n -i
fi
exec /bin/bash -l
