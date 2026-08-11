package ops

import (
	"context"
	"strings"

	"servercli/internal/oss"
)

// OSSUploader implements ops.Uploader with a real OSS Provider. Every upload
// is verified: PutVerified uploads, read-backs and checks SHA256 before
// returning success, so a backup is only reported complete after the remote
// copy is proven present and correct.
type OSSUploader struct {
	Provider oss.Provider
	// BaseKey is the object key prefix (e.g. "servercli/backups"). The ops
	// backup code already passes fully-qualified keys that begin with this
	// prefix, so Upload never double-prefixes: when path already starts with
	// BaseKey the path is used verbatim.
	BaseKey string
}

// Upload uploads data to the object key with read-back verification.
func (u OSSUploader) Upload(ctx context.Context, path string, data []byte) error {
	if u.Provider == nil {
		return ErrNoUploader
	}
	key := path
	if u.BaseKey != "" && !strings.HasPrefix(path, u.BaseKey+"/") {
		key = strings.TrimSuffix(u.BaseKey, "/") + "/" + path
	}
	_, err := u.Provider.PutVerified(ctx, key, data, "application/octet-stream")
	return err
}

// FailingUploader is returned when OSS is configured but the provider cannot
// be initialized (e.g. a bad endpoint). Every upload fails so a backup is
// never silently reported as verified when nothing was actually uploaded.
type FailingUploader struct{}

// Upload always returns an error, surfacing the uploader misconfiguration to
// the caller instead of pretending the backup reached remote storage.
func (FailingUploader) Upload(ctx context.Context, path string, data []byte) error {
	_ = ctx
	_ = path
	_ = data
	return NewUploaderError("ops: OSS uploader misconfigured")
}

// ErrNoUploader is returned when an OSS uploader has no provider.
var ErrNoUploader = NewUploaderError("no OSS provider configured")

// NewUploaderError builds a sentinel error for uploader misconfiguration.
func NewUploaderError(msg string) error { return &uploaderError{msg: msg} }

type uploaderError struct{ msg string }

func (e *uploaderError) Error() string { return e.msg }
