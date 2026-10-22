package model_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type workerCPUStore struct {
	modelcontext.Store
	records      []executionstore.ContextEventRecord
	integrations map[uuid.UUID]agentconfig.IntegrationResolution
	results      []executionstore.ToolCallRecord
	snapshot     executionstore.AgentConfigSnapshotRecord
	artifact     artifactstore.ArtifactRecord
}

func (s *workerCPUStore) CaptureAgentConfigForModelContext(
	context.Context, uuid.UUID, uuid.UUID,
) (executionstore.AgentConfigSnapshotRecord, error) {
	return s.snapshot, nil
}

func (s *workerCPUStore) GetLatestApplicableContextCheckpoint(
	context.Context, uuid.UUID, uuid.UUID, int64,
) (executionstore.ContextCheckpointRecord, bool, error) {
	return executionstore.ContextCheckpointRecord{}, false, nil
}

func (s *workerCPUStore) ListContextEvents(
	_ context.Context, _, _ uuid.UUID, after, watermark int64, limit int32,
) ([]executionstore.ContextEventRecord, error) {
	start := sort.Search(len(s.records), func(i int) bool { return s.records[i].Sequence > after })
	end := sort.Search(len(s.records), func(i int) bool { return s.records[i].Sequence > watermark })
	end = min(end, start+int(limit))
	return append([]executionstore.ContextEventRecord(nil), s.records[start:end]...), nil
}

func (s *workerCPUStore) ListCompletedToolCallsAtWatermark(
	_ context.Context, _, _ uuid.UUID, after, watermark int64,
) ([]executionstore.ToolCallRecord, error) {
	var out []executionstore.ToolCallRecord
	for _, r := range s.results {
		if r.SourceEventSequence > after && r.ToolResultEventSequence <= watermark {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *workerCPUStore) ResolveIntegrationDefinitions(
	context.Context, uuid.UUID, []uuid.UUID,
) (map[uuid.UUID]agentconfig.IntegrationResolution, error) {
	return s.integrations, nil
}

func (s *workerCPUStore) ListAgentMCPConnections(
	context.Context, uuid.UUID, uuid.UUID,
) ([]executionstore.MCPConnectionRecord, error) {
	return nil, nil
}

func (s *workerCPUStore) ListAgentArtifactsByIDs(
	context.Context, uuid.UUID, uuid.UUID, []uuid.UUID,
) ([]artifactstore.ArtifactRecord, error) {
	return []artifactstore.ArtifactRecord{s.artifact}, nil
}

func (s *workerCPUStore) GetArtifactBlob(
	context.Context, uuid.UUID, uuid.UUID, uuid.UUID,
) ([]byte, artifactstore.ArtifactRecord, error) {
	return []byte("A small synthetic reference document used in the conversation."), s.artifact, nil
}

type workerCPUTransport struct{ stream string }

func (t workerCPUTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Body.Close(); err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK,
		Header:  http.Header{"Content-Type": {"text/event-stream"}},
		Body:    io.NopCloser(strings.NewReader(t.stream)),
		Request: req}, nil
}

type workerCPUSink struct{ count int }

func (s *workerCPUSink) Emit(_ context.Context, _ model.StreamEvent) { s.count++ }

func workerCPUFixture(b testing.TB, size int) (*workerCPUStore, modelcontext.BuildInput, uuid.UUID) {
	b.Helper()
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML,
		[]byte(`instruction: Help the user investigate a software project.
model:
  provider_config: benchmark
  name: test-model
tools:
  run_command: {}
`),
		agentconfig.CompileOptions{})
	if err != nil {
		b.Fatal(err)
	}
	project, agent, turn := uuid.New(), uuid.New(), uuid.New()
	opening, configID, revision := uuid.New(), uuid.New(), uuid.New()
	store := &workerCPUStore{snapshot: executionstore.AgentConfigSnapshotRecord{
		AgentConfig: executionstore.AgentConfigRecord{ID: configID,
			CompiledDefinition:      compiled.CanonicalJSON,
			EffectiveDefinitionHash: compiled.Hash},
		InputEventSequence: int64(size * 2)}}
	length := int64(64)
	store.artifact = artifactstore.ArtifactRecord{ID: uuid.New(),
		ProjectID:   project,
		AgentID:     agent,
		ContentType: "text/plain",
		Filename:    "notes.txt",
		SizeBytes:   &length}
	for i := range size {
		event := executionstore.ContextEventRecord{ID: uuid.New(),
			ProjectID:    project,
			AgentID:      agent,
			Sequence:     int64(i*2 + 1),
			Role:         modelprotocol.RoleUser,
			AgentInputID: opening}
		blocks := []map[string]any{{"type": "text",
			"text": fmt.Sprintf("Message %d: inspect the changes, compare the observed results and explain the next step. %s",
				i,
				strings.Repeat("Synthetic transcript text. ", 4))}}
		if i%5 == 1 {
			event.Role = modelprotocol.RoleAssistant
			callID, contextID := uuid.New(), uuid.New()
			event.ModelCallContextID = contextID
			blocks = append(blocks, map[string]any{"type": "tool_call", "tool_call_id": callID.String()})
			store.results = append(store.results,
				executionstore.ToolCallRecord{ID: callID,
					ModelCallContextID:      contextID,
					ProviderCallID:          fmt.Sprintf("call_%d", i),
					Name:                    "run_command",
					Input:                   json.RawMessage(`{"command":"git diff --stat"}`),
					ToolCallResultID:        uuid.New(),
					ToolResultEventID:       uuid.New(),
					SourceEventSequence:     event.Sequence,
					ToolResultEventSequence: event.Sequence + 1,
					State:                   executionstore.ToolCallStateCompleted,
					Outcome:                 executionstore.ToolResultOutcomeSucceeded,
					ResultContentParts:      json.RawMessage(`[{"type":"text","text":"3 files changed, 24 insertions, 8 deletions"},{"type":"structured_data","value":{"exit_code":0}}]`)})
		} else if i%5 == 3 {
			event.Role = modelprotocol.RoleAssistant
		}
		if i%50 == 0 {
			blocks = append(blocks, map[string]any{"type": "media_ref", "artifact_id": store.artifact.ID.String()})
		}
		event.ContentParts, err = json.Marshal(blocks)
		if err != nil {
			b.Fatal(err)
		}
		store.records = append(store.records, event)
	}
	input := modelcontext.BuildInput{ProjectID: project,
		AgentID:                    agent,
		TurnID:                     turn,
		OpeningInputIDs:            []uuid.UUID{opening},
		Now:                        time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
		TextAttachmentBudgetTokens: 4096}
	return store, input, revision
}

func BenchmarkWorkerModelCall(b *testing.B) {
	for _, size := range []int{30, 500, 5000} {
		b.Run(fmt.Sprintf("messages_%d", size), func(b *testing.B) {
			capabilities := model.Capabilities{
				ContextWindowTokens: 2000000, InputModalities: []string{"text", "image", "file"},
			}
			for _, adapter := range adapterWireClients(wireClientConfig{capabilities: capabilities}) {
				b.Run(adapter.name, func(b *testing.B) {
					stream, err := os.ReadFile("testdata/worker_cpu/" + adapter.name + ".sse")
					if err != nil {
						b.Fatal(err)
					}
					clients := adapterWireClients(wireClientConfig{capabilities: adapter.client.Capabilities(),
						httpClient: &http.Client{Transport: workerCPUTransport{stream: string(stream)}}})
					var client model.Client
					for _, candidate := range clients {
						if candidate.name == adapter.name {
							client = candidate.client
						}
					}
					store, input, revision := workerCPUFixture(b, size)
					input.MediaProjector = model.MediaProjectorForClient(client)
					builder := modelcontext.Builder{Store: store}
					policy := model.RequestPolicy{MaxOutputTokens: 4096}
					source := &store.records[size-2]
					store.snapshot.InputEventSequence = source.Sequence - 1
					prefix, err := builder.Build(b.Context(), input)
					if err != nil {
						b.Fatal(err)
					}
					prefix.ConfiguredModelRevisionID = revision
					previous, err := client.Prepare(b.Context(), model.PrepareInput{Context: prefix, Policy: policy})
					if err != nil {
						b.Fatal(err)
					}
					source.InputEventSequence = store.snapshot.InputEventSequence
					source.InputFingerprint = previous.InputFingerprint
					source.AgentConfigID = store.snapshot.AgentConfig.ID
					source.ConfiguredModelRevisionID = revision
					source.RequestedModelSlug = client.RequestedProviderModelSlug()
					source.APIFormat = client.APIFormat()
					source.APIVariant = client.ModelAPIVariant()
					source.Usage = modelenvelope.Usage{InputTokens: previous.InputTokenEstimate, OutputTokens: 64}
					store.snapshot.InputEventSequence = int64(size * 2)
					sink := &workerCPUSink{}
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						bundle, err := builder.Build(b.Context(), input)
						if err != nil {
							b.Fatal(err)
						}
						bundle.ConfiguredModelRevisionID = revision
						prepared, err := model.PrepareForSend(b.Context(),
							client,
							model.PrepareForSendInput{Context: bundle,
								Policy: policy})
						if err != nil {
							b.Fatal(err)
						}
						if !prepared.UsesProviderInputUsage {
							b.Fatal("fixture must exercise prefix validity and suffix measurement")
						}
						response, err := client.Respond(b.Context(), model.Request{ProviderRequest: prepared.Body, DeltaSink: sink})
						if err != nil {
							b.Fatal(err)
						}
						if response.Text() != "Hello world" || len(response.ToolCalls()) != 1 {
							b.Fatalf("unexpected SSE response: %+v", response)
						}
					}
				})
			}
		})
	}
}
