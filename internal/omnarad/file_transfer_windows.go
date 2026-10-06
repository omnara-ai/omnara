//go:build windows

package omnarad

import "os"

func openTransferFile(path string) (*os.File, error) {
	return os.Open(path)
}
