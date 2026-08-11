package bootstrapv2

import "testing"

func TestAllowedTransitionsFullTable(t *testing.T) {
	states := []string{
		StateEmpty,
		StatePreflight,
		StateReleaseDownloading,
		StateReleaseVerified,
		StateReleaseInstalled,
		StateFoundationPlanning,
		StateFoundationApplying,
		StateControlPlaneReady,
		StateAgentReady,
		StateOSSSyncReady,
		StateReady,
		StateFailed,
		StateRecoveryRequired,
	}
	want := map[string]map[string]bool{
		StateEmpty:              {StatePreflight: true, StateFailed: true},
		StatePreflight:          {StateReleaseDownloading: true, StateFailed: true},
		StateReleaseDownloading: {StateReleaseVerified: true, StateFailed: true},
		StateReleaseVerified:    {StateReleaseInstalled: true, StateFailed: true},
		StateReleaseInstalled:   {StateFoundationPlanning: true, StateFailed: true},
		StateFoundationPlanning: {StateFoundationApplying: true, StateFailed: true},
		StateFoundationApplying: {StateControlPlaneReady: true, StateFailed: true},
		StateControlPlaneReady:  {StateAgentReady: true, StateFailed: true},
		StateAgentReady:         {StateOSSSyncReady: true, StateFailed: true},
		StateOSSSyncReady:       {StateReady: true, StateFailed: true},
		StateReady:              {StateFailed: true},
		StateFailed:             {StateRecoveryRequired: true},
		StateRecoveryRequired:   {StateFailed: true},
	}
	for _, from := range states {
		for _, to := range states {
			got := CanTransition(from, to)
			if got != want[from][to] {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", from, to, got, want[from][to])
			}
			if err := ValidateTransition(from, to); (err == nil) != want[from][to] {
				t.Errorf("ValidateTransition(%q, %q) error = %v", from, to, err)
			}
		}
	}
}

func TestInvalidAndUnknownTransitionsRejected(t *testing.T) {
	cases := [][2]string{
		{StateEmpty, StateReady},
		{StateReleaseInstalled, StatePreflight},
		{StateFailed, StateReady},
		{"unknown", StateFailed},
		{StateEmpty, "unknown"},
	}
	for _, tc := range cases {
		if err := ValidateTransition(tc[0], tc[1]); err == nil {
			t.Fatalf("ValidateTransition(%q, %q) succeeded", tc[0], tc[1])
		}
	}
}

func TestFailedTransitionsToRecoveryRequired(t *testing.T) {
	if !CanTransition(StateFailed, StateRecoveryRequired) {
		t.Fatal("failed -> recovery_required must be allowed")
	}
	if err := ValidateTransition(StateFailed, StateRecoveryRequired); err != nil {
		t.Fatal(err)
	}
}
