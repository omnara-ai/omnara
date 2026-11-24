package agentexecution

import "time"

func SetProgramToolNamedLifetime(lifetime time.Duration) func() {
	previous := programToolNamedLifetime
	programToolNamedLifetime = lifetime
	return func() { programToolNamedLifetime = previous }
}
