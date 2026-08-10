import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { useSession } from '../auth/AuthContext';
import { useApi, errorMessage } from '../lib/useApi';
import { useRealtime } from '../lib/realtime';
import { api, unwrapList, unwrapObject, ApiError } from '../api/client';
import type { AiLease, AiLeaseRequest, NodeInfo } from '../api/types';
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

type TabKey = 'active' | 'requests' | 'policy';

/** 秒级时钟：驱动“剩余时间”倒计时与到期状态实时更新。 */
function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

/** Access Token 来源列：名称 + 前缀（仅展示，绝不明文）。 */
function TokenSourceCell({ tokenName, tokenPrefix }: { tokenName?: string; tokenPrefix?: string }) {
  if (!tokenName && !tokenPrefix) return <span className="muted">—</span>;
  return (
    <span title="Access Token 来源">
      {tokenName || '—'}
      {tokenPrefix && <span className="muted mono" style={{ fontSize: 12 }}> ({tokenPrefix})</span>}
    </span>
  );
}

export default function AiCredentialsPage() {
  const session = useSession();
  const confirm = useConfirm();
  const isPrimary = !session || session.role === 'primary';
  const isProd = session?.environment === 'production';
  const [tab, setTab] = useState<TabKey>('active');

  // 凭证/申请变更由 WebSocket 实时推送，轮询仅作兜底。
  useRealtime(['leases_changed'], () => {
    leasesState.reload();
    requestsState.reload();
  });

  const leasesState = useApi<unknown>('/ai/leases', { query: { limit: 100 }, pollIntervalMs: 30000 });
  const requestsState = useApi<unknown>('/ai/lease-requests', { query: { limit: 100 }, pollIntervalMs: 30000 });
  const settingsState = useApi<unknown>('/settings');
  const nodesState = useApi<unknown>(isPrimary ? '/nodes' : null, { pollIntervalMs: 60000 });

  const leases = useMemo(() => unwrapList<AiLease>(leasesState.data, ['leases']), [leasesState.data]);
  const requests = useMemo(() => unwrapList<AiLeaseRequest>(requestsState.data, ['requests', 'lease_requests']), [requestsState.data]);
  const settings = useMemo(() => unwrapObject<Record<string, unknown>>(settingsState.data, ['settings']), [settingsState.data]);
  const nodes = useMemo(() => unwrapList<NodeInfo>(nodesState.data, ['nodes']), [nodesState.data]);

  const [revokeTarget, setRevokeTarget] = useState<AiLease | null>(null);
  const [revokeReason, setRevokeReason] = useState('');
  const [revokeTerminate, setRevokeTerminate] = useState(false);
  const [revokeBusy, setRevokeBusy] = useState(false);
  const [revokeError, setRevokeError] = useState<string | null>(null);

  const [opError, setOpError] = useState<string | null>(null);

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
      requestsState.reload();
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
    }),
    [leases, requests],
  );

  return (
    <div>
      <PageHeader title={isPrimary ? 'AI 凭证' : '本机 AI 凭证'} subtitle="AI Agent 临时 SSH 凭证租约管理（Access Token 自动审批）。" />
      {opError && <div className="alert alert-danger">{opError}</div>}

      <Tabs
        tabs={[
          { key: 'active', label: '活动凭证', count: tabCounts.active },
          { key: 'requests', label: '申请记录', count: tabCounts.requests },
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
        <RequestsTab requests={requests} state={requestsState} />
      )}

      {tab === 'policy' && (
        <PolicyTab
          isPrimary={isPrimary}
          settings={settings}
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
              <dt>目标服务器</dt>
              <dd>{revokeTarget.node_name || revokeTarget.node_id || '—'}</dd>
              <dt>AI</dt>
              <dd>{revokeTarget.ai_agent_name || revokeTarget.ai_agent_id || '—'}</dd>
              <dt>Token</dt>
              <dd><TokenSourceCell tokenName={revokeTarget.access_token_name} tokenPrefix={revokeTarget.access_token_prefix} /></dd>
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
              <th>目标服务器</th>
              <th>AI</th>
              <th>Token 来源</th>
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
                  <td><TokenSourceCell tokenName={l.access_token_name} tokenPrefix={l.access_token_prefix} /></td>
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

/* ------------------------------ 申请记录（只读历史） ------------------------------ */

function RequestsTab({
  requests,
  state,
}: {
  requests: AiLeaseRequest[];
  state: { loading: boolean; error: ApiError | null; reload: () => void; data: unknown };
}) {
  if (state.loading && state.data === null) return <Card><LoadingState label="加载申请记录中…" /></Card>;
  if (state.error) return <Card><ErrorState message={errorMessage(state.error)} onRetry={state.reload} /></Card>;
  if (requests.length === 0) return <Card><EmptyState title="暂无申请记录" /></Card>;
  return (
    <Card title="申请记录" actions={<button className="btn btn-ghost btn-sm" onClick={state.reload}>刷新</button>}>
      <div className="alert alert-info" role="alert" style={{ marginBottom: 12 }}>
        申请由 Access Token 自动审批：提交即签发 Lease，无需人工审批。此页为只读历史。
      </div>
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>申请</th>
              <th>AI</th>
              <th>服务器</th>
              <th>Token 来源</th>
              <th>权限</th>
              <th>时长</th>
              <th>使用原因</th>
              <th>公钥指纹</th>
              <th>状态</th>
              <th>决策 / 原因</th>
              <th>申请时间</th>
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
                <td><TokenSourceCell tokenName={r.access_token_name} tokenPrefix={r.access_token_prefix} /></td>
                <td><ProfileBadge profile={r.requested_profile ?? r.permission_profile} /></td>
                <td className="mono">{r.requested_duration_seconds ? `${Math.round(r.requested_duration_seconds / 60)} 分钟` : '—'}</td>
                <td>{r.purpose || '—'}</td>
                <td className="mono" title={r.public_key_fingerprint}>{r.public_key_fingerprint ? shortId(r.public_key_fingerprint, 16) : '—'}</td>
                <td><StatusBadge status={r.status} /></td>
                <td className="muted">{r.decision_reason || '—'}</td>
                <td><TimeCell value={r.created_at} /></td>
              </tr>
            ))}
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
  nodes,
  onChanged,
}: {
  isPrimary: boolean;
  settings: Record<string, unknown> | null;
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

  const newRequests = boolVal(['ai_new_requests_enabled', 'new_requests_enabled', 'ai_new_requests']);
  const renewals = boolVal(['ai_renewals_enabled', 'renewals_enabled', 'ai_renewals']);
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
      // PATCH /settings/ai-access expects scope to BE the node id when
      // node-scoped (contract: scope:"global"|<node_id>).
      const nodeId = scope ? scope.nodeId : isPrimary ? (scopeNode || '') : session?.nodeId || '';
      const body: Record<string, unknown> = {
        scope: nodeId || 'global',
        [key]: enabled,
      };
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
      message: `确认撤销${scopeNode ? '所选服务器范围内' : '全局范围内'}全部活动 Lease？该操作不可撤销。`,
      confirmLabel: '紧急撤销',
      danger: true,
      production: isProd,
      requireReason: true,
      reasonLabel: '撤销原因',
      reasonPlaceholder: '请填写撤销原因（将写入审计）',
    });
    if (!result.ok) return;
    setBusy(true);
    setError(null);
    try {
      const body: Record<string, unknown> = {
        reason: result.reason,
        terminate_sessions: true,
        scope: isPrimary ? (scopeNode ? 'node_id' : 'global') : 'node_id',
      };
      if (body.scope === 'node_id') body.node_id = scopeNode;
      await api.post('/ai/leases/revoke-all', body);
      onChanged();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '撤销失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  if (!isPrimary) {
    return (
      <div className="grid grid-2">
        <Card title="本机 AI 策略">
          {error && <div className="alert alert-danger">{error}</div>}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
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
              <dt>默认时长</dt>
              <dd className="mono">{defaultMinutes !== undefined ? `${defaultMinutes} 分钟` : '—'}</dd>
              <dt>绝对上限</dt>
              <dd className="mono">{maxHours !== undefined ? `${maxHours} 小时` : '—'}</dd>
            </div>
          </div>
        </Card>
        <Card title="紧急撤销">
          <p className="empty-hint" style={{ marginBottom: 12 }}>
            批量紧急撤销由主服务器统一执行；本机（子服务器）请在主服务器「AI 凭证 → 控制策略」中操作，或对单个 Lease 执行撤销。
          </p>
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
            <dt>默认时长</dt>
            <dd className="mono">{defaultMinutes !== undefined ? `${defaultMinutes} 分钟` : '—'}</dd>
            <dt>绝对上限</dt>
            <dd className="mono">{maxHours !== undefined ? `${maxHours} 小时` : '—'}</dd>
          </div>
          <p className="empty-hint">
            Lease 由 Access Token 自动审批：申请带有效 Token 即自动签发，到期时间为「申请时长、Token 到期、绝对上限」中的最早值。数值在「系统设置」中维护。
          </p>
        </div>
      </Card>

      <Card title="服务器范围控制">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <label className="field">
            <span className="field-label">目标服务器</span>
            <Select value={scopeNode} onChange={(e) => setScopeNode(e.target.value)}>
              <option value="">全局（所有服务器）</option>
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
            切换服务器开关将按所选服务器覆盖策略（PATCH /settings/ai-access，scope=node_id）。
          </div>
        </div>
      </Card>

      <Card title="紧急撤销">
        <p className="empty-hint" style={{ marginBottom: 12 }}>
          立即撤销{scopeNode ? '所选服务器范围内' : '全局范围内'}全部活动 Lease，并终止相关 SSH 会话。
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
