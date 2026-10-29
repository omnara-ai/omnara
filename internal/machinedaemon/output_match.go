package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
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
	outputMatchSettle       = 2 * time.Second
	outputMatchMinInterval  = 10 * time.Second
	outputMatchMaxLines     = 20
	outputMatchMaxLineRunes = 300
)

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

func (m *outputMatcher) scan(window outputWindow) {
	if window.Start > m.cursor {
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
		Pattern: m.pattern.String(),
		Lines:   m.pending,
		Dropped: m.dropped,
	}
	m.pending = nil
	m.dropped = 0
	m.lastReport = m.now()
	return m.seq, result
}

type outputWatch struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *runnerServerState) startOutputWatch(
	runner *localProcessRunner,
	payload processaction.WatchPayload,
) error {
	pattern, err := regexp.Compile(payload.Pattern)
	if err != nil {
		return err
	}
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.watchClosed {
		return errOutputWatchClosed
	}
	s.stopOutputWatchLocked()
	cursor := runner.output.End()
	if payload.Cursor != nil {
		cursor = *payload.Cursor
	}
	s.mu.RLock()
	parent := s.runCtx
	s.mu.RUnlock()
	if parent == nil {
		return errors.New("the process has no live runner")
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(payload.TimeoutSeconds)*time.Second)
	watch := &outputWatch{cancel: cancel, done: make(chan struct{})}
	s.watch = watch
	go func() {
		defer close(watch.done)
		defer cancel()
		s.watchOutputMatches(ctx, parent, runner, pattern, cursor)
	}()
	return nil
}

var errOutputWatchClosed = errors.New("the process has ended")

func (s *runnerServerState) stopOutputWatchLocked() {
	if s.watch != nil {
		s.watch.cancel()
		<-s.watch.done
		s.watch = nil
	}
}

func (s *runnerServerState) closeOutputWatch(ctx context.Context) {
	s.watchMu.Lock()
	s.watchClosed = true
	watch := s.watch
	s.watchMu.Unlock()
	if watch == nil {
		return
	}
	select {
	case <-watch.done:
	case <-ctx.Done():
	}
}

func (s *runnerServerState) watchOutputMatches(
	ctx, parent context.Context,
	runner *localProcessRunner,
	pattern *regexp.Regexp,
	cursor int64,
) {
	var seq int64
	if err := s.autonomousStateWrite(parent, func() error {
		var readErr error
		seq, readErr = s.processState.LastOutputMatchSeq(parent)
		return readErr
	}); err != nil {
		return
	}
	matcher := newOutputMatcher(pattern, cursor, seq)
	for {
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
		stopping := terminal || ctx.Err() != nil
		if matcher.due(stopping) && !s.freezeOutputMatch(parent, runner, matcher) {
			return
		}
		if stopping {
			return
		}
		_ = sleepContext(ctx, outputMatchPollInterval)
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
