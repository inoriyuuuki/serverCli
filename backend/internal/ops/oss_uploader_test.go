package ops

import (
	"context"
	"errors"
	"testing"

	"servercli/internal/oss"
)

// fakeProvider records keys and returns a controllable error.
type fakeProvider struct {
	keys []string
	err  error
}

func (f *fakeProvider) Put(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	f.keys = append(f.keys, key)
	return "etag", f.err
}
func (f *fakeProvider) Get(ctx context.Context, key string) ([]byte, error) { return nil, f.err }
func (f *fakeProvider) Head(ctx context.Context, key string) (*oss.ObjectMeta, error) { return nil, f.err }
func (f *fakeProvider) Exists(ctx context.Context, key string) (bool, error) { return false, f.err }
func (f *fakeProvider) Delete(ctx context.Context, key string) error { return f.err }
func (f *fakeProvider) PutVerified(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	f.keys = append(f.keys, key)
	return "etag", f.err
}
func (f *fakeProvider) GetVerified(ctx context.Context, key string, expectedSHA256 string) ([]byte, error) {
	return nil, f.err
}

func TestOSSUploaderNoDoublePrefix(t *testing.T) {
	fp := &fakeProvider{}
	u := OSSUploader{Provider: fp, BaseKey: "servercli/backups"}
	ctx := context.Background()

	// backup.go passes a fully-qualified key already
	if err := u.Upload(ctx, "servercli/backups/bak-1/manifest.json", []byte("{}")); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if len(fp.keys) != 1 || fp.keys[0] != "servercli/backups/bak-1/manifest.json" {
		t.Fatalf("double prefix bug: keys = %v", fp.keys)
	}
}

func TestOSSUploaderPrefixesRelativePath(t *testing.T) {
	fp := &fakeProvider{}
	u := OSSUploader{Provider: fp, BaseKey: "servercli/backups"}
	if err := u.Upload(context.Background(), "bak-1/manifest.json", []byte("{}")); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if len(fp.keys) != 1 || fp.keys[0] != "servercli/backups/bak-1/manifest.json" {
		t.Fatalf("expected prefixed key, got %v", fp.keys)
	}
}

func TestOSSUploaderNilProvider(t *testing.T) {
	u := OSSUploader{BaseKey: "servercli/backups"}
	if err := u.Upload(context.Background(), "servercli/backups/x", nil); !errors.Is(err, ErrNoUploader) {
		t.Fatalf("expected ErrNoUploader, got %v", err)
	}
}

func TestFailingUploaderAlwaysFails(t *testing.T) {
	u := FailingUploader{}
	err := u.Upload(context.Background(), "servercli/backups/bak-1/manifest.json", []byte("{}"))
	if err == nil {
		t.Fatal("expected FailingUploader.Upload to return an error")
	}
	if got := err.Error(); got != "ops: OSS uploader misconfigured" {
		t.Fatalf("error = %q, want %q", got, "ops: OSS uploader misconfigured")
	}
}
