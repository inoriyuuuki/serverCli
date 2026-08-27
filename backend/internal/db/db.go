// Package db manages database connections and embedded migrations for both
// SQLite (test) and PostgreSQL (production).
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql migrations_postgres/*.sql
var migrationsFS embed.FS

// DB wraps a *sql.DB with migration state.
type DB struct {
	*sql.DB
	Driver    string
	SchemaVer int
}

// Open opens the configured database and applies pending migrations.
func Open(ctx context.Context, driver, dsn string, log *slog.Logger) (*DB, error) {
	var d *sql.DB
	var err error
	switch driver {
	case "sqlite":
		finalDSN := sqliteDSN(dsn)
		d, err = sql.Open("sqlite", finalDSN)
		if err == nil {
			// Apply pragmas defensively on every new connection.
			for _, pragma := range []string{
				"PRAGMA journal_mode=WAL",
				"PRAGMA foreign_keys=ON",
				"PRAGMA busy_timeout=10000",
				"PRAGMA synchronous=NORMAL",
			} {
				if _, perr := d.ExecContext(ctx, pragma); perr != nil {
					log.Warn("sqlite pragma failed", "pragma", pragma, "error", perr)
				}
			}
			d.SetMaxOpenConns(16)
			d.SetMaxIdleConns(4)
		}
	case "postgres":
		d, err = sql.Open("postgres", dsn)
		if err == nil {
			d.SetMaxOpenConns(20)
			d.SetMaxIdleConns(5)
		}
	default:
		return nil, fmt.Errorf("db: unsupported driver %q", driver)
	}
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", driver, err)
	}
	if err := d.PingContext(ctx); err != nil {
		d.Close()
		return nil, fmt.Errorf("db: ping %s: %w", driver, err)
	}
	ver, err := Migrate(ctx, driver, d)
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("db: migrate: %w", err)
	}
	return &DB{DB: d, Driver: driver, SchemaVer: ver}, nil
}

// sqliteDSN appends pragma query parameters to a SQLite DSN while preserving
// existing parameters.
func sqliteDSN(dsn string) string {
	if strings.Contains(dsn, "_pragma=") {
		return dsn // explicit DSN already configured
	}
	params := "_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	if strings.Contains(dsn, "?") {
		return dsn + "&" + params
	}
	return dsn + "?" + params
}

// Migrate applies embedded migrations in version order and returns the latest
// schema version.
func Migrate(ctx context.Context, driver string, d *sql.DB) (int, error) {
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}
	applied := map[int]bool{}
	rows, err := d.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return 0, err
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return 0, err
	}
	sort.Strings(names)
	latest := 0
	for _, name := range names {
		var version int
		if _, err := fmt.Sscanf(name, "migrations/%d_", &version); err != nil {
			continue
		}
		if version > latest {
			latest = version
		}
		if applied[version] {
			continue
		}
		body, err := migrationsFS.ReadFile(name)
		if err != nil {
			return 0, err
		}
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return 0, fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES ($1, $2)`,
			version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			tx.Rollback()
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		// Track the version as applied immediately so the PostgreSQL-only
		// loop below never re-applies/re-inserts a migration that the shared
		// loop already applied (a migration present in both migrations/ and
		// migrations_postgres/ with the same number would otherwise
		// double-insert schema_migrations and fail with a duplicate key).
		applied[version] = true
	}
	// PostgreSQL 专用迁移（SQLite 不适用：如 int4 -> int8 扩容）。
	if driver == "postgres" {
		postgresNames, err := fs.Glob(migrationsFS, "migrations_postgres/*.sql")
		if err != nil {
			return latest, err
		}
		sort.Strings(postgresNames)
		for _, name := range postgresNames {
			var version int
			if _, err := fmt.Sscanf(name, "migrations_postgres/%d_", &version); err != nil {
				continue
			}
			if version > latest {
				latest = version
			}
			if applied[version] {
				continue
			}
			body, err := migrationsFS.ReadFile(name)
			if err != nil {
				return latest, err
			}
			tx, err := d.BeginTx(ctx, nil)
			if err != nil {
				return latest, err
			}
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				tx.Rollback()
				return latest, fmt.Errorf("apply %s: %w", name, err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO schema_migrations (version, applied_at) VALUES ($1, $2)`,
				version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				tx.Rollback()
				return latest, err
			}
			if err := tx.Commit(); err != nil {
				return latest, err
			}
			applied[version] = true
		}
	}
	return latest, nil
}

// SchemaVersion returns the latest applied migration version.
func (d *DB) SchemaVersion(ctx context.Context) int {
	var v int
	_ = d.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&v)
	return v
}

// SanitizeDSNForLog masks the password in a database DSN for logging.
func SanitizeDSNForLog(driver, dsn string) string {
	if driver != "postgres" {
		return dsn
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "[REDACTED]"
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), "[REDACTED]")
		}
	}
	return u.String()
}
