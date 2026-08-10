// Package service contains business logic for the control plane.
package service

import (
	"context"
	"fmt"
	"strconv"

	"servercli/internal/config"
	"servercli/internal/store"
)

// Setting keys.
const (
	KeyHeartbeatInterval   = "heartbeat_interval_seconds"
	KeyOfflineThreshold    = "offline_threshold_seconds"
	KeyLeaseDefaultMinutes = "ai_lease_default_minutes"
	KeyLeaseMaxHours       = "ai_lease_max_hours"
	KeyLeaseGraceSeconds   = "ai_lease_disconnect_grace_seconds"
	// KeyApprovalMode is deprecated: the token-based auto-approval flow no
	// longer reads it. Kept only so legacy rows remain queryable.
	KeyApprovalMode        = "ai_approval_mode"
	KeyNewRequestsEnabled  = "ai_new_requests_enabled"
	KeyRenewalsEnabled     = "ai_renewals_enabled"
	KeyAIAccessScope       = "ai_access_scope"
	KeyMaxOutputBytes      = "task_max_output_bytes"
	KeyCleanupSchedule     = "cleanup_schedule"
	KeyRetentionDays       = "retention_days"
	KeyTaskPollTimeout     = "task_poll_timeout_seconds"
	KeyTaskParamBackfilled = "task_parameter_history_backfilled"
)

// SettingsService reads and updates dynamic system settings.
type SettingsService struct {
	store *store.Store
	cfg   *config.Config
}

// NewSettingsService builds the service.
func NewSettingsService(st *store.Store, cfg *config.Config) *SettingsService {
	return &SettingsService{store: st, cfg: cfg}
}

// Defaults returns the seed values for system settings.
func (s *SettingsService) Defaults() map[string]string {
	return map[string]string{
		KeyHeartbeatInterval:   strconv.Itoa(s.cfg.HeartbeatIntervalSeconds),
		KeyOfflineThreshold:    strconv.Itoa(s.cfg.OfflineThresholdSeconds),
		KeyLeaseDefaultMinutes: strconv.Itoa(s.cfg.AILeaseDefaultMinutes),
		KeyLeaseMaxHours:       strconv.Itoa(s.cfg.AILeaseMaxHours),
		KeyLeaseGraceSeconds:   strconv.Itoa(s.cfg.AILeaseDisconnectGraceSecs),
		KeyNewRequestsEnabled:  "true",
		KeyRenewalsEnabled:     "true",
		KeyAIAccessScope:       "global",
		KeyMaxOutputBytes:      strconv.FormatInt(s.cfg.MaxTaskOutputBytes, 10),
		KeyCleanupSchedule:     s.cfg.CleanupSchedule,
		KeyRetentionDays:       strconv.Itoa(s.cfg.RetentionDays),
		KeyTaskPollTimeout:     strconv.Itoa(s.cfg.TaskPollTimeoutSeconds),
	}
}

// Seed inserts defaults for missing keys.
func (s *SettingsService) Seed(ctx context.Context) error {
	return s.store.SeedSettings(ctx, s.Defaults())
}

// deprecatedSettings are legacy keys kept in the DB for history but no longer
// exposed or editable (the token-based auto-approval flow replaced them).
var deprecatedSettings = map[string]bool{
	KeyApprovalMode:        true,
	"ai_auto_approval_policy": true,
	"auto_approval_policy":    true,
}

// All returns all settings as typed values, hiding deprecated keys so the UI
// cannot render stale approval-mode controls whose save would be rejected.
func (s *SettingsService) All(ctx context.Context) (map[string]any, error) {
	raw, err := s.store.AllSettings(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for k, v := range raw {
		if deprecatedSettings[k] {
			continue
		}
		out[k] = parseSettingValue(k, v)
	}
	return out, nil
}

// Get returns the raw string value for a key.
func (s *SettingsService) Get(ctx context.Context, key string) (string, error) {
	v, err := s.store.Setting(ctx, key)
	if err != nil {
		if err == store.ErrNotFound {
			if def, ok := s.Defaults()[key]; ok {
				return def, nil
			}
			return "", err
		}
		return "", err
	}
	return v, nil
}

// Int returns an integer setting with fallback default.
func (s *SettingsService) Int(ctx context.Context, key string, def int) int {
	v, err := s.Get(ctx, key)
	if err != nil {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Bool returns a boolean setting with fallback default.
func (s *SettingsService) Bool(ctx context.Context, key string, def bool) bool {
	v, err := s.Get(ctx, key)
	if err != nil {
		return def
	}
	switch v {
	case "true", "1":
		return true
	case "false", "0":
		return false
	}
	return def
}

// validKeys maps setting keys to their expected JSON type.
var validKeys = map[string]string{
	KeyHeartbeatInterval:   "integer",
	KeyOfflineThreshold:    "integer",
	KeyLeaseDefaultMinutes: "integer",
	KeyLeaseMaxHours:       "integer",
	KeyLeaseGraceSeconds:   "integer",
	KeyNewRequestsEnabled:  "boolean",
	KeyRenewalsEnabled:     "boolean",
	KeyAIAccessScope:       "string",
	KeyMaxOutputBytes:      "integer",
	KeyCleanupSchedule:     "enum",
	KeyRetentionDays:       "integer",
	KeyTaskPollTimeout:     "integer",
}

// Patch validates and applies settings updates, returning the full settings.
func (s *SettingsService) Patch(ctx context.Context, updates map[string]any) (map[string]any, error) {
	if len(updates) == 0 {
		return s.All(ctx)
	}
	for k, v := range updates {
		typ, ok := validKeys[k]
		if !ok {
			return nil, fmt.Errorf("unknown setting %q", k)
		}
		switch typ {
		case "boolean":
			if _, ok := v.(bool); !ok {
				return nil, fmt.Errorf("setting %q must be boolean", k)
			}
			if err := s.store.SetSetting(ctx, k, strconv.FormatBool(v.(bool))); err != nil {
				return nil, err
			}
		case "integer":
			f, ok := asFloatVal(v)
			if !ok {
				return nil, fmt.Errorf("setting %q must be an integer", k)
			}
			if err := s.store.SetSetting(ctx, k, strconv.FormatInt(int64(f), 10)); err != nil {
				return nil, err
			}
		case "enum":
			str, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("setting %q must be a string", k)
			}
			if k == KeyCleanupSchedule {
				if str != "weekly" && str != "daily" && str != "disabled" {
					return nil, fmt.Errorf("setting %q must be weekly|daily|disabled", k)
				}
			}
			if err := s.store.SetSetting(ctx, k, str); err != nil {
				return nil, err
			}
		case "string":
			str, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("setting %q must be a string", k)
			}
			if err := s.store.SetSetting(ctx, k, str); err != nil {
				return nil, err
			}
		}
	}
	return s.All(ctx)
}

func asFloatVal(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	}
	return 0, false
}

func parseSettingValue(key, v string) any {
	switch validKeys[key] {
	case "boolean":
		return v == "true" || v == "1"
	case "integer":
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0
		}
		return n
	}
	return v
}

// AIAccess returns the current AI request/renewal gates and scope.
func (s *SettingsService) AIAccess(ctx context.Context) (newRequests, renewals bool, scope string) {
	return s.Bool(ctx, KeyNewRequestsEnabled, true),
		s.Bool(ctx, KeyRenewalsEnabled, true),
		s.str(ctx, KeyAIAccessScope, "global")
}

func (s *SettingsService) str(ctx context.Context, key, def string) string {
	return s.Str(ctx, key, def)
}

// Str returns a string setting with fallback default.
func (s *SettingsService) Str(ctx context.Context, key, def string) string {
	v, err := s.Get(ctx, key)
	if err != nil {
		return def
	}
	return v
}
