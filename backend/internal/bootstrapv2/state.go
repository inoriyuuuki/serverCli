// Package bootstrapv2 implements the OSS-first bootstrap flow for the first
// primary servercli node. It deliberately remains database-free and persists
// progress through initstate commit points.
package bootstrapv2

import "fmt"

const (
	StateEmpty              = "empty"
	StatePreflight          = "preflight_passed"
	StateReleaseDownloading = "release_downloading"
	StateReleaseVerified    = "release_verified"
	StateReleaseInstalled   = "release_installed"
	StateFoundationPlanning = "foundation_planning"
	StateFoundationApplying = "foundation_applying"
	StateControlPlaneReady  = "control_plane_ready"
	StateAgentReady         = "agent_ready"
	StateOSSSyncReady       = "oss_sync_ready"
	StateReady              = "ready"
	StateFailed             = "failed"
	StateRecoveryRequired   = "recovery_required"
)

// AllowedTransitions is the complete Primary Bootstrap state graph. Normal
// states advance one step at a time and may fail. A failed bootstrap may only
// enter recovery_required; recovery_required may fail again.
var AllowedTransitions = map[string][]string{
	StateEmpty:              {StatePreflight, StateFailed},
	StatePreflight:          {StateReleaseDownloading, StateFailed},
	StateReleaseDownloading: {StateReleaseVerified, StateFailed},
	StateReleaseVerified:    {StateReleaseInstalled, StateFailed},
	StateReleaseInstalled:   {StateFoundationPlanning, StateFailed},
	StateFoundationPlanning: {StateFoundationApplying, StateFailed},
	StateFoundationApplying: {StateControlPlaneReady, StateFailed},
	StateControlPlaneReady:  {StateAgentReady, StateFailed},
	StateAgentReady:         {StateOSSSyncReady, StateFailed},
	StateOSSSyncReady:       {StateReady, StateFailed},
	StateReady:              {StateFailed},
	StateFailed:             {StateRecoveryRequired},
	StateRecoveryRequired:   {StateFailed},
}

// CanTransition reports whether from -> to is an edge in the Primary
// Bootstrap state graph.
func CanTransition(from, to string) bool {
	for _, next := range AllowedTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// ValidateTransition rejects unknown states and attempts to skip or reverse a
// phase.
func ValidateTransition(from, to string) error {
	if _, ok := AllowedTransitions[from]; !ok {
		return fmt.Errorf("bootstrapv2: unknown source state %q", from)
	}
	if _, ok := AllowedTransitions[to]; !ok {
		return fmt.Errorf("bootstrapv2: unknown target state %q", to)
	}
	if !CanTransition(from, to) {
		return fmt.Errorf("bootstrapv2: invalid state transition %q -> %q", from, to)
	}
	return nil
}
