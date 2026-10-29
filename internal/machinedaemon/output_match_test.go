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

	matcher.scan(outputWindow{Output: "ok\nERROR one\nlisten", Start: 100, Next: 119})
	require.Equal(t, []string{"ERROR one"}, matcher.pending)
	matcher.scan(outputWindow{Output: "ing on :3000\r\nfine\n", Start: 119, Next: 140})
	require.Equal(t, []string{"ERROR one", "listening on :3000"}, matcher.pending)
	require.Equal(t, int64(140), matcher.cursor)

	require.False(t, matcher.due(false), "matches wait to settle")
	clock = clock.Add(outputMatchSettle)
	require.True(t, matcher.due(false))
	seq, result := matcher.take()
	require.Equal(t, int64(4), seq)
	require.Equal(t, []string{"ERROR one", "listening on :3000"}, result.Lines)
	require.Equal(t, "ERROR|listening", result.Pattern)

	matcher.scan(outputWindow{Output: "ERROR two\n", Start: 140, Next: 150})
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
	matcher.scan(outputWindow{Output: output.String(), Next: int64(output.Len())})
	require.Len(t, matcher.pending, outputMatchMaxLines)
	require.Equal(t, 3, matcher.dropped)

	matcher.finish()
	require.Equal(t, 4, matcher.dropped, "the unterminated last line counts once the process ends")
	_, result := matcher.take()
	require.Equal(t, 4, result.Dropped)

	matcher.scan(outputWindow{Output: "\n", Start: 10, Next: 11})
	matcher.cursor = 0
	matcher.scan(outputWindow{Output: "x" + strings.Repeat("z", outputMatchMaxLineRunes+5) + "\n", Start: 5})
	require.Len(t, matcher.pending, 1)
	require.True(t, strings.HasSuffix(matcher.pending[0], "…"))
	require.Equal(t, outputMatchMaxLineRunes+1, len([]rune(matcher.pending[0])))
}

func TestOutputMatcherDropsPartialLineAfterRetentionGap(t *testing.T) {
	t.Parallel()

	matcher := newOutputMatcher(regexp.MustCompile(`ERROR`), 0, 0)
	matcher.scan(outputWindow{Output: "ERROR cut", Next: 9})
	matcher.scan(outputWindow{Output: "off\nERROR whole\n", Start: 20, Next: 36})
	require.Equal(t, []string{"ERROR whole"}, matcher.pending)
}
