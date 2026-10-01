//go:build !linux

package fileexec

import "errors"

func Run(_ []string) error {
	return errors.New("file search and scripted edits require Linux with Landlock and seccomp support; " +
		"use the Docker worker")
}

func RunEdit(args []string) error {
	return Run(args)
}
