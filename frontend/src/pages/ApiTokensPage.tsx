import { useMemo, useState, type FormEvent } from 'react';
import { useApi, errorMessage } from '../lib/useApi';
import { useRealtime } from '../lib/realtime';
import { api, unwrapList, ApiError } from '../api/client';
import type { ApiAccessToken, ApiTokenUsageLog } from '../api/types';
import {
  Badge,
  Card,
  EmptyState,
  ErrorState,
  LoadingState,
  Modal,
  PageHeader,
  Select,
  Tabs,
  TextInput,
  TimeCell,
  useConfirm,
} from '../components/ui';
import { remainingText } from '../lib/format';

const TTL_OPTIONS: { value: string; label: string }[] = [
  { value: '15m', label: '15 分钟' },
  { value: '1h', label: '1 小时' },
  { value: '6h', label: '6 小时' },
  { value: '1d', label: '1 天' },
  { value: '1w', label: '1 周' },
  { value: 'never', label: '永久' },
];

const nowMs = () => Date.now();

type ApiTokenWithCount = ApiAccessToken & { active_lease_count?: number };

export default function ApiTokensPage() {
  const confirm = useConfirm();
  const [tab, setTab] = useState<'tokens' | 'usage'>('tokens');
  const [detailToken, setDetailToken] = useState<ApiTokenWithCount | null>(null);
  const [outcomeFilter, setOutcomeFilter] = useState('');

  const tokensState = useApi<unknown>('/api-tokens', { query: { limit: 200 }, pollIntervalMs: 30000 });
  const tokens = useMemo(() => unwrapList<ApiTokenWithCount>(tokensState.data, ['api_tokens']), [tokensState.data]);

  useRealtime(['leases_changed'], () => {
    tokensState.reload();
  });

  const [createOpen, setCreateOpen] = useState(false);
  const [createName, setCreateName] = useState('');
  const [createTtl, setCreateTtl] = useState('1h');
  const [createBusy, setCreateBusy] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [createdPlaintext, setCreatedPlaintext] = useState<string | null>(null);

  const openCreate = () => {
    setCreateName('');
    setCreateTtl('1h');
    setCreateError(null);
    setCreatedPlaintext(null);
    setCreateOpen(true);
  };

  // Closing during an in-flight create would permanently lose the one-time
  // plaintext, so the modal cannot be dismissed until the request resolves.
  const closeCreate = () => {
    if (createBusy) return;
    setCreateOpen(false);
    setCreatedPlaintext(null);
  };

  const submitCreate = async (e: FormEvent) => {
    e.preventDefault();
    setCreateBusy(true);
    setCreateError(null);
    try {
      const res = await api.post<{ token: string; api_token: ApiAccessToken }>('/api-tokens', {
        name: createName.trim(),
        ttl: createTtl,
      });
      setCreatedPlaintext(res.token);
      setCreateName('');
      tokensState.reload();
    } catch (err) {
      setCreateError(err instanceof ApiError ? err.message : '创建失败，请重试');
    } finally {
      setCreateBusy(false);
    }
  };

  const copyPlaintext = async () => {
    if (!createdPlaintext) return;
    try {
      await navigator.clipboard.writeText(createdPlaintext);
    } catch {
      // 剪贴板不可用时用户可手动复制
    }
  };

  const revoke = async (tok: ApiTokenWithCount) => {
    const result = await confirm({
      title: '撤销 Access Token',
      message: (
        <div className="kv">
          <dt>Token</dt>
          <dd>
            {tok.name || '—'}
            <div className="muted mono" style={{ fontSize: 12 }}>{tok.token_prefix}…</div>
          </dd>
          <dt>关联活动 Lease</dt>
          <dd className="mono">{tok.active_lease_count ?? '—'}</dd>
        </div>
      ),
      confirmLabel: '确认撤销',
      danger: true,
      requireReason: true,
      reasonLabel: '撤销原因',
      reasonPlaceholder: '请填写撤销原因（将写入审计）',
    });
    if (!result.ok) return;
    try {
      await api.post(`/api-tokens/${tok.id}/revoke`, { reason: result.reason });
      tokensState.reload();
      if (detailToken?.id === tok.id) {
        setDetailToken(null);
        setTab('tokens');
      }
    } catch (err) {
      window.alert(err instanceof ApiError ? err.message : '撤销失败，请重试');
    }
  };

  const openDetail = (tok: ApiTokenWithCount) => {
    setDetailToken(tok);
    setOutcomeFilter('');
  };

  const ttlLabel = (expiresAt?: string | null) => {
    if (!expiresAt) return '永久';
    const diff = new Date(expiresAt).getTime() - Date.now();
    if (diff <= 0) return '已到期';
    const h = Math.round(diff / 3600000);
    if (h < 48) return `${h} 小时后`;
    return `${Math.round(h / 24)} 天后`;
  };

  return (
    <div>
      <PageHeader title="API Token" subtitle="Access Token（sct_*）管理：外部 AI 自助 API 的凭证与自动审批依据。" />
      <div className="alert alert-info" role="alert" style={{ marginBottom: 14 }}>
        外部 AI 调用 <code>POST /api/v1/ai/lease-requests</code> 等接口时必须携带 <code>Authorization: Bearer sct_*</code>。
        当前所有 Token 拥有全部权限（可申请包括 admin/root 在内的 Lease）；后续将支持按功能细分。
      </div>

      <Tabs
        tabs={[
          { key: 'tokens', label: 'Token 列表', count: tokens.length },
          { key: 'usage', label: '使用日志' },
        ]}
        active={tab}
        onChange={(k) => setTab(k as 'tokens' | 'usage')}
      />

      {tab === 'tokens' && (
        <Card
          title="Token 列表"
          actions={
            <>
              <button className="btn btn-ghost btn-sm" onClick={tokensState.reload}>刷新</button>
              <button className="btn btn-primary btn-sm" onClick={openCreate}>创建 Token</button>
            </>
          }
        >
          {tokensState.loading && tokensState.data === null && <LoadingState label="加载 Token 中…" />}
          {tokensState.error && <ErrorState message={errorMessage(tokensState.error)} onRetry={tokensState.reload} />}
          {!tokensState.error && tokensState.data !== null && tokens.length === 0 && (
            <EmptyState title="暂无 Token" hint="创建一个 Access Token 供 AI Agent 自助申请 Lease。" />
          )}
          {tokens.length > 0 && (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>名称</th>
                    <th>前缀</th>
                    <th>状态</th>
                    <th>有效期</th>
                    <th>剩余</th>
                    <th>创建时间</th>
                    <th>最近使用</th>
                    <th>使用次数</th>
                    <th>活动 Lease</th>
                    <th style={{ width: 150 }}>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {tokens.map((t) => {
                    const rem = remainingText(t.expires_at, nowMs());
                    const revoked = !!t.revoked_at;
                    return (
                      <tr key={t.id}>
                        <td>
                          {t.name || '—'}
                          {!t.expires_at && !revoked && <Badge tone="red">永久</Badge>}
                        </td>
                        <td className="mono" title={t.id}>{t.token_prefix}…</td>
                        <td>
                          {revoked ? <Badge tone="gray">已撤销</Badge> : rem.kind === 'over' ? <Badge tone="amber">已过期</Badge> : <Badge tone="teal">有效</Badge>}
                        </td>
                        <td>{ttlLabel(t.expires_at)}</td>
                        <td>
                          <span className={rem.kind === 'warn' ? 'text-danger' : rem.kind === 'ok' ? 'text-success' : undefined}>
                            {revoked ? '—' : rem.text}
                          </span>
                        </td>
                        <td><TimeCell value={t.created_at} /></td>
                        <td><TimeCell value={t.last_used_at} /></td>
                        <td className="num">{t.usage_count ?? 0}</td>
                        <td className="num">{t.active_lease_count ?? '—'}</td>
                        <td>
                          <div className="btn-row">
                            <button className="btn btn-ghost btn-sm" onClick={() => openDetail(t)}>详情 / 日志</button>
                            {!revoked && (
                              <button className="btn btn-danger btn-sm" onClick={() => revoke(t)}>撤销</button>
                            )}
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
      )}

      {tab === 'usage' && (
        <Card title="使用日志" actions={<button className="btn btn-ghost btn-sm" onClick={tokensState.reload}>刷新列表</button>}>
          <p className="empty-hint" style={{ marginBottom: 12 }}>
            在 Token 详情中按 Token 查看分页使用记录。日志仅保存路由、状态与结果，不含 Token 明文与敏感请求体。
          </p>
          {tokens.length === 0 ? (
            <EmptyState title="暂无 Token" />
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>Token</th>
                    <th>最近使用</th>
                    <th>使用次数</th>
                    <th>状态</th>
                    <th style={{ width: 120 }}>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {tokens.map((t) => (
                    <tr key={t.id}>
                      <td>
                        {t.name || '—'}
                        <div className="muted mono" style={{ fontSize: 12 }}>{t.token_prefix}…</div>
                      </td>
                      <td><TimeCell value={t.last_used_at} /></td>
                      <td className="num">{t.usage_count ?? 0}</td>
                      <td>{t.revoked_at ? <Badge tone="gray">已撤销</Badge> : <Badge tone="teal">有效</Badge>}</td>
                      <td><button className="btn btn-ghost btn-sm" onClick={() => openDetail(t)}>查看日志</button></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}

      {/* 创建弹窗：创建成功后展示一次明文 */}
      <Modal open={createOpen} title="创建 Access Token" onClose={closeCreate} width={520}>
        {createdPlaintext ? (
          <div>
            <div className="alert alert-danger" role="alert">
              ⚠️ <strong>明文只显示这一次</strong>：关闭后无法再次查看，请立即保存。
            </div>
            <label className="field">
              <span className="field-label">Access Token（仅本次可见）</span>
              <div className="mono" style={{ wordBreak: 'break-all', background: 'var(--bg-soft, #f6f7f9)', padding: 10, borderRadius: 8 }}>
                {createdPlaintext}
              </div>
            </label>
            <div className="modal-actions">
              <button type="button" className="btn btn-primary" onClick={copyPlaintext}>复制</button>
              <button type="button" className="btn btn-ghost" onClick={() => setCreateOpen(false)}>我已保存，关闭</button>
            </div>
          </div>
        ) : (
          <form onSubmit={submitCreate}>
            <label className="field">
              <span className="field-label">名称 <em className="req">*</em></span>
              <TextInput
                value={createName}
                onChange={(e) => setCreateName(e.target.value)}
                placeholder="例如 my-ai-agent"
                maxLength={100}
                required
              />
              <span className="field-hint">仅用于管理端识别，最多 100 字符。</span>
            </label>
            <label className="field">
              <span className="field-label">有效期 <em className="req">*</em></span>
              <Select value={createTtl} onChange={(e) => setCreateTtl(e.target.value)}>
                {TTL_OPTIONS.map((o) => (
                  <option key={o.value} value={o.value}>{o.label}</option>
                ))}
              </Select>
              <span className="field-hint">
                到期后 Token 自动失效，关联 Lease 随之失效；永久 Token 不代表永久 SSH Lease（仍受系统绝对上限约束）。
              </span>
            </label>
            {createTtl === 'never' && (
              <div className="alert alert-danger" role="alert">⚠️ 永久 Token 风险较高：建议仅用于可信的长期自动化任务。</div>
            )}
            {createError && <div className="alert alert-danger">{createError}</div>}
            <div className="modal-actions">
              <button type="button" className="btn btn-ghost" onClick={() => setCreateOpen(false)} disabled={createBusy}>取消</button>
              <button type="submit" className="btn btn-primary" disabled={createBusy || !createName.trim()}>
                {createBusy ? '创建中…' : '创建'}
              </button>
            </div>
          </form>
        )}
      </Modal>

      {/* 详情弹窗：使用日志 */}
      <Modal open={detailToken !== null} title={`Token 详情 / 使用日志：${detailToken?.name || ''}`} onClose={() => setDetailToken(null)} width={680}>
        {detailToken && <TokenDetail token={detailToken} outcome={outcomeFilter} onOutcomeChange={setOutcomeFilter} />}
      </Modal>
    </div>
  );
}

function TokenDetail({
  token,
  outcome,
  onOutcomeChange,
}: {
  token: ApiAccessToken;
  outcome: string;
  onOutcomeChange: (v: string) => void;
}) {
  const logsState = useApi<unknown>(`/api-tokens/${token.id}/usage-logs`, {
    query: { limit: 100, outcome: outcome || undefined },
    deps: [token.id, outcome],
  });
  const logs = useMemo(() => unwrapList<ApiTokenUsageLog>(logsState.data, ['usage_logs']), [logsState.data]);

  return (
    <div>
      <div className="kv" style={{ marginBottom: 12 }}>
        <dt>名称</dt>
        <dd>{token.name || '—'}</dd>
        <dt>前缀</dt>
        <dd className="mono">{token.token_prefix}…</dd>
        <dt>状态</dt>
        <dd>{token.revoked_at ? <Badge tone="gray">已撤销</Badge> : <Badge tone="teal">有效</Badge>}</dd>
        <dt>到期</dt>
        <dd>{token.expires_at ? <TimeCell value={token.expires_at} /> : <Badge tone="red">永久</Badge>}</dd>
        <dt>最近使用</dt>
        <dd><TimeCell value={token.last_used_at} /></dd>
        <dt>使用次数</dt>
        <dd className="num">{token.usage_count ?? 0}</dd>
      </div>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 10 }}>
        <span className="muted" style={{ fontSize: 13 }}>结果筛选</span>
        <Select value={outcome} onChange={(e) => onOutcomeChange(e.target.value)} style={{ width: 160 }}>
          <option value="">全部</option>
          <option value="success">成功</option>
          <option value="denied">拒绝</option>
          <option value="failure">失败</option>
        </Select>
        <button className="btn btn-ghost btn-sm" onClick={logsState.reload}>刷新</button>
      </div>
      {logsState.loading && logsState.data === null && <LoadingState label="加载使用日志中…" />}
      {logsState.stale && <div className="muted" style={{ fontSize: 12, marginBottom: 6 }}>刷新中…</div>}
      {logsState.error && <ErrorState message={errorMessage(logsState.error)} onRetry={logsState.reload} />}
      {logs.length === 0 && !logsState.loading && !logsState.error && <EmptyState title="暂无使用记录" />}
      {logs.length > 0 && (
        <div className="table-wrap" style={{ maxHeight: 360, overflow: 'auto' }}>
          <table className="table">
            <thead>
              <tr>
                <th>时间</th>
                <th>方法</th>
                <th>路由</th>
                <th>结果</th>
                <th>状态</th>
                <th>来源</th>
              </tr>
            </thead>
            <tbody>
              {logs.map((l) => (
                <tr key={l.id}>
                  <td><TimeCell value={l.occurred_at} /></td>
                  <td className="mono">{l.method}</td>
                  <td className="mono" title={l.route}>{l.route}</td>
                  <td>
                    <Badge tone={l.outcome === 'success' ? 'teal' : l.outcome === 'denied' ? 'amber' : 'red'}>
                      {l.outcome === 'success' ? '成功' : l.outcome === 'denied' ? '拒绝' : '失败'}
                    </Badge>
                  </td>
                  <td className="num">{l.status_code}</td>
                  <td className="mono muted">{l.source_ip || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// 导出类型以便其他模块引用
export type { ApiAccessToken };
