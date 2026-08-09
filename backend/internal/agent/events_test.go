package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"servercli/internal/config"
	"servercli/internal/logger"
)

// TestWatchEventsTriggersHeartbeatOnKeyChange verifies the SSE lease-key event
// stream immediately triggers a heartbeat (which pulls install/remove ops),
// instead of waiting for the next scheduled heartbeat.
func TestWatchEventsTriggersHeartbeatOnKeyChange(t *testing.T) {
	var heartbeatCalls atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/events":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			fl, _ := w.(http.Flusher)
			fl.Flush()
			time.Sleep(200 * time.Millisecond)
			fmt.Fprint(w, "event: lease_keys_changed\ndata: {}\n\n")
			fl.Flush()
			// Keep the stream open so the agent can react before EOF.
			time.Sleep(2 * time.Second)
		case "/api/v1/agent/heartbeat":
			heartbeatCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"node_id":"n","status":"online"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cfg := config.Default()
	cfg.PrimaryBackendURL = ts.URL
	log := logger.New(io.Discard, "error")
	a := NewAgent(cfg, log)
	a.client.SetCredential("ncred_test")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.watchEvents(ctx); err != nil {
		t.Fatalf("watchEvents: %v", err)
	}
	if heartbeatCalls.Load() == 0 {
		t.Fatal("lease_keys_changed event did not trigger an immediate heartbeat")
	}
}
