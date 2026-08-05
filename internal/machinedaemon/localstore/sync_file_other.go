//go:build !darwin

package localstore

import "os"

func syncFile(file *os.File) error {
	return file.Sync()
}
