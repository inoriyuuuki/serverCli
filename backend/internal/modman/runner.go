package modman

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// RunOptions configures one provision run.
type RunOptions struct {
	ModuleID       string
	Operation      string
	Config         map[string]string
	Secrets        map[string]string
	ExpectedDigest string // optional: verify input digest before executing
	ModulesDir     string
	RunDir         string // /run/servercli/bootstrap
	LockDir        string // /run/servercli/operations
	Log            *slog.Logger
	Timeout        time.Duration
	Env            []string // extra fixed env, never secrets beyond declared fields
}

// RunResult is the structured outcome of a provision run.
type RunResult struct {
	ModuleID    string    `json:"module_id"`
	Operation   string    `json:"operation"`
	ExitCode    int       `json:"exit_code"`
	Digest      string    `json:"input_digest"`
	Output      string    `json:"output"`       // redacted
	SecretFiles []string  `json:"secret_files"` // created under /run (deleted after)
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

// ErrForbidden signals that the runner refused to execute something outside
// the fixed operation whitelist.
var ErrForbidden = errors.New("modman: operation not in fixed whitelist")

// ErrDigestMismatch signals the input digest did not match ExpectedDigest.
var ErrDigestMismatch = errors.New("modman: input digest mismatch")

// ErrInvalidModuleID signals a module id that is not a plain identifier.
var ErrInvalidModuleID = errors.New("modman: invalid module id")

// ValidateModuleID rejects path traversal and anything that is not a plain
// module identifier ([a-z0-9][a-z0-9-]*), so a module id can never escape the
// modules directory.
func ValidateModuleID(id string) error {
	if id == "" || len(id) > 128 {
		return ErrInvalidModuleID
	}
	if id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return ErrInvalidModuleID
	}
	for i, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if i == 0 && !(r >= 'a' && r <= 'z') {
			ok = false
		}
		if !ok {
			return ErrInvalidModuleID
		}
	}
	return nil
}

// ComputeInputDigest hashes the canonical (sorted) JSON of config+secrets.
// Values are included in the digest but never logged.
func ComputeInputDigest(config, secrets map[string]string) string {
	all := map[string]string{}
	for k, v := range config {
		all[k] = v
	}
	for k, v := range secrets {
		all[k] = v
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(all[k])
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
	}
	buf.WriteByte('}')
	h := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(h[:])
}

// Runner executes module operations with locking, timeouts, secret-file
// handling and output redaction.
type Runner struct {
	modulesDir string
	runDir     string
	lockDir    string
	log        *slog.Logger
	graph      *DepGraph
}

// NewRunner builds a Runner. graph may be nil for single-module runs.
func NewRunner(modulesDir, runDir, lockDir string, log *slog.Logger, graph *DepGraph) *Runner {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Runner{modulesDir: modulesDir, runDir: runDir, lockDir: lockDir, log: log, graph: graph}
}

// Run executes a fixed operation entrypoint.
func (r *Runner) Run(ctx context.Context, opts RunOptions) (*RunResult, error) {
	if !AllowedOperations[opts.Operation] {
		return nil, fmt.Errorf("%w: %q", ErrForbidden, opts.Operation)
	}
	if err := ValidateModuleID(opts.ModuleID); err != nil {
		return nil, fmt.Errorf("%w: %q", err, opts.ModuleID)
	}
	modDir := filepath.Join(r.modulesDir, opts.ModuleID)
	if !withinDir(r.modulesDir, modDir) {
		return nil, fmt.Errorf("%w: module dir escapes modules root", ErrInvalidModuleID)
	}
	mod, err := Load(modDir)
	if err != nil {
		return nil, err
	}
	op, ok := mod.Operations[opts.Operation]
	if !ok {
		return nil, fmt.Errorf("module %s: operation %q not declared", opts.ModuleID, opts.Operation)
	}
	digest := ComputeInputDigest(opts.Config, opts.Secrets)
	if opts.ExpectedDigest != "" && opts.ExpectedDigest != digest {
		return nil, ErrDigestMismatch
	}

	// Locks: node-level for concurrency=node, service-level otherwise.
	lockName := "node"
	if mod.Concurrency == "service" {
		lockName = "svc-" + opts.ModuleID
	}
	unlock, err := r.acquireLock(lockName)
	if err != nil {
		return nil, err
	}
	defer unlock()

	if err := os.MkdirAll(r.runDir, 0o700); err != nil {
		return nil, err
	}
	opID := randID()
	workDir := filepath.Join(r.runDir, opID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(workDir)

	// Build env: fixed internal vars + declared fields.
	env := []string{
		"SERVERCLI_MODULE=" + opts.ModuleID,
		"SERVERCLI_OPERATION=" + opts.Operation,
		"SERVERCLI_OPERATION_ID=" + opID,
		"SERVERCLI_INPUT_DIGEST=" + digest,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	env = append(env, opts.Env...)

	entry := filepath.Join(mod.Dir, op.Entry)
	fi, err := os.Stat(entry)
	if err != nil {
		return nil, fmt.Errorf("module %s: entry %s: %w", opts.ModuleID, op.Entry, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("module %s: entry %s is a directory", opts.ModuleID, op.Entry)
	}
	if op.Root && os.Geteuid() != 0 {
		return nil, fmt.Errorf("module %s: operation %s requires root", opts.ModuleID, opts.Operation)
	}

	var secretFiles []string
	// env delivery: single-line values via environment.
	if mod.Delivery == DeliveryEnv {
		for k, v := range opts.Config {
			env = append(env, fmt.Sprintf("SERVERCLI_CFG_%s=%s", envKey(k), v))
		}
		for k, v := range opts.Secrets {
			env = append(env, fmt.Sprintf("SERVERCLI_SEC_%s=%s", envKey(k), v))
		}
	} else {
		// file delivery: write declared fields as 0600 files under /run.
		for k, v := range opts.Secrets {
			p := filepath.Join(workDir, envKey(k))
			if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
				return nil, err
			}
			env = append(env, fmt.Sprintf("SERVERCLI_SEC_%s=%s", envKey(k), p))
			secretFiles = append(secretFiles, p)
		}
		for k, v := range opts.Config {
			p := filepath.Join(workDir, "cfg-"+envKey(k))
			if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
				return nil, err
			}
			env = append(env, fmt.Sprintf("SERVERCLI_CFG_%s=%s", envKey(k), p))
		}
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now().UTC()
	cmd := exec.CommandContext(ctx2, entry)
	cmd.Env = env
	cmd.Dir = mod.Dir
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	err = cmd.Run()
	completed := time.Now().UTC()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	// Redact secret AND config values (config may carry DSNs/auth URLs).
	allSensitive := map[string]string{}
	for k, v := range opts.Secrets {
		allSensitive[k] = v
	}
	for k, v := range opts.Config {
		allSensitive[k] = v
	}
	redacted := Redact(outBuf.String(), allSensitive)
	res := &RunResult{
		ModuleID:    opts.ModuleID,
		Operation:   opts.Operation,
		ExitCode:    exitCode,
		Digest:      digest,
		Output:      redacted,
		SecretFiles: secretFiles,
		StartedAt:   started,
		CompletedAt: completed,
	}
	r.log.Info("module run completed", "module", opts.ModuleID, "operation", opts.Operation,
		"exit", exitCode, "digest", digest[:12])
	return res, err
}

// Redact replaces secret values in output with *** (longest first to avoid
// partial overlaps).
func Redact(output string, secrets map[string]string) string {
	if len(secrets) == 0 {
		return output
	}
	keys := make([]string, 0, len(secrets))
	for _, v := range secrets {
		if len(v) >= 4 {
			keys = append(keys, v)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	red := output
	for _, v := range keys {
		red = strings.ReplaceAll(red, v, "***")
	}
	return red
}

func (r *Runner) acquireLock(name string) (func(), error) {
	if err := os.MkdirAll(r.lockDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(r.lockDir, name+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("modman: lock %s held by another operation", name)
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func envKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// withinDir reports whether target is inside root (no symlink escape for the
// module dir itself; entries are already validated to not contain "..").
func withinDir(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func randID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("op-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
