package modelretry

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/stretchr/testify/require"
)

func TestRecoverFromOutputNoProgress(t *testing.T) {
	t.Parallel()

	base := model.RequestPolicy{MaxOutputTokens: 8192, ReasoningEffort: "high"}
	got, notice := RecoverFromOutputNoProgress(base, 0)
	require.Equal(t, base, got)
	require.Empty(t, notice)

	for _, failures := range []int{1, 3} {
		got, notice = RecoverFromOutputNoProgress(base, failures)
		want := base
		want.FinalTurnEffortReduction = failures
		require.Equal(t, want, got, "top-level effort must stay unchanged")
		require.Equal(t, OutputNoProgressNotice, notice)
	}
}

func TestLowerReasoningEffort(t *testing.T) {
	t.Parallel()

	anthropic := []string{"low", "medium", "high", "xhigh", "max"}
	openAI := []string{"none", "minimal", "low", "medium", "high", "xhigh"}
	for _, tc := range []struct {
		effort    string
		supported []string
		steps     int
		want      string
	}{
		{effort: "high", supported: anthropic, steps: 0, want: "high"},
		{effort: "high", supported: anthropic, steps: 1, want: "medium"},
		{effort: "max", supported: anthropic, steps: 2, want: "high"},
		{effort: "medium", supported: openAI, steps: 5, want: "low"},
		{effort: "low", supported: openAI, steps: 1, want: "low"},
		{effort: "max", supported: []string{"low", "high", "max"}, steps: 1, want: "high"},
		{effort: "xhigh", steps: 1, want: "high"},
		{effort: "HIGH", supported: []string{"Low", "Medium", "High"}, steps: 1, want: "Medium"},
		{effort: "model-specific", supported: anthropic, steps: 1, want: "model-specific"},
		{effort: "", supported: anthropic, steps: 1, want: ""},
	} {
		require.Equal(t, tc.want, model.LowerReasoningEffort(tc.effort, tc.supported, tc.steps),
			"%q %v -%d", tc.effort, tc.supported, tc.steps)
	}
}
