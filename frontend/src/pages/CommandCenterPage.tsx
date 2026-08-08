import { useCallback, useMemo, useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { useSession } from '../auth/AuthContext';
import { useApi, errorMessage } from '../lib/useApi';
import { api, unwrapList, ApiError } from '../api/client';
import type { CommandInfo, NodeInfo } from '../api/types';
import {
  Badge,
  Card,
  EmptyState,
  ErrorState,
  LoadingState,
  Modal,
  PageHeader,
  ProfileBadge,
  Select,
  TextInput,
  useConfirm,
} from '../components/ui';
import { nodeName } from '../components/NodeInfo';
import { isSensitiveKey, newIdempotencyKey, redact } from '../lib/format';

/* ------------------------- parameter schema helpers ------------------------ */

interface ParamProp {
  type?: string;
  enum?: unknown[];
  default?: unknown;
  description?: string;
  title?: string;
  minimum?: number;
  maximum?: number;
  items?: ParamProp;
  [key: string]: unknown;
}

interface ParamSchema {
  type?: string;
  required?: string[];
  properties?: Record<string, ParamProp>;
  additionalProperties?: boolean;
}

function parseSchema(raw: unknown): ParamSchema {
  if (!raw) return {};
  // The API returns parameter_schema_json as a JSON string; accept both the
  // serialized string and an already-decoded object.
  if (typeof raw === 'string') {
    try {
      const parsed = JSON.parse(raw);
      return parsed && typeof parsed === 'object' ? (parsed as ParamSchema) : {};
    } catch {
      return {};
    }
  }
  if (typeof raw !== 'object') return {};
  return raw as ParamSchema;
}

function defaultFor(prop: ParamProp): unknown {
  if (prop.default !== undefined) return prop.default;
  switch (prop.type) {
    case 'boolean':
      return false;
    case 'integer':
    case 'number':
      return 0;
    default:
      return '';
  }
}

function initValues(schema: ParamSchema): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [key, prop] of Object.entries(schema.properties ?? {})) {
    out[key] = defaultFor(prop);
  }
  return out;
}

function validate(schema: ParamSchema, values: Record<string, unknown>): string | null {
  for (const key of schema.required ?? []) {
    const v = values[key];
    if (v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)) {
      return `缺少必填参数：${key}`;
    }
  }
  return null;
}

/* ---------------------------------- page ---------------------------------- */

export default function CommandCenterPage() {
  const session = useSession();
  const navigate = useNavigate();
  const confirm = useConfirm();
  const isPrimary = !session || session.role === 'primary';
  const isProd = session?.environment === 'production';

  const [nodeFilter, setNodeFilter] = useState<string>('');
  const [categoryFilter, setCategoryFilter] = useState<string>('');
  const [permFilter, setPermFilter] = useState<string>('');
  const [keyword, setKeyword] = useState('');

  const nodesState = useApi<unknown>(isPrimary ? '/nodes' : null);
  const nodes = useMemo(() => unwrapList<NodeInfo>(nodesState.data, ['nodes']), [nodesState.data]);

  // Child nodes: only see their own commands; the API is already scoped server-side.
  const query = useMemo(() => {
    const q: Record<string, string> = {};
    const targetNode = isPrimary ? nodeFilter : (session?.nodeId ?? '');
    if (targetNode) q.node_id = targetNode;
    if (categoryFilter) q.category = categoryFilter;
    if (permFilter) q.permission = permFilter;
    if (keyword.trim()) q.keyword = keyword.trim();
    return q;
  }, [isPrimary, nodeFilter, categoryFilter, permFilter, keyword, session?.nodeId]);

  const commandsState = useApi<unknown>('/commands', { query });
  const commands = useMemo(() => unwrapList<CommandInfo>(commandsState.data, ['commands']), [commandsState.data]);

  const categories = useMemo(() => Array.from(new Set(commands.map((c) => c.category).filter(Boolean))) as string[], [commands]);
  const permissions = useMemo(
    () => Array.from(new Set(commands.map((c) => c.permission_profile).filter(Boolean))) as string[],
    [commands],
  );

  const [execCmd, setExecCmd] = useState<CommandInfo | null>(null);
  const [args, setArgs] = useState<Record<string, unknown>>({});
  const [submitBusy, setSubmitBusy] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  const execSchema = useMemo(() => parseSchema(execCmd?.parameter_schema_json ?? execCmd?.parameter_schema), [execCmd]);

  const openExec = (cmd: CommandInfo) => {
    setExecCmd(cmd);
    setArgs(initValues(parseSchema(cmd.parameter_schema_json ?? cmd.parameter_schema)));
    setSubmitError(null);
  };

  const targetNodeId = execCmd?.node_id || (isPrimary ? nodeFilter : session?.nodeId) || undefined;

  const setArg = (key: string, value: unknown) => {
    setArgs((prev) => ({ ...prev, [key]: value }));
  };

  const handleSubmit = useCallback(
    async (reason?: string) => {
      if (!execCmd) return;
      setSubmitBusy(true);
      setSubmitError(null);
      try {
        const response = await api.post<{ task?: { id?: string } }>(`/nodes/${targetNodeId}/tasks`, {
          command_id: execCmd.command_id ?? execCmd.id,
          command_version: execCmd.command_version ?? execCmd.version,
          arguments: args,
          timeout_seconds: execCmd.timeout_seconds ?? undefined,
          ...(reason ? { reason } : {}),
        }, {
          headers: { 'Idempotency-Key': newIdempotencyKey() },
        });
        const taskId = response?.task?.id;
        if (taskId) navigate(`/tasks/${taskId}`);
        else navigate('/tasks');
      } catch (err) {
        setSubmitError(err instanceof ApiError ? err.message : '提交失败，请重试');
      } finally {
        setSubmitBusy(false);
      }
    },
    [execCmd, targetNodeId, args, navigate],
  );

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!execCmd) return;
    const validationError = validate(execSchema, args);
    if (validationError) {
      setSubmitError(validationError);
      return;
    }
    const profile = execCmd.permission_profile ?? '';
    const requireReason = profile === 'admin' || (isProd && profile === 'operator');
    const result = await confirm({
      title: `执行命令：${execCmd.title ?? execCmd.command_id}`,
      message: (
        <div className="kv">
          <dt>目标节点</dt>
          <dd className="mono">{execCmd.node_name || targetNodeId || '—'}</dd>
          <dt>命令版本</dt>
          <dd className="mono">{execCmd.command_version ?? execCmd.version ?? '—'}</dd>
          <dt>超时</dt>
          <dd className="mono">{execCmd.timeout_seconds ? `${execCmd.timeout_seconds}s` : '默认'}</dd>
          <dt>权限</dt>
          <dd>
            <ProfileBadge profile={profile} />
          </dd>
          <dt>参数预览</dt>
          <dd>
            <pre className="mono" style={{ background: '#f8fafc', padding: 8, borderRadius: 6, fontSize: 12.5, maxHeight: 160, overflow: 'auto' }}>
              {JSON.stringify(redact(args), null, 2)}
            </pre>
          </dd>
        </div>
      ),
      confirmLabel: '确认执行',
      danger: profile === 'admin',
      production: isProd,
      requireReason,
      reasonLabel: '执行原因',
      reasonPlaceholder: '说明执行该命令的目的（将写入审计）',
    });
    if (result.ok) await handleSubmit(result.reason);
  };

  return (
    <div>
      <PageHeader
        title={isPrimary ? '命令中心' : '本机命令'}
        subtitle={isPrimary ? '按分类 / 节点 / 权限筛选可用命令并执行。' : '仅展示本机声明并封装的命令。'}
      />

      <Card>
        <div className="filter-bar">
          {isPrimary && (
            <div className="filter-group">
              <span className="filter-label">目标节点</span>
              <Select value={nodeFilter} onChange={(e) => setNodeFilter(e.target.value)}>
                <option value="">全部节点</option>
                {nodes.map((n) => (
                  <option key={n.id ?? n.node_id} value={n.id ?? n.node_id}>
                    {nodeName(n)}（{n.status ?? '未知'}）
                  </option>
                ))}
              </Select>
            </div>
          )}
          <div className="filter-group">
            <span className="filter-label">分类</span>
            <Select value={categoryFilter} onChange={(e) => setCategoryFilter(e.target.value)}>
              <option value="">全部分类</option>
              {categories.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </Select>
          </div>
          <div className="filter-group">
            <span className="filter-label">权限</span>
            <Select value={permFilter} onChange={(e) => setPermFilter(e.target.value)}>
              <option value="">全部权限</option>
              {permissions.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </Select>
          </div>
          <div className="filter-group">
            <span className="filter-label">关键词</span>
            <TextInput
              placeholder="搜索命令…"
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
              style={{ minWidth: 180 }}
            />
          </div>
          <button className="btn btn-ghost btn-sm" onClick={commandsState.reload}>
            刷新
          </button>
        </div>

        {commandsState.loading && commandsState.data === null ? (
          <LoadingState label="加载命令中…" />
        ) : commandsState.error ? (
          <ErrorState message={errorMessage(commandsState.error)} onRetry={commandsState.reload} />
        ) : commands.length === 0 ? (
          <EmptyState
            title={keyword || nodeFilter || categoryFilter || permFilter ? '没有匹配的命令' : '暂无可用命令'}
            hint={keyword || nodeFilter || categoryFilter || permFilter ? '请调整筛选条件。' : '节点 Agent 上报命令清单后此处会显示可用命令。'}
          />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>命令</th>
                  <th>分类</th>
                  <th>节点</th>
                  <th>权限</th>
                  <th>版本</th>
                  <th>超时</th>
                  <th style={{ width: 90 }}>操作</th>
                </tr>
              </thead>
              <tbody>
                {commands.map((c, i) => {
                  const enabled = c.enabled !== false;
                  return (
                    <tr key={`${c.command_id ?? c.id}-${c.command_version}-${i}`} className={enabled ? undefined : 'is-disabled'}>
                      <td>
                        <div style={{ fontWeight: 600 }}>{c.title || c.command_id || '—'}</div>
                        {c.description && <div className="muted" style={{ fontSize: 12 }}>{c.description}</div>}
                        <div className="mono muted" style={{ fontSize: 11.5 }}>{c.command_id ?? c.id}</div>
                      </td>
                      <td>
                        <Badge tone="blue">{c.category || '—'}</Badge>
                      </td>
                      <td className="mono">{c.node_name || (c.node ? nodeName(c.node as unknown as NodeInfo) : c.node_id) || '—'}</td>
                      <td>
                        <ProfileBadge profile={c.permission_profile} />
                      </td>
                      <td className="mono">{c.command_version ?? c.version ?? '—'}</td>
                      <td className="mono">{c.timeout_seconds ? `${c.timeout_seconds}s` : '—'}</td>
                      <td>
                        {enabled ? (
                          <button className="btn btn-sm btn-primary" onClick={() => openExec(c)}>
                            执行
                          </button>
                        ) : (
                          <Badge tone="gray">停用</Badge>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {/* Execute modal with generated form */}
      <Modal
        open={execCmd !== null}
        title={`执行：${execCmd?.title ?? execCmd?.command_id ?? ''}`}
        onClose={() => setExecCmd(null)}
        width={560}
      >
        {execCmd && (
          <form onSubmit={submit}>
            {execCmd.description && <p className="muted" style={{ marginBottom: 14 }}>{execCmd.description}</p>}
            {Object.keys(execSchema.properties ?? {}).length === 0 ? (
              <p className="empty-hint">该命令不需要参数。</p>
            ) : (
              Object.entries(execSchema.properties ?? {}).map(([key, prop]) => (
                <ParamField
                  key={key}
                  name={key}
                  prop={prop}
                  required={(execSchema.required ?? []).includes(key)}
                  value={args[key]}
                  onChange={(v) => setArg(key, v)}
                />
              ))
            )}
            {submitError && <div className="alert alert-danger">{submitError}</div>}
            <div className="kv" style={{ marginTop: 12, borderTop: '1px solid var(--border)', paddingTop: 12 }}>
              <dt>目标节点</dt>
              <dd className="mono">{execCmd.node_name || targetNodeId || '—'}</dd>
              <dt>命令版本</dt>
              <dd className="mono">{execCmd.command_version ?? execCmd.version ?? '—'}</dd>
              <dt>超时</dt>
              <dd className="mono">{execCmd.timeout_seconds ? `${execCmd.timeout_seconds}s` : '默认'}</dd>
              <dt>权限</dt>
              <dd>
                <ProfileBadge profile={execCmd.permission_profile} />
              </dd>
            </div>
            <div className="modal-actions">
              <button type="button" className="btn btn-ghost" onClick={() => setExecCmd(null)} disabled={submitBusy}>
                取消
              </button>
              <button type="submit" className="btn btn-primary" disabled={submitBusy}>
                {submitBusy ? '提交中…' : '预览并提交'}
              </button>
            </div>
          </form>
        )}
      </Modal>
    </div>
  );
}

function ParamField({
  name,
  prop,
  required,
  value,
  onChange,
}: {
  name: string;
  prop: ParamProp;
  required: boolean;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const label = prop.title || name;
  const sensitive = isSensitiveKey(name);
  return (
    <label className="field">
      <span className="field-label">
        {label} {required && <em className="req">*</em>}
        <span className="mono muted" style={{ fontWeight: 400, marginLeft: 6 }}>{name}</span>
        {sensitive && <Badge tone="amber">敏感</Badge>}
      </span>
      {prop.type === 'boolean' ? (
        <div className="checkbox-row">
          <input type="checkbox" checked={Boolean(value)} onChange={(e) => onChange(e.target.checked)} />
          <span>{value ? '是' : '否'}</span>
        </div>
      ) : prop.enum && prop.enum.length > 0 ? (
        <select className="input" value={String(value ?? '')} onChange={(e) => onChange(e.target.value)}>
          {!required && <option value="">（未选择）</option>}
          {prop.enum.map((opt) => (
            <option key={String(opt)} value={String(opt)}>
              {String(opt)}
            </option>
          ))}
        </select>
      ) : prop.type === 'integer' || prop.type === 'number' ? (
        <input
          className="input"
          type="number"
          step={prop.type === 'integer' ? 1 : 'any'}
          min={prop.minimum}
          max={prop.maximum}
          value={value === null || value === undefined || value === '' ? '' : Number(value)}
          onChange={(e) => onChange(e.target.value === '' ? '' : Number(e.target.value))}
        />
      ) : prop.type === 'array' || prop.type === 'object' ? (
        <textarea
          className="input"
          rows={3}
          value={value && typeof value === 'string' ? value : JSON.stringify(value ?? '', null, 2)}
          placeholder={prop.type === 'array' ? '["item1", "item2"]' : '{"key": "value"}'}
          onChange={(e) => {
            const raw = e.target.value;
            try {
              onChange(JSON.parse(raw));
            } catch {
              onChange(raw);
            }
          }}
        />
      ) : (
        <input
          className="input"
          type="text"
          value={String(value ?? '')}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
      {prop.description && <span className="field-hint">{prop.description}</span>}
    </label>
  );
}
