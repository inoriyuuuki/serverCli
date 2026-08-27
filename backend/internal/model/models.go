// Package model defines the shared data structures persisted by ServerCLI.
package model

import (
	"time"

	"servercli/internal/uuid"
)

// NewUUID returns a fresh UUIDv4 string.
func NewUUID() string { return uuid.New() }

// NewUUIDIsValid reports whether s is a valid UUID string.
func NewUUIDIsValid(s string) bool { return uuid.IsValid(s) }

// Time layout used everywhere: RFC3339 UTC.
const TimeLayout = time.RFC3339Nano

// Status constants for nodes.
const (
	NodeStatusPending  = "pending"
	NodeStatusOnline   = "online"
	NodeStatusDegraded = "degraded"
	NodeStatusOffline  = "offline"
	NodeStatusDisabled = "disabled"
	NodeStatusRejected = "rejected"
)

// Enrollment statuses.
const (
	EnrollmentPending  = "pending"
	EnrollmentApproved = "approved"
	EnrollmentRejected = "rejected"
	EnrollmentExpired  = "expired"
	EnrollmentClaimed  = "claimed"
)

// Task statuses (terminal states are irreversible).
const (
	TaskQueued          = "queued"
	TaskDispatched      = "dispatched"
	TaskRunning         = "running"
	TaskSucceeded       = "succeeded"
	TaskFailed          = "failed"
	TaskTimedOut        = "timed_out"
	TaskCancelled       = "cancelled"
	TaskNodeUnreachable = "node_unreachable"
	TaskResultUnknown   = "result_unknown"
)

// Lease request statuses.
const (
	LeaseRequestPending   = "pending"
	LeaseRequestApproved  = "approved"
	LeaseRequestRejected  = "rejected"
	LeaseRequestFailed    = "failed"
	LeaseRequestCancelled = "cancelled"
)

// Lease statuses (disconnected/expired/revoked/failed are terminal).
const (
	LeaseActive       = "active"
	LeaseDisconnected = "disconnected"
	LeaseExpired      = "expired"
	LeaseRevoked      = "revoked"
	LeaseFailed       = "failed"
)

// Permission profiles.
const (
	ProfileReadOnly = "read-only"
	ProfileOperator = "operator"
	ProfileAdmin    = "admin"
)

// Actor types for audit events.
const (
	ActorAdmin  = "admin"
	ActorAI     = "ai_agent"
	ActorNode   = "node"
	ActorSystem = "system"
)

// IsTaskTerminal reports whether a task status is final.
func IsTaskTerminal(s string) bool {
	switch s {
	case TaskSucceeded, TaskFailed, TaskTimedOut, TaskCancelled, TaskNodeUnreachable, TaskResultUnknown:
		return true
	}
	return false
}

// AdminUser mirrors admin_user.
type AdminUser struct {
	ID                string     `json:"id"`
	Username          string     `json:"username"`
	PasswordHash      string     `json:"-"`
	PasswordChangedAt *time.Time `json:"password_changed_at"`
	FailedLoginCount  int        `json:"-"`
	LockedUntil       *time.Time `json:"-"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// AdminSession mirrors admin_session.
type AdminSession struct {
	ID             string     `json:"id"`
	AdminUserID    string     `json:"admin_user_id"`
	TokenHash      string     `json:"-"`
	CSRFSecretHash string     `json:"-"`
	IPAddress      string     `json:"ip_address"`
	UserAgent      string     `json:"user_agent"`
	ExpiresAt      time.Time  `json:"expires_at"`
	RevokedAt      *time.Time `json:"revoked_at"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

// NodeEnrollment mirrors node_enrollment.
type NodeEnrollment struct {
	ID                    string     `json:"id"`
	InstanceRequestID     string     `json:"instance_request_id"`
	EnvironmentID         string     `json:"environment_id"`
	RequestedRole         string     `json:"requested_role"`
	Hostname              string     `json:"hostname"`
	InstanceName          string     `json:"instance_name"`
	SourceIP              string     `json:"source_ip"`
	ReportedAddressesJSON string     `json:"-"`
	AgentVersion          string     `json:"agent_version"`
	OSName                string     `json:"os_name"`
	OSVersion             string     `json:"os_version"`
	Arch                  string     `json:"arch"`
	FrontendPort          int        `json:"frontend_port"`
	BackendPort           int        `json:"backend_port"`
	Status                string     `json:"status"`
	ReviewedBy            string     `json:"reviewed_by"`
	ReviewedAt            *time.Time `json:"reviewed_at"`
	ReviewNote            string     `json:"review_note"`
	ClaimTokenHash        string     `json:"-"`
	ClaimExpiresAt        *time.Time `json:"-"`
	ClaimedAt             *time.Time `json:"claimed_at"`
	InstancePublicKey     string     `json:"instance_public_key"`
	NodeID                string     `json:"node_id"`
	CreatedAt             time.Time  `json:"created_at"`
}

// Node mirrors node.
type Node struct {
	ID                string     `json:"id"`
	EnvironmentID     string     `json:"environment_id"`
	InstanceName      string     `json:"instance_name"`
	Alias             string     `json:"alias"`
	Role              string     `json:"role"`
	Hostname          string     `json:"hostname"`
	Status            string     `json:"status"`
	Enabled           bool       `json:"enabled"`
	AgentVersion      string     `json:"agent_version"`
	AppVersion        string     `json:"app_version"`
	OSName            string     `json:"os_name"`
	OSVersion         string     `json:"os_version"`
	Arch              string     `json:"arch"`
	FrontendPort      int        `json:"frontend_port"`
	BackendPort       int        `json:"backend_port"`
	LastHeartbeatAt   *time.Time `json:"last_heartbeat_at"`
	LastOnlineAt      *time.Time `json:"last_online_at"`
	LabelsJSON        string     `json:"-"`
	MetadataJSON      string     `json:"-"`
	CredentialHash    string     `json:"-"`
	CredentialPrefix  string     `json:"credential_prefix"`
	CredentialVersion int        `json:"credential_version"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// NodeAddress mirrors node_address.
type NodeAddress struct {
	ID          string    `json:"id"`
	NodeID      string    `json:"node_id"`
	Address     string    `json:"address"`
	AddressType string    `json:"address_type"`
	ServicePort int       `json:"service_port"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	IsPreferred bool      `json:"is_preferred"`
}

// NodeHeartbeat mirrors node_heartbeat.
type NodeHeartbeat struct {
	ID               string     `json:"id"`
	NodeID           string     `json:"node_id"`
	RecordedAt       time.Time  `json:"recorded_at"`
	CPUUsagePercent  float64    `json:"cpu_usage_percent"`
	MemoryTotalBytes int64      `json:"memory_total_bytes"`
	MemoryUsedBytes  int64      `json:"memory_used_bytes"`
	DiskTotalBytes   int64      `json:"disk_total_bytes"`
	DiskUsedBytes    int64      `json:"disk_used_bytes"`
	Load1            float64    `json:"load_1"`
	Load5            float64    `json:"load_5"`
	Load15           float64    `json:"load_15"`
	UptimeSeconds    int64      `json:"uptime_seconds"`
	TimeOffsetMS     int64      `json:"time_offset_ms"`
	SummaryJSON      string     `json:"-"`
	IsProtected      bool       `json:"is_protected"`
	ProtectedAt      *time.Time `json:"protected_at"`
}

// NodeCommand mirrors node_command.
type NodeCommand struct {
	ID                  string    `json:"id"`
	NodeID              string    `json:"node_id"`
	CommandID           string    `json:"command_id"`
	CommandVersion      string    `json:"command_version"`
	CapabilityID        string    `json:"capability_id"`
	Category            string    `json:"category"`
	Title               string    `json:"title"`
	Description         string    `json:"description"`
	ParameterSchemaJSON string    `json:"parameter_schema_json,omitempty"`
	PermissionProfile   string    `json:"permission_profile"`
	TimeoutSeconds      int       `json:"timeout_seconds"`
	MaxOutputBytes      int64     `json:"max_output_bytes"`
	Enabled             bool      `json:"enabled"`
	ManifestHash        string    `json:"manifest_hash"`
	ExecutableHash      string    `json:"executable_hash"`
	FirstSeenAt         time.Time `json:"first_seen_at"`
	LastSeenAt          time.Time `json:"last_seen_at"`
}

// Task mirrors task.
type Task struct {
	ID                string     `json:"id"`
	NodeID            string     `json:"node_id"`
	NodeName          string     `json:"node_name,omitempty"`
	CommandID         string     `json:"command_id"`
	CommandVersion    string     `json:"command_version"`
	RequestedBy       string     `json:"requested_by"`
	IdempotencyKey    string     `json:"-"`
	ArgumentsJSON     string     `json:"-"`
	Status            string     `json:"status"`
	QueuedAt          time.Time  `json:"queued_at"`
	StartedAt         *time.Time `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`
	TimeoutSeconds    int        `json:"timeout_seconds"`
	ExitCode          *int       `json:"exit_code"`
	ErrorCode         string     `json:"error_code"`
	ErrorMessage      string     `json:"error_message"`
	ResultSummaryJSON string     `json:"-"`
	IsProtected       bool       `json:"is_protected"`
	ProtectedAt       *time.Time `json:"protected_at"`
}

// TaskEvent mirrors task_event.
type TaskEvent struct {
	ID         string    `json:"id"`
	TaskID     string    `json:"task_id"`
	Sequence   int64     `json:"sequence"`
	EventType  string    `json:"event_type"`
	Status     string    `json:"status,omitempty"`
	Message    string    `json:"message,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
	Source     string    `json:"source,omitempty"`
}

// TaskOutput mirrors task_output.
type TaskOutput struct {
	TaskID         string     `json:"task_id"`
	StdoutText     string     `json:"stdout_text"`
	StderrText     string     `json:"stderr_text"`
	StdoutBytes    int64      `json:"stdout_bytes"`
	StderrBytes    int64      `json:"stderr_bytes"`
	Truncated      bool       `json:"truncated"`
	RedactionCount int64      `json:"redaction_count"`
	Encoding       string     `json:"encoding"`
	CreatedAt      time.Time  `json:"created_at"`
	IsProtected    bool       `json:"is_protected"`
	ProtectedAt    *time.Time `json:"protected_at"`
}

// AILeaseRequest mirrors ai_lease_request.
type AILeaseRequest struct {
	ID                       string     `json:"id"`
	ClientRequestID          string     `json:"client_request_id"`
	EnvironmentID            string     `json:"environment_id"`
	AccessTokenID            string     `json:"access_token_id,omitempty"`
	AccessTokenName          string     `json:"access_token_name,omitempty"`
	AccessTokenPrefix        string     `json:"access_token_prefix,omitempty"`
	AIAgentID                string     `json:"ai_agent_id"`
	AIAgentName              string     `json:"ai_agent_name"`
	NodeID                   string     `json:"node_id"`
	NodeName                 string     `json:"node_name,omitempty"`
	RequestedProfile         string     `json:"requested_profile"`
	RequestedDurationSeconds int        `json:"requested_duration_seconds"`
	PublicKey                string     `json:"-"`
	PublicKeyFingerprint     string     `json:"public_key_fingerprint"`
	Purpose                  string     `json:"purpose"`
	Status                   string     `json:"status"`
	DecisionReason           string     `json:"decision_reason"`
	SourceIP                 string     `json:"source_ip"`
	ClientMetadataJSON       string     `json:"-"`
	CreatedAt                time.Time  `json:"created_at"`
	DecidedAt                *time.Time `json:"decided_at"`
	IsProtected              bool       `json:"is_protected"`
	ProtectedAt              *time.Time `json:"protected_at"`
}

// AILease mirrors ai_lease.
type AILease struct {
	ID                   string     `json:"id"`
	RequestID            string     `json:"request_id"`
	AccessTokenID        string     `json:"access_token_id,omitempty"`
	AccessTokenName      string     `json:"access_token_name,omitempty"`
	AccessTokenPrefix    string     `json:"access_token_prefix,omitempty"`
	NodeID               string     `json:"node_id"`
	NodeName             string     `json:"node_name,omitempty"`
	AIAgentID            string     `json:"ai_agent_id"`
	PermissionProfile    string     `json:"permission_profile"`
	PublicKey            string     `json:"-"`
	PublicKeyFingerprint string     `json:"public_key_fingerprint"`
	IssuedAt             time.Time  `json:"issued_at"`
	ExpiresAt            time.Time  `json:"expires_at"`
	AbsoluteExpiresAt    time.Time  `json:"absolute_expires_at"`
	LastRenewedAt        *time.Time `json:"last_renewed_at"`
	RenewCount           int        `json:"renew_count"`
	Status               string     `json:"status"`
	RevokedAt            *time.Time `json:"revoked_at"`
	RevokeReason         string     `json:"revoke_reason"`
	RenewalDisabled      bool       `json:"renewal_disabled"`
	RenewalTokenHash     string     `json:"-"`
	RenewalTokenPrefix   string     `json:"-"` // legacy schema column; never returned
	ActiveSessionCount   int        `json:"active_session_count"`
	LastHeartbeatAt      *time.Time `json:"last_heartbeat_at"`
	KeyInstalled         bool       `json:"key_installed"`
	KeyInstalledAt       *time.Time `json:"key_installed_at"`
	IsProtected          bool       `json:"is_protected"`
	ProtectedAt          *time.Time `json:"protected_at"`
}

// AILeaseEvent mirrors ai_lease_event.
type AILeaseEvent struct {
	ID          string    `json:"id"`
	LeaseID     string    `json:"lease_id"`
	EventType   string    `json:"event_type"`
	ActorType   string    `json:"actor_type"`
	ActorID     string    `json:"actor_id"`
	DetailsJSON string    `json:"-"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// AI SSHSession mirrors ai_ssh_session.
type AISSHSession struct {
	ID            string     `json:"id"`
	LeaseID       string     `json:"lease_id"`
	NodeID        string     `json:"node_id"`
	RemoteAddress string     `json:"remote_address"`
	ConnectionID  string     `json:"connection_id"`
	OSPid         int64      `json:"os_pid"`
	CgroupID      string     `json:"cgroup_id"`
	StartedAt     time.Time  `json:"started_at"`
	LastSeenAt    *time.Time `json:"last_seen_at"`
	EndedAt       *time.Time `json:"ended_at"`
	EndReason     string     `json:"end_reason"`
	ExitCode      *int       `json:"exit_code"`
	CommandCount  int        `json:"command_count"`
	RecordingRef  string     `json:"recording_ref"`
	IsProtected   bool       `json:"is_protected"`
	ProtectedAt   *time.Time `json:"protected_at"`
}

// TokenExpiry enumerates the fixed access token lifetimes.
const (
	TokenTTL15m   = "15m"
	TokenTTL1h    = "1h"
	TokenTTL6h    = "6h"
	TokenTTL1d    = "1d"
	TokenTTL1w    = "1w"
	TokenTTLNever = "never"
)

// TokenUsageOutcome values for api_token_usage_log.outcome.
const (
	TokenUsageSuccess = "success"
	TokenUsageDenied  = "denied"
	TokenUsageFailure = "failure"
)

// TokenState values for api_token_usage_log.token_state.
const (
	TokenStateValid   = "valid"
	TokenStateExpired = "expired"
	TokenStateRevoked = "revoked"
)

// APIAccessToken mirrors api_access_token.
type APIAccessToken struct {
	ID                string     `json:"id"`
	EnvironmentID     string     `json:"environment_id"`
	Name              string     `json:"name"`
	TokenHash         string     `json:"-"`
	TokenPrefix       string     `json:"token_prefix"`
	CreatedBy         string     `json:"created_by,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	ExpiresAt         *time.Time `json:"expires_at"`
	RevokedAt         *time.Time `json:"revoked_at"`
	RevokedBy         string     `json:"revoked_by,omitempty"`
	LastUsedAt        *time.Time `json:"last_used_at"`
	LastUsedIP        string     `json:"last_used_ip,omitempty"`
	UsageCount        int64      `json:"usage_count"`
	PermissionVersion int        `json:"permission_version"`
	PermissionsJSON   string     `json:"-"`
}

// APITokenUsageLog mirrors api_token_usage_log.
type APITokenUsageLog struct {
	ID             string    `json:"id"`
	TokenID        string    `json:"token_id"`
	EnvironmentID  string    `json:"environment_id"`
	RequestID      string    `json:"request_id,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
	Method         string    `json:"method"`
	Route          string    `json:"route"`
	Resource       string    `json:"resource,omitempty"`
	Action         string    `json:"action,omitempty"`
	SourceIP       string    `json:"source_ip,omitempty"`
	UserAgent      string    `json:"user_agent,omitempty"`
	StatusCode     int       `json:"status_code"`
	Outcome        string    `json:"outcome"`
	LeaseRequestID string    `json:"lease_request_id,omitempty"`
	LeaseID        string    `json:"lease_id,omitempty"`
	TokenState     string    `json:"token_state"`
}

// AuditEvent mirrors audit_event.
type AuditEvent struct {
	ID            string     `json:"id"`
	OccurredAt    time.Time  `json:"occurred_at"`
	EnvironmentID string     `json:"environment_id"`
	NodeID        string     `json:"node_id"`
	NodeName      string     `json:"node_name,omitempty"`
	ActorType     string     `json:"actor_type"`
	ActorID       string     `json:"actor_id"`
	Action        string     `json:"action"`
	ResourceType  string     `json:"resource_type"`
	ResourceID    string     `json:"resource_id"`
	Result        string     `json:"result"`
	RequestID     string     `json:"request_id"`
	TaskID        string     `json:"task_id"`
	LeaseID       string     `json:"lease_id"`
	SessionID     string     `json:"session_id"`
	SourceIP      string     `json:"source_ip"`
	Summary       string     `json:"summary"`
	DetailsJSON   string     `json:"-"`
	RiskLevel     string     `json:"risk_level"`
	IsProtected   bool       `json:"is_protected"`
	ProtectedAt   *time.Time `json:"protected_at"`
	ProtectedBy   string     `json:"protected_by"`
}

// SystemSetting mirrors system_setting.
type SystemSetting struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CleanupRun mirrors cleanup_run.
type CleanupRun struct {
	ID                    string     `json:"id"`
	StartedAt             time.Time  `json:"started_at"`
	FinishedAt            *time.Time `json:"finished_at"`
	TriggerType           string     `json:"trigger_type"`
	PolicySnapshotJSON    string     `json:"-"`
	CandidateCount        int64      `json:"candidate_count"`
	DeletedCount          int64      `json:"deleted_count"`
	SkippedProtectedCount int64      `json:"skipped_protected_count"`
	Status                string     `json:"status"`
	ErrorMessage          string     `json:"error_message"`
	RequestedBy           string     `json:"requested_by"`
	IsProtected           bool       `json:"is_protected"`
	ProtectedAt           *time.Time `json:"protected_at"`
}

// AIAutoApproval mirrors ai_auto_approval: a device (ai_agent_id) is exempt
// from manual approval for a specific node until expires_at.
type AIAutoApproval struct {
	ID              string    `json:"id"`
	EnvironmentID   string    `json:"-"`
	AIAgentID       string    `json:"ai_agent_id"`
	AIAgentName     string    `json:"ai_agent_name"`
	NodeID          string    `json:"node_id"`
	SourceRequestID string    `json:"source_request_id"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// TaskParameterHistory mirrors task_parameter_history: a reusable set of
// arguments previously used for a command on a specific node.
type TaskParameterHistory struct {
	ID             string         `json:"id"`
	NodeID         string         `json:"node_id"`
	CommandID      string         `json:"command_id"`
	CommandVersion string         `json:"command_version"`
	ArgumentsJSON  string         `json:"-"`
	Arguments      map[string]any `json:"arguments"`
	ArgumentsHash  string         `json:"-"`
	LastTaskID     string         `json:"last_task_id"`
	FirstUsedAt    time.Time      `json:"first_used_at"`
	LastUsedAt     time.Time      `json:"last_used_at"`
	UseCount       int            `json:"use_count"`
}

// ─── 部署管理（Deployment Management）─────────────────────────────────────────────
// 以下类型对应 backend/internal/db/migrations/0009_deployment.sql 中的表。

// Deployment operation actions.
const (
	DeploymentActionInstall     = "install"
	DeploymentActionUpdate      = "update"
	DeploymentActionBackup      = "backup"
	DeploymentActionRollback    = "rollback"
	DeploymentActionHealthCheck = "health_check"
	DeploymentActionRestore     = "restore"
)

// Deployment operation statuses.
const (
	DeploymentStatusDraft                = "draft"
	DeploymentStatusValidated            = "validated"
	DeploymentStatusAwaitingConfirmation = "awaiting_confirmation"
	DeploymentStatusQueued               = "queued"
	DeploymentStatusRunning              = "running"
	DeploymentStatusSucceeded            = "succeeded"
	DeploymentStatusPartialFailed        = "partial_failed"
	DeploymentStatusFailed               = "failed"
	DeploymentStatusCancelled            = "cancelled"
	DeploymentStatusRolledBack           = "rolled_back"
	DeploymentStatusRollbackFailed       = "rollback_failed"
	DeploymentStatusSkipped              = "skipped"
)

// Bootstrap session statuses: repository/agent bootstrap pipeline states and
// their failure states.
const (
	BootstrapStatusCreated              = "created"
	BootstrapStatusRepositorySyncing    = "repository_syncing"
	BootstrapStatusRepositoryVerified   = "repository_verified"
	BootstrapStatusXrayInstalling       = "xray_installing"
	BootstrapStatusProxyChecking        = "proxy_checking"
	BootstrapStatusProxyReady           = "proxy_ready"
	BootstrapStatusAgentDownloading     = "agent_downloading"
	BootstrapStatusAgentVerifying       = "agent_verifying"
	BootstrapStatusAgentInstalling      = "agent_installing"
	BootstrapStatusEnrollmentPending    = "enrollment_pending"
	BootstrapStatusNodeOnline           = "node_online"
	BootstrapStatusCompleted            = "completed"
	BootstrapStatusRepositorySyncFailed = "repository_sync_failed"
	BootstrapStatusManifestInvalid      = "manifest_invalid"
	BootstrapStatusSignatureFailed      = "signature_failed"
	BootstrapStatusXrayFailed           = "xray_failed"
	BootstrapStatusProxyFailed          = "proxy_failed"
	BootstrapStatusAgentDownloadFailed  = "agent_download_failed"
	BootstrapStatusAgentVerifyFailed    = "agent_verify_failed"
	BootstrapStatusAgentStartFailed     = "agent_start_failed"
	BootstrapStatusEnrollmentFailed     = "enrollment_failed"
	BootstrapStatusExpired              = "expired"
	BootstrapStatusCancelled            = "cancelled"
)

// Secret scope types for deployment secret references.
const (
	SecretScopeShared = "shared"
	SecretScopeNode   = "node"
)

// Config scope types for deployment config profiles.
const (
	ConfigScopeShared = "shared"
	ConfigScopeNode   = "node"
)

// Deployment target statuses.
const (
	TargetStatusPending    = "pending"
	TargetStatusInstalling = "installing"
	TargetStatusRunning    = "running"
	TargetStatusHealthy    = "healthy"
	TargetStatusUnhealthy  = "unhealthy"
	TargetStatusError      = "error"
)

// DeploymentFeature mirrors deployment_feature.
type DeploymentFeature struct {
	ID                  string    `json:"id"`
	FeatureKey          string    `json:"feature_key"`
	Name                string    `json:"name"`
	Description         string    `json:"description"`
	OS                  string    `json:"os"`
	Arch                string    `json:"arch"`
	ConfigSchemaJSON    string    `json:"config_schema_json,omitempty"`
	BackupMode          string    `json:"backup_mode"`
	RollbackCapability  string    `json:"rollback_capability"`
	DependenciesJSON    string    `json:"dependencies_json,omitempty"`
	MinimumAgentVersion string    `json:"minimum_agent_version"`
	DefaultVersion      string    `json:"default_version,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// DeploymentRelease mirrors deployment_release: an immutable feature version.
type DeploymentRelease struct {
	ID                        string    `json:"id"`
	FeatureID                 string    `json:"feature_id"`
	Version                   string    `json:"version"`
	SourceCommit              string    `json:"source_commit,omitempty"`
	ObjectKey                 string    `json:"object_key"`
	Size                      int64     `json:"size"`
	SHA256                    string    `json:"sha256"`
	Signature                 string    `json:"signature,omitempty"`
	InstallHook               string    `json:"install_hook,omitempty"`
	UpdateHook                string    `json:"update_hook,omitempty"`
	BackupHook                string    `json:"backup_hook,omitempty"`
	HealthHook                string    `json:"health_hook,omitempty"`
	RollbackHook              string    `json:"rollback_hook,omitempty"`
	RestoreHook               string    `json:"restore_hook,omitempty"`
	BackupMode                string    `json:"backup_mode"`
	DataMigrationMetadataJSON string    `json:"data_migration_metadata_json,omitempty"`
	ManifestHash              string    `json:"manifest_hash,omitempty"`
	CreatedAt                 time.Time `json:"created_at"`
}

// OSSProfile mirrors oss_profile. Access key material is NEVER stored in
// plaintext: access_key_id_enc / access_key_secret_enc hold the ciphertext or
// a key-reference produced by the service layer.
type OSSProfile struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	Endpoint           string     `json:"endpoint"`
	Region             string     `json:"region"`
	Bucket             string     `json:"bucket"`
	Prefix             string     `json:"prefix"`
	AccessKeyIDEnc     string     `json:"access_key_id_enc"`
	AccessKeySecretEnc string     `json:"access_key_secret_enc"`
	IsPrivate          bool       `json:"is_private"`
	LastTestedAt       *time.Time `json:"last_tested_at"`
	LastTestResult     string     `json:"last_test_result,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// DeploymentConfigProfile mirrors deployment_config_profile.
type DeploymentConfigProfile struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ScopeType   string    `json:"scope_type"`
	ScopeID     string    `json:"scope_id"`
	FeatureID   string    `json:"feature_id"`
	ContentJSON string    `json:"content_json"`
	ContentHash string    `json:"content_hash"`
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DeploymentSecretReference mirrors deployment_secret_reference. It only
// stores a reference (object key + content hash + encryption mode) and NEVER
// the secret body; the plaintext secret is resolved by the service layer.
type DeploymentSecretReference struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	FeatureID      string    `json:"feature_id"`
	ScopeType      string    `json:"scope_type"`
	ScopeID        string    `json:"scope_id"`
	ObjectKey      string    `json:"object_key"`
	Version        int       `json:"version"`
	ContentHash    string    `json:"content_hash"`
	EncryptionMode string    `json:"encryption_mode"`
	Size           int64     `json:"size"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// DeploymentTarget mirrors deployment_target: a feature pinned to a node.
type DeploymentTarget struct {
	ID                    string     `json:"id"`
	FeatureID             string     `json:"feature_id"`
	NodeID                string     `json:"node_id"`
	ConfigProfileID       string     `json:"config_profile_id,omitempty"`
	OverrideReferenceJSON string     `json:"override_reference_json,omitempty"`
	DesiredReleaseID      string     `json:"desired_release_id,omitempty"`
	CurrentReleaseID      string     `json:"current_release_id,omitempty"`
	LastHealthyReleaseID  string     `json:"last_healthy_release_id,omitempty"`
	ActualStatus          string     `json:"actual_status"`
	LastHealthCheckAt     *time.Time `json:"last_health_check_at"`
	ConfigRevision        int        `json:"config_revision"`
	Enabled               bool       `json:"enabled"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

// DeploymentTargetSecret mirrors deployment_target_secret: binding of a secret
// reference to a target's filesystem path.
type DeploymentTargetSecret struct {
	ID                string    `json:"id"`
	TargetID          string    `json:"target_id"`
	SecretReferenceID string    `json:"secret_reference_id"`
	BindingPath       string    `json:"binding_path"`
	Version           int       `json:"version"`
	ContentHash       string    `json:"content_hash"`
	EncryptionMode    string    `json:"encryption_mode"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// DeploymentOperation mirrors deployment_operation.
type DeploymentOperation struct {
	ID               string     `json:"id"`
	Action           string     `json:"action"`
	FeatureID        string     `json:"feature_id"`
	ReleaseID        string     `json:"release_id,omitempty"`
	Strategy         string     `json:"strategy"`
	Status           string     `json:"status"`
	RequestedBy      string     `json:"requested_by"`
	Reason           string     `json:"reason,omitempty"`
	EnvironmentID    string     `json:"environment_id"`
	FrozenConfigHash string     `json:"frozen_config_hash,omitempty"`
	BackupID         string     `json:"backup_id,omitempty"`
	ForceDelete      bool       `json:"force_delete,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	StartedAt        *time.Time `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
}

// DeploymentOperationTarget mirrors deployment_operation_target: one target
// inside a deployment operation.
type DeploymentOperationTarget struct {
	ID               string     `json:"id"`
	OperationID      string     `json:"operation_id"`
	TargetID         string     `json:"target_id"`
	NodeID           string     `json:"node_id"`
	Status           string     `json:"status"`
	CurrentReleaseID string     `json:"current_release_id,omitempty"`
	DesiredReleaseID string     `json:"desired_release_id,omitempty"`
	FrozenConfigHash string     `json:"frozen_config_hash,omitempty"`
	FrozenSecretHash string     `json:"frozen_secret_hash,omitempty"`
	ErrorMessage     string     `json:"error_message,omitempty"`
	StartedAt        *time.Time `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
}

// DeploymentStep mirrors deployment_step: one hook/command step of a target.
type DeploymentStep struct {
	ID                string     `json:"id"`
	OperationID       string     `json:"operation_id"`
	OperationTargetID string     `json:"operation_target_id"`
	NodeID            string     `json:"node_id"`
	StepType          string     `json:"step_type"`
	Status            string     `json:"status"`
	CommandID         string     `json:"command_id,omitempty"`
	TaskID            string     `json:"task_id,omitempty"`
	Message           string     `json:"message,omitempty"`
	StartedAt         *time.Time `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`
}

// DeploymentBackup mirrors deployment_backup: a backup artifact produced
// before a risky operation step.
type DeploymentBackup struct {
	ID           string    `json:"id"`
	OperationID  string    `json:"operation_id"`
	TargetID     string    `json:"target_id"`
	NodeID       string    `json:"node_id"`
	FeatureID    string    `json:"feature_id"`
	BackupMode   string    `json:"backup_mode"`
	ObjectKey    string    `json:"object_key"`
	Size         int64     `json:"size"`
	SHA256       string    `json:"sha256"`
	MetadataJSON string    `json:"metadata_json,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

// BootstrapSession mirrors bootstrap_session: a node bootstrap workflow with
// an OSS-backed repository and agent install pipeline.
type BootstrapSession struct {
	ID        string     `json:"id"`
	NodeID    string     `json:"node_id"`
	Status    string     `json:"status"`
	TokenHash string     `json:"-"`
	Bucket    string     `json:"bucket"`
	Prefix    string     `json:"prefix"`
	Region    string     `json:"region"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at"`
	LastState string     `json:"last_state,omitempty"`
}
