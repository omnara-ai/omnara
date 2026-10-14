package executionpolicy

import (
	"fmt"
	"time"
)

const (
	MaxPendingAgentContentInputs    = 50
	MaxModelCallRetriesPerOperation = 8
	DefaultLeaseDuration            = 90 * time.Second
	MinimumLeaseDuration            = 15 * time.Second
	MaximumLeaseDuration            = 30 * time.Minute
	InterruptedToolMessage          = "Tool call was interrupted; its external outcome is unknown."
)

func ValidateLeaseDuration(duration time.Duration) error {
	if duration < MinimumLeaseDuration || duration > MaximumLeaseDuration {
		return fmt.Errorf("runtime lease duration must be between %s and %s", MinimumLeaseDuration, MaximumLeaseDuration)
	}
	return nil
}
