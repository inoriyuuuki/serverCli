// Package bootstrap defines stable public contracts shared by the servercli
// bootstrap/init subsystem: exit codes, signed bundle/release manifests,
// inventory and service ownership types.
//
// The servercli CLI must never require a database, Docker, Gitea or an
// existing Control Plane: it is the first thing a fresh CentOS/RHEL server
// runs after the public installer places the binaries and modules.
package bootstrap

import "time"

// Stable process exit codes. These are part of the public contract and must
// not be renumbered.
const (
	ExitOK         = 0 // success
	ExitUsage      = 2 // 参数错误（用法/参数/校验）
	ExitPreflight  = 3 // 预检失败（OS/arch/DNS/端口/ownership 等）
	ExitSignature  = 4 // 签名/认证失败（Release/Bundle 验签、age 解密、claim 鉴权）
	ExitNetwork    = 5 // 暂时网络失败（可重试）
	ExitModule     = 6 // 模块失败
	ExitPartial    = 7 // 部分成功（部分模块成功、部分失败）
	ExitBlocked    = 8 // blocked（并发 init、owner 冲突、无 ownership 元数据、需要人工决策）
	ExitManual     = 9 // 需要人工处理（凭据轮换、DNS、防火墙等外部动作）
)

// ExitCodeName returns the stable machine-readable name of an exit code.
func ExitCodeName(code int) string {
	switch code {
	case ExitOK:
		return "ok"
	case ExitUsage:
		return "usage_error"
	case ExitPreflight:
		return "preflight_failed"
	case ExitSignature:
		return "signature_failed"
	case ExitNetwork:
		return "network_failed"
	case ExitModule:
		return "module_failed"
	case ExitPartial:
		return "partial_success"
	case ExitBlocked:
		return "blocked"
	case ExitManual:
		return "manual_action_required"
	default:
		return "unknown"
	}
}

// SecretRef points at a secret without ever embedding its value. Values must
// never be rendered into argv, ps, logs, state, git or release assets.
type SecretRef struct {
	Key    string `json:"key" yaml:"key"`       // logical key, e.g. postgres.password
	Store  string `json:"store" yaml:"store"`   // bootstrap | vault (formal secret store)
	Source string `json:"source" yaml:"source"` // bundle field path or external ref
}

// BundleManifest is the independent signed manifest describing an encrypted
// OSS bundle. The bundle payload itself is SOPS/age encrypted; this manifest
// is what resume/repair/import verify and what replay protection checks.
type BundleManifest struct {
	SchemaVersion           string     `json:"schema_version" yaml:"schema_version"`
	BundleID                string     `json:"bundle_id" yaml:"bundle_id"`
	BundleVersion           string     `json:"bundle_version" yaml:"bundle_version"`
	Environment             string     `json:"environment" yaml:"environment"`
	TargetNode              string     `json:"target_node" yaml:"target_node"`
	TargetRole              string     `json:"target_role" yaml:"target_role"`
	CreatedAt               time.Time  `json:"created_at" yaml:"created_at"`
	MinimumBootstrapVersion string     `json:"minimum_bootstrap_version" yaml:"minimum_bootstrap_version"`
	PayloadDigest           string     `json:"payload_digest" yaml:"payload_digest"`
	ExpiresAt               *time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
	// Signature is a base64 Ed25519 signature over the canonical JSON of the
	// manifest excluding the signature field itself. SigningKeyID identifies
	// which release public key to use.
	Signature    string `json:"signature" yaml:"signature"`
	SigningKeyID string `json:"signing_key_id,omitempty" yaml:"signing_key_id,omitempty"`
}

// Artifact is one file in a signed release manifest.
type Artifact struct {
	Path  string `json:"path" yaml:"path"`   // path inside the release bundle
	Kind  string `json:"kind" yaml:"kind"`   // binary|module|template|schema|installer|manifest
	SHA256 string `json:"sha256" yaml:"sha256"`
	Size  int64  `json:"size" yaml:"size"`
}

// SchemaCompat declares the database/state schema compatibility window of a
// release: which schema versions may be upgraded from, and whether the
// migration is reversible. Irreversible migrations must never be auto-rolled
// back to an older binary.
type SchemaCompat struct {
	MinSchemaVersion string `json:"min_schema_version" yaml:"min_schema_version"`
	MaxSchemaVersion string `json:"max_schema_version" yaml:"max_schema_version"`
	Reversible       bool   `json:"reversible" yaml:"reversible"`
	Notes            string `json:"notes,omitempty" yaml:"notes,omitempty"`
}

// ReleaseManifest is signed by the same release public key for GitHub and OSS
// mirrors, so both download sources verify to the same trust root. It carries
// every artifact (installer, three binaries, modules, templates, schema) with
// digests; the manifest signature is the trust anchor, never bare SHA256.
type ReleaseManifest struct {
	SchemaVersion  string         `json:"schema_version" yaml:"schema_version"`
	ReleaseVersion string         `json:"release_version" yaml:"release_version"`
	CreatedAt      time.Time      `json:"created_at" yaml:"created_at"`
	SigningKeyID   string         `json:"signing_key_id" yaml:"signing_key_id"`
	Artifacts      []Artifact     `json:"artifacts" yaml:"artifacts"`
	SchemaCompat   SchemaCompat   `json:"schema_compat" yaml:"schema_compat"`
	Signature      string         `json:"signature" yaml:"signature"`
}

// InventoryNode identifies a node without relying on MAC address.
type InventoryNode struct {
	Name    string `json:"name" yaml:"name"`
	Role    string `json:"role" yaml:"role"`
	Profile string `json:"profile,omitempty" yaml:"profile,omitempty"`
}

// ServiceSpec declares service assignment, dependencies and manager/owner.
type ServiceSpec struct {
	DependsOn []string `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
	Manager   string   `json:"manager" yaml:"manager"`   // servercli | legacy-init | none
	Owner     string   `json:"owner" yaml:"owner"`       // see ownership package
	Phase     string   `json:"phase,omitempty" yaml:"phase,omitempty"` // foundation-core|foundation-services|services
}

// InventoryNetwork holds the private network topology. MAC addresses may only
// be used for migration info and anomaly hints, never as identity/authorization.
type InventoryNetwork struct {
	Domain      string   `json:"domain" yaml:"domain"`
	PublicIP    string   `json:"public_ip" yaml:"public_ip"`
	PrivateIPs  []string `json:"private_ips,omitempty" yaml:"private_ips,omitempty"`
	CaddyBridge string   `json:"caddy_bridge,omitempty" yaml:"caddy_bridge,omitempty"`
}

// BackupPolicy / UpdatePolicy / RestorePolicy control the node-local schedule.
type BackupPolicy struct {
	Enabled     bool   `json:"enabled" yaml:"enabled"`
	Schedule    string `json:"schedule" yaml:"schedule"` // systemd timer calendar, e.g. "*-*-* 02:30:00"
	RemotePath  string `json:"remote_path" yaml:"remote_path"`
	Keep        int    `json:"keep" yaml:"keep"`
	Verify      bool   `json:"verify" yaml:"verify"`
}
type UpdatePolicy struct {
	AutoApply     bool   `json:"auto_apply" yaml:"auto_apply"`
	Maintenance   bool   `json:"maintenance" yaml:"maintenance"` // 更新前维护模式/写冻结
}
type RestorePolicy struct {
	RequireExplicitID bool `json:"require_explicit_id" yaml:"require_explicit_id"`
}

// Inventory is the decrypted private inventory stored under
// /etc/servercli/private/cluster.yaml. It is a YAML document; secrets are
// referenced by SecretRef and never inlined.
type Inventory struct {
	SchemaVersion string           `json:"schema_version" yaml:"schema_version"`
	Environment   string           `json:"environment" yaml:"environment"`
	Node          InventoryNode    `json:"node" yaml:"node"`
	Services      map[string]ServiceSpec `json:"services" yaml:"services"`
	Network       InventoryNetwork `json:"network" yaml:"network"`
	Backup        BackupPolicy     `json:"backup" yaml:"backup"`
	Update        UpdatePolicy     `json:"update" yaml:"update"`
	Restore       RestorePolicy    `json:"restore" yaml:"restore"`
	Secrets       map[string]SecretRef `json:"secrets" yaml:"secrets"`
	// Owners maps service -> owner state; authoritative copy lives in the
	// ownership package but is mirrored here for portability.
	Owners map[string]string `json:"owners,omitempty" yaml:"owners,omitempty"`
	// LegacyMAC is migration metadata only: MAC -> node. Never used for
	// identity, role, install authorization, enrollment or secret grants.
	LegacyMAC map[string]string `json:"legacy_mac,omitempty" yaml:"legacy_mac,omitempty"`
}

// Fixed directory layout. Root-owned; kept together so every component agrees.
const (
	DirEtcServerCLI   = "/etc/servercli"
	DirEtcPrivate     = "/etc/servercli/private"
	DirEtcServicesD   = "/etc/servercli/private/services.d"
	DirEtcKeys        = "/etc/servercli/keys"
	DirVarLibServerCLI = "/var/lib/servercli"
	DirVarBootstrap   = "/var/lib/servercli/bootstrap"
	DirVarPostgres    = "/var/lib/servercli/postgres"
	DirVarState       = "/var/lib/servercli/state"
	DirVarBackups     = "/var/lib/servercli/backups"
	DirRunServerCLI   = "/run/servercli"
	DirRunBootstrap   = "/run/servercli/bootstrap"
	DirRunOperations  = "/run/servercli/operations"
	FileBootstrapAgeKey = "/etc/servercli/keys/bootstrap.agekey"
	FileMasterKey       = "/etc/servercli/keys/master.key"
	FileStateJSON       = "/var/lib/servercli/bootstrap/state.json"
	FileSecretsEnc      = "/var/lib/servercli/bootstrap/secrets.enc"
	FileClusterYAML     = "/etc/servercli/private/cluster.yaml"
)
