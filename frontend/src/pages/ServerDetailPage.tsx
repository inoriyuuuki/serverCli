import { useMemo, useState, type FormEvent } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useSession } from '../auth/AuthContext';
import { useApi, errorMessage } from '../lib/useApi';
import { api, unwrapList, ApiError } from '../api/client';
import type { AiLease, AuditEvent, CommandInfo, NodeInfo, TaskInfo } from '../api/types';
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
  RiskBadge,
  StatusBadge,
  Tabs,
  TextInput,
  TimeCell,
} from '../components/ui';
import { NodeInfoFields, nodeName } from '../components/NodeInfo';
import { shortId } from '../lib/format';

type TabKey = 'info' | 'addresses' | 'commands' | 'tasks' | 'leases' | 'audit' | 'settings';

export default function ServerDetailPage() {
  const { nodeId } = useParams<{ nodeId: string }>();
  const session = useSession();
  const isProd = session?.environment === 'production';
  const [tab, setTab] = useState<TabKey>('info');
  const [editOpen, setEditOpen] = useState(false);
  const [editAlias, setEditAlias] = useState('');
  const [editLabels, setEditLabels] = useState('');
  const [editEnabled, setEditEnabled] = useState(true);
  const [editBusy, setEditBusy] = useState(false);
  const [editError, setEditError] = useState<string | null>(null);

  const nodeState = useApi<unknown>(nodeId ? `/nodes/${nodeId}` : null);
  const node = useMemo(() => {
    const data = nodeState.data;
    if (data && typeof data === 'object' && !Array.isArray(data)) {
      const d = data as Record<string, unknown>;
      if (d.node && typeof d.node === 'object') return d.node as unknown as NodeInfo;
      return data as unknown as NodeInfo;
    }
    return null;
  }, [nodeState.data]);

  const commandsState = useApi<unknown>(nodeId ? `/nodes/${nodeId}/commands` : null);
  const tasksState = useApi<unknown>('/tasks', { query: nodeId ? { node_id: nodeId, limit: 50 } : undefined });
  const leasesState = useApi<unknown>('/ai/leases', { query: nodeId ? { node_id: nodeId, limit: 50 } : undefined });
  const auditState = useApi<unknown>('/audit-events', { query: nodeId ? { node_id: nodeId, limit: 50 } : undefined });

  const commands = useMemo(() => unwrapList<CommandInfo>(commandsState.data, ['commands']), [commandsState.data]);
  const tasks = useMemo(() => unwrapList<TaskInfo>(tasksState.data, ['tasks']), [tasksState.data]);
  const leases = useMemo(() => unwrapList<AiLease>(leasesState.data, ['leases']), [leasesState.data]);
  const audits = useMemo(() => unwrapList<AuditEvent>(auditState.data, ['events']), [auditState.data]);

  const openEdit = () => {
    if (!node) return;
    setEditAlias(node.alias ?? '');
    setEditLabels(
      Array.isArray(node.labels_json)
        ? (node.labels_json as string[]).join(', ')
        : node.labels_json && typeof node.labels_json === 'object'
          ? Object.entries(node.labels_json as Record<string, unknown>)
              .map(([k, v]) => `${k}=${v}`)
              .join(', ')
          : '',
    );
    setEditEnabled(node.enabled !== false);
    setEditError(null);
    setEditOpen(true);
  };

  const submitEdit = async (e: FormEvent) => {
    e.preventDefault();
    if (!node) return;
    setEditBusy(true);
    setEditError(null);
    const labels = editLabels
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    try {
      await api.patch(`/nodes/${node.id ?? node.node_id}`, {
        alias: editAlias.trim() || null,
        labels: labels.length ? labels : [],
        enabled: editEnabled,
      });
      setEditOpen(false);
      nodeState.reload();
    } catch (err) {
      setEditError(err instanceof ApiError ? err.message : '保存失败，请重试');
    } finally {
      setEditBusy(false);
    }
  };

  if (nodeState.loading && !node) {
    return (
      <div>
        <PageHeader title="节点详情" />
        <LoadingState label="加载节点详情…" />
      </div>
    );
  }
  if (nodeState.error) {
    return (
      <div>
        <PageHeader title="节点详情" />
        <ErrorState message={errorMessage(nodeState.error)} onRetry={nodeState.reload} />
      </div>
    );
  }
  if (!node) {
    return (
      <div>
        <PageHeader title="节点详情" />
        <EmptyState title="未找到节点" hint="节点可能已被删除或无权限访问。" />
      </div>
    );
  }

  const tabs: Array<{ key: TabKey; label: string }> = [
    { key: 'info', label: '基础信息' },
    { key: 'addresses', label: '地址' },
    { key: 'commands', label: `命令（${commands.length}）` },
    { key: 'tasks', label: `任务（${tasks.length}）` },
    { key: 'leases', label: `Lease（${leases.length}）` },
    { key: 'audit', label: `审计（${audits.length}）` },
    { key: 'settings', label: '设置' },
  ];

  return (
    <div>
      <PageHeader
        title={nodeName(node)}
        subtitle={
          <Link to="/servers" className="muted">
            ← 返回服务器列表
          </Link>
        }
        actions={
          <div className="btn-row">
            <Badge tone={node.role === 'primary' ? 'indigo' : 'teal'}>{node.role === 'primary' ? '主节点' : '子节点'}</Badge>
            <StatusBadge status={node.status} />
            {node.enabled === false && <Badge tone="gray">已禁用</Badge>}
            <button className="btn btn-ghost btn-sm" onClick={openEdit}>
              编辑
            </button>
          </div>
        }
      />

      <Tabs tabs={tabs} active={tab} onChange={(k) => setTab(k as TabKey)} />

      {tab === 'info' && (
        <Card title="基础信息" actions={<button className="btn btn-ghost btn-sm" onClick={openEdit}>编辑别名/标签</button>}>
          <NodeInfoFields node={node} />
        </Card>
      )}

      {tab === 'addresses' && (
        <Card title="地址记录">
          {!node.addresses?.length ? (
            <EmptyState title="暂无地址记录" />
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>地址</th>
                    <th>类型</th>
                    <th>服务端口</th>
                    <th>首选</th>
                    <th>最近发现</th>
                  </tr>
                </thead>
                <tbody>
                  {node.addresses.map((a, i) => (
                    <tr key={i}>
                      <td className="mono">{a.address || '—'}</td>
                      <td>{a.address_type || '—'}</td>
                      <td className="mono">{a.service_port ?? '—'}</td>
                      <td>{a.is_preferred ? '是' : '否'}</td>
                      <td>
                        <TimeCell value={(a as { last_seen_at?: string }).last_seen_at} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}

      {tab === 'commands' && (
        <Card title="命令清单" actions={<button className="btn btn-ghost btn-sm" onClick={commandsState.reload}>刷新</button>}>
          {commandsState.loading && commandsState.data === null ? (
            <LoadingState label="加载命令中…" />
          ) : commandsState.error ? (
            <ErrorState message={errorMessage(commandsState.error)} onRetry={commandsState.reload} />
          ) : commands.length === 0 ? (
            <EmptyState title="该节点尚未上报命令" hint="Agent 启动或变更后会同步命令清单。" />
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>命令 ID</th>
                    <th>标题</th>
                    <th>分类</th>
                    <th>权限</th>
                    <th>版本</th>
                    <th>超时</th>
                    <th>启用</th>
                  </tr>
                </thead>
                <tbody>
                  {commands.map((c, i) => (
                    <tr key={`${c.command_id ?? c.id}-${c.command_version}-${i}`}>
                      <td className="mono-cell">{c.command_id ?? c.id ?? '—'}</td>
                      <td>
                        {c.title || '—'}
                        {c.description && <div className="muted" style={{ fontSize: 12 }}>{c.description}</div>}
                      </td>
                      <td>{c.category || '—'}</td>
                      <td>
                        <ProfileBadge profile={c.permission_profile} />
                      </td>
                      <td className="mono">{c.command_version ?? c.version ?? '—'}</td>
                      <td className="mono">{c.timeout_seconds ? `${c.timeout_seconds}s` : '—'}</td>
                      <td>
                        <Badge tone={c.enabled === false ? 'gray' : 'green'}>{c.enabled === false ? '停用' : '启用'}</Badge>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}

      {tab === 'tasks' && (
        <TaskTable tasks={tasks} state={tasksState} />
      )}

      {tab === 'leases' && (
        <LeaseTable leases={leases} state={leasesState} />
      )}

      {tab === 'audit' && (
        <Card title="审计事件">
          {auditState.loading && auditState.data === null ? (
            <LoadingState label="加载审计中…" />
          ) : auditState.error ? (
            <ErrorState message={errorMessage(auditState.error)} onRetry={auditState.reload} />
          ) : audits.length === 0 ? (
            <EmptyState title="暂无审计事件" />
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>时间</th>
                    <th>操作者</th>
                    <th>动作</th>
                    <th>资源</th>
                    <th>风险</th>
                    <th>结果</th>
                    <th>摘要</th>
                  </tr>
                </thead>
                <tbody>
                  {audits.map((a) => (
                    <tr key={a.id}>
                      <td>
                        <TimeCell value={a.occurred_at} />
                      </td>
                      <td>
                        {a.actor_type} {a.actor_id && <span className="muted mono">({shortId(a.actor_id)})</span>}
                      </td>
                      <td>{a.action || '—'}</td>
                      <td>
                        {a.resource_type}
                        {a.resource_id && <span className="muted mono">/{shortId(a.resource_id)}</span>}
                      </td>
                      <td>
                        <RiskBadge risk={a.risk_level} />
                      </td>
                      <td>
                        <StatusBadge status={a.result} />
                      </td>
                      <td className="muted">{a.summary || '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}

      {tab === 'settings' && (
        <Card title="节点设置">
          <div className="kv kv-2col">
            <dt>别名</dt>
            <dd>{node.alias || '—'}</dd>
            <dt>标签</dt>
            <dd>
              {Array.isArray(node.labels_json)
                ? node.labels_json.map((l) => (
                    <span key={String(l)} className="tag">
                      {String(l)}
                    </span>
                  ))
                : node.labels_json && typeof node.labels_json === 'object'
                  ? Object.entries(node.labels_json as Record<string, unknown>).map(([k, v]) => (
                      <span key={k} className="tag">
                        {k}={String(v)}
                      </span>
                    ))
                  : '—'}
            </dd>
            <dt>启用状态</dt>
            <dd>
              <Badge tone={node.enabled === false ? 'gray' : 'green'}>{node.enabled === false ? '已禁用' : '已启用'}</Badge>
            </dd>
            <dt>前端端口</dt>
            <dd className="mono">{node.frontend_port ?? '—'}</dd>
            <dt>后端端口</dt>
            <dd className="mono">{node.backend_port ?? '—'}</dd>
            <dt>凭证版本</dt>
            <dd className="mono">{String((node as { credential_version?: unknown }).credential_version ?? '—')}</dd>
            <dt>创建时间</dt>
            <dd>
              <TimeCell value={node.created_at} />
            </dd>
            <dt>更新时间</dt>
            <dd>
              <TimeCell value={node.updated_at} />
            </dd>
          </div>
          <div className="btn-row" style={{ marginTop: 16 }}>
            <button className="btn" onClick={openEdit}>
              编辑别名 / 标签 / 启用状态
            </button>
          </div>
        </Card>
      )}

      <Modal open={editOpen} title={`编辑节点：${nodeName(node)}`} onClose={() => setEditOpen(false)} width={480}>
        <form onSubmit={submitEdit}>
          <label className="field">
            <span className="field-label">别名</span>
            <TextInput value={editAlias} onChange={(e) => setEditAlias(e.target.value)} placeholder="显示名称" />
          </label>
          <label className="field">
            <span className="field-label">标签</span>
            <TextInput value={editLabels} onChange={(e) => setEditLabels(e.target.value)} placeholder="逗号分隔，如 env=prod,team=ops" />
          </label>
          <Checkbox label="启用该节点" checked={editEnabled} onChange={setEditEnabled} />
          {isProd && (
            <div className="alert alert-danger" role="alert">
              ⚠️ <strong>正式环境</strong>：修改节点设置将写入正式环境。
            </div>
          )}
          {editError && <div className="alert alert-danger">{editError}</div>}
          <div className="modal-actions">
            <button type="button" className="btn btn-ghost" onClick={() => setEditOpen(false)} disabled={editBusy}>
              取消
            </button>
            <button type="submit" className="btn btn-primary" disabled={editBusy}>
              {editBusy ? '保存中…' : '保存'}
            </button>
          </div>
        </form>
      </Modal>
    </div>
  );
}

function TaskTable({ tasks, state }: { tasks: TaskInfo[]; state: { loading: boolean; error: ApiError | null; reload: () => void; data: unknown } }) {
  const navigate = useNavigate();
  if (state.loading && state.data === null) return <Card title="任务"><LoadingState label="加载任务中…" /></Card>;
  if (state.error) return <Card title="任务"><ErrorState message={errorMessage(state.error)} onRetry={state.reload} /></Card>;
  if (tasks.length === 0) return <Card title="任务"><EmptyState title="暂无任务" /></Card>;
  return (
    <Card title="任务">
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>ID</th>
              <th>命令</th>
              <th>发起者</th>
              <th>状态</th>
              <th>创建时间</th>
            </tr>
          </thead>
          <tbody>
            {tasks.map((t) => (
              <tr key={t.id} className="clickable" onClick={() => navigate(`/tasks/${t.id}`)}>
                <td className="mono-cell">{shortId(t.id)}</td>
                <td>{t.command_id || '—'}</td>
                <td>{t.requested_by || '—'}</td>
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
    </Card>
  );
}

function LeaseTable({ leases, state }: { leases: AiLease[]; state: { loading: boolean; error: ApiError | null; reload: () => void; data: unknown } }) {
  if (state.loading && state.data === null) return <Card title="Lease"><LoadingState label="加载 Lease 中…" /></Card>;
  if (state.error) return <Card title="Lease"><ErrorState message={errorMessage(state.error)} onRetry={state.reload} /></Card>;
  if (leases.length === 0) return <Card title="Lease"><EmptyState title="暂无 Lease" /></Card>;
  return (
    <Card title="Lease">
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Lease</th>
              <th>AI</th>
              <th>权限</th>
              <th>状态</th>
              <th>到期</th>
              <th>会话</th>
            </tr>
          </thead>
          <tbody>
            {leases.map((l) => (
              <tr key={l.id}>
                <td className="mono-cell">{shortId(l.id)}</td>
                <td>{l.ai_agent_name || l.ai_agent_id || '—'}</td>
                <td>
                  <ProfileBadge profile={l.permission_profile} />
                </td>
                <td>
                  <StatusBadge status={l.status} />
                </td>
                <td>
                  <TimeCell value={l.expires_at} />
                </td>
                <td className="num">{l.active_session_count ?? 0}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}
