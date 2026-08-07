package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// CommandEntry is a validated local command.
type CommandEntry struct {
	CommandID           string `json:"command_id"`
	CommandVersion      string `json:"command_version"`
	CapabilityID        string `json:"capability_id"`
	Category            string `json:"category"`
	Title               string `json:"title"`
	Description         string `json:"description"`
	ParameterSchemaJSON string `json:"parameter_schema_json"`
	PermissionProfile   string `json:"permission_profile"`
	TimeoutSeconds      int    `json:"timeout_seconds"`
	MaxOutputBytes      int64  `json:"max_output_bytes"`
	Enabled             bool   `json:"enabled"`
	ManifestHash        string `json:"manifest_hash"`
	ExecutableHash      string `json:"executable_hash"`
	ExecutablePath      string `json:"-"`
	Concurrency         int    `json:"-"`
}

// manifest mirrors the contract's command manifest YAML.
type manifest struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		ID          string `yaml:"id"`
		Version     string `yaml:"version"`
		Category    string `yaml:"category"`
		Title       string `yaml:"title"`
		Description string `yaml:"description"`
	} `yaml:"metadata"`
	Spec struct {
		Executable        string `yaml:"executable"`
		PermissionProfile string `yaml:"permissionProfile"`
		TimeoutSeconds    int    `yaml:"timeoutSeconds"`
		MaxOutputBytes    int64  `yaml:"maxOutputBytes"`
		Concurrency       int    `yaml:"concurrency"`
		Parameters        any    `yaml:"parameters"`
	} `yaml:"spec"`
}

// LoadCommands scans dir for command manifests and validates them.
func LoadCommands(dir string, log *slog.Logger) ([]CommandEntry, error) {
	var out []CommandEntry
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}
		entry, perr := loadManifest(dir, path)
		if perr != nil {
			log.Warn("skipping invalid command manifest", "path", path, "error", perr)
			return nil
		}
		out = append(out, *entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CommandID < out[j].CommandID })
	return out, nil
}

func loadManifest(root, path string) (*CommandEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("yaml parse: %w", err)
	}
	if m.APIVersion != "servercli/v1" || m.Kind != "Command" {
		return nil, fmt.Errorf("unsupported manifest kind %q", m.Kind)
	}
	if m.Metadata.ID == "" || m.Metadata.Version == "" {
		return nil, fmt.Errorf("command id and version required")
	}
	exe := m.Spec.Executable
	if !filepath.IsAbs(exe) {
		// Relative paths are resolved against COMMANDS_DIR first, then the
		// manifest's own directory (for nested command trees).
		candidates := []string{filepath.Join(root, exe), filepath.Join(filepath.Dir(path), exe)}
		for _, cand := range candidates {
			if _, statErr := os.Stat(cand); statErr == nil {
				exe = cand
				break
			}
		}
	}
	info, err := os.Stat(exe)
	if err != nil {
		return nil, fmt.Errorf("executable not found: %w", err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("executable not executable: %s", exe)
	}
	schemaJSON := "{}"
	if m.Spec.Parameters != nil {
		raw, err := json.Marshal(m.Spec.Parameters)
		if err != nil {
			return nil, fmt.Errorf("parameters not JSON-serializable: %w", err)
		}
		schemaJSON = string(raw)
	}
	profile := m.Spec.PermissionProfile
	if profile == "" {
		profile = "read-only"
	}
	timeout := m.Spec.TimeoutSeconds
	if timeout <= 0 {
		timeout = 60
	}
	maxOut := m.Spec.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = 262144
	}
	concurrency := m.Spec.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	manifestHash := sha256.Sum256(data)
	exeHash, err := hashFile(exe)
	if err != nil {
		return nil, err
	}
	return &CommandEntry{
		CommandID:           m.Metadata.ID,
		CommandVersion:      m.Metadata.Version,
		Category:            m.Metadata.Category,
		Title:               m.Metadata.Title,
		Description:         m.Metadata.Description,
		ParameterSchemaJSON: schemaJSON,
		PermissionProfile:   profile,
		TimeoutSeconds:      timeout,
		MaxOutputBytes:      maxOut,
		Enabled:             true,
		ManifestHash:        hex.EncodeToString(manifestHash[:]),
		ExecutableHash:      exeHash,
		ExecutablePath:      exe,
		Concurrency:         concurrency,
	}, nil
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// SnapshotPayload converts command entries to the API snapshot shape.
func SnapshotPayload(entries []CommandEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"command_id":            e.CommandID,
			"command_version":       e.CommandVersion,
			"capability_id":         e.CapabilityID,
			"category":              e.Category,
			"title":                 e.Title,
			"description":           e.Description,
			"parameter_schema_json": e.ParameterSchemaJSON,
			"permission_profile":    e.PermissionProfile,
			"timeout_seconds":       e.TimeoutSeconds,
			"max_output_bytes":      e.MaxOutputBytes,
			"enabled":               e.Enabled,
			"manifest_hash":         e.ManifestHash,
			"executable_hash":       e.ExecutableHash,
		})
	}
	return out
}
