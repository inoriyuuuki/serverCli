package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"servercli/internal/config"
	"servercli/internal/deployment/ossclient"
	"servercli/internal/deployment/repo"
	"servercli/internal/deployment/secretprovider"
	"servercli/internal/model"
	"servercli/internal/store"
)

// SeedDir is the control-plane seed repository, relative to the repository
// root (the control plane's working directory). It holds the initial
// catalog/features/releases/configs content copied into the deployment
// repository on the first RepositorySync.
const SeedDir = "deploy/deployment/seed"

// deploymentRepositoryPrefix is the fixed OSS prefix under which the whole
// deployment repository is mirrored (see doc/13_DEPLOYMENT_MANAGEMENT.md).
const deploymentRepositoryPrefix = "deployment-repository/"

// validBackupModes is the allowlist for DeploymentFeature.BackupMode.
// safeKeyRe is the allowlist for feature keys and release versions: only
// letters, digits, dot, underscore and dash. This blocks path-injection
// characters when the value is embedded in repository paths / object keys.
var safeKeyRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

var validBackupModes = map[string]bool{
	"database_dump":        true,
	"application_snapshot": true,
	"filesystem_quiesced":  true,
	"cold_backup":          true,
	"external_snapshot":    true,
	"none":                 true,
}

// DeploymentService implements the deployment management core: features,
// releases, OSS profiles, repository sync, config profiles, secret overwrite,
// targets, config resolution, bootstrap sessions and agent upload
// authorization. All OSS credentials and secret bodies stay out of logs,
// audit events and API responses.
type DeploymentService struct {
	store   *store.Store
	cfg     *config.Config
	log     *slog.Logger
	auditor *Auditor
	tasks   *TaskService
	nodes   *NodeService
	ossKey  []byte
}

// NewDeploymentService builds the deployment service. The OSS credential
// encryption key is derived from the control plane master key.
func NewDeploymentService(st *store.Store, cfg *config.Config, log *slog.Logger, auditor *Auditor, tasks *TaskService, nodes *NodeService) (*DeploymentService, error) {
	key, err := MasterKey(cfg)
	if err != nil {
		return nil, err
	}
	return &DeploymentService{
		store:   st,
		cfg:     cfg,
		log:     log,
		auditor: auditor,
		tasks:   tasks,
		nodes:   nodes,
		ossKey:  key,
	}, nil
}

// ─── Input / output types ────────────────────────────────────────────────

// CreateFeatureInput describes a new deployment feature.
type CreateFeatureInput struct {
	FeatureKey          string `json:"feature_key"`
	Name                string `json:"name"`
	Description         string `json:"description"`
	BackupMode          string `json:"backup_mode"`
	RollbackCapability  string `json:"rollback_capability"`
	MinimumAgentVersion string `json:"minimum_agent_version"`
	ConfigSchemaJSON    string `json:"config_schema_json"`
}

// CreateReleaseInput describes a new immutable feature release.
type CreateReleaseInput struct {
	FeatureID                 string `json:"feature_id"`
	Version                   string `json:"version"`
	SourceCommit              string `json:"source_commit"`
	ObjectKey                 string `json:"object_key"`
	Size                      int64  `json:"size"`
	SHA256                    string `json:"sha256"`
	Signature                 string `json:"signature"`
	InstallHook               string `json:"install_hook"`
	UpdateHook                string `json:"update_hook"`
	BackupHook                string `json:"backup_hook"`
	HealthHook                string `json:"health_hook"`
	RollbackHook              string `json:"rollback_hook"`
	RestoreHook               string `json:"restore_hook,omitempty"`
	BackupMode                string `json:"backup_mode"`
	DataMigrationMetadataJSON string `json:"data_migration_metadata_json"`
}

// OSSProfileInput describes an OSS profile. AccessKeyID/AccessKeySecret are
// write-only: they are encrypted at rest and never returned.
type OSSProfileInput struct {
	Name            string `json:"name"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	AccessKeySecret string `json:"access_key_secret"`
}

// ConfigProfileInput describes a config profile. ContentYAML is validated and
// stored as normalized JSON.
type ConfigProfileInput struct {
	Name        string `json:"name"`
	ScopeType   string `json:"scope_type"`
	ScopeID     string `json:"scope_id"`
	FeatureID   string `json:"feature_id"`
	ContentYAML string `json:"content_yaml"`
}

// OverwriteSecretInput carries a new secret body. Value never enters logs,
// audit events or error messages.
type OverwriteSecretInput struct {
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

// SecretReferenceInput describes a new secret reference (metadata only; the
// body is written later through OverwriteSecret).
type SecretReferenceInput struct {
	Name      string `json:"name"`
	FeatureID string `json:"feature_id"`
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id"`
}

// TargetInput describes a feature pinned to a node.
type TargetInput struct {
	FeatureID        string `json:"feature_id"`
	NodeID           string `json:"node_id"`
	ConfigProfileID  string `json:"config_profile_id"`
	DesiredReleaseID string `json:"desired_release_id"`
}

// BootstrapSessionInput describes a node bootstrap session request.
type BootstrapSessionInput struct {
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix"`
	Region string `json:"region"`
	NodeID string `json:"node_id"`
}

// BootstrapSessionResult returns the session plus the one-time token (shown
// exactly once) and the one-line bootstrap command for the node operator.
type BootstrapSessionResult struct {
	Session *model.BootstrapSession `json:"session"`
	Token   string                  `json:"token"`
	Command string                  `json:"command"`
}

// AgentUploadAuthorizeInput is the agent-side upload authorization request.
type AgentUploadAuthorizeInput struct {
	OperationID string `json:"operation_id"`
	TargetID    string `json:"target_id"`
	FeatureKey  string `json:"feature_key"`
}

// AgentUploadAuthorization is the scoped upload authorization returned to a
// node agent. V1 returns the long-lived profile endpoint/bucket with a
// precise prefix; credentials are not disclosed (V1 degradation: the long
// term credential must be limited to that prefix via RAM policy).
type AgentUploadAuthorization struct {
	Endpoint        string `json:"endpoint"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	CredentialsType string `json:"credentials_type"`
}

// RepositorySyncResult summarizes a repository sync run.
type RepositorySyncResult struct {
	StartedAt   time.Time `json:"started_at"`
	Status      string    `json:"status"`
	ObjectCount int       `json:"object_count"`
}

// DeploymentTargetView augments a deployment target with the feature key and
// node display name.
type DeploymentTargetView struct {
	*model.DeploymentTarget
	FeatureKey string `json:"feature_key"`
	NodeName   string `json:"node_name"`
}

// ─── helpers ─────────────────────────────────────────────────────────────

func (s *DeploymentService) envID() string { return s.cfg.InstanceName + "-env" }

func mapStoreErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, store.ErrConflict) {
		return ErrConflict
	}
	return err
}

// auditDeployment records a deployment audit event with whitelisted details.
func (s *DeploymentService) auditDeployment(ctx context.Context, actorType, actorID, action, result string, fields map[string]any) {
	_ = s.auditor.OK(ctx, AuditInput{
		ActorType: actorType,
		ActorID:   actorID,
		Action:    action,
		Details:   DeploymentAuditDetails(fields),
		RiskLevel: RiskMedium,
		Result:    result,
	})
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// encryptOSSSecret encrypts an OSS access key pair value with AES-256-GCM
// using the control plane master key. The output is base64(nonce+ciphertext).
func encryptOSSSecret(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("deployment: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("deployment: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("deployment: nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(append(nonce, ciphertext...)), nil
}

func decryptOSSSecret(key []byte, encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("deployment: decode encrypted secret: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("deployment: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("deployment: gcm: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("deployment: encrypted secret too short")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("deployment: decrypt secret: %w", err)
	}
	return string(plain), nil
}

// decryptProfileCredentials returns the plaintext AK/SK of an OSS profile.
func (s *DeploymentService) decryptProfileCredentials(p *model.OSSProfile) (string, string, error) {
	ak, err := decryptOSSSecret(s.ossKey, p.AccessKeyIDEnc)
	if err != nil {
		return "", "", err
	}
	sk, err := decryptOSSSecret(s.ossKey, p.AccessKeySecretEnc)
	if err != nil {
		return "", "", err
	}
	return ak, sk, nil
}

// primaryOSSProfile returns the first configured OSS profile.
func (s *DeploymentService) primaryOSSProfile(ctx context.Context) (*model.OSSProfile, error) {
	profiles, err := s.store.ListOSSProfiles(ctx)
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, ErrNotFound
	}
	return profiles[0], nil
}

// ossClientFor builds a validated OSS client for a profile, decrypting its
// credentials. Credentials are never logged.
func (s *DeploymentService) ossClientFor(p *model.OSSProfile) (*ossclient.Client, error) {
	ak, sk, err := s.decryptProfileCredentials(p)
	if err != nil {
		return nil, err
	}
	return ossclient.New(p.Endpoint, ossclient.Credentials{AccessKeyID: ak, AccessKeySecret: sk})
}

// writeFileAtomic writes data to path via a temp file + rename with explicit
// file and directory modes. No secret body ever appears in error messages.
func writeFileAtomic(path string, data []byte, fileMode, dirMode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return fmt.Errorf("chmod dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

// copyDir recursively copies src to dst, skipping no files.
func copyDir(ctx context.Context, src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		if err := repo.ValidateRelPath(filepath.ToSlash(rel)); err != nil {
			return err
		}
		dstPath := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(dstPath, 0o750)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return writeFileAtomic(dstPath, data, 0o640, 0o750)
	})
}

func mergeConfigMaps(dst, src map[string]any) {
	for k, v := range src {
		dst[k] = v
	}
}

// ─── Repository signing key ──────────────────────────────────────────────

// signingKeyPath returns the path of the deployment repository signing key
// (inside the .servercli-local runtime zone, never synced or uploaded).
func (s *DeploymentService) signingKeyPath() string {
	return filepath.Join(s.cfg.DeploymentRootDir, repo.LocalDirLocal, repo.DirCredentials, "deploy-signing.key")
}

// ensureSigningKey loads the deployment repository signing key or lazily
// generates a fresh 32-byte key. The key file is created 0600 inside a 0700
// directory. The key is never printed, logged, returned in errors, or
// uploaded to the repository/OSS.
func (s *DeploymentService) ensureSigningKey(ctx context.Context) ([]byte, error) {
	path := s.signingKeyPath()
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) == 0 {
			return nil, fmt.Errorf("deployment: signing key file is empty")
		}
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("deployment: read signing key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("deployment: generate signing key: %w", err)
	}
	if err := writeFileAtomic(path, key, 0o600, 0o700); err != nil {
		return nil, fmt.Errorf("deployment: write signing key: %w", err)
	}
	return key, ctx.Err()
}

// MaterializeDeploymentSigningKey hands the repository signing key to a node
// bootstrap session. Authentication is the one-time session token (only its
// SHA-256 is stored). Revoked/expired/cancelled sessions are rejected. The
// key is returned to the caller exactly once over the HTTPS materialize
// endpoint; it is never logged or written to audit details.
func (s *DeploymentService) MaterializeDeploymentSigningKey(ctx context.Context, token string) ([]byte, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: session_token is required", ErrBadRequest)
	}
	sess, err := s.store.BootstrapSessionByTokenHash(ctx, sha256Hex([]byte(token)))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	now := time.Now().UTC()
	if sess.RevokedAt != nil ||
		sess.Status == model.BootstrapStatusCancelled ||
		sess.Status == model.BootstrapStatusExpired ||
		sess.ExpiresAt.Before(now) {
		s.auditDeployment(ctx, model.ActorNode, sess.NodeID, "deployment.bootstrap.materialize", ResultFailure, map[string]any{
			"node_id": sess.NodeID, "action": "deployment.bootstrap.materialize",
		})
		return nil, ErrForbidden
	}
	key, err := s.ensureSigningKey(ctx)
	if err != nil {
		s.auditDeployment(ctx, model.ActorNode, sess.NodeID, "deployment.bootstrap.materialize", ResultFailure, map[string]any{
			"node_id": sess.NodeID, "action": "deployment.bootstrap.materialize",
		})
		return nil, err
	}
	s.auditDeployment(ctx, model.ActorNode, sess.NodeID, "deployment.bootstrap.materialize", ResultSuccess, map[string]any{
		"node_id": sess.NodeID, "action": "deployment.bootstrap.materialize", "result": ResultSuccess,
	})
	return key, nil
}

// ─── Features ────────────────────────────────────────────────────────────

// CreateFeature registers a new deployment feature.
func (s *DeploymentService) CreateFeature(ctx context.Context, actorID string, in CreateFeatureInput) (*model.DeploymentFeature, error) {
	if strings.TrimSpace(in.FeatureKey) == "" {
		return nil, fmt.Errorf("%w: feature_key is required", ErrBadRequest)
	}
	if !safeKeyRe.MatchString(in.FeatureKey) {
		return nil, fmt.Errorf("%w: feature_key may only contain letters, digits, dot, underscore and dash", ErrBadRequest)
	}
	if in.BackupMode == "" {
		in.BackupMode = "none"
	}
	if !validBackupModes[in.BackupMode] {
		return nil, fmt.Errorf("%w: backup_mode must be one of database_dump, application_snapshot, filesystem_quiesced, cold_backup, external_snapshot, none", ErrBadRequest)
	}
	if in.ConfigSchemaJSON != "" && !json.Valid([]byte(in.ConfigSchemaJSON)) {
		return nil, fmt.Errorf("%w: config_schema_json must be valid JSON", ErrBadRequest)
	}
	f := &model.DeploymentFeature{
		ID:                  model.NewUUID(),
		FeatureKey:          strings.TrimSpace(in.FeatureKey),
		Name:                in.Name,
		Description:         in.Description,
		BackupMode:          in.BackupMode,
		RollbackCapability:  in.RollbackCapability,
		MinimumAgentVersion: in.MinimumAgentVersion,
		ConfigSchemaJSON:    in.ConfigSchemaJSON,
	}
	if err := s.store.CreateDeploymentFeature(ctx, f); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.feature.create", ResultFailure, map[string]any{
			"feature_key": f.FeatureKey, "action": "deployment.feature.create",
		})
		return nil, mapStoreErr(err)
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.feature.create", ResultSuccess, map[string]any{
		"feature_key": f.FeatureKey, "action": "deployment.feature.create", "result": ResultSuccess,
	})
	return f, nil
}

// ListFeatures returns all deployment features.
// UpdateFeature updates editable feature metadata (name/description/backup_mode/
// rollback_capability/minimum_agent_version/config_schema). feature_key and the
// immutable platform are not changed.
func (s *DeploymentService) UpdateFeature(ctx context.Context, actorID, id string, in CreateFeatureInput) (*model.DeploymentFeature, error) {
	f, err := s.store.DeploymentFeatureByID(ctx, id)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	if in.BackupMode != "" {
		if !validBackupMode(in.BackupMode) {
			return nil, fmt.Errorf("%w: invalid backup_mode %q", ErrBadRequest, in.BackupMode)
		}
		f.BackupMode = in.BackupMode
	}
	if in.Name != "" {
		f.Name = in.Name
	}
	f.Description = in.Description
	if in.RollbackCapability != "" {
		f.RollbackCapability = in.RollbackCapability
	}
	if in.MinimumAgentVersion != "" {
		f.MinimumAgentVersion = in.MinimumAgentVersion
	}
	if in.ConfigSchemaJSON != "" {
		if !json.Valid([]byte(in.ConfigSchemaJSON)) {
			return nil, fmt.Errorf("%w: config_schema_json must be valid JSON", ErrBadRequest)
		}
		f.ConfigSchemaJSON = in.ConfigSchemaJSON
	}
	f.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateDeploymentFeature(ctx, f); err != nil {
		return nil, mapStoreErr(err)
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.feature.update", ResultSuccess, map[string]any{
		"feature_key": f.FeatureKey, "action": "deployment.feature.update", "result": ResultSuccess,
	})
	return f, nil
}

func validBackupMode(m string) bool {
	switch m {
	case "database_dump", "application_snapshot", "filesystem_quiesced", "cold_backup", "external_snapshot", "none":
		return true
	}
	return false
}

func (s *DeploymentService) ListFeatures(ctx context.Context) ([]*model.DeploymentFeature, error) {
	return s.store.ListDeploymentFeatures(ctx)
}

// GetFeature returns one deployment feature.
func (s *DeploymentService) GetFeature(ctx context.Context, id string) (*model.DeploymentFeature, error) {
	f, err := s.store.DeploymentFeatureByID(ctx, id)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return f, nil
}

// DeleteFeature removes a deployment feature.
func (s *DeploymentService) DeleteFeature(ctx context.Context, actorID, id string) error {
	f, err := s.store.DeploymentFeatureByID(ctx, id)
	if err != nil {
		return mapStoreErr(err)
	}
	if err := s.store.DeleteDeploymentFeature(ctx, id); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.feature.delete", ResultFailure, map[string]any{
			"feature_key": f.FeatureKey, "action": "deployment.feature.delete",
		})
		return err
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.feature.delete", ResultSuccess, map[string]any{
		"feature_key": f.FeatureKey, "action": "deployment.feature.delete", "result": ResultSuccess,
	})
	return nil
}

// ─── Releases ────────────────────────────────────────────────────────────

// CreateRelease registers an immutable feature release.
func (s *DeploymentService) CreateRelease(ctx context.Context, actorID string, in CreateReleaseInput) (*model.DeploymentRelease, error) {
	feature, err := s.store.DeploymentFeatureByID(ctx, in.FeatureID)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	if strings.TrimSpace(in.Version) == "" {
		return nil, fmt.Errorf("%w: version is required", ErrBadRequest)
	}
	if !safeKeyRe.MatchString(in.Version) {
		return nil, fmt.Errorf("%w: version may only contain letters, digits, dot, underscore and dash", ErrBadRequest)
	}
	if len(in.SHA256) != 64 {
		return nil, fmt.Errorf("%w: sha256 must be a 64 character hex digest", ErrBadRequest)
	}
	if _, err := hex.DecodeString(in.SHA256); err != nil {
		return nil, fmt.Errorf("%w: sha256 must be a 64 character hex digest", ErrBadRequest)
	}
	if err := ossclient.ValidateObjectKey(in.ObjectKey); err != nil {
		return nil, fmt.Errorf("%w: object_key: %v", ErrBadRequest, err)
	}
	if !strings.HasPrefix(in.ObjectKey, deploymentRepositoryPrefix) {
		return nil, fmt.Errorf("%w: object_key must be inside the %s prefix", ErrBadRequest, deploymentRepositoryPrefix)
	}
	if in.Size <= 0 {
		return nil, fmt.Errorf("%w: size must be positive", ErrBadRequest)
	}
	r := &model.DeploymentRelease{
		ID:                        model.NewUUID(),
		FeatureID:                 in.FeatureID,
		Version:                   in.Version,
		SourceCommit:              in.SourceCommit,
		ObjectKey:                 in.ObjectKey,
		Size:                      in.Size,
		SHA256:                    in.SHA256,
		Signature:                 in.Signature,
		InstallHook:               in.InstallHook,
		UpdateHook:                in.UpdateHook,
		BackupHook:                in.BackupHook,
		HealthHook:                in.HealthHook,
		RollbackHook:              in.RollbackHook,
		RestoreHook:               in.RestoreHook,
		BackupMode:                in.BackupMode,
		DataMigrationMetadataJSON: in.DataMigrationMetadataJSON,
	}
	if err := s.store.CreateDeploymentRelease(ctx, r); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.release.create", ResultFailure, map[string]any{
			"feature_key": feature.FeatureKey, "release_version": in.Version, "action": "deployment.release.create",
		})
		return nil, mapStoreErr(err)
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.release.create", ResultSuccess, map[string]any{
		"feature_key": feature.FeatureKey, "release_version": r.Version, "action": "deployment.release.create", "result": ResultSuccess,
	})
	return r, nil
}

// ListReleases returns releases of a feature (empty featureID = all).
func (s *DeploymentService) ListReleases(ctx context.Context, featureID string) ([]*model.DeploymentRelease, error) {
	return s.store.ListDeploymentReleases(ctx, featureID)
}

// ─── OSS profiles ────────────────────────────────────────────────────────

// CreateOSSProfile stores an OSS profile. AK/SK are encrypted with AES-256-GCM
// before hitting the database; the plaintext credentials never leave this
// function scope except through validated OSS client construction.
func (s *DeploymentService) CreateOSSProfile(ctx context.Context, actorID string, in OSSProfileInput) (*model.OSSProfile, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("%w: name is required", ErrBadRequest)
	}
	if in.AccessKeyID == "" || in.AccessKeySecret == "" {
		return nil, fmt.Errorf("%w: access_key_id and access_key_secret are required", ErrBadRequest)
	}
	if err := ossclient.ValidateBucket(in.Bucket); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	// Constructing a client validates the endpoint (SSRF allowlist) and the
	// credentials shape. It performs no network I/O.
	if _, err := ossclient.New(in.Endpoint, ossclient.Credentials{AccessKeyID: in.AccessKeyID, AccessKeySecret: in.AccessKeySecret}); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	idEnc, err := encryptOSSSecret(s.ossKey, in.AccessKeyID)
	if err != nil {
		return nil, err
	}
	secretEnc, err := encryptOSSSecret(s.ossKey, in.AccessKeySecret)
	if err != nil {
		return nil, err
	}
	p := &model.OSSProfile{
		ID:                 model.NewUUID(),
		Name:               in.Name,
		Endpoint:           strings.TrimRight(in.Endpoint, "/"),
		Region:             in.Region,
		Bucket:             in.Bucket,
		Prefix:             strings.Trim(in.Prefix, "/"),
		AccessKeyIDEnc:     idEnc,
		AccessKeySecretEnc: secretEnc,
	}
	if err := s.store.CreateOSSProfile(ctx, p); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.oss-profile.create", ResultFailure, map[string]any{
			"name": in.Name, "action": "deployment.oss-profile.create",
		})
		return nil, mapStoreErr(err)
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.oss-profile.create", ResultSuccess, map[string]any{
		"name": p.Name, "action": "deployment.oss-profile.create", "result": ResultSuccess,
	})
	return p, nil
}

// ListOSSProfiles returns all OSS profiles with credential fields stripped so
// that JSON serialization never contains access key material.
func (s *DeploymentService) ListOSSProfiles(ctx context.Context) ([]*model.OSSProfile, error) {
	profiles, err := s.store.ListOSSProfiles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*model.OSSProfile, 0, len(profiles))
	for _, p := range profiles {
		cp := *p
		cp.AccessKeyIDEnc = ""
		cp.AccessKeySecretEnc = ""
		out = append(out, &cp)
	}
	return out, nil
}

// TestOSSProfile verifies connectivity with the profile's decrypted
// credentials and records the outcome on the profile.
func (s *DeploymentService) TestOSSProfile(ctx context.Context, id string) (bool, string, error) {
	p, err := s.store.OSSProfileByID(ctx, id)
	if err != nil {
		return false, "", mapStoreErr(err)
	}
	client, err := s.ossClientFor(p)
	if err != nil {
		s.recordTestResult(ctx, p, "failed: "+err.Error())
		return false, p.LastTestResult, nil
	}
	objs, err := client.ListObjects(ctx, p.Bucket, p.Prefix, "/")
	if err != nil {
		s.recordTestResult(ctx, p, "failed: "+err.Error())
		s.auditDeployment(ctx, model.ActorAdmin, "", "deployment.oss-profile.test", ResultFailure, map[string]any{
			"name": p.Name, "action": "deployment.oss-profile.test",
		})
		return false, p.LastTestResult, nil
	}
	msg := fmt.Sprintf("ok: %d objects under prefix", len(objs))
	s.recordTestResult(ctx, p, msg)
	s.auditDeployment(ctx, model.ActorAdmin, "", "deployment.oss-profile.test", ResultSuccess, map[string]any{
		"name": p.Name, "action": "deployment.oss-profile.test", "result": ResultSuccess,
	})
	return true, msg, nil
}

func (s *DeploymentService) recordTestResult(ctx context.Context, p *model.OSSProfile, result string) {
	now := time.Now().UTC()
	p.LastTestedAt = &now
	p.LastTestResult = result
	_ = s.store.UpdateOSSProfile(ctx, p)
}

// UpdateOSSProfile edits a profile. Empty AK/SK means "keep the existing
// credentials".
func (s *DeploymentService) UpdateOSSProfile(ctx context.Context, actorID, id string, in OSSProfileInput) (*model.OSSProfile, error) {
	p, err := s.store.OSSProfileByID(ctx, id)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	if in.Name != "" {
		p.Name = in.Name
	}
	newEndpoint := p.Endpoint
	if in.Endpoint != "" {
		newEndpoint = strings.TrimRight(in.Endpoint, "/")
	}
	if in.Region != "" {
		p.Region = in.Region
	}
	if in.Bucket != "" {
		if err := ossclient.ValidateBucket(in.Bucket); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
		}
		p.Bucket = in.Bucket
	}
	p.Prefix = strings.Trim(in.Prefix, "/")
	// Resolve the effective credential pair for endpoint validation: use the
	// newly supplied AK/SK when present, otherwise the stored (decrypted) ones.
	effAK, effSK := in.AccessKeyID, in.AccessKeySecret
	if in.AccessKeyID == "" && in.AccessKeySecret == "" {
		effAK, effSK, err = s.decryptProfileCredentials(p)
		if err != nil {
			return nil, err
		}
	}
	// An endpoint change must pass the same SSRF allowlist validation as
	// CreateOSSProfile, even when the credentials are unchanged.
	if in.Endpoint != "" && newEndpoint != p.Endpoint {
		if _, verr := ossclient.New(newEndpoint, ossclient.Credentials{AccessKeyID: effAK, AccessKeySecret: effSK}); verr != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadRequest, verr)
		}
	}
	if in.AccessKeyID != "" || in.AccessKeySecret != "" {
		if in.AccessKeyID == "" || in.AccessKeySecret == "" {
			return nil, fmt.Errorf("%w: both access_key_id and access_key_secret must be provided together", ErrBadRequest)
		}
		if _, err := ossclient.New(newEndpoint, ossclient.Credentials{AccessKeyID: in.AccessKeyID, AccessKeySecret: in.AccessKeySecret}); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
		}
		idEnc, err := encryptOSSSecret(s.ossKey, in.AccessKeyID)
		if err != nil {
			return nil, err
		}
		secretEnc, err := encryptOSSSecret(s.ossKey, in.AccessKeySecret)
		if err != nil {
			return nil, err
		}
		p.AccessKeyIDEnc = idEnc
		p.AccessKeySecretEnc = secretEnc
	}
	p.Endpoint = newEndpoint
	if err := s.store.UpdateOSSProfile(ctx, p); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.oss-profile.update", ResultFailure, map[string]any{
			"name": p.Name, "action": "deployment.oss-profile.update",
		})
		return nil, mapStoreErr(err)
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.oss-profile.update", ResultSuccess, map[string]any{
		"name": p.Name, "action": "deployment.oss-profile.update", "result": ResultSuccess,
	})
	cp := *p
	cp.AccessKeyIDEnc = ""
	cp.AccessKeySecretEnc = ""
	return &cp, nil
}

// DeleteOSSProfile removes an OSS profile.
func (s *DeploymentService) DeleteOSSProfile(ctx context.Context, actorID, id string) error {
	p, err := s.store.OSSProfileByID(ctx, id)
	if err != nil {
		return mapStoreErr(err)
	}
	if err := s.store.DeleteOSSProfile(ctx, id); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.oss-profile.delete", ResultFailure, map[string]any{
			"name": p.Name, "action": "deployment.oss-profile.delete",
		})
		return err
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.oss-profile.delete", ResultSuccess, map[string]any{
		"name": p.Name, "action": "deployment.oss-profile.delete", "result": ResultSuccess,
	})
	return nil
}

// ─── Repository sync ─────────────────────────────────────────────────────

// RepositorySync rebuilds the local deployment repository from the seed and
// the database, verifies secret material, generates the repository manifest
// and uploads everything to the primary OSS profile. Fail closed: any missing
// secret, hash mismatch or upload error aborts the whole sync.
func (s *DeploymentService) RepositorySync(ctx context.Context, actorID string) (*RepositorySyncResult, error) {
	startedAt := time.Now().UTC()
	layout := repo.New(s.cfg.DeploymentRootDir)

	// The repository signing key is required for every sync; it is generated
	// lazily and never uploaded with the repository.
	key, err := s.ensureSigningKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("deployment: signing key: %w", err)
	}
	if err := layout.EnsureAll(ctx); err != nil {
		return nil, fmt.Errorf("deployment: ensure layout: %w", err)
	}
	if err := s.copySeedRepository(ctx, layout); err != nil {
		return nil, fmt.Errorf("deployment: seed copy: %w", err)
	}
	if err := s.syncConfigProfiles(ctx, layout); err != nil {
		return nil, fmt.Errorf("deployment: config sync: %w", err)
	}
	// Re-sign every release bundle.sig so the repository is self-consistent
	// before the manifest (which includes those signatures) is generated.
	if err := s.signReleaseBundles(ctx, layout, key); err != nil {
		return nil, fmt.Errorf("deployment: release bundle signing: %w", err)
	}
	if err := s.verifySecretReferences(ctx, layout); err != nil {
		return nil, fmt.Errorf("deployment: secret verify: %w", err)
	}
	objects, err := s.computeRepositoryObjects(ctx, layout)
	if err != nil {
		return nil, fmt.Errorf("deployment: manifest compute: %w", err)
	}
	manifest := &repo.RepositoryManifest{
		Version:   repo.ManifestVersion,
		Objects:   objects,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := repo.SignManifest(manifest, key); err != nil {
		return nil, fmt.Errorf("deployment: manifest sign: %w", err)
	}
	if err := s.writeManifest(layout, manifest); err != nil {
		return nil, fmt.Errorf("deployment: manifest write: %w", err)
	}
	if err := s.writeCanonicalManifest(layout, manifest); err != nil {
		return nil, fmt.Errorf("deployment: canonical manifest write: %w", err)
	}
	if err := repo.FixPermissions(s.cfg.DeploymentRootDir); err != nil {
		return nil, fmt.Errorf("deployment: fix permissions: %w", err)
	}
	objectCount, err := s.uploadRepository(ctx, layout)
	if err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.repository.sync", ResultFailure, map[string]any{
			"action": "deployment.repository.sync",
		})
		return nil, fmt.Errorf("deployment: repository upload: %w", err)
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.repository.sync", ResultSuccess, map[string]any{
		"action": "deployment.repository.sync", "result": ResultSuccess, "object_count": objectCount,
	})
	return &RepositorySyncResult{StartedAt: startedAt, Status: "succeeded", ObjectCount: objectCount}, nil
}

// copySeedRepository copies the seed catalog/features/releases/configs into
// the local repository. The secrets directory is skipped: real secret files
// are only ever written by OverwriteSecret, never copied from the seed.
func (s *DeploymentService) copySeedRepository(ctx context.Context, layout *repo.Layout) error {
	for _, sub := range []string{repo.DirCatalog, repo.DirFeatures, repo.DirReleases, repo.DirConfigs} {
		src := filepath.Join(SeedDir, sub)
		info, err := os.Stat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat seed dir %s: %w", src, err)
		}
		if !info.IsDir() {
			continue
		}
		if err := copyDir(ctx, src, filepath.Join(layout.RepoDir(), sub)); err != nil {
			return fmt.Errorf("copy seed %s: %w", sub, err)
		}
	}
	return nil
}

// syncConfigProfiles writes every DB config profile into the repository as
// YAML (shared scope → configs/shared/<name>.yaml; node scope →
// configs/nodes/<scope_id>/<name>.yaml).
func (s *DeploymentService) syncConfigProfiles(ctx context.Context, layout *repo.Layout) error {
	profiles, err := s.store.ListDeploymentConfigProfiles(ctx, "", "", "")
	if err != nil {
		return err
	}
	for _, p := range profiles {
		// Config files are named by feature_key so the control-plane freeze
		// (ResolveConfig) and the node runner read the same deterministic
		// path: configs/shared/<feature_key>.yaml and
		// configs/nodes/<node_id>/<feature_key>.yaml. Multiple profiles for
		// the same (feature, scope) would overwrite each other; the API layer
		// prevents that at creation time.
		feature, ferr := s.store.DeploymentFeatureByID(ctx, p.FeatureID)
		if ferr != nil {
			return fmt.Errorf("config profile %s: feature: %w", p.ID, ferr)
		}
		var content map[string]any
		if err := json.Unmarshal([]byte(p.ContentJSON), &content); err != nil {
			return fmt.Errorf("config profile %s: %w", p.ID, err)
		}
		yamlBytes, err := yaml.Marshal(content)
		if err != nil {
			return fmt.Errorf("config profile %s: %w", p.ID, err)
		}
		var rel string
		switch p.ScopeType {
		case model.ConfigScopeShared:
			rel = filepath.Join(repo.DirConfigs, "shared", feature.FeatureKey+".yaml")
		case model.ConfigScopeNode:
			if p.ScopeID == "" {
				return fmt.Errorf("config profile %s: node scope requires scope_id", p.ID)
			}
			rel = filepath.Join(repo.DirConfigs, "nodes", p.ScopeID, feature.FeatureKey+".yaml")
		default:
			return fmt.Errorf("config profile %s: invalid scope_type %q", p.ID, p.ScopeType)
		}
		if err := repo.ValidateRelPath(filepath.ToSlash(rel)); err != nil {
			return fmt.Errorf("config profile %s: %w", p.ID, err)
		}
		if err := writeFileAtomic(filepath.Join(layout.RepoDir(), rel), yamlBytes, 0o640, 0o750); err != nil {
			return fmt.Errorf("config profile %s: %w", p.ID, err)
		}
	}
	return nil
}

// verifySecretReferences checks that every DB secret reference has a matching
// local file under repository/secrets/ with the recorded content hash.
func (s *DeploymentService) verifySecretReferences(ctx context.Context, layout *repo.Layout) error {
	refs, err := s.store.ListDeploymentSecretReferences(ctx, "", "", "")
	if err != nil {
		return err
	}
	for _, ref := range refs {
		rel := strings.TrimPrefix(ref.ObjectKey, deploymentRepositoryPrefix)
		if err := repo.ValidateRelPath(rel); err != nil {
			return fmt.Errorf("secret reference %s: invalid object key", ref.ID)
		}
		path := filepath.Join(layout.RepoDir(), filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("secret reference %s: material missing on disk", ref.ID)
		}
		if sha256Hex(data) != ref.ContentHash {
			return fmt.Errorf("secret reference %s: content hash mismatch", ref.ID)
		}
	}
	return nil
}

// computeRepositoryObjects hashes every regular file under repository/ except
// the manifests directory (the manifest never includes itself).
func (s *DeploymentService) computeRepositoryObjects(ctx context.Context, layout *repo.Layout) ([]repo.ManifestObject, error) {
	root := layout.RepoDir()
	var objects []repo.ManifestObject
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		relSlash := filepath.ToSlash(rel)
		if d.IsDir() {
			if relSlash == repo.DirManifests {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if err := repo.ValidateRelPath(relSlash); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		objects = append(objects, repo.ManifestObject{
			Path:   relSlash,
			Size:   int64(len(data)),
			SHA256: sha256Hex(data),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Path < objects[j].Path })
	return objects, nil
}

func (s *DeploymentService) writeManifest(layout *repo.Layout, m *repo.RepositoryManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(layout.RepoDir(), repo.DirManifests, repo.ManifestFileName)
	return writeFileAtomic(path, data, 0o640, 0o750)
}

// writeCanonicalManifest writes the auxiliary canonical payload file that the
// node runner and the bootstrap script HMAC to verify the manifest signature.
// It is deliberately NOT listed in m.Objects (that would make the signed
// payload self-referential); uploadRepository walks the directory and uploads
// it alongside the manifest.
func (s *DeploymentService) writeCanonicalManifest(layout *repo.Layout, m *repo.RepositoryManifest) error {
	payload, err := repo.CanonicalManifestPayload(m)
	if err != nil {
		return err
	}
	path := filepath.Join(layout.RepoDir(), repo.DirManifests, repo.ManifestCanonicalFileName)
	return writeFileAtomic(path, payload, 0o640, 0o750)
}

// uploadRepository uploads every file under repository/ to the primary OSS
// profile under <prefix>/deployment-repository/<relpath>. Any failure aborts.
func (s *DeploymentService) uploadRepository(ctx context.Context, layout *repo.Layout) (int, error) {
	p, err := s.primaryOSSProfile(ctx)
	if err != nil {
		return 0, err
	}
	client, err := s.ossClientFor(p)
	if err != nil {
		return 0, err
	}
	basePrefix := strings.Trim(p.Prefix, "/")
	count := 0
	root := layout.RepoDir()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		relSlash := filepath.ToSlash(rel)
		if err := repo.ValidateRelPath(relSlash); err != nil {
			return err
		}
		objectKey := deploymentRepositoryPrefix + relSlash
		if basePrefix != "" {
			objectKey = basePrefix + "/" + objectKey
		}
		if err := ossclient.ValidateObjectKey(objectKey); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := client.PutObject(ctx, p.Bucket, objectKey, f, info.Size(), contentTypeFor(relSlash)); err != nil {
			return fmt.Errorf("upload %s: %w", relSlash, err)
		}
		count++
		return nil
	})
	if err != nil {
		return count, err
	}
	return count, nil
}

func contentTypeFor(path string) string {
	switch {
	case strings.HasSuffix(path, ".json"):
		return "application/json"
	case strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml"):
		return "application/yaml"
	case strings.HasSuffix(path, ".tar.gz") || strings.HasSuffix(path, ".tgz"):
		return "application/gzip"
	case strings.HasSuffix(path, ".sig"):
		return "application/octet-stream"
	default:
		return "application/octet-stream"
	}
}

// signReleaseBundles recomputes releases/**/bundle.sig for every release
// bundle.tar.gz present in the local repository: signature = hex(HMAC-SHA256(
// key, sha256(content))). The signature file is written atomically and is
// uploaded with the repository; the key itself is never written into the
// repository.
func (s *DeploymentService) signReleaseBundles(ctx context.Context, layout *repo.Layout, key []byte) error {
	releasesDir := filepath.Join(layout.RepoDir(), repo.DirReleases)
	if _, err := os.Stat(releasesDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return filepath.WalkDir(releasesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if filepath.Base(path) != "bundle.tar.gz" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(sha256Hex(data)))
		sigPath := filepath.Join(filepath.Dir(path), "bundle.sig")
		if err := writeFileAtomic(sigPath, []byte(hex.EncodeToString(mac.Sum(nil))+"\n"), 0o640, 0o750); err != nil {
			return err
		}
		return ctx.Err()
	})
}

// ─── Config profiles ─────────────────────────────────────────────────────

// CreateConfigProfile stores a validated config profile. The YAML input is
// normalized to JSON; the stored content hash is SHA-256 of that JSON.
func (s *DeploymentService) CreateConfigProfile(ctx context.Context, actorID string, in ConfigProfileInput) (*model.DeploymentConfigProfile, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("%w: name is required", ErrBadRequest)
	}
	if in.ScopeType != model.ConfigScopeShared && in.ScopeType != model.ConfigScopeNode {
		return nil, fmt.Errorf("%w: scope_type must be shared or node", ErrBadRequest)
	}
	if in.ScopeType == model.ConfigScopeNode && strings.TrimSpace(in.ScopeID) == "" {
		return nil, fmt.Errorf("%w: scope_id is required for node scope", ErrBadRequest)
	}
	if strings.TrimSpace(in.FeatureID) == "" {
		return nil, fmt.Errorf("%w: feature_id is required", ErrBadRequest)
	}
	if _, err := s.store.DeploymentFeatureByID(ctx, in.FeatureID); err != nil {
		return nil, mapStoreErr(err)
	}
	// One profile per (feature, scope) keeps the synced config file naming
	// deterministic (configs/shared/<feature_key>.yaml etc.) so the control
	// plane freeze and the node render always read the same file.
	existing, err := s.store.ListDeploymentConfigProfiles(ctx, in.ScopeType, in.ScopeID, in.FeatureID)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return nil, fmt.Errorf("%w: a config profile already exists for this feature and scope", ErrConflict)
	}
	var content map[string]any
	if err := yaml.Unmarshal([]byte(in.ContentYAML), &content); err != nil {
		return nil, fmt.Errorf("%w: content_yaml must be valid YAML: %v", ErrBadRequest, err)
	}
	if content == nil {
		content = map[string]any{}
	}
	jsonBytes, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	p := &model.DeploymentConfigProfile{
		ID:          model.NewUUID(),
		Name:        in.Name,
		ScopeType:   in.ScopeType,
		ScopeID:     in.ScopeID,
		FeatureID:   in.FeatureID,
		ContentJSON: string(jsonBytes),
		ContentHash: sha256Hex(jsonBytes),
		Version:     1,
	}
	if err := s.store.CreateDeploymentConfigProfile(ctx, p); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.config.create", ResultFailure, map[string]any{
			"config_hash": p.ContentHash, "action": "deployment.config.create",
		})
		return nil, mapStoreErr(err)
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.config.create", ResultSuccess, map[string]any{
		"config_hash": p.ContentHash, "action": "deployment.config.create", "result": ResultSuccess,
	})
	return p, nil
}

// ListConfigProfiles returns config profiles, optionally filtered by scope.
func (s *DeploymentService) ListConfigProfiles(ctx context.Context, scopeType, scopeID string) ([]*model.DeploymentConfigProfile, error) {
	return s.store.ListDeploymentConfigProfiles(ctx, scopeType, scopeID, "")
}

// UpdateConfigProfile replaces a profile's content, recomputes the hash and
// bumps the version.
func (s *DeploymentService) UpdateConfigProfile(ctx context.Context, actorID, id string, in ConfigProfileInput) (*model.DeploymentConfigProfile, error) {
	p, err := s.store.DeploymentConfigProfileByID(ctx, id)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	if in.Name != "" {
		p.Name = in.Name
	}
	if in.ScopeType != "" {
		if in.ScopeType != model.ConfigScopeShared && in.ScopeType != model.ConfigScopeNode {
			return nil, fmt.Errorf("%w: scope_type must be shared or node", ErrBadRequest)
		}
		p.ScopeType = in.ScopeType
	}
	if in.ScopeID != "" {
		p.ScopeID = in.ScopeID
	}
	// Node-scoped profiles require a scope_id (mirrors CreateConfigProfile).
	if p.ScopeType == model.ConfigScopeNode && strings.TrimSpace(p.ScopeID) == "" {
		return nil, fmt.Errorf("%w: scope_id is required for node scope", ErrBadRequest)
	}
	if in.FeatureID != "" {
		p.FeatureID = in.FeatureID
	}
	if in.ContentYAML != "" {
		var content map[string]any
		if err := yaml.Unmarshal([]byte(in.ContentYAML), &content); err != nil {
			return nil, fmt.Errorf("%w: content_yaml must be valid YAML: %v", ErrBadRequest, err)
		}
		if content == nil {
			content = map[string]any{}
		}
		jsonBytes, err := json.Marshal(content)
		if err != nil {
			return nil, err
		}
		p.ContentJSON = string(jsonBytes)
		p.ContentHash = sha256Hex(jsonBytes)
		p.Version++
	}
	if err := s.store.UpdateDeploymentConfigProfile(ctx, p); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.config.update", ResultFailure, map[string]any{
			"config_hash": p.ContentHash, "action": "deployment.config.update",
		})
		return nil, mapStoreErr(err)
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.config.update", ResultSuccess, map[string]any{
		"config_hash": p.ContentHash, "action": "deployment.config.update", "result": ResultSuccess,
	})
	return p, nil
}

// DeleteConfigProfile removes a config profile.
func (s *DeploymentService) DeleteConfigProfile(ctx context.Context, actorID, id string) error {
	p, err := s.store.DeploymentConfigProfileByID(ctx, id)
	if err != nil {
		return mapStoreErr(err)
	}
	if err := s.store.DeleteDeploymentConfigProfile(ctx, id); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.config.delete", ResultFailure, map[string]any{
			"config_hash": p.ContentHash, "action": "deployment.config.delete",
		})
		return err
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.config.delete", ResultSuccess, map[string]any{
		"config_hash": p.ContentHash, "action": "deployment.config.delete", "result": ResultSuccess,
	})
	return nil
}

// ─── Secret references ───────────────────────────────────────────────────

// ListSecretReferences returns secret references (metadata only, never the
// body).
func (s *DeploymentService) ListSecretReferences(ctx context.Context, featureID, scopeType, scopeID string) ([]*model.DeploymentSecretReference, error) {
	return s.store.ListDeploymentSecretReferences(ctx, featureID, scopeType, scopeID)
}

// CreateSecretReference registers secret reference metadata (no body). The
// object_key follows the fixed whitelisted structure; content_hash starts
// empty until OverwriteSecret writes the first body.
func (s *DeploymentService) CreateSecretReference(ctx context.Context, actorID string, in SecretReferenceInput) (*model.DeploymentSecretReference, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("%w: name is required", ErrBadRequest)
	}
	if strings.TrimSpace(in.FeatureID) == "" {
		return nil, fmt.Errorf("%w: feature_id is required", ErrBadRequest)
	}
	if in.ScopeType != model.SecretScopeShared && in.ScopeType != model.SecretScopeNode {
		return nil, fmt.Errorf("%w: scope_type must be shared or node", ErrBadRequest)
	}
	if in.ScopeType == model.SecretScopeNode && strings.TrimSpace(in.ScopeID) == "" {
		return nil, fmt.Errorf("%w: scope_id is required for node scope", ErrBadRequest)
	}
	feature, err := s.store.DeploymentFeatureByID(ctx, in.FeatureID)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	ref := &model.DeploymentSecretReference{
		ID:             model.NewUUID(),
		Name:           in.Name,
		FeatureID:      in.FeatureID,
		ScopeType:      in.ScopeType,
		ScopeID:        in.ScopeID,
		ObjectKey:      "",
		Version:        0,
		ContentHash:    "",
		EncryptionMode: secretprovider.ModeNone,
	}
	objectKey, err := secretObjectKey(ref, feature.FeatureKey)
	if err != nil {
		return nil, err
	}
	ref.ObjectKey = objectKey
	if err := s.store.CreateDeploymentSecretReference(ctx, ref); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.secret.create", ResultFailure, map[string]any{
			"secret_reference_id": ref.ID, "feature_key": feature.FeatureKey, "action": "deployment.secret.create",
		})
		return nil, mapStoreErr(err)
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.secret.create", ResultSuccess, map[string]any{
		"secret_reference_id": ref.ID, "feature_key": feature.FeatureKey, "action": "deployment.secret.create", "result": ResultSuccess,
	})
	return ref, nil
}

// DeleteSecretReference removes a secret reference and its local material
// file. The OSS object deletion is best-effort: a failure is logged but does
// not block the deletion.
func (s *DeploymentService) DeleteSecretReference(ctx context.Context, actorID, id string) error {
	ref, err := s.store.DeploymentSecretReferenceByID(ctx, id)
	if err != nil {
		return mapStoreErr(err)
	}
	featureKey := ""
	if f, ferr := s.store.DeploymentFeatureByID(ctx, ref.FeatureID); ferr == nil {
		featureKey = f.FeatureKey
	}
	objectKey, err := secretObjectKey(ref, featureKey)
	if err != nil {
		return err
	}
	// Remove the local material file (best effort; the DB row is the source
	// of truth and RepositorySync verifies any leftover file against it).
	rel := strings.TrimPrefix(objectKey, deploymentRepositoryPrefix)
	if err := repo.ValidateRelPath(rel); err == nil {
		localPath := filepath.Join(s.cfg.DeploymentRootDir, repo.RepoDirRepository, filepath.FromSlash(rel))
		if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
			s.log.Warn("deployment: remove local secret material failed", "secret_reference_id", ref.ID, "error", err)
		}
	}
	// Best-effort OSS deletion: failures are logged, never block.
	if p, perr := s.primaryOSSProfile(ctx); perr == nil {
		if client, cerr := s.ossClientFor(p); cerr == nil {
			if derr := client.DeleteObject(ctx, p.Bucket, objectKey); derr != nil {
				s.log.Warn("deployment: delete OSS secret object failed", "secret_reference_id", ref.ID, "error", derr)
			}
		}
	}
	if err := s.store.DeleteDeploymentSecretReference(ctx, id); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.secret.delete", ResultFailure, map[string]any{
			"secret_reference_id": ref.ID, "feature_key": featureKey, "action": "deployment.secret.delete",
		})
		return err
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.secret.delete", ResultSuccess, map[string]any{
		"secret_reference_id": ref.ID, "feature_key": featureKey, "action": "deployment.secret.delete", "result": ResultSuccess,
	})
	return nil
}

// OverwriteSecret is the only path that writes secret material. It writes the
// local 0600 file (atomic temp+rename), uploads the object to the primary OSS
// profile, and bumps the reference metadata. The request body value never
// enters logs, audit events or error messages.
// GetSecretValue returns the current plaintext secret content for a reference.
//
// 策略说明（操作员决定放宽）：需求 V1 原文要求"Secret 内容不得通过 API GET 返回前端、
// UI 只允许覆盖、旧值不回显"。应操作员要求放宽为"可回显可编辑"，本方法仍保留安全底线：
//   - 仅 admin 会话可调用（requireAdmin）；
//   - 响应设置 Cache-Control: no-store，不写日志；
//   - 每次查看落审计 deployment.secret.view（不含内容）；
//   - 只读取控制面本地镜像（DeploymentRootDir/repository/secrets/...），按 ref hash 校验，
//     镜像与 OSS 不一致时报错要求先同步。
func (s *DeploymentService) GetSecretValue(ctx context.Context, id string) (string, error) {
	ref, err := s.store.DeploymentSecretReferenceByID(ctx, id)
	if err != nil {
		return "", mapStoreErr(err)
	}
	featureKey := ""
	if ref.ScopeType == model.SecretScopeNode {
		feature, ferr := s.store.DeploymentFeatureByID(ctx, ref.FeatureID)
		if ferr != nil {
			return "", mapStoreErr(ferr)
		}
		featureKey = feature.FeatureKey
	}
	objectKey, err := secretObjectKey(ref, featureKey)
	if err != nil {
		return "", err
	}
	rel := strings.TrimPrefix(objectKey, deploymentRepositoryPrefix)
	if err := repo.ValidateRelPath(rel); err != nil {
		return "", fmt.Errorf("%w: secret object key invalid", ErrBadRequest)
	}
	localPath := filepath.Join(s.cfg.DeploymentRootDir, repo.RepoDirRepository, filepath.FromSlash(rel))
	data, err := os.ReadFile(localPath)
	if err != nil {
		return "", fmt.Errorf("deployment: secret mirror not available locally (%v); run repository sync or overwrite the secret", err)
	}
	if len(data) > 1<<20 {
		return "", fmt.Errorf("deployment: secret file exceeds the 1MiB limit")
	}
	actualHash := sha256Hex(data)
	if ref.ContentHash != "" && !strings.EqualFold(actualHash, ref.ContentHash) {
		return "", fmt.Errorf("deployment: secret mirror hash mismatch with DB reference; run repository sync or overwrite the secret")
	}
	s.auditDeployment(ctx, model.ActorAdmin, "", "deployment.secret.view", ResultSuccess, map[string]any{
		"secret_reference_id": ref.ID, "feature_key": featureKey, "action": "deployment.secret.view", "result": ResultSuccess,
	})
	return string(data), nil
}

func (s *DeploymentService) OverwriteSecret(ctx context.Context, actorID, id string, in OverwriteSecretInput) (*model.DeploymentSecretReference, error) {
	ref, err := s.store.DeploymentSecretReferenceByID(ctx, id)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	if len(in.Value) > 1<<20 {
		return nil, fmt.Errorf("%w: secret value exceeds the 1MiB limit", ErrBadRequest)
	}
	if isProductionEnvironment(s.envID()) && strings.TrimSpace(in.Reason) == "" {
		return nil, fmt.Errorf("%w: reason is required when overwriting secrets in production", ErrBadRequest)
	}
	featureKey := ""
	if ref.ScopeType == model.SecretScopeNode {
		feature, ferr := s.store.DeploymentFeatureByID(ctx, ref.FeatureID)
		if ferr != nil {
			return nil, mapStoreErr(ferr)
		}
		featureKey = feature.FeatureKey
	}
	objectKey, err := secretObjectKey(ref, featureKey)
	if err != nil {
		return nil, err
	}
	rel := strings.TrimPrefix(objectKey, deploymentRepositoryPrefix)
	if err := repo.ValidateRelPath(rel); err != nil {
		return nil, fmt.Errorf("%w: secret object key invalid", ErrBadRequest)
	}
	localPath := filepath.Join(s.cfg.DeploymentRootDir, repo.RepoDirRepository, filepath.FromSlash(rel))
	// Write the body to a same-directory temp file first; it is only renamed
	// into place after the OSS upload succeeds, so a failed upload never
	// leaves a new secret file on disk (no drift).
	dir := filepath.Dir(localPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("deployment: create secret dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("deployment: chmod secret dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-secret-*")
	if err != nil {
		return nil, fmt.Errorf("deployment: create secret temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("deployment: chmod secret temp: %w", err)
	}
	if _, err := tmp.Write([]byte(in.Value)); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("deployment: write secret temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("deployment: sync secret temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("deployment: close secret temp: %w", err)
	}
	// Upload to the primary OSS profile. Credentials never leave the client.
	p, err := s.primaryOSSProfile(ctx)
	if err != nil {
		return nil, err
	}
	client, err := s.ossClientFor(p)
	if err != nil {
		return nil, err
	}
	if err := client.PutObject(ctx, p.Bucket, objectKey, strings.NewReader(in.Value), int64(len(in.Value)), "application/yaml"); err != nil {
		return nil, fmt.Errorf("deployment: upload secret object: %w", err)
	}
	if err := os.Rename(tmpName, localPath); err != nil {
		return nil, fmt.Errorf("deployment: install secret material: %w", err)
	}
	ref.Version++
	ref.ContentHash = sha256Hex([]byte(in.Value))
	ref.EncryptionMode = secretprovider.ModeNone
	ref.Size = int64(len(in.Value))
	ref.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateDeploymentSecretReference(ctx, ref); err != nil {
		// DB did not accept the new version: remove the freshly installed
		// local file so the repository cannot drift from the reference.
		_ = os.Remove(localPath)
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.secret.overwrite", ResultFailure, map[string]any{
			"secret_reference_id": ref.ID, "secret_version": ref.Version, "secret_hash": ref.ContentHash,
			"encryption_mode": ref.EncryptionMode, "reason_length": len(in.Reason), "action": "deployment.secret.overwrite",
		})
		return nil, mapStoreErr(err)
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.secret.overwrite", ResultSuccess, map[string]any{
		"secret_reference_id": ref.ID, "secret_version": ref.Version, "secret_hash": ref.ContentHash,
		"encryption_mode": ref.EncryptionMode, "reason_length": len(in.Reason), "action": "deployment.secret.overwrite", "result": ResultSuccess,
	})
	return ref, nil
}

// secretObjectKey builds the fixed-structure OSS object key for a secret
// reference. Whitelisted shapes only:
//
//	shared → deployment-repository/secrets/shared/<name>.secrets.yaml
//	node   → deployment-repository/secrets/nodes/<scope_id>/<feature_key>.secrets.yaml
func secretObjectKey(ref *model.DeploymentSecretReference, featureKey string) (string, error) {
	switch ref.ScopeType {
	case model.SecretScopeShared:
		if strings.TrimSpace(ref.Name) == "" {
			return "", fmt.Errorf("%w: secret name is empty", ErrBadRequest)
		}
		return deploymentRepositoryPrefix + repo.DirSecrets + "/shared/" + ref.Name + ".secrets.yaml", nil
	case model.SecretScopeNode:
		if strings.TrimSpace(ref.ScopeID) == "" {
			return "", fmt.Errorf("%w: secret node scope requires scope_id", ErrBadRequest)
		}
		if strings.TrimSpace(featureKey) == "" {
			return "", fmt.Errorf("%w: secret node scope requires a feature", ErrBadRequest)
		}
		return deploymentRepositoryPrefix + repo.DirSecrets + "/nodes/" + ref.ScopeID + "/" + featureKey + ".secrets.yaml", nil
	default:
		return "", fmt.Errorf("%w: invalid secret scope_type %q", ErrBadRequest, ref.ScopeType)
	}
}

// isProductionEnvironment reports whether an environment id denotes
// production (e.g. "production-env" or any id starting with "production").
func isProductionEnvironment(envID string) bool {
	return strings.HasPrefix(strings.ToLower(envID), "production")
}

// ─── Targets ─────────────────────────────────────────────────────────────

// CreateTarget pins a feature to a node.
func (s *DeploymentService) CreateTarget(ctx context.Context, actorID string, in TargetInput) (*model.DeploymentTarget, error) {
	if strings.TrimSpace(in.FeatureID) == "" || strings.TrimSpace(in.NodeID) == "" {
		return nil, fmt.Errorf("%w: feature_id and node_id are required", ErrBadRequest)
	}
	feature, err := s.store.DeploymentFeatureByID(ctx, in.FeatureID)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	if _, err := s.store.NodeByID(ctx, in.NodeID); err != nil {
		return nil, mapStoreErr(err)
	}
	if in.ConfigProfileID != "" {
		if _, err := s.store.DeploymentConfigProfileByID(ctx, in.ConfigProfileID); err != nil {
			return nil, mapStoreErr(err)
		}
	}
	if in.DesiredReleaseID != "" {
		rel, err := s.store.DeploymentReleaseByID(ctx, in.DesiredReleaseID)
		if err != nil {
			return nil, mapStoreErr(err)
		}
		if rel.FeatureID != in.FeatureID {
			return nil, fmt.Errorf("%w: desired release does not belong to the feature", ErrBadRequest)
		}
	}
	t := &model.DeploymentTarget{
		ID:               model.NewUUID(),
		FeatureID:        in.FeatureID,
		NodeID:           in.NodeID,
		ConfigProfileID:  in.ConfigProfileID,
		DesiredReleaseID: in.DesiredReleaseID,
		ActualStatus:     model.TargetStatusPending,
		Enabled:          true,
	}
	if err := s.store.CreateDeploymentTarget(ctx, t); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.target.create", ResultFailure, map[string]any{
			"feature_key": feature.FeatureKey, "node_id": t.NodeID, "target_id": t.ID, "action": "deployment.target.create",
		})
		return nil, mapStoreErr(err)
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.target.create", ResultSuccess, map[string]any{
		"feature_key": feature.FeatureKey, "node_id": t.NodeID, "target_id": t.ID, "action": "deployment.target.create", "result": ResultSuccess,
	})
	return t, nil
}

// ListTargets returns all targets joined with feature key and node name.
func (s *DeploymentService) ListTargets(ctx context.Context) ([]*DeploymentTargetView, error) {
	targets, err := s.store.ListDeploymentTargets(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]*DeploymentTargetView, 0, len(targets))
	for _, t := range targets {
		v := &DeploymentTargetView{DeploymentTarget: t}
		if f, ferr := s.store.DeploymentFeatureByID(ctx, t.FeatureID); ferr == nil {
			v.FeatureKey = f.FeatureKey
		}
		if n, nerr := s.store.NodeByID(ctx, t.NodeID); nerr == nil {
			v.NodeName = n.InstanceName
		}
		views = append(views, v)
	}
	return views, nil
}

// UpdateTarget edits a target with the same validation as CreateTarget.
func (s *DeploymentService) UpdateTarget(ctx context.Context, actorID, id string, in TargetInput) (*model.DeploymentTarget, error) {
	t, err := s.store.DeploymentTargetByID(ctx, id)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	featureKey := ""
	if in.FeatureID != "" {
		feature, ferr := s.store.DeploymentFeatureByID(ctx, in.FeatureID)
		if ferr != nil {
			return nil, mapStoreErr(ferr)
		}
		featureKey = feature.FeatureKey
		t.FeatureID = in.FeatureID
	}
	if in.NodeID != "" {
		if _, nerr := s.store.NodeByID(ctx, in.NodeID); nerr != nil {
			return nil, mapStoreErr(nerr)
		}
		t.NodeID = in.NodeID
	}
	t.ConfigProfileID = in.ConfigProfileID
	if in.ConfigProfileID != "" {
		if _, cerr := s.store.DeploymentConfigProfileByID(ctx, in.ConfigProfileID); cerr != nil {
			return nil, mapStoreErr(cerr)
		}
	}
	if in.DesiredReleaseID != "" {
		rel, rerr := s.store.DeploymentReleaseByID(ctx, in.DesiredReleaseID)
		if rerr != nil {
			return nil, mapStoreErr(rerr)
		}
		if rel.FeatureID != t.FeatureID {
			return nil, fmt.Errorf("%w: desired release does not belong to the feature", ErrBadRequest)
		}
		t.DesiredReleaseID = in.DesiredReleaseID
	}
	if featureKey == "" {
		if f, ferr := s.store.DeploymentFeatureByID(ctx, t.FeatureID); ferr == nil {
			featureKey = f.FeatureKey
		}
	}
	if err := s.store.UpdateDeploymentTarget(ctx, t); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.target.update", ResultFailure, map[string]any{
			"feature_key": featureKey, "node_id": t.NodeID, "target_id": t.ID, "action": "deployment.target.update",
		})
		return nil, err
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.target.update", ResultSuccess, map[string]any{
		"feature_key": featureKey, "node_id": t.NodeID, "target_id": t.ID, "action": "deployment.target.update", "result": ResultSuccess,
	})
	return t, nil
}

// DeleteTarget removes a target.
func (s *DeploymentService) DeleteTarget(ctx context.Context, actorID, id string) error {
	t, err := s.store.DeploymentTargetByID(ctx, id)
	if err != nil {
		return mapStoreErr(err)
	}
	featureKey := ""
	if f, ferr := s.store.DeploymentFeatureByID(ctx, t.FeatureID); ferr == nil {
		featureKey = f.FeatureKey
	}
	if err := s.store.DeleteDeploymentTarget(ctx, id); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.target.delete", ResultFailure, map[string]any{
			"feature_key": featureKey, "node_id": t.NodeID, "target_id": t.ID, "action": "deployment.target.delete",
		})
		return err
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.target.delete", ResultSuccess, map[string]any{
		"feature_key": featureKey, "node_id": t.NodeID, "target_id": t.ID, "action": "deployment.target.delete", "result": ResultSuccess,
	})
	return nil
}

// ─── Config resolver ─────────────────────────────────────────────────────

// ResolveConfig merges configuration for a target: feature schema defaults <
// shared profile < node override < system derived fields. Secret binding is
// NOT injected into the map (references only). It returns the merged map plus
// the SHA-256 of its canonical JSON serialization.
func (s *DeploymentService) ResolveConfig(ctx context.Context, featureID string, target *model.DeploymentTarget) (map[string]any, string, error) {
	feature, err := s.store.DeploymentFeatureByID(ctx, featureID)
	if err != nil {
		return nil, "", mapStoreErr(err)
	}
	merged := map[string]any{}
	if feature.ConfigSchemaJSON != "" && json.Valid([]byte(feature.ConfigSchemaJSON)) {
		var schema map[string]any
		if err := json.Unmarshal([]byte(feature.ConfigSchemaJSON), &schema); err == nil {
			if defaults, ok := schema["defaults"].(map[string]any); ok {
				mergeConfigMaps(merged, defaults)
			}
		}
	}
	// Static configuration is read from the synced repository (authoritative
	// source on both the control plane and the nodes) using exactly the same
	// file names as the node runner. The frozen config hash covers ONLY the
	// static merged config (feature defaults are release-bundle-scoped and
	// derived fields are machine-specific), so the control-plane freeze and
	// the node-side render always agree on the same bytes.
	layout := repo.New(s.cfg.DeploymentRootDir)
	shared, err := s.loadDeploymentConfigYAML(
		filepath.Join(layout.RepoDir(), repo.DirConfigs, "shared", feature.FeatureKey+".yaml"),
		filepath.Join(layout.RepoDir(), repo.DirConfigs, "shared", feature.FeatureKey+"-shared.yaml"),
	)
	if err != nil {
		return nil, "", err
	}
	mergeConfigMaps(merged, shared)
	var static = map[string]any{}
	mergeConfigMaps(static, shared)
	if target != nil {
		nodeCfg, nerr := s.loadDeploymentConfigYAML(
			filepath.Join(layout.RepoDir(), repo.DirConfigs, "nodes", target.NodeID, feature.FeatureKey+".yaml"),
			filepath.Join(layout.RepoDir(), repo.DirConfigs, "nodes", target.NodeID, feature.FeatureKey+"-node.yaml"),
		)
		if nerr != nil {
			return nil, "", nerr
		}
		mergeConfigMaps(merged, nodeCfg)
		mergeConfigMaps(static, nodeCfg)
	}
	rawStatic, err := json.Marshal(static)
	if err != nil {
		return nil, "", err
	}
	configHash := sha256Hex(rawStatic)
	// System derived fields (highest priority) are applied to the returned
	// map but are deliberately excluded from the frozen config hash.
	hostname, _ := os.Hostname()
	merged["environment_id"] = s.envID()
	merged["hostname"] = hostname
	merged["feature_key"] = feature.FeatureKey
	merged["deployment_root_dir"] = s.cfg.DeploymentRootDir
	merged["operation_id"] = ""
	merged["data_directory"] = filepath.Join(s.cfg.DeploymentRootDir, repo.LocalDirLocal, "runtime", feature.FeatureKey)
	if target != nil {
		merged["node_id"] = target.NodeID
		if addrs, aerr := s.store.NodeAddresses(ctx, target.NodeID); aerr == nil {
			for _, a := range addrs {
				if a.IsPreferred {
					merged["node_address"] = a.Address
					break
				}
			}
			if _, ok := merged["node_address"]; !ok && len(addrs) > 0 {
				merged["node_address"] = addrs[0].Address
			}
		}
		if target.DesiredReleaseID != "" {
			if rel, rerr := s.store.DeploymentReleaseByID(ctx, target.DesiredReleaseID); rerr == nil {
				merged["release_version"] = rel.Version
			}
		}
	}
	return merged, configHash, nil
}

// loadDeploymentConfigYAML parses the first existing YAML config file among
// candidates into a map (mirrors the node runner's loadConfigYAML so the
// control-plane freeze and node render read identical bytes). Missing files
// are ignored; read/parse errors fail closed.
func (s *DeploymentService) loadDeploymentConfigYAML(candidates ...string) (map[string]any, error) {
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read deployment config %s: %w", p, err)
		}
		var m map[string]any
		if err := yaml.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parse deployment config %s: %w", p, err)
		}
		return m, nil
	}
	return map[string]any{}, nil
}

// ─── Bootstrap sessions ──────────────────────────────────────────────────

// CreateBootstrapSession creates a bootstrap session with a one-time token
// (only its SHA-256 is stored) and returns the one-line operator command.
func (s *DeploymentService) CreateBootstrapSession(ctx context.Context, actorID string, in BootstrapSessionInput) (*BootstrapSessionResult, error) {
	if strings.TrimSpace(in.NodeID) == "" {
		return nil, fmt.Errorf("%w: node_id is required", ErrBadRequest)
	}
	if _, err := s.store.NodeByID(ctx, in.NodeID); err != nil {
		return nil, mapStoreErr(err)
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("deployment: token generation: %w", err)
	}
	token := "bst_" + hex.EncodeToString(raw)
	now := time.Now().UTC()
	sess := &model.BootstrapSession{
		ID:        model.NewUUID(),
		NodeID:    in.NodeID,
		Status:    model.BootstrapStatusCreated,
		TokenHash: sha256Hex([]byte(token)),
		Bucket:    in.Bucket,
		Prefix:    in.Prefix,
		Region:    in.Region,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := s.store.CreateBootstrapSession(ctx, sess); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.bootstrap.create", ResultFailure, map[string]any{
			"node_id": in.NodeID, "action": "deployment.bootstrap.create",
		})
		return nil, err
	}
	// Pin the bootstrap script by SHA-256 so a tampered script (or one
	// swapped mid-download) aborts before execution. The script is read from
	// the control plane working directory (scripts/deployment-bootstrap.sh).
	scriptData, err := os.ReadFile(filepath.Join("scripts", "deployment-bootstrap.sh"))
	if err != nil {
		return nil, fmt.Errorf("deployment: read bootstrap script for pinning: %w", err)
	}
	scriptHash := sha256Hex(scriptData)
	rnd := make([]byte, 4)
	if _, err := rand.Read(rnd); err != nil {
		return nil, fmt.Errorf("deployment: temp name generation: %w", err)
	}
	tmpName := fmt.Sprintf("/tmp/servercli-bootstrap-%s.sh", hex.EncodeToString(rnd))
	baseURL := strings.TrimRight(s.cfg.PrimaryBackendURL, "/")
	command := fmt.Sprintf("curl -fsSL %s/deployment-bootstrap.sh -o %s && printf '%s  %s\n' | sha256sum -c - && sudo bash %s --session-id %s --token %s --control-plane-url %s",
		baseURL, tmpName, scriptHash, tmpName, tmpName, sess.ID, token, baseURL)
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.bootstrap.create", ResultSuccess, map[string]any{
		"node_id": sess.NodeID, "action": "deployment.bootstrap.create", "result": ResultSuccess,
	})
	return &BootstrapSessionResult{Session: sess, Token: token, Command: command}, nil
}

// ListBootstrapSessions returns all bootstrap sessions.
func (s *DeploymentService) ListBootstrapSessions(ctx context.Context) ([]*model.BootstrapSession, error) {
	return s.store.ListBootstrapSessions(ctx)
}

// RevokeBootstrapSession cancels a bootstrap session.
func (s *DeploymentService) RevokeBootstrapSession(ctx context.Context, actorID, id string) error {
	sess, err := s.store.BootstrapSessionByID(ctx, id)
	if err != nil {
		return mapStoreErr(err)
	}
	if err := s.store.RevokeBootstrapSession(ctx, id); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.bootstrap.revoke", ResultFailure, map[string]any{
			"node_id": sess.NodeID, "action": "deployment.bootstrap.revoke",
		})
		return err
	}
	s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.bootstrap.revoke", ResultSuccess, map[string]any{
		"node_id": sess.NodeID, "action": "deployment.bootstrap.revoke", "result": ResultSuccess,
	})
	return nil
}

// ─── Agent upload authorization ──────────────────────────────────────────

// AgentUploadAuthorize returns a scoped upload authorization for a node
// agent: the primary OSS endpoint/bucket plus the precise per-operation
// backups/ prefix. nodeID must own the operation target; the operation must
// exist.
func (s *DeploymentService) AgentUploadAuthorize(ctx context.Context, nodeID string, in AgentUploadAuthorizeInput) (*AgentUploadAuthorization, error) {
	if strings.TrimSpace(in.OperationID) == "" || strings.TrimSpace(in.TargetID) == "" {
		return nil, fmt.Errorf("%w: operation_id and target_id are required", ErrBadRequest)
	}
	if strings.TrimSpace(in.FeatureKey) == "" || strings.ContainsAny(in.FeatureKey, "/\\") {
		return nil, fmt.Errorf("%w: invalid feature_key", ErrBadRequest)
	}
	opt, err := s.store.DeploymentOperationTargetByID(ctx, in.TargetID)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	if opt.NodeID != nodeID {
		return nil, ErrForbidden
	}
	if opt.OperationID != in.OperationID {
		return nil, ErrForbidden
	}
	if _, err := s.store.DeploymentOperationByID(ctx, in.OperationID); err != nil {
		return nil, mapStoreErr(err)
	}
	p, err := s.primaryOSSProfile(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	prefix := fmt.Sprintf("backups/%s/%s/%s/%04d/%02d/%02d/%s/",
		s.envID(), in.FeatureKey, nodeID, now.Year(), int(now.Month()), now.Day(), in.OperationID)
	auth := &AgentUploadAuthorization{
		Endpoint:        p.Endpoint,
		Bucket:          p.Bucket,
		Prefix:          prefix,
		CredentialsType: "v1-oss-profile",
	}
	s.auditDeployment(ctx, model.ActorNode, nodeID, "deployment.upload-authorize", ResultSuccess, map[string]any{
		"operation_id": in.OperationID, "target_id": in.TargetID, "node_id": nodeID,
		"action": "deployment.upload-authorize", "result": ResultSuccess,
	})
	return auth, nil
}
