package appstore

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
		_, err := (&Store{}).CleanupTerminalAppInbox(t.Context(), retention, 100)
		if err == nil {
			t.Fatalf("accepted invalid retention %s", retention)
		}
	}
	for _, limit := range []int{-1, 0, AppInboxMaxBatch + 1} {
		if _, err := (&Store{}).CleanupTerminalAppInbox(t.Context(), time.Hour, limit); err == nil {
			t.Fatalf("accepted completed cleanup limit %d", limit)
		}
		if _, err := (&Store{}).CleanupDeletedAppInbox(t.Context(), limit); err == nil {
			t.Fatalf("accepted deleted cleanup limit %d", limit)
		}
	}
}

func TestInboxReceiptBounds(t *testing.T) {
	valid := VerifiedAppReceipt{
		ProjectID: uuid.New(), AppID: uuid.New(), ReceiptKey: "event:1", Payload: []byte{0, 255, 1},
	}
	if err := validateAppReceipt(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*VerifiedAppReceipt){
		func(v *VerifiedAppReceipt) { v.ProjectID = uuid.Nil },
		func(v *VerifiedAppReceipt) { v.AppID = uuid.Nil },
		func(v *VerifiedAppReceipt) { v.ReceiptKey = "  " },
		func(v *VerifiedAppReceipt) { v.ReceiptKey = "bad\x00key" },
		func(v *VerifiedAppReceipt) { v.ReceiptKey = string([]byte{255}) },
		func(v *VerifiedAppReceipt) {
			v.ReceiptKey = strings.Repeat("x", AppInboxMaxReceiptKeyBytes+1)
		},
		func(v *VerifiedAppReceipt) { v.Payload = nil },
		func(v *VerifiedAppReceipt) { v.Payload = make([]byte, AppInboxMaxPayloadBytes+1) },
	} {
		value := valid
		mutate(&value)
		if err := validateAppReceipt(value); !errors.Is(err, storeerr.ErrInvalidRequest) {
			t.Fatalf("invalid receipt error = %v", err)
		}
	}
	valid.Payload = make([]byte, AppInboxMaxPayloadBytes)
	valid.ReceiptKey = strings.Repeat("x", AppInboxMaxReceiptKeyBytes)
	if err := validateAppReceipt(valid); err != nil {
		t.Fatal(err)
	}
}

func TestInboxSelectionRequiresReceiptApp(t *testing.T) {
	appID := uuid.New()
	selection := InboxAppSelection{
		AppID: appID, Address: ConversationAddress{Kind: "thread", Ref: "C123:1.2"}, Slot: "default",
	}
	plan, err := json.Marshal(map[string]any{"one": map[string]any{"selection": selection}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inboxSelectionIdentities(plan, appID); err != nil {
		t.Fatal(err)
	}
	if _, err := inboxSelectionIdentities(plan, uuid.New()); !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("another app's selection accepted: %v", err)
	}
	duplicate, err := json.Marshal(map[string]any{
		"one": map[string]any{"selection": selection}, "two": map[string]any{"selection": selection},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inboxSelectionIdentities(duplicate, appID); !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("duplicate selection slot accepted: %v", err)
	}
	_, _, err = ensureAppProfileChoiceTx(t.Context(), nil,
		AppInboxRecord{AppID: appID},
		EnsureAppProfileChoiceInput{AppID: uuid.New()})
	if !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("another app's chooser accepted: %v", err)
	}
}

func TestAppRuntimeRequiresPositiveSetupRevision(t *testing.T) {
	valid := AppRuntimeRevision{
		ProjectID: uuid.New(), AppID: uuid.New(), Key: "shard/0", SetupRevision: 1, CredentialVersionID: uuid.New(),
	}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{-1, 0} {
		invalid := valid
		invalid.SetupRevision = revision
		if err := invalid.validate(); !errors.Is(err, storeerr.ErrInvalidRequest) {
			t.Fatalf("invalid setup revision %d accepted: %v", revision, err)
		}
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
		`{"one":{"data":"` + strings.Repeat("x", AppInboxMaxPlanBytes) + `"}}`,
	} {
		if _, err := inboxSlots(json.RawMessage(raw)); !errors.Is(err, storeerr.ErrInvalidRequest) {
			t.Fatalf("invalid plan accepted: %v", err)
		}
	}
}

func TestInboxErrorAlwaysBoundedAndStorable(t *testing.T) {
	for _, raw := range []string{
		"", " \x00 ", string([]byte{255}), strings.Repeat("🙂", AppInboxMaxErrorBytes),
		strings.Repeat("x", AppInboxMaxErrorBytes-1) + "🙂",
	} {
		got := boundedInboxError(raw)
		if got == "" || len(got) > AppInboxMaxErrorBytes || !utf8.ValidString(got) || strings.ContainsRune(got, 0) {
			t.Fatalf("invalid stored diagnostic of length %d", len(got))
		}
	}
}
