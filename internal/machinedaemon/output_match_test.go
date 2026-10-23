package machinedaemon

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOutputMatcherBatchesCompleteMatchingLines(t *testing.T) {
	t.Parallel()

	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	matcher := newOutputMatcher(regexp.MustCompile(`ERROR|listening`), 100, 3)
	matcher.now = func() time.Time { return clock }

	matcher.scan(outputWindow{Output: "ok\nERROR one\nlisten", Start: 100, Next: 119}, false)
	require.Equal(t, []string{"ERROR one"}, matcher.pending)
	matcher.scan(outputWindow{Output: "ing on :3000\r\nfine\n", Start: 119, Next: 140}, false)
	require.Equal(t, []string{"ERROR one", "listening on :3000"}, matcher.pending)
	require.Equal(t, int64(140), matcher.cursor)

	require.False(t, matcher.due(false), "matches wait to settle")
	clock = clock.Add(outputMatchSettle)
	require.True(t, matcher.due(false))
	seq, result := matcher.take()
	require.Equal(t, int64(4), seq)
	require.Equal(t, []string{"ERROR one", "listening on :3000"}, result.Lines)
	require.False(t, result.Final)

	matcher.scan(outputWindow{Output: "ERROR two\n", Start: 140, Next: 150}, false)
	clock = clock.Add(outputMatchSettle)
	require.False(t, matcher.due(false), "reports are spaced out")
	require.True(t, matcher.due(true), "a final flush does not wait")
	clock = clock.Add(outputMatchMinInterval)
	require.True(t, matcher.due(false))
}

func TestOutputMatcherLimitsLinesAndFinishesPartialLine(t *testing.T) {
	t.Parallel()

	matcher := newOutputMatcher(regexp.MustCompile(`x`), 0, 0)
	var output strings.Builder
	for i := range outputMatchMaxLines + 3 {
		fmt.Fprintf(&output, "x%d\n", i)
	}
	output.WriteString("x" + strings.Repeat("y", outputMatchMaxLineRunes+5))
	matcher.scan(outputWindow{Output: output.String(), Next: int64(output.Len())}, false)
	require.Len(t, matcher.pending, outputMatchMaxLines)
	require.Equal(t, 3, matcher.dropped)

	matcher.finish()
	require.Equal(t, 4, matcher.dropped, "the unterminated last line counts once the process ends")
	_, result := matcher.take()
	require.Equal(t, 4, result.Dropped)

	matcher.scan(outputWindow{Output: "\n", Start: 10, Next: 11}, false)
	matcher.cursor = 0
	matcher.scan(outputWindow{Output: "x" + strings.Repeat("z", outputMatchMaxLineRunes+5) + "\n", Start: 5}, false)
	require.Len(t, matcher.pending, 1)
	require.True(t, strings.HasSuffix(matcher.pending[0], "…"))
	require.Equal(t, outputMatchMaxLineRunes+1, len([]rune(matcher.pending[0])))
}

func TestOutputMatcherStopsAfterMaxReports(t *testing.T) {
	t.Parallel()

	matcher := newOutputMatcher(regexp.MustCompile(`x`), 0, outputMatchMaxReports-1)
	matcher.scan(outputWindow{Output: "x\n", Next: 2}, false)
	require.False(t, matcher.exhausted())
	seq, result := matcher.take()
	require.Equal(t, int64(outputMatchMaxReports), seq)
	require.True(t, result.Final)
	require.True(t, matcher.exhausted())
}

func TestOutputMatcherSkipsOnlyLinesShownInFull(t *testing.T) {
	t.Parallel()

	head := "ERROR head\nERROR cut "
	omitted := "in two\nERROR omitted\nERROR also "
	tail := "cut\nERROR tail\n"
	later := "ERROR later\n"
	output := head + omitted + tail
	initial := outputWindow{
		Start:     0,
		HeadEnd:   int64(len(head)),
		TailStart: int64(len(head + omitted)),
		Next:      int64(len(output)),
	}
	output += later

	matcher := newOutputMatcher(regexp.MustCompile(`ERROR`), initial.Start, 0)
	matcher.shown = initial.shownSpans()
	for {
		shown, limit := matcher.region()
		end := int64(len(output))
		if limit > 0 {
			end = min(end, matcher.cursor+limit)
		}
		if end == matcher.cursor {
			break
		}
		matcher.scan(outputWindow{
			Output: output[matcher.cursor:end],
			Start:  matcher.cursor,
			Next:   end,
		}, shown)
	}
	require.Equal(t, []string{
		"ERROR cut in two",
		"ERROR omitted",
		"ERROR also cut",
		"ERROR later",
	}, matcher.pending)
}

func TestOutputMatcherSkipsWindowShownWithoutTail(t *testing.T) {
	t.Parallel()

	matcher := newOutputMatcher(regexp.MustCompile(`x`), 0, 0)
	matcher.shown = outputWindow{Next: 4}.shownSpans()
	shown, limit := matcher.region()
	require.True(t, shown)
	require.Equal(t, int64(4), limit)
	matcher.scan(outputWindow{Output: "x1\nx", Next: 4}, true)
	shown, limit = matcher.region()
	require.False(t, shown)
	require.Zero(t, limit)
	matcher.scan(outputWindow{Output: "2\nx3\n", Start: 4, Next: 9}, false)
	require.Equal(t, []string{"x2", "x3"}, matcher.pending)
}
