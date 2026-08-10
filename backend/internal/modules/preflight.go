package modules

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"servercli/internal/bootstrap"
	"servercli/internal/modman"
)

// Check statuses.
const (
	StatusOK          = "ok"
	StatusWarn        = "warn"
	StatusDegraded    = "degraded"
	StatusBlocked     = "blocked"
	StatusUnsupported = "unsupported"
	StatusSkipped     = "skipped"
)

// CheckResult is the outcome of one preflight check.
type CheckResult struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal"`
}

// PreflightResult aggregates every check plus derived flags.
type PreflightResult struct {
	Checks   []CheckResult `json:"checks"`
	Fatal    bool          `json:"fatal"`    // any check requires ExitPreflight
	Blocked  bool          `json:"blocked"`  // any ownership/conflict block
	Degraded bool          `json:"degraded"` // any non-fatal degradation
}

func (r *PreflightResult) add(c CheckResult) {
	r.Checks = append(r.Checks, c)
	switch c.Status {
	case StatusBlocked:
		r.Blocked = true
	case StatusDegraded:
		r.Degraded = true
	}
	if c.Fatal {
		r.Fatal = true
	}
}

// ExitCode maps the result to a stable bootstrap exit code.
func (r *PreflightResult) ExitCode() int {
	if r.Fatal {
		return bootstrap.ExitPreflight
	}
	return bootstrap.ExitOK
}

// OwnerResolver reports the current owner of a service/module id. A nil
// resolver disables ownership conflict checks.
type OwnerResolver interface {
	Owner(moduleID string) (string, error)
}

// Preflight carries the inputs for one preflight run.
type Preflight struct {
	ModulesDir string
	Inventory  *bootstrap.Inventory

	// PlanOnly turns on plan-only informational checks (port 80/443 hints).
	// Plan never changes DNS.
	PlanOnly bool
	// Adopt marks the run as an adopt flow; ownership conflicts are then
	// allowed (warned) instead of blocked.
	Adopt bool

	// Config/Secrets are forwarded to module preflight scripts (delivery env
	// or file are handled by the runner).
	Config  map[string]string
	Secrets map[string]string

	// RunDir is the scratch dir for module preflight scripts; defaults to
	// /run/servercli/bootstrap.
	RunDir string

	// V2RayEnabled mirrors the inventory-controlled v2ray switch. When false
	// and ProbeDirect is true, direct-connect probes run against ProbeURLs.
	V2RayEnabled bool
	ProbeDirect  bool
	ProbeTimeout time.Duration
	ProbeURLs    []string
	HTTPClient   *http.Client

	// ProbePorts enables the plan-only 80/443 writable probe.
	ProbePorts bool

	// LookupDomain and LocalIPs are injectable for hermetic tests. They
	// default to the system resolver and interface addresses.
	LookupDomain func(ctx context.Context, domain string) ([]string, error)
	LocalIPs     func() ([]string, error)

	// SkipModulePreflights skips running each module's preflight.sh in the
	// global pass. The CLI runs module preflights per-module right before that
	// module's install (after its dependencies are committed), which is when
	// their prerequisite checks are meaningful.
	SkipModulePreflights bool

	// Owners is the ownership resolver; nil disables ownership checks.
	Owners OwnerResolver

	// ModuleTimeout bounds each module preflight script (default 5m).
	ModuleTimeout time.Duration

	Log *slog.Logger
}

// Run executes all preflight checks and returns the aggregated result. It
// returns an error only for internal failures (unreadable module tree,
// dependency cycle, ownership resolver errors).
func (p *Preflight) Run(ctx context.Context) (*PreflightResult, error) {
	if p == nil {
		return nil, errors.New("modules: nil preflight")
	}
	mods, err := modman.LoadAll(p.ModulesDir)
	if err != nil {
		return nil, fmt.Errorf("modules: load modules: %w", err)
	}
	graph, err := modman.NewDepGraph(mods)
	if err != nil {
		return nil, fmt.Errorf("modules: dependency graph: %w", err)
	}
	order, err := graph.Ordered()
	if err != nil {
		return nil, fmt.Errorf("modules: dependency order: %w", err)
	}

	res := &PreflightResult{}
	p.checkOS(res)
	p.checkArch(res)
	p.checkConnectivity(ctx, res)
	p.checkDNS(ctx, res)
	p.checkPorts(res)
	if !p.SkipModulePreflights {
		p.checkModulePreflights(ctx, res, mods, order)
	}
	if err := p.checkOwnership(ctx, res, mods, order); err != nil {
		return nil, err
	}
	return res, nil
}

// checkOS recognizes CentOS/RHEL/EL8/EL9. Non-Linux runtimes are reported as
// unsupported but never fatal, so preflight remains testable off-node.
func (p *Preflight) checkOS(res *PreflightResult) {
	if runtime.GOOS != "linux" {
		res.add(CheckResult{
			Name:    "os",
			Status:  StatusUnsupported,
			Message: fmt.Sprintf("runtime %s/%s is not supported; OS detection skipped", runtime.GOOS, runtime.GOARCH),
		})
		return
	}
	id, version, err := readOSRelease("/etc/os-release")
	if err != nil {
		res.add(CheckResult{
			Name:    "os",
			Status:  StatusWarn,
			Message: "cannot read /etc/os-release: " + err.Error(),
		})
		return
	}
	switch {
	case isELFamily(id) && strings.HasPrefix(version, "8"):
		res.add(CheckResult{Name: "os", Status: StatusOK, Message: fmt.Sprintf("%s %s detected (EL8)", id, version)})
	case isELFamily(id) && strings.HasPrefix(version, "9"):
		res.add(CheckResult{Name: "os", Status: StatusOK, Message: fmt.Sprintf("%s %s detected (EL9)", id, version)})
	case isELFamily(id):
		res.add(CheckResult{Name: "os", Status: StatusWarn, Message: fmt.Sprintf("EL-family %s %s is not EL8/EL9", id, version)})
	default:
		res.add(CheckResult{Name: "os", Status: StatusWarn, Message: fmt.Sprintf("distribution %s is not CentOS/RHEL/EL8/EL9", id)})
	}
}

func isELFamily(id string) bool {
	switch id {
	case "centos", "rhel", "rocky", "almalinux", "ol", "tencentos":
		return true
	}
	return false
}

func readOSRelease(path string) (id, version string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ID=") {
			id = strings.Trim(strings.TrimPrefix(line, "ID="), `"`)
		}
		if strings.HasPrefix(line, "VERSION_ID=") {
			version = strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), `"`)
		}
	}
	if id == "" {
		return "", "", errors.New("os-release has no ID")
	}
	return id, version, nil
}

// checkArch requires linux/amd64; arm64 is a warning only.
func (p *Preflight) checkArch(res *PreflightResult) {
	switch runtime.GOARCH {
	case "amd64":
		res.add(CheckResult{Name: "arch", Status: StatusOK, Message: "linux/amd64 supported"})
	case "arm64":
		res.add(CheckResult{Name: "arch", Status: StatusWarn, Message: "linux/arm64: first-version Foundation modules are only guaranteed on amd64"})
	default:
		res.add(CheckResult{Name: "arch", Status: StatusWarn, Message: "unsupported arch " + runtime.GOARCH})
	}
}

// checkConnectivity probes GitHub/OSS/mirror endpoints when v2ray is disabled.
// Failures degrade but are never fatal. Endpoints are injectable; the default
// is the GitHub primary (OSS/mirror endpoints are deployment specific and
// supplied by the caller).
func (p *Preflight) checkConnectivity(ctx context.Context, res *PreflightResult) {
	if p.V2RayEnabled || !p.ProbeDirect {
		res.add(CheckResult{Name: "connectivity", Status: StatusSkipped, Message: "v2ray enabled or direct probing disabled"})
		return
	}
	urls := p.ProbeURLs
	if len(urls) == 0 {
		urls = []string{"https://github.com"}
	}
	client := p.HTTPClient
	if client == nil {
		timeout := p.ProbeTimeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	var failed []string
	for _, u := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
		if err != nil {
			failed = append(failed, u+": "+err.Error())
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			failed = append(failed, u+": "+err.Error())
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 500 {
			failed = append(failed, fmt.Sprintf("%s: HTTP %d", u, resp.StatusCode))
		}
	}
	if len(failed) > 0 {
		res.add(CheckResult{
			Name:    "connectivity",
			Status:  StatusDegraded,
			Message: "direct connectivity degraded: " + strings.Join(failed, "; "),
		})
		return
	}
	res.add(CheckResult{Name: "connectivity", Status: StatusOK, Message: "direct connectivity ok"})
}

// checkDNS verifies the inventory domain resolves to this node. A mismatch is
// a hard (fatal) preflight failure.
func (p *Preflight) checkDNS(ctx context.Context, res *PreflightResult) {
	if p.Inventory == nil || strings.TrimSpace(p.Inventory.Network.Domain) == "" {
		res.add(CheckResult{Name: "dns", Status: StatusSkipped, Message: "no domain configured"})
		return
	}
	domain := p.Inventory.Network.Domain
	lookup := p.LookupDomain
	if lookup == nil {
		lookup = net.DefaultResolver.LookupHost
	}
	ips, err := lookup(ctx, domain)
	if err != nil {
		res.add(CheckResult{Name: "dns", Status: StatusBlocked, Fatal: true, Message: fmt.Sprintf("DNS lookup for %s failed: %v", domain, err)})
		return
	}
	locals := p.localIPSet()
	for _, ip := range ips {
		if locals[ip] {
			res.add(CheckResult{Name: "dns", Status: StatusOK, Message: fmt.Sprintf("%s resolves to this node (%s)", domain, ip)})
			return
		}
	}
	res.add(CheckResult{Name: "dns", Status: StatusBlocked, Fatal: true, Message: fmt.Sprintf("DNS %s -> %v does not point at this node", domain, ips)})
}

func (p *Preflight) localIPSet() map[string]bool {
	set := map[string]bool{}
	add := func(ips []string) {
		for _, ip := range ips {
			ip = strings.TrimSpace(ip)
			if ip != "" {
				set[ip] = true
			}
		}
	}
	if p.LocalIPs != nil {
		if ips, err := p.LocalIPs(); err == nil {
			add(ips)
		}
	} else {
		add(localInterfaceIPs())
	}
	if inv := p.Inventory; inv != nil {
		add([]string{inv.Network.PublicIP})
		add(inv.Network.PrivateIPs)
	}
	return set
}

func localInterfaceIPs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() {
			continue
		}
		out = append(out, ip.String())
	}
	return out
}

// checkPorts runs the plan-only 80/443 writable probe. It never changes DNS
// and is never fatal; results are hints only.
func (p *Preflight) checkPorts(res *PreflightResult) {
	if !p.PlanOnly || !p.ProbePorts {
		res.add(CheckResult{Name: "ports", Status: StatusSkipped, Message: "port probe is plan-only"})
		return
	}
	var notes []string
	for _, port := range []int{80, 443} {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			_ = ln.Close()
			notes = append(notes, fmt.Sprintf("port %d writable", port))
			continue
		}
		if isAddrInUse(err) {
			notes = append(notes, fmt.Sprintf("port %d occupied by another process", port))
		} else {
			notes = append(notes, fmt.Sprintf("port %d cannot be verified from this user: %v", port, err))
		}
	}
	res.add(CheckResult{Name: "ports", Status: StatusWarn, Message: strings.Join(notes, "; ")})
}

func isAddrInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return strings.Contains(opErr.Err.Error(), "address already in use")
	}
	return false
}

// checkModulePreflights executes each module's operations/preflight.sh via the
// modman Runner (exit 0 = ok; non-zero reasons are collected). Modules without
// a preflight.sh are treated as passed.
func (p *Preflight) checkModulePreflights(ctx context.Context, res *PreflightResult, mods []*modman.ModuleManifest, order []string) {
	byID := map[string]*modman.ModuleManifest{}
	for _, m := range mods {
		byID[m.ID] = m
	}
	timeout := p.ModuleTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	runDir := p.RunDir
	if runDir == "" {
		runDir = bootstrap.DirRunBootstrap
	}
	log := p.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	runner := modman.NewRunner(p.ModulesDir, filepath.Join(runDir, "preflight"), filepath.Join(runDir, "locks"), log, nil)
	for _, id := range order {
		m := byID[id]
		op, ok := m.Operations["preflight"]
		if !ok {
			res.add(CheckResult{Name: "module:" + id, Status: StatusOK, Message: "no preflight script; treated as passed"})
			continue
		}
		if _, err := os.Stat(filepath.Join(m.Dir, op.Entry)); err != nil {
			res.add(CheckResult{Name: "module:" + id, Status: StatusOK, Message: "preflight entry missing; treated as passed"})
			continue
		}
		out, err := runner.Run(ctx, modman.RunOptions{
			ModuleID:  id,
			Operation: "preflight",
			Config:    p.Config,
			Secrets:   p.Secrets,
			Timeout:   timeout,
		})
		if err != nil {
			msg := "module preflight failed"
			if out != nil && strings.TrimSpace(out.Output) != "" {
				msg += ": " + truncate(strings.TrimSpace(out.Output), 300)
			}
			res.add(CheckResult{Name: "module:" + id, Status: StatusBlocked, Fatal: true, Message: msg})
			continue
		}
		res.add(CheckResult{Name: "module:" + id, Status: StatusOK, Message: "module preflight ok"})
	}
}

// checkOwnership blocks any module whose owner is not servercli unless the run
// is an adopt flow.
func (p *Preflight) checkOwnership(ctx context.Context, res *PreflightResult, mods []*modman.ModuleManifest, order []string) error {
	if p.Owners == nil {
		res.add(CheckResult{Name: "ownership", Status: StatusSkipped, Message: "ownership resolver not configured"})
		return nil
	}
	for _, id := range order {
		owner, err := p.Owners.Owner(id)
		if err != nil {
			return fmt.Errorf("modules: owner lookup for %s: %w", id, err)
		}
		if owner == "" || owner == "servercli" {
			continue
		}
		if p.Adopt {
			res.add(CheckResult{Name: "ownership:" + id, Status: StatusWarn, Message: fmt.Sprintf("module %s owned by %s; adopt flow will take over", id, owner)})
			continue
		}
		res.add(CheckResult{Name: "ownership:" + id, Status: StatusBlocked, Fatal: true, Message: fmt.Sprintf("module %s owned by %s (not servercli); adopt required", id, owner)})
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
