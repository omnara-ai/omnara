package modelcontext

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/stretchr/testify/require"
)

func TestReasoningAccountingRejectsOverflow(t *testing.T) {
	for _, count := range []int{1, 2} {
		var accounting ReasoningReplayAccounting
		for range count {
			require.True(t, accounting.UseOutputUsage(modelenvelope.Usage{OutputTokens: math.MaxInt}))
		}
		_, err := accounting.EstimateInput(json.RawMessage(`{"input":[]}`), nil)
		require.ErrorContains(t, err, "overflows")
	}
}

func TestReasoningAccountingPreservesUnknownUsage(t *testing.T) {
	var accounting ReasoningReplayAccounting
	for _, usage := range []modelenvelope.Usage{
		{},
		{OutputTokens: 1, ReasoningTokens: 2},
	} {
		require.False(t, accounting.UseOutputUsage(usage))
	}
	require.True(t, accounting.UseOutputUsage(modelenvelope.Usage{OutputTokens: 500}))
	body := json.RawMessage(`{"input":[{"type":"reasoning","encrypted_content":"still-counted"}]}`)
	result, err := accounting.EstimateInput(body, nil)
	require.NoError(t, err)
	require.Equal(t, 500+EstimatePreparedRequest(body, nil), result)
}
