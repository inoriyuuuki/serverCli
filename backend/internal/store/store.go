// Package store is the data access layer. It uses explicit SQL that works on
// both SQLite and PostgreSQL: timestamps are TEXT RFC3339 UTC and booleans are
// INTEGER 0/1.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"servercli/internal/db"
	"servercli/internal/model"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a uniqueness constraint is violated.
var ErrConflict = errors.New("conflict")

// ErrStateTransition is returned when a state machine transition is invalid.
var ErrStateTransition = errors.New("invalid state transition")

// Store wraps the database with typed accessors.
type Store struct {
	db  *db.DB
	log *slog.Logger
}

// New returns a Store.
func New(d *db.DB, log *slog.Logger) *Store { return &Store{db: d, log: log} }

// DB exposes the underlying DB for transactions.
func (s *Store) DB() *db.DB { return s.db }

// SchemaVersion returns the applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) int {
	return s.db.SchemaVersion(ctx)
}

// WithTx runs fn inside a transaction.
func (s *Store) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ts formats a time as UTC RFC3339Nano for storage.
func ts(t time.Time) string { return t.UTC().Format(model.TimeLayout) }

// now returns the current UTC time.
func now() time.Time { return time.Now().UTC() }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func parseTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	t, err := time.Parse(model.TimeLayout, s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func parseTimeVal(s sql.NullString) (time.Time, error) {
	if !s.Valid || s.String == "" {
		return time.Time{}, fmt.Errorf("missing time value")
	}
	return time.Parse(model.TimeLayout, s.String)
}

func parseBool(i int64) bool { return i != 0 }

func sqlErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// TxStore adapts *sql.Tx to the same accessor surface used by Store.
type TxStore struct {
	*Store
	tx *sql.Tx
}

// Tx returns a store scoped to tx. The returned store must not be used after
// the transaction ends.
func (s *Store) Tx(tx *sql.Tx) *TxStore {
	return &TxStore{Store: s, tx: tx}
}

func (s *TxStore) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.tx.ExecContext(ctx, query, args...)
}

func (s *TxStore) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.tx.QueryContext(ctx, query, args...)
}

func (s *TxStore) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.tx.QueryRowContext(ctx, query, args...)
}
