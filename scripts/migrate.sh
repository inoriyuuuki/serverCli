#!/usr/bin/env bash
#
# ServerCLI 数据库迁移脚本：只执行迁移（构建后运行控制面的 --migrate-only）
#
# 用法：
#   ./scripts/migrate.sh --env test --role primary
#   ./scripts/migrate.sh --env production --role primary --confirm-production
#
# 安全约定（doc/03 §6.1）：迁移前自动备份；无法备份时正式环境拒绝继续。
# 幂等：迁移记录由控制面管理，重复执行不会重复应用已应用迁移。

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

usage() {
  cat <<'EOF'
用法: ./scripts/migrate.sh [选项]

选项:
  --env <test|production>   环境（默认 test）
  --role <primary|child>    节点角色（必填）
  --instance <NAME>         实例名
  --confirm-production      正式环境二次确认（production 必填）
  --rebuild                 强制重新构建后端二进制
  --no-build                使用已存在二进制，不构建
  -h, --help                显示帮助
EOF
}

parse_args "$@"
[ -n "$ROLE" ] || die "缺少 --role（primary / child）"
resolve_instance
require_production_confirm
load_instance_config

# ---------------------------------------------------------------------------
# 阶段 1：确保二进制
# ---------------------------------------------------------------------------
if [ -x "$BIN_DIR/$CONTROL_PLANE_BIN" ] && [ "$REBUILD" -eq 0 ]; then
  info "阶段 1/3: 使用已存在二进制 $BIN_DIR/$CONTROL_PLANE_BIN"
elif [ "$NO_BUILD" -eq 1 ]; then
  die "阶段失败: 二进制不存在且指定了 --no-build: $BIN_DIR/$CONTROL_PLANE_BIN"
else
  build_backend
fi

# ---------------------------------------------------------------------------
# 阶段 2：迁移前备份
# ---------------------------------------------------------------------------
backup_before_migrate() {
  local backup_dir="$STATE_DIR/backups" stamp db_file
  mkdir -p "$backup_dir"
  chmod 700 "$STATE_DIR" "$backup_dir"
  stamp="$(date +%Y%m%d-%H%M%S)"

  if [ "$DATABASE_DRIVER" = "postgres" ]; then
    [ -n "${DATABASE_URL:-}" ] || die "阶段失败: DATABASE_DRIVER=postgres 但未配置 DATABASE_URL（请在 secrets 中提供）"
    require_cmd pg_dump || die "阶段失败: 无法备份正式数据库——缺少 pg_dump，拒绝继续迁移"
    info "备份正式数据库（pg_dump）..."
    pg_dump --dbname="$DATABASE_URL" -f "$backup_dir/pre-migrate-$stamp.sql" \
      || die "阶段失败: 正式数据库备份失败，拒绝继续迁移"
    info "备份完成: $backup_dir/pre-migrate-$stamp.sql"
    return 0
  fi

  # SQLite
  db_file="${DATABASE_URL#file:}"
  [ -n "$db_file" ] || db_file="$STATE_DIR/servercli.db"
  if [ ! -f "$db_file" ]; then
    info "数据库文件不存在（首次迁移），跳过备份: $db_file"
    return 0
  fi
  if command -v sqlite3 >/dev/null 2>&1; then
    sqlite3 "$db_file" ".backup '$backup_dir/pre-migrate-$stamp.db'" \
      || die "阶段失败: SQLite 备份失败"
  else
    cp "$db_file" "$backup_dir/pre-migrate-$stamp.db" \
      || die "阶段失败: SQLite 备份失败"
  fi
  info "备份完成: $backup_dir/pre-migrate-$stamp.db"
}

info "阶段 2/3: 迁移前备份（${DATABASE_DRIVER}）"
backup_before_migrate

# ---------------------------------------------------------------------------
# 阶段 3：执行迁移（幂等）
# ---------------------------------------------------------------------------
info "阶段 3/3: 执行迁移 ${MIGRATE_FLAG}（幂等）"
( cd "$REPO_ROOT" && exec "$BIN_DIR/$CONTROL_PLANE_BIN" $MIGRATE_FLAG ) \
  || die "阶段失败: 数据库迁移未成功（${MIGRATE_FLAG}）"
info "迁移完成 ✔"
