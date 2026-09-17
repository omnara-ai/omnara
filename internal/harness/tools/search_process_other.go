//go:build !linux

package tools

import "errors"

func RunSearchProcess(_ []string) error {
	return errors.New("memory search requires Linux with Landlock and seccomp support; use the Docker worker")
}
