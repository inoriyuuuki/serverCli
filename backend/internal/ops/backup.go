package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"servercli/internal/initstate"
)

// Uploader uploads a backup artifact to remote storage. The default
// implementation is a no-op: backups never depend on the Control Plane, so a
// Control Plane outage does not stop local backups. Production deployments
// must implement an OSS/S3-compatible adapter and verify the remote upload
// (remote read-back / sampled verification) inside Upload; this package
// guarantees local digest verification and local read-back verification before
// a backup is reported complete.
type Uploader interface {
	Upload(ctx context.Context, path string, data []byte) error
}

// NoopUploader is the default uploader: it records nothing and always
// succeeds, keeping backups fully local.
type NoopUploader struct{}

// Upload implements Uploader with a no-op.
func (NoopUploader) Upload(ctx context.Context, path string, data []byte) error {
	_ = ctx
	_ = path
	_ = data
	return nil
}

// BackupResult is the outcome of backing up one service.
type BackupResult struct {
	Service       string `json:"service"`
	BackupID      string `json:"backup_id"`
	RecoverySetID string `json:"recovery_set_id"`
	OK            bool   `json:"ok"`
	Error         string `json:"error,omitempty"`
	ManifestPath  string `json:"manifest_path,omitempty"`
}

// manifestFilename is the signed manifest file inside each backup directory.
const manifestFilename = "manifest.json"

// newID builds a readable unique id like bak-20260810T120000Z-1f2e3d4c.
func newID(prefix string) string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UTC().UnixNano())
	}
	return fmt.Sprintf("%s-%s-%s", prefix, time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(b))
}

// Backup creates a signed, verified backup for every requested service. An
// empty service list means all services. Each backup is independent: a failure
// for one service never stops the others, and an *AggregateError is returned
// if any backup failed.
func (o *Ops) Backup(ctx context.Context, services []string, opts RunOpts) ([]BackupResult, error) {
	if len(services) == 0 {
		all, err := o.allServices()
		if err != nil {
			return nil, err
		}
		services = all
	}
	results := make([]BackupResult, 0, len(services))
	for _, svc := range services {
		res := BackupResult{Service: svc}
		cfg := o.Config
		if err := o.Ownership.CanOperate(cfg.Environment, cfg.Node, svc); err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		unlock, err := o.Ownership.Lock(cfg.Environment, cfg.Node, svc, "backup")
		if err != nil {
			res.Error = "lock: " + err.Error()
			results = append(results, res)
			continue
		}
		res = o.backupOne(ctx, svc)
		unlock()
		results = append(results, res)
	}
	var failed []BackupResult
	for _, r := range results {
		if !r.OK {
			failed = append(failed, r)
		}
	}
	if len(failed) > 0 {
		return results, &AggregateError{Total: len(results), Failures: toResults(failed)}
	}
	return results, nil
}

func toResults(bs []BackupResult) []Result {
	out := make([]Result, 0, len(bs))
	for _, b := range bs {
		out = append(out, Result{Service: b.Service, OK: b.OK, Error: b.Error})
	}
	return out
}

func (o *Ops) backupOne(ctx context.Context, svc string) BackupResult {
	cfg := o.Config
	res := BackupResult{Service: svc, BackupID: newID("bak"), RecoverySetID: newID("rec")}
	backupDir := filepath.Join(cfg.BackupDir, res.BackupID)
	if err := ensureDir(backupDir); err != nil {
		res.Error = fmt.Sprintf("backup dir: %v", err)
		return res
	}
	now := time.Now().UTC()

	// 1. Run the module backup hook (only if the module declares it).
	step := initstate.Step{ModuleID: svc, Operation: "backup", StartedAt: now}
	if o.moduleDeclares(svc, "backup") {
		rr, rerr := o.runModule(ctx, svc, "backup", []string{
			"SERVERCLI_BACKUP_ID=" + res.BackupID,
			"SERVERCLI_RECOVERY_SET_ID=" + res.RecoverySetID,
			"SERVERCLI_BACKUP_DIR=" + backupDir,
		})
		if rr != nil {
			step.CompletedAt = rr.CompletedAt
			step.InputDigest = rr.Digest
		}
		if rerr != nil {
			step.Status = initstate.StepFailed
			step.ErrorType = initstate.ErrTypeModule
			o.record(ctx, step)
			res.Error = fmt.Sprintf("backup hook: %v", rerr)
			return res
		}
		if rr.ExitCode != 0 {
			step.Status = initstate.StepFailed
			step.ErrorType = initstate.ErrTypeModule
			o.record(ctx, step)
			res.Error = fmt.Sprintf("backup hook: exit code %d", rr.ExitCode)
			return res
		}
		step.Status = initstate.StepSucceeded
		o.record(ctx, step)
	}

	// 2. Collect file digests.
	files, err := collectFiles(backupDir)
	if err != nil {
		res.Error = fmt.Sprintf("collect files: %v", err)
		return res
	}

	// 3. Build and sign the manifest.
	man := &BackupManifest{
		SchemaVersion:   ManifestSchemaVersion,
		BackupID:        res.BackupID,
		RecoverySetID:   res.RecoverySetID,
		Service:         svc,
		Node:            cfg.Node,
		Environment:     cfg.Environment,
		AppVersion:      o.moduleVersion(svc),
		DBSchemaVersion: "",
		CreatedAt:       now,
		Files:           files,
		Dependencies:    o.moduleDeps(svc),
		SigningKeyID:    cfg.SigningKeyID,
	}
	if cfg.SigningKey != nil {
		if err := man.Sign(cfg.SigningKey); err != nil {
			res.Error = fmt.Sprintf("sign manifest: %v", err)
			return res
		}
	}

	// 4. Local completion conditions: digest verification + read-back
	//    verification of every file.
	if err := verifyFiles(files, backupDir); err != nil {
		res.Error = err.Error()
		return res
	}

	// 5. Remote upload (adapter; default no-op). The Control Plane being
	//    offline never blocks a local backup.
	for _, bf := range files {
		data, err := os.ReadFile(filepath.Join(backupDir, filepath.FromSlash(bf.Path)))
		if err != nil {
			res.Error = fmt.Sprintf("read for upload %s: %v", bf.Path, err)
			return res
		}
		if err := cfg.Uploader.Upload(ctx, "servercli/backups/"+res.BackupID+"/"+bf.Path, data); err != nil {
			res.Error = fmt.Sprintf("upload %s: %v", bf.Path, err)
			return res
		}
	}
	rawMan, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		res.Error = fmt.Sprintf("marshal manifest: %v", err)
		return res
	}
	if err := cfg.Uploader.Upload(ctx, "servercli/backups/"+res.BackupID+"/"+manifestFilename, rawMan); err != nil {
		res.Error = fmt.Sprintf("upload manifest: %v", err)
		return res
	}

	// 6. Persist the signed manifest locally.
	manifestPath := filepath.Join(backupDir, manifestFilename)
	if err := os.WriteFile(manifestPath, rawMan, 0o600); err != nil {
		res.Error = fmt.Sprintf("write manifest: %v", err)
		return res
	}
	res.ManifestPath = manifestPath
	res.OK = true
	return res
}

// FindBackup locates the on-disk backup directory and manifest for a
// backup_id or recovery_set_id.
func (o *Ops) FindBackup(service, backupID string) (string, *BackupManifest, error) {
	root := o.Config.BackupDir
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, ErrBackupNotFound
		}
		return "", nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mp := filepath.Join(root, e.Name(), manifestFilename)
		raw, err := os.ReadFile(mp)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			continue
		}
		var m BackupManifest
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if m.BackupID != backupID && m.RecoverySetID != backupID {
			continue
		}
		if service != "" && m.Service != service {
			continue
		}
		return filepath.Join(root, e.Name()), &m, nil
	}
	return "", nil, ErrBackupNotFound
}
