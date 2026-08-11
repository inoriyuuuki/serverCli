package opsv2

import (
	"fmt"

	"servercli/internal/model"
)

// AllowedTransitions defines every valid Operation V2 status transition.
// Approval and rejection are decisions represented by transitions to queued
// and cancelled respectively; they are not additional persisted statuses.
var AllowedTransitions = map[string][]string{
	model.OpStatusPlanned: {
		model.OpStatusAwaitingApproval,
		model.OpStatusQueued,
		model.OpStatusCancelled,
	},
	model.OpStatusAwaitingApproval: {
		model.OpStatusQueued,
		model.OpStatusCancelled,
	},
	model.OpStatusQueued: {
		model.OpStatusDispatched,
		model.OpStatusCancelled,
	},
	model.OpStatusDispatched: {
		model.OpStatusRunning,
		model.OpStatusResultUnknown,
		model.OpStatusCancelled,
	},
	model.OpStatusRunning: {
		model.OpStatusVerifying,
		model.OpStatusFailed,
		model.OpStatusRollingBack,
	},
	model.OpStatusVerifying: {
		model.OpStatusSucceeded,
		model.OpStatusFailed,
		model.OpStatusRollingBack,
	},
	model.OpStatusRollingBack: {
		model.OpStatusRolledBack,
		model.OpStatusFailed,
	},
	model.OpStatusSucceeded:     {},
	model.OpStatusFailed:        {},
	model.OpStatusRolledBack:    {},
	model.OpStatusCancelled:     {},
	model.OpStatusResultUnknown: {},
}

// CanTransition reports whether to is an explicitly allowed successor of
// from. Re-applying the same status is not a state transition.
func CanTransition(from, to string) bool {
	for _, allowed := range AllowedTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ValidateTransition returns an error unless the transition is allowed by the
// Operation V2 state machine.
func ValidateTransition(from, to string) error {
	if _, ok := AllowedTransitions[from]; !ok {
		return fmt.Errorf("unknown operation status %q", from)
	}
	if _, ok := AllowedTransitions[to]; !ok {
		return fmt.Errorf("unknown operation status %q", to)
	}
	if !CanTransition(from, to) {
		return fmt.Errorf("operation status transition %q -> %q is not allowed", from, to)
	}
	return nil
}

// ValidateEpoch prevents a control operation prepared under one primary epoch
// from executing under another. Epochs are positive and must match exactly.
func ValidateEpoch(opEpoch, clusterEpoch int64) error {
	if opEpoch <= 0 {
		return fmt.Errorf("operation primary_epoch must be greater than zero: %d", opEpoch)
	}
	if clusterEpoch <= 0 {
		return fmt.Errorf("cluster primary_epoch must be greater than zero: %d", clusterEpoch)
	}
	if opEpoch != clusterEpoch {
		return fmt.Errorf("stale primary_epoch: operation=%d cluster=%d", opEpoch, clusterEpoch)
	}
	return nil
}
