import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { useNavigate } from 'react-router-dom';
import { api, setCsrfToken, setUnauthorizedHandler, ApiError } from '../api/client';
import type { SessionInfo } from '../api/types';

/**
 * Normalize the `/api/v1/auth/session` response. The backend is developed in
 * parallel, so this tolerates several plausible envelope shapes:
 *
 *   { session: { csrf_token, admin: {...}, expires_at }, environment, role, node: {...} }
 *   { session: { csrfToken, user: {...} }, env, node_role, node: {...} }
 *   { csrf_token, environment, role, node: {...} }
 */
function normalizeSession(data: unknown): SessionInfo | null {
  if (!data || typeof data !== 'object') return null;
  const root = data as Record<string, unknown>;
  const session =
    root.session && typeof root.session === 'object' && !Array.isArray(root.session)
      ? (root.session as Record<string, unknown>)
      : root;

  const node =
    session.node && typeof session.node === 'object' && !Array.isArray(session.node)
      ? (session.node as Record<string, unknown>)
      : root.node && typeof root.node === 'object' && !Array.isArray(root.node)
        ? (root.node as Record<string, unknown>)
        : null;

  const admin =
    session.admin && typeof session.admin === 'object' && !Array.isArray(session.admin)
      ? (session.admin as Record<string, unknown>)
      : session.user && typeof session.user === 'object' && !Array.isArray(session.user)
        ? (session.user as Record<string, unknown>)
        : null;

  // Backend returns environment as an object: { instance_name, node_role, app_env }.
  const envObj =
    session.environment && typeof session.environment === 'object' && !Array.isArray(session.environment)
      ? (session.environment as Record<string, unknown>)
      : root.environment && typeof root.environment === 'object' && !Array.isArray(root.environment)
        ? (root.environment as Record<string, unknown>)
        : null;

  const environment = firstString(
    envObj?.app_env,
    envObj?.APP_ENV,
    session.environment,
    session.env,
    root.environment,
    root.env,
    session.app_env,
    session.APP_ENV,
  );
  const role = firstString(
    envObj?.node_role,
    envObj?.NODE_ROLE,
    session.role,
    session.node_role,
    root.role,
    root.node_role,
    node?.role,
    session.NODE_ROLE,
  );
  const nodeId = firstString(
    node?.id,
    node?.node_id,
    session.node_id,
    root.node_id,
    session.nodeId,
  );
  const nodeName = firstString(
    node?.alias,
    envObj?.instance_name,
    envObj?.INSTANCE_NAME,
    node?.instance_name,
    node?.name,
    node?.hostname,
    session.node_name,
    root.node_name,
    session.instance_name,
    root.instance_name,
  );
  const nodeIp = firstString(
    node?.ip,
    node?.primary_ip,
    session.node_ip,
    root.node_ip,
    session.ip,
  );
  const adminUsername = firstString(
    admin?.username,
    admin?.name,
    session.username,
    root.username,
    session.admin_username,
  );
  const csrf = firstString(
    session.csrf_token,
    session.csrfToken,
    session.CSRFToken,
    root.csrf_token,
    root.csrfToken,
  );
  const expiresAt = firstString(
    session.expires_at,
    session.expiresAt,
    admin?.expires_at,
    root.expires_at,
    root.session_expires_at,
  );

  return {
    environment: environment ?? 'test',
    role: role ?? 'primary',
    nodeId: nodeId ?? null,
    nodeName: nodeName ?? null,
    nodeIp: nodeIp ?? null,
    adminUsername: adminUsername ?? null,
    csrfToken: csrf ?? null,
    expiresAt: expiresAt ?? null,
    raw: data,
  };
}

function firstString(...values: unknown[]): string | null {
  for (const v of values) {
    if (typeof v === 'string' && v.length > 0) return v;
  }
  return null;
}

interface AuthContextValue {
  session: SessionInfo | null;
  loading: boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  refreshSession: () => Promise<SessionInfo | null>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<SessionInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const navigate = useNavigate();
  const sessionRef = useRef<SessionInfo | null>(null);

  const applySession = useCallback((info: SessionInfo | null) => {
    sessionRef.current = info;
    setSession(info);
    setCsrfToken(info?.csrfToken ?? null);
  }, []);

  const clearSession = useCallback(() => {
    applySession(null);
    navigate('/login', { replace: true });
  }, [applySession, navigate]);

  // Global 401 handler: clear state and go back to login.
  useEffect(() => {
    setUnauthorizedHandler(() => clearSession());
    return () => setUnauthorizedHandler(null);
  }, [clearSession]);

  const refreshSession = useCallback(async (): Promise<SessionInfo | null> => {
    try {
      const data = await api.get('/auth/session');
      const info = normalizeSession(data);
      if (info) applySession(info);
      return info;
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        applySession(null);
        return null;
      }
      throw err;
    }
  }, [applySession]);

  // Initial session load.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const info = await refreshSession();
        if (!cancelled && info) applySession(info);
      } catch {
        if (!cancelled) applySession(null);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [refreshSession, applySession]);

  const login = useCallback(
    async (username: string, password: string) => {
      // Login failure is intentionally generic — no user-existence leaks.
      try {
        await api.post('/auth/login', { username, password });
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) {
          throw new ApiError(401, 'INVALID_CREDENTIALS', '登录失败，请检查用户名和密码');
        }
        throw err;
      }
      const info = await refreshSession();
      if (!info) {
        throw new ApiError(401, 'INVALID_CREDENTIALS', '登录失败，请检查用户名和密码');
      }
      applySession(info);
    },
    [refreshSession, applySession],
  );

  const logout = useCallback(async () => {
    try {
      await api.post('/auth/logout');
    } catch {
      // Even if logout fails server-side, clear local state.
    }
    applySession(null);
    navigate('/login', { replace: true });
  }, [applySession, navigate]);

  const value = useMemo<AuthContextValue>(
    () => ({ session, loading, login, logout, refreshSession }),
    [session, loading, login, logout, refreshSession],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>');
  return ctx;
}

export function useSession(): SessionInfo | null {
  return useAuth().session;
}
