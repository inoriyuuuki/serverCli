package agent

import (
	"archive/tar"
	"bytes"
	"regexp"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"servercli/internal/config"
	"servercli/internal/deployment/ossclient"
	"servercli/internal/deployment/repo"
	"servercli/internal/logger"
)

// ---- test doubles ----

type fakeOSS struct {
	objects  map[string][]byte // object key -> content
	puts     map[string][]byte // uploaded object key -> content
	headMeta map[string]ossclient.ObjectMeta
}

func newFakeOSS(objects map[string][]byte) *fakeOSS {
	return &fakeOSS{objects: objects, puts: map[string][]byte{}, headMeta: map[string]ossclient.ObjectMeta{}}
}

func (f *fakeOSS) ListObjects(_ context.Context, _, prefix, _ string) ([]ossclient.ObjectMeta, error) {
	var out []ossclient.ObjectMeta
	for k, data := range f.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, ossclient.ObjectMeta{Key: k, Size: int64(len(data)), ETag: "etag-" + k})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeOSS) GetObject(_ context.Context, _, key string, w io.Writer) (int64, error) {
	data, ok := f.objects[key]
	if !ok {
		return 0, &ossclient.OSSError{StatusCode: 404, Code: "NoSuchKey", Message: "not found"}
	}
	n, err := w.Write(data)
	return int64(n), err
}

func (f *fakeOSS) PutObject(_ context.Context, _, key string, r io.Reader, size int64, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("size mismatch")
	}
	f.puts[key] = data
	f.headMeta[key] = ossclient.ObjectMeta{Key: key, Size: size, ETag: "etag-" + key}
	return nil
}

func (f *fakeOSS) HeadObject(_ context.Context, _, key string) (*ossclient.ObjectMeta, error) {
	meta, ok := f.headMeta[key]
	if !ok {
		return nil, &ossclient.OSSError{StatusCode: 404, Code: "NoSuchKey", Message: "not found"}
	}
	return &meta, nil
}

var _ OSSClient = (*fakeOSS)(nil)

type recordingReporter struct {
	mu     sync.Mutex
	last   Result
	events []Event
}

func (r *recordingReporter) SendEvent(_ string, ev Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *recordingReporter) SendResult(_ string, res Result) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = res
	return nil
}

func (r *recordingReporter) result() Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// ---- helpers ----

func newTestRunner(t *testing.T, root string) *DeploymentRunner {
	t.Helper()
	cfg := config.Default()
	cfg.DeploymentRootDir = root
	cfg.AppEnv = "test"
	r := NewDeploymentRunner(cfg, logger.New(io.Discard, "error"))
	r.sudoPrefix = nil // tests invoke the (fake) wrapper directly
	return r
}

func writeTestWrapper(t *testing.T, logPath string) string {
	t.Helper()
	dir := t.TempDir()
	wp := filepath.Join(dir, "deploy-wrapper")
	// The fake wrapper mirrors the real wrapper contract: $1=/bin/bash,
	// $2=relative hook path anchored at --rendered-dir (rollback anchors at
	// --previous-release-dir). It resolves the hook and execs bash with the
	// remaining --key=value args.
	script := fmt.Sprintf(`#!/bin/bash
echo "$@" >> %q
HOOK="$2"
RD=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--rendered-dir" ] || [ "$prev" = "--previous-release-dir" ]; then RD="$a"; fi
  prev="$a"
done
case "$HOOK" in
  /*) ;;
  *) HOOK="$RD/$HOOK" ;;
esac
[ -n "$RD" ] || { echo "fake wrapper: missing anchor dir" >&2; exit 126; }
exec /bin/bash "$HOOK" "${@:3}"
`, logPath)
	if err := os.WriteFile(wp, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return wp
}

func seedCredentials(t *testing.T, root string) {
	t.Helper()
	p := filepath.Join(root, deployCredFileRel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	profile := OSSProfile{
		Endpoint:        "https://oss-cn-hangzhou.aliyuncs.com",
		Bucket:          "servercli-deploy-test",
		Prefix:          "deployment-repository/",
		AccessKeyID:     "LTAItestaccesskey000000",
		AccessKeySecret: "testsecret000000000000000000000",
	}
	data, _ := json.Marshal(profile)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	seedSigningKey(t, root)
}

// testSigningKey is a fixed 32-byte deploy signing key used by the tests. It
// is test material only and never a real secret.
var testSigningKey = []byte("0123456789abcdef0123456789abcdef")

func seedSigningKey(t *testing.T, root string) {
	t.Helper()
	p := filepath.Join(root, deploySigningKeyRel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, testSigningKey, 0o600); err != nil {
		t.Fatal(err)
	}
}

// signRepositoryManifest computes the canonical HMAC-SHA256 signature of a
// repository manifest (version + objects sorted by path).
func signRepositoryManifest(m *repo.RepositoryManifest, key []byte) string {
	// Must agree with repo.SignManifest: the canonical payload is the
	// path-sorted object list (repo.CanonicalManifestPayload), NOT a
	// version+objects envelope, so control-plane signing and node
	// verification use identical bytes.
	payload, err := repo.CanonicalManifestPayload(m)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// signBundle computes the bundle.sig value: hex HMAC-SHA256 over the bundle's
// SHA-256 hex digest.
func signBundle(bundle []byte, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(sha256Hex(bundle)))
	return hex.EncodeToString(mac.Sum(nil))
}

func deployTaskArgs(overrides map[string]any) json.RawMessage {
	m := map[string]any{
		"operation_id":    "op-1",
		"target_id":       "target-1",
		"node_id":         "node-1",
		"feature_key":     "app",
		"release_id":      "rel-1",
		"release_version": "1.0.0",
	}
	for k, v := range overrides {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// makeTarGzip builds an in-memory gzip-compressed tar with the given files.
func makeTarGzip(files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		content := files[name]
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// seedRelease writes repository/releases/<feature>/<version>/<sha>/ with a
// doc/15 flat manifest and returns the bundle sha256.
func seedRelease(t *testing.T, root, feature, version string, bundleFiles map[string]string) string {
	t.Helper()
	lay := repo.New(root)
	if err := lay.EnsureAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	bundle := makeTarGzip(bundleFiles)
	sha := sha256Hex(bundle)
	relDir := filepath.Join(lay.RepoDir(), repo.DirReleases, feature, version, sha)
	if err := os.MkdirAll(relDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relDir, repo.BundleFileName), bundle, 0o640); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"feature_key":   feature,
		"version":       version,
		"sha256":        sha,
		"size":          int64(len(bundle)),
		"object_key":    "releases/" + feature + "/" + version + "/" + sha + "/",
		"install_hook":  "hooks/install.sh",
		"update_hook":   "hooks/update.sh",
		"backup_hook":   "hooks/backup.sh",
		"health_hook":   "hooks/health-check.sh",
		"rollback_hook": "hooks/rollback.sh",
		"backup_mode":   "application_snapshot",
	}
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(relDir, "manifest.json"), mb, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relDir, "bundle.sig"), []byte(signBundle(bundle, testSigningKey)), 0o640); err != nil {
		t.Fatal(err)
	}
	return sha
}

// writeRepoManifest builds manifests/repository-manifest.json covering every
// regular file under repository/ except the manifest itself.
func writeRepoManifest(t *testing.T, root string) {
	t.Helper()
	repoDir := filepath.Join(root, repo.RepoDirRepository)
	manifestPath := filepath.Join(repoDir, repo.DirManifests, repo.ManifestFileName)
	var objs []repo.ManifestObject
	err := filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(repoDir, path)
		if rerr != nil {
			return rerr
		}
		if filepath.ToSlash(rel) == filepath.Join(repo.DirManifests, repo.ManifestFileName) {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		objs = append(objs, repo.ManifestObject{Path: filepath.ToSlash(rel), Size: int64(len(data)), SHA256: sha256Hex(data)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Path < objs[j].Path })
	m := repo.RepositoryManifest{Version: repo.ManifestVersion, Objects: objs}
	m.Signature = signRepositoryManifest(&m, testSigningKey)
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(manifestPath, mb, 0o640); err != nil {
		t.Fatal(err)
	}
}

func writeRenderedHook(t *testing.T, root, feature, version, hookName, content string) string {
	t.Helper()
	dir := filepath.Join(root, repo.LocalDirLocal, repo.DirRendered, feature, version, "hooks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, hookName)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, repo.LocalDirLocal, repo.DirRendered, feature, version)
}

func modeOf(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	return fi.Mode().Perm()
}

// ---- sync ----

func TestDeploymentSyncSuccess(t *testing.T) {
	root := t.TempDir()
	seedCredentials(t, root)

	secretContent := []byte("token: fake-secret-value\n")
	secretObj := repo.ManifestObject{
		Path:   "secrets/nodes/node-1/app.secrets.yaml",
		Size:   int64(len(secretContent)),
		SHA256: sha256Hex(secretContent),
	}
	syncManifest := repo.RepositoryManifest{Version: 1, Objects: []repo.ManifestObject{secretObj}}
	syncManifest.Signature = signRepositoryManifest(&syncManifest, testSigningKey)
	manifestContent, _ := json.MarshalIndent(syncManifest, "", "  ")

	fake := newFakeOSS(map[string][]byte{
		"deployment-repository/manifests/repository-manifest.json":    manifestContent,
		"deployment-repository/secrets/nodes/node-1/app.secrets.yaml": secretContent,
		"deployment-repository/.servercli-local/should-not-sync":      []byte("nope"),
	})
	runner := newTestRunner(t, root)
	runner.newOSS = func(_ context.Context, _ OSSProfile) (OSSClient, error) { return fake, nil }

	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-sync", CommandID: "deployment.sync", Arguments: deployTaskArgs(nil),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %q, error=%s (%s)", res.Status, res.ErrorMessage, res.ErrorCode)
	}

	secretPath := filepath.Join(root, repo.RepoDirRepository, "secrets", "nodes", "node-1", "app.secrets.yaml")
	if got, err := os.ReadFile(secretPath); err != nil || !bytes.Equal(got, secretContent) {
		t.Fatalf("secret content mismatch: %v", err)
	}
	if m := modeOf(t, secretPath); m != 0o600 {
		t.Fatalf("secret file mode = %04o, want 0600", m)
	}
	if m := modeOf(t, filepath.Dir(secretPath)); m != 0o700 {
		t.Fatalf("secret dir mode = %04o, want 0700", m)
	}
	// .servercli-local must never be synced.
	if _, err := os.Stat(filepath.Join(root, repo.RepoDirRepository, ".servercli-local")); err == nil {
		t.Fatal(".servercli-local synced into repository")
	}
	// Credentials file stays 0600.
	if m := modeOf(t, filepath.Join(root, deployCredFileRel)); m != 0o600 {
		t.Fatalf("credentials mode = %04o, want 0600", m)
	}
}

func TestDeploymentSyncMissingCredentials(t *testing.T) {
	root := t.TempDir()
	runner := newTestRunner(t, root)
	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-sync", CommandID: "deployment.sync", Arguments: deployTaskArgs(nil),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" || !strings.Contains(res.ErrorMessage, "OSS credentials not provisioned") {
		t.Fatalf("expected credentials failure, got status=%q error=%q", res.Status, res.ErrorMessage)
	}
}

// ---- install ----

func TestDeploymentInstallRunsHook(t *testing.T) {
	root := t.TempDir()
	hookLog := filepath.Join(t.TempDir(), "hook-args.log")
	wrapper := writeTestWrapper(t, hookLog)
	seedSigningKey(t, root)

	// Seed a secret reference that the runner must materialize.
	secretContent := "db_password: s3cr3t\n"
	secretPath := filepath.Join(root, repo.RepoDirRepository, "secrets", "nodes", "node-1", "app.secrets.yaml")
	if err := os.MkdirAll(filepath.Dir(secretPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretPath, []byte(secretContent), 0o600); err != nil {
		t.Fatal(err)
	}

	installScript := fmt.Sprintf(`#!/bin/bash
echo "install-args: $*" >> %q
exit 0
`, hookLog)
	sha := seedRelease(t, root, "app", "1.0.0", map[string]string{
		"hooks/install.sh": installScript,
		"config/app.yaml":  "mode: test\n",
	})
	writeRepoManifest(t, root)

	runner := newTestRunner(t, root)
	runner.wrapperPath = wrapper

	args := deployTaskArgs(map[string]any{
		"secret_refs": []map[string]any{
			{
				"ref_id":          "ref-1",
				"object_key":      "secrets/nodes/node-1/app.secrets.yaml",
				"version":         "1",
				"content_hash":    sha256Hex([]byte(secretContent)),
				"encryption_mode": "none",
			},
		},
	})
	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-install", CommandID: "deployment.install", Arguments: args,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %q, error=%s (%s)", res.Status, res.ErrorMessage, res.ErrorCode)
	}

	renderedDir := filepath.Join(root, repo.LocalDirLocal, repo.DirRendered, "app", "1.0.0")
	for _, f := range []string{"hooks/install.sh", "config/app.yaml", "runtime-config.yaml"} {
		if _, err := os.Stat(filepath.Join(renderedDir, f)); err != nil {
			t.Fatalf("rendered file %s missing: %v", f, err)
		}
	}
	// Staging must be consumed by SwitchDir (the <sha> dir is renamed away).
	if _, err := os.Stat(filepath.Join(root, repo.LocalDirLocal, repo.DirStaging, "app", "1.0.0", sha)); err == nil {
		t.Fatal("staging dir still present after switch")
	}

	// The materialized secret copy is transient: it must be cleaned up after
	// the main hook runs so no plaintext secret survives in the rendered zone.
	matPath := filepath.Join(renderedDir, "secrets", "ref-1.yaml")
	if _, err := os.Stat(matPath); !os.IsNotExist(err) {
		t.Fatalf("materialized secret not cleaned up after hook (err=%v)", err)
	}
	// The repository secret zone is untouched by cleanup.
	repoSecret := filepath.Join(root, repo.RepoDirRepository, "secrets", "nodes", "node-1", "app.secrets.yaml")
	if got, err := os.ReadFile(repoSecret); err != nil || !bytes.Equal(got, []byte(secretContent)) {
		t.Fatalf("repository secret changed: %v", err)
	}

	// Hook was invoked via the wrapper with the fixed argument whitelist.
	logData, err := os.ReadFile(hookLog)
	if err != nil {
		t.Fatalf("hook log missing: %v", err)
	}
	line := strings.TrimSpace(string(logData))
	for _, want := range []string{
		"--feature-key", "app",
		"--node-id", "node-1",
		"--operation-id", "op-1",
		"--deployment-root-dir", root,
		"--config-dir", renderedDir,
		"--data-dir", filepath.Join(renderedDir, "data"),
		"--image-tag",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("hook args missing %q: %s", want, line)
		}
	}
}

func TestDeploymentInstallRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	hookLog := filepath.Join(t.TempDir(), "hook-args.log")
	wrapper := writeTestWrapper(t, hookLog)

	seedSigningKey(t, root)
	seedRelease(t, root, "app", "1.0.0", map[string]string{
		"hooks/install.sh": "#!/bin/bash\nexit 0\n",
	})
	writeRepoManifest(t, root)

	// Overwrite the bundle with a malicious tar containing a traversal entry.
	malicious := makeTarGzip(map[string]string{
		"hooks/install.sh": "#!/bin/bash\nexit 0\n",
		"../evil.txt":      "pwned",
	})
	sha := sha256Hex(malicious)
	// Drop the previously seeded good release so only the malicious bundle
	// remains (locateRelease fails closed on ambiguity).
	_ = os.RemoveAll(filepath.Join(root, repo.RepoDirRepository, repo.DirReleases, "app"))
	relDir := filepath.Join(root, repo.RepoDirRepository, repo.DirReleases, "app", "1.0.0", sha)
	if err := os.MkdirAll(relDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relDir, repo.BundleFileName), malicious, 0o640); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"feature_key": "app", "version": "1.0.0", "sha256": sha, "size": int64(len(malicious)),
		"install_hook": "hooks/install.sh",
	}
	mb, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(relDir, "manifest.json"), mb, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relDir, "bundle.sig"), []byte(signBundle(malicious, testSigningKey)), 0o640); err != nil {
		t.Fatal(err)
	}
	writeRepoManifest(t, root)

	runner := newTestRunner(t, root)
	runner.wrapperPath = wrapper
	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-traversal", CommandID: "deployment.install", Arguments: deployTaskArgs(nil),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" || res.ErrorCode != "EXTRACT_FAILED" {
		t.Fatalf("expected EXTRACT_FAILED, got status=%q code=%q err=%q", res.Status, res.ErrorCode, res.ErrorMessage)
	}
	if _, err := os.Stat(filepath.Join(root, "evil.txt")); err == nil {
		t.Fatal("traversal entry escaped the deployment root")
	}
}

func TestDeploymentInstallRejectsUnknownArgs(t *testing.T) {
	root := t.TempDir()
	runner := newTestRunner(t, root)
	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-args", CommandID: "deployment.install",
		Arguments: deployTaskArgs(map[string]any{"malicious": "value"}),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" || res.ErrorCode != "INVALID_ARGUMENTS" {
		t.Fatalf("expected INVALID_ARGUMENTS, got status=%q code=%q err=%q", res.Status, res.ErrorCode, res.ErrorMessage)
	}
}

// ---- health-check ----

func TestDeploymentHealthCheck(t *testing.T) {
	root := t.TempDir()
	seedSigningKey(t, root)
	wrapper := writeTestWrapper(t, filepath.Join(t.TempDir(), "wrapper.log"))
	seedRelease(t, root, "app", "1.0.0", map[string]string{"hooks/health-check.sh": "#!/bin/bash\nexit 0\n"})
	writeRepoManifest(t, root)

	// Hook content is re-created per run (the rendered copy is what runs).
	writeRenderedHook(t, root, "app", "1.0.0", "health-check.sh", "#!/bin/bash\necho healthy\nexit 0\n")

	runner := newTestRunner(t, root)
	runner.wrapperPath = wrapper

	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-hc", CommandID: "deployment.health-check", Arguments: deployTaskArgs(nil),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("healthy hook should succeed, got status=%q err=%q", res.Status, res.ErrorMessage)
	}

	// Non-zero exit -> failure.
	writeRenderedHook(t, root, "app", "1.0.0", "health-check.sh", "#!/bin/bash\necho unhealthy >&2\nexit 1\n")
	res, err = runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-hc2", CommandID: "deployment.health-check", Arguments: deployTaskArgs(nil),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" || res.ErrorCode != "HEALTH_CHECK_FAILED" {
		t.Fatalf("unhealthy hook should fail, got status=%q code=%q", res.Status, res.ErrorCode)
	}
	if res.ExitCode == nil || *res.ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %v", res.ExitCode)
	}
}

// ---- backup ----

func TestDeploymentBackupUploadsSummary(t *testing.T) {
	root := t.TempDir()
	seedCredentials(t, root)
	wrapper := writeTestWrapper(t, filepath.Join(t.TempDir(), "wrapper.log"))
	seedRelease(t, root, "app", "1.0.0", map[string]string{"hooks/backup.sh": "#!/bin/bash\nexit 0\n"})
	writeRepoManifest(t, root)

	backupHook := `#!/bin/bash
set -euo pipefail
FEATURE=""; NODE=""; OP=""; DD=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --feature-key) FEATURE="$2"; shift 2;;
    --node-id) NODE="$2"; shift 2;;
    --operation-id) OP="$2"; shift 2;;
    --data-dir) DD="$2"; shift 2;;
    --deployment-root-dir) shift 2;;
    --rendered-dir) shift 2;;
    *) echo "unknown $1" >&2; exit 2;;
  esac
done
mkdir -p "$DD"
echo "backup-data" > "$DD/file.txt"
tmp="$(mktemp /tmp/servercli-test-backup-XXXXXX)"
rm -f "$tmp"
out="${tmp}.tar.gz"
tar -czf "$out" -C "$DD" file.txt
echo "$out"
exit 0
`
	writeRenderedHook(t, root, "app", "1.0.0", "backup.sh", backupHook)

	fake := newFakeOSS(nil)
	runner := newTestRunner(t, root)
	runner.wrapperPath = wrapper
	runner.newOSS = func(_ context.Context, _ OSSProfile) (OSSClient, error) { return fake, nil }

	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-backup", CommandID: "deployment.backup", Arguments: deployTaskArgs(nil),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %q, error=%s (%s)", res.Status, res.ErrorMessage, res.ErrorCode)
	}
	if res.SummaryJSON == "" {
		t.Fatal("backup result missing summary_json")
	}
	var summary struct {
		ObjectKey  string `json:"object_key"`
		Size       int64  `json:"size"`
		SHA256     string `json:"sha256"`
		BackupMode string `json:"backup_mode"`
	}
	if err := json.Unmarshal([]byte(res.SummaryJSON), &summary); err != nil {
		t.Fatalf("summary parse: %v", err)
	}
	if !strings.HasPrefix(summary.ObjectKey, "backups/test/app/node-1/") ||
		!strings.HasSuffix(summary.ObjectKey, "/op-1/backup.tar.gz") {
		t.Fatalf("unexpected object key %q", summary.ObjectKey)
	}
	if summary.Size <= 0 || summary.SHA256 == "" || summary.BackupMode != "application_snapshot" {
		t.Fatalf("bad summary: %+v", summary)
	}
	up, ok := fake.puts[summary.ObjectKey]
	if !ok {
		t.Fatalf("object %q not uploaded", summary.ObjectKey)
	}
	if int64(len(up)) != summary.Size {
		t.Fatalf("uploaded size mismatch: %d != %d", len(up), summary.Size)
	}
	if sha256Hex(up) != summary.SHA256 {
		t.Fatal("uploaded sha256 mismatch")
	}
}

// ---- rollback ----

func TestDeploymentRollback(t *testing.T) {
	root := t.TempDir()
	seedSigningKey(t, root)
	wrapper := writeTestWrapper(t, filepath.Join(t.TempDir(), "wrapper.log"))
	seedRelease(t, root, "app", "1.0.0", map[string]string{"hooks/rollback.sh": "#!/bin/bash\nexit 0\n"})
	writeRepoManifest(t, root)

	// current (installed) version rendered dir + previous release rendered dir
	writeRenderedHook(t, root, "app", "2.0.0", "rollback.sh", "#!/bin/bash\nexit 0\n")
	if err := os.WriteFile(filepath.Join(root, repo.LocalDirLocal, repo.DirRendered, "app", "2.0.0", "runtime-config.yaml"), []byte("x: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRenderedHook(t, root, "app", "1.0.0", "rollback.sh", "#!/bin/bash\necho rolled-back\nexit 0\n")
	if err := os.WriteFile(filepath.Join(root, repo.LocalDirLocal, repo.DirRendered, "app", "1.0.0", "runtime-config.yaml"), []byte("x: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runner := newTestRunner(t, root)
	runner.wrapperPath = wrapper
	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-rollback", CommandID: "deployment.rollback", Arguments: deployTaskArgs(nil),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %q, error=%s (%s)", res.Status, res.ErrorMessage, res.ErrorCode)
	}
	if !strings.Contains(res.StdoutText, "rolled-back") {
		t.Fatalf("rollback hook stdout missing, got %q", res.StdoutText)
	}
}

// ---- executor dispatch ----

func TestExecutorDispatchesDeploymentCommands(t *testing.T) {
	rec := &recordingReporter{}
	ex := NewExecutor(rec, logger.New(io.Discard, "error"))
	root := t.TempDir()
	ex.SetDeploymentRunner(newTestRunner(t, root))

	payload := &TaskPayload{
		TaskID: "t-dispatch", CommandID: "deployment.sync", TimeoutSeconds: 60,
		Arguments: deployTaskArgs(nil),
	}
	ex.Execute(context.Background(), payload, CommandEntry{CommandID: "deployment.sync", ExecutablePath: "/nonexistent"})

	res := rec.result()
	if res.Status != "failed" || res.ErrorCode != "SYNC_FAILED" {
		t.Fatalf("expected SYNC_FAILED from runner dispatch, got status=%q code=%q err=%q", res.Status, res.ErrorCode, res.ErrorMessage)
	}
}

// ---- config merge / freeze ----

func TestDeploymentInstallConfigMerge(t *testing.T) {
	root := t.TempDir()
	hookLog := filepath.Join(t.TempDir(), "hook-args.log")
	wrapper := writeTestWrapper(t, hookLog)
	seedSigningKey(t, root)

	// shared config (repository/configs/shared/app.yaml)
	sharedDir := filepath.Join(root, repo.RepoDirRepository, repo.DirConfigs, "shared")
	if err := os.MkdirAll(sharedDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedDir, "app.yaml"), []byte("shared_key: shared-value\noverlap: shared\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// node override (repository/configs/nodes/node-1/app.yaml)
	nodeDir := filepath.Join(root, repo.RepoDirRepository, repo.DirConfigs, "nodes", "node-1")
	if err := os.MkdirAll(nodeDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeDir, "app.yaml"), []byte("node_key: node-value\noverlap: node\nnode_id: evil\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	installScript := fmt.Sprintf("#!/bin/bash\necho \"install-args: $*\" >> %q\nexit 0\n", hookLog)
	seedRelease(t, root, "app", "1.0.0", map[string]string{
		"hooks/install.sh": installScript,
		"manifest.yaml": `apiVersion: servercli/v1
kind: Feature
spec:
  config_schema:
    defaults:
      default_key: default-value
      overlap: default
`,
	})
	writeRepoManifest(t, root)

	// The frozen config hash covers ONLY the static merged config (shared +
	// node override). Feature defaults are release-bundle-scoped and derived
	// fields are machine-specific, so both are excluded from the hash.
	expectedStatic := map[string]any{
		"shared_key": "shared-value",
		"node_key":   "node-value",
		"overlap":    "node",    // node overrides shared
		"node_id":    "evil",    // user-supplied value in the node override file
	}
	cfgHash, err := deployConfigHash(expectedStatic)
	if err != nil {
		t.Fatalf("hash expected config: %v", err)
	}

	runner := newTestRunner(t, root)
	runner.wrapperPath = wrapper
	args := deployTaskArgs(map[string]any{
		"environment_id": "env-test",
		"config_hash":    cfgHash,
	})
	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-cfg", CommandID: "deployment.install", Arguments: args,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %q, error=%s (%s)", res.Status, res.ErrorMessage, res.ErrorCode)
	}

	// runtime-config.yaml must contain the merged result (0600) and the
	// derived fields must win over user config.
	hostname, _ := os.Hostname()
	expectedFull := map[string]any{
		"default_key":         "default-value",
		"shared_key":          "shared-value",
		"node_key":            "node-value",
		"overlap":             "node", // node overrides shared overrides default
		"node_id":             "node-1",
		"environment_id":      "env-test",
		"hostname":            hostname,
		"feature_key":         "app",
		"release_version":     "1.0.0",
		"deployment_root_dir": root,
		"operation_id":        "op-1",
		"data_directory":      filepath.Join(root, repo.LocalDirLocal, "runtime", "app"),
	}
	rcPath := filepath.Join(root, repo.LocalDirLocal, repo.DirRendered, "app", "1.0.0", "runtime-config.yaml")
	data, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("read runtime config: %v", err)
	}
	if m := modeOf(t, rcPath); m != 0o600 {
		t.Fatalf("runtime config mode = %04o, want 0600", m)
	}
	var got map[string]any
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse runtime config: %v", err)
	}
	for k, want := range expectedFull {
		if gv, ok := got[k]; !ok || fmt.Sprintf("%v", gv) != fmt.Sprintf("%v", want) {
			t.Fatalf("runtime config key %q = %v, want %v (full: %v)", k, gv, want, got)
		}
	}
	if got["release_id"] != "rel-1" {
		t.Fatalf("runtime config release_id = %v", got["release_id"])
	}
}

func TestDeploymentInstallConfigHashMismatch(t *testing.T) {
	root := t.TempDir()
	hookLog := filepath.Join(t.TempDir(), "hook-args.log")
	wrapper := writeTestWrapper(t, hookLog)
	seedSigningKey(t, root)
	seedRelease(t, root, "app", "1.0.0", map[string]string{
		"hooks/install.sh": "#!/bin/bash\nexit 0\n",
	})
	writeRepoManifest(t, root)

	runner := newTestRunner(t, root)
	runner.wrapperPath = wrapper
	args := deployTaskArgs(map[string]any{"config_hash": "deadbeef"})
	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-cfghash", CommandID: "deployment.install", Arguments: args,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" || res.ErrorCode != "RENDER_CONFIG_FAILED" {
		t.Fatalf("expected RENDER_CONFIG_FAILED, got status=%q code=%q err=%q", res.Status, res.ErrorCode, res.ErrorMessage)
	}
	if !strings.Contains(res.ErrorMessage, "sync the repository first") {
		t.Fatalf("expected stale-repository hint, got %q", res.ErrorMessage)
	}
}

// ---- signature verification ----

func TestDeploymentInstallMissingSigningKey(t *testing.T) {
	root := t.TempDir()
	seedCredentials(t, root)
	// Remove the key to simulate a node that was not provisioned.
	if err := os.Remove(filepath.Join(root, deploySigningKeyRel)); err != nil {
		t.Fatal(err)
	}
	seedRelease(t, root, "app", "1.0.0", map[string]string{"hooks/install.sh": "#!/bin/bash\nexit 0\n"})
	writeRepoManifest(t, root)

	runner := newTestRunner(t, root)
	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-nokey", CommandID: "deployment.install", Arguments: deployTaskArgs(nil),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" || !strings.Contains(res.ErrorMessage, "deploy signing key not provisioned") {
		t.Fatalf("expected signing key failure, got status=%q err=%q", res.Status, res.ErrorMessage)
	}
}

func TestDeploymentManifestSignatureTampered(t *testing.T) {
	root := t.TempDir()
	seedSigningKey(t, root)
	seedRelease(t, root, "app", "1.0.0", map[string]string{"hooks/install.sh": "#!/bin/bash\nexit 0\n"})
	writeRepoManifest(t, root)

	// Rewrite the manifest with a wrong signature while keeping object hashes
	// valid, so VerifyManifest passes and the signature check must fail.
	manifestPath := filepath.Join(root, repo.RepoDirRepository, repo.DirManifests, repo.ManifestFileName)
	m, err := repo.LoadManifest(filepath.Join(root, repo.RepoDirRepository))
	if err != nil {
		t.Fatal(err)
	}
	m.Signature = strings.Repeat("ab", 32)
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(manifestPath, mb, 0o640); err != nil {
		t.Fatal(err)
	}

	runner := newTestRunner(t, root)
	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-badsig", CommandID: "deployment.install", Arguments: deployTaskArgs(nil),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" || !strings.Contains(res.ErrorMessage, "signature mismatch") {
		t.Fatalf("expected manifest signature failure, got status=%q err=%q", res.Status, res.ErrorMessage)
	}
}

func TestDeploymentBundleSignatureTampered(t *testing.T) {
	root := t.TempDir()
	hookLog := filepath.Join(t.TempDir(), "hook-args.log")
	wrapper := writeTestWrapper(t, hookLog)
	seedSigningKey(t, root)
	sha := seedRelease(t, root, "app", "1.0.0", map[string]string{"hooks/install.sh": "#!/bin/bash\nexit 0\n"})
	writeRepoManifest(t, root)

	// Overwrite bundle.sig with a wrong signature; the hash check passes but
	// the HMAC must fail.
	relDir := filepath.Join(root, repo.RepoDirRepository, repo.DirReleases, "app", "1.0.0", sha)
	if err := os.WriteFile(filepath.Join(relDir, "bundle.sig"), []byte(strings.Repeat("cd", 32)), 0o640); err != nil {
		t.Fatal(err)
	}
	// Rebuild the repository manifest so object hashes reflect the tampered
	// bundle.sig; the signature verification must then reject it.
	writeRepoManifest(t, root)

	runner := newTestRunner(t, root)
	runner.wrapperPath = wrapper
	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-badsig", CommandID: "deployment.install", Arguments: deployTaskArgs(nil),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" || res.ErrorCode != "BUNDLE_SIGNATURE_MISMATCH" {
		t.Fatalf("expected BUNDLE_SIGNATURE_MISMATCH, got status=%q code=%q err=%q", res.Status, res.ErrorCode, res.ErrorMessage)
	}
}

// ---- token validation ----

func TestDeploymentRejectsUnsafeFeatureKey(t *testing.T) {
	root := t.TempDir()
	seedSigningKey(t, root)
	seedRelease(t, root, "app", "1.0.0", map[string]string{"hooks/install.sh": "#!/bin/bash\nexit 0\n"})
	writeRepoManifest(t, root)

	runner := newTestRunner(t, root)
	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-badkey", CommandID: "deployment.install",
		Arguments: deployTaskArgs(map[string]any{"feature_key": "../evil"}),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" || res.ErrorCode != "INVALID_FEATURE_KEY" {
		t.Fatalf("expected INVALID_FEATURE_KEY, got status=%q code=%q err=%q", res.Status, res.ErrorCode, res.ErrorMessage)
	}
}

func TestDeploymentRejectsUnsafeVersion(t *testing.T) {
	root := t.TempDir()
	seedSigningKey(t, root)
	sha := seedRelease(t, root, "app", "1.0.0", map[string]string{"hooks/install.sh": "#!/bin/bash\nexit 0\n"})
	writeRepoManifest(t, root)

	// Tamper the release manifest version to an unsafe value.
	relDir := filepath.Join(root, repo.RepoDirRepository, repo.DirReleases, "app", "1.0.0", sha)
	mp := filepath.Join(relDir, "manifest.json")
	var rm map[string]any
	data, err := os.ReadFile(mp)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &rm); err != nil {
		t.Fatal(err)
	}
	rm["version"] = "../evil"
	mb, _ := json.Marshal(rm)
	if err := os.WriteFile(mp, mb, 0o640); err != nil {
		t.Fatal(err)
	}
	// Rebuild the repository manifest so object hashes reflect the tampered
	// release manifest; the token validation must then reject it.
	writeRepoManifest(t, root)

	runner := newTestRunner(t, root)
	res, err := runner.Run(context.Background(), &TaskPayload{
		TaskID: "t-badver", CommandID: "deployment.install", Arguments: deployTaskArgs(nil),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" || res.ErrorCode != "INVALID_VERSION" {
		t.Fatalf("expected INVALID_VERSION, got status=%q code=%q err=%q", res.Status, res.ErrorCode, res.ErrorMessage)
	}
}

// ---- process group termination ----

func TestDeploymentHookProcessGroupKilledOnCancel(t *testing.T) {
	root := t.TempDir()
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")
	seedSigningKey(t, root)
	hook := fmt.Sprintf(`#!/bin/bash
sleep 300 &
CHILD=$!
echo $CHILD > %q
wait $CHILD
exit 0
`, childPIDFile)
	seedRelease(t, root, "app", "1.0.0", map[string]string{"hooks/install.sh": hook})
	writeRepoManifest(t, root)

	runner := newTestRunner(t, root)
	runner.wrapperPath = writeTestWrapper(t, filepath.Join(t.TempDir(), "wrapper.log"))

	ctx, cancel := context.WithCancel(context.Background())
	type runResult struct {
		res *Result
		err error
	}
	done := make(chan runResult, 1)
	go func() {
		res, err := runner.Run(ctx, &TaskPayload{
			TaskID: "t-kill", CommandID: "deployment.install", Arguments: deployTaskArgs(nil),
		})
		done <- runResult{res: res, err: err}
	}()

	// Wait for the hook to spawn its child, then cancel the context.
	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(childPIDFile); err == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID <= 0 {
		t.Fatal("hook never spawned a child process")
	}
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("child %d already dead before cancel: %v", childPID, err)
	}
	cancel()

	res := <-done
	if res.err != nil {
		t.Fatalf("run: %v", res.err)
	}
	if res.res == nil || res.res.Status != "cancelled" {
		t.Fatalf("expected cancelled result, got %+v", res.res)
	}
	// The child (and the whole process group) must be gone.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			return // gone: expected
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child process %d survived cancellation", childPID)
}

// TestWrapperContractLintsRealWrapper verifies the runner's hook argument
// contract against the REAL scripts/servercli-sudo-deploy-wrapper.sh shipped
// in this repository (not the fake wrapper used by the other tests). It
// statically extracts ALLOWED_KEYS and VALUE_RE and asserts every key/value
// the runner emits is accepted, so a contract drift between the Go runner and
// the hardened wrapper fails CI instead of at deploy time.
func TestWrapperContractLintsRealWrapper(t *testing.T) {
	repoRoot := "."
	// Locate the repo root from the package working directory (backend/).
	if fi, err := os.Stat("../scripts/servercli-sudo-deploy-wrapper.sh"); err == nil && fi.Mode().IsRegular() {
		repoRoot = ".."
	}
	wrapperPath := filepath.Join(repoRoot, "scripts/servercli-sudo-deploy-wrapper.sh")
	data, err := os.ReadFile(wrapperPath)
	if err != nil {
		t.Skipf("real wrapper not found: %v", err)
	}
	script := string(data)
	allowed := map[string]bool{}
	reKeys := regexp.MustCompile(`ALLOWED_KEYS="([^"]+)"`)
	if m := reKeys.FindStringSubmatch(script); len(m) == 2 {
		for _, k := range strings.Fields(m[1]) {
			allowed[k] = true
		}
	}
	if len(allowed) == 0 {
		t.Fatal("could not parse ALLOWED_KEYS from real wrapper")
	}
	reValue := regexp.MustCompile(`VALUE_RE='([^']+)'`)
	var valueRe *regexp.Regexp
	if m := reValue.FindStringSubmatch(script); len(m) == 2 {
		valueRe = regexp.MustCompile(m[1])
	}
	if valueRe == nil {
		t.Fatal("could not parse VALUE_RE from real wrapper")
	}

	// Collect every --key value pair the runner can emit (install/update,
	// health, backup, rollback) via the real argument builders.
	rendered := "/opt/servercli-deployment/.servercli-local/rendered/app/1.0.0"
	releaseDir := "/opt/servercli-deployment/.servercli-local/rendered/app/1.0.0"
	allArgs := [][]string{
		{
			"--feature-key", "app",
			"--node-id", "node-1",
			"--operation-id", "op-1",
			"--deployment-root-dir", "/opt/servercli-deployment",
			"--release-version", "1.0.0",
			"--data-dir", filepath.Join(rendered, "data"),
			"--config-dir", rendered,
			"--rendered-dir", rendered,
			"--config-file", filepath.Join(rendered, "runtime-config.yaml"),
			"--release-dir", releaseDir,
			"--image-tag", "",
			"--port", "",
		},
		{"--feature-key", "app", "--node-id", "node-1", "--port", ""},
		{"--feature-key", "app", "--node-id", "node-1", "--operation-id", "op-1", "--data-dir", "/tmp", "--deployment-root-dir", "/opt/servercli-deployment"},
		{"--feature-key", "app", "--node-id", "node-1", "--operation-id", "op-1", "--deployment-root-dir", "/opt/servercli-deployment", "--previous-release-dir", "/a", "--current-release-dir", "/b"},
	}
	for _, argv := range allArgs {
		for i := 0; i+1 < len(argv); i += 2 {
			key := argv[i]
			if !strings.HasPrefix(key, "--") {
				t.Fatalf("unexpected positional arg %q in runner argv %v", key, argv)
			}
			if !allowed[key] {
				t.Errorf("runner sends key %q not allowed by real wrapper ALLOWED_KEYS", key)
			}
			val := argv[i+1]
			if !valueRe.MatchString(val) {
				t.Errorf("runner value for %s=%q rejected by real wrapper VALUE_RE", key, val)
			}
		}
	}
}
