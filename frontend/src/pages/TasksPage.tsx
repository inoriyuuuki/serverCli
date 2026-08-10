import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useSession } from '../auth/AuthContext';
import { useApi, errorMessage } from '../lib/useApi';
import { useRealtime } from '../lib/realtime';
import { unwrapList } from '../api/client';
import type { NodeInfo, TaskInfo } from '../api/types';
import {
  Card,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
  Select,
  StatusBadge,
  TextInput,
  TimeCell,
} from '../components/ui';
import { nodeName } from '../components/NodeInfo';
import { durationBetween, shortId } from '../lib/format';

const STATUS_OPTIONS = [
  ['queued', '排队中'],
  ['dispatched', '已下发'],
  ['running', '运行中'],
  ['succeeded', '成功'],
  ['failed', '失败'],
  ['timed_out', '超时'],
  ['cancelled', '已取消'],
  ['node_unreachable', '服务器失联'],
  ['result_unknown', '结果未知'],
];

export default function TasksPage() {
  const session = useSession();
  const navigate = useNavigate();
  const isPrimary = !session || session.role === 'primary';

  const [statusFilter, setStatusFilter] = useState('');
  const [nodeFilter, setNodeFilter] = useState('');
  const [search, setSearch] = useState('');

  const nodesState = useApi<unknown>(isPrimary ? '/nodes' : null);
  const nodes = useMemo(() => unwrapList<NodeInfo>(nodesState.data, ['nodes']), [nodesState.data]);

  const query = useMemo(() => {
    const q: Record<string, string> = {};
    if (statusFilter) q.status = statusFilter;
    if (nodeFilter) q.node_id = nodeFilter;
    if (search.trim()) q.q = search.trim();
    return q;
  }, [statusFilter, nodeFilter, search]);

  const tasksState = useApi<unknown>('/tasks', { query, pollIntervalMs: 30000 });
  // 任务创建/事件/结果由 WebSocket 实时推送，轮询仅作兜底。
  useRealtime(['tasks_changed'], () => tasksState.reload());
  const tasks = useMemo(() => unwrapList<TaskInfo>(tasksState.data, ['tasks']), [tasksState.data]);

  return (
    <div>
      <PageHeader title={isPrimary ? '任务' : '本机任务'} subtitle={isPrimary ? '查看集群任务执行记录。' : '仅展示本机任务。'} />

      <Card>
        <div className="filter-bar">
          <div className="filter-group">
            <span className="filter-label">状态</span>
            <Select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
              <option value="">全部状态</option>
              {STATUS_OPTIONS.map(([v, l]) => (
                <option key={v} value={v}>
                  {l}
                </option>
              ))}
            </Select>
          </div>
          {isPrimary && (
            <div className="filter-group">
              <span className="filter-label">服务器</span>
              <Select value={nodeFilter} onChange={(e) => setNodeFilter(e.target.value)}>
                <option value="">全部服务器</option>
                {nodes.map((n) => (
                  <option key={n.id ?? n.node_id} value={n.id ?? n.node_id}>
                    {nodeName(n)}
                  </option>
                ))}
              </Select>
            </div>
          )}
          <div className="filter-group">
            <span className="filter-label">搜索</span>
            <TextInput placeholder="任务 ID / 命令 / 发起者…" value={search} onChange={(e) => setSearch(e.target.value)} style={{ minWidth: 200 }} />
          </div>
          <button className="btn btn-ghost btn-sm" onClick={tasksState.reload}>
            刷新
          </button>
        </div>

        {tasksState.loading && tasksState.data === null ? (
          <LoadingState label="加载任务中…" />
        ) : tasksState.error ? (
          <ErrorState message={errorMessage(tasksState.error)} onRetry={tasksState.reload} />
        ) : tasks.length === 0 ? (
          <EmptyState
            title={statusFilter || nodeFilter || search ? '没有匹配的任务' : '暂无任务'}
            hint={statusFilter || nodeFilter || search ? '请调整筛选条件。' : '前往命令中心执行命令后此处会显示任务。'}
          />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>ID</th>
                  <th>服务器</th>
                  <th>命令</th>
                  <th>发起者</th>
                  <th>状态</th>
                  <th>耗时</th>
                  <th>创建时间</th>
                </tr>
              </thead>
              <tbody>
                {tasks.map((t) => (
                  <tr key={t.id} className="clickable" onClick={() => navigate(`/tasks/${t.id}`)}>
                    <td className="mono-cell" title={t.id}>{shortId(t.id)}</td>
                    <td>{t.node_name || t.node_id || '—'}</td>
                    <td>
                      {t.command_id || '—'}
                      {t.command_version && <span className="muted mono" style={{ fontSize: 12 }}> v{t.command_version}</span>}
                    </td>
                    <td>{t.requested_by || '—'}</td>
                    <td>
                      <StatusBadge status={t.status} />
                    </td>
                    <td className="mono">{durationBetween(t.started_at ?? t.queued_at ?? t.created_at, t.finished_at)}</td>
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
    </div>
  );
}
