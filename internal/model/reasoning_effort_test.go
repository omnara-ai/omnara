package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
		require.Equal(t, tc.want, LowerReasoningEffort(tc.effort, tc.supported, tc.steps),
			"%q %v -%d", tc.effort, tc.supported, tc.steps)
	}
}

func TestEffortSteeringAccumulates(t *testing.T) {
	t.Parallel()

	unknown := NewEffortSteering("", nil)
	_, ok := unknown.Lower(1)
	require.False(t, ok, "an unknown starting effort cannot be lowered")
	var disabled EffortSteering
	_, ok = disabled.Lower(1)
	require.False(t, ok, "the zero value renders no change")

	steering := NewEffortSteering("max", nil)
	_, ok = steering.Lower(0)
	require.False(t, ok)
	effort, ok := steering.Lower(1)
	require.True(t, ok)
	require.Equal(t, "xhigh", effort)
	effort, ok = steering.Lower(2)
	require.True(t, ok)
	require.Equal(t, "medium", effort, "a later change lowers from the effort already in force")
	effort, ok = steering.Lower(4)
	require.True(t, ok)
	require.Equal(t, "low", effort)
	_, ok = steering.Lower(1)
	require.False(t, ok, "nothing is rendered once effort is at its floor")
}

func TestPerMessageEffortModelSupport(t *testing.T) {
	t.Parallel()

	for slug, want := range map[string]bool{
		"claude-fable-5-2":            true,
		"claude-mythos-5-1":           true,
		"claude-opus-5":               true,
		"claude-opus-5-5-20261001":    true,
		"claude-sonnet-5-5":           true,
		"claude-haiku-5-5":            true,
		"claude-fable-5":              false,
		"claude-mythos-5":             false,
		"claude-sonnet-5":             false,
		"claude-opus-4-8":             false,
		"claude-haiku-4-5-20251001":   false,
		"anthropic/claude-opus-5.5":   true,
		"anthropic.claude-sonnet-5-5": true,
		"MiniMax-M3":                  false,
	} {
		require.Equal(t, want, ClaudeSupportsPerMessageEffort(slug), slug)
	}
	for slug, want := range map[string]bool{
		"anthropic/claude-fable-5.2":    true,
		"anthropic/claude-fable-5.1":    true,
		"anthropic/claude-opus-5":       true,
		"anthropic/claude-opus-5.5":     false,
		"anthropic/claude-sonnet-5.5":   false,
		"anthropic/claude-mythos-5.2":   false,
		"openai/gpt-6-sol":              true,
		"openai/gpt-6-astra":            true,
		"openai/gpt-6-luna":             true,
		"openai/gpt-6.1-terra":          true,
		"openai/gpt-6.1-sol-2026-09-01": true,
		"openai/gpt-6-sol-pro":          false,
		"openai/gpt-5.5":                false,
		"google/gemini-3-pro":           false,
	} {
		require.Equal(t, want, OpenRouterSupportsConfigurationUpdate(slug), slug)
	}
}

func TestClaudeDefaultReasoningEffort(t *testing.T) {
	t.Parallel()

	for slug, want := range map[string]string{
		"claude-opus-5-5":   "medium",
		"claude-haiku-5-5":  "medium",
		"claude-sonnet-5-5": "high",
		"claude-fable-5-2":  "high",
		"claude-opus-5":     "high",
		"claude-opus-4-8":   "",
		"gpt-6-sol":         "",
	} {
		require.Equal(t, want, ClaudeDefaultReasoningEffort(slug), slug)
	}
}
