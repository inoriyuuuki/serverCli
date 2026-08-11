import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { api, unwrapList, ApiError } from '../api/client';
import type { NodeInfo } from '../api/types';
import { useApi, errorMessage } from '../lib/useApi';
import { useRealtime } from '../lib/realtime';
import { Badge, Card, EmptyState, ErrorState, LoadingState, PageHeader, Select } from '../components/ui';
import type { Tone } from '../components/ui';
import { nodeName } from '../components/NodeInfo';

interface ServiceOwnership {
  node_id: string;
  node_name?: string;
  service: string;
  owner: string;
  environment?: string;
  updated_at?: string;
}

interface AdoptPlan {
  node_id: string;
  service: string;
  current_owner: string;
  steps: string[];
  redlines: string[];
  dry_run: boolean;
}

const OWNER_TONE: Record<string, Tone> = {
  'legacy-init': 'gray',
  'migration-frozen': 'amber',
  adopting: 'blue',
  servercli: 'green',
  'rollback-pending': 'red',
};

const OWNER_ORDER = ['legacy-init', 'migration-frozen', 'adopting', 'servercli', 'rollback-pending'];

export default function MigrationConsolePage() {
  const navigate = useNavigate();
  const nodesState = useApi<unknown>('/nodes', { pollIntervalMs: 60000 });
  const svcState = useApi<unknown>('/migrate/services', { pollIntervalMs: 30000 });
  useRealtime(['nodes_changed', 'tasks_changed'], () => {
    nodesState.reload();
    svcState.reload();
  });

  const nodes = useMemo(() => unwrapList<NodeInfo>(nodesState.data, ['nodes']), [nodesState.data]);
  const services = useMemo(() => unwrapList<ServiceOwnership>(svcState.data, ['services']), [svcState.data]);

  const [nodeId, setNodeId] = useState('');
  const [service, setService] = useState('');
  const [plan, setPlan] = useState<AdoptPlan | null>(null);
  const [planErr, setPlanErr] = useState<string | null>(null);
  const [planLoading, setPlanLoading] = useState(false);
  const [opsBusy, setOpsBusy] = useState(false);
  const [opsResult, setOpsResult] = useState<{ taskId?: string; error?: string } | null>(null);

  const filtered = nodeId ? services.filter((s) => s.node_id === nodeId) : services;

  async function loadPlan(svc: string) {
    setService(svc);
    setOpsResult(null);
    if (!svc) {
      setPlan(null);
      setPlanErr(null);
      return;
    }
    setPlanLoading(true);
    setPlanErr(null);
    try {
      const data = await api.get<AdoptPlan>('/migrate/plan', { node_id: nodeId, service: svc });
      setPlan(data);
    } catch (err) {
      setPlan(null);
      setPlanErr(err instanceof ApiError ? err.message : String(err));
    } finally {
      setPlanLoading(false);
    }
  }

  async function runOp(operation: string) {
    if (!nodeId || !service) return;
    setOpsBusy(true);
    setOpsResult(null);
    try {
      const idem = (typeof crypto !== 'undefined' && crypto.randomUUID ? crypto.randomUUID() : String(Date.now())) as string;
      const data = await api.post<{ task: { id: string } }>(
        '/migrate/ops',
        { node_id: nodeId, service, operation, confirm: true },
        { headers: { 'Idempotency-Key': idem } },
      );
      setOpsResult({ taskId: data.task.id });
      svcState.reload();
    } catch (err) {
      setOpsResult({ error: err instanceof ApiError ? err.message : String(err) });
    } finally {
      setOpsBusy(false);
    }
  }

  return (
    <div className="page">
      <PageHeader title="迁移与运维" subtitle="分服务从旧 init 迁移到 ServerCLI（adopt），并在此派发后续 update/backup/restore" />

      <div className="mb-3 d-flex flex-wrap gap-3 align-items-end">
        <div style={{ minWidth: 220 }}>
          <Select aria-label="节点" value={nodeId} onChange={(e) => { setNodeId(e.target.value); setService(''); setPlan(null); setOpsResult(null); }}>
            <option value="">全部节点</option>
            {nodes.map((n) => (
              <option key={n.id} value={n.id}>
                {nodeName(n)}（{n.role ?? ''}）
              </option>
            ))}
          </Select>
        </div>
        <div style={{ minWidth: 220 }}>
          <Select aria-label="服务" value={service} onChange={(e) => loadPlan(e.target.value)}>
            <option value="">选择服务查看计划…</option>
            {filtered.map((s) => (
              <option key={s.node_id + '/' + s.service} value={s.service}>
                {s.service}
              </option>
            ))}
          </Select>
        </div>
        <div className="text-muted small">owner 状态机：{OWNER_ORDER.join(' → ')}</div>
      </div>

      <Card title={`服务归属（${filtered.length}）`}>
        {svcState.loading ? (
          <LoadingState />
        ) : svcState.error ? (
          <ErrorState message={errorMessage(svcState.error)} onRetry={() => svcState.reload()} />
        ) : filtered.length === 0 ? (
          <EmptyState
            title="暂无服务归属上报"
            hint="节点 Agent（0.0.33+）在心跳中上报本地 ServerCLI ownership；尚未 adopt 前此处为空。adopt 后服务会出现在这里。"
          />
        ) : (
          <table className="table table-sm table-hover">
            <thead>
              <tr>
                <th>服务</th>
                <th>节点</th>
                <th>owner</th>
                <th>环境</th>
                <th>最近上报</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((s) => (
                <tr key={s.node_id + '/' + s.service} onClick={() => loadPlan(s.service)} style={{ cursor: 'pointer' }}>
                  <td>{s.service}</td>
                  <td>{s.node_name || s.node_id}</td>
                  <td>
                    <Badge tone={OWNER_TONE[s.owner] ?? 'gray'}>{s.owner}</Badge>
                  </td>
                  <td>{s.environment || '-'}</td>
                  <td>{s.updated_at ? new Date(s.updated_at).toLocaleString() : '-'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      {service && (
        <div className="row g-3 mt-1">
          <div className="col-lg-6">
            <Card title={`adopt 计划：${service}`}>
              {planLoading ? (
                <LoadingState />
              ) : planErr ? (
                <ErrorState message={planErr} />
              ) : plan ? (
                <>
                  <div className="mb-2">
                    当前 owner：
                    <Badge tone={OWNER_TONE[plan.current_owner] ?? 'gray'}>{plan.current_owner}</Badge>
                    <span className="text-muted small ms-2">计划只读，不落盘</span>
                  </div>
                  <ol className="small mb-0">
                    {plan.steps.map((step) => (
                      <li key={step} className="mb-1">{step}</li>
                    ))}
                  </ol>
                </>
              ) : null}
            </Card>
          </div>
          <div className="col-lg-6">
            <Card title="操作派发">
              {plan && (
                <div className="mb-3">
                  <div className="fw-semibold small mb-1 text-destructive">红线</div>
                  <ul className="small text-destructive mb-0">
                    {plan.redlines.map((r) => (
                      <li key={r}>{r}</li>
                    ))}
                  </ul>
                </div>
              )}
              <div className="d-flex flex-wrap gap-2">
                <button className="btn btn-sm btn-primary" disabled={opsBusy || !nodeId} onClick={() => runOp('adopt')}>
                  adopt
                </button>
                <button className="btn btn-sm btn-outline-primary" disabled={opsBusy || !nodeId} onClick={() => runOp('update')}>
                  update
                </button>
                <button className="btn btn-sm btn-outline-primary" disabled={opsBusy || !nodeId} onClick={() => runOp('backup')}>
                  backup
                </button>
                <button
                  className="btn btn-sm btn-outline-danger"
                  disabled={opsBusy || !nodeId}
                  onClick={() => {
                    const bid = window.prompt('restore 为高风险操作，请输入 backup_id：');
                    if (bid) runOp('restore');
                  }}
                >
                  restore
                </button>
              </div>
              {opsBusy && <div className="small mt-2 text-muted">正在派发任务…</div>}
              {opsResult?.taskId && (
                <div className="small mt-2">
                  任务已创建：
                  <a
                    href={`#/tasks/${opsResult.taskId}`}
                    onClick={(e) => {
                      e.preventDefault();
                      navigate(`/tasks/${opsResult.taskId}`);
                    }}
                  >
                    {opsResult.taskId}
                  </a>
                </div>
              )}
              {opsResult?.error && <div className="small mt-2 text-destructive">{opsResult.error}</div>}
            </Card>
          </div>
        </div>
      )}
    </div>
  );
}
