import { useCallback, useMemo, useRef, useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useSession } from '../auth/AuthContext';
import { useApi, errorMessage } from '../lib/useApi';
import { api, unwrapList, ApiError } from '../api/client';
import type { CommandInfo, NodeInfo, TaskParameterHistory } from '../api/types';
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
  TimeCell,
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
  for (const [key, prop] of Object.entries(schema.properties ?? {})) {
    const v = values[key];
    if (v === undefined || v === null || v === '') continue;
    if (prop.type === 'integer' && typeof v === 'number' && !Number.isInteger(v)) {
      return `参数 ${key} 必须是整数`;
    }
    if (prop.type === 'number' && typeof v !== 'number') return `参数 ${key} 必须是数字`;
    if (prop.type === 'string' && typeof v !== 'string') return `参数 ${key} 必须是字符串`;
    if (prop.type === 'boolean' && typeof v !== 'boolean') return `参数 ${key} 必须是布尔值`;
    if (prop.enum && prop.enum.length > 0 && !prop.enum.includes(v)) {
      return `参数 ${key} 取值不在允许范围内`;
    }
    if (typeof v === 'number') {
      if (prop.minimum !== undefined && v < prop.minimum) return `参数 ${key} 不能小于 ${prop.minimum}`;
      if (prop.maximum !== undefined && v > prop.maximum) return `参数 ${key} 不能大于 ${prop.maximum}`;
    }
  }
  return null;
}

/** Recursively serialize a JSON value with sorted object keys (stable signature). */
function canonicalJSON(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(',')}]`;
  if (value && typeof value === 'object') {
    const obj = value as Record<string, unknown>;
    return `{${Object.keys(obj)
      .sort()
      .map((k) => `${JSON.stringify(k)}:${canonicalJSON(obj[k])}`)
      .join(',')}}`;
  }
  return JSON.stringify(value);
}

/** Normalize a parameter schema (raw string or object) to a stable signature. */
function schemaSignature(raw: unknown): string {
  if (typeof raw === 'string') {
    try {
      return canonicalJSON(JSON.parse(raw));
    } catch {
      return raw;
    }
  }
  return canonicalJSON(raw ?? null);
}

/* --------------------------- execution group --------------------------- */

interface ExecGroup {
  key: string;
  commandId: string;
  version: string;
  title?: string;
  description?: string | null;
  category?: string;
  permissionProfile?: string;
  timeoutSeconds?: number | null;
  schema: ParamSchema;
  /** node ids (from command records) that share this compatible definition. */
  nodeIds: string[];
}

function groupKey(c: CommandInfo): string {
  const cid = c.command_id ?? c.id ?? '';
  const ver = c.command_version ?? c.version ?? '';
  const sig = JSON.stringify([
    schemaSignature(c.parameter_schema_json ?? c.parameter_schema),
    c.permission_profile ?? '',
    c.timeout_seconds ?? 0,
  ]);
  return `${cid}\u0000${ver}\u0000${sig}`;
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

  // Merge identical command definitions (command_id + version + compatible
  // schema/permission/timeout) into a single row for multi-node execution.
  const groups = useMemo<ExecGroup[]>(() => {
    const map = new Map<string, ExecGroup>();
    for (const c of commands) {
      const cid = c.command_id ?? c.id ?? '';
      const ver = c.command_version ?? c.version ?? '';
      if (!cid) continue;
      const key = groupKey(c);
      const nodeId = c.node_id ?? '';
      let g = map.get(key);
      if (!g) {
        g = {
          key,
          commandId: cid,
          version: ver,
          title: c.title,
          description: c.description,
          category: c.category,
          permissionProfile: c.permission_profile,
          timeoutSeconds: c.timeout_seconds,
          schema: parseSchema(c.parameter_schema_json ?? c.parameter_schema),
          nodeIds: [],
        };
        map.set(key, g);
      }
      if (nodeId && !g.nodeIds.includes(nodeId)) g.nodeIds.push(nodeId);
    }
    return Array.from(map.values());
  }, [commands]);

  const categories = useMemo(() => Array.from(new Set(groups.map((c) => c.category).filter(Boolean))) as string[], [groups]);
  const permissions = useMemo(
    () => Array.from(new Set(groups.map((c) => c.permissionProfile).filter(Boolean))) as string[],
    [groups],
  );

  // Child control planes have no cluster node list; their only eligible
  // target is the node they are running on. The session response does not
  // carry node_id, so derive it from the child's own command records (the
  // proxied /commands response is scoped to the calling node).
  const selfNode: NodeInfo | null = useMemo(() => {
    if (isPrimary) return null;
    const id = commands.find((c) => c.node_id)?.node_id ?? session?.nodeId ?? '';
    if (!id) return null;
    return {
      id,
      node_id: id,
      instance_name: session?.nodeName ?? undefined,
      status: 'online',
      enabled: true,
    };
  }, [isPrimary, commands, session]);

  // Only online + enabled nodes that actually support the group's definition
  // may be selected as execution targets.
  const eligibleNodesFor = useCallback(
    (g: ExecGroup): NodeInfo[] => {
      const pool = isPrimary ? nodes : selfNode ? [selfNode] : [];
      return pool.filter((n) => {
        const id = n.id ?? n.node_id ?? '';
        return g.nodeIds.includes(id) && n.enabled !== false && n.status !== 'offline' && n.status !== 'disabled';
      });
    },
    [isPrimary, nodes, selfNode],
  );

  const [execGroup, setExecGroup] = useState<ExecGroup | null>(null);
  const [args, setArgs] = useState<Record<string, unknown>>({});
  const [selectedNodeIds, setSelectedNodeIds] = useState<string[]>([]);
  // Per-modal idempotency keys keyed by (group, node, args): retrying the same
  // submission reuses the key (no duplicate tasks), while changing arguments
  // produces a fresh key. Cleared each time the modal opens.
  const idemKeysRef = useRef<Record<string, string>>({});
  const [submitBusy, setSubmitBusy] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [submitResults, setSubmitResults] = useState<Array<{ nodeId: string; nodeName: string; ok: boolean; taskId?: string; error?: string }> | null>(null);

  const execSchema = execGroup?.schema ?? {};
  const targetNodes = useMemo(
    () => (execGroup ? eligibleNodesFor(execGroup).filter((n) => selectedNodeIds.includes(n.id ?? n.node_id ?? '')) : []),
    [execGroup, eligibleNodesFor, selectedNodeIds],
  );

  const setArg = (key: string, value: unknown) => {
    setArgs((prev) => ({ ...prev, [key]: value }));
  };

  const openExec = (g: ExecGroup) => {
    setExecGroup(g);
    setArgs(initValues(g.schema));
    setSubmitError(null);
    setSubmitResults(null);
    idemKeysRef.current = {};
    setSelectedNodeIds(eligibleNodesFor(g).map((n) => n.id ?? n.node_id ?? '').filter(Boolean));
  };

  // Reusable parameter history for the command across its eligible nodes.
  const historyQuery = useMemo(() => {
    if (!execGroup) return undefined;
    return {
      node_id: execGroup.nodeIds,
      command_id: execGroup.commandId,
      command_version: execGroup.version,
      limit: 100,
    };
  }, [execGroup]);
  const historiesState = useApi<unknown>(execGroup ? '/task-parameter-histories' : null, { query: historyQuery });
  const allHistories = useMemo(() => unwrapList<TaskParameterHistory>(historiesState.data, ['histories']), [historiesState.data]);
  // Only render histories that belong to the currently open command group.
  // useApi keeps the previous response while a new query is in flight, so this
  // prevents stale records from one command leaking into another's form.
  const histories = useMemo(
    () =>
      execGroup
        ? allHistories.filter(
            (h) =>
              h.command_id === execGroup.commandId &&
              h.command_version === execGroup.version &&
              execGroup.nodeIds.includes(h.node_id ?? ''),
          )
        : [],
    [allHistories, execGroup],
  );

  const nodeById = useCallback(
    (id: string) => nodes.find((n) => (n.id ?? n.node_id) === id) ?? null,
    [nodes],
  );

  const useHistory = (h: TaskParameterHistory) => {
    if (!execGroup) return;
    if (h.command_id !== execGroup.commandId || h.command_version !== execGroup.version) return;
    setArgs(h.arguments ?? {});
    setSubmitError(null);
  };

  const deleteHistory = async (h: TaskParameterHistory) => {
    if (!execGroup) return;
    const result = await confirm({
      title: '删除参数历史',
      message: (
        <div className="kv">
          <dt>服务器</dt>
          <dd>{nodeName(nodeById(h.node_id ?? '')) || h.node_id || '—'}</dd>
          <dt>命令</dt>
          <dd className="mono">{h.command_id} v{h.command_version}</dd>
          <dt>参数</dt>
          <dd>
            <pre className="mono" style={{ background: '#f8fafc', padding: 8, borderRadius: 6, fontSize: 12.5, maxHeight: 160, overflow: 'auto' }}>
              {JSON.stringify(h.arguments ?? {}, null, 2)}
            </pre>
          </dd>
        </div>
      ),
      confirmLabel: '确认删除',
      danger: true,
      production: isProd,
    });
    if (!result.ok) return;
    try {
      await api.delete(`/task-parameter-histories/${h.id}`);
      historiesState.reload();
    } catch (err) {
      setSubmitError(err instanceof ApiError ? err.message : '删除参数历史失败，请重试');
    }
  };

  const handleSubmit = useCallback(
    async (reason?: string) => {
      if (!execGroup) return;
      const targets = selectedNodeIds.filter(Boolean);
      if (targets.length === 0) {
        setSubmitError('请至少选择一个目标服务器');
        return;
      }
      setSubmitBusy(true);
      setSubmitError(null);
      try {
        const outcomes = await Promise.all(
          targets.map(async (nodeId) => {
            const label = nodeName(nodeById(nodeId)) || nodeId;
            try {
              const argsKey = JSON.stringify(args);
              const idemKey =
                idemKeysRef.current[`${execGroup.key}::${nodeId}::${argsKey}`] ??
                (idemKeysRef.current[`${execGroup.key}::${nodeId}::${argsKey}`] = newIdempotencyKey());
              const response = await api.post<{ task?: { id?: string } }>(`/nodes/${nodeId}/tasks`, {
                command_id: execGroup.commandId,
                command_version: execGroup.version,
                arguments: args,
                timeout_seconds: execGroup.timeoutSeconds ?? undefined,
                ...(reason ? { reason } : {}),
              }, {
                headers: { 'Idempotency-Key': idemKey },
              });
              return { nodeId, nodeName: label, ok: true, taskId: response?.task?.id };
            } catch (err) {
              return { nodeId, nodeName: label, ok: false, error: err instanceof ApiError ? err.message : '提交失败，请重试' };
            }
          }),
        );
        if (targets.length === 1) {
          const first = outcomes[0];
          if (first.ok && first.taskId) {
            navigate(`/tasks/${first.taskId}`);
            return;
          }
          setSubmitError(first.error ?? '提交失败，请重试');
          return;
        }
        setSubmitResults(outcomes);
        historiesState.reload();
      } catch (err) {
        setSubmitError(err instanceof ApiError ? err.message : '提交失败，请重试');
      } finally {
        setSubmitBusy(false);
      }
    },
    [execGroup, selectedNodeIds, args, navigate, nodeById, historiesState.reload],
  );

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!execGroup) return;
    const validationError = validate(execSchema, args);
    if (validationError) {
      setSubmitError(validationError);
      return;
    }
    const profile = execGroup.permissionProfile ?? '';
    const requireReason = profile === 'admin' || (isProd && profile === 'operator');
    const result = await confirm({
      title: `执行命令：${execGroup.title ?? execGroup.commandId}`,
      message: (
        <div className="kv">
          <dt>目标服务器</dt>
          <dd className="mono">
            {targetNodes.length > 0 ? targetNodes.map((n) => nodeName(n)).join('、') : '未选择'}
          </dd>
          <dt>命令版本</dt>
          <dd className="mono">{execGroup.version ?? '—'}</dd>
          <dt>超时</dt>
          <dd className="mono">{execGroup.timeoutSeconds ? `${execGroup.timeoutSeconds}s` : '默认'}</dd>
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

  const toggleNode = (id: string) => {
    setSelectedNodeIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  };

  return (
    <div>
      <PageHeader
        title={isPrimary ? '命令中心' : '本机命令'}
        subtitle={isPrimary ? '按分类 / 服务器 / 权限筛选可用命令并执行；相同命令合并展示，可选择多个服务器执行。' : '仅展示本机声明并封装的命令。'}
      />

      <Card>
        <div className="filter-bar">
          {isPrimary && (
            <div className="filter-group">
              <span className="filter-label">目标服务器</span>
              <Select value={nodeFilter} onChange={(e) => setNodeFilter(e.target.value)}>
                <option value="">全部服务器</option>
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
        ) : groups.length === 0 ? (
          <EmptyState
            title={keyword || nodeFilter || categoryFilter || permFilter ? '没有匹配的命令' : '暂无可用命令'}
            hint={keyword || nodeFilter || categoryFilter || permFilter ? '请调整筛选条件。' : '服务器 Agent 上报命令清单后此处会显示可用命令。'}
          />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>命令</th>
                  <th>分类</th>
                  <th>支持服务器</th>
                  <th>权限</th>
                  <th>版本</th>
                  <th>超时</th>
                  <th style={{ width: 90 }}>操作</th>
                </tr>
              </thead>
              <tbody>
                {groups.map((g) => {
                  const eligible = eligibleNodesFor(g);
                  return (
                    <tr key={g.key}>
                      <td>
                        <div style={{ fontWeight: 600 }}>{g.title || g.commandId || '—'}</div>
                        {g.description && <div className="muted" style={{ fontSize: 12 }}>{g.description}</div>}
                        <div className="mono muted" style={{ fontSize: 11.5 }}>{g.commandId}</div>
                      </td>
                      <td>
                        <Badge tone="blue">{g.category || '—'}</Badge>
                      </td>
                      <td>
                        <Badge tone="teal">{eligible.length} 服务器</Badge>
                        <div className="muted" style={{ fontSize: 11.5, marginTop: 2 }}>
                          {eligible.length > 0 ? eligible.slice(0, 3).map((n) => nodeName(n)).join('、') + (eligible.length > 3 ? ' 等' : '') : '—'}
                        </div>
                      </td>
                      <td>
                        <ProfileBadge profile={g.permissionProfile} />
                      </td>
                      <td className="mono">{g.version || '—'}</td>
                      <td className="mono">{g.timeoutSeconds ? `${g.timeoutSeconds}s` : '默认'}</td>
                      <td>
                        {eligible.length > 0 ? (
                          <button className="btn btn-primary btn-sm" onClick={() => openExec(g)}>
                            执行
                          </button>
                        ) : (
                          <Badge tone="gray">无可用服务器</Badge>
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
        open={execGroup !== null}
        title={`执行：${execGroup?.title ?? execGroup?.commandId ?? ''}`}
        onClose={() => setExecGroup(null)}
        width={620}
      >
        {execGroup && (
          <form onSubmit={submit}>
            {execGroup.description && <p className="muted" style={{ marginBottom: 14 }}>{execGroup.description}</p>}

            <div className="kv" style={{ marginBottom: 14 }}>
              <dt>目标服务器</dt>
              <dd>
                <div className="checkbox-group">
                  {eligibleNodesFor(execGroup).map((n) => {
                    const id = n.id ?? n.node_id ?? '';
                    const checked = selectedNodeIds.includes(id);
                    return (
                      <label key={id} className="checkbox-row">
                        <input
                          type="checkbox"
                          checked={checked}
                          onChange={() => toggleNode(id)}
                        />
                        <span>
                          {nodeName(n)}
                          <span className="muted">（{n.status ?? '未知'}）</span>
                        </span>
                      </label>
                    );
                  })}
                  {eligibleNodesFor(execGroup).length === 0 && <span className="muted">没有可执行的目标服务器（服务器离线、停用或未上报该命令）。</span>}
                </div>
              </dd>
            </div>

            <div style={{ marginBottom: 16 }}>
              <div className="kv" style={{ marginBottom: 8 }}>
                <dt>历史参数</dt>
                <dd>
                  <span className="muted" style={{ fontSize: 12.5 }}>
                    展示所选命令在各目标服务器的历史参数；选择后自动填充（含敏感字段）。
                  </span>
                </dd>
              </div>
              {historiesState.loading && historiesState.data === null ? (
                <div className="muted" style={{ fontSize: 12.5 }}>加载历史参数…</div>
              ) : historiesState.error ? (
                <ErrorState message={errorMessage(historiesState.error)} onRetry={historiesState.reload} />
              ) : histories.length === 0 ? (
                <div className="muted" style={{ fontSize: 12.5 }}>暂无历史参数。</div>
              ) : (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 8, maxHeight: 260, overflow: 'auto' }}>
                  {histories.map((h) => {
                    const incompat = validate(execSchema, h.arguments ?? {});
                    return (
                      <div key={h.id} className="param-history-item">
                        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                          <Badge tone="teal">{nodeName(nodeById(h.node_id ?? '')) || h.node_id || '—'}</Badge>
                          <span className="muted" style={{ fontSize: 12 }}>
                            使用 {h.use_count ?? 1} 次 · 最近 <TimeCell value={h.last_used_at} />
                          </span>
                          {incompat && <Badge tone="amber">与当前版本不兼容</Badge>}
                        </div>
                        <pre className="mono" style={{ background: '#f8fafc', padding: 8, borderRadius: 6, fontSize: 12, maxHeight: 120, overflow: 'auto', margin: '6px 0' }}>
                          {JSON.stringify(h.arguments ?? {}, null, 2)}
                        </pre>
                        <div className="btn-row">
                          <button
                            type="button"
                            className="btn btn-ghost btn-sm"
                            disabled={Boolean(incompat)}
                            title={incompat ? incompat : undefined}
                            onClick={() => useHistory(h)}
                          >
                            使用此参数
                          </button>
                          <button type="button" className="btn btn-danger btn-sm" onClick={() => deleteHistory(h)}>
                            删除
                          </button>
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
            </div>

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
              <dt>目标服务器</dt>
              <dd className="mono">{targetNodes.length > 0 ? targetNodes.map((n) => nodeName(n)).join('、') : '未选择'}</dd>
              <dt>命令版本</dt>
              <dd className="mono">{execGroup.version ?? '—'}</dd>
              <dt>超时</dt>
              <dd className="mono">{execGroup.timeoutSeconds ? `${execGroup.timeoutSeconds}s` : '默认'}</dd>
              <dt>权限</dt>
              <dd>
                <ProfileBadge profile={execGroup.permissionProfile} />
              </dd>
            </div>
            <div className="modal-actions">
              <button type="button" className="btn btn-ghost" onClick={() => setExecGroup(null)} disabled={submitBusy}>
                取消
              </button>
              <button type="submit" className="btn btn-primary" disabled={submitBusy}>
                {submitBusy ? '提交中…' : '预览并提交'}
              </button>
            </div>
          </form>
        )}
      </Modal>

      {/* Multi-node submit result summary */}
      <Modal
        open={submitResults !== null}
        title="任务提交结果"
        onClose={() => setSubmitResults(null)}
        width={560}
      >
        {submitResults && (
          <div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 14 }}>
              {submitResults.map((r) => (
                <div key={r.nodeId} className="param-history-item">
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                    <Badge tone="teal">{r.nodeName}</Badge>
                    {r.ok ? <Badge tone="green">成功</Badge> : <Badge tone="red">失败</Badge>}
                  </div>
                  {r.ok ? (
                    <div className="muted" style={{ fontSize: 12.5, marginTop: 4 }}>
                      {r.taskId ? (
                        <>
                          已创建任务 <Link to={`/tasks/${r.taskId}`} className="mono">{r.taskId.slice(0, 8)}</Link>
                        </>
                      ) : (
                        '已创建任务'
                      )}
                    </div>
                  ) : (
                    <div className="text-danger" style={{ fontSize: 12.5, marginTop: 4 }}>{r.error ?? '提交失败'}</div>
                  )}
                </div>
              ))}
            </div>
            <div className="modal-actions">
              <button type="button" className="btn btn-ghost" onClick={() => setSubmitResults(null)}>
                关闭
              </button>
              <button type="button" className="btn btn-primary" onClick={() => navigate('/tasks')}>
                查看任务列表
              </button>
            </div>
          </div>
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
