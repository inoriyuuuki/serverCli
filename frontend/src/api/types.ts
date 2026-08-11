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

/** Declarative operations V2 API views. Secret values are intentionally absent. */
export interface SecretRefLite {
  key: string;
  store?: string;
  source?: string;
}

export interface Cluster {
  id: string;
  name: string;
  environment: string;
  active_primary_node_id?: string;
  primary_epoch: number;
  release_channel?: string;
  oss_provider_ref?: string;
  status: string;
  created_at?: string;
  updated_at?: string;
}

export interface NodeProfile {
  id: string;
  cluster_id: string;
  name: string;
  version: string;
  modules?: ProfileModule[];
  status: string;
  created_at?: string;
  updated_at?: string;
}

export interface ProfileModule {
  module_id: string;
  version?: string;
  config?: Record<string, string>;
  secret_refs?: SecretRefLite[];
  dependencies?: string[];
  service_refs?: string[];
  risk_level?: string;
}

export interface DeclarativeNode {
  id: string;
  cluster_id: string;
  node_id: string;
  role: string;
  profile_id?: string;
  lifecycle: string;
  status: string;
  labels?: Record<string, string>;
  addresses?: DeclarativeNodeAddress[];
  os_name?: string;
  os_version?: string;
  arch?: string;
  desired_revision?: string;
  applied_revision?: string;
  identity_generation: number;
  replacement_status?: string;
  agent_status?: string;
  legacy_mac?: string;
}

export interface DeclarativeNodeAddress {
  address: string;
  address_type: string;
  port?: number;
  preferred?: boolean;
}

export interface ServiceReference {
  id: string;
  cluster_id: string;
  name: string;
  service_instance_id?: string;
  node_id?: string;
  address?: string;
  port?: number;
  status: string;
}

export interface OperationView {
  id: string;
  operation_id: string;
  operation_type: string;
  cluster_id?: string;
  node_id?: string;
  module_id?: string;
  service_instance_id?: string;
  desired_revision?: string;
  approval?: string;
  risk_level?: string;
  status: string;
  requested_by?: string;
  created_at?: string;
  started_at?: string;
  finished_at?: string;
  error_code?: string;
  error_message?: string;
}

export interface OperationStepView {
  id: string;
  operation_id: string;
  sequence: number;
  module_id?: string;
  operation?: string;
  attempt: number;
  commit_point?: string;
  status: string;
  error_type?: string;
  message?: string;
  started_at?: string;
  completed_at?: string;
}

export interface ReleaseCacheEntry {
  id: string;
  version: string;
  source_repository?: string;
  source_release?: string;
  os?: string;
  arch?: string;
  artifact_name: string;
  artifact_size: number;
  sha256: string;
  modules_version?: string;
  schema_min?: string;
  schema_max?: string;
  oss_key?: string;
  status: string;
  uploaded_at?: string;
  verified_at?: string;
  created_at?: string;
}

export interface BackupSetView {
  id: string;
  backup_id: string;
  recovery_set_id?: string;
  node_id?: string;
  service_instance_id?: string;
  module_version?: string;
  status: string;
  created_at?: string;
  sha256?: string;
  oss_key?: string;
  size_bytes: number;
}

export interface PrimaryTransferView {
  id: string;
  cluster_id: string;
  from_node_id: string;
  to_node_id: string;
  primary_epoch: number;
  status: string;
  backup_set_id?: string;
  error_message?: string;
  created_at?: string;
  completed_at?: string;
}
