package identitystore

import "time"

const (
	browserSessionTouchInterval = 5 * time.Minute
	bearerTokenTouchInterval    = time.Hour
)
