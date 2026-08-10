package bundle

import (
	"fmt"

	"servercli/internal/initstate"
)

// ResumeGuard enforces that init resume only continues the exact operation
// that was recorded in the init state: the same bundle_id and the same input
// digest. A state that has never imported a bundle (both fields empty) may
// start fresh with any bundle. Any other mismatch is refused and requires an
// explicit config import plan/apply.
func ResumeGuard(state *initstate.State, bundleID, inputDigest string) error {
	if state == nil {
		return fmt.Errorf("resume guard: nil state")
	}
	if state.BundleID == "" && state.InputDigest == "" {
		// Nothing imported yet; nothing to conflict with.
		return nil
	}
	if state.BundleID != bundleID || state.InputDigest != inputDigest {
		return fmt.Errorf("resume guard: recorded bundle_id=%q input_digest=%q does not match current bundle_id=%q input_digest=%q; run config import plan/apply explicitly",
			state.BundleID, state.InputDigest, bundleID, inputDigest)
	}
	return nil
}
