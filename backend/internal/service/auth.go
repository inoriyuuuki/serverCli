package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"servercli/internal/model"
	"servercli/internal/security"
	"servercli/internal/store"
)

// SessionTTL is how long an admin session lasts.
const SessionTTL = 24 * time.Hour

// AuthService handles admin login/session/password management.
type AuthService struct {
	store      *store.Store
	limiter    *security.LoginLimiter
	log        *slog.Logger
	auditor    *Auditor
	master     []byte
	sessionTTL time.Duration
}

// NewAuthService builds the service.
func NewAuthService(st *store.Store, log *slog.Logger, auditor *Auditor, master []byte) *AuthService {
	return &AuthService{
		store:      st,
		limiter:    security.NewLoginLimiter(),
		log:        log,
		auditor:    auditor,
		master:     master,
		sessionTTL: SessionTTL,
	}
}

// LoginResult carries the created session and raw CSRF token.
type LoginResult struct {
	SessionID    string
	SessionToken string
	CSRF         string
	Admin        *model.AdminUser
}

// Login authenticates an admin, applying IP+username lockout.
func (s *AuthService) Login(ctx context.Context, username, password, ip, ua string) (*LoginResult, error) {
	if locked := s.limiter.Locked(ip, username); locked > 0 {
		s.auditor.Denied(ctx, AuditInput{
			ActorType: model.ActorAdmin, Action: "auth.login", Result: ResultDenied,
			SourceIP: ip, Summary: "login blocked by rate limiter", RiskLevel: RiskHigh,
		})
		return nil, ErrLocked
	}
	admin, err := s.store.AdminByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.limiter.RecordFailure(ip, username)
			s.auditor.Failure(ctx, AuditInput{
				ActorType: model.ActorAdmin, Action: "auth.login", SourceIP: ip,
				Summary: "login failed: unknown user", RiskLevel: RiskMedium,
			})
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if admin.LockedUntil != nil && admin.LockedUntil.After(time.Now().UTC()) {
		s.auditor.Denied(ctx, AuditInput{
			ActorType: model.ActorAdmin, ActorID: admin.ID, Action: "auth.login",
			SourceIP: ip, Summary: "login blocked: account locked", RiskLevel: RiskHigh,
		})
		return nil, ErrLocked
	}
	ok, err := security.VerifyPassword(admin.PasswordHash, password)
	if err != nil || !ok {
		s.limiter.RecordFailure(ip, username)
		admin.FailedLoginCount++
		admin.LockedUntil = nil
		_ = s.store.UpdateAdmin(ctx, admin)
		s.auditor.Failure(ctx, AuditInput{
			ActorType: model.ActorAdmin, ActorID: admin.ID, Action: "auth.login", SourceIP: ip,
			Summary: "login failed: bad password", RiskLevel: RiskMedium,
		})
		return nil, ErrInvalidCredentials
	}
	s.limiter.Reset(ip, username)
	admin.FailedLoginCount = 0
	admin.LockedUntil = nil
	_ = s.store.UpdateAdmin(ctx, admin)

	token, err := security.NewToken(32)
	if err != nil {
		return nil, err
	}
	sessID := model.NewUUID()
	csrf := csrfForSession(s.master, sessID)
	sess := &model.AdminSession{
		ID:             sessID,
		AdminUserID:    admin.ID,
		TokenHash:      security.HashToken(token),
		CSRFSecretHash: security.HashCSRF(csrf),
		IPAddress:      ip,
		UserAgent:      ua,
		ExpiresAt:      time.Now().UTC().Add(s.sessionTTL),
	}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return nil, err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: admin.ID, Action: "auth.login",
		ResourceType: "session", ResourceID: sess.ID, SourceIP: ip, Summary: "admin login",
	})
	return &LoginResult{SessionID: sess.ID, SessionToken: token, CSRF: csrf, Admin: admin}, nil
}

// Authenticate resolves a raw session token to a session and admin.
func (s *AuthService) Authenticate(ctx context.Context, rawToken string) (*model.AdminSession, *model.AdminUser, error) {
	if rawToken == "" {
		return nil, nil, ErrNotAuthenticated
	}
	sess, err := s.store.SessionByTokenHash(ctx, security.HashToken(rawToken))
	if err != nil {
		return nil, nil, ErrNotAuthenticated
	}
	if sess.RevokedAt != nil {
		return nil, nil, ErrNotAuthenticated
	}
	if sess.ExpiresAt.Before(time.Now().UTC()) {
		_ = s.store.RevokeSession(ctx, sess.ID)
		return nil, nil, ErrNotAuthenticated
	}
	admin, err := s.store.AdminByID(ctx, sess.AdminUserID)
	if err != nil {
		return nil, nil, ErrNotAuthenticated
	}
	return sess, admin, nil
}

// ValidateCSRF checks a CSRF token against the session's stored hash.
func (s *AuthService) ValidateCSRF(sess *model.AdminSession, token string) bool {
	if sess == nil || token == "" {
		return false
	}
	expected := csrfForSession(s.master, sess.ID)
	return security.ConstantTimeEqual(expected, token) &&
		security.ConstantTimeEqual(security.HashCSRF(expected), sess.CSRFSecretHash)
}

// CSRFFor re-derives the raw CSRF token for a session (returned by
// GET /auth/session).
func (s *AuthService) CSRFFor(sess *model.AdminSession) string {
	if sess == nil {
		return ""
	}
	return csrfForSession(s.master, sess.ID)
}

// Logout revokes a session.
func (s *AuthService) Logout(ctx context.Context, sess *model.AdminSession, ip string) error {
	if sess == nil {
		return nil
	}
	err := s.store.RevokeSession(ctx, sess.ID)
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: sess.AdminUserID, Action: "auth.logout",
		ResourceType: "session", ResourceID: sess.ID, SourceIP: ip, Summary: "admin logout",
	})
	return err
}

// ChangePassword verifies the old password and sets a new one, revoking all
// other sessions.
func (s *AuthService) ChangePassword(ctx context.Context, admin *model.AdminUser, oldPass, newPass string, ip string) error {
	ok, err := security.VerifyPassword(admin.PasswordHash, oldPass)
	if err != nil || !ok {
		s.auditor.Failure(ctx, AuditInput{
			ActorType: model.ActorAdmin, ActorID: admin.ID, Action: "auth.password", SourceIP: ip,
			Summary: "password change rejected: wrong old password", RiskLevel: RiskHigh,
		})
		return ErrInvalidCredentials
	}
	hash, err := security.HashPassword(newPass)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	admin.PasswordHash = hash
	admin.PasswordChangedAt = &now
	admin.FailedLoginCount = 0
	admin.LockedUntil = nil
	if err := s.store.UpdateAdmin(ctx, admin); err != nil {
		return err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: admin.ID, Action: "auth.password",
		SourceIP: ip, Summary: "admin password changed", RiskLevel: RiskHigh,
	})
	return nil
}
