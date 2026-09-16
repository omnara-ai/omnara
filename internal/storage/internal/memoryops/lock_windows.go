package memoryops

import (
	"errors"
	"os"
)

func tryLock(_ *os.File) (bool, error) {
	return false, errors.New("memory storage requires a Unix filesystem")
}
