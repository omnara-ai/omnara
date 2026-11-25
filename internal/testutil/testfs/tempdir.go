package testfs

import "testing"

func TempDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	Prepare(t, dir)
	return dir
}
