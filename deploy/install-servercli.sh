#!/bin/sh
# =============================================================================
# ServerCLI 安装器（install-servercli.sh）
#
# 用途：在全新的 CentOS/RHEL（EL8/EL9，x86_64/aarch64）物理主机上安装
#       ServerCLI Release：三个二进制（servercli / servercli-control-plane /
#       servercli-node-agent）+ 公共模块（modules/）+ 模板（templates/）+
#       Schema（schema/）+ 安装器自身。
#
# 信任模型（fail-closed）：
#   1) 从 GitHub Release（主源）或 OSS 镜像（回退源）下载 release-manifest.json；
#   2) 用发布公钥（--pubkey，或脚本内嵌占位公钥）以 openssl pkeyutl 校验
#      Manifest 的 Ed25519 签名（签名消息 = 去除 signature 字段后按键名排序的
#      紧凑 JSON，等价于 `jq -cS 'del(.signature)'`）；
#   3) 按 Manifest 内 sha256 摘要表逐个下载并校验每个 artifact；
#   4) 原子安装到 /opt/servercli/releases/<version>，并切换 current/previous 软链接。
#
# 安全约定：
#   - 仅允许 root 运行；目标系统限定 EL8/EL9 + x86_64/aarch64；
#   - 不含任何真实 Secret / 私钥 / Token；内嵌公钥为占位值，生产必须
#     `--pubkey <file>` 替换，否则验签必然失败；
#   - 校验失败一律退出码 4（签名/认证失败），绝不信任裸 SHA256。
#
# 退出码（与 backend/internal/bootstrap 的 Exit* 常量一致）：
#   0 成功；2 参数错误；3 预检失败（OS/arch/缺少 openssl、curl、jq/python3）；
#   4 签名或 sha256 摘要校验失败；5 网络/下载失败；6 安装/解压失败。
#
# 参数：
#   --version <v>         下载版本（默认 releases/latest；也可传 tag 如 v1.2.3）
#   --github-base <url>   GitHub Release 下载基地址
#                         （默认 https://github.com/inoriyuuuki/serverCli/releases/download）
#   --oss-base <url>      可选 OSS 回退源基地址（GitHub 失败时使用；默认空=不回退）
#   --pubkey <file>       发布公钥文件（Ed25519 PKIX PEM）。不传则使用内嵌占位公钥
#   --yes                 安装完成后直接运行 servercli init（不再询问）
#   --no-init-prompt      安装完成后不询问、不运行 servercli init
#   -h, --help            显示帮助
# =============================================================================
set -euo pipefail

# ---------- 常量 ----------
DEFAULT_VERSION="releases/latest"
DEFAULT_GITHUB_BASE="https://github.com/inoriyuuuki/serverCli/releases/download"
INSTALL_ROOT="/opt/servercli"
RELEASES_DIR="${INSTALL_ROOT}/releases"

# 内嵌占位发布公钥（Ed25519 PKIX PEM 形状）。
# 注意：这不是真实密钥，openssl 无法把它解析为合法公钥，验签必然失败。
# 生产环境必须用 --pubkey 提供真实发布公钥，并在发布方与安装方之间线下核对指纹。
EMBEDDED_PUBKEY_PEM='-----BEGIN PUBLIC KEY-----
U0VSVkVSQ0xJLVBMQUNFSE9MREVSLVBVQkxJQy1LRVktRE8tTk9ULVVTRQ==
-----END PUBLIC KEY-----'

# ---------- 参数 ----------
VERSION="${DEFAULT_VERSION}"
GITHUB_BASE="${DEFAULT_GITHUB_BASE}"
OSS_BASE=""
PUBKEY_FILE=""
YES=0
NO_INIT_PROMPT=0

usage() {
  cat <<'EOF'
用法: install-servercli.sh [选项]

选项:
  --version <v>         下载版本（默认 releases/latest；也可传 tag 如 v1.2.3）
  --github-base <url>   GitHub Release 下载基地址
                        （默认 https://github.com/inoriyuuuki/serverCli/releases/download）
  --oss-base <url>      可选 OSS 回退源基地址（GitHub 下载失败时使用；可为空）
  --pubkey <file>       发布公钥文件（Ed25519 PKIX PEM）。
                        不传则使用脚本内嵌占位公钥（必须替换，否则验签失败）
  --yes                 安装完成后直接运行 servercli init（不再询问）
  --no-init-prompt      安装完成后不询问、不运行 servercli init
  -h, --help            显示帮助
EOF
}

die() {
  code="$1"
  shift
  echo "install-servercli: 错误: $*" >&2
  exit "${code}"
}

die_usage() {
  echo "install-servercli: 参数错误: $*" >&2
  usage >&2
  exit 2
}

warn() {
  echo "install-servercli: 警告: $*" >&2
}

info() {
  echo "install-servercli: $*"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || die_usage "--version 需要一个参数"
      VERSION="$2"; shift 2 ;;
    --version=*)
      VERSION="${1#*=}"; shift ;;
    --github-base)
      [ "$#" -ge 2 ] || die_usage "--github-base 需要一个参数"
      GITHUB_BASE="$2"; shift 2 ;;
    --github-base=*)
      GITHUB_BASE="${1#*=}"; shift ;;
    --oss-base)
      [ "$#" -ge 2 ] || die_usage "--oss-base 需要一个参数"
      OSS_BASE="$2"; shift 2 ;;
    --oss-base=*)
      OSS_BASE="${1#*=}"; shift ;;
    --pubkey)
      [ "$#" -ge 2 ] || die_usage "--pubkey 需要一个参数"
      PUBKEY_FILE="$2"; shift 2 ;;
    --pubkey=*)
      PUBKEY_FILE="${1#*=}"; shift ;;
    --yes)
      YES=1; shift ;;
    --no-init-prompt)
      NO_INIT_PROMPT=1; shift ;;
    -h|--help)
      usage; exit 0 ;;
    *)
      die_usage "未知参数: $1" ;;
  esac
done

# ---------- 清理 ----------
WORK=""
cleanup() {
  if [ -n "${WORK}" ]; then
    rm -rf -- "${WORK}"
  fi
}
trap cleanup 0 1 2 15

# ---------- 预检 ----------
[ "$(id -u)" -eq 0 ] || die 3 "必须使用 root 运行（sudo 或以 root 执行）"

if [ ! -r /etc/os-release ]; then
  die 3 "缺少 /etc/os-release，无法识别系统（仅支持 CentOS/RHEL EL8/EL9）"
fi
# shellcheck disable=SC1091
. /etc/os-release
case "${ID:-}" in
  centos|rhel|rocky|almalinux|ol|eurolinux)
    ;;
  *)
    die 3 "不支持的发行版: ${ID:-unknown}（仅支持 CentOS/RHEL EL8/EL9）"
    ;;
esac
case "${VERSION_ID:-}" in
  8.*|9.*)
    ;;
  *)
    die 3 "不支持的 EL 版本: ${VERSION_ID:-unknown}（仅支持 EL8/EL9）"
    ;;
esac

MACH="$(uname -m)"
case "${MACH}" in
  x86_64)  ARCH="x86_64"; GOARCH="amd64" ;;
  aarch64) ARCH="aarch64"; GOARCH="arm64" ;;
  *) die 3 "不支持的 CPU 架构: ${MACH}（仅支持 x86_64 / aarch64）" ;;
esac

command -v openssl >/dev/null 2>&1 || die 3 "未找到 openssl，请先安装：dnf install -y openssl（或 yum install -y openssl）"
if command -v curl >/dev/null 2>&1; then
  DL="curl -fsSL --connect-timeout 15 --retry 3 -o"
elif command -v wget >/dev/null 2>&1; then
  DL="wget -q -O"
else
  die 3 "未找到 curl/wget，请先安装：dnf install -y curl"
fi
if ! command -v jq >/dev/null 2>&1 && ! command -v python3 >/dev/null 2>&1; then
  die 3 "未找到 jq 或 python3（用于重建签名消息与解析 manifest），请先安装其一：dnf install -y jq"
fi

info "目标: ${ID} ${VERSION_ID} / ${ARCH}，版本选择: ${VERSION}"

# ---------- 公钥 ----------
if [ -n "${PUBKEY_FILE}" ]; then
  [ -r "${PUBKEY_FILE}" ] || die_usage "无法读取公钥文件: ${PUBKEY_FILE}"
  info "使用发布公钥: ${PUBKEY_FILE}"
else
  warn "使用脚本内嵌的占位发布公钥（非真实密钥）。"
  warn "生产环境必须使用 --pubkey <file> 指定真实发布公钥，否则验签必然失败（fail-closed）。"
  PUBKEY_FILE="$(mktemp "${TMPDIR:-/tmp}/servercli-pubkey.XXXXXX")"
  printf '%s\n' "${EMBEDDED_PUBKEY_PEM}" > "${PUBKEY_FILE}"
  chmod 600 "${PUBKEY_FILE}"
fi

# ---------- 下载基地址 ----------
# GitHub 最新版资源 URL 形态: .../releases/latest/download/<asset>
# GitHub 指定 tag 资源 URL 形态: .../releases/download/<tag>/<asset>
# OSS 镜像按 <oss-base>/<version>/<asset> 布局（latest 目录对应最新版）。
if [ "${VERSION}" = "releases/latest" ]; then
  GH_DL_BASE="${GITHUB_BASE%/download}/latest/download"
  OSS_DL_BASE="${OSS_BASE%/}/latest"
else
  case "${VERSION}" in
    ''|.|..|*/*|*..*|*[!A-Za-z0-9._-]*)
      die_usage "非法 --version: ${VERSION}"
      ;;
  esac
  GH_DL_BASE="${GITHUB_BASE%/}/${VERSION}"
  OSS_DL_BASE="${OSS_BASE%/}/${VERSION}"
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/servercli-install.XXXXXX")"
chmod 700 "${WORK}"

# 下载一个 asset：GitHub 主源 -> OSS 回退；全部失败返回 1。
download() {
  asset="$1"
  out="$2"
  url="${GH_DL_BASE}/${asset}"
  if ${DL} "${out}" "${url}" 2>/dev/null; then
    info "  下载 [GitHub] ${url}"
    return 0
  fi
  if [ -n "${OSS_BASE}" ]; then
    url="${OSS_DL_BASE}/${asset}"
    if ${DL} "${out}" "${url}" 2>/dev/null; then
      info "  下载 [OSS]   ${url}"
      return 0
    fi
  fi
  return 1
}

# ---------- 1) 下载并校验 release-manifest.json（按平台命名） ----------
MANIFEST_ASSET="release-manifest-linux-${GOARCH}.json"
MANIFEST="${WORK}/release-manifest.json"
if ! download "${MANIFEST_ASSET}" "${MANIFEST}"; then
  die 5 "无法从 GitHub/OSS 下载 ${MANIFEST_ASSET}（可检查 --version / --github-base / --oss-base）"
fi
info "${MANIFEST_ASSET} 下载完成，开始验签"

# 重建签名消息（canonical form）：去除 signature 字段后按键名排序的紧凑 JSON。
# Go 侧等价实现：解码为 interface{} 后 json.Marshal（encoding/json 对 map 按键排序）。
if command -v jq >/dev/null 2>&1; then
  MSG="${WORK}/manifest.msg"
  jq -cS 'del(.signature)' "${MANIFEST}" > "${MSG}"
  SIG_B64="$(jq -r '.signature // empty' "${MANIFEST}")"
  RV="$(jq -r '.release_version // empty' "${MANIFEST}")"
else
  MSG="${WORK}/manifest.msg"
  python3 - "${MANIFEST}" "${MSG}" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
m.pop("signature", None)
open(sys.argv[2], "w").write(json.dumps(m, sort_keys=True, separators=(",", ":"), ensure_ascii=False))
PY
  SIG_B64="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("signature",""))' "${MANIFEST}")"
  RV="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("release_version",""))' "${MANIFEST}")"
fi

[ -n "${SIG_B64}" ] || die 4 "release-manifest.json 缺少 signature 字段（unsigned 产物会被拒绝）"

SIGFILE="${WORK}/manifest.sig"
printf '%s' "${SIG_B64}" | base64 -d > "${SIGFILE}" 2>/dev/null || die 4 "signature 不是合法 base64"

# 校验 Ed25519 签名：签名消息 = 去除 signature 后按键排序的紧凑 JSON（与
# Go CanonicalManifestBytes / CI python sort_keys 完全一致），Ed25519 对
# canonical 原文直接签名/验签（无 SHA-256 预哈希）。
if ! openssl pkeyutl -verify -rawin -pubin -inkey "${PUBKEY_FILE}" \
     -in "${MSG}" -sigfile "${SIGFILE}" 2>/dev/null; then
  die 4 "Release Manifest 签名校验失败（公钥: ${PUBKEY_FILE}）"
fi
info "Release Manifest 签名校验通过（release_version: ${RV}）"

# ---------- 2) 解析 artifacts 摘要表 ----------
[ -n "${RV}" ] || die 4 "release-manifest.json 缺少 release_version 字段"
case "${RV}" in
  ''|.|..|*/*|*..*|*[!A-Za-z0-9._-]*)
    die 4 "release_version 非法: ${RV}"
    ;;
esac
SAFE_VERSION="${RV}"
RELEASE_DIR="${RELEASES_DIR}/${SAFE_VERSION}"

ARTIFACTS_FILE="${WORK}/artifacts.tsv"
if command -v jq >/dev/null 2>&1; then
  jq -r '.artifacts[] | [.path, .kind, .sha256, (.size|tostring)] | @tsv' \
    "${MANIFEST}" > "${ARTIFACTS_FILE}"
else
  python3 - "${MANIFEST}" > "${ARTIFACTS_FILE}" <<'PY'
import json, sys
for a in json.load(open(sys.argv[1])).get("artifacts", []):
    print("\t".join([str(a.get("path", "")), str(a.get("kind", "")),
                     str(a.get("sha256", "")), str(a.get("size", ""))]))
PY
fi
if [ ! -s "${ARTIFACTS_FILE}" ]; then
  die 4 "release-manifest.json 中没有 artifacts"
fi

# ---------- 3) 逐个下载并校验 artifact，安装到 release 目录 ----------
mkdir -p "${INSTALL_ROOT}" "${RELEASES_DIR}"
rm -rf -- "${RELEASE_DIR}"
mkdir -p "${RELEASE_DIR}/bin" "${RELEASE_DIR}/deploy"

TAB="$(printf '\t')"
n=0
while IFS="${TAB}" read -r path kind sha size; do
  [ -n "${path}" ] || continue
  n=$((n + 1))

  # sha256 必须是 64 位十六进制
  sha_len="$(printf '%s' "${sha}" | wc -c | tr -d ' ')"
  if [ "${sha_len}" -ne 64 ]; then
    die 4 "artifact ${path} 的 sha256 非法: ${sha}"
  fi
  case "${sha}" in
    *[!0-9a-fA-F]*) die 4 "artifact ${path} 的 sha256 非法: ${sha}" ;;
  esac

  # 目录型 artifact（path 以 / 结尾）以 <name>.tar.gz 形式发布；
  # 其余 artifact 的 GitHub Release asset 名 = path 中 "/" 替换为 "-"
  # （GitHub asset 名不允许 "/"，CI 发布侧使用同样的映射）。
  case "${path}" in
    */) ASSET="$(printf '%s' "${path%/}").tar.gz" ;;
    *)  ASSET="$(printf '%s' "${path}" | tr '/' '-')" ;;
  esac

  info "下载并校验 artifact ${n}: ${path} (${kind})"
  ART="${WORK}/artifact-${n}"
  if ! download "${ASSET}" "${ART}"; then
    die 5 "下载 artifact 失败: ${ASSET}"
  fi

  if ! printf '%s  %s\n' "${sha}" "${ART}" | sha256sum -c - >/dev/null 2>&1; then
    die 4 "sha256 校验失败: ${path}（期望 ${sha}，来源 ${ASSET}）"
  fi

  case "${path}" in
    */)
      dirname="${path%/}"
      mkdir -p "${RELEASE_DIR}/${dirname}"
      tar -xzf "${ART}" -C "${RELEASE_DIR}/${dirname}" --strip-components=1 \
        || die 6 "解压 ${ASSET} 到 ${RELEASE_DIR}/${dirname} 失败"
      ;;
    *)
      case "${kind}" in
        binary|installer) mode=0755 ;;
        *) mode=0644 ;;
      esac
      install -D -m "${mode}" "${ART}" "${RELEASE_DIR}/${path}" \
        || die 6 "安装 ${path} 失败"
      ;;
  esac
done < "${ARTIFACTS_FILE}"

# 保留安装依据的 manifest（含签名与摘要表）
install -m 0644 "${MANIFEST}" "${RELEASE_DIR}/release-manifest.json"

# 基本完整性检查
for b in servercli servercli-control-plane servercli-node-agent; do
  [ -x "${RELEASE_DIR}/bin/${b}" ] || die 6 "缺少可执行文件: ${RELEASE_DIR}/bin/${b}"
done

# ---------- 4) 原子切换 current / previous ----------
# previous 记录旧 current；current 通过 staging 软链接 + rename 原子切换。
if [ -L "${INSTALL_ROOT}/current" ]; then
  OLD_CURRENT="$(readlink "${INSTALL_ROOT}/current" 2>/dev/null || true)"
  if [ -n "${OLD_CURRENT}" ] && [ "${OLD_CURRENT}" != "releases/${SAFE_VERSION}" ]; then
    ln -sfn "${OLD_CURRENT}" "${INSTALL_ROOT}/previous"
    info "previous -> ${OLD_CURRENT}"
  fi
fi
ln -sfn "releases/${SAFE_VERSION}" "${INSTALL_ROOT}/.current-staging"
mv -Tf "${INSTALL_ROOT}/.current-staging" "${INSTALL_ROOT}/current"
info "current -> releases/${SAFE_VERSION}"

info "ServerCLI 安装完成: ${RELEASE_DIR}"
info "  当前版本: /opt/servercli/current"
info "  之前版本: /opt/servercli/previous"

# ---------- 5) 创建固定目录（root-only） ----------
for d in /etc/servercli/private /etc/servercli/keys /etc/servercli/runtime \
         /var/lib/servercli/bootstrap /var/lib/servercli/postgres \
         /var/lib/servercli/state /var/lib/servercli/backups \
         /run/servercli/bootstrap /run/servercli/operations; do
  mkdir -p "$d"
  chmod 700 "$d"
done

# ---------- 6) 安装兼容入口包装（/opt/servercli/update.sh 等） ----------
if [ -d "${RELEASE_DIR}/deploy/compat" ]; then
  for wrapper in update backup restore; do
    if [ -f "${RELEASE_DIR}/deploy/compat/${wrapper}.sh" ]; then
      sed -e "s|{{SERVERCLI_BIN}}|${INSTALL_ROOT}/bin/servercli|g" \
          -e "s|{{SERVICELIST}}||g" \
          "${RELEASE_DIR}/deploy/compat/${wrapper}.sh" \
        > "/opt/servercli/${wrapper}.sh"
      chmod 0755 "/opt/servercli/${wrapper}.sh"
      info "兼容入口已安装: /opt/servercli/${wrapper}.sh"
    fi
  done
fi

# ---------- 7) 是否运行 servercli init ----------
run_init() {
  info "运行 servercli init ..."
  "${INSTALL_ROOT}/current/bin/servercli" init
}

if [ "${NO_INIT_PROMPT}" -eq 1 ]; then
  info "已跳过 servercli init（--no-init-prompt）"
elif [ "${YES}" -eq 1 ]; then
  run_init
elif [ -t 0 ] && [ -t 1 ]; then
  printf '是否现在运行 servercli init 初始化引导？[y/N] '
  read -r answer
  case "${answer}" in
    y|Y|yes|YES) run_init ;;
    *) info "跳过 init；可稍后手动执行: /opt/servercli/current/bin/servercli init" ;;
  esac
else
  info "非交互环境：仅安装，不运行 servercli init"
  info "之后可手动执行: /opt/servercli/current/bin/servercli init"
fi

exit 0
