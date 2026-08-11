package machinedaemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

func TestTerminalResultMakesOutputReadFailureExplicit(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "output.buf")
	data := []byte("captured")
	if err := writeProcessOutputFile(path, data, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	exitCode := 0
	result, exit := captureTerminalResult(
		&localProcessRunner{output: processOutput{
			path:      path,
			endOffset: int64(len(data)),
			closed:    true,
		}},
		processRunnerExit{
			State:    "exited",
			ExitCode: &exitCode,
		},
		nil,
	)
	if exit.StateReasonCode != "output_capture_failed" ||
		exit.StateReasonMessage == "" ||
		exit.WaitErr == nil {
		t.Fatalf("terminal exit did not retain output failure: %+v", exit)
	}
	var observation struct {
		Output    string `json:"output"`
		Error     string `json:"error"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(result, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Output != "" ||
		observation.Error == "" ||
		!observation.Truncated {
		t.Fatalf("terminal result hid output failure: %s", result)
	}
}

func TestTerminalResultRetainsPrimaryReasonAndOutputFailure(t *testing.T) {
	t.Parallel()

	captureErr := errors.New("disk full")
	result, exit := captureTerminalResult(
		nil,
		processRunnerExit{
			State:              "failed",
			StateReasonCode:    "timeout",
			StateReasonMessage: "process timed out",
		},
		captureErr,
	)
	if exit.StateReasonCode != "timeout" ||
		exit.StateReasonMessage !=
			"process timed out; output capture failed: disk full" ||
		!errors.Is(exit.WaitErr, captureErr) {
		t.Fatalf("terminal exit lost one failure: %+v", exit)
	}
	var observation struct {
		Error     string `json:"error"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(result, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Error != exit.StateReasonMessage ||
		!observation.Truncated {
		t.Fatalf("terminal result lost one failure: %s", result)
	}
}

func TestProcessOutputSliceCannotOverflowAtProtocolBoundary(t *testing.T) {
	t.Parallel()

	output := newTestProcessOutput(t)
	if _, err := output.Write([]byte("bounded")); err != nil {
		t.Fatal(err)
	}
	got, start, next, truncated, err := output.Slice(0, math.MaxInt)
	if err != nil {
		t.Fatal(err)
	}
	if got != "bounded" ||
		start != 0 ||
		next != int64(len("bounded")) ||
		truncated {
		t.Fatalf(
			"slice=%q start=%d next=%d truncated=%t",
			got,
			start,
			next,
			truncated,
		)
	}
}

func TestProcessOutputReplacesInvalidUTF8WithoutChangingByteCursors(
	t *testing.T,
) {
	t.Parallel()

	output := newTestProcessOutput(t)
	if _, err := output.Write([]byte("a\xe2\x82")); err != nil {
		t.Fatalf("write partial UTF-8: %v", err)
	}
	got, start, next, truncated, err := output.Slice(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got != "?" || start != 1 || next != 3 || truncated {
		t.Fatalf(
			"slice=%q start=%d next=%d truncated=%v, want replacement at byte cursors 1..3",
			got,
			start,
			next,
			truncated,
		)
	}
}

func TestProcessOutputCompactionPublishesOneAtomicGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.buf")
	oldData := []byte("old-window")
	if err := writeProcessOutputFile(path, oldData, 0); err != nil {
		t.Fatalf("write initial output: %v", err)
	}

	injected := errors.New("injected replacement failure")
	newData := []byte("new-window")
	_, err := replaceProcessOutputFile(
		path,
		30,
		int64(len(newData)),
		func(candidate *os.File) error {
			if err := writeFull(candidate, newData); err != nil {
				t.Fatal(err)
			}
			info, err := candidate.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf(
					"replacement mode = %o, want 600",
					info.Mode().Perm(),
				)
			}
			body := make([]byte, info.Size())
			if _, err := candidate.ReadAt(body, 0); err != nil {
				t.Fatal(err)
			}
			data, baseOffset, decodeErr := decodeTestProcessOutputFile(body)
			if decodeErr != nil {
				t.Fatalf("decode replacement candidate: %v", decodeErr)
			}
			if string(data) != string(newData) ||
				baseOffset != 30 ||
				baseOffset+int64(len(data)) !=
					30+int64(len(newData)) {
				t.Fatalf(
					"replacement candidate data=%q base=%d",
					data,
					baseOffset,
				)
			}
			return injected
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("replacement error = %v, want injected failure", err)
	}
	data, baseOffset, err := readTestProcessOutputFile(path)
	if err != nil {
		t.Fatalf("read output after failed replacement: %v", err)
	}
	if string(data) != string(oldData) ||
		baseOffset != 0 ||
		baseOffset+int64(len(data)) != int64(len(oldData)) {
		t.Fatalf(
			"failed replacement mixed generations: data=%q base=%d",
			data,
			baseOffset,
		)
	}

	if err := writeProcessOutputFile(path, newData, 30); err != nil {
		t.Fatalf("publish compacted output: %v", err)
	}
	data, baseOffset, err = readTestProcessOutputFile(path)
	if err != nil {
		t.Fatalf("read compacted output: %v", err)
	}
	if string(data) != string(newData) ||
		baseOffset != 30 ||
		baseOffset+int64(len(data)) !=
			30+int64(len(newData)) {
		t.Fatalf(
			"compacted output data=%q base=%d",
			data,
			baseOffset,
		)
	}
}

func TestProcessOutputOpenRequiresMatchingDurableCursorRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.buf")
	if err := writeProcessOutputFile(path, []byte("abc"), 30); err != nil {
		t.Fatalf("write initial output: %v", err)
	}
	file, err := openProcessOutputAppendFile(path, 30, 33)
	if err != nil {
		t.Fatalf("open matching process output: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close matching process output: %v", err)
	}
	if file, err := openProcessOutputAppendFile(
		path,
		31,
		33,
	); err == nil {
		_ = file.Close()
		t.Fatal("open with a conflicting live cursor range succeeded")
	}
}

func TestProcessOutputSliceRejectsChangedDurableCursorRange(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "output.buf")
	if err := writeProcessOutputFile(path, []byte("abc"), 0); err != nil {
		t.Fatalf("write process output: %v", err)
	}
	output := processOutput{
		path:      path,
		endOffset: 3,
		closed:    true,
	}
	if err := os.Truncate(path, processOutputHeaderSize+1); err != nil {
		t.Fatalf("truncate process output: %v", err)
	}
	if got, start, next, truncated, err := output.Slice(
		0,
		64,
	); err == nil || got != "" || start != 0 || next != 0 || !truncated {
		t.Fatalf(
			"changed slice=%q start=%d next=%d truncated=%t error=%v",
			got,
			start,
			next,
			truncated,
			err,
		)
	}
}

func TestProcessOutputRecoveryRequiresPrivateFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows privacy is enforced by ACL rather than chmod")
	}
	path := filepath.Join(t.TempDir(), "output.buf")
	if err := writeProcessOutputFile(path, []byte("private output"), 0); err != nil {
		t.Fatalf("write process output: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("broaden process output permissions: %v", err)
	}
	if _, _, err := readTestProcessOutputFile(path); err == nil {
		t.Fatal("recovery accepted broadly readable process output")
	}
}

func TestProcessOutputRejectsCorruptSelfDescribingHeader(t *testing.T) {
	header, err := encodeProcessOutputHeader(7)
	if err != nil {
		t.Fatalf("encode output: %v", err)
	}
	body := append(header, []byte("output")...)
	body[8] ^= 0xff
	if _, _, err := decodeTestProcessOutputFile(body); err == nil {
		t.Fatal("corrupt output cursor header was accepted")
	}
}

func TestReadProcessOutputSnapshotUsesDurableCursorRange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, localstore.OutputBufferFileName)
	if err := writeProcessOutputFile(path, []byte("retained"), 17); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	output, start, next, truncated, err := readProcessOutputSnapshot(
		root,
		0,
		64,
	)
	if err != nil {
		t.Fatal(err)
	}
	if output != "retained" ||
		start != 17 ||
		next != 25 ||
		!truncated {
		t.Fatalf(
			"recovered slice=%q start=%d next=%d truncated=%v",
			output,
			start,
			next,
			truncated,
		)
	}
}

func TestProcessOutputSnapshotRacingAppendSeesAStablePrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.buf")
	if err := writeProcessOutputFile(path, nil, 0); err != nil {
		t.Fatal(err)
	}
	output := processOutput{
		path:         path,
		limit:        64 * 1024,
		syncBytes:    64 * 1024,
		syncInterval: time.Hour,
	}
	t.Cleanup(func() { _ = output.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	var expected []byte
	for i := range 200 {
		expected = append(expected, bytes.Repeat([]byte{byte('a' + i%26)}, 31)...)
	}
	writeDone := make(chan error, 1)
	go func() {
		for offset := 0; offset < len(expected); offset += 31 {
			if _, err := output.Write(expected[offset : offset+31]); err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- nil
	}()

	for {
		got, start, next, truncated, err := readProcessOutputSnapshot(
			root,
			0,
			len(expected),
		)
		if err != nil {
			t.Fatal(err)
		}
		if start != 0 ||
			next != int64(len(got)) ||
			truncated ||
			len(got) > len(expected) ||
			!bytes.Equal([]byte(got), expected[:len(got)]) {
			t.Fatalf(
				"snapshot start=%d next=%d size=%d truncated=%t",
				start,
				next,
				len(got),
				truncated,
			)
		}
		select {
		case err := <-writeDone:
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
	}
}

func TestProcessOutputSnapshotRacingReplacementSeesOneGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.buf")
	oldData := []byte("complete-old-generation")
	newData := []byte("complete-new-generation")
	if err := writeProcessOutputFile(path, oldData, 0); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	writeDone := make(chan error, 1)
	go func() {
		for range 8 {
			if err := writeProcessOutputFile(path, newData, 100); err != nil {
				writeDone <- err
				return
			}
			if err := writeProcessOutputFile(path, oldData, 0); err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- nil
	}()

	for {
		got, start, next, truncated, err := readProcessOutputSnapshot(
			root,
			0,
			1024,
		)
		if err != nil {
			t.Fatal(err)
		}
		oldGeneration := got == string(oldData) &&
			start == 0 &&
			next == int64(len(oldData)) &&
			!truncated
		newGeneration := got == string(newData) &&
			start == 100 &&
			next == 100+int64(len(newData)) &&
			truncated
		if !oldGeneration && !newGeneration {
			t.Fatalf(
				"mixed snapshot output=%q start=%d next=%d truncated=%t",
				got,
				start,
				next,
				truncated,
			)
		}
		select {
		case err := <-writeDone:
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
	}
}

func TestProcessOutputBatchesSyncAndClosesDurably(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.buf")
	if err := writeProcessOutputFile(path, nil, 0); err != nil {
		t.Fatalf("initialize process output: %v", err)
	}
	syncCalls := 0
	output := processOutput{
		path:         path,
		limit:        1024,
		syncBytes:    6,
		syncInterval: time.Hour,
		syncFile: func(file *os.File) error {
			syncCalls++
			return file.Sync()
		},
	}
	if _, err := output.Write([]byte("abc")); err != nil {
		t.Fatalf("write first output chunk: %v", err)
	}
	if syncCalls != 0 {
		t.Fatalf("sync calls after sub-threshold write = %d, want 0", syncCalls)
	}
	if _, err := output.Write([]byte("def")); err != nil {
		t.Fatalf("write threshold output chunk: %v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("sync calls at byte threshold = %d, want 1", syncCalls)
	}
	if _, err := output.Write([]byte("g")); err != nil {
		t.Fatalf("write terminal output chunk: %v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("sync calls before terminal close = %d, want 1", syncCalls)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close process output: %v", err)
	}
	if syncCalls != 2 {
		t.Fatalf("sync calls after terminal close = %d, want 2", syncCalls)
	}
	if _, err := output.Write([]byte("after-close")); err == nil {
		t.Fatal("closed process output accepted another write")
	}
	got, start, next, truncated, err := output.Slice(0, 64)
	if err != nil {
		t.Fatalf("read closed process output: %v", err)
	}
	if got != "abcdefg" || start != 0 || next != 7 || truncated {
		t.Fatalf(
			"closed output slice=%q start=%d next=%d truncated=%v",
			got,
			start,
			next,
			truncated,
		)
	}
	data, baseOffset, err := readTestProcessOutputFile(path)
	if err != nil {
		t.Fatalf("read closed process output: %v", err)
	}
	if string(data) != "abcdefg" ||
		baseOffset != 0 ||
		baseOffset+int64(len(data)) != 7 {
		t.Fatalf("closed output data=%q base=%d", data, baseOffset)
	}
}

func TestProcessOutputPeriodicSyncBoundsLowVolumeDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.buf")
	if err := writeProcessOutputFile(path, nil, 0); err != nil {
		t.Fatalf("initialize process output: %v", err)
	}
	synced := make(chan struct{}, 1)
	output := processOutput{
		path:         path,
		limit:        1024,
		syncBytes:    1024,
		syncInterval: 5 * time.Millisecond,
		syncFile: func(file *os.File) error {
			if err := file.Sync(); err != nil {
				return err
			}
			synced <- struct{}{}
			return nil
		},
	}
	if _, err := output.Write([]byte("low-volume")); err != nil {
		t.Fatalf("write low-volume output: %v", err)
	}
	select {
	case <-synced:
	case <-time.After(time.Second):
		t.Fatal("low-volume output was not synchronized by the time bound")
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close periodically synchronized output: %v", err)
	}
}

func TestProcessOutputCompactionClosesReplacesAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.buf")
	if err := writeProcessOutputFile(path, nil, 0); err != nil {
		t.Fatalf("initialize process output: %v", err)
	}
	output := processOutput{
		path:         path,
		limit:        8,
		syncBytes:    1024,
		syncInterval: time.Hour,
	}
	if _, err := output.Write([]byte("abcde")); err != nil {
		t.Fatalf("write output before compaction: %v", err)
	}
	if _, err := output.Write([]byte("fghijk")); err != nil {
		t.Fatalf("compact process output: %v", err)
	}
	if _, err := output.Write([]byte("lm")); err != nil {
		t.Fatalf("append after compaction: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close compacted process output: %v", err)
	}
	got, start, next, truncated, err := output.Slice(0, 64)
	if err != nil {
		t.Fatalf("slice compacted process output: %v", err)
	}
	if got != "defghijklm" || start != 3 || next != 13 || !truncated {
		t.Fatalf(
			"compacted slice=%q start=%d next=%d truncated=%v",
			got,
			start,
			next,
			truncated,
		)
	}
	data, baseOffset, err := readTestProcessOutputFile(path)
	if err != nil {
		t.Fatalf("read compacted process output: %v", err)
	}
	if string(data) != "defghijklm" ||
		baseOffset != 3 ||
		baseOffset+int64(len(data)) != 13 {
		t.Fatalf("compacted output data=%q base=%d", data, baseOffset)
	}
}

func TestProcessOutputCompactionRetainsPublishedGenerationAfterSyncFailure(
	t *testing.T,
) {
	path := filepath.Join(t.TempDir(), "output.buf")
	if err := writeProcessOutputFile(path, nil, 0); err != nil {
		t.Fatalf("initialize process output: %v", err)
	}
	injected := errors.New("injected post-publication sync failure")
	output := processOutput{
		path:         path,
		limit:        8,
		syncBytes:    1024,
		syncInterval: time.Hour,
		replaceFile: func(
			path string,
			baseOffset, dataLength int64,
			writeData func(*os.File) error,
		) (bool, error) {
			published, err := replaceProcessOutputFile(
				path,
				baseOffset,
				dataLength,
				writeData,
			)
			if err != nil {
				return published, err
			}
			return published, injected
		},
	}
	t.Cleanup(func() { _ = output.Close() })
	if _, err := output.Write([]byte("abcde")); err != nil {
		t.Fatalf("write before compaction: %v", err)
	}
	if written, err := output.Write(
		[]byte("fghijk"),
	); written != 6 || !errors.Is(err, injected) {
		t.Fatalf(
			"post-publication failure written=%d error=%v",
			written,
			err,
		)
	}
	got, start, next, truncated, err := output.Slice(0, 64)
	if !errors.Is(err, injected) {
		t.Fatalf("published generation error = %v", err)
	}
	if got != "defghijk" || start != 3 || next != 11 || !truncated {
		t.Fatalf(
			"published slice=%q start=%d next=%d truncated=%t",
			got,
			start,
			next,
			truncated,
		)
	}
}

func TestProcessOutputSyncFailureIsSticky(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.buf")
	if err := writeProcessOutputFile(path, nil, 0); err != nil {
		t.Fatalf("initialize process output: %v", err)
	}
	injected := errors.New("injected sync failure")
	output := processOutput{
		path:      path,
		limit:     1024,
		syncBytes: 1,
		syncFile: func(*os.File) error {
			return injected
		},
	}
	if n, err := output.Write([]byte("abc")); n != 3 ||
		!errors.Is(err, injected) {
		t.Fatalf("write after sync failure n=%d error=%v", n, err)
	}
	got, start, next, truncated, err := output.Slice(0, 64)
	if !errors.Is(err, injected) {
		t.Fatalf("slice after sync failure error=%v", err)
	}
	if got != "abc" || start != 0 || next != 3 || !truncated {
		t.Fatalf(
			"slice after sync failure=%q start=%d next=%d truncated=%v",
			got,
			start,
			next,
			truncated,
		)
	}
	if n, err := output.Write([]byte("def")); n != 0 ||
		!errors.Is(err, injected) {
		t.Fatalf("write after sticky failure n=%d error=%v", n, err)
	}
	if err := output.Close(); !errors.Is(err, injected) {
		t.Fatalf("close after sticky sync failure error=%v", err)
	}
	got, start, next, truncated, err = output.Slice(0, 64)
	if !errors.Is(err, injected) {
		t.Fatalf("reopen after sticky sync failure error=%v", err)
	}
	if got != "abc" || start != 0 || next != 3 || !truncated {
		t.Fatalf(
			"reopened sticky slice=%q start=%d next=%d truncated=%v",
			got,
			start,
			next,
			truncated,
		)
	}
}

func TestProcessOutputAsyncSyncFailureIsSticky(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.buf")
	if err := writeProcessOutputFile(path, nil, 0); err != nil {
		t.Fatalf("initialize process output: %v", err)
	}
	injected := errors.New("injected async sync failure")
	syncAttempted := make(chan struct{}, 1)
	output := processOutput{
		path:         path,
		limit:        1024,
		syncBytes:    1024,
		syncInterval: time.Millisecond,
		syncFile: func(*os.File) error {
			syncAttempted <- struct{}{}
			return injected
		},
	}
	if _, err := output.Write([]byte("abc")); err != nil {
		t.Fatalf("write before asynchronous sync: %v", err)
	}
	select {
	case <-syncAttempted:
	case <-time.After(time.Second):
		t.Fatal("asynchronous sync was not attempted")
	}
	output.mu.Lock()
	sticky := output.stickyErr
	output.mu.Unlock()
	if !errors.Is(sticky, injected) {
		t.Fatalf("asynchronous sync error was not retained: %v", sticky)
	}
	if n, err := output.Write([]byte("def")); n != 0 ||
		!errors.Is(err, injected) {
		t.Fatalf("write after async sync failure n=%d error=%v", n, err)
	}
	if err := output.Close(); !errors.Is(err, injected) {
		t.Fatalf("close after async sync failure error=%v", err)
	}
}

func TestProcessOutputCloseJoinsInFlightTimerSync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.buf")
	if err := writeProcessOutputFile(path, nil, 0); err != nil {
		t.Fatalf("initialize process output: %v", err)
	}
	syncStarted := make(chan struct{})
	releaseSync := make(chan struct{})
	output := processOutput{
		path:         path,
		limit:        1024,
		syncBytes:    1024,
		syncInterval: time.Millisecond,
		syncFile: func(file *os.File) error {
			close(syncStarted)
			<-releaseSync
			return file.Sync()
		},
	}
	if _, err := output.Write([]byte("timer-race")); err != nil {
		t.Fatalf("write before timer race: %v", err)
	}
	select {
	case <-syncStarted:
	case <-time.After(time.Second):
		t.Fatal("timer sync did not start")
	}
	closeDone := make(chan error, 1)
	closeStarted := make(chan struct{})
	go func() {
		close(closeStarted)
		closeDone <- output.Close()
	}()
	<-closeStarted
	select {
	case err := <-closeDone:
		t.Fatalf("close returned before in-flight sync completed: %v", err)
	default:
	}
	close(releaseSync)
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("close after in-flight sync: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not join in-flight timer sync")
	}
	data, _, err := readTestProcessOutputFile(path)
	if err != nil {
		t.Fatalf("read output after timer-close race: %v", err)
	}
	if string(data) != "timer-race" {
		t.Fatalf("output after timer-close race = %q", data)
	}
}

func newTestProcessOutput(t *testing.T) *processOutput {
	t.Helper()
	path := filepath.Join(t.TempDir(), "output.buf")
	if err := writeProcessOutputFile(path, nil, 0); err != nil {
		t.Fatal(err)
	}
	output := &processOutput{path: path}
	t.Cleanup(func() { _ = output.Close() })
	return output
}

func readTestProcessOutputFile(
	path string,
) ([]byte, int64, error) {
	file, baseOffset, endOffset, err := openProcessOutputFile(
		path,
		os.O_RDONLY,
	)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	length := endOffset - baseOffset
	if length < 0 || length > int64(math.MaxInt) {
		return nil, 0, errors.New(
			"test process output length is invalid",
		)
	}
	data := make([]byte, int(length))
	if len(data) > 0 {
		n, err := file.ReadAt(data, processOutputHeaderSize)
		if err != nil && !(errors.Is(err, io.EOF) && n == len(data)) {
			return nil, 0, err
		}
	}
	return data, baseOffset, nil
}

func decodeTestProcessOutputFile(body []byte) ([]byte, int64, error) {
	if len(body) < processOutputHeaderSize {
		return nil, 0, errors.New("process output header is truncated")
	}
	data := body[processOutputHeaderSize:]
	baseOffset, err := decodeProcessOutputHeader(
		body[:processOutputHeaderSize],
		int64(len(data)),
	)
	if err != nil {
		return nil, 0, err
	}
	return data, baseOffset, nil
}
