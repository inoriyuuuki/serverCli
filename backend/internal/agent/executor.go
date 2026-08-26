package agent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"servercli/internal/config"
)

// TaskPayload mirrors the control plane's signed task payload.
type TaskPayload struct {
	TaskID         string          `json:"task_id"`
	NodeID         string          `json:"node_id"`
	CommandID      string          `json:"command_id"`
	CommandVersion string          `json:"command_version"`
	Arguments      json.RawMessage `json:"arguments"`
	CreatedAt      time.Time       `json:"created_at"`
	NotBefore      time.Time       `json:"not_before"`
	Deadline       time.Time       `json:"deadline"`
	IdempotencyKey string          `json:"idempotency_key"`
	Attempt        int             `json:"attempt"`
	TimeoutSeconds int             `json:"timeout_seconds"`
	MaxOutputBytes int64           `json:"max_output_bytes"`
	PayloadHash    string          `json:"payload_hash"`
	Signature      string          `json:"signature"`
}

// Event is a task event reported to the control plane.
type Event struct {
	EventType  string    `json:"event_type"`
	Sequence   int64     `json:"sequence"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurred_at"`
}

// Result is the final task result.
type Result struct {
	Status       string    `json:"status"`
	StdoutText   string    `json:"stdout_text"`
	StderrText   string    `json:"stderr_text"`
	ExitCode     *int      `json:"exit_code"`
	ErrorCode    string    `json:"error_code"`
	ErrorMessage string    `json:"error_message"`
	Truncated    bool      `json:"truncated"`
	FinishedAt   time.Time `json:"finished_at"`
	// SummaryJSON carries a machine-readable operation summary (e.g. the
	// deployment backup result) that the control plane persists separately.
	SummaryJSON string `json:"summary_json,omitempty"`
}

// TaskReporter sends events and results for a task.
type TaskReporter interface {
	SendEvent(taskID string, ev Event) error
	SendResult(taskID string, res Result) error
}

// VerifyPayload checks the task signature and payload hash.
func VerifyPayload(p *TaskPayload, credential string) error {
	clone := *p
	clone.PayloadHash = ""
	clone.Signature = ""
	raw, err := json.Marshal(&clone)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if !constantTimeHex(hex.EncodeToString(sum[:]), p.PayloadHash) {
		return fmt.Errorf("payload hash mismatch")
	}
	sig := hmacSign(credential, "task:"+p.TaskID+":"+p.PayloadHash)
	if !constantTimeHex(sig, p.Signature) {
		return fmt.Errorf("payload signature mismatch")
	}
	return nil
}

func hmacSign(key, msg string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

func constantTimeHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// Executor runs tasks with strict argv semantics, timeouts and output limits.
type Executor struct {
	repo         TaskReporter
	log          *slog.Logger
	deployRunner *DeploymentRunner
	mu           sync.Mutex
	running      map[string]context.CancelFunc
	cancelled    map[string]bool
}

// SetDeploymentRunner injects the built-in deployment.* runner. It is
// optional: commands without a runner configured fail with a clear error.
func (e *Executor) SetDeploymentRunner(r *DeploymentRunner) {
	e.deployRunner = r
}

// NewExecutor builds an executor.
func NewExecutor(repo TaskReporter, log *slog.Logger) *Executor {
	return &Executor{repo: repo, log: log, running: map[string]context.CancelFunc{}, cancelled: map[string]bool{}}
}

// newExecutorWithDeployment builds an executor pre-wired with the built-in
// deployment.* runner. It is used by the node agent; NewExecutor keeps its
// signature for callers that do not need deployment commands.
func newExecutorWithDeployment(cfg *config.Config, repo TaskReporter, log *slog.Logger) *Executor {
	e := NewExecutor(repo, log)
	e.SetDeploymentRunner(NewDeploymentRunner(cfg, log))
	return e
}

// MarkCancelled flags a task for termination (from control plane).
func (e *Executor) MarkCancelled(taskID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cancelled[taskID] = true
	if cancel, ok := e.running[taskID]; ok {
		cancel()
	}
}

func (e *Executor) isCancelled(taskID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cancelled[taskID]
}

// Execute runs a task to completion, streaming events.
func (e *Executor) Execute(ctx context.Context, payload *TaskPayload, cmd CommandEntry) {
	taskID := payload.TaskID
	log := e.log.With("task_id", taskID, "command", cmd.CommandID)
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(payload.TimeoutSeconds)*time.Second)
	e.mu.Lock()
	e.running[taskID] = cancel
	e.cancelled[taskID] = false
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, taskID)
		delete(e.cancelled, taskID)
		e.mu.Unlock()
		cancel()
	}()

	var seqMu sync.Mutex
	seq := int64(0)
	send := func(ev Event) {
		seqMu.Lock()
		seq++
		ev.Sequence = seq
		seqMu.Unlock()
		ev.OccurredAt = time.Now().UTC()
		if err := e.repo.SendEvent(taskID, ev); err != nil {
			log.Warn("event send failed", "event", ev.EventType, "error", err)
		}
	}
	send(Event{EventType: "accepted", Message: "task accepted"})
	send(Event{EventType: "started", Message: "task started"})

	// deployment.* commands are handled by the built-in DeploymentRunner
	// (fixed argument whitelist, safe extraction, sudo wrapper hooks) instead
	// of the generic exec path.
	if strings.HasPrefix(cmd.CommandID, "deployment.") {
		if e.deployRunner == nil {
			e.finish(taskID, send, Result{Status: "failed", ErrorCode: "DEPLOY_RUNNER_NOT_CONFIGURED", ErrorMessage: "deployment runner not configured"})
			return
		}
		res, err := e.deployRunner.Run(runCtx, payload)
		if err != nil {
			e.finish(taskID, send, Result{Status: "failed", ErrorCode: "DEPLOY_RUN_ERROR", ErrorMessage: err.Error(), FinishedAt: time.Now().UTC()})
			return
		}
		if res == nil {
			res = &Result{Status: "succeeded", FinishedAt: time.Now().UTC()}
		}
		e.finish(taskID, send, *res)
		return
	}

	args := buildArgs(cmd, payload.Arguments)

	proc := exec.Command(cmd.ExecutablePath, args...)
	proc.Dir = filepath.Dir(cmd.ExecutablePath)
	proc.Env = minimalEnv()
	// Run the child in its own process group so cancellation can terminate
	// the whole group (SIGTERM, then SIGKILL) and no descendant survives.
	proc.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := proc.StdoutPipe()
	if err != nil {
		e.finish(taskID, send, Result{Status: "failed", ErrorCode: "EXEC_SETUP", ErrorMessage: "stdout pipe: " + err.Error()})
		return
	}
	stderr, err := proc.StderrPipe()
	if err != nil {
		e.finish(taskID, send, Result{Status: "failed", ErrorCode: "EXEC_SETUP", ErrorMessage: "stderr pipe: " + err.Error()})
		return
	}
	maxOut := cmd.MaxOutputBytes
	if payload.MaxOutputBytes > 0 {
		maxOut = payload.MaxOutputBytes
	}
	var stdoutBuf, stderrBuf limitedBuffer
	stdoutBuf.max = maxOut
	stderrBuf.max = maxOut
	truncated := false

	stdoutCh := make(chan string, 256)
	stderrCh := make(chan string, 256)
	go pipeStream(stdout, stdoutCh)
	go pipeStream(stderr, stderrCh)

	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		for stdoutCh != nil || stderrCh != nil {
			select {
			case s, ok := <-stdoutCh:
				if !ok {
					stdoutCh = nil
					continue
				}
				stdoutBuf.write([]byte(s))
				if stdoutBuf.truncated {
					truncated = true
				}
				send(Event{EventType: "stdout_chunk", Message: s})
			case s, ok := <-stderrCh:
				if !ok {
					stderrCh = nil
					continue
				}
				stderrBuf.write([]byte(s))
				if stderrBuf.truncated {
					truncated = true
				}
				send(Event{EventType: "stderr_chunk", Message: s})
			case <-runCtx.Done():
				return
			}
		}
	}()

	// Watch for control-plane cancellation signals. Exits when the process
	// finishes so completion is not delayed until the timeout.
	procDone := make(chan struct{})
	var procDoneOnce sync.Once
	closeProcDone := func() { procDoneOnce.Do(func() { close(procDone) }) }
	defer closeProcDone()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if e.isCancelled(taskID) {
					terminateProcessGroup(proc)
					return
				}
			case <-procDone:
				return
			case <-runCtx.Done():
				terminateProcessGroup(proc)
				return
			}
		}
	}()

	if err := proc.Start(); err != nil {
		e.finish(taskID, send, Result{Status: "failed", ErrorCode: "EXEC_START", ErrorMessage: err.Error()})
		<-sendDone
		return
	}
	waitErr := proc.Wait()
	// 进程已结束：立即通知 watch 协程退出，否则 `<-watchDone` 会一直等到任务超时
	// （runCtx.Done），导致每个任务都要跑满 timeout_seconds 才算完成。
	closeProcDone()
	<-sendDone
	<-watchDone

	exitCode := 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
			status := "failed"
			if exitCode == 0 {
				status = "succeeded"
			}
			e.finish(taskID, send, Result{
				Status: status, ExitCode: &exitCode, StdoutText: stdoutBuf.String(), StderrText: stderrBuf.String(),
				Truncated: truncated, FinishedAt: time.Now().UTC(),
			})
			return
		}
		status := "timed_out"
		code := "TIMED_OUT"
		if e.isCancelled(taskID) {
			status = "cancelled"
			code = "CANCELLED"
		}
		e.finish(taskID, send, Result{
			Status: status, ExitCode: &exitCode, ErrorCode: code, ErrorMessage: waitErr.Error(),
			StdoutText: stdoutBuf.String(), StderrText: stderrBuf.String(), Truncated: truncated,
			FinishedAt: time.Now().UTC(),
		})
		return
	}
	e.finish(taskID, send, Result{
		Status: "succeeded", ExitCode: &exitCode, StdoutText: stdoutBuf.String(), StderrText: stderrBuf.String(),
		Truncated: truncated, FinishedAt: time.Now().UTC(),
	})
}

func pipeStream(r io.Reader, ch chan<- string) {
	defer close(ch)
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			ch <- string(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// terminateProcessGroup sends SIGTERM to the process group led by cmd and
// escalates to SIGKILL after a short grace period, ensuring the child and any
// descendants it spawned exit together. It is safe to call after the process
// already exited (Kill returns ESRCH, which is ignored).
func terminateProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	time.AfterFunc(3*time.Second, func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	})
}

func (e *Executor) finish(taskID string, send func(Event), res Result) {
	if res.FinishedAt.IsZero() {
		res.FinishedAt = time.Now().UTC()
	}
	if res.Status == "succeeded" {
		send(Event{EventType: "completed", Message: "task completed"})
	} else {
		send(Event{EventType: res.Status, Message: res.ErrorMessage})
	}
	if err := e.repo.SendResult(taskID, res); err != nil {
		e.log.Warn("result send failed", "task_id", taskID, "error", err)
	}
}

func buildArgs(cmd CommandEntry, raw json.RawMessage) []string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	order := map[string]int{}
	if cmd.ParameterSchemaJSON != "" && cmd.ParameterSchemaJSON != "{}" {
		if idx, ok := schemaPropertyOrder([]byte(cmd.ParameterSchemaJSON)); ok {
			order = idx
		}
	}
	type kv struct {
		key string
		val any
	}
	pairs := make([]kv, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, kv{k, v})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		oi, oki := order[pairs[i].key]
		oj, okj := order[pairs[j].key]
		if oki && okj {
			return oi < oj
		}
		if oki {
			return true
		}
		if okj {
			return false
		}
		return pairs[i].key < pairs[j].key
	})
	args := make([]string, 0, len(pairs))
	for _, p := range pairs {
		args = append(args, argString(p.val))
	}
	return args
}

func argString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%v", t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func minimalEnv() []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin:/usr/local/bin"
	}
	return []string{"PATH=" + path, "LANG=C.UTF-8", "TMPDIR=" + os.TempDir()}
}

// limitedBuffer caps collected output.
type limitedBuffer struct {
	buf       bytes.Buffer
	max       int64
	truncated bool
}

func (b *limitedBuffer) write(p []byte) {
	if b.max <= 0 {
		b.max = 262144
	}
	remaining := b.max - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		return
	}
	if int64(len(p)) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return
	}
	b.buf.Write(p)
}

func (b *limitedBuffer) String() string { return b.buf.String() }

func sha256SumBytes(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// schemaPropertyOrder extracts the "properties" keys of a JSON schema in
// declaration order. Argv for commands is positional, so the order must be
// deterministic across runs (Go maps do not preserve JSON object order).
func schemaPropertyOrder(data []byte) (map[string]int, bool) {
	order := map[string]int{}
	dec := json.NewDecoder(bytes.NewReader(data))
	if _, err := dec.Token(); err != nil { // opening '{'
		return nil, false
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, false
		}
		ks, _ := key.(string)
		if ks == "properties" {
			if err := readSchemaProperties(dec, order); err != nil {
				return nil, false
			}
			return order, len(order) > 0
		}
		if err := skipJSONValue(dec); err != nil {
			return nil, false
		}
	}
	return nil, false
}

func readSchemaProperties(dec *json.Decoder, order map[string]int) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil // "properties" is not an object; ignore
	}
	i := 0
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return err
		}
		if s, ok := k.(string); ok {
			order[s] = i
			i++
		}
		if err := skipJSONValue(dec); err != nil {
			return err
		}
	}
	_, err = dec.Token() // consume '}'
	return err
}

// skipJSONValue consumes one complete JSON value from the decoder.
func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil // scalar
	}
	switch d {
	case '{':
		for dec.More() {
			if _, err := dec.Token(); err != nil { // key
				return err
			}
			if err := skipJSONValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token() // '}'
		return err
	case '[':
		for dec.More() {
			if err := skipJSONValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token() // ']'
		return err
	}
	return nil
}
