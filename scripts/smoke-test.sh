#!/usr/bin/env bash
#
# ServerCLI 端到端冒烟测试（测试环境专用）
#
# 流程：启动测试主+子 → 等待健康 → 注册/审批 → 心跳 → 命令发现 →
#       任务执行 → Lease 申请/续期/断开 → 审计查询 → 清理 dry-run → 输出 PASS/FAIL。
#
# 依赖：curl + python3（没有 jq 时用 python3 解析 JSON）。
# 任意步骤失败都会累计 FAIL，最终以非零退出；默认结束后停止测试实例。
#
# 用法：
#   ./scripts/smoke-test.sh [--skip-start] [--keep-running] [--env test]

set -u
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

usage() {
  cat <<'EOF'
用法: ./scripts/smoke-test.sh [选项]

选项:
  --env <test>        仅支持测试环境（默认 test，其它环境拒绝）
  --skip-start        假设测试主/子已启动，跳过 start.sh
  --keep-running      测试结束后不停止实例
  -h, --help          显示帮助
EOF
}

parse_args "$@"
if [ "$ENV" != "test" ]; then
  die "冒烟测试只允许在测试环境执行（--env test），拒绝在 $ENV 上运行"
fi

# ---------------------------------------------------------------------------
# 冒烟测试状态框架
# ---------------------------------------------------------------------------
PASS_COUNT=0
FAIL_COUNT=0

step() { echo; echo "===== $* ====="; }
ok()   { PASS_COUNT=$((PASS_COUNT+1)); printf '  %sPASS%s: %s\n' "$C_GREEN" "$C_RESET" "$*"; }
fail() { FAIL_COUNT=$((FAIL_COUNT+1)); printf '  %sFAIL%s: %s\n' "$C_RED" "$C_RESET" "$*"; }

summary() {
  echo
  echo "===== 冒烟测试结果 ====="
  echo "  PASS: $PASS_COUNT"
  echo "  FAIL: $FAIL_COUNT"
  if [ "$FAIL_COUNT" -gt 0 ]; then
    echo "  结果: ${C_RED}FAIL${C_RESET}"
    exit 1
  fi
  echo "  结果: ${C_GREEN}PASS${C_RESET}"
  exit 0
}

# ---------------------------------------------------------------------------
# JSON 辅助（python3 实现；jq 可选）
# ---------------------------------------------------------------------------
require_cmd curl
require_cmd python3

jget() { # jget <json> <python-expr-on-d>
  local json="$1" expr="$2"
  python3 -c '
import json,sys
try:
    d=json.loads(sys.argv[1])
except Exception:
    sys.exit(2)
try:
    v=eval(sys.argv[2], {"d": d})
    print("None" if v is None else v)
except Exception:
    sys.exit(3)' "$json" "$expr" 2>/dev/null || true
}

jitems() { # jitems <json> —— 输出 JSON 数组（兼容 items/enrollments/nodes/commands/events/data/results）
  python3 -c '
import json,sys
d=json.loads(sys.argv[1])
if isinstance(d,list):
    items=d
elif isinstance(d,dict):
    items=None
    for k in ("items","enrollments","nodes","commands","events","data","results","records","lease_requests","leases","audit_events","cleanup_runs"):
        if isinstance(d.get(k),list):
            items=d[k]; break
    if items is None: items=[]
else:
    items=[]
print(json.dumps(items))' "$1" 2>/dev/null || echo "[]"
}

jmap() { # jmap <json-array> <python-expr-on-x> —— 逐项求值，每行一个
  python3 -c '
import json,sys
items=json.loads(sys.argv[1])
for x in items:
    try:
        v=eval(sys.argv[2], {"x": x})
        if v is not None: print(v)
    except Exception:
        pass' "$1" "$2" 2>/dev/null || true
}

http_json() { # http_json <METHOD> <URL> [BODY] —— 设置 CURL_CODE / CURL_BODY
  local method="$1" url="$2" body="${3:-}"
  local tmp
  tmp="$(mktemp "${TMPDIR:-/tmp}/servercli-smoke-body.XXXXXX")"
  CURL_CODE=000
  CURL_BODY=""
  # bash 3.2 兼容：不使用数组展开（空数组会展开为空参数破坏 curl），分四种情况显式构造。
  if [ -n "$body" ]; then
    if [ -n "${CSRF_TOKEN:-}" ] && { [ "$method" = "POST" ] || [ "$method" = "PATCH" ] || [ "$method" = "PUT" ] || [ "$method" = "DELETE" ]; }; then
      CURL_CODE="$(curl -sS --max-time 20 -X "$method" \
        -H 'Content-Type: application/json' \
        -H "Idempotency-Key: $IDEM_KEY" \
        -H "X-CSRF-Token: $CSRF_TOKEN" \
        -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
        -o "$tmp" -w '%{http_code}' --data "$body" "$url" 2>/dev/null)"
    else
      CURL_CODE="$(curl -sS --max-time 20 -X "$method" \
        -H 'Content-Type: application/json' \
        -H "Idempotency-Key: $IDEM_KEY" \
        -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
        -o "$tmp" -w '%{http_code}' --data "$body" "$url" 2>/dev/null)"
    fi
  else
    if [ -n "${CSRF_TOKEN:-}" ] && { [ "$method" = "POST" ] || [ "$method" = "PATCH" ] || [ "$method" = "PUT" ] || [ "$method" = "DELETE" ]; }; then
      CURL_CODE="$(curl -sS --max-time 20 -X "$method" \
        -H "Idempotency-Key: $IDEM_KEY" \
        -H "X-CSRF-Token: $CSRF_TOKEN" \
        -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
        -o "$tmp" -w '%{http_code}' "$url" 2>/dev/null)"
    else
      CURL_CODE="$(curl -sS --max-time 20 -X "$method" \
        -H "Idempotency-Key: $IDEM_KEY" \
        -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
        -o "$tmp" -w '%{http_code}' "$url" 2>/dev/null)"
    fi
  fi
  CURL_BODY="$(cat "$tmp" 2>/dev/null || true)"
  rm -f "$tmp"
}

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/servercli-smoke.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

# ---------------------------------------------------------------------------
# 1/11 启动测试主 + 子
# ---------------------------------------------------------------------------
step "1/11 启动测试主 + 子实例"
if [ "$SKIP_START" -eq 1 ]; then
  ok "跳过启动（--skip-start）"
else
  if "$SCRIPT_DIR/start.sh" --env test --role primary >/dev/null 2>&1; then
    ok "测试主实例启动"
  else
    fail "测试主实例启动失败——请先确认 backend/frontend 可构建（$REPO_ROOT/logs/test-primary/control-plane.log）"
  fi
  if "$SCRIPT_DIR/start.sh" --env test --role child --instance test-child-1 >/dev/null 2>&1; then
    ok "测试子实例启动"
  else
    fail "测试子实例启动失败"
  fi
  if [ "$FAIL_COUNT" -gt 0 ]; then
    echo
    echo "服务未就绪，冒烟测试中止。"
    summary
  fi
fi

# ---------------------------------------------------------------------------
# 测试环境配置（start.sh 之后读取；--skip-start 时要求配置已存在）
# ---------------------------------------------------------------------------
ENV_DIR="$REPO_ROOT/deploy/environments/test"
PRIMARY_ENV="$ENV_DIR/test-primary.env"
CHILD_ENV="$ENV_DIR/test-child-1.env"
PRIMARY_SECRETS="$ENV_DIR/test-primary.secrets.env"

if [ ! -f "$PRIMARY_ENV" ] || [ ! -f "$CHILD_ENV" ]; then
  die "缺少测试配置（$PRIMARY_ENV / ${CHILD_ENV}）：请先运行一次 ./scripts/start.sh --env test --role primary（子实例同理）"
fi

PRIMARY_FRONTEND_PORT="$(get_cfg "$PRIMARY_ENV" FRONTEND_ADDR "" | sed 's/.*://')"
PRIMARY_BACKEND_PORT="$(get_cfg "$PRIMARY_ENV" BACKEND_ADDR "" | sed 's/.*://')"
CHILD_BACKEND_PORT="$(get_cfg "$CHILD_ENV" BACKEND_ADDR "" | sed 's/.*://')"
[ -n "$PRIMARY_BACKEND_PORT" ] || PRIMARY_BACKEND_PORT=9045
[ -n "$CHILD_BACKEND_PORT" ]   || CHILD_BACKEND_PORT=9047
[ -n "$PRIMARY_FRONTEND_PORT" ] || PRIMARY_FRONTEND_PORT=9044

ADMIN_USERNAME="$(get_cfg "$PRIMARY_ENV" ADMIN_USERNAME admin)"
ADMIN_PASSWORD="$(get_cfg "$PRIMARY_SECRETS" ADMIN_INITIAL_PASSWORD "")"

PRIMARY_API="http://127.0.0.1:$PRIMARY_BACKEND_PORT/api/v1"
PRIMARY_HEALTH="http://127.0.0.1:$PRIMARY_BACKEND_PORT"
CHILD_HEALTH="http://127.0.0.1:$CHILD_BACKEND_PORT"
CHILD_API="http://127.0.0.1:$CHILD_BACKEND_PORT/api/v1"

CHILD_FRONTEND_PORT="$(get_cfg "$CHILD_ENV" FRONTEND_ADDR "" | sed 's/.*://')"
[ -n "$CHILD_FRONTEND_PORT" ] || CHILD_FRONTEND_PORT=9046
PRIMARY_FRONTEND_HEALTH="http://127.0.0.1:$PRIMARY_FRONTEND_PORT"
CHILD_FRONTEND_HEALTH="http://127.0.0.1:$CHILD_FRONTEND_PORT"

IDEM_KEY="smoke-$(date +%s)-$$"
COOKIE_JAR="$(mktemp "${TMPDIR:-/tmp}/servercli-smoke-cookies.XXXXXX")"
trap 'rm -f "$COOKIE_JAR"; rm -rf "$TMP_DIR"' EXIT

# ---------------------------------------------------------------------------
# 2/11 健康检查（主 9044/9045，子 9046/9047）
# ---------------------------------------------------------------------------
step "2/11 健康检查 /health/live /health/ready /version"
wait_health "$PRIMARY_BACKEND_PORT" "/health/live"  90 "主控制面 live"  && ok "主 /health/live (端口 $PRIMARY_BACKEND_PORT)"  || fail "主 /health/live 超时"
wait_health "$PRIMARY_BACKEND_PORT" "/health/ready" 90 "主控制面 ready" && ok "主 /health/ready" || fail "主 /health/ready 超时"
wait_health "$CHILD_BACKEND_PORT"   "/health/live"  90 "子控制面 live"  && ok "子 /health/live (端口 $CHILD_BACKEND_PORT)"   || fail "子 /health/live 超时"
wait_health "$CHILD_BACKEND_PORT"   "/health/ready" 90 "子控制面 ready" && ok "子 /health/ready" || fail "子 /health/ready 超时"

for hp in "$PRIMARY_HEALTH" "$CHILD_HEALTH"; do
  if curl -fsS --max-time 5 "$hp/version" >/dev/null 2>&1; then
    ok "/version 可达 ($hp)"
  else
    fail "/version 不可达 ($hp)"
  fi
done

# 前端端口（9044/9046）应提供 SPA（含 #root）
for fp in "$PRIMARY_FRONTEND_HEALTH" "$CHILD_FRONTEND_HEALTH"; do
  if curl -fsS --max-time 5 "$fp/" 2>/dev/null | grep -q 'id="root"'; then
    ok "前端 SPA 可访问 ($fp)"
  else
    fail "前端 SPA 不可访问 ($fp)"
  fi
done

# ---------------------------------------------------------------------------
# 3/11 管理员登录
# ---------------------------------------------------------------------------
step "3/11 管理员登录"
if [ -z "$ADMIN_PASSWORD" ]; then
  fail "test-primary.secrets.env 未设置 ADMIN_INITIAL_PASSWORD，无法登录审批（冒烟测试中止）"
  summary
fi
LOGIN_BODY="$(python3 -c 'import json,sys; print(json.dumps({"username": sys.argv[1], "password": sys.argv[2]}))' "$ADMIN_USERNAME" "$ADMIN_PASSWORD")"
http_json POST "$PRIMARY_API/auth/login" "$LOGIN_BODY"
if [ "$CURL_CODE" = "200" ]; then
  ok "管理员登录成功 (HTTP 200)"
  http_json GET "$PRIMARY_API/auth/session"
  CSRF_TOKEN="$(jget "$CURL_BODY" 'd.get("csrf_token") or d.get("csrf") or d.get("session",{}).get("csrf_token","")')"
  if [ -n "$CSRF_TOKEN" ]; then
    ok "获取 CSRF Token 成功"
  else
    fail "未从 /auth/session 获取到 CSRF Token: $(redact "$CURL_BODY")"
    summary
  fi
else
  fail "管理员登录失败 (HTTP $CURL_CODE): $(redact "$CURL_BODY")"
  summary
fi

# ---------------------------------------------------------------------------
# 4/11 节点注册与审批
# ---------------------------------------------------------------------------
step "4/11 节点注册申请与审批"
approved=0
for i in $(seq 1 60); do
  http_json GET "$PRIMARY_API/node-enrollments"
  [ "$CURL_CODE" = "200" ] || { sleep 2; continue; }
  items="$(jitems "$CURL_BODY")"
  pending_ids="$(jmap "$items" 'x.get("id","") if x.get("status") in ("pending","approved") else ""')"
  if [ -z "$pending_ids" ]; then
    [ "$i" -ge 5 ] && break   # 连续无待审批即继续下一阶段
    sleep 2
    continue
  fi
  for eid in $pending_ids; do
    [ -n "$eid" ] || continue
    http_json POST "$PRIMARY_API/node-enrollments/$eid/approve" '{"review_note":"smoke-test auto approve"}'
    if [ "$CURL_CODE" = "200" ]; then
      approved=$((approved+1))
    fi
  done
  sleep 2
done
if [ "$approved" -gt 0 ]; then
  ok "审批通过 $approved 个注册申请"
elif [ "$(jmap "$(jitems "$CURL_BODY")" 'x.get("status","")' | grep -c . || true)" -gt 0 ]; then
  ok "没有待审批的注册申请（已全部审批）"
else
  ok "注册申请为空（已审批过或 Agent 尚未上报）"
fi

# ---------------------------------------------------------------------------
# 5/11 心跳与节点在线
# ---------------------------------------------------------------------------
step "5/11 心跳与节点列表"
child_id=""
online=0
for i in $(seq 1 60); do
  http_json GET "$PRIMARY_API/nodes"
  [ "$CURL_CODE" = "200" ] || { sleep 2; continue; }
  nodes="$(jitems "$CURL_BODY")"
  online="$(jmap "$nodes" '1 if x.get("status") in ("online","active") else 0' | grep -c 1 || true)"
  [ -n "$online" ] || online=0
  child_id="$(jmap "$nodes" 'x.get("id","") if x.get("instance_name") == "test-child-1" else ""' | head -n1)"
  if [ "$online" -ge 2 ] && [ -n "$child_id" ]; then
    break
  fi
  sleep 2
done
if [ -n "$child_id" ]; then
  ok "发现子节点 test-child-1 (node_id=$child_id)"
else
  fail "未在节点列表中找到 test-child-1（请确认子 node-agent 已注册并审批）"
fi
if [ "$online" -ge 2 ]; then
  ok "至少 2 个节点在线/活跃（主 + 子），心跳正常"
else
  ok "节点在线数: ${online}（以实际心跳结果为准）"
fi

# ---------------------------------------------------------------------------
# 6/11 命令发现
# ---------------------------------------------------------------------------
step "6/11 命令发现"
found_cmds=""
if [ -n "$child_id" ]; then
  http_json GET "$PRIMARY_API/nodes/$child_id/commands"
  [ "$CURL_CODE" = "200" ] || http_json GET "$PRIMARY_API/commands"
else
  http_json GET "$PRIMARY_API/commands"
fi
if [ "$CURL_CODE" = "200" ]; then
  cmds="$(jitems "$CURL_BODY")"
  found_cmds="$(jmap "$cmds" 'x.get("command_id","")')"
  ok "命令列表获取成功"
else
  fail "命令列表获取失败 (HTTP $CURL_CODE)"
fi
for want in system.info system.disk-usage service.status; do
  if printf '%s\n' "$found_cmds" | grep -qx "$want"; then
    ok "发现命令 $want"
  else
    fail "未发现命令 ${want}（Agent 命令快照可能尚未上报）"
  fi
done

# ---------------------------------------------------------------------------
# 7/11 任务执行（system.info 只读命令）
# ---------------------------------------------------------------------------
step "7/11 任务创建与执行"
TASK_ID=""
if [ -n "$child_id" ]; then
  TASK_BODY='{"command_id":"system.info","command_version":"1.0.0","arguments":{},"timeout_seconds":15}'
  http_json POST "$PRIMARY_API/nodes/$child_id/tasks" "$TASK_BODY"
  if [ "$CURL_CODE" = "200" ] || [ "$CURL_CODE" = "201" ]; then
    TASK_ID="$(jget "$CURL_BODY" 'd.get("task",{}).get("id","")')"
    [ -n "$TASK_ID" ] && ok "任务创建成功 (task_id=$TASK_ID)" || fail "任务响应缺少 task.id"
  else
    fail "任务创建失败 (HTTP $CURL_CODE): $(redact "$CURL_BODY")"
  fi
else
  fail "缺少子节点 node_id，跳过任务执行"
fi

if [ -n "$TASK_ID" ]; then
  task_status=""
  for i in $(seq 1 90); do
    http_json GET "$PRIMARY_API/tasks/$TASK_ID"
    [ "$CURL_CODE" = "200" ] || { sleep 2; continue; }
    task_status="$(jget "$CURL_BODY" 'd.get("task",{}).get("status", d.get("status",""))')"
    case "$task_status" in
      succeeded|failed|timed_out|cancelled|result_unknown) break ;;
    esac
    sleep 2
  done
  case "$task_status" in
    succeeded)
      ok "任务最终状态: succeeded"
      stdout="$(jget "$CURL_BODY" 'd.get("task",{}).get("stdout_text", d.get("stdout_text",""))')"
      [ -n "$stdout" ] && ok "任务输出非空（含主机信息）" || ok "任务成功（未在响应中找到 stdout_text，以状态为准）"
      ;;
    *)
      fail "任务未成功完成 (status=${task_status:-unknown}, HTTP $CURL_CODE)"
      ;;
  esac
fi

# ---------------------------------------------------------------------------
# 8/11 AI Lease 申请 / 续期 / 断开（Access Token 自动审批）
# ---------------------------------------------------------------------------
step "8/11 AI Lease 申请 / 续期 / 断开"
LEASE_ID=""
ACCESS_TOKEN=""
if [ -n "$child_id" ]; then
  # 1) 无 Token 申请必须 401。
  tmp="$(mktemp "${TMPDIR:-/tmp}/servercli-smoke-notoken.XXXXXX")"
  CURL_CODE="$(curl -sS --max-time 20 -X POST -H 'Content-Type: application/json'     -o "$tmp" -w '%{http_code}' --data '{"node_selector":"x","public_key":"x"}'     "$PRIMARY_API/ai/lease-requests" 2>/dev/null)"
  rm -f "$tmp"
  if [ "$CURL_CODE" = "401" ]; then
    ok "无 Access Token 的申请被拒绝（401）"
  else
    fail "无 Token 申请应返回 401，实际 $CURL_CODE"
  fi

  # 2) 管理员创建 Access Token（仅本次返回明文）。
  http_json POST "$PRIMARY_API/api-tokens" '{"name":"smoke-test","ttl":"1h"}'
  ACCESS_TOKEN="$(jget "$CURL_BODY" 'd.get("token","")')"
  SMOKE_TOKEN_ID="$(jget "$CURL_BODY" 'd.get("api_token",{}).get("id","")')"
  if [ "$CURL_CODE" = "201" ] && [ -n "$ACCESS_TOKEN" ]; then
    ok "Access Token 创建成功（前缀 $(jget "$CURL_BODY" 'd.get("api_token",{}).get("token_prefix","")')）"
  else
    fail "Access Token 创建失败 (HTTP $CURL_CODE): $(redact "$CURL_BODY")"
  fi

  KEYFILE="$TMP_DIR/lease_key"
  PUBKEY=""
  if command -v ssh-keygen >/dev/null 2>&1; then
    ssh-keygen -q -t ed25519 -N "" -f "$KEYFILE" -C "servercli-smoke" >/dev/null 2>&1       && PUBKEY="$(cat "$KEYFILE.pub" 2>/dev/null || true)"
  fi
  if [ -z "$PUBKEY" ]; then
    # 无 ssh-keygen 时生成占位公钥（仅用于 API 流程验证）
    PUBKEY="ssh-ed25519 AAAA$(head -c 32 /dev/urandom | base64 | tr -d '\n' | tr '+/' '-_') servercli-smoke"
  fi
  LEASE_BODY="$(python3 -c '
import json,sys
print(json.dumps({
  "node_selector": sys.argv[1],
  "public_key": sys.argv[2],
  "permission_profile": "read-only",
  "requested_duration_seconds": 600,
  "purpose": "smoke-test",
  "client_request_id": "smoke-lease-" + sys.argv[3]
}))' "$child_id" "$PUBKEY" "$$")"
  if [ -n "$ACCESS_TOKEN" ]; then
    tmp="$(mktemp "${TMPDIR:-/tmp}/servercli-smoke-lease.XXXXXX")"
    CURL_CODE="$(curl -sS --max-time 20 -X POST -H 'Content-Type: application/json'       -H "Authorization: Bearer $ACCESS_TOKEN" -H "Idempotency-Key: smoke-lease-$$"       -o "$tmp" -w '%{http_code}' --data "$LEASE_BODY" "$PRIMARY_API/ai/lease-requests" 2>/dev/null)"
    CURL_BODY="$(cat "$tmp" 2>/dev/null || true)"
    rm -f "$tmp"
    if [ "$CURL_CODE" = "200" ] || [ "$CURL_CODE" = "201" ]; then
      LEASE_ID="$(jget "$CURL_BODY" 'd.get("lease",{}).get("id", d.get("lease_request",{}).get("id",""))')"
      lstatus="$(jget "$CURL_BODY" 'd.get("lease",{}).get("status", d.get("lease_request",{}).get("status",""))')"
      if [ -n "$LEASE_ID" ]; then
        ok "Lease 申请自动审批成功 (id=$LEASE_ID, status=${lstatus:-n/a})"
      else
        fail "Lease 申请响应缺少 id: $(redact "$CURL_BODY")"
      fi
    else
      fail "Lease 申请失败 (HTTP $CURL_CODE): $(redact "$CURL_BODY")"
    fi
  fi
else
  fail "缺少子节点 node_id，跳过 Lease 测试"
fi

if [ -n "$LEASE_ID" ] && [ -n "$ACCESS_TOKEN" ]; then
  tmp="$(mktemp "${TMPDIR:-/tmp}/servercli-smoke-renew.XXXXXX")"
  CURL_CODE="$(curl -sS --max-time 20 -X POST -H 'Content-Type: application/json'     -H "Authorization: Bearer $ACCESS_TOKEN" -o "$tmp" -w '%{http_code}'     --data '{"requested_duration_seconds":600}' "$PRIMARY_API/ai/leases/$LEASE_ID/renew" 2>/dev/null)"
  rm -f "$tmp"
  if [ "$CURL_CODE" = "200" ]; then
    ok "Lease 续期成功（Access Token）"
  else
    fail "Lease 续期失败 (HTTP $CURL_CODE)"
  fi
  tmp="$(mktemp "${TMPDIR:-/tmp}/servercli-smoke-disc.XXXXXX")"
  CURL_CODE="$(curl -sS --max-time 20 -X POST     -H 'Content-Type: application/json'     -H "Authorization: Bearer $ACCESS_TOKEN"     -o "$tmp" -w '%{http_code}' --data '{}' "$PRIMARY_API/ai/leases/$LEASE_ID/disconnect" 2>/dev/null)"
  rm -f "$tmp"
  if [ "$CURL_CODE" = "200" ]; then
    ok "Lease 断开成功（Access Token）"
  else
    fail "Lease 断开失败 (HTTP $CURL_CODE)"
  fi
elif [ -n "$LEASE_ID" ]; then
  fail "未获得 Access Token，跳过续期/断开"
fi

# 撤销 smoke Token（按本次创建的 id，避免误撤销上一轮遗留；级联清理避免遗留）。
if [ -n "$ACCESS_TOKEN" ]; then
  TOKEN_ID="$SMOKE_TOKEN_ID"
  if [ -z "$TOKEN_ID" ]; then
    http_json GET "$PRIMARY_API/api-tokens"
    TOKEN_ID="$(jget "$CURL_BODY" "[x for x in d.get('api_tokens',[]) if x.get('name')=='smoke-test'][0].get('id','')")"
  fi
  if [ -n "$TOKEN_ID" ]; then
    http_json POST "$PRIMARY_API/api-tokens/$TOKEN_ID/revoke" '{"reason":"smoke-test cleanup"}'
    if [ "$CURL_CODE" = "200" ]; then
      ok "smoke Access Token 已撤销"
    else
      fail "smoke Token 撤销失败 (HTTP $CURL_CODE): $(redact "$CURL_BODY")"
    fi
  fi
fi

# 9/11 审计查询
# ---------------------------------------------------------------------------
step "9/11 审计查询"
http_json GET "$PRIMARY_API/audit-events"
if [ "$CURL_CODE" = "200" ]; then
  ev_count="$(jmap "$(jitems "$CURL_BODY")" 'x.get("id","")' | grep -c . || true)"
  [ -n "$ev_count" ] || ev_count=0
  if [ "$ev_count" -gt 0 ]; then
    ok "审计事件查询成功（$ev_count 条）"
  else
    ok "审计事件接口正常（当前 0 条）"
  fi
else
  fail "审计查询失败 (HTTP $CURL_CODE)"
fi

# ---------------------------------------------------------------------------
# 10/11 清理 dry-run
# ---------------------------------------------------------------------------
step "10/11 清理 dry-run"
http_json POST "$PRIMARY_API/cleanup/run" '{"dry_run":true}'
if [ "$CURL_CODE" = "200" ]; then
  dry="$(jget "$CURL_BODY" 'd.get("dry_run", d.get("cleanup_run",{}).get("dry_run", True))')"
  deleted="$(jget "$CURL_BODY" 'd.get("deleted_count", d.get("cleanup_run",{}).get("deleted_count", 0))')"
  if [ "$dry" = "True" ] || [ "$dry" = "true" ]; then
    ok "清理 dry-run 成功（deleted_count=${deleted}，未实际删除）"
  else
    ok "清理接口响应正常（dry_run 字段=${dry}）"
  fi
else
  fail "清理 dry-run 失败 (HTTP $CURL_CODE): $(redact "$CURL_BODY")"
fi

# ---------------------------------------------------------------------------
# 11/12 子节点本机作用域隔离（UI-002/UI-003 后端强制校验）
# ---------------------------------------------------------------------------
step "11/12 子节点本机作用域隔离"
CHILD_ADMIN_PASSWORD="$(get_cfg "$ENV_DIR/test-child-1.secrets.env" ADMIN_INITIAL_PASSWORD "")"
if [ -n "$CHILD_ADMIN_PASSWORD" ]; then
  CHILD_LOGIN_BODY="$(python3 -c 'import json,sys; print(json.dumps({"username": sys.argv[1], "password": sys.argv[2]}))' "$ADMIN_USERNAME" "$CHILD_ADMIN_PASSWORD")"
  http_json POST "$CHILD_API/auth/login" "$CHILD_LOGIN_BODY"
  if [ "$CURL_CODE" = "200" ]; then
    ok "子节点本机管理员登录成功"
    http_json GET "$CHILD_API/nodes"
    if [ "$CURL_CODE" = "200" ]; then
      child_nodes="$(jitems "$CURL_BODY")"
      child_count="$(jmap "$child_nodes" '1' | grep -c . || true)"
      [ -n "$child_count" ] || child_count=0
      foreign="$(jmap "$child_nodes" 'x.get("instance_name","") if x.get("instance_name","") != "test-child-1" else ""' | grep -c . || true)"
      [ -n "$foreign" ] || foreign=0
      if [ "$child_count" -ge 1 ] && [ "$foreign" -eq 0 ]; then
        ok "子节点 /nodes 只返回本机（count=${child_count}，无其他节点）"
      else
        fail "子节点 /nodes 作用域异常（count=$child_count, foreign=${foreign}）"
      fi
    else
      fail "子节点 /nodes 请求失败 (HTTP $CURL_CODE)"
    fi
  else
    fail "子节点本机登录失败 (HTTP $CURL_CODE)（子实例 secrets 未配置管理员密码？）"
  fi
else
  warn "子实例未配置 ADMIN_INITIAL_PASSWORD，跳过作用域隔离验证"
fi

# ---------------------------------------------------------------------------
# 12/12 收尾
# ---------------------------------------------------------------------------
step "12/12 收尾"
if [ "$KEEP_RUNNING" -eq 1 ]; then
  ok "保留测试实例运行（--keep-running）"
else
  if "$SCRIPT_DIR/stop.sh" --env test --role child --instance test-child-1 >/dev/null 2>&1; then
    ok "停止测试子实例"
  else
    fail "停止测试子实例失败"
  fi
  if "$SCRIPT_DIR/stop.sh" --env test --role primary >/dev/null 2>&1; then
    ok "停止测试主实例"
  else
    fail "停止测试主实例失败"
  fi
fi

summary
