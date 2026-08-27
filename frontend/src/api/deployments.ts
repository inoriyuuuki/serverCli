/**
 * 部署管理 API 封装。契约：doc/13_DEPLOYMENT_MANAGEMENT.md §9（API 清单）。
 * - 前缀 /api/v1/deployments/*，Admin 会话认证；写操作由 client.ts 自动附加 CSRF。
 * - 安全约束（§9.1 / 16 号文档）：Secret 正文与 OSS AK 只写不读；前端绝不缓存、
 *   绝不写入 localStorage/URL/参数历史，覆盖表单提交后由调用方清空输入。
 */
import { request, type RequestOptions } from './client';
import type {
  BootstrapSessionCreateResponse,
  BootstrapSessionListResponse,

  DeploymentBackupListResponse,
  DeploymentBackupResponse,
  DeploymentConfigProfileListResponse,
  DeploymentConfigProfileResponse,
  DeploymentFeatureListResponse,
  DeploymentFeatureResponse,
  DeploymentOperationDetail,
  DeploymentOperationListResponse,
  DeploymentOperationResponse,
  DeploymentReleaseListResponse,
  DeploymentReleaseResponse,
  DeploymentSecretReferenceListResponse,
  DeploymentSecretResponse,
  DeploymentTargetListResponse,
  DeploymentTargetResponse,
  OSSProfileListResponse,
  OSSProfileResponse,
  OSSTestResponse,
  RunBackupsResponse,
  RepositorySyncResponse,
  SecretReferenceCreate,
  SecretValueResponse,
} from './types';

/** 统一的 apiFetch 封装（等价于 client.ts 导出的 request，保持契约写法一致）。 */
async function apiFetch<T = unknown>(path: string, options: RequestOptions = {}): Promise<T> {
  return request<T>(path, options);
}

/* ------------------------------ 请求体类型 ------------------------------ */

export interface CreateFeatureBody {
  feature_key: string;
  name?: string;
  description?: string;
  backup_mode?: string;
  rollback_capability?: string;
  minimum_agent_version?: string;
  config_schema_json?: unknown;
}

export interface CreateReleaseBody {
  feature_id: string;
  version: string;
  source_commit?: string;
  /** 制品在 OSS 仓库中的 object_key，须位于 deployment-repository/ 前缀内。 */
  object_key: string;
  /** bundle 的 SHA-256（64 位小写 hex）。 */
  sha256: string;
  /** bundle 大小（字节），必须 > 0。 */
  size: number;
  install_hook?: string;
  update_hook?: string;
  backup_hook?: string;
  health_hook?: string;
  rollback_hook?: string;
  backup_mode?: string;
}

export interface CreateOSSProfileBody {
  name: string;
  endpoint: string;
  region?: string;
  bucket: string;
  prefix?: string;
  access_key_id: string;
  access_key_secret: string;
}

/** 更新 OSS Profile：AK/Secret 只写不读，留空表示不修改。 */
export interface UpdateOSSProfileBody {
  name?: string;
  endpoint?: string;
  region?: string;
  bucket?: string;
  prefix?: string;
  access_key_id?: string;
  access_key_secret?: string;
}

export interface CreateConfigProfileBody {
  name: string;
  scope_type: string;
  scope_id?: string;
  feature_id?: string;
  content_yaml: string;
}

export interface CreateTargetBody {
  feature_id: string;
  node_id: string;
  config_profile_id?: string;
  desired_release_id?: string;
}

export interface OverwriteSecretBody {
  value: string;
  reason?: string;
}

export type OperationAction = 'install' | 'update' | 'backup' | 'rollback' | 'health_check' | 'restore';

export interface CreateOperationBody {
  action: OperationAction;
  feature_id: string;
  release_id?: string;
  /** 空数组 = 该 Feature 下全部已启用 Target（"全部"= 已关联到服务器的 Target） */
  target_ids: string[];
  /** 批量筛选：仅处理该服务器（节点）上的 Target */
  node_id?: string;
  /** restore 专用：使用的备份记录 */
  backup_id?: string;
  /** restore 专用：true 时允许先删除目标已有数据 */
  force_delete?: boolean;
  reason?: string;
}

export interface RunBackupsBody {
  node_id?: string;
  feature_id?: string;
}

export interface CancelOperationBody {
  reason?: string;
}

/* --------------------------------- Features -------------------------------- */

export function listFeatures(): Promise<DeploymentFeatureListResponse> {
  return apiFetch('/deployments/features');
}

export function createFeature(body: CreateFeatureBody): Promise<DeploymentFeatureResponse> {
  return apiFetch('/deployments/features', { method: 'POST', body });
}

/* --------------------------------- Releases -------------------------------- */

export function listReleases(featureId?: string): Promise<DeploymentReleaseListResponse> {
  return apiFetch('/deployments/releases', {
    method: 'GET',
    query: featureId ? { feature_id: featureId } : undefined,
  });
}

export function createRelease(body: CreateReleaseBody): Promise<DeploymentReleaseResponse> {
  return apiFetch('/deployments/releases', { method: 'POST', body });
}

/* -------------------------------- OSS Profiles ----------------------------- */

export function listOSSProfiles(): Promise<OSSProfileListResponse> {
  return apiFetch('/deployments/oss-profiles');
}

export function createOSSProfile(body: CreateOSSProfileBody): Promise<OSSProfileResponse> {
  return apiFetch('/deployments/oss-profiles', { method: 'POST', body });
}

export function updateOSSProfile(id: string, body: UpdateOSSProfileBody): Promise<OSSProfileResponse> {
  return apiFetch(`/deployments/oss-profiles/${id}`, { method: 'PUT', body });
}

export function deleteOSSProfile(id: string): Promise<unknown> {
  return apiFetch(`/deployments/oss-profiles/${id}`, { method: 'DELETE' });
}

export function testOSSProfile(id: string): Promise<OSSTestResponse> {
  return apiFetch(`/deployments/oss-profiles/${id}/test`, { method: 'POST' });
}

/* ------------------------------ 仓库同步（OSS → repository/） ---------------- */

export function repositorySync(): Promise<RepositorySyncResponse> {
  return apiFetch('/deployments/repository/sync', { method: 'POST' });
}

/* ------------------------------- Config Profiles --------------------------- */

export function listConfigProfiles(): Promise<DeploymentConfigProfileListResponse> {
  return apiFetch('/deployments/config-profiles');
}

export function createConfigProfile(body: CreateConfigProfileBody): Promise<DeploymentConfigProfileResponse> {
  return apiFetch('/deployments/config-profiles', { method: 'POST', body });
}

export function updateConfigProfile(id: string, body: CreateConfigProfileBody): Promise<DeploymentConfigProfileResponse> {
  return apiFetch(`/deployments/config-profiles/${id}`, { method: 'PUT', body });
}

export function deleteConfigProfile(id: string): Promise<unknown> {
  return apiFetch(`/deployments/config-profiles/${id}`, { method: 'DELETE' });
}

/* ---------------------------------- Targets -------------------------------- */

export function listTargets(): Promise<DeploymentTargetListResponse> {
  return apiFetch('/deployments/targets');
}

export function createTarget(body: CreateTargetBody): Promise<DeploymentTargetResponse> {
  return apiFetch('/deployments/targets', { method: 'POST', body });
}

export function updateTarget(id: string, body: CreateTargetBody): Promise<DeploymentTargetResponse> {
  return apiFetch(`/deployments/targets/${id}`, { method: 'PUT', body });
}

export function deleteTarget(id: string): Promise<unknown> {
  return apiFetch(`/deployments/targets/${id}`, { method: 'DELETE' });
}

/* -------------------------------- Secrets 引用 ----------------------------- */

export function listSecretReferences(): Promise<DeploymentSecretReferenceListResponse> {
  return apiFetch('/deployments/secrets/references');
}

/**
 * 读取 Secret 明文（策略放宽：仅 admin 会话；响应 no-store；每次查看落审计，不含内容）。
 * 用于「查看/编辑」场景；前端不缓存该值。
 */
export function getSecretValue(id: string): Promise<SecretValueResponse> {
  return apiFetch(`/deployments/secrets/${encodeURIComponent(id)}/value`, { method: 'GET' });
}

export function overwriteSecret(id: string, body: OverwriteSecretBody): Promise<DeploymentSecretResponse> {
  return apiFetch(`/deployments/secrets/${id}/overwrite`, { method: 'POST', body });
}

/** 新建 Secret Reference（仅元数据，正文通过 overwriteSecret 写入；新建后 version 为 0）。 */
export function createSecretReference(body: SecretReferenceCreate): Promise<DeploymentSecretResponse> {
  return apiFetch('/deployments/secrets/references', { method: 'POST', body });
}

/* --------------------------------- Operations ------------------------------ */

export function listOperations(): Promise<DeploymentOperationListResponse> {
  return apiFetch('/deployments/operations');
}

/**
 * 按服务器/Feature 触发备份（供外部定时逻辑调用；node_id 为空 = 全部已关联 Target）。
 */
export function runBackupsForNode(body: RunBackupsBody = {}): Promise<RunBackupsResponse> {
  return apiFetch('/deployments/backups/run', { method: 'POST', body });
}

export function createOperation(body: CreateOperationBody): Promise<DeploymentOperationResponse> {
  return apiFetch('/deployments/operations', { method: 'POST', body });
}

export function getOperation(id: string): Promise<DeploymentOperationDetail> {
  return apiFetch(`/deployments/operations/${id}`);
}

export function cancelOperation(id: string, body: CancelOperationBody): Promise<DeploymentOperationResponse> {
  return apiFetch(`/deployments/operations/${id}/cancel`, { method: 'POST', body });
}

export function continueOperation(id: string): Promise<DeploymentOperationResponse> {
  return apiFetch(`/deployments/operations/${id}/continue`, { method: 'POST' });
}

/* ---------------------------------- Backups -------------------------------- */

export function listBackups(): Promise<DeploymentBackupListResponse> {
  return apiFetch('/deployments/backups');
}

export function getBackup(id: string): Promise<DeploymentBackupResponse> {
  return apiFetch(`/deployments/backups/${id}`);
}


/* ------------------------------ Bootstrap Sessions ------------------------- */

export function listBootstrapSessions(): Promise<BootstrapSessionListResponse> {
  return apiFetch('/deployments/bootstrap-sessions');
}

export interface CreateBootstrapSessionBody {
  /** 引导节点（后端必填）。 */
  node_id: string;
  bucket?: string;
  prefix?: string;
  region?: string;
}

export function createBootstrapSession(body: CreateBootstrapSessionBody): Promise<BootstrapSessionCreateResponse> {
  return apiFetch('/deployments/bootstrap-sessions', { method: 'POST', body });
}

/** 撤销引导会话：后端返回 204（无响应体）。 */
export function revokeBootstrapSession(id: string): Promise<unknown> {
  return apiFetch(`/deployments/bootstrap-sessions/${id}/revoke`, { method: 'POST' });
}
