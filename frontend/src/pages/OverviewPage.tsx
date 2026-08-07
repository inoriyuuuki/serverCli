import { useMemo, type ReactNode } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useSession } from '../auth/AuthContext';
import { useApi, errorMessage } from '../lib/useApi';
import { unwrapList } from '../api/client';
import type { AiLease, AuditEvent, NodeInfo, SystemInfo, TaskInfo } from '../api/types';
import {
  Badge,
  Card,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
  ProfileBadge,
  RiskBadge,
  StatusBadge,
  TimeCell,
} from '../components/ui';
import { NodeInfoFields } from '../components/NodeInfo';
import { remainingText, shortId } from '../lib/format';

function StatCard({ label, value, tone, hint }: { label: string; value: ReactNode; tone?: 'green' | 'red' | 'amber' | 'blue'; hint?: string }) {
  return (
    <div className="card" style={{ marginBottom: 0, padding: '14px 16px' }}>
      <div style={{ fontSize: 12.5, color: 'var(--muted)' }}>{label}</div>
      <div style={{ fontSize: 26, fontWeight: 700, marginTop: 4, color: tone ? `var(--${tone})` : undefined }}>{value}</div>
      {hint && <div style={{ fontSize: 12, color: 'var(--muted)', marginTop: 4 }}>{hint}</div>}
    </div>
  );
}

export default function OverviewPage() {
  const session = useSession();
  const isPrimary = !session || session.role === 'primary';
  const isProd = session?.environment === 'production';
  const navigate = useNavigate();

  const nodesState = useApi<unknown>('/nodes', { enabled: true });
  const sysState = useApi<SystemInfo>('/system/info');
  const tasksState = useApi<unknown>('/tasks', { query: { limit: 50 } });
  const leasesState = useApi<unknown>('/ai/leases', { query: { limit: 100 } });
  const auditState = useApi<unknown>('/audit-events', { query: { limit: 30, risk_level: 'high' } });

  const nodes = useMemo(() => unwrapList<NodeInfo>(nodesState.data, ['nodes']), [nodesState.data]);
  const tasks = useMemo(() => unwrapList<TaskInfo>(tasksState.data, ['tasks']), [tasksState.data]);
  const leases = useMemo(() => unwrapList<AiLease>(leasesState.data, ['leases']), [leasesState.data]);
  const audits = useMemo(() => unwrapList<AuditEvent>(auditState.data, ['events']), [auditState.data]);

  const localNode = useMemo<NodeInfo | null>(() => {
    if (!session) return null;
    const byId = nodes.find((n) => n.id === session.nodeId || n.node_id === session.nodeId);
    if (byId) return byId;
    if (isPrimary) return nodes.find((n) => n.role === 'primary') ?? nodes[0] ?? null;
    return nodes[0] ?? null;
  }, [nodes, session, isPrimary]);

  const summary = useMemo(() => {
    const total = nodes.length;
    const online = nodes.filter((n) => n.status === 'online').length;
    const offline = nodes.filter((n) => n.status === 'offline' || n.status === 'disabled' || n.status === 'degraded').length;
    const pending = nodes.filter((n) => n.status === 'pending').length;
    const failed = tasks.filter((t) => ['failed', 'timed_out', 'result_unknown', 'node_unreachable'].includes(t.status ?? '')).slice(0, 5);
    const active = leases.filter((l) => l.status === 'active');
    const highRisk = audits.slice(0, 5);
    return { total, online, offline, pending, failed, active, highRisk };
  }, [nodes, tasks, leases, audits]);

  const version = (sysState.data as SystemInfo | null)?.version;

  return (
    <div>
      <PageHeader
        title={isPrimary ? '概览' : '本机概览'}
        subtitle={
          session ? (
            <>
              当前实例：{session.nodeName || '—'}（{session.role === 'primary' ? '主节点' : '子节点'}）
              {isProd && <span className="text-danger"> · 正式环境</span>}
            </>
          ) : undefined
        }
      />

      {(
        nodesState.stale || tasksState.stale || leasesState.stale || auditState.stale
      ) && (
        <div className="banner banner-warn" role="status">
          部分数据正在刷新，当前显示可能不是最新（刷新失败会单独提示，不会伪装成“没有数据”）。
        </div>
      )}

      {nodesState.error && (
        <ErrorState
          title="节点数据加载失败"
          message={errorMessage(nodesState.error)}
          onRetry={nodesState.reload}
        />
      )}
      {nodesState.data === null && !nodesState.loading && !nodesState.error && <LoadingState />}

      {!nodesState.error && (
        <div className="grid grid-2">
          <Card title="本机信息" actions={version ? <Badge tone="blue">v{version}</Badge> : undefined}>
            {nodesState.loading && !localNode ? (
              <LoadingState label="正在加载本机信息…" />
            ) : localNode ? (
              <NodeInfoFields node={localNode} />
            ) : sysState.data ? (
              <p className="empty-hint">未找到本机节点记录（可能尚未注册），系统标识：{sysState.data.environment} / {sysState.data.role}。</p>
            ) : (
              <EmptyState title="暂无本机信息" />
            )}
          </Card>

          {isPrimary && (
            <div className="grid" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(150px, 1fr))', gap: 12 }}>
              <StatCard label="节点总数" value={summary.total} />
              <StatCard label="在线" value={summary.online} tone="green" />
              <StatCard label="离线/禁用" value={summary.offline} tone={summary.offline > 0 ? 'red' : undefined} />
              <StatCard label="待审批" value={summary.pending} tone={summary.pending > 0 ? 'amber' : undefined} hint="含注册待审批节点" />
            </div>
          )}
        </div>
      )}

      {isPrimary && (
        <>
          <div className="grid grid-2" style={{ marginTop: 2 }}>
            <Card
              title="最近失败任务"
              actions={
                <Link className="btn btn-ghost btn-sm" to="/tasks">
                  全部任务
                </Link>
              }
            >
              {tasksState.loading && tasksState.data === null ? (
                <LoadingState label="加载任务中…" />
              ) : tasksState.error ? (
                <ErrorState message={errorMessage(tasksState.error)} onRetry={tasksState.reload} />
              ) : summary.failed.length === 0 ? (
                <EmptyState title="没有失败任务" hint="近期任务执行正常。" />
              ) : (
                <div className="table-wrap">
                  <table className="table">
                    <thead>
                      <tr>
                        <th>任务</th>
                        <th>节点</th>
                        <th>命令</th>
                        <th>状态</th>
                        <th>时间</th>
                      </tr>
                    </thead>
                    <tbody>
                      {summary.failed.map((t) => (
                        <tr key={t.id} className="clickable" onClick={() => navigate(`/tasks/${t.id}`)}>
                          <td className="mono-cell">{shortId(t.id)}</td>
                          <td>{t.node_name || t.node_id || '—'}</td>
                          <td>{t.command_id || '—'}</td>
                          <td>
                            <StatusBadge status={t.status} />
                          </td>
                          <td>
                            <TimeCell value={t.created_at ?? t.queued_at} />
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>

            <Card
              title="AI Lease"
              actions={
                <Link className="btn btn-ghost btn-sm" to="/ai-credentials">
                  管理凭证
                </Link>
              }
            >
              {leasesState.loading && leasesState.data === null ? (
                <LoadingState label="加载 Lease 中…" />
              ) : leasesState.error ? (
                <ErrorState message={errorMessage(leasesState.error)} onRetry={leasesState.reload} />
              ) : summary.active.length === 0 ? (
                <EmptyState title="没有活动 Lease" />
              ) : (
                <div className="table-wrap">
                  <table className="table">
                    <thead>
                      <tr>
                        <th>Lease</th>
                        <th>节点</th>
                        <th>权限</th>
                        <th>剩余</th>
                        <th>到期</th>
                      </tr>
                    </thead>
                    <tbody>
                      {summary.active.map((l) => {
                        const rem = remainingText(l.expires_at);
                        return (
                          <tr key={l.id}>
                            <td className="mono-cell" title={l.id}>
                              {shortId(l.id)}
                            </td>
                            <td>{l.node_name || l.node_id || '—'}</td>
                            <td>
                              <ProfileBadge profile={l.permission_profile} />
                            </td>
                            <td>
                              <span className={rem.kind === 'warn' ? 'text-danger' : rem.kind === 'ok' ? 'text-success' : undefined}>
                                {rem.text}
                              </span>
                            </td>
                            <td>
                              <TimeCell value={l.expires_at} />
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>
          </div>

          <Card title="高风险审计事件" actions={<Link className="btn btn-ghost btn-sm" to="/audit">查看审计</Link>}>
            {auditState.loading && auditState.data === null ? (
              <LoadingState label="加载审计中…" />
            ) : auditState.error ? (
              <ErrorState message={errorMessage(auditState.error)} onRetry={auditState.reload} />
            ) : summary.highRisk.length === 0 ? (
              <EmptyState title="近期无高风险审计事件" />
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <thead>
                    <tr>
                      <th>时间</th>
                      <th>节点</th>
                      <th>操作者</th>
                      <th>动作</th>
                      <th>资源</th>
                      <th>风险</th>
                      <th>结果</th>
                    </tr>
                  </thead>
                  <tbody>
                    {summary.highRisk.map((a) => (
                      <tr key={a.id}>
                        <td>
                          <TimeCell value={a.occurred_at} />
                        </td>
                        <td>{a.node_name || a.node_id || '—'}</td>
                        <td>
                          {a.actor_type} {a.actor_id && <span className="muted mono">({shortId(a.actor_id)})</span>}
                        </td>
                        <td>{a.action || '—'}</td>
                        <td>
                          {a.resource_type}
                          {a.resource_id && <span className="muted mono"> / {shortId(a.resource_id)}</span>}
                        </td>
                        <td>
                          <RiskBadge risk={a.risk_level} />
                        </td>
                        <td>
                          <StatusBadge status={a.result} />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </>
      )}

      {!isPrimary && (
        <Card title="本机说明">
          <p className="empty-hint">
            子节点仅展示本机数据。集群管理、节点审批、全局命令与全局策略请登录主节点界面操作。
          </p>
        </Card>
      )}
    </div>
  );
}
