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

func TestInboxShowDoesNotDiscloseRawContent(t *testing.T) {
	t.Parallel()
	record := inboxInspectionRecord()
	command := inboxCommand{action: "show", projectID: record.ProjectID, receiptID: record.ID}
	var output bytes.Buffer
	require.NoError(t, command.run(t.Context(), inboxReadStore{record}, &output))
	var shown map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(output.Bytes(), &shown))
	require.JSONEq(t, `"scheduled"`, string(shown["source"]))
	require.JSONEq(t, `[{"key":"slot","prepared":true,"committed":false}]`, string(shown["slots"]))
	require.NotContains(t, output.String(), "private-")
	for _, field := range []string{"payload", "plan", "progress", "events", "credentials"} {
		require.NotContains(t, shown, field)
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
		Source:  integrationstore.IntegrationInboxSourceScheduled,
		Payload: []byte(`{"token":"private-payload","opening_message":"private-heading"}`),
		Events:  json.RawMessage(`[{"text":"private-event"}]`),
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
