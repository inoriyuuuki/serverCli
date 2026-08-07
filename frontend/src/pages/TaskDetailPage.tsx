import { useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useSession } from '../auth/AuthContext';
import { useApi, errorMessage } from '../lib/useApi';
import { api, unwrapObject, ApiError } from '../api/client';
import type { TaskEvent, TaskInfo } from '../api/types';
import {
  Badge,
  Card,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
  StatusBadge,
  TimeCell,
  useConfirm,
} from '../components/ui';
import { cn, durationBetween, jsonSummary, redact, shortId } from '../lib/format';

function eventTone(type?: string): string {
  const t = type ?? '';
  if (['failed', 'timed_out', 'cancelled'].includes(t)) return 'tl-error';
  if (['progress'].includes(t)) return 'tl-warn';
  return '';
}

export default function TaskDetailPage() {
  const { taskId } = useParams<{ taskId: string }>();
  const navigate = useNavigate();
  const session = useSession();
  const confirm = useConfirm();
  const isProd = session?.environment === 'production';

  const state = useApi<unknown>(taskId ? `/tasks/${taskId}` : null);
  const task = useMemo(() => unwrapObject<TaskInfo>(state.data, ['task']), [state.data]);
  const [cancelBusy, setCancelBusy] = useState(false);
  const [cancelError, setCancelError] = useState<string | null>(null);

  const events = useMemo<TaskEvent[]>(() => {
    if (Array.isArray(task?.events)) return task.events;
    if (task && typeof task === 'object' && Array.isArray((task as Record<string, unknown>).task_events)) {
      return (task as Record<string, unknown>).task_events as TaskEvent[];
    }
    return [];
  }, [task]);

  const output = useMemo(() => {
    const t = task as TaskInfo | null;
    const o = t?.output;
    if (o) return o;
    if (t?.result && (t.result.stdout_text !== undefined || t.result.stderr_text !== undefined)) {
      return {
        stdout_text: t.result.stdout_text,
        stderr_text: t.result.stderr_text,
        truncated: t.result.truncated,
        redaction_count: 0,
      };
    }
    return null;
  }, [task]);

  const canCancel = task && ['queued', 'dispatched', 'running'].includes(task.status ?? '');

  const handleCancel = async () => {
    if (!task) return;
    const result = await confirm({
      title: '取消任务',
      message: `确认取消任务 ${shortId(task.id)}？已取消的任务不可恢复。`,
      danger: true,
      production: isProd,
      requireReason: isProd,
      reasonLabel: '取消原因',
    });
    if (!result.ok) return;
    setCancelBusy(true);
    setCancelError(null);
    try {
      await api.post(`/tasks/${task.id}/cancel`, { reason: result.reason });
      state.reload();
    } catch (err) {
      setCancelError(err instanceof ApiError ? err.message : '取消失败，请重试');
    } finally {
      setCancelBusy(false);
    }
  };

  const copyText = (text: string | null | undefined) => {
    if (!text) return;
    void navigator.clipboard?.writeText(text);
  };

  if (state.loading && !task) {
    return (
      <div>
        <PageHeader title="任务详情" />
        <LoadingState label="加载任务详情…" />
      </div>
    );
  }
  if (state.error) {
    return (
      <div>
        <PageHeader title="任务详情" />
        <ErrorState message={errorMessage(state.error)} onRetry={state.reload} />
      </div>
    );
  }
  if (!task) {
    return (
      <div>
        <PageHeader title="任务详情" />
        <EmptyState title="未找到任务" />
      </div>
    );
  }

  const duration = durationBetween(task.started_at ?? task.queued_at ?? task.created_at, task.finished_at);

  return (
    <div>
      <PageHeader
        title={`任务 ${shortId(task.id, 12)}`}
        subtitle={<Link to="/tasks" className="muted">← 返回任务列表</Link>}
        actions={
          <div className="btn-row">
            <StatusBadge status={task.status} />
            {task.is_protected && <Badge tone="indigo">重要（受保护）</Badge>}
            {canCancel && (
              <button className="btn btn-danger btn-sm" onClick={handleCancel} disabled={cancelBusy}>
                {cancelBusy ? '取消中…' : '取消任务'}
              </button>
            )}
          </div>
        }
      />

      {cancelError && <div className="alert alert-danger">{cancelError}</div>}

      <Card title="基本信息">
        <dl className="kv kv-2col">
          <dt>任务 ID</dt>
          <dd className="mono">{task.id}</dd>
          <dt>节点</dt>
          <dd>{task.node_name || task.node_id || '—'}</dd>
          <dt>命令</dt>
          <dd className="mono">
            {task.command_id || '—'}
            {task.command_version && <span className="muted"> v{task.command_version}</span>}
          </dd>
          <dt>发起者</dt>
          <dd>{task.requested_by || '—'}</dd>
          <dt>创建时间</dt>
          <dd><TimeCell value={task.created_at ?? task.queued_at} /></dd>
          <dt>开始时间</dt>
          <dd><TimeCell value={task.started_at} fallback="未开始" /></dd>
          <dt>结束时间</dt>
          <dd><TimeCell value={task.finished_at} fallback="未结束" /></dd>
          <dt>耗时</dt>
          <dd className="mono">{duration}</dd>
          <dt>超时</dt>
          <dd className="mono">{task.timeout_seconds ? `${task.timeout_seconds}s` : '—'}</dd>
          {task.exit_code !== null && task.exit_code !== undefined && (
            <>
              <dt>退出码</dt>
              <dd className="mono">
                <span className={task.exit_code === 0 ? 'text-success' : 'text-danger'}>{task.exit_code}</span>
              </dd>
            </>
          )}
          {(task.error_code || task.error_message) && (
            <>
              <dt>错误码</dt>
              <dd className="mono text-danger">{task.error_code || '—'}</dd>
              <dt>错误信息</dt>
              <dd className="text-danger">{task.error_message || '—'}</dd>
            </>
          )}
        </dl>
      </Card>

      <div className="grid grid-2">
        <Card title="参数摘要（已脱敏）">
          {task.arguments_json === undefined && task.arguments === undefined ? (
            <EmptyState title="无参数" />
          ) : (
            <pre className="mono" style={{ background: '#f8fafc', padding: 12, borderRadius: 8, fontSize: 12.5, overflow: 'auto' }}>
              {jsonSummary(redact(task.arguments_json ?? task.arguments))}
            </pre>
          )}
        </Card>

        <Card title="时间线（任务事件）">
          {events.length === 0 ? (
            <EmptyState title="暂无事件" hint="任务事件由 Agent 上报。" />
          ) : (
            <div className="timeline">
              {[...events]
                .sort((a, b) => (a.sequence ?? 0) - (b.sequence ?? 0))
                .map((ev, i) => (
                  <div key={ev.id ?? i} className={cn('tl-item', eventTone(ev.event_type))}>
                    <div className="tl-time">
                      <TimeCell value={ev.occurred_at} />
                      {ev.sequence !== undefined && <span className="muted"> seq {ev.sequence}</span>}
                    </div>
                    <div className="tl-title">
                      {ev.event_type || 'event'}
                      {ev.source && <span className="muted" style={{ fontWeight: 400 }}> · {ev.source}</span>}
                    </div>
                    {ev.message && <div className="muted" style={{ fontSize: 12.5 }}>{ev.message}</div>}
                  </div>
                ))}
            </div>
          )}
        </Card>
      </div>

      <Card
        title="输出"
        actions={
          output?.truncated ? (
            <Badge tone="amber">输出已截断</Badge>
          ) : output?.redaction_count ? (
            <Badge tone="amber">已脱敏 {output.redaction_count} 处</Badge>
          ) : undefined
        }
      >
        <div className="grid grid-2" style={{ gap: 14 }}>
          <div>
            <div className="output-actions">
              <strong>stdout</strong>
              {output?.stdout_text && (
                <button className="btn btn-ghost btn-sm" onClick={() => copyText(output.stdout_text)}>
                  复制
                </button>
              )}
            </div>
            <pre className={cn('output-block', !output?.stdout_text && 'is-empty')}>
              {output?.stdout_text || '（无标准输出）'}
            </pre>
          </div>
          <div>
            <div className="output-actions">
              <strong>stderr</strong>
              {output?.stderr_text && (
                <button className="btn btn-ghost btn-sm" onClick={() => copyText(output.stderr_text)}>
                  复制
                </button>
              )}
            </div>
            <pre className={cn('output-block', !output?.stderr_text && 'is-empty')}>
              {output?.stderr_text || '（无错误输出）'}
            </pre>
          </div>
        </div>
      </Card>

      <Card title="操作">
        <div className="btn-row">
          <button className="btn btn-ghost btn-sm" onClick={() => navigate('/tasks')}>
            返回任务列表
          </button>
        </div>
      </Card>
    </div>
  );
}
