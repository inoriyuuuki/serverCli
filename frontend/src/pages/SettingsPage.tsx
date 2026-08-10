import { useMemo, useState, type FormEvent } from 'react';
import { useSession } from '../auth/AuthContext';
import { useApi, errorMessage } from '../lib/useApi';
import { api, unwrapList, unwrapObject, ApiError } from '../api/client';
import type { CleanupRun, VersionInfo } from '../api/types';
import {
  Card,
  Checkbox,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
  Select,
  StatusBadge,
  TextInput,
  TimeCell,
  useConfirm,
} from '../components/ui';

interface ScalarSetting {
  key: string;
  label: string;
  kind: 'number' | 'boolean' | 'select' | 'text';
  options?: string[];
}

const KNOWN_FIELDS: ScalarSetting[] = [
  { key: 'heartbeat_interval_seconds', label: '心跳间隔（秒）', kind: 'number' },
  { key: 'offline_threshold_seconds', label: '离线阈值（秒）', kind: 'number' },
  { key: 'ai_lease_default_minutes', label: 'AI Lease 默认时长（分钟）', kind: 'number' },
  { key: 'ai_lease_max_hours', label: 'AI Lease 绝对上限（小时）', kind: 'number' },
  { key: 'ai_lease_disconnect_grace_seconds', label: '断连宽限（秒）', kind: 'number' },
  { key: 'ai_new_requests_enabled', label: '允许新 AI 申请', kind: 'boolean' },
  { key: 'new_requests_enabled', label: '允许新申请', kind: 'boolean' },
  { key: 'ai_renewals_enabled', label: '允许 AI 续期', kind: 'boolean' },
  { key: 'renewals_enabled', label: '允许续期', kind: 'boolean' },
  { key: 'command_concurrency', label: '命令并发上限', kind: 'number' },
  { key: 'max_concurrent_tasks', label: '最大并发任务数', kind: 'number' },
  { key: 'command_timeout_seconds', label: '命令默认超时（秒）', kind: 'number' },
  { key: 'default_task_timeout_seconds', label: '任务默认超时（秒）', kind: 'number' },
  { key: 'max_output_bytes', label: '输出上限（字节）', kind: 'number' },
  { key: 'task_output_limit_bytes', label: '任务输出上限（字节）', kind: 'number' },
  { key: 'retention_days', label: '数据保留（天）', kind: 'number' },
  { key: 'cleanup_schedule', label: '清理计划', kind: 'select', options: ['weekly', 'daily', 'disabled'] },
];

function humanize(key: string): string {
  return key
    .replace(/_/g, ' ')
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

/** 已废弃设置键：不展示、不可编辑（后端已不再接受这些键的保存）。 */
const DEPRECATED_KEYS = new Set(['ai_approval_mode', 'ai_auto_approval_policy', 'auto_approval_policy']);

function kindFor(key: string, value: unknown): ScalarSetting {
  if (DEPRECATED_KEYS.has(key)) return { key, label: '', kind: 'text' };
  const known = KNOWN_FIELDS.find((f) => f.key === key);
  if (known) return known;
  if (typeof value === 'boolean') return { key, label: humanize(key), kind: 'boolean' };
  if (typeof value === 'number') return { key, label: humanize(key), kind: 'number' };
  if (typeof value === 'string' && ['manual', 'policy', 'disabled', 'weekly', 'daily'].includes(value)) {
    return { key, label: humanize(key), kind: 'select', options: ['manual', 'policy', 'disabled', 'weekly', 'daily'] };
  }
  return { key, label: humanize(key), kind: 'text' };
}

export default function SettingsPage() {
  const session = useSession();
  const confirm = useConfirm();
  const isProd = session?.environment === 'production';

  const settingsState = useApi<unknown>('/settings');
  const versionState = useApi<VersionInfo>('/version', { deps: [] });
  const runsState = useApi<unknown>('/cleanup/runs');

  const settings = useMemo(() => unwrapObject<Record<string, unknown>>(settingsState.data, ['settings']), [settingsState.data]);
  const runs = useMemo(() => unwrapList<CleanupRun>(runsState.data, ['runs']), [runsState.data]);

  const scalarEntries = useMemo(() => {
    if (!settings) return [];
    return Object.entries(settings)
      .filter(([key, v]) => !DEPRECATED_KEYS.has(key) && (typeof v !== 'object' || v === null))
      .map(([key, value]) => ({ key, value, def: kindFor(key, value) }));
  }, [settings]);

  const [draft, setDraft] = useState<Record<string, unknown>>({});
  const [saveBusy, setSaveBusy] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saveOk, setSaveOk] = useState<string | null>(null);

  const setDraftValue = (key: string, value: unknown) => {
    setDraft((prev) => ({ ...prev, [key]: value }));
  };

  const saveSettings = async (e: FormEvent) => {
    e.preventDefault();
    setSaveBusy(true);
    setSaveError(null);
    setSaveOk(null);
    try {
      await api.patch('/settings', draft);
      setSaveOk('设置已保存。');
      settingsState.reload();
      setDraft({});
    } catch (err) {
      setSaveError(err instanceof ApiError ? err.message : '保存失败，请重试');
    } finally {
      setSaveBusy(false);
    }
  };

  /* ------------------------------ cleanup ------------------------------ */

  const [cleanupTypes, setCleanupTypes] = useState<string[]>([]);
  const [cleanupBusy, setCleanupBusy] = useState(false);
  const [cleanupError, setCleanupError] = useState<string | null>(null);
  const [cleanupResult, setCleanupResult] = useState<string | null>(null);

  const DATA_TYPES = [
    ['heartbeats', '心跳明细'],
    ['task_output', '任务输出'],
    ['tasks', '任务元数据'],
    ['ai_lease_requests', 'AI 申请记录'],
    ['ai_leases', '过期 Lease'],
    ['ai_ssh_sessions', 'SSH 会话记录'],
    ['audit_events', '审计日志'],
    ['cleanup_runs', '清理记录'],
  ];

  const toggleCleanupType = (t: string) => {
    setCleanupTypes((prev) => (prev.includes(t) ? prev.filter((x) => x !== t) : [...prev, t]));
  };

  const runCleanup = async (dry: boolean) => {
    const result = await confirm({
      title: dry ? 'Dry-run 清理预览' : '正式执行数据清理',
      message: dry
        ? '将计算符合清理条件的候选数量（不实际删除）。'
        : `将按保留策略实际删除过期数据（${cleanupTypes.length ? `类型：${cleanupTypes.join(', ')}` : '全部类型'}）。重要记录不会被删除。`,
      confirmLabel: dry ? '开始预览' : '确认清理',
      danger: !dry,
      production: isProd,
      requireReason: !dry && isProd,
      reasonLabel: '清理原因',
    });
    if (!result.ok) return;
    setCleanupBusy(true);
    setCleanupError(null);
    setCleanupResult(null);
    try {
      const body: Record<string, unknown> = { dry_run: dry };
      if (cleanupTypes.length) body.data_types = cleanupTypes;
      const resp = await api.post<Record<string, unknown>>('/cleanup/run', body);
      setCleanupResult(JSON.stringify(resp, null, 2));
      runsState.reload();
    } catch (err) {
      setCleanupError(err instanceof ApiError ? err.message : '清理失败，请重试');
    } finally {
      setCleanupBusy(false);
    }
  };

  /* ------------------------------ password ------------------------------ */

  const [oldPw, setOldPw] = useState('');
  const [newPw, setNewPw] = useState('');
  const [newPw2, setNewPw2] = useState('');
  const [pwBusy, setPwBusy] = useState(false);
  const [pwError, setPwError] = useState<string | null>(null);
  const [pwOk, setPwOk] = useState<string | null>(null);

  const changePassword = async (e: FormEvent) => {
    e.preventDefault();
    if (newPw.length < 8) {
      setPwError('新密码长度至少 8 位。');
      return;
    }
    if (newPw !== newPw2) {
      setPwError('两次输入的新密码不一致。');
      return;
    }
    const result = await confirm({
      title: '修改密码',
      message: '修改密码后，其它会话将被撤销，需要重新登录。',
      danger: true,
      production: isProd,
    });
    if (!result.ok) return;
    setPwBusy(true);
    setPwError(null);
    setPwOk(null);
    try {
      await api.post('/auth/password', { old_password: oldPw, new_password: newPw });
      setPwOk('密码已修改。其它会话已撤销，请重新登录。');
      setOldPw('');
      setNewPw('');
      setNewPw2('');
    } catch (err) {
      setPwError(err instanceof ApiError ? err.message : '修改失败，请重试');
    } finally {
      setPwBusy(false);
    }
  };

  const version = versionState.data as VersionInfo | null;

  return (
    <div>
      <PageHeader title={session?.role === 'primary' ? '系统设置' : '受限本机设置'} subtitle={session?.role === 'primary' ? '集群与实例运行参数。' : '子节点仅可修改受限的本机参数。'} />

      <Card title="运行参数" actions={settingsState.error ? undefined : <button className="btn btn-ghost btn-sm" onClick={settingsState.reload}>刷新</button>}>
        {settingsState.loading && settingsState.data === null ? (
          <LoadingState label="加载设置中…" />
        ) : settingsState.error ? (
          <ErrorState message={errorMessage(settingsState.error)} onRetry={settingsState.reload} />
        ) : scalarEntries.length === 0 ? (
          <EmptyState title="暂无设置项" />
        ) : (
          <form onSubmit={saveSettings}>
            <div className="grid grid-2" style={{ gap: 4 }}>
              {scalarEntries.map(({ key, value, def }) => (
                <div key={key} className="field">
                  <span className="field-label">
                    {def.label} <span className="mono muted" style={{ fontWeight: 400 }}>{key}</span>
                  </span>
                  {def.kind === 'boolean' ? (
                    <div className="checkbox-row" style={{ paddingTop: 8 }}>
                      <input
                        type="checkbox"
                        checked={draft[key] !== undefined ? Boolean(draft[key]) : Boolean(value)}
                        onChange={(e) => setDraftValue(key, e.target.checked)}
                      />
                      <span>{draft[key] !== undefined ? Boolean(draft[key]) : Boolean(value) ? '是' : '否'}</span>
                    </div>
                  ) : def.kind === 'number' ? (
                    <TextInput
                      type="number"
                      value={draft[key] !== undefined ? String(draft[key]) : value === null || value === undefined ? '' : String(value)}
                      onChange={(e) => setDraftValue(key, e.target.value === '' ? '' : Number(e.target.value))}
                    />
                  ) : def.kind === 'select' ? (
                    <Select value={String(draft[key] ?? value ?? '')} onChange={(e) => setDraftValue(key, e.target.value)}>
                      {(def.options ?? []).map((o) => (
                        <option key={o} value={o}>
                          {o}
                        </option>
                      ))}
                    </Select>
                  ) : (
                    <TextInput value={String(draft[key] ?? value ?? '')} onChange={(e) => setDraftValue(key, e.target.value)} />
                  )}
                </div>
              ))}
            </div>
            <details style={{ margin: '6px 0 10px' }}>
              <summary className="muted" style={{ cursor: 'pointer', fontSize: 13 }}>
                查看完整设置（只读 JSON）
              </summary>
              <pre className="mono" style={{ background: '#f8fafc', padding: 12, borderRadius: 8, fontSize: 12, overflow: 'auto', maxHeight: 260, marginTop: 8 }}>
                {JSON.stringify(settings, null, 2)}
              </pre>
            </details>
            {saveError && <div className="alert alert-danger">{saveError}</div>}
            {saveOk && <div className="alert alert-success">{saveOk}</div>}
            {isProd && (
              <div className="alert alert-danger" role="alert">
                ⚠️ <strong>正式环境</strong>：保存设置将写入正式环境配置。
              </div>
            )}
            <div className="btn-row" style={{ marginTop: 8 }}>
              <button className="btn btn-primary" type="submit" disabled={saveBusy || Object.keys(draft).length === 0}>
                {saveBusy ? '保存中…' : '保存设置'}
              </button>
              {Object.keys(draft).length > 0 && (
                <button className="btn btn-ghost" type="button" onClick={() => setDraft({})}>
                  撤销修改
                </button>
              )}
            </div>
          </form>
        )}
      </Card>

      <div className="grid grid-2">
        <Card title="数据清理">
          <div className="alert alert-info" style={{ fontSize: 13 }}>
            重要记录（is_protected）不会被自动删除。正式环境清理前请先使用 dry-run 预览。
          </div>
          <div style={{ margin: '12px 0' }}>
            <span className="field-label">数据类型</span>
            <div className="grid grid-2" style={{ gap: 6, marginTop: 6 }}>
              {DATA_TYPES.map(([t, label]) => (
                <Checkbox key={t} label={label} checked={cleanupTypes.includes(t)} onChange={() => toggleCleanupType(t)} />
              ))}
            </div>
            <p className="empty-hint">不勾选表示全部类型。</p>
          </div>
          <div className="btn-row">
            <button className="btn" onClick={() => runCleanup(true)} disabled={cleanupBusy}>
              Dry-run 预览
            </button>
            <button className="btn btn-danger" onClick={() => runCleanup(false)} disabled={cleanupBusy}>
              正式清理
            </button>
          </div>
          {cleanupError && <div className="alert alert-danger" style={{ marginTop: 12 }}>{cleanupError}</div>}
          {cleanupResult && (
            <pre className="mono" style={{ background: '#f8fafc', padding: 12, borderRadius: 8, fontSize: 12, marginTop: 12, overflow: 'auto', maxHeight: 220 }}>
              {cleanupResult}
            </pre>
          )}
        </Card>

        <div>
          <Card title="清理记录" actions={<button className="btn btn-ghost btn-sm" onClick={runsState.reload}>刷新</button>}>
            {runsState.loading && runsState.data === null ? (
              <LoadingState label="加载清理记录中…" />
            ) : runsState.error ? (
              <ErrorState message={errorMessage(runsState.error)} onRetry={runsState.reload} />
            ) : runs.length === 0 ? (
              <EmptyState title="暂无清理记录" />
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <thead>
                    <tr>
                      <th>时间</th>
                      <th>触发</th>
                      <th>候选</th>
                      <th>删除</th>
                      <th>保护跳过</th>
                      <th>状态</th>
                    </tr>
                  </thead>
                  <tbody>
                    {runs.slice(0, 10).map((r) => (
                      <tr key={r.id}>
                        <td><TimeCell value={r.started_at} /></td>
                        <td className="mono">{r.trigger_type || '—'}</td>
                        <td className="num">{r.candidate_count ?? '—'}</td>
                        <td className="num">{r.deleted_count ?? '—'}</td>
                        <td className="num">{r.skipped_protected_count ?? '—'}</td>
                        <td><StatusBadge status={r.status} /></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>

          <Card title="系统版本">
            {versionState.loading && !version ? (
              <LoadingState label="加载版本信息中…" />
            ) : versionState.error ? (
              <ErrorState message={errorMessage(versionState.error)} onRetry={versionState.reload} />
            ) : version ? (
              <dl className="kv kv-2col">
                <dt>系统版本</dt>
                <dd className="mono">{version.version || '—'}</dd>
                <dt>构建</dt>
                <dd className="mono">{version.build || '—'}</dd>
                <dt>Commit</dt>
                <dd className="mono">{version.commit ? String(version.commit).slice(0, 12) : '—'}</dd>
                <dt>数据库迁移版本</dt>
                <dd className="mono">{version.migration_version ?? version.database_migration_version ?? '—'}</dd>
              </dl>
            ) : (
              <EmptyState title="无版本信息" />
            )}
          </Card>
        </div>
      </div>

      <Card title="修改密码">
        <form onSubmit={changePassword} style={{ maxWidth: 420 }}>
          <label className="field">
            <span className="field-label">当前密码</span>
            <TextInput type="password" autoComplete="current-password" value={oldPw} onChange={(e) => setOldPw(e.target.value)} required />
          </label>
          <label className="field">
            <span className="field-label">新密码</span>
            <TextInput type="password" autoComplete="new-password" value={newPw} onChange={(e) => setNewPw(e.target.value)} required />
          </label>
          <label className="field">
            <span className="field-label">确认新密码</span>
            <TextInput type="password" autoComplete="new-password" value={newPw2} onChange={(e) => setNewPw2(e.target.value)} required />
          </label>
          {pwError && <div className="alert alert-danger">{pwError}</div>}
          {pwOk && <div className="alert alert-success">{pwOk}</div>}
          <button className="btn btn-primary" type="submit" disabled={pwBusy}>
            {pwBusy ? '提交中…' : '修改密码'}
          </button>
        </form>
      </Card>
    </div>
  );
}
