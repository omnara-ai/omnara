//go:build !windows

package omnarad

import (
	"os"

	"golang.org/x/sys/unix"
)

func openArtifactFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
