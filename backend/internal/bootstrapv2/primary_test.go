package bootstrapv2

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"servercli/internal/bootstrap"
	"servercli/internal/bundle"
	"servercli/internal/initstate"
	"servercli/internal/oss"
)

type memoryOSS struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemoryOSS() *memoryOSS { return &memoryOSS{objects: map[string][]byte{}} }

func (m *memoryOSS) Put(_ context.Context, key string, data []byte, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = append([]byte(nil), data...)
	return oss.SHA256Hex(data), nil
}
func (m *memoryOSS) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, oss.ErrNotFound
	}
	return append([]byte(nil), data...), nil
}
func (m *memoryOSS) Head(ctx context.Context, key string) (*oss.ObjectMeta, error) {
	data, err := m.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return &oss.ObjectMeta{Key: key, Size: int64(len(data))}, nil
}
func (m *memoryOSS) Exists(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[key]
	return ok, nil
}
func (m *memoryOSS) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}
func (m *memoryOSS) PutVerified(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	if _, err := m.Put(ctx, key, data, contentType); err != nil {
		return "", err
	}
	got, err := m.Get(ctx, key)
	if err != nil {
		return "", err
	}
	if !reflect.DeepEqual(got, data) {
		return "", errors.New("read-back mismatch")
	}
	return oss.SHA256Hex(data), nil
}
func (m *memoryOSS) GetVerified(ctx context.Context, key, expected string) ([]byte, error) {
	data, err := m.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if err := oss.VerifySHA256(data, expected); err != nil {
		return nil, err
	}
	return data, nil
}

func seedRelease(t *testing.T, provider *memoryOSS, version string) *bootstrap.ReleaseManifest {
	t.Helper()
	artifacts := map[string][]byte{
		"bin/servercli":        []byte("servercli-binary"),
		"modules/docker.tgz":   []byte("docker-module"),
		"templates/caddy.tmpl": []byte("caddy-template"),
	}
	manifest := &bootstrap.ReleaseManifest{
		SchemaVersion:  "1",
		ReleaseVersion: version,
		CreatedAt:      time.Now().UTC(),
		Signature:      "publication-signing-disabled",
	}
	prefix := releasePrefix(version)
	for path, data := range artifacts {
		manifest.Artifacts = append(manifest.Artifacts, bootstrap.Artifact{
			Path: path, Kind: "test", SHA256: oss.SHA256Hex(data), Size: int64(len(data)),
		})
		provider.objects[prefix+"/"+path] = append([]byte(nil), data...)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	provider.objects[prefix+"/"+bundle.ReleaseManifestName] = raw
	return manifest
}

func newTestBootstrap(t *testing.T, provider *memoryOSS, installer func(context.Context, *bootstrap.ReleaseManifest, string) error, runner func(context.Context, FoundationRun) error) (*Bootstrap, string) {
	t.Helper()
	dir := t.TempDir()
	envPath := filepath.Join(dir, "bootstrap.env")
	if err := os.WriteFile(envPath, []byte(validEnvText()), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "state.json")
	return New(BootstrapOptions{
		EnvPath:          envPath,
		OSS:              provider,
		Installer:        installer,
		FoundationRunner: runner,
		StatePath:        statePath,
		Timeout:          5 * time.Second,
	}), statePath
}

func TestApplyWalksAllPhasesAndRecordsCommitPoints(t *testing.T) {
	provider := newMemoryOSS()
	seedRelease(t, provider, "1.2.3")
	var calls []string
	b, statePath := newTestBootstrap(t, provider,
		func(_ context.Context, manifest *bootstrap.ReleaseManifest, artifactDir string) error {
			calls = append(calls, "installer")
			if manifest.ReleaseVersion != "1.2.3" {
				t.Fatalf("installer manifest version = %q", manifest.ReleaseVersion)
			}
			if _, err := os.Stat(filepath.Join(artifactDir, "bin/servercli")); err != nil {
				t.Fatalf("installer artifact missing: %v", err)
			}
			return nil
		},
		func(_ context.Context, run FoundationRun) error {
			calls = append(calls, run.ModuleID+":"+run.Operation)
			return nil
		},
	)
	if err := b.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{"installer"}
	for _, moduleID := range foundationModules {
		wantCalls = append(wantCalls, moduleID+":install")
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}

	status, err := b.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateReady || status.Overall != initstate.StateReady {
		t.Fatalf("status = %+v", status)
	}
	for _, phase := range bootstrapPhases {
		if status.CommitPoints[phaseStepID(phase)] == "" {
			t.Errorf("missing commit point for phase %s", phase)
		}
	}
	for _, moduleID := range foundationModules {
		if status.CommitPoints[moduleStepID(moduleID, "install")] == "" {
			t.Errorf("missing module commit point for %s", moduleID)
		}
	}
	persisted, err := initstate.OpenReadOnly(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Overall != initstate.StateReady {
		t.Fatalf("persisted overall = %q", persisted.Overall)
	}
}

func TestResumeSkipsRecordedPhasesAndModuleSideEffects(t *testing.T) {
	provider := newMemoryOSS()
	seedRelease(t, provider, "1.2.3")
	firstCalls := []string{}
	failOnce := true
	b, statePath := newTestBootstrap(t, provider,
		func(context.Context, *bootstrap.ReleaseManifest, string) error {
			firstCalls = append(firstCalls, "installer")
			return nil
		},
		func(_ context.Context, run FoundationRun) error {
			firstCalls = append(firstCalls, run.ModuleID)
			if run.ModuleID == "postgres" && failOnce {
				failOnce = false
				return errors.New("scripted module failure")
			}
			return nil
		},
	)
	if err := b.Apply(context.Background()); err == nil {
		t.Fatal("expected initial failure")
	}
	if !reflect.DeepEqual(firstCalls, []string{"installer", "docker", "postgres"}) {
		t.Fatalf("initial calls = %v", firstCalls)
	}

	var resumed []string
	b2 := New(BootstrapOptions{
		EnvPath:   b.EnvPath,
		OSS:       provider,
		StatePath: statePath,
		Installer: func(context.Context, *bootstrap.ReleaseManifest, string) error {
			t.Fatal("resume re-ran completed installer")
			return nil
		},
		FoundationRunner: func(_ context.Context, run FoundationRun) error {
			resumed = append(resumed, run.ModuleID)
			return nil
		},
	})
	if err := b2.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"postgres", "caddy", "control-plane", "agent", "v2ray", "gitea"}
	if !reflect.DeepEqual(resumed, want) {
		t.Fatalf("resumed calls = %v, want %v", resumed, want)
	}
	status, err := b2.Status(context.Background())
	if err != nil || status.State != StateReady {
		t.Fatalf("resume status = %+v, err = %v", status, err)
	}
}

func TestFoundationFailureMovesStateToFailedAndRecordsErrorType(t *testing.T) {
	provider := newMemoryOSS()
	seedRelease(t, provider, "1.2.3")
	b, _ := newTestBootstrap(t, provider,
		func(context.Context, *bootstrap.ReleaseManifest, string) error { return nil },
		func(_ context.Context, run FoundationRun) error {
			if run.ModuleID == "caddy" {
				return errors.New("caddy failed")
			}
			return nil
		},
	)
	if err := b.Apply(context.Background()); err == nil {
		t.Fatal("expected Apply failure")
	}
	status, err := b.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateFailed || status.Overall != initstate.StateFailed {
		t.Fatalf("status = %+v", status)
	}
	moduleStep := findStep(status.Steps, moduleStepID("caddy", "install"))
	if moduleStep == nil || moduleStep.Status != initstate.StepFailed || moduleStep.ErrorType != initstate.ErrTypeModule {
		t.Fatalf("caddy step = %+v", moduleStep)
	}
	phaseStep := findStep(status.Steps, phaseStepID(StateFoundationApplying))
	if phaseStep == nil || phaseStep.ErrorType != initstate.ErrTypeModule {
		t.Fatalf("foundation phase step = %+v", phaseStep)
	}
}

func TestStateFileContainsNoOSSSecrets(t *testing.T) {
	provider := newMemoryOSS()
	seedRelease(t, provider, "1.2.3")
	b, statePath := newTestBootstrap(t, provider,
		func(context.Context, *bootstrap.ReleaseManifest, string) error { return nil },
		func(context.Context, FoundationRun) error { return nil },
	)
	if err := b.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{statePath, statePath + ".sha256"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), sampleSecret) || strings.Contains(string(raw), "ak-id") {
			t.Fatalf("state file %s contains OSS credential", path)
		}
	}
}

func TestPlanHasNoStateSideEffects(t *testing.T) {
	provider := newMemoryOSS()
	seedRelease(t, provider, "1.2.3")
	b, statePath := newTestBootstrap(t, provider,
		func(context.Context, *bootstrap.ReleaseManifest, string) error { return nil },
		func(context.Context, FoundationRun) error { return nil },
	)
	plan, err := b.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != "1.2.3" || plan.RequiresGitHubToken || len(plan.Phases) != len(bootstrapPhases) {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Plan wrote state: %v", err)
	}
}

func findStep(steps []initstate.Step, id string) *initstate.Step {
	for i := range steps {
		if steps[i].ModuleID == id {
			return &steps[i]
		}
	}
	return nil
}
