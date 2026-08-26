package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"servercli/internal/config"
	"servercli/internal/model"
	"servercli/internal/secret"
	"servercli/internal/store"
)

// DeploymentScheduler claims queued deployment operations and executes them
// serially: it drives agent tasks through the TaskService, performs a
// control-plane secondary health check, records backups, stops on failure and
// optionally auto-rolls back a failed update.
type DeploymentScheduler struct {
	svc          *DeploymentService
	store        *store.Store
	cfg          *config.Config
	log          *slog.Logger
	tasks        *TaskService
	nodes        *NodeService
	tickInterval time.Duration
	pollInterval time.Duration
	startDelay   time.Duration
	httpClient   *http.Client
	redactor     *secret.Redactor
}

// NewDeploymentScheduler builds a scheduler. Tick intervals are configurable
// for tests via the returned struct's unexported fields.
func NewDeploymentScheduler(svc *DeploymentService, st *store.Store, cfg *config.Config, log *slog.Logger, tasks *TaskService, nodes *NodeService) *DeploymentScheduler {
	return &DeploymentScheduler{
		svc:          svc,
		store:        st,
		cfg:          cfg,
		log:          log,
		tasks:        tasks,
		nodes:        nodes,
		tickInterval: 5 * time.Second,
		pollInterval: 2 * time.Second,
		startDelay:   2 * time.Second,
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		redactor:     secret.NewRedactor(),
	}
}

// Run ticks until ctx is cancelled. The first tick happens after startDelay.
func (s *DeploymentScheduler) Run(ctx context.Context) {
	if s.startDelay > 0 {
		select {
		case <-time.After(s.startDelay):
		case <-ctx.Done():
			return
		}
	}
	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()
	for {
		if err := s.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Error("deployment scheduler tick failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick claims at most one queued operation and executes it. A panic anywhere
// in the tick must not crash the scheduler loop.
func (s *DeploymentScheduler) Tick(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("deployment scheduler panic recovered", "panic", r)
			err = fmt.Errorf("deployment scheduler panic: %v", r)
		}
	}()
	op, err := s.store.ClaimQueuedDeploymentOperation(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if op == nil {
		return nil
	}
	if err := s.executeOperation(ctx, op); err != nil {
		s.log.Error("deployment operation execution failed", "operation_id", op.ID, "error", err)
		return err
	}
	return nil
}

// targetResult reports the outcome of executing one operation target.
type targetResult struct {
	succeeded       bool
	cancelled       bool
	autoRollback    bool
	rollbackTarget  string
	rollbackRelease string
}

func (s *DeploymentScheduler) executeOperation(ctx context.Context, op *model.DeploymentOperation) error {
	feature, err := s.store.DeploymentFeatureByID(ctx, op.FeatureID)
	if err != nil {
		s.log.Error("deployment feature missing", "operation_id", op.ID, "error", err)
		return s.finishOperation(ctx, op, model.DeploymentStatusFailed)
	}
	var release *model.DeploymentRelease
	if op.ReleaseID != "" {
		release, err = s.store.DeploymentReleaseByID(ctx, op.ReleaseID)
		if err != nil {
			s.log.Error("deployment release missing", "operation_id", op.ID, "release_id", op.ReleaseID, "error", err)
			return s.finishOperation(ctx, op, model.DeploymentStatusFailed)
		}
	}
	targets, err := s.store.ListDeploymentOperationTargetsByOperation(ctx, op.ID)
	if err != nil {
		return err
	}

	succeeded, failed, terminalFailed := 0, 0, 0
	cancelledAny := false
	var rollbackTarget, rollbackRelease string
	for _, t := range targets {
		if isOperationTargetTerminal(t.Status) {
			if t.Status == model.DeploymentStatusSucceeded {
				succeeded++
			} else {
				terminalFailed++
			}
			continue
		}
		if op.Status == model.DeploymentStatusCancelled {
			now := time.Now().UTC()
			t.Status = model.DeploymentStatusCancelled
			t.FinishedAt = &now
			if err := s.store.UpdateDeploymentOperationTarget(ctx, t); err != nil {
				return err
			}
			cancelledAny = true
			continue
		}
		res, err := s.executeTarget(ctx, op, feature, release, t)
		if err != nil {
			// Infrastructure error: record, log loudly, and stop.
			s.log.Error("deployment target execution failed", "operation_id", op.ID, "target_id", t.ID, "error", err)
			now := time.Now().UTC()
			t.Status = model.DeploymentStatusFailed
			t.ErrorMessage = s.sanitizeMessage(err.Error())
			t.FinishedAt = &now
			if err := s.store.UpdateDeploymentOperationTarget(ctx, t); err != nil {
				return err
			}
			failed++
			break
		}
		if res.cancelled {
			cancelledAny = true
			continue
		}
		if res.succeeded {
			succeeded++
			continue
		}
		failed++
		if res.autoRollback {
			rollbackTarget = res.rollbackTarget
			rollbackRelease = res.rollbackRelease
		}
		break
	}

	// Finalize the operation, honouring a concurrent cancel.
	cur, _ := s.store.DeploymentOperationByID(ctx, op.ID)
	current := model.DeploymentStatusRunning
	if cur != nil {
		current = cur.Status
	}
	var finalStatus string
	switch {
	case current == model.DeploymentStatusCancelled || cancelledAny:
		finalStatus = model.DeploymentStatusCancelled
	case failed > 0 || terminalFailed > 0:
		if succeeded > 0 {
			finalStatus = model.DeploymentStatusPartialFailed
		} else {
			finalStatus = model.DeploymentStatusFailed
		}
	case succeeded == len(targets) && succeeded > 0:
		finalStatus = model.DeploymentStatusSucceeded
	default:
		// No progress made; keep the operation running for a later tick.
		return nil
	}
	if err := s.finishOperation(ctx, op, finalStatus); err != nil {
		return err
	}

	if rollbackTarget != "" && rollbackRelease != "" {
		s.triggerAutoRollback(ctx, op, feature, rollbackTarget, rollbackRelease)
	}
	return nil
}

// finishOperation persists the final status and emits the finish audit event.
func (s *DeploymentScheduler) finishOperation(ctx context.Context, op *model.DeploymentOperation, status string) error {
	now := time.Now().UTC()
	op.Status = status
	op.FinishedAt = &now
	if err := s.store.UpdateDeploymentOperation(ctx, op); err != nil {
		return err
	}
	s.audit(ctx, op, "deployment.operation.finish", nil, map[string]any{
		"operation_id": op.ID,
		"action":       op.Action,
		"result":       status,
	})
	return nil
}

func (s *DeploymentScheduler) audit(ctx context.Context, op *model.DeploymentOperation, action string, t *model.DeploymentOperationTarget, details map[string]any) {
	in := AuditInput{
		ActorType:    model.ActorSystem,
		ActorID:      "deployment:" + op.ID,
		Action:       action,
		ResourceType: "deployment_operation",
		ResourceID:   op.ID,
		Details:      details,
	}
	if t != nil {
		in.ResourceType = "deployment_operation_target"
		in.ResourceID = t.ID
		in.NodeID = t.NodeID
	}
	if err := s.auditor().Record(ctx, in); err != nil {
		s.log.Warn("deployment audit failed", "action", action, "error", err)
	}
}

// auditor returns the auditor owned by the DeploymentService.
func (s *DeploymentScheduler) auditor() *Auditor { return s.svc.auditor }

func (s *DeploymentScheduler) executeTarget(ctx context.Context, op *model.DeploymentOperation, feature *model.DeploymentFeature, release *model.DeploymentRelease, t *model.DeploymentOperationTarget) (targetResult, error) {
	s.markPreflightDone(ctx, op.ID, t.ID)

	// A standalone health_check targets the currently installed release: the
	// operation carries no release_id, so resolve it from the target's
	// current release (the node runner needs release_version to locate the
	// release unambiguously when several versions exist).
	if op.Action == model.DeploymentActionHealthCheck && release == nil {
		if tg, err := s.store.DeploymentTargetByID(ctx, t.TargetID); err == nil && tg != nil && tg.CurrentReleaseID != "" {
			if rel, rerr := s.store.DeploymentReleaseByID(ctx, tg.CurrentReleaseID); rerr == nil {
				release = rel
			}
		}
		if release == nil {
			return s.failTarget(ctx, op, feature, release, t, "no current release installed; cannot run health-check"), nil
		}
	}

	// Plan task-backed steps lazily so a failure never leaves dangling steps.
	plan := []string{op.Action}
	if op.Action == model.DeploymentActionUpdate && feature.BackupMode != "" && feature.BackupMode != "none" {
		plan = append([]string{"backup"}, plan...)
	}
	healthAction := op.Action == model.DeploymentActionInstall ||
		op.Action == model.DeploymentActionUpdate ||
		op.Action == model.DeploymentActionRollback

	for _, stepType := range plan {
		// Command IDs come from the fixed action→command mapping; the
		// standalone health_check action renders its step as "health-check".
		commandID := commandForAction(stepType)
		if commandID == "" {
			return targetResult{}, fmt.Errorf("unsupported deployment action %q", stepType)
		}
		stepTypeName := stepType
		if stepType == model.DeploymentActionHealthCheck {
			stepTypeName = "health-check"
		}
		step, err := s.createStep(ctx, op.ID, t.ID, t.NodeID, stepTypeName, commandID)
		if err != nil {
			return targetResult{}, err
		}
		_, ok, msg, err := s.runTaskStep(ctx, op, feature, release, t, step, commandID)
		if err != nil {
			_ = s.failStep(ctx, step, err.Error())
			return targetResult{}, err
		}
		if !ok {
			_ = s.failStep(ctx, step, msg)
			if cur, _ := s.store.DeploymentOperationByID(ctx, op.ID); cur != nil && cur.Status == model.DeploymentStatusCancelled {
				return s.cancelTarget(ctx, t), nil
			}
			return s.failTarget(ctx, op, feature, release, t, msg), nil
		}
		if stepType == "backup" {
			s.recordBackupFromTask(ctx, op, feature, t, step)
		}
	}

	if healthAction {
		// Real local health-check task: the node runs the feature's health
		// hook (deployment.health-check) before the control plane performs
		// its secondary HTTP probe. The arguments are the same as the main
		// task; only the command/step type changes.
		hcCommandID := commandForAction(model.DeploymentActionHealthCheck)
		hcStep, err := s.createStep(ctx, op.ID, t.ID, t.NodeID, "health-check", hcCommandID)
		if err != nil {
			return targetResult{}, err
		}
		_, ok, msg, err := s.runTaskStep(ctx, op, feature, release, t, hcStep, hcCommandID)
		if err != nil {
			_ = s.failStep(ctx, hcStep, err.Error())
			return targetResult{}, err
		}
		if !ok {
			_ = s.failStep(ctx, hcStep, msg)
			if cur, _ := s.store.DeploymentOperationByID(ctx, op.ID); cur != nil && cur.Status == model.DeploymentStatusCancelled {
				return s.cancelTarget(ctx, t), nil
			}
			res := s.failTarget(ctx, op, feature, release, t, msg)
			if op.Action == model.DeploymentActionUpdate {
				res.autoRollback = true
				if tg, err := s.store.DeploymentTargetByID(ctx, t.TargetID); err == nil && tg != nil {
					res.rollbackTarget = tg.ID
					res.rollbackRelease = tg.LastHealthyReleaseID
				}
			}
			return res, nil
		}

		cphStep, err := s.createStep(ctx, op.ID, t.ID, t.NodeID, "control-plane-health", "")
		if err != nil {
			return targetResult{}, err
		}
		ok, msg = s.controlPlaneHealthCheck(ctx, t, s.targetConfig(ctx, t))
		if !ok {
			_ = s.failStep(ctx, cphStep, msg)
			res := s.failTarget(ctx, op, feature, release, t, msg)
			if op.Action == model.DeploymentActionUpdate {
				res.autoRollback = true
				if tg, err := s.store.DeploymentTargetByID(ctx, t.TargetID); err == nil && tg != nil {
					res.rollbackTarget = tg.ID
					res.rollbackRelease = tg.LastHealthyReleaseID
				}
			}
			return res, nil
		}
		_ = s.markStepOK(ctx, cphStep, msg)
	}

	return s.succeedTarget(ctx, op, feature, release, t), nil
}

// commandForAction maps a deployment action to its fixed agent command ID.
// step types must always be derived from this mapping so the scheduler never
// concatenates untrusted strings into a command name.
func commandForAction(action string) string {
	switch action {
	case model.DeploymentActionInstall:
		return "deployment.install"
	case model.DeploymentActionUpdate:
		return "deployment.update"
	case model.DeploymentActionBackup:
		return "deployment.backup"
	case model.DeploymentActionRollback:
		return "deployment.rollback"
	case model.DeploymentActionHealthCheck, "health-check":
		return "deployment.health-check"
	}
	return ""
}

func (s *DeploymentScheduler) runTaskStep(ctx context.Context, op *model.DeploymentOperation, feature *model.DeploymentFeature, release *model.DeploymentRelease, t *model.DeploymentOperationTarget, step *model.DeploymentStep, commandID string) (*model.Task, bool, string, error) {
	cmd, found := s.resolveCommand(ctx, t.NodeID, commandID)
	if !found {
		return nil, false, "agent command missing: " + commandID, nil
	}
	started := time.Now().UTC()
	step.Status = model.DeploymentStatusRunning
	step.CommandID = commandID
	step.StartedAt = &started
	if err := s.store.UpdateDeploymentStep(ctx, step); err != nil {
		return nil, false, "", err
	}

	args, err := s.taskArguments(ctx, op, feature, release, t)
	if err != nil {
		// Freeze mismatch (e.g. secret references changed after the operation
		// was queued) fails the step without ever creating a task.
		return nil, false, s.sanitizeMessage(err.Error()), nil
	}
	task, err := s.tasks.CreateTask(ctx, t.NodeID, "deployment:"+op.ID, model.ActorSystem,
		op.ID+"-"+t.ID+"-"+step.StepType, CreateTaskInput{
			CommandID:      cmd.CommandID,
			CommandVersion: cmd.CommandVersion,
			Arguments:      args,
			TimeoutSeconds: cmd.TimeoutSeconds,
		})
	if err != nil {
		return nil, false, "", err
	}
	step.TaskID = task.ID
	if err := s.store.UpdateDeploymentStep(ctx, step); err != nil {
		return nil, false, "", err
	}

	timeout := time.Duration(task.TimeoutSeconds)*time.Second + 60*time.Second
	done, err := s.pollTask(ctx, task.ID, timeout)
	if err != nil {
		return nil, false, "", err
	}
	if done.Status != model.TaskSucceeded {
		msg := done.ErrorMessage
		if msg == "" {
			msg = "deployment task " + done.Status
		}
		_ = s.failStep(ctx, step, msg)
		return done, false, s.sanitizeMessage(msg), nil
	}
	_ = s.markStepOK(ctx, step, "ok")
	return done, true, "", nil
}

// taskArguments builds the frozen, reference-only arguments handed to the
// agent. It NEVER contains secret bodies, credentials or presigned URLs.
//
// The configuration hash is frozen before execution: legacy operations whose
// FrozenConfigHash is empty are resolved now and backfilled (best effort).
// Secret references are re-resolved at execution time and compared against
// the frozen secret hash; a mismatch fails the target so a concurrent secret
// rotation cannot silently change references mid-operation.
func (s *DeploymentScheduler) taskArguments(ctx context.Context, op *model.DeploymentOperation, feature *model.DeploymentFeature, release *model.DeploymentRelease, t *model.DeploymentOperationTarget) (map[string]any, error) {
	args := map[string]any{
		"operation_id": op.ID,
		"target_id":    t.ID,
		"release_id":   op.ReleaseID,
		"feature_key":  feature.FeatureKey,
		"node_id":      t.NodeID,
		"config_hash":  s.freezeConfigHash(ctx, op, t),
	}
	if release != nil {
		args["release_version"] = release.Version
	}
	refs := s.secretRefs(ctx, op.FeatureID, t.NodeID)
	refsHash := canonicalSecretRefsHash(refs)
	if frozen := t.FrozenSecretHash; frozen != "" {
		if !strings.EqualFold(frozen, refsHash) {
			return nil, errors.New("secret references changed after freeze")
		}
	} else {
		// Legacy operation: backfill the frozen secret hash so a later
		// concurrent rotation is detected.
		t.FrozenSecretHash = refsHash
		if err := s.store.UpdateDeploymentOperationTarget(ctx, t); err != nil {
			s.log.Warn("backfill frozen secret hash failed", "operation_id", op.ID, "target_id", t.ID, "error", err)
		}
	}
	if len(refs) > 0 {
		args["secret_refs"] = refs
	}
	return args, nil
}

// freezeConfigHash returns the operation's frozen config hash, resolving and
// backfilling it at execution time for legacy operations that predate the
// freeze (best effort: a resolution failure leaves the hash empty and the
// node skips the config verification rather than blocking the operation).
func (s *DeploymentScheduler) freezeConfigHash(ctx context.Context, op *model.DeploymentOperation, t *model.DeploymentOperationTarget) string {
	if h := s.frozenConfigHash(op, t); h != "" {
		return h
	}
	tg, err := s.store.DeploymentTargetByID(ctx, t.TargetID)
	if err != nil || tg == nil {
		s.log.Warn("freeze config hash skipped: target missing", "operation_id", op.ID, "target_id", t.TargetID, "error", err)
		return ""
	}
	_, hash, err := s.svc.ResolveConfig(ctx, op.FeatureID, tg)
	if err != nil || hash == "" {
		s.log.Warn("freeze config hash skipped: resolve failed", "operation_id", op.ID, "target_id", t.TargetID, "error", err)
		return ""
	}
	op.FrozenConfigHash = hash
	t.FrozenConfigHash = hash
	if err := s.store.UpdateDeploymentOperation(ctx, op); err != nil {
		s.log.Warn("backfill frozen config hash failed", "operation_id", op.ID, "error", err)
	}
	if err := s.store.UpdateDeploymentOperationTarget(ctx, t); err != nil {
		s.log.Warn("backfill frozen config hash (target) failed", "operation_id", op.ID, "target_id", t.ID, "error", err)
	}
	return hash
}

func (s *DeploymentScheduler) frozenConfigHash(op *model.DeploymentOperation, t *model.DeploymentOperationTarget) string {
	if op.FrozenConfigHash != "" {
		return op.FrozenConfigHash
	}
	return t.FrozenConfigHash
}

// canonicalSecretRefsHash returns the canonical SHA-256 of a secret reference
// list: entries sorted by ref_id, then JSON-serialised. The scheduler uses it
// as the frozen secret hash so a concurrent secret rotation cannot silently
// change references mid-operation.
func canonicalSecretRefsHash(refs []map[string]any) string {
	type refEntry struct {
		RefID          string `json:"ref_id"`
		ObjectKey      string `json:"object_key"`
		Version        string `json:"version"`
		Hash           string `json:"hash"`
		EncryptionMode string `json:"encryption_mode"`
	}
	entries := make([]refEntry, 0, len(refs))
	for _, r := range refs {
		entries = append(entries, refEntry{
			RefID:          strAny(r["ref_id"]),
			ObjectKey:      strAny(r["object_key"]),
			Version:        strAny(r["version"]),
			Hash:           strAny(r["hash"]),
			EncryptionMode: strAny(r["encryption_mode"]),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RefID < entries[j].RefID })
	raw, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func strAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

// secretRefs returns only safe reference fields for the feature's secrets.
func (s *DeploymentScheduler) secretRefs(ctx context.Context, featureID, nodeID string) []map[string]any {
	refs := []map[string]any{}
	scopes := []struct{ scopeType, scopeID string }{
		{model.SecretScopeShared, ""},
		{model.SecretScopeNode, nodeID},
	}
	for _, sc := range scopes {
		rs, err := s.store.ListDeploymentSecretReferences(ctx, featureID, sc.scopeType, sc.scopeID)
		if err != nil {
			s.log.Warn("list deployment secret references failed", "feature_id", featureID, "scope", sc.scopeType, "error", err)
			continue
		}
		for _, r := range rs {
			refs = append(refs, map[string]any{
				"ref_id":          r.ID,
				"object_key":      r.ObjectKey,
				"version":         r.Version,
				"hash":            r.ContentHash,
				"encryption_mode": r.EncryptionMode,
			})
		}
	}
	return refs
}

// resolveCommand finds the newest enabled node command for commandID.
func (s *DeploymentScheduler) resolveCommand(ctx context.Context, nodeID, commandID string) (*model.NodeCommand, bool) {
	cmds, err := s.store.NodeCommands(ctx, nodeID)
	if err != nil {
		return nil, false
	}
	var best *model.NodeCommand
	for _, c := range cmds {
		if c.CommandID != commandID || !c.Enabled {
			continue
		}
		if best == nil || c.CommandVersion > best.CommandVersion {
			best = c
		}
	}
	return best, best != nil
}

// pollTask waits until the task reaches a terminal state or the timeout
// elapses, returning the final task.
func (s *DeploymentScheduler) pollTask(ctx context.Context, taskID string, timeout time.Duration) (*model.Task, error) {
	deadline := time.Now().Add(timeout)
	for {
		tk, err := s.store.TaskByID(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if model.IsTaskTerminal(tk.Status) {
			return tk, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.pollInterval):
		}
		if time.Now().After(deadline) {
			return tk, fmt.Errorf("deployment task %s did not reach a terminal state within %s", taskID, timeout)
		}
	}
}

// controlPlaneHealthCheck performs the control-plane secondary health check
// against the node address and the port from the frozen config.
func (s *DeploymentScheduler) controlPlaneHealthCheck(ctx context.Context, t *model.DeploymentOperationTarget, cfg map[string]any) (bool, string) {
	port := configInt(cfg, "port")
	if port <= 0 {
		return true, "control plane health check skipped: port not configured"
	}
	addr := s.nodeAddress(ctx, t.NodeID)
	if addr == "" {
		return false, "control plane health check failed: no node address available"
	}
	path := "/health"
	if p, ok := cfg["health_path"].(string); ok && p != "" {
		path = p
	}
	url := fmt.Sprintf("http://%s:%d%s", addr, port, path)
	reqCtx, cancel := context.WithTimeout(ctx, s.httpClient.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return false, "control plane health check error: " + err.Error()
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, "control plane health check failed: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, fmt.Sprintf("control plane health check passed (http %d)", resp.StatusCode)
	}
	return false, fmt.Sprintf("control plane health check failed (http %d)", resp.StatusCode)
}

func (s *DeploymentScheduler) nodeAddress(ctx context.Context, nodeID string) string {
	addrs, err := s.store.NodeAddresses(ctx, nodeID)
	if err != nil || len(addrs) == 0 {
		return ""
	}
	// NodeAddresses orders by is_preferred DESC, last_seen_at DESC.
	return addrs[0].Address
}

// targetConfig loads the target's config profile content as a map (best
// effort; an empty map simply disables the HTTP health check).
func (s *DeploymentScheduler) targetConfig(ctx context.Context, t *model.DeploymentOperationTarget) map[string]any {
	cfg := map[string]any{}
	tg, err := s.store.DeploymentTargetByID(ctx, t.TargetID)
	if err != nil || tg == nil || tg.ConfigProfileID == "" {
		return cfg
	}
	prof, err := s.store.DeploymentConfigProfileByID(ctx, tg.ConfigProfileID)
	if err != nil || prof == nil || prof.ContentJSON == "" {
		return cfg
	}
	parsed := map[string]any{}
	if err := json.Unmarshal([]byte(prof.ContentJSON), &parsed); err != nil {
		s.log.Warn("parse deployment config profile failed", "profile_id", prof.ID, "error", err)
		return cfg
	}
	return parsed
}

func configInt(cfg map[string]any, key string) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0
		}
		return int(i)
	case string:
		i, err := strconv.Atoi(v)
		if err != nil {
			return 0
		}
		return i
	}
	return 0
}

// markPreflightDone marks the target's queued preflight step as succeeded.
func (s *DeploymentScheduler) markPreflightDone(ctx context.Context, opID, opTargetID string) {
	steps, err := s.store.ListDeploymentStepsByOperation(ctx, opID)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, st := range steps {
		if st.OperationTargetID == opTargetID && st.StepType == "preflight" && st.Status == model.DeploymentStatusQueued {
			st.Status = model.DeploymentStatusSucceeded
			st.StartedAt = &now
			st.FinishedAt = &now
			st.Message = "preflight ok"
			_ = s.store.UpdateDeploymentStep(ctx, st)
		}
	}
}

func (s *DeploymentScheduler) createStep(ctx context.Context, opID, opTargetID, nodeID, stepType, commandID string) (*model.DeploymentStep, error) {
	st := &model.DeploymentStep{
		ID:                model.NewUUID(),
		OperationID:       opID,
		OperationTargetID: opTargetID,
		NodeID:            nodeID,
		StepType:          stepType,
		Status:            model.DeploymentStatusQueued,
		CommandID:         commandID,
	}
	if err := s.store.CreateDeploymentStep(ctx, st); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *DeploymentScheduler) markStepOK(ctx context.Context, st *model.DeploymentStep, msg string) error {
	now := time.Now().UTC()
	st.Status = model.DeploymentStatusSucceeded
	st.Message = s.sanitizeMessage(msg)
	if st.StartedAt == nil {
		st.StartedAt = &now
	}
	st.FinishedAt = &now
	return s.store.UpdateDeploymentStep(ctx, st)
}

func (s *DeploymentScheduler) failStep(ctx context.Context, st *model.DeploymentStep, msg string) error {
	now := time.Now().UTC()
	st.Status = model.DeploymentStatusFailed
	st.Message = s.sanitizeMessage(msg)
	if st.StartedAt == nil {
		st.StartedAt = &now
	}
	st.FinishedAt = &now
	return s.store.UpdateDeploymentStep(ctx, st)
}

func (s *DeploymentScheduler) succeedTarget(ctx context.Context, op *model.DeploymentOperation, feature *model.DeploymentFeature, release *model.DeploymentRelease, t *model.DeploymentOperationTarget) targetResult {
	now := time.Now().UTC()
	t.Status = model.DeploymentStatusSucceeded
	t.ErrorMessage = ""
	t.FinishedAt = &now
	if err := s.store.UpdateDeploymentOperationTarget(ctx, t); err != nil {
		s.log.Error("update deployment operation target failed", "operation_id", op.ID, "target_id", t.ID, "error", err)
	}
	if tg, err := s.store.DeploymentTargetByID(ctx, t.TargetID); err == nil && tg != nil {
		switch op.Action {
		case model.DeploymentActionInstall, model.DeploymentActionUpdate, model.DeploymentActionRollback:
			tg.CurrentReleaseID = op.ReleaseID
			if op.Action == model.DeploymentActionInstall || op.Action == model.DeploymentActionUpdate {
				tg.LastHealthyReleaseID = op.ReleaseID
			}
			tg.ActualStatus = model.TargetStatusHealthy
			tg.ConfigRevision++
		case model.DeploymentActionHealthCheck:
			tg.ActualStatus = model.TargetStatusHealthy
			tg.LastHealthCheckAt = &now
		}
		if err := s.store.UpdateDeploymentTarget(ctx, tg); err != nil {
			s.log.Error("update deployment target failed", "target_id", tg.ID, "error", err)
		}
	}
	s.audit(ctx, op, "deployment.operation.target", t, map[string]any{
		"operation_id":    op.ID,
		"target_id":       t.ID,
		"node_id":         t.NodeID,
		"feature_key":     feature.FeatureKey,
		"release_version": releaseVersion(release),
		"action":          op.Action,
		"result":          ResultSuccess,
		"config_hash":     s.frozenConfigHash(op, t),
	})
	return targetResult{succeeded: true}
}

func (s *DeploymentScheduler) failTarget(ctx context.Context, op *model.DeploymentOperation, feature *model.DeploymentFeature, release *model.DeploymentRelease, t *model.DeploymentOperationTarget, msg string) targetResult {
	now := time.Now().UTC()
	t.Status = model.DeploymentStatusFailed
	t.ErrorMessage = s.sanitizeMessage(msg)
	t.FinishedAt = &now
	if err := s.store.UpdateDeploymentOperationTarget(ctx, t); err != nil {
		s.log.Error("update deployment operation target failed", "operation_id", op.ID, "target_id", t.ID, "error", err)
	}
	s.audit(ctx, op, "deployment.operation.target", t, map[string]any{
		"operation_id":    op.ID,
		"target_id":       t.ID,
		"node_id":         t.NodeID,
		"feature_key":     feature.FeatureKey,
		"release_version": releaseVersion(release),
		"action":          op.Action,
		"result":          ResultFailure,
		"config_hash":     s.frozenConfigHash(op, t),
	})
	return targetResult{succeeded: false}
}

func (s *DeploymentScheduler) cancelTarget(ctx context.Context, t *model.DeploymentOperationTarget) targetResult {
	now := time.Now().UTC()
	t.Status = model.DeploymentStatusCancelled
	t.ErrorMessage = "operation cancelled"
	t.FinishedAt = &now
	if err := s.store.UpdateDeploymentOperationTarget(ctx, t); err != nil {
		s.log.Error("update deployment operation target failed", "target_id", t.ID, "error", err)
	}
	return targetResult{cancelled: true}
}

func releaseVersion(r *model.DeploymentRelease) string {
	if r == nil {
		return ""
	}
	return r.Version
}

// aliyunAKRe masks Aliyun OSS access-key patterns (LTAI/AKID) that the shared
// Redactor does not cover. Applied locally so step/error text never persists
// credential material.
var aliyunAKRe = regexp.MustCompile(`(?i)\b(LTAI[A-Za-z0-9]{12,20}|AKID[A-Za-z0-9]{16,40})\b`)

// sanitizeMessage redacts secrets before a message is persisted.
func (s *DeploymentScheduler) sanitizeMessage(msg string) string {
	msg = s.redactor.RedactString(msg)
	return aliyunAKRe.ReplaceAllString(msg, "[REDACTED_KEY]")
}

// backupResult is the subset of the runner's result summary consumed here.
type backupResult struct {
	ObjectKey  string `json:"object_key"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	BackupMode string `json:"backup_mode"`
}

func (s *DeploymentScheduler) recordBackupFromTask(ctx context.Context, op *model.DeploymentOperation, feature *model.DeploymentFeature, t *model.DeploymentOperationTarget, step *model.DeploymentStep) {
	tk, err := s.store.TaskByID(ctx, step.TaskID)
	if err != nil || tk == nil || tk.ResultSummaryJSON == "" {
		return
	}
	var res backupResult
	if err := json.Unmarshal([]byte(tk.ResultSummaryJSON), &res); err != nil {
		s.log.Warn("parse backup task result failed", "task_id", step.TaskID, "error", err)
		return
	}
	if res.ObjectKey == "" {
		s.log.Warn("backup task returned no object_key", "task_id", step.TaskID)
		return
	}
	mode := res.BackupMode
	if mode == "" {
		mode = feature.BackupMode
	}
	b := &model.DeploymentBackup{
		OperationID: op.ID,
		TargetID:    t.TargetID,
		NodeID:      t.NodeID,
		FeatureID:   op.FeatureID,
		BackupMode:  mode,
		ObjectKey:   s.backupObjectKey(op, feature.FeatureKey, t.NodeID, res.ObjectKey),
		Size:        res.Size,
		SHA256:      res.SHA256,
		Status:      model.DeploymentStatusSucceeded,
	}
	if err := s.store.CreateDeploymentBackup(ctx, b); err != nil {
		s.log.Error("create deployment backup failed", "error", err)
	}
}

// backupObjectKey prefixes the runner-relative object key with the
// environment-scoped backups directory layout.
func (s *DeploymentScheduler) backupObjectKey(op *model.DeploymentOperation, featureKey, nodeID, rel string) string {
	if strings.HasPrefix(rel, "backups/") {
		return rel
	}
	env := op.EnvironmentID
	if env == "" {
		env = "default"
	}
	now := time.Now().UTC()
	return fmt.Sprintf("backups/%s/%s/%s/%04d/%02d/%02d/%s/%s",
		env, featureKey, nodeID, now.Year(), int(now.Month()), now.Day(), op.ID, strings.TrimPrefix(rel, "/"))
}

// triggerAutoRollback queues a rollback operation to the last healthy release.
// It must be called after the failed update operation is finalized so the
// per-feature partial unique index is free.
//
// Auto rollback is restricted to stateless features (BackupMode == "none")
// with rollback capability and a known last healthy release. Database-backed
// features are never rolled back automatically: the failure is left terminal
// and a human decision is recorded on the failed target.
func (s *DeploymentScheduler) triggerAutoRollback(ctx context.Context, op *model.DeploymentOperation, feature *model.DeploymentFeature, targetID, releaseID string) {
	if releaseID == "" || targetID == "" {
		return
	}
	if feature.BackupMode != "" && feature.BackupMode != "none" {
		s.noteManualRollbackDecision(ctx, op, targetID)
		return
	}
	if feature.RollbackCapability == "" || feature.RollbackCapability == "none" {
		return
	}
	reason := "auto rollback after update health failure (operation " + op.ID + ")"
	rollbackOp, err := s.svc.CreateOperation(ctx, model.ActorSystem, CreateOperationInput{
		Action:    model.DeploymentActionRollback,
		FeatureID: op.FeatureID,
		ReleaseID: releaseID,
		TargetIDs: []string{targetID},
		Reason:    reason,
	})
	if err != nil {
		s.log.Error("auto rollback operation creation failed", "operation_id", op.ID, "target_id", targetID, "release_id", releaseID, "error", err)
		return
	}
	s.log.Info("auto rollback operation created",
		"operation_id", op.ID, "rollback_operation_id", rollbackOp.ID, "target_id", targetID, "release_id", releaseID)
}

// noteManualRollbackDecision records on the failed target that a database
// feature's rollback requires a human decision. It never triggers a rollback
// operation by itself.
func (s *DeploymentScheduler) noteManualRollbackDecision(ctx context.Context, op *model.DeploymentOperation, targetID string) {
	const msg = "database feature: manual rollback decision required"
	opTargets, err := s.store.ListDeploymentOperationTargetsByOperation(ctx, op.ID)
	if err == nil {
		for _, ot := range opTargets {
			if ot.TargetID == targetID && ot.Status == model.DeploymentStatusFailed {
				if !strings.Contains(ot.ErrorMessage, "manual rollback") {
					ot.ErrorMessage = strings.TrimSpace(strings.TrimSpace(ot.ErrorMessage) + "; " + msg)
					if uerr := s.store.UpdateDeploymentOperationTarget(ctx, ot); uerr != nil {
						s.log.Warn("record manual rollback decision failed", "operation_id", op.ID, "target_id", targetID, "error", uerr)
					}
				}
				break
			}
		}
	}
	s.log.Warn(msg, "operation_id", op.ID, "target_id", targetID, "feature_id", op.FeatureID)
}
