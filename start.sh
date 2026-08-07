#!/bin/bash
# =============================================================================
# ServerCLI 生产实例启动器（兼容 init 仓库 install_common 调用模式）
# 用法: ./start.sh [项目目录]
# 读取 .instance 判定本机实例并启动：
#   production-primary -> scripts/start.sh（控制面+node-agent）
#   production-*       -> 仅启动 node-agent
# 说明：本脚本可能被 init 仓库 shell_run(source) 调用，因此不使用 set -e / exec，
#       避免影响调用方 shell。
# =============================================================================
PROJECT_DIR="${1:-$(cd "$(dirname "$0")" && pwd)}"
cd "$PROJECT_DIR" || { echo "无法进入项目目录 $PROJECT_DIR" >&2; return 1 2>/dev/null || exit 1; }

if [ ! -f .instance ]; then
    echo "serverCli 尚未初始化（缺少 .instance），跳过启动（由 restore_after_serverCli 完成初始化）" >&2
    return 0 2>/dev/null || exit 0
fi
INSTANCE=$(cat .instance)
echo "启动 serverCli 实例: $INSTANCE"

case "$INSTANCE" in
  production-primary)
    ./scripts/start.sh --env production --role primary --instance "$INSTANCE" --confirm-production --non-interactive --no-build --skip-migrate
    # node-agent 需以 servercli-ai 运行（sshd 才能读取其 authorized_keys）
    if id servercli-ai >/dev/null 2>&1; then
        [ -f .env ] || cp "deploy/environments/production/$INSTANCE.env" .env
        chmod 644 .env
        chown -R servercli-ai:servercli-ai "state/$INSTANCE" "logs/$INSTANCE" "run/$INSTANCE" 2>/dev/null || true
        pkill -f "bin/servercli-node-agent" 2>/dev/null || true
        sleep 2
        sudo -u servercli-ai nohup ./bin/servercli-node-agent >> "logs/$INSTANCE/node-agent.log" 2>&1 &
        echo $! > "run/$INSTANCE/node-agent.pid"
        sleep 2
    fi
    ;;
  production-*)
    ENV_FILE="deploy/environments/production/$INSTANCE.env"
    SECRETS_FILE="deploy/environments/production/$INSTANCE.secrets.env"
    if [ ! -f "$ENV_FILE" ]; then
        echo "缺少实例配置 $ENV_FILE" >&2
        return 1 2>/dev/null || exit 1
    fi
    set -a
    . "$ENV_FILE"
    if [ -f "$SECRETS_FILE" ]; then
        . "$SECRETS_FILE"
    fi
    set +a
    # sudo 会重置环境，写仓库根 .env 供 agent 读取配置
    [ -f .env ] || cp "$ENV_FILE" .env
    chmod 644 .env
    mkdir -p "run/$INSTANCE" "logs/$INSTANCE"
    PID_FILE="run/$INSTANCE/node-agent.pid"
    if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
        echo "node-agent 已在运行 (pid $(cat "$PID_FILE"))"
        return 0 2>/dev/null || exit 0
    fi
    if id servercli-ai >/dev/null 2>&1; then
        chown -R servercli-ai:servercli-ai "state/$INSTANCE" "logs/$INSTANCE" "run/$INSTANCE" 2>/dev/null || true
        sudo -u servercli-ai nohup ./bin/servercli-node-agent >> "logs/$INSTANCE/node-agent.log" 2>&1 &
    else
        nohup ./bin/servercli-node-agent >> "logs/$INSTANCE/node-agent.log" 2>&1 &
    fi
    echo $! > "$PID_FILE"
    sleep 2
    if kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
        echo "node-agent 已启动 (pid $(cat "$PID_FILE"))"
        return 0 2>/dev/null || exit 0
    fi
    echo "node-agent 启动失败，请查看 logs/$INSTANCE/node-agent.log" >&2
    return 1 2>/dev/null || exit 1
    ;;
  *)
    echo "未知实例: $INSTANCE" >&2
    return 1 2>/dev/null || exit 1
    ;;
esac
