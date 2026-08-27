package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"servercli/internal/model"
	"servercli/internal/secret"
	"servercli/internal/store"
)

// CreateOperationInput is the validated input for queueing a deployment
// operation. Secrets are never part of this payload: the scheduler later
// freezes references only.
type CreateOperationInput struct {
	Action    string   `json:"action"`
	FeatureID string   `json:"feature_id"`
	ReleaseID string   `json:"release_id"`
	TargetIDs []string `json:"target_ids"`
	// 批量筛选：target_ids 为空时使用"该 Feature 下全部已启用的 Target"
	// （可再用 NodeID 收窄到某台服务器）。
	NodeID string `json:"node_id,omitempty"`
	// restore 专用：使用的备份记录；force_delete=true 时允许先删除目标已有数据。
	BackupID    string `json:"backup_id,omitempty"`
	ForceDelete bool   `json:"force_delete,omitempty"`
	Reason      string `json:"reason"`
}

// OperationDetail is the full view of an operation: its targets and steps.
type OperationDetail struct {
	Operation *model.DeploymentOperation         `json:"operation"`
	Targets   []*model.DeploymentOperationTarget `json:"targets"`
	Steps     []*model.DeploymentStep            `json:"steps"`
}

// isOperationTargetTerminal reports whether an operation target reached a
// final state and must not be re-processed by the scheduler.
func isOperationTargetTerminal(status string) bool {
	switch status {
	case model.DeploymentStatusSucceeded, model.DeploymentStatusFailed, model.DeploymentStatusSkipped,
		model.DeploymentStatusCancelled, model.DeploymentStatusRolledBack,
		model.DeploymentStatusRollbackFailed:
		return true
	}
	return false
}

// CreateOperation validates and queues a deployment operation. It performs a
// feature-level concurrency guard and relies on the partial unique indexes
// (0007 feature-level, 0008 node-level) as the authoritative backstop. The
// operation, its targets and their steps are created in one transaction.
func (s *DeploymentService) CreateOperation(ctx context.Context, actorID string, in CreateOperationInput) (*model.DeploymentOperation, error) {
	switch in.Action {
	case model.DeploymentActionInstall, model.DeploymentActionUpdate,
		model.DeploymentActionBackup, model.DeploymentActionRollback,
		model.DeploymentActionHealthCheck, model.DeploymentActionRestore:
	default:
		return nil, ErrBadRequest
	}
	if in.FeatureID == "" {
		return nil, ErrBadRequest
	}
	// release_id is required for install/update/rollback (they consume a
	// release bundle); backup and health_check may run without one.
	requiresRelease := in.Action == model.DeploymentActionInstall ||
		in.Action == model.DeploymentActionUpdate ||
		in.Action == model.DeploymentActionRollback
	if requiresRelease && in.ReleaseID == "" {
		return nil, ErrBadRequest
	}
	if in.Action == model.DeploymentActionRestore && in.BackupID == "" {
		return nil, fmt.Errorf("%w: backup_id is required for restore", ErrBadRequest)
	}
	// Reasons are redacted before they reach the database or audit trail.
	reason := secret.NewRedactor().RedactString(in.Reason)
	if s.cfg.AppEnv == "production" && reason == "" {
		return nil, ErrBadRequest
	}

	feature, err := s.store.DeploymentFeatureByID(ctx, in.FeatureID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var release *model.DeploymentRelease
	if in.ReleaseID != "" {
		release, err = s.store.DeploymentReleaseByID(ctx, in.ReleaseID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if release.FeatureID != in.FeatureID {
			return nil, ErrBadRequest
		}
	}

	// 目标解析：显式 target_ids 优先；为空时 = 该 Feature 下全部已启用 Target
	// （"全部"= 已关联到服务器的 Target；可用 NodeID 收窄到单台服务器）。
	targetIDs := make([]string, 0, len(in.TargetIDs))
	if len(in.TargetIDs) == 0 {
		tgs, err := s.store.DeploymentTargetsByFeature(ctx, in.FeatureID)
		if err != nil {
			return nil, err
		}
		for _, tg := range tgs {
			if !tg.Enabled {
				continue
			}
			if in.NodeID != "" && tg.NodeID != in.NodeID {
				continue
			}
			targetIDs = append(targetIDs, tg.ID)
		}
		if len(targetIDs) == 0 {
			return nil, ErrBadRequest
		}
	} else {
		seen := map[string]bool{}
		for _, tid := range in.TargetIDs {
			if tid == "" || seen[tid] {
				continue
			}
			seen[tid] = true
			targetIDs = append(targetIDs, tid)
		}
	}
	if len(targetIDs) == 0 {
		return nil, ErrBadRequest
	}

	// Restore 专用校验：备份必须存在、属于该 feature、状态成功且非 none 模式。
	var restoreBackup *model.DeploymentBackup
	if in.Action == model.DeploymentActionRestore {
		b, err := s.store.DeploymentBackupByID(ctx, in.BackupID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if b.FeatureID != in.FeatureID {
			return nil, ErrBadRequest
		}
		if b.Status != model.DeploymentStatusSucceeded {
			return nil, fmt.Errorf("%w: backup is not in a restorable state (status=%s)", ErrBadRequest, b.Status)
		}
		if b.BackupMode == "none" {
			return nil, fmt.Errorf("%w: feature does not support restore (backup_mode=none)", ErrBadRequest)
		}
		restoreBackup = b
	}

	// Feature-level serialization: at most one active operation per feature.
	if active, err := s.store.ActiveDeploymentOperationForFeature(ctx, in.FeatureID); err == nil && active != nil {
		return nil, ErrConflict
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	// Validate targets: existence, feature match, node online & enabled.
	now := time.Now().UTC()
	var firstNodeID, firstTargetID, envID string
	opTargets := make([]*model.DeploymentOperationTarget, 0, len(targetIDs))
	validatedTargets := make([]*model.DeploymentTarget, 0, len(targetIDs))
	for _, tid := range targetIDs {
		tg, err := s.store.DeploymentTargetByID(ctx, tid)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if tg.FeatureID != in.FeatureID {
			return nil, ErrBadRequest
		}
		if !tg.Enabled {
			return nil, ErrBadRequest
		}
		if restoreBackup != nil && restoreBackup.NodeID != tg.NodeID {
			return nil, fmt.Errorf("%w: backup does not belong to this target's node", ErrBadRequest)
		}
		node, err := s.store.NodeByID(ctx, tg.NodeID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if !node.Enabled || node.Status == model.NodeStatusOffline || node.Status == model.NodeStatusDisabled {
			return nil, ErrOffline
		}
		if firstNodeID == "" {
			firstNodeID = node.ID
			envID = node.EnvironmentID
		}
		if firstTargetID == "" {
			firstTargetID = tg.ID
		}
		opTargets = append(opTargets, &model.DeploymentOperationTarget{
			TargetID:         tg.ID,
			NodeID:           tg.NodeID,
			Status:           model.DeploymentStatusQueued,
			DesiredReleaseID: in.ReleaseID,
		})
		validatedTargets = append(validatedTargets, tg)
	}

	// Freeze configuration and secret references before the operation is
	// queued, so the executed configuration cannot drift from what was
	// reviewed. Per-target hashes are stored on each operation target; the
	// operation-level config hash is the first target's (the scheduler reads
	// the op-level hash first, which is exact for single-target operations).
	opFrozenConfigHash := ""
	for i, ot := range opTargets {
		_, cfgHash, err := s.ResolveConfig(ctx, in.FeatureID, validatedTargets[i])
		if err != nil {
			return nil, err
		}
		ot.FrozenConfigHash = cfgHash
		if opFrozenConfigHash == "" {
			opFrozenConfigHash = cfgHash
		}
		secHash, err := s.frozenSecretHash(ctx, in.FeatureID, ot.NodeID)
		if err != nil {
			return nil, err
		}
		ot.FrozenSecretHash = secHash
	}

	// Production installs/updates require explicit confirmation before they
	// may be claimed by the scheduler.
	status := model.DeploymentStatusQueued
	if s.cfg.AppEnv == "production" && (in.Action == model.DeploymentActionInstall ||
		in.Action == model.DeploymentActionUpdate ||
		in.Action == model.DeploymentActionRestore) {
		status = model.DeploymentStatusAwaitingConfirmation
	}
	op := &model.DeploymentOperation{
		ID:               model.NewUUID(),
		Action:           in.Action,
		FeatureID:        in.FeatureID,
		ReleaseID:        in.ReleaseID,
		Strategy:         "serial",
		Status:           status,
		RequestedBy:      actorID,
		Reason:           reason,
		EnvironmentID:    envID,
		FrozenConfigHash: opFrozenConfigHash,
		BackupID:         in.BackupID,
		ForceDelete:      in.ForceDelete,
		CreatedAt:        now,
	}
	steps := make([]*model.DeploymentStep, 0, len(opTargets))
	for _, ot := range opTargets {
		ot.ID = model.NewUUID()
		ot.OperationID = op.ID
		steps = append(steps, &model.DeploymentStep{
			ID:                model.NewUUID(),
			OperationID:       op.ID,
			OperationTargetID: ot.ID,
			NodeID:            ot.NodeID,
			StepType:          "preflight",
			Status:            model.DeploymentStatusQueued,
		})
	}
	// Transactional creation: operation, targets and steps commit or roll back
	// together, so a node-serial conflict never leaves a partial operation.
	if err := s.store.CreateDeploymentOperationBundle(ctx, op, opTargets, steps); err != nil {
		s.auditDeployment(ctx, model.ActorAdmin, actorID, "deployment.operation.create", ResultFailure, map[string]any{
			"feature_key": feature.FeatureKey, "action": "deployment.operation.create",
		})
		return nil, mapStoreErr(err)
	}
	releaseVersion := ""
	if release != nil {
		releaseVersion = release.Version
	}
	s.auditor.Record(ctx, AuditInput{
		ActorType:    model.ActorAdmin,
		ActorID:      actorID,
		Action:       "deployment.operation.create",
		ResourceType: "deployment_operation",
		ResourceID:   op.ID,
		Summary:      "deployment operation created",
		Details: map[string]any{
			"feature_key":     feature.FeatureKey,
			"release_version": releaseVersion,
			"node_id":         firstNodeID,
			"target_id":       firstTargetID,
			"operation_id":    op.ID,
			"action":          in.Action,
			"result":          ResultSuccess,
			"reason_length":   len(reason),
		},
	})
	return op, nil
}

// frozenSecretHash computes the canonical SHA-256 of the feature's secret
// references for a node (shared scope + node scope). It deliberately reuses
// the scheduler's canonicalSecretRefsHash so the freeze recorded at creation
// time is byte-identical to what the scheduler compares at execution time (a
// mismatch fails the target to detect a concurrent secret rotation).
func (s *DeploymentService) frozenSecretHash(ctx context.Context, featureID, nodeID string) (string, error) {
	refs := []map[string]any{}
	for _, sc := range []struct{ scopeType, scopeID string }{
		{model.SecretScopeShared, ""},
		{model.SecretScopeNode, nodeID},
	} {
		rs, err := s.store.ListDeploymentSecretReferences(ctx, featureID, sc.scopeType, sc.scopeID)
		if err != nil {
			return "", err
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
	return canonicalSecretRefsHash(refs), nil
}

// RunBackupsForNode creates one backup operation per feature that has at
// least one enabled target on nodeID (or across all nodes when nodeID is
// empty). It is the per-server backup entry point for external schedulers.
// Returns the created operations (already queued).
func (s *DeploymentService) RunBackupsForNode(ctx context.Context, actorID, nodeID, featureID string) ([]*model.DeploymentOperation, error) {
	var targets []*model.DeploymentTarget
	var err error
	if featureID != "" {
		targets, err = s.store.DeploymentTargetsByFeature(ctx, featureID)
	} else {
		targets, err = s.store.ListDeploymentTargets(ctx)
	}
	if err != nil {
		return nil, err
	}
	byFeature := map[string][]string{}
	order := []string{}
	for _, tg := range targets {
		if !tg.Enabled {
			continue
		}
		if nodeID != "" && tg.NodeID != nodeID {
			continue
		}
		if _, ok := byFeature[tg.FeatureID]; !ok {
			order = append(order, tg.FeatureID)
		}
		byFeature[tg.FeatureID] = append(byFeature[tg.FeatureID], tg.ID)
	}
	if len(order) == 0 {
		return nil, ErrNotFound
	}
	var created []*model.DeploymentOperation
	for _, fid := range order {
		op, err := s.CreateOperation(ctx, actorID, CreateOperationInput{
			Action:    model.DeploymentActionBackup,
			FeatureID: fid,
			TargetIDs: byFeature[fid],
			Reason:    "scheduled/per-server backup",
		})
		if err != nil {
			return created, err
		}
		created = append(created, op)
	}
	return created, nil
}

// ListOperations returns operations newest first; limit<=0 defaults to 100.
func (s *DeploymentService) ListOperations(ctx context.Context, limit int) ([]*model.DeploymentOperation, error) {
	return s.store.ListDeploymentOperations(ctx, limit)
}

// GetOperationDetail returns an operation with its targets and steps.
func (s *DeploymentService) GetOperationDetail(ctx context.Context, id string) (*OperationDetail, error) {
	op, err := s.store.DeploymentOperationByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	targets, err := s.store.ListDeploymentOperationTargetsByOperation(ctx, id)
	if err != nil {
		return nil, err
	}
	steps, err := s.store.ListDeploymentStepsByOperation(ctx, id)
	if err != nil {
		return nil, err
	}
	return &OperationDetail{Operation: op, Targets: targets, Steps: steps}, nil
}

// CancelOperation cancels a cancellable operation: it marks the operation and
// all unfinished targets cancelled and cancels running agent tasks.
func (s *DeploymentService) CancelOperation(ctx context.Context, actorID, id, reason string) (*model.DeploymentOperation, error) {
	op, err := s.store.DeploymentOperationByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	switch op.Status {
	case model.DeploymentStatusQueued, model.DeploymentStatusRunning,
		model.DeploymentStatusAwaitingConfirmation, model.DeploymentStatusPartialFailed:
	default:
		return nil, ErrTerminal
	}

	// Cancel reasons are redacted before they are stored or logged.
	reason = secret.NewRedactor().RedactString(reason)
	now := time.Now().UTC()
	op.Status = model.DeploymentStatusCancelled
	op.Reason = reason
	op.FinishedAt = &now
	if err := s.store.UpdateDeploymentOperation(ctx, op); err != nil {
		return nil, err
	}

	targets, err := s.store.ListDeploymentOperationTargetsByOperation(ctx, op.ID)
	if err != nil {
		return nil, err
	}
	for _, t := range targets {
		if isOperationTargetTerminal(t.Status) {
			continue
		}
		t.Status = model.DeploymentStatusCancelled
		t.FinishedAt = &now
		if err := s.store.UpdateDeploymentOperationTarget(ctx, t); err != nil {
			return nil, err
		}
	}

	steps, err := s.store.ListDeploymentStepsByOperation(ctx, op.ID)
	if err != nil {
		return nil, err
	}
	seenTask := map[string]bool{}
	for _, st := range steps {
		if st.TaskID == "" || seenTask[st.TaskID] {
			continue
		}
		seenTask[st.TaskID] = true
		tk, err := s.store.TaskByID(ctx, st.TaskID)
		if err != nil || tk == nil || model.IsTaskTerminal(tk.Status) {
			continue
		}
		if _, err := s.tasks.CancelTask(ctx, "", st.TaskID, model.ActorAdmin, actorID); err != nil {
			s.log.Warn("cancel deployment task failed", "task_id", st.TaskID, "error", err)
		}
	}

	s.auditor.Record(ctx, AuditInput{
		ActorType:    model.ActorAdmin,
		ActorID:      actorID,
		Action:       "deployment.operation.cancel",
		ResourceType: "deployment_operation",
		ResourceID:   op.ID,
		Summary:      "deployment operation cancelled",
		Details: map[string]any{
			"operation_id":  op.ID,
			"action":        op.Action,
			"result":        ResultSuccess,
			"reason_length": len(reason),
		},
	})
	return op, nil
}

// ContinueOperation requeues a failed/partial_failed operation when at least
// one target is still pending execution.
func (s *DeploymentService) ContinueOperation(ctx context.Context, actorID, id string) (*model.DeploymentOperation, error) {
	op, err := s.store.DeploymentOperationByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// Awaiting-confirmation operations are requeued by explicit confirmation;
	// failed/partial_failed operations are requeued for a retry. Both require
	// at least one non-terminal target.
	if op.Status != model.DeploymentStatusAwaitingConfirmation &&
		op.Status != model.DeploymentStatusFailed &&
		op.Status != model.DeploymentStatusPartialFailed {
		return nil, ErrTerminal
	}
	targets, err := s.store.ListDeploymentOperationTargetsByOperation(ctx, op.ID)
	if err != nil {
		return nil, err
	}
	hasPending := false
	for _, t := range targets {
		if !isOperationTargetTerminal(t.Status) {
			hasPending = true
			break
		}
	}
	if !hasPending {
		return nil, ErrTerminal
	}

	op.Status = model.DeploymentStatusQueued
	op.FinishedAt = nil
	if err := s.store.UpdateDeploymentOperation(ctx, op); err != nil {
		return nil, err
	}

	s.auditor.Record(ctx, AuditInput{
		ActorType:    model.ActorAdmin,
		ActorID:      actorID,
		Action:       "deployment.operation.continue",
		ResourceType: "deployment_operation",
		ResourceID:   op.ID,
		Summary:      "deployment operation requeued",
		Details: map[string]any{
			"operation_id": op.ID,
			"action":       op.Action,
			"result":       ResultSuccess,
		},
	})
	return op, nil
}

// ListBackups returns backup artifacts newest first; empty filters mean "no
// filter" and limit<=0 defaults to 100.
func (s *DeploymentService) ListBackups(ctx context.Context, featureID, nodeID string, limit int) ([]*model.DeploymentBackup, error) {
	return s.store.ListDeploymentBackups(ctx, featureID, nodeID, limit)
}

// GetBackup returns a single backup artifact.
func (s *DeploymentService) GetBackup(ctx context.Context, id string) (*model.DeploymentBackup, error) {
	b, err := s.store.DeploymentBackupByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return b, nil
}
