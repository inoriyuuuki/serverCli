import { useEffect, useRef } from 'react';

/** Real-time events pushed by the control plane WebSocket (/api/v1/ws). */
export type RealtimeEvent = 'leases_changed' | 'tasks_changed' | 'nodes_changed';

type Handler = () => void;

const listeners = new Map<string, Set<Handler>>();
let ws: WebSocket | null = null;
let retryMs = 1000;

function ensureConnection() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${proto}//${window.location.host}/api/v1/ws`);
  ws.onopen = () => {
    retryMs = 1000;
  };
  ws.onmessage = (ev) => {
    try {
      const data = JSON.parse(String(ev.data)) as { event?: string };
      if (data.event) {
        const set = listeners.get(data.event);
        if (set) set.forEach((h) => h());
      }
    } catch {
      // ignore malformed frames
    }
  };
  ws.onclose = () => {
    ws = null;
    setTimeout(ensureConnection, retryMs);
    retryMs = Math.min(retryMs * 2, 15000);
  };
  ws.onerror = () => {
    try {
      ws?.close();
    } catch {
      // noop
    }
  };
}

/** Subscribe to realtime events; returns an unsubscribe function. */
export function onRealtime(events: string[], handler: Handler): () => void {
  for (const ev of events) {
    if (!listeners.has(ev)) listeners.set(ev, new Set());
    listeners.get(ev)!.add(handler);
  }
  ensureConnection();
  return () => {
    for (const ev of events) {
      const set = listeners.get(ev);
      if (set) {
        set.delete(handler);
        if (set.size === 0) listeners.delete(ev);
      }
    }
  };
}

/** React hook: calls handler whenever any of the given events arrive. */
export function useRealtime(events: string[], handler: () => void) {
  const ref = useRef(handler);
  ref.current = handler;
  useEffect(() => onRealtime(events, () => ref.current()), [events.join('|')]);
}
