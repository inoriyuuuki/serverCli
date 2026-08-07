package store

import (
	"context"
	"database/sql"
)

// Setting returns the value of a system setting key.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT value FROM system_setting WHERE key=$1`, key).Scan(&v)
	if err != nil {
		return "", sqlErr(err)
	}
	return v.String, nil
}

// AllSettings returns a map of all system settings.
func (s *Store) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM system_setting ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k string
		var v sql.NullString
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v.String
	}
	return out, rows.Err()
}

// SetSetting upserts a setting value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO system_setting (key, value, updated_at) VALUES ($1,$2,$3)
		ON CONFLICT(key) DO UPDATE SET value=$2, updated_at=$3`, key, value, ts(now()))
	return err
}

// SeedSettings inserts defaults for keys that do not exist yet.
func (s *Store) SeedSettings(ctx context.Context, defaults map[string]string) error {
	for k, v := range defaults {
		var one int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM system_setting WHERE key=$1`, k).Scan(&one)
		if err != nil {
			// missing
			if err := s.SetSetting(ctx, k, v); err != nil {
				return err
			}
		}
	}
	return nil
}
