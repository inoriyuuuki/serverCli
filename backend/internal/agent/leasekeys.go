package agent

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LeaseInstall is an install instruction from the control plane.
type LeaseInstall struct {
	LeaseID           string    `json:"lease_id"`
	PublicKey         string    `json:"public_key"`
	PermissionProfile string    `json:"permission_profile"`
	ExpiresAt         time.Time `json:"expires_at"`
}

// LeaseRemove is a removal instruction from the control plane.
type LeaseRemove struct {
	LeaseID string `json:"lease_id"`
	Reason  string `json:"reason"`
}

var leaseMarkerRe = regexp.MustCompile(`servercli-lease-([A-Za-z0-9-]+)-exp-(\d+)$`)

// LeaseKeyManager atomically manages the temporary authorized_keys file.
type LeaseKeyManager struct {
	path       string
	leaseShell string
	log        *slog.Logger
	mu         sync.Mutex
}

// NewLeaseKeyManager builds the manager.
func NewLeaseKeyManager(path, leaseShell string, log *slog.Logger) *LeaseKeyManager {
	return &LeaseKeyManager{path: path, leaseShell: leaseShell, log: log}
}

// Apply installs and removes keys atomically.
func (m *LeaseKeyManager) Apply(installs []LeaseInstall, removes []LeaseRemove) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	lines, err := m.read()
	if err != nil {
		return err
	}
	byLease := map[string]string{}
	for _, line := range lines {
		if id, exp, ok := parseManagedLine(line); ok {
			byLease[id] = line
			_ = exp
		}
	}
	// Remove.
	for _, rm := range removes {
		if _, ok := byLease[rm.LeaseID]; ok {
			delete(byLease, rm.LeaseID)
			m.log.Info("lease key removed", "lease_id", rm.LeaseID, "reason", rm.Reason)
		}
	}
	// Install/refresh.
	for _, inst := range installs {
		line := m.buildLine(inst)
		if existing, ok := byLease[inst.LeaseID]; ok && existing == line {
			continue
		}
		byLease[inst.LeaseID] = line
		m.log.Info("lease key installed", "lease_id", inst.LeaseID, "expires_at", inst.ExpiresAt)
	}
	// Write only managed lines.
	var out []string
	for _, line := range byLease {
		out = append(out, line)
	}
	return m.write(out)
}

// SweepExpired removes managed keys that expired locally (works even if the
// control plane is unreachable).
func (m *LeaseKeyManager) SweepExpired(now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lines, err := m.read()
	if err != nil {
		return 0, err
	}
	removed := 0
	var kept []string
	for _, line := range lines {
		if id, exp, ok := parseManagedLine(line); ok {
			if time.Now().Unix() >= exp {
				m.log.Info("locally removing expired lease key", "lease_id", id)
				removed++
				continue
			}
		}
		kept = append(kept, line)
	}
	if removed > 0 {
		if err := m.write(kept); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func (m *LeaseKeyManager) buildLine(inst LeaseInstall) string {
	options := "restrict,no-agent-forwarding,no-port-forwarding,no-X11-forwarding,no-user-rc"
	shell := m.leaseShell
	if shell == "" {
		shell = "/usr/local/bin/servercli-lease-shell"
	}
	options += fmt.Sprintf(",command=%q", shell+" --lease "+inst.LeaseID)
	comment := fmt.Sprintf("servercli-lease-%s-exp-%d", inst.LeaseID, inst.ExpiresAt.Unix())
	return options + " " + strings.TrimSpace(inst.PublicKey) + " " + comment
}

func parseManagedLine(line string) (leaseID string, expiresUnix int64, ok bool) {
	m := leaseMarkerRe.FindStringSubmatch(line)
	if m == nil {
		return "", 0, false
	}
	exp, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return m[1], exp, true
}

func (m *LeaseKeyManager) read() ([]string, error) {
	f, err := os.Open(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines, sc.Err()
}

func (m *LeaseKeyManager) write(lines []string) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}
