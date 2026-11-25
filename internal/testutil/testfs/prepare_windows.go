package testfs

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Prepare(t testing.TB, dir string) {
	t.Helper()
	setCaseSensitive(t, dir, windows.FILE_CS_FLAG_CASE_SENSITIVE_DIR)
}

func CaseInsensitiveDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	setCaseSensitive(t, dir, 0)
	return dir
}

func setCaseSensitive(t testing.TB, dir string, flags uint32) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.FILE_WRITE_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	if err := windows.SetFileInformationByHandle(handle, windows.FileCaseSensitiveInfo,
		(*byte)(unsafe.Pointer(&flags)), uint32(unsafe.Sizeof(flags))); err != nil {
		t.Fatalf("set NTFS case sensitivity: %v", err)
	}
}
