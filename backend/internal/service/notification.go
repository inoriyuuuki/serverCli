package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"servercli/internal/config"
	"servercli/internal/logger"
	"servercli/internal/model"
)

// NotificationRequest is the unified payload for internal (in-process) and
// external (HTTP API) notification sends.
type NotificationRequest struct {
	Title   string
	Message string
	Level   string // info|warning|error
	Channel string // first version only "default"
	Source  string // stable module identifier, e.g. "api.token", "task.scheduler"
}

// NotificationResult is returned on a successful send.
type NotificationResult struct {
	Status   string `json:"status"` // "sent"
	Channel  string `json:"channel"`
	Provider string `json:"provider"` // "feishu"
}

// NotificationProvider sends a notification to a concrete channel backend.
type NotificationProvider interface {
	Name() string
	Send(ctx context.Context, req NotificationRequest) error
}

// defaultChannel is the only supported channel alias in the first version.
const defaultChannel = "default"

// NotificationService dispatches notifications through channel providers. It
// is shared by internal Go callers (Send) and the external HTTP API
// (SendAuthorized), and audits every attempt. Only channel aliases are ever
// exposed to callers — never provider URLs — so a caller cannot target
// arbitrary endpoints (SSRF defense).
type NotificationService struct {
	cfg       *config.Config
	log       *slog.Logger
	auditor   *Auditor
	limiter   *NotificationLimiter
	providers map[string]NotificationProvider
}

// NewNotificationService builds the service. The default channel is wired to
// the Feishu provider configured from cfg.
func NewNotificationService(cfg *config.Config, log *slog.Logger, auditor *Auditor, limiter *NotificationLimiter) *NotificationService {
	s := &NotificationService{
		cfg:       cfg,
		log:       log,
		auditor:   auditor,
		limiter:   limiter,
		providers: make(map[string]NotificationProvider),
	}
	s.providers[defaultChannel] = NewFeishuProvider(cfg.NotificationFeishuWebhookURL, 0, nil)
	return s
}

// registerProvider installs a provider under a channel alias. Unexported: only
// the package and its tests wire providers; external callers can only choose
// an alias.
func (s *NotificationService) registerProvider(channel string, p NotificationProvider) {
	if p == nil {
		delete(s.providers, channel)
		return
	}
	s.providers[channel] = p
}

// Send is the internal-call entry point: it first consumes the global rate
// limit quota and returns ErrRateLimited when the global bucket is empty, then
// validates, resolves the channel, sends and audits.
func (s *NotificationService) Send(ctx context.Context, req NotificationRequest) (*NotificationResult, error) {
	// Do not consume global quota when no provider is configured: the attempt
	// would fail anyway and the quota is shared with external callers. The
	// attempt is still audited so an unconfigured provider is never silent.
	if p, ok := s.providers[defaultChannel]; ok {
		if c, ok := p.(interface{ Configured() bool }); ok && !c.Configured() {
			s.log.Warn("notification not configured", "source", strings.TrimSpace(req.Source), "request_id", logger.RequestID(ctx))
			s.auditNotification(ctx, req, auditMeta{actorType: model.ActorSystem}, ResultFailure, "notification.send")
			return nil, ErrNotConfigured
		}
	}
	if retryAfter, ok := s.limiter.TryAcquireGlobal(); !ok {
		s.auditNotification(ctx, req, auditMeta{actorType: model.ActorSystem}, ResultDenied, "notification.ratelimited")
		return nil, fmt.Errorf("%w: retry after %s", ErrRateLimited, retryAfter)
	}
	return s.send(ctx, req, auditMeta{actorType: model.ActorSystem})
}

// SendAuthorized is the external API entry point: the token and global quota
// were already acquired atomically by the HTTP auth hook, so it skips limiter
// acquisition and shares the rest of the pipeline (validation, channel
// resolution, provider send, audit) with Send. The authenticated token ID is
// read from ctx (see WithNotificationTokenID).
func (s *NotificationService) SendAuthorized(ctx context.Context, req NotificationRequest) (*NotificationResult, error) {
	tokenID := NotificationTokenID(ctx)
	meta := auditMeta{
		actorType:    model.ActorAI,
		actorID:      tokenID,
		resourceType: "api_access_token",
		resourceID:   tokenID,
	}
	return s.send(ctx, req, meta)
}

// auditMeta carries the actor/resource fields an audit event is written with.
type auditMeta struct {
	actorType    string
	actorID      string
	resourceType string
	resourceID   string
}

// send runs the common pipeline: validate, resolve the provider by channel
// alias, deliver, and audit the outcome. Every attempt (success or failure)
// writes an audit event.
func (s *NotificationService) send(ctx context.Context, req NotificationRequest, meta auditMeta) (*NotificationResult, error) {
	normalized, err := validateNotification(req)
	if err != nil {
		// Caller-side validation failure is a denied attempt (the request was
		// never delivered), not an upstream failure.
		s.auditNotification(ctx, req, meta, ResultDenied, "notification.send")
		return nil, err
	}
	provider, ok := s.providers[normalized.Channel]
	if !ok {
		err := fmt.Errorf("%w: unsupported channel %q", ErrBadRequest, normalized.Channel)
		s.auditNotification(ctx, normalized, meta, ResultDenied, "notification.send")
		return nil, err
	}
	if err := provider.Send(ctx, normalized); err != nil {
		s.log.Warn("notification send failed",
			"provider", provider.Name(),
			"channel", normalized.Channel,
			"source", normalized.Source,
			"error", err,
			"request_id", logger.RequestID(ctx))
		s.auditNotification(ctx, normalized, meta, ResultFailure, "notification.send")
		return nil, err
	}
	s.auditNotification(ctx, normalized, meta, ResultSuccess, "notification.send")
	return &NotificationResult{
		Status:   "sent",
		Channel:  normalized.Channel,
		Provider: provider.Name(),
	}, nil
}

// validateNotification normalizes and checks a request. The normalized copy
// carries trimmed title/message and defaulted level/channel.
func validateNotification(req NotificationRequest) (NotificationRequest, error) {
	out := req
	out.Title = strings.TrimSpace(req.Title)
	if out.Title == "" {
		return out, fmt.Errorf("%w: title is required", ErrBadRequest)
	}
	if utf8.RuneCountInString(out.Title) > 200 {
		return out, fmt.Errorf("%w: title too long (max 200 characters)", ErrBadRequest)
	}
	out.Message = strings.TrimSpace(req.Message)
	if out.Message == "" {
		return out, fmt.Errorf("%w: message is required", ErrBadRequest)
	}
	if len([]byte(out.Message)) > 8192 {
		return out, fmt.Errorf("%w: message too long (max 8192 bytes)", ErrBadRequest)
	}
	level := strings.TrimSpace(req.Level)
	if level == "" {
		level = "info"
	}
	switch level {
	case "info", "warning", "error":
		out.Level = level
	default:
		return out, fmt.Errorf("%w: level must be one of info|warning|error", ErrBadRequest)
	}
	channel := strings.TrimSpace(req.Channel)
	if channel == "" {
		channel = defaultChannel
	}
	if channel != defaultChannel {
		return out, fmt.Errorf("%w: channel must be %q", ErrBadRequest, defaultChannel)
	}
	out.Channel = channel
	out.Source = strings.TrimSpace(req.Source)
	if out.Source == "" {
		return out, fmt.Errorf("%w: source is required", ErrBadRequest)
	}
	return out, nil
}

// auditNotification writes one audit event. The details map only ever contains
// the whitelisted keys source/channel/level/title_length/message_length/
// outcome/request_id — never the title/message bodies, webhook URLs, tokens
// or upstream responses.
func (s *NotificationService) auditNotification(ctx context.Context, req NotificationRequest, meta auditMeta, outcome, action string) {
	details := map[string]any{
		"source":         req.Source,
		"channel":        req.Channel,
		"level":          req.Level,
		"title_length":   utf8.RuneCountInString(req.Title),
		"message_length": len([]byte(req.Message)),
		"outcome":        outcome,
	}
	requestID := logger.RequestID(ctx)
	if requestID != "" {
		details["request_id"] = requestID
	}
	in := AuditInput{
		ActorType:    meta.actorType,
		ActorID:      meta.actorID,
		Action:       action,
		ResourceType: meta.resourceType,
		ResourceID:   meta.resourceID,
		Result:       outcome,
		RequestID:    requestID,
		Summary:      "notification " + outcome,
		Details:      details,
	}
	if err := s.auditor.Record(ctx, in); err != nil {
		s.log.Error("notification audit write failed", "error", err, "action", action)
	}
}

// notifCtxKey carries the external access-token ID into SendAuthorized.
type notifCtxKey struct{}

// WithNotificationTokenID returns a context carrying the access-token ID that
// authenticated an external notification request.
func WithNotificationTokenID(ctx context.Context, tokenID string) context.Context {
	return context.WithValue(ctx, notifCtxKey{}, tokenID)
}

// NotificationTokenID extracts the access-token ID set by
// WithNotificationTokenID, or "" when absent.
func NotificationTokenID(ctx context.Context) string {
	if v, ok := ctx.Value(notifCtxKey{}).(string); ok {
		return v
	}
	return ""
}
