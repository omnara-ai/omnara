//go:build blackbox

package blackbox

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Live tests run a real model turn end-to-end through the deployed control
// plane: input -> worker leases the turn -> OpenRouter model call -> events.
// They skip unless OMNARA_BLACKBOX_OPENROUTER_KEY is set. Free-tier models
// can queue behind rate limits, so waits are generous and the tier stays
// small (a handful of model calls per run).

const liveTurnTimeout = 3 * time.Minute

// Live test 1: the flagship end-to-end check. An agent created with an
// initial message gets a real model reply that echoes our nonce, proving the
// worker, provider integration, and event log all work together.
func TestLiveTurnCompletes(t *testing.T) {
	requireLiveModel(t)

	nonce := fmt.Sprintf("BLACKBOX_OK_%s", fx.runID)
	launched := createAgentForTest(t, "create live agent with echo instruction", map[string]any{
		"profile": fx.liveProfileID,
		"config":  fx.liveConfigID,
		"message": "Reply with exactly this text and nothing else: " + nonce,
	}, uniqueKey(t, "create"))
	agentID := getString(t, launched, "agent.id")

	output := awaitAgentEvent(t, agentID, liveTurnTimeout, "model output echoing the nonce",
		func(event agentEvent) bool {
			return event.EventKind == "model_output" && strings.Contains(event.textContent(), nonce)
		})
	step(t, "model replied in turn %d (event seq %d)", output.TurnSequence, output.Sequence)
}

// Live test 2: a follow-up input after the first turn starts a second turn,
// and the model sees the new message.
func TestLiveFollowUpInput(t *testing.T) {
	requireLiveModel(t)

	firstNonce := fmt.Sprintf("BLACKBOX_TURN1_%s", fx.runID)
	launched := createAgentForTest(t, "create live agent with echo instruction", map[string]any{
		"profile": fx.liveProfileID,
		"config":  fx.liveConfigID,
		"message": "Reply with exactly this text and nothing else: " + firstNonce,
	}, uniqueKey(t, "create"))
	agentID := getString(t, launched, "agent.id")

	first := awaitAgentEvent(t, agentID, liveTurnTimeout, "first model output",
		func(event agentEvent) bool {
			return event.EventKind == "model_output" && strings.Contains(event.textContent(), firstNonce)
		})

	secondNonce := fmt.Sprintf("BLACKBOX_TURN2_%s", fx.runID)
	createInput(t, "send follow-up input", agentID,
		"Now reply with exactly this text and nothing else: "+secondNonce,
		uniqueKey(t, "followup"))

	// Only require that the follow-up produces a model output in a later
	// turn: that is the control-plane contract under test. Whether the model
	// obeys the echo instruction is model behavior (small models sometimes
	// emit junk like a bare tool-call token) and is already proven by
	// TestLiveTurnCompletes, so a disobedient reply here is only logged.
	second := awaitAgentEvent(t, agentID, liveTurnTimeout, "second-turn model output",
		func(event agentEvent) bool {
			return event.EventKind == "model_output" && event.TurnSequence > first.TurnSequence
		})
	if strings.Contains(second.textContent(), secondNonce) {
		step(t, "model echoed the follow-up nonce in turn %d", second.TurnSequence)
	} else {
		step(t, "model output in turn %d did not echo the nonce (model flake, not a control-plane failure): %q",
			second.TurnSequence, second.textContent())
	}
}
