package integrationstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestControlReceiptRejectsUnsafePayloadBeforeDatabase(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"empty": "", "null": "null", "array": "[]", "trailing": `{} {}`,
		"nested_duplicate": `{"nested":{"a":1,"\u0061":2}}`,
		"nul":              `{"x":"\u0000"}`, "invalid_utf8": "{\"x\":\"\xff\"}",
		"numeric_amplification": `{"x":1e70000}`,
		"oversize":              `{"x":"` + strings.Repeat("a", MaxIntegrationControlPayloadBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var store Store
			_, err := store.ReceiveIntegrationControl(t.Context(), ReceiveIntegrationControlInput{
				IntegrationAppID: uuid.New(), ProviderTenantID: "42", EventID: "delivery", Payload: json.RawMessage(raw),
			})
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
		})
	}
}

func TestControlReceiptFinishValidation(t *testing.T) {
	t.Parallel()
	valid := FinishIntegrationControlInput{
		IntegrationAppID: uuid.New(), ID: uuid.New(), LeaseToken: uuid.New(), LeaseGeneration: 1,
		Outcome: IntegrationControlCompleted,
	}
	for name, change := range map[string]func(*FinishIntegrationControlInput){
		"missing_scope":          func(i *FinishIntegrationControlInput) { i.IntegrationAppID = uuid.Nil },
		"unknown_outcome":        func(i *FinishIntegrationControlInput) { i.Outcome = "restart" },
		"yield_without_progress": func(i *FinishIntegrationControlInput) { i.Outcome = IntegrationControlYield },
		"retry_without_error":    func(i *FinishIntegrationControlInput) { i.Outcome = IntegrationControlRetry },
		"success_with_error":     func(i *FinishIntegrationControlInput) { i.LastError = json.RawMessage(`{"code":"x"}`) },
		"success_with_delay":     func(i *FinishIntegrationControlInput) { i.RetryAfter = time.Second },
		"negative_delay": func(i *FinishIntegrationControlInput) {
			i.Outcome, i.LastError, i.RetryAfter = IntegrationControlRetry, json.RawMessage(`{"code":"x"}`), -time.Second
		},
		"ambiguous_error": func(i *FinishIntegrationControlInput) {
			i.Outcome, i.LastError = IntegrationControlRetry, json.RawMessage(`{"code":1,"code":2}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := valid
			change(&input)
			var store Store
			_, err := store.FinishIntegrationControl(t.Context(), input)
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
		})
	}
}

func TestControlReceiptMaintenanceAndLeaseBounds(t *testing.T) {
	t.Parallel()
	var store Store
	for _, limit := range []int{-1, 0, 1001} {
		_, err := store.FailUnprocessableIntegrationControls(t.Context(), limit)
		require.Error(t, err)
		_, err = store.DeleteRetainedIntegrationControls(t.Context(), DeleteRetainedIntegrationControlsInput{
			Retention: time.Hour, Limit: limit,
		})
		require.Error(t, err)
	}
	for _, duration := range []time.Duration{-time.Second, 0, time.Nanosecond, 6 * time.Minute} {
		_, _, err := store.ClaimNextIntegrationControl(t.Context(), ClaimNextIntegrationControlInput{LeaseDuration: duration})
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	}
	_, err := store.DeleteRetainedIntegrationControls(t.Context(), DeleteRetainedIntegrationControlsInput{
		Retention: time.Nanosecond, Limit: 1,
	})
	require.Error(t, err)
}
