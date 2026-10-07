package compaction

import (
	"context"
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
	previous, err := renderCompactionSource(input, prior, events, claim.Context.SourceExcerptBytes)
	if err != nil {
		return 0, err
	}
	previousBytes := len(previous.priorSummary) + len(previous.sourceText)
	next := min(initialSourceExcerptBytes, previousBytes/2)
	if claim.Context.SourceExcerptBytes != nil {
		if *claim.Context.SourceExcerptBytes <= minimumSourceExcerptBytes {
			return 0, nil
		}
		next = *claim.Context.SourceExcerptBytes / 2
	}
	next = max(minimumSourceExcerptBytes, next)
	for {
		candidate, err := renderCompactionSource(input, prior, events, &next)
		if err != nil {
			return 0, err
		}
		if len(candidate.priorSummary)+len(candidate.sourceText) < previousBytes {
			return next, nil
		}
		if next == minimumSourceExcerptBytes {
			return 0, nil
		}
		next = max(minimumSourceExcerptBytes, next/2)
	}
}

type compactionSource struct {
	priorSummary   string
	sourceText     string
	omissionNotice string
}

func renderCompactionSource(
	input RunInput,
	prior executionstore.ContextCheckpointRecord,
	events []executionstore.CompactionSourceEventRecord,
	excerptBytes *int,
) (compactionSource, error) {
	projected := compactionSource{priorSummary: prior.Summary}
	var notices []string
	if prior.HasOmittedHistory {
		notices = append(notices, "Some details of earlier history were omitted after provider overflow. "+
			"Original events and checkpoints remain stored; this checkpoint does not contain their full content.")
	}
	if excerptBytes != nil && prior.SummarizedThroughEventSequence < input.OpeningEventSequence &&
		len(prior.Summary) > *excerptBytes {
		projected.priorSummary = excerptStoredHistory(
			prior.Summary, *excerptBytes, "checkpoint "+prior.ID.String(),
		)
		notices = append(notices, fmt.Sprintf(
			"Some details of prior checkpoint %s were omitted from the summary source after provider overflow. "+
				"The original checkpoint remains stored; this checkpoint does not contain its full content.", prior.ID,
		))
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
	return excerptStoredHistory(content, retainedBytes, "event")
}

func excerptStoredHistory(content string, retainedBytes int, original string) string {
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
		"\n[OVERSIZED CLOSED HISTORY EXCERPT: %d bytes omitted after the summary provider rejected the full source. The original %s is stored; omitted details are unknown here.]\n",
		tailStart-headEnd,
		original,
	) + content[tailStart:]
}
