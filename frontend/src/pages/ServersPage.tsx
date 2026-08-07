import { useMemo, useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { useApi, errorMessage } from '../lib/useApi';
import { api, unwrapList, ApiError } from '../api/client';
import type { Enrollment, NodeInfo } from '../api/types';
import {
  Badge,
  Card,
  EmptyState,
  ErrorState,
  LoadingState,
  Modal,
  PageHeader,
  Select,
  StatusBadge,
  TextInput,
  TimeCell,
} from '../components/ui';
import { nodeIp, nodeName } from '../components/NodeInfo';
import { cn } from '../lib/format';
import { useSession } from '../auth/AuthContext';

export default function ServersPage() {
  const session = useSession();
  const navigate = useNavigate();
  const isProd = session?.environment === 'production';

  const nodesState = useApi<unknown>('/nodes');
  const enrollState = useApi<unknown>('/node-enrollments');

  const nodes = useMemo(() => unwrapList<NodeInfo>(nodesState.data, ['nodes']), [nodesState.data]);
  const enrollments = useMemo(() => unwrapList<Enrollment>(enrollState.data, ['enrollments']), [enrollState.data]);

  const [keyword, setKeyword] = useState('');
  const [statusFilter, setStatusFilter] = useState('');
  const [roleFilter, setRoleFilter] = useState('');

  const [reviewTarget, setReviewTarget] = useState<Enrollment | null>(null);
  const [reviewAction, setReviewAction] = useState<'approve' | 'reject' | null>(null);
  const [reviewNote, setReviewNote] = useState('');
  const [reviewBusy, setReviewBusy] = useState(false);
  const [reviewError, setReviewError] = useState<string | null>(null);

  const filtered = useMemo(() => {
    const kw = keyword.trim().toLowerCase();
    return nodes.filter((n) => {
      if (statusFilter && n.status !== statusFilter) return false;
      if (roleFilter && n.role !== roleFilter) return false;
      if (!kw) return true;
      const hay = [n.id, n.node_id, n.alias, n.instance_name, n.name, n.hostname, nodeIp(n)].join(' ').toLowerCase();
      return hay.includes(kw);
    });
  }, [nodes, keyword, statusFilter, roleFilter]);

  const openReview = (enr: Enrollment, action: 'approve' | 'reject') => {
    setReviewTarget(enr);
    setReviewAction(action);
    setReviewNote('');
    setReviewError(null);
  };

  const submitReview = async (e: FormEvent) => {
    e.preventDefault();
    if (!reviewTarget || !reviewAction) return;
    setReviewBusy(true);
    setReviewError(null);
    try {
      const path = `/node-enrollments/${reviewTarget.id}/${reviewAction}`;
      await api.post(path, { review_note: reviewNote.trim() || undefined });
      setReviewTarget(null);
      setReviewAction(null);
      enrollState.reload();
      nodesState.reload();
    } catch (err) {
      setReviewError(err instanceof ApiError ? err.message : '操作失败，请重试');
    } finally {
      setReviewBusy(false);
    }
  };

  const pendingCount = enrollments.filter((e) => e.status === 'pending' || !e.status).length;

  return (
    <div>
      <PageHeader title="服务器" subtitle={`共 ${nodes.length} 个节点 · ${pendingCount} 个待审批申请`} />

      <Card title={`待审批申请${pendingCount ? `（${pendingCount}）` : ''}`}>
        {enrollState.loading && enrollState.data === null ? (
          <LoadingState label="加载申请中…" />
        ) : enrollState.error ? (
          <ErrorState message={errorMessage(enrollState.error)} onRetry={enrollState.reload} />
        ) : pendingCount === 0 ? (
          <EmptyState title="没有待审批的节点申请" />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>主机名</th>
                  <th>申请角色</th>
                  <th>来源地址</th>
                  <th>Agent 版本</th>
                  <th>申请时间</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {enrollments
                  .filter((e) => e.status === 'pending' || !e.status)
                  .map((e) => (
                    <tr key={e.id}>
                      <td>
                        {e.hostname || '—'}
                        {e.instance_request_id && <div className="muted mono" style={{ fontSize: 12 }}>{e.instance_request_id}</div>}
                      </td>
                      <td>
                        <Badge tone={e.requested_role === 'primary' ? 'indigo' : 'teal'}>
                          {e.requested_role === 'primary' ? '主节点' : '子节点'}
                        </Badge>
                      </td>
                      <td className="mono">{e.source_ip || (e.reported_addresses?.[0]?.address ?? '—')}</td>
                      <td className="mono">{e.agent_version || '—'}</td>
                      <td>
                        <TimeCell value={e.created_at} />
                      </td>
                      <td>
                        <div className="btn-row">
                          <button className="btn btn-sm btn-primary" onClick={() => openReview(e, 'approve')}>
                            批准
                          </button>
                          <button className="btn btn-sm btn-danger" onClick={() => openReview(e, 'reject')}>
                            拒绝
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card
        title="节点列表"
        actions={
          <div className="btn-row">
            <TextInput
              placeholder="搜索名称/ID/IP…"
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
              style={{ width: 200 }}
            />
            <Select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
              <option value="">全部状态</option>
              <option value="online">在线</option>
              <option value="degraded">降级</option>
              <option value="offline">离线</option>
              <option value="pending">待审批</option>
              <option value="disabled">已禁用</option>
              <option value="rejected">已拒绝</option>
            </Select>
            <Select value={roleFilter} onChange={(e) => setRoleFilter(e.target.value)}>
              <option value="">全部角色</option>
              <option value="primary">主节点</option>
              <option value="child">子节点</option>
            </Select>
            <button className="btn btn-ghost btn-sm" onClick={nodesState.reload}>
              刷新
            </button>
          </div>
        }
      >
        {nodesState.loading && nodesState.data === null ? (
          <LoadingState label="加载节点列表…" />
        ) : nodesState.error ? (
          <ErrorState message={errorMessage(nodesState.error)} onRetry={nodesState.reload} />
        ) : filtered.length === 0 ? (
          <EmptyState title={nodes.length === 0 ? '还没有注册节点' : '没有匹配的节点'} hint={nodes.length === 0 ? '等待节点 Agent 发起注册申请。' : '请调整筛选条件。'} />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>名称</th>
                  <th>角色</th>
                  <th>IP / 端口</th>
                  <th>状态</th>
                  <th>版本</th>
                  <th>最近心跳</th>
                  <th>资源摘要</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((n) => {
                  const hb = n.heartbeat ?? n;
                  const memPct = hb.memory_total_bytes ? Math.round((hb.memory_used_bytes! / hb.memory_total_bytes) * 100) : null;
                  const diskPct = hb.disk_total_bytes ? Math.round((hb.disk_used_bytes! / hb.disk_total_bytes) * 100) : null;
                  return (
                    <tr key={n.id ?? n.node_id} className="clickable" onClick={() => navigate(`/servers/${n.id ?? n.node_id}`)}>
                      <td>
                        {nodeName(n)}
                        <div className="muted mono" style={{ fontSize: 12 }}>{n.id ?? n.node_id}</div>
                      </td>
                      <td>
                        <Badge tone={n.role === 'primary' ? 'indigo' : 'teal'}>{n.role === 'primary' ? '主节点' : '子节点'}</Badge>
                      </td>
                      <td className="mono">
                        {nodeIp(n)}
                        {(n.frontend_port || n.backend_port) && (
                          <span className="muted">
                            {n.frontend_port ? ` :${n.frontend_port}` : ''}
                            {n.backend_port ? ` /${n.backend_port}` : ''}
                          </span>
                        )}
                      </td>
                      <td>
                        <StatusBadge status={n.status} />
                        {n.enabled === false && <Badge tone="gray">已禁用</Badge>}
                      </td>
                      <td className="mono">{n.agent_version || '—'}</td>
                      <td>
                        <TimeCell value={n.last_heartbeat_at ?? n.last_online_at} />
                      </td>
                      <td style={{ minWidth: 160 }}>
                        <div className="muted" style={{ fontSize: 12 }}>
                          CPU {hb.cpu_usage_percent?.toFixed?.(0) ?? '—'}%
                        </div>
                        <div className="muted" style={{ fontSize: 12 }}>
                          内存 {memPct !== null ? `${memPct}%` : '—'} · 磁盘 {diskPct !== null ? `${diskPct}%` : '—'}
                        </div>
                        <div className="muted mono" style={{ fontSize: 12 }}>
                          负载 {hb.load_1 ?? '—'}/{hb.load_5 ?? '—'}/{hb.load_15 ?? '—'}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Modal
        open={reviewTarget !== null}
        title={reviewAction === 'approve' ? '批准节点申请' : '拒绝节点申请'}
        onClose={() => setReviewTarget(null)}
        width={480}
      >
        {reviewTarget && (
          <form onSubmit={submitReview}>
            <div className="kv" style={{ marginBottom: 14 }}>
              <dt>主机名</dt>
              <dd>{reviewTarget.hostname || '—'}</dd>
              <dt>申请角色</dt>
              <dd>{reviewTarget.requested_role === 'primary' ? '主节点' : '子节点'}</dd>
              <dt>来源地址</dt>
              <dd className="mono">{reviewTarget.source_ip || '—'}</dd>
              <dt>Agent 版本</dt>
              <dd className="mono">{reviewTarget.agent_version || '—'}</dd>
            </div>
            <label className="field">
              <span className="field-label">审批备注</span>
              <textarea
                className="input"
                rows={3}
                value={reviewNote}
                onChange={(e) => setReviewNote(e.target.value)}
                placeholder="可选，将写入审计"
              />
            </label>
            {isProd && reviewAction === 'approve' && (
              <div className="alert alert-danger" role="alert">
                ⚠️ <strong>正式环境</strong>：批准后节点将获得正式环境身份。
              </div>
            )}
            {reviewError && <div className="alert alert-danger">{reviewError}</div>}
            <div className="modal-actions">
              <button type="button" className="btn btn-ghost" onClick={() => setReviewTarget(null)} disabled={reviewBusy}>
                取消
              </button>
              <button type="submit" className={cn('btn', reviewAction === 'approve' ? 'btn-primary' : 'btn-danger')} disabled={reviewBusy}>
                {reviewBusy ? '提交中…' : reviewAction === 'approve' ? '确认批准' : '确认拒绝'}
              </button>
            </div>
          </form>
        )}
      </Modal>
    </div>
  );
}
