package modelcontext

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestBuiltContentReuseChecksBytesRoleAndDetachesHistory(t *testing.T) {
	store := &fakeContextStore{watermark: 1,
		messages: []executionstore.ContextEventRecord{contextTextEvent(t,
			testIDN(900),
			1,
			"hello")}}
	store.messages[0].Role = modelprotocol.RoleAssistant
	bundle, err := (Builder{Store: store}).Build(t.Context(),
		BuildInput{ProjectID: testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []uuid.UUID{testInputID},
			Now:             time.Now()})
	require.NoError(t, err)
	require.NotNil(t, bundle.Messages[0].validatedContent)
	first, err := CanonicalHistory(bundle)
	require.NoError(t, err)
	block, ok := first[0].AssistantContent[0].(AssistantBlockEntry)
	require.True(t, ok)
	block.Content[0] = '!'
	second, err := CanonicalHistory(bundle)
	require.NoError(t, err)
	block, ok = second[0].AssistantContent[0].(AssistantBlockEntry)
	require.True(t, ok)
	require.True(t, json.Valid(block.Content))
	copy(bundle.Messages[0].Content, bytes.ReplaceAll(bundle.Messages[0].Content, []byte("text"), []byte("oops")))
	_, err = CanonicalHistory(bundle)
	require.Error(t, err)
	bundle.Messages[0].Content = json.RawMessage(`[{"type":"reasoning","text":"reason"}]`)
	require.NoError(t, normalizeProjection(bundle, true))
	bundle.Messages[0].Role = modelprotocol.RoleUser
	_, err = CanonicalHistory(bundle)
	require.ErrorContains(t, err, "reasoning is only valid")
}

func TestProjectionNormalizerStillRejectsMalformedArrays(t *testing.T) {
	for _, raw := range []string{`[`, `[{}] trailing`, `[null]`, `null`, `{}`, `[{"type":"text","text":1}]`} {
		bundle := Bundle{ProjectID: testProjectID,
			AgentID:            testAgentID,
			TurnID:             testTurnID,
			OpeningInputIDs:    []uuid.UUID{testInputID},
			InputEventSequence: 1,
			Messages: []Message{{ID: "message",
				Role:     modelprotocol.RoleUser,
				Sequence: 1,
				Content:  json.RawMessage(raw)}}}
		require.Error(t, (ProjectionNormalizer{}).Normalize(bundle), raw)
	}
}
