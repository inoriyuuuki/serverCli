/**
 * Unified API client for ServerCLI.
 *
 * Conventions (see doc/11_IMPLEMENTATION_CONTRACT.md):
 * - All admin APIs live under `/api/v1`; same-origin relative paths.
 * - Session cookie `servercli_session` (HttpOnly) is sent via credentials.
 * - Every write request carries `X-CSRF-Token` fetched from `/api/v1/auth/session`.
 * - Errors are normalized to `{ error: { code, message, request_id, details } }`.
 * - A 401 anywhere clears the local session and redirects to the login page.
 */

export interface ApiErrorBody {
  code?: string;
  message?: string;
  request_id?: string;
  details?: unknown;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId?: string;
  readonly details?: unknown;

  constructor(status: number, code: string, message: string, requestId?: string, details?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.requestId = requestId;
    this.details = details;
  }
}

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  /** JSON-serializable body; stringified automatically. */
  body?: unknown;
  /** Extra headers (e.g. Idempotency-Key). */
  headers?: Record<string, string>;
  /** Query params; arrays become repeated keys, undefined/null/'' are skipped. */
  query?: Record<string, string | number | boolean | undefined | null | string[]>;
  /** Custom headers for the path (defaults to `/api/v1`). */
  base?: string;
  timeoutMs?: number;
}

const API_PREFIX = '/api/v1';
/** Root-level (non-versioned) paths served by the control plane. */
const ROOT_PATHS = new Set(['/health/live', '/health/ready', '/version']);

let csrfToken: string | null = null;
let onUnauthorized: (() => void) | null = null;

export function setCsrfToken(token: string | null): void {
  csrfToken = token;
}

export function getCsrfToken(): string | null {
  return csrfToken;
}

/** Registered by AuthProvider so any 401 clears state and redirects to /login. */
export function setUnauthorizedHandler(handler: (() => void) | null): void {
  onUnauthorized = handler;
}

function buildUrl(path: string, query?: RequestOptions['query']): string {
  const normalized = path.startsWith('/') ? path : `/${path}`;
  // All admin APIs live under /api/v1; only health/version are root-level.
  const base = ROOT_PATHS.has(normalized) ? normalized : `${API_PREFIX}${normalized}`;
  if (!query) return base;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === '') continue;
    if (Array.isArray(value)) {
      for (const v of value) {
        if (v !== undefined && v !== null && v !== '') params.append(key, String(v));
      }
    } else {
      params.append(key, String(value));
    }
  }
  const qs = params.toString();
  return qs ? `${base}?${qs}` : base;
}

export async function request<T = unknown>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? (options.body !== undefined ? 'POST' : 'GET');
  const url = buildUrl(path, options.query);

  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...options.headers,
  };
  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json';
  }
  // CSRF token is required for all state-changing requests.
  if (method !== 'GET' && csrfToken) {
    headers['X-CSRF-Token'] = csrfToken;
  }

  let res: Response;
  try {
    const controller = new AbortController();
    const timer = options.timeoutMs
      ? setTimeout(() => controller.abort(), options.timeoutMs)
      : undefined;
    try {
      res = await fetch(url, {
        method,
        headers,
        credentials: 'include',
        body: options.body !== undefined ? JSON.stringify(options.body) : undefined,
        signal: controller.signal,
      });
    } finally {
      if (timer) clearTimeout(timer);
    }
  } catch (err) {
    const aborted = err instanceof DOMException && err.name === 'AbortError';
    throw new ApiError(
      0,
      aborted ? 'TIMEOUT' : 'NETWORK_ERROR',
      aborted ? '请求超时，请稍后重试' : '无法连接服务器（网络错误），请检查网络或稍后重试',
      undefined,
      { cause: String(err) },
    );
  }

  if (res.status === 401) {
    if (onUnauthorized) onUnauthorized();
    throw new ApiError(401, 'UNAUTHORIZED', '会话已过期或未登录，请重新登录');
  }

  let data: unknown = null;
  const text = await res.text();
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }

  if (!res.ok) {
    const body = (data as { error?: ApiErrorBody } | null)?.error;
    throw new ApiError(
      res.status,
      body?.code ?? `HTTP_${res.status}`,
      body?.message ?? `请求失败（HTTP ${res.status}）`,
      body?.request_id,
      body?.details,
    );
  }

  return data as T;
}

export const api = {
  get: <T = unknown>(path: string, query?: RequestOptions['query'], options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'GET', query }),
  post: <T = unknown>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'POST', body }),
  patch: <T = unknown>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'PATCH', body }),
  put: <T = unknown>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'PUT', body }),
  delete: <T = unknown>(path: string, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'DELETE' }),
};

/** Best-effort extraction of a list from an unknown response envelope. */
export function unwrapList<T = unknown>(data: unknown, keys: string[] = []): T[] {
  if (Array.isArray(data)) return data as T[];
  if (data && typeof data === 'object' && !Array.isArray(data)) {
    const d = data as Record<string, unknown>;
    for (const key of ['items', 'data', 'list', 'results', ...keys]) {
      const v = d[key];
      if (Array.isArray(v)) return v as T[];
    }
    // Fallback: first array value in the envelope.
    for (const v of Object.values(d)) {
      if (Array.isArray(v)) return v as T[];
    }
  }
  return [];
}

/** Best-effort extraction of a single object from an unknown response envelope. */
export function unwrapObject<T = Record<string, unknown>>(data: unknown, keys: string[] = []): T | null {
  if (data && typeof data === 'object' && !Array.isArray(data)) {
    const d = data as Record<string, unknown>;
    for (const key of ['data', 'item', ...keys]) {
      const v = d[key];
      if (v && typeof v === 'object' && !Array.isArray(v)) return v as T;
    }
    return d as T;
  }
  return null;
}
