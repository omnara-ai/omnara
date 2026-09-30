package modelcontext

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCheckpointExcerptRecoveryShrinksToNoticeWithoutChangingStoredContext(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("earlier decisions and work ", 2_000),
		strings.Repeat("旧的决定🧠\n<&>\"\\", 2_000),
	} {
		t.Run(text[:8], func(t *testing.T) {
			id := uuid.New()
			summary := "ORIGINAL_HEAD " + text + " ORIGINAL_TAIL"
			original := Bundle{
				ContextCheckpoint: &CheckpointRef{
					ID: id.String(), Summary: summary, SummarizedThroughEventSequence: 10,
				},
				Messages: []Message{{Content: json.RawMessage(`{"text":"keep the current request exactly"}`)}},
			}
			before, err := json.Marshal(ProjectedCheckpointContent(*original.ContextCheckpoint))
			require.NoError(t, err)
			var retained *int
			for attempt := 0; ; attempt++ {
				require.Less(t, attempt, 20, "a geometric recovery must terminate")
				next, ok, err := NextCheckpointExcerptBytes(summary, retained)
				require.NoError(t, err)
				if !ok {
					require.NotNil(t, retained)
					require.Zero(t, *retained)
					break
				}
				if retained != nil {
					require.Less(t, next, *retained)
				}
				projected, err := ApplyCheckpointExcerpt(original, id, next)
				require.NoError(t, err)
				require.True(t, utf8.ValidString(projected.ContextCheckpoint.Summary))
				require.Contains(t, projected.ContextCheckpoint.Summary, "remains stored")
				require.Equal(t, original.Messages, projected.Messages)
				require.Equal(t, int64(10), projected.ContextCheckpoint.SummarizedThroughEventSequence)
				require.Equal(t, summary, original.ContextCheckpoint.Summary)
				after, err := json.Marshal(ProjectedCheckpointContent(*projected.ContextCheckpoint))
				require.NoError(t, err)
				require.Less(t, len(after), len(before))
				if next > 0 {
					require.Contains(t, projected.ContextCheckpoint.Summary, "ORIGINAL_HEAD")
					require.Contains(t, projected.ContextCheckpoint.Summary, "ORIGINAL_TAIL")
				} else {
					require.NotContains(t, projected.ContextCheckpoint.Summary, "ORIGINAL_HEAD")
					require.NotContains(t, projected.ContextCheckpoint.Summary, "ORIGINAL_TAIL")
				}
				before, retained = after, &next
			}
		})
	}
}

func TestCheckpointExcerptRejectsUnrelatedOrInvalidProjection(t *testing.T) {
	id := uuid.New()
	bundle := Bundle{ContextCheckpoint: &CheckpointRef{ID: id.String(), Summary: "saved state"}}
	for _, invalid := range []int{-1, len(bundle.ContextCheckpoint.Summary), len(bundle.ContextCheckpoint.Summary) + 1} {
		_, err := ApplyCheckpointExcerpt(bundle, id, invalid)
		require.Error(t, err)
	}
	_, err := ApplyCheckpointExcerpt(bundle, uuid.New(), 0)
	require.Error(t, err)
	_, err = ApplyCheckpointExcerpt(Bundle{}, id, 0)
	require.Error(t, err)
	_, err = ApplyCheckpointExcerpt(bundle, uuid.Nil, 0)
	require.Error(t, err)
}

func TestCheckpointExcerptDoesNotExpandTinyMemory(t *testing.T) {
	_, ok, err := NextCheckpointExcerptBytes("Done.", nil)
	require.NoError(t, err)
	require.False(t, ok)
}
