package timestampownership

import "time"

type TestOnlyInput struct {
	ExpiresAt time.Time
}

func testOnlyNow() time.Time {
	return time.Now()
}
