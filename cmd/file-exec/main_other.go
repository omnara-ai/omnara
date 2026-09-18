//go:build !linux

package main

import "errors"

func run(_ []string) error {
	return errors.New("file scripts and memory search require Linux with Landlock and seccomp support; " +
		"use the Docker worker")
}
