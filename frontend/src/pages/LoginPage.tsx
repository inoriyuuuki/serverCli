import { useEffect, useState, type FormEvent } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useAuth } from '../auth/AuthContext';
import { ApiError } from '../api/client';
import { Badge } from '../components/ui';
import type { SystemInfo } from '../api/types';

export default function LoginPage() {
  const { session, login, loading } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [sysInfo, setSysInfo] = useState<SystemInfo | null>(null);

  // Already logged in -> overview.
  useEffect(() => {
    if (session && !loading) {
      navigate('/', { replace: true });
    }
  }, [session, loading, navigate]);

  // Environment banner works even when logged out.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch('/api/v1/system/info', { credentials: 'include' });
        if (res.ok) {
          const data = (await res.json()) as SystemInfo;
          if (!cancelled) setSysInfo(data);
        }
      } catch {
        /* ignore — banner is optional */
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const isProd = sysInfo?.environment === 'production';

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (submitting) return;
    setError(null);
    setSubmitting(true);
    try {
      await login(username.trim(), password);
      const from = (location.state as { from?: string } | null)?.from;
      navigate(from || '/', { replace: true });
    } catch (err) {
      // Generic message — do not reveal whether username or password was wrong.
      if (err instanceof ApiError && err.code === 'NETWORK_ERROR') {
        setError('无法连接服务器，请检查网络后重试。');
      } else if (err instanceof ApiError && err.code === 'UNAUTHORIZED') {
        setError('登录失败，请检查用户名和密码。');
      } else if (err instanceof ApiError) {
        setError(err.message || '登录失败，请稍后重试。');
      } else {
        setError('登录失败，请稍后重试。');
      }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="login-page">
      <div className="login-card">
        <div className="login-brand">
          <span className="brand-mark">SC</span>
          <h1>ServerCLI</h1>
          <p>服务器集群控制管理系统</p>
          <div className="pill-row" style={{ justifyContent: 'center', marginTop: 8 }}>
            {sysInfo?.environment && (
              <Badge tone={isProd ? 'red' : 'blue'}>{isProd ? '正式环境' : '测试环境'}</Badge>
            )}
            {sysInfo?.role && (
              <Badge tone={sysInfo.role === 'primary' ? 'indigo' : 'teal'}>
                {sysInfo.role === 'primary' ? '主服务器' : '子服务器'}
              </Badge>
            )}
          </div>
        </div>

        {error && (
          <div className="alert alert-danger" role="alert">
            {error}
          </div>
        )}

        <form onSubmit={handleSubmit}>
          <label className="field">
            <span className="field-label">用户名</span>
            <input
              className="input"
              autoComplete="username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              required
              autoFocus
            />
          </label>
          <label className="field">
            <span className="field-label">密码</span>
            <input
              className="input"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
            />
          </label>
          <button className="btn btn-primary" type="submit" disabled={submitting} style={{ width: '100%' }}>
            {submitting ? '登录中…' : '登录'}
          </button>
        </form>
      </div>
    </div>
  );
}
