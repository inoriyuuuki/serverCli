package ops

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"servercli/internal/initstate"
)

// Restore restores one service from an explicit backup_id or recovery_set_id.
//
// Restore is a deliberately high-risk, explicit operation:
//   - it never auto-restores "latest" for a normal install or an empty
//     directory;
//   - the backup manifest signature and every file digest are verified before
//     any change;
//   - the user must confirm with --yes (RunOpts.Confirm) or an interactive TTY
//     answer "yes";
//   - the service must be owned by servercli and no other op may hold the
//     service lock.
func (o *Ops) Restore(ctx context.Context, service, backupID string, opts RunOpts) error {
	if strings.TrimSpace(backupID) == "" {
		return ErrRequireExplicitID
	}
	if err := o.confirmRestore(opts); err != nil {
		return err
	}
	cfg := o.Config

	dir, man, err := o.FindBackup(service, backupID)
	if err != nil {
		if errors.Is(err, ErrBackupNotFound) {
			// Recognized read-only as a legacy backup? Refuse loudly: a legacy
			// backup is missing metadata/verification and must never be
			// treated as a verified new-format backup.
			adapter := NewLegacyBackupAdapter(cfg.BackupDir)
			lb, lerr := adapter.Read(ctx, backupID)
			if lerr == nil {
				return fmt.Errorf("%w: backup %q is %s with missing metadata %v and verified=false; re-create it with `servercli ops backup` first",
					ErrLegacyBackup, backupID, lb.Format, lb.MissingMetadata)
			}
		}
		return err
	}

	// Verify the manifest signature before touching anything.
	if man.Signature == "" {
		return fmt.Errorf("%w: backup %s is unsigned", ErrUnverified, backupID)
	}
	if len(cfg.VerifyKeyPEM) == 0 {
		return fmt.Errorf("%w: configure the backup verification public key", ErrNoVerifyKey)
	}
	if err := man.Verify(cfg.VerifyKeyPEM); err != nil {
		return fmt.Errorf("ops: backup signature verification failed: %w", err)
	}
	// Verify every file digest + read-back before any change.
	if err := verifyFiles(man.Files, dir); err != nil {
		return fmt.Errorf("ops: pre-restore verification failed: %w", err)
	}

	// Ownership gate + service lock (same lock as update/backup/adopt).
	if err := o.Ownership.CanOperate(cfg.Environment, cfg.Node, service); err != nil {
		return err
	}
	unlock, err := o.Ownership.Lock(cfg.Environment, cfg.Node, service, "restore")
	if err != nil {
		return err
	}
	defer unlock()

	if !o.moduleDeclares(service, "restore") {
		return fmt.Errorf("ops: service %s declares no restore operation", service)
	}
	step := initstate.Step{ModuleID: service, Operation: "restore", StartedAt: time.Now().UTC()}
	rr, rerr := o.runModule(ctx, service, "restore", []string{
		"SERVERCLI_BACKUP_ID=" + man.BackupID,
		"SERVERCLI_RECOVERY_SET_ID=" + man.RecoverySetID,
		"SERVERCLI_BACKUP_DIR=" + dir,
	})
	if rr != nil {
		step.CompletedAt = rr.CompletedAt
		step.InputDigest = rr.Digest
	}
	if rerr != nil {
		step.Status = initstate.StepFailed
		step.ErrorType = initstate.ErrTypeModule
		o.record(ctx, step)
		return fmt.Errorf("ops: restore hook: %w", rerr)
	}
	if rr.ExitCode != 0 {
		step.Status = initstate.StepFailed
		step.ErrorType = initstate.ErrTypeModule
		o.record(ctx, step)
		return fmt.Errorf("ops: restore hook exited %d", rr.ExitCode)
	}
	step.Status = initstate.StepSucceeded
	o.record(ctx, step)
	return nil
}

// confirmRestore enforces explicit confirmation: RunOpts.Confirm (--yes) or an
// interactive "yes" read from RunOpts.In (a TTY). A non-interactive call
// without --yes is refused.
func (o *Ops) confirmRestore(opts RunOpts) error {
	if opts.Confirm {
		return nil
	}
	if opts.In == nil {
		return ErrRequireConfirm
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	fmt.Fprintln(out, "Restore is a high-risk operation. Type 'yes' to continue:")
	sc := bufio.NewScanner(opts.In)
	if !sc.Scan() {
		return ErrRequireConfirm
	}
	if strings.TrimSpace(sc.Text()) != "yes" {
		return ErrRequireConfirm
	}
	return nil
}
