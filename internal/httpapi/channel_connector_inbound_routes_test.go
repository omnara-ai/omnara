package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
)

func TestNormalizeChannelInteractionRequest(t *testing.T) {
	valid := openapi.ResolveChannelConnectorInteractionRequest{

		ExternalTenantId:   "tenant-1",
		ExternalAccountRef: "account-1",
		ProviderRef:        "thread-1",
		Actor: openapi.ChannelActor{
			Ref: "actor-1", DisplayName: "Actor",
			Metadata: json.RawMessage(`{"z":1,"a":"ok"}`),
		},
		Metadata: json.RawMessage(`{"request":"ok"}`),
	}
	normalized, metadata, err := normalizeChannelInteractionRequest(valid)
	if err != nil {
		t.Fatalf("normalize valid request: %v", err)
	}
	if string(normalized.Actor.Metadata) != `{"a":"ok","z":1}` || len(metadata) == 0 {
		t.Fatalf("normalized request = %+v, metadata = %s", normalized, metadata)
	}

	largeValue := `{"value":"` + strings.Repeat("a", 140*1024) + `"}`
	expandedNumber := json.RawMessage(`{"value":1e131071}`)
	for _, test := range []struct {
		name   string
		mutate func(*openapi.ResolveChannelConnectorInteractionRequest)
	}{
		{name: "provider ref empty", mutate: func(body *openapi.ResolveChannelConnectorInteractionRequest) {
			body.ProviderRef = " "
		}},
		{name: "provider ref too large", mutate: func(body *openapi.ResolveChannelConnectorInteractionRequest) {
			body.ProviderRef = strings.Repeat("a", 513)
		}},
		{name: "provider ref NUL", mutate: func(body *openapi.ResolveChannelConnectorInteractionRequest) {
			body.ProviderRef = "thread\x00ref"
		}},
		{name: "actor ref NUL", mutate: func(body *openapi.ResolveChannelConnectorInteractionRequest) {
			body.Actor.Ref = "actor\x00ref"
		}},
		{name: "actor display NUL", mutate: func(body *openapi.ResolveChannelConnectorInteractionRequest) {
			body.Actor.DisplayName = "actor\x00name"
		}},
		{name: "actor metadata NUL", mutate: func(body *openapi.ResolveChannelConnectorInteractionRequest) {
			body.Actor.Metadata = json.RawMessage(`{"value":"bad\u0000value"}`)
		}},
		{name: "request metadata NUL", mutate: func(body *openapi.ResolveChannelConnectorInteractionRequest) {
			body.Metadata = json.RawMessage(`{"value":"bad\u0000value"}`)
		}},
		{name: "aggregate metadata oversized", mutate: func(body *openapi.ResolveChannelConnectorInteractionRequest) {
			body.Actor.Metadata = json.RawMessage(largeValue)
			body.Metadata = json.RawMessage(largeValue)
		}},
		{name: "aggregate PostgreSQL text expansion", mutate: func(body *openapi.ResolveChannelConnectorInteractionRequest) {
			body.Actor.Metadata = expandedNumber
			body.Metadata = expandedNumber
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := valid
			test.mutate(&body)
			if _, _, err := normalizeChannelInteractionRequest(body); err == nil {
				t.Fatal("normalize unsafe request succeeded")
			}
		})
	}
}
