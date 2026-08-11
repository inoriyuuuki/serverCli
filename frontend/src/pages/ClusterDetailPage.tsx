import { useMemo, useState, type FormEvent } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ApiError, api, unwrapList, unwrapObject } from '../api/client';
import type { Cluster, DeclarativeNode, NodeProfile, ServiceReference } from '../api/types';
import {
  Badge,
  Card,
  EmptyState,
  ErrorState,
  Field,
  LoadingState,
  PageHeader,
  Select,
  StatusBadge,
  TextInput,
  TimeCell,
} from '../components/ui';
import { errorMessage, useApi } from '../lib/useApi';
import { useRealtime } from '../lib/realtime';
import { cn } from '../lib/format';

type Tab = 'profiles' | 'nodes' | 'services';

export default function ClusterDetailPage() {
  const { id = '' } = useParams();
  const clusterPath = `/clusters/${encodeURIComponent(id)}`;
  const clusterState = useApi<unknown>(id ? clusterPath : null);
  const profilesState = useApi<unknown>(id ? `${clusterPath}/profiles` : null);
  const nodesState = useApi<unknown>(id ? '/declarative-nodes' : null, { query: { cluster_id: id } });
  const servicesState = useApi<unknown>(id ? `${clusterPath}/service-references` : null);
  useRealtime(['clusters_changed', 'nodes_changed'], () => {
    clusterState.reload();
    profilesState.reload();
    nodesState.reload();
    servicesState.reload();
  });

  const cluster = useMemo(() => unwrapObject<Cluster>(clusterState.data, ['cluster']), [clusterState.data]);
  const profiles = useMemo(() => unwrapList<NodeProfile>(profilesState.data, ['profiles']), [profilesState.data]);
  const nodes = useMemo(() => unwrapList<DeclarativeNode>(nodesState.data, ['declarative_nodes', 'nodes']), [nodesState.data]);
  const services = useMemo(() => unwrapList<ServiceReference>(servicesState.data, ['service_references', 'services']), [servicesState.data]);
  const [tab, setTab] = useState<Tab>('profiles');

  if (clusterState.loading && !cluster) {
    return <div className="page"><PageHeader title="集群详情" /><LoadingState label="加载集群…" /></div>;
  }
  if (clusterState.error) {
    return <div className="page"><PageHeader title="集群详情" /><ErrorState message={errorMessage(clusterState.error)} onRetry={clusterState.reload} /></div>;
  }
  if (!cluster) {
    return <div className="page"><PageHeader title="集群详情" /><EmptyState title="未找到集群" hint={<Link to="/clusters">返回集群列表</Link>} /></div>;
  }

  return (
    <div className="page">
      <PageHeader
        title={cluster.name}
        subtitle={<Link to="/clusters" className="muted">← 返回集群列表</Link>}
        actions={<div className="btn-row"><Badge tone={cluster.environment === 'production' ? 'red' : 'blue'}>{cluster.environment}</Badge><StatusBadge status={cluster.status} /></div>}
      />

      <Card title="集群信息">
        <dl className="kv kv-2col">
          <dt>集群 ID</dt><dd className="mono">{cluster.id}</dd>
          <dt>主节点</dt><dd className="mono">{cluster.active_primary_node_id || '—'}</dd>
          <dt>Primary Epoch</dt><dd className="mono">{cluster.primary_epoch}</dd>
          <dt>Release 通道</dt><dd>{cluster.release_channel || '—'}</dd>
          <dt>OSS Provider</dt><dd className="mono">{cluster.oss_provider_ref || '—'}</dd>
          <dt>更新时间</dt><dd><TimeCell value={cluster.updated_at ?? cluster.created_at} /></dd>
        </dl>
      </Card>

      <div className="tabs" role="tablist">
        <button className={cn('tab', tab === 'profiles' && 'tab-active')} onClick={() => setTab('profiles')}>Profiles <span className="tab-count">{profiles.length}</span></button>
        <button className={cn('tab', tab === 'nodes' && 'tab-active')} onClick={() => setTab('nodes')}>Declarative Nodes <span className="tab-count">{nodes.length}</span></button>
        <button className={cn('tab', tab === 'services' && 'tab-active')} onClick={() => setTab('services')}>Service References <span className="tab-count">{services.length}</span></button>
      </div>

      {tab === 'profiles' && <ProfilesSection clusterPath={clusterPath} state={profilesState} profiles={profiles} />}
      {tab === 'nodes' && <NodesSection clusterId={id} state={nodesState} nodes={nodes} profiles={profiles} />}
      {tab === 'services' && <ServicesSection clusterPath={clusterPath} state={servicesState} services={services} nodes={nodes} />}
    </div>
  );
}

interface ReloadableState {
  loading: boolean;
  data: unknown;
  error: Parameters<typeof errorMessage>[0];
  reload: () => void;
}

function ProfilesSection({ clusterPath, state, profiles }: { clusterPath: string; state: ReloadableState; profiles: NodeProfile[] }) {
  const [name, setName] = useState('');
  const [version, setVersion] = useState('1');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setFormError(null);
    try {
      await api.post(`${clusterPath}/profiles`, { name: name.trim(), version: version.trim(), modules: [] });
      setName('');
      state.reload();
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : '创建 Profile 失败');
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <Card title="创建 Profile">
        <form onSubmit={submit}>
          <div className="grid grid-2">
            <Field label="名称" required><TextInput value={name} onChange={(e) => setName(e.target.value)} required /></Field>
            <Field label="版本" required><TextInput value={version} onChange={(e) => setVersion(e.target.value)} required /></Field>
          </div>
          {formError && <div className="alert alert-danger mb-3">{formError}</div>}
          <button className="btn btn-primary" disabled={busy || !name.trim() || !version.trim()}>{busy ? '创建中…' : '创建 Profile'}</button>
        </form>
      </Card>
      <Card title={`Profiles（${profiles.length}）`}>
        <SectionState state={state} empty="暂无 Profile" count={profiles.length}>
          <div className="table-wrap"><table className="table"><thead><tr><th>名称</th><th>版本</th><th>模块</th><th>状态</th><th>更新时间</th></tr></thead><tbody>
            {profiles.map((profile) => <tr key={profile.id}><td><strong>{profile.name}</strong><div className="muted mono" style={{ fontSize: 12 }}>{profile.id}</div></td><td className="mono">{profile.version}</td><td>{profile.modules?.length ? profile.modules.map((module) => module.module_id).join(', ') : '—'}</td><td><StatusBadge status={profile.status} /></td><td><TimeCell value={profile.updated_at ?? profile.created_at} /></td></tr>)}
          </tbody></table></div>
        </SectionState>
      </Card>
    </>
  );
}

function NodesSection({ clusterId, state, nodes, profiles }: { clusterId: string; state: ReloadableState; nodes: DeclarativeNode[]; profiles: NodeProfile[] }) {
  const [nodeId, setNodeId] = useState('');
  const [role, setRole] = useState('child');
  const [profileId, setProfileId] = useState('');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setFormError(null);
    try {
      await api.post('/declarative-nodes', {
        cluster_id: clusterId,
        node_id: nodeId.trim(),
        role,
        profile_id: profileId || undefined,
        lifecycle: 'draft',
        labels: {},
        addresses: [],
      });
      setNodeId('');
      state.reload();
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : '创建声明式节点失败');
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <Card title="创建 Declarative Node">
        <form onSubmit={submit}>
          <div className="grid grid-3">
            <Field label="Node ID" required><TextInput value={nodeId} onChange={(e) => setNodeId(e.target.value)} placeholder="稳定节点标识" required /></Field>
            <Field label="角色" required><Select value={role} onChange={(e) => setRole(e.target.value)}><option value="child">child</option><option value="primary">primary</option></Select></Field>
            <Field label="Profile"><Select value={profileId} onChange={(e) => setProfileId(e.target.value)}><option value="">暂不绑定</option>{profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name} · {profile.version}</option>)}</Select></Field>
          </div>
          {formError && <div className="alert alert-danger mb-3">{formError}</div>}
          <button className="btn btn-primary" disabled={busy || !nodeId.trim()}>{busy ? '创建中…' : '创建节点'}</button>
        </form>
      </Card>
      <Card title={`Declarative Nodes（${nodes.length}）`}>
        <SectionState state={state} empty="暂无声明式节点" count={nodes.length}>
          <div className="table-wrap"><table className="table"><thead><tr><th>Node ID</th><th>角色</th><th>Profile</th><th>生命周期</th><th>状态</th><th>Revision</th><th>Agent</th></tr></thead><tbody>
            {nodes.map((node) => <tr key={node.id}><td className="mono">{node.node_id}<div className="muted" style={{ fontSize: 12 }}>{node.id}</div></td><td><Badge tone={node.role === 'primary' ? 'indigo' : 'teal'}>{node.role}</Badge></td><td className="mono">{node.profile_id || '—'}</td><td><StatusBadge status={node.lifecycle} /></td><td><StatusBadge status={node.status} /></td><td className="mono">{node.applied_revision || '—'} / {node.desired_revision || '—'}</td><td><StatusBadge status={node.agent_status} /></td></tr>)}
          </tbody></table></div>
        </SectionState>
      </Card>
    </>
  );
}

function ServicesSection({ clusterPath, state, services, nodes }: { clusterPath: string; state: ReloadableState; services: ServiceReference[]; nodes: DeclarativeNode[] }) {
  const [name, setName] = useState('');
  const [instanceId, setInstanceId] = useState('');
  const [nodeId, setNodeId] = useState('');
  const [address, setAddress] = useState('');
  const [port, setPort] = useState('');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setFormError(null);
    try {
      await api.post(`${clusterPath}/service-references`, {
        name: name.trim(),
        service_instance_id: instanceId.trim() || undefined,
        node_id: nodeId || undefined,
        address: address.trim() || undefined,
        port: port ? Number(port) : undefined,
      });
      setName('');
      setInstanceId('');
      setAddress('');
      setPort('');
      state.reload();
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : '创建 Service Reference 失败');
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <Card title="创建 Service Reference">
        <form onSubmit={submit}>
          <div className="grid grid-3">
            <Field label="名称" required><TextInput value={name} onChange={(e) => setName(e.target.value)} required /></Field>
            <Field label="Service Instance ID"><TextInput value={instanceId} onChange={(e) => setInstanceId(e.target.value)} /></Field>
            <Field label="目标节点"><Select value={nodeId} onChange={(e) => setNodeId(e.target.value)}><option value="">不指定</option>{nodes.map((node) => <option key={node.id} value={node.node_id}>{node.node_id}</option>)}</Select></Field>
            <Field label="地址"><TextInput value={address} onChange={(e) => setAddress(e.target.value)} placeholder="主机名或 IP" /></Field>
            <Field label="端口"><TextInput type="number" min="1" max="65535" value={port} onChange={(e) => setPort(e.target.value)} /></Field>
          </div>
          {formError && <div className="alert alert-danger mb-3">{formError}</div>}
          <button className="btn btn-primary" disabled={busy || !name.trim()}>{busy ? '创建中…' : '创建引用'}</button>
        </form>
      </Card>
      <Card title={`Service References（${services.length}）`}>
        <SectionState state={state} empty="暂无 Service Reference" count={services.length}>
          <div className="table-wrap"><table className="table"><thead><tr><th>名称</th><th>实例</th><th>节点</th><th>地址</th><th>状态</th></tr></thead><tbody>
            {services.map((service) => <tr key={service.id}><td><strong>{service.name}</strong><div className="muted mono" style={{ fontSize: 12 }}>{service.id}</div></td><td className="mono">{service.service_instance_id || '—'}</td><td className="mono">{service.node_id || '—'}</td><td className="mono">{service.address ? `${service.address}${service.port ? `:${service.port}` : ''}` : '—'}</td><td><StatusBadge status={service.status} /></td></tr>)}
          </tbody></table></div>
        </SectionState>
      </Card>
    </>
  );
}

function SectionState({ state, empty, count, children }: { state: ReloadableState; empty: string; count: number; children: React.ReactNode }) {
  if (state.loading && state.data === null) return <LoadingState />;
  if (state.error) return <ErrorState message={errorMessage(state.error)} onRetry={state.reload} />;
  if (count === 0) return <EmptyState title={empty} />;
  return children;
}
