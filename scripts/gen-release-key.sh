#!/bin/sh
# =============================================================================
# 一次性生成 ServerCLI 发布签名密钥（Ed25519）
#
# 用途：生成一把"发布签名密钥"：
#   - 私钥（release-signing-key.pem）：只保留在你信任的机器上，离线保管；
#     其 base64 粘贴到 GitHub 仓库 Secret SERVERCLI_RELEASE_SIGNING_KEY，
#     CI 用它给 Release Manifest 签名；
#   - 公钥（release-signing-key.pub.pem）：分发给服务器，
#     安装器 --pubkey 与 servercli --pubkey-file 使用。
#
# 安全约定：
#   - 私钥 0600、目录 0700，绝不上传/提交/进入日志；
#   - 默认不把私钥 base64 打印到终端（写入 0600 文件），用 --print 才打印；
#   - 本脚本在你自己的受信任机器上运行，不要在共享/CI 环境生成。
#
# 用法：scripts/gen-release-key.sh [输出目录，默认 ./release-keys] [--print]
#   退出码：0 成功；1 缺依赖/openssl 不支持 Ed25519；2 用法错误
# =============================================================================
set -euo pipefail

OUT_DIR="${1:-./release-keys}"
PRINT=0
for arg in "$@"; do
  case "${arg}" in
    --print) PRINT=1 ;;
    -h|--help)
      sed -n '1,40p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    -*) echo "gen-release-key: 未知参数: ${arg}" >&2; exit 2 ;;
  esac
done

if ! command -v openssl >/dev/null 2>&1; then
  echo "gen-release-key: 需要 openssl（OpenSSL 3.x，支持 Ed25519）" >&2
  exit 1
fi

# ---- 自检：当前 openssl 是否支持 Ed25519 ----
TMP="$(mktemp -d "${TMPDIR:-/tmp}/gen-release-key.XXXXXX")"
trap 'rm -rf -- "${TMP}"' EXIT
if ! openssl genpkey -algorithm ED25519 -out "${TMP}/probe.pem" >/dev/null 2>&1; then
  echo "gen-release-key: 当前 openssl 不支持 Ed25519（需 OpenSSL 3.x）" >&2
  echo "  Linux: sudo dnf install -y openssl  # EL8/EL9 默认已是 3.x" >&2
  echo "  macOS 自带的 LibreSSL 不支持，请在 Linux 机器上执行本脚本" >&2
  exit 1
fi

umask 077
mkdir -p "${OUT_DIR}"
chmod 700 "${OUT_DIR}"

PRIV="${OUT_DIR}/release-signing-key.pem"
PUB="${OUT_DIR}/release-signing-key.pub.pem"
B64_FILE="${OUT_DIR}/SERVERCLI_RELEASE_SIGNING_KEY.txt"

# 已存在则不覆盖（避免误换密钥导致已装服务器验签失败）。
if [ -f "${PRIV}" ] || [ -f "${PUB}" ] || [ -f "${B64_FILE}" ]; then
  echo "gen-release-key: 输出目录已有密钥文件，不覆盖：${OUT_DIR}" >&2
  echo "  如需重新生成，请先删除或换一个输出目录。" >&2
  exit 1
fi

openssl genpkey -algorithm ED25519 -out "${PRIV}"
chmod 600 "${PRIV}"
openssl pkey -in "${PRIV}" -pubout -out "${PUB}"
chmod 644 "${PUB}"

# base64 单行写入 0600 文件（GitHub Secret 用）
if command -v base64 >/dev/null 2>&1; then
  if base64 -w0 "${PRIV}" >/dev/null 2>&1; then
    base64 -w0 "${PRIV}" > "${B64_FILE}"
  else
    base64 "${PRIV}" | tr -d '\n' > "${B64_FILE}"
  fi
else
  echo "gen-release-key: 未找到 base64 命令，请手动执行: base64 -w0 ${PRIV}" >&2
  exit 1
fi
chmod 600 "${B64_FILE}"

# ---- 自验：签名 + 验签往返 ----
printf 'gen-release-key self-test\n' > "${TMP}/msg"
openssl pkeyutl -sign -rawin -inkey "${PRIV}" -in "${TMP}/msg" -out "${TMP}/sig"
if ! openssl pkeyutl -verify -rawin -pubin -inkey "${PUB}" -in "${TMP}/msg" -sigfile "${TMP}/sig" >/dev/null 2>&1; then
  echo "gen-release-key: 自检验签失败，请检查 openssl 环境" >&2
  exit 1
fi

FINGERPRINT="$(openssl pkey -in "${PRIV}" -pubout -outform DER 2>/dev/null | openssl dgst -sha256 2>/dev/null | awk '{print $2}')"

echo ""
echo "================ ServerCLI 发布签名密钥已生成 ================"
echo "输出目录 : ${OUT_DIR}"
echo "私钥文件 : ${PRIV}          （0600，离线保管，勿提交/勿外传）"
echo "公钥文件 : ${PUB}           （分发给服务器，安装器 --pubkey 用）"
echo "Secret 值: ${B64_FILE}      （0600，粘贴到 GitHub Secret）"
echo "公钥指纹 : ${FINGERPRINT}"
echo ""
echo "下一步（GitHub）："
echo "  1) 打开仓库 Settings -> Secrets and variables -> Actions -> New secret"
echo "  2) Name 填 SERVERCLI_RELEASE_SIGNING_KEY，Value 填 ${B64_FILE} 的内容"
echo "     （复制用: cat ${B64_FILE} | pbcopy   # macOS）"
echo ""
echo "下一步（服务器安装）："
echo "  sudo bash deploy/install-servercli.sh --proxy http://127.0.0.1:8118 \\"
echo "      --pubkey ${PUB} --yes"
echo ""
echo "安全提醒：私钥只存在于 ${OUT_DIR}，请做好备份并离线保存；"
echo "任何人拿到私钥都能签发被服务器信任的 Release。"
echo "============================================================"

if [ "${PRINT}" -eq 1 ]; then
  echo ""
  echo "----- SERVERCLI_RELEASE_SIGNING_KEY（base64 单行） -----"
  cat "${B64_FILE}"
  echo ""
  echo "---------------------------------------------------------"
fi

exit 0
