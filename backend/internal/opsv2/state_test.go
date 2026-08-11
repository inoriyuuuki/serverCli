package opsv2

import (
	"testing"

	"servercli/internal/model"
)

func TestAllowedTransitions(t *testing.T) {
	for from, destinations := range AllowedTransitions {
		for _, to := range destinations {
			if !CanTransition(from, to) {
				t.Errorf("CanTransition(%q, %q) = false", from, to)
			}
			if err := ValidateTransition(from, to); err != nil {
				t.Errorf("ValidateTransition(%q, %q) = %v", from, to, err)
			}
		}
	}
}

func TestInvalidTransitions(t *testing.T) {
	tests := [][2]string{
		{model.OpStatusPlanned, model.OpStatusSucceeded},
		{model.OpStatusRunning, model.OpStatusSucceeded},
		{model.OpStatusSucceeded, model.OpStatusRunning},
		{model.OpStatusFailed, model.OpStatusRollingBack},
		{model.OpStatusQueued, model.OpStatusQueued},
		{"unknown", model.OpStatusQueued},
		{model.OpStatusQueued, "unknown"},
	}
	for _, tt := range tests {
		if CanTransition(tt[0], tt[1]) {
			t.Errorf("CanTransition(%q, %q) = true", tt[0], tt[1])
		}
		if err := ValidateTransition(tt[0], tt[1]); err == nil {
			t.Errorf("ValidateTransition(%q, %q) unexpectedly succeeded", tt[0], tt[1])
		}
	}
}

func TestTerminalStatusesHaveNoTransitions(t *testing.T) {
	for _, status := range []string{
		model.OpStatusSucceeded,
		model.OpStatusFailed,
		model.OpStatusRolledBack,
		model.OpStatusCancelled,
		model.OpStatusResultUnknown,
	} {
		if !model.IsOperationTerminal(status) {
			t.Fatalf("test status %q is not terminal", status)
		}
		if got := len(AllowedTransitions[status]); got != 0 {
			t.Errorf("terminal status %q has %d transitions", status, got)
		}
	}
}

func TestValidateEpoch(t *testing.T) {
	if err := ValidateEpoch(8, 8); err != nil {
		t.Fatalf("matching epoch rejected: %v", err)
	}
	for _, pair := range [][2]int64{{7, 8}, {0, 8}, {8, 0}, {-1, 8}} {
		if err := ValidateEpoch(pair[0], pair[1]); err == nil {
			t.Errorf("ValidateEpoch(%d, %d) unexpectedly succeeded", pair[0], pair[1])
		}
	}
}
