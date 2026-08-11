import { useMemo, useState } from 'react';
import { ApiError, api, unwrapList, unwrapObject } from '../api/client';
import type { OperationStepView, OperationView } from '../api/types';
import { Badge, Card, EmptyState, ErrorState, LoadingState, PageHeader, RiskBadge, Select, TimeCell, type Tone } from '../components/ui';
import { errorMessage, useApi } from '../lib/useApi';
import { useRealtime } from '../lib/realtime';
import { shortId } from '../lib/format';

const STATUS_TONES: Record<string, Tone> = {
  planned: 'gray',
  awaiting_approval: 'amber',
  queued: 'blue',
  dispatched: 'indigo',
  running: 'green',
  verifying: 'teal',
  succeeded: 'green',
  failed: 'red',
  rolling_back: 'amber',
  rolled_back: 'gray',
  cancelled: 'gray',
  result_unknown: 'amber',
};

function OperationStatus({ status }: { status?: string }) {
  return <Badge tone={STATUS_TONES[status ?? ''] ?? 'gray'}>{status || 'unknown'}</Badge>;
}


export default function OperationsPage() {
  const [status, setStatus] = useState('');
  const [selectedId, setSelectedId] = useState('');
  const listState = useApi<unknown>('/operations', { query: { status }, pollIntervalMs: 10000 });
  const detailState = useApi<unknown>(selectedId ? `/operations/${encodeURIComponent(selectedId)}` : null, { pollIntervalMs: 5000 });
  useRealtime(['operations_changed', 'tasks_changed'], () => {
    listState.reload();
    detailState.reload();
  });

  const operations = useMemo(() => unwrapList<OperationView>(listState.data, ['operations']), [listState.data]);
  const selectedFromList = operations.find((operation) => operation.id === selectedId) ?? null;
  const selected = useMemo(
    () => unwrapObject<OperationView>(detailState.data, ['operation']),
    [detailState.data],
  ) ?? selectedFromList;
  const steps = useMemo(
    () => unwrapList<OperationStepView>(detailState.data, ['steps']).sort((a, b) => a.sequence - b.sequence),
    [detailState.data],
  );

  const [actionBusy, setActionBusy] = useState<'approve' | 'cancel' | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  async function runAction(action: 'approve' | 'cancel') {
    if (!selected) return;
    setActionBusy(action);
    setActionError(null);
    try {
      await api.post(`/operations/${encodeURIComponent(selected.id)}/${action}`, {});
      listState.reload();
      detailState.reload();
      } catch (err) {
      setActionError(err instanceof ApiError ? err.message : `${action === 'approve' ? '审批' : '取消'}操作失败`);
    } finally {
      setActionBusy(null);
    }
  }

  return (
    <div className="page">
      <PageHeader title="运维操作" subtitle="查看声明式 Operation V2 状态、步骤与审批结果" />

      <Card
        title={`操作列表（${operations.length}）`}
        actions={
          <div className="btn-row">
            <Select aria-label="状态筛选" value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">全部状态</option>
              {Object.keys(STATUS_TONES).map((item) => <option key={item} value={item}>{item}</option>)}
            </Select>
            <button className="btn btn-ghost btn-sm" onClick={listState.reload}>刷新</button>
          </div>
        }
      >
        {listState.loading && listState.data === null ? (
          <LoadingState label="加载运维操作…" />
        ) : listState.error ? (
          <ErrorState message={errorMessage(listState.error)} onRetry={listState.reload} />
        ) : operations.length === 0 ? (
          <EmptyState title="暂无运维操作" hint={status ? '当前状态筛选下没有记录。' : '操作创建后会显示在这里。'} />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead><tr><th>Operation</th><th>类型</th><th>目标</th><th>风险</th><th>审批</th><th>状态</th><th>创建时间</th></tr></thead>
              <tbody>
                {operations.map((operation) => (
                  <tr key={operation.id} className="clickable" onClick={() => { setSelectedId(operation.id); setActionError(null); }}>
                    <td><strong className="mono">{shortId(operation.operation_id, 16)}</strong><div className="muted mono" style={{ fontSize: 12 }}>{operation.id}</div></td>
                    <td>{operation.operation_type}</td>
                    <td className="mono">{operation.node_id || operation.cluster_id || '—'}{operation.module_id && <div className="muted">module: {operation.module_id}</div>}</td>
                    <td><RiskBadge risk={operation.risk_level} /></td>
                    <td>{operation.approval || '—'}</td>
                    <td><OperationStatus status={operation.status} /></td>
                    <td><TimeCell value={operation.created_at} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {selectedId && (
        <Card
          title={`操作详情 · ${shortId(selected?.operation_id ?? selectedId, 16)}`}
          actions={
            <div className="btn-row">
              {selected?.status === 'planned' && <button className="btn btn-primary btn-sm" disabled={actionBusy !== null} onClick={() => runAction('approve')}>{actionBusy === 'approve' ? '批准中…' : '批准'}</button>}
              {selected?.status === 'planned' && <button className="btn btn-danger btn-sm" disabled={actionBusy !== null} onClick={() => runAction('cancel')}>{actionBusy === 'cancel' ? '取消中…' : '取消操作'}</button>}
              <button className="btn btn-ghost btn-sm" onClick={() => setSelectedId('')}>关闭</button>
            </div>
          }
        >
          {actionError && <div className="alert alert-danger mb-3">{actionError}</div>}
          {detailState.loading && !selected ? (
            <LoadingState label="加载操作详情…" />
          ) : detailState.error && !selected ? (
            <ErrorState message={errorMessage(detailState.error)} onRetry={detailState.reload} />
          ) : selected ? (
            <>
              <dl className="kv kv-2col mb-3">
                <dt>ID</dt><dd className="mono">{selected.id}</dd>
                <dt>Operation ID</dt><dd className="mono">{selected.operation_id}</dd>
                <dt>类型</dt><dd>{selected.operation_type}</dd>
                <dt>状态</dt><dd><OperationStatus status={selected.status} /></dd>
                <dt>集群</dt><dd className="mono">{selected.cluster_id || '—'}</dd>
                <dt>节点</dt><dd className="mono">{selected.node_id || '—'}</dd>
                <dt>模块</dt><dd className="mono">{selected.module_id || '—'}</dd>
                <dt>Service Instance</dt><dd className="mono">{selected.service_instance_id || '—'}</dd>
                <dt>发起者</dt><dd>{selected.requested_by || '—'}</dd>
                <dt>时间</dt><dd><TimeCell value={selected.started_at ?? selected.created_at} /> → <TimeCell value={selected.finished_at} fallback="未结束" /></dd>
                {(selected.error_code || selected.error_message) && <><dt>错误</dt><dd className="text-danger"><span className="mono">{selected.error_code || 'ERROR'}</span> {selected.error_message}</dd></>}
              </dl>
              <h3 className="mb-3">执行步骤</h3>
              {detailState.loading && detailState.data === null ? (
                <LoadingState label="加载操作步骤…" />
              ) : detailState.error ? (
                <ErrorState message={errorMessage(detailState.error)} onRetry={detailState.reload} />
              ) : steps.length === 0 ? (
                <EmptyState title="暂无步骤" />
              ) : (
                <div className="table-wrap"><table className="table"><thead><tr><th>#</th><th>模块 / 操作</th><th>尝试</th><th>Commit Point</th><th>状态</th><th>消息</th><th>时间</th></tr></thead><tbody>
                  {steps.map((step) => <tr key={step.id}><td className="mono">{step.sequence}</td><td><span className="mono">{step.module_id || '—'}</span><div className="muted">{step.operation || '—'}</div></td><td>{step.attempt}</td><td className="mono">{step.commit_point || '—'}</td><td><OperationStatus status={step.status} /></td><td className={step.error_type ? 'text-danger' : undefined}>{step.message || step.error_type || '—'}</td><td><TimeCell value={step.started_at} /><div className="muted"><TimeCell value={step.completed_at} fallback="" /></div></td></tr>)}
                </tbody></table></div>
              )}
            </>
          ) : <EmptyState title="未找到操作" />}
        </Card>
      )}
    </div>
  );
}
