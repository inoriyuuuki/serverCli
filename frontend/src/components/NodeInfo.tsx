import type { NodeInfo } from '../api/types';
import { Badge, TimeCell } from './ui';
import { formatBytes, formatPercent, formatUptime, shortId } from '../lib/format';

export function nodeName(n?: NodeInfo | null): string {
  return n?.alias || n?.instance_name || n?.name || n?.hostname || n?.node_id || '—';
}

export function nodeIp(n?: NodeInfo | null): string {
  if (n?.ip) return n.ip;
  const addr = (n?.addresses ?? []).find((a) => a.is_preferred) ?? (n?.addresses ?? [])[0];
  if (addr?.address) return addr.address;
  return '—';
}

export function nodeOs(n?: NodeInfo | null): string {
  const parts = [n?.os_name, n?.os_version].filter(Boolean);
  return parts.length ? parts.join(' ') : '—';
}

export function resourcePercent(used?: number | null, total?: number | null): number | null {
  if (used === null || used === undefined || total === null || total === undefined || total <= 0) return null;
  return Math.min(100, Math.max(0, (used / total) * 100));
}

export function ResourceBar({
  label,
  used,
  total,
  format = formatBytes,
}: {
  label: string;
  used?: number | null;
  total?: number | null;
  format?: (n: number | null | undefined) => string;
}) {
  const pct = resourcePercent(used, total);
  const tone = pct === null ? '' : pct >= 90 ? 'danger' : pct >= 70 ? 'warn' : '';
  return (
    <div style={{ marginBottom: 10 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 13 }}>
        <span>{label}</span>
        <span className="mono">
          {format(used)} / {format(total)}
          {pct !== null && <span className="muted">（{pct.toFixed(1)}%）</span>}
        </span>
      </div>
      <div className={`progress-bar ${tone}`} role="img" aria-label={`${label} 使用率 ${pct?.toFixed(1) ?? '未知'}%`}>
        <i style={{ width: `${pct ?? 0}%` }} />
      </div>
    </div>
  );
}

export function NodeInfoFields({ node, showAddresses = true }: { node: NodeInfo; showAddresses?: boolean }) {
  const heartbeat = node.heartbeat ?? node;
  const offset = node.time_offset_ms ?? heartbeat.time_offset_ms;
  const timeSyncOk = offset === null || offset === undefined || Math.abs(offset) < 1000;
  return (
    <dl className="kv kv-2col">
      <dt>主机名</dt>
      <dd>{node.hostname || node.name || '—'}</dd>
      <dt>服务器 ID</dt>
      <dd className="mono" title={node.id ?? node.node_id}>{node.id ?? node.node_id ?? '—'}</dd>
      <dt>角色</dt>
      <dd>
        <Badge tone={node.role === 'primary' ? 'indigo' : 'teal'}>
          {node.role === 'primary' ? '主服务器' : node.role === 'child' ? '子服务器' : node.role || '—'}
        </Badge>
      </dd>
      <dt>IP</dt>
      <dd className="mono">{nodeIp(node)}</dd>
      <dt>操作系统</dt>
      <dd>{nodeOs(node)}</dd>
      <dt>架构</dt>
      <dd className="mono">{node.arch || '—'}</dd>
      <dt>CPU 使用率</dt>
      <dd className="mono">{formatPercent(heartbeat.cpu_usage_percent ?? node.cpu_usage_percent)}</dd>
      <dt>内存</dt>
      <dd>
        <ResourceBar
          label=""
          used={heartbeat.memory_used_bytes ?? node.memory_used_bytes}
          total={heartbeat.memory_total_bytes ?? node.memory_total_bytes}
        />
      </dd>
      <dt>磁盘</dt>
      <dd>
        <ResourceBar
          label=""
          used={heartbeat.disk_used_bytes ?? node.disk_used_bytes}
          total={heartbeat.disk_total_bytes ?? node.disk_total_bytes}
        />
      </dd>
      <dt>负载 (1/5/15)</dt>
      <dd className="mono">
        {(heartbeat.load_1 ?? node.load_1) ?? '—'} / {(heartbeat.load_5 ?? node.load_5) ?? '—'} /{' '}
        {(heartbeat.load_15 ?? node.load_15) ?? '—'}
      </dd>
      <dt>运行时间</dt>
      <dd>{formatUptime(heartbeat.uptime_seconds ?? node.uptime_seconds)}</dd>
      <dt>Agent 版本</dt>
      <dd className="mono">{node.agent_version || '—'}</dd>
      <dt>最近心跳</dt>
      <dd>
        <TimeCell value={node.last_heartbeat_at ?? node.last_online_at} />
      </dd>
      <dt>时间同步</dt>
      <dd>
        {timeSyncOk ? (
          <span className="text-success">正常</span>
        ) : (
          <span className="text-danger">偏差 {offset}ms</span>
        )}
      </dd>
      {showAddresses && (node.addresses?.length ?? 0) > 0 && (
        <>
          <dt>地址</dt>
          <dd>
            <div className="pill-row">
              {node.addresses!.map((a, i) => (
                <span key={i} className="tag mono" title={`类型: ${a.address_type ?? '未知'}`}>
                  {a.address}
                  {a.service_port ? `:${a.service_port}` : ''}
                </span>
              ))}
            </div>
          </dd>
        </>
      )}
      <dt>别名 / 标签</dt>
      <dd>
        {node.alias && <span className="tag">{node.alias}</span>}{' '}
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
      <dt>注册时间</dt>
      <dd>
        <TimeCell value={node.created_at} />
      </dd>
    </dl>
  );
}

export function NodeSummaryChip({ node, link }: { node: NodeInfo; link?: string }) {
  const body = (
    <span className="mono" title={node.id ?? node.node_id}>
      {nodeName(node)} {nodeIp(node) !== '—' && `(${nodeIp(node)})`} #{shortId(node.id ?? node.node_id)}
    </span>
  );
  return link ? <a href={link}>{body}</a> : body;
}
