package dbsqlc

import "time"

type GeneratedInput struct {
	ExpiresAt time.Time
}

func generatedNow() time.Time {
	return time.Now()
}
