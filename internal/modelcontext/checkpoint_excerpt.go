package modelcontext

import (
	"encoding/json"
	"errors"
	"math"
	"unicode/utf8"

	"github.com/google/uuid"
)

const minimumCheckpointExcerptBytes = 256

func ApplyCheckpointExcerpt(bundle Bundle, checkpointID uuid.UUID, retainedBytes int) (Bundle, error) {
	if checkpointID == uuid.Nil || bundle.ContextCheckpoint == nil ||
		bundle.ContextCheckpoint.ID != checkpointID.String() {
		return Bundle{}, errors.New("checkpoint excerpt must reference the applicable checkpoint")
	}
	checkpoint := *bundle.ContextCheckpoint
	if retainedBytes < 0 || retainedBytes >= len(checkpoint.Summary) {
		return Bundle{}, errors.New("checkpoint excerpt must retain fewer bytes than the stored summary")
	}
	checkpoint.Summary = checkpointExcerpt(checkpoint.Summary, retainedBytes)
	bundle.ContextCheckpoint = &checkpoint
	return bundle, nil
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
