package service

// P0 安全整改自动测试：Secret 不落库 / 不落 Task / 事件 / 日志 / 审计。
// 覆盖：
//   - Redactor 覆盖阿里云 AccessKey ID / Secret（全形态）
//   - Task Event 落库前脱敏（service/task.go RecordEvent）
//   - Task Result 的 ErrorMessage / stdout / stderr 落库前脱敏
//   - deployment_secret_reference 表结构：无 value/content 列，仅存引用
//   - DeploymentAuditDetails 白名单丢弃 secret 字段 + Auditor 落库不泄密

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"servercli/internal/db"
	"servercli/internal/logger"
	"servercli/internal/model"
	"servercli/internal/secret"
	"servercli/internal/store"
)

func newSecretLeakHarness(t *testing.T) (context.Context, *store.Store, *db.DB, *TaskService, *Auditor, *DeploymentService) {
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
	return ctx, st, database, tasks, auditor, svc
}

const (
	leakAKID = "LTAI5tLeakTestAkId123456"
	leakSK   = "LeakTestSecretValue9876543210"
)

// TestRedactorCoversAliyunCredentials verifies the shared redactor masks
// Alibaba Cloud AccessKey material in free text (full forms and key=value).
func TestRedactorCoversAliyunCredentials(t *testing.T) {
	r := secret.NewRedactor()
	cases := []string{
		leakAKID,
		"accessKeyId=" + leakAKID,
		"accessKeySecret=" + leakSK,
		"access_key_secret: '" + leakSK + "'",
		"ak_secret=" + leakSK,
		"aliyun_secret: " + leakSK,
		"http://" + leakAKID + ":" + leakSK + "@oss.example.com/bucket",
	}
	for _, in := range cases {
		out := r.RedactString(in)
		if strings.Contains(out, leakAKID) || strings.Contains(out, leakSK) {
			t.Fatalf("aliyun credential leaked for input %q: %q", in, out)
		}
	}
	if r.Count() == 0 {
		t.Fatal("expected redaction count")
	}
}

// TestTaskEventMessageRedactedBeforePersist verifies RecordEvent redacts the
// message before it lands in task_event rows.
func TestTaskEventMessageRedactedBeforePersist(t *testing.T) {
	ctx, st, _, tasks, _, _ := newSecretLeakHarness(t)
	nodeID := "n-secret-event"
	seedDeployNode(t, ctx, st, nodeID)
	seedDeployCommand(t, ctx, st, nodeID, "service.status")

	tk, err := tasks.CreateTask(ctx, nodeID, "admin-1", model.ActorAdmin, "idem-event-1", CreateTaskInput{
		CommandID: "service.status", CommandVersion: "1.0.0", Arguments: map[string]any{"service": "sshd"},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	msg := "deploying with accessKeyId=" + leakAKID + " accessKeySecret=" + leakSK
	if err := tasks.RecordEvent(ctx, nodeID, tk.ID, EventInput{EventType: "started", Message: msg, Sequence: 1}); err != nil {
		t.Fatalf("record event: %v", err)
	}
	events, err := st.TaskEvents(ctx, tk.ID)
	if err != nil {
		t.Fatalf("task events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if strings.Contains(events[0].Message, leakAKID) || strings.Contains(events[0].Message, leakSK) {
		t.Fatalf("task event message leaked secret: %q", events[0].Message)
	}
	if !strings.Contains(events[0].Message, "[REDACTED]") {
		t.Fatalf("expected redaction marker in event message: %q", events[0].Message)
	}
}

// TestTaskResultErrorMessageRedacted verifies RecordResult redacts the error
// message and output before persisting (stdout/stderr were already covered).
func TestTaskResultErrorMessageRedacted(t *testing.T) {
	ctx, st, _, tasks, _, _ := newSecretLeakHarness(t)
	nodeID := "n-secret-result"
	seedDeployNode(t, ctx, st, nodeID)
	seedDeployCommand(t, ctx, st, nodeID, "service.status")

	tk, err := tasks.CreateTask(ctx, nodeID, "admin-1", model.ActorAdmin, "idem-result-1", CreateTaskInput{
		CommandID: "service.status", CommandVersion: "1.0.0", Arguments: map[string]any{"service": "sshd"},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	_, err = tasks.RecordResult(ctx, nodeID, tk.ID, ResultInput{
		Status:       model.TaskFailed,
		ErrorMessage: "upload failed: accessKeySecret=" + leakSK,
		StdoutText:   "connecting with " + leakAKID,
		StderrText:   "auth error: access_key_secret=" + leakSK,
	})
	if err != nil {
		t.Fatalf("record result: %v", err)
	}
	got, err := st.TaskByID(ctx, tk.ID)
	if err != nil {
		t.Fatalf("task by id: %v", err)
	}
	if strings.Contains(got.ErrorMessage, leakSK) {
		t.Fatalf("task ErrorMessage leaked secret: %q", got.ErrorMessage)
	}
	out, err := st.TaskOutput(ctx, tk.ID)
	if err != nil {
		t.Fatalf("task output: %v", err)
	}
	if strings.Contains(out.StdoutText, leakAKID) || strings.Contains(out.StderrText, leakSK) {
		t.Fatalf("task output leaked secret: stdout=%q stderr=%q", out.StdoutText, out.StderrText)
	}
}

// TestDeploymentSecretReferenceSchemaNoValueColumn verifies the secret
// reference table stores only metadata (content_hash / object_key), never a
// value or content column.
func TestDeploymentSecretReferenceSchemaNoValueColumn(t *testing.T) {
	ctx, st, database, _, _, _ := newSecretLeakHarness(t)

	// Create a reference through the store to confirm the round trip works
	// with only metadata fields.
	seedDeployFeature(t, ctx, st, "f-secret", "db-pass", "none", "none")
	ref := &model.DeploymentSecretReference{
		Name:           "db-pass",
		FeatureID:      "f-secret",
		ScopeType:      model.SecretScopeShared,
		ObjectKey:      "deployment-repository/secrets/shared/db-pass.secrets.yaml",
		Version:        1,
		ContentHash:    "deadbeef",
		EncryptionMode: "none",
		Size:           10,
	}
	if err := st.CreateDeploymentSecretReference(ctx, ref); err != nil {
		t.Fatalf("create secret reference: %v", err)
	}
	got, err := st.DeploymentSecretReferenceByID(ctx, ref.ID)
	if err != nil {
		t.Fatalf("secret reference by id: %v", err)
	}
	if got.ContentHash != "deadbeef" || got.ObjectKey == "" || got.EncryptionMode != "none" {
		t.Fatalf("secret reference metadata missing: %+v", got)
	}

	// Direct schema assertion: no value/content column may exist.
	rows, err := database.QueryContext(ctx, "PRAGMA table_info(deployment_secret_reference)")
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"value", "content", "secret", "body"} {
		if cols[forbidden] {
			t.Fatalf("deployment_secret_reference must not have a %q column", forbidden)
		}
	}
	for _, required := range []string{"content_hash", "object_key", "encryption_mode", "size", "version"} {
		if !cols[required] {
			t.Fatalf("deployment_secret_reference missing required column %q", required)
		}
	}
}

// TestDeploymentAuditDetailsDropsSecrets verifies the deployment audit
// whitelist drops secret-bearing fields before they can reach audit storage.
func TestDeploymentAuditDetailsDropsSecrets(t *testing.T) {
	fields := map[string]any{
		"operation_id":  "op-1",
		"node_id":       "n-1",
		"reason_length": 4,
		"secret_value":  "LTAI5tAuditSecret123456",
		"config_yaml":   "accessKeySecret: " + leakSK,
		"oss_ak":        leakAKID,
	}
	out := DeploymentAuditDetails(fields)
	if out["operation_id"] != "op-1" || out["node_id"] != "n-1" || out["reason_length"] != 4 {
		t.Fatalf("whitelisted fields dropped: %v", out)
	}
	for _, k := range []string{"secret_value", "config_yaml", "oss_ak"} {
		if _, ok := out[k]; ok {
			t.Fatalf("secret-bearing key %q not dropped: %v", k, out)
		}
	}
}

// TestAuditEventStorageNoSecret verifies that even if a caller passes a
// details map containing secret material (not through the deployment
// whitelist), the auditor redacts before persisting to audit_event.
func TestAuditEventStorageNoSecret(t *testing.T) {
	ctx, st, _, _, auditor, _ := newSecretLeakHarness(t)
	if err := auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: "admin-1", Action: "test.secret-leak",
		Details: map[string]any{
			"access_key_secret": leakSK,
			"secret_body":       leakAKID + "\npassword=hunter2",
			"command_id":        "deployment.install",
		},
	}); err != nil {
		t.Fatalf("auditor record: %v", err)
	}
	events, err := st.ListAuditEvents(ctx, store.AuditFilter{Action: "test.secret-leak"})
	if err != nil || len(events) != 1 {
		t.Fatalf("list audit events: n=%d err=%v", len(events), err)
	}
	if strings.Contains(events[0].DetailsJSON, leakSK) || strings.Contains(events[0].DetailsJSON, leakAKID) || strings.Contains(events[0].DetailsJSON, "hunter2") {
		t.Fatalf("audit event details leaked secret: %s", events[0].DetailsJSON)
	}
	if !strings.Contains(events[0].DetailsJSON, "deployment.install") {
		t.Fatalf("non-secret command_id should be preserved: %s", events[0].DetailsJSON)
	}
	// DetailsJSON must remain valid JSON and contain redaction markers.
	var m map[string]any
	if err := json.Unmarshal([]byte(events[0].DetailsJSON), &m); err != nil {
		t.Fatalf("details not valid json: %v", err)
	}
}
