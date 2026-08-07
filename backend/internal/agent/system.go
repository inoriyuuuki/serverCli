package agent

import (
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// SystemSummary carries basic host metrics for heartbeats.
type SystemSummary struct {
	CPUUsagePercent  float64        `json:"cpu_usage_percent"`
	MemoryTotalBytes int64          `json:"memory_total_bytes"`
	MemoryUsedBytes  int64          `json:"memory_used_bytes"`
	DiskTotalBytes   int64          `json:"disk_total_bytes"`
	DiskUsedBytes    int64          `json:"disk_used_bytes"`
	Load1            float64        `json:"load_1"`
	Load5            float64        `json:"load_5"`
	Load15           float64        `json:"load_15"`
	UptimeSeconds    int64          `json:"uptime_seconds"`
	Extra            map[string]any `json:"extra,omitempty"`
}

// CollectSystem gathers host metrics for the current platform. Unsupported
// platforms return zeros (debug logged).
func CollectSystem(log *slog.Logger) SystemSummary {
	var s SystemSummary
	switch runtime.GOOS {
	case "linux":
		s = collectLinux(log)
	case "darwin":
		s = collectDarwin(log)
	default:
		log.Debug("system metrics unsupported on this platform", "os", runtime.GOOS)
	}
	return s
}

func collectLinux(log *slog.Logger) SystemSummary {
	var s SystemSummary
	// CPU from /proc/stat deltas are handled by the caller across ticks; here
	// we approximate with the first two samples when possible.
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			s.Load1, _ = strconv.ParseFloat(fields[0], 64)
			s.Load5, _ = strconv.ParseFloat(fields[1], 64)
			s.Load15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if f, err := strconv.ParseFloat(fields[0], 64); err == nil {
				s.UptimeSeconds = int64(f)
			}
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			val, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				continue
			}
			val *= 1024 // kB
			switch fields[0] {
			case "MemTotal:":
				s.MemoryTotalBytes = val
			case "MemAvailable:":
				s.MemoryUsedBytes = s.MemoryTotalBytes - val
			}
		}
	}
	if s.MemoryTotalBytes > 0 && s.MemoryUsedBytes == 0 {
		// fallback: MemFree + Buffers + Cached approximation omitted; leave 0
	}
	// Disk: use statfs via /proc/mounts is complex; query df for the root.
	if out, err := exec.Command("df", "-k", "/").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) >= 2 {
			fields := strings.Fields(lines[1])
			if len(fields) >= 4 {
				total, _ := strconv.ParseInt(fields[1], 10, 64)
				used, _ := strconv.ParseInt(fields[2], 10, 64)
				s.DiskTotalBytes = total * 1024
				s.DiskUsedBytes = used * 1024
			}
		}
	}
	_ = log
	return s
}

func collectDarwin(log *slog.Logger) SystemSummary {
	var s SystemSummary
	sysctl := func(name string) string {
		out, err := exec.Command("/usr/sbin/sysctl", "-n", name).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	if v := sysctl("hw.memsize"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			s.MemoryTotalBytes = n
		}
	}
	if v := sysctl("vm.loadavg"); v != "" {
		fields := strings.Fields(v)
		if len(fields) >= 3 {
			s.Load1, _ = strconv.ParseFloat(fields[0], 64)
			s.Load5, _ = strconv.ParseFloat(fields[1], 64)
			s.Load15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}
	if out, err := exec.Command("df", "-k", "/").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) >= 2 {
			fields := strings.Fields(lines[1])
			if len(fields) >= 4 {
				total, _ := strconv.ParseInt(fields[1], 10, 64)
				used, _ := strconv.ParseInt(fields[2], 10, 64)
				s.DiskTotalBytes = total * 1024
				s.DiskUsedBytes = used * 1024
			}
		}
	}
	if out, err := exec.Command("/usr/bin/uptime").Output(); err == nil {
		line := string(out)
		if i := strings.Index(line, "up "); i >= 0 {
			rest := line[i+3:]
			if j := strings.Index(rest, ","); j > 0 {
				rest = rest[:j]
			}
			rest = strings.TrimSpace(rest)
			var days, hours, mins int64
			for _, part := range strings.Split(rest, " ") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				if strings.HasSuffix(part, "day") || strings.HasSuffix(part, "days") {
					continue
				}
				if n, err := strconv.ParseInt(strings.TrimSuffix(part, ":"), 10, 64); err == nil {
					if strings.Contains(part, ":") {
						hours = n
					} else if strings.HasSuffix(part, "min") {
						mins = n
					} else if !strings.Contains(part, ":") {
						// plain number: days or hours, order-dependent; best effort
						if days == 0 && strings.Contains(rest, "day") {
							days = n
						} else if hours == 0 {
							hours = n
						} else {
							mins = n
						}
					}
				}
			}
			s.UptimeSeconds = days*86400 + hours*3600 + mins*60
		}
	}
	_ = log
	return s
}
