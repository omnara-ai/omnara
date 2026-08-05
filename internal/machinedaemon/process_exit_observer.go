//go:build !windows

package machinedaemon

type processExitObserver interface {
	// Wait observes exit without reaping, preserving the leader's PID/PGID.
	// Close is serialized cleanup after Wait, not cancellation.
	Wait() error
	Close() error
}
