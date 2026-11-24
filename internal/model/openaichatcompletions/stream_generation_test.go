package openaichatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

type stopSignalSink chan struct{}

func (s stopSignalSink) Emit(_ context.Context, event model.StreamEvent) {
	if event.Kind == model.StreamEventMessageStop {
		close(s)
	}
}

func consumeStreamWithGeneration(
	t *testing.T,
	variant modelprotocol.APIVariant,
	generationID string,
	save model.SaveGenerationIDFunc,
	stream string,
	sink model.StreamSink,
) (model.Response, map[string]any, error) {
	t.Helper()
	var logs bytes.Buffer
	base := logpkg.WithLogger(context.Background(), slog.New(logpkg.NewJSONHandler(&logs, nil)))
	event := logpkg.NewEvent(base, "model.call")
	ctx := model.WithSaveGenerationID(logpkg.WithEvent(base, event), save)
	header := http.Header{}
	if generationID != "" {
		header.Set("X-Generation-Id", generationID)
	}
	p := protocol{client: Client{EndpointPath: testEndpointPath, ProviderModelSlug: "openai/gpt-5", APIVariant: variant}}
	response, err := p.ConsumeStream(ctx, strings.NewReader(stream), http.StatusOK, header, sink)
	event.Done(context.Background())
	var fields map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &fields))
	return response, fields, err
}

func TestConsumeStreamRecordsOpenRouterGenerationBeforeReturning(t *testing.T) {
	stream := chatCompletionsSSE(
		`{"id":"gen-1","model":"openai/gpt-5","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
		"[DONE]",
	)
	var saved []string
	stopped := make(stopSignalSink)
	save := func(_ context.Context, id string) error {
		<-stopped
		saved = append(saved, id)
		return nil
	}
	response, fields, err := consumeStreamWithGeneration(
		t, modelprotocol.APIVariantOpenRouter, "gen-1", save, stream, stopped,
	)
	require.NoError(t, err)
	require.Equal(t, "gen-1", response.ID)
	require.Equal(t, []string{"gen-1"}, saved, "the stream waits for the generation ID to be saved")
	require.NotContains(t, fields, "model_response.generation_id.header")

	saved = nil
	_, _, err = consumeStreamWithGeneration(
		t, modelprotocol.APIVariantDefault, "gen-1", save, stream, make(stopSignalSink),
	)
	require.NoError(t, err)
	require.Empty(t, saved, "only OpenRouter generation IDs are saved")
}

func TestConsumeStreamCarriesHeaderGenerationIntoFailures(t *testing.T) {
	failing := func(context.Context, string) error { return errors.New("database unavailable") }
	response, fields, err := consumeStreamWithGeneration(
		t, modelprotocol.APIVariantOpenRouter, "gen-1", failing, "", &chatRecordingSink{},
	)
	require.True(t, model.IsAmbiguousProviderOutcome(err))
	require.Equal(t, "gen-1", response.ID, "a stream cut before any chunk keeps the header generation")
	require.Equal(t, "database unavailable", fields["model_response.generation_id.save_error"])

	stream := chatCompletionsSSE(`{"id":"gen-chunk","choices":[{"index":0,"delta":{"content":"partial"}}]}`)
	none := func(context.Context, string) error { return nil }
	response, fields, _ = consumeStreamWithGeneration(
		t, modelprotocol.APIVariantOpenRouter, "gen-1", none, stream, &chatRecordingSink{},
	)
	require.Equal(t, "gen-chunk", response.ID)
	require.Equal(t, "gen-1", fields["model_response.generation_id.header"])
	require.Equal(t, "gen-chunk", fields["model_response.generation_id.chunk"])

	_, fields, _ = consumeStreamWithGeneration(
		t, modelprotocol.APIVariantOpenRouter, "", none, stream, &chatRecordingSink{},
	)
	require.Equal(t, "header_missing", fields["model_response.generation_id.unavailable_reason"])
}
