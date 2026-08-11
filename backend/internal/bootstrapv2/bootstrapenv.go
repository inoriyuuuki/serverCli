package bootstrapv2

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"

	"servercli/internal/oss"
)

// BootstrapEnv is the root-only seed configuration for the first primary.
// It must never be serialized into init state or logged as a value-bearing
// struct because it contains the OSS access-key secret.
type BootstrapEnv struct {
	OSSAccessKeyID      string
	OSSAccessKeySecret  string
	OSSEndpoint         string
	OSSInternalEndpoint string
	OSSBucket           string
	ServerCLIVersion    string
	ClusterID           string
	NodeName            string
	Role                string
	Profile             string
}

var supportedBootstrapEnvKeys = map[string]func(*BootstrapEnv, string){
	"OSS_ACCESS_KEY_ID":     func(e *BootstrapEnv, v string) { e.OSSAccessKeyID = v },
	"OSS_ACCESS_KEY_SECRET": func(e *BootstrapEnv, v string) { e.OSSAccessKeySecret = v },
	"OSS_ENDPOINT":          func(e *BootstrapEnv, v string) { e.OSSEndpoint = v },
	"OSS_INTERNAL_ENDPOINT": func(e *BootstrapEnv, v string) { e.OSSInternalEndpoint = v },
	"OSS_BUCKET":            func(e *BootstrapEnv, v string) { e.OSSBucket = v },
	"SERVERCLI_VERSION":     func(e *BootstrapEnv, v string) { e.ServerCLIVersion = v },
	"BOOTSTRAP_CLUSTER_ID":  func(e *BootstrapEnv, v string) { e.ClusterID = v },
	"BOOTSTRAP_NODE_NAME":   func(e *BootstrapEnv, v string) { e.NodeName = v },
	"BOOTSTRAP_ROLE":        func(e *BootstrapEnv, v string) { e.Role = v },
	"BOOTSTRAP_PROFILE":     func(e *BootstrapEnv, v string) { e.Profile = v },
}

// LoadBootstrapEnv parses a strict KEY=VALUE file. It is intentionally not a
// shell parser: quoting, expansion, command substitution, whitespace-bearing
// values and duplicate supported keys are rejected.
func LoadBootstrapEnv(path string) (*BootstrapEnv, error) {
	// The seed file contains OSS credentials: it must be a regular file (never
	// a symlink) with no group/other permissions (0600/0400/000). An overly
	// permissive file is refused rather than silently accepted.
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("bootstrap env: stat: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("bootstrap env: refusing symlink %s", path)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("bootstrap env: %s is not a regular file", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("bootstrap env: %s permissions %o are too open; require 0600", path, fi.Mode().Perm())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("bootstrap env: open: %w", err)
	}
	defer f.Close()

	env := &BootstrapEnv{}
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(f)
	// Credentials are expected to be short, but use a non-tiny explicit cap so
	// an accidentally large line returns a controlled parse error.
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("bootstrap env: line %d is not KEY=VALUE", lineNo)
		}
		if strings.TrimSpace(key) != key || strings.IndexFunc(key, unicode.IsSpace) >= 0 {
			return nil, fmt.Errorf("bootstrap env: line %d has invalid key whitespace", lineNo)
		}
		setter, supported := supportedBootstrapEnvKeys[key]
		if !supported {
			// Unknown keys are ignored so the seed file can grow without making
			// an older bootstrap binary unusable.
			continue
		}
		if seen[key] {
			return nil, fmt.Errorf("bootstrap env: duplicate key %s", key)
		}
		seen[key] = true
		if value == "" {
			return nil, fmt.Errorf("bootstrap env: %s is empty", key)
		}
		if strings.TrimSpace(value) != value || strings.IndexFunc(value, unicode.IsSpace) >= 0 || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("bootstrap env: %s contains invalid whitespace", key)
		}
		setter(env, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("bootstrap env: read: %w", err)
	}
	if err := env.Validate(); err != nil {
		return nil, err
	}
	return env, nil
}

// Validate checks the required first-primary seed fields without including
// any field value in returned errors.
func (e *BootstrapEnv) Validate() error {
	if e == nil {
		return errors.New("bootstrap env: nil configuration")
	}
	required := []struct {
		name  string
		value string
	}{
		{"OSS_ACCESS_KEY_ID", e.OSSAccessKeyID},
		{"OSS_ACCESS_KEY_SECRET", e.OSSAccessKeySecret},
		{"OSS_ENDPOINT", e.OSSEndpoint},
		{"OSS_BUCKET", e.OSSBucket},
		{"SERVERCLI_VERSION", e.ServerCLIVersion},
		{"BOOTSTRAP_CLUSTER_ID", e.ClusterID},
		{"BOOTSTRAP_NODE_NAME", e.NodeName},
		{"BOOTSTRAP_ROLE", e.Role},
		{"BOOTSTRAP_PROFILE", e.Profile},
	}
	for _, field := range required {
		if field.value == "" {
			return fmt.Errorf("bootstrap env: missing required key %s", field.name)
		}
		if strings.TrimSpace(field.value) != field.value || strings.IndexFunc(field.value, unicode.IsSpace) >= 0 || strings.ContainsRune(field.value, '\x00') {
			return fmt.Errorf("bootstrap env: %s contains invalid whitespace", field.name)
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"OSS_BUCKET", e.OSSBucket},
		{"SERVERCLI_VERSION", e.ServerCLIVersion},
		{"BOOTSTRAP_CLUSTER_ID", e.ClusterID},
		{"BOOTSTRAP_NODE_NAME", e.NodeName},
	} {
		if !safeSeedIdentifier(field.value) {
			return fmt.Errorf("bootstrap env: %s contains invalid characters", field.name)
		}
	}
	if e.Role != "primary" {
		return errors.New("bootstrap env: BOOTSTRAP_ROLE must be primary")
	}
	if e.Profile != "primary-foundation" {
		return errors.New("bootstrap env: BOOTSTRAP_PROFILE must be primary-foundation")
	}
	return nil
}

// ToOSSConfig maps the bootstrap seed to the existing OSS provider config.
func (e *BootstrapEnv) ToOSSConfig() oss.Config {
	if e == nil {
		return oss.Config{}
	}
	return oss.Config{
		Endpoint:         e.OSSEndpoint,
		InternalEndpoint: e.OSSInternalEndpoint,
		Bucket:           e.OSSBucket,
		AccessKeyID:      e.OSSAccessKeyID,
		AccessKeySecret:  e.OSSAccessKeySecret,
		PreferInternal:   e.OSSInternalEndpoint != "",
	}
}

// RequiresGitHubToken is false for the OSS-first initial primary bootstrap.
func (e *BootstrapEnv) RequiresGitHubToken() bool { return false }

func safeSeedIdentifier(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
