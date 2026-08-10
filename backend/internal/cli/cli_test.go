package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb, VersionInfo{Version: "0.1.0-test", Build: "test", Commit: "abc"})
	return code, out.String(), errb.String()
}

func TestVersion(t *testing.T) {
	code, out, _ := run("version")
	if code != 0 || !strings.Contains(out, "0.1.0-test") {
		t.Fatalf("version: code=%d out=%q", code, out)
	}
}

func TestHelp(t *testing.T) {
	code, out, _ := run("help")
	if code != 0 || !strings.Contains(out, "servercli init") {
		t.Fatalf("help: code=%d out=%q", code, out)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, _, errb := run("frobnicate")
	if code != 2 || !strings.Contains(errb, "unknown command") {
		t.Fatalf("code=%d err=%q", code, errb)
	}
}

func TestInitPlanRequiresInputs(t *testing.T) {
	code, _, errb := run("init", "plan")
	if code != 2 || !strings.Contains(errb, "--environment") {
		t.Fatalf("code=%d err=%q", code, errb)
	}
}

func TestInitStatusFresh(t *testing.T) {
	dir := t.TempDir()
	code, out, errb := run("init", "status", "--state-path="+filepath.Join(dir, "state.json"))
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "not_initialized") {
		t.Fatalf("out=%q", out)
	}
}

func TestInitApplyNonTTYRequiresYes(t *testing.T) {
	dir := t.TempDir()
	code, _, errb := run("init", "apply",
		"--state-path="+filepath.Join(dir, "state.json"),
		"--environment=test", "--node-name=n1", "--bundle-url=file:///tmp/x",
		"--age-key-file=/tmp/x", "--pubkey-file=/tmp/x")
	if code != 2 || !strings.Contains(errb, "--yes") {
		t.Fatalf("code=%d err=%q", code, errb)
	}
}

func TestModulesRunRequiresModuleOp(t *testing.T) {
	code, _, errb := run("modules", "run", "--yes")
	if code != 2 || !strings.Contains(errb, "--module") {
		t.Fatalf("code=%d err=%q", code, errb)
	}
}

func TestOpsUnknown(t *testing.T) {
	code, _, errb := run("ops", "explode")
	if code != 2 || !strings.Contains(errb, "unknown operation") {
		t.Fatalf("code=%d err=%q", code, errb)
	}
}

func TestConsumeFlags(t *testing.T) {
	a := defaultApp()
	rest := consumeFlags(a, []string{"--yes", "--environment=prod", "init", "status"})
	if !a.yes || a.env != "prod" {
		t.Fatalf("flags not parsed: %+v", a)
	}
	if len(rest) != 2 || rest[0] != "init" {
		t.Fatalf("rest = %v", rest)
	}
}

func TestJSONOutput(t *testing.T) {
	code, out, _ := run("init", "status", "--output=json", "--state-path="+filepath.Join(t.TempDir(), "s.json"))
	if code != 0 || !strings.Contains(out, "\"overall\"") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}
