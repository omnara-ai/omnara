package modelcontext

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestOutputContinuationProjectsHarnessFeedbackSeparately(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		hasFeedback   bool
		wantMessages  int
	}{
		{"partial text", `[{"type":"text","text":"partial"},{"type":"error","text":"use smaller calls"}]`, true, 2},
		{"only feedback", `[{"type":"error","text":"use smaller calls"}]`, true, 1},
		{
			"partial reasoning",
			`[{"type":"reasoning","text":"thinking"},{"type":"text","text":"partial"},{"type":"error","text":"use smaller calls"}]`,
			true,
			2,
		},
		{
			"historical output",
			`[{"type":"text","text":"partial"},{"type":"error","text":"historical error"}]`,
			false,
			1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages, err := contextEventsToMessages([]executionstore.ContextEventRecord{{
				ID: testIDN(9100), ModelCallContextID: testIDN(9101), Sequence: 42, Role: modelprotocol.RoleAssistant,
				ContentParts: json.RawMessage(tc.content), HasOutputLimitFeedback: tc.hasFeedback,
			}})
			require.NoError(t, err)
			if len(messages) != tc.wantMessages {
				t.Fatalf("messages=%+v", messages)
			}
			bundle := Bundle{
				ProjectID: testIDN(9102),
				AgentID:   testIDN(9103),
				TurnID:    testIDN(9104),
				OpeningInputIDs: []storage.ID{
					testIDN(9105),
				},
				InputEventSequence: 42,
				Messages:           messages,
			}
			if err := (ProjectionNormalizer{}).Normalize(bundle); err != nil {
				t.Fatalf("normalize projected feedback: %v", err)
			}
			if _, err := CanonicalHistory(bundle); err != nil {
				t.Fatalf("canonical history: %v", err)
			}
			last := messages[len(messages)-1]
			if tc.hasFeedback {
				if last.Role != modelprotocol.RoleUser ||
					last.ModelCallContextID != "" ||
					len(last.ProviderReplay) != 0 ||
					last.Sequence != 42 ||
					!strings.Contains(
						string(last.Content),
						`"type":"text"`,
					) {
					t.Fatalf("feedback=%+v", last)
				}
				if len(messages) == 2 &&
					(messages[0].ModelCallContextID != testIDN(9101).String() ||
						messages[0].Role != modelprotocol.RoleAssistant ||
						messages[0].ID == last.ID ||
						strings.Contains(
							string(messages[0].Content),
							"use smaller calls",
						)) {
					t.Fatalf("partial assistant=%+v", messages[0])
				}
			} else if last.Role != modelprotocol.RoleAssistant {
				t.Fatalf("historical role=%s", last.Role)
			}
		})
	}
}

func TestOutputContinuationRejectsMissingFeedback(t *testing.T) {
	_, err := contextEventsToMessages([]executionstore.ContextEventRecord{{
		ID: testIDN(9100), Sequence: 42, Role: modelprotocol.RoleAssistant, HasOutputLimitFeedback: true,
		ContentParts: json.RawMessage(`[{"type":"text","text":"partial"}]`),
	}})
	if err == nil || !strings.Contains(err.Error(), "missing harness feedback") {
		t.Fatalf("error=%v", err)
	}
}

func TestProjectedMessageOrderStillRejectsReversalAndDuplicateIdentity(t *testing.T) {
	base := Bundle{
		ProjectID: testIDN(9102),
		AgentID:   testIDN(9103),
		TurnID:    testIDN(9104),
		OpeningInputIDs: []storage.ID{
			testIDN(9105),
		},
		InputEventSequence: 42,
	}
	first := Message{
		ID:       "first",
		Sequence: 42,
		Role:     modelprotocol.RoleAssistant,
		Content:  json.RawMessage(`[{"type":"text","text":"partial"}]`),
	}
	second := Message{
		ID:       "feedback",
		Sequence: 42,
		Role:     modelprotocol.RoleUser,
		Content:  json.RawMessage(`[{"type":"text","text":"continue"}]`),
	}
	base.Messages = []Message{first, second}
	require.NoError(t, (ProjectionNormalizer{}).Normalize(base))
	base.Messages[1].Sequence = 41
	if err := (ProjectionNormalizer{}).Normalize(base); err == nil {
		t.Fatal("reversed events accepted")
	}
	base.Messages[1] = second
	base.Messages[1].ID = first.ID
	if err := (ProjectionNormalizer{}).Normalize(base); err == nil {
		t.Fatal("duplicate message identity accepted")
	}
}
