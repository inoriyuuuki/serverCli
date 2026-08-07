import type { ReactNode } from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { useAuth } from './AuthContext';

/** Blocks unauthenticated access; redirects to /login preserving the intended location. */
export function RequireAuth({ children }: { children: ReactNode }) {
  const { session, loading } = useAuth();
  const location = useLocation();

  if (loading) {
    return (
      <div className="page-center">
        <div className="spinner" aria-label="正在加载会话" />
        <p className="muted">正在恢复会话…</p>
      </div>
    );
  }
  if (!session) {
    return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />;
  }
  return <>{children}</>;
}

/** Guards routes that are only available on primary nodes. */
export function RequirePrimary({ children }: { children: ReactNode }) {
  const { session } = useAuth();
  if (!session || session.role === 'primary') return <>{children}</>;
  return <Navigate to="/" replace />;
}

/** For child nodes: restricted nav labels/redirect helpers. */
export function isPrimary(session: { role?: string } | null): boolean {
  return !session || session.role === 'primary';
}
