import { useMemo, useState } from 'react';
import { useApi, errorMessage } from '../lib/useApi';
import { request, ApiError } from '../api/client';
import type { ApiRouteSpec } from '../api/types';
import {
  Badge,
  Card,
  EmptyState,
  ErrorState,
  LoadingState,
  Modal,
  PageHeader,
  TextInput,
} from '../components/ui';
import { cn } from '../lib/format';

const AUTH_LABEL: Record<string, { label: string; tone: 'teal' | 'indigo' | 'amber' | 'gray' | 'red' }> = {
  none: { label: '公开', tone: 'gray' },
  admin: { label: '管理员 Session', tone: 'indigo' },
  agent: { label: 'Agent HMAC', tone: 'amber' },
  token: { label: 'Access Token', tone: 'teal' },
  'admin|token': { label: '管理员或 Token', tone: 'indigo' },
  runtime: { label: 'Lease 运行时', tone: 'red' },
};

const DEBUG_DISALLOWED_AUTH = new Set(['agent', 'runtime']);

export default function ApiDirectoryPage() {
  const state = useApi<unknown>('/meta/openapi');
  const routes = useMemo(() => {
    const raw = (state.data as { paths?: ApiRouteSpec[] } | null)?.paths ?? [];
    return [...raw].sort((a, b) => (a.group ?? '').localeCompare(b.group ?? '') || a.path.localeCompare(b.path));
  }, [state.data]);

  const groups = useMemo(() => {
    const map = new Map<string, ApiRouteSpec[]>();
    for (const r of routes) {
      const g = r.group || '其他';
      if (!map.has(g)) map.set(g, []);
      map.get(g)!.push(r);
    }
    return [...map.entries()];
  }, [routes]);

  const [debugRoute, setDebugRoute] = useState<ApiRouteSpec | null>(null);

  if (state.loading && state.data === null) return <LoadingState label="加载接口目录中…" />;
  if (state.error) return <ErrorState message={errorMessage(state.error)} onRetry={state.reload} />;

  return (
    <div>
      <PageHeader title="接口中心" subtitle="全接口目录与在线调试：所有已注册路由的文档，管理员/Token 接口支持在线调试。" />
      <div className="alert alert-info" role="alert" style={{ marginBottom: 14 }}>
        <ul style={{ margin: 0, paddingLeft: 18 }}>
          <li>接口目录由后端同一份路由定义生成，不会与真实路由漂移。</li>
          <li>Agent/HMAC、服务器注册认领、WebSocket、SSE 与 Lease 运行时接口仅展示文档，不允许在线执行。</li>
          <li>在线调试中的 Access Token 仅保存在当前页面内存，不写入 localStorage、URL、日志或生成的 curl 示例。</li>
        </ul>
      </div>

      {groups.length === 0 && <EmptyState title="暂无接口" />}
      {groups.map(([group, specs]) => (
        <div key={group} style={{ marginBottom: 14 }}>
          <Card title={group}>
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th style={{ width: 70 }}>方法</th>
                  <th>路径</th>
                  <th>鉴权</th>
                  <th>说明</th>
                  <th style={{ width: 110 }}>操作</th>
                </tr>
              </thead>
              <tbody>
                {specs.map((r) => {
                  const auth = AUTH_LABEL[r.auth || 'none'] ?? AUTH_LABEL.none;
                  const debuggable = !!r.debug && !DEBUG_DISALLOWED_AUTH.has(r.auth || '');
                  return (
                    <tr key={r.method + r.path}>
                      <td>
                        <Badge tone={r.method === 'GET' ? 'teal' : r.method === 'POST' ? 'indigo' : r.method === 'DELETE' ? 'red' : 'amber'}>
                          {r.method}
                        </Badge>
                      </td>
                      <td className="mono" title={r.summary}>{r.path}</td>
                      <td><Badge tone={auth.tone}>{auth.label}</Badge></td>
                      <td className="muted">{r.summary || '—'}</td>
                      <td>
                        {debuggable ? (
                          <button className="btn btn-ghost btn-sm" onClick={() => setDebugRoute(r)}>调试</button>
                        ) : (
                          <span className="muted" style={{ fontSize: 12 }}>只读</span>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          </Card>
        </div>
      ))}

      <Modal open={debugRoute !== null} title={`在线调试：${debugRoute?.method || ''} ${debugRoute?.path || ''}`} onClose={() => setDebugRoute(null)} width={720}>
        {debugRoute && <DebugForm route={debugRoute} />}
      </Modal>
    </div>
  );
}

function DebugForm({ route }: { route: ApiRouteSpec }) {
  const pathParams = useMemo(
    () => (route.path.match(/\{([^}]+)\}/g) ?? []).map((m) => m.slice(1, -1)),
    [route.path],
  );
  const [pathValues, setPathValues] = useState<Record<string, string>>({});
  const [queryValues, setQueryValues] = useState<Record<string, string>>({});
  const [bodyText, setBodyText] = useState(route.body || '{}');
  const [token, setToken] = useState('');
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ status: number; body: string } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const isTokenAuth = route.auth === 'token';

  const buildPath = (): string | null => {
    let p = route.path;
    for (const param of pathParams) {
      const v = (pathValues[param] || '').trim();
      if (!v) return null;
      p = p.replace(`{${param}}`, encodeURIComponent(v));
    }
    return p;
  };

  const run = async () => {
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const path = buildPath();
      if (!path) {
        setError('请填写所有路径参数。');
        return;
      }
      const query: Record<string, string> = {};
      for (const [k, v] of Object.entries(queryValues)) {
        if (v && v.trim()) query[k] = v.trim();
      }
      let body: unknown;
      if (route.method !== 'GET' && bodyText.trim() !== '') {
        try {
          body = JSON.parse(bodyText);
        } catch {
          setError('请求体不是合法的 JSON。');
          return;
        }
      }
      const headers: Record<string, string> = {};
      if (isTokenAuth) {
        if (!token.trim()) {
          setError('该接口需要 Access Token（sct_*），请粘贴到下方输入框（仅保存在本页面内存）。');
          return;
        }
        headers.Authorization = `Bearer ${token.trim()}`;
      }
      const data = await request<unknown>(path, {
        method: route.method as 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
        body,
        query,
        headers,
        timeoutMs: 20000,
      });
      setResult({ status: 200, body: JSON.stringify(data, null, 2) });
    } catch (err) {
      if (err instanceof ApiError) {
        setResult({ status: err.status, body: JSON.stringify({ error: { code: err.code, message: err.message, details: err.details } }, null, 2) });
      } else {
        setError(String(err));
      }
    } finally {
      setBusy(false);
    }
  };

  const methodTone = route.method === 'GET' ? 'teal' : route.method === 'POST' ? 'indigo' : route.method === 'DELETE' ? 'red' : 'amber';

  return (
    <div>
      <div className="kv" style={{ marginBottom: 12 }}>
        <dt>鉴权</dt>
        <dd>
          <Badge tone={AUTH_LABEL[route.auth || 'none']?.tone ?? 'gray'}>{AUTH_LABEL[route.auth || 'none']?.label ?? route.auth}</Badge>
        </dd>
        {route.summary && (
          <>
            <dt>说明</dt>
            <dd>{route.summary}</dd>
          </>
        )}
      </div>

      <label className="field">
        <span className="field-label">请求路径</span>
        <div className="mono" style={{ background: 'var(--bg-soft, #f6f7f9)', padding: 8, borderRadius: 6, wordBreak: 'break-all' }}>
          <Badge tone={methodTone}>{route.method}</Badge> {route.path}
        </div>
      </label>

      {pathParams.length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 10 }}>
          {pathParams.map((p) => (
            <label className="field" key={p} style={{ margin: 0 }}>
              <span className="field-label">路径参数 {p} <em className="req">*</em></span>
              <TextInput value={pathValues[p] || ''} onChange={(e) => setPathValues((prev) => ({ ...prev, [p]: e.target.value }))} placeholder={`{${p}}`} />
            </label>
          ))}
        </div>
      )}

      {(route.params ?? []).filter((p) => p.in === 'query').length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 10 }}>
          {(route.params ?? [])
            .filter((p) => p.in === 'query')
            .map((p) => (
              <label className="field" key={p.name} style={{ margin: 0 }}>
                <span className="field-label">查询参数 {p.name}</span>
                <TextInput value={queryValues[p.name] || ''} onChange={(e) => setQueryValues((prev) => ({ ...prev, [p.name]: e.target.value }))} placeholder={p.description || p.name} />
              </label>
            ))}
        </div>
      )}

      {route.method !== 'GET' && (
        <label className="field">
          <span className="field-label">请求体（JSON）</span>
          <textarea
            className="input"
            rows={5}
            value={bodyText}
            onChange={(e) => setBodyText(e.target.value)}
            spellCheck={false}
          />
        </label>
      )}

      {isTokenAuth && (
        <label className="field">
          <span className="field-label">Access Token（仅本页面内存）</span>
          <TextInput
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder="sct_..."
            type="password"
            autoComplete="off"
          />
          <span className="field-hint">Token 不会保存到本地存储，也不会出现在生成的请求历史中。</span>
        </label>
      )}

      {error && <div className="alert alert-danger">{error}</div>}

      <div className="modal-actions" style={{ marginTop: 12 }}>
        <button type="button" className="btn btn-primary" onClick={run} disabled={busy}>
          {busy ? '请求中…' : '发送请求'}
        </button>
      </div>

      {result && (
        <div style={{ marginTop: 12 }}>
          <div className={cn('alert', result.status >= 200 && result.status < 300 ? 'alert-info' : 'alert-danger')} role="alert">
            状态码：<strong>{result.status}</strong>
          </div>
          <pre className="mono" style={{ maxHeight: 320, overflow: 'auto', background: 'var(--bg-soft, #f6f7f9)', padding: 10, borderRadius: 8, fontSize: 12 }}>
            {result.body}
          </pre>
        </div>
      )}
    </div>
  );
}
