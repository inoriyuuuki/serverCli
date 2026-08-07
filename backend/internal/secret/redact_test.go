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
