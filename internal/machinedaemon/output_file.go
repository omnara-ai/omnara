package machinedaemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/processaction"
)

const (
	processOutputHeaderSize   = 32
	processOutputSyncBytes    = 1024 * 1024
	processOutputSyncInterval = 250 * time.Millisecond
)

var processOutputMagic = [8]byte{'O', 'M', 'N', 'O', 'U', 'T', '0', '1'}

type processOutputReplacer func(
	path string,
	baseOffset, dataLength int64,
	writeData func(*os.File) error,
) (bool, error)

// Captured bytes exist only in the durable file. After a crash, its synced
// prefix remains valid and determines the recovered cursor.
type processOutput struct {
	mu         sync.Mutex
	baseOffset int64
	endOffset  int64
	limit      int
	path       string

	file           *os.File
	unsyncedBytes  int
	syncBytes      int
	syncInterval   time.Duration
	syncTimer      *time.Timer
	syncGeneration uint64
	syncFile       func(*os.File) error
	replaceFile    processOutputReplacer
	stickyErr      error
	closed         bool
}

func (o *processOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return 0, errors.New("process output is closed")
	}
	if o.stickyErr != nil {
		return 0, o.stickyErr
	}
	if len(p) == 0 {
		return 0, nil
	}
	if o.limit <= 0 {
		o.limit = defaultProcessOutputBufferBytes
	}
	if o.baseOffset < 0 || o.endOffset < o.baseOffset ||
		int64(len(p)) > math.MaxInt64-o.endOffset {
		return 0, errors.New("process output cursor range is invalid")
	}

	stored := o.endOffset - o.baseOffset
	extra := o.limit / 4
	if extra < 1 {
		extra = 1
	}
	compactAt := int64(o.limit)
	if int64(extra) <= math.MaxInt64-compactAt {
		compactAt += int64(extra)
	} else {
		compactAt = math.MaxInt64
	}
	if int64(len(p)) > math.MaxInt64-stored ||
		stored+int64(len(p)) > compactAt {
		return o.compactLocked(p)
	}

	if err := o.openLocked(); err != nil {
		return 0, o.rememberErrorLocked(err)
	}
	n, writeErr := o.file.Write(p)
	if n > 0 {
		o.endOffset += int64(n)
		o.unsyncedBytes += n
	}
	if writeErr == nil && n != len(p) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return n, o.rememberErrorLocked(writeErr)
	}
	if err := o.scheduleOrSyncLocked(); err != nil {
		return n, err
	}
	return n, nil
}

func (o *processOutput) compactLocked(p []byte) (int, error) {
	stored := o.endOffset - o.baseOffset
	if stored < 0 || int64(len(p)) > math.MaxInt64-stored ||
		int64(len(p)) > math.MaxInt64-o.endOffset {
		return 0, errors.New("process output cursor range is invalid")
	}
	total := stored + int64(len(p))
	keep := int64(o.limit)
	if total < keep {
		keep = total
	}
	newEnd := o.endOffset + int64(len(p))
	newBase := newEnd - keep
	fromNew := int64(len(p))
	if fromNew > keep {
		fromNew = keep
	}
	fromExisting := keep - fromNew

	o.stopSyncTimerLocked()
	if err := o.openLocked(); err != nil {
		return 0, o.rememberErrorLocked(err)
	}
	if err := o.syncLocked(); err != nil {
		_ = o.file.Close()
		o.file = nil
		return 0, o.rememberErrorLocked(err)
	}
	source := o.file
	o.file = nil
	sourceOpen := true
	defer func() {
		if sourceOpen {
			_ = source.Close()
		}
	}()

	replaceFile := o.replaceFile
	if replaceFile == nil {
		replaceFile = replaceProcessOutputFile
	}
	published, err := replaceFile(
		o.path,
		newBase,
		keep,
		func(destination *os.File) error {
			var copyErr error
			if fromExisting > 0 {
				offset := int64(processOutputHeaderSize) +
					stored - fromExisting
				_, copyErr = io.CopyN(
					destination,
					io.NewSectionReader(source, offset, fromExisting),
					fromExisting,
				)
			}
			var writeErr error
			if copyErr == nil && fromNew > 0 {
				writeErr = writeFull(
					destination,
					p[len(p)-int(fromNew):],
				)
			}
			closeErr := source.Close()
			sourceOpen = false
			return errors.Join(copyErr, writeErr, closeErr)
		},
	)
	if published {
		o.baseOffset = newBase
		o.endOffset = newEnd
		o.unsyncedBytes = 0
	}
	if err != nil {
		written := 0
		if published {
			written = len(p)
		}
		return written, o.rememberErrorLocked(
			fmt.Errorf("compact process output: %w", err),
		)
	}

	if err := o.openLocked(); err != nil {
		return len(p), o.rememberErrorLocked(
			fmt.Errorf("reopen compacted process output: %w", err),
		)
	}
	return len(p), nil
}

func (o *processOutput) openLocked() error {
	if o.file != nil {
		return nil
	}
	file, err := openProcessOutputAppendFile(
		o.path,
		o.baseOffset,
		o.endOffset,
	)
	if err != nil {
		return err
	}
	o.file = file
	return nil
}

func (o *processOutput) scheduleOrSyncLocked() error {
	if o.unsyncedBytes == 0 || o.file == nil {
		return nil
	}
	syncBytes := o.syncBytes
	if syncBytes <= 0 {
		syncBytes = processOutputSyncBytes
	}
	if o.unsyncedBytes >= syncBytes {
		o.stopSyncTimerLocked()
		if err := o.syncLocked(); err != nil {
			return o.rememberErrorLocked(err)
		}
		return nil
	}
	if o.syncTimer != nil {
		return nil
	}
	interval := o.syncInterval
	if interval <= 0 {
		interval = processOutputSyncInterval
	}
	o.syncGeneration++
	generation := o.syncGeneration
	o.syncTimer = time.AfterFunc(interval, func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.syncGeneration != generation || o.syncTimer == nil {
			return
		}
		o.syncTimer = nil
		if o.closed || o.stickyErr != nil || o.unsyncedBytes == 0 {
			return
		}
		if err := o.syncLocked(); err != nil {
			_ = o.rememberErrorLocked(err)
		}
	})
	return nil
}

func (o *processOutput) syncLocked() error {
	if o.file == nil || o.unsyncedBytes == 0 {
		return nil
	}
	syncFile := o.syncFile
	if syncFile == nil {
		syncFile = localstore.SyncFile
	}
	if err := syncFile(o.file); err != nil {
		return fmt.Errorf("sync process output: %w", err)
	}
	o.unsyncedBytes = 0
	return nil
}

func (o *processOutput) stopSyncTimerLocked() {
	o.syncGeneration++
	if o.syncTimer != nil {
		o.syncTimer.Stop()
		o.syncTimer = nil
	}
}

func (o *processOutput) rememberErrorLocked(err error) error {
	if err == nil {
		return nil
	}
	o.stickyErr = errors.Join(o.stickyErr, err)
	return o.stickyErr
}

func (o *processOutput) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return o.stickyErr
	}
	o.stopSyncTimerLocked()
	var closeErr error
	if o.file != nil {
		if err := o.syncLocked(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
		if err := o.file.Close(); err != nil {
			closeErr = errors.Join(
				closeErr,
				fmt.Errorf("close process output: %w", err),
			)
		}
		o.file = nil
	}
	o.closed = true
	if closeErr != nil {
		_ = o.rememberErrorLocked(closeErr)
	}
	return o.stickyErr
}

func (o *processOutput) Slice(
	cursor int64,
	maxBytes int,
) (string, int64, int64, bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	captureErr := o.stickyErr
	if o.path == "" {
		err := o.rememberErrorLocked(
			errors.New("process output path is required"),
		)
		return "", 0, 0, true, err
	}

	file := o.file
	closeFile := false
	if file == nil {
		var base, end int64
		var err error
		file, base, end, err = openProcessOutputFile(o.path, os.O_RDONLY)
		if err != nil {
			return "", 0, 0, true, o.rememberErrorLocked(err)
		}
		closeFile = true
		if base != o.baseOffset || end != o.endOffset {
			_ = file.Close()
			return "", 0, 0, true, o.rememberErrorLocked(
				errors.New("process output file does not match the live cursor range"),
			)
		}
	}
	if closeFile {
		defer func() { _ = file.Close() }()
	}
	output, start, next, truncated, err := readProcessOutputRange(
		file,
		o.baseOffset,
		o.endOffset,
		cursor,
		maxBytes,
	)
	if err != nil {
		return "", 0, 0, true, o.rememberErrorLocked(err)
	}
	return output, start, next, truncated || captureErr != nil, captureErr
}

func writeProcessOutputFile(path string, data []byte, baseOffset int64) error {
	_, err := replaceProcessOutputFile(
		path,
		baseOffset,
		int64(len(data)),
		func(file *os.File) error {
			return writeFull(file, data)
		},
	)
	return err
}

func replaceProcessOutputFile(
	path string,
	baseOffset, dataLength int64,
	writeData func(*os.File) error,
) (bool, error) {
	if baseOffset < 0 || dataLength < 0 ||
		dataLength > math.MaxInt64-baseOffset {
		return false, errors.New("process output cursor range is invalid")
	}
	if writeData == nil {
		return false, errors.New("process output writer is required")
	}
	header, err := encodeProcessOutputHeader(baseOffset)
	if err != nil {
		return false, err
	}
	return localstore.WriteFileAtomicFunc(
		path,
		0o600,
		func(file *os.File) error {
			if err := writeFull(file, header); err != nil {
				return fmt.Errorf("write process output header: %w", err)
			}
			if err := writeData(file); err != nil {
				return err
			}
			info, err := file.Stat()
			if err != nil {
				return fmt.Errorf("inspect process output replacement: %w", err)
			}
			if info.Size() != int64(processOutputHeaderSize)+dataLength {
				return fmt.Errorf(
					"process output replacement has %d data bytes, want %d",
					info.Size()-int64(processOutputHeaderSize),
					dataLength,
				)
			}
			return nil
		},
	)
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if written > 0 {
			data = data[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}

func encodeProcessOutputHeader(baseOffset int64) ([]byte, error) {
	if baseOffset < 0 {
		return nil, errors.New("process output cursor range is invalid")
	}
	header := make([]byte, processOutputHeaderSize)
	copy(header[:8], processOutputMagic[:])
	binary.BigEndian.PutUint64(header[8:16], uint64(baseOffset))
	checksum := sha256.Sum256(header[:16])
	copy(header[16:], checksum[:16])
	return header, nil
}

func openProcessOutputAppendFile(
	path string,
	baseOffset, endOffset int64,
) (*os.File, error) {
	if baseOffset < 0 || endOffset < baseOffset {
		return nil, errors.New("process output append range is invalid")
	}
	file, actualBase, actualEnd, err := openProcessOutputFile(
		path,
		os.O_RDWR|os.O_APPEND,
	)
	if err != nil {
		return nil, err
	}
	if actualBase != baseOffset || actualEnd != endOffset {
		_ = file.Close()
		return nil, errors.New(
			"process output file does not match the live cursor range",
		)
	}
	return file, nil
}

func openProcessOutputFile(
	path string,
	flags int,
) (*os.File, int64, int64, error) {
	if path == "" {
		return nil, 0, 0, errors.New(
			"process output path is required",
		)
	}
	expected, err := os.Lstat(path)
	if err != nil {
		return nil, 0, 0, err
	}
	if err := localstore.ValidatePrivateFile(path); err != nil {
		return nil, 0, 0, err
	}
	file, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return nil, 0, 0, err
	}
	fail := func(err error) (*os.File, int64, int64, error) {
		_ = file.Close()
		return nil, 0, 0, err
	}
	actual, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if !os.SameFile(expected, actual) {
		return fail(errors.New("process output file changed while opening"))
	}
	if actual.Size() < processOutputHeaderSize {
		return fail(errors.New("process output header is truncated"))
	}
	header := make([]byte, processOutputHeaderSize)
	if _, err := file.ReadAt(header, 0); err != nil {
		return fail(fmt.Errorf("read process output header: %w", err))
	}
	dataLength := actual.Size() - int64(processOutputHeaderSize)
	baseOffset, err := decodeProcessOutputHeader(header, dataLength)
	if err != nil {
		return fail(err)
	}
	latest, err := os.Lstat(path)
	if err != nil {
		return fail(err)
	}
	if !os.SameFile(latest, actual) {
		return fail(errors.New("process output file changed after opening"))
	}
	if err := localstore.ValidatePrivateFile(path); err != nil {
		return fail(err)
	}
	return file, baseOffset, baseOffset + dataLength, nil
}

// Root.Open prevents symlink traversal and uses delete-sharing on Windows, so
// compaction can replace the file while a snapshot is open.
func readProcessOutputSnapshot(
	root *os.Root,
	cursor int64,
	maxBytes int,
) (string, int64, int64, bool, error) {
	if root == nil {
		return "", 0, 0, false, errors.New("process output root is required")
	}
	file, err := root.Open(localstore.OutputBufferFileName)
	if err != nil {
		return "", 0, 0, false, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", 0, 0, false, err
	}
	if err := localstore.ValidatePrivateFileInfo(info); err != nil {
		return "", 0, 0, false, err
	}
	if info.Size() < processOutputHeaderSize {
		return "", 0, 0, false, errors.New(
			"process output header is truncated",
		)
	}
	header := make([]byte, processOutputHeaderSize)
	if _, err := file.ReadAt(header, 0); err != nil {
		return "", 0, 0, false, fmt.Errorf(
			"read process output header: %w",
			err,
		)
	}
	dataLength := info.Size() - int64(processOutputHeaderSize)
	baseOffset, err := decodeProcessOutputHeader(header, dataLength)
	if err != nil {
		return "", 0, 0, false, err
	}
	return readProcessOutputRange(
		file,
		baseOffset,
		baseOffset+dataLength,
		cursor,
		maxBytes,
	)
}

func readProcessOutputRange(
	file *os.File,
	baseOffset, endOffset, cursor int64,
	maxBytes int,
) (string, int64, int64, bool, error) {
	if file == nil || baseOffset < 0 || endOffset < baseOffset {
		return "", 0, 0, false, errors.New(
			"process output read range is invalid",
		)
	}
	if maxBytes <= 0 {
		maxBytes = processaction.DefaultObservationBytes
	}
	if cursor < 0 {
		cursor = 0
	}
	truncated := false
	if cursor < baseOffset {
		cursor = baseOffset
		truncated = true
	}
	if cursor > endOffset {
		return "", cursor, cursor, truncated, nil
	}
	start := cursor
	available := endOffset - start
	readLength := available
	if int64(maxBytes) < readLength {
		readLength = int64(maxBytes)
	}
	if readLength < 0 || readLength > int64(math.MaxInt) {
		return "", 0, 0, false, errors.New(
			"process output read length is invalid",
		)
	}
	data := make([]byte, int(readLength))
	if len(data) > 0 {
		offset := int64(processOutputHeaderSize) + start - baseOffset
		n, err := file.ReadAt(data, offset)
		if err != nil && !(errors.Is(err, io.EOF) && n == len(data)) {
			return "", 0, 0, false, fmt.Errorf(
				"read process output: %w",
				err,
			)
		}
		if n != len(data) {
			return "", 0, 0, false, io.ErrUnexpectedEOF
		}
	}
	next := start + readLength
	truncated = truncated || next < endOffset
	return strings.ToValidUTF8(string(data), "?"),
		start,
		next,
		truncated,
		nil
}

func decodeProcessOutputHeader(
	header []byte,
	dataLength int64,
) (int64, error) {
	if len(header) != processOutputHeaderSize {
		return 0, errors.New(
			"process output header has an invalid size",
		)
	}
	if !bytes.Equal(header[:8], processOutputMagic[:]) {
		return 0, errors.New(
			"process output header has an invalid format",
		)
	}
	checksum := sha256.Sum256(header[:16])
	if !bytes.Equal(header[16:], checksum[:16]) {
		return 0, errors.New(
			"process output header checksum is invalid",
		)
	}
	base := binary.BigEndian.Uint64(header[8:16])
	if base > math.MaxInt64 ||
		dataLength < 0 ||
		dataLength > math.MaxInt64-int64(base) {
		return 0, errors.New(
			"process output cursor range is invalid",
		)
	}
	return int64(base), nil
}
