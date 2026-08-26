package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"servercli/internal/model"
	"servercli/internal/secret"
)

// validBootstrapStatuses is the allowlist of states a node may report for a
// bootstrap session. It mirrors every BootstrapStatus* constant in the model
// so the API can never be driven into an unknown state.
var validBootstrapStatuses = map[string]bool{
	model.BootstrapStatusCreated:              true,
	model.BootstrapStatusRepositorySyncing:    true,
	model.BootstrapStatusRepositoryVerified:   true,
	model.BootstrapStatusXrayInstalling:       true,
	model.BootstrapStatusProxyChecking:        true,
	model.BootstrapStatusProxyReady:           true,
	model.BootstrapStatusAgentDownloading:     true,
	model.BootstrapStatusAgentVerifying:       true,
	model.BootstrapStatusAgentInstalling:      true,
	model.BootstrapStatusEnrollmentPending:    true,
	model.BootstrapStatusNodeOnline:           true,
	model.BootstrapStatusCompleted:            true,
	model.BootstrapStatusRepositorySyncFailed: true,
	model.BootstrapStatusManifestInvalid:      true,
	model.BootstrapStatusSignatureFailed:      true,
	model.BootstrapStatusXrayFailed:           true,
	model.BootstrapStatusProxyFailed:          true,
	model.BootstrapStatusAgentDownloadFailed:  true,
	model.BootstrapStatusAgentVerifyFailed:    true,
	model.BootstrapStatusAgentStartFailed:     true,
	model.BootstrapStatusEnrollmentFailed:     true,
	model.BootstrapStatusExpired:              true,
	model.BootstrapStatusCancelled:            true,
}

// bootstrapReportRedactor redacts node-supplied bootstrap messages before
// they reach logs. Messages are never persisted or returned.
var bootstrapReportRedactor = secret.NewRedactor()

// ReportBootstrapStatus updates a bootstrap session from the node-side
// bootstrap pipeline. Authentication is the one-time session token: only its
// SHA-256 hash is stored, so the plaintext never reaches the database.
//
//   - unknown token                     -> ErrNotFound
//   - revoked / expired / cancelled     -> ErrForbidden
//   - state not in the allowlist        -> ErrBadRequest
//
// The optional message is redacted and only used for logging; it is never
// stored on the session, returned to the caller, or written to audit events.
func (s *DeploymentService) ReportBootstrapStatus(ctx context.Context, token, state, message string) (*model.BootstrapSession, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: session_token is required", ErrBadRequest)
	}
	sess, err := s.store.BootstrapSessionByTokenHash(ctx, sha256Hex([]byte(token)))
	if err != nil {
		return nil, ErrNotFound
	}
	now := time.Now().UTC()
	if sess.RevokedAt != nil ||
		sess.Status == model.BootstrapStatusCancelled ||
		sess.Status == model.BootstrapStatusExpired ||
		sess.ExpiresAt.Before(now) {
		s.auditDeployment(ctx, model.ActorNode, sess.NodeID, "deployment.bootstrap.report", ResultFailure, map[string]any{
			"node_id": sess.NodeID, "action": "deployment.bootstrap.report",
		})
		return nil, ErrForbidden
	}
	if !validBootstrapStatuses[state] {
		return nil, fmt.Errorf("%w: invalid bootstrap status %q", ErrBadRequest, state)
	}
	sess.Status = state
	if err := s.store.UpdateBootstrapSession(ctx, sess); err != nil {
		s.auditDeployment(ctx, model.ActorNode, sess.NodeID, "deployment.bootstrap.report", ResultFailure, map[string]any{
			"node_id": sess.NodeID, "action": "deployment.bootstrap.report",
		})
		return nil, err
	}
	if strings.TrimSpace(message) != "" {
		// Redacted, bounded: keep verbose node-side messages out of the
		// database and audit trail; only a redacted snippet reaches the log.
		s.log.Info("deployment bootstrap report",
			"session_id", sess.ID, "node_id", sess.NodeID, "state", state,
			"message", bootstrapReportRedactor.RedactString(truncateBootstrapMessage(message)))
	}
	s.auditDeployment(ctx, model.ActorNode, sess.NodeID, "deployment.bootstrap.report", ResultSuccess, map[string]any{
		"node_id": sess.NodeID, "action": "deployment.bootstrap.report", "result": ResultSuccess,
	})
	return sess, nil
}

// truncateBootstrapMessage bounds an arbitrary node-supplied message to a
// single-line snippet before redaction.
func truncateBootstrapMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) > 512 {
		msg = msg[:512]
	}
	msg = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, msg)
	return strings.TrimSpace(msg)
}
