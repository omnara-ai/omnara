package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func applyRequestInputMeasurement(
	ctx context.Context,
	prepared *PreparedRequest,
	client Client,
	input PrepareForSendInput,
) {
	projector, ok := client.(RequestProjector)
	if !ok {
		return
	}
	bundle := input.Context
	var source *modelcontext.Message
	var usage modelenvelope.Usage
	// Select the newest compatible measurement, then verify one rendered prefix.
	// Later outputs without usable measurements remain part of the estimated delta.
	for i := len(bundle.Messages) - 1; i >= 0; i-- {
		candidate := &bundle.Messages[i]
		if candidate.Role != modelprotocol.RoleAssistant || candidate.StopReason == modelenvelope.StopReasonError {
			continue
		}
		candidateUsage := modelenvelope.NormalizeUsage(candidate.Usage)
		if candidateUsage.InputTokens <= 0 || candidate.InputEventSequence <= 0 ||
			candidate.InputEventSequence >= candidate.Sequence || candidate.InputFingerprint == "" ||
			bundle.AgentConfigID == uuid.Nil || candidate.AgentConfigID != bundle.AgentConfigID ||
			bundle.ConfiguredModelRevisionID == uuid.Nil ||
			candidate.ConfiguredModelRevisionID != bundle.ConfiguredModelRevisionID {
			continue
		}
		identity := candidate.ProviderReplaySource
		if identity.RequestedProviderModelSlug != client.RequestedProviderModelSlug() ||
			identity.APIFormat != client.APIFormat() || identity.APIVariant != client.ModelAPIVariant() {
			continue
		}
		if checkpoint := bundle.ContextCheckpoint; checkpoint != nil &&
			(checkpoint.Sequence == 0 || checkpoint.Sequence > candidate.InputEventSequence) {
			continue
		}
		source, usage = candidate, candidateUsage
		break
	}
	if source == nil {
		return
	}
	if mediaProjector, ok := client.(modelcontext.MediaProjector); ok &&
		!slices.EqualFunc(mediaProjector.ProjectRenderedMedia(bundle), prepared.RenderedMedia,
			func(original, final modelcontext.RenderedMedia) bool {
				return original.Occurrence == final.Occurrence && original.Media.ArtifactID == final.Media.ArtifactID &&
					original.Representation == final.Representation && original.RouteParsed == final.RouteParsed
			}) {
		return
	}

	prefix := bundle
	prefix.InputEventSequence = source.InputEventSequence
	prefix.Messages = nil
	prefix.ToolResults = nil
	for _, message := range bundle.Messages {
		if message.Sequence <= source.InputEventSequence {
			prefix.Messages = append(prefix.Messages, message)
		}
	}
	for _, result := range bundle.ToolResults {
		if result.ResultEventSequence <= source.InputEventSequence {
			prefix.ToolResults = append(prefix.ToolResults, result)
		} else if result.SourceEventSequence <= source.InputEventSequence {
			// A result arriving after the measured input changes an earlier turn.
			return
		}
	}
	previous, err := projector.Project(ctx, PrepareInput{Context: prefix, Policy: input.Policy})
	if err != nil || previous.InputFingerprint != source.InputFingerprint {
		return
	}
	// Compare complete inputs rendered under the same policy. This includes
	// loaded tool definitions once and follows adapter merging and replay choices.
	// The signed difference also handles removal of synthetic request framing.
	currentTokens, previousTokens := prepared.InputTokenEstimate, previous.InputTokenEstimate
	if currentTokens < 0 || previousTokens < 0 {
		return
	}
	delta := currentTokens - previousTokens
	if (delta > 0 && usage.InputTokens > math.MaxInt-delta) ||
		(delta < 0 && usage.InputTokens <= -delta) {
		return
	}
	prepared.InputTokenEstimate = usage.InputTokens + delta
	prepared.UsesProviderInputUsage = true
}

func RequestInputFingerprint(body json.RawMessage, endpoint string) string {
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil || request == nil {
		return ""
	}
	delete(request, "max_tokens")
	delete(request, "max_output_tokens")
	delete(request, "max_completion_tokens")
	delete(request, "user")
	delete(request, "safety_identifier")
	delete(request, "metadata")
	encoded, err := json.Marshal(request)
	if err != nil {
		return ""
	}
	canonical, err := jsoncanonical.Normalize(encoded)
	if err != nil {
		return ""
	}
	digest := sha256.New()
	digest.Write([]byte(endpoint))
	digest.Write([]byte{0})
	digest.Write(canonical)
	return hex.EncodeToString(digest.Sum(nil))
}
