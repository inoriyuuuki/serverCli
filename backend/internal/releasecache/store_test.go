package releasecache

import (
	"context"
	"testing"
	"time"

	"servercli/internal/model"
)

type fakeEntryStore struct {
	entries []*model.ReleaseCacheEntry
	updates []string
}

func (store *fakeEntryStore) CreateReleaseCacheEntry(_ context.Context, entry *model.ReleaseCacheEntry) error {
	store.entries = append(store.entries, entry)
	return nil
}

func (store *fakeEntryStore) ListReleaseCacheEntries(context.Context, string, string) ([]*model.ReleaseCacheEntry, error) {
	return store.entries, nil
}

func (store *fakeEntryStore) UpdateReleaseCacheEntryStatus(_ context.Context, id, status string, _, _ *time.Time) error {
	store.updates = append(store.updates, id+":"+status)
	return nil
}

func TestRecordEntriesAndMarkVersionAvailable(t *testing.T) {
	store := &fakeEntryStore{}
	result := &SyncResult{
		Version: "v1", Verified: true, SourceRepository: "o/r", SourceRelease: "v1",
		OS: "linux", Arch: "amd64", ModulesVersion: "m1",
		Uploaded: []ArtifactUploaded{{Name: "a.tar.gz", OSSKey: "servercli/releases/v1/a.tar.gz", Size: 3, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
	}
	entries, err := RecordEntries(context.Background(), store, result, SchemaCompatInfo{Min: "1", Max: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Status != model.ReleaseCacheAvailable || entries[0].SchemaMin != "1" || entries[0].UploadedAt == nil {
		t.Fatalf("unexpected recorded entries: %#v", entries)
	}
	if err := MarkVersionAvailable(context.Background(), store, "v1"); err != nil {
		t.Fatal(err)
	}
	if len(store.updates) != 1 || store.updates[0] != entries[0].ID+":"+model.ReleaseCacheAvailable {
		t.Fatalf("unexpected status updates: %#v", store.updates)
	}
}
