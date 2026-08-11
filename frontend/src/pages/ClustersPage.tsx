import { useMemo, useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { ApiError, api, unwrapList } from '../api/client';
import type { Cluster } from '../api/types';
import { Card, EmptyState, ErrorState, Field, LoadingState, PageHeader, Select, StatusBadge, TextInput, TimeCell } from '../components/ui';
import { errorMessage, useApi } from '../lib/useApi';
import { useRealtime } from '../lib/realtime';

export default function ClustersPage() {
  const navigate = useNavigate();
  const state = useApi<unknown>('/clusters', { pollIntervalMs: 30000 });
  useRealtime(['clusters_changed'], state.reload);
  const clusters = useMemo(() => unwrapList<Cluster>(state.data, ['clusters']), [state.data]);

  const [name, setName] = useState('');
  const [environment, setEnvironment] = useState('test');
  const [releaseChannel, setReleaseChannel] = useState('stable');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  async function createCluster(event: FormEvent) {
    event.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    setFormError(null);
    try {
      const created = await api.post<Cluster | { cluster: Cluster }>('/clusters', {
        name: name.trim(),
        environment,
        release_channel: releaseChannel.trim() || undefined,
      });
      const cluster = 'cluster' in created ? created.cluster : created;
      setName('');
      state.reload();
      if (cluster?.id) navigate(`/clusters/${cluster.id}`);
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : '创建集群失败');
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="page">
      <PageHeader title="集群" subtitle="管理声明式运维集群、发布通道与主节点状态" />

      <Card title="创建集群">
        <form onSubmit={createCluster}>
          <div className="grid grid-3">
            <Field label="名称" required>
              <TextInput value={name} onChange={(e) => setName(e.target.value)} placeholder="例如 production-east" required />
            </Field>
            <Field label="环境" required>
              <Select value={environment} onChange={(e) => setEnvironment(e.target.value)}>
                <option value="test">测试环境</option>
                <option value="production">正式环境</option>
              </Select>
            </Field>
            <Field label="Release 通道">
              <TextInput value={releaseChannel} onChange={(e) => setReleaseChannel(e.target.value)} placeholder="stable" />
            </Field>
          </div>
          {formError && <div className="alert alert-danger mb-3">{formError}</div>}
          <button className="btn btn-primary" type="submit" disabled={busy || !name.trim()}>
            {busy ? '创建中…' : '创建集群'}
          </button>
        </form>
      </Card>

      <Card title={`集群列表（${clusters.length}）`} actions={<button className="btn btn-ghost btn-sm" onClick={state.reload}>刷新</button>}>
        {state.loading && state.data === null ? (
          <LoadingState label="加载集群…" />
        ) : state.error ? (
          <ErrorState message={errorMessage(state.error)} onRetry={state.reload} />
        ) : clusters.length === 0 ? (
          <EmptyState title="暂无集群" hint="使用上方表单创建第一个声明式运维集群。" />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead><tr><th>名称</th><th>环境</th><th>状态</th><th>主节点</th><th>Epoch</th><th>Release 通道</th><th>更新时间</th></tr></thead>
              <tbody>
                {clusters.map((cluster) => (
                  <tr key={cluster.id} className="clickable" onClick={() => navigate(`/clusters/${cluster.id}`)}>
                    <td><strong>{cluster.name}</strong><div className="muted mono" style={{ fontSize: 12 }}>{cluster.id}</div></td>
                    <td>{cluster.environment === 'production' ? '正式环境' : cluster.environment === 'test' ? '测试环境' : cluster.environment}</td>
                    <td><StatusBadge status={cluster.status} /></td>
                    <td className="mono">{cluster.active_primary_node_id || '—'}</td>
                    <td className="mono">{cluster.primary_epoch}</td>
                    <td>{cluster.release_channel || '—'}</td>
                    <td><TimeCell value={cluster.updated_at ?? cluster.created_at} /></td>
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
