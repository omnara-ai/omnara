//go:build integration

package storage

import "time"

func WithModelCallRetryBackoff(backoff func(int, string) time.Duration) Option {
	return func(config *storeConfig) {
		config.modelCallRetryBackoff = backoff
	}
}
