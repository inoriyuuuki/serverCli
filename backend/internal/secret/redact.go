// Package secret provides redaction utilities that must be applied before
// secrets are persisted or logged. It tracks how many values were redacted.
package secret

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"sync/atomic"
)

// Redactor masks secret values and counts redactions.
type Redactor struct {
	count atomic.Int64
}

// NewRedactor returns a Redactor with a zero redaction count.
func NewRedactor() *Redactor { return &Redactor{} }

// Count returns the number of values redacted so far.
func (r *Redactor) Count() int64 { return r.count.Load() }

// sensitiveJSONKeys are matched case-insensitively and partially (prefix).
var sensitiveJSONKeys = []string{
	"password", "passwd", "pwd", "secret", "token", "api_key", "apikey",
	"authorization", "cookie", "set-cookie", "private_key", "credential",
	"node_credential", "renewal_token", "claim_token", "access_key",
	"access_token", "refresh_token", "client_secret", "db_password",
	"dsn", "connection_string",
	"webhook", "hook_url", "hook",
}

// IsSensitiveKey reports whether k looks like a secret-bearing key.
func IsSensitiveKey(k string) bool {
	lower := strings.ToLower(k)
	for _, s := range sensitiveJSONKeys {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

var (
	privateKeyRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	authHeaderRe = regexp.MustCompile(`(?i)(Bearer|Basic)\s+\S+`)
	cookieRe     = regexp.MustCompile(`(?i)(session|token|auth|credential)[^=;,\s]*=[^;,\s]+`)
	dsnRe        = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^:/\s]+):([^@/\s]+)@`)
	hookURLRe    = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
	// commonSecretValueRe matches typical high-entropy secret values inside JSON.
	commonSecretValueRe = regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]+|ghp_[A-Za-z0-9]+|AKIA[0-9A-Z]{16}|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}|sct_[A-Za-z0-9_-]+|lrt_[A-Za-z0-9_.-]+)`)
)

// MaskSecret replaces the given value with a safe placeholder, counting it.
func (r *Redactor) MaskSecret(v string) string {
	if v == "" {
		return v
	}
	r.count.Add(1)
	return "[REDACTED]"
}

// RedactString applies all string-level redactions to s.
func (r *Redactor) RedactString(s string) string {
	changed := false
	out := privateKeyRe.ReplaceAllStringFunc(s, func(m string) string {
		changed = true
		return "[REDACTED_PRIVATE_KEY]"
	})
	if lower := strings.ToLower(out); strings.Contains(lower, "authorization") {
		auth := authHeaderRe.ReplaceAllStringFunc(out, func(m string) string {
			changed = true
			return "[REDACTED_AUTH]"
		})
		out = auth
	}
	if lower := strings.ToLower(out); strings.Contains(lower, "cookie") {
		ck := cookieRe.ReplaceAllStringFunc(out, func(m string) string {
			changed = true
			return "[REDACTED_COOKIE]"
		})
		out = ck
	}
	if dsn := dsnRe.ReplaceAllStringFunc(out, func(m string) string {
		changed = true
		return dsnRe.ReplaceAllString(m, "${1}${2}:[REDACTED]@")
	}); dsn != out {
		out = dsn
	}
	// Common cloud key formats.
	if m := commonSecretValueRe.ReplaceAllStringFunc(out, func(m string) string {
		changed = true
		return "[REDACTED_KEY]"
	}); m != out {
		out = m
	}
	// Hook/webhook style URLs (e.g. Feishu bot webhook endpoints) carry
	// secret tokens in the path; mask the whole URL. Runs after dsnRe so
	// credential-bearing DSNs keep their dedicated redaction first.
	if strings.Contains(strings.ToLower(out), "hook") {
		urls := hookURLRe.ReplaceAllStringFunc(out, func(m string) string {
			if isHookURL(m) {
				changed = true
				return "[REDACTED_URL]"
			}
			return m
		})
		out = urls
	}
	if changed {
		r.count.Add(1)
	}
	return out
}

// isHookURL reports whether a matched URL looks like a hook/webhook endpoint.
func isHookURL(u string) bool {
	lower := strings.ToLower(u)
	return strings.Contains(lower, "webhook") || strings.Contains(lower, "/hook")
}

// RedactJSON masks sensitive keys in a JSON document while preserving shape.
// It returns the redacted document; if input is not valid JSON it is returned
// unchanged.
func (r *Redactor) RedactJSON(data []byte) []byte {
	var v any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return data
	}
	nv := r.redactValue(v)
	out, err := json.Marshal(nv)
	if err != nil {
		return data
	}
	return out
}

// redactValue recursively masks map keys considered sensitive.
func (r *Redactor) redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if IsSensitiveKey(k) {
				if str, ok := val.(string); ok {
					out[k] = r.MaskSecret(str)
					continue
				}
				out[k] = "[REDACTED]"
				r.count.Add(1)
				continue
			}
			out[k] = r.redactValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = r.redactValue(item)
		}
		return out
	default:
		return v
	}
}
