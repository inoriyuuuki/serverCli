package repo

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
)

// BundleFileName is the canonical V1 release bundle object name. V1 uses a
// gzip-compressed tar bundle (bundle.tar.gz) even though OSS layout naming
// elsewhere may mention .tar.zst; zstd is intentionally avoided so no
// third-party dependency is required.
const BundleFileName = "bundle.tar.gz"

// Limits bounds archive extraction. All numeric limits must be positive.
type Limits struct {
	MaxFiles           int
	MaxTotalBytes      int64
	MaxSingleFileBytes int64
	MaxPathLen         int
	// AllowedRoot, when non-empty, restricts where destRoot itself may live.
	AllowedRoot string
}

// ExtractTarGzip safely extracts a gzip-compressed tar archive from r into
// destRoot. The archive payload is an ordinary tar stream (V1 bundle format:
// bundle.tar.gz).
//
// Security properties:
//   - every entry name is validated (non-empty, relative, no "..", no
//     absolute path, no backslash, no control characters, bounded length)
//     and the cleaned destination must stay inside destRoot;
//   - symlinks, hardlinks and special files (device/fifo) are rejected;
//   - setuid/setgid/sticky bits are rejected and extracted modes are
//     sanitised (directories 0750, regular files 0640; ownership is ignored);
//   - per-entry and aggregate byte limits are enforced, together with a
//     per-file count limit;
//   - free disk space is probed with syscall.Statfs before each regular file
//     is written; if the probe is unavailable a warning is logged and
//     extraction continues (documented approximation);
//   - after extraction the tree is re-walked to confirm nothing escaped
//     destRoot.
func ExtractTarGzip(ctx context.Context, r io.Reader, destRoot string, limits Limits) error {
	if limits.MaxFiles <= 0 {
		return fmt.Errorf("limits: MaxFiles must be positive")
	}
	if limits.MaxTotalBytes <= 0 {
		return fmt.Errorf("limits: MaxTotalBytes must be positive")
	}
	if limits.MaxSingleFileBytes <= 0 {
		return fmt.Errorf("limits: MaxSingleFileBytes must be positive")
	}
	if limits.MaxPathLen <= 0 {
		return fmt.Errorf("limits: MaxPathLen must be positive")
	}
	if limits.AllowedRoot != "" && !pathWithin(limits.AllowedRoot, destRoot) {
		return fmt.Errorf("destRoot %s is not within AllowedRoot %s", destRoot, limits.AllowedRoot)
	}
	if err := os.MkdirAll(destRoot, 0o750); err != nil {
		return fmt.Errorf("create destRoot %s: %w", destRoot, err)
	}

	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var entries int
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		if err := validateEntryName(hdr.Name, limits.MaxPathLen); err != nil {
			return fmt.Errorf("tar entry %q: %w", hdr.Name, err)
		}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeDir:
			// allowed
		case tar.TypeSymlink, tar.TypeLink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			return fmt.Errorf("tar entry %q: links and special files are rejected (type %s)", hdr.Name, typeName(hdr.Typeflag))
		default:
			return fmt.Errorf("tar entry %q: unsupported entry type %d", hdr.Name, hdr.Typeflag)
		}
		if hdr.Mode&(0o4000|0o2000|0o1000) != 0 {
			return fmt.Errorf("tar entry %q: setuid/setgid/sticky bits are rejected", hdr.Name)
		}

		entries++
		if entries > limits.MaxFiles {
			return fmt.Errorf("tar exceeds MaxFiles %d", limits.MaxFiles)
		}

		target := filepath.Join(destRoot, hdr.Name)
		if !pathWithin(destRoot, target) {
			return fmt.Errorf("tar entry %q escapes destination", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
			if err := os.Chmod(target, 0o750); err != nil {
				return fmt.Errorf("chmod %s: %w", target, err)
			}
		case tar.TypeReg:
			if hdr.Size > limits.MaxSingleFileBytes {
				return fmt.Errorf("tar entry %q exceeds MaxSingleFileBytes %d", hdr.Name, limits.MaxSingleFileBytes)
			}
			if total+hdr.Size > limits.MaxTotalBytes {
				return fmt.Errorf("tar exceeds MaxTotalBytes %d", limits.MaxTotalBytes)
			}
			if err := checkDiskSpace(destRoot, hdr.Size); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
			if err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			n, cerr := io.Copy(f, io.LimitReader(tr, hdr.Size))
			closeErr := f.Close()
			if cerr != nil {
				return fmt.Errorf("write %s: %w", target, cerr)
			}
			if closeErr != nil {
				return fmt.Errorf("close %s: %w", target, closeErr)
			}
			if n != hdr.Size {
				return fmt.Errorf("tar entry %q truncated: got %d bytes, header declares %d", hdr.Name, n, hdr.Size)
			}
			total += n
		}
	}

	// Final re-validation: nothing may have escaped destRoot.
	if err := verifyNoEscape(destRoot); err != nil {
		return err
	}
	return nil
}

// validateEntryName rejects unsafe tar entry names.
func validateEntryName(name string, maxLen int) error {
	if name == "" {
		return fmt.Errorf("empty entry name")
	}
	if len(name) > maxLen {
		return fmt.Errorf("entry name too long (%d > %d)", len(name), maxLen)
	}
	if filepath.IsAbs(name) {
		return fmt.Errorf("absolute entry name %q", name)
	}
	if strings.ContainsRune(name, '\\') {
		return fmt.Errorf("entry name %q contains a backslash", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("entry name %q contains a control character", name)
		}
	}
	for _, comp := range strings.Split(filepath.ToSlash(name), "/") {
		if comp == ".." {
			return fmt.Errorf("entry name %q contains a .. component", name)
		}
	}
	if cleaned := filepath.Clean(name); cleaned == ".." {
		return fmt.Errorf("entry name %q escapes destination", name)
	}
	// "." (the archive root marker produced by `tar -czf .`) is allowed: it
	// maps to the destination root, which is created anyway. Only ".." is a
	// genuine escape.
	return nil
}

// pathWithin reports whether target is base itself or lies under base.
func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// verifyNoEscape re-walks root and fails if any node escaped it. This is a
// defensive no-op under normal operation (paths were already constrained),
// performed after extraction as documented.
func verifyNoEscape(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("verify walk %s: %w", path, err)
		}
		if !pathWithin(root, path) {
			return fmt.Errorf("verify: %s escaped %s", path, root)
		}
		return nil
	})
}

// checkDiskSpace verifies there is at least need bytes free on the filesystem
// containing dir. If the probe cannot be performed (unsupported platform), a
// warning is logged and the check is skipped; this is the documented
// approximation: the real guarantee comes from per-file and aggregate size
// limits.
func checkDiskSpace(dir string, need int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		log.Printf("repo: statfs %s failed (%v); skipping free-space check", dir, err)
		return nil
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	if need > free {
		return fmt.Errorf("insufficient disk space on %s: need %d bytes, %d free", dir, need, free)
	}
	return nil
}

func typeName(t byte) string {
	switch t {
	case tar.TypeSymlink:
		return "symlink"
	case tar.TypeLink:
		return "hardlink"
	case tar.TypeChar:
		return "char device"
	case tar.TypeBlock:
		return "block device"
	case tar.TypeFifo:
		return "fifo"
	}
	return fmt.Sprintf("type %d", t)
}

// SwitchDir atomically replaces finalDir with stagingDir:
//
//  1. every file and directory under stagingDir is fsynced;
//  2. if finalDir exists it is renamed to finalDir.old as a backup;
//  3. stagingDir is renamed to finalDir;
//  4. on failure the backup is restored and the error returned;
//  5. on success the backup is removed best-effort and the parent directory
//     is fsynced so the rename is durable.
func SwitchDir(stagingDir, finalDir string) error {
	if err := fsyncTree(stagingDir); err != nil {
		return fmt.Errorf("fsync staging: %w", err)
	}

	backup := finalDir + ".old"
	hadBackup := false
	if _, err := os.Lstat(finalDir); err == nil {
		if err := os.Rename(finalDir, backup); err != nil {
			return fmt.Errorf("backup %s: %w", finalDir, err)
		}
		hadBackup = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", finalDir, err)
	}

	if err := os.Rename(stagingDir, finalDir); err != nil {
		if hadBackup {
			if rerr := os.Rename(backup, finalDir); rerr != nil {
				return fmt.Errorf("switch %s: %v (rollback failed: %v)", finalDir, err, rerr)
			}
		}
		return fmt.Errorf("switch %s: %w", finalDir, err)
	}

	if hadBackup {
		if err := os.RemoveAll(backup); err != nil {
			log.Printf("repo: remove backup %s failed (best-effort): %v", backup, err)
		}
	}
	if d, err := os.Open(filepath.Dir(finalDir)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// fsyncTree opens every node under root (root included) and fsyncs it so the
// subsequent rename is durable. Directory fsync failures are non-fatal (some
// platforms do not support it) and only logged.
func fsyncTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		serr := f.Sync()
		_ = f.Close()
		if serr != nil {
			if d.IsDir() {
				log.Printf("repo: fsync dir %s failed (non-fatal): %v", path, serr)
				return nil
			}
			return fmt.Errorf("sync %s: %w", path, serr)
		}
		return nil
	})
}
