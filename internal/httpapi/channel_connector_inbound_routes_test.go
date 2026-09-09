package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestChannelInboundCompletionRetryIsAlwaysOpaqueServiceUnavailable(t *testing.T) {
	for _, cause := range []error{storeerr.ErrNotFound, storeerr.ErrStateTransitionConflict} {
		t.Run(cause.Error(), func(t *testing.T) {
			err := channelInboundProcessError(
				context.Background(),
				errors.Join(integration.ErrChannelInboundCompletionRetry, cause),
			)
			var response apierror.ResponseError
			if !errors.As(err, &response) {
				t.Fatalf("mapped error = %T %v", err, err)
			}
			if response.Status != http.StatusServiceUnavailable ||
				response.Code != openapi.ErrorCodeServiceUnavailable {
				t.Fatalf("mapped retry error = %+v", response)
			}
			if strings.Contains(response.Message, cause.Error()) {
				t.Fatalf("retry response exposed internal cause: %q", response.Message)
			}
		})
	}
}

func TestChannelInboundUnavailableHandlerIsOpaqueServiceUnavailable(t *testing.T) {
	err := channelInboundProcessError(
		context.Background(),
		fmt.Errorf("%w: private_handler@9", integration.ErrChannelRouteHandlerUnavailable),
	)
	var response apierror.ResponseError
	if !errors.As(err, &response) {
		t.Fatalf("mapped error = %T %v", err, err)
	}
	if response.Status != http.StatusServiceUnavailable ||
		response.Code != openapi.ErrorCodeServiceUnavailable {
		t.Fatalf("mapped handler error = %+v", response)
	}
	if strings.Contains(response.Message, "private_handler") {
		t.Fatalf("handler response exposed internal key: %q", response.Message)
	}
}

func TestNormalizeChannelInteractionRequest(t *testing.T) {
	valid := openapi.ResolveChannelConnectorInteractionRequest{
		Version:            openapi.ResolveChannelConnectorInteractionRequestVersionV1,
		ExternalTenantId:   "tenant-1",
		ExternalAccountRef: "account-1",
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
