package secretprovider

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// Reserved codec behavior errors.
var (
	// ErrNotImplemented is returned by reserved future codec stubs whose
	// operations are not implemented yet.
	ErrNotImplemented = errors.New("secretprovider: codec not implemented")
	// ErrUnsupportedMode is returned when no codec is registered for a mode.
	ErrUnsupportedMode = errors.New("secretprovider: unsupported encryption mode")
)

// AESGCMSecretCodec is the reserved stub for AES-GCM encrypted secrets. Only
// Mode is implemented; all other operations return ErrNotImplemented.
type AESGCMSecretCodec struct{}

// Mode returns "aes-gcm".
func (c *AESGCMSecretCodec) Mode() string { return ModeAESGCM }

// Decode is not implemented yet.
func (c *AESGCMSecretCodec) Decode(ctx context.Context, in []byte, meta SecretMetadata) ([]byte, error) {
	return nil, ErrNotImplemented
}

// Encode is not implemented yet.
func (c *AESGCMSecretCodec) Encode(ctx context.Context, in []byte, meta SecretMetadata) ([]byte, error) {
	return nil, ErrNotImplemented
}

// Validate is not implemented yet.
func (c *AESGCMSecretCodec) Validate(ctx context.Context, raw []byte, meta SecretMetadata) error {
	return ErrNotImplemented
}

// KMSEnvelopeSecretCodec is the reserved stub for KMS envelope encrypted
// secrets. Only Mode is implemented; all other operations return
// ErrNotImplemented.
type KMSEnvelopeSecretCodec struct{}

// Mode returns "kms-envelope".
func (c *KMSEnvelopeSecretCodec) Mode() string { return ModeKMSEnvelope }

// Decode is not implemented yet.
func (c *KMSEnvelopeSecretCodec) Decode(ctx context.Context, in []byte, meta SecretMetadata) ([]byte, error) {
	return nil, ErrNotImplemented
}

// Encode is not implemented yet.
func (c *KMSEnvelopeSecretCodec) Encode(ctx context.Context, in []byte, meta SecretMetadata) ([]byte, error) {
	return nil, ErrNotImplemented
}

// Validate is not implemented yet.
func (c *KMSEnvelopeSecretCodec) Validate(ctx context.Context, raw []byte, meta SecretMetadata) error {
	return ErrNotImplemented
}

// Registry maps encryption modes to codecs.
type Registry struct {
	codecs map[string]RepositorySecretCodec
}

// NewRegistry returns a Registry preloaded with plaintextCodec under mode
// "none". The aes-gcm and kms-envelope slots are reserved: Get returns
// ErrNotImplemented for them (not ErrUnsupportedMode) so future
// implementations can be dropped in without touching callers.
func NewRegistry(plaintextCodec RepositorySecretCodec) *Registry {
	r := &Registry{codecs: make(map[string]RepositorySecretCodec)}
	if plaintextCodec != nil && plaintextCodec.Mode() != "" {
		r.codecs[plaintextCodec.Mode()] = plaintextCodec
	}
	return r
}

// Get returns the codec registered for mode. Reserved future modes yield
// ErrNotImplemented; unknown modes yield ErrUnsupportedMode.
func (r *Registry) Get(mode string) (RepositorySecretCodec, error) {
	if c, ok := r.codecs[mode]; ok {
		return c, nil
	}
	switch mode {
	case ModeAESGCM, ModeKMSEnvelope:
		return nil, ErrNotImplemented
	default:
		return nil, ErrUnsupportedMode
	}
}

// Register installs codec under its Mode(), replacing any previous one.
func (r *Registry) Register(codec RepositorySecretCodec) error {
	if codec == nil {
		return fmt.Errorf("cannot register nil codec")
	}
	if codec.Mode() == "" {
		return fmt.Errorf("cannot register codec with empty mode")
	}
	r.codecs[codec.Mode()] = codec
	return nil
}

// Modes returns the currently registered modes, sorted for determinism.
func (r *Registry) Modes() []string {
	modes := make([]string, 0, len(r.codecs))
	for m := range r.codecs {
		modes = append(modes, m)
	}
	sort.Strings(modes)
	return modes
}

var (
	_ RepositorySecretCodec = (*AESGCMSecretCodec)(nil)
	_ RepositorySecretCodec = (*KMSEnvelopeSecretCodec)(nil)
)
