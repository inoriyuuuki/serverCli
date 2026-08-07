#!/bin/sh
# =============================================================================
# ServerCLI 容器入口
# 行为:
#   NODE_ROLE=child        -> 仅启动 node-agent (子节点)
#   NODE_ROLE=primary(默认)-> 后台启动 node-agent(可设 START_NODE_AGENT=0 关闭),
#                            随后前台运行 control-plane(主控制面, 托管前端+API)
# 额外参数透传给对应二进制, 例如: docker run ... -- --migrate-only
# 与 scripts/start.sh 的角色/启动逻辑保持一致。
# =============================================================================
set -e

mkdir -p /app/state /app/logs /app/run

NODE_ROLE="${NODE_ROLE:-primary}"

if [ "$NODE_ROLE" = "child" ]; then
  echo "[entrypoint] role=child, starting node-agent"
  exec /app/bin/servercli-node-agent "$@"
fi

echo "[entrypoint] role=primary, starting control-plane (frontend :9044, api :9045)"
if [ "${START_NODE_AGENT:-1}" != "0" ]; then
  echo "[entrypoint] starting node-agent in background (START_NODE_AGENT=${START_NODE_AGENT:-1})"
  /app/bin/servercli-node-agent >>/app/logs/node-agent.log 2>&1 &
  echo "[entrypoint] node-agent pid $!"
fi

exec /app/bin/servercli-control-plane "$@"
