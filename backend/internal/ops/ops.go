// Package ops implements the unified servercli ops surface: update, backup
// and restore for owned services.
//
// Guarantees:
//   - every operation is gated by the ownership package: only owner==servercli
//     may install/update/backup/restore, and adopting blocks all ops;
//   - service-level flock guarantees one service never runs two ops at once
//     (update/backup/restore/adopt share /run/servercli/operations/svc-*.lock);
//   - per-item failures never stop the remaining services; the caller maps the
//     aggregated failure to the stable partial-success exit code;
//   - backups never depend on the Control Plane: the Control Plane can be
//     offline and local backups still complete. Remote upload is an explicit
//     adapter (Uploader); production must implement an OSS/S3-compatible
//     adapter. This package provides the local digest + read-back verification
//     that must pass before a backup is reported complete;
//   - restore requires an explicit backup_id / recovery_set_id and explicit
//     confirmation (--yes or interactive TTY). Normal installs and empty
//     directories never auto-restore "latest";
//   - legacy backups are recognized read-only and are explicitly marked as
//     lacking metadata and verification; they can never masquerade as a
//     verified new-format backup.
package ops

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"servercli/internal/bootstrap"
	"servercli/internal/initstate"
	"servercli/internal/modman"
	"servercli/internal/modules"
	"servercli/internal/ownership"
)

// ModuleRunner is the narrow runner dependency (modman.Runner satisfies it).
type ModuleRunner interface {
	Run(ctx context.Context, opts modman.RunOptions) (*modman.RunResult, error)
}

// ModuleRegistry is the narrow registry dependency (modman.DepGraph satisfies
// it).
type ModuleRegistry interface {
	Ordered() ([]string, error)
	Module(id string) (*modman.ModuleManifest, bool)
}

// StepRecorder records module lifecycle steps into the init state. The default
// implementation writes through initstate.Store.
type StepRecorder interface {
	Record(ctx context.Context, step initstate.Step) error
}

// Config carries the operational context and defaults for Ops.
type Config struct {
	Environment  string
	Node         string
	ModulesDir   string
	RunDir       string // /run/servercli/bootstrap
	LockDir      string // /run/servercli/operations (ops + runner locks)
	BackupDir    string // /var/lib/servercli/backups
	StatePath    string // init state file for step recording
	Log          *slog.Logger
	Timeout      time.Duration
	SigningKey   ed25519.PrivateKey // injected backup signing key (nil disables signing)
	SigningKeyID string
	VerifyKeyPEM []byte // PEM/raw Ed25519 public key used to verify backups on restore
	Uploader     Uploader
	// Inventory is the decrypted cluster inventory used to resolve module
	// config; Secrets is the Bootstrap Store used to resolve module secret
	// fields. Both are optional; when nil the module runs without inputs.
	Inventory *bootstrap.Inventory
	Secrets   modules.SecretReader
	// ReleaseCompat + CurrentSchemaVersion enforce the Release Manifest
	// schema compatibility window on update. Both empty/nil disables the gate
	// (local/single-node updates without a release manifest).
	ReleaseCompat        *bootstrap.SchemaCompat
	CurrentSchemaVersion string
}

// RunOpts carries per-call options.
type RunOpts struct {
	Confirm bool      // restore: --yes given explicitly
	In      io.Reader // restore: interactive confirmation source (TTY)
	Out     io.Writer
	Err     io.Writer
}

// Ops executes update/backup/restore against modules, gated by ownership.
type Ops struct {
	Ownership *ownership.Store
	Registry  ModuleRegistry
	Runner    ModuleRunner
	Recorder  StepRecorder
	Config    Config
}

// Sentinel errors.
var (
	ErrRequireExplicitID = errors.New("ops: restore requires an explicit backup_id or recovery_set_id")
	ErrRequireConfirm    = errors.New("ops: restore is a high-risk operation; requires --yes or interactive confirmation")
	ErrBackupNotFound    = errors.New("ops: backup not found")
	ErrUnverified        = errors.New("ops: backup manifest is unsigned or unverifiable")
	ErrNoVerifyKey       = errors.New("ops: no verification key configured for restore")
	ErrLegacyBackup      = errors.New("ops: legacy backup cannot be restored as a verified backup")
)

// Result is the outcome of updating one service.
type Result struct {
	Service string `json:"service"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Version string `json:"version,omitempty"`
}

// AggregateError is returned when at least one service failed; the CLI maps it
// to the stable partial-success exit code while the per-service results carry
// the details.
type AggregateError struct {
	Total    int      `json:"total"`
	Failures []Result `json:"failures"`
}

func (e *AggregateError) Error() string {
	return fmt.Sprintf("ops: %d of %d services failed", len(e.Failures), e.Total)
}

// initStateRecorder records steps through initstate.Store at StatePath.
type initStateRecorder struct {
	path string
	log  *slog.Logger
}

func (r *initStateRecorder) Record(ctx context.Context, step initstate.Step) error {
	st, err := initstate.Open(r.path)
	if err != nil {
		return fmt.Errorf("ops: open init state: %w", err)
	}
	defer st.Close()
	st.State().UpsertStep(step)
	if err := st.Save(); err != nil {
		return fmt.Errorf("ops: save init state: %w", err)
	}
	return nil
}

// New builds an Ops with production defaults. Ownership must be Loaded before
// use. A nil Runner/Registry can be injected later; without a Registry the
// "empty service list = all services" fallback is the ownership store.
func New(store *ownership.Store, cfg Config) *Ops {
	if store == nil {
		store = ownership.NewStore(ownership.DefaultStatePath)
	}
	if cfg.RunDir == "" {
		cfg.RunDir = bootstrap.DirRunBootstrap
	}
	if cfg.LockDir == "" {
		cfg.LockDir = bootstrap.DirRunOperations
	}
	if cfg.BackupDir == "" {
		cfg.BackupDir = bootstrap.DirVarBackups
	}
	if cfg.StatePath == "" {
		cfg.StatePath = bootstrap.FileStateJSON
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Uploader == nil {
		cfg.Uploader = NoopUploader{}
	}
	o := &Ops{Ownership: store, Config: cfg}
	if o.Recorder == nil {
		o.Recorder = &initStateRecorder{path: cfg.StatePath, log: cfg.Log}
	}
	return o
}

// record best-effort records a lifecycle step; recording failures are logged,
// not fatal to the operation itself.
func (o *Ops) record(ctx context.Context, step initstate.Step) {
	if o.Recorder == nil {
		return
	}
	if err := o.Recorder.Record(ctx, step); err != nil {
		o.Config.Log.Warn("ops: record step failed", "module", step.ModuleID, "err", err)
	}
}

// allServices resolves the "empty list = all services" semantics: registry
// dependency order when a registry is available, otherwise the sorted service
// set from the ownership store.
func (o *Ops) allServices() ([]string, error) {
	if o.Registry != nil {
		ids, err := o.Registry.Ordered()
		if err != nil {
			return nil, fmt.Errorf("ops: registry order: %w", err)
		}
		if len(ids) > 0 {
			return ids, nil
		}
	}
	return o.Ownership.Services(), nil
}

// moduleDeclares reports whether the registry says module declares op. When
// the registry is unavailable the runner is authoritative and ops attempts the
// operation.
func (o *Ops) moduleDeclares(service, op string) bool {
	if o.Registry == nil {
		return true
	}
	m, ok := o.Registry.Module(service)
	if !ok || m == nil {
		return true
	}
	_, ok = m.Operations[op]
	return ok
}

// moduleVersion returns the declared module version, if known.
func (o *Ops) moduleVersion(service string) string {
	if o.Registry == nil {
		return ""
	}
	m, ok := o.Registry.Module(service)
	if !ok || m == nil {
		return ""
	}
	return m.Version
}

// moduleDeps returns the declared dependencies, if known.
func (o *Ops) moduleDeps(service string) []string {
	if o.Registry == nil {
		return nil
	}
	m, ok := o.Registry.Module(service)
	if !ok || m == nil {
		return nil
	}
	return append([]string(nil), m.DependsOn...)
}

// runModule executes one fixed module operation through the injected runner.
// The runner gets its own lock subdirectory so the ops-level service lock and
// the module concurrency locks never conflict on the same flock file.
func (o *Ops) runModule(ctx context.Context, service, operation string, extraEnv []string) (*modman.RunResult, error) {
	cfg := o.Config
	env := []string{
		"SERVERCLI_ENVIRONMENT=" + cfg.Environment,
		"SERVERCLI_NODE=" + cfg.Node,
		"SERVERCLI_SERVICE=" + service,
	}
	env = append(env, extraEnv...)
	opts := modman.RunOptions{
		ModuleID:   service,
		Operation:  operation,
		ModulesDir: cfg.ModulesDir,
		RunDir:     cfg.RunDir,
		LockDir:    cfg.LockDir, // same lock namespace as init (single authority)
		Log:        cfg.Log,
		Timeout:    cfg.Timeout,
		Env:        env,
	}
	// Resolve per-module config/secrets from the inventory + bootstrap store.
	if cfg.Inventory != nil && cfg.Secrets != nil && o.Registry != nil {
		if mod, ok := o.Registry.Module(service); ok && mod != nil {
			cfgMap, secMap, err := modules.ResolveModuleInputs(mod, cfg.Inventory, cfg.Secrets, "")
			if err == nil {
				opts.Config = cfgMap
				opts.Secrets = secMap
			} else {
				cfg.Log.Warn("ops: resolve module inputs failed", "module", service, "err", err)
			}
		}
	}
	return o.Runner.Run(ctx, opts)
}

// Update installs/verifies every requested service in sequence. An empty
// service list means all services. A single failing service never stops the
// rest; a non-nil *AggregateError is returned when any service failed so the
// caller can map it to the partial-success exit code.
func (o *Ops) Update(ctx context.Context, services []string, opts RunOpts) ([]Result, error) {
	if err := o.checkSchemaCompat(); err != nil {
		return nil, err
	}
	if len(services) == 0 {
		all, err := o.allServices()
		if err != nil {
			return nil, err
		}
		services = all
	}
	results := make([]Result, 0, len(services))
	for _, svc := range services {
		results = append(results, o.updateOne(ctx, svc))
	}
	var failed []Result
	for _, r := range results {
		if !r.OK {
			failed = append(failed, r)
		}
	}
	if len(failed) > 0 {
		return results, &AggregateError{Total: len(results), Failures: failed}
	}
	return results, nil
}

// checkSchemaCompat enforces the Release Manifest schema compatibility window
// and the irreversible-migration gates (maintenance mode + pre-update backup)
// before any update runs. It is a no-op when no release manifest is wired.
func (o *Ops) checkSchemaCompat() error {
	c := o.Config
	if c.ReleaseCompat == nil || c.CurrentSchemaVersion == "" {
		return nil
	}
	if cmpSemver(c.CurrentSchemaVersion, c.ReleaseCompat.MinSchemaVersion) < 0 {
		return fmt.Errorf("ops: current schema %s is below the compatible minimum %s of the target release",
			c.CurrentSchemaVersion, c.ReleaseCompat.MinSchemaVersion)
	}
	if c.ReleaseCompat.MaxSchemaVersion != "" && cmpSemver(c.CurrentSchemaVersion, c.ReleaseCompat.MaxSchemaVersion) > 0 {
		return fmt.Errorf("ops: current schema %s exceeds the target release maximum %s",
			c.CurrentSchemaVersion, c.ReleaseCompat.MaxSchemaVersion)
	}
	if !c.ReleaseCompat.Reversible {
		if c.Inventory == nil || !c.Inventory.Update.Maintenance {
			return errors.New("ops: irreversible migration requires maintenance mode / write freeze (inventory.update.maintenance=true)")
		}
	}
	return nil
}

// requiresPreUpdateBackup reports whether the target migration is irreversible
// (a pre-update backup is then mandatory).
func (o *Ops) requiresPreUpdateBackup() bool {
	return o.Config.ReleaseCompat != nil && !o.Config.ReleaseCompat.Reversible
}

func (o *Ops) updateOne(ctx context.Context, svc string) Result {
	res := Result{Service: svc, Version: o.moduleVersion(svc)}
	cfg := o.Config
	if err := o.Ownership.CanOperate(cfg.Environment, cfg.Node, svc); err != nil {
		res.Error = err.Error()
		return res
	}
	if o.requiresPreUpdateBackup() {
		br := o.backupOne(ctx, svc)
		if br.Error != "" {
			res.Error = "pre-update backup: " + br.Error
			return res
		}
	}
	unlock, err := o.Ownership.Lock(cfg.Environment, cfg.Node, svc, "update")
	if err != nil {
		res.Error = "lock: " + err.Error()
		return res
	}
	defer unlock()

	now := time.Now().UTC()
	// install is mandatory; verify is run when the module declares it.
	ops := []string{"install"}
	if o.moduleDeclares(svc, "verify") {
		ops = append(ops, "verify")
	}
	for _, op := range ops {
		step := initstate.Step{ModuleID: svc, Operation: op, StartedAt: now}
		rr, rerr := o.runModule(ctx, svc, op, nil)
		if rr != nil {
			step.CompletedAt = rr.CompletedAt
			step.InputDigest = rr.Digest
			step.Attempt = 1
		}
		if rerr != nil {
			step.Status = initstate.StepFailed
			step.ErrorType = initstate.ErrTypeModule
			step.Retryable = true
			o.record(ctx, step)
			res.Error = fmt.Sprintf("%s: %v", op, rerr)
			return res
		}
		if rr.ExitCode != 0 {
			step.Status = initstate.StepFailed
			step.ErrorType = initstate.ErrTypeModule
			step.Retryable = true
			o.record(ctx, step)
			res.Error = fmt.Sprintf("%s: exit code %d", op, rr.ExitCode)
			return res
		}
		step.Status = initstate.StepSucceeded
		o.record(ctx, step)
	}
	res.OK = true
	return res
}

// sanitizeArg rejects empty/flag-looking service names.
func sanitizeArg(service string) error {
	if service == "" || strings.HasPrefix(service, "-") {
		return fmt.Errorf("ops: invalid service name %q", service)
	}
	return nil
}

// ensureDir creates a 0700 directory.
func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

// cmpSemver compares two dotted numeric versions (e.g. "1.2.3"); returns -1/0/1.
func cmpSemver(a, b string) int {
	as, bs := splitVer(a), splitVer(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := 0, 0
		if i < len(as) {
			fmt.Sscanf(as[i], "%d", &av)
		}
		if i < len(bs) {
			fmt.Sscanf(bs[i], "%d", &bv)
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

func splitVer(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' })
}
