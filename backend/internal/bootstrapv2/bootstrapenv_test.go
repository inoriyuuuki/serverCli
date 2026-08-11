package bootstrapv2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleSecret = "super-secret-value-123"

func validEnvText() string {
	return strings.Join([]string{
		"# first primary",
		"OSS_ACCESS_KEY_ID=ak-id",
		"OSS_ACCESS_KEY_SECRET=" + sampleSecret,
		"OSS_ENDPOINT=https://oss.example.test",
		"OSS_INTERNAL_ENDPOINT=https://oss-internal.example.test",
		"OSS_BUCKET=bootstrap-bucket",
		"SERVERCLI_VERSION=1.2.3",
		"BOOTSTRAP_CLUSTER_ID=cluster-a",
		"BOOTSTRAP_NODE_NAME=primary-1",
		"BOOTSTRAP_ROLE=primary",
		"BOOTSTRAP_PROFILE=primary-foundation",
		"FUTURE_OPTION=ignored",
		"",
	}, "\n")
}

func writeEnvFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bootstrap.env")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadBootstrapEnv(t *testing.T) {
	env, err := LoadBootstrapEnv(writeEnvFile(t, validEnvText()))
	if err != nil {
		t.Fatal(err)
	}
	if env.OSSAccessKeyID != "ak-id" || env.OSSAccessKeySecret != sampleSecret {
		t.Fatal("OSS credentials did not parse")
	}
	if env.ServerCLIVersion != "1.2.3" || env.ClusterID != "cluster-a" || env.NodeName != "primary-1" {
		t.Fatalf("bootstrap identity mismatch: %+v", env)
	}
	if env.Role != "primary" || env.Profile != "primary-foundation" {
		t.Fatalf("role/profile mismatch: %q/%q", env.Role, env.Profile)
	}
}

func TestLoadBootstrapEnvRejectsRequiredKeys(t *testing.T) {
	for _, key := range []string{"OSS_ACCESS_KEY_ID", "OSS_BUCKET", "OSS_ENDPOINT"} {
		t.Run(key, func(t *testing.T) {
			var lines []string
			for _, line := range strings.Split(validEnvText(), "\n") {
				if !strings.HasPrefix(line, key+"=") {
					lines = append(lines, line)
				}
			}
			_, err := LoadBootstrapEnv(writeEnvFile(t, strings.Join(lines, "\n")))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("missing %s error = %v", key, err)
			}
		})
	}
}

func TestBootstrapEnvSecretNeverAppearsInErrors(t *testing.T) {
	bad := strings.Replace(validEnvText(), "OSS_ACCESS_KEY_SECRET="+sampleSecret, "OSS_ACCESS_KEY_SECRET="+sampleSecret+" bad", 1)
	_, err := LoadBootstrapEnv(writeEnvFile(t, bad))
	if err == nil {
		t.Fatal("expected invalid whitespace error")
	}
	if strings.Contains(err.Error(), sampleSecret) {
		t.Fatalf("error leaked secret: %v", err)
	}
}

func TestBootstrapEnvToOSSConfig(t *testing.T) {
	env, err := LoadBootstrapEnv(writeEnvFile(t, validEnvText()))
	if err != nil {
		t.Fatal(err)
	}
	cfg := env.ToOSSConfig()
	if cfg.Endpoint != env.OSSEndpoint || cfg.InternalEndpoint != env.OSSInternalEndpoint || cfg.Bucket != env.OSSBucket {
		t.Fatalf("endpoint mapping mismatch: %+v", cfg)
	}
	if cfg.AccessKeyID != env.OSSAccessKeyID || cfg.AccessKeySecret != env.OSSAccessKeySecret || !cfg.PreferInternal {
		t.Fatal("credential/internal endpoint mapping mismatch")
	}
	if env.RequiresGitHubToken() {
		t.Fatal("first primary must not require a GitHub token")
	}
}

func TestLoadBootstrapEnvRejectsPermissiveFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bootstrap.env")
	text := "OSS_ACCESS_KEY_ID=id\nOSS_ACCESS_KEY_SECRET=secret\nOSS_ENDPOINT=https://oss.example.com\nOSS_BUCKET=bkt\nSERVERCLI_VERSION=0.0.9\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBootstrapEnv(path); err == nil {
		t.Fatal("expected error for 0644 file")
	}
}

func TestLoadBootstrapEnvRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.env")
	if err := os.WriteFile(real, []byte("OSS_ACCESS_KEY_ID=id\nOSS_ENDPOINT=https://x\nOSS_BUCKET=b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.env")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlink unsupported")
	}
	if _, err := LoadBootstrapEnv(link); err == nil {
		t.Fatal("expected error for symlink")
	}
}

func TestLoadBootstrapEnvRejectsNonRegular(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadBootstrapEnv(dir); err == nil {
		t.Fatal("expected error for directory")
	}
}
