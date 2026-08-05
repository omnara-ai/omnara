package daemonprotocol

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

const (
	MinimumSleepAfter                  = 30 * time.Second
	MaximumSleepAfterMS                = math.MaxInt64 / int64(time.Millisecond)
	WakeListenerPort                   = 8377
	SleepAfterEnvVar                   = "OMNARA_DAEMON_SLEEP_AFTER_MS"
	WakeListenAddrEnvVar               = "OMNARA_DAEMON_WAKE_LISTEN_ADDR"
	SleepPlatformEnvVar                = "OMNARA_DAEMON_SLEEP_PLATFORM"
	SleepPlatformBlaxel                = "blaxel"
	SleepPlatformUnikraft              = "unikraft"
	BlaxelLocalAPIURL                  = "http://127.0.0.1:8080"
	BlaxelAwakeProcessNamePrefix       = "omnara-awake-"
	UnikraftScaleToZeroControlFilePath = "/uk/libukp/scale_to_zero_disable"
)

func SleepAfterDuration(milliseconds int) (time.Duration, error) {
	minimumMilliseconds := int64(MinimumSleepAfter / time.Millisecond)
	value := int64(milliseconds)
	if value < minimumMilliseconds {
		return 0, fmt.Errorf("must be at least %d", minimumMilliseconds)
	}
	if value > MaximumSleepAfterMS {
		return 0, fmt.Errorf("must be at most %d", MaximumSleepAfterMS)
	}
	return time.Duration(milliseconds) * time.Millisecond, nil
}

func BlaxelAwakeProcessName(supervisorPID int) string {
	return BlaxelAwakeProcessNamePrefix + strconv.Itoa(supervisorPID)
}
