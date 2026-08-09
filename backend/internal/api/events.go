package api

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// EventLeaseKeysChanged notifies a node that its lease key set changed
// (new lease issued / revoked / disconnected), so the agent refreshes keys
// immediately instead of waiting for the next heartbeat.
const EventLeaseKeysChanged = "lease_keys_changed"

// eventBroker fans out node-scoped server-sent events to connected node agents.
// Node agents are outbound-only, so the control plane cannot push directly;
// SSE over the agent's existing signed channel is the notification path.
type eventBroker struct {
	mu   sync.Mutex
	subs map[string]map[chan string]struct{} // nodeID -> subscribers
}

func newEventBroker() *eventBroker {
	return &eventBroker{subs: make(map[string]map[chan string]struct{})}
}

// subscribe registers a channel for nodeID and returns an unsubscribe func.
func (b *eventBroker) subscribe(nodeID string) (chan string, func()) {
	ch := make(chan string, 32)
	b.mu.Lock()
	if b.subs[nodeID] == nil {
		b.subs[nodeID] = make(map[chan string]struct{})
	}
	b.subs[nodeID][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if m := b.subs[nodeID]; m != nil {
			if _, ok := m[ch]; ok {
				delete(m, ch)
				close(ch)
			}
			if len(m) == 0 {
				delete(b.subs, nodeID)
			}
		}
	}
}

// publish sends event to all subscribers of nodeID (non-blocking; slow or
// disconnected subscribers simply miss the notification and fall back to the
// next heartbeat).
func (b *eventBroker) publish(nodeID, event string) {
	if nodeID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[nodeID] {
		select {
		case ch <- event:
		default:
		}
	}
}

// handleAgentEvents streams server-sent events for the calling node agent.
// Kept alive with periodic ": ping" comments; the client reconnects on drop.
func (s *Server) handleAgentEvents(w http.ResponseWriter, r *http.Request) {
	node := nodeFrom(r.Context())
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, s.log, http.StatusInternalServerError, "STREAM_UNSUPPORTED", "streaming unsupported", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsubscribe := s.events.subscribe(node.ID)
	defer unsubscribe()
	// Flush headers immediately so the client connection is established
	// before the first event arrives.
	flusher.Flush()

	ctx := r.Context()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			if _, err := fmt.Fprintf(w, "event: %s\ndata: {}\n\n", ev); err != nil {
				return
			}
			flusher.Flush()
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
