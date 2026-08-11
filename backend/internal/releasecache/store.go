package releasecache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"servercli/internal/model"
)

// EntryCreator is the narrow subset of store.Store used by RecordEntries.
type EntryCreator interface {
	CreateReleaseCacheEntry(ctx context.Context, entry *model.ReleaseCacheEntry) error
}

// VersionStatusStore is the narrow subset used to mark all rows for a version.
type VersionStatusStore interface {
	ListReleaseCacheEntries(ctx context.Context, version, status string) ([]*model.ReleaseCacheEntry, error)
	UpdateReleaseCacheEntryStatus(ctx context.Context, id, status string, uploadedAt, verifiedAt *time.Time) error
}

// RecordEntries persists one available release-cache row per mirrored artifact.
func RecordEntries(ctx context.Context, store EntryCreator, result *SyncResult, schema SchemaCompatInfo) ([]*model.ReleaseCacheEntry, error) {
	if store == nil {
		return nil, errors.New("releasecache: store is required")
	}
	if result == nil || !result.Verified {
		return nil, errors.New("releasecache: a verified sync result is required")
	}
	uploadedAt := result.UploadedAt
	if uploadedAt.IsZero() {
		uploadedAt = time.Now().UTC()
	}
	verifiedAt := result.VerifiedAt
	if verifiedAt.IsZero() {
		verifiedAt = uploadedAt
	}

	entries := make([]*model.ReleaseCacheEntry, 0, len(result.Uploaded))
	for _, artifact := range result.Uploaded {
		entry := &model.ReleaseCacheEntry{
			ID: model.NewUUID(), Version: result.Version,
			SourceRepository: result.SourceRepository, SourceRelease: result.SourceRelease,
			OS: result.OS, Arch: result.Arch, ArtifactName: artifact.Name,
			ArtifactSize: artifact.Size, SHA256: artifact.SHA256,
			ModulesVersion: result.ModulesVersion, SchemaMin: schema.Min, SchemaMax: schema.Max,
			OSSKey: artifact.OSSKey, Status: model.ReleaseCacheAvailable,
			UploadedAt: &uploadedAt, VerifiedAt: &verifiedAt,
		}
		if err := store.CreateReleaseCacheEntry(ctx, entry); err != nil {
			return entries, fmt.Errorf("releasecache: record artifact %q: %w", artifact.Name, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// MarkVersionAvailable marks every cache entry for version as uploaded and verified.
func MarkVersionAvailable(ctx context.Context, store VersionStatusStore, version string) error {
	if store == nil {
		return errors.New("releasecache: store is required")
	}
	if version == "" {
		return errors.New("releasecache: version is required")
	}
	entries, err := store.ListReleaseCacheEntries(ctx, version, "")
	if err != nil {
		return fmt.Errorf("releasecache: list version entries: %w", err)
	}
	now := time.Now().UTC()
	for _, entry := range entries {
		if err := store.UpdateReleaseCacheEntryStatus(ctx, entry.ID, model.ReleaseCacheAvailable, &now, &now); err != nil {
			return fmt.Errorf("releasecache: mark entry %q available: %w", entry.ID, err)
		}
	}
	return nil
}
