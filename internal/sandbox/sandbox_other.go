//go:build !linux

package sandbox

import "errors"

func Run(_ []string) error {
	return errors.New("file search and scripted edits require Linux with Landlock and seccomp support; " +
		"use the Docker worker")
}

func RunChroot(args []string) error {
	return Run(args)
}
