package omnarad

import (
	"sync"

	"github.com/omnara-ai/omnara/internal/machinedaemon"
)

type supervisorOutputTail struct {
	mu        sync.Mutex
	data      [machinedaemon.MaxFailureDetailBytes]byte
	size      int
	truncated bool
	fatal     bool
	line      [len("runtime: failed to create new OS thread")]byte
	lineSize  int
}

type supervisorOutputWriter struct {
	tail   *supervisorOutputTail
	stderr bool
}

func (w supervisorOutputWriter) Write(p []byte) (int, error) {
	b := w.tail
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.fatal && !w.stderr {
		b.truncated = b.truncated || n > 0
		return n, nil
	}
	if !b.fatal && w.stderr {
	scan:
		for i, c := range p {
			if c == '\n' {
				b.lineSize = 0
				continue
			}
			if b.lineSize < len(b.line) {
				b.line[b.lineSize] = c
				b.lineSize++
			}
			switch string(b.line[:b.lineSize]) {
			case "panic:", "fatal error:", "runtime: out of memory:",
				"runtime: goroutine stack exceeds ", "runtime: program exceeds ",
				"runtime: failed to create new OS thread", "runtime: mmap":
				b.truncated = b.truncated || b.size+i+1 > b.lineSize
				b.size = copy(b.data[:], b.line[:b.lineSize])
				b.fatal = true
				p = p[i+1:]
				break scan
			}
		}
	}
	if b.fatal {
		b.truncated = b.truncated || len(p) > len(b.data)-b.size
		b.size += copy(b.data[b.size:], p)
		return n, nil
	}
	b.truncated = b.truncated || b.size+n > len(b.data)
	p = p[max(0, n-len(b.data)):]
	if excess := b.size + len(p) - len(b.data); excess > 0 {
		b.size = copy(b.data[:], b.data[excess:b.size])
	}
	b.size += copy(b.data[b.size:], p)
	return n, nil
}

func (b *supervisorOutputTail) snapshot(limit int) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	size := min(b.size, max(0, limit))
	if b.fatal {
		return string(b.data[:size]), b.truncated || size < b.size
	}
	return string(b.data[b.size-size : b.size]), b.truncated || size < b.size
}
