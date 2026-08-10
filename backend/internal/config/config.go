// Package config loads ServerCLI runtime configuration from environment
// variables with optional .env file support (simple key=value parser).
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds all runtime settings for control plane and node agent.
type Config struct {
	AppEnv                                 string
	InstanceName                           string
	NodeRole                               string
	PrimaryServerIP                        string
	PrimaryBackendURL                      string
	FrontendAddr                           string
	BackendAddr                            string
	DatabaseDriver                         string
	DatabaseURL                            string
	AgentStateDir                          string
	LogDir                                 string
	AdminInitialPassword                   string
	AdminInitialPasswordFile               string
	AILeaseDefaultMinutes                  int
	AILeaseMaxHours                        int
	AILeaseDisconnectGraceSecs             int
	RetentionDays                          int
	CleanupSchedule                        string
	HeartbeatIntervalSeconds               int
	OfflineThresholdSeconds                int
	TaskPollTimeoutSeconds                 int
	CommandsDir                            string
	AuthorizedKeysFile                     string
	LeaseShellBin                          string
	HTTPInsecureSkipVerify                 bool
	LogLevel                               string
	FrontendDistDir                        string
	MaxTaskOutputBytes                     int64
	TaskPollMaxWaitSeconds                 int
	NotificationFeishuWebhookURL           string
	NotificationRateLimitPerTokenPerMinute int
	NotificationRateLimitGlobalPerMinute   int
	AgentEnrollmentAutoExpireH             int
}

// Default returns a Config populated with contract defaults.
func Default() *Config {
	return &Config{
		AppEnv:                                 "test",
		InstanceName:                           "test-primary",
		NodeRole:                               "primary",
		PrimaryServerIP:                        "127.0.0.1",
		PrimaryBackendURL:                      "http://127.0.0.1:9045",
		FrontendAddr:                           "0.0.0.0:9044",
		BackendAddr:                            "0.0.0.0:9045",
		DatabaseDriver:                         "sqlite",
		DatabaseURL:                            "",
		AgentStateDir:                          "./state/test-primary",
		LogDir:                                 "./logs/test-primary",
		AILeaseDefaultMinutes:                  60,
		AILeaseMaxHours:                        24,
		AILeaseDisconnectGraceSecs:             60,
		RetentionDays:                          7,
		CleanupSchedule:                        "weekly",
		HeartbeatIntervalSeconds:               30,
		OfflineThresholdSeconds:                90,
		TaskPollTimeoutSeconds:                 25,
		CommandsDir:                            "commands",
		AuthorizedKeysFile:                     "",
		LeaseShellBin:                          "",
		HTTPInsecureSkipVerify:                 false,
		LogLevel:                               "info",
		FrontendDistDir:                        "../frontend/dist",
		MaxTaskOutputBytes:                     262144,
		TaskPollMaxWaitSeconds:                 25,
		NotificationFeishuWebhookURL:           "",
		NotificationRateLimitPerTokenPerMinute: 30,
		NotificationRateLimitGlobalPerMinute:   120,
		AgentEnrollmentAutoExpireH:             72,
	}
}

// Load reads configuration from the environment (optionally after a .env file).
func Load() (*Config, error) {
	loadDotEnv(".env")
	cfg := Default()

	str := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	boolean := func(key string, dst *bool) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = parseBool(v)
		}
	}
	intv := func(key string, dst *int) {
		if v, ok := os.LookupEnv(key); ok {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	// intStrict fails startup when a numeric env var is present but not a
	// valid integer (requirement: invalid notification rate config fails boot).
	intStrict := func(key string, dst *int) error {
		v, ok := os.LookupEnv(key)
		if !ok {
			return nil
		}
		v = strings.TrimSpace(v)
		if v == "" {
			return nil // explicitly empty is treated as unset (use default)
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			// Never echo the raw value: an operator may have pasted a secret
			// into a numeric variable and the error goes to logs/stderr.
			return fmt.Errorf("config: %s must be an integer", key)
		}
		*dst = n
		return nil
	}
	int64v := func(key string, dst *int64) {
		if v, ok := os.LookupEnv(key); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				*dst = n
			}
		}
	}

	str("APP_ENV", &cfg.AppEnv)
	str("INSTANCE_NAME", &cfg.InstanceName)
	str("NODE_ROLE", &cfg.NodeRole)
	str("PRIMARY_SERVER_IP", &cfg.PrimaryServerIP)
	str("PRIMARY_BACKEND_URL", &cfg.PrimaryBackendURL)
	str("FRONTEND_ADDR", &cfg.FrontendAddr)
	str("BACKEND_ADDR", &cfg.BackendAddr)
	str("DATABASE_DRIVER", &cfg.DatabaseDriver)
	str("DATABASE_URL", &cfg.DatabaseURL)
	str("AGENT_STATE_DIR", &cfg.AgentStateDir)
	str("LOG_DIR", &cfg.LogDir)
	str("ADMIN_INITIAL_PASSWORD", &cfg.AdminInitialPassword)
	str("ADMIN_INITIAL_PASSWORD_FILE", &cfg.AdminInitialPasswordFile)
	str("CLEANUP_SCHEDULE", &cfg.CleanupSchedule)
	str("COMMANDS_DIR", &cfg.CommandsDir)
	str("AUTHORIZED_KEYS_FILE", &cfg.AuthorizedKeysFile)
	str("LEASE_SHELL_BIN", &cfg.LeaseShellBin)
	str("LOG_LEVEL", &cfg.LogLevel)
	str("FRONTEND_DIST_DIR", &cfg.FrontendDistDir)
	boolean("HTTP_INSECURE_SKIP_VERIFY", &cfg.HTTPInsecureSkipVerify)
	intv("AI_LEASE_DEFAULT_MINUTES", &cfg.AILeaseDefaultMinutes)
	intv("AI_LEASE_MAX_HOURS", &cfg.AILeaseMaxHours)
	intv("AI_LEASE_DISCONNECT_GRACE_SECONDS", &cfg.AILeaseDisconnectGraceSecs)
	intv("RETENTION_DAYS", &cfg.RetentionDays)
	intv("HEARTBEAT_INTERVAL_SECONDS", &cfg.HeartbeatIntervalSeconds)
	intv("OFFLINE_THRESHOLD_SECONDS", &cfg.OfflineThresholdSeconds)
	intv("TASK_POLL_TIMEOUT_SECONDS", &cfg.TaskPollTimeoutSeconds)
	int64v("MAX_TASK_OUTPUT_BYTES", &cfg.MaxTaskOutputBytes)
	str("NOTIFICATION_FEISHU_WEBHOOK_URL", &cfg.NotificationFeishuWebhookURL)
	if err := intStrict("NOTIFICATION_RATE_LIMIT_PER_TOKEN_PER_MINUTE", &cfg.NotificationRateLimitPerTokenPerMinute); err != nil {
		return nil, err
	}
	if err := intStrict("NOTIFICATION_RATE_LIMIT_GLOBAL_PER_MINUTE", &cfg.NotificationRateLimitGlobalPerMinute); err != nil {
		return nil, err
	}

	// Derive dependent defaults.
	if cfg.DatabaseURL == "" {
		cfg.DatabaseURL = "file:" + filepath.Join(cfg.AgentStateDir, "servercli.db")
	}
	if cfg.AuthorizedKeysFile == "" {
		cfg.AuthorizedKeysFile = filepath.Join(cfg.AgentStateDir, "authorized_keys")
	}
	if cfg.LeaseShellBin == "" {
		cfg.LeaseShellBin = filepath.Join(cfg.AgentStateDir, "bin", "servercli-lease-shell")
	}
	if cfg.PrimaryBackendURL == "" {
		cfg.PrimaryBackendURL = fmt.Sprintf("http://%s:9045", cfg.PrimaryServerIP)
	}
	cfg.TaskPollMaxWaitSeconds = cfg.TaskPollTimeoutSeconds
	if cfg.TaskPollMaxWaitSeconds > 55 {
		cfg.TaskPollMaxWaitSeconds = 55
	}

	if cfg.NodeRole != "primary" && cfg.NodeRole != "child" {
		return nil, fmt.Errorf("config: NODE_ROLE must be primary or child, got %q", cfg.NodeRole)
	}
	switch cfg.CleanupSchedule {
	case "weekly", "daily", "disabled":
	default:
		return nil, fmt.Errorf("config: CLEANUP_SCHEDULE must be weekly|daily|disabled, got %q", cfg.CleanupSchedule)
	}
	if cfg.DatabaseDriver != "sqlite" && cfg.DatabaseDriver != "postgres" {
		return nil, fmt.Errorf("config: DATABASE_DRIVER must be sqlite or postgres, got %q", cfg.DatabaseDriver)
	}
	// Notification rate limits: configured values must be positive or startup fails.
	if cfg.NotificationRateLimitPerTokenPerMinute <= 0 {
		return nil, fmt.Errorf("config: NOTIFICATION_RATE_LIMIT_PER_TOKEN_PER_MINUTE must be a positive integer, got %d", cfg.NotificationRateLimitPerTokenPerMinute)
	}
	if cfg.NotificationRateLimitGlobalPerMinute <= 0 {
		return nil, fmt.Errorf("config: NOTIFICATION_RATE_LIMIT_GLOBAL_PER_MINUTE must be a positive integer, got %d", cfg.NotificationRateLimitGlobalPerMinute)
	}
	// Production must never skip TLS verification: the node credential and
	// agent signatures travel to the primary over this connection.
	if cfg.AppEnv == "production" && cfg.HTTPInsecureSkipVerify {
		return nil, fmt.Errorf("config: HTTP_INSECURE_SKIP_VERIFY must be false in production")
	}
	return cfg, nil
}

func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// loadDotEnv parses a simple KEY=VALUE file and populates only variables that
// are not already set in the environment. Lines starting with # are comments;
// export prefixes are tolerated; no variable expansion is performed.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"'`)
		if key == "" {
			continue
		}
		if _, ok := os.LookupEnv(key); !ok {
			_ = os.Setenv(key, val)
		}
	}
}
