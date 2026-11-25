// Package testfs creates directories with the filesystem semantics required by
// memory storage, including case-sensitive names on Windows.
package testfs

import "testing"

func TempDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	Prepare(t, dir)
	return dir
}
