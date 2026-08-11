package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/compaction"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func unitKernelID(seed string) storage.ID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("omnara-kernel-unit:"+seed))
}

type kernelSkillStoreStub struct{}

func (*kernelSkillStoreStub) GetSkillForDispatch(
	context.Context,
	storage.ID,
	string,
) (skillstore.SkillRecord, error) {
	return skillstore.SkillRecord{}, nil
}

func TestAgentExecutorDependencyComposition(t *testing.T) {
	aggregate := storage.NewStore(nil)
	explicit := &kernelSkillStoreStub{}
	sigV4CredentialCache := mcp.NewSigV4CredentialCache()

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

func TestShouldInitializeMCPConnectionPreservesRetryRecoveryWithoutRevivingFailures(t *testing.T) {
	for _, state := range []executionstore.MCPConnectionState{
		executionstore.MCPConnectionStateInitializing,
		executionstore.MCPConnectionStateFailed,
		executionstore.MCPConnectionStateExpired,
	} {
		if !shouldInitializeMCPConnection(mcpInitializationOpening, state) {
			t.Fatalf("opening mode excluded %q connection", state)
		}
	}
	if !shouldInitializeMCPConnection(mcpInitializationResume, executionstore.MCPConnectionStateInitializing) ||
		!shouldInitializeMCPConnection(mcpInitializationResume, executionstore.MCPConnectionStateExpired) {
		t.Fatal("resume mode excluded recoverable connection")
	}
	if shouldInitializeMCPConnection(mcpInitializationResume, executionstore.MCPConnectionStateFailed) {
		t.Fatal("resume mode revived failed connection")
	}
}

func TestShouldPostIntegrationRuntimeError(t *testing.T) {
	baseCtx := context.Background()
	canceledCtx, cancelCanceled := context.WithCancel(baseCtx)
	cancelCanceled()
	deadlineCtx, cancelDeadline := context.WithDeadline(baseCtx, time.Unix(1, 0))
	defer cancelDeadline()
	<-deadlineCtx.Done()

	tests := []struct {
		name    string
		ctxKind string
		err     error
		want    bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "wrapped turn canceled", ctxKind: "canceled", err: errors.New("transient: context canceled"), want: false},
		{name: "child deadline", err: context.DeadlineExceeded, want: true},
		{name: "turn deadline", ctxKind: "deadline", err: context.DeadlineExceeded, want: false},
		{
			name:    "wrapped turn deadline",
			ctxKind: "deadline",
			err:     errors.New("transient: context deadline exceeded"),
			want:    false,
		},
		{name: "unavailable model grant", err: storeerr.ErrModelGrantUnavailable, want: true},
		{name: "agent not advanceable", err: storeerr.ErrAgentNotAdvanceable, want: false},
		{name: "daemon runtime unregistered", err: storeerr.ErrDaemonRuntimeUnregistered, want: false},
		{name: "runtime lock inactive", err: storeerr.ErrRuntimeLockInactive, want: false},
		{name: "state transition conflict", err: storeerr.ErrStateTransitionConflict, want: false},
		{name: "runtime error", err: errors.New("configured model is not available"), want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := baseCtx
			switch tc.ctxKind {
			case "canceled":
				ctx = canceledCtx
			case "deadline":
				ctx = deadlineCtx
			}
			if got := shouldPostIntegrationRuntimeError(ctx, tc.err); got != tc.want {
				t.Fatalf("should post = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestShouldPostIntegrationRuntimeMessageAllowsUnavailableGrantAfterModelResponse(t *testing.T) {
	ctx := context.Background()
	if !shouldPostIntegrationRuntimeMessage(ctx, storeerr.ErrModelGrantUnavailable, true) {
		t.Fatal("unavailable model grant after a prior model response should still post a runtime message")
	}
	if shouldPostIntegrationRuntimeMessage(ctx, errors.New("boom"), true) {
		t.Fatal("generic runtime error after a prior model response should not post a duplicate runtime message")
	}
	if !shouldPostIntegrationRuntimeMessage(ctx, errors.New("boom"), false) {
		t.Fatal("generic runtime error before a model response should post a runtime message")
	}
}

func TestIntegrationRuntimeErrorMessage(t *testing.T) {
	unavailable := integrationRuntimeErrorMessage(storeerr.ErrModelGrantUnavailable)
	if !strings.Contains(unavailable, "does not have access to the configured model") {
		t.Fatalf("unavailable model grant message = %q", unavailable)
	}
	generic := integrationRuntimeErrorMessage(errors.New("boom"))
	if strings.Contains(generic, "configured model") {
		t.Fatalf("generic runtime message used unavailable-grant wording: %q", generic)
	}
}

func TestInvalidModelToolCallResponse(t *testing.T) {
	tests := []struct {
		name    string
		calls   []model.ToolCall
		want    bool
		code    string
		message string
	}{
		{
			name:    "missing id",
			calls:   []model.ToolCall{{Name: "run_command", Input: json.RawMessage(`{}`)}},
			want:    true,
			code:    "malformed_tool_call",
			message: "without an ID",
		},
		{
			name:    "missing name",
			calls:   []model.ToolCall{{ID: "call_missing_name", Input: json.RawMessage(`{}`)}},
			want:    true,
			code:    "malformed_tool_call",
			message: "without a name",
		},
		{
			name:    "empty input",
			calls:   []model.ToolCall{{ID: "call_empty_input", Name: "run_command"}},
			want:    true,
			code:    "malformed_tool_call",
			message: "JSON object",
		},
		{
			name:    "invalid json input",
			calls:   []model.ToolCall{{ID: "call_invalid_json", Name: "run_command", Input: json.RawMessage(`{`)}},
			want:    true,
			code:    "malformed_tool_call",
			message: "JSON object",
		},
		{
			name:    "nonobject input",
			calls:   []model.ToolCall{{ID: "call_array_input", Name: "run_command", Input: json.RawMessage(`[]`)}},
			want:    true,
			code:    "malformed_tool_call",
			message: "JSON object",
		},
		{
			name: "duplicate id",
			calls: []model.ToolCall{
				{ID: "call_duplicate", Name: "run_command", Input: json.RawMessage(`{"command":"one"}`)},
				{ID: "call_duplicate", Name: "run_command", Input: json.RawMessage(`{"command":"two"}`)},
			},
			want:    true,
			code:    "malformed_tool_call",
			message: "duplicate tool call ID",
		},
		{
			name:  "valid",
			calls: []model.ToolCall{{ID: "call_valid", Name: "run_command", Input: json.RawMessage(`{"command":"true"}`)}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := invalidModelToolCallResponse("test-provider", tc.calls)
			if ok != tc.want {
				t.Fatalf("invalid = %t, want %t", ok, tc.want)
			}
			if !tc.want {
				return
			}
			providerErr, classified := model.ClassifyError(got)
			if !classified || providerErr.Kind != model.ErrorKindUnknown ||
				providerErr.Source != "test-provider" || providerErr.Code != tc.code ||
				!strings.Contains(providerErr.Message, tc.message) || !model.IsAmbiguousProviderOutcome(got) {
				t.Fatalf(
					"provider error = %+v, want ambiguous unknown with code %q and message containing %q",
					got,
					tc.code,
					tc.message,
				)
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
		InputIDs:             []storage.ID{unitKernelID("input")},
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
