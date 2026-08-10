package ownership

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireFlock takes a non-blocking exclusive advisory lock on path (creating
// it 0600 with a 0700 parent directory). It returns an unlock closure. This is
// the same flock primitive used by initstate and modman, so ops, adopt and
// module runs all coordinate on one lock registry.
func acquireFlock(path string) (func(), error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ownership: mkdir lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("ownership: open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, path)
		}
		return nil, fmt.Errorf("ownership: flock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// Lock takes the service-level lock for one service
// (/run/servercli/operations/svc-<service>.lock). At most one operation
// (update/backup/restore/adopt) may run for the same service at a time.
// The op name is accepted for diagnostics/logging and does not change the lock
// path: all ops on a service share one lock.
func (s *Store) Lock(env, node, service, op string) (func(), error) {
	if err := validateService(service); err != nil {
		return nil, err
	}
	path := filepath.Join(s.lockDir, "svc-"+service+".lock")
	unlock, err := acquireFlock(path)
	if err != nil {
		return nil, fmt.Errorf("ownership: service %s (%s): %w", service, op, err)
	}
	return unlock, nil
}

// LockAll takes the node-level lock (/run/servercli/operations/node.lock).
// It serializes whole-node operations such as adopt rollback or node-wide
// maintenance.
func (s *Store) LockAll(env, node string) (func(), error) {
	path := filepath.Join(s.lockDir, "node.lock")
	unlock, err := acquireFlock(path)
	if err != nil {
		return nil, fmt.Errorf("ownership: node lock: %w", err)
	}
	return unlock, nil
}
