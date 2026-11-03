package memoryops

import (
	"errors"
	"os"
)

func tryLock(_ *os.File) (bool, error) {
	return false, errors.New("memory storage requires a Unix filesystem")
}

func renameFile(_ *os.File, _ string, _ *os.File, _ string) error {
	return errors.New("memory storage requires a Unix filesystem")
}
