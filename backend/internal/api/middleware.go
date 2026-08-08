package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"servercli/internal/model"
	"servercli/internal/security"
	"servercli/internal/service"
)

type adminCtxKey struct{}
type sessionCtxKey struct{}
type nodeCtxKey struct{}
type agentBodyKey struct{}

func adminFrom(ctx context.Context) *model.AdminUser {
	v, _ := ctx.Value(adminCtxKey{}).(*model.AdminUser)
	return v
}

func sessionFrom(ctx context.Context) *model.AdminSession {
	v, _ := ctx.Value(sessionCtxKey{}).(*model.AdminSession)
	return v
}

func nodeFrom(ctx context.Context) *model.Node {
	v, _ := ctx.Value(nodeCtxKey{}).(*model.Node)
	return v
}

// requireAdmin authenticates the admin session (and CSRF for writes).
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("servercli_session")
		if err != nil {
			writeError(w, r, s.log, http.StatusUnauthorized, "UNAUTHENTICATED", "login required", nil)
			return
		}
		sess, admin, err := s.auth.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, r, s.log, http.StatusUnauthorized, "UNAUTHENTICATED", "session invalid or expired", nil)
			return
		}
		if isWriteMethod(r.Method) {
			token := r.Header.Get("X-CSRF-Token")
			if !s.auth.ValidateCSRF(sess, token) {
				s.auditor.Denied(r.Context(), service.AuditInput{
					ActorType: model.ActorAdmin, ActorID: admin.ID, Action: "auth.csrf",
					SourceIP: remoteIP(r), Summary: "CSRF validation failed", RiskLevel: service.RiskHigh,
				})
				writeError(w, r, s.log, http.StatusForbidden, "CSRF_FAILED", "invalid or missing X-CSRF-Token", nil)
				return
			}
		}
		_ = s.store.TouchSession(r.Context(), sess.ID, remoteIP(r), r.UserAgent())
		ctx := context.WithValue(r.Context(), adminCtxKey{}, admin)
		ctx = context.WithValue(ctx, sessionCtxKey{}, sess)
		next(w, r.WithContext(ctx))
	}
}

func isWriteMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// agentAuth authenticates node agents via Bearer credential + HMAC signature.
func (s *Server) agentAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, "Bearer ") {
			writeError(w, r, s.log, http.StatusUnauthorized, "UNAUTHENTICATED", "missing bearer credential", nil)
			return
		}
		credential := strings.TrimPrefix(authz, "Bearer ")
		node, err := s.store.NodeByCredentialHash(r.Context(), security.HashToken(credential))
		if err != nil {
			s.auditor.Denied(r.Context(), service.AuditInput{
				ActorType: model.ActorNode, Action: "agent.auth", SourceIP: remoteIP(r),
				Summary: "agent authentication failed (unknown credential)", RiskLevel: service.RiskHigh,
			})
			writeError(w, r, s.log, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid node credential", nil)
			return
		}
		if !node.Enabled {
			s.auditor.Denied(r.Context(), service.AuditInput{
				ActorType: model.ActorNode, ActorID: node.ID, Action: "agent.auth", SourceIP: remoteIP(r),
				Summary: "agent authentication rejected (node disabled)", RiskLevel: service.RiskHigh,
			})
			writeError(w, r, s.log, http.StatusForbidden, "FORBIDDEN", "node disabled", nil)
			return
		}
		// Signature: HMAC-SHA256(credential, "ts|method|path|sha256(body)").
		tsHeader := r.Header.Get("X-Agent-Timestamp")
		sigHeader := r.Header.Get("X-Agent-Signature")
		if tsHeader == "" || sigHeader == "" {
			writeError(w, r, s.log, http.StatusUnauthorized, "BAD_SIGNATURE", "missing agent timestamp or signature", nil)
			return
		}
		ts, err := strconv.ParseInt(tsHeader, 10, 64)
		if err != nil || abs(time.Now().Unix()-ts) > 300 {
			s.auditor.Denied(r.Context(), service.AuditInput{
				ActorType: model.ActorNode, ActorID: node.ID, Action: "agent.auth", SourceIP: remoteIP(r),
				Summary: "agent signature timestamp outside tolerance", RiskLevel: service.RiskHigh,
			})
			writeError(w, r, s.log, http.StatusUnauthorized, "BAD_SIGNATURE", "timestamp outside tolerance", nil)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "cannot read body", nil)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		bodyHash := sha256.Sum256(body)
		expected := agentSignature(credential, tsHeader, r.Method, r.URL.Path, hex.EncodeToString(bodyHash[:]))
		if !security.ConstantTimeEqual(expected, sigHeader) {
			s.auditor.Denied(r.Context(), service.AuditInput{
				ActorType: model.ActorNode, ActorID: node.ID, Action: "agent.auth", SourceIP: remoteIP(r),
				Summary: "agent signature mismatch", RiskLevel: service.RiskHigh,
			})
			writeError(w, r, s.log, http.StatusUnauthorized, "BAD_SIGNATURE", "signature mismatch", nil)
			return
		}
		ctx := context.WithValue(r.Context(), nodeCtxKey{}, node)
		next(w, r.WithContext(ctx))
	}
}

// agentSignature computes the agent request signature.
func agentSignature(credential, ts, method, path, bodyHash string) string {
	mac := hmac.New(sha256.New, []byte(credential))
	mac.Write([]byte(ts + "|" + method + "|" + path + "|" + bodyHash))
	return hex.EncodeToString(mac.Sum(nil))
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
