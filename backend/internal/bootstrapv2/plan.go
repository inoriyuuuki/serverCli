package bootstrapv2

// Plan is the read-only description of a Primary Bootstrap execution.
type Plan struct {
	Version             string            `json:"version"`
	Phases              []string          `json:"phases"`
	Artifacts           []string          `json:"artifacts"`
	CommitPoints        map[string]string `json:"commit_points"`
	OSSBucket           string            `json:"oss_bucket"`
	ClusterID           string            `json:"cluster_id"`
	NodeName            string            `json:"node_name"`
	Profile             string            `json:"profile"`
	RequiresGitHubToken bool              `json:"requires_github_token"`
	Warnings            []string          `json:"warnings,omitempty"`
}
