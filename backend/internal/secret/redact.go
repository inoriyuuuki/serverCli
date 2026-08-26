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
	// aliyunAKIDRe matches Alibaba Cloud AccessKey IDs (LTAI prefix), e.g.
	// LTAI5t8x... which appear in OSS/TTS/OSS client configurations. The value
	// itself is a credential and must never be persisted or logged.
	aliyunAKIDRe = regexp.MustCompile(`LTAI[a-zA-Z0-9]{12,24}`)
	// aliyunAKSecretRe matches AccessKey Secret values written as an explicit
	// key=value / key: value pair (accessKeySecret=..., access_key_secret: ...,
	// ak_secret=..., aliyun_secret: ...). Restricting to key=value avoids
	// false positives on ordinary prose.
	aliyunAKSecretRe = regexp.MustCompile(`(?i)(access[_-]?key[_-]?secret|access[_-]?key[_-]?id|ak[_-]?secret|aliyun[_-]?secret)\s*[:=]\s*['"]?[A-Za-z0-9+/=]{16,64}`)
	// httpBasicCredRe matches user:password embedded directly in an HTTP(S)
	// URL (e.g. http://user:pass@host). Both parts are masked so an AccessKey
	// used as the username is not leaked either. It runs after dsnRe (which
	// only masks the password), so the username is masked as well.
	httpBasicCredRe = regexp.MustCompile(`(?i)(https?://)[^/@\s]+:[^/@\s]+@`)
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
	// HTTP(S) URLs with embedded user:password (e.g. Git clone over HTTP)
	// must have BOTH parts masked. Runs after dsnRe (which only masks the
	// password) so an AccessKey used as the username is masked too.
	if strings.Contains(strings.ToLower(out), "://") {
		cred := httpBasicCredRe.ReplaceAllStringFunc(out, func(m string) string {
			changed = true
			return httpBasicCredRe.ReplaceAllString(m, "${1}***:***@")
		})
		if cred != out {
			out = cred
		}
	}
	// Alibaba Cloud AccessKey credentials (OSS profiles etc.).
	if m := aliyunAKIDRe.ReplaceAllStringFunc(out, func(m string) string {
		changed = true
		return "LTAI***REDACTED***"
	}); m != out {
		out = m
	}
	if m := aliyunAKSecretRe.ReplaceAllStringFunc(out, func(m string) string {
		changed = true
		return "[REDACTED]"
	}); m != out {
		out = m
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
