package toolpermission

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/interactionform"
)

func TestAlwaysAskResolutionUsesInteractionFormAnswer(t *testing.T) {
	request := interactionTestRequest(t)
	for _, test := range []struct {
		name         string
		response     interactionform.Resolution
		wantDecision Decision
		wantReason   string
		wantErr      bool
	}{
		{
			name: "allow",
			response: interactionform.Resolution{Answers: []interactionform.Answer{{
				OptionIndices: []int{AllowOptionIndex},
			}}},
			wantDecision: DecisionAllow,
		},
		{
			name: "deny with reason",
			response: interactionform.Resolution{Answers: []interactionform.Answer{{
				OptionIndices: []int{DenyOptionIndex},
				Text:          "not allowed",
			}}},
			wantDecision: DecisionDeny,
			wantReason:   "not allowed",
		},
		{
			name: "unknown option",
			response: interactionform.Resolution{Answers: []interactionform.Answer{{
				OptionIndices: []int{2},
			}}},
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolution, err := Resolve(request, test.response)
			if (err != nil) != test.wantErr {
				t.Fatalf("Resolve() error = %v, wantErr %v", err, test.wantErr)
			}
			if resolution.Decision != test.wantDecision || resolution.Reason != test.wantReason {
				t.Fatalf("Resolve() = %+v, want decision %q reason %q", resolution, test.wantDecision, test.wantReason)
			}
		})
	}
}

func TestParseRequestNormalizesAuthorizationInput(t *testing.T) {
	request := interactionTestRequest(t)
	request.Authorization.Input = json.RawMessage(`{"value":9007199254740993,"a":2}`)
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	parsed, err := ParseRequest(raw)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if got, want := string(parsed.Authorization.Input), `{"a":2,"value":9007199254740993}`; got != want {
		t.Fatalf("authorization input = %s, want %s", got, want)
	}
}

func interactionTestRequest(t *testing.T) Request {
	t.Helper()
	authorization, err := NewAuthorization(
		"test_tool",
		json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatalf("authorization: %v", err)
	}
	mode, ok := FindMode(CommonModeDescriptors(), ModeAlwaysAsk)
	if !ok {
		t.Fatal("always_ask mode missing")
	}
	value, err := NewAllowDenyForm("Permission requested", nil)
	if err != nil {
		t.Fatalf("interaction form: %v", err)
	}
	request, err := NewRequest(
		mode,
		DefaultSelection(ModeAlwaysAsk),
		authorization,
		value,
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return request
}
