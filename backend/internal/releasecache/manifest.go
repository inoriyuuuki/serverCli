// Package releasecache mirrors trusted GitHub release artifacts into OSS.
package releasecache

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const CacheManifestSchemaVersion = "1.0"

// CacheManifest is the normalized release-cache manifest stored in OSS.
type CacheManifest struct {
	SchemaVersion    string           `json:"schema_version"`
	Version          string           `json:"version"`
	CreatedAt        time.Time        `json:"created_at"`
	SourceRepository string           `json:"source_repository"`
	SourceRelease    string           `json:"source_release"`
	OS               string           `json:"os"`
	Arch             string           `json:"arch"`
	ModulesVersion   string           `json:"modules_version,omitempty"`
	SchemaCompat     SchemaCompatInfo `json:"schema_compat,omitempty"`
	Artifacts        []CacheArtifact  `json:"artifacts"`
	Status           string           `json:"status"`
	UploadedAt       time.Time        `json:"uploaded_at"`
	VerifiedAt       time.Time        `json:"verified_at"`
}

// CacheArtifact describes one release asset mirrored to OSS.
type CacheArtifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// SchemaCompatInfo is the schema compatibility range associated with a release.
type SchemaCompatInfo struct {
	Min string `json:"min,omitempty"`
	Max string `json:"max,omitempty"`
}

// BuildManifest constructs a normalized pending manifest. ApplySync changes the
// status and verification timestamps only after every artifact is verified.
func BuildManifest(version, repo, release, osName, arch, modulesVersion string, artifacts []CacheArtifact, schema SchemaCompatInfo) *CacheManifest {
	now := time.Now().UTC().Truncate(time.Second)
	return &CacheManifest{
		SchemaVersion:    CacheManifestSchemaVersion,
		Version:          strings.TrimSpace(version),
		CreatedAt:        now,
		SourceRepository: strings.TrimSpace(repo),
		SourceRelease:    strings.TrimSpace(release),
		OS:               strings.TrimSpace(osName),
		Arch:             strings.TrimSpace(arch),
		ModulesVersion:   strings.TrimSpace(modulesVersion),
		SchemaCompat:     schema,
		Artifacts:        normalizeArtifacts(artifacts),
		Status:           "pending",
	}
}

// Serialize emits deterministic compact JSON. Struct field order is fixed and
// artifacts are sorted by name, so serializing equivalent manifests is stable.
func Serialize(manifest *CacheManifest) ([]byte, error) {
	if manifest == nil {
		return nil, errors.New("releasecache: manifest is nil")
	}
	canonical := *manifest
	canonical.CreatedAt = canonical.CreatedAt.UTC()
	canonical.UploadedAt = canonical.UploadedAt.UTC()
	canonical.VerifiedAt = canonical.VerifiedAt.UTC()
	canonical.Artifacts = normalizeArtifacts(manifest.Artifacts)
	if err := validateManifest(&canonical); err != nil {
		return nil, err
	}

	artifacts := make([]map[string]any, 0, len(canonical.Artifacts))
	for _, artifact := range canonical.Artifacts {
		artifacts = append(artifacts, map[string]any{
			"name": artifact.Name, "sha256": artifact.SHA256, "size": artifact.Size,
		})
	}
	compat := map[string]any{}
	if canonical.SchemaCompat.Min != "" {
		compat["min"] = canonical.SchemaCompat.Min
	}
	if canonical.SchemaCompat.Max != "" {
		compat["max"] = canonical.SchemaCompat.Max
	}
	value := map[string]any{
		"schema_version": canonical.SchemaVersion, "version": canonical.Version,
		"created_at": canonical.CreatedAt, "source_repository": canonical.SourceRepository,
		"source_release": canonical.SourceRelease, "os": canonical.OS, "arch": canonical.Arch,
		"schema_compat": compat, "artifacts": artifacts, "status": canonical.Status,
		"uploaded_at": canonical.UploadedAt, "verified_at": canonical.VerifiedAt,
	}
	if canonical.ModulesVersion != "" {
		value["modules_version"] = canonical.ModulesVersion
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, fmt.Errorf("releasecache: serialize manifest: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Parse decodes and validates a normalized cache manifest.
func Parse(data []byte) (*CacheManifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var manifest CacheManifest
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("releasecache: parse manifest: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("releasecache: trailing JSON data")
		}
		return nil, fmt.Errorf("releasecache: trailing JSON data: %w", err)
	}
	manifest.Artifacts = normalizeArtifacts(manifest.Artifacts)
	if err := validateManifest(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func normalizeArtifacts(artifacts []CacheArtifact) []CacheArtifact {
	out := append([]CacheArtifact(nil), artifacts...)
	for i := range out {
		out[i].Name = strings.TrimSpace(out[i].Name)
		out[i].SHA256 = strings.ToLower(strings.TrimSpace(out[i].SHA256))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func validateManifest(manifest *CacheManifest) error {
	if strings.TrimSpace(manifest.SchemaVersion) == "" {
		return errors.New("releasecache: schema_version is required")
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return errors.New("releasecache: version is required")
	}
	if manifest.CreatedAt.IsZero() {
		return errors.New("releasecache: created_at is required")
	}
	if strings.TrimSpace(manifest.Status) == "" {
		return errors.New("releasecache: status is required")
	}
	seen := make(map[string]struct{}, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if artifact.Name == "" {
			return errors.New("releasecache: artifact name is required")
		}
		if artifact.Size < 0 {
			return fmt.Errorf("releasecache: artifact %q has negative size", artifact.Name)
		}
		if err := validateSHA256(artifact.SHA256); err != nil {
			return fmt.Errorf("releasecache: artifact %q: %w", artifact.Name, err)
		}
		if _, ok := seen[artifact.Name]; ok {
			return fmt.Errorf("releasecache: duplicate artifact %q", artifact.Name)
		}
		seen[artifact.Name] = struct{}{}
	}
	return nil
}

func validateSHA256(digest string) error {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return errors.New("sha256 is required")
	}
	if len(digest) != 64 {
		return errors.New("sha256 must contain 64 hexadecimal characters")
	}
	// Reuse the OSS verifier's strict hexadecimal comparison without accepting
	// malformed input: a zero buffer can only match a valid 64-char digest.
	for _, r := range digest {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return errors.New("sha256 must be hexadecimal")
		}
	}
	return nil
}
