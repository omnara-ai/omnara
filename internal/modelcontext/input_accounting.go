package modelcontext

import (
	"encoding/json"
	"errors"
	"math"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

// ReasoningReplayAccounting accumulates usage for outputs that an adapter
// actually replays. The adapter retains all other input in its accounting body.
type ReasoningReplayAccounting struct {
	outputTokens int
	overflow     bool
}

// UseOutputUsage must only be called after admitting a reasoning-bearing replay.
// A true result replaces its model-generated output in the accounting body;
// tool results and synthetic continuation messages remain locally estimated.
func (a *ReasoningReplayAccounting) UseOutputUsage(usage modelenvelope.Usage) bool {
	usage = modelenvelope.NormalizeUsage(usage)
	if usage.OutputTokens <= 0 {
		return false
	}
	if usage.OutputTokens > math.MaxInt-a.outputTokens {
		a.overflow = true
	} else {
		a.outputTokens += usage.OutputTokens
	}
	return true
}

func (a *ReasoningReplayAccounting) EstimateInput(
	body json.RawMessage,
	media []RenderedMedia,
) (int, error) {
	estimate := EstimatePreparedRequest(body, media)
	if a.overflow || estimate < 0 || estimate > math.MaxInt-a.outputTokens {
		return 0, errors.New("rendered input token estimate overflows int")
	}
	return estimate + a.outputTokens, nil
}
