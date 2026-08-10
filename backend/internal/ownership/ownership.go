// Package ownership implements the per-service ownership state machine that
// gates every ServerCLI operation (install/update/backup/restore/adopt).
//
// Each environment+node+service triple has exactly one owner:
//
//	legacy-init -> migration-frozen -> adopting -> servercli
//	adopting --(adopt failure)--> legacy-init   (original data untouched)
//	servercli -> rollback-pending
//
// Only owner=servercli permits install/update/backup/restore. While a service
// is "adopting" every operation is blocked so a half-migrated service can
// never be mutated by two actors at once. The state is persisted as a JSON
// document (default /etc/servercli/private/ownership.json) with atomic
// write + fsync + 0600 permissions; concurrent writers are serialized with an
// advisory flock on <path>.lock.
//
// MAC addresses are never used for identity, roles, install authorization,
// auto-approval or secret grants: see LegacyMACInfo.
package ownership

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"servercli/internal/bootstrap"
)

// Owner state constants. These strings are part of the public contract and
// are persisted verbatim.
const (
	OwnerLegacyInit      = "legacy-init"
	OwnerMigrationFrozen = "migration-frozen"
	OwnerAdopting        = "adopting"
	OwnerServerCLI       = "servercli"
	OwnerRollbackPending = "rollback-pending"
)

// DefaultStatePath is the production ownership document location.
const DefaultStatePath = bootstrap.DirEtcPrivate + "/ownership.json"

// DefaultFreezeWindow is how long a legacy freeze marker stays valid.
const DefaultFreezeWindow = 30 * time.Minute

// Sentinel errors.
var (
	ErrNoOwnership       = errors.New("ownership: no ownership record for service")
	ErrInvalidTransition = errors.New("ownership: invalid owner transition")
	ErrBlocked           = errors.New("ownership: operation blocked by owner state")
	ErrLocked            = errors.New("ownership: lock held by another operation")
	ErrCorrupt           = errors.New("ownership: ownership file corrupt")
	ErrSymlink           = errors.New("ownership: refusing symlink")
	ErrInvalidService    = errors.New("ownership: invalid service name")
)

// allowedTransitions is the complete legal owner state machine.
var allowedTransitions = map[string][]string{
	OwnerLegacyInit:      {OwnerMigrationFrozen},
	OwnerMigrationFrozen: {OwnerAdopting},
	OwnerAdopting:        {OwnerServerCLI, OwnerLegacyInit}, // failure recovery
	OwnerServerCLI:       {OwnerRollbackPending},
	OwnerRollbackPending: {}, // requires manual re-adopt; no automatic transition
}

// Ownership is the persisted owner record for one service.
type Ownership struct {
	Environment      string    `json:"environment"`
	Node             string    `json:"node"`
	Service          string    `json:"service"`
	Owner            string    `json:"owner"`
	AdoptStartedAt   time.Time `json:"adopt_started_at,omitempty"`
	AdoptCompletedAt time.Time `json:"adopt_completed_at,omitempty"`
	ConfigDigest     string    `json:"config_digest,omitempty"`
	SecretRef        string    `json:"secret_ref,omitempty"`
	LockedUntil      time.Time `json:"locked_until,omitempty"`
}

// LegacyMACInfo is migration metadata ONLY (MAC -> node hint for the adopt
// plan and anomaly diagnostics). It must never be used for identity, roles,
// install authorization, enrollment, auto-approval or secret grants.
type LegacyMACInfo struct {
	MAC  string `json:"mac"`
	Node string `json:"node"`
}

// Store is an in-memory view of the ownership document. Load reads the file,
// Save persists it atomically (fsync + 0600), Close releases resources (there
// is no persistent lock held between calls; callers must Save to persist).
type Store struct {
	path    string
	lockDir string
	data    map[string]Ownership
	mu      sync.Mutex

	// Discover is an optional read-only discovery hook used by AdoptPlan.
	Discover DiscoveryFunc
	// LegacyPaths are probed (read-only) during adopt discovery.
	LegacyPaths []string
	// DataDir is an optional service data directory probed during discovery.
	DataDir string
	// FreezeWindow controls how long FreezeLegacy locks the service.
	FreezeWindow time.Duration
}

// DiscoveryFunc performs read-only discovery of a legacy service. It must not
// write to the system: the plan phase never mutates anything.
type DiscoveryFunc func(ctx context.Context, env, node, service string) (*LegacyDiscovery, error)

// LegacyDiscovery is the read-only result of probing a legacy service.
type LegacyDiscovery struct {
	LegacyEntrypoints []string `json:"legacy_entrypoints,omitempty"`
	ServiceVersion    string   `json:"service_version,omitempty"`
	HasDataDir        bool     `json:"has_data_dir,omitempty"`
}

// AdoptStep is one step of the planned adopt sequence.
type AdoptStep struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// AdoptPlanResult is the diff plan produced by AdoptPlan. Producing a plan
// never writes to the system.
type AdoptPlanResult struct {
	Environment    string         `json:"environment"`
	Node           string         `json:"node"`
	Service        string         `json:"service"`
	Owner          string         `json:"owner"`
	AlreadyAdopted bool           `json:"already_adopted"`
	Discovered     *LegacyDiscovery `json:"discovered,omitempty"`
	Steps          []AdoptStep    `json:"steps,omitempty"`
}

// NewStore returns a store backed by path. The in-memory state is empty until
// Load is called. The service-lock directory defaults to /run/servercli/operations.
func NewStore(path string) *Store {
	return &Store{
		path:         path,
		lockDir:      bootstrap.DirRunOperations,
		data:         map[string]Ownership{},
		FreezeWindow: DefaultFreezeWindow,
	}
}

// Path returns the store file path.
func (s *Store) Path() string { return s.path }

// SetLockDir overrides the directory used for flock-based service/node locks
// (production default /run/servercli/operations). It returns the store so
// tests can chain configuration.
func (s *Store) SetLockDir(dir string) *Store {
	s.lockDir = dir
	return s
}

func key(env, node, service string) string {
	return env + "\x00" + node + "\x00" + service
}

func validateService(service string) error {
	if service == "" || service == "." || service == ".." || strings.ContainsAny(service, "/\\") {
		return fmt.Errorf("%w: %q", ErrInvalidService, service)
	}
	if len(service) > 128 {
		return fmt.Errorf("%w: %q too long", ErrInvalidService, service)
	}
	return nil
}

func validateNames(env, node, service string) error {
	if env == "" || node == "" {
		return errors.New("ownership: environment and node must be non-empty")
	}
	return validateService(service)
}

// Load reads the JSON document into memory. A missing file yields an empty
// store; a corrupt file returns ErrCorrupt and is never auto-repaired.
// Symlinked state files are refused.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = map[string]Ownership{}
	if fi, err := os.Lstat(s.path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, s.path)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%w: %s is not a regular file", ErrCorrupt, s.path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ownership: read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrCorrupt, s.path, err)
	}
	return nil
}

// Save persists the whole document atomically: temp file in the same
// directory, fsync, chmod 0600, rename, then fsync the directory. Writers are
// serialized with a flock on <path>.lock so concurrent processes cannot lose
// updates.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("ownership: marshal: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("ownership: mkdir %s: %w", dir, err)
	}
	unlock, err := acquireFlock(s.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	if fi, err := os.Lstat(s.path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, s.path)
	}

	tmp, err := os.CreateTemp(dir, ".ownership-*.tmp")
	if err != nil {
		return fmt.Errorf("ownership: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("ownership: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("ownership: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("ownership: rename: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Close releases resources. The store holds no persistent lock, so Close is a
// no-op; call Save to persist any in-memory changes.
func (s *Store) Close() error { return nil }

// Get returns the ownership record for a service.
func (s *Store) Get(env, node, service string) (Ownership, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.data[key(env, node, service)]
	return o, ok
}

// Set records ownership (in memory). Call Save to persist.
func (s *Store) Set(env, node, service string, o Ownership) error {
	if err := validateNames(env, node, service); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o.Environment, o.Node, o.Service = env, node, service
	s.data[key(env, node, service)] = o
	return nil
}

// Transition validates and applies an owner state change in memory. Call Save
// to persist. Legal transitions are defined by allowedTransitions.
func (s *Store) Transition(env, node, service, from, to string) error {
	if err := validateNames(env, node, service); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transitionLocked(env, node, service, from, to)
}

func (s *Store) transitionLocked(env, node, service, from, to string) error {
	k := key(env, node, service)
	o, ok := s.data[k]
	if !ok {
		return fmt.Errorf("%w: %s/%s/%s", ErrNoOwnership, env, node, service)
	}
	if o.Owner != from {
		return fmt.Errorf("%w: %s is %q, cannot %q -> %q", ErrInvalidTransition, service, o.Owner, from, to)
	}
	next, ok := allowedTransitions[from]
	if !ok {
		return fmt.Errorf("%w: no transitions from %q", ErrInvalidTransition, from)
	}
	legal := false
	for _, n := range next {
		if n == to {
			legal = true
			break
		}
	}
	if !legal {
		return fmt.Errorf("%w: %q -> %q", ErrInvalidTransition, from, to)
	}
	now := time.Now().UTC()
	o.Owner = to
	switch to {
	case OwnerAdopting:
		o.AdoptStartedAt = now
	case OwnerServerCLI:
		if o.AdoptStartedAt.IsZero() {
			o.AdoptStartedAt = now
		}
		o.AdoptCompletedAt = now
	case OwnerLegacyInit: // adopt failure recovery: original data untouched
		o.AdoptStartedAt = time.Time{}
		o.AdoptCompletedAt = time.Time{}
	}
	s.data[k] = o
	return nil
}

// CanOperate reports whether install/update/backup/restore may run for a
// service. Only owner==servercli is allowed; adopting is blocked; services
// without an ownership record are blocked (never silently operable).
func (s *Store) CanOperate(env, node, service string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.data[key(env, node, service)]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoOwnership, service)
	}
	switch o.Owner {
	case OwnerServerCLI:
		return nil
	case OwnerAdopting:
		return fmt.Errorf("%w: service %s is being adopted", ErrBlocked, service)
	default:
		return fmt.Errorf("%w: service %s owner is %q, not servercli", ErrBlocked, service, o.Owner)
	}
}

// Services returns the distinct service names present in the store, sorted.
func (s *Store) Services() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for k := range s.data {
		parts := strings.Split(k, "\x00")
		if len(parts) == 3 {
			seen[parts[2]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for svc := range seen {
		out = append(out, svc)
	}
	sort.Strings(out)
	return out
}

func (s *Store) discover(ctx context.Context, env, node, service string) (*LegacyDiscovery, error) {
	if s.Discover != nil {
		return s.Discover(ctx, env, node, service)
	}
	paths := s.LegacyPaths
	if len(paths) == 0 {
		paths = []string{
			"/home/init/centos/update.sh",
			"/home/init/centos/backup.sh",
			"/opt/servercli/update.sh",
			"/opt/servercli/backup.sh",
		}
	}
	d := &LegacyDiscovery{}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			d.LegacyEntrypoints = append(d.LegacyEntrypoints, p)
		}
	}
	if s.DataDir != "" {
		if fi, err := os.Stat(s.DataDir); err == nil && fi.IsDir() {
			d.HasDataDir = true
		}
	}
	return d, nil
}

// AdoptPlan performs read-only discovery of a service and produces the adopt
// diff plan. The plan phase never writes to the system and never transitions
// state; apply it with FreezeLegacy / MarkServerCLI / module operations.
func (s *Store) AdoptPlan(ctx context.Context, env, node, service string) (*AdoptPlanResult, error) {
	if err := validateNames(env, node, service); err != nil {
		return nil, err
	}
	o, ok := s.Get(env, node, service)
	if ok && o.Owner == OwnerServerCLI {
		return &AdoptPlanResult{
			Environment: env, Node: node, Service: service,
			Owner: o.Owner, AlreadyAdopted: true,
		}, nil
	}
	disc, err := s.discover(ctx, env, node, service)
	if err != nil {
		return nil, fmt.Errorf("ownership: adopt plan discovery: %w", err)
	}
	owner := OwnerLegacyInit
	if ok {
		owner = o.Owner
	}
	steps := []AdoptStep{
		{Name: "freeze_legacy", Description: "Freeze legacy cron/timer/install/update/backup/restore entrypoints"},
		{Name: "wait_legacy_jobs", Description: "Wait for in-flight legacy jobs to finish"},
		{Name: "migration_backup", Description: "Create a migration backup before any change"},
		{Name: "verify_assets", Description: "Verify directories, containers, ports, database and version"},
		{Name: "write_ownership", Description: "Write ownership metadata for the service"},
		{Name: "import_config", Description: "Import configuration and secret references"},
		{Name: "health_check", Description: "Run health check after import"},
		{Name: "switch_owner", Description: "Switch owner to servercli and disable legacy entrypoints"},
	}
	return &AdoptPlanResult{
		Environment: env, Node: node, Service: service,
		Owner: owner, Discovered: disc, Steps: steps,
	}, nil
}

func (s *Store) freezeMarkerPath(service string) string {
	return filepath.Join(filepath.Dir(s.path), "frozen-"+service+".marker")
}

// FreezeLegacy transitions legacy-init -> migration-frozen, records
// LockedUntil and writes a freeze marker file next to the state document
// (0600). It persists immediately. The freeze marker exists so external
// tooling can detect that a legacy entrypoint must stay disabled.
func (s *Store) FreezeLegacy(env, node, service string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.transitionLocked(env, node, service, OwnerLegacyInit, OwnerMigrationFrozen); err != nil {
		return err
	}
	k := key(env, node, service)
	o := s.data[k]
	window := s.FreezeWindow
	if window <= 0 {
		window = DefaultFreezeWindow
	}
	o.LockedUntil = time.Now().UTC().Add(window)
	s.data[k] = o

	rollback := func() {
		o.Owner = OwnerLegacyInit
		o.LockedUntil = time.Time{}
		s.data[k] = o
	}

	marker := s.freezeMarkerPath(service)
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		rollback()
		return err
	}
	body, _ := json.MarshalIndent(map[string]any{
		"environment":  env,
		"node":         node,
		"service":      service,
		"owner":        OwnerMigrationFrozen,
		"locked_until": o.LockedUntil,
	}, "", "  ")
	if err := os.WriteFile(marker, body, 0o600); err != nil {
		rollback()
		return fmt.Errorf("ownership: write freeze marker: %w", err)
	}
	return s.saveLocked()
}

// MarkServerCLI completes a successful adopt: requires owner==adopting,
// records the config digest and secret reference, transitions adopting ->
// servercli and persists. The freeze marker is removed on success.
func (s *Store) MarkServerCLI(env, node, service, configDigest, secretRef string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(env, node, service)
	o, ok := s.data[k]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoOwnership, service)
	}
	if o.Owner != OwnerAdopting {
		return fmt.Errorf("%w: cannot mark %s servercli while owner is %q", ErrInvalidTransition, service, o.Owner)
	}
	o.ConfigDigest = configDigest
	o.SecretRef = secretRef
	s.data[k] = o
	if err := s.transitionLocked(env, node, service, OwnerAdopting, OwnerServerCLI); err != nil {
		return err
	}
	_ = os.Remove(s.freezeMarkerPath(service))
	return s.saveLocked()
}

// RollbackAdopt reverses a failed adopt: requires owner==adopting, transitions
// back to legacy-init (original data is never moved/deleted/rebuilt) and
// persists.
func (s *Store) RollbackAdopt(env, node, service string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.transitionLocked(env, node, service, OwnerAdopting, OwnerLegacyInit); err != nil {
		return err
	}
	return s.saveLocked()
}
