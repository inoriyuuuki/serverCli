// Package model defines the shared data structures persisted by ServerCLI.
//
// declarative.go adds the declarative multi-node ops (V2) entities:
// Cluster / NodeProfile / Node / ServiceReference / DesiredStateRevision /
// AppliedStateRevision / Operation (V2) / OperationStep / BackupSet /
// OSSSyncRevision / ReleaseCacheEntry / PrimaryTransfer.
//
// Identity is never derived from MAC/IP: a node is identified by cluster_id +
// node_id + node key. MAC/IP may only appear in legacy migration metadata.
package model

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// Cluster
// ---------------------------------------------------------------------------

// ClusterStatus values.
const (
	ClusterStatusActive   = "active"
	ClusterStatusDegraded = "degraded"
	ClusterStatusArchived = "archived"
)

// Cluster is the top-level declarative unit. The Control Plane is the runtime
// authority on the desired state; OSS is a one-way snapshot/backup source.
type Cluster struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	Environment         string    `json:"environment"`
	ActivePrimaryNodeID string    `json:"active_primary_node_id,omitempty"`
	PrimaryEpoch        int64     `json:"primary_epoch"`
	ReleaseChannel      string    `json:"release_channel,omitempty"`
	OSSProviderRef      string    `json:"oss_provider_ref,omitempty"`
	UpdatePolicyJSON    string    `json:"-"`
	BackupPolicyJSON    string    `json:"-"`
	Status              string    `json:"status"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// UpdatePolicy mirrors bootstrap.UpdatePolicy in model form.
type UpdatePolicy struct {
	AutoApply   bool `json:"auto_apply"`
	Maintenance bool `json:"maintenance"`
}

// BackupPolicy mirrors bootstrap.BackupPolicy in model form.
type BackupPolicy struct {
	Enabled    bool   `json:"enabled"`
	Schedule   string `json:"schedule,omitempty"`
	RemotePath string `json:"remote_path,omitempty"`
	Keep       int    `json:"keep"`
	Verify     bool   `json:"verify"`
}

// ---------------------------------------------------------------------------
// NodeProfile
// ---------------------------------------------------------------------------

// NodeProfile is a reusable, versioned assignment of modules + config +
// policies to nodes. It never stores secrets, only SecretRefs.
type NodeProfile struct {
	ID                    string    `json:"id"`
	ClusterID             string    `json:"cluster_id"`
	Name                  string    `json:"name"`
	Version               string    `json:"version"`
	ModulesJSON           string    `json:"-"`
	DefaultConfigJSON     string    `json:"-"`
	SecretRefsJSON        string    `json:"-"`
	BackupPolicyJSON      string    `json:"-"`
	UpdatePolicyJSON      string    `json:"-"`
	VerificationPolicyJSON string   `json:"-"`
	LabelsJSON            string    `json:"-"`
	ResourcesJSON         string    `json:"-"`
	Status                string    `json:"status"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// ProfileModule pins a module version inside a profile.
type ProfileModule struct {
	ModuleID     string            `json:"module_id"`
	Version      string            `json:"version,omitempty"`
	Config       map[string]string `json:"config,omitempty"`
	SecretRefs   []SecretRef       `json:"secret_refs,omitempty"`
	Dependencies []string          `json:"dependencies,omitempty"`
	ServiceRefs  []string          `json:"service_refs,omitempty"`
	RiskLevel    string            `json:"risk_level,omitempty"`
}

// SecretRef points at a secret without embedding its value (matches
// bootstrap.SecretRef shape for JSON compatibility).
type SecretRef struct {
	Key    string `json:"key"`
	Store  string `json:"store,omitempty"`
	Source string `json:"source,omitempty"`
}

// ---------------------------------------------------------------------------
// Node (declarative)
// ---------------------------------------------------------------------------

// NodeLifecycle values.
const (
	NodeLifecycleDraft          = "draft"
	NodeLifecycleBootstrapping  = "bootstrapping"
	NodeLifecycleEnrolling      = "enrolling"
	NodeLifecycleInitializing   = "initializing"
	NodeLifecycleReady          = "ready"
	NodeLifecycleDraining       = "draining"
	NodeLifecycleReprovisioning = "reprovisioning"
	NodeLifecycleDegraded       = "degraded"
	NodeLifecycleRetired        = "retired"
)

// DeclarativeNode is the declarative node record. Identity = cluster_id + node_id + key.
type DeclarativeNode struct {
	ID                  string     `json:"id"`
	ClusterID           string     `json:"cluster_id"`
	NodeID              string     `json:"node_id"`
	Role                string     `json:"role"` // primary | child
	ProfileID           string     `json:"profile_id,omitempty"`
	Lifecycle           string     `json:"lifecycle"`
	Status              string     `json:"status"`
	LabelsJSON          string     `json:"-"`
	AddressesJSON       string     `json:"-"`
	OSName              string     `json:"os_name,omitempty"`
	OSVersion           string     `json:"os_version,omitempty"`
	Arch                string     `json:"arch,omitempty"`
	DesiredRevision     string     `json:"desired_revision,omitempty"`
	AppliedRevision     string     `json:"applied_revision,omitempty"`
	IdentityGeneration  int64      `json:"identity_generation"`
	ReplacementStatus   string     `json:"replacement_status,omitempty"`
	AgentStatus         string     `json:"agent_status,omitempty"`
	LegacyMAC           string     `json:"legacy_mac,omitempty"` // migration metadata ONLY
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	RetiredAt           *time.Time `json:"retired_at,omitempty"`
}

// DeclarativeNodeAddress is a non-identity network address (for reachability only).
type DeclarativeNodeAddress struct {
	Address     string `json:"address"`
	AddressType string `json:"address_type"` // public|private|loopback
	Port        int    `json:"port,omitempty"`
	Preferred   bool   `json:"preferred,omitempty"`
}

// ---------------------------------------------------------------------------
// ServiceReference
// ---------------------------------------------------------------------------

// ServiceReference resolves a logical service dependency to a concrete
// target (node + address + port + secret). No module hardcodes remote IPs.
type ServiceReference struct {
	ID                string    `json:"id"`
	ClusterID         string    `json:"cluster_id"`
	Name              string    `json:"name"`
	ServiceInstanceID string    `json:"service_instance_id,omitempty"`
	NodeID            string    `json:"node_id,omitempty"`
	Address           string    `json:"address,omitempty"`
	Port              int       `json:"port,omitempty"`
	SecretRef         *SecretRef `json:"secret_ref,omitempty"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Desired / Applied state revisions
// ---------------------------------------------------------------------------

// DesiredStateRevision is produced by the Control Plane and synced to OSS
// (one-way; OSS never overwrites the runtime desired state).
type DesiredStateRevision struct {
	ID         string    `json:"id"`
	ClusterID  string    `json:"cluster_id"`
	Revision   int64     `json:"revision"`
	ProfileID  string    `json:"profile_id,omitempty"`
	NodeID     string    `json:"node_id,omitempty"`
	StateJSON  string    `json:"-"`
	Checksum   string    `json:"checksum,omitempty"`
	Source     string    `json:"source,omitempty"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

// AppliedStateRevision records what a node actually applied.
type AppliedStateRevision struct {
	ID          string    `json:"id"`
	ClusterID   string    `json:"cluster_id"`
	NodeID      string    `json:"node_id"`
	RevisionID  string    `json:"revision_id,omitempty"`
	Checksum    string    `json:"checksum,omitempty"`
	Status      string    `json:"status"`
	ResultJSON  string    `json:"-"`
	AppliedAt   time.Time `json:"applied_at"`
}

// ---------------------------------------------------------------------------
// Operation V2
// ---------------------------------------------------------------------------

// Operation types (V2).
const (
	OpTypeInit        = "init"
	OpTypeUpdate      = "update"
	OpTypeBackup      = "backup"
	OpTypeRestore     = "restore"
	OpTypeAdopt       = "adopt"
	OpTypeRollback    = "rollback"
	OpTypeVerify      = "verify"
	OpTypeProvision   = "provision"
	OpTypePrimaryTransfer = "primary_transfer"
)

// Operation statuses (V2 state machine).
const (
	OpStatusPlanned        = "planned"
	OpStatusAwaitingApproval = "awaiting_approval"
	OpStatusQueued         = "queued"
	OpStatusDispatched     = "dispatched"
	OpStatusRunning        = "running"
	OpStatusVerifying      = "verifying"
	OpStatusSucceeded      = "succeeded"
	OpStatusFailed         = "failed"
	OpStatusRollingBack    = "rolling_back"
	OpStatusRolledBack     = "rolled_back"
	OpStatusCancelled      = "cancelled"
	OpStatusResultUnknown  = "result_unknown"
)

// Risk levels.
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
	RiskCritical = "critical"
)

// IsOperationTerminal reports whether an operation status is final.
func IsOperationTerminal(s string) bool {
	switch s {
	case OpStatusSucceeded, OpStatusFailed, OpStatusRolledBack, OpStatusCancelled, OpStatusResultUnknown:
		return true
	}
	return false
}

// Operation is the structured V2 operation. All fields are explicit; agents
// receive arguments via a 0600 JSON context file and secrets via a 0700
// secret dir (never argv).
type Operation struct {
	ID                string     `json:"id"`
	OperationID       string     `json:"operation_id"`
	OperationType     string     `json:"operation_type"`
	ClusterID         string     `json:"cluster_id,omitempty"`
	NodeID            string     `json:"node_id,omitempty"`
	ModuleID          string     `json:"module_id,omitempty"`
	ServiceInstanceID string     `json:"service_instance_id,omitempty"`
	DesiredRevision   string     `json:"desired_revision,omitempty"`
	ArgumentsJSON     string     `json:"-"`
	Approval          string     `json:"approval,omitempty"` // pending|approved|rejected|auto
	RiskLevel         string     `json:"risk_level,omitempty"`
	IdempotencyKey    string     `json:"-"`
	Deadline          *time.Time `json:"deadline,omitempty"`
	PrimaryEpoch      int64      `json:"primary_epoch"`
	Status            string     `json:"status"`
	RequestedBy       string     `json:"requested_by,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	ErrorCode         string     `json:"error_code,omitempty"`
	ErrorMessage      string     `json:"error_message,omitempty"`
	// RequestFingerprint is the canonical fingerprint of the originating
	// OperationRequest used for idempotency replay; never serialized.
	RequestFingerprint string `json:"-"`
}

// OperationStep is one step of an operation, with attempt + commit point for
// idempotent resumption.
type OperationStep struct {
	ID            string     `json:"id"`
	OperationID   string     `json:"operation_id"`
	Sequence      int        `json:"sequence"`
	ModuleID      string     `json:"module_id,omitempty"`
	Operation     string     `json:"operation,omitempty"`
	Attempt       int        `json:"attempt"`
	CommitPoint   string     `json:"commit_point,omitempty"`
	Status        string     `json:"status"`
	ErrorType     string     `json:"error_type,omitempty"`
	Message       string     `json:"message,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// ---------------------------------------------------------------------------
// Backup / OSS sync
// ---------------------------------------------------------------------------

// BackupSet tracks a verified backup uploaded to OSS.
type BackupSet struct {
	ID                string    `json:"id"`
	BackupID          string    `json:"backup_id"`
	RecoverySetID     string    `json:"recovery_set_id,omitempty"`
	ClusterID         string    `json:"cluster_id,omitempty"`
	NodeID            string    `json:"node_id,omitempty"`
	ServiceInstanceID string    `json:"service_instance_id,omitempty"`
	ModuleVersion     string    `json:"module_version,omitempty"`
	AppVersion        string    `json:"app_version,omitempty"`
	SchemaVersion     string    `json:"schema_version,omitempty"`
	FilesJSON         string    `json:"-"`
	SHA256            string    `json:"sha256,omitempty"`
	SizeBytes         int64     `json:"size_bytes"`
	OSSKey            string    `json:"oss_key,omitempty"`
	Status            string    `json:"status"` // verified|legacy|unverified|restored
	CreatedAt         time.Time `json:"created_at"`
}

// OSSSyncRevision records a one-way config/backup sync to OSS.
type OSSSyncRevision struct {
	ID         string    `json:"id"`
	ClusterID  string    `json:"cluster_id,omitempty"`
	Kind       string    `json:"kind"` // desired_state|backup|release_cache
	ObjectKey  string    `json:"object_key"`
	SHA256     string    `json:"sha256,omitempty"`
	Direction  string    `json:"direction"` // upload|download
	Status     string    `json:"status"`    // uploaded|verified|failed
	Etag       string    `json:"etag,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
}

// ---------------------------------------------------------------------------
// Release Cache
// ---------------------------------------------------------------------------

// ReleaseCacheStatus values.
const (
	ReleaseCachePending   = "pending"
	ReleaseCacheAvailable = "available"
	ReleaseCacheFailed    = "failed"
	ReleaseCacheArchived  = "archived"
)

// ReleaseCacheEntry records one mirrored release artifact in OSS.
type ReleaseCacheEntry struct {
	ID               string    `json:"id"`
	Version          string    `json:"version"`
	SourceRepository string    `json:"source_repository,omitempty"`
	SourceRelease    string    `json:"source_release,omitempty"`
	OS               string    `json:"os,omitempty"`
	Arch             string    `json:"arch,omitempty"`
	ArtifactName     string    `json:"artifact_name"`
	ArtifactSize     int64     `json:"artifact_size"`
	SHA256           string    `json:"sha256"`
	ModulesVersion   string    `json:"modules_version,omitempty"`
	SchemaMin        string    `json:"schema_min,omitempty"`
	SchemaMax        string    `json:"schema_max,omitempty"`
	OSSKey           string    `json:"oss_key,omitempty"`
	Status           string    `json:"status"`
	UploadedAt       *time.Time `json:"uploaded_at,omitempty"`
	VerifiedAt       *time.Time `json:"verified_at,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// ---------------------------------------------------------------------------
// PrimaryTransfer
// ---------------------------------------------------------------------------

// PrimaryTransfer statuses.
const (
	TransferPrimaryActive      = "primary_active"
	TransferPlanning           = "transfer_planning"
	TransferCandidatePreparing = "candidate_preparing"
	TransferMaintenance        = "maintenance"
	TransferFinalBackup        = "final_backup"
	TransferCandidateRestoring = "candidate_restoring"
	TransferCandidateVerifying = "candidate_verifying"
	TransferCutover            = "cutover"
	TransferNewPrimaryActive   = "new_primary_active"
	TransferOldPrimaryDemoting = "old_primary_demoting"
	TransferCompleted          = "completed"
	TransferFailed             = "failed"
	TransferRollbackRequired   = "rollback_required"
)

// PrimaryTransfer is a planned primary node handover. All control operations
// must validate primary_epoch.
type PrimaryTransfer struct {
	ID             string     `json:"id"`
	ClusterID      string     `json:"cluster_id"`
	FromNodeID     string     `json:"from_node_id"`
	ToNodeID       string     `json:"to_node_id"`
	PrimaryEpoch   int64      `json:"primary_epoch"` // epoch AFTER transfer
	Status         string     `json:"status"`
	BackupSetID    string     `json:"backup_set_id,omitempty"`
	StepsJSON      string     `json:"-"`
	ErrorCode      string     `json:"error_code,omitempty"`
	ErrorMessage   string     `json:"error_message,omitempty"`
	RequestedBy    string     `json:"requested_by,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

// UnmarshalProfileModules decodes ModulesJSON into []ProfileModule.
func (p *NodeProfile) UnmarshalProfileModules() ([]ProfileModule, error) {
	if p.ModulesJSON == "" {
		return nil, nil
	}
	var out []ProfileModule
	if err := json.Unmarshal([]byte(p.ModulesJSON), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MarshalProfileModules encodes []ProfileModule into ModulesJSON.
func (p *NodeProfile) MarshalProfileModules(mods []ProfileModule) error {
	b, err := json.Marshal(mods)
	if err != nil {
		return err
	}
	p.ModulesJSON = string(b)
	return nil
}

// UnmarshalNodeAddresses decodes AddressesJSON into []NodeAddress.
func (n *DeclarativeNode) UnmarshalNodeAddresses() ([]DeclarativeNodeAddress, error) {
	if n.AddressesJSON == "" {
		return nil, nil
	}
	var out []DeclarativeNodeAddress
	if err := json.Unmarshal([]byte(n.AddressesJSON), &out); err != nil {
		return nil, err
	}
	return out, nil
}
