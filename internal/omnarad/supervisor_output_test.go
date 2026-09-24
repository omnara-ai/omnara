package omnarad

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSupervisorOutputTail(t *testing.T) {
	for _, chunks := range [][]string{
		{}, {"stdout\n", "stderr\n"}, {strings.Repeat("a", 4096)},
		{strings.Repeat("a", 5000), "last line"},
		{strings.Repeat("a", 3000), strings.Repeat("b", 3000)},
		{"old", strings.Repeat("b", 4096)},
	} {
		var tail supervisorOutputTail
		writer := supervisorOutputWriter{tail: &tail, stderr: true}
		for _, chunk := range chunks {
			n, err := writer.Write([]byte(chunk))
			require.NoError(t, err)
			require.Equal(t, len(chunk), n)
		}
		want := strings.Join(chunks, "")
		got, truncated := tail.snapshot(4096)
		require.Equal(t, len(want) > 4096, truncated)
		require.Equal(t, want[max(0, len(want)-4096):], got)
		_, err := writer.Write([]byte("next"))
		require.NoError(t, err)
		require.Equal(t, want[max(0, len(want)-4096):], got)
	}
}

func TestSupervisorOutputTailConcurrentWrites(t *testing.T) {
	var tail supervisorOutputTail
	var writers sync.WaitGroup
	for _, value := range []byte{'a', 'b'} {
		writer := supervisorOutputWriter{tail: &tail, stderr: value == 'b'}
		writers.Go(func() {
			for range 3000 {
				_, _ = writer.Write([]byte{value})
			}
		})
	}
	writers.Wait()
	got, truncated := tail.snapshot(4096)
	require.Len(t, got, 4096)
	require.True(t, truncated)
	require.Equal(t, 4096, strings.Count(got, "a")+strings.Count(got, "b"))
}

func TestSupervisorOutputPreservesFatalBeginning(t *testing.T) {
	for _, failure := range []struct {
		marker string
		detail string
	}{
		{"panic:", " actual cause\n"},
		{"fatal error:", " actual cause\n"},
		{
			"runtime: out of memory:",
			" cannot allocate 1048576-byte block (987654321 in use)\nfatal error: out of memory\n",
		},
		{
			"runtime: goroutine stack exceeds ",
			"32768-byte limit\nruntime: sp=0x100 stack=[0x100, 0x200]\nfatal error: stack overflow\n",
		},
		{"runtime: program exceeds ", "10000-thread limit\nfatal error: thread exhaustion\n"},
		{
			"runtime: failed to create new OS thread",
			" (have 12 already; errno=11)\nruntime: may need to increase max user processes (ulimit -u)\n" +
				"fatal error: newosproc\n",
		},
		{
			"runtime: mmap",
			"(0x100, 1024) returned 0x0, 12\nfatal error: runtime: cannot map pages in arena address space\n",
		},
	} {
		marker := failure.marker
		for split := 0; split <= len(marker); split++ {
			var tail supervisorOutputTail
			var forwarded bytes.Buffer
			stdout := io.MultiWriter(supervisorOutputWriter{tail: &tail}, &forwarded)
			stderr := supervisorOutputWriter{tail: &tail, stderr: true}
			_, err := stderr.Write([]byte(strings.Repeat("old log\n", 1000)))
			require.NoError(t, err)
			_, err = stdout.Write([]byte("partial stdout"))
			require.NoError(t, err)
			_, err = stderr.Write([]byte(marker[:split]))
			require.NoError(t, err)
			noise := strings.Repeat("stdout noise\n", 1000)
			n, err := stdout.Write([]byte(noise))
			require.NoError(t, err)
			require.Equal(t, len(noise), n)
			fatal := marker + failure.detail + strings.Repeat("stack frame\n", 1000)
			_, err = stderr.Write([]byte(fatal[split:]))
			require.NoError(t, err)
			_, err = stdout.Write([]byte(noise))
			require.NoError(t, err)
			_, err = stderr.Write([]byte("panic: later output\n"))
			require.NoError(t, err)
			got, truncated := tail.snapshot(4000)
			require.Equal(t, fatal[:4000], got)
			require.True(t, truncated)
			require.Equal(t, "partial stdout"+noise+noise, forwarded.String())
		}
	}
}

func TestSupervisorOutputStreamBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name         string
		stderrPrefix string
		stdout       string
		stderrSuffix string
		want         string
		truncated    bool
	}{
		{"ordinary", "stderr ", "stdout ", "tail", "stderr stdout tail", false},
		{"stdout marker", "", "panic: stdout only\n", strings.Repeat("x", 5000), strings.Repeat("x", 4096), true},
		{
			"stdout newline", "ordinary stderr ", "\n",
			"panic: ordinary\n" + strings.Repeat("x", 5000), strings.Repeat("x", 4096), true,
		},
		{"stdout after fatal", "panic: cause", "discarded", "\nstack", "panic: cause\nstack", true},
		{"empty stdout after fatal", "panic: cause", "", "\nstack", "panic: cause\nstack", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tail supervisorOutputTail
			stdout := supervisorOutputWriter{tail: &tail}
			stderr := supervisorOutputWriter{tail: &tail, stderr: true}
			_, err := stderr.Write([]byte(tc.stderrPrefix))
			require.NoError(t, err)
			n, err := stdout.Write([]byte(tc.stdout))
			require.NoError(t, err)
			require.Equal(t, len(tc.stdout), n)
			_, err = stderr.Write([]byte(tc.stderrSuffix))
			require.NoError(t, err)
			got, truncated := tail.snapshot(4096)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.truncated, truncated)
		})
	}
}

func TestSupervisorOutputFatalDetectionAndBudget(t *testing.T) {
	for _, tc := range []struct {
		chunks    []string
		limit     int
		want      string
		truncated bool
	}{
		{[]string{"pan", "ic: cause"}, 4096, "panic: cause", false},
		{[]string{"fatal ", "error: cause"}, 4096, "fatal error: cause", false},
		{[]string{"log panic: ordinary\n", "newest"}, 6, "newest", true},
		{[]string{"log runtime: out of memory: ordinary\n", "newest"}, 6, "newest", true},
		{
			[]string{"runtime: ReadTrace called from multiple goroutines simultaneously\n", "panic: real crash"},
			4096, "panic: real crash", true,
		},
		{[]string{"fatal error: cause\nstack"}, 18, "fatal error: cause", true},
		{[]string{"old log\npanic: cause"}, 4096, "panic: cause", true},
		{[]string{strings.Repeat("old log\n", 1000) + "fatal error: cause"}, 4096, "fatal error: cause", true},
		{[]string{"panic: cause"}, 0, "", true},
		{[]string{"ordinary"}, -1, "", true},
	} {
		var tail supervisorOutputTail
		writer := supervisorOutputWriter{tail: &tail, stderr: true}
		for _, chunk := range tc.chunks {
			n, err := writer.Write([]byte(chunk))
			require.NoError(t, err)
			require.Equal(t, len(chunk), n)
		}
		got, truncated := tail.snapshot(tc.limit)
		require.Equal(t, tc.want, got)
		require.Equal(t, tc.truncated, truncated)
	}
}
