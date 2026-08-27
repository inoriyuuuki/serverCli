#!/usr/bin/env bash
# =============================================================================
# ServerCLI 节点首引导脚本（deployment-bootstrap）
# 适用：CentOS/RHEL amd64（systemd + ossutil）
#
# 作用：
#   1) 交互输入 OSS AccessKey（read / read -s，绝不走 argv / echo / URL 拼接）
#   2) 用 ossutil 从私有 OSS 同步 deployment-repository/ 到
#      /opt/servercli-deployment/repository/
#   3) 按 manifests/repository-manifest.json 逐对象校验 sha256/size
#      （对象 path 先做安全校验，stat 前拒绝绝对路径/.. /反斜杠/控制字符）
#   4) 修正 secrets/ 权限（目录 0700 / 文件 0600）
#   5) （控制面模式）上报引导状态；经 materialize 获取 HMAC-SHA256 签名密钥，
#      校验 repository-manifest.json 的签名；密钥落
#      .servercli-local/credentials/deploy-signing.key（0600，目录 0700）
#   6) （控制面模式）从公开 OSS 桶 inori-image 下载最新 ServerCLI Agent 制品
#      （对象固定名 servercli/latest/servercli-latest-linux-<arch>.tar.gz，
#      GitHub Actions 构建后上传，节点直连桶、不经 xray 访问 GitHub），
#      SHA-256 校验后安装到 /usr/local/bin/servercli-node-agent
#   7) 仅输出 Bucket/Prefix/对象数/下载状态/hash，绝不打印凭证与 Secret 正文
#
# 用法：
#   纯同步模式（不传控制面三参数，只做 OSS 同步 + 校验 + 权限修正）：
#     sudo bash scripts/deployment-bootstrap.sh \
#         --bucket <private-oss-bucket> \
#         --prefix deployment-repository/ \
#         [--region cn-hangzhou] \
#         [--endpoint https://oss-cn-hangzhou-internal.aliyuncs.com] \
#         [--repo-dir /opt/servercli-deployment/repository]
#
#   控制面模式（--session-id / --token / --control-plane-url 必须同时提供；
#   额外上报状态 + 物化签名密钥 + 校验 manifest 签名 + 从公开 OSS 桶下载 Agent）：
#     sudo bash scripts/deployment-bootstrap.sh \
#         --bucket <private-oss-bucket> \
#         --prefix deployment-repository/ \
#         --session-id <bootstrap-session-id> \
#         --token <one-time-session-token> \
#         --control-plane-url https://<master>:9043 \
#         [--agent-bucket inori-image] \
#         [--oss-public-endpoint oss-cn-hangzhou.aliyuncs.com] \
#         [--agent-version <信息性：记录安装版本，默认取主控 /version；下载始终用 latest>] \
#         [--region cn-hangzhou] \
#         [--endpoint https://oss-cn-hangzhou-internal.aliyuncs.com] \
#         [--repo-dir /opt/servercli-deployment/repository]
#
# 安全约束（必须遵守）：
#   * OSS AccessKey ID / Secret 仅通过交互 read 输入；禁止通过 argv、环境变量
#     或 URL 传参；禁止在任何输出中打印凭证正文
#   * 一次性 session token 仅经 --token 传入（控制面会话凭证，非 OSS 凭据），
#     同样禁止打印
#   * 临时目录 0700、临时凭证文件 0600，trap EXIT/INT/TERM 删除
#   * ossutil 凭据一律经临时配置文件（-c）传入，禁止 -i/-k argv
#   * --endpoint 必须 https:// 且 host 属于 *.aliyuncs.com / *.aliyuncs.com.cn
#     白名单（防 AK/SK 外发）；ossutil 配置只写 host（其默认 HTTPS）
#   * 签名密钥（deploy-signing.key）绝不写入 OSS、绝不打日志；文件 0600、目录 0700
#   * 主控地址直接访问，不走 xray 代理；xray 探活失败必须非零退出停止
#   * Agent 制品从公开 OSS 桶 inori-image 下载（https，SHA-256 校验），
#     首次安装不再经 xray 访问 GitHub
#   * 整体幂等，可安全重跑
# =============================================================================
set -euo pipefail

# ----------------------------- 默认值/参数 -----------------------------
OSS_BUCKET=""
OSS_PREFIX="deployment-repository/"
OSS_REGION="cn-hangzhou"
OSS_ENDPOINT="https://oss-cn-hangzhou-internal.aliyuncs.com"
REPO_DIR="/opt/servercli-deployment/repository"
CONTROL_PLANE_URL=""
SESSION_ID=""
SESSION_TOKEN=""
# Agent 制品获取：公开 OSS 桶（GitHub Actions 构建后上传，节点直连桶下载，
# 不再经 xray 访问 GitHub）。对象名固定为 servercli/latest/servercli-latest-
# linux-<arch>.tar.gz（不区分版本号），始终下载最新；--agent-version 仅作
# 信息性记录（默认取主控 /version）。
AGENT_BUCKET="inori-image"
OSS_PUBLIC_ENDPOINT="oss-cn-hangzhou.aliyuncs.com"
AGENT_VERSION=""
STATE_FILE="/opt/servercli-deployment/.bootstrap-state"

usage() {
  sed -n '1,70p' "$0"
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --bucket) OSS_BUCKET="${2:-}"; shift 2 ;;
    --prefix) OSS_PREFIX="${2:-}"; shift 2 ;;
    --region) OSS_REGION="${2:-}"; shift 2 ;;
    --endpoint) OSS_ENDPOINT="${2:-}"; shift 2 ;;
    --repo-dir) REPO_DIR="${2:-}"; shift 2 ;;
    --control-plane-url) CONTROL_PLANE_URL="${2:-}"; shift 2 ;;
    --session-id) SESSION_ID="${2:-}"; shift 2 ;;
    --token) SESSION_TOKEN="${2:-}"; shift 2 ;;
    --agent-bucket) AGENT_BUCKET="${2:-}"; shift 2 ;;
    --oss-public-endpoint) OSS_PUBLIC_ENDPOINT="${2:-}"; shift 2 ;;
    --agent-version) AGENT_VERSION="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "[bootstrap] 未知参数: $1（仅接受固定参数；OSS 凭据禁止走 argv）" >&2; exit 2 ;;
  esac
done

[[ -n "$OSS_BUCKET" ]] || { echo "[bootstrap] 缺少必填参数 --bucket（私有 OSS Bucket，不允许默认值）" >&2; exit 2; }
OSS_PREFIX="${OSS_PREFIX%/}/"                       # 统一补尾斜杠
REMOTE="oss://${OSS_BUCKET}/${OSS_PREFIX}"

# ----------------------------- 控制面模式判定 -----------------------------
# 新参数可选：--session-id/--token/--control-plane-url 三个同时提供才进入
# 控制面模式（上报 + 签名校验）；全部不传则维持纯同步模式。只传其中部分视为错误。
CP_MODE=0
if [[ -n "$SESSION_ID" || -n "$SESSION_TOKEN" || -n "$CONTROL_PLANE_URL" ]]; then
  if [[ -z "$SESSION_ID" || -z "$SESSION_TOKEN" || -z "$CONTROL_PLANE_URL" ]]; then
    echo "[bootstrap] 控制面模式要求 --session-id / --token / --control-plane-url 三个参数同时提供（全部不传则进入纯同步模式）" >&2
    exit 2
  fi
  CP_MODE=1
fi

# ----------------------------- endpoint 校验（防 AK/SK 外发） -----------------------------
validate_endpoint() {
  local ep="$1" rest host
  [[ "$ep" == https://* ]] || return 1
  rest="${ep#https://}"
  rest="${rest%/}"
  [[ -n "$rest" ]] || return 1
  host="${rest%%/*}"
  [[ "$host" == "$rest" ]] || return 1                      # 不允许路径/端口/查询
  printf '%s' "$host" | grep -Eq '^[A-Za-z0-9.-]+$' || return 1
  # 白名单：*.aliyuncs.com / *.aliyuncs.com.cn
  [[ "$host" == "aliyuncs.com" || "$host" == *".aliyuncs.com" ]] && return 0
  [[ "$host" == "aliyuncs.com.cn" || "$host" == *".aliyuncs.com.cn" ]] && return 0
  return 1
}
if ! validate_endpoint "$OSS_ENDPOINT"; then
  echo "[bootstrap] FAIL: --endpoint 必须为 https:// 且 host 属于 *.aliyuncs.com / *.aliyuncs.com.cn（防止 AK/SK 外发到非阿里云地址）" >&2
  exit 2
fi
# ossutil 配置只写 host（其默认 HTTPS 传输；host 已过白名单校验）
OSS_ENDPOINT_HOST="${OSS_ENDPOINT#https://}"
OSS_ENDPOINT_HOST="${OSS_ENDPOINT_HOST%/}"

# ----------------------------- 前置检查 -----------------------------
if [[ "$(id -u)" -ne 0 ]]; then
  echo "[bootstrap] 需要 root 权限运行（sudo bash scripts/deployment-bootstrap.sh ...）" >&2
  exit 1
fi
if ! command -v ossutil >/dev/null 2>&1; then
  echo "[bootstrap] 未找到 ossutil。" >&2
  echo "[bootstrap] 请先安装 ossutil 后重试：参考 init/centos/install/install_ossutil.sh" >&2
  echo "[bootstrap] （下载 ossutil 并放入 PATH；或在 /usr/local/bin 下放置可执行 ossutil）" >&2
  exit 1
fi
for _c in sha256sum find mktemp awk sed grep; do
  command -v "$_c" >/dev/null 2>&1 || { echo "[bootstrap] 缺少依赖命令: $_c" >&2; exit 1; }
done
if [[ "$CP_MODE" -eq 1 ]]; then
  for _c in curl openssl base64 od; do
    command -v "$_c" >/dev/null 2>&1 || { echo "[bootstrap] 缺少依赖命令: $_c（控制面模式需要）" >&2; exit 1; }
  done
fi

# ----------------------------- 临时目录与凭证（0600/0700） -----------------------------
TMPDIR_SEC="$(mktemp -d /tmp/servercli-bootstrap-XXXXXX)" || exit 1
chmod 0700 "$TMPDIR_SEC"
CRED_FILE="$TMPDIR_SEC/ossutilconfig"
# P0 #14: rm -rf 保护 —— TMPDIR_SEC 由 mktemp 生成（固定前缀 /tmp/
# servercli-bootstrap-，全新空目录，非空安全），且始终带引号使用；不得改为
# 硬编码路径或去掉引号。
cleanup() {
  rm -rf "$TMPDIR_SEC"
}
trap cleanup EXIT INT TERM

# 交互读取 AccessKey（禁止 argv/echo；read -s 不回显）
echo "[bootstrap] 请输入 OSS AccessKey（仅本次交互，不落盘明文日志）"
read -r -p "  AccessKey ID:     " OSS_AK
read -r -s -p "  AccessKey Secret: " OSS_SK
echo
[[ -n "$OSS_AK" && -n "$OSS_SK" ]] || { echo "[bootstrap] AccessKey 不能为空" >&2; exit 1; }

# 写入 ossutil 配置文件（0600），后续全部经 -c 使用，绝不用 -i/-k argv
cat > "$CRED_FILE" <<CREDEOF
[Credentials]
language=CH
accessKeyID=${OSS_AK}
accessKeySecret=${OSS_SK}
endpoint=${OSS_ENDPOINT_HOST}
CREDEOF
chmod 0600 "$CRED_FILE"
unset OSS_AK OSS_SK   # 立即从环境清除

# ----------------------------- 控制面状态上报（V1 容错：失败仅 warn 不阻断） -----------------------------
# token 仅来自 --token 参数；payload 不经任何日志/回显，绝不打印正文。
json_str() {
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  printf '%s' "$s"
}
report_state() {
  [[ "$CP_MODE" -eq 1 ]] || return 0
  local state="$1" msg="$2"
  local payload
  payload="$(printf '{"session_token":"%s","state":"%s","message":"%s"}' \
    "$SESSION_TOKEN" "$state" "$(json_str "$msg")")"
  if ! curl -fsS -X POST "$CONTROL_PLANE_URL/api/v1/agent/deployments/bootstrap/report" \
      -H 'Content-Type: application/json' -d "$payload" >/dev/null 2>&1; then
    echo "[bootstrap] warn: 引导状态上报失败（V1 容错，不阻断）: state=$state" >&2
  else
    echo "[bootstrap] 状态上报: $state"
  fi
  unset payload
}

# ----------------------------- 对象路径安全校验（stat 前） -----------------------------
validate_rel_path() {
  local p="$1"
  [[ -n "$p" ]] || return 1
  [[ "$p" != /* ]] || return 1          # 拒绝绝对路径
  [[ "$p" != *".."* ]] || return 1      # 拒绝含 ..
  [[ "$p" != *\\* ]] || return 1        # 拒绝反斜杠
  if [[ "$p" =~ [[:cntrl:]] ]]; then return 1; fi   # 拒绝控制字符
  return 0
}

# ----------------------------- 同步 -----------------------------
report_state "repository_syncing" "开始同步 deployment-repository"
mkdir -p "$REPO_DIR"
echo "[bootstrap] 开始同步：$REMOTE -> $REPO_DIR/"
if ! ossutil -c "$CRED_FILE" sync "$REMOTE" "$REPO_DIR/" --update; then
  report_state "repository_sync_failed" "OSS 同步失败"
  echo "[bootstrap] FAIL: OSS 同步失败" >&2
  exit 1
fi
echo "[bootstrap] 同步完成"

MANIFEST="$REPO_DIR/manifests/repository-manifest.json"
if [[ ! -f "$MANIFEST" ]]; then
  report_state "repository_sync_failed" "未找到 repository-manifest.json"
  echo "[bootstrap] FAIL: 未找到 $MANIFEST（请检查 --bucket/--prefix 是否正确）" >&2
  exit 1
fi

# ----------------------------- 逐对象校验（path 安全 + size + sha256） -----------------------------
# 说明：repository-manifest.json 由控制面按固定 pretty 格式生成（每行一个键），
# 此处用 awk 解析 path/size/sha256 三元组；如控制面调整格式须同步本解析。
verify_manifest() {
  local m="$1" total=0 ok=0 bad=0
  while IFS=$'\t' read -r p sz sha; do
    [[ -n "$p" ]] || continue
    total=$((total + 1))
    # P1: 对象 path 安全校验（stat 前）：拒绝 / 开头、..、反斜杠、控制字符
    if ! validate_rel_path "$p"; then
      echo "  [UNSAFE PATH] $p"
      bad=$((bad + 1)); continue
    fi
    local f="$REPO_DIR/$p" actual=""
    if [[ ! -f "$f" ]]; then
      echo "  [MISSING] $p"
      bad=$((bad + 1)); continue
    fi
    local fsz
    fsz="$(stat -c %s "$f" 2>/dev/null || stat -f %z "$f" 2>/dev/null)"
    if [[ "$fsz" != "$sz" ]]; then
      echo "  [SIZE MISMATCH] $p (manifest=$sz disk=$fsz)"
      bad=$((bad + 1)); continue
    fi
    actual="$(sha256sum "$f" | awk '{print $1}')"
    if [[ "$actual" != "$sha" ]]; then
      echo "  [HASH MISMATCH] $p"
      bad=$((bad + 1)); continue
    fi
    echo "  [OK] $p (${sha:0:16}...)"
    ok=$((ok + 1))
  done < <(awk '
    /"path":/   { line=$0; sub(/^.*"path":[ \t]*"/,"",line); sub(/".*$/,"",line); path=line }
    /"size":/   { line=$0; sub(/^.*"size":[ \t]*/,"",line); sub(/[,].*$/,"",line); size=line }
    /"sha256":/ { line=$0; sub(/^.*"sha256":[ \t]*"/,"",line); sub(/".*$/,"",line); sha=line;
                  print path "\t" size "\t" sha }
  ' "$m")
  echo "[verify] total=$total ok=$ok bad=$bad"
  [[ "$bad" -eq 0 && "$total" -gt 0 ]]
}

if verify_manifest "$MANIFEST"; then
  echo "[bootstrap] 仓库校验通过"
else
  report_state "manifest_invalid" "仓库校验未通过（对象缺失/大小或 hash 不一致/路径不安全）"
  echo "[bootstrap] FAIL: 仓库校验未通过（对象缺失/大小或 hash 不一致/路径不安全），请勿继续部署" >&2
  exit 1
fi

# ----------------------------- secrets 权限修正 -----------------------------
if [[ -d "$REPO_DIR/secrets" ]]; then
  find "$REPO_DIR/secrets" -type d -exec chmod 0700 {} +
  find "$REPO_DIR/secrets" -type f -exec chmod 0600 {} +
  # 安全提示：仓库内只允许 .example 模板；若出现真实 Secret 文件仅告警（不打印正文）
  local_non_example="$(find "$REPO_DIR/secrets" -type f ! -name '*.example' 2>/dev/null)"
  if [[ -n "$local_non_example" ]]; then
    echo "[bootstrap] 警告: secrets/ 下发现非 .example 文件（不会打印内容）：" >&2
    printf '  %s\n' $local_non_example >&2
  fi
  echo "[bootstrap] secrets/ 权限已修正（目录 0700 / 文件 0600）"
fi

report_state "repository_verified" "仓库同步与校验完成，权限已修正"

# ----------------------------- 签名密钥物化 + manifest 签名校验（控制面模式） -----------------------------
# V1：制品/仓库签名使用控制面生成的 HMAC-SHA256 密钥（非公钥体系，见
# doc/16_PLAINTEXT_OSS_SECRETS_V1.md「V1 签名与凭证债务」）。密钥经
# materialize 一次性下发（base64），绝不写入 OSS、绝不打日志；文件 0600。
if [[ "$CP_MODE" -eq 1 ]]; then
  DEPLOY_ROOT="$(dirname "$REPO_DIR")"
  LOCAL_DIR="$DEPLOY_ROOT/.servercli-local"
  LOCAL_CRED_DIR="$LOCAL_DIR/credentials"
  SIGNING_KEY_FILE="$LOCAL_CRED_DIR/deploy-signing.key"
  mkdir -p "$LOCAL_CRED_DIR"
  chmod 0700 "$LOCAL_DIR" 2>/dev/null || true
  chmod 0700 "$LOCAL_CRED_DIR"

  # 物化：控制面下发 base64 编码的 HMAC-SHA256 签名密钥
  MAT_JSON="$(curl -fsS -X POST "$CONTROL_PLANE_URL/api/v1/agent/deployments/bootstrap/materialize" \
      -H 'Content-Type: application/json' \
      -d "{\"session_token\":\"$SESSION_TOKEN\"}" 2>/dev/null)" || {
    report_state "signature_failed" "签名密钥物化失败（materialize 接口不可达或拒绝）"
    echo "[bootstrap] FAIL: 签名密钥物化失败（materialize 接口不可达或拒绝）" >&2
    exit 1
  }
  KEY_B64="$(printf '%s' "$MAT_JSON" | sed -n 's/.*"signing_key"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  if [[ -z "$KEY_B64" ]]; then
    report_state "signature_failed" "materialize 响应缺少 signing_key"
    echo "[bootstrap] FAIL: materialize 响应缺少 signing_key" >&2
    exit 1
  fi

  # 解码 base64 -> 原始密钥字节；临时文件 0600；绝不打印密钥
  umask 077
  if printf '%s' "$KEY_B64" | base64 -d > "$SIGNING_KEY_FILE" 2>/dev/null; then
    :
  elif printf '%s' "$KEY_B64" | base64 -D > "$SIGNING_KEY_FILE" 2>/dev/null; then
    :
  else
    rm -f "$SIGNING_KEY_FILE"
    report_state "signature_failed" "signing_key base64 解码失败"
    echo "[bootstrap] FAIL: signing_key base64 解码失败" >&2
    exit 1
  fi
  chmod 0600 "$SIGNING_KEY_FILE"
  unset KEY_B64 MAT_JSON
  echo "[bootstrap] 签名密钥已物化: $SIGNING_KEY_FILE（0600，root-only）"

  # 校验 repository-manifest.json 的 HMAC-SHA256 签名（与 manifest.signature 对比）。
  # 被签数据是控制面与节点一致的 canonical payload 文件
  # manifests/repository-manifest.canonical（不是整个 manifest JSON——其字节与
  # canonical 序列化不同，且 signature 字段自指）。
  MANIFEST_SIG="$(sed -n 's/.*"signature"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$MANIFEST")"
  CANON_FILE="$REPO_DIR/manifests/repository-manifest.canonical"
  if [[ -z "$MANIFEST_SIG" ]]; then
    report_state "signature_failed" "repository-manifest.json 缺少 signature 字段"
    echo "[bootstrap] FAIL: repository-manifest.json 缺少 signature 字段" >&2
    exit 1
  fi
  if [[ ! -f "$CANON_FILE" ]]; then
    report_state "signature_failed" "缺少 canonical payload 文件（repository-manifest.canonical）"
    echo "[bootstrap] FAIL: 缺少 canonical payload 文件（repository-manifest.canonical）" >&2
    exit 1
  fi
  # 等价于 `openssl dgst -sha256 -hmac "$key" -hex`，但密钥经 hexkey 传入，
  # 避免原始二进制密钥字节出现在进程 argv（可含 NUL/不可打印字节）
  KEY_HEX="$(od -An -v -tx1 "$SIGNING_KEY_FILE" | tr -d ' \n')"
  COMPUTED="$(openssl dgst -sha256 -mac HMAC -macopt "hexkey:$KEY_HEX" -hex "$CANON_FILE" 2>/dev/null)" || {
    report_state "signature_failed" "openssl HMAC-SHA256 计算失败"
    echo "[bootstrap] FAIL: openssl HMAC-SHA256 计算失败" >&2
    exit 1
  }
  unset KEY_HEX
  COMPUTED_HEX="${COMPUTED##*= }"
  COMPUTED_HEX="$(printf '%s' "$COMPUTED_HEX" | tr -d '[:space:]')"
  COMPUTED_HEX="$(printf '%s' "$COMPUTED_HEX" | tr '[:upper:]' '[:lower:]')"
  MANIFEST_SIG_L="$(printf '%s' "$MANIFEST_SIG" | tr '[:upper:]' '[:lower:]')"
  if [[ "$COMPUTED_HEX" != "$MANIFEST_SIG_L" ]]; then
    report_state "signature_failed" "repository-manifest.json 签名校验失败"
    echo "[bootstrap] FAIL: repository-manifest.json HMAC-SHA256 签名校验失败" >&2
    exit 1
  fi
  echo "[bootstrap] manifest 签名校验通过（HMAC-SHA256）"
fi

# ----------------------------- Agent 下载（公开 OSS 桶，不经过 xray） -----------------------------
# 首次引导：Agent 制品由 GitHub Actions 构建后上传到公开桶 inori-image
# （oss://inori-image/servercli/<tag>/...），节点直接经 HTTPS 从桶下载并做
# SHA-256 校验，不再依赖 xray 代理访问 GitHub。版本默认取主控 /version
# （与主控同版本），也可用 --agent-version 固定。
if [[ "$CP_MODE" -eq 1 ]]; then
  report_state "agent_downloading" "从公开 OSS 桶下载 ServerCLI Agent"

  # 公开 endpoint 校验：host 必须属于 *.aliyuncs.com / *.aliyuncs.com.cn
  PUB_HOST="${OSS_PUBLIC_ENDPOINT#https://}"
  PUB_HOST="${PUB_HOST#http://}"
  PUB_HOST="${PUB_HOST%/}"
  if ! printf '%s' "$PUB_HOST" | grep -Eq '^[A-Za-z0-9.-]+$'      || { [[ "$PUB_HOST" != "aliyuncs.com" && "$PUB_HOST" != *".aliyuncs.com"             && "$PUB_HOST" != "aliyuncs.com.cn" && "$PUB_HOST" != *".aliyuncs.com.cn" ]]; }; then
    report_state "agent_download_failed" "公开 endpoint 不在白名单"
    echo "[bootstrap] FAIL: --oss-public-endpoint 必须为 *.aliyuncs.com / *.aliyuncs.com.cn 白名单" >&2
    exit 1
  fi

  # 版本（信息性）：--agent-version 优先，否则查询主控 /version 仅用于日志/状态
  if [[ -z "$AGENT_VERSION" ]]; then
    AGENT_VERSION="$(curl -fsS --max-time 15 "$CONTROL_PLANE_URL/version" 2>/dev/null | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' || true)"
  fi
  [[ -n "$AGENT_VERSION" ]] || AGENT_VERSION="latest"

  # 架构（V1 支持 linux amd64/arm64）
  case "$(uname -m)" in
    x86_64|amd64) AGENT_ARCH="amd64" ;;
    aarch64|arm64) AGENT_ARCH="arm64" ;;
    *) report_state "agent_download_failed" "不支持的架构 $(uname -m)"; echo "[bootstrap] FAIL: 不支持架构 $(uname -m)（V1 仅 amd64/arm64）" >&2; exit 1 ;;
  esac

  # 固定 latest 命名（不区分版本号）：始终下载 servercli/latest/ 下最新制品
  AGENT_BASE="https://${AGENT_BUCKET}.${PUB_HOST}/servercli/latest"
  AGENT_TGZ="${AGENT_BASE}/servercli-latest-linux-${AGENT_ARCH}.tar.gz"
  AGENT_SHA_URL="${AGENT_BASE}/sha256sums.txt"
  echo "[bootstrap] 下载 Agent（latest）: ${AGENT_TGZ}（版本 ${AGENT_VERSION}）"

  if ! curl -fsSL --retry 3 --retry-delay 3 --max-time 300 -o "$TMPDIR_SEC/agent.tar.gz" "$AGENT_TGZ"; then
    report_state "agent_download_failed" "Agent 制品下载失败"
    echo "[bootstrap] FAIL: Agent 制品下载失败（$AGENT_TGZ）" >&2
    exit 1
  fi
  if ! curl -fsSL --retry 3 --retry-delay 3 --max-time 60 -o "$TMPDIR_SEC/sha256sums.txt" "$AGENT_SHA_URL"; then
    report_state "agent_download_failed" "sha256sums 下载失败"
    echo "[bootstrap] FAIL: sha256sums.txt 下载失败" >&2
    exit 1
  fi

  report_state "agent_verifying" "校验 Agent 制品 SHA-256"
  # 直接比对哈希（与本地下载文件名解耦：sha256sums.txt 中的条目按制品名
  # servercli-latest-linux-<arch>.tar.gz 记录，而本地下载文件名为 agent.tar.gz）
  EXPECTED_SHA="$(awk -v f="servercli-latest-linux-${AGENT_ARCH}.tar.gz" '$2==f || $2=="*"f {print $1}' "$TMPDIR_SEC/sha256sums.txt" | head -1)"
  ACTUAL_SHA="$(sha256sum "$TMPDIR_SEC/agent.tar.gz" | awk '{print $1}')"
  if [[ -z "$EXPECTED_SHA" || "$EXPECTED_SHA" != "$ACTUAL_SHA" ]]; then
    report_state "agent_verify_failed" "Agent 制品 SHA-256 校验失败"
    echo "[bootstrap] FAIL: Agent 制品 SHA-256 校验失败（期望 ${EXPECTED_SHA:-<空>}，实际 ${ACTUAL_SHA:-<空>}）" >&2
    exit 1
  fi
  echo "[bootstrap] Agent 制品 SHA-256 校验通过（${ACTUAL_SHA:0:16}...）"

  # 解压并安装 node-agent 二进制（systemd 单元与 enrollment 为 V1 下一步）
  mkdir -p "$TMPDIR_SEC/agent"
  if ! tar -xzf "$TMPDIR_SEC/agent.tar.gz" -C "$TMPDIR_SEC/agent"; then
    report_state "agent_verify_failed" "Agent 制品解压失败"
    echo "[bootstrap] FAIL: Agent 制品解压失败" >&2
    exit 1
  fi
  AGENT_BIN="$(find "$TMPDIR_SEC/agent" -type f -name servercli-node-agent | head -1)"
  if [[ -z "$AGENT_BIN" ]]; then
    report_state "agent_verify_failed" "制品缺少 servercli-node-agent"
    echo "[bootstrap] FAIL: 制品中未找到 servercli-node-agent" >&2
    exit 1
  fi
  install -m 0755 "$AGENT_BIN" /usr/local/bin/servercli-node-agent
  report_state "agent_installing" "Agent 二进制已安装到 /usr/local/bin/servercli-node-agent"
  echo "[bootstrap] Agent 已安装: /usr/local/bin/servercli-node-agent（版本 ${AGENT_VERSION}）"
fi

# ----------------------------- 状态文件（非 Secret；不含任何凭证） -----------------------------
cat > "$STATE_FILE" <<STATEEOF
# ServerCLI bootstrap 状态（非 Secret；不含任何凭证/签名密钥）
bucket=${OSS_BUCKET}
prefix=${OSS_PREFIX}
region=${OSS_REGION}
endpoint=${OSS_ENDPOINT}
repo_dir=${REPO_DIR}
control_plane_url=${CONTROL_PLANE_URL}
session_id=${SESSION_ID}
cp_mode=${CP_MODE}
bootstrapped_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
STATEEOF
chmod 0644 "$STATE_FILE"

# ----------------------------- 状态输出（不含凭证/Secret 正文） -----------------------------
echo "==================================================================="
echo "[status] Bucket:       $OSS_BUCKET"
echo "[status] Prefix:       $OSS_PREFIX"
echo "[status] Repo dir:     $REPO_DIR"
echo "[status] Manifest:     $MANIFEST"
echo "[status] Region:       $OSS_REGION"
echo "[status] Endpoint:     $OSS_ENDPOINT"
echo "[status] 对象数:        见上方 verify 输出（total/ok/bad）"
echo "[status] 下载状态:      见上方 [OK]/[MISSING]/[MISMATCH]/[UNSAFE PATH] 行"
echo "[status] 校验:          sha256 逐对象一致 + manifest HMAC-SHA256 签名（控制面模式）"
echo "[status] Secrets:      目录 0700 / 文件 0600（仅 .example 模板）"
echo "==================================================================="

# =============================================================================
# 后续步骤（预留注释，V1 明确未实现；本脚本当前只做 OSS 同步/校验/签名校验；
# 每阶段实现时先 report_state 上报，失败必须 report 对应失败态并非零退出）
# -----------------------------------------------------------------------------
# 1) 安装 xray（feature: xray-bootstrap）：
#       report_state "xray_installing" "安装 xray"
#       bash "$REPO_DIR/features/xray-bootstrap/hooks/install.sh" \
#           --feature-key xray-bootstrap --node-id "$NODE_ID" \
#           --deployment-root-dir /opt/servercli-deployment --config-dir ...
#       失败 report_state "xray_failed"
# 2) 代理探活（127.0.0.1:10809 测 GitHub）：失败必须返回非零并停止后续步骤
#       report_state "proxy_checking" "代理探活"
#       curl -x http://127.0.0.1:10809 -fsS -o /dev/null https://github.com
#       成功 report_state "proxy_ready"；失败 report_state "proxy_failed"
# 3) （已实现）下载 ServerCLI Agent：从公开 OSS 桶 inori-image 下载
#    （GitHub Actions 构建后上传；节点直连桶，不再经 xray 访问 GitHub），
#    SHA-256 校验后安装到 /usr/local/bin/servercli-node-agent
#       report_state "agent_downloading" / "agent_verifying" / "agent_installing"
#       失败 report_state "agent_download_failed" / "agent_verify_failed"
# 4) 安装 systemd 服务（servercli-node-agent）：systemd 单元在制品内
#    deploy/systemd/servercli-node-agent.service.example；V1 下一步实现
#       report_state "agent_installing" ...；失败 report_state "agent_start_failed"
# 5) 复用现有 enrollment 注册：调用主控
#       POST ${CONTROL_PLANE_URL}/api/v1/agent/enrollments
#       report_state "enrollment_pending" ... → "node_online" / "enrollment_failed"
#    主控地址直接访问（NO_PROXY 覆盖 / 不走 xray）
# 6) 管理员审批后 node online
# 注意：主控地址 ${CONTROL_PLANE_URL} 必须直连，不得走 xray 代理；
#       xray 探活失败时以非零退出码停止整个引导流程。
# =============================================================================
echo "[bootstrap] 首引导 OSS 同步/校验/签名校验完成；后续部署步骤见脚本尾部预留注释。"
exit 0
