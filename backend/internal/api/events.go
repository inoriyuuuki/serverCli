package api

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// Realtime event names pushed to connected clients.
// - Node agents receive EventLeaseKeysChanged to refresh keys immediately.
// - Admin UI receives the *_changed events to refresh lists in real time.
const (
	EventLeaseKeysChanged = "lease_keys_changed" // node agent: refresh lease keys
	EventLeasesChanged    = "leases_changed"     // admin UI: leases / lease requests / auto-approvals
	EventTasksChanged     = "tasks_changed"      // admin UI: tasks
	EventNodesChanged     = "nodes_changed"      // admin UI: nodes
)

// eventBroker fans out named events to subscribed channels. Node agents
// subscribe with their node_id (lease key refresh); the admin UI subscribes
// with the global scope "" (list refreshes). Nodes are outbound-only, so the
// control plane pushes over the agent's long-lived WebSocket.
type eventBroker struct {
	mu   sync.Mutex
	subs map[string]map[chan string]struct{}
}

func newEventBroker() *eventBroker {
	return &eventBroker{subs: make(map[string]map[chan string]struct{})}
}

func (b *eventBroker) subscribe(scope string) (chan string, func()) {
	ch := make(chan string, 64)
	b.mu.Lock()
	if b.subs[scope] == nil {
		b.subs[scope] = make(map[chan string]struct{})
	}
	b.subs[scope][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if m := b.subs[scope]; m != nil {
			if _, ok := m[ch]; ok {
				delete(m, ch)
				close(ch)
			}
			if len(m) == 0 {
				delete(b.subs, scope)
			}
		}
	}
}

func (b *eventBroker) publish(scope, event string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[scope] {
		select {
		case ch <- event:
		default: // slow/disconnected subscriber misses it and falls back to polling
		}
	}
}

// wsUpgrader upgrades authenticated agent/admin connections. Origin is
// accepted: agent connections are signature-authenticated and the admin UI is
// same-origin + cookie-authenticated (read-only push, no CSRF surface).
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func writeWSEvent(conn *websocket.Conn, event string) bool {
	msg, err := json.Marshal(map[string]string{"event": event})
	if err != nil {
		return false
	}
	return conn.WriteMessage(websocket.TextMessage, msg) == nil
}

// pumpRead discards inbound frames until the client disconnects, then closes
// done so the writer loop can exit.
func pumpRead(conn *websocket.Conn, done chan<- struct{}) {
	defer close(done)
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// handleAgentWS streams realtime events to the calling node agent (signed
// agent auth). The agent refreshes lease keys on EventLeaseKeysChanged.
func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	node := nodeFrom(r.Context())
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("agent ws upgrade failed", "error", err)
		return
	}
	defer conn.Close()
	ch, unsubscribe := s.events.subscribe(node.ID)
	defer unsubscribe()
	done := make(chan struct{})
	go pumpRead(conn, done)
	for {
		select {
		case <-done:
			return
		case ev := <-ch:
			if !writeWSEvent(conn, ev) {
				return
			}
		}
	}
}

// handleAdminWS streams realtime events to the browser admin UI. Auth uses
// the session cookie only (WebSocket clients cannot set custom headers; this
// endpoint is read-only push so no CSRF token is required).
func (s *Server) handleAdminWS(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("servercli_session")
	if err != nil {
		writeError(w, r, s.log, http.StatusUnauthorized, "UNAUTHENTICATED", "login required", nil)
		return
	}
	if _, _, err := s.auth.Authenticate(r.Context(), cookie.Value); err != nil {
		writeError(w, r, s.log, http.StatusUnauthorized, "UNAUTHENTICATED", "session invalid or expired", nil)
		return
	}
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("admin ws upgrade failed", "error", err)
		return
	}
	defer conn.Close()
	ch, unsubscribe := s.events.subscribe("")
	defer unsubscribe()
	done := make(chan struct{})
	go pumpRead(conn, done)
	for {
		select {
		case <-done:
			return
		case ev := <-ch:
			if !writeWSEvent(conn, ev) {
				return
			}
		}
	}
}
