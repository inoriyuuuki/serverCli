package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"servercli/internal/model"
	"servercli/internal/service"
	"servercli/internal/store"
)

// feishuMock is a configurable in-process Feishu webhook that records the
// payloads it received.
type feishuMock struct {
	mu       sync.Mutex
	code     int // feishu business code (0 = success)
	status   int // HTTP status (0 = 200)
	requests []map[string]any
	url      string
}

func (m *feishuMock) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	m.mu.Lock()
	m.requests = append(m.requests, payload)
	status := m.status
	code := m.code
	m.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(fmt.Sprintf(`{"code":%d}`, code)))
}

func (m *feishuMock) set(status, code int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = status
	m.code = code
}

func (m *feishuMock) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *feishuMock) last() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return nil
	}
	return m.requests[len(m.requests)-1]
}

// setupNotifAPI builds a test env whose NotificationService Feishu webhook
// points at the mock server, so sends are actually delivered in-process.
func setupNotifAPI(t *testing.T, mock *feishuMock) *testEnv {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(mock.handler))
	t.Cleanup(ts.Close)
	mock.url = ts.URL
	t.Setenv("NOTIFICATION_FEISHU_WEBHOOK_URL", ts.URL)
	return setupAPI(t)
}

// serveWithHeaders is serve plus response headers (e.g. Retry-After).
func (e *testEnv) serveWithHeaders(method, path string, body any, headers map[string]string) (int, http.Header, []byte) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Header(), rec.Body.Bytes()
}

func notifSendBody(title, message string) map[string]any {
	return map[string]any{"title": title, "message": message}
}

// assertNoLeak fails when out contains any of the secrets (webhook URL, token
// plaintext, notification content).
func assertNoLeak(t *testing.T, out []byte, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(string(out), s) {
			t.Fatalf("response leaked %q: %s", s, out)
		}
	}
}

// usageOutcomes returns the usage outcomes recorded for a token.
func usageOutcomes(t *testing.T, env *testEnv, tokenID string) []map[string]any {
	t.Helper()
	status, out := env.serve("GET", "/api/v1/api-tokens/"+tokenID+"/usage-logs?limit=100", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("usage logs status %d: %s", status, out)
	}
	var resp struct {
		UsageLogs []map[string]any `json:"usage_logs"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode usage logs %s: %v", out, err)
	}
	return resp.UsageLogs
}

// notificationAudits returns the notification.send / notification.ratelimited
// audit events for a token with their details.
func notificationAudits(t *testing.T, env *testEnv, tokenID string) []*model.AuditEvent {
	t.Helper()
	ctx := context.Background()
	events, err := env.st.ListAuditEvents(ctx, store.AuditFilter{ActorID: tokenID})
	if err != nil {
		t.Fatal(err)
	}
	var out []*model.AuditEvent
	for _, e := range events {
		if e.Action == "notification.send" || e.Action == "notification.ratelimited" {
			out = append(out, e)
		}
	}
	return out
}

func TestNotificationSendAuthAndPermissions(t *testing.T) {
	mock := &feishuMock{code: 0}
	env := setupNotifAPI(t, mock)

	// Missing token -> 401.
	status, out := env.serve("POST", "/api/v1/notifications/send", notifSendBody("t", "m"), nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("missing token should be 401, got %d: %s", status, out)
	}
	assertNoLeak(t, out, mock.url)

	// A token without notifications:send is denied (403) even though the quota
	// was already acquired for this valid token.
	id, tok := createAPIToken(t, env, "no-notif", "1h")
	grantAIPermissions(t, env, id, 1) // AI surface only, no notifications
	status, out = env.serve("POST", "/api/v1/notifications/send", notifSendBody("t", "m"), tokenHeaders(tok))
	if status != http.StatusForbidden {
		t.Fatalf("token without notification permission should be 403, got %d: %s", status, out)
	}
	assertNoLeak(t, out, tok, mock.url)
	found := false
	for _, l := range usageOutcomes(t, env, id) {
		if l["outcome"] == "denied" && l["route"] == "/api/v1/notifications/send" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a denied usage row for the 403, got %+v", usageOutcomes(t, env, id))
	}

	// Grant notifications:send -> success, provider=feishu, channel=default.
	grantNotificationsPermission(t, env, id, 2)
	status, out = env.serve("POST", "/api/v1/notifications/send", notifSendBody("deploy failed", "step 3 exited with code 1"), tokenHeaders(tok))
	if status != http.StatusOK {
		t.Fatalf("send status %d: %s", status, out)
	}
	assertNoLeak(t, out, tok, mock.url, "deploy failed", "step 3 exited with code 1")
	var notif struct {
		Notification struct {
			Status   string `json:"status"`
			Channel  string `json:"channel"`
			Provider string `json:"provider"`
		} `json:"notification"`
	}
	if err := json.Unmarshal(out, &notif); err != nil {
		t.Fatalf("decode send response %s: %v", out, err)
	}
	if notif.Notification.Status != "sent" || notif.Notification.Channel != "default" || notif.Notification.Provider != "feishu" {
		t.Fatalf("unexpected notification result: %s", out)
	}
	if mock.count() != 1 {
		t.Fatalf("expected 1 webhook delivery, got %d", mock.count())
	}
	last := mock.last()
	content, _ := last["content"].(map[string]any)
	if content["text"] != "deploy failed\nstep 3 exited with code 1" {
		t.Fatalf("webhook payload mismatch: %+v", last)
	}
}

func TestNotificationSendValidation(t *testing.T) {
	mock := &feishuMock{code: 0}
	env := setupNotifAPI(t, mock)
	id, tok := createAPIToken(t, env, "notif-valid", "1h")
	grantNotificationsPermission(t, env, id, 1)

	longTitle := strings.Repeat("字", 201)
	bigMsg := strings.Repeat("x", 8193)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"empty title", map[string]any{"message": "m"}},
		{"empty message", map[string]any{"title": "t"}},
		{"blank title", map[string]any{"title": "   ", "message": "m"}},
		{"long title", map[string]any{"title": longTitle, "message": "m"}},
		{"long message", map[string]any{"title": "t", "message": bigMsg}},
		{"bad level", map[string]any{"title": "t", "message": "m", "level": "debug"}},
		{"bad channel", map[string]any{"title": "t", "message": "m", "channel": "slack"}},
		{"source rejected", map[string]any{"title": "t", "message": "m", "source": "evil"}},
		{"url rejected", map[string]any{"title": "t", "message": "m", "url": "https://evil"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out := env.serve("POST", "/api/v1/notifications/send", tc.body, tokenHeaders(tok))
			if status != http.StatusBadRequest {
				t.Fatalf("%s should be 400, got %d: %s", tc.name, status, out)
			}
			assertNoLeak(t, out, tok, mock.url)
		})
	}
	if mock.count() != 0 {
		t.Fatalf("validation failures must not reach the webhook, got %d deliveries", mock.count())
	}

	// A boundary message (8192 bytes) passes.
	status, out := env.serve("POST", "/api/v1/notifications/send", notifSendBody("t", strings.Repeat("x", 8192)), tokenHeaders(tok))
	if status != http.StatusOK {
		t.Fatalf("boundary message should be accepted, got %d: %s", status, out)
	}
}

func TestNotificationSendUpstreamNotConfigured(t *testing.T) {
	// No webhook configured: SendAuthorized returns 503 NOT_CONFIGURED.
	t.Setenv("NOTIFICATION_FEISHU_WEBHOOK_URL", "")
	env := setupAPI(t)
	id, tok := createAPIToken(t, env, "notif-nocfg", "1h")
	grantNotificationsPermission(t, env, id, 1)

	status, out := env.serve("POST", "/api/v1/notifications/send", notifSendBody("t", "m"), tokenHeaders(tok))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured webhook should be 503, got %d: %s", status, out)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(out, &body)
	if body.Error.Code != "NOT_CONFIGURED" {
		t.Fatalf("expected NOT_CONFIGURED, got %s", out)
	}
	assertNoLeak(t, out, tok)
}

func TestNotificationSendUpstreamFailure(t *testing.T) {
	mock := &feishuMock{code: 0}
	env := setupNotifAPI(t, mock)
	id, tok := createAPIToken(t, env, "notif-up", "1h")
	grantNotificationsPermission(t, env, id, 1)

	// Feishu business code != 0 -> 502 UPSTREAM_ERROR.
	mock.set(0, 1)
	status, out := env.serve("POST", "/api/v1/notifications/send", notifSendBody("t", "m"), tokenHeaders(tok))
	if status != http.StatusBadGateway {
		t.Fatalf("business error should be 502, got %d: %s", status, out)
	}
	assertNoLeak(t, out, tok, mock.url)

	// HTTP 500 from upstream -> 502 UPSTREAM_ERROR.
	mock.set(500, 0)
	status, out = env.serve("POST", "/api/v1/notifications/send", notifSendBody("t", "m"), tokenHeaders(tok))
	if status != http.StatusBadGateway {
		t.Fatalf("http 500 should be 502, got %d: %s", status, out)
	}
	assertNoLeak(t, out, tok, mock.url)
}

func TestNoticeCompatibility(t *testing.T) {
	mock := &feishuMock{code: 0}
	env := setupNotifAPI(t, mock)

	// Missing token -> standard 401 (not wrapped in ret=0).
	status, out := env.serve("GET", "/notice?method=t&message=m", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("missing token should be 401, got %d: %s", status, out)
	}

	id, tok := createAPIToken(t, env, "notice", "1h")
	grantNotificationsPermission(t, env, id, 1)
	h := tokenHeaders(tok)

	// Empty method -> 200 ret=0, denied usage outcome, no delivery.
	status, out = env.serve("GET", "/notice", nil, h)
	if status != http.StatusOK {
		t.Fatalf("empty notice should be 200, got %d: %s", status, out)
	}
	if !strings.Contains(string(out), `"ret":0`) {
		t.Fatalf("empty notice should return ret=0: %s", out)
	}
	assertNoLeak(t, out, tok, mock.url)
	if mock.count() != 0 {
		t.Fatalf("empty notice must not deliver, got %d deliveries", mock.count())
	}
	// Empty message -> 200 ret=0.
	status, out = env.serve("GET", "/notice?method=t", nil, h)
	if status != http.StatusOK || !strings.Contains(string(out), `"ret":0`) {
		t.Fatalf("empty message notice should be 200 ret=0, got %d: %s", status, out)
	}

	// Empty case produces a denied usage row for /notice.
	foundDenied := false
	for _, l := range usageOutcomes(t, env, id) {
		if l["outcome"] == "denied" && l["route"] == "/notice" {
			foundDenied = true
		}
	}
	if !foundDenied {
		t.Fatalf("no denied usage row for empty /notice: %+v", usageOutcomes(t, env, id))
	}

	// Too long message (over the /notice 4096-byte cap) -> 200 ret=0, denied,
	// and never delivered (the service cap is 8192, so this is a real guard).
	status, out = env.serve("GET", "/notice?method=t&message="+strings.Repeat("x", 5000), nil, h)
	if status != http.StatusOK || !strings.Contains(string(out), `"ret":0`) {
		t.Fatalf("oversize notice should be 200 ret=0, got %d: %s", status, out)
	}
	if mock.count() != 0 {
		t.Fatalf("oversize notice must not deliver, got %d deliveries", mock.count())
	}
	// Too long title (201 runes) -> 200 ret=0, denied.
	status, out = env.serve("GET", "/notice?method="+strings.Repeat("字", 201)+"&message=m", nil, h)
	if status != http.StatusOK || !strings.Contains(string(out), `"ret":0`) {
		t.Fatalf("oversize title should be 200 ret=0, got %d: %s", status, out)
	}

	// Success -> 200 ret=1, delivered, level mapping warn->warning visible in
	// the audit details.
	status, out = env.serve("GET", "/notice?method=部署完成&message=构建成功&logLevel=warn", nil, h)
	if status != http.StatusOK || !strings.Contains(string(out), `"ret":1`) {
		t.Fatalf("successful notice should be 200 ret=1, got %d: %s", status, out)
	}
	assertNoLeak(t, out, tok, mock.url, "部署完成", "构建成功")
	if mock.count() != 1 {
		t.Fatalf("expected 1 delivery after success, got %d", mock.count())
	}
	last := mock.last()
	content, _ := last["content"].(map[string]any)
	if content["text"] != "部署完成\n构建成功" {
		t.Fatalf("notice webhook payload mismatch: %+v", last)
	}
	audits := notificationAudits(t, env, id)
	var levelWarn bool
	for _, a := range audits {
		if a.Result == service.ResultSuccess && strings.Contains(a.DetailsJSON, `"level":"warning"`) {
			levelWarn = true
		}
	}
	if !levelWarn {
		t.Fatalf("no success audit with level=warning: %+v", audits)
	}

	// logLevel mapping: error|fatal -> error; info/""/debug -> info.
	for _, c := range []struct{ raw, want string }{
		{"error", "error"}, {"fatal", "error"}, {"debug", "info"}, {"info", "info"}, {"", "info"}, {"INFO", "info"},
	} {
		path := "/notice?method=level&message=m"
		if c.raw != "" {
			path += "&logLevel=" + c.raw
		}
		status, out = env.serve("GET", path, nil, h)
		if status != http.StatusOK || !strings.Contains(string(out), `"ret":1`) {
			t.Fatalf("logLevel %q should succeed, got %d: %s", c.raw, status, out)
		}
	}
	levelAudits := notificationAudits(t, env, id)
	counts := map[string]int{}
	for _, a := range levelAudits {
		if a.Result == service.ResultSuccess && strings.Contains(a.DetailsJSON, `"level":"`) {
			var d struct {
				Level string `json:"level"`
			}
			_ = json.Unmarshal([]byte(a.DetailsJSON), &d)
			counts[d.Level]++
		}
	}
	if counts["error"] < 2 || counts["info"] < 4 {
		t.Fatalf("level mapping missing audits: %+v", counts)
	}

	// Provider failure -> 200 ret=0 with outcome=failure in usage.
	mock.set(500, 0)
	status, out = env.serve("GET", "/notice?method=leak-title-abc&message=leak-message-abc", nil, h)
	if status != http.StatusOK || !strings.Contains(string(out), `"ret":0`) {
		t.Fatalf("provider failure should be 200 ret=0, got %d: %s", status, out)
	}
	assertNoLeak(t, out, tok, mock.url, "leak-title-abc", "leak-message-abc")
	foundFailure := false
	for _, l := range usageOutcomes(t, env, id) {
		if l["outcome"] == "failure" && l["route"] == "/notice" {
			foundFailure = true
		}
	}
	if !foundFailure {
		t.Fatalf("no failure usage row for /notice: %+v", usageOutcomes(t, env, id))
	}
}

func TestNotificationRateLimit(t *testing.T) {
	t.Setenv("NOTIFICATION_RATE_LIMIT_PER_TOKEN_PER_MINUTE", "2")
	t.Setenv("NOTIFICATION_RATE_LIMIT_GLOBAL_PER_MINUTE", "120")
	mock := &feishuMock{code: 0}
	env := setupNotifAPI(t, mock)
	id, tok := createAPIToken(t, env, "ratelimit", "1h")
	grantNotificationsPermission(t, env, id, 1)

	// First two requests consume the per-token quota.
	for i := 0; i < 2; i++ {
		status, out := env.serve("POST", "/api/v1/notifications/send", notifSendBody("leak-check-title-xyz", "leak-check-message-xyz"), tokenHeaders(tok))
		if status != http.StatusOK {
			t.Fatalf("request %d should succeed, got %d: %s", i+1, status, out)
		}
	}
	// Third request -> 429 with Retry-After and a standard error envelope.
	status, headers, out := env.serveWithHeaders("POST", "/api/v1/notifications/send", notifSendBody("leak-check-title-xyz", "leak-check-message-xyz"), tokenHeaders(tok))
	if status != http.StatusTooManyRequests {
		t.Fatalf("third request should be 429, got %d: %s", status, out)
	}
	assertNoLeak(t, out, tok, mock.url, "leak-check-title-xyz", "leak-check-message-xyz")
	ra := headers.Get("Retry-After")
	if ra == "" {
		t.Fatalf("429 response missing Retry-After: %+v", headers)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(out, &body)
	if body.Error.Code != "RATE_LIMITED" {
		t.Fatalf("expected RATE_LIMITED, got %s", out)
	}
	if mock.count() != 2 {
		t.Fatalf("rate-limited request must not deliver, got %d deliveries", mock.count())
	}

	// 429 records a denied usage row and a ratelimited audit event.
	found := false
	for _, l := range usageOutcomes(t, env, id) {
		if l["outcome"] == "denied" && l["route"] == "/api/v1/notifications/send" && l["status_code"] == float64(429) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no denied 429 usage row: %+v", usageOutcomes(t, env, id))
	}
	audits := notificationAudits(t, env, id)
	hasRatelimit := false
	for _, a := range audits {
		if a.Action == "notification.ratelimited" && a.Result == service.ResultDenied {
			hasRatelimit = true
		}
	}
	if !hasRatelimit {
		t.Fatalf("no notification.ratelimited audit: %+v", audits)
	}
}

func TestPermissionCatalogAndTokenViews(t *testing.T) {
	env := setupAPI(t)

	// Catalog endpoint.
	status, out := env.serve("GET", "/api/v1/api-tokens/permissions/catalog", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("catalog status %d: %s", status, out)
	}
	var cat struct {
		Categories  []map[string]any `json:"categories"`
		Permissions []map[string]any `json:"permissions"`
	}
	if err := json.Unmarshal(out, &cat); err != nil {
		t.Fatalf("decode catalog %s: %v", out, err)
	}
	if len(cat.Categories) == 0 || len(cat.Permissions) == 0 {
		t.Fatalf("catalog empty: %s", out)
	}
	catNames := map[string]bool{}
	for _, c := range cat.Categories {
		catNames[c["category"].(string)] = true
	}
	if !catNames["notifications"] || !catNames["ai_credentials"] {
		t.Fatalf("catalog missing categories: %s", out)
	}
	sendDefined := false
	for _, p := range cat.Permissions {
		if p["resource"] == "notifications" && p["action"] == "send" {
			sendDefined = true
		}
	}
	if !sendDefined {
		t.Fatalf("catalog missing notifications:send: %s", out)
	}

	// New token has zero structured permissions in create/detail/list.
	id, tok := createAPIToken(t, env, "view", "1h")
	_ = tok
	status, out = env.serve("GET", "/api/v1/api-tokens/"+id, nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("detail status %d: %s", status, out)
	}
	detail := mustDecode[struct {
		APIToken struct {
			Permissions struct {
				Version int   `json:"version"`
				Grants  []any `json:"grants"`
			} `json:"permissions"`
		} `json:"api_token"`
	}](t, out)
	if detail.APIToken.Permissions.Version != 1 || len(detail.APIToken.Permissions.Grants) != 0 {
		t.Fatalf("new token should have zero grants: %s", out)
	}

	// Grant notifications:send -> structured permissions in detail + list.
	grantNotificationsPermission(t, env, id, 1)
	status, out = env.serve("GET", "/api/v1/api-tokens/"+id, nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("detail after grant status %d: %s", status, out)
	}
	detail2 := mustDecode[struct {
		APIToken struct {
			Permissions struct {
				Version int `json:"version"`
				Grants  []struct {
					Resource string   `json:"resource"`
					Actions  []string `json:"actions"`
				} `json:"grants"`
			} `json:"permissions"`
		} `json:"api_token"`
	}](t, out)
	if len(detail2.APIToken.Permissions.Grants) != 1 || detail2.APIToken.Permissions.Grants[0].Resource != "notifications" {
		t.Fatalf("grant not reflected in detail: %s", out)
	}
	status, out = env.serve("GET", "/api/v1/api-tokens", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("list status %d: %s", status, out)
	}
	list := mustDecode[struct {
		APITokens []struct {
			Permissions map[string]any `json:"permissions"`
		} `json:"api_tokens"`
	}](t, out)
	if len(list.APITokens) == 0 || list.APITokens[0].Permissions == nil {
		t.Fatalf("list missing structured permissions: %s", out)
	}

	// Optimistic lock: a stale revision conflicts.
	status, out = env.serve("PUT", "/api/v1/api-tokens/"+id+"/permissions", map[string]any{
		"permission_version": 1,
		"permissions":        map[string]any{"version": 1, "grants": []map[string]any{{"resource": "notifications", "actions": []string{"send"}}}},
	}, env.adminHeaders())
	if status != http.StatusConflict {
		t.Fatalf("stale revision should be 409, got %d: %s", status, out)
	}

	// Unknown permission in the grant set -> 400.
	status, out = env.serve("PUT", "/api/v1/api-tokens/"+id+"/permissions", map[string]any{
		"permission_version": 2,
		"permissions":        map[string]any{"version": 1, "grants": []map[string]any{{"resource": "notifications", "actions": []string{"delete"}}}},
	}, env.adminHeaders())
	if status != http.StatusBadRequest {
		t.Fatalf("unknown action should be 400, got %d: %s", status, out)
	}

	// Revoking the token makes further permission updates conflict.
	status, _ = env.post("/api/v1/api-tokens/"+id+"/revoke", map[string]any{"reason": "test"}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("revoke status %d", status)
	}
	status, out = env.serve("PUT", "/api/v1/api-tokens/"+id+"/permissions", map[string]any{
		"permission_version": 2,
		"permissions":        map[string]any{"version": 1, "grants": []map[string]any{{"resource": "notifications", "actions": []string{"send"}}}},
	}, env.adminHeaders())
	if status != http.StatusConflict {
		t.Fatalf("PUT after revoke should be 409, got %d: %s", status, out)
	}
}
