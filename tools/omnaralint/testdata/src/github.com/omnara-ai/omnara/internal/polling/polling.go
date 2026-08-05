package polling

import (
	stdtime "time"
)

func poll() {
	stdtime.Sleep(stdtime.Second) // want "production polling waits must be context-aware"
}
