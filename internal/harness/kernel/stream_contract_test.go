package kernel

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"

	openapispec "github.com/omnara-ai/omnara/api/openapi"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestPublishedStreamDeltaFramesMatchOpenAPISchema(t *testing.T) {
	loader := openapi3.NewLoader()
	spec, err := loader.LoadFromData(openapispec.YAML)
	if err != nil {
		t.Fatalf("load openapi spec: %v", err)
	}
	schema, ok := spec.Components.Schemas["ModelOutputDelta"]
	if !ok || schema.Value == nil {
		t.Fatal("openapi spec is missing the ModelOutputDelta schema")
	}

	turnID, err := publicid.Encode(publicid.KindAgentTurn, uuid.New())
	if err != nil {
		t.Fatalf("encode turn id: %v", err)
	}
	modelCallContextID, err := publicid.Encode(publicid.KindModelCallContext, uuid.New())
	if err != nil {
		t.Fatalf("encode model call context id: %v", err)
	}
	publisher := &capturingStreamPublisher{}
	sink := &harnessStreamSink{
		publisher:          publisher,
		agentID:            uuid.New(),
		turnID:             turnID,
		modelCallContextID: modelCallContextID,
		toolCalls:          make(map[string]streamToolCall),
		events:             make(chan streamEnvelope, streamSinkBufferSize),
		done:               make(chan struct{}),
	}
	go sink.run(context.Background())

	ctx := context.Background()
	emitter := model.NewStreamEmitter(sink)
	emitter.BlockStart(ctx, 0, model.StreamBlock{Kind: model.StreamBlockText})
	emitter.TextDelta(ctx, 0, "hello")
	emitter.BlockStop(ctx, 0)
	emitter.BlockStart(ctx, 1, model.StreamBlock{Kind: model.StreamBlockThinking})
	emitter.ThinkingDelta(ctx, 1, "considering")
	emitter.BlockStop(ctx, 1)
	emitter.BlockStart(ctx, 2, model.StreamBlock{
		Kind:       model.StreamBlockToolUse,
		ToolCallID: "provider-call-1",
		ToolName:   "shell",
	})
	emitter.ToolArgsDelta(ctx, 2, `{"command":"ls"}`)
	emitter.BlockStop(ctx, 2)
	stopReasons := []model.StopReason{
		model.StopReasonEndTurn,
		model.StopReasonToolUse,
		model.StopReasonMaxTokens,
		model.StopReasonRefusal,
		model.StopReasonContentFilter,
		model.StopReasonPause,
		model.StopReasonContextWindow,
		model.StopReasonUnknown,
	}
	for _, reason := range stopReasons {
		emitter.MessageStop(ctx, reason, model.Usage{
			InputTokens:         100,
			UncachedInputTokens: 40,
			OutputTokens:        20,
			ReasoningTokens:     5,
			CacheReadTokens:     60,
			CacheWriteTokens:    10,
		})
	}
	emitter.MessageStop(ctx, model.StopReasonEndTurn, model.Usage{})
	emitter.Error(ctx, "provider stream failed")
	sink.Close()

	publisher.mu.Lock()
	payloads := append([]json.RawMessage(nil), publisher.payloads...)
	publisher.mu.Unlock()
	wantFrames := 9 + len(stopReasons) + 2
	if len(payloads) != wantFrames {
		t.Fatalf("published %d frames, want %d", len(payloads), wantFrames)
	}
	for _, payload := range payloads {
		var frame any
		if err := json.Unmarshal(payload, &frame); err != nil {
			t.Fatalf("decode published frame %s: %v", payload, err)
		}
		if err := schema.Value.VisitJSON(frame); err != nil {
			t.Fatalf("frame %s does not match ModelOutputDelta: %v", payload, err)
		}
	}
}
