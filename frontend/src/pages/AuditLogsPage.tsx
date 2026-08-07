import { useMemo, useState } from 'react';
import { useSession } from '../auth/AuthContext';
import { useApi, errorMessage } from '../lib/useApi';
import { unwrapList } from '../api/client';
import type { AuditEvent, NodeInfo } from '../api/types';
import {
  Badge,
  Card,
  Checkbox,
  EmptyState,
  ErrorState,
  LoadingState,
  Modal,
  PageHeader,
  RiskBadge,
  Select,
  StatusBadge,
  TextInput,
  TimeCell,
} from '../components/ui';
import { nodeName } from '../components/NodeInfo';
import { parseLocalInput, shortId } from '../lib/format';

export default function AuditLogsPage() {
  const session = useSession();
  const isPrimary = !session || session.role === 'primary';

  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [nodeFilter, setNodeFilter] = useState('');
  const [actorType, setActorType] = useState('');
  const [actorId, setActorId] = useState('');
  const [action, setAction] = useState('');
  const [resourceType, setResourceType] = useState('');
  const [result, setResult] = useState('');
  const [risk, setRisk] = useState('');
  const [relatedId, setRelatedId] = useState('');
  const [importantOnly, setImportantOnly] = useState(false);
  const [page, setPage] = useState(1);

  const nodesState = useApi<unknown>(isPrimary ? '/nodes' : null);
  const nodes = useMemo(() => unwrapList<NodeInfo>(nodesState.data, ['nodes']), [nodesState.data]);

  const query = useMemo(() => {
    const q: Record<string, string> = {};
    const fromIso = parseLocalInput(from);
    const toIso = parseLocalInput(to);
    if (fromIso) q.from = fromIso;
    if (toIso) q.to = toIso;
    if (nodeFilter) q.node_id = nodeFilter;
    if (actorType) q.actor_type = actorType;
    if (actorId.trim()) q.actor_id = actorId.trim();
    if (action.trim()) q.action = action.trim();
    if (resourceType.trim()) q.resource_type = resourceType.trim();
    if (result) q.result = result;
    if (risk) q.risk_level = risk;
    if (relatedId.trim()) q.related_id = relatedId.trim();
    if (importantOnly) q.important = 'true';
    q.page = String(page);
    q.page_size = '50';
    return q;
  }, [from, to, nodeFilter, actorType, actorId, action, resourceType, result, risk, relatedId, importantOnly, page]);

  const state = useApi<unknown>('/audit-events', { query, deps: [page] });
  const events = useMemo(() => unwrapList<AuditEvent>(state.data, ['events']), [state.data]);
  const [detail, setDetail] = useState<AuditEvent | null>(null);

  const resetFilters = () => {
    setFrom('');
    setTo('');
    setNodeFilter('');
    setActorType('');
    setActorId('');
    setAction('');
    setResourceType('');
    setResult('');
    setRisk('');
    setRelatedId('');
    setImportantOnly(false);
    setPage(1);
  };

  const detailFields = useMemo(() => {
    if (!detail) return [];
    const skip = new Set(['id', 'occurred_at', 'details_json']);
    const fields: Array<[string, unknown]> = [];
    for (const [k, v] of Object.entries(detail)) {
      if (skip.has(k)) continue;
      if (v === undefined || v === null || v === '') continue;
      fields.push([k, v]);
    }
    return fields;
  }, [detail]);

  return (
    <div>
      <PageHeader title={isPrimary ? '审计日志' : '本机审计'} subtitle="所有操作与安全事件的结构化记录。" />

      <Card>
        <div className="filter-bar">
          <div className="filter-group">
            <span className="filter-label">开始时间</span>
            <TextInput type="datetime-local" value={from} onChange={(e) => { setFrom(e.target.value); setPage(1); }} />
          </div>
          <div className="filter-group">
            <span className="filter-label">结束时间</span>
            <TextInput type="datetime-local" value={to} onChange={(e) => { setTo(e.target.value); setPage(1); }} />
          </div>
          {isPrimary && (
            <div className="filter-group">
              <span className="filter-label">节点</span>
              <Select value={nodeFilter} onChange={(e) => { setNodeFilter(e.target.value); setPage(1); }}>
                <option value="">全部节点</option>
                {nodes.map((n) => (
                  <option key={n.id ?? n.node_id} value={n.id ?? n.node_id}>
                    {nodeName(n)}
                  </option>
                ))}
              </Select>
            </div>
          )}
          <div className="filter-group">
            <span className="filter-label">操作者类型</span>
            <Select value={actorType} onChange={(e) => { setActorType(e.target.value); setPage(1); }}>
              <option value="">全部</option>
              <option value="admin">管理员</option>
              <option value="ai_agent">AI Agent</option>
              <option value="node">节点</option>
              <option value="system">系统</option>
            </Select>
          </div>
          <div className="filter-group">
            <span className="filter-label">结果</span>
            <Select value={result} onChange={(e) => { setResult(e.target.value); setPage(1); }}>
              <option value="">全部</option>
              <option value="success">成功</option>
              <option value="denied">拒绝</option>
              <option value="failure">失败</option>
            </Select>
          </div>
          <div className="filter-group">
            <span className="filter-label">风险等级</span>
            <Select value={risk} onChange={(e) => { setRisk(e.target.value); setPage(1); }}>
              <option value="">全部</option>
              <option value="low">低</option>
              <option value="medium">中</option>
              <option value="high">高</option>
              <option value="critical">严重</option>
            </Select>
          </div>
          <div className="filter-group">
            <span className="filter-label">动作</span>
            <TextInput placeholder="如 task.create" value={action} onChange={(e) => { setAction(e.target.value); setPage(1); }} style={{ minWidth: 150 }} />
          </div>
          <div className="filter-group">
            <span className="filter-label">资源类型</span>
            <TextInput placeholder="如 node / lease" value={resourceType} onChange={(e) => { setResourceType(e.target.value); setPage(1); }} style={{ minWidth: 140 }} />
          </div>
          <div className="filter-group">
            <span className="filter-label">操作者 ID</span>
            <TextInput placeholder="ID…" value={actorId} onChange={(e) => { setActorId(e.target.value); setPage(1); }} style={{ minWidth: 130 }} />
          </div>
          <div className="filter-group">
            <span className="filter-label">关联 ID</span>
            <TextInput placeholder="request/task/lease/session ID" value={relatedId} onChange={(e) => { setRelatedId(e.target.value); setPage(1); }} style={{ minWidth: 150 }} />
          </div>
          <div className="filter-group" style={{ paddingBottom: 8 }}>
            <Checkbox label="仅重要记录" checked={importantOnly} onChange={(v) => { setImportantOnly(v); setPage(1); }} />
          </div>
          <div className="btn-row">
            <button className="btn btn-ghost btn-sm" onClick={state.reload}>
              刷新
            </button>
            <button className="btn btn-ghost btn-sm" onClick={resetFilters}>
              重置
            </button>
          </div>
        </div>

        {state.loading && state.data === null ? (
          <LoadingState label="加载审计日志中…" />
        ) : state.error ? (
          <ErrorState message={errorMessage(state.error)} onRetry={state.reload} />
        ) : events.length === 0 ? (
          <EmptyState title="没有匹配的审计事件" hint="请调整筛选条件；筛选结果为空并不代表服务异常。" />
        ) : (
          <>
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>时间</th>
                    <th>节点</th>
                    <th>操作者</th>
                    <th>动作</th>
                    <th>资源</th>
                    <th>结果</th>
                    <th>风险</th>
                    <th>重要</th>
                    <th>摘要</th>
                  </tr>
                </thead>
                <tbody>
                  {events.map((a) => (
                    <tr key={a.id} className="clickable" onClick={() => setDetail(a)}>
                      <td><TimeCell value={a.occurred_at} /></td>
                      <td>{a.node_name || a.node_id || '—'}</td>
                      <td>
                        {a.actor_type || '—'}
                        {a.actor_id && <div className="mono muted" style={{ fontSize: 11.5 }}>{shortId(a.actor_id, 12)}</div>}
                      </td>
                      <td className="mono-cell">{a.action || '—'}</td>
                      <td>
                        {a.resource_type || '—'}
                        {a.resource_id && <div className="mono muted" style={{ fontSize: 11.5 }}>{shortId(a.resource_id, 12)}</div>}
                      </td>
                      <td><StatusBadge status={a.result} /></td>
                      <td><RiskBadge risk={a.risk_level} /></td>
                      <td>{a.is_protected ? <Badge tone="indigo">重要</Badge> : '—'}</td>
                      <td className="muted" style={{ maxWidth: 280 }}>{a.summary || '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="card-actions" style={{ padding: '10px 16px', borderTop: '1px solid var(--border)' }}>
              <button className="btn btn-ghost btn-sm" disabled={page <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))}>
                上一页
              </button>
              <span className="muted">第 {page} 页</span>
              <button className="btn btn-ghost btn-sm" disabled={events.length < 50} onClick={() => setPage((p) => p + 1)}>
                下一页
              </button>
            </div>
          </>
        )}
      </Card>

      <Modal open={detail !== null} title="审计事件详情" onClose={() => setDetail(null)} width={640}>
        {detail && (
          <div>
            <div className="alert alert-info" style={{ marginBottom: 14 }}>
              <TimeCell value={detail.occurred_at} /> · {detail.summary || '无摘要'}
            </div>
            <dl className="kv kv-2col">
              <dt>事件 ID</dt>
              <dd className="mono">{detail.id}</dd>
              <dt>节点</dt>
              <dd>{detail.node_name || detail.node_id || '—'}</dd>
              <dt>操作者</dt>
              <dd>
                {detail.actor_type || '—'} {detail.actor_id && <span className="mono muted">({detail.actor_id})</span>}
              </dd>
              <dt>动作</dt>
              <dd className="mono">{detail.action || '—'}</dd>
              <dt>资源</dt>
              <dd>
                {detail.resource_type || '—'}
                {detail.resource_id && <span className="mono muted"> / {detail.resource_id}</span>}
              </dd>
              <dt>结果</dt>
              <dd><StatusBadge status={detail.result} /></dd>
              <dt>风险</dt>
              <dd><RiskBadge risk={detail.risk_level} /></dd>
              <dt>来源 IP</dt>
              <dd className="mono">{detail.source_ip || '—'}</dd>
              <dt>关联 ID</dt>
              <dd className="mono">
                {[detail.request_id && `request:${detail.request_id}`, detail.task_id && `task:${detail.task_id}`, detail.lease_id && `lease:${detail.lease_id}`, detail.session_id && `session:${detail.session_id}`]
                  .filter(Boolean)
                  .join('\n') || '—'}
              </dd>
              <dt>重要</dt>
              <dd>{detail.is_protected ? <Badge tone="indigo">是</Badge> : '否'}</dd>
            </dl>
            {detailFields.length > 0 && (
              <>
                <h4 style={{ margin: '14px 0 8px' }}>其他字段</h4>
                <dl className="kv kv-2col">
                  {detailFields.map(([k, v]) => (
                    <div key={k} style={{ display: 'contents' }}>
                      <dt>{k}</dt>
                      <dd className="mono" style={{ fontSize: 12.5, whiteSpace: 'pre-wrap' }}>
                        {typeof v === 'object' ? JSON.stringify(v, null, 2) : String(v)}
                      </dd>
                    </div>
                  ))}
                </dl>
              </>
            )}
            {Boolean(detail.details_json) && (
              <>
                <h4 style={{ margin: '14px 0 8px' }}>结构化详情</h4>
                <pre className="mono" style={{ background: '#0f172a', color: '#e2e8f0', padding: 12, borderRadius: 8, fontSize: 12, overflow: 'auto', maxHeight: 320 }}>
                  {JSON.stringify(detail.details_json, null, 2)}
                </pre>
              </>
            )}
          </div>
        )}
      </Modal>
    </div>
  );
}
