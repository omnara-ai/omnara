//go:build !windows && !linux && !darwin

package machinedaemon

import (
	"errors"
)

func supportsSafeProcessExitObservation() bool {
	return false
}

func newProcessExitObserver(int) (processExitObserver, error) {
	return nil, errors.New(
		"safe process exit observation is not implemented on this Unix platform",
	)
}
