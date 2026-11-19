package agentexecution

import "time"

// SetProgramToolNamedLifetime changes how long an unclaimed named call outlives its command, and
// returns a function that restores it.
func SetProgramToolNamedLifetime(lifetime time.Duration) func() {
	previous := programToolNamedLifetime
	programToolNamedLifetime = lifetime
	return func() { programToolNamedLifetime = previous }
}
