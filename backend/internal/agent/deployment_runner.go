package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"servercli/internal/config"
	"servercli/internal/deployment/ossclient"
	"servercli/internal/deployment/repo"
	"servercli/internal/deployment/secretprovider"
)

// Deployment runner constants (V1).
const (
	// deployWrapperPath is the fixed root-elevation wrapper used for every
	// deployment hook. The runner never falls back to a bare root shell.
	deployWrapperPath = "/usr/local/libexec/servercli-deploy-wrapper"
	// deployRepoPrefix is the default OSS object prefix for the repository.
	deployRepoPrefix = "deployment-repository/"
)

// deployCredFileRel is the fixed credentials location under the deployment
// root (.servercli-local/credentials/oss-profile.json, 0600).
var deployCredFileRel = filepath.Join(repo.LocalDirLocal, repo.DirCredentials, "oss-profile.json")

// deploySigningKeyRel is the node deploy signing key location under the
// deployment root (.servercli-local/credentials/deploy-signing.key, 0600,
// written by the bootstrap). It is the independent trust root for manifest
// and bundle signature verification and is never synced.
var deploySigningKeyRel = filepath.Join(repo.LocalDirLocal, repo.DirCredentials, "deploy-signing.key")

// deploySecretMode is the only V1 secret encryption mode.
const deploySecretMode = secretprovider.ModeNone

// Deployment limits applied to release bundle extraction.
var deployExtractLimits = repo.Limits{
	MaxFiles:           2000,
	MaxTotalBytes:      1 << 30, // 1 GiB
	MaxSingleFileBytes: 512 << 20,
	MaxPathLen:         1024,
}

// akPattern redacts Alibaba OSS access-key patterns (LTAI/AKID) from hook
// output before it is written to events/results. It never redacts Secret body.
var akPattern = regexp.MustCompile(`(?i)\b(LTAI[A-Za-z0-9]{12,20}|AKID[A-Za-z0-9]{16,40})\b`)

// redactSensitive scrubs access-key patterns from arbitrary output.
func redactSensitive(s string) string { return akPattern.ReplaceAllString(s, "[REDACTED_KEY]") }

// safeTokenRe matches the fixed deployment token charset
// ([A-Za-z0-9._-], doc/14 §4). ".." is additionally rejected so a token can
// never escape a path component.
var safeTokenRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateToken enforces the safe charset for feature_key/version/sha values
// before they are used in any filepath.Join or filesystem removal.
func validateToken(kind, v string) error {
	if v == "" {
		return fmt.Errorf("%s is empty", kind)
	}
	if strings.Contains(v, "..") {
		return fmt.Errorf("%s %q contains a .. component", kind, v)
	}
	if !safeTokenRe.MatchString(v) {
		return fmt.Errorf("%s %q contains unsafe characters (allowed: [A-Za-z0-9._-])", kind, v)
	}
	return nil
}

// OSSProfile is the node-side OSS credentials file format. Credential values
// are secret and must never be logged, printed, or included in errors.
type OSSProfile struct {
	Endpoint        string `json:"endpoint"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	AccessKeySecret string `json:"access_key_secret"`
}

// OSSClient is the minimal OSS surface used by the runner. It is an interface
// so tests can inject a fake without network access; the production
// implementation is *ossclient.Client.
type OSSClient interface {
	PutObject(ctx context.Context, bucket, objectKey string, r io.Reader, size int64, contentType string) error
	GetObject(ctx context.Context, bucket, objectKey string, w io.Writer) (int64, error)
	HeadObject(ctx context.Context, bucket, objectKey string) (*ossclient.ObjectMeta, error)
	ListObjects(ctx context.Context, bucket, prefix, delimiter string) ([]ossclient.ObjectMeta, error)
}

// OSSClientFactory builds an OSSClient for a credentials profile.
type OSSClientFactory func(ctx context.Context, profile OSSProfile) (OSSClient, error)

// DeploymentRunner implements the fixed deployment.* commands on a node. It
// only runs commands with a fixed argument whitelist and never executes
// arbitrary strings supplied by the control plane.
type DeploymentRunner struct {
	cfg         *config.Config
	log         *slog.Logger
	wrapperPath string
	newOSS      OSSClientFactory
	// sudoPrefix elevates the wrapper invocation to root (the wrapper itself
	// refuses to run unprivileged). Defaults to ["sudo","-n"]; tests set nil
	// to invoke a fake wrapper directly.
	sudoPrefix []string
}

// NewDeploymentRunner builds a runner rooted at cfg.DeploymentRootDir.
func NewDeploymentRunner(cfg *config.Config, log *slog.Logger) *DeploymentRunner {
	return &DeploymentRunner{
		cfg:         cfg,
		log:         log,
		wrapperPath: deployWrapperPath,
		newOSS:      defaultOSSFactory,
		sudoPrefix:  []string{"sudo", "-n"},
	}
}

func defaultOSSFactory(_ context.Context, profile OSSProfile) (OSSClient, error) {
	client, err := ossclient.New(profile.Endpoint, ossclient.Credentials{
		AccessKeyID:     profile.AccessKeyID,
		AccessKeySecret: profile.AccessKeySecret,
	})
	if err != nil {
		return nil, err
	}
	return client, nil
}

// Run dispatches a deployment.* task to the matching operation. Unknown
// deployment.* commands fail closed. Handled failures are returned as a
// Result; err is reserved for infrastructure misuse.
func (r *DeploymentRunner) Run(ctx context.Context, task *TaskPayload) (*Result, error) {
	if task == nil {
		return nil, errors.New("deployment runner: nil task payload")
	}
	args, err := parseDeploymentArgs(task.Arguments)
	if err != nil {
		return deployFailure("INVALID_ARGUMENTS", err.Error()), nil
	}
	switch task.CommandID {
	case "deployment.sync":
		return r.runSync(ctx, args)
	case "deployment.install":
		return r.runReleaseHook(ctx, args, "install")
	case "deployment.update":
		return r.runReleaseHook(ctx, args, "update")
	case "deployment.backup":
		return r.runBackup(ctx, args)
	case "deployment.health-check":
		return r.runHealthCheck(ctx, args)
	case "deployment.rollback":
		return r.runRollback(ctx, args)
	default:
		return deployFailure("UNKNOWN_COMMAND", fmt.Sprintf("unknown deployment command %q", task.CommandID)), nil
	}
}

// ---- argument parsing (strict whitelist) ----

// deploymentArgFields is the runner-side argument whitelist. release_version
// is accepted (scheduler compatibility, used for release location and the
// rendered runtime-config); secret body/credentials are never accepted.
var deploymentArgFields = map[string]bool{
	"operation_id":    true,
	"target_id":       true,
	"node_id":         true,
	"feature_key":     true,
	"release_id":      true,
	"config_hash":     true,
	"secret_refs":     true,
	"release_version": true,
	"environment_id":  true,
}

type deploymentSecretRef struct {
	RefID          string
	ObjectKey      string
	Version        string
	ContentHash    string
	EncryptionMode string
}

type deploymentArgs struct {
	OperationID    string
	TargetID       string
	NodeID         string
	FeatureKey     string
	ReleaseID      string
	ConfigHash     string
	ReleaseVersion string
	EnvironmentID  string
	SecretRefs     []deploymentSecretRef
}

func parseDeploymentArgs(raw json.RawMessage) (*deploymentArgs, error) {
	var m map[string]any
	if len(raw) == 0 {
		return nil, errors.New("missing arguments")
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errors.New("arguments are not a JSON object")
	}
	for k := range m {
		if !deploymentArgFields[k] {
			return nil, fmt.Errorf("unexpected argument %q", k)
		}
	}
	req := []string{"operation_id", "target_id", "node_id"}
	for _, k := range req {
		if s, ok := m[k].(string); !ok || s == "" {
			return nil, fmt.Errorf("missing required argument %q", k)
		}
	}
	args := &deploymentArgs{
		OperationID:    m["operation_id"].(string),
		TargetID:       m["target_id"].(string),
		NodeID:         m["node_id"].(string),
		FeatureKey:     strField(m["feature_key"]),
		ReleaseID:      strField(m["release_id"]),
		ConfigHash:     strField(m["config_hash"]),
		ReleaseVersion: strField(m["release_version"]),
		EnvironmentID:  strField(m["environment_id"]),
	}
	refs, err := parseSecretRefs(m["secret_refs"])
	if err != nil {
		return nil, err
	}
	args.SecretRefs = refs
	return args, nil
}

func strField(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

var secretRefFields = map[string]bool{
	"ref_id":          true,
	"object_key":      true,
	"version":         true,
	"content_hash":    true,
	"hash":            true, // scheduler compatibility alias
	"encryption_mode": true,
}

func parseSecretRefs(v any) ([]deploymentSecretRef, error) {
	if v == nil {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, errors.New("secret_refs must be an array")
	}
	out := make([]deploymentSecretRef, 0, len(arr))
	for i, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("secret_refs[%d] must be an object", i)
		}
		for k := range obj {
			if !secretRefFields[k] {
				return nil, fmt.Errorf("secret_refs[%d]: unexpected field %q", i, k)
			}
		}
		ref := deploymentSecretRef{
			RefID:          strField(obj["ref_id"]),
			ObjectKey:      strField(obj["object_key"]),
			Version:        strField(obj["version"]),
			ContentHash:    strField(obj["content_hash"]),
			EncryptionMode: strField(obj["encryption_mode"]),
		}
		if ref.ContentHash == "" {
			ref.ContentHash = strField(obj["hash"])
		}
		if ref.RefID == "" || ref.ObjectKey == "" || ref.Version == "" || ref.ContentHash == "" || ref.EncryptionMode == "" {
			return nil, fmt.Errorf("secret_refs[%d]: ref_id/object_key/version/content_hash/encryption_mode are required", i)
		}
		if ref.EncryptionMode != deploySecretMode {
			return nil, fmt.Errorf("secret_refs[%d]: unsupported encryption_mode %q", i, ref.EncryptionMode)
		}
		if err := repo.ValidateRelPath(ref.ObjectKey); err != nil {
			return nil, fmt.Errorf("secret_refs[%d]: %w", i, err)
		}
		out = append(out, ref)
	}
	return out, nil
}

// ---- result helpers ----

func deploySuccess(stdout, stderr string) *Result {
	return &Result{Status: "succeeded", StdoutText: stdout, StderrText: stderr, FinishedAt: time.Now().UTC()}
}

func deployFailure(code, msg string) *Result {
	return &Result{Status: "failed", ErrorCode: code, ErrorMessage: msg, FinishedAt: time.Now().UTC()}
}

func deployFailureOutput(code, msg, stdout, stderr string) *Result {
	res := deployFailure(code, msg)
	res.StdoutText = stdout
	res.StderrText = stderr
	return res
}

func deployFailureExit(code, msg, stdout, stderr string, exitCode int) *Result {
	res := deployFailureOutput(code, msg, stdout, stderr)
	res.ExitCode = &exitCode
	return res
}

// ctxFailure maps an expired context to a timed_out/cancelled Result. It
// returns nil while the context is still live.
func ctxFailure(ctx context.Context) *Result {
	if ctx.Err() == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Result{Status: "timed_out", ErrorCode: "TIMED_OUT", ErrorMessage: "deployment task timed out", FinishedAt: time.Now().UTC()}
	}
	return &Result{Status: "cancelled", ErrorCode: "CANCELLED", ErrorMessage: "deployment task cancelled", FinishedAt: time.Now().UTC()}
}

// ---- preflight / sync ----

// preflight ensures the layout, verifies the repository (syncing first when
// the manifest is missing) and normalises permissions.
func (r *DeploymentRunner) preflight(ctx context.Context) (*repo.Layout, error) {
	lay := repo.New(r.cfg.DeploymentRootDir)
	if err := lay.EnsureAll(ctx); err != nil {
		return nil, fmt.Errorf("ensure deployment layout: %w", err)
	}
	manifest := filepath.Join(lay.RepoDir(), repo.DirManifests, repo.ManifestFileName)
	if _, err := os.Stat(manifest); err == nil {
		if err := repo.VerifyManifest(ctx, lay.RepoDir()); err != nil {
			return nil, fmt.Errorf("verify repository manifest: %w", err)
		}
		if err := r.verifyRepoSignature(lay); err != nil {
			return nil, fmt.Errorf("verify repository signature: %w", err)
		}
	} else if os.IsNotExist(err) {
		// First sync required: the repository has not been provisioned yet.
		if err := r.sync(ctx); err != nil {
			return nil, fmt.Errorf("initial repository sync: %w", err)
		}
	} else {
		return nil, err
	}
	if err := repo.FixPermissions(r.cfg.DeploymentRootDir); err != nil {
		return nil, fmt.Errorf("fix repository permissions: %w", err)
	}
	return lay, nil
}

// verifyRepoSignature loads the node deploy signing key and verifies the
// repository manifest's HMAC-SHA256 signature. It fails closed when the key
// is missing or the signature does not match.
func (r *DeploymentRunner) verifyRepoSignature(lay *repo.Layout) error {
	key, err := r.loadDeploySigningKey()
	if err != nil {
		return err
	}
	manifest, err := repo.LoadManifest(lay.RepoDir())
	if err != nil {
		return err
	}
	return verifyManifestSignature(manifest, key)
}

// loadDeploySigningKey reads the node deploy signing key
// (.servercli-local/credentials/deploy-signing.key, 0600). A missing key is
// a hard failure: verification is fail-closed by design.
func (r *DeploymentRunner) loadDeploySigningKey() ([]byte, error) {
	p := filepath.Join(r.cfg.DeploymentRootDir, deploySigningKeyRel)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("deploy signing key not provisioned")
		}
		return nil, fmt.Errorf("read deploy signing key: %w", err)
	}
	// Best-effort enforcement of the 0600 invariant; the read already
	// succeeded and FixPermissions re-checks.
	_ = os.Chmod(p, 0o600)
	return data, nil
}

// verifyManifestSignature recomputes the manifest HMAC-SHA256 signature with
// the node deploy signing key and compares it to the manifest's Signature
// field. The canonical serialisation lives in the repo package
// (repo.CanonicalManifestPayload) so control-plane signing and node
// verification always agree.
func verifyManifestSignature(m *repo.RepositoryManifest, key []byte) error {
	if m == nil {
		return errors.New("manifest is nil")
	}
	if m.Signature == "" {
		return errors.New("repository manifest signature missing")
	}
	return repo.VerifyManifestSignature(m, key)
}

func (r *DeploymentRunner) runSync(ctx context.Context, args *deploymentArgs) (*Result, error) {
	if err := r.sync(ctx); err != nil {
		if res := ctxFailure(ctx); res != nil {
			return res, nil
		}
		return deployFailure("SYNC_FAILED", err.Error()), nil
	}
	return deploySuccess("deployment repository synchronized", ""), nil
}

func (r *DeploymentRunner) sync(ctx context.Context) error {
	lay := repo.New(r.cfg.DeploymentRootDir)
	if err := lay.EnsureAll(ctx); err != nil {
		return fmt.Errorf("ensure deployment layout: %w", err)
	}
	profile, err := r.loadOSSProfile()
	if err != nil {
		return err
	}
	client, err := r.newOSS(ctx, *profile)
	if err != nil {
		return fmt.Errorf("build OSS client: %w", err)
	}
	prefix := profile.Prefix
	if prefix == "" {
		prefix = deployRepoPrefix
	}
	prefix = strings.TrimRight(prefix, "/") + "/"

	objects, err := client.ListObjects(ctx, profile.Bucket, prefix, "")
	if err != nil {
		return fmt.Errorf("list repository objects: %w", err)
	}
	repoDir := lay.RepoDir()
	downloaded := 0
	for _, obj := range objects {
		if obj.IsPrefix {
			continue
		}
		rel := strings.TrimPrefix(obj.Key, prefix)
		if rel == "" || rel == "." {
			continue
		}
		// The .servercli-local runtime zone is never synced.
		if rel == repo.LocalDirLocal || strings.HasPrefix(rel, repo.LocalDirLocal+"/") || strings.Contains(rel, "/"+repo.LocalDirLocal+"/") {
			continue
		}
		if err := repo.ValidateRelPath(rel); err != nil {
			return fmt.Errorf("unsafe object key %q: %w", obj.Key, err)
		}
		if err := r.downloadObject(ctx, client, profile.Bucket, obj.Key, filepath.Join(repoDir, rel)); err != nil {
			return err
		}
		downloaded++
	}
	if downloaded == 0 {
		return fmt.Errorf("no objects found under OSS prefix %q", prefix)
	}
	if err := repo.VerifyManifest(ctx, repoDir); err != nil {
		return fmt.Errorf("verify repository after sync: %w", err)
	}
	if err := r.verifyRepoSignature(lay); err != nil {
		return fmt.Errorf("verify repository signature after sync: %w", err)
	}
	if err := repo.FixPermissions(r.cfg.DeploymentRootDir); err != nil {
		return fmt.Errorf("fix repository permissions: %w", err)
	}
	return nil
}

// verifyBundleSignature checks releases/.../bundle.sig: the file must hold a
// 64-character hex HMAC-SHA256 over the bundle's SHA-256 hex digest, computed
// with the node deploy signing key.
func verifyBundleSignature(bundlePath, releaseDir string, key []byte) error {
	wantData, err := os.ReadFile(filepath.Join(releaseDir, "bundle.sig"))
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("bundle signature file missing")
		}
		return fmt.Errorf("read bundle signature: %w", err)
	}
	want := strings.TrimSpace(string(wantData))
	if len(want) != 64 {
		return errors.New("bundle signature file is not a 64 character hex digest")
	}
	sum, err := sha256File(bundlePath)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(sum))
	got := hex.EncodeToString(mac.Sum(nil))
	if !strings.EqualFold(want, got) {
		return errors.New("bundle signature mismatch")
	}
	return nil
}

func (r *DeploymentRunner) downloadObject(ctx context.Context, client OSSClient, bucket, key, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create object parent: %w", err)
	}
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return fmt.Errorf("create object file %s: %w", dst, err)
	}
	_, gerr := client.GetObject(ctx, bucket, key, f)
	cerr := f.Close()
	if gerr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("get object %s: %w", key, gerr)
	}
	if cerr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close object file: %w", cerr)
	}
	if err := os.Chmod(tmp, 0o640); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod object file: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install object file: %w", err)
	}
	return nil
}

func (r *DeploymentRunner) loadOSSProfile() (*OSSProfile, error) {
	p := filepath.Join(r.cfg.DeploymentRootDir, deployCredFileRel)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("OSS credentials not provisioned")
		}
		return nil, fmt.Errorf("read OSS credentials: %w", err)
	}
	var profile OSSProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, errors.New("invalid OSS credentials file")
	}
	if profile.Endpoint == "" || profile.Bucket == "" || profile.AccessKeyID == "" || profile.AccessKeySecret == "" {
		return nil, errors.New("OSS credentials incomplete")
	}
	// Best-effort enforcement of the 0600 invariant; failures are non-fatal
	// because the read already succeeded and FixPermissions re-checks.
	_ = os.Chmod(p, 0o600)
	return &profile, nil
}

// ---- release location & manifest ----

// releaseManifest mirrors doc/15 §3.1 (flat form). The seed FeatureRelease
// envelope (metadata/spec/bundle) is also accepted.
type releaseManifest struct {
	FeatureKey   string `json:"feature_key"`
	Version      string `json:"version"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	ObjectKey    string `json:"object_key"`
	BundleFile   string `json:"bundle_file"`
	BackupMode   string `json:"backup_mode"`
	InstallHook  string `json:"install_hook"`
	UpdateHook   string `json:"update_hook"`
	BackupHook   string `json:"backup_hook"`
	HealthHook   string `json:"health_hook"`
	RollbackHook string `json:"rollback_hook"`
}

type releaseManifestEnvelope struct {
	Metadata struct {
		FeatureKey string `json:"feature_key"`
		Version    string `json:"version"`
	} `json:"metadata"`
	Spec struct {
		FeatureKey string `json:"feature_key"`
		Version    string `json:"version"`
		Bundle     struct {
			File   string `json:"file"`
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size"`
		} `json:"bundle"`
	} `json:"spec"`
}

func parseReleaseManifest(data []byte) (*releaseManifest, error) {
	var rel releaseManifest
	if err := json.Unmarshal(data, &rel); err != nil {
		return nil, fmt.Errorf("parse release manifest: %w", err)
	}
	if rel.FeatureKey == "" || rel.Version == "" {
		var env releaseManifestEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, fmt.Errorf("parse release manifest envelope: %w", err)
		}
		if rel.FeatureKey == "" {
			rel.FeatureKey = env.Metadata.FeatureKey
			if rel.FeatureKey == "" {
				rel.FeatureKey = env.Spec.FeatureKey
			}
		}
		if rel.Version == "" {
			rel.Version = env.Metadata.Version
			if rel.Version == "" {
				rel.Version = env.Spec.Version
			}
		}
		if rel.SHA256 == "" {
			rel.SHA256 = env.Spec.Bundle.SHA256
		}
		if rel.Size == 0 {
			rel.Size = env.Spec.Bundle.Size
		}
		if rel.BundleFile == "" {
			rel.BundleFile = env.Spec.Bundle.File
		}
	}
	if rel.BundleFile == "" {
		rel.BundleFile = repo.BundleFileName
	}
	if rel.FeatureKey == "" || rel.Version == "" || rel.SHA256 == "" {
		return nil, errors.New("release manifest missing feature_key/version/sha256")
	}
	return &rel, nil
}

// hook returns the manifest hook path for kind ("install"/"update"/"backup"/
// "health"/"rollback"), defaulting to the fixed bundle-relative path.
func (rel *releaseManifest) hook(kind string) string {
	switch kind {
	case "install":
		if rel.InstallHook != "" {
			return rel.InstallHook
		}
		return "hooks/install.sh"
	case "update":
		if rel.UpdateHook != "" {
			return rel.UpdateHook
		}
		return "hooks/update.sh"
	case "backup":
		if rel.BackupHook != "" {
			return rel.BackupHook
		}
		return "hooks/backup.sh"
	case "health":
		if rel.HealthHook != "" {
			return rel.HealthHook
		}
		return "hooks/health-check.sh"
	case "rollback":
		if rel.RollbackHook != "" {
			return rel.RollbackHook
		}
		return "hooks/rollback.sh"
	}
	return ""
}

// validateHookPath rejects absolute paths and ".." traversal.
func validateHookPath(hook string) error {
	if hook == "" {
		return errors.New("hook path is empty")
	}
	if filepath.IsAbs(hook) {
		return fmt.Errorf("hook path %q is absolute", hook)
	}
	clean := filepath.Clean(hook)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("hook path %q escapes the release", hook)
	}
	for _, comp := range strings.Split(filepath.ToSlash(clean), "/") {
		if comp == ".." {
			return fmt.Errorf("hook path %q contains a .. component", hook)
		}
	}
	return nil
}

// locateRelease finds the unique release manifest for a feature under
// repository/releases/<feature>/<version>/<sha256>/. When release_version is
// given it narrows the search; ambiguity fails closed.
func (r *DeploymentRunner) locateRelease(lay *repo.Layout, args *deploymentArgs) (*releaseManifest, string, error) {
	if args.FeatureKey == "" {
		return nil, "", errors.New("feature_key is required")
	}
	if err := validateToken("feature_key", args.FeatureKey); err != nil {
		return nil, "", err
	}
	base := filepath.Join(lay.RepoDir(), repo.DirReleases, args.FeatureKey)
	versions, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", fmt.Errorf("no releases for feature %q", args.FeatureKey)
		}
		return nil, "", err
	}
	type candidate struct {
		version, sha, dir string
	}
	var cands []candidate
	for _, v := range versions {
		if !v.IsDir() {
			continue
		}
		// Defensive: directory names come from the synced repository, but the
		// release path is joined below so the safe charset is enforced first.
		if err := validateToken("version", v.Name()); err != nil {
			return nil, "", err
		}
		if args.ReleaseVersion != "" && v.Name() != args.ReleaseVersion {
			continue
		}
		shas, err := os.ReadDir(filepath.Join(base, v.Name()))
		if err != nil {
			return nil, "", err
		}
		for _, s := range shas {
			if !s.IsDir() {
				continue
			}
			if err := validateToken("sha256", s.Name()); err != nil {
				return nil, "", err
			}
			mp := filepath.Join(base, v.Name(), s.Name(), "manifest.json")
			if fi, serr := os.Stat(mp); serr == nil && fi.Mode().IsRegular() {
				cands = append(cands, candidate{version: v.Name(), sha: s.Name(), dir: filepath.Join(base, v.Name(), s.Name())})
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].version != cands[j].version {
			return cands[i].version < cands[j].version
		}
		return cands[i].sha < cands[j].sha
	})
	if len(cands) == 0 {
		hint := ""
		if args.ReleaseVersion != "" {
			hint = "/" + args.ReleaseVersion
		}
		return nil, "", fmt.Errorf("release not found for feature %q%s", args.FeatureKey, hint)
	}
	if len(cands) > 1 {
		return nil, "", fmt.Errorf("ambiguous release for feature %q (%d candidates); specify release_version", args.FeatureKey, len(cands))
	}
	data, err := os.ReadFile(filepath.Join(cands[0].dir, "manifest.json"))
	if err != nil {
		return nil, "", err
	}
	rel, err := parseReleaseManifest(data)
	if err != nil {
		return nil, "", err
	}
	if rel.FeatureKey != "" && rel.FeatureKey != args.FeatureKey {
		return nil, "", fmt.Errorf("release manifest feature mismatch: %q != %q", rel.FeatureKey, args.FeatureKey)
	}
	return rel, cands[0].dir, nil
}

// verifyBundleHash checks the bundle file's SHA-256 against the release
// manifest.
func verifyBundleHash(bundlePath, wantSHA string) error {
	f, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("bundle sha256 mismatch: want %s got %s", wantSHA, got)
	}
	return nil
}

// ---- install / update ----

func (r *DeploymentRunner) runReleaseHook(ctx context.Context, args *deploymentArgs, kind string) (*Result, error) {
	if args.FeatureKey == "" || args.ReleaseID == "" {
		return deployFailure("MISSING_PARAMETER", "feature_key and release_id are required for deployment."+kind), nil
	}
	if err := validateToken("feature_key", args.FeatureKey); err != nil {
		return deployFailure("INVALID_FEATURE_KEY", err.Error()), nil
	}
	lay, err := r.preflight(ctx)
	if err != nil {
		return deployFailure("PREFLIGHT_FAILED", err.Error()), nil
	}
	rel, releaseDir, err := r.locateRelease(lay, args)
	if err != nil {
		return deployFailure("RELEASE_NOT_FOUND", err.Error()), nil
	}
	// Version/sha256 are joined into staging/rendered paths below; validate
	// them before any path construction or removal.
	if err := validateToken("version", rel.Version); err != nil {
		return deployFailure("INVALID_VERSION", err.Error()), nil
	}
	if err := validateToken("sha256", rel.SHA256); err != nil {
		return deployFailure("INVALID_SHA256", err.Error()), nil
	}

	bundlePath := filepath.Join(releaseDir, rel.BundleFile)
	if err := verifyBundleHash(bundlePath, rel.SHA256); err != nil {
		return deployFailure("BUNDLE_HASH_MISMATCH", err.Error()), nil
	}
	key, err := r.loadDeploySigningKey()
	if err != nil {
		return deployFailure("DEPLOY_SIGNING_KEY_MISSING", err.Error()), nil
	}
	if err := verifyBundleSignature(bundlePath, releaseDir, key); err != nil {
		return deployFailure("BUNDLE_SIGNATURE_MISMATCH", err.Error()), nil
	}

	// Staging + atomic switch to the rendered dir.
	stagingDir := filepath.Join(lay.LocalDir(), repo.DirStaging, args.FeatureKey, rel.Version, rel.SHA256)
	if err := os.RemoveAll(stagingDir); err != nil {
		return deployFailure("STAGING_CLEANUP_FAILED", err.Error()), nil
	}
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return deployFailure("STAGING_CREATE_FAILED", err.Error()), nil
	}
	limits := deployExtractLimits
	limits.AllowedRoot = lay.LocalDir()
	if err := extractBundle(ctx, bundlePath, stagingDir, limits); err != nil {
		return deployFailure("EXTRACT_FAILED", err.Error()), nil
	}
	renderedDir := filepath.Join(lay.LocalDir(), repo.DirRendered, args.FeatureKey, rel.Version)
	if err := os.MkdirAll(filepath.Dir(renderedDir), 0o700); err != nil {
		return deployFailure("RENDERED_CREATE_FAILED", err.Error()), nil
	}
	if err := repo.SwitchDir(stagingDir, renderedDir); err != nil {
		return deployFailure("SWITCH_DIR_FAILED", err.Error()), nil
	}

	hookRel := rel.hook(kind)
	if err := validateHookPath(hookRel); err != nil {
		return deployFailure("INVALID_HOOK_PATH", err.Error()), nil
	}
	hookPath := filepath.Join(renderedDir, hookRel)
	if !isRegularWithin(hookPath, renderedDir) {
		return deployFailure("HOOK_NOT_FOUND", fmt.Sprintf("hook %q missing inside rendered release", hookRel)), nil
	}

	if err := r.writeRuntimeConfig(ctx, lay, renderedDir, args, rel); err != nil {
		return deployFailure("RENDER_CONFIG_FAILED", err.Error()), nil
	}
	// Materialized secret copies are transient: they must be removed after
	// the main hook runs, whether it succeeds or fails (and also if
	// materialization itself fails partway).
	defer r.cleanupMaterializedSecrets(lay, args, renderedDir)
	if err := r.materializeSecrets(ctx, lay, args, renderedDir); err != nil {
		return deployFailure("SECRET_MATERIALIZE_FAILED", err.Error()), nil
	}
	if err := repo.FixPermissions(r.cfg.DeploymentRootDir); err != nil {
		return deployFailure("FIX_PERMISSIONS_FAILED", err.Error()), nil
	}

	exitCode, stdout, stderr, err := r.runHook(ctx, hookRel, renderedDir, r.hookArgsInstall(args, rel, renderedDir, releaseDir))
	if err != nil {
		if res := ctxFailure(ctx); res != nil {
			return res, nil
		}
		return deployFailureOutput("HOOK_EXEC_FAILED", err.Error(), stdout, stderr), nil
	}
	if exitCode != 0 {
		return deployFailureExit("HOOK_FAILED", fmt.Sprintf("%s hook exited with code %d", kind, exitCode), stdout, stderr, exitCode), nil
	}
	return deploySuccess(stdout, stderr), nil
}

func extractBundle(ctx context.Context, bundlePath, stagingDir string, limits repo.Limits) error {
	f, err := os.Open(bundlePath)
	if err != nil {
		return fmt.Errorf("open bundle: %w", err)
	}
	defer f.Close()
	return repo.ExtractTarGzip(ctx, f, stagingDir, limits)
}

func isRegularWithin(p, root string) bool {
	fi, err := os.Lstat(p)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return false
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// writeRuntimeConfig merges the feature configuration for one release render
// and writes it to runtime-config.yaml (0600):
//
//	bundle manifest config defaults < repository/configs/shared/<feature>.yaml
//	< repository/configs/nodes/<node>/<feature>.yaml < derived fields
//
// Derived fields always win. When the frozen config_hash is present the
// merged map's canonical JSON SHA-256 must match it; a mismatch fails so a
// stale repository (unsynced configs) can never run a release against the
// wrong configuration.
func (r *DeploymentRunner) writeRuntimeConfig(ctx context.Context, lay *repo.Layout, renderedDir string, args *deploymentArgs, rel *releaseManifest) error {
	if err := ctx.Err(); err != nil {
		return ctx.Err()
	}
	hostname, _ := os.Hostname()
	derived := map[string]any{
		"node_id":             args.NodeID,
		"hostname":            hostname,
		"feature_key":         args.FeatureKey,
		"release_version":     rel.Version,
		"deployment_root_dir": r.cfg.DeploymentRootDir,
		"operation_id":        args.OperationID,
		"data_directory":      filepath.Join(r.cfg.DeploymentRootDir, repo.LocalDirLocal, "runtime", args.FeatureKey),
	}
	// environment_id is a derived field when the control plane provides it;
	// an absent value must not overwrite a configured environment_id.
	if args.EnvironmentID != "" {
		derived["environment_id"] = args.EnvironmentID
	}
	shared, err := r.loadConfigYAML(
		filepath.Join(lay.RepoDir(), repo.DirConfigs, "shared", args.FeatureKey+".yaml"),
		filepath.Join(lay.RepoDir(), repo.DirConfigs, "shared", args.FeatureKey+"-shared.yaml"),
	)
	if err != nil {
		return err
	}
	nodeCfg, err := r.loadConfigYAML(
		filepath.Join(lay.RepoDir(), repo.DirConfigs, "nodes", args.NodeID, args.FeatureKey+".yaml"),
		filepath.Join(lay.RepoDir(), repo.DirConfigs, "nodes", args.NodeID, args.FeatureKey+"-node.yaml"),
	)
	if err != nil {
		return err
	}
	// The frozen config hash covers ONLY the static merged config (feature
	// defaults are release-bundle-scoped and derived fields are
	// machine-specific), matching the control plane's ResolveConfig freeze.
	static := mergeDeployConfig(map[string]any{}, shared, nodeCfg, nil)
	hash, err := deployConfigHash(static)
	if err != nil {
		return fmt.Errorf("hash static config: %w", err)
	}
	merged := mergeDeployConfig(bundleConfigDefaults(renderedDir), shared, nodeCfg, derived)
	if args.ConfigHash != "" && !strings.EqualFold(args.ConfigHash, hash) {
		return fmt.Errorf("config hash mismatch (frozen %s, rendered %s): repository configs are stale; sync the repository first", args.ConfigHash, hash)
	}
	merged["release_id"] = args.ReleaseID
	if args.ConfigHash != "" {
		merged["config_hash"] = args.ConfigHash
	} else {
		merged["config_hash"] = hash
	}
	raw, err := yaml.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal runtime config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(renderedDir, "runtime-config.yaml"), raw, 0o600); err != nil {
		return fmt.Errorf("write runtime config: %w", err)
	}
	return nil
}

// loadConfigYAML parses the first existing YAML config file among candidates
// into a map. Missing files are ignored; read/parse errors fail closed.
func (r *DeploymentRunner) loadConfigYAML(candidates ...string) (map[string]any, error) {
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read config %s: %w", p, err)
		}
		var m map[string]any
		if err := yaml.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", p, err)
		}
		return m, nil
	}
	return map[string]any{}, nil
}

// bundleConfigDefaults extracts spec.config_schema.defaults from the bundle's
// root manifest.yaml (best effort; missing or invalid → empty defaults).
func bundleConfigDefaults(renderedDir string) map[string]any {
	data, err := os.ReadFile(filepath.Join(renderedDir, "manifest.yaml"))
	if err != nil {
		return map[string]any{}
	}
	var m struct {
		Spec struct {
			ConfigSchema map[string]any `yaml:"config_schema"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return map[string]any{}
	}
	if defaults, ok := m.Spec.ConfigSchema["defaults"].(map[string]any); ok {
		return defaults
	}
	return map[string]any{}
}

// mergeDeployConfig merges config sources in priority order; later sources
// override earlier ones and derived fields always win.
func mergeDeployConfig(defaults, shared, node, derived map[string]any) map[string]any {
	out := map[string]any{}
	for _, m := range []map[string]any{defaults, shared, node, derived} {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// deployConfigHash returns the canonical SHA-256 of a merged config map
// (JSON serialisation; encoding/json sorts map keys deterministically).
func deployConfigHash(m map[string]any) (string, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// cleanupMaterializedSecrets removes every transient secret copy under
// rendered/<...>/secrets/<ref_id>.yaml after the main hook ran. It is
// idempotent and never touches the repository secrets zone.
func (r *DeploymentRunner) cleanupMaterializedSecrets(lay *repo.Layout, args *deploymentArgs, renderedDir string) {
	if len(args.SecretRefs) == 0 {
		return
	}
	secretsDir := filepath.Join(renderedDir, "secrets")
	provider := secretprovider.NewPlaintextProvider(
		filepath.Join(lay.RepoDir(), repo.DirSecrets),
		map[string]secretprovider.RepositorySecretCodec{
			deploySecretMode: secretprovider.NewPlaintextSecretCodec(),
		},
		secretprovider.WithRenderedRoot(secretsDir),
	)
	for _, s := range args.SecretRefs {
		ref := secretprovider.SecretReference{
			ID:             s.RefID,
			ObjectKey:      s.RefID + ".yaml",
			Version:        s.Version,
			ContentHash:    s.ContentHash,
			EncryptionMode: s.EncryptionMode,
			Size:           -1,
		}
		if err := provider.Cleanup(context.Background(), ref); err != nil {
			r.log.Warn("cleanup materialized secret failed", "ref_id", s.RefID, "error", err)
		}
	}
}

func (r *DeploymentRunner) materializeSecrets(ctx context.Context, lay *repo.Layout, args *deploymentArgs, renderedDir string) error {
	if len(args.SecretRefs) == 0 {
		return nil
	}
	secretsDir := filepath.Join(renderedDir, "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		return fmt.Errorf("create rendered secrets dir: %w", err)
	}
	// The provider is rooted at repository/secrets: its permission walk
	// requires every directory from the secret up to (and including) the root
	// to be 0700, which matches the layout for the secrets zone (the
	// repository root itself is 0750).
	provider := secretprovider.NewPlaintextProvider(
		filepath.Join(lay.RepoDir(), repo.DirSecrets),
		map[string]secretprovider.RepositorySecretCodec{
			deploySecretMode: secretprovider.NewPlaintextSecretCodec(),
		},
		secretprovider.WithRenderedRoot(secretsDir),
	)
	for _, s := range args.SecretRefs {
		rel, err := localSecretRel(s.ObjectKey)
		if err != nil {
			return err
		}
		ref := secretprovider.SecretReference{
			ID:             s.RefID,
			ObjectKey:      rel,
			Version:        s.Version,
			ContentHash:    s.ContentHash,
			EncryptionMode: s.EncryptionMode,
			Size:           -1,
		}
		if err := provider.Materialize(ctx, ref, s.RefID+".yaml"); err != nil {
			return fmt.Errorf("materialize secret %q: %w", s.RefID, err)
		}
	}
	return nil
}

// localSecretRel maps an OSS secret object key (with the repository prefix,
// e.g. "deployment-repository/secrets/nodes/<node>/<feature>.secrets.yaml")
// to the path relative to repository/secrets ("nodes/<node>/<feature>..."),
// which is what PlaintextSecretProvider expects.
func localSecretRel(objectKey string) (string, error) {
	rel := objectKey
	rel = strings.TrimPrefix(rel, deployRepoPrefix)
	rel = strings.TrimPrefix(rel, repo.DirSecrets+"/")
	if err := repo.ValidateRelPath(rel); err != nil {
		return "", fmt.Errorf("invalid secret object key: %w", err)
	}
	return rel, nil
}

// hookArgsInstall is the fixed install/update hook argument whitelist. Values
// are derived only from validated fields; nothing is concatenated from
// untrusted strings.
func (r *DeploymentRunner) hookArgsInstall(args *deploymentArgs, rel *releaseManifest, renderedDir, releaseDir string) []string {
	return []string{
		"--feature-key", args.FeatureKey,
		"--node-id", args.NodeID,
		"--operation-id", args.OperationID,
		"--deployment-root-dir", r.cfg.DeploymentRootDir,
		"--release-version", rel.Version,
		"--data-dir", filepath.Join(renderedDir, "data"),
		"--config-dir", renderedDir,
		"--rendered-dir", renderedDir,
		"--config-file", filepath.Join(renderedDir, "runtime-config.yaml"),
		"--release-dir", releaseDir,
		"--image-tag", "",
		"--port", "",
	}
}

// ---- health-check ----

func (r *DeploymentRunner) runHealthCheck(ctx context.Context, args *deploymentArgs) (*Result, error) {
	if args.FeatureKey == "" || args.NodeID == "" {
		return deployFailure("MISSING_PARAMETER", "feature_key and node_id are required for deployment.health-check"), nil
	}
	if err := validateToken("feature_key", args.FeatureKey); err != nil {
		return deployFailure("INVALID_FEATURE_KEY", err.Error()), nil
	}
	lay, err := r.preflight(ctx)
	if err != nil {
		return deployFailure("PREFLIGHT_FAILED", err.Error()), nil
	}
	rel, _, err := r.locateRelease(lay, args)
	if err != nil {
		return deployFailure("RELEASE_NOT_FOUND", err.Error()), nil
	}
	if err := validateToken("version", rel.Version); err != nil {
		return deployFailure("INVALID_VERSION", err.Error()), nil
	}
	renderedDir := filepath.Join(lay.LocalDir(), repo.DirRendered, args.FeatureKey, rel.Version)
	hookRel := rel.hook("health")
	if err := validateHookPath(hookRel); err != nil {
		return deployFailure("INVALID_HOOK_PATH", err.Error()), nil
	}
	hookPath := filepath.Join(renderedDir, hookRel)
	if !isRegularWithin(hookPath, renderedDir) {
		return deployFailure("HOOK_NOT_FOUND", fmt.Sprintf("health hook %q missing (feature not installed?)", hookRel)), nil
	}
	argv := []string{
		"--feature-key", args.FeatureKey,
		"--node-id", args.NodeID,
		"--rendered-dir", renderedDir,
		"--port", "",
	}
	exitCode, stdout, stderr, err := r.runHook(ctx, hookRel, renderedDir, argv)
	if err != nil {
		if res := ctxFailure(ctx); res != nil {
			return res, nil
		}
		return deployFailureOutput("HOOK_EXEC_FAILED", err.Error(), stdout, stderr), nil
	}
	if exitCode != 0 {
		return deployFailureExit("HEALTH_CHECK_FAILED", fmt.Sprintf("health check hook exited with code %d", exitCode), stdout, stderr, exitCode), nil
	}
	return deploySuccess(stdout, stderr), nil
}

// ---- backup ----

func (r *DeploymentRunner) runBackup(ctx context.Context, args *deploymentArgs) (*Result, error) {
	if args.FeatureKey == "" || args.NodeID == "" || args.OperationID == "" {
		return deployFailure("MISSING_PARAMETER", "feature_key, node_id and operation_id are required for deployment.backup"), nil
	}
	if err := validateToken("feature_key", args.FeatureKey); err != nil {
		return deployFailure("INVALID_FEATURE_KEY", err.Error()), nil
	}
	lay, err := r.preflight(ctx)
	if err != nil {
		return deployFailure("PREFLIGHT_FAILED", err.Error()), nil
	}
	rel, _, err := r.locateRelease(lay, args)
	if err != nil {
		return deployFailure("RELEASE_NOT_FOUND", err.Error()), nil
	}
	if err := validateToken("version", rel.Version); err != nil {
		return deployFailure("INVALID_VERSION", err.Error()), nil
	}
	renderedDir := filepath.Join(lay.LocalDir(), repo.DirRendered, args.FeatureKey, rel.Version)
	hookRel := rel.hook("backup")
	if err := validateHookPath(hookRel); err != nil {
		return deployFailure("INVALID_HOOK_PATH", err.Error()), nil
	}
	hookPath := filepath.Join(renderedDir, hookRel)
	if !isRegularWithin(hookPath, renderedDir) {
		return deployFailure("HOOK_NOT_FOUND", fmt.Sprintf("backup hook %q missing (feature not installed?)", hookRel)), nil
	}
	dataDir := filepath.Join(renderedDir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return deployFailure("DATA_DIR_CREATE_FAILED", err.Error()), nil
	}
	argv := []string{
		"--feature-key", args.FeatureKey,
		"--node-id", args.NodeID,
		"--operation-id", args.OperationID,
		"--data-dir", dataDir,
		"--deployment-root-dir", r.cfg.DeploymentRootDir,
		"--rendered-dir", renderedDir,
	}
	exitCode, stdout, stderr, err := r.runHook(ctx, hookRel, renderedDir, argv)
	if err != nil {
		if res := ctxFailure(ctx); res != nil {
			return res, nil
		}
		return deployFailureOutput("HOOK_EXEC_FAILED", err.Error(), stdout, stderr), nil
	}
	if exitCode != 0 {
		return deployFailureExit("BACKUP_HOOK_FAILED", fmt.Sprintf("backup hook exited with code %d", exitCode), stdout, stderr, exitCode), nil
	}

	backupPath := lastStdoutPath(stdout)
	if backupPath == "" {
		return deployFailure("BACKUP_PATH_MISSING", "backup hook did not print a backup file path"), nil
	}
	if !strings.HasSuffix(backupPath, ".tar.gz") || !filepath.IsAbs(backupPath) {
		return deployFailure("BACKUP_PATH_INVALID", "backup hook printed an invalid backup path"), nil
	}
	fi, err := os.Stat(backupPath)
	if err != nil || !fi.Mode().IsRegular() {
		return deployFailure("BACKUP_FILE_MISSING", "backup file produced by hook is missing"), nil
	}

	// The backup archive must never include rendered secret material: reject
	// any member whose path contains a "secrets" component.
	if err := backupContainsSecrets(backupPath); err != nil {
		return deployFailure("BACKUP_CONTAINS_SECRETS", err.Error()), nil
	}

	size := fi.Size()
	sha, err := sha256File(backupPath)
	if err != nil {
		return deployFailure("BACKUP_HASH_FAILED", err.Error()), nil
	}

	profile, err := r.loadOSSProfile()
	if err != nil {
		return deployFailure("OSS_CREDENTIALS_MISSING", err.Error()), nil
	}
	client, err := r.newOSS(ctx, *profile)
	if err != nil {
		return deployFailure("OSS_CLIENT_FAILED", err.Error()), nil
	}
	objectKey := backupObjectKey(r.cfg.AppEnv, args.FeatureKey, args.NodeID, args.OperationID)
	if err := ossclient.ValidateObjectKey(objectKey); err != nil {
		return deployFailure("BACKUP_KEY_INVALID", err.Error()), nil
	}
	f, err := os.Open(backupPath)
	if err != nil {
		return deployFailure("BACKUP_OPEN_FAILED", err.Error()), nil
	}
	perr := client.PutObject(ctx, profile.Bucket, objectKey, f, size, "application/gzip")
	cerr := f.Close()
	if perr != nil {
		return deployFailure("BACKUP_UPLOAD_FAILED", perr.Error()), nil
	}
	if cerr != nil {
		return deployFailure("BACKUP_UPLOAD_FAILED", cerr.Error()), nil
	}
	meta, err := client.HeadObject(ctx, profile.Bucket, objectKey)
	if err != nil {
		return deployFailure("BACKUP_VERIFY_FAILED", err.Error()), nil
	}
	if meta.Size != size {
		return deployFailure("BACKUP_VERIFY_FAILED", fmt.Sprintf("uploaded size mismatch: want %d got %d", size, meta.Size)), nil
	}
	if meta.ETag == "" {
		return deployFailure("BACKUP_VERIFY_FAILED", "uploaded object missing etag"), nil
	}

	summary, err := json.Marshal(map[string]any{
		"object_key":  objectKey,
		"size":        size,
		"sha256":      sha,
		"backup_mode": rel.BackupMode,
	})
	if err != nil {
		return deployFailure("SUMMARY_FAILED", err.Error()), nil
	}
	res := deploySuccess(stdout, stderr)
	res.SummaryJSON = string(summary)
	return res, nil
}

func backupObjectKey(env, featureKey, nodeID, operationID string) string {
	if env == "" {
		env = "default"
	}
	now := time.Now().UTC()
	return fmt.Sprintf("backups/%s/%s/%s/%04d/%02d/%02d/%s/backup.tar.gz",
		env, featureKey, nodeID, now.Year(), int(now.Month()), now.Day(), operationID)
}

// lastStdoutPath returns the last non-empty stdout line (the backup hook's
// output path convention).
func lastStdoutPath(stdout string) string {
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}

// backupContainsSecrets scans a backup tar.gz and rejects it when any member
// path contains a "secrets" component, so rendered secret material can never
// be packaged into a backup artifact.
func backupContainsSecrets(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open backup gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read backup tar: %w", err)
		}
		for _, comp := range strings.Split(filepath.ToSlash(hdr.Name), "/") {
			if comp == "secrets" {
				return fmt.Errorf("backup archive contains secrets path %q", hdr.Name)
			}
		}
	}
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---- rollback ----

func (r *DeploymentRunner) runRollback(ctx context.Context, args *deploymentArgs) (*Result, error) {
	if args.FeatureKey == "" || args.NodeID == "" || args.ReleaseID == "" {
		return deployFailure("MISSING_PARAMETER", "feature_key, node_id and release_id are required for deployment.rollback"), nil
	}
	if err := validateToken("feature_key", args.FeatureKey); err != nil {
		return deployFailure("INVALID_FEATURE_KEY", err.Error()), nil
	}
	lay, err := r.preflight(ctx)
	if err != nil {
		return deployFailure("PREFLIGHT_FAILED", err.Error()), nil
	}
	rel, _, err := r.locateRelease(lay, args)
	if err != nil {
		return deployFailure("RELEASE_NOT_FOUND", err.Error()), nil
	}
	if err := validateToken("version", rel.Version); err != nil {
		return deployFailure("INVALID_VERSION", err.Error()), nil
	}
	renderedBase := filepath.Join(lay.LocalDir(), repo.DirRendered, args.FeatureKey)
	previousDir := filepath.Join(renderedBase, rel.Version)
	if !isRegularWithin(filepath.Join(previousDir, rel.hook("rollback")), previousDir) {
		return deployFailure("PREVIOUS_RELEASE_MISSING", fmt.Sprintf("rollback hook for previous release %q not found", rel.Version)), nil
	}
	currentDir, err := otherRenderedVersion(renderedBase, rel.Version)
	if err != nil {
		return deployFailure("CURRENT_RELEASE_UNKNOWN", err.Error()), nil
	}
	argv := []string{
		"--feature-key", args.FeatureKey,
		"--node-id", args.NodeID,
		"--operation-id", args.OperationID,
		"--deployment-root-dir", r.cfg.DeploymentRootDir,
		"--previous-release-dir", previousDir,
		"--current-release-dir", currentDir,
	}
	exitCode, stdout, stderr, err := r.runHook(ctx, rel.hook("rollback"), previousDir, argv)
	if err != nil {
		if res := ctxFailure(ctx); res != nil {
			return res, nil
		}
		return deployFailureOutput("HOOK_EXEC_FAILED", err.Error(), stdout, stderr), nil
	}
	if exitCode != 0 {
		return deployFailureExit("ROLLBACK_HOOK_FAILED", fmt.Sprintf("rollback hook exited with code %d", exitCode), stdout, stderr, exitCode), nil
	}
	return deploySuccess(stdout, stderr), nil
}

// otherRenderedVersion returns the rendered version directory that is not
// version (the currently installed release), failing when it cannot be
// determined.
func otherRenderedVersion(base, version string) (string, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("read rendered dir: %w", err)
	}
	var others []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == version {
			continue
		}
		if _, serr := os.Stat(filepath.Join(base, e.Name(), "runtime-config.yaml")); serr == nil {
			others = append(others, e.Name())
		}
	}
	sort.Strings(others)
	if len(others) != 1 {
		return "", fmt.Errorf("cannot determine current release (candidates=%d)", len(others))
	}
	return filepath.Join(base, others[0]), nil
}

// ---- hook execution ----

// runHook executes the hook (hookRel, relative to renderedDir) via the fixed
// root wrapper with argv. Output is redacted before it is returned. The hook
// runs in its own process group and ctx cancellation terminates the whole
// group (SIGTERM, then SIGKILL after a grace period) so no descendant
// survives. The wrapper requires a relative hook path anchored to
// --rendered-dir and is invoked via sudo -n (the agent runs unprivileged).
func (r *DeploymentRunner) runHook(ctx context.Context, hookRel, renderedDir string, argv []string) (int, string, string, error) {
	if _, err := os.Stat(r.wrapperPath); err != nil {
		r.log.Error("deploy wrapper missing; refusing to fall back to a bare root shell",
			"wrapper", r.wrapperPath, "hook", hookRel, "error", err)
		return -1, "", "", fmt.Errorf("deploy wrapper %s missing: %w", r.wrapperPath, err)
	}
	wrapperArgs := append([]string{r.wrapperPath, "/bin/bash", hookRel}, argv...)
	var cmd *exec.Cmd
	if len(r.sudoPrefix) > 0 {
		cmdArgs := append(append([]string{}, r.sudoPrefix...), wrapperArgs...)
		cmd = exec.Command(cmdArgs[0], cmdArgs[1:]...)
	} else {
		cmd = exec.Command(wrapperArgs[0], wrapperArgs[1:]...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = minimalEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return -1, "", "", err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			terminateProcessGroup(cmd)
		case <-done:
		}
	}()
	waitErr := cmd.Wait()
	close(done)
	if waitErr != nil {
		// Cancellation wins: surface it so callers map to a cancelled or
		// timed-out Result instead of a generic hook failure.
		if ctx.Err() != nil {
			return -1, redactSensitive(stdout.String()), redactSensitive(stderr.String()), ctx.Err()
		}
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			return ee.ExitCode(), redactSensitive(stdout.String()), redactSensitive(stderr.String()), nil
		}
		return -1, redactSensitive(stdout.String()), redactSensitive(stderr.String()), waitErr
	}
	return 0, redactSensitive(stdout.String()), redactSensitive(stderr.String()), nil
}

// compile-time interface assertion
var _ OSSClient = (*ossclient.Client)(nil)
