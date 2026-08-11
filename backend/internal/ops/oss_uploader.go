package ops

import (
	"context"

	"servercli/internal/oss"
)

// OSSUploader implements ops.Uploader with a real OSS Provider. Every upload
// is verified: PutVerified uploads, read-backs and checks SHA256 before
// returning success, so a backup is only reported complete after the remote
// copy is proven present and correct.
type OSSUploader struct {
	Provider oss.Provider
	// BaseKey is the object key prefix (e.g. "servercli/backups"). The ops
	// caller passes the full relative path as the second argument.
	BaseKey string
}

// Upload uploads data to BaseKey + "/" + path with read-back verification.
func (u OSSUploader) Upload(ctx context.Context, path string, data []byte) error {
	if u.Provider == nil {
		return ErrNoUploader
	}
	key := path
	if u.BaseKey != "" {
		key = u.BaseKey + "/" + path
	}
	_, err := u.Provider.PutVerified(ctx, key, data, "application/octet-stream")
	return err
}

// ErrNoUploader is returned when an OSS uploader has no provider.
var ErrNoUploader = NewUploaderError("no OSS provider configured")

// NewUploaderError builds a sentinel error for uploader misconfiguration.
func NewUploaderError(msg string) error { return &uploaderError{msg: msg} }

type uploaderError struct{ msg string }

func (e *uploaderError) Error() string { return e.msg }
