package compaction

import (
	"context"
	"errors"
	"fmt"
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
	events, err := r.loadClosedEventRange(ctx, input.Plan)
	if err != nil {
		return 0, err
	}
	excerptBytes, err := sourceExcerptLimit(input, events, claim.Context.SourceExcerptReductionCount)
	if err != nil {
		return 0, err
	}
	return nextSourceExcerptLimit(input, events, excerptBytes)
}

func sourceExcerptLimit(
	input RunInput,
	events []executionstore.CompactionSourceEventRecord,
	reductions int,
) (*int, error) {
	if reductions < 0 {
		return nil, errors.New("compaction source reduction count cannot be negative")
	}
	var excerptBytes *int
	for range reductions {
		next, err := nextSourceExcerptLimit(input, events, excerptBytes)
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
	events []executionstore.CompactionSourceEventRecord,
	excerptBytes *int,
) (int, error) {
	previous, err := renderCompactionSource(input, events, excerptBytes)
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
		candidate, err := renderCompactionSource(input, events, &next)
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
	events []executionstore.CompactionSourceEventRecord,
	excerptBytes *int,
) (compactionSource, error) {
	var projected compactionSource
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
		projected.omissionNotice = fmt.Sprintf(
			"Some details of old events %d..%d were omitted from the summary source after provider overflow. "+
				"The original events remain stored; this checkpoint does not contain their full content.",
			events[0].Sequence, events[oldEnd-1].Sequence,
		)
	}
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
