//go:build windows

package localstore

import "os"

func syncDirBestEffort(path string) error {
	return nil
}

func validatePrivateFile(os.FileInfo) error {
	return nil
}
