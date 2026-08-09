import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { useSession } from '../auth/AuthContext';
import { useApi, errorMessage } from '../lib/useApi';
import { useRealtime } from '../lib/realtime';
import { api, unwrapList, unwrapObject, ApiError } from '../api/client';
import type { AiAutoApproval, AiLease, AiLeaseRequest, NodeInfo } from '../api/types';
import {
  Badge,
  Card,
  Checkbox,
  EmptyState,
  ErrorState,
  LoadingState,
  Modal,
  PageHeader,
  ProfileBadge,
  Select,
  StatusBadge,
  Tabs,
  TimeCell,
  useConfirm,
} from '../components/ui';
import { nodeName } from '../components/NodeInfo';
import { cn, remainingText, shortId } from '../lib/format';

type TabKey = 'active' | 'requests' | 'auto' | 'policy';

/** 秒级时钟：驱动“剩余时间”倒计时与到期状态实时更新。 */
function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

export default function AiCredentialsPage() {
  const session = useSession();
  const confirm = useConfirm();
  const isPrimary = !session || session.role === 'primary';
  const isProd = session?.environment === 'production';
  const [tab, setTab] = useState<TabKey>('active');

  // 凭证/申请/免审批变更由 WebSocket 实时推送，轮询仅作兜底。
  useRealtime(['leases_changed'], () => {
    leasesState.reload();
    requestsState.reload();
    autoApprovalsState.reload();
  });

  const leasesState = useApi<unknown>('/ai/leases', { query: { limit: 100 }, pollIntervalMs: 30000 });
  const requestsState = useApi<unknown>('/ai/lease-requests', { query: { limit: 100 }, pollIntervalMs: 30000 });
  const autoApprovalsState = useApi<unknown>(isPrimary ? '/ai/auto-approvals' : null, { query: { limit: 100 }, pollIntervalMs: 30000 });
  const settingsState = useApi<unknown>('/settings');
  const nodesState = useApi<unknown>(isPrimary ? '/nodes' : null, { pollIntervalMs: 60000 });

  const leases = useMemo(() => unwrapList<AiLease>(leasesState.data, ['leases']), [leasesState.data]);
  const requests = useMemo(() => unwrapList<AiLeaseRequest>(requestsState.data, ['requests', 'lease_requests']), [requestsState.data]);
  const autoApprovals = useMemo(() => unwrapList<AiAutoApproval>(autoApprovalsState.data, ['auto_approvals']), [autoApprovalsState.data]);
  const settings = useMemo(() => unwrapObject<Record<string, unknown>>(settingsState.data, ['settings']), [settingsState.data]);
  const nodes = useMemo(() => unwrapList<NodeInfo>(nodesState.data, ['nodes']), [nodesState.data]);

  const [revokeTarget, setRevokeTarget] = useState<AiLease | null>(null);
  const [revokeReason, setRevokeReason] = useState('');
  const [revokeTerminate, setRevokeTerminate] = useState(false);
  const [revokeBusy, setRevokeBusy] = useState(false);
  const [revokeError, setRevokeError] = useState<string | null>(null);

  const [opError, setOpError] = useState<string | null>(null);

  const [autoTarget, setAutoTarget] = useState<AiLeaseRequest | null>(null);
  const [autoDays, setAutoDays] = useState(1);
  const [autoBusy, setAutoBusy] = useState(false);
  const [autoError, setAutoError] = useState<string | null>(null);

  const [extendTarget, setExtendTarget] = useState<AiAutoApproval | null>(null);
  const [extendDays, setExtendDays] = useState(1);
  const [extendBusy, setExtendBusy] = useState(false);
  const [extendError, setExtendError] = useState<string | null>(null);

  const openAutoApproval = (r: AiLeaseRequest) => {
    setAutoTarget(r);
    setAutoDays(1);
    setAutoError(null);
  };

  const submitAutoApproval = async (e: FormEvent) => {
    e.preventDefault();
    if (!autoTarget) return;
    setAutoBusy(true);
    setAutoError(null);
    try {
      await api.post(`/ai/lease-requests/${autoTarget.id}/auto-approval`, { duration_days: autoDays });
      setAutoTarget(null);
      requestsState.reload();
      autoApprovalsState.reload();
      leasesState.reload(); // 该操作会同时签发新 Lease
    } catch (err) {
      setAutoError(err instanceof ApiError && err.code === 'TERMINAL_STATE' ? '该申请已被处理，无需重复操作（列表已刷新）' : err instanceof ApiError ? err.message : '操作失败，请重试');
      requestsState.reload();
      autoApprovalsState.reload();
    } finally {
      setAutoBusy(false);
    }
  };

  const openExtend = (r: AiAutoApproval) => {
    setExtendTarget(r);
    setExtendDays(1);
    setExtendError(null);
  };

  const submitExtend = async (e: FormEvent) => {
    e.preventDefault();
    if (!extendTarget) return;
    setExtendBusy(true);
    setExtendError(null);
    try {
      await api.post(`/ai/auto-approvals/${extendTarget.id}/extend`, { duration_days: extendDays });
      setExtendTarget(null);
      autoApprovalsState.reload();
    } catch (err) {
      setExtendError(err instanceof ApiError ? err.message : '操作失败，请重试');
      autoApprovalsState.reload();
    } finally {
      setExtendBusy(false);
    }
  };

  const openRevoke = (lease: AiLease) => {
    setRevokeTarget(lease);
    setRevokeReason('');
    setRevokeTerminate(false);
    setRevokeError(null);
  };

  const submitRevoke = async (e: FormEvent) => {
    e.preventDefault();
    if (!revokeTarget) return;
    setRevokeBusy(true);
    setRevokeError(null);
    try {
      await api.post(`/ai/leases/${revokeTarget.id}/revoke`, {
        reason: revokeReason.trim() || '管理员撤销',
        terminate_sessions: revokeTerminate,
      });
      setRevokeTarget(null);
      leasesState.reload();
    } catch (err) {
      setRevokeError(err instanceof ApiError ? err.message : '撤销失败，请重试');
    } finally {
      setRevokeBusy(false);
    }
  };

  // UI-defined actions not explicitly listed in the contract endpoint table;
  // backend exposes these as POST sub-resources.
  const runLeaseAction = async (lease: AiLease, action: 'disable-renewal' | 'protect', opts: { danger?: boolean; production?: boolean; requireReason?: boolean } = {}) => {
    setOpError(null);
    const result = await confirm({
      title: action === 'disable-renewal' ? '禁止续期' : '标记为重要',
      message:
        action === 'disable-renewal'
          ? `确认禁止 Lease ${shortId(lease.id)} 继续续期？该 Lease 到期后将自动失效。`
          : `确认将 Lease ${shortId(lease.id)} 标记为重要？重要记录不会被自动清理删除。`,
      confirmLabel: action === 'disable-renewal' ? '确认禁止' : '确认标记',
      danger: opts.danger,
      production: isProd,
      requireReason: opts.requireReason,
      reasonLabel: '原因',
    });
    if (!result.ok) return;
    try {
      await api.post(`/ai/leases/${lease.id}/${action}`, result.reason ? { reason: result.reason } : {});
      leasesState.reload();
    } catch (err) {
      setOpError(err instanceof ApiError ? err.message : '操作失败，请重试');
    }
  };

  const tabCounts = useMemo(
    () => ({
      active: leases.filter((l) => l.status === 'active').length,
      requests: requests.length,
      auto: autoApprovals.filter((r) => new Date(r.expires_at ?? '').getTime() > Date.now()).length,
    }),
    [leases, requests, autoApprovals],
  );

  return (
    <div>
      <PageHeader title={isPrimary ? 'AI 凭证' : '本机 AI 凭证'} subtitle="AI Agent 临时 SSH 凭证租约管理。" />
      {opError && <div className="alert alert-danger">{opError}</div>}

      <Tabs
        tabs={[
          { key: 'active', label: '活动凭证', count: tabCounts.active },
          { key: 'requests', label: '申请记录', count: tabCounts.requests },
          ...(isPrimary ? [{ key: 'auto' as TabKey, label: '自动免审批', count: tabCounts.auto }] : []),
          { key: 'policy', label: '控制策略' },
        ]}
        active={tab}
        onChange={(k) => setTab(k as TabKey)}
      />

      {tab === 'active' && (
        <ActiveLeases
          leases={leases}
          state={leasesState}
          onRevoke={openRevoke}
          onDisableRenewal={(l) => runLeaseAction(l, 'disable-renewal')}
          onProtect={(l) => runLeaseAction(l, 'protect')}
        />
      )}

      {tab === 'requests' && (
        <RequestsTab requests={requests} state={requestsState} isProd={isProd} isPrimary={isPrimary} onAutoApproval={openAutoApproval} />
      )}

      {tab === 'auto' && isPrimary && (
        <AutoApprovalsTab rules={autoApprovals} state={autoApprovalsState} nodes={nodes} isProd={isProd} onExtend={openExtend} />
      )}

      {tab === 'policy' && (
        <PolicyTab
          isPrimary={isPrimary}
          settings={settings}
          settingsState={settingsState}
          nodes={nodes}
          onChanged={() => {
            settingsState.reload();
            leasesState.reload();
          }}
        />
      )}

      <Modal open={revokeTarget !== null} title={`撤销 Lease：${revokeTarget ? shortId(revokeTarget.id) : ''}`} onClose={() => setRevokeTarget(null)} width={480}>
        {revokeTarget && (
          <form onSubmit={submitRevoke}>
            <div className="kv" style={{ marginBottom: 14 }}>
              <dt>目标节点</dt>
              <dd>{revokeTarget.node_name || revokeTarget.node_id || '—'}</dd>
              <dt>AI</dt>
              <dd>{revokeTarget.ai_agent_name || revokeTarget.ai_agent_id || '—'}</dd>
              <dt>权限</dt>
              <dd>
                <ProfileBadge profile={revokeTarget.permission_profile} />
              </dd>
              <dt>到期</dt>
              <dd><TimeCell value={revokeTarget.expires_at} /></dd>
            </div>
            <label className="field">
              <span className="field-label">撤销原因 <em className="req">*</em></span>
              <textarea
                className="input"
                rows={3}
                value={revokeReason}
                onChange={(e) => setRevokeReason(e.target.value)}
                placeholder="请填写撤销原因（将写入审计）"
                required
              />
            </label>
            <Checkbox label="终止该 Lease 的活动 SSH 会话" checked={revokeTerminate} onChange={setRevokeTerminate} />
            {isProd && (
              <div className="alert alert-danger" role="alert">
                ⚠️ <strong>正式环境</strong>：撤销将立即生效并写入正式环境审计。
              </div>
            )}
            {revokeError && <div className="alert alert-danger">{revokeError}</div>}
            <div className="modal-actions">
              <button type="button" className="btn btn-ghost" onClick={() => setRevokeTarget(null)} disabled={revokeBusy}>
                取消
              </button>
              <button type="submit" className="btn btn-danger" disabled={revokeBusy || !revokeReason.trim()}>
                {revokeBusy ? '提交中…' : '确认撤销'}
              </button>
            </div>
          </form>
        )}
      </Modal>

      <Modal open={autoTarget !== null} title="批准并设置自动免审批" onClose={() => setAutoTarget(null)} width={520}>
        {autoTarget && (
          <form onSubmit={submitAutoApproval}>
            <div className="kv" style={{ marginBottom: 14 }}>
              <dt>申请</dt>
              <dd className="mono">{autoTarget.id}</dd>
              <dt>AI</dt>
              <dd>{autoTarget.ai_agent_name || autoTarget.ai_agent_id || '—'}</dd>
              <dt>节点</dt>
              <dd>{autoTarget.node_name || autoTarget.node_id || '—'}</dd>
              <dt>权限</dt>
              <dd><ProfileBadge profile={autoTarget.requested_profile ?? autoTarget.permission_profile} /></dd>
            </div>
            <label className="field">
              <span className="field-label">免审批天数 <em className="req">*</em></span>
              <input
                className="input"
                type="number"
                min={1}
                max={15}
                value={autoDays}
                onChange={(e) => setAutoDays(Number(e.target.value))}
                required
              />
              <span className="field-hint">
                1–15 天。该设备（{autoTarget.ai_agent_id}）访问此节点的所有权限申请（包括 admin）将自动批准，最长不超过操作时刻后的 15 天。
              </span>
            </label>
            {isProd && (
              <div className="alert alert-danger" role="alert">
                ⚠️ <strong>正式环境</strong>：规则立即生效并写入正式环境审计。
              </div>
            )}
            {autoError && <div className="alert alert-danger">{autoError}</div>}
            <div className="modal-actions">
              <button type="button" className="btn btn-ghost" onClick={() => setAutoTarget(null)} disabled={autoBusy}>
                取消
              </button>
              <button type="submit" className="btn btn-primary" disabled={autoBusy || autoDays < 1 || autoDays > 15}>
                {autoBusy ? '提交中…' : '确认批准并免审批'}
              </button>
            </div>
          </form>
        )}
      </Modal>

      <Modal open={extendTarget !== null} title="延长自动免审批" onClose={() => setExtendTarget(null)} width={520}>
        {extendTarget && (
          <form onSubmit={submitExtend}>
            <div className="kv" style={{ marginBottom: 14 }}>
              <dt>设备</dt>
              <dd>{extendTarget.ai_agent_name || extendTarget.ai_agent_id || '—'}</dd>
              <dt>节点</dt>
              <dd>{nodeName(nodes.find((n) => (n.id ?? n.node_id) === extendTarget.node_id) ?? null)}</dd>
              <dt>当前到期</dt>
              <dd><TimeCell value={extendTarget.expires_at} /></dd>
            </div>
            <label className="field">
              <span className="field-label">延长天数 <em className="req">*</em></span>
              <input
                className="input"
                type="number"
                min={1}
                max={15}
                value={extendDays}
                onChange={(e) => setExtendDays(Number(e.target.value))}
                required
              />
              <span className="field-hint">
                从当前到期时间累加，最终不超过操作时刻后的 15 天；已过期的规则将从当前时间重新起算。
              </span>
            </label>
            {extendError && <div className="alert alert-danger">{extendError}</div>}
            <div className="modal-actions">
              <button type="button" className="btn btn-ghost" onClick={() => setExtendTarget(null)} disabled={extendBusy}>
                取消
              </button>
              <button type="submit" className="btn btn-primary" disabled={extendBusy || extendDays < 1 || extendDays > 15}>
                {extendBusy ? '提交中…' : '确认延长'}
              </button>
            </div>
          </form>
        )}
      </Modal>
    </div>
  );
}

/* ------------------------------ 活动凭证 ------------------------------ */

function ActiveLeases({
  leases,
  state,
  onRevoke,
  onDisableRenewal,
  onProtect,
}: {
  leases: AiLease[];
  state: { loading: boolean; error: ApiError | null; reload: () => void; data: unknown };
  onRevoke: (l: AiLease) => void;
  onDisableRenewal: (l: AiLease) => void;
  onProtect: (l: AiLease) => void;
}) {
  const now = useNow();
  if (state.loading && state.data === null) return <Card><LoadingState label="加载凭证中…" /></Card>;
  if (state.error) return <Card><ErrorState message={errorMessage(state.error)} onRetry={state.reload} /></Card>;
  if (leases.length === 0) return <Card><EmptyState title="暂无 Lease 记录" /></Card>;
  return (
    <Card title="活动凭证" actions={<button className="btn btn-ghost btn-sm" onClick={state.reload}>刷新</button>}>
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Lease</th>
              <th>状态</th>
              <th>目标节点</th>
              <th>AI</th>
              <th>权限</th>
              <th>签发</th>
              <th>到期</th>
              <th>剩余</th>
              <th>续期</th>
              <th>会话</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {leases.map((l) => {
              const rem = remainingText(l.expires_at, now);
              return (
                <tr key={l.id}>
                  <td className="mono-cell" title={l.id}>
                    {shortId(l.id)}
                    {l.is_protected && <Badge tone="indigo">重要</Badge>}
                    {l.renewal_disabled && <Badge tone="amber">禁续期</Badge>}
                  </td>
                  <td><StatusBadge status={l.status} /></td>
                  <td>{l.node_name || l.node_id || '—'}</td>
                  <td>{l.ai_agent_name || l.ai_agent_id || '—'}</td>
                  <td><ProfileBadge profile={l.permission_profile} /></td>
                  <td><TimeCell value={l.issued_at} /></td>
                  <td><TimeCell value={l.expires_at} /></td>
                  <td>
                    <span className={rem.kind === 'warn' ? 'text-danger' : rem.kind === 'ok' ? 'text-success' : undefined}>
                      {rem.text}
                    </span>
                  </td>
                  <td className="num">{l.renew_count ?? 0}</td>
                  <td className="num">{l.active_session_count ?? 0}</td>
                  <td>
                    <div className="btn-row">
                      {l.status === 'active' && !l.renewal_disabled && (
                        <button className="btn btn-ghost btn-sm" onClick={() => onDisableRenewal(l)}>
                          禁止续期
                        </button>
                      )}
                      {l.status === 'active' && (
                        <button className="btn btn-danger btn-sm" onClick={() => onRevoke(l)}>
                          撤销
                        </button>
                      )}
                      {!l.is_protected && (
                        <button className="btn btn-ghost btn-sm" onClick={() => onProtect(l)}>
                          标记重要
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

/* ------------------------------ 申请记录 ------------------------------ */

function RequestsTab({
  requests,
  state,
  isProd,
  isPrimary,
  onAutoApproval,
}: {
  requests: AiLeaseRequest[];
  state: { loading: boolean; error: ApiError | null; reload: () => void; data: unknown };
  isProd?: boolean;
  isPrimary?: boolean;
  onAutoApproval: (r: AiLeaseRequest) => void;
}) {
  const confirm = useConfirm();
  const [busyId, setBusyId] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  /** 审批/拒绝操作的错误提示：终态申请（已被他人处理）给出友好文案。 */
  const actionErrorMessage = (err: unknown, fallback: string): string => {
    if (err instanceof ApiError) {
      if (err.code === 'TERMINAL_STATE') return '该申请已被处理，无需重复操作（列表已刷新）';
      return err.message || fallback;
    }
    return fallback;
  };

  const approve = async (r: AiLeaseRequest) => {
    setActionError(null);
    const result = await confirm({
      title: '批准 Lease 申请',
      message: (
        <div className="kv">
          <dt>申请</dt>
          <dd className="mono">{r.id}</dd>
          <dt>AI</dt>
          <dd>{r.ai_agent_name || r.ai_agent_id || '—'}</dd>
          <dt>节点</dt>
          <dd>{r.node_name || r.node_id || '—'}</dd>
          <dt>权限</dt>
          <dd><ProfileBadge profile={r.requested_profile ?? r.permission_profile} /></dd>
          <dt>时长</dt>
          <dd className="mono">{r.requested_duration_seconds ? `${Math.round(r.requested_duration_seconds / 60)} 分钟` : '—'}</dd>
        </div>
      ),
      confirmLabel: '确认批准',
      production: isProd,
    });
    if (!result.ok) return;
    setBusyId(r.id);
    try {
      await api.post(`/ai/lease-requests/${r.id}/approve`);
      state.reload();
    } catch (err) {
      setActionError(actionErrorMessage(err, '审批失败，请重试'));
      state.reload(); // 刷新到最新状态，避免停留在已过期的 pending 视图
    } finally {
      setBusyId(null);
    }
  };

  const reject = async (r: AiLeaseRequest) => {
    setActionError(null);
    const result = await confirm({
      title: '拒绝 Lease 申请',
      message: (
        <div className="kv">
          <dt>申请</dt>
          <dd className="mono">{r.id}</dd>
          <dt>AI</dt>
          <dd>{r.ai_agent_name || r.ai_agent_id || '—'}</dd>
          <dt>节点</dt>
          <dd>{r.node_name || r.node_id || '—'}</dd>
        </div>
      ),
      confirmLabel: '确认拒绝',
      danger: true,
      production: isProd,
      requireReason: true,
      reasonLabel: '拒绝原因',
      reasonPlaceholder: '请填写拒绝原因（将写入审计）',
    });
    if (!result.ok) return;
    setBusyId(r.id);
    try {
      await api.post(`/ai/lease-requests/${r.id}/reject`, { reason: result.reason });
      state.reload();
    } catch (err) {
      setActionError(actionErrorMessage(err, '拒绝失败，请重试'));
      state.reload();
    } finally {
      setBusyId(null);
    }
  };

  if (state.loading && state.data === null) return <Card><LoadingState label="加载申请记录中…" /></Card>;
  if (state.error) return <Card><ErrorState message={errorMessage(state.error)} onRetry={state.reload} /></Card>;
  if (requests.length === 0) return <Card><EmptyState title="暂无申请记录" /></Card>;
  const pending = requests.filter((r) => r.status === 'pending');
  return (
    <Card title="申请记录" actions={<button className="btn btn-ghost btn-sm" onClick={state.reload}>刷新</button>}>
      {actionError && <div className="alert alert-danger">{actionError}</div>}
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>申请</th>
              <th>AI</th>
              <th>节点</th>
              <th>权限</th>
              <th>时长</th>
              <th>公钥指纹</th>
              <th>状态</th>
              <th>审批结果 / 原因</th>
              <th>申请时间</th>
              {pending.length > 0 && <th>操作</th>}
            </tr>
          </thead>
          <tbody>
            {requests.map((r) => (
              <tr key={r.id}>
                <td className="mono-cell" title={r.id}>
                  {shortId(r.id)}
                  {r.is_protected && <Badge tone="indigo">重要</Badge>}
                </td>
                <td>{r.ai_agent_name || r.ai_agent_id || '—'}</td>
                <td>{r.node_name || r.node_id || '—'}</td>
                <td><ProfileBadge profile={r.requested_profile ?? r.permission_profile} /></td>
                <td className="mono">{r.requested_duration_seconds ? `${Math.round(r.requested_duration_seconds / 60)} 分钟` : '—'}</td>
                <td className="mono" title={r.public_key_fingerprint}>{r.public_key_fingerprint ? shortId(r.public_key_fingerprint, 16) : '—'}</td>
                <td><StatusBadge status={r.status} /></td>
                <td className="muted">{r.decision_reason || '—'}</td>
                <td><TimeCell value={r.created_at} /></td>
                {pending.length > 0 && (
                  <td>
                    <div className="btn-row">
                      {r.status === 'pending' ? (
                        <>
                          {isPrimary && (
                            <button className="btn btn-sm btn-ghost" disabled={busyId === r.id} onClick={() => onAutoApproval(r)}>
                              批准并免审批
                            </button>
                          )}
                          <button className="btn btn-sm btn-primary" disabled={busyId === r.id} onClick={() => approve(r)}>
                            {busyId === r.id ? '处理中…' : '批准'}
                          </button>
                          <button className="btn btn-sm btn-danger" disabled={busyId === r.id} onClick={() => reject(r)}>
                            拒绝
                          </button>
                        </>
                      ) : (
                        <span className="muted">—</span>
                      )}
                    </div>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

/* ------------------------------ 自动免审批 ------------------------------ */

function AutoApprovalsTab({
  rules,
  state,
  nodes,
  isProd,
  onExtend,
}: {
  rules: AiAutoApproval[];
  state: { loading: boolean; error: ApiError | null; reload: () => void; data: unknown };
  nodes: NodeInfo[];
  isProd?: boolean;
  onExtend: (r: AiAutoApproval) => void;
}) {
  const now = useNow();
  if (state.loading && state.data === null) return <Card><LoadingState label="加载自动免审批…" /></Card>;
  if (state.error) return <Card><ErrorState message={errorMessage(state.error)} onRetry={state.reload} /></Card>;
  if (rules.length === 0) {
    return (
      <Card title="自动免审批" actions={<button className="btn btn-ghost btn-sm" onClick={state.reload}>刷新</button>}>
        <EmptyState
          title="暂无自动免审批规则"
          hint="在「申请记录」中对待审批申请执行“批准并免审批”，即可为设备+节点创建免审批规则。"
        />
      </Card>
    );
  }
  return (
    <Card title="自动免审批" actions={<button className="btn btn-ghost btn-sm" onClick={state.reload}>刷新</button>}>
      {isProd && (
        <div className="alert alert-warn" role="alert">
          命中规则的申请（包括 admin 权限）将自动批准并写入正式环境审计。
        </div>
      )}
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>设备</th>
              <th>节点</th>
              <th>来源申请</th>
              <th>创建人</th>
              <th>创建时间</th>
              <th>到期时间</th>
              <th>剩余</th>
              <th>状态</th>
              <th style={{ width: 80 }}>操作</th>
            </tr>
          </thead>
          <tbody>
            {rules.map((r) => {
              const rem = remainingText(r.expires_at, now);
              const active = rem.kind !== 'over';
              return (
                <tr key={r.id}>
                  <td>
                    {r.ai_agent_name || '—'}
                    <div className="muted mono" style={{ fontSize: 12 }}>{r.ai_agent_id || ''}</div>
                  </td>
                  <td>{nodeName(nodes.find((n) => (n.id ?? n.node_id) === r.node_id) ?? null) || r.node_id || '—'}</td>
                  <td className="mono-cell" title={r.source_request_id || ''}>
                    {r.source_request_id ? shortId(r.source_request_id) : '—'}
                  </td>
                  <td>{r.created_by || '—'}</td>
                  <td><TimeCell value={r.created_at} /></td>
                  <td><TimeCell value={r.expires_at} /></td>
                  <td>
                    <span className={rem.kind === 'warn' ? 'text-danger' : rem.kind === 'ok' ? 'text-success' : undefined}>
                      {rem.text}
                    </span>
                  </td>
                  <td>{active ? <Badge tone="teal">有效</Badge> : <Badge tone="gray">已过期</Badge>}</td>
                  <td>
                    <button className="btn btn-ghost btn-sm" onClick={() => onExtend(r)}>
                      延长
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

/* ------------------------------ 控制策略 ------------------------------ */

function PolicyTab({
  isPrimary,
  settings,
  settingsState,
  nodes,
  onChanged,
}: {
  isPrimary: boolean;
  settings: Record<string, unknown> | null;
  settingsState: { loading: boolean; error: ApiError | null; reload: () => void; data: unknown };
  nodes: NodeInfo[];
  onChanged: () => void;
}) {
  const session = useSession();
  const confirm = useConfirm();
  const isProd = session?.environment === 'production';
  const [scopeNode, setScopeNode] = useState<string>('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const boolVal = (keys: string[], fallback?: boolean): boolean | undefined => {
    for (const k of keys) {
      const v = settings?.[k];
      if (typeof v === 'boolean') return v;
    }
    return fallback;
  };
  const numVal = (keys: string[]): number | undefined => {
    for (const k of keys) {
      const v = settings?.[k];
      if (typeof v === 'number') return v;
    }
    return undefined;
  };
  const strVal = (keys: string[]): string | undefined => {
    for (const k of keys) {
      const v = settings?.[k];
      if (typeof v === 'string') return v;
    }
    return undefined;
  };

  const newRequests = boolVal(['ai_new_requests_enabled', 'new_requests_enabled', 'ai_new_requests']);
  const renewals = boolVal(['ai_renewals_enabled', 'renewals_enabled', 'ai_renewals']);
  const autoApproval = strVal(['ai_auto_approval_policy', 'auto_approval_policy']);
  const defaultMinutes = numVal(['ai_lease_default_minutes']);
  const maxHours = numVal(['ai_lease_max_hours']);

  const toggleSwitch = async (
    enabled: boolean,
    key: 'new_requests_enabled' | 'renewals_enabled',
    scope?: { nodeId: string } | null,
  ) => {
    setBusy(true);
    setError(null);
    try {
      const body: Record<string, unknown> = {
        scope: scope ? 'node_id' : isPrimary ? (scopeNode ? 'node_id' : 'global') : 'node_id',
        [key]: enabled,
      };
      if (scope) body.node_id = scope.nodeId;
      else if (body.scope === 'node_id') body.node_id = scopeNode;
      await api.patch('/settings/ai-access', body);
      onChanged();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '更新失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  const emergencyRevoke = async () => {
    const result = await confirm({
      title: '紧急撤销全部 Lease',
      message: isPrimary && scopeNode ? `将立即撤销节点“${nodeName(nodes.find((n) => n.id === scopeNode) ?? null)}”范围内的全部活动 Lease。` : '将立即撤销全局范围内全部活动 Lease。',
      confirmLabel: '确认紧急撤销',
      danger: true,
      production: isProd,
      requireReason: true,
      reasonLabel: '撤销原因',
      reasonPlaceholder: '必须填写原因（将写入审计）',
    });
    if (!result.ok) return;
    setBusy(true);
    setError(null);
    try {
      // UI-defined endpoint for bulk emergency revoke.
      await api.post('/ai/leases/revoke-all', {
        reason: result.reason,
        terminate_sessions: true,
        scope: isPrimary && scopeNode ? 'node_id' : 'global',
        ...(isPrimary && scopeNode ? { node_id: scopeNode } : {}),
      });
      onChanged();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '操作失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  if (settingsState.loading && settingsState.data === null) return <Card><LoadingState label="加载策略中…" /></Card>;
  if (settingsState.error) return <Card><ErrorState message={errorMessage(settingsState.error)} onRetry={settingsState.reload} /></Card>;

  if (!isPrimary) {
    return (
      <div className="grid grid-2">
        <Card title="本机策略">
          {error && <div className="alert alert-danger">{error}</div>}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
            <p className="empty-hint">子节点仅能控制本机的 AI 申请/续期开关；全局策略由主节点管理。</p>
            <SwitchRow
              label="本机允许新申请"
              checked={newRequests}
              disabled={busy}
              onChange={(v) => toggleSwitch(v, 'new_requests_enabled', session?.nodeId ? { nodeId: session.nodeId } : null)}
            />
            <SwitchRow
              label="本机允许续期"
              checked={renewals}
              disabled={busy}
              onChange={(v) => toggleSwitch(v, 'renewals_enabled', session?.nodeId ? { nodeId: session.nodeId } : null)}
            />
            <div className="kv">
              <dt>自动审批策略</dt>
              <dd>{autoApproval ?? '—'}</dd>
              <dt>默认时长</dt>
              <dd className="mono">{defaultMinutes !== undefined ? `${defaultMinutes} 分钟` : '—'}</dd>
              <dt>绝对上限</dt>
              <dd className="mono">{maxHours !== undefined ? `${maxHours} 小时` : '—'}</dd>
            </div>
          </div>
        </Card>
        <Card title="紧急撤销本机 Lease">
          <p className="empty-hint" style={{ marginBottom: 12 }}>
            立即撤销本机范围内全部活动 Lease，并终止相关 SSH 会话。
          </p>
          <button className="btn btn-danger" onClick={emergencyRevoke} disabled={busy}>
            {busy ? '处理中…' : '紧急撤销本机全部'}
          </button>
        </Card>
      </div>
    );
  }

  return (
    <div className="grid grid-2">
      <Card title="全局策略">
        {error && <div className="alert alert-danger">{error}</div>}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <SwitchRow
            label="允许新的 Lease 申请"
            checked={newRequests}
            disabled={busy}
            onChange={(v) => toggleSwitch(v, 'new_requests_enabled')}
          />
          <SwitchRow
            label="允许续期"
            checked={renewals}
            disabled={busy}
            onChange={(v) => toggleSwitch(v, 'renewals_enabled')}
          />
          <div className="kv">
            <dt>自动审批策略</dt>
            <dd>{autoApproval ?? '—'}</dd>
            <dt>默认时长</dt>
            <dd className="mono">{defaultMinutes !== undefined ? `${defaultMinutes} 分钟` : '—'}</dd>
            <dt>绝对上限</dt>
            <dd className="mono">{maxHours !== undefined ? `${maxHours} 小时` : '—'}</dd>
          </div>
          <p className="empty-hint">数值与审批策略在「系统设置」中维护；此处仅提供申请/续期开关的快捷控制。</p>
        </div>
      </Card>

      <Card title="节点范围控制">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <label className="field">
            <span className="field-label">目标节点</span>
            <Select value={scopeNode} onChange={(e) => setScopeNode(e.target.value)}>
              <option value="">全局（所有节点）</option>
              {nodes.map((n) => (
                <option key={n.id ?? n.node_id} value={n.id ?? n.node_id}>
                  {nodeName(n)}
                </option>
              ))}
            </Select>
          </label>
          <SwitchRow
            label="该范围允许新申请"
            checked={newRequests}
            disabled={busy}
            onChange={(v) => toggleSwitch(v, 'new_requests_enabled')}
          />
          <SwitchRow
            label="该范围允许续期"
            checked={renewals}
            disabled={busy}
            onChange={(v) => toggleSwitch(v, 'renewals_enabled')}
          />
          <div className="alert alert-warn" role="alert">
            切换节点开关将按所选节点覆盖策略（PATCH /settings/ai-access，scope=node_id）。
          </div>
        </div>
      </Card>

      <Card title="紧急撤销">
        <p className="empty-hint" style={{ marginBottom: 12 }}>
          立即撤销{scopeNode ? '所选节点范围内' : '全局范围内'}全部活动 Lease，并终止相关 SSH 会话。
        </p>
        <button className="btn btn-danger" onClick={emergencyRevoke} disabled={busy}>
          {busy ? '处理中…' : '紧急撤销全部'}
        </button>
      </Card>
    </div>
  );
}

function SwitchRow({
  label,
  checked,
  disabled,
  onChange,
}: {
  label: string;
  checked: boolean | undefined;
  disabled: boolean;
  onChange: (v: boolean) => void;
}) {
  const value = checked ?? false;
  return (
    <div className="switch-row">
      <div>
        <div style={{ fontWeight: 600, fontSize: 13.5 }}>{label}</div>
        <div className="muted" style={{ fontSize: 12.5 }}>{value ? '已开启' : '已关闭'}</div>
      </div>
      <button
        type="button"
        role="switch"
        aria-checked={value}
        aria-label={label}
        className={cn('switch', value && 'switch-on')}
        disabled={disabled}
        onClick={() => onChange(!value)}
      >
        <span className="switch-knob" />
      </button>
    </div>
  );
}
