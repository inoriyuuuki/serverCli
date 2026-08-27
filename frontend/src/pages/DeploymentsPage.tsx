import { useMemo, useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { useSession } from '../auth/AuthContext';
import { useApi, errorMessage, type AsyncState } from '../lib/useApi';
import { unwrapList, ApiError } from '../api/client';
import { formatBytes, shortId } from '../lib/format';
import {
  createFeature,
  createRelease,
  createOSSProfile,
  updateOSSProfile,
  deleteOSSProfile,
  testOSSProfile,
  repositorySync,
  createConfigProfile,
  updateConfigProfile,
  deleteConfigProfile,
  createTarget,
  updateTarget,
  deleteTarget,
  getSecretValue,
  overwriteSecret,
  runBackupsForNode,
  createOperation,
  cancelOperation,
  continueOperation,
  createBootstrapSession,
  revokeBootstrapSession,
  createSecretReference,
  type CreateBootstrapSessionBody,
  type CreateFeatureBody,
  type CreateReleaseBody,
  type CreateOSSProfileBody,
  type UpdateOSSProfileBody,
  type CreateConfigProfileBody,
  type CreateTargetBody,
  type OverwriteSecretBody,
  type CreateOperationBody,
  type OperationAction,
} from '../api/deployments';
import type {
  BootstrapSession,
  DeploymentBackup,
  DeploymentConfigProfile,
  DeploymentFeature,
  DeploymentOperation,
  DeploymentOperationDetail,
  DeploymentRelease,
  DeploymentSecretReference,
  DeploymentTarget,
  SecretReferenceCreate,
  NodeInfo,
  OSSProfile,
} from '../api/types';
import {
  Alert,
  Badge,
  Card,
  Checkbox,
  EmptyState,
  ErrorState,
  Field,
  LoadingState,
  Modal,
  PageHeader,
  Select,
  Tabs,
  TextInput,
  TimeCell,
  statusMeta,
  useConfirm,
  type Tone,
} from '../components/ui';
import { nodeName } from '../components/NodeInfo';

/* ------------------------------- 状态展示 ------------------------------- */

const DEPLOY_STATUS: Record<string, { label: string; tone: Tone }> = {
  // operation
  draft: { label: '草稿', tone: 'gray' },
  validated: { label: '已校验', tone: 'blue' },
  awaiting_confirmation: { label: '待确认', tone: 'amber' },
  queued: { label: '排队中', tone: 'blue' },
  running: { label: '运行中', tone: 'green' },
  succeeded: { label: '成功', tone: 'green' },
  partial_failed: { label: '部分失败', tone: 'amber' },
  failed: { label: '失败', tone: 'red' },
  cancelled: { label: '已取消', tone: 'gray' },
  rolled_back: { label: '已回滚', tone: 'teal' },
  rollback_failed: { label: '回滚失败', tone: 'red' },
  skipped: { label: '已跳过（无需操作）', tone: 'gray' },
  // bootstrap session
  created: { label: '已创建', tone: 'gray' },
  repository_syncing: { label: '仓库同步中', tone: 'blue' },
  repository_verified: { label: '仓库已校验', tone: 'teal' },
  xray_installing: { label: '安装 Xray', tone: 'blue' },
  proxy_checking: { label: '代理检查', tone: 'blue' },
  proxy_ready: { label: '代理就绪', tone: 'teal' },
  agent_downloading: { label: '下载 Agent', tone: 'blue' },
  agent_verifying: { label: '校验 Agent', tone: 'blue' },
  agent_installing: { label: '安装 Agent', tone: 'blue' },
  enrollment_pending: { label: '待注册', tone: 'amber' },
  node_online: { label: '节点在线', tone: 'green' },
  completed: { label: '已完成', tone: 'green' },
  expired: { label: '已过期', tone: 'gray' },
  revoked: { label: '已撤销', tone: 'red' },
  // target actual_status
  healthy: { label: '健康', tone: 'green' },
  outdated: { label: '版本落后', tone: 'amber' },
  error: { label: '异常', tone: 'red' },
  unknown: { label: '未知', tone: 'gray' },
};

function deployStatusMeta(status?: string | null): { label: string; tone: Tone } {
  const s = (status ?? '').toLowerCase();
  return DEPLOY_STATUS[s] ?? statusMeta(s);
}

function DeployBadge({ status }: { status?: string | null }) {
  const m = deployStatusMeta(status);
  return <Badge tone={m.tone}>{m.label}</Badge>;
}

const ACTION_LABEL: Record<string, string> = {
  install: '安装',
  update: '更新',
  backup: '备份',
  rollback: '回滚',
  health_check: '健康检查',
  restore: '恢复',
};

const SCOPE_LABEL: Record<string, string> = {
  shared: '共享',
  node: '节点',
};

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    // 剪贴板不可用时用户可手动复制
  }
}

/* --------------------------------- 页面 --------------------------------- */

export default function DeploymentsPage() {
  const session = useSession();
  const isProd = session?.environment === 'production';
  const [tab, setTab] = useState('features');

  const featuresState = useApi<unknown>('/deployments/features', { pollIntervalMs: 30000 });
  const features = useMemo(
    () => unwrapList<DeploymentFeature>(featuresState.data, ['features']),
    [featuresState.data],
  );

  const releasesState = useApi<unknown>('/deployments/releases', { pollIntervalMs: 30000 });
  const releases = useMemo(
    () => unwrapList<DeploymentRelease>(releasesState.data, ['releases']),
    [releasesState.data],
  );

  const ossState = useApi<unknown>('/deployments/oss-profiles', { pollIntervalMs: 30000 });
  const ossProfiles = useMemo(() => unwrapList<OSSProfile>(ossState.data, ['profiles']), [ossState.data]);

  const configState = useApi<unknown>('/deployments/config-profiles', { pollIntervalMs: 30000 });
  const configProfiles = useMemo(
    () => unwrapList<DeploymentConfigProfile>(configState.data, ['profiles']),
    [configState.data],
  );

  const targetsState = useApi<unknown>('/deployments/targets', { pollIntervalMs: 15000 });
  const targets = useMemo(() => unwrapList<DeploymentTarget>(targetsState.data, ['targets']), [targetsState.data]);

  const secretsState = useApi<unknown>('/deployments/secrets/references', { pollIntervalMs: 30000 });
  const secrets = useMemo(
    () => unwrapList<DeploymentSecretReference>(secretsState.data, ['secrets']),
    [secretsState.data],
  );

  const opsState = useApi<unknown>('/deployments/operations', { pollIntervalMs: 15000 });
  const operations = useMemo(
    () => unwrapList<DeploymentOperation>(opsState.data, ['operations']),
    [opsState.data],
  );

  const backupsState = useApi<unknown>('/deployments/backups', { pollIntervalMs: 30000 });
  const backups = useMemo(() => unwrapList<DeploymentBackup>(backupsState.data, ['backups']), [backupsState.data]);

  const sessionsState = useApi<unknown>('/deployments/bootstrap-sessions', { pollIntervalMs: 30000 });
  const sessions = useMemo(
    () => unwrapList<BootstrapSession>(sessionsState.data, ['sessions']),
    [sessionsState.data],
  );

  const nodesState = useApi<unknown>('/nodes');
  const nodes = useMemo(() => unwrapList<NodeInfo>(nodesState.data, ['nodes']), [nodesState.data]);

  return (
    <div>
      <PageHeader
        title="部署管理"
        subtitle="Feature / Release / OSS 仓库 / 配置 / 目标 / Secret / 操作 / 备份 / 节点引导的统一管理（Admin）。"
      />
      <div className="alert alert-danger" role="alert" style={{ marginBottom: 12 }}>
        ⚠️ <strong>安全提示：V1 Secret 以明文存储于私有 OSS</strong>。Secret 仅允许写入（覆盖）、不允许读取/回显；
        请勿存放非必要敏感配置，正式环境覆盖需填写原因并二次确认。
      </div>
      <div className="alert alert-info" role="alert" style={{ marginBottom: 14 }}>
        V1 多节点操作默认<strong>串行</strong>执行、任一目标失败即<strong>停止</strong>（serial / fail-fast），
        失败后需人工介入（继续 / 取消 / 回滚）。
      </div>

      <Tabs
        tabs={[
          { key: 'features', label: '功能与版本', count: features.length },
          { key: 'oss', label: 'OSS 仓库', count: ossProfiles.length },
          { key: 'config', label: '配置 Profile', count: configProfiles.length },
          { key: 'targets', label: '部署目标', count: targets.length },
          { key: 'secrets', label: 'Secret 配置', count: secrets.length },
          { key: 'operations', label: '操作记录', count: operations.length },
          { key: 'bootstrap', label: '节点引导', count: sessions.length },
        ]}
        active={tab}
        onChange={setTab}
      />

      {tab === 'features' && (
        <FeaturesTab
          featuresState={featuresState}
          releasesState={releasesState}
          features={features}
          releases={releases}
        />
      )}
      {tab === 'oss' && <OSSTab ossState={ossState} ossProfiles={ossProfiles} isProd={isProd} />}
      {tab === 'config' && (
        <ConfigTab
          configState={configState}
          configProfiles={configProfiles}
          features={features}
          nodes={nodes}
          isProd={isProd}
        />
      )}
      {tab === 'targets' && (
        <TargetsTab
          targetsState={targetsState}
          targets={targets}
          nodes={nodes}
          features={features}
          releases={releases}
          configProfiles={configProfiles}
          isProd={isProd}
        />
      )}
      {tab === 'secrets' && (
        <SecretsTab secretsState={secretsState} secrets={secrets} features={features} isProd={isProd} />
      )}
      {tab === 'operations' && (
        <OperationsTab
          opsState={opsState}
          operations={operations}
          backupsState={backupsState}
          backups={backups}
          features={features}
          releases={releases}
          targets={targets}
          isProd={isProd}
        />
      )}
      {tab === 'bootstrap' && (
        <BootstrapTab sessionsState={sessionsState} sessions={sessions} nodes={nodes} isProd={isProd} />
      )}
    </div>
  );
}

/* ------------------------------ Feature / Release --------------------------- */

function FeaturesTab({
  featuresState,
  releasesState,
  features,
  releases,
}: {
  featuresState: AsyncState<unknown>;
  releasesState: AsyncState<unknown>;
  features: DeploymentFeature[];
  releases: DeploymentRelease[];
}) {
  const [featureFilter, setFeatureFilter] = useState('');
  const [featureCreateOpen, setFeatureCreateOpen] = useState(false);
  const [releaseCreateOpen, setReleaseCreateOpen] = useState(false);

  const filteredReleases = useMemo(
    () => (featureFilter ? releases.filter((r) => r.feature_id === featureFilter) : releases),
    [releases, featureFilter],
  );

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      <Card
        title="Feature 列表"
        actions={
          <>
            <button className="btn btn-ghost btn-sm" onClick={featuresState.reload}>刷新</button>
            <button className="btn btn-primary btn-sm" onClick={() => setFeatureCreateOpen(true)}>新建 Feature</button>
          </>
        }
      >
        {featuresState.loading && featuresState.data === null ? (
          <LoadingState label="加载 Feature 中…" />
        ) : featuresState.error ? (
          <ErrorState message={errorMessage(featuresState.error)} onRetry={featuresState.reload} />
        ) : features.length === 0 ? (
          <EmptyState title="暂无 Feature" hint="注册一个部署 Feature（如 webapp / worker）后即可发布 Release。" />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>Key</th>
                  <th>名称</th>
                  <th>说明</th>
                  <th>OS / Arch</th>
                  <th>备份模式</th>
                  <th>回滚能力</th>
                  <th>最低 Agent</th>
                  <th>更新时间</th>
                </tr>
              </thead>
              <tbody>
                {features.map((f) => (
                  <tr key={f.id}>
                    <td className="mono-cell">{f.feature_key}</td>
                    <td>{f.name || '—'}</td>
                    <td>
                      <span className="muted" style={{ fontSize: 12 }}>{f.description || '—'}</span>
                    </td>
                    <td>{[f.os, f.arch].filter(Boolean).join(' / ') || '—'}</td>
                    <td className="mono">{f.backup_mode || '—'}</td>
                    <td className="mono">{f.rollback_capability || '—'}</td>
                    <td className="mono">{f.minimum_agent_version || '—'}</td>
                    <td>
                      <TimeCell value={f.updated_at ?? f.created_at} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {featureCreateOpen && (
        <CreateFeatureModal
          onClose={() => setFeatureCreateOpen(false)}
          onCreated={() => {
            setFeatureFilter('');
            featuresState.reload();
          }}
        />
      )}

      <Card
        title="Release 列表"
        actions={
          <>
            <button className="btn btn-ghost btn-sm" onClick={releasesState.reload}>刷新</button>
            <button className="btn btn-primary btn-sm" onClick={() => setReleaseCreateOpen(true)} disabled={features.length === 0}>
              新建 Release
            </button>
          </>
        }
      >
        <div className="filter-bar" style={{ marginBottom: 10 }}>
          <div className="filter-group">
            <span className="filter-label">按 Feature 过滤</span>
            <Select value={featureFilter} onChange={(e) => setFeatureFilter(e.target.value)}>
              <option value="">全部 Feature</option>
              {features.map((f) => (
                <option key={f.id} value={f.id}>
                  {f.feature_key}
                </option>
              ))}
            </Select>
          </div>
        </div>
        {releasesState.loading && releasesState.data === null ? (
          <LoadingState label="加载 Release 中…" />
        ) : releasesState.error ? (
          <ErrorState message={errorMessage(releasesState.error)} onRetry={releasesState.reload} />
        ) : filteredReleases.length === 0 ? (
          <EmptyState title="暂无 Release" hint="发布 Release 后即可作为部署目标的可选版本。" />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>Feature</th>
                  <th>版本</th>
                  <th>Source Commit</th>
                  <th>大小</th>
                  <th>SHA256</th>
                  <th>备份模式</th>
                  <th>创建时间</th>
                </tr>
              </thead>
              <tbody>
                {filteredReleases.map((r) => (
                  <tr key={r.id}>
                    <td className="mono-cell">
                      {features.find((f) => f.id === r.feature_id)?.feature_key ?? shortId(r.feature_id)}
                    </td>
                    <td className="mono">{r.version}</td>
                    <td className="mono muted">{r.source_commit ? shortId(r.source_commit, 12) : '—'}</td>
                    <td className="num">{formatBytes(r.size)}</td>
                    <td className="mono muted" title={r.sha256 ?? undefined}>{r.sha256 ? shortId(r.sha256, 12) : '—'}</td>
                    <td className="mono">{r.backup_mode || '—'}</td>
                    <td>
                      <TimeCell value={r.created_at} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {releaseCreateOpen && (
        <CreateReleaseModal
          features={features}
          onClose={() => setReleaseCreateOpen(false)}
          onCreated={releasesState.reload}
        />
      )}
    </div>
  );
}

function CreateFeatureModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [form, setForm] = useState<CreateFeatureBody>({
    feature_key: '',
    name: '',
    description: '',
    backup_mode: '',
    rollback_capability: '',
    minimum_agent_version: '',
  });
  const [schemaText, setSchemaText] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const set = (key: keyof CreateFeatureBody, value: string) => setForm((p) => ({ ...p, [key]: value }));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!form.feature_key.trim()) return;
    let config_schema_json: unknown;
    if (schemaText.trim()) {
      try {
        config_schema_json = JSON.parse(schemaText);
      } catch {
        setError('config_schema_json 不是合法 JSON，请检查后重试');
        return;
      }
    }
    setBusy(true);
    setError(null);
    try {
      await createFeature({ ...form, config_schema_json });
      onCreated();
      onClose();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '创建失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open title="新建 Feature" onClose={() => !busy && onClose()} width={560}>
      <form onSubmit={submit}>
        <div className="form-grid">
          <Field label="feature_key" required hint="唯一标识，如 webapp / worker">
            <TextInput value={form.feature_key} onChange={(e) => set('feature_key', e.target.value)} placeholder="webapp" required />
          </Field>
          <Field label="名称">
            <TextInput value={form.name ?? ''} onChange={(e) => set('name', e.target.value)} placeholder="Web 应用" />
          </Field>
          <Field label="说明">
            <TextInput value={form.description ?? ''} onChange={(e) => set('description', e.target.value)} />
          </Field>
          <Field label="备份模式">
            <TextInput value={form.backup_mode ?? ''} onChange={(e) => set('backup_mode', e.target.value)} placeholder="oss / none" />
          </Field>
          <Field label="回滚能力">
            <TextInput value={form.rollback_capability ?? ''} onChange={(e) => set('rollback_capability', e.target.value)} placeholder="restore / none" />
          </Field>
          <Field label="最低 Agent 版本">
            <TextInput value={form.minimum_agent_version ?? ''} onChange={(e) => set('minimum_agent_version', e.target.value)} placeholder="0.1.0" />
          </Field>
          <Field label="config_schema_json" hint="可选，合法 JSON 文本">
            <textarea
              className="input mono"
              rows={6}
              value={schemaText}
              onChange={(e) => setSchemaText(e.target.value)}
              placeholder='{ "type": "object", "properties": {} }'
            />
          </Field>
        </div>
        {error && <Alert tone="danger">{error}</Alert>}
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost" onClick={onClose} disabled={busy}>取消</button>
          <button type="submit" className="btn btn-primary" disabled={busy || !form.feature_key.trim()}>
            {busy ? '创建中…' : '创建'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function CreateReleaseModal({
  features,
  onClose,
  onCreated,
}: {
  features: DeploymentFeature[];
  onClose: () => void;
  onCreated: () => void;
}) {
  const [form, setForm] = useState({
    feature_id: features[0]?.id ?? '',
    version: '',
    source_commit: '',
    object_key: '',
    sha256: '',
    size: '',
    install_hook: './hooks/install.sh',
    update_hook: './hooks/update.sh',
    backup_hook: './hooks/backup.sh',
    health_hook: './hooks/health.sh',
    rollback_hook: './hooks/rollback.sh',
    backup_mode: 'application_snapshot',
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const set = (key: keyof typeof form, value: string) => setForm((p) => ({ ...p, [key]: value }));

  const sha256Valid = /^[0-9a-f]{64}$/i.test(form.sha256.trim());
  const sizeNum = Number(form.size);
  const sizeValid = form.size.trim() !== '' && Number.isFinite(sizeNum) && sizeNum > 0;
  const objectKeyValid = form.object_key.trim().startsWith('deployment-repository/');

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!form.feature_id || !form.version.trim() || !form.object_key.trim() || !sha256Valid || !sizeValid) return;
    setBusy(true);
    setError(null);
    try {
      const body: CreateReleaseBody = {
        feature_id: form.feature_id,
        version: form.version.trim(),
        source_commit: form.source_commit?.trim() || undefined,
        object_key: form.object_key.trim(),
        sha256: form.sha256.trim().toLowerCase(),
        size: sizeNum,
        install_hook: form.install_hook.trim() || undefined,
        update_hook: form.update_hook.trim() || undefined,
        backup_hook: form.backup_hook.trim() || undefined,
        health_hook: form.health_hook.trim() || undefined,
        rollback_hook: form.rollback_hook.trim() || undefined,
        backup_mode: form.backup_mode || undefined,
      };
      await createRelease(body);
      onCreated();
      onClose();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '发布失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open title="新建 Release" onClose={() => !busy && onClose()} width={720}>
      <form onSubmit={submit}>
        <div className="grid grid-2">
          <Field label="Feature" required>
            <Select value={form.feature_id} onChange={(e) => set('feature_id', e.target.value)} required>
              <option value="">请选择</option>
              {features.map((f) => (
                <option key={f.id} value={f.id}>
                  {f.feature_key}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="版本" required hint="如 1.2.0（不可变，路径一经发布不可改）">
            <TextInput value={form.version} onChange={(e) => set('version', e.target.value)} placeholder="1.2.0" required />
          </Field>
        </div>
        <div className="grid grid-2">
          <Field label="Source Commit">
            <TextInput value={form.source_commit} onChange={(e) => set('source_commit', e.target.value)} placeholder="git commit sha" />
          </Field>
          <Field label="备份模式" hint="release 的默认备份模式">
            <Select value={form.backup_mode} onChange={(e) => set('backup_mode', e.target.value)}>
              {['database_dump', 'application_snapshot', 'filesystem_quiesced', 'cold_backup', 'external_snapshot', 'none'].map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        <Field label="Object Key" required hint="必须位于 deployment-repository/ 前缀内，示例：deployment-repository/releases/<feature>/<version>/<sha256>/bundle.tar.gz">
          <TextInput value={form.object_key} onChange={(e) => set('object_key', e.target.value)} placeholder="deployment-repository/releases/&lt;feature&gt;/&lt;version&gt;/&lt;sha256&gt;/bundle.tar.gz" required />
          {!objectKeyValid && form.object_key.trim() !== '' && (
            <span className="muted" style={{ fontSize: 12, color: 'var(--danger, #dc2626)' }}>object_key 必须以 deployment-repository/ 开头</span>
          )}
        </Field>
        <div className="grid grid-2">
          <Field label="SHA256" required hint="bundle 的 SHA-256（64 位小写 hex）">
            <TextInput value={form.sha256} onChange={(e) => set('sha256', e.target.value)} placeholder="64 位 hex" required />
            {!sha256Valid && form.sha256.trim() !== '' && (
              <span className="muted" style={{ fontSize: 12, color: 'var(--danger, #dc2626)' }}>必须是 64 位 hex（数字与 a-f）</span>
            )}
          </Field>
          <Field label="大小（字节）" required hint="bundle 大小，必须大于 0">
            <TextInput type="number" min={1} value={form.size} onChange={(e) => set('size', e.target.value)} placeholder="如 1048576" required />
          </Field>
        </div>
        <div className="grid grid-2">
          <Field label="Install Hook" hint="安装钩子脚本相对路径">
            <TextInput value={form.install_hook} onChange={(e) => set('install_hook', e.target.value)} placeholder="./hooks/install.sh" />
          </Field>
          <Field label="Update Hook" hint="更新钩子脚本相对路径">
            <TextInput value={form.update_hook} onChange={(e) => set('update_hook', e.target.value)} placeholder="./hooks/update.sh" />
          </Field>
          <Field label="Backup Hook" hint="备份钩子脚本相对路径">
            <TextInput value={form.backup_hook} onChange={(e) => set('backup_hook', e.target.value)} placeholder="./hooks/backup.sh" />
          </Field>
          <Field label="Health Hook" hint="健康检查钩子脚本相对路径">
            <TextInput value={form.health_hook} onChange={(e) => set('health_hook', e.target.value)} placeholder="./hooks/health.sh" />
          </Field>
          <Field label="Rollback Hook" hint="回滚钩子脚本相对路径">
            <TextInput value={form.rollback_hook} onChange={(e) => set('rollback_hook', e.target.value)} placeholder="./hooks/rollback.sh" />
          </Field>
        </div>
        {error && <Alert tone="danger">{error}</Alert>}
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost" onClick={onClose} disabled={busy}>取消</button>
          <button
            type="submit"
            className="btn btn-primary"
            disabled={busy || !form.feature_id || !form.version.trim() || !objectKeyValid || !sha256Valid || !sizeValid}
          >
            {busy ? '发布中…' : '发布'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

/* -------------------------------- OSS 仓库 -------------------------------- */

function OSSTab({
  ossState,
  ossProfiles,
  isProd,
}: {
  ossState: AsyncState<unknown>;
  ossProfiles: OSSProfile[];
  isProd: boolean;
}) {
  const confirm = useConfirm();
  const [modal, setModal] = useState<{ mode: 'create' } | { mode: 'edit'; profile: OSSProfile } | null>(null);
  const [syncBusy, setSyncBusy] = useState(false);
  const [syncResult, setSyncResult] = useState<string | null>(null);
  const [testingId, setTestingId] = useState<string | null>(null);

  const openCreate = () => setModal({ mode: 'create' });
  const openEdit = (p: OSSProfile) => setModal({ mode: 'edit', profile: p });

  const runSync = async () => {
    if (isProd) {
      const r = await confirm({
        title: '触发仓库同步',
        message: '将从私有 OSS 权威同步制品到主控 repository/ 目录并校验（catalog / features / releases / configs / secrets / manifests）。',
        confirmLabel: '开始同步',
        production: true,
        requireReason: true,
        reasonLabel: '同步原因',
      });
      if (!r.ok) return;
    }
    setSyncBusy(true);
    setSyncResult(null);
    try {
      const res = await repositorySync();
      setSyncResult(`同步已触发：${res.sync.status || 'running'}${res.sync.started_at ? `（开始于 ${res.sync.started_at}）` : ''}`);
    } catch (err) {
      setSyncResult(err instanceof ApiError ? `同步失败：${err.message}` : '同步失败，请重试');
    } finally {
      setSyncBusy(false);
    }
  };

  const runTest = async (p: OSSProfile) => {
    setTestingId(p.id);
    try {
      const res = await testOSSProfile(p.id);
      const msg = res.ok ? (res.message || '连接成功') : (res.message || '连接失败');
      window.alert(`OSS Profile「${p.name}」测试：${msg}`);
    } catch (err) {
      window.alert(err instanceof ApiError ? `测试失败：${err.message}` : '测试失败，请重试');
    } finally {
      setTestingId(null);
      ossState.reload();
    }
  };

  const remove = async (p: OSSProfile) => {
    const r = await confirm({
      title: '删除 OSS Profile',
      message: (
        <div className="kv">
          <dt>名称</dt>
          <dd>{p.name}</dd>
          <dt>Bucket</dt>
          <dd className="mono">{p.bucket || '—'}</dd>
        </div>
      ),
      confirmLabel: '确认删除',
      danger: true,
      production: isProd,
      requireReason: isProd,
      reasonLabel: '删除原因',
    });
    if (!r.ok) return;
    try {
      await deleteOSSProfile(p.id);
      ossState.reload();
    } catch (err) {
      window.alert(err instanceof ApiError ? err.message : '删除失败，请重试');
    }
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      <Card
        title="OSS Profile（私有制品源）"
        actions={
          <>
            <button className="btn btn-ghost btn-sm" onClick={() => { setSyncResult(null); ossState.reload(); }}>刷新</button>
            <button className="btn btn-primary btn-sm" onClick={openCreate}>新建 Profile</button>
          </>
        }
      >
        <div style={{ marginBottom: 10 }}>
          <button className="btn btn-ghost btn-sm" onClick={runSync} disabled={syncBusy}>
            {syncBusy ? '同步中…' : '触发仓库同步（OSS → repository/）'}
          </button>
          {syncResult && (
            <span className="muted" style={{ fontSize: 13, marginLeft: 10 }}>{syncResult}</span>
          )}
        </div>

        {ossState.loading && ossState.data === null ? (
          <LoadingState label="加载 OSS Profile 中…" />
        ) : ossState.error ? (
          <ErrorState message={errorMessage(ossState.error)} onRetry={ossState.reload} />
        ) : ossProfiles.length === 0 ? (
          <EmptyState title="暂无 OSS Profile" hint="新建私有 OSS Profile（AK/Secret 只写不读）后即可同步仓库。" />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>名称</th>
                  <th>Endpoint</th>
                  <th>Region</th>
                  <th>Bucket</th>
                  <th>Prefix</th>
                  <th>私有</th>
                  <th>最近测试</th>
                  <th>更新时间</th>
                  <th style={{ width: 230 }}>操作</th>
                </tr>
              </thead>
              <tbody>
                {ossProfiles.map((p) => (
                  <tr key={p.id}>
                    <td>{p.name || '—'}</td>
                    <td className="mono muted" title={p.endpoint ?? undefined}>{p.endpoint || '—'}</td>
                    <td className="mono">{p.region || '—'}</td>
                    <td className="mono">{p.bucket || '—'}</td>
                    <td className="mono muted">{p.prefix || '—'}</td>
                    <td>
                      {p.is_private ? <Badge tone="green">私有</Badge> : <Badge tone="gray">公开</Badge>}
                    </td>
                    <td>
                      <span className="muted" style={{ fontSize: 12 }}>{p.last_test_result || '未测试'}</span>
                    </td>
                    <td>
                      <TimeCell value={p.updated_at} />
                    </td>
                    <td>
                      <div className="btn-row">
                        <button className="btn btn-ghost btn-sm" onClick={() => runTest(p)} disabled={testingId === p.id}>
                          {testingId === p.id ? '测试中…' : '测试连接'}
                        </button>
                        <button className="btn btn-ghost btn-sm" onClick={() => openEdit(p)}>编辑</button>
                        <button className="btn btn-danger btn-sm" onClick={() => remove(p)}>删除</button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {modal && (
        <OSSProfileModal
          mode={modal.mode}
          profile={modal.mode === 'edit' ? modal.profile : null}
          onClose={() => setModal(null)}
          onSaved={() => {
            setModal(null);
            ossState.reload();
          }}
        />
      )}
    </div>
  );
}

function OSSProfileModal({
  mode,
  profile,
  onClose,
  onSaved,
}: {
  mode: 'create' | 'edit';
  profile: OSSProfile | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const isEdit = mode === 'edit';
  const [form, setForm] = useState<CreateOSSProfileBody & UpdateOSSProfileBody>({
    name: profile?.name ?? '',
    endpoint: profile?.endpoint ?? '',
    region: profile?.region ?? '',
    bucket: profile?.bucket ?? '',
    prefix: profile?.prefix ?? '',
    access_key_id: '',
    access_key_secret: '',
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const set = (key: keyof typeof form, value: string) => setForm((p) => ({ ...p, [key]: value }));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!form.name.trim() || !form.endpoint.trim() || !form.bucket.trim()) return;
    if (!isEdit && (!form.access_key_id.trim() || !form.access_key_secret.trim())) return;
    setBusy(true);
    setError(null);
    try {
      if (isEdit && profile) {
        const body: UpdateOSSProfileBody = {
          name: form.name.trim(),
          endpoint: form.endpoint.trim(),
          region: form.region?.trim() || undefined,
          bucket: form.bucket.trim(),
          prefix: form.prefix?.trim() || undefined,
        };
        // AK 只写不读：仅当用户重新输入时才提交，绝不回显旧值。
        if (form.access_key_id.trim()) body.access_key_id = form.access_key_id.trim();
        if (form.access_key_secret.trim()) body.access_key_secret = form.access_key_secret.trim();
        await updateOSSProfile(profile.id, body);
      } else {
        await createOSSProfile({
          name: form.name.trim(),
          endpoint: form.endpoint.trim(),
          region: form.region?.trim() || undefined,
          bucket: form.bucket.trim(),
          prefix: form.prefix?.trim() || undefined,
          access_key_id: form.access_key_id.trim(),
          access_key_secret: form.access_key_secret.trim(),
        });
      }
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '保存失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open title={isEdit ? `编辑 OSS Profile：${profile?.name ?? ''}` : '新建 OSS Profile'} onClose={() => !busy && onClose()} width={560}>
      <form onSubmit={submit}>
        <div>
          {isEdit && (
            <Alert tone="warn">AK/Secret 只写不读：输入框不回显旧值，留空表示不修改。</Alert>
          )}
          <Field label="名称" required>
            <TextInput value={form.name} onChange={(e) => set('name', e.target.value)} required />
          </Field>
          <Field label="Endpoint" required hint="如 https://oss-cn-hangzhou.aliyuncs.com">
            <TextInput value={form.endpoint} onChange={(e) => set('endpoint', e.target.value)} required />
          </Field>
          <Field label="Region">
            <TextInput value={form.region ?? ''} onChange={(e) => set('region', e.target.value)} placeholder="cn-hangzhou" />
          </Field>
          <Field label="Bucket" required>
            <TextInput value={form.bucket} onChange={(e) => set('bucket', e.target.value)} required />
          </Field>
          <Field label="Prefix" hint="仓库前缀，如 deployment-repository">
            <TextInput value={form.prefix ?? ''} onChange={(e) => set('prefix', e.target.value)} placeholder="deployment-repository" />
          </Field>
          <Field label="Access Key ID" required={!isEdit} hint={isEdit ? '留空则不修改' : undefined}>
            <TextInput
              type="password"
              autoComplete="off"
              value={form.access_key_id}
              onChange={(e) => set('access_key_id', e.target.value)}
              placeholder={isEdit ? '（不回显旧值）' : ''}
            />
          </Field>
          <Field label="Access Key Secret" required={!isEdit} hint={isEdit ? '留空则不修改' : undefined}>
            <TextInput
              type="password"
              autoComplete="off"
              value={form.access_key_secret}
              onChange={(e) => set('access_key_secret', e.target.value)}
              placeholder={isEdit ? '（不回显旧值）' : ''}
            />
          </Field>
        </div>
        {error && <Alert tone="danger">{error}</Alert>}
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost" onClick={onClose} disabled={busy}>取消</button>
          <button
            type="submit"
            className="btn btn-primary"
            disabled={busy || !form.name.trim() || !form.endpoint.trim() || !form.bucket.trim() || (!isEdit && (!form.access_key_id.trim() || !form.access_key_secret.trim()))}
          >
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

/* ------------------------------ 配置 Profile ------------------------------ */

function ConfigTab({
  configState,
  configProfiles,
  features,
  nodes,
  isProd,
}: {
  configState: AsyncState<unknown>;
  configProfiles: DeploymentConfigProfile[];
  features: DeploymentFeature[];
  nodes: NodeInfo[];
  isProd: boolean;
}) {
  const confirm = useConfirm();
  const [modal, setModal] = useState<{ mode: 'create' } | { mode: 'edit'; profile: DeploymentConfigProfile } | null>(null);

  const remove = async (p: DeploymentConfigProfile) => {
    const r = await confirm({
      title: '删除 Config Profile',
      message: (
        <div className="kv">
          <dt>名称</dt>
          <dd>{p.name}</dd>
          <dt>范围</dt>
          <dd>{SCOPE_LABEL[p.scope_type ?? ''] ?? p.scope_type ?? '—'}{p.scope_id ? `（${shortId(p.scope_id)}）` : ''}</dd>
        </div>
      ),
      confirmLabel: '确认删除',
      danger: true,
      production: isProd,
      requireReason: isProd,
      reasonLabel: '删除原因',
    });
    if (!r.ok) return;
    try {
      await deleteConfigProfile(p.id);
      configState.reload();
    } catch (err) {
      window.alert(err instanceof ApiError ? err.message : '删除失败，请重试');
    }
  };

  return (
    <Card
      title="Config Profile（共享 / 节点范围，YAML）"
      actions={
        <>
          <button className="btn btn-ghost btn-sm" onClick={configState.reload}>刷新</button>
          <button className="btn btn-primary btn-sm" onClick={() => setModal({ mode: 'create' })}>新建 Profile</button>
        </>
      }
    >
      {configState.loading && configState.data === null ? (
        <LoadingState label="加载 Config Profile 中…" />
      ) : configState.error ? (
        <ErrorState message={errorMessage(configState.error)} onRetry={configState.reload} />
      ) : configProfiles.length === 0 ? (
        <EmptyState title="暂无 Config Profile" hint="配置合并顺序：Feature 默认 < Profile < 节点 Override。" />
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>名称</th>
                <th>范围</th>
                <th>Feature</th>
                <th>内容 Hash</th>
                <th>版本</th>
                <th>更新时间</th>
                <th style={{ width: 150 }}>操作</th>
              </tr>
            </thead>
            <tbody>
              {configProfiles.map((p) => (
                <tr key={p.id}>
                  <td>{p.name || '—'}</td>
                  <td>
                    {SCOPE_LABEL[p.scope_type ?? ''] ?? p.scope_type ?? '—'}
                    {p.scope_id && <span className="mono muted" style={{ fontSize: 12 }}> {shortId(p.scope_id)}</span>}
                  </td>
                  <td className="mono-cell">
                    {features.find((f) => f.id === p.feature_id)?.feature_key ?? (p.feature_id ? shortId(p.feature_id) : '—')}
                  </td>
                  <td className="mono muted" title={p.content_hash ?? undefined}>{p.content_hash ? shortId(p.content_hash, 12) : '—'}</td>
                  <td className="num">{p.version ?? '—'}</td>
                  <td>
                    <TimeCell value={p.updated_at} />
                  </td>
                  <td>
                    <div className="btn-row">
                      <button className="btn btn-ghost btn-sm" onClick={() => setModal({ mode: 'edit', profile: p })}>编辑</button>
                      <button className="btn btn-danger btn-sm" onClick={() => remove(p)}>删除</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {modal && (
        <ConfigProfileModal
          mode={modal.mode}
          profile={modal.mode === 'edit' ? modal.profile : null}
          features={features}
          nodes={nodes}
          onClose={() => setModal(null)}
          onSaved={() => {
            setModal(null);
            configState.reload();
          }}
        />
      )}
    </Card>
  );
}

function ConfigProfileModal({
  mode,
  profile,
  features,
  nodes,
  onClose,
  onSaved,
}: {
  mode: 'create' | 'edit';
  profile: DeploymentConfigProfile | null;
  features: DeploymentFeature[];
  nodes: NodeInfo[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const isEdit = mode === 'edit';
  const [form, setForm] = useState<CreateConfigProfileBody>({
    name: profile?.name ?? '',
    scope_type: profile?.scope_type ?? 'shared',
    scope_id: profile?.scope_id ?? '',
    feature_id: profile?.feature_id ?? '',
    content_yaml: '',
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const set = (key: keyof CreateConfigProfileBody, value: string) => setForm((p) => ({ ...p, [key]: value }));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!form.name.trim() || !form.content_yaml.trim()) return;
    if (form.scope_type === 'node' && !form.scope_id) return;
    setBusy(true);
    setError(null);
    try {
      const body: CreateConfigProfileBody = {
        name: form.name.trim(),
        scope_type: form.scope_type,
        scope_id: form.scope_type === 'node' ? form.scope_id : undefined,
        feature_id: form.feature_id || undefined,
        content_yaml: form.content_yaml,
      };
      if (isEdit && profile) {
        await updateConfigProfile(profile.id, body);
      } else {
        await createConfigProfile(body);
      }
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '保存失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open title={isEdit ? `编辑 Config Profile：${profile?.name ?? ''}` : '新建 Config Profile'} onClose={() => !busy && onClose()} width={620}>
      <form onSubmit={submit}>
        <div>
          {isEdit && (
            <Alert tone="warn">YAML 内容不回显，编辑时请重新粘贴完整内容后保存。</Alert>
          )}
          <Field label="名称" required>
            <TextInput value={form.name} onChange={(e) => set('name', e.target.value)} required />
          </Field>
          <div className="grid grid-2">
            <Field label="范围" required>
              <Select value={form.scope_type} onChange={(e) => set('scope_type', e.target.value)}>
                <option value="shared">共享（shared）</option>
                <option value="node">节点（node）</option>
              </Select>
            </Field>
            <Field label="Feature（可选）">
              <Select value={form.feature_id} onChange={(e) => set('feature_id', e.target.value)}>
                <option value="">不限 / 通用</option>
                {features.map((f) => (
                  <option key={f.id} value={f.id}>
                    {f.feature_key}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          {form.scope_type === 'node' && (
            <Field label="节点" required hint="节点范围需选择目标节点">
              <Select value={form.scope_id ?? ''} onChange={(e) => set('scope_id', e.target.value)}>
                <option value="">请选择节点</option>
                {nodes.map((n) => (
                  <option key={n.id ?? n.node_id} value={n.id ?? n.node_id}>
                    {nodeName(n)}
                  </option>
                ))}
              </Select>
            </Field>
          )}
          <Field label="content_yaml" required hint="YAML 文本，配置合并：Feature 默认 < Profile < 节点 Override">
            <textarea
              className="input mono"
              rows={10}
              value={form.content_yaml}
              onChange={(e) => set('content_yaml', e.target.value)}
              placeholder="key: value&#10;nested:&#10;  option: true"
              required
            />
          </Field>
        </div>
        {error && <Alert tone="danger">{error}</Alert>}
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost" onClick={onClose} disabled={busy}>取消</button>
          <button
            type="submit"
            className="btn btn-primary"
            disabled={busy || !form.name.trim() || !form.content_yaml.trim() || (form.scope_type === 'node' && !form.scope_id)}
          >
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

/* -------------------------------- 部署目标 -------------------------------- */

function TargetsTab({
  targetsState,
  targets,
  nodes,
  features,
  releases,
  configProfiles,
  isProd,
}: {
  targetsState: AsyncState<unknown>;
  targets: DeploymentTarget[];
  nodes: NodeInfo[];
  features: DeploymentFeature[];
  releases: DeploymentRelease[];
  configProfiles: DeploymentConfigProfile[];
  isProd: boolean;
}) {
  const confirm = useConfirm();
  const [modal, setModal] = useState<{ mode: 'create' } | { mode: 'edit'; target: DeploymentTarget } | null>(null);

  const releaseVersion = (id?: string | null) => (id ? releases.find((r) => r.id === id)?.version ?? '—' : '—');

  const remove = async (t: DeploymentTarget) => {
    const r = await confirm({
      title: '移除部署目标',
      message: (
        <div className="kv">
          <dt>节点</dt>
          <dd>{t.node_name || shortId(t.node_id)}</dd>
          <dt>Feature</dt>
          <dd className="mono">{t.feature_key || shortId(t.feature_id)}</dd>
        </div>
      ),
      confirmLabel: '确认移除',
      danger: true,
      production: isProd,
      requireReason: isProd,
      reasonLabel: '移除原因',
    });
    if (!r.ok) return;
    try {
      await deleteTarget(t.id);
      targetsState.reload();
    } catch (err) {
      window.alert(err instanceof ApiError ? err.message : '移除失败，请重试');
    }
  };

  return (
    <Card
      title="部署目标（纳入部署管理的节点）"
      actions={
        <>
          <button className="btn btn-ghost btn-sm" onClick={targetsState.reload}>刷新</button>
          <button className="btn btn-primary btn-sm" onClick={() => setModal({ mode: 'create' })}>新建目标</button>
        </>
      }
    >
      {targetsState.loading && targetsState.data === null ? (
        <LoadingState label="加载部署目标中…" />
      ) : targetsState.error ? (
        <ErrorState message={errorMessage(targetsState.error)} onRetry={targetsState.reload} />
      ) : targets.length === 0 ? (
        <EmptyState title="暂无部署目标" hint="将节点纳入部署管理（选择 Feature / 配置 Profile / 期望版本）。" />
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>Feature</th>
                <th>节点</th>
                <th>当前版本</th>
                <th>期望版本</th>
                <th>Last Healthy</th>
                <th>实际状态</th>
                <th>健康检查</th>
                <th>配置版本</th>
                <th>启用</th>
                <th style={{ width: 150 }}>操作</th>
              </tr>
            </thead>
            <tbody>
              {targets.map((t) => (
                <tr key={t.id}>
                  <td className="mono-cell">{t.feature_key || shortId(t.feature_id)}</td>
                  <td>{t.node_name || shortId(t.node_id)}</td>
                  <td className="mono">{releaseVersion(t.current_release_id)}</td>
                  <td className="mono">{releaseVersion(t.desired_release_id)}</td>
                  <td className="mono">
                    {t.last_healthy_release_id ? releaseVersion(t.last_healthy_release_id) : <span className="muted">—</span>}
                  </td>
                  <td>
                    <DeployBadge status={t.actual_status} />
                  </td>
                  <td>
                    <TimeCell value={t.last_health_check_at} />
                  </td>
                  <td className="num">{t.config_revision ?? '—'}</td>
                  <td>
                    {t.enabled ? <Badge tone="green">启用</Badge> : <Badge tone="gray">停用</Badge>}
                  </td>
                  <td>
                    <div className="btn-row">
                      <button className="btn btn-ghost btn-sm" onClick={() => setModal({ mode: 'edit', target: t })}>编辑</button>
                      <button className="btn btn-danger btn-sm" onClick={() => remove(t)}>移除</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {modal && (
        <TargetModal
          mode={modal.mode}
          target={modal.mode === 'edit' ? modal.target : null}
          nodes={nodes}
          features={features}
          releases={releases}
          configProfiles={configProfiles}
          onClose={() => setModal(null)}
          onSaved={() => {
            setModal(null);
            targetsState.reload();
          }}
        />
      )}
    </Card>
  );
}

function TargetModal({
  mode,
  target,
  nodes,
  features,
  releases,
  configProfiles,
  onClose,
  onSaved,
}: {
  mode: 'create' | 'edit';
  target: DeploymentTarget | null;
  nodes: NodeInfo[];
  features: DeploymentFeature[];
  releases: DeploymentRelease[];
  configProfiles: DeploymentConfigProfile[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const isEdit = mode === 'edit';
  const [form, setForm] = useState<CreateTargetBody>({
    feature_id: target?.feature_id ?? '',
    node_id: target?.node_id ?? '',
    config_profile_id: target?.config_profile_id ?? '',
    desired_release_id: target?.desired_release_id ?? '',
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const featureReleases = useMemo(
    () => (form.feature_id ? releases.filter((r) => r.feature_id === form.feature_id) : releases),
    [releases, form.feature_id],
  );

  const set = (key: keyof CreateTargetBody, value: string) => setForm((p) => ({ ...p, [key]: value }));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!form.feature_id || !form.node_id) return;
    setBusy(true);
    setError(null);
    try {
      const body: CreateTargetBody = {
        feature_id: form.feature_id,
        node_id: form.node_id,
        config_profile_id: form.config_profile_id || undefined,
        desired_release_id: form.desired_release_id || undefined,
      };
      if (isEdit && target) {
        await updateTarget(target.id, body);
      } else {
        await createTarget(body);
      }
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '保存失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open title={isEdit ? '编辑部署目标' : '新建部署目标'} onClose={() => !busy && onClose()} width={560}>
      <form onSubmit={submit}>
        <div>
          <div className="grid grid-2">
            <Field label="Feature" required>
              <Select value={form.feature_id} onChange={(e) => set('feature_id', e.target.value)} required>
                <option value="">请选择</option>
                {features.map((f) => (
                  <option key={f.id} value={f.id}>
                    {f.feature_key}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="节点" required>
              <Select value={form.node_id} onChange={(e) => set('node_id', e.target.value)} required>
                <option value="">请选择</option>
                {nodes.map((n) => (
                  <option key={n.id ?? n.node_id} value={n.id ?? n.node_id}>
                    {nodeName(n)}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          <Field label="Config Profile（可选）">
            <Select value={form.config_profile_id ?? ''} onChange={(e) => set('config_profile_id', e.target.value)}>
              <option value="">不指定</option>
              {configProfiles.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="期望 Release（可选）" hint="按所选 Feature 过滤">
            <Select value={form.desired_release_id ?? ''} onChange={(e) => set('desired_release_id', e.target.value)}>
              <option value="">不指定</option>
              {featureReleases.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.version}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        {error && <Alert tone="danger">{error}</Alert>}
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost" onClick={onClose} disabled={busy}>取消</button>
          <button type="submit" className="btn btn-primary" disabled={busy || !form.feature_id || !form.node_id}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

/* -------------------------------- Secret 配置 ------------------------------ */

function SecretsTab({
  secretsState,
  secrets,
  features,
  isProd,
}: {
  secretsState: AsyncState<unknown>;
  secrets: DeploymentSecretReference[];
  features: DeploymentFeature[];
  isProd: boolean;
}) {
  const confirm = useConfirm();
  const [secretId, setSecretId] = useState('');
  const [value, setValue] = useState('');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [viewBusy, setViewBusy] = useState(false);
  const [viewError, setViewError] = useState<string | null>(null);

  // 新建 Secret Reference（仅元数据；正文通过覆盖写入）
  const [createForm, setCreateForm] = useState<SecretReferenceCreate>({
    name: '',
    feature_id: features[0]?.id ?? '',
    scope_type: 'shared',
    scope_id: '',
  });
  const [createBusy, setCreateBusy] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [createDone, setCreateDone] = useState<string | null>(null);

  const selected = secrets.find((s) => s.id === secretId) ?? null;
  const needReason = isProd;

  const submitCreate = async (e: FormEvent) => {
    e.preventDefault();
    if (!createForm.name.trim() || !createForm.feature_id) return;
    if (createForm.scope_type === 'node' && !createForm.scope_id?.trim()) return;
    setCreateBusy(true);
    setCreateError(null);
    setCreateDone(null);
    try {
      const body: SecretReferenceCreate = {
        name: createForm.name.trim(),
        feature_id: createForm.feature_id,
        scope_type: createForm.scope_type,
        scope_id: createForm.scope_type === 'node' ? createForm.scope_id?.trim() || undefined : undefined,
      };
      await createSecretReference(body);
      setCreateForm((p) => ({ ...p, name: '', scope_id: '' }));
      setCreateDone('Secret Reference 已创建（version 0），可通过下方「覆盖写入」写入正文。');
      secretsState.reload();
    } catch (err) {
      setCreateError(err instanceof ApiError ? err.message : '创建失败，请重试');
    } finally {
      setCreateBusy(false);
    }
  };

  // 查看/编辑：读取当前明文到编辑框（策略放宽：仅 admin；响应 no-store；不缓存）
  const loadValue = async (id: string) => {
    setSecretId(id);
    setError(null);
    setDone(null);
    setViewError(null);
    setValue('');
    setViewBusy(true);
    try {
      const r = await getSecretValue(id);
      setValue(r.value ?? '');
    } catch (err) {
      setViewError(err instanceof ApiError ? err.message : '读取 Secret 失败，请重试');
    } finally {
      setViewBusy(false);
    }
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!secretId || !value) return;
    if (needReason && !reason.trim()) return;
    if (isProd) {
      const r = await confirm({
        title: '覆盖 Secret',
        message: (
          <div>
            <p>将覆盖 Secret「{selected?.name ?? ''}」并生成新 version。</p>
            <p className="muted" style={{ fontSize: 12 }}>V1 将敏感配置以明文文件写入私有 OSS，请确认操作风险。</p>
          </div>
        ),
        confirmLabel: '确认覆盖',
        danger: true,
        production: true,
      });
      if (!r.ok) return;
    }
    setBusy(true);
    setError(null);
    setDone(null);
    try {
      const body: OverwriteSecretBody = { value };
      if (reason.trim()) body.reason = reason.trim();
      await overwriteSecret(secretId, body);
      // 提交成功后立即清空输入，绝不保留/缓存 Secret 值。
      setValue('');
      setReason('');
      setDone('覆盖成功，已生成新版本。');
      secretsState.reload();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '覆盖失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      <Card title="Secret 引用列表" actions={<button className="btn btn-ghost btn-sm" onClick={secretsState.reload}>刷新</button>}>
        {secretsState.loading && secretsState.data === null ? (
          <LoadingState label="加载 Secret 引用中…" />
        ) : secretsState.error ? (
          <ErrorState message={errorMessage(secretsState.error)} onRetry={secretsState.reload} />
        ) : secrets.length === 0 ? (
          <EmptyState title="暂无 Secret 引用" hint="Secret 引用只含 object_key / version / hash / 加密模式等元数据，不含正文。" />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>名称</th>
                  <th>Feature</th>
                  <th>范围</th>
                  <th>Object Key</th>
                  <th>版本</th>
                  <th>加密模式</th>
                  <th>内容 Hash</th>
                  <th>大小</th>
                  <th>更新时间</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {secrets.map((s) => (
                  <tr key={s.id}>
                    <td>{s.name || '—'}</td>
                    <td className="mono-cell">
                      {features.find((f) => f.id === s.feature_id)?.feature_key ?? (s.feature_id ? shortId(s.feature_id) : '—')}
                    </td>
                    <td>
                      {SCOPE_LABEL[s.scope_type ?? ''] ?? s.scope_type ?? '—'}
                      {s.scope_id && <span className="mono muted" style={{ fontSize: 12 }}> {shortId(s.scope_id)}</span>}
                    </td>
                    <td className="mono muted" title={s.object_key ?? undefined}>{s.object_key ? shortId(s.object_key, 18) : '—'}</td>
                    <td className="num">{s.version ?? '—'}</td>
                    <td className="mono">{s.encryption_mode || 'none'}</td>
                    <td className="mono muted" title={s.content_hash ?? undefined}>{s.content_hash ? shortId(s.content_hash, 12) : '—'}</td>
                    <td className="num">{formatBytes(s.size)}</td>
                    <td>
                      <TimeCell value={s.updated_at} />
                    </td>
                    <td>
                      <button className="btn btn-ghost btn-sm" onClick={() => loadValue(s.id)} disabled={viewBusy}>
                        {viewBusy && secretId === s.id ? '加载中…' : '查看/编辑'}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card title="新建 Secret Reference（仅元数据）">
        <div className="alert alert-info" role="alert" style={{ marginBottom: 12 }}>
          仅登记引用元数据（object_key / version=0 / 加密模式），不含正文；创建后请通过下方「覆盖写入」写入 Secret 内容。
        </div>
        <form onSubmit={submitCreate}>
          <div className="grid grid-2">
            <Field label="名称" required hint="如 db / redis / app_secret">
              <TextInput
                value={createForm.name}
                onChange={(e) => setCreateForm((p) => ({ ...p, name: e.target.value }))}
                placeholder="db"
                required
              />
            </Field>
            <Field label="Feature" required>
              <Select
                value={createForm.feature_id}
                onChange={(e) => setCreateForm((p) => ({ ...p, feature_id: e.target.value }))}
                required
              >
                <option value="">请选择</option>
                {features.map((f) => (
                  <option key={f.id} value={f.id}>
                    {f.feature_key}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="范围类型" required>
              <Select
                value={createForm.scope_type}
                onChange={(e) => setCreateForm((p) => ({ ...p, scope_type: e.target.value as SecretReferenceCreate['scope_type'] }))}
              >
                <option value="shared">共享（shared）</option>
                <option value="node">节点（node）</option>
              </Select>
            </Field>
            {createForm.scope_type === 'node' && (
              <Field label="节点 ID" required hint="node 范围必填">
                <TextInput
                  value={createForm.scope_id ?? ''}
                  onChange={(e) => setCreateForm((p) => ({ ...p, scope_id: e.target.value }))}
                  placeholder="node_id"
                  required
                />
              </Field>
            )}
          </div>
          {createError && <Alert tone="danger">{createError}</Alert>}
          {createDone && <Alert tone="success">{createDone}</Alert>}
          <div className="modal-actions" style={{ marginTop: 8 }}>
            <button
              type="submit"
              className="btn btn-primary"
              disabled={createBusy || !createForm.name.trim() || !createForm.feature_id || (createForm.scope_type === 'node' && !createForm.scope_id?.trim())}
            >
              {createBusy ? '创建中…' : '新建引用'}
            </button>
          </div>
        </form>
      </Card>

      <Card title="查看 / 编辑（Overwrite）">
        <div className="alert alert-warn" role="alert" style={{ marginBottom: 12 }}>
          ⚠️ <strong>V1 将敏感配置以明文文件写入私有 OSS</strong>。此处可读取当前值并编辑（策略已放宽，
          读取仅管理员、响应不缓存、每次查看落审计）；提交后输入框立即清空，不缓存、不入 localStorage/URL。
        </div>
        <form onSubmit={submit}>
          <div className="grid grid-2">
            <Field label="Secret 引用" required>
              <Select value={secretId} onChange={(e) => { setSecretId(e.target.value); setError(null); setDone(null); setViewError(null); setValue(''); }}>
                <option value="">请选择</option>
                {secrets.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="操作" hint="读取当前明文到编辑框后再修改">
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <button type="button" className="btn btn-ghost" onClick={() => secretId && loadValue(secretId)} disabled={viewBusy || !secretId}>
                  {viewBusy ? '加载中…' : '加载当前值'}
                </button>
                {viewError && <span style={{ color: 'var(--danger, #d64545)', fontSize: 12 }}>{viewError}</span>}
              </div>
            </Field>
            <Field label={needReason ? '原因（正式环境必填）' : '原因'} required={needReason} hint="将写入审计日志">
              <TextInput value={reason} onChange={(e) => setReason(e.target.value)} placeholder={needReason ? '请填写覆盖原因' : '可选'} />
            </Field>
          </div>
          <Field label="Secret 内容（可查看/编辑）" required hint="可先点「加载当前值」再修改；提交后立即清空，不缓存、不入 localStorage/URL">
            <textarea
              className="input mono"
              rows={8}
              value={value}
              onChange={(e) => setValue(e.target.value)}
              placeholder="粘贴新的 Secret 内容（如 YAML）"
              autoComplete="off"
            />
          </Field>
          {error && <Alert tone="danger">{error}</Alert>}
          {done && <Alert tone="success">{done}</Alert>}
          <div className="modal-actions" style={{ marginTop: 8 }}>
            <button type="submit" className="btn btn-primary" disabled={busy || !secretId || !value || (needReason && !reason.trim())}>
              {busy ? '覆盖中…' : '覆盖写入'}
            </button>
          </div>
        </form>
      </Card>
    </div>
  );
}

/* --------------------------------- Operations ------------------------------ */

function OperationsTab({
  opsState,
  operations,
  backupsState,
  backups,
  features,
  releases,
  targets,
  isProd,
}: {
  opsState: AsyncState<unknown>;
  operations: DeploymentOperation[];
  backupsState: AsyncState<unknown>;
  backups: DeploymentBackup[];
  features: DeploymentFeature[];
  releases: DeploymentRelease[];
  targets: DeploymentTarget[];
  isProd: boolean;
}) {
  const confirm = useConfirm();
  const [createOpen, setCreateOpen] = useState(false);
  const [backupBusy, setBackupBusy] = useState(false);
  const [detailId, setDetailId] = useState<string | null>(null);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      <Card
        title="操作记录（串行 / 失败停止）"
        actions={
          <>
            <button className="btn btn-ghost btn-sm" onClick={opsState.reload}>刷新</button>
            <button className="btn btn-primary btn-sm" onClick={() => setCreateOpen(true)} disabled={features.length === 0 || targets.length === 0}>
              新建操作
            </button>
            <button
              className="btn btn-ghost btn-sm"
              disabled={targets.length === 0 || backupBusy}
              onClick={async () => {
                if (isProd) {
                  const r = await confirm({ title: '全部备份', message: '将为所有已关联 Target 创建备份操作（每个 Feature 一个）。', confirmLabel: '确认备份', production: true });
                  if (!r.ok) return;
                }
                setBackupBusy(true);
                try {
                  await runBackupsForNode({});
                  opsState.reload();
                  backupsState.reload();
                  window.alert('全部备份已触发（后台串行执行）。');
                } catch (err) {
                  window.alert(err instanceof ApiError ? err.message : '触发备份失败');
                } finally {
                  setBackupBusy(false);
                }
              }}
            >
              {backupBusy ? '触发中…' : '全部备份'}
            </button>
          </>
        }
      >
        {opsState.loading && opsState.data === null ? (
          <LoadingState label="加载操作记录中…" />
        ) : opsState.error ? (
          <ErrorState message={errorMessage(opsState.error)} onRetry={opsState.reload} />
        ) : operations.length === 0 ? (
          <EmptyState title="暂无操作记录" hint="创建安装 / 更新 / 备份 / 回滚操作后此处显示进度。" />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>ID</th>
                  <th>动作</th>
                  <th>Feature</th>
                  <th>策略</th>
                  <th>状态</th>
                  <th>发起者</th>
                  <th>创建时间</th>
                  <th>开始 / 结束</th>
                </tr>
              </thead>
              <tbody>
                {operations.map((op) => (
                  <tr key={op.id} className="clickable" onClick={() => setDetailId(op.id)}>
                    <td className="mono-cell" title={op.id}>{shortId(op.id)}</td>
                    <td>
                      <Badge tone="indigo">{ACTION_LABEL[op.action ?? ''] ?? op.action ?? '—'}</Badge>
                    </td>
                    <td className="mono-cell">{op.feature_key || shortId(op.feature_id)}</td>
                    <td className="mono">{op.strategy || 'serial'}</td>
                    <td>
                      <DeployBadge status={op.status} />
                    </td>
                    <td>{op.requested_by || '—'}</td>
                    <td>
                      <TimeCell value={op.created_at} />
                    </td>
                    <td>
                      <TimeCell value={op.started_at} /> / <TimeCell value={op.finished_at} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card
        title="Backup 记录"
        actions={<button className="btn btn-ghost btn-sm" onClick={backupsState.reload}>刷新</button>}
      >
        {backupsState.loading && backupsState.data === null ? (
          <LoadingState label="加载备份记录中…" />
        ) : backupsState.error ? (
          <ErrorState message={errorMessage(backupsState.error)} onRetry={backupsState.reload} />
        ) : backups.length === 0 ? (
          <EmptyState title="暂无备份记录" hint="备份对象写入独立 backups/ 前缀。" />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>ID</th>
                  <th>操作</th>
                  <th>节点</th>
                  <th>Feature</th>
                  <th>模式</th>
                  <th>Object Key</th>
                  <th>大小</th>
                  <th>SHA256</th>
                  <th>状态</th>
                  <th>创建时间</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {backups.map((b) => (
                  <tr key={b.id}>
                    <td className="mono-cell" title={b.id}>{shortId(b.id)}</td>
                    <td className="mono muted">{b.operation_id ? shortId(b.operation_id) : '—'}</td>
                    <td className="mono muted">{b.node_id ? shortId(b.node_id) : '—'}</td>
                    <td className="mono-cell">{b.feature_key || shortId(b.feature_id)}</td>
                    <td className="mono">{b.backup_mode || '—'}</td>
                    <td className="mono muted" title={b.object_key ?? undefined}>{b.object_key ? shortId(b.object_key, 18) : '—'}</td>
                    <td className="num">{formatBytes(b.size)}</td>
                    <td className="mono muted" title={b.sha256 ?? undefined}>{b.sha256 ? shortId(b.sha256, 12) : '—'}</td>
                    <td>
                      <DeployBadge status={b.status} />
                    </td>
                    <td>
                      <TimeCell value={b.created_at} />
                    </td>
                    <td>
                      <button
                        className="btn btn-ghost btn-sm"
                        disabled={b.status !== 'succeeded' || b.backup_mode === 'none'}
                        title={b.status !== 'succeeded' ? '仅成功备份可恢复' : undefined}
                        onClick={async () => {
                          if (isProd) {
                            const r = await confirm({
                              title: '恢复备份',
                              message: `将从备份恢复 Feature「${b.feature_key}」的数据到该节点（数据已存在时需在新建操作里勾选 force_delete）。`,
                              confirmLabel: '确认恢复',
                              danger: true,
                              production: true,
                            });
                            if (!r.ok) return;
                          }
                          try {
                            await createOperation({
                              action: 'restore',
                              feature_id: b.feature_id ?? '',
                              backup_id: b.id,
                              target_ids: b.target_id ? [String(b.target_id)] : [],
                              reason: '从备份恢复',
                            });
                            opsState.reload();
                            window.alert('恢复操作已创建（后台执行）。');
                          } catch (err) {
                            window.alert(err instanceof ApiError ? err.message : '创建恢复操作失败');
                          }
                        }}
                      >
                        恢复
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {createOpen && (
        <CreateOperationModal
          features={features}
          releases={releases}
          targets={targets}
          backups={backups}
          isProd={isProd}
          onClose={() => setCreateOpen(false)}
          onCreated={() => {
            setCreateOpen(false);
            opsState.reload();
          }}
        />
      )}
      {detailId && (
        <OperationDetailModal
          id={detailId}
          isProd={isProd}
          targets={targets}
          onClose={() => setDetailId(null)}
          onChanged={() => {
            opsState.reload();
            backupsState.reload();
          }}
        />
      )}
    </div>
  );
}

function CreateOperationModal({
  features,
  releases,
  targets,
  backups,
  isProd,
  onClose,
  onCreated,
}: {
  features: DeploymentFeature[];
  releases: DeploymentRelease[];
  targets: DeploymentTarget[];
  backups: DeploymentBackup[];
  isProd: boolean;
  onClose: () => void;
  onCreated: () => void;
}) {
  const confirm = useConfirm();
  const [form, setForm] = useState<CreateOperationBody>({
    action: 'install',
    feature_id: features[0]?.id ?? '',
    release_id: '',
    target_ids: [],
    reason: '',
  });
  const [allTargets, setAllTargets] = useState(true); // 空 target_ids = 全部已关联 Target
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const action = form.action;
  const needRelease = action === 'install' || action === 'update' || action === 'rollback';
  const needBackup = action === 'restore';
  const featureTargets = useMemo(
    () => (form.feature_id ? targets.filter((t) => t.feature_id === form.feature_id) : targets),
    [targets, form.feature_id],
  );
  const featureReleases = useMemo(
    () => (form.feature_id ? releases.filter((r) => r.feature_id === form.feature_id) : releases),
    [releases, form.feature_id],
  );

  const set = (key: keyof CreateOperationBody, value: string | string[]) =>
    setForm((p) => ({ ...p, [key]: value } as CreateOperationBody));

  const toggleAll = (on: boolean) => {
    setAllTargets(on);
    setForm((p) => ({ ...p, target_ids: on ? [] : Array.from(new Set(featureTargets.map((t) => t.id))) }));
  };

  const toggleTarget = (id: string, on: boolean) => {
    setForm((p) => {
      const next = new Set(p.target_ids);
      if (on) next.add(id);
      else next.delete(id);
      return { ...p, target_ids: Array.from(next) };
    });
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!form.feature_id) return;
    if (needRelease && !form.release_id) return;
    if (needBackup && !form.backup_id) return;
    if (isProd && !form.reason?.trim()) return;
    if (isProd) {
      const targetDesc = allTargets || form.target_ids.length === 0 ? '全部已关联 Target' : `${form.target_ids.length} 个目标`;
      const r = await confirm({
        title: '创建部署操作',
        message: `将以「${ACTION_LABEL[action] ?? action}」方式对 ${targetDesc} 执行（多节点串行、失败停止；非最新节点自动跳过）。`,
        confirmLabel: '确认创建',
        production: true,
      });
      if (!r.ok) return;
    }
    setBusy(true);
    setError(null);
    try {
      const body: CreateOperationBody = {
        action: form.action,
        feature_id: form.feature_id,
        target_ids: allTargets ? [] : form.target_ids,
        reason: form.reason?.trim() || undefined,
      };
      if (form.release_id) body.release_id = form.release_id;
      if (form.node_id) body.node_id = form.node_id;
      if (form.backup_id) body.backup_id = form.backup_id;
      if (form.force_delete) body.force_delete = true;
      await createOperation(body);
      onCreated();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '创建失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open title="新建部署操作" onClose={() => !busy && onClose()} width={640}>
      <form onSubmit={submit}>
        <div>
          <div className="grid grid-2">
            <Field label="动作" required>
              <Select value={action} onChange={(e) => set('action', e.target.value as OperationAction)}>
                {(['install', 'update', 'backup', 'rollback', 'health_check'] as OperationAction[]).map((a) => (
                  <option key={a} value={a}>
                    {ACTION_LABEL[a]}（{a}）
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Feature" required>
              <Select value={form.feature_id} onChange={(e) => set('feature_id', e.target.value)}>
                <option value="">请选择</option>
                {features.map((f) => (
                  <option key={f.id} value={f.id}>
                    {f.feature_key}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          {needBackup && (
            <Field label="备份（Restore 数据源）" required hint="仅可恢复 status=succeeded 且非 none 备份">
              <Select value={form.backup_id ?? ''} onChange={(e) => set('backup_id', e.target.value)} required>
                <option value="">请选择备份</option>
                {backups
                  .filter((b) => b.feature_id === form.feature_id && b.status === 'succeeded' && b.backup_mode !== 'none')
                  .map((b) => (
                    <option key={b.id} value={b.id}>
                      {b.feature_key} / {(b.node_id ?? '').slice(0, 8)} / {b.created_at ? new Date(b.created_at).toLocaleString() : '—'}
                    </option>
                  ))}
              </Select>
            </Field>
          )}
          {needBackup && (
            <Field label="force_delete（数据已存在时）" hint="勾选后恢复前先删除/迁移目标已有数据">
              <label style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <input type="checkbox" checked={form.force_delete ?? false} onChange={(e) => setForm((p) => ({ ...p, force_delete: e.target.checked }))} />
                允许覆盖已有数据
              </label>
            </Field>
          )}
          <Field label={needRelease ? 'Release' : 'Release（备份 / 健康检查 / 恢复可选）'}>
            <Select value={form.release_id ?? ''} onChange={(e) => set('release_id', e.target.value)}>
              <option value="">{needRelease ? '请选择' : '不指定'}</option>
              {featureReleases.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.version}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="目标节点（按 Feature 过滤）" required hint={allTargets ? '全部已关联 Target（非最新自动跳过）' : `已选 ${form.target_ids.length} 个目标`}>
            <div style={{ marginBottom: 8 }}>
              <label style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <input type="checkbox" checked={allTargets} onChange={(e) => toggleAll(e.target.checked)} />
                全部（该 Feature 已关联到服务器的 Target）
              </label>
            </div>
            {featureTargets.length === 0 ? (
              <span className="muted" style={{ fontSize: 13 }}>该 Feature 下暂无部署目标，请先在「部署目标」页签创建。</span>
            ) : (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 6, maxHeight: 220, overflow: 'auto' }}>
                {featureTargets.map((t) => (
                  <Checkbox
                    key={t.id}
                    label={
                      <span>
                        {t.node_name || shortId(t.node_id)}
                        <span className="mono muted" style={{ fontSize: 12, marginLeft: 8 }}>{t.feature_key}</span>
                      </span>
                    }
                    checked={form.target_ids.includes(t.id)}
                    onChange={(v) => toggleTarget(t.id, v)}
                    disabled={busy}
                  />
                ))}
              </div>
            )}
          </Field>
          <Field label={isProd ? '原因（正式环境必填）' : '原因'} required={isProd} hint="将写入审计日志">
            <textarea
              className="input"
              rows={3}
              value={form.reason ?? ''}
              onChange={(e) => set('reason', e.target.value)}
              placeholder={isProd ? '请填写操作原因' : '可选'}
            />
          </Field>
        </div>
        {error && <Alert tone="danger">{error}</Alert>}
        <div className="modal-actions">
          <button type="button" className="btn btn-ghost" onClick={onClose} disabled={busy}>取消</button>
          <button
            type="submit"
            className="btn btn-primary"
            disabled={busy || !form.feature_id || (needRelease && !form.release_id) || (needBackup && !form.backup_id) || (isProd && !form.reason?.trim())}
          >
            {busy ? '创建中…' : '创建操作'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function OperationDetailModal({
  id,
  isProd,
  targets,
  onClose,
  onChanged,
}: {
  id: string;
  isProd: boolean;
  targets: DeploymentTarget[];
  onClose: () => void;
  onChanged: () => void;
}) {
  const confirm = useConfirm();
  const navigate = useNavigate();
  const detailState = useApi<DeploymentOperationDetail>(`/deployments/operations/${id}`, { pollIntervalMs: 5000 });

  const data = detailState.data;
  const op = data?.operation ?? null;
  const status = (op?.status ?? '').toLowerCase();
  const terminal = ['succeeded', 'failed', 'cancelled', 'rolled_back', 'rollback_failed'].includes(status);
  const canContinue = ['draft', 'validated', 'awaiting_confirmation', 'failed', 'partial_failed'].includes(status);
  const canRollback = op?.feature_id != null && (data?.targets.length ?? 0) > 0 && ['partial_failed', 'failed', 'rolled_back', 'rollback_failed'].includes(status);

  const doContinue = async () => {
    if (isProd) {
      const r = await confirm({ title: '继续执行操作', message: '确认后将继续推进该部署操作。', confirmLabel: '继续', production: true });
      if (!r.ok) return;
    }
    try {
      await continueOperation(id);
      detailState.reload();
      onChanged();
    } catch (err) {
      window.alert(err instanceof ApiError ? err.message : '继续失败，请重试');
    }
  };

  const doCancel = async () => {
    const r = await confirm({
      title: '取消操作',
      message: '将请求取消该部署操作（含进行中的节点任务）。',
      confirmLabel: '确认取消',
      danger: true,
      production: isProd,
      requireReason: isProd,
      reasonLabel: '取消原因',
    });
    if (!r.ok) return;
    try {
      await cancelOperation(id, { reason: r.reason });
      detailState.reload();
      onChanged();
    } catch (err) {
      window.alert(err instanceof ApiError ? err.message : '取消失败，请重试');
    }
  };

  const doRollback = async () => {
    if (!op?.feature_id || !data) return;
    // 手动回滚需要 release_id（后端 install/update/rollback 必填）：从部署目标
    // 的 last_healthy_release_id 取回滚目标版本。
    const healthyIds = new Set<string>();
    for (const t of data.targets) {
      const target = targets.find((x) => x.id === t.target_id);
      if (target?.last_healthy_release_id) healthyIds.add(target.last_healthy_release_id);
    }
    if (healthyIds.size === 0) {
      window.alert('未找到 last healthy 版本，无法自动回滚；请在「新建操作」中选择回滚目标版本。');
      return;
    }
    if (healthyIds.size > 1) {
      window.alert('目标节点的 last healthy 版本不一致，请在「新建操作」中手动选择回滚版本。');
      return;
    }
    const releaseId = [...healthyIds][0];
    const r = await confirm({
      title: '回滚',
      message: `将创建回滚操作（feature：${op.feature_key || shortId(op.feature_id)}，目标：${data.targets.length} 个），恢复至 last healthy 版本（${releaseId ? shortId(releaseId) : '—'}）。`,
      confirmLabel: '确认回滚',
      danger: true,
      production: isProd,
      requireReason: isProd,
      reasonLabel: '回滚原因',
    });
    if (!r.ok) return;
    try {
      await createOperation({
        action: 'rollback',
        feature_id: op.feature_id,
        release_id: releaseId,
        target_ids: data.targets.map((t) => t.target_id),
        reason: r.reason,
      });
      onChanged();
      onClose();
    } catch (err) {
      window.alert(err instanceof ApiError ? err.message : '回滚失败，请重试');
    }
  };

  return (
    <Modal open title={`操作详情：${shortId(id)}`} onClose={onClose} width={860}>
      {detailState.loading && data === null ? (
        <LoadingState label="加载操作详情中…" />
      ) : detailState.error ? (
        <ErrorState message={errorMessage(detailState.error)} onRetry={detailState.reload} />
      ) : !data || !op ? null : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <div className="kv kv-2col">
            <dt>状态</dt>
            <dd>
              <DeployBadge status={op.status} />
            </dd>
            <dt>动作</dt>
            <dd>{ACTION_LABEL[op.action ?? ''] ?? op.action ?? '—'}</dd>
            <dt>Feature</dt>
            <dd className="mono">{op.feature_key || shortId(op.feature_id)}</dd>
            <dt>策略</dt>
            <dd className="mono">{op.strategy || 'serial'}</dd>
            <dt>发起者</dt>
            <dd>{op.requested_by || '—'}</dd>
            <dt>环境</dt>
            <dd className="mono">{op.environment_id || '—'}</dd>
            <dt>冻结配置 Hash</dt>
            <dd className="mono" title={op.frozen_config_hash ?? undefined}>{op.frozen_config_hash ? shortId(op.frozen_config_hash, 16) : '—'}</dd>
            <dt>创建 / 开始 / 结束</dt>
            <dd>
              <TimeCell value={op.created_at} /> / <TimeCell value={op.started_at} /> / <TimeCell value={op.finished_at} />
            </dd>
          </div>

          <div>
            <h3 className="card-title" style={{ marginBottom: 8 }}>目标进度（{data.targets.length}）</h3>
            {data.targets.length === 0 ? (
              <EmptyState title="暂无目标进度" />
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <thead>
                    <tr>
                      <th>节点</th>
                      <th>状态</th>
                      <th>当前版本</th>
                      <th>期望版本</th>
                      <th>失败原因</th>
                      <th>开始 / 结束</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.targets.map((t) => (
                      <tr key={t.id}>
                        <td className="mono muted">{shortId(t.node_id)}</td>
                        <td>
                          <DeployBadge status={t.status} />
                        </td>
                        <td className="mono">{t.current_release_id ? shortId(t.current_release_id) : '—'}</td>
                        <td className="mono">{t.desired_release_id ? shortId(t.desired_release_id) : '—'}</td>
                        <td>
                          <span className="muted" style={{ fontSize: 12 }}>{t.error_message || '—'}</span>
                        </td>
                        <td>
                          <TimeCell value={t.started_at} /> / <TimeCell value={t.finished_at} />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <div>
            <h3 className="card-title" style={{ marginBottom: 8 }}>节点步骤（{data.steps.length}）</h3>
            {data.steps.length === 0 ? (
              <EmptyState title="暂无步骤" />
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <thead>
                    <tr>
                      <th>节点</th>
                      <th>步骤</th>
                      <th>状态</th>
                      <th>Task</th>
                      <th>消息</th>
                      <th>开始 / 结束</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.steps.map((s) => (
                      <tr key={s.id}>
                        <td className="mono muted">{s.node_id ? shortId(s.node_id) : '—'}</td>
                        <td className="mono">{s.step_type || '—'}</td>
                        <td>
                          <DeployBadge status={s.status} />
                        </td>
                        <td>
                          {s.task_id ? (
                            <button className="btn btn-ghost btn-sm" onClick={() => navigate(`/tasks/${s.task_id}`)}>
                              {shortId(s.task_id)} ↗
                            </button>
                          ) : (
                            '—'
                          )}
                        </td>
                        <td>
                          <span className="muted" style={{ fontSize: 12 }}>{s.message || '—'}</span>
                        </td>
                        <td>
                          <TimeCell value={s.started_at} /> / <TimeCell value={s.finished_at} />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <div className="btn-row">
            {canContinue && (
              <button className="btn btn-primary" onClick={doContinue}>继续执行</button>
            )}
            {!terminal && (
              <button className="btn btn-danger" onClick={doCancel}>取消操作</button>
            )}
            {canRollback && (
              <button className="btn btn-danger" onClick={doRollback}>回滚</button>
            )}
            <button className="btn btn-ghost" onClick={detailState.reload}>刷新</button>
            <button className="btn btn-ghost" onClick={onClose}>关闭</button>
          </div>
        </div>
      )}
    </Modal>
  );
}

/* ------------------------------ Bootstrap Sessions ------------------------- */

function BootstrapTab({
  sessionsState,
  sessions,
  nodes,
  isProd,
}: {
  sessionsState: AsyncState<unknown>;
  sessions: BootstrapSession[];
  nodes: NodeInfo[];
  isProd: boolean;
}) {
  const confirm = useConfirm();
  const [createOpen, setCreateOpen] = useState(false);

  const revoke = async (s: BootstrapSession) => {
    const r = await confirm({
      title: '撤销引导会话',
      message: (
        <div className="kv">
          <dt>节点</dt>
          <dd className="mono">{s.node_id ? shortId(s.node_id) : '—'}</dd>
          <dt>Bucket / Prefix</dt>
          <dd className="mono">{s.bucket || '—'} / {s.prefix || '—'}</dd>
        </div>
      ),
      confirmLabel: '确认撤销',
      danger: true,
      production: isProd,
      requireReason: isProd,
      reasonLabel: '撤销原因',
    });
    if (!r.ok) return;
    try {
      await revokeBootstrapSession(s.id);
      sessionsState.reload();
    } catch (err) {
      window.alert(err instanceof ApiError ? err.message : '撤销失败，请重试');
    }
  };

  return (
    <Card
      title="节点引导会话（Bootstrap Session）"
      actions={
        <>
          <button className="btn btn-ghost btn-sm" onClick={sessionsState.reload}>刷新</button>
          <button className="btn btn-primary btn-sm" onClick={() => setCreateOpen(true)}>新建引导会话</button>
        </>
      }
    >
      {sessionsState.loading && sessionsState.data === null ? (
        <LoadingState label="加载引导会话中…" />
      ) : sessionsState.error ? (
        <ErrorState message={errorMessage(sessionsState.error)} onRetry={sessionsState.reload} />
      ) : sessions.length === 0 ? (
        <EmptyState title="暂无引导会话" hint="新建会话后生成一次性引导脚本（command / token），用于将节点纳入部署管理。" />
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>ID</th>
                <th>节点</th>
                <th>状态</th>
                <th>Bucket</th>
                <th>Prefix</th>
                <th>Region</th>
                <th>创建时间</th>
                <th>过期时间</th>
                <th>撤销时间</th>
                <th style={{ width: 120 }}>操作</th>
              </tr>
            </thead>
            <tbody>
              {sessions.map((s) => (
                <tr key={s.id}>
                  <td className="mono-cell" title={s.id}>{shortId(s.id)}</td>
                  <td className="mono muted">{s.node_id ? shortId(s.node_id) : '—'}</td>
                  <td>
                    <DeployBadge status={s.status} />
                  </td>
                  <td className="mono">{s.bucket || '—'}</td>
                  <td className="mono muted">{s.prefix || '—'}</td>
                  <td className="mono">{s.region || '—'}</td>
                  <td>
                    <TimeCell value={s.created_at} />
                  </td>
                  <td>
                    <TimeCell value={s.expires_at} />
                  </td>
                  <td>
                    <TimeCell value={s.revoked_at} />
                  </td>
                  <td>
                    {!s.revoked_at ? (
                      <button className="btn btn-danger btn-sm" onClick={() => revoke(s)}>撤销</button>
                    ) : (
                      <span className="muted" style={{ fontSize: 12 }}>已撤销</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {createOpen && (
        <CreateBootstrapModal
          nodes={nodes}
          onClose={() => setCreateOpen(false)}
          onCreated={() => {
            setCreateOpen(false);
            sessionsState.reload();
          }}
        />
      )}
    </Card>
  );
}

function CreateBootstrapModal({
  nodes,
  onClose,
  onCreated,
}: {
  nodes: NodeInfo[];
  onClose: () => void;
  onCreated: () => void;
}) {
  const [form, setForm] = useState({
    node_id: nodes[0]?.id ?? nodes[0]?.node_id ?? '',
    bucket: '',
    prefix: '',
    region: '',
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<{ command: string; token: string; session: BootstrapSession } | null>(null);

  const set = (key: keyof typeof form, value: string) => setForm((p) => ({ ...p, [key]: value }));

  const create = async () => {
    if (!form.node_id) return;
    setBusy(true);
    setError(null);
    try {
      const body: CreateBootstrapSessionBody = {
        node_id: form.node_id,
        bucket: form.bucket.trim() || undefined,
        prefix: form.prefix.trim() || undefined,
        region: form.region.trim() || undefined,
      };
      const res = await createBootstrapSession(body);
      setResult({ command: res.command, token: res.token, session: res.session });
      onCreated();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '创建失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open title="新建节点引导会话" onClose={() => !busy && onClose()} width={680}>
      <div>
        {!result ? (
          <div>
            <Alert tone="warn">
              创建后将生成一次性引导脚本（command + token）。<strong>token 仅此一次返回</strong>，
              关闭弹窗后无法再次查看；请立即复制并妥善保存。
            </Alert>
            {error && <Alert tone="danger">{error}</Alert>}
            <div className="form-grid">
              <Field label="引导节点" required hint="后端将按该节点生成会话">
                <Select value={form.node_id} onChange={(e) => set('node_id', e.target.value)} required>
                  <option value="">请选择</option>
                  {nodes.map((n) => (
                    <option key={n.id ?? n.node_id} value={n.id ?? n.node_id}>
                      {nodeName(n)}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field label="Bucket" hint="引导用 OSS Bucket（可选）">
                <TextInput value={form.bucket} onChange={(e) => set('bucket', e.target.value)} placeholder="如 deployment-repository" />
              </Field>
              <Field label="Prefix" hint="引导用 OSS Prefix（可选）">
                <TextInput value={form.prefix} onChange={(e) => set('prefix', e.target.value)} placeholder="如 bootstrap/" />
              </Field>
              <Field label="Region" hint="OSS Region（可选）">
                <TextInput value={form.region} onChange={(e) => set('region', e.target.value)} placeholder="如 oss-cn-hangzhou" />
              </Field>
            </div>
            <div className="modal-actions">
              <button type="button" className="btn btn-ghost" onClick={onClose} disabled={busy}>取消</button>
              <button type="button" className="btn btn-primary" onClick={create} disabled={busy || !form.node_id}>
                {busy ? '创建中…' : '创建并生成'}
              </button>
            </div>
          </div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <Alert tone="success">会话已创建：{shortId(result.session.id)}（{result.session.status || 'created'}）。</Alert>
            <div>
              <div className="filter-label" style={{ marginBottom: 4 }}>一次性引导命令（command）</div>
              <div
                style={{
                  background: 'var(--bg-2, #f1f5f9)',
                  border: '1px solid var(--border, #e2e8f0)',
                  borderRadius: 8,
                  padding: '10px 12px',
                  overflowX: 'auto',
                  marginBottom: 6,
                }}
              >
                <pre style={{ margin: 0, fontFamily: 'var(--mono, monospace)', fontSize: 12.5, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{result.command}</pre>
              </div>
              <button className="btn btn-ghost btn-sm" onClick={() => copyText(result.command)}>复制命令</button>
            </div>
            <div>
              <div className="filter-label" style={{ marginBottom: 4 }}>一次性 Token（仅此一次显示）</div>
              <div
                style={{
                  background: 'var(--bg-2, #f1f5f9)',
                  border: '1px solid var(--border, #e2e8f0)',
                  borderRadius: 8,
                  padding: '10px 12px',
                  overflowX: 'auto',
                  marginBottom: 6,
                }}
              >
                <pre style={{ margin: 0, fontFamily: 'var(--mono, monospace)', fontSize: 12.5, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{result.token}</pre>
              </div>
              <button className="btn btn-ghost btn-sm" onClick={() => copyText(result.token)}>复制 Token</button>
            </div>
            {error && <Alert tone="danger">{error}</Alert>}
            <div className="modal-actions">
              <button type="button" className="btn btn-primary" onClick={onClose}>完成</button>
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}
