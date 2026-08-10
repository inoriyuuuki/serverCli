// Package cli implements the servercli command surface: the database-free
// init wizard/state machine, bundle import, module provisioning and the
// update/backup/restore compatibility operations.
//
// The CLI never connects to PostgreSQL, Docker, Gitea or a Control Plane on
// startup, and never accepts secrets through argv: secrets flow only through
// the encrypted Bootstrap Store, /run 0600 files or declared env fields.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"servercli/internal/bootstrap"
	"servercli/internal/bundle"
	"servercli/internal/initstate"
	"servercli/internal/modman"
	"servercli/internal/modules"
	"servercli/internal/ops"
	"servercli/internal/ownership"
	"servercli/internal/secretstore"
)

// VersionInfo carries build-time version metadata.
type VersionInfo struct {
	Version string
	Build   string
	Commit  string
}

const defaultPubKeyFile = "/etc/servercli/keys/release.pub.pem"

type app struct {
	args                 []string
	stdout               io.Writer
	stderr               io.Writer
	stdin                io.Reader
	vi                   VersionInfo
	jsonOut              bool
	yes                  bool
	env                  string
	node                 string
	bundleURL            string
	ageKeyFile           string
	pubKeyFile           string
	modulesDir           string
	statePath            string
	secretsPath          string
	keysDir              string
	runDir               string
	lockDir              string
	backupDir            string
	inventoryPath        string
	ownershipPath        string
	releaseManifestFile  string
	currentSchemaVersion string
}

func defaultApp() *app {
	return &app{
		modulesDir:           "modules",
		statePath:            bootstrap.FileStateJSON,
		secretsPath:          bootstrap.FileSecretsEnc,
		keysDir:              bootstrap.DirEtcKeys,
		runDir:               bootstrap.DirRunBootstrap,
		lockDir:              bootstrap.DirRunOperations,
		backupDir:            bootstrap.DirVarBackups,
		ageKeyFile:           bootstrap.FileBootstrapAgeKey,
		pubKeyFile:           defaultPubKeyFile,
		inventoryPath:        bootstrap.FileClusterYAML,
		ownershipPath:        ownership.DefaultStatePath,
		currentSchemaVersion: "1.0",
	}
}

func (a *app) out(format string, v ...any) { fmt.Fprintf(a.stdout, format+"\n", v...) }
func (a *app) err(format string, v ...any) { fmt.Fprintf(a.stderr, format+"\n", v...) }

// Run dispatches a servercli invocation and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer, vi VersionInfo) int {
	a := defaultApp()
	a.stdout = stdout
	a.stderr = stderr
	a.stdin = os.Stdin
	a.vi = vi

	rest := consumeFlags(a, args)
	a.args = rest
	if len(a.args) == 0 {
		a.err("servercli: missing command (see `servercli help`)")
		return bootstrap.ExitUsage
	}
	cmd := a.args[0]
	a.args = a.args[1:]
	switch cmd {
	case "init":
		return a.cmdInit()
	case "config":
		return a.cmdConfig()
	case "modules":
		return a.cmdModules()
	case "ops":
		return a.cmdOps()
	case "version", "--version":
		a.out("%s (build %s, commit %s)", a.vi.Version, a.vi.Build, a.vi.Commit)
		return bootstrap.ExitOK
	case "help", "--help", "-h":
		a.printHelp()
		return bootstrap.ExitOK
	default:
		a.err("servercli: unknown command %q", cmd)
		return bootstrap.ExitUsage
	}
}

// consumeFlags extracts flags from anywhere in args (before or after the
// subcommand), returning the remaining positional tokens in order.
func consumeFlags(a *app, args []string) []string {
	var pos []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--output=json":
			a.jsonOut = true
		case arg == "--yes":
			a.yes = true
		case arg == "--":
			pos = append(pos, args[i+1:]...)
			return pos
		case strings.HasPrefix(arg, "--environment="):
			a.env = strings.TrimPrefix(arg, "--environment=")
		case strings.HasPrefix(arg, "--node-name="):
			a.node = strings.TrimPrefix(arg, "--node-name=")
		case strings.HasPrefix(arg, "--bundle-url="):
			a.bundleURL = strings.TrimPrefix(arg, "--bundle-url=")
		case strings.HasPrefix(arg, "--age-key-file="):
			a.ageKeyFile = strings.TrimPrefix(arg, "--age-key-file=")
		case strings.HasPrefix(arg, "--pubkey-file="):
			a.pubKeyFile = strings.TrimPrefix(arg, "--pubkey-file=")
		case strings.HasPrefix(arg, "--modules-dir="):
			a.modulesDir = strings.TrimPrefix(arg, "--modules-dir=")
		case strings.HasPrefix(arg, "--state-path="):
			a.statePath = strings.TrimPrefix(arg, "--state-path=")
		case strings.HasPrefix(arg, "--secrets-path="):
			a.secretsPath = strings.TrimPrefix(arg, "--secrets-path=")
		case strings.HasPrefix(arg, "--keys-dir="):
			a.keysDir = strings.TrimPrefix(arg, "--keys-dir=")
		case strings.HasPrefix(arg, "--run-dir="):
			a.runDir = strings.TrimPrefix(arg, "--run-dir=")
		case strings.HasPrefix(arg, "--lock-dir="):
			a.lockDir = strings.TrimPrefix(arg, "--lock-dir=")
		case strings.HasPrefix(arg, "--backup-dir="):
			a.backupDir = strings.TrimPrefix(arg, "--backup-dir=")
		case strings.HasPrefix(arg, "--inventory-path="):
			a.inventoryPath = strings.TrimPrefix(arg, "--inventory-path=")
		case strings.HasPrefix(arg, "--ownership-path="):
			a.ownershipPath = strings.TrimPrefix(arg, "--ownership-path=")
		case strings.HasPrefix(arg, "--release-manifest-file="):
			a.releaseManifestFile = strings.TrimPrefix(arg, "--release-manifest-file=")
		case strings.HasPrefix(arg, "--current-schema-version="):
			a.currentSchemaVersion = strings.TrimPrefix(arg, "--current-schema-version=")
		default:
			if strings.HasPrefix(arg, "-") && arg != "-" {
				a.err("servercli: unknown flag %q", arg)
				return nil
			}
			pos = append(pos, arg)
		}
	}
	return pos
}

func (a *app) printHelp() {
	a.out(`servercli — database-free bootstrap & ops CLI

Usage:
  servercli init                      interactive wizard
  servercli init plan                 plan without modifying the system
  servercli init apply                apply init (requires --yes in non-TTY)
  servercli init status               show init state
  servercli init resume               resume a failed init (same bundle+input)
  servercli init repair               repair ServerCLI-owned resources only
  servercli config import plan|apply  import an encrypted bundle
  servercli modules run --module <id> --operation <op> [--yes]
  servercli ops update|backup|restore|adopt [service...]
  servercli version

Flags:
  --environment=<env>  --node-name=<node>  --bundle-url=<url>
  --age-key-file=<path>  --pubkey-file=<path>  --yes  --output=json
  --modules-dir=<dir>  --state-path=<file>  --secrets-path=<file>
  --keys-dir=<dir>  --run-dir=<dir>  --lock-dir=<dir>  --backup-dir=<dir>
  --inventory-path=<file>  --ownership-path=<file>

Exit codes are stable and documented in doc/13_INIT_AND_BOOTSTRAP.md.
Secrets are never accepted through argv.`)
}

func (a *app) cmdInit() int {
	if len(a.args) == 0 {
		return a.wizard()
	}
	sub := a.args[0]
	a.args = a.args[1:]
	switch sub {
	case "plan":
		return a.initPlan()
	case "apply":
		return a.initApply(false)
	case "status":
		return a.initStatus()
	case "resume":
		return a.initApply(true)
	case "repair":
		return a.initRepair()
	default:
		a.err("servercli: unknown init subcommand %q", sub)
		return bootstrap.ExitUsage
	}
}

func (a *app) requireBundleInputs() error {
	if a.env == "" || a.node == "" {
		return errors.New("--environment and --node-name are required")
	}
	if a.bundleURL == "" {
		return errors.New("--bundle-url is required")
	}
	if a.ageKeyFile == "" {
		return errors.New("--age-key-file is required")
	}
	if _, err := os.Stat(a.pubKeyFile); err != nil {
		return fmt.Errorf("release public key %s not found (install it or pass --pubkey-file): %w", a.pubKeyFile, err)
	}
	return nil
}

func (a *app) bundleOpts() bundle.ImportOptions {
	return bundle.ImportOptions{
		Environment:      a.env,
		NodeName:         a.node,
		BundleURL:        a.bundleURL,
		AgeKeyFile:       a.ageKeyFile,
		PublicKeyFile:    a.pubKeyFile,
		AllowDevReplay:   false,
		BootstrapVersion: a.vi.Version,
		InventoryPath:    a.inventoryPath,
		SecretsPath:      a.secretsPath,
		MasterKeyPath:    filepath.Join(a.keysDir, "master.key"),
		RunDir:           a.runDir,
	}
}

func (a *app) bootstrapStore() (*secretstore.Store, error) {
	key, err := secretstore.LoadOrCreateMasterKey(filepath.Join(a.keysDir, "master.key"))
	if err != nil {
		return nil, err
	}
	return secretstore.OpenBootstrapStore(a.secretsPath, key)
}

func (a *app) loadInventory() (*bootstrap.Inventory, error) {
	raw, err := os.ReadFile(a.inventoryPath)
	if err != nil {
		return nil, err
	}
	var inv bootstrap.Inventory
	if err := yaml.Unmarshal(raw, &inv); err != nil {
		return nil, err
	}
	return &inv, nil
}

func (a *app) initPlan() int {
	if err := a.requireBundleInputs(); err != nil {
		a.err("init plan: %v", err)
		return bootstrap.ExitUsage
	}
	loaded, err := bundle.LoadBundle(context.Background(), a.bundleOpts())
	if err != nil {
		a.err("init plan: %v", err)
		return mapBundleErr(err)
	}
	reg, err := modules.NewRegistry(a.modulesDir)
	if err != nil {
		a.err("init plan: %v", err)
		return bootstrap.ExitPreflight
	}
	order, err := reg.Ordered(context.Background())
	if err != nil {
		a.err("init plan: %v", err)
		return bootstrap.ExitPreflight
	}
	return a.emit(struct {
		BundleID      string   `json:"bundle_id"`
		Environment   string   `json:"environment"`
		NodeName      string   `json:"node_name"`
		InputDigest   string   `json:"input_digest"`
		Modules       []string `json:"modules"`
		CoreReadyGate []string `json:"core_ready_gate"`
		WritesNothing bool     `json:"writes_nothing"`
	}{
		BundleID:      loaded.Manifest.BundleID,
		Environment:   loaded.Inventory.Environment,
		NodeName:      loaded.Inventory.Node.Name,
		InputDigest:   loaded.InputDigest,
		Modules:       order,
		CoreReadyGate: modules.CoreReadyGate(),
		WritesNothing: true,
	})
}

// initApply runs the foundation init sequence. resume=true only continues a
// failed/pending operation whose bundle_id and input digest match.
func (a *app) initApply(resume bool) int {
	if !a.yes && !isTTY(a.stdin) {
		a.err("init apply: non-interactive apply requires --yes")
		return bootstrap.ExitUsage
	}
	if err := a.requireBundleInputs(); err != nil {
		a.err("init apply: %v", err)
		return bootstrap.ExitUsage
	}
	ctx := context.Background()

	// Verify + decrypt in memory first (never preview unverified input).
	loaded, err := bundle.LoadBundle(ctx, a.bundleOpts())
	if err != nil {
		a.err("init apply: %v", err)
		return mapBundleErr(err)
	}

	st, err := initstate.Open(a.statePath)
	if err != nil {
		a.err("init apply: %v", err)
		if errors.Is(err, initstate.ErrConcurrent) || errors.Is(err, initstate.ErrCorrupt) {
			return bootstrap.ExitBlocked
		}
		return bootstrap.ExitModule
	}
	defer st.Close()
	state := st.State()

	if state.Overall == initstate.StateBlocked {
		a.err("init apply: init is blocked; resolve the blocking condition manually")
		return bootstrap.ExitBlocked
	}
	if state.Overall == initstate.StateNotInitialized {
		// First import: write inventory + secrets (Foundation step 1).
		store, serr := a.bootstrapStore()
		if serr != nil {
			a.err("init apply: %v", serr)
			return bootstrap.ExitModule
		}
		if _, ierr := bundle.ImportBundle(ctx, a.bundleOpts(), store, nil); ierr != nil {
			a.err("init apply: bundle import: %v", ierr)
			return mapBundleErr(ierr)
		}
		opID := fmt.Sprintf("init-%d", time.Now().UnixNano())
		ns := initstate.New(opID, loaded.Manifest.BundleID, loaded.InputDigest, loaded.Manifest.BundleVersion)
		*state = *ns
	} else if resume {
		if rerr := bundle.ResumeGuard(state, loaded.Manifest.BundleID, loaded.InputDigest); rerr != nil {
			a.err("init apply: %v (run `servercli config import plan/apply` to adopt a changed bundle)", rerr)
			return bootstrap.ExitBlocked
		}
	} else {
		// A non-first apply must match the recorded bundle_id AND input
		// digest; URL content changes are never silently accepted.
		if state.BundleID != "" && (state.BundleID != loaded.Manifest.BundleID || state.InputDigest != loaded.InputDigest) {
			a.err("init apply: bundle or input changed since the last run; use `servercli config import plan/apply`")
			return bootstrap.ExitBlocked
		}
	}
	// A crash may leave steps stuck in running; reconcile before continuing.
	if state.Overall == initstate.StateInitializing && len(state.Steps) > 0 {
		initstate.ReconcileAfterCrash(state)
		if serr := st.Save(); serr != nil {
			a.err("init apply: persist reconciled state: %v", serr)
			return bootstrap.ExitModule
		}
	}

	if err := state.SetOverall(initstate.StateInitializing); err != nil {
		a.err("init apply: %v", err)
		return bootstrap.ExitModule
	}
	if err := st.Save(); err != nil {
		a.err("init apply: %v", err)
		return bootstrap.ExitModule
	}

	inv, err := a.loadInventory()
	if err != nil {
		a.err("init apply: load inventory: %v", err)
		return bootstrap.ExitModule
	}

	// Preflight: OS/arch/connectivity/DNS/ownership.
	owners := &ownerResolver{store: ownership.NewStore(a.ownershipPath), env: a.env, node: a.node}
	owners.store.SetLockDir(a.lockDir)
	if lerr := owners.store.Load(); lerr != nil && !errors.Is(lerr, os.ErrNotExist) {
		a.err("init apply: ownership: %v", lerr)
		return bootstrap.ExitModule
	}
	pf := &modules.Preflight{
		ModulesDir:           a.modulesDir,
		Inventory:            inv,
		PlanOnly:             false,
		Adopt:                false,
		Config:               map[string]string{},
		Secrets:              map[string]string{},
		RunDir:               a.runDir,
		SkipModulePreflights: true, // module preflights run per-module before install
		Owners:               owners,
		Log:                  nil,
	}
	pfRes, perr := pf.Run(ctx)
	if perr != nil {
		a.err("init apply: preflight: %v", perr)
		return bootstrap.ExitPreflight
	}
	if pfRes.Blocked {
		a.err("init apply: preflight blocked")
		return bootstrap.ExitBlocked
	}
	if pfRes.Fatal {
		a.err("init apply: preflight failed")
		return bootstrap.ExitPreflight
	}

	store, err := a.bootstrapStore()
	if err != nil {
		a.err("init apply: %v", err)
		return bootstrap.ExitModule
	}
	secrets := map[string]string{}
	keys, _ := store.List()
	for _, k := range keys {
		if v, ok := store.Get(k); ok {
			secrets[k] = v
		}
	}
	reg, err := modules.NewRegistry(a.modulesDir)
	if err != nil {
		a.err("init apply: %v", err)
		return bootstrap.ExitPreflight
	}
	runner := modman.NewRunner(a.modulesDir, a.runDir, a.lockDir, nil, nil)

	succeeded := 0
	partial := false
	coreFailed := false
	order := modules.FoundationCoreOrder()
	coreGate := map[string]bool{}
	for _, id := range modules.CoreReadyGate() {
		coreGate[id] = true
	}
	for _, id := range order {
		step := state.Step(id)
		if step != nil && (step.Status == initstate.StepSucceeded || step.Status == initstate.StepSkipped) {
			succeeded++
			continue
		}
		mod, mok := reg.Module(id)
		if !mok || mod == nil {
			a.err("init apply: module %s missing from registry", id)
			return bootstrap.ExitPreflight
		}
		cfg, sec, rerr := modules.ResolveModuleInputs(mod, inv, store, state.OperationID)
		if rerr != nil {
			a.err("init apply: module %s inputs: %v", id, rerr)
			return bootstrap.ExitModule
		}
		inputDigest := modman.ComputeInputDigest(cfg, sec)
		expected := ""
		if step != nil && step.InputDigest != "" && step.Status == initstate.StepFailed && step.Retryable {
			// Resume must not proceed with changed inputs.
			if step.InputDigest != inputDigest {
				a.err("init apply: module %s input digest changed since last attempt; run `servercli config import plan/apply`", id)
				return bootstrap.ExitBlocked
			}
			expected = step.InputDigest
		}
		attempt := 1
		if step != nil {
			attempt = step.Attempt + 1
		}
		cur := initstate.Step{
			OperationID:   state.OperationID,
			ModuleID:      id,
			Operation:     "install",
			Attempt:       attempt,
			ModuleVersion: moduleVersion(reg, id),
			BundleID:      state.BundleID,
			InputDigest:   inputDigest,
			TargetVersion: state.TargetVersion,
			StartedAt:     time.Now().UTC(),
			Status:        initstate.StepRunning,
			Retryable:     true,
		}
		state.UpsertStep(cur)
		if serr := st.Save(); serr != nil {
			a.err("init apply: persist state: %v", serr)
			return bootstrap.ExitModule
		}

		baseOpts := modman.RunOptions{
			ModuleID:       id,
			Config:         cfg,
			Secrets:        sec,
			ModulesDir:     a.modulesDir,
			RunDir:         a.runDir,
			LockDir:        a.lockDir,
			Log:            nil,
			ExpectedDigest: expected,
		}
		// Per-module preflight, run right before install (deps are committed).
		if _, hasPre := mod.Operations["preflight"]; hasPre {
			po := baseOpts
			po.Operation = "preflight"
			if pres, perr := runner.Run(ctx, po); perr != nil || pres.ExitCode != 0 {
				cur.Status = initstate.StepBlocked
				cur.ErrorType = initstate.ErrTypePreflight
				cur.CompletedAt = time.Now().UTC()
				cur.HealthEvidence = truncate(runOut(pres), 200)
				state.UpsertStep(cur)
				_ = st.Save()
				a.err("init apply: module %s preflight failed", id)
				partial = true
				if coreGate[id] {
					coreFailed = true
				}
				break
			}
		}
		io := baseOpts
		io.Operation = "install"
		res, rerr := runner.Run(ctx, io)
		if rerr != nil || res == nil || res.ExitCode != 0 {
			cur.Status = initstate.StepFailed
			cur.ErrorType = initstate.ErrTypeModule
			cur.CompletedAt = time.Now().UTC()
			cur.HealthEvidence = truncate(runOut(res), 200)
			state.UpsertStep(cur)
			_ = st.Save()
			if rerr != nil {
				a.err("init apply: module %s install failed: %v", id, rerr)
			} else {
				a.err("init apply: module %s install exited %d", id, res.ExitCode)
			}
			partial = true
			if coreGate[id] {
				coreFailed = true
			}
			break
		}
		// Verify step (fixed entrypoint) after install; any error is a failure.
		if _, hasVer := mod.Operations["verify"]; hasVer {
			vo := baseOpts
			vo.Operation = "verify"
			if vres, verr := runner.Run(ctx, vo); verr != nil || vres == nil || vres.ExitCode != 0 {
				cur.Status = initstate.StepFailed
				cur.ErrorType = initstate.ErrTypeModule
				cur.CompletedAt = time.Now().UTC()
				cur.HealthEvidence = truncate(runOut(vres), 200)
				state.UpsertStep(cur)
				_ = st.Save()
				a.err("init apply: module %s verify failed", id)
				partial = true
				if coreGate[id] {
					coreFailed = true
				}
				break
			}
		}
		cur.Status = initstate.StepSucceeded
		cur.CompletedAt = time.Now().UTC()
		cur.LastCommitPoint = commitPointFor(id)
		cur.HealthEvidence = "verify-ok"
		state.UpsertStep(cur)
		state.SetCommitPoint(id, cur.LastCommitPoint)
		if serr := st.Save(); serr != nil {
			a.err("init apply: persist state: %v", serr)
			return bootstrap.ExitModule
		}
		// Record ServerCLI ownership so ops/repair gates are authoritative.
		ow := ownership.Ownership{
			Environment:      a.env,
			Node:             a.node,
			Service:          id,
			Owner:            ownership.OwnerServerCLI,
			ConfigDigest:     inputDigest,
			AdoptCompletedAt: time.Now().UTC(),
		}
		if oerr := owners.store.Set(a.env, a.node, id, ow); oerr != nil {
			a.err("init apply: record ownership %s: %v", id, oerr)
			return bootstrap.ExitModule
		}
		if oerr := owners.store.Save(); oerr != nil {
			a.err("init apply: persist ownership %s: %v", id, oerr)
			return bootstrap.ExitModule
		}
		succeeded++
		a.out("init: %s committed (%s)", id, cur.LastCommitPoint)
	}

	if partial {
		// Non-gate module failures (e.g. gitea) keep core_ready/degraded.
		if !coreFailed {
			_ = state.SetOverall(initstate.StateCoreReady)
			a.out("init: core modules ready; overall=%s (partial: %d succeeded)", state.Overall, succeeded)
		} else {
			_ = state.SetOverall(initstate.StateFailed)
			a.out("init: core module failed; overall=%s (partial: %d succeeded)", state.Overall, succeeded)
		}
		_ = st.Save()
		if succeeded == 0 {
			return bootstrap.ExitModule
		}
		return bootstrap.ExitPartial
	}

	// All modules succeeded: core_ready then ready (foundation complete).
	_ = state.SetOverall(initstate.StateCoreReady)
	if state.Step("gitea") != nil && state.Step("gitea").Status == initstate.StepSucceeded {
		_ = state.SetOverall(initstate.StateReady)
	}
	_ = st.Save()
	a.out("init: overall=%s", state.Overall)
	return bootstrap.ExitOK
}

// runOut safely reads the redacted output of a module run result.
func runOut(r *modman.RunResult) string {
	if r == nil {
		return ""
	}
	return r.Output
}

func (a *app) initStatus() int {
	state, err := initstate.OpenReadOnly(a.statePath)
	if err != nil {
		a.err("init status: %v", err)
		return bootstrap.ExitBlocked
	}
	initstate.ReconcileAfterCrash(state) // display reconciled view; no write
	return a.emit(state)
}

// initRepair only repairs resources with explicit ServerCLI ownership: it
// re-runs the verify entrypoint for failed/blocked ServerCLI-owned modules and
// never touches legacy-owned or unknown resources.
func (a *app) initRepair() int {
	st, err := initstate.Open(a.statePath)
	if err != nil {
		a.err("init repair: %v", err)
		if errors.Is(err, initstate.ErrConcurrent) || errors.Is(err, initstate.ErrCorrupt) {
			return bootstrap.ExitBlocked
		}
		return bootstrap.ExitModule
	}
	defer st.Close()
	state := st.State()
	// A crash may leave steps stuck in running; reconcile before repairing.
	initstate.ReconcileAfterCrash(state)
	if state.Overall == initstate.StateNotInitialized {
		a.err("init repair: nothing to repair (not initialized)")
		return bootstrap.ExitOK
	}
	// Repair requires the same env/node as the init run; fall back to
	// inventory when the flags were not given.
	if a.env == "" || a.node == "" {
		if inv, ierr := a.loadInventory(); ierr == nil {
			if a.env == "" {
				a.env = inv.Environment
			}
			if a.node == "" {
				a.node = inv.Node.Name
			}
		}
	}
	owners := ownership.NewStore(a.ownershipPath)
	owners.SetLockDir(a.lockDir)
	if lerr := owners.Load(); lerr != nil && !errors.Is(lerr, os.ErrNotExist) {
		a.err("init repair: ownership: %v", lerr)
		return bootstrap.ExitModule
	}
	reg, err := modules.NewRegistry(a.modulesDir)
	if err != nil {
		a.err("init repair: %v", err)
		return bootstrap.ExitPreflight
	}
	store, serr := a.bootstrapStore()
	if serr != nil {
		a.err("init repair: %v", serr)
		return bootstrap.ExitModule
	}
	inv, ierr := a.loadInventory()
	if ierr != nil {
		a.err("init repair: load inventory: %v", ierr)
		return bootstrap.ExitModule
	}
	runner := modman.NewRunner(a.modulesDir, a.runDir, a.lockDir, nil, nil)
	repaired := 0
	stillFailing := 0
	for _, id := range modules.FoundationCoreOrder() {
		stp := state.Step(id)
		if stp == nil || (stp.Status != initstate.StepFailed && stp.Status != initstate.StepRunning) {
			continue
		}
		// Only ServerCLI-owned resources may be repaired.
		if oerr := owners.CanOperate(a.env, a.node, id); oerr != nil {
			a.err("init repair: %s skipped (no ServerCLI ownership): %v", id, oerr)
			stillFailing++
			continue
		}
		mod, mok := reg.Module(id)
		if !mok || mod == nil {
			continue
		}
		cfg, sec, xerr := modules.ResolveModuleInputs(mod, inv, store, state.OperationID)
		if xerr != nil {
			a.err("init repair: %s inputs: %v", id, xerr)
			stillFailing++
			continue
		}
		if _, hasVer := mod.Operations["verify"]; !hasVer {
			continue
		}
		if vres, verr := runner.Run(context.Background(), modman.RunOptions{
			ModuleID: id, Operation: "verify", Config: cfg, Secrets: sec,
			ModulesDir: a.modulesDir, RunDir: a.runDir, LockDir: a.lockDir, Log: nil,
		}); verr == nil && vres != nil && vres.ExitCode == 0 {
			stp.Status = initstate.StepSucceeded
			stp.HealthEvidence = "repaired-by-verify"
			stp.CompletedAt = time.Now().UTC()
			stp.Retryable = false
			repaired++
			a.out("init repair: %s repaired", id)
		} else {
			a.err("init repair: %s still failing: %v", id, verr)
			stillFailing++
		}
	}
	if serr := st.Save(); serr != nil {
		a.err("init repair: persist state: %v", serr)
		return bootstrap.ExitModule
	}
	if stillFailing > 0 {
		a.err("init repair: %d repaired, %d still failing", repaired, stillFailing)
		return bootstrap.ExitModule
	}
	a.out("init repair: completed (%d repaired)", repaired)
	return bootstrap.ExitOK
}

func (a *app) cmdConfig() int {
	if len(a.args) == 0 {
		a.err("servercli config: expected `import plan` or `import apply`")
		return bootstrap.ExitUsage
	}
	if a.args[0] != "import" {
		a.err("servercli config: unknown subcommand %q", a.args[0])
		return bootstrap.ExitUsage
	}
	if len(a.args) < 2 {
		a.err("servercli config import: expected plan|apply")
		return bootstrap.ExitUsage
	}
	sub := a.args[1]
	if err := a.requireBundleInputs(); err != nil {
		a.err("config import %s: %v", sub, err)
		return bootstrap.ExitUsage
	}
	switch sub {
	case "plan":
		loaded, err := bundle.LoadBundle(context.Background(), a.bundleOpts())
		if err != nil {
			a.err("config import plan: %v", err)
			return mapBundleErr(err)
		}
		return a.emit(struct {
			BundleID      string `json:"bundle_id"`
			BundleVersion string `json:"bundle_version"`
			Environment   string `json:"environment"`
			NodeName      string `json:"node_name"`
			InputDigest   string `json:"input_digest"`
			WritesNothing bool   `json:"writes_nothing"`
		}{
			BundleID:      loaded.Manifest.BundleID,
			BundleVersion: loaded.Manifest.BundleVersion,
			Environment:   loaded.Inventory.Environment,
			NodeName:      loaded.Inventory.Node.Name,
			InputDigest:   loaded.InputDigest,
			WritesNothing: true,
		})
	case "apply":
		if !a.yes && !isTTY(a.stdin) {
			a.err("config import apply: non-interactive apply requires --yes")
			return bootstrap.ExitUsage
		}
		store, serr := a.bootstrapStore()
		if serr != nil {
			a.err("config import apply: %v", serr)
			return bootstrap.ExitModule
		}
		res, ierr := bundle.ImportBundle(context.Background(), a.bundleOpts(), store, nil)
		if ierr != nil {
			a.err("config import apply: %v", ierr)
			return mapBundleErr(ierr)
		}
		// Adopt the new bundle as the state baseline so apply/resume can
		// continue (otherwise the new bundle would be rejected as changed).
		if st, serr := initstate.Open(a.statePath); serr == nil {
			st.State().BundleID = res.BundleID
			st.State().InputDigest = res.InputDigest
			st.State().TargetVersion = res.BundleVersion
			if serr := st.Save(); serr != nil {
				a.err("config import apply: update state baseline: %v", serr)
			}
			st.Close()
		}
		return a.emit(res)
	default:
		a.err("servercli config import: unknown subcommand %q", sub)
		return bootstrap.ExitUsage
	}
}

func (a *app) cmdModules() int {
	if len(a.args) == 0 || a.args[0] != "run" {
		a.err("modules: expected `servercli modules run --module <id> --operation <op>`")
		return bootstrap.ExitUsage
	}
	owners := ownership.NewStore(a.ownershipPath)
	owners.SetLockDir(a.lockDir)
	_ = owners.Load()
	var moduleID, operation string
	yes := a.yes
	rest := a.args[1:]
	for len(rest) > 0 {
		switch {
		case rest[0] == "--module" && len(rest) > 1:
			moduleID = rest[1]
			rest = rest[2:]
		case rest[0] == "--operation" && len(rest) > 1:
			operation = rest[1]
			rest = rest[2:]
		case rest[0] == "--yes":
			yes = true
			rest = rest[1:]
		default:
			a.err("modules run: unexpected argument %q", rest[0])
			return bootstrap.ExitUsage
		}
	}
	if moduleID == "" || operation == "" {
		a.err("modules run: --module and --operation are required")
		return bootstrap.ExitUsage
	}
	if !yes && !isTTY(a.stdin) {
		a.err("modules run: non-interactive run requires --yes")
		return bootstrap.ExitUsage
	}
	reg, rerr := modules.NewRegistry(a.modulesDir)
	if rerr != nil {
		a.err("modules run: %v", rerr)
		return bootstrap.ExitPreflight
	}
	mod, mok := reg.Module(moduleID)
	if !mok || mod == nil {
		a.err("modules run: unknown module %q", moduleID)
		return bootstrap.ExitUsage
	}
	inv, ierr := a.loadInventory()
	if ierr != nil {
		a.err("modules run: load inventory: %v", ierr)
		return bootstrap.ExitModule
	}
	store, serr := a.bootstrapStore()
	if serr != nil {
		a.err("modules run: %v", serr)
		return bootstrap.ExitModule
	}
	cfg, sec, xerr := modules.ResolveModuleInputs(mod, inv, store, "")
	if xerr != nil {
		a.err("modules run: %v", xerr)
		return bootstrap.ExitModule
	}
	// Mutating operations require ServerCLI ownership for non-bootstrap modules.
	if operation != "preflight" && operation != "plan" && operation != "verify" {
		if oerr := owners.CanOperate(a.env, a.node, moduleID); oerr != nil && operation != "adopt" {
			a.err("modules run: %v", oerr)
			return bootstrap.ExitBlocked
		}
	}
	runner := modman.NewRunner(a.modulesDir, a.runDir, a.lockDir, nil, nil)
	res, err := runner.Run(context.Background(), modman.RunOptions{
		ModuleID: moduleID, Operation: operation, Config: cfg, Secrets: sec,
		ModulesDir: a.modulesDir, RunDir: a.runDir, LockDir: a.lockDir, Log: nil,
	})
	if err != nil {
		a.err("modules run: %v", err)
		if errors.Is(err, modman.ErrForbidden) {
			return bootstrap.ExitUsage
		}
		return bootstrap.ExitModule
	}
	return a.emit(res)
}

func (a *app) cmdOps() int {
	if len(a.args) == 0 {
		a.err("ops: expected update|backup|restore")
		return bootstrap.ExitUsage
	}
	op := a.args[0]
	services := a.args[1:]
	if op != "update" && op != "backup" && op != "restore" && op != "adopt" {
		a.err("ops: unknown operation %q", op)
		return bootstrap.ExitUsage
	}
	store := ownership.NewStore(a.ownershipPath)
	store.SetLockDir(a.lockDir)
	if err := store.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.err("ops: ownership: %v", err)
		return bootstrap.ExitModule
	}
	reg, err := modules.NewRegistry(a.modulesDir)
	if err != nil {
		a.err("ops: %v", err)
		return bootstrap.ExitPreflight
	}
	runner := modman.NewRunner(a.modulesDir, a.runDir, a.lockDir, nil, nil)
	// Environment/node fall back to the imported inventory when not given.
	if a.env == "" || a.node == "" {
		if inv, ierr := a.loadInventory(); ierr == nil {
			if a.env == "" {
				a.env = inv.Environment
			}
			if a.node == "" {
				a.node = inv.Node.Name
			}
		}
	}
	inv, ierr := a.loadInventory()
	if ierr != nil {
		a.err("ops: load inventory: %v", ierr)
		return bootstrap.ExitModule
	}
	bstore, serr := a.bootstrapStore()
	if serr != nil {
		a.err("ops: %v", serr)
		return bootstrap.ExitModule
	}
	ocfg := ops.Config{
		Environment: a.env,
		Node:        a.node,
		ModulesDir:  a.modulesDir,
		RunDir:      a.runDir,
		LockDir:     a.lockDir,
		BackupDir:   a.backupDir,
		StatePath:   a.statePath,
		Timeout:     30 * time.Minute,
		Uploader:    ops.NoopUploader{},
		Inventory:   inv,
		Secrets:     bstore,
	}
	// Schema compatibility gate from the signed release manifest (optional).
	if a.releaseManifestFile != "" {
		raw, rerr := os.ReadFile(a.releaseManifestFile)
		if rerr != nil {
			a.err("ops: read release manifest: %v", rerr)
			return bootstrap.ExitModule
		}
		rm, rerr := bundle.LoadReleaseManifest(raw)
		if rerr != nil {
			a.err("ops: parse release manifest: %v", rerr)
			return bootstrap.ExitModule
		}
		pubPEM, perr := os.ReadFile(a.pubKeyFile)
		if perr != nil {
			a.err("ops: read release public key: %v", perr)
			return bootstrap.ExitModule
		}
		if verr := bundle.VerifyReleaseManifest(rm, pubPEM); verr != nil {
			a.err("ops: release manifest signature: %v", verr)
			return bootstrap.ExitSignature
		}
		ocfg.ReleaseCompat = &rm.SchemaCompat
		ocfg.CurrentSchemaVersion = a.currentSchemaVersion
	}
	o := ops.New(store, ocfg)
	o.Registry = registryAdapter{reg: reg}
	o.Runner = runner
	ctx := context.Background()
	switch op {
	case "update":
		res, uerr := o.Update(ctx, services, ops.RunOpts{Out: a.stdout, Err: a.stderr})
		if uerr != nil {
			var agg *ops.AggregateError
			if errors.As(uerr, &agg) {
				// Partial success: emit per-service results -> ExitPartial.
				return a.emit(res)
			}
			if isOpsBlocked(uerr) {
				a.err("ops update: %v", uerr)
				return bootstrap.ExitBlocked
			}
			a.err("ops update: %v", uerr)
			return bootstrap.ExitModule
		}
		return a.emit(res)
	case "backup":
		res, berr := o.Backup(ctx, services, ops.RunOpts{Out: a.stdout, Err: a.stderr})
		if berr != nil {
			var agg *ops.AggregateError
			if errors.As(berr, &agg) {
				return a.emit(res)
			}
			if isOpsBlocked(berr) {
				a.err("ops backup: %v", berr)
				return bootstrap.ExitBlocked
			}
			a.err("ops backup: %v", berr)
			return bootstrap.ExitModule
		}
		return a.emit(res)
	case "adopt":
		if len(services) < 1 {
			a.err("ops adopt: usage: servercli ops adopt <service>")
			return bootstrap.ExitUsage
		}
		return a.runAdopt(ctx, store, runner, reg, services[0])
	case "restore":
		if len(services) < 2 {
			a.err("ops restore: usage: servercli ops restore <service> <backup_id|recovery_set_id>")
			return bootstrap.ExitUsage
		}
		rerr := o.Restore(ctx, services[0], services[1], ops.RunOpts{Confirm: a.yes, In: a.stdin, Out: a.stdout, Err: a.stderr})
		if rerr != nil {
			a.err("ops restore: %v", rerr)
			switch {
			case errors.Is(rerr, ops.ErrRequireExplicitID), errors.Is(rerr, ops.ErrRequireConfirm):
				return bootstrap.ExitUsage
			case errors.Is(rerr, ops.ErrUnverified), errors.Is(rerr, ops.ErrNoVerifyKey), errors.Is(rerr, ops.ErrLegacyBackup):
				return bootstrap.ExitSignature
			default:
				return bootstrap.ExitModule
			}
		}
		return a.emit(map[string]string{"status": "restored"})
	default:
		return bootstrap.ExitUsage
	}
}

// runAdopt executes the fixed adopt flow for one service:
// plan -> freeze legacy -> transition adopting -> module adopt -> mark
// servercli. Any failure rolls back to the legacy owner; the original data is
// never moved, deleted or rebuilt.
func (a *app) runAdopt(ctx context.Context, store *ownership.Store, runner *modman.Runner, reg *modules.Registry, service string) int {
	if a.env == "" || a.node == "" {
		a.err("ops adopt: --environment and --node-name are required")
		return bootstrap.ExitUsage
	}
	if err := store.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.err("ops adopt: ownership: %v", err)
		return bootstrap.ExitModule
	}
	plan, err := store.AdoptPlan(ctx, a.env, a.node, service)
	if err != nil {
		a.err("ops adopt: plan: %v", err)
		return bootstrap.ExitModule
	}
	if plan.AlreadyAdopted {
		a.out("ops adopt: %s already owned by servercli", service)
		return bootstrap.ExitOK
	}
	// No ownership record yet -> seed a legacy-init record so the adopt state
	// machine has a valid starting point (data is never touched).
	if _, ok := store.Get(a.env, a.node, service); !ok {
		if serr := store.Set(a.env, a.node, service, ownership.Ownership{
			Environment: a.env, Node: a.node, Service: service, Owner: ownership.OwnerLegacyInit,
		}); serr != nil {
			a.err("ops adopt: seed ownership: %v", serr)
			return bootstrap.ExitModule
		}
		if serr := store.Save(); serr != nil {
			a.err("ops adopt: persist ownership: %v", serr)
			return bootstrap.ExitModule
		}
	}
	a.out("ops adopt: plan for %s (owner=%s)", service, plan.Owner)
	for _, stp := range plan.Steps {
		a.out("  - %s: %s", stp.Name, stp.Description)
	}
	if !a.yes && !isTTY(a.stdin) {
		a.err("ops adopt: non-interactive adopt requires --yes")
		return bootstrap.ExitUsage
	}

	// Freeze legacy entrypoints and move into adopting.
	if err := store.FreezeLegacy(a.env, a.node, service); err != nil {
		a.err("ops adopt: freeze legacy: %v", err)
		return bootstrap.ExitBlocked
	}
	if err := store.Transition(a.env, a.node, service, ownership.OwnerMigrationFrozen, ownership.OwnerAdopting); err != nil {
		_ = store.RollbackAdopt(a.env, a.node, service)
		a.err("ops adopt: transition adopting: %v", err)
		return bootstrap.ExitBlocked
	}
	if err := store.Save(); err != nil {
		_ = store.RollbackAdopt(a.env, a.node, service)
		a.err("ops adopt: persist adopting state: %v", err)
		return bootstrap.ExitModule
	}

	// Run the module adopt hook (fixed entrypoint) if declared.
	mod, mok := reg.Module(service)
	if !mok || mod == nil {
		_ = store.RollbackAdopt(a.env, a.node, service)
		a.err("ops adopt: module %s not found", service)
		return bootstrap.ExitModule
	}
	inv, ierr := a.loadInventory()
	if ierr != nil {
		_ = store.RollbackAdopt(a.env, a.node, service)
		a.err("ops adopt: load inventory: %v", ierr)
		return bootstrap.ExitModule
	}
	bstore, serr := a.bootstrapStore()
	if serr != nil {
		_ = store.RollbackAdopt(a.env, a.node, service)
		a.err("ops adopt: %v", serr)
		return bootstrap.ExitModule
	}
	cfg, sec, xerr := modules.ResolveModuleInputs(mod, inv, bstore, "")
	if xerr != nil {
		_ = store.RollbackAdopt(a.env, a.node, service)
		a.err("ops adopt: inputs: %v", xerr)
		return bootstrap.ExitModule
	}
	if _, hasAdopt := mod.Operations["adopt"]; hasAdopt {
		res, rerr := runner.Run(ctx, modman.RunOptions{
			ModuleID: service, Operation: "adopt", Config: cfg, Secrets: sec,
			ModulesDir: a.modulesDir, RunDir: a.runDir, LockDir: a.lockDir, Log: nil,
		})
		if rerr != nil || res == nil || res.ExitCode != 0 {
			_ = store.RollbackAdopt(a.env, a.node, service)
			a.err("ops adopt: module adopt failed; rolled back to legacy owner: %v", rerr)
			return bootstrap.ExitModule
		}
	}
	if err := store.MarkServerCLI(a.env, a.node, service, modman.ComputeInputDigest(cfg, sec), ""); err != nil {
		_ = store.RollbackAdopt(a.env, a.node, service)
		a.err("ops adopt: mark servercli: %v", err)
		return bootstrap.ExitModule
	}
	a.out("ops adopt: %s adopted (owner=servercli)", service)
	return bootstrap.ExitOK
}

// wizard interactively collects the required inputs then runs plan + apply.
func (a *app) wizard() int {
	if !isTTY(a.stdin) {
		a.err("servercli init: non-interactive environments must use `servercli init plan|apply` with flags (--yes for apply)")
		return bootstrap.ExitUsage
	}
	rd := bufio.NewReader(a.stdin)
	ask := func(prompt string) string {
		a.err("%s", prompt)
		line, _ := rd.ReadString('\n')
		return strings.TrimSpace(line)
	}
	if a.env == "" {
		a.env = ask("environment (e.g. production): ")
	}
	if a.node == "" {
		a.node = ask("node-name: ")
	}
	if a.bundleURL == "" {
		a.bundleURL = ask("bundle-url: ")
	}
	if a.ageKeyFile == "" {
		a.ageKeyFile = ask("age-key-file [" + bootstrap.FileBootstrapAgeKey + "]: ")
		if a.ageKeyFile == "" {
			a.ageKeyFile = bootstrap.FileBootstrapAgeKey
		}
	}
	if a.pubKeyFile == "" {
		a.pubKeyFile = ask("release pubkey-file [" + defaultPubKeyFile + "]: ")
		if a.pubKeyFile == "" {
			a.pubKeyFile = defaultPubKeyFile
		}
	}
	if err := a.requireBundleInputs(); err != nil {
		a.err("init: %v", err)
		return bootstrap.ExitUsage
	}
	planCode := a.initPlan()
	if planCode != bootstrap.ExitOK {
		return planCode
	}
	ans := ask("apply now? [y/N]: ")
	if !strings.EqualFold(ans, "y") && !strings.EqualFold(ans, "yes") {
		a.err("init: cancelled")
		return bootstrap.ExitOK
	}
	a.yes = true
	return a.initApply(false)
}

func (a *app) emit(v any) int {
	if a.jsonOut {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			a.err("encode output: %v", err)
			return bootstrap.ExitModule
		}
		a.out("%s", string(b))
		return bootstrap.ExitOK
	}
	switch t := v.(type) {
	case *initstate.State:
		a.out("overall: %s", t.Overall)
		a.out("operation_id: %s", t.OperationID)
		a.out("bundle_id: %s", t.BundleID)
		a.out("input_digest: %s", t.InputDigest)
		for _, st := range t.Steps {
			a.out("  %-18s %-10s attempt=%d commit=%s", st.ModuleID, st.Status, st.Attempt, st.LastCommitPoint)
		}
	case []ops.Result:
		fail := 0
		for _, r := range t {
			status := "ok"
			if !r.OK {
				status = "failed: " + r.Error
				fail++
			}
			a.out("%-18s %s", r.Service, status)
		}
		if fail > 0 {
			return bootstrap.ExitPartial
		}
	default:
		b, _ := json.MarshalIndent(v, "", "  ")
		a.out("%s", string(b))
	}
	return bootstrap.ExitOK
}

// isOpsBlocked reports whether an ops error is an ownership/lock/blocked
// condition that maps to the stable blocked exit code.
func isOpsBlocked(err error) bool {
	return errors.Is(err, ownership.ErrNoOwnership) || errors.Is(err, ownership.ErrBlocked) ||
		errors.Is(err, ownership.ErrLocked) || errors.Is(err, ownership.ErrInvalidTransition)
}

func mapBundleErr(err error) int {
	switch {
	case errors.Is(err, bundle.ErrReplayRejected):
		return bootstrap.ExitBlocked
	case errors.Is(err, initstate.ErrConcurrent), errors.Is(err, initstate.ErrCorrupt):
		return bootstrap.ExitBlocked
	default:
		// Signature/decryption failures map to ExitSignature when they look
		// like auth; otherwise module failure.
		s := strings.ToLower(err.Error())
		if strings.Contains(s, "signature") || strings.Contains(s, "decrypt") || strings.Contains(s, "age") || strings.Contains(s, "digest mismatch") {
			return bootstrap.ExitSignature
		}
		return bootstrap.ExitModule
	}
}

func moduleVersion(reg *modules.Registry, id string) string {
	if m, ok := reg.Module(id); ok {
		return m.Version
	}
	return ""
}

func commitPointFor(id string) string {
	switch id {
	case "v2ray":
		return "v2ray_ready"
	case "docker":
		return "docker_ready"
	case "postgres":
		return "postgres_ready"
	case "caddy":
		return "caddy_gateway_ready"
	case "control-plane":
		return "control_plane_local_ready"
	case "agent":
		return "agent_ready"
	case "gitea":
		return "gitea_ready"
	default:
		return id + "_ready"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func isTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// registryAdapter adapts *modules.Registry to ops.ModuleRegistry.
type registryAdapter struct {
	reg *modules.Registry
}

func (r registryAdapter) Ordered() ([]string, error)                      { return r.reg.Ordered(context.Background()) }
func (r registryAdapter) Module(id string) (*modman.ModuleManifest, bool) { return r.reg.Module(id) }

// ownerResolver adapts ownership.Store to modules.OwnerResolver.
type ownerResolver struct {
	store *ownership.Store
	env   string
	node  string
}

func (o *ownerResolver) Owner(moduleID string) (string, error) {
	_ = o.store.Load() // best-effort refresh
	ow, ok := o.store.Get(o.env, o.node, moduleID)
	if !ok {
		return "", nil
	}
	return ow.Owner, nil
}
