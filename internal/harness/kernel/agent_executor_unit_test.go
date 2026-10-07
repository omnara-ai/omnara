package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/compaction"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/sigv4"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func unitKernelID(seed string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("omnara-kernel-unit:"+seed))
}

type kernelSkillStoreStub struct{}

func (*kernelSkillStoreStub) GetSkillForDispatch(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (skillstore.SkillRecord, error) {
	return skillstore.SkillRecord{}, nil
}

func TestAgentExecutorDependencyComposition(t *testing.T) {
	aggregate := storage.NewStore(nil)
	explicit := &kernelSkillStoreStub{}
	sigV4CredentialCache, err := sigv4.NewCredentialCache()
	if err != nil {
		t.Fatalf("create SigV4 credential cache: %v", err)
	}

	defaults := (AgentExecutor{Store: aggregate}).contextBuilder()
	if defaults.Store == nil || defaults.Skills != aggregate.Skills() {
		t.Fatalf("default context dependencies = %+v, want aggregate capabilities", defaults)
	}
	overridden := (AgentExecutor{
		Store:          aggregate,
		ContextBuilder: modelcontext.Builder{Skills: explicit},
	}).contextBuilder()
	if overridden.Skills != explicit {
		t.Fatalf("context skill store = %T, want explicit override", overridden.Skills)
	}
	if empty := (AgentExecutor{}).contextBuilder(); empty.Store != nil || empty.Skills != nil {
		t.Fatalf("empty context dependencies = %+v, want unresolved dependencies", empty)
	}

	toolDefaults := (AgentExecutor{
		Store:                aggregate,
		SigV4CredentialCache: sigV4CredentialCache,
	}).configuredToolExecutor()
	if toolDefaults.Store != aggregate || toolDefaults.Skills != nil ||
		toolDefaults.SigV4CredentialCache != sigV4CredentialCache {
		t.Fatalf("default tool dependencies = %+v, want aggregate store with deferred skill resolution", toolDefaults)
	}
	toolOverride := (AgentExecutor{
		Store:        aggregate,
		ToolExecutor: tools.Executor{Skills: explicit},
	}).configuredToolExecutor()
	if toolOverride.Store != aggregate || toolOverride.Skills != explicit {
		t.Fatalf("overridden tool dependencies = %+v, want explicit skill store", toolOverride)
	}
}

func TestProviderInputFailureTriggerRoutesOnlyDeterministicInputFailures(t *testing.T) {
	tests := []struct {
		name string
		kind model.ErrorKind
		want bool
	}{
		{name: "context window", kind: model.ErrorKindContextWindow, want: true},
		{name: "payload too large", kind: model.ErrorKindPayloadTooLarge, want: true},
		{name: "rate limit", kind: model.ErrorKindRateLimit},
		{name: "transient", kind: model.ErrorKindTransient},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := model.ProviderError{
				Kind:      test.kind,
				Code:      "provider_code",
				Message:   "provider message",
				RequestID: "request-id",
			}
			trigger, got := providerInputFailureTrigger(cause)
			if got != test.want {
				t.Fatalf("maintenance trigger = %+v ok=%t, want ok=%t", trigger, got, test.want)
			}
			if !test.want {
				return
			}
			var retained model.ProviderError
			if trigger.Kind != test.kind || trigger.Code != cause.Code ||
				trigger.Message != cause.Message || trigger.RequestID != cause.RequestID ||
				!errors.As(trigger.Cause, &retained) || retained.Kind != test.kind {
				t.Fatalf("maintenance trigger = %+v, want retained %q evidence", trigger, test.kind)
			}
		})
	}
}

func TestRetainFromForRecentEventsUsesApproximateTokenTail(t *testing.T) {
	events := []executionstore.CompactionSourceEventRecord{
		{Sequence: 10, ContentParts: json.RawMessage(`[{"type":"text","text":"old compactable history"}]`)},
		{Sequence: 11, ContentParts: json.RawMessage(`[{"type":"text","text":"middle compactable history"}]`)},
		{Sequence: 12, ContentParts: json.RawMessage(`[{"type":"text","text":"recent tail one"}]`)},
		{Sequence: 13, ContentParts: json.RawMessage(`[{"type":"text","text":"recent tail two"}]`)},
	}

	lastEventTokens := compaction.EstimateSourceEventTokens(events[3])
	if got := retainFromForRecentEvents(events, lastEventTokens); got != 13 {
		t.Fatalf("retain from = %d, want only newest event 13", got)
	}
	tailTokens := compaction.EstimateSourceEventTokens(events[2]) + compaction.EstimateSourceEventTokens(events[3])
	if got := retainFromForRecentEvents(events, tailTokens); got != 12 {
		t.Fatalf("retain from = %d, want two-event tail starting at 12", got)
	}
	if got := retainFromForRecentEvents(events, tailTokens+compaction.EstimateSourceEventTokens(events[1])); got != 11 {
		t.Fatalf("retain from = %d, want expanded tail starting at 11", got)
	}
}

func TestRetainFromForRecentEventsCountsToolResults(t *testing.T) {
	events := []executionstore.CompactionSourceEventRecord{
		{
			Sequence:     10,
			Kind:         "agent_input",
			ContentParts: json.RawMessage(`[{"type":"text","text":"old compactable history"}]`),
		},
		{
			Sequence:     11,
			Kind:         "model_output",
			ContentParts: json.RawMessage(`[{"type":"tool_call","name":"run_command","input":{"command":"cat big.log"}}]`),
		},
		{
			Sequence: 12,
			Kind:     "tool_result",
			ContentParts: json.RawMessage(
				`[{"type":"text","text":"large tool output that must count toward the raw tail budget"}]`,
			),
		},
		{Sequence: 13, Kind: "agent_input", ContentParts: json.RawMessage(`[{"type":"text","text":"follow up"}]`)},
	}
	keep := compaction.EstimateSourceEventTokens(events[2]) + compaction.EstimateSourceEventTokens(events[3])
	if got := retainFromForRecentEvents(events, keep); got != 12 {
		t.Fatalf("retain from = %d, want tool result included in recent tail", got)
	}
}

func TestFirstFittingRetainFromClampsPreferredRawTail(t *testing.T) {
	var checked []int64
	got, ok, err := firstFittingRetainFrom(
		[]int64{10, 20, 30, 40, 50},
		20,
		func(retainFrom int64) (bool, error) {
			checked = append(checked, retainFrom)
			return retainFrom >= 40, nil
		},
	)
	if err != nil || !ok || got != 40 {
		t.Fatalf("first fitting retain boundary = %d ok=%t err=%v, want 40/true/nil", got, ok, err)
	}
	for _, candidate := range checked {
		if candidate < 20 {
			t.Fatalf("checked candidate %d before desired boundary 20", candidate)
		}
	}

	got, ok, err = firstFittingRetainFrom([]int64{10, 20, 30}, 20, func(int64) (bool, error) {
		return false, nil
	})
	if err != nil || ok || got != 0 {
		t.Fatalf("irreducible retain boundary = %d ok=%t err=%v, want 0/false/nil", got, ok, err)
	}
}

func TestMCPInitializationRetryableFailureClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "service unavailable", err: &mcp.HTTPError{Status: http.StatusServiceUnavailable}, want: true},
		{name: "too many requests", err: &mcp.HTTPError{Status: http.StatusTooManyRequests}, want: true},
		{name: "incomplete stream", err: mcp.ErrIncompleteStream, want: true},
		{name: "deadline", err: context.DeadlineExceeded, want: true},
		{name: "unauthorized", err: &mcp.HTTPError{Status: http.StatusUnauthorized}, want: false},
		{name: "forbidden", err: &mcp.HTTPError{Status: http.StatusForbidden}, want: false},
		{name: "ssrf", err: ssrf.ErrBlockedAddress, want: false},
		{name: "redirect", err: outboundhttp.ErrRedirect, want: false},
		{name: "unsupported response", err: mcp.ErrUnsupportedResponse, want: false},
		{name: "oversized response", err: mcp.ErrResponseTooLarge, want: false},
		{name: "plain error", err: errors.New("plain failure"), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcp.IsRetryableConnectionFailure(tc.err); got != tc.want {
				t.Fatalf("retryable = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestAgentExecutorUncommittedErrorDoesNotNotify(t *testing.T) {
	input := ModelWorkExecution{
		OrgID: uuid.New(), ProjectID: uuid.New(), AgentID: uuid.New(), TurnID: uuid.New(),
		RuntimeLockID: uuid.New(), InputIDs: []uuid.UUID{uuid.New()}, OpeningEventSequence: 1,
		Kind: executionstore.ModelWorkStart,
	}
	executor := AgentExecutor{
		Store: storage.NewStore(nil),
		OnModelFailure: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
			t.Fatal("an uncommitted configuration failure must not send a terminal notice")
			return nil
		},
	}
	if err := executor.ExecuteModelWork(t.Context(), input); err == nil {
		t.Fatal("missing resolver must return an error")
	}
}

func TestAgentExecutorFailureCallbackIsOptionalAndBounded(t *testing.T) {
	input := ModelWorkExecution{ProjectID: uuid.New(), AgentID: uuid.New(), RuntimeLockID: uuid.New()}
	(AgentExecutor{}).notifyModelFailure(t.Context(), input)
	calls := 0
	executor := AgentExecutor{OnModelFailure: func(ctx context.Context, project, agent, runtime uuid.UUID) error {
		calls++
		if project != input.ProjectID || agent != input.AgentID || runtime != input.RuntimeLockID {
			t.Fatal("failure callback lost runtime authority")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 10*time.Second {
			t.Fatal("failure callback must have a ten-second budget")
		}
		return nil
	}}
	executor.notifyModelFailure(t.Context(), input)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	executor.notifyModelFailure(ctx, input)
	if calls != 1 {
		t.Fatalf("failure callback calls = %d, want one live callback", calls)
	}
}

func TestInvalidModelResponse(t *testing.T) {
	call := model.ToolCall{ID: "call_valid", Name: "run_command", Input: json.RawMessage(`{"command":"true"}`)}
	tests := []struct {
		name      string
		reason    model.StopReason
		calls     []model.ToolCall
		kind      model.ErrorKind
		code      string
		ambiguous bool
	}{
		{name: "text completion", reason: model.StopReasonEndTurn},
		{name: "tools with completion", reason: model.StopReasonEndTurn, calls: []model.ToolCall{call}},
		{name: "text cutoff", reason: model.StopReasonMaxTokens},
		{name: "tools with cutoff", reason: model.StopReasonMaxTokens, calls: []model.ToolCall{call}},
		{name: "tools", reason: model.StopReasonToolUse, calls: []model.ToolCall{call}},
		{
			name: "missing tools", reason: model.StopReasonToolUse,
			kind: model.ErrorKindUnknown, code: "tool_use", ambiguous: true,
		},
		{name: "refusal", reason: model.StopReasonRefusal},
		{name: "filtered", reason: model.StopReasonContentFilter},
		{
			name: "refusal with tools", reason: model.StopReasonRefusal, calls: []model.ToolCall{call},
			kind: model.ErrorKindUnknown, code: "contradictory_stop_reason", ambiguous: true,
		},
		{
			name: "filtered with tools", reason: model.StopReasonContentFilter, calls: []model.ToolCall{call},
			kind: model.ErrorKindUnknown, code: "contradictory_stop_reason", ambiguous: true,
		},
		{
			name: "context overflow", reason: model.StopReasonContextWindow, calls: []model.ToolCall{call},
			kind: model.ErrorKindContextWindow, code: "context_window",
		},
		{name: "pause", reason: model.StopReasonPause, kind: model.ErrorKindInvalidRequest, code: "pause"},
		{name: "unknown", reason: model.StopReasonUnknown, kind: model.ErrorKindUnknown, code: "unknown", ambiguous: true},
		{name: "error", reason: model.StopReasonError, kind: model.ErrorKindUnknown, code: "error", ambiguous: true},
		{
			name: "duplicate IDs precede stop reason", reason: model.StopReasonContextWindow,
			calls: []model.ToolCall{call, call},
			kind:  model.ErrorKindUnknown, code: "malformed_tool_call", ambiguous: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := invalidModelResponse("test-provider", tc.reason, tc.calls)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("acceptable response rejected: %v", err)
				}
				return
			}
			providerErr, classified := model.ClassifyError(err)
			if !classified || providerErr.Kind != tc.kind || providerErr.Code != tc.code ||
				providerErr.Source != "test-provider" || model.IsAmbiguousProviderOutcome(err) != tc.ambiguous {
				t.Fatalf("response error = %+v, want kind=%s code=%s ambiguous=%t", err, tc.kind, tc.code, tc.ambiguous)
			}
		})
	}
}

func TestToolSpecDerivedSets(t *testing.T) {
	specs := []modelcontext.ToolSpec{
		{
			Name:       "run_command",
			Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
		},
		{Name: "lookup_customer"},
	}
	if byName := toolSpecSet(
		specs,
	); byName["run_command"].Name != "run_command" || byName["lookup_customer"].Name != "lookup_customer" {
		t.Fatalf("tool spec set = %+v", byName)
	}
	executable := executableToolSet(specs)
	if executable["run_command"].Permission.Mode != toolpermission.ModeAlwaysAllow ||
		len(executable) != 2 {
		t.Fatalf("executable tool set = %+v", executable)
	}
}

func TestToToolTurnAndExecutorNow(t *testing.T) {
	now := time.Date(2026, 6, 5, 9, 30, 0, 0, time.FixedZone("test", -7*60*60))
	input := ModelWorkExecution{
		OrgID:                unitKernelID("org"),
		ProjectID:            unitKernelID("project"),
		AgentID:              unitKernelID("agent"),
		TurnID:               unitKernelID("turn"),
		InputIDs:             []uuid.UUID{unitKernelID("input")},
		OpeningEventSequence: 12,
		RuntimeLockID:        unitKernelID("runtime-lock"),
		Now:                  now,
	}
	turn := toToolTurn(input)
	if turn.OrgID != input.OrgID ||
		turn.ProjectID != input.ProjectID ||
		turn.AgentID != input.AgentID ||
		turn.RuntimeLockID != input.RuntimeLockID {
		t.Fatalf("tool turn = %+v, want fields copied from %+v", turn, input)
	}
	gotNow := (AgentExecutor{Now: func() time.Time { return now }}).now()
	if gotNow.Location() != time.UTC || !gotNow.Equal(now.UTC()) {
		t.Fatalf("executor now = %s (%s), want UTC %s", gotNow, gotNow.Location(), now.UTC())
	}
}
