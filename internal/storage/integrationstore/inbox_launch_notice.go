package integrationstore

import (
	"context"
	"encoding/json"
)

// ClaimLaunchFailureNotice permits one best-effort provider notice for a failed
// frozen launch. It never commits the recipient or consumes its recovery state.
// Claim precedes provider I/O: a crash may lose a notice, but retries cannot spam
// the conversation. The failed receipt remains the authoritative diagnosis.
func (w *IntegrationInboxLeaseTx) ClaimLaunchFailureNotice(ctx context.Context, slot string) (bool, error) {
	if err := w.checkLease(ctx); err != nil {
		return false, err
	}
	var plan map[string]struct {
		Launch json.RawMessage `json:"launch"`
	}
	if json.Unmarshal(w.record.Plan, &plan) != nil || len(plan[slot].Launch) == 0 {
		return false, inboxInvalid("failure notice requires a frozen launch")
	}
	var progress map[string]map[string]json.RawMessage
	if err := json.Unmarshal(w.record.Progress, &progress); err != nil {
		return false, err
	}
	for _, stages := range progress {
		if stages["launch_failure_notice"] != nil {
			return false, nil
		}
	}
	if progress[slot]["committed"] != nil {
		return false, nil
	}
	if err := w.writeSlotStage(ctx, slot, "launch_failure_notice", json.RawMessage(`{"attempted":true}`)); err != nil {
		return false, err
	}
	return true, nil
}
