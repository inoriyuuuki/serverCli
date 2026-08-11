import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { ApiError, api, unwrapList } from '../api/client';
import type { Cluster, DeclarativeNode, PrimaryTransferView } from '../api/types';
import { Badge, Card, EmptyState, ErrorState, Field, LoadingState, PageHeader, Select, TimeCell, type Tone } from '../components/ui';
import { errorMessage, useApi } from '../lib/useApi';
import { useRealtime } from '../lib/realtime';

const TRANSFER_TONES: Record<string, Tone> = {
  primary_active: 'green',
  transfer_planning: 'blue',
  candidate_preparing: 'blue',
  maintenance: 'amber',
  final_backup: 'indigo',
  candidate_restoring: 'indigo',
  candidate_verifying: 'teal',
  cutover: 'amber',
  new_primary_active: 'green',
  old_primary_demoting: 'blue',
  completed: 'green',
  failed: 'red',
  rollback_required: 'red',
};

export default function PrimaryTransferPage() {
  const clustersState = useApi<unknown>('/clusters');
  const clusters = useMemo(() => unwrapList<Cluster>(clustersState.data, ['clusters']), [clustersState.data]);
  const [clusterId, setClusterId] = useState('');
  useEffect(() => {
    if (!clusterId && clusters.length > 0) setClusterId(clusters[0].id);
  }, [clusterId, clusters]);

  const nodesState = useApi<unknown>(clusterId ? '/declarative-nodes' : null, { query: { cluster_id: clusterId } });
  const nodes = useMemo(() => unwrapList<DeclarativeNode>(nodesState.data, ['declarative_nodes', 'nodes']), [nodesState.data]);
  const transfersState = useApi<unknown>(clusterId ? '/primary-transfers' : null, { query: { cluster_id: clusterId }, pollIntervalMs: 10000 });
  const transfers = useMemo(() => unwrapList<PrimaryTransferView>(transfersState.data, ['primary_transfers', 'transfers']), [transfersState.data]);
  useRealtime(['primary_transfers_changed', 'operations_changed'], () => {
    transfersState.reload();
    nodesState.reload();
  });

  const currentCluster = clusters.find((cluster) => cluster.id === clusterId);
  const [fromNodeId, setFromNodeId] = useState('');
  const [toNodeId, setToNodeId] = useState('');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  useEffect(() => {
    if (currentCluster?.active_primary_node_id && !fromNodeId) setFromNodeId(currentCluster.active_primary_node_id);
  }, [currentCluster, fromNodeId]);

  async function createTransfer(event: FormEvent) {
    event.preventDefault();
    if (!clusterId || !fromNodeId || !toNodeId) return;
    setBusy(true);
    setFormError(null);
    try {
      await api.post('/primary-transfers', {
        cluster_id: clusterId,
        from_node_id: fromNodeId,
        to_node_id: toNodeId,
        primary_epoch: (currentCluster?.primary_epoch ?? 0) + 1,
      });
      setToNodeId('');
      transfersState.reload();
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : '创建主节点迁移失败');
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="page">
      <PageHeader title="主节点迁移" subtitle="规划并跟踪 primary 节点切换；迁移过程由状态机串行执行" />

      <Card title="创建迁移">
        {clustersState.loading && clustersState.data === null ? (
          <LoadingState label="加载集群…" />
        ) : clustersState.error ? (
          <ErrorState message={errorMessage(clustersState.error)} onRetry={clustersState.reload} />
        ) : clusters.length === 0 ? (
          <EmptyState title="请先创建集群" />
        ) : (
          <form onSubmit={createTransfer}>
            <div className="grid grid-3">
              <Field label="集群" required>
                <Select value={clusterId} onChange={(e) => { setClusterId(e.target.value); setFromNodeId(''); setToNodeId(''); }}>
                  {clusters.map((cluster) => <option key={cluster.id} value={cluster.id}>{cluster.name} · {cluster.environment}</option>)}
                </Select>
              </Field>
              <Field label="From Node ID" required>
                <Select value={fromNodeId} onChange={(e) => setFromNodeId(e.target.value)}>
                  <option value="">选择当前主节点</option>
                  {currentCluster?.active_primary_node_id && !nodes.some((node) => node.node_id === currentCluster.active_primary_node_id) && <option value={currentCluster.active_primary_node_id}>{currentCluster.active_primary_node_id}</option>}
                  {nodes.map((node) => <option key={node.id} value={node.node_id}>{node.node_id} · {node.role}</option>)}
                </Select>
              </Field>
              <Field label="To Node ID" required>
                <Select value={toNodeId} onChange={(e) => setToNodeId(e.target.value)}>
                  <option value="">选择候选节点</option>
                  {nodes.filter((node) => node.node_id !== fromNodeId).map((node) => <option key={node.id} value={node.node_id}>{node.node_id} · {node.lifecycle}</option>)}
                </Select>
              </Field>
            </div>
            {nodesState.error && <div className="alert alert-warn mb-3">节点列表加载失败：{errorMessage(nodesState.error)}</div>}
            {formError && <div className="alert alert-danger mb-3">{formError}</div>}
            <button className="btn btn-primary" disabled={busy || !fromNodeId || !toNodeId || fromNodeId === toNodeId}>{busy ? '创建中…' : '开始迁移'}</button>
          </form>
        )}
      </Card>

      <Card title={`迁移记录（${transfers.length}）`} actions={<button className="btn btn-ghost btn-sm" onClick={transfersState.reload}>刷新</button>}>
        {transfersState.loading && transfersState.data === null ? (
          <LoadingState label="加载迁移记录…" />
        ) : transfersState.error ? (
          <ErrorState message={errorMessage(transfersState.error)} onRetry={transfersState.reload} />
        ) : transfers.length === 0 ? (
          <EmptyState title="暂无主节点迁移记录" />
        ) : (
          <div className="table-wrap"><table className="table"><thead><tr><th>集群</th><th>From</th><th>To</th><th>Epoch</th><th>状态</th><th>Backup Set</th><th>创建时间</th><th>完成时间</th></tr></thead><tbody>
            {transfers.map((transfer) => <tr key={transfer.id}><td className="mono">{clusters.find((cluster) => cluster.id === transfer.cluster_id)?.name || transfer.cluster_id}</td><td className="mono">{transfer.from_node_id}</td><td className="mono">{transfer.to_node_id}</td><td className="mono">{transfer.primary_epoch}</td><td><Badge tone={TRANSFER_TONES[transfer.status] ?? 'gray'}>{transfer.status}</Badge>{transfer.error_message && <div className="text-danger" style={{ fontSize: 12 }}>{transfer.error_message}</div>}</td><td className="mono">{transfer.backup_set_id || '—'}</td><td><TimeCell value={transfer.created_at} /></td><td><TimeCell value={transfer.completed_at} fallback="进行中" /></td></tr>)}
          </tbody></table></div>
        )}
      </Card>
    </div>
  );
}
