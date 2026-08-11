package releasecache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"servercli/internal/bundle"
	"servercli/internal/oss"
)

type fakeGitHub struct {
	metas     []ReleaseArtifactMeta
	artifacts map[string][]byte
	lists     int
	downloads int
}

func (fake *fakeGitHub) ListArtifacts(context.Context, string, string, string) ([]ReleaseArtifactMeta, error) {
	fake.lists++
	return append([]ReleaseArtifactMeta(nil), fake.metas...), nil
}

func (fake *fakeGitHub) DownloadArtifact(_ context.Context, _, _, _, name string) ([]byte, error) {
	fake.downloads++
	data, ok := fake.artifacts[name]
	if !ok {
		return nil, fmt.Errorf("missing artifact %s", name)
	}
	return append([]byte(nil), data...), nil
}

type memoryOSS struct {
	mu               sync.Mutex
	objects          map[string][]byte
	contentTypes     map[string]string
	putCalls         int
	putVerifiedCalls int
	corruptReadback  bool
}

func newMemoryOSS() *memoryOSS {
	return &memoryOSS{objects: make(map[string][]byte), contentTypes: make(map[string]string)}
}

func (store *memoryOSS) Put(_ context.Context, key string, data []byte, contentType string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.putCalls++
	store.objects[key] = append([]byte(nil), data...)
	store.contentTypes[key] = contentType
	return "etag", nil
}

func (store *memoryOSS) Get(_ context.Context, key string) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	data, ok := store.objects[key]
	if !ok {
		return nil, oss.ErrNotFound
	}
	out := append([]byte(nil), data...)
	if store.corruptReadback && len(out) > 0 {
		out[0] ^= 0xff
	}
	return out, nil
}

func (store *memoryOSS) Head(ctx context.Context, key string) (*oss.ObjectMeta, error) {
	data, err := store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return &oss.ObjectMeta{Key: key, Size: int64(len(data)), ContentType: store.contentTypes[key]}, nil
}

func (store *memoryOSS) Exists(ctx context.Context, key string) (bool, error) {
	_, err := store.Head(ctx, key)
	if errors.Is(err, oss.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (store *memoryOSS) Delete(_ context.Context, key string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.objects, key)
	return nil
}

func (store *memoryOSS) PutVerified(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	store.mu.Lock()
	store.putVerifiedCalls++
	store.mu.Unlock()
	if _, err := store.Put(ctx, key, data, contentType); err != nil {
		return "", err
	}
	digest := oss.SHA256Hex(data)
	if _, err := store.GetVerified(ctx, key, digest); err != nil {
		return "", err
	}
	return digest, nil
}

func (store *memoryOSS) GetVerified(ctx context.Context, key, expectedSHA256 string) ([]byte, error) {
	data, err := store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if err := oss.VerifySHA256(data, expectedSHA256); err != nil {
		return nil, err
	}
	return data, nil
}

func syncFixture() (*fakeGitHub, *memoryOSS, SyncOptions) {
	one := []byte("artifact-one")
	two := []byte("artifact-two")
	github := &fakeGitHub{
		metas: []ReleaseArtifactMeta{
			{Name: "servercli-linux-amd64.tar.gz", Size: int64(len(one)), SHA256: oss.SHA256Hex(one)},
			{Name: "deploy-install-servercli.sh", Size: int64(len(two)), SHA256: oss.SHA256Hex(two)},
		},
		artifacts: map[string][]byte{"servercli-linux-amd64.tar.gz": one, "deploy-install-servercli.sh": two},
	}
	objectStore := newMemoryOSS()
	return github, objectStore, SyncOptions{
		Version: "v1.2.3", Owner: "example", Repo: "servercli", Tag: "v1.2.3",
		GitHub: github, OSS: objectStore, OS: "linux", Arch: "amd64",
		ModulesVersion: "modules-v1", Schema: SchemaCompatInfo{Min: "1", Max: "3"}, Timeout: time.Second,
	}
}

func TestPlanSyncAndApplySync(t *testing.T) {
	github, objectStore, opts := syncFixture()
	plan, err := PlanSync(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ManifestOSSKey != "servercli/releases/v1.2.3/release-manifest.json" || plan.SHA256SumsOSSKey != "servercli/releases/v1.2.3/sha256sums.txt" {
		t.Fatalf("unexpected plan keys: %#v", plan)
	}
	if len(plan.Artifacts) != 2 || plan.Artifacts[0].OSSKey != "servercli/releases/v1.2.3/deploy-install-servercli.sh" {
		t.Fatalf("unexpected artifact plan: %#v", plan.Artifacts)
	}

	result, err := ApplySync(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.AlreadyUploaded || len(result.Uploaded) != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if objectStore.putVerifiedCalls != 5 { // 2 artifacts + sums + cache manifest + bootstrap release manifest
		t.Fatalf("PutVerified calls = %d, want 5", objectStore.putVerifiedCalls)
	}
	// The bootstrap-compatible release manifest must be parseable by bundle.
	bmData, err := objectStore.Get(context.Background(), "servercli/releases/v1.2.3/"+bundle.ReleaseManifestName)
	if err != nil {
		t.Fatalf("bootstrap release manifest missing: %v", err)
	}
	bm, err := bundle.LoadReleaseManifest(bmData)
	if err != nil {
		t.Fatalf("bootstrap release manifest does not parse: %v", err)
	}
	if bm.ReleaseVersion != "v1.2.3" || len(bm.Artifacts) != 2 {
		t.Fatalf("unexpected bootstrap manifest: %#v", bm)
	}
	manifestData, err := objectStore.Get(context.Background(), plan.ManifestOSSKey)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := Parse(manifestData)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "available" || manifest.VerifiedAt.IsZero() || manifest.UploadedAt.IsZero() {
		t.Fatalf("manifest was not marked available: %#v", manifest)
	}
	if github.downloads != 2 {
		t.Fatalf("downloads = %d, want 2", github.downloads)
	}
}

func TestApplySyncSHA256MismatchFailsBeforeAnyUpload(t *testing.T) {
	_, objectStore, opts := syncFixture()
	bad := []byte("same-size-bad")
	opts.GitHub.(*fakeGitHub).artifacts["artifact.bin"] = bad
	opts.GitHub.(*fakeGitHub).metas = []ReleaseArtifactMeta{{Name: "artifact.bin", Size: int64(len(bad)), SHA256: oss.SHA256Hex([]byte("same-size-ok!"))}}
	_, err := ApplySync(context.Background(), opts)
	if err == nil {
		t.Fatal("ApplySync succeeded with a bad GitHub digest")
	}
	if objectStore.putCalls != 0 {
		t.Fatalf("objects were uploaded before all GitHub digests were verified: %d puts", objectStore.putCalls)
	}
}

func TestApplySyncPutVerifiedChecksReadback(t *testing.T) {
	_, objectStore, opts := syncFixture()
	objectStore.corruptReadback = true
	_, err := ApplySync(context.Background(), opts)
	if err == nil {
		t.Fatal("ApplySync succeeded despite a corrupt OSS readback")
	}
	if objectStore.putVerifiedCalls == 0 {
		t.Fatal("fake PutVerified was not exercised")
	}
}

func TestApplySyncIdempotentVerifiedManifestSkipsUpload(t *testing.T) {
	github, objectStore, opts := syncFixture()
	first, err := ApplySync(context.Background(), opts)
	if err != nil || !first.Verified {
		t.Fatalf("first sync: result=%#v err=%v", first, err)
	}
	puts := objectStore.putCalls
	downloads := github.downloads
	second, err := ApplySync(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Verified || !second.AlreadyUploaded {
		t.Fatalf("second sync did not report already uploaded: %#v", second)
	}
	if objectStore.putCalls != puts {
		t.Fatalf("idempotent sync uploaded again: before=%d after=%d", puts, objectStore.putCalls)
	}
	if github.downloads != downloads {
		t.Fatalf("idempotent sync downloaded artifacts again: before=%d after=%d", downloads, github.downloads)
	}
}

func TestCLIArgsRejectSecretsAndKeepPlanCredentialFree(t *testing.T) {
	secret := "do-not-put-this-on-argv"
	planArgs := []string{"plan", "--owner", "example", "--repo", "servercli", "--tag", "v1"}
	for _, arg := range planArgs {
		if arg == secret {
			t.Fatalf("plan argv contains secret value %q", secret)
		}
	}
	if err := ValidateCredentialFreeArgs(planArgs); err != nil {
		t.Fatalf("safe plan arguments rejected: %v", err)
	}
	for _, args := range [][]string{
		{"apply", "--oss-access-key-secret=" + secret},
		{"plan", "--github-token", secret},
		{"apply", "--oss-access-key-id", secret},
	} {
		if err := ValidateCredentialFreeArgs(args); err == nil {
			t.Fatalf("credential-bearing argv accepted: %#v", args)
		}
	}
	if err := ValidateCredentialFreeArgs([]string{"apply", "--oss-ak-file", "/run/secrets/oss"}); err != nil {
		t.Fatalf("protected credential file path should be allowed: %v", err)
	}
}
