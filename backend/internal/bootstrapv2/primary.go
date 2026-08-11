package bootstrapv2

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"servercli/internal/bootstrap"
	"servercli/internal/bundle"
	"servercli/internal/initstate"
	"servercli/internal/oss"
)

const (
	defaultEnvPath = "/root/servercli-bootstrap/bootstrap.env"
	ossProbeKey    = "servercli/bootstrap/probe"
)

var (
	bootstrapPhases = []string{
		StatePreflight,
		StateReleaseDownloading,
		StateReleaseVerified,
		StateReleaseInstalled,
		StateFoundationPlanning,
		StateFoundationApplying,
		StateControlPlaneReady,
		StateAgentReady,
		StateOSSSyncReady,
		StateReady,
	}
	foundationModules = []string{
		"docker",
		"postgres",
		"caddy",
		"control-plane",
		"agent",
		"v2ray",
		"gitea",
	}
)

// FoundationRun describes one servercli-owned foundation module invocation.
// Operation is "install" during Apply/Resume and "repair" during Repair.
type FoundationRun struct {
	ModuleID  string
	Operation string
	Manifest  *bootstrap.ReleaseManifest
}

// BootstrapOptions supplies all external effects as hooks so the orchestration
// remains testable and independent from the database and CLI packages.
type BootstrapOptions struct {
	EnvPath          string
	OSS              oss.Provider
	ManifestLoader   func(ctx context.Context, baseURL string) (*bootstrap.ReleaseManifest, error)
	Installer        func(ctx context.Context, manifest *bootstrap.ReleaseManifest, artifactDir string) error
	FoundationRunner func(ctx context.Context, run FoundationRun) error
	Restorer         func(ctx context.Context, backupManifestPath string) error
	StatePath        string
	Log              *slog.Logger
	Timeout          time.Duration
}

// Bootstrap is the Primary Bootstrap executor. It intentionally has the same
// configurable fields as BootstrapOptions so callers may use either a struct
// literal or New/NewBootstrap.
type Bootstrap BootstrapOptions

// New creates a Primary Bootstrap executor.
func New(opts BootstrapOptions) *Bootstrap {
	b := Bootstrap(opts)
	return &b
}

// NewBootstrap is an explicit-name alias for New.
func NewBootstrap(opts BootstrapOptions) *Bootstrap { return New(opts) }

// Status is a read-only view of durable bootstrap progress.
type Status struct {
	State        string            `json:"state"`
	Overall      string            `json:"overall"`
	OperationID  string            `json:"operation_id,omitempty"`
	Version      string            `json:"version,omitempty"`
	Steps        []initstate.Step  `json:"steps"`
	CommitPoints map[string]string `json:"commit_points,omitempty"`
}

// RecoveryPlan describes the verified backup object selected for recovery.
type RecoveryPlan struct {
	NodeID               string `json:"node_id"`
	PointerKey           string `json:"pointer_key"`
	BackupManifestKey    string `json:"backup_manifest_key"`
	BackupManifestSHA256 string `json:"backup_manifest_sha256"`
	LocalManifestPath    string `json:"local_manifest_path"`
}

type recoveryPointer struct {
	ManifestKey string `json:"manifest_key"`
	SHA256      string `json:"sha256"`
}

type execution struct {
	b           *Bootstrap
	env         *BootstrapEnv
	store       *initstate.Store
	manifest    *bootstrap.ReleaseManifest
	artifactDir string
}

// Plan returns a read-only execution plan. It reads bootstrap.env and the
// release manifest but does not write state, artifacts, OSS objects, or local
// resources.
func (b *Bootstrap) Plan(ctx context.Context) (*Plan, error) {
	env, err := LoadBootstrapEnv(b.envPath())
	if err != nil {
		return nil, err
	}
	if b.OSS == nil && b.ManifestLoader == nil {
		return nil, errors.New("bootstrapv2: OSS provider or manifest loader is required")
	}
	ctx, cancel := b.withTimeout(ctx)
	defer cancel()
	manifest, err := b.loadManifest(ctx, env, "")
	if err != nil {
		return nil, fmt.Errorf("bootstrapv2: load release manifest: %w", err)
	}
	if err := validateManifest(manifest, env.ServerCLIVersion); err != nil {
		return nil, err
	}

	phases := append([]string(nil), bootstrapPhases...)
	artifacts := make([]string, 0, len(manifest.Artifacts))
	for _, art := range manifest.Artifacts {
		artifacts = append(artifacts, art.Path)
	}
	commitPoints := make(map[string]string, len(phases)+len(foundationModules))
	for _, phase := range phases {
		commitPoints[phase] = phaseStepID(phase)
	}
	for _, moduleID := range foundationModules {
		commitPoints["foundation:"+moduleID] = moduleStepID(moduleID, "install")
	}
	return &Plan{
		Version:             manifest.ReleaseVersion,
		Phases:              phases,
		Artifacts:           artifacts,
		CommitPoints:        commitPoints,
		OSSBucket:           env.OSSBucket,
		ClusterID:           env.ClusterID,
		NodeName:            env.NodeName,
		Profile:             env.Profile,
		RequiresGitHubToken: env.RequiresGitHubToken(),
		Warnings:            []string{},
	}, nil
}

// Apply runs or continues the OSS-first Primary Bootstrap flow.
func (b *Bootstrap) Apply(ctx context.Context) error { return b.execute(ctx) }

// Resume continues from the first missing durable commit point. Completed
// phases and completed foundation module side effects are never invoked again.
func (b *Bootstrap) Resume(ctx context.Context) error { return b.execute(ctx) }

func (b *Bootstrap) execute(ctx context.Context) error {
	env, err := LoadBootstrapEnv(b.envPath())
	if err != nil {
		b.recordEarlyFailure(initstate.ErrTypePreflight)
		return err
	}
	if b.OSS == nil {
		b.recordEarlyFailure(initstate.ErrTypeNetwork)
		return errors.New("bootstrapv2: OSS provider is required")
	}
	if b.Installer == nil {
		return errors.New("bootstrapv2: Installer hook is required")
	}
	if b.FoundationRunner == nil {
		return errors.New("bootstrapv2: FoundationRunner hook is required")
	}

	ctx, cancel := b.withTimeout(ctx)
	defer cancel()
	store, err := initstate.Open(b.statePath())
	if err != nil {
		return err
	}
	defer store.Close()
	if err := prepareState(store, env.ServerCLIVersion); err != nil {
		return err
	}
	exec := &execution{
		b:           b,
		env:         env,
		store:       store,
		artifactDir: b.artifactDir(env.ServerCLIVersion),
	}

	for _, phase := range bootstrapPhases {
		if err := exec.runPhase(ctx, phase); err != nil {
			return err
		}
	}
	return nil
}

func (e *execution) runPhase(ctx context.Context, phase string) error {
	stepID := phaseStepID(phase)
	if commitPointRecorded(e.store.State(), stepID) {
		return nil
	}
	from := durableState(e.store.State(), false)
	if err := ValidateTransition(from, phase); err != nil {
		return err
	}
	if err := startStep(e.store, stepID, "phase", e.env.ServerCLIVersion); err != nil {
		return err
	}

	var err error
	switch phase {
	case StatePreflight:
		err = e.preflight(ctx)
	case StateReleaseDownloading:
		err = e.downloadRelease(ctx)
	case StateReleaseVerified:
		err = e.verifyRelease(ctx)
	case StateReleaseInstalled:
		err = e.installRelease(ctx)
	case StateFoundationPlanning:
		err = validateFoundationOrder()
	case StateFoundationApplying:
		err = e.runFoundation(ctx, "install")
	case StateControlPlaneReady, StateAgentReady, StateReady:
		// These are explicit durable readiness gates. Component-specific
		// health work belongs in the installer/module hooks.
	case StateOSSSyncReady:
		err = e.probeOSS(ctx)
	default:
		err = fmt.Errorf("bootstrapv2: unknown phase %q", phase)
	}
	if err != nil {
		errType := phaseErrorType(phase)
		if saveErr := failStep(e.store, stepID, errType); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		e.b.logger().Error("bootstrap phase failed", "phase", phase, "error_type", errType)
		return fmt.Errorf("bootstrapv2: phase %s failed: %w", phase, err)
	}
	if err := succeedStep(e.store, stepID, phase); err != nil {
		return err
	}
	if phase == StateControlPlaneReady && e.store.State().Overall == initstate.StateInitializing {
		if err := e.store.State().SetOverall(initstate.StateCoreReady); err != nil {
			return err
		}
		if err := e.store.Save(); err != nil {
			return err
		}
	}
	if phase == StateReady && e.store.State().Overall != initstate.StateReady {
		if err := e.store.State().SetOverall(initstate.StateReady); err != nil {
			return err
		}
		if err := e.store.Save(); err != nil {
			return err
		}
	}
	return nil
}

func (e *execution) preflight(ctx context.Context) error {
	// Root suitability is intentionally only represented by the durable
	// preflight gate here; process privilege enforcement belongs to the CLI.
	return e.probeOSS(ctx)
}

func (e *execution) probeOSS(ctx context.Context) error {
	_, err := e.b.OSS.Exists(ctx, ossProbeKey)
	if err != nil {
		return fmt.Errorf("OSS reachability probe: %w", err)
	}
	return nil
}

func (e *execution) downloadRelease(ctx context.Context) error {
	manifest, err := e.b.loadManifest(ctx, e.env, e.artifactDir)
	if err != nil {
		return err
	}
	if err := validateManifest(manifest, e.env.ServerCLIVersion); err != nil {
		return err
	}
	if err := os.MkdirAll(e.artifactDir, 0o700); err != nil {
		return fmt.Errorf("create artifact directory: %w", err)
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal release manifest: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(e.artifactDir, bundle.ReleaseManifestName), raw, 0o600); err != nil {
		return err
	}
	prefix := releasePrefix(e.env.ServerCLIVersion)
	for _, art := range manifest.Artifacts {
		localPath, err := safeArtifactPath(e.artifactDir, art.Path)
		if err != nil {
			return err
		}
		data, err := e.b.OSS.Get(ctx, prefix+"/"+art.Path)
		if err != nil {
			return fmt.Errorf("download release artifact %q: %w", art.Path, err)
		}
		if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
			return err
		}
		if err := writeFileAtomic(localPath, data, 0o600); err != nil {
			return err
		}
	}
	e.manifest = manifest
	return nil
}

func (e *execution) verifyRelease(ctx context.Context) error {
	manifest, err := e.ensureManifest(ctx)
	if err != nil {
		return err
	}
	for _, art := range manifest.Artifacts {
		path, err := safeArtifactPath(e.artifactDir, art.Path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read release artifact %q: %w", art.Path, err)
		}
		if art.Size > 0 && int64(len(data)) != art.Size {
			return fmt.Errorf("verify release artifact %q: size mismatch", art.Path)
		}
		if err := oss.VerifySHA256(data, art.SHA256); err != nil {
			return fmt.Errorf("verify release artifact %q: %w", art.Path, err)
		}
	}
	return nil
}

func (e *execution) installRelease(ctx context.Context) error {
	manifest, err := e.ensureManifest(ctx)
	if err != nil {
		return err
	}
	return e.b.Installer(ctx, manifest, e.artifactDir)
}

func (e *execution) runFoundation(ctx context.Context, operation string) error {
	manifest, err := e.ensureManifest(ctx)
	if err != nil {
		return err
	}
	for _, moduleID := range foundationModules {
		stepID := moduleStepID(moduleID, operation)
		if commitPointRecorded(e.store.State(), stepID) {
			continue
		}
		if err := startStep(e.store, stepID, operation, manifest.ReleaseVersion); err != nil {
			return err
		}
		err := e.b.FoundationRunner(ctx, FoundationRun{
			ModuleID:  moduleID,
			Operation: operation,
			Manifest:  manifest,
		})
		if err != nil {
			if saveErr := failStep(e.store, stepID, initstate.ErrTypeModule); saveErr != nil {
				return errors.Join(err, saveErr)
			}
			return fmt.Errorf("foundation module %s %s: %w", moduleID, operation, err)
		}
		if err := succeedStep(e.store, stepID, moduleID+":"+operation); err != nil {
			return err
		}
	}
	return nil
}

func (e *execution) ensureManifest(ctx context.Context) (*bootstrap.ReleaseManifest, error) {
	if e.manifest != nil {
		return e.manifest, nil
	}
	manifest, err := e.b.loadManifest(ctx, e.env, e.artifactDir)
	if err != nil {
		return nil, err
	}
	if err := validateManifest(manifest, e.env.ServerCLIVersion); err != nil {
		return nil, err
	}
	e.manifest = manifest
	return manifest, nil
}

// Status reads state without acquiring the execution lock and performs no
// reconciliation or writes.
func (b *Bootstrap) Status(ctx context.Context) (*Status, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, err := initstate.OpenReadOnly(b.statePath())
	if err != nil {
		return nil, err
	}
	steps := append([]initstate.Step(nil), state.Steps...)
	commitPoints := make(map[string]string, len(state.CommitPoints))
	for key, value := range state.CommitPoints {
		commitPoints[key] = value
	}
	return &Status{
		State:        durableState(state, true),
		Overall:      state.Overall,
		OperationID:  state.OperationID,
		Version:      state.TargetVersion,
		Steps:        steps,
		CommitPoints: commitPoints,
	}, nil
}

// Repair invokes only the fixed servercli-owned foundation set with operation
// "repair". It does not discover or mutate externally-owned resources.
func (b *Bootstrap) Repair(ctx context.Context) error {
	env, err := LoadBootstrapEnv(b.envPath())
	if err != nil {
		return err
	}
	if b.OSS == nil {
		return errors.New("bootstrapv2: OSS provider is required")
	}
	if b.FoundationRunner == nil {
		return errors.New("bootstrapv2: FoundationRunner hook is required")
	}
	ctx, cancel := b.withTimeout(ctx)
	defer cancel()
	store, err := initstate.Open(b.statePath())
	if err != nil {
		return err
	}
	defer store.Close()
	if err := prepareState(store, env.ServerCLIVersion); err != nil {
		return err
	}
	exec := &execution{b: b, env: env, store: store, artifactDir: b.artifactDir(env.ServerCLIVersion)}
	return exec.runFoundation(ctx, "repair")
}

// PlanRecovery downloads and verifies the latest primary backup manifest and
// writes a root-only local copy suitable for the Restorer hook.
func (b *Bootstrap) PlanRecovery(ctx context.Context) (*RecoveryPlan, error) {
	env, err := LoadBootstrapEnv(b.envPath())
	if err != nil {
		return nil, err
	}
	if b.OSS == nil {
		return nil, errors.New("bootstrapv2: OSS provider is required")
	}
	ctx, cancel := b.withTimeout(ctx)
	defer cancel()
	pointerKey := fmt.Sprintf("servercli/backups/%s/control-plane/latest-manifest.json", env.NodeName)
	raw, err := b.OSS.Get(ctx, pointerKey)
	if err != nil {
		return nil, fmt.Errorf("load recovery pointer: %w", err)
	}
	var pointer recoveryPointer
	if err := json.Unmarshal(raw, &pointer); err != nil {
		return nil, fmt.Errorf("parse recovery pointer: %w", err)
	}
	if pointer.ManifestKey == "" || pointer.SHA256 == "" {
		return nil, errors.New("recovery pointer: manifest_key and sha256 are required")
	}
	manifestRaw, err := b.OSS.GetVerified(ctx, pointer.ManifestKey, pointer.SHA256)
	if err != nil {
		return nil, fmt.Errorf("verify recovery manifest: %w", err)
	}
	if !json.Valid(manifestRaw) {
		return nil, errors.New("recovery manifest: invalid JSON")
	}
	localDir := filepath.Join(filepath.Dir(b.statePath()), "recovery")
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		return nil, err
	}
	localPath := filepath.Join(localDir, "control-plane-backup-manifest.json")
	if err := writeFileAtomic(localPath, manifestRaw, 0o600); err != nil {
		return nil, err
	}
	return &RecoveryPlan{
		NodeID:               env.NodeName,
		PointerKey:           pointerKey,
		BackupManifestKey:    pointer.ManifestKey,
		BackupManifestSHA256: pointer.SHA256,
		LocalManifestPath:    localPath,
	}, nil
}

// Recover enters recovery_required and delegates the actual restore to the
// configured Restorer. No restore implementation lives in this package.
func (b *Bootstrap) Recover(ctx context.Context) error {
	if b.Restorer == nil {
		return errors.New("bootstrapv2: Restorer hook is required")
	}
	state, err := initstate.OpenReadOnly(b.statePath())
	if err != nil {
		return err
	}
	from := durableState(state, true)
	if err := ValidateTransition(from, StateRecoveryRequired); err != nil {
		return err
	}
	plan, err := b.PlanRecovery(ctx)
	if err != nil {
		return err
	}
	store, err := initstate.Open(b.statePath())
	if err != nil {
		return err
	}
	defer store.Close()
	stepID := phaseStepID(StateRecoveryRequired)
	if err := startStep(store, stepID, "restore", state.TargetVersion); err != nil {
		return err
	}
	if err := b.Restorer(ctx, plan.LocalManifestPath); err != nil {
		if saveErr := failStep(store, stepID, initstate.ErrTypeModule); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		return fmt.Errorf("bootstrapv2: restore failed: %w", err)
	}
	if store.State().Overall == initstate.StateFailed {
		if err := store.State().SetOverall(initstate.StateBlocked); err != nil {
			return err
		}
		if err := store.Save(); err != nil {
			return err
		}
	}
	return succeedStep(store, stepID, StateRecoveryRequired)
}

func prepareState(store *initstate.Store, version string) error {
	state := store.State()
	if state.TargetVersion != "" && state.TargetVersion != version && len(state.CommitPoints) > 0 {
		return fmt.Errorf("bootstrapv2: state targets version %q, requested %q", state.TargetVersion, version)
	}
	if durableState(state, true) == StateReady {
		return nil
	}
	switch state.Overall {
	case initstate.StateNotInitialized, initstate.StateFailed, initstate.StateBlocked, initstate.StateReady:
		if err := state.SetOverall(initstate.StateInitializing); err != nil {
			return err
		}
	case initstate.StateInitializing, initstate.StateCoreReady, initstate.StateDegraded:
		// Already resumable.
	default:
		return fmt.Errorf("bootstrapv2: unsupported initstate overall %q", state.Overall)
	}
	if state.OperationID == "" {
		state.OperationID = newOperationID()
		state.StartedAt = time.Now().UTC()
	}
	state.TargetVersion = version
	return store.Save()
}

func startStep(store *initstate.Store, stepID, operation, version string) error {
	state := store.State()
	now := time.Now().UTC()
	attempt := 1
	if previous := state.Step(stepID); previous != nil {
		attempt = previous.Attempt + 1
	}
	state.UpsertStep(initstate.Step{
		OperationID:   state.OperationID,
		ModuleID:      stepID,
		Operation:     operation,
		Attempt:       attempt,
		TargetVersion: version,
		StartedAt:     now,
		Retryable:     true,
		ResumeFrom:    stepID,
		Status:        initstate.StepRunning,
	})
	return store.Save()
}

func succeedStep(store *initstate.Store, stepID, evidence string) error {
	state := store.State()
	step := state.Step(stepID)
	if step == nil {
		return fmt.Errorf("bootstrapv2: step %q was not started", stepID)
	}
	step.Status = initstate.StepSucceeded
	step.CompletedAt = time.Now().UTC()
	step.LastCommitPoint = evidence
	step.ErrorType = ""
	step.Retryable = false
	step.ResumeFrom = ""
	state.SetCommitPoint(stepID, evidence)
	return store.Save()
}

func failStep(store *initstate.Store, stepID, errType string) error {
	state := store.State()
	step := state.Step(stepID)
	if step == nil {
		step = &initstate.Step{OperationID: state.OperationID, ModuleID: stepID, Attempt: 1}
		state.UpsertStep(*step)
		step = state.Step(stepID)
	}
	step.Status = initstate.StepFailed
	step.CompletedAt = time.Now().UTC()
	step.ErrorType = errType
	step.Retryable = errType == initstate.ErrTypeNetwork || errType == initstate.ErrTypeModule || errType == initstate.ErrTypeUnknown
	step.ResumeFrom = stepID
	if state.Overall != initstate.StateFailed {
		if err := state.SetOverall(initstate.StateFailed); err != nil {
			return err
		}
	}
	return store.Save()
}

func (b *Bootstrap) recordEarlyFailure(errType string) {
	store, err := initstate.Open(b.statePath())
	if err != nil {
		return
	}
	defer store.Close()
	state := store.State()
	if state.OperationID == "" {
		state.OperationID = newOperationID()
	}
	_ = startStep(store, phaseStepID(StatePreflight), "phase", "")
	_ = failStep(store, phaseStepID(StatePreflight), errType)
}

func durableState(state *initstate.State, honorOverall bool) string {
	if honorOverall {
		if commitPointRecorded(state, phaseStepID(StateRecoveryRequired)) || state.Overall == initstate.StateBlocked {
			return StateRecoveryRequired
		}
		if state.Overall == initstate.StateFailed {
			return StateFailed
		}
	}
	current := StateEmpty
	for _, phase := range bootstrapPhases {
		if !commitPointRecorded(state, phaseStepID(phase)) {
			break
		}
		current = phase
	}
	return current
}

func commitPointRecorded(state *initstate.State, stepID string) bool {
	if state == nil || state.CommitPoints == nil {
		return false
	}
	_, ok := state.CommitPoints[stepID]
	return ok
}

func phaseStepID(phase string) string { return "phase:" + phase }
func moduleStepID(moduleID, operation string) string {
	return "module:" + moduleID + ":" + operation
}

func phaseErrorType(phase string) string {
	switch phase {
	case StatePreflight:
		return initstate.ErrTypePreflight
	case StateReleaseDownloading, StateOSSSyncReady:
		return initstate.ErrTypeNetwork
	case StateReleaseVerified:
		return initstate.ErrTypeSignature
	default:
		return initstate.ErrTypeModule
	}
}

func validateFoundationOrder() error {
	seen := make(map[string]bool, len(foundationModules))
	for _, moduleID := range foundationModules {
		if moduleID == "" || seen[moduleID] {
			return errors.New("bootstrapv2: invalid foundation module order")
		}
		seen[moduleID] = true
	}
	return nil
}

func validateManifest(manifest *bootstrap.ReleaseManifest, version string) error {
	if manifest == nil {
		return errors.New("bootstrapv2: nil release manifest")
	}
	if manifest.ReleaseVersion == "" {
		return errors.New("bootstrapv2: release manifest has empty version")
	}
	if manifest.ReleaseVersion != version {
		return fmt.Errorf("bootstrapv2: release manifest version %q does not match requested version %q", manifest.ReleaseVersion, version)
	}
	for _, art := range manifest.Artifacts {
		if _, err := safeArtifactPath("/artifact-root", art.Path); err != nil {
			return err
		}
		if !validSHA256Hex(art.SHA256) {
			return fmt.Errorf("bootstrapv2: artifact %q has invalid sha256", art.Path)
		}
	}
	return nil
}

func safeArtifactPath(root, artifactPath string) (string, error) {
	if artifactPath == "" || filepath.IsAbs(artifactPath) || strings.ContainsAny(artifactPath, "\\\x00") {
		return "", fmt.Errorf("bootstrapv2: invalid artifact path %q", artifactPath)
	}
	clean := filepath.Clean(artifactPath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("bootstrapv2: invalid artifact path %q", artifactPath)
	}
	path := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("bootstrapv2: artifact path escapes root")
	}
	return path, nil
}

func (b *Bootstrap) loadManifest(ctx context.Context, env *BootstrapEnv, artifactDir string) (*bootstrap.ReleaseManifest, error) {
	if artifactDir != "" {
		raw, err := os.ReadFile(filepath.Join(artifactDir, bundle.ReleaseManifestName))
		if err == nil {
			return decodeReleaseManifest(raw)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	prefix := releasePrefix(env.ServerCLIVersion)
	if b.ManifestLoader != nil {
		return b.ManifestLoader(ctx, prefix)
	}
	if b.OSS == nil {
		return nil, errors.New("bootstrapv2: OSS provider is required")
	}
	raw, err := b.OSS.Get(ctx, prefix+"/"+bundle.ReleaseManifestName)
	if err != nil {
		return nil, err
	}
	return decodeReleaseManifest(raw)
}

func decodeReleaseManifest(raw []byte) (*bootstrap.ReleaseManifest, error) {
	var manifest bootstrap.ReleaseManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("parse release manifest: %w", err)
	}
	return &manifest, nil
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}

func releasePrefix(version string) string {
	return "servercli/releases/" + version
}

func (b *Bootstrap) envPath() string {
	if b.EnvPath != "" {
		return b.EnvPath
	}
	return defaultEnvPath
}

func (b *Bootstrap) statePath() string {
	if b.StatePath != "" {
		return b.StatePath
	}
	return bootstrap.FileStateJSON
}

func (b *Bootstrap) artifactDir(version string) string {
	return filepath.Join(filepath.Dir(b.statePath()), "artifacts", version)
}

func (b *Bootstrap) logger() *slog.Logger {
	if b.Log != nil {
		return b.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (b *Bootstrap) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if b.Timeout > 0 {
		return context.WithTimeout(ctx, b.Timeout)
	}
	return context.WithCancel(ctx)
}

func newOperationID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	return fmt.Sprintf("bootstrap-%d", time.Now().UnixNano())
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".bootstrapv2-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
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
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
