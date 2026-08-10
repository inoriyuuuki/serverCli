/** Formatting helpers shared across pages. All timestamps are RFC3339 UTC. */

export function cn(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ');
}

export function shortId(id: string | null | undefined, len = 8): string {
  if (!id) return '—';
  return id.length > len ? id.slice(0, len) : id;
}

export function utcString(iso?: string | null): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return `${d.toISOString().replace('T', ' ').replace(/\.\d{3}Z$/, ' UTC')}`;
}

export function formatDateTime(iso?: string | null): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return String(iso);
  return d.toLocaleString('zh-CN', { hour12: false });
}

/** Renders time as YYYY-MM-DD HH:MM:SS for query inputs (datetime-local). */
export function toLocalInputValue(iso?: string | null): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function parseLocalInput(value: string): string | undefined {
  if (!value) return undefined;
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return undefined;
  return d.toISOString();
}

export function formatDuration(totalSeconds: number | null | undefined): string {
  if (totalSeconds === null || totalSeconds === undefined || Number.isNaN(totalSeconds)) return '—';
  const s = Math.max(0, Math.round(totalSeconds));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m ${s % 60}s`;
  const d = Math.floor(h / 24);
  return `${d}d ${h % 24}h ${m % 60}m`;
}

export function durationBetween(fromIso?: string | null, toIso?: string | null): string {
  if (!fromIso) return '—';
  const a = new Date(fromIso).getTime();
  const b = toIso ? new Date(toIso).getTime() : Date.now();
  if (Number.isNaN(a) || Number.isNaN(b)) return '—';
  return formatDuration(Math.round((b - a) / 1000));
}

export function formatBytes(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '—';
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB', 'PB'];
  let v = n;
  let i = -1;
  do {
    v /= 1024;
    i += 1;
  } while (v >= 1024 && i < units.length - 1);
  return `${v.toFixed(v >= 100 ? 0 : 1)} ${units[i]}`;
}

export function formatPercent(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '—';
  return `${Number(n).toFixed(1)}%`;
}

export function formatNumber(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '—';
  return Number(n).toLocaleString('zh-CN');
}

export function formatUptime(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || Number.isNaN(seconds)) return '—';
  return formatDuration(seconds);
}

export function remainingText(expiresIso?: string | null, now = Date.now()): { text: string; kind: 'ok' | 'warn' | 'over' | 'none' } {
  if (!expiresIso) return { text: '—', kind: 'none' };
  const ms = new Date(expiresIso).getTime() - now;
  if (Number.isNaN(ms)) return { text: '—', kind: 'none' };
  if (ms <= 0) return { text: '已到期', kind: 'over' };
  const mins = ms / 60000;
  if (mins < 15) return { text: formatDuration(ms / 1000), kind: 'warn' };
  return { text: formatDuration(ms / 1000), kind: 'ok' };
}

/** Mask values whose key looks sensitive (passwords, secrets, tokens, keys). */
const SENSITIVE_KEY_RE = /(password|passwd|secret|token|api[_-]?key|private[_-]?key|authorization|cookie)/i;
export function isSensitiveKey(key: string): boolean {
  return SENSITIVE_KEY_RE.test(key);
}

export function maskValue(key: string, value: unknown): unknown {
  if (isSensitiveKey(key) && value !== undefined && value !== null && value !== '') {
    return '••••••（已脱敏）';
  }
  return value;
}

/** Recursively build a redacted preview of task/command arguments. */
export function redact(value: unknown, key = ''): unknown {
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      out[k] = redact(v, k);
    }
    return out;
  }
  if (Array.isArray(value)) return value.map((v, i) => redact(v, `${key}[${i}]`));
  return maskValue(key, value);
}

export function jsonSummary(value: unknown, maxLen = 500): string {
  let s: string;
  try {
    s = JSON.stringify(value, null, 2);
  } catch {
    s = String(value);
  }
  return s.length > maxLen ? `${s.slice(0, maxLen)}…（已截断）` : s;
}

export function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && crypto.randomUUID) return crypto.randomUUID();
  return `ui-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

/** Humanized labels for common enums. */
export const ROLE_LABEL: Record<string, string> = {
  primary: '主服务器',
  child: '子服务器',
};
