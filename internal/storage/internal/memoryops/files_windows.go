package memoryops

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func tryLock(file *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	runtime.KeepAlive(file)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

// openRelative also accepts an empty name to reopen the held object itself,
// preserving its identity even if its directory entry has moved or changed.
func openRelative(directory *os.File, name string, access uint32) (windows.Handle, error) {
	name16, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory: windows.Handle(directory.Fd()), ObjectName: name16,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, access|windows.SYNCHRONIZE, &attributes,
		&windows.IO_STATUS_BLOCK{}, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0, 0)
	runtime.KeepAlive(directory)
	var status windows.NTStatus
	if errors.As(err, &status) {
		err = status.Errno()
	}
	return handle, err
}

func syncFile(file *os.File) error {
	// Go opens directories read-only, but FlushFileBuffers requires write access.
	handle, err := openRelative(file, "", windows.FILE_GENERIC_WRITE)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	return windows.FlushFileBuffers(handle)
}

func renameFile(source *os.File, sourceName string, destination *os.File, destinationName string) error {
	handle, err := openRelative(source, sourceName, windows.DELETE)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	name, err := windows.UTF16FromString(destinationName)
	if err != nil {
		return err
	}
	// FileRenameInformationEx (65), unlike class 10, honors POSIX replacement
	// while readers retain the old file. Only a single path component is passed.
	info := struct {
		Flags          uint32
		RootDirectory  windows.Handle
		FileNameLength uint32
		FileName       [256]uint16
	}{
		Flags:         windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS,
		RootDirectory: windows.Handle(destination.Fd()), FileNameLength: uint32((len(name) - 1) * 2),
	}
	if len(name) > len(info.FileName) {
		return fs.ErrInvalid
	}
	copy(info.FileName[:], name)
	err = windows.NtSetInformationFile(handle, &windows.IO_STATUS_BLOCK{},
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), 65)
	runtime.KeepAlive(destination)
	var status windows.NTStatus
	if errors.As(err, &status) {
		return status.Errno()
	}
	return err
}

func normalizedName(file *os.File) (string, error) {
	buffer := make([]uint16, 260)
	for {
		n, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0)
		runtime.KeepAlive(file)
		if err != nil {
			return "", err
		}
		if n < uint32(len(buffer)) {
			return windows.UTF16ToString(buffer[:n]), nil
		}
		buffer = make([]uint16, n+1)
	}
}

func checkCaseSensitive(file *os.File) error {
	var flags uint32
	err := windows.GetFileInformationByHandleEx(windows.Handle(file.Fd()), windows.FileCaseSensitiveInfo,
		(*byte)(unsafe.Pointer(&flags)), uint32(unsafe.Sizeof(flags)))
	runtime.KeepAlive(file)
	if err != nil {
		return err
	}
	if flags&windows.FILE_CS_FLAG_CASE_SENSITIVE_DIR == 0 {
		return errors.New("memory directories on Windows must have the NTFS case-sensitive flag enabled")
	}
	return nil
}

func checkFilesystem(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	var filesystem [32]uint16
	err = windows.GetVolumeInformationByHandle(windows.Handle(file.Fd()), nil, 0, nil, nil, nil,
		&filesystem[0], uint32(len(filesystem)))
	runtime.KeepAlive(file)
	if err != nil {
		return err
	}
	name, err := normalizedName(file)
	if err != nil {
		return err
	}
	if windows.UTF16ToString(filesystem[:]) != "NTFS" || strings.HasPrefix(name, `\\?\UNC\`) {
		return errors.New("memory storage on Windows requires a local NTFS directory")
	}
	return checkCaseSensitive(file)
}

func checkNativePath(root *os.Root, name string, info fs.FileInfo) error {
	for range 3 {
		err := checkNativeComponent(root, name, info)
		if !errors.Is(err, storeerr.ErrConflict) {
			return err
		}
	}
	return fmt.Errorf("memory path changed during filename validation: %w", storeerr.ErrConflict)
}

func checkNativeComponent(root *os.Root, name string, info fs.FileInfo) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if path.Base(name) != "." {
		if err := checkNativeFilename(root, name, file); err != nil {
			return err
		}
	}
	if info.IsDir() {
		return checkCaseSensitive(file)
	}
	return nil
}

func checkNativeFilename(root *os.Root, name string, file *os.File) error {
	actual, nameErr := normalizedName(file)
	if nameErr == nil && filepath.Base(actual) == path.Base(name) {
		return nil
	}
	// A lock-free reader can hold an unlinked file after POSIX replacement.
	// NTFS moves its name into $Deleted; that is not a caller-supplied alias.
	held, err := file.Stat()
	if err != nil {
		return err
	}
	current, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !os.SameFile(held, current) {
		return storeerr.ErrConflict
	}
	if nameErr != nil {
		return nameErr
	}
	return storeerr.InvalidRequest(fmt.Errorf("memory path must use its exact filename, not a Windows alias: %s", name))
}
