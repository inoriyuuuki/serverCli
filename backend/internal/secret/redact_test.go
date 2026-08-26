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

func TestRedactAliyunAccessKeyID(t *testing.T) {
	r := NewRedactor()
	// LTAI prefix + 12..24 alphanumeric (full AccessKey ID forms).
	cases := []string{
		"LTAI5tExampleAccessKey1",
		"accessKeyId=LTAI5tExampleAccessKey1",
		"oss config: LTAI5tABCDEFGHIJKLMNOPQRSTUVWX",
	}
	for _, in := range cases {
		out := r.RedactString(in)
		if strings.Contains(out, "LTAI5tExampleAccessKey1") || strings.Contains(out, "LTAI5tABCDEFGHIJKLMNOPQRSTUVWX") {
			t.Fatalf("aliyun AK ID leaked for input %q: %q", in, out)
		}
		if !strings.Contains(out, "LTAI***REDACTED***") {
			t.Fatalf("expected LTAI***REDACTED*** marker for input %q, got %q", in, out)
		}
	}
	// An AK ID embedded as the username of an HTTP URL must be masked too.
	urlCase := "http://LTAI5tExampleAccessKey1:secret@oss-cn-hangzhou.aliyuncs.com/bucket"
	out := r.RedactString(urlCase)
	if strings.Contains(out, "LTAI5tExampleAccessKey1") || strings.Contains(out, "secret") {
		t.Fatalf("aliyun AK ID leaked in URL for %q: %q", urlCase, out)
	}
	if !strings.Contains(out, "***:***@") {
		t.Fatalf("expected ***:***@ marker for URL case, got %q", out)
	}
	if r.Count() == 0 {
		t.Fatal("expected redaction count")
	}
}

func TestRedactAliyunAKSecretKeyValue(t *testing.T) {
	r := NewRedactor()
	cases := []struct{ in, secret string }{
		{"accessKeySecret=abcdefghijklmnop123456", "abcdefghijklmnop123456"},
		{"access_key_secret: 'GHIJKLMNOPQRSTUVWXYZabcdefgh12'", "GHIJKLMNOPQRSTUVWXYZabcdefgh12"},
		{"ak_secret = ABCDEFGHIJKLMNOPQRSTUVWXYZ012345", "ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"},
		{"aliyun_secret: abcdefghijklmnopqrstuvwxyz12345678", "abcdefghijklmnopqrstuvwxyz12345678"},
		{"ACCESS_KEY_SECRET=AbCdEfGhIjKlMnOpQrStUvWxYz1234", "AbCdEfGhIjKlMnOpQrStUvWxYz1234"},
	}
	for _, c := range cases {
		out := r.RedactString(c.in)
		if strings.Contains(out, c.secret) {
			t.Fatalf("AK secret leaked for input %q: %q", c.in, out)
		}
		if !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("expected [REDACTED] marker for input %q, got %q", c.in, out)
		}
	}
	if r.Count() == 0 {
		t.Fatal("expected redaction count")
	}
}

func TestRedactHTTPBasicCredentialURL(t *testing.T) {
	r := NewRedactor()
	cases := []struct{ in, user, pass string }{
		{"git clone http://gituser:gitpass@git.example.com/repo.git", "gituser", "gitpass"},
		{"https://LTAI5tExampleAccessKey1:Sup3rSecret123456@oss.example.com/x", "LTAI5tExampleAccessKey1", "Sup3rSecret123456"},
	}
	for _, c := range cases {
		out := r.RedactString(c.in)
		if strings.Contains(out, c.user) || strings.Contains(out, c.pass) {
			t.Fatalf("HTTP basic credential leaked for input %q: %q", c.in, out)
		}
		if !strings.Contains(out, "***:***@") {
			t.Fatalf("expected ***:***@ marker for input %q, got %q", c.in, out)
		}
		if strings.Contains(out, c.in) {
			t.Fatalf("input unchanged: %q", out)
		}
	}
	if r.Count() == 0 {
		t.Fatal("expected redaction count")
	}
}

func TestRedactMixedAliyunSecrets(t *testing.T) {
	r := NewRedactor()
	ak := "LTAI5tMixedCaseAkId123456"
	sk := "MixedCaseSecretValue987654"
	in := "accessKeyID=" + ak + " accessKeySecret=" + sk + " endpoint=http://user:pw@oss.example.com"
	out := r.RedactString(in)
	for _, v := range []string{ak, sk, "user", "pw"} {
		if strings.Contains(out, v) {
			t.Fatalf("mixed input leaked %q: %q", v, out)
		}
	}
	if !strings.Contains(out, "[REDACTED]") || !strings.Contains(out, "LTAI***REDACTED***") {
		t.Fatalf("expected redaction markers, got %q", out)
	}
}

func TestRedactAliyunAccessKeyIDKeyValueJSON(t *testing.T) {
	// JSON redaction must also mask AK IDs used as access_key values.
	r := NewRedactor()
	in := `{"access_key_id":"LTAI5tJsonAkId123456","access_key_secret":"JsonSecretValue12345678","bucket":"b"}`
	out := r.RedactJSON([]byte(in))
	if strings.Contains(string(out), "LTAI5tJsonAkId123456") || strings.Contains(string(out), "JsonSecretValue12345678") {
		t.Fatalf("JSON AK credentials leaked: %s", out)
	}
	if !strings.Contains(string(out), "[REDACTED]") {
		t.Fatalf("expected redaction markers in %s", out)
	}
}

func TestRedactDSNStillWorks(t *testing.T) {
	// Existing DSN redaction must not regress with the new URL rule.
	r := NewRedactor()
	out := r.RedactString("postgres://user:s3cret@db.example:5432/servercli")
	if strings.Contains(out, "s3cret") {
		t.Fatalf("dsn password leaked: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("expected dsn redaction marker, got %s", out)
	}
}
