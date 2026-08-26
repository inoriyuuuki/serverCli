/** Shared API types. Backend contract: doc/11_IMPLEMENTATION_CONTRACT.md */

export type Environment = 'test' | 'production' | string;
export type Role = 'primary' | 'child' | string;

export interface SessionInfo {
  environment: Environment;
  role: Role;
  nodeId: string | null;
  nodeName: string | null;
  nodeIp: string | null;
  adminUsername: string | null;
  csrfToken: string | null;
  expiresAt: string | null;
  raw: unknown;
}

export interface NodeInfo {
  id: string;
  node_id?: string;
  instance_name?: string;
  alias?: string | null;
  name?: string;
  hostname?: string | null;
  role?: Role;
  status?: string;
  enabled?: boolean;
  agent_version?: string;
  app_version?: string;
  os_name?: string;
  os_version?: string;
  arch?: string;
  ip?: string;
  addresses?: Array<{
    address?: string;
    address_type?: string;
    service_port?: number | null;
    is_preferred?: boolean;
  }>;
  frontend_port?: number | null;
  backend_port?: number | null;
  last_heartbeat_at?: string | null;
  last_online_at?: string | null;
  labels_json?: unknown;
  metadata_json?: unknown;
  created_at?: string;
  updated_at?: string;
  // Nested snapshot for list views
  heartbeat?: {
    cpu_usage_percent?: number | null;
    memory_total_bytes?: number | null;
    memory_used_bytes?: number | null;
    disk_total_bytes?: number | null;
    disk_used_bytes?: number | null;
    load_1?: number | null;
    load_5?: number | null;
    load_15?: number | null;
    uptime_seconds?: number | null;
    time_offset_ms?: number | null;
  } | null;
  cpu_usage_percent?: number | null;
  memory_total_bytes?: number | null;
  memory_used_bytes?: number | null;
  disk_total_bytes?: number | null;
  disk_used_bytes?: number | null;
  load_1?: number | null;
  load_5?: number | null;
  load_15?: number | null;
  uptime_seconds?: number | null;
  time_offset_ms?: number | null;
  [key: string]: unknown;
}

export interface Enrollment {
  id: string;
  instance_request_id?: string;
  requested_role?: Role;
  hostname?: string;
  source_ip?: string;
  agent_version?: string;
  status?: string;
  review_note?: string | null;
  created_at?: string;
  reported_addresses?: Array<{ address?: string; address_type?: string; service_port?: number | null }>;
  [key: string]: unknown;
}

export interface CommandInfo {
  command_id?: string;
  id?: string;
  command_version?: string;
  version?: string;
  capability_id?: string;
  category?: string;
  title?: string;
  description?: string | null;
  parameter_schema_json?: unknown;
  parameter_schema?: unknown;
  permission_profile?: string;
  timeout_seconds?: number | null;
  max_output_bytes?: number | null;
  enabled?: boolean;
  node_id?: string;
  node?: { id?: string; name?: string; alias?: string; instance_name?: string; hostname?: string } | null;
  node_name?: string;
  [key: string]: unknown;
}

export interface TaskEvent {
  id?: string;
  sequence?: number;
  event_type?: string;
  status?: string;
  message?: string | null;
  occurred_at?: string;
  source?: string;
  [key: string]: unknown;
}

export interface TaskInfo {
  id: string;
  task_id?: string;
  node_id?: string;
  command_id?: string;
  command_version?: string;
  requested_by?: string;
  status?: string;
  arguments_json?: unknown;
  arguments?: unknown;
  queued_at?: string;
  started_at?: string | null;
  finished_at?: string | null;
  created_at?: string;
  timeout_seconds?: number | null;
  exit_code?: number | null;
  error_code?: string | null;
  error_message?: string | null;
  is_protected?: boolean;
  node?: { id?: string; name?: string; alias?: string; instance_name?: string; hostname?: string } | null;
  node_name?: string;
  command_title?: string;
  events?: TaskEvent[];
  output?: {
    stdout_text?: string | null;
    stderr_text?: string | null;
    stdout_bytes?: number | null;
    stderr_bytes?: number | null;
    truncated?: boolean;
    redaction_count?: number;
    encoding?: string;
  } | null;
  result?: {
    status?: string;
    stdout_text?: string | null;
    stderr_text?: string | null;
    exit_code?: number | null;
    error_code?: string | null;
    error_message?: string | null;
    truncated?: boolean;
    finished_at?: string;
  } | null;
  [key: string]: unknown;
}

export interface AiLease {
  id: string;
  request_id?: string;
  node_id?: string;
  access_token_id?: string;
  access_token_name?: string;
  access_token_prefix?: string;
  ai_agent_id?: string;
  ai_agent_name?: string;
  permission_profile?: string;
  public_key_fingerprint?: string;
  issued_at?: string;
  expires_at?: string;
  absolute_expires_at?: string;
  last_renewed_at?: string | null;
  renew_count?: number;
  status?: string;
  revoked_at?: string | null;
  revoke_reason?: string | null;
  renewal_disabled?: boolean;
  active_session_count?: number;
  is_protected?: boolean;
  node?: { id?: string; name?: string; alias?: string; instance_name?: string; hostname?: string } | null;
  node_name?: string;
  [key: string]: unknown;
}

export interface TaskParameterHistory {
  id: string;
  node_id?: string;
  node?: { id?: string; name?: string; alias?: string; instance_name?: string; hostname?: string } | null;
  node_name?: string;
  command_id?: string;
  command_version?: string;
  arguments?: Record<string, unknown>;
  last_task_id?: string | null;
  first_used_at?: string;
  last_used_at?: string;
  use_count?: number;
  [key: string]: unknown;
}

export interface AiLeaseRequest {
  id: string;
  client_request_id?: string;
  access_token_id?: string;
  access_token_name?: string;
  access_token_prefix?: string;
  ai_agent_id?: string;
  ai_agent_name?: string;
  node_id?: string;
  requested_profile?: string;
  permission_profile?: string;
  requested_duration_seconds?: number;
  public_key_fingerprint?: string;
  purpose?: string | null;
  status?: string;
  decision_reason?: string | null;
  source_ip?: string;
  created_at?: string;
  decided_at?: string | null;
  is_protected?: boolean;
  node?: { id?: string; name?: string; alias?: string; instance_name?: string; hostname?: string } | null;
  node_name?: string;
  [key: string]: unknown;
}

export interface PermissionGrant {
  resource: string;
  actions: string[];
  constraints?: Record<string, unknown>;
}

export interface PermissionSet {
  version: number;
  grants: PermissionGrant[];
}

export interface PermissionDef {
  category: string;
  resource: string;
  action: string;
  label: string;
  description: string;
}

export interface PermissionCategory {
  category: string;
  label: string;
}

export interface PermissionCatalogResponse {
  categories: PermissionCategory[];
  permissions: PermissionDef[];
}

export interface ApiAccessToken {
  id: string;
  name?: string;
  token_prefix?: string;
  created_by?: string | null;
  created_at?: string;
  expires_at?: string | null;
  revoked_at?: string | null;
  revoked_by?: string | null;
  last_used_at?: string | null;
  last_used_ip?: string | null;
  usage_count?: number;
  permission_version?: number;
  permissions?: PermissionSet;
}

export interface ApiTokenUsageLog {
  id: string;
  token_id?: string;
  request_id?: string | null;
  occurred_at?: string;
  method?: string;
  route?: string;
  resource?: string | null;
  action?: string | null;
  source_ip?: string | null;
  user_agent?: string | null;
  status_code?: number;
  outcome?: string;
  lease_request_id?: string | null;
  lease_id?: string | null;
  token_state?: string;
}

export interface ApiRouteSpec {
  method: string;
  path: string;
  group?: string;
  auth?: string;
  summary?: string;
  params?: { name: string; in: string; type: string; required?: boolean; description?: string }[];
  body?: string;
  response?: string;
  errors?: string[];
  debug?: boolean;
}

export interface AuditEvent {
  id: string;
  occurred_at?: string;
  node_id?: string;
  actor_type?: string;
  actor_id?: string;
  action?: string;
  resource_type?: string;
  resource_id?: string;
  result?: string;
  request_id?: string;
  task_id?: string;
  lease_id?: string;
  session_id?: string;
  source_ip?: string;
  summary?: string | null;
  details_json?: unknown;
  risk_level?: string;
  is_protected?: boolean;
  node?: { id?: string; name?: string; alias?: string; instance_name?: string } | null;
  node_name?: string;
  [key: string]: unknown;
}

export interface CleanupRun {
  id: string;
  started_at?: string;
  finished_at?: string | null;
  trigger_type?: string;
  candidate_count?: number;
  deleted_count?: number;
  skipped_protected_count?: number;
  status?: string;
  error_message?: string | null;
  requested_by?: string;
  [key: string]: unknown;
}

export interface SystemInfo {
  environment?: string;
  role?: Role;
  version?: string;
  instance_name?: string;
  node_id?: string;
  [key: string]: unknown;
}

export interface VersionInfo {
  version?: string;
  build?: string;
  commit?: string;
  migration_version?: string;
  database_migration_version?: string;
  [key: string]: unknown;
}

/* ------------------------------ 部署管理 ------------------------------ */
/** 契约：doc/13_DEPLOYMENT_MANAGEMENT.md §9；时间 RFC3339 UTC。 */

export interface DeploymentFeature {
  id: string;
  feature_key: string;
  name?: string;
  description?: string | null;
  os?: string | null;
  arch?: string | null;
  backup_mode?: string | null;
  rollback_capability?: string | null;
  minimum_agent_version?: string | null;
  config_schema_json?: unknown;
  created_at?: string | null;
  updated_at?: string | null;
  [key: string]: unknown;
}

export interface DeploymentRelease {
  id: string;
  feature_id: string;
  version: string;
  source_commit?: string | null;
  object_key?: string | null;
  size?: number | null;
  sha256?: string | null;
  signature?: string | null;
  install_hook?: string | null;
  update_hook?: string | null;
  backup_hook?: string | null;
  health_hook?: string | null;
  rollback_hook?: string | null;
  backup_mode?: string | null;
  data_migration_metadata_json?: string | null;
  manifest_hash?: string | null;
  created_at?: string | null;
  [key: string]: unknown;
}

export interface OSSProfile {
  id: string;
  name: string;
  endpoint?: string | null;
  region?: string | null;
  bucket?: string | null;
  prefix?: string | null;
  is_private?: boolean;
  last_test_result?: string | null;
  created_at?: string | null;
  updated_at?: string | null;
  // 不含 access_key_id / access_key_secret：AK 只写不读。
  [key: string]: unknown;
}

export interface DeploymentConfigProfile {
  id: string;
  name: string;
  scope_type?: string | null;
  scope_id?: string | null;
  feature_id?: string | null;
  content_hash?: string | null;
  version?: number | null;
  created_at?: string | null;
  updated_at?: string | null;
  [key: string]: unknown;
}

export interface DeploymentTarget {
  id: string;
  feature_id: string;
  feature_key?: string | null;
  node_id: string;
  node_name?: string | null;
  config_profile_id?: string | null;
  desired_release_id?: string | null;
  current_release_id?: string | null;
  last_healthy_release_id?: string | null;
  actual_status?: string | null;
  last_health_check_at?: string | null;
  config_revision?: number | null;
  enabled?: boolean;
  created_at?: string | null;
  updated_at?: string | null;
  [key: string]: unknown;
}

export interface DeploymentSecretReference {
  id: string;
  name: string;
  feature_id?: string | null;
  scope_type?: string | null;
  scope_id?: string | null;
  object_key?: string | null;
  version?: number | null;
  content_hash?: string | null;
  encryption_mode?: string | null;
  size?: number | null;
  created_at?: string | null;
  updated_at?: string | null;
  // 只含引用元数据，绝不返回正文。
  [key: string]: unknown;
}

/** 新建 Secret Reference 请求体（仅元数据，正文通过覆盖写入）。scope_type 为 shared / node。 */
export interface SecretReferenceCreate {
  name: string;
  feature_id: string;
  scope_type: 'shared' | 'node';
  scope_id?: string;
}

export interface DeploymentOperation {
  id: string;
  action?: string | null;
  feature_id?: string | null;
  feature_key?: string | null;
  strategy?: string | null;
  status?: string | null;
  requested_by?: string | null;
  environment_id?: string | null;
  frozen_config_hash?: string | null;
  created_at?: string | null;
  started_at?: string | null;
  finished_at?: string | null;
  [key: string]: unknown;
}

export interface DeploymentOperationTarget {
  id: string;
  target_id: string;
  node_id?: string | null;
  status?: string | null;
  current_release_id?: string | null;
  desired_release_id?: string | null;
  error_message?: string | null;
  started_at?: string | null;
  finished_at?: string | null;
  [key: string]: unknown;
}

export interface DeploymentStep {
  id: string;
  operation_target_id?: string | null;
  node_id?: string | null;
  step_type?: string | null;
  status?: string | null;
  command_id?: string | null;
  task_id?: string | null;
  message?: string | null;
  started_at?: string | null;
  finished_at?: string | null;
  [key: string]: unknown;
}

export interface DeploymentOperationDetail {
  operation: DeploymentOperation;
  targets: DeploymentOperationTarget[];
  steps: DeploymentStep[];
}

export interface DeploymentBackup {
  id: string;
  operation_id?: string | null;
  target_id?: string | null;
  node_id?: string | null;
  feature_id?: string | null;
  feature_key?: string | null;
  backup_mode?: string | null;
  object_key?: string | null;
  size?: number | null;
  sha256?: string | null;
  status?: string | null;
  created_at?: string | null;
  [key: string]: unknown;
}

export interface BootstrapSession {
  id: string;
  node_id?: string | null;
  status?: string | null;
  bucket?: string | null;
  prefix?: string | null;
  region?: string | null;
  created_at?: string | null;
  expires_at?: string | null;
  revoked_at?: string | null;
  [key: string]: unknown;
}

/* ---------------------- 部署管理：list/detail 响应包装 ---------------------- */

export interface DeploymentFeatureListResponse {
  features: DeploymentFeature[];
}
export interface DeploymentFeatureResponse {
  feature: DeploymentFeature;
}
export interface DeploymentReleaseListResponse {
  releases: DeploymentRelease[];
}
export interface DeploymentReleaseResponse {
  release: DeploymentRelease;
}
export interface OSSProfileListResponse {
  profiles: OSSProfile[];
}
export interface OSSProfileResponse {
  profile: OSSProfile;
}
export interface OSSTestResponse {
  ok: boolean;
  message?: string;
}
export interface RepositorySyncResponse {
  sync: { started_at?: string; status?: string };
}
export interface DeploymentConfigProfileListResponse {
  profiles: DeploymentConfigProfile[];
}
export interface DeploymentConfigProfileResponse {
  profile: DeploymentConfigProfile;
}
export interface DeploymentTargetListResponse {
  targets: DeploymentTarget[];
}
export interface DeploymentTargetResponse {
  target: DeploymentTarget;
}
export interface DeploymentSecretReferenceListResponse {
  secrets: DeploymentSecretReference[];
}
export interface DeploymentSecretResponse {
  secret: DeploymentSecretReference;
}
export interface DeploymentOperationListResponse {
  operations: DeploymentOperation[];
}
export interface DeploymentOperationResponse {
  operation: DeploymentOperation;
}
export interface DeploymentBackupListResponse {
  backups: DeploymentBackup[];
}
export interface DeploymentBackupResponse {
  backup: DeploymentBackup;
}
export interface BootstrapSessionListResponse {
  sessions: BootstrapSession[];
}
export interface BootstrapSessionCreateResponse {
  session: BootstrapSession;
  command: string;
  token: string;
}
