package releasecache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"time"

	"servercli/internal/oss"
)

const (
	defaultOSSBaseKey = "servercli/releases"
	manifestFileName  = "release-manifest.json"
	shaSumsFileName   = "sha256sums.txt"
)

// GitHubReleaseClient is the narrow GitHub API used by release synchronization.
type GitHubReleaseClient interface {
	ListArtifacts(ctx context.Context, owner, repo, tag string) ([]ReleaseArtifactMeta, error)
	DownloadArtifact(ctx context.Context, owner, repo, tag, name string) ([]byte, error)
}

// ReleaseArtifactMeta is authoritative metadata discovered from GitHub.
type ReleaseArtifactMeta struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// SyncOptions configures planning and applying a release mirror operation.
type SyncOptions struct {
	Version        string
	Owner          string
	Repo           string
	Tag            string
	OSS            oss.Provider
	GitHub         GitHubReleaseClient
	OSSBaseKey     string
	ModulesVersion string
	Schema         SchemaCompatInfo
	Log            *slog.Logger
	Timeout        time.Duration
	Force          bool
	OS             string
	Arch           string
}

// PlannedArtifact binds GitHub metadata to its destination OSS key.
type PlannedArtifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	OSSKey string `json:"oss_key"`
}

// SyncPlan is the deterministic set of objects produced by PlanSync.
type SyncPlan struct {
	Version          string            `json:"version"`
	SourceRepository string            `json:"source_repository"`
	SourceRelease    string            `json:"source_release"`
	Artifacts        []PlannedArtifact `json:"artifacts"`
	ManifestOSSKey   string            `json:"manifest_oss_key"`
	SHA256SumsOSSKey string            `json:"sha256sums_oss_key"`
}

// SyncResult reports a completely verified sync. Additional source fields are
// retained so RecordEntries can persist complete release-cache rows.
type SyncResult struct {
	Version          string             `json:"version"`
	Uploaded         []ArtifactUploaded `json:"uploaded"`
	ManifestSHA256   string             `json:"manifest_sha256"`
	Verified         bool               `json:"verified"`
	AlreadyUploaded  bool               `json:"already_uploaded,omitempty"`
	SourceRepository string             `json:"source_repository,omitempty"`
	SourceRelease    string             `json:"source_release,omitempty"`
	OS               string             `json:"os,omitempty"`
	Arch             string             `json:"arch,omitempty"`
	ModulesVersion   string             `json:"modules_version,omitempty"`
	UploadedAt       time.Time          `json:"uploaded_at,omitempty"`
	VerifiedAt       time.Time          `json:"verified_at,omitempty"`
}

// ArtifactUploaded describes one mirrored GitHub release artifact.
type ArtifactUploaded struct {
	Name   string `json:"name"`
	OSSKey string `json:"oss_key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// PlanSync discovers GitHub artifacts and computes their OSS object keys.
func PlanSync(ctx context.Context, opts SyncOptions) (*SyncPlan, error) {
	if opts.GitHub == nil {
		return nil, errors.New("releasecache: GitHub client is required")
	}
	owner := strings.TrimSpace(opts.Owner)
	repo := strings.TrimSpace(opts.Repo)
	tag := strings.TrimSpace(opts.Tag)
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = tag
	}
	if owner == "" || repo == "" || tag == "" {
		return nil, errors.New("releasecache: owner, repo, and tag are required")
	}
	if err := validatePathSegment(version, "version"); err != nil {
		return nil, err
	}

	ctx, cancel := withTimeout(ctx, opts.Timeout)
	defer cancel()
	metas, err := opts.GitHub.ListArtifacts(ctx, owner, repo, tag)
	if err != nil {
		return nil, fmt.Errorf("releasecache: list GitHub release artifacts: %w", err)
	}
	if len(metas) == 0 {
		return nil, errors.New("releasecache: GitHub release contains no artifacts")
	}

	base := normalizedBaseKey(opts.OSSBaseKey)
	planned := make([]PlannedArtifact, 0, len(metas))
	seen := make(map[string]struct{}, len(metas))
	for _, meta := range metas {
		name := strings.TrimSpace(meta.Name)
		if name == manifestFileName || name == shaSumsFileName {
			continue
		}
		if err := validateArtifactName(name); err != nil {
			return nil, err
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("releasecache: duplicate GitHub artifact %q", name)
		}
		seen[name] = struct{}{}
		if meta.Size < 0 {
			return nil, fmt.Errorf("releasecache: artifact %q has negative size", name)
		}
		if err := validateSHA256(meta.SHA256); err != nil {
			return nil, fmt.Errorf("releasecache: artifact %q: %w", name, err)
		}
		planned = append(planned, PlannedArtifact{
			Name:   name,
			Size:   meta.Size,
			SHA256: strings.ToLower(strings.TrimSpace(meta.SHA256)),
			OSSKey: objectKey(base, version, name),
		})
	}
	if len(planned) == 0 {
		return nil, errors.New("releasecache: GitHub release contains no mirrorable artifacts")
	}
	sort.Slice(planned, func(i, j int) bool { return planned[i].Name < planned[j].Name })

	return &SyncPlan{
		Version:          version,
		SourceRepository: owner + "/" + repo,
		SourceRelease:    tag,
		Artifacts:        planned,
		ManifestOSSKey:   objectKey(base, version, manifestFileName),
		SHA256SumsOSSKey: objectKey(base, version, shaSumsFileName),
	}, nil
}

// ApplySync downloads and validates all artifacts before performing any upload.
// The manifest is uploaded last and therefore acts as the availability marker.
func ApplySync(ctx context.Context, opts SyncOptions) (*SyncResult, error) {
	if opts.OSS == nil {
		return nil, errors.New("releasecache: OSS provider is required")
	}
	plan, err := PlanSync(ctx, opts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := withTimeout(ctx, opts.Timeout)
	defer cancel()

	if !opts.Force {
		result, ok, err := verifiedExistingSync(ctx, opts, plan)
		if err != nil {
			return nil, err
		}
		if ok {
			return result, nil
		}
	}

	type downloadedArtifact struct {
		plan PlannedArtifact
		data []byte
	}
	downloaded := make([]downloadedArtifact, 0, len(plan.Artifacts))
	for _, artifact := range plan.Artifacts {
		data, err := opts.GitHub.DownloadArtifact(ctx, strings.TrimSpace(opts.Owner), strings.TrimSpace(opts.Repo), strings.TrimSpace(opts.Tag), artifact.Name)
		if err != nil {
			return nil, fmt.Errorf("releasecache: download GitHub artifact %q: %w", artifact.Name, err)
		}
		if artifact.Size > 0 && int64(len(data)) != artifact.Size {
			return nil, fmt.Errorf("releasecache: artifact %q size mismatch: expected %d, got %d", artifact.Name, artifact.Size, len(data))
		}
		if err := oss.VerifySHA256(data, artifact.SHA256); err != nil {
			return nil, fmt.Errorf("releasecache: verify GitHub artifact %q: %w", artifact.Name, err)
		}
		downloaded = append(downloaded, downloadedArtifact{plan: artifact, data: data})
	}

	uploaded := make([]ArtifactUploaded, 0, len(downloaded))
	cacheArtifacts := make([]CacheArtifact, 0, len(downloaded))
	for _, artifact := range downloaded {
		digest, err := opts.OSS.PutVerified(ctx, artifact.plan.OSSKey, artifact.data, contentTypeFor(artifact.plan.Name))
		if err != nil {
			return nil, fmt.Errorf("releasecache: upload and verify %q: %w", artifact.plan.Name, err)
		}
		if !strings.EqualFold(digest, artifact.plan.SHA256) {
			return nil, fmt.Errorf("releasecache: OSS returned unexpected sha256 for %q", artifact.plan.Name)
		}
		uploaded = append(uploaded, ArtifactUploaded{
			Name: artifact.plan.Name, OSSKey: artifact.plan.OSSKey,
			Size: int64(len(artifact.data)), SHA256: artifact.plan.SHA256,
		})
		cacheArtifacts = append(cacheArtifacts, CacheArtifact{
			Name: artifact.plan.Name, Size: int64(len(artifact.data)), SHA256: artifact.plan.SHA256,
		})
	}

	sumsData := buildSHA256Sums(cacheArtifacts)
	sumsDigest, err := opts.OSS.PutVerified(ctx, plan.SHA256SumsOSSKey, sumsData, "text/plain; charset=utf-8")
	if err != nil {
		return nil, fmt.Errorf("releasecache: upload and verify sha256sums.txt: %w", err)
	}
	if err := oss.VerifySHA256(sumsData, sumsDigest); err != nil {
		return nil, fmt.Errorf("releasecache: verify returned sha256sums digest: %w", err)
	}

	manifest := BuildManifest(plan.Version, plan.SourceRepository, plan.SourceRelease, opts.OS, opts.Arch, opts.ModulesVersion, cacheArtifacts, opts.Schema)
	now := time.Now().UTC().Truncate(time.Second)
	manifest.Status = "available"
	manifest.UploadedAt = now
	manifest.VerifiedAt = now
	manifestData, err := Serialize(manifest)
	if err != nil {
		return nil, err
	}
	manifestSHA := oss.SHA256Hex(manifestData)
	returnedSHA, err := opts.OSS.PutVerified(ctx, plan.ManifestOSSKey, manifestData, "application/json")
	if err != nil {
		return nil, fmt.Errorf("releasecache: upload and verify manifest: %w", err)
	}
	if !strings.EqualFold(returnedSHA, manifestSHA) {
		return nil, errors.New("releasecache: OSS returned unexpected manifest sha256")
	}

	if opts.Log != nil {
		opts.Log.Info("release sync completed", "version", plan.Version, "artifacts", len(uploaded))
	}
	return &SyncResult{
		Version: plan.Version, Uploaded: uploaded, ManifestSHA256: manifestSHA, Verified: true,
		SourceRepository: plan.SourceRepository, SourceRelease: plan.SourceRelease,
		OS: opts.OS, Arch: opts.Arch, ModulesVersion: opts.ModulesVersion,
		UploadedAt: now, VerifiedAt: now,
	}, nil
}

func verifiedExistingSync(ctx context.Context, opts SyncOptions, plan *SyncPlan) (*SyncResult, bool, error) {
	data, err := opts.OSS.Get(ctx, plan.ManifestOSSKey)
	if errors.Is(err, oss.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("releasecache: read existing manifest: %w", err)
	}
	manifest, err := Parse(data)
	if err != nil || manifest.Version != plan.Version || manifest.Status != "available" {
		if opts.Log != nil {
			opts.Log.Warn("existing release manifest is not verified; replacing", "version", plan.Version)
		}
		return nil, false, nil
	}

	byName := make(map[string]PlannedArtifact, len(plan.Artifacts))
	for _, artifact := range plan.Artifacts {
		byName[artifact.Name] = artifact
	}
	if len(manifest.Artifacts) != len(plan.Artifacts) {
		return nil, false, nil
	}
	uploaded := make([]ArtifactUploaded, 0, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		planned, ok := byName[artifact.Name]
		if !ok || artifact.Size != planned.Size || !strings.EqualFold(artifact.SHA256, planned.SHA256) {
			return nil, false, nil
		}
		if _, err := opts.OSS.GetVerified(ctx, planned.OSSKey, artifact.SHA256); err != nil {
			if errors.Is(err, oss.ErrNotFound) {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("releasecache: verify existing artifact %q: %w", artifact.Name, err)
		}
		uploaded = append(uploaded, ArtifactUploaded{Name: artifact.Name, OSSKey: planned.OSSKey, Size: artifact.Size, SHA256: artifact.SHA256})
	}
	expectedSums := buildSHA256Sums(manifest.Artifacts)
	actualSums, err := opts.OSS.Get(ctx, plan.SHA256SumsOSSKey)
	if errors.Is(err, oss.ErrNotFound) || err == nil && string(actualSums) != string(expectedSums) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("releasecache: read existing sha256sums.txt: %w", err)
	}
	return &SyncResult{
		Version: plan.Version, Uploaded: uploaded, ManifestSHA256: oss.SHA256Hex(data), Verified: true,
		AlreadyUploaded: true, SourceRepository: manifest.SourceRepository, SourceRelease: manifest.SourceRelease,
		OS: manifest.OS, Arch: manifest.Arch, ModulesVersion: manifest.ModulesVersion,
		UploadedAt: manifest.UploadedAt, VerifiedAt: manifest.VerifiedAt,
	}, true, nil
}

func buildSHA256Sums(artifacts []CacheArtifact) []byte {
	items := normalizeArtifacts(artifacts)
	var b strings.Builder
	for _, artifact := range items {
		fmt.Fprintf(&b, "%s  %s\n", strings.ToLower(artifact.SHA256), artifact.Name)
	}
	return []byte(b.String())
}

func normalizedBaseKey(base string) string {
	base = strings.Trim(strings.TrimSpace(base), "/")
	if base == "" {
		return defaultOSSBaseKey
	}
	return base
}

func objectKey(base, version, name string) string {
	return strings.Trim(base, "/") + "/" + version + "/" + name
}

func validatePathSegment(value, label string) error {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\") {
		return fmt.Errorf("releasecache: invalid %s %q", label, value)
	}
	return nil
}

func validateArtifactName(name string) error {
	if err := validatePathSegment(name, "artifact name"); err != nil {
		return err
	}
	if path.Base(name) != name {
		return fmt.Errorf("releasecache: invalid artifact name %q", name)
	}
	return nil
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return context.WithTimeout(ctx, timeout)
}

func contentTypeFor(name string) string {
	switch {
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	case strings.HasSuffix(name, ".txt"), strings.HasSuffix(name, ".sha256"):
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(name, ".gz"):
		return "application/gzip"
	default:
		return "application/octet-stream"
	}
}

// ValidateCredentialFreeArgs rejects credential-bearing command-line flags.
// Credential values must be supplied through the environment or a 0600 file.
func ValidateCredentialFreeArgs(args []string) error {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name := strings.ToLower(strings.SplitN(arg, "=", 2)[0])
		if strings.Contains(name, "secret") || strings.Contains(name, "password") || strings.Contains(name, "token") ||
			strings.Contains(name, "access-key-id") || strings.Contains(name, "access-key-secret") {
			return fmt.Errorf("releasecache: credential flag %q is forbidden; use environment variables or a 0600 file", name)
		}
	}
	return nil
}
