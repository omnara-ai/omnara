package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

type usageLookup struct {
	orgID, providerConfigID uuid.UUID
	generationID            string
}

type usageRecoveryHarness struct {
	recovery *modelUsageRecovery
	logs     *bytes.Buffer
	now      time.Time
	due      []executionstore.ModelUsageRecovery
	more     bool
	listErr  error
	writeErr error
	lookups  map[string]func() (model.GenerationUsage, error)
	mu       sync.Mutex
	looked   map[string]int
	asked    []usageLookup
	recorded []executionstore.RecordRecoveredModelCallUsageInput
}

func newUsageRecoveryHarness() *usageRecoveryHarness {
	h := &usageRecoveryHarness{
		logs: &bytes.Buffer{}, now: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC),
		lookups: map[string]func() (model.GenerationUsage, error){}, looked: map[string]int{},
	}
	h.recovery = &modelUsageRecovery{
		log: slog.New(slog.NewTextHandler(h.logs, nil)),
		list: func(
			context.Context, executionstore.ModelUsageRecoveryCursor, time.Duration, int32, []time.Duration,
		) ([]executionstore.ModelUsageRecovery, executionstore.ModelUsageRecoveryCursor, bool, error) {
			return h.due, executionstore.ModelUsageRecoveryCursor{}, h.more, h.listErr
		},
		lookupUsage: func(
			_ context.Context, orgID, providerConfigID uuid.UUID, generationID string,
		) (model.GenerationUsage, error) {
			h.mu.Lock()
			h.looked[generationID]++
			h.asked = append(h.asked, usageLookup{orgID, providerConfigID, generationID})
			h.mu.Unlock()
			return h.lookups[generationID]()
		},
		record: func(_ context.Context, input executionstore.RecordRecoveredModelCallUsageInput) (bool, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.recorded = append(h.recorded, input)
			return h.writeErr == nil, h.writeErr
		},
		now: func() time.Time { return h.now },
	}
	recoverModelUsage(h.recovery)
	return h
}

func (h *usageRecoveryHarness) call(
	providerConfigID uuid.UUID, generationID string, final bool,
) executionstore.ModelUsageRecovery {
	return executionstore.ModelUsageRecovery{
		OrgID: uuid.New(), ProjectID: uuid.New(), AgentID: uuid.New(), ModelCallContextID: uuid.New(),
		ModelProviderConfigID: providerConfigID, GenerationID: generationID, Final: final,
	}
}

func (h *usageRecoveryHarness) run() maintenanceResult {
	return h.runIn(context.Background())
}

func (h *usageRecoveryHarness) runIn(ctx context.Context) maintenanceResult {
	return h.recovery.run(logpkg.WithLogger(ctx, slog.New(logpkg.NewJSONHandler(h.logs, nil))))
}

func (h *usageRecoveryHarness) misses() []string {
	var misses []string
	for _, line := range bytes.Split(h.logs.Bytes(), []byte("\n")) {
		var event map[string]any
		if json.Unmarshal(line, &event) == nil && event["model_usage_recovery.outcome"] != nil {
			misses = append(misses, fmt.Sprintf("%v/%v/%v",
				event["model_usage_recovery.outcome"], event["model_usage_recovery.cause"], event["level"]))
		}
	}
	return misses
}

func TestModelUsageRecoveryOutcomes(t *testing.T) {
	h := newUsageRecoveryHarness()
	provider := uuid.New()
	usage := model.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 5}
	h.lookups["costed"] = func() (model.GenerationUsage, error) {
		return model.GenerationUsage{Usage: usage, ProviderReportedCostUSD: "0.01"}, nil
	}
	h.lookups["missing"] = func() (model.GenerationUsage, error) {
		return model.GenerationUsage{}, model.ErrGenerationUsageNotFound
	}

	costed := h.call(provider, "costed", false)
	h.due = []executionstore.ModelUsageRecovery{
		costed, h.call(provider, "missing", false), h.call(provider, "missing", true),
	}
	h.more = true
	require.Equal(t, maintenanceResult{worked: true, more: true}, h.run(), "a full page asks to run again")
	h.more = false
	require.Equal(t, []executionstore.RecordRecoveredModelCallUsageInput{{
		ProjectID: costed.ProjectID, AgentID: costed.AgentID, ModelCallContextID: costed.ModelCallContextID,
		ProviderResponseID: "costed", Usage: usage, ProviderReportedCostUSD: "0.01",
	}}, h.recorded)
	require.Contains(t, h.asked, usageLookup{costed.OrgID, provider, "costed"})
	require.Equal(t, []string{"gave_up/not_recorded/warn"}, h.misses(), "only the final lookup gives up")

	h.writeErr = errors.New("write failed")
	failing := h.call(provider, "costed", true)
	h.due = []executionstore.ModelUsageRecovery{failing, h.call(provider, "missing", false)}
	result := h.run()
	require.ErrorIs(t, result.err, h.writeErr, "a failed cost write fails the run")
	require.ErrorContains(t, result.err, failing.ModelCallContextID.String())
	require.Equal(t, []string{"gave_up/not_recorded/warn", "gave_up/record/error"}, h.misses(),
		"a failed write on the final lookup gives up at error")

	h.writeErr = nil
	broken := errors.New("credential rejected")
	h.lookups["broken"] = func() (model.GenerationUsage, error) { return model.GenerationUsage{}, broken }
	unexpected := h.call(provider, "broken", false)
	h.due = []executionstore.ModelUsageRecovery{unexpected, h.call(provider, "missing", false)}
	result = h.run()
	require.ErrorIs(t, result.err, broken, "an unexpected lookup failure fails the run")
	require.ErrorContains(t, result.err, unexpected.ModelCallContextID.String())
	require.Equal(t, []string{"gave_up/not_recorded/warn", "gave_up/record/error", "failed/lookup/warn"}, h.misses(),
		"a non-final failure is reported without giving up")

	h.listErr = errors.New("database unavailable")
	h.recorded = nil
	result = h.run()
	require.ErrorIs(t, result.err, h.listErr)
	require.Empty(t, h.recorded)
}

func TestModelUsageRecoveryCoolsDownRateLimitedProviders(t *testing.T) {
	h := newUsageRecoveryHarness()
	throttled, healthy := uuid.New(), uuid.New()
	sixty := int64(60)
	retryAfter := &model.RetryAfter{DeltaSeconds: &sixty}
	h.lookups["throttled"] = func() (model.GenerationUsage, error) {
		return model.GenerationUsage{}, model.ProviderError{
			Kind: model.ErrorKindRateLimit, StatusCode: http.StatusTooManyRequests, RetryAfter: retryAfter,
		}
	}
	h.lookups["healthy"] = func() (model.GenerationUsage, error) {
		return model.GenerationUsage{ProviderReportedCostUSD: "0.01"}, nil
	}
	run := func(calls ...executionstore.ModelUsageRecovery) error {
		t.Helper()
		h.due = calls
		return h.run().err
	}

	require.Error(t, run(h.call(throttled, "throttled", false)), "a 429 is reported")
	require.Equal(t, 1, h.looked["throttled"])

	h.now = h.now.Add(59 * time.Second)
	require.NoError(t, run(h.call(throttled, "throttled", false), h.call(healthy, "healthy", false)),
		"skipping a cooling-down provider is not a failure")
	require.Equal(t, 1, h.looked["throttled"], "a throttled provider is skipped until its Retry-After ends")
	require.Len(t, h.recorded, 1, "other providers keep recovering")

	h.now = h.now.Add(time.Second)
	retryAfter = nil
	run(h.call(throttled, "throttled", false))
	require.Equal(t, 2, h.looked["throttled"], "the provider is retried once its cooldown ends")

	h.now = h.now.Add(modelUsageRecoveryCooldown - time.Second)
	run(h.call(throttled, "throttled", true))
	require.Equal(t, 2, h.looked["throttled"], "a 429 without Retry-After cools down for the default")
	require.Contains(t, h.misses(), "gave_up/cooling_down/error", "a cooldown on the final lookup gives up at error")

	h.now = h.now.Add(time.Second)
	run(h.call(throttled, "throttled", false))
	require.Equal(t, 3, h.looked["throttled"])
}

func TestModelUsageRecoveryStaysQuietOnShutdown(t *testing.T) {
	h := newUsageRecoveryHarness()
	provider := uuid.New()
	canceled := model.ProviderError{Kind: model.ErrorKindTransient, Cause: context.Canceled}
	h.lookups["canceled"] = func() (model.GenerationUsage, error) { return model.GenerationUsage{}, canceled }
	h.lookups["costed"] = func() (model.GenerationUsage, error) {
		return model.GenerationUsage{ProviderReportedCostUSD: "0.01"}, nil
	}
	h.lookups["broken"] = func() (model.GenerationUsage, error) {
		return model.GenerationUsage{}, errors.New("credential rejected")
	}
	h.writeErr = fmt.Errorf("write: %w", context.Canceled)
	shutdown, cancel := context.WithCancel(context.Background())
	cancel()

	h.due = []executionstore.ModelUsageRecovery{
		h.call(provider, "canceled", false), h.call(provider, "canceled", true), h.call(provider, "costed", true),
	}
	require.Equal(t, maintenanceResult{}, h.runIn(shutdown), "a cancellation-only shutdown is not a failure")
	require.Empty(t, h.misses(), "lookups and writes cut off by shutdown are not reported as misses")

	h.due = []executionstore.ModelUsageRecovery{h.call(provider, "broken", true), h.call(provider, "canceled", true)}
	require.Error(t, h.runIn(shutdown).err, "a genuine failure during shutdown still fails the run")
	require.Equal(t, []string{"gave_up/lookup/error"}, h.misses(), "a genuine failure during shutdown is still reported")
}
