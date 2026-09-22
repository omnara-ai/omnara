//go:build !linux

package fileexec

import "errors"

func Run(_ []string) error {
	return errors.New("file scripts and memory search require Linux with Landlock and seccomp support; " +
		"use the Docker worker")
}
