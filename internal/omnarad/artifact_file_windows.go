//go:build windows

package omnarad

import "os"

func openArtifactFile(path string) (*os.File, error) {
	return os.Open(path)
}
