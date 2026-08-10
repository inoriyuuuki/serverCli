package config

import (
	"strings"
	"testing"
)

func TestLoadNotificationRateLimitStrict(t *testing.T) {
	cases := []struct {
		name  string
		per   string
		glob  string
		wantP int
		wantG int
		err   bool
	}{
		{"defaults when unset", "", "", 30, 120, false},
		{"valid values", "10", "5", 10, 5, false},
		{"per non-integer fails", "abc", "", 0, 0, true},
		{"global non-integer fails", "", "30.5", 0, 0, true},
		{"per negative fails", "-1", "", 0, 0, true},
		{"global zero fails", "", "0", 0, 0, true},
		{"whitespace tolerated", " 15 ", "", 15, 120, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("NOTIFICATION_RATE_LIMIT_PER_TOKEN_PER_MINUTE", c.per)
			t.Setenv("NOTIFICATION_RATE_LIMIT_GLOBAL_PER_MINUTE", c.glob)
			cfg, err := Load()
			if c.err {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.NotificationRateLimitPerTokenPerMinute != c.wantP || cfg.NotificationRateLimitGlobalPerMinute != c.wantG {
				t.Fatalf("got per=%d glob=%d want per=%d glob=%d", cfg.NotificationRateLimitPerTokenPerMinute, cfg.NotificationRateLimitGlobalPerMinute, c.wantP, c.wantG)
			}
		})
	}
}

func TestLoadNotificationRateLimitErrorMessages(t *testing.T) {
	t.Setenv("NOTIFICATION_RATE_LIMIT_PER_TOKEN_PER_MINUTE", "not-a-number")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for non-integer rate limit")
	}
	if !strings.Contains(err.Error(), "NOTIFICATION_RATE_LIMIT_PER_TOKEN_PER_MINUTE") {
		t.Fatalf("error should name the env var, got: %v", err)
	}
}
