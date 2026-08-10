import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { useApi, errorMessage } from '../lib/useApi';
import { useRealtime } from '../lib/realtime';
import { api, unwrapList, unwrapObject, ApiError } from '../api/client';
import type {
  ApiAccessToken,
  ApiTokenUsageLog,
  PermissionCatalogResponse,
  PermissionDef,
  PermissionGrant,
  PermissionSet,
} from '../api/types';
import {
  Alert,
  Badge,
  Card,
  Checkbox,
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

const EMPTY_CATALOG: PermissionCatalogResponse = { categories: [], permissions: [] };

type ApiTokenWithCount = ApiAccessToken & { active_lease_count?: number };

/* ------------------------- 权限目录/摘要 辅助函数 ------------------------- */

/** 资源+动作的唯一键（前端绝不生成 "*" 通配符，仅由目录项勾选而来）。 */
function permissionKey(resource: string, action: string): string {
  return `${resource}:${action}`;
}

function catalogIndex(permissions: PermissionDef[]): Map<string, PermissionDef> {
  const idx = new Map<string, PermissionDef>();
  for (const p of permissions) idx.set(permissionKey(p.resource, p.action), p);
  return idx;
}

/** 已撤销或已过期：权限只读。 */
function isTokenLocked(token: ApiAccessToken): boolean {
  if (token.revoked_at) return true;
  if (token.expires_at) {
    const t = new Date(token.expires_at).getTime();
    if (!Number.isNaN(t) && t <= Date.now()) return true;
  }
  return false;
}

/** 勾选集 → grants：同一 resource 合并 actions；全部不勾则为空数组。 */
function grantsFromChecked(checked: Set<string>, permissions: PermissionDef[]): PermissionGrant[] {
  const byResource = new Map<string, string[]>();
  for (const def of permissions) {
    if (checked.has(permissionKey(def.resource, def.action))) {
      const actions = byResource.get(def.resource) ?? [];
      actions.push(def.action);
      byResource.set(def.resource, actions);
    }
  }
  return Array.from(byResource.entries()).map(([resource, actions]) => ({ resource, actions }));
}

/** token.permissions → 勾选集。 */
function checkedFromPermissions(permissions: PermissionSet | undefined): Set<string> {
  const out = new Set<string>();
  for (const grant of permissions?.grants ?? []) {
    for (const action of grant.actions ?? []) {
      out.add(permissionKey(grant.resource, action));
    }
  }
  return out;
}

/** 权限摘要文本：按分类列出已授予项 label；无授予则“无权限”。 */
function permissionSummaryText(permissions: PermissionSet | undefined, catalog: PermissionCatalogResponse): string {
  const idx = catalogIndex(catalog.permissions);
  const grants = permissions?.grants ?? [];
  if (grants.length === 0) return '无权限';
  const byCategory = new Map<string, string[]>();
  for (const grant of grants) {
    for (const action of grant.actions ?? []) {
      const def = idx.get(permissionKey(grant.resource, action));
      const category = def?.category ?? grant.resource;
      const label = def?.label ?? `${grant.resource}:${action}`;
      const arr = byCategory.get(category) ?? [];
      arr.push(label);
      byCategory.set(category, arr);
    }
  }
  return Array.from(byCategory.entries())
    .map(([category, labels]) => {
      const catLabel = catalog.categories.find((c) => c.category === category)?.label ?? category;
      return `${catLabel}：${labels.join('、')}`;
    })
    .join('；');
}

/* ------------------------------ 权限编辑弹窗 ------------------------------ */

function PermissionEditor({
  token,
  onSaved,
  onClose,
  onConflict,
}: {
  token: ApiAccessToken;
  onSaved: (t: ApiAccessToken) => void;
  onClose: () => void;
  onConflict: () => void;
}) {
  // 打开编辑时拉取权限目录（按分类通用渲染，不硬编码分类/权限项）。
  const catalogState = useApi<unknown>('/api-tokens/permissions/catalog');
  const catalog = useMemo<PermissionCatalogResponse>(
    () => (catalogState.data as PermissionCatalogResponse | null) ?? EMPTY_CATALOG,
    [catalogState.data],
  );

  const [checked, setChecked] = useState<Set<string>>(() => checkedFromPermissions(token.permissions));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const lastRevision = useRef<number>(token.permission_version ?? 0);

  // 409 冲突后父级已重新拉取详情：token 版本变化时，将勾选同步为服务器最新权限，
  // 绝不静默覆盖服务器上的新状态。
  useEffect(() => {
    const rev = token.permission_version ?? 0;
    if (rev !== lastRevision.current) {
      lastRevision.current = rev;
      setChecked(checkedFromPermissions(token.permissions));
    }
  }, [token]);

  const locked = isTokenLocked(token);

  const editorCategories = useMemo(() => {
    const catMap = new Map<string, string>();
    for (const c of catalog.categories) catMap.set(c.category, c.label);
    // 目录中可能出现未在 categories 中声明的分类，兜底显示原始分类名。
    for (const p of catalog.permissions) {
      if (!catMap.has(p.category)) catMap.set(p.category, p.category);
    }
    return Array.from(catMap.entries()).map(([category, label]) => ({ category, label }));
  }, [catalog.categories, catalog.permissions]);

  const toggle = (key: string, on: boolean) => {
    setChecked((prev) => {
      const next = new Set(prev);
      if (on) next.add(key);
      else next.delete(key);
      return next;
    });
  };

  const toggleCategory = (category: string, on: boolean) => {
    setChecked((prev) => {
      const next = new Set(prev);
      for (const def of catalog.permissions) {
        if (def.category !== category) continue;
        const k = permissionKey(def.resource, def.action);
        if (on) next.add(k);
        else next.delete(k);
      }
      return next;
    });
  };

  const categoryState = (category: string): 'all' | 'some' | 'none' => {
    const defs = catalog.permissions.filter((d) => d.category === category);
    if (defs.length === 0) return 'none';
    const all = defs.every((d) => checked.has(permissionKey(d.resource, d.action)));
    if (all) return 'all';
    return defs.some((d) => checked.has(permissionKey(d.resource, d.action))) ? 'some' : 'none';
  };

  const save = async () => {
    if (!token.id) return;
    setBusy(true);
    setError(null);
    try {
      const res = await api.put<{ api_token: ApiAccessToken }>(`/api-tokens/${token.id}/permissions`, {
        permission_version: token.permission_version,
        permissions: { version: 1, grants: grantsFromChecked(checked, catalog.permissions) },
      });
      onSaved(res.api_token);
      onClose();
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        // 保留提示并重新拉取服务器最新数据，不静默覆盖。
        setError('保存冲突：该 Token 的权限已被其他操作修改。已重新加载服务器最新权限，请基于最新状态重新勾选后保存。');
        onConflict();
      } else {
        setError(err instanceof ApiError ? err.message : '保存失败，请重试');
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title={`编辑权限：${token.name || token.token_prefix || ''}`}
      onClose={() => {
        if (!busy) onClose();
      }}
      width={620}
    >
      <div>
        <Alert tone="info">勾选后点击保存即生效；权限按目录分类授予，通知发送需授权 notifications:send。</Alert>
        {locked && (
          <Alert tone="danger">该 Token 已撤销或已过期，权限为只读，无法修改。</Alert>
        )}
        {catalogState.loading && catalogState.data === null && <LoadingState label="加载权限目录中…" />}
        {catalogState.error && <ErrorState message={errorMessage(catalogState.error)} onRetry={catalogState.reload} />}
        {!catalogState.error && catalogState.data !== null && catalog.permissions.length === 0 && (
          <EmptyState title="暂无可用权限项" hint="权限目录为空，无法编辑。" />
        )}
        {catalog.permissions.length > 0 && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 16, marginTop: 4 }}>
            {editorCategories.map((cat) => {
              const defs = catalog.permissions.filter((d) => d.category === cat.category);
              if (defs.length === 0) return null;
              const state = categoryState(cat.category);
              return (
                <div key={cat.category}>
                  <Checkbox
                    label={<strong>{cat.label}</strong>}
                    checked={state === 'all'}
                    onChange={(v) => toggleCategory(cat.category, v)}
                    disabled={locked || busy}
                  />
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 5, paddingLeft: 24, marginTop: 5 }}>
                    {defs.map((def) => (
                      <Checkbox
                        key={permissionKey(def.resource, def.action)}
                        label={
                          <span title={def.description}>
                            {def.label}
                            <span className="muted mono" style={{ fontSize: 11, marginLeft: 6 }}>
                              {def.resource}:{def.action}
                            </span>
                          </span>
                        }
                        checked={checked.has(permissionKey(def.resource, def.action))}
                        onChange={(v) => toggle(permissionKey(def.resource, def.action), v)}
                        disabled={locked || busy}
                      />
                    ))}
                  </div>
                </div>
              );
            })}
          </div>
        )}
        {error && <Alert tone="danger" >{error}</Alert>}
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost" onClick={onClose} disabled={busy}>取消</button>
          <button
            type="button"
            className="btn btn-primary"
            onClick={save}
            disabled={locked || busy || catalogState.data === null}
          >
            {busy ? '保存中…' : '保存权限'}
          </button>
        </div>
      </div>
    </Modal>
  );
}

/* --------------------------------- 页面 ---------------------------------- */

export default function ApiTokensPage() {
  const confirm = useConfirm();
  const [tab, setTab] = useState<'tokens' | 'usage'>('tokens');
  const [detailToken, setDetailToken] = useState<ApiTokenWithCount | null>(null);
  const [outcomeFilter, setOutcomeFilter] = useState('');

  const tokensState = useApi<unknown>('/api-tokens', { query: { limit: 200 }, pollIntervalMs: 30000 });
  const tokens = useMemo(() => unwrapList<ApiTokenWithCount>(tokensState.data, ['api_tokens']), [tokensState.data]);

  // 权限目录：用于列表/详情展示权限摘要（编辑弹窗打开时另行拉取最新目录）。
  const catalogState = useApi<unknown>('/api-tokens/permissions/catalog');
  const catalog = useMemo<PermissionCatalogResponse>(
    () => (catalogState.data as PermissionCatalogResponse | null) ?? EMPTY_CATALOG,
    [catalogState.data],
  );

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
        新 Token 默认无权限，需主节点管理员在详情中按分类授权；通知发送需授权 <code>notifications:send</code>。
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
                    <th>权限</th>
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
                    const permText = permissionSummaryText(t.permissions, catalog);
                    return (
                      <tr key={t.id}>
                        <td>
                          {t.name || '—'}
                          {!t.expires_at && !revoked && <Badge tone="red">永久</Badge>}
                        </td>
                        <td className="mono" title={t.id}>{t.token_prefix}…</td>
                        <td>
                          <span className="muted" style={{ fontSize: 12 }} title={permText}>
                            {permText.length > 28 ? `${permText.slice(0, 28)}…` : permText}
                          </span>
                        </td>
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
            <Alert tone="info">当前无权限，需管理员授权后使用。</Alert>
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

      {/* 详情弹窗：权限 + 使用日志 */}
      <Modal open={detailToken !== null} title={`Token 详情 / 使用日志：${detailToken?.name || ''}`} onClose={() => setDetailToken(null)} width={720}>
        {detailToken && (
          <TokenDetail
            token={detailToken}
            catalog={catalog}
            outcome={outcomeFilter}
            onOutcomeChange={setOutcomeFilter}
            onTokensChanged={tokensState.reload}
          />
        )}
      </Modal>
    </div>
  );
}

function TokenDetail({
  token,
  catalog,
  outcome,
  onOutcomeChange,
  onTokensChanged,
}: {
  token: ApiTokenWithCount;
  catalog: PermissionCatalogResponse;
  outcome: string;
  onOutcomeChange: (v: string) => void;
  onTokensChanged: () => void;
}) {
  // 打开详情时拉取最新 Token（含 permissions 与 permission_version 乐观锁版本）。
  const detailState = useApi<unknown>(`/api-tokens/${token.id}`, { deps: [token.id] });
  const detailToken = useMemo<ApiTokenWithCount>(
    () => unwrapObject<ApiTokenWithCount>(detailState.data, ['api_token']) ?? token,
    [detailState.data, token],
  );

  const logsState = useApi<unknown>(`/api-tokens/${token.id}/usage-logs`, {
    query: { limit: 100, outcome: outcome || undefined },
    deps: [token.id, outcome],
  });
  const logs = useMemo(() => unwrapList<ApiTokenUsageLog>(logsState.data, ['usage_logs']), [logsState.data]);

  const [permEditorOpen, setPermEditorOpen] = useState(false);
  const locked = isTokenLocked(detailToken);

  const handlePermissionsSaved = () => {
    // 保存成功后刷新详情与列表，使乐观锁版本与权限摘要同步。
    detailState.reload();
    onTokensChanged();
  };

  const handlePermissionConflict = () => {
    // 409：保留编辑弹窗中的提示，并重新拉取服务器最新数据。
    detailState.reload();
  };

  return (
    <div>
      <div className="kv" style={{ marginBottom: 12 }}>
        <dt>名称</dt>
        <dd>{detailToken.name || '—'}</dd>
        <dt>前缀</dt>
        <dd className="mono">{detailToken.token_prefix}…</dd>
        <dt>状态</dt>
        <dd>{detailToken.revoked_at ? <Badge tone="gray">已撤销</Badge> : <Badge tone="teal">有效</Badge>}</dd>
        <dt>到期</dt>
        <dd>{detailToken.expires_at ? <TimeCell value={detailToken.expires_at} /> : <Badge tone="red">永久</Badge>}</dd>
        <dt>最近使用</dt>
        <dd><TimeCell value={detailToken.last_used_at} /></dd>
        <dt>使用次数</dt>
        <dd className="num">{detailToken.usage_count ?? 0}</dd>
      </div>

      {/* 权限摘要 + 编辑入口 */}
      <div className="card" style={{ marginBottom: 12 }}>
        <div className="card-head">
          <h2 className="card-title">权限</h2>
          <div className="card-actions">
            {locked && <Badge tone="gray">只读（已撤销或已过期）</Badge>}
            {!locked && (
              <button className="btn btn-ghost btn-sm" onClick={() => setPermEditorOpen(true)}>
                编辑权限
              </button>
            )}
          </div>
        </div>
        <div className="card-body" style={{ paddingTop: 4 }}>
          {detailState.loading && detailState.data === null && <LoadingState label="加载权限中…" />}
          {detailState.error && <ErrorState message={errorMessage(detailState.error)} onRetry={detailState.reload} />}
          {!detailState.error && <PermissionSummary permissions={detailToken.permissions} catalog={catalog} />}
        </div>
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

      {/* 权限编辑弹窗：仅在打开时挂载（每次打开重新拉取目录并初始化勾选） */}
      {permEditorOpen && (
        <PermissionEditor
          token={detailToken}
          onSaved={handlePermissionsSaved}
          onClose={() => setPermEditorOpen(false)}
          onConflict={handlePermissionConflict}
        />
      )}
    </div>
  );
}

/** 权限摘要：按分类列出已授予项的 label；无权限时显示“无权限”。 */
function PermissionSummary({
  permissions,
  catalog,
}: {
  permissions?: PermissionSet;
  catalog: PermissionCatalogResponse;
}) {
  const idx = useMemo(() => catalogIndex(catalog.permissions), [catalog.permissions]);
  const grants = permissions?.grants ?? [];
  if (grants.length === 0) {
    return <span className="muted">无权限</span>;
  }
  const byCategory = new Map<string, PermissionDef[]>();
  for (const grant of grants) {
    for (const action of grant.actions ?? []) {
      const def = idx.get(permissionKey(grant.resource, action));
      if (!def) continue;
      const arr = byCategory.get(def.category) ?? [];
      arr.push(def);
      byCategory.set(def.category, arr);
    }
  }
  if (byCategory.size === 0) {
    // 授予项不在目录中（异常数据）：回退展示原始 resource:action。
    return (
      <span className="muted mono" style={{ fontSize: 12 }}>
        {grants.map((g) => `${g.resource}:${(g.actions ?? []).join(',')}`).join('；')}
      </span>
    );
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      {Array.from(byCategory.entries()).map(([category, defs]) => {
        const catLabel = catalog.categories.find((c) => c.category === category)?.label ?? category;
        return (
          <div key={category} style={{ fontSize: 13, lineHeight: 1.5 }}>
            <Badge tone="blue" title={catLabel}>{catLabel}</Badge>{' '}
            <span>{defs.map((d) => d.label).join('、')}</span>
          </div>
        );
      })}
    </div>
  );
}

// 导出类型以便其他模块引用
export type { ApiAccessToken };
