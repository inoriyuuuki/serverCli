package repo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarEntry struct {
	name     string
	mode     int64
	typeflag byte
	link     string
	data     string
}

func buildTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     mode,
			Size:     int64(len(e.data)),
			Typeflag: e.typeflag,
			Linkname: e.link,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", e.name, err)
		}
		if e.typeflag == tar.TypeReg && len(e.data) > 0 {
			if _, err := tw.Write([]byte(e.data)); err != nil {
				t.Fatalf("write data %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func defaultLimits() Limits {
	return Limits{
		MaxFiles:           1000,
		MaxTotalBytes:      1 << 20,
		MaxSingleFileBytes: 1 << 20,
		MaxPathLen:         255,
	}
}

func TestExtractTarGzipNormal(t *testing.T) {
	data := buildTarGz(t, []tarEntry{
		{name: "a/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "a/b.txt", typeflag: tar.TypeReg, data: "hello"},
		{name: "top.txt", typeflag: tar.TypeReg, data: "data"},
	})
	dest := filepath.Join(t.TempDir(), "dest")
	if err := ExtractTarGzip(context.Background(), bytes.NewReader(data), dest, defaultLimits()); err != nil {
		t.Fatalf("extract: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "a", "b.txt"))
	if err != nil {
		t.Fatalf("read a/b.txt: %v", err)
	}
	if string(b) != "hello" {
		t.Fatalf("a/b.txt = %q, want %q", b, "hello")
	}
	b, err = os.ReadFile(filepath.Join(dest, "top.txt"))
	if err != nil {
		t.Fatalf("read top.txt: %v", err)
	}
	if string(b) != "data" {
		t.Fatalf("top.txt = %q, want %q", b, "data")
	}
}

func TestExtractTarGzipRejectsMalicious(t *testing.T) {
	base := t.TempDir()
	ctx := context.Background()

	tests := []struct {
		name    string
		entries []tarEntry
		limits  Limits
		// outside is a path (relative to base) that must not exist afterwards.
		outside string
	}{
		{
			name:    "parent traversal",
			entries: []tarEntry{{name: "../evil.txt", typeflag: tar.TypeReg, data: "x"}},
			outside: filepath.Join("evil.txt"),
		},
		{
			name:    "deep parent traversal",
			entries: []tarEntry{{name: "a/../../evil.txt", typeflag: tar.TypeReg, data: "x"}},
			outside: filepath.Join("evil.txt"),
		},
		{
			name:    "absolute path",
			entries: []tarEntry{{name: "/tmp/repo-evil.txt", typeflag: tar.TypeReg, data: "x"}},
			outside: filepath.Join("tmp", "repo-evil.txt"),
		},
		{
			name:    "backslash",
			entries: []tarEntry{{name: `a\b`, typeflag: tar.TypeReg, data: "x"}},
			outside: filepath.Join("a\\b"),
		},
		{
			name:    "control character",
			entries: []tarEntry{{name: "a\x01b", typeflag: tar.TypeReg, data: "x"}},
			outside: filepath.Join("a\x01b"),
		},
		{
			name:    "symlink",
			entries: []tarEntry{{name: "link", typeflag: tar.TypeSymlink, link: "/etc/passwd"}},
		},
		{
			name: "hardlink",
			entries: []tarEntry{
				{name: "orig", typeflag: tar.TypeReg, data: "x"},
				{name: "hard", typeflag: tar.TypeLink, link: "orig"},
			},
		},
		{
			name:    "fifo",
			entries: []tarEntry{{name: "pipe", typeflag: tar.TypeFifo}},
		},
		{
			name:    "setuid",
			entries: []tarEntry{{name: "suid", typeflag: tar.TypeReg, mode: 0o4755, data: "x"}},
		},
		{
			name:    "sticky",
			entries: []tarEntry{{name: "sticky", typeflag: tar.TypeReg, mode: 0o1644, data: "x"}},
		},
		{
			name:    "oversized single file",
			entries: []tarEntry{{name: "big", typeflag: tar.TypeReg, data: strings.Repeat("a", 200)}},
			limits: func() Limits {
				l := defaultLimits()
				l.MaxSingleFileBytes = 100
				return l
			}(),
		},
		{
			name: "too many files",
			entries: []tarEntry{
				{name: "f1", typeflag: tar.TypeReg, data: "a"},
				{name: "f2", typeflag: tar.TypeReg, data: "b"},
				{name: "f3", typeflag: tar.TypeReg, data: "c"},
				{name: "f4", typeflag: tar.TypeReg, data: "d"},
			},
			limits: func() Limits {
				l := defaultLimits()
				l.MaxFiles = 3
				return l
			}(),
		},
		{
			name: "total too large",
			entries: []tarEntry{
				{name: "f1", typeflag: tar.TypeReg, data: strings.Repeat("a", 60)},
				{name: "f2", typeflag: tar.TypeReg, data: strings.Repeat("b", 60)},
			},
			limits: func() Limits {
				l := defaultLimits()
				l.MaxTotalBytes = 100
				return l
			}(),
		},
		{
			name:    "name too long",
			entries: []tarEntry{{name: strings.Repeat("x", 300), typeflag: tar.TypeReg, data: "a"}},
			limits: func() Limits {
				l := defaultLimits()
				l.MaxPathLen = 64
				return l
			}(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(base, "dest-"+strings.ReplaceAll(tc.name, " ", "-"))
			limits := tc.limits
			if limits.MaxFiles == 0 {
				limits = defaultLimits()
			}
			data := buildTarGz(t, tc.entries)
			if err := ExtractTarGzip(ctx, bytes.NewReader(data), dest, limits); err == nil {
				t.Fatalf("expected error for %q", tc.name)
			}
			if tc.outside != "" {
				if _, err := os.Lstat(filepath.Join(base, tc.outside)); err == nil {
					t.Fatalf("out-of-bounds file %s was created", tc.outside)
				}
			}
			// destRoot itself must remain a directory, never a file.
			if fi, err := os.Lstat(dest); err == nil && !fi.IsDir() {
				t.Fatalf("destRoot %s became a non-directory", dest)
			}
		})
	}
}

func TestExtractTarGzipDestRootOutsideAllowedRoot(t *testing.T) {
	base := t.TempDir()
	limits := defaultLimits()
	limits.AllowedRoot = filepath.Join(base, "allowed")
	dest := filepath.Join(base, "outside", "dest")
	if err := ExtractTarGzip(context.Background(), bytes.NewReader(nil), dest, limits); err == nil {
		t.Fatal("expected error when destRoot is outside AllowedRoot")
	}
}

func TestExtractTarGzipZeroLimitsRejected(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "dest")
	if err := ExtractTarGzip(context.Background(), bytes.NewReader(nil), dest, Limits{}); err == nil {
		t.Fatal("expected error for zero limits")
	}
}

func TestSwitchDirSuccessNew(t *testing.T) {
	base := t.TempDir()
	staging := filepath.Join(base, "staging")
	final := filepath.Join(base, "final")
	mustWrite(t, filepath.Join(staging, "x.txt"), "new")
	if err := SwitchDir(staging, final); err != nil {
		t.Fatalf("SwitchDir: %v", err)
	}
	if b := mustRead(t, filepath.Join(final, "x.txt")); b != "new" {
		t.Fatalf("final/x.txt = %q, want %q", b, "new")
	}
	if _, err := os.Lstat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging should be gone, got %v", err)
	}
}

func TestSwitchDirSuccessReplace(t *testing.T) {
	base := t.TempDir()
	staging := filepath.Join(base, "staging")
	final := filepath.Join(base, "final")
	mustWrite(t, filepath.Join(staging, "x.txt"), "new")
	mustWrite(t, filepath.Join(final, "old.txt"), "old")
	if err := SwitchDir(staging, final); err != nil {
		t.Fatalf("SwitchDir: %v", err)
	}
	if b := mustRead(t, filepath.Join(final, "x.txt")); b != "new" {
		t.Fatalf("final/x.txt = %q, want %q", b, "new")
	}
	if _, err := os.Lstat(filepath.Join(final, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old content should have been replaced")
	}
	if _, err := os.Lstat(final + ".old"); !os.IsNotExist(err) {
		t.Fatalf("backup %s.old should have been removed", final)
	}
}

func TestSwitchDirFailureRollback(t *testing.T) {
	base := t.TempDir()
	staging := filepath.Join(base, "staging")
	final := filepath.Join(base, "final")
	mustWrite(t, filepath.Join(staging, "x.txt"), "new")
	mustWrite(t, filepath.Join(final, "keep.txt"), "keep")

	// Read-only parent forces the backup rename to fail.
	if err := os.Chmod(base, 0o500); err != nil {
		t.Fatalf("chmod base: %v", err)
	}
	err := SwitchDir(staging, final)
	if cerr := os.Chmod(base, 0o700); cerr != nil {
		t.Fatalf("restore chmod base: %v", cerr)
	}
	if err == nil {
		t.Fatal("expected SwitchDir error with read-only parent")
	}
	// Target must be untouched.
	if b := mustRead(t, filepath.Join(final, "keep.txt")); b != "keep" {
		t.Fatalf("final/keep.txt = %q, want %q (target corrupted)", b, "keep")
	}
	if _, err := os.Lstat(final + ".old"); !os.IsNotExist(err) {
		t.Fatalf("backup %s.old must not be left dangling", final)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestExtractTarGzipAllowsRootDot verifies a tarball built with `tar -czf .`
// (whose first entry is "./") extracts successfully: the root marker "." is
// allowed while ".." remains rejected.
func TestExtractTarGzipAllowsRootDot(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	// root marker
	if err := tw.WriteHeader(&tar.Header{Name: "./", Mode: 0o750, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	content := []byte("hello")
	if err := tw.WriteHeader(&tar.Header{Name: "./hooks/install.sh", Mode: 0o640, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := ExtractTarGzip(context.Background(), &buf, dest, Limits{
		MaxFiles: 100, MaxTotalBytes: 1 << 20, MaxSingleFileBytes: 1 << 20, MaxPathLen: 1024, AllowedRoot: dest,
	}); err != nil {
		t.Fatalf("extract with root dot should succeed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "hooks", "install.sh")); err != nil || string(got) != "hello" {
		t.Fatalf("extracted content = %q, err=%v", got, err)
	}
}
