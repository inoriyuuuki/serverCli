#!/usr/bin/env bash
# =============================================================================
# Hook: install — DataDashboard (data-dashboard)
# 由 ServerCLI Agent Runner 以固定参数调用；幂等可重跑。
#
# 安全约束（本文件必须遵守）:
#   * 只接受固定参数，未知参数直接报错退出（不接收任意 shell 字符串参数）
#   * 不 source 任何全局 secrets.sh；敏感值由 Runner 经固定参数/配置文件注入
#   * 无 MAC 判断逻辑
#   * 镜像/制品 tag 默认固定写死；config 文件存在时 config 优先
#   * 不打印任何 Secret 正文
# =============================================================================
set -euo pipefail

FEATURE_KEY=""; NODE_ID=""; ENVIRONMENT_ID=""; HOSTNAME=""; RELEASE_VERSION=""
DEPLOYMENT_ROOT_DIR=""; OPERATION_ID=""; DATA_DIR=""; CONFIG_DIR=""; RENDERED_DIR=""
CONFIG_FILE=""; RELEASE_DIR=""; IMAGE_TAG=""; PORT=""

# ---- 固定参数说明 ----
# 支持参数（固定）：--feature-key --node-id --environment-id --hostname
#   --release-version --deployment-root-dir --operation-id --data-dir
#   --config-dir --rendered-dir --config-file --release-dir --image-tag --port
# 未知参数一律报错退出；不接收任意 shell 字符串参数。

while [[ $# -gt 0 ]]; do
  case "$1" in
    --feature-key) FEATURE_KEY="${2:-}"; shift 2 ;;
    --node-id) NODE_ID="${2:-}"; shift 2 ;;
    --environment-id) ENVIRONMENT_ID="${2:-}"; shift 2 ;;
    --hostname) HOSTNAME="${2:-}"; shift 2 ;;
    --release-version) RELEASE_VERSION="${2:-}"; shift 2 ;;
    --deployment-root-dir) DEPLOYMENT_ROOT_DIR="${2:-}"; shift 2 ;;
    --operation-id) OPERATION_ID="${2:-}"; shift 2 ;;
    --data-dir) DATA_DIR="${2:-}"; shift 2 ;;
    --config-dir) CONFIG_DIR="${2:-}"; shift 2 ;;
    --rendered-dir) RENDERED_DIR="${2:-}"; shift 2 ;;
    --config-file) CONFIG_FILE="${2:-}"; shift 2 ;;
    --release-dir) RELEASE_DIR="${2:-}"; shift 2 ;;
    --image-tag) IMAGE_TAG="${2:-}"; shift 2 ;;
    --port) PORT="${2:-}"; shift 2 ;;
    -h|--help) sed -n '1,45p' "$0"; exit 0 ;;
    *) echo "[install] 未知参数: $1（仅接受固定参数）" >&2; exit 2 ;;
  esac
done

for _v in FEATURE_KEY NODE_ID DEPLOYMENT_ROOT_DIR; do
  if [[ -z "${!_v:-}" ]]; then
    echo "[install] 缺少必填参数 --${_v}（feature-key/node-id/deployment-root-dir 必填）" >&2
    exit 2
  fi
done

DATA_DIR="${DATA_DIR:-$DEPLOYMENT_ROOT_DIR/data/$FEATURE_KEY}"
CONFIG_DIR="${CONFIG_DIR:-$DEPLOYMENT_ROOT_DIR/configs/$FEATURE_KEY}"
RENDERED_DIR="${RENDERED_DIR:-$DEPLOYMENT_ROOT_DIR/rendered/$FEATURE_KEY}"
CONFIG_FILE="${CONFIG_FILE:-$CONFIG_DIR/config.yaml}"

# ---- 从 config 读取（约定：Agent Runner 将合并后的 feature 配置写入 CONFIG_FILE）----
read_cfg() {
  local key="$1" file="${2:-$CONFIG_FILE}" val=""
  [[ -f "$file" ]] || return 0
  val="$(sed -n "s/^[[:space:]]*${key}:[[:space:]]*[\"']\?\([^\"'[:space:]]*\).*/\1/p" "$file" | head -n1)"
  printf '%s' "$val"
}

DEFAULT_APP_MODULE="app:app"
APP_MODULE="${APP_MODULE:-$(read_cfg app_module)}"
APP_MODULE="${APP_MODULE:-$DEFAULT_APP_MODULE}"
PORT="${PORT:-$(read_cfg service_port)}"
PORT="${PORT:-9080}"
PY_BIN="$(command -v python3 || true)"
[[ -n "$PY_BIN" ]] || { echo "[install] 缺少 python3" >&2; exit 1; }

# ---- release 目录管理（current/previous 符号链接；幂等）----
# 布局: $DEPLOYMENT_ROOT_DIR/releases/<feature_key>/<version>/ (bundle 解包目录)
#       .../current   -> 当前生效 release 目录
#       .../previous  -> 上一 release 目录（供 rollback）
if [[ -n "$RELEASE_DIR" && -n "$RELEASE_VERSION" ]]; then
  rel_root="$DEPLOYMENT_ROOT_DIR/releases/$FEATURE_KEY"
  mkdir -p "$rel_root"
  cur="$(readlink "$rel_root/current" 2>/dev/null || true)"
  if [[ "$cur" != "$RELEASE_DIR" ]]; then
    if [[ -n "$cur" ]]; then
      rm -f "$rel_root/previous"
      ln -s "$cur" "$rel_root/previous"
    fi
    rm -f "$rel_root/current"
    ln -s "$RELEASE_DIR" "$rel_root/current"
    echo "[install] current release -> $RELEASE_DIR" >&2
  else
    echo "[install] release 未变化（current=${cur}），幂等跳过" >&2
  fi
  RENDERED_DIR="$RELEASE_DIR/rendered"
fi
mkdir -p "$RENDERED_DIR"

mkdir -p "$DATA_DIR/app"

# 应用源码：release 内 app/ 优先，否则写入占位可运行应用
if [[ -d "$RELEASE_DIR/app" ]]; then
  cp -rf "$RELEASE_DIR/app/." "$DATA_DIR/app/"
elif [[ ! -f "$DATA_DIR/app/main.py" ]]; then
  cat > "$DATA_DIR/app/main.py" <<'APP_EOF'
#!/usr/bin/env python3
# 占位应用（seed 模板）：真实 data-dashboard 由控制面以 release 制品下发
import http.server, socketserver, sys
PORT = int(sys.argv[sys.argv.index('--port') + 1]) if '--port' in sys.argv else 9080
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b'data-dashboard placeholder OK'
        self.send_response(200); self.send_header('Content-Length', str(len(body)))
        self.end_headers(); self.wfile.write(body)
    def log_message(self, *a): pass
with socketserver.TCPServer(('127.0.0.1', PORT), H) as httpd:
    httpd.serve_forever()
APP_EOF
fi

# venv（幂等）
if [[ ! -x "$DATA_DIR/venv/bin/python" ]]; then
  "$PY_BIN" -m venv "$DATA_DIR/venv"
fi
if [[ -f "$RELEASE_DIR/requirements.txt" ]]; then
  "$DATA_DIR/venv/bin/pip" install --quiet -r "$RELEASE_DIR/requirements.txt"
fi

# systemd 单元（幂等）
UNIT_NAME="data-dashboard-${NODE_ID}"
cat > "/etc/systemd/system/${UNIT_NAME}.service" <<UNIT_EOF
[Unit]
Description=Data Dashboard (data-dashboard / ${NODE_ID})
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=${DATA_DIR}
ExecStart=${DATA_DIR}/venv/bin/python ${DATA_DIR}/app/main.py --port ${PORT}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT_EOF
systemctl daemon-reload
systemctl enable "$UNIT_NAME" >/dev/null 2>&1 || true
systemctl start "$UNIT_NAME"
sleep 2
if ! systemctl is-active --quiet "$UNIT_NAME"; then
  echo "[install] FAIL: $UNIT_NAME 未进入 active" >&2
  exit 1
fi
echo "[install] OK: $UNIT_NAME 已启动 (port=$PORT)"
exit 0
