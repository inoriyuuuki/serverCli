#!/bin/bash
# =============================================================================
# servercli-lease-shell - AI Lease 会话包装器
# 由 init 的 restore_after_serverCli 安装到各实例 LEASE_SHELL_BIN（自动替换
# __PRIMARY_BACKEND_URL__ 为正式主控地址）。OpenSSH 通过 authorized_keys 的
# command= 调用本包装器；先向控制面校验 Lease 仍 active，再执行受限命令。
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

# 2) 受限环境
export HOME="/home/servercli-ai"
export PATH="/usr/local/bin:/usr/bin:/bin"
umask 077

# 3) 执行请求的命令或受限交互 shell
if [ -n "${SSH_ORIGINAL_COMMAND:-}" ]; then
    exec /bin/bash -c "$SSH_ORIGINAL_COMMAND"
fi
exec /bin/bash -l
