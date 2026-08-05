//go:build !windows && !linux && !darwin

package machinedaemon

import "errors"

func processGroupHasLiveMembers(int) (bool, error) {
	return false, errors.New(
		"live process-group member observation is not implemented on this Unix platform",
	)
}
