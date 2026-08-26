package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRelPath(t *testing.T) {
	valid := []string{
		"a/b/c.txt",
		"catalog/foo.json",
		"shared/configs/app.yaml",
		"releases/bundle.tar.gz",
	}
	for _, p := range valid {
		if err := ValidateRelPath(p); err != nil {
			t.Errorf("ValidateRelPath(%q) = %v, want nil", p, err)
		}
	}

	invalid := []string{
		"",
		"../evil",
		"a/../b",
		"/abs/path",
		`a\b`,
		"a\x01b",
		"a\x7fb",
	}
	for _, p := range invalid {
		if err := ValidateRelPath(p); err == nil {
			t.Errorf("ValidateRelPath(%q) = nil, want error", p)
		}
	}
}

type manifestFixture struct {
	root    string
	objects map[string]string // rel path -> content
}

// buildManifestRoot creates rootDir with manifests/repository-manifest.json
// listing objects with correct size/sha256, then applies mutate to alter the
// recorded values if needed.
func buildManifestRoot(t *testing.T, objects map[string]string, mutate func(m *RepositoryManifest)) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DirManifests), 0o755); err != nil {
		t.Fatalf("mkdir manifests: %v", err)
	}
	m := &RepositoryManifest{Version: ManifestVersion}
	for p, content := range objects {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		sum := sha256.Sum256([]byte(content))
		m.Objects = append(m.Objects, ManifestObject{
			Path:   p,
			Size:   int64(len(content)),
			SHA256: hex.EncodeToString(sum[:]),
		})
	}
	if mutate != nil {
		mutate(m)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, DirManifests, ManifestFileName), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return root
}

func TestVerifyManifestOK(t *testing.T) {
	root := buildManifestRoot(t, map[string]string{
		"catalog/app.json":       `{"name":"app"}`,
		"releases/bundle.tar.gz": "bundle-bytes",
	}, nil)
	if err := VerifyManifest(context.Background(), root); err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
}

func TestVerifyManifestFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(m *RepositoryManifest)
	}{
		{
			name: "bad hash",
			mutate: func(m *RepositoryManifest) {
				m.Objects[0].SHA256 = hex.EncodeToString(make([]byte, 32))
			},
		},
		{
			name: "wrong size",
			mutate: func(m *RepositoryManifest) {
				m.Objects[0].Size++
			},
		},
		{
			name: "missing object",
			mutate: func(m *RepositoryManifest) {
				m.Objects = append(m.Objects, ManifestObject{
					Path:   "releases/does-not-exist.tar.gz",
					Size:   1,
					SHA256: hex.EncodeToString(make([]byte, 32)),
				})
			},
		},
		{
			name: "unsafe path",
			mutate: func(m *RepositoryManifest) {
				m.Objects = append(m.Objects, ManifestObject{
					Path: "../escape.txt", Size: 1,
					SHA256: hex.EncodeToString(make([]byte, 32)),
				})
			},
		},
		{
			name: "symlink object",
			mutate: func(m *RepositoryManifest) {
				m.Objects = append(m.Objects, ManifestObject{
					Path: "releases/link.tar.gz", Size: 1,
					SHA256: hex.EncodeToString(make([]byte, 32)),
				})
			},
		},
		{
			name: "duplicate path",
			mutate: func(m *RepositoryManifest) {
				m.Objects = append(m.Objects, m.Objects[0])
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := buildManifestRoot(t, map[string]string{
				"catalog/app.json": `{"name":"app"}`,
			}, tc.mutate)
			if tc.name == "symlink object" {
				// Materialise the symlink the manifest points at.
				linkDir := filepath.Join(root, "releases")
				if err := os.MkdirAll(linkDir, 0o755); err != nil {
					t.Fatalf("mkdir releases: %v", err)
				}
				if err := os.Symlink("../../etc/passwd", filepath.Join(linkDir, "link.tar.gz")); err != nil {
					t.Fatalf("symlink: %v", err)
				}
			}
			if err := VerifyManifest(context.Background(), root); err == nil {
				t.Fatalf("expected error for %q", tc.name)
			}
		})
	}
}

func TestVerifyManifestWrongVersion(t *testing.T) {
	root := buildManifestRoot(t, map[string]string{
		"catalog/app.json": `{}`,
	}, func(m *RepositoryManifest) { m.Version = 99 })
	if err := VerifyManifest(context.Background(), root); err == nil {
		t.Fatal("expected error for unsupported version")
	}
}

func TestVerifyObjectsNil(t *testing.T) {
	if err := VerifyObjects(nil, t.TempDir()); err == nil {
		t.Fatal("expected error for nil manifest")
	}
}

func TestEnsureAll(t *testing.T) {
	root := filepath.Join(t.TempDir(), "deploy")
	l := New(root)
	if err := l.EnsureAll(context.Background()); err != nil {
		t.Fatalf("EnsureAll: %v", err)
	}
	checkMode := func(rel string, want os.FileMode) {
		t.Helper()
		fi, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("mode %s = %o, want %o", rel, got, want)
		}
	}
	// repository zone
	checkMode(RepoDirRepository, 0o750)
	checkMode(filepath.Join(RepoDirRepository, DirCatalog), 0o750)
	checkMode(filepath.Join(RepoDirRepository, DirManifests), 0o750)
	checkMode(filepath.Join(RepoDirRepository, DirSecrets), 0o700)
	checkMode(filepath.Join(RepoDirRepository, DirSharedSecrets), 0o700)
	// local zone
	checkMode(LocalDirLocal, 0o700)
	checkMode(filepath.Join(LocalDirLocal, DirCredentials), 0o700)
	checkMode(filepath.Join(LocalDirLocal, DirState), 0o700)
}

func TestFixPermissions(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write(filepath.Join(RepoDirRepository, "catalog", "app.json"))
	write(filepath.Join(RepoDirRepository, "shared", "configs", "conf.yaml"))
	write(filepath.Join(RepoDirRepository, "secrets", "secret.txt"))
	write(filepath.Join(RepoDirRepository, "shared", "secrets", "cred.txt"))
	write(filepath.Join(LocalDirLocal, "state", "state.bin"))

	if err := FixPermissions(root); err != nil {
		t.Fatalf("FixPermissions: %v", err)
	}

	check := func(rel string, want os.FileMode) {
		t.Helper()
		fi, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("mode %s = %o, want %o", rel, got, want)
		}
	}
	check(filepath.Join(RepoDirRepository, "catalog"), 0o750)
	check(filepath.Join(RepoDirRepository, "catalog", "app.json"), 0o640)
	check(filepath.Join(RepoDirRepository, "shared", "configs", "conf.yaml"), 0o640)
	check(filepath.Join(RepoDirRepository, "secrets"), 0o700)
	check(filepath.Join(RepoDirRepository, "secrets", "secret.txt"), 0o600)
	check(filepath.Join(RepoDirRepository, "shared", "secrets"), 0o700)
	check(filepath.Join(RepoDirRepository, "shared", "secrets", "cred.txt"), 0o600)
	check(filepath.Join(LocalDirLocal, "state"), 0o700)
	check(filepath.Join(LocalDirLocal, "state", "state.bin"), 0o600)
}

func TestFixPermissionsMissingZones(t *testing.T) {
	root := t.TempDir()
	// Neither repository/ nor .servercli-local/ exists: must be a no-op.
	if err := FixPermissions(root); err != nil {
		t.Fatalf("FixPermissions on empty root: %v", err)
	}
}

func TestSyncEligiblePath(t *testing.T) {
	l := New(t.TempDir())
	eligible := []string{
		"repository",
		"repository/",
		"repository/catalog/app.json",
		"repository/secrets/secret.txt",
	}
	for _, p := range eligible {
		if !l.SyncEligiblePath(p) {
			t.Errorf("SyncEligiblePath(%q) = false, want true", p)
		}
	}
	notEligible := []string{
		"",
		".servercli-local",
		".servercli-local/state/x",
		"repository/../.servercli-local",
		"../repository",
		"/absolute/path",
		"other/file",
		"..",
	}
	for _, p := range notEligible {
		if l.SyncEligiblePath(p) {
			t.Errorf("SyncEligiblePath(%q) = true, want false", p)
		}
	}
}
