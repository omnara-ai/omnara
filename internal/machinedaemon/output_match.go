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

// outputMatcher finds complete output lines that match a pattern and batches
// them into reports.
type outputMatcher struct {
	pattern      *regexp.Regexp
	now          func() time.Time
	cursor       int64
	partial      string
	pending      []string
	dropped      int
	firstPending time.Time
	lastReport   time.Time
	seq          int64
}

func newOutputMatcher(pattern *regexp.Regexp, cursor, seq int64) *outputMatcher {
	return &outputMatcher{pattern: pattern, now: time.Now, cursor: cursor, seq: seq}
}

// scan consumes a contiguous window read from the matcher's cursor.
func (m *outputMatcher) scan(window outputWindow) {
	if window.Start > m.cursor {
		// Older output was dropped from retention, so the partial line is gone.
		m.partial = ""
	}
	m.cursor = window.Next
	lines := strings.Split(m.partial+window.Output, "\n")
	m.partial = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		m.consider(line)
	}
	if len(m.partial) > processaction.MaxObservationBytes {
		m.consider(m.partial)
		m.partial = ""
	}
}

// finish treats a trailing line without a newline as complete.
func (m *outputMatcher) finish() {
	if m.partial != "" {
		m.consider(m.partial)
		m.partial = ""
	}
}

func (m *outputMatcher) consider(line string) {
	line = strings.TrimSuffix(line, "\r")
	if !m.pattern.MatchString(line) {
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

// due reports whether pending matches should be reported now.
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

// take returns the next report's sequence and result and clears the pending matches.
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
// notify_match from cursor until the process ends, flushing what is pending
// before returning so it precedes the terminal report.
func (s *runnerServerState) watchOutputMatches(
	ctx context.Context,
	runner *localProcessRunner,
	cursor int64,
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
	matcher := newOutputMatcher(pattern, cursor, seq)
	for !matcher.exhausted() {
		terminal := runner.hasTerminalResult()
		for {
			window, err := runner.Window(matcher.cursor, processaction.MaxObservationBytes, 0)
			if err != nil {
				return
			}
			if window.Output == "" {
				break
			}
			matcher.scan(window)
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
