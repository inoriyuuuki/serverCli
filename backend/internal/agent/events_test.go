package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"servercli/internal/config"
	"servercli/internal/logger"
)

// TestWatchEventsTriggersHeartbeatOnKeyChange verifies the WebSocket lease-key
// event stream immediately triggers a heartbeat (which pulls install/remove
// ops), instead of waiting for the next scheduled heartbeat.
func TestWatchEventsTriggersHeartbeatOnKeyChange(t *testing.T) {
	var heartbeatCalls atomic.Int64
	up := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(200 * time.Millisecond)
		msg, _ := json.Marshal(map[string]string{"event": "lease_keys_changed"})
		_ = conn.WriteMessage(websocket.TextMessage, msg)
		// Keep the connection open so the agent can react before EOF.
		time.Sleep(2 * time.Second)
	})
	mux.HandleFunc("/api/v1/agent/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		heartbeatCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"node_id":"n","status":"online"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := config.Default()
	cfg.PrimaryBackendURL = ts.URL
	log := logger.New(io.Discard, "error")
	a := NewAgent(cfg, log)
	a.client.SetCredential("ncred_test")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The test server closes the connection after sending the event; watchEvents
	// returning an error on close is expected. What matters is the heartbeat.
	_ = a.watchEvents(ctx)
	if heartbeatCalls.Load() == 0 {
		t.Fatal("lease_keys_changed event did not trigger an immediate heartbeat")
	}
}
