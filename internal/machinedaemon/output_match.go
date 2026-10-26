package machinedaemon

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/processaction"
)

const (
	outputMatchPollInterval = 250 * time.Millisecond
	// outputMatchSettle gathers matches that arrive together into one report.
	outputMatchSettle = 2 * time.Second
	// outputMatchMinInterval spaces reports so a noisy log cannot flood the agent.
	outputMatchMinInterval  = 10 * time.Second
	outputMatchMaxLines     = 20
	outputMatchMaxLineRunes = 300
	outputMatchMaxReports   = 50
)

type outputSpan struct {
	start, end int64
}

// shownSpans returns the output the window's result showed the agent.
func (w outputWindow) shownSpans() []outputSpan {
	if w.TailStart > 0 {
		return []outputSpan{{w.Start, w.HeadEnd}, {w.TailStart, w.Next}}
	}
	return []outputSpan{{w.Start, w.Next}}
}

// outputMatcher finds complete output lines that match a pattern and batches
// them into reports. Lines that lie wholly within shown spans are skipped,
// since the agent has already seen them.
type outputMatcher struct {
	pattern      *regexp.Regexp
	now          func() time.Time
	cursor       int64
	shown        []outputSpan
	partial      string
	partialShown bool
	pending      []string
	dropped      int
	firstPending time.Time
	lastReport   time.Time
	seq          int64
}

func newOutputMatcher(pattern *regexp.Regexp, cursor, seq int64) *outputMatcher {
	return &outputMatcher{pattern: pattern, now: time.Now, cursor: cursor, seq: seq}
}

// region reports whether output at the cursor was shown, and how many bytes
// remain until that changes, or 0 if it never does.
func (m *outputMatcher) region() (bool, int64) {
	for _, span := range m.shown {
		if m.cursor < span.start {
			return false, span.start - m.cursor
		}
		if m.cursor < span.end {
			return true, span.end - m.cursor
		}
	}
	return false, 0
}

// scan consumes a contiguous window read from the matcher's cursor, all of
// it shown or all of it not.
func (m *outputMatcher) scan(window outputWindow, shown bool) {
	if window.Start > m.cursor {
		// Older output was dropped from retention, so the partial line is gone.
		m.partial = ""
	}
	m.cursor = window.Next
	firstShown := shown && (m.partial == "" || m.partialShown)
	lines := strings.Split(m.partial+window.Output, "\n")
	m.partial = lines[len(lines)-1]
	m.partialShown = shown
	if len(lines) == 1 {
		m.partialShown = firstShown
	}
	for i, line := range lines[:len(lines)-1] {
		m.consider(line, shown && (i > 0 || firstShown))
	}
	if len(m.partial) > processaction.MaxObservationBytes {
		m.consider(m.partial, m.partialShown)
		m.partial = ""
	}
}

// finish treats a trailing line without a newline as complete.
func (m *outputMatcher) finish() {
	if m.partial != "" {
		m.consider(m.partial, m.partialShown)
		m.partial = ""
	}
}

func (m *outputMatcher) consider(line string, shown bool) {
	line = strings.TrimSuffix(line, "\r")
	if shown || !m.pattern.MatchString(line) {
		return
	}
	if len(m.pending) == 0 && m.dropped == 0 {
		m.firstPending = m.now()
	}
	if len(m.pending) == outputMatchMaxLines {
		m.dropped++
		return
	}
	if utf8.RuneCountInString(line) > outputMatchMaxLineRunes {
		line = string([]rune(line)[:outputMatchMaxLineRunes]) + "…"
	}
	m.pending = append(m.pending, line)
}

func (m *outputMatcher) due(final bool) bool {
	if len(m.pending) == 0 && m.dropped == 0 {
		return false
	}
	if final {
		return true
	}
	now := m.now()
	return now.Sub(m.firstPending) >= outputMatchSettle &&
		(m.lastReport.IsZero() || now.Sub(m.lastReport) >= outputMatchMinInterval)
}

func (m *outputMatcher) take() (int64, daemonprotocol.OutputMatchResult) {
	m.seq++
	result := daemonprotocol.OutputMatchResult{
		Lines:   m.pending,
		Dropped: m.dropped,
		Final:   m.seq >= outputMatchMaxReports,
	}
	m.pending = nil
	m.dropped = 0
	m.lastReport = m.now()
	return m.seq, result
}

func (m *outputMatcher) exhausted() bool {
	return m.seq >= outputMatchMaxReports
}

// watchOutputMatches reports output lines matching the assignment's
// notify_match until the process ends, skipping lines the initial window
// showed in full, and flushes what is pending before returning so it precedes
// the terminal report.
func (s *runnerServerState) watchOutputMatches(
	ctx context.Context,
	runner *localProcessRunner,
	initial outputWindow,
) {
	pattern, err := regexp.Compile(s.assignment.NotifyMatch)
	if err != nil {
		return
	}
	var seq int64
	if err := s.autonomousStateWrite(ctx, func() error {
		var readErr error
		seq, readErr = s.processState.LastOutputMatchSeq(ctx)
		return readErr
	}); err != nil {
		return
	}
	matcher := newOutputMatcher(pattern, initial.Start, seq)
	matcher.shown = initial.shownSpans()
	for !matcher.exhausted() {
		terminal := runner.hasTerminalResult()
		for {
			shown, limit := matcher.region()
			maxBytes := processaction.MaxObservationBytes
			if limit > 0 && limit < int64(maxBytes) {
				maxBytes = int(limit)
			}
			window, err := runner.Window(matcher.cursor, maxBytes, 0)
			if err != nil {
				return
			}
			if window.Output == "" {
				break
			}
			matcher.scan(window, shown)
		}
		if terminal {
			matcher.finish()
		}
		if matcher.due(terminal) && !s.freezeOutputMatch(ctx, runner, matcher) {
			return
		}
		if terminal || sleepContext(ctx, outputMatchPollInterval) != nil {
			return
		}
	}
}

func (s *runnerServerState) freezeOutputMatch(
	ctx context.Context,
	runner *localProcessRunner,
	matcher *outputMatcher,
) bool {
	seq, matched := matcher.take()
	result, err := json.Marshal(matched)
	if err != nil {
		return false
	}
	body, err := json.Marshal(daemonReportedEvent{
		Type:       daemonprotocol.EventProcessOutputMatched,
		ProcessID:  s.assignment.ID,
		Sequence:   seq,
		ObservedAt: runner.sourceTimeNow(),
		Result:     result,
	})
	if err != nil {
		return false
	}
	report := statedb.Report{
		ProcessID: s.assignment.ID,
		Kind:      statedb.ReportProcessOutputMatch,
		Seq:       seq,
		Body:      body,
	}
	return s.autonomousStateWrite(ctx, func() error {
		_, freezeErr := s.processState.FreezeOutputMatchReport(ctx, report)
		return freezeErr
	}) == nil
}
