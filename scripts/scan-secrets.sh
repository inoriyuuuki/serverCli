#!/bin/sh
# =============================================================================
# scan-secrets.sh — 仓库 Secret 扫描
#
# 扫描当前工作树（默认当前目录），查找可能泄露到仓库的敏感内容：
#   - 私钥块（BEGIN ... PRIVATE KEY / BEGIN OPENSSH PRIVATE KEY）
#   - age 密钥（AGE-SECRET-KEY-...）
#   - AWS Access Key（AKIA/ASIA）
#   - 阿里云 OSS AccessKey（LTAI）
#   - 通用 Token（ghp_ / github_pat_ / sk-）
#   - 认证 URL（https?://user:pass@...）
#   - 宽松文件权限（权限位 777）
#   - 脚本引入 secrets 类文件（source 命令）
#   - 明文密码赋值（PASSWORD 等变量直接赋非空且非占位值）
#
# 排除目录：.git、node_modules、frontend/dist（及所有 dist）、bin/、release/、
#           logs/、state/、.tmp/
# 排除文件：*.map、*.min.js
#
# 用法：scripts/scan-secrets.sh [目录，默认当前目录] [--include-tests]
#   --include-tests  同时扫描 *_test.go 测试夹具（发布门禁必须启用并人工复核命中）
# 退出码：0 = 无命中（输出 OK）；1 = 有命中；2 = 用法/内部错误
# =============================================================================
set -eu

ROOT="$(pwd)"
INCLUDE_TESTS=0
for arg in "$@"; do
  case "${arg}" in
    --include-tests) INCLUDE_TESTS=1 ;;
    -*) echo "scan-secrets: 未知参数 ${arg}" >&2; exit 2 ;;
    *) ROOT="${arg}" ;;
  esac
done
if [ ! -d "${ROOT}" ]; then
  echo "scan-secrets: 目录不存在: ${ROOT}" >&2
  exit 2
fi
cd "${ROOT}" || exit 2

HITS_FILE="$(mktemp "${TMPDIR:-/tmp}/scan-secrets-hits.XXXXXX")"
trap 'rm -f -- "${HITS_FILE}"' 0 1 2 15

# ---------- 检测模式（扩展正则） ----------
PAT_PRIVATE_KEY='-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----'
PAT_AGE='AGE-SECRET-KEY-[0-9A-Za-z]{10,}'
PAT_AWS='(AKIA|ASIA)[0-9A-Z]{16}'
PAT_OSS='LTAI[0-9A-Za-z]{12,}'
PAT_TOKEN='(ghp_[0-9A-Za-z]{20,}|github_pat_[0-9A-Za-z_]{20,}|sk-[A-Za-z0-9]{16,})'
PAT_AUTH_URL='[a-zA-Z][a-zA-Z0-9+.-]*://[^/@[:space:]]+:[^/@[:space:]]+@'
PAT_CHMOD='chmod[[:space:]]+(-R[[:space:]]+)?0?777([[:space:]]|$)'
PAT_SOURCE='(^|[^A-Za-z0-9_])(source|\.)[[:space:]]+[^[:space:]]*secrets'
# PASSWORD 赋值检测：后跟非空值；变量引用/占位值在下面的过滤器中排除
PAT_PASSWORD='(^|[^A-Za-z0-9_])PASSWORD[[:space:]]*=[[:space:]]*["'"'"']?[^"'"'"'[:space:]]{2,}'
# 明文密码命中后的过滤：shell 变量/命令替换/占位符不视为明文泄露
PASS_FILTER='\$|<[^>]*>|placeholder|changeme|change_me|your[-_][A-Za-z]|xxxx+|example|_TEST_|test_'

files_to_scan() {
  # 注意：排除组必须放在最前（不能以 -type f 开头），否则 -prune 对目录
  # 会因短路求值而不执行，导致 .git/.tmp/node_modules 等目录被继续扫描。
  # *_test.go 测试夹具（如脱敏测试里的假私钥块）默认跳过，--include-tests 时纳入。
  if [ "${INCLUDE_TESTS}" = "1" ]; then
    find . \( -path './.git' -o -name node_modules -o -path './frontend/dist' \
         -o -path './bin' -o -path './release' -o -path './logs' \
         -o -path './state' -o -path './.tmp' -o -path '*/dist' \
         -o -name '*.map' -o -name '*.min.js' \) \
      -prune -o -type f -print0
  else
    find . \( -path './.git' -o -name node_modules -o -path './frontend/dist' \
         -o -path './bin' -o -path './release' -o -path './logs' \
         -o -path './state' -o -path './.tmp' -o -path '*/dist' \
         -o -name '*.map' -o -name '*.min.js' -o -name '*_test.go' \) \
      -prune -o -type f -print0
  fi
}

scan_files() {
  name="$1"
  pat="$2"
  extra="${3:-}"
  files_to_scan | xargs -0 -r grep -HnIE -- "${pat}" 2>/dev/null | while IFS= read -r line; do
    if [ -n "${extra}" ]; then
      printf '%s\n' "${line}" | grep -Eiq "${extra}" && continue
    fi
    printf '%s\t%s\n' "${name}" "${line}" >> "${HITS_FILE}"
  done
}

scan_files "private-key"     "${PAT_PRIVATE_KEY}"
# DSN 命中过滤：${变量} / CHANGE_ME / example / xxx 等占位不视为泄露
DSN_FILTER='\$\{|CHANGE_ME|change_me|example|xxxx+|placeholder|your[-_][A-Za-z]|localhost|127\.0\.0\.1'
scan_files "dsn"             "${PAT_AUTH_URL}" "${DSN_FILTER}"
scan_files "age-secret-key"  "${PAT_AGE}"
scan_files "aws-access-key"  "${PAT_AWS}"
scan_files "oss-access-key"  "${PAT_OSS}"
scan_files "token"           "${PAT_TOKEN}"
# auth-url is covered by the scheme-agnostic "dsn" pattern above.
scan_files "chmod-777"       "${PAT_CHMOD}"
scan_files "source-secrets"  "${PAT_SOURCE}"
scan_files "plaintext-password" "${PAT_PASSWORD}" "${PASS_FILTER}"

TAB="$(printf '\t')"
COUNT="$(wc -l < "${HITS_FILE}" | tr -d ' ')"
if [ "${COUNT}" -gt 0 ]; then
  echo "scan-secrets: 发现 ${COUNT} 处敏感内容（file:line: 内容）："
  while IFS="${TAB}" read -r name loc; do
    echo "scan-secrets: HIT [${name}] ${loc}"
  done < "${HITS_FILE}"
  echo "scan-secrets: 请清除上述内容后再提交；必要时使用 Secret 管理/环境注入。"
  exit 1
fi

echo "scan-secrets: OK（无命中）"
exit 0
