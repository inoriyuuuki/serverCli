package modules

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"servercli/internal/bootstrap"
)

// fakeOwners implements OwnerResolver with a fixed owner.
type fakeOwners struct {
	owner string
	err   error
}

func (f fakeOwners) Owner(string) (string, error) { return f.owner, f.err }

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestPreflight(t *testing.T, root string, mutate func(*Preflight)) *Preflight {
	t.Helper()
	p := &Preflight{ModulesDir: root, RunDir: t.TempDir()}
	if mutate != nil {
		mutate(p)
	}
	return p
}

func findCheck(res *PreflightResult, name string) *CheckResult {
	for i := range res.Checks {
		if res.Checks[i].Name == name {
			return &res.Checks[i]
		}
	}
	return nil
}

func TestPreflightRunsAllChecks(t *testing.T) {
	root := t.TempDir()
	writeFoundation(t, root, nil)
	p := newTestPreflight(t, root, nil)
	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"os", "arch", "connectivity", "dns", "ports", "ownership"} {
		if findCheck(res, name) == nil {
			t.Fatalf("missing check %q in %+v", name, res.Checks)
		}
	}
	for _, id := range FoundationCoreOrder() {
		if findCheck(res, "module:"+id) == nil {
			t.Fatalf("missing module check for %s", id)
		}
	}
	if res.Fatal {
		t.Fatalf("clean preflight reported fatal: %+v", res.Checks)
	}
	if res.ExitCode() != bootstrap.ExitOK {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode(), bootstrap.ExitOK)
	}
}

func TestPreflightNonLinuxUnsupported(t *testing.T) {
	root := t.TempDir()
	writeFoundation(t, root, nil)
	p := newTestPreflight(t, root, nil)
	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	osCheck := findCheck(res, "os")
	if osCheck == nil {
		t.Fatal("missing os check")
	}
	if runtime.GOOS != "linux" {
		if osCheck.Status != StatusUnsupported {
			t.Fatalf("os status = %q, want unsupported on %s", osCheck.Status, runtime.GOOS)
		}
		if osCheck.Fatal || res.Fatal {
			t.Fatalf("unsupported OS must not be fatal: %+v", res.Checks)
		}
	}
}

func TestPreflightArch(t *testing.T) {
	root := t.TempDir()
	writeFoundation(t, root, nil)
	p := newTestPreflight(t, root, nil)
	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	arch := findCheck(res, "arch")
	if arch == nil {
		t.Fatal("missing arch check")
	}
	switch runtime.GOARCH {
	case "amd64":
		if arch.Status != StatusOK {
			t.Fatalf("arch status = %q, want ok", arch.Status)
		}
	case "arm64":
		if arch.Status != StatusWarn {
			t.Fatalf("arch status = %q, want warn on arm64", arch.Status)
		}
	default:
		if arch.Status != StatusWarn {
			t.Fatalf("arch status = %q, want warn on %s", arch.Status, runtime.GOARCH)
		}
	}
	if arch.Fatal {
		t.Fatal("arch check must not be fatal")
	}
}

func TestPreflightOwnershipBlocked(t *testing.T) {
	root := t.TempDir()
	writeFoundation(t, root, nil)
	p := newTestPreflight(t, root, func(p *Preflight) {
		p.Owners = fakeOwners{owner: "legacy-init"}
	})
	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	chk := findCheck(res, "ownership:docker")
	if chk == nil {
		t.Fatal("missing ownership:docker check")
	}
	if chk.Status != StatusBlocked || !chk.Fatal {
		t.Fatalf("ownership:docker = %+v, want blocked/fatal", chk)
	}
	if !res.Fatal || !res.Blocked {
		t.Fatalf("result flags = fatal:%v blocked:%v, want fatal+blocked", res.Fatal, res.Blocked)
	}
	if res.ExitCode() != bootstrap.ExitPreflight {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode(), bootstrap.ExitPreflight)
	}
}

func TestPreflightOwnershipAdoptAllowed(t *testing.T) {
	root := t.TempDir()
	writeFoundation(t, root, nil)
	p := newTestPreflight(t, root, func(p *Preflight) {
		p.Owners = fakeOwners{owner: "legacy-init"}
		p.Adopt = true
	})
	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	chk := findCheck(res, "ownership:docker")
	if chk == nil || chk.Status != StatusWarn {
		t.Fatalf("ownership:docker = %+v, want warn during adopt", chk)
	}
	if chk.Fatal || res.Fatal {
		t.Fatalf("adopt ownership conflicts must not be fatal: %+v", res.Checks)
	}
}

func TestPreflightOwnershipServerCliOK(t *testing.T) {
	root := t.TempDir()
	writeFoundation(t, root, nil)
	p := newTestPreflight(t, root, func(p *Preflight) {
		p.Owners = fakeOwners{owner: "servercli"}
	})
	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Fatal {
		t.Fatalf("servercli-owned modules must not be blocked: %+v", res.Checks)
	}
}

func TestPreflightOwnershipResolverError(t *testing.T) {
	root := t.TempDir()
	writeFoundation(t, root, nil)
	p := newTestPreflight(t, root, func(p *Preflight) {
		p.Owners = fakeOwners{err: errors.New("boom")}
	})
	if _, err := p.Run(context.Background()); err == nil {
		t.Fatal("expected owner resolver error")
	}
}

func TestPreflightDNS(t *testing.T) {
	root := t.TempDir()
	writeFoundation(t, root, nil)
	inv := &bootstrap.Inventory{
		Network: bootstrap.InventoryNetwork{Domain: "svc.example", PublicIP: "192.0.2.10"},
	}

	t.Run("points at node", func(t *testing.T) {
		p := newTestPreflight(t, root, func(p *Preflight) {
			p.Inventory = inv
			p.LookupDomain = func(ctx context.Context, d string) ([]string, error) { return []string{"192.0.2.10"}, nil }
			p.LocalIPs = func() ([]string, error) { return []string{"192.0.2.10"}, nil }
		})
		res, err := p.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		chk := findCheck(res, "dns")
		if chk == nil || chk.Status != StatusOK {
			t.Fatalf("dns = %+v, want ok", chk)
		}
	})

	t.Run("mismatch blocked", func(t *testing.T) {
		p := newTestPreflight(t, root, func(p *Preflight) {
			p.Inventory = inv
			p.LookupDomain = func(ctx context.Context, d string) ([]string, error) { return []string{"203.0.113.5"}, nil }
			p.LocalIPs = func() ([]string, error) { return []string{"192.0.2.10"}, nil }
		})
		res, err := p.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		chk := findCheck(res, "dns")
		if chk == nil || chk.Status != StatusBlocked || !chk.Fatal {
			t.Fatalf("dns = %+v, want blocked/fatal", chk)
		}
	})

	t.Run("lookup error blocked", func(t *testing.T) {
		p := newTestPreflight(t, root, func(p *Preflight) {
			p.Inventory = inv
			p.LookupDomain = func(ctx context.Context, d string) ([]string, error) { return nil, errors.New("NXDOMAIN") }
		})
		res, err := p.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		chk := findCheck(res, "dns")
		if chk == nil || chk.Status != StatusBlocked || !chk.Fatal {
			t.Fatalf("dns = %+v, want blocked/fatal", chk)
		}
	})
}

func TestPreflightConnectivityDegradedNotFatal(t *testing.T) {
	root := t.TempDir()
	writeFoundation(t, root, nil)
	p := newTestPreflight(t, root, func(p *Preflight) {
		p.V2RayEnabled = false
		p.ProbeDirect = true
		p.ProbeURLs = []string{"https://download.example/invalid"}
		p.HTTPClient = &http.Client{
			Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("dial tcp: connection refused")
			}),
		}
	})
	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	chk := findCheck(res, "connectivity")
	if chk == nil || chk.Status != StatusDegraded {
		t.Fatalf("connectivity = %+v, want degraded", chk)
	}
	if chk.Fatal || res.Fatal {
		t.Fatalf("connectivity degradation must not be fatal: %+v", res.Checks)
	}
	if !res.Degraded {
		t.Fatal("expected degraded flag")
	}
}

func TestPreflightConnectivityOK(t *testing.T) {
	root := t.TempDir()
	writeFoundation(t, root, nil)
	p := newTestPreflight(t, root, func(p *Preflight) {
		p.V2RayEnabled = false
		p.ProbeDirect = true
		p.ProbeURLs = []string{"https://download.example/asset"}
		p.HTTPClient = &http.Client{
			Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: r}, nil
			}),
		}
	})
	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	chk := findCheck(res, "connectivity")
	if chk == nil || chk.Status != StatusOK {
		t.Fatalf("connectivity = %+v, want ok", chk)
	}
}

func TestPreflightModuleScripts(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, "a", nil, 0, "")                                 // preflight ok
	writeModule(t, root, "b", nil, 1, "")                                 // preflight fails
	writeModule(t, root, "c", nil, -1, "")                                // no preflight script
	writeModule(t, root, "d", nil, 0, "")                                 // op declared...
	_ = os.Remove(filepath.Join(root, "d", "operations", "preflight.sh")) // ...but entry missing

	p := newTestPreflight(t, root, nil)
	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	chkA := findCheck(res, "module:a")
	if chkA == nil || chkA.Status != StatusOK {
		t.Fatalf("module:a = %+v, want ok", chkA)
	}
	chkB := findCheck(res, "module:b")
	if chkB == nil || chkB.Status != StatusBlocked || !chkB.Fatal {
		t.Fatalf("module:b = %+v, want blocked/fatal", chkB)
	}
	if !strings.Contains(chkB.Message, "preflight b") {
		t.Fatalf("module:b message should include script reason: %q", chkB.Message)
	}
	chkC := findCheck(res, "module:c")
	if chkC == nil || chkC.Status != StatusOK {
		t.Fatalf("module:c = %+v, want ok (no preflight)", chkC)
	}
	chkD := findCheck(res, "module:d")
	if chkD == nil || chkD.Status != StatusOK {
		t.Fatalf("module:d = %+v, want ok (entry missing)", chkD)
	}
	if !res.Fatal {
		t.Fatal("module preflight failure must be fatal")
	}
	if res.ExitCode() != bootstrap.ExitPreflight {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode(), bootstrap.ExitPreflight)
	}
}

func TestPreflightNilReceiver(t *testing.T) {
	var p *Preflight
	if _, err := p.Run(context.Background()); err == nil {
		t.Fatal("expected error for nil preflight")
	}
}

func TestPreflightMissingModulesDir(t *testing.T) {
	p := &Preflight{ModulesDir: t.TempDir() + "/nope", RunDir: t.TempDir()}
	if _, err := p.Run(context.Background()); err == nil {
		t.Fatal("expected error for missing modules dir")
	}
}
