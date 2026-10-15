package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	initialSourceExcerptBytes = 32_768
	minimumSourceExcerptBytes = 256
)

func (r Runner) nextSourceExcerptBytes(
	ctx context.Context,
	input RunInput,
	claim executionstore.ModelCallClaim,
) (int, error) {
	prior, _, err := r.Store.GetLatestApplicableContextCheckpoint(
		ctx, input.Plan.ProjectID, input.Plan.AgentID, input.Plan.InputEventSequence,
	)
	if err != nil {
		return 0, err
	}
	events, err := r.loadClosedEventRange(ctx, input.Plan)
	if err != nil {
		return 0, err
	}
	excerptBytes, err := sourceExcerptLimit(input, prior, events, claim.Context.SourceExcerptReductionCount)
	if err != nil {
		return 0, err
	}
	return nextSourceExcerptLimit(input, prior, events, excerptBytes)
}

func sourceExcerptLimit(
	input RunInput,
	prior executionstore.ContextCheckpointRecord,
	events []executionstore.CompactionSourceEventRecord,
	reductions int,
) (*int, error) {
	if reductions < 0 {
		return nil, errors.New("compaction source reduction count cannot be negative")
	}
	var excerptBytes *int
	for range reductions {
		next, err := nextSourceExcerptLimit(input, prior, events, excerptBytes)
		if err != nil {
			return nil, err
		}
		if next == 0 {
			break
		}
		excerptBytes = &next
	}
	return excerptBytes, nil
}

func nextSourceExcerptLimit(
	input RunInput,
	prior executionstore.ContextCheckpointRecord,
	events []executionstore.CompactionSourceEventRecord,
	excerptBytes *int,
) (int, error) {
	previous, err := renderCompactionSource(input, prior, events, excerptBytes)
	if err != nil {
		return 0, err
	}
	previousBytes := len(previous.sourceText)
	next := min(initialSourceExcerptBytes, previousBytes/2)
	if excerptBytes != nil {
		if *excerptBytes <= minimumSourceExcerptBytes {
			return 0, nil
		}
		next = *excerptBytes / 2
	}
	next = max(minimumSourceExcerptBytes, next)
	for {
		candidate, err := renderCompactionSource(input, prior, events, &next)
		if err != nil {
			return 0, err
		}
		if len(candidate.sourceText) < previousBytes {
			return next, nil
		}
		if next == minimumSourceExcerptBytes {
			return 0, nil
		}
		next = max(minimumSourceExcerptBytes, next/2)
	}
}

type compactionSource struct {
	sourceText     string
	omissionNotice string
}

func renderCompactionSource(
	input RunInput,
	prior executionstore.ContextCheckpointRecord,
	events []executionstore.CompactionSourceEventRecord,
	excerptBytes *int,
) (compactionSource, error) {
	var projected compactionSource
	var notices []string
	if prior.HasOmittedHistory {
		notices = append(notices, "Some details of earlier history were omitted after provider overflow. "+
			"Original events and checkpoints remain stored; this checkpoint does not contain their full content.")
	}
	oldEnd := 0
	for oldEnd < len(events) && events[oldEnd].Sequence < input.OpeningEventSequence {
		oldEnd++
	}
	oldText, omitted, err := renderEventSourceProjection(events[:oldEnd], excerptBytes)
	if err != nil {
		return compactionSource{}, err
	}
	currentText, err := renderEventSource(events[oldEnd:])
	if err != nil {
		return compactionSource{}, err
	}
	projected.sourceText = oldText + currentText
	if omitted {
		notices = append(notices, fmt.Sprintf(
			"Some details of old events %d..%d were omitted from the summary source after provider overflow. "+
				"The original events remain stored; this checkpoint does not contain their full content.",
			events[0].Sequence, events[oldEnd-1].Sequence,
		))
	}
	projected.omissionNotice = strings.Join(notices, "\n")
	return projected, nil
}

func excerptClosedEvent(content string, retainedBytes int) string {
	if len(content) <= retainedBytes {
		return content
	}
	headEnd := retainedBytes * 2 / 3
	for headEnd > 0 && !utf8.RuneStart(content[headEnd]) {
		headEnd--
	}
	tailStart := len(content) - (retainedBytes - headEnd)
	for tailStart < len(content) && !utf8.RuneStart(content[tailStart]) {
		tailStart++
	}
	return content[:headEnd] + fmt.Sprintf(
		"\n[OVERSIZED CLOSED HISTORY EXCERPT: %d bytes omitted after the summary provider rejected the full source. The original event is stored; omitted details are unknown here.]\n",
		tailStart-headEnd,
	) + content[tailStart:]
}
