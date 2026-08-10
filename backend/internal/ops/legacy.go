package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Legacy backup formats that the adapter can recognize read-only.
const (
	FormatLegacyCatalog = "legacy-catalog"
	FormatLegacyImport  = "legacy-import"
)

// ErrNoVerifiedManifest is returned by LegacyBackup.Manifest: a legacy backup
// can never be represented as a verified new-format manifest, so it cannot
// masquerade as a full backup.
var ErrNoVerifiedManifest = errors.New("ops: legacy backup has no verifiable new-format manifest")

// legacyMissingMetadata lists the metadata a new-format manifest guarantees
// but a legacy backup does not.
var legacyMissingMetadata = []string{
	"backup_id",
	"created_at",
	"app_version",
	"db_schema_version",
	"file_digests",
	"signature",
}

// LegacyBackup is a read-only description of an old-format backup. Verified is
// always false: legacy backups carry no signature and no file digests, so they
// are informational only and must never be used to skip verification.
type LegacyBackup struct {
	BackupID        string   `json:"backup_id"`
	Service         string   `json:"service,omitempty"`
	Format          string   `json:"format"`
	MissingMetadata []string `json:"missing_metadata"`
	Verified        bool     `json:"verified"`
	Files           []string `json:"files,omitempty"`
	SourcePath      string   `json:"source_path,omitempty"`
}

// Manifest always fails: a legacy backup cannot be converted into a verified
// new-format manifest. This is the explicit "cannot masquerade" guarantee.
func (l *LegacyBackup) Manifest() (*BackupManifest, error) {
	return nil, fmt.Errorf("%w: %s", ErrNoVerifiedManifest, l.BackupID)
}

// LegacyBackupAdapter performs read-only recognition of old-format backups
// under a backup directory. It never writes, migrates or "upgrades" legacy
// data.
type LegacyBackupAdapter struct {
	dir string
}

// NewLegacyBackupAdapter builds an adapter over a backup directory.
func NewLegacyBackupAdapter(dir string) *LegacyBackupAdapter {
	return &LegacyBackupAdapter{dir: dir}
}

// List returns every legacy backup directory found under the backup root.
func (a *LegacyBackupAdapter) List(ctx context.Context) ([]LegacyBackup, error) {
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []LegacyBackup
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		lb, err := a.readDir(ctx, e.Name())
		if err != nil {
			continue // not a legacy backup directory
		}
		out = append(out, *lb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BackupID < out[j].BackupID })
	return out, nil
}

// Read recognizes one backup directory. It returns an error when the
// directory is not a legacy backup (for example it is a verified new-format
// backup).
func (a *LegacyBackupAdapter) Read(ctx context.Context, backupID string) (*LegacyBackup, error) {
	dir := filepath.Join(a.dir, backupID)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, ErrBackupNotFound
	}
	// A new-format backup is not legacy: defer to the real manifest.
	if _, err := os.Stat(filepath.Join(dir, manifestFilename)); err == nil {
		return nil, ErrBackupNotFound
	}
	return a.readDir(ctx, backupID)
}

func (a *LegacyBackupAdapter) readDir(ctx context.Context, name string) (*LegacyBackup, error) {
	dir := filepath.Join(a.dir, name)
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return nil, ErrBackupNotFound
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	format := FormatLegacyImport
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch e.Name() {
		case "catalog.json", "backup.catalog", "catalog.yaml":
			format = FormatLegacyCatalog
		}
		files = append(files, e.Name())
	}
	if len(files) == 0 {
		return nil, ErrBackupNotFound
	}
	sort.Strings(files)
	lb := &LegacyBackup{
		BackupID:        name,
		Format:          format,
		MissingMetadata: append([]string(nil), legacyMissingMetadata...),
		Verified:        false, // legacy backups are never verified
		Files:           files,
		SourcePath:      dir,
	}
	// Service is not reliably derivable from a legacy directory; leave it
	// empty and flag it as missing metadata when it cannot be determined.
	return lb, nil
}
