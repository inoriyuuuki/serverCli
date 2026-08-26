// Package repo implements the node-side unified deployment directory layout,
// repository manifest verification, and safe archive extraction for the
// deployment management module.
//
// Directory model
//
//	<Root>/
//	  repository/            OSS-authoritative sync zone (never holds runtime state)
//	    catalog/ features/ releases/ configs/
//	    shared/configs/ shared/secrets/
//	    nodes/ manifests/
//	    secrets/            secret material, tightened to 0700
//	  .servercli-local/      node runtime zone, never synced (0700/0600)
//	    credentials/ runtime/ state/ rendered/ staging/ logs/
//
// After every sync the tree must be normalised with FixPermissions. The
// .servercli-local zone is excluded from sync via SyncEligiblePath.
package repo

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DeploymentRootDefault is the default root of the node-side deployment tree.
const DeploymentRootDefault = "/opt/servercli-deployment"

// Top-level directory names under the deployment root.
const (
	// RepoDirRepository is the OSS-authoritative sync zone.
	RepoDirRepository = "repository"
	// LocalDirLocal is the node runtime zone. It is excluded from sync.
	LocalDirLocal = ".servercli-local"
)

// Repository (sync zone) sub-directory names, relative to repository/.
const (
	DirCatalog       = "catalog"
	DirFeatures      = "features"
	DirReleases      = "releases"
	DirConfigs       = "configs"
	DirSharedConfigs = "shared/configs"
	DirNodes         = "nodes"
	DirSecrets       = "secrets"
	DirSharedSecrets = "shared/secrets"
	DirManifests     = "manifests"
)

// Local (node runtime zone) sub-directory names, relative to .servercli-local/.
const (
	DirCredentials = "credentials"
	DirRuntime     = "runtime"
	DirState       = "state"
	DirRendered    = "rendered"
	DirStaging     = "staging"
	DirLogs        = "logs"
)

// Layout describes the node-side deployment directory tree rooted at Root.
type Layout struct {
	Root string
}

// New returns a Layout rooted at root.
func New(root string) *Layout {
	return &Layout{Root: root}
}

// RepoDir returns the full path of the OSS sync zone.
func (l *Layout) RepoDir() string { return filepath.Join(l.Root, RepoDirRepository) }

// LocalDir returns the full path of the node runtime zone.
func (l *Layout) LocalDir() string { return filepath.Join(l.Root, LocalDirLocal) }

// repoSubPaths lists repository sub-directory names in creation order.
var repoSubPaths = []string{
	DirCatalog, DirFeatures, DirReleases, DirConfigs, DirSharedConfigs,
	DirNodes, DirSecrets, DirSharedSecrets, DirManifests,
}

// localSubPaths lists .servercli-local sub-directory names.
var localSubPaths = []string{
	DirCredentials, DirRuntime, DirState, DirRendered, DirStaging, DirLogs,
}

// EnsureAll creates every directory in the layout with its canonical mode:
// repository/ is 0750 except the secrets directories which are 0700;
// .servercli-local/ is entirely 0700.
func (l *Layout) EnsureAll(ctx context.Context) error {
	repoDir := l.RepoDir()
	if err := os.MkdirAll(repoDir, 0o750); err != nil {
		return fmt.Errorf("create repository root %s: %w", repoDir, err)
	}
	if err := os.Chmod(repoDir, 0o750); err != nil {
		return fmt.Errorf("chmod repository root %s: %w", repoDir, err)
	}
	for _, sub := range repoSubPaths {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := filepath.Join(repoDir, sub)
		mode := os.FileMode(0o750)
		if isSecretsPath(sub) {
			mode = 0o700
		}
		if err := os.MkdirAll(p, mode); err != nil {
			return fmt.Errorf("create %s: %w", p, err)
		}
		if err := os.Chmod(p, mode); err != nil {
			return fmt.Errorf("chmod %s: %w", p, err)
		}
	}

	localDir := l.LocalDir()
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		return fmt.Errorf("create local root %s: %w", localDir, err)
	}
	if err := os.Chmod(localDir, 0o700); err != nil {
		return fmt.Errorf("chmod local root %s: %w", localDir, err)
	}
	for _, sub := range localSubPaths {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := filepath.Join(localDir, sub)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", p, err)
		}
		if err := os.Chmod(p, 0o700); err != nil {
			return fmt.Errorf("chmod %s: %w", p, err)
		}
	}
	return nil
}

// FixPermissions normalises permissions under root after a sync:
//
//   - repository/: secrets directories 0700, files inside secrets 0600,
//     every other directory 0750 and every other file 0640;
//   - .servercli-local/: all directories 0700, all files 0600.
//
// Errors carry the offending path but never file contents. Missing zones are
// skipped.
func FixPermissions(root string) error {
	if err := walkChmod(filepath.Join(root, RepoDirRepository), func(rel string, isDir bool) os.FileMode {
		if isSecretsPath(rel) {
			if isDir {
				return 0o700
			}
			return 0o600
		}
		if isDir {
			return 0o750
		}
		return 0o640
	}); err != nil {
		return err
	}
	if err := walkChmod(filepath.Join(root, LocalDirLocal), func(_ string, isDir bool) os.FileMode {
		if isDir {
			return 0o700
		}
		return 0o600
	}); err != nil {
		return err
	}
	return nil
}

// walkChmod applies modeFor to every node under root (root included). rel is
// the slash-separated path relative to root.
func walkChmod(root string, modeFor func(rel string, isDir bool) os.FileMode) error {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", root, err)
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return fmt.Errorf("rel %s: %w", path, rerr)
		}
		if cerr := os.Chmod(path, modeFor(filepath.ToSlash(rel), d.IsDir())); cerr != nil {
			return fmt.Errorf("chmod %s: %w", path, cerr)
		}
		return nil
	})
}

// isSecretsPath reports whether any path component of rel equals "secrets"
// (e.g. "secrets", "shared/secrets", "nodes/secrets").
func isSecretsPath(rel string) bool {
	for _, comp := range strings.Split(filepath.ToSlash(rel), "/") {
		if comp == "secrets" {
			return true
		}
	}
	return false
}

// SyncEligiblePath reports whether path may be included in OSS sync: only
// relative paths under repository/ are eligible. The .servercli-local zone
// and anything outside the sync zone is excluded, so node runtime state is
// never uploaded or overwritten by sync.
func (l *Layout) SyncEligiblePath(path string) bool {
	if path == "" || filepath.IsAbs(path) {
		return false
	}
	cleaned := filepath.Clean(path)
	if cleaned == "." || cleaned == ".." ||
		strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return false
	}
	if cleaned == RepoDirRepository {
		return true
	}
	return strings.HasPrefix(cleaned, RepoDirRepository+string(filepath.Separator))
}
