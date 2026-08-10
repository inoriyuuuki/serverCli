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
