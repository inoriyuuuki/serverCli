package bundle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"servercli/internal/bootstrap"
)

func TestFetchAndVerifyReleasePrimaryGitHub(t *testing.T) {
	priv, pubPEM := testEd25519Key(t)
	art := bootstrap.Artifact{Path: "servercli", Kind: "binary", SHA256: strings.Repeat("ab", 32), Size: 10}
	rm := testReleaseManifest("1.2.3", art)

	githubDir := t.TempDir()
	writeReleaseManifest(t, githubDir, priv, rm)
	ossDir := t.TempDir() // intentionally empty

	got, src, err := FetchAndVerifyRelease(context.Background(), fileURL(githubDir), fileURL(ossDir), pubPEM, nil)
	if err != nil {
		t.Fatalf("FetchAndVerifyRelease: %v", err)
	}
	if src != SourceGitHub {
		t.Fatalf("source = %q, want %q", src, SourceGitHub)
	}
	if got.ReleaseVersion != "1.2.3" {
		t.Fatalf("release_version = %q, want 1.2.3", got.ReleaseVersion)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].SHA256 != art.SHA256 {
		t.Fatalf("artifacts not preserved: %+v", got.Artifacts)
	}
}

func TestFetchAndVerifyReleaseFallsBackToOSS(t *testing.T) {
	priv, pubPEM := testEd25519Key(t)
	githubDir := t.TempDir()
	ossDir := t.TempDir()

	// Valid manifest on OSS only.
	rm := testReleaseManifest("2.0.0")
	writeReleaseManifest(t, ossDir, priv, rm)

	// Tampered manifest on GitHub: signature no longer valid.
	tampered := testReleaseManifest("9.9.9")
	tampered.Signature = "AAAA"
	raw, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(githubDir, ReleaseManifestName), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	got, src, err := FetchAndVerifyRelease(context.Background(), fileURL(githubDir), fileURL(ossDir), pubPEM, nil)
	if err != nil {
		t.Fatalf("FetchAndVerifyRelease: %v", err)
	}
	if src != SourceOSS {
		t.Fatalf("source = %q, want %q", src, SourceOSS)
	}
	if got.ReleaseVersion != "2.0.0" {
		t.Fatalf("release_version = %q, want 2.0.0", got.ReleaseVersion)
	}
}

func TestFetchAndVerifyReleaseBothSourcesFail(t *testing.T) {
	_, pubPEM := testEd25519Key(t)
	githubDir := t.TempDir()
	ossDir := t.TempDir()
	// Corrupt both manifests so neither source verifies.
	os.WriteFile(filepath.Join(githubDir, ReleaseManifestName), []byte("{not json"), 0o600)
	os.WriteFile(filepath.Join(ossDir, ReleaseManifestName), []byte("{not json"), 0o600)

	if _, _, err := FetchAndVerifyRelease(context.Background(), fileURL(githubDir), fileURL(ossDir), pubPEM, nil); err == nil {
		t.Fatal("expected error when both sources fail verification")
	}
}

func TestFetchAndVerifyReleaseTamperedSignatureRejected(t *testing.T) {
	priv, pubPEM := testEd25519Key(t)
	dir := t.TempDir()
	rm := testReleaseManifest("1.0.0")
	writeReleaseManifest(t, dir, priv, rm)
	// Flip a byte in the artifact digest inside the signed manifest.
	rm.Artifacts = append(rm.Artifacts, bootstrap.Artifact{Path: "x", Kind: "binary", SHA256: strings.Repeat("cd", 32)})
	raw, _ := json.Marshal(rm)
	os.WriteFile(filepath.Join(dir, ReleaseManifestName), raw, 0o600)

	if _, _, err := FetchAndVerifyRelease(context.Background(), fileURL(dir), fileURL(t.TempDir()), pubPEM, nil); err == nil {
		t.Fatal("expected signature verification failure on tampered manifest")
	}
}

func TestDownloadArtifactVerifiesSHA256(t *testing.T) {
	dir := t.TempDir()
	content := []byte("#!/bin/sh\necho servercli\n")
	if err := os.WriteFile(filepath.Join(dir, "servercli"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	art := bootstrap.Artifact{Path: "servercli", Kind: "binary", SHA256: sha256Hex(content)}

	got, err := DownloadArtifact(context.Background(), fileURL(dir), art, 10*time.Second)
	if err != nil {
		t.Fatalf("DownloadArtifact: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("downloaded content mismatch")
	}

	// Tamper the file: digest no longer matches the manifest.
	if err := os.WriteFile(filepath.Join(dir, "servercli"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DownloadArtifact(context.Background(), fileURL(dir), art, 10*time.Second); err == nil {
		t.Fatal("expected sha256 mismatch error for tampered artifact")
	}
}

func TestDownloadArtifactMissing(t *testing.T) {
	art := bootstrap.Artifact{Path: "nope", Kind: "binary", SHA256: strings.Repeat("ef", 32)}
	if _, err := DownloadArtifact(context.Background(), fileURL(t.TempDir()), art, time.Second); err == nil {
		t.Fatal("expected error for missing artifact")
	}
}

func TestCanonicalManifestBytesStable(t *testing.T) {
	rm := testReleaseManifest("1.0.0")
	rm.Signature = "should-be-blanked"
	canon, err := CanonicalManifestBytes(rm)
	if err != nil {
		t.Fatal(err)
	}
	asMap := map[string]any{}
	if err := json.Unmarshal(canon, &asMap); err != nil {
		t.Fatal(err)
	}
	if _, ok := asMap["signature"]; ok {
		t.Fatalf("signature field must be absent from canonical bytes (got %v)", asMap["signature"])
	}
	// Same canonical bytes whether Signature is set or not.
	rm2 := testReleaseManifest("1.0.0")
	canon2, _ := CanonicalManifestBytes(rm2)
	if string(canon) != string(canon2) {
		t.Fatalf("canonical bytes differ with signature set/blank:\n%s\n%s", canon, canon2)
	}
	// Pointer and value forms agree.
	canon3, _ := CanonicalManifestBytes(*rm2)
	if string(canon2) != string(canon3) {
		t.Fatal("pointer and value canonical bytes differ")
	}
}
