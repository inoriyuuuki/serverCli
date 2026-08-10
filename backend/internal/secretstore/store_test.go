package secretstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMasterKeyCreatedSecure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	k, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Bytes()) != 32 {
		t.Fatalf("key len = %d", len(k.Bytes()))
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v want 0600", fi.Mode().Perm())
	}
	dirFi, _ := os.Stat(dir)
	if dirFi.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm = %v want 0700", dirFi.Mode().Perm())
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	key, err := LoadOrCreateMasterKey(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := OpenBootstrapStore(filepath.Join(dir, "secrets.enc"), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set("postgres.password", "s3cret-value"); err != nil {
		t.Fatal(err)
	}
	v, ok := st.Get("postgres.password")
	if !ok || v != "s3cret-value" {
		t.Fatalf("get = %q %v", v, ok)
	}
	// Reload from disk.
	st2, err := OpenBootstrapStore(filepath.Join(dir, "secrets.enc"), key)
	if err != nil {
		t.Fatal(err)
	}
	v, ok = st2.Get("postgres.password")
	if !ok || v != "s3cret-value" {
		t.Fatalf("reload get = %q %v", v, ok)
	}
	// Wrong key must fail to open/decrypt the store.
	otherKey, _ := LoadOrCreateMasterKey(filepath.Join(dir, "other.key"))
	if _, err := OpenBootstrapStore(filepath.Join(dir, "secrets.enc"), otherKey); err == nil {
		t.Fatal("wrong master key should fail to open store")
	}
	keys, err := st2.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "postgres.password" {
		t.Fatalf("keys = %v", keys)
	}
}

func TestSanitizeName(t *testing.T) {
	valid := []string{"postgres.password", "ADMIN_INITIAL_PASSWORD", "a_b-c.d"}
	for _, v := range valid {
		if err := SanitizeName(v); err != nil {
			t.Errorf("SanitizeName(%q) unexpected error: %v", v, err)
		}
	}
	invalid := []string{"", "a b", "a$b", "a`b", "a;b", "1abc", "a=b"}
	for _, v := range invalid {
		if err := SanitizeName(v); err == nil {
			t.Errorf("SanitizeName(%q) should fail", v)
		}
	}
}

func TestStoreFileContainsNoPlaintext(t *testing.T) {
	dir := t.TempDir()
	key, err := LoadOrCreateMasterKey(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := OpenBootstrapStore(filepath.Join(dir, "secrets.enc"), key)
	secret := "s3cret-plaintext-value-12345"
	if err := st.Set("postgres.password", secret); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "secrets.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if containsStr(string(raw), secret) {
		t.Fatalf("secrets.enc contains plaintext secret value")
	}
	if containsStr(string(raw), "postgres.password") {
		t.Fatalf("secrets.enc contains plaintext secret key")
	}
	if containsStr(string(raw), `"values"`) {
		t.Fatalf("secrets.enc still contains a plaintext values field")
	}
}

func containsStr(hay, needle string) bool {
	return len(needle) > 0 && indexOfStr(hay, needle) >= 0
}
func indexOfStr(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
