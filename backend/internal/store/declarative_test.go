package store

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"servercli/internal/db"
	"servercli/internal/logger"
	"servercli/internal/model"
)

func newDeclarativeTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	log := logger.New(io.Discard, "error")
	database, err := db.Open(ctx, "sqlite", filepath.Join(dir, "test.db"), log)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return New(database, log)
}

func TestClusterCRUD(t *testing.T) {
	s := newDeclarativeTestStore(t)
	ctx := context.Background()

	c := &model.Cluster{
		ID: "clu-1", Name: "prod", Environment: "production",
		PrimaryEpoch: 1, ReleaseChannel: "stable", OSSProviderRef: "oss-main",
		Status: model.ClusterStatusActive,
	}
	if err := s.CreateCluster(ctx, c); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.ClusterByID(ctx, "clu-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "prod" || got.Environment != "production" || got.PrimaryEpoch != 1 || got.Status != "active" {
		t.Fatalf("unexpected cluster: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("timestamps missing: %+v", got)
	}

	// update epoch + active primary
	got.ActivePrimaryNodeID = "node-p"
	got.PrimaryEpoch = 2
	if err := s.UpdateCluster(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, err := s.ClusterByID(ctx, "clu-1")
	if err != nil {
		t.Fatal(err)
	}
	if got2.ActivePrimaryNodeID != "node-p" || got2.PrimaryEpoch != 2 {
		t.Fatalf("update not applied: %+v", got2)
	}

	// list filtered
	list, err := s.ListClusters(ctx, "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "clu-1" {
		t.Fatalf("list: %+v", list)
	}
	list2, err := s.ListClusters(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(list2) != 0 {
		t.Fatalf("expected empty filter, got %+v", list2)
	}

	// not found
	if _, err := s.ClusterByID(ctx, "missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// delete
	if err := s.DeleteCluster(ctx, "clu-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.ClusterByID(ctx, "clu-1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestNodeProfileCRUD(t *testing.T) {
	s := newDeclarativeTestStore(t)
	ctx := context.Background()

	p := &model.NodeProfile{
		ID: "prof-1", ClusterID: "clu-1", Name: "primary-foundation", Version: "1",
		Status: "active",
	}
	mods := []model.ProfileModule{
		{ModuleID: "docker", Version: "1.0", Config: map[string]string{"enabled": "true"}, RiskLevel: model.RiskLow},
		{ModuleID: "postgres", Version: "1.2", RiskLevel: model.RiskMedium},
	}
	if err := p.MarshalProfileModules(mods); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNodeProfile(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.NodeProfileByID(ctx, "prof-1")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := got.UnmarshalProfileModules()
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 || decoded[0].ModuleID != "docker" || decoded[1].ModuleID != "postgres" {
		t.Fatalf("unexpected modules: %+v", decoded)
	}

	// by name picks latest version
	p2 := &model.NodeProfile{ID: "prof-2", ClusterID: "clu-1", Name: "primary-foundation", Version: "2", Status: "active"}
	if err := s.CreateNodeProfile(ctx, p2); err != nil {
		t.Fatal(err)
	}
	latest, err := s.NodeProfileByName(ctx, "clu-1", "primary-foundation")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Version != "2" {
		t.Fatalf("expected latest version 2, got %s", latest.Version)
	}

	list, err := s.ListNodeProfiles(ctx, "clu-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(list))
	}
}

func TestDeclarativeNodeCRUD(t *testing.T) {
	s := newDeclarativeTestStore(t)
	ctx := context.Background()

	n := &model.DeclarativeNode{
		ID: "dn-1", ClusterID: "clu-1", NodeID: "node-1", Role: "primary",
		ProfileID: "prof-1", Lifecycle: model.NodeLifecycleReady, Status: model.NodeStatusOnline,
		OSName: "centos", OSVersion: "7", Arch: "amd64", IdentityGeneration: 3,
		LegacyMAC: "aa:bb:cc:dd:ee:ff", // migration metadata only
	}
	n.AddressesJSON = `[{"address":"10.0.0.1","address_type":"private","preferred":true}]`
	if err := s.CreateDeclarativeNode(ctx, n); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.DeclarativeNodeByNodeID(ctx, "clu-1", "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Lifecycle != model.NodeLifecycleReady || got.IdentityGeneration != 3 || got.LegacyMAC != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("unexpected node: %+v", got)
	}
	addrs, err := got.UnmarshalNodeAddresses()
	if err != nil {
		t.Fatal(err)
	}
	if len(addrs) != 1 || addrs[0].Address != "10.0.0.1" || !addrs[0].Preferred {
		t.Fatalf("unexpected addresses: %+v", addrs)
	}

	// replacement keeps node_id, changes machine identity generation
	got.IdentityGeneration++
	got.ReplacementStatus = "reprovisioned"
	if err := s.UpdateDeclarativeNode(ctx, got); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.DeclarativeNodeByNodeID(ctx, "clu-1", "node-1")
	if got2.IdentityGeneration != 4 || got2.ReplacementStatus != "reprovisioned" {
		t.Fatalf("replacement update failed: %+v", got2)
	}

	list, err := s.ListDeclarativeNodes(ctx, "clu-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 node, got %d", len(list))
	}
}

func TestServiceReferenceCRUD(t *testing.T) {
	s := newDeclarativeTestStore(t)
	ctx := context.Background()

	ref := &model.ServiceReference{
		ID: "ref-1", ClusterID: "clu-1", Name: "postgres-main",
		NodeID: "node-1", Address: "10.0.0.5", Port: 5432,
		SecretRef: &model.SecretRef{Key: "postgres.password", Store: "bootstrap"},
		Status:    "active",
	}
	if err := s.CreateServiceReference(ctx, ref); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.ServiceReferenceByID(ctx, "ref-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != 5432 || got.SecretRef == nil || got.SecretRef.Key != "postgres.password" {
		t.Fatalf("unexpected ref: %+v", got)
	}

	got.Address = "10.0.0.6"
	got.Port = 5433
	if err := s.UpdateServiceReference(ctx, got); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.ServiceReferenceByID(ctx, "ref-1")
	if got2.Address != "10.0.0.6" || got2.Port != 5433 {
		t.Fatalf("update failed: %+v", got2)
	}
}

func TestOperationV2CRUDAndSteps(t *testing.T) {
	s := newDeclarativeTestStore(t)
	ctx := context.Background()

	deadline := time.Now().UTC().Add(time.Hour)
	op := &model.Operation{
		ID: "op-1", OperationID: "op-id-1", OperationType: model.OpTypeUpdate,
		ClusterID: "clu-1", NodeID: "node-1", ModuleID: "docker",
		ArgumentsJSON: `{"image":"nginx:1.25"}`, Approval: "auto", RiskLevel: model.RiskMedium,
		IdempotencyKey: "idem-1", Deadline: &deadline, PrimaryEpoch: 1,
		Status: model.OpStatusPlanned, RequestedBy: "admin-1",
	}
	if err := s.CreateOperation(ctx, op); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.OperationByID(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.OpStatusPlanned || got.PrimaryEpoch != 1 || got.IdempotencyKey != "idem-1" {
		t.Fatalf("unexpected op: %+v", got)
	}

	// idempotency lookup
	dup, err := s.OperationByIdempotency(ctx, "admin-1", "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	if dup.ID != "op-1" {
		t.Fatalf("idempotency lookup failed: %+v", dup)
	}

	// steps
	for i, seq := range []string{"plan", "apply", "verify"} {
		st := &model.OperationStep{ID: "st-" + seq, OperationID: "op-1", Sequence: i, ModuleID: "docker", Operation: seq, Status: "pending"}
		if err := s.CreateOperationStep(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	steps, err := s.ListOperationSteps(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 || steps[0].Sequence != 0 || steps[2].Sequence != 2 {
		t.Fatalf("unexpected steps: %+v", steps)
	}

	// status transition + step update
	if err := s.UpdateOperationStatus(ctx, "op-1", model.OpStatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.OperationByID(ctx, "op-1")
	if got2.Status != model.OpStatusSucceeded || got2.FinishedAt == nil {
		t.Fatalf("terminal transition not persisted: %+v", got2)
	}
	if err := s.UpdateOperationStepStatus(ctx, "st-apply", "succeeded", "commit:abc", "", ""); err != nil {
		t.Fatal(err)
	}
	steps2, _ := s.ListOperationSteps(ctx, "op-1")
	if steps2[1].Status != "succeeded" || steps2[1].CommitPoint != "commit:abc" {
		t.Fatalf("step update failed: %+v", steps2[1])
	}

	// list filter
	list, err := s.ListOperations(ctx, "clu-1", "", model.OpStatusSucceeded, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 op, got %d", len(list))
	}
}

func TestReleaseCacheAndOSSSync(t *testing.T) {
	s := newDeclarativeTestStore(t)
	ctx := context.Background()

	uploaded := time.Now().UTC().Add(-time.Minute)
	verified := time.Now().UTC()
	e := &model.ReleaseCacheEntry{
		ID: "rc-1", Version: "0.0.9", SourceRepository: "inoriyuuuki/serverCli",
		SourceRelease: "0.0.9", OS: "linux", Arch: "amd64", ArtifactName: "servercli-linux-amd64",
		ArtifactSize: 12345, SHA256: "abc123", ModulesVersion: "1.0", SchemaMin: "1.0", SchemaMax: "8",
		OSSKey: "servercli/releases/0.0.9/servercli-linux-amd64", Status: model.ReleaseCacheAvailable,
		UploadedAt: &uploaded, VerifiedAt: &verified,
	}
	if err := s.CreateReleaseCacheEntry(ctx, e); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.ReleaseCacheEntryByVersion(ctx, "0.0.9", "servercli-linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.ReleaseCacheAvailable || got.SHA256 != "abc123" {
		t.Fatalf("unexpected entry: %+v", got)
	}

	// status update
	if err := s.UpdateReleaseCacheEntryStatus(ctx, "rc-1", model.ReleaseCacheFailed, nil, nil); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.ReleaseCacheEntryByID(ctx, "rc-1")
	if got2.Status != model.ReleaseCacheFailed {
		t.Fatalf("status update failed: %+v", got2)
	}

	// oss sync
	sync := &model.OSSSyncRevision{
		ID: "os-1", ClusterID: "clu-1", Kind: "release_cache", ObjectKey: "servercli/releases/0.0.9/release-manifest.json",
		SHA256: "def456", Direction: "upload", Status: "verified",
	}
	if err := s.CreateOSSSyncRevision(ctx, sync); err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListOSSSyncRevisions(ctx, "release_cache", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ObjectKey != sync.ObjectKey {
		t.Fatalf("unexpected sync list: %+v", listed)
	}
}

func TestBackupSetAndPrimaryTransfer(t *testing.T) {
	s := newDeclarativeTestStore(t)
	ctx := context.Background()

	b := &model.BackupSet{
		ID: "bk-1", BackupID: "bak-20260811", RecoverySetID: "rs-1",
		ClusterID: "clu-1", NodeID: "node-1", ServiceInstanceID: "svc-postgres",
		ModuleVersion: "1.2", AppVersion: "16", SchemaVersion: "1.0",
		FilesJSON: `["/var/lib/servercli/postgres/data"]`, SHA256: "abc", SizeBytes: 2048,
		OSSKey: "servercli/backups/node-1/postgres/bak-20260811", Status: "verified",
	}
	if err := s.CreateBackupSet(ctx, b); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.BackupSetByID(ctx, "bk-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.BackupID != "bak-20260811" || got.Status != "verified" {
		t.Fatalf("unexpected backup: %+v", got)
	}
	list, err := s.ListBackupSets(ctx, "node-1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(list))
	}

	tr := &model.PrimaryTransfer{
		ID: "pt-1", ClusterID: "clu-1", FromNodeID: "node-p", ToNodeID: "node-c",
		PrimaryEpoch: 2, Status: model.TransferPlanning, BackupSetID: "bk-1",
		RequestedBy: "admin-1",
	}
	if err := s.CreatePrimaryTransfer(ctx, tr); err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	gotTr, err := s.PrimaryTransferByID(ctx, "pt-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotTr.Status != model.TransferPlanning || gotTr.PrimaryEpoch != 2 {
		t.Fatalf("unexpected transfer: %+v", gotTr)
	}
	if err := s.UpdatePrimaryTransferStatus(ctx, "pt-1", model.TransferCompleted, "", ""); err != nil {
		t.Fatal(err)
	}
	gotTr2, _ := s.PrimaryTransferByID(ctx, "pt-1")
	if gotTr2.Status != model.TransferCompleted || gotTr2.CompletedAt == nil {
		t.Fatalf("transfer completion failed: %+v", gotTr2)
	}
	transfers, err := s.ListPrimaryTransfers(ctx, "clu-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(transfers) != 1 {
		t.Fatalf("expected 1 transfer, got %d", len(transfers))
	}
}
