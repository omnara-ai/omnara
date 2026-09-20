package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestInboxCommandValidationAndHelp(t *testing.T) {
	t.Parallel()
	project, err := publicid.Encode(publicid.KindProject, uuid.New())
	require.NoError(t, err)
	for _, args := range [][]string{
		{}, {"delete"}, {"list"}, {"list", "--project", project, "--limit", "101"},
		{"list", "--project", project, "--after", "broken"}, {"list", "--project", project, "--state", "other"},
		{"discard", "--project", project, "--receipt", uuid.NewString()},
		{"retry", "--project", project, "--receipt", uuid.Nil.String()},
		{"show", "--project", project, "--receipt", uuid.NewString(), "unexpected"},
	} {
		_, err := parseInboxCommand(args, io.Discard)
		require.Error(t, err, "args=%v", args)
	}
	var help bytes.Buffer
	require.NoError(t, runInboxCLI(t.Context(), []string{"inbox", "discard", "--help"}, io.Discard, &help))
	require.Contains(t, help.String(), "-reason")
	require.Contains(t, help.String(), "-receipt")
}

func TestInboxShowPublicationStateWithoutRawDisclosure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, preparation, scheduled string
		source                       integrationstore.IntegrationInboxSource
	}{
		{
			name: "provider", source: integrationstore.IntegrationInboxSourceProvider,
			preparation: `{"attempted_at":"private-provider-preparation"}`,
		},
		{
			name: "not attempted", source: integrationstore.IntegrationInboxSourceScheduledLaunch,
			preparation: `{}`, scheduled: `{"root_known":false}`,
		},
		{
			name: "uncertain publication", source: integrationstore.IntegrationInboxSourceScheduledLaunch,
			preparation: `{"attempted_at":"2026-09-20T09:00:00Z","heading":"private-heading"}`,
			scheduled:   `{"attempted_at":"2026-09-20T09:00:00Z","root_known":false}`,
		},
		{
			name: "known root", source: integrationstore.IntegrationInboxSourceScheduledLaunch,
			preparation: `{"attempted_at":"2026-09-20T09:00:00Z",` +
				`"root":{"slack":{"channel_id":"C-PRIVATE-CHANNEL","thread_ts":"private-root"}},` +
				`"credentials":"private-credentials","heading":"private-heading"}`,
			scheduled: `{"attempted_at":"2026-09-20T09:00:00Z","root_known":true}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := inboxInspectionRecord()
			record.Source = tc.source
			record.Preparation = json.RawMessage(tc.preparation)
			command := inboxCommand{action: "show", projectID: record.ProjectID, receiptID: record.ID}
			var output bytes.Buffer
			require.NoError(t, command.run(t.Context(), inboxReadStore{record}, &output))
			var shown map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(output.Bytes(), &shown))
			var source integrationstore.IntegrationInboxSource
			require.NoError(t, json.Unmarshal(shown["source"], &source))
			require.Equal(t, tc.source, source)
			if tc.scheduled == "" {
				require.NotContains(t, shown, "scheduled")
			} else {
				require.JSONEq(t, tc.scheduled, string(shown["scheduled"]))
			}
			require.JSONEq(t, `[{"key":"slot","prepared":true,"committed":false}]`, string(shown["slots"]))
			for _, hidden := range []string{
				"private-payload", "private-config", "private-message", "private-preparation", "private-event",
				"private-credentials", "private-heading", "C-PRIVATE-CHANNEL", "private-root", "private-provider-preparation",
			} {
				require.NotContains(t, output.String(), hidden)
			}
			for _, rawField := range []string{"payload", "plan", "progress", "preparation", "events", "credentials"} {
				require.NotContains(t, shown, rawField)
			}
		})
	}
}

func TestInboxShowInvalidPublicationStateDoesNotDiscloseRawValues(t *testing.T) {
	t.Parallel()
	for _, preparation := range []string{
		`{"attempted_at":"private-invalid-timestamp"}`,
		`{"attempted_at":"2026-09-20T09:00:00Z","root":{"slack":{"channel_id":42}}}`,
		`{"root":{"slack":{"channel_id":"private-channel"}}}`,
		`{"private-preparation":`,
	} {
		record := inboxInspectionRecord()
		record.Preparation = json.RawMessage(preparation)
		command := inboxCommand{action: "show", projectID: record.ProjectID, receiptID: record.ID}
		var output bytes.Buffer
		err := command.run(t.Context(), inboxReadStore{record}, &output)
		require.EqualError(t, err, "decode scheduled publication state")
		require.Empty(t, output.String())
	}
}

func TestInboxListOmitsPublicationDetails(t *testing.T) {
	t.Parallel()
	record := inboxInspectionRecord()
	command := inboxCommand{action: "list", list: integrationstore.ListIntegrationInboxInput{ProjectID: record.ProjectID}}
	var output bytes.Buffer
	require.NoError(t, command.run(t.Context(), inboxReadStore{record}, &output))
	var page struct {
		Receipts []map[string]json.RawMessage `json:"receipts"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &page))
	require.Len(t, page.Receipts, 1)
	require.NotContains(t, page.Receipts[0], "source")
	require.NotContains(t, page.Receipts[0], "scheduled")
	require.NotContains(t, output.String(), "private-")
}

func inboxInspectionRecord() integrationstore.IntegrationInboxRecord {
	now := time.Date(2026, time.September, 20, 9, 0, 0, 0, time.UTC)
	return integrationstore.IntegrationInboxRecord{
		IntegrationInboxSummary: integrationstore.IntegrationInboxSummary{
			ID: uuid.New(), ProjectID: uuid.New(), AppID: uuid.New(), ReceiptKey: "scheduled-receipt",
			State: integrationstore.IntegrationInboxFailed, AttemptCount: 1,
			CreatedAt: now, UpdatedAt: now, AvailableAt: now,
		},
		Source:      integrationstore.IntegrationInboxSourceScheduledLaunch,
		Preparation: json.RawMessage(`{}`),
		Payload:     []byte(`{"token":"private-payload","opening_message":"private-heading"}`),
		Events:      json.RawMessage(`[{"text":"private-event"}]`),
		Plan: json.RawMessage(`{"slot":{"launch":{"compiled_config":"private-config"},` +
			`"input":{"text":"private-message"}}}`),
		Progress: json.RawMessage(`{"slot":{"prepared":{"private":"private-preparation"}}}`),
	}
}

type inboxReadStore struct {
	record integrationstore.IntegrationInboxRecord
}

func (s inboxReadStore) ListIntegrationInbox(
	context.Context, integrationstore.ListIntegrationInboxInput,
) (integrationstore.ListIntegrationInboxResult, error) {
	return integrationstore.ListIntegrationInboxResult{
		Receipts: []integrationstore.IntegrationInboxSummary{s.record.IntegrationInboxSummary},
	}, nil
}

func (s inboxReadStore) GetIntegrationInbox(
	context.Context, uuid.UUID, uuid.UUID,
) (integrationstore.IntegrationInboxRecord, error) {
	return s.record, nil
}

func (inboxReadStore) RetryFailedIntegrationInbox(context.Context, uuid.UUID, uuid.UUID) error {
	return errors.New("unexpected retry")
}

func (inboxReadStore) DiscardFailedIntegrationInbox(context.Context, uuid.UUID, uuid.UUID) error {
	return errors.New("unexpected discard")
}
