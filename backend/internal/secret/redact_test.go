package secret

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactString(t *testing.T) {
	r := NewRedactor()
	out := r.RedactString("Authorization: Bearer abcdef123456")
	if strings.Contains(out, "abcdef123456") {
		t.Fatal("authorization token leaked")
	}
	if !strings.Contains(out, "[REDACTED") {
		t.Fatal("expected redaction marker")
	}
	if r.Count() == 0 {
		t.Fatal("expected redaction count > 0")
	}
}

func TestRedactPrivateKeyBlock(t *testing.T) {
	r := NewRedactor()
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\nabcdef\n-----END OPENSSH PRIVATE KEY-----"
	out := r.RedactString("key=" + pem)
	if strings.Contains(out, "abcdef") || strings.Contains(out, "OPENSSH PRIVATE KEY") {
		t.Fatalf("private key block leaked: %s", out)
	}
}

func TestRedactDSN(t *testing.T) {
	r := NewRedactor()
	out := r.RedactString("postgres://user:s3cret@db.example:5432/servercli")
	if strings.Contains(out, "s3cret") {
		t.Fatal("dsn password leaked")
	}
}

func TestRedactJSON(t *testing.T) {
	r := NewRedactor()
	in := `{"username":"admin","password":"hunter2","nested":{"token":"tok_abc","ok":true}}`
	out := r.RedactJSON([]byte(in))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["password"] != "[REDACTED]" {
		t.Fatalf("password not redacted: %v", m["password"])
	}
	nested := m["nested"].(map[string]any)
	if nested["token"] != "[REDACTED]" {
		t.Fatalf("token not redacted: %v", nested["token"])
	}
	if nested["ok"] != true {
		t.Fatal("non-secret value changed")
	}
	if r.Count() == 0 {
		t.Fatal("expected redaction count")
	}
}

func TestIsSensitiveKeyWebhook(t *testing.T) {
	for _, k := range []string{"webhook", "hook_url", "webhook_url", "feishu_webhook_url", "HOOK_URL"} {
		if !IsSensitiveKey(k) {
			t.Fatalf("expected %q to be sensitive", k)
		}
	}
	for _, k := range []string{"username", "title", "message", "channel", "source"} {
		if IsSensitiveKey(k) {
			t.Fatalf("expected %q to not be sensitive", k)
		}
	}
}

func TestRedactStringWebhookURL(t *testing.T) {
	r := NewRedactor()
	url := "https://open.feishu.cn/open-apis/bot/v2/hook/xxx"
	out := r.RedactString("feishu webhook: " + url)
	if strings.Contains(out, url) || strings.Contains(out, "open.feishu.cn") {
		t.Fatalf("webhook url leaked: %s", out)
	}
	if !strings.Contains(out, "[REDACTED_URL]") {
		t.Fatalf("expected [REDACTED_URL] marker, got %s", out)
	}
	if r.Count() == 0 {
		t.Fatal("expected redaction count")
	}
}

func TestRedactStringNonHookURLUnchanged(t *testing.T) {
	r := NewRedactor()
	// A non-hook URL must not be masked by the hook URL rule.
	u := "https://example.com/docs/guide"
	out := r.RedactString("see " + u)
	if !strings.Contains(out, u) {
		t.Fatalf("non-hook url should be preserved: %s", out)
	}
	if r.Count() != 0 {
		t.Fatalf("expected no redaction, count=%d", r.Count())
	}
}

func TestRedactStringWebhookStillKeepsDSNRedaction(t *testing.T) {
	r := NewRedactor()
	out := r.RedactString("db=postgres://user:s3cret@db.example:5432/servercli webhook=https://open.feishu.cn/open-apis/bot/v2/hook/yyy")
	if strings.Contains(out, "s3cret") {
		t.Fatal("dsn password leaked")
	}
	if strings.Contains(out, "yyy") || strings.Contains(out, "open.feishu.cn") {
		t.Fatalf("webhook url leaked: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") || !strings.Contains(out, "[REDACTED_URL]") {
		t.Fatalf("expected both redaction markers, got %s", out)
	}
}

func TestRedactJSONWebhook(t *testing.T) {
	r := NewRedactor()
	in := `{"webhook_url":"https://open.feishu.cn/open-apis/bot/v2/hook/abc","ok":true}`
	out := r.RedactJSON([]byte(in))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["webhook_url"] != "[REDACTED]" {
		t.Fatalf("webhook_url not redacted: %v", m["webhook_url"])
	}
	if m["ok"] != true {
		t.Fatal("non-secret value changed")
	}
	if r.Count() == 0 {
		t.Fatal("expected redaction count")
	}
}

func TestRedactStringUppercaseSensitiveKeys(t *testing.T) {
	r := NewRedactor()
	cases := []struct{ in, marker string }{
		{"AUTHORIZATION: Bearer sct_abc123def456", "[REDACTED_AUTH]"},
		{"COOKIE: session=abc123", "[REDACTED_COOKIE]"},
		{"HTTPS://OPEN.FEISHU.CN/OPEN-APIS/BOT/V2/HOOK/UPPER", "[REDACTED_URL]"},
	}
	for _, c := range cases {
		out := r.RedactString(c.in)
		if !strings.Contains(out, c.marker) {
			t.Fatalf("input %q should produce %s, got %q", c.in, c.marker, out)
		}
	}
}
