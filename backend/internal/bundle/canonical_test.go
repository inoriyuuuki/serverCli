package bundle

import (
	"strings"
	"testing"
	"time"

	"servercli/internal/bootstrap"
)

// TestCanonicalManifestBytesMatchesJQ locks the canonical form to the exact
// bytes produced by `jq -cS 'del(.signature)'` (and Python
// `json.dumps(sort_keys=True)`), so Go, CI and the installer verify the same
// message. The expected value below was generated with jq 1.6 from the Go
// struct-field JSON of the same manifest.
func TestCanonicalManifestBytesMatchesJQ(t *testing.T) {
	m := bootstrap.ReleaseManifest{
		SchemaVersion:  "1.0",
		ReleaseVersion: "v0.1.0-test",
		CreatedAt:      time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		SigningKeyID:   "k1",
		Artifacts: []bootstrap.Artifact{
			{Path: "bin/servercli", Kind: "binary", SHA256: "ab", Size: 1},
		},
		SchemaCompat: bootstrap.SchemaCompat{MinSchemaVersion: "1", MaxSchemaVersion: "2", Reversible: true},
	}
	want := `{"artifacts":[{"kind":"binary","path":"bin/servercli","sha256":"ab","size":1}],"created_at":"2026-08-10T00:00:00Z","release_version":"v0.1.0-test","schema_compat":{"max_schema_version":"2","min_schema_version":"1","reversible":true},"schema_version":"1.0","signing_key_id":"k1"}`
	got, err := CanonicalManifestBytes(&m)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("canonical mismatch\n got: %s\nwant: %s", got, want)
	}
	// Signature key must never appear in the canonical bytes.
	if strings.Contains(string(got), "signature") {
		t.Fatalf("canonical contains signature key: %s", got)
	}
}

func TestCanonicalManifestBytesDeterministic(t *testing.T) {
	m := bootstrap.BundleManifest{
		SchemaVersion: "1.0", BundleID: "b1", BundleVersion: "1.0",
		Environment: "prod", TargetNode: "n1", TargetRole: "primary",
		CreatedAt: time.Now().UTC(), PayloadDigest: "abc",
	}
	a, err := CanonicalManifestBytes(&m)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := CanonicalManifestBytes(&m)
	if string(a) != string(b) {
		t.Fatalf("canonical not deterministic")
	}
}
