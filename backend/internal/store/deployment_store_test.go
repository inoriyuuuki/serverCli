package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"servercli/internal/db"
	"servercli/internal/logger"
	"servercli/internal/model"
)

// newTestStore opens a fresh temporary SQLite database with migrations applied
// and returns the store plus the raw DB handle for direct SQL assertions.
func newTestStore(t *testing.T) (context.Context, *Store, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	log := logger.New(io.Discard, "error")
	ctx := context.Background()
	database, err := db.Open(ctx, "sqlite", path, log)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return ctx, New(database, log), database
}

// insertTestNode inserts a minimal node row via raw SQL (the deployment store
// does not manage nodes).
func insertTestNode(t *testing.T, ctx context.Context, database *db.DB, id string) {
	t.Helper()
	now := time.Now().UTC().Format(model.TimeLayout)
	if _, err := database.ExecContext(ctx, `INSERT INTO node
		(id, environment_id, instance_name, role, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		id, "env-test", "node-"+id, "child", now, now); err != nil {
		t.Fatalf("insert node %s: %v", id, err)
	}
}

func newFeature(id, key string) *model.DeploymentFeature {
	return &model.DeploymentFeature{
		ID:                  id,
		FeatureKey:          key,
		Name:                "Feature " + key,
		Description:         "test feature",
		OS:                  "linux",
		Arch:                "amd64",
		BackupMode:          "none",
		RollbackCapability:  "none",
		MinimumAgentVersion: "1.0.0",
	}
}

// ---- Features ----

func TestDeploymentFeatureCRUD(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	f := newFeature("f-1", "key-1")
	if err := st.CreateDeploymentFeature(ctx, f); err != nil {
		t.Fatalf("create: %v", err)
	}

	byID, err := st.DeploymentFeatureByID(ctx, "f-1")
	if err != nil {
		t.Fatalf("by id: %v", err)
	}
	if byID.FeatureKey != "key-1" || byID.Name != "Feature key-1" || byID.OS != "linux" || byID.Arch != "amd64" {
		t.Fatalf("unexpected feature: %+v", byID)
	}
	if byID.CreatedAt.IsZero() || byID.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not persisted: %+v", byID)
	}

	byKey, err := st.DeploymentFeatureByKey(ctx, "key-1")
	if err != nil {
		t.Fatalf("by key: %v", err)
	}
	if byKey.ID != "f-1" {
		t.Fatalf("by key returned %s, want f-1", byKey.ID)
	}

	// Duplicate key must conflict.
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-2", "key-1")); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate feature_key: got %v, want ErrConflict", err)
	}

	// Update.
	f.Name = "Renamed"
	f.Description = "updated"
	if err := st.UpdateDeploymentFeature(ctx, f); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := st.DeploymentFeatureByID(ctx, "f-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Renamed" || got.Description != "updated" {
		t.Fatalf("update not persisted: %+v", got)
	}

	// List.
	list, err := st.ListDeploymentFeatures(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != "f-1" {
		t.Fatalf("list: %+v", list)
	}

	// Delete.
	if err := st.DeleteDeploymentFeature(ctx, "f-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.DeploymentFeatureByID(ctx, "f-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: got %v, want ErrNotFound", err)
	}
}

// ---- Releases ----

func TestDeploymentReleaseCRUD(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-1", "rel-key")); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-2", "rel-key-2")); err != nil {
		t.Fatal(err)
	}
	r1 := &model.DeploymentRelease{ID: "r-1", FeatureID: "f-1", Version: "1.0.0", ObjectKey: "releases/f1/v1", Size: 1024, SHA256: "sha256:1"}
	r2 := &model.DeploymentRelease{ID: "r-2", FeatureID: "f-1", Version: "1.0.1", ObjectKey: "releases/f1/v2", Size: 2048, SHA256: "sha256:2"}
	r3 := &model.DeploymentRelease{ID: "r-3", FeatureID: "f-2", Version: "2.0.0", ObjectKey: "releases/f2/v1", Size: 512, SHA256: "sha256:3"}
	for _, r := range []*model.DeploymentRelease{r1, r2, r3} {
		if err := st.CreateDeploymentRelease(ctx, r); err != nil {
			t.Fatalf("create release %s: %v", r.ID, err)
		}
	}

	byID, err := st.DeploymentReleaseByID(ctx, "r-1")
	if err != nil {
		t.Fatal(err)
	}
	if byID.Version != "1.0.0" || byID.SHA256 != "sha256:1" || byID.Size != 1024 || byID.FeatureID != "f-1" {
		t.Fatalf("by id: %+v", byID)
	}

	// Filter by feature.
	onlyF1, err := st.ListDeploymentReleases(ctx, "f-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyF1) != 2 {
		t.Fatalf("filter f-1: got %d, want 2", len(onlyF1))
	}
	all, err := st.ListDeploymentReleases(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("list all: got %d, want 3", len(all))
	}

	// Duplicate (feature_id, version) must conflict.
	dup := &model.DeploymentRelease{ID: "r-dup", FeatureID: "f-1", Version: "1.0.0", ObjectKey: "x", SHA256: "s"}
	if err := st.CreateDeploymentRelease(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate release: got %v, want ErrConflict", err)
	}

	// Delete.
	if err := st.DeleteDeploymentRelease(ctx, "r-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeploymentReleaseByID(ctx, "r-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: got %v, want ErrNotFound", err)
	}
}

// ---- OSS profiles ----

func TestOSSProfileCRUD(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	p := &model.OSSProfile{
		ID: "oss-1", Name: "primary", Endpoint: "https://oss.example.com", Region: "cn-shanghai",
		Bucket: "servercli", Prefix: "deploy/", AccessKeyIDEnc: "enc:id", AccessKeySecretEnc: "enc:secret",
		IsPrivate: true,
	}
	if err := st.CreateOSSProfile(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	byID, err := st.OSSProfileByID(ctx, "oss-1")
	if err != nil {
		t.Fatal(err)
	}
	if byID.Name != "primary" || byID.AccessKeySecretEnc != "enc:secret" || !byID.IsPrivate {
		t.Fatalf("by id: %+v", byID)
	}

	// Duplicate name conflicts.
	dup := *p
	dup.ID = "oss-2"
	if err := st.CreateOSSProfile(ctx, &dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: got %v, want ErrConflict", err)
	}

	// Update.
	tested := time.Now().UTC().Truncate(time.Second)
	p.Bucket = "other-bucket"
	p.IsPrivate = false
	p.LastTestedAt = &tested
	p.LastTestResult = "ok"
	if err := st.UpdateOSSProfile(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := st.OSSProfileByID(ctx, "oss-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Bucket != "other-bucket" || got.IsPrivate || got.LastTestResult != "ok" || got.LastTestedAt == nil || !got.LastTestedAt.Equal(tested) {
		t.Fatalf("update not persisted: %+v", got)
	}

	list, err := st.ListOSSProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list: %d", len(list))
	}

	if err := st.DeleteOSSProfile(ctx, "oss-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OSSProfileByID(ctx, "oss-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: got %v, want ErrNotFound", err)
	}
}

// ---- Config profiles ----

func TestDeploymentConfigProfileCRUD(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-1", "cfg-key")); err != nil {
		t.Fatal(err)
	}
	p1 := &model.DeploymentConfigProfile{
		ID: "cp-1", Name: "prod", ScopeType: model.ConfigScopeShared, ScopeID: "", FeatureID: "f-1",
		ContentJSON: `{"a":1}`, ContentHash: "h1", Version: 1,
	}
	p2 := &model.DeploymentConfigProfile{
		ID: "cp-2", Name: "node-cfg", ScopeType: model.ConfigScopeNode, ScopeID: "node-9", FeatureID: "f-1",
		ContentJSON: `{"b":2}`, ContentHash: "h2", Version: 2,
	}
	for _, p := range []*model.DeploymentConfigProfile{p1, p2} {
		if err := st.CreateDeploymentConfigProfile(ctx, p); err != nil {
			t.Fatalf("create %s: %v", p.ID, err)
		}
	}

	// Filtering.
	shared, err := st.ListDeploymentConfigProfiles(ctx, model.ConfigScopeShared, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(shared) != 1 || shared[0].ID != "cp-1" {
		t.Fatalf("scope shared: %+v", shared)
	}
	nodeCfg, err := st.ListDeploymentConfigProfiles(ctx, model.ConfigScopeNode, "node-9", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodeCfg) != 1 || nodeCfg[0].ID != "cp-2" {
		t.Fatalf("scope node: %+v", nodeCfg)
	}
	byFeature, err := st.ListDeploymentConfigProfiles(ctx, "", "", "f-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(byFeature) != 2 {
		t.Fatalf("feature filter: %+v", byFeature)
	}
	// Empty filters = no filter.
	all, err := st.ListDeploymentConfigProfiles(ctx, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("no filter: %d", len(all))
	}

	// Duplicate (feature, scope_type, scope_id, name) conflicts.
	dup := *p1
	dup.ID = "cp-dup"
	if err := st.CreateDeploymentConfigProfile(ctx, &dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate config profile: got %v, want ErrConflict", err)
	}

	// Update.
	p1.ContentJSON = `{"a":2}`
	p1.ContentHash = "h1b"
	p1.Version = 2
	if err := st.UpdateDeploymentConfigProfile(ctx, p1); err != nil {
		t.Fatal(err)
	}
	got, err := st.DeploymentConfigProfileByID(ctx, "cp-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentJSON != `{"a":2}` || got.ContentHash != "h1b" || got.Version != 2 {
		t.Fatalf("update not persisted: %+v", got)
	}

	if err := st.DeleteDeploymentConfigProfile(ctx, "cp-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeploymentConfigProfileByID(ctx, "cp-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: got %v, want ErrNotFound", err)
	}
}

// ---- Secret references ----

func TestDeploymentSecretReferenceCRUD(t *testing.T) {
	ctx, st, database := newTestStore(t)
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-1", "secret-key")); err != nil {
		t.Fatal(err)
	}

	// No content/secret column exists: only reference metadata may be stored.
	cols := map[string]bool{}
	rows, err := database.QueryContext(ctx, `PRAGMA table_info(deployment_secret_reference)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	rows.Close()
	for _, forbidden := range []string{"content", "secret", "secret_value", "value"} {
		if cols[forbidden] {
			t.Fatalf("deployment_secret_reference must not persist plaintext column %q", forbidden)
		}
	}

	r1 := &model.DeploymentSecretReference{
		ID: "sr-1", Name: "db-password", FeatureID: "f-1", ScopeType: model.SecretScopeShared,
		ObjectKey: "secrets/s1", Version: 3, ContentHash: "sha256:aaa", EncryptionMode: "aes-gcm", Size: 128,
	}
	r2 := &model.DeploymentSecretReference{
		ID: "sr-2", Name: "node-token", FeatureID: "f-1", ScopeType: model.SecretScopeNode, ScopeID: "node-9",
		ObjectKey: "secrets/s2", Version: 1, ContentHash: "sha256:bbb", EncryptionMode: "none", Size: 64,
	}
	for _, r := range []*model.DeploymentSecretReference{r1, r2} {
		if err := st.CreateDeploymentSecretReference(ctx, r); err != nil {
			t.Fatalf("create %s: %v", r.ID, err)
		}
	}

	byID, err := st.DeploymentSecretReferenceByID(ctx, "sr-1")
	if err != nil {
		t.Fatal(err)
	}
	if byID.ObjectKey != "secrets/s1" || byID.ContentHash != "sha256:aaa" || byID.EncryptionMode != "aes-gcm" || byID.Size != 128 || byID.Version != 3 {
		t.Fatalf("by id: %+v", byID)
	}
	if byID.UpdatedAt.IsZero() {
		t.Fatal("updated_at not persisted")
	}

	// Filtering.
	nodeRefs, err := st.ListDeploymentSecretReferences(ctx, "f-1", model.SecretScopeNode, "node-9")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodeRefs) != 1 || nodeRefs[0].ID != "sr-2" {
		t.Fatalf("node filter: %+v", nodeRefs)
	}
	all, err := st.ListDeploymentSecretReferences(ctx, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("no filter: %d", len(all))
	}

	// Update only reference metadata.
	r1.ObjectKey = "secrets/s1-v4"
	r1.Version = 4
	r1.ContentHash = "sha256:ccc"
	r1.EncryptionMode = "kms-envelope"
	r1.Size = 256
	if err := st.UpdateDeploymentSecretReference(ctx, r1); err != nil {
		t.Fatal(err)
	}
	got, err := st.DeploymentSecretReferenceByID(ctx, "sr-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ObjectKey != "secrets/s1-v4" || got.Version != 4 || got.ContentHash != "sha256:ccc" ||
		got.EncryptionMode != "kms-envelope" || got.Size != 256 {
		t.Fatalf("update not persisted: %+v", got)
	}
	if got.Name != "db-password" || got.FeatureID != "f-1" {
		t.Fatalf("update must not touch identity fields: %+v", got)
	}

	if err := st.DeleteDeploymentSecretReference(ctx, "sr-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeploymentSecretReferenceByID(ctx, "sr-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: got %v, want ErrNotFound", err)
	}
}

// ---- Targets ----

func TestDeploymentTargetCRUD(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-1", "target-key")); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-2", "target-key-2")); err != nil {
		t.Fatal(err)
	}
	insertTestNode(t, ctx, st.DB(), "n-1")
	insertTestNode(t, ctx, st.DB(), "n-2")

	t1 := &model.DeploymentTarget{ID: "t-1", FeatureID: "f-1", NodeID: "n-1", ActualStatus: model.TargetStatusPending, Enabled: true}
	t2 := &model.DeploymentTarget{ID: "t-2", FeatureID: "f-1", NodeID: "n-2", ActualStatus: model.TargetStatusHealthy, Enabled: false}
	t3 := &model.DeploymentTarget{ID: "t-3", FeatureID: "f-2", NodeID: "n-1", ActualStatus: model.TargetStatusRunning, Enabled: true}
	for _, tg := range []*model.DeploymentTarget{t1, t2, t3} {
		if err := st.CreateDeploymentTarget(ctx, tg); err != nil {
			t.Fatalf("create target %s: %v", tg.ID, err)
		}
	}

	byID, err := st.DeploymentTargetByID(ctx, "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if byID.FeatureID != "f-1" || byID.NodeID != "n-1" || !byID.Enabled || byID.ActualStatus != model.TargetStatusPending {
		t.Fatalf("by id: %+v", byID)
	}

	// Duplicate (feature_id, node_id) must conflict.
	dup := &model.DeploymentTarget{ID: "t-dup", FeatureID: "f-1", NodeID: "n-1"}
	if err := st.CreateDeploymentTarget(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate target: got %v, want ErrConflict", err)
	}

	byFeature, err := st.DeploymentTargetsByFeature(ctx, "f-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(byFeature) != 2 {
		t.Fatalf("by feature: %d", len(byFeature))
	}
	byNode, err := st.DeploymentTargetsByNode(ctx, "n-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(byNode) != 2 {
		t.Fatalf("by node: %d", len(byNode))
	}
	all, err := st.ListDeploymentTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("list: %d", len(all))
	}

	// Update.
	checkAt := time.Now().UTC().Truncate(time.Second)
	t1.ActualStatus = model.TargetStatusUnhealthy
	t1.Enabled = false
	t1.ConfigRevision = 3
	t1.LastHealthCheckAt = &checkAt
	if err := st.UpdateDeploymentTarget(ctx, t1); err != nil {
		t.Fatal(err)
	}
	got, err := st.DeploymentTargetByID(ctx, "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ActualStatus != model.TargetStatusUnhealthy || got.Enabled || got.ConfigRevision != 3 ||
		got.LastHealthCheckAt == nil || !got.LastHealthCheckAt.Equal(checkAt) {
		t.Fatalf("update not persisted: %+v", got)
	}

	// Deleting a feature referenced by a target must be rejected by FK RESTRICT.
	if err := st.DeleteDeploymentFeature(ctx, "f-1"); err == nil {
		t.Fatal("expected FK RESTRICT to reject deleting a feature that still has targets")
	}

	// Delete targets then feature deletion succeeds.
	for _, id := range []string{"t-1", "t-2"} {
		if err := st.DeleteDeploymentTarget(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeleteDeploymentFeature(ctx, "f-1"); err != nil {
		t.Fatalf("delete feature after removing targets: %v", err)
	}
	if _, err := st.DeploymentTargetByID(ctx, "t-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: got %v, want ErrNotFound", err)
	}
}

// ---- Target secrets ----

func TestDeploymentTargetSecretCRUD(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-1", "tsec-key")); err != nil {
		t.Fatal(err)
	}
	insertTestNode(t, ctx, st.DB(), "n-1")
	tg := &model.DeploymentTarget{ID: "t-1", FeatureID: "f-1", NodeID: "n-1", ActualStatus: model.TargetStatusPending}
	if err := st.CreateDeploymentTarget(ctx, tg); err != nil {
		t.Fatal(err)
	}
	ref1 := &model.DeploymentSecretReference{ID: "sr-1", Name: "a", FeatureID: "f-1", ObjectKey: "k1", ContentHash: "h1", EncryptionMode: "none", Size: 1}
	ref2 := &model.DeploymentSecretReference{ID: "sr-2", Name: "b", FeatureID: "f-1", ObjectKey: "k2", ContentHash: "h2", EncryptionMode: "none", Size: 2}
	for _, r := range []*model.DeploymentSecretReference{ref1, ref2} {
		if err := st.CreateDeploymentSecretReference(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	ts1 := &model.DeploymentTargetSecret{ID: "ts-1", TargetID: "t-1", SecretReferenceID: "sr-1", BindingPath: "/etc/a", Version: 2, ContentHash: "h1", EncryptionMode: "aes-gcm"}
	ts2 := &model.DeploymentTargetSecret{ID: "ts-2", TargetID: "t-1", SecretReferenceID: "sr-2", BindingPath: "/etc/b", Version: 1, ContentHash: "h2", EncryptionMode: "none"}
	for _, ts := range []*model.DeploymentTargetSecret{ts1, ts2} {
		if err := st.CreateDeploymentTargetSecret(ctx, ts); err != nil {
			t.Fatalf("create %s: %v", ts.ID, err)
		}
	}

	// Duplicate (target_id, secret_reference_id) conflicts.
	dup := *ts1
	dup.ID = "ts-dup"
	if err := st.CreateDeploymentTargetSecret(ctx, &dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate target secret: got %v, want ErrConflict", err)
	}

	list, err := st.ListDeploymentTargetSecretsByTarget(ctx, "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list by target: %d", len(list))
	}

	if err := st.DeleteDeploymentTargetSecretsByTarget(ctx, "t-1"); err != nil {
		t.Fatal(err)
	}
	list, err = st.ListDeploymentTargetSecretsByTarget(ctx, "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("after delete: %d", len(list))
	}
}

// ---- Operations ----

func TestDeploymentOperationCRUD(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-1", "op-key")); err != nil {
		t.Fatal(err)
	}
	o := &model.DeploymentOperation{
		ID: "op-1", Action: model.DeploymentActionInstall, FeatureID: "f-1",
		Strategy: "serial", Status: model.DeploymentStatusQueued, RequestedBy: "tester",
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := st.CreateDeploymentOperation(ctx, o); err != nil {
		t.Fatalf("create: %v", err)
	}

	byID, err := st.DeploymentOperationByID(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if byID.Action != model.DeploymentActionInstall || byID.Status != model.DeploymentStatusQueued || byID.RequestedBy != "tester" {
		t.Fatalf("by id: %+v", byID)
	}
	if byID.CreatedAt.IsZero() {
		t.Fatal("created_at not persisted")
	}

	// Update.
	started := time.Now().UTC().Truncate(time.Second)
	o.Status = model.DeploymentStatusRunning
	o.StartedAt = &started
	o.Reason = "approved"
	if err := st.UpdateDeploymentOperation(ctx, o); err != nil {
		t.Fatal(err)
	}
	got, err := st.DeploymentOperationByID(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.DeploymentStatusRunning || got.Reason != "approved" ||
		got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Fatalf("update not persisted: %+v", got)
	}

	// List, newest first, with explicit created_at ordering.
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newT := old.Add(2 * time.Hour)
	if err := st.CreateDeploymentOperation(ctx, &model.DeploymentOperation{
		ID: "op-old", Action: model.DeploymentActionUpdate, FeatureID: "f-1",
		Strategy: "serial", Status: model.DeploymentStatusDraft, RequestedBy: "t", CreatedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDeploymentOperation(ctx, &model.DeploymentOperation{
		ID: "op-new", Action: model.DeploymentActionUpdate, FeatureID: "f-1",
		Strategy: "serial", Status: model.DeploymentStatusDraft, RequestedBy: "t", CreatedAt: newT,
	}); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListDeploymentOperations(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("list: %d", len(list))
	}
	ids := []string{}
	for _, op := range list {
		ids = append(ids, op.ID)
	}
	if list[0].ID != "op-new" || list[1].ID != "op-old" {
		t.Fatalf("list not newest first: %v", ids)
	}
	limited, err := st.ListDeploymentOperations(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("limit: %d", len(limited))
	}
}

func TestClaimQueuedDeploymentOperation(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-1", "claim-1")); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-2", "claim-2")); err != nil {
		t.Fatal(err)
	}
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	if err := st.CreateDeploymentOperation(ctx, &model.DeploymentOperation{
		ID: "op-later", Action: model.DeploymentActionInstall, FeatureID: "f-2",
		Status: model.DeploymentStatusQueued, RequestedBy: "t", CreatedAt: newer,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDeploymentOperation(ctx, &model.DeploymentOperation{
		ID: "op-earlier", Action: model.DeploymentActionUpdate, FeatureID: "f-1",
		Status: model.DeploymentStatusQueued, RequestedBy: "t", CreatedAt: older,
	}); err != nil {
		t.Fatal(err)
	}

	// Claim returns the oldest queued operation and flips it to running.
	claimed, err := st.ClaimQueuedDeploymentOperation(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != "op-earlier" {
		t.Fatalf("claimed %s, want op-earlier (oldest)", claimed.ID)
	}
	if claimed.Status != model.DeploymentStatusRunning || claimed.StartedAt == nil {
		t.Fatalf("claimed op not running with started_at: %+v", claimed)
	}

	claimed2, err := st.ClaimQueuedDeploymentOperation(ctx)
	if err != nil {
		t.Fatalf("claim 2: %v", err)
	}
	if claimed2.ID != "op-later" {
		t.Fatalf("claimed %s, want op-later", claimed2.ID)
	}

	// Queue empty.
	if _, err := st.ClaimQueuedDeploymentOperation(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim empty queue: got %v, want ErrNotFound", err)
	}
}

func TestClaimQueuedDeploymentOperationConcurrent(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-1", "conc")); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDeploymentOperation(ctx, &model.DeploymentOperation{
		ID: "op-race", Action: model.DeploymentActionInstall, FeatureID: "f-1",
		Status: model.DeploymentStatusQueued, RequestedBy: "t",
	}); err != nil {
		t.Fatal(err)
	}

	const workers = 4
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			op, err := st.ClaimQueuedDeploymentOperation(ctx)
			if err != nil {
				results <- err
				return
			}
			if op == nil || op.ID != "op-race" {
				results <- fmt.Errorf("claimed wrong op: %+v", op)
				return
			}
			results <- nil
		}()
	}

	success, notFound := 0, 0
	for i := 0; i < workers; i++ {
		switch err := <-results; {
		case err == nil:
			success++
		case errors.Is(err, ErrNotFound):
			notFound++
		default:
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if success != 1 || notFound != workers-1 {
		t.Fatalf("expected exactly one winner, got success=%d notFound=%d", success, notFound)
	}

	// The winner's op is running.
	got, err := st.DeploymentOperationByID(ctx, "op-race")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.DeploymentStatusRunning || got.StartedAt == nil {
		t.Fatalf("op not running after claim: %+v", got)
	}
}

func TestActiveDeploymentOperationForFeature(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-1", "active-key")); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-2", "idle-key")); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDeploymentOperation(ctx, &model.DeploymentOperation{
		ID: "op-1", Action: model.DeploymentActionInstall, FeatureID: "f-1",
		Status: model.DeploymentStatusQueued, RequestedBy: "t",
	}); err != nil {
		t.Fatal(err)
	}

	active, err := st.ActiveDeploymentOperationForFeature(ctx, "f-1")
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if active.ID != "op-1" {
		t.Fatalf("active op: %s", active.ID)
	}

	if _, err := st.ActiveDeploymentOperationForFeature(ctx, "f-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("idle feature: got %v, want ErrNotFound", err)
	}

	// Once the op is finished it is no longer active.
	if err := st.UpdateDeploymentOperation(ctx, &model.DeploymentOperation{
		ID: "op-1", Action: model.DeploymentActionInstall, FeatureID: "f-1",
		Status: model.DeploymentStatusSucceeded, RequestedBy: "t", FinishedAt: nowPtr(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActiveDeploymentOperationForFeature(ctx, "f-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after finish: got %v, want ErrNotFound", err)
	}
}

// ---- Operation targets ----

func TestDeploymentOperationTargetCRUDAndNodeSerial(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-1", "opt-key")); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-2", "opt-key-2")); err != nil {
		t.Fatal(err)
	}
	insertTestNode(t, ctx, st.DB(), "n-1")
	if err := st.CreateDeploymentOperation(ctx, &model.DeploymentOperation{
		ID: "op-1", Action: model.DeploymentActionInstall, FeatureID: "f-1", Status: model.DeploymentStatusQueued, RequestedBy: "t",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDeploymentOperation(ctx, &model.DeploymentOperation{
		ID: "op-2", Action: model.DeploymentActionUpdate, FeatureID: "f-2", Status: model.DeploymentStatusQueued, RequestedBy: "t",
	}); err != nil {
		t.Fatal(err)
	}
	tg1 := &model.DeploymentTarget{ID: "t-1", FeatureID: "f-1", NodeID: "n-1", ActualStatus: model.TargetStatusPending}
	tg2 := &model.DeploymentTarget{ID: "t-2", FeatureID: "f-2", NodeID: "n-1", ActualStatus: model.TargetStatusPending}
	for _, tg := range []*model.DeploymentTarget{tg1, tg2} {
		if err := st.CreateDeploymentTarget(ctx, tg); err != nil {
			t.Fatal(err)
		}
	}

	ot1 := &model.DeploymentOperationTarget{
		ID: "ot-1", OperationID: "op-1", TargetID: "t-1", NodeID: "n-1",
		Status: model.DeploymentStatusRunning,
	}
	if err := st.CreateDeploymentOperationTarget(ctx, ot1); err != nil {
		t.Fatalf("create first running target: %v", err)
	}
	byID, err := st.DeploymentOperationTargetByID(ctx, "ot-1")
	if err != nil {
		t.Fatal(err)
	}
	if byID.Status != model.DeploymentStatusRunning || byID.OperationID != "op-1" || byID.NodeID != "n-1" {
		t.Fatalf("by id: %+v", byID)
	}

	// Node serial: a second queued/running target on the same node conflicts.
	ot2 := &model.DeploymentOperationTarget{
		ID: "ot-2", OperationID: "op-2", TargetID: "t-2", NodeID: "n-1",
		Status: model.DeploymentStatusQueued,
	}
	if err := st.CreateDeploymentOperationTarget(ctx, ot2); !errors.Is(err, ErrConflict) {
		t.Fatalf("second queued target on same node: got %v, want ErrConflict", err)
	}
	ot2Running := *ot2
	ot2Running.ID = "ot-2b"
	ot2Running.Status = model.DeploymentStatusRunning
	if err := st.CreateDeploymentOperationTarget(ctx, &ot2Running); !errors.Is(err, ErrConflict) {
		t.Fatalf("running target while another active: got %v, want ErrConflict", err)
	}

	// List by operation returns only that operation's targets.
	only, err := st.ListDeploymentOperationTargetsByOperation(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].ID != "ot-1" {
		t.Fatalf("list by op: %+v", only)
	}

	// Update.
	ot1.Status = model.DeploymentStatusSucceeded
	ot1.ErrorMessage = ""
	ot1.FinishedAt = nowPtr()
	if err := st.UpdateDeploymentOperationTarget(ctx, ot1); err != nil {
		t.Fatal(err)
	}
	got, err := st.DeploymentOperationTargetByID(ctx, "ot-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.DeploymentStatusSucceeded || got.FinishedAt == nil {
		t.Fatalf("update not persisted: %+v", got)
	}

	// Once the active target finishes, the node is free for the next target.
	if err := st.CreateDeploymentOperationTarget(ctx, ot2); err != nil {
		t.Fatalf("queued target after previous finished: %v", err)
	}

	// A pending (inactive) target never conflicts, even while ot2 is queued.
	ot3 := &model.DeploymentOperationTarget{
		ID: "ot-3", OperationID: "op-2", TargetID: "t-1", NodeID: "n-1", Status: model.TargetStatusPending,
	}
	if err := st.CreateDeploymentOperationTarget(ctx, ot3); err != nil {
		t.Fatalf("pending target on same node must be allowed: %v", err)
	}
}

// ---- Steps ----

func TestDeploymentStepCRUD(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-1", "step-key")); err != nil {
		t.Fatal(err)
	}
	insertTestNode(t, ctx, st.DB(), "n-1")
	if err := st.CreateDeploymentOperation(ctx, &model.DeploymentOperation{
		ID: "op-1", Action: model.DeploymentActionInstall, FeatureID: "f-1", Status: model.DeploymentStatusQueued, RequestedBy: "t",
	}); err != nil {
		t.Fatal(err)
	}
	tg := &model.DeploymentTarget{ID: "t-1", FeatureID: "f-1", NodeID: "n-1", ActualStatus: model.TargetStatusPending}
	if err := st.CreateDeploymentTarget(ctx, tg); err != nil {
		t.Fatal(err)
	}
	ot := &model.DeploymentOperationTarget{
		ID: "ot-1", OperationID: "op-1", TargetID: "t-1", NodeID: "n-1", Status: model.DeploymentStatusQueued,
	}
	if err := st.CreateDeploymentOperationTarget(ctx, ot); err != nil {
		t.Fatal(err)
	}

	s1 := &model.DeploymentStep{ID: "s-1", OperationID: "op-1", OperationTargetID: "ot-1", NodeID: "n-1", StepType: "backup", Status: "pending"}
	s2 := &model.DeploymentStep{ID: "s-2", OperationID: "op-1", OperationTargetID: "ot-1", NodeID: "n-1", StepType: "install", Status: "pending"}
	for _, s := range []*model.DeploymentStep{s1, s2} {
		if err := st.CreateDeploymentStep(ctx, s); err != nil {
			t.Fatalf("create step %s: %v", s.ID, err)
		}
	}
	list, err := st.ListDeploymentStepsByOperation(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list by op: %d", len(list))
	}

	s1.Status = "running"
	s1.TaskID = "task-9"
	s1.Message = "executing"
	started := time.Now().UTC().Truncate(time.Second)
	s1.StartedAt = &started
	if err := st.UpdateDeploymentStep(ctx, s1); err != nil {
		t.Fatal(err)
	}
	got, err := stepByIDHelper(ctx, st, "s-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" || got.TaskID != "task-9" || got.Message != "executing" ||
		got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Fatalf("update not persisted: %+v", got)
	}
}

// stepByIDHelper reads a step back through the store's list accessor.
func stepByIDHelper(ctx context.Context, st *Store, id string) (*model.DeploymentStep, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT `+deploymentStepColumns+` FROM deployment_step WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrNotFound
	}
	step, err := scanDeploymentStep(rows)
	if err != nil {
		return nil, err
	}
	return step, rows.Err()
}

// ---- Backups ----

func TestDeploymentBackupCRUD(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	if err := st.CreateDeploymentFeature(ctx, newFeature("f-1", "backup-key")); err != nil {
		t.Fatal(err)
	}
	insertTestNode(t, ctx, st.DB(), "n-1")
	insertTestNode(t, ctx, st.DB(), "n-2")
	if err := st.CreateDeploymentOperation(ctx, &model.DeploymentOperation{
		ID: "op-1", Action: model.DeploymentActionUpdate, FeatureID: "f-1", Status: model.DeploymentStatusDraft, RequestedBy: "t",
	}); err != nil {
		t.Fatal(err)
	}
	tg := &model.DeploymentTarget{ID: "t-1", FeatureID: "f-1", NodeID: "n-1", ActualStatus: model.TargetStatusPending}
	if err := st.CreateDeploymentTarget(ctx, tg); err != nil {
		t.Fatal(err)
	}

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newT := old.Add(3 * time.Hour)
	b1 := &model.DeploymentBackup{
		ID: "b-1", OperationID: "op-1", TargetID: "t-1", NodeID: "n-1", FeatureID: "f-1",
		BackupMode: "full", ObjectKey: "backups/b1", Size: 10, SHA256: "s1", Status: "completed", CreatedAt: old,
	}
	b2 := &model.DeploymentBackup{
		ID: "b-2", OperationID: "op-1", TargetID: "t-1", NodeID: "n-1", FeatureID: "f-1",
		BackupMode: "incremental", ObjectKey: "backups/b2", Size: 20, SHA256: "s2", Status: "pending", CreatedAt: newT,
	}
	for _, b := range []*model.DeploymentBackup{b1, b2} {
		if err := st.CreateDeploymentBackup(ctx, b); err != nil {
			t.Fatalf("create %s: %v", b.ID, err)
		}
	}

	byID, err := st.DeploymentBackupByID(ctx, "b-1")
	if err != nil {
		t.Fatal(err)
	}
	if byID.ObjectKey != "backups/b1" || byID.Status != "completed" || byID.Size != 10 {
		t.Fatalf("by id: %+v", byID)
	}

	// Newest first.
	all, err := st.ListDeploymentBackups(ctx, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "b-2" {
		t.Fatalf("list not newest first: %+v", all)
	}
	byFeature, err := st.ListDeploymentBackups(ctx, "f-1", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(byFeature) != 2 {
		t.Fatalf("feature filter: %d", len(byFeature))
	}
	byNode, err := st.ListDeploymentBackups(ctx, "", "n-2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(byNode) != 0 {
		t.Fatalf("node filter: %d", len(byNode))
	}
	limited, err := st.ListDeploymentBackups(ctx, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].ID != "b-2" {
		t.Fatalf("limit: %+v", limited)
	}

	// Update.
	b1.Status = "failed"
	b1.SHA256 = "s1-updated"
	b1.Size = 99
	if err := st.UpdateDeploymentBackup(ctx, b1); err != nil {
		t.Fatal(err)
	}
	got, err := st.DeploymentBackupByID(ctx, "b-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.SHA256 != "s1-updated" || got.Size != 99 {
		t.Fatalf("update not persisted: %+v", got)
	}
}

// ---- Bootstrap sessions ----

func TestBootstrapSessionCRUD(t *testing.T) {
	ctx, st, _ := newTestStore(t)
	insertTestNode(t, ctx, st.DB(), "n-1")
	expires := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	b := &model.BootstrapSession{
		ID: "bs-1", NodeID: "n-1", Status: model.BootstrapStatusCreated, TokenHash: "sha256:token-hash",
		Bucket: "repo-bucket", Prefix: "agent/", Region: "cn-shanghai", ExpiresAt: expires,
	}
	if err := st.CreateBootstrapSession(ctx, b); err != nil {
		t.Fatalf("create: %v", err)
	}

	byID, err := st.BootstrapSessionByID(ctx, "bs-1")
	if err != nil {
		t.Fatal(err)
	}
	if byID.TokenHash != "sha256:token-hash" || byID.Status != model.BootstrapStatusCreated || byID.Bucket != "repo-bucket" {
		t.Fatalf("by id: %+v", byID)
	}
	if !byID.ExpiresAt.Equal(expires) {
		t.Fatalf("expires_at mismatch: %v vs %v", byID.ExpiresAt, expires)
	}

	byHash, err := st.BootstrapSessionByTokenHash(ctx, "sha256:token-hash")
	if err != nil {
		t.Fatal(err)
	}
	if byHash.ID != "bs-1" {
		t.Fatalf("by token hash: %s", byHash.ID)
	}
	if _, err := st.BootstrapSessionByTokenHash(ctx, "sha256:nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token hash: got %v, want ErrNotFound", err)
	}

	// Update.
	b.Status = model.BootstrapStatusRepositorySyncing
	b.LastState = "cloning"
	if err := st.UpdateBootstrapSession(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := st.BootstrapSessionByID(ctx, "bs-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.BootstrapStatusRepositorySyncing || got.LastState != "cloning" {
		t.Fatalf("update not persisted: %+v", got)
	}

	// Revoke.
	if err := st.RevokeBootstrapSession(ctx, "bs-1"); err != nil {
		t.Fatal(err)
	}
	got, err = st.BootstrapSessionByID(ctx, "bs-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.BootstrapStatusCancelled || got.RevokedAt == nil {
		t.Fatalf("revoke not persisted: %+v", got)
	}

	// List newest first.
	list, err := st.ListBootstrapSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list: %d", len(list))
	}
}

// nowPtr returns a pointer to the current UTC time.
func nowPtr() *time.Time {
	t := time.Now().UTC()
	return &t
}
