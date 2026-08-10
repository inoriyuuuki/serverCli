package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"servercli/internal/config"
	"servercli/internal/db"
	"servercli/internal/logger"
	"servercli/internal/model"
	"servercli/internal/store"
)

// notifTestSetup builds a minimal DB-backed environment for notification
// tests (store + auditor) without depending on other service constructors.
func notifTestSetup(t *testing.T) (context.Context, *store.Store, *config.Config, *Auditor) {
	t.Helper()
	cfg := config.Default()
	cfg.AgentStateDir = t.TempDir()
	cfg.DatabaseURL = filepath.Join(cfg.AgentStateDir, "test.db")
	cfg.LogLevel = "error"
	log := logger.New(os.Stderr, "error")
	database, err := db.Open(context.Background(), "sqlite", cfg.DatabaseURL, log)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	st := store.New(database, log)
	auditor := NewAuditor(st, log, "notif-env", "test-primary")
	return context.Background(), st, cfg, auditor
}

// fakeNotificationProvider records calls for service/provider interaction tests.
type fakeNotificationProvider struct {
	name  string
	err   error
	calls int
	last  NotificationRequest
}

func (f *fakeNotificationProvider) Name() string {
	if f.name == "" {
		return "fake"
	}
	return f.name
}

func (f *fakeNotificationProvider) Send(_ context.Context, req NotificationRequest) error {
	f.calls++
	f.last = req
	return f.err
}

// notifServiceForTest builds a NotificationService wired to a fake provider on
// the default channel. cfg rate fields drive the limiter.
func notifServiceForTest(cfg *config.Config, auditor *Auditor, fake NotificationProvider) (*NotificationService, *NotificationLimiter) {
	lim := NewNotificationLimiter(cfg.NotificationRateLimitPerTokenPerMinute, cfg.NotificationRateLimitGlobalPerMinute)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewNotificationService(cfg, log, auditor, lim)
	if fake != nil {
		svc.registerProvider(defaultChannel, fake)
	}
	return svc, lim
}

func validNotifReq() NotificationRequest {
	return NotificationRequest{
		Title:   "deploy failed",
		Message: "step 3 exited with code 1",
		Level:   "error",
		Channel: "default",
		Source:  "test.suite",
	}
}

func TestNotificationValidate(t *testing.T) {
	ctx, _, cfg, auditor := notifTestSetup(t)
	fake := &fakeNotificationProvider{}
	svc, _ := notifServiceForTest(cfg, auditor, fake)

	longTitle := strings.Repeat("字", 201)
	bigMessage := strings.Repeat("x", 8193)

	cases := []struct {
		name      string
		req       NotificationRequest
		wantErr   bool
		wantLevel string
	}{
		{"valid", validNotifReq(), false, "error"},
		{"empty title", NotificationRequest{Title: "  ", Message: "m", Source: "s"}, true, ""},
		{"long title", NotificationRequest{Title: longTitle, Message: "m", Source: "s"}, true, ""},
		{"empty message", NotificationRequest{Title: "t", Message: " \n", Source: "s"}, true, ""},
		{"long message", NotificationRequest{Title: "t", Message: bigMessage, Source: "s"}, true, ""},
		{"message boundary 8192 bytes", NotificationRequest{Title: "t", Message: strings.Repeat("x", 8192), Source: "s"}, false, "info"},
		{"bad level uppercase", NotificationRequest{Title: "t", Message: "m", Level: "INFO", Source: "s"}, true, ""},
		{"bad level value", NotificationRequest{Title: "t", Message: "m", Level: "debug", Source: "s"}, true, ""},
		{"bad channel", NotificationRequest{Title: "t", Message: "m", Channel: "slack", Source: "s"}, true, ""},
		{"missing source", NotificationRequest{Title: "t", Message: "m"}, true, ""},
		{"defaults applied", NotificationRequest{Title: "t", Message: "m", Source: "s"}, false, "info"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := svc.Send(ctx, tc.req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got result %+v", res)
				}
				if !errors.Is(err, ErrBadRequest) {
					t.Fatalf("expected ErrBadRequest, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res == nil || res.Status != "sent" || res.Provider != "fake" {
				t.Fatalf("unexpected result: %+v", res)
			}
			// Channel always normalizes to the default alias.
			if fake.last.Channel != "default" {
				t.Fatalf("channel not defaulted: %+v", fake.last)
			}
			if fake.last.Level != tc.wantLevel {
				t.Fatalf("level = %q, want %q", fake.last.Level, tc.wantLevel)
			}
		})
	}
}

func TestNotificationWebhookNotConfigured(t *testing.T) {
	ctx, _, cfg, auditor := notifTestSetup(t)
	cfg.NotificationFeishuWebhookURL = ""
	// No fake provider: the real Feishu provider (unconfigured) serves default.
	svc, _ := notifServiceForTest(cfg, auditor, nil)

	res, err := svc.Send(ctx, validNotifReq())
	if res != nil {
		t.Fatalf("expected nil result, got %+v", res)
	}
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestNotificationSendGlobalRateLimited(t *testing.T) {
	ctx, st, cfg, auditor := notifTestSetup(t)
	cfg.NotificationRateLimitPerTokenPerMinute = 100
	cfg.NotificationRateLimitGlobalPerMinute = 1
	fake := &fakeNotificationProvider{}
	svc, _ := notifServiceForTest(cfg, auditor, fake)

	if _, err := svc.Send(ctx, validNotifReq()); err != nil {
		t.Fatalf("first send should succeed: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("provider should have been called once, got %d", fake.calls)
	}
	if _, err := svc.Send(ctx, validNotifReq()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second send should be rate limited, got %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("provider must not be called when rate limited, got %d calls", fake.calls)
	}

	events, err := st.ListAuditEvents(ctx, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 audit events, got %d", len(events))
	}
	var sawSuccess, sawDenied bool
	for _, ev := range events {
		if ev.Result == ResultSuccess && ev.Action == "notification.send" {
			sawSuccess = true
		}
		if ev.Result == ResultDenied && ev.Action == "notification.ratelimited" {
			sawDenied = true
		}
	}
	if !sawSuccess || !sawDenied {
		t.Fatalf("expected both success and ratelimited audit events, got %d events", len(events))
	}
}

func TestNotificationSendAuthorizedSkipsLimiter(t *testing.T) {
	ctx, st, cfg, auditor := notifTestSetup(t)
	cfg.NotificationRateLimitPerTokenPerMinute = 1
	cfg.NotificationRateLimitGlobalPerMinute = 1
	fake := &fakeNotificationProvider{}
	svc, _ := notifServiceForTest(cfg, auditor, fake)

	actx := WithNotificationTokenID(ctx, "tok-123")
	for i := 0; i < 2; i++ {
		if _, err := svc.SendAuthorized(actx, validNotifReq()); err != nil {
			t.Fatalf("SendAuthorized #%d should succeed without consuming limiter: %v", i+1, err)
		}
	}
	if fake.calls != 2 {
		t.Fatalf("expected 2 provider calls, got %d", fake.calls)
	}

	events, err := st.ListAuditEvents(ctx, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 audit events, got %d", len(events))
	}
	for _, ev := range events {
		if ev.ActorType != model.ActorAI || ev.ActorID != "tok-123" {
			t.Fatalf("external audit actor wrong: type=%q id=%q", ev.ActorType, ev.ActorID)
		}
		if ev.ResourceType != "api_access_token" || ev.ResourceID != "tok-123" {
			t.Fatalf("external audit resource wrong: type=%q id=%q", ev.ResourceType, ev.ResourceID)
		}
	}
}

func TestNotificationAuditDetailsWhitelist(t *testing.T) {
	ctx, st, cfg, auditor := notifTestSetup(t)
	fake := &fakeNotificationProvider{err: errors.New("boom")}
	svc, _ := notifServiceForTest(cfg, auditor, fake)

	actx := logger.WithRequestID(WithNotificationTokenID(ctx, "tok-9"), "req-42")
	// Success case.
	fake.err = nil
	req := validNotifReq()
	if _, err := svc.SendAuthorized(actx, req); err != nil {
		t.Fatalf("send: %v", err)
	}
	// Failure case.
	fake.err = errors.New("boom")
	if _, err := svc.SendAuthorized(actx, req); err == nil {
		t.Fatal("expected provider failure")
	}

	events, err := st.ListAuditEvents(ctx, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 audit events, got %d", len(events))
	}

	whitelist := map[string]bool{
		"source": true, "channel": true, "level": true,
		"title_length": true, "message_length": true,
		"outcome": true, "request_id": true,
	}
	wantOutcome := map[string]bool{ResultSuccess: true, ResultFailure: true}
	for _, ev := range events {
		if !wantOutcome[ev.Result] {
			t.Fatalf("unexpected outcome %q", ev.Result)
		}
		var details map[string]any
		if err := json.Unmarshal([]byte(ev.DetailsJSON), &details); err != nil {
			t.Fatalf("details not JSON: %q: %v", ev.DetailsJSON, err)
		}
		for k := range details {
			if !whitelist[k] {
				t.Fatalf("details key %q not in whitelist: %s", k, ev.DetailsJSON)
			}
		}
		if details["outcome"] != ev.Result {
			t.Fatalf("details outcome %v != result %q", details["outcome"], ev.Result)
		}
		if details["title_length"] != float64(len([]rune(req.Title))) {
			t.Fatalf("title_length mismatch: %v", details["title_length"])
		}
		if details["message_length"] != float64(len([]byte(req.Message))) {
			t.Fatalf("message_length mismatch: %v", details["message_length"])
		}
		if details["request_id"] != "req-42" {
			t.Fatalf("request_id missing from details: %v", details["request_id"])
		}
		// Bodies must never appear anywhere in the persisted event.
		raw := ev.DetailsJSON + " " + ev.Summary
		if strings.Contains(raw, req.Title) || strings.Contains(raw, req.Message) {
			t.Fatal("notification body leaked into audit event")
		}
		if strings.Contains(ev.Summary, req.Title) || strings.Contains(ev.Summary, req.Message) {
			t.Fatal("notification body leaked into summary")
		}
	}
}

func TestFeishuProviderSuccess(t *testing.T) {
	var gotBody string
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer srv.Close()

	p := NewFeishuProvider(srv.URL, 0, nil)
	if err := p.Send(context.Background(), validNotifReq()); err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if !strings.Contains(gotCT, "application/json") {
		t.Fatalf("unexpected content type %q", gotCT)
	}
	var payload struct {
		MsgType string `json:"msg_type"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MsgType != "text" {
		t.Fatalf("unexpected msg_type %q", payload.MsgType)
	}
	if payload.Content.Text != "deploy failed\nstep 3 exited with code 1" {
		t.Fatalf("unexpected text %q", payload.Content.Text)
	}
}

func TestFeishuProviderBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":9499,"msg":"sign invalid"}`))
	}))
	defer srv.Close()

	p := NewFeishuProvider(srv.URL, 0, nil)
	err := p.Send(context.Background(), validNotifReq())
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected ErrUpstream, got %v", err)
	}
	if strings.Contains(err.Error(), "sign invalid") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("upstream error leaked response/url: %v", err)
	}
}

func TestFeishuProviderHTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("oops"))
	}))
	defer srv.Close()

	p := NewFeishuProvider(srv.URL, 0, nil)
	err := p.Send(context.Background(), validNotifReq())
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected ErrUpstream, got %v", err)
	}
	if strings.Contains(err.Error(), "oops") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("upstream error leaked response/url: %v", err)
	}
}

func TestFeishuProviderInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	p := NewFeishuProvider(srv.URL, 0, nil)
	if err := p.Send(context.Background(), validNotifReq()); !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected ErrUpstream, got %v", err)
	}
}

func TestFeishuProviderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	p := NewFeishuProvider(srv.URL, 20*time.Millisecond, nil)
	start := time.Now()
	err := p.Send(context.Background(), validNotifReq())
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("timeout not enforced, took %s", elapsed)
	}
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected ErrUpstream on timeout, got %v", err)
	}
	if strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("timeout error leaked url: %v", err)
	}
}

func TestFeishuProviderNotConfigured(t *testing.T) {
	p := NewFeishuProvider("", 0, nil)
	if err := p.Send(context.Background(), validNotifReq()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestFeishuProviderTimeoutConstantDefault(t *testing.T) {
	if feishuTimeout != 5*time.Second {
		t.Fatalf("feishuTimeout must be 5s, got %s", feishuTimeout)
	}
	p := NewFeishuProvider("http://example.invalid", 0, nil)
	if p.timeout != 5*time.Second {
		t.Fatalf("expected default timeout 5s, got %s", p.timeout)
	}
}
