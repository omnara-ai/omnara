package modelprotocol

import (
	"fmt"
	"hash/fnv"
	"time"
)

func RetryBackoff(attemptNumber int, contextID string) time.Duration {
	if attemptNumber < 1 {
		attemptNumber = 1
	}
	delay := time.Second
	for i := 1; i < attemptNumber && delay < 30*time.Second; i++ {
		delay *= 2
	}

	percent := deterministicModelCallRetryJitterPercent(contextID, attemptNumber)
	delay = time.Duration(int64(delay) * int64(percent) / 100)
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

func deterministicModelCallRetryJitterPercent(contextID string, attemptNumber int) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(fmt.Sprintf("%s:%d", contextID, attemptNumber)))
	return 80 + int(hash.Sum32()%41)
}
