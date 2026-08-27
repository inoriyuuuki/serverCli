package service

import (
	"errors"
	"testing"
	"time"

	"servercli/internal/model"
	"servercli/internal/store"
)

func TestCreateOperationRestoreValidation(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	seedDeployFeature(t, ctx, st, "f", "app", "application_snapshot", "true")
	seedDeployNode(t, ctx, st, "n1")
	seedDeployNode(t, ctx, st, "n2")
	seedDeployRelease(t, ctx, st, "f", "rel1", "0.1.0")
	tg := &model.DeploymentTarget{ID: "t1", FeatureID: "f", NodeID: "n1", Enabled: true}
	if err := st.CreateDeploymentTarget(ctx, tg); err != nil {
		t.Fatal(err)
	}

	// 先建一条真实 operation（deployment_backup.operation_id 有外键）
	seedOp := &model.DeploymentOperation{ID: model.NewUUID(), Action: model.DeploymentActionBackup, FeatureID: "f", Strategy: "serial", Status: model.DeploymentStatusSucceeded, CreatedAt: time.Now().UTC()}
	if err := st.CreateDeploymentOperation(ctx, seedOp); err != nil {
		t.Fatal(err)
	}
	// 缺 backup_id
	if _, err := svc.CreateOperation(ctx, "admin", CreateOperationInput{Action: model.DeploymentActionRestore, FeatureID: "f", TargetIDs: []string{"t1"}}); err == nil {
		t.Fatal("restore without backup_id should fail")
	}
	// 备份属于其他节点
	bOther := &model.DeploymentBackup{ID: model.NewUUID(), OperationID: seedOp.ID, TargetID: "t1", NodeID: "n2", FeatureID: "f", BackupMode: "application_snapshot", ObjectKey: "backups/e/f/n2/2026/08/27/o/backup.tar.gz", Size: 1, SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Status: model.DeploymentStatusSucceeded}
	if err := st.CreateDeploymentBackup(ctx, bOther); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateOperation(ctx, "admin", CreateOperationInput{Action: model.DeploymentActionRestore, FeatureID: "f", TargetIDs: []string{"t1"}, BackupID: bOther.ID}); err == nil {
		t.Fatal("restore with backup of another node should fail")
	}
	// 有效备份（同节点 + succeeded）
	b := &model.DeploymentBackup{ID: model.NewUUID(), OperationID: seedOp.ID, TargetID: "t1", NodeID: "n1", FeatureID: "f", BackupMode: "application_snapshot", ObjectKey: "backups/e/f/n1/2026/08/27/o1/backup.tar.gz", Size: 1, SHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Status: model.DeploymentStatusSucceeded}
	if err := st.CreateDeploymentBackup(ctx, b); err != nil {
		t.Fatal(err)
	}
	op, err := svc.CreateOperation(ctx, "admin", CreateOperationInput{Action: model.DeploymentActionRestore, FeatureID: "f", TargetIDs: []string{"t1"}, BackupID: b.ID, Reason: "restore"})
	if err != nil {
		t.Fatalf("valid restore should succeed: %v", err)
	}
	if op.BackupID != b.ID {
		t.Fatalf("operation backup_id = %q, want %q", op.BackupID, b.ID)
	}
}

func TestCreateOperationBatchAllTargets(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	seedDeployFeature(t, ctx, st, "f", "app", "application_snapshot", "true")
	seedDeployNode(t, ctx, st, "n1")
	seedDeployNode(t, ctx, st, "n2")
	seedDeployRelease(t, ctx, st, "f", "rel1", "0.1.0")
	seedDeployTarget(t, ctx, st, "t1", "f", "n1", "")
	seedDeployTarget(t, ctx, st, "t2", "f", "n2", "")
	seedDeployNode(t, ctx, st, "n3")
	// disabled target 应被排除（放在独立节点避免 (feature,node) 唯一冲突）
	disabled := &model.DeploymentTarget{ID: "t3", FeatureID: "f", NodeID: "n3", Enabled: false}
	if err := st.CreateDeploymentTarget(ctx, disabled); err != nil {
		t.Fatal(err)
	}

	op, err := svc.CreateOperation(ctx, "admin", CreateOperationInput{Action: model.DeploymentActionBackup, FeatureID: "f", TargetIDs: nil, Reason: "all"})
	if err != nil {
		t.Fatalf("batch all: %v", err)
	}
	targets, err := st.ListDeploymentOperationTargetsByOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, x := range targets {
		ids[x.TargetID] = true
	}
	if len(ids) != 2 || !ids["t1"] || !ids["t2"] || ids["t3"] {
		t.Fatalf("batch all targets = %v, want t1+t2 (disabled excluded)", ids)
	}

	// node_id 收窄（先终结第一个 op 及其 operation_targets，避免同 feature 活跃
	// 操作与节点串行索引冲突）
	op.Status = model.DeploymentStatusSucceeded
	_ = st.UpdateDeploymentOperation(ctx, op)
	if ots1, e := st.ListDeploymentOperationTargetsByOperation(ctx, op.ID); e == nil {
		for _, x := range ots1 {
			x.Status = model.DeploymentStatusSucceeded
			_ = st.UpdateDeploymentOperationTarget(ctx, x)
		}
	}
	op2, err := svc.CreateOperation(ctx, "admin", CreateOperationInput{Action: model.DeploymentActionBackup, FeatureID: "f", TargetIDs: nil, NodeID: "n1", Reason: "node"})
	if err != nil {
		t.Fatalf("batch by node: %v", err)
	}
	t2, _ := st.ListDeploymentOperationTargetsByOperation(ctx, op2.ID)
	if len(t2) != 1 || t2[0].NodeID != "n1" {
		t.Fatalf("batch by node targets = %+v, want only n1", t2)
	}
}

func TestSchedulerSkipWhenAlreadyDesired(t *testing.T) {
	ctx, st, _, svc, sched := newDeploymentHarness(t)
	seedDeployFeature(t, ctx, st, "f", "app", "application_snapshot", "true")
	seedDeployNode(t, ctx, st, "n1")
	seedDeployRelease(t, ctx, st, "f", "rel1", "0.1.0")
	tg := &model.DeploymentTarget{ID: "t1", FeatureID: "f", NodeID: "n1", Enabled: true, CurrentReleaseID: "rel1", DesiredReleaseID: "rel1"}
	if err := st.CreateDeploymentTarget(ctx, tg); err != nil {
		t.Fatal(err)
	}
	op, err := svc.CreateOperation(ctx, "admin", CreateOperationInput{Action: model.DeploymentActionInstall, FeatureID: "f", TargetIDs: []string{"t1"}, ReleaseID: "rel1", Reason: "install"})
	if err != nil {
		t.Fatalf("create install op: %v", err)
	}
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	ots, err := st.ListDeploymentOperationTargetsByOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ots) != 1 || ots[0].Status != model.DeploymentStatusSkipped {
		t.Fatalf("install on already-desired target = %+v, want skipped", ots)
	}
	final, _ := st.DeploymentOperationByID(ctx, op.ID)
	if final.Status != model.DeploymentStatusSucceeded {
		t.Fatalf("operation status = %s, want succeeded", final.Status)
	}
}

func TestRunBackupsForNodeNoTargets(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	seedDeployFeature(t, ctx, st, "f", "app", "application_snapshot", "true")
	seedDeployNode(t, ctx, st, "n1")
	_, err := svc.RunBackupsForNode(ctx, "admin", "n1", "f")
	if !errors.Is(err, ErrNotFound) && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RunBackupsForNode with no enabled targets = %v, want not found", err)
	}
}
