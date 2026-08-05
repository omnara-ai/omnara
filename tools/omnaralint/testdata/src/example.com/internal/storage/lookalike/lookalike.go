package lookalike

import "time"

type MutationInput struct {
	ExpiresAt time.Time
}

func mutate() time.Time {
	return time.Now()
}
