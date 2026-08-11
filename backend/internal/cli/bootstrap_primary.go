package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"io"
	"log/slog"
	"runtime"

	"servercli/internal/bootstrap"
	"servercli/internal/bootstrapv2"
	"servercli/internal/modman"
	"servercli/internal/ops"
	"servercli/internal/oss"
)

// cmdBootstrap dispatches `servercli bootstrap ...`.
func (a *app) cmdBootstrap() int {
	if len(a.args) == 0 {
		a.err("bootstrap: expected `primary`")
		return bootstrap.ExitUsage
	}
	switch a.args[0] {
	case "primary":
		a.args = a.args[1:]
		return a.bootstrapPrimary()
	case "help", "--help", "-h":
		a.err(`bootstrap primary plan|apply|status|resume|repair|recover`)
		return bootstrap.ExitUsage
	default:
		a.err("bootstrap: unknown subcommand %q (expected `primary`)", a.args[0])
		return bootstrap.ExitUsage
	}
}

// bootstrapPrimary runs the OSS-first primary bootstrap flow. It is the first
// thing a fresh primary runs: read bootstrap.env, download + verify the
// release from OSS, install servercli, then install the foundation modules.
func (a *app) bootstrapPrimary() int {
	if len(a.args) == 0 {
		a.err("bootstrap primary: expected plan|apply|status|resume|repair|recover")
		return bootstrap.ExitUsage
	}
	sub := a.args[0]
	a.args = a.args[1:]
	if len(a.args) != 0 {
		a.err("bootstrap primary: unexpected arguments: %s", strings.Join(a.args, " "))
		return bootstrap.ExitUsage
	}

	b, err := a.newPrimaryBootstrap()
	if err != nil {
		a.err("bootstrap primary: %v", err)
		return bootstrap.ExitPreflight
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	switch sub {
	case "plan":
		plan, perr := b.Plan(ctx)
		if perr != nil {
			a.err("bootstrap primary plan: %v", perr)
			return mapBootstrapErr(perr)
		}
		return a.emit(plan)
	case "apply":
		if !a.yes && !isTTY(a.stdin) {
			a.err("bootstrap primary apply: non-interactive apply requires --yes")
			return bootstrap.ExitUsage
		}
		if aerr := b.Apply(ctx); aerr != nil {
			a.err("bootstrap primary apply: %v", aerr)
			return mapBootstrapErr(aerr)
		}
		return a.emit(map[string]string{"status": bootstrapv2.StateReady})
	case "resume":
		if rerr := b.Resume(ctx); rerr != nil {
			a.err("bootstrap primary resume: %v", rerr)
			return mapBootstrapErr(rerr)
		}
		return a.emit(map[string]string{"status": bootstrapv2.StateReady})
	case "status":
		st, serr := b.Status(ctx)
		if serr != nil {
			a.err("bootstrap primary status: %v", serr)
			return bootstrap.ExitModule
		}
		return a.emit(st)
	case "repair":
		if rerr := b.Repair(ctx); rerr != nil {
			a.err("bootstrap primary repair: %v", rerr)
			return mapBootstrapErr(rerr)
		}
		return a.emit(map[string]string{"status": "repaired"})
	case "recover":
		plan, perr := b.PlanRecovery(ctx)
		if perr != nil {
			a.err("bootstrap primary recover: %v", perr)
			return mapBootstrapErr(perr)
		}
		a.out("recovery plan: node=%s pointer=%s manifest=%s", plan.NodeID, plan.PointerKey, plan.BackupManifestKey)
		if !a.yes && !isTTY(a.stdin) {
			a.err("bootstrap primary recover: non-interactive recover requires --yes")
			return bootstrap.ExitUsage
		}
		if rerr := b.Recover(ctx); rerr != nil {
			a.err("bootstrap primary recover: %v", rerr)
			return mapBootstrapErr(rerr)
		}
		return a.emit(map[string]string{"status": "recovered"})
	default:
		a.err("bootstrap primary: unknown subcommand %q", sub)
		return bootstrap.ExitUsage
	}
}

// newPrimaryBootstrap constructs the bootstrap executor with production hooks:
// OSS provider from bootstrap.env, module runner for foundation install, and
// an installer that places the downloaded servercli binary.
func (a *app) newPrimaryBootstrap() (*bootstrapv2.Bootstrap, error) {
	envPath := a.bootstrapEnvPath()
	env, err := bootstrapv2.LoadBootstrapEnv(envPath)
	if err != nil {
		return nil, err
	}
	if err := env.Validate(); err != nil {
		return nil, err
	}
	if env.Role != "" && env.Role != "primary" {
		return nil, fmt.Errorf("bootstrap env BOOTSTRAP_ROLE=%q; this command is for the primary only", env.Role)
	}

	provider, err := oss.New(env.ToOSSConfig())
	if err != nil {
		return nil, fmt.Errorf("build OSS provider: %w", err)
	}

	// Installer: copy the verified servercli binary into the fixed bin dirs.
	installer := func(ctx context.Context, manifest *bootstrap.ReleaseManifest, artifactDir string) error {
		return installServerCLIFromArtifacts(ctx, manifest, artifactDir)
	}

	// FoundationRunner: run each foundation module through modman.
	foundationRunner := func(ctx context.Context, run bootstrapv2.FoundationRun) error {
		return a.runFoundationModule(ctx, run)
	}

	return bootstrapv2.New(bootstrapv2.BootstrapOptions{
		EnvPath:          envPath,
		OSS:              provider,
		Installer:        installer,
		FoundationRunner: foundationRunner,
		StatePath:        a.statePath,
		Log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	}), nil
}

func (a *app) bootstrapEnvPath() string {
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "--bootstrap-env=") {
			return strings.TrimPrefix(arg, "--bootstrap-env=")
		}
	}
	return "/root/servercli-bootstrap/bootstrap.env"
}

// installServerCLIFromArtifacts places the downloaded servercli binaries from
// the verified artifact directory into standard bin locations. Only files
// declared in the release manifest are installed; the artifact directory is
// root-owned 0700 with 0600 files.
func installServerCLIFromArtifacts(ctx context.Context, manifest *bootstrap.ReleaseManifest, artifactDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Select the servercli binary artifact for this platform. The generic
	// `servercli` artifact (bin/servercli) is accepted; platform-specific
	// names like servercli-linux-amd64 are preferred when present.
	candidate := ""
	for _, art := range manifest.Artifacts {
		if art.Kind != "binary" {
			continue
		}
		base := filepath.Base(filepath.FromSlash(art.Path))
		if strings.Contains(art.Path, "control-plane") || strings.Contains(art.Path, "node-agent") {
			continue
		}
		if base != "servercli" && !strings.HasPrefix(base, "servercli-") {
			continue
		}
		if base == "servercli" && candidate == "" {
			candidate = art.Path
			continue
		}
		if strings.HasPrefix(base, "servercli-") && platformMatches(base, runtime.GOOS, runtime.GOARCH) {
			candidate = art.Path
			break
		}
	}
	if candidate == "" {
		return fmt.Errorf("install servercli: no servercli binary artifact for %s/%s in release manifest", runtime.GOOS, runtime.GOARCH)
	}
	src := filepath.Join(artifactDir, filepath.FromSlash(candidate))
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("install servercli: missing artifact %s: %w", candidate, err)
	}
	installed := 0
	for _, binDir := range []string{"/usr/local/bin", "/opt/servercli/bin", "/home/inori/serverCli/bin"} {
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			continue
		}
		dst := filepath.Join(binDir, "servercli")
		if err := installBinarySafely(src, dst, 0o755); err != nil {
			return fmt.Errorf("install servercli to %s: %w", dst, err)
		}
		installed++
	}
	if installed == 0 {
		return fmt.Errorf("install servercli: no writable bin directory")
	}
	return nil
}

// platformMatches reports whether an artifact basename like
// "servercli-linux-amd64" matches the target os/arch.
func platformMatches(base, osName, arch string) bool {
	lower := strings.ToLower(base)
	return strings.Contains(lower, strings.ToLower(osName)) && strings.Contains(lower, strings.ToLower(arch))
}

// runFoundationModule executes one foundation module lifecycle operation
// through the module runner. It never passes secrets via argv; config comes
// from module.yaml and the runner's secret reader.
func (a *app) runFoundationModule(ctx context.Context, run bootstrapv2.FoundationRun) error {
	runner := modman.NewRunner(a.modulesDir, a.runDir, a.lockDir, nil, nil)
	_, err := runner.Run(ctx, modman.RunOptions{
		ModuleID:   run.ModuleID,
		Operation:  run.Operation,
		ModulesDir: a.modulesDir,
		RunDir:     a.runDir,
		LockDir:    a.lockDir,
		Timeout:    30 * time.Minute,
	})
	if err != nil {
		if errors.Is(err, modman.ErrForbidden) {
			return fmt.Errorf("foundation module %s: operation %s not permitted", run.ModuleID, run.Operation)
		}
		return err
	}
	return nil
}

func mapBootstrapErr(err error) int {
	switch {
	case errors.Is(err, oss.ErrNotFound):
		return bootstrap.ExitNetwork
	default:
		msg := err.Error()
		switch {
		case strings.Contains(msg, "preflight"):
			return bootstrap.ExitPreflight
		case strings.Contains(msg, "sha256"), strings.Contains(msg, "verify"), strings.Contains(msg, "signature"):
			return bootstrap.ExitSignature
		case strings.Contains(msg, "network"), strings.Contains(msg, "timeout"), strings.Contains(msg, "reachability"):
			return bootstrap.ExitNetwork
		case strings.Contains(msg, "module"):
			return bootstrap.ExitModule
		case strings.Contains(msg, "blocked"), strings.Contains(msg, "owner"), strings.Contains(msg, "concurrent"):
			return bootstrap.ExitBlocked
		default:
			return bootstrap.ExitModule
		}
	}
}

// installBinarySafely installs src to dst with the given mode without
// following symlinks. It refuses to write through a symlinked destination or
// into a symlinked parent directory, and writes via a temp file in the target
// directory (same filesystem) so the final rename is atomic. This prevents a
// local attacker from pre-placing a symlink that a root install would follow.
func installBinarySafely(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	dir := filepath.Dir(dst)
	if err := ensureNoSymlinkPath(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Re-check after MkdirAll in case a symlink appeared in the path.
	if err := ensureNoSymlinkPath(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".servercli-install-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	fi, err := os.Lstat(dst)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		os.Remove(dst)
		return fmt.Errorf("install: destination %s is a symlink", dst)
	}
	return nil
}

// ensureNoSymlinkPath rejects symlinks in the writable install path. It
// ascends from dir to the nearest existing ancestor: components the installer
// itself creates (MkdirAll) are regular directories by construction, and any
// pre-existing component in the writable region that is a symlink is rejected.
// System symlinks above the nearest existing ancestor (e.g. /var on macOS)
// are intentionally not traversed.
func ensureNoSymlinkPath(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	rest := abs
	for {
		fi, lerr := os.Lstat(rest)
		if lerr == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("install: refusing to write through symlinked directory %s", rest)
			}
			// Nearest existing regular directory: stop ascending.
			return nil
		}
		if !os.IsNotExist(lerr) {
			return lerr
		}
		parent := filepath.Dir(rest)
		if parent == rest {
			return nil
		}
		rest = parent
	}
}

// opsUploader returns a real OSS-backed uploader when a root-only
// bootstrap.env or OSS env vars are present, otherwise the Noop uploader so
// backups never depend on the Control Plane being online.
func (a *app) opsUploader() ops.Uploader {
	cfg, err := a.ossConfigFromEnv()
	if err != nil || cfg.Bucket == "" {
		return ops.NoopUploader{}
	}
	provider, err := oss.New(cfg)
	if err != nil {
		return ops.NoopUploader{}
	}
	return ops.OSSUploader{Provider: provider, BaseKey: "servercli/backups"}
}

// ossConfigFromEnv builds an oss.Config from a root-only bootstrap.env
// (preferred) or OSS_* environment variables. Secrets come from files/env
// only, never argv.
func (a *app) ossConfigFromEnv() (oss.Config, error) {
	if envPath := a.bootstrapEnvPath(); envPath != "" {
		if env, err := bootstrapv2.LoadBootstrapEnv(envPath); err == nil {
			if env.OSSBucket != "" && env.OSSEndpoint != "" {
				cfg := env.ToOSSConfig()
				cfg.AccessKeyID = env.OSSAccessKeyID
				cfg.AccessKeySecret = env.OSSAccessKeySecret
				return cfg, nil
			}
		}
	}
	cfg := oss.Config{
		Endpoint:         os.Getenv("OSS_ENDPOINT"),
		InternalEndpoint: os.Getenv("OSS_INTERNAL_ENDPOINT"),
		Bucket:           os.Getenv("OSS_BUCKET"),
		Region:           os.Getenv("OSS_REGION"),
		AccessKeyID:      os.Getenv("OSS_ACCESS_KEY_ID"),
		AccessKeySecret:  os.Getenv("OSS_ACCESS_KEY_SECRET"),
		PreferInternal:   os.Getenv("OSS_PREFER_INTERNAL") == "1" || os.Getenv("OSS_PREFER_INTERNAL") == "true",
	}
	cfg.Normalize()
	if cfg.Bucket == "" || cfg.Endpoint == "" {
		return oss.Config{}, errors.New("OSS not configured")
	}
	return cfg, nil
}
