package omnarad

import (
	"strings"
	"sync"

	"github.com/omnara-ai/omnara/internal/machinedaemon"
)

var supervisorFatalOutputMarkers = [...]string{
	"panic:",
	"fatal error:",
	"runtime: out of memory:",
	"runtime: goroutine stack exceeds ",
	"runtime: program exceeds ",
	"runtime: failed to create new OS thread",
	"runtime: mmap",
}

type supervisorOutputTail struct {
	mu        sync.Mutex
	data      [machinedaemon.MaxFailureDetailBytes]byte
	size      int
	truncated bool
	fatal     bool
	line      []byte
	skipLine  bool
}

type supervisorOutputWriter struct {
	tail   *supervisorOutputTail
	stderr bool
}

func (writer supervisorOutputWriter) Write(output []byte) (int, error) {
	return writer.tail.write(output, writer.stderr)
}

func (tail *supervisorOutputTail) write(output []byte, stderr bool) (int, error) {
	tail.mu.Lock()
	defer tail.mu.Unlock()
	originalLength := len(output)
	if tail.fatal && !stderr {
		tail.truncated = tail.truncated || originalLength > 0
		return originalLength, nil
	}
	if !tail.fatal && stderr {
		if markerEnd := tail.scanFatalOutput(output); markerEnd >= 0 {
			tail.truncated = tail.truncated || tail.size+markerEnd > len(tail.line)
			tail.size = copy(tail.data[:], tail.line)
			tail.fatal = true
			output = output[markerEnd:]
		}
	}
	if tail.fatal {
		tail.truncated = tail.truncated || len(output) > len(tail.data)-tail.size
		tail.size += copy(tail.data[tail.size:], output)
		return originalLength, nil
	}
	tail.truncated = tail.truncated || tail.size+originalLength > len(tail.data)
	output = output[max(0, originalLength-len(tail.data)):]
	if excess := tail.size + len(output) - len(tail.data); excess > 0 {
		tail.size = copy(tail.data[:], tail.data[excess:tail.size])
	}
	tail.size += copy(tail.data[tail.size:], output)
	return originalLength, nil
}

func (tail *supervisorOutputTail) scanFatalOutput(output []byte) int {
	for index, value := range output {
		if value == '\n' {
			tail.line = tail.line[:0]
			tail.skipLine = false
			continue
		}
		if tail.skipLine {
			continue
		}
		tail.line = append(tail.line, value)
		matched, possible := matchSupervisorFatalOutput(tail.line)
		if matched {
			return index + 1
		}
		if !possible {
			tail.line = tail.line[:0]
			tail.skipLine = true
		}
	}
	return -1
}

func matchSupervisorFatalOutput(line []byte) (bool, bool) {
	text := string(line)
	possible := false
	for _, marker := range supervisorFatalOutputMarkers {
		if text == marker {
			return true, true
		}
		possible = possible || strings.HasPrefix(marker, text)
	}
	return false, possible
}

func (tail *supervisorOutputTail) snapshot(limit int) (string, bool) {
	tail.mu.Lock()
	defer tail.mu.Unlock()
	size := min(tail.size, max(0, limit))
	if tail.fatal {
		return string(tail.data[:size]), tail.truncated || size < tail.size
	}
	return string(tail.data[tail.size-size : tail.size]), tail.truncated || size < tail.size
}
