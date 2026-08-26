package service

// deploymentAuditWhitelist is the only set of keys allowed to appear in
// deployment audit details. Everything else (secret bodies, config YAML/JSON
// text, OSS credentials, object keys, feature descriptions, ...) is dropped
// so that sensitive material can never reach the audit trail.
var deploymentAuditWhitelist = map[string]bool{
	"feature_key":         true,
	"release_version":     true,
	"node_id":             true,
	"target_id":           true,
	"operation_id":        true,
	"backup_id":           true,
	"config_hash":         true,
	"secret_reference_id": true,
	"secret_version":      true,
	"secret_hash":         true,
	"encryption_mode":     true,
	"action":              true,
	"result":              true,
	"reason_length":       true,
	"object_count":        true,
	"name":                true,
}

// DeploymentAuditDetails filters a details map down to the deployment audit
// whitelist. Any key that is not explicitly allowlisted is discarded. Callers
// should use this for every deployment audit event so that secret material,
// configuration text and OSS credentials can never leak into audit storage.
func DeploymentAuditDetails(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		if deploymentAuditWhitelist[k] {
			out[k] = v
		}
	}
	return out
}
