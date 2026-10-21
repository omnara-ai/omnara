package mcpauthorization_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/mcpauthorization"
)

func TestRequestRoundTripsAndResolves(t *testing.T) {
	secretID := uuid.New()
	request, err := mcpauthorization.NewRequest(mcpauthorization.NewRequestInput{
		ServerKey:      "google_calendar",
		ToolName:       "create_event",
		MCPURL:         "https://calendar.example/mcp",
		SecretID:       secretID,
		SecretName:     "google-calendar-oauth",
		AgentConfigID:  uuid.New(),
		RequiredScopes: []string{"calendar.events", "calendar.events"},
		GrantedScopes:  []string{"calendar.read"},
	})
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mcpauthorization.ParseRequest(raw)
	if err != nil {
		t.Fatalf("ParseRequest() error = %v", err)
	}
	if parsed.SecretID == nil || *parsed.SecretID != secretID {
		t.Fatalf("SecretID = %v, want %s", parsed.SecretID, secretID)
	}
	if got := parsed.RequestedScopes(); !slices.Equal(got, []string{"calendar.read", "calendar.events"}) {
		t.Fatalf("RequestedScopes() = %v", got)
	}
	if got := parsed.MissingScopes([]string{"calendar.read"}); !slices.Equal(got, []string{"calendar.events"}) {
		t.Fatalf("MissingScopes() = %v", got)
	}

	granted, err := mcpauthorization.Granted(parsed, mcpauthorization.GrantResolution())
	if err != nil || !granted {
		t.Fatalf("Granted(grant) = %v, %v; want true", granted, err)
	}
	granted, err = mcpauthorization.Granted(parsed, interactionform.Resolution{
		Answers: []interactionform.Answer{{OptionIndices: []int{mcpauthorization.DeclineOptionIndex}}},
	})
	if err != nil || granted {
		t.Fatalf("Granted(decline) = %v, %v; want false", granted, err)
	}
}

func TestParseRequestRejectsTamperedForm(t *testing.T) {
	request, err := mcpauthorization.NewRequest(mcpauthorization.NewRequestInput{
		ServerKey:     "google_calendar",
		ToolName:      "create_event",
		MCPURL:        "https://calendar.example/mcp",
		SecretName:    "google-calendar-oauth",
		AgentConfigID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if request.SecretID != nil {
		t.Fatalf("SecretID = %v, want nil for an unconfigured server", request.SecretID)
	}
	request.Form.Questions[0].Options[mcpauthorization.DeclineOptionIndex].AllowsText = true
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mcpauthorization.ParseRequest(raw); err == nil {
		t.Fatal("ParseRequest() error = nil, want invalid form")
	}
}
