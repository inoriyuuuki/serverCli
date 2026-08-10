package secretstore

import "testing"

func TestParseDotEnv(t *testing.T) {
	m, err := ParseDotEnv([]byte("# comment\nA=1\nB=\"two\"\nC='three'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m["A"] != "1" || m["B"] != "two" || m["C"] != "three" {
		t.Fatalf("parsed = %v", m)
	}
}

func TestParseDotEnvRejectsExpansion(t *testing.T) {
	for _, in := range []string{"A=$HOME", "A=$(id)", "A=`id`", "A=${B}"} {
		if _, err := ParseDotEnv([]byte(in)); err == nil {
			t.Errorf("should reject expansion: %q", in)
		}
	}
}

func TestParseDotEnvRejectsDuplicatesAndBadKeys(t *testing.T) {
	if _, err := ParseDotEnv([]byte("A=1\nA=2\n")); err == nil {
		t.Fatal("duplicate key should fail")
	}
	if _, err := ParseDotEnv([]byte("1BAD=x\n")); err == nil {
		t.Fatal("bad key should fail")
	}
	if _, err := ParseDotEnv([]byte("A=B C D")); err != nil {
		t.Fatalf("space in value should be fine: %v", err)
	}
}
