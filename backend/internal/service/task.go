package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"servercli/internal/config"
	"servercli/internal/model"
	"servercli/internal/secret"
	"servercli/internal/store"
	"servercli/internal/validate"
)

// TaskService creates, dispatches and tracks tasks.
type TaskService struct {
	store      *store.Store
	cfg        *config.Config
	log        *slog.Logger
	auditor    *Auditor
	nodes      *NodeService
	dispatcher *Dispatcher
}

// NewTaskService builds the service.
func NewTaskService(st *store.Store, cfg *config.Config, log *slog.Logger, auditor *Auditor, nodes *NodeService) *TaskService {
	return &TaskService{
		store:      st,
		cfg:        cfg,
		log:        log,
		auditor:    auditor,
		nodes:      nodes,
		dispatcher: NewDispatcher(st, log),
	}
}

// CreateTaskInput is the admin's task creation payload.
type CreateTaskInput struct {
	CommandID      string         `json:"command_id"`
	CommandVersion string         `json:"command_version"`
	Arguments      map[string]any `json:"arguments"`
	TimeoutSeconds int            `json:"timeout_seconds"`
}

// CreateTask validates and queues a task, returning it (idempotent). actorType
// identifies the originator for audit purposes (model.ActorAdmin for the admin
// API, model.ActorNode for node self-service).
func (s *TaskService) CreateTask(ctx context.Context, nodeID, requestedBy, actorType, idempotencyKey string, in CreateTaskInput) (*model.Task, error) {
	if in.CommandID == "" || in.CommandVersion == "" {
		return nil, ErrBadRequest
	}
	if idempotencyKey != "" {
		if existing, err := s.store.TaskByIdempotency(ctx, requestedBy, idempotencyKey); err == nil {
			return existing, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	node, err := s.store.NodeByID(ctx, nodeID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !node.Enabled || node.Status == model.NodeStatusOffline || node.Status == model.NodeStatusDisabled {
		return nil, ErrOffline
	}
	cmd, err := s.store.NodeCommandByID(ctx, nodeID, in.CommandID, in.CommandVersion)
	if err != nil {
		return nil, ErrNotFound
	}
	if !cmd.Enabled {
		return nil, ErrDisabled
	}
	if cmd.ParameterSchemaJSON != "" {
		schema, err := validate.Parse([]byte(cmd.ParameterSchemaJSON))
		if err != nil {
			return nil, fmt.Errorf("command schema invalid: %w", err)
		}
		if err := schema.Validate(in.Arguments); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
		}
	}
	argsJSON, err := json.Marshal(in.Arguments)
	if err != nil {
		return nil, ErrBadRequest
	}
	timeout := in.TimeoutSeconds
	if timeout <= 0 {
		timeout = cmd.TimeoutSeconds
	}
	if timeout <= 0 {
		timeout = 60
	}
	now := time.Now().UTC()
	t := &model.Task{
		ID:             model.NewUUID(),
		NodeID:         nodeID,
		CommandID:      in.CommandID,
		CommandVersion: in.CommandVersion,
		RequestedBy:    requestedBy,
		IdempotencyKey: idempotencyKey,
		ArgumentsJSON:  string(argsJSON),
		Status:         model.TaskQueued,
		QueuedAt:       now,
		TimeoutSeconds: timeout,
	}
	if err := s.store.CreateTask(ctx, t); err != nil {
		return nil, err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: actorType, ActorID: requestedBy, NodeID: nodeID, Action: "task.create",
		ResourceType: "task", ResourceID: t.ID, TaskID: t.ID,
		Summary: fmt.Sprintf("task created: %s %s", in.CommandID, in.CommandVersion),
		Details: map[string]any{"command_id": in.CommandID, "command_version": in.CommandVersion},
	})
	s.dispatcher.Notify(nodeID)
	return t, nil
}

// GetTask returns a task with its events and output.
func (s *TaskService) GetTask(ctx context.Context, scopeNodeID, id string) (*model.Task, []*model.TaskEvent, *model.TaskOutput, error) {
	t, err := s.store.TaskByID(ctx, id)
	if err != nil {
		return nil, nil, nil, ErrNotFound
	}
	if scopeNodeID != "" && scopeNodeID != t.NodeID {
		return nil, nil, nil, ErrNotFound
	}
	events, err := s.store.TaskEvents(ctx, id)
	if err != nil {
		return nil, nil, nil, err
	}
	output, err := s.store.TaskOutput(ctx, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil, err
	}
	return t, events, output, nil
}

// ListTasks lists tasks within scope.
func (s *TaskService) ListTasks(ctx context.Context, scopeNodeID, nodeID, status string, limit, offset int) ([]*model.Task, error) {
	if scopeNodeID != "" {
		nodeID = scopeNodeID
	}
	return s.store.ListTasks(ctx, nodeID, status, limit, offset)
}

// CancelTask cancels a non-terminal task. actorType identifies the originator
// for audit purposes (model.ActorAdmin for the admin API, model.ActorNode for
// node self-service).
func (s *TaskService) CancelTask(ctx context.Context, scopeNodeID, id, actorType, actorID string) (*model.Task, error) {
	t, err := s.store.TaskByID(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if scopeNodeID != "" && scopeNodeID != t.NodeID {
		return nil, ErrNotFound
	}
	if model.IsTaskTerminal(t.Status) {
		return nil, ErrTerminal
	}
	t.Status = model.TaskCancelled
	t.FinishedAt = ptrTime(time.Now().UTC())
	if err := s.store.UpdateTask(ctx, t); err != nil {
		return nil, err
	}
	ev := &model.TaskEvent{TaskID: t.ID, EventType: "cancelled", Status: model.TaskCancelled,
		Message: "task cancelled", Source: "control-plane"}
	if err := s.store.AppendTaskEventDedup(ctx, ev); err != nil {
		s.log.Warn("append cancel event failed", "error", err)
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: actorType, ActorID: actorID, NodeID: t.NodeID, Action: "task.cancel",
		ResourceType: "task", ResourceID: t.ID, TaskID: t.ID, Summary: "task cancelled",
	})
	return t, nil
}

func ptrTime(t time.Time) *time.Time { return &t }

// TaskPayload is the signed task handed to the agent.
type TaskPayload struct {
	TaskID         string          `json:"task_id"`
	NodeID         string          `json:"node_id"`
	CommandID      string          `json:"command_id"`
	CommandVersion string          `json:"command_version"`
	Arguments      json.RawMessage `json:"arguments"`
	CreatedAt      time.Time       `json:"created_at"`
	NotBefore      time.Time       `json:"not_before"`
	Deadline       time.Time       `json:"deadline"`
	IdempotencyKey string          `json:"idempotency_key"`
	Attempt        int             `json:"attempt"`
	TimeoutSeconds int             `json:"timeout_seconds"`
	MaxOutputBytes int64           `json:"max_output_bytes"`
	PayloadHash    string          `json:"payload_hash"`
	Signature      string          `json:"signature"`
}

// PollTask claims the next queued task for a node and builds a signed payload,
// or returns nil when the queue is empty.
func (s *TaskService) PollTask(ctx context.Context, nodeID string) (*TaskPayload, error) {
	t, err := s.store.ClaimNextTask(ctx, nodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	credential, err := s.nodes.CredentialForNode(ctx, nodeID)
	if err != nil {
		// Without the credential we cannot sign; requeue the task.
		s.log.Error("cannot derive node credential for signing", "node_id", nodeID, "task_id", t.ID, "error", err)
		return nil, ErrUnavailable
	}
	cmd, err := s.store.NodeCommandByID(ctx, t.NodeID, t.CommandID, t.CommandVersion)
	if err != nil {
		// Command removed; fail the task.
		now := time.Now().UTC()
		t.Status = model.TaskFailed
		t.FinishedAt = &now
		t.ErrorCode = "COMMAND_NOT_FOUND"
		t.ErrorMessage = "command no longer registered"
		_ = s.store.UpdateTask(ctx, t)
		return nil, ErrUnavailable
	}
	base := &TaskPayload{
		TaskID:         t.ID,
		NodeID:         t.NodeID,
		CommandID:      t.CommandID,
		CommandVersion: t.CommandVersion,
		Arguments:      json.RawMessage(t.ArgumentsJSON),
		CreatedAt:      t.QueuedAt,
		NotBefore:      t.QueuedAt,
		Deadline:       t.QueuedAt.Add(time.Duration(t.TimeoutSeconds) * time.Second),
		IdempotencyKey: t.IdempotencyKey,
		Attempt:        1,
		TimeoutSeconds: t.TimeoutSeconds,
		MaxOutputBytes: cmd.MaxOutputBytes,
	}
	raw, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(raw)
	base.PayloadHash = hex.EncodeToString(hash[:])
	mac := hmac.New(sha256.New, []byte(credential))
	mac.Write([]byte("task:" + t.ID + ":" + base.PayloadHash))
	base.Signature = hex.EncodeToString(mac.Sum(nil))
	return base, nil
}

// CancelledTasks returns recently cancelled running tasks for a node (the
// agent terminates these).
func (s *TaskService) CancelledTasks(ctx context.Context, nodeID string) ([]string, error) {
	return s.store.CancelledRunningTasks(ctx, nodeID)
}

// EventInput is an agent task event.
type EventInput struct {
	EventType  string    `json:"event_type"`
	Sequence   int64     `json:"sequence"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurred_at"`
}

// RecordEvent stores an agent task event, enforcing state transitions.
func (s *TaskService) RecordEvent(ctx context.Context, nodeID, taskID string, in EventInput) error {
	t, err := s.store.TaskByID(ctx, taskID)
	if err != nil {
		return ErrNotFound
	}
	if t.NodeID != nodeID {
		return ErrNotFound
	}
	if in.Sequence <= 0 {
		// server-assigned sequence
		maxSeq, err := s.store.TaskEventMaxSequence(ctx, taskID)
		if err != nil {
			return err
		}
		in.Sequence = maxSeq + 1
	}
	ev := &model.TaskEvent{
		TaskID:     taskID,
		Sequence:   in.Sequence,
		EventType:  in.EventType,
		Message:    in.Message,
		OccurredAt: in.OccurredAt,
		Source:     "agent",
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	switch in.EventType {
	case "accepted", "started":
		if t.Status == model.TaskDispatched {
			_ = s.store.MarkTaskRunning(ctx, taskID, time.Now().UTC())
			ev.Status = model.TaskRunning
		} else if t.Status == model.TaskQueued {
			ev.Status = model.TaskQueued
		} else {
			ev.Status = t.Status
		}
	case "completed":
		if !model.IsTaskTerminal(t.Status) {
			now := time.Now().UTC()
			t.Status = model.TaskSucceeded
			t.FinishedAt = &now
			_ = s.store.UpdateTask(ctx, t)
		}
		ev.Status = model.TaskSucceeded
	case "failed":
		if !model.IsTaskTerminal(t.Status) {
			now := time.Now().UTC()
			t.Status = model.TaskFailed
			t.FinishedAt = &now
			_ = s.store.UpdateTask(ctx, t)
		}
		ev.Status = model.TaskFailed
	case "timed_out":
		if !model.IsTaskTerminal(t.Status) {
			now := time.Now().UTC()
			t.Status = model.TaskTimedOut
			t.FinishedAt = &now
			_ = s.store.UpdateTask(ctx, t)
		}
		ev.Status = model.TaskTimedOut
	case "cancelled":
		if !model.IsTaskTerminal(t.Status) {
			now := time.Now().UTC()
			t.Status = model.TaskCancelled
			t.FinishedAt = &now
			_ = s.store.UpdateTask(ctx, t)
		}
		ev.Status = model.TaskCancelled
	case "stdout_chunk", "stderr_chunk", "progress":
		ev.Status = t.Status
	}
	return s.store.AppendTaskEventDedup(ctx, ev)
}

// ResultInput is the agent's final task result.
type ResultInput struct {
	Status       string    `json:"status"`
	StdoutText   string    `json:"stdout_text"`
	StderrText   string    `json:"stderr_text"`
	ExitCode     *int      `json:"exit_code"`
	ErrorCode    string    `json:"error_code"`
	ErrorMessage string    `json:"error_message"`
	Truncated    bool      `json:"truncated"`
	FinishedAt   time.Time `json:"finished_at"`
}

// RecordResult stores the final task result and output.
func (s *TaskService) RecordResult(ctx context.Context, nodeID, taskID string, in ResultInput) (*model.Task, error) {
	t, err := s.store.TaskByID(ctx, taskID)
	if err != nil {
		return nil, ErrNotFound
	}
	if t.NodeID != nodeID {
		return nil, ErrNotFound
	}
	if !model.IsTaskTerminal(t.Status) {
		status := in.Status
		switch status {
		case model.TaskSucceeded, model.TaskFailed, model.TaskTimedOut, model.TaskCancelled, model.TaskResultUnknown:
		default:
			status = model.TaskResultUnknown
		}
		finished := in.FinishedAt
		if finished.IsZero() {
			finished = time.Now().UTC()
		}
		t.Status = status
		t.FinishedAt = &finished
		t.ExitCode = in.ExitCode
		t.ErrorCode = in.ErrorCode
		t.ErrorMessage = in.ErrorMessage
		if err := s.store.UpdateTask(ctx, t); err != nil {
			return nil, err
		}
	}
	// Persist output (redacted).
	redactor := secret.NewRedactor()
	stdout := redactor.RedactString(in.StdoutText)
	stderr := redactor.RedactString(in.StderrText)
	output, err := s.store.TaskOutput(ctx, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			output = &model.TaskOutput{TaskID: taskID}
			output.StdoutText = stdout
			output.StderrText = stderr
			output.StdoutBytes = int64(len(stdout))
			output.StderrBytes = int64(len(stderr))
			output.Truncated = in.Truncated
			output.RedactionCount = redactor.Count()
			output.Encoding = "utf-8"
			if err := s.store.CreateTaskOutput(ctx, output); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	} else {
		output.StdoutText = stdout
		output.StderrText = stderr
		output.StdoutBytes = int64(len(stdout))
		output.StderrBytes = int64(len(stderr))
		output.Truncated = in.Truncated
		output.RedactionCount = redactor.Count()
		if err := s.store.UpdateTaskOutput(ctx, output); err != nil {
			return nil, err
		}
	}
	ev := &model.TaskEvent{TaskID: taskID, EventType: "result", Status: t.Status,
		Message: "final result recorded", Source: "agent"}
	_ = s.store.AppendTaskEventDedup(ctx, ev)
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorNode, ActorID: nodeID, NodeID: nodeID, Action: "task.result",
		ResourceType: "task", ResourceID: taskID, TaskID: taskID,
		Summary: "task finished: " + t.Status,
		Details: map[string]any{"status": t.Status, "redaction_count": redactor.Count()},
	})
	return t, nil
}

// Dispatcher exposes the dispatcher for long-poll wiring.
func (s *TaskService) Dispatcher() *Dispatcher { return s.dispatcher }

// Dispatcher wakes polling agents when tasks arrive.
type Dispatcher struct {
	store *store.Store
	log   *slog.Logger
	mu    sync.Mutex
	chans map[string]chan struct{}
}

// NewDispatcher builds a dispatcher.
func NewDispatcher(st *store.Store, log *slog.Logger) *Dispatcher {
	return &Dispatcher{store: st, log: log, chans: map[string]chan struct{}{}}
}

// Notify wakes a node's poller, if any.
func (d *Dispatcher) Notify(nodeID string) {
	d.mu.Lock()
	ch := d.chans[nodeID]
	d.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Wait blocks until the node has a task or the timeout elapses.
func (d *Dispatcher) Wait(nodeID string, timeout time.Duration) {
	d.mu.Lock()
	ch := d.chans[nodeID]
	if ch == nil {
		ch = make(chan struct{}, 1)
		d.chans[nodeID] = ch
	}
	d.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
	}
}
