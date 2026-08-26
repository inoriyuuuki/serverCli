package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"servercli/internal/config"
	"servercli/internal/db"
	"servercli/internal/logger"
	"servercli/internal/model"
	"servercli/internal/store"
)

// newDeploymentHarness builds an in-memory deployment test environment. No
// network access is required: agent tasks are terminal states written directly
// into the store.
func newDeploymentHarness(t *testing.T) (context.Context, *store.Store, *config.Config, *DeploymentService, *DeploymentScheduler) {
	t.Helper()
	cfg := testCfg(t)
	ctx := context.Background()
	log := logger.New(io.Discard, "error")
	database, err := db.Open(ctx, "sqlite", cfg.DatabaseURL, log)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	st := store.New(database, log)
	settings := NewSettingsService(st, cfg)
	if err := settings.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	auditor := NewAuditor(st, log, cfg.InstanceName+"-env", cfg.InstanceName)
	nodes, err := NewNodeService(st, cfg, log, auditor, settings)
	if err != nil {
		t.Fatal(err)
	}
	tasks := NewTaskService(st, cfg, log, auditor, nodes)
	svc, err := NewDeploymentService(st, cfg, log, auditor, tasks, nodes)
	if err != nil {
		t.Fatal(err)
	}
	sched := NewDeploymentScheduler(svc, st, cfg, log, tasks, nodes)
	sched.pollInterval = 15 * time.Millisecond
	return ctx, st, cfg, svc, sched
}

// ---- seeding helpers ----

func seedDeployNode(t *testing.T, ctx context.Context, st *store.Store, id string) {
	t.Helper()
	n := &model.Node{
		ID:            id,
		EnvironmentID: "env-test",
		InstanceName:  "node-" + id,
		Role:          "child",
		Hostname:      "host-" + id,
		Status:        model.NodeStatusOnline,
		Enabled:       true,
	}
	if err := st.CreateNode(ctx, n); err != nil {
		t.Fatalf("create node %s: %v", id, err)
	}
}

func seedDeployCommand(t *testing.T, ctx context.Context, st *store.Store, nodeID, commandID string) {
	t.Helper()
	c := &model.NodeCommand{
		NodeID:            nodeID,
		CommandID:         commandID,
		CommandVersion:    "1.0.0",
		Category:          "deployment",
		Title:             commandID,
		PermissionProfile: "read-only",
		TimeoutSeconds:    30,
		Enabled:           true,
	}
	if err := st.UpsertNodeCommand(ctx, c); err != nil {
		t.Fatalf("upsert command %s for node %s: %v", commandID, nodeID, err)
	}
}

func seedDeployFeature(t *testing.T, ctx context.Context, st *store.Store, id, key, backupMode, rollback string) {
	t.Helper()
	f := &model.DeploymentFeature{
		ID:                  id,
		FeatureKey:          key,
		Name:                "Feature " + key,
		OS:                  "linux",
		Arch:                "amd64",
		BackupMode:          backupMode,
		RollbackCapability:  rollback,
		MinimumAgentVersion: "1.0.0",
	}
	if err := st.CreateDeploymentFeature(ctx, f); err != nil {
		t.Fatalf("create feature: %v", err)
	}
}

func seedDeployRelease(t *testing.T, ctx context.Context, st *store.Store, featureID, id, version string) {
	t.Helper()
	r := &model.DeploymentRelease{
		ID:         id,
		FeatureID:  featureID,
		Version:    version,
		ObjectKey:  "releases/" + featureID + "/" + version + "/bundle.tar.gz",
		Size:       10,
		SHA256:     "abc",
		BackupMode: "none",
	}
	if err := st.CreateDeploymentRelease(ctx, r); err != nil {
		t.Fatalf("create release: %v", err)
	}
}

func seedDeployTarget(t *testing.T, ctx context.Context, st *store.Store, id, featureID, nodeID, lastHealthy string) {
	t.Helper()
	tg := &model.DeploymentTarget{
		ID:                   id,
		FeatureID:            featureID,
		NodeID:               nodeID,
		ActualStatus:         model.TargetStatusHealthy,
		LastHealthyReleaseID: lastHealthy,
		Enabled:              true,
	}
	if err := st.CreateDeploymentTarget(ctx, tg); err != nil {
		t.Fatalf("create target: %v", err)
	}
}

// ---- wait/result helpers ----

func waitForOpStatus(t *testing.T, ctx context.Context, st *store.Store, id, status string) *model.DeploymentOperation {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		op, err := st.DeploymentOperationByID(ctx, id)
		if err == nil && op.Status == status {
			return op
		}
		time.Sleep(10 * time.Millisecond)
	}
	op, _ := st.DeploymentOperationByID(ctx, id)
	t.Fatalf("operation %s did not reach %s (last: %+v)", id, status, op)
	return nil
}

func waitForTaskStep(t *testing.T, ctx context.Context, st *store.Store, opID, stepType string) *model.Task {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		steps, err := st.ListDeploymentStepsByOperation(ctx, opID)
		if err == nil {
			for _, stp := range steps {
				if stp.StepType == stepType && stp.TaskID != "" {
					tk, err := st.TaskByID(ctx, stp.TaskID)
					// Only match a task the scheduler is still waiting on; a
					// terminal task may belong to a previous execution pass.
					if err == nil && tk != nil && !model.IsTaskTerminal(tk.Status) {
						return tk
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pending task for step %q of operation %s within timeout", stepType, opID)
	return nil
}

// finalizeTargets marks every operation target terminal (mirrors scheduler
// finalization so a test can release the node-level serial index).
func finalizeTargets(t *testing.T, ctx context.Context, st *store.Store, opID, status string) {
	t.Helper()
	now := time.Now().UTC()
	targets, err := st.ListDeploymentOperationTargetsByOperation(ctx, opID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		if isOperationTargetTerminal(tg.Status) {
			continue
		}
		tg.Status = status
		tg.FinishedAt = &now
		if err := st.UpdateDeploymentOperationTarget(ctx, tg); err != nil {
			t.Fatal(err)
		}
	}
}

func finishTask(t *testing.T, ctx context.Context, st *store.Store, taskID, status, errMsg, resultSummary string) {
	t.Helper()
	tk, err := st.TaskByID(ctx, taskID)
	if err != nil {
		t.Fatalf("task %s: %v", taskID, err)
	}
	now := time.Now().UTC()
	tk.Status = status
	tk.FinishedAt = &now
	tk.ErrorMessage = errMsg
	tk.ResultSummaryJSON = resultSummary
	if err := st.UpdateTask(ctx, tk); err != nil {
		t.Fatalf("update task %s: %v", taskID, err)
	}
}

// ---- input mutation helpers ----

func withAction(in CreateOperationInput, action string) CreateOperationInput {
	in.Action = action
	return in
}

func withFeature(in CreateOperationInput, featureID string) CreateOperationInput {
	in.FeatureID = featureID
	return in
}

func withRelease(in CreateOperationInput, releaseID string) CreateOperationInput {
	in.ReleaseID = releaseID
	return in
}

func withTargets(in CreateOperationInput, ids ...string) CreateOperationInput {
	in.TargetIDs = ids
	return in
}

// ---- tests ----

func TestCreateOperationValidation(t *testing.T) {
	ctx, st, cfg, svc, _ := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployTarget(t, ctx, st, "t-1", "f-1", "n-1", "")

	base := CreateOperationInput{Action: model.DeploymentActionInstall, FeatureID: "f-1", ReleaseID: "r-1", TargetIDs: []string{"t-1"}, Reason: "ok"}

	if _, err := svc.CreateOperation(ctx, "admin-1", withAction(base, "nope")); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("invalid action: got %v, want ErrBadRequest", err)
	}

	// production requires a non-empty reason
	cfg.AppEnv = "production"
	if _, err := svc.CreateOperation(ctx, "admin-1", withTargets(base)); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("production without reason: got %v, want ErrBadRequest", err)
	}
	cfg.AppEnv = "test"

	if _, err := svc.CreateOperation(ctx, "admin-1", withFeature(base, "f-missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing feature: got %v, want ErrNotFound", err)
	}
	if _, err := svc.CreateOperation(ctx, "admin-1", withRelease(base, "r-missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing release: got %v, want ErrNotFound", err)
	}
	if _, err := svc.CreateOperation(ctx, "admin-1", withTargets(base, "t-missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing target: got %v, want ErrNotFound", err)
	}
	if _, err := svc.CreateOperation(ctx, "admin-1", withTargets(base)); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("empty targets: got %v, want ErrBadRequest", err)
	}

	if _, err := svc.CreateOperation(ctx, "admin-1", base); err != nil {
		t.Fatalf("create: %v", err)
	}
	// second active operation for the same feature must conflict
	if _, err := svc.CreateOperation(ctx, "admin-1", base); !errors.Is(err, ErrConflict) {
		t.Fatalf("concurrent active op: got %v, want ErrConflict", err)
	}
}

func TestCreateOperationSuccess(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployTarget(t, ctx, st, "t-1", "f-1", "n-1", "")

	op, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionInstall, FeatureID: "f-1", ReleaseID: "r-1", TargetIDs: []string{"t-1"}, Reason: "ok",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if op.Status != model.DeploymentStatusQueued || op.Strategy != "serial" || op.Action != model.DeploymentActionInstall {
		t.Fatalf("unexpected operation: %+v", op)
	}
	detail, err := svc.GetOperationDetail(ctx, op.ID)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if len(detail.Targets) != 1 || detail.Targets[0].Status != model.DeploymentStatusQueued {
		t.Fatalf("targets: %+v", detail.Targets)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].StepType != "preflight" || detail.Steps[0].Status != model.DeploymentStatusQueued {
		t.Fatalf("steps: %+v", detail.Steps)
	}
}

func TestCancelOperationStateMachine(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployTarget(t, ctx, st, "t-1", "f-1", "n-1", "")

	// queued operations are cancellable
	op, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionInstall, FeatureID: "f-1", ReleaseID: "r-1", TargetIDs: []string{"t-1"}, Reason: "ok",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cancelled, err := svc.CancelOperation(ctx, "admin-1", op.ID, "cancel it")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != model.DeploymentStatusCancelled {
		t.Fatalf("status: %s", cancelled.Status)
	}
	detail, _ := svc.GetOperationDetail(ctx, op.ID)
	if detail.Targets[0].Status != model.DeploymentStatusCancelled {
		t.Fatalf("target should be cancelled: %+v", detail.Targets[0])
	}

	// succeeded operations are not cancellable
	op2, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionInstall, FeatureID: "f-1", ReleaseID: "r-1", TargetIDs: []string{"t-1"}, Reason: "ok",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Now().UTC()
	op2.Status = model.DeploymentStatusSucceeded
	op2.FinishedAt = &now
	if err := st.UpdateDeploymentOperation(ctx, op2); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelOperation(ctx, "admin-1", op2.ID, "x"); !errors.Is(err, ErrTerminal) {
		t.Fatalf("cancel succeeded op: got %v, want ErrTerminal", err)
	}
}

func TestContinueOperationStateMachine(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployTarget(t, ctx, st, "t-1", "f-1", "n-1", "")

	now := time.Now().UTC()
	create := func() *model.DeploymentOperation {
		t.Helper()
		op, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
			Action: model.DeploymentActionInstall, FeatureID: "f-1", ReleaseID: "r-1", TargetIDs: []string{"t-1"}, Reason: "ok",
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return op
	}

	// succeeded operations cannot continue
	suc := create()
	suc.Status = model.DeploymentStatusSucceeded
	suc.FinishedAt = &now
	if err := st.UpdateDeploymentOperation(ctx, suc); err != nil {
		t.Fatal(err)
	}
	finalizeTargets(t, ctx, st, suc.ID, model.DeploymentStatusSucceeded)
	if _, err := svc.ContinueOperation(ctx, "admin-1", suc.ID); !errors.Is(err, ErrTerminal) {
		t.Fatalf("continue succeeded op: got %v, want ErrTerminal", err)
	}

	// failed operation with a pending target can continue
	op := create()
	op.Status = model.DeploymentStatusFailed
	op.FinishedAt = &now
	if err := st.UpdateDeploymentOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	cont, err := svc.ContinueOperation(ctx, "admin-1", op.ID)
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if cont.Status != model.DeploymentStatusQueued || cont.FinishedAt != nil {
		t.Fatalf("continue result: %+v", cont)
	}
}

func TestSchedulerTickNoQueued(t *testing.T) {
	ctx, _, _, _, sched := newDeploymentHarness(t)
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("tick with no queued operations: %v", err)
	}
}

func TestSchedulerInstallSuccess(t *testing.T) {
	ctx, st, _, svc, sched := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.install")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.health-check")
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployTarget(t, ctx, st, "t-1", "f-1", "n-1", "")

	op, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionInstall, FeatureID: "f-1", ReleaseID: "r-1", TargetIDs: []string{"t-1"}, Reason: "ok",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	tickDone := make(chan error, 1)
	go func() { tickDone <- sched.Tick(ctx) }()

	// The main install task must use the fixed deployment.install command.
	task := waitForTaskStep(t, ctx, st, op.ID, "install")
	if task.CommandID != "deployment.install" {
		t.Fatalf("install task command = %q, want deployment.install", task.CommandID)
	}
	finishTask(t, ctx, st, task.ID, model.TaskSucceeded, "", "")

	// A real local health-check task is dispatched before the control plane
	// probe: same arguments, step_type "health-check", command
	// deployment.health-check.
	hcTask := waitForTaskStep(t, ctx, st, op.ID, "health-check")
	if hcTask.CommandID != "deployment.health-check" {
		t.Fatalf("health-check task command = %q, want deployment.health-check", hcTask.CommandID)
	}
	if hcTask.ArgumentsJSON == "" || !strings.Contains(hcTask.ArgumentsJSON, `"operation_id"`) {
		t.Fatalf("health-check task missing operation args: %s", hcTask.ArgumentsJSON)
	}
	finishTask(t, ctx, st, hcTask.ID, model.TaskSucceeded, "", "")

	final := waitForOpStatus(t, ctx, st, op.ID, model.DeploymentStatusSucceeded)
	if final.Status != model.DeploymentStatusSucceeded {
		t.Fatalf("final status: %s", final.Status)
	}
	if err := <-tickDone; err != nil {
		t.Fatalf("tick: %v", err)
	}

	detail, _ := svc.GetOperationDetail(ctx, op.ID)
	if len(detail.Targets) != 1 || detail.Targets[0].Status != model.DeploymentStatusSucceeded {
		t.Fatalf("targets: %+v", detail.Targets)
	}
	var foundHC bool
	for _, stp := range detail.Steps {
		if stp.StepType == "health-check" {
			foundHC = true
			if stp.Status != model.DeploymentStatusSucceeded || stp.CommandID != "deployment.health-check" {
				t.Fatalf("health-check step: %+v", stp)
			}
		}
	}
	if !foundHC {
		t.Fatalf("no health-check step recorded; steps: %+v", detail.Steps)
	}
	tg, err := st.DeploymentTargetByID(ctx, "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if tg.CurrentReleaseID != "r-1" || tg.LastHealthyReleaseID != "r-1" || tg.ConfigRevision != 1 || tg.ActualStatus != model.TargetStatusHealthy {
		t.Fatalf("target not updated: %+v", tg)
	}
}

func TestSchedulerFailStop(t *testing.T) {
	ctx, st, _, svc, sched := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployNode(t, ctx, st, "n-2")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.install")
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployTarget(t, ctx, st, "t-1", "f-1", "n-1", "")
	seedDeployTarget(t, ctx, st, "t-2", "f-1", "n-2", "")

	op, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionInstall, FeatureID: "f-1", ReleaseID: "r-1", TargetIDs: []string{"t-1", "t-2"}, Reason: "ok",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	tickDone := make(chan error, 1)
	go func() { tickDone <- sched.Tick(ctx) }()

	task := waitForTaskStep(t, ctx, st, op.ID, "install")
	finishTask(t, ctx, st, task.ID, model.TaskFailed, "boom", "")

	final := waitForOpStatus(t, ctx, st, op.ID, model.DeploymentStatusFailed)
	if final.Status != model.DeploymentStatusFailed {
		t.Fatalf("final status: %s", final.Status)
	}
	if err := <-tickDone; err != nil {
		t.Fatalf("tick: %v", err)
	}

	detail, _ := svc.GetOperationDetail(ctx, op.ID)
	if len(detail.Targets) != 2 {
		t.Fatalf("targets: %+v", detail.Targets)
	}
	if detail.Targets[0].Status != model.DeploymentStatusFailed {
		t.Fatalf("first target should be failed: %+v", detail.Targets[0])
	}
	if detail.Targets[1].Status != model.DeploymentStatusQueued {
		t.Fatalf("second target should not have executed: %+v", detail.Targets[1])
	}
}

func TestSchedulerAutoRollback(t *testing.T) {
	ctx, st, _, svc, sched := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.update")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.health-check")
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "yes")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployRelease(t, ctx, st, "f-1", "r-2", "2.0.0")
	// A configured port with no node address makes the control-plane health
	// check fail deterministically.
	prof := &model.DeploymentConfigProfile{
		ID: "p-1", Name: "cfg", ScopeType: model.ConfigScopeShared, FeatureID: "f-1",
		ContentJSON: `{"port": 1}`, ContentHash: "h", Version: 1,
	}
	if err := st.CreateDeploymentConfigProfile(ctx, prof); err != nil {
		t.Fatalf("create config profile: %v", err)
	}
	tg := &model.DeploymentTarget{
		ID: "t-1", FeatureID: "f-1", NodeID: "n-1", ConfigProfileID: "p-1",
		ActualStatus: model.TargetStatusHealthy, CurrentReleaseID: "r-1", LastHealthyReleaseID: "r-1", Enabled: true,
	}
	if err := st.CreateDeploymentTarget(ctx, tg); err != nil {
		t.Fatalf("create target: %v", err)
	}

	op, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionUpdate, FeatureID: "f-1", ReleaseID: "r-2", TargetIDs: []string{"t-1"}, Reason: "upgrade",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	tickDone := make(chan error, 1)
	go func() { tickDone <- sched.Tick(ctx) }()

	task := waitForTaskStep(t, ctx, st, op.ID, "update")
	finishTask(t, ctx, st, task.ID, model.TaskSucceeded, "", "")

	// The local health-check task fails -> the update target fails and the
	// stateless feature is auto-rolled back.
	hcTask := waitForTaskStep(t, ctx, st, op.ID, "health-check")
	if hcTask.CommandID != "deployment.health-check" {
		t.Fatalf("health-check command = %q", hcTask.CommandID)
	}
	finishTask(t, ctx, st, hcTask.ID, model.TaskFailed, "health check hook failed", "")

	final := waitForOpStatus(t, ctx, st, op.ID, model.DeploymentStatusFailed)
	if final.Status != model.DeploymentStatusFailed {
		t.Fatalf("final status: %s", final.Status)
	}
	if err := <-tickDone; err != nil {
		t.Fatalf("tick: %v", err)
	}

	// The rollback operation must exist and target the last healthy release.
	var rb *model.DeploymentOperation
	ops, err := st.ListDeploymentOperations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if o.Action == model.DeploymentActionRollback {
			rb = o
			break
		}
	}
	if rb == nil {
		t.Fatalf("no rollback operation created; ops: %+v", ops)
	}
	if rb.ReleaseID != "r-1" || rb.Status != model.DeploymentStatusQueued {
		t.Fatalf("rollback operation: %+v", rb)
	}
	rbDetail, err := svc.GetOperationDetail(ctx, rb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rbDetail.Targets) != 1 || rbDetail.Targets[0].TargetID != "t-1" {
		t.Fatalf("rollback targets: %+v", rbDetail.Targets)
	}
}

func TestSchedulerBackupRecorded(t *testing.T) {
	ctx, st, _, svc, sched := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.backup")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.update")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.health-check")
	seedDeployFeature(t, ctx, st, "f-1", "app", "filesystem_quiesced", "none")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployRelease(t, ctx, st, "f-1", "r-2", "2.0.0")
	seedDeployTarget(t, ctx, st, "t-1", "f-1", "n-1", "r-1")

	op, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionUpdate, FeatureID: "f-1", ReleaseID: "r-2", TargetIDs: []string{"t-1"}, Reason: "upgrade",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	tickDone := make(chan error, 1)
	go func() { tickDone <- sched.Tick(ctx) }()

	backupTask := waitForTaskStep(t, ctx, st, op.ID, "backup")
	finishTask(t, ctx, st, backupTask.ID, model.TaskSucceeded, "",
		`{"object_key":"snapshot.tar.gz","size":123,"sha256":"deadbeef","backup_mode":"filesystem_quiesced"}`)

	updateTask := waitForTaskStep(t, ctx, st, op.ID, "update")
	finishTask(t, ctx, st, updateTask.ID, model.TaskSucceeded, "", "")

	hcTask := waitForTaskStep(t, ctx, st, op.ID, "health-check")
	finishTask(t, ctx, st, hcTask.ID, model.TaskSucceeded, "", "")

	final := waitForOpStatus(t, ctx, st, op.ID, model.DeploymentStatusSucceeded)
	if final.Status != model.DeploymentStatusSucceeded {
		t.Fatalf("final status: %s", final.Status)
	}
	if err := <-tickDone; err != nil {
		t.Fatalf("tick: %v", err)
	}

	backups, err := st.ListDeploymentBackups(ctx, "f-1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups: %d", len(backups))
	}
	b := backups[0]
	if b.Size != 123 || b.SHA256 != "deadbeef" || b.BackupMode != "filesystem_quiesced" {
		t.Fatalf("backup: %+v", b)
	}
	if !strings.HasPrefix(b.ObjectKey, "backups/env-test/app/n-1/") ||
		!strings.HasSuffix(b.ObjectKey, "/"+op.ID+"/snapshot.tar.gz") {
		t.Fatalf("backup object key: %s", b.ObjectKey)
	}
}

func TestSchedulerContinueAfterPartialFailure(t *testing.T) {
	ctx, st, _, svc, sched := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployNode(t, ctx, st, "n-2")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.install")
	seedDeployCommand(t, ctx, st, "n-2", "deployment.install")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.health-check")
	seedDeployCommand(t, ctx, st, "n-2", "deployment.health-check")
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployTarget(t, ctx, st, "t-1", "f-1", "n-1", "")
	seedDeployTarget(t, ctx, st, "t-2", "f-1", "n-2", "")

	op, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionInstall, FeatureID: "f-1", ReleaseID: "r-1", TargetIDs: []string{"t-1", "t-2"}, Reason: "ok",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// First pass: target 1 fails.
	done := make(chan error, 1)
	go func() { done <- sched.Tick(ctx) }()
	task := waitForTaskStep(t, ctx, st, op.ID, "install")
	finishTask(t, ctx, st, task.ID, model.TaskFailed, "boom", "")
	waitForOpStatus(t, ctx, st, op.ID, model.DeploymentStatusFailed)
	if err := <-done; err != nil {
		t.Fatalf("tick1: %v", err)
	}

	// Continue requeues the operation.
	if _, err := svc.ContinueOperation(ctx, "admin-1", op.ID); err != nil {
		t.Fatalf("continue: %v", err)
	}

	// Second pass: target 2 succeeds; the operation ends partial_failed.
	go func() { done <- sched.Tick(ctx) }()
	task2 := waitForTaskStep(t, ctx, st, op.ID, "install")
	finishTask(t, ctx, st, task2.ID, model.TaskSucceeded, "", "")
	hc2 := waitForTaskStep(t, ctx, st, op.ID, "health-check")
	finishTask(t, ctx, st, hc2.ID, model.TaskSucceeded, "", "")
	waitForOpStatus(t, ctx, st, op.ID, model.DeploymentStatusPartialFailed)
	if err := <-done; err != nil {
		t.Fatalf("tick2: %v", err)
	}

	detail, _ := svc.GetOperationDetail(ctx, op.ID)
	if detail.Targets[0].Status != model.DeploymentStatusFailed || detail.Targets[1].Status != model.DeploymentStatusSucceeded {
		t.Fatalf("targets: %+v", detail.Targets)
	}
}

func TestCommandForActionMapping(t *testing.T) {
	cases := map[string]string{
		model.DeploymentActionInstall:     "deployment.install",
		model.DeploymentActionUpdate:      "deployment.update",
		model.DeploymentActionBackup:      "deployment.backup",
		model.DeploymentActionRollback:    "deployment.rollback",
		model.DeploymentActionHealthCheck: "deployment.health-check",
		"health-check":                    "deployment.health-check",
		"bogus":                           "",
	}
	for action, want := range cases {
		if got := commandForAction(action); got != want {
			t.Fatalf("commandForAction(%q) = %q, want %q", action, got, want)
		}
	}
}

func TestSchedulerNoAutoRollbackForDatabaseFeature(t *testing.T) {
	ctx, st, _, svc, sched := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.update")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.health-check")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.backup")
	// Database-backed feature: auto rollback must NOT be triggered.
	seedDeployFeature(t, ctx, st, "f-1", "app", "database_dump", "yes")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployRelease(t, ctx, st, "f-1", "r-2", "2.0.0")
	tg := &model.DeploymentTarget{
		ID: "t-1", FeatureID: "f-1", NodeID: "n-1",
		ActualStatus: model.TargetStatusHealthy, CurrentReleaseID: "r-1", LastHealthyReleaseID: "r-1", Enabled: true,
	}
	if err := st.CreateDeploymentTarget(ctx, tg); err != nil {
		t.Fatalf("create target: %v", err)
	}

	op, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionUpdate, FeatureID: "f-1", ReleaseID: "r-2", TargetIDs: []string{"t-1"}, Reason: "upgrade",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	tickDone := make(chan error, 1)
	go func() { tickDone <- sched.Tick(ctx) }()

	// Database-backed features run a backup step before the update.
	backupTask := waitForTaskStep(t, ctx, st, op.ID, "backup")
	finishTask(t, ctx, st, backupTask.ID, model.TaskSucceeded, "",
		`{"object_key":"snapshot.tar.gz","size":1,"sha256":"deadbeef","backup_mode":"database_dump"}`)
	task := waitForTaskStep(t, ctx, st, op.ID, "update")
	finishTask(t, ctx, st, task.ID, model.TaskSucceeded, "", "")
	hcTask := waitForTaskStep(t, ctx, st, op.ID, "health-check")
	finishTask(t, ctx, st, hcTask.ID, model.TaskFailed, "health check hook failed", "")

	final := waitForOpStatus(t, ctx, st, op.ID, model.DeploymentStatusFailed)
	if final.Status != model.DeploymentStatusFailed {
		t.Fatalf("final status: %s", final.Status)
	}
	if err := <-tickDone; err != nil {
		t.Fatalf("tick: %v", err)
	}

	// No rollback operation may exist for a database-backed feature.
	ops, err := st.ListDeploymentOperations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if o.Action == model.DeploymentActionRollback {
			t.Fatalf("unexpected auto rollback for database feature: %+v", o)
		}
	}

	// The failed target must carry the manual-decision message.
	detail, _ := svc.GetOperationDetail(ctx, op.ID)
	if len(detail.Targets) != 1 {
		t.Fatalf("targets: %+v", detail.Targets)
	}
	if detail.Targets[0].Status != model.DeploymentStatusFailed ||
		!strings.Contains(detail.Targets[0].ErrorMessage, "database feature: manual rollback decision required") {
		t.Fatalf("target error missing manual decision: %+v", detail.Targets[0])
	}
}

func TestSchedulerSecretFreezeMismatchFailsTarget(t *testing.T) {
	ctx, st, _, svc, sched := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployCommand(t, ctx, st, "n-1", "deployment.install")
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployTarget(t, ctx, st, "t-1", "f-1", "n-1", "")

	// A secret reference exists when the operation is created, so the frozen
	// secret hash captures it.
	ref := &model.DeploymentSecretReference{
		ID: "sr-1", Name: "db", FeatureID: "f-1", ScopeType: model.SecretScopeNode, ScopeID: "n-1",
		ObjectKey: "secrets/nodes/n-1/app.secrets.yaml", Version: 1,
		ContentHash: "hash-v1", EncryptionMode: "none", Size: 10,
	}
	if err := st.CreateDeploymentSecretReference(ctx, ref); err != nil {
		t.Fatalf("create secret ref: %v", err)
	}

	op, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionInstall, FeatureID: "f-1", ReleaseID: "r-1", TargetIDs: []string{"t-1"}, Reason: "ok",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Rotate the secret reference after the operation was frozen: version and
	// content hash change, so the execution-time recomputation must mismatch.
	ref.Version = 2
	ref.ContentHash = "hash-v2"
	if err := st.UpdateDeploymentSecretReference(ctx, ref); err != nil {
		t.Fatalf("update secret ref: %v", err)
	}

	tickDone := make(chan error, 1)
	go func() { tickDone <- sched.Tick(ctx) }()
	final := waitForOpStatus(t, ctx, st, op.ID, model.DeploymentStatusFailed)
	if final.Status != model.DeploymentStatusFailed {
		t.Fatalf("final status: %s", final.Status)
	}
	if err := <-tickDone; err != nil {
		t.Fatalf("tick: %v", err)
	}

	detail, _ := svc.GetOperationDetail(ctx, op.ID)
	if len(detail.Targets) != 1 || detail.Targets[0].Status != model.DeploymentStatusFailed {
		t.Fatalf("targets: %+v", detail.Targets)
	}
	if !strings.Contains(detail.Targets[0].ErrorMessage, "secret references changed after freeze") {
		t.Fatalf("target error: %q", detail.Targets[0].ErrorMessage)
	}
}
