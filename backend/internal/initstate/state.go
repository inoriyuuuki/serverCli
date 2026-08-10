// Package initstate implements the local, database-free initialization state
// machine persisted at /var/lib/servercli/bootstrap/state.json.
//
// Guarantees:
//   - no secret, DSN, auth URL, private key or token is ever recorded;
//   - writes are file-locked, checksummed and atomically renamed;
//   - concurrent init is rejected via an exclusive lock;
//   - a corrupted state file yields read-only diagnostics and never triggers
//     automatic re-initialization.
package initstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Overall state machine values.
const (
	StateNotInitialized = "not_initialized"
	StateInitializing   = "initializing"
	StateDegraded       = "degraded"
	StateCoreReady      = "core_ready"
	StateReady          = "ready"
	StateFailed         = "failed"
	StateBlocked        = "blocked"
)

// Step status values.
const (
	StepPending   = "pending"
	StepRunning   = "running"
	StepSucceeded = "succeeded"
	StepSkipped   = "skipped"
	StepFailed    = "failed"
	StepBlocked   = "blocked"
)

// Error classification for steps (stable strings).
const (
	ErrTypePreflight = "preflight"
	ErrTypeSignature = "signature"
	ErrTypeNetwork   = "network"
	ErrTypeModule    = "module"
	ErrTypeBlocked   = "blocked"
	ErrTypeManual    = "manual"
	ErrTypeUnknown   = "unknown"
)

// Step is one module lifecycle step in the init sequence.
type Step struct {
	OperationID     string    `json:"operation_id" yaml:"operation_id"`
	ModuleID        string    `json:"module_id" yaml:"module_id"`
	Operation       string    `json:"operation,omitempty" yaml:"operation,omitempty"` // install|update|backup|restore|adopt
	Attempt         int       `json:"attempt" yaml:"attempt"`
	ModuleVersion   string    `json:"module_version,omitempty" yaml:"module_version,omitempty"`
	BundleID        string    `json:"bundle_id,omitempty" yaml:"bundle_id,omitempty"`
	InputDigest     string    `json:"input_digest,omitempty" yaml:"input_digest,omitempty"`
	TargetVersion   string    `json:"target_version,omitempty" yaml:"target_version,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty" yaml:"started_at,omitempty"`
	CompletedAt     time.Time `json:"completed_at,omitempty" yaml:"completed_at,omitempty"`
	LastCommitPoint string    `json:"last_commit_point,omitempty" yaml:"last_commit_point,omitempty"`
	HealthEvidence  string    `json:"health_evidence,omitempty" yaml:"health_evidence,omitempty"`
	Retryable       bool      `json:"retryable" yaml:"retryable"`
	ResumeFrom      string    `json:"resume_from,omitempty" yaml:"resume_from,omitempty"`
	ErrorType       string    `json:"error_type,omitempty" yaml:"error_type,omitempty"`
	Status          string    `json:"status" yaml:"status"`
}

// State is the persisted init state.
type State struct {
	SchemaVersion int               `json:"schema_version"`
	Overall       string            `json:"overall"`
	OperationID   string            `json:"operation_id,omitempty"`
	BundleID      string            `json:"bundle_id,omitempty"`
	InputDigest   string            `json:"input_digest,omitempty"`
	TargetVersion string            `json:"target_version,omitempty"`
	StartedAt     time.Time         `json:"started_at,omitempty"`
	UpdatedAt     time.Time         `json:"updated_at,omitempty"`
	Steps         []Step            `json:"steps"`
	CommitPoints  map[string]string `json:"commit_points,omitempty"` // module_id -> evidence
	Checksum      string            `json:"-"`                       // not serialized
}

const stateSchemaVersion = 1

// New returns a fresh state for a new operation.
func New(operationID, bundleID, inputDigest, targetVersion string) *State {
	now := time.Now().UTC()
	return &State{
		SchemaVersion: stateSchemaVersion,
		Overall:       StateInitializing,
		OperationID:   operationID,
		BundleID:      bundleID,
		InputDigest:   inputDigest,
		TargetVersion: targetVersion,
		StartedAt:     now,
		UpdatedAt:     now,
		Steps:         []Step{},
		CommitPoints:  map[string]string{},
	}
}

// Step returns a pointer to a step by module id, or nil.
func (s *State) Step(moduleID string) *Step {
	for i := range s.Steps {
		if s.Steps[i].ModuleID == moduleID {
			return &s.Steps[i]
		}
	}
	return nil
}

// UpsertStep appends a step or updates it in place.
func (s *State) UpsertStep(st Step) {
	for i := range s.Steps {
		if s.Steps[i].ModuleID == st.ModuleID {
			s.Steps[i] = st
			return
		}
	}
	s.Steps = append(s.Steps, st)
}

// SetCommitPoint records a safe commit point for a module.
func (s *State) SetCommitPoint(moduleID, evidence string) {
	if s.CommitPoints == nil {
		s.CommitPoints = map[string]string{}
	}
	s.CommitPoints[moduleID] = evidence
}

// AllowedTransitions is the minimal guard for overall state transitions.
var AllowedTransitions = map[string][]string{
	StateNotInitialized: {StateInitializing, StateFailed, StateBlocked},
	StateInitializing:   {StateInitializing, StateDegraded, StateCoreReady, StateReady, StateFailed, StateBlocked},
	StateDegraded:       {StateInitializing, StateCoreReady, StateReady, StateFailed, StateBlocked, StateDegraded},
	StateCoreReady:      {StateInitializing, StateReady, StateDegraded, StateBlocked, StateFailed},
	StateReady:          {StateInitializing, StateDegraded, StateBlocked, StateFailed},
	StateFailed:         {StateInitializing, StateBlocked},
	StateBlocked:        {StateInitializing},
}

// CanTransition reports whether moving from -> to is allowed.
func CanTransition(from, to string) bool {
	for _, next := range AllowedTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// SetOverall transitions overall with validation. Invalid transitions are
// rejected so a bug cannot silently skip states.
func (s *State) SetOverall(to string) error {
	if !CanTransition(s.Overall, to) {
		return fmt.Errorf("invalid state transition %q -> %q", s.Overall, to)
	}
	s.Overall = to
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// ErrCorrupt is returned by Load when the state file fails checksum or parse.
var ErrCorrupt = errors.New("initstate: state file corrupt or checksum mismatch")

// lockFile is an exclusive advisory lock used to reject concurrent init.
type lockFile struct{ f *os.File }

func acquireLock(path string) (*lockFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrConcurrent
		}
		return nil, fmt.Errorf("flock: %w", err)
	}
	return &lockFile{f: f}, nil
}

func (l *lockFile) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	return l.f.Close()
}

// ErrConcurrent reports that another init operation holds the state lock.
var ErrConcurrent = errors.New("initstate: another init operation is running")

// Store wraps a state file plus lock, checksum and atomic persistence.
type Store struct {
	path  string
	lock  *lockFile
	state *State
}

// Open loads (or creates) the state store and holds the exclusive lock until
// Close. Use OpenReadOnly when only diagnostics are needed.
func Open(path string) (*Store, error) {
	lk, err := acquireLock(path + ".lock")
	if err != nil {
		return nil, err
	}
	st := &Store{path: path, lock: lk}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		st.state = freshState()
		return st, nil
	}
	s, err := loadFile(path)
	if err != nil {
		lk.Close()
		return nil, err
	}
	st.state = s
	return st, nil
}

// OpenReadOnly loads state without the lock; used for diagnostics only.
// It never writes and never auto-reinitializes.
func OpenReadOnly(path string) (*State, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return freshState(), nil
	}
	return loadFile(path)
}

func freshState() *State {
	s := New("", "", "", "")
	s.Overall = StateNotInitialized
	s.UpdatedAt = time.Now().UTC()
	return s
}

func loadFile(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	want := checksum(raw)
	got, err := os.ReadFile(path + ".sha256")
	if err != nil {
		return nil, fmt.Errorf("%w: missing checksum sidecar", ErrCorrupt)
	}
	if strings.TrimSpace(string(got)) != want {
		return nil, fmt.Errorf("%w: checksum mismatch", ErrCorrupt)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if s.SchemaVersion != stateSchemaVersion {
		return nil, fmt.Errorf("%w: unsupported schema_version %d", ErrCorrupt, s.SchemaVersion)
	}
	return &s, nil
}

func checksum(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// State returns the in-memory state (call Save to persist).
func (s *Store) State() *State { return s.state }

// Save persists the state atomically and updates the checksum sidecar.
func (s *Store) Save() error {
	if s.lock == nil {
		return errors.New("initstate: store not locked")
	}
	s.state.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("rename state: %w", err)
	}
	// Checksum sidecar (write after rename so a torn state never has a
	// matching checksum).
	side := checksum(raw)
	sideTmp, err := os.CreateTemp(dir, ".state-sha-*.tmp")
	if err != nil {
		return err
	}
	sideName := sideTmp.Name()
	defer os.Remove(sideName)
	if _, err := sideTmp.WriteString(side); err != nil {
		sideTmp.Close()
		return err
	}
	if err := sideTmp.Sync(); err != nil {
		sideTmp.Close()
		return err
	}
	sideTmp.Close()
	if err := os.Chmod(sideName, 0o600); err != nil {
		return err
	}
	return os.Rename(sideName, s.path+".sha256")
}

// Close releases the lock.
func (s *Store) Close() error {
	if s.lock != nil {
		return s.lock.Close()
	}
	return nil
}

// ReconcileAfterCrash inspects the persisted state after an interruption
// (power loss / kill). Steps stuck in running are marked failed with
// retryable=true and resume_from preserved; overall is derived from the
// surviving committed steps. This never re-runs anything and never
// auto-initializes a not_initialized state.
func ReconcileAfterCrash(s *State) {
	now := time.Now().UTC()
	anyRunning := false
	for i := range s.Steps {
		if s.Steps[i].Status == StepRunning {
			s.Steps[i].Status = StepFailed
			s.Steps[i].ErrorType = ErrTypeUnknown
			s.Steps[i].Retryable = true
			s.Steps[i].CompletedAt = now
			anyRunning = true
		}
	}
	if !anyRunning {
		return
	}
	// Derive overall from the step set.
	if s.Overall == StateInitializing || s.Overall == StateFailed {
		allOK := true
		anyFail := false
		anyBlock := false
		for _, st := range s.Steps {
			switch st.Status {
			case StepSucceeded, StepSkipped:
			case StepFailed:
				allOK = false
				anyFail = true
			case StepBlocked:
				allOK = false
				anyBlock = true
			default:
				allOK = false
			}
		}
		switch {
		case anyBlock:
			s.Overall = StateBlocked
		case anyFail:
			s.Overall = StateFailed
		case allOK && len(s.Steps) > 0:
			s.Overall = StateDegraded // requires re-verification before ready
		}
	}
}
