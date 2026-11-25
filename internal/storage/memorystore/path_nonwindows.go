//go:build !windows

package memorystore

func validateNativePath(string) error { return nil }
