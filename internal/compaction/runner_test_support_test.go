package compaction

import (
	"context"
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	agentevents "github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	modelresolvertest "github.com/omnara-ai/omnara/internal/testutil/modelresolver"
)

type fakeStore struct {
	events                     []executionstore.CompactionSourceEventRecord
	atomicGroups               []executionstore.CompactionAtomicGroupRecord
	priorCheckpoint            *executionstore.ContextCheckpointRecord
	agentConfig                executionstore.AgentConfigRecord
	consecutiveCheckpointCount int

	claimInputs          []executionstore.ClaimCompactionModelCallInput
	claimResults         []executionstore.ModelCallClaim
	nextClaimInputs      []executionstore.ClaimNextModelCallContextInput
	nextClaimResults     []executionstore.ModelCallClaim
	claims               []executionstore.ModelCallClaim
	retryFailures        []executionstore.RecordRecoverableModelCallFailureInput
	terminalFailures     []executionstore.RecordTerminalCompactionFailureInput
	replacements         []executionstore.ReplaceCompactionSourceInput
	replacementPreempted bool
	publishInputs        []executionstore.PublishContextCheckpointInput
	publishErrs          []error
	published            *executionstore.ContextCheckpointRecord
	clock                func() time.Time
}

func (s *fakeStore) ListCompactionSourceEvents(
	_ context.Context,
	_, _ storage.ID,
	afterSequence int64,
	limit int32,
) ([]executionstore.CompactionSourceEventRecord, error) {
	out := make([]executionstore.CompactionSourceEventRecord, 0)
	for _, event := range s.events {
		if event.Sequence <= afterSequence {
			continue
		}
		out = append(out, event)
		if int32(len(out)) == limit {
			break
		}
	}
	return out, nil
}

func (s *fakeStore) ListCompactionAtomicGroups(
	_ context.Context,
	_, _ storage.ID,
	_, _ int64,
) ([]executionstore.CompactionAtomicGroupRecord, error) {
	return append([]executionstore.CompactionAtomicGroupRecord(nil), s.atomicGroups...), nil
}

func (s *fakeStore) CaptureAgentConfigForEventWatermark(
	_ context.Context,
	projectID, _ storage.ID,
	watermark int64,
) (executionstore.AgentConfigSnapshotRecord, error) {
	config := s.agentConfig
	if config.ID == storage.NilID {
		config = defaultCompactionAgentConfig()
	}
	config.ProjectID = projectID
	return executionstore.AgentConfigSnapshotRecord{
		AgentConfig:        config,
		InputEventSequence: watermark,
	}, nil
}

func (s *fakeStore) GetLatestApplicableContextCheckpoint(
	_ context.Context,
	_, _ storage.ID,
	maxEventSequence int64,
) (executionstore.ContextCheckpointRecord, bool, error) {
	if s.priorCheckpoint == nil || s.priorCheckpoint.CheckpointEventSequence > maxEventSequence {
		return executionstore.ContextCheckpointRecord{}, false, nil
	}
	return *s.priorCheckpoint, true, nil
}

func (s *fakeStore) GetContextCheckpointByProducerContext(
	_ context.Context,
	_, _, modelCallContextID storage.ID,
) (executionstore.ContextCheckpointRecord, bool, error) {
	if s.published != nil && s.published.ProducerModelCallContextID == modelCallContextID {
		return *s.published, true, nil
	}
	return executionstore.ContextCheckpointRecord{}, false, nil
}

func (s *fakeStore) CountConsecutiveContextCheckpointLineage(
	_ context.Context,
	_, _ storage.ID,
	_ int64,
) (int, error) {
	return s.consecutiveCheckpointCount, nil
}

func (s *fakeStore) GetProviderReplaySuppressionCutoff(
	context.Context,
	storage.ID,
	storage.ID,
	storage.ID,
) (int64, error) {
	return 0, nil
}

func (s *fakeStore) ClaimCompactionModelCall(
	_ context.Context,
	input executionstore.ClaimCompactionModelCallInput,
) (executionstore.ModelCallClaim, error) {
	s.claimInputs = append(s.claimInputs, input)
	if len(s.claimResults) > 0 {
		result := s.claimResults[0]
		s.claimResults = s.claimResults[1:]
		s.claims = append(s.claims, result)
		return result, nil
	}
	index := len(s.claimInputs)
	claim := newCompactionClaim(input, index, s.now())
	s.claims = append(s.claims, claim)
	return claim, nil
}

func newCompactionClaim(
	input executionstore.ClaimCompactionModelCallInput,
	index int,
	now time.Time,
) executionstore.ModelCallClaim {
	end := input.SourceEventSequenceEnd
	return executionstore.ModelCallClaim{
		Created: true,
		Claimed: true,
		Context: executionstore.ModelCallContextRecord{
			ID:                        testIDN(100 + index),
			OrgID:                     testIDN(500),
			ProjectID:                 input.ProjectID,
			AgentID:                   input.AgentID,
			OperationKind:             executionstore.ModelCallOperationCompaction,
			AttemptNumber:             1,
			AgentConfigID:             testIDN(501),
			ConfiguredModelRevisionID: testIDN(601),
			InputEventSequence:        input.InputEventSequence,
			SourceEventSequenceEnd:    &end,
			RuntimeLockID:             input.RuntimeLockID,
			State:                     executionstore.ModelCallContextStarted,
			CreatedAt:                 now,
		},
	}
}

func (s *fakeStore) ClaimNextModelCallContext(
	_ context.Context,
	input executionstore.ClaimNextModelCallContextInput,
) (executionstore.ModelCallClaim, error) {
	s.nextClaimInputs = append(s.nextClaimInputs, input)
	if len(s.nextClaimResults) > 0 {
		result := s.nextClaimResults[0]
		s.nextClaimResults = s.nextClaimResults[1:]
		s.claims = append(s.claims, result)
		return result, nil
	}
	predecessor, found := s.contextByID(input.PredecessorModelCallContextID)
	if !found || predecessor.RecoveryKind != executionstore.ModelCallRecoveryRetry ||
		predecessor.RetryAt == nil || predecessor.RetryAt.After(s.now()) {
		return executionstore.ModelCallClaim{Context: predecessor}, nil
	}
	next := predecessor
	next.ID = testIDN(300 + len(s.nextClaimInputs))
	next.AttemptNumber++
	next.RuntimeLockID = input.RuntimeLockID
	next.State = executionstore.ModelCallContextStarted
	next.RecoveryKind = ""
	next.APIFormat = ""
	next.APIVariant = ""
	next.ProviderRequestID = ""
	next.ProviderResponseID = ""
	next.ErrorKind = ""
	next.ErrorCode = ""
	next.ErrorMessage = ""
	next.ErrorDetails = nil
	next.RetryAt = nil
	next.Usage = modelenvelope.Usage{}
	next.CreatedAt = s.now()
	next.CompletedAt = nil
	claim := executionstore.ModelCallClaim{Context: next, Created: true, Claimed: true}
	s.claims = append(s.claims, claim)
	return claim, nil
}

func (s *fakeStore) RecordRetryableModelCallFailure(
	_ context.Context,
	input executionstore.RecordRecoverableModelCallFailureInput,
) (executionstore.ModelCallContextRecord, error) {
	s.retryFailures = append(s.retryFailures, input)
	contextRecord, found := s.contextByID(input.ModelCallContextID)
	if !found {
		return executionstore.ModelCallContextRecord{}, storeerr.ErrStateTransitionConflict
	}
	retryAt := s.now().Add(input.RetryDelay)
	contextRecord.State = executionstore.ModelCallContextFailed
	contextRecord.RecoveryKind = executionstore.ModelCallRecoveryRetry
	contextRecord.ErrorKind = input.ErrorKind
	contextRecord.ErrorCode = input.ErrorCode
	contextRecord.ErrorMessage = input.ErrorMessage
	contextRecord.RetryAt = &retryAt
	return contextRecord, nil
}

func (s *fakeStore) RecordTerminalCompactionFailure(
	_ context.Context,
	input executionstore.RecordTerminalCompactionFailureInput,
) error {
	s.terminalFailures = append(s.terminalFailures, input)
	return nil
}

func (s *fakeStore) ReplaceCompactionSource(
	_ context.Context,
	input executionstore.ReplaceCompactionSourceInput,
) (executionstore.ReplaceCompactionSourceResult, error) {
	s.replacements = append(s.replacements, input)
	if s.replacementPreempted {
		return executionstore.ReplaceCompactionSourceResult{BoundaryPreempted: true}, nil
	}
	end := input.NextSourceEventSequenceEnd
	claim := executionstore.ModelCallClaim{
		Context: executionstore.ModelCallContextRecord{
			ID:                        testIDN(700 + len(s.replacements)),
			OrgID:                     testIDN(500),
			ProjectID:                 input.ProjectID,
			AgentID:                   input.AgentID,
			OperationKind:             executionstore.ModelCallOperationCompaction,
			AttemptNumber:             1,
			AgentConfigID:             testIDN(501),
			ConfiguredModelRevisionID: testIDN(601),
			InputEventSequence:        s.claims[0].Context.InputEventSequence,
			SourceEventSequenceEnd:    &end,
			RuntimeLockID:             input.RuntimeLockID,
			State:                     executionstore.ModelCallContextStarted,
			CreatedAt:                 s.now(),
		},
		Created: true,
		Claimed: true,
	}
	s.claims = append(s.claims, claim)
	return executionstore.ReplaceCompactionSourceResult{CompactionCall: claim}, nil
}

type fakeContextBuilder struct {
	inputs []modelcontext.BuildInput
}

type errorResolver struct {
	err error
}

func (r errorResolver) Resolve(context.Context, model.Selection) (model.ResolvedClient, error) {
	return model.ResolvedClient{}, r.err
}

func (b *fakeContextBuilder) Build(
	_ context.Context,
	input modelcontext.BuildInput,
) (modelcontext.Bundle, error) {
	b.inputs = append(b.inputs, input)
	return modelcontext.Bundle{ContextCheckpoint: input.CheckpointOverride}, nil
}

type fakeProgressiveCheckpointPolicy struct {
	inputs   []ProgressiveCheckpointInput
	decision ProgressiveCheckpointDecision
	err      error
}

func (p *fakeProgressiveCheckpointPolicy) Evaluate(
	input ProgressiveCheckpointInput,
) (ProgressiveCheckpointDecision, error) {
	p.inputs = append(p.inputs, input)
	return p.decision, p.err
}

func (s *fakeStore) PublishContextCheckpoint(
	_ context.Context,
	input executionstore.PublishContextCheckpointInput,
) (executionstore.ContextCheckpointRecord, error) {
	s.publishInputs = append(s.publishInputs, input)
	if len(s.publishErrs) > 0 {
		err := s.publishErrs[0]
		s.publishErrs = s.publishErrs[1:]
		if err != nil {
			return executionstore.ContextCheckpointRecord{}, err
		}
	}
	claim := s.claimInputs[len(s.claimInputs)-1]
	record := executionstore.ContextCheckpointRecord{
		ID:                             testIDN(900 + len(s.publishInputs)),
		ProjectID:                      input.ProjectID,
		AgentID:                        input.AgentID,
		SummarizedThroughEventSequence: claim.SourceEventSequenceEnd,
		ProducerModelCallContextID:     input.ModelCallContextID,
		CheckpointEventID:              testIDN(950 + len(s.publishInputs)),
		CheckpointEventSequence:        claim.InputEventSequence + 1,
		Summary:                        input.Summary,
		CreatedAt:                      s.now(),
	}
	s.published = &record
	return record, nil
}

func (s *fakeStore) contextByID(id storage.ID) (executionstore.ModelCallContextRecord, bool) {
	for index := len(s.claims) - 1; index >= 0; index-- {
		if s.claims[index].Context.ID == id {
			return s.claims[index].Context, true
		}
	}
	return executionstore.ModelCallContextRecord{}, false
}

func (s *fakeStore) now() time.Time {
	if s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

type summaryResult struct {
	response model.Response
	err      error
}

type summaryModel struct {
	providerModelSlug           string
	caps                        model.Capabilities
	preparedBundles             []modelcontext.Bundle
	preparedPolicies            []model.RequestPolicy
	preparedEstimates           []int
	checkpointPreparedEstimates []int
	prepareErrs                 []error
	requests                    []model.Request
	results                     []summaryResult
}

func (m *summaryModel) RequestedProviderModelSlug() string {
	if m.providerModelSlug != "" {
		return m.providerModelSlug
	}
	return "summary"
}

func (m *summaryModel) APIFormat() modelprotocol.APIFormat {
	return modelprotocol.APIFormatOpenAIResponses
}

func (m *summaryModel) ModelAPIVariant() modelprotocol.APIVariant {
	return modelprotocol.APIVariantDefault
}

func (m *summaryModel) Capabilities() model.Capabilities {
	caps := m.caps
	if caps.ContextWindowTokens == 0 {
		caps.ContextWindowTokens = 200_000
	}
	if caps.MaxOutputTokens == 0 {
		caps.MaxOutputTokens = 4_096
	}
	if caps.DefaultMaxOutputTokens == 0 {
		caps.DefaultMaxOutputTokens = 2_048
	}
	return caps
}

func (m *summaryModel) Prepare(
	_ context.Context,
	input model.PrepareInput,
) (model.PreparedRequest, error) {
	if len(m.prepareErrs) > 0 {
		err := m.prepareErrs[0]
		m.prepareErrs = m.prepareErrs[1:]
		if err != nil {
			return model.PreparedRequest{}, err
		}
	}
	m.preparedBundles = append(m.preparedBundles, input.Context)
	m.preparedPolicies = append(m.preparedPolicies, input.Policy)
	body, err := json.Marshal(map[string]any{"messages": input.Context.Messages})
	if err != nil {
		return model.PreparedRequest{}, err
	}
	estimate := modelcontext.EstimatePreparedRequest(body, nil)
	if input.Context.ContextCheckpoint != nil && len(m.checkpointPreparedEstimates) > 0 {
		estimate = m.checkpointPreparedEstimates[0]
		m.checkpointPreparedEstimates = m.checkpointPreparedEstimates[1:]
	} else if len(m.preparedEstimates) > 0 {
		estimate = m.preparedEstimates[0]
		m.preparedEstimates = m.preparedEstimates[1:]
	}
	return model.PreparedRequest{Body: body, InputTokenEstimate: estimate}, nil
}

func (m *summaryModel) Respond(_ context.Context, input model.Request) (model.Response, error) {
	m.requests = append(m.requests, input)
	if len(m.results) > 0 {
		result := m.results[0]
		m.results = m.results[1:]
		return result.response, result.err
	}
	return completeSummaryResponse(
		"## Goal\nContinue the current task.\n\n" +
			"## Next Steps\nProceed with the next verified action.",
	), nil
}

func defaultCompactionAgentConfig() executionstore.AgentConfigRecord {
	return compactionAgentConfigWithReasoning("")
}

func compactionAgentConfigWithReasoning(reasoningEffort string) executionstore.AgentConfigRecord {
	source := `
instruction: Continue the task.
model:
  provider_config: openai-prod
  name: summary
`
	if reasoningEffort != "" {
		source += "  reasoning:\n    effort: " + reasoningEffort + "\n"
	}
	compiled, err := agentconfig.Compile(
		agentconfig.SourceFormatYAML,
		[]byte(source),
		agentconfig.CompileOptions{
			ResolveModelSelection: func(_, _ string) (agentconfig.ResolvedModelSelection, error) {
				return agentconfig.ResolvedModelSelection{ConfiguredModelID: testIDN(600).String()}, nil
			},
		},
	)
	if err != nil {
		panic(err)
	}
	return executionstore.AgentConfigRecord{
		ID:                      testIDN(501),
		OrgID:                   testIDN(500),
		ConfiguredModelID:       testIDN(600),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	}
}

func compactionResolver(client model.Client) model.Resolver {
	return modelresolvertest.Static{Clients: []model.ResolvedClient{{
		Client:                    client,
		ConfiguredModelRevisionID: testIDN(601).String(),
	}}}
}

func testRunner(store *fakeStore, client model.Client, now ...func() time.Time) Runner {
	runner := Runner{
		Store:          store,
		Resolver:       compactionResolver(client),
		ContextBuilder: &fakeContextBuilder{},
	}
	if len(now) > 0 {
		runner.Now = now[0]
	}
	store.clock = runner.now
	return runner
}

func runInput(plan Plan) RunInput {
	return RunInput{
		Plan:                     plan,
		TurnID:                   testTurnID,
		OpeningInputIDs:          []storage.ID{testOpeningInputID},
		OpeningEventSequence:     plan.InputEventSequence,
		RuntimeLockID:            testRuntimeLockID,
		ParentModelCallContextID: testIDN(799),
	}
}

func compactionClaimInput(input RunInput, _ time.Time) executionstore.ClaimCompactionModelCallInput {
	return executionstore.ClaimCompactionModelCallInput{
		ProjectID:              input.Plan.ProjectID,
		AgentID:                input.Plan.AgentID,
		RuntimeLockID:          input.RuntimeLockID,
		InputEventSequence:     input.Plan.InputEventSequence,
		SourceEventSequenceEnd: input.Plan.EventSequenceEnd,
		ParentContextID:        input.ParentModelCallContextID,
	}
}

func testPlan(start, end, frontier int64) Plan {
	if frontier <= end {
		frontier = end + 1
	}
	return Plan{
		ProjectID:          testProjectID,
		AgentID:            testAgentID,
		InputEventSequence: frontier,
		EventSequenceStart: start,
		EventSequenceEnd:   end,
	}
}

func completeSummaryResponse(summary string) model.Response {
	return model.Response{
		ID:                "resp_complete",
		ProviderRequestID: "req_complete",
		StopReason:        model.StopReasonEndTurn,
		Content:           []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: summary}},
	}
}

func mustCompactionEvent(
	sequence int64,
	kind, inputKind string,
	contentParts json.RawMessage,
) executionstore.CompactionSourceEventRecord {
	if len(contentParts) == 0 {
		contentParts = json.RawMessage(`[]`)
	}
	return executionstore.CompactionSourceEventRecord{
		ID:           testIDN(300 + int(sequence)),
		Sequence:     sequence,
		Kind:         kind,
		InputKind:    inputKind,
		ContentParts: contentParts,
		CreatedAt:    time.Unix(sequence, 0).UTC(),
	}
}

func textCompactionEvent(sequence int64, text string) executionstore.CompactionSourceEventRecord {
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
	if err != nil {
		panic(err)
	}
	return mustCompactionEvent(sequence, string(agentevents.KindModelOutput), "content", content)
}
