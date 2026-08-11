package releasecache

import (
	"bytes"
	"reflect"
	"testing"
)

func TestManifestBuildSerializeParseRoundTrip(t *testing.T) {
	artifacts := []CacheArtifact{
		{Name: "servercli-linux-amd64.tar.gz", Size: 12, SHA256: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"},
		{Name: "deploy-install-servercli.sh", Size: 7, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	manifest := BuildManifest("v1.2.3", "example/servercli", "v1.2.3", "linux", "amd64", "modules-v4", artifacts, SchemaCompatInfo{Min: "1", Max: "4"})
	data, err := Serialize(manifest)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest.Artifacts[0].Name, "deploy-install-servercli.sh") {
		t.Fatalf("BuildManifest did not normalize artifact order: %#v", manifest.Artifacts)
	}
	if parsed.Version != manifest.Version || parsed.SourceRepository != manifest.SourceRepository || parsed.ModulesVersion != manifest.ModulesVersion {
		t.Fatalf("round trip changed manifest: %#v", parsed)
	}
	if !reflect.DeepEqual(parsed.Artifacts, manifest.Artifacts) {
		t.Fatalf("round trip changed artifacts:\nwant %#v\n got %#v", manifest.Artifacts, parsed.Artifacts)
	}
}

func TestManifestCanonicalJSONStable(t *testing.T) {
	manifest := BuildManifest("v2", "o/r", "v2", "linux", "arm64", "", []CacheArtifact{
		{Name: "z", Size: 1, SHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
		{Name: "a", Size: 2, SHA256: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},
	}, SchemaCompatInfo{})
	first, err := Serialize(manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Serialize(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("canonical serialization is unstable:\n%s\n%s", first, second)
	}
	parsed, err := Parse(first)
	if err != nil {
		t.Fatal(err)
	}
	third, err := Serialize(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, third) {
		t.Fatalf("parse/serialize changed canonical JSON:\n%s\n%s", first, third)
	}
	if bytes.Contains(first, []byte("\n")) {
		t.Fatalf("canonical JSON must be compact: %q", first)
	}
	if !bytes.HasPrefix(first, []byte(`{"arch":`)) {
		t.Fatalf("canonical JSON keys are not sorted: %s", first)
	}
}

func TestParseRejectsTrailingJSON(t *testing.T) {
	manifest := BuildManifest("v1", "o/r", "v1", "", "", "", []CacheArtifact{
		{Name: "a", Size: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}, SchemaCompatInfo{})
	data, err := Serialize(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(append(data, []byte(` {}`)...)); err == nil {
		t.Fatal("Parse accepted multiple JSON values")
	}
}
