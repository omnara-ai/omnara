package integrationstore

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestInboxRetentionRejectsInvalidPolicyBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()
	for _, retention := range []time.Duration{-time.Hour, 0, time.Nanosecond, time.Second - 1} {
		_, err := (&Store{}).CleanupTerminalIntegrationInbox(t.Context(), retention, 100)
		if err == nil {
			t.Fatalf("accepted invalid retention %s", retention)
		}
	}
	for _, limit := range []int{-1, 0, IntegrationInboxMaxBatch + 1} {
		if _, err := (&Store{}).CleanupTerminalIntegrationInbox(t.Context(), time.Hour, limit); err == nil {
			t.Fatalf("accepted completed cleanup limit %d", limit)
		}
		if _, err := (&Store{}).CleanupDeletedIntegrationInbox(t.Context(), limit); err == nil {
			t.Fatalf("accepted deleted cleanup limit %d", limit)
		}
	}
}

func TestInboxReceiptBounds(t *testing.T) {
	valid := VerifiedIntegrationReceipt{
		ProjectID: uuid.New(), ConnectionID: uuid.New(), ReceiptKey: "event:1", Payload: []byte{0, 255, 1},
	}
	if err := validateIntegrationReceipt(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*VerifiedIntegrationReceipt){
		func(v *VerifiedIntegrationReceipt) { v.ProjectID = uuid.Nil },
		func(v *VerifiedIntegrationReceipt) { v.ConnectionID = uuid.Nil },
		func(v *VerifiedIntegrationReceipt) { v.ReceiptKey = "  " },
		func(v *VerifiedIntegrationReceipt) { v.ReceiptKey = "bad\x00key" },
		func(v *VerifiedIntegrationReceipt) { v.ReceiptKey = string([]byte{255}) },
		func(v *VerifiedIntegrationReceipt) {
			v.ReceiptKey = strings.Repeat("x", IntegrationInboxMaxReceiptKeyBytes+1)
		},
		func(v *VerifiedIntegrationReceipt) { v.Payload = nil },
		func(v *VerifiedIntegrationReceipt) { v.Payload = make([]byte, IntegrationInboxMaxPayloadBytes+1) },
	} {
		value := valid
		mutate(&value)
		if err := validateIntegrationReceipt(value); !errors.Is(err, storeerr.ErrInvalidRequest) {
			t.Fatalf("invalid receipt error = %v", err)
		}
	}
	valid.Payload = make([]byte, IntegrationInboxMaxPayloadBytes)
	valid.ReceiptKey = strings.Repeat("x", IntegrationInboxMaxReceiptKeyBytes)
	if err := validateIntegrationReceipt(valid); err != nil {
		t.Fatal(err)
	}
}

func TestInboxPlanShapeAndBounds(t *testing.T) {
	for _, raw := range []string{`{}`, `{"one":{"agent_id":"pinned"},"two":{"config_id":"pinned"}}`} {
		if _, err := inboxSlots(json.RawMessage(raw)); err != nil {
			t.Fatalf("valid plan %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"", `null`, `[]`, `{"one":null}`, `{"one":[]}`, `{" ":{}}`, `{} {}`,
		`{"one":{"data":"` + strings.Repeat("x", IntegrationInboxMaxPlanBytes) + `"}}`,
	} {
		if _, err := inboxSlots(json.RawMessage(raw)); !errors.Is(err, storeerr.ErrInvalidRequest) {
			t.Fatalf("invalid plan accepted: %v", err)
		}
	}
}

func TestInboxErrorAlwaysBoundedAndStorable(t *testing.T) {
	for _, raw := range []string{
		"", " \x00 ", string([]byte{255}), strings.Repeat("🙂", IntegrationInboxMaxErrorBytes),
		strings.Repeat("x", IntegrationInboxMaxErrorBytes-1) + "🙂",
	} {
		got := boundedInboxError(raw)
		if got == "" || len(got) > IntegrationInboxMaxErrorBytes || !utf8.ValidString(got) || strings.ContainsRune(got, 0) {
			t.Fatalf("invalid stored diagnostic of length %d", len(got))
		}
	}
}
