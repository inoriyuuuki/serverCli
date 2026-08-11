import { useMemo, useState } from 'react';
import { unwrapList } from '../api/client';
import type { ReleaseCacheEntry } from '../api/types';
import { Badge, Card, EmptyState, ErrorState, LoadingState, PageHeader, Select, TextInput, TimeCell, type Tone } from '../components/ui';
import { errorMessage, useApi } from '../lib/useApi';
import { useRealtime } from '../lib/realtime';
import { formatBytes, shortId } from '../lib/format';

const CACHE_TONES: Record<string, Tone> = {
  pending: 'amber',
  available: 'green',
  failed: 'red',
  archived: 'gray',
};

export default function ReleaseCachePage() {
  const [status, setStatus] = useState('');
  const [version, setVersion] = useState('');
  const state = useApi<unknown>('/release-cache', { query: { status, version }, pollIntervalMs: 30000 });
  useRealtime(['release_cache_changed'], state.reload);
  const entries = useMemo(() => unwrapList<ReleaseCacheEntry>(state.data, ['release_cache', 'entries']), [state.data]);

  return (
    <div className="page">
      <PageHeader title="Release 缓存" subtitle="查看从发布源同步到 OSS 的安装包与校验状态" />
      <Card
        title={`缓存条目（${entries.length}）`}
        actions={<div className="btn-row"><TextInput aria-label="版本筛选" placeholder="版本" value={version} onChange={(e) => setVersion(e.target.value)} style={{ width: 150 }} /><Select aria-label="状态筛选" value={status} onChange={(e) => setStatus(e.target.value)}><option value="">全部状态</option>{Object.keys(CACHE_TONES).map((item) => <option key={item} value={item}>{item}</option>)}</Select><button className="btn btn-ghost btn-sm" onClick={state.reload}>刷新</button></div>}
      >
        {state.loading && state.data === null ? (
          <LoadingState label="加载 Release 缓存…" />
        ) : state.error ? (
          <ErrorState message={errorMessage(state.error)} onRetry={state.reload} />
        ) : entries.length === 0 ? (
          <EmptyState title="暂无缓存条目" hint="release-sync 成功同步产物后会显示在这里。" />
        ) : (
          <div className="table-wrap"><table className="table"><thead><tr><th>版本</th><th>产物</th><th>平台</th><th>大小</th><th>SHA256</th><th>Schema</th><th>状态</th><th>校验时间</th></tr></thead><tbody>
            {entries.map((entry) => <tr key={entry.id}><td><strong>{entry.version}</strong>{entry.modules_version && <div className="muted">modules {entry.modules_version}</div>}</td><td>{entry.artifact_name}<div className="muted mono" style={{ fontSize: 12 }}>{entry.source_release || entry.source_repository || entry.oss_key || '—'}</div></td><td className="mono">{entry.os || 'any'} / {entry.arch || 'any'}</td><td className="mono">{formatBytes(entry.artifact_size)}</td><td className="mono" title={entry.sha256}>{shortId(entry.sha256, 16)}</td><td className="mono">{entry.schema_min || '—'} → {entry.schema_max || '—'}</td><td><Badge tone={CACHE_TONES[entry.status] ?? 'gray'}>{entry.status}</Badge></td><td><TimeCell value={entry.verified_at ?? entry.uploaded_at ?? entry.created_at} /></td></tr>)}
          </tbody></table></div>
        )}
      </Card>
    </div>
  );
}
