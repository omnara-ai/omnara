package testsleep

import (
	"testing"
	stdtime "time"
)

func TestSleeps(t *testing.T) {
	stdtime.Sleep(stdtime.Millisecond) // want "test sleeps hide race conditions"
}
