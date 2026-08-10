// Package modman implements the module manifest (module.yaml), the dependency
// graph, and the restricted Provision Runner that executes fixed operation
// entrypoints.
//
// Modules declare their config/secret schema in module.yaml (the authoritative
// source), a fixed set of operations, health checks and backup/restore hooks.
// The runner only ever executes <module>/operations/<op> where op is from a
// fixed whitelist, never arbitrary paths or commands.
package modman

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Phases, in dependency order.
const (
	PhaseFoundationCore     = "foundation-core"
	PhaseFoundationServices = "foundation-services"
	PhaseServices           = "services"
)

// Fixed operation whitelist. The runner refuses anything else.
var AllowedOperations = map[string]bool{
	"install":   true,
	"uninstall": true,
	"verify":    true,
	"backup":    true,
	"restore":   true,
	"adopt":     true,
	"preflight": true,
	"plan":      true,
}

// Delivery modes.
const (
	DeliveryEnv  = "env"  // single-line values via environment variables
	DeliveryFile = "file" // multi-line/binary via 0600 temp file under /run
)

// Field is a config or secret field declared by a module.
type Field struct {
	Name      string `yaml:"name"`
	Type      string `yaml:"type"` // string|int|bool|file
	Required  bool   `yaml:"required"`
	Sensitive bool   `yaml:"sensitive"`
}

// Operation is a fixed entrypoint declared by a module.
type Operation struct {
	Entry      string   `yaml:"entry"` // relative to module dir, e.g. operations/install.sh
	TimeoutSec int      `yaml:"timeout_seconds"`
	Requires   []string `yaml:"requires,omitempty"` // prerequisite commit points
	Root       bool     `yaml:"root"`               // requires root Provision Runner
}

// HealthCheck describes a verification entrypoint plus optional command.
type HealthCheck struct {
	Entry string `yaml:"entry"`
}

// BackupRestore describes backup/restore hooks.
type BackupRestore struct {
	Backup  string `yaml:"backup,omitempty"`
	Restore string `yaml:"restore,omitempty"`
	Verify  string `yaml:"verify,omitempty"`
}

// ModuleManifest is the parsed module.yaml.
type ModuleManifest struct {
	ID           string               `yaml:"id"`
	Version      string               `yaml:"version"`
	Phase        string               `yaml:"phase"`
	DependsOn    []string             `yaml:"depends_on,omitempty"`
	ConfigFields []Field              `yaml:"config_fields"`
	SecretFields []Field              `yaml:"secret_fields"`
	Delivery     string               `yaml:"delivery"` // env|file
	Operations   map[string]Operation `yaml:"operations"`
	HealthCheck  map[string]string    `yaml:"healthcheck"`
	Backup       *BackupRestore       `yaml:"backup,omitempty"`
	Concurrency  string               `yaml:"concurrency"` // node|service|none
	Digest       string               `yaml:"-"`           // sha256 of manifest bytes
	Dir          string               `yaml:"-"`
}

// Validate checks a parsed manifest for structural correctness.
func (m *ModuleManifest) Validate() error {
	if m.ID == "" {
		return errors.New("module: missing id")
	}
	if m.Version == "" {
		return fmt.Errorf("module %s: missing version", m.ID)
	}
	if m.Phase != PhaseFoundationCore && m.Phase != PhaseFoundationServices && m.Phase != PhaseServices {
		return fmt.Errorf("module %s: invalid phase %q", m.ID, m.Phase)
	}
	if m.Delivery != DeliveryEnv && m.Delivery != DeliveryFile {
		return fmt.Errorf("module %s: invalid delivery %q", m.ID, m.Delivery)
	}
	for name := range m.Operations {
		if !AllowedOperations[name] {
			return fmt.Errorf("module %s: operation %q not in whitelist", m.ID, name)
		}
		op := m.Operations[name]
		if op.Entry == "" {
			return fmt.Errorf("module %s: operation %s missing entry", m.ID, name)
		}
		if strings.Contains(op.Entry, "..") || filepath.IsAbs(op.Entry) {
			return fmt.Errorf("module %s: operation %s has invalid entry %q", m.ID, name, op.Entry)
		}
	}
	return nil
}

// Load reads and validates a module.yaml from dir.
func Load(dir string) (*ModuleManifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "module.yaml"))
	if err != nil {
		return nil, err
	}
	var m ModuleManifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("module %s: %w", dir, err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	m.Dir = dir
	return &m, nil
}

// LoadAll scans a modules directory for module.yaml files.
func LoadAll(modulesDir string) ([]*ModuleManifest, error) {
	entries, err := os.ReadDir(modulesDir)
	if err != nil {
		return nil, err
	}
	var mods []*ModuleManifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := Load(filepath.Join(modulesDir, e.Name()))
		if err != nil {
			return nil, err
		}
		mods = append(mods, m)
	}
	return mods, nil
}

// ByID returns a module by id.
func ByID(mods []*ModuleManifest, id string) *ModuleManifest {
	for _, m := range mods {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// SortedIDs returns module ids sorted by phase then id.
func SortedIDs(mods []*ModuleManifest) []string {
	ids := make([]string, 0, len(mods))
	for _, m := range mods {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids
}
