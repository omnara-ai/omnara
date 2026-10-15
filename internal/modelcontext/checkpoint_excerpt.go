package modelcontext

import (
	"encoding/json"
	"errors"
	"math"
	"unicode/utf8"

	"github.com/google/uuid"
)

const minimumCheckpointExcerptBytes = 256

func ApplyCheckpointExcerpt(bundle Bundle, checkpointID uuid.UUID, reductions int) (Bundle, error) {
	if checkpointID == uuid.Nil || bundle.ContextCheckpoint == nil ||
		bundle.ContextCheckpoint.ID != checkpointID.String() {
		return Bundle{}, errors.New("checkpoint excerpt must reference the applicable checkpoint")
	}
	if reductions <= 0 {
		return Bundle{}, errors.New("checkpoint excerpt reductions must be positive")
	}
	checkpoint := *bundle.ContextCheckpoint
	retainedBytes, err := CheckpointExcerptBytes(checkpoint.Summary, reductions)
	if err != nil {
		return Bundle{}, err
	}
	if retainedBytes == nil {
		return bundle, nil
	}
	checkpoint.Summary = checkpointExcerpt(checkpoint.Summary, *retainedBytes)
	checkpoint.Excerpted = true
	bundle.ContextCheckpoint = &checkpoint
	return bundle, nil
}

func CheckpointExcerptBytes(summary string, reductions int) (*int, error) {
	if reductions < 0 {
		return nil, errors.New("checkpoint excerpt reductions cannot be negative")
	}
	var retained *int
	for range reductions {
		next, reduced, err := NextCheckpointExcerptBytes(summary, retained)
		if err != nil {
			return nil, err
		}
		if !reduced {
			break
		}
		retained = &next
	}
	return retained, nil
}

func NextCheckpointExcerptBytes(summary string, current *int) (int, bool, error) {
	previous := summary
	next := min(len(summary)/2, math.MaxInt32)
	if current != nil {
		if *current <= 0 || *current >= len(summary) {
			return 0, false, nil
		}
		previous = checkpointExcerpt(summary, *current)
		next = *current / 2
	}
	previousBytes, err := checkpointContentBytes(previous)
	if err != nil {
		return 0, false, err
	}
	for {
		if next < minimumCheckpointExcerptBytes {
			next = 0
		}
		candidateBytes, err := checkpointContentBytes(checkpointExcerpt(summary, next))
		if err != nil {
			return 0, false, err
		}
		if candidateBytes < previousBytes {
			return next, true, nil
		}
		if next == 0 {
			return 0, false, nil
		}
		next /= 2
	}
}

func checkpointContentBytes(summary string) (int, error) {
	projected := ProjectedCheckpointContent(CheckpointRef{Summary: summary})
	content, err := json.Marshal(projected)
	return len(content), err
}

func checkpointExcerpt(summary string, retainedBytes int) string {
	if retainedBytes == 0 {
		return "[Earlier history omitted to fit the model. The original checkpoint remains stored.]"
	}
	headEnd := retainedBytes * 2 / 3
	for headEnd > 0 && !utf8.RuneStart(summary[headEnd]) {
		headEnd--
	}
	tailStart := len(summary) - (retainedBytes - headEnd)
	for tailStart < len(summary) && !utf8.RuneStart(summary[tailStart]) {
		tailStart++
	}
	return "[Earlier history excerpted to fit the model. Omitted details are unknown here; " +
		"the full checkpoint remains stored.]\n" + summary[:headEnd] +
		"\n[... earlier history omitted ...]\n" + summary[tailStart:]
}
